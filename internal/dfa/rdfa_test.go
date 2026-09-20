package dfa

import (
	"github.com/smartwalle/scankit/internal/nfa"
	"github.com/smartwalle/scankit/internal/nfagraph"
	"github.com/smartwalle/scankit/internal/parser"
	"reflect"
	"testing"
)

func TestReverseProgram(t *testing.T) {
	r, _ := parser.Parse("bc|abc")
	g, _ := nfagraph.NewBuilder().Build(r)
	p, e := CompileReverse(g)
	if e != nil {
		t.Fatal(e)
	}
	data := []byte("xxabc")
	if got, want := p.MatchReverseAt(data, len(data)), []int{2, 3}; !reflect.DeepEqual(got, want) {
		t.Fatalf("reverse boundary mismatch: got %v want %v", got, want)
	}
	if got, want := p.ReverseSpans(data), []nfa.Span{{From: 2, To: 5}, {From: 3, To: 5}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("reverse spans mismatch: got %v want %v", got, want)
	}
}

func TestReverseProgramHonorsReadAndResultLimits(t *testing.T) {
	r, err := parser.Parse("abc")
	if err != nil {
		t.Fatal(err)
	}
	g, err := nfagraph.NewBuilder().Build(r)
	if err != nil {
		t.Fatal(err)
	}
	p, err := CompileReverse(g)
	if err != nil {
		t.Fatal(err)
	}
	if got := p.MatchReverseAtLimit([]byte("zabc"), 4, 2); len(got) != 0 {
		t.Fatalf("short reverse budget matched: %v", got)
	}
	if got, want := p.MatchReverseAtLimit([]byte("zabc"), 4, 3), []int{1}; !reflect.DeepEqual(got, want) {
		t.Fatalf("reverse budget mismatch: got=%v want=%v", got, want)
	}
	if got := p.ReverseSpansLimit([]byte("abcabc"), 1); len(got) != 1 {
		t.Fatalf("reverse result limit=%v", got)
	}
}

func TestReverseEligibleRejectsUnboundedAndAssertionGraphs(t *testing.T) {
	for _, pattern := range []string{`a+`, `^a`} {
		root, err := parser.Parse(pattern)
		if err != nil {
			t.Fatal(err)
		}
		g, err := nfagraph.NewBuilder().Build(root)
		if err != nil {
			t.Fatal(err)
		}
		if ReverseEligible(g) {
			t.Fatalf("%q 应使用正向确认", pattern)
		}
	}
}

func TestReverseProgramCloneHandlesMissingForwardProgram(t *testing.T) {
	clone := (&ReverseProgram{Program: &Program{}}).Clone()
	if clone == nil || clone.forward != nil {
		t.Fatalf("克隆结果异常: %#v", clone)
	}
}

func BenchmarkMatchReverseAt(b *testing.B) {
	r, err := parser.Parse("abcdef")
	if err != nil {
		b.Fatal(err)
	}
	g, err := nfagraph.NewBuilder().Build(r)
	if err != nil {
		b.Fatal(err)
	}
	p, err := CompileReverse(g)
	if err != nil {
		b.Fatal(err)
	}
	data := []byte("prefix-abcdef")
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if starts := p.MatchReverseAt(data, len(data)); len(starts) != 1 {
			b.Fatal(starts)
		}
	}
}
