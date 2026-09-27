// caseless_test：忽略大小写规则的候选索引回归
//
// 忽略大小写的规则曾经被完全挡在必须文字候选索引之外，退化为对输入每个
// 起点执行一次确认程序。这里锁住两条底线：
//  1. 结果与 Go 标准库 regexp 一致（对 ASCII 场景），大小写混写不能漏报；
//  2. 忽略大小写规则与精确规则的候选不互相污染。

package tests

import (
	"regexp"
	"sort"
	"testing"

	"github.com/smartwalle/scankit"
)

func TestCaselessMatchesStandardRegexp(t *testing.T) {
	patternLists := []struct {
		name  string
		exprs []scankit.Expression
		goPat string
	}{
		{
			name:  "inline caseless field",
			exprs: []scankit.Expression{{Id: 1, Pattern: `(?i)password\s*[:=]\s*\S+`}},
			goPat: `(?i)password\s*[:=]\s*\S+`,
		},
		{
			name:  "expression flag caseless field",
			exprs: []scankit.Expression{{Id: 1, Pattern: `password\s*[:=]\s*\S+`, Flags: scankit.CompileCaseless}},
			goPat: `(?i)password\s*[:=]\s*\S+`,
		},
		{
			name:  "short literal",
			exprs: []scankit.Expression{{Id: 1, Pattern: `(?i)ab`}},
			goPat: `(?i)ab`,
		},
		{
			name:  "single byte repeat",
			exprs: []scankit.Expression{{Id: 1, Pattern: `(?i)z+`}},
			goPat: `(?i)z+`,
		},
		{
			name:  "expanded class",
			exprs: []scankit.Expression{{Id: 1, Pattern: `(?i)[a-c]x`}},
			goPat: `(?i)[a-c]x`,
		},
		{
			name:  "partial caseless scope",
			exprs: []scankit.Expression{{Id: 1, Pattern: `(?i)abc(?-i:def)`}},
			goPat: `(?i)abc(?-i:def)`,
		},
		{
			name:  "alternation",
			exprs: []scankit.Expression{{Id: 1, Pattern: `(?i)(foo|BAR)baz`}},
			goPat: `(?i)(foo|BAR)baz`,
		},
	}

	inputs := []string{
		"",
		"password=abc",
		"PASSWORD=abc",
		"PaSsWoRd=abc",
		"password_hint=disabled",
		"PASSWORD_HINT=disabled",
		"pre PASSWORD=abc post",
		"ab",
		"AB",
		"aB",
		"Ab",
		"xx AB yy",
		"a b",
		"z",
		"Z",
		"zz",
		"ZZ",
		"zZ",
		"ax",
		"AX",
		"aX",
		"Ax",
		"cx",
		"BX",
		"ABCdef",
		"abcdef",
		"ABCDEF",
		"AbCdef",
		"FOObaz",
		"foobaz",
		"baRbaz",
		"BARbaz",
		"barbaz",
		"foo",
		"token",
	}

	for _, list := range patternLists {
		t.Run(list.name, func(t *testing.T) {
			scanner, err := scankit.Compile(list.exprs)
			if err != nil {
				t.Fatalf("Compile: %v", err)
			}
			pattern := regexp.MustCompile(list.goPat)
			for _, input := range inputs {
				matches, err := scanner.Scan([]byte(input))
				if err != nil {
					t.Fatalf("Scan(%q): %v", input, err)
				}
				got := len(matches) > 0
				want := pattern.MatchString(input)
				if got != want {
					t.Errorf("pattern %q 输入 %q：scankit 命中=%v，regexp 命中=%v", list.goPat, input, got, want)
				}
			}
		})
	}
}

// TestCaselessAndExactRulesShareCandidates 验证忽略大小写规则与精确规则在同一
// 批扫描中共用候选缓冲区时不会互相覆盖：候选编号必须按匹配语义分开。
func TestCaselessAndExactRulesShareCandidates(t *testing.T) {
	scanner, err := scankit.Compile([]scankit.Expression{
		{Id: 1, Pattern: `(?i)password=[a-z]+`},
		{Id: 2, Pattern: `PASSWORD=[a-z]+`},
	})
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	cases := []struct {
		input string
		want  []uint32
	}{
		{"password=abc", []uint32{1}},
		{"PASSWORD=abc", []uint32{1, 2}},
		{"PassWord=abc", []uint32{1}},
	}
	for _, tc := range cases {
		matches, err := scanner.Scan([]byte(tc.input))
		if err != nil {
			t.Fatalf("Scan(%q): %v", tc.input, err)
		}
		ids := make([]uint32, 0, len(matches))
		for _, match := range matches {
			ids = append(ids, match.Id)
		}
		sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
		if len(ids) != len(tc.want) {
			t.Fatalf("Scan(%q) 命中规则 = %v，期望 %v", tc.input, ids, tc.want)
		}
		for i := range ids {
			if ids[i] != tc.want[i] {
				t.Fatalf("Scan(%q) 命中规则 = %v，期望 %v", tc.input, ids, tc.want)
			}
		}
	}
}

// TestCaselessNonASCIIKeepsSemantics 验证非 ASCII 的忽略大小写规则在接入候选
// 索引后仍保持原有语义，不能被 ASCII 折叠推导所替代。
func TestCaselessNonASCIIKeepsSemantics(t *testing.T) {
	scanner, err := scankit.Compile([]scankit.Expression{{Id: 1, Pattern: `(?i)é`}})
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	cases := []struct {
		input string
		want  bool
	}{
		{"é", true},
		{"É", true},
		{"xÉx", true},
		{"e", false},
		{"E", false},
	}
	for _, tc := range cases {
		matches, err := scanner.Scan([]byte(tc.input))
		if err != nil {
			t.Fatalf("Scan(%q): %v", tc.input, err)
		}
		if got := len(matches) > 0; got != tc.want {
			t.Errorf("Scan(%q) 命中 = %v，期望 %v", tc.input, got, tc.want)
		}
	}
}
