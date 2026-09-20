package graph

import (
	"reflect"
	"slices"
	"testing"
)

func TestDirectedTraversalReachabilityAndSCC(t *testing.T) {
	g := NewDirected()
	g.AddEdge(0, 1)
	g.AddEdge(0, 2)
	g.AddEdge(1, 2)
	g.AddEdge(2, 1)
	g.AddEdge(2, 3)
	if got := g.DFS(0); !slices.Equal(got, []Vertex{0, 1, 2, 3}) {
		t.Fatalf("DFS = %v", got)
	}
	if got := g.BFS(0); !slices.Equal(got, []Vertex{0, 1, 2, 3}) {
		t.Fatalf("BFS = %v", got)
	}
	if !g.Reachable(0, 3) || g.Reachable(3, 0) {
		t.Fatal("unexpected reachability")
	}
	if got := g.SCC(); !reflect.DeepEqual(got, [][]Vertex{{0}, {1, 2}, {3}}) {
		t.Fatalf("SCC = %v", got)
	}
}

func TestNormalizeHasEdgeAndCyclePath(t *testing.T) {
	g := NewDirected()
	g.AddEdge(1, 2)
	g.AddEdge(1, 2)
	g.AddEdge(2, 1)
	g.Normalize()
	if !g.HasEdge(1, 2) || g.EdgeCount() != 2 {
		t.Fatalf("边规范化失败")
	}
	cycle := g.CyclePath()
	if len(cycle) < 3 || cycle[0] != cycle[len(cycle)-1] {
		t.Fatalf("环路径=%v", cycle)
	}
}

func TestCompressPreservesEdges(t *testing.T) {
	g := NewDirected()
	g.AddEdge(10, 20)
	g.AddEdge(20, 40)
	c, mapping := g.Compress()
	if c == nil || len(mapping) != 3 || !c.HasEdge(mapping[10], mapping[20]) || !c.HasEdge(mapping[20], mapping[40]) {
		t.Fatalf("压缩图不一致: %#v %#v", c, mapping)
	}
}

func TestUndirectedGraph(t *testing.T) {
	g := NewUndirected()
	g.AddEdge(2, 1)
	g.AddVertex(3)
	if got := g.Vertices(); !slices.Equal(got, []Vertex{1, 2, 3}) {
		t.Fatalf("vertices = %v", got)
	}
	if got := g.Neighbors(1); !slices.Equal(got, []Vertex{2}) {
		t.Fatalf("neighbors = %v", got)
	}
}
