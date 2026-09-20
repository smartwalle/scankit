// Package util 提供 compiler/runtime 共用的无分配基础容器。
package util

// BitSet 是可增长的位集合。零值可直接使用。
type BitSet struct{ words []uint64 }

func (b BitSet) Clone() BitSet { return BitSet{words: append([]uint64(nil), b.words...)} }
func (b BitSet) Any() bool {
	for _, w := range b.words {
		if w != 0 {
			return true
		}
	}
	return false
}
func (b *BitSet) Difference(other *BitSet) {
	if b == nil || other == nil {
		return
	}
	for i := range b.words {
		if i < len(other.words) {
			b.words[i] &^= other.words[i]
		}
	}
}

func (b *BitSet) Union(other *BitSet) {
	if b == nil || other == nil {
		return
	}
	if len(b.words) < len(other.words) {
		b.words = append(b.words, make([]uint64, len(other.words)-len(b.words))...)
	}
	for i, v := range other.words {
		b.words[i] |= v
	}
}
func (b *BitSet) Intersect(other *BitSet) {
	if b == nil {
		return
	}
	if other == nil {
		b.words = nil
		return
	}
	if len(b.words) > len(other.words) {
		b.words = b.words[:len(other.words)]
	}
	for i := range b.words {
		b.words[i] &= other.words[i]
	}
}

// NewBitSet 创建可容纳 n 位的集合。
func NewBitSet(n int) BitSet {
	if n <= 0 {
		return BitSet{}
	}
	if n > int(^uint(0)>>1)-63 {
		return BitSet{}
	}
	return BitSet{words: make([]uint64, (n+63)/64)}
}

// Set 设置 bit。
func (b *BitSet) Set(bit int) {
	if b == nil || bit < 0 {
		return
	}
	if bit > int(^uint(0)>>1)-64 {
		return
	}
	word := bit >> 6
	if word >= len(b.words) {
		grown := make([]uint64, word+1)
		copy(grown, b.words)
		b.words = grown
	}
	b.words[word] |= uint64(1) << (bit & 63)
}

// Clear 清除 bit。
func (b *BitSet) Clear(bit int) {
	if bit >= 0 && bit>>6 < len(b.words) {
		b.words[bit>>6] &^= uint64(1) << (bit & 63)
	}
}

// Has 判断 bit 是否存在。
func (b BitSet) Has(bit int) bool {
	return bit >= 0 && bit>>6 < len(b.words) && b.words[bit>>6]&(uint64(1)<<(bit&63)) != 0
}

// Reset 清空集合内容但保留容量。
func (b *BitSet) Reset() {
	for i := range b.words {
		b.words[i] = 0
	}
}

// Count 返回置位数量。
func (b BitSet) Count() int {
	count := 0
	for _, word := range b.words {
		for word != 0 {
			word &= word - 1
			count++
		}
	}
	return count
}

// CountRange 返回闭区间内的置位数量。
func (b BitSet) CountRange(from, to int) int {
	if from < 0 {
		from = 0
	}
	if to < from || len(b.words) == 0 {
		return 0
	}
	start, end := from>>6, to>>6
	if start >= len(b.words) {
		return 0
	}
	if end >= len(b.words) {
		end = len(b.words) - 1
	}
	count := 0
	for i := start; i <= end; i++ {
		word := b.words[i]
		if i == start {
			word &^= (uint64(1) << uint(from&63)) - 1
		}
		if i == end && to&63 < 63 {
			word &= (uint64(1) << uint((to&63)+1)) - 1
		}
		for word != 0 {
			word &= word - 1
			count++
		}
	}
	return count
}

// Intersects 判断两个位集合是否存在公共位。
func (b BitSet) Intersects(other BitSet) bool {
	limit := len(b.words)
	if len(other.words) < limit {
		limit = len(other.words)
	}
	for i := 0; i < limit; i++ {
		if b.words[i]&other.words[i] != 0 {
			return true
		}
	}
	return false
}

// ContainsAll 判断当前集合是否包含另一个集合的全部置位。
func (b BitSet) ContainsAll(other BitSet) bool {
	for i, word := range other.words {
		if i >= len(b.words) || b.words[i]&word != word {
			return false
		}
	}
	return true
}

// NextSet 返回不小于 start 的下一个置位索引。
func (b BitSet) NextSet(start int) (int, bool) {
	if start < 0 {
		start = 0
	}
	wordIndex := start >> 6
	if wordIndex >= len(b.words) {
		return 0, false
	}
	word := b.words[wordIndex] &^ ((uint64(1) << (start & 63)) - 1)
	for {
		if word != 0 {
			for bit := 0; bit < 64; bit++ {
				if word&(uint64(1)<<bit) != 0 {
					return wordIndex*64 + bit, true
				}
			}
		}
		wordIndex++
		if wordIndex >= len(b.words) {
			return 0, false
		}
		word = b.words[wordIndex]
	}
}

// LastSet 返回不大于 end 的最大置位索引。
func (b BitSet) LastSet(end int) (int, bool) {
	if end < 0 || len(b.words) == 0 {
		return 0, false
	}
	idx := end >> 6
	if idx >= len(b.words) {
		idx = len(b.words) - 1
		end = idx*64 + 63
	}
	mask := ^uint64(0)
	if end&63 < 63 {
		mask = (uint64(1) << uint((end&63)+1)) - 1
	}
	word := b.words[idx] & mask
	for idx >= 0 {
		if word != 0 {
			bit := 63
			for word&(uint64(1)<<uint(bit)) == 0 {
				bit--
			}
			return idx*64 + bit, true
		}
		idx--
		if idx < 0 {
			break
		}
		word = b.words[idx]
	}
	return 0, false
}

// SetRange 设置闭区间内的全部位。
func (b *BitSet) SetRange(from, to int) {
	if b == nil || from < 0 || to < from {
		return
	}
	b.Set(to)
	start, end := from>>6, to>>6
	for i := start; i <= end; i++ {
		mask := ^uint64(0)
		if i == start {
			mask &^= (uint64(1) << uint(from&63)) - 1
		}
		if i == end && to&63 < 63 {
			mask &= (uint64(1) << uint((to&63)+1)) - 1
		}
		b.words[i] |= mask
	}
}

// ClearRange 清除闭区间内的全部位。
func (b *BitSet) ClearRange(from, to int) {
	if b == nil || from < 0 || to < from {
		return
	}
	if len(b.words) == 0 {
		return
	}
	max := len(b.words)*64 - 1
	if from > max {
		return
	}
	if to > max {
		to = max
	}
	start, end := from>>6, to>>6
	for i := start; i <= end; i++ {
		mask := ^uint64(0)
		if i == start {
			mask &^= (uint64(1) << uint(from&63)) - 1
		}
		if i == end && to&63 < 63 {
			mask &= (uint64(1) << uint((to&63)+1)) - 1
		}
		b.words[i] &^= mask
	}
}

// All 判断集合是否所有已分配位均已设置。
func (b BitSet) All() bool {
	if len(b.words) == 0 {
		return false
	}
	for _, word := range b.words {
		if word != ^uint64(0) {
			return false
		}
	}
	return true
}
func (b BitSet) Len() int        { return len(b.words) * 64 }
func (b BitSet) Empty() bool     { return !b.Any() }
func (b BitSet) Words() []uint64 { return append([]uint64(nil), b.words...) }

// Equal 判断两个位集合的逻辑内容是否一致，忽略尾部零容量差异。
func (b BitSet) Equal(other BitSet) bool {
	limit := len(b.words)
	if len(other.words) > limit {
		limit = len(other.words)
	}
	for i := 0; i < limit; i++ {
		var left, right uint64
		if i < len(b.words) {
			left = b.words[i]
		}
		if i < len(other.words) {
			right = other.words[i]
		}
		if left != right {
			return false
		}
	}
	return true
}

// Not 返回按当前容量取反的独立副本。
func (b BitSet) Not() BitSet {
	out := b.Clone()
	for i := range out.words {
		out.words[i] = ^out.words[i]
	}
	return out
}

// Resize 调整集合容量，缩容时丢弃越界位。
func (b *BitSet) Resize(bits int) {
	if b == nil || bits < 0 {
		return
	}
	words := (bits + 63) / 64
	if words < len(b.words) {
		b.words = b.words[:words]
	} else if words > len(b.words) {
		b.words = append(b.words, make([]uint64, words-len(b.words))...)
	}
	if bits&63 != 0 && len(b.words) > 0 {
		b.words[len(b.words)-1] &= (uint64(1) << uint(bits&63)) - 1
	}
}

// SetAll 设置当前容量内的全部位。
func (b *BitSet) SetAll() {
	if b == nil {
		return
	}
	for i := range b.words {
		b.words[i] = ^uint64(0)
	}
}

// Indices 返回升序置位索引快照。
func (b BitSet) Indices() []int {
	out := make([]int, 0, b.Count())
	for index, ok := b.NextSet(0); ok; index, ok = b.NextSet(index + 1) {
		out = append(out, index)
	}
	return out
}

// CharReach 表示 256 个字节的字符集合。
type CharReach struct{ bits [4]uint64 }

func (c CharReach) Clone() CharReach { return c }

// Add 加入一个字节。
func (c *CharReach) Add(ch byte) {
	if c != nil {
		c.bits[ch>>6] |= uint64(1) << (ch & 63)
	}
}

// AddRange 加入闭区间内的全部字节。
func (c *CharReach) AddRange(lo, hi byte) {
	if c == nil || hi < lo {
		return
	}
	for ch := lo; ; ch++ {
		c.Add(ch)
		if ch == hi {
			break
		}
	}
}

// Remove 移除一个字节。
func (c *CharReach) Remove(ch byte) {
	if c != nil {
		c.bits[ch>>6] &^= uint64(1) << (ch & 63)
	}
}

// RemoveRange 移除闭区间内的全部字节。
func (c *CharReach) RemoveRange(lo, hi byte) {
	if c == nil || hi < lo {
		return
	}
	for ch := lo; ; ch++ {
		c.Remove(ch)
		if ch == hi {
			break
		}
	}
}

// Contains 判断集合是否包含字节。
func (c CharReach) Contains(ch byte) bool { return c.bits[ch>>6]&(uint64(1)<<(ch&63)) != 0 }

// Empty 判断集合是否为空。
func (c CharReach) Empty() bool { return c.bits == [4]uint64{} }

// Union 将 other 并入集合。
func (c *CharReach) Union(other CharReach) {
	if c == nil {
		return
	}
	for i := range c.bits {
		c.bits[i] |= other.bits[i]
	}
}

// Intersect 保留与 other 的交集。
func (c *CharReach) Intersect(other CharReach) {
	if c == nil {
		return
	}
	for i := range c.bits {
		c.bits[i] &= other.bits[i]
	}
}

// Difference 从集合中移除 other 中的字符。
func (c *CharReach) Difference(other CharReach) {
	if c == nil {
		return
	}
	for i := range c.bits {
		c.bits[i] &^= other.bits[i]
	}
}

// Count 返回集合大小。
func (c CharReach) Count() int {
	count := 0
	for _, word := range c.bits {
		for word != 0 {
			word &= word - 1
			count++
		}
	}
	return count
}
func (c CharReach) Complement() CharReach {
	for i := range c.bits {
		c.bits[i] = ^c.bits[i]
	}
	return c
}
func (c CharReach) Intersects(other CharReach) bool {
	for i := range c.bits {
		if c.bits[i]&other.bits[i] != 0 {
			return true
		}
	}
	return false
}

// ContainsAll 判断字符集合是否包含另一个集合。
func (c CharReach) ContainsAll(other CharReach) bool {
	for i := range c.bits {
		if c.bits[i]&other.bits[i] != other.bits[i] {
			return false
		}
	}
	return true
}

// First 返回集合中的最小字节。
func (c CharReach) First() (byte, bool) {
	for value := 0; value < 256; value++ {
		if c.Contains(byte(value)) {
			return byte(value), true
		}
	}
	return 0, false
}

// Last 返回集合中的最大字节。
func (c CharReach) Last() (byte, bool) {
	for value := 255; value >= 0; value-- {
		if c.Contains(byte(value)) {
			return byte(value), true
		}
	}
	return 0, false
}

// IsFull 判断是否覆盖全部字节。
func (c CharReach) IsFull() bool {
	return c.bits == [4]uint64{^uint64(0), ^uint64(0), ^uint64(0), ^uint64(0)}
}

// Bytes 返回集合中的全部字节。
func (c CharReach) Bytes() []byte {
	out := make([]byte, 0, c.Count())
	for value := 0; value < 256; value++ {
		if c.Contains(byte(value)) {
			out = append(out, byte(value))
		}
	}
	return out
}

// Range 返回集合中连续字节区间的最小覆盖范围。
func (c CharReach) Range() (lo, hi byte, ok bool) {
	lo, ok = c.First()
	if !ok {
		return 0, 0, false
	}
	hi, _ = c.Last()
	return lo, hi, true
}

// Ranges 返回集合的最小不相交闭区间表示。
func (c CharReach) Ranges() [][2]byte {
	out := make([][2]byte, 0)
	for value := 0; value < 256; {
		if !c.Contains(byte(value)) {
			value++
			continue
		}
		lo := value
		for value+1 < 256 && c.Contains(byte(value+1)) {
			value++
		}
		out = append(out, [2]byte{byte(lo), byte(value)})
		value++
	}
	return out
}

// Equal 判断两个字符集合是否完全一致。
func (c CharReach) Equal(other CharReach) bool { return c.bits == other.bits }

// IsSubset 判断当前字符集合是否为目标集合的子集。
func (c CharReach) IsSubset(other CharReach) bool { return other.ContainsAll(c) }
