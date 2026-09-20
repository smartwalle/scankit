package fuzzy

import "testing"

func TestSimilar(t *testing.T) {
	if !Similar([]byte("abc"), []byte("abd"), 1) {
		t.Fatal()
	}
}
