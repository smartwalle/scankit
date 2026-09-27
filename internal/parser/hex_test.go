package parser

import "testing"

func TestHexEscape(t *testing.T) {
	n, e := Parse(`\x41`)
	if e != nil {
		t.Fatal(e)
	}
	if l, ok := n.(Literal); !ok || l.Value[0] != 'A' {
		t.Fatal(n)
	}
}

func TestBracedHexAndControlEscapes(t *testing.T) {
	node, err := Parse(`\x{4e2d}\cA\e`)
	if err != nil {
		t.Fatal(err)
	}
	literal, ok := Normalize(node).(Literal)
	if !ok || string(literal.Value) != "中\x01\x1b" {
		t.Fatalf("unexpected escaped literal: %#v", node)
	}
	for _, pattern := range []string{`\x{}`, `\x{110000}`, `\x{d800}`, `\c1`} {
		if _, err := Parse(pattern); err == nil {
			t.Fatalf("invalid escape accepted: %q", pattern)
		}
	}
}

func TestBracedOctalAndBellEscapes(t *testing.T) {
	node, err := Parse(`\o{101}\a`)
	if err != nil {
		t.Fatal(err)
	}
	literal, ok := Normalize(node).(Literal)
	if !ok || string(literal.Value) != "A\a" {
		t.Fatalf("unexpected escaped literal: %#v", node)
	}
	for _, pattern := range []string{`\o{}`, `\o{8}`, `\o{40000000}`, `\o101`} {
		if _, err := Parse(pattern); err == nil {
			t.Fatalf("invalid octal escape accepted: %q", pattern)
		}
	}
}
