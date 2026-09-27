// Package som 跟踪块扫描的匹配起点偏移。
package som

import (
	"encoding/json"
	"fmt"
	"slices"
)

// Slot 是跟踪器中的一个槽位，Valid 为 false 时表示尚未写入。
type Slot struct {
	Value uint64
	Valid bool
}

// Tracker 为每个规则分配一个槽位，记录块扫描中的匹配起点。
type Tracker struct{ slots []Slot }

// Dump 序列化当前起点跟踪状态。
func (t *Tracker) Dump() ([]byte, error) {
	if t == nil {
		return nil, fmt.Errorf("nil tracker")
	}
	return json.Marshal(t.slots)
}

// Load 反序列化起点跟踪状态。
func Load(data []byte) (*Tracker, error) {
	var slots []Slot
	if err := json.Unmarshal(data, &slots); err != nil {
		return nil, err
	}
	return &Tracker{slots: slots}, nil
}

// Merge 将另一个跟踪器中的最早起点合并到当前对象。
func (t *Tracker) Merge(other *Tracker) {
	if t == nil || other == nil {
		return
	}
	if other.Len() > t.Len() {
		t.slots = append(t.slots, make([]Slot, other.Len()-t.Len())...)
	}
	for i := 0; i < other.Len(); i++ {
		if value, ok := other.Get(i); ok {
			t.Earliest(i, value)
		}
	}
}

// Equal 判断两个跟踪器的槽位状态和值是否一致。
func (t *Tracker) Equal(other *Tracker) bool {
	if t == nil || other == nil {
		return t == other
	}
	if t.Len() != other.Len() {
		return false
	}
	for i := range t.slots {
		if t.slots[i] != other.slots[i] {
			return false
		}
	}
	return true
}

// Clone 返回槽位快照的副本。
func (t *Tracker) Clone() *Tracker {
	if t == nil {
		return nil
	}
	return &Tracker{slots: append([]Slot(nil), t.slots...)}
}

// New 创建包含 n 个槽位的跟踪器，负数按 0 处理。
func New(n int) *Tracker {
	if n < 0 {
		n = 0
	}
	return &Tracker{slots: make([]Slot, n)}
}

// Set 写入槽位取值，槽位越界时忽略。
func (t *Tracker) Set(slot int, value uint64) {
	if t != nil && slot >= 0 && slot < len(t.slots) {
		t.slots[slot] = Slot{value, true}
	}
}

// Resize 调整槽位数量；扩容槽位初始无效。
func (t *Tracker) Resize(size int) {
	if t == nil || size < 0 {
		return
	}
	if size <= len(t.slots) {
		t.slots = t.slots[:size]
		return
	}
	t.slots = append(t.slots, make([]Slot, size-len(t.slots))...)
}

// Ensure 确保指定槽位存在，并返回该槽位是否可访问。
func (t *Tracker) Ensure(slot int) bool {
	if t == nil || slot < 0 {
		return false
	}
	if slot >= len(t.slots) {
		t.Resize(slot + 1)
	}
	return true
}

// Clear 重置单个槽位，槽位越界时忽略。
func (t *Tracker) Clear(slot int) {
	if t != nil && slot >= 0 && slot < len(t.slots) {
		t.slots[slot] = Slot{}
	}
}

// Valid 判断槽位是否已写入有效取值。
func (t *Tracker) Valid(slot int) bool { _, ok := t.Get(slot); return ok }

// Values 按槽位顺序返回值快照，未生效的槽位填 0。
func (t *Tracker) Values() []uint64 {
	if t == nil {
		return nil
	}
	out := make([]uint64, len(t.slots))
	for i, s := range t.slots {
		if s.Valid {
			out[i] = s.Value
		}
	}
	return out
}

// SlotsCopy 返回槽位快照的副本，便于并发读取。
func (t *Tracker) SlotsCopy() []Slot {
	if t == nil {
		return nil
	}
	return append([]Slot(nil), t.slots...)
}

// ClearAll 清空全部槽位。
func (t *Tracker) ClearAll() {
	if t != nil {
		t.Reset()
	}
}

// Get 返回槽位取值，槽位越界或未写入时 ok 为 false。
func (t *Tracker) Get(slot int) (uint64, bool) {
	if t == nil || slot < 0 || slot >= len(t.slots) {
		return 0, false
	}
	s := t.slots[slot]
	return s.Value, s.Valid
}

// Reset 将全部槽位恢复为未写入状态。
func (t *Tracker) Reset() {
	for i := range t.slots {
		t.slots[i] = Slot{}
	}
}

// Propagate 将 src 的取值按“取更早”的规则传播到 dst。
func (t *Tracker) Propagate(dst, src int) {
	if t == nil {
		return
	}
	if v, ok := t.Get(src); ok {
		t.Earliest(dst, v)
	}
}

// Earliest 仅在 value 更早时更新槽位。
func (t *Tracker) Earliest(slot int, value uint64) {
	if t == nil {
		return
	}
	if old, ok := t.Get(slot); !ok || value < old {
		t.Set(slot, value)
	}
}

// Latest 仅在新值更晚时更新槽位。
func (t *Tracker) Latest(slot int, value uint64) {
	if t == nil {
		return
	}
	if old, ok := t.Get(slot); !ok || value > old {
		t.Set(slot, value)
	}
}

// Len 返回槽位总数。
func (t *Tracker) Len() int {
	if t == nil {
		return 0
	}
	return len(t.slots)
}

// Snapshot 返回槽位快照的副本。
func (t *Tracker) Snapshot() []Slot {
	if t == nil {
		return nil
	}
	return append([]Slot(nil), t.slots...)
}

// ValidCount 返回已写入有效取值的槽位数量。
func (t *Tracker) ValidCount() int {
	if t == nil {
		return 0
	}
	n := 0
	for _, s := range t.slots {
		if s.Valid {
			n++
		}
	}
	return n
}

// ValidSlots 返回当前有效槽位编号。
func (t *Tracker) ValidSlots() []int {
	if t == nil {
		return nil
	}
	out := make([]int, 0, t.ValidCount())
	for i, slot := range t.slots {
		if slot.Valid {
			out = append(out, i)
		}
	}
	return out
}

// ClearRange 清空半开区间内的槽位。
func (t *Tracker) ClearRange(from, to int) {
	if t == nil || from < 0 || to < from {
		return
	}
	if to > len(t.slots) {
		to = len(t.slots)
	}
	for i := from; i < to; i++ {
		t.slots[i] = Slot{}
	}
}

// EarliestValue 返回全部有效槽位中的最早起点。
func (t *Tracker) EarliestValue() (uint64, bool) {
	if t == nil {
		return 0, false
	}
	var value uint64
	ok := false
	for _, slot := range t.slots {
		if slot.Valid && (!ok || slot.Value < value) {
			value, ok = slot.Value, true
		}
	}
	return value, ok
}

// LatestValue 返回全部有效槽位中的最晚起点。
func (t *Tracker) LatestValue() (uint64, bool) {
	if t == nil {
		return 0, false
	}
	var value uint64
	ok := false
	for _, slot := range t.slots {
		if slot.Valid && (!ok || slot.Value > value) {
			value, ok = slot.Value, true
		}
	}
	return value, ok
}

// ValidValues 返回有效槽位值并按起点升序排列。
func (t *Tracker) ValidValues() []uint64 {
	if t == nil {
		return nil
	}
	out := make([]uint64, 0, t.ValidCount())
	for _, slot := range t.slots {
		if slot.Valid {
			out = append(out, slot.Value)
		}
	}
	slices.Sort(out)
	return out
}

// Compact 删除无效槽位，仅保留有效值并返回新槽位编号映射。
func (t *Tracker) Compact() map[int]int {
	if t == nil {
		return nil
	}
	mapping := make(map[int]int)
	out := make([]Slot, 0, t.ValidCount())
	for old, slot := range t.slots {
		if !slot.Valid {
			continue
		}
		mapping[old] = len(out)
		out = append(out, slot)
	}
	t.slots = out
	return mapping
}

// CopyRange 返回槽位半开区间的独立跟踪器。
func (t *Tracker) CopyRange(from, to int) *Tracker {
	if t == nil || from < 0 || to < from || from > len(t.slots) {
		return nil
	}
	if to > len(t.slots) {
		to = len(t.slots)
	}
	return &Tracker{slots: append([]Slot(nil), t.slots[from:to]...)}
}

// MergeRange 将另一跟踪器的指定区间合并到目标槽位起点。
func (t *Tracker) MergeRange(other *Tracker, from, to, dst int) {
	if t == nil || other == nil || from < 0 || to < from || dst < 0 {
		return
	}
	if to > other.Len() {
		to = other.Len()
	}
	for i := from; i < to; i++ {
		if value, ok := other.Get(i); ok {
			t.Earliest(dst+i-from, value)
		}
	}
}

// EarliestRange 返回槽位区间内最早的有效起点。
func (t *Tracker) EarliestRange(from, to int) (uint64, bool) {
	if t == nil || from < 0 || to < from {
		return 0, false
	}
	if to > len(t.slots) {
		to = len(t.slots)
	}
	var value uint64
	ok := false
	for _, slot := range t.slots[from:to] {
		if slot.Valid && (!ok || slot.Value < value) {
			value, ok = slot.Value, true
		}
	}
	return value, ok
}
