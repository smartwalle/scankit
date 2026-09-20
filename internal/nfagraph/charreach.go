package nfagraph

import (
	"sort"

	"github.com/smartwalle/scankit/internal/parser"
)

// CharReach 是 256 字节字符集合的固定大小表示。
type CharReach struct{ words [4]uint64 }

// CharReachFromClass 将规范化字符类转换为位集合。
func CharReachFromClass(class parser.Class) CharReach {
	var out CharReach
	for _, r := range class.Ranges {
		for c := int(r.Lo); c <= int(r.Hi); c++ {
			out.words[c>>6] |= uint64(1) << uint(c&63)
		}
	}
	if class.Negated {
		out = out.Complement()
	}
	return out
}

// Contains 判断字节是否在集合中。
func (r CharReach) Contains(value byte) bool {
	return r.words[value>>6]&(uint64(1)<<uint(value&63)) != 0
}

// Union 将另一个集合并入当前集合。
func (r *CharReach) Union(other CharReach) {
	if r == nil {
		return
	}
	for i := range r.words {
		r.words[i] |= other.words[i]
	}
}

// Intersect 保留与另一个集合的交集。
func (r *CharReach) Intersect(other CharReach) {
	if r == nil {
		return
	}
	for i := range r.words {
		r.words[i] &= other.words[i]
	}
}

// Difference 删除另一个集合包含的字符。
func (r *CharReach) Difference(other CharReach) {
	if r == nil {
		return
	}
	for i := range r.words {
		r.words[i] &^= other.words[i]
	}
}

// Complement 返回相对于全部字节的补集。
func (r CharReach) Complement() CharReach {
	for i := range r.words {
		r.words[i] = ^r.words[i]
	}
	return r
}

// Empty 判断集合是否为空。
func (r CharReach) Empty() bool {
	return r.words[0] == 0 && r.words[1] == 0 && r.words[2] == 0 && r.words[3] == 0
}

// Count 返回集合中的字符数量。
func (r CharReach) Count() int {
	n := 0
	for _, word := range r.words {
		for word != 0 {
			word &= word - 1
			n++
		}
	}
	return n
}

// ToClass 将位集合转换为排序且合并后的字符类。
func (r CharReach) ToClass() parser.Class {
	ranges := make([]parser.Range, 0, r.Count())
	for c := 0; c < 256; {
		if !r.Contains(byte(c)) {
			c++
			continue
		}
		start := c
		for c+1 < 256 && r.Contains(byte(c+1)) {
			c++
		}
		ranges = append(ranges, parser.Range{Lo: byte(start), Hi: byte(c)})
		c++
	}
	return parser.Class{Ranges: ranges}
}

// Ranges 返回稳定排序的非空区间。
func (r CharReach) Ranges() []parser.Range {
	class := r.ToClass()
	sort.Slice(class.Ranges, func(i, j int) bool { return class.Ranges[i].Lo < class.Ranges[j].Lo })
	return class.Ranges
}
