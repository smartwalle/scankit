package hwlm

import "testing"

func TestIndexRangeLimitsUseStableWindowOrder(t *testing.T) {
	i := NewIndex([]Literal{{ID: 2, Value: []byte("a")}, {ID: 1, Value: []byte("a")}, {ID: 3, Value: []byte("aa")}})
	got := i.FindRangeLimit([]byte("aaa"), 0, 3, 2)
	if len(got) != 2 || got[0].From != 0 || got[0].ID != 1 || got[1].From != 0 || got[1].ID != 2 {
		t.Fatalf("区间限量顺序=%v", got)
	}
	end := i.FindEndRangeLimit([]byte("aaa"), 1, 3, 1)
	if len(end) != 1 || end[0].To != 1 {
		t.Fatalf("结束区间限量=%v", end)
	}
}
