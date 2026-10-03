# PBF decoder provenance

`scanner.go`, `decode.go`, `decode_data.go`, and `internal/osmpbf/` derive from
[`github.com/paulmach/osm` v0.9.0](https://github.com/paulmach/osm/tree/v0.9.0/osmpbf).
The MIT license is preserved in `LICENSE.md`; the generated protocol files also
retain their original notices.

This local copy adds ordinary (non-dense) node decoding, bounded decompression,
and conversion of malformed-data panics into decoder errors. It always uses Go's
standard zlib implementation, without a native dependency. Keeping the patch
local avoids changing files in the Go module cache. The root PBF integration
tests exercise dense and ordinary nodes, raw/zlib blocks, unordered objects,
relation assembly, and truncated input.
