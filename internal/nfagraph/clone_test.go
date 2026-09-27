package nfagraph

import (
	"testing"

	"github.com/smartwalle/scankit/internal/parser"
)

func TestClone(t *testing.T) {
	r, _ := parser.Parse("abc")
	g, _ := NewBuilder().Build(r)
	c := g.Clone()
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	if len(c.Nodes) != len(g.Nodes) {
		t.Fatal()
	}
}
