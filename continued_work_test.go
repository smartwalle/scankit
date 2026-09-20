package scankit

import (
	"context"
	"testing"

	"github.com/smartwalle/scankit/internal/combination"
	"github.com/smartwalle/scankit/internal/compiler"
	"github.com/smartwalle/scankit/internal/database"
	"github.com/smartwalle/scankit/internal/dfa"
	"github.com/smartwalle/scankit/internal/dispatch"
	"github.com/smartwalle/scankit/internal/engine"
	"github.com/smartwalle/scankit/internal/fuzzy"
	"github.com/smartwalle/scankit/internal/nfagraph"
	"github.com/smartwalle/scankit/internal/parser"
	"github.com/smartwalle/scankit/internal/scratch"
	"github.com/smartwalle/scankit/internal/simd/generic"
	"github.com/smartwalle/scankit/internal/smallblock"
)

func TestContinuedDatabaseDigestLifecycle(t *testing.T) {
	d := database.New([]string{"program"})
	if _, err := d.Marshal(); err != nil {
		t.Fatal(err)
	}
	if d.DigestValue() == "" || !d.VerifyDigest() {
		t.Fatalf("digest was not persisted")
	}
	d.Programs[0] = "changed"
	if d.VerifyDigest() {
		t.Fatalf("tampered database passed verification")
	}
}

func TestContinuedEngineAndNFASerialization(t *testing.T) {
	root, err := parser.Parse("ab")
	if err != nil {
		t.Fatal(err)
	}
	g, err := nfagraph.NewBuilder().Build(root)
	if err != nil {
		t.Fatal(err)
	}
	p, err := engine.Build(g, engine.KindDFA)
	if err != nil {
		t.Fatal(err)
	}
	if !p.AcceptsAt([]byte("ab"), 0, 2) {
		t.Fatal("dfa did not accept expected span")
	}
	raw, err := p.Dump()
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := engine.Load(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !loaded.AcceptsAt([]byte("ab"), 0, 2) {
		t.Fatal("loaded engine lost acceptance")
	}
}

func TestContinuedUtilityContracts(t *testing.T) {
	var s scratch.Scratch
	s.ReserveAll(4, 3, 2, 1)
	if b, st, r, tmp := s.Capacities(); b != 4 || st != 3 || r != 2 || tmp != 1 {
		t.Fatalf("unexpected scratch capacities: %d %d %d %d", b, st, r, tmp)
	}
	a := generic.Broadcast(0xaa)
	b := generic.Broadcast(0xaa)
	if generic.EqualMaskAt(a, b, 2, 3) != generic.MaskRange(2, 3) || !generic.ContainsByte(a, 0xaa) {
		t.Fatal("generic SIMD contract mismatch")
	}
}

func TestContinuedUTF8CaselessLiteral(t *testing.T) {
	s, err := Compile([]Expression{{Id: 1, Pattern: "ÄBC", Flags: FlagUTF8 | FlagCaseless}})
	if err != nil {
		t.Fatal(err)
	}
	matches, err := s.Scan([]byte("xxäbc"))
	if err != nil || len(matches) != 1 || matches[0].From != 2 {
		t.Fatalf("unexpected UTF-8 caseless result: %v %#v", err, matches)
	}
}

func TestContinuedExpressionExtBounds(t *testing.T) {
	ext := &ExpressionExt{Flags: ExtFlagMinOffset | ExtFlagMaxOffset | ExtFlagMinLength, MinOffset: 2, MaxOffset: 5, MinLength: 3}
	if ext.OffsetAllowed(1) || !ext.OffsetAllowed(2) || ext.EndOffsetAllowed(6) || !ext.EndOffsetAllowed(5) || !ext.LengthAllowed(3) || ext.LengthAllowed(2) {
		t.Fatal("extension bounds contract mismatch")
	}
}

func TestContinuedSmallBlockBudgetSerialization(t *testing.T) {
	p := smallblock.NewWithLimit([]byte("abc"), 2)
	if p.Eligible([]byte("abc")) || !p.Eligible([]byte("ab")) {
		t.Fatal("small block budget mismatch")
	}
	raw, err := p.Dump()
	if err != nil {
		t.Fatal(err)
	}
	q, err := smallblock.Load(raw)
	if err != nil || q.Eligible([]byte("abc")) {
		t.Fatalf("budget was not preserved: %v", err)
	}
}

func TestContinuedFuzzyScratchDistance(t *testing.T) {
	buf := make([]int, 5)
	if d, ok := fuzzy.EditDistanceInto([]byte("abcd"), []byte("abce"), 1, buf); !ok || d != 1 {
		t.Fatalf("unexpected scratch distance: %d %v", d, ok)
	}
	if d, ok := fuzzy.EditDistanceInto([]byte("abcd"), []byte("xyz"), 1, buf); !ok || d <= 1 {
		t.Fatalf("bounded cutoff failed: %d %v", d, ok)
	}
}

func TestContinuedCompileContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c := compiler.CompileContext{Context: ctx}
	if c.CheckCancelled() == nil {
		t.Fatal("cancelled context was not observed")
	}
}

func TestContinuedCombinationNormalization(t *testing.T) {
	n := combination.Normalize(parser.CombinationNot{Child: parser.CombinationNot{Child: parser.CombinationOperand{ID: 7}}})
	if _, ok := n.(parser.CombinationOperand); !ok {
		t.Fatalf("double negation was not normalized: %#v", n)
	}
	if !combination.IsContradiction(parser.CombinationOperator{Op: '&', Left: parser.CombinationOperand{ID: 1}, Right: parser.CombinationNot{Child: parser.CombinationOperand{ID: 1}}}) {
		t.Fatal("contradiction was not detected")
	}
}

func TestContinuedReverseAndDenseDFA(t *testing.T) {
	root, _ := parser.Parse("ab")
	g, _ := nfagraph.NewBuilder().Build(root)
	rp, err := dfa.CompileReverse(g)
	if err != nil {
		t.Fatal(err)
	}
	starts := rp.MatchReverse([]byte("xxab"))
	if len(starts) == 0 || starts[0] != 2 {
		t.Fatalf("reverse match mismatch: %#v", starts)
	}
	p, _ := dfa.Compile(g)
	if len(p.DenseTable()) != p.StateCount() || len(p.AcceptStates()) == 0 {
		t.Fatal("dense DFA snapshot incomplete")
	}
}

func TestContinuedMetadataAndDispatch(t *testing.T) {
	s, err := Compile([]Expression{{Id: 1, Pattern: "abc"}})
	if err != nil {
		t.Fatal(err)
	}
	info, ok := s.expressionInfo(1)
	if !ok || info.MinimumWidth() != 3 || info.MaximumWidth() != 3 || !info.HasGraph() {
		t.Fatalf("expression metadata mismatch: %#v", info)
	}
	if backend, ok := dispatch.ParseBackend("amd64"); !ok || backend != dispatch.BackendX86 || !backend.Valid() {
		t.Fatalf("backend parse mismatch: %v %v", backend, ok)
	}
}
