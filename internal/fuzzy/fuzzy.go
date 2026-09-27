// Package fuzzy 提供有界编辑距离和汉明距离判断。
package fuzzy

import "bytes"

// overLimit 返回表示“超过上限”的距离值，并避免上限为整型最大值时溢出。
func overLimit(limit int) int {
	if limit == int(^uint(0)>>1) {
		return limit
	}
	return limit + 1
}

// HammingDistance 返回不同位置数量；长度不一致时返回 -1。
func HammingDistance(a, b []byte) int {
	if len(a) != len(b) {
		return -1
	}
	n := 0
	for i := range a {
		if a[i] != b[i] {
			n++
		}
	}
	return n
}

// WithinHamming 判断等长字节串的差异是否不超过上限。
func WithinHamming(a, b []byte, maxDistance int) bool {
	d := HammingDistance(a, b)
	return d >= 0 && d <= maxDistance
}

// HammingDistanceFoldASCII 返回忽略 ASCII 大小写后的汉明距离。
func HammingDistanceFoldASCII(a, b []byte) int {
	if len(a) != len(b) {
		return -1
	}
	n := 0
	for i := range a {
		if foldASCII(a[i]) != foldASCII(b[i]) {
			n++
		}
	}
	return n
}

// WithinHammingFoldASCII 判断忽略 ASCII 大小写后的汉明距离是否不超过上限。
func WithinHammingFoldASCII(a, b []byte, maxDistance int) bool {
	distance := HammingDistanceFoldASCII(a, b)
	return distance >= 0 && distance <= maxDistance
}

// EditDistance 计算编辑距离，并在超过上限时提前终止。
func EditDistance(a, b []byte, maxDistance int) int {
	if maxDistance < 0 {
		return overLimit(maxDistance)
	}
	if bytes.Equal(a, b) {
		return 0
	}
	if maxDistance == 0 {
		return 1
	}
	delta := len(a) - len(b)
	if delta < 0 {
		delta = -delta
	}
	if delta > maxDistance {
		return overLimit(maxDistance)
	}
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur[0] = i
		rowMin := cur[0]
		for j := 1; j <= len(b); j++ {
			x := prev[j-1]
			if a[i-1] != b[j-1] {
				x++
			}
			if prev[j]+1 < x {
				x = prev[j] + 1
			}
			if cur[j-1]+1 < x {
				x = cur[j-1] + 1
			}
			cur[j] = x
			if x < rowMin {
				rowMin = x
			}
		}
		if rowMin > maxDistance {
			return overLimit(maxDistance)
		}
		prev, cur = cur, prev
	}
	return prev[len(b)]
}

// EditDistanceBanded 使用带宽限制计算编辑距离，适合较小阈值场景。
func EditDistanceBanded(a, b []byte, maxDistance int) int {
	if maxDistance < 0 {
		return overLimit(maxDistance)
	}
	if bytes.Equal(a, b) {
		return 0
	}
	if maxDistance == 0 {
		return 1
	}
	if len(a) > len(b) {
		a, b = b, a
	}
	if len(b)-len(a) > maxDistance {
		return overLimit(maxDistance)
	}
	if maxDistance >= len(b) {
		return EditDistance(a, b, maxDistance)
	}
	const inf = int(^uint(0) >> 1)
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		for j := range cur {
			cur[j] = inf
		}
		lo, hi := i-maxDistance, i+maxDistance
		if lo < 1 {
			lo = 1
		}
		if hi > len(b) {
			hi = len(b)
		}
		if lo == 1 {
			cur[0] = i
		}
		rowMin := inf
		for j := lo; j <= hi; j++ {
			v := prev[j-1]
			if a[i-1] != b[j-1] && v < inf-1 {
				v++
			}
			if prev[j] < inf-1 && prev[j]+1 < v {
				v = prev[j] + 1
			}
			if cur[j-1] < inf-1 && cur[j-1]+1 < v {
				v = cur[j-1] + 1
			}
			cur[j] = v
			if v < rowMin {
				rowMin = v
			}
		}
		if rowMin > maxDistance {
			return overLimit(maxDistance)
		}
		prev, cur = cur, prev
	}
	if prev[len(b)] > maxDistance {
		return overLimit(maxDistance)
	}
	return prev[len(b)]
}

// WithinEdit 判断两个字节串的编辑距离是否不超过 maxDistance。
func WithinEdit(a, b []byte, maxDistance int) bool {
	if maxDistance < 0 {
		return false
	}
	return EditDistanceBanded(a, b, maxDistance) <= maxDistance
}

// EditDistanceFoldASCII 计算忽略 ASCII 大小写后的有界编辑距离。
func EditDistanceFoldASCII(a, b []byte, maxDistance int) int {
	if maxDistance < 0 {
		return overLimit(maxDistance)
	}
	if len(a) == len(b) {
		equal := true
		for i := range a {
			if foldASCII(a[i]) != foldASCII(b[i]) {
				equal = false
				break
			}
		}
		if equal {
			return 0
		}
	}
	if maxDistance == 0 {
		return 1
	}
	delta := len(a) - len(b)
	if delta < 0 {
		delta = -delta
	}
	if delta > maxDistance {
		return overLimit(maxDistance)
	}
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur[0] = i
		rowMin := cur[0]
		for j := 1; j <= len(b); j++ {
			cost := prev[j-1]
			if foldASCII(a[i-1]) != foldASCII(b[j-1]) {
				cost++
			}
			if prev[j]+1 < cost {
				cost = prev[j] + 1
			}
			if cur[j-1]+1 < cost {
				cost = cur[j-1] + 1
			}
			cur[j] = cost
			if cost < rowMin {
				rowMin = cost
			}
		}
		if rowMin > maxDistance {
			return overLimit(maxDistance)
		}
		prev, cur = cur, prev
	}
	return prev[len(b)]
}

// WithinEditFoldASCII 判断忽略 ASCII 大小写后的编辑距离是否不超过上限。
func WithinEditFoldASCII(a, b []byte, maxDistance int) bool {
	if maxDistance < 0 {
		return false
	}
	return editDistanceBandedFoldASCII(a, b, maxDistance) <= maxDistance
}

func editDistanceBandedFoldASCII(a, b []byte, maxDistance int) int {
	if maxDistance < 0 {
		return overLimit(maxDistance)
	}
	if len(a) == len(b) {
		equal := true
		for i := range a {
			if foldASCII(a[i]) != foldASCII(b[i]) {
				equal = false
				break
			}
		}
		if equal {
			return 0
		}
	}
	if maxDistance == 0 {
		return 1
	}
	if len(a) > len(b) {
		a, b = b, a
	}
	if len(b)-len(a) > maxDistance {
		return overLimit(maxDistance)
	}
	if maxDistance >= len(b) {
		return EditDistanceFoldASCII(a, b, maxDistance)
	}
	const inf = int(^uint(0) >> 1)
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		for j := range cur {
			cur[j] = inf
		}
		lo, hi := i-maxDistance, i+maxDistance
		if lo < 1 {
			lo = 1
		}
		if hi > len(b) {
			hi = len(b)
		}
		if lo == 1 {
			cur[0] = i
		}
		rowMin := inf
		for j := lo; j <= hi; j++ {
			v := prev[j-1]
			if foldASCII(a[i-1]) != foldASCII(b[j-1]) && v < inf-1 {
				v++
			}
			if prev[j] < inf-1 && prev[j]+1 < v {
				v = prev[j] + 1
			}
			if cur[j-1] < inf-1 && cur[j-1]+1 < v {
				v = cur[j-1] + 1
			}
			cur[j] = v
			if v < rowMin {
				rowMin = v
			}
		}
		if rowMin > maxDistance {
			return overLimit(maxDistance)
		}
		prev, cur = cur, prev
	}
	if prev[len(b)] > maxDistance {
		return overLimit(maxDistance)
	}
	return prev[len(b)]
}

func foldASCII(value byte) byte {
	if value >= 'A' && value <= 'Z' {
		return value + ('a' - 'A')
	}
	return value
}

// Similar 判断两个字节串是否在 maxDistance 编辑距离内。
func Similar(a, b []byte, maxDistance int) bool { return WithinEdit(a, b, maxDistance) }

// Equal 判断两个字节串是否逐字节相等。
func Equal(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// EditDistanceLimited 返回编辑距离，并判断是否不超过 maxDistance。
func EditDistanceLimited(a, b []byte, maxDistance int) (int, bool) {
	// 受限接口直接使用带宽动态规划，避免在小阈值下遍历无关矩阵。
	d := EditDistanceBanded(a, b, maxDistance)
	return d, d <= maxDistance
}

// WithinDistance 在 maxDistance 非负时判断编辑距离是否在范围内。
func WithinDistance(a, b []byte, maxDistance int) bool {
	return maxDistance >= 0 && WithinEdit(a, b, maxDistance)
}

// DistanceRatio 返回按较长串长度归一的相似度，取值区间为 [0, 1]。
func DistanceRatio(a, b []byte) float64 {
	if len(a) == 0 && len(b) == 0 {
		return 1
	}
	d := EditDistance(a, b, maxDistanceForLengths(len(a), len(b)))
	m := max(len(b), len(a))
	return 1 - float64(d)/float64(m)
}

// MaxDistance 返回两个字节串的精确编辑距离。
func MaxDistance(a, b []byte) int { return EditDistance(a, b, maxDistanceForLengths(len(a), len(b))) }

func maxDistanceForLengths(a, b int) int {
	maxInt := int(^uint(0) >> 1)
	if a > maxInt-b {
		return maxInt
	}
	return a + b
}

// Similarity 返回 0 到 1 之间的编辑相似度。
func Similarity(a, b []byte) float64 { return DistanceRatio(a, b) }

// WithinRatio 判断相似度是否达到阈值。
func WithinRatio(a, b []byte, threshold float64) bool { return DistanceRatio(a, b) >= threshold }

// ValidRatio 判断相似度阈值是否位于闭区间 [0,1]。
func ValidRatio(threshold float64) bool { return threshold >= 0 && threshold <= 1 }

// BestPrefix 返回与目标具有最小编辑距离的候选前缀长度和距离。
func BestPrefix(pattern, data []byte, maxDistance int) (length, distance int, ok bool) {
	if maxDistance < 0 {
		return 0, 0, false
	}
	// 按数据前缀逐列推进，避免对每个前缀重复构造完整 DP 矩阵。
	prev := make([]int, len(pattern)+1)
	cur := make([]int, len(pattern)+1)
	for j := range prev {
		prev[j] = j
	}
	best, bestLength := prev[len(pattern)], 0
	for i, value := range data {
		cur[0] = i + 1
		for j := 1; j <= len(pattern); j++ {
			v := prev[j-1]
			if pattern[j-1] != value {
				v++
			}
			if prev[j]+1 < v {
				v = prev[j] + 1
			}
			if cur[j-1]+1 < v {
				v = cur[j-1] + 1
			}
			cur[j] = v
		}
		if cur[len(pattern)] < best {
			best, bestLength = cur[len(pattern)], i+1
		}
		prev, cur = cur, prev
	}
	return bestLength, best, best <= maxDistance
}

// BestPrefixFoldASCII 返回忽略 ASCII 大小写时最优前缀。
func BestPrefixFoldASCII(pattern, data []byte, maxDistance int) (length, distance int, ok bool) {
	if maxDistance < 0 {
		return 0, 0, false
	}
	prev := make([]int, len(pattern)+1)
	cur := make([]int, len(pattern)+1)
	for j := range prev {
		prev[j] = j
	}
	best, bestLength := prev[len(pattern)], 0
	for i, value := range data {
		cur[0] = i + 1
		for j := 1; j <= len(pattern); j++ {
			v := prev[j-1]
			if foldASCII(pattern[j-1]) != foldASCII(value) {
				v++
			}
			if prev[j]+1 < v {
				v = prev[j] + 1
			}
			if cur[j-1]+1 < v {
				v = cur[j-1] + 1
			}
			cur[j] = v
		}
		if cur[len(pattern)] < best {
			best, bestLength = cur[len(pattern)], i+1
		}
		prev, cur = cur, prev
	}
	return bestLength, best, best <= maxDistance
}

// BestPrefixRange 在候选前缀长度范围内查找最小编辑距离。
func BestPrefixRange(pattern, data []byte, minLen, maxLen, maxDistance int) (length, distance int, ok bool) {
	if minLen < 0 || maxLen < minLen || maxLen > len(data) || maxDistance < 0 {
		return 0, 0, false
	}
	prev := make([]int, len(pattern)+1)
	cur := make([]int, len(pattern)+1)
	for j := range prev {
		prev[j] = j
	}
	best, bestLen := int(^uint(0)>>1), 0
	if minLen == 0 {
		best = len(pattern)
	}
	for i, value := range data[:maxLen] {
		cur[0] = i + 1
		for j := 1; j <= len(pattern); j++ {
			v := prev[j-1]
			if pattern[j-1] != value {
				v++
			}
			if prev[j]+1 < v {
				v = prev[j] + 1
			}
			if cur[j-1]+1 < v {
				v = cur[j-1] + 1
			}
			cur[j] = v
		}
		length := i + 1
		if length >= minLen && cur[len(pattern)] < best {
			best, bestLen = cur[len(pattern)], length
		}
		prev, cur = cur, prev
	}
	if best == int(^uint(0)>>1) {
		return 0, 0, false
	}
	return bestLen, best, best <= maxDistance
}

// BestPrefixFoldASCIIRange 是大小写折叠版本的前缀范围搜索。
func BestPrefixFoldASCIIRange(pattern, data []byte, minLen, maxLen, maxDistance int) (length, distance int, ok bool) {
	if minLen < 0 || maxLen < minLen || maxLen > len(data) || maxDistance < 0 {
		return 0, 0, false
	}
	prev := make([]int, len(pattern)+1)
	cur := make([]int, len(pattern)+1)
	for j := range prev {
		prev[j] = j
	}
	best, bestLen := int(^uint(0)>>1), 0
	if minLen == 0 {
		best = len(pattern)
	}
	for i, value := range data[:maxLen] {
		cur[0] = i + 1
		for j := 1; j <= len(pattern); j++ {
			v := prev[j-1]
			if foldASCII(pattern[j-1]) != foldASCII(value) {
				v++
			}
			if prev[j]+1 < v {
				v = prev[j] + 1
			}
			if cur[j-1]+1 < v {
				v = cur[j-1] + 1
			}
			cur[j] = v
		}
		length := i + 1
		if length >= minLen && cur[len(pattern)] < best {
			best, bestLen = cur[len(pattern)], length
		}
		prev, cur = cur, prev
	}
	if best == int(^uint(0)>>1) {
		return 0, 0, false
	}
	return bestLen, best, best <= maxDistance
}

// BestHamming 返回目标等长窗口中最小汉明距离及起点。
func BestHamming(pattern, data []byte) (offset, distance int, ok bool) {
	if len(pattern) == 0 || len(pattern) > len(data) {
		return 0, 0, false
	}
	best := len(pattern) + 1
	bestOffset := 0
	for start := 0; start+len(pattern) <= len(data); start++ {
		d := HammingDistance(pattern, data[start:start+len(pattern)])
		if d < best {
			best, bestOffset = d, start
		}
	}
	return bestOffset, best, true
}

// BestHammingFoldASCII 返回忽略 ASCII 大小写时最优窗口。
func BestHammingFoldASCII(pattern, data []byte) (offset, distance int, ok bool) {
	if len(pattern) == 0 || len(pattern) > len(data) {
		return 0, 0, false
	}
	best, bestOffset := len(pattern)+1, 0
	for start := 0; start+len(pattern) <= len(data); start++ {
		d := HammingDistanceFoldASCII(pattern, data[start:start+len(pattern)])
		if d < best {
			best, bestOffset = d, start
		}
	}
	return bestOffset, best, true
}

// FindWithinHamming 返回所有汉明距离不超过上限的窗口起点。
func FindWithinHamming(pattern, data []byte, maxDistance int) []int {
	if maxDistance < 0 || len(pattern) == 0 || len(pattern) > len(data) {
		return nil
	}
	out := make([]int, 0)
	for off := 0; off+len(pattern) <= len(data); off++ {
		if WithinHamming(pattern, data[off:off+len(pattern)], maxDistance) {
			out = append(out, off)
		}
	}
	return out
}

// FindWithinEditPrefix 返回与模式编辑距离受限的所有数据前缀长度。
func FindWithinEditPrefix(pattern, data []byte, maxDistance int) []int {
	if maxDistance < 0 {
		return nil
	}
	// 沿数据前缀逐列更新 DP，避免对每个前缀重复构造完整矩阵。
	prev := make([]int, len(pattern)+1)
	cur := make([]int, len(pattern)+1)
	for j := range prev {
		prev[j] = j
	}
	out := make([]int, 0)
	if prev[len(pattern)] <= maxDistance {
		out = append(out, 0)
	}
	for i, value := range data {
		cur[0] = i + 1
		for j := 1; j <= len(pattern); j++ {
			cost := 0
			if pattern[j-1] != value {
				cost = 1
			}
			v := min(cur[j-1]+1, min(prev[j]+1, prev[j-1]+cost))
			cur[j] = v
		}
		if cur[len(pattern)] <= maxDistance {
			out = append(out, i+1)
		}
		prev, cur = cur, prev
	}
	return out
}

// EditDistanceInto 使用调用方提供的工作缓冲计算编辑距离。
func EditDistanceInto(a, b []byte, maxDistance int, scratch []int) (int, bool) {
	if maxDistance < 0 {
		return overLimit(maxDistance), false
	}
	if len(scratch) < len(b)+1 {
		return EditDistance(a, b, maxDistance), false
	}
	for j := 0; j <= len(b); j++ {
		scratch[j] = j
	}
	for i := 1; i <= len(a); i++ {
		prevDiag := scratch[0]
		scratch[0] = i
		rowMin := scratch[0]
		for j := 1; j <= len(b); j++ {
			old := scratch[j]
			cost := prevDiag
			if a[i-1] != b[j-1] {
				cost++
			}
			if scratch[j]+1 < cost {
				cost = scratch[j] + 1
			}
			if scratch[j-1]+1 < cost {
				cost = scratch[j-1] + 1
			}
			scratch[j] = cost
			prevDiag = old
			if cost < rowMin {
				rowMin = cost
			}
		}
		if rowMin > maxDistance {
			return overLimit(maxDistance), true
		}
	}
	return scratch[len(b)], true
}
