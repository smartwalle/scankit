package scankit

import (
	"bytes"
	"slices"
	"strings"

	"github.com/smartwalle/scankit/internal/parser"
	"github.com/smartwalle/scankit/internal/prefilter"
)

// 本文件实现 Block 扫描候选起点的确认程序。候选文字索引只能定位“可能命中”
// 的起点，真正的规则语义仍要逐起点求值；通用 AST 求值按节点切分切片、按位置
// 集合去重排序，在命中密集时成为扫描瓶颈。确认程序把规则编译成一维指令序列，
// 由回溯式虚机直接执行：字符类预展开成 256 位查找表，连续类和连续重复各自
// 合并为单条指令，只为真正需要的分支保存回溯点。
//
// 语义契约与通用求值保持一致：返回“偏好结束位置”，即贪婪规则的全部结束
// 偏移中的最大值、非贪婪规则的最小值，正是 blockScanState.record 唯一使用的
// 那一个结束偏移。虚机遇到任何未支持的结构或超过预算时返回 false，调用方
// 回退到既有确认路径，因此确认程序只影响性能，不影响结果。

const (
	// confirmMaxSteps 限制单次确认的虚机步数，避免退化结构在候选路径上
	// 放大成指数级回溯；超限时回退通用求值。
	confirmMaxSteps = 4096
	// confirmMaxStack 限制回溯点数量，达到上限同样回退通用求值。
	confirmMaxStack = 1024
	// confirmMaxInstructions 限制单条规则编译出的指令规模，避免把超大
	// 有界重复展开成不受控的指令序列。
	confirmMaxInstructions = 2048
	// confirmMaxRepeat 限制可展开的有界重复上界。
	confirmMaxRepeat = 256
	// confirmRemainUnbounded 是“剩余可消费字节数无上界”的哨兵值。达到该值
	// 的指令不参与回溯点剪枝，保证剪枝只会丢弃不可能改善偏好结束偏移的帧。
	confirmRemainUnbounded = confirmMaxSteps
)

type confirmOpcode uint8

const (
	// confirmOpLiteral 处理长度不超过 3 的文字，字节直接打包在指令的
	// index 字段里，热路径无需再解引用文字表。
	confirmOpLiteral confirmOpcode = iota
	confirmOpLongLiteral
	confirmOpSet
	confirmOpSetRepeat
	confirmOpAny
	confirmOpAssert
	confirmOpSplit
	confirmOpMatch
)

// confirmPackedLiteralLimit 是可直接内联进指令的文字长度上限。
const confirmPackedLiteralLimit = 3

// packConfirmLiteral 把不超过上限的文字编码成指令字：长度放在最高字节，
// 其余字节按出现顺序依次放在高位。
func packConfirmLiteral(value []byte) int32 {
	packed := int32(len(value)) << 24
	shift := 16
	for _, b := range value {
		packed |= int32(b) << uint(shift)
		shift -= 8
	}
	return packed
}

// confirmInstr 是确认程序的一条指令。index 复用为 split 的备选分支或
// 集合/文字/重复表的编号，next 是顺序后继。定长 12 字节的布局让虚机取指
// 只需一次小拷贝，重复上限等稀疏参数集中存放在 confirmRepeat。
type confirmInstr struct {
	op    confirmOpcode
	kind  parser.AssertionKind
	index int32
	next  int32
}

// confirmRepeat 保存一条集合重复指令的参数，只有 SetRepeat 会索引它。
type confirmRepeat struct {
	set   int32
	min   int32
	max   int32
	final bool
}

// confirmFrame 是虚机保存的回溯点。
type confirmFrame struct {
	pc  int32
	pos int
}

// confirmProgram 是一条规则的确认程序。literals 与 sets 在编译期构建，
// 扫描期只读；nonGreedy 决定返回最小还是最大结束偏移。
type confirmProgram struct {
	instrs    []confirmInstr
	repeats   []confirmRepeat
	literals  [][]byte
	sets      []confirmByteSet
	entry     int32
	nonGreedy bool
	// bounded 记录程序是否只包含有上界重复。执行步数始终受 confirmMaxSteps
	// 约束：有界但分支密集的程序在失败路径上仍可能组合爆炸，唯一的保护是
	// 每步的预算判定。
	bounded bool
	// setRepeatAccept 表示程序入口就是「集合重复 + 接受」两条可达指令：整条规则
	// 等价于一个字符类重复，命中结束偏移可以直接由连续段求出（见
	// setRepeatAcceptEnd），无需解释器逐指令求值。放在既有 bool 之后的填充位
	// 内，不改变 confirmProgram 的大小。
	setRepeatAccept bool
	// remain 是各指令到接受状态的最大可消费字节数，仅在有界程序上构建。
	// 无界程序存在回边，该上界不成立，字段保持 nil 并禁用回溯点剪枝。
	remain []int
}

// confirmByteSet 是展开大小写折叠与取反后的字节判定表。
type confirmByteSet [4]uint64

func (set *confirmByteSet) match(value byte) bool {
	return set[value>>6]&(uint64(1)<<(value&63)) != 0
}

func (c *confirmCompiler) addSet(bits confirmByteSet) int32 {
	c.sets = append(c.sets, bits)
	return int32(len(c.sets) - 1)
}

func (c *confirmCompiler) setFromNode(node parser.Node) (confirmByteSet, bool) {
	var set confirmByteSet
	switch v := node.(type) {
	case parser.Class:
		for value := range 256 {
			if classByteMatch(v, byte(value), c.flags) {
				set[value>>6] |= uint64(1) << (value & 63)
			}
		}
		return set, true
	case parser.Any:
		for value := range 256 {
			if byte(value) != '\n' || c.flags&CompileDotAll != 0 {
				set[value>>6] |= uint64(1) << (value & 63)
			}
		}
		return set, true
	case parser.Literal:
		if len(v.Value) != 1 {
			return set, false
		}
		for value := range 256 {
			if equalByte(v.Value[0], byte(value), c.flags) {
				set[value>>6] |= uint64(1) << (value & 63)
			}
		}
		return set, true
	case parser.Group:
		if v.Atomic || v.HasScopedFlags() {
			return set, false
		}
		return c.setFromNode(v.Child)
	default:
	}
	return set, false
}

type confirmCompiler struct {
	instrs   []confirmInstr
	repeats  []confirmRepeat
	literals [][]byte
	sets     []confirmByteSet
	flags    CompileFlag
	ok       bool
	// unbounded 记录程序是否包含无上界重复生成的回边。这类程序必须保留
	// 步数预算，其余程序的执行步数天然受指令规模约束。
	unbounded bool
}

func (c *confirmCompiler) fail() int32 {
	c.ok = false
	return -1
}

func (c *confirmCompiler) emit(instr confirmInstr) int32 {
	c.instrs = append(c.instrs, instr)
	return int32(len(c.instrs) - 1)
}

// reserve 预留一条稍后回填的指令槽，用于把循环体指回循环入口。
func (c *confirmCompiler) reserve() int32 {
	c.instrs = append(c.instrs, confirmInstr{})
	return int32(len(c.instrs) - 1)
}

func (c *confirmCompiler) addLiteral(value []byte) int32 {
	c.literals = append(c.literals, value)
	return int32(len(c.literals) - 1)
}

func (c *confirmCompiler) addRepeat(set int32, minCount, maxCount int32, final bool) int32 {
	c.repeats = append(c.repeats, confirmRepeat{set: set, min: minCount, max: maxCount, final: final})
	return int32(len(c.repeats) - 1)
}

// compileConfirmProgram 把规则语法树编译成确认程序。返回 nil 表示存在
// 快路径无法保证语义的结构，调用方必须保留通用确认路径。
// leadingLiteral 返回规则开头必然出现的固定文字。返回 nil 表示规则开头
// 不是纯文字，无法用候选文字命中替代这次比较。
func leadingLiteral(root parser.Node) []byte {
	for {
		switch v := root.(type) {
		case parser.Group:
			if v.Atomic || v.HasScopedFlags() {
				return nil
			}
			root = v.Child
		case parser.Sequence:
			if len(v.Elements) == 0 {
				return nil
			}
			root = v.Elements[0]
		case parser.Literal:
			return v.Value
		default:
			return nil
		}
	}
}

// requiredImpliesLiteral 判断候选文字集合能否证明指定文字已在匹配起点出现：
// 只有当每个变体都固定出现在匹配起点（偏移窗口为 0）且以该文字开头时成立。
func requiredImpliesLiteral(required []prefilter.Variant, literal []byte) bool {
	if len(required) == 0 || len(literal) == 0 {
		return false
	}
	for _, variant := range required {
		if variant.MinOffset != 0 || variant.MaxOffset != 0 {
			return false
		}
		if len(variant.Value) < len(literal) || string(variant.Value[:len(literal)]) != string(literal) {
			return false
		}
	}
	return true
}

// requiredImpliesLiteralSet 判断候选文字集合能否证明起点处的前 length 个字节一定
// 落在给定文字集合内：只有当每个变体都固定出现在匹配起点（偏移窗口为 0）、不是由
// 字符类展开而来、长度不短于 length，且该前缀属于集合时成立。
func requiredImpliesLiteralSet(required []prefilter.Variant, leaves map[string]struct{}, length int) bool {
	if len(required) == 0 || len(leaves) == 0 || length <= 0 {
		return false
	}
	for _, variant := range required {
		if variant.Class || variant.MinOffset != 0 || variant.MaxOffset != 0 {
			return false
		}
		if len(variant.Value) < length {
			return false
		}
		if _, ok := leaves[string(variant.Value[:length])]; !ok {
			return false
		}
	}
	return true
}

// confirmExactEntry 是 confirmEntry 的哨兵值：跳过入口文字后确认程序立刻命中，
// 结束偏移恒为 start+confirmSkip，不需要进入解释器。真实的程序入口一定非负。
const confirmExactEntry int32 = -1

// leadingLiteralSkip 推导确认程序入口处可以整体跳过的前缀文字，返回后继指令与
// 跳过字节数。两种形态都要求候选索引能保证起点处的这些字节：
//
//   - 入口是唯一固定文字（`needle...`）：候选文字以该文字开头且偏移为 0；
//   - 入口是一组等长文字分支（`ab|cd`）：候选文字的等长前缀全部落在分支集合内。
//
// 第二种形态覆盖纯文字析取：候选本来就由文字查找器给出，确认程序却要重新比较
// 同一批文字。`ab|cd` 在 64 KiB 固定语料上是扫描里最大的单个函数（见 §47.4）。
func leadingLiteralSkip(rule *compiledRule) (int32, int, bool) {
	confirm := rule.confirm
	if literal := leadingLiteral(rule.root); len(literal) > 0 && requiredImpliesLiteral(rule.required, literal) {
		if entry, ok := confirm.afterLeadingLiteral(literal); ok {
			return entry, len(literal), true
		}
	}
	// 忽略大小写与扩展参数会改变候选索引与确认程序之间的字节比较语义，
	// 集合形态只在两者都是逐字节比较时启用。
	if rule.ext != nil || ruleCaseless(*rule) || parser.HasScopedFlags(rule.root) {
		return 0, 0, false
	}
	leaves, length, entry, ok := confirm.afterLeadingLiteralSet()
	if !ok || !requiredImpliesLiteralSet(rule.required, leaves, length) {
		return 0, 0, false
	}
	return entry, length, true
}

// afterLeadingLiteralSet 解析确认程序入口处的「一组等长文字分支」。入口必须
// 只由 Split 与等长文字指令组成：每个叶子消费同样多的字节，并且都跳到同一个
// 后继。返回叶子文字集合、字节数与后继指令；结构不符时 ok 为 false。
//
// 交替（`ab|cd`）编译出的入口就是这种形态：Split 链 + 每个分支一条文字指令。
// 候选索引已经保证起点处的这些字节落在叶子集合内时，入口比较必然成立，可以
// 整体跳到后继，不必逐字节重走候选文字。
func (p *confirmProgram) afterLeadingLiteralSet() (map[string]struct{}, int, int32, bool) {
	if p == nil || p.entry < 0 || int(p.entry) >= len(p.instrs) {
		return nil, 0, 0, false
	}
	leaves := make(map[string]struct{}, 4)
	length, next := -1, int32(-1)
	stack := []int32{p.entry}
	visited := 0
	for len(stack) > 0 {
		visited++
		// 指令条数是访问次数的上界：超过它说明结构有环，直接放弃跳过。
		if visited > len(p.instrs) {
			return nil, 0, 0, false
		}
		pc := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if pc < 0 || int(pc) >= len(p.instrs) {
			return nil, 0, 0, false
		}
		instr := p.instrs[pc]
		if instr.op == confirmOpSplit {
			stack = append(stack, instr.next, instr.index)
			continue
		}
		var value []byte
		switch instr.op {
		case confirmOpLiteral:
			packed := uint32(instr.index)
			size := int(packed >> 24)
			if size <= 0 || size > confirmPackedLiteralLimit {
				return nil, 0, 0, false
			}
			value = make([]byte, size)
			for index := range size {
				value[index] = byte(packed >> uint(16-8*index))
			}
		case confirmOpLongLiteral:
			if instr.index < 0 || int(instr.index) >= len(p.literals) {
				return nil, 0, 0, false
			}
			value = p.literals[instr.index]
		default:
			// 入口链上出现文字比较以外的指令（集合、断言、重复等）时分支
			// 语义不再只由候选文字决定，不能整体跳过。
			return nil, 0, 0, false
		}
		if len(value) == 0 {
			return nil, 0, 0, false
		}
		if length < 0 {
			length, next = len(value), instr.next
		} else if len(value) != length || instr.next != next {
			return nil, 0, 0, false
		}
		leaves[string(value)] = struct{}{}
	}
	if length <= 0 || next < 0 || int(next) >= len(p.instrs) {
		return nil, 0, 0, false
	}
	return leaves, length, next, true
}

// afterLeadingLiteral 返回跳过入口文字后的入口指令。确认程序的入口必须
// 恰好是同一段文字，否则说明编译结果与推导出的文字不一致，不能跳过。
func (p *confirmProgram) afterLeadingLiteral(literal []byte) (int32, bool) {
	if p == nil || p.entry < 0 || int(p.entry) >= len(p.instrs) {
		return 0, false
	}
	instr := p.instrs[p.entry]
	switch instr.op {
	case confirmOpLiteral:
		if len(literal) > confirmPackedLiteralLimit || packConfirmLiteral(literal) != instr.index {
			return 0, false
		}
	case confirmOpLongLiteral:
		if !bytes.Equal(p.literals[instr.index], literal) {
			return 0, false
		}
	default:
		return 0, false
	}
	return instr.next, true
}

func compileConfirmProgram(root parser.Node, flags CompileFlag) *confirmProgram {
	if root == nil || flags&(CompileUTF8|CompileUCP) != 0 {
		return nil
	}
	compiler := &confirmCompiler{flags: flags, ok: true}
	entry := compiler.compile(root, compiler.emit(confirmInstr{op: confirmOpMatch}))
	if !compiler.ok || entry < 0 || len(compiler.instrs) > confirmMaxInstructions {
		return nil
	}
	program := &confirmProgram{
		instrs:    compiler.instrs,
		repeats:   compiler.repeats,
		literals:  compiler.literals,
		sets:      compiler.sets,
		entry:     entry,
		nonGreedy: firstRepeatPreference(root),
		bounded:   !compiler.unbounded,
	}
	// 入口是集合重复、后继就是接受状态时整条规则只有一个字符类重复，确认求值
	// 可以完全绕开解释器；入口指令恒非 0（指令 0 是接受状态），因此这个布尔
	// 标志不会与「入口就是接受」的纯文字程序混淆。
	if instr := &program.instrs[program.entry]; instr.op == confirmOpSetRepeat && instr.next >= 0 && program.instrs[instr.next].op == confirmOpMatch {
		program.setRepeatAccept = true
	}
	program.remain = buildConfirmRemain(program)
	return program
}

// buildConfirmRemain 计算每条指令到接受状态的最大可消费字节数。
//
// 只在无界程序之外调用：没有无上界重复时指令图是前向无环图，简单的
// 迭代松弛在最多指令数轮内收敛，无需显式拓扑排序。
func buildConfirmRemain(p *confirmProgram) []int {
	if p == nil || p.unboundedRemain() {
		return nil
	}
	size := len(p.instrs)
	if size == 0 {
		return nil
	}
	remain := make([]int, size)
	for range size {
		changed := false
		for index := range remain {
			next := confirmRemainAt(p, int32(index), remain)
			if next > remain[index] {
				remain[index] = next
				changed = true
			}
		}
		if !changed {
			break
		}
	}
	return remain
}

// unboundedRemain 报告程序是否必须禁用按字节数剪枝。
func (p *confirmProgram) unboundedRemain() bool {
	return p == nil || !p.bounded
}

// confirmRemainAt 是 buildConfirmRemain 的单指令松弛步骤，只使用已求出的
// 后继上界，返回该指令可达的最大消费字节数。
func confirmRemainAt(p *confirmProgram, pc int32, remain []int) int {
	instr := &p.instrs[pc]
	after := func(next int32, extra int) int {
		if next < 0 || int(next) >= len(remain) {
			return confirmRemainUnbounded
		}
		value := remain[next]
		if value >= confirmRemainUnbounded {
			return confirmRemainUnbounded
		}
		value += extra
		if value > confirmRemainUnbounded {
			return confirmRemainUnbounded
		}
		return value
	}
	switch instr.op {
	case confirmOpLiteral:
		return after(instr.next, int(uint32(instr.index)>>24))
	case confirmOpLongLiteral:
		return after(instr.next, len(p.literals[instr.index]))
	case confirmOpSet, confirmOpAny:
		return after(instr.next, 1)
	case confirmOpAssert:
		return after(instr.next, 0)
	case confirmOpSplit:
		left := after(instr.index, 0)
		if right := after(instr.next, 0); right > left {
			return right
		}
		return left
	case confirmOpSetRepeat:
		repeat := &p.repeats[instr.index]
		if repeat.max < 0 {
			return confirmRemainUnbounded
		}
		return after(instr.next, int(repeat.max))
	case confirmOpMatch:
		return 0
	default:
	}
	return confirmRemainUnbounded
}

// compile 生成“执行 node 后继续执行 next”的指令序列，返回入口指令下标。
func (c *confirmCompiler) compile(node parser.Node, next int32) int32 {
	if !c.ok {
		return -1
	}
	switch v := node.(type) {
	case parser.Literal:
		if len(v.Value) == 0 {
			return next
		}
		if len(v.Value) <= confirmPackedLiteralLimit {
			return c.emit(confirmInstr{op: confirmOpLiteral, next: next, index: packConfirmLiteral(v.Value)})
		}
		return c.emit(confirmInstr{op: confirmOpLongLiteral, next: next, index: c.addLiteral(v.Value)})
	case parser.Class:
		set, ok := c.setFromNode(v)
		if !ok {
			return c.fail()
		}
		return c.emit(confirmInstr{op: confirmOpSet, next: next, index: c.addSet(set)})
	case parser.Any:
		return c.emit(confirmInstr{op: confirmOpAny, next: next})
	case parser.Assertion:
		return c.emit(confirmInstr{op: confirmOpAssert, kind: v.Kind, next: next})
	case parser.Group:
		if v.Atomic || v.HasScopedFlags() {
			return c.fail()
		}
		return c.compile(v.Child, next)
	case parser.Sequence:
		entry := next
		for _, v0 := range slices.Backward(v.Elements) {
			entry = c.compile(v0, entry)
			if !c.ok {
				return -1
			}
		}
		return entry
	case parser.Alternation:
		if len(v.Options) == 0 {
			return c.fail()
		}
		entry := c.compile(v.Options[len(v.Options)-1], next)
		for index := len(v.Options) - 2; index >= 0; index-- {
			option := c.compile(v.Options[index], next)
			if !c.ok {
				return -1
			}
			entry = c.emit(confirmInstr{op: confirmOpSplit, next: option, index: entry})
		}
		return entry
	case parser.Repeat:
		return c.compileRepeat(v, next)
	case parser.ControlVerb:
		if strings.EqualFold(v.Name, "ACCEPT") {
			return c.emit(confirmInstr{op: confirmOpMatch})
		}
		return c.fail()
	default:
	}
	return c.fail()
}

func (c *confirmCompiler) compileRepeat(v parser.Repeat, next int32) int32 {
	if v.Max == 0 {
		return next
	}
	if v.Max > 0 && v.Max < v.Min {
		return c.fail()
	}
	if v.Max < 0 && v.Min > confirmMaxRepeat {
		return c.fail()
	}
	if v.Max > confirmMaxRepeat {
		return c.fail()
	}
	// 子节点恰好消费一个字节时使用集合重复指令，把整个重复压成一次
	// 表驱动的连续扫描；其余结构按有界展开或自尾循环处理。
	if set, ok := c.setFromNode(v.Child); ok && (v.Max < 0 || v.Max <= confirmMaxRepeat) {
		if v.Max < 0 {
			// 无上界集合重复在指令图上没有回边，但消费长度无上界，
			// 同样必须禁用按字节数剪枝。
			c.unbounded = true
		}
		final := next >= 0 && c.instrs[next].op == confirmOpMatch
		index := c.addRepeat(c.addSet(set), int32(v.Min), int32(v.Max), final)
		return c.emit(confirmInstr{op: confirmOpSetRepeat, index: index, next: next})
	}
	if v.Max < 0 {
		if minWidth, _, ok := parser.WidthRange(v.Child); !ok || minWidth < 1 {
			return c.fail()
		}
		c.unbounded = true
		split := c.reserve()
		body := c.compile(v.Child, split)
		if !c.ok {
			return -1
		}
		c.instrs[split] = confirmInstr{op: confirmOpSplit, next: body, index: next}
		entry := split
		for range v.Min {
			entry = c.compile(v.Child, entry)
			if !c.ok {
				return -1
			}
		}
		return entry
	}
	entry := next
	for range v.Max - v.Min {
		body := c.compile(v.Child, entry)
		if !c.ok {
			return -1
		}
		entry = c.emit(confirmInstr{op: confirmOpSplit, next: body, index: entry})
	}
	for range v.Min {
		entry = c.compile(v.Child, entry)
		if !c.ok {
			return -1
		}
	}
	return entry
}

// preferredEnd 执行确认程序，返回 record 需要的偏好结束偏移。ok 为 false
// 表示程序不适用（预算、回溯栈或结构超限），调用方必须回退通用确认路径。
func (p *confirmProgram) preferredEnd(data []byte, start int, flags CompileFlag, stack []confirmFrame) (int, bool) {
	if p.setRepeatAccept {
		instr := &p.instrs[p.entry]
		return setRepeatAcceptEnd(data, start, &p.sets[p.repeats[instr.index].set], &p.repeats[instr.index], p.nonGreedy)
	}
	return p.run(data, p.entry, start, flags, stack)
}

// setRepeatAcceptEnd 求「集合重复后立刻接受」形态的偏好结束偏移，语义与解释器
// 的 confirmOpSetRepeat + confirmOpMatch 完全一致：
//   - 连续段长度不足下限时无命中，返回 (-1, true)（ok 为 true 表示求值完成，
//     不是回退信号）；
//   - 贪婪取最大消费长度（受上界与数据末尾截断），非贪婪取最小消费长度。
//
// 实测 64 KiB 固定语料（12 条规则）上规则 7（`x{2,4}`，2,338 个候选）的确认
// 求值 14.2 → 10.6 µs、规则 11（`[[:alpha:]]{5,}`，3,341 个候选）26.2 → 21.4 µs
// （同一探针、5 轮取最小值）；整块扫描的确认阶段 74.3 → 61.8 µs。
func setRepeatAcceptEnd(data []byte, start int, set *confirmByteSet, repeat *confirmRepeat, nonGreedy bool) (int, bool) {
	// 起点越界时与解释器入口检查保持一致：不适用直通，交回调用方回退通用
	// 求值（返回 ok = false），避免在这里给出解释器不会给出的命中。
	if start < 0 || start > len(data) {
		return -1, false
	}
	limit := len(data) - start
	if limit < 0 {
		limit = 0
	}
	maxCount := int(repeat.max)
	if maxCount < 0 || maxCount > limit {
		maxCount = limit
	}
	count := 0
	for count < maxCount && set.match(data[start+count]) {
		count++
	}
	if count < int(repeat.min) {
		return -1, true
	}
	if nonGreedy {
		return start + int(repeat.min), true
	}
	return start + count, true
}

// run 从指定入口指令和位置执行确认程序。
//
// 失败统一跳到循环末尾的回溯点处理，避免每个指令都写一次失败标志；
// 短文字、常见断言和集合重复都在此就地判定，只有真正需要分支时才
// 保存回溯点。
func (p *confirmProgram) run(data []byte, entry int32, start int, flags CompileFlag, stack []confirmFrame) (int, bool) {
	if p == nil || len(p.instrs) == 0 || start < 0 || start > len(data) {
		return -1, false
	}
	if entry < 0 || int(entry) >= len(p.instrs) {
		return -1, false
	}
	if stack == nil {
		stack = make([]confirmFrame, confirmMaxStack)
	}
	if len(stack) > confirmMaxStack {
		stack = stack[:confirmMaxStack]
	}
	caseless := flags&CompileCaseless != 0
	dotAll := flags&CompileDotAll != 0
	unicodeWord := flags&(CompileUTF8|CompileUCP) != 0
	dataLen := len(data)
	pc := entry
	pos := start
	best := -1
	depth := 0
	maxEnd := !p.nonGreedy
	remain := p.remain
scan:
	for steps := 0; ; steps++ {
		if steps > confirmMaxSteps {
			return -1, false
		}
		instr := p.instrs[pc]
		switch instr.op {
		case confirmOpLiteral:
			packed := uint32(instr.index)
			length := int(packed >> 24)
			if pos+length > dataLen {
				goto backtrack
			}
			if !caseless {
				switch length {
				case 1:
					if data[pos] != byte(packed>>16) {
						goto backtrack
					}
				case 2:
					if data[pos] != byte(packed>>16) || data[pos+1] != byte(packed>>8) {
						goto backtrack
					}
				default:
					if data[pos] != byte(packed>>16) || data[pos+1] != byte(packed>>8) || data[pos+2] != byte(packed) {
						goto backtrack
					}
				}
			} else {
				for index := range length {
					if !equalByte(byte(packed>>uint(16-8*index)), data[pos+index], flags) {
						goto backtrack
					}
				}
			}
			pos += length
			pc = instr.next
		case confirmOpLongLiteral:
			literal := p.literals[instr.index]
			length := len(literal)
			if pos+length > dataLen {
				goto backtrack
			}
			if !caseless {
				if !bytes.Equal(data[pos:pos+length], literal) {
					goto backtrack
				}
			} else {
				for index := range length {
					if !equalByte(literal[index], data[pos+index], flags) {
						goto backtrack
					}
				}
			}
			pos += length
			pc = instr.next
		case confirmOpSet:
			if pos >= dataLen {
				goto backtrack
			}
			set := &p.sets[instr.index]
			value := data[pos]
			if set[value>>6]&(uint64(1)<<(value&63)) == 0 {
				goto backtrack
			}
			pos++
			pc = instr.next
		case confirmOpSetRepeat:
			repeat := &p.repeats[instr.index]
			limit := dataLen - pos
			maxCount := int(repeat.max)
			if maxCount < 0 || maxCount > limit {
				maxCount = limit
			}
			set := &p.sets[repeat.set]
			count := 0
			for count < maxCount {
				value := data[pos+count]
				if set[value>>6]&(uint64(1)<<(value&63)) == 0 {
					break
				}
				count++
			}
			minCount := int(repeat.min)
			if count < minCount {
				goto backtrack
			}
			proceed := count
			switch {
			case repeat.final:
				// 后继就是接受状态：本次重复的所有合法次数都是完整命中的
				// 结束位置，直接按偏好方向取端点，无需保存回溯点。
				if !maxEnd {
					proceed = minCount
				}
			case maxEnd:
				for shorter := count - 1; shorter >= minCount; shorter-- {
					if depth == len(stack) {
						return -1, false
					}
					stack[depth] = confirmFrame{pc: instr.next, pos: pos + shorter}
					depth++
				}
			default:
				for longer := minCount + 1; longer <= count; longer++ {
					if depth == len(stack) {
						return -1, false
					}
					stack[depth] = confirmFrame{pc: instr.next, pos: pos + longer}
					depth++
				}
				proceed = minCount
			}
			pos += proceed
			pc = instr.next
		case confirmOpAny:
			if pos >= dataLen || data[pos] == '\n' && !dotAll {
				goto backtrack
			}
			pos++
			pc = instr.next
		case confirmOpAssert:
			switch instr.kind {
			case parser.WordBoundary, parser.NonWordBoundary:
				if !unicodeWord {
					left := pos > 0 && isWord(data[pos-1])
					right := pos < dataLen && isWord(data[pos])
					if (left != right) != (instr.kind == parser.WordBoundary) {
						goto backtrack
					}
					break
				}
				if !assertionHolds(instr.kind, data, pos, flags) {
					goto backtrack
				}
			case parser.BeginAbsolute:
				if pos != 0 {
					goto backtrack
				}
			case parser.EndAbsolute:
				if pos != dataLen {
					goto backtrack
				}
			default:
				if !assertionHolds(instr.kind, data, pos, flags) {
					goto backtrack
				}
			}
			pc = instr.next
		case confirmOpSplit:
			if depth == len(stack) {
				return -1, false
			}
			stack[depth] = confirmFrame{pc: instr.index, pos: pos}
			depth++
			pc = instr.next
		case confirmOpMatch:
			if best < 0 || maxEnd && pos > best || !maxEnd && pos < best {
				best = pos
			}
			goto backtrack
		default:
			return -1, false
		}
		continue
	backtrack:
		// 已经找到命中后，剩余回溯点只要不可能改善偏好结束偏移就可以整段
		// 丢弃：有界程序用“该指令最多还能消费多少字节”判定，无界程序仅在
		// 非贪婪方向用“帧位置已不小于最优结束”判定。
		for depth > 0 {
			depth--
			frame := stack[depth]
			if best >= 0 {
				if !maxEnd {
					if frame.pos >= best {
						continue
					}
				} else if remain != nil {
					if limit := remain[frame.pc]; limit < confirmRemainUnbounded && frame.pos+limit <= best {
						continue
					}
				}
			}
			pc = frame.pc
			pos = frame.pos
			continue scan
		}
		return best, true
	}
}
