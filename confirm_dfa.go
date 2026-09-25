package scankit

import (
	"math/bits"

	"github.com/smartwalle/scankit/internal/graph"
	"github.com/smartwalle/scankit/internal/nfagraph"
	"github.com/smartwalle/scankit/internal/parser"
)

// 本文件为「末尾断言」类规则提供确定性字节自动机确认路径。
//
// 背景：确认程序虚机按候选起点逐条解释执行，Email 这类「长字符类重复 + 锚点 +
// 末尾词边界」的规则每次确认要跑几十条指令，在命中密集的语料上是 PII 脱敏链路
// 的头号热点。这里把规则图确定化成稠密字节转移表，把一次确认压缩成「每字节一次
// 表查找」，语义与确认程序保持等价。
//
// 适用性边界（不满足即返回 nil，调用方回退到确认程序虚机）：
//
//   - 图中所有断言都只能是词边界类（\b、\B），且其后继必须全部落在「不消费字节
//     即可到达接受态」的尾部集合内。这保证断言只会出现在匹配结束位置，其成立与否
//     仅取决于结束位置前后两个字节的词属性，可以用 4 种上下文的查表直接判定。
//   - 其余节点必须是确定化可字节化的结构：单字节文字、字节字符类、零宽分支/合并、
//     以及有界重复展开后的分支节点。
//
// 断言之外的语义完全由同一张图承担，因此本路径只改变求值方式，不改变规则语言。
const (
	// confirmDFAWords 是顶点集合位图的机器字数；配合 confirmDFAMaxNodes 把
	// 状态键控制在固定大小，避免确定化过程产生不定长键。
	confirmDFAWords = 16
	// confirmDFAMaxNodes 限制参与确定化的顶点数，超限直接回退。
	confirmDFAMaxNodes = confirmDFAWords * 64
	// confirmDFAMaxStates 限制确定化后的状态数。单条规则最坏情况要为
	// states*256 个 (状态, 字节) 组合各保存一个打包条目（后继 + 上下文掩码），
	// 上限据此控制在 1MB 量级；超出即回退确认程序虚机，不影响正确性。
	confirmDFAMaxStates = 1024
	// confirmDFATargetBits 是转移条目里后继状态占用的低位宽度，高 8 位存放
	// 词边界上下文掩码。上限 2^24 远大于 confirmDFAMaxStates，因此
	// confirmDFADead 可以安全地当作「该字节下无路可走」的哨兵值。
	confirmDFATargetBits = 24
	// confirmDFATargetMask 取出发送条目中的后继状态。
	confirmDFATargetMask = 1<<confirmDFATargetBits - 1
	// confirmDFADead 是死转移哨兵；其掩码位为 0，因此命中后直接返回即可。
	confirmDFADead = confirmDFATargetMask
)

// confirmDFASet 是顶点集合位图，用作确定化状态键与中间集合。
type confirmDFASet [confirmDFAWords]uint64

// or 把 other 并入接收者。
func (set *confirmDFASet) or(other *confirmDFASet) {
	for index := range set {
		set[index] |= other[index]
	}
}

// setBit 置位指定下标。
func (set *confirmDFASet) setBit(index int) {
	set[index>>6] |= uint64(1) << uint(index&63)
}

// empty 判断集合是否为空。
func (set *confirmDFASet) empty() bool {
	for index := range set {
		if set[index] != 0 {
			return false
		}
	}
	return true
}

// forEach 按升序枚举集合内的顶点下标。
func (set *confirmDFASet) forEach(fn func(index int32)) {
	for word := range set {
		value := set[word]
		for value != 0 {
			bit := bits.TrailingZeros64(value)
			value &= value - 1
			fn(int32(word<<6 | bit))
		}
	}
}

// confirmDFA 是断言敏感确认路径的稠密转移表。
//
// 每个 (状态, 字节) 条目只占 4 字节：低 confirmDFATargetBits 位是后继状态
// （confirmDFADead 表示该字节下无路可走），高位是「消费该字节后到达的位置」
// 允许作为匹配结束位置时的词边界上下文集合，位下标为 prevWord<<1 | curWord。
// 后继与掩码合并在同一条目内，确认热循环每字节只需一次表访问。
type confirmDFA struct {
	// table 按 (状态, 字节) 展平存放打包后的转移条目。
	table []uint32
	// startState 是候选起点处的确定性状态，startMask 是该位置自身的上下文集合。
	startState uint32
	startMask  uint8
	nonGreedy  bool
}

// confirmWordByte 判定单字节是否属于 \w 的 ASCII 词字符集合。
// 与 isWord 同义，单独内联成局部判定是为了避免确认热循环里出现跨函数调用。
func confirmWordByte(value byte) uint8 {
	if value == '_' || value >= '0' && value <= '9' || value >= 'A' && value <= 'Z' || value >= 'a' && value <= 'z' {
		return 1
	}
	return 0
}

// confirmAssertContexts 返回断言在 4 种词边界上下文下成立的位置集合。
func confirmAssertContexts(kind parser.AssertionKind) uint8 {
	switch kind {
	case parser.WordBoundary:
		// prevWord != curWord
		return 1<<1 | 1<<2
	case parser.NonWordBoundary:
		// prevWord == curWord
		return 1<<0 | 1<<3
	default:
	}
	return 0
}

// confirmDFAEligible 判断规则是否值得构建确定性确认表。
//
// 只在「带词边界断言、且没有其它会改变字节语义的开关」时启用；扩展参数、
// 作用域修饰符、大小写折叠、UTF-8/UCP、多行与点号匹配语义都会改变图与
// 字节的对应关系，一律回退到确认程序虚机。
func confirmDFAEligible(root parser.Node, ext *ExpressionExt, flags CompileFlag) bool {
	if root == nil || ext != nil {
		return false
	}
	if flags&(CompileCaseless|CompileUTF8|CompileUCP|CompileMultiline|CompileDotAll|CompileSOMLeftmost) != 0 {
		return false
	}
	if parser.HasScopedFlags(root) || parser.RequiresStatefulRuntime(root) {
		return false
	}
	return parser.HasAssertion(root)
}

// buildConfirmDFA 把规则图确定化成断言敏感确认表。返回 nil 表示该图不满足
// 本路径的适用性边界或超出规模预算，调用方必须继续使用确认程序虚机。
func buildConfirmDFA(g *nfagraph.Graph, nonGreedy bool) *confirmDFA {
	expanded := nfagraph.ExpandLiterals(g)
	if expanded == nil || expanded.Flow == nil {
		return nil
	}
	vertices := expanded.Flow.Vertices()
	if len(vertices) == 0 || len(vertices) > confirmDFAMaxNodes {
		return nil
	}
	slot := make(map[graph.Vertex]int32, len(vertices))
	for index, vertex := range vertices {
		if expanded.Nodes[vertex] == nil {
			return nil
		}
		slot[vertex] = int32(index)
	}
	count := len(vertices)
	consuming := make([]bool, count)
	byteSet := make([]confirmDFASet, count)
	successors := make([]confirmDFASet, count)
	successorList := make([][]int32, count)
	kinds := make([]nfagraph.NodeKind, count)
	assertions := make([]parser.AssertionKind, count)
	for index, vertex := range vertices {
		node := expanded.Nodes[vertex]
		kinds[index] = node.Kind
		assertions[index] = node.Assertion
		switch node.Kind {
		case nfagraph.KindLiteral:
			if len(node.Literal) != 1 {
				return nil
			}
			consuming[index] = true
			byteSet[index].setBit(int(node.Literal[0]))
		case nfagraph.KindClass:
			if node.Unicode != nil {
				return nil
			}
			consuming[index] = true
			for value := 0; value < 256; value++ {
				if confirmByteMatchesClass(node, byte(value)) {
					byteSet[index].setBit(value)
				}
			}
		case nfagraph.KindStart, nfagraph.KindAccept, nfagraph.KindSplit, nfagraph.KindJoin, nfagraph.KindRepeat, nfagraph.KindReport:
		case nfagraph.KindAssertion:
		default:
			return nil
		}
		list := make([]int32, 0, 2)
		for _, next := range expanded.Flow.Successors(vertex) {
			target, ok := slot[next]
			if !ok {
				return nil
			}
			successors[index].setBit(int(target))
			list = append(list, target)
		}
		successorList[index] = list
	}
	// 尾部集合：不消费任何字节即可到达接受态的顶点。断言的后继必须全部落在其中，
	// 否则断言可能出现在匹配中段，本路径的上下文查表就不再成立。
	tail := make([]bool, count)
	for index := range tail {
		tail[index] = kinds[index] == nfagraph.KindAccept
	}
	for changed := true; changed; {
		changed = false
		for index := range vertices {
			if tail[index] || consuming[index] {
				continue
			}
			for _, target := range successorList[index] {
				if tail[target] {
					tail[index] = true
					changed = true
					break
				}
			}
		}
	}
	for index := range vertices {
		if kinds[index] != nfagraph.KindAssertion {
			continue
		}
		if confirmAssertContexts(assertions[index]) == 0 {
			return nil
		}
		for _, target := range successorList[index] {
			if !tail[target] {
				return nil
			}
		}
	}
	// reach 是「从顶点出发，只经过零宽节点、且沿途断言在当前上下文成立时
	// 能到达接受态」的上下文集合，取最小不动点。
	reach := make([]uint8, count)
	for index := range vertices {
		if kinds[index] == nfagraph.KindAccept {
			reach[index] = 0x0f
		}
	}
	for changed := true; changed; {
		changed = false
		for index := range vertices {
			if kinds[index] == nfagraph.KindAccept {
				continue
			}
			var union uint8
			for _, target := range successorList[index] {
				union |= reach[target]
			}
			value := union
			if consuming[index] {
				value = 0
			} else if kinds[index] == nfagraph.KindAssertion {
				value = confirmAssertContexts(assertions[index]) & union
			}
			if value & ^reach[index] != 0 {
				reach[index] |= value
				changed = true
			}
		}
	}
	// eps 是「经过任意零宽节点可达的顶点集合」，用于确定化时的空闭包。
	eps := make([]confirmDFASet, count)
	for index := range eps {
		eps[index].setBit(index)
	}
	for changed := true; changed; {
		changed = false
		for index := range vertices {
			if consuming[index] {
				continue
			}
			var union confirmDFASet
			for _, target := range successorList[index] {
				union.or(&eps[target])
			}
			merged := eps[index]
			merged.or(&union)
			if merged != eps[index] {
				eps[index] = merged
				changed = true
			}
		}
	}
	start, ok := slot[expanded.Start]
	if !ok {
		return nil
	}
	states := make([]confirmDFASet, 0, 256)
	index := make(map[confirmDFASet]uint32, 256)
	states = append(states, eps[start])
	index[eps[start]] = 0
	table := make([]uint32, 0, 256*256)
	for cursor := 0; cursor < len(states); cursor++ {
		current := states[cursor]
		for value := 0; value < 256; value++ {
			var moved confirmDFASet
			current.forEach(func(vertex int32) {
				if byteSet[vertex].hasBit(value) {
					moved.or(&successors[vertex])
				}
			})
			if moved.empty() {
				table = append(table, confirmDFADead)
				continue
			}
			var closed confirmDFASet
			moved.forEach(func(vertex int32) {
				closed.or(&eps[vertex])
			})
			target, known := index[closed]
			if !known {
				if len(states) >= confirmDFAMaxStates {
					return nil
				}
				target = uint32(len(states))
				index[closed] = target
				states = append(states, closed)
			}
			var mask uint8
			moved.forEach(func(vertex int32) { mask |= reach[vertex] })
			table = append(table, target|uint32(mask)<<confirmDFATargetBits)
		}
	}
	return &confirmDFA{
		table:      table,
		startState: 0,
		startMask:  reach[start],
		nonGreedy:  nonGreedy,
	}
}

// hasBit 判断位图是否包含指定下标。
func (set *confirmDFASet) hasBit(index int) bool {
	return set[index>>6]&(uint64(1)<<uint(index&63)) != 0
}

// confirmByteMatchesClass 判定单字节是否属于字符类，取值与内部 DFA 的字节
// 判定保持一致：Unicode 类不参与字节化，取反语义在范围命中后翻转。
func confirmByteMatchesClass(node *nfagraph.Node, value byte) bool {
	hit := false
	for _, bounds := range node.Class.Ranges {
		if value >= bounds.Lo && value <= bounds.Hi {
			hit = true
			break
		}
	}
	return hit != node.Class.Negated
}

// memoryBytes 估算确认表的常驻内存，用于编译期的资源预算核算。
func (d *confirmDFA) memoryBytes() uint64 {
	if d == nil {
		return 0
	}
	return uint64(len(d.table)) * 4
}

// preferredEnd 从候选起点执行确定性确认，返回 record 需要的偏好结束偏移。
// ok 为 false 表示参数越界，调用方应保持原有语义。
func (d *confirmDFA) preferredEnd(data []byte, start int) (int, bool) {
	if d == nil || start < 0 || start > len(data) {
		return -1, false
	}
	state := d.startState
	mask := d.startMask
	best := -1
	pos := start
	// prevWord 是当前位置之前一个字节的词属性；越界一律按非词字符处理，
	// 与断言在数据边界上的约定一致。每推进一个字节只需重新判定一次当前字节，
	// 上一轮的 curWord 直接成为下一轮的 prevWord。
	prevWord := uint8(0)
	if pos > 0 {
		prevWord = confirmWordByte(data[pos-1])
	}
	for {
		curWord := uint8(0)
		if pos < len(data) {
			curWord = confirmWordByte(data[pos])
		}
		if mask&(1<<(prevWord<<1|curWord)) != 0 {
			if d.nonGreedy {
				return pos, true
			}
			best = pos
		}
		if pos >= len(data) {
			return best, true
		}
		offset := state<<8 | uint32(data[pos])
		entry := d.table[offset]
		mask = uint8(entry >> confirmDFATargetBits)
		target := entry & confirmDFATargetMask
		if target == confirmDFADead {
			return best, true
		}
		state = target
		prevWord = curWord
		pos++
	}
}
