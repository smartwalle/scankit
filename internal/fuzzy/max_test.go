package fuzzy

import "testing"

func TestMaxDistance(t *testing.T) {
	if MaxDistance([]byte("a"), []byte("b")) != 1 {
		t.Fatal()
	}
}

func TestBandedDistanceAndWindowSearch(t *testing.T) {
	if got := EditDistanceBanded([]byte("kitten"), []byte("sitten"), 1); got != 1 {
		t.Fatalf("带宽距离=%d", got)
	}
	if got := FindWithinHamming([]byte("ab"), []byte("xxabac"), 0); len(got) != 1 || got[0] != 2 {
		t.Fatalf("窗口命中=%v", got)
	}
	if got := FindWithinEditPrefix([]byte("abc"), []byte("zabc"), 1); len(got) == 0 || got[len(got)-1] != 4 {
		t.Fatalf("前缀命中=%v", got)
	}
}
