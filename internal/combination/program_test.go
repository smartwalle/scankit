package combination

import (
	"testing"

	"github.com/smartwalle/scankit/internal/parser"
)

func TestAccumulatorCompilationAndShortCircuit(t *testing.T) {
	r, err := parser.ParseCombination("1&!2")
	if err != nil {
		t.Fatal(err)
	}
	p, err := Compile(r)
	if err != nil {
		t.Fatal(err)
	}
	a := p.NewAccumulator()
	if a.Add(2) {
		t.Fatal("否定条件不应满足")
	}
	if a.Add(1) {
		t.Fatal("仍不应满足")
	}
	if err := a.Restore([]uint32{1}); err != nil || !a.Program.Evaluate(a.Hits) {
		t.Fatalf("恢复失败=%v", err)
	}
	if _, ids := EvaluateTrace(r, HitSet{1: false, 2: true}); len(ids) != 1 || ids[0] != 1 {
		t.Fatalf("短路依赖=%v", ids)
	}
}

func TestAccumulatorRestoreIsAtomic(t *testing.T) {
	r, err := parser.ParseCombination("1|2")
	if err != nil {
		t.Fatal(err)
	}
	p, err := Compile(r)
	if err != nil {
		t.Fatal(err)
	}
	a := p.NewAccumulator()
	a.Add(1)
	if err := a.Restore([]uint32{2, 99}); err == nil {
		t.Fatal("非法恢复未拒绝")
	}
	if !a.Has(1) || a.Has(2) {
		t.Fatalf("失败恢复破坏原状态: %#v", a.Snapshot())
	}
}
