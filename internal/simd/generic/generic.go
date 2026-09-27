// Package generic 提供跨平台向量语义的标量参考实现。
package generic

// Width 是参考向量宽度。
const Width = 16

// Vector 是固定宽度字节向量。
type Vector [Width]byte

// Bytes 返回向量的字节副本，修改副本不会影响原向量。
func (v Vector) Bytes() []byte { return append([]byte(nil), v[:]...) }

// Equal 判断两个向量是否逐字节相等。
func (v Vector) Equal(other Vector) bool { return v == other }

// Load 从 data[offset:] 加载完整向量；数据不足时返回 false。
func Load(data []byte, offset int) (Vector, bool) {
	var v Vector
	if offset < 0 || len(data)-offset < Width {
		return v, false
	}
	copy(v[:], data[offset:offset+Width])
	return v, true
}

// PartialLoad 安全加载最多 Width 字节，缺失字节补零。
func PartialLoad(data []byte, offset int) Vector {
	var v Vector
	if offset < 0 || offset >= len(data) {
		return v
	}
	end := len(data)
	if Width <= len(data)-offset {
		end = offset + Width
	}
	copy(v[:], data[offset:end])
	return v
}

// Store 将完整向量写入目标切片；空间不足时不写入并返回 false。
func Store(dst []byte, offset int, v Vector) bool {
	if offset < 0 || offset > len(dst) || len(dst)-offset < Width {
		return false
	}
	copy(dst[offset:offset+Width], v[:])
	return true
}

// SafeLoad 是不会越界的部分加载别名。
func SafeLoad(data []byte, offset int) Vector { return PartialLoad(data, offset) }

// EqualMask 返回逐字节相等掩码，最低位对应第 0 字节。
func EqualMask(a, b Vector) uint16 {
	var mask uint16
	for i := range a {
		if a[i] == b[i] {
			mask |= 1 << i
		}
	}
	return mask
}

// EqualByteMask 返回等于 value 的逐字节掩码。
func EqualByteMask(a Vector, value byte) uint16 {
	var mask uint16
	for i, item := range a {
		if item == value {
			mask |= 1 << i
		}
	}
	return mask
}

// EqualByteMaskFold 返回忽略 ASCII 大小写后的等值掩码。
func EqualByteMaskFold(a Vector, value byte) uint16 { return EqualByteMaskFoldASCII(a, value) }

// EqualByteMaskFoldASCII 返回忽略 ASCII 大小写后的等值掩码。
func EqualByteMaskFoldASCII(a Vector, value byte) uint16 {
	if value >= 'A' && value <= 'Z' {
		value += 'a' - 'A'
	}
	var mask uint16
	for i, item := range a {
		if item >= 'A' && item <= 'Z' {
			item += 'a' - 'A'
		}
		if item == value {
			mask |= 1 << i
		}
	}
	return mask
}

// ByteSetMask 返回向量中属于 256 位字节集合的位置掩码。
func ByteSetMask(a Vector, set [4]uint64) uint16 {
	var mask uint16
	for i, value := range a {
		if set[value/64]&(1<<uint(value%64)) != 0 {
			mask |= 1 << uint(i)
		}
	}
	return mask
}

// InRangeMask 返回落在闭区间 [lo, hi] 内的字节掩码。
func InRangeMask(a Vector, lo, hi byte) uint16 {
	var mask uint16
	for i, value := range a {
		if value >= lo && value <= hi {
			mask |= 1 << i
		}
	}
	return mask
}

// EqualAt 判断两个向量在指定位置是否相等。
func EqualAt(a, b Vector, index int) bool { return index >= 0 && index < Width && a[index] == b[index] }

// EqualMaskAt 返回从指定位置开始比较 count 个字节的掩码。
func EqualMaskAt(a, b Vector, start, count int) uint16 {
	mask := EqualMask(a, b)
	return mask & MaskRange(start, count)
}

// AnyEqual 判断向量中是否存在等于指定字节的位置。
func AnyEqual(a Vector, value byte) bool { return EqualByteMask(a, value) != 0 }

// ContainsByte 判断向量中是否存在指定字节。
func ContainsByte(a Vector, value byte) bool { return AnyEqual(a, value) }

// AllEqual 判断向量每个位置是否都等于指定字节。
func AllEqual(a Vector, value byte) bool { return EqualByteMask(a, value) == ^uint16(0) }

// ByteAt 返回指定位置的字节。
func ByteAt(a Vector, index int) (byte, bool) {
	if index < 0 || index >= Width {
		return 0, false
	}
	return a[index], true
}

// FirstEqualByte 返回第一个等于指定字节的位置。
func FirstEqualByte(a Vector, value byte) (int, bool) {
	mask := EqualByteMask(a, value)
	if mask == 0 {
		return 0, false
	}
	for i := range Width {
		if mask&(1<<i) != 0 {
			return i, true
		}
	}
	return 0, false
}

// LastEqualByte 返回最后一个等于指定字节的位置。
func LastEqualByte(a Vector, value byte) (int, bool) {
	mask := EqualByteMask(a, value)
	if mask == 0 {
		return 0, false
	}
	for i := Width - 1; i >= 0; i-- {
		if mask&(1<<i) != 0 {
			return i, true
		}
	}
	return 0, false
}

// AnyEqualRange 判断指定区间内是否存在目标字节。
func AnyEqualRange(a Vector, value byte, start, count int) bool {
	return EqualByteMask(a, value)&MaskRange(start, count) != 0
}

// GreaterMask 返回逐字节无符号大于比较的掩码，最低位对应第 0 字节。
func GreaterMask(a, b Vector) uint16 {
	var mask uint16
	for i := range a {
		if a[i] > b[i] {
			mask |= 1 << i
		}
	}
	return mask
}

// LessMask 返回逐字节无符号小于比较的掩码，最低位对应第 0 字节。
func LessMask(a, b Vector) uint16 {
	var mask uint16
	for i := range a {
		if a[i] < b[i] {
			mask |= 1 << i
		}
	}
	return mask
}

// AnyGreater 判断是否存在大于对应位置的字节。
func AnyGreater(a, b Vector) bool { return GreaterMask(a, b) != 0 }

// AnyLess 判断是否存在小于对应位置的字节。
func AnyLess(a, b Vector) bool { return LessMask(a, b) != 0 }

// Select 按掩码逐字节选择 yes 或 no，掩码置位时取 yes。
func Select(mask uint16, yes, no Vector) (out Vector) {
	for i := range out {
		if mask&(1<<i) != 0 {
			out[i] = yes[i]
		} else {
			out[i] = no[i]
		}
	}
	return out
}

// Blend 按掩码混合 a 和 b，掩码置位时取 b。
func Blend(a, b Vector, mask uint16) Vector { return Select(mask, b, a) }

// And 返回逐字节按位与的向量。
func And(a, b Vector) (out Vector) {
	for i := range out {
		out[i] = a[i] & b[i]
	}
	return out
}

// Or 返回逐字节按位或的向量。
func Or(a, b Vector) (out Vector) {
	for i := range out {
		out[i] = a[i] | b[i]
	}
	return out
}

// Xor 返回逐字节按位异或的向量。
func Xor(a, b Vector) (out Vector) {
	for i := range out {
		out[i] = a[i] ^ b[i]
	}
	return out
}

// Not 对向量逐字节取反。
func Not(a Vector) (out Vector) {
	for i := range out {
		out[i] = ^a[i]
	}
	return out
}

// AndNot 计算 a 与 b 取反后的逐字节按位与。
func AndNot(a, b Vector) (out Vector) {
	for i := range out {
		out[i] = a[i] &^ b[i]
	}
	return out
}

// ShiftLeft 对每个字节执行逻辑左移，移位量不小于 8 时结果为 0。
func ShiftLeft(a Vector, bits uint) (out Vector) {
	for i := range out {
		if bits < 8 {
			out[i] = a[i] << bits
		}
	}
	return out
}

// ShiftRight 对每个字节执行逻辑右移，移位量不小于 8 时结果为 0。
func ShiftRight(a Vector, bits uint) (out Vector) {
	for i := range out {
		if bits < 8 {
			out[i] = a[i] >> bits
		}
	}
	return out
}

// Shuffle 根据 index 低 4 位重排 a。
func Shuffle(a, index Vector) (out Vector) {
	for i := range out {
		out[i] = a[index[i]&0x0f]
	}
	return out
}

// Permute 根据索引低四位重排向量元素。
func Permute(a, index Vector) Vector { return Shuffle(a, index) }

// PopCount 返回掩码中置位数量。
func PopCount(mask uint16) int {
	count := 0
	for mask != 0 {
		mask &= mask - 1
		count++
	}
	return count
}

// MaskRange 返回从 start 开始的 count 位掩码，越界部分自动裁剪。
func MaskRange(start, count int) uint16 {
	if start < 0 {
		start = 0
	}
	if count <= 0 || start >= Width {
		return 0
	}
	if count > Width-start {
		count = Width - start
	}
	return uint16(((uint32(1) << count) - 1) << start)
}

// Reverse 返回字节顺序反转的向量。
func Reverse(a Vector) (out Vector) {
	for i := range a {
		out[len(a)-1-i] = a[i]
	}
	return out
}

// Min 返回逐字节取最小值的向量。
func Min(a, b Vector) (out Vector) {
	for i := range out {
		out[i] = min(a[i], b[i])
	}
	return
}

// Max 返回逐字节取最大值的向量。
func Max(a, b Vector) (out Vector) {
	for i := range out {
		out[i] = max(a[i], b[i])
	}
	return
}

// Broadcast 返回所有字节都等于 c 的向量。
func Broadcast(c byte) (out Vector) {
	for i := range out {
		out[i] = c
	}
	return
}

// RotateLeft 将字节整体左移 n 位，n 按向量宽度取模。
func RotateLeft(a Vector, n int) (out Vector) {
	if n < 0 {
		n = 0
	}
	n %= Width
	for i := range out {
		out[i] = a[(i+n)%Width]
	}
	return out
}

// RotateRight 将字节整体右移 n 位，n 按向量宽度取模。
func RotateRight(a Vector, n int) (out Vector) {
	if n < 0 {
		n = 0
	}
	return RotateLeft(a, Width-(n%Width))
}

// RotateRightSafe 把负数位移归一为 0 后再右移。
func RotateRightSafe(a Vector, n int) (out Vector) {
	if n < 0 {
		n = 0
	}
	return RotateRight(a, n)
}

// IsZero 判断向量的所有字节是否都为 0。
func IsZero(a Vector) bool {
	for _, v := range a {
		if v != 0 {
			return false
		}
	}
	return true
}
