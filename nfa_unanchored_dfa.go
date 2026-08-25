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
	// buildNFAVerifierDFA 将起始闭包固定为状态零。新起点无需先写入 active 再被同一轮
	// 读取；直接推进可减少每个输入字节一次切片写入，并保持旧起点优先的稳定输出顺序。
	state := s.dfa.transitions[uint32(value)]
	if state != nfaDFANoState {
		next = append(next, nfaDFAThread{state: state, start: absoluteOffset})
		if s.dfa.matches[state] {
			matches = append(matches, nfaThread{start: absoluteOffset})
		}
	}
	s.active, s.next = next, s.active[:0]
	s.matches = matches
	return matches
}
