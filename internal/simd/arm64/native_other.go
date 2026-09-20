//go:build !arm64

package arm64

import "github.com/smartwalle/scankit/internal/simd/generic"

func nativeEqualByteVector(v *generic.Vector, value byte, out *generic.Vector) {
	for i, item := range v {
		if item == value {
			out[i] = 0xff
		}
	}
}

func nativeEqualVector(a, b *generic.Vector, out *generic.Vector) {
	for i := range a {
		if a[i] == b[i] {
			out[i] = 0xff
		}
	}
}
