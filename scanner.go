// Package scankit 提供跨平台的 Block 多规则扫描器。
package scankit

import (
	"bytes"
	"context"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"unicode"
	"unicode/utf8"

	"github.com/smartwalle/scankit/internal/combination"
	"github.com/smartwalle/scankit/internal/compiler"
	"github.com/smartwalle/scankit/internal/engine"
	"github.com/smartwalle/scankit/internal/fdr"
	"github.com/smartwalle/scankit/internal/fuzzy"
	"github.com/smartwalle/scankit/internal/hwlm"
	"github.com/smartwalle/scankit/internal/hwlm/noodle"
	"github.com/smartwalle/scankit/internal/hwlm/teddy"
	nfalib "github.com/smartwalle/scankit/internal/nfa"
	"github.com/smartwalle/scankit/internal/parser"
	"github.com/smartwalle/scankit/internal/repeat"
	"github.com/smartwalle/scankit/internal/report"
	"github.com/smartwalle/scankit/internal/rose"
	"github.com/smartwalle/scankit/internal/scratch"
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
	usage            compiler.Usage
	validationErr    error
	rosePlan         *rose.Program
	// cancelFlag 由 ScanContext 的 ctx.Done 异步设置，供主扫描循环周期探测，
	// 避免把 context.Context 引入 scanInto 签名或破坏对象池。
	cancelFlag uint32
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
	// hasBackref 缓存 containsBackreference 的结果。
	hasBackref bool
	// hasConditional 缓存 containsConditional 的结果。
	hasConditional bool
}

func newScanner(rules []compiledRule) *Scanner {
	copyRules := append([]compiledRule(nil), rules...)
	// 预先计算每条规则的 AST 派生属性，避免扫描时重复遍历规则树。
	for i := range copyRules {
		copyRules[i].containsAny = containsAny(copyRules[i].root)
		copyRules[i].requiresEndOfData = requiresEndOfData(copyRules[i].root)
		copyRules[i].backendEligible = backendEligible(copyRules[i])
		copyRules[i].nonGreedy = firstRepeatPreference(copyRules[i].root)
		copyRules[i].hasBackref = containsBackreference(copyRules[i].root)
		copyRules[i].hasConditional = containsConditional(copyRules[i].root)
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
	scanner := &Scanner{rules: copyRules, ruleIndex: index, ruleOrder: order, hasCombo: hasCombo, candidateIDs: candidateIDs, literalIDs: literalIDs, contextPool: &sync.Pool{New: func() any { return &scanContext{Scratch: scratch.New(), Reports: report.New()} }}, roseStatePool: &sync.Pool{New: func() any { return new([]rose.State) }}, roseSinglePool: &sync.Pool{New: func() any { return new(map[uint32]struct{}) }}}
	if len(literals) > 1 {
		scanner.literalKind, scanner.literalFind, scanner.literalFindInto = newLiteralCandidateFinder(literals)
	}
	// Rose 角色图与文字候选索引随规则计划不可变，构造阶段完成一次，
	// 扫描时直接复用，避免每个 Block 重建角色、指令和自动机。
	scanner.rosePlan = scanner.buildRoseProgram()
	scanner.canUseRoseInScan = scanner.computeCanUseRoseInScan()
	// 编译结果不可变，计划校验只需在构造时执行一次；扫描热路径复用该结论。
	scanner.validationErr = scanner.validate()
	return scanner
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
			for _, candidate := range program.RolesCopy() {
				if candidate.ReportID == event.ID && candidate.MatchAt(data, int(event.From)) {
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

// ErrCancelled 是 ScanContext 在上下文取消时返回的哨兵错误。
var ErrCancelled = fmt.Errorf("scankit: scan cancelled")

// ScanContext 支持通过 context.Context 取消的整块扫描。
// ctx 在调用前若已取消或扫描期间被取消，立即返回 ErrCancelled 与已收集的 matches。
func (scanner *Scanner) ScanContext(ctx context.Context, data []byte) ([]Match, error) {
	return scanner.ScanContextInto(ctx, data, nil)
}

// ScanContextInto 与 ScanContext 类似，但复用调用方提供的 matches 缓冲。
// ctx == nil 时等价于 ScanInto，保留现有调用语义。
func (scanner *Scanner) ScanContextInto(ctx context.Context, data []byte, matches []Match) ([]Match, error) {
	if scanner == nil {
		return matches, nil
	}
	return scanner.scanContextInto(ctx, data, matches)
}

// scanContextInto 在现有 scanInto 主路径上增加 ctx.Err() 探测点，
// 避免破坏现有调用方。cancelCh 在 ctx.Done 时同步关闭以快速唤醒跨候选循环。
func (scanner *Scanner) scanContextInto(ctx context.Context, data []byte, matches []Match) ([]Match, error) {
	if ctx == nil {
		return scanner.scanInto(data, matches)
	}
	if err := ctx.Err(); err != nil {
		return matches, ErrCancelled
	}
	// 通过 sync/atomic 标志把 ctx.Done 异步广播到主扫描循环，
	// 让长输入扫描也能在 ctx 取消时提前返回，避免修改 scanInto 签名。
	// sync.Once 保证 stop channel 恰好被关闭一次，避免 close-of-closed 竞争。
	var stop sync.Once
	stopCh := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			atomic.StoreUint32(&scanner.cancelFlag, 1)
			stop.Do(func() { close(stopCh) })
		case <-stopCh:
		}
	}()
	defer func() {
		stop.Do(func() { close(stopCh) })
		atomic.StoreUint32(&scanner.cancelFlag, 0)
	}()
	return scanner.scanInto(data, matches)
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
	// 所有规则均为精确文字且无需特殊报告语义时，直接使用文字索引，
	// 避免先构造后端结果再进入逐起点确认循环。
	fastLiteral := !scanner.hasCombo && len(scanner.rules) > 1 &&
		len(scanner.candidateIDs) == len(scanner.rules) &&
		len(scanner.literalIDs) == len(scanner.rules)
	if fastLiteral {
		for _, rule := range scanner.rules {
			if rule.flags&(FlagQuiet|FlagSingleMatch) != 0 {
				fastLiteral = false
				break
			}
		}
	}
	backendOnly := !scanner.hasCombo && !fastLiteral
	backendRuleCount := 0
	if backendOnly {
		for _, rule := range scanner.rules {
			if !rule.backendEligible {
				continue
			}
			backendRuleCount++
			if rule.nfaEngine != nil {
				for _, span := range rule.nfaEngine.Spans(data) {
					matches = append(matches, Match{Id: rule.id, From: uint64(span.From), To: uint64(span.To)})
				}
			} else {
				for _, span := range rule.program.Spans(data) {
					matches = append(matches, Match{Id: rule.id, From: uint64(span[0]), To: uint64(span[1])})
				}
			}
		}
	}
	if backendOnly && backendRuleCount == len(scanner.rules) {
		return matches, nil
	}
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
		clear(ctx.blockedUntil)
		clear(ctx.fired)
		ctx.blockedUntil = ctx.blockedUntil[:0]
		ctx.fired = ctx.fired[:0]
		ctx.comboTriggers = ctx.comboTriggers[:0]
		ctx.literalCandidates = ctx.literalCandidates[:0]
		ctx.ruleMatchBuf = ctx.ruleMatchBuf[:0]
		for id, starts := range ctx.prefilterStarts {
			if len(starts) > 1<<20 {
				delete(ctx.prefilterStarts, id)
				continue
			}
			clear(starts)
		}
		clear(ctx.literalEnds)
		pool.Put(ctx)
	}()
	// 每个起点都必须独立求值，块模式允许同一规则产生重叠命中。
	blockedUntil := reserveInts(ctx.blockedUntil, len(scanner.rules))
	fired := reserveBools(ctx.fired, len(scanner.rules))
	comboTriggers := ctx.comboTriggers[:0]
	// SOM_LEFTMOST 对同一结束位置只保留最左起点，避免 AST 回溯产生重复事件。
	var somSeen map[uint32]map[int]struct{}
	literalEnds := ctx.literalEnds
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
	if scanner.literalFind != nil {
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
			clear(literalEnds)
			for _, candidate := range literalCandidates {
				if literalEnds[candidate.From] == nil {
					literalEnds[candidate.From] = make(map[uint32]int)
				}
				literalEnds[candidate.From][candidate.ID] = candidate.To
			}
		}
	} else if len(scanner.candidateIDs) == 1 && len(scanner.literalIDs) == 1 {
		if literalEnds == nil {
			literalEnds = make(map[int]map[uint32]int)
			ctx.literalEnds = literalEnds
		}
		clear(literalEnds)
		scanner.fillSingleLiteralEnds(data, literalEnds)
	}
	for _, rule := range scanner.rules {
		if len(rule.prefilter) == 0 || scanner.literalFind != nil {
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
	for start := 0; start <= len(data); start++ {
		// 每 1024 个起点探测一次 ScanContext 异步设置的取消标志，
		// 避免在每个候选都做原子读。
		if start&1023 == 0 && atomic.LoadUint32(&scanner.cancelFlag) == 1 {
			return matches, ErrCancelled
		}
		for ri, rule := range scanner.rules {
			if backendOnly && rule.backendEligible {
				continue
			}
			if ctx.Reports.Stopped() {
				break
			}
			if rule.comb != nil {
				continue
			}
			if fired[ri] {
				continue
			}
			if start < blockedUntil[ri] {
				continue
			}
			if rule.flags&FlagPrefilter != 0 && len(rule.prefilter) > 0 {
				if starts, indexed := prefilterStarts[rule.id]; indexed {
					if _, ok := starts[start]; !ok {
						continue
					}
				} else if start+len(rule.prefilter) > len(data) || !hwlm.Contains(data[start:start+len(rule.prefilter)], hwlm.Literal{Value: rule.prefilter, CaseInsensitive: rule.flags&FlagCaseless != 0}) {
					continue
				}
			}
			var ends []int
			if _, indexed := scanner.candidateIDs[rule.id]; indexed && literalEnds != nil {
				end, ok := literalEnds[start][rule.id]
				if !ok {
					continue
				}
				if _, exact := scanner.literalIDs[rule.id]; exact {
					ends = []int{end}
				} else {
					ends = matchRuleInto(rule, data, start, ctx.ruleMatchBuf[:0])
					ctx.ruleMatchBuf = ends
				}
			} else {
				ends = matchRuleInto(rule, data, start, ctx.ruleMatchBuf[:0])
				ctx.ruleMatchBuf = ends
			}
			end := -1
			// 结束偏移已按升序排序，直接取边界值即可获得贪婪/非贪婪的首选结束。
			if rule.nonGreedy {
				if len(ends) > 0 {
					end = ends[0]
				}
			} else if len(ends) > 0 {
				end = ends[len(ends)-1]
			}
			if end < 0 || end > len(data) {
				continue
			}
			if rule.ext != nil {
				if !rule.ext.OffsetAllowed(uint64(end)) {
					continue
				}
				if !rule.ext.LengthAllowed(uint64(end - start)) {
					continue
				}
			}
			if rule.flags&FlagSOMLeftmost != 0 {
				if somSeen == nil {
					somSeen = make(map[uint32]map[int]struct{})
				}
				seenEnds := somSeen[rule.id]
				if seenEnds == nil {
					seenEnds = make(map[int]struct{})
					somSeen[rule.id] = seenEnds
				}
				if _, exists := seenEnds[end]; exists {
					continue
				}
				seenEnds[end] = struct{}{}
			}
			if scanner.hasCombo {
				comboTriggers = append(comboTriggers, report.Event{ID: rule.id, From: uint64(start), To: uint64(end), Flags: uint32(rule.flags)})
			}
			quiet := rule.flags&FlagQuiet != 0
			if quiet {
				if rule.flags&FlagSingleMatch != 0 {
					fired[ri] = true
				}
				continue
			}
			ctx.Reports.Add(report.Event{ID: rule.id, From: uint64(start), To: uint64(end), SOM: uint64(start), Flags: uint32(rule.flags)})
			if end > start {
				blockedUntil[ri] = end
			}
			if rule.flags&FlagSingleMatch != 0 {
				fired[ri] = true
			}
		}
		if ctx.Reports.Stopped() {
			break
		}
	}
	if scanner.hasCombo {
		scanner.emitCombinationReports(ctx.Reports, comboTriggers, uint64(len(data)))
	}
	ctx.comboTriggers = comboTriggers
	for _, event := range ctx.Reports.Events() {
		matches = append(matches, Match{Id: event.ID, From: event.From, To: event.To})
	}
	return matches, nil
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
		if rule.repeat != nil {
			return rule.repeat.MatchAtInto(data, start, endsBuf[:0])
		}
		if rule.nfaEngine != nil {
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
	if rule.program != nil && !rule.info.RequiresStatefulRuntime() && rule.flags&(FlagCaseless|FlagUTF8|FlagUCP|FlagMultiline|FlagDotAll) == 0 && !containsAny(rule.root) {
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
		matched := false
		for _, r := range v.Ranges {
			if data[pos] >= r.Lo && data[pos] <= r.Hi || flags&FlagCaseless != 0 && ((data[pos] >= 'a' && data[pos] <= 'z' && data[pos]-'a'+'A' >= r.Lo && data[pos]-'a'+'A' <= r.Hi) || (data[pos] >= 'A' && data[pos] <= 'Z' && data[pos]-'A'+'a' >= r.Lo && data[pos]-'A'+'a' <= r.Hi)) {
				matched = true
				break
			}
		}
		if matched != v.Negated {
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
		ok := false
		switch v.Kind {
		case parser.Begin:
			ok = pos == 0 || flags&FlagMultiline != 0 && pos > 0 && data[pos-1] == '\n'
		case parser.BeginAbsolute:
			ok = pos == 0
		case parser.End:
			ok = pos == len(data) || flags&FlagMultiline != 0 && pos < len(data) && data[pos] == '\n'
		case parser.EndAbsolute:
			ok = pos == len(data)
		case parser.EndBeforeFinalNewline:
			ok = pos == len(data) || pos+1 == len(data) && data[pos] == '\n' || pos+2 == len(data) && data[pos] == '\r' && data[pos+1] == '\n'
		case parser.WordBoundary, parser.NonWordBoundary:
			left := wordBefore(data, pos, flags)
			right := wordAfter(data, pos, flags)
			boundary := left != right
			ok = boundary == (v.Kind == parser.WordBoundary)
		}
		if ok {
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
		return matchNode(v.Child, data, pos, flags)
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
				for _, e := range matchNode(v.Child, data, start, flags) {
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
		ends := matchNode(v.Child, data, pos, flags)
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
				next = append(next, matchNode(child, data, p, flags)...)
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
			out = append(out, matchNode(child, data, pos, flags)...)
		}
		return dedup(out)
	case parser.Repeat:
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
				next = append(next, matchNode(v.Child, data, p, flags)...)
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
