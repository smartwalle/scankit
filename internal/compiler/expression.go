package compiler

import (
	"fmt"
	"math"
	"slices"

	"github.com/smartwalle/scankit/internal/nfagraph"
	"github.com/smartwalle/scankit/internal/parser"
)

// ExpressionInfo 是编译阶段和执行后端共享的不可变元数据。
type ExpressionInfo struct {
	ID               uint32
	Flags            uint32
	ExtFlags         uint64
	AST              parser.Node
	Graph            *nfagraph.Graph
	MinLength        uint64
	MaxLength        uint64
	CanMatchEmpty    bool
	UnorderedMatches bool
	MatchesAtEOD     bool
	MatchesOnlyAtEOD bool
	UTF8             bool
	UCP              bool
	SOMLeftmost      bool
	Prefilter        bool
	Combination      bool
	LBR              bool
	Captures         uint32
	Backreference    bool
	Conditional      bool
	Stateful         bool
	GraphFallback    bool
	// 编译阶段保留的源码级扩展参数，供后端选择和运行期确认共同使用。
	AllowVacuous    bool
	Highlander      bool
	Quiet           bool
	MinOffset       uint64
	MaxOffset       uint64
	ExtMinLength    uint64
	EditDistance    uint32
	HammingDistance uint32
	BailoutReason   string
}

// Width 返回表达式的最大匹配宽度。
func (i ExpressionInfo) Width() uint64 { return i.MaxLength }

// MinimumWidth 返回表达式最小匹配长度。
func (i ExpressionInfo) MinimumWidth() uint64 { return i.MinLength }

// MaximumWidth 返回表达式最大匹配长度；无界时为最大无符号值。
func (i ExpressionInfo) MaximumWidth() uint64 { return i.MaxLength }

// FixedWidth 判断表达式是否具有固定匹配长度。
func (i ExpressionInfo) FixedWidth() (uint64, bool) {
	return i.MinLength, i.MinLength == i.MaxLength
}

// RequiresUTF8 判断表达式是否需要 UTF-8 运行时语义。
func (i ExpressionInfo) RequiresUTF8() bool { return i.UTF8 || i.UCP }

// HasGraph 判断表达式是否拥有降级图。
func (i ExpressionInfo) HasGraph() bool { return i.Graph != nil }

// WidthBounded 判断表达式是否具有有限最大宽度。
func (i ExpressionInfo) WidthBounded() bool { return i.MaxLength != math.MaxUint64 }

// CapabilityNames 返回表达式依赖的运行能力名称。
func (i ExpressionInfo) CapabilityNames() []string {
	out := make([]string, 0, 5)
	if i.UTF8 {
		out = append(out, "utf8")
	}
	if i.UCP {
		out = append(out, "ucp")
	}
	if i.SOMLeftmost {
		out = append(out, "som")
	}
	if i.Prefilter {
		out = append(out, "prefilter")
	}
	if i.Combination {
		out = append(out, "combination")
	}
	if i.Stateful {
		out = append(out, "stateful")
	}
	return out
}

// RequiresCaptureRuntime 判断表达式是否依赖运行期捕获状态。
func (i ExpressionInfo) RequiresCaptureRuntime() bool { return i.Backreference || i.Conditional }

// RequiresStatefulRuntime 判断表达式是否必须由保留状态的执行路径处理。
func (i ExpressionInfo) RequiresStatefulRuntime() bool { return i.Stateful }

// DeriveExpressionInfo 计算保守的长度和能力元数据。
func DeriveExpressionInfo(id uint32, flags uint32, extFlags uint64, root parser.Node, graph *nfagraph.Graph) ExpressionInfo {
	minLength, maxLength := nodeLength(root, flags)
	atEOD, onlyAtEOD := eodProperties(root)
	return ExpressionInfo{ID: id, Flags: flags, ExtFlags: extFlags, AST: root, Graph: graph, MinLength: minLength, MaxLength: maxLength, CanMatchEmpty: minLength == 0, UnorderedMatches: parser.HasAssertion(root), MatchesAtEOD: atEOD, MatchesOnlyAtEOD: onlyAtEOD, UTF8: flags&(1<<5) != 0, UCP: flags&(1<<6) != 0, SOMLeftmost: flags&(1<<8) != 0, Prefilter: flags&(1<<7) != 0, Combination: flags&(1<<9) != 0, LBR: containsLookaround(root), Captures: uint32(parser.CaptureCount(root)), Backreference: parser.HasKind(root, parser.KindBackreference), Conditional: parser.HasKind(root, parser.KindConditional), Stateful: parser.RequiresStatefulRuntime(root)}
}

func eodProperties(root parser.Node) (matchesAtEOD, onlyAtEOD bool) {
	var walk func(parser.Node) (bool, bool)
	walk = func(node parser.Node) (bool, bool) {
		switch v := node.(type) {
		case parser.Assertion:
			switch v.Kind {
			case parser.End, parser.EndAbsolute, parser.EndBeforeFinalNewline:
				return true, true
			case parser.WordBoundary, parser.NonWordBoundary:
				return true, false
			default:
				return false, false
			}
		case parser.Group:
			return walk(v.Child)
		case parser.Lookaround:
			return walk(v.Child)
		case parser.Sequence:
			atEOD := false
			for index, child := range v.Elements {
				childAt, childOnly := walk(child)
				atEOD = atEOD || childAt
				if childOnly {
					suffixNullable := true
					for _, suffix := range v.Elements[index+1:] {
						if !parser.Nullable(suffix) {
							suffixNullable = false
							break
						}
					}
					if suffixNullable {
						return true, true
					}
				}
			}
			return atEOD, false
		case parser.Alternation:
			if len(v.Options) == 0 {
				return false, false
			}
			atEOD, only := false, true
			for _, option := range v.Options {
				optionAt, optionOnly := walk(option)
				atEOD = atEOD || optionAt
				only = only && optionOnly
			}
			return atEOD, only
		case parser.Repeat:
			return walk(v.Child)
		default:
			return false, false
		}
	}
	matchesAtEOD, onlyAtEOD = walk(root)
	if parser.Nullable(root) {
		matchesAtEOD = true
	}
	return matchesAtEOD, onlyAtEOD && matchesAtEOD
}

// Validate 检查表达式元数据是否自洽，例如状态化能力与捕获组是否匹配。
func (i ExpressionInfo) Validate() error {
	if i.AST == nil {
		return fmt.Errorf("missing AST")
	}
	if i.Graph == nil && !i.Combination && !i.GraphFallback {
		return fmt.Errorf("missing graph")
	}
	if i.Graph != nil {
		if err := i.Graph.Validate(); err != nil {
			return err
		}
	}
	if i.MinLength > i.MaxLength {
		return fmt.Errorf("invalid expression width")
	}
	if i.MatchesOnlyAtEOD && !i.MatchesAtEOD {
		return fmt.Errorf("eod-only metadata requires eod metadata")
	}
	if i.Backreference && i.Captures == 0 {
		return fmt.Errorf("backreference metadata requires capture")
	}
	if i.Conditional && i.Captures == 0 {
		return fmt.Errorf("conditional metadata requires capture")
	}
	if i.RequiresCaptureRuntime() && !i.Stateful {
		return fmt.Errorf("capture runtime metadata requires stateful execution")
	}
	if i.LBR && !i.Stateful {
		return fmt.Errorf("lookaround metadata requires stateful execution")
	}
	if i.BailoutReason != "" && !i.GraphFallback && i.Graph == nil && !i.Combination {
		return fmt.Errorf("bailout metadata requires fallback or combination")
	}
	return nil
}

// ApplyCompileAttributes 写入编译标志和扩展参数的完整快照。
// 参数位值与公开编译契约保持一致，避免编译器包依赖上层包。
func (i *ExpressionInfo) ApplyCompileAttributes(flags uint32, extFlags uint64,
	minOffset, maxOffset, minLength uint64, editDistance, hammingDistance uint32) {
	if i == nil {
		return
	}
	i.AllowVacuous = flags&(1<<4) != 0
	i.Highlander = flags&(1<<3) != 0
	i.Quiet = flags&(1<<10) != 0
	i.MinOffset, i.MaxOffset, i.ExtMinLength = minOffset, maxOffset, minLength
	i.EditDistance, i.HammingDistance = editDistance, hammingDistance
	// 未启用的字段使用源码中的默认值：偏移上界为无界，其余为零。
	if extFlags&(1<<1) == 0 {
		i.MaxOffset = math.MaxUint64
	}
	if extFlags&(1<<0) == 0 {
		i.MinOffset = 0
	}
	if extFlags&(1<<2) == 0 {
		i.ExtMinLength = 0
	}
	if extFlags&(1<<3) == 0 {
		i.EditDistance = 0
	}
	if extFlags&(1<<4) == 0 {
		i.HammingDistance = 0
	}
}

// SetBailout 记录后端未采用专用路径的稳定原因。
func (i *ExpressionInfo) SetBailout(reason string) {
	if i != nil {
		i.BailoutReason = reason
	}
}

// Clone 创建元数据及其 AST、图的独立副本。
func (i ExpressionInfo) Clone() ExpressionInfo {
	out := i
	out.AST = parser.Clone(i.AST)
	out.Graph = i.Graph.Clone()
	return out
}

func nodeLength(n parser.Node, flags uint32) (uint64, uint64) {
	switch v := n.(type) {
	case parser.Literal:
		return uint64(len(v.Value)), uint64(len(v.Value))
	case parser.Any:
		if flags&(1<<5) != 0 {
			return 1, 4
		}
		return 1, 1
	case parser.UnicodeClass:
		return 1, 4
	case parser.Class:
		if flags&(1<<6) != 0 && v.Kind != parser.ClassNormal {
			return 1, 4
		}
		return 1, 1
	case parser.Assertion, parser.ControlVerb, parser.Lookaround:
		return 0, 0
	case parser.Group:
		return nodeLength(v.Child, flags)
	case parser.Backreference:
		return 0, math.MaxUint64
	case parser.Sequence:
		var minLength, maxLength uint64
		for _, c := range v.Elements {
			a, b := nodeLength(c, flags)
			minLength = satAdd(minLength, a)
			if maxLength == math.MaxUint64 || b == math.MaxUint64 {
				maxLength = math.MaxUint64
			} else {
				maxLength = satAdd(maxLength, b)
			}
		}
		return minLength, maxLength
	case parser.Alternation:
		if len(v.Options) == 0 {
			return 0, 0
		}
		minLength := uint64(math.MaxUint64)
		var maxLength uint64
		for _, c := range v.Options {
			a, b := nodeLength(c, flags)
			if a < minLength {
				minLength = a
			}
			if b > maxLength {
				maxLength = b
			}
		}
		return minLength, maxLength
	case parser.Repeat:
		a, b := nodeLength(v.Child, flags)
		minLength := satMul(a, uint64(v.Min))
		if v.Max < 0 || b == math.MaxUint64 {
			return minLength, math.MaxUint64
		}
		return minLength, satMul(b, uint64(v.Max))
	default:
		return 0, math.MaxUint64
	}
}

func satAdd(a, b uint64) uint64 {
	if math.MaxUint64-a < b {
		return math.MaxUint64
	}
	return a + b
}

func satMul(a, b uint64) uint64 {
	if a == 0 || b == 0 {
		return 0
	}
	if a > math.MaxUint64/b {
		return math.MaxUint64
	}
	return a * b
}

func containsLookaround(n parser.Node) bool {
	switch v := n.(type) {
	case parser.Lookaround:
		return true
	case parser.Group:
		return containsLookaround(v.Child)
	case parser.Sequence:
		if slices.ContainsFunc(v.Elements, containsLookaround) {
			return true
		}
	case parser.Alternation:
		if slices.ContainsFunc(v.Options, containsLookaround) {
			return true
		}
	case parser.Repeat:
		return containsLookaround(v.Child)
	default:
	}
	return false
}
