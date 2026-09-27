package parser

import "testing"

func TestNegatedClass(t *testing.T) {
	n, err := Parse(`[^a]`)
	if err != nil {
		t.Fatal(err)
	}
	c, ok := n.(Class)
	if !ok || !c.Negated {
		t.Fatalf("%#v", n)
	}
}
