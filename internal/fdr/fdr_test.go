package fdr

import (
	"github.com/smartwalle/scankit/internal/hwlm"
	"testing"
)

func TestMatcherFind(t *testing.T) {
	m := New([]hwlm.Literal{{ID: 1, Value: []byte("ab")}, {ID: 2, Value: []byte("bc")}})
	got := m.Find([]byte("abc"))
	if len(got) != 2 || got[0].ID != 1 || got[1].ID != 2 {
		t.Fatalf("%#v", got)
	}
}

func TestMatcherReverseAndMatchAt(t *testing.T) {
	m := New([]hwlm.Literal{{ID: 1, Value: []byte("ab")}, {ID: 2, Value: []byte("b")}})
	if got := m.MatchAt([]byte("ab"), 0); len(got) != 1 || got[0].ID != 1 {
		t.Fatalf("起点命中=%v", got)
	}
	if got := m.FindReverse([]byte("ab")); len(got) != 2 || got[0].To != 2 {
		t.Fatalf("反向命中=%v", got)
	}
}

func TestMatcherFindLimitsRejectNegative(t *testing.T) {
	m := New([]hwlm.Literal{{ID: 1, Value: []byte("a")}})
	if m.FindLimit([]byte("a"), -1) != nil || m.FindFromLimit([]byte("a"), 0, -1) != nil {
		t.Fatal("负数量限制应返回空")
	}
}

func TestMatcherSerializationAndOffset(t *testing.T) {
	m := New([]hwlm.Literal{{ID: 3, Value: []byte("ab")}})
	raw, err := m.Dump()
	if err != nil {
		t.Fatal(err)
	}
	copyM, err := Load(raw)
	if err != nil || copyM.CountByID([]byte("zab"), 3) != 1 {
		t.Fatal(err)
	}
	if len(copyM.FindFrom([]byte("abxab"), 2)) != 1 {
		t.Fatal("偏移过滤错误")
	}
	if (Match{From: 3, To: 1}).Length() != 0 {
		t.Fatal("非法命中长度错误")
	}
}

func TestMatcherAutomatonSharedPrefixesAndCaseless(t *testing.T) {
	m := New([]hwlm.Literal{{ID: 1, Value: []byte("he")}, {ID: 2, Value: []byte("her")}, {ID: 3, Value: []byte("hers")}, {ID: 4, Value: []byte("SHE"), CaseInsensitive: true}})
	got := m.Find([]byte("hers SHE"))
	if len(got) != 4 || got[0].ID != 1 || got[1].ID != 2 || got[2].ID != 3 || got[3].ID != 4 {
		t.Fatalf("unexpected automaton matches: %#v", got)
	}
	if got := (&Matcher{}).Find([]byte("anything")); len(got) != 0 {
		t.Fatalf("zero-value matcher matches: %#v", got)
	}
}
func FuzzMatcherFind(f *testing.F) {
	f.Add([]byte("abc"))
	f.Fuzz(func(t *testing.T, data []byte) {
		m := New([]hwlm.Literal{{ID: 1, Value: []byte("a")}})
		for _, x := range m.Find(data) {
			if x.From < 0 || x.To > len(data) {
				t.Fatal(x)
			}
		}
	})
}
func BenchmarkMatcherFind(b *testing.B) {
	m := New([]hwlm.Literal{{ID: 1, Value: []byte("abc")}})
	data := []byte("abc0123456789abc")
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = m.Find(data)
	}
}
