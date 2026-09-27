package report

import "testing"

func TestDrainLimitRemovesStablePrefix(t *testing.T) {
	m := New()
	m.Add(Event{ID: 2, From: 2, To: 3})
	m.Add(Event{ID: 1, From: 0, To: 1})
	got := m.DrainLimit(1)
	if len(got) != 1 || got[0].ID != 1 || m.Len() != 1 {
		t.Fatalf("drain=%v remain=%d", got, m.Len())
	}
}
