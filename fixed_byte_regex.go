package scankit

import "math/bits"

const (
	maxFixedByteRegexSequences = 64
	maxFixedByteRegexLength    = 128
)

// fixedByteRegex 是纯字节定宽正则的有界展开。它从每个分支中选择性最强的字节直接分派，
// 避免为每个可能起点维护活跃 DFA 线程。
type fixedByteRegex struct {
	sequences       []fixedByteRegexSequence
	sequenceTrigger [byteClassCardinality][]uint16
}

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
}

type fixedByteRegexRun struct {
	matches []fixedByteRegexMatch
}

type fixedByteRegexMatch struct {
	start int
	end   int
}

func extractFixedByteRegex(root *regexNode) (*fixedByteRegex, bool) {
	sequences, ok := expandFixedByteRegex(root)
	if !ok || len(sequences) == 0 || len(sequences) > maxFixedByteRegexSequences {
		return nil, false
	}
	program := &fixedByteRegex{sequences: make([]fixedByteRegexSequence, len(sequences))}
	for index, classes := range sequences {
		if len(classes) == 0 || len(classes) > maxFixedByteRegexLength {
			return nil, false
		}
		triggerOffset := mostSelectiveByteClass(classes)
		program.sequences[index] = fixedByteRegexSequence{classes: classes, triggerOffset: triggerOffset}
		trigger := classes[triggerOffset]
		for value := range program.sequenceTrigger {
			if trigger.contains(byte(value)) {
				program.sequenceTrigger[value] = append(program.sequenceTrigger[value], uint16(index))
			}
		}
	}
	return program, true
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
		if node.min != node.max || node.min < 1 || node.min > maxFixedByteRegexLength {
			return nil, false
		}
		sequences, ok := expandFixedByteRegex(node.children[0])
		if !ok {
			return nil, false
		}
		result := [][]byteClass{{}}
		for range node.min {
			result, ok = combineFixedByteRegexSequences(result, sequences)
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
		count := bits.OnesCount64(class[0]) + bits.OnesCount64(class[1]) + bits.OnesCount64(class[2]) + bits.OnesCount64(class[3])
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
	return fixedByteRegexAnchor{class: classes[offset], offset: offset, width: width}, true
}

// extractFixedByteRegexAnchor 合并固定宽度分支在每个字节偏移的必要字符类。它不展开
// 分支组合：合并后的类仅作候选预过滤，完整 NFA 验证仍决定实际匹配。
func extractFixedByteRegexAnchor(root *regexNode) (fixedByteRegexAnchor, bool) {
	classes, ok := fixedByteRegexPositionClasses(root)
	if !ok || len(classes) == 0 || len(classes) > maxFixedByteRegexLength {
		return fixedByteRegexAnchor{}, false
	}
	offset := mostSelectiveByteClass(classes)
	return fixedByteRegexAnchor{class: classes[offset], offset: offset, width: len(classes)}, true
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
	for _, sequenceIndex := range program.sequenceTrigger[data[offset]] {
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
			for index := sequence.triggerOffset + 1; index < len(sequence.classes); index++ {
				if !sequence.classes[index].contains(data[start+index]) {
					matched = false
					break
				}
			}
		}
		if !matched {
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
