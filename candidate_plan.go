package scankit

// byteRegexCandidatePlan 汇总一个字节正则进入 verifier 前可证明安全的候选来源与必要
// 条件。它只在 Compile 时构建；扫描器继续读取已下沉到 compiledRegexProgram 和
// blockScanPlan 的字段，避免在每个候选上解释该计划。
type byteRegexCandidatePlan struct {
	anchor         regexAnchor
	hasAnchor      bool
	bounded        bool
	anchorSpan     int
	anchorScore    uint32
	requiredChecks uint8
	fallback       byteRegexCandidateFallback
}

type byteRegexCandidateFallback uint8

const (
	byteRegexCandidateSelected byteRegexCandidateFallback = iota
	byteRegexCandidateNoLiteral
	byteRegexCandidateUnboundedOffset
)

// buildByteRegexCandidatePlan 从所有必需字面量中选择候选来源。选择规则先偏向有限位置
// 窗口，再偏向更长字面量和更窄窗口；它不是语言判断，任一候选最终仍由完整 verifier
// 确认。这样可避免“最长字面量位于无界前缀之后”时，错过更短但可安全调度的有限 anchor。
func buildByteRegexCandidatePlan(analysis regexAnalysis) byteRegexCandidatePlan {
	if len(analysis.anchors) == 0 {
		return byteRegexCandidatePlan{fallback: byteRegexCandidateNoLiteral}
	}
	best := regexAnchor{}
	bestScore := uint32(0)
	foundBounded := false
	for _, anchor := range analysis.anchors {
		if anchor.maxOffset == unboundedRepeat || anchor.maxOffset < anchor.minOffset {
			continue
		}
		span := anchor.maxOffset - anchor.minOffset
		score := byteRegexCandidateScore(anchor, span)
		if !foundBounded || score > bestScore || score == bestScore && candidateAnchorEarlier(anchor, best) {
			best, bestScore, foundBounded = anchor, score, true
		}
	}
	if foundBounded {
		return byteRegexCandidatePlan{
			anchor:      best,
			hasAnchor:   true,
			bounded:     true,
			anchorSpan:  best.maxOffset - best.minOffset,
			anchorScore: bestScore,
		}
	}
	anchor, ok := selectRegexAnchor(analysis)
	if !ok {
		return byteRegexCandidatePlan{fallback: byteRegexCandidateNoLiteral}
	}
	return byteRegexCandidatePlan{
		anchor:      anchor,
		hasAnchor:   true,
		anchorSpan:  unboundedRepeat,
		anchorScore: uint32(len(anchor.literal)),
		fallback:    byteRegexCandidateUnboundedOffset,
	}
}

func byteRegexCandidateScore(anchor regexAnchor, span int) uint32 {
	// 长字面量通常比额外几个位置检查更具选择性；位置窗口只作为同长度候选的稳定
	// 次级排序。所有参与该函数的 anchor 都已证明为有限窗口。
	const literalWeight = 1 << 12
	const spanLimit = literalWeight - 1
	length := len(anchor.literal)
	if length > 0xfff {
		length = 0xfff
	}
	if span > spanLimit {
		span = spanLimit
	}
	return uint32(length*literalWeight + spanLimit - span)
}

func candidateAnchorEarlier(left, right regexAnchor) bool {
	if left.minOffset != right.minOffset {
		return left.minOffset < right.minOffset
	}
	if left.maxOffset != right.maxOffset {
		return left.maxOffset < right.maxOffset
	}
	return left.literal < right.literal
}

func (plan *byteRegexCandidatePlan) setRequiredChecks(count uint8) {
	plan.requiredChecks = count
}
