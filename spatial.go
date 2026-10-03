package geocode

import (
	"math"
	"sort"
)

const coordScale = 1e7

type box struct{ south, west, north, east int32 }

func quant(v float64) int32 { return int32(math.Round(v * coordScale)) }
func pointBox(p Point) box  { return box{quant(p.Lat), quant(p.Lon), quant(p.Lat), quant(p.Lon)} }
func (a box) union(b box) box {
	return box{min(a.south, b.south), min(a.west, b.west), max(a.north, b.north), max(a.east, b.east)}
}
func (b box) contains(p Point) bool {
	return p.Lat >= float64(b.south)/coordScale && p.Lat <= float64(b.north)/coordScale && p.Lon >= float64(b.west)/coordScale && p.Lon <= float64(b.east)/coordScale
}
func featureBox(f Feature) box {
	b := pointBox(f.Point)
	for _, path := range f.Geometry {
		for i, p := range path {
			b = b.union(pointBox(p))
			if i > 0 && math.Abs(p.Lon-path[i-1].Lon) > 180 {
				b.west = -1800000000
				b.east = 1800000000
			}
		}
	}
	return b
}
func radians(x float64) float64 { return x * math.Pi / 180 }
func wrap(x float64) float64 {
	if x >= -180 && x <= 180 {
		return x
	}
	if x > 180 && x <= 540 {
		return x - 360
	}
	if x < -180 && x >= -540 {
		return x + 360
	}
	return math.Remainder(x, 360)
}
func haversine(a, b Point) float64 {
	x, y := radians(b.Lat-a.Lat)/2, radians(wrap(b.Lon-a.Lon))/2
	h := math.Sin(x)*math.Sin(x) + math.Cos(radians(a.Lat))*math.Cos(radians(b.Lat))*math.Sin(y)*math.Sin(y)
	return 2 * earthRadius * math.Asin(math.Sqrt(min(1, max(0, h))))
}

// lowerBound is a conservative spherical distance to a latitude/longitude box.
// It accounts for longitude wrap and the closest latitude on a meridian.
func (b box) lowerBound(p Point) float64 {
	return newQueryMetric(p).lowerBound(b)
}

// The query latitude's trigonometric values are shared by every spatial node.
type queryMetric struct {
	point               Point
	lat, sinLat, cosLat float64
}

func newQueryMetric(p Point) queryMetric {
	lat := radians(p.Lat)
	s, c := math.Sincos(lat)
	return queryMetric{p, lat, s, c}
}
func (q queryMetric) lowerBound(b box) float64 {
	p := q.point
	w, e := float64(b.west)/coordScale, float64(b.east)/coordScale
	south, north := float64(b.south)/coordScale, float64(b.north)/coordScale
	if p.Lon >= w && p.Lon <= e {
		return max(0, earthRadius*radians(max(south-p.Lat, p.Lat-north))-0.01)
	}
	dlon := 0.0
	if p.Lon < w || p.Lon > e {
		dlon = min(math.Abs(wrap(p.Lon-w)), math.Abs(wrap(p.Lon-e)))
	}
	s, n := radians(south), radians(north)
	sinLon, cosLon := math.Sincos(radians(dlon))
	a, c := q.sinLat, q.cosLat*cosLon
	sinS, cosS := math.Sincos(s)
	sinN, cosN := math.Sincos(n)
	// On [-pi/2,pi/2], the interior maximum exists only for c >= 0.
	// Derivative signs locate it without calculating atan2 and another sin/cos.
	if c >= 0 && a*cosS-c*sinS >= 0 && a*cosN-c*sinN <= 0 {
		return max(0, earthRadius*math.Asin(min(1, math.Abs(q.cosLat*sinLon)))-0.01)
	}
	corner, cosCorner := s, cosS
	if a*sinN+c*cosN > a*sinS+c*cosS {
		corner, cosCorner = n, cosN
	}
	// Haversine remains stable for centimetre-scale boxes. acos(dot) can round
	// a centimetre distance up to ~10 cm and incorrectly prune a near feature.
	sinLatHalf := math.Sin((q.lat - corner) / 2)
	sinLonHalf := math.Sin(radians(dlon) / 2)
	h := sinLatHalf*sinLatHalf + q.cosLat*cosCorner*sinLonHalf*sinLonHalf
	return max(0, 2*earthRadius*math.Asin(math.Sqrt(min(1, max(0, h))))-0.01)
}

// segmentDistance uses a local equirectangular projection to find a point on the
// OSM segment, then measures its spherical distance. Segments interpolate in
// latitude/longitude (not great-circle arcs), matching stored bounding boxes.
func segmentDistance(p, a, b Point) float64 {
	ax, bx := wrap(a.Lon-p.Lon), wrap(b.Lon-p.Lon)
	if math.Abs(bx-ax) > 180 {
		if bx < ax {
			bx += 360
		} else {
			ax += 360
		}
	}
	c := math.Cos(radians(p.Lat))
	dx, dy := (bx-ax)*c, b.Lat-a.Lat
	t := 0.0
	if d := dx*dx + dy*dy; d > 0 {
		t = max(0, min(1, -(ax*c*dx+(a.Lat-p.Lat)*dy)/d))
	}
	q := Point{Lat: a.Lat + t*(b.Lat-a.Lat), Lon: wrap(p.Lon + ax + t*(bx-ax))}
	return min(haversine(p, q), min(haversine(p, a), haversine(p, b)))
}

func insideRing(p Point, ring []Point) bool {
	inside := false
	for i, j := 0, len(ring)-1; i < len(ring); j, i = i, i+1 {
		if rayCrossReference(p, ring[i], ring[j], ring[0].Lon) {
			inside = !inside
		}
	}
	return inside
}
func geometryDistance(p Point, f Feature) float64 {
	if len(f.Geometry) == 0 {
		return haversine(p, f.Point)
	}
	d := math.Inf(1)
	inside := false
	for _, path := range f.Geometry {
		if f.Polygon && insideRing(p, path) {
			inside = !inside
		}
		if len(path) == 1 {
			d = min(d, haversine(p, path[0]))
		}
		for i := 1; i < len(path); i++ {
			d = min(d, segmentDistance(p, path[i-1], path[i]))
		}
	}
	if inside {
		return 0
	}
	return d
}

type treeNode struct {
	bounds                    box
	left, right, start, count uint32
}
type spatialTree struct {
	nodes []treeNode
	order []uint32
}

func buildTree(boxes []box) spatialTree {
	t := spatialTree{order: make([]uint32, len(boxes))}
	for i := range t.order {
		t.order[i] = uint32(i)
	}
	if len(boxes) == 0 {
		return t
	}
	var build func(int, int) uint32
	build = func(lo, hi int) uint32 {
		b := boxes[t.order[lo]]
		for _, id := range t.order[lo+1 : hi] {
			b = b.union(boxes[id])
		}
		idx := uint32(len(t.nodes))
		t.nodes = append(t.nodes, treeNode{bounds: b})
		if hi-lo <= 16 {
			t.nodes[idx].start = uint32(lo)
			t.nodes[idx].count = uint32(hi - lo)
			return idx
		}
		axisLon := int64(b.east)-int64(b.west) > int64(b.north)-int64(b.south)
		center := func(id uint32) int64 {
			b := boxes[id]
			if axisLon {
				return int64(b.west) + int64(b.east)
			}
			return int64(b.south) + int64(b.north)
		}
		mid := (lo + hi) / 2
		selectMedian(t.order[lo:hi], mid-lo, center)
		left, right := build(lo, mid), build(mid, hi)
		t.nodes[idx].left = left
		t.nodes[idx].right = right
		return idx
	}
	build(0, len(boxes))
	return t
}

// Three-way quickselect avoids sorting each tree level, including datasets
// with many identical coordinates. Depth limit guards adversarial input.
func selectMedian(a []uint32, k int, key func(uint32) int64) {
	for budget := 64; len(a) > 1; budget-- {
		if budget == 0 {
			sort.Slice(a, func(i, j int) bool { return key(a[i]) < key(a[j]) })
			return
		}
		pivot := key(a[len(a)/2])
		lo, i, hi := 0, 0, len(a)
		for i < hi {
			v := key(a[i])
			if v < pivot {
				a[lo], a[i] = a[i], a[lo]
				lo++
				i++
			} else if v > pivot {
				hi--
				a[i], a[hi] = a[hi], a[i]
			} else {
				i++
			}
		}
		if k < lo {
			a = a[:lo]
		} else if k >= hi {
			k -= hi
			a = a[hi:]
		} else {
			return
		}
	}
}
