// Package teddy 提供按首字节分桶的多文字候选匹配器。
package teddy

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

// Matcher 使用首字节桶降低每个输入偏移的候选比较数量。
type Matcher struct {
	literals []hwlm.Literal
	buckets  [256][]hwlm.Literal
	firstSet [4]uint64
}

const version = 1

// Dump 序列化文字配置，分桶表会在加载时重建。
func (m *Matcher) Dump() ([]byte, error) {
	if m == nil {
		return nil, fmt.Errorf("nil teddy matcher")
	}
	return json.Marshal(struct {
		Version  int            `json:"version"`
		Literals []hwlm.Literal `json:"literals"`
	}{Version: version, Literals: m.Literals()})
}

// Load 反序列化文字配置并重建分桶表。
func Load(data []byte) (*Matcher, error) {
	var raw struct {
		Version  int            `json:"version"`
		Literals []hwlm.Literal `json:"literals"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	if raw.Version != version {
		return nil, fmt.Errorf("unsupported teddy version %d", raw.Version)
	}
	return New(raw.Literals), nil
}

// New 构建独立的候选分桶表。
func New(literals []hwlm.Literal) *Matcher {
	m := &Matcher{literals: hwlm.Deduplicate(literals)}
	for _, literal := range m.literals {
		if len(literal.Value) == 0 {
			continue
		}
		m.buckets[literal.Value[0]] = append(m.buckets[literal.Value[0]], literal)
		m.firstSet[literal.Value[0]/64] |= 1 << uint(literal.Value[0]%64)
		if literal.CaseInsensitive {
			other := swapASCII(literal.Value[0])
			if other != literal.Value[0] {
				m.buckets[other] = append(m.buckets[other], literal)
				m.firstSet[other/64] |= 1 << uint(other%64)
			}
		}
	}
	return m
}

// Find 返回按起点、编号和终点稳定排序的全部候选命中。
func (m *Matcher) Find(data []byte) []Match {
	return m.FindInto(data, nil)
}

// FindInto 将候选结果写入调用方切片，便于扫描上下文复用结果缓冲。
func (m *Matcher) FindInto(data []byte, dst []Match) []Match {
	if m == nil {
		return dst[:0]
	}
	out := dst[:0]
	seen := make(map[[3]int]struct{})
	backend := dispatch.DefaultBackend()
	visit := func(from int) {
		if from < 0 || from >= len(data) {
			return
		}
		value := data[from]
		for _, literal := range m.buckets[value] {
			if !hwlm.ContainsAt(data, from, literal) {
				continue
			}
			match := Match{ID: literal.ID, From: from, To: from + len(literal.Value)}
			key := [3]int{int(match.ID), match.From, match.To}
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			out = append(out, match)
		}
	}
	// 先用向量掩码筛选可能的首字节，再进入分桶确认，减少稀疏输入上的哈希查找。
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
		if m.firstSet[data[off]/64]&(1<<uint(data[off]%64)) != 0 {
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

func swapASCII(value byte) byte {
	if value >= 'a' && value <= 'z' {
		return value - ('a' - 'A')
	}
	if value >= 'A' && value <= 'Z' {
		return value + ('a' - 'A')
	}
	return value
}
