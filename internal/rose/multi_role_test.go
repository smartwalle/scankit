package rose

import "testing"

func TestMultiRoleCandidateResultsMatchRoleScan(t *testing.T) {
	program := New([]Role{
		{ID: 1, Literal: []byte("foo")},
		{ID: 2, Literal: []byte("BAR"), CaseInsensitive: true},
	})
	roles := program.FindMatches([]byte("xxfoo BAR foo"))
	if len(roles) != 3 {
		t.Fatalf("候选数量=%d, %#v", len(roles), roles)
	}
	if roles[0].RoleID != 1 || roles[0].Offset != 2 || roles[1].RoleID != 2 || roles[1].Offset != 6 || roles[2].Offset != 10 {
		t.Fatalf("候选顺序或位置错误: %#v", roles)
	}
}

func TestSingleRoleCandidateResults(t *testing.T) {
	program := New([]Role{{ID: 7, Literal: []byte("foo")}})
	if program.matcher == nil {
		t.Fatal("single role did not build candidate matcher")
	}
	got := program.FindMatches([]byte("foo xfoo"))
	if len(got) != 2 || got[0].Offset != 0 || got[1].Offset != 5 {
		t.Fatalf("single role results=%#v", got)
	}
}

func TestSingleRoleRangeAndLimit(t *testing.T) {
	program := New([]Role{{ID: 8, Literal: []byte("aa")}})
	got := program.FindMatchesRangeLimit([]byte("aaaa"), 1, 4, 1)
	if len(got) != 1 || got[0].Offset != 1 {
		t.Fatalf("区间限量结果=%#v", got)
	}
}

func TestSingleRoleEndRangeUsesExclusiveUpperBound(t *testing.T) {
	program := New([]Role{{ID: 9, Literal: []byte("aa")}})
	got := program.FindMatchesEndRange([]byte("aaaa"), 2, 4)
	if len(got) != 2 || got[0].Offset != 0 || got[1].Offset != 1 {
		t.Fatalf("结束区间结果=%#v", got)
	}
}
