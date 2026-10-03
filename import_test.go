package geocode

import (
	"bytes"
	"compress/zlib"
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

// This fixture encoder writes the public OSM PBF wire format directly, keeping
// the importer test independent of the production decoder implementation.
func wireVar(field int, n uint64) []byte {
	b := binary.AppendUvarint(nil, uint64(field<<3))
	return binary.AppendUvarint(b, n)
}
func wireBytes(field int, v []byte) []byte {
	b := binary.AppendUvarint(nil, uint64(field<<3|2))
	b = binary.AppendUvarint(b, uint64(len(v)))
	return append(b, v...)
}
func zig(n int64) uint64 { return uint64(n<<1) ^ uint64(n>>63) }
func packed(field int, values []uint64) []byte {
	var b []byte
	for _, v := range values {
		b = binary.AppendUvarint(b, v)
	}
	return wireBytes(field, b)
}
func message(parts ...[]byte) []byte { return bytes.Join(parts, nil) }
func writeBlock(t *testing.T, out *bytes.Buffer, kind string, payload []byte, compressed bool) {
	t.Helper()
	blob := wireBytes(1, payload)
	if compressed {
		var zipped bytes.Buffer
		z := zlib.NewWriter(&zipped)
		if _, e := z.Write(payload); e != nil {
			t.Fatal(e)
		}
		if e := z.Close(); e != nil {
			t.Fatal(e)
		}
		blob = message(wireVar(2, uint64(len(payload))), wireBytes(3, zipped.Bytes()))
	}
	header := message(wireBytes(1, []byte(kind)), wireVar(3, uint64(len(blob))))
	var size [4]byte
	binary.BigEndian.PutUint32(size[:], uint32(len(header)))
	out.Write(size[:])
	out.Write(header)
	out.Write(blob)
}

func tinyPBF(t *testing.T) []byte {
	t.Helper()
	var out bytes.Buffer
	writeBlock(t, &out, "OSMHeader", message(wireBytes(4, []byte("OsmSchema-V0.6")), wireBytes(4, []byte("DenseNodes"))), false)
	strings := []string{"", "name", "Teststrasse", "highway", "residential", "addr:housenumber", "10", "addr:street", "addr:postcode", "8000", "type", "boundary", "administrative", "admin_level", "8", "Teststadt", "2", "Testland", "ISO3166-1:alpha2", "TL", "outer", "Library", "amenity", "library", "12"}
	var table []byte
	for _, s := range strings {
		table = append(table, wireBytes(1, []byte(s))...)
	}
	tags := func(pairs ...uint64) []byte {
		var k, v []uint64
		for i := 0; i < len(pairs); i += 2 {
			k = append(k, pairs[i])
			v = append(v, pairs[i+1])
		}
		return message(packed(2, k), packed(3, v))
	}
	way := func(id uint64, refs []int64, tag []byte) []byte {
		var deltas []uint64
		prev := int64(0)
		for _, id := range refs {
			deltas = append(deltas, zig(id-prev))
			prev = id
		}
		return wireBytes(3, message(wireVar(1, id), tag, packed(8, deltas)))
	}
	ways := message(way(11, []int64{1, 2, 3}, nil), way(12, []int64{1, 4, 3}, nil), way(20, []int64{5, 6}, tags(1, 2, 3, 4)), way(21, []int64{8, 9, 10, 11, 8}, tags(1, 21, 22, 23, 5, 24, 7, 2)), way(99, []int64{1, 999}, tags(1, 2, 3, 4)))
	// Deliberately put ways before their nodes, and nodes out of ID order.
	writeBlock(t, &out, "OSMData", message(wireBytes(1, table), wireBytes(2, ways)), true)
	node := func(id int64, p Point) []byte {
		return wireBytes(1, message(wireVar(1, zig(id)), wireVar(8, zig(int64(quant(p.Lat)))), wireVar(9, zig(int64(quant(p.Lon))))))
	}
	nodes := message(node(2, Point{47, 9}), node(1, Point{47, 8}), node(4, Point{48, 8}), node(3, Point{48, 9}), node(5, Point{47.5, 8.2}), node(6, Point{47.5, 8.8}), node(8, Point{47.6, 8.5}), node(9, Point{47.6, 8.501}), node(10, Point{47.601, 8.501}), node(11, Point{47.601, 8.5}))
	dense := wireBytes(2, message(packed(1, []uint64{zig(7)}), packed(8, []uint64{zig(475000000)}), packed(9, []uint64{zig(85000000)}), packed(10, []uint64{5, 6, 7, 2, 8, 9, 0})))
	writeBlock(t, &out, "OSMData", message(wireBytes(1, table), wireBytes(2, nodes), wireBytes(2, dense)), true)
	relation := func(id uint64, tag []byte, refs []int64) []byte {
		var ids, roles, types []uint64
		var prev int64
		for _, r := range refs {
			ids = append(ids, zig(r-prev))
			prev = r
			roles = append(roles, 20)
			types = append(types, 1)
		}
		return wireBytes(4, message(wireVar(1, id), tag, packed(8, roles), packed(9, ids), packed(10, types)))
	}
	rels := message(relation(100, tags(10, 11, 11, 12, 13, 14, 1, 15), []int64{11, 12}), relation(200, tags(10, 11, 11, 12, 13, 16, 1, 17, 18, 19), []int64{11, 12}), relation(300, tags(10, 11, 11, 12, 13, 14, 1, 15), []int64{555}))
	writeBlock(t, &out, "OSMData", message(wireBytes(1, table), wireBytes(2, rels)), false)
	return out.Bytes()
}

func TestImportPBFEndToEnd(t *testing.T) {
	dir := t.TempDir()
	pbf, output := filepath.Join(dir, "tiny.pbf"), filepath.Join(dir, "tiny.db")
	if e := os.WriteFile(pbf, tinyPBF(t), 0600); e != nil {
		t.Fatal(e)
	}
	stats, e := ImportPBF(context.Background(), pbf, output, ImportOptions{Workers: 2})
	if e != nil {
		t.Fatal(e)
	}
	if stats.Nodes != 11 || stats.Features != 5 || stats.MissingNodeWays != 1 || stats.IncompleteRelations != 1 {
		t.Fatalf("stats %+v", stats)
	}
	db, e := Open(output)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	if e = db.Verify(); e != nil {
		t.Fatal(e)
	}
	r, e := db.Search(context.Background(), "Teststrasse 10 Teststadt", SearchOptions{Limit: 1})
	if e != nil || len(r) != 1 || r[0].OSMID != 7 || r[0].Address.CountryCode != "tl" {
		t.Fatalf("search %+v %v", r, e)
	}
	r, e = db.Reverse(context.Background(), Point{47.6005, 8.5005}, ReverseOptions{Limit: 1, RadiusMeters: 100})
	if e != nil || len(r) != 1 || r[0].OSMID != 21 || r[0].Distance != 0 {
		t.Fatalf("reverse %+v %v", r, e)
	}
	if _, e = ImportPBF(context.Background(), pbf, output, ImportOptions{}); e == nil {
		t.Fatal("overwrote existing snapshot")
	}
}
func TestImportTruncatedAndCancellation(t *testing.T) {
	dir := t.TempDir()
	pbf := filepath.Join(dir, "bad.pbf")
	b := tinyPBF(t)
	if e := os.WriteFile(pbf, b[:len(b)-9], 0600); e != nil {
		t.Fatal(e)
	}
	output := filepath.Join(dir, "bad.db")
	if _, e := ImportPBF(context.Background(), pbf, output, ImportOptions{Workers: 1}); e == nil {
		t.Fatal("accepted truncated PBF")
	}
	if _, e := os.Stat(output); !os.IsNotExist(e) {
		t.Fatal("published failed import")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if e := BuildContext(ctx, output, testFeatures()); e == nil {
		t.Fatal("ignored cancellation")
	}
}

func TestExternalNodeMerge(t *testing.T) {
	c := nodeCollector{dir: t.TempDir()}
	for _, ids := range [][]int64{{4, 1, 9}, {8, 2, 5}, {7, 3, 6}} {
		for _, id := range ids {
			if e := c.add(id, Point{float64(id), -float64(id)}); e != nil {
				t.Fatal(e)
			}
		}
		if e := c.flush(); e != nil {
			t.Fatal(e)
		}
	}
	idx, e := c.finish()
	if e != nil {
		t.Fatal(e)
	}
	defer idx.close()
	for id := int64(1); id <= 9; id++ {
		p, ok := idx.get(id)
		if !ok || p.Lat != float64(id) || p.Lon != -float64(id) {
			t.Fatalf("node %d %+v %v", id, p, ok)
		}
	}
	for _, id := range []int64{0, 10, 999} {
		if _, ok := idx.get(id); ok {
			t.Fatal("phantom node", id)
		}
	}
}
func TestNodeDuplicate(t *testing.T) {
	c := nodeCollector{dir: t.TempDir()}
	if e := c.add(1, Point{}); e != nil {
		t.Fatal(e)
	}
	if e := c.flush(); e != nil {
		t.Fatal(e)
	}
	if e := c.add(1, Point{}); e != nil {
		t.Fatal(e)
	}
	if idx, e := c.finish(); e == nil {
		idx.close()
		t.Fatal("accepted duplicate nodes")
	}
}
