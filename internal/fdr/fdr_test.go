package fdr

import (
	"bytes"
	"slices"
	"sort"
	"testing"

	"github.com/smartwalle/scankit/internal/hwlm"
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
	for b.Loop() {
		_ = m.Find(data)
	}
}

// TestMatcherSkipPathsAgreeWithBruteForce 覆盖单字节根、多字节根整窗跳过以及
// 空自动机三条快速路径，并逐长度验证它们与逐位置暴力匹配的候选集合一致。
func TestMatcherSkipPathsAgreeWithBruteForce(t *testing.T) {
	cases := []struct {
		name     string
		literals []hwlm.Literal
	}{
		{name: "single_root", literals: []hwlm.Literal{{ID: 1, Value: []byte("@")}}},
		{name: "single_root_shared_prefix", literals: []hwlm.Literal{{ID: 1, Value: []byte("@")}, {ID: 2, Value: []byte("a@b")}}},
		{name: "multi_root", literals: []hwlm.Literal{{ID: 1, Value: []byte("18")}, {ID: 2, Value: []byte("29")}}},
		{name: "caseless_only", literals: []hwlm.Literal{{ID: 1, Value: []byte("Ab"), CaseInsensitive: true}}},
		{name: "empty_sensitive", literals: []hwlm.Literal{{ID: 1, Value: []byte("Zz"), CaseInsensitive: true}}},
		{name: "empty_folded", literals: []hwlm.Literal{{ID: 1, Value: []byte("Zz")}}},
	}
	corpus := []byte("a@b18@18 29@29 Zz AB ab zz AB@18@29 @")
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			matcher := New(test.literals)
			for length := 0; length <= len(corpus); length++ {
				data := corpus[:length]
				got := matcher.Find(data)
				want := bruteForceMatches(test.literals, data)
				if !slices.Equal(got, want) {
					t.Fatalf("长度 %d 命中不一致: got=%v want=%v", length, got, want)
				}
			}
		})
	}
}

// bruteForceMatches 逐位置暴力匹配并复现 FindInto 的排序去重语义。
func bruteForceMatches(literals []hwlm.Literal, data []byte) []Match {
	out := make([]Match, 0)
	for _, literal := range literals {
		for start := 0; start+len(literal.Value) <= len(data); start++ {
			window := data[start : start+len(literal.Value)]
			if literal.CaseInsensitive {
				if !bytes.EqualFold(window, literal.Value) {
					continue
				}
			} else if !bytes.Equal(window, literal.Value) {
				continue
			}
			out = append(out, Match{ID: literal.ID, From: start, To: start + len(literal.Value)})
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].From != out[j].From {
			return out[i].From < out[j].From
		}
		if out[i].ID != out[j].ID {
			return out[i].ID < out[j].ID
		}
		return out[i].To < out[j].To
	})
	deduped := out[:0]
	for _, match := range out {
		if len(deduped) > 0 && deduped[len(deduped)-1] == match {
			continue
		}
		deduped = append(deduped, match)
	}
	return deduped
}
