package som

import "testing"

func TestValidCount(t *testing.T) {
	tr := New(2)
	tr.Set(0, 1)
	if tr.ValidCount() != 1 {
		t.Fatal()
	}
}
