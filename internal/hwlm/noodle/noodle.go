// Package noodle 提供基于前缀树的多文字候选匹配器。
package noodle

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math/bits"
	"slices"
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

// node 是冻结后的前缀树节点。子节点按字节序压缩存放：mask 标记存在哪些
// 子字节，kids 与置位顺序一一对应，查询用位图判断存在性并用 popcount 定位。
// 相比逐节点 map，热路径的每一步都退化为常数级位运算，避免命中密集时
// 每个候选起点都要付出多次哈希查找。
type node struct {
	// label 是父节点分派字节之后、到达本节点前必须匹配的压缩字节串。
	// 冻结期把没有分叉也没有终结点的单子节点链合并成一条边，命中密集时
	// 每个候选只需少量整段比较，而不是逐节点走完共享前缀。
	label []byte
	// labelWord/labelLen 是 label 的定长视图：长度不超过 8 时把字节按
	// 小端顺序打包进 labelWord，热路径用一次对齐读取替换 bytes.Equal
	// 的函数调用链，命中密集时这一步是候选确认的主要常数开销。
	labelWord uint64
	labelLen  int
	mask      [4]uint64
	// base 保存各 64 位分组之前的子节点数量前缀和，冻结时算好，查询时
	// 只需一次 popcount 即可定位子节点下标。
	base     [4]int32
	kids     []*node
	terminal []hwlm.Literal
}

// child 返回该字节对应的子节点，不存在时返回 nil。
func (n *node) child(value byte) *node {
	if n == nil {
		return nil
	}
	group := value >> 6
	bit := uint64(1) << (value & 63)
	if n.mask[group]&bit == 0 {
		return nil
	}
	index := n.base[group] + int32(bits.OnesCount64(n.mask[group]&(bit-1)))
	return n.kids[index]
}

// children 判断节点是否存在子节点，用于跳过空前缀树。
func (n *node) children() bool {
	if n == nil {
		return false
	}
	return n.mask[0]|n.mask[1]|n.mask[2]|n.mask[3] != 0
}

// builder 是构建期的可变节点，完成插入后再冻结为查询用布局。
type builder struct {
	next     map[byte]*builder
	terminal []hwlm.Literal
}

// freeze 将构建期节点转换为压缩位图布局，并合并单子节点链。子节点按
// 字节升序排列，与 mask 中置位的顺序一致；label 承接合并链上的后继字节。
func freeze(b *builder, label []byte) *node {
	if b == nil {
		return nil
	}
	keys := make([]byte, 0, len(b.next))
	for key := range b.next {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	n := &node{label: label, labelLen: len(label), terminal: b.terminal}
	if len(label) > 0 && len(label) <= 8 {
		for index, value := range label {
			n.labelWord |= uint64(value) << uint(8*index)
		}
	}
	if len(keys) == 0 {
		return n
	}
	n.kids = make([]*node, len(keys))
	for index, key := range keys {
		n.mask[key>>6] |= 1 << (key & 63)
		child := b.next[key]
		chain := []byte(nil)
		// 只合并既无终结点也无分叉的中间节点：出现终结点说明存在更短
		// 文字必须单独命中，出现分叉说明该节点需要按字节分派。
		for len(child.terminal) == 0 && len(child.next) == 1 {
			for next := range child.next {
				chain = append(chain, next)
				child = child.next[next]
			}
		}
		n.kids[index] = freeze(child, chain)
	}
	for group := 1; group < len(n.base); group++ {
		n.base[group] = n.base[group-1] + int32(bits.OnesCount64(n.mask[group-1]))
	}
	return n
}

// Matcher 使用两棵前缀树分别处理大小写敏感和不敏感文字，
// 并用最多 4 个 lane 的字节掩码先在窗口级别筛选候选起点。
type Matcher struct {
	sensitive *node
	folded    *node
	literals  []hwlm.Literal
	// laneSets 保存每个 lane 允许的候选字节集合，lane 0 即首字节集合。
	laneSets [4][4]uint64
	// tables 是 laneSets 的预编译形式，按 lane 连续存放供窗口热路径一次查表。
	tables simd.ByteSetTables
	// lanes 为实际启用的 lane 数，取值 1..4。
	lanes int
	// flat 在文字集合满足定长比较条件时给出扁平索引，热路径据此跳过
	// 压缩前缀树的逐节点解引用；不满足条件时为 nil。
	flat *flatTable
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
	builderSensitive := &builder{}
	builderFolded := &builder{}
	m := &Matcher{literals: hwlm.Deduplicate(literals)}
	m.flat = newFlatTable(m.literals)
	shortest := 0
	for _, literal := range m.literals {
		if len(literal.Value) == 0 {
			continue
		}
		if shortest == 0 || len(literal.Value) < shortest {
			shortest = len(literal.Value)
		}
		root := builderSensitive
		if literal.CaseInsensitive {
			root = builderFolded
		}
		insert(root, literal)
	}
	// lane 数取最短文字长度，上限为 laneSets 的容量：lane 越多，窗口掩码
	// 的过滤越强，但每个窗口的原生查表次数也随之增加。
	m.lanes = shortest
	if m.lanes <= 0 {
		m.lanes = 1
	}
	if m.lanes > len(m.laneSets) {
		m.lanes = len(m.laneSets)
	}
	// 8 字节主键下候选确认已经退化成一次主键读取，多 lane 掩码挡下的位置
	// 本来也只会付一次哈希查找，省下的确认次数不足以抵消每窗口成倍的查表
	// 开销，因此退回单 lane 首字节掩码；4 字节主键的桶更长、候选密度更高，
	// 保留多 lane 掩码更划算（实测 10 MB 语料 4 lane 17.7 ms vs 1 lane 27.2 ms）。
	if m.flat != nil && m.flat.keyBytes >= hwlm.FlatKeyLong {
		m.lanes = 1
	}
	for _, literal := range m.literals {
		for lane := 0; lane < m.lanes && lane < len(literal.Value); lane++ {
			value := literal.Value[lane]
			m.laneSets[lane][value/64] |= 1 << uint(value%64)
			if !literal.CaseInsensitive {
				continue
			}
			if swapped := swapASCII(value); swapped != value {
				m.laneSets[lane][swapped/64] |= 1 << uint(swapped%64)
			}
		}
	}
	m.tables = simd.NewByteSetTables(m.laneSets)
	m.sensitive = freeze(builderSensitive, nil)
	m.folded = freeze(builderFolded, nil)
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

func insert(root *builder, literal hwlm.Literal) {
	current := root
	for _, value := range literal.Value {
		if literal.CaseInsensitive {
			value = foldASCII(value)
		}
		if current.next == nil {
			current.next = make(map[byte]*builder)
		}
		child := current.next[value]
		if child == nil {
			child = &builder{}
			current.next[value] = child
		}
		current = child
	}
	current.terminal = append(current.terminal, literal)
}

// Find 返回稳定排序的全部候选命中。
func (m *Matcher) Find(data []byte) []Match {
	return m.FindInto(data, nil)
}

// FindInto 将候选命中追加到调用方提供的切片，减少长输入扫描中的重复分配。
// 结果按 (起点, 编号, 终点) 稳定排序并去重。
func (m *Matcher) FindInto(data []byte, dst []Match) []Match {
	out := m.FindIntoUnsorted(data, dst)
	if len(out) <= 1 {
		return out
	}
	slices.SortStableFunc(out, func(a, b Match) int {
		if a.From != b.From {
			return a.From - b.From
		}
		if a.ID != b.ID {
			return int(a.ID) - int(b.ID)
		}
		return a.To - b.To
	})
	write := 1
	for _, match := range out[1:] {
		last := out[write-1]
		if match == last {
			continue
		}
		out[write] = match
		write++
	}
	return out[:write]
}

// FindIntoUnsorted 与 FindInto 的候选集合完全一致，但跳过排序与去重。
// 调用方在派生出按起点排序的确认起点时会重新排序，命中密集的语料上
// 重复排序只会放大常数开销，因此提供该入口复用同一份扫描逻辑。
func (m *Matcher) FindIntoUnsorted(data []byte, dst []Match) []Match {
	if m == nil {
		return dst[:0]
	}
	out := dst[:0]
	backend := dispatch.DefaultBackend()
	if m.flat != nil {
		// 扁平索引下候选确认已经退化成一次定长主键比较，这里直接展开三段扫描，
		// 省去每条命中都要经过的闭包间接调用与 nil 判定。
		return m.flat.findInto(out, data, backend, &m.tables, &m.laneSets[0], m.lanes)
	}
	folded := m.folded.children()
	visit := func(from int) {
		out = appendMatchesAt(out, m.sensitive, data, from, false)
		if folded {
			out = appendMatchesAt(out, m.folded, data, from, true)
		}
	}
	// 窗口宽度优先取整个宽窗口，剩余不足一个宽窗口时先用超向量窗口补齐，
	// 最后再逐字节回退判定，三段区间互不重叠，保证每个偏移只判定一次。
	off := 0
	for ; off+simd.WideWidth <= len(data); off += simd.WideWidth {
		mask, ok := backend.WindowMask64(data, off, &m.tables, m.lanes)
		if !ok {
			break
		}
		for mask != 0 {
			visit(off + bits.TrailingZeros64(mask))
			mask &= mask - 1
		}
	}
	if off+simd.SuperWidth <= len(data) {
		if mask, ok := windowMask(backend, m, data, off); ok {
			for mask != 0 {
				visit(off + bits.TrailingZeros32(mask))
				mask &= mask - 1
			}
			off += simd.SuperWidth
		}
	}
	for ; off < len(data); off++ {
		value := data[off]
		folded := foldASCII(value)
		first := m.laneSets[0]
		if first[value/64]&(1<<uint(value%64)) != 0 || first[folded/64]&(1<<uint(folded%64)) != 0 {
			visit(off)
		}
	}
	return out
}

func appendMatchesAt(out []Match, root *node, data []byte, from int, folded bool) []Match {
	current := root
	pos := from
	for pos < len(data) {
		value := data[pos]
		if folded {
			value = foldASCII(value)
		}
		current = current.child(value)
		if current == nil {
			break
		}
		pos++
		if length := current.labelLen; length != 0 {
			if pos+length > len(data) {
				break
			}
			// 大小写敏感且长度不超过 8 的标签用一次定长读取替代
			// bytes.Equal 的调用链，避免命中密集时的函数调用开销。
			if !folded && length <= 8 && pos+8 <= len(data) {
				if binary.LittleEndian.Uint64(data[pos:pos+8])&labelMask(length) != current.labelWord {
					break
				}
			} else if !matchNodeLabel(data[pos:pos+length], current.label, folded) {
				break
			}
			pos += length
		}
		out = appendMatches(out, current.terminal, from)
	}
	return out
}

// matchNodeLabel 比较压缩边标签。大小写折叠树上的标签已折叠，窗口字节
// 需要逐字节折叠后再比较；大小写敏感且长度不超过 8 的标签走一次定长读取，
// 避免为每个候选付出 bytes.Equal 的调用链开销。函数体保持精简以便被
// appendMatchesAt 内联。
func matchNodeLabel(window, label []byte, folded bool) bool {
	if !folded {
		return bytes.Equal(window, label)
	}
	for index, value := range label {
		if foldASCII(window[index]) != value {
			return false
		}
	}
	return true
}

// labelMask 返回长度不超过 8 的标签在定长比较中使用的低位掩码。
func labelMask(length int) uint64 {
	if length >= 8 {
		return ^uint64(0)
	}
	return uint64(1)<<uint(8*length) - 1
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
