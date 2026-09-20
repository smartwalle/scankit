//go:build !amd64

package x86

import "github.com/smartwalle/scankit/internal/simd/generic"

// nativeByteSetMask 在非 x86 平台沿用同一张半字节查找表，保证语义一致。
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

// nativeInRangeMask 在非 x86 平台保留通用实现，保证区间语义一致。
func nativeInRangeMask(v *generic.Vector, lo, hi byte) uint16 {
	return generic.InRangeMask(*v, lo, hi)
}
