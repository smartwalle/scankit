package scankit

// byteRegexCompilePlan 是块执行器与规则集规划器共享的编译器 IR。Scanner 冻结前会推导
// 所有字段，扫描路径无需重新判断正则形态、锚点资格或有界执行限制。
type byteRegexCompilePlan struct {
	analysis            regexAnalysis
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
	fixed               *fixedByteRegex
	fixedAnchor         *fixedByteRegexAnchor
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
	anchor, hasBoundedAnchor := selectRegexAnchor(analysis)
	internalAnchor, internalPrefixClass, internalLeading, hasInternalAnchor := extractInternalLiteralAnchor(root)
	hasInternalAnchor = hasInternalAnchor && flags&CompileCaseless == 0
	prefixClass, hasPrefixClass := leadingBoundedPrefixClass(root, anchor)
	if !hasPrefixClass {
		for _, candidate := range analysis.anchors {
			if candidateClass, ok := leadingBoundedPrefixClass(root, candidate); ok {
				anchor = candidate
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
	if flags&CompileLeftmostStart == 0 {
		if fixed, ok := extractFixedByteRegex(root); ok {
			plan.fixed = fixed
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
	if len(literal) == 0 || prefixClass.contains(literal[0]) {
		return regexAnchor{}, byteClass{}, "", false
	}
	return regexAnchor{literal: string(literal), minOffset: prefix.min, maxOffset: unboundedRepeat}, prefixClass, string(leading), true
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
