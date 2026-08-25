package scankit

// nfaVerifierContext 持有一个 NFA 验证器的可变位集和闭包栈。它可供固定程序复用，
// 但不能并发调用。
type nfaVerifierContext struct {
	active       []uint64
	next         []uint64
	activeStates []uint32
	nextStates   []uint32
	stack        []uint32
	ends         []int
}

func newNFAVerifierContext(program nfaProgram) *nfaVerifierContext {
	words := (len(program.states) + 63) / 64
	return &nfaVerifierContext{
		active:       make([]uint64, words),
		next:         make([]uint64, words),
		activeStates: make([]uint32, 0, len(program.states)),
		nextStates:   make([]uint32, 0, len(program.states)),
		stack:        make([]uint32, 0, len(program.states)),
		ends:         make([]int, 0, 4),
	}
}

// nfaMatchFrom 从已知输入偏移执行已编译 NFA，并返回全部非空匹配的结束偏移。它是字面量触发
// 正则路径的验证原语，不会在每个输入字节重新启动程序。
func nfaMatchFrom(program nfaProgram, data []byte, start int) []int {
	return newNFAVerifierContext(program).matchFrom(program, data, start)
}

func (s *nfaVerifierContext) matchFrom(program nfaProgram, data []byte, start int) []int {
	return s.matchFromLimit(program, data, start, len(data)-start)
}

// matchFromLimit 仅将验证器运行到 start+maximumLength。正则分析器会为字面量触发表达式
// 提供该精确上界，从而避免最终可能匹配结束位置之后继续遍历大块输入。
func (s *nfaVerifierContext) matchFromLimit(program nfaProgram, data []byte, start, maximumLength int) []int {
	if start < 0 || start > len(data) {
		return nil
	}
	end := len(data)
	if maximumLength >= 0 && maximumLength < end-start {
		end = start + maximumLength
	}
	if program.verifierDFA != nil {
		return s.matchFromDFA(program.verifierDFA, data, start, end)
	}
	clear(s.active)
	clear(s.next)
	s.ends = s.ends[:0]
	s.activeStates = s.activeStates[:0]
	s.nextStates = s.nextStates[:0]
	s.addClosure(program, s.active, &s.activeStates, program.start, data, start)

	for offset := start; offset < end; offset++ {
		clear(s.next)
		s.nextStates = s.nextStates[:0]
		for _, stateIndex := range s.activeStates {
			state := program.states[stateIndex]
			next, ok := nfaAdvanceState(state, data[offset], data, offset)
			if !ok {
				continue
			}
			s.addClosure(program, s.next, &s.nextStates, next, data, offset+1)
		}
		s.active, s.next = s.next, s.active
		s.activeStates, s.nextStates = s.nextStates, s.activeStates
		if nfaStateSet(s.active, program.match) {
			s.ends = append(s.ends, offset+1)
		}
		if len(s.activeStates) == 0 {
			break
		}
	}
	if len(s.ends) == 0 {
		return nil
	}
	return s.ends
}

func (s *nfaVerifierContext) matchFromDFA(dfa *nfaVerifierDFA, data []byte, start, end int) []int {
	return s.matchFromDFAState(dfa, data, start, end, 0)
}

// matchFromDFAState 从已知 DFA 状态继续验证。锚定调用方会先独立验证有界固定字符类前缀，
// 因此剩余输入从字面量触发器而非表达式起点开始。
func (s *nfaVerifierContext) matchFromDFAState(dfa *nfaVerifierDFA, data []byte, start, end int, state uint16) []int {
	s.ends = s.ends[:0]
	if dfa.hasTailAssertion {
		switch dfa.tailAssertion.kind {
		case nfaWordBoundary:
			return s.matchFromDFAStateWordBoundary(dfa, data, start, end, state, false)
		case nfaNotWordBoundary:
			return s.matchFromDFAStateWordBoundary(dfa, data, start, end, state, true)
		}
	}
	for offset := start; offset < end; offset++ {
		state = dfa.transitions[uint32(state)<<8|uint32(data[offset])]
		if state == nfaDFANoState {
			break
		}
		if dfa.matches[state] && (!dfa.hasTailAssertion || nfaMatchesTailAssertion(dfa.tailAssertion, data, offset+1)) {
			s.ends = append(s.ends, offset+1)
		}
	}
	if len(s.ends) == 0 {
		return nil
	}
	return s.ends
}

func (s *nfaVerifierContext) matchFromDFAStateWordBoundary(dfa *nfaVerifierDFA, data []byte, start, end int, state uint16, invert bool) []int {
	for offset := start; offset < end; offset++ {
		state = dfa.transitions[uint32(state)<<8|uint32(data[offset])]
		if state == nfaDFANoState {
			break
		}
		if dfa.matches[state] && nfaMatchesWordBoundary(data, offset+1) != invert {
			s.ends = append(s.ends, offset+1)
		}
	}
	if len(s.ends) == 0 {
		return nil
	}
	return s.ends
}

func nfaMatchesTailAssertion(state nfaState, data []byte, offset int) bool {
	switch state.kind {
	case nfaAnchorEnd:
		return nfaMatchesEndAnchor(state.multiline, data, offset)
	case nfaAbsoluteEnd:
		return offset == len(data)
	case nfaEndBeforeFinalNewline:
		return nfaMatchesEndBeforeFinalNewline(data, offset)
	case nfaWordBoundary:
		return nfaMatchesWordBoundary(data, offset)
	case nfaNotWordBoundary:
		return !nfaMatchesWordBoundary(data, offset)
	default:
		return false
	}
}

func (s *nfaVerifierContext) addClosure(program nfaProgram, states []uint64, activeStates *[]uint32, start uint32, data []byte, offset int) {
	if start == nfaNoState {
		return
	}
	if program.epsilonClosure != nil {
		for _, stateIndex := range program.epsilonClosure[start] {
			if nfaStateSet(states, stateIndex) {
				continue
			}
			nfaSetState(states, stateIndex)
			if nfaStateConsumes(program.states[stateIndex].kind) {
				*activeStates = append(*activeStates, stateIndex)
			}
		}
		return
	}
	s.stack = append(s.stack, start)
	for len(s.stack) != 0 {
		stateIndex := s.stack[len(s.stack)-1]
		s.stack = s.stack[:len(s.stack)-1]
		if stateIndex == nfaNoState || nfaStateSet(states, stateIndex) {
			continue
		}
		nfaSetState(states, stateIndex)
		state := program.states[stateIndex]
		switch state.kind {
		case nfaSplit:
			s.stack = append(s.stack, state.out1, state.out2)
		case nfaAnchorStart:
			if nfaMatchesStartAnchor(state.multiline, data, offset) {
				s.stack = append(s.stack, state.out1)
			}
		case nfaAnchorEnd:
			if nfaMatchesEndAnchor(state.multiline, data, offset) {
				s.stack = append(s.stack, state.out1)
			}
		case nfaAbsoluteStart:
			if offset == 0 {
				s.stack = append(s.stack, state.out1)
			}
		case nfaAbsoluteEnd:
			if offset == len(data) {
				s.stack = append(s.stack, state.out1)
			}
		case nfaEndBeforeFinalNewline:
			if nfaMatchesEndBeforeFinalNewline(data, offset) {
				s.stack = append(s.stack, state.out1)
			}
		case nfaWordBoundary:
			if nfaMatchesWordBoundary(data, offset) {
				s.stack = append(s.stack, state.out1)
			}
		case nfaNotWordBoundary:
			if !nfaMatchesWordBoundary(data, offset) {
				s.stack = append(s.stack, state.out1)
			}
		case nfaConsume, nfaLineBreak, nfaLineBreakCR:
			*activeStates = append(*activeStates, stateIndex)
		default:
		}
	}
}

func nfaStateConsumes(kind nfaStateKind) bool {
	return kind == nfaConsume || kind == nfaLineBreak || kind == nfaLineBreakCR
}

func nfaAdvanceState(state nfaState, value byte, data []byte, offset int) (uint32, bool) {
	switch state.kind {
	case nfaConsume:
		return state.out1, state.class.contains(value)
	case nfaLineBreak:
		switch value {
		case '\r':
			if offset+1 < len(data) && data[offset+1] == '\n' {
				return state.out2, true
			}
			return state.out1, true
		case '\n', '\v', '\f':
			return state.out1, true
		}
	case nfaLineBreakCR:
		return state.out1, value == '\n'
	default:
	}
	return nfaNoState, false
}

func nfaMatchesStartAnchor(multiline bool, data []byte, offset int) bool {
	return offset == 0 || multiline && offset > 0 && data[offset-1] == '\n'
}

func nfaMatchesEndAnchor(multiline bool, data []byte, offset int) bool {
	return offset == len(data) || multiline && offset < len(data) && data[offset] == '\n'
}

// nfaMatchesEndBeforeFinalNewline 实现 \\Z 边界：绝对结尾，或最后一个 LF/CRLF 序列之前的位置。
// 此字节扫描器不支持可配置换行约定。
func nfaMatchesEndBeforeFinalNewline(data []byte, offset int) bool {
	return offset == len(data) ||
		offset+1 == len(data) && data[offset] == '\n' ||
		offset+2 == len(data) && data[offset] == '\r' && data[offset+1] == '\n'
}

// nfaMatchesWordBoundary 实现 \b 和 \B 使用的面向字节 ASCII 边界。未启用 UTF-8/UCP 时，
// 单词成员严格为 [A-Za-z0-9_]。
func nfaMatchesWordBoundary(data []byte, offset int) bool {
	previousWord := offset > 0 && isASCIIWordByte(data[offset-1])
	nextWord := offset < len(data) && isASCIIWordByte(data[offset])
	return previousWord != nextWord
}

// nfaMatchesEmptyAt 仅在一个边界计算 epsilon 与断言转移，用于 AllowEmpty 报告。它不会
// 跟随消费状态，因此不会将非空分支误判为零宽匹配。
func nfaMatchesEmptyAt(program nfaProgram, data []byte, offset int) bool {
	if offset < 0 || offset > len(data) {
		return false
	}
	seen := make([]bool, len(program.states))
	stack := []uint32{program.start}
	for len(stack) != 0 {
		stateIndex := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if stateIndex == nfaNoState || seen[stateIndex] {
			continue
		}
		seen[stateIndex] = true
		if stateIndex == program.match {
			return true
		}
		state := program.states[stateIndex]
		switch state.kind {
		case nfaSplit:
			stack = append(stack, state.out1, state.out2)
		case nfaAnchorStart:
			if nfaMatchesStartAnchor(state.multiline, data, offset) {
				stack = append(stack, state.out1)
			}
		case nfaAnchorEnd:
			if nfaMatchesEndAnchor(state.multiline, data, offset) {
				stack = append(stack, state.out1)
			}
		case nfaAbsoluteStart:
			if offset == 0 {
				stack = append(stack, state.out1)
			}
		case nfaAbsoluteEnd:
			if offset == len(data) {
				stack = append(stack, state.out1)
			}
		case nfaEndBeforeFinalNewline:
			if nfaMatchesEndBeforeFinalNewline(data, offset) {
				stack = append(stack, state.out1)
			}
		case nfaWordBoundary:
			if nfaMatchesWordBoundary(data, offset) {
				stack = append(stack, state.out1)
			}
		case nfaNotWordBoundary:
			if !nfaMatchesWordBoundary(data, offset) {
				stack = append(stack, state.out1)
			}
		default:
		}
	}
	return false
}

func isASCIIWordByte(value byte) bool {
	return value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z' || value >= '0' && value <= '9' || value == '_'
}

func nfaStateSet(states []uint64, state uint32) bool {
	return states[state>>6]&(uint64(1)<<(state&63)) != 0
}

func nfaSetState(states []uint64, state uint32) {
	states[state>>6] |= uint64(1) << (state & 63)
}
