package geocode

import (
	"context"
	"encoding/json"
	"math/rand/v2"
	"os"
	"testing"
)

type realGeometrySample struct {
	ID       uint32
	Point    Point
	Vertices int
}

type realGeometryStats struct {
	WithGeometry, LongNonBoundary, LongBoundary, MaxPathVertices int
}

// Reservoir sampling gives long features throughout the database equal chances
// of selection. Queries sit a few metres from an actual vertex, rather than a
// label which could be far from the feature's geometry. This file also compiles
// on main, so both implementations use identical samples and benchmark code.
func realGeometryWorkload(t testing.TB, db *DB) ([]realGeometrySample, realGeometryStats) {
	t.Helper()
	rng := rand.New(rand.NewPCG(261023, 128))
	samples := make([]realGeometrySample, 0, 128)
	var stats realGeometryStats
	for id := uint32(0); id < db.count; id++ {
		r := db.record(id)
		off, n := le.Uint64(r[40:]), uint64(le.Uint32(r[48:]))
		if n == 0 {
			continue
		}
		stats.WithGeometry++
		data := db.sections[sGeometry][off : off+n]
		if len(data) < 4 {
			t.Fatal("short geometry header")
		}
		paths := int(le.Uint32(data))
		data = data[4:]
		var longest int
		var point Point
		for range paths {
			if len(data) < 4 {
				t.Fatal("short path header")
			}
			count := int(le.Uint32(data))
			data = data[4:]
			if count > len(data)/8 {
				t.Fatal("short path")
			}
			if count > longest {
				longest = count
				mid := data[(count/2)*8:]
				point = Point{float64(int32(le.Uint32(mid))) / coordScale,
					float64(int32(le.Uint32(mid[4:]))) / coordScale}
			}
			data = data[count*8:]
		}
		if len(data) != 0 {
			t.Fatal("trailing geometry bytes")
		}
		stats.MaxPathVertices = max(stats.MaxPathVertices, longest)
		if longest <= 256 {
			continue
		}
		if r[10] > 0 {
			stats.LongBoundary++
			continue
		}
		stats.LongNonBoundary++
		point.Lat += .00003
		point.Lon = wrap(point.Lon - .00003)
		if !validPoint(point) {
			point.Lat -= .00006
		}
		sample := realGeometrySample{id, point, longest}
		if len(samples) < cap(samples) {
			samples = append(samples, sample)
		} else if i := rng.IntN(stats.LongNonBoundary); i < len(samples) {
			samples[i] = sample
		}
	}
	if len(samples) == 0 {
		t.Fatal("no non-boundary features with paths longer than 256 vertices")
	}
	return samples, stats
}

func BenchmarkRealLongGeometry(b *testing.B) {
	if os.Getenv("GEOCODE_BENCH_DB") == "" {
		b.Skip("set GEOCODE_BENCH_DB to an imported database")
	}
	db := benchmarkDB(b)
	samples, _ := realGeometryWorkload(b, db)
	for _, s := range samples {
		if d, err := db.distanceWithin(s.ID, s.Point, 1000); err != nil || d > 1000 {
			b.Fatalf("sample %d: distance %g (%v)", s.ID, d, err)
		}
	}
	i := 0
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		s := samples[i%len(samples)]
		if _, err := db.distanceWithin(s.ID, s.Point, 1000); err != nil {
			b.Fatal(err)
		}
		i++
	}
}

func BenchmarkReverseRealLongFeatures(b *testing.B) {
	if os.Getenv("GEOCODE_BENCH_DB") == "" {
		b.Skip("set GEOCODE_BENCH_DB to an imported database")
	}
	db := benchmarkDB(b)
	samples, _ := realGeometryWorkload(b, db)
	ctx := context.Background()
	for _, s := range samples {
		if r, err := db.Reverse(ctx, s.Point, ReverseOptions{Limit: 1}); err != nil || len(r) == 0 {
			b.Fatalf("sample %d: %d results (%v)", s.ID, len(r), err)
		}
	}
	i := 0
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		if _, err := db.Reverse(ctx, samples[i%len(samples)].Point, ReverseOptions{Limit: 1}); err != nil {
			b.Fatal(err)
		}
		i++
	}
}

func TestRealGeometrySnapshot(t *testing.T) {
	path, output := os.Getenv("GEOCODE_BENCH_DB"), os.Getenv("GEOCODE_GEOMETRY_SNAPSHOT")
	if path == "" || output == "" {
		t.Skip("set GEOCODE_BENCH_DB and GEOCODE_GEOMETRY_SNAPSHOT to record a corpus")
	}
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	samples, stats := realGeometryWorkload(t, db)
	t.Logf("geometry statistics: %+v; samples: %d", stats, len(samples))
	type entry struct {
		realGeometrySample
		Distance           float64
		Reverse, Addresses []Result
	}
	corpus := make([]entry, len(samples))
	ctx := context.Background()
	for i, s := range samples {
		e := &corpus[i]
		e.realGeometrySample = s
		e.Distance, err = db.distanceWithin(s.ID, s.Point, 1000)
		if err != nil || e.Distance > 1000 {
			t.Fatalf("sample %d: distance %g (%v)", s.ID, e.Distance, err)
		}
		e.Reverse, err = db.Reverse(ctx, s.Point, ReverseOptions{Limit: 5})
		if err != nil {
			t.Fatal(err)
		}
		e.Addresses, err = db.Reverse(ctx, s.Point, ReverseOptions{Limit: 3, Class: "address"})
		if err != nil {
			t.Fatal(err)
		}
	}
	data, err := json.MarshalIndent(struct {
		Stats  realGeometryStats
		Corpus []entry
	}{stats, corpus}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(output, data, 0600); err != nil {
		t.Fatal(err)
	}
}
