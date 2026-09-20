// Package scratch 提供可复用的单次扫描存储。
package scratch

import (
	"fmt"
	"sync"
)

const maxScratchBytes = 64 << 20

type Scratch struct {
	Bytes  []byte
	States []uint32
	Report []uint32
	Temp   []byte
}

// Validate 检查各分区长度、容量和总资源上限，拒绝被篡改的工作区。
func (s *Scratch) Validate() error {
	if s == nil {
		return fmt.Errorf("nil scratch")
	}
	if len(s.Bytes) > cap(s.Bytes) || len(s.States) > cap(s.States) || len(s.Report) > cap(s.Report) || len(s.Temp) > cap(s.Temp) {
		return fmt.Errorf("scratch length exceeds capacity")
	}
	if s.TotalCapacity() > maxScratchBytes {
		return fmt.Errorf("scratch exceeds resource limit")
	}
	return nil
}

func (s *Scratch) Clone() *Scratch {
	if s == nil {
		return nil
	}
	return &Scratch{Bytes: append([]byte(nil), s.Bytes...), States: append([]uint32(nil), s.States...), Report: append([]uint32(nil), s.Report...), Temp: append([]byte(nil), s.Temp...)}
}

func New() *Scratch { return &Scratch{} }
func (s *Scratch) Reset() {
	if s == nil {
		return
	}
	s.Bytes = s.Bytes[:0]
	s.States = s.States[:0]
	s.Report = s.Report[:0]
	s.Temp = s.Temp[:0]
}

func (s *Scratch) ReserveBytes(n int) []byte {
	if s == nil || n < 0 {
		return nil
	}
	if cap(s.Bytes) < n {
		s.Bytes = make([]byte, 0, n)
	}
	s.Bytes = s.Bytes[:n]
	return s.Bytes
}
func (s *Scratch) ReserveStates(n int) []uint32 {
	if s == nil || n < 0 {
		return nil
	}
	if cap(s.States) < n {
		s.States = make([]uint32, 0, n)
	}
	s.States = s.States[:n]
	return s.States
}
func (s *Scratch) ReserveReport(n int) []uint32 {
	if s == nil || n < 0 {
		return nil
	}
	if cap(s.Report) < n {
		s.Report = make([]uint32, 0, n)
	}
	s.Report = s.Report[:n]
	return s.Report
}
func (s *Scratch) ReserveTemp(n int) []byte {
	if s == nil || n < 0 {
		return nil
	}
	if cap(s.Temp) < n {
		s.Temp = make([]byte, 0, n)
	}
	s.Temp = s.Temp[:n]
	return s.Temp
}
func (s *Scratch) Capacity() (int, int) {
	if s == nil {
		return 0, 0
	}
	return cap(s.Bytes), cap(s.States)
}

// CapacityBytes 返回四个分区按字节计的容量。
func (s *Scratch) CapacityBytes() (bytes, states, report, temp int) {
	if s == nil {
		return 0, 0, 0, 0
	}
	return cap(s.Bytes), cap(s.States) * 4, cap(s.Report) * 4, cap(s.Temp)
}

// ReportCapacity 返回报告分区的容量。
func (s *Scratch) ReportCapacity() int {
	if s == nil {
		return 0
	}
	return cap(s.Report)
}
func (s *Scratch) TempCapacity() int {
	if s == nil {
		return 0
	}
	return cap(s.Temp)
}
func (s *Scratch) StateCount() int {
	if s == nil {
		return 0
	}
	return len(s.States)
}
func (s *Scratch) ReportCount() int {
	if s == nil {
		return 0
	}
	return len(s.Report)
}
func (s *Scratch) TempCount() int {
	if s == nil {
		return 0
	}
	return len(s.Temp)
}

// TotalCapacity 返回全部分区的总容量。
func (s *Scratch) TotalCapacity() int {
	if s == nil {
		return 0
	}
	total := uint64(cap(s.Bytes))
	add := func(value uint64) {
		if ^uint64(0)-total < value {
			total = ^uint64(0)
			return
		}
		total += value
	}
	add(uint64(cap(s.States)) * 4)
	add(uint64(cap(s.Report)) * 4)
	add(uint64(cap(s.Temp)))
	if total > uint64(^uint(0)>>1) {
		return int(^uint(0) >> 1)
	}
	return int(total)
}

// Capacities 返回四个分区的容量快照，顺序为字节、状态、报告、临时。
func (s *Scratch) Capacities() (bytes, states, report, temp int) {
	if s == nil {
		return 0, 0, 0, 0
	}
	return cap(s.Bytes), cap(s.States), cap(s.Report), cap(s.Temp)
}

// ReserveAll 一次性准备全部分区并返回是否成功。
func (s *Scratch) ReserveAll(bytesN, statesN, reportN, tempN int) bool {
	if s == nil || bytesN < 0 || statesN < 0 || reportN < 0 || tempN < 0 {
		return false
	}
	s.ReserveBytes(bytesN)
	s.ReserveStates(statesN)
	s.ReserveReport(reportN)
	s.ReserveTemp(tempN)
	return true
}

// ReserveAllWithin 按总容量预算准备分区，超出预算时不改变当前对象。
func (s *Scratch) ReserveAllWithin(bytesN, statesN, reportN, tempN, budget int) bool {
	if s == nil || budget < 0 || bytesN < 0 || statesN < 0 || reportN < 0 || tempN < 0 {
		return false
	}
	var total uint64
	values := []uint64{uint64(bytesN), uint64(statesN), uint64(reportN), uint64(tempN)}
	for i, n := range values {
		if i == 1 || i == 2 {
			if n > ^uint64(0)/4 {
				return false
			}
			n *= 4
		}
		if ^uint64(0)-total < n {
			return false
		}
		total += n
	}
	if budget > 0 && total > uint64(budget) {
		return false
	}
	if total > maxScratchBytes {
		return false
	}
	return s.ReserveAll(bytesN, statesN, reportN, tempN)
}

// Zero 清空所有分区中的敏感内容后重置长度。
func (s *Scratch) Zero() {
	if s == nil {
		return
	}
	for i := range s.Bytes {
		s.Bytes[i] = 0
	}
	for i := range s.Temp {
		s.Temp[i] = 0
	}
	s.Reset()
}
func (s *Scratch) ResetReport() {
	if s != nil {
		s.Report = s.Report[:0]
	}
}

// ResetStates 清空状态分区并保留容量。
func (s *Scratch) ResetStates() {
	if s != nil {
		s.States = s.States[:0]
	}
}

// ResetBytes 清空字节分区并保留容量。
func (s *Scratch) ResetBytes() {
	if s != nil {
		s.Bytes = s.Bytes[:0]
	}
}

// ResetTemp 清空临时分区并保留容量。
func (s *Scratch) ResetTemp() {
	if s != nil {
		s.Temp = s.Temp[:0]
	}
}

// TotalCount 返回全部分区当前元素数量。
func (s *Scratch) TotalCount() int {
	if s == nil {
		return 0
	}
	return len(s.Bytes) + len(s.States) + len(s.Report) + len(s.Temp)
}

// Trim 释放未使用容量并保留当前内容。
func (s *Scratch) Trim() {
	if s == nil {
		return
	}
	s.Bytes = append([]byte(nil), s.Bytes...)
	s.States = append([]uint32(nil), s.States...)
	s.Report = append([]uint32(nil), s.Report...)
	s.Temp = append([]byte(nil), s.Temp...)
}

// CopyInto 将当前内容复制到目标 Scratch，目标原有内容会被替换。
func (s *Scratch) CopyInto(dst *Scratch) bool {
	if s == nil || dst == nil {
		return false
	}
	dst.Bytes = append(dst.Bytes[:0], s.Bytes...)
	dst.States = append(dst.States[:0], s.States...)
	dst.Report = append(dst.Report[:0], s.Report...)
	dst.Temp = append(dst.Temp[:0], s.Temp...)
	return true
}

type Pool struct{ pool sync.Pool }

func NewPool() *Pool { p := &Pool{}; p.pool.New = func() any { return New() }; return p }
func (p *Pool) Get() *Scratch {
	if p == nil {
		return New()
	}
	s := p.pool.Get().(*Scratch)
	s.Reset()
	return s
}
func (p *Pool) Put(s *Scratch) {
	if p != nil && s != nil {
		s.Reset()
		p.pool.Put(s)
	}
}
