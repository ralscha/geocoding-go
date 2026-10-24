package geocode

// latitudeBlockOutsideScalar accepts 65 packed points (64 segments). Keep the
// final comparison in degrees, as in distanceWithin, to preserve rounding at
// the latitude window's endpoints.
func latitudeBlockOutsideScalar(b []byte, south, north float64) bool {
	lo, hi := int32(le.Uint32(b)), int32(le.Uint32(b))
	for i := 8; i < 65*8; i += 8 {
		lat := int32(le.Uint32(b[i:]))
		lo, hi = min(lo, lat), max(hi, lat)
	}
	return float64(hi)/coordScale < south || float64(lo)/coordScale > north
}
