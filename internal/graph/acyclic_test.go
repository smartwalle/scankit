package graph

import "testing"

func TestIsAcyclic(t *testing.T) {
	g := NewDirected()
	g.AddEdge(1, 2)
	g.AddEdge(2, 1)
	if g.IsAcyclic() {
		t.Fatal()
	}
}
