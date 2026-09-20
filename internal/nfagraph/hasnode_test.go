package nfagraph

import (
	"github.com/smartwalle/scankit/internal/parser"
	"testing"
)

func TestHasNode(t *testing.T) {
	r, _ := parser.Parse("a")
	g, _ := NewBuilder().Build(r)
	if !g.HasNode(g.Start) {
		t.Fatal()
	}
}
