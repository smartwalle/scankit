package nfagraph

import (
	"reflect"
	"slices"
	"sort"

	"github.com/smartwalle/scankit/internal/graph"
)

// Analysis 保存编译阶段使用的确定性图属性。
type Analysis struct {
	Depth          map[graph.Vertex]int
	Dominators     map[graph.Vertex]map[graph.Vertex]struct{}
	PostDominators map[graph.Vertex]map[graph.Vertex]struct{}
	SCC            [][]graph.Vertex
	Regions        [][]graph.Vertex
	ReverseDepth   map[graph.Vertex]int
	// LoopNodes 标记位于循环中的节点；LoopWidth 给出循环中节点数量。
	LoopNodes map[graph.Vertex]bool
	LoopWidth map[graph.Vertex]int
}

// Dominates 判断一个节点是否支配另一个节点。
func (a *Analysis) Dominates(from, to graph.Vertex) bool {
	if a == nil {
		return false
	}
	_, ok := a.Dominators[to][from]
	return ok
}

// PostDominates 判断一个节点是否后支配另一个节点。
func (a *Analysis) PostDominates(from, to graph.Vertex) bool {
	if a == nil {
		return false
	}
	_, ok := a.PostDominators[to][from]
	return ok
}

// DepthOf 返回节点的最短起点深度；不可达节点为 -1。
func (a *Analysis) DepthOf(v graph.Vertex) int {
	if a == nil {
		return -1
	}
	return a.Depth[v]
}

// ReverseDepthOf 返回节点到接受节点的最短反向深度。
func (a *Analysis) ReverseDepthOf(v graph.Vertex) int {
	if a == nil {
		return -1
	}
	return a.ReverseDepth[v]
}

// RegionCount 返回包含环的强连通区域数量。
func (a *Analysis) RegionCount() int {
	if a == nil {
		return 0
	}
	return len(a.Regions)
}

// InCycle 判断节点是否处于强连通循环区域。
func (a *Analysis) InCycle(v graph.Vertex) bool {
	return a != nil && a.LoopNodes[v]
}

// CycleWidth 返回节点所在循环区域的节点规模。
func (a *Analysis) CycleWidth(v graph.Vertex) int {
	if a == nil {
		return 0
	}
	return a.LoopWidth[v]
}

// AcceptReachable 返回可从起点到达的接受节点快照。
func AcceptReachable(g *Graph) []graph.Vertex {
	if g == nil || g.Flow == nil {
		return nil
	}
	reachable := map[graph.Vertex]struct{}{}
	for _, id := range g.Flow.BFS(g.Start) {
		reachable[id] = struct{}{}
	}
	out := make([]graph.Vertex, 0)
	for _, id := range g.Accepts() {
		if _, ok := reachable[id]; ok {
			out = append(out, id)
		}
	}
	slices.Sort(out)
	return out
}

// Analyze 计算深度、支配关系和强连通分量。
func Analyze(g *Graph) *Analysis {
	a := &Analysis{Depth: make(map[graph.Vertex]int), ReverseDepth: make(map[graph.Vertex]int), Dominators: make(map[graph.Vertex]map[graph.Vertex]struct{}), PostDominators: make(map[graph.Vertex]map[graph.Vertex]struct{}), LoopNodes: make(map[graph.Vertex]bool), LoopWidth: make(map[graph.Vertex]int)}
	if g == nil || g.Flow == nil {
		return a
	}
	for _, v := range g.Flow.Vertices() {
		a.Depth[v] = -1
	}
	a.Depth[g.Start] = 0
	for _, v := range g.Flow.BFS(g.Start) {
		for _, w := range g.Flow.Successors(v) {
			if a.Depth[w] < 0 || a.Depth[w] > a.Depth[v]+1 {
				a.Depth[w] = a.Depth[v] + 1
			}
		}
	}
	vertices := g.Flow.Vertices()
	all := make(map[graph.Vertex]struct{}, len(vertices))
	for _, v := range vertices {
		all[v] = struct{}{}
	}
	for _, v := range vertices {
		if v == g.Start {
			a.Dominators[v] = map[graph.Vertex]struct{}{v: {}}
		} else {
			a.Dominators[v] = cloneSet(all)
		}
	}
	changed := true
	for changed {
		changed = false
		for _, v := range vertices {
			if v == g.Start {
				continue
			}
			preds := g.Flow.Predecessors(v)
			if len(preds) == 0 {
				continue
			}
			next := cloneSet(all)
			for _, p := range preds {
				next = intersect(next, a.Dominators[p])
			}
			next[v] = struct{}{}
			if !equalSet(next, a.Dominators[v]) {
				a.Dominators[v] = next
				changed = true
			}
		}
	}
	// 以接受节点为终点计算后支配关系。无法到达接受节点的节点保持空集合。
	for _, v := range vertices {
		a.PostDominators[v] = cloneSet(all)
	}
	for _, acc := range g.Accepts() {
		a.PostDominators[acc] = map[graph.Vertex]struct{}{acc: {}}
	}
	changed = true
	for changed {
		changed = false
		for _, v := range vertices {
			if g.IsAccept(v) {
				continue
			}
			succs := g.Flow.Successors(v)
			if len(succs) == 0 {
				a.PostDominators[v] = map[graph.Vertex]struct{}{}
				continue
			}
			next := cloneSet(all)
			for _, s := range succs {
				next = intersect(next, a.PostDominators[s])
			}
			if len(next) > 0 {
				next[v] = struct{}{}
			}
			if !equalSet(next, a.PostDominators[v]) {
				a.PostDominators[v] = next
				changed = true
			}
		}
	}
	a.SCC = g.Flow.SCC()
	for _, component := range a.SCC {
		cyclic := len(component) > 1
		if !cyclic && len(component) == 1 {
			v := component[0]
			cyclic = g.Flow.HasEdge(v, v)
		}
		if cyclic {
			for _, v := range component {
				a.LoopNodes[v] = true
				a.LoopWidth[v] = len(component)
			}
		}
	}
	for _, v := range g.Flow.Vertices() {
		a.ReverseDepth[v] = -1
	}
	for _, acc := range g.Accepts() {
		a.ReverseDepth[acc] = 0
	}
	queue := append([]graph.Vertex(nil), g.Accepts()...)
	for len(queue) > 0 {
		v := queue[0]
		queue = queue[1:]
		for _, w := range g.Flow.Predecessors(v) {
			candidate := a.ReverseDepth[v] + 1
			if a.ReverseDepth[w] < 0 || candidate < a.ReverseDepth[w] {
				a.ReverseDepth[w] = candidate
				queue = append(queue, w)
			}
		}
	}
	for _, c := range a.SCC {
		if len(c) > 1 || len(c) == 1 && g.Flow.HasEdge(c[0], c[0]) {
			a.Regions = append(a.Regions, c)
		}
	}
	return a
}

// PruneUnreachable 删除从起点不可达的节点并重建边。
func PruneUnreachable(g *Graph) int {
	if g == nil || g.Flow == nil {
		return 0
	}
	reachable := make(map[graph.Vertex]struct{})
	for _, v := range g.Flow.BFS(g.Start) {
		reachable[v] = struct{}{}
	}
	removed := 0
	for id := range g.Nodes {
		if _, ok := reachable[id]; !ok {
			delete(g.Nodes, id)
			removed++
		}
	}
	if removed == 0 {
		return 0
	}
	flow := graph.NewDirected()
	for id := range g.Nodes {
		flow.AddVertex(id)
	}
	for from := range g.Nodes {
		for _, to := range g.Flow.Successors(from) {
			if _, ok := g.Nodes[to]; ok {
				flow.AddEdge(from, to)
			}
		}
	}
	g.Flow = flow
	return removed
}

// RemoveRedundantEdges 删除重复出边并保持原有顺序。
func RemoveRedundantEdges(g *Graph) int {
	if g == nil || g.Flow == nil {
		return 0
	}
	flow := graph.NewDirected()
	removed := 0
	for _, from := range g.Flow.Vertices() {
		flow.AddVertex(from)
		seen := map[graph.Vertex]struct{}{}
		for _, to := range g.Flow.Successors(from) {
			if _, ok := seen[to]; ok {
				removed++
				continue
			}
			seen[to] = struct{}{}
			flow.AddEdge(from, to)
		}
	}
	g.Flow = flow
	return removed
}

func cloneSet(in map[graph.Vertex]struct{}) map[graph.Vertex]struct{} {
	out := make(map[graph.Vertex]struct{}, len(in))
	for k := range in {
		out[k] = struct{}{}
	}
	return out
}
func intersect(a, b map[graph.Vertex]struct{}) map[graph.Vertex]struct{} {
	out := map[graph.Vertex]struct{}{}
	for k := range a {
		if _, ok := b[k]; ok {
			out[k] = struct{}{}
		}
	}
	return out
}
func equalSet(a, b map[graph.Vertex]struct{}) bool {
	if len(a) != len(b) {
		return false
	}
	for k := range a {
		if _, ok := b[k]; !ok {
			return false
		}
	}
	return true
}

// EquivalentNodePairs 返回具有相同可观察节点元数据的节点对。
func EquivalentNodePairs(g *Graph) [][2]graph.Vertex {
	if g == nil {
		return nil
	}
	ids := g.Flow.Vertices()
	out := make([][2]graph.Vertex, 0)
	for i := range ids {
		for j := i + 1; j < len(ids); j++ {
			a, b := g.Nodes[ids[i]], g.Nodes[ids[j]]
			if a == nil || b == nil {
				continue
			}
			if a.Kind == b.Kind && string(a.Literal) == string(b.Literal) && a.Assertion == b.Assertion && a.ReportID == b.ReportID {
				out = append(out, [2]graph.Vertex{a.ID, b.ID})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i][0] != out[j][0] {
			return out[i][0] < out[j][0]
		}
		return out[i][1] < out[j][1]
	})
	return out
}

// Equivalent 判断两个图的节点与边结构是否完全一致。
func Equivalent(a, b *Graph) bool {
	if a == nil || b == nil {
		return a == b
	}
	if a.Start != b.Start || len(a.Nodes) != len(b.Nodes) || a.EdgeCount() != b.EdgeCount() {
		return false
	}
	for id, node := range a.Nodes {
		other, ok := b.Nodes[id]
		if !ok || node == nil || other == nil || !node.Equal(*other) {
			return false
		}
	}
	return reflect.DeepEqual(a.EdgePairs(), b.EdgePairs())
}
