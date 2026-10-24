package geocode

import (
	"math"
	"math/rand/v2"
	"testing"
)

func TestLatitudeBlockOutside(t *testing.T) {
	rng := rand.New(rand.NewPCG(261, 279))
	for _, alignment := range []int{0, 1, 4, 7} {
		storage := make([]byte, alignment+65*8)
		b := storage[alignment:]
		for _, lat := range []int32{-900000000, -470000000, 0, 470000000, 900000000} {
			for i := range 65 {
				le.PutUint32(b[i*8:], uint32(lat))
				le.PutUint32(b[i*8+4:], rng.Uint32()) // Longitudes must be ignored.
			}
			y := float64(lat) / coordScale
			for _, window := range [][2]float64{
				{y, y}, {y - 1, y}, {y, y + 1},
				{math.Nextafter(y, math.Inf(1)), y + 1},
				{y - 1, math.Nextafter(y, math.Inf(-1))},
				{math.Inf(-1), math.Inf(1)},
			} {
				want := y < window[0] || y > window[1]
				if got := latitudeBlockOutside(b, window[0], window[1]); got != want {
					t.Fatalf("alignment %d lat %d window %v: got %v, want %v", alignment, lat, window, got, want)
				}
			}
		}
		// A single vertex in the window, including the shared block endpoint,
		// must prevent rejection regardless of its lane or which side surrounds it.
		for _, outside := range []int32{-10000000, 10000000} {
			for i := range 65 {
				le.PutUint32(b[i*8:], uint32(outside))
			}
			if !latitudeBlockOutside(b, -.5, .5) {
				t.Fatal("did not reject a block wholly outside the window")
			}
			for i := range 65 {
				le.PutUint32(b[i*8:], 0)
				if latitudeBlockOutside(b, -.5, .5) {
					t.Fatalf("alignment %d: skipped crossing at vertex %d", alignment, i)
				}
				le.PutUint32(b[i*8:], uint32(outside))
			}
		}
		// Endpoints on opposite sides cross the window even when none is inside.
		le.PutUint32(b[64*8:], uint32(10000000))
		opposite := int32(-10000000)
		le.PutUint32(b, uint32(opposite))
		if latitudeBlockOutside(b, -.5, .5) {
			t.Fatal("skipped a block spanning the window")
		}
	}
}

func TestGeometryBlockTails(t *testing.T) {
	for _, count := range []int{63, 64, 65, 66, 255, 256, 257, 258, 319, 320, 321, 322, 513} {
		var features []Feature
		for _, polygon := range []bool{false, true} {
			for _, centre := range []Point{{47.37, 8.54}, {-89.9, 179.99}, {89.9, -179.99}} {
				ring := make([]Point, count)
				for i := range ring {
					a := 2 * math.Pi * float64(i) / float64(count)
					ring[i] = Point{float64(quant(centre.Lat+.02*math.Sin(a))) / coordScale,
						float64(quant(wrap(centre.Lon+.02*math.Cos(a)))) / coordScale}
				}
				features = append(features, Feature{OSMID: int64(len(features) + 1), OSMType: "way",
					Point: centre, Polygon: polygon, Geometry: [][]Point{nil, ring, {ring[0]}}})
			}
		}
		db := testDB(t, features)
		for id, f := range features {
			for _, delta := range []float64{0, .0199999, .021, -.03} {
				p := Point{f.Point.Lat + delta, f.Point.Lon}
				want := geometryDistance(p, f)
				// Leave a micrometre for rounding in the existing spherical
				// lower bound; TestLatitudeBlockOutside checks exact window edges.
				for _, cutoff := range []float64{0, want / 2, want + 1e-6, math.Inf(1)} {
					got, err := db.distanceWithin(uint32(id), p, cutoff)
					if err != nil || want <= cutoff && got != want || want > cutoff && got <= cutoff {
						t.Fatalf("count %d feature %d delta %g cutoff %g: got %g, want %g (%v)", count, id, delta, cutoff, got, want, err)
					}
				}
			}
		}
	}
}
