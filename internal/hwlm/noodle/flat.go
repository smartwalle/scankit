package noodle

import (
	"encoding/binary"
	"math/bits"

	"github.com/smartwalle/scankit/internal/hwlm"
	"github.com/smartwalle/scankit/internal/simd"
)

// flatKeyBytes 是扁平索引的主键长度：所有候选文字都至少包含这么多字节，
// 主键可以直接从窗口读出而不需要任何长度判断。
const flatKeyBytes = 8

// flatMaxLiteral 是扁平索引支持的文字长度上限，超出的文字回退前缀树。
// 上限取 16 让尾部比较可以用一次定长读取完成。
const flatMaxLiteral = 16

// flatHashPrime 是 64 位乘法散列使用的奇数常量。
const flatHashPrime = 0x9E3779B97F4A7C15

// flatEntry 保存一条候选文字在扁平索引中的定长比较视图。
// lo 是前 8 字节，hi/mask 是第 8 字节之后的有效尾部。
type flatEntry struct {
	id     uint32
	length int32
	lo     uint64
	hi     uint64
	mask   uint64
}

// flatTable 以候选文字的前 8 字节建立开放寻址索引。
//
// 共享长前缀的文字集合在前缀树上必须逐节点解引用，命中密集时每次候选都有
// 若干次相互依赖的指针跳转；扁平索引把同样的判定压缩成一次主键读取、一次
// 散列和常数次尾部比较，候选确认成本与命中数线性相关且常数更小。
// 主键相同而尾部不同的文字进入同一个桶，桶内按掩码比较尾部字节。
type flatTable struct {
	slots   []int32
	keys    []uint64
	buckets [][]flatEntry
	shift   uint
}

// flatEligible 判断文字集合是否可以使用扁平索引。大小写不敏感的文字需要
// 逐字节折叠，长度不足主键宽度或超出尾部上限的文字也仍然交给前缀树。
func flatEligible(literals []hwlm.Literal) bool {
	if len(literals) == 0 {
		return false
	}
	for index := range literals {
		literal := &literals[index]
		if literal.CaseInsensitive {
			return false
		}
		if len(literal.Value) < flatKeyBytes || len(literal.Value) > flatMaxLiteral {
			return false
		}
	}
	return true
}

// newFlatTable 构建扁平索引。size 取文字数量的 4 倍向上取整到 2 的幂，
// 让开放寻址的装载因子保持在 25% 以下，线性探测的期望长度接近 1。
func newFlatTable(literals []hwlm.Literal) *flatTable {
	if !flatEligible(literals) {
		return nil
	}
	size := 1
	for size < len(literals)*4 {
		size <<= 1
	}
	if size < 64 {
		size = 64
	}
	table := &flatTable{
		slots: make([]int32, size),
		shift: uint(64 - bits.Len(uint(size)) + 1),
	}
	for index := range literals {
		table.insert(literals[index])
	}
	return table
}

// insert 把一条文字放进它主键所属的桶。
func (t *flatTable) insert(literal hwlm.Literal) {
	key := binary.LittleEndian.Uint64(literal.Value[:flatKeyBytes])
	slot := t.probe(key)
	entry := flatEntry{id: literal.ID, length: int32(len(literal.Value)), lo: key}
	tail := literal.Value[flatKeyBytes:]
	for index, value := range tail {
		shift := uint(8 * index)
		entry.hi |= uint64(value) << shift
		entry.mask |= uint64(0xFF) << shift
	}
	t.buckets[slot-1] = append(t.buckets[slot-1], entry)
}

// probe 返回主键所在的桶编号（从 1 开始），必要时新建空桶。
func (t *flatTable) probe(key uint64) int32 {
	mask := uint64(len(t.slots) - 1)
	index := (key * flatHashPrime) >> t.shift
	for {
		slot := t.slots[index]
		if slot == 0 {
			t.slots[index] = int32(len(t.keys) + 1)
			t.keys = append(t.keys, key)
			t.buckets = append(t.buckets, nil)
			return t.slots[index]
		}
		if t.keys[slot-1] == key {
			return slot
		}
		index = (index + 1) & mask
	}
}

// lookup 返回主键对应的桶，不存在时返回 nil。热路径保留同一份探测逻辑，
// 由 bucket 作为等价入口供测试与诊断使用。
func (t *flatTable) lookup(key uint64) []flatEntry {
	index := (key * flatHashPrime) >> t.shift
	for {
		slot := t.slots[index]
		if slot == 0 {
			return nil
		}
		if t.keys[slot-1] == key {
			return t.buckets[slot-1]
		}
		index = (index + 1) & (uint64(len(t.slots)) - 1)
	}
}

// bucket 返回主键对应的桶，不存在时返回 nil。
func (t *flatTable) bucket(key uint64) []flatEntry {
	return t.lookup(key)
}

// findInto 用扁平索引扫描整块数据，返回未排序、未去重的候选命中。
//
// 窗口掩码已经保证窗口内的候选字节落在首 lane 集合内，因此这里只需按位
// 迭代窗口掩码并对每个置位起点做一次定长比较。三段扫描（宽窗口、超向量
// 窗口、逐字节回退）与压缩前缀树路径完全一致，保证两条路径的候选集合
// 逐位相同。
func (t *flatTable) findInto(out []Match, data []byte, backend simd.Backend, tables *simd.ByteSetTables, first *[4]uint64) []Match {
	off := 0
	for ; off+simd.WideWidth <= len(data); off += simd.WideWidth {
		mask, ok := backend.WindowMask64(data, off, tables, 1)
		if !ok {
			break
		}
		for mask != 0 {
			out = t.appendMatches(out, data, off+bits.TrailingZeros64(mask))
			mask &= mask - 1
		}
	}
	if off+simd.SuperWidth <= len(data) {
		if mask, ok := backend.WindowMask(data, off, tables, 1); ok {
			for mask != 0 {
				out = t.appendMatches(out, data, off+bits.TrailingZeros32(mask))
				mask &= mask - 1
			}
			off += simd.SuperWidth
		}
	}
	for ; off < len(data); off++ {
		value := data[off]
		folded := foldASCII(value)
		if first[value>>6]&(uint64(1)<<(value&63)) != 0 || first[folded>>6]&(uint64(1)<<(folded&63)) != 0 {
			out = t.appendMatches(out, data, off)
		}
	}
	return out
}

// appendMatches 把 from 起点上命中的候选文字追加到 out。
//
// 窗口剩余字节足够容纳最长文字时走定长比较：一次 8 字节主键读取、一次
// 8 字节尾部读取，桶内每条文字只需一次掩码比较。缓冲区末尾不足 16 字节
// 时退回逐字节比较，保证与压缩前缀树完全一致的候选集合。
func (t *flatTable) appendMatches(out []Match, data []byte, from int) []Match {
	remaining := len(data) - from
	if remaining < flatKeyBytes {
		return out
	}
	key := binary.LittleEndian.Uint64(data[from:])
	entries := t.lookup(key)
	if len(entries) == 0 {
		return out
	}
	if remaining >= flatMaxLiteral {
		hi := binary.LittleEndian.Uint64(data[from+flatKeyBytes:])
		for index := range entries {
			entry := &entries[index]
			if hi&entry.mask != entry.hi {
				continue
			}
			out = append(out, Match{ID: entry.id, From: from, To: from + int(entry.length)})
		}
		return out
	}
	for index := range entries {
		entry := &entries[index]
		length := int(entry.length)
		if length > remaining {
			continue
		}
		tail := data[from+flatKeyBytes : from+length]
		matched := true
		for shift, value := range tail {
			if entry.hi>>uint(8*shift)&0xFF != uint64(value) {
				matched = false
				break
			}
		}
		if matched {
			out = append(out, Match{ID: entry.id, From: from, To: from + length})
		}
	}
	return out
}
