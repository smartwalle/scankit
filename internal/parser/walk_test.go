package parser

import "testing"

func TestWalk(t *testing.T) {
	r, _ := Parse("ab|c")
	n := 0
	Walk(r, func(Node) bool { n++; return true })
	if n < 3 {
		t.Fatal(n)
	}
}
