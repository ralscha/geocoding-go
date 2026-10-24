//go:build !goexperiment.simd || !(amd64 || arm64 || wasm)

package geocode

const useGeometryBlocks = false

func latitudeBlockOutside(b []byte, south, north float64) bool {
	return latitudeBlockOutsideScalar(b, south, north)
}
