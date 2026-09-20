package generic

import "testing"

func TestSafeLoadsAndMasks(t *testing.T) {
	data := []byte("abcdefghijklmnopXYZ")
	v, ok := Load(data, 0)
	if !ok || string(v[:]) != "abcdefghijklmnop" {
		t.Fatalf("load = %q, ok=%v", v, ok)
	}
	if _, ok := Load(data, len(data)-1); ok {
		t.Fatal("short load succeeded")
	}
	partial := PartialLoad(data, len(data)-3)
	if partial[0] != 'X' || partial[1] != 'Y' || partial[2] != 'Z' || partial[3] != 0 {
		t.Fatalf("partial load = %v", partial)
	}
	if got := EqualByteMask(v, 'a'); got != 1 || PopCount(got) != 1 {
		t.Fatalf("mask = %x", got)
	}
}

func TestStoreSafeLoadAndPermute(t *testing.T) {
	data := []byte("abcdefghijklmnop")
	v, ok := Load(data, 0)
	if !ok {
		t.Fatal("完整加载失败")
	}
	got := make([]byte, len(data))
	if !Store(got, 0, v) || string(got) != string(data) {
		t.Fatalf("向量写回错误: %q", got)
	}
	if Store(got, 1, v) || Store(got, -1, v) {
		t.Fatal("越界写回未拒绝")
	}
	if SafeLoad(data, -1) != (Vector{}) {
		t.Fatal("非法安全加载未返回零向量")
	}
	var index Vector
	for i := range index {
		index[i] = byte(Width - 1 - i)
	}
	if Permute(v, index) != Reverse(v) {
		t.Fatal("置换语义错误")
	}
}

func TestVectorOperations(t *testing.T) {
	a, b := Vector{1, 2, 3}, Vector{3, 2, 1}
	if got := EqualMask(a, b); got != 0xfffa {
		t.Fatalf("equal mask = %x", got)
	}
	if got := ShiftLeft(a, 1); got[0] != 2 || got[1] != 4 {
		t.Fatalf("shift = %v", got)
	}
	if got := Shuffle(a, Vector{2, 1, 0}); got[0] != 3 || got[2] != 1 {
		t.Fatalf("shuffle = %v", got)
	}
}

func TestLastEqualAndRange(t *testing.T) {
	v := Broadcast(1)
	v[3] = 2
	if index, ok := LastEqualByte(v, 1); !ok || index != 15 {
		t.Fatalf("最后匹配=%d,%v", index, ok)
	}
	if !AnyEqualRange(v, 2, 3, 1) || AnyEqualRange(v, 2, 0, 3) {
		t.Fatal("区间比较错误")
	}
}
