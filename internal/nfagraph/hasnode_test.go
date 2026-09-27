package nfagraph

import (
	"testing"

	"github.com/smartwalle/scankit/internal/parser"
)

func TestHasNode(t *testing.T) {
	r, _ := parser.Parse("a")
	g, _ := NewBuilder().Build(r)
	if !g.HasNode(g.Start) {
		t.Fatal()
	}
}
