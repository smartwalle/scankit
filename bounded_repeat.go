package scankit

const maxBoundedRepeatWidth = 256

// byteRegexBoundedRepeat 是“固定前缀 + 单字节类有限重复 + 固定后缀”的紧凑执行器。
// 它不展开每个合法宽度，也不维护逐字节 NFA 线程；每个候选仍逐一验证全部固定位置和
// 重复区间，并产生每一个合法结束位置。
type byteRegexBoundedRepeat struct {
	prefix              []byteClass
	repeat              byteClass
	minimum             int
	maximum             int
	suffix              []byteClass
	trigger             byteClass
	triggerOffset       int
	triggerInSuffix     bool
	suffixTriggerOffset int
}

func extractByteRegexBoundedRepeat(root *regexNode) (*byteRegexBoundedRepeat, bool) {
	if root == nil || root.kind != regexConcat || len(root.children) < 2 {
		return nil, false
	}
	repeatIndex := -1
	var repeatClass byteClass
	minimum, maximum := 0, 0
	for index, child := range root.children {
		if child.kind != regexRepeat || len(child.children) != 1 || child.min < 1 || child.min == child.max || child.max == unboundedRepeat || child.max > maxBoundedRepeatWidth {
			continue
		}
		class, ok := extractSingleByteClass(child.children[0])
		if !ok || repeatIndex >= 0 {
			return nil, false
		}
		repeatIndex, repeatClass, minimum, maximum = index, class, child.min, child.max
	}
	if repeatIndex < 0 {
		return nil, false
	}
	prefix := make([]byteClass, 0, repeatIndex)
	for _, child := range root.children[:repeatIndex] {
		class, ok := extractSingleByteClass(child)
		if !ok {
			return nil, false
		}
		prefix = append(prefix, class)
	}
	suffix := make([]byteClass, 0, len(root.children)-repeatIndex-1)
	for _, child := range root.children[repeatIndex+1:] {
		class, ok := extractSingleByteClass(child)
		if !ok {
			return nil, false
		}
		suffix = append(suffix, class)
	}
	if len(prefix) == 0 && len(suffix) == 0 {
		return nil, false
	}
	program := &byteRegexBoundedRepeat{prefix: prefix, repeat: repeatClass, minimum: minimum, maximum: maximum, suffix: suffix}
	program.selectTrigger()
	return program, true
}

func (program *byteRegexBoundedRepeat) selectTrigger() {
	found := false
	bestSize := 0
	for index, class := range program.prefix {
		size := byteClassSize(class)
		if !found || size < bestSize {
			program.trigger, program.triggerOffset, program.triggerInSuffix, program.suffixTriggerOffset = class, index, false, 0
			bestSize, found = size, true
		}
	}
	for index, class := range program.suffix {
		size := byteClassSize(class)
		if !found || size < bestSize {
			program.trigger, program.triggerOffset, program.triggerInSuffix, program.suffixTriggerOffset = class, index, true, index
			bestSize, found = size, true
		}
	}
}

func (program byteRegexBoundedRepeat) appendMatches(data []byte, offset int, matches []fixedByteRegexMatch) []fixedByteRegexMatch {
	if !program.trigger.contains(data[offset]) {
		return matches
	}
	if !program.triggerInSuffix {
		start := offset - program.triggerOffset
		if start < 0 {
			return matches
		}
		return program.appendMatchesFrom(data, start, matches)
	}
	for width := program.minimum; width <= program.maximum; width++ {
		start := offset - len(program.prefix) - width - program.suffixTriggerOffset
		if start < 0 {
			continue
		}
		end := start + len(program.prefix) + width + len(program.suffix)
		if end > len(data) || !program.matchesAt(data, start, width) {
			continue
		}
		matches = append(matches, fixedByteRegexMatch{start: start, end: end})
	}
	return matches
}

func (program byteRegexBoundedRepeat) appendMatchesFrom(data []byte, start int, matches []fixedByteRegexMatch) []fixedByteRegexMatch {
	for width := program.minimum; width <= program.maximum; width++ {
		end := start + len(program.prefix) + width + len(program.suffix)
		if end > len(data) {
			break
		}
		if program.matchesAt(data, start, width) {
			matches = append(matches, fixedByteRegexMatch{start: start, end: end})
		}
	}
	return matches
}

func (program byteRegexBoundedRepeat) matchesAt(data []byte, start, width int) bool {
	if start < 0 || width < program.minimum || width > program.maximum {
		return false
	}
	offset := start
	for _, class := range program.prefix {
		if offset >= len(data) || !class.contains(data[offset]) {
			return false
		}
		offset++
	}
	for end := offset + width; offset < end; offset++ {
		if offset >= len(data) || !program.repeat.contains(data[offset]) {
			return false
		}
	}
	for _, class := range program.suffix {
		if offset >= len(data) || !class.contains(data[offset]) {
			return false
		}
		offset++
	}
	return true
}
