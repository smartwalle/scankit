package som

import "testing"

func TestValidValuesAndCompact(t *testing.T) {
	tracker := New(4)
	tracker.Set(1, 8)
	tracker.Set(3, 2)
	if got := tracker.ValidValues(); len(got) != 2 || got[0] != 2 || got[1] != 8 {
		t.Fatalf("值=%v", got)
	}
	m := tracker.Compact()
	if len(m) != 2 || tracker.Len() != 2 {
		t.Fatalf("映射=%v 槽位=%d", m, tracker.Len())
	}
}
