package scankit

// 该上限同时约束编译期图大小和扫描期活跃状态工作集。图只接受等宽、纯字节分支，
// 候选验证使用固定数组推进，不引入堆分配；超过上限仍安全回退到既有 verifier。
const maxEqualAlternationStates = 64

// byteRegexAlternation 是等宽固定分支的共享决策图。每个节点代表一个输入位置的可达
// 分支集合，公共前缀只保存一次；候选验证时只推进实际可达的稀疏状态，避免逐分支调用
// NFA verifier、扫描所有图状态或把每个分支作为独立执行器扫描。
type byteRegexAlternation struct {
	states   []byteRegexAlternationState
	width    int
	anchor   fixedByteRegexAnchor
	leading  fixedByteRegexBoundary
	trailing fixedByteRegexBoundary
}

type byteRegexAlternationState struct {
	edges  []byteRegexAlternationEdge
	accept bool
}

type byteRegexAlternationEdge struct {
	class byteClass
	next  uint8
}

func extractByteRegexAlternation(root *regexNode) (*byteRegexAlternation, bool) {
	fixed, ok := extractFixedByteRegex(root)
	if !ok || len(fixed.sequences) < 2 || !fixedByteRegexHasSingleWidth(fixed) {
		return nil, false
	}
	width := len(fixed.sequences[0].classes)
	if width == 0 || width > maxFixedByteRegexLength {
		return nil, false
	}
	graph := &byteRegexAlternation{
		states:   []byteRegexAlternationState{{}},
		width:    width,
		leading:  fixed.leadingWordBoundary,
		trailing: fixed.trailingWordBoundary,
	}
	classes := make([]byteClass, width)
	for _, sequence := range fixed.sequences {
		state := uint8(0)
		for offset, class := range sequence.classes {
			classes[offset].merge(class)
			next, found := graph.findEdge(state, class)
			if !found {
				if len(graph.states) == maxEqualAlternationStates {
					return nil, false
				}
				next = uint8(len(graph.states))
				graph.states = append(graph.states, byteRegexAlternationState{})
				graph.states[state].edges = append(graph.states[state].edges, byteRegexAlternationEdge{class: class, next: next})
			}
			state = next
		}
		graph.states[state].accept = true
	}
	graph.anchor = newFixedByteRegexAnchor(classes, mostSelectiveByteClass(classes))
	return graph, true
}

func (graph *byteRegexAlternation) findEdge(state uint8, class byteClass) (uint8, bool) {
	for _, edge := range graph.states[state].edges {
		if edge.class == class {
			return edge.next, true
		}
	}
	return 0, false
}

func (graph byteRegexAlternation) matchesAt(data []byte, start int) bool {
	end := start + graph.width
	if start < 0 || end > len(data) || graph.anchor.checks != nil && !fixedByteRegexAnchorChecksMatch(data, start, graph.anchor.checks) {
		return false
	}
	// 图的边只指向其父状态新建的节点，因此同一层的不同边不会汇合。候选验证无需
	// 位图去重：两个固定数组轮换即可精确保存当前活跃前沿，并把工作量限制为实际可达
	// 分支数而不是图的总状态数。
	var active, next [maxEqualAlternationStates]uint8
	active[0] = 0
	activeCount := 1
	for offset := 0; offset < graph.width; offset++ {
		value := data[start+offset]
		nextCount := 0
		for activeIndex := 0; activeIndex < activeCount; activeIndex++ {
			state := active[activeIndex]
			for _, edge := range graph.states[state].edges {
				if edge.class.contains(value) {
					next[nextCount] = edge.next
					nextCount++
				}
			}
		}
		if nextCount == 0 {
			return false
		}
		active, next = next, active
		activeCount = nextCount
	}
	for index := 0; index < activeCount; index++ {
		if graph.states[active[index]].accept {
			return fixedByteRegexBoundaryMatches(graph.leading, data, start) && fixedByteRegexBoundaryMatches(graph.trailing, data, end)
		}
	}
	return false
}
