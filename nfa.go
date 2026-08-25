package scankit

import "math"

const (
	nfaNoState              = math.MaxUint32
	maxNFAStates            = 1 << 16
	maxBoundedRepetition    = 4_096
	maxCachedClosureStates  = 512
	maxCachedClosureEntries = 32 << 10
)

type nfaStateKind uint8

const (
	nfaConsume nfaStateKind = iota
	nfaSplit
	nfaAnchorStart
	nfaAnchorEnd
	nfaAbsoluteStart
	nfaAbsoluteEnd
	nfaEndBeforeFinalNewline
	nfaLineBreak
	nfaLineBreakCR
	nfaWordBoundary
	nfaNotWordBoundary
	nfaMatch
)

// nfaState 是编译后的不可变 Thompson NFA 状态。消费状态使用 class，分支和断言状态仅使用出边。
type nfaState struct {
	kind      nfaStateKind
	class     byteClass
	multiline bool
	out1      uint32
	out2      uint32
}

type nfaProgram struct {
	states         []nfaState
	start          uint32
	match          uint32
	multiline      bool
	epsilonClosure [][]uint32
	verifierDFA    *nfaVerifierDFA
}

type nfaPatch struct {
	state uint32
	slot  uint8
}

type nfaFragment struct {
	start uint32
	outs  []nfaPatch
}

type nfaCompiler struct {
	states []nfaState
}

func compileNFA(root *regexNode) (nfaProgram, error) {
	compiler := nfaCompiler{states: make([]nfaState, 0, 32)}
	fragment, err := compiler.compile(root)
	if err != nil {
		return nfaProgram{}, err
	}
	match, err := compiler.addState(nfaState{kind: nfaMatch, out1: nfaNoState, out2: nfaNoState})
	if err != nil {
		return nfaProgram{}, err
	}
	compiler.patch(fragment.outs, match)
	program := nfaProgram{states: compiler.states, start: fragment.start, match: match}
	program.epsilonClosure = buildNFAEpsilonClosures(program)
	program.verifierDFA = buildNFAVerifierDFA(program)
	return program, nil
}

// nfaProgramFingerprint 精确标识调度器或验证器使用的不可变 NFA 状态图。它刻意采用结构
// 标识而不判定一般正则语言等价：仅当编译产生相同可执行程序时，两个源表达式才共享执行。
func nfaProgramFingerprint(program nfaProgram) string {
	encoded := make([]byte, 0, 9+len(program.states)*41)
	if program.multiline {
		encoded = append(encoded, 1)
	} else {
		encoded = append(encoded, 0)
	}
	encoded = appendNFAFingerprintUint32(encoded, program.start)
	encoded = appendNFAFingerprintUint32(encoded, program.match)
	for _, state := range program.states {
		encoded = append(encoded, byte(state.kind))
		if state.multiline {
			encoded = append(encoded, 1)
		} else {
			encoded = append(encoded, 0)
		}
		for _, word := range state.class {
			encoded = appendNFAFingerprintUint64(encoded, word)
		}
		encoded = appendNFAFingerprintUint32(encoded, state.out1)
		encoded = appendNFAFingerprintUint32(encoded, state.out2)
	}
	return string(encoded)
}

func appendNFAFingerprintUint32(target []byte, value uint32) []byte {
	return append(target, byte(value), byte(value>>8), byte(value>>16), byte(value>>24))
}

func appendNFAFingerprintUint64(target []byte, value uint64) []byte {
	return append(target,
		byte(value), byte(value>>8), byte(value>>16), byte(value>>24),
		byte(value>>32), byte(value>>40), byte(value>>48), byte(value>>56),
	)
}

// buildNFAEpsilonClosures 为小型无断言程序缓存可经分支节点到达的终止状态。它刻意保持有界：
// 这是常见规则的吞吐缓存，而非病态 NFA 图的无界编译期展开。
func buildNFAEpsilonClosures(program nfaProgram) [][]uint32 {
	return buildNFAEpsilonClosuresLimit(program, maxCachedClosureStates)
}

func buildNFAEpsilonClosuresLimit(program nfaProgram, maximumStates int) [][]uint32 {
	if len(program.states) == 0 || len(program.states) > maximumStates || programContainsAnchor(program) {
		return nil
	}
	closures := make([][]uint32, len(program.states))
	seen := make([]uint32, len(program.states))
	stack := make([]uint32, 0, len(program.states))
	var generation uint32
	entries := 0
	for start := range program.states {
		generation++
		if generation == 0 {
			clear(seen)
			generation = 1
		}
		stack = append(stack[:0], uint32(start))
		closure := make([]uint32, 0, 2)
		for len(stack) != 0 {
			stateIndex := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if stateIndex == nfaNoState || seen[stateIndex] == generation {
				continue
			}
			seen[stateIndex] = generation
			state := program.states[stateIndex]
			if state.kind == nfaSplit {
				stack = append(stack, state.out1, state.out2)
				continue
			}
			closure = append(closure, stateIndex)
			entries++
			if entries > maxCachedClosureEntries {
				return nil
			}
		}
		closures[start] = closure
	}
	return closures
}

func (c *nfaCompiler) compile(node *regexNode) (nfaFragment, error) {
	switch node.kind {
	case regexEmpty:
		return c.epsilon()
	case regexLiteral:
		var class byteClass
		class.add(node.literal)
		return c.consume(class)
	case regexClass:
		return c.consume(node.class)
	case regexDot:
		return nfaFragment{}, ErrUnsupportedExpression
	case regexAnchorStart:
		return c.assertion(nfaAnchorStart, node.flags)
	case regexAnchorEnd:
		return c.assertion(nfaAnchorEnd, node.flags)
	case regexAbsoluteStart:
		return c.assertion(nfaAbsoluteStart, node.flags)
	case regexAbsoluteEnd:
		return c.assertion(nfaAbsoluteEnd, node.flags)
	case regexEndBeforeFinalNewline:
		return c.assertion(nfaEndBeforeFinalNewline, node.flags)
	case regexLineBreak:
		return c.lineBreak()
	case regexWordBoundary:
		return c.assertion(nfaWordBoundary, node.flags)
	case regexNotWordBoundary:
		return c.assertion(nfaNotWordBoundary, node.flags)
	case regexConcat:
		return c.compileConcat(node.children)
	case regexAlternate:
		return c.compileAlternate(node.children)
	case regexRepeat:
		return c.compileRepeat(node)
	default:
		return nfaFragment{}, ErrUnsupportedExpression
	}
}

func (c *nfaCompiler) lineBreak() (nfaFragment, error) {
	line, err := c.addState(nfaState{kind: nfaLineBreak, out1: nfaNoState, out2: nfaNoState})
	if err != nil {
		return nfaFragment{}, err
	}
	cr, err := c.addState(nfaState{kind: nfaLineBreakCR, out1: nfaNoState, out2: nfaNoState})
	if err != nil {
		return nfaFragment{}, err
	}
	c.states[line].out2 = cr
	return nfaFragment{start: line, outs: []nfaPatch{{state: line, slot: 1}, {state: cr, slot: 1}}}, nil
}

func (c *nfaCompiler) epsilon() (nfaFragment, error) {
	state, err := c.addState(nfaState{kind: nfaSplit, out1: nfaNoState, out2: nfaNoState})
	if err != nil {
		return nfaFragment{}, err
	}
	return nfaFragment{start: state, outs: []nfaPatch{{state: state, slot: 1}}}, nil
}

func (c *nfaCompiler) consume(class byteClass) (nfaFragment, error) {
	state, err := c.addState(nfaState{kind: nfaConsume, class: class, out1: nfaNoState, out2: nfaNoState})
	if err != nil {
		return nfaFragment{}, err
	}
	return nfaFragment{start: state, outs: []nfaPatch{{state: state, slot: 1}}}, nil
}

func (c *nfaCompiler) assertion(kind nfaStateKind, flags CompileFlag) (nfaFragment, error) {
	state, err := c.addState(nfaState{kind: kind, multiline: flags&CompileMultiline != 0, out1: nfaNoState, out2: nfaNoState})
	if err != nil {
		return nfaFragment{}, err
	}
	return nfaFragment{start: state, outs: []nfaPatch{{state: state, slot: 1}}}, nil
}

func (c *nfaCompiler) compileConcat(children []*regexNode) (nfaFragment, error) {
	if len(children) == 0 {
		return c.epsilon()
	}
	result, err := c.compile(children[0])
	if err != nil {
		return nfaFragment{}, err
	}
	for _, child := range children[1:] {
		next, err := c.compile(child)
		if err != nil {
			return nfaFragment{}, err
		}
		c.patch(result.outs, next.start)
		result.outs = next.outs
	}
	return result, nil
}

func (c *nfaCompiler) compileAlternate(children []*regexNode) (nfaFragment, error) {
	if len(children) == 0 {
		return c.epsilon()
	}
	result, err := c.compile(children[0])
	if err != nil {
		return nfaFragment{}, err
	}
	for _, child := range children[1:] {
		next, err := c.compile(child)
		if err != nil {
			return nfaFragment{}, err
		}
		split, err := c.addState(nfaState{kind: nfaSplit, out1: result.start, out2: next.start})
		if err != nil {
			return nfaFragment{}, err
		}
		result = nfaFragment{
			start: split,
			outs:  append(result.outs, next.outs...),
		}
	}
	return result, nil
}

func (c *nfaCompiler) compileRepeat(node *regexNode) (nfaFragment, error) {
	if len(node.children) != 1 {
		return nfaFragment{}, ErrUnsupportedExpression
	}
	if node.min > maxBoundedRepetition || (node.max != unboundedRepeat && node.max > maxBoundedRepetition) {
		return nfaFragment{}, ErrRegexTooComplex
	}

	result, err := c.epsilon()
	if err != nil {
		return nfaFragment{}, err
	}
	for range node.min {
		occurrence, err := c.compile(node.children[0])
		if err != nil {
			return nfaFragment{}, err
		}
		c.patch(result.outs, occurrence.start)
		result.outs = occurrence.outs
	}
	if node.max == node.min {
		return result, nil
	}
	if node.max == unboundedRepeat {
		loop, err := c.star(node.children[0])
		if err != nil {
			return nfaFragment{}, err
		}
		c.patch(result.outs, loop.start)
		result.outs = loop.outs
		return result, nil
	}
	for range node.max - node.min {
		optional, err := c.optional(node.children[0])
		if err != nil {
			return nfaFragment{}, err
		}
		c.patch(result.outs, optional.start)
		result.outs = optional.outs
	}
	return result, nil
}

func (c *nfaCompiler) star(node *regexNode) (nfaFragment, error) {
	child, err := c.compile(node)
	if err != nil {
		return nfaFragment{}, err
	}
	split, err := c.addState(nfaState{kind: nfaSplit, out1: child.start, out2: nfaNoState})
	if err != nil {
		return nfaFragment{}, err
	}
	c.patch(child.outs, split)
	return nfaFragment{start: split, outs: []nfaPatch{{state: split, slot: 2}}}, nil
}

func (c *nfaCompiler) optional(node *regexNode) (nfaFragment, error) {
	child, err := c.compile(node)
	if err != nil {
		return nfaFragment{}, err
	}
	split, err := c.addState(nfaState{kind: nfaSplit, out1: child.start, out2: nfaNoState})
	if err != nil {
		return nfaFragment{}, err
	}
	return nfaFragment{
		start: split,
		outs:  append(child.outs, nfaPatch{state: split, slot: 2}),
	}, nil
}

func (c *nfaCompiler) addState(state nfaState) (uint32, error) {
	if len(c.states) == maxNFAStates {
		return 0, ErrRegexTooComplex
	}
	index := uint32(len(c.states))
	c.states = append(c.states, state)
	return index, nil
}

func (c *nfaCompiler) patch(patches []nfaPatch, target uint32) {
	for _, patch := range patches {
		if patch.slot == 1 {
			c.states[patch.state].out1 = target
			continue
		}
		c.states[patch.state].out2 = target
	}
}
