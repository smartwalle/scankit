package smallwrite

import "testing"

func TestProgram(t *testing.T) {
	p := New([]byte("123"))
	if !p.Eligible([]byte("12")) {
		t.Fatal()
	}
	if string(p.Run([]byte("x"))) != "x" {
		t.Fatal()
	}
	if !p.MatchAt([]byte("0123"), 1) || len(p.Find([]byte("123123"))) != 2 {
		t.Fatal("字节匹配结果错误")
	}
	raw, err := p.Dump()
	if err != nil {
		t.Fatal(err)
	}
	if q, err := Load(raw); err != nil || !q.MatchAt([]byte("123"), 0) {
		t.Fatal(err)
	}
}

func TestFindLimitRejectsNegative(t *testing.T) {
	if got := New([]byte("a")).FindLimit([]byte("a"), -1); got != nil {
		t.Fatalf("结果=%v", got)
	}
}

func TestLoadRejectsNullProgram(t *testing.T) {
	if _, err := Load([]byte("null")); err == nil {
		t.Fatal("null 程序未拒绝")
	}
}

func TestKMPOverlappingAndZeroValue(t *testing.T) {
	p := New([]byte("aaa"))
	if matches := p.Find([]byte("aaaaa")); len(matches) != 3 || matches[0] != 0 || matches[2] != 2 {
		t.Fatalf("overlapping matches=%v", matches)
	}
	zero := &Program{Bytes: []byte("ab")}
	if matches := zero.Find([]byte("zab")); len(matches) != 1 || matches[0] != 1 {
		t.Fatalf("zero-value program matches=%v", matches)
	}
}
func FuzzProgram(f *testing.F) {
	f.Add([]byte("abc"))
	f.Fuzz(func(t *testing.T, in []byte) {
		p := New(in)
		if out := p.Run(in); string(out) != string(in) {
			t.Fatal()
		}
	})
}
