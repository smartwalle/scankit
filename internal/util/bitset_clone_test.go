package util

import "testing"

func TestBitSetClone(t *testing.T) {
	a := NewBitSet(2)
	a.Set(1)
	b := a.Clone()
	b.Set(2)
	if a.Has(2) {
		t.Fatal()
	}
}

func TestBitSetRejectsOverflowingCapacity(t *testing.T) {
	if got := NewBitSet(int(^uint(0) >> 1)); got.Len() != 0 {
		t.Fatalf("溢出容量=%d", got.Len())
	}
}
