package simd

import "github.com/smartwalle/scankit/internal/simd/generic"

// SuperVector 由两个基础向量组成，用于统一处理较宽的候选扫描窗口。
type SuperVector struct {
	Lo Vector
	Hi Vector
}

// SuperWidth 返回超向量可容纳的字节数。
const SuperWidth = generic.Width * 2

// WideWidth 返回宽窗口后端一次判定可容纳的字节数，为超向量窗口的两倍。
// 256 位后端用它把两个 32 字节窗口合并成一次调用；512 位后端用它驱动
// 单个 zmm 寄存器完成全部 64 字节判定。
const WideWidth = SuperWidth * 2

// LoadSuper 从指定位置加载完整超向量；数据不足时返回 false。
func LoadSuper(data []byte, offset int) (SuperVector, bool) {
	return LoadSuperWithBackend(GenericBackend{}, data, offset)
}

// LoadSuperWithBackend 使用指定基础后端加载完整超向量，保证分派路径与基础向量一致。
func LoadSuperWithBackend(backend Backend, data []byte, offset int) (SuperVector, bool) {
	var out SuperVector
	if offset < 0 || offset > len(data) || len(data)-offset < SuperWidth {
		return out, false
	}
	if backend == nil {
		return out, false
	}
	lo, okLo := backend.Load(data, offset)
	hi, okHi := backend.Load(data, offset+generic.Width)
	if !okLo || !okHi {
		return out, false
	}
	out.Lo, out.Hi = lo, hi
	return out, true
}

// PartialLoadSuper 安全加载超向量，缺失字节补零。
func PartialLoadSuper(data []byte, offset int) SuperVector {
	return PartialLoadSuperWithBackend(GenericBackend{}, data, offset)
}

// PartialLoadSuperWithBackend 使用指定后端进行安全部分加载。
func PartialLoadSuperWithBackend(backend Backend, data []byte, offset int) SuperVector {
	if backend == nil {
		return SuperVector{}
	}
	return SuperVector{Lo: backend.PartialLoad(data, offset), Hi: backend.PartialLoad(data, offset+generic.Width)}
}

// Store 将完整超向量写入目标；目标空间不足时不写入。
func (v SuperVector) Store(dst []byte, offset int) bool {
	return v.StoreWithBackend(GenericBackend{}, dst, offset)
}

// StoreWithBackend 使用指定后端写回完整超向量。
func (v SuperVector) StoreWithBackend(backend Backend, dst []byte, offset int) bool {
	if offset < 0 || offset > len(dst) || len(dst)-offset < SuperWidth {
		return false
	}
	return backend != nil && backend.Store(dst, offset, v.Lo) && backend.Store(dst, offset+generic.Width, v.Hi)
}

// EqualByteMask 返回低、高半区的逐字节相等掩码。
func (v SuperVector) EqualByteMask(value byte) (uint16, uint16) {
	return generic.EqualByteMask(v.Lo, value), generic.EqualByteMask(v.Hi, value)
}

// EqualByteMaskWithBackend 使用指定后端生成两段掩码。
// 传入空后端时返回全零，避免在分派失败后误报候选。
func (v SuperVector) EqualByteMaskWithBackend(backend Backend, value byte) (uint16, uint16) {
	if backend == nil {
		return 0, 0
	}
	return backend.EqualByteMask(v.Lo, value), backend.EqualByteMask(v.Hi, value)
}

// InRangeMask 返回低、高半区落在闭区间内的字节掩码。
func (v SuperVector) InRangeMask(lo, hi byte) (uint16, uint16) {
	return generic.InRangeMask(v.Lo, lo, hi), generic.InRangeMask(v.Hi, lo, hi)
}

// InRangeMaskWithBackend 使用指定后端生成闭区间掩码。
func (v SuperVector) InRangeMaskWithBackend(backend Backend, lo, hi byte) (uint16, uint16) {
	if backend == nil || lo > hi {
		return 0, 0
	}
	return backend.InRangeMask(v.Lo, lo, hi), backend.InRangeMask(v.Hi, lo, hi)
}

// AnyEqual 判断超向量中是否存在给定字节。
func (v SuperVector) AnyEqual(value byte) bool {
	lo, hi := v.EqualByteMask(value)
	return lo != 0 || hi != 0
}

// AnyEqualWithBackend 判断指定后端处理的超向量是否包含目标字节。
func (v SuperVector) AnyEqualWithBackend(backend Backend, value byte) bool {
	lo, hi := v.EqualByteMaskWithBackend(backend, value)
	return lo != 0 || hi != 0
}
