package rose

import (
	"encoding/json"
	"sort"
	"testing"

	"github.com/smartwalle/scankit/internal/nfagraph"
	"github.com/smartwalle/scankit/internal/parser"
)

func TestBuildDumpLoad(t *testing.T) {
	r, _ := parser.Parse("abc")
	g, _ := nfagraph.NewBuilder().Build(r)
	p, e := Build(g)
	if e != nil || len(p.Roles) != 1 || string(p.Roles[0].Literal) != "abc" {
		t.Fatalf("%v %#v", e, p)
	}
	d, e := p.Dump()
	if e != nil {
		t.Fatal(e)
	}
	q, e := Load(d)
	if e != nil || len(q.Roles) != 1 || string(q.Roles[0].Literal) != "abc" {
		t.Fatal(e)
	}
}

func TestProgramFindMatchesUsesSharedCandidateMatcher(t *testing.T) {
	p := New([]Role{
		{ID: 1, Literal: []byte("header")},
		{ID: 2, Literal: []byte("HeAd"), CaseInsensitive: true},
		{ID: 3, Literal: []byte("footer")},
	})
	matches := p.FindMatches([]byte("HEADER footer header"))
	if len(matches) != 4 || matches[0] != (State{RoleID: 2, Offset: 0}) || matches[2] != (State{RoleID: 1, Offset: 14}) || matches[3] != (State{RoleID: 2, Offset: 14}) {
		t.Fatalf("candidate matches: %#v", matches)
	}
}

func TestProgramFindMatchesRangeUsesCandidateRange(t *testing.T) {
	p := New([]Role{{ID: 1, Literal: []byte("ab")}, {ID: 2, Literal: []byte("bc")}})
	matches := p.FindMatchesRange([]byte("abxxbc"), 2, 6)
	if len(matches) != 1 || matches[0] != (State{RoleID: 2, Offset: 4}) {
		t.Fatalf("range candidate matches: %#v", matches)
	}
}

func TestBuildDerivesAbsoluteAnchorRoles(t *testing.T) {
	root, err := parser.Parse(`\Aabc\z`)
	if err != nil {
		t.Fatal(err)
	}
	g, err := nfagraph.NewBuilder().Build(root)
	if err != nil {
		t.Fatal(err)
	}
	p, err := Build(g)
	if err != nil || len(p.Roles) != 1 || !p.Roles[0].Anchored || !p.Roles[0].EndOfData {
		t.Fatalf("absolute role: %v %#v", err, p)
	}
	if got := p.FindMatches([]byte("xabc")); len(got) != 0 {
		t.Fatalf("absolute role matched non-whole input: %#v", got)
	}
	if got := p.FindMatches([]byte("abc")); len(got) != 1 || got[0].Offset != 0 {
		t.Fatalf("absolute role failed whole input: %#v", got)
	}
}

func TestRoleBoundsRoundTrip(t *testing.T) {
	p := New([]Role{{ID: 7, ReportID: 9, Literal: []byte("abc"), Anchored: true, EndOfData: true, MinOffset: 3, MaxOffset: 3, HasMaxOffset: true}})
	raw, err := p.Dump()
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(raw)
	if err != nil {
		t.Fatal(err)
	}
	role, ok := loaded.FindRole(7)
	if !ok || !role.Anchored || !role.EndOfData || !role.HasMaxOffset || role.MinOffset != 3 || role.MaxOffset != 3 {
		t.Fatalf("role bounds lost: %#v", role)
	}
	if got := loaded.FindMatches([]byte("abc")); len(got) != 1 {
		t.Fatalf("loaded role did not match: %#v", got)
	}
}

func TestLoadRejectsDuplicateRoles(t *testing.T) {
	p := New([]Role{{ID: 1, Literal: []byte("a")}})
	raw, err := p.Dump()
	if err != nil {
		t.Fatal(err)
	}
	var d map[string]any
	if err := json.Unmarshal(raw, &d); err != nil {
		t.Fatal(err)
	}
	roles := d["roles"].([]any)
	d["roles"] = append(roles, roles[0])
	raw, _ = json.Marshal(d)
	if _, err := Load(raw); err == nil {
		t.Fatal("重复角色未拒绝")
	}
}
func FuzzProgramDump(f *testing.F) {
	f.Add([]byte(`{"version":1,"roles":[]}`))
	f.Fuzz(func(_ *testing.T, d []byte) {
		if p, e := Load(d); e == nil {
			_ = p.Validate()
		}
	})
}

func TestFindMatchesLimitSortsBeforeApplyingLimit(t *testing.T) {
	p := New([]Role{{ID: 1, Literal: []byte("zz")}, {ID: 2, Literal: []byte("aa")}})
	states := p.FindMatchesLimit([]byte("aa zz"), 1)
	if len(states) != 1 || states[0].RoleID != 2 || states[0].Offset != 0 {
		t.Fatalf("限额未按稳定顺序截取: %#v", states)
	}
}

func TestBuildCompilerConformanceCorpus(t *testing.T) {
	cases := []struct {
		name      string
		pattern   string
		wantRoles []string
	}{
		{name: "literal", pattern: "abc", wantRoles: []string{"abc"}},
		{name: "alternation", pattern: "ab|cd", wantRoles: []string{"ab", "cd"}},
		{name: "finite repeat", pattern: "x{2}", wantRoles: []string{"xx"}},
		{name: "absolute bounds", pattern: `\Afoo\z`, wantRoles: []string{"foo"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root, err := parser.Parse(tc.pattern)
			if err != nil {
				t.Fatal(err)
			}
			graph, err := nfagraph.NewBuilder().Build(root)
			if err != nil {
				t.Fatal(err)
			}
			program, err := Build(graph)
			if err != nil {
				t.Fatal(err)
			}
			if err := program.Validate(); err != nil {
				t.Fatal(err)
			}
			got := make([]string, 0, len(program.Roles))
			for _, role := range program.Roles {
				got = append(got, string(role.Literal))
			}
			sort.Strings(got)
			want := append([]string(nil), tc.wantRoles...)
			sort.Strings(want)
			if len(got) != len(want) {
				t.Fatalf("角色数量=%v want=%v", got, want)
			}
			for i := range want {
				if got[i] != want[i] {
					t.Fatalf("角色文字=%v want=%v", got, want)
				}
			}
		})
	}
}

func BenchmarkProgramFindMatches(b *testing.B) {
	p := New([]Role{{ID: 1, Literal: []byte("header")}, {ID: 2, Literal: []byte("footer")}, {ID: 3, Literal: []byte("body")}})
	data := []byte("header body payload footer header body footer")
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		if len(p.FindMatches(data)) == 0 {
			b.Fatal("未产生角色命中")
		}
	}
}

func FuzzProgramFindMatches(f *testing.F) {
	f.Add([]byte("header body footer"))
	f.Add([]byte{})
	p := New([]Role{{ID: 1, Literal: []byte("header")}, {ID: 2, Literal: []byte("body")}, {ID: 3, Literal: []byte("footer")}})
	f.Fuzz(func(t *testing.T, data []byte) {
		for _, state := range p.FindMatches(data) {
			role, ok := p.FindRole(state.RoleID)
			if !ok || !role.Eligible(data, int(state.Offset)) {
				t.Fatalf("无效角色状态=%v", state)
			}
		}
	})
}
