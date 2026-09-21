package prefilter

import (
	"math"

	"github.com/smartwalle/scankit/internal/parser"
)

// Variant 描述一个必须出现在匹配中的文字及其相对匹配起点的偏移窗口。
// 语义为：任意匹配都至少包含某个变体的文字，且该文字在匹配内的起点相对
// 匹配起点的偏移落在 [MinOffset, MaxOffset] 闭区间内。
type Variant struct {
	Value     []byte
	MinOffset int
	MaxOffset int
	// Back 非空时表示偏移窗口内命中位置左侧的每个字节都必须属于该集合，
	// 扫描时可用它把候选起点收缩到命中位置左侧的连续区间。
	Back []byte
}

// Required 是一组变体：任意匹配至少包含其中一个变体。
type Required struct {
	Variants []Variant
}

const (
	// maxVariants 限制单个计划的变体数量，避免类展开过早膨胀。
	maxVariants = 128
	// maxLiteralBytes 限制候选文字长度，过长的文字对索引没有额外收益。
	maxLiteralBytes = 16
	// maxWindow 限制候选文字相对匹配起点的偏移窗口宽度。
	maxWindow = 64
)

// FromAST 从语法树中提取必须出现的候选文字集合。返回 false 表示无法证明
// 存在可用的候选文字，调用方必须保留完整确认路径。
func FromAST(root parser.Node) (Required, bool) {
	best, ok := nodePlan(root)
	if !ok || len(best.variants) == 0 {
		return Required{}, false
	}
	out := Required{Variants: make([]Variant, 0, len(best.variants))}
	for _, v := range best.variants {
		if len(v.value) == 0 {
			return Required{}, false
		}
		if v.max >= v.min {
			if v.max-v.min > maxWindow {
				return Required{}, false
			}
		} else if v.back == nil {
			// 偏移无上界时必须能靠左侧字节集合收缩起点，否则窗口无法约束。
			return Required{}, false
		}
		out.Variants = append(out.Variants, Variant{Value: v.value, MinOffset: v.min, MaxOffset: v.max, Back: v.back})
	}
	return out, true
}

type planVariant struct {
	value []byte
	min   int
	max   int
	back  []byte
}

type plan struct {
	variants []planVariant
}

// nodePlan 返回一个即可覆盖该节点的候选计划；ok 为 false 时无法覆盖。
func nodePlan(n parser.Node) (plan, bool) {
	switch v := n.(type) {
	case parser.Literal:
		if len(v.Value) == 0 {
			return plan{}, false
		}
		return plan{variants: []planVariant{{value: v.Value, min: 0, max: 0}}}, true
	case parser.Alternation:
		out := plan{}
		for _, option := range v.Options {
			sub, ok := nodePlan(option)
			if !ok || len(out.variants)+len(sub.variants) > maxVariants {
				return plan{}, false
			}
			out.variants = append(out.variants, sub.variants...)
		}
		return out, len(out.variants) > 0
	case parser.Repeat:
		if v.Min < 1 {
			return plan{}, false
		}
		return nodePlan(v.Child)
	case parser.Group:
		return nodePlan(v.Child)
	case parser.Sequence:
		return sequencePlan(v.Elements)
	default:
		return plan{}, false
	}
}

// sequencePlan 在序列的每个起始元素上逐步展开前缀文字，并按选择性与变体
// 数量的折中选择最优计划。
func sequencePlan(elements []parser.Node) (plan, bool) {
	var best plan
	bestScore := math.Inf(-1)
	found := false
	prefixMin, prefixMax := 0, 0
	for i := 0; i <= len(elements); i++ {
		// 前缀无上界时只有在能靠 Back 集合把起点收缩到有限位置时才可用。
		back := backByteSet(elements[:i])
		if prefixMax >= 0 || back != nil {
			for _, variants := range expandSuffixSteps(elements[i:]) {
				candidate := plan{variants: make([]planVariant, 0, len(variants))}
				for _, value := range variants {
					candidate.variants = append(candidate.variants, planVariant{value: value, min: prefixMin, max: prefixMax, back: back})
				}
				if score := planScore(candidate); score > bestScore {
					best, bestScore, found = candidate, score, true
				}
			}
		}
		if i == len(elements) {
			break
		}
		min, max, ok := parser.WidthRange(elements[i])
		if !ok {
			break
		}
		prefixMin += min
		if prefixMax >= 0 {
			if max < 0 {
				prefixMax = -1
			} else {
				prefixMax += max
			}
		}
	}
	return best, found
}

func planScore(candidate plan) float64 {
	if len(candidate.variants) == 0 {
		return math.Inf(-1)
	}
	shortest := candidate.variants[0]
	window := 0
	for _, v := range candidate.variants {
		if len(v.value) < len(shortest.value) {
			shortest = v
		}
		if width := v.max - v.min + 1; width > window {
			window = width
		}
	}
	if window < 1 {
		window = 1
	}
	// 每字节具体文字的选择性约为 8 bit；按变体数量折减，并对变体数量和
	// 偏移窗口宽度施加额外代价，避免为极小的选择性收益引入大量候选文字。
	count := float64(len(candidate.variants))
	return float64(len(shortest.value)*8) - math.Log2(count) - 0.25*count - 0.5*math.Log2(float64(window))
}

// expandSuffixSteps 返回序列后缀逐单位展开后的累积前缀文字集合。
// 每个元素内部的固定次数重复会逐轮暴露，便于调用方在“更长的文字”与
// “更多变体”之间取舍。元素未能完整展开时立即停止，避免把只成立在部分
// 重复上的前缀与后续元素拼接成并不必然出现的文字。
func expandSuffixSteps(elements []parser.Node) [][][]byte {
	acc := [][][]byte{{{}}}
	for _, element := range elements {
		steps, complete := expandPrefixSteps(element)
		if len(steps) == 0 {
			break
		}
		base := acc[len(acc)-1]
		failed := false
		for _, step := range steps {
			product, ok := crossProduct(base, step)
			if !ok {
				failed = true
				break
			}
			acc = append(acc, nonEmpty(product))
		}
		if failed || !complete || !fixedWidth(element) {
			break
		}
	}
	out := make([][][]byte, 0, len(acc))
	for _, step := range acc {
		cleaned := nonEmpty(step)
		if len(cleaned) > 0 {
			out = append(out, cleaned)
		}
	}
	return out
}

// expandPrefixSteps 返回节点在消费 1..n 个单位后各自的累积前缀文字集合，
// 以及这些步骤是否已覆盖节点的最小宽度。返回 nil 表示该节点无法展开为
// 任何文字前缀；complete 为 false 表示最后一个步骤只是部分前缀，调用方
// 只能把它当作候选文字使用，不能继续拼接后续元素。
func expandPrefixSteps(n parser.Node) ([][][]byte, bool) {
	switch v := n.(type) {
	case parser.Group:
		return expandPrefixSteps(v.Child)
	case parser.Sequence:
		acc := [][][]byte{{{}}}
		for _, element := range v.Elements {
			steps, complete := expandPrefixSteps(element)
			if len(steps) == 0 {
				return nil, false
			}
			base := acc[len(acc)-1]
			for _, step := range steps {
				product, ok := crossProduct(base, step)
				if !ok {
					return nil, false
				}
				acc = append(acc, product)
			}
			if !complete {
				return acc, false
			}
		}
		return acc, true
	case parser.Repeat:
		return expandRepeatSteps(v)
	case parser.Literal:
		if len(v.Value) == 0 {
			return [][][]byte{{{}}}, true
		}
		return [][][]byte{{v.Value}}, true
	case parser.Class:
		bytes, ok := classBytes(v)
		if !ok {
			return nil, false
		}
		out := make([][]byte, 0, len(bytes))
		for _, b := range bytes {
			out = append(out, []byte{b})
		}
		return [][][]byte{out}, true
	case parser.Assertion, parser.ControlVerb, parser.Lookaround:
		return [][][]byte{{{}}}, true
	case parser.Alternation:
		if len(v.Options) == 0 {
			return nil, false
		}
		optionSteps := make([][][][]byte, 0, len(v.Options))
		longest := 0
		complete := true
		for _, option := range v.Options {
			steps, optionComplete := expandPrefixSteps(option)
			if len(steps) == 0 {
				return nil, false
			}
			if !optionComplete {
				complete = false
			}
			optionSteps = append(optionSteps, steps)
			if len(steps) > longest {
				longest = len(steps)
			}
		}
		out := make([][][]byte, 0, longest)
		for step := 0; step < longest; step++ {
			union := make([][]byte, 0)
			for _, steps := range optionSteps {
				index := step
				if index >= len(steps) {
					index = len(steps) - 1
				}
				if len(union)+len(steps[index]) > maxVariants {
					return out, false
				}
				union = append(union, steps[index]...)
			}
			out = append(out, union)
		}
		return out, complete
	default:
		return nil, false
	}
}

func expandRepeatSteps(v parser.Repeat) ([][][]byte, bool) {
	switch {
	case v.Min == 0 && v.Max == 0:
		return [][][]byte{{{}}}, true
	case v.Min < 1 || v.Max < 0:
		return nil, false
	}
	child := mustPrefix(v.Child)
	if len(child) == 0 {
		return nil, false
	}
	steps := [][][]byte{{{}}}
	reachedMin := true
	for range v.Min {
		product, ok := crossProduct(steps[len(steps)-1], child)
		if !ok {
			reachedMin = false
			break
		}
		steps = append(steps, product)
	}
	if len(steps) == 1 {
		return nil, false
	}
	return steps, reachedMin
}

// fixedWidth 判断节点每次匹配消耗的字节数是否固定：可变宽度节点会让后续
// 元素的相对偏移漂移，拼接得到的文字并不必然出现。
func fixedWidth(n parser.Node) bool {
	switch v := n.(type) {
	case parser.Group:
		return fixedWidth(v.Child)
	case parser.Sequence:
		for _, element := range v.Elements {
			if !fixedWidth(element) {
				return false
			}
		}
		return true
	case parser.Repeat:
		return v.Min == v.Max && fixedWidth(v.Child)
	case parser.Alternation:
		for _, option := range v.Options {
			if !fixedWidth(option) {
				return false
			}
		}
		return true
	default:
		return true
	}
}

// mustPrefix 返回节点最小宽度对应的完整前缀文字集合；节点无法完整展开时返回 nil。
func mustPrefix(n parser.Node) [][]byte {
	steps, complete := expandPrefixSteps(n)
	if len(steps) == 0 || !complete {
		return nil
	}
	return steps[len(steps)-1]
}

func nonEmpty(variants [][]byte) [][]byte {
	out := make([][]byte, 0, len(variants))
	for _, value := range variants {
		if len(value) > 0 {
			out = append(out, value)
		}
	}
	return out
}

func crossProduct(left, right [][]byte) ([][]byte, bool) {
	if len(left) == 0 || len(right) == 0 {
		return nil, false
	}
	if len(left)*len(right) > maxVariants {
		return nil, false
	}
	out := make([][]byte, 0, len(left)*len(right))
	for _, a := range left {
		for _, b := range right {
			if len(a)+len(b) > maxLiteralBytes {
				return nil, false
			}
			value := make([]byte, 0, len(a)+len(b))
			value = append(value, a...)
			value = append(value, b...)
			out = append(out, value)
		}
	}
	return out, true
}

// backByteSet 在序列前缀恰好是单个有界类重复时返回该类的字节集合。
func backByteSet(elements []parser.Node) []byte {
	if len(elements) != 1 {
		return nil
	}
	node := elements[0]
	for {
		group, ok := node.(parser.Group)
		if !ok {
			break
		}
		node = group.Child
	}
	repeat, ok := node.(parser.Repeat)
	if !ok || repeat.Min < 1 {
		return nil
	}
	if repeat.Max >= 0 && repeat.Max < repeat.Min {
		return nil
	}
	class, ok := repeat.Child.(parser.Class)
	if !ok || class.Negated {
		return nil
	}
	bytes, ok := classBytes(class)
	if !ok {
		return nil
	}
	set := make([]byte, 256)
	for _, b := range bytes {
		set[b] = 1
	}
	return set
}

func classBytes(class parser.Class) ([]byte, bool) {
	set := make([]bool, 256)
	count := 0
	for _, r := range class.Ranges {
		for value := int(r.Lo); value <= int(r.Hi); value++ {
			if !set[value] {
				set[value] = true
				count++
			}
		}
	}
	out := make([]byte, 0, count)
	for value := 0; value < 256; value++ {
		matched := set[value]
		if class.Negated {
			matched = !matched
		}
		if matched {
			out = append(out, byte(value))
		}
	}
	if len(out) == 0 || len(out) > maxVariants {
		return nil, false
	}
	return out, true
}
