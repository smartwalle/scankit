package engine

import (
	"testing"

	"github.com/smartwalle/scankit/internal/nfagraph"
	"github.com/smartwalle/scankit/internal/parser"
)

func TestCompareBackends(t *testing.T) {
	r, _ := parser.Parse("ab|a")
	g, _ := nfagraph.NewBuilder().Build(r)
	if err := CompareBackends(g, []ConformanceInput{{Name: "positive", Data: []byte("zab")}, {Name: "negative", Data: []byte("zzz")}}); err != nil {
		t.Fatal(err)
	}
}

func TestCompareBackendsCoversRangesAndSplits(t *testing.T) {
	for _, pattern := range []string{`a[0-9]`, `(?:ab|cd)+`, `a*`} {
		r, err := parser.Parse(pattern)
		if err != nil {
			t.Fatal(err)
		}
		g, err := nfagraph.NewBuilder().Build(r)
		if err != nil {
			t.Fatal(err)
		}
		inputs := []ConformanceInput{
			{Name: "空", Data: nil},
			{Name: "正向", Data: []byte("ababa")},
			{Name: "NUL", Data: []byte{'a', 0, '7'}},
		}
		if err := CompareBackends(g, inputs); err != nil {
			t.Fatalf("pattern=%q: %v", pattern, err)
		}
	}
}
