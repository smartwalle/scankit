//go:build amd64

package x86

import (
	"sync"

	"github.com/smartwalle/scankit/internal/simd"
)

// wideKernelCheck 保存 512 位宽窗口原生内核的一次性自检结果。
//
// 512 位内核依赖 AVX512BW/VBMI 指令，开发与评测环境不一定具备执行能力，因此首次
// 使用时先做穷举字节覆盖的差分自检：把全部 256 个字节取值铺在四个 64 字节窗口上，
// 与标量参考实现逐位比对。自检失败时永久退回经验证的 256 位拼接路径，避免在未知
// 硬件上产生错误的候选集合。
var wideKernelCheck struct {
	once sync.Once
	bw   bool
	vbmi bool
}

// wideKernelsReady 返回原生宽窗口内核的自检结果，并按可用能力优先选择 VBMI。
func wideKernelsReady(tier Tier) (useVBMI, useBW bool) {
	wideKernelCheck.once.Do(func() {
		wideKernelCheck.bw = verifyWideKernel(nativeByteSetMask64AVX512)
		if detectedTier() >= TierAVX512VBMI {
			wideKernelCheck.vbmi = verifyWideKernel(nativeByteSetMask64VBMI)
		}
	})
	if tier >= TierAVX512VBMI && wideKernelCheck.vbmi {
		return true, wideKernelCheck.bw
	}
	return false, wideKernelCheck.bw
}

// verifyWideKernel 用全部 256 个字节取值与三档集合校验一个原生宽窗口内核。
//
// 集合覆盖全空、全满与每个高半字节行都有命中的稀疏集合，窗口覆盖 0..255 的全部
// 字节取值以及 64 个窗口位置，能暴露表下标、128 位半区复制和位序上的实现错误。
func verifyWideKernel(kernel func([]byte, *[32]byte) uint64) bool {
	var sets [3][4][4]uint64
	sets[1] = [4][4]uint64{{^uint64(0), ^uint64(0), ^uint64(0), ^uint64(0)}}
	for h := range 16 {
		for _, low := range []uint{0, 3, 7, 8, 12, 15} {
			v := uint(h<<4) | low
			sets[2][0][v/64] |= 1 << uint(v%64)
		}
	}
	windows := make([][]byte, 4)
	for w := range windows {
		windows[w] = make([]byte, simd.WideWidth)
		for i := range windows[w] {
			windows[w][i] = byte(w*simd.WideWidth + i)
		}
	}
	for _, window := range windows {
		for _, set := range sets {
			tables := simd.NewByteSetTables(set)
			want, ok := simd.WindowMask64Scalar(window, 0, &tables, 1)
			if !ok {
				return false
			}
			if got := kernel(window, &tables[0]); got != want {
				return false
			}
		}
	}
	return true
}

// NativeWideMask 在 SSE4 及以上层级报告原生宽窗口内核可用：AVX512 走 512 位内核，
// AVX2 与 SSE4 用两个已验证的 32 字节内核拼出等价结果，都在原生指令内完成。
func (b Backend) NativeWideMask() bool { return b.resolved >= TierSSE4 }

// WindowMask64 使用半字节查表在 64 字节宽窗口内生成候选起点掩码。
//
// AVX512BW/VBMI 能力下由 512 位寄存器一次判定整个窗口，并经一次性自检确认内核与
// 标量参考逐位一致；自检失败、能力不足或平台不支持时退回由已验证的 256 位内核
// 拼接出的等价结果，保持与 WindowMask 相同的 lane 对齐和窗口末端退让语义。
func (b Backend) WindowMask64(data []byte, off int, tables *simd.ByteSetTables, lanes int) (uint64, bool) {
	if tables == nil || off < 0 || off > len(data) || len(data)-off < simd.WideWidth {
		return 0, false
	}
	lanes = simd.ClampLanes(lanes)
	if b.resolved >= TierAVX512 {
		useVBMI, useBW := wideKernelsReady(b.resolved)
		if useVBMI {
			return composeWideKernel(data, off, tables, lanes, nativeByteSetMask64VBMI)
		}
		if useBW {
			return composeWideKernel(data, off, tables, lanes, nativeByteSetMask64AVX512)
		}
	}
	if b.resolved >= TierAVX2 {
		return composeWideFromNarrow(data, off, tables, lanes, nativeByteSetMask32AVX2)
	}
	if b.resolved >= TierSSE4 {
		return composeWideFromNarrow(data, off, tables, lanes, nativeByteSetMask32)
	}
	return simd.WindowMask64Scalar(data, off, tables, lanes)
}

// composeWideKernel 用 512 位内核逐 lane 生成 64 位位置掩码后组合。
func composeWideKernel(data []byte, off int, tables *simd.ByteSetTables, lanes int, kernel func([]byte, *[32]byte) uint64) (uint64, bool) {
	window := data[off : off+simd.WideWidth]
	first := kernel(window, &tables[0])
	// 首 lane 为空时组合结果必然为空，可跳过其余 lane 的宽内核调用。
	if first == 0 {
		return 0, true
	}
	if lanes == 1 {
		return first, true
	}
	strict := first
	laneMasks := [4]uint64{first}
	for lane := 1; lane < lanes; lane++ {
		mask := kernel(window, &tables[lane])
		laneMasks[lane] = mask
		strict &= mask >> uint(lane)
		if strict == 0 {
			return simd.ComposeWindowMask64Partial(first, lanes), true
		}
	}
	return simd.ComposeWindowMask64(laneMasks, lanes), true
}

// composeWideFromNarrow 用两个已验证的 32 字节内核拼出同一 lane 的 64 位位置掩码。
// 该路径不依赖 512 位指令，是宽窗口在低能力平台上的等价回退。
func composeWideFromNarrow(data []byte, off int, tables *simd.ByteSetTables, lanes int, kernel func([]byte, *[32]byte) uint32) (uint64, bool) {
	window := data[off : off+simd.WideWidth]
	first := uint64(kernel(window[:simd.SuperWidth], &tables[0])) |
		uint64(kernel(window[simd.SuperWidth:], &tables[0]))<<32
	// 组合结果必然包含首 lane 掩码的按位与，因此首 lane 为空时窗口内
	// 不可能有候选，直接省去其余 lane 的内核调用。稀疏语料下大多数窗口
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
		lo := kernel(window[:simd.SuperWidth], &tables[lane])
		hi := kernel(window[simd.SuperWidth:], &tables[lane])
		mask := uint64(lo) | uint64(hi)<<32
		laneMasks[lane] = mask
		strict &= mask >> uint(lane)
		if strict == 0 {
			return simd.ComposeWindowMask64Partial(first, lanes), true
		}
	}
	return simd.ComposeWindowMask64(laneMasks, lanes), true
}
