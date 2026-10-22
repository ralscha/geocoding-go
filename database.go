package geocode

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/edsrzf/mmap-go"
)

const (
	formatMagic   = "GEOCODB1"
	formatVersion = 1
	headerSize    = 256
	recordSize    = 64
	metadataSize  = 44
	termSize      = 24
	treeNodeSize  = 32
	sectionCount  = 8
	sRecords      = 0
	sMetadata     = 1
	sGeometry     = 2
	sTerms        = 3
	sStrings      = 4
	sPostings     = 5
	sTree         = 6
	sOrder        = 7
)

var le = binary.LittleEndian
var castagnoli = crc32.MakeTable(crc32.Castagnoli)

// DB owns a read-only mapping. Close waits for active queries; callers must not
// modify or truncate the underlying file. Publish a new path for each rebuild.
type DB struct {
	mu       sync.RWMutex
	data     mmap.MMap
	sections [sectionCount][]byte
	count    uint32
	closed   bool
}
type Stats struct {
	Features     uint32 `json:"features"`
	Terms        int    `json:"terms"`
	Bytes        int    `json:"bytes"`
	SpatialNodes int    `json:"spatial_nodes"`
}

type contextWriter struct {
	ctx    context.Context
	writer io.Writer
}

func (w contextWriter) Write(p []byte) (int, error) {
	if e := checkContext(w.ctx); e != nil {
		return 0, e
	}
	return w.writer.Write(p)
}

// Open maps and structurally validates a database. Verify additionally checks
// section checksums and posting order; it is useful after transfer or backup.
func Open(path string) (*DB, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	stat, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if stat.Size() < headerSize {
		return nil, errors.New("geocode: truncated database")
	}
	data, err := mmap.Map(f, mmap.RDONLY, 0)
	if err != nil {
		return nil, err
	}
	db := &DB{data: data}
	if err = db.validate(); err != nil {
		data.Unmap()
		return nil, err
	}
	return db, nil
}
func (db *DB) Close() error {
	db.mu.Lock()
	defer db.mu.Unlock()
	if db.closed {
		return nil
	}
	db.closed = true
	err := db.data.Unmap()
	db.data = nil
	db.sections = [sectionCount][]byte{}
	return err
}
func (db *DB) Stats() Stats {
	db.mu.RLock()
	defer db.mu.RUnlock()
	return Stats{db.count, len(db.sections[sTerms]) / termSize, len(db.data), len(db.sections[sTree]) / treeNodeSize}
}
func corrupt(s string) error        { return fmt.Errorf("geocode: corrupt database: %s", s) }
func fits(off, n, size uint64) bool { return off <= size && n <= size-off }

func (db *DB) validate() error {
	h := db.data[:headerSize]
	if string(h[:8]) != formatMagic || le.Uint32(h[8:]) != formatVersion {
		return corrupt("unsupported format")
	}
	if le.Uint32(h[252:]) != crc32.Checksum(h[:252], castagnoli) {
		return corrupt("header checksum")
	}
	if le.Uint64(h[16:]) != uint64(len(db.data)) {
		return corrupt("file size")
	}
	db.count = le.Uint32(h[12:])
	end := uint64(headerSize)
	for i := range sectionCount {
		off, n := le.Uint64(h[24+i*16:]), le.Uint64(h[32+i*16:])
		if off < end || !fits(off, n, uint64(len(db.data))) {
			return corrupt("section bounds")
		}
		db.sections[i] = db.data[off : off+n]
		end = off + n
	}
	if len(db.sections[sRecords]) != int(db.count)*recordSize || len(db.sections[sTerms])%termSize != 0 || len(db.sections[sTree])%treeNodeSize != 0 || len(db.sections[sOrder]) != int(db.count)*4 {
		return corrupt("section sizes")
	}
	if db.count > 0 && len(db.sections[sTree]) == 0 {
		return corrupt("missing spatial tree")
	}
	for id := uint32(0); id < db.count; id++ {
		r := db.record(id)
		if !fits(le.Uint64(r[56:]), uint64(le.Uint32(r[52:])), uint64(len(db.sections[sMetadata]))) || !fits(le.Uint64(r[40:]), uint64(le.Uint32(r[48:])), uint64(len(db.sections[sGeometry]))) {
			return corrupt("record offsets")
		}
		importance := math.Float32frombits(le.Uint32(r[12:]))
		if math.IsNaN(float64(importance)) || importance < 0 || importance > 1 {
			return corrupt("record importance")
		}
		if !validPoint(recordPoint(r)) || !validBox(readBox(r[24:])) {
			return corrupt("record coordinates")
		}
		if r[8] > 2 || le.Uint32(r[52:]) != metadataSize {
			return corrupt("record metadata")
		}
	}
	var previous []byte
	for i := 0; i < len(db.sections[sTerms])/termSize; i++ {
		r := db.sections[sTerms][i*termSize:]
		off, n := le.Uint64(r), uint64(le.Uint32(r[8:]))
		po, pn := le.Uint64(r[16:]), uint64(le.Uint32(r[12:]))*8
		if pn/8 > 256 {
			pn += 100 * 8
		}
		if !fits(off, n, uint64(len(db.sections[sStrings]))) || !fits(po, pn, uint64(len(db.sections[sPostings]))) {
			return corrupt("term offsets")
		}
		term := db.sections[sStrings][off : off+n]
		if len(term) == 0 || (i > 0 && string(previous) >= string(term)) {
			return corrupt("term order")
		}
		previous = term
	}
	nodes := uint32(len(db.sections[sTree]) / treeNodeSize)
	for i := range nodes {
		n := db.treeNode(i)
		if !validBox(n.bounds) {
			return corrupt("spatial bounds")
		}
		if n.count > 0 {
			if !fits(uint64(n.start), uint64(n.count), uint64(db.count)) {
				return corrupt("spatial leaf")
			}
		} else if n.left <= i || n.right <= i || n.left >= nodes || n.right >= nodes {
			return corrupt("spatial children")
		}
	}
	for i := 0; i < len(db.sections[sOrder]); i += 4 {
		if le.Uint32(db.sections[sOrder][i:]) >= db.count {
			return corrupt("spatial feature")
		}
	}
	return nil
}
func validBox(b box) bool {
	return b.south >= -900000000 && b.north <= 900000000 && b.west >= -1800000000 && b.east <= 1800000000 && b.south <= b.north && b.west <= b.east
}
func (db *DB) Verify() error {
	db.mu.RLock()
	defer db.mu.RUnlock()
	if db.closed {
		return ErrClosed
	}
	for i, s := range db.sections {
		if crc32.Checksum(s, castagnoli) != le.Uint32(db.data[152+i*4:]) {
			return corrupt(fmt.Sprintf("section %d checksum", i))
		}
	}
	for i := 0; i < len(db.sections[sTerms])/termSize; i++ {
		p := db.posting(i)
		var prev uint32
		for j := 0; j < len(p); j += 8 {
			id := le.Uint32(p[j:])
			if id >= db.count || (j > 0 && id <= prev) {
				return corrupt("posting order")
			}
			prev = id
		}
	}
	return nil
}
func (db *DB) record(id uint32) []byte {
	return db.sections[sRecords][int(id)*recordSize : int(id+1)*recordSize]
}
func recordPoint(r []byte) Point {
	return Point{float64(int32(le.Uint32(r[16:]))) / coordScale, float64(int32(le.Uint32(r[20:]))) / coordScale}
}
func readBox(b []byte) box {
	return box{int32(le.Uint32(b)), int32(le.Uint32(b[4:])), int32(le.Uint32(b[8:])), int32(le.Uint32(b[12:]))}
}
func putBox(b []byte, v box) {
	le.PutUint32(b, uint32(v.south))
	le.PutUint32(b[4:], uint32(v.west))
	le.PutUint32(b[8:], uint32(v.north))
	le.PutUint32(b[12:], uint32(v.east))
}
func (db *DB) treeNode(id uint32) treeNode {
	r := db.sections[sTree][int(id)*treeNodeSize:]
	return treeNode{readBox(r), le.Uint32(r[16:]), le.Uint32(r[20:]), le.Uint32(r[24:]), le.Uint32(r[28:])}
}
func (db *DB) metadata(id uint32) (Feature, error) {
	r := db.record(id)
	off, n := le.Uint64(r[56:]), le.Uint32(r[52:])
	var f Feature
	a := &f.Address
	fields := []*string{&f.Name, &f.Class, &f.Type, &a.HouseNumber, &a.Street, &a.Postcode, &a.City, &a.County, &a.State, &a.Country, &a.CountryCode}
	b := db.sections[sMetadata][off : off+uint64(n)]
	// Copy all returned fields into one owned backing string. None of the result
	// strings refer to the mapping, so they remain valid after Close.
	var values [11][]byte
	var ends [11]int
	total := 0
	for i := range fields {
		v, err := db.poolBytes(le.Uint32(b[i*4:]))
		if err != nil {
			return f, err
		}
		values[i] = v
		total += len(v)
		ends[i] = total
	}
	var text strings.Builder
	text.Grow(total)
	for _, v := range values {
		text.Write(v)
	}
	owned := text.String()
	start := 0
	for i, dst := range fields {
		*dst = owned[start:ends[i]]
		start = ends[i]
	}
	f.OSMID = int64(le.Uint64(r))
	f.OSMType = [3]string{"node", "way", "relation"}[r[8]]
	f.Importance = float64(math.Float32frombits(le.Uint32(r[12:])))
	f.Point = recordPoint(r)
	f.Polygon = r[9]&1 != 0
	f.AdminLevel = int(r[10])
	return f, nil
}
func (db *DB) poolBytes(offset uint32) ([]byte, error) {
	p := db.sections[sStrings]
	off := uint64(offset)
	if !fits(off, 4, uint64(len(p))) {
		return nil, corrupt("string offset")
	}
	n := uint64(le.Uint32(p[off:]))
	off += 4
	if !fits(off, n, uint64(len(p))) {
		return nil, corrupt("string length")
	}
	return p[off : off+n], nil
}

func (db *DB) hasClass(r []byte, class string) (bool, error) {
	off := le.Uint64(r[56:])
	b, err := db.poolBytes(le.Uint32(db.sections[sMetadata][off+4:]))
	if err != nil {
		return false, err
	}
	// Comparing the temporary conversion does not allocate or expose mapped data.
	return string(b) == class, nil
}

type stringPool struct {
	offsets map[string]uint32
	data    []byte
}

func (p *stringPool) add(s string) (uint32, error) {
	if off, ok := p.offsets[s]; ok {
		return off, nil
	}
	if uint64(len(p.data))+uint64(len(s))+4 > math.MaxUint32 {
		return 0, errors.New("geocode: string pool exceeds 4 GiB")
	}
	off := uint32(len(p.data))
	p.data = le.AppendUint32(p.data, uint32(len(s)))
	p.data = append(p.data, s...)
	p.offsets[s] = off
	return off, nil
}
func (db *DB) termBytes(i int) []byte {
	r := db.sections[sTerms][i*termSize:]
	off, n := le.Uint64(r), le.Uint32(r[8:])
	return db.sections[sStrings][off : off+uint64(n)]
}
func (db *DB) topPosting(i int) []byte {
	r := db.sections[sTerms][i*termSize:]
	n := uint64(le.Uint32(r[12:]))
	if n <= 256 {
		return nil
	}
	off := le.Uint64(r[16:]) + n*8
	return db.sections[sPostings][off : off+800]
}
func (db *DB) posting(i int) []byte {
	r := db.sections[sTerms][i*termSize:]
	off, n := le.Uint64(r[16:]), uint64(le.Uint32(r[12:]))*8
	return db.sections[sPostings][off : off+n]
}

type postingEntry struct{ id, weight uint32 }

func featureWeights(f Feature, cache map[string][]string, weights map[string]uint32) {
	clear(weights)
	add := func(s string, w uint32) {
		if s == "" {
			return
		}
		tokens, ok := cache[s]
		if !ok {
			tokens = splitTerms(s)
			if len(cache) < 500000 {
				cache[s] = tokens
			}
		}
		for _, t := range tokens {
			weights[t] = max(weights[t], w)
		}
	}
	add(f.Name, 12)
	for _, a := range f.Aliases {
		add(a, 9)
	}
	a := f.Address
	add(a.HouseNumber, 12)
	add(a.Street, 10)
	add(a.Postcode, 8)
	add(a.City, 3)
	add(a.County, 2)
	add(a.State, 2)
	add(a.Country, 1)
	add(a.CountryCode, 1)
}

// Build writes a complete snapshot using streaming sections and a checksummed
// header. Publication uses a hard link with no replacement: an existing database
// is never destroyed, and readers cannot observe a partial database.
func Build(path string, features []Feature) (err error) {
	return BuildContext(context.Background(), path, features)
}

// BuildContext is Build with cancellation during validation and section writes.
func BuildContext(ctx context.Context, path string, features []Feature) (err error) {
	if uint64(len(features)) > math.MaxUint32 {
		return fmt.Errorf("geocode: too many features for format v%d", formatVersion)
	}
	for i, f := range features {
		if i%4096 == 0 {
			if err = checkContext(ctx); err != nil {
				return err
			}
		}
		if !validPoint(f.Point) || math.IsNaN(f.Importance) || math.IsInf(f.Importance, 0) || f.Importance < 0 || f.Importance > 1 || f.AdminLevel < 0 || f.AdminLevel > 255 {
			return fmt.Errorf("geocode: invalid feature %d", i)
		}
		for _, p := range f.Geometry {
			for _, v := range p {
				if !validPoint(v) {
					return fmt.Errorf("geocode: invalid geometry %d", i)
				}
			}
		}
	}
	if err = os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".geocode-build-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer func() { f.Close(); os.Remove(tmp) }()
	if _, err = f.Write(make([]byte, headerSize)); err != nil {
		return err
	}
	var h [headerSize]byte
	copy(h[:8], formatMagic)
	le.PutUint32(h[8:], formatVersion)
	le.PutUint32(h[12:], uint32(len(features)))
	writeSection := func(idx int, fn func(io.Writer) error) error {
		if e := checkContext(ctx); e != nil {
			return e
		}
		off, e := f.Seek(0, io.SeekCurrent)
		if e != nil {
			return e
		}
		hash := crc32.New(castagnoli)
		bw := bufio.NewWriterSize(io.MultiWriter(contextWriter{ctx, f}, hash), 1024*1024)
		if e = fn(bw); e != nil {
			return e
		}
		if e = bw.Flush(); e != nil {
			return e
		}
		end, e := f.Seek(0, io.SeekCurrent)
		if e != nil {
			return e
		}
		le.PutUint64(h[24+idx*16:], uint64(off))
		le.PutUint64(h[32+idx*16:], uint64(end-off))
		le.PutUint32(h[152+idx*4:], hash.Sum32())
		return nil
	}
	// Fixed records and interned strings keep repeated address context compact.
	records := make([]byte, len(features)*recordSize)
	boxes := make([]box, len(features))
	var goff uint64
	for i, feat := range features {
		if i%4096 == 0 {
			if err = checkContext(ctx); err != nil {
				return err
			}
		}
		gn := geometrySize(feat.Geometry)
		if gn > math.MaxUint32 {
			return errors.New("geocode: feature exceeds format limits")
		}
		r := records[i*recordSize:]
		le.PutUint64(r, uint64(feat.OSMID))
		switch feat.OSMType {
		case "node":
			r[8] = 0
		case "way":
			r[8] = 1
		case "relation":
			r[8] = 2
		default:
			return fmt.Errorf("geocode: unsupported OSM type %q", feat.OSMType)
		}
		if feat.Polygon {
			r[9] = 1
		}
		r[10] = byte(feat.AdminLevel)
		le.PutUint32(r[12:], math.Float32bits(float32(feat.Importance)))
		le.PutUint32(r[16:], uint32(quant(feat.Point.Lat)))
		le.PutUint32(r[20:], uint32(quant(feat.Point.Lon)))
		boxes[i] = featureBox(feat)
		putBox(r[24:], boxes[i])
		le.PutUint64(r[40:], goff)
		le.PutUint32(r[48:], uint32(gn))
		le.PutUint32(r[52:], metadataSize)
		le.PutUint64(r[56:], uint64(i)*metadataSize)
		goff += gn
	}
	if err = writeSection(sRecords, func(w io.Writer) error { _, e := w.Write(records); return e }); err != nil {
		return err
	}
	records = nil
	pool := stringPool{offsets: make(map[string]uint32)}
	if err = writeSection(sMetadata, func(w io.Writer) error {
		var b [metadataSize]byte
		for i, feat := range features {
			if i%4096 == 0 {
				if e := checkContext(ctx); e != nil {
					return e
				}
			}
			a := feat.Address
			values := []string{feat.Name, feat.Class, feat.Type, a.HouseNumber, a.Street, a.Postcode, a.City, a.County, a.State, a.Country, a.CountryCode}
			for i, v := range values {
				off, e := pool.add(v)
				if e != nil {
					return e
				}
				le.PutUint32(b[i*4:], off)
			}
			if _, e := w.Write(b[:]); e != nil {
				return e
			}
		}
		return nil
	}); err != nil {
		return err
	}
	if err = writeSection(sGeometry, func(w io.Writer) error {
		for _, feat := range features {
			if e := writeGeometry(w, feat.Geometry); e != nil {
				return e
			}
		}
		return nil
	}); err != nil {
		return err
	}
	index := make(map[string][]postingEntry)
	tokenCache := make(map[string][]string)
	weights := make(map[string]uint32)
	for i, feat := range features {
		if i%4096 == 0 {
			if err = checkContext(ctx); err != nil {
				return err
			}
		}
		featureWeights(feat, tokenCache, weights)
		for term, weight := range weights {
			index[term] = append(index[term], postingEntry{uint32(i), weight})
		}
	}
	terms := make([]string, 0, len(index))
	for t := range index {
		terms = append(terms, t)
	}
	sort.Strings(terms)
	if err = writeSection(sTerms, func(w io.Writer) error {
		var po uint64
		var r [termSize]byte
		for _, term := range terms {
			so, e := pool.add(term)
			if e != nil {
				return e
			}
			le.PutUint64(r[:], uint64(so)+4)
			le.PutUint32(r[8:], uint32(len(term)))
			le.PutUint32(r[12:], uint32(len(index[term])))
			le.PutUint64(r[16:], po)
			if _, e := w.Write(r[:]); e != nil {
				return e
			}
			po += uint64(len(index[term])) * 8
			if len(index[term]) > 256 {
				po += 800
			}
		}
		return nil
	}); err != nil {
		return err
	}
	if err = writeSection(sStrings, func(w io.Writer) error {
		_, e := w.Write(pool.data)
		return e
	}); err != nil {
		return err
	}
	pool = stringPool{}
	if err = writeSection(sPostings, func(w io.Writer) error {
		buf := make([]byte, 64*1024)
		for _, term := range terms {
			p := index[term]
			for len(p) > 0 {
				n := min(len(p), len(buf)/8)
				for i, v := range p[:n] {
					le.PutUint32(buf[i*8:], v.id)
					le.PutUint32(buf[i*8+4:], v.weight)
				}
				if _, e := w.Write(buf[:n*8]); e != nil {
					return e
				}
				p = p[n:]
			}
			// Materialize the exact top 100 for common single-token lookups. This
			// avoids scanning millions of country/city postings on every request.
			if entries := index[term]; len(entries) > 256 {
				idf := math.Log1p(float64(len(features)) / float64(len(entries)))
				best := make(matches, 0, 100)
				for _, v := range entries {
					score := float64(v.weight)*idf + float64(float32(features[v.id].Importance))*10
					best.offer(match{id: v.id, score: score}, 100)
				}
				sort.Slice(best, func(i, j int) bool {
					if best[i].score == best[j].score {
						return best[i].id < best[j].id
					}
					return best[i].score > best[j].score
				})
				for i, v := range best {
					j := sort.Search(len(entries), func(i int) bool { return entries[i].id >= v.id })
					le.PutUint32(buf[i*8:], v.id)
					le.PutUint32(buf[i*8+4:], entries[j].weight)
				}
				if _, e := w.Write(buf[:800]); e != nil {
					return e
				}
			}
		}
		return nil
	}); err != nil {
		return err
	}
	index = nil
	tree := buildTree(boxes)
	if err = writeSection(sTree, func(w io.Writer) error {
		var r [treeNodeSize]byte
		for _, n := range tree.nodes {
			putBox(r[:], n.bounds)
			le.PutUint32(r[16:], n.left)
			le.PutUint32(r[20:], n.right)
			le.PutUint32(r[24:], n.start)
			le.PutUint32(r[28:], n.count)
			if _, e := w.Write(r[:]); e != nil {
				return e
			}
		}
		return nil
	}); err != nil {
		return err
	}
	if err = writeSection(sOrder, func(w io.Writer) error {
		var r [4]byte
		for _, id := range tree.order {
			le.PutUint32(r[:], id)
			if _, e := w.Write(r[:]); e != nil {
				return e
			}
		}
		return nil
	}); err != nil {
		return err
	}
	end, err := f.Seek(0, io.SeekCurrent)
	if err != nil {
		return err
	}
	le.PutUint64(h[16:], uint64(end))
	le.PutUint32(h[252:], crc32.Checksum(h[:252], castagnoli))
	if _, err = f.WriteAt(h[:], 0); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = checkContext(ctx); err != nil {
		return err
	}
	if err = os.Link(tmp, path); err != nil {
		return fmt.Errorf("geocode: publish new snapshot (destination must not exist): %w", err)
	}
	return nil
}
func geometrySize(paths [][]Point) uint64 {
	if len(paths) == 0 {
		return 0
	}
	n := uint64(4)
	for _, p := range paths {
		n += 4 + uint64(len(p))*8
	}
	return n
}
func writeGeometry(w io.Writer, paths [][]Point) error {
	if len(paths) == 0 {
		return nil
	}
	var b [8]byte
	le.PutUint32(b[:], uint32(len(paths)))
	if _, e := w.Write(b[:4]); e != nil {
		return e
	}
	for _, p := range paths {
		le.PutUint32(b[:], uint32(len(p)))
		if _, e := w.Write(b[:4]); e != nil {
			return e
		}
		for _, v := range p {
			le.PutUint32(b[:], uint32(quant(v.Lat)))
			le.PutUint32(b[4:], uint32(quant(v.Lon)))
			if _, e := w.Write(b[:]); e != nil {
				return e
			}
		}
	}
	return nil
}
