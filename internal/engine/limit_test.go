package engine

import (
	"errors"
	"testing"

	"github.com/smartwalle/scankit/internal/nfa"
	"github.com/smartwalle/scankit/internal/nfagraph"
	"github.com/smartwalle/scankit/internal/parser"
)

func TestSelectKindWithLimit(t *testing.T) {
	r, _ := parser.Parse("a+")
	g, _ := nfagraph.NewBuilder().Build(r)
	if got := SelectKindWithLimit(g, 1); got != KindNFA {
		t.Fatalf("状态预算不足时应选择 NFA 回退: got %v", got)
	}
}

func TestCompileAutoFallsBackWhenDFAExceedsBudget(t *testing.T) {
	r, err := parser.Parse("(?:a|b){40}")
	if err != nil {
		t.Fatal(err)
	}
	g, err := nfagraph.NewBuilder().Build(r)
	if err != nil {
		t.Fatal(err)
	}
	p, err := CompileAutoWithLimits(g, 1)
	if err != nil {
		t.Fatal(err)
	}
	if SelectKind(g) != KindDFA {
		t.Fatal("test graph did not select DFA")
	}
	if p.Kind != KindNFA || !p.AcceptsAt([]byte("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"), 0, 40) {
		t.Fatalf("unexpected fallback program: %#v", p)
	}
}

func TestSelectKindWithLimitFallsBackBeforeDFAConstruction(t *testing.T) {
	r, err := parser.Parse("(?:a|b){40}")
	if err != nil {
		t.Fatal(err)
	}
	g, err := nfagraph.NewBuilder().Build(r)
	if err != nil {
		t.Fatal(err)
	}
	if got := SelectKindWithLimit(g, 1); got != KindNFA {
		t.Fatalf("状态预算不足时应选择 NFA 回退，得到=%v", got)
	}
}

func TestBuildWithBudgetsAppliesMemoryLimitToNFA(t *testing.T) {
	r, _ := parser.Parse("abcdefgh")
	g, _ := nfagraph.NewBuilder().Build(r)
	if _, err := BuildWithBudgets(g, KindNFA, 0, 1); !errors.Is(err, nfa.ErrMemoryLimit) {
		t.Fatalf("NFA 内存预算未生效: %v", err)
	}
}
