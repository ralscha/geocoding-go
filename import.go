package geocode

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/paulmach/osm"
	osmpbf "github.com/ralscha/geocoding-go/internal/pbf"
)

type ImportOptions struct {
	Workers int
	TempDir string
	// Explicit fallbacks when an extract omits the enclosing country boundary.
	Country     string
	CountryCode string
	Progress    func(ImportProgress)
}
type ImportProgress struct {
	Stage    string        `json:"stage"`
	Objects  uint64        `json:"objects"`
	Features int           `json:"features"`
	Elapsed  time.Duration `json:"elapsed"`
}
type ImportStats struct {
	Nodes               uint64        `json:"nodes"`
	Ways                uint64        `json:"ways"`
	Relations           uint64        `json:"relations"`
	Features            int           `json:"features"`
	MissingNodeWays     uint64        `json:"missing_node_ways"`
	IncompleteRelations uint64        `json:"incomplete_relations"`
	Duration            time.Duration `json:"duration"`
}

// ImportPBF makes three streaming passes: relation membership, nodes, then
// ways. This supports arbitrary object ordering without retaining the raw PBF
// in memory. Node coordinates spill to disk; searchable features stay in RAM.
func ImportPBF(ctx context.Context, input, output string, opts ImportOptions) (stats ImportStats, err error) {
	start := time.Now()
	opts.CountryCode = strings.ToLower(opts.CountryCode)
	defer func() { stats.Duration = time.Since(start) }()
	if _, e := os.Stat(output); e == nil {
		return stats, fmt.Errorf("geocode: output already exists: %s", output)
	} else if !os.IsNotExist(e) {
		return stats, e
	}
	workers := opts.Workers
	if workers <= 0 {
		workers = min(runtime.GOMAXPROCS(0), 8)
	}
	if workers > 64 {
		return stats, fmt.Errorf("geocode: workers must be at most 64")
	}
	tmpRoot := opts.TempDir
	if tmpRoot == "" {
		tmpRoot = filepath.Dir(output)
		if err = os.MkdirAll(tmpRoot, 0755); err != nil {
			return stats, err
		}
	}
	tmp, err := os.MkdirTemp(tmpRoot, "geocode-import-*")
	if err != nil {
		return stats, err
	}
	defer os.RemoveAll(tmp)
	var features []Feature
	var relations []*osm.Relation
	needed := make(map[int64]bool)
	progress := func(stage string, n uint64) {
		if opts.Progress != nil {
			opts.Progress(ImportProgress{stage, n, len(features), time.Since(start)})
		}
	}
	scan := func(stage string, configure func(*osmpbf.Scanner), visit func(osm.Object) error) error {
		progress(stage, 0)
		f, e := os.Open(input)
		if e != nil {
			return e
		}
		defer f.Close()
		s := osmpbf.New(ctx, f, workers)
		defer s.Close()
		configure(s)
		header, e := s.Header()
		if e != nil {
			return e
		}
		if header == nil {
			return fmt.Errorf("geocode: missing PBF header")
		}
		if slices.Contains(header.RequiredFeatures, "HistoricalInformation") {
			return fmt.Errorf("geocode: history PBFs are unsupported; use a current snapshot")
		}
		var count uint64
		for s.Scan() {
			if e = visit(s.Object()); e != nil {
				return e
			}
			count++
			if count%1000000 == 0 {
				progress(stage, count)
			}
		}
		if e = s.Err(); e != nil {
			return e
		}
		if e = checkContext(ctx); e != nil {
			return e
		}
		progress(stage, count)
		return nil
	}
	err = scan("relations", func(s *osmpbf.Scanner) {
		s.SkipNodes = true
		s.SkipWays = true
		s.FilterRelation = func(r *osm.Relation) bool {
			typ := r.Tags.Find("type")
			return (typ == "boundary" || typ == "multipolygon") && interesting(r.Tags)
		}
	}, func(o osm.Object) error {
		r := o.(*osm.Relation)
		relations = append(relations, r)
		stats.Relations++
		for _, m := range r.Members {
			if m.Type == osm.TypeWay && (m.Role == "outer" || m.Role == "inner" || m.Role == "") {
				needed[m.Ref] = true
			}
		}
		return nil
	})
	if err != nil {
		return stats, fmt.Errorf("relations: %w", err)
	}
	collector := nodeCollector{dir: tmp}
	err = scan("nodes", func(s *osmpbf.Scanner) { s.SkipWays = true; s.SkipRelations = true }, func(o osm.Object) error {
		n := o.(*osm.Node)
		p := Point{n.Lat, n.Lon}
		if e := collector.add(int64(n.ID), p); e != nil {
			return e
		}
		stats.Nodes++
		if interesting(n.Tags) {
			f := tagFeature(int64(n.ID), "node", n.Tags)
			f.Point = p
			features = append(features, f)
		}
		return nil
	})
	if err != nil {
		return stats, fmt.Errorf("nodes: %w", err)
	}
	progress("node-index", stats.Nodes)
	nodes, err := collector.finish()
	if err != nil {
		return stats, err
	}
	defer nodes.close()
	shapes := make(map[int64]wayShape, len(needed))
	err = scan("ways", func(s *osmpbf.Scanner) {
		s.SkipNodes = true
		s.SkipRelations = true
		s.FilterWay = func(w *osm.Way) bool { return needed[int64(w.ID)] || interesting(w.Tags) }
	}, func(o osm.Object) error {
		w := o.(*osm.Way)
		stats.Ways++
		if len(w.Nodes) < 2 {
			return nil
		}
		points := make([]Point, 0, len(w.Nodes))
		for _, n := range w.Nodes {
			p, ok := nodes.get(int64(n.ID))
			if !ok {
				stats.MissingNodeWays++
				return nil
			}
			points = append(points, p)
		}
		if needed[int64(w.ID)] {
			shapes[int64(w.ID)] = wayShape{first: int64(w.Nodes[0].ID), last: int64(w.Nodes[len(w.Nodes)-1].ID), points: points}
		}
		if interesting(w.Tags) {
			f := tagFeature(int64(w.ID), "way", w.Tags)
			f.Geometry = [][]Point{points}
			f.Polygon = w.Nodes[0].ID == w.Nodes[len(w.Nodes)-1].ID && w.Tags.Find("area") != "no" && (w.Tags.Find("highway") == "" || w.Tags.Find("area") == "yes")
			f.Point = representative(points, f.Polygon)
			features = append(features, f)
		}
		return nil
	})
	if err != nil {
		return stats, fmt.Errorf("ways: %w", err)
	}
	nodes.close()
	nodes.data = nil
	progress("assemble-relations", uint64(len(relations)))
	for _, r := range relations {
		if err = checkContext(ctx); err != nil {
			return stats, err
		}
		rings, ok := assembleRelation(r, shapes)
		if !ok {
			stats.IncompleteRelations++
			continue
		}
		f := tagFeature(int64(r.ID), "relation", r.Tags)
		f.Geometry = rings
		f.Polygon = true
		f.Point = representative(rings[0], true)
		f.Point = ringsLabel(rings, f.Point.Lat)
		features = append(features, f)
	}
	relations = nil
	shapes = nil
	needed = nil
	progress("address-context", uint64(len(features)))
	if err = enrichAddresses(ctx, features, opts, workers); err != nil {
		return stats, err
	}
	if err = checkContext(ctx); err != nil {
		return stats, err
	}
	progress("build-database", uint64(len(features)))
	err = BuildContext(ctx, output, features)
	stats.Features = len(features)
	if err == nil {
		progress("done", uint64(len(features)))
	}
	return stats, err
}

func interesting(t osm.Tags) bool {
	if t.Find("addr:housenumber") != "" {
		return true
	}
	name := t.Find("name")
	if name == "" {
		name = t.Find("name:en")
	}
	if name == "" {
		return false
	}
	for _, k := range []string{"place", "highway", "amenity", "shop", "tourism", "leisure", "natural", "building", "railway", "aeroway", "waterway", "office", "historic", "man_made"} {
		if t.Find(k) != "" {
			return true
		}
	}
	return t.Find("boundary") == "administrative"
}
func tagFeature(id int64, typ string, t osm.Tags) Feature {
	f := Feature{OSMID: id, OSMType: typ, Name: t.Find("name"), Importance: 0.2}
	if f.Name == "" {
		f.Name = t.Find("name:en")
	}
	for _, tag := range t {
		if strings.HasPrefix(tag.Key, "name:") || tag.Key == "alt_name" || tag.Key == "official_name" || tag.Key == "short_name" {
			for a := range strings.SplitSeq(tag.Value, ";") {
				if a != f.Name && a != "" {
					f.Aliases = append(f.Aliases, a)
				}
			}
		}
	}
	f.Address = Address{HouseNumber: t.Find("addr:housenumber"), Street: t.Find("addr:street"), Postcode: t.Find("addr:postcode"), City: t.Find("addr:city"), County: t.Find("addr:county"), State: t.Find("addr:state"), Country: t.Find("addr:country")}
	if f.Address.Street == "" {
		f.Address.Street = t.Find("addr:place")
	}
	if f.Address.City == "" {
		f.Address.City = t.Find("addr:town")
	}
	if f.Address.City == "" {
		f.Address.City = t.Find("addr:village")
	}
	if len(f.Address.Country) == 2 {
		f.Address.CountryCode = strings.ToLower(f.Address.Country)
		f.Address.Country = ""
	}
	if t.Find("boundary") == "administrative" {
		f.Class = "boundary"
		f.Type = "administrative"
		f.AdminLevel, _ = strconv.Atoi(t.Find("admin_level"))
		if f.AdminLevel < 1 || f.AdminLevel > 12 {
			f.AdminLevel = 12
		}
		f.Importance = 0.5 + float64(12-f.AdminLevel)*0.025
		if f.AdminLevel == 2 {
			f.Address.Country = f.Name
			f.Address.CountryCode = strings.ToLower(t.Find("ISO3166-1:alpha2"))
			if f.Address.CountryCode == "" {
				f.Address.CountryCode = strings.ToLower(t.Find("ISO3166-1"))
			}
		}
	} else if f.Address.HouseNumber != "" {
		f.Class = "address"
		f.Type = "house"
		f.Importance = 0.65
	} else {
		for _, k := range []string{"place", "highway", "amenity", "shop", "tourism", "railway", "aeroway", "office", "historic", "leisure", "natural", "waterway", "building", "man_made"} {
			if v := t.Find(k); v != "" {
				f.Class = k
				f.Type = v
				break
			}
		}
		if f.Class == "highway" {
			f.Importance = 0.45
			f.Address.Street = f.Name
		}
		if f.Class == "place" {
			switch f.Type {
			case "country":
				f.Importance = 1
			case "state":
				f.Importance = .9
			case "city":
				f.Importance = .85
			case "town":
				f.Importance = .75
			case "village":
				f.Importance = .65
			default:
				f.Importance = .5
			}
			if p, e := strconv.ParseFloat(t.Find("population"), 64); e == nil && p > 0 {
				f.Importance = min(1, f.Importance+math.Log10(p)/100)
			}
		}
	}
	return f
}

func representative(p []Point, polygon bool) Point {
	if len(p) == 0 {
		return Point{}
	}
	if polygon && len(p) >= 4 {
		// Use a horizontal scanline through the bounding-box midpoint and choose
		// the midpoint of an interior interval, avoiding centroids outside a U.
		b := pointBox(p[0])
		for _, q := range p {
			b = b.union(pointBox(q))
		}
		lat := (float64(b.south) + float64(b.north)) / 2 / coordScale
		return polygonLabel(p, lat)
	}
	total := 0.0
	for i := 1; i < len(p); i++ {
		total += haversine(p[i-1], p[i])
	}
	target := total / 2
	for i := 1; i < len(p); i++ {
		d := haversine(p[i-1], p[i])
		if target <= d && d > 0 {
			t := target / d
			return Point{p[i-1].Lat + t*(p[i].Lat-p[i-1].Lat), wrap(p[i-1].Lon + t*wrap(p[i].Lon-p[i-1].Lon))}
		}
		target -= d
	}
	return p[0]
}
