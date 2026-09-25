// Package rose 提供角色调度器的基础契约。
package rose

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/bits"
	"slices"
	"sort"
	"unicode"
	"unicode/utf8"

	"github.com/smartwalle/scankit/internal/dispatch"
	"github.com/smartwalle/scankit/internal/fdr"
	"github.com/smartwalle/scankit/internal/hwlm"
	"github.com/smartwalle/scankit/internal/nfagraph"
	"github.com/smartwalle/scankit/internal/parser"
	"github.com/smartwalle/scankit/internal/report"
	"github.com/smartwalle/scankit/internal/simd"
)

// Role 描述一条 Rose 角色：以固定文字为触发条件，并携带偏移与确认约束。
type Role struct {
	ID              uint32
	Literal         []byte
	ReportID        uint32
	CaseInsensitive bool
	Anchored        bool
	EndOfData       bool
	MinOffset       uint64
	MaxOffset       uint64
	HasMaxOffset    bool
	Confirm         bool
}

// Length 返回角色触发文字的字节长度。
func (r Role) Length() int { return len(r.Literal) }

// roleCostLengthCap 是角色代价对文字长度的封顶值，避免超长文字压过其他约束。
const roleCostLengthCap = 64

// RoleCost 描述角色候选扫描代价的构成分量。
// 结构本身不参与命中判定，只用于在等价角色之间选取代表角色。
type RoleCost struct {
	// LiteralLength 是角色文字长度（封顶到 roleCostLengthCap）。
	LiteralLength int `json:"literal_length"`
	// Anchored 表示角色只允许在输入起点命中。
	Anchored bool `json:"anchored,omitempty"`
	// EndOfData 表示角色必须以输入末尾结束。
	EndOfData bool `json:"end_of_data,omitempty"`
	// NeedsConfirm 表示角色命中后仍需完整规则确认。
	NeedsConfirm bool `json:"needs_confirm,omitempty"`
	// CaseFolded 表示角色按大小写不敏感扫描。
	CaseFolded bool `json:"case_folded,omitempty"`
	// BoundedOffset 表示角色带有结束偏移上界。
	BoundedOffset bool `json:"bounded_offset,omitempty"`
}

// Cost 返回角色的扫描代价分量快照。
func (r Role) Cost() RoleCost {
	length := min(len(r.Literal), roleCostLengthCap)
	return RoleCost{
		LiteralLength: length,
		Anchored:      r.Anchored,
		EndOfData:     r.EndOfData,
		NeedsConfirm:  r.Confirm,
		CaseFolded:    r.CaseInsensitive,
		BoundedOffset: r.HasMaxOffset,
	}
}

// Weight 返回按扫描代价分量折算的权值：越小表示候选越稀疏、越应优先选中。
// 文字越长候选越少，锚定、末尾约束和偏移上界都会进一步收紧候选集合；
// 需要额外确认或大小写折叠的放宽项会提高权值。
func (r Role) Weight() int {
	cost := r.Cost()
	weight := (roleCostLengthCap - cost.LiteralLength) * 4
	if cost.Anchored {
		weight -= 24
	}
	if cost.EndOfData {
		weight -= 24
	}
	if cost.BoundedOffset {
		weight -= 8
	}
	if cost.CaseFolded {
		weight += 2
	}
	if cost.NeedsConfirm {
		weight += 4
	}
	if weight < 0 {
		weight = 0
	}
	return weight
}

// ScanEquivalent 判断两个角色的候选扫描是否完全等价。
// 等价角色在相同输入上产生完全相同的候选与报告，因此只需保留一个代表。
func (r Role) ScanEquivalent(other Role) bool {
	if r.ReportID != other.ReportID ||
		r.Anchored != other.Anchored ||
		r.EndOfData != other.EndOfData ||
		r.Confirm != other.Confirm ||
		r.MinOffset != other.MinOffset ||
		r.HasMaxOffset != other.HasMaxOffset ||
		!bytes.Equal(r.Literal, other.Literal) {
		return false
	}
	if r.HasMaxOffset && r.MaxOffset != other.MaxOffset {
		return false
	}
	if r.CaseInsensitive != other.CaseInsensitive {
		// 不含 ASCII 字母时大小写折叠是恒等变换，敏感与不敏感扫描产生相同候选。
		return !containsASCIILetter(r.Literal)
	}
	return true
}

// containsASCIILetter 判断文字中是否含有会改变大小写折叠结果的 ASCII 字母。
func containsASCIILetter(literal []byte) bool {
	for _, value := range literal {
		if value >= 'A' && value <= 'Z' || value >= 'a' && value <= 'z' {
			return true
		}
	}
	return false
}

// Clone 返回角色副本，并复制触发文字。
func (r Role) Clone() Role { r.Literal = append([]byte(nil), r.Literal...); return r }

// MatchAt 判断角色触发文字是否在 off 处命中，必要时按角色配置折叠大小写。
func (r Role) MatchAt(data []byte, off int) bool {
	if len(r.Literal) == 0 || off < 0 || off+len(r.Literal) > len(data) {
		return false
	}
	if r.CaseInsensitive && !isASCII(r.Literal) {
		if off >= len(data) || !utf8.RuneStart(data[off]) || !utf8.Valid(r.Literal) || !utf8.Valid(data[off:off+len(r.Literal)]) {
			return false
		}
		literal := string(r.Literal)
		input := string(data[off : off+len(r.Literal)])
		lr, ls := utf8.DecodeRuneInString(literal)
		ir, is := utf8.DecodeRuneInString(input)
		for len(literal) > 0 && len(input) > 0 {
			if lr != ir && unicode.ToLower(lr) != unicode.ToLower(ir) {
				return false
			}
			if ls <= 0 || is <= 0 || ls != is {
				return false
			}
			literal = literal[ls:]
			input = input[is:]
			if len(literal) > 0 {
				lr, ls = utf8.DecodeRuneInString(literal)
			}
			if len(input) > 0 {
				ir, is = utf8.DecodeRuneInString(input)
			}
		}
		return len(literal) == 0 && len(input) == 0
	}
	if !r.CaseInsensitive {
		// 大小写不敏感且全 ASCII 走 bytes.Equal。
		return bytes.Equal(data[off:off+len(r.Literal)], r.Literal)
	}
	// 大小写不敏感且全 ASCII 走 bytes.EqualFold。
	return bytes.EqualFold(r.Literal, data[off:off+len(r.Literal)])
}

func isASCII(data []byte) bool {
	for _, b := range data {
		if b >= utf8.RuneSelf {
			return false
		}
	}
	return true
}

// Eligible 判断角色命中位置是否满足锚定、输入末尾和结束偏移约束。
func (r Role) Eligible(data []byte, off int) bool {
	if !r.MatchAt(data, off) {
		return false
	}
	if r.Anchored && off != 0 {
		return false
	}
	end := uint64(off + len(r.Literal))
	if r.EndOfData && end != uint64(len(data)) {
		return false
	}
	if end < r.MinOffset {
		return false
	}
	return !r.HasMaxOffset || end <= r.MaxOffset
}

// MatchEnd 判断角色是否在指定结束偏移命中。
func (r Role) MatchEnd(data []byte, end int) bool {
	return end >= 0 && end <= len(data) && r.Eligible(data, end-len(r.Literal))
}

func roleEnd(offset uint64, length int) (uint64, bool) {
	if length < 0 || uint64(length) > ^uint64(0)-offset {
		return 0, false
	}
	return offset + uint64(length), true
}

// Program 是角色集合与指令序列构成的 Rose 程序。
type Program struct {
	Roles        []Role
	Instructions []Instruction
	matcher      *fdr.Matcher
	// matchBuf 复用 matcher 返回的命中缓冲，避免每次 FindMatchesInto 都重新分配。
	matchBuf        []fdr.Match
	miracles        []*Miracle
	miracleBuckets  [256][]int
	miracleFirstSet [4]uint64
	// miracleFirstTable 是 miracleFirstSet 的预编译窗口查找表，供候选取点热路径
	// 一次判定 32 字节；只在 miracleReady 为真时构建，与首字节集合同步维护。
	miracleFirstTable simd.ByteSetTables
	miracleReady      bool
	index             map[uint32]int
	byReport          map[uint32][]int
	instructions      map[uint32][]Instruction
}

// InstructionKind 表示角色状态执行时的动作类型。
type InstructionKind uint8

// InstructionReport 表示产生报告指令，其余常量对应其他指令类别。
const (
	InstructionReport InstructionKind = iota + 1
	InstructionActivate
	InstructionTransition
)

// Instruction 描述角色命中后的报告、激活或状态迁移动作。
type Instruction struct {
	Kind        InstructionKind
	RoleID      uint32
	TargetID    uint32
	OffsetDelta int64
	ReportID    uint32
	Flags       uint32
	IncludeSOM  bool
}

// Validate 检查指令类别、角色编号以及跳转目标是否合法。
func (i Instruction) Validate(p *Program) error {
	if i.RoleID == 0 || i.Kind < InstructionReport || i.Kind > InstructionTransition {
		return fmt.Errorf("invalid rose instruction")
	}
	if p == nil || !p.HasRole(i.RoleID) {
		return fmt.Errorf("instruction references unknown role %d", i.RoleID)
	}
	if i.Kind != InstructionReport && (i.TargetID == 0 || !p.HasRole(i.TargetID)) {
		return fmt.Errorf("instruction references unknown target %d", i.TargetID)
	}
	if i.Kind == InstructionReport {
		if i.TargetID != 0 || i.OffsetDelta != 0 {
			return fmt.Errorf("report instruction cannot carry transition fields")
		}
	} else {
		if i.ReportID != 0 || i.Flags != 0 || i.IncludeSOM {
			return fmt.Errorf("state instruction cannot carry report fields")
		}
	}
	return nil
}

// Empty 判断程序是否没有任何角色。
func (p *Program) Empty() bool { return p == nil || len(p.Roles) == 0 }

// RolesCopy 返回角色列表的深拷贝。
func (p *Program) RolesCopy() []Role {
	if p == nil {
		return nil
	}
	out := append([]Role(nil), p.Roles...)
	for i := range out {
		out[i] = out[i].Clone()
	}
	return out
}

// FindMatches 返回 data 中全部角色命中状态，按稳定顺序排列。
func (p *Program) FindMatches(data []byte) []State {
	return p.FindMatchesInto(data, nil)
}

// FindMatchesInto 将角色命中写入复用缓冲，供整块扫描避免重复创建状态切片。
func (p *Program) FindMatchesInto(data []byte, dst []State) []State {
	if p == nil {
		return dst[:0]
	}
	// matcher 为 nil 有两种构建期有意为之的形态：候选器就绪（miracleReady）、
	// 以及单角色走轻量线性确认路径。两者都不能在这里重新构建，否则对应的
	// 快路径永远不可达，而且每次扫描都要白建一个完整 FDR 自动机。
	matcher := p.matcher
	if matcher == nil && !p.miracleReady && len(p.Roles) > 1 {
		matcher = buildRoleMatcher(p.Roles)
	}
	if matcher != nil {
		// 复用 matcher 自身的匹配结果缓冲，避免每次扫描都重新分配。
		matches := matcher.FindInto(data, p.matchBuf[:0])
		p.matchBuf = matches
		// 超出阈值时丢弃缓冲，避免池/字段持有的缓冲无限增长。
		if cap(p.matchBuf) > 1<<20 {
			p.matchBuf = nil
		}
		out := dst[:0]
		if cap(out) < len(matches) {
			out = make([]State, 0, len(matches))
		}
		for _, match := range matches {
			role, ok := p.findRole(match.ID)
			if !ok || !role.Eligible(data, match.From) {
				continue
			}
			out = append(out, State{RoleID: match.ID, Offset: uint64(match.From)})
		}
		sortStatesInPlace(out)
		return out
	}
	if len(p.Roles) == 1 && !p.miracleReady {
		// 单角色不构建共享自动机时，直接线性确认即可；该路径保留
		// 重叠命中并避免每个输入偏移建立去重映射。
		role := p.Roles[0]
		out := dst[:0]
		for off := 0; off+len(role.Literal) <= len(data); off++ {
			if role.Eligible(data, off) {
				out = append(out, State{RoleID: role.ID, Offset: uint64(off)})
			}
		}
		return out
	}
	if p.miracleReady {
		return p.findMiracleMultiInto(data, 0, len(data), 0, dst)
	}
	// 此分支在每个角色至多被处理一次的条件下，不存在 (role.ID, off) 重复。
	// 直接写入结果缓冲，避免每次扫描都分配去重 map。
	out := dst[:0]
	for _, role := range p.Roles {
		for off := 0; off+len(role.Literal) <= len(data); off++ {
			if role.Eligible(data, off) {
				out = append(out, State{RoleID: role.ID, Offset: uint64(off)})
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Offset != out[j].Offset {
			return out[i].Offset < out[j].Offset
		}
		return out[i].RoleID < out[j].RoleID
	})
	return out
}

// FindMatchesLimit 返回稳定顺序的角色命中，并限制最多返回指定数量的结果。
func (p *Program) FindMatchesLimit(data []byte, limit int) []State {
	if limit < 0 {
		return nil
	}
	if limit == 0 {
		return p.FindMatches(data)
	}
	if p == nil {
		return nil
	}
	// 与 FindMatchesInto 同源：单角色与候选器就绪两种形态都刻意不持有通用
	// 匹配器，只有真正需要共享自动机的程序才按需构建一次。
	matcher := p.matcher
	if matcher == nil && !p.miracleReady && len(p.Roles) > 1 {
		matcher = buildRoleMatcher(p.Roles)
	}
	if matcher == nil {
		if p.miracleReady {
			// 每个角色单独限量会在合并排序前丢失更早的候选，
			// 这里先收集完整候选，再统一去重、排序和截断。
			out := p.findMiracleMulti(data, 0, len(data), 0)
			if len(out) > limit {
				out = out[:limit]
			}
			return out
		}
		out := make([]State, 0, limit)
		seen := make(map[[2]uint64]struct{}, limit)
		for _, role := range p.Roles {
			for off := 0; off+len(role.Literal) <= len(data); off++ {
				if !role.Eligible(data, off) {
					continue
				}
				key := [2]uint64{uint64(role.ID), uint64(off)}
				if _, exists := seen[key]; exists {
					continue
				}
				seen[key] = struct{}{}
				out = append(out, State{RoleID: role.ID, Offset: uint64(off)})
			}
		}
		out = SortStates(out)
		if len(out) > limit {
			out = out[:limit]
		}
		return out
	}
	out := make([]State, 0, limit)
	seen := make(map[[2]uint64]struct{}, limit)
	for _, match := range matcher.Find(data) {
		role, ok := p.findRole(match.ID)
		if !ok || !role.Eligible(data, match.From) {
			continue
		}
		key := [2]uint64{uint64(match.ID), uint64(match.From)}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, State{RoleID: match.ID, Offset: uint64(match.From)})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Offset != out[j].Offset {
			return out[i].Offset < out[j].Offset
		}
		return out[i].RoleID < out[j].RoleID
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

// FindMatchesRange 返回完全位于半开区间内的角色命中。
func (p *Program) FindMatchesRange(data []byte, from, to int) []State {
	if p == nil || from < 0 || to < from || to > len(data) {
		return nil
	}
	// 与 FindMatchesInto 一致：候选器就绪与单角色两种形态都保留构建期的
	// nil matcher 决定，不走按需构建。
	matcher := p.matcher
	if matcher == nil && !p.miracleReady && len(p.Roles) > 1 {
		matcher = buildRoleMatcher(p.Roles)
	}
	if matcher != nil {
		matches := matcher.FindRange(data, from, to)
		out := make([]State, 0, len(matches))
		seen := make(map[[2]uint64]struct{}, len(matches))
		for _, match := range matches {
			role, ok := p.findRole(match.ID)
			if !ok || !role.Eligible(data, match.From) {
				continue
			}
			key := [2]uint64{uint64(match.ID), uint64(match.From)}
			if _, exists := seen[key]; exists {
				continue
			}
			seen[key] = struct{}{}
			out = append(out, State{RoleID: match.ID, Offset: uint64(match.From)})
		}
		return SortStates(out)
	}
	if p.miracleReady {
		return p.findMiracleMulti(data, from, to, 0)
	}
	out := make([]State, 0)
	for _, state := range p.FindMatches(data) {
		role, ok := p.findRole(state.RoleID)
		if ok && int(state.Offset)+len(role.Literal) <= to && int(state.Offset) >= from {
			out = append(out, state)
		}
	}
	sortStatesInPlace(out)
	return out
}

// FindMatchesRangeLimit 返回区间内不超过 limit 个角色命中。
func (p *Program) FindMatchesRangeLimit(data []byte, from, to, limit int) []State {
	if limit < 0 || from < 0 || to < from || to > len(data) {
		return nil
	}
	if limit == 0 {
		return p.FindMatchesRange(data, from, to)
	}
	if p.matcher != nil {
		out := make([]State, 0, limit)
		seen := make(map[[2]uint64]struct{}, limit)
		for _, match := range p.matcher.FindRangeLimit(data, from, to, 0) {
			role, ok := p.findRole(match.ID)
			if !ok || !role.Eligible(data, match.From) {
				continue
			}
			key := [2]uint64{uint64(match.ID), uint64(match.From)}
			if _, exists := seen[key]; exists {
				continue
			}
			seen[key] = struct{}{}
			out = append(out, State{RoleID: match.ID, Offset: uint64(match.From)})
		}
		out = SortStates(out)
		if len(out) > limit {
			out = out[:limit]
		}
		return out
	}
	if p.miracleReady {
		return p.findMiracleMulti(data, from, to, limit)
	}
	out := make([]State, 0)
	seen := make(map[[2]uint64]struct{})
	for _, role := range p.Roles {
		for off := from; off+len(role.Literal) <= to; off++ {
			if role.Eligible(data, off) {
				key := [2]uint64{uint64(role.ID), uint64(off)}
				if _, exists := seen[key]; exists {
					continue
				}
				seen[key] = struct{}{}
				out = append(out, State{RoleID: role.ID, Offset: uint64(off)})
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Offset != out[j].Offset {
			return out[i].Offset < out[j].Offset
		}
		return out[i].RoleID < out[j].RoleID
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

// FindMatchesEndRange 返回结束偏移位于指定区间内的角色命中。
func (p *Program) FindMatchesEndRange(data []byte, from, to int) []State {
	if p == nil || from < 0 || to < from || to > len(data) {
		return nil
	}
	if p.matcher != nil {
		matches := p.matcher.FindEndRange(data, from, to)
		out := make([]State, 0, len(matches))
		seen := make(map[[2]uint64]struct{}, len(matches))
		for _, match := range matches {
			role, ok := p.findRole(match.ID)
			if !ok || !role.Eligible(data, match.From) {
				continue
			}
			key := [2]uint64{uint64(match.ID), uint64(match.From)}
			if _, exists := seen[key]; exists {
				continue
			}
			seen[key] = struct{}{}
			out = append(out, State{RoleID: match.ID, Offset: uint64(match.From)})
		}
		return SortStates(out)
	}
	if len(p.miracles) == len(p.Roles) && len(p.miracles) > 0 {
		out := make([]State, 0)
		for _, miracle := range p.miracles {
			out = append(out, miracle.FindEndRange(data, from, to, 0)...)
		}
		return SortStates(dedupStates(out))
	}
	out := make([]State, 0)
	seen := make(map[[2]uint64]struct{})
	for _, role := range p.Roles {
		length := len(role.Literal)
		start := max(from-length, 0)
		last := min(to-length-1, len(data)-length)
		for off := start; off <= last; off++ {
			if !role.Eligible(data, off) {
				continue
			}
			key := [2]uint64{uint64(role.ID), uint64(off)}
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			out = append(out, State{RoleID: role.ID, Offset: uint64(off)})
		}
	}
	return SortStates(out)
}

// FindMatchesEndRangeLimit 返回结束偏移在区间内且不超过上限的角色命中。
func (p *Program) FindMatchesEndRangeLimit(data []byte, from, to, limit int) []State {
	if limit < 0 || p == nil || from < 0 || to < from || to > len(data) {
		return nil
	}
	out := p.FindMatchesEndRange(data, from, to)
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

// FindMatchesReverse 返回按偏移降序排列的角色命中，便于反向确认。
func (p *Program) FindMatchesReverse(data []byte) []State {
	out := p.FindMatches(data)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Offset != out[j].Offset {
			return out[i].Offset > out[j].Offset
		}
		return out[i].RoleID > out[j].RoleID
	})
	return out
}

// MatchReportIDs 将角色命中转换为去重排序的报告编号。
func (p *Program) MatchReportIDs(states []State) []uint32 {
	if p == nil {
		return nil
	}
	seen := make(map[uint32]struct{}, len(states))
	for _, state := range states {
		if role, ok := p.findRole(state.RoleID); ok {
			seen[role.ReportID] = struct{}{}
		}
	}
	out := make([]uint32, 0, len(seen))
	for id := range seen {
		out = append(out, id)
	}
	slices.Sort(out)
	return out
}

// New 基于角色列表构建程序，并复制输入切片。
func New(roles []Role) *Program {
	out := make([]Role, len(roles))
	copy(out, roles)
	for i := range out {
		out[i].Literal = append([]byte(nil), roles[i].Literal...)
		if out[i].ReportID == 0 {
			out[i].ReportID = out[i].ID
		}
	}
	p := &Program{Roles: out, index: buildRoleIndex(out)}
	p.rebuildCandidateState()
	p.rebuildInstructionIndex()
	return p
}

// rebuildCandidateState 由当前 Roles 重新推导 matcher、候选器与首字节集合。
//
// 这是 New 与 Normalize 共用的唯一派生入口：两处分别推导会让“跳过通用匹配器”
// 的决定在 Normalize 之后被还原，候选路径随之永久失效。
func (p *Program) rebuildCandidateState() {
	p.miracles = p.miracles[:0]
	for _, role := range p.Roles {
		if miracle := newMiracle(role); miracle != nil {
			p.miracles = append(p.miracles, miracle)
		}
	}
	p.miracleReady = false
	p.miracleFirstSet = [4]uint64{}
	for i := range p.miracleBuckets {
		p.miracleBuckets[i] = nil
	}
	p.miracleFirstTable = simd.ByteSetTables{}
	p.matcher = buildRoleMatcher(p.Roles)
	// 单角色使用轻量候选路径，多角色继续使用共享自动机。
	if len(p.Roles) == 1 {
		p.matcher = nil
	}
	if len(p.miracles) == len(p.Roles) && len(p.Roles) > 1 {
		p.miracleReady = true
		// 所有角色均可由候选器直接定位时跳过通用多模式匹配器。
		// 完整 Role.Eligible 仍在候选确认阶段执行。
		p.matcher = nil
		for i, role := range p.Roles {
			if len(role.Literal) > 0 {
				first := role.Literal[0]
				outFirst := first
				p.miracleFirstSet[outFirst/64] |= 1 << uint(outFirst%64)
				p.miracleBuckets[first] = append(p.miracleBuckets[first], i)
				if role.CaseInsensitive {
					folded := foldASCII(first)
					p.miracleFirstSet[folded/64] |= 1 << uint(folded%64)
					if folded != first {
						p.miracleBuckets[folded] = append(p.miracleBuckets[folded], i)
					}
					if folded >= 'a' && folded <= 'z' {
						upper := folded - 'a' + 'A'
						p.miracleFirstSet[upper/64] |= 1 << uint(upper%64)
						if upper != first {
							p.miracleBuckets[upper] = append(p.miracleBuckets[upper], i)
						}
					}
				}
			}
		}
		p.miracleFirstTable = simd.FirstByteTables(p.miracleFirstSet)
	}
}

func (p *Program) findMiracleMulti(data []byte, from, to, limit int) []State {
	return p.findMiracleMultiInto(data, from, to, limit, nil)
}

func (p *Program) findMiracleMultiInto(data []byte, from, to, limit int, dst []State) []State {
	if p == nil || !p.miracleReady || from < 0 || to < from || to > len(data) || limit < 0 {
		return dst[:0]
	}
	out := dst[:0]
	visit := func(off int) bool {
		for _, idx := range p.miracleBuckets[data[off]] {
			role := p.Roles[idx]
			if off+len(role.Literal) > to || !role.Eligible(data, off) {
				continue
			}
			out = append(out, State{RoleID: role.ID, Offset: uint64(off)})
			if limit > 0 && len(out) >= limit {
				return false
			}
		}
		return true
	}
	backend := dispatch.DefaultBackend()
	// 候选枚举优先按宽窗口批量判定，剩余不足一个宽窗口时先用超向量窗口补齐，
	// 最后再逐字节回退判定；三段区间按起始位置无缝衔接，不会重复访问同一偏移。
	off := from
	for ; off+simd.WideWidth <= to; off += simd.WideWidth {
		mask, ok := backend.WindowMask64(data, off, &p.miracleFirstTable, 1)
		if !ok {
			break
		}
		for mask != 0 {
			bit := bits.TrailingZeros64(mask)
			if !visit(off + bit) {
				return SortStates(out)
			}
			mask &^= 1 << uint(bit)
		}
	}
	if off+simd.SuperWidth <= to {
		if mask, ok := backend.WindowMask(data, off, &p.miracleFirstTable, 1); ok {
			for mask != 0 {
				bit := bits.TrailingZeros32(mask)
				if !visit(off + bit) {
					return SortStates(out)
				}
				mask &^= 1 << uint(bit)
			}
			off += simd.SuperWidth
		}
	}
	for ; off < to; off++ {
		if p.miracleFirstSet[data[off]/64]&(1<<uint(data[off]%64)) == 0 {
			continue
		}
		if !visit(off) {
			break
		}
	}
	out = SortStates(out)
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

func (p *Program) rebuildInstructionIndex() {
	if p == nil {
		return
	}
	p.instructions = make(map[uint32][]Instruction)
	for _, instruction := range p.Instructions {
		p.instructions[instruction.RoleID] = append(p.instructions[instruction.RoleID], instruction)
	}
}

// SetInstructions 设置并校验角色指令表。
func (p *Program) SetInstructions(instructions []Instruction) error {
	if p == nil {
		return fmt.Errorf("nil rose program")
	}
	for _, instruction := range instructions {
		if err := instruction.Validate(p); err != nil {
			return err
		}
	}
	p.Instructions = append(p.Instructions[:0], instructions...)
	p.rebuildInstructionIndex()
	return nil
}

// InstructionsCopy 返回角色指令表副本。
func (p *Program) InstructionsCopy() []Instruction {
	if p == nil {
		return nil
	}
	return append([]Instruction(nil), p.Instructions...)
}

func buildRoleIndex(roles []Role) map[uint32]int {
	index := make(map[uint32]int, len(roles))
	for i, role := range roles {
		if _, exists := index[role.ID]; !exists {
			index[role.ID] = i
		}
	}
	return index
}

func buildRoleMatcher(roles []Role) *fdr.Matcher {
	if len(roles) == 0 {
		return nil
	}
	literals := make([]hwlm.Literal, 0, len(roles))
	for _, role := range roles {
		if role.ID == 0 || len(role.Literal) == 0 {
			continue
		}
		// 字节自动机只处理 ASCII 折叠；含非 ASCII 的角色走逐字节边界校验。
		if role.CaseInsensitive && !isASCII(role.Literal) {
			return nil
		}
		literals = append(literals, hwlm.Literal{ID: role.ID, Value: append([]byte(nil), role.Literal...), CaseInsensitive: role.CaseInsensitive})
	}
	if len(literals) == 0 {
		return nil
	}
	return fdr.New(literals)
}

// Normalize 按角色编号排序并删除重复编号，保证程序顺序稳定。
func (p *Program) Normalize() {
	if p == nil {
		return
	}
	sort.SliceStable(p.Roles, func(i, j int) bool { return p.Roles[i].ID < p.Roles[j].ID })
	out := p.Roles[:0]
	seen := map[uint32]struct{}{}
	for _, role := range p.Roles {
		if _, ok := seen[role.ID]; ok {
			continue
		}
		seen[role.ID] = struct{}{}
		out = append(out, role)
	}
	p.Roles = out
	p.rebuildCandidateState()
	p.index = buildRoleIndex(p.Roles)
	p.byReport = nil
	p.rebuildInstructionIndex()
}

// Clone 深拷贝程序，结果与原程序不共享角色与指令。
func (p *Program) Clone() *Program {
	if p == nil {
		return nil
	}
	out := New(p.Roles)
	out.Instructions = append([]Instruction(nil), p.Instructions...)
	out.rebuildInstructionIndex()
	for i := range out.Roles {
		out.Roles[i].Literal = append([]byte(nil), out.Roles[i].Literal...)
	}
	return out
}

// RoleCount 返回角色数量，nil 程序返回 0。
func (p *Program) RoleCount() int {
	if p == nil {
		return 0
	}
	return len(p.Roles)
}

// RoleIDs 返回按程序顺序排列的角色编号。
func (p *Program) RoleIDs() []uint32 {
	if p == nil {
		return nil
	}
	out := make([]uint32, len(p.Roles))
	for i, role := range p.Roles {
		out[i] = role.ID
	}
	return out
}

// CountMatches 返回输入中的角色命中数量。
func (p *Program) CountMatches(data []byte) int { return len(p.FindMatches(data)) }

// LiteralLengths 返回按角色编号排列的文字长度快照。
func (p *Program) LiteralLengths() map[uint32]int {
	if p == nil {
		return nil
	}
	out := make(map[uint32]int, len(p.Roles))
	for _, role := range p.Roles {
		out[role.ID] = len(role.Literal)
	}
	return out
}

// FindRole 按编号查找角色，未找到时 ok 为 false。
func (p *Program) FindRole(id uint32) (Role, bool) {
	r, ok := p.findRole(id)
	if !ok {
		return Role{}, false
	}
	// 保留对外返回值的隔离性，调用方修改 Literal 不会影响程序状态。
	r.Literal = append([]byte(nil), r.Literal...)
	return r, true
}

// findRole 返回共享底层字面量切片的角色，避免在只读路径上重复分配。
func (p *Program) findRole(id uint32) (Role, bool) {
	if p == nil {
		return Role{}, false
	}
	if p.index == nil {
		p.index = buildRoleIndex(p.Roles)
	}
	if i, ok := p.index[id]; ok && i >= 0 && i < len(p.Roles) {
		return p.Roles[i], true
	}
	return Role{}, false
}

// RoleByID 返回共享底层字面量切片的角色。仅在只读场景使用，避免对字面量做防御性复制。
// 调用方不得修改返回 Role 的 Literal 字段；如需独立副本请使用 FindRole。
func (p *Program) RoleByID(id uint32) (Role, bool) {
	return p.findRole(id)
}

// HasRole 判断角色编号是否存在。
func (p *Program) HasRole(id uint32) bool { _, ok := p.FindRole(id); return ok }

// FindRolesByReportID 返回映射到指定报告编号的角色快照。
func (p *Program) FindRolesByReportID(reportID uint32) []Role {
	if p == nil {
		return nil
	}
	out := make([]Role, 0)
	for _, role := range p.Roles {
		if role.ReportID == reportID {
			out = append(out, role.Clone())
		}
	}
	return out
}

// ensureReportIndex 返回按报告编号分组的角色下标，组内按扫描代价升序（相同代价按编号）。
// 索引惰性构建，调用方不得修改返回切片。
func (p *Program) ensureReportIndex() map[uint32][]int {
	if p.byReport != nil {
		return p.byReport
	}
	index := make(map[uint32][]int)
	for i, role := range p.Roles {
		index[role.ReportID] = append(index[role.ReportID], i)
	}
	for _, positions := range index {
		sort.SliceStable(positions, func(i, j int) bool {
			left, right := p.Roles[positions[i]], p.Roles[positions[j]]
			leftWeight, rightWeight := left.Weight(), right.Weight()
			if leftWeight != rightWeight {
				return leftWeight < rightWeight
			}
			return left.ID < right.ID
		})
	}
	p.byReport = index
	return index
}

// RolesForReport 按扫描代价升序返回指定报告编号的角色。
// 返回的角色共享程序底层字面量，调用方不得修改；如需独立副本请使用 FindRolesByReportID。
func (p *Program) RolesForReport(reportID uint32) []Role {
	if p == nil {
		return nil
	}
	positions := p.ensureReportIndex()[reportID]
	if len(positions) == 0 {
		return nil
	}
	out := make([]Role, 0, len(positions))
	for _, position := range positions {
		if position >= 0 && position < len(p.Roles) {
			out = append(out, p.Roles[position])
		}
	}
	return out
}

// CheapestRoleForReport 返回指定报告编号下扫描代价最低的角色代表。
// 代价相同时返回编号最小的角色，保证选择结果稳定。
func (p *Program) CheapestRoleForReport(reportID uint32) (Role, bool) {
	if p == nil {
		return Role{}, false
	}
	positions := p.ensureReportIndex()[reportID]
	if len(positions) == 0 {
		return Role{}, false
	}
	position := positions[0]
	if position < 0 || position >= len(p.Roles) {
		return Role{}, false
	}
	return p.Roles[position].Clone(), true
}

// ReportIDs 返回角色对应的报告编号快照并去重排序。
func (p *Program) ReportIDs() []uint32 {
	if p == nil {
		return nil
	}
	seen := map[uint32]struct{}{}
	for _, role := range p.Roles {
		seen[role.ReportID] = struct{}{}
	}
	out := make([]uint32, 0, len(seen))
	for id := range seen {
		out = append(out, id)
	}
	slices.Sort(out)
	return out
}

// Build 将 NFA 图降低为 Rose 程序，g 为 nil 时返回错误。
func Build(g *nfagraph.Graph) (*Program, error) {
	if g == nil {
		return nil, fmt.Errorf("nil graph")
	}
	if err := g.Validate(); err != nil {
		return nil, err
	}
	p := New(nil)
	used := make(map[uint32]struct{})
	for _, id := range g.Flow.Vertices() {
		n := g.Nodes[id]
		if n == nil || n.Kind != nfagraph.KindLiteral || len(n.Literal) == 0 {
			continue
		}
		if _, exists := used[uint32(id)]; exists {
			continue
		}
		role := Role{ID: uint32(id), ReportID: uint32(id)}
		first, last := id, id
		for {
			used[uint32(last)] = struct{}{}
			role.Literal = append(role.Literal, g.Nodes[last].Literal...)
			next := g.Flow.Successors(last)
			if len(next) != 1 {
				break
			}
			nextNode := g.Nodes[next[0]]
			if nextNode == nil || nextNode.Kind != nfagraph.KindLiteral || len(g.Flow.Predecessors(next[0])) != 1 {
				break
			}
			last = next[0]
		}
		for _, previous := range g.Flow.Predecessors(first) {
			if node := g.Nodes[previous]; node != nil && node.Kind == nfagraph.KindAssertion && node.Assertion == parser.BeginAbsolute {
				role.Anchored = true
			}
		}
		for _, next := range g.Flow.Successors(last) {
			if node := g.Nodes[next]; node != nil && node.Kind == nfagraph.KindAssertion && node.Assertion == parser.EndAbsolute {
				role.EndOfData = true
			}
		}
		p.Roles = append(p.Roles, role)
	}
	p.Normalize()
	return p, nil
}

// Validate 检查角色与指令序列是否自洽。
func (p *Program) Validate() error {
	if p == nil {
		return fmt.Errorf("nil program")
	}
	seen := map[uint32]bool{}
	for _, r := range p.Roles {
		if r.ID == 0 || r.ReportID == 0 || len(r.Literal) == 0 || r.HasMaxOffset && r.MaxOffset < r.MinOffset {
			return fmt.Errorf("invalid role")
		}
		if seen[r.ID] {
			return fmt.Errorf("duplicate role %d", r.ID)
		}
		seen[r.ID] = true
		if r.EndOfData && r.HasMaxOffset && r.MaxOffset < uint64(len(r.Literal)) {
			return fmt.Errorf("role %d end offset is too small", r.ID)
		}
	}
	for _, instruction := range p.Instructions {
		if err := instruction.Validate(p); err != nil {
			return err
		}
	}
	return nil
}

// Dump 将程序序列化为带版本号的 JSON 负载。
func (p *Program) Dump() ([]byte, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(struct {
		Version      int           `json:"version"`
		Roles        []Role        `json:"roles"`
		Instructions []Instruction `json:"instructions,omitempty"`
	}{2, p.Roles, p.Instructions})
}

// Load 从 JSON 负载恢复 Rose 程序。
func Load(data []byte) (*Program, error) {
	if len(data) > 64<<20 {
		return nil, fmt.Errorf("rose payload exceeds size limit")
	}
	var d struct {
		Version      int           `json:"version"`
		Roles        []Role        `json:"roles"`
		Instructions []Instruction `json:"instructions"`
	}
	if err := json.Unmarshal(data, &d); err != nil {
		return nil, err
	}
	if d.Version != 1 && d.Version != 2 {
		return nil, fmt.Errorf("unsupported rose version %d", d.Version)
	}
	p := New(d.Roles)
	p.Instructions = append(p.Instructions, d.Instructions...)
	if err := p.Validate(); err != nil {
		return nil, err
	}
	p.Normalize()
	p.rebuildInstructionIndex()
	return p, nil
}

// State 是调度器中的角色命中状态。
type State struct {
	RoleID   uint32
	Offset   uint64
	Priority uint32
}

// SortStates 返回按偏移、优先级和角色编号排序的状态副本。
func SortStates(states []State) []State {
	out := append([]State(nil), states...)
	sortStatesInPlace(out)
	return out
}

func sortStatesInPlace(out []State) {
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Offset != out[j].Offset {
			return out[i].Offset < out[j].Offset
		}
		if out[i].Priority != out[j].Priority {
			return out[i].Priority < out[j].Priority
		}
		return out[i].RoleID < out[j].RoleID
	})
}

// Validate 检查调度状态编号和偏移。
func (s State) Validate() error {
	if s.RoleID == 0 {
		return fmt.Errorf("invalid role id")
	}
	return nil
}

// Queue 是按优先级和编号排序的状态队列，零值即可使用。
type Queue struct{ items []State }

// Push 追加状态并维持优先级顺序，非法状态会被忽略。
func (q *Queue) Push(s State) {
	if q != nil && s.Validate() == nil {
		q.items = append(q.items, s)
		for i := len(q.items) - 1; i > 0; i-- {
			if stateBefore(q.items[i-1], q.items[i]) {
				break
			}
			q.items[i-1], q.items[i] = q.items[i], q.items[i-1]
		}
	}
}

// stateBefore 定义调度队列的稳定顺序：优先级、输入偏移、角色编号依次比较。
// 三个字段都是严格小于比较，保证等价状态不互相重排。
func stateBefore(a, b State) bool {
	if a.Priority != b.Priority {
		return a.Priority < b.Priority
	}
	if a.Offset != b.Offset {
		return a.Offset < b.Offset
	}
	return a.RoleID < b.RoleID
}

// PushAll 批量加入状态：队列内容与逐个 Push 完全一致，
// 但只做一次整体排序，避免逐元素插入排序退化为 O(n²)。
func (q *Queue) PushAll(states []State) {
	if q == nil || len(states) == 0 {
		return
	}
	appended := 0
	for _, state := range states {
		if state.Validate() != nil {
			continue
		}
		q.items = append(q.items, state)
		appended++
	}
	if appended == 0 {
		return
	}
	sort.SliceStable(q.items, func(i, j int) bool { return stateBefore(q.items[i], q.items[j]) })
}

// Compact 就地压缩队列：过滤非法状态，按 (优先级, 偏移, 角色编号) 稳定排序，
// 并对同一角色/偏移只保留优先级最低（数值最小）的状态。返回被移除的状态数量。
func (q *Queue) Compact() int {
	if q == nil || len(q.items) == 0 {
		return 0
	}
	total := len(q.items)
	valid := q.items[:0]
	for _, state := range q.items {
		if state.Validate() != nil {
			continue
		}
		valid = append(valid, state)
	}
	sort.SliceStable(valid, func(i, j int) bool { return stateBefore(valid[i], valid[j]) })
	out := valid[:0]
	seen := make(map[[2]uint64]struct{}, len(valid))
	for _, state := range valid {
		key := stateKey(state)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, state)
	}
	for i := len(out); i < total; i++ {
		q.items[i] = State{}
	}
	removed := total - len(out)
	q.items = out
	return removed
}

// PopLimit 取出不超过 limit 个状态；零值表示取出全部。
func (q *Queue) PopLimit(limit int) []State {
	if q == nil || limit < 0 {
		return nil
	}
	if limit == 0 || limit > len(q.items) {
		limit = len(q.items)
	}
	out := make([]State, 0, limit)
	for len(out) < limit {
		state, ok := q.Pop()
		if !ok {
			break
		}
		out = append(out, state)
	}
	return out
}

// Pop 取出优先级最高的状态，队列为空时返回 false。
func (q *Queue) Pop() (State, bool) {
	if q == nil || len(q.items) == 0 {
		return State{}, false
	}
	s := q.items[0]
	copy(q.items, q.items[1:])
	q.items[len(q.items)-1] = State{}
	q.items = q.items[:len(q.items)-1]
	return s, true
}

// Len 返回队列中的状态数量，nil 队列返回 0。
func (q *Queue) Len() int {
	if q == nil {
		return 0
	}
	return len(q.items)
}

// Cap 返回底层存储容量，用于观测复用效果。
func (q *Queue) Cap() int {
	if q == nil {
		return 0
	}
	return cap(q.items)
}

// Empty 判断队列中是否还有待处理状态。
func (q *Queue) Empty() bool { return q == nil || len(q.items) == 0 }

// Peek 返回优先级最高的状态但不移出队列。
func (q *Queue) Peek() (State, bool) {
	if q == nil || len(q.items) == 0 {
		return State{}, false
	}
	return q.items[0], true
}

// Reset 清空队列并保留已分配的存储。
func (q *Queue) Reset() {
	if q != nil {
		q.items = q.items[:0]
	}
}

// States 返回队列快照，不影响当前队列。
func (q *Queue) States() []State {
	if q == nil {
		return nil
	}
	return append([]State(nil), q.items...)
}

// Scheduler 按程序指令推进状态，并按稳定顺序汇聚报告事件。
type Scheduler struct {
	Program *Program
	Queue   Queue
	Reports *report.Manager
	// MaxSteps 限制一次调度最多执行的状态数，零值按角色规模推导安全上限。
	MaxSteps   int
	maxPending int
	active     map[[2]uint64]State
}

// Reset 清空队列、活跃状态和报告，便于复用同一调度器。
func (s *Scheduler) Reset() {
	if s != nil {
		s.Queue.Reset()
		if s.Reports != nil {
			s.Reports.Reset()
		}
		clear(s.active)
	}
}

// SetMaxReports 设置单次运行保留的报告上限，零值表示不限制。
func (s *Scheduler) SetMaxReports(n int) {
	if s != nil && s.Reports != nil {
		s.Reports.SetMaxEvents(n)
	}
}

// SetMaxPending 设置待执行状态上限，超出时保留更早状态。
func (s *Scheduler) SetMaxPending(n int) {
	if s == nil || n < 0 {
		return
	}
	s.maxPending = n
	if n <= 0 {
		return
	}
	// 截断前先压缩队列，去除重复状态并保证尾部截断保留的都是最低优先级候选。
	s.Queue.Compact()
	if s.Queue.Len() <= n {
		return
	}
	for _, st := range s.Queue.items[n:] {
		delete(s.active, stateKey(st))
	}
	s.Queue.items = s.Queue.items[:n]
}

// SetMaxSteps 设置一次调度的状态执行上限；负值被忽略。
func (s *Scheduler) SetMaxSteps(n int) {
	if s != nil && n >= 0 {
		s.MaxSteps = n
	}
}

func (s *Scheduler) runStepLimit() int {
	if s == nil {
		return 0
	}
	if s.MaxSteps > 0 {
		return s.MaxSteps
	}
	roles := 1
	if s.Program != nil && len(s.Program.Roles) > 0 {
		roles = len(s.Program.Roles)
	}
	// 默认上限足以覆盖正常角色图，同时阻断偏移不断增长的指令环。
	if roles > int(^uint(0)>>1)/4096 {
		return int(^uint(0) >> 1)
	}
	return roles * 4096
}

// NewScheduler 创建绑定程序的调度器，program 可为 nil。
func NewScheduler(program *Program) *Scheduler {
	return &Scheduler{Program: program, Reports: report.New(), active: make(map[[2]uint64]State)}
}

// Activate 将状态放入待处理队列，非法状态会被忽略。
func (s *Scheduler) Activate(state State) {
	if s == nil || state.Validate() != nil {
		return
	}
	if s.active == nil {
		s.active = make(map[[2]uint64]State)
	}
	key := [2]uint64{uint64(state.RoleID), state.Offset}
	if existing, ok := s.active[key]; ok {
		if existing.Priority <= state.Priority {
			return
		}
		s.Queue.remove(existing)
	}
	s.active[key] = state
	s.Queue.Push(state)
	// 队列已按优先级排序，截断尾部即可保留最早候选。
	s.enforcePendingLimit()
}

// ActivateChecked 仅激活已在程序中声明的角色。
func (s *Scheduler) ActivateChecked(state State) bool {
	if s == nil || s.Program == nil || !s.Program.HasRole(state.RoleID) {
		return false
	}
	if state.Validate() != nil {
		return false
	}
	key := stateKey(state)
	previous, existed := s.active[key]
	s.Activate(state)
	current, present := s.active[key]
	return present && (!existed || current != previous)
}

func (q *Queue) remove(target State) bool {
	if q == nil {
		return false
	}
	for index, item := range q.items {
		if item != target {
			continue
		}
		copy(q.items[index:], q.items[index+1:])
		q.items[len(q.items)-1] = State{}
		q.items = q.items[:len(q.items)-1]
		return true
	}
	return false
}

// RemoveRoleOffset 删除指定角色和偏移的待执行状态。
func (q *Queue) RemoveRoleOffset(roleID uint32, offset uint64) bool {
	if q == nil {
		return false
	}
	for _, item := range q.items {
		if item.RoleID == roleID && item.Offset == offset {
			return q.remove(item)
		}
	}
	return false
}

func stateKey(state State) [2]uint64 { return [2]uint64{uint64(state.RoleID), state.Offset} }

// QueueLen 返回待处理状态数量，nil 调度器返回 0。
func (s *Scheduler) QueueLen() int {
	if s == nil {
		return 0
	}
	return s.Queue.Len()
}

// ActiveCount 返回已激活且尚未执行的状态数。
func (s *Scheduler) ActiveCount() int {
	if s == nil {
		return 0
	}
	return len(s.active)
}

// Pending 返回当前待执行状态快照。
func (s *Scheduler) Pending() []State {
	if s == nil {
		return nil
	}
	return s.Queue.States()
}

// ActivateMatches 将程序在输入中的全部角色命中加入调度队列。
func (s *Scheduler) ActivateMatches(data []byte) int {
	if s == nil || s.Program == nil {
		return 0
	}
	matches := s.Program.FindMatches(data)
	s.activateBatch(matches)
	return len(matches)
}

// ActivateMatchesRange 将完全位于指定半开区间内的角色命中加入调度队列。
func (s *Scheduler) ActivateMatchesRange(data []byte, from, to int) int {
	if s == nil || s.Program == nil {
		return 0
	}
	matches := s.Program.FindMatchesRange(data, from, to)
	s.activateBatch(matches)
	return len(matches)
}

// activateBatch 批量激活状态：先在批内压缩键，再补齐 active 表并把整批
// 状态一次性并入队列。等价于逐个 Activate，但队列只压缩一次。
func (s *Scheduler) activateBatch(states []State) {
	if s == nil || len(states) == 0 {
		return
	}
	if s.active == nil {
		s.active = make(map[[2]uint64]State)
	}
	batch := make([]State, 0, len(states))
	index := make(map[[2]uint64]int, len(states))
	for _, state := range states {
		if state.Validate() != nil {
			continue
		}
		key := stateKey(state)
		if position, exists := index[key]; exists {
			if state.Priority < batch[position].Priority {
				batch[position] = state
			}
			continue
		}
		index[key] = len(batch)
		batch = append(batch, state)
	}
	pending := batch[:0]
	for _, state := range batch {
		key := stateKey(state)
		if existing, ok := s.active[key]; ok {
			if existing.Priority <= state.Priority {
				continue
			}
			// 更低优先级的状态会替换既有待执行项，需要先移除旧队列项。
			s.Queue.remove(existing)
		}
		s.active[key] = state
		pending = append(pending, state)
	}
	if len(pending) == 0 {
		return
	}
	s.Queue.PushAll(pending)
	s.enforcePendingLimit()
}

// enforcePendingLimit 按 maxPending 截断队列尾巴并同步 active 表。
func (s *Scheduler) enforcePendingLimit() {
	if s == nil || s.maxPending <= 0 || s.Queue.Len() <= s.maxPending {
		return
	}
	for _, dropped := range s.Queue.items[s.maxPending:] {
		delete(s.active, stateKey(dropped))
	}
	s.Queue.items = s.Queue.items[:s.maxPending]
}

// ActivateStateAt 将角色命中按输入绝对偏移激活，并返回是否新增状态。
func (s *Scheduler) ActivateStateAt(roleID uint32, offset uint64, priority uint32) bool {
	if s == nil {
		return false
	}
	return s.ActivateChecked(State{RoleID: roleID, Offset: offset, Priority: priority})
}

// Transition 将现有状态移动到新的偏移，旧状态会被去重。
func (s *Scheduler) Transition(state State, offset uint64) bool {
	if s == nil || state.Validate() != nil || s.Program == nil || !s.Program.HasRole(state.RoleID) {
		return false
	}
	if s.active == nil {
		return false
	}
	if _, ok := s.active[stateKey(state)]; !ok {
		return false
	}
	if s.active != nil {
		delete(s.active, stateKey(state))
	}
	s.Queue.RemoveRoleOffset(state.RoleID, state.Offset)
	state.Offset = offset
	s.Activate(state)
	return true
}

// RunMatches 激活并执行输入中的角色命中。
func (s *Scheduler) RunMatches(data []byte, onState func(State) []report.Event) []report.Event {
	if s == nil {
		return nil
	}
	s.ActivateMatches(data)
	return s.Run(onState)
}

// CatchUp 仅处理指定区间中新到达的角色命中，并保留既有报告用于重复抑制。
func (s *Scheduler) CatchUp(data []byte, from, to int, onState func(State) []report.Event) []report.Event {
	if s == nil {
		return nil
	}
	s.ActivateMatchesRange(data, from, to)
	return s.Run(onState)
}

// RunReports 执行角色扫描并将角色对应的报告编号写入统一报告管理器。
func (s *Scheduler) RunReports(data []byte) []report.Event {
	return s.RunReportsWithSOM(data, false)
}

// RunReportsWithSOM 执行角色扫描并可选择写入匹配起点。
func (s *Scheduler) RunReportsWithSOM(data []byte, includeSOM bool) []report.Event {
	if s == nil {
		return nil
	}
	s.ActivateMatches(data)
	return s.RunProgram(includeSOM)
}

// RunProgram 执行已激活状态及其角色指令，返回稳定排序的报告快照。
func (s *Scheduler) RunProgram(includeSOM bool) []report.Event {
	if s == nil || s.Program == nil {
		return nil
	}
	return s.Run(func(state State) []report.Event {
		role, ok := s.Program.FindRole(state.RoleID)
		if !ok {
			return nil
		}
		if s.Program.instructions == nil {
			s.Program.rebuildInstructionIndex()
		}
		instructions := s.Program.instructions[state.RoleID]
		if len(instructions) == 0 {
			end, ok := roleEnd(state.Offset, len(role.Literal))
			if !ok {
				return nil
			}
			event := report.Event{ID: role.ReportID, From: state.Offset, To: end}
			if includeSOM {
				event.SOM = state.Offset
			}
			return []report.Event{event}
		}
		events := make([]report.Event, 0, len(instructions))
		for _, instruction := range instructions {
			switch instruction.Kind {
			case InstructionReport:
				id := instruction.ReportID
				if id == 0 {
					id = role.ReportID
				}
				end, ok := roleEnd(state.Offset, len(role.Literal))
				if !ok {
					continue
				}
				event := report.Event{ID: id, From: state.Offset, To: end, Flags: instruction.Flags}
				if includeSOM || instruction.IncludeSOM {
					event.SOM = state.Offset
				}
				events = append(events, event)
			case InstructionActivate, InstructionTransition:
				offset, ok := applyOffsetDelta(state.Offset, instruction.OffsetDelta)
				if ok {
					s.ActivateChecked(State{RoleID: instruction.TargetID, Offset: offset, Priority: state.Priority})
				}
			default:
			}
		}
		return events
	})
}

func applyOffsetDelta(offset uint64, delta int64) (uint64, bool) {
	if delta >= 0 {
		value := uint64(delta)
		if offset > ^uint64(0)-value {
			return 0, false
		}
		return offset + value, true
	}
	value := uint64(-(delta + 1)) + 1
	if value > offset {
		return 0, false
	}
	return offset - value, true
}

// Run 逐个执行队列中的状态，并用 onState 的结果汇总报告事件。
func (s *Scheduler) Run(onState func(State) []report.Event) []report.Event {
	if s == nil {
		return nil
	}
	if s.Reports == nil {
		s.Reports = report.New()
	}
	executed := make(map[[2]uint64]struct{}, s.Queue.Len())
	steps := 0
	stepLimit := s.runStepLimit()
	for {
		if stepLimit > 0 && steps >= stepLimit {
			break
		}
		st, ok := s.Queue.Pop()
		if !ok {
			break
		}
		steps++
		delete(s.active, stateKey(st))
		key := stateKey(st)
		if _, seen := executed[key]; seen {
			continue
		}
		executed[key] = struct{}{}
		if onState == nil {
			continue
		}
		for _, e := range onState(st) {
			s.Reports.Add(e)
			if s.Reports.Stopped() {
				return s.Reports.Events()
			}
		}
	}
	s.Reports.Deduplicate()
	return s.Reports.Events()
}

// RunLimit 执行调度并限制最多产生指定数量的报告。
func (s *Scheduler) RunLimit(onState func(State) []report.Event, limit int) []report.Event {
	if s == nil || limit < 0 {
		return nil
	}
	if s.Reports == nil {
		s.Reports = report.New()
	}
	s.Reports.SetMaxEvents(limit)
	return s.Run(onState)
}

// RunCount 返回本次实际执行的状态数量及生成报告。
func (s *Scheduler) RunCount(onState func(State) []report.Event) (int, []report.Event) {
	if s == nil {
		return 0, nil
	}
	if s.Reports == nil {
		s.Reports = report.New()
	}
	count := 0
	stepLimit := s.runStepLimit()
	executed := make(map[[2]uint64]struct{}, s.Queue.Len())
	for !s.Queue.Empty() {
		if stepLimit > 0 && count >= stepLimit {
			break
		}
		st, ok := s.Queue.Pop()
		if !ok {
			break
		}
		delete(s.active, stateKey(st))
		key := stateKey(st)
		if _, seen := executed[key]; seen {
			continue
		}
		executed[key] = struct{}{}
		count++
		if onState != nil {
			for _, event := range onState(st) {
				s.Reports.Add(event)
			}
		}
		if s.Reports != nil && s.Reports.Stopped() {
			break
		}
	}
	s.Reports.Deduplicate()
	return count, s.Reports.Events()
}
