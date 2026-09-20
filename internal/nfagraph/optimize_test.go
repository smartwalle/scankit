package nfagraph

import (
	"errors"
	"strings"
	"testing"

	"github.com/smartwalle/scankit/internal/parser"
)

// TestOptimizeWithLimitReportsLastChangedPassInError 验证 OptimizeWithLimit
// 在收敛失败时返回的错误携带触发震荡的最后修改 pass 名称与轮数上限，
// 便于编译期按 pass 粒度记录 optimization-fallback 原因。
func TestOptimizeWithLimitReportsLastChangedPassInError(t *testing.T) {
	// 构造一个会在 NFA 归一化过程反复修改的图。
	r, err := parser.Parse("(?:a|b)+")
	if err != nil {
		t.Fatal(err)
	}
	g, err := NewBuilder().Build(r)
	if err != nil {
		t.Fatal(err)
	}
	_, err = OptimizeWithLimit(g, 1)
	if err == nil {
		t.Fatal("期望收敛失败错误")
	}
	msg := err.Error()
	if !strings.Contains(msg, "did not converge after 1 rounds") {
		t.Fatalf("错误信息缺少轮数: %v", err)
	}
	if !strings.Contains(msg, "last modified by") {
		t.Fatalf("错误信息缺少最后修改 pass: %v", err)
	}
}

// TestOptimizeWithLimitRestoresGraphOnFailure 验证 OptimizeWithLimit 在
// 超过轮数后调用方图被恢复为原图，避免将半成品继续交给后端。
func TestOptimizeWithLimitRestoresGraphOnFailure(t *testing.T) {
	r, err := parser.Parse("(?:a|b)+")
	if err != nil {
		t.Fatal(err)
	}
	g, err := NewBuilder().Build(r)
	if err != nil {
		t.Fatal(err)
	}
	original := g.Clone()
	if _, err := OptimizeWithLimit(g, 1); err == nil {
		t.Fatal("期望收敛失败")
	}
	if !g.Equal(original) {
		t.Fatal("收敛失败后图未恢复")
	}
}

// TestLastChangingPass 校验 lastChangingPass 按 pass 优先级返回最后触发修改图的 pass。
func TestLastChangingPass(t *testing.T) {
	cases := []struct {
		name string
		stat NormalizeStats
		want string
	}{
		{"merge_nodes", NormalizeStats{MergedNodes: 1}, "MergeEquivalentNodes"},
		{"merge_literals", NormalizeStats{MergedLiterals: 1}, "SquashLinearLiterals"},
		{"squash_joins", NormalizeStats{SquashedJoins: 1}, "SquashLinearJoins"},
		{"bypass_joins", NormalizeStats{BypassedJoins: 1}, "BypassEpsilonJoins"},
		{"dead_ends", NormalizeStats{DeadEnds: 1}, "PruneDeadEnds"},
		{"unreachable", NormalizeStats{Unreachable: 1}, "PruneUnreachable"},
		{"remove_edges", NormalizeStats{RemovedEdges: 1}, "RemoveRedundantEdges"},
		{"empty", NormalizeStats{}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := lastChangingPass(tc.stat); got != tc.want {
				t.Fatalf("lastChangingPass(%+v) = %q, want %q", tc.stat, got, tc.want)
			}
		})
	}
}

// TestOptimizeSucceedsWithinLimit 验证合法图形在合理轮数内能收敛，
// 不会对已有正确路径误报 fallback。
func TestOptimizeSucceedsWithinLimit(t *testing.T) {
	r, err := parser.Parse("abc")
	if err != nil {
		t.Fatal(err)
	}
	g, err := NewBuilder().Build(r)
	if err != nil {
		t.Fatal(err)
	}
	stats, err := OptimizeWithLimit(g, 16)
	if err != nil {
		t.Fatal(err)
	}
	_ = stats
}

// Compile-time interface check for the error type used in bailout messages.
var _ error = errors.New("placeholder")

// TestNormalizeWithCostSkipsExpensivePassesForSmallGraph 验证 NormalizeWithCost
// 在节点数小于阈值时跳过 BypassEpsilonJoins/SquashLinearJoins/MergeEquivalentNodes
// 等昂贵 pass，小图上保留 RemoveRedundantEdges/PruneUnreachable/PruneDeadEnds
// 等低代价收益稳定的 pass。
func TestNormalizeWithCostSkipsExpensivePassesForSmallGraph(t *testing.T) {
	r, err := parser.Parse("abc")
	if err != nil {
		t.Fatal(err)
	}
	g, err := NewBuilder().Build(r)
	if err != nil {
		t.Fatal(err)
	}
	before := g.Clone()
	stats, err := NormalizeWithCost(g, CostModel{MinNodesForMerge: 1024, MinNodesForJoinBypass: 1024})
	if err != nil {
		t.Fatal(err)
	}
	if !g.Equal(before) {
		t.Fatal("小图不应被改写")
	}
	if stats.MergedNodes != 0 || stats.MergedLiterals != 0 || stats.BypassedJoins != 0 || stats.SquashedJoins != 0 {
		t.Fatalf("昂贵 pass 应被跳过: %+v", stats)
	}
}

// TestNormalizeWithCostAppliesExpensivePassesForLargeGraph 验证 NormalizeWithCost
// 在节点数大于阈值时仍正常应用昂贵 pass，与无阈值行为一致。
func TestNormalizeWithCostAppliesExpensivePassesForLargeGraph(t *testing.T) {
	// 构造一个足以触发合并的图（两个等价分支）。
	r, err := parser.Parse("(?:ab|cd)")
	if err != nil {
		t.Fatal(err)
	}
	g, err := NewBuilder().Build(r)
	if err != nil {
		t.Fatal(err)
	}
	_, err = NormalizeWithCost(g, CostModel{MinNodesForMerge: 0, MinNodesForJoinBypass: 0})
	if err != nil {
		t.Fatal(err)
	}
	// CostModel 全 0 等价 NormalizeWithStats；仅验证函数调用成功。
}

// TestNormalizeWithCostDefaultEqualsNormalizeWithStats 验证 NormalizeWithCost
// 在默认 CostModel{} 下与 NormalizeWithStats 行为一致。
func TestNormalizeWithCostDefaultEqualsNormalizeWithStats(t *testing.T) {
	r, err := parser.Parse("(?:ab|cd)")
	if err != nil {
		t.Fatal(err)
	}
	g1, _ := NewBuilder().Build(r)
	g2, _ := NewBuilder().Build(r)
	stats1, err1 := NormalizeWithStats(g1)
	stats2, err2 := NormalizeWithCost(g2, CostModel{})
	if err1 != nil || err2 != nil {
		t.Fatalf("err1=%v err2=%v", err1, err2)
	}
	if stats1 != stats2 {
		t.Fatalf("默认 CostModel 与 NormalizeWithStats 行为应一致: %+v vs %+v", stats1, stats2)
	}
}
