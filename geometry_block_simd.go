//go:build goexperiment.simd && (amd64 || arm64 || wasm)

package geocode

import "simd"

const useGeometryBlocks = true

func latitudeBlockOutside(b []byte, south, north float64) bool {
	var bytes simd.Uint8s
	width := bytes.Len()
	// These architectures are little-endian, matching the database. Bound the
	// scratch space; a future wider SIMD implementation can use the scalar path.
	if simd.Emulated() || width > 64 {
		return latitudeBlockOutsideScalar(b, south, north)
	}
	lo := simd.LoadUint8s(b).ReshapeToUint32s().BitsToInt32()
	hi := lo
	for i := width; i < 64*8; i += width {
		v := simd.LoadUint8s(b[i:]).ReshapeToUint32s().BitsToInt32()
		lo, hi = lo.Min(v), hi.Max(v)
	}
	var lows, highs [16]int32
	lo.Store(lows[:])
	hi.Store(highs[:])
	finishGeometrySIMD()
	minLat, maxLat := int32(le.Uint32(b[64*8:])), int32(le.Uint32(b[64*8:]))
	// Even lanes contain latitude; odd lanes contain longitude.
	for i := 0; i < lo.Len(); i += 2 {
		minLat, maxLat = min(minLat, lows[i]), max(maxLat, highs[i])
	}
	return float64(maxLat)/coordScale < south || float64(minLat)/coordScale > north
}
