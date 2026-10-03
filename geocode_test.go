package geocode

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"math"
	"math/rand/v2"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
)

func testDB(t testing.TB, features []Feature) *DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	if e := Build(path, features); e != nil {
		t.Fatal(e)
	}
	db, e := Open(path)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { db.Close() })
	if e = db.Verify(); e != nil {
		t.Fatal(e)
	}
	return db
}
func testFeatures() []Feature {
	return []Feature{
		{OSMID: 1, OSMType: "node", Name: "Zürich", Class: "place", Type: "city", Point: Point{47.3769, 8.5417}, Importance: .9, Address: Address{Country: "Schweiz", CountryCode: "ch"}},
		{OSMID: 2, OSMType: "node", Class: "address", Type: "house", Point: Point{47.37, 8.54}, Importance: .65, Address: Address{HouseNumber: "10", Street: "Bahnhofstrasse", City: "Zürich", Postcode: "8001", CountryCode: "ch"}},
		{OSMID: 3, OSMType: "node", Class: "address", Type: "house", Point: Point{46.95, 7.44}, Importance: .65, Address: Address{HouseNumber: "10", Street: "Bahnhofstrasse", City: "Bern", Postcode: "3000"}},
		{OSMID: 4, OSMType: "way", Name: "Bahnhofstrasse", Class: "highway", Type: "residential", Point: Point{47.375, 8.54}, Geometry: [][]Point{{{47.36, 8.54}, {47.39, 8.54}}}, Importance: .45, Address: Address{Street: "Bahnhofstrasse", City: "Zürich"}},
		{OSMID: 5, OSMType: "node", Name: "Café Straße", Aliases: []string{"Coffee House"}, Class: "amenity", Type: "cafe", Point: Point{47.4, 8.5}, Importance: .2},
	}
}

func TestSearch(t *testing.T) {
	db := testDB(t, testFeatures())
	ctx := context.Background()
	for _, tt := range []struct {
		q      string
		prefix bool
		want   int64
	}{
		{"bahnhofstrasse 10 zurich", false, 2}, {"8001 10", false, 2}, {"10 bahnhofstrasse bern", false, 3}, {"ZURICH", false, 1}, {"cafe strasse", false, 5}, {"coffee house", false, 5}, {"10 zurich bahnhof", true, 2}, {"zürich zürich", false, 1},
	} {
		t.Run(tt.q, func(t *testing.T) {
			got, e := db.Search(ctx, tt.q, SearchOptions{Limit: 1, Prefix: tt.prefix})
			if e != nil || len(got) != 1 || got[0].OSMID != tt.want {
				t.Fatalf("got %+v, %v; want %d", got, e, tt.want)
			}
		})
	}
	for _, q := range []string{"", "notpresent", "bahnhofstrasse missing", "10 zur"} {
		r, e := db.Search(ctx, q, SearchOptions{})
		if e != nil || len(r) != 0 {
			t.Fatalf("%q: %+v %v", q, r, e)
		}
	}
	b := [4]float64{46, 7, 47, 8}
	r, e := db.Search(ctx, "10 Bahnhofstrasse", SearchOptions{Bounds: &b})
	if e != nil || len(r) != 1 || r[0].OSMID != 3 {
		t.Fatalf("bounds %+v %v", r, e)
	}
	near := Point{46.95, 7.44}
	r, e = db.Search(ctx, "10 Bahnhofstrasse", SearchOptions{Near: &near, Limit: 1})
	if e != nil || r[0].OSMID != 3 {
		t.Fatalf("near %+v %v", r, e)
	}
}

func TestReverseUsesStreetGeometry(t *testing.T) {
	db := testDB(t, testFeatures())
	r, e := db.Reverse(context.Background(), Point{47.389, 8.54001}, ReverseOptions{Limit: 1, RadiusMeters: 20})
	if e != nil || len(r) != 1 || r[0].OSMID != 4 || r[0].Distance > 2 {
		t.Fatalf("%+v %v", r, e)
	}
}
func TestPolygonHoleAndDateline(t *testing.T) {
	f := Feature{OSMID: 10, OSMType: "way", Name: "Park", Class: "leisure", Type: "park", Polygon: true, Point: Point{.5, .5}, Geometry: [][]Point{{{0, 0}, {0, 4}, {4, 4}, {4, 0}, {0, 0}}, {{1, 1}, {3, 1}, {3, 3}, {1, 3}, {1, 1}}}}
	d := Feature{OSMID: 11, OSMType: "way", Name: "Island", Class: "natural", Type: "land", Polygon: true, Point: Point{0, 179.5}, Geometry: [][]Point{{{-1, 179}, {-1, -179}, {1, -179}, {1, 179}, {-1, 179}}}}
	db := testDB(t, []Feature{f, d})
	for _, tt := range []struct {
		p      Point
		id     uint32
		inside bool
	}{{Point{.5, .5}, 0, true}, {Point{2, 2}, 0, false}, {Point{0, 180}, 1, true}, {Point{0, -179.5}, 1, true}, {Point{0, 0}, 1, false}} {
		dist, e := db.distance(tt.id, tt.p)
		if e != nil || (dist == 0) != tt.inside {
			t.Fatalf("%+v => %v %v", tt, dist, e)
		}
	}
	r, e := db.Reverse(context.Background(), Point{0, -179.9}, ReverseOptions{Limit: 1, RadiusMeters: 10})
	if e != nil || len(r) != 1 || r[0].OSMID != 11 {
		t.Fatalf("dateline %+v %v", r, e)
	}
	prepared := prepareBoundary(0, f)
	if !prepared.contains(Point{.5, .5}) || prepared.contains(Point{2, 2}) {
		t.Fatal("prepared polygon lost hole")
	}
}

func TestReverseAgainstBruteForce(t *testing.T) {
	rng := rand.New(rand.NewPCG(42, 99))
	features := make([]Feature, 4000)
	for i := range features {
		p := Point{rng.Float64()*170 - 85, rng.Float64()*360 - 180}
		features[i] = Feature{OSMID: int64(i + 1), OSMType: "node", Name: "point", Class: "place", Type: "locality", Point: p}
	}
	db := testDB(t, features)
	for range 80 {
		p := Point{rng.Float64()*180 - 90, rng.Float64()*360 - 180}
		want := make([]match, len(features))
		for i, f := range features {
			want[i] = match{id: uint32(i), distance: haversine(p, f.Point)}
		}
		sort.Slice(want, func(i, j int) bool { return want[i].distance < want[j].distance })
		got, e := db.Reverse(context.Background(), p, ReverseOptions{Limit: 7, RadiusMeters: math.Pi * earthRadius})
		if e != nil || len(got) != 7 {
			t.Fatalf("%+v %v", got, e)
		}
		for i := range got {
			if got[i].OSMID != int64(want[i].id+1) {
				t.Fatalf("query %+v result %d: got %d want %d", p, i, got[i].OSMID, want[i].id+1)
			}
		}
	}
}

func TestBoxLowerBound(t *testing.T) {
	rng := rand.New(rand.NewPCG(3, 5))
	for range 20000 {
		a, b := Point{rng.Float64()*180 - 90, rng.Float64()*360 - 180}, Point{rng.Float64()*180 - 90, rng.Float64()*360 - 180}
		bounds := pointBox(a).union(pointBox(b))
		p := Point{rng.Float64()*180 - 90, rng.Float64()*360 - 180}
		q := Point{a.Lat + (b.Lat-a.Lat)*rng.Float64(), a.Lon + (b.Lon-a.Lon)*rng.Float64()}
		if bounds.lowerBound(p) > haversine(p, q)+.1 {
			t.Fatalf("invalid bound %+v %+v %+v", bounds, p, q)
		}
	}
}

func TestInvalidClosedCanceled(t *testing.T) {
	db := testDB(t, testFeatures())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e := db.Search(ctx, "zurich", SearchOptions{}); !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
	if _, e := db.Reverse(ctx, Point{}, ReverseOptions{}); !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
	for _, p := range []Point{{91, 0}, {0, 181}, {math.NaN(), 0}, {0, math.Inf(1)}} {
		if _, e := db.Reverse(context.Background(), p, ReverseOptions{}); !errors.Is(e, ErrInvalidCoordinate) {
			t.Fatal(e)
		}
	}
	if e := db.Close(); e != nil {
		t.Fatal(e)
	}
	if _, e := db.Search(context.Background(), "zurich", SearchOptions{}); !errors.Is(e, ErrClosed) {
		t.Fatal(e)
	}
	if e := db.Close(); e != nil {
		t.Fatal(e)
	}
}
func TestConcurrentQueriesAndClose(t *testing.T) {
	db := testDB(t, testFeatures())
	var wg sync.WaitGroup
	for range 12 {
		wg.Go(func() {
			for range 30 {
				_, e := db.Search(context.Background(), "zurich", SearchOptions{})
				if e != nil && !errors.Is(e, ErrClosed) {
					t.Error(e)
				}
				_, e = db.Reverse(context.Background(), Point{47.37, 8.54}, ReverseOptions{})
				if e != nil && !errors.Is(e, ErrClosed) {
					t.Error(e)
				}
			}
		})
	}
	_ = db.Close()
	wg.Wait()
}

func TestDatabaseFormatV1(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v1.db")
	if err := Build(path, testFeatures()); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if magic, version := string(data[:8]), binary.LittleEndian.Uint32(data[8:12]); magic != "GEOCODB1" || version != 1 {
		t.Fatalf("unexpected format: magic=%q version=%d", magic, version)
	}
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Verify(); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name, magic string
		version     uint32
	}{
		{"wrong_magic", "GEOCODB2", 1},
		{"wrong_version", "GEOCODB1", 2},
		{"development_format", "GEOCODB3", 3},
	} {
		t.Run(tt.name, func(t *testing.T) {
			modified := append([]byte(nil), data...)
			copy(modified[:8], tt.magic)
			binary.LittleEndian.PutUint32(modified[8:12], tt.version)
			// Keep the checksum valid so rejection specifically tests the format.
			binary.LittleEndian.PutUint32(modified[252:256], crc32.Checksum(modified[:252], crc32.MakeTable(crc32.Castagnoli)))
			path := filepath.Join(t.TempDir(), "unsupported.db")
			if err := os.WriteFile(path, modified, 0o600); err != nil {
				t.Fatal(err)
			}
			db, err := Open(path)
			if err == nil {
				_ = db.Close()
				t.Fatal("accepted unsupported format")
			}
			if !strings.Contains(err.Error(), "unsupported format") {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestCorruptionAndNoOverwrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "db")
	if e := Build(path, testFeatures()); e != nil {
		t.Fatal(e)
	}
	if e := Build(path, nil); e == nil {
		t.Fatal("overwrote database")
	}
	b, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	for _, length := range []int{0, 8, 255, len(b) - 1} {
		p := filepath.Join(dir, fmt.Sprint(length))
		if e = os.WriteFile(p, b[:length], 0600); e != nil {
			t.Fatal(e)
		}
		if db, e := Open(p); e == nil {
			db.Close()
			t.Fatalf("opened truncated file %d", length)
		}
	}
	b[len(b)-1] ^= 0x80
	bad := filepath.Join(dir, "bad")
	if e = os.WriteFile(bad, b, 0600); e != nil {
		t.Fatal(e)
	}
	db, e := Open(bad)
	if e == nil {
		defer db.Close()
		if e = db.Verify(); e == nil {
			t.Fatal("accepted corruption")
		}
	}
}

func TestPrefixBreadth(t *testing.T) {
	features := make([]Feature, 300)
	for i := range features {
		features[i] = Feature{OSMID: int64(i + 1), OSMType: "node", Name: fmt.Sprintf("ab%04d", i), Class: "place", Type: "locality"}
	}
	db := testDB(t, features)
	if _, e := db.Search(context.Background(), "ab", SearchOptions{Prefix: true}); !errors.Is(e, ErrQueryTooBroad) {
		t.Fatal(e)
	}
}
func TestEmptyDatabase(t *testing.T) {
	db := testDB(t, nil)
	r, e := db.Search(context.Background(), "test", SearchOptions{})
	if e != nil || len(r) != 0 {
		t.Fatal(r, e)
	}
	r, e = db.Reverse(context.Background(), Point{}, ReverseOptions{})
	if e != nil || len(r) != 0 {
		t.Fatal(r, e)
	}
}

func TestCommonTermAccelerationMatchesFullRanking(t *testing.T) {
	features := make([]Feature, 4000)
	for i := range features {
		f := Feature{OSMID: int64(i + 1), OSMType: "node", Class: "place", Type: "locality", Point: Point{47, 8}, Importance: float64(i%101) / 100, Address: Address{City: "Common"}}
		if i%7 == 0 {
			f.Name = "Common"
		}
		features[i] = f
	}
	db := testDB(t, features)
	bounds := [4]float64{-90, -180, 90, 180}
	fast, e := db.Search(context.Background(), "Common", SearchOptions{Limit: 100})
	if e != nil {
		t.Fatal(e)
	}
	full, e := db.Search(context.Background(), "Common", SearchOptions{Limit: 100, Bounds: &bounds})
	if e != nil {
		t.Fatal(e)
	}
	if len(fast) != 100 || len(full) != 100 {
		t.Fatal("wrong result counts")
	}
	for i := range fast {
		if fast[i].OSMID != full[i].OSMID || fast[i].Score != full[i].Score {
			t.Fatalf("rank %d: %+v != %+v", i, fast[i], full[i])
		}
	}
}

func TestResultsSurviveClose(t *testing.T) {
	db := testDB(t, testFeatures())
	r, e := db.Search(context.Background(), "zurich", SearchOptions{Limit: 1})
	if e != nil {
		t.Fatal(e)
	}
	if e = db.Close(); e != nil {
		t.Fatal(e)
	}
	if r[0].Name != "Zürich" || r[0].Address.Country != "Schweiz" {
		t.Fatal(r)
	}
}

func TestHTTP(t *testing.T) {
	db := testDB(t, testFeatures())
	for _, tt := range []struct {
		url    string
		status int
	}{
		{"/search?q=zurich", 200}, {"/search?street=Bahnhofstrasse+10&city=Zurich", 200}, {"/search?q=zurich&limit=0", 400}, {"/search?q=zurich&format=xml", 400}, {"/search?q=zurich&lat=1", 400}, {"/search", 400}, {"/reverse?lat=47.389&lon=8.54001", 200}, {"/reverse?lat=NaN&lon=8", 400}, {"/reverse?lat=0&lon=0&radius=10", 404}, {"/health", 200},
	} {
		r := httptest.NewRequest("GET", tt.url, nil)
		w := httptest.NewRecorder()
		db.Handler().ServeHTTP(w, r)
		if w.Code != tt.status {
			t.Errorf("%s => %d %s", tt.url, w.Code, w.Body.String())
		}
		if w.Header().Get("X-Data-Attribution") == "" {
			t.Error("missing attribution")
		}
	}
}

func FuzzNormalize(f *testing.F) {
	for _, s := range []string{"Zürich", "Straße", "東京", "a\x00b", ""} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		n := Normalize(s)
		if Normalize(n) != n {
			t.Fatalf("not idempotent: %q => %q", s, n)
		}
	})
}
