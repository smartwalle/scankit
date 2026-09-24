package dfa

import (
	"testing"

	"github.com/smartwalle/scankit/internal/nfagraph"
	"github.com/smartwalle/scankit/internal/parser"
)

func TestReverseBoundsSeparateReadAndResultLimits(t *testing.T) {
	r, _ := parser.Parse("a+")
	g, _ := nfagraph.NewBuilder().Build(r)
	p, err := CompileReverse(g)
	if err != nil {
		t.Fatal(err)
	}
	if got := p.MatchReverseAtBounds([]byte("aaa"), 3, 3, 1); len(got) != 1 {
		t.Fatalf("结果限额=%v", got)
	}
	if got := p.MatchReverseAtBounds([]byte("aaa"), 3, 1, 0); len(got) != 1 || got[0] != 2 {
		t.Fatalf("读取限额=%v", got)
	}
}
