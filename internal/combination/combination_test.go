package combination

import (
	"github.com/smartwalle/scankit/internal/parser"
	"testing"
)

func TestEvaluate(t *testing.T) {
	r, err := parser.ParseCombination("(1&2)|!3")
	if err != nil {
		t.Fatal(err)
	}
	if !Evaluate(r, HitSet{1: true, 2: true}) {
		t.Fatal()
	}
	if Evaluate(r, HitSet{3: true}) {
		t.Fatal()
	}
}

func TestEvaluateTrace(t *testing.T) {
	node := parser.CombinationOperator{Op: '&', Left: parser.CombinationOperand{ID: 2}, Right: parser.CombinationNot{Child: parser.CombinationOperand{ID: 1}}}
	ok, ids := EvaluateTrace(node, HitSet{2: true})
	if !ok || len(ids) != 2 || ids[0] != 1 || ids[1] != 2 {
		t.Fatalf("轨迹=%v,%v", ok, ids)
	}
}

func TestDependencyCycle(t *testing.T) {
	cycle := DependencyCycle(map[uint32][]uint32{1: {2}, 2: {3}, 3: {1}, 4: {5}})
	if len(cycle) != 4 || cycle[0] != 1 || cycle[len(cycle)-1] != 1 {
		t.Fatalf("cycle=%v", cycle)
	}
	if err := ValidateDependencies(map[uint32][]uint32{1: {2}, 2: {3}}); err != nil {
		t.Fatal(err)
	}
}

func TestAccumulatorRejectsUnknownAndReportsTransition(t *testing.T) {
	root, err := parser.ParseCombination("1&2")
	if err != nil {
		t.Fatal(err)
	}
	p, err := Compile(root)
	if err != nil {
		t.Fatal(err)
	}
	a := p.NewAccumulator()
	if a.Add(9) || a.Has(9) {
		t.Fatal("未知依赖不应进入累计状态")
	}
	if result, changed := a.EvaluateAfterAdd(1); result || !changed {
		t.Fatalf("首次状态=%v,%v", result, changed)
	}
	if result, changed := a.EvaluateAfterAdd(2); !result || !changed {
		t.Fatalf("满足状态=%v,%v", result, changed)
	}
}

func TestAccumulatorEvaluateAfterAddRejectsUnknown(t *testing.T) {
	root, err := parser.ParseCombination("1")
	if err != nil {
		t.Fatal(err)
	}
	p, err := Compile(root)
	if err != nil {
		t.Fatal(err)
	}
	a := p.NewAccumulator()
	if result, changed := a.EvaluateAfterAdd(9); result || changed || a.Has(9) {
		t.Fatalf("未知依赖被接受: result=%v changed=%v hits=%v", result, changed, a.Snapshot())
	}
}

func TestHitSetEqualIgnoresFalseEntries(t *testing.T) {
	if !(HitSet{1: true, 2: false}).Equal(HitSet{1: true}) {
		t.Fatal("无效命中项不应影响集合相等性")
	}
}

func FuzzEvaluate(f *testing.F) {
	f.Add("1|2", uint32(1))
	f.Fuzz(func(t *testing.T, expr string, id uint32) {
		r, err := parser.ParseCombination(expr)
		if err != nil {
			return
		}
		_ = Evaluate(r, HitSet{id: true})
	})
}
