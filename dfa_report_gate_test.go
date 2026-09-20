package scankit

import (
	"testing"

	"github.com/smartwalle/scankit/internal/engine"
	"github.com/smartwalle/scankit/internal/nfagraph"
	"github.com/smartwalle/scankit/internal/parser"
)

// TestBackendEligibleRejectsEODReportProgram 验证携带末尾报告的后端会被
// 直接匹配快路径拒绝：字节状态表无法区分“任意位置接受”和“仅数据末尾接受”，
// 继续使用会在非末尾偏移产生误报。
func TestBackendEligibleRejectsEODReportProgram(t *testing.T) {
	root, err := parser.Parse(`a$`)
	if err != nil {
		t.Fatal(err)
	}
	g := nfagraph.New()
	g.AddNode(nfagraph.Node{ID: 1, Kind: nfagraph.KindLiteral, Literal: []byte{'a'}})
	g.AddNode(nfagraph.Node{ID: 2, Kind: nfagraph.KindAssertion, Assertion: parser.End})
	g.AddNode(nfagraph.Node{ID: 3, Kind: nfagraph.KindReport, ReportID: 8})
	g.AddNode(nfagraph.Node{ID: 4, Kind: nfagraph.KindAccept})
	g.AddEdge(0, 1)
	g.AddEdge(1, 2)
	g.AddEdge(2, 3)
	g.AddEdge(3, 4)
	program, err := engine.Build(g, engine.KindDFA)
	if err != nil {
		t.Fatal(err)
	}
	if !program.HasEODReports() {
		t.Fatal("构造的后端应携带末尾报告")
	}
	rule := compiledRule{root: root, program: program}
	if backendEligible(rule) {
		t.Fatal("末尾报告后端不应进入直接匹配快路径")
	}
	scanner := newScanner([]compiledRule{rule})
	if !scanner.rules[0].eodReports {
		t.Fatal("末尾报告标记未缓存到规则")
	}
	if scanner.rules[0].backendEligible {
		t.Fatal("缓存结果仍允许末尾报告后端直接匹配")
	}
}

// TestBackendEligibleAllowsPlainReportProgram 验证普通报告后端不受该门禁影响。
func TestBackendEligibleAllowsPlainReportProgram(t *testing.T) {
	root, err := parser.Parse("ab")
	if err != nil {
		t.Fatal(err)
	}
	g := nfagraph.New()
	g.AddNode(nfagraph.Node{ID: 1, Kind: nfagraph.KindLiteral, Literal: []byte{'a'}})
	g.AddNode(nfagraph.Node{ID: 2, Kind: nfagraph.KindLiteral, Literal: []byte{'b'}})
	g.AddNode(nfagraph.Node{ID: 3, Kind: nfagraph.KindReport, ReportID: 3})
	g.AddNode(nfagraph.Node{ID: 4, Kind: nfagraph.KindAccept})
	g.AddEdge(0, 1)
	g.AddEdge(1, 2)
	g.AddEdge(2, 3)
	g.AddEdge(3, 4)
	program, err := engine.Build(g, engine.KindDFA)
	if err != nil {
		t.Fatal(err)
	}
	if program.HasEODReports() {
		t.Fatal("普通报告不应标记为末尾报告")
	}
	rule := compiledRule{root: root, program: program}
	if !backendEligible(rule) {
		t.Fatal("固定宽度普通报告后端应保留直接匹配快路径")
	}
}
