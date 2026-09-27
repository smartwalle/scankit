package database

import "testing"

func TestDatabaseValidation(t *testing.T) {
	d := New([]int{1, 2})
	if err := d.Validate(); err != nil {
		t.Fatal(err)
	}
	d.Version = 2
	if err := d.Validate(); err == nil {
		t.Fatal()
	}
}

func TestDatabaseProgramMutationRefreshesDigest(t *testing.T) {
	d := New([]int{1})
	if !d.AppendProgram(2) || !d.ReplaceProgram(0, 3) {
		t.Fatal("程序变更失败")
	}
	if d.DigestValue() != "" {
		t.Fatal("变更后摘要未清除")
	}
	if _, err := d.RefreshDigest(); err != nil || d.DigestValue() == "" || !d.VerifyDigest() {
		t.Fatalf("摘要刷新失败: %v", err)
	}
}
func FuzzDatabase(f *testing.F) {
	f.Add(1)
	f.Fuzz(func(t *testing.T, v int) {
		d := New([]int{v})
		if d.Len() != 1 {
			t.Fatal()
		}
	})
}
func TestEmpty(t *testing.T) {
	if !New[int](nil).Empty() {
		t.Fatal()
	}
}
