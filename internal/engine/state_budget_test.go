package engine

import (
	"github.com/smartwalle/scankit/internal/nfagraph"
	"github.com/smartwalle/scankit/internal/parser"
	"testing"
)

func TestSelectKindWithStateBudget(t *testing.T) {
	r, _ := parser.Parse("(a|b|c|d|e|f|g|h)+")
	g, _ := nfagraph.NewBuilder().Build(r)
	if got := SelectKindWithStateBudget(g, -1); got != KindNFA {
		t.Fatalf("负预算=%v", got)
	}
	_ = SelectKindWithStateBudget(g, 2)
}
