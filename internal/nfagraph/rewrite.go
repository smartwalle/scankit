package nfagraph

import (
	"fmt"
	"reflect"

	"github.com/smartwalle/scankit/internal/graph"
	"github.com/smartwalle/scankit/internal/parser"
)

// NormalizeStats 记录一次图规范化各阶段删除的节点和边数量。
type NormalizeStats struct {
	RemovedEdges   int
	Unreachable    int
	DeadEnds       int
	BypassedJoins  int
	SquashedJoins  int
	MergedNodes    int
	MergedLiterals int
}

// Normalize 执行保持语义的图结构清理流程。
func Normalize(g *Graph) error {
	if g == nil {
		return nil
	}
	work := g.Clone()
	NormalizeClasses(work)
	RemoveRedundantEdges(work)
	PruneUnreachable(work)
	PruneDeadEnds(work)
	BypassEpsilonJoins(work)
	SquashLinearJoins(work)
	SquashLinearLiterals(work)
	MergeEquivalentNodes(work)
	RemoveRedundantEdges(work)
	if err := work.Validate(); err != nil {
		return err
	}
	g.Flow, g.Nodes, g.Start = work.Flow, work.Nodes, work.Start
	return nil
}

// NormalizeWithStats 执行规范化并返回各阶段统计信息。
func NormalizeWithStats(g *Graph) (NormalizeStats, error) {
	var stats NormalizeStats
	if g == nil {
		return stats, nil
	}
	work := g.Clone()
	NormalizeClasses(work)
	stats.RemovedEdges += RemoveRedundantEdges(work)
	stats.Unreachable = PruneUnreachable(work)
	stats.DeadEnds = PruneDeadEnds(work)
	stats.BypassedJoins = BypassEpsilonJoins(work)
	stats.SquashedJoins = SquashLinearJoins(work)
	stats.MergedLiterals = SquashLinearLiterals(work)
	stats.MergedNodes = MergeEquivalentNodes(work)
	stats.RemovedEdges += RemoveRedundantEdges(work)
	if err := work.Validate(); err != nil {
		return stats, err
	}
	g.Flow, g.Nodes, g.Start = work.Flow, work.Nodes, work.Start
	return stats, nil
}

// NormalizeClasses 规范化所有字节字符类的区间顺序并合并相邻区间。
// Unicode 字符类保持独立，避免被错误地下沉到字节路径。
func NormalizeClasses(g *Graph) int {
	if g == nil {
		return 0
	}
	changed := 0
	for _, n := range g.Nodes {
		if n == nil || n.Kind != KindClass || n.Unicode != nil {
			continue
		}
		before := append([]parser.Range(nil), n.Class.Ranges...)
		normalized, ok := parser.Normalize(n.Class).(parser.Class)
		if !ok {
			continue
		}
		n.Class = normalized
		if !reflect.DeepEqual(before, n.Class.Ranges) {
			changed++
		}
	}
	return changed
}

// Optimize 反复执行图规范化直到结构稳定，适合编译器在多轮重写后收敛。
// 每轮都在副本上验证，超过上限时返回错误，避免异常图触发无限重写。
func Optimize(g *Graph) (NormalizeStats, error) {
	return OptimizeWithLimit(g, 16)
}

// OptimizeWithLimit 执行有界图重写；超过轮数时恢复原图并返回错误。
func OptimizeWithLimit(g *Graph, maxRounds int) (NormalizeStats, error) {
	var total NormalizeStats
	if g == nil {
		return total, nil
	}
	if maxRounds <= 0 {
		return total, fmt.Errorf("graph optimization round limit must be positive")
	}
	if g.Flow == nil {
		return total, fmt.Errorf("graph has no flow")
	}
	original := g.Clone()
	for round := 0; round < maxRounds; round++ {
		before := g.Clone()
		stats, err := NormalizeWithStats(g)
		if err != nil {
			return total, err
		}
		total.RemovedEdges += stats.RemovedEdges
		total.Unreachable += stats.Unreachable
		total.DeadEnds += stats.DeadEnds
		total.BypassedJoins += stats.BypassedJoins
		total.SquashedJoins += stats.SquashedJoins
		total.MergedNodes += stats.MergedNodes
		total.MergedLiterals += stats.MergedLiterals
		if before.Equal(g) {
			return total, nil
		}
	}
	// 收敛失败时恢复调用方图，避免将半成品继续交给后端。
	if original != nil {
		g.Flow, g.Nodes, g.Start = original.Flow, original.Nodes, original.Start
	}
	return total, fmt.Errorf("graph optimization did not converge")
}

// SquashLinearLiterals 合并无分支的相邻文字节点，减少执行状态数量。
func SquashLinearLiterals(g *Graph) int {
	if g == nil || g.Flow == nil {
		return 0
	}
	merged := 0
	for changed := true; changed; {
		changed = false
		for _, id := range g.Flow.Vertices() {
			node := g.Nodes[id]
			if node == nil || node.Kind != KindLiteral || node.ReportID != 0 {
				continue
			}
			succ := g.Flow.Successors(id)
			if len(succ) != 1 {
				continue
			}
			nextID := succ[0]
			next := g.Nodes[nextID]
			if next == nil || next.Kind != KindLiteral || next.ReportID != 0 || len(g.Flow.Predecessors(nextID)) != 1 {
				continue
			}
			node.Literal = append(node.Literal, next.Literal...)
			for _, to := range g.Flow.Successors(nextID) {
				g.Flow.AddEdge(id, to)
			}
			g.Flow.RemoveVertex(nextID)
			delete(g.Nodes, nextID)
			merged++
			changed = true
			break
		}
	}
	return merged
}

// BypassEpsilonJoins 移除只有一个出口的无观测连接节点，并保留所有入边。
func BypassEpsilonJoins(g *Graph) int {
	if g == nil || g.Flow == nil {
		return 0
	}
	removed := 0
	for changed := true; changed; {
		changed = false
		for _, id := range g.Flow.Vertices() {
			node := g.Nodes[id]
			if node == nil || node.Kind != KindJoin || id == g.Start || node.ReportID != 0 {
				continue
			}
			preds, successors := g.Flow.Predecessors(id), g.Flow.Successors(id)
			if len(preds) == 0 || len(successors) != 1 || successors[0] == id {
				continue
			}
			for _, pred := range preds {
				if pred != id {
					g.Flow.AddEdge(pred, successors[0])
				}
			}
			g.Flow.RemoveVertex(id)
			delete(g.Nodes, id)
			removed++
			changed = true
			break
		}
	}
	return removed
}

// PruneDeadEnds 删除无法到达接受节点的死分支，返回删除数量。
func PruneDeadEnds(g *Graph) int {
	if g == nil || g.Flow == nil {
		return 0
	}
	reverse := g.Flow.Reverse()
	keep := map[graph.Vertex]struct{}{}
	keep[g.Start] = struct{}{}
	queue := make([]graph.Vertex, 0, len(g.Accepts()))
	for _, id := range g.Accepts() {
		keep[id] = struct{}{}
		queue = append(queue, id)
	}
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		for _, pred := range reverse.Successors(id) {
			if _, ok := keep[pred]; ok {
				continue
			}
			keep[pred] = struct{}{}
			queue = append(queue, pred)
		}
	}
	removed := 0
	for id := range g.Nodes {
		if _, ok := keep[id]; !ok {
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

// SquashLinearJoins 移除只有一个前驱和一个后继的连接节点。
func SquashLinearJoins(g *Graph) int {
	if g == nil || g.Flow == nil {
		return 0
	}
	removed := 0
	for changed := true; changed; {
		changed = false
		for _, id := range g.Flow.Vertices() {
			node := g.Nodes[id]
			if node == nil || node.Kind != KindJoin || id == g.Start {
				continue
			}
			preds, successors := g.Flow.Predecessors(id), g.Flow.Successors(id)
			if len(preds) != 1 || len(successors) != 1 || preds[0] == id || successors[0] == id {
				continue
			}
			g.Flow.AddEdge(preds[0], successors[0])
			g.Flow.RemoveVertex(id)
			delete(g.Nodes, id)
			removed++
			changed = true
			break
		}
	}
	return removed
}

// MergeEquivalentNodes 合并具有相同可观察负载和相同后继的无报告节点。
func MergeEquivalentNodes(g *Graph) int {
	if g == nil || g.Flow == nil {
		return 0
	}
	removed := 0
	for {
		ids := g.Flow.Vertices()
		merged := false
		for left := 0; left < len(ids) && !merged; left++ {
			aID, a := ids[left], g.Nodes[ids[left]]
			if !mergeableNode(g, aID, a) {
				continue
			}
			for right := left + 1; right < len(ids); right++ {
				bID, b := ids[right], g.Nodes[ids[right]]
				if !mergeableNode(g, bID, b) || !equalMergeableNode(a, b) || !reflect.DeepEqual(g.Flow.Successors(aID), g.Flow.Successors(bID)) {
					continue
				}
				for _, pred := range g.Flow.Predecessors(bID) {
					g.Flow.AddEdge(pred, aID)
				}
				g.Flow.RemoveVertex(bID)
				delete(g.Nodes, bID)
				removed++
				merged = true
				break
			}
		}
		if !merged {
			// 合并过程中可能为同一前驱产生重复边，统一重建边索引。
			RemoveRedundantEdges(g)
			return removed
		}
	}
}

func mergeableNode(g *Graph, id graph.Vertex, node *Node) bool {
	if node == nil || id == g.Start || node.ReportID != 0 {
		return false
	}
	switch node.Kind {
	case KindLiteral, KindClass, KindAssertion, KindJoin:
		return true
	}
	return false
}

func equalMergeableNode(a, b *Node) bool {
	if a == nil || b == nil || a.Kind != b.Kind || a.Assertion != b.Assertion || a.RepeatMin != b.RepeatMin || a.RepeatMax != b.RepeatMax || a.Greedy != b.Greedy || !reflect.DeepEqual(a.Class, b.Class) {
		return false
	}
	if string(a.Literal) != string(b.Literal) {
		return false
	}
	if a.Unicode == nil || b.Unicode == nil {
		return a.Unicode == nil && b.Unicode == nil
	}
	return *a.Unicode == *b.Unicode
}

// ExpandLiterals 将多字节文字节点展开为等价的单字节节点链。
func ExpandLiterals(g *Graph) *Graph {
	if g == nil || g.Flow == nil {
		return nil
	}
	// 展开前先估算节点和边的上限，避免超长文字在构建中间图时
	// 瞬间分配不可控的大块内存。
	const maxExpandedNodes = 1 << 20
	const maxExpandedBytes = uint64(1 << 26)
	expandedNodes := 0
	var expandedBytes uint64
	for _, id := range g.Flow.Vertices() {
		n := g.Nodes[id]
		if n == nil {
			continue
		}
		count := 1
		if n.Kind == KindLiteral && len(n.Literal) > 1 {
			count = len(n.Literal)
		}
		if expandedNodes > maxExpandedNodes-count {
			return nil
		}
		expandedNodes += count
		if uint64(count) > maxExpandedBytes/64 || expandedBytes > maxExpandedBytes-uint64(count)*64 {
			return nil
		}
		expandedBytes += uint64(count) * 64
	}
	out := New()
	out.Start = g.Start
	out.Nodes = make(map[graph.Vertex]*Node, len(g.Nodes))
	out.Flow = graph.NewDirected()
	next := graph.Vertex(0)
	for id := range g.Nodes {
		if id >= next {
			next = id + 1
		}
	}
	entries := make(map[graph.Vertex]graph.Vertex, len(g.Nodes))
	exits := make(map[graph.Vertex]graph.Vertex, len(g.Nodes))
	for _, id := range g.Flow.Vertices() {
		node := g.Nodes[id]
		if node == nil {
			continue
		}
		entries[id], exits[id] = id, id
		if node.Kind != KindLiteral || len(node.Literal) == 1 {
			out.AddNode(node.Clone())
			continue
		}
		if len(node.Literal) == 0 {
			out.AddNode(Node{ID: id, Kind: KindJoin})
			continue
		}
		first := node.Clone()
		first.Literal = []byte{node.Literal[0]}
		out.AddNode(first)
		previous := id
		for _, value := range node.Literal[1:] {
			part := node.Clone()
			part.ID = next
			part.Literal = []byte{value}
			part.ReportID = 0
			out.AddNode(part)
			out.AddEdge(previous, next)
			previous = next
			next++
		}
		exits[id] = previous
	}
	for _, from := range g.Flow.Vertices() {
		for _, to := range g.Flow.Successors(from) {
			out.AddEdge(exits[from], entries[to])
		}
	}
	return out
}
