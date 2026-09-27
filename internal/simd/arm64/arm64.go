// Package arm64 提供可移植的 ARM64 后端入口。
package arm64

import (
	"github.com/smartwalle/scankit/internal/simd"
	"github.com/smartwalle/scankit/internal/simd/generic"
	"golang.org/x/sys/cpu"
)

// Tier 表示 ARM64 后端允许使用的最低能力层级。
type Tier uint8

// TierScalar 表示纯标量展开路径，其余常量对应逐级提升的 ARM64 指令集层级。
const (
	TierScalar Tier = iota
	TierNEON
	TierSVE
	TierSVE2
)

// Backend 提供 ARM64 入口，并在能力允许时使用展开热循环。
type Backend struct {
	simd.GenericBackend
	// tier 是构建时请求的能力层级，供分派门禁与诊断读取。
	tier Tier
	// resolved 是请求层级按构建时能力探测裁剪后的实际生效层级。
	// 热路径直接读取该字段，避免每次调用重复读取 CPU 能力状态。
	resolved Tier
}

// NewWithTier 创建带能力门禁的 ARM64 后端。
// 真实能力不足时会在这里裁剪一次，之后热路径不再重复判定。
func NewWithTier(tier Tier) simd.Backend {
	if tier > TierSVE2 {
		tier = TierScalar
	}
	return Backend{tier: tier, resolved: resolveTier(tier)}
}

// resolveTier 将请求层级裁剪到当前进程真实具备的能力。
func resolveTier(tier Tier) Tier {
	if detected := detectedTier(); tier > detected {
		return detected
	}
	return tier
}

// Load 提供 ARM64 分派入口的完整窗口加载，越界时保持安全失败。
func (Backend) Load(data []byte, offset int) (generic.Vector, bool) {
	var v generic.Vector
	if offset < 0 || offset > len(data) || len(data)-offset < generic.Width {
		return v, false
	}
	copy(v[:], data[offset:offset+generic.Width])
	return v, true
}

// EqualByteMask 生成逐字节相等掩码。
func (b Backend) EqualByteMask(v generic.Vector, value byte) uint16 {
	if b.resolved >= TierNEON {
		return nativeEqualByteMask(&v, value)
	}
	// 不具备 NEON 时仍使用展开标量路径，保证与原生路径完全一致。
	return equalByteMaskUnrolled(v, value)
}

func detectedTier() Tier {
	if cpu.ARM64.HasSVE2 {
		return TierSVE2
	}
	if cpu.ARM64.HasSVE {
		return TierSVE
	}
	if cpu.ARM64.HasASIMD {
		return TierNEON
	}
	return TierScalar
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
	// 折叠后只需同时比较小写与大写两种写法，非字母时两者相同。
	upper := value
	if value >= 'a' && value <= 'z' {
		upper = value - ('a' - 'A')
	}
	if b.resolved >= TierNEON {
		return nativeEqualByteMaskFold(&v, value, upper)
	}
	var mask uint16
	for i, item := range v {
		if item >= 'A' && item <= 'Z' {
			item += 'a' - 'A'
		}
		if item == value {
			mask |= 1 << uint(i)
		}
	}
	return mask
}

// InRangeMask 返回闭区间范围掩码。
func (b Backend) InRangeMask(v generic.Vector, lo, hi byte) uint16 {
	if b.resolved >= TierNEON {
		// 原生路径一次完成全部 16 字节的无符号范围比较并直接压缩为位掩码。
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

// ByteSetMask 使用 ARM64 后端展开循环筛选字节集合，保持与通用掩码逐字节一致。
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

// ByteSetMaskPrepared 使用 NEON 半字节查表判定窗口内每个字节是否属于预编译集合。
func (b Backend) ByteSetMaskPrepared(v generic.Vector, set *simd.ByteSet) uint16 {
	if set == nil {
		return 0
	}
	if b.resolved >= TierNEON {
		return nativeByteSetMask(&v, set.TableVector())
	}
	return set.Mask(v)
}

// EqualMask 返回两向量逐字节相等掩码。
func (b Backend) EqualMask(a, c generic.Vector) uint16 {
	if b.resolved >= TierNEON {
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

// AnyGreater 判断是否存在 a 大于 c 的字节位置。
func (b Backend) AnyGreater(a, c generic.Vector) bool { return b.GreaterMask(a, c) != 0 }

// AnyLess 判断是否存在 a 小于 c 的字节位置。
func (b Backend) AnyLess(a, c generic.Vector) bool { return b.LessMask(a, c) != 0 }

// New 返回固定使用展开标量路径的 ARM64 后端，用于对照原生实现的结果。
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
