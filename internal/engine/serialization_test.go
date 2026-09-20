package engine

import (
	"github.com/smartwalle/scankit/internal/dfa"
	"github.com/smartwalle/scankit/internal/nfagraph"
	"github.com/smartwalle/scankit/internal/parser"
	"testing"
)

func TestProgramValidation(t *testing.T) {
	if err := (&Program{Kind: 0}).Validate(); err == nil {
		t.Fatal()
	}
	if _, err := (&Program{Kind: KindNFA}).Dump(); err == nil {
		t.Fatal()
	}
}
func TestProgramRoundTrip(t *testing.T) {
	r, _ := parser.Parse("abc")
	g, _ := nfagraph.NewBuilder().Build(r)
	p, e := Build(g, KindNFA)
	if e != nil {
		t.Fatal(e)
	}
	raw, e := p.Dump()
	if e != nil {
		t.Fatal(e)
	}
	q, e := Load(raw)
	if e != nil || len(q.MatchAt([]byte("abc"), 0)) == 0 {
		t.Fatal(e)
	}
}

func TestDFAMinimizedTableRoundTrip(t *testing.T) {
	r, err := parser.Parse("a|b")
	if err != nil {
		t.Fatal(err)
	}
	g, err := nfagraph.NewBuilder().Build(r)
	if err != nil {
		t.Fatal(err)
	}
	minimized, err := dfa.Minimize(g)
	if err != nil {
		t.Fatal(err)
	}
	p := &Program{Kind: KindDFA, DFA: minimized}
	raw, err := p.Dump()
	if err != nil {
		t.Fatal(err)
	}
	q, err := Load(raw)
	if err != nil || q.DFA == nil || q.DFA.StateCount() != minimized.StateCount() || !q.AcceptsAt([]byte("b"), 0, 1) {
		t.Fatalf("DFA round trip: %#v %v", q, err)
	}
}
func FuzzProgramDump(f *testing.F) {
	f.Add(uint8(1))
	f.Fuzz(func(t *testing.T, k uint8) { p := &Program{Kind: Kind(k)}; _, _ = p.Dump() })
}
