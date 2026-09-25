package scankit

// 本文件为「必须文字索引」中长度 2~3 的候选文字提供专用匹配器。
//
// 背景（见 docs/technical-solutions/scankit-block-mode/问题与排查计划.md §33.8/§36）：
// 必须文字索引按文字长度分组后，2~3 字节一组此前交给通用文字匹配器（FDR）。
// 通用匹配器必须支持任意长度，因此每条候选都要走 Aho-Corasick 的 256 项转移表，
// 再遍历命中状态的 output 切片。对定长 2~3 字节文字来说这两步都是纯开销：
// 判定只需要「首字节相同、次字节相同、（3 字节时）第三字节相同」，用两级小表
// 就能直接得出候选编号。

import (
	"math/bits"

	"github.com/smartwalle/scankit/internal/dispatch"
	"github.com/smartwalle/scankit/internal/hwlm"
	"github.com/smartwalle/scankit/internal/simd"
)

// requiredShortEntry 是一个「首字节 + 次字节」前缀对应的候选文字集合。
//
// 同一个前缀可能同时终结一条 2 字节文字，并继续匹配若干条 3 字节文字，
// 因此两者分开保存：literal 保存 2 字节文字的编号（0 表示不存在），tail 保存
// 3 字节文字第三级表的下标（负数表示不存在）。第一级表按首字节（至多 256 项）
// 索引，因此用 int16 保存第二级下标不会溢出。
type requiredShortEntry struct {
	literal uint32
	tail    int32
}

// requiredShortFinder 是长度 2~3 的候选文字专用匹配器。
//
// first 把首字节映射到第二级表下标（-1 表示该字节不能开启任何候选），second 把
// 次字节映射到 requiredShortEntry 下标（0 表示该前缀不存在），third 把第三字节
// 映射到 3 字节文字编号。三张表的规模都只有几百字节，判定过程不触碰通用自动机。
type requiredShortFinder struct {
	first   [256]int16
	second  [][256]uint32
	entries []requiredShortEntry
	third   [][256]uint32
	tables  simd.ByteSetTables
}

// newRequiredShortFinder 为长度恰好 2~3 的候选文字构建专用匹配器。
//
// 传入集合不含 2~3 字节文字时返回 nil，调用方应回退到通用匹配器。调用方
// （splitRequiredLiterals）保证 medium 分组的长度恰好落在 2~3，这里的长度
// 判定只是让函数在没有可用文字时安全退化。
func newRequiredShortFinder(literals []hwlm.Literal) func([]byte, []requiredHit) []requiredHit {
	finder := &requiredShortFinder{entries: []requiredShortEntry{{tail: -1}}}
	for index := range finder.first {
		finder.first[index] = -1
	}
	var firstSet [4]uint64
	for _, literal := range literals {
		value := literal.Value
		if len(value) < 2 || len(value) > 3 {
			continue
		}
		second := finder.first[value[0]]
		if second < 0 {
			second = int16(len(finder.second))
			finder.first[value[0]] = second
			finder.second = append(finder.second, [256]uint32{})
			firstSet[value[0]>>6] |= uint64(1) << uint(value[0]&63)
		}
		slot := finder.second[second][value[1]]
		if slot == 0 {
			finder.entries = append(finder.entries, requiredShortEntry{tail: -1})
			slot = uint32(len(finder.entries) - 1)
			finder.second[second][value[1]] = slot
		}
		entry := &finder.entries[slot]
		if len(value) == 2 {
			if entry.literal == 0 {
				entry.literal = literal.ID
			}
			continue
		}
		if entry.tail < 0 {
			entry.tail = int32(len(finder.third))
			finder.third = append(finder.third, [256]uint32{})
		}
		if finder.third[entry.tail][value[2]] == 0 {
			finder.third[entry.tail][value[2]] = literal.ID
		}
	}
	if len(finder.entries) == 1 {
		return nil
	}
	// 窗口掩码只用首字节集合这一个 lane。次字节同样可以做成第二个 lane，让掩码
	// 阶段就滤掉「首字节命中但次字节不可能是任何文字」的位置，但实测该做法每个
	// 窗口都要多付一次原生掩码调用，在首字节稀疏的集合（如 BankCard 的 "62x"）
	// 上得不偿失：同一语料上多 lane 让 BankCard/CreditCard 三档分别退化 8%~12%。
	// 次字节判定因此留在候选循环里的两级查表内，只有掩码真正命中的位置才付出
	// 一次查表，成本随候选数线性增长而不是随窗口数增长。
	finder.tables = simd.NewByteSetTables([4][4]uint64{firstSet})
	return finder.find
}

// find 扫描 data 并把命中的候选文字追加到 dst。
//
// 候选位置由首字节集合的宽窗口掩码枚举，掩码为空时整窗跳过；候选处再用两级小表
// 判定文字编号。窗口覆盖不到的数据尾部逐字节回退，保证末尾候选不会漏报。
func (f *requiredShortFinder) find(data []byte, dst []requiredHit) []requiredHit {
	backend := dispatch.DefaultBackend()
	// 文字长度至少 2，末尾不足一个后继字节的位置不可能命中，扫描上界因此收紧一格。
	limit := len(data) - 1
	offset := 0
	for ; offset+simd.WideWidth <= limit; offset += simd.WideWidth {
		mask, ok := backend.WindowMask64(data, offset, &f.tables, 1)
		if !ok {
			break
		}
		for mask != 0 {
			position := offset + bits.TrailingZeros64(mask)
			mask &= mask - 1
			index := f.first[data[position]]
			if index < 0 {
				continue
			}
			if entry := f.second[index][data[position+1]]; entry != 0 {
				dst = f.appendAt(data, position, entry, dst)
			}
		}
	}
	for ; offset < limit; offset++ {
		index := f.first[data[offset]]
		if index < 0 {
			continue
		}
		if entry := f.second[index][data[offset+1]]; entry != 0 {
			dst = f.appendAt(data, offset, entry, dst)
		}
	}
	return dst
}

// appendAt 把一个「首字节 + 次字节」前缀对应的全部文字追加到 dst。
//
// 只有前缀真正命中候选文字时才被调用（命中相对候选位置稀疏），因此单独成函数，
// 让扫描循环本体保持足够小以便内联。同一前缀既可能是 2 字节文字，也可能继续
// 延伸到 3 字节文字，两者都要产出。
func (f *requiredShortFinder) appendAt(data []byte, position int, entry uint32, dst []requiredHit) []requiredHit {
	slot := &f.entries[entry]
	if slot.literal != 0 {
		dst = append(dst, requiredHit{slot: int(slot.literal) - 1, pos: position})
	}
	if slot.tail >= 0 && position+2 < len(data) {
		if id := f.third[slot.tail][data[position+2]]; id != 0 {
			dst = append(dst, requiredHit{slot: int(id) - 1, pos: position})
		}
	}
	return dst
}
