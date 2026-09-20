package parser

import (
	"fmt"
	"math"
	"strings"
	"unicode"
	"unicode/utf8"
)

var unicodePropertyNames = buildUnicodePropertyNames()

func buildUnicodePropertyNames() map[string]struct{} {
	names := make(map[string]struct{}, len(unicode.Categories)+len(unicode.Properties)+len(unicode.Scripts))
	for name := range unicode.Categories {
		names[normalizeUnicodeProperty(name)] = struct{}{}
	}
	for name := range unicode.Properties {
		names[normalizeUnicodeProperty(name)] = struct{}{}
	}
	for name := range unicode.Scripts {
		names[normalizeUnicodeProperty(name)] = struct{}{}
	}
	return names
}

// Validate 检查 AST 中可静态发现的引用和语义错误。
func Validate(root Node) error {
	return validate(root, false)
}

// ValidateMany 校验多个语法树，并返回首个失败项的索引。
func ValidateMany(nodes []Node) (int, error) {
	for i, node := range nodes {
		if err := Validate(node); err != nil {
			return i, err
		}
	}
	return -1, nil
}

// ValidateWithUTF8 在 UTF-8 模式下使用字节宽度规则校验反向断言。
func ValidateWithUTF8(root Node, utf8Mode bool) error {
	return validate(root, utf8Mode)
}

func validate(root Node, utf8Mode bool) error {
	if root == nil {
		return fmt.Errorf("nil AST")
	}
	maxCapture := 0
	captureSeen := map[int]struct{}{}
	duplicateCapture := false
	var collect func(Node)
	collect = func(n Node) {
		switch v := n.(type) {
		case Group:
			if v.Capture > 0 {
				if _, exists := captureSeen[v.Capture]; exists {
					duplicateCapture = true
				}
				captureSeen[v.Capture] = struct{}{}
			}
			if v.Capture > maxCapture {
				maxCapture = v.Capture
			}
			collect(v.Child)
		case Sequence:
			for _, c := range v.Elements {
				collect(c)
			}
		case Alternation:
			for _, c := range v.Options {
				collect(c)
			}
		case Repeat:
			collect(v.Child)
		case Lookaround:
			collect(v.Child)
		case Conditional:
			collect(v.Yes)
			collect(v.No)
		}
	}
	collect(root)
	if duplicateCapture {
		return fmt.Errorf("capture groups must use unique positive indexes")
	}
	if err := validateReferenceOrder(root, map[int]struct{}{}); err != nil {
		return err
	}
	var walk func(Node) error
	walk = func(n Node) error {
		if n == nil {
			return fmt.Errorf("nil AST child")
		}
		switch v := n.(type) {
		case Group:
			if v.Capture < 0 {
				return fmt.Errorf("invalid capture index %d", v.Capture)
			}
			if v.Capture > maxCapture {
				maxCapture = v.Capture
			}
			return walk(v.Child)
		case Sequence:
			for _, child := range v.Elements {
				if err := walk(child); err != nil {
					return err
				}
			}
		case Alternation:
			if len(v.Options) == 0 {
				return fmt.Errorf("empty alternation")
			}
			for _, child := range v.Options {
				if err := walk(child); err != nil {
					return err
				}
			}
		case Repeat:
			if v.Min < 0 || v.Min > 1<<20 || (v.Max >= 0 && (v.Max < v.Min || v.Max > 1<<20)) {
				return fmt.Errorf("invalid repeat bounds")
			}
			return walk(v.Child)
		case Lookaround:
			if v.Kind == Lookbehind || v.Kind == NegativeLookbehind {
				min, max := fixedWidth(v.Child)
				if utf8Mode {
					_, _, fixed := FixedWidthUTF8(v.Child)
					if !fixed {
						return fmt.Errorf("lookbehind must have fixed UTF-8 width")
					}
					min, max, _ = FixedWidthUTF8(v.Child)
				}
				if min < 0 || min != max {
					return fmt.Errorf("lookbehind must have fixed length")
				}
			}
			return walk(v.Child)
		case Backreference:
			if v.Index <= 0 || v.Index > maxCapture {
				return fmt.Errorf("invalid backreference %d", v.Index)
			}
		case Class:
			if len(v.Ranges) == 0 {
				return fmt.Errorf("empty character class")
			}
			for _, r := range v.Ranges {
				if r.Hi < r.Lo {
					return fmt.Errorf("invalid character class range")
				}
			}
		case UnicodeClass:
			if !SupportedUnicodeProperty(v.Name) {
				return fmt.Errorf("unsupported unicode property %q", v.Name)
			}
		case ControlVerb:
			switch v.Name {
			case "FAIL", "F", "SKIP", "PRUNE", "COMMIT", "ACCEPT":
			default:
				return fmt.Errorf("unsupported control verb %q", v.Name)
			}
		case Conditional:
			if v.Index <= 0 || v.Index > maxCapture {
				return fmt.Errorf("invalid conditional reference %d", v.Index)
			}
			if err := walk(v.Yes); err != nil {
				return err
			}
			if err := walk(v.No); err != nil {
				return err
			}
		case CombinationOperand:
			if v.ID == 0 {
				return fmt.Errorf("combination id must be positive")
			}
		case Combination:
			return walk(v.Expr)
		case CombinationOperator:
			if v.Op != '&' && v.Op != '|' {
				return fmt.Errorf("unsupported combination operator %q", v.Op)
			}
			if err := walk(v.Left); err != nil {
				return err
			}
			return walk(v.Right)
		case CombinationNot:
			return walk(v.Child)
		}
		return nil
	}
	return walk(root)
}

// validateReferenceOrder 拒绝在对应捕获组出现前使用的数字引用。
func validateReferenceOrder(root Node, defined map[int]struct{}) error {
	if root == nil {
		return fmt.Errorf("nil AST child")
	}
	clone := func(in map[int]struct{}) map[int]struct{} {
		out := make(map[int]struct{}, len(in))
		for index := range in {
			out[index] = struct{}{}
		}
		return out
	}
	var visit func(Node, map[int]struct{}) (map[int]struct{}, error)
	visit = func(node Node, current map[int]struct{}) (map[int]struct{}, error) {
		switch value := node.(type) {
		case Group:
			next := clone(current)
			if value.Capture > 0 {
				next[value.Capture] = struct{}{}
			}
			return visit(value.Child, next)
		case Sequence:
			next := clone(current)
			for _, child := range value.Elements {
				var err error
				next, err = visit(child, next)
				if err != nil {
					return nil, err
				}
			}
			return next, nil
		case Alternation:
			next := clone(current)
			for _, option := range value.Options {
				branch, err := visit(option, clone(current))
				if err != nil {
					return nil, err
				}
				for index := range branch {
					next[index] = struct{}{}
				}
			}
			return next, nil
		case Repeat:
			return visit(value.Child, clone(current))
		case Lookaround:
			return visit(value.Child, clone(current))
		case Conditional:
			if _, ok := current[value.Index]; !ok {
				return nil, fmt.Errorf("conditional reference %d appears before capture", value.Index)
			}
			yes, err := visit(value.Yes, clone(current))
			if err != nil {
				return nil, err
			}
			no, err := visit(value.No, clone(current))
			if err != nil {
				return nil, err
			}
			for index := range no {
				yes[index] = struct{}{}
			}
			return yes, nil
		case Backreference:
			if _, ok := current[value.Index]; !ok {
				return nil, fmt.Errorf("backreference %d appears before capture", value.Index)
			}
		}
		return clone(current), nil
	}
	_, err := visit(root, clone(defined))
	return err
}

func SupportedUnicodeProperty(name string) bool {
	name = normalizeUnicodeProperty(name)
	for _, prefix := range []string{"script=", "sc=", "script:", "generalcategory=", "gc="} {
		if strings.HasPrefix(name, prefix) {
			name = strings.TrimPrefix(name, prefix)
			break
		}
	}
	if len(name) > 2 && (strings.HasPrefix(name, "is") || strings.HasPrefix(name, "in")) {
		name = name[2:]
	}
	switch name {
	case "l", "letter", "alpha", "n", "number", "nd", "z", "space", "whitespace", "lu", "uppercaseletter", "ll", "lowercaseletter", "lt", "lm", "lo", "m", "mark", "p", "punct", "s", "symbol", "cc", "control", "ascii", "any", "assigned", "unassigned":
		return true
	}
	_, ok := unicodePropertyNames[name]
	return ok
}

func normalizeUnicodeProperty(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	name = strings.ReplaceAll(name, "_", "")
	name = strings.ReplaceAll(name, "-", "")
	name = strings.ReplaceAll(name, " ", "")
	return name
}

func fixedWidth(n Node) (int, int) {
	switch v := n.(type) {
	case Literal:
		return len(v.Value), len(v.Value)
	case Any, Class:
		return 1, 1
	case UnicodeClass:
		return 1, 1
	case Assertion, ControlVerb:
		return 0, 0
	case Group:
		return fixedWidth(v.Child)
	case Sequence:
		min, max := 0, 0
		for _, c := range v.Elements {
			a, b := fixedWidth(c)
			if a < 0 {
				return -1, -1
			}
			min = safeAddInt(min, a)
			max = safeAddInt(max, b)
		}
		return min, max
	case Alternation:
		if len(v.Options) == 0 {
			return 0, 0
		}
		a, b := fixedWidth(v.Options[0])
		for _, c := range v.Options[1:] {
			x, y := fixedWidth(c)
			if x != a || y != b {
				return -1, -1
			}
		}
		return a, b
	case Repeat:
		a, b := fixedWidth(v.Child)
		if a < 0 || v.Max < 0 || v.Min != v.Max {
			return -1, -1
		}
		return safeMulInt(a, v.Min), safeMulInt(b, v.Max)
	default:
		return -1, -1
	}
}

func safeAddInt(a, b int) int {
	if a < 0 || b < 0 {
		return -1
	}
	if a > math.MaxInt-b {
		return -1
	}
	return a + b
}

func safeMulInt(a, b int) int {
	if a < 0 || b < 0 || (a != 0 && b > math.MaxInt/a) {
		return -1
	}
	return a * b
}

func FixedWidth(n Node) (min, max int, ok bool) {
	min, max = fixedWidth(n)
	return min, max, min >= 0 && max >= 0
}

// widthRange 返回表达式可能消耗的字节范围；max 为 -1 表示无上界。
func widthRange(n Node) (min, max int, ok bool) {
	switch v := n.(type) {
	case Literal:
		return len(v.Value), len(v.Value), true
	case Any:
		return 1, 4, true
	case Class:
		return 1, 1, true
	case UnicodeClass:
		return 1, 4, true
	case Assertion, ControlVerb, Lookaround:
		return 0, 0, true
	case Group:
		return widthRange(v.Child)
	case Sequence:
		min, max = 0, 0
		for _, child := range v.Elements {
			a, b, childOK := widthRange(child)
			if !childOK {
				return 0, 0, false
			}
			min = safeAddInt(min, a)
			if max < 0 || b < 0 {
				max = -1
			} else {
				max = safeAddInt(max, b)
			}
		}
		return min, max, min >= 0
	case Alternation:
		if len(v.Options) == 0 {
			return 0, 0, true
		}
		min, max, _ = widthRange(v.Options[0])
		for _, child := range v.Options[1:] {
			a, b, childOK := widthRange(child)
			if !childOK {
				return 0, 0, false
			}
			if a < min {
				min = a
			}
			if max < 0 || b < 0 {
				max = -1
			} else if b > max {
				max = b
			}
		}
		return min, max, min >= 0
	case Repeat:
		a, b, childOK := widthRange(v.Child)
		if !childOK {
			return 0, 0, false
		}
		min = safeMulInt(a, v.Min)
		if v.Max < 0 || b < 0 {
			max = -1
		} else {
			max = safeMulInt(b, v.Max)
		}
		return min, max, min >= 0
	default:
		return 0, 0, false
	}
}

// FixedWidthUTF8 返回 UTF-8 字节流中的固定宽度；无法证明固定宽度时返回失败。
func FixedWidthUTF8(n Node) (min, max int, ok bool) {
	min, max, ok = fixedWidthUTF8(n)
	return min, max, ok && min >= 0 && max >= 0
}

// WidthRangeUTF8 返回 UTF-8 字节宽度上下界，不要求上下界相等。
func WidthRangeUTF8(n Node) (min, max int, ok bool) { return fixedWidthUTF8(n) }

func fixedWidthUTF8(n Node) (int, int, bool) {
	switch v := n.(type) {
	case Literal:
		if !utf8.Valid(v.Value) {
			return -1, -1, false
		}
		return len(v.Value), len(v.Value), true
	case Class:
		if v.Kind != ClassNormal {
			return 1, 4, false
		}
		return 1, 1, true
	case Any, UnicodeClass:
		return 1, 4, false
	case Assertion, ControlVerb:
		return 0, 0, true
	case Lookaround:
		return 0, 0, true
	case Group:
		return fixedWidthUTF8(v.Child)
	case Sequence:
		min, max := 0, 0
		for _, child := range v.Elements {
			a, b, ok := fixedWidthUTF8(child)
			if !ok {
				return -1, -1, false
			}
			min, max = safeAddInt(min, a), safeAddInt(max, b)
			if min < 0 || max < 0 {
				return -1, -1, false
			}
		}
		return min, max, true
	case Alternation:
		if len(v.Options) == 0 {
			return 0, 0, true
		}
		a, b, ok := fixedWidthUTF8(v.Options[0])
		if !ok {
			return -1, -1, false
		}
		for _, child := range v.Options[1:] {
			x, y, childOK := fixedWidthUTF8(child)
			if !childOK || x != a || y != b {
				return -1, -1, false
			}
		}
		return a, b, true
	case Repeat:
		a, b, ok := fixedWidthUTF8(v.Child)
		if !ok || v.Max < 0 || v.Min != v.Max {
			return -1, -1, false
		}
		return safeMulInt(a, v.Min), safeMulInt(b, v.Max), true
	default:
		return -1, -1, false
	}
}
