// Package smallwrite 提供小写入模式的前缀表匹配程序。
package smallwrite

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// Program 是小写入匹配程序，Bytes 为模式字节，MaxInput 限制可处理输入长度。
type Program struct {
	Bytes    []byte `json:"bytes"`
	MaxInput int    `json:"max_input"`
	prefix   []int
}

// Validate 检查小写入程序的字节串和输入预算。
func (p *Program) Validate() error {
	if p == nil || len(p.Bytes) == 0 {
		return fmt.Errorf("invalid small write program")
	}
	if p.MaxInput < 0 {
		return fmt.Errorf("invalid max input")
	}
	return nil
}

// Dump 序列化小写入程序。
func (p *Program) Dump() ([]byte, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	if p.MaxInput == len(p.Bytes) {
		return json.Marshal(p.Bytes)
	}
	return json.Marshal(p)
}

// Load 反序列化小写入程序。
func Load(data []byte) (*Program, error) {
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		return nil, fmt.Errorf("nil program")
	}
	var p Program
	if err := json.Unmarshal(data, &p); err != nil {
		var legacy []byte
		if legacyErr := json.Unmarshal(data, &legacy); legacyErr != nil {
			return nil, err
		}
		return New(legacy), nil
	}
	if p.MaxInput < 0 {
		return nil, fmt.Errorf("invalid max input")
	}
	return NewWithLimit(p.Bytes, p.MaxInput), nil
}

// Empty 判断程序是否缺少模式字节。
func (p *Program) Empty() bool { return p == nil || len(p.Bytes) == 0 }

// New 创建按模式长度限制输入的匹配程序。
func New(data []byte) *Program { return NewWithLimit(data, len(data)) }

// NewWithLimit 创建带输入长度预算的匹配程序，数据会被复制。
func NewWithLimit(data []byte, maxInput int) *Program {
	if maxInput < 0 {
		maxInput = 0
	}
	p := &Program{Bytes: append([]byte(nil), data...), MaxInput: maxInput}
	p.rebuildPrefix()
	return p
}

// Eligible 判断输入长度是否满足程序预算。
func (p *Program) Eligible(input []byte) bool {
	return p != nil && (p.MaxInput == 0 || len(input) <= p.MaxInput)
}

// EligibleRange 判断指定输入区间是否满足小写入容量预算。
func (p *Program) EligibleRange(input []byte, from, to int) bool {
	return p != nil && from >= 0 && to >= from && to <= len(input) && p.Eligible(input[from:to])
}

// MatchCount 返回输入中的重叠命中数量。
func (p *Program) MatchCount(input []byte) int { return len(p.Find(input)) }

// MatchAt 判断程序字节串是否在指定偏移完整匹配。
func (p *Program) MatchAt(input []byte, off int) bool {
	if p == nil || off < 0 || off+len(p.Bytes) > len(input) {
		return false
	}
	for i, b := range p.Bytes {
		if input[off+i] != b {
			return false
		}
	}
	return true
}

// Find 返回所有包含重叠区间的匹配起点。
func (p *Program) Find(input []byte) []int {
	if p == nil || len(p.Bytes) == 0 {
		return nil
	}
	prefix := p.prefix
	if len(prefix) != len(p.Bytes) {
		prefix = buildPrefix(p.Bytes)
	}
	out := make([]int, 0)
	matched := 0
	for offset, value := range input {
		for matched > 0 && value != p.Bytes[matched] {
			matched = prefix[matched-1]
		}
		if value == p.Bytes[matched] {
			matched++
		}
		if matched == len(p.Bytes) {
			out = append(out, offset-len(p.Bytes)+1)
			matched = prefix[matched-1]
		}
	}
	return out
}

// MatchWindow 在输入子区间内查找命中，返回原输入偏移。
func (p *Program) MatchWindow(input []byte, from, to int) []int {
	if p == nil || from < 0 || to < from || to > len(input) {
		return nil
	}
	if from == 0 && to == len(input) {
		return p.Find(input)
	}
	if len(p.Bytes) == 0 || to-from < len(p.Bytes) {
		return nil
	}
	out := make([]int, 0)
	for off := from; off+len(p.Bytes) <= to; off++ {
		if p.MatchAt(input, off) {
			out = append(out, off)
		}
	}
	return out
}

// FindLimit 返回最多 limit 个匹配起点；零值表示不限制。
func (p *Program) FindLimit(input []byte, limit int) []int {
	if limit < 0 {
		return nil
	}
	if limit == 0 {
		return p.Find(input)
	}
	if p == nil || len(p.Bytes) == 0 {
		return nil
	}
	prefix := p.prefix
	if len(prefix) != len(p.Bytes) {
		prefix = buildPrefix(p.Bytes)
	}
	out := make([]int, 0, limit)
	matched := 0
	for offset, value := range input {
		for matched > 0 && value != p.Bytes[matched] {
			matched = prefix[matched-1]
		}
		if value == p.Bytes[matched] {
			matched++
		}
		if matched == len(p.Bytes) {
			out = append(out, offset-len(p.Bytes)+1)
			if len(out) >= limit {
				return out
			}
			matched = prefix[matched-1]
		}
	}
	return out
}

// Contains 判断输入中是否包含程序字节串。
func (p *Program) Contains(input []byte) bool { return len(p.Find(input)) > 0 }

// Count 返回输入中包含的重叠匹配数量。
func (p *Program) Count(input []byte) int { return len(p.Find(input)) }

// FindRange 返回完全位于半开区间内的匹配起点。
func (p *Program) FindRange(input []byte, from, to int) []int {
	if p == nil || from < 0 || to < from || to > len(input) {
		return nil
	}
	return p.MatchWindow(input, from, to)
}

// FindRangeLimit 在区间内查找并限制返回数量。
func (p *Program) FindRangeLimit(input []byte, from, to, limit int) []int {
	if limit < 0 || p == nil || from < 0 || to < from || to > len(input) {
		return nil
	}
	if limit == 0 {
		return p.FindRange(input, from, to)
	}
	out := make([]int, 0, limit)
	for _, offset := range p.MatchWindow(input, from, to) {
		out = append(out, offset)
		if len(out) >= limit {
			break
		}
	}
	return out
}

// FindEndRange 返回结束偏移位于指定半开区间内的命中起点。
func (p *Program) FindEndRange(input []byte, from, to int) []int {
	if p == nil || from < 0 || to < from || to > len(input) {
		return nil
	}
	out := make([]int, 0)
	for _, off := range p.MatchWindow(input, 0, len(input)) {
		end := off + len(p.Bytes)
		if end >= from && end < to {
			out = append(out, off)
		}
	}
	return out
}

// Run 返回输入中全部命中的结束偏移。
func (p *Program) Run(input []byte) []byte {
	if p == nil {
		return nil
	}
	return append([]byte(nil), input...)
}

// RunChecked 执行输入并返回是否满足小写入容量条件。
func (p *Program) RunChecked(input []byte) ([]byte, bool) {
	if !p.Eligible(input) {
		return nil, false
	}
	return p.Run(input), true
}

// RunInto 复用 dst 返回输入中全部命中的结束偏移。
func (p *Program) RunInto(dst, input []byte) []byte {
	if p == nil {
		return dst
	}
	return append(dst, input...)
}

// Size 返回模式字节长度。
func (p *Program) Size() int {
	if p == nil {
		return 0
	}
	return len(p.Bytes)
}

// Clone 深拷贝程序，结果与原程序不共享模式字节。
func (p *Program) Clone() *Program {
	if p == nil {
		return nil
	}
	return NewWithLimit(p.Bytes, p.MaxInput)
}

// BytesCopy 返回模式字节的副本。
func (p *Program) BytesCopy() []byte {
	if p == nil {
		return nil
	}
	return append([]byte(nil), p.Bytes...)
}

func (p *Program) rebuildPrefix() {
	if p == nil {
		return
	}
	p.prefix = buildPrefix(p.Bytes)
}

func buildPrefix(pattern []byte) []int {
	prefix := make([]int, len(pattern))
	for index, length := 1, 0; index < len(pattern); {
		if pattern[index] == pattern[length] {
			length++
			prefix[index] = length
			index++
			continue
		}
		if length > 0 {
			length = prefix[length-1]
			continue
		}
		index++
	}
	return prefix
}
