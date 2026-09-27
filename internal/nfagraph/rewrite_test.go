package nfagraph

import (
	"testing"

	"github.com/smartwalle/scankit/internal/parser"
)

func TestNormalize(t *testing.T) {
	r, _ := parser.Parse("a|b")
	g, _ := NewBuilder().Build(r)
	g.Flow.AddVertex(999)
	g.Nodes[999] = &Node{ID: 999, Kind: KindJoin}
	if err := Normalize(g); err != nil {
		t.Fatal(err)
	}
	if _, ok := g.Nodes[999]; ok {
		t.Fatal("unreachable node retained")
	}
}

func TestNormalizeWithStats(t *testing.T) {
	r, _ := parser.Parse("a")
	g, _ := NewBuilder().Build(r)
	stats, err := NormalizeWithStats(g)
	if err != nil || g.Validate() != nil || stats.RemovedEdges < 0 {
		t.Fatalf("规范化统计=%#v,%v", stats, err)
	}
}

func TestSquashLinearJoins(t *testing.T) {
	g := New()
	g.AddNode(Node{ID: 1, Kind: KindLiteral, Literal: []byte("a")})
	g.AddNode(Node{ID: 2, Kind: KindJoin})
	g.AddNode(Node{ID: 3, Kind: KindAccept})
	g.AddEdge(g.Start, 1)
	g.AddEdge(1, 2)
	g.AddEdge(2, 3)
	if err := g.Validate(); err != nil {
		t.Fatal(err)
	}
	if removed := SquashLinearJoins(g); removed != 1 {
		t.Fatalf("linear joins removed=%d", removed)
	}
	if err := g.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestSquashLinearLiterals(t *testing.T) {
	g := New()
	g.AddNode(Node{ID: 1, Kind: KindLiteral, Literal: []byte("a")})
	g.AddNode(Node{ID: 2, Kind: KindLiteral, Literal: []byte("bc")})
	g.AddNode(Node{ID: 3, Kind: KindAccept})
	g.AddEdge(g.Start, 1)
	g.AddEdge(1, 2)
	g.AddEdge(2, 3)
	if removed := SquashLinearLiterals(g); removed != 1 {
		t.Fatalf("linear literals merged=%d", removed)
	}
	if n := g.Nodes[1]; n == nil || string(n.Literal) != "abc" {
		t.Fatalf("合并文字=%v", n)
	}
	if err := g.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestMergeEquivalentNodes(t *testing.T) {
	root, err := parser.Parse(`(?:ab|ab)c`)
	if err != nil {
		t.Fatal(err)
	}
	g, err := NewBuilder().Build(root)
	if err != nil {
		t.Fatal(err)
	}
	before := g.NodeCount()
	if removed := MergeEquivalentNodes(g); removed == 0 {
		t.Fatal("expected equivalent nodes to be merged")
	}
	if err := g.Validate(); err != nil {
		t.Fatal(err)
	}
	if g.NodeCount() >= before {
		t.Fatalf("rewrite did not reduce graph: before=%d after=%d", before, g.NodeCount())
	}
}

func TestExpandLiteralsPreservesGraphValidity(t *testing.T) {
	root, err := parser.Parse(`\Qabc\E|\Qde\E`)
	if err != nil {
		t.Fatal(err)
	}
	g, err := NewBuilder().Build(root)
	if err != nil {
		t.Fatal(err)
	}
	expanded := ExpandLiterals(g)
	if err := expanded.Validate(); err != nil {
		t.Fatal(err)
	}
	if expanded.NodeCount() <= g.NodeCount() {
		t.Fatalf("literal expansion did not add byte states: before=%d after=%d", g.NodeCount(), expanded.NodeCount())
	}
}
func TestNormalizeDeduplicates(t *testing.T) {
	r, _ := parser.Parse("a")
	g, _ := NewBuilder().Build(r)
	g.AddEdge(g.Start, 1)
	before := len(g.Flow.Successors(g.Start))
	RemoveRedundantEdges(g)
	if len(g.Flow.Successors(g.Start)) >= before {
		t.Fatal()
	}
}
func FuzzNormalize(f *testing.F) {
	f.Add("a|b")
	f.Fuzz(func(t *testing.T, p string) {
		r, e := parser.Parse(p)
		if e != nil {
			return
		}
		g, e := NewBuilder().Build(r)
		if e != nil {
			t.Fatal(e)
		}
		if e := Normalize(g); e != nil {
			t.Fatal(e)
		}
	})
}
func BenchmarkNormalize(b *testing.B) {
	r, _ := parser.Parse(`(?:ab|cd){1,3}`)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		g, _ := NewBuilder().Build(r)
		_ = Normalize(g)
	}
}
