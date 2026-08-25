package scankit

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

const unicodePropertyUnbounded = ^uint32(0)

const maxUnicodeApproximateAtoms = 256

// unicodePOSIXClass 表示启用 UTF8|UCP 时 POSIX 字符类使用的 Unicode 语义。它不复用
// byteClass：ASCII 专用的 POSIX 类和 Unicode POSIX 类在 digit、word、space 等定义上不同。
type unicodePOSIXClass uint8

const (
	unicodePOSIXNone unicodePOSIXClass = iota
	unicodePOSIXAlnum
	unicodePOSIXAlpha
	unicodePOSIXASCII
	unicodePOSIXBlank
	unicodePOSIXCntrl
	unicodePOSIXDigit
	unicodePOSIXGraph
	unicodePOSIXLower
	unicodePOSIXPrint
	unicodePOSIXPunct
	unicodePOSIXSpace
	unicodePOSIXUpper
	unicodePOSIXWord
	unicodePOSIXXDigit
)

// unicodeGraphApproximateContext 是字节 NFA 编辑乘积的有界 rune 图版本。状态保存当前已解码
// rune 边界处的最小编辑代价；同一状态的更大代价会被支配。
type unicodeGraphApproximateContext struct {
	current     []uint16
	next        []uint16
	editCurrent []uint16
	editNext    []uint16
	queue       []uint16
	queued      []bool
	distance    uint16
}

func newUnicodeGraphApproximateContext(graph *unicodePropertyGraph, distance uint32) unicodeGraphApproximateContext {
	ctx := unicodeGraphApproximateContext{
		current:     make([]uint16, len(graph.states)),
		next:        make([]uint16, len(graph.states)),
		editCurrent: make([]uint16, 0, len(graph.states)),
		editNext:    make([]uint16, 0, len(graph.states)),
		queue:       make([]uint16, len(graph.states)),
		queued:      make([]bool, len(graph.states)),
		distance:    uint16(distance),
	}
	ctx.resetSlice(ctx.current)
	ctx.resetSlice(ctx.next)
	return ctx
}

// unicodePropertyGraphWidthRange 仅接受有限且非空 rune 宽度范围的无环无断言图。UCP \R
// 因 CRLF 跨两个 rune 而可变宽；断言需要 rune 编辑乘积不保留的周边字节 ctx。汉明距离调用方
// 还要求返回宽度相等，编辑距离可使用完整范围。
func unicodePropertyGraphWidthRange(graph *unicodePropertyGraph) (int, int, bool) {
	if graph == nil || len(graph.states) == 0 || len(graph.states) > maxCachedClosureStates {
		return 0, 0, false
	}
	type width struct {
		minimum int
		maximum int
		valid   bool
	}
	memo := make([]width, len(graph.states))
	visiting := make([]bool, len(graph.states))
	var visit func(uint16) width
	visit = func(index uint16) width {
		if memo[index].valid {
			return memo[index]
		}
		if visiting[index] {
			return width{}
		}
		visiting[index] = true
		defer func() { visiting[index] = false }()
		state := graph.states[index]
		if state.accept {
			memo[index] = width{valid: true}
			return memo[index]
		}
		if state.hasAssertion || state.lineBreak || state.lineBreakCRWait {
			return width{}
		}
		minimum, maximum := maxUnicodeApproximateAtoms+1, -1
		invalid := false
		add := func(next uint16, consumed int) {
			result := visit(next)
			if !result.valid {
				invalid = true
				return
			}
			minimum = min(minimum, result.minimum+consumed)
			maximum = max(maximum, result.maximum+consumed)
		}
		for _, next := range state.epsilon {
			add(next, 0)
		}
		if state.hasAtom {
			add(state.next, 1)
		}
		if invalid || maximum < 0 || maximum > maxUnicodeApproximateAtoms {
			return width{}
		}
		memo[index] = width{minimum: minimum, maximum: maximum, valid: true}
		return memo[index]
	}
	result := visit(graph.start)
	return result.minimum, result.maximum, result.valid && result.minimum != 0
}

func (ctx *unicodeGraphApproximateContext) matchesHamming(graph *unicodePropertyGraph, values []unicodePropertyRune) bool {
	if len(values) == 0 || len(ctx.current) != len(graph.states) {
		return false
	}
	ctx.reset()
	ctx.current[graph.start] = 0
	ctx.expandZero(graph, ctx.current)
	for _, value := range values {
		ctx.resetSlice(ctx.next)
		for index, cost := range ctx.current {
			if cost > ctx.distance || !graph.states[index].hasAtom {
				continue
			}
			nextCost := cost
			if !graph.states[index].atom.matches(value.value) {
				nextCost++
			}
			if nextCost <= ctx.distance && nextCost < ctx.next[graph.states[index].next] {
				ctx.next[graph.states[index].next] = nextCost
			}
		}
		ctx.expandZero(graph, ctx.next)
		ctx.current, ctx.next = ctx.next, ctx.current
	}
	return ctx.accepts(graph)
}

func (ctx *unicodeGraphApproximateContext) matchesEdit(graph *unicodePropertyGraph, values []unicodePropertyRune) bool {
	if len(values) == 0 || len(ctx.current) != len(graph.states) {
		return false
	}
	ctx.clearEditActive(ctx.current, &ctx.editCurrent)
	ctx.clearEditActive(ctx.next, &ctx.editNext)
	ctx.setEditCost(ctx.current, &ctx.editCurrent, graph.start, 0)
	ctx.expandEditSparse(graph, ctx.current, &ctx.editCurrent)
	for _, value := range values {
		ctx.clearEditActive(ctx.next, &ctx.editNext)
		for _, index := range ctx.editCurrent {
			cost := ctx.current[index]
			if cost < ctx.distance {
				ctx.setEditCost(ctx.next, &ctx.editNext, index, cost+1)
			}
			state := graph.states[index]
			if !state.hasAtom {
				continue
			}
			nextCost := cost
			if !state.atom.matches(value.value) {
				nextCost++
			}
			if nextCost <= ctx.distance {
				ctx.setEditCost(ctx.next, &ctx.editNext, state.next, nextCost)
			}
		}
		ctx.expandEditSparse(graph, ctx.next, &ctx.editNext)
		ctx.current, ctx.next = ctx.next, ctx.current
		ctx.editCurrent, ctx.editNext = ctx.editNext, ctx.editCurrent
	}
	for _, index := range ctx.editCurrent {
		if graph.states[index].accept && ctx.current[index] <= ctx.distance {
			return true
		}
	}
	return false
}

func (ctx *unicodeGraphApproximateContext) reset() {
	ctx.resetSlice(ctx.current)
	ctx.resetSlice(ctx.next)
}

func (ctx *unicodeGraphApproximateContext) resetSlice(costs []uint16) {
	for index := range costs {
		costs[index] = ctx.distance + 1
	}
}

func (ctx *unicodeGraphApproximateContext) clearEditActive(costs []uint16, active *[]uint16) {
	for _, index := range *active {
		costs[index] = ctx.distance + 1
	}
	*active = (*active)[:0]
}

func (ctx *unicodeGraphApproximateContext) setEditCost(costs []uint16, active *[]uint16, state uint16, candidate uint16) {
	if candidate >= costs[state] {
		return
	}
	if costs[state] > ctx.distance {
		*active = append(*active, state)
	}
	costs[state] = candidate
}

func (ctx *unicodeGraphApproximateContext) accepts(graph *unicodePropertyGraph) bool {
	for index, cost := range ctx.current {
		if graph.states[index].accept && cost <= ctx.distance {
			return true
		}
	}
	return false
}

func (ctx *unicodeGraphApproximateContext) expandZero(graph *unicodePropertyGraph, costs []uint16) {
	ctx.expand(graph, costs, false)
}

func (ctx *unicodeGraphApproximateContext) expandEdit(graph *unicodePropertyGraph, costs []uint16) {
	ctx.expand(graph, costs, true)
}

func (ctx *unicodeGraphApproximateContext) expandEditSparse(graph *unicodePropertyGraph, costs []uint16, active *[]uint16) {
	clear(ctx.queued)
	head, tail, count := 0, 0, 0
	push := func(state uint16) {
		if ctx.queued[state] {
			return
		}
		ctx.queue[tail] = state
		tail = (tail + 1) % len(ctx.queue)
		count++
		ctx.queued[state] = true
	}
	for _, state := range *active {
		push(state)
	}
	for count != 0 {
		index := ctx.queue[head]
		head = (head + 1) % len(ctx.queue)
		count--
		ctx.queued[index] = false
		cost := costs[index]
		state := graph.states[index]
		relax := func(next uint16, candidate uint16) {
			if candidate > ctx.distance {
				return
			}
			previous := costs[next]
			ctx.setEditCost(costs, active, next, candidate)
			if candidate < previous {
				push(next)
			}
		}
		for _, next := range state.epsilon {
			relax(next, cost)
		}
		if state.hasAtom {
			relax(state.next, cost+1)
		}
	}
}

func (ctx *unicodeGraphApproximateContext) expand(graph *unicodePropertyGraph, costs []uint16, deletions bool) {
	clear(ctx.queued)
	head, tail, count := 0, 0, 0
	push := func(state uint16) {
		if ctx.queued[state] {
			return
		}
		ctx.queue[tail] = state
		tail = (tail + 1) % len(ctx.queue)
		count++
		ctx.queued[state] = true
	}
	for state, cost := range costs {
		if cost <= ctx.distance {
			push(uint16(state))
		}
	}
	for count != 0 {
		index := ctx.queue[head]
		head = (head + 1) % len(ctx.queue)
		count--
		ctx.queued[index] = false
		cost := costs[index]
		state := graph.states[index]
		relax := func(next uint16, candidate uint16) {
			if candidate > ctx.distance || candidate >= costs[next] {
				return
			}
			costs[next] = candidate
			push(next)
		}
		for _, next := range state.epsilon {
			relax(next, cost)
		}
		if deletions && state.hasAtom {
			relax(state.next, cost+1)
		}
	}
}

func compileUnicodePropertyPrograms(expressionIndex uint32, pattern string, flags CompileFlag) ([]unicodePropertyProgram, error) {
	// 局部修饰符与 Unicode 点号/多行语义需要标志附着在单个图状态上。普通 UCP 规则保留
	// 紧凑的原子/序列执行器，但这些语义可观察时选择 rune 图。
	if unicodePropertyPatternHasInlineFlags(pattern) || flags&(CompileDotAll|CompileMultiline) != 0 || strings.Contains(pattern, ".") || strings.Contains(pattern, `\Q`) || strings.Contains(pattern, `\R`) {
		program, err := compileUnicodePropertyGraphProgramWithFlags(expressionIndex, pattern, flags)
		if err != nil {
			return nil, err
		}
		program.flagsApplied = true
		return []unicodePropertyProgram{program}, nil
	}
	anchorStart, anchorEnd := strings.HasPrefix(pattern, "^"), strings.HasSuffix(pattern, "$")
	wordStart, wordEnd := strings.HasPrefix(pattern, `\b`), strings.HasSuffix(pattern, `\b`)
	if anchorStart && len(pattern) >= 1 {
		pattern = pattern[1:]
	}
	if wordStart && len(pattern) >= 2 {
		pattern = pattern[2:]
	}
	if anchorEnd && len(pattern) >= 1 {
		pattern = pattern[:len(pattern)-1]
	}
	if wordEnd && len(pattern) >= 2 {
		pattern = pattern[:len(pattern)-2]
	} else if wordEnd {
		// \b 等单令牌表达式同时是词法前缀和后缀，仅包含一个断言而非两个。
		wordEnd = false
	}
	if strings.HasPrefix(pattern, `\A`) {
		anchorStart = true
		pattern = pattern[2:]
	}
	if strings.HasSuffix(pattern, `\z`) {
		anchorEnd = true
		pattern = pattern[:len(pattern)-2]
	}
	if pattern == "" {
		assertions := ""
		if anchorStart {
			assertions += "^"
		}
		if wordStart {
			assertions += `\b`
		}
		if anchorEnd {
			assertions += "$"
		}
		if wordEnd {
			assertions += `\b`
		}
		if assertions == "" {
			return nil, fmt.Errorf("empty anchored Unicode property expression")
		}
		program, err := compileUnicodePropertyGraphProgram(expressionIndex, assertions)
		if err != nil {
			return nil, err
		}
		return []unicodePropertyProgram{program}, nil
	}
	// 分组以 rune Thompson 图表示而非展开为字符串。这使嵌套复杂度相对表达式大小保持线性，
	// 并使 (a|b)+ 等分组量词可执行。
	if unicodePropertyPatternHasGroup(pattern) || unicodePropertyPatternHasZeroWidth(pattern) {
		program, err := compileUnicodePropertyGraphProgram(expressionIndex, pattern)
		if err != nil {
			return nil, err
		}
		program.anchorStart = anchorStart
		program.anchorEnd = anchorEnd
		program.wordStart = wordStart
		program.wordEnd = wordEnd
		return []unicodePropertyProgram{program}, nil
	}
	expanded, err := expandUnicodePropertyGroups(pattern)
	if err != nil {
		return nil, err
	}
	programs := make([]unicodePropertyProgram, 0, len(expanded))
	for _, expression := range expanded {
		branches, err := splitUnicodePropertyAlternatives(expression)
		if err != nil {
			return nil, err
		}
		for _, branch := range branches {
			program, err := compileUnicodePropertyProgram(expressionIndex, branch)
			if err != nil {
				return nil, err
			}
			programs = append(programs, program)
			programs[len(programs)-1].anchorStart = anchorStart
			programs[len(programs)-1].anchorEnd = anchorEnd
			programs[len(programs)-1].wordStart = wordStart
			programs[len(programs)-1].wordEnd = wordEnd
			if anchorStart || anchorEnd || wordStart || wordEnd {
				programs[len(programs)-1].sequence = append([]unicodePropertyAtom(nil), program.sequence...)
				if len(programs[len(programs)-1].sequence) == 0 {
					programs[len(programs)-1].sequence = []unicodePropertyAtom{program.atom}
				}
				programs[len(programs)-1].runeNFA = true
			}
		}
	}
	return programs, nil
}

// unicodePropertyPatternHasInlineFlags 查找真实的 '(?' 分隔符，跳过引用转义和字符类。
// 它刻意保守：误报仅会选择等价的 rune 图执行器。
func unicodePropertyPatternHasInlineFlags(pattern string) bool {
	inClass := false
	for index := 0; index < len(pattern); index++ {
		switch pattern[index] {
		case '\\':
			if index+1 < len(pattern) {
				index++
			}
		case '[':
			inClass = true
		case ']':
			inClass = false
		case '(':
			if !inClass && index+1 < len(pattern) && pattern[index+1] == '?' {
				return true
			}
		}
	}
	return false
}

// unicodePropertyPatternHasGroup 在跳过转义字节和字符类时查找分组起始分隔符，对应 UCP
// 解析器接受的子集。
func unicodePropertyPatternHasGroup(pattern string) bool {
	class := false
	for index := 0; index < len(pattern); index++ {
		switch pattern[index] {
		case '\\':
			if index+1 < len(pattern) {
				index++
			}
		case '[':
			class = true
		case ']':
			class = false
		case '(':
			if !class {
				return true
			}
		}
	}
	return false
}

// unicodePropertyPatternHasZeroWidth 报告需要 ctx 感知 rune 图的断言。末尾 ^、$ 和 \b
// 已在上方剥离，继续使用低开销过滤路径。
func unicodePropertyPatternHasZeroWidth(pattern string) bool {
	class := false
	for index := 0; index < len(pattern); index++ {
		switch pattern[index] {
		case '\\':
			if index+1 >= len(pattern) {
				continue
			}
			if !class && strings.ContainsRune("AzZbB", rune(pattern[index+1])) {
				return true
			}
			index++
		case '[':
			class = true
		case ']':
			class = false
		case '^', '$':
			if !class {
				return true
			}
		}
	}
	return false
}

const maxUnicodeGraphStates = 1_024

type unicodePropertyNodeKind uint8

const (
	unicodePropertyNodeAtom unicodePropertyNodeKind = iota
	unicodePropertyNodeConcat
	unicodePropertyNodeAlternate
	unicodePropertyNodeRepeat
	unicodePropertyNodeAssertion
	unicodePropertyNodeLineBreak
	unicodePropertyNodeEmpty
)

type unicodePropertyNode struct {
	kind      unicodePropertyNodeKind
	atom      unicodePropertyAtom
	assertion unicodePropertyAssertion
	children  []*unicodePropertyNode
	minimum   uint32
	maximum   uint32
	flags     CompileFlag
}

// unicodePropertyParser 刻意保持精简：它接受本包已实现的 UCP 原子子集，并增加递归分组及
// 其量词。未支持的结构会在编译期失败，不会静默改变语义。
type unicodePropertyParser struct {
	pattern  string
	position int
	flags    CompileFlag
}

func compileUnicodePropertyGraphProgram(expressionIndex uint32, pattern string) (unicodePropertyProgram, error) {
	program, err := compileUnicodePropertyGraphProgramWithFlags(expressionIndex, pattern, 0)
	if err != nil {
		return unicodePropertyProgram{}, err
	}
	// 兼容图入口将表达式级标志留给调用方处理，保留紧凑编译器既有的 CASELESS 行为。
	program.flagsApplied = false
	return program, nil
}

func compileUnicodePropertyGraphProgramWithFlags(expressionIndex uint32, pattern string, flags CompileFlag) (unicodePropertyProgram, error) {
	parser := unicodePropertyParser{pattern: pattern, flags: flags}
	root, err := parser.parseAlternate()
	if err != nil {
		return unicodePropertyProgram{}, err
	}
	if parser.position != len(pattern) {
		return unicodePropertyProgram{}, fmt.Errorf("unexpected Unicode regular-expression token %q", pattern[parser.position])
	}
	builder := unicodePropertyGraphBuilder{}
	fragment, err := builder.compile(root)
	if err != nil {
		return unicodePropertyProgram{}, err
	}
	if int(fragment.end) >= len(builder.graph.states) {
		return unicodePropertyProgram{}, fmt.Errorf("invalid Unicode graph accept state")
	}
	builder.graph.states[fragment.end].accept = true
	if err := builder.graph.buildClosures(); err != nil {
		return unicodePropertyProgram{}, err
	}
	nullable := builder.graph.reachesAccept(builder.graph.start)
	return unicodePropertyProgram{
		expressionIndex: expressionIndex,
		graph:           &builder.graph,
		runeNFA:         true,
		nullable:        nullable,
		hasAlternation:  unicodePropertyNodeHasAlternation(root),
		hasAssertions:   unicodePropertyNodeHasAssertion(root),
		flagsApplied:    true,
	}, nil
}

func (parser *unicodePropertyParser) parseAlternate() (*unicodePropertyNode, error) {
	first, err := parser.parseConcat()
	if err != nil {
		return nil, err
	}
	children := []*unicodePropertyNode{first}
	for {
		parser.skipExtendedWhitespace()
		if parser.position == len(parser.pattern) || parser.pattern[parser.position] != '|' {
			break
		}
		parser.position++
		next, err := parser.parseConcat()
		if err != nil {
			return nil, err
		}
		children = append(children, next)
	}
	if len(children) == 1 {
		return first, nil
	}
	return &unicodePropertyNode{kind: unicodePropertyNodeAlternate, children: children}, nil
}

func (parser *unicodePropertyParser) parseConcat() (*unicodePropertyNode, error) {
	parser.skipExtendedWhitespace()
	children := make([]*unicodePropertyNode, 0, 2)
	for {
		parser.skipExtendedWhitespace()
		if parser.position == len(parser.pattern) || parser.pattern[parser.position] == ')' || parser.pattern[parser.position] == '|' {
			break
		}
		node, err := parser.parseRepeat()
		if err != nil {
			return nil, err
		}
		children = append(children, node)
	}
	if len(children) == 0 {
		return &unicodePropertyNode{kind: unicodePropertyNodeEmpty, flags: parser.flags}, nil
	}
	if len(children) == 1 {
		return children[0], nil
	}
	return &unicodePropertyNode{kind: unicodePropertyNodeConcat, children: children}, nil
}

func (parser *unicodePropertyParser) parseRepeat() (*unicodePropertyNode, error) {
	node, err := parser.parseAtom()
	if err != nil {
		return nil, err
	}
	parser.skipExtendedWhitespace()
	if parser.position == len(parser.pattern) {
		return node, nil
	}
	quantifierStart := parser.position
	quantifierEnd := quantifierStart
	switch parser.pattern[quantifierStart] {
	case '+', '*', '?':
		quantifierEnd++
	case '{':
		quantifierParser := regexParser{pattern: parser.pattern[quantifierStart:]}
		if _, _, quantified, err := quantifierParser.parseBraceQuantifier(); err != nil || !quantified {
			return nil, fmt.Errorf("invalid Unicode property repeat")
		}
		quantifierEnd += quantifierParser.pos
	default:
		return node, nil
	}
	minimum, maximum, err := parseUnicodePropertyQuantifier(parser.pattern[quantifierStart:quantifierEnd])
	if err != nil {
		return nil, err
	}
	parser.position = quantifierEnd
	if parser.position < len(parser.pattern) && parser.pattern[parser.position] == '?' {
		// 懒惰语法仅影响优先级；图会报告完整匹配集合，因此其接受语言与贪婪重复相同。
		parser.position++
	}
	if parser.position < len(parser.pattern) && isQuantifierStart(parser.pattern[parser.position]) {
		return nil, fmt.Errorf("multiple Unicode property repeat operators")
	}
	return &unicodePropertyNode{kind: unicodePropertyNodeRepeat, children: []*unicodePropertyNode{node}, minimum: minimum, maximum: maximum, flags: parser.flags}, nil
}

func (parser *unicodePropertyParser) parseAtom() (*unicodePropertyNode, error) {
	if parser.position == len(parser.pattern) {
		return nil, fmt.Errorf("expected Unicode property atom")
	}
	if parser.pattern[parser.position] == '(' {
		groupFlags := parser.flags
		parser.position++
		if parser.position < len(parser.pattern) && parser.pattern[parser.position] == '?' {
			parser.position++
			if parser.position < len(parser.pattern) && parser.pattern[parser.position] == ':' {
				parser.position++
			} else if parser.position < len(parser.pattern) && parser.pattern[parser.position] == '<' {
				parser.position++
				if err := parser.consumeGroupName('>'); err != nil {
					return nil, err
				}
			} else if parser.position+1 < len(parser.pattern) && parser.pattern[parser.position] == 'P' && parser.pattern[parser.position+1] == '<' {
				parser.position += 2
				if err := parser.consumeGroupName('>'); err != nil {
					return nil, err
				}
			} else if parser.position < len(parser.pattern) && parser.pattern[parser.position] == '#' {
				commentStart := parser.position
				for parser.position < len(parser.pattern) && parser.pattern[parser.position] != ')' {
					parser.position++
				}
				if parser.position == len(parser.pattern) {
					return nil, fmt.Errorf("unterminated Unicode comment group at byte %d", commentStart)
				}
				parser.position++
				return &unicodePropertyNode{kind: unicodePropertyNodeEmpty, flags: parser.flags}, nil
			} else {
				return parser.parseInlineFlagGroup()
			}
		}
		node, err := parser.parseAlternate()
		if err != nil {
			return nil, err
		}
		if parser.position == len(parser.pattern) || parser.pattern[parser.position] != ')' {
			return nil, fmt.Errorf("unbalanced Unicode group")
		}
		parser.position++
		parser.flags = groupFlags
		return node, nil
	}
	if assertion, width, ok := unicodePropertyAssertionAt(parser.pattern, parser.position); ok {
		parser.position += width
		return &unicodePropertyNode{kind: unicodePropertyNodeAssertion, assertion: assertion, flags: parser.flags}, nil
	}
	if strings.HasPrefix(parser.pattern[parser.position:], `\Q`) {
		return parser.parseQuotedLiteral()
	}
	if strings.HasPrefix(parser.pattern[parser.position:], `\R`) {
		parser.position += 2
		return &unicodePropertyNode{kind: unicodePropertyNodeLineBreak, flags: parser.flags}, nil
	}
	if strings.HasPrefix(parser.pattern[parser.position:], `\N`) {
		parser.position += 2
		if parser.position < len(parser.pattern) && parser.pattern[parser.position] == '{' {
			return nil, fmt.Errorf("named character escapes are not supported")
		}
		flags := parser.flags &^ CompileDotAll
		return &unicodePropertyNode{kind: unicodePropertyNodeAtom, atom: unicodeDotAtom(flags), flags: flags}, nil
	}
	if parser.pattern[parser.position] == '.' {
		parser.position++
		return &unicodePropertyNode{kind: unicodePropertyNodeAtom, atom: unicodeDotAtom(parser.flags), flags: parser.flags}, nil
	}
	atom, next, err := parseUnicodePropertyAtom(parser.pattern, parser.position)
	if err != nil {
		return nil, err
	}
	parser.position = next
	unicodeApplyAtomFlags(&atom, parser.flags)
	return &unicodePropertyNode{kind: unicodePropertyNodeAtom, atom: atom, flags: parser.flags}, nil
}

func (parser *unicodePropertyParser) parseQuotedLiteral() (*unicodePropertyNode, error) {
	start := parser.position
	parser.position += 2 // \\Q
	children := make([]*unicodePropertyNode, 0, 4)
	for parser.position < len(parser.pattern) && !strings.HasPrefix(parser.pattern[parser.position:], `\E`) {
		value, width := utf8.DecodeRuneInString(parser.pattern[parser.position:])
		if value == utf8.RuneError && width == 1 {
			return nil, fmt.Errorf("invalid UTF-8 quoted Unicode literal at byte %d", parser.position)
		}
		atom := unicodePropertyAtom{matchers: []unicodePropertyMatcher{{literal: value, isLiteral: true}}}
		unicodeApplyAtomFlags(&atom, parser.flags)
		children = append(children, &unicodePropertyNode{kind: unicodePropertyNodeAtom, atom: atom, flags: parser.flags})
		parser.position += width
	}
	if !strings.HasPrefix(parser.pattern[parser.position:], `\E`) {
		return nil, fmt.Errorf("unterminated Unicode quoted literal at byte %d", start)
	}
	parser.position += 2
	if len(children) == 0 {
		return &unicodePropertyNode{kind: unicodePropertyNodeEmpty, flags: parser.flags}, nil
	}
	if len(children) == 1 {
		return children[0], nil
	}
	return &unicodePropertyNode{kind: unicodePropertyNodeConcat, children: children, flags: parser.flags}, nil
}

func (parser *unicodePropertyParser) consumeGroupName(terminator byte) error {
	start := parser.position
	for parser.position < len(parser.pattern) && parser.pattern[parser.position] != terminator {
		value := parser.pattern[parser.position]
		if !(value == '_' || value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z' || value >= '0' && value <= '9' && parser.position > start) {
			return fmt.Errorf("invalid Unicode group name at byte %d", parser.position)
		}
		parser.position++
	}
	if start == parser.position || parser.position == len(parser.pattern) {
		return fmt.Errorf("unterminated Unicode group name")
	}
	parser.position++
	return nil
}

func (parser *unicodePropertyParser) parseInlineFlagGroup() (*unicodePropertyNode, error) {
	set, unset, ok, err := parser.parseInlineFlags()
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("unsupported Unicode group extension")
	}
	flags := parser.flags | set
	flags &^= unset
	if parser.position < len(parser.pattern) && parser.pattern[parser.position] == ':' {
		parser.position++
		outer := parser.flags
		parser.flags = flags
		node, err := parser.parseAlternate()
		parser.flags = outer
		if err != nil {
			return nil, err
		}
		if parser.position == len(parser.pattern) || parser.pattern[parser.position] != ')' {
			return nil, fmt.Errorf("expected ')' after Unicode inline flags")
		}
		parser.position++
		return node, nil
	}
	if parser.position == len(parser.pattern) || parser.pattern[parser.position] != ')' {
		return nil, fmt.Errorf("expected ':' or ')' after Unicode inline flags")
	}
	parser.position++
	parser.flags = flags
	return &unicodePropertyNode{kind: unicodePropertyNodeEmpty, flags: flags}, nil
}

func (parser *unicodePropertyParser) parseInlineFlags() (CompileFlag, CompileFlag, bool, error) {
	var set, unset CompileFlag
	clearing, seen, cleared := false, false, false
	for parser.position < len(parser.pattern) {
		value := parser.pattern[parser.position]
		if value == '-' {
			if clearing {
				return 0, 0, false, fmt.Errorf("duplicate '-' in Unicode inline flags")
			}
			clearing = true
			parser.position++
			continue
		}
		var flag CompileFlag
		switch value {
		case 'i':
			flag = CompileCaseless
		case 'm':
			flag = CompileMultiline
		case 's':
			flag = CompileDotAll
		case 'x':
			flag = regexInlineExtended
		default:
			if value == ':' || value == ')' {
				if !seen {
					return 0, 0, false, nil
				}
				if clearing && !cleared {
					return 0, 0, false, fmt.Errorf("unicode inline '-' must be followed by a flag")
				}
				return set, unset, true, nil
			}
			if !seen {
				return 0, 0, false, nil
			}
			return 0, 0, false, fmt.Errorf("unsupported Unicode inline flag %q", value)
		}
		seen = true
		parser.position++
		if clearing {
			if set&flag != 0 {
				return 0, 0, false, fmt.Errorf("unicode inline flag %q is both set and cleared", value)
			}
			unset |= flag
			cleared = true
		} else {
			if unset&flag != 0 {
				return 0, 0, false, fmt.Errorf("unicode inline flag %q is both set and cleared", value)
			}
			set |= flag
		}
	}
	return set, unset, seen, nil
}

func (parser *unicodePropertyParser) skipExtendedWhitespace() {
	if parser.flags&regexInlineExtended == 0 {
		return
	}
	for parser.position < len(parser.pattern) {
		switch parser.pattern[parser.position] {
		case ' ', '\t', '\n', '\r', '\f':
			parser.position++
		case '#':
			parser.position++
			for parser.position < len(parser.pattern) && parser.pattern[parser.position] != '\n' {
				parser.position++
			}
		default:
			return
		}
	}
}

func unicodeDotAtom(flags CompileFlag) unicodePropertyAtom {
	return unicodePropertyAtom{matchers: []unicodePropertyMatcher{{any: true, dotAll: flags&CompileDotAll != 0}}}
}

func unicodeApplyAtomFlags(atom *unicodePropertyAtom, flags CompileFlag) {
	if flags&CompileCaseless == 0 {
		return
	}
	for index := range atom.matchers {
		atom.matchers[index].caseless = true
	}
}

func unicodePropertyAssertionAt(pattern string, position int) (unicodePropertyAssertion, int, bool) {
	if position >= len(pattern) {
		return 0, 0, false
	}
	switch pattern[position] {
	case '^':
		return unicodePropertyAssertStart, 1, true
	case '$':
		return unicodePropertyAssertEnd, 1, true
	case '\\':
		if position+1 == len(pattern) {
			return 0, 0, false
		}
		switch pattern[position+1] {
		case 'A':
			return unicodePropertyAssertStart, 2, true
		case 'z':
			return unicodePropertyAssertEnd, 2, true
		case 'Z':
			return unicodePropertyAssertEndBeforeFinalNewline, 2, true
		case 'b':
			return unicodePropertyAssertWordBoundary, 2, true
		case 'B':
			return unicodePropertyAssertNotWordBoundary, 2, true
		}
	}
	return 0, 0, false
}

func unicodePropertyNodeHasAlternation(node *unicodePropertyNode) bool {
	if node.kind == unicodePropertyNodeAlternate {
		return true
	}
	for _, child := range node.children {
		if unicodePropertyNodeHasAlternation(child) {
			return true
		}
	}
	return false
}

func unicodePropertyNodeHasAssertion(node *unicodePropertyNode) bool {
	if node.kind == unicodePropertyNodeAssertion {
		return true
	}
	for _, child := range node.children {
		if unicodePropertyNodeHasAssertion(child) {
			return true
		}
	}
	return false
}

type unicodePropertyGraphFragment struct {
	start uint16
	end   uint16
}

type unicodePropertyGraphBuilder struct {
	graph unicodePropertyGraph
}

func (builder *unicodePropertyGraphBuilder) newState() (uint16, error) {
	if len(builder.graph.states) >= maxUnicodeGraphStates {
		return 0, ErrRegexTooComplex
	}
	state := uint16(len(builder.graph.states))
	builder.graph.states = append(builder.graph.states, unicodePropertyGraphState{})
	return state, nil
}

func (builder *unicodePropertyGraphBuilder) addEpsilon(from, to uint16) {
	builder.graph.states[from].epsilon = append(builder.graph.states[from].epsilon, to)
}

func (builder *unicodePropertyGraphBuilder) compile(node *unicodePropertyNode) (unicodePropertyGraphFragment, error) {
	switch node.kind {
	case unicodePropertyNodeAtom:
		start, err := builder.newState()
		if err != nil {
			return unicodePropertyGraphFragment{}, err
		}
		end, err := builder.newState()
		if err != nil {
			return unicodePropertyGraphFragment{}, err
		}
		builder.graph.states[start].atom = node.atom
		builder.graph.states[start].hasAtom = true
		builder.graph.states[start].next = end
		return unicodePropertyGraphFragment{start: start, end: end}, nil
	case unicodePropertyNodeAssertion:
		start, err := builder.newState()
		if err != nil {
			return unicodePropertyGraphFragment{}, err
		}
		end, err := builder.newState()
		if err != nil {
			return unicodePropertyGraphFragment{}, err
		}
		builder.graph.states[start].assertion = node.assertion
		builder.graph.states[start].hasAssertion = true
		builder.graph.states[start].multiline = node.flags&CompileMultiline != 0
		builder.graph.states[start].next = end
		return unicodePropertyGraphFragment{start: start, end: end}, nil
	case unicodePropertyNodeLineBreak:
		start, err := builder.newState()
		if err != nil {
			return unicodePropertyGraphFragment{}, err
		}
		end, err := builder.newState()
		if err != nil {
			return unicodePropertyGraphFragment{}, err
		}
		crWait, err := builder.newState()
		if err != nil {
			return unicodePropertyGraphFragment{}, err
		}
		builder.graph.states[start].lineBreak = true
		builder.graph.states[start].next = end
		builder.graph.states[start].lineBreakCRContinuation = crWait
		builder.graph.states[crWait].lineBreakCRWait = true
		builder.graph.states[crWait].next = end
		return unicodePropertyGraphFragment{start: start, end: end}, nil
	case unicodePropertyNodeEmpty:
		start, err := builder.newState()
		if err != nil {
			return unicodePropertyGraphFragment{}, err
		}
		end, err := builder.newState()
		if err != nil {
			return unicodePropertyGraphFragment{}, err
		}
		builder.addEpsilon(start, end)
		return unicodePropertyGraphFragment{start: start, end: end}, nil
	case unicodePropertyNodeConcat:
		first, err := builder.compile(node.children[0])
		if err != nil {
			return unicodePropertyGraphFragment{}, err
		}
		for _, child := range node.children[1:] {
			next, err := builder.compile(child)
			if err != nil {
				return unicodePropertyGraphFragment{}, err
			}
			builder.addEpsilon(first.end, next.start)
			first.end = next.end
		}
		return first, nil
	case unicodePropertyNodeAlternate:
		start, err := builder.newState()
		if err != nil {
			return unicodePropertyGraphFragment{}, err
		}
		end, err := builder.newState()
		if err != nil {
			return unicodePropertyGraphFragment{}, err
		}
		for _, child := range node.children {
			fragment, err := builder.compile(child)
			if err != nil {
				return unicodePropertyGraphFragment{}, err
			}
			builder.addEpsilon(start, fragment.start)
			builder.addEpsilon(fragment.end, end)
		}
		return unicodePropertyGraphFragment{start: start, end: end}, nil
	case unicodePropertyNodeRepeat:
		return builder.compileRepeat(node.children[0], node.minimum, node.maximum)
	default:
		return unicodePropertyGraphFragment{}, fmt.Errorf("unsupported Unicode graph node")
	}
}

func (builder *unicodePropertyGraphBuilder) compileRepeat(child *unicodePropertyNode, minimum, maximum uint32) (unicodePropertyGraphFragment, error) {
	start, err := builder.newState()
	if err != nil {
		return unicodePropertyGraphFragment{}, err
	}
	last := start
	for count := uint32(0); count < minimum; count++ {
		fragment, err := builder.compile(child)
		if err != nil {
			return unicodePropertyGraphFragment{}, err
		}
		builder.addEpsilon(last, fragment.start)
		last = fragment.end
	}
	end, err := builder.newState()
	if err != nil {
		return unicodePropertyGraphFragment{}, err
	}
	if maximum == minimum {
		builder.addEpsilon(last, end)
		return unicodePropertyGraphFragment{start: start, end: end}, nil
	}
	// 每个有限可选尾部均可在当前终点退出。
	builder.addEpsilon(last, end)
	if maximum == unicodePropertyUnbounded {
		fragment, err := builder.compile(child)
		if err != nil {
			return unicodePropertyGraphFragment{}, err
		}
		builder.addEpsilon(last, fragment.start)
		builder.addEpsilon(fragment.end, last)
		return unicodePropertyGraphFragment{start: start, end: end}, nil
	}
	for count := minimum; count < maximum; count++ {
		fragment, err := builder.compile(child)
		if err != nil {
			return unicodePropertyGraphFragment{}, err
		}
		builder.addEpsilon(last, fragment.start)
		last = fragment.end
		builder.addEpsilon(last, end)
	}
	return unicodePropertyGraphFragment{start: start, end: end}, nil
}

func (graph *unicodePropertyGraph) buildClosures() error {
	graph.closures = make([][]uint16, len(graph.states))
	graph.canConsume = make([]bool, len(graph.states))
	graph.accepts = make([]bool, len(graph.states))
	for start := range graph.states {
		seen := make([]bool, len(graph.states))
		closure := make([]uint16, 0, 4)
		stack := []uint16{uint16(start)}
		for len(stack) != 0 {
			index := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if seen[index] {
				continue
			}
			seen[index] = true
			closure = append(closure, index)
			if graph.states[index].hasAssertion {
				continue
			}
			for _, target := range graph.states[index].epsilon {
				stack = append(stack, target)
			}
		}
		if len(closure) > maxUnicodeGraphStates {
			return ErrRegexTooComplex
		}
		graph.closures[start] = closure
		for _, index := range closure {
			graph.canConsume[start] = graph.canConsume[start] || unicodePropertyGraphStateCanConsume(graph.states[index])
			graph.accepts[start] = graph.accepts[start] || graph.states[index].accept
		}
	}
	return nil
}

func (graph *unicodePropertyGraph) reachesAccept(state uint16) bool {
	seen := make([]bool, len(graph.states))
	stack := []uint16{state}
	for len(stack) != 0 {
		index := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if seen[index] {
			continue
		}
		seen[index] = true
		current := graph.states[index]
		if current.accept {
			return true
		}
		if unicodePropertyGraphStateCanConsume(current) {
			continue
		}
		if current.hasAssertion {
			stack = append(stack, current.next)
			continue
		}
		stack = append(stack, current.epsilon...)
	}
	return false
}

const maxUnicodeGroupExpansions = 256

func expandUnicodePropertyGroups(pattern string) ([]string, error) {
	class, depth, start := false, 0, -1
	for index := 0; index < len(pattern); index++ {
		switch pattern[index] {
		case '\\':
			if index+1 < len(pattern) {
				index++
			}
		case '[':
			class = true
		case ']':
			class = false
		case '(':
			if !class && depth == 0 {
				start = index
			}
			if !class {
				depth++
			}
		case ')':
			if class || depth == 0 {
				return nil, fmt.Errorf("unbalanced Unicode group")
			}
			depth--
			if depth != 0 {
				continue
			}
			end := index
			if end+1 < len(pattern) && strings.ContainsRune("+*?{", rune(pattern[end+1])) {
				return nil, fmt.Errorf("unicode group repeats are not yet supported")
			}
			content := pattern[start+1 : end]
			if strings.HasPrefix(content, "?:") {
				content = content[2:]
			} else if strings.HasPrefix(content, "?") {
				return nil, fmt.Errorf("unsupported Unicode group extension")
			}
			inner, err := expandUnicodePropertyGroups(content)
			if err != nil {
				return nil, err
			}
			result := make([]string, 0, len(inner))
			for _, value := range inner {
				branches, err := splitUnicodePropertyAlternatives(value)
				if err != nil {
					return nil, err
				}
				for _, branch := range branches {
					result = append(result, pattern[:start]+branch+pattern[end+1:])
				}
			}
			if len(result) > maxUnicodeGroupExpansions {
				return nil, fmt.Errorf("unicode group expansion is too large")
			}
			var expanded []string
			for _, value := range result {
				values, err := expandUnicodePropertyGroups(value)
				if err != nil {
					return nil, err
				}
				expanded = append(expanded, values...)
				if len(expanded) > maxUnicodeGroupExpansions {
					return nil, fmt.Errorf("unicode group expansion is too large")
				}
			}
			return expanded, nil
		}
	}
	if class || depth != 0 {
		return nil, fmt.Errorf("unbalanced Unicode group or class")
	}
	return []string{pattern}, nil
}

// splitUnicodePropertyAlternatives 处理平面分支，同时保留属性类中的字面量竖线。嵌套分组
// 仍由原子解析器拒绝，直至完整 rune 图编译器处理。
func splitUnicodePropertyAlternatives(pattern string) ([]string, error) {
	branches := make([]string, 0, 2)
	start, class := 0, false
	for index := 0; index < len(pattern); index++ {
		switch pattern[index] {
		case '\\':
			if index+1 < len(pattern) {
				index++
			}
		case '[':
			class = true
		case ']':
			class = false
		case '|':
			if !class {
				if start == index {
					return nil, fmt.Errorf("empty Unicode property alternative")
				}
				branches = append(branches, pattern[start:index])
				start = index + 1
			}
		}
	}
	if class || start == len(pattern) {
		return nil, fmt.Errorf("unterminated class or empty Unicode property alternative")
	}
	return append(branches, pattern[start:]), nil
}

func compileUnicodePropertyProgram(expressionIndex uint32, pattern string) (unicodePropertyProgram, error) {
	atoms, err := parseUnicodePropertyAtoms(pattern)
	if err != nil {
		return unicodePropertyProgram{}, err
	}
	if len(atoms) == 1 && atoms[0].minimum != 0 {
		return unicodePropertyProgram{expressionIndex: expressionIndex, atom: atoms[0]}, nil
	}
	if len(atoms) == 1 {
		return unicodePropertyProgram{expressionIndex: expressionIndex, sequence: atoms, runeNFA: true, nullable: true}, nil
	}
	for _, atom := range atoms {
		if atom.minimum != 1 || atom.maximum != 1 {
			return unicodePropertyProgram{expressionIndex: expressionIndex, sequence: atoms, runeNFA: true}, nil
		}
	}
	return unicodePropertyProgram{expressionIndex: expressionIndex, sequence: atoms}, nil
}

func unicodePropertyFixedAtoms(program unicodePropertyProgram) ([]unicodePropertyAtom, bool) {
	if program.graph != nil || program.nullable || program.anchorStart || program.anchorEnd || program.wordStart || program.wordEnd {
		return nil, false
	}
	atoms := program.sequence
	if len(atoms) == 0 {
		atoms = []unicodePropertyAtom{program.atom}
	}
	fixed := make([]unicodePropertyAtom, 0, len(atoms))
	for _, atom := range atoms {
		if atom.maximum == unicodePropertyUnbounded || atom.minimum != atom.maximum || atom.minimum == 0 || int(atom.minimum) > maxUnicodeApproximateAtoms-len(fixed) {
			return nil, false
		}
		count := atom.minimum
		atom.minimum, atom.maximum = 1, 1
		for ; count != 0; count-- {
			fixed = append(fixed, atom)
		}
	}
	return fixed, len(fixed) != 0
}

func parseUnicodePropertyAtoms(pattern string) ([]unicodePropertyAtom, error) {
	atoms := make([]unicodePropertyAtom, 0, 2)
	for position := 0; position < len(pattern); {
		atom, next, err := parseUnicodePropertyAtom(pattern, position)
		if err != nil {
			return nil, err
		}
		quantifierEnd := next
		if next < len(pattern) {
			switch pattern[next] {
			case '+':
				quantifierEnd++
			case '{':
				parser := regexParser{pattern: pattern[next:]}
				if _, _, quantified, err := parser.parseBraceQuantifier(); err != nil || !quantified {
					return nil, fmt.Errorf("invalid Unicode property repeat")
				}
				quantifierEnd += parser.pos
			case '*', '?':
				quantifierEnd++
			}
		}
		atom.minimum, atom.maximum, err = parseUnicodePropertyQuantifier(pattern[next:quantifierEnd])
		if err != nil {
			return nil, err
		}
		atoms = append(atoms, atom)
		position = quantifierEnd
	}
	return atoms, nil
}

func parseUnicodePropertyAtom(pattern string, position int) (unicodePropertyAtom, int, error) {
	if position >= len(pattern) {
		return unicodePropertyAtom{}, position, fmt.Errorf("expected Unicode property atom")
	}
	if pattern[position] == '\\' {
		return parseUnicodePropertyEscape(pattern, position)
	}
	if pattern[position] != '[' {
		value, width := utf8.DecodeRuneInString(pattern[position:])
		if value == utf8.RuneError && width == 1 {
			return unicodePropertyAtom{}, position, fmt.Errorf("invalid UTF-8 literal at byte %d", position)
		}
		if strings.ContainsRune(".|(){}*+?^$]", value) {
			return unicodePropertyAtom{}, position, fmt.Errorf("unsupported Unicode regular-expression token %q", value)
		}
		return unicodePropertyAtom{matchers: []unicodePropertyMatcher{{literal: value, isLiteral: true}}}, position + width, nil
	}
	position++
	negated := position < len(pattern) && pattern[position] == '^'
	if negated {
		position++
	}
	atom := unicodePropertyAtom{negated: negated}
	for position < len(pattern) && pattern[position] != ']' {
		var member unicodePropertyAtom
		var next int
		var err error
		if strings.HasPrefix(pattern[position:], "[:") {
			member, next, err = parseUnicodePOSIXClass(pattern, position)
			if err != nil {
				return unicodePropertyAtom{}, position, fmt.Errorf("invalid Unicode POSIX character class: %w", err)
			}
		} else if pattern[position] == '\\' {
			member, next, err = parseUnicodePropertyEscape(pattern, position)
			if err != nil {
				return unicodePropertyAtom{}, position, fmt.Errorf("invalid Unicode property class: %w", err)
			}
		} else {
			value, width := utf8.DecodeRuneInString(pattern[position:])
			if value == utf8.RuneError && width == 1 || value == '[' {
				return unicodePropertyAtom{}, position, fmt.Errorf("invalid Unicode property class literal")
			}
			member = unicodePropertyAtom{matchers: []unicodePropertyMatcher{{literal: value, isLiteral: true}}}
			next = position + width
		}
		if len(member.matchers) == 1 && member.matchers[0].isLiteral && next+1 < len(pattern) && pattern[next] == '-' && pattern[next+1] != ']' {
			end, width := utf8.DecodeRuneInString(pattern[next+1:])
			if end == utf8.RuneError && width == 1 || end == '[' || end == '\\' || end < member.matchers[0].literal {
				return unicodePropertyAtom{}, position, fmt.Errorf("invalid Unicode property class range")
			}
			member.matchers[0].rangeEnd = end
			member.matchers[0].isRange = true
			next += 1 + width
		}
		if member.negated {
			return unicodePropertyAtom{}, position, fmt.Errorf("unicode property classes cannot contain a negated member")
		}
		atom.matchers = append(atom.matchers, member.matchers...)
		position = next
	}
	if position == len(pattern) || len(atom.matchers) == 0 {
		return unicodePropertyAtom{}, position, fmt.Errorf("unterminated or empty Unicode property class")
	}
	return atom, position + 1, nil
}

// parseUnicodePOSIXClass 解析字符类内部的 [:name:] 和 [:^name:]。调用方只会在 '['
// 已经开启的 Unicode 字符类中调用它，因此返回的 next 指向 POSIX 子类后的第一个字节。
func parseUnicodePOSIXClass(pattern string, position int) (unicodePropertyAtom, int, error) {
	start := position
	position += 2 // "[:"
	negated := position < len(pattern) && pattern[position] == '^'
	if negated {
		position++
	}
	nameStart := position
	for position < len(pattern) && pattern[position] != ':' {
		position++
	}
	if position == nameStart || position+1 >= len(pattern) || pattern[position+1] != ']' {
		return unicodePropertyAtom{}, start, fmt.Errorf("unterminated POSIX character class")
	}
	class, ok := unicodePOSIXClassForName(pattern[nameStart:position])
	if !ok {
		return unicodePropertyAtom{}, start, fmt.Errorf("unknown POSIX character class %q", pattern[nameStart:position])
	}
	return unicodePropertyAtom{
		matchers: []unicodePropertyMatcher{{posix: class, negated: negated}},
	}, position + 2, nil
}

func unicodePOSIXClassForName(name string) (unicodePOSIXClass, bool) {
	switch name {
	case "alnum":
		return unicodePOSIXAlnum, true
	case "alpha":
		return unicodePOSIXAlpha, true
	case "ascii":
		return unicodePOSIXASCII, true
	case "blank":
		return unicodePOSIXBlank, true
	case "cntrl":
		return unicodePOSIXCntrl, true
	case "digit":
		return unicodePOSIXDigit, true
	case "graph":
		return unicodePOSIXGraph, true
	case "lower":
		return unicodePOSIXLower, true
	case "print":
		return unicodePOSIXPrint, true
	case "punct":
		return unicodePOSIXPunct, true
	case "space":
		return unicodePOSIXSpace, true
	case "upper":
		return unicodePOSIXUpper, true
	case "word":
		return unicodePOSIXWord, true
	case "xdigit":
		return unicodePOSIXXDigit, true
	default:
		return unicodePOSIXNone, false
	}
}

func parseUnicodePropertyEscape(pattern string, position int) (unicodePropertyAtom, int, error) {
	if len(pattern)-position < 2 || pattern[position] != '\\' {
		return unicodePropertyAtom{}, position, fmt.Errorf("expected Unicode property escape at byte %d", position)
	}
	switch pattern[position+1] {
	case 'd':
		return unicodePropertyAtom{matchers: []unicodePropertyMatcher{{table: unicode.Categories["Nd"]}}}, position + 2, nil
	case 'D':
		return unicodePropertyAtom{matchers: []unicodePropertyMatcher{{table: unicode.Categories["Nd"], negated: true}}}, position + 2, nil
	case 's':
		return unicodePropertyAtom{matchers: []unicodePropertyMatcher{{table: unicode.White_Space}}}, position + 2, nil
	case 'S':
		return unicodePropertyAtom{matchers: []unicodePropertyMatcher{{table: unicode.White_Space, negated: true}}}, position + 2, nil
	case 'w':
		return unicodePropertyAtom{matchers: []unicodePropertyMatcher{
			{table: unicode.Letter},
			{table: unicode.Mark},
			{table: unicode.Number},
			{table: unicode.Categories["Pc"]},
		}}, position + 2, nil
	case 'W':
		return unicodePropertyAtom{negated: true, matchers: []unicodePropertyMatcher{
			{table: unicode.Letter},
			{table: unicode.Mark},
			{table: unicode.Number},
			{table: unicode.Categories["Pc"]},
		}}, position + 2, nil
	case 'h':
		return unicodePropertyAtom{matchers: []unicodePropertyMatcher{{horizontal: true}}}, position + 2, nil
	case 'H':
		return unicodePropertyAtom{matchers: []unicodePropertyMatcher{{horizontal: true, negated: true}}}, position + 2, nil
	case 'v':
		return unicodePropertyAtom{matchers: []unicodePropertyMatcher{{vertical: true}}}, position + 2, nil
	case 'V':
		return unicodePropertyAtom{matchers: []unicodePropertyMatcher{{vertical: true, negated: true}}}, position + 2, nil
	case 'n':
		return unicodePropertyAtom{matchers: []unicodePropertyMatcher{{literal: '\n', isLiteral: true}}}, position + 2, nil
	case 'a':
		return unicodePropertyAtom{matchers: []unicodePropertyMatcher{{literal: '\a', isLiteral: true}}}, position + 2, nil
	case 'e':
		return unicodePropertyAtom{matchers: []unicodePropertyMatcher{{literal: 0x1b, isLiteral: true}}}, position + 2, nil
	case 'x':
		if position+2 >= len(pattern) || pattern[position+2] != '{' {
			return unicodePropertyAtom{}, position, fmt.Errorf("unicode hex escape requires braced hexadecimal digits")
		}
		value, next, err := parseBracedHexEscape(pattern, position+2, utf8.MaxRune)
		if err != nil {
			return unicodePropertyAtom{}, position, err
		}
		if value >= 0xd800 && value <= 0xdfff {
			return unicodePropertyAtom{}, position, fmt.Errorf("unicode hex escape cannot be a surrogate")
		}
		return unicodePropertyAtom{matchers: []unicodePropertyMatcher{{literal: rune(value), isLiteral: true}}}, next, nil
	case 'o':
		value, next, err := parseBracedOctalEscape(pattern, position+2, utf8.MaxRune)
		if err != nil {
			return unicodePropertyAtom{}, position, err
		}
		if value >= 0xd800 && value <= 0xdfff {
			return unicodePropertyAtom{}, position, fmt.Errorf("unicode octal escape cannot be a surrogate")
		}
		return unicodePropertyAtom{matchers: []unicodePropertyMatcher{{literal: rune(value), isLiteral: true}}}, next, nil
	case '0':
		value, next := byte(0), position+2
		for range 3 {
			if next == len(pattern) || !isOctalDigit(pattern[next]) {
				break
			}
			value = value<<3 | (pattern[next] - '0')
			next++
		}
		return unicodePropertyAtom{matchers: []unicodePropertyMatcher{{literal: rune(value), isLiteral: true}}}, next, nil
	case 'r':
		return unicodePropertyAtom{matchers: []unicodePropertyMatcher{{literal: '\r', isLiteral: true}}}, position + 2, nil
	case 't':
		return unicodePropertyAtom{matchers: []unicodePropertyMatcher{{literal: '\t', isLiteral: true}}}, position + 2, nil
	case 'f':
		return unicodePropertyAtom{matchers: []unicodePropertyMatcher{{literal: '\f', isLiteral: true}}}, position + 2, nil
	case 'c':
		if len(pattern)-position < 3 {
			return unicodePropertyAtom{}, position, fmt.Errorf("control escape requires an ASCII letter")
		}
		value, ok := asciiControlEscape(pattern[position+2])
		if !ok {
			return unicodePropertyAtom{}, position, fmt.Errorf("control escape requires an ASCII letter")
		}
		return unicodePropertyAtom{matchers: []unicodePropertyMatcher{{literal: rune(value), isLiteral: true}}}, position + 3, nil
	case 'p', 'P':
		nameStart := position + 3
		if position+2 >= len(pattern) || pattern[position+2] != '{' {
			return unicodePropertyAtom{}, position, fmt.Errorf("expected '{' after Unicode property")
		}
		nameEnd := strings.IndexByte(pattern[nameStart:], '}')
		if nameEnd < 0 {
			return unicodePropertyAtom{}, position, fmt.Errorf("unterminated Unicode property")
		}
		nameEnd += nameStart
		name := pattern[nameStart:nameEnd]
		negated := pattern[position+1] == 'P'
		if strings.HasPrefix(name, "^") {
			name = name[1:]
			negated = !negated
		}
		matcher, ok := unicodePropertyMatcherForName(name)
		if !ok {
			return unicodePropertyAtom{}, position, fmt.Errorf("unknown Unicode property %q", pattern[nameStart:nameEnd])
		}
		matcher.negated = matcher.negated != negated
		return unicodePropertyAtom{matchers: []unicodePropertyMatcher{matcher}}, nameEnd + 1, nil
	default:
		return unicodePropertyAtom{}, position, fmt.Errorf("unsupported Unicode property escape \\%c", pattern[position+1])
	}
}

func unicodePropertyTable(name string) *unicode.RangeTable {
	canonical := canonicalUnicodePropertyName(name)
	if value, ok := strings.CutPrefix(canonical, "generalcategory="); ok {
		return unicodePropertyCategory(value)
	}
	if value, ok := strings.CutPrefix(canonical, "gc="); ok {
		return unicodePropertyCategory(value)
	}
	if value, ok := strings.CutPrefix(canonical, "script="); ok {
		return unicodePropertyScript(value)
	}
	if value, ok := strings.CutPrefix(canonical, "sc="); ok {
		return unicodePropertyScript(value)
	}
	if value, ok := strings.CutPrefix(canonical, "is"); ok {
		return unicodePropertyAnyTable(value)
	}
	return unicodePropertyAnyTable(canonical)
}

func unicodePropertyMatcherForName(name string) (unicodePropertyMatcher, bool) {
	canonical := canonicalUnicodePropertyName(name)
	switch canonical {
	case "any":
		return unicodePropertyMatcher{any: true, dotAll: true}, true
	case "assigned":
		return unicodePropertyMatcher{table: unicode.Cn, negated: true}, true
	case "ascii":
		return unicodePropertyMatcher{table: unicodeASCII}, true
	}
	table := unicodePropertyTable(canonical)
	if table == nil {
		return unicodePropertyMatcher{}, false
	}
	return unicodePropertyMatcher{table: table}, true
}

var unicodeASCII = &unicode.RangeTable{R16: []unicode.Range16{{Lo: 0, Hi: 0x7f, Stride: 1}}}

func canonicalUnicodePropertyName(name string) string {
	var result strings.Builder
	result.Grow(len(name))
	for _, value := range name {
		switch value {
		case '_', '-', ' ', '\t':
			continue
		}
		if value >= 'A' && value <= 'Z' {
			value += 'a' - 'A'
		}
		result.WriteRune(value)
	}
	return result.String()
}

func unicodePropertyCategory(name string) *unicode.RangeTable {
	if table := unicodePropertyTableFromMap(unicode.Categories, name); table != nil {
		return table
	}
	switch name {
	case "letter":
		return unicode.Letter
	case "lowercaseletter":
		return unicode.Lower
	case "uppercaseletter":
		return unicode.Upper
	case "titlecaseletter":
		return unicode.Title
	case "modifierletter":
		return unicode.Lm
	case "otherletter":
		return unicode.Lo
	case "mark":
		return unicode.Mark
	case "nonspacingmark":
		return unicode.Mn
	case "spacingmark", "combiningspacingmark":
		return unicode.Mc
	case "enclosingmark":
		return unicode.Me
	case "number":
		return unicode.Number
	case "decimalnumber", "decimaldigitnumber", "digit":
		return unicode.Digit
	case "letternumber":
		return unicode.Nl
	case "othernumber":
		return unicode.No
	case "punctuation":
		return unicode.Punct
	case "symbol":
		return unicode.Symbol
	case "separator":
		return unicode.Z
	case "space", "spaceseparator":
		return unicode.White_Space
	case "lineseparator":
		return unicode.Zl
	case "paragraphseparator":
		return unicode.Zp
	case "other", "control":
		return unicode.C
	case "format":
		return unicode.Cf
	case "privateuse":
		return unicode.Co
	case "surrogate":
		return unicode.Cs
	case "unassigned":
		return unicode.Cn
	default:
		return nil
	}
}

func unicodePropertyScript(name string) *unicode.RangeTable {
	return unicodePropertyTableFromMap(unicode.Scripts, name)
}

func unicodePropertyAnyTable(name string) *unicode.RangeTable {
	if table := unicodePropertyCategory(name); table != nil {
		return table
	}
	if table := unicodePropertyScript(name); table != nil {
		return table
	}
	return unicodePropertyTableFromMap(unicode.Properties, name)
}

func unicodePropertyTableFromMap(tables map[string]*unicode.RangeTable, name string) *unicode.RangeTable {
	for key, table := range tables {
		if canonicalUnicodePropertyName(key) == name {
			return table
		}
	}
	return nil
}

func parseUnicodePropertyQuantifier(suffix string) (uint32, uint32, error) {
	switch suffix {
	case "":
		return 1, 1, nil
	case "+":
		return 1, unicodePropertyUnbounded, nil
	case "?":
		return 0, 1, nil
	case "*":
		return 0, unicodePropertyUnbounded, nil
	}
	if len(suffix) == 0 || suffix[0] != '{' {
		return 0, 0, fmt.Errorf("unicode properties support only + or positive bounded repeats")
	}
	parser := regexParser{pattern: suffix}
	minimum, maximum, quantified, err := parser.parseBraceQuantifier()
	if err != nil || !quantified || parser.pos != len(suffix) {
		return 0, 0, fmt.Errorf("invalid Unicode property repeat")
	}
	if maximum != unboundedRepeat && maximum > maxBoundedRepetition {
		return 0, 0, fmt.Errorf("unicode property repeat is too large")
	}
	if maximum == unboundedRepeat {
		return uint32(minimum), unicodePropertyUnbounded, nil
	}
	return uint32(minimum), uint32(maximum), nil
}

func (program unicodePropertyProgram) matches(value rune) bool {
	return program.atom.matches(value)
}

func (program *unicodePropertyProgram) enableCaseless() {
	for index := range program.atom.matchers {
		program.atom.matchers[index].caseless = true
	}
	for atomIndex := range program.sequence {
		for matcherIndex := range program.sequence[atomIndex].matchers {
			program.sequence[atomIndex].matchers[matcherIndex].caseless = true
		}
	}
	if program.graph != nil {
		for stateIndex := range program.graph.states {
			state := &program.graph.states[stateIndex]
			if !state.hasAtom {
				continue
			}
			for matcherIndex := range state.atom.matchers {
				state.atom.matchers[matcherIndex].caseless = true
			}
		}
	}
}

// prepareASCII 降低 UCP 执行器处理普通日志文本的开销。每个非 ASCII 码点仍使用 unicode.Is，
// 保持完整 Unicode 语义；ASCII 属性成员关系则变为两次位测试。
func (program *unicodePropertyProgram) prepareASCII() {
	program.atom.prepareASCII()
	for index := range program.sequence {
		program.sequence[index].prepareASCII()
	}
	if program.graph == nil {
		return
	}
	for index := range program.graph.states {
		if program.graph.states[index].hasAtom {
			program.graph.states[index].atom.prepareASCII()
		}
	}
}

func (atom *unicodePropertyAtom) prepareASCII() {
	for index := range atom.matchers {
		atom.matchers[index].prepareASCII()
	}
}

func (matcher *unicodePropertyMatcher) prepareASCII() {
	for value := rune(0); value < 128; value++ {
		if matcher.matchesSlow(value) {
			matcher.ascii[value>>6] |= uint64(1) << (value & 63)
		}
	}
}

func (atom unicodePropertyAtom) matches(value rune) bool {
	if len(atom.matchers) == 1 {
		matcher := &atom.matchers[0]
		return (matcher.matches(value) != matcher.negated) != atom.negated
	}
	for index := range atom.matchers {
		matcher := &atom.matchers[index]
		if matcher.matches(value) != matcher.negated {
			return !atom.negated
		}
	}
	return atom.negated
}

func (matcher *unicodePropertyMatcher) matches(value rune) bool {
	if uint32(value) < 128 {
		return matcher.ascii[value>>6]&(uint64(1)<<(value&63)) != 0
	}
	return matcher.matchesSlow(value)
}

func (matcher *unicodePropertyMatcher) matchesSlow(value rune) bool {
	if matcher.posix != unicodePOSIXNone {
		matched := unicodePOSIXClassMatches(matcher.posix, value)
		if matched || !matcher.caseless {
			return matched
		}
		for folded := unicode.SimpleFold(value); folded != value; folded = unicode.SimpleFold(folded) {
			if unicodePOSIXClassMatches(matcher.posix, folded) {
				return true
			}
		}
		return false
	}
	if matcher.any {
		return matcher.dotAll || value != '\n'
	}
	if matcher.horizontal {
		return unicodeHorizontalSpace(value)
	}
	if matcher.vertical {
		return unicodeVerticalSpace(value)
	}
	if matcher.isLiteral {
		if matcher.isRange {
			if matcher.literal <= value && value <= matcher.rangeEnd {
				return true
			}
			if !matcher.caseless {
				return false
			}
			for folded := unicode.SimpleFold(value); folded != value; folded = unicode.SimpleFold(folded) {
				if matcher.literal <= folded && folded <= matcher.rangeEnd {
					return true
				}
			}
			return false
		}
		return matcher.literal == value || matcher.caseless && unicodeSimpleFoldEqual(matcher.literal, value)
	}
	matched := unicode.Is(matcher.table, value)
	if matched || !matcher.caseless {
		return matched
	}
	// Unicode 属性也是字符类。在 CASELESS 下检查完整简单折叠环，使 \p{Lu}、\p{Ll} 和属性类
	// 与字面量和范围成员拥有相同的闭包行为。
	for folded := unicode.SimpleFold(value); folded != value; folded = unicode.SimpleFold(folded) {
		if unicode.Is(matcher.table, folded) {
			return true
		}
	}
	return false
}

func unicodePOSIXClassMatches(class unicodePOSIXClass, value rune) bool {
	switch class {
	case unicodePOSIXAlnum:
		return unicode.IsLetter(value) || unicode.IsNumber(value)
	case unicodePOSIXAlpha:
		return unicode.IsLetter(value)
	case unicodePOSIXASCII:
		return value >= 0 && value <= 0x7f
	case unicodePOSIXBlank:
		return unicodeHorizontalSpace(value)
	case unicodePOSIXCntrl:
		return unicode.Is(unicode.Categories["Cc"], value)
	case unicodePOSIXDigit:
		return unicode.Is(unicode.Categories["Nd"], value)
	case unicodePOSIXGraph:
		return !unicode.Is(unicode.C, value) && !unicode.Is(unicode.Z, value)
	case unicodePOSIXLower:
		return unicode.IsLower(value)
	case unicodePOSIXPrint:
		return !unicode.Is(unicode.C, value)
	case unicodePOSIXPunct:
		return unicode.IsPunct(value)
	case unicodePOSIXSpace:
		return unicode.IsSpace(value)
	case unicodePOSIXUpper:
		return unicode.IsUpper(value)
	case unicodePOSIXWord:
		return unicode.IsLetter(value) || unicode.IsMark(value) || unicode.IsNumber(value) || unicode.Is(unicode.Categories["Pc"], value)
	case unicodePOSIXXDigit:
		return value >= '0' && value <= '9' || value >= 'a' && value <= 'f' || value >= 'A' && value <= 'F'
	default:
		return false
	}
}

func unicodeHorizontalSpace(value rune) bool {
	switch value {
	case '\t', ' ', '\u00a0', '\u1680', '\u180e', '\u2000', '\u2001', '\u2002', '\u2003', '\u2004', '\u2005', '\u2006', '\u2007', '\u2008', '\u2009', '\u200a', '\u202f', '\u205f', '\u3000':
		return true
	}
	return false
}

func unicodeVerticalSpace(value rune) bool {
	switch value {
	case '\n', '\r', '\v', '\f', '\u0085', '\u2028', '\u2029':
		return true
	}
	return false
}

func unicodeSimpleFoldEqual(left, right rune) bool {
	if left == right {
		return true
	}
	for value := unicode.SimpleFold(left); value != left; value = unicode.SimpleFold(value) {
		if value == right {
			return true
		}
	}
	return false
}

// scanBlockUnicodeProperties 仅解码输入一次，并在 rune 边界推进每条 UCP 属性规则。重复单原子
// 保留运行起点，固定属性串联仅保留最后一个 rune 窗口。两种形式均保留字节偏移，使每个可观察
// 匹配都在有效 UTF-8 边界报告。
func (scanner *Scanner) scanBlockUnicodeProperties(data []byte, ctx *context, matches *[]Match) error {
	switch scanner.unicodeScanPlan {
	case unicodeScanPlanLiteralAC:
		return scanner.scanBlockUnicodePropertiesLiteralOnly(data, ctx, matches)
	case unicodeScanPlanPure:
		return scanner.scanBlockUnicodePropertiesPure(data, ctx, matches)
	case unicodeScanPlanSimpleRepeats:
		return scanner.scanBlockUnicodePropertiesSimpleRepeats(data, ctx, matches)
	case unicodeScanPlanGeneric:
		return scanner.scanBlockUnicodePropertiesGeneric(data, ctx, matches)
	default:
		return scanner.scanBlockUnicodePropertiesGeneric(data, ctx, matches)
	}
}

// scanBlockUnicodePropertiesSimpleRepeats 是字节侧仅含紧凑单类重复（例如 \d+）时的混合 UCP
// 路径。它从字节热循环中移除 AC、锚定 NFA 和高级事件分支，不改变字节终点顺序或重叠报告。
func (scanner *Scanner) scanBlockUnicodePropertiesSimpleRepeats(data []byte, ctx *context, matches *[]Match) error {
	for programIndex, program := range scanner.unicodeProperties {
		if !program.nullable || !scanner.eventNeeded[program.expressionIndex] {
			continue
		}
		if program.graph != nil && program.hasAssertions && !unicodePropertyGraphMatchesEmptyAt(program.graph, &ctx.unicodeRuns[programIndex], data, 0) {
			continue
		}
		expression := scanner.expressions[program.expressionIndex]
		ctx.readyEvents = append(ctx.readyEvents, scanEvent{match: Match{Id: expression.id}, expressionIndex: program.expressionIndex, anchorStart: program.anchorStart, anchorEnd: program.anchorEnd, wordStart: program.wordStart, wordEnd: program.wordEnd})
	}
	if len(ctx.readyEvents) != 0 {
		filterUnicodeAnchorEvents(&ctx.readyEvents, 0, data)
		collectScanEvents(scanner, ctx, 0, matches)
	}
	for offset := 0; offset < len(data); {
		value, width := utf8.DecodeRune(data[offset:])
		end := offset + width
		for byteOffset := offset; byteOffset < end; byteOffset++ {
			scanner.appendUnicodeSimpleRepeatEvents(data, data[byteOffset], byteOffset, ctx)
			if byteOffset+1 < end && (len(ctx.readyEvents) != 0 || ctx.pendingCount != 0 && ctx.pendingFirstEnd <= uint64(byteOffset+1)) {
				collectScanEvents(scanner, ctx, byteOffset+1, matches)
			}
		}
		for programIndex, program := range scanner.unicodeProperties {
			if !scanner.eventNeeded[program.expressionIndex] {
				continue
			}
			run := &ctx.unicodeRuns[programIndex]
			if program.graph != nil {
				scanner.scanUnicodePropertyGraph(program, run, value, offset, end, data, &ctx.readyEvents)
				continue
			}
			if len(program.sequence) != 0 && program.runeNFA {
				scanner.scanUnicodePropertyNFA(program, run, value, offset, end, &ctx.readyEvents)
				continue
			}
			if len(program.sequence) != 0 {
				run.sequence = append(run.sequence, unicodePropertyRune{offset: offset, value: value})
				if len(run.sequence) > len(program.sequence) {
					copy(run.sequence, run.sequence[len(run.sequence)-len(program.sequence):])
					run.sequence = run.sequence[:len(program.sequence)]
				}
				if len(run.sequence) != len(program.sequence) {
					continue
				}
				matched := true
				for atomIndex, atom := range program.sequence {
					if !atom.matches(run.sequence[atomIndex].value) {
						matched = false
						break
					}
				}
				if matched {
					expression := scanner.expressions[program.expressionIndex]
					ctx.readyEvents = append(ctx.readyEvents, scanEvent{match: Match{Id: expression.id, From: uint64(run.sequence[0].offset), To: uint64(end)}, expressionIndex: program.expressionIndex})
				}
				continue
			}
			if !program.matches(value) {
				run.starts = run.starts[:0]
				continue
			}
			run.starts = append(run.starts, offset)
			if program.atom.maximum != unicodePropertyUnbounded && len(run.starts) > int(program.atom.maximum) {
				copy(run.starts, run.starts[len(run.starts)-int(program.atom.maximum):])
				run.starts = run.starts[:program.atom.maximum]
			}
			endIndex := len(run.starts) - int(program.atom.minimum)
			if endIndex < 0 {
				continue
			}
			expression := scanner.expressions[program.expressionIndex]
			for startIndex := 0; startIndex <= endIndex; startIndex++ {
				ctx.readyEvents = append(ctx.readyEvents, scanEvent{match: Match{Id: expression.id, From: uint64(run.starts[startIndex]), To: uint64(end)}, expressionIndex: program.expressionIndex})
			}
		}
		for programIndex, program := range scanner.unicodeApproximate {
			if scanner.eventNeeded[program.expressionIndex] {
				scanner.scanUnicodeApproximate(program, &ctx.unicodeApprox[programIndex], value, offset, end, &ctx.readyEvents)
			}
		}
		for programIndex, program := range scanner.unicodeProperties {
			if !program.nullable || !scanner.eventNeeded[program.expressionIndex] {
				continue
			}
			if program.graph != nil && program.hasAssertions && !unicodePropertyGraphMatchesEmptyAt(program.graph, &ctx.unicodeRuns[programIndex], data, end) {
				continue
			}
			expression := scanner.expressions[program.expressionIndex]
			ctx.readyEvents = append(ctx.readyEvents, scanEvent{match: Match{Id: expression.id, From: uint64(end), To: uint64(end)}, expressionIndex: program.expressionIndex, anchorStart: program.anchorStart, anchorEnd: program.anchorEnd, wordStart: program.wordStart, wordEnd: program.wordEnd})
		}
		if len(ctx.readyEvents) != 0 || ctx.pendingCount != 0 && ctx.pendingFirstEnd <= uint64(end) {
			filterUnicodeAnchorEvents(&ctx.readyEvents, end, data)
			if scanner.unicodeAlternation {
				dedupeUnicodePropertyEvents(&ctx.readyEvents)
			}
			collectScanEvents(scanner, ctx, end, matches)
		}
		offset = end
	}
	return nil
}

func (scanner *Scanner) scanBlockUnicodePropertiesGeneric(data []byte, ctx *context, matches *[]Match) error {
	if scanner.advancedEvents && len(scanner.emptyRegex) != 0 {
		scanner.appendEmptyRegexEvents(&ctx.readyEvents, 0, data)
	}
	for programIndex, program := range scanner.unicodeProperties {
		if program.nullable && scanner.eventNeeded[program.expressionIndex] {
			if program.graph != nil && program.hasAssertions && !unicodePropertyGraphMatchesEmptyAt(program.graph, &ctx.unicodeRuns[programIndex], data, 0) {
				continue
			}
			expression := scanner.expressions[program.expressionIndex]
			ctx.readyEvents = append(ctx.readyEvents, scanEvent{match: Match{Id: expression.id}, expressionIndex: program.expressionIndex, anchorStart: program.anchorStart, anchorEnd: program.anchorEnd, wordStart: program.wordStart, wordEnd: program.wordEnd})
		}
	}
	if len(ctx.readyEvents) != 0 {
		filterUnicodeAnchorEvents(&ctx.readyEvents, 0, data)
		collectScanEvents(scanner, ctx, 0, matches)
	}
	for offset := 0; offset < len(data); {
		value, width := utf8.DecodeRune(data[offset:])
		end := offset + width
		for byteOffset := offset; byteOffset < end; byteOffset++ {
			scanner.appendUnicodeMixedByteEvents(data, byteOffset, ctx)
			// UTF-8 延续字节不能作为 UCP 匹配终点。在处理下一个字节前投递其处的字节扫描器结果，
			// 使组合和约束保持常规字节偏移语义。
			if byteOffset+1 < end && (len(ctx.readyEvents) != 0 || ctx.pendingCount != 0 && ctx.pendingFirstEnd <= uint64(byteOffset+1)) {
				collectScanEvents(scanner, ctx, byteOffset+1, matches)
			}
		}
		for programIndex, program := range scanner.unicodeProperties {
			if !scanner.eventNeeded[program.expressionIndex] {
				continue
			}
			run := &ctx.unicodeRuns[programIndex]
			if program.graph != nil {
				scanner.scanUnicodePropertyGraph(program, run, value, offset, end, data, &ctx.readyEvents)
				continue
			}
			if len(program.sequence) != 0 && program.runeNFA {
				scanner.scanUnicodePropertyNFA(program, run, value, offset, end, &ctx.readyEvents)
				continue
			}
			if len(program.sequence) != 0 {
				run.sequence = append(run.sequence, unicodePropertyRune{offset: offset, value: value})
				if len(run.sequence) > len(program.sequence) {
					copy(run.sequence, run.sequence[len(run.sequence)-len(program.sequence):])
					run.sequence = run.sequence[:len(program.sequence)]
				}
				if len(run.sequence) != len(program.sequence) {
					continue
				}
				matched := true
				for atomIndex, atom := range program.sequence {
					if !atom.matches(run.sequence[atomIndex].value) {
						matched = false
						break
					}
				}
				if matched {
					expression := scanner.expressions[program.expressionIndex]
					ctx.readyEvents = append(ctx.readyEvents, scanEvent{
						match:           Match{Id: expression.id, From: uint64(run.sequence[0].offset), To: uint64(end)},
						expressionIndex: program.expressionIndex,
					})
				}
				continue
			}
			if !program.matches(value) {
				run.starts = run.starts[:0]
				continue
			}
			run.starts = append(run.starts, offset)
			if program.atom.maximum != unicodePropertyUnbounded && len(run.starts) > int(program.atom.maximum) {
				copy(run.starts, run.starts[len(run.starts)-int(program.atom.maximum):])
				run.starts = run.starts[:program.atom.maximum]
			}
			endIndex := len(run.starts) - int(program.atom.minimum)
			if endIndex < 0 {
				continue
			}
			expression := scanner.expressions[program.expressionIndex]
			for startIndex := 0; startIndex <= endIndex; startIndex++ {
				ctx.readyEvents = append(ctx.readyEvents, scanEvent{
					match:           Match{Id: expression.id, From: uint64(run.starts[startIndex]), To: uint64(end)},
					expressionIndex: program.expressionIndex,
				})
			}
		}
		for programIndex, program := range scanner.unicodeApproximate {
			if !scanner.eventNeeded[program.expressionIndex] {
				continue
			}
			scanner.scanUnicodeApproximate(program, &ctx.unicodeApprox[programIndex], value, offset, end, &ctx.readyEvents)
		}
		for programIndex, program := range scanner.unicodeProperties {
			if program.nullable && scanner.eventNeeded[program.expressionIndex] {
				if program.graph != nil && program.hasAssertions && !unicodePropertyGraphMatchesEmptyAt(program.graph, &ctx.unicodeRuns[programIndex], data, end) {
					continue
				}
				expression := scanner.expressions[program.expressionIndex]
				ctx.readyEvents = append(ctx.readyEvents, scanEvent{match: Match{Id: expression.id, From: uint64(end), To: uint64(end)}, expressionIndex: program.expressionIndex, anchorStart: program.anchorStart, anchorEnd: program.anchorEnd, wordStart: program.wordStart, wordEnd: program.wordEnd})
			}
		}
		if len(ctx.readyEvents) != 0 || ctx.pendingCount != 0 && ctx.pendingFirstEnd <= uint64(end) {
			filterUnicodeAnchorEvents(&ctx.readyEvents, end, data)
			if scanner.unicodeAlternation {
				dedupeUnicodePropertyEvents(&ctx.readyEvents)
			}
			collectScanEvents(scanner, ctx, end, matches)
		}
		offset = end
	}
	return nil
}

// scanBlockUnicodePropertiesPure 仅推进面向 rune 的 UCP 程序。它刻意不含字节扫描器分派：
// 这是常见的纯 Unicode 路径，不应继承 AC 或字节 NFA 分支工作。
func (scanner *Scanner) scanBlockUnicodePropertiesPure(data []byte, ctx *context, matches *[]Match) error {
	for programIndex, program := range scanner.unicodeProperties {
		if !program.nullable || !scanner.eventNeeded[program.expressionIndex] {
			continue
		}
		if program.graph != nil && program.hasAssertions && !unicodePropertyGraphMatchesEmptyAt(program.graph, &ctx.unicodeRuns[programIndex], data, 0) {
			continue
		}
		expression := scanner.expressions[program.expressionIndex]
		ctx.readyEvents = append(ctx.readyEvents, scanEvent{match: Match{Id: expression.id}, expressionIndex: program.expressionIndex, anchorStart: program.anchorStart, anchorEnd: program.anchorEnd, wordStart: program.wordStart, wordEnd: program.wordEnd})
	}
	if len(ctx.readyEvents) != 0 {
		filterUnicodeAnchorEvents(&ctx.readyEvents, 0, data)
		collectScanEvents(scanner, ctx, 0, matches)
	}
	for offset := 0; offset < len(data); {
		value, width := utf8.DecodeRune(data[offset:])
		end := offset + width
		for programIndex, program := range scanner.unicodeProperties {
			if !scanner.eventNeeded[program.expressionIndex] {
				continue
			}
			run := &ctx.unicodeRuns[programIndex]
			if program.graph != nil {
				scanner.scanUnicodePropertyGraph(program, run, value, offset, end, data, &ctx.readyEvents)
				continue
			}
			if len(program.sequence) != 0 && program.runeNFA {
				scanner.scanUnicodePropertyNFA(program, run, value, offset, end, &ctx.readyEvents)
				continue
			}
			if len(program.sequence) != 0 {
				run.sequence = append(run.sequence, unicodePropertyRune{offset: offset, value: value})
				if len(run.sequence) > len(program.sequence) {
					copy(run.sequence, run.sequence[len(run.sequence)-len(program.sequence):])
					run.sequence = run.sequence[:len(program.sequence)]
				}
				if len(run.sequence) != len(program.sequence) {
					continue
				}
				matched := true
				for atomIndex, atom := range program.sequence {
					if !atom.matches(run.sequence[atomIndex].value) {
						matched = false
						break
					}
				}
				if matched {
					expression := scanner.expressions[program.expressionIndex]
					ctx.readyEvents = append(ctx.readyEvents, scanEvent{match: Match{Id: expression.id, From: uint64(run.sequence[0].offset), To: uint64(end)}, expressionIndex: program.expressionIndex})
				}
				continue
			}
			if !program.matches(value) {
				run.starts = run.starts[:0]
				continue
			}
			run.starts = append(run.starts, offset)
			if program.atom.maximum != unicodePropertyUnbounded && len(run.starts) > int(program.atom.maximum) {
				copy(run.starts, run.starts[len(run.starts)-int(program.atom.maximum):])
				run.starts = run.starts[:program.atom.maximum]
			}
			endIndex := len(run.starts) - int(program.atom.minimum)
			if endIndex < 0 {
				continue
			}
			expression := scanner.expressions[program.expressionIndex]
			for startIndex := 0; startIndex <= endIndex; startIndex++ {
				ctx.readyEvents = append(ctx.readyEvents, scanEvent{match: Match{Id: expression.id, From: uint64(run.starts[startIndex]), To: uint64(end)}, expressionIndex: program.expressionIndex})
			}
		}
		for programIndex, program := range scanner.unicodeApproximate {
			if scanner.eventNeeded[program.expressionIndex] {
				scanner.scanUnicodeApproximate(program, &ctx.unicodeApprox[programIndex], value, offset, end, &ctx.readyEvents)
			}
		}
		for programIndex, program := range scanner.unicodeProperties {
			if !program.nullable || !scanner.eventNeeded[program.expressionIndex] {
				continue
			}
			if program.graph != nil && program.hasAssertions && !unicodePropertyGraphMatchesEmptyAt(program.graph, &ctx.unicodeRuns[programIndex], data, end) {
				continue
			}
			expression := scanner.expressions[program.expressionIndex]
			ctx.readyEvents = append(ctx.readyEvents, scanEvent{match: Match{Id: expression.id, From: uint64(end), To: uint64(end)}, expressionIndex: program.expressionIndex, anchorStart: program.anchorStart, anchorEnd: program.anchorEnd, wordStart: program.wordStart, wordEnd: program.wordEnd})
		}
		if len(ctx.readyEvents) != 0 {
			filterUnicodeAnchorEvents(&ctx.readyEvents, end, data)
			if scanner.unicodeAlternation {
				dedupeUnicodePropertyEvents(&ctx.readyEvents)
			}
			collectScanEvents(scanner, ctx, end, matches)
		}
		offset = end
	}
	return nil
}

// scanBlockUnicodePropertiesLiteralOnly 是常见的 UCP 加 ASCII 字面量路径。将通用字节正则和
// 近似检查排除在循环外，可保留脱敏和 PII 工作负载所需的 AC 吞吐。
func (scanner *Scanner) scanBlockUnicodePropertiesLiteralOnly(data []byte, ctx *context, matches *[]Match) error {
	for offset := 0; offset < len(data); {
		value, width := utf8.DecodeRune(data[offset:])
		end := offset + width
		scanner.appendUnicodeMixedLiteralEvents(data, offset, end, ctx)
		for programIndex, program := range scanner.unicodeProperties {
			if !scanner.eventNeeded[program.expressionIndex] {
				continue
			}
			run := &ctx.unicodeRuns[programIndex]
			if program.graph != nil {
				scanner.scanUnicodePropertyGraph(program, run, value, offset, end, data, &ctx.readyEvents)
				continue
			}
			if len(program.sequence) != 0 && program.runeNFA {
				scanner.scanUnicodePropertyNFA(program, run, value, offset, end, &ctx.readyEvents)
				continue
			}
			if len(program.sequence) != 0 {
				run.sequence = append(run.sequence, unicodePropertyRune{offset: offset, value: value})
				if len(run.sequence) > len(program.sequence) {
					copy(run.sequence, run.sequence[len(run.sequence)-len(program.sequence):])
					run.sequence = run.sequence[:len(program.sequence)]
				}
				if len(run.sequence) != len(program.sequence) {
					continue
				}
				matched := true
				for atomIndex, atom := range program.sequence {
					if !atom.matches(run.sequence[atomIndex].value) {
						matched = false
						break
					}
				}
				if matched {
					expression := scanner.expressions[program.expressionIndex]
					ctx.readyEvents = append(ctx.readyEvents, scanEvent{match: Match{Id: expression.id, From: uint64(run.sequence[0].offset), To: uint64(end)}, expressionIndex: program.expressionIndex})
				}
				continue
			}
			if !program.matches(value) {
				run.starts = run.starts[:0]
				continue
			}
			run.starts = append(run.starts, offset)
			if program.atom.maximum != unicodePropertyUnbounded && len(run.starts) > int(program.atom.maximum) {
				copy(run.starts, run.starts[len(run.starts)-int(program.atom.maximum):])
				run.starts = run.starts[:program.atom.maximum]
			}
			endIndex := len(run.starts) - int(program.atom.minimum)
			if endIndex < 0 {
				continue
			}
			expression := scanner.expressions[program.expressionIndex]
			for startIndex := 0; startIndex <= endIndex; startIndex++ {
				ctx.readyEvents = append(ctx.readyEvents, scanEvent{match: Match{Id: expression.id, From: uint64(run.starts[startIndex]), To: uint64(end)}, expressionIndex: program.expressionIndex})
			}
		}
		for programIndex, program := range scanner.unicodeProperties {
			if !program.nullable || !scanner.eventNeeded[program.expressionIndex] {
				continue
			}
			if program.graph != nil && program.hasAssertions && !unicodePropertyGraphMatchesEmptyAt(program.graph, &ctx.unicodeRuns[programIndex], data, end) {
				continue
			}
			expression := scanner.expressions[program.expressionIndex]
			ctx.readyEvents = append(ctx.readyEvents, scanEvent{match: Match{Id: expression.id, From: uint64(end), To: uint64(end)}, expressionIndex: program.expressionIndex, anchorStart: program.anchorStart, anchorEnd: program.anchorEnd, wordStart: program.wordStart, wordEnd: program.wordEnd})
		}
		if len(ctx.readyEvents) != 0 {
			filterUnicodeAnchorEvents(&ctx.readyEvents, end, data)
			if scanner.unicodeAlternation {
				dedupeUnicodePropertyEvents(&ctx.readyEvents)
			}
			collectScanEvents(scanner, ctx, end, matches)
		}
		offset = end
	}
	return nil
}

func (scanner *Scanner) scanUnicodeApproximate(program unicodeApproximateProgram, run *unicodeApproximateRun, value rune, offset, end int, events *[]scanEvent) {
	run.runes = append(run.runes, unicodePropertyRune{offset: offset, value: value})
	maximum := len(program.atoms)
	if program.graph != nil {
		maximum = program.maximumWidth
	}
	if !program.hamming {
		maximum += int(program.distance)
	}
	if len(run.runes) > maximum {
		copy(run.runes, run.runes[len(run.runes)-maximum:])
		run.runes = run.runes[:maximum]
	}
	if program.hamming {
		if len(run.runes) != maximum {
			return
		}
		if program.graph != nil {
			if !run.graphProduct.matchesHamming(program.graph, run.runes) {
				return
			}
		} else if !unicodeHammingMatches(run.runes, program.atoms, program.distance) {
			return
		}
		scanner.appendUnicodeApproximateEvent(program, run.runes[0].offset, end, events)
		return
	}
	width := len(program.atoms)
	if program.graph != nil {
		width = program.minimumWidth
	}
	minimum := width - int(program.distance)
	if minimum < 1 {
		minimum = 1
	}
	for length := minimum; length <= len(run.runes); length++ {
		window := run.runes[len(run.runes)-length:]
		matched := false
		if program.graph != nil {
			matched = run.graphProduct.matchesEdit(program.graph, window)
		} else {
			matched = unicodeEditDistanceAtMost(run.previous, run.current, window, program.atoms, program.distance)
		}
		if matched {
			scanner.appendUnicodeApproximateEvent(program, window[0].offset, end, events)
		}
	}
}

func (scanner *Scanner) appendUnicodeApproximateEvent(program unicodeApproximateProgram, start, end int, events *[]scanEvent) {
	expression := scanner.expressions[program.expressionIndex]
	*events = append(*events, scanEvent{match: Match{Id: expression.id, From: uint64(start), To: uint64(end)}, expressionIndex: program.expressionIndex})
}

func unicodeHammingMatches(values []unicodePropertyRune, atoms []unicodePropertyAtom, maximum uint32) bool {
	var distance uint32
	for index, value := range values {
		if atoms[index].matches(value.value) {
			continue
		}
		distance++
		if distance > maximum {
			return false
		}
	}
	return true
}

// unicodeEditDistanceAtMost 是字节编辑匹配器对应的 rune/属性版本。对应 UCP 原子接受 rune 时，
// 替换代价为零，因此字符类和属性无需实体化为具体字面量展开即可参与匹配。
func unicodeEditDistanceAtMost(previous, current []uint16, values []unicodePropertyRune, atoms []unicodePropertyAtom, maximum uint32) bool {
	if len(atoms) > maxUnicodeApproximateAtoms || len(previous) < len(atoms)+1 || len(current) < len(atoms)+1 {
		return false
	}
	previous[0] = 0
	for index := range atoms {
		previous[index+1] = uint16(index + 1)
	}
	for valueIndex, value := range values {
		current[0] = uint16(valueIndex + 1)
		for atomIndex, atom := range atoms {
			substitution := previous[atomIndex]
			if !atom.matches(value.value) {
				substitution++
			}
			insertion := previous[atomIndex+1] + 1
			deletion := current[atomIndex] + 1
			current[atomIndex+1] = min(substitution, min(insertion, deletion))
		}
		previous, current = current, previous
	}
	return previous[len(atoms)] <= uint16(maximum)
}

// appendUnicodeMixedByteEvents 对一个输入字节精确推进每个面向字节的扫描器一次。调用方从
// rune 解码器调用它，因此 UCP 与字节规则共享一次前向遍历，而非由两个独立执行器重扫块输入。
func (scanner *Scanner) appendUnicodeMixedByteEvents(data []byte, offset int, ctx *context) {
	value := data[offset]
	if len(scanner.triggers) != 0 {
		ctx.state = scanner.automaton.transitions[ctx.state<<8|uint32(value)]
		for output := scanner.automaton.outputStart[ctx.state]; output < scanner.automaton.outputEnd[ctx.state]; output++ {
			scanner.appendBlockTriggerEvents(data, offset, scanner.automaton.outputs[output], ctx)
		}
	}
	scanner.appendBlockUnanchoredEvents(data, value, offset, ctx)
	if scanner.advancedEvents {
		scanner.appendBlockHammingEvents(&ctx.readyEvents, ctx, data, offset+1)
		scanner.appendBlockEditEvents(&ctx.readyEvents, ctx, data, offset+1)
		scanner.appendEmptyRegexEvents(&ctx.readyEvents, uint64(offset+1), data)
	}
}

// appendUnicodeSimpleRepeatEvents 仅对 unicodeScanPlanSimpleRepeats 有效。该计划中的每个
// 编译通道均有紧凑重复执行器，且不存在活跃触发器或高级事件程序。
func (scanner *Scanner) appendUnicodeSimpleRepeatEvents(data []byte, value byte, offset int, ctx *context) {
	for _, lane := range scanner.blockScanPlan.unicode.simpleRepeats {
		var start int
		var ok bool
		if lane.repeat.wordBounded {
			start, ok = ctx.regexRepeats[lane.contextIndex].advanceWordBounded(lane.repeat, value, offset, data)
		} else {
			start, ok = ctx.regexRepeats[lane.contextIndex].advance(lane.repeat, value, offset)
		}
		if ok {
			for _, expressionIndex := range lane.consumers {
				expression := scanner.expressions[expressionIndex]
				ctx.readyEvents = append(ctx.readyEvents, scanEvent{match: Match{Id: expression.id, From: uint64(start), To: uint64(offset + 1)}, expressionIndex: expressionIndex})
			}
		}
	}
}

// appendUnicodeMixedLiteralEvents 是通用混合执行器仅 AC 的对应路径。它刻意批量处理一个
// 已解码 rune 的全部字节：ASCII 字面量不能在 UTF-8 延续字节结束，因此不会延迟可观察字节事件，
// 同时 UCP 扫描器保持精简热路径。
func (scanner *Scanner) appendUnicodeMixedLiteralEvents(data []byte, offset, end int, ctx *context) {
	for index := offset; index < end; index++ {
		ctx.state = scanner.automaton.transitions[ctx.state<<8|uint32(data[index])]
		for output := scanner.automaton.outputStart[ctx.state]; output < scanner.automaton.outputEnd[ctx.state]; output++ {
			scanner.appendBlockTriggerEvents(data, index, scanner.automaton.outputs[output], ctx)
		}
	}
}

func filterUnicodeAnchorEvents(events *[]scanEvent, end int, data []byte) {
	values := *events
	kept := values[:0]
	for _, value := range values {
		if (!value.anchorStart || value.match.From == 0) && (!value.anchorEnd || end == len(data)) && (!value.wordStart || unicodeWordBoundary(data, int(value.match.From))) && (!value.wordEnd || unicodeWordBoundary(data, int(value.match.To))) {
			kept = append(kept, value)
		}
	}
	*events = kept
}

func unicodeWordBoundary(data []byte, offset int) bool {
	var left, right rune
	if offset > 0 {
		left, _ = utf8.DecodeLastRune(data[:offset])
	}
	if offset < len(data) {
		right, _ = utf8.DecodeRune(data[offset:])
	}
	return unicodeWordRune(left) != unicodeWordRune(right)
}
func unicodeWordRune(value rune) bool {
	return unicode.IsLetter(value) || unicode.IsMark(value) || unicode.IsNumber(value) || unicode.Is(unicode.Categories["Pc"], value)
}

func dedupeUnicodePropertyEvents(events *[]scanEvent) {
	values := *events
	kept := values[:0]
	for _, value := range values {
		duplicate := false
		for _, prior := range kept {
			if prior.expressionIndex == value.expressionIndex && prior.match.From == value.match.From && prior.match.To == value.match.To {
				duplicate = true
				break
			}
		}
		if !duplicate {
			kept = append(kept, value)
		}
	}
	*events = kept
}

func (scanner *Scanner) scanUnicodePropertyNFA(program unicodePropertyProgram, run *unicodePropertyRun, value rune, offset, end int, events *[]scanEvent) {
	next := run.next[:0]
	for _, state := range run.active {
		next = scanner.advanceUnicodePropertyState(program, state, value, end, next, events)
	}
	next = scanner.advanceUnicodePropertyState(program, unicodePropertyState{start: offset}, value, end, next, events)
	run.next = run.active[:0]
	run.active = next
}

// scanUnicodePropertyGraph 推进带分组 UCP 表达式。配置由图状态和字节起始偏移标识；合并相同
// 配置可避免重叠分支产生重复结果。
func (scanner *Scanner) scanUnicodePropertyGraph(program unicodePropertyProgram, run *unicodePropertyRun, value rune, offset, end int, data []byte, events *[]scanEvent) {
	if program.hasAssertions {
		scanner.scanUnicodePropertyGraphWithAssertions(program, run, value, offset, end, data, events)
		return
	}
	next := run.next[:0]
	if !program.hasAlternation {
		for _, state := range run.active {
			next = scanner.advanceUnicodePropertyGraphStateFast(program, state, value, end, data, next, events)
		}
		next = scanner.advanceUnicodePropertyGraphStateFast(program, unicodePropertyState{start: offset, graphState: program.graph.start}, value, end, data, next, events)
		run.next = run.active[:0]
		run.active = next
		return
	}
	start := -1
	generation := uint32(0)
	for _, state := range run.active {
		// 活跃状态按起始偏移顺序产生。因此一个代际标记足以在同一起点内去重图目标，无须
		// 反复线性扫描不断增长的 next 切片。
		if state.start != start {
			start = state.start
			generation = run.nextGraphActiveGeneration()
		}
		next = scanner.advanceUnicodePropertyGraphState(program, run, generation, state, value, end, data, next, events)
	}
	next = scanner.advanceUnicodePropertyGraphState(program, run, run.nextGraphActiveGeneration(), unicodePropertyState{start: offset, graphState: program.graph.start}, value, end, data, next, events)
	run.next = run.active[:0]
	run.active = next
}

func (scanner *Scanner) scanUnicodePropertyGraphWithAssertions(program unicodePropertyProgram, run *unicodePropertyRun, value rune, offset, end int, data []byte, events *[]scanEvent) {
	next := run.next[:0]
	for _, state := range run.active {
		next = scanner.advanceUnicodePropertyGraphAssertionState(program, run, state, value, offset, end, data, next, events)
	}
	next = scanner.advanceUnicodePropertyGraphAssertionState(program, run, unicodePropertyState{start: offset, graphState: program.graph.start}, value, offset, end, data, next, events)
	run.next = run.active[:0]
	run.active = next
}

// advanceUnicodePropertyGraphAssertionState 在当前 rune 之前的边界和原子转移后计算零宽
// 断言。闭包遍历器拥有每调用位集，因此 (?:\b)* 等可空断言循环保持有界，且仅由常规空匹配
// 边界路径报告。
func (scanner *Scanner) advanceUnicodePropertyGraphAssertionState(program unicodePropertyProgram, run *unicodePropertyRun, state unicodePropertyState, value rune, offset, end int, data []byte, next []unicodePropertyState, events *[]scanEvent) []unicodePropertyState {
	graph := program.graph
	unicodePropertyGraphVisitAt(graph, run, state.graphState, data, offset, func(index uint16) {
		current := graph.states[index]
		target, ok := unicodePropertyGraphTransition(current, value, end, data)
		if !ok {
			return
		}
		unicodePropertyGraphVisitAt(graph, run, target, data, end, func(closureTarget uint16) {
			if graph.states[closureTarget].accept {
				scanner.appendUnicodePropertyGraphEvent(program, state.start, end, events)
			}
		})
		if unicodePropertyGraphCanConsumeAt(graph, run, target, data, end) {
			next = appendUniqueUnicodePropertyGraphState(next, unicodePropertyState{start: state.start, graphState: target})
		}
	})
	return next
}

func unicodePropertyGraphVisitAt(graph *unicodePropertyGraph, run *unicodePropertyRun, state uint16, data []byte, offset int, visit func(uint16)) {
	slot := int(run.graphDepth)
	if slot >= len(run.graphSeen) {
		// 执行器中断言回调最多嵌套两层。编译器限制图大小，该保护也使未来异常路径保持有界。
		return
	}
	run.graphDepth++
	defer func() { run.graphDepth-- }()
	run.graphGeneration[slot]++
	if run.graphGeneration[slot] == 0 {
		clear(run.graphSeen[slot])
		run.graphGeneration[slot] = 1
	}
	generation := run.graphGeneration[slot]
	stack := run.graphStack[slot][:0]
	stack = append(stack, state)
	for len(stack) != 0 {
		stackLen := len(stack) - 1
		base := stack[stackLen]
		stack = stack[:stackLen]
		for _, index := range graph.closures[base] {
			if run.graphSeen[slot][index] == generation {
				continue
			}
			run.graphSeen[slot][index] = generation
			current := graph.states[index]
			if !current.hasAssertion {
				visit(index)
				continue
			}
			if unicodePropertyAssertionMatches(current.assertion, current.multiline, data, offset) && len(stack) < cap(stack) {
				stack = append(stack, current.next)
			}
		}
	}
}

func unicodePropertyGraphCanConsumeAt(graph *unicodePropertyGraph, run *unicodePropertyRun, state uint16, data []byte, offset int) bool {
	canConsume := false
	unicodePropertyGraphVisitAt(graph, run, state, data, offset, func(index uint16) {
		canConsume = canConsume || unicodePropertyGraphStateCanConsume(graph.states[index])
	})
	return canConsume
}

func unicodePropertyGraphMatchesEmptyAt(graph *unicodePropertyGraph, run *unicodePropertyRun, data []byte, offset int) bool {
	matched := false
	unicodePropertyGraphVisitAt(graph, run, graph.start, data, offset, func(index uint16) {
		matched = matched || graph.states[index].accept
	})
	return matched
}

func unicodePropertyAssertionMatches(assertion unicodePropertyAssertion, multiline bool, data []byte, offset int) bool {
	switch assertion {
	case unicodePropertyAssertStart:
		return offset == 0 || multiline && offset > 0 && data[offset-1] == '\n'
	case unicodePropertyAssertEnd:
		return offset == len(data) || multiline && offset < len(data) && data[offset] == '\n'
	case unicodePropertyAssertEndBeforeFinalNewline:
		return offset == len(data) ||
			offset+1 == len(data) && data[offset] == '\n' ||
			offset+2 == len(data) && data[offset] == '\r' && data[offset+1] == '\n'
	case unicodePropertyAssertWordBoundary:
		return unicodeWordBoundary(data, offset)
	case unicodePropertyAssertNotWordBoundary:
		return !unicodeWordBoundary(data, offset)
	default:
		return false
	}
}

func unicodePropertyGraphStateCanConsume(state unicodePropertyGraphState) bool {
	return state.hasAtom || state.lineBreak || state.lineBreakCRWait
}

// unicodePropertyGraphTransition 推进一个消费型图状态。\R 有专用 CR 延续状态，使 CRLF
// 产生一个双 rune 匹配而非额外的单 rune CR 匹配。
func unicodePropertyGraphTransition(state unicodePropertyGraphState, value rune, end int, data []byte) (uint16, bool) {
	if state.hasAtom {
		return state.next, state.atom.matches(value)
	}
	if state.lineBreakCRWait {
		return state.next, value == '\n'
	}
	if !state.lineBreak {
		return 0, false
	}
	switch value {
	case '\r':
		if end < len(data) && data[end] == '\n' {
			return state.lineBreakCRContinuation, true
		}
	case '\n', '\v', '\f', '\u0085', '\u2028', '\u2029':
		return state.next, true
	default:
		return 0, false
	}
	return state.next, true
}

func (scanner *Scanner) advanceUnicodePropertyGraphStateFast(program unicodePropertyProgram, state unicodePropertyState, value rune, end int, data []byte, next []unicodePropertyState, events *[]scanEvent) []unicodePropertyState {
	graph := program.graph
	for _, index := range graph.closures[state.graphState] {
		current := graph.states[index]
		target, ok := unicodePropertyGraphTransition(current, value, end, data)
		if !ok {
			continue
		}
		if graph.accepts[target] {
			scanner.appendUnicodePropertyGraphEvent(program, state.start, end, events)
		}
		if graph.canConsume[target] {
			next = append(next, unicodePropertyState{start: state.start, graphState: target})
		}
	}
	return next
}

func (scanner *Scanner) advanceUnicodePropertyGraphState(program unicodePropertyProgram, run *unicodePropertyRun, generation uint32, state unicodePropertyState, value rune, end int, data []byte, next []unicodePropertyState, events *[]scanEvent) []unicodePropertyState {
	graph := program.graph
	for _, index := range graph.closures[state.graphState] {
		current := graph.states[index]
		target, ok := unicodePropertyGraphTransition(current, value, end, data)
		if !ok {
			continue
		}
		if graph.accepts[target] {
			scanner.appendUnicodePropertyGraphEvent(program, state.start, end, events)
		}
		// 仅保留直接目标。其 epsilon 闭包会在消费下一个 rune 前展开，避免为每个活跃起点复制闭包。
		if graph.canConsume[target] && run.graphActiveSeen[target] != generation {
			run.graphActiveSeen[target] = generation
			next = append(next, unicodePropertyState{start: state.start, graphState: target})
		}
	}
	return next
}

func (run *unicodePropertyRun) nextGraphActiveGeneration() uint32 {
	run.graphActiveGeneration++
	if run.graphActiveGeneration == 0 {
		clear(run.graphActiveSeen)
		run.graphActiveGeneration = 1
	}
	return run.graphActiveGeneration
}

// appendUniqueUnicodePropertyGraphState 保持在断言路径上。其闭包遍历可能按依赖 ctx 的顺序
// 访问目标，因此不具备无断言快路径使用的起点分组顺序。
func appendUniqueUnicodePropertyGraphState(states []unicodePropertyState, state unicodePropertyState) []unicodePropertyState {
	for _, prior := range states {
		if prior.start == state.start && prior.graphState == state.graphState {
			return states
		}
	}
	return append(states, state)
}

func (scanner *Scanner) appendUnicodePropertyGraphEvent(program unicodePropertyProgram, start, end int, events *[]scanEvent) {
	expression := scanner.expressions[program.expressionIndex]
	for _, prior := range *events {
		if prior.expressionIndex == program.expressionIndex && prior.match.From == uint64(start) && prior.match.To == uint64(end) {
			return
		}
	}
	*events = append(*events, scanEvent{
		match:           Match{Id: expression.id, From: uint64(start), To: uint64(end)},
		expressionIndex: program.expressionIndex,
		anchorStart:     program.anchorStart,
		anchorEnd:       program.anchorEnd,
		wordStart:       program.wordStart,
		wordEnd:         program.wordEnd,
	})
}

func (scanner *Scanner) advanceUnicodePropertyState(program unicodePropertyProgram, state unicodePropertyState, value rune, end int, next []unicodePropertyState, events *[]scanEvent) []unicodePropertyState {
	atom := program.sequence[state.atomIndex]
	if !atom.matches(value) {
		return next
	}
	state.count++
	if atom.maximum != unicodePropertyUnbounded && state.count > atom.maximum {
		return next
	}
	if state.count >= atom.minimum {
		if int(state.atomIndex)+1 == len(program.sequence) {
			expression := scanner.expressions[program.expressionIndex]
			*events = append(*events, scanEvent{match: Match{Id: expression.id, From: uint64(state.start), To: uint64(end)}, expressionIndex: program.expressionIndex, anchorStart: program.anchorStart, anchorEnd: program.anchorEnd, wordStart: program.wordStart, wordEnd: program.wordEnd})
		} else {
			next = append(next, unicodePropertyState{start: state.start, atomIndex: state.atomIndex + 1})
		}
	}
	if atom.maximum == unicodePropertyUnbounded || state.count < atom.maximum {
		next = append(next, state)
	}
	return next
}
