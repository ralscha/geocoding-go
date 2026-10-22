package osmpbf

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"strconv"
	"testing"

	"github.com/paulmach/osm"
	pb "github.com/ralscha/geocoding-go/internal/pbf/internal/osmpbf"
	"google.golang.org/protobuf/proto"
)

func testFileBlock(t testing.TB, kind string, message proto.Message) []byte {
	t.Helper()
	payload, err := proto.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	blob, err := proto.Marshal(&pb.Blob{Raw: payload})
	if err != nil {
		t.Fatal(err)
	}
	header, err := proto.Marshal(&pb.BlobHeader{Type: new(kind), Datasize: new(int32(len(blob)))})
	if err != nil {
		t.Fatal(err)
	}
	data := binary.BigEndian.AppendUint32(nil, uint32(len(header)))
	data = append(data, header...)
	return append(data, blob...)
}

func testHeader(t testing.TB) []byte {
	return testFileBlock(t, osmHeaderType, &pb.HeaderBlock{RequiredFeatures: []string{"OsmSchema-V0.6"}})
}

func testNodeBlock(t testing.TB, id int64) []byte {
	return testFileBlock(t, osmDataType, &pb.PrimitiveBlock{
		Stringtable: &pb.StringTable{S: []string{""}},
		Primitivegroup: []*pb.PrimitiveGroup{{Nodes: []*pb.Node{
			{Id: new(id), Lat: new(int64(0)), Lon: new(int64(0))},
		}}},
	})
}

type cancelAfterReader struct {
	r      io.Reader
	cancel context.CancelFunc
	limit  int
	read   int
}

func (r *cancelAfterReader) Read(p []byte) (int, error) {
	n, err := r.r.Read(p)
	r.read += n
	if r.read >= r.limit {
		r.cancel()
	}
	return n, err
}

func TestScannerCancellationStopsReading(t *testing.T) {
	header, block := testHeader(t), testNodeBlock(t, 1)
	data := append(header, bytes.Repeat(block, 1000)...)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := &cancelAfterReader{r: bytes.NewReader(data), cancel: cancel, limit: len(header) + len(block)}
	s := New(ctx, r, 4)
	if _, err := s.Header(); err != nil {
		t.Fatal(err)
	}
	<-ctx.Done()
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if r.read > r.limit {
		t.Fatalf("read %d bytes after cancellation at byte %d", r.read, r.limit)
	}
}

func TestScannerAlreadyCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r := bytes.NewReader(testHeader(t))
	s := New(ctx, r, 1)
	defer s.Close()
	if _, err := s.Header(); !errors.Is(err, context.Canceled) {
		t.Fatalf("Header error = %v, want context.Canceled", err)
	}
	if int64(r.Len()) != r.Size() {
		t.Fatal("read input despite canceled context")
	}
}

func TestScannerOrderAndErrors(t *testing.T) {
	for _, workers := range []int{1, 4, 16} {
		for _, truncated := range []bool{false, true} {
			t.Run(strconv.Itoa(workers)+"/truncated="+strconv.FormatBool(truncated), func(t *testing.T) {
				data := testHeader(t)
				for id := int64(1); id <= 20; id++ {
					data = append(data, testNodeBlock(t, id)...)
				}
				if truncated {
					data = append(data, 0, 0)
				}
				s := New(context.Background(), bytes.NewReader(data), workers)
				defer s.Close()
				// Progress is exposed through atomic loads and can be polled while scanning.
				done, stopped := make(chan struct{}), make(chan struct{})
				go func() {
					defer close(stopped)
					for {
						select {
						case <-done:
							return
						default:
							s.FullyScannedBytes()
							s.PreviousFullyScannedBytes()
						}
					}
				}()
				defer func() { close(done); <-stopped }()
				var count int64
				for s.Scan() {
					count++
					if got := int64(s.Object().(*osm.Node).ID); got != count {
						t.Fatalf("node ID = %d, want %d", got, count)
					}
				}
				if count != 20 {
					t.Fatalf("decoded %d nodes, want 20", count)
				}
				if truncated {
					if !errors.Is(s.Err(), io.ErrUnexpectedEOF) {
						t.Fatalf("Err = %v, want io.ErrUnexpectedEOF", s.Err())
					}
				} else if err := s.Err(); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}

func BenchmarkDecodeSkippedBlock(b *testing.B) {
	data, err := proto.Marshal(&pb.PrimitiveBlock{
		Stringtable: &pb.StringTable{S: []string{""}},
		Primitivegroup: []*pb.PrimitiveGroup{{Nodes: []*pb.Node{
			{Id: new(int64(1)), Lat: new(int64(0)), Lon: new(int64(0))},
		}}},
	})
	if err != nil {
		b.Fatal(err)
	}
	decoder := dataDecoder{scanner: &Scanner{SkipNodes: true}}
	blob := &pb.Blob{Raw: data}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		if _, err := decoder.Decode(blob); err != nil {
			b.Fatal(err)
		}
	}
}
