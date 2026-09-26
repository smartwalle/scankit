package scankit

import "github.com/smartwalle/scankit/internal/parser"

// anchorKind 标识必然被求值的输入锚点种类。
type anchorKind uint8

const (
	// anchorBegin 表示匹配必须从数据起点开始（\A，或不开启多行时的 ^）。
	anchorBegin anchorKind = iota + 1
	// anchorEnd 表示匹配必须结束于数据末尾（\z，或不开启多行时的 $）。
	anchorEnd
)

// startAnchor 描述规则内部一个必然被求值的输入锚点，以及从匹配起点到该锚点
// 固定消费的字节数。只有偏移在所有分支上唯一时才能用它把候选起点收敛成单个
// 偏移，因此无法证明唯一性时返回 ok=false，调用方保持原有候选枚举行为。
type startAnchor struct {
	kind           anchorKind
	consumedBefore int
}

// ruleStartAnchor 从规则语法树推导唯一可行起点。
//
// 匹配起点为 S 时，位于「匹配内固定消费 C 字节之后」的锚点会被求值在 S+C：
//   - \A 成立要求 S+C == 0，所以只有 C == 0 时才存在可行起点，即 S 恒为 0；
//     C > 0 时该断言在任何起点上都不成立，规则不可能命中。
//   - \z 成立要求 S+C == len(data)，所以 S 被唯一确定为 len(data)-C。
//
// 因此这类规则的候选起点不需要从候选文字的全部命中里展开，可以直接收敛成
// 单个偏移。无法证明偏移唯一时返回 false，候选窗口保持原样。
func ruleStartAnchor(rule compiledRule) (startAnchor, bool) {
	// 组合规则（1|2、1&2、!1）的语法树描述的是规则之间的组合关系，不是可直接
	// 匹配的文本表达式，断言推导不适用。
	if rule.root == nil || rule.comb != nil {
		return startAnchor{}, false
	}
	return requiredAnchor(rule.root, rule.flags)
}

// requiredAnchor 在语法树中寻找必然被求值的输入锚点及其固定消费偏移。
func requiredAnchor(node parser.Node, flags CompileFlag) (startAnchor, bool) {
	switch value := node.(type) {
	case parser.Assertion:
		switch value.Kind {
		case parser.BeginAbsolute:
			return startAnchor{kind: anchorBegin}, true
		case parser.Begin:
			if flags&CompileMultiline == 0 {
				return startAnchor{kind: anchorBegin}, true
			}
		case parser.EndAbsolute:
			return startAnchor{kind: anchorEnd}, true
		case parser.End:
			if flags&CompileMultiline == 0 {
				return startAnchor{kind: anchorEnd}, true
			}
		}
		return startAnchor{}, false
	case parser.Group:
		return requiredAnchor(value.Child, scopedGroupFlags(flags, value))
	case parser.Lookaround:
		// 正向先行断言的子表达式在原位置求值，其内部锚点必然被求值。
		// 负向先行与后行环视在锚点不成立时反而会成功，不能当作约束。
		if value.Kind == parser.Lookahead {
			return requiredAnchor(value.Child, flags)
		}
		return startAnchor{}, false
	case parser.Sequence:
		consumed := 0
		for _, element := range value.Elements {
			if anchor, ok := requiredAnchor(element, flags); ok {
				anchor.consumedBefore += consumed
				return anchor, true
			}
			length, exact := exactConsumedBytes(element)
			if !exact {
				// 前面元素的消费字节数不唯一，后续锚点相对匹配起点的偏移
				// 就不再是固定值，无法用它收敛候选。
				return startAnchor{}, false
			}
			consumed += length
		}
		return startAnchor{}, false
	case parser.Alternation:
		var (
			found startAnchor
			have  bool
		)
		for _, option := range value.Options {
			anchor, ok := requiredAnchor(option, flags)
			if !ok {
				return startAnchor{}, false
			}
			if have && anchor != found {
				return startAnchor{}, false
			}
			found, have = anchor, true
		}
		return found, have
	case parser.Repeat:
		// 至少执行一次的重复，其第一次迭代内的锚点必然被求值，偏移与迭代
		// 起点一致；Min 为 0 时子表达式可以被整体跳过，不能作为约束。
		if value.Min < 1 {
			return startAnchor{}, false
		}
		return requiredAnchor(value.Child, flags)
	}
	return startAnchor{}, false
}

// exactConsumedBytes 返回节点在一次成功匹配中必然消费的字节数。
// 只有全部分支消费同样字节数时才返回 ok=true；调用方只会把它用在候选索引
// 认可的规则上，这些规则已经排除了 UTF8/UCP 语义，字符类与通配符一律按单字节
// 计算。
func exactConsumedBytes(node parser.Node) (int, bool) {
	switch value := node.(type) {
	case parser.Literal:
		return len(value.Value), true
	case parser.Any, parser.Class:
		return 1, true
	case parser.Assertion, parser.Lookaround:
		return 0, true
	case parser.Group:
		return exactConsumedBytes(value.Child)
	case parser.Sequence:
		total := 0
		for _, element := range value.Elements {
			length, ok := exactConsumedBytes(element)
			if !ok {
				return 0, false
			}
			total += length
		}
		return total, true
	case parser.Alternation:
		total := 0
		for index, option := range value.Options {
			length, ok := exactConsumedBytes(option)
			if !ok {
				return 0, false
			}
			if index == 0 {
				total = length
				continue
			}
			if length != total {
				return 0, false
			}
		}
		return total, len(value.Options) > 0
	case parser.Repeat:
		if value.Max >= 0 && value.Min == value.Max {
			length, ok := exactConsumedBytes(value.Child)
			if !ok {
				return 0, false
			}
			return length * value.Min, true
		}
		return 0, false
	}
	return 0, false
}

// anchorEntry 是一条锚定在输入首尾的规则的候选来源。
type anchorEntry struct {
	ruleIndex int
	anchor    startAnchor
}

// pinnedStart 返回锚点推导出的唯一候选起点。ok 为 false 表示锚点位置落在数据
// 范围之外，该规则在本次输入上不可能命中。
func (entry anchorEntry) pinnedStart(dataLen int) (int, bool) {
	if entry.anchor.kind == anchorEnd {
		start := dataLen - entry.anchor.consumedBefore
		if start < 0 {
			return 0, false
		}
		return start, true
	}
	if entry.anchor.consumedBefore != 0 {
		// \A 出现在匹配起点之后：任何起点都无法满足该断言。
		return 0, false
	}
	return 0, true
}
