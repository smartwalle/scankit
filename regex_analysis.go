package scankit

import (
	"math/bits"
	"sort"
)

// maxRegexFiniteWidth 是当前 Thompson 编译器在必然超出状态预算前可表示的最大有限宽度。
// 它也限制编译期宽度运算，避免嵌套有限重复在 NFA 资源保护拒绝前使 int 溢出。
const maxRegexFiniteWidth = maxNFAStates - 1

type regexAnchor struct {
	literal   string
	minOffset int
	maxOffset int
}

type regexAnalysis struct {
	min     int
	max     int
	anchors []regexAnchor
}

func analyzeRegex(root *regexNode) (regexAnalysis, error) {
	return analyzeRegexNode(root)
}

func analyzeRegexNode(node *regexNode) (regexAnalysis, error) {
	switch node.kind {
	case regexEmpty, regexAnchorStart, regexAnchorEnd, regexAbsoluteStart, regexAbsoluteEnd, regexEndBeforeFinalNewline, regexWordBoundary, regexNotWordBoundary:
		return regexAnalysis{max: 0}, nil
	case regexLineBreak:
		return regexAnalysis{min: 1, max: 2}, nil
	case regexLiteral:
		// regexAnchor 的 literal 是字节序列而非 Unicode 字符串。直接转换 byte 会把
		// 0x80–0xff 编码为多字节 UTF-8，导致 AC 触发器与 NFA 字节语言不一致。
		return regexAnalysis{min: 1, max: 1, anchors: []regexAnchor{{literal: string([]byte{node.literal})}}}, nil
	case regexClass, regexDot:
		return regexAnalysis{min: 1, max: 1}, nil
	case regexConcat:
		return analyzeRegexConcat(node.children)
	case regexAlternate:
		return analyzeRegexAlternate(node.children)
	case regexRepeat:
		if len(node.children) != 1 {
			return regexAnalysis{}, ErrUnsupportedExpression
		}
		return analyzeRegexRepeat(node, node.children[0])
	default:
		return regexAnalysis{}, ErrUnsupportedExpression
	}
}

func analyzeRegexConcat(children []*regexNode) (regexAnalysis, error) {
	result := regexAnalysis{max: 0}
	for _, child := range children {
		analysis, err := analyzeRegexNode(child)
		if err != nil {
			return regexAnalysis{}, err
		}
		for _, anchor := range analysis.anchors {
			anchor.minOffset, err = addRegexFiniteWidth(anchor.minOffset, result.min)
			if err != nil {
				return regexAnalysis{}, err
			}
			anchor.maxOffset, err = addRegexMaximum(anchor.maxOffset, result.max)
			if err != nil {
				return regexAnalysis{}, err
			}
			result.anchors = append(result.anchors, anchor)
		}
		result.min, err = addRegexFiniteWidth(result.min, analysis.min)
		if err != nil {
			return regexAnalysis{}, err
		}
		result.max, err = addRegexMaximum(result.max, analysis.max)
		if err != nil {
			return regexAnalysis{}, err
		}
	}
	result.anchors = normalizeRegexAnchors(result.anchors)
	return result, nil
}

func analyzeRegexAlternate(children []*regexNode) (regexAnalysis, error) {
	if len(children) == 0 {
		return regexAnalysis{max: 0}, nil
	}
	result, err := analyzeRegexNode(children[0])
	if err != nil {
		return regexAnalysis{}, err
	}
	for _, child := range children[1:] {
		analysis, err := analyzeRegexNode(child)
		if err != nil {
			return regexAnalysis{}, err
		}
		if analysis.min < result.min {
			result.min = analysis.min
		}
		if result.max == unboundedRepeat || analysis.max == unboundedRepeat {
			result.max = unboundedRepeat
		} else if analysis.max > result.max {
			result.max = analysis.max
		}
		result.anchors = intersectRegexAnchors(result.anchors, analysis.anchors)
	}
	return result, nil
}

func analyzeRegexRepeat(node, child *regexNode) (regexAnalysis, error) {
	analysis, err := analyzeRegexNode(child)
	if err != nil {
		return regexAnalysis{}, err
	}
	result := regexAnalysis{}
	result.min, err = multiplyRegexWidth(analysis.min, node.min)
	if err != nil {
		return regexAnalysis{}, err
	}
	result.max, err = multiplyRegexWidth(analysis.max, node.max)
	if err != nil {
		return regexAnalysis{}, err
	}
	if node.min != node.max || analysis.min != analysis.max {
		return result, nil
	}
	for copyIndex := 0; copyIndex < node.min; copyIndex++ {
		for _, anchor := range analysis.anchors {
			offset, err := multiplyRegexWidth(analysis.min, copyIndex)
			if err != nil {
				return regexAnalysis{}, err
			}
			anchor.minOffset, err = addRegexFiniteWidth(anchor.minOffset, offset)
			if err != nil {
				return regexAnalysis{}, err
			}
			anchor.maxOffset, err = addRegexMaximum(anchor.maxOffset, offset)
			if err != nil {
				return regexAnalysis{}, err
			}
			result.anchors = append(result.anchors, anchor)
		}
	}
	result.anchors = normalizeRegexAnchors(result.anchors)
	return result, nil
}

func selectRegexAnchor(analysis regexAnalysis) (regexAnchor, bool) {
	if len(analysis.anchors) == 0 {
		return regexAnchor{}, false
	}
	best := analysis.anchors[0]
	for _, anchor := range analysis.anchors[1:] {
		if len(anchor.literal) > len(best.literal) || (len(anchor.literal) == len(best.literal) && anchor.minOffset < best.minOffset) {
			best = anchor
		}
	}
	return best, true
}

// leadingBoundedPrefixClass 识别常见 PII 形态 "class{min,max}<literal-anchor>..."。
// 它刻意保守：仅当选定锚点紧随前导有界字符类时使用快路径，因此回溯至一个候选起点仍能
// 保持浮动锚点的最左匹配策略。
func leadingBoundedPrefixClass(root *regexNode, anchor regexAnchor) (byteClass, bool) {
	if root == nil || root.kind != regexConcat || len(root.children) < 2 {
		return byteClass{}, false
	}
	prefix := root.children[0]
	if prefix.kind != regexRepeat || len(prefix.children) != 1 || prefix.children[0].kind != regexClass || prefix.min <= 0 || prefix.max == unboundedRepeat {
		return byteClass{}, false
	}
	if anchor.minOffset != prefix.min || anchor.maxOffset != prefix.max {
		return byteClass{}, false
	}
	return prefix.children[0].class, true
}

// leadingAnchorSuffixClass 识别紧跟必需字节类或其正重复的固定字面量前缀。AC 找到前缀
// 字面量后，非成员字节不可能开始匹配，因此可作为不改变起始位置选择的安全二级预过滤。
func leadingAnchorSuffixClass(root *regexNode, anchor regexAnchor) (byteClass, bool) {
	if root == nil || root.kind != regexConcat || anchor.minOffset != 0 || anchor.maxOffset != 0 || len(anchor.literal) == 0 {
		return byteClass{}, false
	}
	matched := 0
	for _, child := range root.children {
		if matched < len(anchor.literal) {
			if child.kind != regexLiteral || child.literal != anchor.literal[matched] {
				return byteClass{}, false
			}
			matched++
			continue
		}
		if child.kind == regexClass {
			return child.class, true
		}
		if child.kind == regexRepeat && child.min > 0 && len(child.children) == 1 {
			class, ok := extractSingleByteClass(child.children[0])
			if ok && isSelectiveAnchorSuffixClass(class) {
				return class, true
			}
		}
		return byteClass{}, false
	}
	return byteClass{}, false
}

// isSelectiveAnchorSuffixClass 仅保留足以抵消一次额外字节检查的小类。宽类（如 \d）在
// 锚点命中后通常不能排除足够候选，交给 verifier 反而更快。
func isSelectiveAnchorSuffixClass(class byteClass) bool {
	const maxSelectiveAnchorSuffixBytes = 8
	return bits.OnesCount64(class[0])+bits.OnesCount64(class[1])+bits.OnesCount64(class[2])+bits.OnesCount64(class[3]) <= maxSelectiveAnchorSuffixBytes
}

func normalizeRegexAnchors(anchors []regexAnchor) []regexAnchor {
	if len(anchors) < 2 {
		return anchors
	}
	sort.Slice(anchors, func(i, j int) bool {
		if anchors[i].minOffset != anchors[j].minOffset {
			return anchors[i].minOffset < anchors[j].minOffset
		}
		if anchors[i].maxOffset != anchors[j].maxOffset {
			return anchors[i].maxOffset < anchors[j].maxOffset
		}
		return anchors[i].literal < anchors[j].literal
	})
	merged := make([]regexAnchor, 0, len(anchors))
	for _, anchor := range anchors {
		lastIndex := len(merged) - 1
		if lastIndex >= 0 && merged[lastIndex].minOffset+len(merged[lastIndex].literal) == anchor.minOffset && merged[lastIndex].maxOffset+len(merged[lastIndex].literal) == anchor.maxOffset {
			merged[lastIndex].literal += anchor.literal
			continue
		}
		merged = append(merged, anchor)
	}
	return merged
}

func intersectRegexAnchors(left, right []regexAnchor) []regexAnchor {
	if len(left) == 0 || len(right) == 0 {
		return nil
	}
	result := make([]regexAnchor, 0, min(len(left), len(right)))
	for _, candidate := range left {
		for _, other := range right {
			if candidate == other {
				result = append(result, candidate)
				break
			}
		}
	}
	return normalizeRegexAnchors(result)
}

func addRegexFiniteWidth(left, right int) (int, error) {
	if left < 0 || right < 0 || left > maxRegexFiniteWidth-right {
		return 0, ErrRegexTooComplex
	}
	return left + right, nil
}

func addRegexMaximum(left, right int) (int, error) {
	if left == unboundedRepeat || right == unboundedRepeat {
		return unboundedRepeat, nil
	}
	return addRegexFiniteWidth(left, right)
}

func multiplyRegexWidth(width, count int) (int, error) {
	if width == unboundedRepeat || count == unboundedRepeat {
		return unboundedRepeat, nil
	}
	if width < 0 || count < 0 || width != 0 && count > maxRegexFiniteWidth/width {
		return 0, ErrRegexTooComplex
	}
	return width * count, nil
}
