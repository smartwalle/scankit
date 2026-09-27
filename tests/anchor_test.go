// anchor_test：锚定在输入首尾的规则的候选起点收敛
//
// `\A`/`\z`（以及未开启多行时的 `^`/`$`）把匹配位置钉死在一个偏移上：匹配起点
// 只能是 0，或者 len(data) 减去锚点之前的固定消费字节数。这类规则不需要从必须
// 文字的全部命中里展开候选，但也因此必须在候选收敛后仍与标准库给出同样的
// 命中集合。这里用 Go 标准库 regexp 逐字节对照，覆盖「会收敛」与「不能收敛」
// 两类模式，避免收敛逻辑漏报或误报。

package tests

import (
	"regexp"
	"sort"
	"testing"

	"github.com/smartwalle/scankit"
)

func TestAnchoredPatternsMatchStandardRegexp(t *testing.T) {
	cases := []struct {
		name  string
		pat   string
		flags scankit.CompileFlag
		goPat string
	}{
		{name: "begin_absolute", pat: `\Az`, goPat: `\Az`},
		{name: "begin_line_head", pat: `^z`, goPat: `^z`},
		{name: "end_absolute", pat: `z\z`, goPat: `z\z`},
		{name: "end_line_tail", pat: `z$`, goPat: `z$`},
		{name: "begin_with_tail", pat: `^abc`, goPat: `^abc`},
		{name: "end_with_head", pat: `abc$`, goPat: `abc$`},
		{name: "both_absolute", pat: `\Aabc\z`, goPat: `\Aabc\z`},
		{name: "single_char_both", pat: `^a$`, goPat: `^a$`},
		{name: "alternation_both_anchored", pat: `^(?:ab|cd)$`, goPat: `^(?:ab|cd)$`},
		{name: "separate_branch_anchors", pat: `^ab|cd$`, goPat: `^ab|cd$`},
		{name: "fixed_repeat", pat: `^a{2}$`, goPat: `^a{2}$`},
		{name: "end_pinned_multibyte", pat: `az\z`, goPat: `az\z`},
		{name: "end_pinned_multibyte_dollar", pat: `az$`, goPat: `az$`},
		{name: "class_window", pat: `^[a-z]$`, goPat: `^[a-z]$`},
		{name: "caseless_anchored", pat: `(?i)^pass$`, goPat: `(?i)^pass$`},
		{name: "no_overlap_inner_alternation", pat: `(?:^ab|cd)`, goPat: `(?:^ab|cd)`},
		{name: "optional_begin", pat: `(?:^)?abc`, goPat: `(?:^)?abc`},
		{name: "begin_after_consumed", pat: `abc^`, goPat: `abc^`},
		{name: "begin_inside_sequence", pat: `a(?:^)b`, goPat: `a(?:^)b`},
		{name: "multiline_begin", pat: `^z`, flags: scankit.CompileMultiline, goPat: `(?m)^z`},
		{name: "multiline_end", pat: `z$`, flags: scankit.CompileMultiline, goPat: `(?m)z$`},
	}

	inputs := []string{
		"",
		"z",
		"zz",
		"zzz",
		"az",
		"za",
		"abc",
		"xabc",
		"abcx",
		"ab",
		"cd",
		"abcd",
		"a",
		"aa",
		"aaa",
		"pass",
		"PASS",
		"PaSs",
		"a\nz",
		"z\nz",
		"\nz",
		"z\n",
		"ab\ncd",
		"abc\n",
		"azb",
		"azaz",
		"zazaz",
		"xx\nab",
		"ab\n",
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			scanner, err := scankit.Compile([]scankit.Expression{{Id: 1, Pattern: c.pat, Flags: c.flags}})
			if err != nil {
				t.Fatalf("Compile(%q): %v", c.pat, err)
			}
			pattern := regexp.MustCompile(c.goPat)
			for _, input := range inputs {
				matches, err := scanner.Scan([]byte(input))
				if err != nil {
					t.Fatalf("Scan(%q): %v", input, err)
				}
				got := matchOffsets(matches)
				want := indexOffsets(pattern.FindAllStringIndex(input, -1))
				if !sameOffsets(got, want) {
					t.Errorf("pattern %q 输入 %q：scankit=%v，regexp=%v", c.pat, input, got, want)
				}
			}
		})
	}
}

// TestAnchoredLookaroundCandidates 覆盖标准库无法对照的锚定形态：环视内部的
// 锚点只在正向前瞻里构成约束，负向前瞻恰好相反。期望值按语义手写。
func TestAnchoredLookaroundCandidates(t *testing.T) {
	cases := []struct {
		pat   string
		input string
		want  [][2]int
	}{
		{pat: `(?=\A)z`, input: "z", want: [][2]int{{0, 1}}},
		{pat: `(?=\A)z`, input: "zz", want: [][2]int{{0, 1}}},
		{pat: `(?=\A)z`, input: "az", want: nil},
		{pat: `(?!\A)z`, input: "z", want: nil},
		{pat: `(?!\A)z`, input: "zz", want: [][2]int{{1, 2}}},
		{pat: `(?!\A)z`, input: "az", want: [][2]int{{1, 2}}},
		{pat: `(?:^ab)+`, input: "ab", want: [][2]int{{0, 2}}},
		{pat: `(?:^ab)+`, input: "abab", want: [][2]int{{0, 2}}},
		{pat: `(?:^ab)+`, input: "xab", want: nil},
		{pat: `\A(?:ab|cd)\z`, input: "ab", want: [][2]int{{0, 2}}},
		{pat: `\A(?:ab|cd)\z`, input: "abcd", want: nil},
		{pat: `az\z`, input: "az", want: [][2]int{{0, 2}}},
		{pat: `az\z`, input: "zaz", want: [][2]int{{1, 3}}},
		{pat: `az\z`, input: "azb", want: nil},
	}
	for _, c := range cases {
		scanner, err := scankit.Compile([]scankit.Expression{{Id: 1, Pattern: c.pat}})
		if err != nil {
			t.Fatalf("Compile(%q): %v", c.pat, err)
		}
		matches, err := scanner.Scan([]byte(c.input))
		if err != nil {
			t.Fatalf("Scan(%q): %v", c.input, err)
		}
		got := matchOffsets(matches)
		if len(got) != len(c.want) {
			t.Fatalf("pattern %q 输入 %q：命中 = %v，期望 %v", c.pat, c.input, got, c.want)
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Fatalf("pattern %q 输入 %q：命中 = %v，期望 %v", c.pat, c.input, got, c.want)
			}
		}
	}
}

// TestAnchoredWithUnanchoredRules 验证同一次编译里锚定规则与普通规则共存时，
// 锚定规则的候选来源不会吞掉其它规则的候选。
func TestAnchoredWithUnanchoredRules(t *testing.T) {
	scanner, err := scankit.Compile([]scankit.Expression{
		{Id: 1, Pattern: `^KEY=`},
		{Id: 2, Pattern: `token=[a-z]+`},
		{Id: 3, Pattern: `z$`},
	})
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	cases := []struct {
		input string
		want  []uint32
	}{
		{"KEY=1", []uint32{1}},
		{" key=1", nil},
		{"xx token=abc yy", []uint32{2}},
		{"zz", []uint32{3}},
		{"z", []uint32{3}},
		{"KEY=1 token=abc z", []uint32{1, 2, 3}},
		{"", nil},
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

// TestAnchoredCandidateWindowScalesWithData 用长输入确认锚定规则的候选起点不随
// 数据里候选文字的出现次数增长：命中集合必须与短输入一致。
func TestAnchoredCandidateWindowScalesWithData(t *testing.T) {
	scanner, err := scankit.Compile([]scankit.Expression{
		{Id: 1, Pattern: `^z+`},
		{Id: 2, Pattern: `z+$`},
	})
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	long := make([]byte, 0, 1<<16)
	for len(long) < 1<<16 {
		long = append(long, 'z')
	}
	matches, err := scanner.Scan(long)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	ids := make([]uint32, 0, len(matches))
	positions := make([][2]uint64, 0, len(matches))
	for _, match := range matches {
		ids = append(ids, match.Id)
		positions = append(positions, [2]uint64{match.From, match.To})
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	if len(ids) != 2 {
		t.Fatalf("长输入命中数 = %d（%v %v），期望 2 条", len(ids), ids, positions)
	}
	if positions[0] != [2]uint64{0, uint64(len(long))} || positions[1] != [2]uint64{0, uint64(len(long))} {
		t.Fatalf("长输入命中区间 = %v，期望两条都是 [0,%d]", positions, len(long))
	}
}

// TestWordBoundaryNarrowMatchesStandardRegexp 覆盖入口词边界断言折算后的收窄路径。
//
// `\b`/`\B` 在起点字节属于 ASCII 单词集合时只取决于起点前一位字节，可以折进候选
// 匹配器的宽窗口掩码：把「前一位字节属于单词集合」的候选整段剔除，省掉逐候选
// 断言求值。判据一旦放宽到字符类入口（`\b\d{3}\b`）与分支入口
// （`\b(?:1|2)\d\d\b`），任何错判都会直接漏掉命中，因此这里把「会收窄」「不
// 会收窄」与相邻形态一起与标准库逐字节对照。
func TestWordBoundaryNarrowMatchesStandardRegexp(t *testing.T) {
	cases := []struct {
		name  string
		pat   string
		flags scankit.CompileFlag
		goPat string
	}{
		{name: "single_literal", pat: `\bz`, goPat: `\bz`},
		{name: "single_digit", pat: `\b1`, goPat: `\b1`},
		{name: "repeated_literal", pat: `\bz+`, goPat: `\bz+`},
		{name: "class_digits", pat: `\b\d{3}\b`, goPat: `\b\d{3}\b`},
		{name: "class_explicit", pat: `\b[0-9]{3}\b`, goPat: `\b[0-9]{3}\b`},
		{name: "class_repeat_bounded", pat: `\b[0-9]{2,4}\b`, goPat: `\b[0-9]{2,4}\b`},
		{name: "branch_digits", pat: `\b(?:1|2)\d\d\b`, goPat: `\b(?:1|2)\d\d\b`},
		{name: "non_boundary_class", pat: `\B\d{3}\B`, goPat: `\B\d{3}\B`},
		{name: "non_boundary_literal", pat: `\Bzz`, goPat: `\Bzz`},
		{name: "word_class_plus", pat: `\b\w+`, goPat: `\b\w+`},
		{name: "public_phone", pat: `\b(?:86)?1[3-9][0-9]{9}\b`, goPat: `\b(?:86)?1[3-9][0-9]{9}\b`},
		{name: "no_narrow_mixed_class", pat: `\b[0-9a-]{3}`, goPat: `\b[0-9a-]{3}`},
		{name: "no_narrow_wide_class", pat: `\b[a-z0-9-]{2}`, goPat: `\b[a-z0-9-]{2}`},
		{name: "nonword_head", pat: `\b-z`, goPat: `\b-z`},
		{name: "offset_literal", pat: `\b.z`, goPat: `\b.z`},
		{name: "unicode_class_fallback", pat: `\b\d{3}\b`, flags: scankit.CompileUCP, goPat: `\b\d{3}\b`},
	}

	inputs := []string{
		"",
		"z",
		"zz",
		"zzz",
		"1",
		"12",
		"123",
		"1234",
		"a1b",
		"x123y",
		"123-456",
		"1111111-1111111-",
		"zzz zzz",
		"_z",
		"-z",
		"z-",
		"--",
		"ab12cd",
		"a1a1a1",
		"12345678901",
		"+8612345678901",
		"8612345678901",
		"13800138000",
		"\n123\n",
		"x\n123",
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			scanner, err := scankit.Compile([]scankit.Expression{{Id: 1, Pattern: c.pat, Flags: c.flags}})
			if err != nil {
				t.Fatalf("Compile(%q): %v", c.pat, err)
			}
			pattern := regexp.MustCompile(c.goPat)
			for _, input := range inputs {
				matches, err := scanner.Scan([]byte(input))
				if err != nil {
					t.Fatalf("Scan(%q): %v", input, err)
				}
				got := matchOffsets(matches)
				want := indexOffsets(pattern.FindAllStringIndex(input, -1))
				if !sameOffsets(got, want) {
					t.Errorf("pattern %q 输入 %q：scankit=%v，regexp=%v", c.pat, input, got, want)
				}
			}
		})
	}
}

func matchOffsets(matches []scankit.Match) [][2]int {
	out := make([][2]int, 0, len(matches))
	for _, match := range matches {
		out = append(out, [2]int{int(match.From), int(match.To)})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i][0] != out[j][0] {
			return out[i][0] < out[j][0]
		}
		return out[i][1] < out[j][1]
	})
	return out
}

func indexOffsets(indexes [][]int) [][2]int {
	out := make([][2]int, 0, len(indexes))
	for _, index := range indexes {
		out = append(out, [2]int{index[0], index[1]})
	}
	return out
}

func sameOffsets(got, want [][2]int) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
