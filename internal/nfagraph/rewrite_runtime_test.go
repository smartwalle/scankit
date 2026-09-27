package nfagraph_test

import (
	"reflect"
	"testing"

	"github.com/smartwalle/scankit/internal/nfa"
	"github.com/smartwalle/scankit/internal/nfagraph"
	"github.com/smartwalle/scankit/internal/parser"
)

func TestBypassEpsilonJoinsPreservesAlternationLanguage(t *testing.T) {
	root, err := parser.Parse(`(?:a|b)c`)
	if err != nil {
		t.Fatal(err)
	}
	g, err := nfagraph.NewBuilder().Build(root)
	if err != nil {
		t.Fatal(err)
	}
	before, err := nfa.Compile(g.Clone())
	if err != nil {
		t.Fatal(err)
	}
	if removed := nfagraph.BypassEpsilonJoins(g); removed == 0 {
		t.Fatal("expected epsilon join to be bypassed")
	}
	after, err := nfa.Compile(g)
	if err != nil {
		t.Fatal(err)
	}
	for _, input := range [][]byte{[]byte("ac"), []byte("bc"), []byte("cc"), []byte("a")} {
		if got, want := after.Spans(input), before.Spans(input); !reflect.DeepEqual(got, want) {
			t.Fatalf("rewrite changed %q: got %#v want %#v", input, got, want)
		}
	}
}
