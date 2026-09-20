package dfa

import (
	"github.com/smartwalle/scankit/internal/nfagraph"
	"github.com/smartwalle/scankit/internal/parser"
	"testing"
)

func TestIsDeterministic(t *testing.T) {
	r, _ := parser.Parse("a|b")
	g, _ := nfagraph.NewBuilder().Build(r)
	if IsDeterministic(g) {
		t.Fatal()
	}
}
