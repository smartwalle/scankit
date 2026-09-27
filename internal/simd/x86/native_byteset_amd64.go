//go:build amd64

package x86

import "github.com/smartwalle/scankit/internal/simd/generic"

// nativeByteSetMask 使用 PSHUFB 半字节查表判定窗口内每个字节是否属于预编译集合。
//
//go:noescape
func nativeByteSetMask(v *generic.Vector, tables *[32]byte) uint16

// nativeInRangeMask 使用饱和减法判定窗口内每个字节是否落在 [lo, hi] 闭区间内。
//
//go:noescape
func nativeInRangeMask(v *generic.Vector, lo, hi byte) uint16

// nativeByteSetMask32 使用 PSHUFB 半字节查表判定 32 字节窗口内每个字节是否属于预编译集合。
//
//go:noescape
func nativeByteSetMask32(window []byte, tables *[32]byte) uint32

// nativeByteSetMask32AVX2 使用 AVX2 一次判定 32 字节窗口内每个字节是否属于预编译集合。
//
//go:noescape
func nativeByteSetMask32AVX2(window []byte, tables *[32]byte) uint32

// nativeByteSetMask64AVX512 使用 AVX512BW 一次判定 64 字节宽窗口内的字节集合命中。
//
//go:noescape
func nativeByteSetMask64AVX512(window []byte, tables *[32]byte) uint64

// nativeByteSetMask64VBMI 使用 AVX512VBMI 的 VPERMB 一次判定 64 字节宽窗口内的字节集合命中。
//
//go:noescape
func nativeByteSetMask64VBMI(window []byte, tables *[32]byte) uint64
