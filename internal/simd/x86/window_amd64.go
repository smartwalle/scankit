//go:build amd64

package x86

import "github.com/smartwalle/scankit/internal/simd"

// WindowMask 使用半字节查表在 32 字节窗口内生成候选起点掩码。
//
// 每个 lane 只需一次原生调用即可覆盖整个窗口：AVX2 能力下由 256 位寄存器一次判定，
// 否则用 SSSE3 分两个 128 位半区判定后拼接。lane 对齐与窗口末端退让规则由
// simd.ComposeWindowMask 定义，与通用标量参考实现逐位一致；能力不足时回退到同一参考实现。
func (b Backend) WindowMask(data []byte, off int, tables *simd.ByteSetTables, lanes int) (uint32, bool) {
	if tables == nil || off < 0 || off > len(data) || len(data)-off < simd.SuperWidth {
		return 0, false
	}
	if b.resolved < TierSSE4 {
		return simd.WindowMaskScalar(data, off, tables, lanes)
	}
	lanes = simd.ClampLanes(lanes)
	window := data[off : off+simd.SuperWidth]
	var laneMasks [4]uint32
	if b.resolved >= TierAVX2 {
		for lane := 0; lane < lanes; lane++ {
			laneMasks[lane] = nativeByteSetMask32AVX2(window, &tables[lane])
		}
		return simd.ComposeWindowMask(laneMasks, lanes), true
	}
	for lane := 0; lane < lanes; lane++ {
		laneMasks[lane] = nativeByteSetMask32(window, &tables[lane])
	}
	return simd.ComposeWindowMask(laneMasks, lanes), true
}
