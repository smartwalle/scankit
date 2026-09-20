package hwlm

import "testing"

func TestLiteralMetricsAndEndRange(t *testing.T) {
	idx := NewIndex([]Literal{{ID: 1, Value: []byte("ab")}, {ID: 2, Value: []byte("abcd")}})
	if got := idx.FindEndRange([]byte("xxabcdy"), 4, 7); len(got) != 2 || got[1].ID != 2 {
		t.Fatalf("结束范围=%v", got)
	}
	if Shortest(idx.Literals()).ID != 1 || TotalBytes(idx.Literals()) != 6 {
		t.Fatal("文字统计错误")
	}
}
