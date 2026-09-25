package rose

import "testing"

// TestSingleRoleMatcherSemantics 锁定单角色程序的候选器语义：共享自动机只负责
// 定位候选，最终是否命中仍由 Role.Eligible 确认，因此重叠命中、相邻命中、大小写
// 折叠、锚定约束与无命中都必须与逐偏移确认一致。
func TestSingleRoleMatcherSemantics(t *testing.T) {
	cases := []struct {
		name    string
		role    Role
		data    string
		offsets []uint64
	}{
		{"overlapping", Role{ID: 1, Literal: []byte("aa")}, "aaaa", []uint64{0, 1, 2}},
		{"adjacent", Role{ID: 1, Literal: []byte("foo")}, "foo xfoo", []uint64{0, 5}},
		{"caseless", Role{ID: 1, Literal: []byte("FOO"), CaseInsensitive: true}, "foo FOO FoO", []uint64{0, 4, 8}},
		{"no match", Role{ID: 1, Literal: []byte("foo")}, "fo", nil},
		{"anchored miss", Role{ID: 1, Literal: []byte("foo"), Anchored: true}, "xfoo foo", nil},
		{"anchored hit", Role{ID: 1, Literal: []byte("foo"), Anchored: true}, "foo foo", []uint64{0}},
		{"non ascii caseless", Role{ID: 1, Literal: []byte("é"), CaseInsensitive: true}, "xÉx", []uint64{1}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			program := New([]Role{tc.role})
			got := program.FindMatches([]byte(tc.data))
			if len(got) != len(tc.offsets) {
				t.Fatalf("命中数量=%d %#v，期望 %v", len(got), got, tc.offsets)
			}
			for i := range got {
				if got[i].Offset != tc.offsets[i] {
					t.Fatalf("命中偏移=%v，期望 %v", got, tc.offsets)
				}
			}
		})
	}
}

// TestSingleRoleUsesSharedMatcher 断言可建字节自动机的单角色程序在构建期就固化
// 自动机，扫描时不再退化为逐偏移 Role.Eligible。非 ASCII 的大小写不敏感角色无法
// 建字节自动机，必须保持 nil 并回落到逐偏移确认。
func TestSingleRoleUsesSharedMatcher(t *testing.T) {
	ascii := New([]Role{{ID: 1, Literal: []byte("password")}})
	if ascii.matcher == nil {
		t.Fatal("ASCII 单角色应复用共享自动机")
	}
	caseless := New([]Role{{ID: 1, Literal: []byte("password"), CaseInsensitive: true}})
	if caseless.matcher == nil {
		t.Fatal("ASCII 大小写不敏感单角色应复用共享自动机")
	}
	unicode := New([]Role{{ID: 1, Literal: []byte("é"), CaseInsensitive: true}})
	if unicode.matcher != nil {
		t.Fatal("非 ASCII 大小写不敏感角色无法建字节自动机，matcher 应为 nil")
	}
}
