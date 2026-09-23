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

// nativeByteSetMask64 使用 NEON 半字节查表判定 64 字节窗口内每个字节是否属于预编译集合。
// 与 32 字节版本逐位一致，但在一次调用内处理四个 128 位半区，省掉宽窗口扫描里第二次
// 调用的查找表装载与常数量化。
//
// 首参用 *byte 而不是 []byte：函数只读窗口内容、不需要长度与容量，而切片入参在
// ABI0 下要额外搬运长度与容量两个字，热路径上每次调用都要多两条存储。
//
//go:noescape
func nativeByteSetMask64(window *byte, tables *[32]byte) uint64
