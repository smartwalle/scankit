// Package x86 提供可移植的 x86 后端入口。
package x86

import (
	"github.com/smartwalle/scankit/internal/simd"
	"github.com/smartwalle/scankit/internal/simd/generic"
	"golang.org/x/sys/cpu"
)

// Tier 表示 x86 后端允许使用的最低能力层级。
type Tier uint8

const (
	TierScalar Tier = iota
	TierSSE
	TierSSE4
	TierAVX2
	TierAVX512
	TierAVX512VBMI
)

// Backend 提供 x86 入口，并在能力允许时使用对应的展开热循环。
type Backend struct {
	simd.GenericBackend
	// tier 是构建时请求的能力层级，供分派门禁与诊断读取。
	tier Tier
	// resolved 是请求层级按构建时能力探测裁剪后的实际生效层级。
	// 热路径直接读取该字段，避免每次调用重复读取 CPU 能力状态。
	resolved Tier
}

// NewWithTier 创建带能力门禁的 x86 后端。
// 调用方必须先完成运行时探测；真实能力不足时会在这里裁剪一次，之后热路径不再重复判定。
func NewWithTier(tier Tier) simd.Backend {
	if tier > TierAVX512VBMI {
		tier = TierScalar
	}
	return Backend{tier: tier, resolved: resolveTier(tier)}
}

// resolveTier 把请求层级裁剪到当前进程真实具备的能力，避免误触发高阶指令。
func resolveTier(tier Tier) Tier {
	if detected := detectedTier(); tier > detected {
		return detected
	}
	return tier
}

// detectedTier 按真实 CPU 能力给出可用层级。
//
// AVX512 层级要求 AVX512BW：字节粒度的集合判定依赖 VPSHUFB/VPCMPGTB/VPTESTMB，
// 仅具备 AVX512F 的机器必须退回 AVX2，避免执行不支持的指令。
func detectedTier() Tier {
	if cpu.X86.HasAVX512VBMI && cpu.X86.HasAVX512BW {
		return TierAVX512VBMI
	}
	if cpu.X86.HasAVX512 && cpu.X86.HasAVX512BW {
		return TierAVX512
	}
	if cpu.X86.HasAVX2 {
		return TierAVX2
	}
	if cpu.X86.HasSSE41 || cpu.X86.HasSSE42 {
		return TierSSE4
	}
	if cpu.X86.HasSSE2 {
		return TierSSE
	}
	return TierScalar
}

// Load 在 x86 分派入口实现完整窗口加载；边界判断与通用后端保持一致。
func (Backend) Load(data []byte, offset int) (generic.Vector, bool) {
	var v generic.Vector
	if offset < 0 || offset > len(data) || len(data)-offset < generic.Width {
		return v, false
	}
	copy(v[:], data[offset:offset+generic.Width])
	return v, true
}

// EqualByteMask 使用连续窗口生成掩码，避免热路径通过接口再次分派。
func (b Backend) EqualByteMask(v generic.Vector, value byte) uint16 {
	if b.resolved >= TierSSE {
		if b.resolved >= TierAVX2 {
			return nativeEqualByteMaskAVX2(&v, value)
		}
		return nativeEqualByteMask(&v, value)
	}
	// SSE4 及以上路径按四字节组展开，避免每次候选扫描再进入接口调用。
	if b.resolved >= TierSSE4 {
		return equalByteMaskUnrolled(v, value)
	}
	var mask uint16
	for i, b := range v {
		if b == value {
			mask |= 1 << uint(i)
		}
	}
	return mask
}

func equalByteMaskUnrolled(v generic.Vector, value byte) uint16 {
	var mask uint16
	for base := 0; base < generic.Width; base += 4 {
		if v[base] == value {
			mask |= 1 << uint(base)
		}
		if v[base+1] == value {
			mask |= 1 << uint(base+1)
		}
		if v[base+2] == value {
			mask |= 1 << uint(base+2)
		}
		if v[base+3] == value {
			mask |= 1 << uint(base+3)
		}
	}
	return mask
}

// EqualByteMaskFold 生成忽略 ASCII 大小写后的掩码。
func (b Backend) EqualByteMaskFold(v generic.Vector, value byte) uint16 {
	if value >= 'A' && value <= 'Z' {
		value += 'a' - 'A'
	}
	if b.resolved >= TierSSE4 {
		var mask uint16
		for base := 0; base < generic.Width; base += 4 {
			for i := base; i < base+4; i++ {
				item := v[i]
				if item >= 'A' && item <= 'Z' {
					item += 'a' - 'A'
				}
				if item == value {
					mask |= 1 << uint(i)
				}
			}
		}
		return mask
	}
	var mask uint16
	for i, b := range v {
		if b >= 'A' && b <= 'Z' {
			b += 'a' - 'A'
		}
		if b == value {
			mask |= 1 << uint(i)
		}
	}
	return mask
}

// InRangeMask 返回闭区间范围掩码。
func (b Backend) InRangeMask(v generic.Vector, lo, hi byte) uint16 {
	if b.resolved >= TierSSE {
		// SSE2 饱和减法一次完成全部 16 字节的闭区间判定。
		return nativeInRangeMask(&v, lo, hi)
	}
	var mask uint16
	for i, b := range v {
		if b >= lo && b <= hi {
			mask |= 1 << uint(i)
		}
	}
	return mask
}

// ByteSetMaskPrepared 使用 PSHUFB 半字节查表判定窗口内每个字节是否属于预编译集合。
// PSHUFB 属于 SSSE3，由 SSE4.1 隐含，因此能力门禁沿用 TierSSE4。
func (b Backend) ByteSetMaskPrepared(v generic.Vector, set *simd.ByteSet) uint16 {
	if set == nil {
		return 0
	}
	if b.resolved >= TierSSE4 {
		return nativeByteSetMask(&v, set.TableVector())
	}
	return set.Mask(v)
}

// ByteSetMask 使用后端专用展开循环筛选字节集合，供 NFA 起点热路径调用。
func (b Backend) ByteSetMask(v generic.Vector, set [4]uint64) uint16 {
	var mask uint16
	for base := 0; base < generic.Width; base += 4 {
		if set[v[base]/64]&(1<<uint(v[base]%64)) != 0 {
			mask |= 1 << uint(base)
		}
		if set[v[base+1]/64]&(1<<uint(v[base+1]%64)) != 0 {
			mask |= 1 << uint(base+1)
		}
		if set[v[base+2]/64]&(1<<uint(v[base+2]%64)) != 0 {
			mask |= 1 << uint(base+2)
		}
		if set[v[base+3]/64]&(1<<uint(v[base+3]%64)) != 0 {
			mask |= 1 << uint(base+3)
		}
	}
	return mask
}

// EqualMask 返回两向量逐字节相等掩码。
func (b Backend) EqualMask(a, c generic.Vector) uint16 {
	if b.resolved >= TierAVX2 {
		return nativeEqualMaskAVX2(&a, &c)
	}
	if b.resolved >= TierSSE {
		return nativeEqualMask(&a, &c)
	}
	var mask uint16
	for base := 0; base < generic.Width; base += 4 {
		if a[base] == c[base] {
			mask |= 1 << uint(base)
		}
		if a[base+1] == c[base+1] {
			mask |= 1 << uint(base+1)
		}
		if a[base+2] == c[base+2] {
			mask |= 1 << uint(base+2)
		}
		if a[base+3] == c[base+3] {
			mask |= 1 << uint(base+3)
		}
	}
	return mask
}

// GreaterMask 返回逐字节无符号大于掩码。
func (b Backend) GreaterMask(a, c generic.Vector) uint16 {
	var mask uint16
	for base := 0; base < generic.Width; base += 4 {
		if a[base] > c[base] {
			mask |= 1 << uint(base)
		}
		if a[base+1] > c[base+1] {
			mask |= 1 << uint(base+1)
		}
		if a[base+2] > c[base+2] {
			mask |= 1 << uint(base+2)
		}
		if a[base+3] > c[base+3] {
			mask |= 1 << uint(base+3)
		}
	}
	return mask
}

// LessMask 返回逐字节无符号小于掩码。
func (b Backend) LessMask(a, c generic.Vector) uint16 {
	var mask uint16
	for base := 0; base < generic.Width; base += 4 {
		if a[base] < c[base] {
			mask |= 1 << uint(base)
		}
		if a[base+1] < c[base+1] {
			mask |= 1 << uint(base+1)
		}
		if a[base+2] < c[base+2] {
			mask |= 1 << uint(base+2)
		}
		if a[base+3] < c[base+3] {
			mask |= 1 << uint(base+3)
		}
	}
	return mask
}

func (b Backend) AnyGreater(a, c generic.Vector) bool { return b.GreaterMask(a, c) != 0 }
func (b Backend) AnyLess(a, c generic.Vector) bool    { return b.LessMask(a, c) != 0 }

// New 返回固定使用标量展开路径的 x86 后端，用于对照原生实现的结果。
func New() simd.Backend { return Backend{tier: TierScalar, resolved: TierScalar} }

// TierOf 返回构建后端时请求的能力层级，供分派门禁和诊断使用。
// 请求层级可能高于当前进程真实具备的能力，实际生效层级请用 EffectiveTierOf。
func TierOf(backend simd.Backend) (Tier, bool) {
	b, ok := backend.(Backend)
	if !ok {
		return TierScalar, false
	}
	return b.tier, true
}

// EffectiveTierOf 返回后端构建时裁剪后的实际生效层级，供诊断与跨平台核验使用。
func EffectiveTierOf(backend simd.Backend) (Tier, bool) {
	b, ok := backend.(Backend)
	if !ok {
		return TierScalar, false
	}
	return b.resolved, true
}

var _ simd.Backend = Backend{}

var _ = generic.Width
