// Package hwlm 提供文字候选提取和通用匹配。
package hwlm

import (
	"bytes"
	"fmt"
	"sort"
	"strconv"

	"github.com/smartwalle/scankit/internal/dispatch"
	"github.com/smartwalle/scankit/internal/parser"
	"github.com/smartwalle/scankit/internal/simd"
)

// Literal 是参与硬件加速匹配的文字候选。
type Literal struct {
	ID              uint32
	Value           []byte
	CaseInsensitive bool
}

// Validate 检查文字编号和内容。
func (l Literal) Validate() error {
	if l.ID == 0 {
		return fmt.Errorf("literal id must be positive")
	}
	if len(l.Value) == 0 {
		return fmt.Errorf("literal must not be empty")
	}
	return nil
}

// Index 是按文字长度分组的候选索引。
type Index struct {
	literals []Literal
	byLength map[int][]Literal
}

// Validate 检查索引中的文字编号和内容唯一性。
func (i *Index) Validate() error {
	if i == nil {
		return fmt.Errorf("nil literal index")
	}
	seen := map[uint32]struct{}{}
	for _, literal := range i.literals {
		if err := literal.Validate(); err != nil {
			return err
		}
		if _, ok := seen[literal.ID]; ok {
			return fmt.Errorf("duplicate literal id %d", literal.ID)
		}
		seen[literal.ID] = struct{}{}
	}
	if i.byLength != nil {
		counts := make(map[int]int, len(i.byLength))
		expected := make(map[string]struct{}, len(i.literals))
		for _, literal := range i.literals {
			expected[literalKey(literal)] = struct{}{}
		}
		for length, literals := range i.byLength {
			if length <= 0 {
				return fmt.Errorf("invalid literal length bucket %d", length)
			}
			for _, literal := range literals {
				if len(literal.Value) != length || literal.ID == 0 {
					return fmt.Errorf("literal length bucket mismatch")
				}
				if _, ok := expected[literalKey(literal)]; !ok {
					return fmt.Errorf("literal length bucket contains unknown entries")
				}
				counts[length]++
			}
		}
		for _, literal := range i.literals {
			counts[len(literal.Value)]--
		}
		for _, count := range counts {
			if count != 0 {
				return fmt.Errorf("literal length bucket contains unknown entries")
			}
		}
	}
	return nil
}

// NewIndex 创建候选索引并复制输入数据。
func NewIndex(literals []Literal) *Index {
	idx := &Index{literals: Deduplicate(literals), byLength: map[int][]Literal{}}
	for _, lit := range idx.literals {
		idx.byLength[len(lit.Value)] = append(idx.byLength[len(lit.Value)], lit.Clone())
	}
	return idx
}

// Literals 返回索引中的文字快照。
func (i *Index) Literals() []Literal {
	if i == nil {
		return nil
	}
	out := make([]Literal, len(i.literals))
	for n, lit := range i.literals {
		out[n] = lit.Clone()
	}
	return out
}

// Clone 创建候选索引的独立副本。
func (i *Index) Clone() *Index {
	if i == nil {
		return nil
	}
	return NewIndex(i.Literals())
}

// Find 返回所有候选文字命中。
func (i *Index) Find(data []byte) []struct {
	ID       uint32
	From, To int
} {
	if i == nil {
		return nil
	}
	var out []struct {
		ID       uint32
		From, To int
	}
	seen := map[[3]int]struct{}{}
	for _, lit := range i.literals {
		for _, off := range FindAll(data, lit) {
			key := [3]int{int(lit.ID), off, off + len(lit.Value)}
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			out = append(out, struct {
				ID       uint32
				From, To int
			}{lit.ID, off, off + len(lit.Value)})
		}
	}
	sort.SliceStable(out, func(a, b int) bool {
		if out[a].From != out[b].From {
			return out[a].From < out[b].From
		}
		if out[a].ID != out[b].ID {
			return out[a].ID < out[b].ID
		}
		return out[a].To < out[b].To
	})
	return out
}

// FindLimit 返回不超过 limit 个候选命中；零值表示不限制。
func (i *Index) FindLimit(data []byte, limit int) []struct {
	ID       uint32
	From, To int
} {
	if limit < 0 || i == nil {
		return nil
	}
	if limit == 0 {
		return i.Find(data)
	}
	out := i.Find(data)
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

// HasID 判断索引是否包含指定文字编号。
func (i *Index) HasID(id uint32) bool {
	if i == nil {
		return false
	}
	for _, literal := range i.literals {
		if literal.ID == id {
			return true
		}
	}
	return false
}

// FindRange 返回完全位于半开区间内的候选命中。
func (i *Index) FindRange(data []byte, from, to int) []struct {
	ID       uint32
	From, To int
} {
	if from < 0 || to < from || to > len(data) {
		return nil
	}
	out := make([]struct {
		ID       uint32
		From, To int
	}, 0)
	seen := make(map[[3]int]struct{})
	for _, lit := range i.literals {
		endStart := to - len(lit.Value)
		if endStart < from {
			continue
		}
		for _, off := range FindAllFrom(data, lit, from) {
			if off > endStart {
				break
			}
			key := [3]int{int(lit.ID), off, off + len(lit.Value)}
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			out = append(out, struct {
				ID       uint32
				From, To int
			}{lit.ID, off, off + len(lit.Value)})
		}
	}
	sort.SliceStable(out, func(a, b int) bool {
		if out[a].From != out[b].From {
			return out[a].From < out[b].From
		}
		if out[a].ID != out[b].ID {
			return out[a].ID < out[b].ID
		}
		return out[a].To < out[b].To
	})
	return out
}

// FindRangeLimit 返回区间内不超过 limit 个候选命中。
func (i *Index) FindRangeLimit(data []byte, from, to, limit int) []struct {
	ID       uint32
	From, To int
} {
	if limit < 0 || from < 0 || to < from || to > len(data) || i == nil {
		return nil
	}
	if limit == 0 {
		return i.FindRange(data, from, to)
	}
	out := make([]struct {
		ID       uint32
		From, To int
	}, 0, limit)
	seen := make(map[[3]int]struct{})
	for _, lit := range i.literals {
		last := to - len(lit.Value)
		if last < from {
			continue
		}
		for _, off := range FindAllFrom(data, lit, from) {
			if off > last {
				break
			}
			key := [3]int{int(lit.ID), off, off + len(lit.Value)}
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			out = append(out, struct {
				ID       uint32
				From, To int
			}{lit.ID, off, off + len(lit.Value)})
		}
	}
	sort.SliceStable(out, func(a, b int) bool {
		if out[a].From != out[b].From {
			return out[a].From < out[b].From
		}
		if out[a].ID != out[b].ID {
			return out[a].ID < out[b].ID
		}
		return out[a].To < out[b].To
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

// FindEndRange 返回结束偏移位于半开区间内的候选命中。
func (i *Index) FindEndRange(data []byte, from, to int) []struct {
	ID       uint32
	From, To int
} {
	if i == nil || from < 0 || to < from || to > len(data) {
		return nil
	}
	out := make([]struct {
		ID       uint32
		From, To int
	}, 0)
	seen := make(map[[3]int]struct{})
	for _, lit := range i.literals {
		start := max(from-len(lit.Value), 0)
		for _, off := range FindAllFrom(data, lit, start) {
			end := off + len(lit.Value)
			if end >= to {
				break
			}
			if end >= from {
				key := [3]int{int(lit.ID), off, end}
				if _, ok := seen[key]; ok {
					continue
				}
				seen[key] = struct{}{}
				out = append(out, struct {
					ID       uint32
					From, To int
				}{lit.ID, off, end})
			}
		}
	}
	sort.SliceStable(out, func(a, b int) bool {
		if out[a].To != out[b].To {
			return out[a].To < out[b].To
		}
		if out[a].From != out[b].From {
			return out[a].From < out[b].From
		}
		return out[a].ID < out[b].ID
	})
	return out
}

// FindEndRangeLimit 返回结束偏移位于半开区间内且不超过 limit 的候选。
func (i *Index) FindEndRangeLimit(data []byte, from, to, limit int) []struct {
	ID       uint32
	From, To int
} {
	if limit < 0 || i == nil {
		return nil
	}
	out := i.FindEndRange(data, from, to)
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

// FindByID 返回指定文字编号的候选命中。
func (i *Index) FindByID(data []byte, id uint32) []struct {
	ID       uint32
	From, To int
} {
	out := i.Find(data)
	filtered := out[:0]
	for _, match := range out {
		if match.ID == id {
			filtered = append(filtered, match)
		}
	}
	return filtered
}

// FindByIDLimit 返回指定文字编号的不超过 limit 个命中。
func (i *Index) FindByIDLimit(data []byte, id uint32, limit int) []struct {
	ID       uint32
	From, To int
} {
	if limit < 0 || i == nil {
		return nil
	}
	if limit == 0 {
		return i.FindByID(data, id)
	}
	out := make([]struct {
		ID       uint32
		From, To int
	}, 0, limit)
	for _, match := range i.Find(data) {
		if match.ID != id {
			continue
		}
		out = append(out, match)
		if len(out) >= limit {
			break
		}
	}
	return out
}

// FindByIDRangeLimit 返回指定文字编号在区间内的有限候选命中。
func (i *Index) FindByIDRangeLimit(data []byte, id uint32, from, to, limit int) []struct {
	ID       uint32
	From, To int
} {
	if i == nil || limit < 0 || from < 0 || to < from || to > len(data) {
		return nil
	}
	out := make([]struct {
		ID       uint32
		From, To int
	}, 0)
	for _, match := range i.FindRange(data, from, to) {
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

// MatchAt 返回指定偏移开始的候选文字命中。
func (i *Index) MatchAt(data []byte, offset int) []struct {
	ID       uint32
	From, To int
} {
	if i == nil || offset < 0 || offset > len(data) {
		return nil
	}
	out := make([]struct {
		ID       uint32
		From, To int
	}, 0)
	for _, lit := range i.literals {
		if offset+len(lit.Value) > len(data) {
			continue
		}
		matched := true
		for n, value := range lit.Value {
			got := data[offset+n]
			if lit.CaseInsensitive {
				got, value = asciiFold(got), asciiFold(value)
			}
			if got != value {
				matched = false
				break
			}
		}
		if matched {
			out = append(out, struct {
				ID       uint32
				From, To int
			}{lit.ID, offset, offset + len(lit.Value)})
		}
	}
	sort.Slice(out, func(a, b int) bool { return out[a].ID < out[b].ID })
	return out
}

func asciiFold(value byte) byte {
	if value >= 'A' && value <= 'Z' {
		return value + ('a' - 'A')
	}
	return value
}

// IDs 返回索引中的文字编号快照。
func (i *Index) IDs() []uint32 {
	if i == nil {
		return nil
	}
	out := make([]uint32, 0, len(i.literals))
	for _, lit := range i.literals {
		out = append(out, lit.ID)
	}
	return out
}

// CountByID 返回指定文字的候选命中数量。
func (i *Index) CountByID(data []byte, id uint32) int { return len(i.FindByID(data, id)) }

// Count 返回全部候选命中数量。
func (i *Index) Count(data []byte) int { return len(i.Find(data)) }

// Clone 返回文字候选副本，并复制字节内容。
func (l Literal) Clone() Literal { l.Value = append([]byte(nil), l.Value...); return l }

// Empty 判断文字候选是否不含字节。
func (l Literal) Empty() bool { return len(l.Value) == 0 }

// Extract 从 AST 中提取稳定的文字候选，优先保留最长连续文字。
func Extract(root parser.Node) []Literal {
	var out []Literal
	var walk func(parser.Node)
	walk = func(n parser.Node) {
		switch v := n.(type) {
		case parser.Literal:
			if len(v.Value) > 0 {
				out = append(out, Literal{Value: append([]byte(nil), v.Value...)})
			}
		case parser.Group:
			walk(v.Child)
		case parser.Sequence:
			var buf []byte
			flush := func() {
				if len(buf) > 0 {
					out = append(out, Literal{Value: append([]byte(nil), buf...)})
					buf = nil
				}
			}
			for _, c := range v.Elements {
				if lit, ok := c.(parser.Literal); ok {
					buf = append(buf, lit.Value...)
				} else {
					flush()
					walk(c)
				}
			}
			flush()
		case parser.Alternation:
			for _, c := range v.Options {
				walk(c)
			}
		case parser.Repeat:
			if v.Min > 0 {
				walk(v.Child)
			}
		case parser.Lookaround:
		// 断言只约束匹配，不产生可消费的候选文字。
		default:
		}
	}
	walk(root)
	for i := range out {
		if out[i].ID == 0 {
			out[i].ID = uint32(i + 1)
		}
	}
	return out
}

// ExtractUnique 提取文字候选并按值及大小写属性去重。
func ExtractUnique(root parser.Node) []Literal { return Deduplicate(Extract(root)) }

// teddyLiteralLimit 是仍交给 Teddy 的文字数量上限；超过后 Teddy 的桶内线性
// 确认成本会超过前缀树的分摊成本。
const teddyLiteralLimit = 128

// 扁平索引的文字边界。
//
// 扁平索引把逐候选确认压缩成"定长主键查找 + 尾部掩码比较"，前提是集合内
// 每条文字都长到足以读出主键。最短文字不足 8 字节时退到 4 字节主键：阈值越低
// 能覆盖的文字集合越多，但候选密度也越高，因此只在没有更短文字时才退让。
const (
	// FlatKeyShort 是 4 字节主键：适用最短文字长度为 4~7 的集合。
	FlatKeyShort = 4
	// FlatKeyLong 是 8 字节主键：适用最短文字长度不小于 8 的集合。
	FlatKeyLong = 8
	// FlatMaxLiteral 是扁平索引支持的文字长度上限，尾部比较在此时仍可用
	// 两次定长读取完成。
	FlatMaxLiteral = 16
)

// FlatKeyWidth 返回文字集合可用的扁平索引主键宽度。
//
// 返回 0 表示集合不适合扁平索引：存在大小写不敏感文字、最短文字不足
// FlatKeyShort，或最长文字超过 FlatMaxLiteral。
func FlatKeyWidth(literals []Literal) int {
	if len(literals) == 0 {
		return 0
	}
	shortest, longest := FlatMaxLiteral+1, 0
	for index := range literals {
		literal := &literals[index]
		if literal.CaseInsensitive {
			return 0
		}
		length := len(literal.Value)
		if length < shortest {
			shortest = length
		}
		if length > longest {
			longest = length
		}
	}
	if shortest < FlatKeyShort || longest > FlatMaxLiteral {
		return 0
	}
	if shortest >= FlatKeyLong {
		return FlatKeyLong
	}
	return FlatKeyShort
}

// Select 按文字集合的特征返回建议的后端名称，空集合返回 none。
func Select(literals []Literal) string {
	if len(literals) == 0 {
		return "none"
	}
	// 只要集合能建扁平索引就交给前缀树：扁平索引把逐候选确认压成一次定长
	// 主键读取加一次掩码比较，而 Teddy 在首字节分桶后仍要逐条线性确认，逐候选
	// 常数明显更大。集合最短文字落在 [4, 8) 时是 4 字节主键（10 MB 语料上
	// 9.2 ms vs Teddy 15.8 ms）；最短文字不少于 8 字节时是 8 字节主键，差距
	// 更大（同一语料上 9.0 ms vs 20.5 ms，且 Teddy 的掩码在这种集合上几乎不
	// 跳位置）。因此两种宽度都优先扁平索引，只有集合无法建索引时才看 Teddy。
	if FlatKeyWidth(literals) == FlatKeyShort {
		return "noodle"
	}
	long := 0
	buckets := make(map[byte]int, 16)
	for _, l := range literals {
		if len(l.Value) > long {
			long = len(l.Value)
		}
		if len(l.Value) == 0 {
			continue
		}
		buckets[l.Value[0]]++
		if l.CaseInsensitive {
			buckets[foldASCIIByte(l.Value[0])]++
		}
	}
	if long >= 8 {
		// Teddy 按首字节分桶后逐条确认，桶过大时单次确认成本随文字数线性增长；
		// 首字节高度集中的文字集合交给前缀树，避免共享前缀退化为全桶比较。
		if len(literals) >= 32 && 2*maxBucket(buckets) >= len(literals) {
			return "noodle"
		}
		// 文字数量很大且首字节分散时 Teddy 的掩码几乎不跳过任何位置，每个触发
		// 位置仍要在所属桶内线性确认，确认成本随文字总数增长；前缀树用压缩共享
		// 前缀分摊这一步，因此数量超过阈值后交给前缀树。
		if len(literals) >= teddyLiteralLimit {
			return "noodle"
		}
		return "teddy"
	}
	if len(literals) <= 4 {
		return "fdr"
	}
	return "noodle"
}

// foldASCIIByte 返回 ASCII 大写字母的小写形式，其余字节保持不变。
func foldASCIIByte(value byte) byte {
	if value >= 'A' && value <= 'Z' {
		return value + 'a' - 'A'
	}
	return value
}

// maxBucket 返回首字节分桶中的最大桶大小。
func maxBucket(buckets map[byte]int) int {
	largest := 0
	for _, count := range buckets {
		if count > largest {
			largest = count
		}
	}
	return largest
}

// SelectionDecision 返回候选匹配器选择及其依据。
type SelectionDecision struct {
	Backend      string
	LiteralCount int
	Longest      int
	Caseless     bool
}

// ExplainSelection 返回稳定的候选匹配器选择信息。
func ExplainSelection(literals []Literal) SelectionDecision {
	d := SelectionDecision{Backend: Select(literals), LiteralCount: len(literals)}
	for _, literal := range literals {
		if len(literal.Value) > d.Longest {
			d.Longest = len(literal.Value)
		}
		d.Caseless = d.Caseless || literal.CaseInsensitive
	}
	return d
}

// Deduplicate 按文字内容和大小写敏感性去重，保持首次出现顺序。
func Deduplicate(in []Literal) []Literal {
	var out []Literal
	seen := map[string]bool{}
	for _, l := range in {
		k := literalKey(l)
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, l.Clone())
	}
	return out
}

func literalKey(l Literal) string {
	return strconv.FormatUint(uint64(l.ID), 10) + ":" + string([]byte{boolByte(l.CaseInsensitive)}) + string(l.Value)
}

func boolByte(v bool) byte {
	if v {
		return 1
	}
	return 0
}

// Longest 返回输入中最长文字的独立副本。
func Longest(in []Literal) Literal {
	var out Literal
	for _, l := range in {
		if len(l.Value) > len(out.Value) {
			out = l
		}
	}
	out.Value = append([]byte(nil), out.Value...)
	return out
}

// LongestLength 返回最长文字的字节长度。
func LongestLength(in []Literal) int { return len(Longest(in).Value) }

// Shortest 返回输入中最短文字的独立副本。
func Shortest(in []Literal) Literal {
	var out Literal
	for _, literal := range in {
		if len(literal.Value) == 0 {
			continue
		}
		if len(out.Value) == 0 || len(literal.Value) < len(out.Value) {
			out = literal
		}
	}
	out.Value = append([]byte(nil), out.Value...)
	return out
}

// TotalBytes 返回所有文字负载的总字节数。
func TotalBytes(in []Literal) int {
	total := 0
	for _, literal := range in {
		if len(literal.Value) > int(^uint(0)>>1)-total {
			return int(^uint(0) >> 1)
		}
		total += len(literal.Value)
	}
	return total
}

// Prefix 返回全部文字的最长公共前缀。
func Prefix(in []Literal) []byte {
	if len(in) == 0 {
		return nil
	}
	p := append([]byte(nil), in[0].Value...)
	for _, l := range in[1:] {
		n := 0
		for n < len(p) && n < len(l.Value) && p[n] == l.Value[n] {
			n++
		}
		p = p[:n]
	}
	return p
}

// PrefixLength 返回全部文字最长公共前缀的字节长度。
func PrefixLength(in []Literal) int { return len(Prefix(in)) }

// PrefixFold 返回忽略 ASCII 大小写后的公共前缀。
func PrefixFold(in []Literal) []byte {
	if len(in) == 0 {
		return nil
	}
	p := append([]byte(nil), in[0].Value...)
	for _, lit := range in[1:] {
		n := 0
		for n < len(p) && n < len(lit.Value) && fold(p[n]) == fold(lit.Value[n]) {
			n++
		}
		p = p[:n]
	}
	return p
}

func fold(c byte) byte {
	if c >= 'A' && c <= 'Z' {
		return c + 32
	}
	return c
}

// FindAll 查找全部文字出现位置，包括重叠命中。
func FindAll(data []byte, literal Literal) []int {
	return FindAllFrom(data, literal, 0)
}

// FindByteRange 返回闭区间内所有字节的位置，使用统一向量掩码并保留尾部扫描。
func FindByteRange(data []byte, lo, hi byte) []int {
	if lo > hi {
		return nil
	}
	// 输入足够大时使用双向量窗口，尾部仍由安全部分加载处理。
	if len(data) >= simd.SuperWidth {
		return FindByteRangeSuper(data, lo, hi)
	}
	out := make([]int, 0, len(data)/4)
	backend := dispatch.DefaultBackend()
	for off := 0; off < len(data); {
		vec, full := backend.Load(data, off)
		if !full {
			for i, b := range data[off:] {
				if b >= lo && b <= hi {
					out = append(out, off+i)
				}
			}
			break
		}
		mask := backend.InRangeMask(vec, lo, hi)
		for bit := range simdWidth {
			if mask&(1<<bit) != 0 {
				out = append(out, off+bit)
			}
		}
		off += simdWidth
	}
	return out
}

// FindByteRangeLimit 查找字节范围命中，并在达到数量上限时立即停止。
// 向量窗口与标量尾部共享同一顺序，适合候选结果预算场景。
func FindByteRangeLimit(data []byte, lo, hi byte, limit int) []int {
	if limit < 0 || lo > hi {
		return nil
	}
	if limit == 0 {
		return FindByteRange(data, lo, hi)
	}
	out := make([]int, 0, limit)
	backend := dispatch.DefaultBackend()
	for off := 0; off < len(data); {
		vec, full := backend.Load(data, off)
		if !full {
			for i, b := range data[off:] {
				if b >= lo && b <= hi {
					out = append(out, off+i)
					if len(out) >= limit {
						return out
					}
				}
			}
			break
		}
		mask := backend.InRangeMask(vec, lo, hi)
		for bit := range simdWidth {
			if mask&(1<<bit) != 0 {
				out = append(out, off+bit)
				if len(out) >= limit {
					return out
				}
			}
		}
		off += simdWidth
	}
	return out
}

// FindByteRangeSuper 使用较宽的可移植向量窗口查找字节范围。
func FindByteRangeSuper(data []byte, lo, hi byte) []int {
	if lo > hi {
		return nil
	}
	out := make([]int, 0, len(data)/4)
	backend := dispatch.DefaultBackend()
	for off := 0; off < len(data); {
		window, ok := simd.LoadSuperWithBackend(backend, data, off)
		if !ok {
			for i, b := range data[off:] {
				if b >= lo && b <= hi {
					out = append(out, off+i)
				}
			}
			break
		}
		lowMask, highMask := window.InRangeMaskWithBackend(backend, lo, hi)
		for bit := range simdWidth {
			if lowMask&(1<<bit) != 0 {
				out = append(out, off+bit)
			}
		}
		for bit := range simdWidth {
			if highMask&(1<<bit) != 0 {
				out = append(out, off+simdWidth+bit)
			}
		}
		off += simd.SuperWidth
	}
	return out
}

// FindAllFrom 从指定偏移开始查找所有文字命中。
func FindAllFrom(data []byte, literal Literal, start int) []int {
	if start < 0 {
		start = 0
	}
	if start > len(data) {
		return nil
	}
	if len(literal.Value) == 0 {
		return nil
	}
	if len(literal.Value) == 1 && !literal.CaseInsensitive {
		positions := findLiteralBytePositions(data[start:], literal.Value[0])
		for i := range positions {
			positions[i] += start
		}
		return positions
	}
	var out []int
	verify := func(off int) bool {
		ok := true
		for i, c := range literal.Value {
			d := data[off+i]
			if literal.CaseInsensitive {
				if c >= 'A' && c <= 'Z' {
					c += 'a' - 'A'
				}
				if d >= 'A' && d <= 'Z' {
					d += 'a' - 'A'
				}
			}
			if c != d {
				ok = false
				break
			}
		}
		return ok
	}
	if start+len(literal.Value) > len(data) {
		return out
	}
	backend := dispatch.DefaultBackend()
	first := literal.Value[0]
	for off := start; off+len(literal.Value) <= len(data); {
		vec, full := backend.Load(data, off)
		if !full {
			if verify(off) {
				out = append(out, off)
			}
			off++
			continue
		}
		mask := backend.EqualByteMask(vec, first)
		if literal.CaseInsensitive {
			mask = backend.EqualByteMaskFold(vec, first)
		}
		if mask == 0 {
			off += simdWidth
			continue
		}
		for bit := range simdWidth {
			if mask&(1<<bit) == 0 {
				continue
			}
			candidate := off + bit
			if candidate+len(literal.Value) <= len(data) && verify(candidate) {
				out = append(out, candidate)
			}
		}
		off += simdWidth
	}
	return out
}

// findLiteralBytePositions 根据输入规模选择单向量或超向量扫描。
// 两条路径输出完全相同的偏移，短输入保留更低的固定开销。
func findLiteralBytePositions(data []byte, value byte) []int {
	if len(data) >= simd.SuperWidth*2 {
		return FindByteRangeSuper(data, value, value)
	}
	return FindByteRange(data, value, value)
}

const simdWidth = 16

// ContainsAt 判断指定偏移是否完整匹配文字。
func ContainsAt(data []byte, off int, literal Literal) bool {
	value := literal.Value
	length := len(value)
	if off < 0 || length == 0 || off+length > len(data) {
		return false
	}
	window := data[off : off+length]
	if !literal.CaseInsensitive {
		// 逐字节循环在长文字上退化成每字节一次比较，而 bytes.Equal 会走
		// 定长加载/向量比较：候选命中密集时这一步是逐候选确认的主要成本。
		return bytes.Equal(window, value)
	}
	for i, c := range value {
		if foldByte(c) != foldByte(window[i]) {
			return false
		}
	}
	return true
}

func foldByte(c byte) byte {
	if c >= 'A' && c <= 'Z' {
		return c + ('a' - 'A')
	}
	return c
}

// FindFirst 返回文字在 data 中最早出现的位置。
func FindFirst(data []byte, literal Literal) (int, bool) {
	all := FindAll(data, literal)
	if len(all) == 0 {
		return 0, false
	}
	return all[0], true
}

// Count 返回文字在 data 中出现的次数，允许重叠。
func Count(data []byte, literal Literal) int { return len(FindAll(data, literal)) }

// Contains 判断文字是否出现在输入中。
func Contains(data []byte, literal Literal) bool {
	if !literal.CaseInsensitive {
		return bytes.Contains(data, literal.Value)
	}
	return len(FindAll(data, literal)) > 0
}
