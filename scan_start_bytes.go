package scankit

import (
	"math/bits"

	"github.com/smartwalle/scankit/internal/parser"
)

// startByteIndex 把需要逐起点确认的规则按"可能的首字节"分组，扫描时用一次
// 查表替换"每个起点 × 每条规则"的遍历。mask[value] 的第 slot 位表示
// rules[slot] 的首字节可能是 value；首字节无法静态限定的规则进入全部 256 组，
// 因此过滤只会排除确定不可能匹配的规则。
type startByteIndex struct {
	words []uint64
	rules []int
	width int
}

// newStartByteIndex 为给定规则下标集合建立首字节索引；集合为空时返回 nil。
func newStartByteIndex(rules []compiledRule, indexes []int) *startByteIndex {
	if len(indexes) == 0 {
		return nil
	}
	width := (len(indexes) + 63) / 64
	index := &startByteIndex{
		words: make([]uint64, (startByteEndRow+1)*width),
		rules: append([]int(nil), indexes...),
		width: width,
	}
	for slot, ruleIndex := range indexes {
		byteSet := startByteSet(rules[ruleIndex])
		bit := uint64(1) << (uint(slot) & 63)
		word := slot >> 6
		for _, value := range byteSet {
			index.words[int(value)*width+word] |= bit
		}
		if rules[ruleIndex].root != nil && parser.Nullable(rules[ruleIndex].root) {
			index.words[startByteEndRow*width+word] |= bit
		}
	}
	return index
}

// startByteSet 返回规则首字节的候选集合；返回 256 个字节表示无法限定。
func startByteSet(rule compiledRule) []byte {
	full := func() []byte {
		all := make([]byte, 256)
		for i := range all {
			all[i] = byte(i)
		}
		return all
	}
	if rule.root == nil {
		return full()
	}
	// 大小写折叠、UTF-8 语义、编辑距离与状态化结构都会让首字节偏离语法树
	// 直接推导的结果，这里一律不做过滤。
	if rule.flags&(CompileCaseless|CompileUTF8|CompileUCP) != 0 {
		return full()
	}
	if rule.ext != nil && rule.ext.Flags&(ExtFlagEditDistance|ExtFlagHammingDistance) != 0 {
		return full()
	}
	if parser.RequiresStatefulRuntime(rule.root) {
		return full()
	}
	bytes := parser.FirstBytes(rule.root)
	if len(bytes) == 0 {
		return full()
	}
	return bytes
}

// startByteEndRow 是数据末尾起点的分组编号：只有可能匹配空串的规则需要在该
// 起点尝试，其它规则无论首字节如何都不可能在末尾产生零长度匹配。
const startByteEndRow = 256

// rulesAt 返回 value 对应分组中的规则位图；startByteEndRow 表示数据末尾起点。
func (index *startByteIndex) rulesAt(value int) []uint64 {
	base := value * index.width
	return index.words[base : base+index.width]
}

// groupOf 返回起点对应的分组编号。
func (index *startByteIndex) groupOf(data []byte, start int) int {
	if start >= len(data) {
		return startByteEndRow
	}
	return int(data[start])
}

// confirmIndexed 使用首字节索引逐起点确认，起点顺序与纯逐起点确认一致。
func (st *blockScanState) confirmIndexed(index *startByteIndex) {
	if index == nil {
		return
	}
	width := index.width
	for start := 0; start <= len(st.data); start++ {
		row := index.rulesAt(index.groupOf(st.data, start))
		for word := range width {
			remaining := row[word]
			for remaining != 0 {
				slot := word*64 + bits.TrailingZeros64(remaining)
				remaining &= remaining - 1
				if !st.verify(index.rules[slot], start) {
					return
				}
			}
		}
		if st.ctx.Reports.Stopped() {
			return
		}
	}
}

// confirmCandidatesAndFallback 交错执行候选起点确认与首字节过滤后的逐起点确认。
// starts 已按 (起点, 规则下标) 升序，索引内规则也按规则下标升序，两者合并后
// 输出顺序与纯逐起点确认一致。
func (st *blockScanState) confirmCandidatesAndFallback(starts []uint64, index *startByteIndex) {
	if index == nil {
		// 候选已按 (起点, 规则) 升序。被重叠抑制或已终止的规则在这里直接
		// 跳过，避免为必然返回的候选进入通用确认例程。
		simple := st.scanner.simpleReports
		for _, key := range starts {
			start := int(key >> 32)
			rule := int(uint32(key))
			if start < st.blockedUntil[rule] || st.fired[rule] {
				continue
			}
			if !simple && st.ctx.Reports.Stopped() {
				return
			}
			if !st.verify(rule, start) {
				return
			}
		}
		return
	}
	width := index.width
	cursor := 0
	for start := 0; start <= len(st.data); start++ {
		row := index.rulesAt(index.groupOf(st.data, start))
		slot, remaining := 0, row[0]
		rule, hasRule := -1, false
		for {
			if !hasRule {
				for {
					if remaining == 0 {
						slot++
						if slot >= width {
							break
						}
						remaining = row[slot]
						continue
					}
					rule = index.rules[slot*64+bits.TrailingZeros64(remaining)]
					remaining &= remaining - 1
					hasRule = true
					break
				}
			}
			candidate := -1
			if cursor < len(starts) {
				if candidateStart, candidateRule := requiredStartParts(starts[cursor]); candidateStart == start {
					candidate = candidateRule
				}
			}
			if !hasRule && candidate < 0 {
				break
			}
			if candidate >= 0 && (!hasRule || candidate < rule) {
				if !st.verify(candidate, start) {
					return
				}
				cursor++
				continue
			}
			if !st.verify(rule, start) {
				return
			}
			hasRule = false
		}
		if st.ctx.Reports.Stopped() {
			return
		}
	}
}
