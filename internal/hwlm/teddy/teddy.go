// Package teddy 提供按首字节分桶的多文字候选匹配器。
package teddy

import (
	"encoding/json"
	"fmt"
	"math/bits"
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
// 掩码按最短文字长度扩展为最多 4 个 lane，用连续字节共同筛选候选起点。
type Matcher struct {
	literals []hwlm.Literal
	buckets  [256][]hwlm.Literal
	// laneSets 保存每个 lane 的候选字节集合，lane 0 即首字节集合。
	laneSets [4][4]uint64
	// tables 是 laneSets 的预编译形式，按 lane 连续存放供窗口热路径一次查表。
	tables simd.ByteSetTables
	// lanes 为实际启用的 lane 数，取值 1..4。
	lanes int
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
	shortest := 0
	for _, literal := range m.literals {
		if len(literal.Value) == 0 {
			continue
		}
		if shortest == 0 || len(literal.Value) < shortest {
			shortest = len(literal.Value)
		}
		m.buckets[literal.Value[0]] = append(m.buckets[literal.Value[0]], literal)
		if literal.CaseInsensitive {
			other := swapASCII(literal.Value[0])
			if other != literal.Value[0] {
				m.buckets[other] = append(m.buckets[other], literal)
			}
		}
	}
	m.lanes = shortest
	if m.lanes <= 0 {
		m.lanes = 1
	}
	if m.lanes > len(m.laneSets) {
		m.lanes = len(m.laneSets)
	}
	for _, literal := range m.literals {
		for lane := 0; lane < m.lanes && lane < len(literal.Value); lane++ {
			value := literal.Value[lane]
			m.laneSets[lane][value/64] |= 1 << uint(value%64)
			if !literal.CaseInsensitive {
				continue
			}
			other := swapASCII(value)
			if other != value {
				m.laneSets[lane][other/64] |= 1 << uint(other%64)
			}
		}
	}
	m.tables = simd.NewByteSetTables(m.laneSets)
	return m
}

// Lanes 返回候选掩码实际使用的 lane 数。
func (m *Matcher) Lanes() int {
	if m == nil {
		return 0
	}
	return m.lanes
}

// windowMask 返回窗口内可能成为候选起点的位置掩码。
// 位置 i 的前 lanes 个字节必须分别命中对应 lane 集合；
// 窗口尾部没有足够后继字节的位置退回首字节判定，避免漏报。
// 一次调用覆盖整个超向量窗口，原生与标量实现的组合规则完全一致。
func windowMask(backend simd.Backend, m *Matcher, data []byte, off int) (uint32, bool) {
	return backend.WindowMask(data, off, &m.tables, m.lanes)
}

// Find 返回按起点、编号和终点稳定排序的全部候选命中。
func (m *Matcher) Find(data []byte) []Match {
	return m.FindInto(data, nil)
}

// FindInto 将候选结果写入调用方切片，便于扫描上下文复用结果缓冲。
// 结果按 (起点, 编号, 终点) 稳定排序并去重。
func (m *Matcher) FindInto(data []byte, dst []Match) []Match {
	out := m.FindIntoUnsorted(data, dst)
	if len(out) <= 1 {
		return out
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
	write := 1
	for _, match := range out[1:] {
		if match == out[write-1] {
			continue
		}
		out[write] = match
		write++
	}
	return out[:write]
}

// FindIntoUnsorted 与 FindInto 的候选集合一致，但跳过排序与去重。调用方在
// 派生出按起点排序的确认起点时会重新排序，这里重复排序只放大常数开销。
func (m *Matcher) FindIntoUnsorted(data []byte, dst []Match) []Match {
	if m == nil {
		return dst[:0]
	}
	out := dst[:0]
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
			out = append(out, Match{ID: literal.ID, From: from, To: from + len(literal.Value)})
		}
	}
	// 先用向量掩码筛选可能的首字节，再进入分桶确认，减少稀疏输入上的哈希查找。
	// 窗口宽度优先取整个宽窗口，掩码位 i 对应窗口内偏移 i；剩余不足一个宽窗口时
	// 先用超向量窗口补齐，最后再逐字节回退判定，三段区间互不重叠。
	off := 0
	for ; off+simd.WideWidth <= len(data); off += simd.WideWidth {
		mask, ok := backend.WindowMask64(data, off, &m.tables, m.lanes)
		if !ok {
			break
		}
		for mask != 0 {
			bit := bits.TrailingZeros64(mask)
			visit(off + bit)
			mask &^= 1 << uint(bit)
		}
	}
	if off+simd.SuperWidth <= len(data) {
		if mask, ok := windowMask(backend, m, data, off); ok {
			for mask != 0 {
				bit := bits.TrailingZeros32(mask)
				visit(off + bit)
				mask &^= 1 << uint(bit)
			}
			off += simd.SuperWidth
		}
	}
	for ; off < len(data); off++ {
		if m.laneSets[0][data[off]/64]&(1<<uint(data[off]%64)) != 0 {
			visit(off)
		}
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

// Len 返回匹配器中的文字数量。
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
