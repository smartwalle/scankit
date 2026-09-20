package nfagraph

import (
	"encoding/json"
	"github.com/smartwalle/scankit/internal/parser"
	"testing"
)

func TestBuildGraph(t *testing.T) {
	root, err := parser.Parse("ab|c")
	if err != nil {
		t.Fatal(err)
	}
	g, err := NewBuilder().Build(root)
	if err != nil || len(g.Nodes) == 0 {
		t.Fatalf("graph build: %v", err)
	}
	if err := g.Validate(); err != nil {
		t.Fatal(err)
	}
	a := Analyze(g)
	if a.Depth[g.Start] != 0 || len(a.SCC) == 0 {
		t.Fatalf("invalid analysis: %#v", a)
	}
}

func TestRepeatAndAlternationHaveReachableAccept(t *testing.T) {
	for _, pattern := range []string{"a*", "a+", "a{2,4}", "a|bc", ""} {
		root, err := parser.Parse(pattern)
		if err != nil {
			t.Fatal(err)
		}
		g, err := NewBuilder().Build(root)
		if err != nil {
			t.Fatalf("%q: %v", pattern, err)
		}
		if err := g.Validate(); err != nil {
			t.Fatalf("%q: %v", pattern, err)
		}
	}
}

func TestLookaroundKeepsNonConsumableBoundary(t *testing.T) {
	root, err := parser.Parse(`(?=a)b`)
	if err != nil {
		t.Fatal(err)
	}
	g, err := NewBuilder().Build(root)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, node := range g.Nodes {
		if node != nil && node.Kind == KindAssertion {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("查找断言被错误下沉为普通文字")
	}
}

func TestValidateRejectsDuplicateEdgeAndTracksSelfLoop(t *testing.T) {
	g := New()
	g.AddNode(Node{ID: 1, Kind: KindLiteral, Literal: []byte{'a'}})
	g.AddNode(Node{ID: 2, Kind: KindAccept})
	g.AddEdge(g.Start, 1)
	g.AddEdge(1, 1)
	g.AddEdge(1, 2)
	if err := g.Validate(); err != nil {
		t.Fatal(err)
	}
	a := Analyze(g)
	if !a.InCycle(1) || a.CycleWidth(1) != 1 || a.RegionCount() != 1 {
		t.Fatalf("self loop analysis failed: %#v", a)
	}
	g.AddEdge(1, 2)
	if err := g.Validate(); err == nil {
		t.Fatal("duplicate edge was accepted")
	}
}

func TestValidateRejectsInconsistentNodePayload(t *testing.T) {
	g := New()
	g.AddNode(Node{ID: 1, Kind: KindLiteral, Literal: []byte("a"), Class: parser.Class{Ranges: []parser.Range{{Lo: 'a', Hi: 'a'}}}})
	g.AddNode(Node{ID: 2, Kind: KindAccept})
	g.AddEdge(g.Start, 1)
	g.AddEdge(1, 2)
	if err := g.Validate(); err == nil {
		t.Fatal("文字节点混入字符类负载后仍通过校验")
	}
}

func FuzzBuildValidate(f *testing.F) {
	for _, seed := range []string{"a", "a|b", "a*", "(?:ab){1,3}", "[a-z]"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, pattern string) {
		root, err := parser.Parse(pattern)
		if err != nil {
			return
		}
		g, err := NewBuilder().Build(root)
		if err != nil {
			t.Fatal(err)
		}
		if err := g.Validate(); err != nil {
			t.Fatal(err)
		}
	})
}

func BenchmarkBuildGraph(b *testing.B) {
	root, err := parser.Parse(`(?:ab|cd){1,4}[0-9]+`)
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := NewBuilder().Build(root); err != nil {
			b.Fatal(err)
		}
	}
}

func TestDumpLoad(t *testing.T) {
	root, _ := parser.Parse("ab|c")
	g, err := NewBuilder().Build(root)
	if err != nil {
		t.Fatal(err)
	}
	data, err := Dump(g)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Nodes) != len(g.Nodes) {
		t.Fatal(len(loaded.Nodes))
	}
}

func TestLoadRejectsDuplicateNodesAndEdges(t *testing.T) {
	root, _ := parser.Parse("a")
	g, err := NewBuilder().Build(root)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := Dump(g)
	if err != nil {
		t.Fatal(err)
	}
	var d map[string]any
	if err := json.Unmarshal(raw, &d); err != nil {
		t.Fatal(err)
	}
	nodes := d["nodes"].([]any)
	d["nodes"] = append(nodes, nodes[0])
	dup, _ := json.Marshal(d)
	if _, err := Load(dup); err == nil {
		t.Fatal("重复节点未拒绝")
	}

	if err := json.Unmarshal(raw, &d); err != nil {
		t.Fatal(err)
	}
	edges := d["edges"].([]any)
	d["edges"] = append(edges, edges[0])
	dup, _ = json.Marshal(d)
	if _, err := Load(dup); err == nil {
		t.Fatal("重复边未拒绝")
	}
}
