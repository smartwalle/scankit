package scankit

import (
	"errors"
	"testing"
)

func TestParseRegexAcceptsCoreSyntax(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		pattern string
		kind    regexNodeKind
	}{
		{name: "literal", pattern: "token", kind: regexConcat},
		{name: "alternation", pattern: "foo|bar", kind: regexAlternate},
		{name: "group and repeat", pattern: "(foo|bar)+", kind: regexRepeat},
		{name: "class range", pattern: "[A-Z0-9]{2,4}", kind: regexRepeat},
		{name: "anchors", pattern: "^id-[0-9]+$", kind: regexConcat},
		{name: "absolute anchors", pattern: `\AID:\d+\z`, kind: regexConcat},
		{name: "end before final newline", pattern: `token\Z`, kind: regexConcat},
		{name: "inline flags", pattern: `(?i:token)(?s).(?-m:^x$)`, kind: regexConcat},
		{name: "word boundaries", pattern: `\btoken\B`, kind: regexConcat},
		{name: "hex escape", pattern: `\x3A`, kind: regexLiteral},
		{name: "braced hex escape", pattern: `\x{3A}`, kind: regexLiteral},
		{name: "POSIX class", pattern: `[[:alpha:]][[:digit:]]`, kind: regexConcat},
		{name: "escaped classes", pattern: "\\d+\\s\\w+", kind: regexConcat},
		{name: "control escapes", pattern: `\a\e`, kind: regexConcat},
		{name: "control letter escape", pattern: `\cA\cz`, kind: regexConcat},
		{name: "octal escape", pattern: `\0\012\0377`, kind: regexConcat},
		{name: "braced octal escape", pattern: `\o{72}`, kind: regexLiteral},
		{name: "not newline escape", pattern: `\N`, kind: regexDot},
		{name: "lazy repeat", pattern: "a{2,}?", kind: regexRepeat},
		{name: "PCRE named group", pattern: `(?<token>secret)`, kind: regexConcat},
		{name: "Go named group", pattern: `(?P<token>secret)`, kind: regexConcat},
		{name: "comment group", pattern: `(?#metadata)token`, kind: regexConcat},
		{name: "quoted literal", pattern: `\Q[a-z]+\E`, kind: regexConcat},
		{name: "unterminated quoted literal", pattern: `\Q[a-z]+`, kind: regexConcat},
		{name: "empty branch", pattern: "foo|", kind: regexAlternate},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			node, err := parseRegex(tt.pattern)
			if err != nil {
				t.Fatalf("parseRegex(%q) error = %v", tt.pattern, err)
			}
			if node.kind != tt.kind {
				t.Fatalf("parseRegex(%q) kind = %d, want %d", tt.pattern, node.kind, tt.kind)
			}
		})
	}
}

func TestParseRegexBuildsExpectedClassesAndRepeats(t *testing.T) {
	t.Parallel()

	node, err := parseRegex("[a-c\\d]{2,4}")
	if err != nil {
		t.Fatal(err)
	}
	if node.kind != regexRepeat || node.min != 2 || node.max != 4 {
		t.Fatalf("repeat = %#v, want {2,4}", node)
	}
	class := node.children[0]
	if class.kind != regexClass || !class.class.contains('a') || !class.class.contains('9') || class.class.contains('z') {
		t.Fatalf("class has unexpected membership")
	}

	node, err = parseRegex(`[[:alpha:]\x2D[:^digit:]]`)
	if err != nil {
		t.Fatal(err)
	}
	class = node
	if class.kind != regexClass || !class.class.contains('a') || !class.class.contains('-') || !class.class.contains('!') || class.class.contains('9') {
		t.Fatalf("POSIX class has unexpected membership")
	}
}

func TestParseRegexRejectsInvalidSyntax(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		pattern string
	}{
		{name: "unclosed group", pattern: "(foo"},
		{name: "unclosed class", pattern: "[abc"},
		{name: "empty class", pattern: "[]"},
		{name: "reversed range", pattern: "[z-a]"},
		{name: "missing repeat lower", pattern: "a{,2}"},
		{name: "inverted repeat bounds", pattern: "a{3,2}"},
		{name: "duplicate repeat", pattern: "a++"},
		{name: "trailing escape", pattern: "foo\\"},
		{name: "short hex escape", pattern: `\x0`},
		{name: "invalid hex escape", pattern: `\xGG`},
		{name: "empty braced hex escape", pattern: `\x{}`},
		{name: "invalid braced hex escape", pattern: `\x{GG}`},
		{name: "unterminated braced hex escape", pattern: `\x{3A`},
		{name: "out of range braced hex escape", pattern: `\x{100}`},
		{name: "missing control escape letter", pattern: `\c`},
		{name: "invalid control escape letter", pattern: `\c1`},
		{name: "numeric escape without leading zero", pattern: `\1`},
		{name: "out of range octal escape", pattern: `\400`},
		{name: "missing braced octal escape", pattern: `\o`},
		{name: "empty braced octal escape", pattern: `\o{}`},
		{name: "invalid braced octal escape", pattern: `\o{8}`},
		{name: "unterminated braced octal escape", pattern: `\o{72`},
		{name: "out of range braced octal escape", pattern: `\o{400}`},
		{name: "word boundary in class", pattern: `[\b]`},
		{name: "unknown POSIX class", pattern: `[[:unknown:]]`},
		{name: "unterminated POSIX class", pattern: `[[:digit]`},
		{name: "missing named group name", pattern: `(?<>)`},
		{name: "invalid named group name", pattern: `(?<1name>token)`},
		{name: "unterminated named group name", pattern: `(?<name token)`},
		{name: "unterminated comment group", pattern: `(?# comment`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parseRegex(tt.pattern)
			if err == nil {
				t.Fatalf("parseRegex(%q) error = nil", tt.pattern)
			}
			if _, ok := err.(*regexParseError); !ok {
				t.Fatalf("error type = %T, want *regexParseError", err)
			}
		})
	}
}

func TestCompileUsesRegexParserForRegularExpressionSyntax(t *testing.T) {
	t.Parallel()

	_, err := Compile([]Expression{{Id: 1, Pattern: "[unterminated"}})
	var parseErr *regexParseError
	if !errors.As(err, &parseErr) {
		t.Fatalf("Compile() error = %T %v, want *regexParseError", err, err)
	}

	if _, err = Compile([]Expression{{Id: 1, Pattern: `\d+`}}); err != nil {
		t.Fatalf("Compile() error = %v, want nil", err)
	}

	database, err := Compile([]Expression{{Id: 1, Pattern: `(?<id>ID:\d{2})`}})
	if err != nil {
		t.Fatal(err)
	}
	matches, err := database.Scan([]byte("ID:42"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 || matches[0].From != 0 || matches[0].To != 5 {
		t.Fatalf("named-group scan matches = %#v", matches)
	}

	database, err = Compile([]Expression{{Id: 1, Pattern: `\Q[a-z]+\E`}})
	if err != nil {
		t.Fatal(err)
	}
	matches, err = database.Scan([]byte("[a-z]+"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 || matches[0].From != 0 || matches[0].To != 6 {
		t.Fatalf("quoted-literal scan matches = %#v", matches)
	}
}

func FuzzParseRegex(f *testing.F) {
	for _, seed := range []string{"", "token", "(foo|bar)+", "(?<name>token)", "(?P<name>token)", "(?#comment)token", `\Q[a-z]+\E`, "[a-z]{1,8}", "\\d+", `\\x3A`, `\a\e`, `\N`, `\N{LATIN CAPITAL LETTER A}`, `token\Z`, `\R`, `(?i:token)`, `(?x) token # comment`, "[[:digit:]]", "[", "foo\\"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, pattern string) {
		if len(pattern) > 4_096 {
			t.Skip()
		}
		node, err := parseRegex(pattern)
		if err == nil && node == nil {
			t.Fatal("successful parse returned nil node")
		}
	})
}
