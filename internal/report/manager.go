// Package report 提供稳定的匹配事件排序和重复抑制。
package report

import (
	"fmt"
	"maps"
	"slices"
	"sort"
)

// Event 是一条匹配事件，SOM 为匹配起点，未启用时为 0。
type Event struct {
	ID            uint32
	From, To, SOM uint64
	Flags         uint32
}

// Span 返回事件的起止偏移。
func (e Event) Span() (uint64, uint64) { return e.From, e.To }

// FlagSingleMatch 表示单次匹配事件，FlagQuiet 表示静默丢弃事件。
const (
	FlagSingleMatch uint32 = 1 << 3
	FlagQuiet       uint32 = 1 << 10
)

// Manager 收集事件、抑制重复并按稳定顺序输出报告。
type Manager struct {
	events     []Event
	seen       map[uint32]bool
	duplicates map[Event]struct{}
	callback   func(Event) bool
	stopped    bool
	MaxEvents  int
}

// Validate 检查事件偏移是否满足 From ≤ To 且 SOM ≤ To。
func (e Event) Validate() error {
	if e.From > e.To {
		return fmt.Errorf("invalid event range")
	}
	if e.SOM > e.To {
		return fmt.Errorf("invalid som offset")
	}
	return nil
}

// Length 返回事件的字节长度，偏移翻转时返回 0。
func (e Event) Length() uint64 {
	if e.To < e.From {
		return 0
	}
	return e.To - e.From
}

// Empty 判断事件是否为长度为零的空匹配。
func (e Event) Empty() bool { return e.From == e.To }

// Equal 判断两个事件的字段是否完全一致。
func (e Event) Equal(other Event) bool { return e == other }

// New 创建空的事件管理器。
func New() *Manager { return &Manager{seen: map[uint32]bool{}, duplicates: map[Event]struct{}{}} }

// Clone 深拷贝管理器，包含已收集事件和去重状态。
func (m *Manager) Clone() *Manager {
	if m == nil {
		return nil
	}
	o := New()
	o.MaxEvents = m.MaxEvents
	o.callback = m.callback
	o.stopped = m.stopped
	o.events = append([]Event(nil), m.events...)
	maps.Copy(o.seen, m.seen)
	for event := range m.duplicates {
		o.duplicates[event] = struct{}{}
	}
	return o
}

// SetCallback 设置事件回调，回调返回 false 时后续事件会被丢弃。
func (m *Manager) SetCallback(fn func(Event) bool) {
	if m != nil {
		m.callback = fn
		m.stopped = false
	}
}

// Add 追加一条事件，非法、静默或重复的事件返回 false。
func (m *Manager) Add(e Event) bool {
	if m == nil {
		return false
	}
	if e.Validate() != nil {
		return false
	}
	if m.seen == nil {
		m.seen = map[uint32]bool{}
	}
	if m.duplicates == nil {
		m.duplicates = map[Event]struct{}{}
	}
	if e.Flags&FlagQuiet != 0 {
		return false
	}
	if _, ok := m.duplicates[e]; ok {
		return false
	}
	// 达到上限时，普通事件不再新增；单次事件仅允许替换同规则结果。
	if m.MaxEvents > 0 && len(m.events) >= m.MaxEvents {
		if e.Flags&FlagSingleMatch == 0 {
			return false
		}
		for i := range m.events {
			if m.events[i].ID == e.ID && m.events[i].Flags&FlagSingleMatch != 0 && eventBefore(e, m.events[i]) {
				delete(m.duplicates, m.events[i])
				m.events[i] = e
				m.duplicates[e] = struct{}{}
				return true
			}
		}
		return false
	}
	if m.stopped {
		return false
	}
	if m.callback != nil && !m.callback(e) {
		m.stopped = true
		return false
	}
	if e.Flags&FlagSingleMatch != 0 && m.seen[e.ID] {
		for i := range m.events {
			if m.events[i].ID == e.ID && m.events[i].Flags&FlagSingleMatch != 0 {
				if e.From < m.events[i].From || e.From == m.events[i].From && e.To < m.events[i].To {
					if m.callback != nil && !m.callback(e) {
						m.stopped = true
						return false
					}
					delete(m.duplicates, m.events[i])
					m.events[i] = e
					m.duplicates[e] = struct{}{}
					return true
				}
				return false
			}
		}
	}
	if m.MaxEvents > 0 && len(m.events) >= m.MaxEvents {
		return false
	}
	if e.Flags&FlagSingleMatch != 0 {
		m.seen[e.ID] = true
	}
	m.events = append(m.events, e)
	m.duplicates[e] = struct{}{}
	return true
}

func eventBefore(a, b Event) bool {
	if a.From != b.From {
		return a.From < b.From
	}
	if a.ID != b.ID {
		return a.ID < b.ID
	}
	if a.To != b.To {
		return a.To < b.To
	}
	return a.SOM < b.SOM
}

// AddAll 逐项加入事件，返回实际写入的数量。
func (m *Manager) AddAll(events []Event) int {
	added := 0
	for _, event := range events {
		if m.Add(event) {
			added++
		}
	}
	return added
}

// SetMaxEvents 设置保留事件上限，负数表示恢复为不限制。
func (m *Manager) SetMaxEvents(limit int) {
	if m == nil {
		return
	}
	if limit < 0 {
		limit = 0
	}
	m.MaxEvents = limit
	if limit > 0 && len(m.events) > limit {
		sort.SliceStable(m.events, func(i, j int) bool {
			if m.events[i].From != m.events[j].From {
				return m.events[i].From < m.events[j].From
			}
			if m.events[i].ID != m.events[j].ID {
				return m.events[i].ID < m.events[j].ID
			}
			if m.events[i].To != m.events[j].To {
				return m.events[i].To < m.events[j].To
			}
			return m.events[i].SOM < m.events[j].SOM
		})
		m.events = m.events[:limit]
	}
	m.rebuildIndexes()
}

// Events 返回按起点、规则编号和终点稳定排序的事件副本。
func (m *Manager) Events() []Event {
	if m == nil {
		return nil
	}
	out := append([]Event(nil), m.events...)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].From != out[j].From {
			return out[i].From < out[j].From
		}
		if out[i].ID != out[j].ID {
			return out[i].ID < out[j].ID
		}
		if out[i].To != out[j].To {
			return out[i].To < out[j].To
		}
		return out[i].SOM < out[j].SOM
	})
	return out
}

// EventsLimit 返回排序后的前 limit 个事件，零值表示全部。
func (m *Manager) EventsLimit(limit int) []Event {
	if m == nil || limit < 0 {
		return nil
	}
	out := m.Events()
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

// DrainLimit 返回并移除当前最早的有限事件；零值表示全部移除。
func (m *Manager) DrainLimit(limit int) []Event {
	if m == nil || limit < 0 {
		return nil
	}
	events := m.EventsLimit(limit)
	if len(events) == 0 {
		return events
	}
	remove := make(map[Event]struct{}, len(events))
	for _, event := range events {
		remove[event] = struct{}{}
	}
	kept := m.events[:0]
	for _, event := range m.events {
		if _, ok := remove[event]; !ok {
			kept = append(kept, event)
		}
	}
	m.events = kept
	m.rebuildIndexes()
	return events
}

// Merge 将另一管理器的事件按当前去重、回调和限额规则合并。
func (m *Manager) Merge(other *Manager) int {
	if m == nil || other == nil {
		return 0
	}
	return m.AddAll(other.Events())
}

// Sort 按稳定报告顺序重排内部事件。
func (m *Manager) Sort() {
	if m == nil {
		return
	}
	sort.SliceStable(m.events, func(i, j int) bool {
		if m.events[i].From != m.events[j].From {
			return m.events[i].From < m.events[j].From
		}
		if m.events[i].ID != m.events[j].ID {
			return m.events[i].ID < m.events[j].ID
		}
		if m.events[i].To != m.events[j].To {
			return m.events[i].To < m.events[j].To
		}
		return m.events[i].SOM < m.events[j].SOM
	})
}

// Reset 清空已收集事件与去重状态，保留回调配置。
func (m *Manager) Reset() {
	if m == nil {
		return
	}
	m.events = m.events[:0]
	m.stopped = false
	if m.seen == nil {
		m.seen = map[uint32]bool{}
	} else {
		clear(m.seen)
	}
	if m.duplicates == nil {
		m.duplicates = map[Event]struct{}{}
	} else {
		clear(m.duplicates)
	}
}

// Stopped 返回回调是否请求停止后续报告。
func (m *Manager) Stopped() bool { return m != nil && m.stopped }

// Deduplicate 按编号和起止偏移原地去除重复事件。
func (m *Manager) Deduplicate() {
	if m == nil {
		return
	}
	seen := map[[4]uint64]struct{}{}
	out := m.events[:0]
	for _, e := range m.events {
		k := [4]uint64{uint64(e.ID), e.From, e.To, e.SOM}
		if _, ok := seen[k]; ok {
			continue
		}
		seen[k] = struct{}{}
		out = append(out, e)
	}
	m.events = out
	m.rebuildIndexes()
}

// Len 返回已收集的事件数量。
func (m *Manager) Len() int {
	if m == nil {
		return 0
	}
	return len(m.events)
}

// Snapshot 返回当前事件的稳定排序快照。
func (m *Manager) Snapshot() []Event {
	if m == nil {
		return nil
	}
	return m.Events()
}

// EventsFor 返回指定规则编号的稳定排序事件副本。
func (m *Manager) EventsFor(id uint32) []Event {
	if m == nil {
		return nil
	}
	var out []Event
	for _, e := range m.events {
		if e.ID == id {
			out = append(out, e)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].From != out[j].From {
			return out[i].From < out[j].From
		}
		if out[i].To != out[j].To {
			return out[i].To < out[j].To
		}
		return out[i].SOM < out[j].SOM
	})
	return out
}

// EventsForLimit 返回指定规则的前 limit 个事件。
func (m *Manager) EventsForLimit(id uint32, limit int) []Event {
	if m == nil || limit < 0 {
		return nil
	}
	out := make([]Event, 0)
	for _, event := range m.EventsFor(id) {
		out = append(out, event)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out
}

// EventsRange 返回完全落在半开区间内的事件。
func (m *Manager) EventsRange(from, to uint64) []Event {
	if m == nil || to < from {
		return nil
	}
	out := make([]Event, 0)
	for _, event := range m.Events() {
		if event.From >= from && event.To <= to {
			out = append(out, event)
		}
	}
	return out
}

// EventsOverlapping 返回与指定半开区间有交集的事件。
func (m *Manager) EventsOverlapping(from, to uint64) []Event {
	if m == nil || to < from {
		return nil
	}
	out := make([]Event, 0)
	for _, event := range m.Events() {
		if event.From < to && event.To > from {
			out = append(out, event)
		}
	}
	return out
}

// EventsRangeLimit 返回完全位于区间内的前 limit 个事件。
func (m *Manager) EventsRangeLimit(from, to uint64, limit int) []Event {
	if m == nil || to < from || limit < 0 {
		return nil
	}
	out := make([]Event, 0)
	for _, event := range m.Events() {
		if event.From < from || event.To > to {
			continue
		}
		out = append(out, event)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out
}

// RemoveRange 删除完全落在区间内的事件并返回删除数量。
func (m *Manager) RemoveRange(from, to uint64) int {
	if m == nil || to < from {
		return 0
	}
	removed := 0
	kept := m.events[:0]
	for _, event := range m.events {
		if event.From >= from && event.To <= to {
			removed++
		} else {
			kept = append(kept, event)
		}
	}
	m.events = kept
	m.rebuildIndexes()
	return removed
}

// EventsByEnd 返回按结束偏移、起点和规则编号排序的事件快照。
func (m *Manager) EventsByEnd() []Event {
	if m == nil {
		return nil
	}
	out := append([]Event(nil), m.events...)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].To != out[j].To {
			return out[i].To < out[j].To
		}
		if out[i].From != out[j].From {
			return out[i].From < out[j].From
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// EventsBySOM 返回按起始匹配偏移、结束偏移和规则编号排序的事件。
func (m *Manager) EventsBySOM() []Event {
	if m == nil {
		return nil
	}
	out := append([]Event(nil), m.events...)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].SOM != out[j].SOM {
			return out[i].SOM < out[j].SOM
		}
		if out[i].To != out[j].To {
			return out[i].To < out[j].To
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// EventsFrom 返回起点不小于 from 的事件。
func (m *Manager) EventsFrom(from uint64) []Event {
	if m == nil {
		return nil
	}
	out := make([]Event, 0)
	for _, event := range m.Events() {
		if event.From >= from {
			out = append(out, event)
		}
	}
	return out
}

// EventsFromLimit 返回起点不小于 from 的前 limit 个事件。
func (m *Manager) EventsFromLimit(from uint64, limit int) []Event {
	if m == nil || limit < 0 {
		return nil
	}
	out := make([]Event, 0)
	for _, event := range m.Events() {
		if event.From < from {
			continue
		}
		out = append(out, event)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out
}

// EventsAfter 返回起点严格晚于指定偏移的事件。
func (m *Manager) EventsAfter(from uint64) []Event {
	if m == nil {
		return nil
	}
	out := make([]Event, 0)
	for _, event := range m.Events() {
		if event.From > from {
			out = append(out, event)
		}
	}
	return out
}

// EventsForRange 返回指定规则且完全位于区间内的事件。
func (m *Manager) EventsForRange(id uint32, from, to uint64) []Event {
	if m == nil || to < from {
		return nil
	}
	out := make([]Event, 0)
	for _, event := range m.events {
		if event.ID == id && event.From >= from && event.To <= to {
			out = append(out, event)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].From != out[j].From {
			return out[i].From < out[j].From
		}
		return out[i].To < out[j].To
	})
	return out
}

// IDs 返回当前事件涉及的规则编号并去重排序。
func (m *Manager) IDs() []uint32 {
	if m == nil {
		return nil
	}
	seen := map[uint32]struct{}{}
	for _, event := range m.events {
		seen[event.ID] = struct{}{}
	}
	out := make([]uint32, 0, len(seen))
	for id := range seen {
		out = append(out, id)
	}
	slices.Sort(out)
	return out
}

// CountFor 返回指定规则的事件数量。
func (m *Manager) CountFor(id uint32) int { return len(m.EventsFor(id)) }

// Counts 返回各规则当前事件数量。
func (m *Manager) Counts() map[uint32]int {
	if m == nil {
		return nil
	}
	out := make(map[uint32]int)
	for _, event := range m.events {
		out[event.ID]++
	}
	return out
}

// FirstFor 返回指定规则最早事件。
func (m *Manager) FirstFor(id uint32) (Event, bool) {
	events := m.EventsFor(id)
	if len(events) == 0 {
		return Event{}, false
	}
	return events[0], true
}

// LastFor 返回指定规则最晚事件。
func (m *Manager) LastFor(id uint32) (Event, bool) {
	events := m.EventsFor(id)
	if len(events) == 0 {
		return Event{}, false
	}
	return events[len(events)-1], true
}

// HasID 判断是否已收集到指定规则编号的事件。
func (m *Manager) HasID(id uint32) bool {
	if m == nil {
		return false
	}
	for _, e := range m.events {
		if e.ID == id {
			return true
		}
	}
	return false
}

// Contains 判断是否存在完全相同的事件。
func (m *Manager) Contains(event Event) bool {
	if m == nil {
		return false
	}
	if _, ok := m.duplicates[event]; ok {
		return true
	}
	return false
}

// Filter 返回满足谓词的事件快照。
func (m *Manager) Filter(fn func(Event) bool) []Event {
	if m == nil || fn == nil {
		return nil
	}
	out := make([]Event, 0)
	for _, event := range m.Events() {
		if fn(event) {
			out = append(out, event)
		}
	}
	return out
}

// ValidateAll 校验当前全部事件并返回首个失败索引。
func (m *Manager) ValidateAll() (int, error) {
	if m == nil {
		return -1, fmt.Errorf("nil report manager")
	}
	for i, event := range m.events {
		if err := event.Validate(); err != nil {
			return i, err
		}
	}
	return -1, nil
}

// IDsRange 返回区间内涉及的规则编号快照。
func (m *Manager) IDsRange(from, to uint64) []uint32 {
	ids := make(map[uint32]struct{})
	for _, event := range m.EventsRange(from, to) {
		ids[event.ID] = struct{}{}
	}
	out := make([]uint32, 0, len(ids))
	for id := range ids {
		out = append(out, id)
	}
	slices.Sort(out)
	return out
}

// First 返回排序后的首个事件。
func (m *Manager) First() (Event, bool) {
	events := m.Events()
	if len(events) == 0 {
		return Event{}, false
	}
	return events[0], true
}

// Last 返回排序后的末个事件。
func (m *Manager) Last() (Event, bool) {
	events := m.Events()
	if len(events) == 0 {
		return Event{}, false
	}
	return events[len(events)-1], true
}

// RemoveID 删除指定规则的全部事件并重建索引。
func (m *Manager) RemoveID(id uint32) int {
	if m == nil {
		return 0
	}
	out := m.events[:0]
	removed := 0
	for _, event := range m.events {
		if event.ID == id {
			removed++
			continue
		}
		out = append(out, event)
	}
	m.events = out
	if removed > 0 {
		m.rebuildIndexes()
	}
	return removed
}

// Remove 删除指定事件，返回是否存在。
func (m *Manager) Remove(target Event) bool {
	if m == nil {
		return false
	}
	for i, event := range m.events {
		if event != target {
			continue
		}
		copy(m.events[i:], m.events[i+1:])
		m.events = m.events[:len(m.events)-1]
		m.rebuildIndexes()
		return true
	}
	return false
}

func (m *Manager) rebuildIndexes() {
	if m == nil {
		return
	}
	if m.seen == nil {
		m.seen = map[uint32]bool{}
	} else {
		clear(m.seen)
	}
	if m.duplicates == nil {
		m.duplicates = map[Event]struct{}{}
	} else {
		clear(m.duplicates)
	}
	for _, event := range m.events {
		if event.Flags&FlagSingleMatch != 0 {
			m.seen[event.ID] = true
		}
		m.duplicates[event] = struct{}{}
	}
}
