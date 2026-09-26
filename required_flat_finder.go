package scankit

// 本文件为「必须文字索引」中长度不小于 2 的候选文字提供统一的专用匹配器。
//
// 背景（见 docs/technical-solutions/scankit-block-mode/问题与排查计划.md §54）：
// 候选文字此前按长度拆成「短（2~3 字节）」「长（不小于 4 字节）」「忽略大小写」
// 三组，分别交给 requiredShortFinder 与通用文字匹配器（Teddy/FDR/Noodle）。
// 三组各自扫描一遍整个输入，光是窗口掩码就要付三次遍历；长组一旦含有超过扁平
// 索引长度的文字（如 26 字节），整个长组还会被 hwlm.Select 拖回 Teddy 的首字节
// 分桶逐条确认路径，每次触发都要复制 40 字节的 Literal 再调用 hwlm.ContainsAt。
//
// 统一匹配器把「前两字节」作为入口键：两级小表直接给出候选编号（2 字节文字）、
// 第三级表（3 字节文字）或需要完整比较的文字下标列表。判定只需要「首字节查表、
// 次字节查表、（必要时）完整比较」，没有桶结构与 40 字节结构体复制；一次宽窗口
// 掩码扫描即可覆盖全部长度不小于 2 的候选文字。

import (
	"math/bits"

	"github.com/smartwalle/scankit/internal/dispatch"
	"github.com/smartwalle/scankit/internal/hwlm"
	"github.com/smartwalle/scankit/internal/simd"
)

// requiredFlatEntry 是一个「首字节 + 次字节」前缀对应的候选文字集合。
//
// 同一前缀可能终结一条 2 字节文字（literal）、继续匹配若干条 3 字节文字
// （tail 指向第三级表），并继续匹配若干条更长的文字（longs 保存下标，逐一
// 完整比较）。三者可以同时成立。
type requiredFlatEntry struct {
	literal uint32
	tail    int32
	longs   []int32
}

// requiredFlatFinder 是长度不小于 2 的候选文字专用匹配器。
//
// first 把首字节映射到第二级表下标（-1 表示该字节不能开启任何候选），second 把
// 次字节映射到 requiredFlatEntry 下标（0 表示该前缀不存在），third 把第三字节
// 映射到 3 字节文字编号。三张表的规模都只有几百字节，判定过程不触碰通用自动机。
//
// tables 保存首字节集合。扫描用单 lane 宽窗口掩码枚举「首字节属于集合」的位置，
// 再由两级表判定前缀。曾试过把次字节集合作为第二个 lane 组成两字节掩码，实测在
// 64 KiB 固定语料上把掩码命中位置从 5,344 降到 4,008，但每个字节多一次 lane 查表
// 的代价更大：finder 从 35 µs 涨到 39 µs，已回退（见问题与排查计划 §55）。
type requiredFlatFinder struct {
	values  []hwlm.Literal
	first   [256]int16
	second  [][256]uint32
	entries []requiredFlatEntry
	third   [][256]uint32
	tables  simd.ByteSetTables
}

// flatOtherCase 返回字节在 ASCII 大小写折叠下的另一形态；无对应形态时返回自身。
func flatOtherCase(value byte) byte {
	if value >= 'A' && value <= 'Z' {
		return value + 'a' - 'A'
	}
	if value >= 'a' && value <= 'z' {
		return value - 'a' + 'A'
	}
	return value
}

// newRequiredFlatFinder 为长度不小于 2 的候选文字构建统一匹配器。
//
// 传入集合不含长度不小于 2 的文字时返回 nil，调用方应回退到通用匹配器。
// 忽略大小写的文字一律进入 longs 走完整比较：入口表按精确字节建表，2 字节
// 文字虽然可以把四种大小写组合逐个登记，但 3 字节文字的第三级表按单字节建表，
// 混用精确编号与折叠语义会误报，因此统一按「表只负责收窄、比较负责判定」处理。
func newRequiredFlatFinder(literals []hwlm.Literal) *requiredFlatFinder {
	finder := &requiredFlatFinder{entries: []requiredFlatEntry{{tail: -1}}}
	for index := range finder.first {
		finder.first[index] = -1
	}
	var firstSet [4]uint64
	distinct := 0
	addPair := func(first, second byte) int32 {
		index := finder.first[first]
		if index < 0 {
			index = int16(len(finder.second))
			finder.first[first] = index
			finder.second = append(finder.second, [256]uint32{})
			firstSet[first>>6] |= uint64(1) << uint(first&63)
			distinct++
		}
		entry := finder.second[index][second]
		if entry == 0 {
			finder.entries = append(finder.entries, requiredFlatEntry{tail: -1})
			entry = uint32(len(finder.entries) - 1)
			finder.second[index][second] = entry
		}
		return int32(entry)
	}
	var pairs [4][2]byte
	for index := range literals {
		literal := literals[index]
		value := literal.Value
		if len(value) < 2 {
			continue
		}
		valueIndex := int32(len(finder.values))
		finder.values = append(finder.values, literal)
		count := 1
		pairs[0] = [2]byte{value[0], value[1]}
		if literal.CaseInsensitive {
			firsts := [2]byte{value[0], flatOtherCase(value[0])}
			seconds := [2]byte{value[1], flatOtherCase(value[1])}
			count = 0
			for first := 0; first < 2; first++ {
				if first == 1 && firsts[1] == firsts[0] {
					continue
				}
				for second := 0; second < 2; second++ {
					if second == 1 && seconds[1] == seconds[0] {
						continue
					}
					pairs[count] = [2]byte{firsts[first], seconds[second]}
					count++
				}
			}
		}
		for pair := 0; pair < count; pair++ {
			entryIndex := addPair(pairs[pair][0], pairs[pair][1])
			entry := &finder.entries[entryIndex]
			switch {
			case literal.CaseInsensitive || len(value) > 3:
				finder.appendLong(entry, valueIndex)
			case len(value) == 2:
				if entry.literal == 0 {
					entry.literal = literal.ID
				} else if entry.literal != literal.ID {
					finder.appendLong(entry, valueIndex)
				}
			default:
				if entry.tail < 0 {
					entry.tail = int32(len(finder.third))
					finder.third = append(finder.third, [256]uint32{})
				}
				id := finder.third[entry.tail][value[2]]
				if id == 0 {
					finder.third[entry.tail][value[2]] = literal.ID
				} else if id != literal.ID {
					finder.appendLong(entry, valueIndex)
				}
			}
		}
	}
	if distinct == 0 || distinct > flatFinderMaxFirstBytes || len(finder.values) > flatFinderMaxLiterals {
		return nil
	}
	for index := range finder.entries {
		if len(finder.entries[index].longs) > flatFinderMaxLongs {
			return nil
		}
	}
	finder.tables = simd.NewByteSetTables([4][4]uint64{firstSet})
	return finder
}

// appendLong 把一个需要完整比较的文字下标加入前缀条目，重复登记直接忽略。
func (f *requiredFlatFinder) appendLong(entry *requiredFlatEntry, index int32) {
	for _, existing := range entry.longs {
		if existing == index {
			return
		}
	}
	entry.longs = append(entry.longs, index)
}

// find 扫描 data 并把命中的候选文字追加到 dst。
//
// 候选位置由首字节集合的宽窗口掩码枚举，掩码为空时整窗跳过；掩码命中的位置再用
// 两级小表得到前缀条目，2/3 字节文字直接给出编号，更长的文字才付出一次完整比较。
// 窗口覆盖不到的数据尾部逐字节回退，保证末尾候选不会漏报。
func (f *requiredFlatFinder) find(data []byte, dst []requiredHit) []requiredHit {
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
			bit := bits.TrailingZeros64(mask)
			mask &^= 1 << uint(bit)
			position := offset + bit
			index := f.first[data[position]]
			if index < 0 {
				continue
			}
			entryIndex := f.second[index][data[position+1]]
			if entryIndex == 0 {
				continue
			}
			entry := &f.entries[entryIndex]
			if entry.literal != 0 {
				dst = append(dst, requiredHit{slot: int(entry.literal) - 1, pos: position})
			}
			if entry.tail >= 0 && position+2 < len(data) {
				if id := f.third[entry.tail][data[position+2]]; id != 0 {
					dst = append(dst, requiredHit{slot: int(id) - 1, pos: position})
				}
			}
			if len(entry.longs) > 0 {
				dst = f.appendLongs(data, position, entry, dst)
			}
		}
	}
	for ; offset < limit; offset++ {
		index := f.first[data[offset]]
		if index < 0 {
			continue
		}
		entryIndex := f.second[index][data[offset+1]]
		if entryIndex == 0 {
			continue
		}
		entry := &f.entries[entryIndex]
		if entry.literal != 0 {
			dst = append(dst, requiredHit{slot: int(entry.literal) - 1, pos: offset})
		}
		if entry.tail >= 0 && offset+2 < len(data) {
			if id := f.third[entry.tail][data[offset+2]]; id != 0 {
				dst = append(dst, requiredHit{slot: int(id) - 1, pos: offset})
			}
		}
		if len(entry.longs) > 0 {
			dst = f.appendLongs(data, offset, entry, dst)
		}
	}
	return dst
}

// appendLongs 对共享同一两字节前缀的长文字逐一做完整比较。
//
// 只有前缀条目真的挂有长文字时才会进入这里，因此该调用不影响 2~3 字节文字的
// 逐候选常数，热路径本体保持可内联。
func (f *requiredFlatFinder) appendLongs(data []byte, position int, entry *requiredFlatEntry, dst []requiredHit) []requiredHit {
	for _, index := range entry.longs {
		literal := &f.values[index]
		if hwlm.ContainsAt(data, position, *literal) {
			dst = append(dst, requiredHit{slot: int(literal.ID) - 1, pos: position})
		}
	}
	return dst
}

// 统一匹配器的启用门槛。
//
// 两字节前缀表的逐候选常数由「同一个两字节前缀下需要完整比较的文字条数」决定：
// 条数很少时逐候选只是一次定长比较，明显小于 Teddy 的首字节分桶确认；条数很多
// 时（例如 100 条共享 "field" 前缀的字段名规则）逐候选要线性扫过整串文字，
// 压缩前缀树分摊共享前缀的优势反而更大。首字节集合过宽时单 lane 掩码在自然文本
// 上几乎不跳位置，同等条目下分组的通用匹配器能用多 lane 掩码跳得更远。
const (
	flatFinderMaxFirstBytes = 24
	flatFinderMaxLiterals   = 32
	flatFinderMaxLongs      = 4
)
