// Package fdr 提供确定性的通用多文字匹配器。
package fdr

import (
	"encoding/json"
	"fmt"
	"github.com/smartwalle/scankit/internal/dispatch"
	"github.com/smartwalle/scankit/internal/hwlm"
	"github.com/smartwalle/scankit/internal/simd"
	"sort"
)

const version = 1

type Match struct {
	ID       uint32
	From, To int
}

// Length 返回文字命中的长度。
func (m Match) Length() int {
	if m.To < m.From {
		return 0
	}
	return m.To - m.From
}

// Empty 判断是否为空命中。
func (m Match) Empty() bool { return m.From == m.To }

// Valid 判断命中范围是否位于输入长度内。
func (m Match) Valid(length int) bool {
	return length >= 0 && m.From >= 0 && m.From <= m.To && m.To <= length
}

type acNode struct {
	next    [256]int
	failure int
	output  []hwlm.Literal
}

type automaton struct {
	nodes   []acNode
	rootSet [4]uint64
}

// Matcher 使用两套 Aho-Corasick 自动机处理大小写敏感与不敏感文字。
type Matcher struct {
	literals  []hwlm.Literal
	sensitive automaton
	folded    automaton
	// sensitiveSet/foldedSet 是两棵自动机根状态的预编译首字节集合，
	// 在构建期解析一次，供每次扫描的窗口跳过逻辑直接复用。
	sensitiveSet simd.ByteSet
	foldedSet    simd.ByteSet
}

// Validate 检查文字匹配器的编号和文字内容。
func (m *Matcher) Validate() error {
	if m == nil {
		return fmt.Errorf("nil matcher")
	}
	seen := map[uint32]struct{}{}
	for _, literal := range m.literals {
		if err := literal.Validate(); err != nil {
			return err
		}
		if _, ok := seen[literal.ID]; ok {
			return fmt.Errorf("duplicate literal id %d", literal.ID)
		}
		seen[literal.ID] = struct{}{}
	}
	return nil
}

// Dump 序列化文字匹配器。
func (m *Matcher) Dump() ([]byte, error) {
	if m == nil {
		return nil, fmt.Errorf("nil matcher")
	}
	return json.Marshal(struct {
		Version  int            `json:"version"`
		Literals []hwlm.Literal `json:"literals"`
	}{version, m.Literals()})
}

// Load 反序列化文字匹配器并校验版本。
func Load(data []byte) (*Matcher, error) {
	var d struct {
		Version  int            `json:"version"`
		Literals []hwlm.Literal `json:"literals"`
	}
	if err := json.Unmarshal(data, &d); err != nil {
		return nil, err
	}
	if d.Version != version {
		return nil, fmt.Errorf("unsupported matcher version %d", d.Version)
	}
	return New(d.Literals), nil
}

func New(literals []hwlm.Literal) *Matcher {
	out := &Matcher{literals: hwlm.Deduplicate(literals)}
	for i := range out.literals {
		out.literals[i] = out.literals[i].Clone()
	}
	out.sensitive = buildAutomaton(out.literals, false)
	out.folded = buildAutomaton(out.literals, true)
	out.sensitiveSet = simd.NewByteSet(automatonRootSet(out.sensitive.nodes))
	out.foldedSet = simd.NewByteSet(automatonRootSet(out.folded.nodes))
	return out
}
func (m *Matcher) Len() int {
	if m == nil {
		return 0
	}
	return len(m.literals)
}
func (m *Matcher) Literals() []hwlm.Literal {
	if m == nil {
		return nil
	}
	out := append([]hwlm.Literal(nil), m.literals...)
	for i := range out {
		out[i].Value = append([]byte(nil), m.literals[i].Value...)
	}
	return out
}
func (m *Matcher) Empty() bool { return m == nil || len(m.literals) == 0 }
func (m *Matcher) Clone() *Matcher {
	if m == nil {
		return nil
	}
	out := New(m.literals)
	for i := range out.literals {
		out.literals[i].Value = append([]byte(nil), out.literals[i].Value...)
	}
	return out
}
func (m *Matcher) FindFirst(data []byte) (Match, bool) {
	all := m.Find(data)
	if len(all) == 0 {
		return Match{}, false
	}
	return all[0], true
}
func (m *Matcher) Count(data []byte) int { return len(m.Find(data)) }

// FindReverse 返回按结束位置和起点逆序排列的文字命中。
func (m *Matcher) FindReverse(data []byte) []Match {
	out := m.Find(data)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].To != out[j].To {
			return out[i].To > out[j].To
		}
		if out[i].From != out[j].From {
			return out[i].From > out[j].From
		}
		return out[i].ID > out[j].ID
	})
	return out
}

// MatchAt 返回指定起点开始的文字命中。
func (m *Matcher) MatchAt(data []byte, offset int) []Match {
	if m == nil || offset < 0 || offset > len(data) {
		return nil
	}
	out := make([]Match, 0)
	for _, literal := range m.literals {
		if offset+len(literal.Value) > len(data) {
			continue
		}
		matched := true
		for i, value := range literal.Value {
			got := data[offset+i]
			if literal.CaseInsensitive {
				got, value = foldASCII(got), foldASCII(value)
			}
			if got != value {
				matched = false
				break
			}
		}
		if matched {
			out = append(out, Match{literal.ID, offset, offset + len(literal.Value)})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}
func (m *Matcher) FindLimit(data []byte, limit int) []Match {
	if limit < 0 {
		return nil
	}
	if limit == 0 {
		return m.Find(data)
	}
	if m == nil {
		return nil
	}
	out := make([]Match, 0)
	seen := map[[3]int]struct{}{}
	appendAutomaton := func(nodes []acNode, folded bool) bool {
		if len(nodes) == 0 {
			return true
		}
		state := 0
		for offset, value := range data {
			if folded {
				value = foldASCII(value)
			}
			state = nodes[state].next[value]
			for _, literal := range nodes[state].output {
				from := offset - len(literal.Value) + 1
				key := [3]int{int(literal.ID), from, offset + 1}
				if _, ok := seen[key]; ok {
					continue
				}
				seen[key] = struct{}{}
				out = append(out, Match{literal.ID, from, offset + 1})
				// 先完整收集两套自动机结果，最终按稳定顺序截断。
			}
		}
		return true
	}
	appendAutomaton(m.sensitive.nodes, false)
	appendAutomaton(m.folded.nodes, true)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].From != out[j].From {
			return out[i].From < out[j].From
		}
		if out[i].ID != out[j].ID {
			return out[i].ID < out[j].ID
		}
		return out[i].To < out[j].To
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

// FindFrom 返回起始偏移不小于 from 的全部文字命中。
func (m *Matcher) FindFrom(data []byte, from int) []Match {
	if from < 0 {
		from = 0
	}
	out := []Match{}
	for _, match := range m.Find(data) {
		if match.From >= from {
			out = append(out, match)
		}
	}
	return out
}

// FindFromLimit 返回指定偏移后的有限数量文字命中。
func (m *Matcher) FindFromLimit(data []byte, from, limit int) []Match {
	if limit < 0 {
		return nil
	}
	out := m.FindFrom(data, from)
	if limit > 0 && len(out) > limit {
		return out[:limit]
	}
	return out
}

// FindRange 返回完全落在半开区间内的文字命中。
func (m *Matcher) FindRange(data []byte, from, to int) []Match {
	if from < 0 || to < from || to > len(data) {
		return nil
	}
	out := make([]Match, 0)
	for _, match := range m.FindFrom(data, from) {
		if match.From >= to {
			break
		}
		if match.To <= to {
			out = append(out, match)
		}
	}
	return out
}

// FindRangeLimit 返回区间内不超过 limit 个文字命中。
func (m *Matcher) FindRangeLimit(data []byte, from, to, limit int) []Match {
	if limit < 0 || from < 0 || to < from || to > len(data) {
		return nil
	}
	if limit == 0 {
		return m.FindRange(data, from, to)
	}
	out := make([]Match, 0, limit)
	for _, match := range m.FindFromLimit(data, from, 0) {
		if match.From >= to {
			break
		}
		if match.To <= to {
			out = append(out, match)
			if len(out) >= limit {
				break
			}
		}
	}
	return out
}

// FindEndRange 返回结束偏移位于半开区间内的文字命中。
func (m *Matcher) FindEndRange(data []byte, from, to int) []Match {
	if m == nil || from < 0 || to < from || to > len(data) {
		return nil
	}
	maxLen := 0
	for _, literal := range m.Literals() {
		if len(literal.Value) > maxLen {
			maxLen = len(literal.Value)
		}
	}
	start := from - maxLen
	if start < 0 {
		start = 0
	}
	out := make([]Match, 0)
	for _, match := range m.FindFrom(data, start) {
		if match.From >= to {
			break
		}
		if match.To >= from && match.To < to {
			out = append(out, match)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].To != out[j].To {
			return out[i].To < out[j].To
		}
		if out[i].From != out[j].From {
			return out[i].From < out[j].From
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// FindEndRangeLimit 返回结束偏移在区间内且不超过 limit 的文字命中。
func (m *Matcher) FindEndRangeLimit(data []byte, from, to, limit int) []Match {
	if limit < 0 {
		return nil
	}
	out := m.FindEndRange(data, from, to)
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}
func (m *Matcher) FindByID(data []byte, id uint32) []Match {
	out := []Match{}
	for _, v := range m.Find(data) {
		if v.ID == id {
			out = append(out, v)
		}
	}
	return out
}

// LongestForID 返回指定文字在每个起点的最长命中。
func (m *Matcher) LongestForID(data []byte, id uint32) []Match {
	if m == nil {
		return nil
	}
	byStart := make(map[int]Match)
	for _, match := range m.FindByID(data, id) {
		if old, ok := byStart[match.From]; !ok || match.To > old.To {
			byStart[match.From] = match
		}
	}
	out := make([]Match, 0, len(byStart))
	for _, match := range byStart {
		out = append(out, match)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].From < out[j].From })
	return out
}

// FindByIDRangeLimit 返回指定文字在区间内的有限命中集合。
func (m *Matcher) FindByIDRangeLimit(data []byte, id uint32, from, to, limit int) []Match {
	if m == nil || limit < 0 || from < 0 || to < from || to > len(data) {
		return nil
	}
	out := make([]Match, 0)
	for _, match := range m.FindRange(data, from, to) {
		if match.ID != id {
			continue
		}
		out = append(out, match)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out
}

// Contains 判断输入中是否至少包含一个文字命中。
func (m *Matcher) Contains(data []byte) bool { return len(m.Find(data)) > 0 }

// IDs 返回已注册文字编号快照，保持声明顺序并去重。
func (m *Matcher) IDs() []uint32 {
	if m == nil {
		return nil
	}
	seen := map[uint32]struct{}{}
	out := make([]uint32, 0, len(m.literals))
	for _, literal := range m.literals {
		if _, ok := seen[literal.ID]; ok {
			continue
		}
		seen[literal.ID] = struct{}{}
		out = append(out, literal.ID)
	}
	return out
}

// CountByID 返回指定文字编号的命中数。
func (m *Matcher) CountByID(data []byte, id uint32) int { return len(m.FindByID(data, id)) }

// CountByIDLimit 统计指定文字编号的命中数量，并在达到上限时提前结束。
func (m *Matcher) CountByIDLimit(data []byte, id uint32, limit int) int {
	if limit < 0 {
		return 0
	}
	count := 0
	for _, match := range m.Find(data) {
		if match.ID == id {
			count++
			if limit > 0 && count >= limit {
				break
			}
		}
	}
	return count
}
func (m *Matcher) Find(data []byte) []Match {
	return m.FindInto(data, nil)
}

// FindInto 将候选结果写入调用方切片，减少重复扫描时的分配。
func (m *Matcher) FindInto(data []byte, dst []Match) []Match {
	if m == nil {
		return dst[:0]
	}
	out := dst[:0]
	appendMatches := func(nodes []acNode, rootSet *simd.ByteSet, folded bool) {
		if len(nodes) == 0 {
			return
		}
		state := 0
		backend := dispatch.DefaultBackend()
		for offset := 0; offset < len(data); offset++ {
			// 处于根状态时，整块不含首字节的输入不会改变状态，可安全跳过。
			if state == 0 && offset+simd.SuperWidth/2 <= len(data) {
				vector, ok := backend.Load(data, offset)
				if ok && backend.ByteSetMaskPrepared(vector, rootSet) == 0 {
					offset += simd.SuperWidth/2 - 1
					continue
				}
			}
			value := data[offset]
			if folded {
				value = foldASCII(value)
			}
			state = nodes[state].next[value]
			for _, literal := range nodes[state].output {
				from := offset - len(literal.Value) + 1
				out = append(out, Match{literal.ID, from, offset + 1})
			}
		}
	}
	appendMatches(m.sensitive.nodes, &m.sensitiveSet, false)
	appendMatches(m.folded.nodes, &m.foldedSet, true)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].From != out[j].From {
			return out[i].From < out[j].From
		}
		if out[i].ID != out[j].ID {
			return out[i].ID < out[j].ID
		}
		return out[i].To < out[j].To
	})
	if len(out) > 1 {
		write := 1
		for _, match := range out[1:] {
			if match == out[write-1] {
				continue
			}
			out[write] = match
			write++
		}
		out = out[:write]
	}
	return out
}

func automatonRootSet(nodes []acNode) [4]uint64 {
	var set [4]uint64
	if len(nodes) == 0 {
		return set
	}
	for value, child := range nodes[0].next {
		if child != 0 {
			set[value/64] |= 1 << uint(value%64)
			if value >= 'a' && value <= 'z' {
				upper := value - ('a' - 'A')
				set[upper/64] |= 1 << uint(upper%64)
			} else if value >= 'A' && value <= 'Z' {
				lower := value + ('a' - 'A')
				set[lower/64] |= 1 << uint(lower%64)
			}
		}
	}
	return set
}

func buildAutomaton(literals []hwlm.Literal, folded bool) automaton {
	a := automaton{nodes: []acNode{{}}}
	for _, literal := range literals {
		if len(literal.Value) == 0 || literal.CaseInsensitive != folded {
			continue
		}
		state := 0
		for _, value := range literal.Value {
			if folded {
				value = foldASCII(value)
			}
			next := a.nodes[state].next[value]
			if next == 0 {
				next = len(a.nodes)
				a.nodes[state].next[value] = next
				a.nodes = append(a.nodes, acNode{})
			}
			state = next
		}
		a.nodes[state].output = append(a.nodes[state].output, literal.Clone())
	}
	queue := make([]int, 0)
	for value := 0; value < 256; value++ {
		child := a.nodes[0].next[byte(value)]
		if child != 0 {
			queue = append(queue, child)
			continue
		}
		a.nodes[0].next[byte(value)] = 0
	}
	for len(queue) > 0 {
		state := queue[0]
		queue = queue[1:]
		for value := 0; value < 256; value++ {
			child := a.nodes[state].next[byte(value)]
			if child == 0 {
				a.nodes[state].next[byte(value)] = a.nodes[a.nodes[state].failure].next[byte(value)]
				continue
			}
			failure := a.nodes[a.nodes[state].failure].next[byte(value)]
			a.nodes[child].failure = failure
			a.nodes[child].output = append(a.nodes[child].output, a.nodes[failure].output...)
			queue = append(queue, child)
		}
	}
	return a
}

func foldASCII(value byte) byte {
	if value >= 'A' && value <= 'Z' {
		return value + ('a' - 'A')
	}
	return value
}
