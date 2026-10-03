package geocode

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// Sample addresses across the complete file rather than repeating one city.
// Each index starts in a different stratum, including node and way records.
func queryWorkload(t testing.TB, db *DB) ([]string, []Point) {
	t.Helper()
	const count = 128
	queries := make([]string, 0, count)
	points := make([]Point, 0, count)
	for i := range count {
		start := uint32(uint64(i) * uint64(db.count) / count)
		for id := start; id < db.count; id++ {
			f, err := db.metadata(id)
			if err != nil {
				t.Fatal(err)
			}
			if f.Address.HouseNumber == "" || f.Address.Street == "" || f.Address.City == "" {
				continue
			}
			q := strings.Join([]string{f.Address.Street, f.Address.HouseNumber, f.Address.City}, " ")
			if len(q) > 512 || len(splitTerms(q)) > 30 {
				continue
			}
			queries = append(queries, q)
			p := f.Point
			// Offset the label to exercise nearest segments as well as containment.
			p.Lat += float64(i%5-2) * 0.00003
			p.Lon = wrap(p.Lon + float64(i%7-3)*0.00003)
			points = append(points, p)
			break
		}
	}
	if len(queries) == 0 {
		t.Fatal("no addresses found for workload")
	}
	return queries, points
}

func BenchmarkSearchMixed(b *testing.B) {
	db := benchmarkDB(b)
	queries, _ := queryWorkload(b, db)
	ctx := context.Background()
	for _, q := range queries {
		r, err := db.Search(ctx, q, SearchOptions{Limit: 5})
		if err != nil || len(r) == 0 {
			b.Fatalf("%q: %v (%d results)", q, err, len(r))
		}
	}
	i := 0
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		if _, err := db.Search(ctx, queries[i%len(queries)], SearchOptions{Limit: 5}); err != nil {
			b.Fatal(err)
		}
		i++
	}
}

func BenchmarkReverseMixed(b *testing.B) {
	db := benchmarkDB(b)
	_, points := queryWorkload(b, db)
	ctx := context.Background()
	for _, p := range points {
		r, err := db.Reverse(ctx, p, ReverseOptions{Limit: 1})
		if err != nil || len(r) == 0 {
			b.Fatalf("%+v: %v (%d results)", p, err, len(r))
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

func BenchmarkReverseAddressMixed(b *testing.B) {
	db := benchmarkDB(b)
	_, points := queryWorkload(b, db)
	ctx := context.Background()
	i := 0
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		if _, err := db.Reverse(ctx, points[i%len(points)], ReverseOptions{Limit: 1, Class: "address"}); err != nil {
			b.Fatal(err)
		}
		i++
	}
}

// This opt-in integration fixture records complete results for comparison
// across implementations on the same real database. It is not a latency test.
func TestQueryWorkloadSnapshot(t *testing.T) {
	path, output := os.Getenv("GEOCODE_BENCH_DB"), os.Getenv("GEOCODE_QUERY_SNAPSHOT")
	if path == "" || output == "" {
		t.Skip("set GEOCODE_BENCH_DB and GEOCODE_QUERY_SNAPSHOT to record a corpus")
	}
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	queries, points := queryWorkload(t, db)
	type entry struct {
		Query                      string
		Point                      Point
		Search, Reverse, Addresses []Result
	}
	corpus := make([]entry, len(queries))
	ctx := context.Background()
	for i, q := range queries {
		e := &corpus[i]
		e.Query = q
		e.Point = points[i]
		e.Search, err = db.Search(ctx, q, SearchOptions{Limit: 5})
		if err != nil {
			t.Fatal(err)
		}
		e.Reverse, err = db.Reverse(ctx, e.Point, ReverseOptions{Limit: 5})
		if err != nil {
			t.Fatal(err)
		}
		e.Addresses, err = db.Reverse(ctx, e.Point, ReverseOptions{Limit: 3, Class: "address"})
		if err != nil {
			t.Fatal(err)
		}
	}
	data, err := json.MarshalIndent(corpus, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(output, data, 0600); err != nil {
		t.Fatal(err)
	}
}
