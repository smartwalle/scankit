// Package smallengine 统一管理短文字规则的两种专用执行程序。
package smallengine

import (
	"encoding/json"
	"fmt"
	"github.com/smartwalle/scankit/internal/smallblock"
	"github.com/smartwalle/scankit/internal/smallwrite"
)

type Kind uint8

const (
	KindNone Kind = iota
	KindSmallWrite
	KindSmallBlock
)

func (k Kind) String() string {
	if k == KindSmallWrite {
		return "smallwrite"
	}
	if k == KindSmallBlock {
		return "smallblock"
	}
	return "none"
}

// Program 保存已选择的短文字执行程序。
type Program struct {
	Kind  Kind
	Write *smallwrite.Program
	Block *smallblock.Program
}

// CompileOptions 控制短文字执行器的选择边界。
type CompileOptions struct {
	MaxInput       int
	SmallWriteSize int
	PreferBlock    bool
}

// Compile 按文字长度选择短写入或小块执行路径。
func Compile(literal []byte, maxInput int) (*Program, error) {
	return CompileWithOptions(literal, CompileOptions{MaxInput: maxInput, SmallWriteSize: 8})
}

// CompileWithOptions 根据文字宽度和调用方偏好选择执行器。
func CompileWithOptions(literal []byte, options CompileOptions) (*Program, error) {
	if len(literal) == 0 {
		return nil, fmt.Errorf("empty literal")
	}
	if options.MaxInput < 0 {
		return nil, fmt.Errorf("invalid max input")
	}
	threshold := options.SmallWriteSize
	if threshold <= 0 {
		threshold = 8
	}
	if !options.PreferBlock && len(literal) <= threshold {
		return &Program{Kind: KindSmallWrite, Write: smallwrite.NewWithLimit(literal, options.MaxInput)}, nil
	}
	return &Program{Kind: KindSmallBlock, Block: smallblock.NewWithLimit(literal, options.MaxInput)}, nil
}

func (p *Program) Validate() error {
	if p == nil {
		return fmt.Errorf("nil small engine")
	}
	switch p.Kind {
	case KindSmallWrite:
		if p.Write == nil {
			return fmt.Errorf("missing small write program")
		}
		return p.Write.Validate()
	case KindSmallBlock:
		if p.Block == nil {
			return fmt.Errorf("missing small block program")
		}
		return p.Block.Validate()
	default:
		return fmt.Errorf("invalid small engine kind")
	}
}

// Clone 创建短文字执行程序的独立副本。
func (p *Program) Clone() *Program {
	if p == nil {
		return nil
	}
	out := &Program{Kind: p.Kind}
	if p.Write != nil {
		out.Write = p.Write.Clone()
	}
	if p.Block != nil {
		out.Block = p.Block.Clone()
	}
	return out
}

func (p *Program) Size() int {
	if p == nil {
		return 0
	}
	if p.Write != nil {
		return p.Write.Size()
	}
	if p.Block != nil {
		return p.Block.Size()
	}
	return 0
}
func (p *Program) Eligible(data []byte) bool {
	if p == nil {
		return false
	}
	if p.Write != nil {
		return p.Write.Eligible(data)
	}
	if p.Block != nil {
		return p.Block.Eligible(data)
	}
	return false
}
func (p *Program) Find(data []byte) []int {
	if p == nil {
		return nil
	}
	if p.Write != nil {
		return p.Write.Find(data)
	}
	if p.Block != nil {
		return p.Block.Find(data)
	}
	return nil
}

// MatchAt 判断指定偏移是否命中。
func (p *Program) MatchAt(data []byte, off int) bool {
	if p == nil {
		return false
	}
	if p.Write != nil {
		return p.Write.MatchAt(data, off)
	}
	if p.Block != nil {
		return p.Block.MatchAt(data, off)
	}
	return false
}

// Dump 序列化短文字执行程序。
func (p *Program) Dump() ([]byte, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(struct {
		Version int                 `json:"version"`
		Kind    Kind                `json:"kind"`
		Write   *smallwrite.Program `json:"write,omitempty"`
		Block   *smallblock.Program `json:"block,omitempty"`
	}{1, p.Kind, p.Write, p.Block})
}

// Load 反序列化并校验短文字执行程序。
func Load(data []byte) (*Program, error) {
	var raw struct {
		Version int                 `json:"version"`
		Kind    Kind                `json:"kind"`
		Write   *smallwrite.Program `json:"write"`
		Block   *smallblock.Program `json:"block"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	if raw.Version != 1 {
		return nil, fmt.Errorf("unsupported small engine version %d", raw.Version)
	}
	p := &Program{Kind: raw.Kind, Write: raw.Write, Block: raw.Block}
	if err := p.Validate(); err != nil {
		return nil, err
	}
	return p, nil
}
func (p *Program) FindRange(data []byte, from, to, limit int) []int {
	if p == nil {
		return nil
	}
	if p.Write != nil {
		return p.Write.FindRangeLimit(data, from, to, limit)
	}
	if p.Block != nil {
		return p.Block.FindRangeLimit(data, from, to, limit)
	}
	return nil
}
