package graph

import "testing"

func BenchmarkSCC(b *testing.B) {
	g := NewDirected()
	for i := 0; i < 100; i++ {
		g.AddEdge(Vertex(i), Vertex((i+1)%100))
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = g.SCC()
	}
}
