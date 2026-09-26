// Package scankit 提供跨平台的 Block 多规则扫描器。
package scankit

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"math/bits"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"unicode"
	"unicode/utf8"
	"unsafe"

	"github.com/smartwalle/scankit/internal/combination"
	"github.com/smartwalle/scankit/internal/compiler"
	"github.com/smartwalle/scankit/internal/dispatch"
	"github.com/smartwalle/scankit/internal/engine"
	"github.com/smartwalle/scankit/internal/fdr"
	"github.com/smartwalle/scankit/internal/fuzzy"
	"github.com/smartwalle/scankit/internal/hwlm"
	"github.com/smartwalle/scankit/internal/hwlm/noodle"
	"github.com/smartwalle/scankit/internal/hwlm/teddy"
	"github.com/smartwalle/scankit/internal/nfa"
	"github.com/smartwalle/scankit/internal/parser"
	"github.com/smartwalle/scankit/internal/prefilter"
	"github.com/smartwalle/scankit/internal/repeat"
	"github.com/smartwalle/scankit/internal/report"
	"github.com/smartwalle/scankit/internal/rose"
	"github.com/smartwalle/scankit/internal/scratch"
	"github.com/smartwalle/scankit/internal/simd"
	"github.com/smartwalle/scankit/internal/smallblock"
	"github.com/smartwalle/scankit/internal/smallwrite"
)

// Match 是一条匹配结果：规则编号与左闭右开区间 [From, To)。
type Match struct {
	Id   uint32
	From uint64
	To   uint64
}

// Scanner 是内存中的不可变编译规则执行计划，可安全地并发扫描，并管理所需的可复用扫描上下文。
type Scanner struct {
	contextPool *sync.Pool
	// retainedContext 是最近一次扫描结束后留在 Scanner 上的扫描上下文槽位。
	// sync.Pool 会在任意一次 GC 时被清空，工作集较大的扫描因此会反复重新
	// 增长同一批缓冲；这里额外保留一个槽位，让顺序扫描在 GC 之后仍能直接
	// 复用上次的容量。并发扫描时多出来的上下文照旧回到 contextPool。
	retainedContext *retainedContextSlot
	roseStatePool   *sync.Pool
	roseSinglePool  *sync.Pool
	// canUseRoseInScan 缓存 canUseRoseInScan 的结果，避免每次 Scan 重复遍历。
	canUseRoseInScan bool
	// roseDirectLiteralIndex 表示 Rose 直通路径可以直接消费 Scanner 自己的候选
	// 索引，而不必调用 rose.Program 内部另建的 matcher。推导见 newScanner。
	roseDirectLiteralIndex bool
	rules                  []compiledRule
	ruleIndex              map[uint32]int
	ruleOrder              map[uint32]int
	hasCombo               bool
	literalFind            func([]byte) []literalCandidate
	literalFindInto        func([]byte, []literalCandidate) []literalCandidate
	literalKind            string
	candidateIDs           map[uint32]struct{}
	literalIDs             map[uint32]struct{}
	// indexedRules/exactRules 是 candidateIDs 与 literalIDs 的下标视图，
	// 供确认热路径避免每个候选起点都做一次 map 查询。
	indexedRules []bool
	exactRules   []bool
	// simpleReports 表示本次扫描不需要报告管理器参与：没有组合规则，
	// 也没有依赖 SOM/单次/静默语义的规则，可直接追加匹配结果。
	simpleReports bool
	// requiredLiterals 是必须文字候选索引：每个文字对应一组 (规则, 偏移窗口)。
	// 任意匹配至少命中其中一个文字，因此只需要在这些命中位置派生的起点上确认。
	requiredLiterals []requiredLiteral
	requiredFindInto func([]byte, []requiredHit) []requiredHit
	// guardRunCovered 标记哪些规则的候选起点由前缀字节约束枚举，
	// guardRunGroups 按字节集合合并这些约束，使同集合的规则共享一次线性扫描。
	guardRunCovered []bool
	guardRunGroups  []guardRunGroup
	// requiredCovered 标记规则是否由候选文字索引覆盖；覆盖的规则只需在
	// 候选起点确认，不再参与整块后端扫描。
	requiredCovered []bool
	// runHeadWindows 标记哪些规则的候选窗口启用了段首过滤（见 denseRunHead）。
	// 这类规则的段内起点不再登记，因此确认阶段一旦被重叠抑制必须把候选推迟到
	// 水位线位置重新登记，否则水位线之后的段内起点会漏报（见
	// retryCollapsedCandidate）。
	runHeadWindows []bool
	// anchorEntries 是锚定在输入首尾的规则候选来源：这些规则只有一个可行起点，
	// 直接按锚点推导，无需候选文字命中。anchorCovered 标记这些规则，使它们同样
	// 免于整块后端扫描；与 requiredCovered 分开是因为该来源没有校验规则入口
	// 文字，确认程序不能跳过入口指令。
	anchorEntries []anchorEntry
	anchorCovered []bool
	// roseDirect 把 Rose 直通路径需要的报告信息按角色编号固化下来：角色编号到
	// 报告编号、命中长度与报告语义的映射在构造期解析一次，避免每个候选都做
	// 两次 map 查找并复制 compiledRule。roseDirectSingle 表示是否需要单次命中
	// 去重表，全为普通规则时可以整块跳过该表的取用与清空。
	roseDirect       map[uint32]roseDirectEntry
	roseDirectSingle bool
	// fastLiteral/backendOnly 只依赖编译结果，构造阶段固化以避免每次扫描重复推导。
	fastLiteral bool
	backendOnly bool
	// confirmAllRules 是候选路径不可用时需要逐起点确认的规则；
	// confirmUncoveredRules 是候选路径生效后仍未被索引覆盖的规则。
	confirmAllRules       []int
	confirmUncoveredRules []int
	// confirmDFAStarts 是按规则下标对齐的确认表续跑点：规则既有确认表、又有
	// 入口文字（confirmSkip > 0）时，构造期把入口文字在确认表上预走一遍，扫描期
	// 直接从文字之后续跑（见 confirmDFAStart）。无确认表或无法安全续跑的规则为
	// confirmDFAStart.ok == false。整表为空表示没有任何规则可用该捷径。
	confirmDFAStarts []confirmDFAStart
	// startBytes/startBytesAll 是按首字节分组的逐起点确认索引，分别对应
	// confirmUncoveredRules 与 confirmAllRules。
	startBytes    *startByteIndex
	startBytesAll *startByteIndex
	// hasCollapse 表示至少一条规则具备可复用的确认窗口，扫描期需要为
	// collapseWindows 预留按规则下标的缓存。
	hasCollapse   bool
	usage         compiler.Usage
	validationErr error
	rosePlan      *rose.Program
}

// retainedContextSlot 是 Scanner 上不参与 GC 清理的扫描上下文槽位。
//
// 槽位之所以用指针持有、而不是把 atomic.Pointer 直接内嵌进 Scanner，是为了让
// Scanner 保持可拷贝：Scanner 的其余字段都是只读执行计划，若干测试会浅拷贝它
// 构造对照实现（例如关闭候选窗口复用或前缀约束后再比对结果）。槽位本身只是
// 一份缓存，浅拷贝共享同一槽位不影响语义——取出是原子交换，任何时刻只有一个
// 持有者。
type retainedContextSlot struct {
	ctx atomic.Pointer[scanContext]
}

type scanContext struct {
	Scratch *scratch.Scratch
	Reports *report.Manager

	blockedUntil      []int
	fired             []bool
	comboTriggers     []report.Event
	literalEnds       map[int]map[uint32]int
	prefilterStarts   map[uint32]map[int]struct{}
	literalCandidates []literalCandidate
	// ruleMatchBuf 复用规则 NFA/Repeat 路径的结束偏移缓冲，
	// 避免每个起点都重新分配临时切片。
	ruleMatchBuf []int
	// requiredHits 与 requiredStarts 复用候选文字命中及其派生的确认起点，
	// 避免每次扫描重新分配候选切片。
	requiredHits []requiredHit
	// 候选起点线性排序所需的复用缓冲：第一轮按规则下标、第二轮按起点做稳定
	// 计数排序，避免在候选密集时付比较排序的对数因子。
	requiredSortScratch []uint64
	requiredStartCounts []uint32
	requiredStarts      []uint64
	// retryStarts 复用收敛窗口被重叠抑制后推迟到水位线的补登候选队列，按
	// (起点, 规则) 升序使用，长度有界于规则数（见 retryCollapsedCandidate）。
	retryStarts []uint64
	// chainArena 是确认快路径的单次工作区，跨候选起点复用同一块内存。
	chainArena byteChainArena
	// confirmStack 是确认程序的回溯栈，跨候选起点复用。
	confirmStack []confirmFrame
	// directMatches 复用 simpleReports 模式下的直接结果切片。
	directMatches []Match
	// nodeEval 是本次扫描 AST 求值的可复用工作区：既缓存 subject 的 UTF-8
	// 合法性判定（UTF8 模式字面量命中要求整个 subject 合法，逐起点重复全量
	// 校验会退化成 O(n²)），也承载逐节点结束偏移滑窗的切分内存。
	nodeEval nodeEval
	// collapseWindows 按规则下标缓存「起点无关窗口」：窗口内的候选起点共享
	// 同一次确认程序求值结果，避免为同一次文字命中的每个起点重复执行确认。
	collapseWindows []collapseWindow
}

// collapseWindow 是一条规则最近一次确认程序求值覆盖的起点区间及其结果。
// to < from 表示缓存无效。
type collapseWindow struct {
	from int
	to   int
	end  int
}

// guardRun 是由前缀字节约束直接派生的候选起点来源：约束在 [offset, offset+length)
// 上是同一个字节集合，满足该窗口的全部位置构成规则的候选起点集合。
type guardRun struct {
	ruleIndex int
	set       [4]uint64
	offset    int
	length    int
	// guard 是规则的完整前缀字节约束。等值窗口之外的约束位置也要校验时，
	// 候选起点在被展开成候选之前先过一次查表，可以挡掉大量必然失败的起点。
	guard *prefixGuard
	// prefixCovered 表示约束窗口本身已经覆盖全部前缀字节集合。此时窗口扫描
	// 已经保证前缀成立，展开时只剩首尾断言折算出的相邻字节约束需要校验。
	prefixCovered bool
	// leftmostOnly 表示规则恰好是约束集合的无上界贪婪重复（见 guardRunRepeat），
	// 同一条连续段内只有段首起点需要登记：段首必命中且贪婪重复消费到段尾，段内
	// 更靠右的起点都会被同一规则的重叠抑制挡住。
	leftmostOnly bool
	// stride 大于 0 表示规则恰好是约束集合的有上界贪婪重复且上界等于该值（见
	// guardRunRepeat）：连续段内被报告的起点只可能是 段首 + k*stride，登记整段
	// 只会让必然被重叠抑制的起点也走一遍排序与候选循环。
	stride int
}

// guardRunGroup 把共享同一字节集合的约束合并成一次线性扫描。
type guardRunGroup struct {
	set    [4]uint64
	tables simd.ByteSetTables
	// runs 按窗口长度升序排列：连续段短于某条规则的窗口时，后面的规则窗口
	// 只会更长，展开可以直接结束。
	runs []guardRun
	// minLength 是组内最短的约束窗口长度（runs[0].length）。连续段长度不足它
	// 时整组都不可能成立，扫描期直接跳过该段，省掉一次展开调用。日志语料里
	// 绝大多数连续段远短于窗口长度（时间戳被 '-'、':' 切成 2~4 位数字段），
	// 「先比长度再展开」把这一部分开销从每次调用压到一次整数比较。
	minLength int
}

// member 判断字节是否属于该组的约束集合。只有不足一个宽窗口的尾部和跨窗口边界
// 需要逐字节判定，主体扫描走预编译的宽窗口掩码。
func (group *guardRunGroup) member(value byte) bool {
	return group.set[value>>6]&(uint64(1)<<(value&63)) != 0
}

// guardRunMinLength 是可派生候选起点的最短窗口长度。更短的约束在逐起点确认
// 中只需一两次字节判定，单独做线性扫描并不划算。
const guardRunMinLength = 4

// guardRunShortLiteralBytes 是判定候选文字"过短"的字节数上限。单字节或双字节
// 候选文字在混合字母数字语料上的命中密度远高于同长度的字节集合窗口，此时前缀
// 约束枚举能显著压缩候选。
const guardRunShortLiteralBytes = 2

// guardRunPromoteWindow 是"候选文字过短"的规则改用约束枚举所需的最短窗口。
// 窗口越长，约束窗口的命中密度下降越快，才值得放弃候选文字索引。
const guardRunPromoteWindow = 8

// guardRunPromoteCardinality 是"候选文字过短"时改用约束枚举允许的窗口集合规模。
// 集合越大，窗口在自然文本里越容易连续命中，枚举出的候选反而多于候选文字命中；
// 超过该规模时改看窗口长度。
const guardRunPromoteCardinality = 24

// guardRunPromoteLongWindow 是宽集合仍改用约束枚举所需的最短窗口。窗口内的每个
// 字节都必须落在集合内，命中密度随长度指数下降，长度足够时即使集合很宽也比逐字节
// 候选文字稀疏：`[A-Za-z0-9+/]{20,}` 要连续 20 个 base64 字符才算候选。
const guardRunPromoteLongWindow = 16

// 扫描期缓冲的池内保留策略：容量不超过绝对上限即留在池中跨扫描复用。
// 缓冲容量由输入内容决定，释放一次就得在下一次同等规模的扫描里重新 grow，
// 累计分配可达数据量的数倍（§21.6）。因此判据只看绝对上限，不随本次输入长度
// 缩放：按输入长度缩放会让"大输入 → 小输入 → 大输入"的交替负载每次都重新 grow，
// 实测单次大扫描多分配 147 MB（§26.2）。绝对上限兜住异常大的缓冲，
// sync.Pool 又会在 GC 时清空池，长期驻留由两者共同约束。
const (
	requiredStartsRetainLimit = 1 << 22
	literalMatchesRetainLimit = 1 << 22
	requiredHitsRetainLimit   = 1 << 22
	directMatchesRetainLimit  = 1 << 22
)

// retainBuffer 是保留判据本身：容量不超过绝对上限即保留。
func retainBuffer(capacity, limit int) bool {
	return capacity <= limit
}

// retainRequiredStarts 判断候选起点缓冲能否留在池中跨扫描复用。
func retainRequiredStarts(capacity int) bool {
	return retainBuffer(capacity, requiredStartsRetainLimit)
}

// retainLiteralMatches 判断文字匹配缓冲能否留在池中跨扫描复用。
func retainLiteralMatches(capacity int) bool {
	return retainBuffer(capacity, literalMatchesRetainLimit)
}

// retainRequiredHits 判断候选文字命中缓冲能否留在池中跨扫描复用。
func retainRequiredHits(capacity int) bool {
	return retainBuffer(capacity, requiredHitsRetainLimit)
}

// retainDirectMatches 判断直接结果缓冲能否留在池中跨扫描复用。
func retainDirectMatches(capacity int) bool {
	return retainBuffer(capacity, directMatchesRetainLimit)
}

// requiredHit 是一次候选文字命中：编号来自 requiredLiterals 的序号加一。
type requiredHit struct {
	slot int
	pos  int
}

// requiredStartKey 把需要确认的 (起点, 规则下标) 组合打包进一个机器字。
//
// 排序是候选展开后的固定开销，按机器字排序可以直接用 slices.Sort 的有序
// 快路径，避免 16 字节结构体在比较器回调中来回搬运。起点放在高位，因此
// 排序结果仍按起点升序、同起点按规则下标升序，与去重语义一致。
func requiredStartKey(start, rule int) uint64 {
	return uint64(uint32(start))<<32 | uint64(uint32(rule))
}

// requiredStartParts 拆回打包前的起点与规则下标。
func requiredStartParts(key uint64) (int, int) {
	return int(key >> 32), int(key & 0xFFFFFFFF)
}

// requiredEntry 把候选文字与具体规则及其偏移窗口关联起来。
type requiredEntry struct {
	ruleID    uint32
	ruleIndex int
	min       int
	max       int
	back      []byte
	// guard 是规则的前缀字节约束，skip 表示该规则已由整块后端扫描产出结果。
	// 两者都在构造期固化，候选展开热路径直接读取，避免逐候选回查规则表。
	guard *prefixGuard
	skip  bool
	// collapseWindow 表示该规则的候选窗口已被证明是一组确认结果相同的起点
	// （见 collapseHead）：窗口内任意起点都会让入口集合重复消费到同一个文字
	// 命中位置，之后的确认程序与起点无关。此时窗口只需登记最左起点，其余起点
	// 由确认阶段的推迟逻辑补齐（见 retryCollapsedCandidate）。
	collapseWindow bool
	// runHead 非空时表示规则以「该字节集合的无上界贪婪重复」开头（见
	// denseRunHead）：同一连续段内的起点共享同一个确认结果，窗口展开时只需登记
	// 段首起点，段内更靠右的起点必然被同一规则的重叠抑制挡住。
	runHead [4]uint64
}

// requiredLiteral 是一个候选文字及共享它的全部规则。
type requiredLiteral struct {
	entries []requiredEntry
}

type literalCandidate struct {
	ID       uint32
	From, To int
}

type compiledRule struct {
	id         uint32
	root       parser.Node
	comb       parser.Node
	flags      CompileFlag
	ext        *ExpressionExt
	info       compiler.ExpressionInfo
	program    *engine.Program
	prefilter  []byte
	smallBlock *smallblock.Program
	smallWrite *smallwrite.Program
	repeat     *repeat.Program
	nfaEngine  *nfa.Engine
	// backendEligible 缓存 backendEligible 函数的结果，
	// 避免每个起点重复遍历 AST。
	backendEligible bool
	// requiresEndOfData 缓存 requiresEndOfData 的结果。
	requiresEndOfData bool
	// containsAny 缓存 containsAny 的结果。
	containsAny bool
	// nonGreedy 缓存 firstRepeatPreference 的结果，表示规则是否偏好非贪婪。
	nonGreedy bool
	// hasCapture 缓存 containsCaptureSemantics 的结果，确认快路径据此判断
	// 能否跳过捕获语义求值。
	hasCapture bool
	// hasBackref 缓存 containsBackreference 的结果。
	hasBackref bool
	// hasConditional 缓存 containsConditional 的结果。
	hasConditional bool
	// eodReports 缓存后端是否携带只在数据末尾触发的报告，
	// 避免每个起点重新遍历状态表。
	eodReports bool
	// required 是编译期推导出的必须文字集合，用于候选起点驱动扫描。
	required []prefilter.Variant
	// guard 是匹配起点之后必然满足的逐字节集合约束，用于在确认程序之前
	// 直接丢弃不可能命中的候选起点。
	guard *prefixGuard
	// confirm 是候选起点确认程序，nil 表示规则必须走通用确认路径。
	confirm *confirmProgram
	// confirmDFA 是断言敏感的确定性确认表，非空时优先于 confirm 使用。
	// 编译期为「断言只落在匹配末尾」的图构建（见 confirm_dfa.go）。
	confirmDFA *confirmDFA
	// confirmEntry 是跳过入口文字后的程序入口，confirmSkip 是跳过的文字长度。
	// confirmSkip 为 0 时从规则入口正常确认。
	//
	// confirmEntry 为 confirmExactEntry 时表示跳过入口文字后确认程序立刻命中：
	// 候选索引已证明起点处的入口文字成立，且入口之后没有其它消费与断言，匹配
	// 结束偏移恒为 start+confirmSkip，不必再进解释器（`needle`、`ab|cd` 这类
	// 纯文字规则）。用哨兵值而不是新增字段：compiledRule 每增大 8 字节，逐规则
	// 循环的缓存足迹就变化一次，实测在 Rules100 零命中基准上等同 +4%。
	confirmEntry int32
	confirmSkip  int
	// collapseHead 非空表示确认程序入口是一条与候选窗口一一对应的集合重复。
	// 此时同一次文字命中摊开的全部候选起点会让重复停在同一位置，重复之后的
	// 程序与起点无关，确认结果可以在窗口内复用（见 collapseHead）。
	collapseHead *collapseHead
}

// collapseHead 描述确认程序入口处的集合重复：重复集合等于候选窗口左侧的回退
// 集合、上下界等于窗口上下界，且入口文字首字节不属于该集合。满足这些条件时，
// 从窗口内任意起点出发，重复都会消费到同一个非集合字节（入口文字命中位置），
// 之后的程序完全相同，因此同一次文字命中派生出的候选起点共享同一个确认结果。
type collapseHead struct {
	set *confirmByteSet
	min int
	max int
}

func newScanner(rules []compiledRule) *Scanner {
	copyRules := append([]compiledRule(nil), rules...)
	// dfaStarts 惰性分配：只有确实存在「确认表 + 入口文字」的规则时才占用内存。
	var dfaStarts []confirmDFAStart
	// 预先计算每条规则的 AST 派生属性，避免扫描时重复遍历规则树。
	for i := range copyRules {
		copyRules[i].containsAny = containsAny(copyRules[i].root)
		copyRules[i].requiresEndOfData = requiresEndOfData(copyRules[i].root)
		copyRules[i].backendEligible = backendEligible(copyRules[i])
		copyRules[i].nonGreedy = firstRepeatPreference(copyRules[i].root)
		copyRules[i].hasCapture = containsCaptureSemantics(copyRules[i].root)
		copyRules[i].hasBackref = containsBackreference(copyRules[i].root)
		copyRules[i].hasConditional = containsConditional(copyRules[i].root)
		copyRules[i].eodReports = copyRules[i].program != nil && copyRules[i].program.HasEODReports()
		if copyRules[i].root != nil && copyRules[i].comb == nil {
			if required, ok := prefilter.FromAST(copyRules[i].root); ok {
				copyRules[i].required = required.Variants
			}
			copyRules[i].guard = newPrefixGuard(copyRules[i])
			copyRules[i].confirm = compileConfirmProgram(copyRules[i].root, copyRules[i].flags)
			if copyRules[i].confirm != nil {
				if entry, skip, ok := leadingLiteralSkip(&copyRules[i]); ok {
					if instrs := copyRules[i].confirm.instrs; entry >= 0 && int(entry) < len(instrs) && instrs[entry].op == confirmOpMatch {
						// 入口之后立刻命中：确认结果与解释器无关。
						entry = confirmExactEntry
					}
					copyRules[i].confirmEntry = entry
					copyRules[i].confirmSkip = skip
					// 入口文字是单条文字时，把它在确认表上预走一遍：扫描期可以
					// 直接从文字之后续跑，省掉解释器对入口后长尾程序的逐指令
					// 求值，也不必像从规则起点出发那样重走入口文字。入口是一组
					// 等长文字分支（`ab|cd`）时无法用单条文字预走，保持解释器。
					if skip > 0 && entry != confirmExactEntry && copyRules[i].confirmDFA != nil {
						if literal := leadingLiteral(copyRules[i].root); len(literal) == skip {
							if start, ok := copyRules[i].confirmDFA.stridePrefix(literal); ok {
								if dfaStarts == nil {
									dfaStarts = make([]confirmDFAStart, len(copyRules))
								}
								dfaStarts[i] = start
							}
						}
					}
				}
				copyRules[i].collapseHead = newCollapseHead(copyRules[i])
			}
		}
	}
	index := make(map[uint32]int, len(copyRules))
	order := make(map[uint32]int, len(copyRules))
	candidateIDs := make(map[uint32]struct{})
	literalIDs := make(map[uint32]struct{})
	literals := make([]hwlm.Literal, 0)
	hasCombo := false
	for i, rule := range copyRules {
		index[rule.id] = i
		order[rule.id] = i
		hasCombo = hasCombo || rule.comb != nil
		if rule.ext != nil || parser.HasScopedFlags(rule.root) || rule.flags&(CompileUTF8|CompileUCP|CompileMultiline|CompileDotAll) != 0 {
			continue
		}
		candidate := rule.prefilter
		if len(candidate) == 0 {
			if literal, ok := literalPattern(rule.root); ok {
				candidate = literal
			}
		}
		if len(candidate) == 0 {
			continue
		}
		candidateIDs[rule.id] = struct{}{}
		literals = append(literals, hwlm.Literal{ID: rule.id, Value: append([]byte(nil), candidate...), CaseInsensitive: rule.flags&CompileCaseless != 0})
		if rule.flags&CompileCaseless == 0 {
			if literal, exact := literalPattern(rule.root); exact && bytes.Equal(candidate, literal) && (rule.smallWrite != nil || rule.smallBlock != nil) {
				literalIDs[rule.id] = struct{}{}
			}
		}
	}
	indexedRules := make([]bool, len(copyRules))
	exactRules := make([]bool, len(copyRules))
	for i, rule := range copyRules {
		if _, ok := candidateIDs[rule.id]; ok {
			indexedRules[i] = true
		}
		if _, ok := literalIDs[rule.id]; ok {
			exactRules[i] = true
		}
	}
	simpleReports := !hasCombo
	if simpleReports {
		for _, rule := range copyRules {
			if rule.flags&(CompileQuiet|CompileSingleMatch|CompileSOMLeftmost) != 0 {
				simpleReports = false
				break
			}
		}
	}
	scanner := &Scanner{rules: copyRules, ruleIndex: index, ruleOrder: order, hasCombo: hasCombo, candidateIDs: candidateIDs, literalIDs: literalIDs, indexedRules: indexedRules, exactRules: exactRules, simpleReports: simpleReports, retainedContext: &retainedContextSlot{}, contextPool: &sync.Pool{New: func() any { return newScanContext() }}, roseStatePool: &sync.Pool{New: func() any { return new([]rose.State) }}, roseSinglePool: &sync.Pool{New: func() any { return new(map[uint32]struct{}) }}}
	if len(literals) > 1 {
		scanner.literalKind, scanner.literalFind, scanner.literalFindInto = newLiteralCandidateFinder(literals)
	}
	// 无法提取必须文字的规则本来只能逐起点确认；当前缀字节约束能枚举起点时
	// 改由约束驱动，省掉整块逐起点循环。改由约束枚举的规则不再登记候选文字，
	// 避免同一规则同时走两条候选来源、在同一批命中上重复展开窗口。
	for i := range copyRules {
		scanner.hasCollapse = scanner.hasCollapse || copyRules[i].collapseHead != nil
	}
	// 约束枚举依赖宽窗口掩码：原生内核下它比候选文字索引更快，标量参考实现下
	// 反而比 memchr 慢数倍（见 simd.HasNativeWideMask）。集合自重复规则据此在
	// 两条候选来源之间二选一，两条路径都产出等价候选集合，只影响开销。
	scanner.guardRunCovered = guardRunCandidates(copyRules, simd.HasNativeWideMask(dispatch.DefaultBackend()))
	scanner.runHeadWindows = make([]bool, len(copyRules))
	scanner.buildRequiredIndex(copyRules, scanner.guardRunCovered)
	scanner.fastLiteral = !scanner.hasCombo && len(copyRules) > 1 &&
		len(candidateIDs) == len(copyRules) && len(literalIDs) == len(copyRules)
	if scanner.fastLiteral {
		for _, rule := range copyRules {
			if rule.flags&(CompileQuiet|CompileSingleMatch) != 0 {
				scanner.fastLiteral = false
				break
			}
		}
	}
	scanner.backendOnly = !scanner.hasCombo && !scanner.fastLiteral
	scanner.confirmDFAStarts = dfaStarts
	scanner.buildConfirmRuleLists()
	scanner.markRequiredEntrySkips()
	scanner.startBytes = newStartByteIndex(copyRules, scanner.confirmUncoveredRules)
	if len(scanner.confirmAllRules) != len(scanner.confirmUncoveredRules) {
		scanner.startBytesAll = newStartByteIndex(copyRules, scanner.confirmAllRules)
	}

	// Rose 角色图与文字候选索引随规则计划不可变，构造阶段完成一次，
	// 扫描时直接复用，避免每个 Block 重建角色、指令和自动机。
	scanner.rosePlan = scanner.buildRoseProgram()
	scanner.buildRoseDirectIndex()
	scanner.canUseRoseInScan = scanner.computeCanUseRoseInScan()
	// Rose 直通场景要求全部规则都是单条纯文字，而 buildRoseProgram 只在「同一规则
	// 有第二个及之后的文字分支」时才另分配角色编号，因此该场景下角色编号恒等于
	// 规则编号。若再满足每条规则的候选文字就是完整文字（而非更短前缀），Scanner
	// 的候选索引与 Rose 角色 matcher 给出完全相同的候选集合，可以直接复用，
	// 省掉 rose 包内另建一份 fdr.Matcher 的构建与查询开销。
	scanner.roseDirectLiteralIndex = scanner.canUseRoseInScan &&
		len(candidateIDs) == len(scanner.rules) && len(literalIDs) == len(scanner.rules)
	// 编译结果不可变，计划校验只需在构造时执行一次；扫描热路径复用该结论。
	scanner.validationErr = scanner.validate()
	return scanner
}

// requiredIndexEligible 判断规则能否安全进入必须文字候选索引。
// UTF-8/UCP 会改变文字或字符类的匹配语义；作用域修饰符只有在整条规则统一
// 忽略大小写时才等价于 ASCII 折叠，其余情况一律保留完整确认路径。
func requiredIndexEligible(rule compiledRule) bool {
	if len(rule.required) == 0 {
		return false
	}
	if rule.flags&(CompileUTF8|CompileUCP) != 0 {
		return false
	}
	// 模糊匹配允许匹配文字本身发生变化，精确文字候选会漏报。
	if rule.ext != nil && rule.ext.Flags&(ExtFlagEditDistance|ExtFlagHammingDistance) != 0 {
		return false
	}
	if ruleCaseless(rule) {
		// ASCII 折叠无法覆盖 Unicode 折叠；字符类展开出的候选在忽略大小写时
		// 需要重新展开；无上界窗口依赖的左侧字节集合同样需要折叠。三者都保留
		// 原确认路径，宁可退化为逐起点确认也不能漏报。
		for _, variant := range rule.required {
			if variant.Class || variant.MaxOffset < variant.MinOffset || !asciiBytes(variant.Value) {
				return false
			}
		}
		return true
	}
	return !parser.HasScopedFlags(rule.root)
}

// ruleCaseless 判断规则是否整体处于忽略大小写语义。忽略大小写既可能来自
// [CompileCaseless]，也可能来自模式开头的内联 (?i)，两者都需要识别。
func ruleCaseless(rule compiledRule) bool {
	if rule.flags&CompileCaseless != 0 {
		// 表达式级忽略大小写仍可能被内联 (?-i:...) 局部撤消，此时不再具备
		// 统一的 ASCII 折叠语义。
		return !parser.HasCaselessClear(rule.root)
	}
	return parser.UniformCaseless(rule.root)
}

// buildRequiredIndex 为可安全提取必须文字的规则建立共享候选索引。
//
// covered 标记哪些规则的候选起点由必须文字索引枚举：无法提取必须文字、或偏移
// 窗口无法约束的规则保留原确认路径，不会让整个索引失效。窗口上界为负代表偏移
// 无上界，此时必须携带 Back 字节集合才能把候选收缩到有限起点。
//
// 锚定在输入首尾的规则只有一个可行起点，单独记在 anchorEntries 里：它们既不
// 需要候选文字的全部命中，也不需要为窗口内每个起点展开确认，只要在唯一的那个
// 起点上跑一次完整确认即可。
func (scanner *Scanner) buildRequiredIndex(rules []compiledRule, guardDriven []bool) {
	byValue := make(map[string]int)
	literals := make([]hwlm.Literal, 0, 64)
	index := make([]requiredLiteral, 0, 64)
	covered := make([]bool, len(rules))
	anchorCovered := make([]bool, len(rules))
	var anchors []anchorEntry
	for ruleIndex := range rules {
		rule := rules[ruleIndex]
		if !requiredIndexEligible(rule) || !requiredWindowUsable(rule.required) {
			continue
		}
		if ruleIndex < len(guardDriven) && guardDriven[ruleIndex] {
			continue
		}
		if anchor, ok := ruleStartAnchor(rule); ok {
			anchorCovered[ruleIndex] = true
			anchors = append(anchors, anchorEntry{ruleIndex: ruleIndex, anchor: anchor})
			continue
		}
		caseless := ruleCaseless(rule)
		runHead := denseRunHead(&rule)
		for _, variant := range rule.required {
			// 忽略大小写与精确匹配的文字不能共用同一个候选：两者的候选匹配
			// 语义不同，混用会让精确规则多确认（可接受）或让忽略大小写的规则
			// 漏掉大小写变体（不可接受），因此按语义分开编号。
			var key string
			if caseless {
				key = "\x01" + string(variant.Value)
			} else {
				key = "\x00" + string(variant.Value)
			}
			position, ok := byValue[key]
			if !ok {
				position = len(index)
				byValue[key] = position
				index = append(index, requiredLiteral{})
				literals = append(literals, hwlm.Literal{ID: uint32(position + 1), Value: variant.Value, CaseInsensitive: caseless})
			}
			entry := requiredEntry{ruleID: rule.id, ruleIndex: ruleIndex, min: variant.MinOffset, max: variant.MaxOffset, back: variant.Back, guard: rule.guard}
			// 折叠窗口由 newCollapseHead 在确认程序入口证明：窗口内所有起点共享
			// 同一个集合重复，确认结果与起点无关，因此只登记最左起点。判据与
			// newCollapseHead 完全一致（单候选、guard 由回退集合蕴含），这里只
			// 复用结论，不重复推导。
			entry.collapseWindow = rule.collapseHead != nil
			// 段首过滤只在无上界窗口上成立：有上界时窗口下界会被上界裁剪到
			// 连续段内部，段首不在窗口里，被丢弃的段内起点没有任何候选可以
			// 退回补齐。回退集合必须覆盖重复集合，否则窗口左侧可能停在段内，
			// 段首同样不在窗口里。折叠窗口已给出更强结论（窗口内全部起点共享
			// 结果），不再叠加其余条件。
			if !entry.collapseWindow && entry.max < 0 && runHead != ([4]uint64{}) && backSetContains(variant.Back, runHead) {
				entry.runHead = runHead
				scanner.runHeadWindows[ruleIndex] = true
			}
			if guardImpliedByBack(entry.guard, variant.Back, variant.MinOffset) {
				entry.guard = nil
			}
			index[position].entries = append(index[position].entries, entry)
		}
		covered[ruleIndex] = true
	}
	scanner.requiredLiterals = index
	scanner.requiredCovered = covered
	scanner.anchorEntries = anchors
	scanner.anchorCovered = anchorCovered
	if len(literals) == 0 {
		scanner.requiredFindInto = nil
		return
	}
	scanner.requiredFindInto = newRequiredFinder(literals)
}

// requiredWindowUsable 判断偏移窗口能否在扫描期展开为有限起点集合。
func requiredWindowUsable(variants []prefilter.Variant) bool {
	for _, variant := range variants {
		if variant.MaxOffset >= variant.MinOffset {
			if variant.MaxOffset-variant.MinOffset > maxRequiredWindow {
				return false
			}
			continue
		}
		// 偏移无上界时依赖 Back 收缩左侧起点，缺少集合就无法限定窗口。
		if variant.Back == nil {
			return false
		}
	}
	return true
}

// maxRequiredWindow 与编译期候选推导保持一致的偏移窗口上限。
const maxRequiredWindow = 64

// makeFlatFinder 在长组与忽略大小写组满足两字节前缀表门槛时构建统一匹配器。
//
// 第二个返回值是统一匹配器覆盖不到、必须由调用方另行处理的文字：两字节前缀表
// 的最短文字为 2，忽略大小写组里长度为 1 的文字（例如 (?i)a）进不了任何前缀
// 条目，若随长组一起交给统一匹配器就会被静默丢弃。构建失败（合并集合为空、或
// 超过 newRequiredFlatFinder 的规模门槛）时返回 nil，并交回原样的两组文字，让
// 调用方按老路径分组处理。
func makeFlatFinder(long, caseless []hwlm.Literal) (func([]byte, []requiredHit) []requiredHit, []hwlm.Literal) {
	if len(long) == 0 && len(caseless) == 0 {
		return nil, nil
	}
	covered := make([]hwlm.Literal, 0, len(long)+len(caseless))
	covered = append(covered, long...)
	var leftover []hwlm.Literal
	for _, literal := range caseless {
		if len(literal.Value) < 2 {
			leftover = append(leftover, literal)
			continue
		}
		covered = append(covered, literal)
	}
	if len(covered) == 0 {
		return nil, leftover
	}
	finder := newRequiredFlatFinder(covered)
	if finder == nil {
		return nil, nil
	}
	return finder.find, leftover
}

// newRequiredFinder 按候选文字规模选择匹配器，并把每次扫描的命中缓冲
// 放入对象池，避免为每条命中重新分配临时切片。调用方 requiredScanStarts
// 会重新按起点排序并在派生的起点上去重，因此这里统一使用无序输出，
// 命中密集的语料上不再为同一批候选重复排序。
//
// 各分支把匹配器命中直接填入 requiredHit，避免为每条命中经过函数值调用；
// 命中密集时这一步是候选展开前的固定开销，改用直写的转换循环可让它随
// 命中数线性摊薄。
func newRequiredFinder(literals []hwlm.Literal) func([]byte, []requiredHit) []requiredHit {
	single, medium, long, caseless := splitRequiredLiterals(literals)
	if len(medium) == 0 && len(long) == 0 && len(caseless) == 0 {
		// 全部候选文字都是单字节：此时拆分拿不到任何 lane 收益，通用匹配器
		// （FDR/Teddy）在稀疏命中语料上的跳过能力反而更好。
		return newLiteralMatcherFinder(literals)
	}
	finders := make([]func([]byte, []requiredHit) []requiredHit, 0, 4)
	// 长组与忽略大小写组先用统一的两字节前缀表尝试：集合足够窄时它用一次宽窗口
	// 扫描覆盖两组，逐候选只做两三次小表查值，比 Teddy 的首字节分桶逐条确认
	// （每次触发复制 40 字节 Literal 并调用 hwlm.ContainsAt）便宜得多；集合过宽
	// 或同一前缀下文字过多时返回 nil，退回原有的分组通用匹配器。
	flat, flatLeftover := makeFlatFinder(long, caseless)
	if flat != nil {
		finders = append(finders, flat)
		if len(flatLeftover) > 0 {
			// 统一匹配器只覆盖长度不小于 2 的文字，忽略大小写的单字节文字
			// 仍要交给维护折叠自动机的通用后端。
			finders = append(finders, newLiteralMatcherFinder(flatLeftover))
		}
	} else {
		if len(long) > 0 {
			finders = append(finders, newLiteralMatcherFinder(long))
		}
		if len(caseless) > 0 {
			// 忽略大小写的候选必须交给按 ASCII 折叠建表的通用后端：扁平索引与
			// 专用精确匹配器都按精确字节建表，只有 Teddy/FDR 这类维护折叠自动机
			// 的后端才能正确处理，不能与前三类混排。
			finders = append(finders, newLiteralMatcherFinder(caseless))
		}
	}
	if len(medium) > 0 {
		// 2~3 字节的定长文字走专用两级查表，避免通用自动机的逐候选转移与
		// output 遍历；专用匹配器只在其覆盖范围内生效，异常时回退通用匹配器。
		if finder := newRequiredShortFinder(medium); finder != nil {
			finders = append(finders, finder)
		} else {
			finders = append(finders, newLiteralMatcherFinder(medium))
		}
	}
	if len(single) > 0 {
		finders = append(finders, newSingleByteFinder(single))
	}
	switch len(finders) {
	case 0:
		return nil
	case 1:
		return finders[0]
	default:
		return func(data []byte, dst []requiredHit) []requiredHit {
			for _, find := range finders {
				dst = find(data, dst)
			}
			return dst
		}
	}
}

// splitRequiredLiterals 把候选文字按匹配语义与长度分成四组：精确单字节、精确
// 短文字（2~3 字节）、精确长文字（不小于 4 字节）与忽略大小写的文字。候选中的
// 单字节成员会把匹配器的最短文字压到 1，候选掩码随之退化成"首字节集合"，几乎
// 覆盖全部输入字节；拆开之后多字节匹配器可以启用 2 个以上 lane，掩码只保留连续
// 多字节共同命中的位置。
//
// 短组与长组再拆一次，是因为二者的最佳后端不同：长组可以走 4 字节主键的扁平
// 索引，把逐候选确认压成一次定长比较；短组的文字长度不足主键宽度，只能留在
// 前缀树上。混在一起会把长组拖回前缀树，实测 10 MB 语料上拆开后长组从 21.5 ms
// 降到 9.2 ms。短组自身的首字节集合很窄，留在前缀树上代价有限。
//
// 忽略大小写的文字单独成组：hwlm 的扁平索引与专用精确匹配器都按精确字节建表，
// 只有 Teddy/FDR 这类维护折叠自动机的后端才能正确处理，不能与前三类混排。
func splitRequiredLiterals(literals []hwlm.Literal) (single, medium, long, caseless []hwlm.Literal) {
	for _, literal := range literals {
		if literal.CaseInsensitive {
			caseless = append(caseless, literal)
			continue
		}
		switch length := len(literal.Value); {
		case length <= 1:
			single = append(single, literal)
		case length < hwlm.FlatKeyShort:
			medium = append(medium, literal)
		default:
			long = append(long, literal)
		}
	}
	return single, medium, long, caseless
}

// newSingleByteFinder 为长度为 1 的候选文字构建专用匹配器：命中判定只取决于当前
// 字节，因此用一次 SIMD 字节集合扫描求出全部命中位置，再用 256 项查表换算成
// 候选编号，省掉通用匹配器的分桶确认。候选文字很少时改用逐文字 memchr，成本随
// 文字数线性增长而常数远小于集合扫描；命中顺序不参与后续语义，两种策略可以互换。
func newSingleByteFinder(literals []hwlm.Literal) func([]byte, []requiredHit) []requiredHit {
	var table [256]uint32
	var set [4]uint64
	values := make([]byte, 0, len(literals))
	for _, literal := range literals {
		if len(literal.Value) == 0 {
			continue
		}
		value := literal.Value[0]
		if table[value] == 0 {
			values = append(values, value)
		}
		table[value] = literal.ID
		set[value>>6] |= uint64(1) << uint(value&63)
	}
	// 候选文字很少时逐文字调用 memchr 明显更省：字节集合扫描每个 64 字节窗口
	// 都要付一次半字节查表与常数量化的固定成本，而 memchr 在稀疏字节上快近一个
	// 数量级。实测 26 KB 日志语料上两个单字节文字分别约 1.2 µs 与 3.1 µs，
	// 成本交点落在 4 个文字附近，因此只有超过阈值才走集合扫描。
	if len(values) <= singleByteFinderMemchrLimit {
		scanned := make([]singleByteScan, 0, len(values))
		for _, value := range values {
			scanned = append(scanned, singleByteScan{value: value, slot: int(table[value]) - 1})
		}
		return func(data []byte, dst []requiredHit) []requiredHit {
			for _, literal := range scanned {
				for off := 0; off < len(data); {
					index := bytes.IndexByte(data[off:], literal.value)
					if index < 0 {
						break
					}
					dst = append(dst, requiredHit{slot: literal.slot, pos: off + index})
					off += index + 1
				}
			}
			return dst
		}
	}
	tables := simd.NewByteSetTables([4][4]uint64{set})
	return func(data []byte, dst []requiredHit) []requiredHit {
		backend := dispatch.DefaultBackend()
		off := 0
		for ; off+simd.WideWidth <= len(data); off += simd.WideWidth {
			mask, ok := backend.WindowMask64(data, off, &tables, 1)
			if !ok {
				break
			}
			for mask != 0 {
				bit := bits.TrailingZeros64(mask)
				if id := table[data[off+bit]]; id != 0 {
					dst = append(dst, requiredHit{slot: int(id) - 1, pos: off + bit})
				}
				mask &^= 1 << uint(bit)
			}
		}
		for ; off < len(data); off++ {
			if id := table[data[off]]; id != 0 {
				dst = append(dst, requiredHit{slot: int(id) - 1, pos: off})
			}
		}
		return dst
	}
}

// singleByteFinderMemchrLimit 是改用逐文字 memchr 的单字节候选文字数量上限。
const singleByteFinderMemchrLimit = 4

// singleByteScan 保存一次 memchr 扫描所需的文字字节与候选编号。
type singleByteScan struct {
	value byte
	slot  int
}

// newLiteralMatcherFinder 为全部长度不小于 2 的候选文字选择具体匹配后端。
func newLiteralMatcherFinder(literals []hwlm.Literal) func([]byte, []requiredHit) []requiredHit {
	switch hwlm.Select(literals) {
	case "teddy":
		matcher := teddy.New(literals)
		pool := &sync.Pool{New: func() any { s := make([]teddy.Match, 0, 64); return &s }}
		return func(data []byte, dst []requiredHit) []requiredHit {
			buf := pool.Get().(*[]teddy.Match)
			matches := matcher.FindIntoUnsorted(data, (*buf)[:0])
			*buf = matches
			dst = growRequiredHits(dst, len(dst)+len(matches))
			for index := range matches {
				dst = append(dst, requiredHit{slot: int(matches[index].ID) - 1, pos: matches[index].From})
			}
			if !retainLiteralMatches(cap(matches)) {
				*buf = nil
			}
			pool.Put(buf)
			return dst
		}
	case "fdr":
		matcher := fdr.New(literals)
		pool := &sync.Pool{New: func() any { s := make([]fdr.Match, 0, 64); return &s }}
		return func(data []byte, dst []requiredHit) []requiredHit {
			buf := pool.Get().(*[]fdr.Match)
			matches := matcher.FindIntoUnsorted(data, (*buf)[:0])
			*buf = matches
			dst = growRequiredHits(dst, len(dst)+len(matches))
			for index := range matches {
				dst = append(dst, requiredHit{slot: int(matches[index].ID) - 1, pos: matches[index].From})
			}
			if !retainLiteralMatches(cap(matches)) {
				*buf = nil
			}
			pool.Put(buf)
			return dst
		}
	default:
		matcher := noodle.New(literals)
		pool := &sync.Pool{New: func() any { s := make([]noodle.Match, 0, 64); return &s }}
		return func(data []byte, dst []requiredHit) []requiredHit {
			buf := pool.Get().(*[]noodle.Match)
			matches := matcher.FindIntoUnsorted(data, (*buf)[:0])
			*buf = matches
			dst = growRequiredHits(dst, len(dst)+len(matches))
			for index := range matches {
				dst = append(dst, requiredHit{slot: int(matches[index].ID) - 1, pos: matches[index].From})
			}
			if !retainLiteralMatches(cap(matches)) {
				*buf = nil
			}
			pool.Put(buf)
			return dst
		}
	}
}

// growRequiredHits 在追加命中前保证目标切片能容纳 need 条结果。
//
// 之前各分支都把目标切片截成 [:0]，只有在"单个匹配器独占输出"时才是等价的：
// 必须文字索引按文字长度拆成多组后，后一组的空结果会把前一组已经积累的命中
// 一起清掉，因此这里改成只扩容、不截断，把"清空"留给调用方的初始切片。
func growRequiredHits(dst []requiredHit, need int) []requiredHit {
	if cap(dst) < need {
		grown := make([]requiredHit, len(dst), need)
		copy(grown, dst)
		return grown
	}
	return dst
}

func newLiteralCandidateFinder(literals []hwlm.Literal) (string, func([]byte) []literalCandidate, func([]byte, []literalCandidate) []literalCandidate) {
	switch hwlm.Select(literals) {
	case "teddy":
		matcher := teddy.New(literals)
		// 复用 teddy.Match 缓冲，避免每次扫描都重新分配。
		matchPool := &sync.Pool{New: func() any { s := make([]teddy.Match, 0, 64); return &s }}
		convert := func(data []byte, dst []literalCandidate) []literalCandidate {
			mp := matchPool.Get().(*[]teddy.Match)
			matches := matcher.FindInto(data, (*mp)[:0])
			*mp = matches
			out := dst[:0]
			if cap(out) < len(matches) {
				out = make([]literalCandidate, 0, len(matches))
			}
			for _, match := range matches {
				out = append(out, literalCandidate{ID: match.ID, From: match.From, To: match.To})
			}
			if !retainLiteralMatches(cap(matches)) {
				*mp = nil
			}
			matchPool.Put(mp)
			return out
		}
		return "teddy", func(data []byte) []literalCandidate { return convert(data, nil) }, convert
	case "noodle":
		matcher := noodle.New(literals)
		matchPool := &sync.Pool{New: func() any { s := make([]noodle.Match, 0, 64); return &s }}
		convert := func(data []byte, dst []literalCandidate) []literalCandidate {
			mp := matchPool.Get().(*[]noodle.Match)
			matches := matcher.FindInto(data, (*mp)[:0])
			*mp = matches
			out := dst[:0]
			if cap(out) < len(matches) {
				out = make([]literalCandidate, 0, len(matches))
			}
			for _, match := range matches {
				out = append(out, literalCandidate{ID: match.ID, From: match.From, To: match.To})
			}
			if !retainLiteralMatches(cap(matches)) {
				*mp = nil
			}
			matchPool.Put(mp)
			return out
		}
		return "noodle", func(data []byte) []literalCandidate { return convert(data, nil) }, convert
	default:
		matcher := fdr.New(literals)
		matchPool := &sync.Pool{New: func() any { s := make([]fdr.Match, 0, 64); return &s }}
		convert := func(data []byte, dst []literalCandidate) []literalCandidate {
			mp := matchPool.Get().(*[]fdr.Match)
			matches := matcher.FindInto(data, (*mp)[:0])
			*mp = matches
			out := dst[:0]
			if cap(out) < len(matches) {
				out = make([]literalCandidate, 0, len(matches))
			}
			for _, match := range matches {
				out = append(out, literalCandidate{ID: match.ID, From: match.From, To: match.To})
			}
			if !retainLiteralMatches(cap(matches)) {
				*mp = nil
			}
			matchPool.Put(mp)
			return out
		}
		return "fdr", func(data []byte) []literalCandidate { return convert(data, nil) }, convert
	}
}

// RoseProgram 返回当前规则中可转换为文字角色的独立程序。
func (scanner *Scanner) roseProgram() *rose.Program {
	if scanner == nil {
		return nil
	}
	if scanner.rosePlan != nil {
		return scanner.rosePlan
	}
	return scanner.buildRoseProgram()
}

func (scanner *Scanner) buildRoseProgram() *rose.Program {
	if scanner == nil {
		return nil
	}
	roles := make([]rose.Role, 0)
	usedIDs := make(map[uint32]struct{})
	for _, rule := range scanner.rules {
		usedIDs[rule.id] = struct{}{}
	}
	nextRoleID := uint32(1)
	for {
		if _, exists := usedIDs[nextRoleID]; !exists {
			break
		}
		nextRoleID++
	}
	for _, rule := range scanner.rules {
		alternatives, ok := roseLiteralAlternatives(rule.root)
		if !ok || rule.comb != nil {
			continue
		}
		if len(alternatives) == 1 {
			if _, _, _, direct := roseLiteralPattern(alternatives[0]); !direct {
				if literal := longestLiteral(rule.root); len(literal) > 0 {
					if rule.ext != nil && rule.ext.Flags&ExtFlagMinLength != 0 && rule.ext.MinLength > uint64(len(literal)) {
						continue
					}
					role := rose.Role{ID: rule.id, Literal: literal, ReportID: rule.id, CaseInsensitive: rule.flags&CompileCaseless != 0, Confirm: true}
					if rule.ext != nil {
						if rule.ext.Flags&ExtFlagMinOffset != 0 {
							role.MinOffset = rule.ext.MinOffset
						}
						role.MaxOffset = rule.ext.MaxOffset
						role.HasMaxOffset = rule.ext.Flags&ExtFlagMaxOffset != 0
					}
					roles = append(roles, role)
				}
				continue
			}
		}
		for index, alternative := range alternatives {
			literal, anchored, endOfData, ok := roseLiteralPattern(alternative)
			confirm := false
			if !ok || len(literal) == 0 {
				literal = longestLiteral(alternative)
				if len(literal) == 0 {
					continue
				}
				anchored, endOfData, confirm = false, false, true
			}
			if rule.ext != nil && rule.ext.Flags&ExtFlagMinLength != 0 && rule.ext.MinLength > uint64(len(literal)) {
				continue
			}
			roleID := rule.id
			if index > 0 {
				for {
					if _, exists := usedIDs[nextRoleID]; !exists {
						break
					}
					nextRoleID++
				}
				roleID = nextRoleID
				usedIDs[roleID] = struct{}{}
				nextRoleID++
			}
			role := rose.Role{ID: roleID, Literal: literal, ReportID: rule.id, CaseInsensitive: rule.flags&CompileCaseless != 0, Anchored: anchored, EndOfData: endOfData, Confirm: confirm}
			if rule.ext != nil {
				if rule.ext.Flags&ExtFlagMinOffset != 0 {
					role.MinOffset = rule.ext.MinOffset
				}
				role.MaxOffset = rule.ext.MaxOffset
				role.HasMaxOffset = rule.ext.Flags&ExtFlagMaxOffset != 0
			}
			roles = append(roles, role)
		}
	}
	roles = dedupeRoseRoles(roles)
	if len(roles) == 0 {
		return nil
	}
	program := rose.New(roles)
	program.Normalize()
	instructions := make([]rose.Instruction, 0, len(roles))
	for _, role := range roles {
		flags := uint32(0)
		if index, ok := scanner.ruleIndex[role.ReportID]; ok && index >= 0 && index < len(scanner.rules) {
			flags = uint32(scanner.rules[index].flags)
		}
		instructions = append(instructions, rose.Instruction{Kind: rose.InstructionReport, RoleID: role.ID, ReportID: role.ReportID, Flags: flags})
	}
	if err := program.SetInstructions(instructions); err != nil {
		return nil
	}
	return program
}

// dedupeRoseRoles 合并候选扫描完全等价的角色：这些角色在相同输入上产生
// 完全相同的候选与报告，重复扫描只会浪费确认开销。
// 等价角色之间按扫描代价挑选代表，代价相同则保留编号更小的角色，
// 保证程序构建结果稳定。
func dedupeRoseRoles(roles []rose.Role) []rose.Role {
	if len(roles) < 2 {
		return roles
	}
	out := make([]rose.Role, 0, len(roles))
	for _, role := range roles {
		merged := false
		for i := range out {
			if !out[i].ScanEquivalent(role) {
				continue
			}
			weight, existing := role.Weight(), out[i].Weight()
			if weight < existing || (weight == existing && role.ID < out[i].ID) {
				out[i] = role
			}
			merged = true
			break
		}
		if !merged {
			out = append(out, role)
		}
	}
	return out
}

func roseLiteralAlternatives(n parser.Node) ([]parser.Node, bool) {
	switch value := n.(type) {
	case parser.Alternation:
		out := make([]parser.Node, 0)
		for _, option := range value.Options {
			parts, ok := roseLiteralAlternatives(option)
			if !ok {
				return nil, false
			}
			out = append(out, parts...)
		}
		return out, len(out) > 0
	case parser.Group:
		if value.HasScopedFlags() || value.Atomic {
			return nil, false
		}
		return roseLiteralAlternatives(value.Child)
	case parser.Sequence:
		products := [][]parser.Node{{}}
		for _, child := range value.Elements {
			parts, ok := roseLiteralAlternatives(child)
			if !ok {
				return nil, false
			}
			if len(products) > 256 || len(parts) > 256 || len(products) > 256/len(parts) {
				return nil, false
			}
			next := make([][]parser.Node, 0, len(products)*len(parts))
			for _, prefix := range products {
				for _, part := range parts {
					elements := append(append([]parser.Node(nil), prefix...), part)
					next = append(next, elements)
				}
			}
			products = next
		}
		out := make([]parser.Node, 0, len(products))
		for _, elements := range products {
			out = append(out, parser.Sequence{Elements: elements})
		}
		return out, len(out) > 0
	default:
		return []parser.Node{n}, true
	}
}

// roseLiteralPattern 提取可由角色运行时直接确认的文字及绝对边界。
func roseLiteralPattern(n parser.Node) ([]byte, bool, bool, bool) {
	switch v := n.(type) {
	case parser.Literal:
		return append([]byte(nil), v.Value...), false, false, true
	case parser.Group:
		if v.HasScopedFlags() || v.Atomic {
			return nil, false, false, false
		}
		return roseLiteralPattern(v.Child)
	case parser.Repeat:
		if v.Max < 0 || v.Min != v.Max || v.Min > 32 {
			return nil, false, false, false
		}
		literal, anchored, endOfData, ok := roseLiteralPattern(v.Child)
		if !ok {
			return nil, false, false, false
		}
		out := make([]byte, 0, len(literal)*v.Min)
		for i := 0; i < v.Min; i++ {
			out = append(out, literal...)
		}
		return out, anchored, endOfData, true
	case parser.Sequence:
		literal := make([]byte, 0)
		anchored, endOfData := false, false
		for _, child := range v.Elements {
			switch assertion := child.(type) {
			case parser.Assertion:
				switch assertion.Kind {
				case parser.BeginAbsolute:
					anchored = true
				case parser.EndAbsolute:
					endOfData = true
				default:
					return nil, false, false, false
				}
			default:
				part, partAnchored, partEnd, ok := roseLiteralPattern(child)
				if !ok {
					return nil, false, false, false
				}
				literal = append(literal, part...)
				anchored = anchored || partAnchored
				endOfData = endOfData || partEnd
			}
		}
		return literal, anchored, endOfData, true
	default:
		return nil, false, false, false
	}
}

func longestLiteral(n parser.Node) []byte {
	best := []byte(nil)
	parser.Walk(n, func(node parser.Node) bool {
		if literal, ok := node.(parser.Literal); ok && len(literal.Value) > len(best) {
			best = append(best[:0], literal.Value...)
		}
		return true
	})
	return best
}

// backendEligible 判断规则是否可以直接使用已编译后端完成单次扫描。
func backendEligible(rule compiledRule) bool {
	if rule.program == nil || rule.repeat != nil || rule.comb != nil || rule.ext != nil || rule.containsAny || rule.requiresEndOfData || rule.flags&CompileSOMLeftmost != 0 {
		return false
	}
	// 末尾报告只能在数据末尾触发，字节状态表的普通匹配会提前误报，
	// 因此保留 AST 确认路径。
	if rule.program.HasEODReports() {
		return false
	}
	if _, _, fixed := parser.FixedWidth(rule.root); !fixed {
		return false
	}
	if rule.nfaEngine != nil && rule.nfaEngine.Independent() && !rule.nfaEngine.HasUnicode() && !rule.info.RequiresStatefulRuntime() {
		return rule.flags&(CompileCaseless|CompileUTF8|CompileUCP|CompileMultiline|CompileDotAll|CompileSingleMatch|CompileQuiet) == 0
	}
	literal, ok := literalPattern(rule.root)
	if !ok || len(literal) == 0 || hasLiteralSelfOverlap(literal) {
		return false
	}
	return rule.flags&(CompileCaseless|CompileUTF8|CompileUCP|CompileMultiline|CompileDotAll|CompileSingleMatch|CompileQuiet) == 0 && !rule.info.RequiresStatefulRuntime()
}

func requiresEndOfData(root parser.Node) bool {
	requires := false
	parser.Walk(root, func(node parser.Node) bool {
		assertion, ok := node.(parser.Assertion)
		if !ok {
			return true
		}
		switch assertion.Kind {
		case parser.End, parser.EndAbsolute, parser.EndBeforeFinalNewline:
			requires = true
			return false
		default:
		}
		return true
	})
	return requires
}

func hasLiteralSelfOverlap(literal []byte) bool {
	for width := 1; width < len(literal); width++ {
		matched := true
		for i := 0; i < width; i++ {
			if literal[i] != literal[len(literal)-width+i] {
				matched = false
				break
			}
		}
		if matched {
			return true
		}
	}
	return false
}

func uint32ToInt(value uint32) int {
	maxInt := uint64(^uint(0) >> 1)
	if uint64(value) > maxInt {
		return int(maxInt)
	}
	return int(value)
}

// Validate 检查扫描计划中的规则索引、编号唯一性和元数据一致性。
func (scanner *Scanner) validate() error {
	if scanner == nil {
		return fmt.Errorf("nil scanner")
	}
	if len(scanner.ruleIndex) != len(scanner.rules) || len(scanner.ruleOrder) != len(scanner.rules) {
		return fmt.Errorf("rule index size mismatch")
	}
	seen := make(map[uint32]struct{}, len(scanner.rules))
	for index, rule := range scanner.rules {
		if _, exists := seen[rule.id]; exists {
			return fmt.Errorf("duplicate rule id %d", rule.id)
		}
		seen[rule.id] = struct{}{}
		if position, ok := scanner.ruleIndex[rule.id]; !ok || position != index {
			return fmt.Errorf("rule index mismatch")
		}
		if err := rule.info.Validate(); err != nil {
			return err
		}
		if err := rule.ext.Validate(); err != nil {
			return err
		}
		if rule.program != nil {
			if err := rule.program.Validate(); err != nil {
				return err
			}
		}
		if rule.nfaEngine != nil {
			if err := rule.nfaEngine.Validate(); err != nil {
				return err
			}
			if rule.nfaEngine.Program == nil || rule.nfaEngine.Program.Graph == nil {
				return fmt.Errorf("rule nfa engine has no graph")
			}
		}
	}
	for id := range scanner.candidateIDs {
		if _, ok := scanner.ruleIndex[id]; !ok {
			return fmt.Errorf("candidate references unknown rule %d", id)
		}
	}
	for id := range scanner.literalIDs {
		if _, ok := scanner.candidateIDs[id]; !ok {
			return fmt.Errorf("literal candidate references unknown rule %d", id)
		}
	}
	return nil
}

// Scan 扫描输入并返回全部匹配。
func (scanner *Scanner) Scan(data []byte) ([]Match, error) {
	return scanner.ScanInto(data, nil)
}

// ScanInto 将匹配追加到 matches；输入数据不会被修改。
func (scanner *Scanner) ScanInto(data []byte, matches []Match) ([]Match, error) {
	return scanner.scanInto(data, matches)
}

// newScanContext 分配一个全新的扫描上下文。
func newScanContext() *scanContext {
	return &scanContext{Scratch: scratch.New(), Reports: report.New()}
}

// acquireScanContext 取出一个可复用的扫描上下文。优先使用保留槽位，其次才是
// sync.Pool：保留槽位不参与 GC 清理，顺序调用时缓冲容量可以跨扫描保持。
func (scanner *Scanner) acquireScanContext() *scanContext {
	if slot := scanner.retainedContext; slot != nil {
		if ctx := slot.ctx.Swap(nil); ctx != nil {
			return ctx
		}
	}
	if scanner.contextPool != nil {
		if ctx, _ := scanner.contextPool.Get().(*scanContext); ctx != nil {
			return ctx
		}
	}
	return newScanContext()
}

// releaseScanContext 归还扫描上下文。保留槽位为空、且工作集没有超出保留上限时
// 留下当前上下文，否则回退到 sync.Pool；并发扫描多出来的上下文因此仍然可复用，
// 不会退化成每次重建。
func (scanner *Scanner) releaseScanContext(ctx *scanContext) {
	if ctx == nil {
		return
	}
	if slot := scanner.retainedContext; slot != nil && ctx.retainedBytes() <= retainedContextLimit && slot.ctx.CompareAndSwap(nil, ctx) {
		return
	}
	if scanner.contextPool != nil {
		scanner.contextPool.Put(ctx)
	}
}

// retainedContextLimit 是允许留在保留槽位（不参与 GC 回收）的上下文缓冲估算
// 上限。保留槽位让顺序扫描在 GC 之后仍能直接复用容量，但把超大工作集也钉住会
// 让长期存活的 Scanner 一直背着上百 MB，因此超过上限的上下文只回到 sync.Pool，
// 由 GC 在需要时回收。
const retainedContextLimit = 64 << 20

// retainedBytes 估算上下文里随输入长度线性增长的主要缓冲占用。
func (ctx *scanContext) retainedBytes() int {
	var (
		hit       requiredHit
		match     Match
		candidate literalCandidate
	)
	return cap(ctx.requiredHits)*int(unsafe.Sizeof(hit)) +
		cap(ctx.requiredStarts)*int(unsafe.Sizeof(uint64(0))) +
		cap(ctx.requiredSortScratch)*int(unsafe.Sizeof(uint64(0))) +
		cap(ctx.requiredStartCounts)*int(unsafe.Sizeof(uint32(0))) +
		cap(ctx.directMatches)*int(unsafe.Sizeof(match)) +
		cap(ctx.literalCandidates)*int(unsafe.Sizeof(candidate)) +
		cap(ctx.ruleMatchBuf)*int(unsafe.Sizeof(0)) +
		cap(ctx.nodeEval.ends.buf)*int(unsafe.Sizeof(0)) +
		cap(ctx.nodeEval.states.buf)*int(unsafe.Sizeof(captureState{}))
}

func (scanner *Scanner) scanInto(data []byte, matches []Match) ([]Match, error) {
	if scanner == nil {
		return matches, nil
	}
	// 执行前校验不可变计划，避免被篡改的后端布局或规则索引进入块扫描。
	if err := scanner.validationErr; err != nil {
		return matches, err
	}
	base := len(matches)
	// 所有规则均为精确文字且无需特殊报告语义时，优先使用共享文字索引：
	// 一次候选扫描即覆盖全部规则，避免 Rose 角色按候选逐个确认的线性放大。
	fastLiteral := scanner.fastLiteral
	if fastLiteral && scanner.literalFind != nil {
		// 复用栈上缓冲，避免每次扫描都重新分配候选切片。
		var candBuf [256]literalCandidate
		var candidates []literalCandidate
		if scanner.literalFindInto != nil {
			candidates = scanner.literalFindInto(data, candBuf[:0])
		} else {
			candidates = scanner.literalFind(data)
		}
		// fastLiteral 路径下每个候选对应唯一规则，候选数与最终匹配数相等；
		// 预分配匹配切片避免多次扩容。
		expected := len(matches) + len(candidates)
		if cap(matches) < expected {
			matches = append(make([]Match, 0, expected+len(candidates)), matches...)
		}
		for _, candidate := range candidates {
			matches = append(matches, Match{Id: candidate.ID, From: uint64(candidate.From), To: uint64(candidate.To)})
		}
		return matches, nil
	}
	// 当全部规则都能安全转换为 Rose 角色时，整块扫描直接复用角色调度器；
	// 只要存在无法转换的规则或组合规则，就保留原有统一确认路径，避免候选
	// 调度改变未覆盖规则的结果语义。
	if scanner.canUseRoseInScan {
		// 纯文字角色没有状态迁移或确认依赖，直接消费共享候选索引，
		// 避免为每个候选创建调度队列和执行去重表。
		roseMatches := scanner.scanRoseDirectInto(data, matches[base:base])
		matches = matches[:base]
		matches = append(matches, roseMatches...)
		return matches, nil
	}
	// 整块后端扫描只覆盖无法进入候选文字索引的规则；能由候选文字定位的
	// 规则交给候选起点确认，避免每条规则各扫一遍整块输入。
	requiredDriven := scanner.requiredIndexDriven()
	backendOnly := scanner.backendOnly
	if backendOnly {
		for ri := range scanner.rules {
			if !scanner.backendScanned(ri, backendOnly, requiredDriven) {
				continue
			}
			rule := scanner.rules[ri]
			if rule.nfaEngine != nil {
				// 缓冲只在真正执行后端扫描的规则作用域内声明，候选索引覆盖
				// 全部规则时不会为未使用的缓冲付出每次扫描的堆分配。
				var spanBuf [32]nfa.Span
				spans := rule.nfaEngine.SpansInto(data, spanBuf[:0], 0)
				for _, span := range spans {
					matches = append(matches, Match{Id: rule.id, From: uint64(span.From), To: uint64(span.To)})
				}
				continue
			}
			for _, span := range rule.program.Spans(data) {
				matches = append(matches, Match{Id: rule.id, From: uint64(span[0]), To: uint64(span[1])})
			}
		}
	}
	// 候选索引未生效时全部规则都由后端整块扫描处理，无需进入确认路径。
	if backendOnly && !requiredDriven && len(scanner.confirmAllRules) == 0 {
		return matches, nil
	}
	ctx := scanner.acquireScanContext()
	if ctx.Scratch == nil {
		ctx.Scratch = scratch.New()
	}
	if ctx.Reports == nil {
		ctx.Reports = report.New()
	}
	ctx.Reports.Reset()
	defer func() {
		ctx.Scratch.Reset()
		ctx.Reports.Reset()
		// 保留切片长度以便下次扫描直接复用容量，只清零内容。
		clear(ctx.blockedUntil)
		clear(ctx.fired)
		ctx.comboTriggers = ctx.comboTriggers[:0]
		ctx.literalCandidates = ctx.literalCandidates[:0]
		ctx.ruleMatchBuf = ctx.ruleMatchBuf[:0]
		// 命中缓冲与候选起点同源：固定阈值在密集输入下会变成"每次扫描都释放"。
		// 命中缓冲与候选起点同源，同样按绝对上限保留：固定阈值在密集输入下
		// 会退化成"每次扫描都释放、下次重新 grow"。
		if retainRequiredHits(cap(ctx.requiredHits)) {
			ctx.requiredHits = ctx.requiredHits[:0]
		} else {
			ctx.requiredHits = nil
		}
		// 命中密集的大块输入会把候选起点撑到百万级，固定阈值会让每次扫描都从
		// cap=0 重新 grow，累计分配约为数据量的 5 倍；这里按绝对上限保留，见 §26。
		if retainRequiredStarts(cap(ctx.requiredStarts)) {
			ctx.requiredStarts = ctx.requiredStarts[:0]
		} else {
			ctx.requiredStarts = nil
		}
		// 补登队列长度有界于规则数，保留容量即可，无需上限判断。
		ctx.retryStarts = ctx.retryStarts[:0]
		// 线性排序缓冲按下标复用，超限时释放，避免长期占住大块内存。
		resetRequiredSortBuffer(&ctx.requiredSortScratch)
		resetRequiredSortBuffer(&ctx.requiredStartCounts)
		for id, starts := range ctx.prefilterStarts {
			if len(starts) > 1<<20 {
				delete(ctx.prefilterStarts, id)
				continue
			}
			clear(starts)
		}
		// 逐起点子表跨扫描复用，只清空内容，避免每次扫描重建内层 map。
		for start, byRule := range ctx.literalEnds {
			if len(byRule) > 1<<16 {
				delete(ctx.literalEnds, start)
				continue
			}
			clear(byRule)
		}
		ctx.nodeEval.reset()
		scanner.releaseScanContext(ctx)
	}()
	// 每个起点都必须独立求值，块模式允许同一规则产生重叠命中。
	blockedUntil := reserveInts(ctx.blockedUntil, len(scanner.rules))
	fired := reserveBools(ctx.fired, len(scanner.rules))
	ctx.blockedUntil = blockedUntil
	ctx.fired = fired
	if scanner.hasCollapse {
		windows := reserveCollapseWindows(ctx.collapseWindows, len(scanner.rules))
		ctx.collapseWindows = windows
	}
	comboTriggers := ctx.comboTriggers[:0]
	// SOM_LEFTMOST 对同一结束位置只保留最左起点，避免 AST 回溯产生重复事件。
	var somSeen map[uint32]map[int]struct{}
	// 候选驱动扫描不使用共享文字索引：池化上下文里可能残留上一次超限回退
	// 建立的过滤表，直接复用会让确认阶段误判“起点不是候选”而漏报。
	var literalEnds map[int]map[uint32]int
	if !requiredDriven {
		literalEnds = ctx.literalEnds
	}
	var literalCandidates []literalCandidate
	// 对没有合并文字索引的规则预先建立前缀起点集合，避免在每个起点
	// 重复执行 Contains；候选集合只用于过滤，命中后仍由完整规则确认。
	prefilterStarts := ctx.prefilterStarts
	if prefilterStarts == nil {
		prefilterStarts = make(map[uint32]map[int]struct{})
		ctx.prefilterStarts = prefilterStarts
	}
	for id, starts := range prefilterStarts {
		for start := range starts {
			delete(starts, start)
		}
		delete(prefilterStarts, id)
	}
	// 必须文字索引已经为全部规则给出候选起点时，共享文字索引的整块扫描
	// 只会重复同一份候选集合，直接跳过；规则确认仍由各自的完整语义完成。
	if scanner.literalFind != nil && !requiredDriven {
		if scanner.literalFindInto != nil {
			literalCandidates = scanner.literalFindInto(data, ctx.literalCandidates[:0])
			ctx.literalCandidates = literalCandidates
		} else {
			literalCandidates = scanner.literalFind(data)
		}
		if !fastLiteral || backendOnly {
			if literalEnds == nil {
				literalEnds = make(map[int]map[uint32]int)
				ctx.literalEnds = literalEnds
			}
			resetLiteralEnds(literalEnds)
			for _, candidate := range literalCandidates {
				byRule := literalEnds[candidate.From]
				if byRule == nil {
					byRule = make(map[uint32]int)
					literalEnds[candidate.From] = byRule
				}
				byRule[candidate.ID] = candidate.To
			}
		}
	} else if !requiredDriven && len(scanner.candidateIDs) == 1 && len(scanner.literalIDs) == 1 {
		// 只为「候选起点由候选文字索引枚举」的扫描建立单文字结束偏移表；
		// 候选驱动扫描的起点已由必须文字索引给定，这张表不会参与候选枚举，
		// 建立它只是每个文字命中分配一个 map。实测 2 条规则（`ab|cd` + 文字
		// 规则）的 64 KiB 语料上，这一步凭空产生约 700 次分配，占该配置全部
		// 分配的 97%，而扫描结果与耗时都只取决于必须文字路径。
		if literalEnds == nil {
			literalEnds = make(map[int]map[uint32]int)
			ctx.literalEnds = literalEnds
		}
		resetLiteralEnds(literalEnds)
		scanner.fillSingleLiteralEnds(data, literalEnds)
	}
	for _, rule := range scanner.rules {
		// 编译期为所有规则推导出的候选文字只在调用方显式开启 CompilePrefilter 时
		// 参与起点过滤（见 verify），其余规则建立起点表属于无效工作。
		if rule.flags&CompilePrefilter == 0 || len(rule.prefilter) == 0 || scanner.literalFind != nil {
			continue
		}
		if _, indexed := scanner.candidateIDs[rule.id]; indexed && literalEnds != nil {
			continue
		}
		positions := hwlm.FindAll(data, hwlm.Literal{Value: rule.prefilter, CaseInsensitive: rule.flags&CompileCaseless != 0})
		if len(positions) == 0 {
			prefilterStarts[rule.id] = nil
			continue
		}
		starts := prefilterStarts[rule.id]
		if starts == nil {
			starts = make(map[int]struct{}, len(positions))
			prefilterStarts[rule.id] = starts
		}
		for _, position := range positions {
			starts[position] = struct{}{}
		}
		prefilterStarts[rule.id] = starts
	}
	// 单个精确文字规则无需创建扫描上下文，直接返回索引命中结果。
	if !backendOnly && !scanner.hasCombo && len(scanner.rules) == 1 && len(scanner.literalIDs) == 1 && literalEnds != nil {
		rule := scanner.rules[0]
		if rule.flags&(CompileQuiet|CompileSingleMatch) == 0 {
			for start, byRule := range literalEnds {
				if end, ok := byRule[rule.id]; ok {
					matches = append(matches, Match{Id: rule.id, From: uint64(start), To: uint64(end)})
				}
			}
			return matches, nil
		}
	}
	// 纯文字规则可直接消费候选索引，避免再执行“每个起点遍历全部规则”的确认循环。
	// 仅在所有规则均为已验证的精确文字时启用，复杂前缀规则仍走原确认路径。
	allFast := fastLiteral && (scanner.literalFind != nil || literalEnds != nil)
	if allFast {
		if len(literalCandidates) > 0 && cap(matches)-len(matches) < len(literalCandidates) {
			grown := make([]Match, len(matches), len(matches)+len(literalCandidates))
			copy(grown, matches)
			matches = grown
		}
		if len(literalCandidates) > 0 {
			for _, candidate := range literalCandidates {
				matches = append(matches, Match{Id: candidate.ID, From: uint64(candidate.From), To: uint64(candidate.To)})
			}
		} else {
			for start, byRule := range literalEnds {
				for id, end := range byRule {
					matches = append(matches, Match{Id: id, From: uint64(start), To: uint64(end)})
				}
			}
		}
		return matches, nil
	}
	state := blockScanState{
		scanner:         scanner,
		data:            data,
		ctx:             ctx,
		blockedUntil:    blockedUntil,
		fired:           fired,
		comboTriggers:   comboTriggers,
		somSeen:         somSeen,
		literalEnds:     literalEnds,
		direct:          ctx.directMatches[:0],
		prefilterStarts: prefilterStarts,
		backendOnly:     backendOnly,
		requiredDriven:  requiredDriven,
	}
	// 必须文字候选驱动：任意匹配都包含至少一个候选文字，因此只需在候选文字
	// 命中位置派生的起点上确认，跳过整块“每个起点 × 每条规则”的循环。
	if requiredDriven {
		if starts, ok := scanner.requiredScanStarts(ctx, data); ok {
			state.requiredStartDriven = true
			// 真实命中数量有界于 (起点数 + 1)，而候选起点可以远多于命中；
			// 这里只在候选不多时按候选数预分配，候选密集时交给 append 增长，
			// 避免为稀疏场景申请整块等于候选数的结果缓冲。
			if scanner.simpleReports && cap(ctx.directMatches) < len(starts) && len(starts) <= directMatchPrealloc {
				ctx.directMatches = make([]Match, 0, len(starts))
				state.direct = ctx.directMatches[:0]
			}
			state.confirmCandidatesAndFallback(starts, scanner.startBytes)
			return state.finish(matches)
		}
		// 候选起点超过上限时退回逐起点确认；此时补建共享文字索引，
		// 让确认循环仍能按候选位置过滤，而不是遍历全部起点。
		if scanner.literalFind != nil && state.literalEnds == nil {
			literalEnds = scanner.refreshLiteralEnds(ctx, data, ctx.literalEnds)
			state.literalEnds = literalEnds
		}
	}
	fallbackIndex := scanner.startBytesAll
	if fallbackIndex == nil {
		fallbackIndex = scanner.startBytes
	}
	if fallbackIndex == nil {
		state.confirmPerStart(scanner.confirmAllRules)
	} else {
		state.confirmIndexed(fallbackIndex)
	}
	return state.finish(matches)
}

// confirmFrames 返回确认程序使用的回溯栈，按扫描上下文复用同一块内存。
func (ctx *scanContext) confirmFrames() []confirmFrame {
	if ctx.confirmStack == nil {
		ctx.confirmStack = make([]confirmFrame, confirmMaxStack)
	}
	return ctx.confirmStack
}

// blockScanState 保存块扫描的确认状态，供候选起点驱动与逐起点两条路径共用，
// 保证两种派发方式的过滤、去重和报告语义完全一致。
type blockScanState struct {
	scanner         *Scanner
	data            []byte
	ctx             *scanContext
	blockedUntil    []int
	fired           []bool
	comboTriggers   []report.Event
	somSeen         map[uint32]map[int]struct{}
	literalEnds     map[int]map[uint32]int
	prefilterStarts map[uint32]map[int]struct{}
	backendOnly     bool
	// requiredDriven 表示本次扫描由候选文字索引驱动，此时整块后端扫描只
	// 覆盖未被候选索引覆盖的规则。
	requiredDriven bool
	// requiredStartDriven 表示当前确认来自必须文字候选展开出的起点集合，
	// 确认程序据此安全跳过已被候选索引验证过的入口文字。
	requiredStartDriven bool
	// direct 保存 simpleReports 模式下直接产出的匹配结果，避免经过报告
	// 管理器的去重表与二次拷贝。
	direct []Match
}

// confirmPerStart 对给定规则集合逐起点确认，起点从 0 到数据末尾。
func (st *blockScanState) confirmPerStart(rules []int) {
	if len(rules) == 0 {
		return
	}
	for start := 0; start <= len(st.data); start++ {
		for _, ri := range rules {
			if !st.verify(ri, start) {
				return
			}
		}
		if st.ctx.Reports.Stopped() {
			return
		}
	}
}

// buildConfirmRuleLists 预计算逐起点确认所需的规则集合，避免每次扫描重复遍历。
func (scanner *Scanner) buildConfirmRuleLists() {
	requiredDriven := scanner.requiredIndexDriven()
	scanner.confirmAllRules = make([]int, 0, len(scanner.rules))
	scanner.confirmUncoveredRules = make([]int, 0, len(scanner.rules))
	scanner.guardRunGroups = nil
	var runs []guardRun
	for ri := range scanner.rules {
		if scanner.backendScanned(ri, scanner.backendOnly, requiredDriven) {
			continue
		}
		scanner.confirmAllRules = append(scanner.confirmAllRules, ri)
		if scanner.guardRunCovers(ri) {
			if run, ok := guardRunFor(ri, &scanner.rules[ri]); ok {
				// 组合规则的触发位置按「每条规则的全部命中」累计，收敛候选会
				// 改变触发集合，因此只在没有组合依赖时启用（见 guardRunRepeat）。
				if scanner.hasCombo {
					run.leftmostOnly, run.stride = false, 0
				}
				runs = append(runs, run)
			}
			continue
		}
		if requiredDriven && scanner.requiredIndexCovered(ri) {
			continue
		}
		scanner.confirmUncoveredRules = append(scanner.confirmUncoveredRules, ri)
	}
	scanner.guardRunGroups = groupGuardRuns(runs)
}

// guardRunCandidates 选出应该改由前缀字节约束枚举候选起点的规则。
//
// 选中条件是该约束窗口比候选文字索引更省：无法进入必须文字索引的规则一律
// 改用约束枚举；可以进入但候选文字只有一两个字节时，其在混合字母数字语料上
// 的命中密度远高于同长度的集合窗口，同样改用约束枚举。
func guardRunCandidates(rules []compiledRule, nativeWideMask bool) []bool {
	var promoted []bool
	for ruleIndex := range rules {
		run, ok := guardRunFor(ruleIndex, &rules[ruleIndex])
		if !ok {
			continue
		}
		// 集合自重复（见 guardRunRepeat）的候选枚举与逐起点确认等价，且比候选
		// 文字索引更省：文字索引按「候选文字出现的每个位置」登记，自重复则按
		// 连续段与重复上界收敛，还能让候选文字本身退化成一次集合掩码扫描。
		// 该判据与最短窗口、集合规模都无关，因此不再经过 guardRunOutshinesLiteral。
		if run.leftmostOnly || run.stride > 0 {
			// 有上界自重复在标量宽掩码后端上不划算：它要用一次全语料集合扫描换掉
			// 候选文字索引，而标量扫描比候选文字的 memchr 慢，净效果是负的
			// （实测 generic 后端 554.5 → 572.2 µs）。无上界自重复本来就没有候选
			// 文字索引可放弃，不受该判据影响。
			if run.stride > 0 && !nativeWideMask {
				continue
			}
			if promoted == nil {
				promoted = make([]bool, len(rules))
			}
			promoted[ruleIndex] = true
			continue
		}
		if !guardRunOutshinesLiteral(rules[ruleIndex]) {
			continue
		}
		if promoted == nil {
			promoted = make([]bool, len(rules))
		}
		promoted[ruleIndex] = true
	}
	return promoted
}

// guardRunOutshinesLiteral 判断前缀字节约束枚举是否比候选文字索引更省。
func guardRunOutshinesLiteral(rule compiledRule) bool {
	if !requiredIndexEligible(rule) || !requiredWindowUsable(rule.required) {
		return true
	}
	for _, variant := range rule.required {
		if len(variant.Value) > guardRunShortLiteralBytes {
			return false
		}
	}
	set, _, length, ok := rule.guard.longestEqualWindow()
	if !ok || length < guardRunPromoteWindow {
		return false
	}
	// 集合越大，同一窗口长度在自然文本里的命中越密：`[a-z]{9,}` 这类规则的窗口
	// 几乎覆盖英文日志里的每个单词，枚举出的候选远多于候选文字命中，此时保留
	// 候选文字索引更省。只有窄集合（数字、部分十六进制）才值得改用约束枚举。
	members := 0
	for _, word := range set {
		members += bits.OnesCount64(word)
	}
	if members <= guardRunPromoteCardinality {
		return true
	}
	return length >= guardRunPromoteLongWindow
}

// guardRunFor 判断规则能否用前缀字节约束枚举候选起点。约束必须能完整覆盖
// 规则的匹配前缀，且规则不允许空匹配，否则末尾起点会落在扫描范围之外。
func guardRunFor(ruleIndex int, rule *compiledRule) (guardRun, bool) {
	if rule.guard == nil || rule.root == nil || parser.Nullable(rule.root) {
		return guardRun{}, false
	}
	set, offset, length, ok := rule.guard.longestEqualWindow()
	if !ok {
		return guardRun{}, false
	}
	// 集合自重复（见 guardRunRepeat）不受最短窗口长度限制：它的候选枚举已经与
	// 逐起点确认等价，窗口只有 2 个字节也值得用一次掩码扫描换掉候选文字索引。
	leftmost, stride, selfRepeat := guardRunRepeat(rule)
	if length < guardRunMinLength && !selfRepeat {
		return guardRun{}, false
	}
	return guardRun{
		ruleIndex:     ruleIndex,
		set:           set,
		offset:        offset,
		length:        length,
		guard:         rule.guard,
		prefixCovered: offset == 0 && length == len(rule.guard.sets),
		leftmostOnly:  leftmost,
		stride:        stride,
	}, true
}

// guardRunRepeat 判断规则是否恰好是「约束集合的重复」，并给出候选收敛模式。
//
// 判据全部来自语法树与编译期固化的约束，不含运行期假设：
//   - 整条规则就是一个 Repeat{Class 或单字节 Literal, Min≥1, Greedy}，没有锚点、
//     组合、扩展参数、忽略大小写与 Unicode 语义；
//   - 约束集合与窗口逐字节一致，窗口从偏移 0 开始并覆盖约束的全部位置
//     （即 prefixCovered），所以段首起点必然满足前缀、必然命中；
//   - 没有 \b 折算出的相邻字节约束，否则段首起点仍可能被相邻字节拒绝；
//   - 调用方还需排除组合依赖（触发位置按全部命中累计）。
//
// 成立时按重复上界给出两种候选收敛方式：
//   - 上界为 -1（无上界贪婪，leftmost）：段首起点的贪婪匹配消费到段尾，同一条
//     连续段内更靠右的起点都落在同一规则的 blockedUntil 之内，逐起点确认原本
//     也只是「枚举 + 排序 + 跳过」，因此只需登记段首起点。实测把
//     `[[:alpha:]]{5,}` 在 64 KiB 固定语料上的候选起点从 14,024 压到 3,341。
//   - 上界为 M（有上界贪婪，stride=M）：段首起点命中后消费 min(M, 剩余段长) 个
//     字节并把 blockedUntil 推到该位置，所以段内被报告的起点恰好是
//     「段首 + k*M」且不超过 段尾-下限；按 M 为步长登记与整段逐起点登记得到
//     的命中集合逐位相同。实测把 `x{2,4}` 的候选从 2,338 压到 668，并顺带
//     让候选文字索引整个消失（一次全语料单字节 memchr 换成一次集合掩码扫描）。
func guardRunRepeat(rule *compiledRule) (leftmost bool, stride int, ok bool) {
	if rule.nonGreedy || rule.comb != nil || rule.ext != nil || rule.root == nil {
		return false, 0, false
	}
	// 忽略大小写必须同时排除编译参数与 scoped flags（`(?i)x{2,4}`）：折叠语义下
	// 重复集合会变宽，约束集合与重复集合的逐字节相等判据不再成立。
	if rule.flags&(CompileCaseless|CompileUTF8|CompileUCP|CompileQuiet) != 0 ||
		parser.HasScopedFlags(rule.root) || ruleCaseless(*rule) {
		return false, 0, false
	}
	if rule.guard == nil || len(rule.guard.sets) == 0 || rule.guard.hasBoundary() {
		return false, 0, false
	}
	repeatNode, ok := unwrapGroups(rule.root).(parser.Repeat)
	if !ok || repeatNode.Min < 1 || !repeatNode.Greedy {
		return false, 0, false
	}
	// 重复体可以是字符类，也可以是单字节字面量（`x{2,4}` 的语法树保留为
	// Literal，不会归一化成 Class），两者都折算成同一个字节集合比较。
	var set [4]uint64
	switch child := unwrapGroups(repeatNode.Child).(type) {
	case parser.Class:
		set = classByteSet(child)
	case parser.Literal:
		if len(child.Value) != 1 {
			return false, 0, false
		}
		set[child.Value[0]>>6] |= uint64(1) << (child.Value[0] & 63)
	default:
		return false, 0, false
	}
	if set == [4]uint64{} || repeatNode.Min != len(rule.guard.sets) {
		return false, 0, false
	}
	for index := range rule.guard.sets {
		if rule.guard.sets[index] != set {
			return false, 0, false
		}
	}
	_, offset, length, ok := rule.guard.longestEqualWindow()
	if !ok || offset != 0 || length != len(rule.guard.sets) {
		return false, 0, false
	}
	if repeatNode.Max < 0 {
		// 无上界贪婪：收敛到段首。窗口短于 guardRunMinLength 时省略整段展开
		// 本来就不划算，保持既有阈值。
		if length < guardRunMinLength {
			return false, 0, false
		}
		return true, 0, true
	}
	// 有上界贪婪：下界至少 2 个字节时按上界步长收敛。下界为 1 的规则在每个
	// 成员字节上都成立，收敛不会减少候选，只保留候选文字索引更稳。
	if repeatNode.Min < 2 || repeatNode.Max < repeatNode.Min {
		return false, 0, false
	}
	return false, repeatNode.Max, true
}

// denseRunHead 判断规则是否以「单个字节集合的无上界贪婪重复」`S+` 开头，且该
// 集合恰好等于规则首字节约束。返回空集合表示判据不成立。
//
// 解析器对 `[a-z]+@[a-z]+\.example` 这类前缀只推导出一段首字节约束：无上界重复
// 之后的字节位置不再确定，因此 guard.sets 恰为这一段。判据成立时，同一条连续段
// 上的任意起点都会让该重复贪婪消费到同一个段尾，后继程序从同一位置求值，命中
// 终点与整个求值结果都与起点无关。
//
// 「段内共享结果」只说明段内更靠右的起点冗余，并不说明它们必然被重叠抑制挡住：
// 前一次命中的结束位置可能落在段内（`[a-z]+\.x` 在 `x.xb.a.x` 上的第二次命中
// 起点就落在段内），此时水位线之后的段内起点仍必须确认。因此段首过滤只登记段首
// 起点，水位线处由 retryCollapsedCandidate 在确认阶段补齐。
//
// 为保证共享结果成立，除重复本身还要求：
//   - 下界恰为 1（`+`）且无上界、贪婪：下界更大时段尾附近不足下界的起点会单独
//     失败，共享结果不再成立；
//   - 重复之后紧跟的消费字节不在该集合内：贪婪消费到段尾后回溯必然失败（回退
//     位置仍属于集合），后继求值位置因此固定为段尾；
//   - 重复之后紧跟的必须是一个消费单字节的字面量或字符类，断言、分组、可选重复
//     都会让后继求值依赖起点；
//   - 无尾随 `\b` 折算出的相邻字节约束、无可变宽度的后继重复、无回溯引用与条件
//     分支、无组合与扩展参数、无忽略大小写与 Unicode 语义。
func denseRunHead(rule *compiledRule) [4]uint64 {
	var empty [4]uint64
	if rule.guard == nil || len(rule.guard.sets) != 1 || rule.guard.hasBoundary() {
		return empty
	}
	if rule.nonGreedy || rule.comb != nil || rule.ext != nil || rule.root == nil ||
		rule.hasBackref || rule.hasConditional {
		return empty
	}
	if rule.flags&(CompileCaseless|CompileUTF8|CompileUCP|CompileQuiet) != 0 ||
		parser.HasScopedFlags(rule.root) || ruleCaseless(*rule) {
		return empty
	}
	sequence, ok := unwrapGroups(rule.root).(parser.Sequence)
	if !ok || len(sequence.Elements) < 2 {
		return empty
	}
	repeatNode, ok := unwrapGroups(sequence.Elements[0]).(parser.Repeat)
	if !ok || repeatNode.Min != 1 || repeatNode.Max >= 0 || !repeatNode.Greedy {
		return empty
	}
	set, ok := singleByteNodeSet(repeatNode.Child)
	if !ok || set == empty || set != rule.guard.sets[0] {
		return empty
	}
	next, ok := firstByteNodeSet(sequence.Elements[1])
	if !ok || next == empty || !byteSetDisjoint(next, set) {
		return empty
	}
	return set
}

// firstByteNodeSet 返回节点必然消费的第一个字节的取值集合；节点可能不消费字节或
// 首字节位置不确定时返回 false。后继字面量被归一化合并成多字节（`\.x` 合并为
// `.x`）时只取首字节：回溯必然在属于重复集合的位置上匹配它，首字节不在集合内
// 就足以排除回溯。
func firstByteNodeSet(node parser.Node) ([4]uint64, bool) {
	switch value := unwrapGroups(node).(type) {
	case parser.Class:
		return classByteSet(value), true
	case parser.Literal:
		if len(value.Value) == 0 {
			return [4]uint64{}, false
		}
		return singleByteSet(value.Value[0]), true
	}
	return [4]uint64{}, false
}

// singleByteNodeSet 返回节点恰好消费一个字节时的字节集合；节点不是单字节消费时
// 返回 false。重复体必须恰好一个字节：多字节字面量重复的连续段判定是另一套形态。
func singleByteNodeSet(node parser.Node) ([4]uint64, bool) {
	if value, ok := unwrapGroups(node).(parser.Literal); ok && len(value.Value) != 1 {
		return [4]uint64{}, false
	}
	return firstByteNodeSet(node)
}

// byteSetDisjoint 判断两个字节集合是否没有交集。
func byteSetDisjoint(left, right [4]uint64) bool {
	for index := range left {
		if left[index]&right[index] != 0 {
			return false
		}
	}
	return true
}

// unwrapGroups 剥掉语法树外层不影响语义的分组节点。
func unwrapGroups(node parser.Node) parser.Node {
	for {
		group, ok := node.(parser.Group)
		if !ok {
			return node
		}
		node = group.Child
	}
}

// groupGuardRuns 按字节集合合并约束，使同集合的规则共享一次线性扫描。
func groupGuardRuns(runs []guardRun) []guardRunGroup {
	if len(runs) == 0 {
		return nil
	}
	groups := make([]guardRunGroup, 0, len(runs))
	for _, run := range runs {
		placed := -1
		for index := range groups {
			if groups[index].set == run.set {
				placed = index
				groups[index].runs = append(groups[index].runs, run)
				break
			}
		}
		if placed < 0 {
			groups = append(groups, guardRunGroup{
				set:    run.set,
				tables: simd.NewByteSetTables([4][4]uint64{run.set}),
				runs:   []guardRun{run},
			})
			continue
		}
	}
	for index := range groups {
		slices.SortFunc(groups[index].runs, func(left, right guardRun) int {
			return left.length - right.length
		})
		groups[index].minLength = groups[index].runs[0].length
	}
	return groups
}

// requiredIndexDriven 判断本次扫描是否存在候选起点索引（必须文字或前缀字节
// 约束）。任一来源生效时都必须走候选驱动路径，否则这两类规则会退化成整块
// 逐起点确认。判定只依赖构造期固化的字段，因此在确认规则集合构建之前即可用。
func (scanner *Scanner) requiredIndexDriven() bool {
	return scanner.requiredFindInto != nil || scanner.guardRunCovered != nil || len(scanner.anchorEntries) > 0
}

// guardImpliedByBack 判断前缀约束是否只校验首字节、且该字节集合与左侧回退集合
// 完全一致。此时窗口内的每个候选起点都已由回退扫描保证了首字节归属，逐点查表
// 只是重复开销，可以整条约束直接省略。
func guardImpliedByBack(guard *prefixGuard, back []byte, minOffset int) bool {
	// 起点可以落在文字命中位置（minOffset 为 0）时，首字节就是文字本身，
	// 不一定属于左侧集合，此时约束不能省略。首尾断言折算出的相邻字节约束
	// 与左侧回退无关，携带这类约束时同样不能整条省略。
	if guard == nil || back == nil || len(guard.sets) != 1 || minOffset < 1 || guard.hasBoundary() {
		return false
	}
	set := guard.sets[0]
	for value := range 256 {
		member := byte(0)
		if set[value>>6]&(uint64(1)<<(uint(value)&63)) != 0 {
			member = 1
		}
		if member != back[value] {
			return false
		}
	}
	return true
}

// backSetContains 判断回退字节集合是否覆盖给定的字节集合。回退集合非空时窗口
// 左侧最多收缩到命中位置左侧第一个不属于回退集合的字节之后，因此只有覆盖重复
// 集合才能保证窗口下界不落在连续段内部。
func backSetContains(back []byte, set [4]uint64) bool {
	if back == nil {
		return true
	}
	for value := range 256 {
		if set[value>>6]&(uint64(1)<<(uint(value)&63)) != 0 && back[value] != 1 {
			return false
		}
	}
	return true
}

// newCollapseHead 推导「同一次文字命中摊开的候选起点共享确认结果」所需的集合
// 重复描述。条件不成立时返回 nil，确认程序照常逐起点执行。
func newCollapseHead(rule compiledRule) *collapseHead {
	confirm := rule.confirm
	// confirmSkip > 0 时求值从入口文字之后开始，窗口推导不适用。
	if confirm == nil || rule.confirmSkip > 0 || len(rule.required) != 1 || rule.guard == nil {
		return nil
	}
	variant := rule.required[0]
	// 前缀约束必须完全由窗口左侧的回退扫描保证，否则窗口内不同起点的前缀
	// 成立情况可能不同，最左起点失败而更靠右的起点命中。
	if !guardImpliedByBack(rule.guard, variant.Back, variant.MinOffset) {
		return nil
	}
	entry := confirm.entry
	if entry < 0 || int(entry) >= len(confirm.instrs) {
		return nil
	}
	instr := confirm.instrs[entry]
	if instr.op != confirmOpSetRepeat {
		return nil
	}
	rep := confirm.repeats[instr.index]
	if rep.min < 0 || rep.max < 0 {
		return nil
	}
	// 重复必须与候选窗口一一对应：窗口就是重复的所有合法消费长度。
	if int(rep.min) != variant.MinOffset || int(rep.max) != variant.MaxOffset {
		return nil
	}
	set := &confirm.sets[rep.set]
	head := variant.Value[0]
	if set.match(head) {
		// 入口文字首字节属于重复集合时重复不会停在命中位置，窗口不再成立。
		return nil
	}
	// 回退集合必须与重复集合逐字节一致：窗口内的候选起点都由回退扫描保证
	// 首字节落在重复集合内，此时重复消费长度才恰好等于命中位置减起点。
	for value := range 256 {
		member := byte(0)
		if set.match(byte(value)) {
			member = 1
		}
		if member != variant.Back[value] {
			return nil
		}
	}
	return &collapseHead{set: set, min: int(rep.min), max: int(rep.max)}
}

// runEnd 返回从 start 起连续属于重复集合的第一个位置。连续段长于重复上界时重复
// 会截断在上界处，尾程序起点随候选起点变化，因此返回 -1 表示该窗口不可复用。
func (head *collapseHead) runEnd(data []byte, start int) int {
	pos := start
	for pos < len(data) && pos-start < head.max && head.set.match(data[pos]) {
		pos++
	}
	if pos < len(data) && pos-start == head.max && head.set.match(data[pos]) {
		return -1
	}
	return pos
}

// requiredIndexCovered 判断规则的候选起点是否已由候选索引（必须文字或前缀
// 字节约束）枚举，覆盖的规则不再参与整块后端扫描。
func (scanner *Scanner) requiredIndexCovered(ri int) bool {
	if scanner.requiredCovered != nil && ri < len(scanner.requiredCovered) && scanner.requiredCovered[ri] {
		return true
	}
	if scanner.anchorCovered != nil && ri < len(scanner.anchorCovered) && scanner.anchorCovered[ri] {
		return true
	}
	return scanner.guardRunCovers(ri)
}

// literalIndexCovered 判断规则的候选起点是否由必须文字索引枚举。前缀字节约束
// 枚举只校验约束窗口，不校验规则的入口文字，因此这类规则不能跳过确认程序的
// 入口指令，否则会在窗口命中但入口文字不匹配的位置产生误报。
func (scanner *Scanner) literalIndexCovered(ri int) bool {
	return scanner.requiredCovered != nil && ri < len(scanner.requiredCovered) && scanner.requiredCovered[ri]
}

// guardRunCovers 判断规则是否由前缀字节约束枚举候选起点。
func (scanner *Scanner) guardRunCovers(ri int) bool {
	return scanner.guardRunCovered != nil && ri < len(scanner.guardRunCovered) && scanner.guardRunCovered[ri]
}

// markRequiredEntrySkips 把“该规则已由整块后端扫描产出结果”的判定结果固化到
// 候选条目上。判定只依赖构造期确定的扫描计划，扫描热路径因此不必对每个候选
// 重新求值。
func (scanner *Scanner) markRequiredEntrySkips() {
	requiredDriven := scanner.requiredIndexDriven()
	for literalIndex := range scanner.requiredLiterals {
		entries := scanner.requiredLiterals[literalIndex].entries
		for entryIndex := range entries {
			entries[entryIndex].skip = scanner.backendScanned(entries[entryIndex].ruleIndex, scanner.backendOnly, requiredDriven)
		}
	}
}

// backendScanned 判断规则是否由整块后端扫描产出结果。后端扫描只在 backendOnly
// 下执行，并且会跳过已被候选索引覆盖的规则，避免同一规则产出重复事件。
func (scanner *Scanner) backendScanned(ri int, backendOnly, requiredDriven bool) bool {
	if !backendOnly || !scanner.rules[ri].backendEligible {
		return false
	}
	return !(requiredDriven && scanner.requiredIndexCovered(ri))
}

// confirmArena 返回该规则可用于字节链快路径的工作区；返回 nil 表示规则
// 携带捕获、扩展参数或 UTF-8 语义，必须走通用确认路径。
func (st *blockScanState) confirmArena(rule *compiledRule) *byteChainArena {
	if rule.hasCapture || rule.ext != nil || rule.flags&(CompileUTF8|CompileUCP) != 0 {
		return nil
	}
	return &st.ctx.chainArena
}

// verify 在指定起点确认单条规则；返回 false 表示报告已停止，扫描应立即结束。
func (st *blockScanState) verify(ri int, start int) bool {
	rule := &st.scanner.rules[ri]
	if !st.scanner.simpleReports && st.ctx.Reports.Stopped() {
		return false
	}
	if rule.comb != nil {
		return true
	}
	if st.fired[ri] {
		return true
	}
	if start < st.blockedUntil[ri] {
		return true
	}
	if rule.flags&CompilePrefilter != 0 && len(rule.prefilter) > 0 {
		if starts, indexed := st.prefilterStarts[rule.id]; indexed {
			if _, ok := starts[start]; !ok {
				return true
			}
		} else if start+len(rule.prefilter) > len(st.data) || !hwlm.Contains(st.data[start:start+len(rule.prefilter)], hwlm.Literal{Value: rule.prefilter, CaseInsensitive: rule.flags&CompileCaseless != 0}) {
			return true
		}
	}
	// 共享文字索引已经把精确文字规则的结束偏移算出来，直接使用即可。
	if st.scanner.indexedRules[ri] && st.literalEnds != nil {
		end, ok := st.literalEnds[start][rule.id]
		if !ok {
			return true
		}
		if st.scanner.exactRules[ri] {
			var only [1]int
			only[0] = end
			return st.record(ri, start, only[:1])
		}
	}
	// 确认程序直接给出 record 唯一使用的偏好结束偏移，命中密集时跳过
	// 通用 AST 求值的逐节点切片与排序。
	if rule.confirm != nil && rule.ext == nil {
		var end int
		var ok bool
		if head := rule.collapseHead; head != nil {
			memo := &st.ctx.collapseWindows[ri]
			if start >= memo.from && start <= memo.to {
				// 同一窗口内的候选起点由同一个集合重复派生，重复必然停在同一个
				// 入口文字命中位置，之后的程序与起点无关，直接复用求值结果。
				end, ok = memo.end, true
			} else {
				end, ok = st.confirmEnd(ri, start)
				st.recordCollapseWindow(head, ri, start, end, ok)
			}
		} else {
			end, ok = st.confirmEnd(ri, start)
		}
		if ok {
			// simpleReports 已排除组合、SOM、静默与单次语义，且扩展参数规则
			// 不走确认程序，命中可以直接写入结果并抑制重叠。
			if st.scanner.simpleReports {
				if end >= 0 {
					st.direct = append(st.direct, Match{Id: rule.id, From: uint64(start), To: uint64(end)})
					if end > start {
						st.blockedUntil[ri] = end
					}
				}
				return true
			}
			var only [1]int
			if end < 0 {
				return st.record(ri, start, only[:0])
			}
			only[0] = end
			return st.record(ri, start, only[:1])
		}
	}
	arena := st.confirmArena(rule)
	ends := matchRuleIntoArena(rule, st.data, start, st.ctx.ruleMatchBuf[:0], arena, &st.ctx.nodeEval)
	st.ctx.ruleMatchBuf = ends
	return st.record(ri, start, ends)
}

// confirmEnd 运行规则的确认程序并返回偏好结束偏移。
func (st *blockScanState) confirmEnd(ri int, start int) (int, bool) {
	rule := &st.scanner.rules[ri]
	// 候选由必须文字索引派生时，起点处的入口文字已由匹配器确认，可以直接从
	// 入口文字之后的指令开始求值。这条捷径比确定性表更省：解释器整段跳过前缀，
	// 而表必须从候选起点逐字节重新走一遍，所以该捷径可用时优先保留解释器。
	if rule.confirmSkip > 0 && st.requiredStartDriven && st.scanner.literalIndexCovered(ri) {
		// 入口之后立刻命中（纯文字规则）：候选索引已经证明起点处的入口文字成立，
		// 结束偏移恒为 start+confirmSkip，连解释器调用都可以省掉。
		if rule.confirmEntry == confirmExactEntry {
			return start + rule.confirmSkip, true
		}
		if starts := st.scanner.confirmDFAStarts; starts != nil && starts[ri].ok {
			return rule.confirmDFA.preferredEndFrom(st.data, start+rule.confirmSkip, starts[ri])
		}
		return rule.confirm.run(st.data, rule.confirmEntry, start+rule.confirmSkip, rule.flags, st.ctx.confirmFrames())
	}
	// 断言敏感确认表把单次确认压缩成每字节一次表查找，命中密集时远快于
	// 解释执行确认程序；两者语义等价，结果同样只作为偏好结束偏移。
	if rule.confirmDFA != nil {
		return rule.confirmDFA.preferredEnd(st.data, start)
	}
	return rule.confirm.preferredEnd(st.data, start, rule.flags, st.ctx.confirmFrames())
}

// recordCollapseWindow 把一次「未命中」的确认程序求值结果登记为可复用窗口。
//
// 只在程序于预算内正常返回且没有命中（ok 为 true、end 为负）时登记：超限回退通用
// 求值的结果依赖起点，不能跨起点复用；而命中时 record 会把 blockedUntil 推到匹配
// 结束位置，同一窗口里更靠右的起点在候选取值阶段就被重叠抑制，缓存不会带来任何
// 收益，登记反而要多付一次连续段扫描。窗口右界由集合重复的结束位置决定：起点落在
// [start, stop-min] 内时重复消费长度仍满足上下界，尾程序起点保持不变。
func (st *blockScanState) recordCollapseWindow(head *collapseHead, ri int, start int, end int, ok bool) {
	memo := &st.ctx.collapseWindows[ri]
	if !ok || end >= 0 {
		memo.to = -1
		return
	}
	stop := head.runEnd(st.data, start)
	if stop < 0 {
		memo.to = -1
		return
	}
	to := stop - head.min
	if to < start {
		memo.to = -1
		return
	}
	memo.from = start
	memo.to = to
	memo.end = end
}

// record 处理已确认的结束偏移：约束过滤、报告写入与重叠抑制。
func (st *blockScanState) record(ri int, start int, ends []int) bool {
	rule := &st.scanner.rules[ri]
	end := -1
	// 结束偏移已按升序排序，直接取边界值即可获得贪婪/非贪婪的首选结束。
	if rule.nonGreedy {
		if len(ends) > 0 {
			end = ends[0]
		}
	} else if len(ends) > 0 {
		end = ends[len(ends)-1]
	}
	if end < 0 || end > len(st.data) {
		return true
	}
	if rule.ext != nil {
		if !rule.ext.OffsetAllowed(uint64(end)) {
			return true
		}
		if !rule.ext.LengthAllowed(uint64(end - start)) {
			return true
		}
	}
	if rule.flags&CompileSOMLeftmost != 0 {
		if st.somSeen == nil {
			st.somSeen = make(map[uint32]map[int]struct{})
		}
		seenEnds := st.somSeen[rule.id]
		if seenEnds == nil {
			seenEnds = make(map[int]struct{})
			st.somSeen[rule.id] = seenEnds
		}
		if _, exists := seenEnds[end]; exists {
			return true
		}
		seenEnds[end] = struct{}{}
	}
	if st.scanner.simpleReports {
		// 没有组合依赖与 SOM/单次/静默语义时，事件不会重复也不需要排序，
		// 直接产出结果即可跳过报告管理器的去重表与收尾拷贝。
		st.direct = append(st.direct, Match{Id: rule.id, From: uint64(start), To: uint64(end)})
		if end > start {
			st.blockedUntil[ri] = end
		}
		return true
	}
	if st.scanner.hasCombo {
		st.comboTriggers = append(st.comboTriggers, report.Event{ID: rule.id, From: uint64(start), To: uint64(end), Flags: uint32(rule.flags)})
	}
	quiet := rule.flags&CompileQuiet != 0
	if quiet {
		if rule.flags&CompileSingleMatch != 0 {
			st.fired[ri] = true
		}
		return true
	}
	st.ctx.Reports.Add(report.Event{ID: rule.id, From: uint64(start), To: uint64(end), SOM: uint64(start), Flags: uint32(rule.flags)})
	if end > start {
		st.blockedUntil[ri] = end
	}
	if rule.flags&CompileSingleMatch != 0 {
		st.fired[ri] = true
	}
	return true
}

// finish 收尾组合报告并将事件转换为结果切片。
func (st *blockScanState) finish(matches []Match) ([]Match, error) {
	if st.scanner.simpleReports {
		out := append(matches, st.direct...)
		// 直接结果缓冲与候选起点一致，按绝对上限保留；无上限保留会让后续小输入
		// 长期背着大块内存。
		if retainDirectMatches(cap(st.direct)) {
			st.ctx.directMatches = st.direct[:0]
		} else {
			st.ctx.directMatches = nil
		}
		return out, nil
	}
	if st.scanner.hasCombo {
		st.scanner.emitCombinationReports(st.ctx.Reports, st.comboTriggers, uint64(len(st.data)))
	}
	st.ctx.comboTriggers = st.comboTriggers
	for _, event := range st.ctx.Reports.Events() {
		matches = append(matches, Match{Id: event.ID, From: event.From, To: event.To})
	}
	return matches, nil
}

// requiredScanStarts 把候选文字命中展开为按起点升序的 (起点, 规则) 组合。
// 返回 false 表示候选数量异常膨胀，调用方应退回逐起点确认。
func (scanner *Scanner) requiredScanStarts(ctx *scanContext, data []byte) ([]uint64, bool) {
	starts := ctx.requiredStarts[:0]
	limit := 16*len(data) + 4096
	// 锚定在输入首尾的规则只有一个可行起点（\A 为 0，\z 为 len(data) 减去
	// 锚点之前的固定消费字节数），直接登记该起点，不依赖候选文字命中。
	for _, entry := range scanner.anchorEntries {
		start, ok := entry.pinnedStart(len(data))
		if !ok {
			continue
		}
		if len(starts) >= limit {
			return nil, false
		}
		starts = append(starts, uint64(uint32(start))<<32|uint64(uint32(entry.ruleIndex)))
	}
	hits := []requiredHit(nil)
	if scanner.requiredFindInto != nil {
		hits = scanner.requiredFindInto(data, ctx.requiredHits[:0])
		ctx.requiredHits = hits
	}
	for _, hit := range hits {
		literal := scanner.requiredLiterals[hit.slot]
		for index := range literal.entries {
			entry := &literal.entries[index]
			if entry.skip {
				continue
			}
			// 偏移上界为负表示无上界，此时起点只受数据左边界约束。
			from, to := 0, hit.pos-entry.min
			if entry.max >= 0 {
				from = max(hit.pos-entry.max, 0)
			}
			if to < 0 || to > len(data) {
				continue
			}
			if entry.back != nil {
				left := hit.pos - 1
				edge := 0
				if entry.max >= 0 {
					edge = hit.pos - entry.max
				}
				for left >= 0 && left >= edge && entry.back[data[left]] == 1 {
					left--
				}
				left++
				if left > from {
					from = left
				}
			}
			if from > to {
				continue
			}
			ruleKey := uint64(uint32(entry.ruleIndex))
			if to == from {
				// 窗口退化为单个起点，前缀约束在起点位置上必然成立，查表只是
				// 额外常数开销。
				if len(starts) >= limit {
					return nil, false
				}
				starts = append(starts, uint64(uint32(from))<<32|ruleKey)
				continue
			}
			if entry.collapseWindow {
				// 窗口内所有起点的确认结果相同，只登记最左起点：更靠右的起点要么
				// 被同一规则的重叠抑制挡住，要么由 retryCollapsedCandidate 推迟
				// 到 blockedUntil 位置重新登记。登记整段窗口只会让同一份确认结果
				// 反复经过排序与候选循环，实测让 Email 单规则的 HighMatch 场景
				// 多付出一倍以上的确认前开销。
				if len(starts) >= limit {
					return nil, false
				}
				starts = append(starts, uint64(uint32(from))<<32|ruleKey)
				continue
			}
			// 窗口较宽时同一次文字命中要摊开成多个起点，其中绝大多数连规则
			// 前缀都过不了；先做一次约束查表，避免为它们建立候选并启动确认程序。
			guard := entry.guard
			runHead := entry.runHead
			for start := from; start <= to; start++ {
				if len(starts) >= limit {
					return nil, false
				}
				if !guard.allows(data, start) {
					continue
				}
				// 规则以无上界贪婪重复开头时，同一连续段内更靠右的起点与段首
				// 共享确认结果（见 denseRunHead），只会被段首命中的重叠抑制挡住。
				if runHead != ([4]uint64{}) && start > 0 {
					prev := data[start-1]
					if runHead[prev>>6]&(uint64(1)<<(prev&63)) != 0 {
						continue
					}
				}
				starts = append(starts, uint64(uint32(start))<<32|ruleKey)
			}
		}
	}
	// 前缀字节约束枚举出的起点本身与时序无关，但确认阶段必要时要与首字节索引
	// 归并，因此必须和候选文字路径一样整理成 (起点, 规则) 升序；缺少这一步会
	// 让归并游标错过靠前的约束候选，直接漏报。
	if len(scanner.guardRunGroups) > 0 {
		var ok bool
		starts, ok = scanner.appendGuardRunStarts(data, starts, limit)
		if !ok {
			return nil, false
		}
	}
	ctx.requiredStarts = starts
	if len(starts) < 2 {
		return starts, true
	}
	scanner.sortRequiredStarts(ctx, starts, len(data))
	deduped := starts[:1]
	for _, entry := range starts[1:] {
		if entry == deduped[len(deduped)-1] {
			continue
		}
		deduped = append(deduped, entry)
	}
	ctx.requiredStarts = deduped
	return deduped, true
}

// appendGuardRunStarts 为只能由前缀字节约束定位的规则枚举候选起点。
// 约束在 [offset, offset+length) 上是同一字节集合，同集合的规则共享一次扫描：
// 先求出全部极大连续段，再把落在段内的窗口起点整段展开。
func (scanner *Scanner) appendGuardRunStarts(data []byte, starts []uint64, limit int) ([]uint64, bool) {
	backend := dispatch.DefaultBackend()
	for index := range scanner.guardRunGroups {
		var ok bool
		starts, ok = scanner.guardRunGroups[index].appendStarts(backend, data, starts, limit)
		if !ok {
			return nil, false
		}
	}
	return starts, true
}

// appendStarts 用宽窗口字节集合掩码求出该组的全部极大连续段，再把窗口完整落在
// 连续段内的起点整段展开。连续段由掩码的"段起点位"与"段终点位"配对得到，跨窗口
// 的唯一一段用 runStart 接力；不足一个宽窗口的尾部回退成逐字节求掩码后走同一套
// 配对逻辑，因此结果与完全逐字节的实现逐位一致。
func (group *guardRunGroup) appendStarts(backend simd.Backend, data []byte, starts []uint64, limit int) ([]uint64, bool) {
	emitter := guardRunEmitter{starts: starts, limit: limit, data: data}
	window := guardRunWindow{group: group, emitter: &emitter, runStart: -1}
	off := 0
	for ; off+simd.WideWidth <= len(data); off += simd.WideWidth {
		mask, ok := backend.WindowMask64(data, off, &group.tables, 1)
		if !ok {
			return group.appendStartsScalar(data, starts, limit)
		}
		edgeIn := off > 0 && group.member(data[off-1])
		edgeOut := off+simd.WideWidth < len(data) && group.member(data[off+simd.WideWidth])
		window.process(off, simd.WideWidth, mask, edgeIn, edgeOut)
		if emitter.overflow {
			return nil, false
		}
	}
	if off < len(data) {
		width := len(data) - off
		var mask uint64
		for index := range width {
			if group.member(data[off+index]) {
				mask |= uint64(1) << uint(index)
			}
		}
		window.process(off, width, mask, off > 0 && group.member(data[off-1]), false)
	}
	if window.runStart >= 0 {
		window.close(len(data))
	}
	if emitter.overflow {
		return nil, false
	}
	return emitter.starts, true
}

// guardRunEmitter 在宽窗口扫描期间累计候选起点。切片头保存在结构体字段里而不是
// 闭包捕获的局部变量，追加时不必每次穿透一层间接寻址。
type guardRunEmitter struct {
	starts   []uint64
	limit    int
	overflow bool
	data     []byte
}

// emitRun 把一个连续段 [from, to) 覆盖的窗口起点整段展开成候选键。
func (emitter *guardRunEmitter) emitRun(run *guardRun, from, to int) {
	// 窗口 [start+offset, start+offset+length) 必须落在 [from, to) 内。
	first := max(from-run.offset, 0)
	last := to - run.length - run.offset
	count := last - first + 1
	if count <= 0 {
		return
	}
	if run.leftmostOnly {
		// 段首起点必命中且贪婪重复消费到段尾（见 guardRunRepeat），段内其它
		// 起点都落在同一条规则的 blockedUntil 内，登记它们只会付出排序与
		// 候选循环的固定开销。
		if len(emitter.starts) >= emitter.limit {
			emitter.overflow = true
			return
		}
		emitter.starts = append(emitter.starts, requiredStartKey(first, run.ruleIndex))
		return
	}
	if run.stride > 0 {
		// 有上界贪婪重复：段内被报告的起点只可能是「段首 + k*上界」（见
		// guardRunRepeat），按步长展开与整段展开得到同一批命中。
		count = (last-first)/run.stride + 1
		if len(emitter.starts)+count > emitter.limit {
			emitter.overflow = true
			return
		}
		starts := emitter.starts
		if cap(starts)-len(starts) < count {
			starts = slices.Grow(starts, count)
		}
		base := len(starts)
		starts = starts[:base+count]
		ruleKey := uint64(uint32(run.ruleIndex))
		for index := range count {
			starts[base+index] = uint64(uint32(first+index*run.stride))<<32 | ruleKey
		}
		emitter.starts = starts
		return
	}
	if run.needsGuardFilter() {
		emitter.emitFiltered(run, first, last, run.prefixCovered)
		return
	}
	if len(emitter.starts)+count > emitter.limit {
		emitter.overflow = true
		return
	}
	starts := emitter.starts
	if cap(starts)-len(starts) < count {
		starts = slices.Grow(starts, count)
	}
	base := len(starts)
	starts = starts[:base+count]
	ruleKey := uint64(uint32(run.ruleIndex))
	for index := range count {
		starts[base+index] = uint64(uint32(first+index))<<32 | ruleKey
	}
	emitter.starts = starts
}

// needsGuardFilter 判断约束窗口是否没有覆盖完整前缀约束。窗口落在前缀内部或
// 短于前缀时，窗口之外的固定位置仍可能拒绝候选，需要在展开时逐起点校验。
func (run *guardRun) needsGuardFilter() bool {
	if run.guard == nil {
		return false
	}
	return run.offset > 0 || len(run.guard.sets) > run.length || run.guard.hasBoundary()
}

// emitFiltered 逐起点通过完整前缀约束后追加候选键；窗口命中的起点只有少数
// 真正满足完整前缀，逐点过滤比整段展开再确认省得多。boundaryOnly 表示窗口
// 已覆盖全部前缀集合，只需校验首尾断言折算出的相邻字节约束。
func (emitter *guardRunEmitter) emitFiltered(run *guardRun, first, last int, boundaryOnly bool) {
	starts := emitter.starts
	ruleKey := uint64(uint32(run.ruleIndex))
	for start := first; start <= last; start++ {
		allowed := false
		if boundaryOnly {
			allowed = run.guard.allowsBoundary(emitter.data, start)
		} else {
			allowed = run.guard.allows(emitter.data, start)
		}
		if !allowed {
			continue
		}
		if len(starts) >= emitter.limit {
			emitter.overflow = true
			emitter.starts = starts
			return
		}
		starts = append(starts, uint64(uint32(start))<<32|ruleKey)
	}
	emitter.starts = starts
}

// guardRunWindow 把掩码窗口配对出的极大连续段转换为候选起点。掩码窗口的 bit i
// 对应 data[off+i] 是否属于集合，edgeIn/edgeOut 表示窗口两侧相邻字节的归属。
type guardRunWindow struct {
	group    *guardRunGroup
	emitter  *guardRunEmitter
	runStart int
}

// process 处理一个宽度为 width 的掩码窗口。
func (window *guardRunWindow) process(off, width int, mask uint64, edgeIn, edgeOut bool) {
	if window.skipPairing(off, width, mask, edgeOut) {
		return
	}
	beginBits := mask &^ (mask << 1)
	if edgeIn {
		beginBits &^= 1
	}
	endBits := mask &^ (mask >> 1)
	if edgeOut {
		endBits &^= uint64(1) << uint(width-1)
	}
	if window.runStart >= 0 && endBits != 0 {
		endBit := bits.TrailingZeros64(endBits)
		endBits &^= uint64(1) << uint(endBit)
		window.close(off + endBit + 1)
	}
	if window.runStart >= 0 {
		return
	}
	for beginBits != 0 {
		beginBit := bits.TrailingZeros64(beginBits)
		beginBits &^= uint64(1) << uint(beginBit)
		if endBits == 0 {
			window.runStart = off + beginBit
			return
		}
		endBit := bits.TrailingZeros64(endBits)
		endBits &^= uint64(1) << uint(endBit)
		// 段长不足组内最短窗口时没有任何起点成立，直接跳过展开调用。
		if endBit-beginBit+1 < window.group.minLength {
			continue
		}
		window.emitRange(off+beginBit, off+endBit+1)
	}
}

// skipPairing 在没有"长度达标的连续段"时跳过整段配对循环。
//
// 配对循环逐段枚举"段起点位/段终点位"，日志语料上绝大多数窗口只有单词长度的
// 短段，逐段枚举的代价（实测占整次扫描约 21%）几乎全部花在必然会被
// minLength 判据挡回的迭代上。这里先用一次常数级判定给出同样的结论：
//
//  1. 窗口内不存在完全落在窗口里的 minLength 连续成员（必要条件即可，
//     跨窗口接力的部分由第 2 条覆盖）；
//  2. 从窗口外接力进来的连续段即使在本窗口结束也够不到 minLength。
//
// 两条同时成立时，配对循环既不会展开任何连续段，也不会产出候选，只在
// 接力状态上留下"窗口尾部是否还有未结束的连续段"这一个副作用，因此这里
// 直接把它算出来。判定与配对循环逐窗口等价，随机差分测试
// （TestGuardRunSkipPairingMatchesScalar）以标量实现为参照覆盖该等价性。
func (window *guardRunWindow) skipPairing(off, width int, mask uint64, edgeOut bool) bool {
	minLength := window.group.minLength
	carry := 0
	if window.runStart >= 0 {
		carry = off - window.runStart
	}
	lead := min(bits.TrailingZeros64(^mask), width)
	if carry+lead >= minLength {
		return false
	}
	if hasGuardRun(mask, minLength) {
		return false
	}
	trail := min(bits.LeadingZeros64(^(mask << uint(64-width))), width)
	if trail == 0 || !edgeOut {
		// 段在窗口内结束（或本窗口就是数据尾部）：接力状态清空。
		window.runStart = -1
		return true
	}
	if carry > 0 && lead == width {
		// 整个窗口都属于同一条接力段，起点保持不变。
		return true
	}
	window.runStart = off + width - trail
	return true
}

// hasGuardRun 判断掩码中是否存在 minLength 个连续的置位（即窗口内完整落下的
// minLength 长连续段）。判定用倍增位移完成：每步把已覆盖的连续长度从 covered
// 增加到 covered+shift，循环结束时掩码的置位位恰好是"以该位开头有 minLength
// 个连续置位"的位置。
func hasGuardRun(mask uint64, minLength int) bool {
	covered := 1
	for covered < minLength {
		shift := covered
		if covered+shift > minLength {
			shift = minLength - covered
		}
		mask &= mask >> uint(shift)
		if mask == 0 {
			return false
		}
		covered += shift
	}
	return mask != 0
}

// close 结束跨窗口接力的连续段。
func (window *guardRunWindow) close(end int) {
	window.emitRange(window.runStart, end)
	window.runStart = -1
}

// emitRange 把 [from, to) 段内所有成立的窗口起点交给同组的每条规则。组内规则按
// 窗口长度升序排列，连续段长度不足时后面的规则只会更长，可以立即结束整段展开，
// 省掉大量必然返回的逐规则调用。
func (window *guardRunWindow) emitRange(from, to int) {
	runs := window.group.runs
	length := to - from
	for index := range runs {
		run := &runs[index]
		if run.length > length {
			break
		}
		window.emitter.emitRun(run, from, to)
	}
}

// appendStartsScalar 是不支持宽窗口掩码的后端上的回退实现：逐字节求出极大连续段。
func (group *guardRunGroup) appendStartsScalar(data []byte, starts []uint64, limit int) ([]uint64, bool) {
	runStart := -1
	for index := 0; index <= len(data); index++ {
		if index < len(data) && group.member(data[index]) {
			if runStart < 0 {
				runStart = index
			}
			continue
		}
		if runStart < 0 {
			continue
		}
		for _, run := range group.runs {
			first := max(runStart-run.offset, 0)
			last := index - run.length - run.offset
			if first > last {
				continue
			}
			if run.leftmostOnly {
				if len(starts) >= limit {
					return nil, false
				}
				starts = append(starts, requiredStartKey(first, run.ruleIndex))
				continue
			}
			step := 1
			if run.stride > 0 {
				step = run.stride
			}
			for start := first; start <= last; start += step {
				if len(starts) >= limit {
					return nil, false
				}
				starts = append(starts, requiredStartKey(start, run.ruleIndex))
			}
		}
		runStart = -1
	}
	return starts, true
}

// directMatchPrealloc 是候选驱动路径上直接结果切片的预分配上限：超过该
// 数量时改为按实际命中增长，避免稀疏语料按候选数量预留大块内存。
const directMatchPrealloc = 1 << 16

// 候选起点线性排序的规模上限。
//
// 候选数低于 requiredCountingSortMin 时比较排序的常数更小，直接退回比较排序；
// 高于 requiredCountingSortMaxKeys 时暂存区已经超过与候选起点缓冲同量级的内存
// 预算，同样退回比较排序。计数表另受 requiredCountingSortMaxBuckets 限制，
// 桶跨度按候选密度自适应放大，因此语料再长也不会让计数表跟着语料长度增长。
const (
	requiredCountingSortMin        = 8192
	requiredCountingSortMaxKeys    = 1 << 22
	requiredCountingSortMaxBuckets = 1 << 21
	// requiredBucketSortCutoff 是桶内改用比较排序的规模门槛：候选数超过桶数
	// 上限（平均每桶超过一两个候选）时，桶内插入排序的平方代价不再划算。
	requiredBucketSortCutoff = 24
)

// sortRequiredStarts 把候选键按 (起点, 规则下标) 升序整理。
// 候选先按 "起点 >> shift" 分桶，桶内元素数量接近常数，再用插入排序恢复
// 桶内的完整顺序；两次内置循环都是候选数量的线性函数，比整表计数排序少了
// 一轮直方图和一次全量搬运。任一门槛不满足时退回 slices.Sort。
func (scanner *Scanner) sortRequiredStarts(ctx *scanContext, starts []uint64, dataLen int) {
	keyCount := len(starts)
	if keyCount < requiredCountingSortMin || keyCount > requiredCountingSortMaxKeys || dataLen <= 0 {
		slices.Sort(starts)
		return
	}
	if cap(ctx.requiredSortScratch) < keyCount {
		ctx.requiredSortScratch = make([]uint64, keyCount)
	} else {
		ctx.requiredSortScratch = ctx.requiredSortScratch[:keyCount]
	}
	sortScratch := ctx.requiredSortScratch
	// 桶数目标先取候选数（平均每桶一到两个候选，桶内插入排序摊到常数代价），
	// 再受计数表内存上限约束。语料越长桶跨度越大，桶数始终不超过目标值，
	// 所以计数表规模只跟候选数走，不会随语料长度线性增长。
	target := min(keyCount, requiredCountingSortMaxBuckets)
	shift := 0
	for dataLen>>(shift+1) > target/2 {
		shift++
	}
	buckets := dataLen>>shift + 2
	if cap(ctx.requiredStartCounts) < buckets {
		ctx.requiredStartCounts = make([]uint32, buckets)
	} else {
		ctx.requiredStartCounts = ctx.requiredStartCounts[:buckets]
	}
	counts := ctx.requiredStartCounts
	clear(counts)
	for _, key := range starts {
		counts[key>>32>>shift]++
	}
	sortByCount(counts)
	for _, key := range starts {
		bucket := key >> 32 >> shift
		sortScratch[counts[bucket]] = key
		counts[bucket]++
	}
	// 每个桶覆盖至多 2^shift 个相邻起点，桶内按完整键排序即可得到
	// (起点, 规则下标) 升序。桶内元素通常在常数规模以内，插入排序的常数最小；
	// 候选数超过桶数上限时平均桶长会变大，此时改用比较排序避免平方代价。
	begin := 0
	for index := range buckets {
		end := int(counts[index])
		if end-begin > requiredBucketSortCutoff {
			slices.Sort(sortScratch[begin:end])
			begin = end
			continue
		}
		for current := begin + 1; current < end; current++ {
			key := sortScratch[current]
			position := current - 1
			for position >= begin && sortScratch[position] > key {
				sortScratch[position+1] = sortScratch[position]
				position--
			}
			sortScratch[position+1] = key
		}
		begin = end
	}
	copy(starts, sortScratch)
}

// sortByCount 把直方图原地转换为各桶的写入位置。
func sortByCount(counts []uint32) {
	var sum uint32
	for index := range counts {
		count := counts[index]
		counts[index] = sum
		sum += count
	}
}

// resetRequiredSortBuffer 清空线性排序缓冲，容量过大时直接丢弃。
func resetRequiredSortBuffer[T any](buffer *[]T) {
	// 保留上限与分桶排序的候选数上限对齐：扫描规模在门槛以内时缓冲跨扫描复用，
	// 超过上限的缓冲立即丢弃，避免长期占住与输入同阶的大块内存。
	const maxRetained = requiredCountingSortMaxKeys
	if cap(*buffer) > maxRetained {
		*buffer = nil
		return
	}
	*buffer = (*buffer)[:0]
}

// computeCanUseRoseInScan 一次性计算 Rose 是否覆盖全部规则且不包含组合依赖。
// 编译期缓存结果，运行时直接读取，避免每次 Scan 重复遍历规则。
func (scanner *Scanner) computeCanUseRoseInScan() bool {
	if scanner == nil || len(scanner.rules) == 0 || scanner.hasCombo {
		return false
	}
	program := scanner.roseProgram()
	if program == nil {
		return false
	}
	covered := make(map[uint32]struct{}, len(scanner.rules))
	for _, role := range program.Roles {
		if role.Confirm {
			return false
		}
		covered[role.ReportID] = struct{}{}
	}
	if len(covered) != len(scanner.rules) {
		return false
	}
	for _, rule := range scanner.rules {
		// 仅将无需 AST 二次确认、且没有偏移/长度扩展的纯文字规则
		// 交给 Rose；其余规则必须保留原有逐起点语义。
		if rule.ext != nil {
			return false
		}
		alternatives, ok := roseLiteralAlternatives(rule.root)
		if !ok || len(alternatives) != 1 {
			return false
		}
		_, anchored, endOfData, direct := roseLiteralPattern(alternatives[0])
		if !direct {
			return false
		}
		// 锚定（\A）与末尾约束（\z）的角色最多只有一个可行偏移，但通用自动机
		// 仍会在整段输入上枚举其字面量的全部命中，再由 Role.Eligible 逐条丢弃，
		// 在长输入上退化成 O(输入长度)。这类规则交给恰好收敛到单个起点的锚点
		// 索引（见 scan_anchor_window.go）更省，因此不进入 Rose 直通路径。
		if anchored || endOfData {
			return false
		}
		if _, ok := covered[rule.id]; !ok {
			return false
		}
	}
	return true
}

// roseDirectEntry 是 Rose 直通路径每个候选都需要的报告信息。
type roseDirectEntry struct {
	reportID uint32
	length   int
	quiet    bool
	single   bool
}

// buildRoseDirectIndex 把「角色编号 -> 报告信息」的映射在构造期固化到 Scanner。
// 直通路径对每个候选都要这份信息，运行时逐候选回查角色表与规则表会变成每个
// 命中两次 map 查找加一次 compiledRule 结构体复制。
func (scanner *Scanner) buildRoseDirectIndex() {
	program := scanner.rosePlan
	if program == nil {
		return
	}
	index := make(map[uint32]roseDirectEntry, len(program.Roles))
	single := false
	for _, role := range program.Roles {
		entry, ok := scanner.roseDirectEntryFor(program, role.ID)
		if !ok {
			continue
		}
		index[role.ID] = entry
		single = single || entry.single
	}
	scanner.roseDirect = index
	scanner.roseDirectSingle = single
}

// roseDirectEntryFor 解析单个角色编号的报告信息；直通索引未建立时作为回落路径。
func (scanner *Scanner) roseDirectEntryFor(program *rose.Program, roleID uint32) (roseDirectEntry, bool) {
	role, ok := program.RoleByID(roleID)
	if !ok {
		return roseDirectEntry{}, false
	}
	index, ok := scanner.ruleIndex[role.ReportID]
	if !ok || index < 0 || index >= len(scanner.rules) {
		return roseDirectEntry{}, false
	}
	flags := scanner.rules[index].flags
	return roseDirectEntry{
		reportID: role.ReportID,
		length:   len(role.Literal),
		quiet:    flags&CompileQuiet != 0,
		single:   flags&CompileSingleMatch != 0,
	}, true
}

// scanRoseDirectInto 将无确认文字角色的候选直接转换为 Block 结果。
// 角色索引已经在计划构造阶段完成，运行时只保留 SingleMatch 的最小状态。
func (scanner *Scanner) scanRoseDirectInto(data []byte, dst []Match) []Match {
	// 候选索引能完整替代 Rose 角色 matcher 时优先走它：同样的候选集合，但省掉
	// rose.Program 内部的完整输入扫描与 State 组装。
	if scanner.roseDirectLiteralIndex && scanner.literalFindInto != nil {
		return scanner.scanRoseDirectByLiteralIndex(data, dst)
	}
	program := scanner.roseProgram()
	if program == nil {
		return dst[:0]
	}
	pool := scanner.roseStatePool
	if pool == nil {
		pool = &sync.Pool{New: func() any { return new([]rose.State) }}
	}
	stateBuffer, _ := pool.Get().(*[]rose.State)
	if stateBuffer == nil {
		stateBuffer = new([]rose.State)
	}
	states := program.FindMatchesInto(data, (*stateBuffer)[:0])
	// 当结果切片容量超过当前池缓冲时，回收新的结果切片以避免下次扫描
	// 重新分配；这与 matcher 的 findRole 路径在 dst 不够大时分配新切片的
	// 行为配合，确保状态缓冲在跨多次扫描时逐步成长到最大需求。
	if cap(states) > cap(*stateBuffer) {
		*stateBuffer = states[:0]
	}
	defer func() {
		if cap(states) > 1<<20 {
			*stateBuffer = nil
		} else {
			*stateBuffer = states[:0]
		}
		pool.Put(stateBuffer)
	}()
	// 按状态数预分配目标切片，避免 append 多次扩容。
	need := len(states)
	if need == 0 {
		return dst[:0]
	}
	if cap(dst) < need {
		dst = make([]Match, 0, need+cap(dst))
	}
	out := dst[:0]
	// 只有存在 SingleMatch 规则时才需要去重表；全为普通规则时整块跳过池取用、
	// 建表与逐次清空，命中密集的扫描因此少一次 map 分配与维护。
	single := (map[uint32]struct{})(nil)
	if scanner.roseDirectSingle {
		singlePtr, _ := scanner.roseSinglePool.Get().(*map[uint32]struct{})
		if singlePtr == nil {
			singlePtr = new(map[uint32]struct{})
		}
		if *singlePtr == nil {
			// 池的 New 返回的是「指向 nil map 的指针」，上次若因桶过大被丢弃也会
			// 回填 nil；这里必须补建，否则下面登记 SingleMatch 去重项会 panic。
			*singlePtr = make(map[uint32]struct{})
		}
		single = *singlePtr
		defer func() {
			// 池中 map 的桶大小不应随单次扫描无限增长，超出限制时丢弃避免泄漏。
			if len(single) > 1<<16 {
				*singlePtr = nil
			} else {
				clear(single)
			}
			scanner.roseSinglePool.Put(singlePtr)
		}()
	}
	for _, state := range states {
		entry, ok := scanner.roseDirect[state.RoleID]
		if !ok {
			// 直通索引未建立时回落到逐候选回查，保证不漏报。
			entry, ok = scanner.roseDirectEntryFor(program, state.RoleID)
			if !ok {
				continue
			}
		}
		if entry.quiet {
			continue
		}
		if entry.single {
			if _, exists := single[entry.reportID]; exists {
				continue
			}
			single[entry.reportID] = struct{}{}
		}
		out = append(out, Match{Id: entry.reportID, From: state.Offset, To: state.Offset + uint64(entry.length)})
	}
	return out
}

// scanRoseDirectByLiteralIndex 用 Scanner 自己的候选索引驱动 Rose 直通路径。
// 进入条件（roseDirectLiteralIndex）保证候选索引覆盖全部规则、候选文字就是完整
// 文字，且角色编号恒等于规则编号，因此候选集合与 Rose 角色的 matcher 完全一致。
func (scanner *Scanner) scanRoseDirectByLiteralIndex(data []byte, dst []Match) []Match {
	var candBuf [256]literalCandidate
	candidates := scanner.literalFindInto(data, candBuf[:0])
	if len(candidates) == 0 {
		return dst[:0]
	}
	if cap(dst) < len(candidates) {
		dst = make([]Match, 0, len(candidates)+cap(dst))
	}
	out := dst[:0]
	// 只有存在 SingleMatch 规则时才需要去重表，全为普通规则时整块跳过池取用、
	// 建表与逐次清空，与 rose.Program 路径保持同一策略。
	single := (map[uint32]struct{})(nil)
	if scanner.roseDirectSingle {
		singlePtr, _ := scanner.roseSinglePool.Get().(*map[uint32]struct{})
		if singlePtr == nil {
			singlePtr = new(map[uint32]struct{})
		}
		if *singlePtr == nil {
			// 池的 New 返回的是「指向 nil map 的指针」，上次若因桶过大被丢弃也会
			// 回填 nil；这里必须补建，否则登记 SingleMatch 去重项会 panic。
			*singlePtr = make(map[uint32]struct{})
		}
		single = *singlePtr
		defer func() {
			if len(single) > 1<<16 {
				*singlePtr = nil
			} else {
				clear(single)
			}
			scanner.roseSinglePool.Put(singlePtr)
		}()
	}
	for _, candidate := range candidates {
		entry, ok := scanner.roseDirect[candidate.ID]
		if !ok {
			continue
		}
		if entry.quiet {
			continue
		}
		if entry.single {
			if _, exists := single[entry.reportID]; exists {
				continue
			}
			single[entry.reportID] = struct{}{}
		}
		out = append(out, Match{Id: entry.reportID, From: uint64(candidate.From), To: uint64(candidate.From) + uint64(entry.length)})
	}
	return out
}

func reserveInts(values []int, size int) []int {
	if cap(values) < size {
		return make([]int, size)
	}
	values = values[:size]
	clear(values)
	return values
}

// reserveCollapseWindows 复用确认窗口缓存，并把全部条目标记为无效。
func reserveCollapseWindows(values []collapseWindow, size int) []collapseWindow {
	if cap(values) < size {
		values = make([]collapseWindow, size)
	} else {
		values = values[:size]
	}
	for index := range values {
		values[index] = collapseWindow{from: 0, to: -1}
	}
	return values
}

func reserveBools(values []bool, size int) []bool {
	if cap(values) < size {
		return make([]bool, size)
	}
	values = values[:size]
	clear(values)
	return values
}

// emitCombinationReports 按匹配结束位置传播逻辑组合状态，并只由相关操作数触发报告。
func (scanner *Scanner) emitCombinationReports(reports *report.Manager, triggers []report.Event, eod uint64) {
	if scanner == nil || reports == nil {
		return
	}
	sort.SliceStable(triggers, func(i, j int) bool {
		if triggers[i].To != triggers[j].To {
			return triggers[i].To < triggers[j].To
		}
		if triggers[i].From != triggers[j].From {
			return triggers[i].From < triggers[j].From
		}
		return scanner.ruleOrder[triggers[i].ID] < scanner.ruleOrder[triggers[j].ID]
	})
	hits := combination.HitSet{}
	fired := make(map[uint32]bool)
	emitted := make(map[uint32]bool)
	for _, trigger := range triggers {
		hits[trigger.ID] = true
		scanner.emitCombinationAt(reports, trigger, hits, fired, emitted, false)
	}
	// 无论是否存在普通触发，都在输入末尾补一次求值，使否定及 EOD 组合获得闭环。
	scanner.emitCombinationAt(reports, report.Event{From: eod, To: eod}, hits, fired, emitted, true)
}

func (scanner *Scanner) emitCombinationAt(reports *report.Manager, trigger report.Event, hits combination.HitSet, fired, emittedRule map[uint32]bool, eod bool) {
	active := map[uint32]bool{}
	if !eod {
		active[trigger.ID] = true
	}
	emitted := make(map[uint32]bool)
	for pass := 0; pass < len(scanner.rules); pass++ {
		changed := false
		for _, rule := range scanner.rules {
			if rule.comb == nil || fired[rule.id] {
				continue
			}
			if !eod && !combinationReferencesActive(rule.comb, active) {
				continue
			}
			if !combination.Evaluate(rule.comb, hits) {
				continue
			}
			if rule.ext != nil && (!rule.ext.OffsetAllowed(trigger.To) || !rule.ext.LengthAllowed(trigger.To-trigger.From)) {
				continue
			}
			hits[rule.id] = true
			active[rule.id] = true
			changed = true
			if rule.flags&CompileSingleMatch != 0 {
				fired[rule.id] = true
			}
			if rule.flags&CompileQuiet != 0 || emitted[rule.id] || eod && emittedRule[rule.id] {
				continue
			}
			reports.Add(report.Event{ID: rule.id, From: trigger.From, To: trigger.To, SOM: trigger.From, Flags: uint32(rule.flags)})
			emitted[rule.id] = true
			emittedRule[rule.id] = true
		}
		if !changed {
			break
		}
		eod = false
	}
}

func combinationReferencesActive(root parser.Node, active map[uint32]bool) bool {
	for _, id := range combination.Dependencies(root) {
		if active[id] {
			return true
		}
	}
	return false
}

// resetLiteralEnds 清空起点到规则结束偏移的映射内容，保留内层 map 以便复用。
// refreshLiteralEnds 用共享文字索引重建逐起点过滤表；内层 map 跨扫描复用，
// 只清空内容，避免候选密集时每次扫描都重建哈希表。
func (scanner *Scanner) refreshLiteralEnds(ctx *scanContext, data []byte, ends map[int]map[uint32]int) map[int]map[uint32]int {
	var candidates []literalCandidate
	if scanner.literalFindInto != nil {
		candidates = scanner.literalFindInto(data, ctx.literalCandidates[:0])
		ctx.literalCandidates = candidates
	} else if scanner.literalFind != nil {
		candidates = scanner.literalFind(data)
	}
	if ends == nil {
		ends = make(map[int]map[uint32]int)
		ctx.literalEnds = ends
	}
	resetLiteralEnds(ends)
	for _, candidate := range candidates {
		byRule := ends[candidate.From]
		if byRule == nil {
			byRule = make(map[uint32]int)
			ends[candidate.From] = byRule
		}
		byRule[candidate.ID] = candidate.To
	}
	return ends
}

func resetLiteralEnds(ends map[int]map[uint32]int) {
	if ends == nil {
		return
	}
	for start := range ends {
		clear(ends[start])
	}
}

func (scanner *Scanner) fillSingleLiteralEnds(data []byte, ends map[int]map[uint32]int) {
	if ends == nil {
		return
	}
	for _, rule := range scanner.rules {
		if _, ok := scanner.literalIDs[rule.id]; !ok {
			continue
		}
		var offsets []int
		width := 0
		if rule.smallWrite != nil {
			offsets, width = rule.smallWrite.Find(data), rule.smallWrite.Size()
		}
		if rule.smallBlock != nil {
			offsets, width = rule.smallBlock.Find(data), rule.smallBlock.Size()
		}
		for _, offset := range offsets {
			if ends[offset] == nil {
				ends[offset] = make(map[uint32]int)
			}
			ends[offset][rule.id] = offset + width
		}
	}
}

// firstRepeatPreference 返回从左到右遇到的第一个重复节点的贪婪策略。
// 没有重复节点时默认选择最长结果。
func firstRepeatPreference(n parser.Node) bool {
	var walk func(parser.Node) (bool, bool)
	walk = func(node parser.Node) (bool, bool) {
		switch v := node.(type) {
		case parser.Repeat:
			return !v.Greedy, true
		case parser.Group:
			return walk(v.Child)
		case parser.Lookaround:
			return walk(v.Child)
		case parser.Sequence:
			for _, child := range v.Elements {
				if value, ok := walk(child); ok {
					return value, true
				}
			}
		case parser.Alternation:
			for _, child := range v.Options {
				if value, ok := walk(child); ok {
					return value, true
				}
			}
		case parser.Conditional:
			if value, ok := walk(v.Yes); ok {
				return value, true
			}
			return walk(v.No)
		default:
		}
		return false, false
	}
	value, ok := walk(n)
	return ok && value
}

// matchRuleIntoArena 是 matchRuleInto 的工作区版本：arena 非空时优先用
// 字节链快路径求值全部结束偏移，超出自持能力再退回通用后端与 AST 求值。
func matchRuleIntoArena(rule *compiledRule, data []byte, start int, endsBuf []int, arena *byteChainArena, ev *nodeEval) []int {
	// 求值工作区按「一次规则求值」为生命周期整体复位；跨求值复用同一块内存，
	// 因此任何结束偏移/捕获状态切片都不得在本次求值结束后继续持有。缓冲区本身
	// 保留容量，候选密集时不会随起点数累积占用。
	if ev != nil {
		ev.resetEval()
	}
	if rule.hasBackref || rule.hasConditional {
		// 空闲起点无需捕获表：写入路径（Group）会自行创建，只读路径接受 nil。
		states := matchCaptured(rule.root, data, start, rule.flags, nil, ev)
		out := endsBuf[:0]
		for _, state := range states {
			out = append(out, state.pos)
		}
		return dedup(out)
	}
	if rule.ext == nil && rule.flags&(CompileCaseless|CompileUTF8|CompileUCP|CompileMultiline|CompileDotAll) == 0 {
		if rule.smallWrite != nil && rule.smallWrite.MatchAt(data, start) {
			return []int{start + rule.smallWrite.Size()}
		}
		if rule.smallBlock != nil && rule.smallBlock.MatchAt(data, start) {
			return []int{start + rule.smallBlock.Size()}
		}
		if arena != nil && !rule.hasCapture {
			if ends, ok := matchByteChain(arena, rule.root, data, start, rule.flags); ok {
				return append(endsBuf[:0], ends...)
			}
		}
		if rule.repeat != nil {
			return rule.repeat.MatchAtInto(data, start, endsBuf[:0])
		}
		if rule.nfaEngine != nil {
			// 首字节/前缀不可能命中的起点直接跳过，避免为每个起点付出
			// 一次完整的后端确认调用；判据与后端内部过滤完全一致。
			if !rule.nfaEngine.CandidateStartAllowed(data, start) {
				return nil
			}
			return rule.nfaEngine.MatchAtInto(data, start, endsBuf[:0])
		}
	}
	if rule.ext != nil && rule.ext.Flags&(ExtFlagEditDistance|ExtFlagHammingDistance) != 0 {
		// 只有每条分支都能降解为有限的字节原子时才走快速确认；
		// 其余结构保留完整 AST 求值，避免候选路径造成误报。
		if paths, ok := fuzzyAtomPaths(rule.root); ok && rule.flags&(CompileUTF8|CompileUCP) == 0 {
			if rule.ext.Flags&ExtFlagHammingDistance != 0 {
				maxDistance := boundedDistance(uint64(rule.ext.HammingDistance), len(data)-start)
				ends := make([]int, 0)
				for _, atoms := range paths {
					if got, matched := matchHammingAtoms(atoms, data, start, maxDistance, rule.flags); matched {
						ends = append(ends, got...)
					}
				}
				return dedup(ends)
			}
			if rule.ext.Flags&ExtFlagEditDistance != 0 {
				maxDistance := boundedDistance(uint64(rule.ext.EditDistance), len(data)-start)
				ends := make([]int, 0)
				for _, atoms := range paths {
					if got, matched := matchEditAtoms(atoms, data, start, maxDistance, rule.flags); matched {
						ends = append(ends, got...)
					}
				}
				return dedup(ends)
			}
		}
		patterns := literalAlternatives(rule.root)
		if len(patterns) > 0 {
			var out []int
			for _, literal := range patterns {
				if rule.ext.Flags&ExtFlagHammingDistance != 0 {
					end := start + len(literal)
					if end > len(data) {
						continue
					}
					within := fuzzy.WithinHamming(literal, data[start:end], uint32ToInt(rule.ext.HammingDistance))
					if rule.flags&CompileCaseless != 0 {
						within = fuzzy.WithinHammingFoldASCII(literal, data[start:end], uint32ToInt(rule.ext.HammingDistance))
					}
					if within {
						out = append(out, end)
					}
					continue
				}
				max64 := uint64(rule.ext.EditDistance)
				maxWindow := uint64(len(data))
				if uint64(len(literal)) > ^uint64(0)-maxWindow {
					maxWindow = ^uint64(0)
				} else {
					maxWindow += uint64(len(literal))
				}
				if max64 > maxWindow {
					max64 = maxWindow
				}
				maxInt := int(^uint(0) >> 1)
				maxDistance := maxInt
				if max64 < uint64(maxInt) {
					maxDistance = int(max64)
				}
				low := max(len(literal)-maxDistance, 0)
				high := len(data) - start
				if maxDistance <= maxInt-len(literal) && len(literal)+maxDistance < high {
					high = len(literal) + maxDistance
				}
				for n := low; n <= high; n++ {
					within := fuzzy.WithinEdit(literal, data[start:start+n], maxDistance)
					if rule.flags&CompileCaseless != 0 {
						within = fuzzy.WithinEditFoldASCII(literal, data[start:start+n], maxDistance)
					}
					if within {
						out = append(out, start+n)
					}
				}
			}
			return dedup(out)
		}
	}
	if rule.program != nil && !rule.eodReports && !rule.info.RequiresStatefulRuntime() && rule.flags&(CompileCaseless|CompileUTF8|CompileUCP|CompileMultiline|CompileDotAll) == 0 && !containsAny(rule.root) {
		return rule.program.MatchAt(data, start)
	}
	return matchNodeGated(rule.root, data, start, rule.flags, ev)
}

type fuzzyAtom struct {
	literal *byte
	class   *parser.Class
	mask    [4]uint64
	hasMask bool
	any     bool
}

func newFuzzyClass(c parser.Class) fuzzyAtom {
	a := fuzzyAtom{class: &c, hasMask: true}
	for _, r := range c.Ranges {
		lo, hi := int(r.Lo), int(r.Hi)
		if lo < 0 {
			lo = 0
		}
		if hi > 255 {
			hi = 255
		}
		for v := lo; v <= hi; v++ {
			a.mask[v/64] |= 1 << uint(v%64)
		}
	}
	if c.Negated {
		for i := range a.mask {
			a.mask[i] = ^a.mask[i]
		}
	}
	return a
}

// fuzzyAtomPaths 将可安全确认的表达式展开为有限原子路径。
// 路径数量和总长度均有上限，超过上限时交由完整 AST 确认。
func fuzzyAtomPaths(n parser.Node) ([][]fuzzyAtom, bool) {
	const maxPaths = 128
	const maxAtoms = 256
	var expand func(parser.Node) ([][]fuzzyAtom, bool)
	concat := func(left, right [][]fuzzyAtom) ([][]fuzzyAtom, bool) {
		if len(left) == 0 || len(right) == 0 || len(left) > maxPaths/len(right) {
			return nil, false
		}
		out := make([][]fuzzyAtom, 0, len(left)*len(right))
		for _, a := range left {
			for _, b := range right {
				if len(a)+len(b) > maxAtoms {
					return nil, false
				}
				path := make([]fuzzyAtom, 0, len(a)+len(b))
				path = append(path, a...)
				path = append(path, b...)
				out = append(out, path)
			}
		}
		return out, true
	}
	expand = func(node parser.Node) ([][]fuzzyAtom, bool) {
		switch v := node.(type) {
		case parser.Literal:
			if len(v.Value) == 0 {
				return nil, false
			}
			path := make([]fuzzyAtom, len(v.Value))
			for i, b := range v.Value {
				value := b
				path[i].literal = &value
			}
			return [][]fuzzyAtom{path}, true
		case parser.Class:
			return [][]fuzzyAtom{{newFuzzyClass(v)}}, true
		case parser.Any:
			return [][]fuzzyAtom{{{any: true}}}, true
		case parser.Group:
			if v.HasScopedFlags() || v.Atomic {
				return nil, false
			}
			return expand(v.Child)
		case parser.Sequence:
			out := [][]fuzzyAtom{{}}
			for _, child := range v.Elements {
				part, ok := expand(child)
				if !ok {
					return nil, false
				}
				out, ok = concat(out, part)
				if !ok {
					return nil, false
				}
			}
			if len(out) == 0 || len(out[0]) == 0 {
				return nil, false
			}
			return out, true
		case parser.Alternation:
			out := make([][]fuzzyAtom, 0)
			for _, child := range v.Options {
				part, ok := expand(child)
				if !ok || len(out)+len(part) > maxPaths {
					return nil, false
				}
				out = append(out, part...)
			}
			if len(out) == 0 {
				return nil, false
			}
			return out, true
		case parser.Repeat:
			if v.Max < 0 || v.Min < 0 || v.Min > v.Max || v.Max > 32 {
				return nil, false
			}
			part, ok := expand(v.Child)
			if !ok {
				return nil, false
			}
			out := make([][]fuzzyAtom, 0, v.Max-v.Min+1)
			prefix := [][]fuzzyAtom{{}}
			for i := 0; i <= v.Max; i++ {
				if i >= v.Min {
					for _, path := range prefix {
						if len(path) > 0 {
							out = append(out, path)
						}
					}
				}
				if i == v.Max {
					break
				}
				prefix, ok = concat(prefix, part)
				if !ok {
					return nil, false
				}
			}
			if len(out) == 0 || len(out) > maxPaths {
				return nil, false
			}
			return out, true
		default:
			return nil, false
		}
	}
	return expand(n)
}

func boundedDistance(value uint64, available int) int {
	if available < 0 {
		return 0
	}
	maxInt := uint64(^uint(0) >> 1)
	if value > maxInt {
		if available < int(maxInt) {
			return available
		}
		return int(maxInt)
	}
	if int(value) > available {
		return available
	}
	return int(value)
}

func matchHammingAtoms(atoms []fuzzyAtom, data []byte, start, maxDistance int, flags CompileFlag) ([]int, bool) {
	if len(atoms) == 0 || maxDistance < 0 || flags&(CompileUTF8|CompileUCP) != 0 || start < 0 || start > len(data) || len(atoms) > len(data)-start {
		return nil, false
	}
	distance := 0
	for i, atom := range atoms {
		ok := false
		if atom.literal != nil {
			ok = equalByte(*atom.literal, data[start+i], flags)
		} else if atom.class != nil {
			ok = fuzzyAtomMatches(atom, data[start+i], flags)
		} else if atom.any {
			ok = fuzzyAtomMatches(atom, data[start+i], flags)
		}
		if !ok {
			distance++
			if distance > maxDistance {
				return nil, true
			}
		}
	}
	return []int{start + len(atoms)}, true
}

func matchEditAtoms(atoms []fuzzyAtom, data []byte, start, maxDistance int, flags CompileFlag) ([]int, bool) {
	if len(atoms) == 0 || maxDistance < 0 || flags&(CompileUTF8|CompileUCP) != 0 || start < 0 || start > len(data) {
		return nil, false
	}
	minLen := max(len(atoms)-maxDistance, 0)
	available := len(data) - start
	maxLen := available
	if maxDistance <= available-len(atoms) {
		maxLen = len(atoms) + maxDistance
	}
	if maxLen > available {
		maxLen = available
	}
	if minLen > maxLen {
		return nil, true
	}
	// 一次计算所有候选宽度的动态规划末行。只保留距离带内的单元格，
	// 避免对每个候选宽度重复构造完整矩阵，控制复杂分支确认的额外成本。
	// 无穷值只需大于本次 DP 的最大可能距离，避免 MaxInt 加法溢出。
	inf := len(atoms) + maxLen + 1
	if inf < 0 || inf > int(^uint(0)>>1)-1 {
		inf = int(^uint(0) >> 1)
	}
	prev := make([]int, maxLen+1)
	cur := make([]int, maxLen+1)
	inc := func(v int) int {
		if v >= inf {
			return inf
		}
		return v + 1
	}
	addCost := func(v, cost int) int {
		if v >= inf-cost {
			return inf
		}
		return v + cost
	}
	for width := range prev {
		prev[width] = inf
		if width <= maxDistance {
			prev[width] = width
		}
	}
	for i, atom := range atoms {
		for width := range cur {
			cur[width] = inf
		}
		if i+1 <= maxDistance {
			cur[0] = i + 1
		}
		lo := max(i+1-maxDistance, 1)
		hi := min(i+1+maxDistance, maxLen)
		for width := lo; width <= hi; width++ {
			cost := 1
			if fuzzyAtomMatches(atom, data[start+width-1], flags) {
				cost = 0
			}
			cur[width] = minInt(inc(prev[width]), inc(cur[width-1]), addCost(prev[width-1], cost))
		}
		prev, cur = cur, prev
	}
	ends := make([]int, 0, maxLen-minLen+1)
	for width := minLen; width <= maxLen; width++ {
		if prev[width] <= maxDistance {
			ends = append(ends, start+width)
		}
	}
	return ends, true
}

func fuzzyAtomMatches(atom fuzzyAtom, value byte, flags CompileFlag) bool {
	if atom.any {
		return flags&CompileDotAll != 0 || value != '\n'
	}
	if atom.literal != nil {
		return equalByte(*atom.literal, value, flags)
	}
	if atom.class == nil {
		return false
	}
	if atom.hasMask && flags&CompileCaseless == 0 {
		return atom.mask[value/64]&(1<<uint(value%64)) != 0
	}
	matched := false
	for _, r := range atom.class.Ranges {
		if value >= r.Lo && value <= r.Hi {
			matched = true
			break
		}
	}
	if !matched && flags&CompileCaseless != 0 {
		folded := value
		if folded >= 'A' && folded <= 'Z' {
			folded += 'a' - 'A'
		} else if folded >= 'a' && folded <= 'z' {
			folded -= 'a' - 'A'
		}
		for _, r := range atom.class.Ranges {
			if folded >= r.Lo && folded <= r.Hi {
				matched = true
				break
			}
		}
	}
	return matched != atom.class.Negated
}

func minInt(values ...int) int {
	if len(values) == 0 {
		return 0
	}
	smallest := values[0]
	for _, value := range values[1:] {
		if value < smallest {
			smallest = value
		}
	}
	return smallest
}

func containsConditional(n parser.Node) bool {
	switch v := n.(type) {
	case parser.Conditional:
		return true
	case parser.Group:
		return containsConditional(v.Child)
	case parser.Sequence:
		if slices.ContainsFunc(v.Elements, containsConditional) {
			return true
		}
	case parser.Alternation:
		if slices.ContainsFunc(v.Options, containsConditional) {
			return true
		}
	case parser.Repeat:
		return containsConditional(v.Child)
	case parser.Lookaround:
		return containsConditional(v.Child)
	default:
	}
	return false
}

func containsAny(n parser.Node) bool {
	switch v := n.(type) {
	case parser.Any:
		return true
	case parser.Group:
		return containsAny(v.Child)
	case parser.Sequence:
		if slices.ContainsFunc(v.Elements, containsAny) {
			return true
		}
	case parser.Alternation:
		if slices.ContainsFunc(v.Options, containsAny) {
			return true
		}
	case parser.Repeat:
		return containsAny(v.Child)
	case parser.Lookaround:
		return containsAny(v.Child)
	default:
	}
	return false
}

func literalPattern(n parser.Node) ([]byte, bool) {
	switch v := n.(type) {
	case parser.Literal:
		return append([]byte(nil), v.Value...), true
	case parser.Group:
		return literalPattern(v.Child)
	case parser.Sequence:
		var out []byte
		for _, c := range v.Elements {
			p, ok := literalPattern(c)
			if !ok {
				return nil, false
			}
			out = append(out, p...)
		}
		return out, true
	case parser.Repeat:
		if v.Max < 0 || v.Min != v.Max || v.Min > 32 {
			return nil, false
		}
		part, ok := literalPattern(v.Child)
		if !ok || len(part) == 0 && v.Min > 0 {
			return nil, false
		}
		out := make([]byte, 0, len(part)*v.Min)
		for i := 0; i < v.Min; i++ {
			out = append(out, part...)
		}
		return out, true
	default:
		return nil, false
	}
}

// literalAlternatives 提取并规范化可用于候选确认的有限文字分支。
func literalAlternatives(n parser.Node) [][]byte {
	parts := literalAlternativesNode(n)
	if len(parts) == 0 || len(parts) > 256 {
		return nil
	}
	seen := make(map[string]struct{}, len(parts))
	out := make([][]byte, 0, len(parts))
	total := 0
	for _, part := range parts {
		if len(part) == 0 || len(part) > 1<<20 {
			return nil
		}
		total += len(part)
		if total > 1<<20 {
			return nil
		}
		key := string(part)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, append([]byte(nil), part...))
	}
	return out
}

func literalAlternativesNode(n parser.Node) [][]byte {
	switch v := n.(type) {
	case parser.Literal:
		if len(v.Value) == 0 {
			return nil
		}
		return [][]byte{append([]byte(nil), v.Value...)}
	case parser.Group:
		if v.HasScopedFlags() || v.Atomic {
			return nil
		}
		return literalAlternativesNode(v.Child)
	case parser.Alternation:
		out := make([][]byte, 0, len(v.Options))
		for _, option := range v.Options {
			parts := literalAlternativesNode(option)
			if len(parts) == 0 {
				return nil
			}
			out = append(out, parts...)
		}
		return out
	case parser.Sequence:
		out := [][]byte{{}}
		for _, child := range v.Elements {
			parts := literalAlternativesNode(child)
			if len(parts) == 0 {
				return nil
			}
			next := make([][]byte, 0, len(out)*len(parts))
			for _, prefix := range out {
				for _, part := range parts {
					value := append(append([]byte(nil), prefix...), part...)
					next = append(next, value)
				}
			}
			out = next
			if len(out) > 256 {
				return nil
			}
		}
		return out
	case parser.Repeat:
		if v.Max < 0 || v.Min != v.Max || v.Min > 32 {
			return nil
		}
		parts := literalAlternativesNode(v.Child)
		out := make([][]byte, 0, len(parts))
		for _, part := range parts {
			value := make([]byte, 0, len(part)*v.Min)
			for i := 0; i < v.Min; i++ {
				value = append(value, part...)
			}
			out = append(out, value)
		}
		return out
	default:
		return nil
	}
}

// matchNodeGated 是 matchNode 的可降级版本：ev 非空时复用扫描级工作区与
// UTF-8 校验结果，nil 时退回就地分配与逐次校验。
func matchNodeGated(n parser.Node, data []byte, pos int, flags CompileFlag, ev *nodeEval) []int {
	if containsCaptureSemantics(n) {
		states := matchCaptured(n, data, pos, flags, nil, ev)
		out := make([]int, 0, len(states))
		for _, state := range states {
			out = append(out, state.pos)
		}
		return dedup(out)
	}
	return matchNodeBody(n, data, pos, flags, ev)
}

// matchNodeBody 执行不涉及捕获语义的递归求值。捕获检查只在整个子树的入口
// 做一次，避免每个子节点重复遍历语法树。
func matchNodeBody(n parser.Node, data []byte, pos int, flags CompileFlag, ev *nodeEval) []int {
	switch v := n.(type) {
	case parser.Literal:
		if flags&CompileUTF8 != 0 {
			if !utf8.Valid(v.Value) || pos > 0 && pos < len(data) && data[pos]&0xc0 == 0x80 {
				return nil
			}
			if flags&CompileCaseless != 0 {
				return matchUTF8Literal(v.Value, data, pos)
			}
		}
		if pos+len(v.Value) > len(data) {
			return nil
		}
		if flags&CompileUTF8 != 0 && !validUTF8LiteralAt(data, pos, len(v.Value), ev) {
			return nil
		}
		for i, c := range v.Value {
			if !equalByte(c, data[pos+i], flags) {
				return nil
			}
		}
		return ev.singleEnd(pos + len(v.Value))
	case parser.Any:
		if pos >= len(data) || (data[pos] == '\n' && flags&CompileDotAll == 0) {
			return nil
		}
		if flags&CompileUTF8 != 0 {
			r, size := utf8.DecodeRune(data[pos:])
			if r == utf8.RuneError && size == 1 && data[pos] >= 0x80 {
				return nil
			}
			if size == 0 {
				size = 1
			}
			return ev.singleEnd(pos + size)
		}
		return ev.singleEnd(pos + 1)
	case parser.Class:
		if pos >= len(data) {
			return nil
		}
		if flags&CompileUCP != 0 && v.Kind != parser.ClassNormal {
			runeValue, size := utf8.DecodeRune(data[pos:])
			if size == 0 || runeValue == utf8.RuneError && size == 1 && data[pos] >= 0x80 {
				return nil
			}
			matched := unicodeShorthandClass(runeValue, v.Kind)
			if matched != v.Negated {
				return ev.singleEnd(pos + size)
			}
			return nil
		}
		if flags&CompileUTF8 != 0 {
			runeValue, size := utf8.DecodeRune(data[pos:])
			if size == 0 || runeValue == utf8.RuneError && size == 1 && data[pos] >= 0x80 {
				return nil
			}
			matched := false
			if runeValue <= 0xff {
				value := byte(runeValue)
				for _, r := range v.Ranges {
					if value >= r.Lo && value <= r.Hi || flags&CompileCaseless != 0 && ((value >= 'a' && value <= 'z' && value-'a'+'A' >= r.Lo && value-'a'+'A' <= r.Hi) || (value >= 'A' && value <= 'Z' && value-'A'+'a' >= r.Lo && value-'A'+'a' <= r.Hi)) {
						matched = true
						break
					}
				}
			}
			if matched != v.Negated {
				return ev.singleEnd(pos + size)
			}
			return nil
		}
		if classByteMatch(v, data[pos], flags) {
			return ev.singleEnd(pos + 1)
		}
		return nil
	case parser.UnicodeClass:
		if flags&CompileUTF8 == 0 || pos >= len(data) {
			return nil
		}
		r, size := utf8.DecodeRune(data[pos:])
		if size == 0 || (r == utf8.RuneError && size == 1 && data[pos] >= 0x80) {
			return nil
		}
		// 属性名在解析阶段已解析成判定函数；节点缺少解析结果时按名称现场解析，
		// 保证手工构造的 AST 仍然得到与解析器一致的语义。
		property := v.Resolved
		if property == nil {
			property = parser.ResolveUnicodeProperty(v.Name)
		}
		ok := property.Match(r)
		if v.Negated {
			ok = !ok
		}
		if ok {
			return ev.singleEnd(pos + size)
		}
		return nil
	case parser.Assertion:
		if assertionHolds(v.Kind, data, pos, flags) {
			return ev.singleEnd(pos)
		}
		return nil
	case parser.Group:
		flags = scopedGroupFlags(flags, v)
		if v.Atomic {
			states := matchCaptured(v.Child, data, pos, flags, nil, ev)
			if len(states) == 0 {
				return nil
			}
			state := selectAtomicState(states, v.Child)
			return ev.singleEnd(state.pos)
		}
		return matchNodeBody(v.Child, data, pos, flags, ev)
	case parser.Lookaround:
		positive := v.Kind == parser.Lookahead || v.Kind == parser.Lookbehind
		if v.Kind == parser.Lookbehind || v.Kind == parser.NegativeLookbehind {
			found := false
			width, _, ok := parser.FixedWidth(v.Child)
			if flags&CompileUTF8 != 0 {
				width, _, ok = parser.FixedWidthUTF8(v.Child)
			}
			// 候选起点缓冲走求值工作区：固定宽度只需一个起点，变宽时起点数随
			// pos 增长、按实际数量预留，避免每个候选起点都从 nil 切片 append。
			var starts []int
			if ok {
				starts = ev.endsReserve(1)
				if pos >= width {
					starts = append(starts, pos-width)
				}
			} else {
				starts = ev.endsReserve(pos + 1)
				for start := 0; start <= pos; start++ {
					starts = append(starts, start)
				}
			}
			for _, start := range starts {
				if slices.Contains(matchNodeBody(v.Child, data, start, flags, ev), pos) {
					found = true
				}
			}
			if found == positive {
				return ev.singleEnd(pos)
			}
			return nil
		}
		ends := matchNodeBody(v.Child, data, pos, flags, ev)
		if positive {
			if len(ends) > 0 {
				return ev.singleEnd(pos)
			}
			return nil
		}
		if len(ends) == 0 {
			return ev.singleEnd(pos)
		}
		return nil
	case parser.Sequence:
		positions := ev.singleEnd(pos)
		for _, child := range v.Elements {
			if verb, ok := child.(parser.ControlVerb); ok && strings.EqualFold(verb.Name, "ACCEPT") {
				return positions
			}
			next := ev.endsReserve(endsReserveFor(len(positions)))
			for _, p := range positions {
				next = append(next, matchNodeBody(child, data, p, flags, ev)...)
			}
			positions = dedup(next)
			if len(positions) == 0 {
				break
			}
		}
		return positions
	case parser.Alternation:
		out := ev.endsReserve(len(v.Options))
		for _, child := range v.Options {
			out = append(out, matchNodeBody(child, data, pos, flags, ev)...)
		}
		return dedup(out)
	case parser.Repeat:
		if ends, ok := matchByteRepeat(v, data, pos, flags); ok {
			return ends
		}
		positions := ev.singleEnd(pos)
		maxCount := v.Max
		budget := max(len(data)-pos+1, 1)
		if maxCount < 0 || maxCount > budget {
			maxCount = budget
		}
		// 结果长度取决于实际可重复次数而不是 maxCount 上界，因此只按一个
		// 中等规模的批量预留；超出部分交给 append 自行扩容。
		results := ev.endsReserve(min(maxCount+1, repeatResultReserve))
		if v.Min == 0 {
			results = append(results, pos)
		}
		for count := 1; count <= maxCount; count++ {
			next := ev.endsReserve(len(positions))
			for _, p := range positions {
				next = append(next, matchNodeBody(v.Child, data, p, flags, ev)...)
			}
			next = dedup(next)
			if len(next) == 0 {
				break
			}
			positions = next
			if count >= v.Min {
				results = append(results, next...)
			}
			if len(next) == 1 && next[0] == pos {
				break
			}
		}
		return dedup(results)
	case parser.ControlVerb:
		switch v.Name {
		case "FAIL", "F":
			return nil
		case "ACCEPT":
			return ev.singleEnd(pos)
		case "SKIP", "PRUNE", "COMMIT":
			return nil
		default:
			return nil
		}
	case parser.Backreference:
		return nil
	default:
		return nil
	}
}

// classByteMatch 判定单个字节是否落在字符类内，语义与 matchNodeBody 的
// 非 UTF-8 字符类分支完全一致，供普通确认与确定性重复的快路径共用。
func classByteMatch(v parser.Class, value byte, flags CompileFlag) bool {
	matched := false
	for _, r := range v.Ranges {
		if value >= r.Lo && value <= r.Hi || flags&CompileCaseless != 0 && ((value >= 'a' && value <= 'z' && value-'a'+'A' >= r.Lo && value-'a'+'A' <= r.Hi) || (value >= 'A' && value <= 'Z' && value-'A'+'a' >= r.Lo && value-'A'+'a' <= r.Hi)) {
			matched = true
			break
		}
	}
	return matched != v.Negated
}

// byteRepeatAtom 判断重复的子节点在当前模式下是否恰好消费一个字节。
// UTF-8/UCP 模式下字符类按码位消费 1~4 字节，不能按字节推进，因此显式排除。
func byteRepeatAtom(n parser.Node, flags CompileFlag) bool {
	if flags&(CompileUTF8|CompileUCP) != 0 {
		return false
	}
	switch v := n.(type) {
	case parser.Group:
		if v.Atomic || v.HasScopedFlags() {
			return false
		}
		return byteRepeatAtom(v.Child, flags)
	case parser.Literal:
		return len(v.Value) == 1
	case parser.Any:
		return true
	case parser.Class:
		return true
	default:
	}
	return false
}

// matchByteAtom 判定恰好消费一个字节的原子节点是否匹配该字节。
func matchByteAtom(n parser.Node, value byte, flags CompileFlag) bool {
	switch v := n.(type) {
	case parser.Group:
		return matchByteAtom(v.Child, value, flags)
	case parser.Literal:
		return len(v.Value) == 1 && equalByte(v.Value[0], value, flags)
	case parser.Any:
		return value != '\n' || flags&CompileDotAll != 0
	case parser.Class:
		return classByteMatch(v, value, flags)
	default:
	}
	return false
}

// matchByteRepeat 处理“子节点恰好消费一个字节”的确定性重复：这类重复的结束
// 位置只有一条链，直接按字节推进并一次性收集 min..max 区间内的结束位置，
// 避免逐轮分配中间状态切片。返回 ok 为 false 表示该重复不适用快路径。
func matchByteRepeat(v parser.Repeat, data []byte, pos int, flags CompileFlag) ([]int, bool) {
	return matchByteRepeatInto(v, data, pos, flags, nil)
}

// matchByteRepeatInto 是 matchByteRepeat 的缓冲复用版本：调用方提供结束偏移
// 缓冲时不再分配，供确认快路径在可复用工作区上直接写入。
func matchByteRepeatInto(v parser.Repeat, data []byte, pos int, flags CompileFlag, dst []int) ([]int, bool) {
	if !byteRepeatAtom(v.Child, flags) {
		return nil, false
	}
	limit := max(len(data)-pos, 0)
	maxCount := v.Max
	if maxCount < 0 || maxCount > limit {
		maxCount = limit
	}
	count := 0
	for count < maxCount && matchByteAtom(v.Child, data[pos+count], flags) {
		count++
	}
	if count < v.Min {
		return dst[:0], true
	}
	ends := dst[:0]
	for c := v.Min; c <= count; c++ {
		ends = append(ends, pos+c)
	}
	return ends, true
}

// nodeEval 承载单次扫描内 AST 求值可复用的状态。
//
// 它绑定在某一份 subject 上：调用方必须在整次扫描中传入同一份数据切片。求值
// 过程中产生的结束偏移切片、UTF-8 合法性判定都存放在这里，避免逐候选起点重复
// 分配与重复扫描。nil 表示不提供工作区，此时退化为就地分配与逐次校验。
type nodeEval struct {
	ends   sliceArena[int]
	states sliceArena[captureState]
	// caps 承载捕获表副本。捕获表按「一次写入一份新表」的不可变方式生成，
	// 每条记录只有 32 字节；工作区化之后带捕获语义的规则在求值期不再分配。
	caps sliceArena[captureValue]
	utf8 utf8Gate
}

// reset 清空全部缓存，供扫描上下文跨扫描复用。
func (ev *nodeEval) reset() {
	ev.resetEval()
	ev.utf8.reset()
}

// resetEval 复位单次规则求值的工作区，保留已分配容量。
//
// 结束偏移与捕获状态只在一次求值内有效，必须逐次复位；否则候选密集的扫描会
// 按候选数持续累积工作区（容量本身会被保留复用）。
func (ev *nodeEval) resetEval() {
	ev.ends.reset()
	ev.states.reset()
	ev.caps.reset()
}

// subjectValid 返回 data 是否为合法 UTF-8，非 nil 时记忆结果。
//
// 逐起点校验整个 subject 是 O(候选起点数 × 数据长度)，在重复输入下会退化成
// 平方级，因此这里必须复用同一次扫描的结果。
func (ev *nodeEval) subjectValid(data []byte) bool {
	if ev == nil {
		return utf8.Valid(data)
	}
	return ev.utf8.subjectValid(data)
}

// endsAlloc 返回长度为 n 的结束偏移切片；非 nil 时从工作区切分。
func (ev *nodeEval) endsAlloc(n int) []int {
	if ev == nil {
		return make([]int, n)
	}
	return ev.ends.alloc(n)
}

// endsReserve 返回长度 0、容量不小于 n 的结束偏移切片，供 append 复用；
// 超出预留容量时 append 自行扩容，只损失复用而不影响正确性。
func (ev *nodeEval) endsReserve(n int) []int {
	if ev == nil {
		return make([]int, 0, n)
	}
	return ev.ends.reserve(n)
}

// singleEnd 返回只包含一个结束偏移的结果切片，优先复用求值工作区。
func (ev *nodeEval) singleEnd(end int) []int {
	out := ev.endsAlloc(1)
	out[0] = end
	return out
}

// captureAlloc 返回长度为 n 的捕获表缓冲；非 nil 时从工作区切分。
func (ev *nodeEval) captureAlloc(n int) captureTable {
	if ev == nil {
		return make(captureTable, n)
	}
	return ev.caps.alloc(n)
}

// statesReserve 返回长度 0、容量不小于 n 的捕获状态切片。
func (ev *nodeEval) statesReserve(n int) []captureState {
	if ev == nil {
		return make([]captureState, 0, n)
	}
	return ev.states.reserve(n)
}

// endsReserveFor 把「当前位置数」放大成一次预留量：子节点可能把单个位置展开
// 成多个结束偏移，按位置数精确预留几乎必然触发一次扩容与拷贝。
//
// 只用于预留次数与输入长度无关的场合（如序列的每个子节点）；重复节点按轮次
// 预留，放大会让工作区随输入长度成倍增长。
func endsReserveFor(n int) int {
	if n < sliceArenaMin {
		return sliceArenaMin
	}
	return n
}

// singleState 返回只包含一个捕获状态的结果切片。
func (ev *nodeEval) singleState(state captureState) []captureState {
	if ev == nil {
		return []captureState{state}
	}
	out := ev.states.alloc(1)
	out[0] = state
	return out
}

// utf8Gate 缓存单次扫描内 subject 的 UTF-8 合法性。
type utf8Gate struct {
	checked bool
	valid   bool
}

// subjectValid 返回 data 是否为合法 UTF-8，并在非 nil 时记忆结果。
func (g *utf8Gate) subjectValid(data []byte) bool {
	if g == nil {
		return utf8.Valid(data)
	}
	if !g.checked {
		g.valid = utf8.Valid(data)
		g.checked = true
	}
	return g.valid
}

// reset 清空缓存，供扫描上下文跨扫描复用。
func (g *utf8Gate) reset() {
	g.checked = false
	g.valid = false
}

// sliceArena 是 AST 求值中间结果的单次工作区：只按游标向后切分，不做单独释放。
// 每次进入规则求值前整体 reset，因此同一块内存在一次求值内被反复复用。
//
// 切分出的切片只能存活到本次求值结束：调用方读取结束后不得继续持有。
type sliceArena[T any] struct {
	buf []T
	off int
}

// sliceArenaMin 是工作区的起始容量，避免单节点求值频繁扩容。
const sliceArenaMin = 64

// sliceArenaMax 是单块工作区的容量上限（元素数）。
//
// 工作区只按游标前进、不单独释放，因此单次求值的占用等于这次求值的**累计**
// 切分量，而不是同时存活量：长重复、深回溯这类结构会让累计量远大于实际需求。
// 超过上限后退回普通分配，把工作区占用限制在常数规模；上限之内的常见求值
// 仍然完全不分配。
const sliceArenaMax = 1 << 13

// repeatResultReserve 是重复节点一次性预留的结果槽位上限。重复结果的长度由
// 实际匹配长度决定，预留过多会在长输入上按起点浪费工作区，预留过少则会为每个
// 候选起点付一次扩容拷贝。
const repeatResultReserve = sliceArenaMin

// reset 把游标归零，保留已分配容量。
func (a *sliceArena[T]) reset() {
	a.off = 0
}

// reserve 切出一段容量不小于 n 的空闲区域。超出上限时返回一次性切片，
// 语义（长度 0、容量不小于 n）与工作区版本一致，只是不再复用内存。
func (a *sliceArena[T]) reserve(n int) []T {
	if n <= 0 {
		return nil
	}
	if a.off+n > sliceArenaMax {
		return make([]T, n)[:0]
	}
	if a.off+n > len(a.buf) {
		size := len(a.buf) * 2
		if size < n {
			size = n
		}
		if size < sliceArenaMin {
			size = sliceArenaMin
		}
		if size > sliceArenaMax {
			size = sliceArenaMax
		}
		if a.off+n > size {
			size = a.off + n
		}
		grown := make([]T, size)
		copy(grown, a.buf[:a.off])
		a.buf = grown
	}
	out := a.buf[a.off : a.off : a.off+n]
	a.off += n
	return out
}

// alloc 切出一段长度与容量都为 n 的区域。
func (a *sliceArena[T]) alloc(n int) []T {
	return a.reserve(n)[:n]
}

// validUTF8LiteralAt 判断 UTF8 模式下位置 pos 处长度为 width 的字面量命中是否有效。
//
// 语义要求三条同时成立：整个 subject 是合法 UTF-8、pos 落在字符起始边界上、
// 命中区间自身是合法 UTF-8。字面量本身在调用方已经校验过，命中区间又必须与字面量
// 逐字节相等，因此后两条在全量校验通过时必然成立；保留它们是为了在全量校验尚未
// 发生时尽早排除明显非法的位置。全量校验走工作区，避免逐起点重复扫描整个 subject。
func validUTF8LiteralAt(data []byte, pos, width int, ev *nodeEval) bool {
	if pos < 0 || width <= 0 || pos+width > len(data) || pos > 0 && pos < len(data) && data[pos]&0xc0 == 0x80 {
		return false
	}
	if !ev.subjectValid(data) {
		return false
	}
	return utf8.Valid(data[pos : pos+width])
}

// scopedGroupFlags 将分组内的局部修饰符合并到当前匹配状态。
func scopedGroupFlags(flags CompileFlag, group parser.Group) CompileFlag {
	if group.SetFlags&parser.GroupFlagCaseless != 0 {
		flags |= CompileCaseless
	}
	if group.SetFlags&parser.GroupFlagDotAll != 0 {
		flags |= CompileDotAll
	}
	if group.SetFlags&parser.GroupFlagMultiline != 0 {
		flags |= CompileMultiline
	}
	if group.ClearFlags&parser.GroupFlagCaseless != 0 {
		flags &^= CompileCaseless
	}
	if group.ClearFlags&parser.GroupFlagDotAll != 0 {
		flags &^= CompileDotAll
	}
	if group.ClearFlags&parser.GroupFlagMultiline != 0 {
		flags &^= CompileMultiline
	}
	return flags
}

func unicodeShorthandClass(value rune, kind parser.ClassKind) bool {
	switch kind {
	case parser.ClassDigit:
		return unicode.IsDigit(value)
	case parser.ClassWord:
		return unicode.IsLetter(value) || unicode.IsNumber(value) || unicode.IsMark(value) || unicode.Is(unicode.Pc, value)
	case parser.ClassSpace:
		return unicode.IsSpace(value)
	case parser.ClassHorizontalSpace:
		return value == '\t' || value == ' ' || value == 0x00a0 || value == 0x1680 || value >= 0x2000 && value <= 0x200a || value == 0x202f || value == 0x205f || value == 0x3000
	case parser.ClassVerticalSpace:
		return value == '\n' || value == '\r' || value == '\v' || value == '\f' || value == 0x0085 || value == 0x2028 || value == 0x2029
	default:
	}
	return false
}

func matchUTF8Literal(literal, data []byte, pos int) []int {
	if pos < 0 || pos > len(data) || !utf8.Valid(literal) || !utf8.Valid(data) || !utf8.Valid(data[pos:]) || pos > 0 && pos < len(data) && data[pos]&0xc0 == 0x80 {
		return nil
	}
	litRunes := []rune(string(literal))
	dataRunes := []rune(string(data[pos:]))
	if len(dataRunes) < len(litRunes) {
		return nil
	}
	for i, want := range litRunes {
		got := dataRunes[i]
		if got != want {
			matched := false
			for folded := unicode.SimpleFold(want); folded != want; folded = unicode.SimpleFold(folded) {
				if folded == got {
					matched = true
					break
				}
			}
			if !matched {
				return nil
			}
		}
	}
	end := pos
	for range litRunes {
		_, size := utf8.DecodeRune(data[end:])
		end += size
	}
	return []int{end}
}

func containsCaptureSemantics(n parser.Node) bool {
	switch v := n.(type) {
	case parser.Backreference, parser.Conditional:
		return true
	case parser.Group:
		return containsCaptureSemantics(v.Child)
	case parser.Sequence:
		if slices.ContainsFunc(v.Elements, containsCaptureSemantics) {
			return true
		}
	case parser.Alternation:
		if slices.ContainsFunc(v.Options, containsCaptureSemantics) {
			return true
		}
	case parser.Repeat, parser.Lookaround:
		return containsCaptureSemantics(childNode(v))
	default:
	}
	return false
}
func childNode(n parser.Node) parser.Node {
	switch v := n.(type) {
	case parser.Repeat:
		return v.Child
	case parser.Lookaround:
		return v.Child
	default:
	}
	return nil
}
func containsBackreference(n parser.Node) bool {
	switch v := n.(type) {
	case parser.Backreference:
		return true
	case parser.Sequence:
		if slices.ContainsFunc(v.Elements, containsBackreference) {
			return true
		}
	case parser.Group:
		return containsBackreference(v.Child)
	case parser.Alternation:
		if slices.ContainsFunc(v.Options, containsBackreference) {
			return true
		}
	default:
	}
	return false
}

// captureValue 是一条捕获记录：捕获组编号与它在 subject 上占据的子切片。
type captureValue struct {
	index int
	value []byte
}

// captureTable 是捕获内容的不可变快照，条目按捕获组编号升序排列。
//
// 捕获组编号在编译期已经确定，实际用到的编号通常只有个位数；用有序小切片代替
// map，可以在写入路径上省掉每写一次捕获组就分配一个 map 头与桶的开销。表本身
// 不可变——写入总是生成新表——因此可以在不同求值状态之间安全共享。
//
// 查找与比较都依赖「按编号升序且编号唯一」，这两条不变式由 withCapture 与
// scopedCaptureTable 保证。
type captureTable []captureValue

// lookup 返回捕获组编号对应的子切片。条目升序排列，因此在越过目标编号后即可
// 提前结束，小表上比二分更省。
func (t captureTable) lookup(index int) ([]byte, bool) {
	for _, entry := range t {
		if entry.index == index {
			return entry.value, true
		}
		if entry.index > index {
			break
		}
	}
	return nil, false
}

type captureState struct {
	pos  int
	caps captureTable
}

func matchCaptured(node parser.Node, data []byte, pos int, flags CompileFlag, caps captureTable, ev *nodeEval) []captureState {
	switch value := node.(type) {
	case parser.Group:
		flags = scopedGroupFlags(flags, value)
		entryCaps := scopedCaptureTable(ev, caps, value)
		out := matchCaptured(value.Child, data, pos, flags, entryCaps, ev)
		if value.Atomic && len(out) > 0 {
			out = ev.singleState(selectAtomicState(out, value.Child))
		}
		if value.Capture <= 0 {
			return out
		}
		for i := range out {
			out[i].caps = withCapture(ev, out[i].caps, value.Capture, data[pos:out[i].pos])
		}
		return out
	case parser.Sequence:
		// 捕获表只在即将被写入的路径上复制（见 Group 与 scopedCaptureTable 注释），
		// 这里与后续的只读分支都可以直接透传。
		states := ev.singleState(captureState{pos: pos, caps: caps})
		for _, child := range value.Elements {
			next := ev.statesReserve(len(states))
			for _, state := range states {
				next = append(next, matchCaptured(child, data, state.pos, flags, state.caps, ev)...)
			}
			states = dedupCaptureStates(next)
			if len(states) == 0 {
				break
			}
		}
		return states
	case parser.Alternation:
		out := ev.statesReserve(len(value.Options))
		for _, child := range value.Options {
			out = append(out, matchCaptured(child, data, pos, flags, caps, ev)...)
		}
		return dedupCaptureStates(out)
	case parser.Repeat:
		states := ev.singleState(captureState{pos: pos, caps: caps})
		// 结果长度由实际可重复次数决定。只按 Min+1 预留会让第一次重复命中就
		// 触发 append 扩容、并绕开求值工作区；按一个最小批量预留后，常见的短
		// 重复可以在预留区里完成，代价只是同一块工作区多切几个槽位。
		out := ev.statesReserve(statesRepeatReserve(value.Min))
		if value.Min == 0 {
			out = append(out, states...)
		}
		maxCount := value.Max
		budget := max(len(data)-pos+1, 1)
		if maxCount < 0 || maxCount > budget {
			maxCount = budget
		}
		for count := 1; count <= maxCount; count++ {
			next := ev.statesReserve(len(states))
			for _, state := range states {
				next = append(next, matchCaptured(value.Child, data, state.pos, flags, state.caps, ev)...)
			}
			states = dedupCaptureStates(next)
			if len(states) == 0 {
				break
			}
			if count >= value.Min {
				out = append(out, states...)
			}
			if len(states) == 1 && states[0].pos == pos {
				break
			}
		}
		return dedupCaptureStates(out)
	case parser.Backreference:
		captured, ok := caps.lookup(value.Index)
		if !ok || pos+len(captured) > len(data) {
			return nil
		}
		for i, item := range captured {
			if !equalByte(item, data[pos+i], flags) {
				return nil
			}
		}
		return ev.singleState(captureState{pos: pos + len(captured), caps: caps})
	case parser.Conditional:
		child := value.No
		if _, ok := caps.lookup(value.Index); ok {
			child = value.Yes
		}
		return matchCaptured(child, data, pos, flags, caps, ev)
	case parser.Lookaround:
		positive := value.Kind == parser.Lookahead || value.Kind == parser.Lookbehind
		if value.Kind == parser.Lookbehind || value.Kind == parser.NegativeLookbehind {
			matched := ev.statesReserve(1)
			starts := ev.endsReserve(1)
			if width, _, ok := parser.FixedWidth(value.Child); ok && flags&CompileUTF8 == 0 {
				if pos >= width {
					starts = append(starts, pos-width)
				}
			} else {
				for start := 0; start <= pos; start++ {
					starts = append(starts, start)
				}
			}
			for _, start := range starts {
				for _, state := range matchCaptured(value.Child, data, start, flags, caps, ev) {
					if state.pos == pos {
						matched = append(matched, state)
					}
				}
			}
			if positive && len(matched) > 0 {
				for i := range matched {
					matched[i].pos = pos
				}
				return dedupCaptureStates(matched)
			}
			if !positive && len(matched) == 0 {
				return ev.singleState(captureState{pos: pos, caps: caps})
			}
			return nil
		}
		states := matchCaptured(value.Child, data, pos, flags, caps, ev)
		if positive && len(states) > 0 {
			for i := range states {
				states[i].pos = pos
			}
			return dedupCaptureStates(states)
		}
		if !positive && len(states) == 0 {
			return ev.singleState(captureState{pos: pos, caps: caps})
		}
		return nil
	default:
		ends := matchNodeGated(node, data, pos, flags, ev)
		out := ev.statesReserve(len(ends))
		for _, end := range ends {
			out = append(out, captureState{pos: end, caps: caps})
		}
		return out
	}
}

// scopedCaptureTable 返回清除了分组自身及其子树旧捕获后的捕获表：分组重入时
// 上一轮的捕获值必须失效，否则反向引用会读到上一次迭代的内容。
//
// 尚无捕获时直接复用空表；已有捕获但不落在该作用域内时（绝大多数重入场景）
// 复用原表。两种情况都不再为「可能被清除」构造一遍捕获编号列表。
func scopedCaptureTable(ev *nodeEval, caps captureTable, group parser.Group) captureTable {
	if len(caps) == 0 {
		return caps
	}
	ids := parser.CaptureIDs(group.Child)
	removed := 0
	for _, entry := range caps {
		if captureInScope(group.Capture, ids, entry.index) {
			removed++
		}
	}
	if removed == 0 {
		return caps
	}
	out := ev.captureAlloc(len(caps) - removed)
	write := 0
	for _, entry := range caps {
		if captureInScope(group.Capture, ids, entry.index) {
			continue
		}
		out[write] = entry
		write++
	}
	return out[:write]
}

// captureInScope 判断捕获编号是否属于该分组：分组自身的编号，或子树里任意
// 分组的编号。
func captureInScope(own int, ids []int, index int) bool {
	if index <= 0 {
		return false
	}
	if own > 0 && index == own {
		return true
	}
	for _, id := range ids {
		if id == index {
			return true
		}
	}
	return false
}

// selectAtomicState 保留原子分组首次确定的路径，避免后续回溯改写选择。
func selectAtomicState(states []captureState, child parser.Node) captureState {
	if rep, ok := child.(parser.Repeat); ok && rep.Greedy {
		return states[len(states)-1]
	}
	return states[0]
}

// statesRepeatReserveMin 是重复节点结果的预留下限（元素数）。
//
// 重复节点在一轮里可能产出多个状态，只按 Min+1 预留必然紧接着扩容；预留一个
// 小批量可以覆盖绝大多数短重复，又不至于在长输入上按起点浪费工作区。
const statesRepeatReserveMin = 8

// statesRepeatReserve 把重复节点的最小重复次数换算成结果预留量。
func statesRepeatReserve(minCount int) int {
	if minCount+1 >= statesRepeatReserveMin {
		return minCount + 1
	}
	return statesRepeatReserveMin
}

// captureStateLinearDedup 是线性去重的状态数上限。状态集合在回溯里普遍很小，
// 直接两两比较比重建 map 与编码键更省：既没有哈希开销，也不产生任何分配。
const captureStateLinearDedup = 8

// dedupCaptureStates 按「结束位置 + 捕获内容」去重，保留首次出现的顺序。
func dedupCaptureStates(states []captureState) []captureState {
	if len(states) < 2 {
		return states
	}
	if len(states) <= captureStateLinearDedup {
		out := states[:0]
		for _, state := range states {
			duplicate := false
			for _, kept := range out {
				if captureStatesEqual(kept, state) {
					duplicate = true
					break
				}
			}
			if !duplicate {
				out = append(out, state)
			}
		}
		return out
	}
	seen := make(map[string]struct{}, len(states))
	out := states[:0]
	// key 跨状态复用，避免每个状态都重新构造一份字符串。
	key := make([]byte, 0, 64)
	for _, state := range states {
		key = appendCaptureStateKey(key[:0], state)
		if _, ok := seen[string(key)]; ok {
			continue
		}
		seen[string(key)] = struct{}{}
		out = append(out, state)
	}
	return out
}

// captureStatesEqual 判定两个捕获状态是否等价。捕获表按编号升序排列且编号唯一，
// 因此可以直接按下标逐条比较，不必再做一次哈希查找。
func captureStatesEqual(left, right captureState) bool {
	if left.pos != right.pos || len(left.caps) != len(right.caps) {
		return false
	}
	for i, entry := range left.caps {
		other := right.caps[i]
		if entry.index != other.index || !bytes.Equal(entry.value, other.value) {
			return false
		}
	}
	return true
}

// appendCaptureStateKey 把捕获状态编码成前缀无关的字节序列：结束位置、捕获
// 数量，随后是按下标升序的「下标 + 长度 + 原始字节」。
//
// 长度前缀保证不同状态的编码不会产生歧义，因此不需要转义，也不必像早先基于
// 引号转义字符串的实现那样为每次去重构造多份临时字符串。捕获表本身已经按编号
// 升序，所以这里直接顺序编码即可得到与插入顺序无关的规范键。
func appendCaptureStateKey(dst []byte, state captureState) []byte {
	dst = binary.AppendUvarint(dst, uint64(state.pos))
	dst = binary.AppendUvarint(dst, uint64(len(state.caps)))
	for _, entry := range state.caps {
		dst = binary.AppendUvarint(dst, uint64(entry.index))
		dst = binary.AppendUvarint(dst, uint64(len(entry.value)))
		dst = append(dst, entry.value...)
	}
	return dst
}

// withCapture 返回「保留全部旧捕获、并把 id 指向 captured 内容」的新捕获表。
//
// captured 直接引用 subject 的子切片：输入在扫描期间保持不变，且捕获值只被读取
// 和比较，因此不必为每次捕获复制一份字节。
func withCapture(ev *nodeEval, caps captureTable, id int, captured []byte) captureTable {
	out := ev.captureAlloc(len(caps) + 1)
	write := 0
	replaced := false
	for _, entry := range caps {
		if !replaced && entry.index >= id {
			out[write] = captureValue{index: id, value: captured}
			write++
			replaced = true
			if entry.index == id {
				// 同编号覆盖：旧记录到此为止。
				continue
			}
		}
		out[write] = entry
		write++
	}
	if !replaced {
		out[write] = captureValue{index: id, value: captured}
		write++
	}
	return out[:write]
}

func dedup(values []int) []int {
	// 结束偏移大多由确定性推进产生，天然就是升序；先做一次线性检查即可跳过
	// 排序，避免在命中密集的重复求值里为每个极小切片付出排序调用开销。
	if len(values) > 1 && !slices.IsSorted(values) {
		slices.Sort(values)
	}
	out := values[:0]
	for _, v := range values {
		if len(out) == 0 || out[len(out)-1] != v {
			out = append(out, v)
		}
	}
	return out
}

// assertionHolds 判定零宽断言在指定位置是否成立，通用 AST 求值与
// 字节链快路径共用同一份语义，避免两条路径对边界条件的解释出现分歧。
func assertionHolds(kind parser.AssertionKind, data []byte, pos int, flags CompileFlag) bool {
	switch kind {
	case parser.Begin:
		return pos == 0 || flags&CompileMultiline != 0 && pos > 0 && data[pos-1] == '\n'
	case parser.BeginAbsolute:
		return pos == 0
	case parser.End:
		return pos == len(data) || flags&CompileMultiline != 0 && pos < len(data) && data[pos] == '\n'
	case parser.EndAbsolute:
		return pos == len(data)
	case parser.EndBeforeFinalNewline:
		return pos == len(data) || pos+1 == len(data) && data[pos] == '\n' || pos+2 == len(data) && data[pos] == '\r' && data[pos+1] == '\n'
	case parser.WordBoundary, parser.NonWordBoundary:
		left := wordBefore(data, pos, flags)
		right := wordAfter(data, pos, flags)
		return left != right == (kind == parser.WordBoundary)
	default:
	}
	return false
}

// byteChainFrontier 限制快路径单步推进的位置数，位置集合膨胀时退回通用求值。
const byteChainFrontier = 64

// byteChainRepeatBuf 是单字节原子重复的专用缓冲。它只被最内层重复占用，
// 与位置前沿分开存放，避免嵌套层级把工作区按前沿宽度成倍放大。
const byteChainRepeatBuf = 1024

// byteChainArenaSize 是快路径单次确认可用的工作区规模，单位为 int。
const byteChainArenaSize = 4096

// byteChainMarksLimit 限制位置去重位图可覆盖的输入长度。超出该长度的输入
// 直接退回通用求值，避免为一个 Block 预留过大的标记数组。
const byteChainMarksLimit = 1 << 19

// byteChainArena 是确认快路径的单次工作区。位置前沿、重复轮次和中间结果
// 都从同一块可复用内存切分，避免每个候选起点都产生堆分配。
type byteChainArena struct {
	ints       []int
	used       int
	repeatBuf  []int
	marks      []int32
	generation int32
}

// nextGeneration 分配一个新的位置去重代次。嵌套求值各自持有独立代次，
// 因此内层步骤不会污染外层的去重结果。
func (arena *byteChainArena) nextGeneration() int32 {
	arena.generation++
	if arena.generation <= 0 {
		clear(arena.marks)
		arena.generation = 1
	}
	return arena.generation
}

// markSeen 判断位置是否已在当前代次出现；第二个返回值表示工作区是否可覆盖
// 该位置，false 表示调用方必须退回通用求值。
func (arena *byteChainArena) markSeen(generation int32, pos int) (bool, bool) {
	if pos < 0 || pos >= byteChainMarksLimit {
		return false, false
	}
	if pos >= len(arena.marks) {
		size := max(len(arena.marks), 64)
		for size <= pos {
			size *= 2
		}
		if size > byteChainMarksLimit {
			size = byteChainMarksLimit
		}
		marks := make([]int32, size)
		copy(marks, arena.marks)
		arena.marks = marks
	}
	if arena.marks[pos] == generation {
		return true, true
	}
	arena.marks[pos] = generation
	return false, true
}

func (arena *byteChainArena) ready() bool {
	if arena == nil {
		return false
	}
	if arena.ints == nil {
		arena.ints = make([]int, byteChainArenaSize)
	}
	if arena.repeatBuf == nil {
		arena.repeatBuf = make([]int, byteChainRepeatBuf)
	}
	arena.used = 0
	return true
}

// take 从工作区切出 n 个 int，空间不足时返回 false 由调用方退回通用求值。
func (arena *byteChainArena) take(n int) ([]int, bool) {
	if n <= 0 {
		return nil, true
	}
	if arena == nil || arena.used+n > len(arena.ints) {
		return nil, false
	}
	out := arena.ints[arena.used : arena.used+n : arena.used+n]
	arena.used += n
	return out, true
}

// compactInts 原地排序并去重位置集合，与通用求值的 dedup 语义一致。
func compactInts(values []int) []int {
	if len(values) > 1 {
		if !slices.IsSorted(values) {
			slices.Sort(values)
		}
		write := 1
		for _, value := range values[1:] {
			if values[write-1] == value {
				continue
			}
			values[write] = value
			write++
		}
		values = values[:write]
	}
	return values
}

// matchByteChain 在单个起点上求值只由单字节原子、零宽断言、非捕获分组、
// 序列、选择和重复构成的表达式，返回升序去重的全部结束偏移。
// 返回 ok 为 false 表示超出快路径能力，调用方必须退回通用 AST 求值。
func matchByteChain(arena *byteChainArena, node parser.Node, data []byte, pos int, flags CompileFlag) ([]int, bool) {
	if arena == nil || !arena.ready() || pos < 0 || pos > len(data) {
		return nil, false
	}
	return arena.eval(node, data, pos, flags)
}

func (arena *byteChainArena) eval(node parser.Node, data []byte, pos int, flags CompileFlag) ([]int, bool) {
	switch v := node.(type) {
	case parser.Literal:
		if pos > len(data) || pos+len(v.Value) > len(data) {
			return nil, true
		}
		if flags&CompileCaseless == 0 {
			// 大小写敏感文字交给标准比较，命中密集时比逐字节判定更快。
			if !bytes.Equal(data[pos:pos+len(v.Value)], v.Value) {
				return nil, true
			}
		} else {
			for index := range v.Value {
				if !equalByte(v.Value[index], data[pos+index], flags) {
					return nil, true
				}
			}
		}
		out, ok := arena.take(1)
		if !ok {
			return nil, false
		}
		out[0] = pos + len(v.Value)
		return out, true
	case parser.Class:
		if pos >= len(data) || !classByteMatch(v, data[pos], flags) {
			return nil, true
		}
		out, ok := arena.take(1)
		if !ok {
			return nil, false
		}
		out[0] = pos + 1
		return out, true
	case parser.Any:
		if pos >= len(data) || data[pos] == '\n' && flags&CompileDotAll == 0 {
			return nil, true
		}
		out, ok := arena.take(1)
		if !ok {
			return nil, false
		}
		out[0] = pos + 1
		return out, true
	case parser.Assertion:
		if !assertionHolds(v.Kind, data, pos, flags) {
			return nil, true
		}
		out, ok := arena.take(1)
		if !ok {
			return nil, false
		}
		out[0] = pos
		return out, true
	case parser.Group:
		// 通用求值同样忽略捕获编号：只有反向引用和条件分支依赖捕获内容，
		// 这两类结构在入口处已由 hasCapture 排除。
		if v.Atomic || v.HasScopedFlags() {
			return nil, false
		}
		return arena.eval(v.Child, data, pos, flags)
	case parser.Sequence:
		return arena.evalSequence(v.Elements, data, pos, flags)
	case parser.Alternation:
		return arena.evalAlternation(v.Options, data, pos, flags)
	case parser.Repeat:
		return arena.evalRepeat(v, data, pos, flags)
	default:
	}
	return nil, false
}

func (arena *byteChainArena) evalSequence(elements []parser.Node, data []byte, pos int, flags CompileFlag) ([]int, bool) {
	mark := arena.used
	current, ok := arena.take(byteChainFrontier)
	if !ok {
		return nil, false
	}
	next, ok := arena.take(byteChainFrontier)
	if !ok {
		return nil, false
	}
	count := 1
	current[0] = pos
	for _, child := range elements {
		if verb, isVerb := child.(parser.ControlVerb); isVerb {
			if strings.EqualFold(verb.Name, "ACCEPT") {
				return current[:count], true
			}
			return nil, false
		}
		arena.used = mark + 2*byteChainFrontier
		if count == 1 {
			// 单一来源位置不会产生跨来源重复，子结果本身已升序去重，
			// 直接替换前沿即可跳过去重位图与排序。
			ends, ok := arena.eval(child, data, current[0], flags)
			if !ok {
				return nil, false
			}
			if len(ends) > len(current) {
				return nil, false
			}
			copy(current, ends)
			count = len(ends)
			if count == 0 {
				break
			}
			continue
		}
		generation := arena.nextGeneration()
		written := 0
		for _, start := range current[:count] {
			ends, ok := arena.eval(child, data, start, flags)
			if !ok {
				return nil, false
			}
			for _, end := range ends {
				seen, covered := arena.markSeen(generation, end)
				if !covered {
					return nil, false
				}
				if seen {
					continue
				}
				if written == len(next) {
					return nil, false
				}
				next[written] = end
				written++
			}
		}
		copy(current, next[:written])
		count = len(compactInts(current[:written]))
		if count == 0 {
			break
		}
	}
	return current[:count], true
}

func (arena *byteChainArena) evalAlternation(options []parser.Node, data []byte, pos int, flags CompileFlag) ([]int, bool) {
	out, ok := arena.take(byteChainFrontier)
	if !ok {
		return nil, false
	}
	written := 0
	for _, option := range options {
		ends, ok := arena.eval(option, data, pos, flags)
		if !ok {
			return nil, false
		}
		if written+len(ends) > len(out) {
			return nil, false
		}
		copy(out[written:], ends)
		written += len(ends)
	}
	return compactInts(out[:written]), true
}

func (arena *byteChainArena) evalRepeat(v parser.Repeat, data []byte, pos int, flags CompileFlag) ([]int, bool) {
	mark := arena.used
	current, ok := arena.take(byteChainFrontier)
	if !ok {
		return nil, false
	}
	next, ok := arena.take(byteChainFrontier)
	if !ok {
		return nil, false
	}
	results, ok := arena.take(byteChainFrontier)
	if !ok {
		return nil, false
	}
	if ends, matched := matchByteRepeatInto(v, data, pos, flags, arena.repeatBuf[:0]); matched {
		if len(ends) > len(results) {
			return nil, false
		}
		copy(results, ends)
		return compactInts(results[:len(ends)]), true
	}
	count := 1
	current[0] = pos
	written := 0
	if v.Min == 0 {
		results[0] = pos
		written = 1
	}
	budget := max(len(data)-pos+1, 1)
	maxCount := v.Max
	if maxCount < 0 || maxCount > budget {
		maxCount = budget
	}
	for round := 1; round <= maxCount; round++ {
		arena.used = mark + 3*byteChainFrontier
		if count == 1 {
			// 单来源轮次不需要跨来源去重，直接轮换前沿缓冲。
			ends, ok := arena.eval(v.Child, data, current[0], flags)
			if !ok {
				return nil, false
			}
			if len(ends) > len(next) {
				return nil, false
			}
			copy(next, ends)
			nextCount := len(ends)
			if nextCount == 0 {
				break
			}
			copy(current, next[:nextCount])
			count = nextCount
			if round >= v.Min {
				if written+count > len(results) {
					return nil, false
				}
				copy(results[written:], current[:count])
				written += count
			}
			if count == 1 && current[0] == pos {
				break
			}
			continue
		}
		generation := arena.nextGeneration()
		nextCount := 0
		for _, start := range current[:count] {
			ends, ok := arena.eval(v.Child, data, start, flags)
			if !ok {
				return nil, false
			}
			for _, end := range ends {
				seen, covered := arena.markSeen(generation, end)
				if !covered {
					return nil, false
				}
				if seen {
					continue
				}
				if nextCount == len(next) {
					return nil, false
				}
				next[nextCount] = end
				nextCount++
			}
		}
		nextCount = len(compactInts(next[:nextCount]))
		if nextCount == 0 {
			break
		}
		copy(current, next[:nextCount])
		count = nextCount
		if round >= v.Min {
			if written+count > len(results) {
				return nil, false
			}
			copy(results[written:], current[:count])
			written += count
		}
		if count == 1 && current[0] == pos {
			break
		}
	}
	return compactInts(results[:written]), true
}

func equalByte(a, b byte, flags CompileFlag) bool {
	if a == b {
		return true
	}
	if flags&CompileCaseless == 0 {
		return false
	}
	if a >= 'a' && a <= 'z' {
		a -= 32
	}
	if b >= 'a' && b <= 'z' {
		b -= 32
	}
	return a == b
}
func isWord(c byte) bool {
	return c == '_' || c >= '0' && c <= '9' || c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z'
}

func wordBefore(data []byte, pos int, flags CompileFlag) bool {
	if pos <= 0 {
		return false
	}
	if flags&CompileUTF8 != 0 && pos < len(data) && data[pos]&0xc0 == 0x80 {
		return false
	}
	if flags&CompileUCP == 0 {
		return isWord(data[pos-1])
	}
	r, _ := utf8.DecodeLastRune(data[:pos])
	return unicodeWord(r)
}
func wordAfter(data []byte, pos int, flags CompileFlag) bool {
	if pos >= len(data) {
		return false
	}
	if flags&CompileUTF8 != 0 && data[pos]&0xc0 == 0x80 {
		return false
	}
	if flags&CompileUCP == 0 {
		return isWord(data[pos])
	}
	r, _ := utf8.DecodeRune(data[pos:])
	return unicodeWord(r)
}

func unicodeWord(value rune) bool {
	return unicode.IsLetter(value) || unicode.IsNumber(value) || unicode.IsMark(value) || unicode.Is(unicode.Pc, value)
}
