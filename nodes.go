package geocode

import (
	"bufio"
	"container/heap"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"

	"github.com/edsrzf/mmap-go"
)

// The transient node store uses 16 bytes/node on disk, external sorted runs,
// and a sparse fence index. It avoids a Go hash map containing every OSM node.
const nodeRunCapacity = 1 << 20

type nodeEntry struct {
	id       int64
	lat, lon int32
}
type nodeRun struct {
	path        string
	first, last int64
}
type nodeCollector struct {
	dir   string
	buf   []nodeEntry
	runs  []nodeRun
	count uint64
}

func (c *nodeCollector) add(id int64, p Point) error {
	if id <= 0 || !validPoint(p) {
		return fmt.Errorf("geocode: invalid node %d", id)
	}
	if c.buf == nil {
		c.buf = make([]nodeEntry, 0, nodeRunCapacity)
	}
	c.buf = append(c.buf, nodeEntry{id, quant(p.Lat), quant(p.Lon)})
	c.count++
	if len(c.buf) == nodeRunCapacity {
		return c.flush()
	}
	return nil
}
func (c *nodeCollector) flush() error {
	if len(c.buf) == 0 {
		return nil
	}
	if !sort.SliceIsSorted(c.buf, func(i, j int) bool { return c.buf[i].id < c.buf[j].id }) {
		sort.Slice(c.buf, func(i, j int) bool { return c.buf[i].id < c.buf[j].id })
	}
	path := filepath.Join(c.dir, fmt.Sprintf("nodes-%06d.run", len(c.runs)))
	f, e := os.Create(path)
	if e != nil {
		return e
	}
	defer f.Close()
	w := bufio.NewWriterSize(f, 1<<20)
	for i, v := range c.buf {
		if i > 0 && v.id == c.buf[i-1].id {
			return fmt.Errorf("geocode: duplicate node %d; history PBFs are unsupported", v.id)
		}
		if e = writeNode(w, v); e != nil {
			return e
		}
	}
	if e = w.Flush(); e != nil {
		return e
	}
	c.runs = append(c.runs, nodeRun{path, c.buf[0].id, c.buf[len(c.buf)-1].id})
	c.buf = c.buf[:0]
	return nil
}
func writeNode(w *bufio.Writer, v nodeEntry) error {
	if w.Available() < 16 {
		if err := w.Flush(); err != nil {
			return err
		}
	}
	// Encode into the writer's spare capacity instead of allocating a temporary
	// record for every imported node.
	b := w.AvailableBuffer()
	b = le.AppendUint64(b, uint64(v.id))
	b = le.AppendUint32(b, uint32(v.lat))
	b = le.AppendUint32(b, uint32(v.lon))
	_, e := w.Write(b)
	return e
}

type nodeMerge struct {
	entry  nodeEntry
	reader *bufio.Reader
}
type nodeHeap []nodeMerge

func (h nodeHeap) Len() int           { return len(h) }
func (h nodeHeap) Less(i, j int) bool { return h[i].entry.id < h[j].entry.id }
func (h nodeHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *nodeHeap) Push(v any)        { *h = append(*h, v.(nodeMerge)) }
func (h *nodeHeap) Pop() any          { a := *h; v := a[len(a)-1]; *h = a[:len(a)-1]; return v }
func readNode(r *bufio.Reader) (nodeEntry, error) {
	b, err := r.Peek(16)
	if err != nil {
		if len(b) > 0 {
			_, _ = r.Discard(len(b))
			if err == io.EOF {
				err = io.ErrUnexpectedEOF
			}
		}
		return nodeEntry{}, err
	}
	v := nodeEntry{int64(le.Uint64(b)), int32(le.Uint32(b[8:])), int32(le.Uint32(b[12:]))}
	_, err = r.Discard(16)
	return v, err
}
func (c *nodeCollector) finish() (*nodeIndex, error) {
	if e := c.flush(); e != nil {
		return nil, e
	}
	c.buf = nil
	path := filepath.Join(c.dir, "nodes.bin")
	f, e := os.Create(path)
	if e != nil {
		return nil, e
	}
	defer f.Close()
	w := bufio.NewWriterSize(f, 1<<20)
	ordered := true
	for i := 1; i < len(c.runs); i++ {
		if c.runs[i].first <= c.runs[i-1].last {
			ordered = false
		}
	}
	if ordered {
		for _, run := range c.runs {
			r, err := os.Open(run.path)
			if err != nil {
				return nil, err
			}
			_, err = io.Copy(w, r)
			r.Close()
			if err != nil {
				return nil, err
			}
		}
	} else {
		var files []*os.File
		defer func() {
			for _, f := range files {
				f.Close()
			}
		}()
		var h nodeHeap
		for _, run := range c.runs {
			r, err := os.Open(run.path)
			if err != nil {
				return nil, err
			}
			files = append(files, r)
			br := bufio.NewReaderSize(r, 64<<10)
			v, err := readNode(br)
			if err != nil {
				return nil, err
			}
			h = append(h, nodeMerge{v, br})
		}
		heap.Init(&h)
		var previous int64
		for len(h) > 0 {
			v := h[0].entry
			if v.id == previous {
				return nil, fmt.Errorf("geocode: duplicate node %d", v.id)
			}
			previous = v.id
			if e = writeNode(w, v); e != nil {
				return nil, e
			}
			v, e = readNode(h[0].reader)
			if e == io.EOF {
				heap.Pop(&h)
			} else if e != nil {
				return nil, e
			} else {
				h[0].entry = v
				heap.Fix(&h, 0)
			}
		}
		for _, source := range files {
			if err := source.Close(); err != nil {
				return nil, err
			}
		}
	}
	if e = w.Flush(); e != nil {
		return nil, e
	}
	if e = f.Close(); e != nil {
		return nil, e
	}
	for _, run := range c.runs {
		if e = os.Remove(run.path); e != nil {
			return nil, e
		}
	}
	if c.count == 0 {
		return &nodeIndex{}, nil
	}
	r, e := os.Open(path)
	if e != nil {
		return nil, e
	}
	defer r.Close()
	data, e := mmap.Map(r, mmap.RDONLY, 0)
	if e != nil {
		return nil, e
	}
	idx := &nodeIndex{data: data}
	for i := 0; i < len(data)/16; i += 256 {
		idx.fences = append(idx.fences, int64(le.Uint64(data[i*16:])))
	}
	return idx, nil
}

type nodeIndex struct {
	data   mmap.MMap
	fences []int64
}

func (n *nodeIndex) close() {
	if n.data != nil {
		n.data.Unmap()
		n.data = nil
	}
}
func (n *nodeIndex) get(id int64) (Point, bool) {
	if len(n.fences) == 0 {
		return Point{}, false
	}
	page := sort.Search(len(n.fences), func(i int) bool { return n.fences[i] > id }) - 1
	if page < 0 {
		return Point{}, false
	}
	lo, hi := page*256, min((page+1)*256, len(n.data)/16)
	i := lo + sort.Search(hi-lo, func(i int) bool { return int64(le.Uint64(n.data[(lo+i)*16:])) >= id })
	if i == hi || int64(le.Uint64(n.data[i*16:])) != id {
		return Point{}, false
	}
	r := n.data[i*16:]
	return Point{float64(int32(le.Uint32(r[8:]))) / coordScale, float64(int32(le.Uint32(r[12:]))) / coordScale}, true
}
