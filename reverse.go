package geocode

import (
	"context"
	"errors"
	"math"
)

type visit struct {
	id       uint32
	distance float64
}
type visitHeap []visit

func (h *visitHeap) push(v visit) {
	a := append(*h, v)
	i := len(a) - 1
	for i > 0 {
		p := (i - 1) / 2
		if a[p].distance <= v.distance {
			break
		}
		a[i] = a[p]
		i = p
	}
	a[i] = v
	*h = a
}
func (h *visitHeap) pop() visit {
	a := *h
	v := a[0]
	last := a[len(a)-1]
	a = a[:len(a)-1]
	if len(a) > 0 {
		i := 0
		for {
			child := i*2 + 1
			if child >= len(a) {
				break
			}
			if child+1 < len(a) && a[child+1].distance < a[child].distance {
				child++
			}
			if last.distance <= a[child].distance {
				break
			}
			a[i] = a[child]
			i = child
		}
		a[i] = last
	}
	*h = a
	return v
}

// Reverse uses best-first branch-and-bound traversal of the packed spatial tree.
// Distances refer to geometry, including polygon interiors, in metres.
func (db *DB) Reverse(ctx context.Context, p Point, opts ReverseOptions) ([]Result, error) {
	db.mu.RLock()
	defer db.mu.RUnlock()
	if db.closed {
		return nil, ErrClosed
	}
	if err := checkContext(ctx); err != nil {
		return nil, err
	}
	if !validPoint(p) {
		return nil, ErrInvalidCoordinate
	}
	radius := opts.RadiusMeters
	if radius == 0 {
		radius = 1000
	}
	if math.IsNaN(radius) || radius < 0 || radius > math.Pi*earthRadius {
		return nil, errors.New("geocode: invalid radius")
	}
	if db.count == 0 {
		return []Result{}, nil
	}
	k := queryLimit(opts.Limit)
	best := make(matches, 0, k)
	metric := newQueryMetric(p)
	queue := visitHeap{{0, metric.lowerBound(db.treeNode(0).bounds)}}
	cutoff := radius
	steps := 0
	for len(queue) > 0 {
		if steps%128 == 0 {
			if err := checkContext(ctx); err != nil {
				return nil, err
			}
		}
		steps++
		v := queue.pop()
		if v.distance > cutoff {
			break
		}
		n := db.treeNode(v.id)
		if n.count == 0 {
			for _, id := range []uint32{n.left, n.right} {
				d := metric.lowerBound(db.treeNode(id).bounds)
				if d <= cutoff {
					queue.push(visit{id, d})
				}
			}
			continue
		}
		for j := n.start; j < n.start+n.count; j++ {
			id := le.Uint32(db.sections[sOrder][int(j)*4:])
			r := db.record(id)
			if r[10] > 0 && opts.Class != "boundary" {
				continue
			}
			if metric.lowerBound(readBox(r[24:])) > cutoff {
				continue
			}
			if opts.Class != "" {
				matches, err := db.hasClass(r, opts.Class)
				if err != nil {
					return nil, err
				}
				if !matches {
					continue
				}
			}
			d, err := db.distanceWithin(id, p, cutoff)
			if err != nil {
				return nil, err
			}
			if d > radius || d > cutoff {
				continue
			}
			// A sub-micrometre preference resolves co-located geometries without
			// allowing a farther feature to beat a meaningfully nearer one.
			score := -d + float64(math.Float32frombits(le.Uint32(r[12:])))*1e-7
			best.offer(match{id: id, score: score, distance: d}, k)
			if len(best) == k {
				cutoff = min(radius, best[0].distance+1e-6)
			}
		}
	}
	return db.results(best, true)
}

// distance reads coordinates directly from the mapping. No geometry slices or
// feature JSON are allocated on the reverse-query candidate path.
func (db *DB) distance(id uint32, p Point) (float64, error) {
	return db.distanceWithin(id, p, math.Inf(1))
}

func (db *DB) distanceWithin(id uint32, p Point, cutoff float64) (float64, error) {
	r := db.record(id)
	off, n := le.Uint64(r[40:]), uint64(le.Uint32(r[48:]))
	if n == 0 {
		return haversine(p, recordPoint(r)), nil
	}
	b := db.sections[sGeometry][off : off+n]
	if len(b) < 4 {
		return 0, corrupt("geometry header")
	}
	paths := int(le.Uint32(b))
	b = b[4:]
	if paths > len(b)/4 {
		return 0, corrupt("geometry paths")
	}
	d := math.Inf(1)
	inside := false
	bb := readBox(r[24:])
	maxLat := max(math.Abs(p.Lat), max(math.Abs(float64(bb.south)/coordScale), math.Abs(float64(bb.north)/coordScale)))
	lonScale := earthRadius * 2 / 180 * math.Cos(radians(maxLat))
	latScale := earthRadius * math.Pi / 180
	// Longitude's conservative chord bound and latitude's exact angular bound
	// reject distant segments before any per-segment trigonometry. Containment
	// still processes every crossing, including segments outside the radius.
	for range paths {
		if len(b) < 4 {
			return 0, corrupt("geometry path")
		}
		count := int(le.Uint32(b))
		b = b[4:]
		if count > len(b)/8 {
			return 0, corrupt("geometry points")
		}
		var prev, first Point
		for j := range count {
			q := Point{float64(int32(le.Uint32(b[j*8:]))) / coordScale, float64(int32(le.Uint32(b[j*8+4:]))) / coordScale}
			if j == 0 {
				first = q
				d = min(d, haversine(p, q))
			} else {
				limit := min(cutoff, d)
				latGap := max(0, max(min(prev.Lat, q.Lat)-p.Lat, p.Lat-max(prev.Lat, q.Lat)))
				if latGap*latScale <= limit {
					ax, bx := wrap(prev.Lon-p.Lon), wrap(q.Lon-p.Lon)
					lonGap := 0.0
					if math.Abs(ax-bx) <= 180 {
						lonGap = max(0, max(min(ax, bx), -max(ax, bx)))
					}
					if lonGap*lonScale <= limit {
						d = min(d, segmentDistance(p, prev, q))
					}
				}
				if r[9]&1 != 0 && rayCrossReference(p, prev, q, first.Lon) {
					inside = !inside
				}
			}
			prev = q
		}
		if r[9]&1 != 0 && count > 1 && first != prev && rayCrossReference(p, prev, first, first.Lon) {
			inside = !inside
		}
		b = b[count*8:]
	}
	if len(b) != 0 {
		return 0, corrupt("geometry trailing bytes")
	}
	if inside {
		return 0, nil
	}
	return d, nil
}
func rayCrossReference(p, a, b Point, reference float64) bool {
	if (a.Lat > p.Lat) == (b.Lat > p.Lat) {
		return false
	}
	ax, bx := wrap(a.Lon-reference), wrap(b.Lon-reference)
	x := wrap(p.Lon - reference)
	return x < (bx-ax)*(p.Lat-a.Lat)/(b.Lat-a.Lat)+ax
}
