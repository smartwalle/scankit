package scankit

// byteRegexCompilePlan 是块执行器与规则集规划器共享的编译器 IR。Scanner 冻结前会推导
// 所有字段，扫描路径无需重新判断正则形态、锚点资格或有界执行限制。
type byteRegexCompilePlan struct {
	analysis            regexAnalysis
	candidate           byteRegexCandidatePlan
	program             nfaProgram
	anchor              regexAnchor
	hasBoundedAnchor    bool
	internalAnchor      regexAnchor
	hasInternalAnchor   bool
	internalPrefixClass byteClass
	internalLeading     string
	prefixClass         byteClass
	hasPrefixClass      bool
	suffixClass         byteClass
	hasSuffixClass      bool
	suffixChecks        *anchoredSuffixChecks
	simpleRepeat        byteRegexRepeat
	hasSimpleRepeat     bool
	prefixRepeat        byteRegexPrefixRepeat
	hasPrefixRepeat     bool
	boundedRepeat       *byteRegexBoundedRepeat
	alternation         *byteRegexAlternation
	fixed               *fixedByteRegex
	fixedAnchor         *fixedByteRegexAnchor
	fixedLiteralAnchors []string
	fixedLiteralOffset  int
	leftmostOnly        bool
	prefixDFAStates     []uint16
}

func compileByteRegexPlan(root *regexNode, flags CompileFlag) (byteRegexCompilePlan, error) {
	analysis, err := analyzeRegex(root)
	if err != nil {
		return byteRegexCompilePlan{}, err
	}
	program, err := compileNFA(root)
	if err != nil {
		return byteRegexCompilePlan{}, err
	}
	program.multiline = flags&CompileMultiline != 0
	candidatePlan := buildByteRegexCandidatePlan(analysis)
	anchor, hasBoundedAnchor := candidatePlan.anchor, candidatePlan.bounded
	internalAnchor, internalPrefixClass, internalLeading, hasInternalAnchor := extractInternalLiteralAnchor(root)
	hasInternalAnchor = hasInternalAnchor && flags&CompileCaseless == 0
	// 前置单词边界会使浮动字面量锚点的候选起点与触发位置不再能由现有 verifier 安全关联。
	// 保持通用 NFA 调度器可避免某个较早候选吞掉后续触发器，从而漏报独立匹配。
	if hasLeadingRegexWordBoundary(root) && anchor.minOffset != anchor.maxOffset {
		hasBoundedAnchor = false
	}
	prefixClass, hasPrefixClass := leadingBoundedPrefixClass(root, anchor)
	if !hasPrefixClass {
		for _, candidateAnchor := range analysis.anchors {
			if candidateClass, ok := leadingBoundedPrefixClass(root, candidateAnchor); ok {
				anchor = candidateAnchor
				candidatePlan = buildByteRegexCandidatePlan(regexAnalysis{anchors: []regexAnchor{candidateAnchor}})
				hasBoundedAnchor = candidatePlan.bounded
				prefixClass = candidateClass
				hasPrefixClass = true
				break
			}
		}
	}
	suffixChecks := leadingAnchorSuffixChecks(root, anchor)
	hasBoundedAnchor = hasBoundedAnchor && anchor.maxOffset != unboundedRepeat && flags&CompileCaseless == 0
	plan := byteRegexCompilePlan{
		analysis:            analysis,
		candidate:           candidatePlan,
		program:             program,
		anchor:              anchor,
		hasBoundedAnchor:    hasBoundedAnchor,
		internalAnchor:      internalAnchor,
		hasInternalAnchor:   hasInternalAnchor,
		internalPrefixClass: internalPrefixClass,
		internalLeading:     internalLeading,
		prefixClass:         prefixClass,
		hasPrefixClass:      hasPrefixClass,
		suffixClass:         suffixChecks.first,
		hasSuffixClass:      suffixChecks.count != 0,
		suffixChecks:        suffixChecks.extra(),
		leftmostOnly:        anchor.minOffset != anchor.maxOffset,
	}
	plan.candidate.anchor = anchor
	plan.candidate.hasAnchor = len(anchor.literal) != 0
	plan.candidate.bounded = hasBoundedAnchor
	if hasBoundedAnchor {
		plan.candidate.anchorSpan = anchor.maxOffset - anchor.minOffset
		plan.candidate.anchorScore = byteRegexCandidateScore(anchor, plan.candidate.anchorSpan)
	}
	plan.candidate.setRequiredChecks(suffixChecks.count)
	if plan.hasPrefixClass {
		plan.prefixDFAStates = buildAnchoredPrefixDFAStates(plan.program, plan.prefixClass, plan.anchor.maxOffset)
	}
	if repeat, ok := extractByteWordBoundedRegexRepeat(root); ok {
		plan.simpleRepeat = repeat
		plan.hasSimpleRepeat = true
	} else if repeat, ok := extractByteRegexRepeat(root); ok {
		plan.simpleRepeat = repeat
		plan.hasSimpleRepeat = true
	}
	if repeat, ok := extractByteRegexPrefixRepeat(root); ok && flags&CompileLeftmostStart == 0 {
		plan.prefixRepeat = repeat
		plan.hasPrefixRepeat = true
	}
	if bounded, ok := extractByteRegexBoundedRepeat(root); ok {
		plan.boundedRepeat = bounded
	}
	if alternation, ok := extractByteRegexAlternation(root); ok {
		plan.alternation = alternation
	}
	if flags&CompileLeftmostStart == 0 {
		if fixed, ok := extractFixedByteRegex(root); ok {
			plan.fixed = fixed
			if flags&CompileCaseless == 0 && plan.alternation == nil {
				anchors, offset, ok := fixedByteRegexLiteralAnchors(fixed.sequences)
				if ok {
					plan.fixedLiteralAnchors = anchors
					plan.fixedLiteralOffset = offset
				}
			}
			if anchor, ok := fixedByteRegexClassAnchor(fixed.sequences); ok {
				plan.fixed = nil
				plan.fixedAnchor = &anchor
			}
		} else if anchor, ok := extractFixedByteRegexAnchor(root); ok {
			plan.fixedAnchor = &anchor
		}
	}
	return plan, nil
}

// hasLeadingRegexWordBoundary 识别连接表达式开头的 \b 或 \B。^ 和 \A 会将起点固定，
// 因此仍可安全使用字面量锚点；单词边界允许多个候选起点，必须保持通用 NFA 调度。
func hasLeadingRegexWordBoundary(root *regexNode) bool {
	if root == nil {
		return false
	}
	if root.kind != regexConcat {
		return isRegexWordBoundary(root)
	}
	for _, child := range root.children {
		if child.kind == regexEmpty {
			continue
		}
		return isRegexWordBoundary(child)
	}
	return false
}

func isRegexWordBoundary(node *regexNode) bool {
	if node == nil {
		return false
	}
	switch node.kind {
	case regexWordBoundary, regexNotWordBoundary:
		return true
	case regexAlternate:
		for _, child := range node.children {
			if isRegexWordBoundary(child) {
				return true
			}
		}
	default:
	}
	return false
}

// extractInternalLiteralAnchor 识别“可选固定字面量前导 + 无界单字节类重复 + 必经
// 字面量”的结构。前导字面量的末字节和锚点首字节都必须不属于重复类，才能由锚点向左
// 恢复唯一的类运行边界；随后完整 NFA 仍验证整个表达式。
func extractInternalLiteralAnchor(root *regexNode) (regexAnchor, byteClass, string, bool) {
	if root == nil || root.kind != regexConcat || len(root.children) < 2 {
		return regexAnchor{}, byteClass{}, "", false
	}
	repeatIndex := 0
	leading := make([]byte, 0, len(root.children))
	for repeatIndex < len(root.children) && root.children[repeatIndex].kind == regexLiteral {
		leading = append(leading, root.children[repeatIndex].literal)
		repeatIndex++
	}
	if repeatIndex == len(root.children) || repeatIndex+1 == len(root.children) {
		return regexAnchor{}, byteClass{}, "", false
	}
	prefix := root.children[repeatIndex]
	if prefix.kind != regexRepeat || len(prefix.children) != 1 || prefix.max != unboundedRepeat {
		return regexAnchor{}, byteClass{}, "", false
	}
	prefixClass, ok := extractSingleByteClass(prefix.children[0])
	if !ok {
		return regexAnchor{}, byteClass{}, "", false
	}
	if len(leading) != 0 && prefixClass.contains(leading[len(leading)-1]) {
		return regexAnchor{}, byteClass{}, "", false
	}
	literal := make([]byte, 0, len(root.children)-repeatIndex-1)
	for _, child := range root.children[repeatIndex+1:] {
		if child.kind != regexLiteral {
			break
		}
		literal = append(literal, child.literal)
	}
	if len(literal) == 0 {
		return regexAnchor{}, byteClass{}, "", false
	}
	// 默认 . 不跨 LF。对于 .*<literal>，即使 literal 首字节也能被 . 消费，向左
	// 扫到当前行首仍恰好是同一结束位置的最左有效起点。其他类保留原限制：它们若吞掉
	// 锚点首字节，无法在不扩大候选验证范围的前提下恢复唯一前缀边界。
	if prefixClass.contains(literal[0]) && !isNonDotAllClass(prefixClass) {
		return regexAnchor{}, byteClass{}, "", false
	}
	return regexAnchor{literal: string(literal), minOffset: prefix.min, maxOffset: unboundedRepeat}, prefixClass, string(leading), true
}

// isNonDotAllClass 仅识别 applyRegexFlags 后默认 . 的字节类。DotAll 会跨行，单个触发器
// 可能验证并产生多个后续命中，不能复用 internal anchor 的一次触发一次行级验证模型。
func isNonDotAllClass(class byteClass) bool {
	dot := allBytes()
	dot.remove('\n')
	return class == dot
}

// buildAnchoredPrefixDFAStates 记录一个字节类有界运行后的 DFA 状态。仅当每个成员字节
// 在每个前缀长度都进入相同后继状态时才有效，否则验证器必须按常规方式消费前缀。固定限制
// 使该可选的编译期缓存保持有界。
func buildAnchoredPrefixDFAStates(program nfaProgram, prefix byteClass, maximum int) []uint16 {
	const maxCachedPrefixDFAStates = 128
	if program.verifierDFA == nil || maximum <= 0 || maximum > maxCachedPrefixDFAStates {
		return nil
	}
	states := make([]uint16, maximum+1)
	state := uint16(0)
	for length := 1; length <= maximum; length++ {
		next := uint16(nfaDFANoState)
		for value := range 256 {
			if !prefix.contains(byte(value)) {
				continue
			}
			candidate := program.verifierDFA.transitions[uint32(state)<<8|uint32(byte(value))]
			if candidate == nfaDFANoState {
				return nil
			}
			if next != nfaDFANoState && next != candidate {
				return nil
			}
			next = candidate
		}
		if next == nfaDFANoState {
			return nil
		}
		states[length] = next
		state = next
	}
	return states
}

// regexMatchLimit 将编译期最大长度转换为验证器本地窗口。无界语言仅受剩余输入限制，
// 不依赖可能在 32 位平台变为负数的 uint32 转换。
func regexMatchLimit(maximum, remaining int) int {
	if maximum == unboundedRepeat || maximum > remaining {
		return remaining
	}
	return maximum
}
