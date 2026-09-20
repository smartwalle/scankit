package som

import "testing"

func TestClone(t *testing.T) {
	a := New(1)
	a.Set(0, 4)
	b := a.Clone()
	b.Set(0, 8)
	v, _ := a.Get(0)
	if v != 4 {
		t.Fatal()
	}
}
