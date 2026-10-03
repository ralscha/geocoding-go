package main

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
)

// A single ordinary OSM node, encoded independently of the production decoder.
func fixturePBF() []byte {
	var fieldVar = func(field int, value uint64) []byte {
		b := binary.AppendUvarint(nil, uint64(field<<3))
		return binary.AppendUvarint(b, value)
	}
	var fieldBytes = func(field int, value []byte) []byte {
		b := binary.AppendUvarint(nil, uint64(field<<3|2))
		b = binary.AppendUvarint(b, uint64(len(value)))
		return append(b, value...)
	}
	join := func(parts ...[]byte) []byte { return bytes.Join(parts, nil) }
	var out bytes.Buffer
	block := func(kind string, payload []byte, compressed bool) {
		blob := fieldBytes(1, payload)
		if compressed {
			var zipped bytes.Buffer
			z := zlib.NewWriter(&zipped)
			_, _ = z.Write(payload) // bytes.Buffer writes cannot fail.
			_ = z.Close()
			blob = join(fieldVar(2, uint64(len(payload))), fieldBytes(3, zipped.Bytes()))
		}
		header := join(fieldBytes(1, []byte(kind)), fieldVar(3, uint64(len(blob))))
		out.Write(binary.BigEndian.AppendUint32(nil, uint32(len(header))))
		out.Write(header)
		out.Write(blob)
	}
	block("OSMHeader", fieldBytes(4, []byte("OsmSchema-V0.6")), false)
	var table []byte
	for _, s := range []string{"", "name", "Release Library", "amenity", "library", "addr:housenumber", "10", "addr:street", "Teststrasse", "addr:city", "Teststadt"} {
		table = append(table, fieldBytes(1, []byte(s))...)
	}
	node := join(fieldVar(1, 42<<1), fieldBytes(2, []byte{1, 3, 5, 7, 9}), fieldBytes(3, []byte{2, 4, 6, 8, 10}),
		fieldVar(8, 473769000<<1), fieldVar(9, 85417000<<1))
	block("OSMData", join(fieldBytes(1, table), fieldBytes(2, fieldBytes(1, node))), true)
	return out.Bytes()
}
