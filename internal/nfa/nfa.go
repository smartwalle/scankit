// Package nfa 执行通用 NFAGraph 表示。
package nfa

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"sort"
	"sync"
	"unicode/utf8"

	"github.com/smartwalle/scankit/internal/graph"
	"github.com/smartwalle/scankit/internal/nfagraph"
	"github.com/smartwalle/scankit/internal/parser"
)

const version = 1

// Program 是 NFA 执行后端，持有图、专用引擎布局与推导出的静态元数据。
type Program struct {
	Graph  *nfagraph.Graph
	castle *castleProgram
	dead   map[graph.Vertex]bool
	// minBytes/maxBytes 是从起点到任一接受节点的保守长度界。
	// maxBytes 为 -1 表示图中存在无法安全界定的循环路径。
	minBytes      int
	maxBytes      int
	boundsKnown   bool
	firstMask     [4]uint64
	acceptsEmpty  bool
	hasUnicode    bool
	hasReports    bool
	hasAssertions bool
	metadataKnown bool
	prefix        []byte
	classMasks    map[graph.Vertex][4]uint64
}

// Span 是一段匹配区间，From 与 To 为左闭右开偏移。
type Span struct{ From, To int }

type genericState struct {
	v      graph.Vertex
	pos    int
	report uint32
}

type genericReportEnd struct {
	report uint32
	pos    int
}

type genericWork struct {
	q        []genericState
	pending  map[genericState]struct{}
	seen     map[genericState]bool
	emitted  map[int]struct{}
	accepted map[genericReportEnd]struct{}
}

var genericWorkPool sync.Pool

func acquireGenericWork() *genericWork {
	w, _ := genericWorkPool.Get().(*genericWork)
	if w == nil {
		w = &genericWork{pending: make(map[genericState]struct{}), seen: make(map[genericState]bool), emitted: make(map[int]struct{}), accepted: make(map[genericReportEnd]struct{})}
	}
	if w.pending == nil {
		w.pending = make(map[genericState]struct{})
	}
	if w.seen == nil {
		w.seen = make(map[genericState]bool)
	}
	if w.emitted == nil {
		w.emitted = make(map[int]struct{})
	}
	if w.accepted == nil {
		w.accepted = make(map[genericReportEnd]struct{})
	}
	w.q = w.q[:0]
	for k := range w.pending {
		delete(w.pending, k)
	}
	for k := range w.seen {
		delete(w.seen, k)
	}
	for k := range w.emitted {
		delete(w.emitted, k)
	}
	for k := range w.accepted {
		delete(w.accepted, k)
	}
	return w
}

func releaseGenericWork(w *genericWork) {
	if w == nil {
		return
	}
	w.q = w.q[:0]
	if cap(w.q) > 1<<16 || len(w.pending) > 1<<16 || len(w.seen) > 1<<16 || len(w.accepted) > 1<<16 {
		w.q = nil
		w.pending = nil
		w.seen = nil
		w.emitted = nil
		w.accepted = nil
		return
	}
	genericWorkPool.Put(w)
}

// Empty 判断程序是否缺少可执行图。
func (p *Program) Empty() bool { return p == nil || p.Graph == nil }

// NodeCount 返回底层图节点数量。
func (p *Program) NodeCount() int {
	if p == nil || p.Graph == nil {
		return 0
	}
	return len(p.Graph.Nodes)
}

// EdgeCount 返回底层图边数量。
func (p *Program) EdgeCount() int {
	if p == nil || p.Graph == nil || p.Graph.Flow == nil {
		return 0
	}
	return p.Graph.Flow.EdgeCount()
}

// MemoryBytes 返回图结构的近似内存占用。
func (p *Program) MemoryBytes() uint64 {
	if p == nil || p.Graph == nil {
		return 0
	}
	size := saturatingAdd(saturatingMul(uint64(len(p.Graph.Nodes)), 64), saturatingMul(uint64(p.EdgeCount()), 16))
	for _, id := range p.Graph.NodeIDs() {
		n := p.Graph.Nodes[id]
		if n == nil {
			continue
		}
		size = saturatingAdd(size, uint64(len(n.Literal)))
		size = saturatingAdd(size, saturatingMul(uint64(len(n.Class.Ranges)), 4))
	}
	size = saturatingAdd(size, saturatingMul(uint64(len(p.classMasks)), 32))
	return size
}

// StartVertex 返回程序起始节点编号。
func (p *Program) StartVertex() (graph.Vertex, bool) {
	if p == nil || p.Graph == nil {
		return 0, false
	}
	return p.Graph.Start, true
}

// AcceptCount 返回图中接受节点数量。
func (p *Program) AcceptCount() int {
	if p == nil || p.Graph == nil {
		return 0
	}
	return p.Graph.AcceptCount()
}

// IsByteOnly 判断图是否只包含单字节可消费节点和控制连接节点。
func (p *Program) IsByteOnly() bool { return p != nil && p.Graph != nil && dfaLikeGraph(p.Graph) }

// HasAssertions 判断图中是否存在边界或断言节点。
func (p *Program) HasAssertions() bool {
	if p == nil || p.Graph == nil {
		return false
	}
	if p.metadataKnown {
		return p.hasAssertions
	}
	for _, id := range p.Graph.NodeIDs() {
		if p.Graph.Nodes[id].Kind == nfagraph.KindAssertion {
			return true
		}
	}
	return false
}

// HasReports 判断图中是否包含报告节点。
func (p *Program) HasReports() bool {
	if p == nil || p.Graph == nil {
		return false
	}
	if p.metadataKnown {
		return p.hasReports
	}
	for _, id := range p.Graph.NodeIDs() {
		if p.Graph.Nodes[id].Kind == nfagraph.KindReport {
			return true
		}
	}
	return false
}

// ReportIDs 返回图中报告节点携带的报告编号，升序去重。
// NFA 通用执行器在每个报告节点经过时暂存编号，
// 这里的快照用于与 DFA 后端做报告集合一致性校验。
func (p *Program) ReportIDs() []uint32 {
	if p == nil || p.Graph == nil {
		return nil
	}
	ids := make([]uint32, 0, len(p.Graph.Nodes))
	for _, id := range p.Graph.NodeIDs() {
		node := p.Graph.Nodes[id]
		if node == nil || node.Kind != nfagraph.KindReport || node.ReportID == 0 {
			continue
		}
		ids = append(ids, node.ReportID)
	}
	if len(ids) < 2 {
		return ids
	}
	slices.Sort(ids)
	out := ids[:1]
	for _, id := range ids[1:] {
		if id != out[len(out)-1] {
			out = append(out, id)
		}
	}
	return out
}

// HasUnicode 判断图中是否包含 Unicode 字符类。
func (p *Program) HasUnicode() bool {
	if p == nil || p.Graph == nil {
		return false
	}
	if p.metadataKnown {
		return p.hasUnicode
	}
	for _, id := range p.Graph.NodeIDs() {
		if p.Graph.Nodes[id].Unicode != nil {
			return true
		}
	}
	return false
}

func graphHasUnicode(g *nfagraph.Graph) bool {
	if g == nil {
		return false
	}
	for _, id := range g.NodeIDs() {
		if n := g.Nodes[id]; n != nil && n.Unicode != nil {
			return true
		}
	}
	return false
}

// AcceptsEmpty 判断程序是否能在起点产生零长度接受结果。
func (p *Program) AcceptsEmpty() bool {
	if p == nil {
		return false
	}
	if p.metadataKnown {
		return p.acceptsEmpty
	}
	return containsInt(p.MatchAt(nil, 0), 0)
}

func containsInt(values []int, target int) bool {
	return slices.Contains(values, target)
}

func computeGraphDead(g *nfagraph.Graph) map[graph.Vertex]bool {
	dead := make(map[graph.Vertex]bool)
	if g == nil || g.Flow == nil {
		return dead
	}
	reverse := make(map[graph.Vertex][]graph.Vertex, len(g.Nodes))
	reachable := make(map[graph.Vertex]bool, len(g.Nodes))
	queue := make([]graph.Vertex, 0)
	for id, node := range g.Nodes {
		if node != nil && node.Kind == nfagraph.KindAccept {
			reachable[id] = true
			queue = append(queue, id)
		}
		for _, to := range g.Flow.Successors(id) {
			reverse[to] = append(reverse[to], id)
		}
	}
	for head := 0; head < len(queue); head++ {
		for _, from := range reverse[queue[head]] {
			if !reachable[from] {
				reachable[from] = true
				queue = append(queue, from)
			}
		}
	}
	for id := range g.Nodes {
		if !reachable[id] {
			dead[id] = true
		}
	}
	return dead
}

// HasNode 判断节点编号是否存在于程序图中。
func (p *Program) HasNode(id graph.Vertex) bool {
	return p != nil && p.Graph != nil && p.Graph.HasNode(id)
}

// Dump 序列化 NFA 程序。
func (p *Program) Dump() ([]byte, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	raw, err := nfagraph.Dump(p.Graph)
	if err != nil {
		return nil, err
	}
	return json.Marshal(struct {
		Version int    `json:"version"`
		Graph   []byte `json:"graph"`
	}{version, raw})
}

// Load 反序列化 NFA 程序。
func Load(data []byte) (*Program, error) {
	if len(data) > 64<<20 {
		return nil, fmt.Errorf("nfa payload exceeds size limit")
	}
	var d struct {
		Version int    `json:"version"`
		Graph   []byte `json:"graph"`
	}
	if err := json.Unmarshal(data, &d); err != nil {
		return nil, err
	}
	if d.Version != version {
		return nil, fmt.Errorf("unsupported nfa version %d", d.Version)
	}
	g, err := nfagraph.Load(d.Graph)
	if err != nil {
		return nil, err
	}
	return Compile(g)
}

// Validate 检查程序及其图结构。
func (p *Program) Validate() error {
	if p == nil || p.Graph == nil {
		return fmt.Errorf("nil nfa program")
	}
	if len(p.Graph.Nodes) > 1<<20 || p.Graph.Flow == nil || p.Graph.Flow.EdgeCount() > 1<<22 {
		return fmt.Errorf("nfa graph exceeds execution limits")
	}
	if p.dead != nil {
		for id := range p.dead {
			if p.Graph.Nodes[id] == nil {
				return fmt.Errorf("nfa dead-state index out of bounds")
			}
		}
		want := computeGraphDead(p.Graph)
		for id, dead := range want {
			if p.dead[id] != dead {
				return fmt.Errorf("nfa dead-state map mismatch")
			}
		}
		for id := range p.dead {
			if _, ok := want[id]; !ok {
				return fmt.Errorf("nfa dead-state map contains unknown state")
			}
		}
	}
	if p.boundsKnown {
		minBytes, maxBytes := graphLengthBounds(p.Graph)
		if p.minBytes != minBytes || p.maxBytes != maxBytes {
			return fmt.Errorf("nfa length bounds mismatch")
		}
		if p.minBytes < 0 || p.maxBytes < -1 || p.maxBytes >= 0 && p.maxBytes < p.minBytes {
			return fmt.Errorf("nfa length bounds are invalid")
		}
	}
	if p.metadataKnown {
		if p.firstMask != firstByteMask(p.Graph) || p.acceptsEmpty != graphAcceptsEmpty(p.Graph) || p.hasUnicode != graphHasUnicode(p.Graph) || p.hasReports != graphHasReports(p.Graph) || p.hasAssertions != graphHasAssertions(p.Graph) {
			return fmt.Errorf("nfa start metadata mismatch")
		}
		if !bytes.Equal(p.prefix, requiredLiteralPrefix(p.Graph)) {
			return fmt.Errorf("nfa prefix metadata mismatch")
		}
	}
	if p.classMasks != nil {
		expectedMasks := 0
		for _, id := range p.Graph.NodeIDs() {
			if n := p.Graph.Nodes[id]; n != nil && n.Kind == nfagraph.KindClass && n.Unicode == nil {
				expectedMasks++
			}
		}
		if len(p.classMasks) != expectedMasks {
			return fmt.Errorf("nfa class mask count mismatch")
		}
		for id, mask := range p.classMasks {
			n := p.Graph.Nodes[id]
			if n == nil || n.Kind != nfagraph.KindClass || n.Unicode != nil {
				return fmt.Errorf("nfa class mask node mismatch")
			}
			for b := range 256 {
				want := castleMatches(n, byte(b))
				got := mask[b/64]&(1<<uint(b%64)) != 0
				if want != got {
					return fmt.Errorf("nfa class mask mismatch")
				}
			}
		}
	}
	return p.Graph.Validate()
}

func graphHasReports(g *nfagraph.Graph) bool {
	if g == nil {
		return false
	}
	for _, id := range g.NodeIDs() {
		if n := g.Nodes[id]; n != nil && n.Kind == nfagraph.KindReport {
			return true
		}
	}
	return false
}

func graphHasAssertions(g *nfagraph.Graph) bool {
	if g == nil {
		return false
	}
	for _, id := range g.NodeIDs() {
		if n := g.Nodes[id]; n != nil && n.Kind == nfagraph.KindAssertion {
			return true
		}
	}
	return false
}

// Clone 深拷贝程序，结果与原程序不共享图与缓存布局。
func (p *Program) Clone() *Program {
	if p == nil || p.Graph == nil {
		return nil
	}
	graphClone := p.Graph.Clone()
	out := &Program{Graph: graphClone, dead: computeGraphDead(graphClone), minBytes: p.minBytes, maxBytes: p.maxBytes, boundsKnown: p.boundsKnown, firstMask: p.firstMask, acceptsEmpty: p.acceptsEmpty, hasUnicode: p.hasUnicode, hasReports: p.hasReports, hasAssertions: p.hasAssertions, metadataKnown: p.metadataKnown, prefix: append([]byte(nil), p.prefix...), classMasks: cloneClassMasks(p.classMasks)}
	if p.castle != nil {
		// Castle 使用展开后的一字节图；克隆时保持与原程序相同的专用路径。
		out.castle = newCastleProgram(nfagraph.ExpandLiterals(out.Graph))
	}
	return out
}

// Compile 将 NFA 图编译为执行程序，g 为 nil 时返回错误。
func Compile(g *nfagraph.Graph) (*Program, error) {
	if g == nil {
		return nil, fmt.Errorf("nil graph")
	}
	if err := g.Validate(); err != nil {
		return nil, err
	}
	minBytes, maxBytes := graphLengthBounds(g)
	classMasks := make(map[graph.Vertex][4]uint64)
	for _, id := range g.NodeIDs() {
		if n := g.Nodes[id]; n != nil && n.Kind == nfagraph.KindClass && n.Unicode == nil {
			var mask [4]uint64
			for b := range 256 {
				if castleMatches(n, byte(b)) {
					mask[b/64] |= 1 << uint(b%64)
				}
			}
			classMasks[id] = mask
		}
	}
	p := &Program{Graph: g, dead: computeGraphDead(g), minBytes: minBytes, maxBytes: maxBytes, boundsKnown: true, firstMask: firstByteMask(g), acceptsEmpty: graphAcceptsEmpty(g), hasUnicode: graphHasUnicode(g), hasReports: graphHasReports(g), hasAssertions: graphHasAssertions(g), metadataKnown: true, prefix: requiredLiteralPrefix(g), classMasks: classMasks}
	expanded := nfagraph.ExpandLiterals(g)
	if castleEligible(expanded) {
		p.castle = newCastleProgram(expanded)
	}
	return p, nil
}

// graphLengthBounds 计算从起点到接受节点的字节长度界。
// 最小值使用反向最短路松弛；最大值仅在无环图上计算，遇到循环时返回 -1，
// 这样不会把可能无限增长的路径错误地截断。
func graphLengthBounds(g *nfagraph.Graph) (int, int) {
	if g == nil || g.Flow == nil || g.Nodes[g.Start] == nil {
		return 0, -1
	}
	const inf = int(^uint(0) >> 1)
	ids := g.NodeIDs()
	dist := make(map[graph.Vertex]int, len(ids))
	for _, id := range ids {
		dist[id] = inf
	}
	for _, id := range ids {
		if n := g.Nodes[id]; n != nil && n.Kind == nfagraph.KindAccept {
			dist[id] = 0
		}
	}
	// 图规模受 Validate 限制，|V| 次松弛足以求得无负权最短路。
	for range ids {
		changed := false
		for _, id := range ids {
			n := g.Nodes[id]
			if n == nil {
				continue
			}
			cost := nodeMinWidth(n)
			best := dist[id]
			for _, to := range g.Flow.Successors(id) {
				if v := dist[to]; v != inf && v <= inf-cost && cost+v < best {
					best = cost + v
				}
			}
			if best != dist[id] {
				dist[id], changed = best, true
			}
		}
		if !changed {
			break
		}
	}
	minStart := dist[g.Start]
	if minStart == inf {
		minStart = 0
	}
	// 只对从起点可达且能到达接受节点的节点做 Kahn 拓扑排序；
	// 无法到达接受节点的死循环不会把整个图误判为无界。
	dead := computeGraphDead(g)
	if dead[g.Start] {
		return minStart, -1
	}
	reachable := make(map[graph.Vertex]bool, len(ids))
	queue := []graph.Vertex{g.Start}
	reachable[g.Start] = true
	for head := 0; head < len(queue); head++ {
		for _, to := range g.Flow.Successors(queue[head]) {
			if !dead[to] && !reachable[to] {
				reachable[to] = true
				queue = append(queue, to)
			}
		}
	}
	indegree := make(map[graph.Vertex]int, len(reachable))
	for id := range reachable {
		for _, to := range g.Flow.Successors(id) {
			if reachable[to] {
				indegree[to]++
			}
		}
	}
	topo := make([]graph.Vertex, 0, len(reachable))
	ready := make([]graph.Vertex, 0)
	for id, degree := range indegree {
		if degree == 0 {
			ready = append(ready, id)
		}
	}
	for head := 0; head < len(ready); head++ {
		id := ready[head]
		topo = append(topo, id)
		for _, to := range g.Flow.Successors(id) {
			if !reachable[to] {
				continue
			}
			indegree[to]--
			if indegree[to] == 0 {
				ready = append(ready, to)
			}
		}
	}
	if len(topo) != len(reachable) {
		return minStart, -1
	}
	maxLen := make(map[graph.Vertex]int, len(reachable))
	for _, id := range slices.Backward(topo) {

		n := g.Nodes[id]
		best := -1
		if n != nil && n.Kind == nfagraph.KindAccept {
			best = 0
		}
		for _, to := range g.Flow.Successors(id) {
			if v, ok := maxLen[to]; ok && v >= 0 {
				width := nodeMaxWidth(n)
				if v > inf-width {
					v = inf
				} else {
					v += width
				}
				if v > best {
					best = v
				}
			}
		}
		maxLen[id] = best
	}
	return minStart, maxLen[g.Start]
}

func nodeMinWidth(n *nfagraph.Node) int {
	if n == nil {
		return 0
	}
	switch n.Kind {
	case nfagraph.KindLiteral:
		return len(n.Literal)
	case nfagraph.KindClass:
		return 1
	default:
		return 0
	}
}

func nodeMaxWidth(n *nfagraph.Node) int {
	if n == nil {
		return 0
	}
	switch n.Kind {
	case nfagraph.KindLiteral:
		return len(n.Literal)
	case nfagraph.KindClass:
		if n.Unicode != nil {
			return utf8.UTFMax
		}
		return 1
	default:
		return 0
	}
}

func cloneClassMasks(src map[graph.Vertex][4]uint64) map[graph.Vertex][4]uint64 {
	if src == nil {
		return nil
	}
	dst := make(map[graph.Vertex][4]uint64, len(src))
	maps.Copy(dst, src)
	return dst
}

// MatchAt 返回从指定起点匹配时所有被接受的结束偏移。
func (p *Program) MatchAt(data []byte, start int) []int {
	if p != nil && p.metadataKnown && !p.HasUnicode() && start >= 0 && start < len(data) && !p.acceptsEmpty && len(p.prefix) == 0 && !byteMaskEmpty(p.firstMask) && !hasFirstByte(p.firstMask, data[start]) {
		return nil
	}
	return p.MatchAtLimit(data, start, 0)
}

func (p *Program) startWithinBounds(data []byte, start int) bool {
	if p == nil || start < 0 || start > len(data) {
		return false
	}
	return !p.boundsKnown || p.minBytes <= len(data)-start
}

// LongestMatchAt 返回指定起点的最长接受结束偏移。
func (p *Program) LongestMatchAt(data []byte, start int) (int, bool) {
	ends := p.MatchAt(data, start)
	if len(ends) == 0 {
		return 0, false
	}
	return ends[len(ends)-1], true
}

// MatchAtWithFlags 在指定多行语义下执行一次匹配。
func (p *Program) MatchAtWithFlags(data []byte, start int, multiline bool) []int {
	return p.matchAtLimit(data, start, 0, multiline, false)
}

// MatchAtWithOptions 在指定多行和 ASCII 大小写折叠语义下执行匹配。
func (p *Program) MatchAtWithOptions(data []byte, start int, multiline, caseless bool) []int {
	return p.matchAtLimit(data, start, 0, multiline, caseless)
}

// MatchAtLimit 在运行状态预算内执行匹配，超过预算时返回当前已收集结果。
func (p *Program) MatchAtLimit(data []byte, start int, limit int) []int {
	if p != nil && p.castle != nil && castleRuntimeShapeCached(p.castle) {
		if limit == 0 {
			return p.castle.MatchAt(data, start)
		}
		ends, _, _ := p.castle.MatchAtBudget(data, start, 0, limit)
		return ends
	}
	return p.matchAtLimit(data, start, limit, false, false)
}

// MatchAtBudget 在实际状态步数和结果数量预算内执行一次匹配。
func (p *Program) MatchAtBudget(data []byte, start, maxSteps, maxResults int) ([]int, int, bool) {
	if p == nil || p.Graph == nil || p.Graph.Flow == nil || start < 0 || start > len(data) || maxSteps < 0 || maxResults < 0 {
		return nil, 0, false
	}
	return p.matchAtLimitBudget(data, start, maxResults, false, false, maxSteps)
}

// MatchAtResultLimit 返回指定起点的接受结束位置，并限制结果数量。
func (p *Program) MatchAtResultLimit(data []byte, start, limit int) []int {
	if limit < 0 || p == nil {
		return nil
	}
	if limit == 0 {
		return p.MatchAt(data, start)
	}
	ends, _, _ := p.matchAtLimitBudget(data, start, limit, false, false, 0)
	return ends
}

// LongestAt 返回指定起点的最长接受结束位置。
func (p *Program) LongestAt(data []byte, start int) (int, bool) {
	ends := p.MatchAt(data, start)
	if len(ends) == 0 {
		return 0, false
	}
	return ends[len(ends)-1], true
}

func (p *Program) matchAtLimit(data []byte, start int, limit int, multiline, caseless bool) []int {
	ends, _, _ := p.matchAtLimitBudget(data, start, limit, multiline, caseless, 0)
	return ends
}

// matchAtLimitBudget 在队列状态实际出队时计步，避免先估算后无限执行造成
// 状态爆炸；maxSteps 为零表示不限制步骤。
func (p *Program) matchAtLimitBudget(data []byte, start int, limit int, multiline, caseless bool, maxSteps int) ([]int, int, bool) {
	if p == nil || p.Graph == nil || p.Graph.Flow == nil || start < 0 || start > len(data) || limit < 0 {
		return nil, 0, false
	}
	if p.boundsKnown && p.minBytes > 0 && len(data)-start < p.minBytes {
		return nil, 0, false
	}
	if p.metadataKnown && !caseless && len(p.prefix) > 0 && !prefixMatchesAt(data, start, p.prefix) {
		return nil, 0, false
	}
	if p.metadataKnown && !caseless && !p.HasUnicode() && len(p.prefix) == 0 && start < len(data) && !p.acceptsEmpty && !byteMaskEmpty(p.firstMask) && !hasFirstByte(p.firstMask, data[start]) {
		return nil, 0, false
	}
	endLimit := len(data)
	if p.boundsKnown && p.maxBytes >= 0 && p.maxBytes < endLimit-start {
		endLimit = start + p.maxBytes
	}
	work := acquireGenericWork()
	defer releaseGenericWork(work)
	q := append(work.q, genericState{v: p.Graph.Start, pos: start})
	work.q = q
	head := 0
	const queueLimit = 1 << 20
	pending := work.pending
	pending[genericState{v: p.Graph.Start, pos: start}] = struct{}{}
	enqueue := func(next genericState) bool {
		// 死状态不可能到达接受节点，入队前直接裁剪可避免
		// 在大分支图中积累无效状态。
		if p.dead != nil && p.dead[next.v] {
			return true
		}
		if _, exists := pending[next]; exists {
			return true
		}
		if len(q)-head >= queueLimit {
			return false
		}
		pending[next] = struct{}{}
		q = append(q, next)
		return true
	}
	seen := work.seen
	emitted := work.emitted
	var out []int
	steps := 0
	for head < len(q) {
		s := q[head]
		head++
		if head > 1024 && head*2 >= len(q) {
			copy(q, q[head:])
			q = q[:len(q)-head]
			head = 0
		}
		delete(pending, s)
		if seen[s] {
			continue
		}
		steps++
		if maxSteps > 0 && steps > maxSteps {
			return nil, steps, true
		}
		seen[s] = true
		if len(seen) > queueLimit {
			return out, steps, true
		}
		if p.dead != nil && p.dead[s.v] {
			continue
		}
		n := p.Graph.Nodes[s.v]
		if n == nil {
			continue
		}
		if n.Kind == nfagraph.KindAccept {
			key := genericReportEnd{report: s.report, pos: s.pos}
			if _, exists := work.accepted[key]; exists {
				continue
			}
			work.accepted[key] = struct{}{}
			if _, exists := emitted[s.pos]; exists {
				continue
			}
			out = append(out, s.pos)
			emitted[s.pos] = struct{}{}
			if limit > 0 && len(out) >= limit {
				return orderedEnds(out), steps, true
			}
			continue
		}
		// 后继列表在本次状态出队期间保持不变，只读视图避免每个状态复制切片。
		successors := p.Graph.Flow.SuccessorView(s.v)
		switch n.Kind {
		case nfagraph.KindLiteral:
			if s.pos >= 0 && s.pos <= endLimit && len(n.Literal) <= endLimit-s.pos && equalLiteral(data[s.pos:s.pos+len(n.Literal)], n.Literal, caseless) {
				for _, to := range successors {
					if !enqueue(genericState{v: to, pos: s.pos + len(n.Literal), report: s.report}) {
						return out, steps, true
					}
				}
			}
		case nfagraph.KindClass:
			if n.Unicode != nil {
				if s.pos >= len(data) {
					continue
				}
				r, size := utf8.DecodeRune(data[s.pos:])
				if r == utf8.RuneError && size == 1 && data[s.pos] >= 0x80 {
					continue
				}
				// 属性名在解析阶段已解析成判定函数，运行时不再重复规范化名称。
				property := n.Unicode.Resolved
				if property == nil {
					property = parser.ResolveUnicodeProperty(n.Unicode.Name)
				}
				ok := property.Match(r)
				if n.Unicode.Negated {
					ok = !ok
				}
				if !ok {
					continue
				}
				for _, to := range successors {
					if !enqueue(genericState{v: to, pos: s.pos + size, report: s.report}) {
						return out, steps, true
					}
				}
				continue
			}
			matched := false
			if s.pos < endLimit {
				if !caseless && p.classMasks != nil {
					mask, ok := p.classMasks[s.v]
					matched = ok && mask[data[s.pos]/64]&(1<<uint(data[s.pos]%64)) != 0
				} else {
					matched = inClass(data[s.pos], n.Class, caseless)
				}
			}
			if matched {
				for _, to := range successors {
					if !enqueue(genericState{v: to, pos: s.pos + 1, report: s.report}) {
						return out, steps, true
					}
				}
			}
		case nfagraph.KindAssertion:
			// 图中无法表达查找断言的子表达式时，节点使用零值标记；
			// 该情况必须拒绝而不是当作无条件空转移。
			if n.Assertion == 0 || !assertion(n.Assertion, data, s.pos, multiline) {
				continue
			}
			for _, to := range successors {
				if !enqueue(genericState{v: to, pos: s.pos, report: s.report}) {
					return out, steps, true
				}
			}
		case nfagraph.KindReport:
			nextReport := s.report
			if n.ReportID != 0 {
				nextReport = n.ReportID
			}
			for _, to := range successors {
				if !enqueue(genericState{v: to, pos: s.pos, report: nextReport}) {
					return out, steps, true
				}
			}
		default:
			for _, to := range successors {
				if !enqueue(genericState{v: to, pos: s.pos, report: s.report}) {
					return out, steps, true
				}
			}
		}
	}
	sort.Ints(out)
	return orderedEnds(out), steps, false
}

func orderedEnds(ends []int) []int {
	if len(ends) < 2 {
		return ends
	}
	sort.Ints(ends)
	return dedupInts(ends)
}

func equalLiteral(got, want []byte, caseless bool) bool {
	if !caseless {
		return bytes.Equal(got, want)
	}
	for i := range want {
		g, w := got[i], want[i]
		if g >= 'A' && g <= 'Z' {
			g += 'a' - 'A'
		}
		if w >= 'A' && w <= 'Z' {
			w += 'a' - 'A'
		}
		if g != w {
			return false
		}
	}
	return true
}

// Match 返回 data 全部起点上的结束偏移，按数值升序去重。
func (p *Program) Match(data []byte) []int {
	if p == nil {
		return nil
	}
	var out []int
	seen := map[int]struct{}{}
	prefix := p.prefix
	if !p.metadataKnown {
		prefix = requiredLiteralPrefix(p.Graph)
	}
	forEachPrefixStart(data, prefix, func(start int) bool {
		if !p.startWithinBounds(data, start) {
			return true
		}
		if p.metadataKnown && !p.HasUnicode() && len(prefix) == 0 && start < len(data) && !p.acceptsEmpty && !byteMaskEmpty(p.firstMask) && !hasFirstByte(p.firstMask, data[start]) {
			return true
		}
		for _, end := range p.MatchAt(data, start) {
			if _, ok := seen[end]; ok {
				continue
			}
			seen[end] = struct{}{}
			out = append(out, end)
		}
		return true
	})
	return out
}

// MatchLimit 返回不超过 limit 个结束偏移；零值表示不限制。
func (p *Program) MatchLimit(data []byte, limit int) []int {
	if limit < 0 {
		return nil
	}
	if limit == 0 {
		return p.Match(data)
	}
	out := make([]int, 0, limit)
	seen := make(map[int]struct{}, limit)
	prefix := p.prefix
	if !p.metadataKnown {
		prefix = requiredLiteralPrefix(p.Graph)
	}
	forEachPrefixStart(data, prefix, func(start int) bool {
		if !p.startWithinBounds(data, start) {
			return true
		}
		if len(out) >= limit {
			return false
		}
		if p.metadataKnown && !p.HasUnicode() && len(prefix) == 0 && start < len(data) && !p.acceptsEmpty && !byteMaskEmpty(p.firstMask) && !hasFirstByte(p.firstMask, data[start]) {
			return true
		}
		for _, end := range p.MatchAt(data, start) {
			if _, ok := seen[end]; ok {
				continue
			}
			seen[end] = struct{}{}
			out = append(out, end)
			if len(out) >= limit {
				return false
			}
		}
		return true
	})
	return out
}

// Spans 返回 data 中全部匹配区间，按起点和终点升序去重。
func (p *Program) Spans(data []byte) []Span {
	if p == nil {
		return nil
	}
	var out []Span
	seen := map[Span]struct{}{}
	prefix := p.prefix
	if !p.metadataKnown {
		prefix = requiredLiteralPrefix(p.Graph)
	}
	forEachPrefixStart(data, prefix, func(start int) bool {
		if !p.startWithinBounds(data, start) {
			return true
		}
		if p.metadataKnown && !p.HasUnicode() && len(prefix) == 0 && start < len(data) && !p.acceptsEmpty && !hasFirstByte(p.firstMask, data[start]) {
			return true
		}
		for _, end := range p.MatchAt(data, start) {
			s := Span{start, end}
			if _, ok := seen[s]; !ok {
				seen[s] = struct{}{}
				out = append(out, s)
			}
		}
		return true
	})
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].From != out[j].From {
			return out[i].From < out[j].From
		}
		return out[i].To < out[j].To
	})
	return out
}

// SpansRange 返回完全位于半开区间内的匹配区间。
func (p *Program) SpansRange(data []byte, from, to int) []Span {
	if p == nil || from < 0 || to < from || to > len(data) {
		return nil
	}
	out := make([]Span, 0)
	seen := make(map[Span]struct{})
	for start := from; start <= to; start++ {
		if !p.startWithinBounds(data, start) {
			continue
		}
		if p.metadataKnown {
			if len(p.prefix) > 0 {
				if !prefixMatchesAt(data, start, p.prefix) {
					continue
				}
			} else if !p.HasUnicode() && start < len(data) && !p.acceptsEmpty && !byteMaskEmpty(p.firstMask) && !hasFirstByte(p.firstMask, data[start]) {
				continue
			}
		}
		for _, end := range p.MatchAt(data, start) {
			if end < from || end > to {
				continue
			}
			span := Span{From: start, To: end}
			if _, exists := seen[span]; exists {
				continue
			}
			seen[span] = struct{}{}
			out = append(out, span)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].From != out[j].From {
			return out[i].From < out[j].From
		}
		return out[i].To < out[j].To
	})
	return out
}

// SpansLimit 返回不超过 limit 个匹配区间；零值表示不限制。
func (p *Program) SpansLimit(data []byte, limit int) []Span {
	if limit < 0 {
		return nil
	}
	if limit == 0 {
		return p.Spans(data)
	}
	if p == nil {
		return nil
	}
	out := make([]Span, 0, limit)
	seen := make(map[Span]struct{}, limit)
	prefix := p.prefix
	if !p.metadataKnown {
		prefix = requiredLiteralPrefix(p.Graph)
	}
	forEachPrefixStart(data, prefix, func(start int) bool {
		if !p.startWithinBounds(data, start) {
			return true
		}
		if len(out) >= limit {
			return false
		}
		if p.metadataKnown && !p.HasUnicode() && len(prefix) == 0 && start < len(data) && !p.acceptsEmpty && !byteMaskEmpty(p.firstMask) && !hasFirstByte(p.firstMask, data[start]) {
			return true
		}
		for _, end := range p.MatchAt(data, start) {
			span := Span{From: start, To: end}
			if _, ok := seen[span]; ok {
				continue
			}
			seen[span] = struct{}{}
			out = append(out, span)
			if len(out) >= limit {
				return false
			}
		}
		return true
	})
	return out
}

// SpansBudget 在状态步数和结果数量预算内扫描全部起点。
func (p *Program) SpansBudget(data []byte, maxSteps, maxResults int) (spans []Span, steps int, stopped bool) {
	if p == nil || maxSteps < 0 || maxResults < 0 {
		return nil, 0, false
	}
	for start := 0; start <= len(data); start++ {
		if !p.startWithinBounds(data, start) {
			continue
		}
		if p.metadataKnown {
			if len(p.prefix) > 0 {
				if !prefixMatchesAt(data, start, p.prefix) {
					continue
				}
			} else if !p.HasUnicode() && start < len(data) && !p.acceptsEmpty && !byteMaskEmpty(p.firstMask) && !hasFirstByte(p.firstMask, data[start]) {
				continue
			}
		}
		stepBudget, resultBudget := 0, 0
		if maxSteps > 0 {
			stepBudget = maxSteps - steps
			// 零值只在调用方未设置全局步骤预算时表示不限额；
			// 已启用预算后剩余额度为零必须立即停止。
			if stepBudget <= 0 {
				return spans, steps, true
			}
		}
		if maxResults > 0 {
			resultBudget = maxResults - len(spans)
			if resultBudget <= 0 {
				return spans, steps, true
			}
		}
		ends, used, cut := p.MatchAtBudget(data, start, stepBudget, resultBudget)
		if stepBudget > 0 && used > stepBudget {
			used = stepBudget
		}
		steps += used
		for _, end := range ends {
			spans = append(spans, Span{From: start, To: end})
		}
		if cut {
			return spans, steps, true
		}
	}
	return spans, steps, false
}

// ScanEach 按起点、终点顺序回调匹配区间；回调返回 false 时提前结束。
func (p *Program) ScanEach(data []byte, fn func(Span) bool) {
	if p == nil || fn == nil {
		return
	}
	seen := map[Span]struct{}{}
	prefix := p.prefix
	if !p.metadataKnown {
		prefix = requiredLiteralPrefix(p.Graph)
	}
	forEachPrefixStart(data, prefix, func(start int) bool {
		if !p.startWithinBounds(data, start) {
			return true
		}
		if p.metadataKnown && !p.HasUnicode() && len(prefix) == 0 && start < len(data) && !p.acceptsEmpty && !byteMaskEmpty(p.firstMask) && !hasFirstByte(p.firstMask, data[start]) {
			return true
		}
		for _, end := range p.MatchAt(data, start) {
			span := Span{From: start, To: end}
			if _, ok := seen[span]; ok {
				continue
			}
			seen[span] = struct{}{}
			if !fn(span) {
				return false
			}
		}
		return true
	})
}

// MatchFirst 返回最早出现的匹配起点与结束偏移。
func (p *Program) MatchFirst(data []byte) (int, int, bool) {
	if p == nil {
		return 0, 0, false
	}
	prefix := p.prefix
	if !p.metadataKnown {
		prefix = requiredLiteralPrefix(p.Graph)
	}
	var from, to int
	found := false
	forEachPrefixStart(data, prefix, func(i int) bool {
		if !p.startWithinBounds(data, i) {
			return true
		}
		if p.metadataKnown && !p.HasUnicode() && len(prefix) == 0 && i < len(data) && !p.acceptsEmpty && !byteMaskEmpty(p.firstMask) && !hasFirstByte(p.firstMask, data[i]) {
			return true
		}
		for _, e := range p.MatchAt(data, i) {
			from, to, found = i, e, true
			return false
		}
		return true
	})
	if found {
		return from, to, true
	}
	return 0, 0, false
}

// Matches 报告输入中是否存在可接受的起点。
func (p *Program) Matches(data []byte) bool {
	_, _, ok := p.MatchFirst(data)
	return ok
}

// MatchAtFirst 返回指定起点最早被接受的结束偏移。
func (p *Program) MatchAtFirst(data []byte, start int) (int, bool) {
	ends := p.MatchAt(data, start)
	if len(ends) == 0 {
		return 0, false
	}
	return ends[0], true
}

// MatchAtRange 返回结束偏移位于半开区间内的结果。
func (p *Program) MatchAtRange(data []byte, start, from, to, limit int) []int {
	if limit < 0 || start < 0 || start > len(data) || from < 0 || to < from || to > len(data) {
		return nil
	}
	if !p.startWithinBounds(data, start) {
		return nil
	}
	out := make([]int, 0)
	for _, end := range p.MatchAt(data, start) {
		if end < from || end > to {
			continue
		}
		out = append(out, end)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out
}

// MinEnd 返回指定起点的最早结束偏移。
func (p *Program) MinEnd(data []byte, start int) (int, bool) { return p.MatchAtFirst(data, start) }

// MaxEnd 返回指定起点的最晚结束偏移。
func (p *Program) MaxEnd(data []byte, start int) (int, bool) {
	ends := p.MatchAt(data, start)
	if len(ends) == 0 {
		return 0, false
	}
	return ends[len(ends)-1], true
}

// AcceptsAt 判断指定起点能否恰好结束在 end。
func (p *Program) AcceptsAt(data []byte, start int, end int) bool {
	return slices.Contains(p.MatchAt(data, start), end)
}
func inClass(c byte, class parser.Class, caseless bool) bool {
	matched := false
	for _, r := range class.Ranges {
		if c >= r.Lo && c <= r.Hi || caseless && ((c >= 'a' && c <= 'z' && c-'a'+'A' >= r.Lo && c-'a'+'A' <= r.Hi) || (c >= 'A' && c <= 'Z' && c-'A'+'a' >= r.Lo && c-'A'+'a' <= r.Hi)) {
			matched = true
			break
		}
	}
	return matched != class.Negated
}
func assertion(kind parser.AssertionKind, data []byte, pos int, multiline bool) bool {
	switch kind {
	case parser.Begin:
		return pos == 0 || multiline && pos > 0 && data[pos-1] == '\n'
	case parser.BeginAbsolute:
		return pos == 0
	case parser.End:
		return pos == len(data) || multiline && pos < len(data) && data[pos] == '\n'
	case parser.EndAbsolute:
		return pos == len(data)
	case parser.EndBeforeFinalNewline:
		return pos == len(data) || pos+1 == len(data) && data[pos] == '\n' || pos+2 == len(data) && data[pos] == '\r' && data[pos+1] == '\n'
	case parser.WordBoundary:
		return (pos > 0 && word(data[pos-1])) != (pos < len(data) && word(data[pos]))
	case parser.NonWordBoundary:
		return (pos > 0 && word(data[pos-1])) == (pos < len(data) && word(data[pos]))
	default:
	}
	return false
}
func word(c byte) bool {
	return c == '_' || c >= '0' && c <= '9' || c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z'
}
