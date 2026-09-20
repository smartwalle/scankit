package compiler

import (
	"context"
	"testing"
)

func TestCompileContextCheckAndReserve(t *testing.T) {
	c := CompileContext{Limits: CompileLimits{GraphVertices: 2}, Context: context.Background()}
	if err := c.ReserveChecked(Usage{GraphVertices: 2}); err != nil {
		t.Fatal(err)
	}
	if err := c.ReserveChecked(Usage{GraphVertices: 1}); err == nil {
		t.Fatal("应拒绝超限预留")
	}
	c.Context = context.Background()
	if err := c.Check(); err != nil {
		t.Fatal(err)
	}
	if c.Usage.GraphVertices != 2 {
		t.Fatalf("资源回滚失败=%+v", c.Usage)
	}
}
