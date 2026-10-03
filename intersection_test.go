package geocode

import (
	"context"
	"fmt"
	"testing"
)

func TestPostingIntersectionSkipsWithoutLosingResults(t *testing.T) {
	features := make([]Feature, 5000)
	for i := range features {
		features[i] = Feature{OSMID: int64(i + 1), OSMType: "node", Name: fmt.Sprintf("a%d b%d c%d", i%13, i%19, i%7), Class: "place", Type: "locality"}
	}
	db := testDB(t, features)
	for a := range 13 {
		for b := range 19 {
			var want []int64
			for i := range features {
				if i%13 == a && i%19 == b {
					want = append(want, int64(i+1))
				}
			}
			r, e := db.Search(context.Background(), fmt.Sprintf("a%d b%d", a, b), SearchOptions{Limit: 100})
			if e != nil || len(r) != len(want) {
				t.Fatalf("a%d b%d: %d != %d (%v)", a, b, len(r), len(want), e)
			}
			for i := range r {
				if r[i].OSMID != want[i] {
					t.Fatalf("lost match: %d != %d", r[i].OSMID, want[i])
				}
			}
		}
	}
}
