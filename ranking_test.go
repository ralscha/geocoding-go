package geocode

import (
	"context"
	"math/rand/v2"
	"sort"
	"testing"
)

func TestTopKMatchesFullSort(t *testing.T) {
	rng := rand.New(rand.NewPCG(217, 831))
	all := make([]match, 10000)
	for i := range all {
		all[i] = match{id: uint32(i), score: float64(rng.IntN(23))}
	}
	rng.Shuffle(len(all), func(i, j int) { all[i], all[j] = all[j], all[i] })
	want := append([]match(nil), all...)
	sort.Slice(want, func(i, j int) bool {
		if want[i].score == want[j].score {
			return want[i].id < want[j].id
		}
		return want[i].score > want[j].score
	})
	for _, k := range []int{1, 5, 10, 100} {
		var best matches
		for _, m := range all {
			best.offer(m, k)
		}
		seen := make(map[uint32]bool, k)
		for _, m := range best {
			seen[m.id] = true
		}
		if len(seen) != k {
			t.Fatalf("top %d contains duplicates", k)
		}
		for _, m := range want[:k] {
			if !seen[m.id] {
				t.Fatalf("top %d missed id %d with score %v", k, m.id, m.score)
			}
		}
	}
}

func TestReverseClassFilter(t *testing.T) {
	db := testDB(t, testFeatures())
	r, err := db.Reverse(context.Background(), Point{47.389, 8.54001}, ReverseOptions{Limit: 1, RadiusMeters: 5000, Class: "address"})
	if err != nil || len(r) != 1 || r[0].OSMID != 2 || r[0].Class != "address" {
		t.Fatalf("address filter: %+v %v", r, err)
	}
	r, err = db.Reverse(context.Background(), Point{47.389, 8.54001}, ReverseOptions{Limit: 1, RadiusMeters: 5000, Class: "nonexistent"})
	if err != nil || len(r) != 0 {
		t.Fatalf("unknown class: %+v %v", r, err)
	}
}
