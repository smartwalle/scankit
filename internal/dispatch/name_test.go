package dispatch

import "testing"

func TestBackendName(t *testing.T) {
	if BackendName(BackendGeneric) != "generic" {
		t.Fatal()
	}
}
