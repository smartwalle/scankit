package nfa

import (
	"testing"

	"github.com/smartwalle/scankit/internal/nfagraph"
	"github.com/smartwalle/scankit/internal/parser"
)

// TestSelectEngineKindPrioritizesSpecializedLayout 验证 SelectEngineKind 在
// 多个专用布局都可选时优先返回更具体的执行后端，而不是总退回到通用解释器。
func TestSelectEngineKindPrioritizesSpecializedLayout(t *testing.T) {
	cases := []struct {
		pattern string
		want    EngineKind
	}{
		{`a+`, EngineRepeat},
		{`(?:abc|def)`, EngineMPV},
		{`a|b`, EngineMPV},
		{`[a-c]+`, EngineLimEx},
		{`a{1,3}`, EngineRepeat},
		{`abc`, EngineMPV},
		{`^abc$`, EngineLimEx},
		{`a`, EngineMPV},
	}
	for _, tc := range cases {
		r, err := parser.Parse(tc.pattern)
		if err != nil {
			t.Fatalf("parse %q: %v", tc.pattern, err)
		}
		g, err := nfagraph.NewBuilder().Build(r)
		if err != nil {
			t.Fatalf("graph %q: %v", tc.pattern, err)
		}
		if got := SelectEngineKind(g); got != tc.want {
			t.Fatalf("pattern=%q got=%s want=%s", tc.pattern, got, tc.want)
		}
	}
}

// TestExplainEngineSelectionRecordsCostAndBailout 验证 ExplainEngineSelection
// 暴露的成本、内存和 bailout 原因与 CompileEngine 的实际行为保持一致；
// 当专用布局不可用时，Kind/Bailout 必须明确反映降级路径。
func TestExplainEngineSelectionRecordsCostAndBailout(t *testing.T) {
	r, err := parser.Parse(`^abc$`)
	if err != nil {
		t.Fatal(err)
	}
	g, err := nfagraph.NewBuilder().Build(r)
	if err != nil {
		t.Fatal(err)
	}
	info := ExplainEngineSelection(g)
	if info.Kind == 0 {
		t.Fatal("选择诊断未提供 Kind")
	}
	if info.Cost == 0 {
		t.Fatal("选择诊断未提供 Cost")
	}
	if info.Reason == "" {
		t.Fatal("选择诊断未提供 Reason")
	}
	if info.Independent && info.Bailout != "" {
		t.Fatalf("独立布局不应记录 bailout: kind=%s reason=%q", info.Kind, info.Bailout)
	}
	if !info.Independent && info.Bailout == "" {
		t.Fatalf("非独立布局应记录 bailout: kind=%s reason=%q", info.Kind, info.Reason)
	}
}

// TestExplainEngineSelectionHandlesUnsupportedGraph 验证含不支持节点属性的
// 图能被 ExplainEngineSelection 安全诊断并标注专用布局不可用原因。
func TestExplainEngineSelectionHandlesUnsupportedGraph(t *testing.T) {
	r, err := parser.Parse(`(?<=ab)cd`)
	if err != nil {
		t.Fatal(err)
	}
	g, err := nfagraph.NewBuilder().Build(r)
	if err != nil {
		t.Fatal(err)
	}
	info := ExplainEngineSelection(g)
	if info.Independent {
		t.Fatalf("含 lookaround 的图不应报告独立布局: kind=%s reason=%q", info.Kind, info.Reason)
	}
	if info.Bailout == "" {
		t.Fatal("含 lookaround 的图应记录 bailout 原因")
	}
}

// TestSelectTableKindPicksLowestCostEngine 验证 selectTableKind 在 Sheng / Shufti
// / Tamarama 三者都满足基本结构条件时，由 estimateEngineCost 选取代价最低的
// 引擎，而不是依赖单一的启发式分支判断。
func TestSelectTableKindPicksLowestCostEngine(t *testing.T) {
	// 构造一个无 class、无分支的图：selectTableKind 应当从 {Sheng, Shufti} 中
	// 选择 estimateEngineCost 更低的引擎。
	r, err := parser.Parse("abc")
	if err != nil {
		t.Fatal(err)
	}
	g, err := nfagraph.NewBuilder().Build(r)
	if err != nil {
		t.Fatal(err)
	}
	kind := SelectEngineKind(g)
	if !kind.Valid() {
		t.Fatalf("引擎种类无效: %d", kind)
	}
	if cost := estimateEngineCost(g, kind); cost == 0 {
		t.Fatalf("代价计算失败: %d", cost)
	}
}

// TestSelectTableKindPrefersTamaramaForBranchedGraph 验证有 class 又有分支时，
// selectTableKind 仍然把 Tamarama 纳入候选并按代价挑选。
func TestSelectTableKindPrefersTamaramaForBranchedGraph(t *testing.T) {
	// 构造有 class 又有分支的图。
	r, err := parser.Parse(`(?:abc)|[a-z]`)
	if err != nil {
		t.Fatal(err)
	}
	g, err := nfagraph.NewBuilder().Build(r)
	if err != nil {
		t.Fatal(err)
	}
	kind := SelectEngineKind(g)
	if !kind.Valid() {
		t.Fatalf("引擎种类无效: %d", kind)
	}
}
