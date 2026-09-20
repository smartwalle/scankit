package scankit

import "testing"

func TestCompileRejectsInvalidFlagsAndOffsets(t *testing.T) {
	if _, err := Compile([]Expression{{Id: 1, Pattern: "x", Flags: CompileFlag(1 << 31)}}); err == nil {
		t.Fatal("unknown compile flag accepted")
	}
	if _, err := Compile([]Expression{{Id: 1, Pattern: "x", Flags: FlagUCP}}); err != nil {
		t.Fatalf("UCP without UTF8 rejected: %v", err)
	}
	if _, err := Compile([]Expression{{Id: 1, Pattern: "x", Ext: &ExpressionExt{Flags: ExtFlagMinOffset | ExtFlagMaxOffset, MinOffset: 8, MaxOffset: 2}}}); err == nil {
		t.Fatal("reversed offsets accepted")
	}
	if _, err := Compile([]Expression{{Id: 1, Pattern: ""}}); err == nil {
		t.Fatal("empty match accepted")
	}
	if _, err := Compile([]Expression{{Id: 1, Pattern: "", Flags: FlagAllowEmpty}}); err != nil {
		t.Fatalf("allow empty rejected: %v", err)
	}
}

func TestCompileRejectsCombinationDependencyCycle(t *testing.T) {
	_, err := Compile([]Expression{
		{Id: 1, Pattern: "2", Flags: FlagCombination},
		{Id: 2, Pattern: "3", Flags: FlagCombination},
		{Id: 3, Pattern: "1", Flags: FlagCombination},
	})
	if err == nil {
		t.Fatal("combination dependency cycle accepted")
	}
}

func TestCompileRejectsIncompatibleFlags(t *testing.T) {
	cases := []CompileFlag{
		FlagSingleMatch | FlagSOMLeftmost,
		FlagQuiet | FlagSOMLeftmost,
		FlagPrefilter | FlagSOMLeftmost,
		FlagCombination | FlagCaseless,
	}
	for _, flags := range cases {
		if _, err := Compile([]Expression{{Id: 1, Pattern: "a", Flags: flags}}); err == nil {
			t.Fatalf("incompatible flags accepted: %s", flags)
		}
	}
}

func TestCompileRejectsUnsupportedCombinationOperandsAndExtensions(t *testing.T) {
	cases := [][]Expression{
		{{Id: 1, Pattern: "a", Flags: FlagPrefilter}, {Id: 2, Pattern: "1", Flags: FlagCombination}},
		{{Id: 1, Pattern: "a", Flags: FlagSOMLeftmost}, {Id: 2, Pattern: "1", Flags: FlagCombination}},
		{{Id: 1, Pattern: "a"}, {Id: 2, Pattern: "1", Flags: FlagCombination}, {Id: 3, Pattern: "2", Flags: FlagCombination}},
		{{Id: 1, Pattern: "1", Flags: FlagCombination, Ext: &ExpressionExt{Flags: ExtFlagMinLength, MinLength: 1}}},
	}
	for _, expressions := range cases {
		if _, err := Compile(expressions); err == nil {
			t.Fatalf("unsupported combination accepted: %#v", expressions)
		}
	}
}

func TestCompileRejectsMinimumLengthAboveMaximumOffset(t *testing.T) {
	_, err := Compile([]Expression{{Id: 1, Pattern: "a", Ext: &ExpressionExt{Flags: ExtFlagMinLength | ExtFlagMaxOffset, MinLength: 2, MaxOffset: 1}}})
	if err == nil {
		t.Fatal("invalid extension bounds accepted")
	}
}

func TestCompileAcceptsPatternLengthWithinSourceSemantics(t *testing.T) {
	pattern := make([]byte, 16001)
	for i := range pattern {
		pattern[i] = 'a'
	}
	if _, err := Compile([]Expression{{Id: 1, Pattern: string(pattern)}}); err != nil {
		t.Fatalf("不应额外限制表达式长度: %v", err)
	}
}

func TestScannerValidateChecksDerivedIndexes(t *testing.T) {
	scanner, err := Compile([]Expression{{Id: 1, Pattern: "abc"}})
	if err != nil {
		t.Fatal(err)
	}
	scanner.candidateIDs[99] = struct{}{}
	if err := scanner.validate(); err == nil {
		t.Fatal("派生候选索引引用未知规则未拒绝")
	}
}
