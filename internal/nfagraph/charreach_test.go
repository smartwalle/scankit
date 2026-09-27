package nfagraph

import (
	"testing"

	"github.com/smartwalle/scankit/internal/parser"
)

func TestCharReachSetOperationsAndRoundTrip(t *testing.T) {
	a := CharReachFromClass(parser.Class{Ranges: []parser.Range{{Lo: 'a', Hi: 'c'}}})
	b := CharReachFromClass(parser.Class{Ranges: []parser.Range{{Lo: 'c', Hi: 'e'}}})
	intersection := a
	intersection.Intersect(b)
	if intersection.Count() != 1 || !intersection.Contains('c') {
		t.Fatal("交集错误")
	}
	a.Union(b)
	if got := a.ToClass().Ranges; len(got) != 1 || got[0].Lo != 'a' || got[0].Hi != 'e' {
		t.Fatalf("并集未合并: %#v", got)
	}
	a.Difference(b)
	if a.Contains('c') || !a.Contains('b') {
		t.Fatal("差集错误")
	}
	if !CharReachFromClass(parser.Class{Ranges: []parser.Range{{Lo: 'a', Hi: 'a'}}, Negated: true}).Contains(0) {
		t.Fatal("补集错误")
	}
}
