package database

import "testing"

func TestCompatibilityAndScratchRequirement(t *testing.T) {
	a, b := New([]int{1}), New([]int{2})
	if !a.CompatibleWith(b) || a.ProgramCount() != 1 {
		t.Fatal("数据库兼容性错误")
	}
	a.SetScratchRequirement(8)
	if a.ScratchSufficient(7) || !a.ScratchSufficient(8) {
		t.Fatal("scratch 容量判断错误")
	}
}
