package compiler

import "testing"

func TestLimitsMerge(t *testing.T) {
	x := CompileLimits{GraphVertices: 5}
	y := CompileLimits{GraphVertices: 3, GraphEdges: 2}
	z := x.Merge(y)
	if z.GraphVertices != 3 || z.GraphEdges != 2 {
		t.Fatal(z)
	}
}
