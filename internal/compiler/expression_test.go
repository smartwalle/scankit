package compiler

import (
	"testing"

	"github.com/smartwalle/scankit/internal/nfagraph"
	"github.com/smartwalle/scankit/internal/parser"
)

func TestExpressionInfo(t *testing.T) {
	r, _ := parser.Parse("ab*")
	g, _ := nfagraph.NewBuilder().Build(r)
	i := DeriveExpressionInfo(1, 0, 0, r, g)
	if err := i.Validate(); err != nil {
		t.Fatal(err)
	}
	if i.MinLength != 1 || i.CanMatchEmpty {
		t.Fatal(i)
	}
}

func TestExpressionInfoCaptureMetadata(t *testing.T) {
	r, err := parser.Parse(`((a)|b)(?(1)c|d)\1`)
	if err != nil {
		t.Fatal(err)
	}
	g, err := nfagraph.NewBuilder().Build(r)
	if err != nil {
		t.Fatal(err)
	}
	info := DeriveExpressionInfo(1, 0, 0, r, g)
	if info.Captures != 2 || !info.Backreference || !info.Conditional || !info.RequiresCaptureRuntime() || !info.RequiresStatefulRuntime() {
		t.Fatalf("unexpected capture metadata: %#v", info)
	}
	if err := info.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestExpressionInfoRejectsInconsistentStatefulMetadata(t *testing.T) {
	root, err := parser.Parse(`(?=a)b`)
	if err != nil {
		t.Fatal(err)
	}
	graph, err := nfagraph.NewBuilder().Build(root)
	if err != nil {
		t.Fatal(err)
	}
	info := DeriveExpressionInfo(1, 0, 0, root, graph)
	info.Stateful = false
	if err := info.Validate(); err == nil {
		t.Fatal("inconsistent stateful metadata accepted")
	}
}

func TestExpressionInfoUsesByteWidthsForUTF8AndAssertions(t *testing.T) {
	root, err := parser.Parse(`(?=abc).\p{L}`)
	if err != nil {
		t.Fatal(err)
	}
	graph, err := nfagraph.NewBuilder().Build(root)
	if err != nil {
		t.Fatal(err)
	}
	info := DeriveExpressionInfo(1, 1<<5|1<<6, 0, root, graph)
	if info.MinLength != 2 || info.MaxLength != 8 {
		t.Fatalf("unexpected utf8 width: %d..%d", info.MinLength, info.MaxLength)
	}
}

func TestExpressionInfoDerivesEODProperties(t *testing.T) {
	cases := []struct {
		pattern        string
		atEOD, onlyEOD bool
	}{
		{pattern: `a$`, atEOD: true, onlyEOD: true},
		{pattern: `a\b`, atEOD: true, onlyEOD: false},
		{pattern: `a|b$`, atEOD: true, onlyEOD: false},
		{pattern: `a$b`, atEOD: true, onlyEOD: false},
		{pattern: `^a`, atEOD: false, onlyEOD: false},
	}
	for _, tc := range cases {
		t.Run(tc.pattern, func(t *testing.T) {
			root, err := parser.Parse(tc.pattern)
			if err != nil {
				t.Fatal(err)
			}
			info := DeriveExpressionInfo(1, 0, 0, root, nil)
			if info.MatchesAtEOD != tc.atEOD || info.MatchesOnlyAtEOD != tc.onlyEOD {
				t.Fatalf("EOD metadata=%v/%v, want=%v/%v", info.MatchesAtEOD, info.MatchesOnlyAtEOD, tc.atEOD, tc.onlyEOD)
			}
		})
	}
}

func TestExpressionInfoRejectsInconsistentEODMetadata(t *testing.T) {
	root, err := parser.Parse(`a`)
	if err != nil {
		t.Fatal(err)
	}
	info := DeriveExpressionInfo(1, 0, 0, root, nil)
	info.MatchesOnlyAtEOD = true
	if err := info.Validate(); err == nil {
		t.Fatal("inconsistent EOD metadata accepted")
	}
}

func TestExpressionInfoCompileAttributesSnapshot(t *testing.T) {
	root, err := parser.Parse("abc")
	if err != nil {
		t.Fatal(err)
	}
	info := DeriveExpressionInfo(7, 1<<3|1<<4|1<<10, 1|2|4|8,
		root, nil)
	info.ApplyCompileAttributes(1<<3|1<<4|1<<10, 1|2|4|8, 3, 9, 4, 2, 0)
	if !info.Highlander || !info.AllowVacuous || !info.Quiet ||
		info.MinOffset != 3 || info.MaxOffset != 9 || info.ExtMinLength != 4 || info.EditDistance != 2 || info.HammingDistance != 0 {
		t.Fatalf("unexpected compile attributes: %#v", info)
	}
}
