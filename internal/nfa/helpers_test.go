package nfa

import (
	"github.com/smartwalle/scankit/internal/nfagraph"
	"github.com/smartwalle/scankit/internal/parser"
	"testing"
)

func TestMatchHelpers(t *testing.T) {
	g, err := nfagraph.NewBuilder().Build(parser.Literal{Value: []byte("ab")})
	if err != nil {
		t.Fatal(err)
	}
	p, err := Compile(g)
	if err != nil {
		t.Fatal(err)
	}
	if !p.Matches([]byte("zab")) {
		t.Fatal("expected match")
	}
	if end, ok := p.MatchAtFirst([]byte("ab"), 0); !ok || end != 2 {
		t.Fatalf("got %d %v", end, ok)
	}
}
