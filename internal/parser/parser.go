package parser

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// Parse 将 pattern 解析为 AST。它只负责语法，不执行匹配。
func Parse(pattern string) (Node, error) {
	extended := false
	var globalSet, globalClear GroupFlag
	for strings.HasPrefix(pattern, "(?") {
		close := strings.IndexByte(pattern, ')')
		if close < 0 {
			break
		}
		body := pattern[2:close]
		if body == "" || strings.ContainsAny(body, ":<>=!'(") {
			break
		}
		enable, changed, valid := true, false, true
		for _, value := range body {
			switch value {
			case '-':
				enable = false
			case 'i', 's', 'm', 'x':
				changed = true
				var flag GroupFlag
				switch value {
				case 'i':
					flag = GroupFlagCaseless
				case 's':
					flag = GroupFlagDotAll
				case 'm':
					flag = GroupFlagMultiline
				case 'x':
					flag = GroupFlagExtended
				}
				if enable {
					globalSet |= flag
					globalClear &^= flag
				} else {
					globalClear |= flag
					globalSet &^= flag
				}
				if value == 'x' {
					extended = enable
				}
			default:
				valid = false
			}
		}
		if !changed || !valid {
			break
		}
		pattern = pattern[close+1:]
	}
	node, err := ParseWithExtended(pattern, extended)
	if err != nil {
		return nil, err
	}
	globalSet &^= GroupFlagExtended
	globalClear &^= GroupFlagExtended
	if globalSet != 0 || globalClear != 0 {
		return Group{Child: node, SetFlags: globalSet, ClearFlags: globalClear}, nil
	}
	return node, nil
}

// ParseMany 按输入顺序解析多个表达式，并在首个错误处返回索引和原始错误。
func ParseMany(patterns []string) ([]Node, error) {
	if patterns == nil {
		return nil, nil
	}
	out := make([]Node, len(patterns))
	for i, pattern := range patterns {
		node, err := Parse(pattern)
		if err != nil {
			return nil, fmt.Errorf("expression %d: %w", i, err)
		}
		out[i] = node
	}
	return out, nil
}

// ParseWithExtended 在指定的扩展模式下解析表达式。
func ParseWithExtended(pattern string, extended bool) (Node, error) {
	p := &state{input: []byte(pattern), names: make(map[string]int), extended: extended}
	node, err := p.parseAlternation()
	if err != nil {
		return nil, err
	}
	if p.pos != len(p.input) {
		return nil, p.errorf("unexpected character %q", p.input[p.pos])
	}
	return node, nil
}

type state struct {
	input         []byte
	pos, captures int
	names         map[string]int
	extended      bool
}

func (p *state) errorf(format string, args ...any) error {
	return &ParseError{Position: p.pos, Message: fmt.Sprintf(format, args...)}
}

func (p *state) parseCaptureName() (string, error) {
	switch p.peek() {
	case '<':
		p.pos++
		return p.parseCaptureNameBody('>')
	case '\'':
		p.pos++
		return p.parseCaptureNameBody('\'')
	default:
		return "", p.errorf("named reference requires delimiters")
	}
}

func (p *state) parseCaptureNameBody(close byte) (string, error) {
	start := p.pos
	for p.pos < len(p.input) && p.peek() != close {
		value := p.peek()
		valid := value == '_' || value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z' || value >= '0' && value <= '9'
		if !valid {
			return "", p.errorf("invalid capture name")
		}
		p.pos++
	}
	if p.pos == start || p.peek() != close {
		return "", p.errorf("invalid capture name")
	}
	name := string(p.input[start:p.pos])
	p.pos++
	return name, nil
}
func (p *state) peek() byte {
	if p.pos >= len(p.input) {
		return 0
	}
	return p.input[p.pos]
}
func (p *state) parseAlternation() (Node, error) {
	options := []Node{}
	for {
		p.skipExtended()
		n, err := p.parseSequence()
		if err != nil {
			return nil, err
		}
		options = append(options, n)
		p.skipExtended()
		if p.peek() != '|' {
			break
		}
		p.pos++
	}
	if len(options) == 1 {
		return options[0], nil
	}
	return Alternation{Options: options}, nil
}
func (p *state) parseSequence() (Node, error) {
	items := []Node{}
	for {
		p.skipExtended()
		if p.pos >= len(p.input) || p.peek() == ')' || p.peek() == '|' {
			break
		}
		n, err := p.parseAtom()
		if err != nil {
			return nil, err
		}
		items = append(items, n)
	}
	if len(items) == 0 {
		return Sequence{}, nil
	}
	if len(items) == 1 {
		return items[0], nil
	}
	return Sequence{Elements: items}, nil
}

func (p *state) skipExtended() {
	if !p.extended {
		return
	}
	for p.pos < len(p.input) {
		switch p.input[p.pos] {
		case ' ', '\t', '\n', '\r', '\f':
			p.pos++
		case '#':
			for p.pos < len(p.input) && p.input[p.pos] != '\n' && p.input[p.pos] != '\r' {
				p.pos++
			}
		default:
			return
		}
	}
}
func (p *state) parseAtom() (Node, error) {
	var n Node
	switch p.peek() {
	case '(':
		p.pos++
		if p.peek() == '?' && p.pos+1 < len(p.input) && p.input[p.pos+1] == '(' {
			p.pos += 2
			index := 0
			if p.peek() == '<' || p.peek() == '\'' {
				name, err := p.parseCaptureName()
				if err != nil {
					return nil, err
				}
				var ok bool
				index, ok = p.names[name]
				if !ok {
					return nil, p.errorf("unknown conditional reference %q", name)
				}
			} else {
				start := p.pos
				for p.pos < len(p.input) && p.input[p.pos] >= '0' && p.input[p.pos] <= '9' {
					index = index*10 + int(p.input[p.pos]-'0')
					p.pos++
				}
				if p.pos == start {
					return nil, p.errorf("invalid conditional reference")
				}
			}
			if p.peek() != ')' {
				return nil, p.errorf("invalid conditional reference")
			}
			p.pos++
			yes, err := p.parseSequence()
			if err != nil {
				return nil, err
			}
			var no Node = Sequence{}
			if p.peek() == '|' {
				p.pos++
				no, err = p.parseSequence()
				if err != nil {
					return nil, err
				}
			}
			if p.peek() != ')' {
				return nil, p.errorf("missing conditional close")
			}
			p.pos++
			n = Conditional{Index: index, Yes: yes, No: no}
			break
		}
		if p.peek() == '*' {
			p.pos++
			start := p.pos
			for p.pos < len(p.input) && p.input[p.pos] != ')' {
				p.pos++
			}
			if p.pos >= len(p.input) {
				return nil, p.errorf("missing control verb close")
			}
			name := string(p.input[start:p.pos])
			p.pos++
			if name == "" {
				return nil, p.errorf("empty control verb")
			}
			return ControlVerb{Name: name}, nil
		}
		atomic := false
		capture := true
		captureName := ""
		var lookaround LookaroundKind
		var setFlags, clearFlags GroupFlag
		previousExtended := p.extended
		if set, clear, scoped, err := p.parseScopedFlags(); err != nil {
			return nil, err
		} else if scoped {
			capture = false
			setFlags, clearFlags = set, clear
		} else if p.peek() == '?' && p.pos+1 < len(p.input) {
			switch p.input[p.pos+1] {
			case '#':
				p.pos += 2
				for p.pos < len(p.input) && p.peek() != ')' {
					p.pos++
				}
				if p.pos >= len(p.input) {
					return nil, p.errorf("missing comment close")
				}
				p.pos++
				return Literal{Value: nil}, nil
			case '>':
				atomic = true
				p.pos += 2
			case ':':
				capture = false
				p.pos += 2
			case '=':
				lookaround = Lookahead
				capture = false
				p.pos += 2
			case '!':
				lookaround = NegativeLookahead
				capture = false
				p.pos += 2
			case '<':
				if p.pos+2 < len(p.input) && (p.input[p.pos+2] == '=' || p.input[p.pos+2] == '!') {
					if p.input[p.pos+2] == '=' {
						lookaround = Lookbehind
					} else {
						lookaround = NegativeLookbehind
					}
					capture = false
					p.pos += 3
				} else {
					p.pos += 2
					var err error
					captureName, err = p.parseCaptureNameBody('>')
					if err != nil {
						return nil, err
					}
				}
			case '\'':
				p.pos += 2
				var err error
				captureName, err = p.parseCaptureNameBody('\'')
				if err != nil {
					return nil, err
				}
			case 'P':
				if p.pos+2 < len(p.input) && p.input[p.pos+2] == '=' {
					p.pos += 3
					start := p.pos
					for p.pos < len(p.input) && p.peek() != ')' {
						p.pos++
					}
					if p.pos == start {
						return nil, p.errorf("invalid named backreference")
					}
					name := string(p.input[start:p.pos])
					index, ok := p.names[name]
					if !ok {
						return nil, p.errorf("unknown named backreference %q", name)
					}
					p.pos++
					return Backreference{Index: index}, nil
				}
				if p.pos+2 >= len(p.input) || p.input[p.pos+2] != '<' {
					return nil, p.errorf("invalid named capture")
				}
				p.pos += 3
				var err error
				captureName, err = p.parseCaptureNameBody('>')
				if err != nil {
					return nil, err
				}
			default:
				return nil, p.errorf("unsupported group extension")
			}
		}
		if setFlags&GroupFlagExtended != 0 {
			p.extended = true
		}
		if clearFlags&GroupFlagExtended != 0 {
			p.extended = false
		}
		captureIndex := 0
		if capture {
			p.captures++
			captureIndex = p.captures
			if captureName != "" {
				if _, exists := p.names[captureName]; exists {
					return nil, p.errorf("duplicate capture name %q", captureName)
				}
				p.names[captureName] = captureIndex
			}
		}
		child, err := p.parseAlternation()
		p.extended = previousExtended
		if err != nil {
			return nil, err
		}
		if p.peek() != ')' {
			return nil, p.errorf("missing closing parenthesis")
		}
		p.pos++
		if lookaround != 0 {
			n = Lookaround{Child: child, Kind: lookaround}
		} else {
			n = Group{Child: child, Atomic: atomic, Capture: captureIndex, SetFlags: setFlags, ClearFlags: clearFlags}
		}
	case '[':
		var err error
		n, err = p.parseClass()
		if err != nil {
			return nil, err
		}
	case '^':
		p.pos++
		n = Assertion{Kind: Begin}
	case '$':
		p.pos++
		n = Assertion{Kind: End}
	case '.':
		p.pos++
		n = Any{}
	case '\\':
		var err error
		n, err = p.parseEscape()
		if err != nil {
			return nil, err
		}
	default:
		p.pos++
		n = Literal{Value: []byte{p.input[p.pos-1]}}
	}
	if p.pos < len(p.input) {
		switch p.peek() {
		case '*':
			p.pos++
			n = Repeat{Child: n, Max: -1, Greedy: true}
		case '+':
			p.pos++
			n = Repeat{Child: n, Min: 1, Max: -1, Greedy: true}
		case '?':
			p.pos++
			n = Repeat{Child: n, Max: 1, Greedy: true}
		case '{':
			var err error
			n, err = p.parseCounted(n)
			if err != nil {
				return nil, err
			}
		}
		if r, ok := n.(Repeat); ok && p.pos < len(p.input) {
			switch p.peek() {
			case '?':
				r.Greedy = false
				p.pos++
				n = r
			case '+':
				p.pos++
				n = Group{Child: r, Atomic: true}
			}
		}
	}
	return n, nil
}

// parseScopedFlags 解析形如 (?im-s:...) 的局部模式修饰符。
func (p *state) parseScopedFlags() (GroupFlag, GroupFlag, bool, error) {
	if p.peek() != '?' || p.pos+1 >= len(p.input) {
		return 0, 0, false, nil
	}
	next := p.input[p.pos+1]
	if next != 'i' && next != 's' && next != 'm' && next != 'x' && next != '-' {
		return 0, 0, false, nil
	}
	p.pos++
	var set, clear GroupFlag
	disabled := false
	seen := false
	for p.pos < len(p.input) {
		value := p.peek()
		if value == ':' {
			if !seen {
				return 0, 0, false, p.errorf("empty scoped flags")
			}
			p.pos++
			return set, clear, true, nil
		}
		if value == '-' {
			if disabled {
				return 0, 0, false, p.errorf("invalid scoped flags")
			}
			disabled = true
			p.pos++
			continue
		}
		var flag GroupFlag
		switch value {
		case 'i':
			flag = GroupFlagCaseless
		case 's':
			flag = GroupFlagDotAll
		case 'm':
			flag = GroupFlagMultiline
		case 'x':
			flag = GroupFlagExtended
		default:
			return 0, 0, false, p.errorf("invalid scoped flags")
		}
		if disabled {
			clear |= flag
			set &^= flag
		} else {
			set |= flag
			clear &^= flag
		}
		seen = true
		p.pos++
	}
	return 0, 0, false, p.errorf("unterminated scoped flags")
}
func (p *state) parseEscape() (Node, error) {
	p.pos++
	if p.pos >= len(p.input) {
		return nil, p.errorf("dangling escape")
	}
	ch := p.input[p.pos]
	p.pos++
	if ch == 'Q' {
		start := p.pos
		for p.pos+1 < len(p.input) && !(p.input[p.pos] == '\\' && p.input[p.pos+1] == 'E') {
			p.pos++
		}
		if p.pos+1 >= len(p.input) {
			return nil, p.errorf("unterminated quoted escape")
		}
		value := append([]byte(nil), p.input[start:p.pos]...)
		p.pos += 2
		return Literal{Value: value}, nil
	}
	if ch == 'p' || ch == 'P' {
		if p.peek() != '{' {
			return nil, p.errorf("unicode property requires braces")
		}
		p.pos++
		start := p.pos
		for p.pos < len(p.input) && p.peek() != '}' {
			p.pos++
		}
		if p.peek() != '}' || p.pos == start {
			return nil, p.errorf("invalid unicode property")
		}
		name := string(p.input[start:p.pos])
		p.pos++
		return UnicodeClass{Name: name, Negated: ch == 'P'}, nil
	}
	if ch >= '1' && ch <= '9' {
		start := p.pos - 1
		index := int(ch - '0')
		for p.pos < len(p.input) && p.input[p.pos] >= '0' && p.input[p.pos] <= '9' {
			digit := int(p.input[p.pos] - '0')
			if index > (int(^uint(0)>>1)-digit)/10 {
				return nil, p.errorf("backreference index overflow")
			}
			index = index*10 + digit
			p.pos++
		}
		digits := p.input[start:p.pos]
		if len(digits) == 3 && digits[0] <= '3' && digits[1] <= '7' && digits[2] <= '7' {
			value := int(digits[0]-'0')*64 + int(digits[1]-'0')*8 + int(digits[2]-'0')
			return Literal{Value: []byte{byte(value)}}, nil
		}
		return Backreference{Index: index}, nil
	}
	switch ch {
	case 'g':
		if p.peek() != '<' && p.peek() != '{' {
			return nil, p.errorf("invalid named backreference")
		}
		close := byte('>')
		if p.peek() == '{' {
			close = '}'
		}
		p.pos++
		start := p.pos
		for p.pos < len(p.input) && p.peek() != close {
			p.pos++
		}
		if p.pos == start || p.peek() != close {
			return nil, p.errorf("invalid named backreference")
		}
		name := string(p.input[start:p.pos])
		p.pos++
		index, ok := p.names[name]
		if !ok {
			return nil, p.errorf("unknown named backreference %q", name)
		}
		return Backreference{Index: index}, nil
	case 'G', 'K':
		return nil, p.errorf("unsupported escape \\%c", ch)
	case 'k':
		name, err := p.parseCaptureName()
		if err != nil {
			return nil, err
		}
		index, ok := p.names[name]
		if !ok {
			return nil, p.errorf("unknown named backreference %q", name)
		}
		return Backreference{Index: index}, nil
	case '0':
		v := 0
		for i := 0; i < 3 && p.pos < len(p.input) && p.input[p.pos] >= '0' && p.input[p.pos] <= '7'; i++ {
			v = v*8 + int(p.input[p.pos]-'0')
			p.pos++
		}
		return Literal{Value: []byte{byte(v)}}, nil
	case 'x':
		if p.peek() == '{' {
			p.pos++
			start := p.pos
			value := 0
			for p.pos < len(p.input) && p.peek() != '}' {
				digit := hexDigit(p.input[p.pos])
				if digit < 0 || value > (utf8.MaxRune-digit)/16 {
					return nil, p.errorf("invalid braced hex escape")
				}
				value = value*16 + digit
				p.pos++
			}
			if p.pos == start || p.peek() != '}' || value > utf8.MaxRune || value >= 0xd800 && value <= 0xdfff {
				return nil, p.errorf("invalid braced hex escape")
			}
			p.pos++
			return Literal{Value: []byte(string(rune(value)))}, nil
		}
		if p.pos+2 > len(p.input) {
			return nil, p.errorf("invalid hex escape")
		}
		a, b := hexDigit(p.input[p.pos]), hexDigit(p.input[p.pos+1])
		if a < 0 || b < 0 {
			return nil, p.errorf("invalid hex escape")
		}
		p.pos += 2
		return Literal{Value: []byte{byte(a*16 + b)}}, nil
	case 'o':
		if p.peek() != '{' {
			return nil, p.errorf("octal escape requires braces")
		}
		p.pos++
		start := p.pos
		value := 0
		for p.pos < len(p.input) && p.peek() != '}' {
			digit := p.input[p.pos]
			if digit < '0' || digit > '7' || value > (utf8.MaxRune-int(digit-'0'))/8 {
				return nil, p.errorf("invalid braced octal escape")
			}
			value = value*8 + int(digit-'0')
			p.pos++
		}
		if p.pos == start || p.peek() != '}' || value > utf8.MaxRune || value >= 0xd800 && value <= 0xdfff {
			return nil, p.errorf("invalid braced octal escape")
		}
		p.pos++
		return Literal{Value: []byte(string(rune(value)))}, nil
	case 'A':
		return Assertion{Kind: BeginAbsolute}, nil
	case 'z':
		return Assertion{Kind: EndAbsolute}, nil
	case 'Z':
		return Assertion{Kind: EndBeforeFinalNewline}, nil
	case 'b':
		return Assertion{Kind: WordBoundary}, nil
	case 'B':
		return Assertion{Kind: NonWordBoundary}, nil
	case 'd':
		return shorthandClass(ClassDigit, false), nil
	case 'D':
		return shorthandClass(ClassDigit, true), nil
	case 'w':
		return shorthandClass(ClassWord, false), nil
	case 'W':
		return shorthandClass(ClassWord, true), nil
	case 's':
		return shorthandClass(ClassSpace, false), nil
	case 'S':
		return shorthandClass(ClassSpace, true), nil
	case 'h':
		return shorthandClass(ClassHorizontalSpace, false), nil
	case 'H':
		return shorthandClass(ClassHorizontalSpace, true), nil
	case 'n':
		return Literal{Value: []byte{'\n'}}, nil
	case 'r':
		return Literal{Value: []byte{'\r'}}, nil
	case 't':
		return Literal{Value: []byte{'\t'}}, nil
	case 'f':
		return Literal{Value: []byte{'\f'}}, nil
	case 'a':
		return Literal{Value: []byte{'\a'}}, nil
	case 'e':
		return Literal{Value: []byte{0x1b}}, nil
	case 'c':
		if p.pos >= len(p.input) || (p.input[p.pos] < 'A' || p.input[p.pos] > 'Z') && (p.input[p.pos] < 'a' || p.input[p.pos] > 'z') {
			return nil, p.errorf("invalid control escape")
		}
		value := p.input[p.pos]
		p.pos++
		if value >= 'a' && value <= 'z' {
			value -= 'a' - 'A'
		}
		return Literal{Value: []byte{value & 0x1f}}, nil
	case 'v':
		return shorthandClass(ClassVerticalSpace, false), nil
	case 'V':
		return shorthandClass(ClassVerticalSpace, true), nil
	case 'C':
		return Class{Ranges: []Range{{Lo: 0, Hi: 0xff}}}, nil
	case 'R':
		return Alternation{Options: []Node{
			Literal{Value: []byte{'\r', '\n'}},
			Literal{Value: []byte{'\n'}},
			Literal{Value: []byte{'\v'}},
			Literal{Value: []byte{'\f'}},
			Literal{Value: []byte{'\r'}},
			Literal{Value: []byte{0x85}},
			Literal{Value: []byte("\u2028")},
			Literal{Value: []byte("\u2029")},
		}}, nil
	case 'N':
		return Class{Ranges: []Range{{Lo: '\n', Hi: '\n'}}, Negated: true}, nil
	default:
		return Literal{Value: []byte{ch}}, nil
	}
}

func hexDigit(value byte) int {
	if value >= '0' && value <= '9' {
		return int(value - '0')
	}
	if value >= 'a' && value <= 'f' {
		return int(value - 'a' + 10)
	}
	if value >= 'A' && value <= 'F' {
		return int(value - 'A' + 10)
	}
	return -1
}
func (p *state) parseClass() (Node, error) {
	p.pos++
	negate := false
	if p.peek() == '^' {
		negate = true
		p.pos++
	}
	ranges := []Range{}
	readChar := func() (byte, error) {
		if p.pos >= len(p.input) {
			return 0, p.errorf("unterminated character class")
		}
		ch := p.input[p.pos]
		p.pos++
		if ch != '\\' || p.pos >= len(p.input) {
			return ch, nil
		}
		esc := p.input[p.pos]
		p.pos++
		switch esc {
		case 'x':
			if p.pos+2 > len(p.input) {
				return 0, p.errorf("invalid hex escape in class")
			}
			hex := func(c byte) int {
				if c >= '0' && c <= '9' {
					return int(c - '0')
				}
				if c >= 'a' && c <= 'f' {
					return int(c-'a') + 10
				}
				if c >= 'A' && c <= 'F' {
					return int(c-'A') + 10
				}
				return -1
			}
			a, b := hex(p.input[p.pos]), hex(p.input[p.pos+1])
			if a < 0 || b < 0 {
				return 0, p.errorf("invalid hex escape in class")
			}
			p.pos += 2
			return byte(a*16 + b), nil
		case '0':
			v := 0
			for i := 0; i < 3 && p.pos < len(p.input) && p.input[p.pos] >= '0' && p.input[p.pos] <= '7'; i++ {
				v = v*8 + int(p.input[p.pos]-'0')
				p.pos++
			}
			return byte(v), nil
		default:
			switch esc {
			case 'n':
				return '\n', nil
			case 'r':
				return '\r', nil
			case 't':
				return '\t', nil
			case 'b':
				return '\b', nil
			case 'f':
				return '\f', nil
			case 'v':
				return '\v', nil
			}
			if esc >= '1' && esc <= '7' {
				v := int(esc - '0')
				for i := 0; i < 2 && p.pos < len(p.input) && p.input[p.pos] >= '0' && p.input[p.pos] <= '7'; i++ {
					v = v*8 + int(p.input[p.pos]-'0')
					p.pos++
				}
				return byte(v), nil
			}
			return esc, nil
		}
	}
	for p.pos < len(p.input) && p.peek() != ']' {
		if p.pos+2 < len(p.input) && p.input[p.pos] == '[' && p.input[p.pos+1] == ':' {
			classRanges, err := p.parsePOSIXClass()
			if err != nil {
				return nil, err
			}
			ranges = append(ranges, classRanges...)
			continue
		}
		if p.peek() == '\\' && p.pos+1 < len(p.input) {
			switch p.input[p.pos+1] {
			case 'd':
				p.pos += 2
				ranges = append(ranges, digitRanges()...)
				continue
			case 'D':
				p.pos += 2
				ranges = append(ranges, complementRanges(digitRanges())...)
				continue
			case 'w':
				p.pos += 2
				ranges = append(ranges, wordRanges()...)
				continue
			case 'W':
				p.pos += 2
				ranges = append(ranges, complementRanges(wordRanges())...)
				continue
			case 's':
				p.pos += 2
				ranges = append(ranges, spaceRanges()...)
				continue
			case 'S':
				p.pos += 2
				ranges = append(ranges, complementRanges(spaceRanges())...)
				continue
			case 'h':
				p.pos += 2
				ranges = append(ranges, horizontalSpaceRanges()...)
				continue
			case 'H':
				p.pos += 2
				ranges = append(ranges, complementRanges(horizontalSpaceRanges())...)
				continue
			case 'v':
				p.pos += 2
				ranges = append(ranges, verticalSpaceRanges()...)
				continue
			case 'V':
				p.pos += 2
				ranges = append(ranges, complementRanges(verticalSpaceRanges())...)
				continue
			}
		}
		lo, err := readChar()
		if err != nil {
			return nil, err
		}
		hi := lo
		if p.peek() == '-' && p.pos+1 < len(p.input) && p.input[p.pos+1] != ']' {
			p.pos++
			hi, err = readChar()
			if err != nil {
				return nil, err
			}
		}
		if hi < lo {
			return nil, p.errorf("character class range is reversed")
		}
		ranges = append(ranges, Range{lo, hi})
	}
	if p.peek() != ']' {
		return nil, p.errorf("missing closing character class")
	}
	p.pos++
	if len(ranges) == 0 {
		return nil, p.errorf("empty character class")
	}
	return Class{Ranges: ranges, Negated: negate}, nil
}

func (p *state) parsePOSIXClass() ([]Range, error) {
	start := p.pos
	p.pos += 2
	negated := false
	if p.peek() == '^' {
		negated = true
		p.pos++
	}
	nameStart := p.pos
	for p.pos+1 < len(p.input) && !(p.input[p.pos] == ':' && p.input[p.pos+1] == ']') {
		p.pos++
	}
	if nameStart == p.pos || p.pos+1 >= len(p.input) {
		return nil, p.errorf("invalid POSIX character class at %d", start)
	}
	name := string(p.input[nameStart:p.pos])
	p.pos += 2
	ranges, ok := posixClassRanges(name)
	if !ok {
		return nil, p.errorf("unsupported POSIX character class %q", name)
	}
	if negated {
		return complementRanges(ranges), nil
	}
	return ranges, nil
}

func posixClassRanges(name string) ([]Range, bool) {
	switch name {
	case "alnum":
		return []Range{{'0', '9'}, {'A', 'Z'}, {'a', 'z'}}, true
	case "alpha":
		return []Range{{'A', 'Z'}, {'a', 'z'}}, true
	case "ascii":
		return []Range{{0, 0x7f}}, true
	case "blank":
		return []Range{{'\t', '\t'}, {' ', ' '}}, true
	case "cntrl":
		return []Range{{0, 0x1f}, {0x7f, 0x7f}}, true
	case "digit":
		return digitRanges(), true
	case "graph":
		return []Range{{0x21, 0x7e}}, true
	case "lower":
		return []Range{{'a', 'z'}}, true
	case "print":
		return []Range{{0x20, 0x7e}}, true
	case "punct":
		return []Range{{'!', '/'}, {':', '@'}, {'[', '`'}, {'{', '~'}}, true
	case "space":
		return spaceRanges(), true
	case "upper":
		return []Range{{'A', 'Z'}}, true
	case "word":
		return wordRanges(), true
	case "xdigit":
		return []Range{{'0', '9'}, {'A', 'F'}, {'a', 'f'}}, true
	default:
		return nil, false
	}
}

func digitRanges() []Range { return []Range{{'0', '9'}} }

func wordRanges() []Range { return []Range{{'0', '9'}, {'A', 'Z'}, {'a', 'z'}, {'_', '_'}} }

func spaceRanges() []Range { return []Range{{'\t', '\r'}, {' ', ' '}} }

func horizontalSpaceRanges() []Range { return []Range{{'\t', '\t'}, {' ', ' '}, {0xa0, 0xa0}} }

func verticalSpaceRanges() []Range { return []Range{{'\n', '\r'}, {'\v', '\f'}, {0x85, 0x85}} }

func shorthandClass(kind ClassKind, negated bool) Class {
	switch kind {
	case ClassDigit:
		return Class{Ranges: digitRanges(), Negated: negated, Kind: kind}
	case ClassWord:
		return Class{Ranges: wordRanges(), Negated: negated, Kind: kind}
	case ClassSpace:
		return Class{Ranges: spaceRanges(), Negated: negated, Kind: kind}
	case ClassHorizontalSpace:
		return Class{Ranges: horizontalSpaceRanges(), Negated: negated, Kind: kind}
	case ClassVerticalSpace:
		return Class{Ranges: verticalSpaceRanges(), Negated: negated, Kind: kind}
	default:
		return Class{Negated: negated}
	}
}

func complementRanges(ranges []Range) []Range {
	normalized := Normalize(Class{Ranges: append([]Range(nil), ranges...)}).(Class).Ranges
	out := make([]Range, 0, len(normalized)+1)
	next := 0
	for _, current := range normalized {
		if next < int(current.Lo) {
			out = append(out, Range{Lo: byte(next), Hi: current.Lo - 1})
		}
		if current.Hi == 0xff {
			return out
		}
		next = int(current.Hi) + 1
	}
	if next <= 0xff {
		out = append(out, Range{Lo: byte(next), Hi: 0xff})
	}
	return out
}
func (p *state) parseCounted(child Node) (Node, error) {
	start := p.pos
	p.pos++
	read := func() (int, bool) {
		n := 0
		ok := false
		for p.pos < len(p.input) && p.input[p.pos] >= '0' && p.input[p.pos] <= '9' {
			ok = true
			digit := int(p.input[p.pos] - '0')
			if n > (int(^uint(0)>>1)-digit)/10 {
				p.pos++
				return 0, false
			}
			n = n*10 + digit
			p.pos++
		}
		return n, ok
	}
	min, ok := read()
	if !ok {
		return nil, p.errorf("invalid repeat at %d", start)
	}
	max := min
	if p.peek() == ',' {
		p.pos++
		if p.peek() == '}' {
			max = -1
		} else {
			var ok bool
			max, ok = read()
			if !ok {
				return nil, p.errorf("invalid repeat upper bound")
			}
		}
	}
	if p.peek() != '}' {
		return nil, p.errorf("missing repeat close")
	}
	p.pos++
	if max >= 0 && max < min {
		return nil, p.errorf("repeat upper bound below lower bound")
	}
	if min > 1<<20 || max > 1<<20 {
		return nil, p.errorf("repeat bound exceeds limit")
	}
	return Repeat{Child: child, Min: min, Max: max, Greedy: true}, nil
}
