package geocode

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func performanceBuildFeatures() []Feature {
	features := make([]Feature, 10000)
	for i := range features {
		f := Feature{
			OSMID: int64(i + 1), OSMType: "node", Class: "address", Type: "house", Importance: .65,
			Point: Point{47 + float64(i/100)*.0001, 8 + float64(i%100)*.0001},
			Address: Address{
				HouseNumber: fmt.Sprint(i%100 + 1), Street: fmt.Sprintf("Example Street %d", i/100),
				Postcode: "8000", City: fmt.Sprintf("Town %d", i%10), County: "Example County",
				State: "Example State", Country: "Testland", CountryCode: "tl",
			},
		}
		if i%10 == 0 {
			f.Name, f.Class, f.Type = "Library", "amenity", "library"
			f.Aliases = []string{"Reading Room"}
		}
		if i%4 == 0 {
			f.OSMType = "way"
			f.Geometry = [][]Point{{f.Point, {f.Point.Lat + .0001, f.Point.Lon + .0001}}}
		}
		features[i] = f
	}
	return features
}

func TestFeatureWeights(t *testing.T) {
	features := []Feature{
		{
			Name: "Main Main Street", Aliases: []string{"Alt Main"},
			Address: Address{HouseNumber: "12", Street: "Main Street", Postcode: "8000", City: "Town Main", County: "County", State: "State", Country: "Country", CountryCode: "CC"},
		},
		{Name: "Other"},
		{},
	}
	want := []map[string]uint32{
		{"main": 12, "street": 12, "alt": 9, "12": 12, "8000": 8, "town": 3, "county": 2, "state": 2, "country": 1, "cc": 1},
		{"other": 12},
		{},
	}
	cache := make(map[string][]string)
	weights := make(map[string]uint32)
	for i, feature := range features {
		featureWeights(feature, cache, weights)
		if !reflect.DeepEqual(weights, want[i]) {
			t.Fatalf("feature %d weights = %v, want %v", i, weights, want[i])
		}
	}
}

func TestDisplayNameFormatting(t *testing.T) {
	tests := []struct {
		name    string
		feature Feature
		want    string
	}{
		{"empty", Feature{}, ""},
		{"name", Feature{Name: "Library"}, "Library"},
		{"street_only", Feature{Address: Address{Street: "\t Main Street \u2003"}}, "Main Street"},
		{"number_only", Feature{Address: Address{HouseNumber: "\u2003 10\t"}}, "10"},
		{"interior_whitespace", Feature{Address: Address{Street: " Main Street ", HouseNumber: " 10 "}}, "Main Street   10"},
		{"duplicate_fields", Feature{Name: "Town", Address: Address{Street: "Town", City: "Town", County: "Town", State: "Town", Country: "Town"}}, "Town"},
		{"exact_duplicates_only", Feature{Name: "Town", Address: Address{City: "town", County: " Town ", State: "Town"}}, "Town, town,  Town "},
		{"address", Feature{Name: "Library", Address: Address{Street: "Main Street", HouseNumber: "10", Postcode: "8000", City: "Town", County: "County", State: "State", Country: "Country"}}, "Library, Main Street 10, 8000, Town, County, State, Country"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := displayName(test.feature); got != test.want {
				t.Fatalf("display name = %q, want %q", got, test.want)
			}
		})
	}
}

func BenchmarkFeatureWeights(b *testing.B) {
	features := performanceBuildFeatures()
	cache := make(map[string][]string)
	weights := make(map[string]uint32)
	for _, feature := range features {
		featureWeights(feature, cache, weights)
	}
	i := 0
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		featureWeights(features[i%len(features)], cache, weights)
		if len(weights) == 0 {
			b.Fatal("empty feature weights")
		}
		i++
	}
}

func BenchmarkDisplayName(b *testing.B) {
	for _, fixture := range []struct {
		name    string
		feature Feature
	}{
		{"place", Feature{Name: "Zürich", Address: Address{City: "Zürich", State: "Zürich", Country: "Switzerland"}}},
		{"street", Feature{Name: "Main Street", Address: Address{Street: "Main Street", City: "Town", State: "State", Country: "Country"}}},
		{"address", Feature{Name: "Library", Address: Address{Street: "Main Street", HouseNumber: "10", Postcode: "8000", City: "Town", County: "County", State: "State", Country: "Country"}}},
	} {
		b.Run(fixture.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if displayName(fixture.feature) == "" {
					b.Fatal("empty display name")
				}
			}
		})
	}
}

func BenchmarkBuild10K(b *testing.B) {
	features := performanceBuildFeatures()
	path := filepath.Join(b.TempDir(), "build.db")
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		if err := Build(path, features); err != nil {
			b.Fatal(err)
		}
		b.StopTimer()
		if err := os.Remove(path); err != nil {
			b.Fatal(err)
		}
		b.StartTimer()
	}
}

// The before and after benchmark binaries can also produce byte-identical
// fixtures, covering the complete serialized index, metadata, and geometry.
func TestBuildPerformanceSnapshot(t *testing.T) {
	path := os.Getenv("GEOCODE_BUILD_SNAPSHOT")
	if path == "" {
		t.Skip("set GEOCODE_BUILD_SNAPSHOT to write a comparison database")
	}
	if err := Build(path, performanceBuildFeatures()); err != nil {
		t.Fatal(err)
	}
}
