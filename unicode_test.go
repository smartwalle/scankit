package scankit

import "testing"

func TestUnicodeProperty(t *testing.T) {
	s, err := Compile([]Expression{{Id: 1, Pattern: `\p{L}+`, Flags: FlagUTF8 | FlagUCP}})
	if err != nil {
		t.Fatal(err)
	}
	m, err := s.Scan([]byte("123 中文 abc"))
	if err != nil || len(m) != 2 {
		t.Fatalf("%v %#v", err, m)
	}
}

func TestUnicodeBoundaryRejectsContinuationBytePosition(t *testing.T) {
	data := []byte("中")
	if wordBefore(data, 1, FlagUTF8) || wordAfter(data, 1, FlagUTF8) {
		t.Fatal("续字节位置被错误识别为单词边界")
	}
	s, err := Compile([]Expression{{Id: 1, Pattern: `\b中`, Flags: FlagUTF8 | FlagUCP}})
	if err != nil {
		t.Fatal(err)
	}
	if got, err := s.Scan([]byte("中")); err != nil || len(got) != 1 || got[0].From != 0 {
		t.Fatalf("Unicode 边界结果=%v,%v", got, err)
	}
}

func TestUnicodePropertyAliases(t *testing.T) {
	s, err := Compile([]Expression{{Id: 1, Pattern: `\p{ASCII}+`, Flags: FlagUTF8}})
	if err != nil {
		t.Fatal(err)
	}
	m, err := s.Scan([]byte("abc"))
	if err != nil || len(m) == 0 {
		t.Fatalf("%v %#v", err, m)
	}
}

func TestUnicodeScriptProperty(t *testing.T) {
	s, err := Compile([]Expression{{Id: 1, Pattern: `\p{Greek}`, Flags: FlagUTF8}})
	if err != nil {
		t.Fatal(err)
	}
	matches, err := s.Scan([]byte("AΩ"))
	if err != nil || len(matches) != 1 || matches[0].From != 1 {
		t.Fatalf("Unicode 脚本属性结果错误: %v %#v", err, matches)
	}
}

func TestUnicodePropertyQualifiedAliases(t *testing.T) {
	for _, pattern := range []string{`\p{Script=Greek}`, `\p{sc=Greek}`, `\p{gc=Lu}`} {
		s, err := Compile([]Expression{{Id: 1, Pattern: pattern, Flags: FlagUTF8}})
		if err != nil {
			t.Fatalf("属性 %s 编译失败: %v", pattern, err)
		}
		if matches, err := s.Scan([]byte("AΩ")); err != nil || len(matches) == 0 {
			t.Fatalf("属性 %s 结果错误: %v %#v", pattern, err, matches)
		}
	}
}

func TestUnicodeCharacterShorthandsWithUCP(t *testing.T) {
	cases := []struct {
		pattern string
		input   string
		count   int
	}{
		{`\w+`, "中文 123", 2},
		{`\d+`, "١٢٣ abc", 1},
		{`\s+`, "a\u2003b", 1},
		{`\D+`, "١a", 1},
	}
	for _, tc := range cases {
		s, err := Compile([]Expression{{Id: 1, Pattern: tc.pattern, Flags: FlagUTF8 | FlagUCP}})
		if err != nil {
			t.Fatalf("compile %q: %v", tc.pattern, err)
		}
		matches, err := s.Scan([]byte(tc.input))
		if err != nil || len(matches) != tc.count {
			t.Fatalf("scan %q: %v %#v", tc.pattern, err, matches)
		}
	}
}

func TestBracedHexUTF8Literal(t *testing.T) {
	s, err := Compile([]Expression{{Id: 1, Pattern: `\x{4e2d}\x{6587}`, Flags: FlagUTF8}})
	if err != nil {
		t.Fatal(err)
	}
	matches, err := s.Scan([]byte("中文"))
	if err != nil || len(matches) != 1 || matches[0] != (Match{Id: 1, From: 0, To: 6}) {
		t.Fatalf("braced hex UTF8 mismatch: %v %#v", err, matches)
	}
}

func TestUTF8NegatedClassConsumesWholeRune(t *testing.T) {
	s, err := Compile([]Expression{{Id: 1, Pattern: `\N+`, Flags: FlagUTF8}})
	if err != nil {
		t.Fatal(err)
	}
	matches, err := s.Scan([]byte("中文\n"))
	if err != nil || len(matches) != 1 || matches[0] != (Match{Id: 1, From: 0, To: 6}) {
		t.Fatalf("UTF8 non-newline mismatch: %v %#v", err, matches)
	}
}

func TestUCPWordBoundaryTreatsCombiningMarkAsWord(t *testing.T) {
	s, err := Compile([]Expression{{Id: 1, Pattern: "a\\b\u0301", Flags: FlagUTF8 | FlagUCP}})
	if err != nil {
		t.Fatal(err)
	}
	matches, err := s.Scan([]byte("a\u0301"))
	if err != nil || len(matches) != 0 {
		t.Fatalf("UCP word boundary split combining mark: %v %#v", err, matches)
	}
}
