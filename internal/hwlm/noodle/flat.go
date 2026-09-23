package noodle

import (
	"encoding/binary"
	"math/bits"

	"github.com/smartwalle/scankit/internal/hwlm"
	"github.com/smartwalle/scankit/internal/simd"
)

// flatHashPrime 是 64 位乘法散列使用的奇数常量。
const flatHashPrime = 0x9E3779B97F4A7C15

// flatEntry 保存一条候选文字在扁平索引中的定长比较视图。
// hi/mask 是主键之后前 8 字节的定长视图，hi2/mask2 是紧接其后的 4 字节视图。
// mask 中置位的位置表示该处存在有效字节；主键本身由槽位保存，条目里不重复。
type flatEntry struct {
	id     uint32
	length int32
	hi     uint64
	mask   uint64
	hi2    uint32
	mask2  uint32
}

// flatTable 以候选文字的前 keyBytes 字节建立开放寻址索引。
//
// 共享长前缀的文字集合在前缀树上必须逐节点解引用，命中密集时每次候选都有
// 若干次相互依赖的指针跳转；扁平索引把同样的判定压缩成一次主键读取、一次
// 散列和常数次尾部比较，候选确认成本与命中数线性相关且常数更小。
// 主键相同而尾部不同的文字进入同一个桶，桶内按掩码比较尾部字节。
//
// keyBytes 由 hwlm.FlatKeyWidth 选定：集合里最短文字不足 8 字节时退到 4 字节
// 主键，让长度 4~7 的文字也能走同一条确认路径；此时尾部最长 12 字节，需要
// 第二次定长读取补齐。
type flatTable struct {
	slots    []int32
	keys     []uint64
	buckets  [][]flatEntry
	shift    uint
	keyBytes int
}

// newFlatTable 构建扁平索引。size 取文字数量的 4 倍向上取整到 2 的幂，
// 让开放寻址的装载因子保持在 25% 以下，线性探测的期望长度接近 1。
func newFlatTable(literals []hwlm.Literal) *flatTable {
	keyBytes := hwlm.FlatKeyWidth(literals)
	if keyBytes == 0 {
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
		slots:    make([]int32, size),
		shift:    uint(64 - bits.Len(uint(size)) + 1),
		keyBytes: keyBytes,
	}
	for index := range literals {
		table.insert(literals[index])
	}
	return table
}

// keyAt 读取起点处的定长主键。调用方保证剩余字节不少于 keyBytes。
func (t *flatTable) keyAt(data []byte, off int) uint64 {
	if t.keyBytes >= hwlm.FlatKeyLong {
		return binary.LittleEndian.Uint64(data[off:])
	}
	return uint64(binary.LittleEndian.Uint32(data[off:]))
}

// insert 把一条文字放进它主键所属的桶。
func (t *flatTable) insert(literal hwlm.Literal) {
	key := t.keyAt(literal.Value, 0)
	slot := t.probe(key)
	entry := flatEntry{id: literal.ID, length: int32(len(literal.Value))}
	tail := literal.Value[t.keyBytes:]
	for index, value := range tail {
		if index < 8 {
			entry.hi |= uint64(value) << uint(8*index)
			entry.mask |= 0xFF << uint(8*index)
			continue
		}
		shift := uint(8 * (index - 8))
		entry.hi2 |= uint32(value) << shift
		entry.mask2 |= 0xFF << shift
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
// 窗口掩码已经保证窗口内的候选字节落在对应 lane 集合内，因此这里只需按位
// 迭代窗口掩码并对每个置位起点做一次定长比较。三段扫描（宽窗口、超向量
// 窗口、逐字节回退）与压缩前缀树路径完全一致，保证两条路径的候选集合
// 逐位相同。
func (t *flatTable) findInto(out []Match, data []byte, backend simd.Backend, tables *simd.ByteSetTables, first *[4]uint64, lanes int) []Match {
	off := 0
	for ; off+simd.WideWidth <= len(data); off += simd.WideWidth {
		mask, ok := backend.WindowMask64(data, off, tables, lanes)
		if !ok {
			break
		}
		for mask != 0 {
			from := off + bits.TrailingZeros64(mask)
			if remaining := len(data) - from; remaining >= t.keyBytes {
				if entries := t.lookup(t.keyAt(data, from)); len(entries) != 0 {
					out = t.appendEntries(out, data, from, remaining, entries)
				}
			}
			mask &= mask - 1
		}
	}
	if off+simd.SuperWidth <= len(data) {
		if mask, ok := backend.WindowMask(data, off, tables, lanes); ok {
			for mask != 0 {
				from := off + bits.TrailingZeros32(mask)
				if remaining := len(data) - from; remaining >= t.keyBytes {
					if entries := t.lookup(t.keyAt(data, from)); len(entries) != 0 {
						out = t.appendEntries(out, data, from, remaining, entries)
					}
				}
				mask &= mask - 1
			}
			off += simd.SuperWidth
		}
	}
	for ; off < len(data); off++ {
		value := data[off]
		folded := foldASCII(value)
		if first[value>>6]&(uint64(1)<<(value&63)) != 0 || first[folded>>6]&(uint64(1)<<(folded&63)) != 0 {
			if remaining := len(data) - off; remaining >= t.keyBytes {
				if entries := t.lookup(t.keyAt(data, off)); len(entries) != 0 {
					out = t.appendEntries(out, data, off, remaining, entries)
				}
			}
		}
	}
	return out
}

// appendMatches 把 from 起点上命中的候选文字追加到 out。
//
// 窗口剩余字节足够容纳最长文字时走定长比较：两次定长读取覆盖主键之后的全部
// 尾部，桶内每条文字只需一次掩码比较。缓冲区末尾不足 hwlm.FlatMaxLiteral 时退回
// 逐字节比较，保证与压缩前缀树完全一致的候选集合。
func (t *flatTable) appendMatches(out []Match, data []byte, from int) []Match {
	remaining := len(data) - from
	if remaining < t.keyBytes {
		return out
	}
	entries := t.lookup(t.keyAt(data, from))
	if len(entries) == 0 {
		return out
	}
	return t.appendEntries(out, data, from, remaining, entries)
}

// appendEntries 在已确认主键命中的桶内做尾部比较。查找失败的位置在调用方
// 直接跳过，只有真正进入桶的候选才会走到这里，因此这一层保持独立函数，
// 让热循环里的未命中路径不承担桶内比较的分支与代码体积。
func (t *flatTable) appendEntries(out []Match, data []byte, from, remaining int, entries []flatEntry) []Match {
	if remaining >= hwlm.FlatMaxLiteral {
		hi := binary.LittleEndian.Uint64(data[from+t.keyBytes:])
		var hi2 uint32
		if t.keyBytes < hwlm.FlatKeyLong {
			hi2 = binary.LittleEndian.Uint32(data[from+t.keyBytes+8:])
		}
		for index := range entries {
			entry := &entries[index]
			if hi&entry.mask != entry.hi {
				continue
			}
			if entry.mask2 != 0 && hi2&entry.mask2 != entry.hi2 {
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
		tail := data[from+t.keyBytes : from+length]
		matched := true
		for shift, value := range tail {
			if t.tailByte(entry, shift) != value {
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

// tailByte 返回主键之后第 index 个尾部字节。
func (t *flatTable) tailByte(entry *flatEntry, index int) byte {
	if index < 8 {
		return byte(entry.hi >> uint(8*index))
	}
	return byte(entry.hi2 >> uint(8*(index-8)))
}
