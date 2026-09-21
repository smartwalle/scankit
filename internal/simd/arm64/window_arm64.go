//go:build arm64

package arm64

import "github.com/smartwalle/scankit/internal/simd"

// WindowMask 使用 NEON 半字节查表在 32 字节窗口内生成候选起点掩码。
//
// 每个 lane 只需一次原生调用即可覆盖整个窗口，并在同一次调用内把两个 128 位半区的
// 位置掩码拼成 32 位结果。lane 对齐与窗口末端退让规则由 simd.ComposeWindowMask 定义，
// 与通用标量参考实现逐位一致；能力不足时回退到同一参考实现。
func (b Backend) WindowMask(data []byte, off int, tables *simd.ByteSetTables, lanes int) (uint32, bool) {
	if tables == nil || off < 0 || off > len(data) || len(data)-off < simd.SuperWidth {
		return 0, false
	}
	if b.resolved < TierNEON {
		return simd.WindowMaskScalar(data, off, tables, lanes)
	}
	lanes = simd.ClampLanes(lanes)
	window := data[off : off+simd.SuperWidth]
	var laneMasks [4]uint32
	for lane := 0; lane < lanes; lane++ {
		laneMasks[lane] = nativeByteSetMask32(window, &tables[lane])
	}
	return simd.ComposeWindowMask(laneMasks, lanes), true
}

// WindowMask64 使用 NEON 半字节查表在 64 字节宽窗口内生成候选起点掩码。
//
// NEON 寄存器宽度固定为 128 位，因此宽窗口由两次已验证的 32 字节内核调用拼接而成：
// 低、高两个 32 字节半区各自产出 32 位位置掩码后合并为 64 位，再由
// simd.ComposeWindowMask64 完成 lane 对齐与窗口末端退让，语义与 WindowMask 完全一致。
func (b Backend) WindowMask64(data []byte, off int, tables *simd.ByteSetTables, lanes int) (uint64, bool) {
	if tables == nil || off < 0 || off > len(data) || len(data)-off < simd.WideWidth {
		return 0, false
	}
	if b.resolved < TierNEON {
		return simd.WindowMask64Scalar(data, off, tables, lanes)
	}
	lanes = simd.ClampLanes(lanes)
	window := data[off : off+simd.WideWidth]
	first := uint64(nativeByteSetMask32(window[:simd.SuperWidth], &tables[0])) |
		uint64(nativeByteSetMask32(window[simd.SuperWidth:], &tables[0]))<<32
	// 组合结果必然包含首 lane 掩码的按位与，因此首 lane 为空时窗口内
	// 不可能有候选，直接省去其余 lane 的原生调用。稀疏语料下大多数窗口
	// 都走该分支，是候选扫描的主要开销削减点。
	if first == 0 {
		return 0, true
	}
	if lanes == 1 {
		return first, true
	}
	strict := first
	laneMasks := [4]uint64{first}
	for lane := 1; lane < lanes; lane++ {
		lo := nativeByteSetMask32(window[:simd.SuperWidth], &tables[lane])
		hi := nativeByteSetMask32(window[simd.SuperWidth:], &tables[lane])
		mask := uint64(lo) | uint64(hi)<<32
		laneMasks[lane] = mask
		strict &= mask >> uint(lane)
		if strict == 0 {
			return simd.ComposeWindowMask64Partial(first, lanes), true
		}
	}
	return simd.ComposeWindowMask64(laneMasks, lanes), true
}
