package geocode

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"testing"
)

func TestNodeBufferedRoundTrip(t *testing.T) {
	var data bytes.Buffer
	w := bufio.NewWriterSize(&data, 17)
	for i := int64(1); i <= 50; i++ {
		if err := writeNode(w, nodeEntry{i, int32(-i), int32(i * 2)}); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	r := bufio.NewReaderSize(&data, 17)
	for i := int64(1); i <= 50; i++ {
		got, err := readNode(r)
		want := nodeEntry{i, int32(-i), int32(i * 2)}
		if err != nil || got != want {
			t.Fatalf("node %d: got %+v, %v; want %+v", i, got, err, want)
		}
	}
	if _, err := readNode(r); err != io.EOF {
		t.Fatalf("end of stream: %v", err)
	}
}

func TestNodeTruncatedRecord(t *testing.T) {
	for length := 1; length < 16; length++ {
		r := bufio.NewReader(bytes.NewReader(make([]byte, length)))
		if _, err := readNode(r); !errors.Is(err, io.ErrUnexpectedEOF) {
			t.Fatalf("%d-byte record: %v", length, err)
		}
		if _, err := readNode(r); err != io.EOF {
			t.Fatalf("truncated record was not consumed: %v", err)
		}
	}
}

type nodeErrorWriter struct{}

func (nodeErrorWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestNodeWritePropagatesFlushError(t *testing.T) {
	w := bufio.NewWriterSize(nodeErrorWriter{}, 16)
	if err := writeNode(w, nodeEntry{1, 2, 3}); err != nil {
		t.Fatal(err)
	}
	if err := writeNode(w, nodeEntry{4, 5, 6}); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("flush error: %v", err)
	}
}

func BenchmarkNodeWrite(b *testing.B) {
	w := bufio.NewWriterSize(io.Discard, 64<<10)
	b.ReportAllocs()
	b.SetBytes(16)
	b.ResetTimer()
	for b.Loop() {
		if err := writeNode(w, nodeEntry{123, -456, 789}); err != nil {
			b.Fatal(err)
		}
	}
	if err := w.Flush(); err != nil {
		b.Fatal(err)
	}
}

func BenchmarkNodeRead(b *testing.B) {
	const count = 4096
	data := make([]byte, count*16)
	for i := range count {
		le.PutUint64(data[i*16:], uint64(i+1))
	}
	source := bytes.NewReader(data)
	r := bufio.NewReaderSize(source, 64<<10)
	i := 0
	b.ReportAllocs()
	b.SetBytes(16)
	b.ResetTimer()
	for b.Loop() {
		if i == count {
			source.Reset(data)
			r.Reset(source)
			i = 0
		}
		if _, err := readNode(r); err != nil {
			b.Fatal(err)
		}
		i++
	}
}
