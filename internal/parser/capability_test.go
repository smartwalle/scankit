package parser

import "testing"

func TestCapabilitySummary(t *testing.T) {
	r, err := Parse(`(ab)\1`)
	if err != nil {
		t.Fatal(err)
	}
	if !HasCapture(r) || !HasKind(r, KindBackreference) || !RequiresBacktracking(r) {
		t.Fatal("能力识别不完整")
	}
	if MaxLiteralLength(r) != 1 {
		t.Fatalf("最长文字长度=%d", MaxLiteralLength(r))
	}
	s := Summarize(r)
	if s.Nodes == 0 || s.Captures != 1 || !s.Stateful {
		t.Fatalf("摘要=%+v", s)
	}
}
