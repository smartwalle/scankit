package scankit

import (
	"bytes"
	"sort"
)

// ReplaceFunc 将一个匹配片段的替换内容写入 buf。matched 是原始输入中与 match 对应的切片。
type ReplaceFunc func(buf *bytes.Buffer, match Match, matched []byte)

// MaskFunc 原地修改一个已命中的片段。value 与调用方传入的数据共享底层数组，
// 长度固定；函数应只修改 value 的内容，不能修改命中片段外的数据。
type MaskFunc func(match Match, value []byte)

// Replace 使用 fn 重组 data 中的匹配片段。重叠片段按起点最早、同起点跨度最长的规则处理。
//
// Replace 会原地重排 matches，并返回不与 data 共享底层数组的新切片。
func Replace(data []byte, matches []Match, fn ReplaceFunc) []byte {
	if len(matches) == 0 {
		return data
	}
	matches = resolveOverlappingMatches(matches)

	// 预分配输入长度，以覆盖替换内容不增长时的常见情况。
	var buf = bytes.NewBuffer(make([]byte, 0, len(data)))
	var cursor = 0
	for _, m := range matches {
		buf.Write(data[cursor:m.From])
		fn(buf, m, data[m.From:m.To])
		cursor = int(m.To)
	}
	buf.Write(data[cursor:])
	return buf.Bytes()
}

// Mask 使用 fn 原地修改 data 中的匹配片段。重叠片段按与 Replace 相同的规则处理。
//
// value 与 data 共享底层数组，长度固定。
// Mask 会原地重排 matches，并返回 data；调用方需要保留原始数据时必须传入副本。
func Mask(data []byte, matches []Match, fn MaskFunc) []byte {
	if len(matches) == 0 {
		return data
	}
	matches = resolveOverlappingMatches(matches)
	for _, match := range matches {
		fn(match, data[match.From:match.To])
	}
	return data
}

// resolveOverlappingMatches 原地排序 matches，并保留优先级最高的不重叠片段。
func resolveOverlappingMatches(matches []Match) []Match {
	sort.Slice(matches, func(i, j int) bool {
		if matches[i].From != matches[j].From {
			return matches[i].From < matches[j].From
		}
		return matches[i].To > matches[j].To
	})

	resolvedMatches := matches[:0]
	for _, m := range matches {
		if len(resolvedMatches) == 0 || m.From >= resolvedMatches[len(resolvedMatches)-1].To {
			resolvedMatches = append(resolvedMatches, m)
		}
	}
	return resolvedMatches
}
