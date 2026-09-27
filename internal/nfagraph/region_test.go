package nfagraph

import (
	"testing"

	"github.com/smartwalle/scankit/internal/parser"
)

func TestAnalysisRegions(t *testing.T) {
	r, _ := parser.Parse("a*")
	g, _ := NewBuilder().Build(r)
	a := Analyze(g)
	if a.Depth[g.Start] != 0 {
		t.Fatal()
	}
}

func TestAnalysisQueriesAndEquivalent(t *testing.T) {
	r, err := parser.Parse("ab")
	if err != nil {
		t.Fatal(err)
	}
	g, err := NewBuilder().Build(r)
	if err != nil {
		t.Fatal(err)
	}
	a := Analyze(g)
	if a.DepthOf(g.Start) != 0 || a.ReverseDepthOf(g.Start) < 0 || !a.Dominates(g.Start, g.Start) {
		t.Fatal("分析查询失败")
	}
	if !Equivalent(g, g.Clone()) {
		t.Fatal("图等价判断失败")
	}
}
