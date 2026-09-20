// Package arm64 提供可移植的 ARM64 后端入口。
package arm64

import (
	"github.com/smartwalle/scankit/internal/simd"
	"github.com/smartwalle/scankit/internal/simd/generic"
	"golang.org/x/sys/cpu"
)

// Tier 表示 ARM64 后端允许使用的最低能力层级。
type Tier uint8

const (
	TierScalar Tier = iota
	TierNEON
	TierSVE
	TierSVE2
)

// Backend 提供 ARM64 入口，并在能力允许时使用展开热循环。
type Backend struct {
	simd.GenericBackend
	tier Tier
}

// effectiveTier 将请求层级裁剪到当前进程真实具备的能力。
func (b Backend) effectiveTier() Tier {
	detected := detectedTier()
	if b.tier > detected {
		return detected
	}
	return b.tier
}

// NewWithTier 创建带能力门禁的 ARM64 后端。
func NewWithTier(tier Tier) simd.Backend {
	if tier > TierSVE2 {
		tier = TierScalar
	}
	return Backend{tier: tier}
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
	if b.effectiveTier() >= TierNEON {
		var compared generic.Vector
		nativeEqualByteVector(&v, value, &compared)
		var mask uint16
		for i, item := range compared {
			if item != 0 {
				mask |= 1 << uint(i)
			}
		}
		return mask
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
	if b.effectiveTier() >= TierNEON {
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
	if b.effectiveTier() >= TierNEON {
		var mask uint16
		for base := 0; base < generic.Width; base += 4 {
			for i := base; i < base+4; i++ {
				if v[i] >= lo && v[i] <= hi {
					mask |= 1 << uint(i)
				}
			}
		}
		return mask
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

// EqualMask 返回两向量逐字节相等掩码。
func (b Backend) EqualMask(a, c generic.Vector) uint16 {
	if b.effectiveTier() >= TierNEON {
		var compared generic.Vector
		nativeEqualVector(&a, &c, &compared)
		var mask uint16
		for i, item := range compared {
			if item != 0 {
				mask |= 1 << uint(i)
			}
		}
		return mask
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

func New() simd.Backend { return Backend{tier: TierScalar} }

// TierOf 返回后端实际采用的能力层级。
func TierOf(backend simd.Backend) (Tier, bool) {
	b, ok := backend.(Backend)
	if !ok {
		return TierScalar, false
	}
	return b.tier, true
}

var _ simd.Backend = Backend{}
