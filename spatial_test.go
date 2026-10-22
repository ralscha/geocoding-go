package geocode

import (
	"context"
	"math"
	"math/rand/v2"
	"testing"
)

func TestPrunedGeometryAgainstFullEvaluation(t *testing.T) {
	rng := rand.New(rand.NewPCG(778, 221))
	var features []Feature
	for i := range 300 {
		base := Point{rng.Float64()*160 - 80, rng.Float64()*360 - 180}
		path := make([]Point, 20)
		for j := range path {
			path[j] = Point{base.Lat + (rng.Float64()-.5)*.3, wrap(base.Lon + (rng.Float64()-.5)*.3)}
		}
		features = append(features, Feature{OSMID: int64(i + 1), OSMType: "way", Name: "road", Class: "highway", Type: "residential", Point: path[0], Geometry: [][]Point{path}})
	}
	db := testDB(t, features)
	for i, f := range features {
		p := Point{f.Point.Lat + .01, wrap(f.Point.Lon + .01)}
		want := geometryDistance(p, f)
		got, e := db.distanceWithin(uint32(i), p, want+1)
		if e != nil || math.Abs(got-want) > .03 {
			t.Fatalf("feature %d: %.6f vs %.6f (%v)", i, got, want, e)
		}
		r, e := db.Reverse(context.Background(), p, ReverseOptions{Limit: 1, RadiusMeters: want + 1})
		if e != nil || len(r) != 1 {
			t.Fatalf("pruned valid result %d: %+v %v", i, r, e)
		}
	}
}

func TestBoxLowerBoundAtVeryShortDistances(t *testing.T) {
	for _, lat := range []float64{-89, -47.3, 0, 47.3, 89} {
		for _, lon := range []float64{-179.99, 0, 179.99} {
			point := Point{lat, lon}
			bounds := pointBox(point)
			for _, offset := range []float64{1e-10, 1e-9, 1e-8, 1e-7, 1e-6, .0001} {
				query := Point{lat + offset, lon + offset}
				want := haversine(point, query)
				got := bounds.lowerBound(query)
				if got > want+1e-6 || got < max(0, want-.010001) {
					t.Fatalf("%+v -> %+v: lower bound %.9f, actual %.9f", point, query, got, want)
				}
			}
		}
	}
}

func TestPolygonClosingEdgeDistance(t *testing.T) {
	for _, tt := range []struct {
		name     string
		geometry [][]Point
		query    Point
		nearest  Point
	}{
		{
			name:     "outer ring",
			geometry: [][]Point{{{0, 0}, {0, 1}, {1, 1}, {1, 0}}},
			query:    Point{.5, -.00001},
			nearest:  Point{.5, 0},
		},
		{
			name: "hole",
			geometry: [][]Point{
				{{-2, -2}, {-2, 2}, {2, 2}, {2, -2}, {-2, -2}},
				{{-1, -1}, {-1, 1}, {1, 1}, {1, -1}},
			},
			query:   Point{0, -.99999},
			nearest: Point{0, -1},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := Feature{OSMID: 1, OSMType: "way", Name: "Park", Class: "leisure", Type: "park", Polygon: true, Geometry: tt.geometry}
			want := haversine(tt.query, tt.nearest)
			if got := geometryDistance(tt.query, f); math.Abs(got-want) > .001 {
				t.Errorf("geometry distance = %.6f, want %.6f", got, want)
			}
			db := testDB(t, []Feature{f})
			got, err := db.Reverse(context.Background(), tt.query, ReverseOptions{Limit: 1, RadiusMeters: 2})
			if err != nil || len(got) != 1 {
				t.Fatalf("closing edge omitted: %+v, %v", got, err)
			}
			if math.Abs(got[0].Distance-want) > .001 {
				t.Fatalf("reverse distance = %.6f, want %.6f", got[0].Distance, want)
			}
		})
	}
}

func TestPolygonClosingEdgeAcrossDateline(t *testing.T) {
	f := Feature{Polygon: true, Geometry: [][]Point{{{-1, 179}, {-1, 0}, {1, 0}, {1, -179}}}}
	bounds := featureBox(f)
	if bounds.west != -1800000000 || bounds.east != 1800000000 {
		t.Fatalf("bounds exclude closing edge across dateline: %+v", bounds)
	}
}

func TestUnclosedPreparedBoundaryAndLabel(t *testing.T) {
	ring := []Point{{0, 1}, {0, 0}, {1, 0}, {1, 1}}
	f := Feature{Polygon: true, Geometry: [][]Point{ring}}
	prepared := prepareBoundary(0, f)
	if !prepared.contains(Point{.5, .5}) {
		t.Fatal("prepared boundary omitted its closing edge")
	}
	if got := polygonLabel(ring, .5); got != (Point{.5, .5}) {
		t.Fatalf("polygon label = %+v, want ring centre", got)
	}
}

func TestGeometryLatitudeWindow(t *testing.T) {
	var features []Feature
	for _, lat := range []float64{-89.95, 0, 47.37, 89.95} {
		for _, lon := range []float64{-179.99, 0, 179.99} {
			for _, polygon := range []bool{false, true} {
				ring := make([]Point, 128)
				for i := range ring {
					angle := 2 * math.Pi * float64(i) / float64(len(ring))
					ring[i] = Point{
						float64(quant(lat+.02*math.Sin(angle))) / coordScale,
						float64(quant(wrap(lon+.02*math.Cos(angle)))) / coordScale,
					}
				}
				features = append(features, Feature{
					OSMID: int64(len(features) + 1), OSMType: "way", Point: Point{lat, lon},
					Geometry: [][]Point{nil, ring}, Polygon: polygon,
				})
			}
		}
	}
	db := testDB(t, features)
	for id, f := range features {
		for _, offset := range []float64{-.03, 0, .0199999, .03} {
			p := Point{f.Point.Lat + offset, f.Point.Lon}
			want := geometryDistance(p, f)
			for _, cutoff := range []float64{0, want / 2, want, math.Nextafter(want, math.Inf(1)), math.Inf(1)} {
				got, err := db.distanceWithin(uint32(id), p, cutoff)
				if err != nil {
					t.Fatal(err)
				}
				if want <= cutoff && got != want {
					t.Fatalf("feature %d query %+v cutoff %.9f: distance %.9f, want %.9f", id, p, cutoff, got, want)
				}
				if want > cutoff && got <= cutoff {
					t.Fatalf("feature %d query %+v cutoff %.9f: accepted distance %.9f, actual %.9f", id, p, cutoff, got, want)
				}
			}
		}
	}
}

func BenchmarkGeometryTraversal(b *testing.B) {
	centre := Point{47.37, 8.54}
	ring := make([]Point, 8193)
	for i := range ring {
		angle := 2 * math.Pi * float64(i) / float64(len(ring)-1)
		ring[i] = Point{centre.Lat + .1*math.Sin(angle), centre.Lon + .1*math.Cos(angle)}
	}
	ring[len(ring)-1] = ring[0]
	for _, polygon := range []bool{false, true} {
		name := "line"
		if polygon {
			name = "polygon"
		}
		b.Run(name, func(b *testing.B) {
			f := Feature{OSMID: 1, OSMType: "way", Point: centre, Geometry: [][]Point{ring}, Polygon: polygon}
			db := testDB(b, []Feature{f})
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				if _, err := db.distanceWithin(0, centre, 1000); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
