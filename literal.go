package scankit

import "sort"

// denseLiteralTransitionLimit 限制完整转移矩阵的常驻内存。稀疏边仅用于特别大的纯字面量
// 数据库：稠密查询对普通规则集明显更快，稀疏存储则避免数据库占用数 GB 内存。
const denseLiteralTransitionLimit = 32 << 20

// literalAutomaton 是完整的面向字节 Aho-Corasick 自动机。转移矩阵刻意与稀疏输出元数据
// 分离，使扫描循环无需从每个 1 KiB 转移行加载输出字段。
type literalAutomaton struct {
	transitions   []uint32
	outputStart   []uint32
	outputEnd     []uint32
	outputs       []uint32
	sparse        bool
	sparseStates  []literalSparseState
	sparseEdges   []literalSparseEdge
	rootByteFast  bool
	rootByteCount uint8
	rootByteVals  [8]byte
}

type literalSparseState struct {
	failure   uint32
	edgeStart uint32
	edgeEnd   uint32
}

type literalSparseEdge struct {
	value byte
	next  uint32
}

type literalBuilder struct {
	nodes []literalBuildNode
}

type literalBuildNode struct {
	next    [256]uint32
	edges   [4]uint64
	failure uint32
	outputs []uint32
}

func newLiteralBuilder() literalBuilder {
	return literalBuilder{nodes: []literalBuildNode{{}}}
}

func (b *literalBuilder) add(pattern []byte, expressionIndex uint32) {
	state := uint32(0)
	for _, c := range pattern {
		next := b.nodes[state].next[c]
		if next == 0 {
			b.nodes = append(b.nodes, literalBuildNode{})
			next = uint32(len(b.nodes) - 1)
			b.nodes[state].next[c] = next
			b.nodes[state].edges[c>>6] |= uint64(1) << (c & 63)
		}
		state = next
	}
	b.nodes[state].outputs = append(b.nodes[state].outputs, expressionIndex)
}

func (b *literalBuilder) freeze() literalAutomaton {
	queue := make([]uint32, 0, len(b.nodes))
	for c := range 256 {
		next := b.nodes[0].next[byte(c)]
		if next != 0 {
			queue = append(queue, next)
		}
	}

	for head := 0; head < len(queue); head++ {
		state := queue[head]
		for c := range 256 {
			byteValue := byte(c)
			next := b.nodes[state].next[byteValue]
			if next == 0 {
				b.nodes[state].next[byteValue] = b.nodes[b.nodes[state].failure].next[byteValue]
				continue
			}

			failure := b.nodes[b.nodes[state].failure].next[byteValue]
			b.nodes[next].failure = failure
			b.nodes[next].outputs = append(b.nodes[next].outputs, b.nodes[failure].outputs...)
			queue = append(queue, next)
		}
	}

	automaton := literalAutomaton{
		outputStart: make([]uint32, len(b.nodes)),
		outputEnd:   make([]uint32, len(b.nodes)),
		sparse:      len(b.nodes)*256*4 > denseLiteralTransitionLimit,
	}
	if automaton.sparse {
		automaton.sparseStates = make([]literalSparseState, len(b.nodes))
	} else {
		automaton.transitions = make([]uint32, len(b.nodes)*256)
	}
	rootByteCount := 0
	for value := range 256 {
		byteValue := byte(value)
		if b.nodes[0].edges[byteValue>>6]&(uint64(1)<<(byteValue&63)) == 0 {
			continue
		}
		if rootByteCount < len(automaton.rootByteVals) {
			automaton.rootByteVals[rootByteCount] = byteValue
		}
		rootByteCount++
	}
	// 将未使用槽位保持为真实根字节，避免根字节 unsafe 预过滤器将零填充槽位当作额外触发器。
	if rootByteCount != 0 && rootByteCount <= len(automaton.rootByteVals) {
		for index := rootByteCount; index < len(automaton.rootByteVals); index++ {
			automaton.rootByteVals[index] = automaton.rootByteVals[0]
		}
	}
	automaton.rootByteCount = uint8(rootByteCount)
	automaton.rootByteFast = rootByteCount != 0 && rootByteCount <= len(automaton.rootByteVals)
	for state, source := range b.nodes {
		sort.Slice(source.outputs, func(i, j int) bool {
			return source.outputs[i] < source.outputs[j]
		})
		outputStart := uint32(len(automaton.outputs))
		automaton.outputs = append(automaton.outputs, source.outputs...)
		if !automaton.sparse {
			copy(automaton.transitions[state*256:], source.next[:])
		} else {
			stateInfo := literalSparseState{
				failure:   source.failure,
				edgeStart: uint32(len(automaton.sparseEdges)),
			}
			for value := range 256 {
				byteValue := byte(value)
				if source.edges[byteValue>>6]&(uint64(1)<<(byteValue&63)) == 0 {
					continue
				}
				automaton.sparseEdges = append(automaton.sparseEdges, literalSparseEdge{
					value: byteValue,
					next:  source.next[byteValue],
				})
			}
			stateInfo.edgeEnd = uint32(len(automaton.sparseEdges))
			automaton.sparseStates[state] = stateInfo
		}
		if len(source.outputs) != 0 {
			automaton.outputStart[state] = outputStart
			automaton.outputEnd[state] = uint32(len(automaton.outputs))
		}
	}
	return automaton
}

// nextSparse 沿显式 Trie 边和失败链接转移，仅在编译阶段选择稀疏表示后使用；稠密自动机
// 保持无分支的索引转移循环。
func (a literalAutomaton) nextSparse(state uint32, value byte) uint32 {
	for {
		stateInfo := a.sparseStates[state]
		for edgeIndex := stateInfo.edgeStart; edgeIndex < stateInfo.edgeEnd; edgeIndex++ {
			edge := a.sparseEdges[edgeIndex]
			if edge.value == value {
				return edge.next
			}
			if edge.value > value {
				break
			}
		}
		if state == 0 {
			return 0
		}
		state = stateInfo.failure
	}
}
