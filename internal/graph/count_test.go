package graph

import "testing"

func TestEdgeCountRemove(t *testing.T) {
	g := NewDirected()
	g.AddEdge(1, 2)
	g.AddEdge(2, 3)
	if g.EdgeCount() != 2 {
		t.Fatal()
	}
	g.RemoveVertex(2)
	if g.EdgeCount() != 0 {
		t.Fatal()
	}
}
