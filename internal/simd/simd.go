// Package simd 定义可移植向量后端契约。
package simd

import "github.com/smartwalle/scankit/internal/simd/generic"

type Vector = generic.Vector
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

type GenericBackend struct{}

func (GenericBackend) Load(d []byte, o int) (Vector, bool)   { return generic.Load(d, o) }
func (GenericBackend) PartialLoad(d []byte, o int) Vector    { return generic.PartialLoad(d, o) }
func (GenericBackend) SafeLoad(d []byte, o int) Vector       { return generic.SafeLoad(d, o) }
func (GenericBackend) Store(d []byte, o int, v Vector) bool  { return generic.Store(d, o, v) }
func (GenericBackend) EqualMask(a, b Vector) uint16          { return generic.EqualMask(a, b) }
func (GenericBackend) EqualByteMask(a Vector, c byte) uint16 { return generic.EqualByteMask(a, c) }
func (GenericBackend) EqualByteMaskFold(a Vector, c byte) uint16 {
	return generic.EqualByteMaskFold(a, c)
}
func (GenericBackend) ByteSetMask(a Vector, set [4]uint64) uint16 {
	return generic.ByteSetMask(a, set)
}
func (GenericBackend) InRangeMask(a Vector, lo, hi byte) uint16 {
	return generic.InRangeMask(a, lo, hi)
}
func (GenericBackend) GreaterMask(a, b Vector) uint16         { return generic.GreaterMask(a, b) }
func (GenericBackend) LessMask(a, b Vector) uint16            { return generic.LessMask(a, b) }
func (GenericBackend) Not(a Vector) Vector                    { return generic.Not(a) }
func (GenericBackend) AndNot(a, b Vector) Vector              { return generic.AndNot(a, b) }
func (GenericBackend) Select(m uint16, yes, no Vector) Vector { return generic.Select(m, yes, no) }
func (GenericBackend) And(a, b Vector) Vector                 { return generic.And(a, b) }
func (GenericBackend) Or(a, b Vector) Vector                  { return generic.Or(a, b) }
func (GenericBackend) Xor(a, b Vector) Vector                 { return generic.Xor(a, b) }
func (GenericBackend) Permute(a, b Vector) Vector             { return generic.Permute(a, b) }
func (GenericBackend) AnyGreater(a, b Vector) bool            { return generic.AnyGreater(a, b) }
func (GenericBackend) AnyLess(a, b Vector) bool               { return generic.AnyLess(a, b) }
func Default() Backend                                        { return GenericBackend{} }
