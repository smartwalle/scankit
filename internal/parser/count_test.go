package parser

import "testing"

func TestNodeCount(t *testing.T) {
	r, _ := Parse("ab")
	if NodeCount(r) < 2 {
		t.Fatal()
	}
}
