package scankit

// nfaUnanchoredDFASchedulerContext 为每个可行匹配起点推进一个紧凑 DFA 状态。与通用
// 调度器不同，它不会在扫描循环中展开 epsilon 闭包或散列 (state,start) 对。起点保持独立，
// 使 Scan 保留全部重叠 Match.From 语义。
type nfaUnanchoredDFASchedulerContext struct {
	dfa     *nfaVerifierDFA
	active  []nfaDFAThread
	next    []nfaDFAThread
	matches []nfaThread
}

type nfaDFAThread struct {
	state uint16
	start uint64
}

func newNFAUnanchoredDFASchedulerContext(dfa *nfaVerifierDFA) *nfaUnanchoredDFASchedulerContext {
	capacity := len(dfa.matches)
	if capacity < 4 {
		capacity = 4
	}
	return &nfaUnanchoredDFASchedulerContext{
		dfa:     dfa,
		active:  make([]nfaDFAThread, 0, capacity),
		next:    make([]nfaDFAThread, 0, capacity),
		matches: make([]nfaThread, 0, capacity),
	}
}

func (s *nfaUnanchoredDFASchedulerContext) reset() {
	s.active = s.active[:0]
	s.next = s.next[:0]
	s.matches = s.matches[:0]
}

func (s *nfaUnanchoredDFASchedulerContext) advance(value byte, absoluteOffset uint64) []nfaThread {
	// buildNFAVerifierDFA 会先插入 program.start 的闭包，因此零是从当前字节开始的候选 DFA 状态。
	s.active = append(s.active, nfaDFAThread{start: absoluteOffset})
	next := s.next[:0]
	matches := s.matches[:0]
	for _, thread := range s.active {
		state := s.dfa.transitions[int(thread.state)<<8|int(value)]
		if state == nfaDFANoState {
			continue
		}
		next = append(next, nfaDFAThread{state: state, start: thread.start})
		if s.dfa.matches[state] {
			matches = append(matches, nfaThread{start: thread.start})
		}
	}
	s.active, s.next = next, s.active[:0]
	s.matches = matches
	return matches
}
