package simd

import "testing"

func TestDefaultBackend(t *testing.T) {
	b := Default()
	v, ok := b.Load([]byte("abcdefghijklmnop"), 0)
	if !ok || b.EqualByteMask(v, 'a') != 1 {
		t.Fatal()
	}
	if b.InRangeMask(v, 'a', 'c') != 7 {
		t.Fatalf("范围掩码错误: %x", b.InRangeMask(v, 'a', 'c'))
	}
}
func FuzzBackend(f *testing.F) {
	f.Add([]byte("abc"), 0)
	f.Fuzz(func(_ *testing.T, data []byte, off int) { b := Default(); _ = b.PartialLoad(data, off) })
}
