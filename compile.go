package scankit

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/smartwalle/scankit/internal/combination"
	"github.com/smartwalle/scankit/internal/compiler"
	"github.com/smartwalle/scankit/internal/dfa"
	"github.com/smartwalle/scankit/internal/engine"
	"github.com/smartwalle/scankit/internal/nfa"
	"github.com/smartwalle/scankit/internal/nfagraph"
	"github.com/smartwalle/scankit/internal/parser"
	"github.com/smartwalle/scankit/internal/prefilter"
	"github.com/smartwalle/scankit/internal/repeat"
	"github.com/smartwalle/scankit/internal/smallengine"
)

var (
	// ErrEmptyExpressions 表示传入的规则集为空，没有任何可编译的表达式。
	ErrEmptyExpressions = errors.New("empty expressions")

	// ErrDuplicateExpression 表示同一个 Scanner 中存在两个 Id 相同的表达式。
	ErrDuplicateExpression = errors.New("duplicate expression")

	// ErrInvalidExpression 表示单条表达式的 Flag/Pattern/Ext 组合不合法，例如包含未知位、
	// 同时启用互斥的 Flag 组合，或者组合规则引用的 id 不存在。
	ErrInvalidExpression = errors.New("invalid expression")

	// ErrUnsupportedFlag 表示 [Expression.Flags] 中出现了未定义的编译位或不支持的组合。
	ErrUnsupportedFlag = errors.New("unsupported compile flag")

	// ErrInvalidUTF8 表示开启 [CompileUTF8] 的表达式收到了不合法 UTF-8 输入。
	ErrInvalidUTF8 = errors.New("invalid UTF-8 input")

	// ErrUnsupportedExpression 表示语法解析成功但当前实现尚未提供执行路径，例如某些
	// 字符类断言或近似匹配在特定 Flag 组合下被拒绝。
	ErrUnsupportedExpression = errors.New("unsupported expression")

	// ErrRegexTooComplex 表示表达式的资源占用超出 [compiler.CompileLimits] 设定的上限，
	// 例如状态数、缓存展开或字符类数量超过阈值。
	ErrRegexTooComplex = errors.New("regular expression is too complex")

	// ErrInvalidExtension 表示 [Expression.Ext] 的 Flags 与数值字段不一致，例如偏移区间非法。
	ErrInvalidExtension = errors.New("invalid expression extension")

	// ErrUnsupportedExtension 表示扩展请求在当前表达式形态下无法精确执行，
	// 例如组合规则搭配了非偏移类扩展。
	ErrUnsupportedExtension = errors.New("unsupported expression extension")

	// ErrInvalidCombination 表示组合规则的语法或操作数引用不合法，例如空组合、引用未知
	// id、组合规则引用另一个组合规则。
	ErrInvalidCombination = errors.New("invalid combination")
)

// CompileFlag 是编译期标志位集合，控制匹配语义和输出行为。
type CompileFlag uint32

// String 返回组合标志名称，未知位以十六进制保留。
func (f CompileFlag) String() string {
	if f == 0 {
		return "0"
	}
	names := []struct {
		flag CompileFlag
		name string
	}{{CompileCaseless, "caseless"}, {CompileDotAll, "dotall"}, {CompileMultiline, "multiline"}, {CompileSingleMatch, "singlematch"}, {CompileAllowEmpty, "allowempty"}, {CompileUTF8, "utf8"}, {CompileUCP, "ucp"}, {CompilePrefilter, "prefilter"}, {CompileSOMLeftmost, "som-leftmost"}, {CompileCombination, "combination"}, {CompileQuiet, "quiet"}}
	out := ""
	for _, item := range names {
		if f&item.flag != 0 {
			if out != "" {
				out += "|"
			}
			out += item.name
		}
	}
	if unknown := uint32(f &^ (CompileCaseless | CompileDotAll | CompileMultiline | CompileSingleMatch | CompileAllowEmpty | CompileUTF8 | CompileUCP | CompilePrefilter | CompileSOMLeftmost | CompileCombination | CompileQuiet)); unknown != 0 {
		if out != "" {
			out += "|"
		}
		out += "0x" + strconv.FormatUint(uint64(unknown), 16)
	}
	return out
}

// Valid 判断标志位是否只包含已定义的编译标志。
func (f CompileFlag) Valid() bool {
	known := CompileCaseless | CompileDotAll | CompileMultiline | CompileSingleMatch | CompileAllowEmpty | CompileUTF8 | CompileUCP | CompilePrefilter | CompileSOMLeftmost | CompileCombination | CompileQuiet
	return f&^known == 0
}

// Has 判断指定标志位是否已启用。
func (f CompileFlag) Has(flag CompileFlag) bool { return f&flag != 0 }

func validateCompileFlags(flags CompileFlag) error {
	if !flags.Valid() {
		return fmt.Errorf("%w: invalid compile flag", ErrUnsupportedFlag)
	}
	switch {
	case flags&CompileSingleMatch != 0 && flags&CompileSOMLeftmost != 0:
		return fmt.Errorf("%w: singlematch is not supported with som-leftmost", ErrUnsupportedFlag)
	case flags&CompileQuiet != 0 && flags&CompileSOMLeftmost != 0:
		return fmt.Errorf("%w: quiet is not supported with som-leftmost", ErrUnsupportedFlag)
	case flags&CompilePrefilter != 0 && flags&CompileSOMLeftmost != 0:
		return fmt.Errorf("%w: prefilter is not supported with som-leftmost", ErrUnsupportedFlag)
	case flags&CompileCombination != 0 && flags&^(CompileCombination|CompileQuiet|CompileSingleMatch) != 0:
		return fmt.Errorf("%w: only quiet and singlematch are supported with combination", ErrUnsupportedFlag)
	default:
	}
	return nil
}

// 编译标志的位定义，可按位或组合后写入 [Expression.Flags]，未定义位由 [CompileFlag.Valid] 判定。
const (
	// CompileCaseless 忽略大小写：默认按 ASCII 折叠，配合 [CompileUTF8] 时按 Unicode 简单折叠。
	CompileCaseless CompileFlag = 1

	// CompileDotAll 让 `.` 匹配换行符。
	CompileDotAll CompileFlag = 2

	// CompileMultiline 让 `^` 与 `$` 额外匹配行首和行尾，`\A` 与 `\z` 仍是数据的绝对边界。
	CompileMultiline CompileFlag = 4

	// CompileSingleMatch 每条规则只报告首个匹配，之后不再报告该规则的结果。
	CompileSingleMatch CompileFlag = 8

	// CompileAllowEmpty 放行匹配空串的规则，未开启时 [Compile] 直接报错，同时关闭前缀守卫优化。
	CompileAllowEmpty CompileFlag = 16

	// CompileUTF8 按 UTF-8 码位解释模式与输入，匹配起点不会落在续字节上。
	CompileUTF8 CompileFlag = 32

	// CompileUCP 让 `\b`、`\w` 这类字符判定按 Unicode 码点而非字节进行，通常与 [CompileUTF8] 同时开启。
	CompileUCP CompileFlag = 64

	// CompilePrefilter 启用编译期推导的候选文字做起点过滤，未开启时不建立起点表。
	CompilePrefilter CompileFlag = 128

	// CompileSOMLeftmost 同一规则在每个结束偏移只保留起点最早的匹配。
	CompileSOMLeftmost CompileFlag = 256

	// CompileCombination 表示模式是布尔组合表达式而非正则，只允许搭配偏移类扩展。
	CompileCombination CompileFlag = 512

	// CompileQuiet 只参与内部判定，不向调用方报告该规则的匹配。
	CompileQuiet CompileFlag = 1024
)

// Expression 是一条待编译的规则：编号、正则模式和编译标志。
type Expression struct {
	Id      uint32
	Pattern string
	Flags   CompileFlag
	Ext     *ExpressionExt
}

// Validate 检查单条表达式的标志和扩展参数，不执行图构造。
func (e Expression) Validate() error {
	if err := validateCompileFlags(e.Flags); err != nil {
		return err
	}
	return e.Ext.Validate()
}

// ExpressionExtFlag 是扩展约束的标志位集合。
type ExpressionExtFlag uint64

// 表达式扩展标志的位定义，每一位决定 [ExpressionExt] 中对应字段是否参与校验。
const (
	// ExtFlagMinOffset 启用 [ExpressionExt.MinOffset]：匹配结束偏移不得小于该值，值为 0 时不产生约束。
	ExtFlagMinOffset ExpressionExtFlag = 1

	// ExtFlagMaxOffset 启用 [ExpressionExt.MaxOffset]：匹配结束偏移不得大于该值，未启用时上界无界。
	ExtFlagMaxOffset ExpressionExtFlag = 2

	// ExtFlagMinLength 启用 [ExpressionExt.MinLength]：匹配长度不得小于该值。
	ExtFlagMinLength ExpressionExtFlag = 4

	// ExtFlagEditDistance 按编辑距离容错，上限取 [ExpressionExt.EditDistance]，与 [ExtFlagHammingDistance] 互斥。
	ExtFlagEditDistance ExpressionExtFlag = 8

	// ExtFlagHammingDistance 按汉明距离容错，上限取 [ExpressionExt.HammingDistance]，与 [ExtFlagEditDistance] 互斥。
	ExtFlagHammingDistance ExpressionExtFlag = 16
)

// ExpressionExt 保存偏移、长度与模糊匹配等扩展约束。
type ExpressionExt struct {
	Flags           ExpressionExtFlag
	MinOffset       uint64
	MaxOffset       uint64
	MinLength       uint64
	EditDistance    uint32
	HammingDistance uint32
}

// Clone 返回扩展约束的副本，nil 输入返回 nil。
func (e *ExpressionExt) Clone() *ExpressionExt {
	if e == nil {
		return nil
	}
	c := *e
	return &c
}

// Enabled 判断扩展标志是否已启用。
func (e *ExpressionExt) Enabled(flag ExpressionExtFlag) bool {
	return e != nil && e.Flags&flag != 0
}

// OffsetAllowed 判断指定匹配结束偏移是否满足扩展偏移范围。
func (e *ExpressionExt) OffsetAllowed(offset uint64) bool {
	if e == nil {
		return true
	}
	if e.Flags&ExtFlagMinOffset != 0 && offset < e.MinOffset {
		return false
	}
	return e.Flags&ExtFlagMaxOffset == 0 || offset <= e.MaxOffset
}

// EndOffsetAllowed 是 OffsetAllowed 的兼容别名。
func (e *ExpressionExt) EndOffsetAllowed(offset uint64) bool { return e.OffsetAllowed(offset) }

// OffsetRange 返回启用的结束偏移范围；未设置边界时返回无界标记。
func (e *ExpressionExt) OffsetRange() (minOffset, maxOffset uint64, bounded bool) {
	if e == nil {
		return 0, 0, false
	}
	if e.Flags&ExtFlagMinOffset != 0 {
		minOffset = e.MinOffset
	}
	if e.Flags&ExtFlagMaxOffset != 0 {
		maxOffset, bounded = e.MaxOffset, true
	}
	return minOffset, maxOffset, bounded
}

// LengthAllowed 判断指定长度是否满足扩展最小长度约束。
func (e *ExpressionExt) LengthAllowed(length uint64) bool {
	return e == nil || e.Flags&ExtFlagMinLength == 0 || length >= e.MinLength
}

// Validate 检查扩展标志组合是否合法，例如偏移区间与模糊距离的互斥关系。
func (e *ExpressionExt) Validate() error {
	if e == nil {
		return nil
	}
	known := ExtFlagMinOffset | ExtFlagMaxOffset | ExtFlagMinLength | ExtFlagEditDistance | ExtFlagHammingDistance
	if e.Flags&^known != 0 {
		return fmt.Errorf("%w: unknown extension flag", ErrInvalidExtension)
	}
	if e.Flags&ExtFlagEditDistance != 0 && e.Flags&ExtFlagHammingDistance != 0 {
		return fmt.Errorf("%w: edit and hamming distance are mutually exclusive", ErrInvalidExtension)
	}
	if e.Flags&ExtFlagMinOffset != 0 && e.Flags&ExtFlagMaxOffset != 0 && e.MaxOffset < e.MinOffset {
		return fmt.Errorf("%w: maximum offset below minimum offset", ErrInvalidExtension)
	}
	if e.Flags&ExtFlagMinLength != 0 && e.Flags&ExtFlagMaxOffset != 0 && e.MinLength > e.MaxOffset {
		return fmt.Errorf("%w: minimum length exceeds maximum offset", ErrInvalidExtension)
	}
	return nil
}

// Compile 编译全部表达式并返回不可变 Scanner。
func Compile(expressions []Expression) (*Scanner, error) {
	return compileWithContext(context.Background(), expressions, compiler.CompileLimits{})
}

func compileWithContext(ctx context.Context, expressions []Expression, limits compiler.CompileLimits) (*Scanner, error) {
	if len(expressions) == 0 {
		return nil, fmt.Errorf("%w", ErrEmptyExpressions)
	}
	rules := make([]compiledRule, 0, len(expressions))
	seen := make(map[uint32]struct{}, len(expressions))
	var totalUsage compiler.Usage
	for i, expression := range expressions {
		if err := ctx.Err(); err != nil {
			return nil, &compiler.CompileError{Kind: compiler.ErrorCancelled, Expression: i, Message: "compile cancelled"}
		}
		if _, ok := seen[expression.Id]; ok {
			return nil, fmt.Errorf("%w: expression %d: %w", ErrDuplicateExpression, i, &compiler.CompileError{Kind: compiler.ErrorInvalidExpression, Expression: i, Position: 0, Message: "duplicate expression id"})
		}
		seen[expression.Id] = struct{}{}
		if err := expression.Validate(); err != nil {
			return nil, fmt.Errorf("%w: expression %d: %w: %w", ErrInvalidExpression, i, &compiler.CompileError{Kind: compiler.ErrorInvalidExpression, Expression: i, Position: 0, Message: err.Error()}, err)
		}
		flags := expression.Flags
		pattern, flags, extended, err := consumeLeadingInlineFlags(expression.Pattern, flags)
		if err != nil {
			return nil, fmt.Errorf("%w: expression %d: %w: %w", ErrUnsupportedExpression, i, &compiler.CompileError{Kind: compiler.ErrorSyntax, Expression: i, Position: 0, Message: err.Error()}, err)
		}
		if flags&CompileUTF8 != 0 && !utf8.ValidString(pattern) {
			return nil, fmt.Errorf("%w: expression %d: %w", ErrInvalidUTF8, i, &compiler.CompileError{Kind: compiler.ErrorSyntax, Expression: i, Position: 0, Message: "pattern is not valid UTF-8"})
		}
		if err = validateCompileFlags(flags); err != nil {
			return nil, fmt.Errorf("%w: expression %d: %w: %w", ErrUnsupportedFlag, i, &compiler.CompileError{Kind: compiler.ErrorInvalidExpression, Expression: i, Message: err.Error()}, err)
		}
		if flags&CompileCombination != 0 && expression.Ext != nil && expression.Ext.Flags&^(ExtFlagMinOffset|ExtFlagMaxOffset) != 0 {
			return nil, fmt.Errorf("%w: expression %d: %w", ErrUnsupportedExtension, i, &compiler.CompileError{Kind: compiler.ErrorInvalidExpression, Expression: i, Message: "only offset extensions are supported with combination"})
		}
		var root parser.Node
		var combNode parser.Node
		if flags&CompileCombination != 0 {
			combNode, err = parser.ParseCombination(pattern)
			if err == nil {
				combNode = combination.Normalize(combNode)
				root = combNode
			}
		} else {
			root, err = parser.ParseWithExtended(pattern, extended)
		}
		if err != nil {
			position := 0
			if parseErr, ok := errors.AsType[*parser.ParseError](err); ok {
				position = parseErr.Position
			}
			return nil, fmt.Errorf("%w: expression %d: %w: %w", ErrUnsupportedExpression, i, &compiler.CompileError{Kind: compiler.ErrorSyntax, Expression: i, Position: position, Message: err.Error()}, err)
		}
		root = parser.Normalize(root)
		if flags&CompileCombination != 0 {
			if err = parser.Validate(root); err != nil {
				return nil, fmt.Errorf("%w: expression %d: %w: %w", ErrInvalidCombination, i, &compiler.CompileError{Kind: compiler.ErrorInvalidExpression, Expression: i, Message: err.Error()}, err)
			}
			if err = expression.Ext.Validate(); err != nil {
				return nil, fmt.Errorf("%w: expression %d: %w: %w", ErrInvalidExtension, i, &compiler.CompileError{Kind: compiler.ErrorInvalidExpression, Expression: i, Message: err.Error()}, err)
			}
			var extCopy *ExpressionExt
			var extFlags ExpressionExtFlag
			if expression.Ext != nil {
				copyValue := *expression.Ext
				extCopy = &copyValue
				extFlags = expression.Ext.Flags
			}
			comboBytes := uint64(len(expression.Pattern))
			totalUsage.Add(compiler.Usage{ProgramBytes: comboBytes, MemoryBytes: comboBytes})
			if err = limits.Check(totalUsage); err != nil {
				return nil, fmt.Errorf("%w: expression %d: %w: %w", ErrRegexTooComplex, i, &compiler.CompileError{Kind: compiler.ErrorResourceLimit, Expression: i, Message: err.Error()}, err)
			}
			info := compiler.DeriveExpressionInfo(expression.Id, uint32(flags), uint64(extFlags), root, nil)
			applyExpressionAttributes(&info, flags, extCopy)
			rules = append(rules, compiledRule{id: expression.Id, root: root, comb: combNode, flags: flags, ext: extCopy, info: info})
			continue
		}
		if parser.Nullable(root) && flags&CompileAllowEmpty == 0 {
			return nil, fmt.Errorf("%w: expression %d: %w", ErrInvalidExpression, i, &compiler.CompileError{Kind: compiler.ErrorInvalidExpression, Expression: i, Message: "empty match requires allow-empty flag"})
		}
		if err = parser.Validate(root); err != nil {
			return nil, fmt.Errorf("%w: expression %d: %w: %w", ErrInvalidExpression, i, &compiler.CompileError{Kind: compiler.ErrorInvalidExpression, Expression: i, Position: 0, Message: err.Error()}, err)
		}
		if err = expression.Ext.Validate(); err != nil {
			return nil, fmt.Errorf("%w: expression %d: %w: %w", ErrInvalidExtension, i, &compiler.CompileError{Kind: compiler.ErrorInvalidExpression, Expression: i, Message: err.Error()}, err)
		}
		ng, err := nfagraph.NewBuilder().Build(root)
		if err != nil {
			// 图构造失败时继续保留 AST；运行期会走完整语义匹配器。
			var extCopy *ExpressionExt
			var extFlags ExpressionExtFlag
			if expression.Ext != nil {
				copyValue := *expression.Ext
				extCopy = &copyValue
				extFlags = copyValue.Flags
			}
			usage := compiler.Usage{ProgramBytes: uint64(len(pattern)), MemoryBytes: uint64(len(pattern))}
			totalUsage.Add(usage)
			if err = limits.Check(totalUsage); err != nil {
				return nil, fmt.Errorf("%w: expression %d: %w: %w", ErrRegexTooComplex, i, &compiler.CompileError{Kind: compiler.ErrorResourceLimit, Expression: i, Position: 0, Message: err.Error()}, err)
			}
			info := compiler.DeriveExpressionInfo(expression.Id, uint32(flags), uint64(extFlags), root, nil)
			applyExpressionAttributes(&info, flags, extCopy)
			info.GraphFallback = true
			info.SetBailout("graph-build")
			rules = append(rules, compiledRule{id: expression.Id, root: root, flags: flags, ext: extCopy, info: info})
			continue
		}
		var optimizeBailout string
		if _, err = nfagraph.Optimize(ng); err != nil {
			// 优化不收敛不应使合法表达式编译失败；Optimize 已恢复原图，
			// 后续继续使用未优化图。optimization-fallback 元数据携带 Optimize
			// 报告的 pass 粒度原因，便于调用方按 pass 排查震荡源。
			optimizeBailout = "optimization-fallback: " + err.Error()
		}
		if err = ctx.Err(); err != nil {
			return nil, &compiler.CompileError{Kind: compiler.ErrorCancelled, Expression: i, Message: "compile cancelled"}
		}
		graphEdges := uint64(ng.EdgeCount())
		graphUsage := compiler.Usage{GraphVertices: uint64(ng.NodeCount()), GraphEdges: graphEdges}
		if err = limits.Check(totalUsage.Merge(graphUsage)); err != nil {
			return nil, fmt.Errorf("%w: expression %d: %w: %w", ErrRegexTooComplex, i, &compiler.CompileError{Kind: compiler.ErrorResourceLimit, Expression: i, Position: 0, Message: err.Error()}, err)
		}
		dfaLimit := 0
		if limits.DFAStates > 0 && limits.DFAStates <= uint64(^uint(0)>>1) {
			dfaLimit = int(limits.DFAStates)
		}
		// 依赖断言、回溯或 Unicode 语义的规则保留 AST 确认路径，
		// 图仅作为元数据，不构造可能产生误报的字节后端。
		features := engine.SelectionFeatures{
			Stateful:   parser.RequiresStatefulRuntime(root),
			Assertions: parser.HasAssertion(root) || containsAny(root),
			UTF8:       flags&CompileUTF8 != 0,
			UCP:        flags&(CompileUCP|CompileCaseless|CompileMultiline|CompileDotAll) != 0,
		}
		var autoProgram *engine.Program
		if !features.Stateful && !features.Assertions && !features.UTF8 && !features.UCP {
			autoProgram, err = engine.CompileAutoWithFeatures(ng, features, dfaLimit, limits.MemoryBytes)
			if err != nil {
				kind := compiler.ErrorUnsupported
				if errors.Is(err, nfa.ErrStateLimit) || errors.Is(err, nfa.ErrEdgeLimit) || errors.Is(err, nfa.ErrMemoryLimit) || errors.Is(err, dfa.ErrStateLimit) || errors.Is(err, dfa.ErrMemoryLimit) {
					kind = compiler.ErrorResourceLimit
				}
				return nil, fmt.Errorf("%w: expression %d: %w", mapEngineKindToSentinel(kind), i, &compiler.CompileError{Kind: kind, Expression: i, Position: 0, Message: err.Error()})
			}
		}
		edges := uint64(0)
		roles := uint64(0)
		for _, id := range ng.Flow.Vertices() {
			if err = ctx.Err(); err != nil {
				return nil, &compiler.CompileError{Kind: compiler.ErrorCancelled, Expression: i, Message: "compile cancelled"}
			}
			edges += uint64(len(ng.Flow.Successors(id)))
			if node := ng.Nodes[id]; node != nil && node.Kind == nfagraph.KindLiteral && len(node.Literal) > 0 {
				roles++
			}
		}
		programBytes := satAdd64(uint64(len(pattern)), satAdd64(satMul64(uint64(len(ng.Nodes)), 32), satMul64(edges, 8)))
		usage := compiler.Usage{GraphVertices: uint64(len(ng.Nodes)), GraphEdges: edges, RoseRoles: roles, ProgramBytes: programBytes, MemoryBytes: programBytes}
		if autoProgram != nil {
			switch autoProgram.Kind {
			case engine.KindDFA:
				usage.DFAStates = uint64(engineStateCount(autoProgram))
				if autoProgram.DFA != nil {
					usage.MemoryBytes = satAdd64(usage.MemoryBytes, autoProgram.DFA.MemoryBytes())
				}
			case engine.KindNFA:
				if autoProgram.NFA != nil {
					usage.MemoryBytes = satAdd64(usage.MemoryBytes, autoProgram.NFA.MemoryBytes())
				}
			default:
			}
		}
		totalUsage.Add(usage)
		if err = limits.Check(totalUsage); err != nil {
			return nil, fmt.Errorf("%w: expression %d: %w: %w", ErrRegexTooComplex, i, &compiler.CompileError{Kind: compiler.ErrorResourceLimit, Expression: i, Position: 0, Message: err.Error()}, err)
		}
		extFlags := ExpressionExtFlag(0)
		var extCopy *ExpressionExt
		if expression.Ext != nil {
			extFlags = expression.Ext.Flags
			copyValue := *expression.Ext
			extCopy = &copyValue
			if flags&CompilePrefilter != 0 {
				extCopy.Flags &^= ExtFlagMinLength
				extCopy.MinLength = 0
				extFlags = extCopy.Flags
			}
		}
		var candidate []byte
		if lit, ok := literalPrefix(root); ok && !parser.HasScopedFlags(root) {
			candidate = lit
		}
		if len(candidate) == 0 && !parser.HasScopedFlags(root) {
			if filter, ok := prefilter.FromGraph(ng); ok {
				candidate = append([]byte(nil), filter.Literal...)
			}
		}
		// ASCII 大小写折叠无法覆盖多字节字符；此时不建立可能漏报的候选过滤。
		if flags&CompileCaseless != 0 && !asciiBytes(candidate) {
			candidate = nil
		}
		info := compiler.DeriveExpressionInfo(expression.Id, uint32(flags), uint64(extFlags), root, ng)
		applyExpressionAttributes(&info, flags, extCopy)
		if autoProgram == nil && (features.Stateful || features.Assertions || features.UTF8 || features.UCP) {
			info.SetBailout("feature-gate")
		}
		if optimizeBailout != "" {
			info.SetBailout(optimizeBailout)
		}
		rule := compiledRule{id: expression.Id, root: root, flags: flags, ext: extCopy, info: info, prefilter: candidate, program: autoProgram}
		// 词边界断言会挡掉字节后端，规则只能逐候选确认。这里尝试为确认阶段
		// 额外构建断言敏感的确定性表（见 confirm_dfa.go），把命中密集时的
		// 逐指令解释压缩成每字节一次表查找。不满足适用性边界或超出预算时保持
		// 为 nil，规则继续走确认程序虚机，语义不变。
		if autoProgram == nil && !info.GraphFallback && !info.Stateful && confirmDFAEligible(root, extCopy, flags) {
			if table := buildConfirmDFA(ng, firstRepeatPreference(root)); table != nil {
				if tableBytes := table.memoryBytes(); limits.MemoryBytes == 0 || tableBytes <= limits.MemoryBytes {
					rule.confirmDFA = table
					totalUsage.MemoryBytes = satAdd64(totalUsage.MemoryBytes, tableBytes)
				}
			}
		}
		if extCopy == nil && !parser.HasScopedFlags(root) && flags&(CompileCaseless|CompileUTF8|CompileUCP|CompileMultiline|CompileDotAll) == 0 {
			if literal, ok := literalPattern(root); ok && len(literal) > 0 {
				if small, smallErr := smallengine.CompileWithOptions(literal, smallengine.CompileOptions{SmallWriteSize: 8}); smallErr == nil {
					switch small.Kind {
					case smallengine.KindSmallWrite:
						rule.smallWrite = small.Write
					case smallengine.KindSmallBlock:
						rule.smallBlock = small.Block
					default:
					}
				}
			}
			if unit, minCount, maxCount, greedy, ok := repeatedLiteralPattern(root); ok {
				rule.repeat, err = repeat.New(unit, minCount, maxCount, greedy)
				if err != nil {
					return nil, fmt.Errorf("%w: expression %d: %w: %w", ErrUnsupportedExpression, i, &compiler.CompileError{Kind: compiler.ErrorUnsupported, Expression: i, Position: 0, Message: err.Error()}, err)
				}
			}
		}
		if rule.ext == nil && flags&(CompileCaseless|CompileUTF8|CompileUCP|CompileMultiline|CompileDotAll) == 0 && !rule.info.Stateful && !rule.info.LBR {
			if specialized, compileErr := nfa.CompileAuto(ng); compileErr == nil && specialized.SupportsGraph(ng) {
				backendBytes := specialized.MemoryBytes()
				if limits.MemoryBytes == 0 || totalUsage.MemoryBytes <= limits.MemoryBytes && backendBytes <= limits.MemoryBytes-totalUsage.MemoryBytes {
					rule.nfaEngine = specialized
					totalUsage.MemoryBytes = satAdd64(totalUsage.MemoryBytes, backendBytes)
				} else {
					info.SetBailout("nfa-memory")
				}
			} else if compileErr != nil {
				info.SetBailout("nfa-compile")
			} else {
				info.SetBailout("nfa-unsupported")
			}
		}
		// 后端选择可能在资源或布局校验后补充 bailout；将最终元数据
		// 写回规则，保证调用方观察到的选择原因与实际执行一致。
		rule.info = info
		rules = append(rules, rule)
	}
	knownIDs := make(map[uint32]struct{}, len(rules))
	ruleByID := make(map[uint32]compiledRule, len(rules))
	dependencies := make(map[uint32][]uint32)
	for _, rule := range rules {
		knownIDs[rule.id] = struct{}{}
		ruleByID[rule.id] = rule
		if rule.comb != nil {
			dependencies[rule.id] = combination.Dependencies(rule.comb)
		}
	}
	for i, rule := range rules {
		if rule.comb == nil {
			continue
		}
		for _, id := range combination.IDs(rule.comb) {
			if _, ok := knownIDs[id]; !ok {
				return nil, fmt.Errorf("%w: expression %d: %w", ErrInvalidCombination, i, &compiler.CompileError{Kind: compiler.ErrorInvalidReference, Expression: i, Message: fmt.Sprintf("combination references unknown expression %d", id)})
			}
			target := ruleByID[id]
			if target.comb != nil {
				return nil, fmt.Errorf("%w: expression %d: %w", ErrInvalidCombination, i, &compiler.CompileError{Kind: compiler.ErrorInvalidReference, Expression: i, Message: fmt.Sprintf("combination references another combination %d", id)})
			}
			if target.flags&(CompileSOMLeftmost|CompilePrefilter) != 0 {
				return nil, fmt.Errorf("%w: expression %d: %w", ErrInvalidCombination, i, &compiler.CompileError{Kind: compiler.ErrorInvalidReference, Expression: i, Message: fmt.Sprintf("combination operand %d uses unsupported flags", id)})
			}
		}
	}
	if err := combination.ValidateDependencies(dependencies); err != nil {
		return nil, fmt.Errorf("%w: %w: %w", ErrInvalidCombination, &compiler.CompileError{Kind: compiler.ErrorInvalidReference, Expression: -1, Message: err.Error()}, err)
	}
	scanner := newScanner(rules)
	scanner.usage = totalUsage
	return scanner, nil
}

func asciiBytes(data []byte) bool {
	for _, b := range data {
		if b >= utf8.RuneSelf {
			return false
		}
	}
	return true
}

func applyExpressionAttributes(info *compiler.ExpressionInfo, flags CompileFlag, ext *ExpressionExt) {
	if info == nil {
		return
	}
	var extFlags uint64
	var minOffset, maxOffset, minLength uint64
	var editDistance, hammingDistance uint32
	if ext != nil {
		extFlags = uint64(ext.Flags)
		minOffset, maxOffset, minLength = ext.MinOffset, ext.MaxOffset, ext.MinLength
		editDistance, hammingDistance = ext.EditDistance, ext.HammingDistance
	}
	info.ApplyCompileAttributes(uint32(flags), extFlags, minOffset, maxOffset, minLength, editDistance, hammingDistance)
}

// consumeLeadingInlineFlags 处理表达式开头的全局模式修饰符。
func consumeLeadingInlineFlags(pattern string, flags CompileFlag) (string, CompileFlag, bool, error) {
	extended := false
	for strings.HasPrefix(pattern, "(?") {
		closeIdx := strings.IndexByte(pattern, ')')
		if closeIdx < 0 {
			break
		}
		body := pattern[2:closeIdx]
		if body == "" || strings.ContainsAny(body, ":<>=!'(") {
			break
		}
		enable := true
		valid := true
		changed := false
		for _, value := range body {
			if value == 'x' {
				changed = true
				extended = enable
				continue
			}
			var flag CompileFlag
			switch value {
			case '-':
				enable = false
				continue
			case 'i':
				flag = CompileCaseless
			case 's':
				flag = CompileDotAll
			case 'm':
				flag = CompileMultiline
			default:
				valid = false
				continue
			}
			changed = true
			if enable {
				flags |= flag
			} else {
				flags &^= flag
			}
		}
		if !changed {
			break
		}
		if !valid {
			return "", flags, extended, fmt.Errorf("unsupported inline flag")
		}
		pattern = pattern[closeIdx+1:]
	}
	return pattern, flags, extended, nil
}

func satAdd64(a, b uint64) uint64 {
	if ^uint64(0)-a < b {
		return ^uint64(0)
	}
	return a + b
}

func satMul64(a, b uint64) uint64 {
	if a == 0 || b == 0 {
		return 0
	}
	if a > ^uint64(0)/b {
		return ^uint64(0)
	}
	return a * b
}

func engineStateCount(p *engine.Program) int {
	if p == nil {
		return 0
	}
	if p.NFA != nil {
		return len(p.NFA.Graph.Nodes)
	}
	if p.DFA != nil {
		return p.DFA.StateCount()
	}
	return 0
}

func literalPrefix(n parser.Node) ([]byte, bool) {
	switch v := n.(type) {
	case parser.Literal:
		return append([]byte(nil), v.Value...), len(v.Value) > 0
	case parser.Group:
		return literalPrefix(v.Child)
	case parser.Sequence:
		prefix := make([]byte, 0)
		for _, element := range v.Elements {
			if whole, ok := literalWhole(element); ok {
				prefix = append(prefix, whole...)
				continue
			}
			part, ok := literalPrefix(element)
			if ok {
				prefix = append(prefix, part...)
			}
			break
		}
		return prefix, len(prefix) > 0
	case parser.Alternation:
		if len(v.Options) == 0 {
			return nil, false
		}
		first, ok := literalPrefix(v.Options[0])
		if !ok || len(first) == 0 {
			return nil, false
		}
		width := len(first)
		for _, option := range v.Options[1:] {
			part, partOK := literalPrefix(option)
			if !partOK {
				return nil, false
			}
			if len(part) < width {
				width = len(part)
			}
			for i := 0; i < width; i++ {
				if first[i] != part[i] {
					width = i
					break
				}
			}
			if width == 0 {
				return nil, false
			}
		}
		return append([]byte(nil), first[:width]...), true
	case parser.Repeat:
		if v.Min > 0 {
			return literalPrefix(v.Child)
		}
	default:
	}
	return nil, false
}

// literalWhole 仅在节点必然消费同一串字节时返回完整文字。
func literalWhole(n parser.Node) ([]byte, bool) {
	switch v := n.(type) {
	case parser.Literal:
		return append([]byte(nil), v.Value...), len(v.Value) > 0
	case parser.Group:
		if v.HasScopedFlags() || v.Atomic {
			return nil, false
		}
		return literalWhole(v.Child)
	case parser.Sequence:
		out := make([]byte, 0)
		for _, child := range v.Elements {
			part, ok := literalWhole(child)
			if !ok {
				return nil, false
			}
			out = append(out, part...)
		}
		return out, len(out) > 0
	case parser.Repeat:
		if v.Max < 0 || v.Min != v.Max || v.Min == 0 {
			return nil, false
		}
		part, ok := literalWhole(v.Child)
		if !ok {
			return nil, false
		}
		out := make([]byte, 0, len(part)*v.Min)
		for i := 0; i < v.Min; i++ {
			out = append(out, part...)
		}
		return out, true
	default:
	}
	return nil, false
}

func repeatedLiteralPattern(n parser.Node) ([]byte, int, int, bool, bool) {
	switch value := n.(type) {
	case parser.Group:
		return repeatedLiteralPattern(value.Child)
	case parser.Repeat:
		unit, ok := literalPattern(value.Child)
		return unit, value.Min, value.Max, value.Greedy, ok && len(unit) > 0
	default:
		return nil, 0, 0, false, false
	}
}

// mapEngineKindToSentinel 根据 engine.CompileAutoWithFeatures 报告的 [compiler.ErrorKind]
// 映射到对应的公开哨兵错误，仅用于编译期错误出口，不参与业务逻辑判断。
func mapEngineKindToSentinel(kind compiler.ErrorKind) error {
	switch kind {
	case compiler.ErrorResourceLimit:
		return ErrRegexTooComplex
	case compiler.ErrorUnsupported:
		return ErrUnsupportedExpression
	default:
		return ErrInvalidExpression
	}
}
