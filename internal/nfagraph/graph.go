// Package nfagraph 定义从 Parser AST 降低后的 NFA 图中间表示。
package nfagraph

import (
	"fmt"
	"reflect"
	"slices"
	"sort"

	"github.com/smartwalle/scankit/internal/graph"
	"github.com/smartwalle/scankit/internal/parser"
)

// NodeKind 标识 NFA 图节点的语义类别。
type NodeKind uint8

func (k NodeKind) String() string {
	names := []string{"", "start", "accept", "literal", "class", "assertion", "repeat", "report", "split", "join"}
	if int(k) < len(names) {
		return names[k]
	}
	return "unknown"
}

// KindStart 表示图起点，其余常量按语义依次对应接受、文字、字符类等节点。
const (
	KindStart NodeKind = iota + 1
	KindAccept
	KindLiteral
	KindClass
	KindAssertion
	KindRepeat
	KindReport
	KindSplit
	KindJoin
)

// Node 是 NFA 图的节点，负载字段按 Kind 生效。
type Node struct {
	ID        graph.Vertex
	Kind      NodeKind
	Literal   []byte
	Class     parser.Class
	Unicode   *parser.UnicodeClass
	Assertion parser.AssertionKind
	ReportID  uint32
	RepeatMin int
	RepeatMax int
	Greedy    bool
}

// String 返回节点的简洁诊断表示。
func (n Node) String() string { return fmt.Sprintf("%d:%s", n.ID, n.Kind.String()) }

// Equal 判断两个节点的结构和负载是否一致。
func (n Node) Equal(other Node) bool {
	if n.ID != other.ID || n.Kind != other.Kind || string(n.Literal) != string(other.Literal) || n.Assertion != other.Assertion || n.ReportID != other.ReportID || n.RepeatMin != other.RepeatMin || n.RepeatMax != other.RepeatMax || n.Greedy != other.Greedy || !reflect.DeepEqual(n.Class, other.Class) {
		return false
	}
	if n.Unicode == nil || other.Unicode == nil {
		return n.Unicode == nil && other.Unicode == nil
	}
	return *n.Unicode == *other.Unicode
}

// Clone 返回节点副本，并复制负载中的可变切片。
func (n Node) Clone() Node {
	n.Literal = append([]byte(nil), n.Literal...)
	n.Class.Ranges = append([]parser.Range(nil), n.Class.Ranges...)
	if n.Unicode != nil {
		u := *n.Unicode
		n.Unicode = &u
	}
	return n
}

// Graph 是 NFA 中间表示：Flow 保存拓扑，Nodes 保存节点负载。
type Graph struct {
	Flow  *graph.Directed
	Nodes map[graph.Vertex]*Node
	Start graph.Vertex
}

// New 创建只包含起点节点的空图。
func New() *Graph {
	g := &Graph{Flow: graph.NewDirected(), Nodes: make(map[graph.Vertex]*Node), Start: 0}
	g.AddNode(Node{ID: 0, Kind: KindStart})
	return g
}

// AddNode 写入节点负载，已存在的同 ID 节点会被覆盖。
func (g *Graph) AddNode(node Node) {
	if g == nil {
		return
	}
	if g.Flow == nil {
		g.Flow = graph.NewDirected()
	}
	if g.Nodes == nil {
		g.Nodes = make(map[graph.Vertex]*Node)
	}
	g.Flow.AddVertex(node.ID)
	copyNode := node.Clone()
	g.Nodes[node.ID] = &copyNode
}

// AddEdge 添加一条有向边，重复边会被忽略。
func (g *Graph) AddEdge(from, to graph.Vertex) {
	if g != nil {
		if g.Flow == nil {
			g.Flow = graph.NewDirected()
		}
		g.Flow.AddEdge(from, to)
	}
}

// Node 返回节点负载，节点不存在时 ok 为 false。
func (g *Graph) Node(id graph.Vertex) (Node, bool) {
	if g == nil {
		return Node{}, false
	}
	n, ok := g.Nodes[id]
	if !ok {
		return Node{}, false
	}
	return *n, true
}

// Accepts 返回全部接受顶点的升序快照。
func (g *Graph) Accepts() []graph.Vertex {
	if g == nil {
		return nil
	}
	var out []graph.Vertex
	for id, n := range g.Nodes {
		if n != nil && n.Kind == KindAccept {
			out = append(out, id)
		}
	}
	slices.Sort(out)
	return out
}

// HasAccept 判断图中是否存在接受顶点。
func (g *Graph) HasAccept() bool { return len(g.Accepts()) > 0 }

// AcceptCount 返回接受顶点的数量。
func (g *Graph) AcceptCount() int { return len(g.Accepts()) }

// HasUnicode 判断图中是否存在 Unicode 字符类节点。
func (g *Graph) HasUnicode() bool {
	if g == nil {
		return false
	}
	for _, n := range g.Nodes {
		if n != nil && n.Unicode != nil {
			return true
		}
	}
	return false
}

// ByteOnly 判断图是否可完全按字节字符语义执行。
func (g *Graph) ByteOnly() bool { return g != nil && !g.HasUnicode() }

// StartNode 返回起点节点的负载，起点缺失时 ok 为 false。
func (g *Graph) StartNode() (Node, bool) {
	if g == nil {
		return Node{}, false
	}
	return g.Node(g.Start)
}

// NodesCopy 按顶点编号升序返回节点负载的副本。
func (g *Graph) NodesCopy() []Node {
	if g == nil {
		return nil
	}
	ids := g.Flow.Vertices()
	out := make([]Node, 0, len(ids))
	for _, id := range ids {
		if n, ok := g.Node(id); ok {
			out = append(out, n)
		}
	}
	return out
}

// EdgePairs 按起点、终点升序返回全部边的端点对。
func (g *Graph) EdgePairs() [][2]graph.Vertex {
	if g == nil || g.Flow == nil {
		return nil
	}
	var out [][2]graph.Vertex
	for _, from := range g.Flow.Vertices() {
		for _, to := range g.Flow.Successors(from) {
			out = append(out, [2]graph.Vertex{from, to})
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

// NodeIDs 返回顶点编号的升序快照。
func (g *Graph) NodeIDs() []graph.Vertex {
	if g == nil || g.Flow == nil {
		return nil
	}
	return g.Flow.Vertices()
}

// Reachable 判断顶点能否从起点到达。
func (g *Graph) Reachable(id graph.Vertex) bool {
	if g == nil || g.Flow == nil || !g.HasNode(id) {
		return false
	}
	return slices.Contains(g.Flow.BFS(g.Start), id)
}

// EdgeCount 返回有向边数量，空图返回 0。
func (g *Graph) EdgeCount() int {
	if g == nil || g.Flow == nil {
		return 0
	}
	return g.Flow.EdgeCount()
}

// NodeCount 返回节点数量。
func (g *Graph) NodeCount() int {
	if g == nil {
		return 0
	}
	return len(g.Nodes)
}

// IsReachable 判断节点是否可从图起点到达。
func (g *Graph) IsReachable(id graph.Vertex) bool { return g.Reachable(id) }

// IsAccept 判断节点是否为接受节点。
func (g *Graph) IsAccept(id graph.Vertex) bool {
	if g == nil || g.Nodes[id] == nil {
		return false
	}
	return g.Nodes[id].Kind == KindAccept
}

// Successors 返回节点后继快照。
func (g *Graph) Successors(id graph.Vertex) []graph.Vertex {
	if g == nil || g.Flow == nil {
		return nil
	}
	return g.Flow.Successors(id)
}

// Predecessors 返回节点前驱快照。
func (g *Graph) Predecessors(id graph.Vertex) []graph.Vertex {
	if g == nil || g.Flow == nil {
		return nil
	}
	return g.Flow.Predecessors(id)
}

// HasNode 判断顶点是否已登记节点负载。
func (g *Graph) HasNode(id graph.Vertex) bool {
	if g == nil {
		return false
	}
	_, ok := g.Nodes[id]
	return ok
}

// Clone 深度拷贝图，节点负载中的切片同样被复制。
func (g *Graph) Clone() *Graph {
	if g == nil {
		return nil
	}
	out := New()
	out.Start = g.Start
	out.Nodes = map[graph.Vertex]*Node{}
	out.Flow = graph.NewDirected()
	for _, n := range g.Nodes {
		if n == nil {
			// 克隆损坏图时保留可校验的空槽位，避免直接解引用
			// 导致 panic；调用方随后可通过 Validate 得到明确错误。
			continue
		}
		out.AddNode(*n)
	}
	if g.Flow == nil {
		return out
	}
	for _, from := range g.Flow.Vertices() {
		for _, to := range g.Flow.Successors(from) {
			out.AddEdge(from, to)
		}
	}
	return out
}

// Equal 判断两个降级图的节点、边和起点是否一致。
func (g *Graph) Equal(other *Graph) bool {
	if g == nil || other == nil {
		return g == other
	}
	if g.Start != other.Start || len(g.Nodes) != len(other.Nodes) || !reflect.DeepEqual(g.EdgePairs(), other.EdgePairs()) {
		return false
	}
	for id, node := range g.Nodes {
		otherNode, ok := other.Nodes[id]
		if !ok || node == nil || otherNode == nil || !node.Equal(*otherNode) {
			return false
		}
	}
	return true
}

// Validate 检查降级图的结构不变量。
func (g *Graph) Validate() error {
	if g == nil || g.Flow == nil {
		return fmt.Errorf("nil graph")
	}
	if _, ok := g.Nodes[g.Start]; !ok {
		return fmt.Errorf("start node %d is missing", g.Start)
	}
	if g.Nodes[g.Start] == nil || g.Nodes[g.Start].Kind != KindStart {
		return fmt.Errorf("start node %d has invalid kind", g.Start)
	}
	vertices := g.Flow.Vertices()
	vertexSet := make(map[graph.Vertex]struct{}, len(vertices))
	for _, id := range vertices {
		vertexSet[id] = struct{}{}
	}
	if len(vertexSet) != len(g.Nodes) {
		return fmt.Errorf("graph vertex/node count mismatch")
	}
	for id := range g.Nodes {
		if _, ok := vertexSet[id]; !ok {
			return fmt.Errorf("node %d is missing from flow", id)
		}
	}
	for _, id := range vertices {
		node, ok := g.Nodes[id]
		if !ok || node == nil {
			return fmt.Errorf("vertex %d has no node", id)
		}
		if node.Kind < KindStart || node.Kind > KindJoin {
			return fmt.Errorf("vertex %d has invalid node kind", id)
		}
		seen := make(map[graph.Vertex]struct{})
		for _, to := range g.Flow.Successors(id) {
			if _, duplicate := seen[to]; duplicate {
				return fmt.Errorf("duplicate edge %d -> %d", id, to)
			}
			seen[to] = struct{}{}
			if _, ok := g.Nodes[to]; !ok {
				return fmt.Errorf("edge %d -> %d references missing node", id, to)
			}
			preds := g.Flow.Predecessors(to)
			found := false
			for _, pred := range preds {
				if pred == id {
					if found {
						return fmt.Errorf("duplicate predecessor %d for %d", id, to)
					}
					found = true
				}
			}
			if !found {
				return fmt.Errorf("edge %d -> %d missing predecessor entry", id, to)
			}
		}
	}
	for _, id := range vertices {
		seen := make(map[graph.Vertex]struct{})
		for _, pred := range g.Flow.Predecessors(id) {
			if _, duplicate := seen[pred]; duplicate {
				return fmt.Errorf("duplicate predecessor %d for %d", pred, id)
			}
			seen[pred] = struct{}{}
			if !g.Flow.HasEdge(pred, id) {
				return fmt.Errorf("predecessor entry %d -> %d missing successor entry", pred, id)
			}
		}
	}
	if len(g.Flow.BFS(g.Start)) != len(g.Nodes) {
		return fmt.Errorf("graph contains unreachable nodes")
	}
	accepts := 0
	for id, n := range g.Nodes {
		if n == nil {
			return fmt.Errorf("node %d is nil", id)
		}
		if n.ID != id {
			return fmt.Errorf("node key %d does not match embedded id %d", id, n.ID)
		}
		if id < 0 {
			return fmt.Errorf("node %d has negative id", id)
		}
		if n.Kind == KindAccept {
			accepts++
			if len(g.Flow.Successors(id)) != 0 {
				return fmt.Errorf("accept node %d has outgoing edges", id)
			}
		}
		switch n.Kind {
		case KindLiteral:
			if len(n.Literal) == 0 {
				return fmt.Errorf("literal node %d has empty payload", id)
			}
			if n.Unicode != nil || len(n.Class.Ranges) != 0 {
				return fmt.Errorf("literal node %d has class payload", id)
			}
		case KindClass:
			if n.Unicode != nil && len(n.Class.Ranges) != 0 {
				return fmt.Errorf("class node %d mixes byte and unicode payload", id)
			}
		case KindRepeat:
			if n.RepeatMin < 0 || (n.RepeatMax >= 0 && n.RepeatMax < n.RepeatMin) {
				return fmt.Errorf("repeat node %d has invalid bounds", id)
			}
			if n.Unicode != nil || len(n.Class.Ranges) != 0 || len(n.Literal) != 0 {
				return fmt.Errorf("repeat node %d has character payload", id)
			}
		default:
		}
	}
	if accepts == 0 {
		return fmt.Errorf("graph has no accept node")
	}
	return nil
}

// Builder 负责把 Parser AST 逐步降低为 NFA 图，并分配顶点编号。
type Builder struct {
	graph *Graph
	next  graph.Vertex
}

type fragment struct{ entry, exit graph.Vertex }

// NewBuilder 创建带空图的构建器，顶点编号从 1 开始分配。
func NewBuilder() *Builder { return &Builder{graph: New(), next: 1} }

// Reset 丢弃当前图并重置顶点编号分配器。
func (b *Builder) Reset() {
	if b != nil {
		b.graph = New()
		b.next = 1
	}
}

// Graph 返回构建中的图，空构建器返回 nil。
func (b *Builder) Graph() *Graph {
	if b == nil {
		return nil
	}
	return b.graph
}
func (b *Builder) newNode(kind NodeKind) graph.Vertex {
	id := b.next
	b.next++
	b.graph.AddNode(Node{ID: id, Kind: kind})
	return id
}

// Build 将 AST 降低为 NFA 图，root 为 nil 时返回错误。
func (b *Builder) Build(root parser.Node) (*Graph, error) {
	if root == nil {
		return nil, fmt.Errorf("nil AST")
	}
	if err := parser.Validate(root); err != nil {
		return nil, err
	}
	b.Reset()
	frag, err := b.build(root)
	if err != nil {
		return nil, err
	}
	accept := b.newNode(KindAccept)
	b.graph.AddEdge(b.graph.Start, frag.entry)
	b.graph.AddEdge(frag.exit, accept)
	if err := b.graph.Validate(); err != nil {
		return nil, err
	}
	return b.graph, nil
}
func (b *Builder) build(n parser.Node) (fragment, error) {
	switch v := n.(type) {
	case parser.Literal:
		id := b.newNode(KindLiteral)
		b.graph.Nodes[id].Literal = append([]byte(nil), v.Value...)
		return fragment{id, id}, nil
	case parser.Class:
		id := b.newNode(KindClass)
		// 字节字符类在进入图前统一排序合并；Unicode 类保持独立节点。
		b.graph.Nodes[id].Class = parser.Normalize(v).(parser.Class)
		return fragment{id, id}, nil
	case parser.Any:
		// Any 在降级图中表示为完整字节字符类，运行时仍依据规则应用点号、换行和 UTF-8 策略。
		id := b.newNode(KindClass)
		b.graph.Nodes[id].Class = parser.Class{Ranges: []parser.Range{{Lo: 0, Hi: 0xff}}}
		return fragment{id, id}, nil
	case parser.UnicodeClass:
		id := b.newNode(KindClass)
		u := v
		b.graph.Nodes[id].Unicode = &u
		return fragment{id, id}, nil
	case parser.Assertion:
		id := b.newNode(KindAssertion)
		b.graph.Nodes[id].Assertion = v.Kind
		return fragment{id, id}, nil
	case parser.Group:
		return b.build(v.Child)
	case parser.Lookaround:
		// 查找断言依赖当前位置之外的输入，不能把子表达式伪装成普通消费节点。
		// 以不可下沉节点保留图边界，调用方将回退到完整 AST 确认。
		id := b.newNode(KindAssertion)
		return fragment{id, id}, nil
	case parser.Sequence:
		if len(v.Elements) == 0 {
			id := b.newNode(KindJoin)
			return fragment{id, id}, nil
		}
		var out fragment
		for i, child := range v.Elements {
			part, err := b.build(child)
			if err != nil {
				return fragment{}, err
			}
			if i == 0 {
				out = part
			} else {
				b.graph.AddEdge(out.exit, part.entry)
				out.exit = part.exit
			}
		}
		return out, nil
	case parser.Alternation:
		split := b.newNode(KindSplit)
		join := b.newNode(KindJoin)
		for _, child := range v.Options {
			part, err := b.build(child)
			if err != nil {
				return fragment{}, err
			}
			b.graph.AddEdge(split, part.entry)
			b.graph.AddEdge(part.exit, join)
		}
		return fragment{split, join}, nil
	case parser.Repeat:
		return b.buildRepeat(v)
	case parser.ControlVerb:
		id := b.newNode(KindAssertion)
		return fragment{id, id}, nil
	case parser.Backreference:
		id := b.newNode(KindAssertion)
		return fragment{id, id}, nil
	case parser.Conditional:
		split := b.newNode(KindSplit)
		join := b.newNode(KindJoin)
		for _, child := range []parser.Node{v.Yes, v.No} {
			part, err := b.build(child)
			if err != nil {
				return fragment{}, err
			}
			b.graph.AddEdge(split, part.entry)
			b.graph.AddEdge(part.exit, join)
		}
		return fragment{split, join}, nil
	default:
		return fragment{}, fmt.Errorf("unsupported AST node %T", n)
	}
}

func (b *Builder) buildRepeat(v parser.Repeat) (fragment, error) {
	// 先构造必选副本，再构造可选副本或循环尾部。
	var out fragment
	first := true
	appendPart := func(part fragment) {
		if first {
			out, first = part, false
		} else {
			b.graph.AddEdge(out.exit, part.entry)
			out.exit = part.exit
		}
	}
	for i := 0; i < v.Min; i++ {
		part, err := b.build(v.Child)
		if err != nil {
			return fragment{}, err
		}
		appendPart(part)
	}
	if v.Max < 0 {
		split := b.newNode(KindRepeat)
		b.graph.Nodes[split].RepeatMin, b.graph.Nodes[split].RepeatMax, b.graph.Nodes[split].Greedy = v.Min, v.Max, v.Greedy
		if first {
			out = fragment{split, split}
			first = false
		} else {
			b.graph.AddEdge(out.exit, split)
			out.exit = split
		}
		join := b.newNode(KindJoin)
		part, err := b.build(v.Child)
		if err != nil {
			return fragment{}, err
		}
		if v.Greedy {
			b.graph.AddEdge(split, part.entry)
			b.graph.AddEdge(split, join)
		} else {
			b.graph.AddEdge(split, join)
			b.graph.AddEdge(split, part.entry)
		}
		b.graph.AddEdge(part.exit, split)
		out.exit = join
		return out, nil
	}
	for i := v.Min; i < v.Max; i++ {
		split := b.newNode(KindRepeat)
		b.graph.Nodes[split].RepeatMin, b.graph.Nodes[split].RepeatMax, b.graph.Nodes[split].Greedy = v.Min, v.Max, v.Greedy
		if first {
			out = fragment{split, split}
			first = false
		} else {
			b.graph.AddEdge(out.exit, split)
			out.exit = split
		}
		join := b.newNode(KindJoin)
		part, err := b.build(v.Child)
		if err != nil {
			return fragment{}, err
		}
		if v.Greedy {
			b.graph.AddEdge(split, part.entry)
			b.graph.AddEdge(split, join)
		} else {
			b.graph.AddEdge(split, join)
			b.graph.AddEdge(split, part.entry)
		}
		b.graph.AddEdge(part.exit, join)
		out.exit = join
	}
	if first {
		id := b.newNode(KindJoin)
		out = fragment{id, id}
	}
	return out, nil
}
