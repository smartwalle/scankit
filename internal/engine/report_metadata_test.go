package engine

import (
	"reflect"
	"testing"

	"github.com/smartwalle/scankit/internal/nfagraph"
	"github.com/smartwalle/scankit/internal/parser"
)

// TestProgramReportMetadataPropagates 验证后端报告元数据从底层图传播到
// 引擎层，普通报告计入 ReportIDs，尾部报告不产生额外标记。
func TestProgramReportMetadataPropagates(t *testing.T) {
	r, err := parser.Parse("abc")
	if err != nil {
		t.Fatal(err)
	}
	plain, err := nfagraph.NewBuilder().Build(r)
	if err != nil {
		t.Fatal(err)
	}
	p, err := Build(plain, KindNFA)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.ReportIDs()) != 0 || p.HasEODReports() {
		t.Fatalf("无报告图不应携带报告元数据: %v", p.ReportIDs())
	}

	reportGraph := nfagraph.New()
	reportGraph.AddNode(nfagraph.Node{ID: 1, Kind: nfagraph.KindLiteral, Literal: []byte{'a'}})
	reportGraph.AddNode(nfagraph.Node{ID: 2, Kind: nfagraph.KindReport, ReportID: 5})
	reportGraph.AddNode(nfagraph.Node{ID: 3, Kind: nfagraph.KindAccept})
	reportGraph.AddEdge(0, 1)
	reportGraph.AddEdge(1, 2)
	reportGraph.AddEdge(2, 3)
	dfaProgram, err := Build(reportGraph, KindDFA)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := dfaProgram.ReportIDs(), []uint32{5}; !reflect.DeepEqual(got, want) {
		t.Fatalf("DFA 报告编号不一致: got=%v want=%v", got, want)
	}
	if dfaProgram.HasEODReports() {
		t.Fatal("普通报告不应标记为末尾报告")
	}

	nfaProgram, err := Build(reportGraph, KindNFA)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := nfaProgram.ReportIDs(), []uint32{5}; !reflect.DeepEqual(got, want) {
		t.Fatalf("NFA 报告编号不一致: got=%v want=%v", got, want)
	}
}

// TestProgramEODReportMetadata 验证末尾断言之后的报告在引擎层单独暴露。
func TestProgramEODReportMetadata(t *testing.T) {
	g := nfagraph.New()
	g.AddNode(nfagraph.Node{ID: 1, Kind: nfagraph.KindLiteral, Literal: []byte{'a'}})
	g.AddNode(nfagraph.Node{ID: 2, Kind: nfagraph.KindAssertion, Assertion: parser.End})
	g.AddNode(nfagraph.Node{ID: 3, Kind: nfagraph.KindReport, ReportID: 8})
	g.AddNode(nfagraph.Node{ID: 4, Kind: nfagraph.KindAccept})
	g.AddEdge(0, 1)
	g.AddEdge(1, 2)
	g.AddEdge(2, 3)
	g.AddEdge(3, 4)
	p, err := Build(g, KindDFA)
	if err != nil {
		t.Fatal(err)
	}
	if !p.HasEODReports() {
		t.Fatal("末尾报告未传播到引擎层")
	}
	if got, want := p.ReportIDs(), []uint32{8}; !reflect.DeepEqual(got, want) {
		t.Fatalf("末尾报告编号不一致: got=%v want=%v", got, want)
	}
}
