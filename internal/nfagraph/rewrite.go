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
	return NormalizeWithCost(g, CostModel{})
}

// CostModel 描述图归一化的代价阈值与跳过策略。
// 字段均按 0 表示无约束；空结构等价于自动使用默认 pass 序列。
type CostModel struct {
	// MinNodesForMerge 在 MergeEquivalentNodes/SquashLinearLiterals 之前生效，
	// 小图收益低时直接跳过昂贵的 O(V²) 合并。
	MinNodesForMerge int
	// MinNodesForJoinBypass 在 BypassEpsilonJoins/SquashLinearJoins 之前生效，
	// 小图的 join 旁路收益不明显。
	MinNodesForJoinBypass int
}

// NormalizeWithCost 按 cost 模型决定每轮应用的归一化子集，
// 返回本次应用的 NormalizeStats。CostModel 全零时等价 NormalizeWithStats。
func NormalizeWithCost(g *Graph, cm CostModel) (NormalizeStats, error) {
	var stats NormalizeStats
	if g == nil {
		return stats, nil
	}
	work := g.Clone()
	NormalizeClasses(work)
	stats.RemovedEdges += RemoveRedundantEdges(work)
	stats.Unreachable = PruneUnreachable(work)
	stats.DeadEnds = PruneDeadEnds(work)
	nodeCount := len(work.Nodes)
	if cm.MinNodesForJoinBypass == 0 || nodeCount >= cm.MinNodesForJoinBypass {
		stats.BypassedJoins += BypassEpsilonJoins(work)
		stats.SquashedJoins += SquashLinearJoins(work)
	}
	if cm.MinNodesForMerge == 0 || nodeCount >= cm.MinNodesForMerge {
		stats.MergedLiterals = SquashLinearLiterals(work)
		stats.MergedNodes = MergeEquivalentNodes(work)
	}
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
// 返回的错误携带本轮累计 NormalizeStats 与最后修改图的 pass 名称，
// 便于调用方在编译期按 pass 粒度记录 optimization-fallback 的原因。
func OptimizeWithLimit(g *Graph, maxRounds int) (NormalizeStats, error) {
	return OptimizeWithCostAndLimit(g, CostModel{}, maxRounds)
}

// OptimizeWithCostAndLimit 在 OptimizeWithLimit 基础上叠加 CostModel，
// 允许小图跳过昂贵 pass，降低编译期 CPU 成本。
func OptimizeWithCostAndLimit(g *Graph, cm CostModel, maxRounds int) (NormalizeStats, error) {
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
	lastModifiedPass := ""
	for round := 0; round < maxRounds; round++ {
		before := g.Clone()
		stats, err := NormalizeWithCost(g, cm)
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
		// 记录本轮中真正改变图的 pass，作为失败时的可定位原因。
		lastModifiedPass = lastChangingPass(stats)
	}
	// 收敛失败时恢复调用方图，避免将半成品继续交给后端。
	if original != nil {
		g.Flow, g.Nodes, g.Start = original.Flow, original.Nodes, original.Start
	}
	if lastModifiedPass != "" {
		return total, fmt.Errorf("graph optimization did not converge after %d rounds (last modified by %s, edges=%d nodes=%d)", maxRounds, lastModifiedPass, total.RemovedEdges, total.MergedNodes+total.MergedLiterals)
	}
	return total, fmt.Errorf("graph optimization did not converge after %d rounds", maxRounds)
}

// lastChangingPass 根据一轮 NormalizeStats 判断哪一个 pass 修改了图。
// 优先级与 NormalizeWithStats 的调用顺序一致：合并节点/合并文字通常是震荡源。
func lastChangingPass(s NormalizeStats) string {
	switch {
	case s.MergedNodes > 0:
		return "MergeEquivalentNodes"
	case s.MergedLiterals > 0:
		return "SquashLinearLiterals"
	case s.SquashedJoins > 0:
		return "SquashLinearJoins"
	case s.BypassedJoins > 0:
		return "BypassEpsilonJoins"
	case s.DeadEnds > 0:
		return "PruneDeadEnds"
	case s.Unreachable > 0:
		return "PruneUnreachable"
	case s.RemovedEdges > 0:
		return "RemoveRedundantEdges"
	}
	return ""
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
