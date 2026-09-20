package util

import "testing"

func TestQueuePeek(t *testing.T) {
	var q Queue[int]
	if _, ok := q.Peek(); ok {
		t.Fatal()
	}
	q.Push(3)
	if v, _ := q.Peek(); v != 3 {
		t.Fatal(v)
	}
}

func TestNilQueuePushIsSafe(t *testing.T) {
	var q *Queue[int]
	q.Push(1)
}
