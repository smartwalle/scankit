package rose

import "testing"

func TestRoleMatch(t *testing.T) {
	r := Role{Literal: []byte("ab")}
	if !r.MatchAt([]byte("zab"), 1) || r.MatchAt([]byte("a"), 0) {
		t.Fatal()
	}
}

func TestFindMatches(t *testing.T) {
	p := New([]Role{{ID: 7, Literal: []byte("aba")}})
	got := p.FindMatches([]byte("ababa"))
	if len(got) != 2 || got[0].Offset != 0 || got[1].Offset != 2 {
		t.Fatalf("%v", got)
	}
}

func TestRoleEligibility(t *testing.T) {
	p := New([]Role{
		{ID: 1, Literal: []byte("ab"), Anchored: true},
		{ID: 2, Literal: []byte("ab"), EndOfData: true},
		{ID: 3, Literal: []byte("xx"), MinOffset: 4, MaxOffset: 4, HasMaxOffset: true},
	})
	got := p.FindMatches([]byte("abxxab"))
	if len(got) != 3 || got[0] != (State{RoleID: 1, Offset: 0}) || got[1] != (State{RoleID: 3, Offset: 2}) || got[2] != (State{RoleID: 2, Offset: 4}) {
		t.Fatalf("eligible states=%#v", got)
	}
	if New([]Role{{ID: 1, Literal: []byte("a"), MinOffset: 2, MaxOffset: 1, HasMaxOffset: true}}).Validate() == nil {
		t.Fatal("invalid role bounds accepted")
	}
}

func TestProgramFindMatchesLimitRejectsNegative(t *testing.T) {
	p := New([]Role{{ID: 1, Literal: []byte("a")}})
	if p.FindMatchesLimit([]byte("a"), -1) != nil {
		t.Fatal("负结果限制应返回空")
	}
}

func TestFindMatchesLimitFallbackKeepsGlobalOrdering(t *testing.T) {
	p := New([]Role{
		{ID: 2, Literal: []byte("é"), CaseInsensitive: true},
		{ID: 1, Literal: []byte("a")},
	})
	got := p.FindMatchesLimit([]byte("aéa"), 2)
	if len(got) != 2 || got[0].Offset != 0 || got[1].Offset != 1 {
		t.Fatalf("回退结果未按偏移排序: %#v", got)
	}
}

func TestNilProgramFindMatchesRange(t *testing.T) {
	var p *Program
	if p.FindMatchesRange([]byte("a"), 0, 1) != nil {
		t.Fatal("空程序区间查询应返回空")
	}
}

func TestInstructionStateRejectsReportFields(t *testing.T) {
	p := New([]Role{{ID: 1, Literal: []byte("a")}, {ID: 2, Literal: []byte("b")}})
	for _, instruction := range []Instruction{
		{Kind: InstructionActivate, RoleID: 1, TargetID: 2, ReportID: 9},
		{Kind: InstructionTransition, RoleID: 1, TargetID: 2, Flags: 1},
		{Kind: InstructionActivate, RoleID: 1, TargetID: 2, IncludeSOM: true},
	} {
		if err := p.SetInstructions([]Instruction{instruction}); err == nil {
			t.Fatalf("状态指令携带报告字段未拒绝: %#v", instruction)
		}
	}
}

func TestRoleRangeQueriesUseMatcherOrdering(t *testing.T) {
	p := New([]Role{{ID: 2, Literal: []byte("a")}, {ID: 1, Literal: []byte("a")}})
	if got := p.FindMatchesRangeLimit([]byte("aa"), 0, 2, 2); len(got) != 2 || got[0].RoleID != 1 || got[1].RoleID != 2 {
		t.Fatalf("区间限量结果=%v", got)
	}
	if got := p.FindMatchesEndRange([]byte("aa"), 1, 2); len(got) != 2 || got[0].RoleID != 1 || got[1].RoleID != 2 {
		t.Fatalf("结束区间结果=%v", got)
	}
}
