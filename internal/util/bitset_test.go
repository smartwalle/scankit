package util

import "testing"

func TestBitSetAndCharReach(t *testing.T) {
	b := NewBitSet(1)
	b.Set(0)
	b.Set(64)
	if !b.Has(0) || !b.Has(64) || b.Count() != 2 {
		t.Fatalf("unexpected bitset: has0=%v has64=%v count=%d", b.Has(0), b.Has(64), b.Count())
	}
	b.Clear(0)
	if b.Has(0) || b.Count() != 1 {
		t.Fatalf("clear failed")
	}
	var chars CharReach
	chars.Add(0)
	chars.Add(255)
	if !chars.Contains(0) || !chars.Contains(255) || chars.Count() != 2 {
		t.Fatalf("unexpected char reach")
	}
}

func TestQueue(t *testing.T) {
	var q Queue[int]
	q.Push(1)
	q.Push(2)
	if q.Len() != 2 {
		t.Fatal("length")
	}
	got, _ := q.Pop()
	if got != 1 {
		t.Fatalf("first = %d", got)
	}
	got, _ = q.Pop()
	if got != 2 || q.Len() != 0 {
		t.Fatalf("second = %d len=%d", got, q.Len())
	}
}
