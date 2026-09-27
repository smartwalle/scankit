// Package repeat 提供纯文字重复规则的独立编译与执行路径。
package repeat

import (
	"bytes"
	"encoding/json"
	"fmt"
)

const version = 1

// Program 是一个不可变的重复文字执行程序。Max 为负数表示无上限。
type Program struct {
	Unit   []byte `json:"unit"`
	Min    int    `json:"min"`
	Max    int    `json:"max"`
	Greedy bool   `json:"greedy"`
}

// Width 返回单次重复单元的字节宽度。
func (p *Program) Width() int {
	if p == nil {
		return 0
	}
	return len(p.Unit)
}

// Bounds 返回重复次数上下界，无上限时 maxCount 为 -1。
func (p *Program) Bounds() (minCount, maxCount int) {
	if p == nil {
		return 0, 0
	}
	return p.Min, p.Max
}

// CanMatchEmpty 判断程序是否允许零长度命中。
func (p *Program) CanMatchEmpty() bool { return p != nil && p.Min == 0 }

// New 构造纯文字重复程序。
func New(unit []byte, minCount, maxCount int, greedy bool) (*Program, error) {
	p := &Program{Unit: append([]byte(nil), unit...), Min: minCount, Max: maxCount, Greedy: greedy}
	if err := p.Validate(); err != nil {
		return nil, err
	}
	return p, nil
}

// Validate 校验重复边界和文字单元。
func (p *Program) Validate() error {
	if p == nil || len(p.Unit) == 0 {
		return fmt.Errorf("invalid repeat unit")
	}
	if len(p.Unit) > 1<<20 || p.Min < 0 || p.Min > 1<<20 || p.Max < -1 || p.Max >= 0 && (p.Max < p.Min || p.Max > 1<<20) {
		return fmt.Errorf("invalid repeat bounds")
	}
	return nil
}

// MinBytes 返回最小匹配字节数。
func (p *Program) MinBytes() int {
	if p == nil {
		return 0
	}
	return saturatingProduct(len(p.Unit), p.Min)
}

// MaxBytes 返回最大匹配字节数；无上限时返回 -1。
func (p *Program) MaxBytes() int {
	if p == nil || p.Max < 0 {
		return -1
	}
	return saturatingProduct(len(p.Unit), p.Max)
}

func saturatingProduct(a, b int) int {
	if a <= 0 || b <= 0 {
		return 0
	}
	maxInt := int(^uint(0) >> 1)
	if a > maxInt/b {
		return maxInt
	}
	return a * b
}

// AcceptCount 返回指定输入起点理论上可接受的重复次数。
func (p *Program) AcceptCount(data []byte, start int) int { return len(p.MatchAt(data, start)) }

// MatchAt 返回指定起点的所有可接受结束偏移，按重复次数递增排列。
func (p *Program) MatchAt(data []byte, start int) []int {
	return p.MatchAtInto(data, start, nil)
}

// MatchAtInto 将指定起点的结束偏移写入 dst 并返回结果切片。
// 调用方可复用 dst，避免重复扫描时为每个起点分配临时切片。
func (p *Program) MatchAtInto(data []byte, start int, dst []int) []int {
	if p == nil || p.Validate() != nil || start < 0 || start > len(data) {
		return dst[:0]
	}
	ends := dst[:0]
	end := start
	for count := 0; count < p.Min; count++ {
		if !p.unitAt(data, end) {
			return ends[:0]
		}
		end += len(p.Unit)
	}
	ends = append(ends, end)
	for count := p.Min; p.Max < 0 || count < p.Max; count++ {
		if !p.unitAt(data, end) {
			break
		}
		end += len(p.Unit)
		ends = append(ends, end)
	}
	return ends
}

// MatchAtLimit 返回不超过 limit 个结束偏移；零值表示不限制。
func (p *Program) MatchAtLimit(data []byte, start, limit int) []int {
	if limit < 0 || p == nil {
		return nil
	}
	ends := p.MatchAt(data, start)
	if limit > 0 && len(ends) > limit {
		return ends[:limit]
	}
	return ends
}

// MatchAtBudget 在重复次数和结果数量预算内执行一次匹配。
// 返回值中的 stopped 表示因预算耗尽而提前终止。
func (p *Program) MatchAtBudget(data []byte, start, maxSteps, maxResults int) (ends []int, steps int, stopped bool) {
	return p.MatchAtBudgetInto(data, start, maxSteps, maxResults, nil)
}

// MatchAtBudgetInto 在预算内执行匹配并复用调用方提供的结束偏移缓冲。
func (p *Program) MatchAtBudgetInto(data []byte, start, maxSteps, maxResults int, dst []int) (ends []int, steps int, stopped bool) {
	if p == nil || p.Validate() != nil || start < 0 || start > len(data) || maxSteps < 0 || maxResults < 0 {
		return dst[:0], 0, false
	}
	ends = dst[:0]
	end := start
	accept := func(pos int) bool {
		if maxResults > 0 && len(ends) >= maxResults {
			return false
		}
		ends = append(ends, pos)
		return true
	}
	for count := 0; count < p.Min; count++ {
		steps++
		if maxSteps > 0 && steps > maxSteps {
			return nil, steps, true
		}
		if !p.unitAt(data, end) {
			return nil, steps, false
		}
		end += len(p.Unit)
	}
	if !accept(end) {
		return ends, steps, true
	}
	for count := p.Min; p.Max < 0 || count < p.Max; count++ {
		steps++
		if maxSteps > 0 && steps > maxSteps {
			return nil, steps, true
		}
		if !p.unitAt(data, end) {
			break
		}
		end += len(p.Unit)
		if !accept(end) {
			return ends, steps, true
		}
	}
	return ends, steps, false
}

// MatchFirst 返回贪婪或非贪婪策略下的单个结束偏移。
func (p *Program) MatchFirst(data []byte, start int) (int, bool) {
	ends := p.MatchAt(data, start)
	if len(ends) == 0 {
		return 0, false
	}
	if p.Greedy {
		return ends[len(ends)-1], true
	}
	return ends[0], true
}

// MatchFirstBudget 在预算内返回贪婪策略选定的首个结束位置。
func (p *Program) MatchFirstBudget(data []byte, start, maxSteps int) (end int, ok bool, stopped bool) {
	ends, _, stopped := p.MatchAtBudget(data, start, maxSteps, 0)
	if len(ends) == 0 {
		return 0, false, stopped
	}
	if p.Greedy {
		return ends[len(ends)-1], true, stopped
	}
	return ends[0], true, stopped
}

// Find 返回输入内全部可接受区间，保留每个起点的重复长度。
func (p *Program) Find(data []byte) [][2]int {
	if p == nil || p.Validate() != nil {
		return nil
	}
	out := make([][2]int, 0)
	ends := make([]int, 0, 8)
	forEachRepeatStart(data, p.Unit, p.Min > 0, func(start int) bool {
		ends = p.MatchAtInto(data, start, ends[:0])
		for _, end := range ends {
			out = append(out, [2]int{start, end})
		}
		return true
	})
	return out
}

// FindLimit 返回不超过 limit 个匹配区间；零值表示不限制。
func (p *Program) FindLimit(data []byte, limit int) [][2]int {
	if limit < 0 || p == nil {
		return nil
	}
	if limit == 0 {
		return p.Find(data)
	}
	out := make([][2]int, 0, limit)
	ends := make([]int, 0, 8)
	forEachRepeatStart(data, p.Unit, p.Min > 0, func(start int) bool {
		if len(out) >= limit {
			return false
		}
		ends = p.MatchAtInto(data, start, ends[:0])
		for _, end := range ends {
			out = append(out, [2]int{start, end})
			if len(out) >= limit {
				return false
			}
		}
		return true
	})
	return out
}

// forEachRepeatStart 枚举重复单元可能出现的起点，并保留重叠匹配。
// 对有最小重复次数的规则使用 Index，避免在长输入上逐字节调用 MatchAt。
func forEachRepeatStart(data, unit []byte, requireUnit bool, fn func(int) bool) {
	if fn == nil {
		return
	}
	if !requireUnit || len(unit) == 0 {
		for start := 0; start <= len(data); start++ {
			if !fn(start) {
				return
			}
		}
		return
	}
	for from := 0; from+len(unit) <= len(data); {
		offset := bytes.Index(data[from:], unit)
		if offset < 0 {
			return
		}
		start := from + offset
		if !fn(start) {
			return
		}
		from = start + 1
	}
}

// FindRange 返回完全位于半开区间内的重复命中。
func (p *Program) FindRange(data []byte, from, to int) [][2]int {
	if p == nil || from < 0 || to < from || to > len(data) {
		return nil
	}
	out := make([][2]int, 0)
	ends := make([]int, 0, 8)
	forEachRepeatStartRange(data, p.Unit, p.Min > 0, from, to, func(start int) bool {
		ends = p.MatchAtInto(data, start, ends[:0])
		for _, end := range ends {
			if end <= to {
				out = append(out, [2]int{start, end})
			}
		}
		return true
	})
	return out
}

// FindFrom 返回起点不早于 from 的命中。
func (p *Program) FindFrom(data []byte, from int) [][2]int { return p.FindRange(data, from, len(data)) }

// FindTo 返回终点不超过 to 的全部命中。
func (p *Program) FindTo(data []byte, to int) [][2]int { return p.FindRange(data, 0, to) }

// FindRangeLimit 返回区间内不超过 limit 个重复命中。
func (p *Program) FindRangeLimit(data []byte, from, to, limit int) [][2]int {
	if p == nil || limit < 0 || from < 0 || to < from || to > len(data) {
		return nil
	}
	if limit == 0 {
		return p.FindRange(data, from, to)
	}
	out := make([][2]int, 0, limit)
	ends := make([]int, 0, 8)
	forEachRepeatStartRange(data, p.Unit, p.Min > 0, from, to, func(start int) bool {
		if len(out) >= limit {
			return false
		}
		ends = p.MatchAtInto(data, start, ends[:0])
		for _, end := range ends {
			if end > to {
				continue
			}
			out = append(out, [2]int{start, end})
			if len(out) >= limit {
				return false
			}
		}
		return true
	})
	return out
}

func forEachRepeatStartRange(data, unit []byte, requireUnit bool, from, to int, fn func(int) bool) {
	if fn == nil || from < 0 || to < from || from > len(data) {
		return
	}
	if to > len(data) {
		to = len(data)
	}
	if !requireUnit || len(unit) == 0 {
		for start := from; start <= to; start++ {
			if !fn(start) {
				return
			}
		}
		return
	}
	for cursor := from; cursor+len(unit) <= len(data) && cursor <= to; {
		offset := bytes.Index(data[cursor:], unit)
		if offset < 0 {
			return
		}
		start := cursor + offset
		if start > to {
			return
		}
		if !fn(start) {
			return
		}
		cursor = start + 1
	}
}

// MatchAtRange 返回指定起点且结束位置位于区间内的结果。
func (p *Program) MatchAtRange(data []byte, start, from, to int) []int {
	return p.MatchAtRangeLimit(data, start, from, to, 0)
}

// MatchAtRangeLimit 在结束位置区间内限制返回数量。
func (p *Program) MatchAtRangeLimit(data []byte, start, from, to, limit int) []int {
	if p == nil || limit < 0 || start < 0 || start > len(data) || from < 0 || to < from || to > len(data) {
		return nil
	}
	ends := make([]int, 0, 8)
	for _, end := range p.MatchAtInto(data, start, ends[:0]) {
		if end < from || end > to {
			continue
		}
		ends = append(ends, end)
		if limit > 0 && len(ends) >= limit {
			break
		}
	}
	return ends
}

// FindEndRange 返回结束偏移位于半开区间内的重复命中。
func (p *Program) FindEndRange(data []byte, from, to int) [][2]int {
	if p == nil || from < 0 || to < from || to > len(data) {
		return nil
	}
	out := make([][2]int, 0)
	ends := make([]int, 0, 8)
	startFrom := 0
	if maxBytes := p.MaxBytes(); maxBytes >= 0 && from > maxBytes {
		startFrom = from - maxBytes
	}
	startTo := min(to, len(data))
	forEachRepeatStartRange(data, p.Unit, p.Min > 0, startFrom, startTo, func(start int) bool {
		ends = p.MatchAtInto(data, start, ends[:0])
		for _, end := range ends {
			if end < from || end >= to {
				continue
			}
			out = append(out, [2]int{start, end})
		}
		return true
	})
	return out
}

// FindEndRangeLimit 返回结束位置区间内的有限命中。
func (p *Program) FindEndRangeLimit(data []byte, from, to, limit int) [][2]int {
	if p == nil || limit < 0 || from < 0 || to < from || to > len(data) {
		return nil
	}
	out := make([][2]int, 0)
	ends := make([]int, 0, 8)
	startFrom := 0
	if maxBytes := p.MaxBytes(); maxBytes >= 0 && from > maxBytes {
		startFrom = from - maxBytes
	}
	startTo := min(to, len(data))
	forEachRepeatStartRange(data, p.Unit, p.Min > 0, startFrom, startTo, func(start int) bool {
		ends = p.MatchAtInto(data, start, ends[:0])
		for _, end := range ends {
			if end < from || end >= to {
				continue
			}
			out = append(out, [2]int{start, end})
			if limit > 0 && len(out) >= limit {
				return false
			}
		}
		return true
	})
	return out
}

// CountRange 返回指定区间内的命中数量。
func (p *Program) CountRange(data []byte, from, to int) int {
	return len(p.FindRange(data, from, to))
}

// CountRangeLimit 统计区间内命中，达到上限后提前结束。
func (p *Program) CountRangeLimit(data []byte, from, to, limit int) int {
	return len(p.FindRangeLimit(data, from, to, limit))
}

// Count 返回输入中的重复命中数量。
func (p *Program) Count(data []byte) int { return len(p.Find(data)) }

// FindReverse 返回按起点、终点逆序排列的重复命中。
func (p *Program) FindReverse(data []byte) [][2]int {
	out := p.Find(data)
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

// FindReverseLimit 返回逆序排列且不超过上限的命中。
func (p *Program) FindReverseLimit(data []byte, limit int) [][2]int {
	if limit < 0 {
		return nil
	}
	out := p.FindReverse(data)
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

// Contains 判断输入中是否存在重复命中。
func (p *Program) Contains(data []byte) bool { return p != nil && len(p.Find(data)) > 0 }

// FindBudget 在起点扫描和结果数量预算内返回命中。
func (p *Program) FindBudget(data []byte, maxSteps, maxResults int) (hits [][2]int, steps int, stopped bool) {
	if p == nil || maxSteps < 0 || maxResults < 0 {
		return nil, 0, false
	}
	ends := make([]int, 0, 8)
	stoppedByBudget := false
	visit := func(start int) bool {
		ends = p.MatchAtInto(data, start, ends[:0])
		for _, end := range ends {
			steps++
			if maxSteps > 0 && steps > maxSteps {
				stoppedByBudget = true
				hits = nil
				return false
			}
			hits = append(hits, [2]int{start, end})
			if maxResults > 0 && len(hits) >= maxResults {
				stoppedByBudget = true
				return false
			}
		}
		return true
	}
	forEachRepeatStart(data, p.Unit, p.Min > 0, visit)
	if stoppedByBudget {
		return hits, steps, true
	}
	return hits, steps, false
}

// MaxMatches 返回在指定输入中的理论最大命中数；无上限时返回 -1。
func (p *Program) MaxMatches(data []byte) int {
	if p == nil || len(p.Unit) == 0 {
		return 0
	}
	if p.Max < 0 {
		return -1
	}
	count := 0
	ends := make([]int, 0, 8)
	for start := 0; start <= len(data); start++ {
		ends = p.MatchAtInto(data, start, ends[:0])
		if len(ends) > int(^uint(0)>>1)-count {
			return int(^uint(0) >> 1)
		}
		count += len(ends)
	}
	return count
}

// Clone 返回不共享文字字节的独立副本。
func (p *Program) Clone() *Program {
	if p == nil {
		return nil
	}
	clone, err := New(p.Unit, p.Min, p.Max, p.Greedy)
	if err != nil {
		return nil
	}
	return clone
}

// Dump 序列化重复程序。
func (p *Program) Dump() ([]byte, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(struct {
		Version int      `json:"version"`
		Program *Program `json:"program"`
	}{Version: version, Program: p})
}

// Load 反序列化并校验重复程序。
func Load(data []byte) (*Program, error) {
	var raw struct {
		Version int      `json:"version"`
		Program *Program `json:"program"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	if raw.Version != version {
		return nil, fmt.Errorf("unsupported repeat version %d", raw.Version)
	}
	if raw.Program == nil {
		return nil, fmt.Errorf("nil repeat program")
	}
	return New(raw.Program.Unit, raw.Program.Min, raw.Program.Max, raw.Program.Greedy)
}

func (p *Program) unitAt(data []byte, start int) bool {
	if start+len(p.Unit) > len(data) {
		return false
	}
	for i, value := range p.Unit {
		if data[start+i] != value {
			return false
		}
	}
	return true
}
