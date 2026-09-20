// Package noodle 提供基于前缀树的多文字候选匹配器。
package noodle

import (
	"encoding/json"
	"fmt"
	"sort"

	"github.com/smartwalle/scankit/internal/dispatch"
	"github.com/smartwalle/scankit/internal/hwlm"
	"github.com/smartwalle/scankit/internal/simd"
)

// Match 表示一个候选文字命中。
type Match struct {
	ID       uint32
	From, To int
}

type node struct {
	next     map[byte]*node
	terminal []hwlm.Literal
}

// Matcher 使用两棵前缀树分别处理大小写敏感和不敏感文字。
type Matcher struct {
	sensitive *node
	folded    *node
	literals  []hwlm.Literal
	firstSet  [4]uint64
}

const version = 1

// Dump 序列化文字配置，前缀树会在加载时重建。
func (m *Matcher) Dump() ([]byte, error) {
	if m == nil {
		return nil, fmt.Errorf("nil noodle matcher")
	}
	return json.Marshal(struct {
		Version  int            `json:"version"`
		Literals []hwlm.Literal `json:"literals"`
	}{Version: version, Literals: m.Literals()})
}

// Load 反序列化文字配置并重建前缀树。
func Load(data []byte) (*Matcher, error) {
	var raw struct {
		Version  int            `json:"version"`
		Literals []hwlm.Literal `json:"literals"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	if raw.Version != version {
		return nil, fmt.Errorf("unsupported noodle version %d", raw.Version)
	}
	return New(raw.Literals), nil
}

// New 构建前缀树匹配器。
func New(literals []hwlm.Literal) *Matcher {
	m := &Matcher{sensitive: &node{}, folded: &node{}, literals: hwlm.Deduplicate(literals)}
	for _, literal := range m.literals {
		if len(literal.Value) == 0 {
			continue
		}
		root := m.sensitive
		if literal.CaseInsensitive {
			root = m.folded
		}
		value := literal.Value[0]
		m.firstSet[value/64] |= 1 << uint(value%64)
		if literal.CaseInsensitive {
			value = foldASCII(value)
			m.firstSet[value/64] |= 1 << uint(value%64)
			if swapped := swapASCII(value); swapped != value {
				m.firstSet[swapped/64] |= 1 << uint(swapped%64)
			}
		}
		insert(root, literal)
	}
	return m
}

func insert(root *node, literal hwlm.Literal) {
	current := root
	for _, value := range literal.Value {
		if literal.CaseInsensitive {
			value = foldASCII(value)
		}
		if current.next == nil {
			current.next = make(map[byte]*node)
		}
		if current.next[value] == nil {
			current.next[value] = &node{}
		}
		current = current.next[value]
	}
	current.terminal = append(current.terminal, literal)
}

// Find 返回稳定排序的全部候选命中。
func (m *Matcher) Find(data []byte) []Match {
	return m.FindInto(data, nil)
}

// FindInto 将候选命中追加到调用方提供的切片，减少长输入扫描中的重复分配。
func (m *Matcher) FindInto(data []byte, dst []Match) []Match {
	if m == nil {
		return dst[:0]
	}
	out := dst[:0]
	visit := func(from int) {
		out = appendMatchesAt(out, m.sensitive, data, from, false)
		out = appendMatchesAt(out, m.folded, data, from, true)
	}
	backend := dispatch.DefaultBackend()
	const width = simd.SuperWidth / 2
	for off := 0; off+width <= len(data); off += width {
		vec, ok := backend.Load(data, off)
		if !ok {
			break
		}
		mask := backend.ByteSetMask(vec, m.firstSet)
		for mask != 0 {
			bit := trailingZeros16(mask)
			visit(off + bit)
			mask &^= 1 << uint(bit)
		}
	}
	for off := len(data) - len(data)%width; off < len(data); off++ {
		value := data[off]
		folded := foldASCII(value)
		if m.firstSet[value/64]&(1<<uint(value%64)) != 0 || m.firstSet[folded/64]&(1<<uint(folded%64)) != 0 {
			visit(off)
		}
	}
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
			last := out[write-1]
			if match == last {
				continue
			}
			out[write] = match
			write++
		}
		out = out[:write]
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

func appendMatchesAt(out []Match, root *node, data []byte, from int, folded bool) []Match {
	current := root
	for offset := from; offset < len(data); offset++ {
		value := data[offset]
		if folded {
			value = foldASCII(value)
		}
		if current == nil || current.next == nil {
			break
		}
		current = current.next[value]
		if current == nil {
			break
		}
		out = appendMatches(out, current.terminal, from)
	}
	return out
}

func appendMatches(out []Match, literals []hwlm.Literal, from int) []Match {
	for _, literal := range literals {
		match := Match{ID: literal.ID, From: from, To: from + len(literal.Value)}
		out = append(out, match)
	}
	return out
}

// FindRange 返回完全位于指定半开区间内的候选命中。
func (m *Matcher) FindRange(data []byte, from, to int) []Match {
	if from < 0 || to < from || to > len(data) {
		return nil
	}
	out := make([]Match, 0)
	for _, match := range m.Find(data) {
		if match.From >= from && match.To <= to {
			out = append(out, match)
		}
	}
	return out
}

// FindLimit 返回不超过 limit 个候选命中；零值表示不限制。
func (m *Matcher) FindLimit(data []byte, limit int) []Match {
	if limit < 0 {
		return nil
	}
	out := m.Find(data)
	if limit > 0 && len(out) > limit {
		return out[:limit]
	}
	return out
}

// Literals 返回文字配置副本。
func (m *Matcher) Literals() []hwlm.Literal {
	if m == nil {
		return nil
	}
	out := make([]hwlm.Literal, len(m.literals))
	for i, literal := range m.literals {
		out[i] = literal.Clone()
	}
	return out
}

// Clone 创建独立的匹配器副本。
func (m *Matcher) Clone() *Matcher {
	if m == nil {
		return nil
	}
	return New(m.Literals())
}

func (m *Matcher) Len() int {
	if m == nil {
		return 0
	}
	return len(m.literals)
}

func foldASCII(value byte) byte {
	if value >= 'A' && value <= 'Z' {
		return value + ('a' - 'A')
	}
	return value
}

func swapASCII(value byte) byte {
	if value >= 'a' && value <= 'z' {
		return value - ('a' - 'A')
	}
	if value >= 'A' && value <= 'Z' {
		return value + ('a' - 'A')
	}
	return value
}
