package scankit

import "math"

// nfaThread 标识一个活跃 NFA 状态及其候选匹配的绝对字节起始位置。正则没有字面量预过滤
// 锚点时，必须保留起始位置以满足公开 Match.From 契约。
type nfaThread struct {
	state uint32
	start uint64
}

// nfaSchedulerContext 是可复用、面向块的 NFA 调度器。它在每个输入位置启动一个程序候选，
// 并在每个输入字节推进全部活跃候选一次。与反复调用 matchFrom 不同，候选不会重读创建前的字节。
type nfaSchedulerContext struct {
	active     []nfaThread
	next       []nfaThread
	stack      []nfaThread
	activeSeen map[nfaThread]struct{}
	nextSeen   map[nfaThread]struct{}
	closureGen []uint32
	generation uint32
	leftmost   *nfaLeftmostSchedulerContext
	dfa        *nfaUnanchoredDFASchedulerContext
}

func newNFASchedulerContext(program nfaProgram, leftmost bool) *nfaSchedulerContext {
	if !leftmost && program.verifierDFA != nil {
		return &nfaSchedulerContext{dfa: newNFAUnanchoredDFASchedulerContext(program.verifierDFA)}
	}
	return newNFASchedulerContextNFA(program, leftmost)
}

// newNFASchedulerContextNFA 是包含断言或确定化超出本地 DFA 限制时的有界内存回退路径。
func newNFASchedulerContextNFA(program nfaProgram, leftmost bool) *nfaSchedulerContext {
	capacity := len(program.states)
	context := &nfaSchedulerContext{
		active:     make([]nfaThread, 0, capacity),
		next:       make([]nfaThread, 0, capacity),
		stack:      make([]nfaThread, 0, capacity),
		activeSeen: make(map[nfaThread]struct{}, capacity),
		nextSeen:   make(map[nfaThread]struct{}, capacity),
		closureGen: make([]uint32, len(program.states)),
	}
	if leftmost {
		context.leftmost = newNFALeftmostSchedulerContext(program)
	}
	return context
}

func (s *nfaSchedulerContext) reset() {
	if s.dfa != nil {
		s.dfa.reset()
		return
	}
	if s.leftmost != nil {
		s.leftmost.reset()
		return
	}
	s.active = s.active[:0]
	s.next = s.next[:0]
	s.stack = s.stack[:0]
	clear(s.activeSeen)
	clear(s.nextSeen)
}

// advance 消费 offset 处一个字节，并返回紧随其后结束的全部不同匹配。返回切片别名引用
// context 存储，仅在下一次 advance 调用前有效。
func (s *nfaSchedulerContext) advance(program nfaProgram, value byte, data []byte, offset int, absoluteOffset uint64) []nfaThread {
	if s.dfa != nil {
		return s.dfa.advance(value, absoluteOffset)
	}
	if s.leftmost != nil {
		return s.leftmost.advance(program, value, data, offset, absoluteOffset)
	}
	s.addClosure(program, &s.active, s.activeSeen, nfaThread{state: program.start, start: absoluteOffset}, data, offset)
	clear(s.nextSeen)
	s.next = s.next[:0]
	for _, thread := range s.active {
		state := program.states[thread.state]
		if next, ok := nfaAdvanceState(state, value, data, offset); ok {
			s.addClosure(program, &s.next, s.nextSeen, nfaThread{state: next, start: thread.start}, data, offset+1)
		}
	}
	s.active, s.next = s.next, s.active[:0]
	s.activeSeen, s.nextSeen = s.nextSeen, s.activeSeen

	matched := s.stack[:0]
	for _, thread := range s.active {
		if thread.state == program.match {
			matched = append(matched, thread)
		}
	}
	s.stack = matched
	return matched
}

// nfaLeftmostSchedulerContext 是 SOM_LEFTMOST 调度器。它合并到达同一 NFA 状态的全部路径，
// 仅保留最小起始偏移。这对最左起点报告保持语言不变，并将 \d+ 等无界模式从按起点增长的状态集
// 转化为按程序状态有界的状态集。
type nfaLeftmostSchedulerContext struct {
	active      []uint32
	next        []uint32
	stack       []uint32
	activeStart []uint64
	nextStart   []uint64
	matches     []nfaThread
}

func newNFALeftmostSchedulerContext(program nfaProgram) *nfaLeftmostSchedulerContext {
	starts := make([]uint64, len(program.states))
	nextStarts := make([]uint64, len(program.states))
	for index := range starts {
		starts[index] = math.MaxUint64
		nextStarts[index] = math.MaxUint64
	}
	return &nfaLeftmostSchedulerContext{
		active:      make([]uint32, 0, len(program.states)),
		next:        make([]uint32, 0, len(program.states)),
		stack:       make([]uint32, 0, len(program.states)),
		activeStart: starts,
		nextStart:   nextStarts,
		matches:     make([]nfaThread, 0, 1),
	}
}

func (s *nfaLeftmostSchedulerContext) reset() {
	for _, state := range s.active {
		s.activeStart[state] = math.MaxUint64
	}
	for _, state := range s.next {
		s.nextStart[state] = math.MaxUint64
	}
	s.active = s.active[:0]
	s.next = s.next[:0]
	s.stack = s.stack[:0]
	s.matches = s.matches[:0]
}

func (s *nfaLeftmostSchedulerContext) advance(program nfaProgram, value byte, data []byte, offset int, absoluteOffset uint64) []nfaThread {
	s.addClosure(program, &s.active, s.activeStart, program.start, absoluteOffset, data, offset)
	for _, state := range s.next {
		s.nextStart[state] = math.MaxUint64
	}
	s.next = s.next[:0]
	for _, stateIndex := range s.active {
		state := program.states[stateIndex]
		if next, ok := nfaAdvanceState(state, value, data, offset); ok {
			s.addClosure(program, &s.next, s.nextStart, next, s.activeStart[stateIndex], data, offset+1)
		}
	}
	for _, state := range s.active {
		s.activeStart[state] = math.MaxUint64
	}
	s.active, s.next = s.next, s.active[:0]
	s.activeStart, s.nextStart = s.nextStart, s.activeStart
	s.matches = s.matches[:0]
	if start := s.activeStart[program.match]; start != math.MaxUint64 {
		s.matches = append(s.matches, nfaThread{state: program.match, start: start})
	}
	return s.matches
}

func (s *nfaLeftmostSchedulerContext) addClosure(program nfaProgram, target *[]uint32, starts []uint64, start uint32, matchStart uint64, data []byte, offset int) {
	if start == nfaNoState {
		return
	}
	s.stack = append(s.stack, start)
	for len(s.stack) != 0 {
		stateIndex := s.stack[len(s.stack)-1]
		s.stack = s.stack[:len(s.stack)-1]
		if stateIndex == nfaNoState || starts[stateIndex] <= matchStart {
			continue
		}
		starts[stateIndex] = matchStart
		*target = append(*target, stateIndex)
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
		default:
		}
	}
}

func (s *nfaSchedulerContext) addClosure(program nfaProgram, target *[]nfaThread, seen map[nfaThread]struct{}, start nfaThread, data []byte, offset int) {
	if program.epsilonClosure != nil {
		for _, stateIndex := range program.epsilonClosure[start.state] {
			thread := nfaThread{state: stateIndex, start: start.start}
			if _, exists := seen[thread]; exists {
				continue
			}
			seen[thread] = struct{}{}
			*target = append(*target, thread)
		}
		return
	}
	s.generation++
	if s.generation == 0 {
		clear(s.closureGen)
		s.generation = 1
	}
	generation := s.generation
	s.stack = append(s.stack[:0], start)
	for len(s.stack) != 0 {
		thread := s.stack[len(s.stack)-1]
		s.stack = s.stack[:len(s.stack)-1]
		if thread.state == nfaNoState || s.closureGen[thread.state] == generation {
			continue
		}
		s.closureGen[thread.state] = generation
		state := program.states[thread.state]
		switch state.kind {
		case nfaSplit:
			s.stack = append(s.stack, nfaThread{state: state.out1, start: thread.start}, nfaThread{state: state.out2, start: thread.start})
		case nfaAnchorStart:
			if nfaMatchesStartAnchor(state.multiline, data, offset) {
				s.stack = append(s.stack, nfaThread{state: state.out1, start: thread.start})
			}
		case nfaAnchorEnd:
			if nfaMatchesEndAnchor(state.multiline, data, offset) {
				s.stack = append(s.stack, nfaThread{state: state.out1, start: thread.start})
			}
		case nfaAbsoluteStart:
			if offset == 0 {
				s.stack = append(s.stack, nfaThread{state: state.out1, start: thread.start})
			}
		case nfaAbsoluteEnd:
			if offset == len(data) {
				s.stack = append(s.stack, nfaThread{state: state.out1, start: thread.start})
			}
		case nfaEndBeforeFinalNewline:
			if nfaMatchesEndBeforeFinalNewline(data, offset) {
				s.stack = append(s.stack, nfaThread{state: state.out1, start: thread.start})
			}
		case nfaWordBoundary:
			if nfaMatchesWordBoundary(data, offset) {
				s.stack = append(s.stack, nfaThread{state: state.out1, start: thread.start})
			}
		case nfaNotWordBoundary:
			if !nfaMatchesWordBoundary(data, offset) {
				s.stack = append(s.stack, nfaThread{state: state.out1, start: thread.start})
			}
		default:
			if _, exists := seen[thread]; !exists {
				seen[thread] = struct{}{}
				*target = append(*target, thread)
			}
		}
	}
}
