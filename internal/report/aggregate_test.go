package report

import "testing"

func TestEventAggregates(t *testing.T) {
	m := New()
	m.Add(Event{ID: 2, From: 4, To: 6, SOM: 4})
	m.Add(Event{ID: 1, From: 1, To: 3, SOM: 1})
	if m.Counts()[1] != 1 {
		t.Fatal("规则计数错误")
	}
	if first, ok := m.FirstFor(1); !ok || first.From != 1 {
		t.Fatal("最早事件错误")
	}
	if got := m.EventsBySOM(); len(got) != 2 || got[0].ID != 1 {
		t.Fatalf("SOM 排序=%v", got)
	}
}
