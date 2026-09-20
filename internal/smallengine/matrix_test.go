package smallengine

import "testing"

func TestSelectionBoundaryAndEligibility(t *testing.T) {
	short, err := Compile([]byte("12345678"), 8)
	if err != nil || short.Kind != KindSmallWrite {
		t.Fatalf("短路径=%v,%v", short.Kind, err)
	}
	long, err := Compile([]byte("123456789"), 9)
	if err != nil || long.Kind != KindSmallBlock {
		t.Fatalf("长路径=%v,%v", long.Kind, err)
	}
	if short.Eligible([]byte("123456789")) || !long.Eligible([]byte("123456789")) {
		t.Fatal("容量判定错误")
	}
	if short.MatchAt([]byte("x12345678"), 1) != true || long.MatchAt([]byte("x123456789"), 1) != true {
		t.Fatal("匹配接口错误")
	}
}
