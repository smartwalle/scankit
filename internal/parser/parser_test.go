package parser

import (
	"strings"
	"testing"
)

func TestParseCoreExpressions(t *testing.T) {
	for _, pattern := range []string{"abc", "a|b", "[a-z]+", "(?:ab)?", "^\\d{2,4}$", "(?>ab)", "a(?=b)", "a(?!b)", "(?<=a)b", "(?<!a)b", "(a)\\1", "(*SKIP)"} {
		if _, err := Parse(pattern); err != nil {
			t.Fatalf("Parse(%q): %v", pattern, err)
		}
	}
}

func TestParseRejectsMalformed(t *testing.T) {
	for _, pattern := range []string{"[", "(", "a{3,1}", "\\"} {
		if _, err := Parse(pattern); err == nil {
			t.Fatalf("Parse(%q) unexpectedly succeeded", pattern)
		}
	}
}

func TestParseManyReportsIndex(t *testing.T) {
	nodes, err := ParseMany([]string{"a", "["})
	if err == nil || nodes != nil || !strings.Contains(err.Error(), "expression 1") {
		t.Fatalf("批量解析错误=%v", err)
	}
}

func TestValidateReferences(t *testing.T) {
	root, err := Parse("(a)\\1")
	if err != nil {
		t.Fatal(err)
	}
	if err := Validate(root); err != nil {
		t.Fatal(err)
	}
	root, err = Parse("\\2")
	if err != nil {
		t.Fatal(err)
	}
	if err := Validate(root); err == nil {
		t.Fatal("expected invalid reference")
	}
}

func TestParsePOSIXCharacterClasses(t *testing.T) {
	for _, pattern := range []string{`[[:digit:]]+`, `[[:^digit:]]`, `[[:alpha:]_]+`, `[[:xdigit:]]`} {
		node, err := Parse(pattern)
		if err != nil {
			t.Fatalf("Parse(%q): %v", pattern, err)
		}
		if err := Validate(node); err != nil {
			t.Fatalf("Validate(%q): %v", pattern, err)
		}
	}
	for _, pattern := range []string{`[[:unknown:]]`, `[[:digit]`} {
		if _, err := Parse(pattern); err == nil {
			t.Fatalf("Parse(%q) unexpectedly succeeded", pattern)
		}
	}
}

func TestParseAnyByteAndNewlineEscapes(t *testing.T) {
	for _, pattern := range []string{`\C`, `\R`, `\N`, `\Z`} {
		if _, err := Parse(pattern); err != nil {
			t.Fatalf("Parse(%q): %v", pattern, err)
		}
	}
}

func TestParseNamedCaptureAndReference(t *testing.T) {
	for _, pattern := range []string{`(?<word>a)\k<word>`, `(?'word'a)\k'word'`, `(?P<word>a)\k<word>`, `(?<word>a)(?(<word>)b|c)`} {
		node, err := Parse(pattern)
		if err != nil {
			t.Fatalf("Parse(%q): %v", pattern, err)
		}
		if err := Validate(node); err != nil {
			t.Fatalf("Validate(%q): %v", pattern, err)
		}
	}
	for _, pattern := range []string{`\k<missing>`, `(?<word>a)(?<word>b)`, `(?<bad-name>a)`} {
		if _, err := Parse(pattern); err == nil {
			t.Fatalf("Parse(%q) unexpectedly succeeded", pattern)
		}
	}
}

func TestParsePossessiveQuantifier(t *testing.T) {
	for _, pattern := range []string{`a*+`, `a++`, `a?+`, `a{2,3}+`} {
		node, err := Parse(pattern)
		if err != nil {
			t.Fatalf("Parse(%q): %v", pattern, err)
		}
		group, ok := node.(Group)
		if !ok || !group.Atomic {
			t.Fatalf("possessive quantifier did not become atomic: %#v", node)
		}
	}
}

func TestParseScopedFlags(t *testing.T) {
	node, err := Parse(`a(?is-m:bc.)d`)
	if err != nil {
		t.Fatal(err)
	}
	sequence, ok := node.(Sequence)
	if !ok || len(sequence.Elements) != 3 {
		t.Fatalf("node=%#v", node)
	}
	group, ok := sequence.Elements[1].(Group)
	if !ok || group.SetFlags != GroupFlagCaseless|GroupFlagDotAll || group.ClearFlags != GroupFlagMultiline || !HasScopedFlags(node) {
		t.Fatalf("group=%#v", group)
	}
	for _, pattern := range []string{`(?q:a)`, `(?-:a)`, `(?i-a)`} {
		if _, err := Parse(pattern); err == nil {
			t.Fatalf("invalid scoped flags accepted: %q", pattern)
		}
	}
}

func TestParseExtendedMode(t *testing.T) {
	node, err := ParseWithExtended("a # 注释\n b[ #]", true)
	if err != nil {
		t.Fatal(err)
	}
	if !Equal(node, Sequence{Elements: []Node{Literal{Value: []byte("a")}, Literal{Value: []byte("b")}, Class{Ranges: []Range{{Lo: ' ', Hi: ' '}, {Lo: '#', Hi: '#'}}}}}) {
		t.Fatalf("extended node=%#v", node)
	}
	node, err = Parse(`(?x:a # 注释
 b)`)
	if err != nil || !HasScopedFlags(node) {
		t.Fatalf("scoped extended mode: %v %#v", err, node)
	}
	node, err = Parse("(?x)a # 注释\n b")
	if err != nil || !Equal(node, Sequence{Elements: []Node{Literal{Value: []byte("a")}, Literal{Value: []byte("b")}}}) {
		t.Fatalf("global extended mode: %v %#v", err, node)
	}
}

func TestParseGlobalFlags(t *testing.T) {
	node, err := Parse("(?im)^a$")
	if err != nil {
		t.Fatal(err)
	}
	group, ok := node.(Group)
	if !ok || group.SetFlags != GroupFlagCaseless|GroupFlagMultiline {
		t.Fatalf("全局修饰符未保留: %#v", node)
	}
}

func TestParseRejectsUnsupportedReferenceAndPositionEscapes(t *testing.T) {
	for _, pattern := range []string{`\g{1}`, `\Gabc`, `foo\Kbar`} {
		if _, err := Parse(pattern); err == nil {
			t.Fatalf("unsupported escape accepted: %q", pattern)
		}
	}
}
