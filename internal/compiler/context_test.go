package compiler

import (
	"strings"
	"testing"
)

func TestCompileLimits(t *testing.T) {
	limits := CompileLimits{GraphVertices: 2}
	if err := limits.Check(Usage{GraphVertices: 2}); err != nil {
		t.Fatal(err)
	}
	err := limits.Check(Usage{GraphVertices: 3})
	if err == nil || !strings.Contains(err.Error(), "graph vertices") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCompileErrorDoesNotContainPattern(t *testing.T) {
	err := (&CompileError{Kind: ErrorSyntax, Expression: 1, Position: 4, Message: "unexpected token"}).Error()
	if !strings.Contains(err, "unexpected token") || strings.Contains(err, "pattern") {
		t.Fatalf("unexpected compile error: %q", err)
	}
}

func TestCheckStageIncludesStageAndLimitKind(t *testing.T) {
	c := CompileContext{Limits: CompileLimits{DFAStates: 2}}
	if err := c.CheckStage("dfa", Usage{DFAStates: 3}); err == nil || !strings.Contains(err.Error(), "dfa") {
		t.Fatalf("阶段限额错误=%v", err)
	}
}

func FuzzCompileLimits(f *testing.F) {
	f.Add(uint64(1), uint64(2))
	f.Fuzz(func(t *testing.T, maxValue, used uint64) {
		err := CompileLimits{GraphVertices: maxValue}.Check(Usage{GraphVertices: used})
		if maxValue != 0 && used > maxValue && err == nil {
			t.Fatal("limit not enforced")
		}
		if (maxValue == 0 || used <= maxValue) && err != nil {
			t.Fatal(err)
		}
	})
}
