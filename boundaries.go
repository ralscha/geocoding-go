package geocode

import (
	"context"
	"math"
	"sort"
	"sync"

	"github.com/paulmach/osm"
)

type wayShape struct {
	first, last int64
	points      []Point
}

func assembleRelation(r *osm.Relation, shapes map[int64]wayShape) ([][]Point, bool) {
	var all [][]Point
	hasOuter := false
	for _, inner := range []bool{false, true} {
		var ways []wayShape
		for _, m := range r.Members {
			if m.Role != "outer" && m.Role != "inner" && m.Role != "" {
				continue
			}
			if m.Type == osm.TypeRelation {
				return nil, false
			} // no guessed nested geometry
			if m.Type != osm.TypeWay || (m.Role == "inner") != inner {
				continue
			}
			w, ok := shapes[m.Ref]
			if !ok {
				return nil, false
			}
			ways = append(ways, w)
		}
		adj := make(map[int64][]int, len(ways)*2)
		for i, w := range ways {
			adj[w.first] = append(adj[w.first], i)
			adj[w.last] = append(adj[w.last], i)
		}
		used := make([]bool, len(ways))
		for i, w := range ways {
			if used[i] {
				continue
			}
			used[i] = true
			ring := append([]Point(nil), w.points...)
			end := w.last
			for end != w.first {
				next := -1
				for _, j := range adj[end] {
					if !used[j] {
						if next >= 0 {
							return nil, false
						}
						next = j
					}
				}
				if next < 0 {
					return nil, false
				}
				used[next] = true
				v := ways[next]
				if v.first == end {
					ring = append(ring, v.points[1:]...)
					end = v.last
				} else {
					for j := len(v.points) - 2; j >= 0; j-- {
						ring = append(ring, v.points[j])
					}
					end = v.first
				}
			}
			if len(ring) < 4 {
				return nil, false
			}
			all = append(all, ring)
			if !inner {
				hasOuter = true
			}
		}
	}
	return all, hasOuter
}

func polygonLabel(p []Point, lat float64) Point { return ringsLabel([][]Point{p}, lat) }
func ringsLabel(rings [][]Point, lat float64) Point {
	if len(rings) == 0 || len(rings[0]) == 0 {
		return Point{}
	}
	ref := rings[0][0].Lon
	var crossings []float64
	for _, p := range rings {
		for i := 1; i < len(p); i++ {
			a, b := p[i-1], p[i]
			if (a.Lat > lat) != (b.Lat > lat) {
				ax, bx := wrap(a.Lon-ref), wrap(b.Lon-ref)
				crossings = append(crossings, ax+(bx-ax)*(lat-a.Lat)/(b.Lat-a.Lat))
			}
		}
	}
	sort.Float64s(crossings)
	best := -1.0
	point := rings[0][0]
	for i := 0; i+1 < len(crossings); i += 2 {
		if w := crossings[i+1] - crossings[i]; w > best {
			best = w
			point = Point{lat, wrap(ref + (crossings[i]+crossings[i+1])/2)}
		}
	}
	return point
}

type preparedEdge struct {
	a, b      Point
	reference float64
}
type preparedBoundary struct {
	feature int
	bounds  box
	buckets [][]preparedEdge
}

func prepareBoundary(id int, f Feature) preparedBoundary {
	p := preparedBoundary{feature: id, bounds: featureBox(f)}
	edges := 0
	for _, r := range f.Geometry {
		edges += len(r)
	}
	bins := min(2048, max(16, int(math.Sqrt(float64(edges)))*4))
	p.buckets = make([][]preparedEdge, bins)
	for _, r := range f.Geometry {
		if len(r) == 0 {
			continue
		}
		for i := 1; i < len(r); i++ {
			a, b := r[i-1], r[i]
			if a.Lat == b.Lat {
				continue
			}
			lo, hi := p.bucket(min(a.Lat, b.Lat)), p.bucket(max(a.Lat, b.Lat))
			edge := preparedEdge{a, b, r[0].Lon}
			for j := lo; j <= hi; j++ {
				p.buckets[j] = append(p.buckets[j], edge)
			}
		}
	}
	return p
}
func (p preparedBoundary) bucket(lat float64) int {
	height := float64(p.bounds.north) - float64(p.bounds.south)
	if height == 0 {
		return 0
	}
	return max(0, min(len(p.buckets)-1, int((lat*coordScale-float64(p.bounds.south))/height*float64(len(p.buckets)))))
}
func (p preparedBoundary) contains(q Point) bool {
	if !p.bounds.contains(q) {
		return false
	}
	inside := false
	for _, e := range p.buckets[p.bucket(q.Lat)] {
		if rayCrossReference(q, e.a, e.b, e.reference) {
			inside = !inside
		}
	}
	return inside
}

func enrichAddresses(ctx context.Context, features []Feature, opts ImportOptions, workers int) error {
	var boundaries []preparedBoundary
	var boxes []box
	for i, f := range features {
		if f.AdminLevel > 0 && f.Polygon {
			p := prepareBoundary(i, f)
			boundaries = append(boundaries, p)
			boxes = append(boxes, p.bounds)
		}
	}
	tree := buildTree(boxes)
	// Boundary metadata is copied before parallel mutation of feature addresses.
	admins := make([]Feature, len(boundaries))
	for i, p := range boundaries {
		admins[i] = features[p.feature]
	}
	jobs := make(chan [2]int, workers)
	var wg sync.WaitGroup
	for range workers {
		wg.Go(func() {
			stack := make([]uint32, 0, 32)
			found := make([]int, 0, 12)
			for job := range jobs {
				for i := job[0]; i < job[1]; i++ {
					if ctx.Err() != nil {
						return
					}
					f := &features[i]
					a := &f.Address
					found = found[:0]
					stack = stack[:0]
					if len(tree.nodes) > 0 {
						stack = append(stack, 0)
					}
					for len(stack) > 0 {
						id := stack[len(stack)-1]
						stack = stack[:len(stack)-1]
						n := tree.nodes[id]
						if !n.bounds.contains(f.Point) {
							continue
						}
						if n.count == 0 {
							stack = append(stack, n.left, n.right)
							continue
						}
						for _, j := range tree.order[n.start : n.start+n.count] {
							if boundaries[j].contains(f.Point) {
								found = append(found, int(j))
							}
						}
					}
					sort.Slice(found, func(i, j int) bool {
						a, b := admins[found[i]], admins[found[j]]
						if a.AdminLevel == b.AdminLevel {
							return a.OSMID < b.OSMID
						}
						return a.AdminLevel < b.AdminLevel
					})
					for _, j := range found {
						b := admins[j]
						switch {
						case b.AdminLevel == 2:
							if a.Country == "" {
								a.Country = b.Name
							}
							if a.CountryCode == "" {
								a.CountryCode = b.Address.CountryCode
							}
						case b.AdminLevel == 4:
							if a.State == "" {
								a.State = b.Name
							}
						case b.AdminLevel >= 5 && b.AdminLevel <= 6:
							if a.County == "" {
								a.County = b.Name
							}
						case b.AdminLevel >= 7 && b.AdminLevel <= 8:
							if a.City == "" {
								a.City = b.Name
							}
						}
					}
					if a.Country == "" {
						a.Country = opts.Country
					}
					if a.CountryCode == "" {
						a.CountryCode = opts.CountryCode
					}
				}
			}
		})
	}
	for lo := 0; lo < len(features); lo += 1024 {
		select {
		case jobs <- [2]int{lo, min(lo+1024, len(features))}:
		case <-ctx.Done():
			close(jobs)
			wg.Wait()
			return ctx.Err()
		}
	}
	close(jobs)
	wg.Wait()
	return checkContext(ctx)
}
