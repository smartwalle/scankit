package util

import "testing"

func TestBitSetDifference(t *testing.T) {
	a, b := NewBitSet(4), NewBitSet(4)
	a.Set(1)
	a.Set(2)
	b.Set(2)
	a.Difference(&b)
	if !a.Has(1) || a.Has(2) || !a.Any() {
		t.Fatal()
	}
}
