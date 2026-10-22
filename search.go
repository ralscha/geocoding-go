package geocode

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"math"
	"slices"
	"sort"
	"strings"
	"unicode/utf8"
)

func splitTerms(s string) []string { return strings.Fields(Normalize(s)) }

type match struct {
	id              uint32
	score, distance float64
}

// The root is the worst retained match. Ties use feature IDs for deterministic
// pagination-free results, regardless of spatial traversal order.
type matches []match

func (h *matches) offer(m match, k int) {
	a := *h
	if len(*h) < k {
		a = append(a, m)
		i := len(a) - 1
		for i > 0 {
			parent := (i - 1) / 2
			if !worseMatch(m, a[parent]) {
				break
			}
			a[i] = a[parent]
			i = parent
		}
		a[i] = m
		*h = a
	} else if m.score > (*h)[0].score || (m.score == (*h)[0].score && m.id < (*h)[0].id) {
		i := 0
		for {
			child := i*2 + 1
			if child >= len(a) {
				break
			}
			if child+1 < len(a) && worseMatch(a[child+1], a[child]) {
				child++
			}
			if !worseMatch(a[child], m) {
				break
			}
			a[i] = a[child]
			i = child
		}
		a[i] = m
	}
}
func worseMatch(a, b match) bool { return a.score < b.score || (a.score == b.score && a.id > b.id) }
func compareMatches(a, b match) int {
	if worseMatch(a, b) {
		return 1
	}
	if worseMatch(b, a) {
		return -1
	}
	return 0
}

type postingCursor struct {
	data []byte
	pos  int
	idf  float64
}

func (p *postingCursor) seek(id uint32) (uint32, bool) {
	n := len(p.data) / 8
	pos := p.pos
	if pos >= n {
		return 0, false
	}
	current := le.Uint32(p.data[pos*8:])
	if current < id {
		lo, step := pos+1, 1
		hi := min(n, lo+step)
		for hi < n && le.Uint32(p.data[(hi-1)*8:]) < id {
			lo = hi
			step *= 2
			hi = min(n, lo+step)
		}
		for lo < hi {
			mid := int(uint(lo+hi) >> 1)
			if le.Uint32(p.data[mid*8:]) < id {
				lo = mid + 1
			} else {
				hi = mid
			}
		}
		pos = lo
		p.pos = pos
		if pos >= n {
			return 0, false
		}
		current = le.Uint32(p.data[pos*8:])
	}
	if current == id {
		return le.Uint32(p.data[pos*8+4:]), true
	}
	return 0, false
}

// Search intersects sorted posting lists, starting with the rarest token. It
// scans all matching candidates and retains only the best Limit scores.
func (db *DB) Search(ctx context.Context, q string, opts SearchOptions) ([]Result, error) {
	db.mu.RLock()
	defer db.mu.RUnlock()
	if db.closed {
		return nil, ErrClosed
	}
	if err := checkContext(ctx); err != nil {
		return nil, err
	}
	if len(q) > 1024 {
		return nil, errors.New("geocode: query exceeds 1024 bytes")
	}
	if opts.Near != nil && !validPoint(*opts.Near) {
		return nil, ErrInvalidCoordinate
	}
	if b := opts.Bounds; b != nil {
		if !validPoint(Point{b[0], b[1]}) || !validPoint(Point{b[2], b[3]}) || b[0] > b[2] {
			return nil, errors.New("geocode: invalid bounds")
		}
	}
	terms := splitTerms(q)
	if len(terms) == 0 {
		return []Result{}, nil
	}
	if len(terms) > 32 {
		return nil, errors.New("geocode: query exceeds 32 tokens")
	}
	lists := make([]postingCursor, 0, len(terms))
	seen := make(map[string]bool, len(terms))
	nt := len(db.sections[sTerms]) / termSize
	k := queryLimit(opts.Limit)
	for ti, t := range terms {
		if seen[t] {
			continue
		}
		seen[t] = true
		idx := sort.Search(nt, func(i int) bool { return bytes.Compare(db.termBytes(i), []byte(t)) >= 0 })
		var p []byte
		var df int
		singleTerm := -1
		if opts.Prefix && ti == len(terms)-1 && utf8.RuneCountInString(t) >= 2 {
			var parts [][]byte
			for i := idx; i < nt && bytes.HasPrefix(db.termBytes(i), []byte(t)); i++ {
				if len(parts) == 256 {
					return nil, ErrQueryTooBroad
				}
				parts = append(parts, db.posting(i))
			}
			var filter []byte
			if len(parts) > 1 {
				for _, list := range lists {
					if filter == nil || len(list.data) < len(filter) {
						filter = list.data
					}
				}
			}
			var err error
			p, df, err = unionPostings(ctx, parts, filter)
			if err != nil {
				return nil, err
			}
			if len(parts) == 1 {
				singleTerm = idx
			}
		} else if idx < nt && bytes.Equal(db.termBytes(idx), []byte(t)) {
			p = db.posting(idx)
			df = len(p) / 8
			singleTerm = idx
		}
		if len(p) == 0 {
			return []Result{}, nil
		}
		// A prefix that expands to one dictionary term has the same ranking
		// as an exact lookup and can reuse its precomputed best matches.
		if singleTerm >= 0 && len(terms) == 1 && opts.Near == nil && opts.Bounds == nil {
			if top := db.topPosting(singleTerm); len(top) > 0 {
				idf := math.Log1p(float64(db.count) / float64(df))
				best := make(matches, 0, k)
				for j := 0; j < k; j++ {
					id := le.Uint32(top[j*8:])
					if id >= db.count {
						return nil, corrupt("top posting feature")
					}
					score := float64(le.Uint32(top[j*8+4:]))*idf + float64(math.Float32frombits(le.Uint32(db.record(id)[12:])))*10
					best = append(best, match{id: id, score: score})
				}
				return db.results(best, false)
			}
		}
		lists = append(lists, postingCursor{data: p, idf: math.Log1p(float64(db.count) / float64(df))})
	}
	// Keep the original frequency order even when a prefix buffer was filtered;
	// this preserves the order of floating-point additions in result scores.
	slices.SortFunc(lists, func(a, b postingCursor) int { return cmp.Compare(b.idf, a.idf) })
	best := make(matches, 0, k)
	base := lists[0]
	steps := 0
candidates:
	for base.pos < len(base.data)/8 {
		if steps%1024 == 0 {
			if err := checkContext(ctx); err != nil {
				return nil, err
			}
		}
		steps++
		off := base.pos * 8
		base.pos++
		id := le.Uint32(base.data[off:])
		if id >= db.count {
			return nil, corrupt("posting feature")
		}
		score := float64(le.Uint32(base.data[off+4:])) * base.idf
		ok := true
		for j := 1; j < len(lists); j++ {
			w, found := lists[j].seek(id)
			if !found {
				if lists[j].pos >= len(lists[j].data)/8 {
					break candidates
				}
				// Leap to the next possible intersection instead of walking every
				// document in the shortest list when address tokens rarely overlap.
				base.seek(le.Uint32(lists[j].data[lists[j].pos*8:]))
				ok = false
				break
			}
			score += float64(w) * lists[j].idf
		}
		if !ok {
			continue
		}
		r := db.record(id)
		p := recordPoint(r)
		if b := opts.Bounds; b != nil {
			lonOK := p.Lon >= b[1] && p.Lon <= b[3]
			if b[1] > b[3] {
				lonOK = p.Lon >= b[1] || p.Lon <= b[3]
			}
			if p.Lat < b[0] || p.Lat > b[2] || !lonOK {
				continue
			}
		}
		score += float64(math.Float32frombits(le.Uint32(r[12:]))) * 10
		if opts.Near != nil {
			score += 10 / (1 + haversine(p, *opts.Near)/1000)
		}
		best.offer(match{id: id, score: score}, k)
	}
	return db.results(best, false)
}

type unionPart struct {
	data []byte
	pos  int
	id   uint32
}
type unionHeap []unionPart

func (h unionHeap) down(i int) {
	p := h[i]
	for {
		child := i*2 + 1
		if child >= len(h) {
			break
		}
		if child+1 < len(h) && h[child+1].id < h[child].id {
			child++
		}
		if p.id <= h[child].id {
			break
		}
		h[i] = h[child]
		i = child
	}
	h[i] = p
}

// unionPostings counts all distinct feature IDs for exact IDF scoring. When a
// filter is supplied, it only materializes IDs present in that exact-token list.
func unionPostings(ctx context.Context, parts [][]byte, filter []byte) ([]byte, int, error) {
	if len(parts) == 0 {
		return nil, 0, nil
	}
	if len(parts) == 1 && filter == nil {
		return parts[0], len(parts[0]) / 8, nil
	}
	h := make(unionHeap, 0, len(parts))
	capacity := 0
	for _, p := range parts {
		if len(p) > 0 {
			h = append(h, unionPart{data: p, id: le.Uint32(p)})
			capacity = max(capacity, len(p))
		}
	}
	for i := len(h)/2 - 1; i >= 0; i-- {
		h.down(i)
	}
	if filter != nil {
		capacity = min(capacity, len(filter))
	}
	out := make([]byte, 0, capacity)
	cursor := postingCursor{data: filter}
	var previous uint32
	count := 0
	emit := false
	steps := 0
	for len(h) > 0 {
		if steps%4096 == 0 {
			if err := checkContext(ctx); err != nil {
				return nil, 0, err
			}
		}
		steps++
		p := &h[0]
		id, w := p.id, le.Uint32(p.data[p.pos+4:])
		if count == 0 || id != previous {
			count++
			previous = id
			emit = true
			if filter != nil {
				_, emit = cursor.seek(id)
			}
			if emit {
				if len(out) == cap(out) {
					// Large unions otherwise grow in small increments, repeatedly
					// copying their accumulated postings. Doubling bounds that work.
					capacity := max(8, 2*cap(out))
					if filter != nil {
						capacity = min(capacity, len(filter))
					}
					grown := make([]byte, len(out), capacity)
					copy(grown, out)
					out = grown
				}
				out = le.AppendUint32(out, id)
				out = le.AppendUint32(out, w)
			}
		} else if emit {
			le.PutUint32(out[len(out)-4:], max(w, le.Uint32(out[len(out)-4:])))
		}
		p.pos += 8
		if p.pos == len(p.data) {
			h[0] = h[len(h)-1]
			h = h[:len(h)-1]
		} else {
			p.id = le.Uint32(p.data[p.pos:])
		}
		if len(h) > 0 {
			h.down(0)
		}
	}
	return out, count, nil
}
func (db *DB) results(best matches, reverse bool) ([]Result, error) {
	slices.SortFunc(best, compareMatches)
	out := make([]Result, 0, len(best))
	for _, m := range best {
		f, err := db.metadata(m.id)
		if err != nil {
			return nil, err
		}
		r := featureResult(f)
		if reverse {
			r.Distance = m.distance
		} else {
			r.Score = m.score
		}
		out = append(out, r)
	}
	return out, nil
}
