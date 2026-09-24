package parser

import "testing"

func TestNormalizeSequence(t *testing.T) {
	r, _ := Parse("abc")
	n := Normalize(r)
	if l, ok := n.(Literal); !ok || string(l.Value) != "abc" {
		t.Fatalf("%#v", n)
	}
}
func TestNormalizeFlattens(t *testing.T) {
	n := Normalize(Sequence{Elements: []Node{Literal{Value: []byte("a")}, Sequence{Elements: []Node{Literal{Value: []byte("b")}}}}})
	l, ok := n.(Literal)
	if !ok || string(l.Value) != "ab" {
		t.Fatalf("%#v", n)
	}
}

func TestNormalizeClassRangesAndAlternation(t *testing.T) {
	class := Normalize(Class{Ranges: []Range{{'d', 'f'}, {'a', 'c'}, {'c', 'd'}}}).(Class)
	if len(class.Ranges) != 1 || class.Ranges[0].Lo != 'a' || class.Ranges[0].Hi != 'f' {
		t.Fatalf("字符类区间未合并: %#v", class.Ranges)
	}
	alt := Normalize(Alternation{Options: []Node{Literal{Value: []byte("a")}, Literal{Value: []byte("a")}}})
	if _, ok := alt.(Literal); !ok {
		t.Fatalf("重复分支未消除: %#v", alt)
	}
}
func FuzzNormalize(f *testing.F) {
	f.Add("abc|def")
	f.Fuzz(func(_ *testing.T, p string) {
		r, e := Parse(p)
		if e != nil {
			return
		}
		_ = Normalize(r)
	})
}
