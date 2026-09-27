package nfagraph

import (
	"testing"

	"github.com/smartwalle/scankit/internal/parser"
)

func TestAccepts(t *testing.T) {
	r, _ := parser.Parse("a")
	g, _ := NewBuilder().Build(r)
	if len(g.Accepts()) != 1 {
		t.Fatal()
	}
}
