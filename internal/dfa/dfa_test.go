package dfa

import (
	"testing"

	"github.com/smartwalle/scankit/internal/nfagraph"
	"github.com/smartwalle/scankit/internal/parser"
)

func TestStateCount(t *testing.T) {
	r, _ := parser.Parse("ab")
	g, _ := nfagraph.NewBuilder().Build(r)
	if StateCount(g) == 0 {
		t.Fatal()
	}
	if _, e := Minimize(g); e != nil {
		t.Fatal(e)
	}
}

func TestMinimizeWithBudgetsKeepsExecutableTable(t *testing.T) {
	r, err := parser.Parse(`(?:ab|cb)`)
	if err != nil {
		t.Fatal(err)
	}
	g, err := nfagraph.NewBuilder().Build(r)
	if err != nil {
		t.Fatal(err)
	}
	p, err := MinimizeWithBudgets(g, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	if got := p.MatchAt([]byte("ab"), 0); len(got) != 1 || got[0] != 2 {
		t.Fatalf("最小化后匹配异常: %v", got)
	}
}
