package hwlm

import (
	"fmt"
	"reflect"
	"testing"

	"github.com/smartwalle/scankit/internal/parser"
	"github.com/smartwalle/scankit/internal/simd"
)

func TestExtractAndFindAll(t *testing.T) {
	root, err := parser.Parse("ab|cd")
	if err != nil {
		t.Fatal(err)
	}
	lits := Extract(root)
	if len(lits) != 2 {
		t.Fatalf("literals=%#v", lits)
	}
	if got := FindAll([]byte("zabcdab"), lits[0]); len(got) != 2 || got[0] != 1 || got[1] != 5 {
		t.Fatalf("got=%v", got)
	}
}

func TestLiteralIndexMatchAtAndIDs(t *testing.T) {
	i := NewIndex([]Literal{{ID: 2, Value: []byte("b")}, {ID: 1, Value: []byte("ab")}})
	if got := i.MatchAt([]byte("ab"), 0); len(got) != 1 || got[0].ID != 1 {
		t.Fatalf("起点候选=%v", got)
	}
	if ids := i.IDs(); len(ids) != 2 {
		t.Fatalf("编号=%v", ids)
	}
}

func TestLiteralIndexMatchAtHonorsCaseFolding(t *testing.T) {
	i := NewIndex([]Literal{{ID: 1, Value: []byte("Ab"), CaseInsensitive: true}})
	if got := i.MatchAt([]byte("aB"), 0); len(got) != 1 || got[0].ID != 1 {
		t.Fatalf("大小写折叠未生效=%v", got)
	}
}

func TestLiteralIndexAndFoldPrefix(t *testing.T) {
	root, _ := parser.Parse("Ab|ac")
	idx := NewIndex(Extract(root))
	if len(idx.Find([]byte("xxAbac"))) != 2 || idx.Clone() == nil {
		t.Fatal("候选索引错误")
	}
	if string(PrefixFold([]Literal{{Value: []byte("Ab")}, {Value: []byte("aC")}})) != "A" {
		t.Fatal("大小写公共前缀错误")
	}
	if !ContainsAt([]byte("xxAb"), 2, Literal{Value: []byte("ab"), CaseInsensitive: true}) {
		t.Fatal("偏移文字匹配错误")
	}
}

func TestLiteralIndexFindLimit(t *testing.T) {
	idx := NewIndex([]Literal{{ID: 1, Value: []byte("a")}})
	if got := idx.FindLimit([]byte("aa"), 1); len(got) != 1 {
		t.Fatalf("限量结果=%v", got)
	}
	if idx.FindLimit([]byte("a"), -1) != nil {
		t.Fatal("负限量应返回空")
	}
}

func TestFindByteRangeHandlesVectorTailAndInvalidRange(t *testing.T) {
	data := []byte("0123456789abcdefXYZ")
	got := FindByteRange(data, '3', 'C')
	want := []int{3, 4, 5, 6, 7, 8, 9}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("范围命中=%v, want=%v", got, want)
	}
	if got := FindByteRange(data, 'z', 'a'); got != nil {
		t.Fatalf("非法范围应为空: %v", got)
	}
}

func TestFindByteRangeSuperMatchesStandardPath(t *testing.T) {
	data := []byte("0123456789abcdefghijklmnopqrstuvwxyz0123456789")
	for _, bounds := range [][2]byte{{'0', '9'}, {'a', 'f'}, {'x', 'z'}, {0, 255}} {
		got := FindByteRangeSuper(data, bounds[0], bounds[1])
		want := FindByteRange(data, bounds[0], bounds[1])
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("范围 %q-%q: got=%v want=%v", bounds[0], bounds[1], got, want)
		}
	}
	if got := FindByteRangeSuper(data, 'z', 'a'); got != nil {
		t.Fatalf("非法范围=%v", got)
	}
}

func TestFindAllFromPastEndIsSafe(t *testing.T) {
	if got := FindAllFrom([]byte("abc"), Literal{Value: []byte("a")}, 99); got != nil {
		t.Fatalf("越界起点结果=%v", got)
	}
}

func TestFindAllFromSingleByteMatchesSuperVectorAndTail(t *testing.T) {
	data := []byte("00000000000000000000000000000000x000000000000000x")
	got := FindAllFrom(data, Literal{Value: []byte("x")}, 1)
	want := []int{32, 48}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("单字节超向量候选结果=%v want=%v", got, want)
	}
}

func TestFindByteRangeUsesWidePathWithTail(t *testing.T) {
	data := append(make([]byte, simd.SuperWidth+3), 'a', 'm', 'z')
	got := FindByteRange(data, 'm', 'z')
	want := []int{simd.SuperWidth + 4, simd.SuperWidth + 5}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("宽向量范围结果=%v want=%v", got, want)
	}
}

func FuzzFindByteRange(f *testing.F) {
	f.Add([]byte("abcXYZ012"), byte('a'), byte('z'))
	f.Fuzz(func(t *testing.T, data []byte, lo, hi byte) {
		got := FindByteRange(data, lo, hi)
		if lo > hi {
			if got != nil {
				t.Fatalf("非法范围返回=%v", got)
			}
			return
		}
		for _, off := range got {
			if off < 0 || off >= len(data) || data[off] < lo || data[off] > hi {
				t.Fatalf("越界或错误位置=%d", off)
			}
		}
	})
}
func FuzzFindAll(f *testing.F) {
	f.Add([]byte("abcabc"), []byte("abc"))
	f.Fuzz(func(t *testing.T, data, lit []byte) {
		if len(lit) == 0 {
			return
		}
		got := FindAll(data, Literal{Value: lit})
		for _, off := range got {
			if off < 0 || off+len(lit) > len(data) {
				t.Fatal(off)
			}
		}
	})
}
func BenchmarkFindAll(b *testing.B) {
	data := []byte("0123456789abcdef0123456789abcdef")
	lit := Literal{Value: []byte("0123")}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		_ = FindAll(data, lit)
	}
}
