// Package rose 提供角色调度器的基础契约。
package rose

import (
	"encoding/json"
	"fmt"
	"github.com/smartwalle/scankit/internal/dispatch"
	"github.com/smartwalle/scankit/internal/fdr"
	"github.com/smartwalle/scankit/internal/hwlm"
	"github.com/smartwalle/scankit/internal/nfagraph"
	"github.com/smartwalle/scankit/internal/parser"
	"github.com/smartwalle/scankit/internal/report"
	"github.com/smartwalle/scankit/internal/simd"
	"sort"
	"unicode"
	"unicode/utf8"
)

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

func (r Role) Length() int { return len(r.Literal) }
func (r Role) Clone() Role { r.Literal = append([]byte(nil), r.Literal...); return r }

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
	for i, c := range r.Literal {
		got := data[off+i]
		if r.CaseInsensitive {
			if c >= 'A' && c <= 'Z' {
				c += 'a' - 'A'
			}
			if got >= 'A' && got <= 'Z' {
				got += 'a' - 'A'
			}
		}
		if got != c {
			return false
		}
	}
	return true
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

type Program struct {
	Roles           []Role
	Instructions    []Instruction
	matcher         *fdr.Matcher
	// matchBuf 复用 matcher 返回的命中缓冲，避免每次 FindMatchesInto 都重新分配。
	matchBuf        []fdr.Match
	miracles        []*Miracle
	miracleBuckets  [256][]int
	miracleFirstSet [4]uint64
	miracleReady    bool
	index           map[uint32]int
	instructions    map[uint32][]Instruction
}

// InstructionKind 表示角色状态执行时的动作类型。
type InstructionKind uint8

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

func (p *Program) Empty() bool { return p == nil || len(p.Roles) == 0 }
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

func (p *Program) FindMatches(data []byte) []State {
	return p.FindMatchesInto(data, nil)
}

// FindMatchesInto 将角色命中写入复用缓冲，供整块扫描避免重复创建状态切片。
func (p *Program) FindMatchesInto(data []byte, dst []State) []State {
	if p == nil {
		return dst[:0]
	}
	matcher := p.matcher
	if matcher == nil {
		matcher = buildRoleMatcher(p.Roles)
	}
	if matcher != nil {
		// 复用 matcher 自身的匹配结果缓冲，避免每次扫描都重新分配。
		matches := matcher.FindInto(data, p.matchBuf[:0])
		p.matchBuf = matches
		out := dst[:0]
		if cap(out) < len(matches) {
			out = make([]State, 0, len(matches))
		}
		for _, match := range matches {
			role, ok := p.FindRole(match.ID)
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
		return p.findMiracleMulti(data, 0, len(data), 0)
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
	matcher := p.matcher
	if matcher == nil {
		matcher = buildRoleMatcher(p.Roles)
	}
	if matcher == nil {
		if p.miracleReady {
			// 每个角色单独限量会在合并排序前丢失更早的候选，
			// 这里先收集完整候选，再统一去重、排序和截断。
			out := make([]State, 0, limit*len(p.miracles))
			out = p.findMiracleMulti(data, 0, len(data), 0)
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
		role, ok := p.FindRole(match.ID)
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
	matcher := p.matcher
	if matcher == nil {
		matcher = buildRoleMatcher(p.Roles)
	}
	if matcher != nil {
		matches := matcher.FindRange(data, from, to)
		out := make([]State, 0, len(matches))
		seen := make(map[[2]uint64]struct{}, len(matches))
		for _, match := range matches {
			role, ok := p.FindRole(match.ID)
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
		role, ok := p.FindRole(state.RoleID)
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
			role, ok := p.FindRole(match.ID)
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
			role, ok := p.FindRole(match.ID)
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
		start := from - length
		if start < 0 {
			start = 0
		}
		last := to - length - 1
		if last >= len(data)-length {
			last = len(data) - length
		}
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
		if role, ok := p.FindRole(state.RoleID); ok {
			seen[role.ReportID] = struct{}{}
		}
	}
	out := make([]uint32, 0, len(seen))
	for id := range seen {
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func New(roles []Role) *Program {
	out := make([]Role, len(roles))
	copy(out, roles)
	for i := range out {
		out[i].Literal = append([]byte(nil), roles[i].Literal...)
		if out[i].ReportID == 0 {
			out[i].ReportID = out[i].ID
		}
	}
	matcher := buildRoleMatcher(out)
	// 单角色使用轻量候选路径，多角色继续使用共享自动机。
	if len(out) == 1 {
		matcher = nil
	}
	p := &Program{Roles: out, matcher: matcher, index: buildRoleIndex(out)}
	for _, role := range out {
		if miracle := newMiracle(role); miracle != nil {
			p.miracles = append(p.miracles, miracle)
		}
	}
	if len(p.miracles) == len(out) && len(out) > 1 {
		p.miracleReady = true
		// 所有角色均可由候选器直接定位时跳过通用多模式匹配器。
		// 完整 Role.Eligible 仍在候选确认阶段执行。
		p.matcher = nil
		for i, role := range out {
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
	}
	p.rebuildInstructionIndex()
	return p
}

func (p *Program) findMiracleMulti(data []byte, from, to, limit int) []State {
	if p == nil || !p.miracleReady || from < 0 || to < from || to > len(data) || limit < 0 {
		return nil
	}
	out := make([]State, 0)
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
	const width = simd.SuperWidth / 2
	for off := from; off+width <= to; off += width {
		vec, ok := backend.Load(data, off)
		if !ok {
			break
		}
		mask := backend.ByteSetMask(vec, p.miracleFirstSet)
		for mask != 0 {
			bit := trailingZeros16(mask)
			if !visit(off + bit) {
				return SortStates(out)
			}
			mask &^= 1 << uint(bit)
		}
	}
	start := from + ((to-from)/width)*width
	for off := start; off < to; off++ {
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

func trailingZeros16(v uint16) int {
	if v == 0 {
		return 0
	}
	n := 0
	for v&1 == 0 {
		v >>= 1
		n++
	}
	return n
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
	p.matcher = buildRoleMatcher(p.Roles)
	if len(p.Roles) == 1 {
		p.matcher = nil
	}
	p.miracles = p.miracles[:0]
	for _, role := range p.Roles {
		if miracle := newMiracle(role); miracle != nil {
			p.miracles = append(p.miracles, miracle)
		}
	}
	p.index = buildRoleIndex(p.Roles)
	p.rebuildInstructionIndex()
}
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
func (p *Program) FindRole(id uint32) (Role, bool) {
	if p == nil {
		return Role{}, false
	}
	if p.index == nil {
		p.index = buildRoleIndex(p.Roles)
	}
	if i, ok := p.index[id]; ok && i >= 0 && i < len(p.Roles) {
		r := p.Roles[i]
		r.Literal = append([]byte(nil), r.Literal...)
		return r, true
	}
	return Role{}, false
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
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
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

type Queue struct{ items []State }

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
func stateBefore(a, b State) bool {
	if a.Priority != b.Priority {
		return a.Priority < b.Priority
	}
	if a.Offset != b.Offset {
		return a.Offset < b.Offset
	}
	return a.RoleID <= b.RoleID
}

// PushAll 批量加入状态并按优先级稳定排序。
func (q *Queue) PushAll(states []State) {
	if q == nil {
		return
	}
	for _, state := range states {
		q.Push(state)
	}
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
func (q *Queue) Len() int {
	if q == nil {
		return 0
	}
	return len(q.items)
}
func (q *Queue) Cap() int {
	if q == nil {
		return 0
	}
	return cap(q.items)
}
func (q *Queue) Empty() bool { return q == nil || len(q.items) == 0 }
func (q *Queue) Peek() (State, bool) {
	if q == nil || len(q.items) == 0 {
		return State{}, false
	}
	return q.items[0], true
}
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

type Scheduler struct {
	Program *Program
	Queue   Queue
	Reports *report.Manager
	// MaxSteps 限制一次调度最多执行的状态数，零值按角色规模推导安全上限。
	MaxSteps   int
	maxPending int
	active     map[[2]uint64]State
}

func (s *Scheduler) Reset() {
	if s != nil {
		s.Queue.Reset()
		if s.Reports != nil {
			s.Reports.Reset()
		}
		clear(s.active)
	}
}
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
	if n == 0 || s.Queue.Len() <= n {
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

func NewScheduler(program *Program) *Scheduler {
	return &Scheduler{Program: program, Reports: report.New(), active: make(map[[2]uint64]State)}
}
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
	if s.maxPending > 0 && s.Queue.Len() > s.maxPending {
		// 队列已按优先级排序，截断尾部即可保留最早候选。
		for _, dropped := range s.Queue.items[s.maxPending:] {
			delete(s.active, stateKey(dropped))
		}
		s.Queue.items = s.Queue.items[:s.maxPending]
	}
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
	for _, state := range matches {
		s.Activate(state)
	}
	return len(matches)
}

// ActivateMatchesRange 将完全位于指定半开区间内的角色命中加入调度队列。
func (s *Scheduler) ActivateMatchesRange(data []byte, from, to int) int {
	if s == nil || s.Program == nil {
		return 0
	}
	matches := s.Program.FindMatchesRange(data, from, to)
	for _, state := range matches {
		s.Activate(state)
	}
	return len(matches)
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
