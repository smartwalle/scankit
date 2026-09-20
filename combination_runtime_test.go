package scankit

import "testing"

func TestCombinationRuntime(t *testing.T) {
	if _, err := Compile([]Expression{{Id: 9, Pattern: "1", Flags: FlagCombination}}); err == nil {
		t.Fatal("未检测到未知组合引用")
	}
	s, err := Compile([]Expression{
		{Id: 1, Pattern: "cat"},
		{Id: 2, Pattern: "dog"},
		{Id: 3, Pattern: "1&2", Flags: FlagCombination},
	})
	if err != nil {
		t.Fatal(err)
	}
	matches, err := s.Scan([]byte("cat dog"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 3 || matches[2] != (Match{Id: 3, From: 4, To: 7}) {
		t.Fatalf("组合规则未按操作数历史触发: %#v", matches)
	}

	s, err = Compile([]Expression{
		{Id: 1, Pattern: "a"},
		{Id: 2, Pattern: "a"},
		{Id: 3, Pattern: "1&2", Flags: FlagCombination},
	})
	if err != nil {
		t.Fatal(err)
	}
	matches, err = s.Scan([]byte("a"))
	if err != nil || len(matches) != 3 || matches[2].Id != 3 {
		t.Fatalf("组合结果不正确: %#v, %v", matches, err)
	}
	s, err = Compile([]Expression{
		{Id: 1, Pattern: "a", Flags: FlagQuiet},
		{Id: 2, Pattern: "1", Flags: FlagCombination},
	})
	if err != nil {
		t.Fatal(err)
	}
	matches, err = s.Scan([]byte("a"))
	if err != nil || len(matches) != 1 || matches[0].Id != 2 {
		t.Fatalf("静默基础规则未参与组合: %#v, %v", matches, err)
	}
	if info, ok := s.expressionInfo(2); !ok || !info.Combination || info.Validate() != nil {
		t.Fatal("组合元数据无效")
	}
}

func TestCombinationNegativeReportsAtEndOfData(t *testing.T) {
	s, err := Compile([]Expression{{Id: 1, Pattern: "a", Flags: FlagQuiet}, {Id: 2, Pattern: "!1", Flags: FlagCombination}})
	if err != nil {
		t.Fatal(err)
	}
	matches, err := s.Scan([]byte("bbb"))
	if err != nil || len(matches) != 1 || matches[0] != (Match{Id: 2, From: 3, To: 3}) {
		t.Fatalf("negative EOD match mismatch: %v %#v", err, matches)
	}
	matches, err = s.Scan([]byte("aba"))
	if err != nil || len(matches) != 0 {
		t.Fatalf("negative combination ignored operand hit: %v %#v", err, matches)
	}
}

func TestCombinationOnlyReportsForRelevantOperands(t *testing.T) {
	s, err := Compile([]Expression{
		{Id: 1, Pattern: "a", Flags: FlagQuiet},
		{Id: 2, Pattern: "b", Flags: FlagQuiet},
		{Id: 3, Pattern: "c", Flags: FlagQuiet},
		{Id: 4, Pattern: "1&2", Flags: FlagCombination},
	})
	if err != nil {
		t.Fatal(err)
	}
	matches, err := s.Scan([]byte("abcc"))
	if err != nil || len(matches) != 1 || matches[0] != (Match{Id: 4, From: 1, To: 2}) {
		t.Fatalf("unrelated operand retriggered combination: %v %#v", err, matches)
	}
}

func TestCombinationSingleMatchAndExtensionGate(t *testing.T) {
	s, err := Compile([]Expression{
		{Id: 1, Pattern: "a"},
		{Id: 2, Pattern: "1", Flags: FlagCombination | FlagQuiet | FlagSingleMatch},
		{Id: 3, Pattern: "1", Flags: FlagCombination, Ext: &ExpressionExt{Flags: ExtFlagMinOffset, MinOffset: 2}},
	})
	if err != nil {
		t.Fatal(err)
	}
	matches, err := s.Scan([]byte("aaa"))
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, match := range matches {
		if match.Id == 3 {
			count++
		}
	}
	if count != 2 {
		t.Fatalf("combination gate mismatch: count=%d matches=%#v", count, matches)
	}
}

func TestNestedCombinationOperatorsPreservePositions(t *testing.T) {
	scanner, err := Compile([]Expression{
		{Id: 1, Pattern: "a", Flags: FlagQuiet},
		{Id: 2, Pattern: "b", Flags: FlagQuiet},
		{Id: 3, Pattern: "c", Flags: FlagQuiet},
		{Id: 4, Pattern: "(1&2)|3", Flags: FlagCombination},
	})
	if err != nil {
		t.Fatal(err)
	}
	matches, err := scanner.Scan([]byte("ab"))
	if err != nil || len(matches) != 1 || matches[0] != (Match{Id: 4, From: 1, To: 2}) {
		t.Fatalf("嵌套组合位置错误: %#v err=%v", matches, err)
	}
}

func TestCombinationPositiveBranchDoesNotRepeatAtEOD(t *testing.T) {
	scanner, err := Compile([]Expression{
		{Id: 1, Pattern: "a", Flags: FlagQuiet},
		{Id: 2, Pattern: "b", Flags: FlagQuiet},
		{Id: 3, Pattern: "1|!2", Flags: FlagCombination},
	})
	if err != nil {
		t.Fatal(err)
	}
	matches, err := scanner.Scan([]byte("a"))
	if err != nil || len(matches) != 1 || matches[0] != (Match{Id: 3, From: 0, To: 1}) {
		t.Fatalf("组合 EOD 重复报告=%v err=%v", matches, err)
	}
}
