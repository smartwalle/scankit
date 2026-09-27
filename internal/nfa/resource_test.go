package nfa

import (
	"errors"
	"testing"

	"github.com/smartwalle/scankit/internal/nfagraph"
	"github.com/smartwalle/scankit/internal/parser"
)

func TestResourceLimitsMatchCompiledLayout(t *testing.T) {
	root, err := parser.Parse(`(?:ab|a[0-9])+`)
	if err != nil {
		t.Fatal(err)
	}
	g, err := nfagraph.NewBuilder().Build(root)
	if err != nil {
		t.Fatal(err)
	}
	e, err := CompileEngine(g, EngineLimEx)
	if err != nil {
		t.Fatal(err)
	}
	u := e.Usage()
	if u.States == 0 || u.Edges == 0 || u.Memory == 0 {
		t.Fatalf("布局用量不完整: %+v", u)
	}
	if err := e.CheckLimits(ResourceLimits{States: u.States, Edges: u.Edges, Memory: u.Memory}); err != nil {
		t.Fatal(err)
	}
	if err := e.CheckLimits(ResourceLimits{Memory: u.Memory - 1}); !errors.Is(err, ErrMemoryLimit) {
		t.Fatalf("应拒绝超出内存限制: %v", err)
	}
	if err := e.CheckDefaultLimits(); err != nil {
		t.Fatalf("默认限制不应拒绝有效布局: %v", err)
	}
}

func TestExecutionResourceLimitsRejectExceededBudget(t *testing.T) {
	if err := CheckExecutionLimits(Context{Steps: 3, Results: 2}, ResourceLimits{Steps: 2}); !errors.Is(err, ErrExecutionLimit) {
		t.Fatalf("步骤超限未拒绝: %v", err)
	}
	if err := CheckExecutionLimits(Context{Results: 3}, ResourceLimits{Results: 2}); !errors.Is(err, ErrExecutionLimit) {
		t.Fatalf("结果超限未拒绝: %v", err)
	}
}

func TestCompileEngineWithLimitsChecksEdgesBeforeLayout(t *testing.T) {
	root, err := parser.Parse(`(?:a|b|c)+`)
	if err != nil {
		t.Fatal(err)
	}
	g, err := nfagraph.NewBuilder().Build(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := CompileEngineWithLimits(g, EngineSheng, ResourceLimits{Edges: 1}); !errors.Is(err, ErrEdgeLimit) {
		t.Fatalf("边数限制错误: %v", err)
	}
	if _, err := CompileEngineWithLimits(g, EngineSheng, ResourceLimits{States: 1}); !errors.Is(err, ErrStateLimit) {
		t.Fatalf("状态限制错误: %v", err)
	}
}

func TestCheckExecutionLimitsRejectsInvalidUsage(t *testing.T) {
	if err := CheckExecutionLimits(Context{Steps: -1}, ResourceLimits{}); err == nil {
		t.Fatal("负步骤用量未拒绝")
	}
}
