package generic

import "testing"

func TestMaskRange(t *testing.T) {
	if MaskRange(2, 3) != 0x1c {
		t.Fatal()
	}
}

func TestRotateRightNegativeIsStable(t *testing.T) {
	var v Vector
	for i := range v {
		v[i] = byte(i)
	}
	if got := RotateRight(v, -1); got != v {
		t.Fatalf("负旋转=%v", got)
	}
}
