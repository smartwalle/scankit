package som

import "testing"

func TestTrackerPropagation(t *testing.T) {
	tr := New(2)
	tr.Set(0, 8)
	tr.Propagate(1, 0)
	if v, ok := tr.Get(1); !ok || v != 8 {
		t.Fatal(v, ok)
	}
	tr.Earliest(1, 3)
	if v, _ := tr.Get(1); v != 3 {
		t.Fatal(v)
	}
	tr.Reset()
	if _, ok := tr.Get(0); ok {
		t.Fatal()
	}
}

func TestTrackerRangeCopyAndMerge(t *testing.T) {
	a := New(3)
	a.Set(1, 8)
	b := New(3)
	b.Set(0, 2)
	dup := a.CopyRange(1, 3)
	if dup.Len() != 2 {
		t.Fatal("区间复制失败")
	}
	a.MergeRange(b, 0, 1, 2)
	if v, _ := a.Get(2); v != 2 {
		t.Fatalf("区间合并值=%d", v)
	}
}

func TestTrackerPropagationKeepsEarliest(t *testing.T) {
	tr := New(2)
	tr.Set(0, 8)
	tr.Set(1, 3)
	tr.Propagate(1, 0)
	if got, _ := tr.Get(1); got != 3 {
		t.Fatalf("传播覆盖了更早起点: %d", got)
	}
}

func TestTrackerSerializationAndMerge(t *testing.T) {
	a, b := New(2), New(2)
	a.Set(0, 8)
	b.Set(0, 3)
	b.Set(1, 4)
	a.Merge(b)
	if v, _ := a.Get(0); v != 3 {
		t.Fatal(v)
	}
	raw, err := a.Dump()
	if err != nil {
		t.Fatal(err)
	}
	c, err := Load(raw)
	if err != nil || !a.Equal(c) {
		t.Fatal(err)
	}
}
func FuzzTracker(f *testing.F) {
	f.Add(2, uint64(4))
	f.Fuzz(func(t *testing.T, n int, v uint64) {
		if n < 0 || n > 100 {
			return
		}
		tr := New(n)
		if n > 0 {
			tr.Set(0, v)
			if got, ok := tr.Get(0); !ok || got != v {
				t.Fatal()
			}
		}
	})
}
