package scankit

// extractByteRegexRepeat 识别一个字节类的非空重复。它刻意排除可空形式，其零宽报告由常规
// NFA 路径处理。
func extractByteRegexRepeat(root *regexNode) (byteRegexRepeat, bool) {
	if root == nil || root.kind != regexRepeat || len(root.children) != 1 || root.min < 1 {
		return byteRegexRepeat{}, false
	}
	child := root.children[0]
	var class byteClass
	switch child.kind {
	case regexLiteral:
		class.add(child.literal)
	case regexClass:
		class = child.class
	default:
		return byteRegexRepeat{}, false
	}
	return byteRegexRepeat{class: class, minimum: root.min, maximum: root.max}, true
}

// extractByteWordBoundedRegexRepeat 在重复类仅含 ASCII 单词字节时识别 \b<class-repeat>\b。
// 此时可直接在每个候选运行的起点和终点判断边界。
func extractByteWordBoundedRegexRepeat(root *regexNode) (byteRegexRepeat, bool) {
	if root == nil || root.kind != regexConcat || len(root.children) != 3 || root.children[0].kind != regexWordBoundary || root.children[2].kind != regexWordBoundary {
		return byteRegexRepeat{}, false
	}
	repeat, ok := extractByteRegexRepeat(root.children[1])
	if !ok {
		return byteRegexRepeat{}, false
	}
	for value := 0; value < 256; value++ {
		if repeat.class.contains(byte(value)) && !isASCIIWordByte(byte(value)) {
			return byteRegexRepeat{}, false
		}
	}
	repeat.wordBounded = true
	return repeat, true
}

// extractByteRegexPrefixRepeat 识别“一个前导 class，后接同一运行内无界 class-repeat”的
// 非空结构。前导 class 必须是后缀 class 的子集，因此同一连续后缀段只需保留最早前导位置，
// 即可满足同一结束位置保留最左起点的事件契约。
func extractByteRegexPrefixRepeat(root *regexNode) (byteRegexPrefixRepeat, bool) {
	if root == nil || root.kind != regexConcat || len(root.children) != 2 {
		return byteRegexPrefixRepeat{}, false
	}
	prefix, ok := extractSingleByteClass(root.children[0])
	if !ok {
		return byteRegexPrefixRepeat{}, false
	}
	repeat := root.children[1]
	if repeat.kind != regexRepeat || len(repeat.children) != 1 || repeat.max != unboundedRepeat {
		return byteRegexPrefixRepeat{}, false
	}
	tail, ok := extractSingleByteClass(repeat.children[0])
	if !ok || !byteClassSubset(prefix, tail) {
		return byteRegexPrefixRepeat{}, false
	}
	return byteRegexPrefixRepeat{prefix: prefix, tail: tail, minimum: repeat.min}, true
}

func byteClassSubset(candidate, container byteClass) bool {
	for index := range candidate {
		if candidate[index]&^container[index] != 0 {
			return false
		}
	}
	return true
}

// advance 返回 offset 后结束的匹配在公开事件契约下唯一可见的起点。投递层只保留每个
// 表达式/终点对的最小有效起点，因此在此产生全部重叠起点只会制造立即丢弃的工作。
func (run *byteRegexRepeatRun) advance(program byteRegexRepeat, value byte, offset int) (int, bool) {
	if !program.class.contains(value) {
		run.reset()
		return 0, false
	}
	if run.length == 0 {
		run.start = offset
	}
	if program.maximum != unboundedRepeat && run.length == program.maximum {
		run.start++
	} else {
		run.length++
	}
	if run.length < program.minimum {
		return 0, false
	}
	return run.start, true
}

func (run *byteRegexRepeatRun) advanceWordBounded(program byteRegexRepeat, value byte, offset int, data []byte) (int, bool) {
	if !program.class.contains(value) {
		run.reset()
		return 0, false
	}
	if run.length == 0 {
		run.start = offset
		run.wordStart = nfaMatchesWordBoundary(data, offset)
	}
	run.length++
	if !run.wordStart || run.length < program.minimum || program.maximum != unboundedRepeat && run.length > program.maximum || !nfaMatchesWordBoundary(data, offset+1) {
		return 0, false
	}
	return run.start, true
}

func (run *byteRegexRepeatRun) reset() {
	run.start = 0
	run.length = 0
	run.wordStart = false
}
