package prefilter

import (
	"testing"

	"github.com/smartwalle/scankit/internal/nfagraph"
	"github.com/smartwalle/scankit/internal/parser"
)

func TestFromGraphExtractsSafePrefix(t *testing.T) {
	root, err := parser.Parse("abc[0-9]")
	if err != nil {
		t.Fatal(err)
	}
	graph, err := nfagraph.NewBuilder().Build(root)
	if err != nil {
		t.Fatal(err)
	}
	filter, ok := FromGraph(graph)
	if !ok || string(filter.Literal) != "abc" || !filter.Candidate([]byte("xxabc7")) {
		t.Fatalf("图前缀过滤器=%q,%v", filter.Literal, ok)
	}
}

func TestFromGraphAlternationCommonPrefix(t *testing.T) {
	root, err := parser.Parse("ab(?:c|d)")
	if err != nil {
		t.Fatal(err)
	}
	graph, err := nfagraph.NewBuilder().Build(root)
	if err != nil {
		t.Fatal(err)
	}
	filter, ok := FromGraph(graph)
	if !ok || string(filter.Literal) != "ab" {
		t.Fatalf("公共前缀=%q, ok=%v", filter.Literal, ok)
	}
}

func TestFromGraphRejectsOpaqueLookaroundPrefix(t *testing.T) {
	root, err := parser.Parse(`(?=abc)abc`)
	if err != nil {
		t.Fatal(err)
	}
	graph, err := nfagraph.NewBuilder().Build(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := FromGraph(graph); ok {
		t.Fatal("不透明查找断言不应生成候选过滤器")
	}
}

func TestFilterFind(t *testing.T) {
	f := New([]byte("ab"))
	if got := f.Find([]byte("zab ab")); len(got) != 2 || got[0] != 1 {
		t.Fatal(got)
	}
}

func TestFilterMatchAtAndAggregate(t *testing.T) {
	a, b := New([]byte("ab")), New([]byte("cd"))
	if !a.MatchAt([]byte("zab"), 1) || !CandidateAny([]Filter{a, b}, []byte("xxab")) || CandidateAll([]Filter{a, b}, []byte("xxab")) {
		t.Fatal("过滤聚合语义错误")
	}
}

func TestFilterFindLimitRejectsNegative(t *testing.T) {
	if New([]byte("a")).FindLimit([]byte("a"), -1) != nil {
		t.Fatal("负数量限制应返回空")
	}
}

func TestFilterFindLimitStopsAtRequestedCount(t *testing.T) {
	got := New([]byte("aa")).FindLimit([]byte("aaaa"), 2)
	if len(got) != 2 || got[0] != 0 || got[1] != 1 {
		t.Fatalf("限量候选=%v", got)
	}
}

func TestFilterCaseFoldAndBounds(t *testing.T) {
	f := New([]byte("Ab"))
	if !f.CandidateFold([]byte("zzab")) || len(f.FindFold([]byte("aBAb"))) != 2 {
		t.Fatal("大小写候选错误")
	}
	if got := f.FindFrom([]byte("xxab"), 4); got != nil {
		t.Fatal(got)
	}
	if got := New(nil).FindFrom([]byte("abc"), 2); len(got) != 1 || got[0] != 2 {
		t.Fatal(got)
	}
}
