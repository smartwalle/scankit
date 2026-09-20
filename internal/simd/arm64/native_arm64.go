//go:build arm64

package arm64

import "github.com/smartwalle/scankit/internal/simd/generic"

// nativeEqualByteMask 用 NEON 生成逐字节等于指定值的 16 位位置掩码。
//
//go:noescape
func nativeEqualByteMask(v *generic.Vector, value byte) uint16

// nativeEqualByteMaskFold 生成逐字节等于 lower 或 upper 的 16 位位置掩码。
//
//go:noescape
func nativeEqualByteMaskFold(v *generic.Vector, lower, upper byte) uint16

// nativeEqualMask 生成两向量逐字节相等的 16 位位置掩码。
//
//go:noescape
func nativeEqualMask(a, b *generic.Vector) uint16

// nativeInRangeMask 按无符号比较返回每个字节是否落在 [lo, hi] 闭区间内的位置掩码。
//
//go:noescape
func nativeInRangeMask(v *generic.Vector, lo, hi byte) uint16

// nativeByteSetMask 使用 NEON 半字节查表判定窗口内每个字节是否属于预编译集合。
//
//go:noescape
func nativeByteSetMask(v *generic.Vector, tables *[32]byte) uint16

// nativeByteSetMask32 使用 NEON 半字节查表判定 32 字节窗口内每个字节是否属于预编译集合。
//
//go:noescape
func nativeByteSetMask32(window []byte, tables *[32]byte) uint32
