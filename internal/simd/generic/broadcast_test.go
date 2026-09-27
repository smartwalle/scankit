package generic

import "testing"

func TestBroadcast(t *testing.T) {
	if Broadcast(3)[7] != 3 {
		t.Fatal()
	}
}
