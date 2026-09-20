package util

import "testing"

func TestBitSetSetOps(t *testing.T) {
	a, b := NewBitSet(8), NewBitSet(8)
	a.Set(1)
	b.Set(2)
	a.Union(&b)
	if !a.Has(2) {
		t.Fatal()
	}
	a.Intersect(&b)
	if a.Has(1) || !a.Has(2) {
		t.Fatal()
	}
}

func TestBitSetResizeSetAllIndices(t *testing.T) {
	b := NewBitSet(130)
	b.Set(1)
	b.Set(129)
	if got := b.Indices(); len(got) != 2 || got[0] != 1 || got[1] != 129 {
		t.Fatalf("索引=%v", got)
	}
	b.Resize(64)
	if b.Has(129) {
		t.Fatal("缩容未清理越界位")
	}
	b.SetAll()
	if !b.All() || b.Count() != 64 {
		t.Fatalf("全量设置失败")
	}
}
