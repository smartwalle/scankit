// Package graph 提供 compiler 使用的有向/无向图基础结构和遍历算法。
package graph

import (
	"slices"
	"sort"
)

// Vertex 是图顶点编号。编号从 0 开始，允许稀疏使用。
type Vertex int

// Edge 描述一条有向边。
type Edge struct {
	From Vertex
	To   Vertex
}

// Directed 是可变有向图，重复边可在规范化阶段清理。
type Directed struct {
	succ map[Vertex][]Vertex
	pred map[Vertex][]Vertex
}

// EdgeCount 返回有向边的数量，nil 图返回 0。
func (g *Directed) EdgeCount() int {
	if g == nil {
		return 0
	}
	n := 0
	for _, v := range g.succ {
		n += len(v)
	}
	return n
}

// HasEdge 判断有向边是否存在。
func (g *Directed) HasEdge(from, to Vertex) bool {
	if g == nil {
		return false
	}
	return slices.Contains(g.succ[from], to)
}

// OutDegree 返回顶点出度。
func (g *Directed) OutDegree(v Vertex) int {
	if g == nil {
		return 0
	}
	return len(g.succ[v])
}

// InDegree 返回顶点入度。
func (g *Directed) InDegree(v Vertex) int {
	if g == nil {
		return 0
	}
	return len(g.pred[v])
}

// VertexCount 返回图中的顶点数量。
func (g *Directed) VertexCount() int {
	if g == nil {
		return 0
	}
	return len(g.succ)
}

// Clone 返回结构副本，顶点和边的集合与原图一致。
func (g *Directed) Clone() *Directed {
	if g == nil {
		return nil
	}
	o := NewDirected()
	for _, v := range g.Vertices() {
		o.AddVertex(v)
		for _, w := range g.Successors(v) {
			o.AddEdge(v, w)
		}
	}
	return o
}

// Reverse 返回所有边方向反转后的新图。
func (g *Directed) Reverse() *Directed {
	if g == nil {
		return nil
	}
	o := NewDirected()
	for _, v := range g.Vertices() {
		o.AddVertex(v)
		for _, w := range g.Successors(v) {
			o.AddEdge(w, v)
		}
	}
	return o
}

// Subgraph 返回由给定顶点及其之间的边构成的子图。
func (g *Directed) Subgraph(vertices []Vertex) *Directed {
	if g == nil {
		return nil
	}
	keep := map[Vertex]bool{}
	for _, v := range vertices {
		keep[v] = true
	}
	o := NewDirected()
	for _, v := range vertices {
		if keep[v] && g.HasVertex(v) {
			o.AddVertex(v)
			for _, w := range g.succ[v] {
				if keep[w] {
					o.AddEdge(v, w)
				}
			}
		}
	}
	return o
}

// Compress 将顶点按编号压缩为连续编号，并返回旧编号到新编号的映射。
func (g *Directed) Compress() (*Directed, map[Vertex]Vertex) {
	if g == nil {
		return nil, nil
	}
	vertices := g.Vertices()
	mapping := make(map[Vertex]Vertex, len(vertices))
	for index, vertex := range vertices {
		mapping[vertex] = Vertex(index)
	}
	out := NewDirected()
	for _, from := range vertices {
		out.AddVertex(mapping[from])
		for _, to := range g.Successors(from) {
			if mapped, ok := mapping[to]; ok {
				out.AddEdge(mapping[from], mapped)
			}
		}
	}
	out.Normalize()
	return out, mapping
}

// RemoveEdge 删除一条边；返回是否确实删除。
func (g *Directed) RemoveEdge(from, to Vertex) bool {
	if g == nil {
		return false
	}
	removed := false
	vs := g.succ[from]
	out := vs[:0]
	for _, v := range vs {
		if v == to {
			removed = true
			continue
		}
		out = append(out, v)
	}
	if !removed {
		return false
	}
	g.succ[from] = out
	ps := g.pred[to]
	pout := ps[:0]
	for _, v := range ps {
		if v != from {
			pout = append(pout, v)
		}
	}
	g.pred[to] = pout
	return true
}

// RemoveVertex 删除顶点以及所有与之相连的边。
func (g *Directed) RemoveVertex(v Vertex) {
	if g == nil {
		return
	}
	delete(g.succ, v)
	delete(g.pred, v)
	for x, vs := range g.succ {
		out := vs[:0]
		for _, y := range vs {
			if y != v {
				out = append(out, y)
			}
		}
		g.succ[x] = out
	}
	for x, vs := range g.pred {
		out := vs[:0]
		for _, y := range vs {
			if y != v {
				out = append(out, y)
			}
		}
		g.pred[x] = out
	}
}

// NewDirected 创建有向图。
func NewDirected() *Directed {
	return &Directed{succ: make(map[Vertex][]Vertex), pred: make(map[Vertex][]Vertex)}
}

// AddVertex 确保顶点存在。
func (g *Directed) AddVertex(v Vertex) {
	if g == nil {
		return
	}
	if _, ok := g.succ[v]; !ok {
		g.succ[v] = nil
	}
	if _, ok := g.pred[v]; !ok {
		g.pred[v] = nil
	}
}

// AddEdge 添加有向边，并自动创建端点。
func (g *Directed) AddEdge(from, to Vertex) {
	if g == nil {
		return
	}
	g.AddVertex(from)
	g.AddVertex(to)
	g.succ[from] = append(g.succ[from], to)
	g.pred[to] = append(g.pred[to], from)
}

// Normalize 删除重复边并按顶点编号稳定排序。
func (g *Directed) Normalize() {
	if g == nil {
		return
	}
	for from, list := range g.succ {
		slices.Sort(list)
		out := list[:0]
		for _, to := range list {
			if len(out) == 0 || out[len(out)-1] != to {
				out = append(out, to)
			}
		}
		g.succ[from] = out
	}
	for to := range g.pred {
		g.pred[to] = nil
	}
	for from, list := range g.succ {
		for _, to := range list {
			g.pred[to] = append(g.pred[to], from)
		}
	}
	for to, list := range g.pred {
		slices.Sort(list)
		g.pred[to] = list
	}
}

// Vertices 返回按编号升序排列的顶点快照。
func (g *Directed) Vertices() []Vertex {
	if g == nil {
		return nil
	}
	vertices := make([]Vertex, 0, len(g.succ))
	for v := range g.succ {
		vertices = append(vertices, v)
	}
	for i := 1; i < len(vertices); i++ {
		for j := i; j > 0 && vertices[j] < vertices[j-1]; j-- {
			vertices[j], vertices[j-1] = vertices[j-1], vertices[j]
		}
	}
	return vertices
}

// Successors 返回 v 的后继顶点副本。
func (g *Directed) Successors(v Vertex) []Vertex {
	if g == nil {
		return nil
	}
	return append([]Vertex(nil), g.succ[v]...)
}

// SuccessorView 返回 v 后继顶点的内部切片，只读遍历场景使用，
// 避免热路径为每个状态复制后继列表。调用方不得修改返回值。
func (g *Directed) SuccessorView(v Vertex) []Vertex {
	if g == nil {
		return nil
	}
	return g.succ[v]
}

// Predecessors 返回 v 的前驱顶点副本。
func (g *Directed) Predecessors(v Vertex) []Vertex {
	if g == nil {
		return nil
	}
	return append([]Vertex(nil), g.pred[v]...)
}

// DFS 按邻接顺序深度优先遍历，从 start 开始返回访问顺序。
func (g *Directed) DFS(start Vertex) []Vertex {
	if g == nil {
		return nil
	}
	if _, ok := g.succ[start]; !ok {
		return nil
	}
	seen := map[Vertex]bool{start: true}
	order := make([]Vertex, 0)
	stack := []Vertex{start}
	for len(stack) > 0 {
		last := len(stack) - 1
		v := stack[last]
		stack = stack[:last]
		order = append(order, v)
		successors := g.succ[v]
		for _, next := range slices.Backward(successors) {

			if !seen[next] {
				seen[next] = true
				stack = append(stack, next)
			}
		}
	}
	return order
}

// BFS 按邻接顺序广度优先遍历，从 start 开始返回访问顺序。
func (g *Directed) BFS(start Vertex) []Vertex {
	if g == nil {
		return nil
	}
	if _, ok := g.succ[start]; !ok {
		return nil
	}
	seen := map[Vertex]bool{start: true}
	queue := []Vertex{start}
	order := make([]Vertex, 0)
	for len(queue) > 0 {
		v := queue[0]
		queue = queue[1:]
		order = append(order, v)
		for _, next := range g.succ[v] {
			if !seen[next] {
				seen[next] = true
				queue = append(queue, next)
			}
		}
	}
	return order
}

// Reachable 判断从 from 是否可到达 to。
func (g *Directed) Reachable(from, to Vertex) bool {
	if g == nil {
		return false
	}
	return slices.Contains(g.BFS(from), to)
}

// ShortestPath 返回无权图中的最短路径。
func (g *Directed) ShortestPath(from, to Vertex) []Vertex {
	if g == nil || !g.HasVertex(from) || !g.HasVertex(to) {
		return nil
	}
	previous := map[Vertex]Vertex{}
	seen := map[Vertex]bool{from: true}
	queue := []Vertex{from}
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		if current == to {
			break
		}
		for _, next := range g.Successors(current) {
			if seen[next] {
				continue
			}
			seen[next] = true
			previous[next] = current
			queue = append(queue, next)
		}
	}
	if !seen[to] {
		return nil
	}
	path := []Vertex{to}
	for current := to; current != from; {
		current = previous[current]
		path = append(path, current)
	}
	for i := 0; i < len(path)/2; i++ {
		path[i], path[len(path)-1-i] = path[len(path)-1-i], path[i]
	}
	return path
}

// PathWithin 返回长度不超过 maxEdges 的一条路径。
func (g *Directed) PathWithin(from, to Vertex, maxEdges int) []Vertex {
	if maxEdges < 0 || g == nil || !g.HasVertex(from) || !g.HasVertex(to) {
		return nil
	}
	path := g.ShortestPath(from, to)
	if len(path) == 0 || len(path)-1 > maxEdges {
		return nil
	}
	return path
}

// PostOrder 返回从起点可达顶点的后序遍历。
func (g *Directed) PostOrder(start Vertex) []Vertex {
	if g == nil || !g.HasVertex(start) {
		return nil
	}
	type frame struct {
		vertex Vertex
		next   int
	}
	seen := map[Vertex]bool{start: true}
	stack := []frame{{vertex: start}}
	out := make([]Vertex, 0)
	for len(stack) > 0 {
		top := &stack[len(stack)-1]
		successors := g.Successors(top.vertex)
		if top.next < len(successors) {
			next := successors[top.next]
			top.next++
			if seen[next] {
				continue
			}
			seen[next] = true
			stack = append(stack, frame{vertex: next})
			continue
		}
		out = append(out, top.vertex)
		stack = stack[:len(stack)-1]
	}
	return out
}

// Sources 返回入度为零的顶点。
func (g *Directed) Sources() []Vertex {
	if g == nil {
		return nil
	}
	out := make([]Vertex, 0)
	for _, id := range g.Vertices() {
		if g.InDegree(id) == 0 {
			out = append(out, id)
		}
	}
	return out
}

// Sinks 返回出度为零的顶点。
func (g *Directed) Sinks() []Vertex {
	if g == nil {
		return nil
	}
	out := make([]Vertex, 0)
	for _, id := range g.Vertices() {
		if g.OutDegree(id) == 0 {
			out = append(out, id)
		}
	}
	return out
}

// Equal 判断两张有向图的顶点和边是否一致。
func (g *Directed) Equal(other *Directed) bool {
	if g == nil || other == nil {
		return g == other
	}
	if g.VertexCount() != other.VertexCount() || g.EdgeCount() != other.EdgeCount() {
		return false
	}
	for _, id := range g.Vertices() {
		if !other.HasVertex(id) || !sameVertices(g.Successors(id), other.Successors(id)) {
			return false
		}
	}
	return true
}

func sameVertices(a, b []Vertex) bool {
	if len(a) != len(b) {
		return false
	}
	left := append([]Vertex(nil), a...)
	right := append([]Vertex(nil), b...)
	slices.Sort(left)
	slices.Sort(right)
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

// Topological 按顶点编号稳定输出拓扑序；存在环时 ok 为 false。
func (g *Directed) Topological() ([]Vertex, bool) {
	if g == nil {
		return nil, true
	}
	ind := map[Vertex]int{}
	for _, v := range g.Vertices() {
		ind[v] = len(g.Predecessors(v))
	}
	var q []Vertex
	for v, n := range ind {
		if n == 0 {
			q = append(q, v)
		}
	}
	slices.Sort(q)
	var out []Vertex
	for len(q) > 0 {
		v := q[0]
		q = q[1:]
		out = append(out, v)
		for _, w := range g.Successors(v) {
			ind[w]--
			if ind[w] == 0 {
				q = append(q, w)
				slices.Sort(q)
			}
		}
	}
	return out, len(out) == len(ind)
}

// IsAcyclic 判断图是否存在有向环。
func (g *Directed) IsAcyclic() bool { _, ok := g.Topological(); return ok }

// CyclePath 返回一条有向环路径；无环时返回空切片。
func (g *Directed) CyclePath() []Vertex {
	if g == nil {
		return nil
	}
	state := make(map[Vertex]uint8)
	stack := make([]Vertex, 0)
	var visit func(Vertex) []Vertex
	visit = func(v Vertex) []Vertex {
		state[v] = 1
		stack = append(stack, v)
		for _, next := range g.succ[v] {
			if state[next] == 0 {
				if cycle := visit(next); len(cycle) > 0 {
					return cycle
				}
			}
			if state[next] == 1 {
				for i, value := range stack {
					if value == next {
						cycle := append([]Vertex(nil), stack[i:]...)
						return append(cycle, next)
					}
				}
			}
		}
		stack = stack[:len(stack)-1]
		state[v] = 2
		return nil
	}
	for _, v := range g.Vertices() {
		if state[v] == 0 {
			if cycle := visit(v); len(cycle) > 0 {
				return cycle
			}
		}
	}
	return nil
}

// HasVertex 判断顶点是否存在于图中。
func (g *Directed) HasVertex(v Vertex) bool {
	if g == nil {
		return false
	}
	_, ok := g.succ[v]
	return ok
}

// Edges 返回按起点和终点排序的边快照。
func (g *Directed) Edges() []Edge {
	if g == nil {
		return nil
	}
	var out []Edge
	for _, from := range g.Vertices() {
		for _, to := range g.Successors(from) {
			out = append(out, Edge{From: from, To: to})
		}
	}
	return out
}

// SCC 返回强连通分量，分量内部和分量列表均按顶点编号稳定排序。
func (g *Directed) SCC() [][]Vertex {
	if g == nil {
		return nil
	}
	index, low := make(map[Vertex]int), make(map[Vertex]int)
	onStack := make(map[Vertex]bool)
	stack := make([]Vertex, 0)
	nextIndex := 0
	components := make([][]Vertex, 0)
	var visit func(Vertex)
	visit = func(v Vertex) {
		index[v], low[v] = nextIndex, nextIndex
		nextIndex++
		stack = append(stack, v)
		onStack[v] = true
		for _, w := range g.succ[v] {
			if _, ok := index[w]; !ok {
				visit(w)
				if low[w] < low[v] {
					low[v] = low[w]
				}
			} else if onStack[w] && index[w] < low[v] {
				low[v] = index[w]
			}
		}
		if low[v] != index[v] {
			return
		}
		component := make([]Vertex, 0)
		for {
			last := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			onStack[last] = false
			component = append(component, last)
			if last == v {
				break
			}
		}
		for i := 1; i < len(component); i++ {
			for j := i; j > 0 && component[j] < component[j-1]; j-- {
				component[j], component[j-1] = component[j-1], component[j]
			}
		}
		components = append(components, component)
	}
	for _, v := range g.Vertices() {
		if _, ok := index[v]; !ok {
			visit(v)
		}
	}
	sort.Slice(components, func(i, j int) bool { return components[i][0] < components[j][0] })
	return components
}

// Undirected 是无向图，边以双向邻接形式保存。
type Undirected struct{ adj map[Vertex][]Vertex }

// EdgeCount 返回无向边的数量，nil 图返回 0。
func (g *Undirected) EdgeCount() int {
	if g == nil {
		return 0
	}
	n := 0
	for _, neighbors := range g.adj {
		n += len(neighbors)
	}
	return n / 2
}

// NewUndirected 创建无向图。
func NewUndirected() *Undirected { return &Undirected{adj: make(map[Vertex][]Vertex)} }

// AddVertex 确保顶点存在。
func (g *Undirected) AddVertex(v Vertex) {
	if g == nil {
		return
	}
	if _, ok := g.adj[v]; !ok {
		g.adj[v] = nil
	}
}

// AddEdge 添加无向边。
func (g *Undirected) AddEdge(a, b Vertex) {
	if g == nil {
		return
	}
	g.AddVertex(a)
	g.AddVertex(b)
	g.adj[a] = append(g.adj[a], b)
	g.adj[b] = append(g.adj[b], a)
}

// Neighbors 返回 v 的邻接顶点副本。
func (g *Undirected) Neighbors(v Vertex) []Vertex {
	if g == nil {
		return nil
	}
	return append([]Vertex(nil), g.adj[v]...)
}

// Vertices 返回按编号升序排列的顶点。
func (g *Undirected) Vertices() []Vertex {
	if g == nil {
		return nil
	}
	vertices := make([]Vertex, 0, len(g.adj))
	for v := range g.adj {
		vertices = append(vertices, v)
	}
	for i := 1; i < len(vertices); i++ {
		for j := i; j > 0 && vertices[j] < vertices[j-1]; j-- {
			vertices[j], vertices[j-1] = vertices[j-1], vertices[j]
		}
	}
	return vertices
}

// HasEdge 判断两个顶点之间是否存在无向边。
func (g *Undirected) HasEdge(a, b Vertex) bool {
	if g == nil {
		return false
	}
	return slices.Contains(g.adj[a], b)
}

// HasVertex 判断顶点是否存在。
func (g *Undirected) HasVertex(v Vertex) bool {
	if g == nil {
		return false
	}
	_, ok := g.adj[v]
	return ok
}

// Degree 返回顶点的度数，nil 图返回 0。
func (g *Undirected) Degree(v Vertex) int {
	if g == nil {
		return 0
	}
	return len(g.adj[v])
}

// Connected 判断两个顶点是否位于同一连通分量。
func (g *Undirected) Connected(a, b Vertex) bool {
	if g == nil || !g.HasVertex(a) || !g.HasVertex(b) {
		return false
	}
	seen := map[Vertex]bool{a: true}
	queue := []Vertex{a}
	for len(queue) > 0 {
		v := queue[0]
		queue = queue[1:]
		if v == b {
			return true
		}
		for _, next := range g.Neighbors(v) {
			if !seen[next] {
				seen[next] = true
				queue = append(queue, next)
			}
		}
	}
	return false
}

// Components 返回无向图的连通分量。
func (g *Undirected) Components() [][]Vertex {
	if g == nil {
		return nil
	}
	seen := map[Vertex]bool{}
	components := make([][]Vertex, 0)
	for _, root := range g.Vertices() {
		if seen[root] {
			continue
		}
		component := []Vertex{root}
		seen[root] = true
		for i := 0; i < len(component); i++ {
			for _, next := range g.Neighbors(component[i]) {
				if !seen[next] {
					seen[next] = true
					component = append(component, next)
				}
			}
		}
		slices.Sort(component)
		components = append(components, component)
	}
	return components
}

// Clone 返回结构副本，顶点和边的集合与原图一致。
func (g *Undirected) Clone() *Undirected {
	if g == nil {
		return nil
	}
	o := NewUndirected()
	for _, v := range g.Vertices() {
		o.AddVertex(v)
		for _, n := range g.adj[v] {
			if v <= n {
				o.AddEdge(v, n)
			}
		}
	}
	return o
}
