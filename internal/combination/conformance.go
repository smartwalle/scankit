package combination

import (
	"fmt"

	"github.com/smartwalle/scankit/internal/parser"
)

// ConformanceCase 描述组合表达式及其命中集合期望值。
type ConformanceCase struct {
	Name string
	Expr string
	Hits HitSet
	Want bool
}

// RunConformance 验证组合解析、编译和运行结果的一致性。
func RunConformance(cases []ConformanceCase) error {
	for index, item := range cases {
		root, err := parser.ParseCombination(item.Expr)
		if err != nil {
			return fmt.Errorf("case %d (%s): parse: %w", index, item.Name, err)
		}
		program, err := Compile(root)
		if err != nil {
			return fmt.Errorf("case %d (%s): compile: %w", index, item.Name, err)
		}
		if got := program.Evaluate(item.Hits); got != item.Want {
			return fmt.Errorf("case %d (%s): got %v want %v", index, item.Name, got, item.Want)
		}
	}
	return nil
}

// DefaultConformanceCases 返回覆盖布尔运算和否定的固定样例。
func DefaultConformanceCases() []ConformanceCase {
	return []ConformanceCase{
		{Name: "and", Expr: "1&2", Hits: HitSet{1: true, 2: true}, Want: true},
		{Name: "or", Expr: "1|2", Hits: HitSet{2: true}, Want: true},
		{Name: "not", Expr: "!1", Hits: HitSet{}, Want: true},
		{Name: "precedence", Expr: "1|2&3", Hits: HitSet{1: true}, Want: true},
		{Name: "nested", Expr: "!(1|2)&3", Hits: HitSet{3: true}, Want: true},
	}
}
