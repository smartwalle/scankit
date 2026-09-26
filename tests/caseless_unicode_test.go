// caseless_unicode_test：含非 ASCII 字节的忽略大小写文字在「Rose 直通」与
// 「确认程序」两条路径上的结果一致性。
//
// 背景：`(?i)école` 这类规则单独成批时由 Rose 角色匹配（按 rune 做 Unicode
// 折叠），一旦与其它规则同批导致 Rose 让位，就会改走确认程序。确认程序的
// 字面量比较原本只做字节级 ASCII 折叠，于是「école」匹配不到「ÉCOLE」，
// 造成「同一条规则的结果随同批其它规则变化」的漏报。本文件把两条路径的
// 结果一致性固定下来，避免该缺陷回归。

package tests

import (
	"fmt"
	"strings"
	"testing"

	"github.com/smartwalle/scankit"
)

// caselessUnicodePatterns 是需要按 rune 折叠的非 ASCII 忽略大小写规则。
var caselessUnicodePatterns = []scankit.Expression{
	{Id: 1, Pattern: `(?i)école`},
	{Id: 1, Pattern: `école`, Flags: scankit.CompileCaseless},
	{Id: 1, Pattern: `(?i)café`},
	{Id: 1, Pattern: `(?i)Straße`},
	{Id: 1, Pattern: `(?i)Здравствуйте`},
	{Id: 1, Pattern: `(?i)é`},
	{Id: 1, Pattern: `(?i)içerik`},
	{Id: 1, Pattern: `(?i)münchen[0-9]`},
	{Id: 1, Pattern: `(?i)ÉÇÀ\\d`},
}

// caselessUnicodeCorpora 覆盖大小写变体、命中/未命中、非 UTF-8 字节与多命中。
var caselessUnicodeCorpora = []string{
	"ÉCOLE", "école", "École", "éCOLE", "xx ÉCOLE yy", "ÉCOLE école",
	"CAFÉ", "café", "Café", "CAFÉ CAFÉ",
	"STRASSE", "Straße", "straße", "straße STRASSE",
	"ЗДРАВСТВУЙТЕ", "Здравствуйте", "здравствуйте",
	"É", "é", "İÇERİK", "içerik", "İçerik",
	"MÜNCHEN7", "münchen7", "München7", "MÜNCHENX", "MÜNCHEN77",
	"ÉÇÀ7", "éçà7", "ÉçÀ7",
	"", "....", "ÉCOLE\nCAFÉ", "\xff\xfe école \x80", strings.Repeat("ÉCOLE ", 40),
}

// caselessUnicodeDecorators 是会让 Rose 让位的同批规则：任意一条存在时整批
// 改走确认程序，因此规则 1 的结果必须与单独成批时完全一致。
var caselessUnicodeDecorators = []struct {
	name  string
	extra []scankit.Expression
}{
	{"none", nil},
	{"literal", []scankit.Expression{{Id: 2, Pattern: `zzz`}}},
	{"class", []scankit.Expression{{Id: 2, Pattern: `[0-9]+`}}},
	{"anchored", []scankit.Expression{{Id: 2, Pattern: `\Azzz`}}},
	{"end-anchored", []scankit.Expression{{Id: 2, Pattern: `zzz\z`}}},
	{"literal+class", []scankit.Expression{{Id: 2, Pattern: `zzz`}, {Id: 3, Pattern: `[0-9]+`}}},
	{"alternation", []scankit.Expression{{Id: 2, Pattern: `zzz|yyy`}}},
	{"word-boundary", []scankit.Expression{{Id: 2, Pattern: `\bword\b`}}},
	{"multiline", []scankit.Expression{{Id: 2, Pattern: `(?m)^x`}}},
	{"repeat", []scankit.Expression{{Id: 2, Pattern: `x*y+`}}},
}

// TestCaselessNonASCIIAgreesAcrossPaths 断言规则 1 在「Rose 直通」（单独成批）
// 与「确认程序」（与任意诱饵规则同批）下的命中完全一致。
func TestCaselessNonASCIIAgreesAcrossPaths(t *testing.T) {
	for _, pattern := range caselessUnicodePatterns {
		for _, corpus := range caselessUnicodeCorpora {
			want := caselessMatchesFor(t, []scankit.Expression{pattern}, 1, corpus)
			for _, decorator := range caselessUnicodeDecorators {
				expressions := append([]scankit.Expression{pattern}, decorator.extra...)
				got := caselessMatchesFor(t, expressions, 1, corpus)
				if fmt.Sprint(got) != fmt.Sprint(want) {
					t.Fatalf("规则 %q 语料 %q 诱饵 %s：命中 %v，单独成批为 %v",
						pattern.Pattern, corpus, decorator.name, got, want)
				}
			}
		}
	}
}

// TestCaselessNonASCIIExpandsToCaseVariants 显式固定「非 ASCII 折叠」的语义：
// 「école」必须命中「ÉCOLE」，且不能命中「ecole」（é 与 e 不是同一等价类）。
func TestCaselessNonASCIIExpandsToCaseVariants(t *testing.T) {
	scanner, err := scankit.Compile([]scankit.Expression{
		{Id: 1, Pattern: `(?i)école`},
		{Id: 2, Pattern: `[0-9]+`},
	})
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	cases := []struct {
		input string
		want  bool
	}{
		{"ÉCOLE", true},
		{"école", true},
		{"École", true},
		{"xÉCOLEy", true},
		{"ecole", false},
		{"ECOLE", false},
	}
	for _, tc := range cases {
		matches, err := scanner.Scan([]byte(tc.input))
		if err != nil {
			t.Fatalf("Scan(%q): %v", tc.input, err)
		}
		hit := false
		for _, m := range matches {
			if m.Id == 1 {
				hit = true
			}
		}
		if hit != tc.want {
			t.Errorf("Scan(%q) 命中 = %v，期望 %v（全部命中 %v）", tc.input, hit, tc.want, matches)
		}
	}
}

func caselessMatchesFor(t *testing.T, expressions []scankit.Expression, wantID uint32, corpus string) []scankit.Match {
	t.Helper()
	scanner, err := scankit.Compile(expressions)
	if err != nil {
		t.Fatalf("Compile(%v): %v", expressions, err)
	}
	matches, err := scanner.ScanInto([]byte(corpus), nil)
	if err != nil {
		t.Fatalf("ScanInto(%q): %v", corpus, err)
	}
	out := make([]scankit.Match, 0, len(matches))
	for _, m := range matches {
		if m.Id == wantID {
			out = append(out, m)
		}
	}
	return out
}

// BenchmarkCaselessNonASCIIRoseScan 覆盖「单条非 ASCII 忽略大小写文字单独成批」的
// 端到端扫描：该形态由 Rose 直通路径承担，逐偏移确认前先做首字节廉价否定。
func BenchmarkCaselessNonASCIIRoseScan(b *testing.B) {
	scanner, err := scankit.Compile([]scankit.Expression{{Id: 1, Pattern: `(?i)école`}})
	if err != nil {
		b.Fatal(err)
	}
	data := []byte(strings.Repeat("The quick brown fox jumps over the lazy dog. ", 1560))
	b.SetBytes(int64(len(data)))
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		if _, err := scanner.ScanInto(data, nil); err != nil {
			b.Fatal(err)
		}
	}
}

// caselessNonASCIIFallback50Expressions 是 50 条非 ASCII 忽略大小写规则加一条
// 诱饵：每条规则的首字节集合收窄前都是完整 256 字节，用来量化回退路径
// `confirmIndexed` 的「每起点 × 每规则」确认开销。
func caselessNonASCIIFallback50Expressions() []scankit.Expression {
	expressions := make([]scankit.Expression, 0, 51)
	for i := range 50 {
		expressions = append(expressions, scankit.Expression{
			Id:      uint32(i + 1),
			Pattern: `(?i)école` + string(rune('A'+i%26)),
		})
	}
	return append(expressions, scankit.Expression{Id: 100, Pattern: `[0-9]+`})
}

// BenchmarkCaselessNonASCIIFallbackScan50 与 BenchmarkCaselessNonASCIIFallbackScan
// 同语料，但用 50 条非 ASCII 忽略大小写规则把回退路径的确认次数放大 50 倍（见
// 问题与排查计划 §62.3）。
func BenchmarkCaselessNonASCIIFallbackScan50(b *testing.B) {
	scanner, err := scankit.Compile(caselessNonASCIIFallback50Expressions())
	if err != nil {
		b.Fatal(err)
	}
	data := []byte(strings.Repeat("The quick brown fox jumps over the lazy dog. ", 1560))
	b.SetBytes(int64(len(data)))
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		if _, err := scanner.ScanInto(data, nil); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkCaselessNonASCIIFallbackScan 覆盖「Rose 让位后由确认程序求值」的
// 非 ASCII 忽略大小写规则在 64 KiB 英文型语料上的扫描开销。
func BenchmarkCaselessNonASCIIFallbackScan(b *testing.B) {
	scanner, err := scankit.Compile([]scankit.Expression{
		{Id: 1, Pattern: `(?i)école`},
		{Id: 2, Pattern: `[0-9]+`},
	})
	if err != nil {
		b.Fatal(err)
	}
	data := []byte(strings.Repeat("The quick brown fox jumps over the lazy dog. ", 1560))
	b.SetBytes(int64(len(data)))
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		if _, err := scanner.ScanInto(data, nil); err != nil {
			b.Fatal(err)
		}
	}
}
