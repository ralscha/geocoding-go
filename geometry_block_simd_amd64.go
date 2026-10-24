//go:build goexperiment.simd

package geocode

import "simd/archsimd"

func finishGeometrySIMD() {
	// Go 1.27 does not insert this transition automatically. Clear the upper
	// lanes before returning to the scalar floating-point distance code.
	if archsimd.X86.AVX() {
		archsimd.ClearAVXUpperBits()
	}
}
