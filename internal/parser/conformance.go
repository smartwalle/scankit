package parser

import "fmt"

// ConformanceCase 描述一个固定解析语义样例及其期望结果。
type ConformanceCase struct {
	Name     string
	Pattern  string
	Valid    bool
	Kind     Kind
	MinWidth int
	MaxWidth int
}

// DefaultConformanceCases 返回覆盖核心语法的固定样例集合。
func DefaultConformanceCases() []ConformanceCase {
	return []ConformanceCase{
		{Name: "literal", Pattern: "a", Valid: true, Kind: KindLiteral, MinWidth: 1, MaxWidth: 1},
		{Name: "sequence", Pattern: "ab", Valid: true, Kind: KindSequence, MinWidth: 2, MaxWidth: 2},
		{Name: "alternation", Pattern: "a|bc", Valid: true, Kind: KindAlternation, MinWidth: 1, MaxWidth: 2},
		{Name: "class", Pattern: "[a-z]", Valid: true, Kind: KindClass, MinWidth: 1, MaxWidth: 1},
		{Name: "repeat", Pattern: "a{2,4}", Valid: true, Kind: KindRepeat, MinWidth: 2, MaxWidth: 4},
		{Name: "assertion", Pattern: "^a$", Valid: true, Kind: KindSequence, MinWidth: 1, MaxWidth: 1},
		{Name: "lookaround", Pattern: "(?<=a)b", Valid: true, Kind: KindSequence, MinWidth: 1, MaxWidth: 1},
		{Name: "unicode", Pattern: `\p{L}`, Valid: true, Kind: KindUnicodeClass, MinWidth: 1, MaxWidth: 4},
		{Name: "backreference", Pattern: `(a)\1`, Valid: true, Kind: KindSequence, MinWidth: -1, MaxWidth: -1},
		{Name: "control-verb", Pattern: `a(*SKIP)b`, Valid: true, Kind: KindSequence, MinWidth: 2, MaxWidth: 2},
		{Name: "bad-range", Pattern: "a{4,2}", Valid: false},
		{Name: "reference", Pattern: `\2`, Valid: true, Kind: KindBackreference, MinWidth: -1, MaxWidth: -1},
		{Name: "negated-class", Pattern: `[^a]`, Valid: true, Kind: KindClass, MinWidth: 1, MaxWidth: 1},
		{Name: "conditional", Pattern: `(a)?(?(1)b|c)`, Valid: true, Kind: KindSequence, MinWidth: -1, MaxWidth: -1},
		{Name: "empty", Pattern: `(?:)`, Valid: true, Kind: KindGroup, MinWidth: 0, MaxWidth: 0},
	}
}

// RunConformance 执行样例集合并返回首个失败样例的诊断。
func RunConformance(cases []ConformanceCase) error {
	for index, item := range cases {
		node, err := Parse(item.Pattern)
		if !item.Valid {
			if err == nil {
				return fmt.Errorf("case %d (%s): invalid pattern accepted", index, item.Name)
			}
			continue
		}
		if err != nil {
			return fmt.Errorf("case %d (%s): %w", index, item.Name, err)
		}
		if NodeKind(node) != item.Kind {
			return fmt.Errorf("case %d (%s): kind=%d want=%d", index, item.Name, NodeKind(node), item.Kind)
		}
		minWidth, maxWidth, ok := WidthRange(node)
		if item.MinWidth >= 0 && (!ok || minWidth != item.MinWidth || maxWidth != item.MaxWidth) {
			return fmt.Errorf("case %d (%s): width=%d..%d", index, item.Name, minWidth, maxWidth)
		}
	}
	return nil
}
