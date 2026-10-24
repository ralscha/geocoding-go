package geocode

import (
	"context"
	"fmt"
	"math"
	"testing"
)

// These benchmarks also compile unchanged on main, for comparisons using the
// same geometry, cutoff and query corpus. Database construction is not timed.
func BenchmarkGeometryWindow(b *testing.B) {
	for _, segments := range []int{8, 64, 256, 8192} {
		for _, cutoff := range []float64{1000, math.Inf(1)} {
			b.Run(fmt.Sprintf("segments=%d/cutoff=%g", segments, cutoff), func(b *testing.B) {
				f := benchmarkGeometryFeature(segments)
				db := testDB(b, []Feature{f})
				b.ReportAllocs()
				b.ResetTimer()
				for b.Loop() {
					if _, err := db.distanceWithin(0, f.Point, cutoff); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}

func BenchmarkReverseLongGeometry(b *testing.B) {
	f := benchmarkGeometryFeature(8192)
	f.Polygon = true
	db := testDB(b, []Feature{f})
	ctx := context.Background()
	points := make([]Point, 128)
	for i := range points {
		a := 2 * math.Pi * float64(i) / float64(len(points))
		points[i] = Point{f.Point.Lat + .101*math.Sin(a), f.Point.Lon + .101*math.Cos(a)}
		if r, err := db.Reverse(ctx, points[i], ReverseOptions{Limit: 1}); err != nil || len(r) != 1 {
			b.Fatalf("query %d: %d results (%v)", i, len(r), err)
		}
	}
	i := 0
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		if _, err := db.Reverse(ctx, points[i%len(points)], ReverseOptions{Limit: 1}); err != nil {
			b.Fatal(err)
		}
		i++
	}
}

func benchmarkGeometryFeature(segments int) Feature {
	centre := Point{47.37, 8.54}
	ring := make([]Point, segments+1)
	for i := range ring {
		a := 2 * math.Pi * float64(i) / float64(segments)
		ring[i] = Point{centre.Lat + .1*math.Sin(a), centre.Lon + .1*math.Cos(a)}
	}
	ring[segments] = ring[0]
	return Feature{OSMID: 1, OSMType: "way", Name: "Geometry benchmark", Class: "leisure", Type: "park",
		Point: centre, Geometry: [][]Point{ring}}
}
