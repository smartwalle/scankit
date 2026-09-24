package engine

import (
	"testing"

	"github.com/smartwalle/scankit/internal/nfagraph"
	"github.com/smartwalle/scankit/internal/parser"
)

func TestProgramCloneAndMatch(t *testing.T) {
	g, err := nfagraph.NewBuilder().Build(parser.Literal{Value: []byte("x")})
	if err != nil {
		t.Fatal(err)
	}
	p, err := Build(g, KindNFA)
	if err != nil {
		t.Fatal(err)
	}
	clone := p.Clone()
	if clone == nil || clone.BackendName() != "nfa" || len(clone.Match([]byte("xx"))) != 2 {
		t.Fatal("clone mismatch")
	}
	if _, err := Build(g, Kind(99)); err == nil {
		t.Fatal("invalid kind accepted")
	}
}
