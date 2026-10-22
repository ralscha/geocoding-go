package geocode

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"math/rand/v2"
	"os"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"
)

func TestPrefixSingleTermMatchesExactRanking(t *testing.T) {
	features := make([]Feature, 600)
	for i := range features {
		features[i] = Feature{OSMID: int64(i + 1), OSMType: "node", Name: "Common", Class: "place", Importance: float64(i%101) / 100}
		if i%3 == 0 {
			features[i].Name = "Other"
			features[i].Address.City = "Common"
		}
	}
	db := testDB(t, features)
	ctx := context.Background()
	world := [4]float64{-90, -180, 90, 180}
	for _, limit := range []int{1, 5, 100} {
		want, err := db.Search(ctx, "Common", SearchOptions{Limit: limit, Bounds: &world})
		if err != nil {
			t.Fatal(err)
		}
		for _, query := range []string{"Common", "Comm"} {
			got, err := db.Search(ctx, query, SearchOptions{Limit: limit, Prefix: true})
			if err != nil || !reflect.DeepEqual(got, want) {
				t.Fatalf("prefix %q limit %d differs from full ranking: %v", query, limit, err)
			}
		}
	}
}

func TestUnionPostingsMatchesReference(t *testing.T) {
	rng := rand.New(rand.NewPCG(316, 719))
	for _, partCount := range []int{0, 1, 2, 8, 64, 256} {
		parts := make([][]byte, partCount)
		weights := make(map[uint32]uint32)
		for i := range parts {
			for id := uint32(0); id < 400; id++ {
				if rng.IntN(9) != 0 {
					continue
				}
				weight := uint32(rng.IntN(12) + 1)
				parts[i] = le.AppendUint32(parts[i], id)
				parts[i] = le.AppendUint32(parts[i], weight)
				weights[id] = max(weights[id], weight)
			}
		}
		got, df, err := unionPostings(context.Background(), parts, nil)
		if err != nil || len(got)/8 != len(weights) || df != len(weights) {
			t.Fatalf("%d parts: got %d postings, want %d: %v", partCount, len(got)/8, len(weights), err)
		}
		var previous uint32
		for i := 0; i < len(got); i += 8 {
			id, weight := le.Uint32(got[i:]), le.Uint32(got[i+4:])
			if weight != weights[id] || i > 0 && id <= previous {
				t.Fatalf("%d parts: unexpected posting %d weight %d", partCount, id, weight)
			}
			previous = id
		}
		var filter []byte
		filteredCount := 0
		for id := uint32(0); id < 450; id += 5 {
			filter = le.AppendUint32(filter, id)
			filter = le.AppendUint32(filter, 1)
			if _, ok := weights[id]; ok {
				filteredCount++
			}
		}
		filtered, df, err := unionPostings(context.Background(), parts, filter)
		if err != nil || len(filtered)/8 != filteredCount || df != len(weights) {
			t.Fatalf("%d filtered parts: got %d postings df %d, want %d df %d: %v", partCount, len(filtered)/8, df, filteredCount, len(weights), err)
		}
		for i := 0; i < len(filtered); i += 8 {
			id, weight := le.Uint32(filtered[i:]), le.Uint32(filtered[i+4:])
			if id%5 != 0 || weight != weights[id] {
				t.Fatalf("%d filtered parts: unexpected posting %d weight %d", partCount, id, weight)
			}
		}
	}
}

func TestPrefixIntersectionPreservesFullDocumentFrequency(t *testing.T) {
	features := make([]Feature, 700)
	for i := range features {
		f := Feature{OSMID: int64(i + 1), OSMType: "node", Class: "place", Importance: float64(i%101) / 100}
		if i%7 != 0 {
			f.Address.City = fmt.Sprintf("precity%d", i%8)
		}
		if i%3 == 0 {
			f.Name = fmt.Sprintf("prename%d", i%5)
		}
		if i%4 == 0 {
			f.Aliases = []string{"prealias"}
		}
		if i%23 == 0 {
			f.Address.Street = "anchor"
		}
		if i%2 == 0 {
			f.Address.Country = "country"
		}
		features[i] = f
	}
	db := testDB(t, features)
	ctx := context.Background()
	// The unrestricted prefix query supplies scores with the complete prefix
	// document frequency. Exact terms contribute independently to that score.
	all, err := db.Search(ctx, "pre", SearchOptions{Prefix: true, Limit: 100})
	if err != nil || len(all) == 0 {
		t.Fatal(err)
	}
	var prefixCount, anchorCount, countryCount int
	for _, f := range features {
		if f.Name != "" || len(f.Aliases) > 0 || f.Address.City != "" {
			prefixCount++
		}
		if f.Address.Street != "" {
			anchorCount++
		}
		if f.Address.Country != "" {
			countryCount++
		}
	}
	for _, withCountry := range []bool{false, true} {
		query := "anchor pre"
		if withCountry {
			query = "country anchor pre"
		}
		var want matches
		for i, f := range features {
			if f.Address.Street == "" || withCountry && f.Address.Country == "" {
				continue
			}
			weight := 0
			if f.Address.City != "" {
				weight = 3
			}
			if len(f.Aliases) > 0 {
				weight = 9
			}
			if f.Name != "" {
				weight = 12
			}
			if weight == 0 {
				continue
			}
			score := 10 * math.Log1p(float64(len(features))/float64(anchorCount))
			if withCountry {
				score += math.Log1p(float64(len(features)) / float64(countryCount))
			}
			score += float64(weight) * math.Log1p(float64(len(features))/float64(prefixCount))
			score += float64(float32(f.Importance)) * 10
			want = append(want, match{id: uint32(i), score: score})
		}
		slices.SortFunc(want, compareMatches)
		got, err := db.Search(ctx, query, SearchOptions{Prefix: true, Limit: 100})
		if err != nil || len(got) != len(want) {
			t.Fatalf("%q: got %d, want %d: %v", query, len(got), len(want), err)
		}
		for i := range want {
			if got[i].OSMID != int64(want[i].id)+1 || got[i].Score != want[i].score {
				t.Fatalf("%q rank %d: got id %d score %.17g, want id %d score %.17g", query, i, got[i].OSMID, got[i].Score, want[i].id+1, want[i].score)
			}
		}
	}
}

func BenchmarkUnionPostings(b *testing.B) {
	for _, partCount := range []int{8, 64} {
		for _, overlap := range []bool{false, true} {
			b.Run(fmt.Sprintf("parts%d/overlap%t", partCount, overlap), func(b *testing.B) {
				parts := make([][]byte, partCount)
				for p := range parts {
					parts[p] = make([]byte, 4096*8)
					for i := range 4096 {
						id := i*partCount + p
						if overlap {
							id = i + p
						}
						le.PutUint32(parts[p][i*8:], uint32(id))
						le.PutUint32(parts[p][i*8+4:], uint32(p%12+1))
					}
				}
				b.ReportAllocs()
				b.ResetTimer()
				for b.Loop() {
					if _, _, err := unionPostings(context.Background(), parts, nil); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}

func BenchmarkSearchPrefixMultipleTerms(b *testing.B) {
	db := benchmarkDB(b)
	ctx := context.Background()
	queries := []string{"10 street4"}
	if os.Getenv("GEOCODE_BENCH_DB") != "" {
		queries = []string{"10 bahnhofstr", "Bahnhofstrasse 10 zuri"}
	}
	for _, query := range queries {
		b.Run(query, func(b *testing.B) {
			results, err := db.Search(ctx, query, SearchOptions{Limit: 5, Prefix: true})
			if err != nil || len(results) == 0 {
				b.Fatalf("prefix benchmark: %v (%d results)", err, len(results))
			}
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				if _, err := db.Search(ctx, query, SearchOptions{Limit: 5, Prefix: true}); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func TestPrefixWorkloadSnapshot(t *testing.T) {
	path, output := os.Getenv("GEOCODE_BENCH_DB"), os.Getenv("GEOCODE_PREFIX_SNAPSHOT")
	if path == "" || output == "" {
		t.Skip("set GEOCODE_BENCH_DB and GEOCODE_PREFIX_SNAPSHOT to record prefix results")
	}
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	queries, _ := queryWorkload(t, db)
	queries = append(queries, "10 bahnhofstr", "Bahnhofstrasse 10 zuri")
	type entry struct {
		Query   string
		Results []Result
		Error   string
	}
	corpus := make([]entry, 0, len(queries))
	for _, query := range queries {
		terms := splitTerms(query)
		last := []rune(terms[len(terms)-1])
		if len(last) > 2 {
			terms[len(terms)-1] = string(last[:len(last)-1])
		}
		query = strings.Join(terms, " ")
		result, err := db.Search(context.Background(), query, SearchOptions{Limit: 100, Prefix: true})
		e := entry{Query: query, Results: result}
		if err != nil {
			e.Error = err.Error()
		}
		corpus = append(corpus, e)
	}
	data, err := json.MarshalIndent(corpus, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(output, data, 0600); err != nil {
		t.Fatal(err)
	}
}

func BenchmarkSearchPrefixSingleTerm(b *testing.B) {
	db := benchmarkDB(b)
	terms := len(db.sections[sTerms]) / termSize
	start := sort.Search(terms, func(i int) bool { return string(db.termBytes(i)) >= "zurich" })
	if start+1 < terms && strings.HasPrefix(string(db.termBytes(start+1)), "zurich") {
		b.Skip("fixture contains multiple zurich prefix terms")
	}
	ctx := context.Background()
	results, err := db.Search(ctx, "zurich", SearchOptions{Limit: 5, Prefix: true})
	if err != nil || len(results) == 0 {
		b.Fatalf("prefix benchmark: %v (%d results)", err, len(results))
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		if _, err := db.Search(ctx, "zurich", SearchOptions{Limit: 5, Prefix: true}); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkNormalize(b *testing.B) {
	for _, input := range []struct{ name, value string }{
		{"normalized_ascii", "bahnhofstrasse 10 zurich"},
		{"mixed_ascii", "Bahnhofstrasse, 10  Zurich!"},
		{"unicode", "Bahnhofstraße 10 Zürich"},
	} {
		b.Run(input.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				Normalize(input.value)
			}
		})
	}
}
