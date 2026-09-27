//go:build !arm64

package arm64

import "github.com/smartwalle/scankit/internal/simd/generic"

// nativeEqualByteMask 在非 ARM64 平台保留通用实现，保证语义一致。
func nativeEqualByteMask(v *generic.Vector, value byte) uint16 {
	return generic.EqualByteMask(*v, value)
}

// nativeEqualByteMaskFold 在非 ARM64 平台保留通用实现，保证语义一致。
func nativeEqualByteMaskFold(v *generic.Vector, lower, upper byte) uint16 {
	return generic.EqualByteMask(*v, lower) | generic.EqualByteMask(*v, upper)
}

// nativeEqualMask 在非 ARM64 平台保留通用实现，保证语义一致。
func nativeEqualMask(a, b *generic.Vector) uint16 {
	return generic.EqualMask(*a, *b)
}

// nativeInRangeMask 在非 ARM64 平台保留通用实现，保证语义一致。
func nativeInRangeMask(v *generic.Vector, lo, hi byte) uint16 {
	return generic.InRangeMask(*v, lo, hi)
}

// nativeByteSetMask 在非 ARM64 平台沿用同一张半字节查找表，保证语义一致。
func nativeByteSetMask(v *generic.Vector, tables *[32]byte) uint16 {
	var mask uint16
	for i, value := range v {
		high := value >> 4
		low := value & 0x0F
		var row byte
		if low < 8 {
			row = tables[high]
		} else {
			row = tables[16+high]
		}
		if row&(1<<(low&7)) != 0 {
			mask |= 1 << uint(i)
		}
	}
	return mask
}
