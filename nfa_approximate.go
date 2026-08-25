package scankit

// 有界乘积执行器刻意与普通验证器分离。它每个字节仅消费一次，距离层记录到达每个 Thompson
// NFA 状态所需的替换次数。
const (
	maxHammingNFAWidth    = 256
	maxHammingNFADistance = 64
)

type hammingNFA struct {
	expressionIndex uint32
	program         nfaProgram
	width           int
	distance        uint32
}

type editNFA struct {
	expressionIndex uint32
	program         nfaProgram
	minimumWidth    int
	maximumWidth    int
	distance        uint32
}

// nfaHammingContext 持有固定的 NFA×距离乘积位集。它随 context 创建，即使模式的展开定宽
// 分支过多而无法使用字符类序列执行器，块扫描仍可保持无分配。
type nfaHammingContext struct {
	active   []uint64
	next     []uint64
	words    int
	distance uint32
}

func newNFAHammingContext(program nfaProgram, distance uint32) nfaHammingContext {
	words := (len(program.states) + 63) / 64
	capacity := (int(distance) + 1) * words
	return nfaHammingContext{
		active:   make([]uint64, capacity),
		next:     make([]uint64, capacity),
		words:    words,
		distance: distance,
	}
}

func compileHammingNFA(root *regexNode, distance uint32) (nfaProgram, int, bool, error) {
	if distance > maxHammingNFADistance {
		return nfaProgram{}, 0, false, nil
	}
	analysis, err := analyzeRegex(root)
	if err != nil {
		return nfaProgram{}, 0, false, err
	}
	if analysis.min == 0 || analysis.min != analysis.max || analysis.max > maxHammingNFAWidth {
		return nfaProgram{}, 0, false, nil
	}
	program, err := compileNFA(root)
	if err != nil {
		return nfaProgram{}, 0, false, err
	}
	// 断言和 \R 具有上下文或可变宽语义，不属于汉明替换字母表。定宽乘积路径会刻意拒绝它们，
	// 而非采用任意解释。
	if len(program.states) > maxCachedClosureStates || program.epsilonClosure == nil || programContainsAnchor(program) {
		return nfaProgram{}, 0, false, nil
	}
	for _, state := range program.states {
		switch state.kind {
		case nfaConsume, nfaSplit, nfaMatch:
		default:
			return nfaProgram{}, 0, false, nil
		}
	}
	return program, analysis.min, true, nil
}

// nfaEditContext 使用标准 Levenshtein 乘积构造。在给定输入前缀处，每个 NFA 状态保留
// 一个代价：NFA 分支为零代价边；无输入消费正则字节为删除；跨输入字节保留状态为插入；
// 同时消费二者为精确匹配或替换。同一状态的最小代价支配更大代价，因此工作集由 NFA 状态数
// 而非状态数×距离决定。
type nfaEditContext struct {
	current       []uint16
	next          []uint16
	currentActive []uint32
	nextActive    []uint32
	queue         []uint32
	queued        []bool
	distance      uint16
}

func newNFAEditContext(program nfaProgram, distance uint32) nfaEditContext {
	context := nfaEditContext{
		current:       make([]uint16, len(program.states)),
		next:          make([]uint16, len(program.states)),
		currentActive: make([]uint32, 0, len(program.states)),
		nextActive:    make([]uint32, 0, len(program.states)),
		queue:         make([]uint32, len(program.states)),
		queued:        make([]bool, len(program.states)),
		distance:      uint16(distance),
	}
	context.resetAll(context.current)
	context.resetAll(context.next)
	return context
}

// compileEditNFA 接受有限且有界的字节语言。与汉明距离不同，Levenshtein 距离可比较不同
// 输入长度；只要编译器已将其展开为无环有界 Thompson 图，乘积便支持闭区间宽度范围。
func compileEditNFA(root *regexNode, distance uint32) (nfaProgram, int, int, bool, error) {
	if distance > maxEditLiteralDistance {
		return nfaProgram{}, 0, 0, false, nil
	}
	analysis, err := analyzeRegex(root)
	if err != nil {
		return nfaProgram{}, 0, 0, false, err
	}
	if analysis.min == 0 || analysis.max == unboundedRepeat || analysis.max > maxEditLiteralLength {
		return nfaProgram{}, 0, 0, false, nil
	}
	program, err := compileNFA(root)
	if err != nil {
		return nfaProgram{}, 0, 0, false, err
	}
	if len(program.states) > maxCachedClosureStates || program.epsilonClosure == nil || programContainsAnchor(program) {
		return nfaProgram{}, 0, 0, false, nil
	}
	for _, state := range program.states {
		switch state.kind {
		case nfaConsume, nfaSplit, nfaMatch:
		default:
			return nfaProgram{}, 0, 0, false, nil
		}
	}
	return program, analysis.min, analysis.max, true, nil
}

func (context *nfaEditContext) matches(program nfaProgram, data []byte) bool {
	if len(data) == 0 || len(data) > maxEditLiteralLength || len(context.current) != len(program.states) {
		return false
	}
	context.clearActive(context.current, &context.currentActive)
	context.clearActive(context.next, &context.nextActive)
	context.setCost(context.current, &context.currentActive, program.start, 0)
	context.expand(program, context.current, &context.currentActive)
	for _, value := range data {
		context.clearActive(context.next, &context.nextActive)
		for _, stateIndex := range context.currentActive {
			cost := context.current[stateIndex]
			// 插入：消费一个输入字节但不推进正则。
			if cost < context.distance {
				context.setCost(context.next, &context.nextActive, stateIndex, cost+1)
			}
			state := program.states[stateIndex]
			if state.kind != nfaConsume {
				continue
			}
			nextCost := cost
			if !state.class.contains(value) {
				nextCost++
			}
			if nextCost <= context.distance {
				context.setCost(context.next, &context.nextActive, state.out1, nextCost)
			}
		}
		context.expand(program, context.next, &context.nextActive)
		context.current, context.next = context.next, context.current
		context.currentActive, context.nextActive = context.nextActive, context.currentActive
	}
	return context.current[program.match] <= context.distance
}

func (context *nfaEditContext) resetAll(costs []uint16) {
	for index := range costs {
		costs[index] = context.distance + 1
	}
}

func (context *nfaEditContext) clearActive(costs []uint16, active *[]uint32) {
	for _, index := range *active {
		costs[index] = context.distance + 1
	}
	*active = (*active)[:0]
}

func (context *nfaEditContext) setCost(costs []uint16, active *[]uint32, state uint32, candidate uint16) {
	if candidate >= costs[state] {
		return
	}
	if costs[state] > context.distance {
		*active = append(*active, state)
	}
	costs[state] = candidate
}

func (context *nfaEditContext) expand(program nfaProgram, costs []uint16, active *[]uint32) {
	clear(context.queued)
	head, tail, count := 0, 0, 0
	push := func(state uint32) {
		if context.queued[state] {
			return
		}
		context.queue[tail] = state
		tail = (tail + 1) % len(context.queue)
		count++
		context.queued[state] = true
	}
	for _, state := range *active {
		push(state)
	}
	for count != 0 {
		stateIndex := context.queue[head]
		head = (head + 1) % len(context.queue)
		count--
		context.queued[stateIndex] = false
		cost := costs[stateIndex]
		state := program.states[stateIndex]
		relax := func(target uint32, candidate uint16) {
			if target == nfaNoState || candidate > context.distance {
				return
			}
			previous := costs[target]
			context.setCost(costs, active, target, candidate)
			if candidate >= previous {
				return
			}
			push(target)
		}
		switch state.kind {
		case nfaSplit:
			relax(state.out1, cost)
			relax(state.out2, cost)
		case nfaConsume:
			// 删除：推进正则但不消费输入字节。
			relax(state.out1, cost+1)
		default:
		}
	}
}

func (context *nfaHammingContext) matches(program nfaProgram, data []byte) bool {
	if len(data) == 0 || len(data) > maxHammingNFAWidth || context.words == 0 {
		return false
	}
	clear(context.active)
	clear(context.next)
	context.addClosure(program, context.active[:context.words], program.start)
	for _, value := range data {
		clear(context.next)
		for distance := uint32(0); distance <= context.distance; distance++ {
			states := context.active[int(distance)*context.words : (int(distance)+1)*context.words]
			for stateIndex, state := range program.states {
				if state.kind != nfaConsume || !nfaStateSet(states, uint32(stateIndex)) {
					continue
				}
				nextDistance := distance
				if !state.class.contains(value) {
					nextDistance++
				}
				if nextDistance > context.distance {
					continue
				}
				target := context.next[int(nextDistance)*context.words : (int(nextDistance)+1)*context.words]
				context.addClosure(program, target, state.out1)
			}
		}
		context.active, context.next = context.next, context.active
	}
	for distance := uint32(0); distance <= context.distance; distance++ {
		states := context.active[int(distance)*context.words : (int(distance)+1)*context.words]
		if nfaStateSet(states, program.match) {
			return true
		}
	}
	return false
}

func (context *nfaHammingContext) addClosure(program nfaProgram, states []uint64, start uint32) {
	for _, state := range program.epsilonClosure[start] {
		nfaSetState(states, state)
	}
}
