package nfa

import (
	"reflect"
	"testing"

	"github.com/smartwalle/scankit/internal/nfagraph"
)

// TestProgramReportIDsCollectsGraphReports 验证 NFA 报告编号快照按升序去重，
// 且忽略零值占位的报告节点。
func TestProgramReportIDsCollectsGraphReports(t *testing.T) {
	g := nfagraph.New()
	g.AddNode(nfagraph.Node{ID: 1, Kind: nfagraph.KindLiteral, Literal: []byte{'a'}})
	g.AddNode(nfagraph.Node{ID: 2, Kind: nfagraph.KindReport, ReportID: 4})
	g.AddNode(nfagraph.Node{ID: 3, Kind: nfagraph.KindJoin})
	g.AddNode(nfagraph.Node{ID: 4, Kind: nfagraph.KindReport, ReportID: 2})
	g.AddNode(nfagraph.Node{ID: 5, Kind: nfagraph.KindReport, ReportID: 2})
	g.AddNode(nfagraph.Node{ID: 6, Kind: nfagraph.KindReport, ReportID: 0})
	g.AddNode(nfagraph.Node{ID: 7, Kind: nfagraph.KindAccept})
	g.AddEdge(0, 1)
	g.AddEdge(1, 2)
	g.AddEdge(2, 3)
	g.AddEdge(3, 4)
	g.AddEdge(4, 5)
	g.AddEdge(5, 6)
	g.AddEdge(6, 7)
	p, err := Compile(g)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := p.ReportIDs(), []uint32{2, 4}; !reflect.DeepEqual(got, want) {
		t.Fatalf("报告编号快照不一致: got=%v want=%v", got, want)
	}
	ids := p.ReportIDs()
	ids[0] = 99
	if p.ReportIDs()[0] != 2 {
		t.Fatal("报告编号快照被调用方修改污染")
	}
}
