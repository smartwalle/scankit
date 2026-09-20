package engine

import (
	"github.com/smartwalle/scankit/internal/nfagraph"
	"github.com/smartwalle/scankit/internal/parser"
	"testing"
)

func TestBuildBackends(t *testing.T) {
	r, _ := parser.Parse("abc")
	g, e := nfagraph.NewBuilder().Build(r)
	if e != nil {
		t.Fatal(e)
	}
	for _, k := range []Kind{KindNFA, KindDFA} {
		p, e := Build(g, k)
		if e != nil {
			t.Fatal(e)
		}
		if len(p.MatchAt([]byte("abc"), 0)) != 1 {
			t.Fatal(k)
		}
	}
}

func TestProgramRejectsAmbiguousBackends(t *testing.T) {
	g, _ := nfagraph.NewBuilder().Build(parser.Literal{Value: []byte("x")})
	n, _ := Build(g, KindNFA)
	d, _ := Build(g, KindDFA)
	if (&Program{Kind: KindNFA, NFA: n.NFA, DFA: d.DFA}).Validate() == nil {
		t.Fatal("未拒绝二义性后端")
	}
}
