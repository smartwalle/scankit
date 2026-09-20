package engine

import (
	"github.com/smartwalle/scankit/internal/dfa"
	"github.com/smartwalle/scankit/internal/nfagraph"
	"github.com/smartwalle/scankit/internal/parser"
	"testing"
)

func TestCompileAuto(t *testing.T) {
	r, _ := parser.Parse("(?:ab|cd)+")
	g, _ := nfagraph.NewBuilder().Build(r)
	p, e := CompileAuto(g)
	if e != nil || p == nil {
		t.Fatal(e)
	}
}

func TestProgramStatsAndStartBytes(t *testing.T) {
	r, err := parser.Parse("ab")
	if err != nil {
		t.Fatal(err)
	}
	g, err := nfagraph.NewBuilder().Build(r)
	if err != nil {
		t.Fatal(err)
	}
	p, err := Build(g, KindDFA)
	if err != nil {
		t.Fatal(err)
	}
	s := p.Stats()
	if s.Kind != KindDFA || s.DFAStates == 0 || s.GraphVertices == 0 {
		t.Fatalf("统计不完整: %#v", s)
	}
	if got := p.StartBytes(); len(got) != 1 || got[0] != 'a' {
		t.Fatalf("起始字节=%q", got)
	}
}

func TestBuildWithDFAOptions(t *testing.T) {
	r, _ := parser.Parse("a")
	g, _ := nfagraph.NewBuilder().Build(r)
	p, err := BuildWithDFAOptions(g, KindDFA, dfa.CompileOptions{Dense: true, DeadState: true})
	if err != nil || p.BackendName() != "dfa" || p.StateCount() < 2 {
		t.Fatalf("构建结果=%v,%v", p, err)
	}
}

func TestEquivalentResultsAcrossBackends(t *testing.T) {
	r, _ := parser.Parse("a|b")
	g, _ := nfagraph.NewBuilder().Build(r)
	nfaProgram, _ := Build(g, KindNFA)
	dfaProgram, _ := Build(g, KindDFA)
	if !nfaProgram.EquivalentResults(dfaProgram, []byte("zab")) {
		t.Fatal("跨后端结果不一致")
	}
}

func TestSelectKindForFeaturesKeepsUnsafeGraphsOutOfDFA(t *testing.T) {
	r, err := parser.Parse("(?:ab|cd)+")
	if err != nil {
		t.Fatal(err)
	}
	g, err := nfagraph.NewBuilder().Build(r)
	if err != nil {
		t.Fatal(err)
	}
	if got := SelectKindForFeatures(g, SelectionFeatures{}); got != KindDFA {
		t.Fatalf("plain byte graph=%v", got)
	}
	for _, features := range []SelectionFeatures{
		{Stateful: true}, {Assertions: true}, {UTF8: true}, {UCP: true},
	} {
		if got := SelectKindForFeatures(g, features); got != KindNFA {
			t.Fatalf("unsafe graph features=%#v kind=%v", features, got)
		}
	}
}

func TestCompileAutoWithFeaturesFallsBackWithoutChangingMatches(t *testing.T) {
	r, err := parser.Parse("(?:a|b){8}")
	if err != nil {
		t.Fatal(err)
	}
	g, err := nfagraph.NewBuilder().Build(r)
	if err != nil {
		t.Fatal(err)
	}
	p, err := CompileAutoWithFeatures(g, SelectionFeatures{}, 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	if p.Kind != KindNFA {
		t.Fatalf("fallback kind=%v", p.Kind)
	}
	if !p.AcceptsAt([]byte("aaaaaaaa"), 0, 8) {
		t.Fatal("fallback lost accepted match")
	}
}

func TestExplainSelectionIncludesLoopAndAcceptMetadata(t *testing.T) {
	root, err := parser.Parse("ab*")
	if err != nil {
		t.Fatal(err)
	}
	g, err := nfagraph.NewBuilder().Build(root)
	if err != nil {
		t.Fatal(err)
	}
	info := ExplainSelection(g)
	if info.Accepts == 0 || info.SCC == 0 || info.Vertices == 0 {
		t.Fatalf("选择诊断元数据不完整: %#v", info)
	}
}
func FuzzSelectKind(f *testing.F) {
	f.Add("a|b")
	f.Fuzz(func(t *testing.T, p string) {
		r, e := parser.Parse(p)
		if e != nil {
			return
		}
		g, e := nfagraph.NewBuilder().Build(r)
		if e != nil {
			return
		}
		if k := SelectKind(g); k != KindNFA && k != KindDFA {
			t.Fatal(k)
		}
	})
}
