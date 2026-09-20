package nfa

import (
	"bytes"
	"fmt"
	"sort"
	"sync"

	"github.com/smartwalle/scankit/internal/graph"
	"github.com/smartwalle/scankit/internal/nfagraph"
)

// castleProgram 维护可消费节点集合及预计算的 epsilon 闭包。
type castleProgram struct {
	graph             *nfagraph.Graph
	closureTable      map[graph.Vertex][]graph.Vertex
	index             map[graph.Vertex]int
	transitions       map[graph.Vertex][]graph.Vertex
	closureByIndex    [][]graph.Vertex
	transitionByIndex [][]graph.Vertex
	closureIndex      [][]int
	transitionIndex   [][]int
	accept            map[graph.Vertex]struct{}
	acceptByIndex     []bool
	dead              map[graph.Vertex]bool
	deadByIndex       []bool
	kinds             []nfagraph.NodeKind
	literals          []byte
	classMasks        [][4]uint64
	// reverseClosureIndex 保存从节点向前回溯时可经过的零宽状态集合。
	// 它与正向闭包并列维护，使复杂分支可在结束位置反向确认。
	reverseClosureIndex [][]int
	predecessorIndex    [][]int
	reverseEnabled      bool
	queueLimit          int
	minBytes            int
	maxBytes            int
	firstMask           [4]uint64
	acceptsEmpty        bool
	prefix              []byte
}

const castleQueueLimit = 1 << 20

type castleWork struct {
	marks  []uint32
	active []int
	next   []int
	closed []int
}

type castleReverseWork struct {
	active []int
	next   []int
	closed []int
}

var castleReverseWorkPool sync.Pool

func acquireCastleReverseWork(states int) *castleReverseWork {
	w, _ := castleReverseWorkPool.Get().(*castleReverseWork)
	if w == nil {
		w = &castleReverseWork{}
	}
	if cap(w.active) < states {
		w.active = make([]int, 0, states)
	} else {
		w.active = w.active[:0]
	}
	if cap(w.next) < states {
		w.next = make([]int, 0, states)
	} else {
		w.next = w.next[:0]
	}
	if cap(w.closed) < states {
		w.closed = make([]int, 0, states)
	} else {
		w.closed = w.closed[:0]
	}
	return w
}

func releaseCastleReverseWork(w *castleReverseWork) {
	if w == nil {
		return
	}
	if cap(w.active) > 1<<16 || cap(w.next) > 1<<16 || cap(w.closed) > 1<<16 {
		return
	}
	w.active, w.next, w.closed = w.active[:0], w.next[:0], w.closed[:0]
	castleReverseWorkPool.Put(w)
}

var castleWorkPool sync.Pool

func acquireCastleWork(states int) *castleWork {
	w, _ := castleWorkPool.Get().(*castleWork)
	if w == nil {
		w = &castleWork{}
	}
	if cap(w.marks) < states {
		w.marks = make([]uint32, states)
	} else {
		w.marks = w.marks[:states]
		clear(w.marks)
	}
	if cap(w.active) < states {
		w.active = make([]int, 0, states)
	} else {
		w.active = w.active[:0]
	}
	if cap(w.next) < states {
		w.next = make([]int, 0, states)
	} else {
		w.next = w.next[:0]
	}
	if cap(w.closed) < states {
		w.closed = make([]int, 0, states)
	} else {
		w.closed = w.closed[:0]
	}
	return w
}

func releaseCastleWork(w *castleWork) {
	if w == nil {
		return
	}
	w.active = w.active[:0]
	w.next = w.next[:0]
	w.closed = w.closed[:0]
	if cap(w.marks) > 1<<16 || cap(w.active) > 1<<16 || cap(w.next) > 1<<16 || cap(w.closed) > 1<<16 {
		w.marks, w.active, w.next, w.closed = nil, nil, nil, nil
	}
	castleWorkPool.Put(w)
}

func (p *castleProgram) validate() error {
	if p == nil || p.graph == nil || len(p.kinds) != len(p.graph.Nodes) || len(p.literals) != len(p.graph.Nodes) || len(p.classMasks) != len(p.graph.Nodes) || len(p.acceptByIndex) != len(p.graph.Nodes) || len(p.closureIndex) != len(p.graph.Nodes) || len(p.transitionIndex) != len(p.graph.Nodes) || len(p.reverseClosureIndex) != len(p.graph.Nodes) || len(p.predecessorIndex) != len(p.graph.Nodes) {
		return fmt.Errorf("invalid castle graph")
	}
	if err := p.graph.Validate(); err != nil {
		return err
	}
	if !castleEligible(p.graph) {
		return fmt.Errorf("castle graph contains unsupported node")
	}
	for source, closure := range p.closureTable {
		if p.graph.Nodes[source] == nil {
			return fmt.Errorf("castle closure source out of bounds")
		}
		for _, target := range closure {
			if p.graph.Nodes[target] == nil {
				return fmt.Errorf("castle closure target out of bounds")
			}
		}
		found := false
		for i := 1; i < len(closure); i++ {
			if closure[i-1] >= closure[i] {
				return fmt.Errorf("castle closure is not strictly ordered")
			}
		}
		for _, target := range closure {
			if target == source {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("castle closure misses source")
		}
		want := p.computeClosure(source)
		if len(want) != len(closure) {
			return fmt.Errorf("castle closure mismatch")
		}
		for i := range want {
			if want[i] != closure[i] {
				return fmt.Errorf("castle closure mismatch")
			}
		}
	}
	if len(p.index) != len(p.graph.Nodes) || len(p.transitions) != len(p.graph.Nodes) || len(p.closureTable) != len(p.graph.Nodes) {
		return fmt.Errorf("castle vertex index size mismatch")
	}
	for _, id := range p.graph.NodeIDs() {
		if _, ok := p.index[id]; !ok {
			return fmt.Errorf("castle vertex index missing")
		}
		i := p.index[id]
		if i < 0 || i >= len(p.kinds) {
			return fmt.Errorf("castle vertex index out of bounds")
		}
		n := p.graph.Nodes[id]
		if p.kinds[i] != n.Kind {
			return fmt.Errorf("castle state kind mismatch")
		}
		if n.Kind == nfagraph.KindLiteral && len(n.Literal) == 1 && p.literals[i] != n.Literal[0] {
			return fmt.Errorf("castle literal state mismatch")
		}
		if n.Kind == nfagraph.KindClass {
			for value := 0; value < 256; value++ {
				want := castleMatches(n, byte(value))
				got := p.classMasks[i][value/64]&(1<<uint(value%64)) != 0
				if want != got {
					return fmt.Errorf("castle class mask mismatch")
				}
			}
		}
		if _, ok := p.transitions[id]; !ok {
			return fmt.Errorf("castle transition row missing")
		}
		if _, ok := p.closureTable[id]; !ok {
			return fmt.Errorf("castle closure row missing")
		}
		if len(p.closureIndex[i]) != len(p.closureByIndex[i]) || len(p.transitionIndex[i]) != len(p.transitionByIndex[i]) {
			return fmt.Errorf("castle indexed row size mismatch")
		}
		for j, target := range p.closureIndex[i] {
			if target < 0 || target >= len(p.graph.Nodes) || p.index[p.closureByIndex[i][j]] != target {
				return fmt.Errorf("castle indexed closure mismatch")
			}
			if j > 0 && p.closureIndex[i][j-1] >= target {
				return fmt.Errorf("castle indexed closure order mismatch")
			}
		}
		for j, target := range p.transitionIndex[i] {
			if target < 0 || target >= len(p.graph.Nodes) || p.index[p.transitionByIndex[i][j]] != target {
				return fmt.Errorf("castle indexed transition mismatch")
			}
			if j > 0 && p.transitionIndex[i][j-1] >= target {
				return fmt.Errorf("castle indexed transition order mismatch")
			}
		}
	}
	for id := range p.index {
		if p.graph.Nodes[id] == nil {
			return fmt.Errorf("castle vertex index contains unknown vertex")
		}
	}
	for i, row := range p.reverseClosureIndex {
		if len(row) == 0 {
			return fmt.Errorf("castle reverse closure row missing")
		}
		for j, target := range row {
			if target < 0 || target >= len(p.kinds) || j > 0 && row[j-1] >= target {
				return fmt.Errorf("castle reverse closure is not strictly ordered")
			}
		}
		if !containsInt(row, i) {
			return fmt.Errorf("castle reverse closure misses source")
		}
		for _, target := range p.predecessorIndex[i] {
			if target < 0 || target >= len(p.kinds) {
				return fmt.Errorf("castle predecessor out of bounds")
			}
		}
	}
	if p.queueLimit <= 0 || len(p.accept) == 0 || len(p.accept) != len(p.graph.Accepts()) {
		return fmt.Errorf("castle execution limits or accept set missing")
	}
	if !bytes.Equal(p.prefix, requiredLiteralPrefix(p.graph)) {
		return fmt.Errorf("castle prefix mismatch")
	}
	if p.firstMask != firstByteMask(p.graph) {
		return fmt.Errorf("castle start candidate mask mismatch")
	}
	minBytes, maxBytes := graphLengthBounds(p.graph)
	if p.minBytes != minBytes || p.maxBytes != maxBytes {
		return fmt.Errorf("castle length bounds mismatch")
	}
	if p.acceptsEmpty != graphAcceptsEmpty(p.graph) {
		return fmt.Errorf("castle empty-match metadata mismatch")
	}
	if len(p.dead) != len(p.graph.Nodes) || len(p.deadByIndex) != len(p.graph.Nodes) {
		return fmt.Errorf("castle dead-state map size mismatch")
	}
	needsReverse := len(p.accept) > 1
	for _, id := range p.graph.NodeIDs() {
		if len(p.graph.Successors(id)) > 1 {
			needsReverse = true
			break
		}
	}
	if p.reverseEnabled != needsReverse {
		return fmt.Errorf("castle reverse confirmation mode mismatch")
	}
	for id, index := range p.index {
		if index < 0 || index >= len(p.deadByIndex) || p.deadByIndex[index] != p.dead[id] {
			return fmt.Errorf("castle indexed dead-state mismatch")
		}
	}
	wantDead := computeGraphDead(p.graph)
	for id := range p.graph.Nodes {
		_, want := wantDead[id]
		if p.dead[id] != want {
			return fmt.Errorf("castle dead-state map mismatch")
		}
	}
	for id := range p.accept {
		if !p.graph.IsAccept(id) {
			return fmt.Errorf("castle accept set mismatch")
		}
	}
	for vertex, index := range p.index {
		if index < 0 || index >= len(p.index) || p.graph.Nodes[vertex] == nil {
			return fmt.Errorf("castle vertex index out of bounds")
		}
	}
	for source, targets := range p.transitions {
		if p.graph.Nodes[source] == nil {
			return fmt.Errorf("castle transition source out of bounds")
		}
		seen := make(map[graph.Vertex]struct{}, len(targets))
		for i := 1; i < len(targets); i++ {
			if targets[i-1] >= targets[i] {
				return fmt.Errorf("castle transitions are not strictly ordered")
			}
		}
		for _, target := range targets {
			if p.graph.Nodes[target] == nil {
				return fmt.Errorf("castle transition target out of bounds")
			}
			if _, exists := seen[target]; exists {
				return fmt.Errorf("castle duplicate transition")
			}
			seen[target] = struct{}{}
		}
		expected := append([]graph.Vertex(nil), p.graph.Successors(source)...)
		sort.Slice(expected, func(i, j int) bool { return expected[i] < expected[j] })
		actual := append([]graph.Vertex(nil), targets...)
		sort.Slice(actual, func(i, j int) bool { return actual[i] < actual[j] })
		if len(expected) != len(actual) {
			return fmt.Errorf("castle transition mismatch")
		}
		for i := range expected {
			if expected[i] != actual[i] {
				return fmt.Errorf("castle transition mismatch")
			}
		}
	}
	return nil
}

func newCastleProgram(g *nfagraph.Graph) *castleProgram {
	if g == nil || g.Validate() != nil {
		return nil
	}
	if g.Flow == nil || uint64(len(g.Nodes))*72+uint64(g.Flow.EdgeCount())*16 > maxLayoutMemory {
		return nil
	}
	ids := g.NodeIDs()
	dead := make(map[graph.Vertex]bool, len(g.Nodes))
	deadSet := computeGraphDead(g)
	for _, id := range ids {
		_, dead[id] = deadSet[id]
	}
	minBytes, maxBytes := graphLengthBounds(g)
	queueLimit := castleQueueLimit
	if budget := int(maxLayoutMemory / 64); queueLimit > budget {
		queueLimit = budget
	}
	if queueLimit < len(ids) {
		queueLimit = len(ids)
	}
	p := &castleProgram{graph: g, closureTable: make(map[graph.Vertex][]graph.Vertex, len(g.Nodes)), index: make(map[graph.Vertex]int, len(ids)), transitions: make(map[graph.Vertex][]graph.Vertex, len(g.Nodes)), closureByIndex: make([][]graph.Vertex, len(ids)), transitionByIndex: make([][]graph.Vertex, len(ids)), closureIndex: make([][]int, len(ids)), transitionIndex: make([][]int, len(ids)), reverseClosureIndex: make([][]int, len(ids)), predecessorIndex: make([][]int, len(ids)), accept: make(map[graph.Vertex]struct{}), acceptByIndex: make([]bool, len(ids)), dead: dead, deadByIndex: make([]bool, len(ids)), kinds: make([]nfagraph.NodeKind, len(ids)), literals: make([]byte, len(ids)), classMasks: make([][4]uint64, len(ids)), queueLimit: queueLimit, minBytes: minBytes, maxBytes: maxBytes, firstMask: firstByteMask(g), acceptsEmpty: graphAcceptsEmpty(g), prefix: requiredLiteralPrefix(g)}
	for i, v := range ids {
		p.index[v] = i
	}
	for i, v := range ids {
		p.deadByIndex[i] = dead[v]
		n := g.Nodes[v]
		p.kinds[i] = n.Kind
		if n.Kind == nfagraph.KindLiteral && len(n.Literal) == 1 {
			p.literals[i] = n.Literal[0]
		} else if n.Kind == nfagraph.KindClass {
			for value := 0; value < 256; value++ {
				if castleMatches(n, byte(value)) {
					p.classMasks[i][value/64] |= 1 << uint(value%64)
				}
			}
		}
		p.closureTable[v] = p.computeClosure(v)
		p.closureByIndex[i] = p.closureTable[v]
		p.closureIndex[i] = make([]int, 0, len(p.closureTable[v]))
		for _, target := range p.closureTable[v] {
			p.closureIndex[i] = append(p.closureIndex[i], p.index[target])
		}
		p.transitions[v] = append([]graph.Vertex(nil), g.Successors(v)...)
		sort.Slice(p.transitions[v], func(a, b int) bool { return p.transitions[v][a] < p.transitions[v][b] })
		if len(p.transitions[v]) > 1 {
			uniq := p.transitions[v][:1]
			for _, target := range p.transitions[v][1:] {
				if target != uniq[len(uniq)-1] {
					uniq = append(uniq, target)
				}
			}
			p.transitions[v] = uniq
		}
		p.transitionByIndex[i] = p.transitions[v]
		p.transitionIndex[i] = make([]int, 0, len(p.transitions[v]))
		for _, target := range p.transitions[v] {
			p.transitionIndex[i] = append(p.transitionIndex[i], p.index[target])
		}
		predecessors := append([]graph.Vertex(nil), g.Predecessors(v)...)
		sort.Slice(predecessors, func(a, b int) bool { return predecessors[a] < predecessors[b] })
		predecessors = dedupVertices(predecessors)
		for _, previous := range predecessors {
			if index, ok := p.index[previous]; ok {
				p.predecessorIndex[i] = append(p.predecessorIndex[i], index)
			}
		}
		reverse := make(map[graph.Vertex]struct{})
		queue := []graph.Vertex{v}
		for head := 0; head < len(queue); head++ {
			current := queue[head]
			if _, seen := reverse[current]; seen {
				continue
			}
			reverse[current] = struct{}{}
			node := g.Nodes[current]
			if node != nil && (node.Kind == nfagraph.KindLiteral || node.Kind == nfagraph.KindClass) {
				continue
			}
			queue = append(queue, g.Predecessors(current)...)
		}
		p.reverseClosureIndex[i] = make([]int, 0, len(reverse))
		for ancestor := range reverse {
			if index, ok := p.index[ancestor]; ok {
				p.reverseClosureIndex[i] = append(p.reverseClosureIndex[i], index)
			}
		}
		sort.Ints(p.reverseClosureIndex[i])
		if g.IsAccept(v) {
			p.accept[v] = struct{}{}
			p.acceptByIndex[i] = true
		}
	}
	for _, id := range ids {
		if len(g.Successors(id)) > 1 {
			p.reverseEnabled = true
			break
		}
	}
	if len(p.accept) > 1 {
		p.reverseEnabled = true
	}
	if err := p.validate(); err != nil {
		return nil
	}
	return p
}

func (p *castleProgram) MatchAt(data []byte, start int) []int {
	if p == nil || p.graph == nil || start < 0 || start > len(data) || !castleRuntimeShapeOK(p) {
		return nil
	}
	if startIndex, ok := p.index[p.graph.Start]; !ok || startIndex < 0 || startIndex >= len(p.closureIndex) {
		return nil
	}
	if len(p.prefix) == 0 && start < len(data) && !p.acceptsEmpty && !byteMaskEmpty(p.firstMask) && !hasFirstByte(p.firstMask, data[start]) {
		return nil
	}
	if len(p.prefix) > 0 && !prefixMatchesAt(data, start, p.prefix) {
		return nil
	}
	work := acquireCastleWork(len(p.index))
	defer releaseCastleWork(work)
	startIndex := p.index[p.graph.Start]
	active := append(work.active, p.closureIndex[startIndex]...)
	active = filterCastleDeadIndex(active, p.deadByIndex)
	out := make([]int, 0)
	next := work.next
	marks := work.marks
	generation := uint32(0)
	closed := work.closed
	if p.minBytes > len(data)-start {
		return out
	}
	firstEnd := start + p.minBytes
	lastEnd := boundedBackendEnd(data, start, p.maxBytes)
	for pos := start; pos <= lastEnd; pos++ {
		if pos >= firstEnd {
			for _, id := range active {
				if p.deadByIndex[id] {
					continue
				}
				if p.acceptByIndex[id] && (!p.reverseEnabled || p.reverseAccepts(data, start, pos)) {
					out = append(out, pos)
					break
				}
			}
		}
		if pos >= lastEnd {
			break
		}
		next = next[:0]
		for _, id := range active {
			if id < 0 || id >= len(p.kinds) || (p.kinds[id] != nfagraph.KindLiteral && p.kinds[id] != nfagraph.KindClass) {
				continue
			}
			if p.kinds[id] == nfagraph.KindLiteral && p.literals[id] != data[pos] || p.kinds[id] == nfagraph.KindClass && p.classMasks[id][data[pos]/64]&(1<<uint(data[pos]%64)) == 0 {
				continue
			}
			targets := p.transitionIndex[id]
			if len(next) > p.queueLimit-len(targets) {
				return out
			}
			next = append(next, targets...)
		}
		// 分支汇合可能产生重复目标，先归一化再展开闭包，避免重复状态放大队列。
		next = dedupInts(next)
		active = p.closureIndexWithWork(next, marks, &generation, closed)
		active = filterCastleDeadIndex(active, p.deadByIndex)
		closed = active
		if len(active) == 0 {
			break
		}
	}
	return out
}

// reverseAccepts 从候选结束位置反向传播到起点，用于复杂分支的独立确认。
// 该路径只访问已编译的索引表，不重新解释原始图；任一边界不满足时直接拒绝。
func (p *castleProgram) reverseAccepts(data []byte, start, end int) bool {
	if p == nil || start < 0 || end < start || end > len(data) || len(p.reverseClosureIndex) != len(p.kinds) {
		return false
	}
	work := acquireCastleReverseWork(len(p.kinds))
	defer releaseCastleReverseWork(work)
	active := work.active
	// 多接受节点必须合并所有反向闭包，不能只取首个接受节点。
	active = active[:0]
	for i, accepted := range p.acceptByIndex {
		if accepted {
			active = append(active, p.reverseClosureIndex[i]...)
		}
	}
	active = dedupInts(active)
	for pos := end - 1; pos >= start; pos-- {
		next := work.next[:0]
		for _, id := range active {
			if id < 0 || id >= len(p.kinds) || p.deadByIndex[id] {
				continue
			}
			matched := false
			switch p.kinds[id] {
			case nfagraph.KindLiteral:
				matched = p.literals[id] == data[pos]
			case nfagraph.KindClass:
				matched = p.classMasks[id][data[pos]/64]&(1<<uint(data[pos]%64)) != 0
			}
			if !matched {
				continue
			}
			next = append(next, p.predecessorIndex[id]...)
		}
		if len(next) == 0 {
			return false
		}
		next = dedupInts(next)
		closed := work.closed[:0]
		for _, id := range next {
			if id >= 0 && id < len(p.reverseClosureIndex) {
				closed = append(closed, p.reverseClosureIndex[id]...)
			}
		}
		active = dedupInts(closed)
		work.active = active
	}
	startIndex, ok := p.index[p.graph.Start]
	return ok && containsInt(active, startIndex)
}

func (p *castleProgram) MatchAtBudget(data []byte, start, maxSteps, maxResults int) ([]int, int, bool) {
	if p == nil || p.graph == nil || start < 0 || start > len(data) || maxSteps < 0 || maxResults < 0 || !castleRuntimeShapeOK(p) {
		return nil, 0, false
	}
	return p.matchAtBudgetUnchecked(data, start, maxSteps, maxResults)
}

// matchAtBudgetUnchecked 在调用方已完成布局校验后执行 Castle 状态机。
// 整块扫描复用不可变布局，避免每个起点重复遍历状态索引和边界表。
func (p *castleProgram) matchAtBudgetUnchecked(data []byte, start, maxSteps, maxResults int) ([]int, int, bool) {
	return p.matchAtBudgetUncheckedInto(data, start, maxSteps, maxResults, nil)
}

func (p *castleProgram) matchAtBudgetUncheckedInto(data []byte, start, maxSteps, maxResults int, dst []int) ([]int, int, bool) {
	if startIndex, ok := p.index[p.graph.Start]; !ok || startIndex < 0 || startIndex >= len(p.closureIndex) {
		return nil, 0, false
	}
	if len(p.prefix) == 0 && start < len(data) && !p.acceptsEmpty && !byteMaskEmpty(p.firstMask) && !hasFirstByte(p.firstMask, data[start]) {
		return nil, 0, false
	}
	if len(p.prefix) > 0 && !prefixMatchesAt(data, start, p.prefix) {
		return nil, 0, false
	}
	work := acquireCastleWork(len(p.index))
	defer releaseCastleWork(work)
	startIndex := p.index[p.graph.Start]
	active := append(work.active, p.closureIndex[startIndex]...)
	active = filterCastleDeadIndex(active, p.deadByIndex)
	steps := len(active)
	if maxSteps > 0 && steps > maxSteps {
		return nil, steps, true
	}
	out := dst[:0]
	next := work.next
	marks := work.marks
	generation := uint32(0)
	closed := work.closed
	if p.minBytes > len(data)-start {
		return nil, steps, false
	}
	firstEnd := start + p.minBytes
	lastEnd := boundedBackendEnd(data, start, p.maxBytes)
	for pos := start; pos <= lastEnd; pos++ {
		if pos >= firstEnd {
			for _, id := range active {
				steps++
				if maxSteps > 0 && steps > maxSteps {
					return nil, steps, true
				}
				if p.deadByIndex[id] {
					continue
				}
				if p.acceptByIndex[id] && (!p.reverseEnabled || p.reverseAccepts(data, start, pos)) {
					if maxResults > 0 && len(out) >= maxResults {
						return out, steps, true
					}
					out = append(out, pos)
					break
				}
			}
		}
		if pos >= lastEnd {
			break
		}
		next = next[:0]
		for _, id := range active {
			if id < 0 || id >= len(p.kinds) || (p.kinds[id] != nfagraph.KindLiteral && p.kinds[id] != nfagraph.KindClass) || p.kinds[id] == nfagraph.KindLiteral && p.literals[id] != data[pos] || p.kinds[id] == nfagraph.KindClass && p.classMasks[id][data[pos]/64]&(1<<uint(data[pos]%64)) == 0 {
				continue
			}
			targets := p.transitionIndex[id]
			if len(next) > p.queueLimit-len(targets) {
				return out, steps, true
			}
			next = append(next, targets...)
		}
		active = p.closureIndexWithWork(next, marks, &generation, closed)
		active = filterCastleDeadIndex(active, p.deadByIndex)
		closed = active
		steps += len(active)
		if maxSteps > 0 && steps > maxSteps {
			return nil, steps, true
		}
		if len(active) == 0 {
			break
		}
	}
	return out, steps, false
}

// filterCastleDead 在闭包展开后移除无法到达接受节点的状态。
func filterCastleDead(states []graph.Vertex, dead map[graph.Vertex]bool) []graph.Vertex {
	if len(states) == 0 || len(dead) == 0 {
		return states
	}
	write := 0
	for _, id := range states {
		if dead[id] {
			continue
		}
		states[write] = id
		write++
	}
	return states[:write]
}

// filterCastleDeadIndex 在整数状态布局中移除无法到达接受节点的状态。
func filterCastleDeadIndex(states []int, dead []bool) []int {
	write := 0
	for _, id := range states {
		if id < 0 || id >= len(dead) || dead[id] {
			continue
		}
		states[write] = id
		write++
	}
	return states[:write]
}

func equalVertices(a, b []graph.Vertex) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func castleRuntimeShapeOK(p *castleProgram) bool {
	if p == nil || p.graph == nil || p.graph.Flow == nil || p.queueLimit <= 0 || len(p.index) != len(p.graph.Nodes) || len(p.transitions) != len(p.graph.Nodes) || len(p.closureTable) != len(p.graph.Nodes) || len(p.closureByIndex) != len(p.graph.Nodes) || len(p.transitionByIndex) != len(p.graph.Nodes) || len(p.closureIndex) != len(p.graph.Nodes) || len(p.transitionIndex) != len(p.graph.Nodes) || len(p.kinds) != len(p.graph.Nodes) || len(p.literals) != len(p.graph.Nodes) || len(p.classMasks) != len(p.graph.Nodes) || len(p.acceptByIndex) != len(p.graph.Nodes) || len(p.accept) != len(p.graph.Accepts()) {
		return false
	}
	if len(p.dead) != len(p.graph.Nodes) || len(p.deadByIndex) != len(p.graph.Nodes) {
		return false
	}
	minBytes, maxBytes := graphLengthBounds(p.graph)
	if p.minBytes != minBytes || p.maxBytes != maxBytes {
		return false
	}
	for _, id := range p.graph.NodeIDs() {
		index, ok := p.index[id]
		if !ok || index < 0 || index >= len(p.closureIndex) {
			return false
		}
		if p.deadByIndex[index] != p.dead[id] {
			return false
		}
		if p.acceptByIndex[index] != p.graph.IsAccept(id) {
			return false
		}
		if !equalVertices(p.closureByIndex[index], p.closureTable[id]) || !equalVertices(p.transitionByIndex[index], p.transitions[id]) || len(p.closureIndex[index]) != len(p.closureByIndex[index]) || len(p.transitionIndex[index]) != len(p.transitionByIndex[index]) {
			return false
		}
		for i, target := range p.closureIndex[index] {
			if target < 0 || target >= len(p.index) || p.index[p.closureByIndex[index][i]] != target {
				return false
			}
		}
		for i, target := range p.transitionIndex[index] {
			if target < 0 || target >= len(p.index) || p.index[p.transitionByIndex[index][i]] != target {
				return false
			}
		}
		closure, ok := p.closureTable[id]
		if !ok || len(closure) == 0 {
			return false
		}
		found := false
		for i, target := range closure {
			if target == id {
				found = true
			}
			if i > 0 && closure[i-1] >= target {
				return false
			}
			if p.graph.Nodes[target] == nil {
				return false
			}
		}
		if !found {
			return false
		}
		for i, target := range p.transitions[id] {
			if p.graph.Nodes[target] == nil {
				return false
			}
			if i > 0 && p.transitions[id][i-1] >= target {
				return false
			}
		}
	}
	for id := range p.accept {
		if p.graph.Nodes[id] == nil || !p.graph.IsAccept(id) {
			return false
		}
	}
	for id, index := range p.index {
		if p.acceptByIndex[index] != p.graph.IsAccept(id) {
			return false
		}
	}
	_, ok := p.closureTable[p.graph.Start]
	if !ok {
		return false
	}
	return p.firstMask == firstByteMask(p.graph) && p.acceptsEmpty == graphAcceptsEmpty(p.graph)
}

func (p *castleProgram) Spans(data []byte, limit int) []Span {
	if p == nil || limit < 0 || !castleRuntimeShapeOK(p) {
		return nil
	}
	out := make([]Span, 0, initialSpanCapacity(data, limit))
	endsBuf := make([]int, 0, 4)
	forEachPrefixStart(data, p.prefix, func(start int) bool {
		if len(p.prefix) == 0 && start < len(data) && !p.acceptsEmpty && !byteMaskEmpty(p.firstMask) && !hasFirstByte(p.firstMask, data[start]) {
			return true
		}
		ends, _, _ := p.matchAtBudgetUncheckedInto(data, start, 0, 0, endsBuf[:0])
		endsBuf = ends
		for _, end := range ends {
			out = append(out, Span{From: start, To: end})
			if limit > 0 && len(out) >= limit {
				return false
			}
		}
		return true
	})
	return out
}

func (p *castleProgram) closure(seed []graph.Vertex) []graph.Vertex {
	if len(p.closureTable) > 0 {
		seen := make(map[graph.Vertex]struct{}, len(seed))
		for _, v := range seed {
			for _, item := range p.closureTable[v] {
				seen[item] = struct{}{}
			}
		}
		out := make([]graph.Vertex, 0, len(seen))
		for v := range seen {
			out = append(out, v)
		}
		sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
		return out
	}
	seen := make(map[graph.Vertex]struct{})
	queue := append([]graph.Vertex(nil), seed...)
	for len(queue) > 0 {
		v := queue[0]
		queue = queue[1:]
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		n := p.graph.Nodes[v]
		if n == nil || n.Kind == nfagraph.KindLiteral || n.Kind == nfagraph.KindClass {
			continue
		}
		queue = append(queue, p.graph.Successors(v)...)
	}
	out := make([]graph.Vertex, 0, len(seen))
	for v := range seen {
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func (p *castleProgram) closureWithWork(seed []graph.Vertex, marks []uint32, generation *uint32, out []graph.Vertex) []graph.Vertex {
	if len(seed) == 0 {
		return nil
	}
	*generation++
	if *generation == 0 {
		clear(marks)
		*generation = 1
	}
	out = out[:0]
	for _, v := range seed {
		for _, item := range p.closureTable[v] {
			index, ok := p.index[item]
			if !ok || marks[index] == *generation {
				continue
			}
			marks[index] = *generation
			out = append(out, item)
		}
	}
	return out
}

// closureIndexWithWork 合并预计算的整数闭包，避免扫描热路径访问顶点映射。
func (p *castleProgram) closureIndexWithWork(seed []int, marks []uint32, generation *uint32, out []int) []int {
	if len(seed) == 0 {
		return nil
	}
	*generation++
	if *generation == 0 {
		clear(marks)
		*generation = 1
	}
	out = out[:0]
	for _, id := range seed {
		if id < 0 || id >= len(p.closureIndex) {
			continue
		}
		for _, target := range p.closureIndex[id] {
			if target < 0 || target >= len(marks) || marks[target] == *generation {
				continue
			}
			marks[target] = *generation
			out = append(out, target)
		}
	}
	return out
}

func (p *castleProgram) computeClosure(seed graph.Vertex) []graph.Vertex {
	seen := make(map[graph.Vertex]struct{})
	queue := []graph.Vertex{seed}
	for len(queue) > 0 {
		v := queue[0]
		queue = queue[1:]
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		n := p.graph.Nodes[v]
		if n == nil || n.Kind == nfagraph.KindLiteral || n.Kind == nfagraph.KindClass {
			continue
		}
		queue = append(queue, p.graph.Successors(v)...)
	}
	out := make([]graph.Vertex, 0, len(seen))
	for v := range seen {
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func castleMatches(n *nfagraph.Node, b byte) bool {
	if n == nil {
		return false
	}
	if n.Kind == nfagraph.KindLiteral {
		return len(n.Literal) == 1 && n.Literal[0] == b
	}
	if n.Kind != nfagraph.KindClass || n.Unicode != nil {
		return false
	}
	hit := false
	for _, r := range n.Class.Ranges {
		if b >= r.Lo && b <= r.Hi {
			hit = true
			break
		}
	}
	return hit != n.Class.Negated
}
