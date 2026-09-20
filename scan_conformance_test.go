package scankit

import (
	"testing"

	"github.com/smartwalle/scankit/internal/parser"
)

func TestSingleBlockConformanceMatrix(t *testing.T) {
	cases := []struct {
		name string
		expr Expression
		data []byte
		want int
	}{
		{"utf8", Expression{Id: 1, Pattern: "é", Flags: FlagUTF8}, []byte("xé"), 1},
		{"boundary", Expression{Id: 2, Pattern: `\bcat\b`}, []byte("cat scatter"), 1},
		{"backref", Expression{Id: 3, Pattern: `(ab)\1`}, []byte("abab"), 1},
		{"repeat", Expression{Id: 4, Pattern: `a{2,3}`}, []byte("aaa"), 1},
		{"hamming", Expression{Id: 5, Pattern: "abcd", Ext: &ExpressionExt{Flags: ExtFlagHammingDistance, HammingDistance: 1}}, []byte("abxd"), 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			scanner, err := Compile([]Expression{tc.expr})
			if err != nil {
				t.Fatal(err)
			}
			matches, err := scanner.Scan(tc.data)
			if err != nil || len(matches) != tc.want {
				t.Fatalf("结果=%v，错误=%v", matches, err)
			}
		})
	}
}

func TestBinaryModeAcceptsInvalidUTF8Bytes(t *testing.T) {
	scanner, err := Compile([]Expression{{Id: 1, Pattern: "\\xff"}})
	if err != nil {
		t.Fatal(err)
	}
	matches, err := scanner.Scan([]byte{0xff})
	if err != nil || len(matches) != 1 || matches[0] != (Match{Id: 1, From: 0, To: 1}) {
		t.Fatalf("二进制模式错误: %#v, %v", matches, err)
	}
}

func TestSingleScanEmptyAndNULConformance(t *testing.T) {
	scanner, err := Compile([]Expression{
		{Id: 1, Pattern: `\x00`},
		{Id: 2, Pattern: `a*`, Flags: FlagAllowEmpty},
	})
	if err != nil {
		t.Fatal(err)
	}
	matches, err := scanner.Scan([]byte{0, 'a', 0})
	if err != nil {
		t.Fatal(err)
	}
	seenNUL := false
	for _, match := range matches {
		if match.Id == 1 {
			seenNUL = true
		}
		if match.From > match.To || match.To > 3 {
			t.Fatalf("NUL/空匹配产生越界区间: %#v", match)
		}
	}
	if !seenNUL {
		t.Fatalf("未保留 NUL 命中: %#v", matches)
	}
}

func TestFixedRepeatFuzzyUsesExpandedPattern(t *testing.T) {
	scanner, err := Compile([]Expression{{
		Id:      7,
		Pattern: `ab{2}`,
		Ext:     &ExpressionExt{Flags: ExtFlagHammingDistance, HammingDistance: 1},
	}})
	if err != nil {
		t.Fatal(err)
	}
	matches, err := scanner.Scan([]byte("abb abx"))
	if err != nil || len(matches) != 2 {
		t.Fatalf("固定重复模糊匹配=%v err=%v", matches, err)
	}
	if matches[0] != (Match{Id: 7, From: 0, To: 3}) || matches[1] != (Match{Id: 7, From: 4, To: 7}) {
		t.Fatalf("固定重复模糊区间=%v", matches)
	}
}

func TestAlternationFuzzyConfirmsEachLiteralBranch(t *testing.T) {
	scanner, err := Compile([]Expression{{Id: 8, Pattern: `cat|dog`, Ext: &ExpressionExt{Flags: ExtFlagHammingDistance, HammingDistance: 1}}})
	if err != nil {
		t.Fatal(err)
	}
	matches, err := scanner.Scan([]byte("bat dig cow"))
	if err != nil || len(matches) != 2 {
		t.Fatalf("分支模糊结果=%v err=%v", matches, err)
	}
	if matches[0].From != 0 || matches[1].From != 4 {
		t.Fatalf("分支模糊偏移=%v", matches)
	}
}

func TestFuzzyLiteralAlternativesDeduplicateAndRejectEmptyBranch(t *testing.T) {
	root, err := parser.Parse(`(?:cat|cat)`)
	if err != nil {
		t.Fatal(err)
	}
	parts := literalAlternatives(root)
	if len(parts) != 1 || string(parts[0]) != "cat" {
		t.Fatalf("文字分支未去重: %#v", parts)
	}
	empty, err := parser.Parse(`(?:a{0}|b)`)
	if err != nil {
		t.Fatal(err)
	}
	if got := literalAlternatives(empty); got != nil {
		t.Fatalf("包含空分支的模糊候选不应下沉: %#v", got)
	}
}

func TestHammingFixedClassUsesDirectConfirmation(t *testing.T) {
	scanner, err := Compile([]Expression{{
		Id:      9,
		Pattern: `a[bc]d`,
		Ext:     &ExpressionExt{Flags: ExtFlagHammingDistance, HammingDistance: 1},
	}})
	if err != nil {
		t.Fatal(err)
	}
	matches, err := scanner.Scan([]byte("abd axd azz"))
	if err != nil || len(matches) != 2 {
		t.Fatalf("字符类模糊结果=%v err=%v", matches, err)
	}
	if matches[0].From != 0 || matches[1].From != 4 {
		t.Fatalf("字符类模糊偏移=%v", matches)
	}
}

func TestEditFixedClassUsesDynamicConfirmation(t *testing.T) {
	scanner, err := Compile([]Expression{{
		Id:      10,
		Pattern: `a[bc]d`,
		Ext:     &ExpressionExt{Flags: ExtFlagEditDistance, EditDistance: 1},
	}})
	if err != nil {
		t.Fatal(err)
	}
	matches, err := scanner.Scan([]byte("abd axd abxd ax"))
	if err != nil || len(matches) != 3 {
		t.Fatalf("字符类编辑距离结果=%v err=%v", matches, err)
	}
	if matches[0].From != 0 || matches[1].From != 4 || matches[2].From != 8 {
		t.Fatalf("字符类编辑距离偏移=%v", matches)
	}
}

func TestHammingClassHonorsCaselessFlag(t *testing.T) {
	scanner, err := Compile([]Expression{{
		Id: 11, Pattern: `[a-c]x`, Flags: FlagCaseless,
		Ext: &ExpressionExt{Flags: ExtFlagHammingDistance, HammingDistance: 0},
	}})
	if err != nil {
		t.Fatal(err)
	}
	matches, err := scanner.Scan([]byte("AX dx"))
	if err != nil || len(matches) != 1 || matches[0].From != 0 {
		t.Fatalf("大小写字符类结果=%v err=%v", matches, err)
	}
}

func TestFuzzyNegatedClassUsesComplementMask(t *testing.T) {
	scanner, err := Compile([]Expression{{
		Id:      91,
		Pattern: `[^a]`,
		Ext:     &ExpressionExt{Flags: ExtFlagHammingDistance, HammingDistance: 0},
	}})
	if err != nil {
		t.Fatal(err)
	}
	matches, err := scanner.Scan([]byte("bab"))
	if err != nil || len(matches) != 2 || matches[0].From != 0 || matches[1].From != 2 {
		t.Fatalf("否定字符类模糊匹配错误: %v err=%v", matches, err)
	}
}

func TestHammingUppercaseClassHonorsCaselessFlag(t *testing.T) {
	scanner, err := Compile([]Expression{{
		Id: 12, Pattern: `[A-C]x`, Flags: FlagCaseless,
		Ext: &ExpressionExt{Flags: ExtFlagHammingDistance, HammingDistance: 0},
	}})
	if err != nil {
		t.Fatal(err)
	}
	matches, err := scanner.Scan([]byte("ax"))
	if err != nil || len(matches) != 1 {
		t.Fatalf("大写字符类大小写折叠错误: %v, %v", matches, err)
	}
}

func TestFuzzyAlternationWithClassesConfirmsOnlyValidBranches(t *testing.T) {
	scanner, err := Compile([]Expression{{
		Id: 13, Pattern: `(?:a[bc]d|x[yz])`,
		Ext: &ExpressionExt{Flags: ExtFlagHammingDistance, HammingDistance: 0},
	}})
	if err != nil {
		t.Fatal(err)
	}
	matches, err := scanner.Scan([]byte("abd axd xyy xzd"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 3 || matches[0] != (Match{Id: 13, From: 0, To: 3}) || matches[1] != (Match{Id: 13, From: 8, To: 10}) || matches[2] != (Match{Id: 13, From: 12, To: 14}) {
		t.Fatalf("带字符类分支的汉明确认结果错误: %#v", matches)
	}
}

func TestFuzzyAlternationWithClassesSupportsEditWidthChanges(t *testing.T) {
	scanner, err := Compile([]Expression{{
		Id: 14, Pattern: `(?:ab[cd]|x[yz])`,
		Ext: &ExpressionExt{Flags: ExtFlagEditDistance, EditDistance: 1},
	}})
	if err != nil {
		t.Fatal(err)
	}
	matches, err := scanner.Scan([]byte("abc abxc xyz xqz"))
	if err != nil {
		t.Fatal(err)
	}
	// 编辑距离允许插入、删除，因此同一起点可能产生多个合法结束偏移。
	for _, match := range matches {
		if match.Id != 14 || match.To < match.From || match.To-match.From == 0 {
			t.Fatalf("编辑分支产生非法区间: %#v", matches)
		}
	}
	if len(matches) == 0 {
		t.Fatalf("编辑分支未产生任何合法确认: %#v", matches)
	}
}

func TestEditClassConfirmationRejectsInvalidStart(t *testing.T) {
	root, err := parser.Parse(`a[bc]d`)
	if err != nil {
		t.Fatal(err)
	}
	atoms, ok := fuzzyAtoms(root)
	if !ok {
		t.Fatal("字符类模式未生成固定宽度原子")
	}
	if ends, matched := matchEditAtoms(atoms, []byte("axd"), 4, 1, 0); matched || ends != nil {
		t.Fatalf("越界起点不应进入模糊确认: ends=%v matched=%v", ends, matched)
	}
}

func TestFuzzyWildcardAtomHonorsDotAll(t *testing.T) {
	without, err := Compile([]Expression{{Id: 31, Pattern: `a.b`, Ext: &ExpressionExt{Flags: ExtFlagHammingDistance, HammingDistance: 0}}})
	if err != nil {
		t.Fatal(err)
	}
	got, err := without.Scan([]byte("a\nb axb"))
	if err != nil || len(got) != 1 || got[0].From != 4 {
		t.Fatalf("通配原子换行语义错误: %#v, %v", got, err)
	}
	with, err := Compile([]Expression{{Id: 32, Pattern: `a.b`, Flags: FlagDotAll, Ext: &ExpressionExt{Flags: ExtFlagHammingDistance, HammingDistance: 0}}})
	if err != nil {
		t.Fatal(err)
	}
	got, err = with.Scan([]byte("a\nb"))
	if err != nil || len(got) != 1 || got[0].From != 0 || got[0].To != 3 {
		t.Fatalf("通配原子 DotAll 语义错误: %#v, %v", got, err)
	}
}

func BenchmarkEditClassConfirmation(b *testing.B) {
	root, err := parser.Parse(`a[bc]d`)
	if err != nil {
		b.Fatal(err)
	}
	atoms, ok := fuzzyAtoms(root)
	if !ok {
		b.Fatal("字符类模式未生成固定宽度原子")
	}
	data := []byte("axd")
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = matchEditAtoms(atoms, data, 0, 1, 0)
	}
}
