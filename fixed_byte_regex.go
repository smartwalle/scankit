package scankit

import (
	"math/bits"
	"slices"
)

const (
	maxFixedByteRegexSequences = 64
	maxFixedByteRegexLength    = 128
	maxFixedByteRegexChecks    = 3
	maxFixedByteRegexCheckSize = 16
	maxFixedLiteralAnchors     = 8
	maxFixedLiteralAnchorWidth = 4
)

// fixedByteRegex 是纯字节定宽正则的有界展开。它从每个分支中选择性最强的字节直接分派，
// 避免为每个可能起点维护活跃 DFA 线程。
type fixedByteRegex struct {
	sequences            []fixedByteRegexSequence
	sequenceIndexes      []uint16
	sequenceTrigger      [byteClassCardinality]fixedByteRegexTriggerRange
	mayDuplicateMatches  bool
	leadingWordBoundary  fixedByteRegexBoundary
	trailingWordBoundary fixedByteRegexBoundary
}

// fixedByteRegexTriggerRange 指向 sequenceIndexes 中同一个触发字节的连续序列。编译阶段
// 将原本分散的 slice 收紧为一个连续数组，扫描期只读取两个小整数并顺序访问分支索引。
// 这不会合并不同 triggerOffset，因此每个合法候选起点仍会独立验证。
type fixedByteRegexTriggerRange struct {
	start        uint16
	end          uint16
	sharedSuffix uint8
	nextSubset   *[byteClassCardinality]uint64
}

func (r fixedByteRegexTriggerRange) empty() bool {
	return r.start == r.end
}

func (r fixedByteRegexTriggerRange) length() int {
	return int(r.end - r.start)
}

// fixedByteRegexBoundary 表示定长字节执行器可在首尾独立验证的单词边界断言。
// 中间断言仍交给完整 NFA，避免改变其匹配语义。
type fixedByteRegexBoundary uint8

const (
	fixedByteRegexNoBoundary   fixedByteRegexBoundary = 0
	fixedByteRegexWordBoundary fixedByteRegexBoundary = 1 << (iota - 1)
	fixedByteRegexNotWordBoundary
	fixedByteRegexStart
	fixedByteRegexEnd
)

type fixedByteRegexSequence struct {
	classes       []byteClass
	asciiRanges   []fixedByteRegexASCIIRange
	triggerOffset int
}

// fixedByteRegexASCIIRange 是纯 ASCII 连续字符类的紧凑表示。它只用于整个序列的每个
// 位置都可精确表达为 ASCII 区间时；其他序列继续使用 byteClass 位图，不会改变语义。
type fixedByteRegexASCIIRange struct {
	minimum byte
	maximum byte
}

// fixedByteRegexAnchor 是定宽表达式每个分支在固定偏移处的必要字节类条件。完整语言仍由
// NFA 验证器判定，因此该分派不会扩大匹配范围。
type fixedByteRegexAnchor struct {
	class  byteClass
	offset int
	width  int
	checks *fixedByteRegexAnchorChecks
}

// fixedByteRegexAnchorChecks 保存触发位置之外的必要字符类。它只在编译期能得出定宽
// 位置类时创建；扫描期全部通过后仍由 verifier 做最终语言判定。
type fixedByteRegexAnchorChecks struct {
	values [maxFixedByteRegexChecks]fixedByteRegexAnchorCheck
	count  uint8
}

type fixedByteRegexAnchorCheck struct {
	class  byteClass
	offset int
}

type fixedByteRegexRun struct {
	matches []fixedByteRegexMatch
}

type fixedByteRegexMatch struct {
	start int
	end   int
}

func extractFixedByteRegex(root *regexNode) (*fixedByteRegex, bool) {
	root, leadingBoundary, trailingBoundary, ok := splitFixedByteRegexBoundaries(root)
	if !ok {
		return nil, false
	}
	sequences, ok := expandFixedByteRegex(root)
	if !ok || len(sequences) == 0 || len(sequences) > maxFixedByteRegexSequences {
		return nil, false
	}
	program := &fixedByteRegex{
		sequences:            make([]fixedByteRegexSequence, len(sequences)),
		leadingWordBoundary:  leadingBoundary,
		trailingWordBoundary: trailingBoundary,
	}
	triggerOffsets := make([]int, len(sequences))
	var individualTrigger byteClass
	for index, classes := range sequences {
		if len(classes) == 0 || len(classes) > maxFixedByteRegexLength {
			return nil, false
		}
		triggerOffsets[index] = mostSelectiveByteClass(classes)
		individualTrigger.merge(classes[triggerOffsets[index]])
	}
	// 变宽分支可能拥有相同的后缀。若共同字符类比各分支独立触发字符的并集更小，统一
	// 由该类触发可减少日志中无关前缀字符造成的 verifier 调用；等宽分支后续会转为
	// fixedAnchor，本选择不会改变其执行路径。
	if offsets, trigger, ok := commonFixedByteRegexTrigger(sequences); ok && byteClassSize(trigger) < byteClassSize(individualTrigger) {
		triggerOffsets = offsets
	}
	for index, classes := range sequences {
		triggerOffset := triggerOffsets[index]
		sequence := fixedByteRegexSequence{
			classes:       classes,
			asciiRanges:   fixedByteRegexASCIIRanges(classes),
			triggerOffset: triggerOffset,
		}
		program.sequences[index] = sequence
	}
	var triggerCounts [byteClassCardinality]uint16
	for _, sequence := range program.sequences {
		trigger := sequence.classes[sequence.triggerOffset]
		for value := range triggerCounts {
			if trigger.contains(byte(value)) {
				triggerCounts[value]++
			}
		}
	}
	total := 0
	for value, count := range triggerCounts {
		program.sequenceTrigger[value].start = uint16(total)
		total += int(count)
		program.sequenceTrigger[value].end = uint16(total)
	}
	// 一个程序最多展开 64 个序列，每个序列最多覆盖 256 个字节；总索引数小于 uint16。
	program.sequenceIndexes = make([]uint16, total)
	cursors := program.sequenceTrigger
	for index, sequence := range program.sequences {
		trigger := sequence.classes[sequence.triggerOffset]
		for value := range cursors {
			if !trigger.contains(byte(value)) {
				continue
			}
			cursor := cursors[value].start
			program.sequenceIndexes[cursor] = uint16(index)
			cursors[value].start++
		}
	}
	for value, triggerRange := range program.sequenceTrigger {
		if triggerRange.length() < 2 {
			continue
		}
		first := program.sequences[program.sequenceIndexes[triggerRange.start]]
		shared := 0
		for {
			position := first.triggerOffset + shared + 1
			if position >= len(first.classes) {
				break
			}
			class := first.classes[position]
			matchesAll := true
			for index := triggerRange.start + 1; index < triggerRange.end; index++ {
				sequenceIndex := program.sequenceIndexes[index]
				sequence := program.sequences[sequenceIndex]
				otherPosition := sequence.triggerOffset + shared + 1
				if otherPosition >= len(sequence.classes) || sequence.classes[otherPosition] != class {
					matchesAll = false
					break
				}
			}
			if !matchesAll {
				break
			}
			shared++
		}
		if shared != 0 {
			program.sequenceTrigger[value].sharedSuffix = uint8(shared)
		}
		// 分支较多且下一位置字符类足够稀疏时，预先为每个下一字节建立分支位集。
		// 扫描时只遍历可能接受该字节的序列；位集按原 sequenceIndexes 顺序消费，
		// 因此不会改变重叠结果或事件顺序。共享后缀已经是更短路径时不再叠加。
		if shared == 0 && triggerRange.length() >= 8 {
			var subsets [byteClassCardinality]uint64
			valid := true
			members := 0
			for index := triggerRange.start; index < triggerRange.end && valid; index++ {
				sequence := program.sequences[program.sequenceIndexes[index]]
				position := sequence.triggerOffset + 1
				if position >= len(sequence.classes) {
					valid = false
					break
				}
				for value := 0; value < byteClassCardinality; value++ {
					if sequence.classes[position].contains(byte(value)) {
						subsets[value] |= uint64(1) << uint(index-triggerRange.start)
						members++
					}
				}
			}
			// 平均候选密度不超过一半时，位集才有机会抵消一次下一字节读取。
			if valid && members*2 <= triggerRange.length()*byteClassCardinality {
				subsetCopy := subsets
				program.sequenceTrigger[value].nextSubset = &subsetCopy
			}
		}
	}
	// 同一触发位置可能对应多个展开分支。只有两个等长分支的每个对应位置都存在
	// 交集时，才可能在一次候选中生成相同的范围；其余情况无需在扫描期逐个回看
	// 已产生的结果。这个判断只依赖已编译的字符类，不改变分支或重叠匹配语义。
	program.mayDuplicateMatches = fixedByteRegexMayDuplicateMatches(program.sequences)
	return program, true
}

func fixedByteRegexASCIIRanges(classes []byteClass) []fixedByteRegexASCIIRange {
	for _, class := range classes {
		if _, _, ok := byteClassASCIIRange(class); !ok {
			return nil
		}
	}
	ranges := make([]fixedByteRegexASCIIRange, len(classes))
	for index, class := range classes {
		minimum, maximum, _ := byteClassASCIIRange(class)
		ranges[index] = fixedByteRegexASCIIRange{minimum: minimum, maximum: maximum}
	}
	return ranges
}

func fixedByteRegexMayDuplicateMatches(sequences []fixedByteRegexSequence) bool {
	for left := 0; left < len(sequences); left++ {
		for right := left + 1; right < len(sequences); right++ {
			first, second := sequences[left], sequences[right]
			if len(first.classes) != len(second.classes) {
				continue
			}
			overlaps := true
			for index := range first.classes {
				if !byteClassesOverlap(first.classes[index], second.classes[index]) {
					overlaps = false
					break
				}
			}
			if overlaps {
				return true
			}
		}
	}
	return false
}

func byteClassesOverlap(left, right byteClass) bool {
	return left[0]&right[0] != 0 || left[1]&right[1] != 0 || left[2]&right[2] != 0 || left[3]&right[3] != 0
}

// commonFixedByteRegexTrigger 查找每个分支均包含的同一位置类。它只给出候选；调用方
// 还会比较字符类大小，确保不会用宽泛共同后缀替换更具选择性的分支触发器。
func commonFixedByteRegexTrigger(sequences [][]byteClass) ([]int, byteClass, bool) {
	if len(sequences) < 2 {
		return nil, byteClass{}, false
	}
	var best byteClass
	var bestOffsets []int
	found := false
	for firstOffset, candidate := range sequences[0] {
		offsets := make([]int, len(sequences))
		offsets[0] = firstOffset
		complete := true
		for sequenceIndex := 1; sequenceIndex < len(sequences); sequenceIndex++ {
			offset := fixedByteRegexClassIndex(sequences[sequenceIndex], candidate)
			if offset < 0 {
				complete = false
				break
			}
			offsets[sequenceIndex] = offset
		}
		if !complete || (found && byteClassSize(best) <= byteClassSize(candidate)) {
			continue
		}
		best, bestOffsets, found = candidate, offsets, true
	}
	return bestOffsets, best, found
}

func fixedByteRegexClassIndex(classes []byteClass, target byteClass) int {
	for index, class := range classes {
		if class == target {
			return index
		}
	}
	return -1
}

func fixedByteRegexHasSingleWidth(fixed *fixedByteRegex) bool {
	if fixed == nil || len(fixed.sequences) == 0 {
		return false
	}
	width := len(fixed.sequences[0].classes)
	for _, sequence := range fixed.sequences[1:] {
		if len(sequence.classes) != width {
			return false
		}
	}
	return true
}

// splitFixedByteRegexBoundaries 仅从连接表达式两端剥离 \b 或 \B。两端之外的断言无法
// 由固定位置类安全表示，必须继续使用完整 NFA/DFA 执行器。
func splitFixedByteRegexBoundaries(root *regexNode) (*regexNode, fixedByteRegexBoundary, fixedByteRegexBoundary, bool) {
	if root == nil {
		return nil, fixedByteRegexNoBoundary, fixedByteRegexNoBoundary, false
	}
	if root.kind != regexConcat {
		return root, fixedByteRegexNoBoundary, fixedByteRegexNoBoundary, true
	}
	start, end := 0, len(root.children)
	for start < end && root.children[start].kind == regexEmpty {
		start++
	}
	for end > start && root.children[end-1].kind == regexEmpty {
		end--
	}
	leading := fixedByteRegexNoBoundary
	if start < end {
		if boundary, ok := fixedByteRegexBoundaryForNode(root.children[start]); ok {
			leading = boundary
			start++
		}
	}
	trailing := fixedByteRegexNoBoundary
	if start < end {
		if boundary, ok := fixedByteRegexBoundaryForNode(root.children[end-1]); ok {
			trailing = boundary
			end--
		}
	}
	if start >= end {
		return nil, fixedByteRegexNoBoundary, fixedByteRegexNoBoundary, false
	}
	if end-start == 1 {
		return root.children[start], leading, trailing, true
	}
	return &regexNode{kind: regexConcat, children: root.children[start:end]}, leading, trailing, true
}

func fixedByteRegexBoundaryForNode(node *regexNode) (fixedByteRegexBoundary, bool) {
	if node == nil {
		return fixedByteRegexNoBoundary, false
	}
	switch node.kind {
	case regexWordBoundary:
		return fixedByteRegexWordBoundary, true
	case regexNotWordBoundary:
		return fixedByteRegexNotWordBoundary, true
	case regexAbsoluteStart:
		return fixedByteRegexStart, true
	case regexAbsoluteEnd:
		return fixedByteRegexEnd, true
	case regexAnchorStart:
		if node.flags&CompileMultiline == 0 {
			return fixedByteRegexStart, true
		}
	case regexAnchorEnd:
		if node.flags&CompileMultiline == 0 {
			return fixedByteRegexEnd, true
		}
	case regexAlternate:
		if len(node.children) == 0 {
			return fixedByteRegexNoBoundary, false
		}
		var boundary fixedByteRegexBoundary
		for _, child := range node.children {
			part, ok := fixedByteRegexBoundaryForNode(child)
			if !ok {
				return fixedByteRegexNoBoundary, false
			}
			boundary |= part
		}
		return boundary, boundary != fixedByteRegexNoBoundary
	default:
	}
	return fixedByteRegexNoBoundary, false
}

// extractBoundedByteClassRepeat 将重复的单字节分支下沉为等价字节类。它刻意仅限一个
// 重复原子，以便局部证明语言等价，并避免 (?:A|B){6,8} 形式的指数分支展开。
func extractBoundedByteClassRepeat(root *regexNode) (byteClass, int, int, bool) {
	if root == nil || root.kind != regexRepeat || len(root.children) != 1 || root.min < 1 || root.max == unboundedRepeat || root.max > maxEditLiteralLength {
		return byteClass{}, 0, 0, false
	}
	class, ok := extractSingleByteClass(root.children[0])
	if !ok {
		return byteClass{}, 0, 0, false
	}
	return class, root.min, root.max, true
}

func extractSingleByteClass(node *regexNode) (byteClass, bool) {
	if node == nil {
		return byteClass{}, false
	}
	switch node.kind {
	case regexLiteral:
		var class byteClass
		class.add(node.literal)
		return class, true
	case regexClass:
		return node.class, true
	case regexAlternate:
		if len(node.children) == 0 {
			return byteClass{}, false
		}
		var class byteClass
		for _, child := range node.children {
			member, ok := extractSingleByteClass(child)
			if !ok {
				return byteClass{}, false
			}
			class.merge(member)
		}
		return class, true
	default:
		return byteClass{}, false
	}
}

func expandFixedByteRegex(node *regexNode) ([][]byteClass, bool) {
	if node == nil {
		return nil, false
	}
	switch node.kind {
	case regexLiteral:
		var class byteClass
		class.add(node.literal)
		return [][]byteClass{{class}}, true
	case regexClass:
		return [][]byteClass{{node.class}}, true
	case regexConcat:
		sequences := [][]byteClass{{}}
		for _, child := range node.children {
			childSequences, ok := expandFixedByteRegex(child)
			if !ok {
				return nil, false
			}
			sequences, ok = combineFixedByteRegexSequences(sequences, childSequences)
			if !ok {
				return nil, false
			}
		}
		return sequences, true
	case regexAlternate:
		sequences := make([][]byteClass, 0, len(node.children))
		for _, child := range node.children {
			childSequences, ok := expandFixedByteRegex(child)
			if !ok || len(sequences)+len(childSequences) > maxFixedByteRegexSequences {
				return nil, false
			}
			sequences = append(sequences, childSequences...)
		}
		return sequences, true
	case regexRepeat:
		if len(node.children) != 1 || node.min < 0 || node.max == unboundedRepeat || node.max < node.min || node.max > maxFixedByteRegexLength {
			return nil, false
		}
		sequences, ok := expandFixedByteRegex(node.children[0])
		if !ok {
			return nil, false
		}
		result := make([][]byteClass, 0, maxFixedByteRegexSequences)
		current := [][]byteClass{{}}
		for count := 0; count <= node.max; count++ {
			if count >= node.min {
				if len(result)+len(current) > maxFixedByteRegexSequences {
					return nil, false
				}
				result = append(result, current...)
			}
			if count == node.max {
				break
			}
			current, ok = combineFixedByteRegexSequences(current, sequences)
			if !ok {
				return nil, false
			}
		}
		return result, true
	default:
		return nil, false
	}
}

func combineFixedByteRegexSequences(left, right [][]byteClass) ([][]byteClass, bool) {
	if len(left) == 0 || len(right) == 0 || len(left)*len(right) > maxFixedByteRegexSequences {
		return nil, false
	}
	result := make([][]byteClass, 0, len(left)*len(right))
	for _, prefix := range left {
		for _, suffix := range right {
			if len(prefix)+len(suffix) > maxFixedByteRegexLength {
				return nil, false
			}
			sequence := make([]byteClass, 0, len(prefix)+len(suffix))
			sequence = append(sequence, prefix...)
			sequence = append(sequence, suffix...)
			result = append(result, sequence)
		}
	}
	return result, true
}

func mostSelectiveByteClass(classes []byteClass) int {
	best, bestCount := 0, byteClassCardinality+1
	for index, class := range classes {
		count := byteClassSize(class)
		if count < bestCount {
			best, bestCount = index, count
		}
	}
	return best
}

func fixedByteRegexClassAnchor(sequences []fixedByteRegexSequence) (fixedByteRegexAnchor, bool) {
	if len(sequences) < 2 {
		return fixedByteRegexAnchor{}, false
	}
	width := len(sequences[0].classes)
	if width == 0 {
		return fixedByteRegexAnchor{}, false
	}
	classes := make([]byteClass, width)
	for _, sequence := range sequences {
		if len(sequence.classes) != width {
			return fixedByteRegexAnchor{}, false
		}
		for index, class := range sequence.classes {
			classes[index].merge(class)
		}
	}
	offset := mostSelectiveByteClass(classes)
	return newFixedByteRegexAnchor(classes, offset), true
}

// fixedByteRegexLiteralAnchors 查找定宽展开分支共同覆盖的、同一固定偏移的短字面量集合。
// 每个序列都必须在该窗口内由单字节类组成，且不同字面量数量受严格上限约束；因此任一
// 完整匹配必然经过其中一个 AC 锚点。它只提供候选，不参与最终语言判断。
func fixedByteRegexLiteralAnchors(sequences []fixedByteRegexSequence) (anchors []string, offset int, ok bool) {
	if len(sequences) < 2 {
		return nil, 0, false
	}
	width := len(sequences[0].classes)
	for _, sequence := range sequences[1:] {
		if len(sequence.classes) < width {
			width = len(sequence.classes)
		}
	}
	if width < 2 {
		return nil, 0, false
	}
	maximum := maxFixedLiteralAnchorWidth
	if maximum > width {
		maximum = width
	}
	for length := maximum; length >= 2; length-- {
		for start := 0; start+length <= width; start++ {
			values := make(map[string]struct{}, len(sequences))
			valid := true
			for _, sequence := range sequences {
				bytes := make([]byte, length)
				for index := range bytes {
					value, single := byteClassSingleValue(sequence.classes[start+index])
					if !single {
						valid = false
						break
					}
					bytes[index] = value
				}
				if !valid {
					break
				}
				values[string(bytes)] = struct{}{}
				if len(values) > maxFixedLiteralAnchors {
					valid = false
					break
				}
			}
			if !valid {
				continue
			}
			anchors = make([]string, 0, len(values))
			for value := range values {
				anchors = append(anchors, value)
			}
			slices.Sort(anchors)
			return anchors, start, true
		}
	}
	return nil, 0, false
}

func byteClassSingleValue(class byteClass) (byte, bool) {
	for word, values := range class {
		if values == 0 {
			continue
		}
		if values&(values-1) != 0 {
			return 0, false
		}
		for following := word + 1; following < len(class); following++ {
			if class[following] != 0 {
				return 0, false
			}
		}
		return byte(word*64 + bits.TrailingZeros64(values)), true
	}
	return 0, false
}

// extractFixedByteRegexAnchor 合并固定宽度分支在每个字节偏移的必要字符类。它不展开
// 分支组合：合并后的类仅作候选预过滤，完整 NFA 验证仍决定实际匹配。
func extractFixedByteRegexAnchor(root *regexNode) (fixedByteRegexAnchor, bool) {
	classes, ok := fixedByteRegexPositionClasses(root)
	if !ok || len(classes) == 0 || len(classes) > maxFixedByteRegexLength {
		return fixedByteRegexAnchor{}, false
	}
	offset := mostSelectiveByteClass(classes)
	return newFixedByteRegexAnchor(classes, offset), true
}

func newFixedByteRegexAnchor(classes []byteClass, offset int) fixedByteRegexAnchor {
	return fixedByteRegexAnchor{
		class:  classes[offset],
		offset: offset,
		width:  len(classes),
		checks: selectFixedByteRegexAnchorChecks(classes, offset),
	}
}

// selectFixedByteRegexAnchorChecks 选择少量独立的必要位置：先取最小字符类，再取距触发
// 位置最远的选择性类，最后按选择性补齐。这样既能快速拒绝常见候选，也能覆盖模式末尾的
// 格式错误；所有检查均只来自各分支同一固定偏移的并集。
func selectFixedByteRegexAnchorChecks(classes []byteClass, anchorOffset int) *fixedByteRegexAnchorChecks {
	candidates := make([]fixedByteRegexAnchorCheck, 0, len(classes))
	for offset, class := range classes {
		if offset == anchorOffset || byteClassSize(class) > maxFixedByteRegexCheckSize {
			continue
		}
		candidates = append(candidates, fixedByteRegexAnchorCheck{class: class, offset: offset})
	}
	if len(candidates) == 0 {
		return nil
	}
	checks := &fixedByteRegexAnchorChecks{}
	appendCheck := func(index int) {
		candidate := candidates[index]
		for existing := 0; existing < int(checks.count); existing++ {
			if checks.values[existing].offset == candidate.offset {
				return
			}
		}
		checks.values[checks.count] = candidate
		checks.count++
	}
	best := 0
	for index := 1; index < len(candidates); index++ {
		if fixedByteRegexAnchorCheckLess(candidates[index], candidates[best], anchorOffset) {
			best = index
		}
	}
	appendCheck(best)
	if checks.count < maxFixedByteRegexChecks {
		farthest := 0
		for index := 1; index < len(candidates); index++ {
			if fixedByteRegexAnchorCheckFurther(candidates[index], candidates[farthest], anchorOffset) {
				farthest = index
			}
		}
		appendCheck(farthest)
	}
	for checks.count < maxFixedByteRegexChecks {
		best = -1
		for index, candidate := range candidates {
			alreadySelected := false
			for existing := 0; existing < int(checks.count); existing++ {
				if checks.values[existing].offset == candidate.offset {
					alreadySelected = true
					break
				}
			}
			if !alreadySelected && (best == -1 || fixedByteRegexAnchorCheckLess(candidate, candidates[best], anchorOffset)) {
				best = index
			}
		}
		if best == -1 {
			break
		}
		appendCheck(best)
	}
	return checks
}

func fixedByteRegexAnchorCheckLess(left, right fixedByteRegexAnchorCheck, anchorOffset int) bool {
	leftSize, rightSize := byteClassSize(left.class), byteClassSize(right.class)
	if leftSize != rightSize {
		return leftSize < rightSize
	}
	return absoluteDistance(left.offset, anchorOffset) > absoluteDistance(right.offset, anchorOffset)
}

func fixedByteRegexAnchorCheckFurther(left, right fixedByteRegexAnchorCheck, anchorOffset int) bool {
	leftDistance, rightDistance := absoluteDistance(left.offset, anchorOffset), absoluteDistance(right.offset, anchorOffset)
	if leftDistance != rightDistance {
		return leftDistance > rightDistance
	}
	return byteClassSize(left.class) < byteClassSize(right.class)
}

func fixedByteRegexAnchorChecksMatch(data []byte, start int, checks *fixedByteRegexAnchorChecks) bool {
	for index := 0; index < int(checks.count); index++ {
		// byteClass 含四个 uint64。保留数组元素的地址，避免每个候选都复制整个字符类；
		// checks 在已编译 Scanner 中不可变，因此这里没有并发可见性问题。
		check := &checks.values[index]
		if !check.class.contains(data[start+check.offset]) {
			return false
		}
	}
	return true
}

func byteClassSize(class byteClass) int {
	return bits.OnesCount64(class[0]) + bits.OnesCount64(class[1]) + bits.OnesCount64(class[2]) + bits.OnesCount64(class[3])
}

func absoluteDistance(left, right int) int {
	if left < right {
		return right - left
	}
	return left - right
}

func fixedByteRegexPositionClasses(node *regexNode) ([]byteClass, bool) {
	if node == nil {
		return nil, false
	}
	switch node.kind {
	case regexLiteral:
		var class byteClass
		class.add(node.literal)
		return []byteClass{class}, true
	case regexClass:
		return []byteClass{node.class}, true
	case regexConcat:
		classes := make([]byteClass, 0)
		for _, child := range node.children {
			part, ok := fixedByteRegexPositionClasses(child)
			if !ok || len(classes)+len(part) > maxFixedByteRegexLength {
				return nil, false
			}
			classes = append(classes, part...)
		}
		return classes, true
	case regexAlternate:
		if len(node.children) == 0 {
			return nil, false
		}
		classes, ok := fixedByteRegexPositionClasses(node.children[0])
		if !ok {
			return nil, false
		}
		classes = append([]byteClass(nil), classes...)
		for _, child := range node.children[1:] {
			part, ok := fixedByteRegexPositionClasses(child)
			if !ok || len(part) != len(classes) {
				return nil, false
			}
			for index := range classes {
				classes[index].merge(part[index])
			}
		}
		return classes, true
	case regexRepeat:
		if node.min != node.max || node.min < 1 || node.min > maxFixedByteRegexLength || len(node.children) != 1 {
			return nil, false
		}
		part, ok := fixedByteRegexPositionClasses(node.children[0])
		if !ok || len(part) == 0 || len(part)*node.min > maxFixedByteRegexLength {
			return nil, false
		}
		classes := make([]byteClass, 0, len(part)*node.min)
		for range node.min {
			classes = append(classes, part...)
		}
		return classes, true
	default:
		return nil, false
	}
}

// advance 仅验证选定锚点接受 value 的分支。选择性锚点可位于序列结束前，因此调用方应立即
// 投递结果，或经由常规待处理事件队列投递。
func (run *fixedByteRegexRun) advance(program *fixedByteRegex, data []byte, offset int) []fixedByteRegexMatch {
	triggerRange := program.sequenceTrigger[data[offset]]
	if triggerRange.empty() {
		return run.matches[:0]
	}
	return run.advanceKnownTrigger(program, data, offset, triggerRange)
}

// advanceKnownTrigger 由已按触发字节分桶的 Block 扫描循环调用。调用方已证明 triggerRange
// 非空，因此这里不再重复读取输入字节或查询 256 项触发表；直接入口 advance 仍保留给测试
// 和未知候选调用方。
func (run *fixedByteRegexRun) advanceKnownTrigger(program *fixedByteRegex, data []byte, offset int, triggerRange fixedByteRegexTriggerRange) []fixedByteRegexMatch {
	matches := run.matches[:0]
	sharedSuffix := int(triggerRange.sharedSuffix)
	if sharedSuffix != 0 {
		if offset+sharedSuffix >= len(data) {
			return matches
		}
		first := &program.sequences[program.sequenceIndexes[triggerRange.start]]
		if first.asciiRanges != nil {
			// 切出已证明共同的后缀，避免每个候选在循环内重复计算相对于
			// sequence 起点的位置；窗口边界已由上方检查保证。
			suffix := first.asciiRanges[first.triggerOffset+1 : first.triggerOffset+sharedSuffix+1]
			for index, asciiRange := range suffix {
				value := data[offset+index+1]
				if value < asciiRange.minimum || value > asciiRange.maximum {
					return matches
				}
			}
		} else {
			suffix := first.classes[first.triggerOffset+1 : first.triggerOffset+sharedSuffix+1]
			for index, class := range suffix {
				if !class.contains(data[offset+index+1]) {
					return matches
				}
			}
		}
	}
	sequenceIndexCursor := triggerRange.start
	var subset uint64
	if triggerRange.nextSubset != nil {
		if offset+1 >= len(data) {
			return matches
		}
		subset = triggerRange.nextSubset[data[offset+1]]
	}
	for sequenceIndexCursor < triggerRange.end {
		if triggerRange.nextSubset != nil {
			if subset == 0 {
				break
			}
			bit := bits.TrailingZeros64(subset)
			sequenceIndexCursor = triggerRange.start + uint16(bit)
			subset &^= uint64(1) << uint(bit)
		}
		sequenceIndex := program.sequenceIndexes[sequenceIndexCursor]
		if triggerRange.nextSubset == nil {
			sequenceIndexCursor++
		}
		// sequence 含有一个 slice header。直接使用已编译数组元素的地址，避免在每个
		// 触发候选上复制它；Scanner 在编译后不可变，因此指针在并发扫描中稳定。
		sequence := &program.sequences[sequenceIndex]
		start := offset - sequence.triggerOffset
		end := start + len(sequence.classes)
		if start < 0 || end > len(data) {
			continue
		}
		// sequenceTrigger 已证明 triggerOffset 处的选定字符类。纯 ASCII 连续类使用
		// 紧凑范围比较；其余表达式继续直接索引 byteClass 位图。
		matched := true
		if sequence.asciiRanges != nil {
			for index := 0; index < sequence.triggerOffset; index++ {
				value := data[start+index]
				asciiRange := sequence.asciiRanges[index]
				if value < asciiRange.minimum || value > asciiRange.maximum {
					matched = false
					break
				}
			}
			if matched {
				for index := sequence.triggerOffset + sharedSuffix + 1; index < len(sequence.asciiRanges); index++ {
					value := data[start+index]
					asciiRange := sequence.asciiRanges[index]
					if value < asciiRange.minimum || value > asciiRange.maximum {
						matched = false
						break
					}
				}
			}
		} else {
			for index := 0; index < sequence.triggerOffset; index++ {
				if !sequence.classes[index].contains(data[start+index]) {
					matched = false
					break
				}
			}
			if matched {
				for index := sequence.triggerOffset + sharedSuffix + 1; index < len(sequence.classes); index++ {
					if !sequence.classes[index].contains(data[start+index]) {
						matched = false
						break
					}
				}
			}
		}
		if !matched {
			continue
		}
		if !fixedByteRegexBoundaryMatches(program.leadingWordBoundary, data, start) ||
			!fixedByteRegexBoundaryMatches(program.trailingWordBoundary, data, end) {
			continue
		}
		if !program.mayDuplicateMatches {
			matches = append(matches, fixedByteRegexMatch{start: start, end: end})
			continue
		}
		duplicate := false
		for _, prior := range matches {
			if prior.start == start && prior.end == end {
				duplicate = true
				break
			}
		}
		if !duplicate {
			matches = append(matches, fixedByteRegexMatch{start: start, end: end})
		}
	}
	// 失败候选返回的 matches 始终是 run.matches[:0]；下一次 advance 也会从零长度
	// 切片开始，无需把这个已知长度重复写回 context。只有产生结果时才更新复用容量。
	if len(matches) != 0 {
		run.matches = matches
	}
	return matches
}

func fixedByteRegexBoundaryMatches(boundary fixedByteRegexBoundary, data []byte, offset int) bool {
	return boundary == fixedByteRegexNoBoundary ||
		boundary&fixedByteRegexWordBoundary != 0 && nfaMatchesWordBoundary(data, offset) ||
		boundary&fixedByteRegexNotWordBoundary != 0 && !nfaMatchesWordBoundary(data, offset) ||
		boundary&fixedByteRegexStart != 0 && offset == 0 ||
		boundary&fixedByteRegexEnd != 0 && offset == len(data)
}
