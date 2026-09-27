package graph

import "testing"

func TestHasVertex(t *testing.T) {
	g := NewDirected()
	g.AddVertex(1)
	if !g.HasVertex(1) || g.HasVertex(2) {
		t.Fatal()
	}
}

func TestTraversalUnknownVertex(t *testing.T) {
	g := NewDirected()
	g.AddVertex(1)
	if got := g.BFS(9); got != nil {
		t.Fatalf("未知顶点不应产生遍历结果: %#v", got)
	}
	if got := g.DFS(9); got != nil {
		t.Fatalf("未知顶点不应产生遍历结果: %#v", got)
	}
}
