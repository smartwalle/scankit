package scankit

import "testing"

func TestScanRoseConfirmedRoleHonorsOffsetConstraints(t *testing.T) {
	scanner, err := Compile([]Expression{{
		Id:      91,
		Pattern: `abc(?=x)`,
		Ext:     &ExpressionExt{Flags: ExtFlagMinOffset | ExtFlagMaxOffset, MinOffset: 3, MaxOffset: 3},
	}})
	if err != nil {
		t.Fatal(err)
	}
	// 候选文字 abc 从偏移 0 开始，结束偏移为 3，满足扩展范围。
	got := scanner.scanRoseInto([]byte("abcx"), nil)
	if len(got) != 1 || got[0] != (Match{Id: 91, From: 0, To: 3}) {
		t.Fatalf("unexpected rose matches: %#v", got)
	}
	// 同一候选结束偏移超出上限时必须被确认路径拒绝。
	scanner, err = Compile([]Expression{{
		Id:      92,
		Pattern: `abc(?=x)`,
		Ext:     &ExpressionExt{Flags: ExtFlagMinOffset | ExtFlagMaxOffset, MinOffset: 4, MaxOffset: 4},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if got = scanner.scanRoseInto([]byte("abcx"), nil); len(got) != 0 {
		t.Fatalf("offset constraint ignored: %#v", got)
	}
}

func TestScanRoseConfirmedRoleHonorsMinimumLength(t *testing.T) {
	scanner, err := Compile([]Expression{{
		Id:      93,
		Pattern: `abc(?=x)`,
		Ext:     &ExpressionExt{Flags: ExtFlagMinLength, MinLength: 4},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if got := scanner.scanRoseInto([]byte("abcx"), nil); len(got) != 0 {
		t.Fatalf("minimum length constraint ignored: %#v", got)
	}
}
