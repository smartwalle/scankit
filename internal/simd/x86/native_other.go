//go:build !amd64

package x86

import "github.com/smartwalle/scankit/internal/simd/generic"

func nativeEqualByteMask(v *generic.Vector, value byte) uint16 {
	return generic.EqualByteMask(*v, value)
}

func nativeEqualMask(a, b *generic.Vector) uint16 {
	return generic.EqualMask(*a, *b)
}

func nativeEqualMaskAVX2(a, b *generic.Vector) uint16 {
	return generic.EqualMask(*a, *b)
}

func nativeEqualByteMaskAVX2(v *generic.Vector, value byte) uint16 {
	return generic.EqualByteMask(*v, value)
}
