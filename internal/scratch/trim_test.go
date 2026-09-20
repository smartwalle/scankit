package scratch

import "testing"

func TestTrimAndCopyInto(t *testing.T) {
	s := New()
	s.ReserveAll(32, 8, 4, 16)
	s.Bytes = append(s.Bytes[:0], []byte("abc")...)
	if s.TotalCapacity() < 32 {
		t.Fatal("容量准备失败")
	}
	s.Trim()
	if string(s.Bytes) != "abc" || cap(s.Bytes) > len(s.Bytes)*3 {
		t.Fatal("Trim 内容异常")
	}
	d := New()
	if !s.CopyInto(d) || string(d.Bytes) != "abc" {
		t.Fatal("复制失败")
	}
}
