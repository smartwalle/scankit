package rose

import "testing"

// TestRoleWeightPrefersSelectiveRoles 验证代价权值随文字长度和约束收紧而下降，
// 需要额外确认或大小写折叠时上升。
func TestRoleWeightPrefersSelectiveRoles(t *testing.T) {
	short := Role{ID: 1, Literal: []byte("ab")}
	long := Role{ID: 2, Literal: []byte("abcdefgh")}
	if long.Weight() >= short.Weight() {
		t.Fatalf("长文字代价权值应更低: short=%d long=%d", short.Weight(), long.Weight())
	}
	anchored := Role{ID: 3, Literal: []byte("ab"), Anchored: true}
	if anchored.Weight() >= short.Weight() {
		t.Fatalf("锚定角色代价权值应更低: anchored=%d plain=%d", anchored.Weight(), short.Weight())
	}
	endOfData := Role{ID: 4, Literal: []byte("ab"), EndOfData: true}
	if endOfData.Weight() >= short.Weight() {
		t.Fatalf("末尾约束角色代价权值应更低: eod=%d plain=%d", endOfData.Weight(), short.Weight())
	}
	bounded := Role{ID: 5, Literal: []byte("ab"), HasMaxOffset: true, MaxOffset: 8}
	if bounded.Weight() >= short.Weight() {
		t.Fatalf("带偏移上界角色代价权值应更低: bounded=%d plain=%d", bounded.Weight(), short.Weight())
	}
	folded := Role{ID: 6, Literal: []byte("ab"), CaseInsensitive: true}
	if folded.Weight() <= short.Weight() {
		t.Fatalf("大小写不敏感角色代价权值应更高: folded=%d plain=%d", folded.Weight(), short.Weight())
	}
	confirm := Role{ID: 7, Literal: []byte("ab"), Confirm: true}
	if confirm.Weight() <= short.Weight() {
		t.Fatalf("需要确认的角色代价权值应更高: confirm=%d plain=%d", confirm.Weight(), short.Weight())
	}
	if capped := (Role{ID: 8, Literal: []byte("0123456789012345678901234567890123456789012345678901234567890123456789")}).Cost().LiteralLength; capped != roleCostLengthCap {
		t.Fatalf("文字长度分量应封顶到 %d: %d", roleCostLengthCap, capped)
	}
}

// TestRoleScanEquivalentCoversFoldingIdentity 验证扫描等价判定：
// 报告编号、锚定、末尾、确认、偏移约束或文字不同都视为不等价，
// 不含 ASCII 字母时大小写折叠是恒等变换，可视为等价。
func TestRoleScanEquivalentCoversFoldingIdentity(t *testing.T) {
	base := Role{ID: 1, ReportID: 9, Literal: []byte("abc")}
	if !base.ScanEquivalent(Role{ID: 2, ReportID: 9, Literal: []byte("abc")}) {
		t.Fatal("相同扫描特征的角色应等价")
	}
	variants := []Role{
		{ID: 2, ReportID: 10, Literal: []byte("abc")},
		{ID: 2, ReportID: 9, Literal: []byte("abd")},
		{ID: 2, ReportID: 9, Literal: []byte("abc"), Anchored: true},
		{ID: 2, ReportID: 9, Literal: []byte("abc"), EndOfData: true},
		{ID: 2, ReportID: 9, Literal: []byte("abc"), Confirm: true},
		{ID: 2, ReportID: 9, Literal: []byte("abc"), MinOffset: 3},
		{ID: 2, ReportID: 9, Literal: []byte("abc"), HasMaxOffset: true, MaxOffset: 12},
		{ID: 2, ReportID: 9, Literal: []byte("ABC"), CaseInsensitive: true},
	}
	for _, variant := range variants {
		if base.ScanEquivalent(variant) {
			t.Fatalf("不应等价: %#v", variant)
		}
	}
	digits := Role{ID: 3, ReportID: 9, Literal: []byte("12")}
	foldedDigits := Role{ID: 4, ReportID: 9, Literal: []byte("12"), CaseInsensitive: true}
	if !digits.ScanEquivalent(foldedDigits) {
		t.Fatal("无字母文字的大小写折叠是恒等变换，应视为等价")
	}
}

// TestCheapestRoleForReportSelectsStableRepresentative 验证同一报告下
// 代价最低的角色被选为代表，代价相同时退回编号最小的角色。
func TestCheapestRoleForReportSelectsStableRepresentative(t *testing.T) {
	p := New([]Role{
		{ID: 5, ReportID: 9, Literal: []byte("ab")},
		{ID: 3, ReportID: 9, Literal: []byte("abcdef"), Anchored: true},
		{ID: 7, ReportID: 9, Literal: []byte("ab")},
		{ID: 2, ReportID: 4, Literal: []byte("zz")},
	})
	role, ok := p.CheapestRoleForReport(9)
	if !ok || role.ID != 3 {
		t.Fatalf("应选中最长且锚定的角色: %#v ok=%v", role, ok)
	}
	ordered := p.RolesForReport(9)
	if len(ordered) != 3 || ordered[0].ID != 3 || ordered[1].ID != 5 || ordered[2].ID != 7 {
		t.Fatalf("角色应按代价与编号升序: %#v", ordered)
	}
	if role := ordered[1]; role.ID != 5 {
		t.Fatalf("代价相同时应保留编号更小的角色: %#v", role)
	}
	if _, ok := p.CheapestRoleForReport(999); ok {
		t.Fatal("未知报告不应返回角色")
	}
	// 返回的快照必须与程序内部状态隔离。
	role.Literal[0] = 'z'
	if got, _ := p.CheapestRoleForReport(9); string(got.Literal) != "abcdef" {
		t.Fatalf("代表角色快照被调用方污染: %q", got.Literal)
	}
}

// TestRolesForReportTracksNormalizeAndLoad 验证角色索引在
// Normalize 与序列化往返后仍然可用。
func TestRolesForReportTracksNormalizeAndLoad(t *testing.T) {
	p := New([]Role{{ID: 1, ReportID: 9, Literal: []byte("ab")}})
	if len(p.RolesForReport(9)) != 1 {
		t.Fatal("Normalize 后角色索引不可用")
	}
	p.Normalize()
	if len(p.RolesForReport(9)) != 1 {
		t.Fatal("重复 Normalize 后角色索引不可用")
	}
	raw, err := p.Dump()
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(raw)
	if err != nil {
		t.Fatal(err)
	}
	if got := loaded.RolesForReport(9); len(got) != 1 || string(got[0].Literal) != "ab" {
		t.Fatalf("Load 后角色索引错误: %#v", got)
	}
}
