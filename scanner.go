// Package scankit 提供跨平台的 Block 多规则扫描器。
package scankit

import (
	"bytes"
	"fmt"
	"math"
	"math/bits"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"github.com/smartwalle/scankit/internal/combination"
	"github.com/smartwalle/scankit/internal/compiler"
	"github.com/smartwalle/scankit/internal/dispatch"
	"github.com/smartwalle/scankit/internal/engine"
	"github.com/smartwalle/scankit/internal/fdr"
	"github.com/smartwalle/scankit/internal/fuzzy"
	"github.com/smartwalle/scankit/internal/hwlm"
	"github.com/smartwalle/scankit/internal/hwlm/noodle"
	"github.com/smartwalle/scankit/internal/hwlm/teddy"
	nfalib "github.com/smartwalle/scankit/internal/nfa"
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

type Match struct {
	Id   uint32
	From uint64
	To   uint64
}

var scannerUnicodeTables = buildScannerUnicodeTables()

func buildScannerUnicodeTables() map[string]*unicode.RangeTable {
	tables := make(map[string]*unicode.RangeTable, len(unicode.Categories)+len(unicode.Properties)+len(unicode.Scripts))
	for name, table := range unicode.Categories {
		tables[normalizeScannerUnicodeProperty(name)] = table
	}
	for name, table := range unicode.Properties {
		tables[normalizeScannerUnicodeProperty(name)] = table
	}
	for name, table := range unicode.Scripts {
		tables[normalizeScannerUnicodeProperty(name)] = table
	}
	return tables
}

func normalizeScannerUnicodeProperty(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	name = strings.ReplaceAll(name, "_", "")
	name = strings.ReplaceAll(name, "-", "")
	name = strings.ReplaceAll(name, " ", "")
	return name
}

// Scanner 是内存中的不可变编译规则执行计划，可安全地并发扫描，并管理所需的可复用扫描上下文。
type Scanner struct {
	contextPool    *sync.Pool
	roseStatePool  *sync.Pool
	roseSinglePool *sync.Pool
	// canUseRoseInScan 缓存 canUseRoseInScan 的结果，避免每次 Scan 重复遍历。
	canUseRoseInScan bool
	rules            []compiledRule
	ruleIndex        map[uint32]int
	ruleOrder        map[uint32]int
	hasCombo         bool
	literalFind      func([]byte) []literalCandidate
	literalFindInto  func([]byte, []literalCandidate) []literalCandidate
	literalKind      string
	candidateIDs     map[uint32]struct{}
	literalIDs       map[uint32]struct{}
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
	// fastLiteral/backendOnly 只依赖编译结果，构造阶段固化以避免每次扫描重复推导。
	fastLiteral bool
	backendOnly bool
	// confirmAllRules 是候选路径不可用时需要逐起点确认的规则；
	// confirmUncoveredRules 是候选路径生效后仍未被索引覆盖的规则。
	confirmAllRules       []int
	confirmUncoveredRules []int
	// startBytes/startBytesAll 是按首字节分组的逐起点确认索引，分别对应
	// confirmUncoveredRules 与 confirmAllRules。
	startBytes    *startByteIndex
	startBytesAll *startByteIndex
	usage         compiler.Usage
	validationErr error
	rosePlan      *rose.Program
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
	// chainArena 是确认快路径的单次工作区，跨候选起点复用同一块内存。
	chainArena byteChainArena
	// confirmStack 是确认程序的回溯栈，跨候选起点复用。
	confirmStack []confirmFrame
	// directMatches 复用 simpleReports 模式下的直接结果切片。
	directMatches []Match
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
}

// guardRunGroup 把共享同一字节集合的约束合并成一次线性扫描。
type guardRunGroup struct {
	set    [4]uint64
	tables simd.ByteSetTables
	// runs 按窗口长度升序排列：连续段短于某条规则的窗口时，后面的规则窗口
	// 只会更长，展开可以直接结束。
	runs []guardRun
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
	nfaEngine  *nfalib.Engine
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
	// confirmEntry 是跳过入口文字后的程序入口，confirmSkip 是跳过的文字长度。
	// confirmSkip 为 0 时从规则入口正常确认。
	confirmEntry int32
	confirmSkip  int
}

func newScanner(rules []compiledRule) *Scanner {
	copyRules := append([]compiledRule(nil), rules...)
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
				if literal := leadingLiteral(copyRules[i].root); requiredImpliesLiteral(copyRules[i].required, literal) {
					if entry, ok := copyRules[i].confirm.afterLeadingLiteral(literal); ok {
						copyRules[i].confirmEntry = entry
						copyRules[i].confirmSkip = len(literal)
					}
				}
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
		if rule.ext != nil || parser.HasScopedFlags(rule.root) || rule.flags&(FlagUTF8|FlagUCP|FlagMultiline|FlagDotAll) != 0 {
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
		literals = append(literals, hwlm.Literal{ID: rule.id, Value: append([]byte(nil), candidate...), CaseInsensitive: rule.flags&FlagCaseless != 0})
		if rule.flags&FlagCaseless == 0 {
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
			if rule.flags&(FlagQuiet|FlagSingleMatch|FlagSOMLeftmost) != 0 {
				simpleReports = false
				break
			}
		}
	}
	scanner := &Scanner{rules: copyRules, ruleIndex: index, ruleOrder: order, hasCombo: hasCombo, candidateIDs: candidateIDs, literalIDs: literalIDs, indexedRules: indexedRules, exactRules: exactRules, simpleReports: simpleReports, contextPool: &sync.Pool{New: func() any { return &scanContext{Scratch: scratch.New(), Reports: report.New()} }}, roseStatePool: &sync.Pool{New: func() any { return new([]rose.State) }}, roseSinglePool: &sync.Pool{New: func() any { return new(map[uint32]struct{}) }}}
	if len(literals) > 1 {
		scanner.literalKind, scanner.literalFind, scanner.literalFindInto = newLiteralCandidateFinder(literals)
	}
	// 无法提取必须文字的规则本来只能逐起点确认；当前缀字节约束能枚举起点时
	// 改由约束驱动，省掉整块逐起点循环。改由约束枚举的规则不再登记候选文字，
	// 避免同一规则同时走两条候选来源、在同一批命中上重复展开窗口。
	scanner.guardRunCovered = guardRunCandidates(copyRules)
	scanner.requiredLiterals, scanner.requiredFindInto, scanner.requiredCovered = buildRequiredIndex(copyRules, scanner.guardRunCovered)
	scanner.fastLiteral = !scanner.hasCombo && len(copyRules) > 1 &&
		len(candidateIDs) == len(copyRules) && len(literalIDs) == len(copyRules)
	if scanner.fastLiteral {
		for _, rule := range copyRules {
			if rule.flags&(FlagQuiet|FlagSingleMatch) != 0 {
				scanner.fastLiteral = false
				break
			}
		}
	}
	scanner.backendOnly = !scanner.hasCombo && !scanner.fastLiteral
	scanner.buildConfirmRuleLists()
	scanner.markRequiredEntrySkips()
	scanner.startBytes = newStartByteIndex(copyRules, scanner.confirmUncoveredRules)
	if len(scanner.confirmAllRules) != len(scanner.confirmUncoveredRules) {
		scanner.startBytesAll = newStartByteIndex(copyRules, scanner.confirmAllRules)
	}

	// Rose 角色图与文字候选索引随规则计划不可变，构造阶段完成一次，
	// 扫描时直接复用，避免每个 Block 重建角色、指令和自动机。
	scanner.rosePlan = scanner.buildRoseProgram()
	scanner.canUseRoseInScan = scanner.computeCanUseRoseInScan()
	// 编译结果不可变，计划校验只需在构造时执行一次；扫描热路径复用该结论。
	scanner.validationErr = scanner.validate()
	return scanner
}

// requiredIndexEligible 判断规则能否安全进入必须文字候选索引。
// 大小写折叠、UTF-8/UCP 会改变文字或字符类的匹配语义，作用域修饰符同理。
func requiredIndexEligible(rule compiledRule) bool {
	if len(rule.required) == 0 {
		return false
	}
	if rule.flags&(FlagCaseless|FlagUTF8|FlagUCP) != 0 {
		return false
	}
	// 模糊匹配允许匹配文字本身发生变化，精确文字候选会漏报。
	if rule.ext != nil && rule.ext.Flags&(ExtFlagEditDistance|ExtFlagHammingDistance) != 0 {
		return false
	}
	return !parser.HasScopedFlags(rule.root)
}

// buildRequiredIndex 为可安全提取必须文字的规则建立共享候选索引。
// 返回的 covered 标记哪些规则真正进入索引：无法提取必须文字、或偏移窗口无法
// 约束的规则保留原确认路径，不会让整个索引失效。窗口上界为负代表偏移无上界，
// 此时必须携带 Back 字节集合才能把候选收缩到有限起点。
func buildRequiredIndex(rules []compiledRule, guardDriven []bool) ([]requiredLiteral, func([]byte, []requiredHit) []requiredHit, []bool) {
	byValue := make(map[string]int)
	literals := make([]hwlm.Literal, 0, 64)
	index := make([]requiredLiteral, 0, 64)
	covered := make([]bool, len(rules))
	for ruleIndex := range rules {
		rule := rules[ruleIndex]
		if !requiredIndexEligible(rule) || !requiredWindowUsable(rule.required) {
			continue
		}
		if ruleIndex < len(guardDriven) && guardDriven[ruleIndex] {
			continue
		}
		for _, variant := range rule.required {
			key := string(variant.Value)
			position, ok := byValue[key]
			if !ok {
				position = len(index)
				byValue[key] = position
				index = append(index, requiredLiteral{})
				literals = append(literals, hwlm.Literal{ID: uint32(position + 1), Value: variant.Value})
			}
			entry := requiredEntry{ruleID: rule.id, ruleIndex: ruleIndex, min: variant.MinOffset, max: variant.MaxOffset, back: variant.Back, guard: rule.guard}
			if guardImpliedByBack(entry.guard, variant.Back, variant.MinOffset) {
				entry.guard = nil
			}
			index[position].entries = append(index[position].entries, entry)
		}
		covered[ruleIndex] = true
	}
	if len(index) == 0 {
		return nil, nil, nil
	}
	return index, newRequiredFinder(literals), covered
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

// newRequiredFinder 按候选文字规模选择匹配器，并把每次扫描的命中缓冲
// 放入对象池，避免为每条命中重新分配临时切片。调用方 requiredScanStarts
// 会重新按起点排序并在派生的起点上去重，因此这里统一使用无序输出，
// 命中密集的语料上不再为同一批候选重复排序。
//
// 各分支把匹配器命中直接填入 requiredHit，避免为每条命中经过函数值调用；
// 命中密集时这一步是候选展开前的固定开销，改用直写的转换循环可让它随
// 命中数线性摊薄。
func newRequiredFinder(literals []hwlm.Literal) func([]byte, []requiredHit) []requiredHit {
	short, long := splitRequiredLiterals(literals)
	if len(long) == 0 {
		// 全部候选文字都是单字节：此时拆分拿不到任何 lane 收益，通用匹配器
		// （FDR/Teddy）在稀疏命中语料上的跳过能力反而更好。
		return newLiteralMatcherFinder(literals)
	}
	finders := make([]func([]byte, []requiredHit) []requiredHit, 0, 2)
	finders = append(finders, newLiteralMatcherFinder(long))
	if len(short) > 0 {
		finders = append(finders, newSingleByteFinder(short))
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

// splitRequiredLiterals 把候选文字按长度分成"单字节"与"多字节"两组。候选文字
// 中的单字节成员会把匹配器的最短文字压到 1，候选掩码随之退化成"首字节集合"，
// 几乎覆盖全部输入字节；拆开之后多字节匹配器可以启用 2 个以上 lane，掩码只保留
// 连续多字节共同命中的位置。
func splitRequiredLiterals(literals []hwlm.Literal) (short, long []hwlm.Literal) {
	for _, literal := range literals {
		if len(literal.Value) <= 1 {
			short = append(short, literal)
			continue
		}
		long = append(long, literal)
	}
	return short, long
}

// newSingleByteFinder 为长度为 1 的候选文字构建专用匹配器：命中判定只取决于当前
// 字节，因此用一次 SIMD 字节集合扫描求出全部命中位置，再用 256 项查表换算成
// 候选编号，省掉通用匹配器的分桶确认。
func newSingleByteFinder(literals []hwlm.Literal) func([]byte, []requiredHit) []requiredHit {
	var table [256]uint32
	var set [4]uint64
	for _, literal := range literals {
		if len(literal.Value) == 0 {
			continue
		}
		value := literal.Value[0]
		table[value] = literal.ID
		set[value>>6] |= uint64(1) << uint(value&63)
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
			dst = growRequiredHits(dst, len(matches))
			for index := range matches {
				dst = append(dst, requiredHit{slot: int(matches[index].ID) - 1, pos: matches[index].From})
			}
			if cap(matches) > 1<<20 {
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
			dst = growRequiredHits(dst, len(matches))
			for index := range matches {
				dst = append(dst, requiredHit{slot: int(matches[index].ID) - 1, pos: matches[index].From})
			}
			if cap(matches) > 1<<20 {
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
			dst = growRequiredHits(dst, len(matches))
			for index := range matches {
				dst = append(dst, requiredHit{slot: int(matches[index].ID) - 1, pos: matches[index].From})
			}
			if cap(matches) > 1<<20 {
				*buf = nil
			}
			pool.Put(buf)
			return dst
		}
	}
}

// growRequiredHits 按本次命中数把目标切片调整到可复用的空切片。
func growRequiredHits(dst []requiredHit, need int) []requiredHit {
	if cap(dst) < need {
		return make([]requiredHit, 0, need)
	}
	return dst[:0]
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
			if cap(matches) > 1<<20 {
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
			if cap(matches) > 1<<20 {
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
			if cap(matches) > 1<<20 {
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
					role := rose.Role{ID: rule.id, Literal: literal, ReportID: rule.id, CaseInsensitive: rule.flags&FlagCaseless != 0, Confirm: true}
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
			role := rose.Role{ID: roleID, Literal: literal, ReportID: rule.id, CaseInsensitive: rule.flags&FlagCaseless != 0, Anchored: anchored, EndOfData: endOfData, Confirm: confirm}
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

// scanRoseInto 使用独立角色调度器扫描可转换文字，并复用调用方提供的结果切片。
func (scanner *Scanner) scanRoseInto(data []byte, dst []Match) []Match {
	program := scanner.roseProgram()
	if program == nil {
		return dst[:0]
	}
	scheduler := rose.NewScheduler(program)
	events := scheduler.RunReports(data)
	out := dst[:0]
	seen := make(map[Match]struct{})
	confirmEnds := make(map[uint32]map[uint64]uint64)
	singleSeen := make(map[uint32]bool)
	for _, event := range events {
		role, ok := program.FindRole(event.ID)
		// 多个角色可以共享同一报告编号；必须按候选偏移选择真正命中的角色，
		// 否则第一个分支会吞掉后续分支的确认结果。
		if !ok || !role.MatchAt(data, int(event.From)) {
			ok = false
			// 同一报告编号可能对应多个角色候选，按扫描代价从低到高选出真正命中的代表。
			for _, candidate := range program.RolesForReport(event.ID) {
				if candidate.MatchAt(data, int(event.From)) {
					role, ok = candidate, true
					break
				}
			}
		}
		if !ok {
			// 角色候选可能因前缀、边界或指令条件失效；候选不是最终
			// 结果，使用对应规则的完整确认器重新验证当前起点。
			if index, exists := scanner.ruleIndex[event.ID]; exists && index >= 0 && index < len(scanner.rules) {
				rule := scanner.rules[index]
				if rule.flags&FlagQuiet == 0 {
					for _, end := range matchRule(rule, data, int(event.From)) {
						if rule.ext != nil && (!rule.ext.OffsetAllowed(uint64(end)) || !rule.ext.LengthAllowed(uint64(end)-event.From)) {
							continue
						}
						match := Match{Id: rule.id, From: event.From, To: uint64(end)}
						if _, exists := seen[match]; !exists {
							seen[match] = struct{}{}
							out = append(out, match)
						}
					}
				}
			}
			continue
		}
		index, ok := scanner.ruleIndex[role.ReportID]
		if !ok {
			continue
		}
		rule := scanner.rules[index]
		if rule.flags&FlagQuiet != 0 {
			continue
		}
		if !role.Confirm {
			if singleSeen[rule.id] {
				continue
			}
			// 直接文字角色已经完成整段确认，仅需应用报告指令和扩展限制。
			if rule.ext != nil && (!rule.ext.OffsetAllowed(event.To) || !rule.ext.LengthAllowed(event.To-event.From)) {
				continue
			}
			instructions := program.InstructionsCopy()
			emitted := false
			hasInstruction := false
			for _, instruction := range instructions {
				if instruction.RoleID != role.ID {
					continue
				}
				hasInstruction = true
				if instruction.Kind != rose.InstructionReport || instruction.Flags&report.FlagQuiet != 0 {
					continue
				}
				id := instruction.ReportID
				if id == 0 {
					id = role.ReportID
				}
				match := Match{Id: id, From: event.From, To: event.To}
				if _, exists := seen[match]; exists {
					continue
				}
				seen[match] = struct{}{}
				out = append(out, match)
				emitted = true
				if instruction.Flags&report.FlagSingleMatch != 0 {
					singleSeen[rule.id] = true
				}
			}
			if !emitted && !hasInstruction {
				match := Match{Id: role.ReportID, From: event.From, To: event.To}
				if _, exists := seen[match]; !exists {
					seen[match] = struct{}{}
					out = append(out, match)
				}
				if rule.flags&FlagSingleMatch != 0 {
					singleSeen[rule.id] = true
				}
			}
			continue
		}
		if singleSeen[rule.id] {
			continue
		}
		startFrom := 0
		if rule.info.MaxLength != math.MaxUint64 && rule.info.MaxLength <= uint64(event.To) {
			startFrom = int(event.To - rule.info.MaxLength)
		}
		for start := startFrom; start <= int(event.From); start++ {
			for _, end := range matchRule(rule, data, start) {
				if end < int(event.From)+len(role.Literal) || start > int(event.From) {
					continue
				}
				if rule.ext != nil && (!rule.ext.OffsetAllowed(uint64(end)) || !rule.ext.LengthAllowed(uint64(end-start))) {
					continue
				}
				match := Match{Id: role.ReportID, From: uint64(start), To: uint64(end)}
				if confirmEnds[role.ReportID] == nil {
					confirmEnds[role.ReportID] = make(map[uint64]uint64)
				}
				if previous, exists := confirmEnds[role.ReportID][uint64(end)]; exists && previous <= uint64(start) {
					continue
				}
				confirmEnds[role.ReportID][uint64(end)] = uint64(start)
				if _, exists := seen[match]; !exists {
					seen[match] = struct{}{}
					out = append(out, match)
					if rule.flags&FlagSingleMatch != 0 {
						singleSeen[rule.id] = true
					}
				}
				if singleSeen[rule.id] {
					break
				}
			}
		}
	}
	return out
}

func (scanner *Scanner) literalCandidateBackend() string {
	if scanner == nil {
		return ""
	}
	return scanner.literalKind
}
func (scanner *Scanner) ruleIDs() []uint32 {
	if scanner == nil {
		return nil
	}
	out := make([]uint32, len(scanner.rules))
	for i, rule := range scanner.rules {
		out[i] = rule.id
	}
	return out
}
func (scanner *Scanner) expressionInfo(id uint32) (compiler.ExpressionInfo, bool) {
	if scanner == nil {
		return compiler.ExpressionInfo{}, false
	}
	index, ok := scanner.ruleIndex[id]
	if !ok || index < 0 || index >= len(scanner.rules) {
		return compiler.ExpressionInfo{}, false
	}
	return scanner.rules[index].info.Clone(), true
}
func (scanner *Scanner) backendName(id uint32) string {
	if scanner == nil {
		return ""
	}
	index, ok := scanner.ruleIndex[id]
	if !ok || index < 0 || index >= len(scanner.rules) {
		return ""
	}
	rule := scanner.rules[index]
	if rule.comb != nil {
		return "combination"
	}
	if rule.smallWrite != nil {
		return "smallwrite"
	}
	if rule.smallBlock != nil {
		return "smallblock"
	}
	if rule.repeat != nil {
		return "repeat"
	}
	if rule.program != nil {
		return rule.program.BackendName()
	}
	return "ast"
}
func (scanner *Scanner) ruleFlags(id uint32) (CompileFlag, bool) {
	if scanner == nil {
		return 0, false
	}
	index, ok := scanner.ruleIndex[id]
	if !ok || index < 0 || index >= len(scanner.rules) {
		return 0, false
	}
	return scanner.rules[index].flags, true
}
func (scanner *Scanner) ruleExtension(id uint32) (*ExpressionExt, bool) {
	if scanner == nil {
		return nil, false
	}
	index, ok := scanner.ruleIndex[id]
	if !ok || index < 0 || index >= len(scanner.rules) {
		return nil, false
	}
	return scanner.rules[index].ext.Clone(), true
}
func (scanner *Scanner) clone() *Scanner {
	if scanner == nil {
		return nil
	}
	out := newScanner(scanner.rules)
	out.usage = scanner.usage
	return out
}

// backendEligible 判断规则是否可以直接使用已编译后端完成单次扫描。
func backendEligible(rule compiledRule) bool {
	if rule.program == nil || rule.repeat != nil || rule.comb != nil || rule.ext != nil || rule.containsAny || rule.requiresEndOfData || rule.flags&FlagSOMLeftmost != 0 {
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
		return rule.flags&(FlagCaseless|FlagUTF8|FlagUCP|FlagMultiline|FlagDotAll|FlagSingleMatch|FlagQuiet) == 0
	}
	literal, ok := literalPattern(rule.root)
	if !ok || len(literal) == 0 || hasLiteralSelfOverlap(literal) {
		return false
	}
	return rule.flags&(FlagCaseless|FlagUTF8|FlagUCP|FlagMultiline|FlagDotAll|FlagSingleMatch|FlagQuiet) == 0 && !rule.info.RequiresStatefulRuntime()
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
	max := uint64(^uint(0) >> 1)
	if uint64(value) > max {
		return int(max)
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
				var spanBuf [32]nfalib.Span
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
	pool := scanner.contextPool
	if pool == nil {
		pool = &sync.Pool{New: func() any { return &scanContext{Scratch: scratch.New(), Reports: report.New()} }}
	}
	ctx, _ := pool.Get().(*scanContext)
	if ctx == nil {
		ctx = &scanContext{Scratch: scratch.New(), Reports: report.New()}
	}
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
		if cap(ctx.requiredHits) > 1<<20 {
			ctx.requiredHits = nil
		} else {
			ctx.requiredHits = ctx.requiredHits[:0]
		}
		if cap(ctx.requiredStarts) > 1<<20 {
			ctx.requiredStarts = nil
		} else {
			ctx.requiredStarts = ctx.requiredStarts[:0]
		}
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
		pool.Put(ctx)
	}()
	// 每个起点都必须独立求值，块模式允许同一规则产生重叠命中。
	blockedUntil := reserveInts(ctx.blockedUntil, len(scanner.rules))
	fired := reserveBools(ctx.fired, len(scanner.rules))
	ctx.blockedUntil = blockedUntil
	ctx.fired = fired
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
	} else if len(scanner.candidateIDs) == 1 && len(scanner.literalIDs) == 1 {
		if literalEnds == nil {
			literalEnds = make(map[int]map[uint32]int)
			ctx.literalEnds = literalEnds
		}
		resetLiteralEnds(literalEnds)
		scanner.fillSingleLiteralEnds(data, literalEnds)
	}
	for _, rule := range scanner.rules {
		// 编译期为所有规则推导出的候选文字只在调用方显式开启 FlagPrefilter 时
		// 参与起点过滤（见 verify），其余规则建立起点表属于无效工作。
		if rule.flags&FlagPrefilter == 0 || len(rule.prefilter) == 0 || scanner.literalFind != nil {
			continue
		}
		if _, indexed := scanner.candidateIDs[rule.id]; indexed && literalEnds != nil {
			continue
		}
		positions := hwlm.FindAll(data, hwlm.Literal{Value: rule.prefilter, CaseInsensitive: rule.flags&FlagCaseless != 0})
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
		if rule.flags&(FlagQuiet|FlagSingleMatch) == 0 {
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
func guardRunCandidates(rules []compiledRule) []bool {
	var promoted []bool
	for ruleIndex := range rules {
		if _, ok := guardRunFor(ruleIndex, &rules[ruleIndex]); !ok {
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
	if !ok || length < guardRunMinLength {
		return guardRun{}, false
	}
	return guardRun{
		ruleIndex:     ruleIndex,
		set:           set,
		offset:        offset,
		length:        length,
		guard:         rule.guard,
		prefixCovered: offset == 0 && length == len(rule.guard.sets),
	}, true
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
	}
	return groups
}

// requiredIndexDriven 判断本次扫描是否存在候选起点索引（必须文字或前缀字节
// 约束）。任一来源生效时都必须走候选驱动路径，否则这两类规则会退化成整块
// 逐起点确认。判定只依赖构造期固化的字段，因此在确认规则集合构建之前即可用。
func (scanner *Scanner) requiredIndexDriven() bool {
	return scanner.requiredFindInto != nil || scanner.guardRunCovered != nil
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

// requiredIndexCovered 判断规则的候选起点是否已由候选索引（必须文字或前缀
// 字节约束）枚举，覆盖的规则不再参与整块后端扫描。
func (scanner *Scanner) requiredIndexCovered(ri int) bool {
	if scanner.requiredCovered != nil && scanner.requiredCovered[ri] {
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
	if rule.hasCapture || rule.ext != nil || rule.flags&(FlagUTF8|FlagUCP) != 0 {
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
	if rule.flags&FlagPrefilter != 0 && len(rule.prefilter) > 0 {
		if starts, indexed := st.prefilterStarts[rule.id]; indexed {
			if _, ok := starts[start]; !ok {
				return true
			}
		} else if start+len(rule.prefilter) > len(st.data) || !hwlm.Contains(st.data[start:start+len(rule.prefilter)], hwlm.Literal{Value: rule.prefilter, CaseInsensitive: rule.flags&FlagCaseless != 0}) {
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
		// 候选由必须文字索引派生时，起点处的入口文字已由匹配器确认，
		// 可以直接从入口文字之后的指令开始求值。
		if rule.confirmSkip > 0 && st.requiredStartDriven && st.scanner.literalIndexCovered(ri) {
			end, ok = rule.confirm.run(st.data, rule.confirmEntry, start+rule.confirmSkip, rule.flags, st.ctx.confirmFrames())
		} else {
			end, ok = rule.confirm.preferredEnd(st.data, start, rule.flags, st.ctx.confirmFrames())
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
	ends := matchRuleIntoArena(rule, st.data, start, st.ctx.ruleMatchBuf[:0], arena)
	st.ctx.ruleMatchBuf = ends
	return st.record(ri, start, ends)
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
	if rule.flags&FlagSOMLeftmost != 0 {
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
	quiet := rule.flags&FlagQuiet != 0
	if quiet {
		if rule.flags&FlagSingleMatch != 0 {
			st.fired[ri] = true
		}
		return true
	}
	st.ctx.Reports.Add(report.Event{ID: rule.id, From: uint64(start), To: uint64(end), SOM: uint64(start), Flags: uint32(rule.flags)})
	if end > start {
		st.blockedUntil[ri] = end
	}
	if rule.flags&FlagSingleMatch != 0 {
		st.fired[ri] = true
	}
	return true
}

// finish 收尾组合报告并将事件转换为结果切片。
func (st *blockScanState) finish(matches []Match) ([]Match, error) {
	if st.scanner.simpleReports {
		out := append(matches, st.direct...)
		st.ctx.directMatches = st.direct[:0]
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
				from = hit.pos - entry.max
				if from < 0 {
					from = 0
				}
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
			// 窗口较宽时同一次文字命中要摊开成多个起点，其中绝大多数连规则
			// 前缀都过不了；先做一次约束查表，避免为它们建立候选并启动确认程序。
			guard := entry.guard
			for start := from; start <= to; start++ {
				if len(starts) >= limit {
					return nil, false
				}
				if !guard.allows(data, start) {
					continue
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
		for index := 0; index < width; index++ {
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
	first := from - run.offset
	if first < 0 {
		first = 0
	}
	last := to - run.length - run.offset
	count := last - first + 1
	if count <= 0 {
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
	for index := 0; index < count; index++ {
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
		window.emitRange(off+beginBit, off+endBit+1)
	}
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
			first := runStart - run.offset
			if first < 0 {
				first = 0
			}
			last := index - run.length - run.offset
			for start := first; start <= last; start++ {
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

// 候选起点线性排序的启用门槛：起点计数表按语料长度线性增长，语料过大时
// 直接退回比较排序，避免为排序申请成倍于语料的内存。
const (
	requiredCountingSortMin     = 8192
	requiredCountingSortMaxKeys = 1 << 21
	requiredCountingSortMaxData = 1 << 20
)

// requiredBucketMaxShift 限制候选分桶排序的桶跨度。桶跨度按候选密度自适应：
// 目标是每个桶平均不超过两个候选，桶内再用插入排序恢复完整顺序。
const requiredBucketMaxShift = 16

// sortRequiredStarts 把候选键按 (起点, 规则下标) 升序整理。
// 候选先按 "起点 >> shift" 分桶，桶内元素数量接近常数，再用插入排序恢复
// 桶内的完整顺序；两次内置循环都是候选数量的线性函数，比整表计数排序少了
// 一轮直方图和一次全量搬运。任一门槛不满足时退回 slices.Sort。
func (scanner *Scanner) sortRequiredStarts(ctx *scanContext, starts []uint64, dataLen int) {
	keyCount := len(starts)
	if keyCount < requiredCountingSortMin || keyCount > requiredCountingSortMaxKeys || dataLen > requiredCountingSortMaxData {
		slices.Sort(starts)
		return
	}
	if cap(ctx.requiredSortScratch) < keyCount {
		ctx.requiredSortScratch = make([]uint64, keyCount)
	} else {
		ctx.requiredSortScratch = ctx.requiredSortScratch[:keyCount]
	}
	scratch := ctx.requiredSortScratch
	shift := 0
	for shift < requiredBucketMaxShift && dataLen>>(shift+1) > keyCount/2 {
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
		scratch[counts[bucket]] = key
		counts[bucket]++
	}
	// 每个桶覆盖至多 2^shift 个相邻起点，桶内按完整键插入排序即可得到
	// (起点, 规则下标) 升序；桶内元素数量接近常数，这一步是线性代价。
	begin := 0
	for index := range buckets {
		end := int(counts[index])
		for current := begin + 1; current < end; current++ {
			key := scratch[current]
			position := current - 1
			for position >= begin && scratch[position] > key {
				scratch[position+1] = scratch[position]
				position--
			}
			scratch[position+1] = key
		}
		begin = end
	}
	copy(starts, scratch)
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
	const maxRetained = 1 << 21
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
		if _, _, _, direct := roseLiteralPattern(alternatives[0]); !direct {
			return false
		}
		if _, ok := covered[rule.id]; !ok {
			return false
		}
	}
	return true
}

// scanRoseDirectInto 将无确认文字角色的候选直接转换为 Block 结果。
// 角色索引已经在计划构造阶段完成，运行时只保留 SingleMatch 的最小状态。
func (scanner *Scanner) scanRoseDirectInto(data []byte, dst []Match) []Match {
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
	// 复用 SingleMatch 规则的去重表，避免每次扫描都分配 map。
	singlePtr, _ := scanner.roseSinglePool.Get().(*map[uint32]struct{})
	if singlePtr == nil {
		singlePtr = new(map[uint32]struct{})
	}
	single := *singlePtr
	defer func() {
		// 池中 map 的桶大小不应随单次扫描无限增长，超出限制时丢弃避免泄漏。
		if len(single) > 1<<16 {
			*singlePtr = nil
		} else {
			clear(single)
		}
		scanner.roseSinglePool.Put(singlePtr)
	}()
	for _, state := range states {
		// 只读场景使用 RoleByID 避免对 Literal 做防御性复制。
		role, ok := program.RoleByID(state.RoleID)
		if !ok {
			continue
		}
		index, ok := scanner.ruleIndex[role.ReportID]
		if !ok || index < 0 || index >= len(scanner.rules) {
			continue
		}
		rule := scanner.rules[index]
		if rule.flags&FlagQuiet != 0 {
			continue
		}
		if rule.flags&FlagSingleMatch != 0 {
			if _, exists := single[rule.id]; exists {
				continue
			}
			single[rule.id] = struct{}{}
		}
		end := state.Offset + uint64(len(role.Literal))
		out = append(out, Match{Id: role.ReportID, From: state.Offset, To: end})
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
			if rule.flags&FlagSingleMatch != 0 {
				fired[rule.id] = true
			}
			if rule.flags&FlagQuiet != 0 || emitted[rule.id] || eod && emittedRule[rule.id] {
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

func (scanner *Scanner) findSingleLiteralEnds(data []byte) map[int]map[uint32]int {
	if scanner == nil {
		return nil
	}
	ends := make(map[int]map[uint32]int)
	scanner.fillSingleLiteralEnds(data, ends)
	return ends
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

func hasNonGreedy(n parser.Node) bool {
	switch v := n.(type) {
	case parser.Repeat:
		return !v.Greedy || hasNonGreedy(v.Child)
	case parser.Lookaround:
		return hasNonGreedy(v.Child)
	case parser.Conditional:
		return hasNonGreedy(v.Yes) || hasNonGreedy(v.No)
	case parser.Group:
		return hasNonGreedy(v.Child)
	case parser.Sequence:
		for _, c := range v.Elements {
			if hasNonGreedy(c) {
				return true
			}
		}
	case parser.Alternation:
		for _, c := range v.Options {
			if hasNonGreedy(c) {
				return true
			}
		}
	}
	return false
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
		}
		return false, false
	}
	value, ok := walk(n)
	return ok && value
}

func matchRule(rule compiledRule, data []byte, start int) []int {
	return matchRuleInto(rule, data, start, nil)
}

// matchRuleInto 在已确认快路径规则上复用调用方提供的结束偏移缓冲，
// 避免每次起点重新分配结果切片。Fuzzy、Backreference、Conditional 仍返回新切片。
func matchRuleInto(rule compiledRule, data []byte, start int, endsBuf []int) []int {
	return matchRuleIntoArena(&rule, data, start, endsBuf, nil)
}

// matchRuleIntoArena 是 matchRuleInto 的工作区版本：arena 非空时优先用
// 字节链快路径求值全部结束偏移，超出自持能力再退回通用后端与 AST 求值。
func matchRuleIntoArena(rule *compiledRule, data []byte, start int, endsBuf []int, arena *byteChainArena) []int {
	if rule.hasBackref || rule.hasConditional {
		states := matchCaptured(rule.root, data, start, rule.flags, make(map[int][]byte))
		out := make([]int, 0, len(states))
		for _, state := range states {
			out = append(out, state.pos)
		}
		return dedup(out)
	}
	if rule.ext == nil && rule.flags&(FlagCaseless|FlagUTF8|FlagUCP|FlagMultiline|FlagDotAll) == 0 {
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
		if paths, ok := fuzzyAtomPaths(rule.root); ok && rule.flags&(FlagUTF8|FlagUCP) == 0 {
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
			out := []int{}
			for _, literal := range patterns {
				if rule.ext.Flags&ExtFlagHammingDistance != 0 {
					end := start + len(literal)
					if end > len(data) {
						continue
					}
					within := fuzzy.WithinHamming(literal, data[start:end], uint32ToInt(rule.ext.HammingDistance))
					if rule.flags&FlagCaseless != 0 {
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
				max := maxInt
				if max64 < uint64(maxInt) {
					max = int(max64)
				}
				low := len(literal) - max
				if low < 0 {
					low = 0
				}
				high := len(data) - start
				if max <= maxInt-len(literal) && len(literal)+max < high {
					high = len(literal) + max
				}
				for n := low; n <= high; n++ {
					within := fuzzy.WithinEdit(literal, data[start:start+n], max)
					if rule.flags&FlagCaseless != 0 {
						within = fuzzy.WithinEditFoldASCII(literal, data[start:start+n], max)
					}
					if within {
						out = append(out, start+n)
					}
				}
			}
			return dedup(out)
		}
	}
	if rule.program != nil && !rule.eodReports && !rule.info.RequiresStatefulRuntime() && rule.flags&(FlagCaseless|FlagUTF8|FlagUCP|FlagMultiline|FlagDotAll) == 0 && !containsAny(rule.root) {
		return rule.program.MatchAt(data, start)
	}
	return matchNode(rule.root, data, start, rule.flags)
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

func fuzzyAtoms(n parser.Node) ([]fuzzyAtom, bool) {
	switch v := n.(type) {
	case parser.Literal:
		out := make([]fuzzyAtom, len(v.Value))
		for i, b := range v.Value {
			value := b
			out[i].literal = &value
		}
		return out, len(out) > 0
	case parser.Class:
		c := v
		return []fuzzyAtom{newFuzzyClass(c)}, true
	case parser.Any:
		return []fuzzyAtom{{any: true}}, true
	case parser.Group:
		if v.HasScopedFlags() || v.Atomic {
			return nil, false
		}
		return fuzzyAtoms(v.Child)
	case parser.Sequence:
		out := make([]fuzzyAtom, 0)
		for _, child := range v.Elements {
			part, ok := fuzzyAtoms(child)
			if !ok || len(out)+len(part) > 256 {
				return nil, false
			}
			out = append(out, part...)
		}
		return out, len(out) > 0
	case parser.Repeat:
		if v.Max < 0 || v.Min != v.Max || v.Min == 0 || v.Min > 32 {
			return nil, false
		}
		part, ok := fuzzyAtoms(v.Child)
		if !ok || len(part)*v.Min > 256 {
			return nil, false
		}
		out := make([]fuzzyAtom, 0, len(part)*v.Min)
		for i := 0; i < v.Min; i++ {
			out = append(out, part...)
		}
		return out, true
	default:
		return nil, false
	}
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
	if len(atoms) == 0 || maxDistance < 0 || flags&(FlagUTF8|FlagUCP) != 0 || start < 0 || start > len(data) || len(atoms) > len(data)-start {
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
	if len(atoms) == 0 || maxDistance < 0 || flags&(FlagUTF8|FlagUCP) != 0 || start < 0 || start > len(data) {
		return nil, false
	}
	minLen := len(atoms) - maxDistance
	if minLen < 0 {
		minLen = 0
	}
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
		lo := i + 1 - maxDistance
		if lo < 1 {
			lo = 1
		}
		hi := i + 1 + maxDistance
		if hi > maxLen {
			hi = maxLen
		}
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

func editAtomsWithin(atoms []fuzzyAtom, data []byte, maxDistance int, flags CompileFlag) bool {
	prev := make([]int, len(data)+1)
	cur := make([]int, len(data)+1)
	return editAtomsWithinBuffer(atoms, data, maxDistance, flags, prev, cur)
}

// editAtomsWithinBuffer 使用调用方提供的双行缓冲区确认编辑距离，避免候选宽度循环重复分配。
func editAtomsWithinBuffer(atoms []fuzzyAtom, data []byte, maxDistance int, flags CompileFlag, prev, cur []int) bool {
	if maxDistance < 0 || len(data) == int(^uint(0)>>1) || len(prev) < len(data)+1 || len(cur) < len(data)+1 {
		return false
	}
	prev = prev[:len(data)+1]
	cur = cur[:len(data)+1]
	for j := range prev {
		prev[j] = j
	}
	for i, atom := range atoms {
		cur[0] = i + 1
		for j, b := range data {
			cost := 1
			if fuzzyAtomMatches(atom, b, flags) {
				cost = 0
			}
			cur[j+1] = minInt(prev[j+1]+1, cur[j]+1, prev[j]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(data)] <= maxDistance
}

func fuzzyAtomMatches(atom fuzzyAtom, value byte, flags CompileFlag) bool {
	if atom.any {
		return flags&FlagDotAll != 0 || value != '\n'
	}
	if atom.literal != nil {
		return equalByte(*atom.literal, value, flags)
	}
	if atom.class == nil {
		return false
	}
	if atom.hasMask && flags&FlagCaseless == 0 {
		return atom.mask[value/64]&(1<<uint(value%64)) != 0
	}
	matched := false
	for _, r := range atom.class.Ranges {
		if value >= r.Lo && value <= r.Hi {
			matched = true
			break
		}
	}
	if !matched && flags&FlagCaseless != 0 {
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
	min := values[0]
	for _, value := range values[1:] {
		if value < min {
			min = value
		}
	}
	return min
}

func containsConditional(n parser.Node) bool {
	switch v := n.(type) {
	case parser.Conditional:
		return true
	case parser.Group:
		return containsConditional(v.Child)
	case parser.Sequence:
		for _, child := range v.Elements {
			if containsConditional(child) {
				return true
			}
		}
	case parser.Alternation:
		for _, child := range v.Options {
			if containsConditional(child) {
				return true
			}
		}
	case parser.Repeat:
		return containsConditional(v.Child)
	case parser.Lookaround:
		return containsConditional(v.Child)
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
		for _, child := range v.Elements {
			if containsAny(child) {
				return true
			}
		}
	case parser.Alternation:
		for _, child := range v.Options {
			if containsAny(child) {
				return true
			}
		}
	case parser.Repeat:
		return containsAny(v.Child)
	case parser.Lookaround:
		return containsAny(v.Child)
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
		out := []byte{}
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

func matchNode(n parser.Node, data []byte, pos int, flags CompileFlag) []int {
	if containsCaptureSemantics(n) {
		states := matchCaptured(n, data, pos, flags, map[int][]byte{})
		out := make([]int, 0, len(states))
		for _, state := range states {
			out = append(out, state.pos)
		}
		return dedup(out)
	}
	return matchNodeBody(n, data, pos, flags)
}

// matchNodeBody 执行不涉及捕获语义的递归求值。捕获检查只在整个子树的入口
// 做一次，避免每个子节点重复遍历语法树。
func matchNodeBody(n parser.Node, data []byte, pos int, flags CompileFlag) []int {
	switch v := n.(type) {
	case parser.Literal:
		if flags&FlagUTF8 != 0 {
			if !utf8.Valid(v.Value) || pos > 0 && pos < len(data) && data[pos]&0xc0 == 0x80 {
				return nil
			}
			if flags&FlagCaseless != 0 {
				return matchUTF8Literal(v.Value, data, pos)
			}
		}
		if pos+len(v.Value) > len(data) {
			return nil
		}
		if flags&FlagUTF8 != 0 && !validUTF8LiteralAt(data, pos, len(v.Value)) {
			return nil
		}
		for i, c := range v.Value {
			if !equalByte(c, data[pos+i], flags) {
				return nil
			}
		}
		return []int{pos + len(v.Value)}
	case parser.Any:
		if pos >= len(data) || (data[pos] == '\n' && flags&FlagDotAll == 0) {
			return nil
		}
		if flags&FlagUTF8 != 0 {
			r, size := utf8.DecodeRune(data[pos:])
			if r == utf8.RuneError && size == 1 && data[pos] >= 0x80 {
				return nil
			}
			if size == 0 {
				size = 1
			}
			return []int{pos + size}
		}
		return []int{pos + 1}
	case parser.Class:
		if pos >= len(data) {
			return nil
		}
		if flags&FlagUCP != 0 && v.Kind != parser.ClassNormal {
			runeValue, size := utf8.DecodeRune(data[pos:])
			if size == 0 || runeValue == utf8.RuneError && size == 1 && data[pos] >= 0x80 {
				return nil
			}
			matched := unicodeShorthandClass(runeValue, v.Kind)
			if matched != v.Negated {
				return []int{pos + size}
			}
			return nil
		}
		if flags&FlagUTF8 != 0 {
			runeValue, size := utf8.DecodeRune(data[pos:])
			if size == 0 || runeValue == utf8.RuneError && size == 1 && data[pos] >= 0x80 {
				return nil
			}
			matched := false
			if runeValue <= 0xff {
				value := byte(runeValue)
				for _, r := range v.Ranges {
					if value >= r.Lo && value <= r.Hi || flags&FlagCaseless != 0 && ((value >= 'a' && value <= 'z' && value-'a'+'A' >= r.Lo && value-'a'+'A' <= r.Hi) || (value >= 'A' && value <= 'Z' && value-'A'+'a' >= r.Lo && value-'A'+'a' <= r.Hi)) {
						matched = true
						break
					}
				}
			}
			if matched != v.Negated {
				return []int{pos + size}
			}
			return nil
		}
		if classByteMatch(v, data[pos], flags) {
			return []int{pos + 1}
		}
		return nil
	case parser.UnicodeClass:
		if flags&FlagUTF8 == 0 || pos >= len(data) {
			return nil
		}
		r, size := utf8.DecodeRune(data[pos:])
		if size == 0 || (r == utf8.RuneError && size == 1 && data[pos] >= 0x80) {
			return nil
		}
		ok := unicodeProperty(r, v.Name)
		if v.Negated {
			ok = !ok
		}
		if ok {
			return []int{pos + size}
		}
		return nil
	case parser.Assertion:
		if assertionHolds(v.Kind, data, pos, flags) {
			return []int{pos}
		}
		return nil
	case parser.Group:
		flags = scopedGroupFlags(flags, v)
		if v.Atomic {
			states := matchCaptured(v.Child, data, pos, flags, map[int][]byte{})
			if len(states) == 0 {
				return nil
			}
			state := selectAtomicState(states, v.Child)
			return []int{state.pos}
		}
		return matchNodeBody(v.Child, data, pos, flags)
	case parser.Lookaround:
		positive := v.Kind == parser.Lookahead || v.Kind == parser.Lookbehind
		if v.Kind == parser.Lookbehind || v.Kind == parser.NegativeLookbehind {
			found := false
			starts := []int{}
			width, _, ok := parser.FixedWidth(v.Child)
			if flags&FlagUTF8 != 0 {
				width, _, ok = parser.FixedWidthUTF8(v.Child)
			}
			if ok {
				if pos >= width {
					starts = append(starts, pos-width)
				}
			} else {
				for start := 0; start <= pos; start++ {
					starts = append(starts, start)
				}
			}
			for _, start := range starts {
				for _, e := range matchNodeBody(v.Child, data, start, flags) {
					if e == pos {
						found = true
						break
					}
				}
			}
			if found == positive {
				return []int{pos}
			}
			return nil
		}
		ends := matchNodeBody(v.Child, data, pos, flags)
		if positive {
			if len(ends) > 0 {
				return []int{pos}
			}
			return nil
		}
		if len(ends) == 0 {
			return []int{pos}
		}
		return nil
	case parser.Sequence:
		positions := []int{pos}
		for _, child := range v.Elements {
			if verb, ok := child.(parser.ControlVerb); ok && strings.EqualFold(verb.Name, "ACCEPT") {
				return positions
			}
			next := make([]int, 0, len(positions))
			for _, p := range positions {
				next = append(next, matchNodeBody(child, data, p, flags)...)
			}
			positions = dedup(next)
			if len(positions) == 0 {
				break
			}
		}
		return positions
	case parser.Alternation:
		var out []int
		for _, child := range v.Options {
			out = append(out, matchNodeBody(child, data, pos, flags)...)
		}
		return dedup(out)
	case parser.Repeat:
		if ends, ok := matchByteRepeat(v, data, pos, flags); ok {
			return ends
		}
		positions := []int{pos}
		results := []int{}
		if v.Min == 0 {
			results = append(results, pos)
		}
		maxCount := v.Max
		budget := len(data) - pos + 1
		if budget < 1 {
			budget = 1
		}
		if maxCount < 0 || maxCount > budget {
			maxCount = budget
		}
		for count := 1; count <= maxCount; count++ {
			next := []int{}
			for _, p := range positions {
				next = append(next, matchNodeBody(v.Child, data, p, flags)...)
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
			return []int{pos}
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
		if value >= r.Lo && value <= r.Hi || flags&FlagCaseless != 0 && ((value >= 'a' && value <= 'z' && value-'a'+'A' >= r.Lo && value-'a'+'A' <= r.Hi) || (value >= 'A' && value <= 'Z' && value-'A'+'a' >= r.Lo && value-'A'+'a' <= r.Hi)) {
			matched = true
			break
		}
	}
	return matched != v.Negated
}

// byteRepeatAtom 判断重复的子节点在当前模式下是否恰好消费一个字节。
// UTF-8/UCP 模式下字符类按码位消费 1~4 字节，不能按字节推进，因此显式排除。
func byteRepeatAtom(n parser.Node, flags CompileFlag) bool {
	if flags&(FlagUTF8|FlagUCP) != 0 {
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
		return value != '\n' || flags&FlagDotAll != 0
	case parser.Class:
		return classByteMatch(v, value, flags)
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
	limit := len(data) - pos
	if limit < 0 {
		limit = 0
	}
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

func validUTF8LiteralAt(data []byte, pos, width int) bool {
	if !utf8.Valid(data) {
		return false
	}
	if pos < 0 || width <= 0 || pos+width > len(data) || pos > 0 && pos < len(data) && data[pos]&0xc0 == 0x80 {
		return false
	}
	return utf8.Valid(data[pos : pos+width])
}

// scopedGroupFlags 将分组内的局部修饰符合并到当前匹配状态。
func scopedGroupFlags(flags CompileFlag, group parser.Group) CompileFlag {
	if group.SetFlags&parser.GroupFlagCaseless != 0 {
		flags |= FlagCaseless
	}
	if group.SetFlags&parser.GroupFlagDotAll != 0 {
		flags |= FlagDotAll
	}
	if group.SetFlags&parser.GroupFlagMultiline != 0 {
		flags |= FlagMultiline
	}
	if group.ClearFlags&parser.GroupFlagCaseless != 0 {
		flags &^= FlagCaseless
	}
	if group.ClearFlags&parser.GroupFlagDotAll != 0 {
		flags &^= FlagDotAll
	}
	if group.ClearFlags&parser.GroupFlagMultiline != 0 {
		flags &^= FlagMultiline
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

func unicodeProperty(r rune, name string) bool {
	name = normalizeScannerUnicodeProperty(name)
	for _, prefix := range []string{"script=", "sc=", "script:", "generalcategory=", "gc="} {
		if strings.HasPrefix(name, prefix) {
			name = strings.TrimPrefix(name, prefix)
			break
		}
	}
	if len(name) > 2 && (strings.HasPrefix(name, "is") || strings.HasPrefix(name, "in")) {
		name = name[2:]
	}
	switch name {
	case "l", "letter", "alpha":
		return unicode.IsLetter(r)
	case "n", "number":
		return unicode.IsNumber(r)
	case "nd":
		return unicode.IsDigit(r)
	case "z", "space", "whitespace":
		return unicode.IsSpace(r)
	case "lu", "uppercaseletter":
		return unicode.IsUpper(r)
	case "ll", "lowercaseletter":
		return unicode.IsLower(r)
	case "lt":
		return unicode.Is(unicode.Lt, r)
	case "lm":
		return unicode.Is(unicode.Lm, r)
	case "lo":
		return unicode.Is(unicode.Lo, r)
	case "m", "mark":
		return unicode.Is(unicode.M, r)
	case "p", "punct":
		return unicode.Is(unicode.P, r)
	case "s", "symbol":
		return unicode.Is(unicode.S, r)
	case "cc", "control":
		return unicode.Is(unicode.Cc, r)
	case "ascii":
		return r < 128
	case "any":
		return true
	case "assigned":
		return !unicode.Is(unicode.Cn, r)
	case "unassigned":
		return unicode.Is(unicode.Cn, r)
	default:
		if table, ok := scannerUnicodeTables[name]; ok {
			return unicode.Is(table, r)
		}
		return false
	}
}

func hasCaptureReference(s parser.Sequence) bool {
	for _, n := range s.Elements {
		if containsCaptureSemantics(n) {
			return true
		}
	}
	return false
}
func containsCaptureSemantics(n parser.Node) bool {
	switch v := n.(type) {
	case parser.Backreference, parser.Conditional:
		return true
	case parser.Group:
		return containsCaptureSemantics(v.Child)
	case parser.Sequence:
		for _, c := range v.Elements {
			if containsCaptureSemantics(c) {
				return true
			}
		}
	case parser.Alternation:
		for _, c := range v.Options {
			if containsCaptureSemantics(c) {
				return true
			}
		}
	case parser.Repeat, parser.Lookaround:
		return containsCaptureSemantics(childNode(v))
	}
	return false
}
func childNode(n parser.Node) parser.Node {
	switch v := n.(type) {
	case parser.Repeat:
		return v.Child
	case parser.Lookaround:
		return v.Child
	}
	return nil
}
func containsBackreference(n parser.Node) bool {
	switch v := n.(type) {
	case parser.Backreference:
		return true
	case parser.Sequence:
		for _, c := range v.Elements {
			if containsBackreference(c) {
				return true
			}
		}
	case parser.Group:
		return containsBackreference(v.Child)
	case parser.Alternation:
		for _, c := range v.Options {
			if containsBackreference(c) {
				return true
			}
		}
	}
	return false
}
func matchSequenceCaptures(s parser.Sequence, data []byte, pos int, flags CompileFlag) []int {
	states := matchCaptured(s, data, pos, flags, map[int][]byte{})
	out := make([]int, 0, len(states))
	for _, state := range states {
		out = append(out, state.pos)
	}
	return dedup(out)
}

type captureState struct {
	pos  int
	caps map[int][]byte
}

func matchCaptured(node parser.Node, data []byte, pos int, flags CompileFlag, caps map[int][]byte) []captureState {
	switch value := node.(type) {
	case parser.Group:
		flags = scopedGroupFlags(flags, value)
		entryCaps := cloneCaps(caps)
		clearCaptureScope(entryCaps, value)
		out := matchCaptured(value.Child, data, pos, flags, entryCaps)
		if value.Atomic && len(out) > 0 {
			out = []captureState{selectAtomicState(out, value.Child)}
		}
		if value.Capture <= 0 {
			return out
		}
		for i := range out {
			out[i].caps = cloneCaps(out[i].caps)
			out[i].caps[value.Capture] = append([]byte(nil), data[pos:out[i].pos]...)
		}
		return out
	case parser.Sequence:
		states := []captureState{{pos: pos, caps: cloneCaps(caps)}}
		for _, child := range value.Elements {
			next := make([]captureState, 0)
			for _, state := range states {
				next = append(next, matchCaptured(child, data, state.pos, flags, state.caps)...)
			}
			states = dedupCaptureStates(next)
			if len(states) == 0 {
				break
			}
		}
		return states
	case parser.Alternation:
		out := make([]captureState, 0)
		for _, child := range value.Options {
			out = append(out, matchCaptured(child, data, pos, flags, caps)...)
		}
		return dedupCaptureStates(out)
	case parser.Repeat:
		states := []captureState{{pos: pos, caps: cloneCaps(caps)}}
		out := make([]captureState, 0)
		if value.Min == 0 {
			out = append(out, states...)
		}
		maxCount := value.Max
		budget := len(data) - pos + 1
		if budget < 1 {
			budget = 1
		}
		if maxCount < 0 || maxCount > budget {
			maxCount = budget
		}
		for count := 1; count <= maxCount; count++ {
			next := make([]captureState, 0)
			for _, state := range states {
				next = append(next, matchCaptured(value.Child, data, state.pos, flags, state.caps)...)
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
		captured, ok := caps[value.Index]
		if !ok || pos+len(captured) > len(data) {
			return nil
		}
		for i, item := range captured {
			if !equalByte(item, data[pos+i], flags) {
				return nil
			}
		}
		return []captureState{{pos: pos + len(captured), caps: cloneCaps(caps)}}
	case parser.Conditional:
		child := value.No
		if _, ok := caps[value.Index]; ok {
			child = value.Yes
		}
		return matchCaptured(child, data, pos, flags, caps)
	case parser.Lookaround:
		positive := value.Kind == parser.Lookahead || value.Kind == parser.Lookbehind
		if value.Kind == parser.Lookbehind || value.Kind == parser.NegativeLookbehind {
			matched := make([]captureState, 0)
			starts := make([]int, 0)
			if width, _, ok := parser.FixedWidth(value.Child); ok && flags&FlagUTF8 == 0 {
				if pos >= width {
					starts = append(starts, pos-width)
				}
			} else {
				for start := 0; start <= pos; start++ {
					starts = append(starts, start)
				}
			}
			for _, start := range starts {
				for _, state := range matchCaptured(value.Child, data, start, flags, caps) {
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
				return []captureState{{pos: pos, caps: cloneCaps(caps)}}
			}
			return nil
		}
		states := matchCaptured(value.Child, data, pos, flags, caps)
		if positive && len(states) > 0 {
			for i := range states {
				states[i].pos = pos
			}
			return dedupCaptureStates(states)
		}
		if !positive && len(states) == 0 {
			return []captureState{{pos: pos, caps: cloneCaps(caps)}}
		}
		return nil
	default:
		ends := matchNode(node, data, pos, flags)
		out := make([]captureState, 0, len(ends))
		for _, end := range ends {
			out = append(out, captureState{pos: end, caps: cloneCaps(caps)})
		}
		return out
	}
}

// clearCaptureScope 在重复进入分组时清除该分组及其子树的旧捕获值。
func clearCaptureScope(caps map[int][]byte, group parser.Group) {
	if group.Capture > 0 {
		delete(caps, group.Capture)
	}
	for _, id := range parser.CaptureIDs(group.Child) {
		delete(caps, id)
	}
}

// selectAtomicState 保留原子分组首次确定的路径，避免后续回溯改写选择。
func selectAtomicState(states []captureState, child parser.Node) captureState {
	if repeat, ok := child.(parser.Repeat); ok && repeat.Greedy {
		return states[len(states)-1]
	}
	return states[0]
}

func dedupCaptureStates(states []captureState) []captureState {
	seen := map[string]struct{}{}
	out := states[:0]
	for _, state := range states {
		key := captureStateKey(state)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, state)
	}
	return out
}

func captureStateKey(state captureState) string {
	key := strconv.Itoa(state.pos) + ":"
	indexes := make([]int, 0, len(state.caps))
	for index := range state.caps {
		indexes = append(indexes, index)
	}
	sort.Ints(indexes)
	for _, index := range indexes {
		key += strconv.Itoa(index) + "=" + strconv.Quote(string(state.caps[index])) + ";"
	}
	return key
}
func cloneCaps(in map[int][]byte) map[int][]byte {
	out := make(map[int][]byte, len(in))
	for k, v := range in {
		out[k] = append([]byte(nil), v...)
	}
	return out
}
func dedup(values []int) []int {
	sort.Ints(values)
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
		return pos == 0 || flags&FlagMultiline != 0 && pos > 0 && data[pos-1] == '\n'
	case parser.BeginAbsolute:
		return pos == 0
	case parser.End:
		return pos == len(data) || flags&FlagMultiline != 0 && pos < len(data) && data[pos] == '\n'
	case parser.EndAbsolute:
		return pos == len(data)
	case parser.EndBeforeFinalNewline:
		return pos == len(data) || pos+1 == len(data) && data[pos] == '\n' || pos+2 == len(data) && data[pos] == '\r' && data[pos+1] == '\n'
	case parser.WordBoundary, parser.NonWordBoundary:
		left := wordBefore(data, pos, flags)
		right := wordAfter(data, pos, flags)
		return left != right == (kind == parser.WordBoundary)
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
		size := len(arena.marks)
		if size < 64 {
			size = 64
		}
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
		if flags&FlagCaseless == 0 {
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
		if pos >= len(data) || data[pos] == '\n' && flags&FlagDotAll == 0 {
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
	budget := len(data) - pos + 1
	if budget < 1 {
		budget = 1
	}
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
	if flags&FlagCaseless == 0 {
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
	if flags&FlagUTF8 != 0 && pos < len(data) && data[pos]&0xc0 == 0x80 {
		return false
	}
	if flags&FlagUCP == 0 {
		return isWord(data[pos-1])
	}
	r, _ := utf8.DecodeLastRune(data[:pos])
	return unicodeWord(r)
}
func wordAfter(data []byte, pos int, flags CompileFlag) bool {
	if pos >= len(data) {
		return false
	}
	if flags&FlagUTF8 != 0 && data[pos]&0xc0 == 0x80 {
		return false
	}
	if flags&FlagUCP == 0 {
		return isWord(data[pos])
	}
	r, _ := utf8.DecodeRune(data[pos:])
	return unicodeWord(r)
}

func unicodeWord(value rune) bool {
	return unicode.IsLetter(value) || unicode.IsNumber(value) || unicode.IsMark(value) || unicode.Is(unicode.Pc, value)
}
