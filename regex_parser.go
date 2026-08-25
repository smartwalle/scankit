package scankit

import (
	"fmt"
	"strconv"
)

const unboundedRepeat = -1

// regexInlineExtended 是解析器内部的 (?x) 状态，不是公开 CompileFlag，并会在解析表达式时完全下沉。
const regexInlineExtended CompileFlag = 1 << 31

type regexNodeKind uint8

const (
	regexEmpty regexNodeKind = iota
	regexLiteral
	regexClass
	regexDot
	regexConcat
	regexAlternate
	regexRepeat
	regexAnchorStart
	regexAnchorEnd
	regexAbsoluteStart
	regexAbsoluteEnd
	regexEndBeforeFinalNewline
	regexLineBreak
	regexWordBoundary
	regexNotWordBoundary
)

type regexNode struct {
	kind     regexNodeKind
	offset   int
	flags    CompileFlag
	literal  byte
	class    byteClass
	children []*regexNode
	min      int
	max      int
}

// byteClass 用四个 64 位字保存字节成员关系。解析器和 NFA 均使用字节语义。
type byteClass [4]uint64

// byteClassCardinality 是 byteClass 可表示的不同值数量。容量相关的分派结构应依赖该表示，
// 避免在调用处重复字节域大小。
const byteClassCardinality = len(byteClass{}) * 64

func (c *byteClass) add(value byte) {
	c[value>>6] |= uint64(1) << (value & 63)
}

func (c *byteClass) remove(value byte) {
	c[value>>6] &^= uint64(1) << (value & 63)
}

func (c *byteClass) addRange(from, to byte) {
	for value := from; ; value++ {
		c.add(value)
		if value == to {
			return
		}
	}
}

func (c *byteClass) merge(other byteClass) {
	for index := range c {
		c[index] |= other[index]
	}
}

func (c *byteClass) invert() {
	for index := range c {
		c[index] = ^c[index]
	}
}

func (c byteClass) contains(value byte) bool {
	return c[value>>6]&(uint64(1)<<(value&63)) != 0
}

func allBytes() byteClass {
	return byteClass{^uint64(0), ^uint64(0), ^uint64(0), ^uint64(0)}
}

func classRange(from, to byte) byteClass {
	var class byteClass
	class.addRange(from, to)
	return class
}

type regexParseError struct {
	offset int
	reason string
	cause  error
}

func (e *regexParseError) Error() string {
	return fmt.Sprintf("regex syntax error at byte %d: %s", e.offset, e.reason)
}

func (e *regexParseError) Unwrap() error {
	return e.cause
}

type regexParser struct {
	pattern string
	pos     int
	flags   CompileFlag
}

func parseRegex(pattern string) (*regexNode, error) {
	return parseRegexWithFlags(pattern, 0)
}

func parseRegexWithFlags(pattern string, flags CompileFlag) (*regexNode, error) {
	parser := regexParser{pattern: pattern, flags: flags}
	node, err := parser.parseAlternate()
	if err != nil {
		return nil, err
	}
	if parser.pos != len(pattern) {
		return nil, parser.errorf("unexpected %q", pattern[parser.pos])
	}
	return node, nil
}

func (p *regexParser) parseAlternate() (*regexNode, error) {
	first, err := p.parseConcat()
	if err != nil {
		return nil, err
	}
	children := []*regexNode{first}
	for p.consume('|') {
		next, err := p.parseConcat()
		if err != nil {
			return nil, err
		}
		children = append(children, next)
	}
	if len(children) == 1 {
		return first, nil
	}
	return &regexNode{kind: regexAlternate, offset: children[0].offset, flags: p.flags, children: children}, nil
}

func (p *regexParser) parseConcat() (*regexNode, error) {
	p.skipExtendedWhitespace()
	children := make([]*regexNode, 0, 4)
	start := p.pos
	for {
		p.skipExtendedWhitespace()
		if p.pos == len(p.pattern) || p.pattern[p.pos] == ')' || p.pattern[p.pos] == '|' {
			break
		}
		node, err := p.parseRepeat()
		if err != nil {
			return nil, err
		}
		children = append(children, node)
	}
	switch len(children) {
	case 0:
		return &regexNode{kind: regexEmpty, offset: start, flags: p.flags}, nil
	case 1:
		return children[0], nil
	default:
		return &regexNode{kind: regexConcat, offset: start, flags: p.flags, children: children}, nil
	}
}

func (p *regexParser) parseRepeat() (*regexNode, error) {
	node, err := p.parseAtom()
	if err != nil {
		return nil, err
	}
	p.skipExtendedWhitespace()
	if p.pos == len(p.pattern) {
		return node, nil
	}

	quantifierOffset := p.pos
	minimum, maximum, quantified := 0, 0, false
	switch p.pattern[p.pos] {
	case '?':
		p.pos++
		minimum, maximum, quantified = 0, 1, true
	case '*':
		p.pos++
		minimum, maximum, quantified = 0, unboundedRepeat, true
	case '+':
		p.pos++
		minimum, maximum, quantified = 1, unboundedRepeat, true
	case '{':
		var quantifierErr error
		minimum, maximum, quantified, quantifierErr = p.parseBraceQuantifier()
		if quantifierErr != nil {
			return nil, quantifierErr
		}
	}
	if !quantified {
		return node, nil
	}
	if p.pos < len(p.pattern) && p.pattern[p.pos] == '?' {
		// 贪婪性不会改变 NFA 接受的语言。此处保留语法，捕获和优先级语义后续再实现。
		p.pos++
	}
	if p.pos < len(p.pattern) && p.pattern[p.pos] == '+' {
		return nil, p.unsupportedExpressionf(p.pos, "possessive quantifiers are not supported")
	}
	if p.pos < len(p.pattern) && isQuantifierStart(p.pattern[p.pos]) {
		return nil, &regexParseError{offset: p.pos, reason: "multiple repeat operators"}
	}
	return &regexNode{kind: regexRepeat, offset: quantifierOffset, flags: p.flags, children: []*regexNode{node}, min: minimum, max: maximum}, nil
}

func (p *regexParser) parseBraceQuantifier() (int, int, bool, error) {
	start := p.pos
	p.pos++ // '{'
	minimum, ok := p.parseDecimal()
	if !ok {
		return 0, 0, false, &regexParseError{offset: start, reason: "repeat lower bound is required"}
	}
	if p.consume('}') {
		return minimum, minimum, true, nil
	}
	if !p.consume(',') {
		return 0, 0, false, p.errorf("expected ',' or '}' in repeat")
	}
	if p.consume('}') {
		return minimum, unboundedRepeat, true, nil
	}
	maximum, ok := p.parseDecimal()
	if !ok {
		return 0, 0, false, p.errorf("repeat upper bound is required")
	}
	if !p.consume('}') {
		return 0, 0, false, p.errorf("expected '}' in repeat")
	}
	if maximum < minimum {
		return 0, 0, false, &regexParseError{offset: start, reason: "repeat upper bound is smaller than lower bound"}
	}
	return minimum, maximum, true, nil
}

func (p *regexParser) parseDecimal() (int, bool) {
	start := p.pos
	for p.pos < len(p.pattern) && p.pattern[p.pos] >= '0' && p.pattern[p.pos] <= '9' {
		p.pos++
	}
	if start == p.pos {
		return 0, false
	}
	value, err := strconv.ParseUint(p.pattern[start:p.pos], 10, 31)
	if err != nil {
		p.pos = start
		return 0, false
	}
	return int(value), true
}

func (p *regexParser) parseAtom() (*regexNode, error) {
	if p.pos == len(p.pattern) {
		return nil, p.errorf("expected expression")
	}
	offset := p.pos
	current := p.pattern[p.pos]
	p.pos++
	switch current {
	case '(':
		groupFlags := p.flags
		if p.consume('?') {
			switch {
			case p.consume(':'):
				// 非捕获分组。
			case p.consume('<'):
				if p.pos < len(p.pattern) && (p.pattern[p.pos] == '=' || p.pattern[p.pos] == '!') {
					return nil, p.unsupportedExpressionf(offset, "lookbehind assertions are not supported")
				}
				if err := p.consumeGroupName('>'); err != nil {
					return nil, err
				}
			case p.consume('P'):
				if !p.consume('<') {
					return nil, p.unsupportedExpressionf(offset, "unsupported group extension")
				}
				if err := p.consumeGroupName('>'); err != nil {
					return nil, err
				}
			case p.consume('#'):
				commentStart := p.pos
				for p.pos < len(p.pattern) && p.pattern[p.pos] != ')' {
					p.pos++
				}
				if !p.consume(')') {
					return nil, &regexParseError{offset: commentStart, reason: "unterminated comment group"}
				}
				return &regexNode{kind: regexEmpty, offset: offset, flags: p.flags}, nil
			default:
				return p.parseInlineFlagGroup(offset)
			}
		}
		node, err := p.parseAlternate()
		if err != nil {
			return nil, err
		}
		if !p.consume(')') {
			return nil, p.errorf("expected ')'")
		}
		p.flags = groupFlags
		return node, nil
	case '[':
		class, err := p.parseClass()
		if err != nil {
			return nil, err
		}
		return &regexNode{kind: regexClass, offset: offset, flags: p.flags, class: class}, nil
	case '.':
		return &regexNode{kind: regexDot, offset: offset, flags: p.flags}, nil
	case '^':
		return &regexNode{kind: regexAnchorStart, offset: offset, flags: p.flags}, nil
	case '$':
		return &regexNode{kind: regexAnchorEnd, offset: offset, flags: p.flags}, nil
	case '\\':
		if p.pos < len(p.pattern) {
			switch p.pattern[p.pos] {
			case 'Q':
				p.pos++
				node := p.parseQuotedLiteral(offset)
				setRegexNodeFlags(node, p.flags)
				return node, nil
			case 'A':
				p.pos++
				return &regexNode{kind: regexAbsoluteStart, offset: offset, flags: p.flags}, nil
			case 'z':
				p.pos++
				return &regexNode{kind: regexAbsoluteEnd, offset: offset, flags: p.flags}, nil
			case 'Z':
				p.pos++
				return &regexNode{kind: regexEndBeforeFinalNewline, offset: offset, flags: p.flags}, nil
			case 'R':
				p.pos++
				return &regexNode{kind: regexLineBreak, offset: offset, flags: p.flags}, nil
			case 'N':
				p.pos++
				if p.pos < len(p.pattern) && p.pattern[p.pos] == '{' {
					return nil, p.unsupportedExpressionf(offset, "named character escapes are not supported")
				}
				return &regexNode{kind: regexDot, offset: offset, flags: p.flags &^ CompileDotAll}, nil
			case 'b':
				p.pos++
				return &regexNode{kind: regexWordBoundary, offset: offset, flags: p.flags}, nil
			case 'B':
				p.pos++
				return &regexNode{kind: regexNotWordBoundary, offset: offset, flags: p.flags}, nil
			}
		}
		class, literal, isClass, err := p.parseEscape()
		if err != nil {
			return nil, err
		}
		if isClass {
			return &regexNode{kind: regexClass, offset: offset, flags: p.flags, class: class}, nil
		}
		return &regexNode{kind: regexLiteral, offset: offset, flags: p.flags, literal: literal}, nil
	case ')', '|', '*', '+', '?', '{':
		return nil, &regexParseError{offset: offset, reason: "expected expression"}
	default:
		return &regexNode{kind: regexLiteral, offset: offset, flags: p.flags, literal: current}, nil
	}
}

func (p *regexParser) parseInlineFlagGroup(offset int) (*regexNode, error) {
	set, unset, ok, err := p.parseInlineFlags()
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, p.unsupportedExpressionf(offset, "unsupported group extension")
	}
	flags := p.flags | set
	flags &^= unset
	if p.consume(':') {
		outer := p.flags
		p.flags = flags
		node, err := p.parseAlternate()
		p.flags = outer
		if err != nil {
			return nil, err
		}
		if !p.consume(')') {
			return nil, p.errorf("expected ')'")
		}
		return node, nil
	}
	if !p.consume(')') {
		return nil, p.errorf("expected ':' or ')' after inline flags")
	}
	p.flags = flags
	return &regexNode{kind: regexEmpty, offset: offset, flags: flags}, nil
}

func (p *regexParser) parseInlineFlags() (CompileFlag, CompileFlag, bool, error) {
	var set, unset CompileFlag
	clearing := false
	seen := false
	cleared := false
	for p.pos < len(p.pattern) {
		value := p.pattern[p.pos]
		if value == '-' {
			if clearing {
				return 0, 0, false, p.errorf("duplicate '-' in inline flags")
			}
			clearing = true
			p.pos++
			continue
		}
		var flag CompileFlag
		switch value {
		case 'i':
			flag = CompileCaseless
		case 'm':
			flag = CompileMultiline
		case 's':
			flag = CompileDotAll
		case 'x':
			flag = regexInlineExtended
		default:
			if value == ':' || value == ')' {
				if !seen {
					return 0, 0, false, nil
				}
				if clearing && !cleared {
					return 0, 0, false, p.errorf("inline '-' must be followed by a flag")
				}
				return set, unset, true, nil
			}
			if !seen {
				return 0, 0, false, nil
			}
			return 0, 0, false, p.errorf("unsupported inline flag %q", value)
		}
		seen = true
		p.pos++
		if clearing {
			if set&flag != 0 {
				return 0, 0, false, p.errorf("inline flag %q is both set and cleared", value)
			}
			unset |= flag
			cleared = true
		} else {
			if unset&flag != 0 {
				return 0, 0, false, p.errorf("inline flag %q is both set and cleared", value)
			}
			set |= flag
		}
	}
	return set, unset, seen, nil
}

func (p *regexParser) skipExtendedWhitespace() {
	if p.flags&regexInlineExtended == 0 {
		return
	}
	for p.pos < len(p.pattern) {
		switch p.pattern[p.pos] {
		case ' ', '\t', '\n', '\r', '\f':
			p.pos++
		case '#':
			p.pos++
			for p.pos < len(p.pattern) && p.pattern[p.pos] != '\n' {
				p.pos++
			}
		default:
			return
		}
	}
}

// parseQuotedLiteral 实现 \Q...\E 引用。未配对的 \Q 会一直引用到表达式末尾。
func (p *regexParser) parseQuotedLiteral(offset int) *regexNode {
	start := p.pos
	for p.pos < len(p.pattern) {
		if p.pattern[p.pos] == '\\' && p.pos+1 < len(p.pattern) && p.pattern[p.pos+1] == 'E' {
			end := p.pos
			p.pos += 2
			return regexLiteralSequence(offset, p.pattern[start:end])
		}
		p.pos++
	}
	return regexLiteralSequence(offset, p.pattern[start:])
}

func regexLiteralSequence(offset int, literal string) *regexNode {
	if len(literal) == 0 {
		return &regexNode{kind: regexEmpty, offset: offset}
	}
	if len(literal) == 1 {
		return &regexNode{kind: regexLiteral, offset: offset, literal: literal[0]}
	}
	children := make([]*regexNode, len(literal))
	for index := 0; index < len(literal); index++ {
		children[index] = &regexNode{kind: regexLiteral, offset: offset + index, literal: literal[index]}
	}
	return &regexNode{kind: regexConcat, offset: offset, children: children}
}

func setRegexNodeFlags(node *regexNode, flags CompileFlag) {
	if node == nil {
		return
	}
	node.flags = flags
	for _, child := range node.children {
		setRegexNodeFlags(child, flags)
	}
}

// consumeGroupName 接受常见格式的捕获组名称。此 API 不暴露捕获，因此名称仅影响解析兼容性，
// 被包围的表达式仍是普通分组节点。
func (p *regexParser) consumeGroupName(terminator byte) error {
	start := p.pos
	if p.pos == len(p.pattern) || !isGroupNameStart(p.pattern[p.pos]) {
		return &regexParseError{offset: start, reason: "capture name is required"}
	}
	p.pos++
	for p.pos < len(p.pattern) && isGroupNameContinue(p.pattern[p.pos]) {
		p.pos++
	}
	if !p.consume(terminator) {
		return &regexParseError{offset: p.pos, reason: "unterminated capture name"}
	}
	return nil
}

func isGroupNameStart(value byte) bool {
	return value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z' || value == '_'
}

func isGroupNameContinue(value byte) bool {
	return isGroupNameStart(value) || value >= '0' && value <= '9'
}

func (p *regexParser) parseClass() (byteClass, error) {
	classOffset := p.pos - 1
	negated := p.consume('^')
	var class byteClass
	empty := true
	for p.pos < len(p.pattern) && p.pattern[p.pos] != ']' {
		fromClass, from, fromIsClass, err := p.parseClassItem()
		if err != nil {
			return byteClass{}, err
		}
		empty = false
		if p.pos+1 < len(p.pattern) && p.pattern[p.pos] == '-' && p.pattern[p.pos+1] != ']' {
			p.pos++
			_, to, toIsClass, err := p.parseClassItem()
			if err != nil {
				return byteClass{}, err
			}
			if fromIsClass || toIsClass {
				return byteClass{}, &regexParseError{offset: p.pos, reason: "character-class range requires literal endpoints"}
			}
			if to < from {
				return byteClass{}, &regexParseError{offset: p.pos, reason: "character-class range is reversed"}
			}
			class.addRange(from, to)
			continue
		}
		if fromIsClass {
			class.merge(fromClass)
			continue
		}
		class.add(from)
	}
	if empty {
		return byteClass{}, &regexParseError{offset: classOffset, reason: "empty character class"}
	}
	if !p.consume(']') {
		return byteClass{}, p.errorf("unterminated character class")
	}
	if negated {
		class.invert()
	}
	return class, nil
}

func (p *regexParser) parseClassItem() (byteClass, byte, bool, error) {
	if p.pos == len(p.pattern) || p.pattern[p.pos] == ']' {
		return byteClass{}, 0, false, p.errorf("character-class item is required")
	}
	if p.pos+1 < len(p.pattern) && p.pattern[p.pos] == '[' && p.pattern[p.pos+1] == ':' {
		return p.parsePOSIXClass()
	}
	current := p.pattern[p.pos]
	p.pos++
	if current != '\\' {
		return byteClass{}, current, false, nil
	}
	class, literal, isClass, err := p.parseEscape()
	if err != nil {
		return byteClass{}, 0, false, err
	}
	return class, literal, isClass, nil
}

func (p *regexParser) parseEscape() (byteClass, byte, bool, error) {
	if p.pos == len(p.pattern) {
		return byteClass{}, 0, false, p.errorf("trailing escape")
	}
	escaped := p.pattern[p.pos]
	p.pos++
	if escaped == 'c' {
		if p.pos == len(p.pattern) {
			return byteClass{}, 0, false, p.unsupportedExpressionf(p.pos-1, "control escape requires an ASCII letter")
		}
		value, ok := asciiControlEscape(p.pattern[p.pos])
		if !ok {
			return byteClass{}, 0, false, p.unsupportedExpressionf(p.pos-1, "control escape requires an ASCII letter")
		}
		p.pos++
		return byteClass{}, value, false, nil
	}
	if escaped == '0' {
		value := byte(0)
		for range 3 {
			if p.pos == len(p.pattern) || !isOctalDigit(p.pattern[p.pos]) {
				break
			}
			value = value<<3 | (p.pattern[p.pos] - '0')
			p.pos++
		}
		return byteClass{}, value, false, nil
	}
	if escaped == 'x' {
		return p.parseHexEscape()
	}
	if escaped == 'o' {
		return p.parseBracedOctalEscape()
	}
	if isUnsupportedPCREByteEscape(escaped) {
		return byteClass{}, 0, false, p.unsupportedExpressionf(p.pos-1, "unsupported escape \\%c", escaped)
	}
	class, literal, isClass, err := classForEscape(escaped)
	if err != nil {
		return byteClass{}, 0, false, &regexParseError{offset: p.pos - 1, reason: err.Error()}
	}
	return class, literal, isClass, nil
}

func (p *regexParser) parseBracedOctalEscape() (byteClass, byte, bool, error) {
	value, next, err := parseBracedOctalEscape(p.pattern, p.pos, 0xff)
	if err != nil {
		return byteClass{}, 0, false, &regexParseError{offset: p.pos, reason: err.Error()}
	}
	p.pos = next
	return byteClass{}, byte(value), false, nil
}

func (p *regexParser) parseHexEscape() (byteClass, byte, bool, error) {
	if p.pos < len(p.pattern) && p.pattern[p.pos] == '{' {
		value, next, err := parseBracedHexEscape(p.pattern, p.pos, 0xff)
		if err != nil {
			return byteClass{}, 0, false, &regexParseError{offset: p.pos, reason: err.Error()}
		}
		p.pos = next
		return byteClass{}, byte(value), false, nil
	}
	if len(p.pattern)-p.pos < 2 {
		return byteClass{}, 0, false, p.errorf("hex escape requires two hexadecimal digits")
	}
	high, ok := hexadecimalValue(p.pattern[p.pos])
	if !ok {
		return byteClass{}, 0, false, p.errorf("hex escape requires hexadecimal digits")
	}
	low, ok := hexadecimalValue(p.pattern[p.pos+1])
	if !ok {
		return byteClass{}, 0, false, p.errorf("hex escape requires hexadecimal digits")
	}
	p.pos += 2
	return byteClass{}, high<<4 | low, false, nil
}

// parseBracedHexEscape 解析位于 start 的 {hex} 形式。调用方负责根据自身字符域指定上限：
// 字节正则为 0xff，UCP 为最大 Unicode 标量值。
func parseBracedHexEscape(pattern string, start int, maximum uint32) (uint32, int, error) {
	if start >= len(pattern) || pattern[start] != '{' {
		return 0, start, fmt.Errorf("expected '{' after hex escape")
	}
	position := start + 1
	if position == len(pattern) || pattern[position] == '}' {
		return 0, start, fmt.Errorf("braced hex escape requires hexadecimal digits")
	}
	var value uint32
	for position < len(pattern) && pattern[position] != '}' {
		digit, ok := hexadecimalValue(pattern[position])
		if !ok {
			return 0, start, fmt.Errorf("braced hex escape requires hexadecimal digits")
		}
		if value > (maximum-uint32(digit))/16 {
			return 0, start, fmt.Errorf("braced hex escape is out of range")
		}
		value = value*16 + uint32(digit)
		position++
	}
	if position == len(pattern) {
		return 0, start, fmt.Errorf("unterminated braced hex escape")
	}
	return value, position + 1, nil
}

// parseBracedOctalEscape 解析位于 start 的 {octal} 形式。它不接受没有花括号的 \o，
// 以免和本库的其他字面量/转义规则产生歧义。
func parseBracedOctalEscape(pattern string, start int, maximum uint32) (uint32, int, error) {
	if start >= len(pattern) || pattern[start] != '{' {
		return 0, start, fmt.Errorf("expected '{' after octal escape")
	}
	position := start + 1
	if position == len(pattern) || pattern[position] == '}' {
		return 0, start, fmt.Errorf("braced octal escape requires octal digits")
	}
	var value uint32
	for position < len(pattern) && pattern[position] != '}' {
		if !isOctalDigit(pattern[position]) {
			return 0, start, fmt.Errorf("braced octal escape requires octal digits")
		}
		digit := uint32(pattern[position] - '0')
		if value > (maximum-digit)/8 {
			return 0, start, fmt.Errorf("braced octal escape is out of range")
		}
		value = value*8 + digit
		position++
	}
	if position == len(pattern) {
		return 0, start, fmt.Errorf("unterminated braced octal escape")
	}
	return value, position + 1, nil
}

func hexadecimalValue(value byte) (byte, bool) {
	switch {
	case value >= '0' && value <= '9':
		return value - '0', true
	case value >= 'a' && value <= 'f':
		return value - 'a' + 10, true
	case value >= 'A' && value <= 'F':
		return value - 'A' + 10, true
	default:
		return 0, false
	}
}

func isOctalDigit(value byte) bool { return value >= '0' && value <= '7' }

// asciiControlEscape 将 ASCII 字母转换为对应的控制字符。为避免对不同正则方言的符号规则
// 产生歧义，当前只接受字母形式的 \cX。
func asciiControlEscape(value byte) (byte, bool) {
	if value >= 'a' && value <= 'z' {
		value -= 'a' - 'A'
	}
	if value < 'A' || value > 'Z' {
		return 0, false
	}
	return value & 0x1f, true
}

func (p *regexParser) parsePOSIXClass() (byteClass, byte, bool, error) {
	start := p.pos
	p.pos += 2 // "[:"
	negated := p.consume('^')
	nameStart := p.pos
	for p.pos < len(p.pattern) && p.pattern[p.pos] != ':' {
		p.pos++
	}
	if p.pos == nameStart || p.pos+1 >= len(p.pattern) || p.pattern[p.pos+1] != ']' {
		return byteClass{}, 0, false, &regexParseError{offset: start, reason: "unterminated POSIX character class"}
	}
	name := p.pattern[nameStart:p.pos]
	p.pos += 2 // ":]"
	class, ok := classForPOSIXName(name)
	if !ok {
		return byteClass{}, 0, false, &regexParseError{offset: start, reason: fmt.Sprintf("unknown POSIX character class %q", name)}
	}
	if negated {
		class.invert()
	}
	return class, 0, true, nil
}

func classForPOSIXName(name string) (byteClass, bool) {
	letters := classRange('a', 'z')
	letters.merge(classRange('A', 'Z'))
	digits := classRange('0', '9')
	word := letters
	word.merge(digits)
	word.add('_')
	space, _, _, _ := classForEscape('s')
	switch name {
	case "alnum":
		class := letters
		class.merge(digits)
		return class, true
	case "alpha":
		return letters, true
	case "ascii":
		return classRange(0, 0x7f), true
	case "blank":
		var class byteClass
		class.add(' ')
		class.add('\t')
		return class, true
	case "cntrl":
		class := classRange(0, 0x1f)
		class.add(0x7f)
		return class, true
	case "digit":
		return digits, true
	case "graph":
		return classRange(0x21, 0x7e), true
	case "lower":
		return classRange('a', 'z'), true
	case "print":
		return classRange(0x20, 0x7e), true
	case "punct":
		class := classRange(0x21, 0x7e)
		for index := range class {
			class[index] &^= letters[index] | digits[index]
		}
		return class, true
	case "space":
		return space, true
	case "upper":
		return classRange('A', 'Z'), true
	case "word":
		return word, true
	case "xdigit":
		class := digits
		class.merge(classRange('a', 'f'))
		class.merge(classRange('A', 'F'))
		return class, true
	default:
		return byteClass{}, false
	}
}

func classForEscape(escaped byte) (byteClass, byte, bool, error) {
	switch escaped {
	case 'd':
		return classRange('0', '9'), 0, true, nil
	case 'D':
		class := classRange('0', '9')
		class.invert()
		return class, 0, true, nil
	case 'w':
		class := classRange('a', 'z')
		class.merge(classRange('A', 'Z'))
		class.merge(classRange('0', '9'))
		class.add('_')
		return class, 0, true, nil
	case 'W':
		class, _, _, _ := classForEscape('w')
		class.invert()
		return class, 0, true, nil
	case 's':
		var class byteClass
		for _, value := range []byte{' ', '\t', '\n', '\r', '\f', '\v'} {
			class.add(value)
		}
		return class, 0, true, nil
	case 'S':
		class, _, _, _ := classForEscape('s')
		class.invert()
		return class, 0, true, nil
	case 'h':
		var class byteClass
		class.add(' ')
		class.add('\t')
		return class, 0, true, nil
	case 'H':
		class, _, _, _ := classForEscape('h')
		class.invert()
		return class, 0, true, nil
	case 'v':
		var class byteClass
		for _, value := range []byte{'\n', '\r', '\f', '\v'} {
			class.add(value)
		}
		return class, 0, true, nil
	case 'V':
		class, _, _, _ := classForEscape('v')
		class.invert()
		return class, 0, true, nil
	case 'n':
		return byteClass{}, '\n', false, nil
	case 'a':
		return byteClass{}, '\a', false, nil
	case 'e':
		return byteClass{}, 0x1b, false, nil
	case 'r':
		return byteClass{}, '\r', false, nil
	case 't':
		return byteClass{}, '\t', false, nil
	case 'f':
		return byteClass{}, '\f', false, nil
	case 'b', 'B':
		return byteClass{}, 0, false, fmt.Errorf("word-boundary assertion is only valid outside a character class")
	default:
		return byteClass{}, escaped, false, nil
	}
}

// isUnsupportedPCREByteEscape 拒绝当前字节解析器无法按既定语义执行的转义形式。将其当作
// 字面量会静默改变规则语言，可能漏检敏感数据。受支持的 UCP 属性转义会在选择字节解析器前，
// 由专用 UTF-8/UCP 编译器处理。
func isUnsupportedPCREByteEscape(value byte) bool {
	switch value {
	case 'd', 'D', 'w', 'W', 's', 'S', 'h', 'H', 'v', 'V', 'a', 'e', 'n', 'r', 't', 'f':
		return false
	}
	return value >= '0' && value <= '9' || value >= 'A' && value <= 'Z' || value >= 'a' && value <= 'z'
}

func (p *regexParser) consume(want byte) bool {
	if p.pos < len(p.pattern) && p.pattern[p.pos] == want {
		p.pos++
		return true
	}
	return false
}

func (p *regexParser) errorf(format string, arguments ...any) error {
	return &regexParseError{offset: p.pos, reason: fmt.Sprintf(format, arguments...)}
}

func (p *regexParser) unsupportedExpressionf(offset int, format string, arguments ...any) error {
	return &regexParseError{offset: offset, reason: fmt.Sprintf(format, arguments...), cause: ErrUnsupportedExpression}
}

func isQuantifierStart(value byte) bool {
	return value == '?' || value == '*' || value == '+' || value == '{'
}
