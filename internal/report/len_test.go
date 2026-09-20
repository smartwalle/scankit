package report

import "testing"

func TestLen(t *testing.T) {
	m := New()
	m.Add(Event{ID: 1})
	if m.Len() != 1 {
		t.Fatal()
	}
}
