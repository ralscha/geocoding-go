package geocode

import (
	"fmt"
	"hash/crc32"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOpenRejectsInvalidImportance(t *testing.T) {
	path := filepath.Join(t.TempDir(), "valid.db")
	if err := Build(path, testFeatures()); err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, importance := range []float32{float32(math.NaN()), float32(math.Inf(1)), float32(math.Inf(-1)), -0.1, 1.1} {
		t.Run(fmt.Sprint(importance), func(t *testing.T) {
			data := append([]byte(nil), original...)
			off := le.Uint64(data[24+sRecords*16:])
			n := le.Uint64(data[32+sRecords*16:])
			le.PutUint32(data[off+12:], math.Float32bits(importance))
			// Keep both checksums valid to exercise semantic validation.
			le.PutUint32(data[152+sRecords*4:], crc32.Checksum(data[off:off+n], castagnoli))
			le.PutUint32(data[252:], crc32.Checksum(data[:252], castagnoli))
			bad := filepath.Join(t.TempDir(), "invalid.db")
			if err := os.WriteFile(bad, data, 0600); err != nil {
				t.Fatal(err)
			}
			db, err := Open(bad)
			if err == nil {
				db.Close()
				t.Fatal("opened database with invalid importance")
			}
			if !strings.Contains(err.Error(), "corrupt database") {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}
