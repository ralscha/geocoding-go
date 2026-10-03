package osmpbf

import (
	"bytes"
	"compress/zlib"
	"testing"

	pb "github.com/ralscha/geocoding-go/internal/pbf/internal/osmpbf"
	"google.golang.org/protobuf/proto"
)

func TestRejectInvalidBlobSizes(t *testing.T) {
	for _, size := range []int32{-1, 0, maxBlobSize} {
		encoded, e := proto.Marshal(&pb.BlobHeader{Type: new("OSMData"), Datasize: new(size)})
		if e != nil {
			t.Fatal(e)
		}
		d := decoder{r: bytes.NewReader(encoded)}
		if _, e = d.readBlobHeader(make([]byte, len(encoded))); e == nil {
			t.Fatal("accepted invalid blob size", size)
		}
	}
}
func TestBoundedDecompression(t *testing.T) {
	var b bytes.Buffer
	z := zlib.NewWriter(&b)
	_, _ = z.Write(make([]byte, 1024*1024))
	_ = z.Close()
	for _, size := range []int32{-1, 8, maxBlobSize + 1} {
		if _, e := getData(&pb.Blob{RawSize: new(size), ZlibData: b.Bytes()}, nil); e == nil {
			t.Fatal("accepted invalid raw size", size)
		}
	}
}
