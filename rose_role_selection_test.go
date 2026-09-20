package scankit

import "testing"

// TestRoseProgramDeduplicatesScanEquivalentAlternatives 验证扫描等价的
// 分支在 Rose 程序构建阶段被合并成一个角色，避免同一文字被重复扫描，
// 同时保持扫描结果与单分支写法完全一致。
func TestRoseProgramDeduplicatesScanEquivalentAlternatives(t *testing.T) {
	equivalent, err := Compile([]Expression{{Id: 1, Pattern: `(?:ab)c|a(?:bc)`}})
	if err != nil {
		t.Fatal(err)
	}
	alternatives, ok := roseLiteralAlternatives(equivalent.rules[0].root)
	if !ok || len(alternatives) != 2 {
		t.Fatalf("测试模式应包含两个可转换分支: ok=%v count=%d", ok, len(alternatives))
	}
	program := equivalent.roseProgram()
	if program == nil {
		t.Fatal("等价分支规则未构建 Rose 程序")
	}
	if program.RoleCount() != 1 {
		t.Fatalf("等价分支应合并为单个角色: %d", program.RoleCount())
	}
	roles := program.RolesForReport(1)
	if len(roles) != 1 || string(roles[0].Literal) != "abc" {
		t.Fatalf("合并后的代表角色错误: %#v", roles)
	}
	if cheapest, ok := program.CheapestRoleForReport(1); !ok || cheapest.ID != roles[0].ID {
		t.Fatalf("代价最低角色选择错误: %#v ok=%v", cheapest, ok)
	}

	data := []byte("zabcabc abcc")
	got, err := equivalent.Scan(data)
	if err != nil {
		t.Fatal(err)
	}
	single, err := Compile([]Expression{{Id: 1, Pattern: `abc`}})
	if err != nil {
		t.Fatal(err)
	}
	want, err := single.Scan(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(want) {
		t.Fatalf("合并分支后结果数量变化: got=%v want=%v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("合并分支后结果不一致: got=%v want=%v", got, want)
		}
	}
}

// TestRoseProgramKeepsDistinctAlternatives 验证扫描特征不同的分支不会被误合并。
func TestRoseProgramKeepsDistinctAlternatives(t *testing.T) {
	scanner, err := Compile([]Expression{{Id: 1, Pattern: `ab|cd`}})
	if err != nil {
		t.Fatal(err)
	}
	program := scanner.roseProgram()
	if program == nil {
		t.Fatal("分支规则未构建 Rose 程序")
	}
	if program.RoleCount() != 2 {
		t.Fatalf("不同文字的分支不应被合并: %d", program.RoleCount())
	}
	if got := program.RolesForReport(1); len(got) != 2 {
		t.Fatalf("同一报告应保留两个角色: %#v", got)
	}
}

// TestRoseRoleSelectionDrivesScanResults 验证同一报告下的多个角色都能被
// 扫描路径正确定位，合并与代价选择不改变最终命中集合。
func TestRoseRoleSelectionDrivesScanResults(t *testing.T) {
	scanner, err := Compile([]Expression{{Id: 1, Pattern: `foo|bar`}})
	if err != nil {
		t.Fatal(err)
	}
	data := []byte("bar foo baz")
	got, err := scanner.Scan(data)
	if err != nil {
		t.Fatal(err)
	}
	gotRose := scanner.scanRoseInto(data, nil)
	if len(got) != 2 || len(gotRose) != 2 {
		t.Fatalf("扫描结果数量错误: scan=%v rose=%v", got, gotRose)
	}
	for i := range got {
		if got[i] != gotRose[i] {
			t.Fatalf("Rose 与 Scan 结果不一致: scan=%v rose=%v", got, gotRose)
		}
	}
	want := []Match{{Id: 1, From: 0, To: 3}, {Id: 1, From: 4, To: 7}}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("扫描结果错误: got=%v want=%v", got, want)
		}
	}
}
