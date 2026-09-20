//go:build amd64

package x86

import "github.com/smartwalle/scankit/internal/simd/generic"

// nativeEqualByteMask 使用 SSE2 生成逐字节比较掩码。
func nativeEqualByteMask(v *generic.Vector, value byte) uint16

// nativeEqualMask 使用 SSE2 生成两个向量的逐字节比较掩码。
func nativeEqualMask(a, b *generic.Vector) uint16

// nativeEqualMaskAVX2 使用 AVX2 生成两个向量的逐字节比较掩码。
func nativeEqualMaskAVX2(a, b *generic.Vector) uint16

// nativeEqualByteMaskAVX2 使用 AVX2 生成逐字节比较掩码。
func nativeEqualByteMaskAVX2(v *generic.Vector, value byte) uint16
