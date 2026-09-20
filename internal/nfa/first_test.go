package nfa

import (
	"github.com/smartwalle/scankit/internal/nfagraph"
	"github.com/smartwalle/scankit/internal/parser"
	"testing"
)

func TestMatchFirst(t *testing.T) {
	r, _ := parser.Parse("b")
	g, _ := nfagraph.NewBuilder().Build(r)
	p, _ := Compile(g)
	a, z, ok := p.MatchFirst([]byte("ab"))
	if !ok || a != 1 || z != 2 {
		t.Fatal(a, z, ok)
	}
}
