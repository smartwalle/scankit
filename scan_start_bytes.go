package scankit

import (
	"math/bits"
	"sort"

	"github.com/smartwalle/scankit/internal/dispatch"
	"github.com/smartwalle/scankit/internal/parser"
	"github.com/smartwalle/scankit/internal/simd"
)

// startByteIndex 把需要逐起点确认的规则按"可能的首字节"分组，扫描时用一次
// 查表替换"每个起点 × 每条规则"的遍历。mask[value] 的第 slot 位表示
// rules[slot] 的首字节可能是 value；首字节无法静态限定的规则进入全部 256 组，
// 因此过滤只会排除确定不可能匹配的规则。
type startByteIndex struct {
	words []uint64
	rules []int
	width int

	// union 是全部规则首字节集合的并集：窗口枚举据此按 64 字节窗口批量跳过
	// 必然无候选的区间，而不是逐字节查表判定要不要确认。unionCount 是并集的
	// 势，unionFull 表示并集覆盖全部字节。
	union      [4]uint64
	unionCount int
	unionFull  bool
	tables     simd.ByteSetTables
	// windowed 表示本轮索引值得走窗口枚举：并集既不满、又足够稀疏，且后端具备
	// 原生宽窗口掩码内核。否则逐字节循环反而更省（见
	// guardRunCandidates 对原生内核的同类判定）。
	windowed bool
	// endRowNonEmpty 表示有规则可匹配空串，需要在数据末尾起点额外确认。
	endRowNonEmpty bool
	// backend 是构造期选定的向量后端，热路径复用避免每次扫描重新探测。
	backend simd.Backend
}

// startByteUnionLimit 是改用窗口枚举的首字节并集规模上限。并集越稀疏，窗口枚举
// 跳过的整段无候选区间越多；并集接近全量时窗口掩码几乎全置位，逐字节循环更省。
const startByteUnionLimit = 128

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
			index.union[value>>6] |= uint64(1) << uint(value&63)
		}
		if rules[ruleIndex].root != nil && parser.Nullable(rules[ruleIndex].root) {
			index.words[startByteEndRow*width+word] |= bit
			index.endRowNonEmpty = true
		}
	}
	for _, word := range index.union {
		index.unionCount += bits.OnesCount64(word)
	}
	index.unionFull = index.unionCount == 256
	index.tables = simd.FirstByteTables(index.union)
	index.backend = dispatch.DefaultBackend()
	index.windowed = !index.unionFull && index.unionCount <= startByteUnionLimit &&
		simd.HasNativeWideMask(index.backend)
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
	// UTF-8 语义、编辑距离与运行时状态结构都会让首字节偏离语法树直接推导
	// 的结果，这里一律不做过滤。
	if rule.flags&(CompileUTF8|CompileUCP) != 0 {
		return full()
	}
	if rule.ext != nil && rule.ext.Flags&(ExtFlagEditDistance|ExtFlagHammingDistance) != 0 {
		return full()
	}
	if ruleCaseless(rule) {
		// 忽略大小写只改变字节的等价类，不改变哪些结构会消费字节：首字节集合
		// 仍可静态推导，按折叠后的闭包过滤即可。只有消费范围无法静态确定的
		// 结构才回退全放行。
		if unsupportedFirstBytes(rule.root) {
			return full()
		}
		bytes := parser.FirstBytesCaseless(rule.root)
		if len(bytes) == 0 || len(bytes) >= 256 {
			return full()
		}
		return bytes
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

// unsupportedFirstBytes 判断表达式是否包含首字节集合无法静态推导的结构：
// 反向引用、条件分支、环视与控制动词的消费范围都不能由语法树直接确定。
func unsupportedFirstBytes(root parser.Node) bool {
	unsupported := false
	parser.Walk(root, func(node parser.Node) bool {
		switch node.(type) {
		case parser.Backreference, parser.Conditional, parser.Lookaround, parser.ControlVerb:
			unsupported = true
			return false
		default:
			return true
		}
	})
	return unsupported
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
//
// 索引可窗口枚举时（并集稀疏且后端有原生宽窗口掩码），按 64 字节窗口一次判定
// 窗口内哪些偏移的字节确实属于某条规则的首字节集合，只对这些偏移做确认；被跳过
// 的偏移在逐字节版本里也只会读到空规则位图、什么都不做，因此输出顺序与命中完全
// 一致，只是省掉了整段必然无候选的逐字节查表。
func (st *blockScanState) confirmIndexed(index *startByteIndex) {
	if index == nil {
		return
	}
	if !index.windowed {
		st.confirmIndexedBytewise(index)
		return
	}
	data := st.data
	off := 0
	for ; off+simd.WideWidth <= len(data); off += simd.WideWidth {
		mask, ok := index.backend.WindowMask64(data, off, &index.tables, 1)
		if !ok {
			break
		}
		for mask != 0 {
			if !st.confirmIndexedAt(index, off+bits.TrailingZeros64(mask)) {
				return
			}
			mask &= mask - 1
		}
		if !st.scanner.simpleReports && st.ctx.Reports.Stopped() {
			return
		}
	}
	for ; off < len(data); off++ {
		value := data[off]
		if index.union[value>>6]&(uint64(1)<<uint(value&63)) == 0 {
			continue
		}
		if !st.confirmIndexedAt(index, off) {
			return
		}
	}
	if index.endRowNonEmpty && !st.confirmIndexedRow(index, startByteEndRow, len(data)) {
		return
	}
}

// confirmIndexedBytewise 是逐起点确认的原始实现，起点顺序与纯逐起点确认一致。
func (st *blockScanState) confirmIndexedBytewise(index *startByteIndex) {
	for start := 0; start <= len(st.data); start++ {
		if !st.confirmIndexedRow(index, index.groupOf(st.data, start), start) {
			return
		}
		if st.ctx.Reports.Stopped() {
			return
		}
	}
}

// confirmIndexedAt 确认单个候选起点：调用方已保证该偏移的字节属于首字节并集。
func (st *blockScanState) confirmIndexedAt(index *startByteIndex, start int) bool {
	return st.confirmIndexedRow(index, int(st.data[start]), start)
}

// confirmIndexedRow 逐规则确认某个起点；返回 false 表示扫描应中止。
func (st *blockScanState) confirmIndexedRow(index *startByteIndex, group int, start int) bool {
	row := index.rulesAt(group)
	for word := range index.width {
		remaining := row[word]
		for remaining != 0 {
			slot := word*64 + bits.TrailingZeros64(remaining)
			remaining &= remaining - 1
			if !st.verify(index.rules[slot], start) {
				return false
			}
		}
	}
	return true
}

// confirmCandidatesAndFallback 交错执行候选起点确认与首字节过滤后的逐起点确认。
// starts 已按 (起点, 规则下标) 升序，索引内规则也按规则下标升序，两者合并后
// 输出顺序与纯逐起点确认一致。
//
// 收敛窗口（折叠窗口、段首窗口）只登记了部分起点，被重叠抑制时会派生出推迟到
// blockedUntil 的补登候选。补登候选放在独立的小队列里按 (起点, 规则) 升序归并，
// 而不是插回 starts：starts 长度可达数万，每次插入都要搬移整段尾部，实测让
// 64 KiB 固定语料多付出 45% 的耗时。
func (st *blockScanState) confirmCandidatesAndFallback(starts []uint64, index *startByteIndex) {
	// retries[head:] 保存推迟到水位线的补登候选，按 (起点, 规则) 升序排列；
	// 每条规则至多有一个待处理项，长度有界于规则数。用读取下标而不是切片前移，
	// 避免消费后容量归零导致每次补登都重新分配。
	retries := st.ctx.retryStarts[:0]
	head := 0
	defer func() { st.ctx.retryStarts = retries }()
	queue := func(next uint64) {
		position := head + sort.Search(len(retries)-head, func(index int) bool { return retries[head+index] >= next })
		if position < len(retries) && retries[position] == next {
			return
		}
		retries = append(retries, 0)
		copy(retries[position+1:], retries[position:])
		retries[position] = next
	}
	if index == nil {
		// 候选已按 (起点, 规则) 升序。被重叠抑制或已终止的规则在这里直接
		// 跳过，避免为必然返回的候选进入通用确认例程。
		simple := st.scanner.simpleReports
		cursor := 0
		for cursor < len(starts) || head < len(retries) {
			key := uint64(0)
			if head < len(retries) && (cursor >= len(starts) || retries[head] <= starts[cursor]) {
				key, head = retries[head], head+1
			} else {
				key, cursor = starts[cursor], cursor+1
			}
			start := int(key >> 32)
			rule := int(uint32(key))
			if start < st.blockedUntil[rule] || st.fired[rule] {
				if next, ok := st.retryCollapsedCandidate(start, rule); ok {
					queue(next)
				}
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
			fromRetry := false
			if cursor < len(starts) {
				if candidateStart, candidateRule := requiredStartParts(starts[cursor]); candidateStart == start {
					candidate = candidateRule
				}
			}
			if head < len(retries) {
				if retryStart, retryRule := requiredStartParts(retries[head]); retryStart == start && (candidate < 0 || retryRule < candidate) {
					candidate = retryRule
					fromRetry = true
				}
			}
			if !hasRule && candidate < 0 {
				break
			}
			if candidate >= 0 && (!hasRule || candidate < rule) {
				if fromRetry {
					head++
				} else {
					cursor++
				}
				if start < st.blockedUntil[candidate] || st.fired[candidate] {
					if next, ok := st.retryCollapsedCandidate(start, candidate); ok {
						queue(next)
					}
					continue
				}
				if !st.verify(candidate, start) {
					return
				}
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

// retryCollapsedCandidate 处理窗口候选被重叠抑制的情况，覆盖两类只登记了部分
// 起点的候选窗口：
//
//   - 折叠窗口（见 collapseHead）：窗口内起点的确认结果完全相同，逐起点确认在
//     窗口内验证的是第一个未被抑制的起点，其余起点只会被 blockedUntil 挡住。
//   - 段首窗口（见 denseRunHead）：窗口内非段首起点与段首共享确认结果，逐起点
//     确认报告的段内起点只可能是段首，或恰好等于前一次命中的结束位置（重叠
//     抑制的水位线），后者正是这里补登记的位置。
//
// 两类窗口都只登记了部分起点，因此这里把候选推迟到 blockedUntil 位置重新登记：
// 该位置要么仍在窗口内、给出与逐起点确认一致的起点，要么已经越过窗口（此时窗口
// 整体被抑制，重新确认会自然失败，或命中一个本就在该位置成立的合法匹配）。登记
// 位置保持 (起点, 规则) 升序，所以结果集合与稳定投递顺序都与逐起点确认一致。
//
// 返回 false 表示该候选不需要推迟：规则没有上述窗口、规则已终止、推迟位置不晚于
// 当前起点，或推迟位置已经在数据末尾之外。
func (st *blockScanState) retryCollapsedCandidate(start int, rule int) (uint64, bool) {
	if st.fired[rule] {
		return 0, false
	}
	if st.scanner.rules[rule].collapseHead == nil && !st.scanner.runHeadWindows[rule] {
		return 0, false
	}
	next := st.blockedUntil[rule]
	if next <= start || next > len(st.data) {
		return 0, false
	}
	return requiredStartKey(next, rule), true
}
