package parser

import "testing"

func TestOctalEscape(t *testing.T) {
	n, e := Parse(`\101`)
	if e != nil {
		t.Fatal(e)
	}
	if l, ok := n.(Literal); !ok || l.Value[0] != 'A' {
		t.Fatal(n)
	}
}

func TestQuotedEscape(t *testing.T) {
	n, err := Parse(`\Qa.*[b]\E`)
	if err != nil {
		t.Fatal(err)
	}
	l, ok := n.(Literal)
	if !ok || string(l.Value) != "a.*[b]" {
		t.Fatalf("unexpected node %#v", n)
	}
}

func TestClassNumericEscapes(t *testing.T) {
	n, err := Parse(`[\x41-\103]`)
	if err != nil {
		t.Fatal(err)
	}
	c, ok := n.(Class)
	if !ok || len(c.Ranges) != 1 || c.Ranges[0] != (Range{'A', 'C'}) {
		t.Fatalf("unexpected node %#v", n)
	}
}
