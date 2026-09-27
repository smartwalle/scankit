package fuzzy

import "testing"

func TestDistanceRatio(t *testing.T) {
	if DistanceRatio([]byte("abc"), []byte("abc")) != 1 {
		t.Fatal()
	}
}
