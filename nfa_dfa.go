package scankit

import "math"

const (
	maxVerifierDFAProgramStates = 1_024
	maxVerifierDFAStates        = 512
	maxVerifierDFAWords         = maxVerifierDFAProgramStates / 64
	nfaDFANoState               = math.MaxUint16
)

// nfaVerifierDFA 仅将小型无断言 NFA 确定化，用于从已知起始偏移匹配。它刻意不编码匹配起点，
// 从而使字面量触发验证的状态空间保持有界。
type nfaVerifierDFA struct {
	transitions      []uint16
	matches          []bool
	tailAssertion    nfaState
	hasTailAssertion bool
}

type nfaVerifierDFAState struct {
	active []uint32
	match  bool
}

func buildNFAVerifierDFA(program nfaProgram) *nfaVerifierDFA {
	if program.epsilonClosure != nil {
		return buildNFAVerifierDFAProgram(program, nfaState{}, false)
	}
	program, tailAssertion, ok := nfaVerifierDFATailAssertionProgram(program)
	if !ok {
		return nil
	}
	return buildNFAVerifierDFAProgram(program, tailAssertion, true)
}

// nfaVerifierDFATailAssertionProgram 将唯一的末尾断言从主体 NFA 中剥离。主体不再依赖
// 输入位置后可确定化；DFA 到达接受状态时再判断原断言，保持字节边界语义。
func nfaVerifierDFATailAssertionProgram(program nfaProgram) (nfaProgram, nfaState, bool) {
	if len(program.states) == 0 || len(program.states) > maxVerifierDFAProgramStates {
		return nfaProgram{}, nfaState{}, false
	}
	predecessor := uint32(nfaNoState)
	references := 0
	for index, state := range program.states {
		if state.out1 == program.match {
			predecessor = uint32(index)
			references++
		}
		if state.kind == nfaSplit && state.out2 == program.match {
			references++
		}
	}
	if references != 1 || predecessor == nfaNoState {
		return nfaProgram{}, nfaState{}, false
	}
	tail := program.states[predecessor]
	if !nfaStateIsDFAEligibleTailAssertion(tail.kind) {
		return nfaProgram{}, nfaState{}, false
	}
	states := append([]nfaState(nil), program.states...)
	states[predecessor] = nfaState{kind: nfaMatch, out1: nfaNoState, out2: nfaNoState}
	program.states = states
	program.match = predecessor
	program.epsilonClosure = buildNFAEpsilonClosuresLimit(program, maxVerifierDFAProgramStates)
	if program.epsilonClosure == nil || nfaMatchesEmptyAt(program, nil, 0) {
		return nfaProgram{}, nfaState{}, false
	}
	return program, tail, true
}

func nfaStateIsDFAEligibleTailAssertion(kind nfaStateKind) bool {
	switch kind {
	case nfaAnchorEnd, nfaAbsoluteEnd, nfaEndBeforeFinalNewline, nfaWordBoundary, nfaNotWordBoundary:
		return true
	default:
		return false
	}
}

func buildNFAVerifierDFAProgram(program nfaProgram, tailAssertion nfaState, hasTailAssertion bool) *nfaVerifierDFA {
	if program.epsilonClosure == nil || len(program.states) > maxVerifierDFAProgramStates {
		return nil
	}
	builder := nfaVerifierDFABuilder{
		program:    program,
		stateIndex: make(map[[maxVerifierDFAWords]uint64]uint16),
	}
	startBits, startActive := builder.closure(program.start)
	if _, ok := builder.addState(startBits, startActive); !ok {
		return nil
	}
	for stateIndex := 0; stateIndex < len(builder.states); stateIndex++ {
		state := builder.states[stateIndex]
		for value := range 256 {
			bits, active := builder.advance(state, byte(value))
			if len(active) == 0 && !nfaVerifierDFABitSet(bits, program.match) {
				continue
			}
			next, ok := builder.addState(bits, active)
			if !ok {
				return nil
			}
			builder.transitions[stateIndex*256+value] = next
		}
	}
	matches := make([]bool, len(builder.states))
	for index, state := range builder.states {
		matches[index] = state.match
	}
	return &nfaVerifierDFA{
		transitions:      builder.transitions,
		matches:          matches,
		tailAssertion:    tailAssertion,
		hasTailAssertion: hasTailAssertion,
	}
}

type nfaVerifierDFABuilder struct {
	program     nfaProgram
	states      []nfaVerifierDFAState
	transitions []uint16
	stateIndex  map[[maxVerifierDFAWords]uint64]uint16
}

func (b *nfaVerifierDFABuilder) closure(start uint32) ([maxVerifierDFAWords]uint64, []uint32) {
	var bits [maxVerifierDFAWords]uint64
	active := make([]uint32, 0, len(b.program.states))
	b.addClosure(&bits, &active, start)
	return bits, active
}

func (b *nfaVerifierDFABuilder) advance(state nfaVerifierDFAState, value byte) ([maxVerifierDFAWords]uint64, []uint32) {
	var bits [maxVerifierDFAWords]uint64
	active := make([]uint32, 0, len(state.active))
	for _, stateIndex := range state.active {
		state := b.program.states[stateIndex]
		if state.class.contains(value) {
			b.addClosure(&bits, &active, state.out1)
		}
	}
	return bits, active
}

func (b *nfaVerifierDFABuilder) addClosure(bits *[maxVerifierDFAWords]uint64, active *[]uint32, start uint32) {
	for _, stateIndex := range b.program.epsilonClosure[start] {
		if nfaVerifierDFABitSet(*bits, stateIndex) {
			continue
		}
		nfaVerifierDFASetBit(bits, stateIndex)
		if b.program.states[stateIndex].kind == nfaConsume {
			*active = append(*active, stateIndex)
		}
	}
}

func (b *nfaVerifierDFABuilder) addState(bits [maxVerifierDFAWords]uint64, active []uint32) (uint16, bool) {
	if stateIndex, ok := b.stateIndex[bits]; ok {
		return stateIndex, true
	}
	if len(b.states) == maxVerifierDFAStates {
		return nfaDFANoState, false
	}
	stateIndex := uint16(len(b.states))
	b.stateIndex[bits] = stateIndex
	b.states = append(b.states, nfaVerifierDFAState{
		active: append([]uint32(nil), active...),
		match:  nfaVerifierDFABitSet(bits, b.program.match),
	})
	transitionStart := len(b.transitions)
	b.transitions = append(b.transitions, make([]uint16, 256)...)
	for index := range b.transitions[transitionStart:] {
		b.transitions[transitionStart+index] = nfaDFANoState
	}
	return stateIndex, true
}

func nfaVerifierDFABitSet(bits [maxVerifierDFAWords]uint64, state uint32) bool {
	return bits[state>>6]&(uint64(1)<<(state&63)) != 0
}

func nfaVerifierDFASetBit(bits *[maxVerifierDFAWords]uint64, state uint32) {
	bits[state>>6] |= uint64(1) << (state & 63)
}
