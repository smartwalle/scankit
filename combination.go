package scankit

import (
	"fmt"
	"strconv"
)

type combinationNodeKind uint8

const (
	combinationOperand combinationNodeKind = iota
	combinationNot
	combinationAnd
	combinationOr
)

// combinationNode 是组合规则使用的紧凑布尔 AST。operand 初始保存公开表达式 id，并在
// 所有表达式编译完成后解析为表达式索引。
type combinationNode struct {
	kind     combinationNodeKind
	operand  uint32
	children []*combinationNode
}

type combinationProgram struct {
	expressionIndex uint32
	root            *combinationNode
}

type combinationParser struct {
	pattern string
	pos     int
}

func parseCombination(pattern string) (*combinationNode, error) {
	parser := combinationParser{pattern: pattern}
	node, err := parser.parseOr()
	if err != nil {
		return nil, err
	}
	parser.skipSpace()
	if node == nil || parser.pos != len(pattern) {
		return nil, parser.errorf("expected combination operand or operator")
	}
	return node, nil
}

func (p *combinationParser) parseOr() (*combinationNode, error) {
	left, err := p.parseAnd()
	if err != nil {
		return nil, err
	}
	for {
		p.skipSpace()
		if !p.consume('|') {
			return left, nil
		}
		right, err := p.parseAnd()
		if err != nil {
			return nil, err
		}
		left = &combinationNode{kind: combinationOr, children: []*combinationNode{left, right}}
	}
}

func (p *combinationParser) parseAnd() (*combinationNode, error) {
	left, err := p.parseUnary()
	if err != nil {
		return nil, err
	}
	for {
		p.skipSpace()
		if !p.consume('&') {
			return left, nil
		}
		right, err := p.parseUnary()
		if err != nil {
			return nil, err
		}
		left = &combinationNode{kind: combinationAnd, children: []*combinationNode{left, right}}
	}
}

func (p *combinationParser) parseUnary() (*combinationNode, error) {
	p.skipSpace()
	if p.consume('!') {
		child, err := p.parseUnary()
		if err != nil {
			return nil, err
		}
		return &combinationNode{kind: combinationNot, children: []*combinationNode{child}}, nil
	}
	if p.consume('(') {
		node, err := p.parseOr()
		if err != nil {
			return nil, err
		}
		p.skipSpace()
		if !p.consume(')') {
			return nil, p.errorf("expected ')'")
		}
		return node, nil
	}
	start := p.pos
	for p.pos < len(p.pattern) && p.pattern[p.pos] >= '0' && p.pattern[p.pos] <= '9' {
		p.pos++
	}
	if start == p.pos {
		return nil, p.errorf("expected expression id")
	}
	value, err := strconv.ParseUint(p.pattern[start:p.pos], 10, 32)
	if err != nil {
		return nil, p.errorf("invalid expression id")
	}
	return &combinationNode{kind: combinationOperand, operand: uint32(value)}, nil
}

func (p *combinationParser) skipSpace() {
	for p.pos < len(p.pattern) && (p.pattern[p.pos] == ' ' || p.pattern[p.pos] == '\t' || p.pattern[p.pos] == '\n' || p.pattern[p.pos] == '\r') {
		p.pos++
	}
}

func (p *combinationParser) consume(want byte) bool {
	if p.pos < len(p.pattern) && p.pattern[p.pos] == want {
		p.pos++
		return true
	}
	return false
}

func (p *combinationParser) errorf(format string, arguments ...any) error {
	return fmt.Errorf("invalid combination at byte %d: "+format, append([]any{p.pos}, arguments...)...)
}

func resolveCombinationOperands(node *combinationNode, expressionIndex uint32, ids map[uint32]uint32, expressions []compiledExpression) error {
	if node == nil {
		return fmt.Errorf("%w: empty combination", ErrInvalidCombination)
	}
	if node.kind == combinationOperand {
		index, ok := ids[node.operand]
		if !ok || index == expressionIndex {
			return fmt.Errorf("%w: unknown or self-referential expression id %d", ErrInvalidCombination, node.operand)
		}
		if expressions[index].flags&CompileCombination != 0 {
			return fmt.Errorf("%w: combinations cannot reference combinations", ErrInvalidCombination)
		}
		node.operand = index
		return nil
	}
	for _, child := range node.children {
		if err := resolveCombinationOperands(child, expressionIndex, ids, expressions); err != nil {
			return err
		}
	}
	return nil
}

// markCombinationOperands 记录计算组合规则所需的生产者表达式。没有组合消费者的 Quiet
// 生产者不会产生可观察效果，可从扫描器热路径中移除。
func markCombinationOperands(node *combinationNode, needed []bool) {
	if node == nil {
		return
	}
	if node.kind == combinationOperand {
		needed[node.operand] = true
		return
	}
	for _, child := range node.children {
		markCombinationOperands(child, needed)
	}
}

func (node *combinationNode) evaluate(matched []bool) bool {
	switch node.kind {
	case combinationOperand:
		return matched[node.operand]
	case combinationNot:
		return !node.children[0].evaluate(matched)
	case combinationAnd:
		return node.children[0].evaluate(matched) && node.children[1].evaluate(matched)
	case combinationOr:
		return node.children[0].evaluate(matched) || node.children[1].evaluate(matched)
	default:
		return false
	}
}

// appendCombinationEvents 应用与普通事件相同的约束可见性规则，记录成功的操作数表达式，
// 并在组合条件每次从假变真时产生事件。
func (scanner *Scanner) appendCombinationEvents(events *[]scanEvent, seen, active []bool, end uint64) {
	if len(scanner.combinations) == 0 {
		return
	}
	for _, event := range *events {
		expression := scanner.expressions[event.expressionIndex]
		if expression.flags&CompileCombination == 0 && expression.constraint.accepts(event.match) {
			seen[event.expressionIndex] = true
		}
	}
	for _, combination := range scanner.combinations {
		expression := scanner.expressions[combination.expressionIndex]
		on := combination.root.evaluate(seen)
		visible := on && expression.constraint.accepts(Match{To: end})
		if visible && !active[combination.expressionIndex] {
			*events = append(*events, scanEvent{
				match:           Match{Id: expression.id, To: end},
				expressionIndex: combination.expressionIndex,
			})
		}
		active[combination.expressionIndex] = visible
	}
}
