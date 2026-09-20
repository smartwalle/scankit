package util

import "testing"

func TestPriorityQueueStableOrder(t *testing.T) {
	var q PriorityQueue[string]
	q.Push("late", 2)
	q.Push("first", 1)
	q.Push("second", 1)
	if value, priority, ok := q.Pop(); !ok || value != "first" || priority != 1 {
		t.Fatalf("首项=%v,%d,%v", value, priority, ok)
	}
	if value, _, _ := q.Pop(); value != "second" {
		t.Fatalf("同优先级未保持稳定顺序: %q", value)
	}
}
