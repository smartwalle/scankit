package parser

import "fmt"

type Combination struct{ Expr Node }

func (Combination) node() {}

type CombinationOperand struct{ ID uint32 }

func (CombinationOperand) node() {}

type CombinationOperator struct {
	Op          byte
	Left, Right Node
}

func (CombinationOperator) node() {}

type CombinationNot struct{ Child Node }

func (CombinationNot) node() {}

// ParseCombination 解析 rule-id 逻辑组合语法。
func ParseCombination(input string) (Node, error) {
	p := &combinationState{input: []byte(input)}
	n, err := p.parseOr()
	if err != nil {
		return nil, err
	}
	p.skipSpace()
	if p.pos != len(p.input) {
		return nil, &ParseError{Position: p.pos, Message: fmt.Sprintf("unexpected combination character %q", p.input[p.pos])}
	}
	return Combination{Expr: n}, nil
}

type combinationState struct {
	input []byte
	pos   int
}

func (p *combinationState) parseOr() (Node, error) {
	p.skipSpace()
	left, err := p.parseAnd()
	if err != nil {
		return nil, err
	}
	for {
		p.skipSpace()
		if p.pos >= len(p.input) || p.input[p.pos] != '|' {
			break
		}
		p.pos++
		p.skipSpace()
		right, e := p.parseAnd()
		if e != nil {
			return nil, e
		}
		left = CombinationOperator{Op: '|', Left: left, Right: right}
	}
	return left, nil
}
func (p *combinationState) parseAnd() (Node, error) {
	p.skipSpace()
	left, err := p.parseUnary()
	if err != nil {
		return nil, err
	}
	for {
		p.skipSpace()
		if p.pos >= len(p.input) || p.input[p.pos] != '&' {
			break
		}
		p.pos++
		p.skipSpace()
		right, e := p.parseUnary()
		if e != nil {
			return nil, e
		}
		left = CombinationOperator{Op: '&', Left: left, Right: right}
	}
	return left, nil
}
func (p *combinationState) parseUnary() (Node, error) {
	p.skipSpace()
	if p.pos < len(p.input) && p.input[p.pos] == '!' {
		p.pos++
		n, e := p.parseUnary()
		if e != nil {
			return nil, e
		}
		return CombinationNot{Child: n}, nil
	}
	if p.pos < len(p.input) && p.input[p.pos] == '(' {
		p.pos++
		n, e := p.parseOr()
		if e != nil {
			return nil, e
		}
		p.skipSpace()
		if p.pos >= len(p.input) || p.input[p.pos] != ')' {
			return nil, fmt.Errorf("missing combination close")
		}
		p.pos++
		return n, nil
	}
	start := p.pos
	var id uint64
	for p.pos < len(p.input) && p.input[p.pos] >= '0' && p.input[p.pos] <= '9' {
		digit := uint64(p.input[p.pos] - '0')
		if id > (1<<32-1-digit)/10 {
			return nil, fmt.Errorf("combination id overflow")
		}
		id = id*10 + digit
		if id > 1<<32-1 {
			return nil, fmt.Errorf("combination id overflow")
		}
		p.pos++
	}
	if p.pos == start {
		return nil, fmt.Errorf("expected combination operand")
	}
	return CombinationOperand{ID: uint32(id)}, nil
}

func (p *combinationState) skipSpace() {
	for p.pos < len(p.input) && (p.input[p.pos] == ' ' || p.input[p.pos] == '\t' || p.input[p.pos] == '\n' || p.input[p.pos] == '\r') {
		p.pos++
	}
}
