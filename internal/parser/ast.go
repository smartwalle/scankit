// Package parser 实现 scankit 的正则前端 AST。
package parser

import "reflect"

type Node interface{ node() }

// Kind 是 AST 节点的稳定分类。
type Kind uint8

// Summary 汇总 AST 节点数量和关键能力。
type Summary struct {
	Nodes    int
	Literals int
	Captures int
	MaxDepth int
	Nullable bool
	Stateful bool
}

// Summarize 返回表达式结构摘要，便于编译器选择执行路径。
func Summarize(root Node) Summary {
	s := Summary{Nullable: Nullable(root), Stateful: RequiresStatefulRuntime(root)}
	var walk func(Node, int)
	walk = func(node Node, depth int) {
		if node == nil {
			return
		}
		s.Nodes++
		if depth > s.MaxDepth {
			s.MaxDepth = depth
		}
		if _, ok := node.(Literal); ok {
			s.Literals++
		}
		if g, ok := node.(Group); ok && g.Capture > s.Captures {
			s.Captures = g.Capture
		}
		Children(node, func(child Node) { walk(child, depth+1) })
	}
	walk(root, 1)
	return s
}

const (
	KindUnknown Kind = iota
	KindLiteral
	KindAny
	KindClass
	KindUnicodeClass
	KindSequence
	KindAlternation
	KindRepeat
	KindGroup
	KindLookaround
	KindAssertion
	KindBackreference
	KindControlVerb
	KindConditional
	KindCombination
)

// NodeKind 返回节点分类。
func NodeKind(n Node) Kind {
	switch n.(type) {
	case Literal:
		return KindLiteral
	case Any:
		return KindAny
	case Class:
		return KindClass
	case UnicodeClass:
		return KindUnicodeClass
	case Sequence:
		return KindSequence
	case Alternation:
		return KindAlternation
	case Repeat:
		return KindRepeat
	case Group:
		return KindGroup
	case Lookaround:
		return KindLookaround
	case Assertion:
		return KindAssertion
	case Backreference:
		return KindBackreference
	case ControlVerb:
		return KindControlVerb
	case Conditional:
		return KindConditional
	case Combination, CombinationOperand, CombinationOperator, CombinationNot:
		return KindCombination
	default:
		return KindUnknown
	}
}

// Children 遍历节点的直接子节点。
func Children(n Node, fn func(Node)) {
	if fn == nil {
		return
	}
	switch v := n.(type) {
	case Group:
		fn(v.Child)
	case Sequence:
		for _, child := range v.Elements {
			fn(child)
		}
	case Alternation:
		for _, child := range v.Options {
			fn(child)
		}
	case Repeat:
		fn(v.Child)
	case Lookaround:
		fn(v.Child)
	case Conditional:
		fn(v.Yes)
		fn(v.No)
	case Combination:
		fn(v.Expr)
	case CombinationOperator:
		fn(v.Left)
		fn(v.Right)
	case CombinationNot:
		fn(v.Child)
	}
}

// Equal 判断两个 AST 的节点和值是否一致。
func Equal(a, b Node) bool { return reflect.DeepEqual(a, b) }

func IsEmpty(n Node) bool {
	switch v := n.(type) {
	case Sequence:
		for _, child := range v.Elements {
			if !IsEmpty(child) {
				return false
			}
		}
		return true
	case Literal:
		return len(v.Value) == 0
	case Group:
		return IsEmpty(v.Child)
	case Repeat:
		return v.Min == 0
	case Assertion, ControlVerb, Lookaround:
		return true
	case Alternation:
		if len(v.Options) == 0 {
			return true
		}
		for _, child := range v.Options {
			if !IsEmpty(child) {
				return false
			}
		}
		return true
	case Conditional:
		return Nullable(v.Yes) || Nullable(v.No)
	case Combination, CombinationOperand, CombinationOperator, CombinationNot:
		return true
	default:
		return false
	}
}

// CanMatchEmpty 是 IsEmpty 的语义别名。
func CanMatchEmpty(n Node) bool { return Nullable(n) }

func CaptureCount(root Node) int {
	count := 0
	Walk(root, func(n Node) bool {
		if g, ok := n.(Group); ok && g.Capture > count {
			count = g.Capture
		}
		return true
	})
	return count
}

// HasCapture 判断 AST 是否包含捕获组。
func HasCapture(root Node) bool { return CaptureCount(root) > 0 }

// MaxLiteralLength 返回 AST 中最长文字节点的字节长度。
func MaxLiteralLength(root Node) int {
	max := 0
	Walk(root, func(n Node) bool {
		if literal, ok := n.(Literal); ok && len(literal.Value) > max {
			max = len(literal.Value)
		}
		return true
	})
	return max
}

// MinLiteralLength 返回 AST 中非空文字节点的最短字节长度。
func MinLiteralLength(root Node) int {
	min := 0
	found := false
	Walk(root, func(node Node) bool {
		if literal, ok := node.(Literal); ok && len(literal.Value) > 0 && (!found || len(literal.Value) < min) {
			min, found = len(literal.Value), true
		}
		return true
	})
	return min
}

// HasAssertion 判断 AST 是否包含断言或边界节点。
func HasAssertion(root Node) bool {
	return HasKind(root, KindAssertion) || HasKind(root, KindLookaround)
}

// MaxRepeatCount 返回重复节点的最大上界；无限重复以 -1 表示。
func MaxRepeatCount(root Node) int {
	max := 0
	Walk(root, func(node Node) bool {
		if repeat, ok := node.(Repeat); ok {
			if repeat.Max == -1 {
				max = -1
			} else if max >= 0 && repeat.Max > max {
				max = repeat.Max
			}
		}
		return true
	})
	return max
}

// RequiresBacktracking 判断是否存在需要保存运行上下文的能力。
func RequiresBacktracking(root Node) bool {
	return HasKind(root, KindBackreference) || HasKind(root, KindConditional) || HasKind(root, KindLookaround)
}

// Literals 返回 AST 中出现的文字节点副本，按遍历顺序排列。
func Literals(root Node) [][]byte {
	out := make([][]byte, 0)
	Walk(root, func(node Node) bool {
		if literal, ok := node.(Literal); ok {
			out = append(out, append([]byte(nil), literal.Value...))
		}
		return true
	})
	return out
}

// FirstLiterals 返回可能出现在匹配起点的文字前缀。
func FirstLiterals(root Node) [][]byte {
	out := make([][]byte, 0)
	var walk func(Node)
	walk = func(n Node) {
		switch v := n.(type) {
		case Literal:
			if len(v.Value) > 0 {
				out = append(out, append([]byte(nil), v.Value...))
			}
		case Group:
			walk(v.Child)
		case Sequence:
			for _, child := range v.Elements {
				walk(child)
				if !Nullable(child) {
					break
				}
			}
		case Alternation:
			for _, child := range v.Options {
				walk(child)
			}
		case Repeat:
			if v.Min > 0 || Nullable(v.Child) {
				walk(v.Child)
			}
		}
	}
	walk(root)
	seen := make(map[string]struct{}, len(out))
	unique := out[:0]
	for _, literal := range out {
		key := string(literal)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		unique = append(unique, literal)
	}
	return unique
}

// FirstBytes 返回可能在匹配起点消费的字节集合；无法静态确定时返回完整集合。
func FirstBytes(root Node) []byte {
	set := [256]bool{}
	if Nullable(root) {
		for i := range set {
			set[i] = true
		}
	}
	var walk func(Node)
	walk = func(n Node) {
		switch v := n.(type) {
		case Literal:
			if len(v.Value) > 0 {
				set[v.Value[0]] = true
			}
		case Class:
			if v.Negated {
				for i := range set {
					set[i] = true
				}
			} else {
				for _, r := range v.Ranges {
					for c := r.Lo; ; c++ {
						set[c] = true
						if c == r.Hi {
							break
						}
					}
				}
			}
		case Any, UnicodeClass:
			for i := range set {
				set[i] = true
			}
		case Group:
			walk(v.Child)
		case Sequence:
			for _, child := range v.Elements {
				walk(child)
				if !Nullable(child) {
					break
				}
			}
		case Alternation:
			for _, child := range v.Options {
				walk(child)
			}
		case Repeat:
			walk(v.Child)
		}
	}
	walk(root)
	out := make([]byte, 0)
	for i, ok := range set {
		if ok {
			out = append(out, byte(i))
		}
	}
	return out
}

// HasKind 判断 AST 是否包含指定类型的节点。
func HasKind(root Node, kind Kind) bool {
	found := false
	Walk(root, func(node Node) bool {
		if NodeKind(node) == kind {
			found = true
			return false
		}
		return true
	})
	return found
}

// RequiresStatefulRuntime 判断表达式是否依赖不能直接降级为无状态图的执行语义。
func RequiresStatefulRuntime(root Node) bool {
	required := false
	Walk(root, func(node Node) bool {
		switch value := node.(type) {
		case Backreference, Conditional, Lookaround, ControlVerb:
			required = true
			return false
		case Group:
			if value.Atomic || value.HasScopedFlags() {
				required = true
				return false
			}
		}
		return true
	})
	return required
}

// HasScopedFlags 判断表达式中是否存在只作用于子表达式的模式修饰符。
func HasScopedFlags(root Node) bool {
	found := false
	Walk(root, func(node Node) bool {
		if group, ok := node.(Group); ok && group.HasScopedFlags() {
			found = true
			return false
		}
		return true
	})
	return found
}

// CaptureIDs 返回捕获组编号，按 AST 遍历顺序排列。
func CaptureIDs(root Node) []int {
	out := make([]int, 0)
	Walk(root, func(node Node) bool {
		if group, ok := node.(Group); ok && group.Capture > 0 {
			out = append(out, group.Capture)
		}
		return true
	})
	return out
}

// ReferenceIDs 返回反向引用和条件引用的捕获组编号，按遍历顺序去重。
func ReferenceIDs(root Node) []int {
	seen := map[int]struct{}{}
	out := make([]int, 0)
	Walk(root, func(node Node) bool {
		index := 0
		switch value := node.(type) {
		case Backreference:
			index = value.Index
		case Conditional:
			index = value.Index
		}
		if index > 0 {
			if _, ok := seen[index]; !ok {
				seen[index] = struct{}{}
				out = append(out, index)
			}
		}
		return true
	})
	return out
}

func Nullable(n Node) bool {
	switch v := n.(type) {
	case nil, Assertion, ControlVerb:
		return true
	case Literal:
		return len(v.Value) == 0
	case Group:
		return Nullable(v.Child)
	case Lookaround:
		return true
	case Sequence:
		for _, c := range v.Elements {
			if !Nullable(c) {
				return false
			}
		}
		return true
	case Alternation:
		for _, c := range v.Options {
			if Nullable(c) {
				return true
			}
		}
		return false
	case Repeat:
		return v.Min == 0 || Nullable(v.Child)
	case Conditional:
		return Nullable(v.Yes) || Nullable(v.No)
	case Combination, CombinationOperand, CombinationOperator, CombinationNot:
		return true
	default:
		return false
	}
}

func Clone(root Node) Node {
	switch v := root.(type) {
	case Literal:
		return Literal{Value: append([]byte(nil), v.Value...)}
	case Group:
		return Group{Child: Clone(v.Child), Atomic: v.Atomic, Capture: v.Capture, SetFlags: v.SetFlags, ClearFlags: v.ClearFlags}
	case Lookaround:
		return Lookaround{Child: Clone(v.Child), Kind: v.Kind}
	case Sequence:
		out := Sequence{Elements: make([]Node, len(v.Elements))}
		for i, c := range v.Elements {
			out.Elements[i] = Clone(c)
		}
		return out
	case Alternation:
		out := Alternation{Options: make([]Node, len(v.Options))}
		for i, c := range v.Options {
			out.Options[i] = Clone(c)
		}
		return out
	case Repeat:
		return Repeat{Child: Clone(v.Child), Min: v.Min, Max: v.Max, Greedy: v.Greedy}
	case Class:
		return Class{Ranges: append([]Range(nil), v.Ranges...), Negated: v.Negated, Kind: v.Kind}
	case UnicodeClass, Any, Assertion, Backreference, ControlVerb, Conditional, Combination, CombinationOperand, CombinationOperator, CombinationNot:
		return cloneComposite(v)
	default:
		return root
	}
}
func Depth(root Node) int {
	max := 0
	WalkDepth(root, 0, func(d int) {
		if d > max {
			max = d
		}
	})
	return max
}

// Width 返回节点的固定宽度；非固定宽度时 ok 为 false。
func Width(root Node) (int, bool) {
	min, max, ok := FixedWidth(root)
	return min, ok && min == max
}

// WidthRange 返回节点的最小和最大宽度；无界时 max 为 -1。
func WidthRange(root Node) (min, max int, ok bool) {
	return widthRange(root)
}
func WalkDepth(root Node, depth int, fn func(int)) {
	if root == nil || fn == nil {
		return
	}
	fn(depth)
	var children []Node
	switch v := root.(type) {
	case Group:
		children = []Node{v.Child}
	case Lookaround:
		children = []Node{v.Child}
	case Repeat:
		children = []Node{v.Child}
	case Sequence:
		children = v.Elements
	case Alternation:
		children = v.Options
	case Conditional:
		children = []Node{v.Yes, v.No}
	case Combination:
		children = []Node{v.Expr}
	case CombinationOperator:
		children = []Node{v.Left, v.Right}
	case CombinationNot:
		children = []Node{v.Child}
	}
	for _, child := range children {
		WalkDepth(child, depth+1, fn)
	}
}
func cloneComposite(v any) Node {
	switch n := v.(type) {
	case Any:
		return n
	case Assertion:
		return n
	case Backreference:
		return n
	case ControlVerb:
		return n
	case UnicodeClass:
		return n
	case Conditional:
		return Conditional{Index: n.Index, Yes: Clone(n.Yes), No: Clone(n.No)}
	case Combination:
		return Combination{Expr: Clone(n.Expr)}
	case CombinationOperand:
		return n
	case CombinationOperator:
		return CombinationOperator{Op: n.Op, Left: Clone(n.Left), Right: Clone(n.Right)}
	case CombinationNot:
		return CombinationNot{Child: Clone(n.Child)}
	}
	return nil
}

type Literal struct{ Value []byte }

func (Literal) node() {}

// Any 匹配一个字节，对应点号元字符。
type Any struct{}

func (Any) node() {}

type Class struct {
	Ranges  []Range
	Negated bool
	Kind    ClassKind
}

func (Class) node() {}

type UnicodeClass struct {
	Name    string
	Negated bool
}

func (UnicodeClass) node() {}

type Range struct{ Lo, Hi byte }

// ClassKind 标识由简写字符类生成的运行时分类。
type ClassKind uint8

const (
	ClassNormal ClassKind = iota
	ClassDigit
	ClassWord
	ClassSpace
	ClassHorizontalSpace
	ClassVerticalSpace
)

type Sequence struct{ Elements []Node }

func (Sequence) node() {}

type Alternation struct{ Options []Node }

func (Alternation) node() {}

type Repeat struct {
	Child    Node
	Min, Max int
	Greedy   bool
}

func (Repeat) node() {}

type Assertion struct{ Kind AssertionKind }

func (Assertion) node() {}

type AssertionKind uint8

const (
	Begin AssertionKind = iota + 1
	End
	WordBoundary
	NonWordBoundary
	BeginAbsolute
	EndAbsolute
	EndBeforeFinalNewline
)

// GroupFlag 表示可在分组内局部启用或关闭的模式修饰符。
type GroupFlag uint8

const (
	GroupFlagCaseless GroupFlag = 1 << iota
	GroupFlagDotAll
	GroupFlagMultiline
	GroupFlagExtended
)

type Group struct {
	Child      Node
	Atomic     bool
	Capture    int
	SetFlags   GroupFlag
	ClearFlags GroupFlag
}

// HasScopedFlags 判断分组是否修改子表达式的模式修饰符。
func (g Group) HasScopedFlags() bool { return g.SetFlags != 0 || g.ClearFlags != 0 }

func (Group) node() {}

type Backreference struct{ Index int }

func (Backreference) node() {}

type ControlVerb struct{ Name string }

func (ControlVerb) node() {}

// Conditional 根据此前捕获的分组是否存在选择分支。
type Conditional struct {
	Index   int
	Yes, No Node
}

func (Conditional) node() {}

type Lookaround struct {
	Child Node
	Kind  LookaroundKind
}

func (Lookaround) node() {}

type LookaroundKind uint8

const (
	Lookahead LookaroundKind = iota + 1
	NegativeLookahead
	Lookbehind
	NegativeLookbehind
)

type ParseError struct {
	Position int
	Message  string
}

func (e *ParseError) Error() string { return e.Message }
