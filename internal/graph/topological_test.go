package graph

import "testing"

func TestTopological(t *testing.T) {
	g := NewDirected()
	g.AddEdge(1, 2)
	g.AddEdge(2, 3)
	o, ok := g.Topological()
	if !ok || len(o) != 3 {
		t.Fatal(o, ok)
	}
}
