// Package simd 定义可移植向量后端契约。
package simd

import "github.com/smartwalle/scankit/internal/simd/generic"

// Vector 是一个向量的字节别名，可移植后端固定使用 16 字节宽度。
type Vector = generic.Vector

// Backend 定义向量后端需要提供的加载、掩码和位运算契约。
type Backend interface {
	Load([]byte, int) (Vector, bool)
	PartialLoad([]byte, int) Vector
	SafeLoad([]byte, int) Vector
	Store([]byte, int, Vector) bool
	EqualMask(Vector, Vector) uint16
	EqualByteMask(Vector, byte) uint16
	EqualByteMaskFold(Vector, byte) uint16
	ByteSetMask(Vector, [4]uint64) uint16
	// ByteSetMaskPrepared 使用配置期构建的预编译集合生成掩码，供热路径复用查找表。
	ByteSetMaskPrepared(Vector, *ByteSet) uint16
	// WindowMask 使用配置期构建的预编译集合按超向量窗口生成候选起点掩码，
	// 一次调用完成全部 lane 判定，数据不足一个窗口时返回 false。
	WindowMask([]byte, int, *ByteSetTables, int) (uint32, bool)
	// WindowMask64 使用同一组预编译集合按 64 字节宽窗口生成候选起点掩码，
	// 数据不足一个宽窗口时返回 false；缺少原生 64 字节内核的后端由
	// GenericBackend 的标量参考实现兜底。
	WindowMask64([]byte, int, *ByteSetTables, int) (uint64, bool)
	InRangeMask(Vector, byte, byte) uint16
	GreaterMask(Vector, Vector) uint16
	LessMask(Vector, Vector) uint16
	Not(Vector) Vector
	AndNot(Vector, Vector) Vector
	Select(uint16, Vector, Vector) Vector
	And(Vector, Vector) Vector
	Or(Vector, Vector) Vector
	Xor(Vector, Vector) Vector
	Permute(Vector, Vector) Vector
	AnyGreater(Vector, Vector) bool
	AnyLess(Vector, Vector) bool
}

// WideMaskProbe 由具备原生 64 字节宽窗口掩码内核的后端实现。未实现该接口的
// 后端一律按标量参考实现处理（见 HasNativeWideMask）。
type WideMaskProbe interface {
	// NativeWideMask 返回 WindowMask64 是否由原生内核完成。取值只反映"是否存在
	// 原生内核"，不反映运行时自检结果：自检失败时后端内部会自行退回标量实现，
	// 判定因此只可能偏保守。
	NativeWideMask() bool
}

// HasNativeWideMask 判断后端是否具备原生宽窗口掩码内核。
//
// GenericBackend 的 WindowMask64 逐字节查表，64 字节窗口需要 64 次集合判定；
// 实测在 64 KiB 语料上比单字节 memchr 慢约 2.5 倍，而原生内核快约 3 倍。约束枚举
// （guardRun）只有在原生内核可用时才值得取代候选文字索引，调用方据此选择候选来源。
func HasNativeWideMask(backend Backend) bool {
	if backend == nil {
		return false
	}
	probe, ok := backend.(WideMaskProbe)
	return ok && probe.NativeWideMask()
}

// GenericBackend 是纯 Go 标量参考实现，可在任意平台编译。
type GenericBackend struct{}

// Load 从 data 的 offset 处加载完整向量，数据不足时返回 false。
func (GenericBackend) Load(d []byte, o int) (Vector, bool) { return generic.Load(d, o) }

// PartialLoad 从 offset 处加载最多一个向量的数据，缺失字节补零。
func (GenericBackend) PartialLoad(d []byte, o int) Vector { return generic.PartialLoad(d, o) }

// SafeLoad 与 PartialLoad 等价，永不越界。
func (GenericBackend) SafeLoad(d []byte, o int) Vector { return generic.SafeLoad(d, o) }

// Store 将向量写入 dst 的 offset 处，空间不足时不写入并返回 false。
func (GenericBackend) Store(d []byte, o int, v Vector) bool { return generic.Store(d, o, v) }

// EqualMask 返回两个向量逐字节相等的掩码。
func (GenericBackend) EqualMask(a, b Vector) uint16 { return generic.EqualMask(a, b) }

// EqualByteMask 返回向量与单字节相等的逐字节掩码。
func (GenericBackend) EqualByteMask(a Vector, c byte) uint16 { return generic.EqualByteMask(a, c) }

// EqualByteMaskFold 返回按 ASCII 折叠大小写后比较的逐字节掩码。
func (GenericBackend) EqualByteMaskFold(a Vector, c byte) uint16 {
	return generic.EqualByteMaskFold(a, c)
}

// ByteSetMask 返回向量中各字节是否命中 256 位字节集的掩码。
func (GenericBackend) ByteSetMask(a Vector, set [4]uint64) uint16 {
	return generic.ByteSetMask(a, set)
}

// InRangeMask 返回落在闭区间 [lo, hi] 内的字节掩码。
func (GenericBackend) InRangeMask(a Vector, lo, hi byte) uint16 {
	return generic.InRangeMask(a, lo, hi)
}

// GreaterMask 返回逐字节无符号大于比较的掩码。
func (GenericBackend) GreaterMask(a, b Vector) uint16 { return generic.GreaterMask(a, b) }

// LessMask 返回逐字节无符号小于比较的掩码。
func (GenericBackend) LessMask(a, b Vector) uint16 { return generic.LessMask(a, b) }

// Not 返回逐字节取反的向量。
func (GenericBackend) Not(a Vector) Vector { return generic.Not(a) }

// AndNot 返回 a 按位与非 b 的向量。
func (GenericBackend) AndNot(a, b Vector) Vector { return generic.AndNot(a, b) }

// Select 按掩码逐字节选择 yes 或 no 的取值。
func (GenericBackend) Select(m uint16, yes, no Vector) Vector { return generic.Select(m, yes, no) }

// And 返回逐字节按位与的向量。
func (GenericBackend) And(a, b Vector) Vector { return generic.And(a, b) }

// Or 返回逐字节按位或的向量。
func (GenericBackend) Or(a, b Vector) Vector { return generic.Or(a, b) }

// Xor 返回逐字节按位异或的向量。
func (GenericBackend) Xor(a, b Vector) Vector { return generic.Xor(a, b) }

// Permute 按 index 的低 4 位重排 a 的字节。
func (GenericBackend) Permute(a, b Vector) Vector { return generic.Permute(a, b) }

// AnyGreater 判断是否存在 a 大于 b 的字节位置。
func (GenericBackend) AnyGreater(a, b Vector) bool { return generic.AnyGreater(a, b) }

// AnyLess 判断是否存在 a 小于 b 的字节位置。
func (GenericBackend) AnyLess(a, b Vector) bool { return generic.AnyLess(a, b) }

// Default 返回可移植的标量后端，作为缺少原生内核时的兜底实现。
func Default() Backend { return GenericBackend{} }
