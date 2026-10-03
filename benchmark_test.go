package geocode

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func benchmarkDB(b *testing.B) *DB {
	b.Helper()
	if path := os.Getenv("GEOCODE_BENCH_DB"); path != "" {
		db, e := Open(path)
		if e != nil {
			b.Fatal(e)
		}
		b.Cleanup(func() { db.Close() })
		return db
	}
	features := make([]Feature, 100000)
	for i := range features {
		features[i] = Feature{OSMID: int64(i + 1), OSMType: "node", Class: "address", Type: "house", Point: Point{47.3 + float64(i/1000)*.0001, 8.5 + float64(i%1000)*.0001}, Importance: .65, Address: Address{HouseNumber: fmt.Sprint(i%200 + 1), Street: fmt.Sprintf("Street%d", i/200), City: "Zürich", Postcode: "8001"}}
	}
	path := filepath.Join(b.TempDir(), "bench.db")
	if e := Build(path, features); e != nil {
		b.Fatal(e)
	}
	db, e := Open(path)
	if e != nil {
		b.Fatal(e)
	}
	b.Cleanup(func() { db.Close() })
	return db
}
func BenchmarkSearch(b *testing.B) {
	db := benchmarkDB(b)
	q := "Street42 10 Zurich"
	if os.Getenv("GEOCODE_BENCH_DB") != "" {
		q = "Bahnhofstrasse 10 Zürich"
	}
	ctx := context.Background()
	r, e := db.Search(ctx, q, SearchOptions{Limit: 5})
	if e != nil || len(r) == 0 {
		b.Fatalf("empty benchmark query %q: %v", q, e)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		if _, e := db.Search(ctx, q, SearchOptions{Limit: 5}); e != nil {
			b.Fatal(e)
		}
	}
}
func BenchmarkSearchCommon(b *testing.B) {
	db := benchmarkDB(b)
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		if _, e := db.Search(ctx, "Zürich", SearchOptions{Limit: 5}); e != nil {
			b.Fatal(e)
		}
	}
}
func BenchmarkReverse(b *testing.B) {
	db := benchmarkDB(b)
	p := Point{47.305, 8.55}
	if os.Getenv("GEOCODE_BENCH_DB") != "" {
		p = Point{47.3769, 8.5417}
	}
	ctx := context.Background()
	r, e := db.Reverse(ctx, p, ReverseOptions{Limit: 1})
	if e != nil || len(r) == 0 {
		b.Fatalf("empty reverse: %v", e)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		if _, e := db.Reverse(ctx, p, ReverseOptions{Limit: 1}); e != nil {
			b.Fatal(e)
		}
	}
}
func BenchmarkReverseParallel(b *testing.B) {
	db := benchmarkDB(b)
	ctx := context.Background()
	p := Point{47.305, 8.55}
	if os.Getenv("GEOCODE_BENCH_DB") != "" {
		p = Point{47.3769, 8.5417}
	}
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			if _, e := db.Reverse(ctx, p, ReverseOptions{Limit: 1}); e != nil {
				b.Error(e)
				return
			}
		}
	})
}
