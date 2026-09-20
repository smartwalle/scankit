package report

import "testing"

func TestEventValidate(t *testing.T) {
	if (Event{From: 2, To: 1}).Validate() == nil {
		t.Fatal()
	}
}

func TestManagerRemoveIDAndEventsAfter(t *testing.T) {
	m := New()
	m.Add(Event{ID: 1, From: 1, To: 2})
	m.Add(Event{ID: 2, From: 3, To: 4})
	if got := m.EventsAfter(1); len(got) != 1 || got[0].ID != 2 {
		t.Fatalf("偏移过滤=%v", got)
	}
	if m.RemoveID(1) != 1 || m.HasID(1) {
		t.Fatal("规则事件未删除")
	}
}
