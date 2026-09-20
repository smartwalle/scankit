package generic

import "testing"

func TestReverse(t *testing.T) {
	a := Vector{1, 2}
	if Reverse(a)[15] != 1 {
		t.Fatal()
	}
}
