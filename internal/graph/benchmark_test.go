package graph

import "testing"

func BenchmarkSCC(b *testing.B) {
	g := NewDirected()
	for i := range 100 {
		g.AddEdge(Vertex(i), Vertex((i+1)%100))
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		_ = g.SCC()
	}
}
