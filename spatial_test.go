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
