package prefilter

import (
	"math"
	"slices"

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
	// class 表示文字的字节至少有一部分由字符类展开而来（而非字面量）。
	class bool
}

// classOnlySingleByte 判断计划是否完全由字符类展开出的单字节候选组成。
// 只要存在一个多字节候选、或存在一个来自字面量的候选，计划就不属于该形态，
// 因为这两类候选都能给出与输入内容无关的选择性下界。
func classOnlySingleByte(variants []planVariant) bool {
	for _, variant := range variants {
		if len(variant.value) != 1 || !variant.class {
			return false
		}
	}
	return len(variants) > 0
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
	// consider 登记一组候选文字，并在候选退化为"完全由字符类展开出的单字节"
	// 形态时额外尝试左合并：把前序序列末尾必然出现的文字并入候选，升级为多字节
	// 候选。类中可能包含 `e` 这类高频字节，单字节候选的命中密度与大块输入同阶，
	// 合并后密度回落到与输入内容无关的水平；两个计划都参与打分，只有总分更高时
	// 才会替换原候选，其余模式的提取结果保持不变。
	consider := func(steps []literalStep, minWidth, maxWidth int, back []byte, trail []literalStep, trailLen int) {
		if len(steps) == 0 {
			return
		}
		candidate := plan{variants: make([]planVariant, 0, len(steps))}
		for _, step := range steps {
			candidate.variants = append(candidate.variants, planVariant{value: step.value, min: minWidth, max: maxWidth, back: back, class: step.class})
		}
		if score := planScore(candidate); score > bestScore {
			best, bestScore, found = candidate, score, true
		}
		if !classOnlySingleByte(candidate.variants) {
			return
		}
		if len(trail) == 0 || trailLen <= 0 || minWidth < trailLen || len(trail)*len(steps) > maxVariants {
			return
		}
		mergedMin, mergedMax := minWidth-trailLen, maxWidth
		if mergedMax >= 0 {
			mergedMax -= trailLen
		}
		mergedSeen := make(map[string]struct{}, len(trail)*len(steps))
		merged := plan{variants: make([]planVariant, 0, len(trail)*len(steps))}
		for _, tail := range trail {
			for _, step := range steps {
				if len(tail.value)+len(step.value) > maxLiteralBytes {
					return
				}
				value := make([]byte, 0, len(tail.value)+len(step.value))
				value = append(value, tail.value...)
				value = append(value, step.value...)
				if _, dup := mergedSeen[string(value)]; dup {
					continue
				}
				mergedSeen[string(value)] = struct{}{}
				merged.variants = append(merged.variants, planVariant{
					value: value,
					min:   mergedMin,
					max:   mergedMax,
					back:  back,
					class: tail.class || step.class,
				})
			}
		}
		if score := planScore(merged); score > bestScore {
			best, bestScore, found = merged, score, true
		}
	}
	prefixMin, prefixMax := 0, 0
	for i := 0; i <= len(elements); i++ {
		// 前缀无上界时只有在能靠 Back 集合把起点收缩到有限位置时才可用。
		back := backByteSet(elements[:i])
		if prefixMax >= 0 || back != nil {
			trail, trailLen, trailOK := trailingUniform(elements[:i])
			if !trailOK {
				trail, trailLen = nil, 0
			}
			for _, variants := range expandSuffixSteps(elements[i:]) {
				consider(variants, prefixMin, prefixMax, back, trail, trailLen)
			}
			// 后续元素无法完整展开时（例如 `[0-9]{8}-[0-9]{4}` 这类长重复），
			// 逐单位拼接会提前中断；此处至少取该元素起点处必然出现的文字，
			// 让 `-`、`:` 这类固定分隔符仍能作为高选择性候选。
			if i < len(elements) {
				if steps, ok := leadingLiterals(elements[i]); ok && len(steps) > 0 {
					consider(steps, prefixMin, prefixMax, back, trail, trailLen)
				}
			}
		}
		if i == len(elements) {
			break
		}
		minWidth, maxWidth, ok := parser.WidthRange(elements[i])
		if !ok {
			break
		}
		prefixMin += minWidth
		if prefixMax >= 0 {
			if maxWidth < 0 {
				prefixMax = -1
			} else {
				prefixMax += maxWidth
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

// literalStep 是一个候选文字，并携带该文字是否由字符类展开而来。
// 单字节且来自字符类的候选（如 `[eE]` 展开出的 `e`、`E`）不具备稳定
// 选择性，调用方据此触发左合并把它升级为多字节候选。
type literalStep struct {
	value []byte
	class bool
}

// expandSuffixSteps 返回序列后缀逐单位展开后的累积前缀文字集合。
// 每个元素内部的固定次数重复会逐轮暴露，便于调用方在“更长的文字”与
// “更多变体”之间取舍。元素未能完整展开时立即停止，避免把只成立在部分
// 重复上的前缀与后续元素拼接成并不必然出现的文字。
func expandSuffixSteps(elements []parser.Node) [][]literalStep {
	acc := [][]literalStep{{{}}}
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
	out := make([][]literalStep, 0, len(acc))
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
func expandPrefixSteps(n parser.Node) ([][]literalStep, bool) {
	switch v := n.(type) {
	case parser.Group:
		return expandPrefixSteps(v.Child)
	case parser.Sequence:
		acc := [][]literalStep{{{}}}
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
			return [][]literalStep{{{}}}, true
		}
		return [][]literalStep{{{value: v.Value}}}, true
	case parser.Class:
		bytes, ok := classBytes(v)
		if !ok {
			return nil, false
		}
		out := make([]literalStep, 0, len(bytes))
		for _, b := range bytes {
			out = append(out, literalStep{value: []byte{b}, class: true})
		}
		return [][]literalStep{out}, true
	case parser.Assertion, parser.ControlVerb, parser.Lookaround:
		return [][]literalStep{{{}}}, true
	case parser.Alternation:
		if len(v.Options) == 0 {
			return nil, false
		}
		optionSteps := make([][][]literalStep, 0, len(v.Options))
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
		out := make([][]literalStep, 0, longest)
		for step := 0; step < longest; step++ {
			union := make([]literalStep, 0)
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

func expandRepeatSteps(v parser.Repeat) ([][]literalStep, bool) {
	switch {
	case v.Min == 0 && v.Max == 0:
		return [][]literalStep{{{}}}, true
	case v.Min < 1 || v.Max < 0:
		return nil, false
	default:
	}
	child := mustPrefix(v.Child)
	if len(child) == 0 {
		return nil, false
	}
	steps := [][]literalStep{{{}}}
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

// leadingLiterals 返回节点起点处必然出现的候选文字集合。集合中的每个文字都是
// "或"关系，且一定出现在节点消耗的第一个字节处；ok 为 false 表示无法保证任何
// 文字出现在节点起点（例如节点可为空、以点号开头或类展开超过变体上限）。
//
// sequencePlan 用它补上"后续元素无法完整展开"时的候选：`[0-9]{8}-[0-9]{4}` 这类
// 长重复无法逐字节拼接，但分隔符 `-` 一定出现在固定偏移处，是比单字节类成员更
// 高选择性的候选。
func leadingLiterals(n parser.Node) ([]literalStep, bool) {
	switch v := n.(type) {
	case parser.Group:
		return leadingLiterals(v.Child)
	case parser.Literal:
		if len(v.Value) == 0 {
			return nil, false
		}
		return []literalStep{{value: v.Value}}, true
	case parser.Class:
		bytes, ok := classBytes(v)
		if !ok || len(bytes) == 0 || len(bytes) > maxVariants {
			return nil, false
		}
		out := make([]literalStep, 0, len(bytes))
		for _, b := range bytes {
			out = append(out, literalStep{value: []byte{b}, class: true})
		}
		return out, true
	case parser.Sequence:
		for _, element := range v.Elements {
			if zeroWidthNode(element) {
				continue
			}
			return leadingLiterals(element)
		}
		return nil, false
	case parser.Repeat:
		if v.Min < 1 {
			return nil, false
		}
		return leadingLiterals(v.Child)
	case parser.Alternation:
		if len(v.Options) == 0 {
			return nil, false
		}
		out := make([]literalStep, 0, len(v.Options))
		for _, option := range v.Options {
			part, ok := leadingLiterals(option)
			if !ok || len(out)+len(part) > maxVariants {
				return nil, false
			}
			out = append(out, part...)
		}
		return out, true
	default:
		return nil, false
	}
}

// maxTrailingBytes 限制左合并时从前序序列末尾截取的字节数。截取得越长，
// 合并后的候选越具选择性，但与前缀候选的笛卡尔积也会同步放大。
const maxTrailingBytes = 4

// trailingUniform 返回元素序列末尾必然出现的候选文字集合，并要求集合内所有
// 文字长度一致：只有长度一致才能把合并后的偏移窗口整体左移同一距离。返回
// ok 为 false 表示序列为空、末尾无法保证任何文字，或候选长度不一致。
func trailingUniform(elements []parser.Node) ([]literalStep, int, bool) {
	if len(elements) == 0 {
		return nil, 0, false
	}
	steps, ok := trailingLiterals(elements)
	if !ok || len(steps) == 0 {
		return nil, 0, false
	}
	// 末尾候选按分支取并集，同一文字可能被多条分支重复贡献；去重后变体数量
	// 才反映真实候选规模，也避免重复变体压低打分。
	steps = dedupLiteralSteps(steps)
	length := len(steps[0].value)
	for _, step := range steps {
		if len(step.value) != length {
			return nil, 0, false
		}
	}
	return steps, length, true
}

// dedupLiteralSteps 按文字内容去重，保留首次出现的顺序。
func dedupLiteralSteps(steps []literalStep) []literalStep {
	if len(steps) < 2 {
		return steps
	}
	seen := make(map[string]struct{}, len(steps))
	out := steps[:0]
	for _, step := range steps {
		key := string(step.value)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, step)
	}
	return out
}

// trailingLiterals 返回元素序列末尾必然出现的候选文字集合：每个文字都一定出现
// 在序列最后消费的字节处，集合本身是"或"关系。
//
// 末尾可空元素会让末尾字节有两条来源——它自己非空匹配时的尾部，或者它空匹配时
// 由更早元素决定的尾部；两条分支的候选都要并入集合，否则会漏掉其中一类匹配。
// 序列整体可空且找不到任何必现字节时返回 ok 为 false。
func trailingLiterals(elements []parser.Node) ([]literalStep, bool) {
	var out []literalStep
	for _, element := range slices.Backward(elements) {

		if zeroWidthNode(element) {
			// 零宽结构不消费字节，也不影响末尾字节来自哪个元素。
			continue
		}
		// 该元素非空匹配时贡献的末尾候选；空匹配时末尾字节由更早元素决定，
		// 因此无论它是否可空都要先把这部分并进来。
		if steps, ok := trailingLiteralsOfNode(element); ok && len(steps) > 0 {
			out = append(out, steps...)
			if len(out) > maxVariants {
				return nil, false
			}
		}
		if !parser.Nullable(element) {
			return out, len(out) > 0
		}
	}
	return out, len(out) > 0
}

// trailingLiteralsOfNode 返回单个节点在消费至少一个字节时的末尾候选文字集合。
// 节点只能匹配空串时返回 ok 为 false。
func trailingLiteralsOfNode(n parser.Node) ([]literalStep, bool) {
	switch v := n.(type) {
	case parser.Group:
		return trailingLiteralsOfNode(v.Child)
	case parser.Sequence:
		return trailingLiterals(v.Elements)
	case parser.Repeat:
		// 可空子节点会让最后一次重复可能整个为空，末尾字节随之前移到更早的
		// 迭代，静态推导无法穷举，直接放弃。
		if parser.Nullable(v.Child) {
			return nil, false
		}
		if v.Max == 0 {
			return nil, false
		}
		return trailingLiteralsOfNode(v.Child)
	case parser.Literal:
		if len(v.Value) == 0 {
			return nil, false
		}
		value := v.Value
		if len(value) > maxTrailingBytes {
			value = value[len(value)-maxTrailingBytes:]
		}
		return []literalStep{{value: value}}, true
	case parser.Class:
		bytes, ok := classBytes(v)
		if !ok || len(bytes) == 0 || len(bytes) > maxVariants {
			return nil, false
		}
		out := make([]literalStep, 0, len(bytes))
		for _, b := range bytes {
			out = append(out, literalStep{value: []byte{b}, class: true})
		}
		return out, true
	case parser.Alternation:
		if len(v.Options) == 0 {
			return nil, false
		}
		out := make([]literalStep, 0, len(v.Options))
		for _, option := range v.Options {
			part, ok := trailingLiteralsOfNode(option)
			if !ok || len(part) == 0 {
				// 可空分支的末尾字节由更早元素决定，无法在节点内部封闭推导。
				continue
			}
			if len(out)+len(part) > maxVariants {
				return nil, false
			}
			out = append(out, part...)
		}
		if len(out) == 0 {
			return nil, false
		}
		return out, true
	default:
		return nil, false
	}
}

// zeroWidthNode 判断节点是否不消耗任何字节。
func zeroWidthNode(n parser.Node) bool {
	switch n.(type) {
	case parser.Assertion, parser.ControlVerb, parser.Lookaround:
		return true
	default:
		return false
	}
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
func mustPrefix(n parser.Node) []literalStep {
	steps, complete := expandPrefixSteps(n)
	if len(steps) == 0 || !complete {
		return nil
	}
	return steps[len(steps)-1]
}

func nonEmpty(variants []literalStep) []literalStep {
	out := make([]literalStep, 0, len(variants))
	for _, step := range variants {
		if len(step.value) > 0 {
			out = append(out, step)
		}
	}
	return out
}

func crossProduct(left, right []literalStep) ([]literalStep, bool) {
	if len(left) == 0 || len(right) == 0 {
		return nil, false
	}
	if len(left)*len(right) > maxVariants {
		return nil, false
	}
	out := make([]literalStep, 0, len(left)*len(right))
	for _, a := range left {
		for _, b := range right {
			if len(a.value)+len(b.value) > maxLiteralBytes {
				return nil, false
			}
			value := make([]byte, 0, len(a.value)+len(b.value))
			value = append(value, a.value...)
			value = append(value, b.value...)
			out = append(out, literalStep{value: value, class: a.class || b.class})
		}
	}
	return out, true
}

// maxBackBytes 限制 Back 集合的规模。集合越大，命中位置左侧的收缩区间越
// 长，候选起点随之增多；规模过大的集合（例如取反类）不具备收缩价值。
const maxBackBytes = 96

// backByteSet 返回前缀可能消费的字节集合，用于无上界窗口的左侧收缩。
//
// 返回的集合是前缀全部字节的保守超集：命中位置左侧只有落在集合内的字节才
// 属于前缀，扫描时据此把候选起点收缩到命中位置左侧的连续区间。返回 nil 表示
// 无法给出这样的超集（含点号、无法展开的类或集合规模过大），此时调用方不能
// 接受无上界窗口。
func backByteSet(elements []parser.Node) []byte {
	if len(elements) == 0 {
		return nil
	}
	member := make([]bool, 256)
	for _, element := range elements {
		if !unionInto(member, element) {
			return nil
		}
	}
	// 结果按字节值直接索引：扫描时对命中位置左侧的每个字节做一次 0/1 判定。
	table := make([]byte, 256)
	count := 0
	for value := range 256 {
		if member[value] {
			table[value] = 1
			count++
		}
	}
	if count == 0 || count > maxBackBytes {
		return nil
	}
	return table
}

// unionInto 把节点可能消费的全部字节并入集合。返回 false 表示该节点无法给出
// 字节超集，调用方必须放弃基于 Back 的窗口收缩。
func unionInto(set []bool, node parser.Node) bool {
	switch v := node.(type) {
	case parser.Literal:
		for _, value := range v.Value {
			set[value] = true
		}
		return true
	case parser.Class:
		bytes, ok := classBytes(v)
		if !ok {
			return false
		}
		for _, value := range bytes {
			set[value] = true
		}
		return true
	case parser.Sequence:
		for _, element := range v.Elements {
			if !unionInto(set, element) {
				return false
			}
		}
		return true
	case parser.Alternation:
		for _, option := range v.Options {
			if !unionInto(set, option) {
				return false
			}
		}
		return true
	case parser.Repeat:
		return unionInto(set, v.Child)
	case parser.Group:
		return unionInto(set, v.Child)
	case parser.Assertion, parser.ControlVerb, parser.Lookaround:
		// 零宽结构不消费字节，也不影响后续字节的相对位置。
		return true
	default:
		// Any、UnicodeClass、Backreference、Conditional 等无法给出字节超集。
		return false
	}
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
	for value := range 256 {
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
