package graph

import "testing"

func TestCloneGraph(t *testing.T) {
	g := NewDirected()
	g.AddEdge(1, 2)
	if g.Clone().EdgeCount() != 1 {
		t.Fatal()
	}
}

func TestPostOrderHandlesDeepGraph(t *testing.T) {
	g := NewDirected()
	for i := range 10000 {
		g.AddVertex(Vertex(i))
		if i > 0 {
			g.AddEdge(Vertex(i-1), Vertex(i))
		}
	}
	order := g.PostOrder(0)
	if len(order) != 10000 || order[0] != 9999 {
		t.Fatalf("后序长度=%d 首项=%d", len(order), order[0])
	}
	if got := g.DFS(0); len(got) != 10000 || got[len(got)-1] != 9999 {
		t.Fatalf("深度优先长度=%d", len(got))
	}
}
