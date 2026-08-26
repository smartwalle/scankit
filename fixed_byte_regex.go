package scankit

import "math/bits"

const (
	maxFixedByteRegexSequences = 64
	maxFixedByteRegexLength    = 128
	maxFixedByteRegexChecks    = 3
	maxFixedByteRegexCheckSize = 16
)

// fixedByteRegex 是纯字节定宽正则的有界展开。它从每个分支中选择性最强的字节直接分派，
// 避免为每个可能起点维护活跃 DFA 线程。
type fixedByteRegex struct {
	sequences            []fixedByteRegexSequence
	sequenceTrigger      [byteClassCardinality][]uint16
	sharedSuffix         [byteClassCardinality]uint8
	leadingWordBoundary  fixedByteRegexBoundary
	trailingWordBoundary fixedByteRegexBoundary
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
	triggerOffset int
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
		program.sequences[index] = fixedByteRegexSequence{classes: classes, triggerOffset: triggerOffset}
		trigger := classes[triggerOffset]
		for value := range program.sequenceTrigger {
			if trigger.contains(byte(value)) {
				program.sequenceTrigger[value] = append(program.sequenceTrigger[value], uint16(index))
			}
		}
	}
	for value, sequenceIndexes := range program.sequenceTrigger {
		if len(sequenceIndexes) < 2 {
			continue
		}
		first := program.sequences[sequenceIndexes[0]]
		shared := 0
		for {
			position := first.triggerOffset + shared + 1
			if position >= len(first.classes) {
				break
			}
			class := first.classes[position]
			matchesAll := true
			for _, sequenceIndex := range sequenceIndexes[1:] {
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
			program.sharedSuffix[value] = uint8(shared)
		}
	}
	return program, true
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
	matches := run.matches[:0]
	value := data[offset]
	sequenceIndexes := program.sequenceTrigger[value]
	sharedSuffix := int(program.sharedSuffix[value])
	if sharedSuffix != 0 {
		if offset+sharedSuffix >= len(data) {
			run.matches = matches
			return matches
		}
		first := program.sequences[sequenceIndexes[0]]
		for index := 0; index < sharedSuffix; index++ {
			if !first.classes[first.triggerOffset+index+1].contains(data[offset+index+1]) {
				run.matches = matches
				return matches
			}
		}
	}
	for _, sequenceIndex := range sequenceIndexes {
		sequence := program.sequences[sequenceIndex]
		start := offset - sequence.triggerOffset
		end := start + len(sequence.classes)
		if start < 0 || end > len(data) {
			continue
		}
		// sequenceTrigger 已证明 triggerOffset 处的选定字符类。避免重新加载它，并直接索引
		// classes，防止 range 为每个候选字节复制四个字的 byteClass。
		matched := true
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
		if !matched {
			continue
		}
		if !fixedByteRegexBoundaryMatches(program.leadingWordBoundary, data, start) ||
			!fixedByteRegexBoundaryMatches(program.trailingWordBoundary, data, end) {
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
	run.matches = matches
	return matches
}

func fixedByteRegexBoundaryMatches(boundary fixedByteRegexBoundary, data []byte, offset int) bool {
	return boundary == fixedByteRegexNoBoundary ||
		boundary&fixedByteRegexWordBoundary != 0 && nfaMatchesWordBoundary(data, offset) ||
		boundary&fixedByteRegexNotWordBoundary != 0 && !nfaMatchesWordBoundary(data, offset) ||
		boundary&fixedByteRegexStart != 0 && offset == 0 ||
		boundary&fixedByteRegexEnd != 0 && offset == len(data)
}
