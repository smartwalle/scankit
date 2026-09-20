// Package prefilter 提供可选的文字候选过滤。
package prefilter

import (
	"bytes"

	"github.com/smartwalle/scankit/internal/graph"
	"github.com/smartwalle/scankit/internal/hwlm"
	"github.com/smartwalle/scankit/internal/nfagraph"
)

type Filter struct{ Literal []byte }

// New 创建不可变文字候选过滤器。
func New(literal []byte) Filter { return Filter{Literal: append([]byte(nil), literal...)} }

// FromGraph 从只有单一路径前缀的图中提取安全候选文字。
func FromGraph(g *nfagraph.Graph) (Filter, bool) {
	if g == nil || g.Flow == nil {
		return Filter{}, false
	}
	literal, ok := graphPrefix(g, g.Start, make(map[graph.Vertex]struct{}))
	if !ok || len(literal) == 0 {
		return Filter{}, false
	}
	return New(literal), true
}

func graphPrefix(g *nfagraph.Graph, id graph.Vertex, visiting map[graph.Vertex]struct{}) ([]byte, bool) {
	if _, ok := visiting[id]; ok {
		return nil, false
	}
	visiting[id] = struct{}{}
	defer delete(visiting, id)
	node, ok := g.Node(id)
	if !ok {
		return nil, false
	}
	var own []byte
	switch node.Kind {
	case nfagraph.KindLiteral:
		own = append(own, node.Literal...)
	case nfagraph.KindStart, nfagraph.KindJoin, nfagraph.KindReport:
	case nfagraph.KindAssertion:
		// 未携带可静态判断信息的断言通常是查找断言，不能忽略后继续提取候选。
		if node.Assertion == 0 {
			return nil, false
		}
	case nfagraph.KindAccept:
		return nil, true
	default:
		return nil, true
	}
	successors := g.Successors(id)
	if len(successors) == 0 {
		return own, true
	}
	paths := make([][]byte, 0, len(successors))
	for _, next := range successors {
		prefix, valid := graphPrefix(g, next, visiting)
		if !valid {
			return nil, false
		}
		paths = append(paths, prefix)
	}
	common := paths[0]
	for _, path := range paths[1:] {
		n := len(common)
		if len(path) < n {
			n = len(path)
		}
		for i := 0; i < n; i++ {
			if common[i] != path[i] {
				n = i
				break
			}
		}
		common = common[:n]
	}
	return append(own, common...), true
}

// MatchAt 判断指定偏移是否为候选文字起点。
func (f Filter) MatchAt(data []byte, offset int) bool {
	if offset < 0 || offset+len(f.Literal) > len(data) {
		return false
	}
	if len(f.Literal) == 0 {
		return offset <= len(data)
	}
	return bytes.Equal(data[offset:offset+len(f.Literal)], f.Literal)
}

// Candidate 判断输入中是否存在候选文字。
func (f Filter) Candidate(data []byte) bool { return len(f.Find(data)) > 0 }

// CandidateFold 判断输入中是否存在忽略 ASCII 大小写的候选文字。
func (f Filter) CandidateFold(data []byte) bool { return len(f.FindFold(data)) > 0 }

// CandidateAny 判断多个过滤器是否至少有一个候选命中。
func CandidateAny(filters []Filter, data []byte) bool {
	for _, filter := range filters {
		if filter.Candidate(data) {
			return true
		}
	}
	return false
}

// CandidateAll 判断多个过滤器是否全部有候选命中。
func CandidateAll(filters []Filter, data []byte) bool {
	for _, filter := range filters {
		if !filter.Candidate(data) {
			return false
		}
	}
	return true
}
func (f Filter) Find(data []byte) []int {
	if len(f.Literal) == 0 {
		return []int{0}
	}
	if len(f.Literal) == 1 {
		return hwlm.FindByteRange(data, f.Literal[0], f.Literal[0])
	}
	return hwlm.FindAll(data, hwlm.Literal{Value: f.Literal})
}

// FindFold 查找不区分 ASCII 大小写的候选起点。
func (f Filter) FindFold(data []byte) []int {
	if len(f.Literal) == 0 {
		return []int{0}
	}
	return hwlm.FindAll(data, hwlm.Literal{Value: f.Literal, CaseInsensitive: true})
}

func foldASCII(c byte) byte {
	if c >= 'A' && c <= 'Z' {
		return c + ('a' - 'A')
	}
	return c
}
func (f Filter) FindFrom(data []byte, start int) []int {
	if start < 0 {
		start = 0
	}
	if len(f.Literal) == 0 {
		if start <= len(data) {
			return []int{start}
		}
		return nil
	}
	if start >= len(data) {
		return nil
	}
	offsets := f.Find(data[start:])
	for i := range offsets {
		offsets[i] += start
	}
	return offsets
}

// FindFoldFrom 从指定偏移查找忽略 ASCII 大小写的候选。
func (f Filter) FindFoldFrom(data []byte, start int) []int {
	if start < 0 {
		start = 0
	}
	if start > len(data) {
		return nil
	}
	offsets := f.FindFold(data[start:])
	for i := range offsets {
		offsets[i] += start
	}
	return offsets
}

// CandidateFrom 判断指定偏移之后是否存在候选文字。
func (f Filter) CandidateFrom(data []byte, start int) bool {
	return len(f.FindFrom(data, start)) > 0
}

// CandidateFoldFrom 判断指定偏移之后是否存在忽略 ASCII 大小写的候选文字。
func (f Filter) CandidateFoldFrom(data []byte, start int) bool {
	return len(f.FindFoldFrom(data, start)) > 0
}
func (f Filter) FindLimit(data []byte, limit int) []int {
	if limit < 0 {
		return nil
	}
	if limit == 0 {
		return f.Find(data)
	}
	if len(f.Literal) == 0 {
		return nil
	}
	if len(f.Literal) == 1 {
		return hwlm.FindByteRangeLimit(data, f.Literal[0], f.Literal[0], limit)
	}
	all := f.Find(data)
	if len(all) > limit {
		all = all[:limit]
	}
	return all
}

// FindFoldLimit 查找忽略 ASCII 大小写的候选起点，并在达到限额时立即返回。
func (f Filter) FindFoldLimit(data []byte, limit int) []int {
	if limit < 0 {
		return nil
	}
	if limit == 0 {
		return f.FindFold(data)
	}
	if len(f.Literal) == 0 {
		return nil
	}
	all := f.FindFold(data)
	if len(all) > limit {
		all = all[:limit]
	}
	return all
}

// FindRange 返回完全位于半开区间内的候选起点。
func (f Filter) FindRange(data []byte, from, to int) []int {
	if from < 0 || to < from || to > len(data) {
		return nil
	}
	out := make([]int, 0)
	for _, offset := range f.FindFrom(data, from) {
		if offset+len(f.Literal) <= to {
			out = append(out, offset)
		}
	}
	return out
}

// FindFoldRange 返回忽略 ASCII 大小写且完全位于区间内的候选起点。
func (f Filter) FindFoldRange(data []byte, from, to int) []int {
	if from < 0 || to < from || to > len(data) {
		return nil
	}
	out := make([]int, 0)
	for _, offset := range f.FindFoldFrom(data, from) {
		if offset+len(f.Literal) <= to {
			out = append(out, offset)
		}
	}
	return out
}

// FindRangeLimit 返回区间内不超过 limit 个候选起点。
func (f Filter) FindRangeLimit(data []byte, from, to, limit int) []int {
	if limit < 0 || from < 0 || to < from || to > len(data) {
		return nil
	}
	out := make([]int, 0)
	for _, off := range f.Find(data) {
		if off < from || off+len(f.Literal) > to {
			continue
		}
		out = append(out, off)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out
}

// FindFoldRangeLimit 返回忽略大小写时区间内的有限候选起点。
func (f Filter) FindFoldRangeLimit(data []byte, from, to, limit int) []int {
	if limit < 0 || from < 0 || to < from || to > len(data) {
		return nil
	}
	out := make([]int, 0)
	for _, off := range f.FindFold(data) {
		if off < from || off+len(f.Literal) > to {
			continue
		}
		out = append(out, off)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out
}

// Count 返回候选命中数量。
func (f Filter) Count(data []byte) int { return len(f.Find(data)) }

// CountFold 返回忽略 ASCII 大小写的候选数量。
func (f Filter) CountFold(data []byte) int { return len(f.FindFold(data)) }
func (f Filter) Empty() bool               { return len(f.Literal) == 0 }

func (f Filter) Len() int      { return len(f.Literal) }
func (f Filter) Clone() Filter { return New(f.Literal) }
