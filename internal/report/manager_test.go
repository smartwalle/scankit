package report

import "testing"

func TestManagerOrdering(t *testing.T) {
	m := New()
	m.Add(Event{ID: 2, From: 3, To: 4})
	m.Add(Event{ID: 1, From: 1, To: 2})
	e := m.Events()
	if e[0].ID != 1 {
		t.Fatal(e)
	}
}

func TestManagerMergeAndEventLimit(t *testing.T) {
	left, right := New(), New()
	left.Add(Event{ID: 2, From: 2, To: 3, SOM: 2})
	right.Add(Event{ID: 1, From: 1, To: 2, SOM: 1})
	if left.Merge(right) != 1 {
		t.Fatal("合并数量错误")
	}
	events := left.EventsLimit(1)
	if len(events) != 1 || events[0].ID != 1 {
		t.Fatalf("限量排序错误=%#v", events)
	}
}

func TestManagerCallbackCanSuppress(t *testing.T) {
	m := New()
	m.SetCallback(func(Event) bool { return false })
	if m.Add(Event{ID: 1}) {
		t.Fatal()
	}
	if len(m.Events()) != 0 {
		t.Fatal()
	}
}
func TestManagerDeduplicate(t *testing.T) {
	m := New()
	m.Add(Event{ID: 1, From: 0, To: 1})
	m.Add(Event{ID: 1, From: 0, To: 1})
	m.Deduplicate()
	if len(m.Events()) != 1 {
		t.Fatal()
	}
}

func TestManagerZeroValueAndLength(t *testing.T) {
	var m Manager
	if m.Add(Event{ID: 1, From: 2, To: 1}) {
		t.Fatal()
	}
	if m.Add(Event{ID: 1, From: 1, To: 3}) != true || m.Len() != 1 {
		t.Fatal()
	}
	if (Event{From: 3, To: 1}).Length() != 0 {
		t.Fatal("非法事件长度应为零")
	}
}

func TestManagerMaxEventsKeepsBoundForSingleMatch(t *testing.T) {
	m := New()
	m.SetMaxEvents(1)
	if !m.Add(Event{ID: 1, From: 2, To: 3, Flags: FlagSingleMatch}) {
		t.Fatal("首次单次事件应写入")
	}
	if m.Add(Event{ID: 2, From: 0, To: 1, Flags: FlagSingleMatch}) {
		t.Fatal("达到上限后不应新增其它单次事件")
	}
	if got := m.Len(); got != 1 {
		t.Fatalf("事件数量=%d", got)
	}
}

func TestManagerDeduplicateRetainsDistinctSOM(t *testing.T) {
	m := New()
	m.Add(Event{ID: 1, From: 0, To: 2, SOM: 0})
	m.Add(Event{ID: 1, From: 0, To: 2, SOM: 1})
	m.Deduplicate()
	if got := m.Len(); got != 2 {
		t.Fatalf("不同起始标记不应被合并: %d", got)
	}
}
func FuzzManager(f *testing.F) {
	f.Add(uint32(1), uint64(0))
	f.Fuzz(func(t *testing.T, id uint32, off uint64) {
		m := New()
		m.Add(Event{ID: id, From: off, To: off + 1})
		if len(m.Events()) != 1 {
			t.Fatal()
		}
	})
}
