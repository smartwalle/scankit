package dfa

import (
	"reflect"
	"testing"

	"github.com/smartwalle/scankit/internal/nfagraph"
	"github.com/smartwalle/scankit/internal/parser"
)

// literalReportGraph 构造 start -> 'a' -> report -> accept 的手工图。
func literalReportGraph(reportID uint32) *nfagraph.Graph {
	g := nfagraph.New()
	g.AddNode(nfagraph.Node{ID: 1, Kind: nfagraph.KindLiteral, Literal: []byte{'a'}})
	g.AddNode(nfagraph.Node{ID: 2, Kind: nfagraph.KindReport, ReportID: reportID})
	g.AddNode(nfagraph.Node{ID: 3, Kind: nfagraph.KindAccept})
	g.AddEdge(0, 1)
	g.AddEdge(1, 2)
	g.AddEdge(2, 3)
	return g
}

// eodReportGraph 构造 start -> 'a' -> 末尾断言 -> report -> accept 的手工图。
func eodReportGraph(reportID uint32) *nfagraph.Graph {
	g := nfagraph.New()
	g.AddNode(nfagraph.Node{ID: 1, Kind: nfagraph.KindLiteral, Literal: []byte{'a'}})
	g.AddNode(nfagraph.Node{ID: 2, Kind: nfagraph.KindAssertion, Assertion: parser.End})
	g.AddNode(nfagraph.Node{ID: 3, Kind: nfagraph.KindReport, ReportID: reportID})
	g.AddNode(nfagraph.Node{ID: 4, Kind: nfagraph.KindAccept})
	g.AddEdge(0, 1)
	g.AddEdge(1, 2)
	g.AddEdge(2, 3)
	g.AddEdge(3, 4)
	return g
}

// TestDeterminizePropagatesReportsToAcceptStates 验证确定化把报告编号
// 传播到接受状态，并且匹配仍然成立。
func TestDeterminizePropagatesReportsToAcceptStates(t *testing.T) {
	p, err := Compile(literalReportGraph(7))
	if err != nil {
		t.Fatal(err)
	}
	if !p.HasReports() {
		t.Fatal("包含报告节点的 DFA 应报告 HasReports=true")
	}
	if got, want := p.AcceptReportIDs(), []uint32{7}; !reflect.DeepEqual(got, want) {
		t.Fatalf("接受报告编号不一致: got=%v want=%v", got, want)
	}
	accepts := p.AcceptStates()
	if len(accepts) != 1 {
		t.Fatalf("接受状态数量异常: %v", accepts)
	}
	if got, want := p.ReportsFor(accepts[0]), []uint32{7}; !reflect.DeepEqual(got, want) {
		t.Fatalf("接受状态报告集合不一致: got=%v want=%v", got, want)
	}
	if len(p.EODReportsFor(accepts[0])) != 0 {
		t.Fatalf("普通报告不应计入末尾报告: %v", p.EODReportsFor(accepts[0]))
	}
	if got := p.MatchAt([]byte("a"), 0); len(got) != 1 || got[0] != 1 {
		t.Fatalf("报告图匹配异常: %v", got)
	}
}

// TestDeterminizeSeparatesEODReports 验证末尾断言之后的报告进入末尾集合，
// 不会在任意位置提前触发。
func TestDeterminizeSeparatesEODReports(t *testing.T) {
	p, err := Compile(eodReportGraph(9))
	if err != nil {
		t.Fatal(err)
	}
	if p.HasReports() {
		t.Fatal("末尾报告不应计入任意位置报告")
	}
	if !p.HasEODReports() {
		t.Fatal("末尾报告未传播到 ReportsEOD")
	}
	if got, want := p.AcceptReportIDs(), []uint32{9}; !reflect.DeepEqual(got, want) {
		t.Fatalf("末尾报告编号不一致: got=%v want=%v", got, want)
	}
	accepts := p.AcceptStates()
	if len(accepts) != 1 {
		t.Fatalf("接受状态数量异常: %v", accepts)
	}
	state := p.States[accepts[0]]
	if len(state.Reports) != 0 {
		t.Fatalf("末尾报告不应进入 Reports: %v", state.Reports)
	}
	if got, want := state.ReportsEOD, []uint32{9}; !reflect.DeepEqual(got, want) {
		t.Fatalf("末尾报告集合不一致: got=%v want=%v", got, want)
	}
}

// TestMinimizeKeepsReportSemantics 验证最小化的初始划分保留报告差异：
// 转移完全相同的接受状态若报告编号不同则不能合并。
func TestMinimizeKeepsReportSemantics(t *testing.T) {
	g := nfagraph.New()
	g.AddNode(nfagraph.Node{ID: 1, Kind: nfagraph.KindLiteral, Literal: []byte{'a'}})
	g.AddNode(nfagraph.Node{ID: 2, Kind: nfagraph.KindReport, ReportID: 1})
	g.AddNode(nfagraph.Node{ID: 3, Kind: nfagraph.KindAccept})
	g.AddNode(nfagraph.Node{ID: 4, Kind: nfagraph.KindLiteral, Literal: []byte{'b'}})
	g.AddNode(nfagraph.Node{ID: 5, Kind: nfagraph.KindReport, ReportID: 2})
	g.AddNode(nfagraph.Node{ID: 6, Kind: nfagraph.KindAccept})
	g.AddEdge(0, 1)
	g.AddEdge(1, 2)
	g.AddEdge(2, 3)
	g.AddEdge(0, 4)
	g.AddEdge(4, 5)
	g.AddEdge(5, 6)
	base, err := Compile(g)
	if err != nil {
		t.Fatal(err)
	}
	min, err := Minimize(g)
	if err != nil {
		t.Fatal(err)
	}
	if err := min.Validate(); err != nil {
		t.Fatal(err)
	}
	if got, want := min.AcceptReportIDs(), []uint32{1, 2}; !reflect.DeepEqual(got, want) {
		t.Fatalf("最小化丢失报告语义: got=%v want=%v", got, want)
	}
	reportSets := map[string]struct{}{}
	for _, id := range min.AcceptStates() {
		reportSets[reportPartitionKey(min.States[id])] = struct{}{}
	}
	if len(reportSets) != 2 {
		t.Fatalf("不同报告编号的接受状态被合并: %v", reportSets)
	}
	for _, input := range []string{"a", "b", "ab", "ba", ""} {
		for start := 0; start <= len(input); start++ {
			want := base.MatchAt([]byte(input), start)
			got := min.MatchAt([]byte(input), start)
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("最小化改变语言 input=%q start=%d got=%v want=%v", input, start, got, want)
			}
		}
	}
}

// TestMinimizeMergesStatesWithEqualReportSet 验证报告集合相同的等价状态仍会合并，
// 且合并后报告不重复。
func TestMinimizeMergesStatesWithEqualReportSet(t *testing.T) {
	g := nfagraph.New()
	g.AddNode(nfagraph.Node{ID: 1, Kind: nfagraph.KindLiteral, Literal: []byte{'a'}})
	g.AddNode(nfagraph.Node{ID: 2, Kind: nfagraph.KindReport, ReportID: 4})
	g.AddNode(nfagraph.Node{ID: 3, Kind: nfagraph.KindAccept})
	g.AddNode(nfagraph.Node{ID: 4, Kind: nfagraph.KindLiteral, Literal: []byte{'b'}})
	g.AddNode(nfagraph.Node{ID: 5, Kind: nfagraph.KindReport, ReportID: 4})
	g.AddNode(nfagraph.Node{ID: 6, Kind: nfagraph.KindAccept})
	g.AddEdge(0, 1)
	g.AddEdge(1, 2)
	g.AddEdge(2, 3)
	g.AddEdge(0, 4)
	g.AddEdge(4, 5)
	g.AddEdge(5, 6)
	min, err := Minimize(g)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := min.AcceptReportIDs(), []uint32{4}; !reflect.DeepEqual(got, want) {
		t.Fatalf("报告集合一致的状态未按预期合并: got=%v want=%v", got, want)
	}
	if got := len(min.AcceptStates()); got != 1 {
		t.Fatalf("报告集合相同的接受状态应合并: %v", min.AcceptStates())
	}
}

// TestCloneAndReloadPreserveReports 验证克隆与序列化往返保留报告集合。
func TestCloneAndReloadPreserveReports(t *testing.T) {
	p, err := Compile(literalReportGraph(11))
	if err != nil {
		t.Fatal(err)
	}
	clone := p.Clone()
	if got, want := clone.AcceptReportIDs(), []uint32{11}; !reflect.DeepEqual(got, want) {
		t.Fatalf("克隆丢失报告: got=%v want=%v", got, want)
	}
	data, err := p.Dump()
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(data)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := loaded.AcceptReportIDs(), []uint32{11}; !reflect.DeepEqual(got, want) {
		t.Fatalf("序列化往返丢失报告: got=%v want=%v", got, want)
	}
	for i := range loaded.States {
		if !reflect.DeepEqual(loaded.States[i].Reports, p.States[i].Reports) {
			t.Fatalf("状态 %d 报告集合往返不一致", i)
		}
	}
}

// TestValidateRejectsMalformedReports 验证校验拒绝未排序、重复和零值报告。
func TestValidateRejectsMalformedReports(t *testing.T) {
	cases := []struct {
		name    string
		reports []uint32
	}{
		{"unsorted", []uint32{9, 4}},
		{"duplicate", []uint32{4, 4}},
		{"zero", []uint32{0}},
		{"untraceable", []uint32{4, 77}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, err := Compile(literalReportGraph(4))
			if err != nil {
				t.Fatal(err)
			}
			accepts := p.AcceptStates()
			if len(accepts) == 0 {
				t.Fatal("缺少接受状态")
			}
			p.States[accepts[0]].Reports = tc.reports
			if err := p.Validate(); err == nil {
				t.Fatalf("非法报告集合应被拒绝: %v", tc.reports)
			}
		})
	}
}

// TestReverseProgramKeepsAcceptReports 验证反向确认程序与前向程序报告集合一致。
func TestReverseProgramKeepsAcceptReports(t *testing.T) {
	// 覆盖单字节和多字节文字，多字节节点在确定化前需要展开。
	for _, literal := range []string{"a", "ab"} {
		g := nfagraph.New()
		g.AddNode(nfagraph.Node{ID: 1, Kind: nfagraph.KindLiteral, Literal: []byte(literal)})
		g.AddNode(nfagraph.Node{ID: 2, Kind: nfagraph.KindReport, ReportID: 21})
		g.AddNode(nfagraph.Node{ID: 3, Kind: nfagraph.KindAccept})
		g.AddEdge(0, 1)
		g.AddEdge(1, 2)
		g.AddEdge(2, 3)
		forward, err := Compile(g)
		if err != nil {
			t.Fatal(err)
		}
		reverse, err := CompileReverse(g)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(reverse.AcceptReportIDs(), forward.AcceptReportIDs()) {
			t.Fatalf("字面量 %q 反向报告集合不一致: reverse=%v forward=%v", literal, reverse.AcceptReportIDs(), forward.AcceptReportIDs())
		}
		if got, want := forward.AcceptReportIDs(), []uint32{21}; !reflect.DeepEqual(got, want) {
			t.Fatalf("字面量 %q 前向报告集合异常: got=%v want=%v", literal, got, want)
		}
		data := append([]byte("x"), []byte(literal)...)
		if got, want := reverse.MatchReverseAt(data, len(data)), []int{1}; !reflect.DeepEqual(got, want) {
			t.Fatalf("字面量 %q 反向确认结果异常: got=%v want=%v", literal, got, want)
		}
	}
}
