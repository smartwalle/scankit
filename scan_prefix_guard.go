package scankit

import "github.com/smartwalle/scankit/internal/parser"

// prefixGuardLimit 限制前缀字节约束的深度。约束越长过滤越强，但每个候选起点的
// 检查常数也随之增长；32 字节足以覆盖固定长度形态（例如 32 位十六进制摘要）。
const prefixGuardLimit = 32

// prefixGuard 保存规则在匹配起点之后必然消费的逐字节集合。集合只包含匹配一定
// 消费的字节位置和一定命中的取值，因此被它拒绝的起点不可能产生命中。
//
// 约束只用在"把一次必须文字命中摊开成多个候选起点"的位置：这里每个窗口内只有
// 少数起点能通过约束，查表的收益远大于成本。逐起点确认（verify）不再重复调用它
// ——那里的候选已经由窗口收敛过一次，再按整段约束重扫一遍实测只增加常数开销。
type prefixGuard struct {
	sets [][4]uint64
	// before/after 保存由首尾 \b 断言折算出的相邻字节约束。起点前一个字节、
	// 以及固定偏移 afterOffset 处的字节都必须不属于对应集合，越界视为满足。
	before      [4]uint64
	hasBefore   bool
	after       [4]uint64
	afterOffset int
	hasAfter    bool
}

// newPrefixGuard 从规则语法树推导前缀字节约束；无法安全推导时返回 nil。
func newPrefixGuard(rule compiledRule) *prefixGuard {
	if rule.root == nil || rule.comb != nil || rule.ext != nil {
		return nil
	}
	// 大小写折叠、UTF-8 与 Unicode 语义都会让字节级推导失效。
	if rule.flags&(FlagCaseless|FlagUTF8|FlagUCP) != 0 || parser.HasScopedFlags(rule.root) {
		return nil
	}
	if rule.flags&FlagAllowEmpty != 0 {
		return nil
	}
	sets, _ := prefixSets(rule.root, prefixGuardLimit)
	if len(sets) == 0 {
		return nil
	}
	guard := &prefixGuard{sets: sets}
	applyWordBoundaryConstraints(guard, rule.root)
	return guard
}

// applyWordBoundaryConstraints 把模式首尾的 \b 断言折算成相邻字节约束。
// 断言只有紧邻必然属于单词字节集合的消费字节时才能用单个字节集合表达，
// 断言前是可变长度重复、或首末字节集合跨出单词范围时一律放弃推导。
func applyWordBoundaryConstraints(guard *prefixGuard, root parser.Node) {
	elements, ok := topLevelElements(root)
	if !ok || len(elements) < 2 {
		return
	}
	if isWordBoundary(elements[0]) {
		if head, ok := firstByteSet(elements[1:]); ok && byteSetSubset(head, wordBytes) {
			guard.before, guard.hasBefore = wordBytes, true
		}
	}
	last := len(elements) - 1
	if isWordBoundary(elements[last]) {
		if length, tail, ok := fixedTailByteSet(elements[:last]); ok && length > 0 && byteSetSubset(tail, wordBytes) {
			guard.after, guard.afterOffset, guard.hasAfter = wordBytes, length, true
		}
	}
}

// topLevelElements 展开顶层分组后返回序列元素；非序列结构不参与断言折算。
func topLevelElements(node parser.Node) ([]parser.Node, bool) {
	for {
		group, ok := node.(parser.Group)
		if !ok {
			break
		}
		node = group.Child
	}
	sequence, ok := node.(parser.Sequence)
	if !ok {
		return nil, false
	}
	return sequence.Elements, true
}

func isWordBoundary(node parser.Node) bool {
	assertion, ok := node.(parser.Assertion)
	return ok && assertion.Kind == parser.WordBoundary
}

// firstByteSet 返回节点序列必然消费的首字节集合；首字节不受限时返回 false。
func firstByteSet(nodes []parser.Node) ([4]uint64, bool) {
	if len(nodes) == 0 {
		return [4]uint64{}, false
	}
	sets, _ := prefixSets(parser.Sequence{Elements: nodes}, 1)
	if len(sets) == 0 {
		return [4]uint64{}, false
	}
	return sets[0], true
}

// fixedTailByteSet 返回节点序列必然消费的字节数与末字节集合；长度可变时返回 false。
func fixedTailByteSet(nodes []parser.Node) (int, [4]uint64, bool) {
	total := 0
	var tail [4]uint64
	hasTail := false
	for _, node := range nodes {
		length, set, nodeHasTail, ok := fixedConsume(node)
		if !ok {
			return 0, [4]uint64{}, false
		}
		total += length
		if nodeHasTail {
			tail, hasTail = set, true
		}
	}
	return total, tail, hasTail
}

// fixedConsume 推导节点必然消费的固定字节数与末字节集合。只有整棵子树长度
// 固定时 ok 才为真，任何可变长度形态都会让调用方放弃断言折算。
func fixedConsume(node parser.Node) (int, [4]uint64, bool, bool) {
	var empty [4]uint64
	switch v := node.(type) {
	case parser.Group:
		return fixedConsume(v.Child)
	case parser.Literal:
		if len(v.Value) == 0 {
			return 0, empty, false, true
		}
		return len(v.Value), singleByteSet(v.Value[len(v.Value)-1]), true, true
	case parser.Class:
		return 1, classByteSet(v), true, true
	case parser.Any:
		return 1, anyByteSet(), true, true
	case parser.Assertion, parser.ControlVerb:
		return 0, empty, false, true
	case parser.Sequence:
		total := 0
		var tail [4]uint64
		hasTail := false
		for _, element := range v.Elements {
			length, set, elementHasTail, ok := fixedConsume(element)
			if !ok {
				return 0, empty, false, false
			}
			total += length
			if elementHasTail {
				tail, hasTail = set, true
			}
		}
		return total, tail, hasTail, true
	case parser.Repeat:
		if v.Min != v.Max || v.Min == 0 {
			return 0, empty, false, false
		}
		length, set, hasTail, ok := fixedConsume(v.Child)
		if !ok || length == 0 || !hasTail {
			return 0, empty, false, false
		}
		return length * v.Min, set, true, true
	default:
		return 0, empty, false, false
	}
}

func byteSetSubset(subset, superset [4]uint64) bool {
	for index := range subset {
		if subset[index]&^superset[index] != 0 {
			return false
		}
	}
	return true
}

// wordBytes 是 ASCII 语义下 \b 断言的单词字节集合；大小写折叠、UTF-8 与
// Unicode 语义的规则不会进入前缀约束推导。
var wordBytes = func() [4]uint64 {
	var set [4]uint64
	for _, current := range [...]struct{ lo, hi byte }{
		{'0', '9'}, {'A', 'Z'}, {'a', 'z'}, {'_', '_'},
	} {
		for value := int(current.lo); value <= int(current.hi); value++ {
			set[value>>6] |= uint64(1) << (uint(value) & 63)
		}
	}
	return set
}()

// longestEqualWindow 返回约束中最长的一段同字节集合窗口。窗口 [offset, offset+length)
// 内的每个位置都以同一集合为约束，因此可以用一次线性扫描求出全部满足该窗口的
// 起点；`[0-9]{17}[0-9Xx]`、`[A-Z][0-9]{8}` 这类只有局部定长重复的形态也能派生
// 候选，而不必依赖候选文字命中。
func (guard *prefixGuard) longestEqualWindow() (set [4]uint64, offset int, length int, ok bool) {
	if guard == nil || len(guard.sets) == 0 {
		return set, 0, 0, false
	}
	sets := guard.sets
	bestStart, bestLength := 0, 0
	start := 0
	for index := 1; index <= len(sets); index++ {
		if index < len(sets) && sets[index] == sets[start] {
			continue
		}
		if current := index - start; current > bestLength {
			bestStart, bestLength = start, current
		}
		start = index
	}
	if bestLength == 0 || sets[bestStart] == [4]uint64{} {
		return set, 0, 0, false
	}
	return sets[bestStart], bestStart, bestLength, true
}

// allows 判断起点是否满足前缀字节约束；nil 接收者或空约束表示不做过滤。
func (guard *prefixGuard) allows(data []byte, start int) bool {
	if guard == nil || len(guard.sets) == 0 && !guard.hasBoundary() {
		return true
	}
	if start < 0 {
		return true
	}
	if !guard.allowsBoundary(data, start) {
		return false
	}
	if len(guard.sets) == 0 {
		return true
	}
	if len(data)-start < len(guard.sets) {
		return false
	}
	sets := guard.sets
	window := data[start:]
	for index := range sets {
		// 直接索引切片元素，避免 range 复制 32 字节的字节集合。
		set := &sets[index]
		value := window[index]
		if set[value>>6]&(uint64(1)<<(value&63)) == 0 {
			return false
		}
	}
	return true
}

// hasBoundary 判断是否存在由首尾断言折算出的相邻字节约束。
func (guard *prefixGuard) hasBoundary() bool {
	return guard != nil && (guard.hasBefore || guard.hasAfter)
}

// allowsBoundary 只校验相邻字节约束，供窗口已经覆盖全部前缀集合的调用方
// 复用：此时窗口扫描已保证前缀成立，再逐候选重扫整段前缀只是重复开销。
func (guard *prefixGuard) allowsBoundary(data []byte, start int) bool {
	if guard == nil {
		return true
	}
	if start < 0 {
		return true
	}
	if guard.hasBefore && start > 0 {
		value := data[start-1]
		if guard.before[value>>6]&(uint64(1)<<(value&63)) != 0 {
			return false
		}
	}
	if guard.hasAfter {
		if pos := start + guard.afterOffset; pos < len(data) {
			value := data[pos]
			if guard.after[value>>6]&(uint64(1)<<(value&63)) != 0 {
				return false
			}
		}
	}
	return true
}

// prefixSets 返回节点从起点开始必然消费的字节集合；covered 表示返回的集合已经
// 覆盖该节点的全部字节位置，调用方据此决定能否继续拼接后续元素。
func prefixSets(node parser.Node, limit int) ([][4]uint64, bool) {
	if limit <= 0 {
		return nil, false
	}
	switch v := node.(type) {
	case parser.Group:
		return prefixSets(v.Child, limit)
	case parser.Literal:
		sets := make([][4]uint64, 0, min(len(v.Value), limit))
		for _, value := range v.Value {
			if len(sets) == limit {
				return sets, false
			}
			sets = append(sets, singleByteSet(value))
		}
		return sets, true
	case parser.Class:
		return [][4]uint64{classByteSet(v)}, true
	case parser.Any:
		return [][4]uint64{anyByteSet()}, true
	case parser.Assertion, parser.Lookaround, parser.ControlVerb:
		// 零宽结构不消费字节，也不改变后续字节的相对位置。
		return nil, true
	case parser.Sequence:
		sets := make([][4]uint64, 0, limit)
		for _, element := range v.Elements {
			if len(sets) >= limit {
				return sets, false
			}
			part, covered := prefixSets(element, limit-len(sets))
			sets = append(sets, part...)
			if !covered {
				return sets, false
			}
		}
		return sets, true
	case parser.Alternation:
		if len(v.Options) == 0 {
			return nil, false
		}
		var common [][4]uint64
		covered := true
		for index, option := range v.Options {
			part, optionCovered := prefixSets(option, limit)
			if index == 0 {
				common = part
			} else if !sameByteSets(common, part) {
				return nil, false
			}
			covered = covered && optionCovered
		}
		return common, covered
	case parser.Repeat:
		if v.Max == 0 {
			return nil, true
		}
		if v.Min < 1 {
			return nil, false
		}
		child, childCovered := prefixSets(v.Child, limit)
		if len(child) == 0 {
			return nil, false
		}
		// 子节点宽度可变时只有第一次重复的字节位置是确定的。
		repetitions := v.Min
		if !childCovered {
			repetitions = 1
		}
		total := len(child) * repetitions
		if total > limit {
			// 截断到上限：前缀约束仍然成立，只是不再覆盖整个节点。
			sets := make([][4]uint64, 0, limit)
			for len(sets) < limit {
				remain := limit - len(sets)
				if remain >= len(child) {
					sets = append(sets, child...)
					continue
				}
				sets = append(sets, child[:remain]...)
			}
			return sets, false
		}
		sets := make([][4]uint64, 0, total)
		for range repetitions {
			sets = append(sets, child...)
		}
		return sets, childCovered && v.Min == v.Max
	default:
		return nil, false
	}
}

func sameByteSets(left, right [][4]uint64) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func singleByteSet(value byte) [4]uint64 {
	var set [4]uint64
	set[value>>6] = uint64(1) << (value & 63)
	return set
}

func anyByteSet() [4]uint64 {
	return [4]uint64{^uint64(0), ^uint64(0), ^uint64(0), ^uint64(0)}
}

func classByteSet(class parser.Class) [4]uint64 {
	var set [4]uint64
	for _, current := range class.Ranges {
		for value := int(current.Lo); ; value++ {
			set[value>>6] |= uint64(1) << (uint(value) & 63)
			if value == int(current.Hi) {
				break
			}
		}
	}
	if class.Negated {
		for index := range set {
			set[index] = ^set[index]
		}
	}
	return set
}
