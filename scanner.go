// Package scankit 提供可移植的多规则编译扫描器。
//
// 它在一次连续块扫描中支持字面量表达式和已实现的正则表达式子集。
package scankit

import (
	"errors"
	"fmt"
	"math/bits"
	"slices"
	"sync"
	"unicode"
	"unicode/utf8"
)

var (
	// ErrEmptyExpressions 表示尝试编译空规则集。
	ErrEmptyExpressions = errors.New("empty expressions")

	// ErrDuplicateExpression 表示同一扫描器中存在重复的表达式 id。
	ErrDuplicateExpression = errors.New("duplicate expression")

	// ErrInvalidExpression 表示表达式无效。
	ErrInvalidExpression = errors.New("invalid expression")

	// ErrUnsupportedFlag 表示使用了当前尚未支持的编译标志。
	ErrUnsupportedFlag = errors.New("unsupported compile flag")

	// ErrInvalidUTF8 表示包含 CompileUTF8 表达式的扫描器收到了无效 UTF-8 输入。
	ErrInvalidUTF8 = errors.New("invalid UTF-8 input")

	// ErrUnsupportedExpression 表示使用了语法有效但尚未实现的正则表达式。
	ErrUnsupportedExpression = errors.New("unsupported expression")

	// ErrRegexTooComplex 表示表达式超出编译器资源限制。
	ErrRegexTooComplex = errors.New("regular expression is too complex")

	// ErrInvalidExtension 表示表达式扩展参数不一致。
	ErrInvalidExtension = errors.New("invalid expression extension")

	// ErrUnsupportedExtension 表示无法精确执行的扩展。
	ErrUnsupportedExtension = errors.New("unsupported expression extension")

	// ErrInvalidCombination 表示格式错误或无效的逻辑组合规则。
	ErrInvalidCombination = errors.New("invalid combination")
)

// CompileFlag 定义编译一条 Expression 时的匹配语义和结果交付方式。多个标志可按位组合。
type CompileFlag uint32

const (
	// CompileCaseless 启用大小写无关匹配。字节表达式仅折叠 ASCII；与
	// CompileUTF8 和 CompileUnicodeProperties 一同使用时，采用 Unicode simple fold。
	CompileCaseless CompileFlag = 1 << iota

	// CompileDotAll 使 . 与普通字符一样可以匹配换行符。
	CompileDotAll

	// CompileMultiline 使 ^ 和 $ 除输入首尾外，也能匹配每一行的首尾。
	CompileMultiline

	// CompileSingleMatch 使一条表达式在一次扫描中最多交付一个有效匹配。
	CompileSingleMatch

	// CompileAllowEmpty 允许能够匹配空字节或空码点序列的表达式；未设置时这类表达式会编译失败。
	CompileAllowEmpty

	// CompileUTF8 要求模式和扫描输入为合法 UTF-8。Unicode 正则语义还需要 CompileUnicodeProperties。
	CompileUTF8

	// CompileUnicodeProperties 启用 Unicode 属性和按 rune 执行的字符语义；必须与 CompileUTF8 一同设置。
	CompileUnicodeProperties

	// CompilePrefilter 请求预过滤语义。当前已支持的语法仍精确执行，不会扩大匹配结果集。
	CompilePrefilter

	// CompileLeftmostStart 要求同一表达式在同一结束位置有多个候选起点时，保留最靠左的起点。
	CompileLeftmostStart

	// CompileCombination 将 Pattern 视为表达式 id 的布尔组合，而不是正则表达式。
	CompileCombination

	// CompileQuiet 不单独交付该表达式的匹配，但该表达式仍可作为组合规则的操作数。
	CompileQuiet
)

// Expression 标识 Scanner 中的一条规则。
type Expression struct {
	Id      uint32
	Pattern string
	Flags   CompileFlag
	Ext     *ExpressionExt
}

// ExpressionExtFlag 选择 ExpressionExt 中生效的限制。只有设置了对应标志的字段才会生效；
// 未设置标志时字段值会被忽略，因此零值也是可以明确配置的有效值。
type ExpressionExtFlag uint64

const (
	// ExtMinOffset 使 MinOffset 生效：只保留结束边界距输入开头不少于 MinOffset 字节的匹配。
	ExtMinOffset ExpressionExtFlag = 1 << iota

	// ExtMaxOffset 使 MaxOffset 生效：只保留结束边界距输入开头不超过 MaxOffset 字节的匹配。
	ExtMaxOffset

	// ExtMinLength 使 MinLength 生效：只保留长度不少于 MinLength 字节的匹配。
	ExtMinLength

	// ExtEditDistance 使 EditDistance 生效：允许最多该次数的插入、删除或替换；值为 0 表示精确匹配。
	ExtEditDistance

	// ExtHammingDistance 使 HammingDistance 生效：允许最多该次数的等宽位置替换；值为 0 表示精确匹配。
	ExtHammingDistance
)

// ExpressionExt 为一条表达式配置匹配结果限制和近似匹配参数。Flags 决定哪些字段生效：
// MinOffset 和 MaxOffset 作用于匹配结束边界 Match.To，MinLength 作用于匹配字节长度
// Match.To-Match.From。
//
// EditDistance 与 HammingDistance 不能同时设置。近似匹配仅接受受资源上限约束的非空、
// 有界表达式：HammingDistance 还要求固定宽度；不满足条件时 Compile 返回 ErrUnsupportedExtension。
type ExpressionExt struct {
	Flags           ExpressionExtFlag
	MinOffset       uint64
	MaxOffset       uint64
	MinLength       uint64
	EditDistance    uint32
	HammingDistance uint32
}

// Match 描述输入中一个左闭右开的匹配字节区间。
type Match struct {
	Id   uint32
	From uint64
	To   uint64
}

// Scanner 是内存中的不可变编译规则执行计划，可安全地并发扫描，并管理所需的可复用扫描上下文。
type Scanner struct {
	expressions        []compiledExpression
	automaton          literalAutomaton
	triggers           []scanTrigger
	regexPrograms      []compiledRegexProgram
	unicodeProperties  []unicodePropertyProgram
	unicodeApproximate []unicodeApproximateProgram
	unicodeAlternation bool
	unicodeScanPlan    unicodeScanPlan
	anchoredGroups     [][]uint32
	anchoredNeeded     []bool
	unanchoredRegex    []uint32
	unanchoredGroups   [][]uint32
	unanchoredNeeded   []bool
	blockScanPlan      blockScanPlan
	// eventNeeded 标记事件会影响可观察结果的生产者表达式；不被组合规则使用的
	// Quiet 表达式会完全排除在扫描热路径之外。
	eventNeeded           []bool
	emptyRegex            []uint32
	hammingLiterals       []hammingLiteral
	hammingRegexes        []hammingFixedRegex
	hammingNFAs           []hammingNFA
	editLiterals          []editLiteral
	editRegexes           []editFixedRegex
	editClassRepeats      []editClassRepeat
	editNFAs              []editNFA
	maxEditLength         int
	advancedEvents        bool
	directLiterals        bool
	directSingleEvent     bool
	requiresUTF8          bool
	singleByteOnly        bool
	singleByteFast        bool
	singleByteSimple      bool
	singleRootFixedAnchor bool
	orderedPendingEvents  bool
	singleByteValues      [2]byte
	singleByteTriggers    [256][]scanTrigger
	singleByteRegex       [256]uint32
	combinations          []combinationProgram
	eventCapacity         int
	contextPool           *sync.Pool
}

// unicodeScanPlan 在 Scanner 编译时确定，避免 UCP 扫描器在每次块扫描时重新判断字节扫描形态。
type unicodeScanPlan uint8

const (
	unicodeScanPlanPure unicodeScanPlan = iota
	unicodeScanPlanLiteralAC
	unicodeScanPlanSimpleRepeats
	unicodeScanPlanGeneric
)

type compiledExpression struct {
	id         uint32
	length     uint32
	flags      CompileFlag
	constraint matchConstraint
}

type matchConstraint struct {
	minOffset    uint64
	maxOffset    uint64
	minLength    uint64
	hasMinOffset bool
	hasMaxOffset bool
	hasMinLength bool
}

func (constraint matchConstraint) accepts(match Match) bool {
	if constraint.hasMinOffset && match.To < constraint.minOffset {
		return false
	}
	if constraint.hasMaxOffset && match.To > constraint.maxOffset {
		return false
	}
	return !constraint.hasMinLength || match.To-match.From >= constraint.minLength
}

func compileMatchConstraint(extension *ExpressionExt) (matchConstraint, error) {
	if extension == nil {
		return matchConstraint{}, nil
	}
	if extension.Flags&^(ExtMinOffset|ExtMaxOffset|ExtMinLength|ExtEditDistance|ExtHammingDistance) != 0 {
		return matchConstraint{}, fmt.Errorf("%w: unknown flags %#x", ErrInvalidExtension, extension.Flags)
	}
	if extension.Flags&(ExtEditDistance|ExtHammingDistance) == ExtEditDistance|ExtHammingDistance {
		return matchConstraint{}, fmt.Errorf("%w: edit and Hamming distance are mutually exclusive", ErrInvalidExtension)
	}
	constraint := matchConstraint{
		minOffset:    extension.MinOffset,
		maxOffset:    extension.MaxOffset,
		minLength:    extension.MinLength,
		hasMinOffset: extension.Flags&ExtMinOffset != 0,
		hasMaxOffset: extension.Flags&ExtMaxOffset != 0,
		hasMinLength: extension.Flags&ExtMinLength != 0,
	}
	if constraint.hasMinOffset && constraint.hasMaxOffset && constraint.minOffset > constraint.maxOffset {
		return matchConstraint{}, fmt.Errorf("%w: minimum offset exceeds maximum offset", ErrInvalidExtension)
	}
	return constraint, nil
}

type scanTriggerKind uint8

const (
	scanLiteral scanTriggerKind = iota
	scanRegex
)

type scanTrigger struct {
	kind            scanTriggerKind
	expressionIndex uint32
	regexIndex      uint32
	regexGroupIndex uint32
}

// byteRegexPlanCacheKey 仅包含影响字节语言的标志。投递标志和扩展约束保留在
// 表达式级别，因此相同的可执行 IR 可安全共享，同时保持每条规则独立报告。
type byteRegexPlanCacheKey struct {
	pattern string
	flags   CompileFlag
}

const byteRegexPlanLanguageFlags = CompileCaseless | CompileDotAll | CompileMultiline

type compiledRegexProgram struct {
	expressionIndex     uint32
	unanchored          bool
	leftmostOnly        bool
	simpleRepeat        byteRegexRepeat
	hasSimpleRepeat     bool
	fixed               *fixedByteRegex
	fixedAnchor         *fixedByteRegexAnchor
	prefixClass         byteClass
	hasPrefixClass      bool
	suffixClass         byteClass
	hasSuffixClass      bool
	anchorMinOffset     uint32
	anchorMaxOffset     uint32
	anchorLength        uint32
	anchorByte          byte
	internalAnchor      bool
	internalPrefixClass byteClass
	internalLeading     string
	prefixDFAStates     []uint16
	maxLength           int
	program             nfaProgram
}

// byteRegexRepeat 是无锚点单字节类重复的紧凑执行器。它满足结果列表语义，且避免
// \d+ 等规则在通用 NFA 调度器中产生每线程哈希表开销。
type byteRegexRepeat struct {
	class       byteClass
	minimum     int
	maximum     int
	wordBounded bool
}

type byteRegexRepeatRun struct {
	start     int
	length    int
	wordStart bool
}

// unicodePropertyProgram 是首个码点执行路径。它与字节 NFA 分离，确保 UCP 规则
// 不会将 UTF-8 延续字节误判为字符。
type unicodePropertyProgram struct {
	expressionIndex uint32
	atom            unicodePropertyAtom
	sequence        []unicodePropertyAtom
	graph           *unicodePropertyGraph
	runeNFA         bool
	nullable        bool
	hasAlternation  bool
	hasAssertions   bool
	// flagsApplied 表示解析器局部修饰符已直接下沉到 rune 图中。调用方不得再次
	// 应用表达式级 CASELESS，因为作用域 (?-i:...) 节点必须保持大小写敏感。
	flagsApplied bool
	anchorStart  bool
	anchorEnd    bool
	wordStart    bool
	wordEnd      bool
}

type unicodePropertyAtom struct {
	matchers []unicodePropertyMatcher
	negated  bool
	minimum  uint32
	maximum  uint32
}

type unicodePropertyMatcher struct {
	table      *unicode.RangeTable
	posix      unicodePOSIXClass
	literal    rune
	rangeEnd   rune
	isLiteral  bool
	isRange    bool
	negated    bool
	caseless   bool
	any        bool
	dotAll     bool
	horizontal bool
	vertical   bool
	// ascii 缓存最常见 ASCII 输入路径的基础匹配结果。它在应用全部编译标志（尤其是
	// CASELESS）后生成，因此不会改变扫描语义。
	ascii [2]uint64
}

type unicodePropertyRun struct {
	starts                []int
	sequence              []unicodePropertyRune
	active                []unicodePropertyState
	next                  []unicodePropertyState
	graphSeen             [2][]uint32
	graphGeneration       [2]uint32
	graphStack            [2][]uint16
	graphDepth            uint8
	graphActiveSeen       []uint32
	graphActiveGeneration uint32
}

type unicodeApproximateProgram struct {
	expressionIndex uint32
	atoms           []unicodePropertyAtom
	graph           *unicodePropertyGraph
	minimumWidth    int
	maximumWidth    int
	distance        uint32
	hamming         bool
}

type unicodeApproximateRun struct {
	runes        []unicodePropertyRune
	previous     []uint16
	current      []uint16
	graphProduct unicodeGraphApproximateContext
}

type unicodePropertyRune struct {
	offset int
	value  rune
}

type unicodePropertyState struct {
	start      int
	atomIndex  uint32
	count      uint32
	graphState uint16
}

// unicodePropertyGraph 是基于已解码 rune 的紧凑 Thompson NFA，用于带分组的 UCP
// 表达式；更简单的单原子和定长序列路径继续使用开销更低的执行器。
type unicodePropertyGraph struct {
	states     []unicodePropertyGraphState
	closures   [][]uint16
	canConsume []bool
	accepts    []bool
	start      uint16
}

type unicodePropertyGraphState struct {
	epsilon                 []uint16
	atom                    unicodePropertyAtom
	next                    uint16
	lineBreakCRContinuation uint16
	assertion               unicodePropertyAssertion
	hasAtom                 bool
	hasAssertion            bool
	lineBreak               bool
	lineBreakCRWait         bool
	multiline               bool
	accept                  bool
}

type unicodePropertyAssertion uint8

const (
	unicodePropertyAssertStart unicodePropertyAssertion = iota + 1
	unicodePropertyAssertEnd
	unicodePropertyAssertWordBoundary
	unicodePropertyAssertNotWordBoundary
	unicodePropertyAssertEndBeforeFinalNewline
)

// unanchoredGroupKey 标识相同的已编译 NFA 及其起始位置保留策略。QUIET、SINGLEMATCH
// 等报告控制不参与键计算：事件会分发给各表达式，再由常规投递路径过滤。
type unanchoredGroupKey struct {
	program  string
	leftmost bool
}

// blockScanPlan 是一次连续输入扫描的不可变调度 IR。编译阶段会将每个触发通道解析到
// 对应执行器上下文槽位和可观察消费者，scanBlock 无须反复从分组索引查找代表程序及
// 表达式元数据。UCP 仍使用专用的 rune 执行器，但与本计划共享不可变的字节侧调度元数据。
type blockScanPlan struct {
	unanchored blockUnanchoredPlan
	triggers   []blockTriggerLane
	unicode    blockUnicodePlan
}

// blockUnicodePlan 记录 rune 感知扫描选择的字节侧工作。它与常规块扫描计划共享通道，
// 但绝不会将 UCP 程序交给字节执行器。
type blockUnicodePlan struct {
	scanPlan      unicodeScanPlan
	simpleRepeats []blockAlwaysLane
}

// blockTriggerLane 是一个 Aho-Corasick 输出的编译后目标。它不保存源分组索引，扫描时
// 可立即产生字面量事件，或调用已解析的锚定验证器和消费者。
type blockTriggerLane struct {
	kind     blockTriggerLaneKind
	literal  blockLiteralLane
	anchored blockAnchoredLane
}

type blockTriggerLaneKind uint8

const (
	blockTriggerInactive blockTriggerLaneKind = iota
	blockTriggerLiteral
	blockTriggerAnchored
)

type blockLiteralLane struct {
	expressionIndex uint32
	length          uint32
}

type blockAnchoredLane struct {
	contextIndex        uint32
	program             nfaProgram
	anchorMinOffset     uint32
	anchorMaxOffset     uint32
	anchorLength        uint32
	maxLength           int
	prefixClass         byteClass
	hasPrefixClass      bool
	suffixClass         byteClass
	hasSuffixClass      bool
	leftmost            bool
	internalAnchor      bool
	internalPrefixClass byteClass
	internalLeading     string
	consumers           []uint32
}

type blockUnanchoredPlan struct {
	fixed       [256][]blockFixedLane
	fixedAnchor [256][]blockFixedAnchorLane
	always      []blockAlwaysLane
}

type blockFixedLane struct {
	contextIndex uint32
	fixed        *fixedByteRegex
	consumers    []uint32
}

type blockFixedAnchorLane struct {
	contextIndex uint32
	program      nfaProgram
	anchor       fixedByteRegexAnchor
	consumers    []uint32
}

type blockAlwaysLane struct {
	contextIndex    uint32
	program         nfaProgram
	repeat          byteRegexRepeat
	hasSimpleRepeat bool
	leftmost        bool
	consumers       []uint32
}

func (plan blockUnanchoredPlan) hasLanes() bool {
	if len(plan.always) != 0 {
		return true
	}
	for _, lanes := range plan.fixed {
		if len(lanes) != 0 {
			return true
		}
	}
	for _, lanes := range plan.fixedAnchor {
		if len(lanes) != 0 {
			return true
		}
	}
	return false
}

func buildBlockScanPlan(programs []compiledRegexProgram, expressions []compiledExpression, groups [][]uint32, needed []bool, triggers []scanTrigger, anchoredGroups [][]uint32, anchoredNeeded []bool, eventNeeded []bool, advancedEvents bool) blockScanPlan {
	var plan blockScanPlan
	for groupIndex, group := range groups {
		if !needed[groupIndex] {
			continue
		}
		representative := group[0]
		regex := programs[representative]
		consumers := make([]uint32, 0, len(group))
		for _, regexIndex := range group {
			expressionIndex := programs[regexIndex].expressionIndex
			if eventNeeded[expressionIndex] {
				consumers = append(consumers, expressionIndex)
			}
		}
		switch {
		case regex.fixed != nil:
			lane := blockFixedLane{contextIndex: representative, fixed: regex.fixed, consumers: consumers}
			for value := range plan.unanchored.fixed {
				if len(regex.fixed.sequenceTrigger[value]) != 0 {
					plan.unanchored.fixed[value] = append(plan.unanchored.fixed[value], lane)
				}
			}
		case regex.fixedAnchor != nil:
			lane := blockFixedAnchorLane{contextIndex: representative, program: regex.program, anchor: *regex.fixedAnchor, consumers: consumers}
			for value := range plan.unanchored.fixedAnchor {
				if regex.fixedAnchor.class.contains(byte(value)) {
					plan.unanchored.fixedAnchor[value] = append(plan.unanchored.fixedAnchor[value], lane)
				}
			}
		default:
			plan.unanchored.always = append(plan.unanchored.always, blockAlwaysLane{
				contextIndex:    representative,
				program:         regex.program,
				repeat:          regex.simpleRepeat,
				hasSimpleRepeat: regex.hasSimpleRepeat,
				leftmost:        expressions[regex.expressionIndex].flags&CompileLeftmostStart != 0,
				consumers:       consumers,
			})
		}
	}
	plan.triggers = make([]blockTriggerLane, len(triggers))
	for triggerIndex, trigger := range triggers {
		switch trigger.kind {
		case scanLiteral:
			if !eventNeeded[trigger.expressionIndex] {
				continue
			}
			plan.triggers[triggerIndex] = blockTriggerLane{
				kind: blockTriggerLiteral,
				literal: blockLiteralLane{
					expressionIndex: trigger.expressionIndex,
					length:          expressions[trigger.expressionIndex].length,
				},
			}
		case scanRegex:
			groupIndex := int(trigger.regexGroupIndex)
			if !anchoredNeeded[groupIndex] {
				continue
			}
			group := anchoredGroups[groupIndex]
			representative := group[0]
			regex := programs[representative]
			consumers := make([]uint32, 0, len(group))
			for _, regexIndex := range group {
				expressionIndex := programs[regexIndex].expressionIndex
				if eventNeeded[expressionIndex] {
					consumers = append(consumers, expressionIndex)
				}
			}
			plan.triggers[triggerIndex] = blockTriggerLane{
				kind: blockTriggerAnchored,
				anchored: blockAnchoredLane{
					contextIndex:        representative,
					program:             regex.program,
					anchorMinOffset:     regex.anchorMinOffset,
					anchorMaxOffset:     regex.anchorMaxOffset,
					anchorLength:        regex.anchorLength,
					maxLength:           regex.maxLength,
					prefixClass:         regex.prefixClass,
					hasPrefixClass:      regex.hasPrefixClass,
					suffixClass:         regex.suffixClass,
					hasSuffixClass:      regex.hasSuffixClass,
					leftmost:            regex.leftmostOnly,
					internalAnchor:      regex.internalAnchor,
					internalPrefixClass: regex.internalPrefixClass,
					internalLeading:     regex.internalLeading,
					consumers:           consumers,
				},
			}
		}
	}
	plan.unicode = buildBlockUnicodePlan(plan, advancedEvents)
	return plan
}

func hasSingleRootFixedAnchoredTrigger(automaton literalAutomaton, plan blockScanPlan) bool {
	if !automaton.rootByteFast || automaton.rootByteCount != 1 || len(plan.triggers) != 1 {
		return false
	}
	lane := plan.triggers[0]
	return lane.kind == blockTriggerAnchored &&
		lane.anchored.anchorMinOffset == 0 &&
		lane.anchored.anchorMaxOffset == 0 &&
		lane.anchored.anchorLength >= 2
}

// anchoredGroupKey 与无锚点 NFA 共享使用相同的语言键。编译器对同一模式的锚点选择
// 是确定的，因此每个成员也拥有相同的字面量触发器和验证程序。
type anchoredGroupKey struct {
	program             string
	anchorMinOffset     uint32
	anchorMaxOffset     uint32
	anchorLength        uint32
	maxLength           int
	internal            bool
	internalPrefixClass byteClass
	internalLeading     string
	prefixClass         byteClass
	hasPrefixClass      bool
	suffixClass         byteClass
	hasSuffixClass      bool
	leftmost            bool
}

// hammingLiteral 是精确的定宽近似匹配器。汉明距离不允许插入或删除，因此可在单个
// 输入结束偏移处判断，无须重放正则表达式执行器。
type hammingLiteral struct {
	expressionIndex uint32
	pattern         string
	distance        uint32
}

// hammingFixedRegex 将精确汉明匹配扩展到有界定宽字节正则（字符类、分支和精确重复），
// 不支持通用 NFA 的编辑距离。
type hammingFixedRegex struct {
	expressionIndex uint32
	sequences       []fixedByteRegexSequence
	distance        uint32
}

// editLiteral 是纯字节字面量的有界精确 Levenshtein 匹配器，其限制使可移植的动态规划
// 路径保持可预测。
type editLiteral struct {
	expressionIndex uint32
	pattern         string
	distance        uint32
}

type editFixedRegex struct {
	expressionIndex uint32
	sequences       []fixedByteRegexSequence
	distance        uint32
}

// editClassRepeat 表示 [class]{minimum,maximum}。它等价于重复的单字节分支，但其
// 编辑距离可由候选窗口的失配数判断，无须 NFA 乘积。
type editClassRepeat struct {
	expressionIndex uint32
	class           byteClass
	minimum         int
	maximum         int
	distance        uint32
}

const (
	maxEditLiteralLength   = 256
	maxEditLiteralDistance = 64
)

// Scan 扫描一个输入，并按稳定投递顺序返回全部匹配事件。返回切片由调用方拥有。
func (scanner *Scanner) Scan(data []byte) ([]Match, error) {
	return scanner.ScanInto(data, nil)
}

// ScanInto 将全部匹配事件追加到 matches 并返回结果切片。Scanner 会复用内部扫描上下文；
// 提供足够的结果容量可避免结果列表分配。
func (scanner *Scanner) ScanInto(data []byte, matches []Match) ([]Match, error) {
	ctx, _ := scanner.contextPool.Get().(*context)
	if ctx == nil {
		ctx = scanner.newContext()
	}
	err := scanner.scanBlock(data, ctx, &matches)
	scanner.contextPool.Put(ctx)
	return matches, err
}

// appendBlockTriggerEvents 消费一个已解析的 Aho-Corasick 输出。该通道是编译阶段组装的
// 不可变 Scanner 状态，因此热路径无需通过触发分组恢复锚定正则或过滤消费者。
func (scanner *Scanner) appendBlockTriggerEvents(data []byte, offset int, triggerIndex uint32, ctx *context) {
	lane := scanner.blockScanPlan.triggers[triggerIndex]
	switch lane.kind {
	case blockTriggerLiteral:
		expression := scanner.expressions[lane.literal.expressionIndex]
		ctx.readyEvents = append(ctx.readyEvents, scanEvent{
			match:           Match{Id: expression.id, From: uint64(offset + 1 - int(lane.literal.length)), To: uint64(offset + 1)},
			expressionIndex: lane.literal.expressionIndex,
		})
	case blockTriggerAnchored:
		anchored := lane.anchored
		anchorStart := offset + 1 - int(anchored.anchorLength)
		if anchored.internalAnchor {
			classStart := anchorStart
			for classStart > 0 && anchored.internalPrefixClass.contains(data[classStart-1]) {
				classStart--
			}
			if anchorStart-classStart < int(anchored.anchorMinOffset) {
				return
			}
			start := classStart - len(anchored.internalLeading)
			if start < 0 || !matchesInternalLeading(data, start, anchored.internalLeading) {
				return
			}
			verifier := ctx.regexVerifiers[anchored.contextIndex]
			for _, end := range verifier.matchFromLimit(anchored.program, data, start, regexMatchLimit(anchored.maxLength, len(data)-start)) {
				for _, expressionIndex := range anchored.consumers {
					expression := scanner.expressions[expressionIndex]
					event := scanEvent{match: Match{Id: expression.id, From: uint64(start), To: uint64(end)}, expressionIndex: expressionIndex}
					if end == offset+1 {
						ctx.readyEvents = append(ctx.readyEvents, event)
						continue
					}
					ctx.pushPendingEvent(event)
				}
			}
			return
		}
		if anchorStart < int(anchored.anchorMinOffset) {
			return
		}
		if !matchesAnchoredSuffix(data, anchorStart, anchored.anchorLength, anchored.suffixClass, anchored.hasSuffixClass) {
			return
		}
		startMin := anchorStart - int(anchored.anchorMaxOffset)
		if startMin < 0 {
			startMin = 0
		}
		startMax := anchorStart - int(anchored.anchorMinOffset)
		if anchored.hasPrefixClass {
			startMin = anchorStart
			for startMin > 0 && anchorStart-startMin < int(anchored.anchorMaxOffset) && anchored.prefixClass.contains(data[startMin-1]) {
				startMin--
			}
			startMax = startMin
		}
		verifier := ctx.regexVerifiers[anchored.contextIndex]
		for start := startMin; start <= startMax; start++ {
			for _, end := range verifier.matchFromLimit(anchored.program, data, start, regexMatchLimit(anchored.maxLength, len(data)-start)) {
				for _, expressionIndex := range anchored.consumers {
					expression := scanner.expressions[expressionIndex]
					event := scanEvent{match: Match{Id: expression.id, From: uint64(start), To: uint64(end)}, expressionIndex: expressionIndex}
					if end == offset+1 {
						ctx.readyEvents = append(ctx.readyEvents, event)
						continue
					}
					ctx.pushPendingEvent(event)
				}
			}
			if anchored.leftmost && len(verifier.ends) != 0 {
				break
			}
		}
	default:
	}
}

func matchesInternalLeading(data []byte, start int, leading string) bool {
	if start < 0 || start+len(leading) > len(data) {
		return false
	}
	for index := range leading {
		if data[start+index] != leading[index] {
			return false
		}
	}
	return true
}

func matchesAnchoredSuffix(data []byte, anchorStart int, anchorLength uint32, suffixClass byteClass, hasSuffixClass bool) bool {
	if !hasSuffixClass {
		return true
	}
	suffixOffset := anchorStart + int(anchorLength)
	return suffixOffset < len(data) && suffixClass.contains(data[suffixOffset])
}

// appendBlockUnanchoredEvents 在一个输入字节上推进已编译字节通道一次。常规块扫描器和
// UCP 混合扫描器均使用此方法，UCP 规则仍只在已解码 rune 上推进。
func (scanner *Scanner) appendBlockUnanchoredEvents(data []byte, value byte, offset int, ctx *context) {
	for _, lane := range scanner.blockScanPlan.unanchored.fixed[value] {
		for _, found := range ctx.regexFixed[lane.contextIndex].advance(lane.fixed, data, offset) {
			for _, expressionIndex := range lane.consumers {
				expression := scanner.expressions[expressionIndex]
				event := scanEvent{match: Match{Id: expression.id, From: uint64(found.start), To: uint64(found.end)}, expressionIndex: expressionIndex}
				if found.end == offset+1 {
					ctx.readyEvents = append(ctx.readyEvents, event)
				} else {
					ctx.pushPendingEvent(event)
				}
			}
		}
	}
	for _, lane := range scanner.blockScanPlan.unanchored.fixedAnchor[value] {
		anchor := lane.anchor
		start := offset - anchor.offset
		if start < 0 || start+anchor.width > len(data) {
			continue
		}
		verifier := ctx.regexVerifiers[lane.contextIndex]
		for _, end := range verifier.matchFromLimit(lane.program, data, start, anchor.width) {
			for _, expressionIndex := range lane.consumers {
				expression := scanner.expressions[expressionIndex]
				event := scanEvent{match: Match{Id: expression.id, From: uint64(start), To: uint64(end)}, expressionIndex: expressionIndex}
				if end == offset+1 {
					ctx.readyEvents = append(ctx.readyEvents, event)
				} else {
					ctx.pushPendingEvent(event)
				}
			}
		}
	}
	for _, lane := range scanner.blockScanPlan.unanchored.always {
		if lane.hasSimpleRepeat {
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
			continue
		}
		runner := ctx.regexRunners[lane.contextIndex]
		if runner == nil {
			continue
		}
		for _, thread := range runner.advance(lane.program, value, data, offset, uint64(offset)) {
			for _, expressionIndex := range lane.consumers {
				expression := scanner.expressions[expressionIndex]
				ctx.readyEvents = append(ctx.readyEvents, scanEvent{
					match:           Match{Id: expression.id, From: thread.start, To: uint64(offset + 1)},
					expressionIndex: expressionIndex,
				})
			}
		}
	}
}

func (scanner *Scanner) scanBlock(data []byte, ctx *context, matches *[]Match) error {
	if scanner.requiresUTF8 && !utf8.Valid(data) {
		return ErrInvalidUTF8
	}
	ctx.pendingFIFO = scanner.orderedPendingEvents
	ctx.pendingHead = 0
	ctx.pendingCount = 0
	ctx.pendingFirstEnd = 0
	defer func() {
		ctx.state = 0
		ctx.pendingEvents = ctx.pendingEvents[:0]
		ctx.pendingFIFO = true
		ctx.pendingHead = 0
		ctx.pendingCount = 0
		ctx.pendingFirstEnd = 0
		ctx.readyEvents = ctx.readyEvents[:0]
		clear(ctx.singleMatched)
		clear(ctx.combinationSeen)
		clear(ctx.combinationOn)
		for _, runner := range ctx.regexRunners {
			if runner != nil {
				runner.reset()
			}
		}
		for index := range ctx.regexRepeats {
			ctx.regexRepeats[index].reset()
		}
		for index := range ctx.regexFixed {
			ctx.regexFixed[index].matches = ctx.regexFixed[index].matches[:0]
		}
		for index := range ctx.unicodeRuns {
			ctx.unicodeRuns[index].starts = ctx.unicodeRuns[index].starts[:0]
			ctx.unicodeRuns[index].sequence = ctx.unicodeRuns[index].sequence[:0]
			ctx.unicodeRuns[index].active = ctx.unicodeRuns[index].active[:0]
			ctx.unicodeRuns[index].next = ctx.unicodeRuns[index].next[:0]
			ctx.unicodeRuns[index].graphDepth = 0
		}
		for index := range ctx.unicodeApprox {
			ctx.unicodeApprox[index].runes = ctx.unicodeApprox[index].runes[:0]
		}
	}()
	if scanner.directLiterals {
		return scanner.scanBlockDirectLiterals(data, matches)
	}
	if len(scanner.unicodeProperties) != 0 || len(scanner.unicodeApproximate) != 0 {
		return scanner.scanBlockUnicodeProperties(data, ctx, matches)
	}
	if scanner.automaton.sparse && len(scanner.regexPrograms) == 0 && !scanner.advancedEvents {
		return scanner.scanBlockSparseLiterals(data, ctx, matches)
	}
	if scanner.automaton.rootByteFast && scanner.automaton.rootByteCount <= 2 && singleByteWordScanAvailable && len(scanner.regexPrograms) == 0 && !scanner.advancedEvents {
		return scanner.scanBlockRootByteLiteralsWord(data, ctx, matches)
	}
	if len(scanner.unanchoredGroups) == 0 && !scanner.advancedEvents {
		if scanner.singleByteOnly {
			if scanner.singleByteFast && singleByteWordScanAvailable {
				return scanner.scanBlockSingleByteAnchorsWord(data, ctx, matches)
			}
			return scanner.scanBlockSingleByteAnchors(data, ctx, matches)
		}
		if scanner.automaton.rootByteFast && singleByteWordScanAvailable {
			if scanner.singleRootFixedAnchor {
				return scanner.scanBlockRootByteAnchoredWordSingle(data, ctx, matches)
			}
			return scanner.scanBlockRootByteAnchoredWord(data, ctx, matches)
		}
		return scanner.scanBlockAnchoredOnly(data, ctx, matches)
	}
	state := uint32(0)
	if scanner.advancedEvents && len(scanner.emptyRegex) != 0 {
		scanner.appendEmptyRegexEvents(&ctx.readyEvents, 0, data)
	}
	if len(ctx.readyEvents) != 0 {
		collectScanEvents(scanner, ctx, 0, matches)
	}
	transitions := scanner.automaton.transitions
	unanchoredPlan := scanner.blockScanPlan.unanchored
	hasUnanchored := unanchoredPlan.hasLanes()
	advancedEvents := scanner.advancedEvents
	for offset := 0; offset < len(data); offset++ {
		b := data[offset]
		state = transitions[state<<8|uint32(b)]
		outputEnd := scanner.automaton.outputEnd[state]
		if outputEnd != 0 {
			outputStart := scanner.automaton.outputStart[state]
			for outputIndex := outputStart; outputIndex < outputEnd; outputIndex++ {
				scanner.appendBlockTriggerEvents(data, offset, scanner.automaton.outputs[outputIndex], ctx)
			}
		}
		if hasUnanchored {
			for _, lane := range unanchoredPlan.fixed[b] {
				for _, found := range ctx.regexFixed[lane.contextIndex].advance(lane.fixed, data, offset) {
					for _, expressionIndex := range lane.consumers {
						expression := scanner.expressions[expressionIndex]
						event := scanEvent{match: Match{Id: expression.id, From: uint64(found.start), To: uint64(found.end)}, expressionIndex: expressionIndex}
						if found.end == offset+1 {
							ctx.readyEvents = append(ctx.readyEvents, event)
						} else {
							ctx.pushPendingEvent(event)
						}
					}
				}
			}
			for _, lane := range unanchoredPlan.fixedAnchor[b] {
				anchor := lane.anchor
				start := offset - anchor.offset
				if start < 0 || start+anchor.width > len(data) {
					continue
				}
				verifier := ctx.regexVerifiers[lane.contextIndex]
				for _, end := range verifier.matchFromLimit(lane.program, data, start, anchor.width) {
					for _, expressionIndex := range lane.consumers {
						expression := scanner.expressions[expressionIndex]
						event := scanEvent{match: Match{Id: expression.id, From: uint64(start), To: uint64(end)}, expressionIndex: expressionIndex}
						if end == offset+1 {
							ctx.readyEvents = append(ctx.readyEvents, event)
						} else {
							ctx.pushPendingEvent(event)
						}
					}
				}
			}
			for _, lane := range unanchoredPlan.always {
				if lane.hasSimpleRepeat {
					var start int
					var ok bool
					if lane.repeat.wordBounded {
						start, ok = ctx.regexRepeats[lane.contextIndex].advanceWordBounded(lane.repeat, b, offset, data)
					} else {
						start, ok = ctx.regexRepeats[lane.contextIndex].advance(lane.repeat, b, offset)
					}
					if ok {
						for _, expressionIndex := range lane.consumers {
							expression := scanner.expressions[expressionIndex]
							ctx.readyEvents = append(ctx.readyEvents, scanEvent{match: Match{Id: expression.id, From: uint64(start), To: uint64(offset + 1)}, expressionIndex: expressionIndex})
						}
					}
					continue
				}
				runner := ctx.regexRunners[lane.contextIndex]
				if runner == nil {
					continue
				}
				for _, thread := range runner.advance(lane.program, b, data, offset, uint64(offset)) {
					for _, expressionIndex := range lane.consumers {
						expression := scanner.expressions[expressionIndex]
						ctx.readyEvents = append(ctx.readyEvents, scanEvent{
							match:           Match{Id: expression.id, From: thread.start, To: uint64(offset + 1)},
							expressionIndex: expressionIndex,
						})
					}
				}
			}
		}
		if advancedEvents {
			scanner.appendBlockHammingEvents(&ctx.readyEvents, ctx, data, offset+1)
			scanner.appendBlockEditEvents(&ctx.readyEvents, ctx, data, offset+1)
			scanner.appendEmptyRegexEvents(&ctx.readyEvents, uint64(offset+1), data)
		}
		end := uint64(offset + 1)
		if len(ctx.readyEvents) != 0 || ctx.pendingCount != 0 && ctx.pendingFirstEnd <= end {
			collectScanEvents(scanner, ctx, offset+1, matches)
		}
	}
	return nil
}

// scanBlockDirectLiterals 仅处理没有任何结果过滤语义的普通字面量规则。AC 输出已按表达式
// 索引稳定排序，因此可直接写入结果，避免为每个命中构造、排序和消解中间事件。
func (scanner *Scanner) scanBlockDirectLiterals(data []byte, matches *[]Match) error {
	state := uint32(0)
	for offset, value := range data {
		if scanner.automaton.sparse {
			state = scanner.automaton.nextSparse(state, value)
		} else {
			state = scanner.automaton.transitions[state<<8|uint32(value)]
		}
		for outputIndex := scanner.automaton.outputStart[state]; outputIndex < scanner.automaton.outputEnd[state]; outputIndex++ {
			trigger := scanner.triggers[scanner.automaton.outputs[outputIndex]]
			expression := scanner.expressions[trigger.expressionIndex]
			*matches = append(*matches, Match{
				Id:   expression.id,
				From: uint64(offset + 1 - int(expression.length)),
				To:   uint64(offset + 1),
			})
		}
	}
	return nil
}

// scanBlockSparseLiterals 仅用于转移表超过 denseLiteralTransitionLimit 的纯精确字面量
// 数据库。组合投递仍走常规事件路径，因此稀疏遍历只改变 Aho-Corasick 查找字面量终点的方式。
func (scanner *Scanner) scanBlockSparseLiterals(data []byte, ctx *context, matches *[]Match) error {
	state := uint32(0)
	for offset, value := range data {
		state = scanner.automaton.nextSparse(state, value)
		outputEnd := scanner.automaton.outputEnd[state]
		if outputEnd != 0 {
			outputStart := scanner.automaton.outputStart[state]
			for outputIndex := outputStart; outputIndex < outputEnd; outputIndex++ {
				scanner.appendBlockTriggerEvents(data, offset, scanner.automaton.outputs[outputIndex], ctx)
			}
		}
		end := uint64(offset + 1)
		if len(ctx.readyEvents) != 0 || ctx.pendingCount != 0 && ctx.pendingFirstEnd <= end {
			collectScanEvents(scanner, ctx, offset+1, matches)
		}
	}
	return nil
}

// scanBlockRootByteLiteralsWord 仅在自动机位于根节点且至多两个根边字节均未出现时跳过完整机器字。
// 该路径只匹配字面量，被跳过的字节不能产生就绪或待处理事件；遇到候选字节后恢复常规 Aho-Corasick 遍历。
func (scanner *Scanner) scanBlockRootByteLiteralsWord(data []byte, ctx *context, matches *[]Match) error {
	first, second := scanner.automaton.rootByteVals[0], scanner.automaton.rootByteVals[1]
	transitions := scanner.automaton.transitions
	state := uint32(0)
	offset := 0
	for offset < len(data) {
		if state == 0 && offset+8 <= len(data) && singleByteTriggerMask(data, offset, first, second) == 0 {
			offset += 8
			continue
		}
		value := data[offset]
		state = transitions[state<<8|uint32(value)]
		outputEnd := scanner.automaton.outputEnd[state]
		if outputEnd != 0 {
			outputStart := scanner.automaton.outputStart[state]
			for outputIndex := outputStart; outputIndex < outputEnd; outputIndex++ {
				scanner.appendBlockTriggerEvents(data, offset, scanner.automaton.outputs[outputIndex], ctx)
			}
		}
		end := uint64(offset + 1)
		if len(ctx.readyEvents) != 0 || ctx.pendingCount != 0 && ctx.pendingFirstEnd <= end {
			collectScanEvents(scanner, ctx, offset+1, matches)
		}
		offset++
	}
	return nil
}

// scanBlockRootByteAnchoredWord 是用于最多八个不同根字节、由字面量触发正则的数据库的
// 稠密 Aho-Corasick 执行器。根状态必须消费根边字节才能离开零状态，因此没有根边字节的
// 完整机器字可以跳过；自动机离开根节点后必须恢复常规遍历，因为失败转移可能进入另一前缀。
//
// 待处理正则事件也是可观察边界，不能跨越其结束偏移跳过，以保持与逐字节扫描相同的投递结果。
func (scanner *Scanner) scanBlockRootByteAnchoredWord(data []byte, ctx *context, matches *[]Match) error {
	transitions := scanner.automaton.transitions
	state := uint32(0)
	offset := 0
	for offset < len(data) {
		wordEnd := offset + 8
		if state == 0 && wordEnd <= len(data) &&
			rootByteTriggerMask(data, offset, scanner.automaton.rootByteVals, scanner.automaton.rootByteCount) == 0 &&
			(ctx.pendingCount == 0 || ctx.pendingFirstEnd > uint64(wordEnd)) {
			offset = wordEnd
			continue
		}

		value := data[offset]
		state = transitions[state<<8|uint32(value)]
		outputEnd := scanner.automaton.outputEnd[state]
		if outputEnd != 0 {
			outputStart := scanner.automaton.outputStart[state]
			for outputIndex := outputStart; outputIndex < outputEnd; outputIndex++ {
				scanner.appendBlockTriggerEvents(data, offset, scanner.automaton.outputs[outputIndex], ctx)
			}
		}
		end := uint64(offset + 1)
		if len(ctx.readyEvents) != 0 || ctx.pendingCount != 0 && ctx.pendingFirstEnd <= end {
			collectScanEvents(scanner, ctx, offset+1, matches)
		}
		offset++
	}
	return nil
}

// scanBlockRootByteAnchoredWordSingle 是通用根机器字扫描器的单根字节版本。单条锚定正则
// （例如银行卡前缀）在 PII 场景常见；特化候选掩码可让无触发循环不依赖多根分派。
func (scanner *Scanner) scanBlockRootByteAnchoredWordSingle(data []byte, ctx *context, matches *[]Match) error {
	transitions := scanner.automaton.transitions
	rootByte := scanner.automaton.rootByteVals[0]
	state := uint32(0)
	offset := 0
	for offset < len(data) {
		wordEnd := offset + 8
		if state == 0 && wordEnd <= len(data) &&
			rootByteSingleTriggerMask(data, offset, rootByte) == 0 &&
			(ctx.pendingCount == 0 || ctx.pendingFirstEnd > uint64(wordEnd)) {
			offset = wordEnd
			continue
		}

		value := data[offset]
		state = transitions[state<<8|uint32(value)]
		outputEnd := scanner.automaton.outputEnd[state]
		if outputEnd != 0 {
			outputStart := scanner.automaton.outputStart[state]
			for outputIndex := outputStart; outputIndex < outputEnd; outputIndex++ {
				scanner.appendBlockTriggerEvents(data, offset, scanner.automaton.outputs[outputIndex], ctx)
			}
		}
		end := uint64(offset + 1)
		if len(ctx.readyEvents) != 0 || ctx.pendingCount != 0 && ctx.pendingFirstEnd <= end {
			collectScanEvents(scanner, ctx, offset+1, matches)
		}
		offset++
	}
	return nil
}

func (scanner *Scanner) appendSingleByteTriggers(data []byte, ctx *context, offset int, triggers []scanTrigger) {
	for _, trigger := range triggers {
		if trigger.kind == scanLiteral {
			if !scanner.eventNeeded[trigger.expressionIndex] {
				continue
			}
			expression := scanner.expressions[trigger.expressionIndex]
			ctx.readyEvents = append(ctx.readyEvents, scanEvent{
				match:           Match{Id: expression.id, From: uint64(offset), To: uint64(offset + 1)},
				expressionIndex: trigger.expressionIndex,
			})
			continue
		}

		groupIndex := int(trigger.regexGroupIndex)
		if !scanner.anchoredNeeded[groupIndex] {
			continue
		}
		group := scanner.anchoredGroups[groupIndex]
		representative := group[0]
		regex := &scanner.regexPrograms[representative]
		verifier := ctx.regexVerifiers[representative]
		anchorStart := offset
		if anchorStart < int(regex.anchorMinOffset) {
			continue
		}
		if !matchesAnchoredSuffix(data, anchorStart, regex.anchorLength, regex.suffixClass, regex.hasSuffixClass) {
			continue
		}
		startMin := anchorStart - int(regex.anchorMaxOffset)
		if startMin < 0 {
			startMin = 0
		}
		startMax := anchorStart - int(regex.anchorMinOffset)
		if regex.hasPrefixClass {
			startMin = anchorStart
			for startMin > 0 && anchorStart-startMin < int(regex.anchorMaxOffset) && regex.prefixClass.contains(data[startMin-1]) {
				startMin--
			}
			startMax = startMin
		}
		for start := startMin; start <= startMax; start++ {
			var ends []int
			prefixLength := anchorStart - start
			if prefixLength > 0 && prefixLength < len(regex.prefixDFAStates) {
				endLimit := start + regexMatchLimit(regex.maxLength, len(data)-start)
				ends = verifier.matchFromDFAState(regex.program.verifierDFA, data, anchorStart, endLimit, regex.prefixDFAStates[prefixLength])
			} else {
				ends = verifier.matchFromLimit(regex.program, data, start, regexMatchLimit(regex.maxLength, len(data)-start))
			}
			for _, end := range ends {
				for _, regexIndex := range group {
					expressionIndex := scanner.regexPrograms[regexIndex].expressionIndex
					if !scanner.eventNeeded[expressionIndex] {
						continue
					}
					expression := scanner.expressions[expressionIndex]
					event := scanEvent{match: Match{Id: expression.id, From: uint64(start), To: uint64(end)}, expressionIndex: expressionIndex}
					if end == offset+1 {
						ctx.readyEvents = append(ctx.readyEvents, event)
						continue
					}
					ctx.pushPendingEvent(event)
				}
			}
			if regex.leftmostOnly && len(verifier.ends) != 0 {
				break
			}
		}
	}
}

// appendSingleByteRegex 是单个可观察正则消费者的字节触发器直达路径。仅当扫描时没有
// 规则共享或消费者扇出时，Compile 才会启用它；常规结果过滤仍在 collectScanEvents 中完成。
func (scanner *Scanner) appendSingleByteRegex(data []byte, ctx *context, offset int, regexIndex uint32) {
	regex := &scanner.regexPrograms[regexIndex]
	verifier := ctx.regexVerifiers[regexIndex]
	anchorStart := offset
	if anchorStart < int(regex.anchorMinOffset) {
		return
	}
	if !matchesAnchoredSuffix(data, anchorStart, regex.anchorLength, regex.suffixClass, regex.hasSuffixClass) {
		return
	}
	startMin := anchorStart - int(regex.anchorMaxOffset)
	if startMin < 0 {
		startMin = 0
	}
	startMax := anchorStart - int(regex.anchorMinOffset)
	if regex.hasPrefixClass {
		startMin = anchorStart
		for startMin > 0 && anchorStart-startMin < int(regex.anchorMaxOffset) && regex.prefixClass.contains(data[startMin-1]) {
			startMin--
		}
		startMax = startMin
	}
	expression := scanner.expressions[regex.expressionIndex]
	for start := startMin; start <= startMax; start++ {
		var ends []int
		prefixLength := anchorStart - start
		if prefixLength > 0 && prefixLength < len(regex.prefixDFAStates) {
			endLimit := start + regexMatchLimit(regex.maxLength, len(data)-start)
			ends = verifier.matchFromDFAState(regex.program.verifierDFA, data, anchorStart, endLimit, regex.prefixDFAStates[prefixLength])
		} else {
			ends = verifier.matchFromLimit(regex.program, data, start, regexMatchLimit(regex.maxLength, len(data)-start))
		}
		for _, end := range ends {
			event := scanEvent{match: Match{Id: expression.id, From: uint64(start), To: uint64(end)}, expressionIndex: regex.expressionIndex}
			if end == offset+1 {
				ctx.readyEvents = append(ctx.readyEvents, event)
				continue
			}
			ctx.pushPendingEvent(event)
		}
		if regex.leftmostOnly && len(verifier.ends) != 0 {
			break
		}
	}
}

// scanBlockSingleByteAnchorsWord 针对两个常见 PII 触发字节，每次处理八个字节。unsafe 读取
// 仅限完整的边界内机器字；生成事件前会重新检查每个候选。
func (scanner *Scanner) scanBlockSingleByteAnchorsWord(data []byte, ctx *context, matches *[]Match) error {
	first, second := scanner.singleByteValues[0], scanner.singleByteValues[1]
	simple := scanner.singleByteSimple
	offset := 0
	for ; offset+8 <= len(data); offset += 8 {
		mask := singleByteTriggerMask(data, offset, first, second)
		wordEnd := offset + 8
		if mask == 0 && (ctx.pendingCount == 0 || ctx.pendingFirstEnd > uint64(wordEnd)) {
			continue
		}

		// 扫描结果仅能在触发字节或先前触发器已入队的结束偏移处变得可观察。机器字掩码
		// 可直接跳转这些边界，同时保持精确顺序（包括相同结束处的待处理和新事件）。
		for mask != 0 || ctx.pendingCount != 0 && ctx.pendingFirstEnd <= uint64(wordEnd) {
			triggerEnd := wordEnd + 1
			if mask != 0 {
				triggerEnd = offset + (bits.TrailingZeros64(mask) >> 3) + 1
			}
			pendingEnd := wordEnd + 1
			if ctx.pendingCount != 0 && ctx.pendingFirstEnd <= uint64(wordEnd) {
				pendingEnd = int(ctx.pendingFirstEnd)
			}
			end := triggerEnd
			if pendingEnd < end {
				end = pendingEnd
			}

			if triggerEnd == end {
				position := end - 1
				value := data[position]
				// 位技巧在发生借位后可能保守地标记相邻字节通道，因此保留该字节级确认。
				if value == first || value == second {
					if simple {
						scanner.appendSingleByteRegex(data, ctx, position, scanner.singleByteRegex[value])
					} else {
						scanner.appendSingleByteTriggers(data, ctx, position, scanner.singleByteTriggers[value])
					}
				}
				mask &= mask - 1
			}
			if len(ctx.readyEvents) != 0 || ctx.pendingCount != 0 && ctx.pendingFirstEnd <= uint64(end) {
				collectScanEvents(scanner, ctx, end, matches)
			}
		}
	}
	for ; offset < len(data); offset++ {
		value := data[offset]
		if value == first || value == second {
			if simple {
				scanner.appendSingleByteRegex(data, ctx, offset, scanner.singleByteRegex[value])
			} else {
				scanner.appendSingleByteTriggers(data, ctx, offset, scanner.singleByteTriggers[value])
			}
		}
		end := uint64(offset + 1)
		if len(ctx.readyEvents) != 0 || ctx.pendingCount != 0 && ctx.pendingFirstEnd <= end {
			collectScanEvents(scanner, ctx, offset+1, matches)
		}
	}
	return nil
}

// scanBlockSingleByteAnchors 在每个触发器均为单字节时跳过 AC。这常见于锚点分别为
// '1' 和 '@' 的定宽手机号与有界邮箱规则。
func (scanner *Scanner) scanBlockSingleByteAnchors(data []byte, ctx *context, matches *[]Match) error {
	for offset, value := range data {
		for _, trigger := range scanner.singleByteTriggers[value] {
			if trigger.kind == scanLiteral {
				if !scanner.eventNeeded[trigger.expressionIndex] {
					continue
				}
				expression := scanner.expressions[trigger.expressionIndex]
				ctx.readyEvents = append(ctx.readyEvents, scanEvent{
					match:           Match{Id: expression.id, From: uint64(offset), To: uint64(offset + 1)},
					expressionIndex: trigger.expressionIndex,
				})
				continue
			}

			groupIndex := int(trigger.regexGroupIndex)
			if !scanner.anchoredNeeded[groupIndex] {
				continue
			}
			group := scanner.anchoredGroups[groupIndex]
			representative := group[0]
			regex := scanner.regexPrograms[representative]
			verifier := ctx.regexVerifiers[representative]
			anchorStart := offset
			if anchorStart < int(regex.anchorMinOffset) {
				continue
			}
			if !matchesAnchoredSuffix(data, anchorStart, regex.anchorLength, regex.suffixClass, regex.hasSuffixClass) {
				continue
			}
			startMin := anchorStart - int(regex.anchorMaxOffset)
			if startMin < 0 {
				startMin = 0
			}
			startMax := anchorStart - int(regex.anchorMinOffset)
			if regex.hasPrefixClass {
				startMin = anchorStart
				for startMin > 0 && anchorStart-startMin < int(regex.anchorMaxOffset) && regex.prefixClass.contains(data[startMin-1]) {
					startMin--
				}
				startMax = startMin
			}
			for start := startMin; start <= startMax; start++ {
				for _, end := range verifier.matchFromLimit(regex.program, data, start, regexMatchLimit(regex.maxLength, len(data)-start)) {
					for _, regexIndex := range group {
						expressionIndex := scanner.regexPrograms[regexIndex].expressionIndex
						if !scanner.eventNeeded[expressionIndex] {
							continue
						}
						expression := scanner.expressions[expressionIndex]
						event := scanEvent{match: Match{Id: expression.id, From: uint64(start), To: uint64(end)}, expressionIndex: expressionIndex}
						if end == offset+1 {
							ctx.readyEvents = append(ctx.readyEvents, event)
							continue
						}
						ctx.pushPendingEvent(event)
					}
				}
				if regex.leftmostOnly && len(verifier.ends) != 0 {
					break
				}
			}
		}
		end := uint64(offset + 1)
		if len(ctx.readyEvents) != 0 || ctx.pendingCount != 0 && ctx.pendingFirstEnd <= end {
			collectScanEvents(scanner, ctx, offset+1, matches)
		}
	}
	return nil
}

// scanBlockAnchoredOnly 是常见 PII 数据库的紧凑热路径：仅含字面量与字面量触发正则，
// 不含无锚点调度器、零宽或近似扩展。将这些缺失特性排除在字节循环外，可避免每个输入字节
// 的两次不可预测检查。
func (scanner *Scanner) scanBlockAnchoredOnly(data []byte, ctx *context, matches *[]Match) error {
	state := uint32(0)
	transitions := scanner.automaton.transitions
	for offset := 0; offset < len(data); offset++ {
		b := data[offset]
		state = transitions[state<<8|uint32(b)]
		outputEnd := scanner.automaton.outputEnd[state]
		if outputEnd != 0 {
			outputStart := scanner.automaton.outputStart[state]
			for outputIndex := outputStart; outputIndex < outputEnd; outputIndex++ {
				scanner.appendBlockTriggerEvents(data, offset, scanner.automaton.outputs[outputIndex], ctx)
			}
		}
		end := uint64(offset + 1)
		if len(ctx.readyEvents) != 0 || ctx.pendingCount != 0 && ctx.pendingFirstEnd <= end {
			collectScanEvents(scanner, ctx, offset+1, matches)
		}
	}
	return nil
}

// appendEmptyRegexEvents 在一个输入边界记录可空表达式。可空程序会编译进无锚点调度器，
// 由其报告非空分支；零宽分支单独产生事件，使其在空输入和每个字节边界均可见。
func (scanner *Scanner) appendEmptyRegexEvents(events *[]scanEvent, offset uint64, data []byte) {
	for _, regexIndex := range scanner.emptyRegex {
		regex := scanner.regexPrograms[regexIndex]
		if !scanner.eventNeeded[regex.expressionIndex] {
			continue
		}
		if programContainsAnchor(regex.program) && !nfaMatchesEmptyAt(regex.program, data, int(offset)) {
			continue
		}
		expression := scanner.expressions[regex.expressionIndex]
		*events = append(*events, scanEvent{
			match:           Match{Id: expression.id, From: offset, To: offset},
			expressionIndex: regex.expressionIndex,
		})
	}
}

func (scanner *Scanner) appendBlockHammingEvents(events *[]scanEvent, ctx *context, data []byte, end int) {
	for _, literal := range scanner.hammingLiterals {
		if !scanner.eventNeeded[literal.expressionIndex] {
			continue
		}
		length := len(literal.pattern)
		if end < length || !hammingMatches(data[end-length:end], literal.pattern, literal.distance) {
			continue
		}
		expression := scanner.expressions[literal.expressionIndex]
		*events = append(*events, scanEvent{
			match:           Match{Id: expression.id, From: uint64(end - length), To: uint64(end)},
			expressionIndex: literal.expressionIndex,
		})
	}
	for _, regex := range scanner.hammingRegexes {
		if !scanner.eventNeeded[regex.expressionIndex] {
			continue
		}
		for _, sequence := range regex.sequences {
			length := len(sequence.classes)
			if end < length || !hammingClassesMatch(data[end-length:end], sequence.classes, regex.distance) {
				continue
			}
			expression := scanner.expressions[regex.expressionIndex]
			*events = append(*events, scanEvent{match: Match{Id: expression.id, From: uint64(end - length), To: uint64(end)}, expressionIndex: regex.expressionIndex})
		}
	}
	for index, regex := range scanner.hammingNFAs {
		if !scanner.eventNeeded[regex.expressionIndex] || end < regex.width || !ctx.hammingNFA[index].matches(regex.program, data[end-regex.width:end]) {
			continue
		}
		expression := scanner.expressions[regex.expressionIndex]
		*events = append(*events, scanEvent{match: Match{Id: expression.id, From: uint64(end - regex.width), To: uint64(end)}, expressionIndex: regex.expressionIndex})
	}
}

func hammingClassesMatch(data []byte, classes []byteClass, maximum uint32) bool {
	var distance uint32
	for index, class := range classes {
		if class.contains(data[index]) {
			continue
		}
		distance++
		if distance > maximum {
			return false
		}
	}
	return true
}

func (scanner *Scanner) appendBlockEditEvents(events *[]scanEvent, ctx *context, data []byte, end int) {
	for _, literal := range scanner.editLiterals {
		if !scanner.eventNeeded[literal.expressionIndex] {
			continue
		}
		minimumLength := len(literal.pattern) - int(literal.distance)
		if minimumLength < 1 {
			minimumLength = 1
		}
		maximumLength := len(literal.pattern) + int(literal.distance)
		if maximumLength > end {
			maximumLength = end
		}
		for length := minimumLength; length <= maximumLength; length++ {
			start := end - length
			if !editDistanceAtMost(ctx.editPrevious, ctx.editCurrent, data[start:end], literal.pattern, literal.distance) {
				continue
			}
			expression := scanner.expressions[literal.expressionIndex]
			*events = append(*events, scanEvent{
				match:           Match{Id: expression.id, From: uint64(start), To: uint64(end)},
				expressionIndex: literal.expressionIndex,
			})
		}
	}
	for _, regex := range scanner.editRegexes {
		if !scanner.eventNeeded[regex.expressionIndex] {
			continue
		}
		for _, sequence := range regex.sequences {
			minimumLength := len(sequence.classes) - int(regex.distance)
			if minimumLength < 1 {
				minimumLength = 1
			}
			maximumLength := len(sequence.classes) + int(regex.distance)
			if maximumLength > end {
				maximumLength = end
			}
			for length := minimumLength; length <= maximumLength; length++ {
				start := end - length
				if !editClassesAtMost(ctx.editPrevious, ctx.editCurrent, data[start:end], sequence.classes, regex.distance) {
					continue
				}
				expression := scanner.expressions[regex.expressionIndex]
				*events = append(*events, scanEvent{match: Match{Id: expression.id, From: uint64(start), To: uint64(end)}, expressionIndex: regex.expressionIndex})
			}
		}
	}
	for _, repeat := range scanner.editClassRepeats {
		if !scanner.eventNeeded[repeat.expressionIndex] {
			continue
		}
		minimumLength := repeat.minimum - int(repeat.distance)
		if minimumLength < 1 {
			minimumLength = 1
		}
		maximumLength := repeat.maximum + int(repeat.distance)
		if maximumLength > end {
			maximumLength = end
		}
		for length := minimumLength; length <= maximumLength; length++ {
			start := end - length
			if !editClassRepeatAtMost(data[start:end], repeat.class, repeat.minimum, repeat.maximum, repeat.distance) {
				continue
			}
			expression := scanner.expressions[repeat.expressionIndex]
			*events = append(*events, scanEvent{match: Match{Id: expression.id, From: uint64(start), To: uint64(end)}, expressionIndex: repeat.expressionIndex})
		}
	}
	for index, regex := range scanner.editNFAs {
		if !scanner.eventNeeded[regex.expressionIndex] {
			continue
		}
		minimumLength := regex.minimumWidth - int(regex.distance)
		if minimumLength < 1 {
			minimumLength = 1
		}
		maximumLength := regex.maximumWidth + int(regex.distance)
		if maximumLength > end {
			maximumLength = end
		}
		for length := minimumLength; length <= maximumLength; length++ {
			start := end - length
			if !ctx.editNFA[index].matches(regex.program, data[start:end]) {
				continue
			}
			expression := scanner.expressions[regex.expressionIndex]
			*events = append(*events, scanEvent{match: Match{Id: expression.id, From: uint64(start), To: uint64(end)}, expressionIndex: regex.expressionIndex})
		}
	}
}

func editClassRepeatAtMost(data []byte, class byteClass, minimum, maximum int, distance uint32) bool {
	mismatches := 0
	for _, value := range data {
		if !class.contains(value) {
			mismatches++
		}
	}
	cost := mismatches
	switch {
	case len(data) < minimum:
		cost += minimum - len(data)
	case len(data) > maximum:
		if deleted := len(data) - maximum; deleted > cost {
			cost = deleted
		}
	}
	return uint64(cost) <= uint64(distance)
}

func editClassesAtMost(previous, current []uint16, data []byte, classes []byteClass, maximum uint32) bool {
	if len(previous) < len(classes)+1 || len(current) < len(classes)+1 {
		return false
	}
	for index := range classes {
		previous[index+1] = uint16(index + 1)
	}
	previous[0] = 0
	for row, value := range data {
		current[0] = uint16(row + 1)
		for column, class := range classes {
			substitution := previous[column]
			if !class.contains(value) {
				substitution++
			}
			deletion := previous[column+1] + 1
			insertion := current[column] + 1
			if deletion < substitution {
				substitution = deletion
			}
			if insertion < substitution {
				substitution = insertion
			}
			current[column+1] = substitution
		}
		previous, current = current, previous
	}
	return uint32(previous[len(classes)]) <= maximum
}

func editDistanceAtMost(previous, current []uint16, data []byte, pattern string, maximum uint32) bool {
	for index := 0; index <= len(pattern); index++ {
		previous[index] = uint16(index)
	}
	for dataIndex, value := range data {
		current[0] = uint16(dataIndex + 1)
		for patternIndex := 1; patternIndex <= len(pattern); patternIndex++ {
			substitution := previous[patternIndex-1]
			if value != pattern[patternIndex-1] {
				substitution++
			}
			insertion := previous[patternIndex] + 1
			deletion := current[patternIndex-1] + 1
			current[patternIndex] = min(substitution, min(insertion, deletion))
		}
		previous, current = current, previous
	}
	return previous[len(pattern)] <= uint16(maximum)
}

func hammingMatches(data []byte, pattern string, maximum uint32) bool {
	var distance uint32
	for index := range pattern {
		if data[index] != pattern[index] {
			distance++
			if distance > maximum {
				return false
			}
		}
	}
	return true
}

func collectScanEvents(scanner *Scanner, ctx *context, end int, matches *[]Match) {
	prepareScanEvents(ctx, end)
	if scanner.directSingleEvent && len(ctx.readyEvents) == 1 {
		event := ctx.readyEvents[0]
		*matches = append(*matches, event.match)
		ctx.readyEvents = ctx.readyEvents[:0]
		return
	}
	sortScanEvents(ctx.readyEvents)
	scanner.keepOneEventPerExpressionEnd(&ctx.readyEvents)
	if len(scanner.combinations) != 0 {
		scanner.appendCombinationEvents(&ctx.readyEvents, ctx.combinationSeen, ctx.combinationOn, uint64(end))
		sortScanEvents(ctx.readyEvents)
	}
	for _, event := range ctx.readyEvents {
		expression := scanner.expressions[event.expressionIndex]
		if expression.flags&CompileSingleMatch != 0 && ctx.singleMatched[event.expressionIndex] {
			continue
		}
		if expression.flags&CompileSingleMatch != 0 {
			ctx.singleMatched[event.expressionIndex] = true
		}
		if expression.flags&CompileQuiet != 0 {
			continue
		}
		*matches = append(*matches, event.match)
	}
	ctx.readyEvents = ctx.readyEvents[:0]
}

// keepOneEventPerExpressionEnd 是所有执行器共用的公开事件边界。常规扫描中到达同一次
// collect 调用的事件具有相同终点，但为兼容后续执行器，To 仍参与分组。压缩过程无分配，
// 并选择最小有效起点，作为本 API 对按结束偏移识别事件的确定性表示。
func (scanner *Scanner) keepOneEventPerExpressionEnd(events *[]scanEvent) {
	values := *events
	kept := values[:0]
	for start := 0; start < len(values); {
		end := start + 1
		for end < len(values) && values[end].expressionIndex == values[start].expressionIndex && values[end].match.To == values[start].match.To {
			end++
		}
		selected := -1
		for index := start; index < end; index++ {
			event := values[index]
			expression := scanner.expressions[event.expressionIndex]
			if !expression.constraint.accepts(event.match) {
				continue
			}
			if selected == -1 || event.match.From < values[selected].match.From {
				selected = index
			}
		}
		if selected != -1 {
			kept = append(kept, values[selected])
		}
		start = end
	}
	*events = kept
}

func sortScanEvents(events []scanEvent) {
	if len(events) < 2 {
		return
	}
	ordered := true
	for index := 1; index < len(events); index++ {
		if events[index-1].expressionIndex > events[index].expressionIndex {
			ordered = false
			break
		}
	}
	if ordered {
		return
	}
	// 仅字面量批次通常已按表达式索引排序，插入排序在其较小的常见规模下更快。锚定验证器
	// 和宽泛多规则命中会产生大批次，此时插入排序的二次成本会成为可测量的扫描开销。
	if len(events) > 8 {
		slices.SortStableFunc(events, func(left, right scanEvent) int {
			switch {
			case left.expressionIndex < right.expressionIndex:
				return -1
			case left.expressionIndex > right.expressionIndex:
				return 1
			default:
				return 0
			}
		})
		return
	}
	for index := 1; index < len(events); index++ {
		current := events[index]
		position := index - 1
		for position >= 0 && events[position].expressionIndex > current.expressionIndex {
			events[position+1] = events[position]
			position--
		}
		events[position+1] = current
	}
}

func containsRegexMeta(pattern string) bool {
	for i := 0; i < len(pattern); i++ {
		switch pattern[i] {
		case '\\', '.', '*', '+', '?', '(', ')', '[', ']', '{', '}', '|', '^', '$':
			return true
		}
	}
	return false
}

func isASCIIPattern(pattern string) bool {
	for index := range pattern {
		if pattern[index] >= utf8.RuneSelf {
			return false
		}
	}
	return true
}
