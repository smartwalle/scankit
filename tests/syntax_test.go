// syntax_test：规则语法与语义验证
//
// 每条用例只验证一种语法或语义；单条规则用 SyntaxCases 表驱动，组合规则与真实业务规则各用一张表。
// 由原根目录的 syntax_test.go、backref_test.go、conditional_test.go、controlverb_test.go、
// negated_class_test.go、unicode_test.go、assertion_test.go、combination_runtime_test.go、
// scan_conformance_test.go 合并而成。

package tests

import (
	"math/rand/v2"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/smartwalle/scankit"
)

// ---- 以下用例来自原根目录的 syntax_test.go ----

// TestSyntaxCoverage 每条用例只验证一种正则语法；输入数据经过挑选使期望匹配位置唯一，
// 便于在错误信息中直接看出是哪种语义的回退或支持缺失。
//
// 数据来源：docs/technical-solutions/scankit-block-mode/scankit-main-vs-dev-branch-strategy.md
// Section 3 of scankit-main-vs-dev-branch-strategy.md, all dev=YES rows.
type SyntaxCase struct {
	Name  string
	Pat   string
	Flags scankit.CompileFlag
	Data  string
	Want  []scankit.Match
}

// SyntaxCases 列出 dev 当前支持的每条正则语法，每个用例只验证一种语法。
// 由 syntax_test.go 与 syntax_bench_test.go 共同消费，避免重复定义。
var SyntaxCases = []SyntaxCase{
	// 字节字面量与转义
	{"literal_byte", `x`, 0, "ax", []scankit.Match{{Id: 1, From: 1, To: 2}}},
	{"escape_quoted", `\Qabc\E`, 0, "ababc", []scankit.Match{{Id: 1, From: 2, To: 5}}},
	{"escape_n", `\n`, 0, "a\n", []scankit.Match{{Id: 1, From: 1, To: 2}}},
	{"escape_r", `\r`, 0, "a\r", []scankit.Match{{Id: 1, From: 1, To: 2}}},
	{"escape_t", `\t`, 0, "a\t", []scankit.Match{{Id: 1, From: 1, To: 2}}},
	{"escape_f", `\f`, 0, "a\f", []scankit.Match{{Id: 1, From: 1, To: 2}}},
	{"escape_a", `\a`, 0, "a\a", []scankit.Match{{Id: 1, From: 1, To: 2}}},
	{"escape_e", `\e`, 0, "a\x1b" + "b", []scankit.Match{{Id: 1, From: 1, To: 2}}},
	{"escape_c", `\cA`, 0, "a\x01" + "b", []scankit.Match{{Id: 1, From: 1, To: 2}}},
	{"escape_hex", `\x41`, 0, "aAb", []scankit.Match{{Id: 1, From: 1, To: 2}}},
	{"escape_hex_braced", `\x{41}`, 0, "aAb", []scankit.Match{{Id: 1, From: 1, To: 2}}},
	{"escape_octal", `\101`, 0, "aAb", []scankit.Match{{Id: 1, From: 1, To: 2}}},
	{"escape_octal_braced", `\o{101}`, 0, "aAb", []scankit.Match{{Id: 1, From: 1, To: 2}}},
	{"escape_N_negated_newline", `\N+`, 0, "\nz", []scankit.Match{{Id: 1, From: 1, To: 2}}},
	{"escape_C_any_byte", `\Ca`, 0, "za", []scankit.Match{{Id: 1, From: 0, To: 2}}},

	// 通配符与锚点
	{"wildcard_dot", `.z`, 0, "az", []scankit.Match{{Id: 1, From: 0, To: 2}}},
	{"anchor_begin", `^z`, 0, "z", []scankit.Match{{Id: 1, From: 0, To: 1}}},
	{"anchor_end", `z$`, 0, "z", []scankit.Match{{Id: 1, From: 0, To: 1}}},
	{"anchor_AbsoluteStart", `\Az`, 0, "z", []scankit.Match{{Id: 1, From: 0, To: 1}}},
	{"anchor_AbsoluteEnd", `z\z`, 0, "z", []scankit.Match{{Id: 1, From: 0, To: 1}}},
	{"anchor_EndBeforeFinalNewline", `z\Z`, 0, "z", []scankit.Match{{Id: 1, From: 0, To: 1}}},
	{"boundary_word", `\bcat\b`, 0, "1 cat 5", []scankit.Match{{Id: 1, From: 2, To: 5}}},
	{"boundary_nonword", `\Bcat\B`, 0, "ocat5", []scankit.Match{{Id: 1, From: 1, To: 4}}},

	// 字符类
	{"class_simple", `[abc]z`, 0, "zbz", []scankit.Match{{Id: 1, From: 1, To: 3}}},
	{"class_negated", `[^x]z`, 0, "zz", []scankit.Match{{Id: 1, From: 0, To: 2}}},
	{"class_range", `[a-z]z`, 0, "1az", []scankit.Match{{Id: 1, From: 1, To: 3}}},
	{"class_posix_alpha", `[[:alpha:]]z`, 0, "1az", []scankit.Match{{Id: 1, From: 1, To: 3}}},
	{"class_posix_negated_alpha", `[[:^alpha:]]z`, 0, "1z", []scankit.Match{{Id: 1, From: 0, To: 2}}},
	{"class_posix_digit", `[[:digit:]]z`, 0, "5z", []scankit.Match{{Id: 1, From: 0, To: 2}}},
	{"class_posix_alnum", `[[:alnum:]]z`, 0, "5z", []scankit.Match{{Id: 1, From: 0, To: 2}}},
	{"class_posix_xdigit", `[[:xdigit:]]z`, 0, "fz", []scankit.Match{{Id: 1, From: 0, To: 2}}},
	{"class_posix_blank", `[[:blank:]]z`, 0, " z", []scankit.Match{{Id: 1, From: 0, To: 2}}},
	{"class_posix_cntrl", `[[:cntrl:]]z`, 0, "\x01z", []scankit.Match{{Id: 1, From: 0, To: 2}}},
	{"class_posix_graph", `[[:graph:]]z`, 0, "az", []scankit.Match{{Id: 1, From: 0, To: 2}}},
	{"class_posix_lower", `[[:lower:]]z`, 0, "az", []scankit.Match{{Id: 1, From: 0, To: 2}}},
	{"class_posix_print", `[[:print:]]z`, 0, "az", []scankit.Match{{Id: 1, From: 0, To: 2}}},
	{"class_posix_punct", `[[:punct:]]z`, 0, "!z", []scankit.Match{{Id: 1, From: 0, To: 2}}},
	{"class_posix_space", `[[:space:]]z`, 0, " z", []scankit.Match{{Id: 1, From: 0, To: 2}}},
	{"class_posix_upper", `[[:upper:]]z`, 0, "Az", []scankit.Match{{Id: 1, From: 0, To: 2}}},
	{"class_posix_word", `[[:word:]]z`, 0, "_z", []scankit.Match{{Id: 1, From: 0, To: 2}}},
	{"class_shorthand_digit", `\dz`, 0, "5z", []scankit.Match{{Id: 1, From: 0, To: 2}}},
	{"class_shorthand_non_digit", `\Dz`, 0, "az", []scankit.Match{{Id: 1, From: 0, To: 2}}},
	{"class_shorthand_word", `\wz`, 0, "az", []scankit.Match{{Id: 1, From: 0, To: 2}}},
	{"class_shorthand_non_word", `\Wz`, 0, "!z", []scankit.Match{{Id: 1, From: 0, To: 2}}},
	{"class_shorthand_space", `\sz`, 0, " z", []scankit.Match{{Id: 1, From: 0, To: 2}}},
	{"class_shorthand_non_space", `\Sz`, 0, "1z", []scankit.Match{{Id: 1, From: 0, To: 2}}},
	{"class_shorthand_hspace", `\hz`, 0, " z", []scankit.Match{{Id: 1, From: 0, To: 2}}},
	{"class_shorthand_non_hspace", `\Hz`, 0, "1z", []scankit.Match{{Id: 1, From: 0, To: 2}}},
	{"class_shorthand_vspace", `\vz`, 0, "\nz", []scankit.Match{{Id: 1, From: 0, To: 2}}},
	{"class_shorthand_non_vspace", `\Vz`, 0, "az", []scankit.Match{{Id: 1, From: 0, To: 2}}},
	{"class_linebreak_R", `\Rz`, 0, "\nz", []scankit.Match{{Id: 1, From: 0, To: 2}}},

	// Unicode 属性
	{"unicode_property_L", `\p{L}+z`, scankit.CompileUTF8 | scankit.CompileUCP, "12中文z", []scankit.Match{{Id: 1, From: 2, To: 9}}},
	{"unicode_negated_property", `\P{L}+z`, scankit.CompileUTF8 | scankit.CompileUCP, "12z", []scankit.Match{{Id: 1, From: 0, To: 3}}},

	// 数字序列匹配（以匹配数字为主要目的的规则）
	{"digit_run_shorthand", `\d+`, 0, "abc1234xy", []scankit.Match{{Id: 1, From: 3, To: 7}}},
	{"digit_run_range", `[0-9]+`, 0, "abc1234xy", []scankit.Match{{Id: 1, From: 3, To: 7}}},
	{"digit_run_posix", `[[:digit:]]+`, 0, "abc1234xy", []scankit.Match{{Id: 1, From: 3, To: 7}}},
	{"digit_run_unicode_property", `\p{Nd}+`, scankit.CompileUTF8 | scankit.CompileUCP, "abc１２３４xy", []scankit.Match{{Id: 1, From: 3, To: 15}}},
	{"integer_literal_plus", `1+`, 0, "abc111222xy", []scankit.Match{{Id: 1, From: 3, To: 6}}},
	{"float_like", `\d+\.\d+`, 0, "v=3.14 x", []scankit.Match{{Id: 1, From: 2, To: 6}}},

	// 量词
	{"quantifier_question", `ab?c`, 0, "abc", []scankit.Match{{Id: 1, From: 0, To: 3}}},
	{"quantifier_plus", `a+`, 0, "baaa", []scankit.Match{{Id: 1, From: 1, To: 4}}},
	{"quantifier_star_allowempty", `a*x`, scankit.CompileAllowEmpty, "aaax", []scankit.Match{{Id: 1, From: 0, To: 4}}},
	{"quantifier_exact_count", `a{3}`, 0, "baaa", []scankit.Match{{Id: 1, From: 1, To: 4}}},
	{"quantifier_min_count", `a{2,}`, 0, "baa", []scankit.Match{{Id: 1, From: 1, To: 3}}},
	{"quantifier_range_count", `a{2,3}x`, 0, "baaax", []scankit.Match{{Id: 1, From: 1, To: 5}}},
	{"quantifier_lazy_question", `ab??c`, 0, "abc", []scankit.Match{{Id: 1, From: 0, To: 3}}},
	{"quantifier_lazy_plus", `a+?z`, 0, "aaz", []scankit.Match{{Id: 1, From: 0, To: 3}}},
	{"quantifier_lazy_star_allowempty", `a*?z`, scankit.CompileAllowEmpty, "aaz", []scankit.Match{{Id: 1, From: 0, To: 3}}},
	{"quantifier_lazy_range", `a{2,3}?x`, 0, "baaax", []scankit.Match{{Id: 1, From: 1, To: 5}}},
	{"quantifier_possessive_plus", `a++z`, 0, "aaaz", []scankit.Match{{Id: 1, From: 0, To: 4}}},

	// 分组
	{"group_capture", `(ab)+x`, 0, "ababx", []scankit.Match{{Id: 1, From: 0, To: 5}}},
	{"group_non_capture", `(?:ab)+x`, 0, "ababx", []scankit.Match{{Id: 1, From: 0, To: 5}}},
	{"group_named", `(?<n>ab)+x`, 0, "ababx", []scankit.Match{{Id: 1, From: 0, To: 5}}},
	{"group_python_named", `(?P<n>ab)+x`, 0, "ababx", []scankit.Match{{Id: 1, From: 0, To: 5}}},
	{"group_atomic", `(?>ab)+x`, 0, "ababx", []scankit.Match{{Id: 1, From: 0, To: 5}}},

	// 注释与内联 flag
	{"comment", `(?#note)abc`, 0, "abc", []scankit.Match{{Id: 1, From: 0, To: 3}}},
	{"inline_flag_caseless", `(?i:abc)`, 0, "ABC", []scankit.Match{{Id: 1, From: 0, To: 3}}},
	{"inline_flag_multiline", `(?m:^z)`, 0, "z\nz", []scankit.Match{{Id: 1, From: 0, To: 1}, {Id: 1, From: 2, To: 3}}},
	{"inline_flag_dotall", `(?s:a.b)`, 0, "a\nb", []scankit.Match{{Id: 1, From: 0, To: 3}}},
	{"inline_flag_extended", `(?x:a b)`, 0, "ab", []scankit.Match{{Id: 1, From: 0, To: 2}}},
	{"inline_flag_toggle", `(?i:X)`, 0, "x", []scankit.Match{{Id: 1, From: 0, To: 1}}},

	// 环视
	{"lookahead", `a(?=b)`, 0, "ab", []scankit.Match{{Id: 1, From: 0, To: 1}}},
	{"negative_lookahead", `a(?!b)`, 0, "ax", []scankit.Match{{Id: 1, From: 0, To: 1}}},
	{"lookbehind", `(?<=a)b`, 0, "ab", []scankit.Match{{Id: 1, From: 1, To: 2}}},
	{"negative_lookbehind", `(?<!a)b`, 0, "xb", []scankit.Match{{Id: 1, From: 1, To: 2}}},

	// 反向引用
	{"backref_numeric", `(a)\1`, 0, "aa", []scankit.Match{{Id: 1, From: 0, To: 2}}},
	{"backref_named_k", `(?<n>a)\k<n>`, 0, "aa", []scankit.Match{{Id: 1, From: 0, To: 2}}},
	{"backref_named_g_angle", `(?<n>a)\g<n>`, 0, "aa", []scankit.Match{{Id: 1, From: 0, To: 2}}},
	{"backref_named_g_brace", `(?<n>a)\g{n}`, 0, "aa", []scankit.Match{{Id: 1, From: 0, To: 2}}},

	// 条件引用
	{"conditional_numeric", `(a)(?(1)b|c)`, 0, "ab", []scankit.Match{{Id: 1, From: 0, To: 2}}},
	{"conditional_named", `(?<n>a)(?(<n>)b|c)`, 0, "ab", []scankit.Match{{Id: 1, From: 0, To: 2}}},

	// 控制动词
	{"control_verb_ACCEPT", `a(*ACCEPT)`, 0, "ab", []scankit.Match{{Id: 1, From: 0, To: 1}}},
	{"control_verb_FAIL", `a(*FAIL)`, 0, "ab", nil},

	// Sequence 多字面量隐式连接
	{"sequence_literal_concat", `abc`, 0, "_abc_", []scankit.Match{{Id: 1, From: 1, To: 4}}},

	// 3+ 分支交替
	{"alternation_three_branches", `a|b|c`, 0, "xb", []scankit.Match{{Id: 1, From: 1, To: 2}}},

	// 反向引用 + 量词
	{"backref_with_quantifier", `(a)\1+`, 0, "aaaa", []scankit.Match{{Id: 1, From: 0, To: 4}}},

	// 边界符位置组合
	{"boundary_word_start_only", `\bcat`, 0, "cat", []scankit.Match{{Id: 1, From: 0, To: 3}}},
	{"boundary_word_end_only", `cat\b`, 0, "cat", []scankit.Match{{Id: 1, From: 0, To: 3}}},
	{"boundary_nonword_start", `\Bcat`, 0, "ocat", []scankit.Match{{Id: 1, From: 1, To: 4}}},
	{"boundary_nonword_end", `cat\B`, 0, "cat_", []scankit.Match{{Id: 1, From: 0, To: 3}}},

	// 交替
	{"alternation", `a|z`, 0, "zy", []scankit.Match{{Id: 1, From: 0, To: 1}}},
}

func TestSyntaxCoverage(t *testing.T) {
	for _, c := range SyntaxCases {
		s, err := scankit.Compile([]scankit.Expression{{Id: 1, Pattern: c.Pat, Flags: c.Flags}})
		if err != nil {
			t.Errorf("[FAIL] %s %q: compile: %v", c.Name, c.Pat, err)
			continue
		}
		got, err := s.Scan([]byte(c.Data))
		if err != nil {
			t.Errorf("[FAIL] %s %q: scan: %v", c.Name, c.Pat, err)
			continue
		}
		if !matchEqual(got, c.Want) {
			t.Errorf("[FAIL] %s: got %v want %v", c.Name, got, c.Want)
		}
	}
}

func matchEqual(got, want []scankit.Match) bool {
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

// TestSyntaxCombination 覆盖 §3 表中 Combination 操作数行（`!`/`&`/`|`、括号组合）。
// 组合规则需要多个 Expression 联合编译，与单模式 SyntaxCase 形状不同，因此单独建表。
//
// 编译规则：
//   - CompileCombination 标记 Pattern 字段是布尔组合表达式（操作数为其他表达式 Id）；
//   - `1&2` 表示"1 与 2 同时存在"；
//   - `1|2` 表示"1 或 2"；
//   - `!1` 表示"1 不存在"；
//   - 括号用于子表达式优先级。
func TestSyntaxCombination(t *testing.T) {
	type comboCase struct {
		name string
		expr []scankit.Expression
		data string
		want []scankit.Match
	}
	cases := []comboCase{
		// 1|2：组合规则的 Id 在每个操作数位置都会产生匹配。
		{
			name: "combinator_or",
			expr: []scankit.Expression{
				{Id: 1, Pattern: "foo"},
				{Id: 2, Pattern: "bar"},
				{Id: 3, Pattern: "1|2", Flags: scankit.CompileCombination},
			},
			data: "xfoo bar x",
			want: []scankit.Match{
				{Id: 1, From: 1, To: 4}, {Id: 3, From: 1, To: 4},
				{Id: 2, From: 5, To: 8}, {Id: 3, From: 5, To: 8},
			},
		},
		// 1&2：组合规则仅在两个操作数都命中的位置触发。
		{
			name: "combinator_and",
			expr: []scankit.Expression{
				{Id: 1, Pattern: "foo"},
				{Id: 2, Pattern: "bar"},
				{Id: 3, Pattern: "1&2", Flags: scankit.CompileCombination},
			},
			data: "foobar",
			want: []scankit.Match{
				{Id: 1, From: 0, To: 3},
				{Id: 2, From: 3, To: 6},
				{Id: 3, From: 3, To: 6},
			},
		},
		// !1：操作数完全无命中时，组合规则在数据末尾报告一次。
		{
			name: "combinator_not_no_operand",
			expr: []scankit.Expression{
				{Id: 1, Pattern: "foo", Flags: scankit.CompileQuiet},
				{Id: 2, Pattern: "!1", Flags: scankit.CompileCombination},
			},
			data: "xbarx",
			want: []scankit.Match{{Id: 2, From: 5, To: 5}},
		},
		// (1|2)&!1：先按括号优先级取 1 或 2，再与"非 1"求与。等价于"2"在无 1 命中的子串。
		{
			name: "combinator_parens_and_not",
			expr: []scankit.Expression{
				{Id: 1, Pattern: "foo"},
				{Id: 2, Pattern: "bar"},
				{Id: 3, Pattern: "(1|2)&!1", Flags: scankit.CompileCombination},
			},
			data: "xbar foo y",
			want: []scankit.Match{
				{Id: 2, From: 1, To: 4},
				{Id: 3, From: 1, To: 4},
				{Id: 1, From: 5, To: 8},
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s, err := scankit.Compile(c.expr)
			if err != nil {
				t.Fatalf("compile: %v", err)
			}
			got, err := s.Scan([]byte(c.data))
			if err != nil {
				t.Fatalf("scan: %v", err)
			}
			if !matchEqual(got, c.want) {
				t.Fatalf("%s: got %v want %v", c.name, got, c.want)
			}
		})
	}
}

// TestSyntaxRealWorld 覆盖真实业务里常见的复合正则规则，每条用例的 pattern 由多个
// 语法元素组合而成，目的不是单点验证某个 escape，而是确认 dev 引擎在真实输入下的
// 整体匹配能力（与 TestSyntaxCoverage 的「单语法」视角互补）。
func TestSyntaxRealWorld(t *testing.T) {
	type tc struct {
		name string
		pat  string
		data string
		want []scankit.Match
	}
	cases := []tc{
		// === 联系方式 ===
		{"email", `[\w.+-]+@[\w.-]+\.[a-zA-Z]{2,}`, "联系 support@example.com 或 info@test.co.cn",
			[]scankit.Match{{Id: 1, From: 7, To: 26}, {Id: 1, From: 31, To: 46}}},
		{"url_http", `https?://[\w./-]+`, "访问 https://example.com/path 或 http://a.b/c",
			[]scankit.Match{{Id: 1, From: 7, To: 31}, {Id: 1, From: 36, To: 48}}},
		{"phone_cn_mobile", `1[3-9]\d{9}`, "电话 13800138000",
			[]scankit.Match{{Id: 1, From: 7, To: 18}}},
		{"phone_with_dash", `1[3-9]\d{1}-?\d{4}-?\d{4}`, "13912345678 或 139-1234-5678",
			[]scankit.Match{{Id: 1, From: 0, To: 11}, {Id: 1, From: 16, To: 29}}},

		// === 网络标识 ===
		{"ipv4", `\d+\.\d+\.\d+\.\d+`, "服务器 192.168.1.1",
			[]scankit.Match{{Id: 1, From: 10, To: 21}}},
		{"ipv4_with_port", `\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}:\d{1,5}`, "连接 192.168.1.1:8080",
			[]scankit.Match{{Id: 1, From: 7, To: 23}}},
		{"ipv4_cidr", `\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}/\d{1,2}`, "网段 10.0.0.0/24",
			[]scankit.Match{{Id: 1, From: 7, To: 18}}},
		{"mac_address", `[0-9a-fA-F]{2}(:[0-9a-fA-F]{2}){5}`, "MAC AA:BB:CC:DD:EE:FF",
			[]scankit.Match{{Id: 1, From: 4, To: 21}}},
		{"uuid", `[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`,
			"id=550e8400-e29b-41d4-a716-446655440000",
			[]scankit.Match{{Id: 1, From: 3, To: 39}}},
		{"http_status", `\b\d{3}\b`, "状态 200 404 500",
			[]scankit.Match{{Id: 1, From: 7, To: 10}, {Id: 1, From: 11, To: 14}, {Id: 1, From: 15, To: 18}}},

		// === 时间日期 ===
		{"date_iso", `\d{4}-\d{2}-\d{2}`, "日期 2026-09-24",
			[]scankit.Match{{Id: 1, From: 7, To: 17}}},
		{"time_hms", `\d{2}:\d{2}:\d{2}`, "时间 12:34:56",
			[]scankit.Match{{Id: 1, From: 7, To: 15}}},
		{"date_slash", `\d{1,2}/\d{1,2}/\d{4}`, "日期 9/24/2026",
			[]scankit.Match{{Id: 1, From: 7, To: 16}}},

		// === 数值 ===
		{"float_number", `-?\d+\.\d+`, "温度 -3.14",
			[]scankit.Match{{Id: 1, From: 7, To: 12}}},
		{"percentage", `\d+\.\d+%`, "增长 12.5%",
			[]scankit.Match{{Id: 1, From: 7, To: 12}}},
		{"number_with_comma", `\d{1,3}(,\d{3})+`, "金额 1,234,567",
			[]scankit.Match{{Id: 1, From: 7, To: 16}}},

		// === 路径与文件 ===
		{"unix_path", `/[\w./-]+`, "路径 /usr/local/bin/scankit",
			[]scankit.Match{{Id: 1, From: 7, To: 29}}},
		{"file_ext", `\w+\.\w{2,4}`, "文件 README.md",
			[]scankit.Match{{Id: 1, From: 7, To: 16}}},

		// === HTML / 标记 ===
		{"html_tag", `<[a-z]+[^>]*>`, `<div class="x">`,
			[]scankit.Match{{Id: 1, From: 0, To: 15}}},
		{"hex_value_word_boundary", `\b0x[0-9a-fA-F]+\b`, "地址 0xDEADBEEF",
			[]scankit.Match{{Id: 1, From: 7, To: 17}}},
		{"quoted_string", `"[^"]*"`, `他说 "hello world"`,
			[]scankit.Match{{Id: 1, From: 7, To: 20}}},
		{"json_string_escaped", `"[^"\\]*(?:\\.[^"\\]*)*"`, `"hello \"world\""`,
			[]scankit.Match{{Id: 1, From: 0, To: 17}}},

		// === 中文场景 ===
		{"chinese_chars", `[\p{Han}]+`, "中文 abc 字符",
			[]scankit.Match{{Id: 1, From: 7, To: 8}}},
		{"chinese_id_18", `\d{17}[\dXx]`, "身份证 11010120000101123X",
			[]scankit.Match{{Id: 1, From: 10, To: 28}}},

		// === 编程语言 ===
		{"c_identifier", `[a-zA-Z_]\w*`, "变量 my_var123",
			[]scankit.Match{{Id: 1, From: 7, To: 16}}},
		{"semver", `\d+\.\d+\.\d+`, "版本 1.2.3",
			[]scankit.Match{{Id: 1, From: 7, To: 12}}},
		{"git_sha", `[0-9a-f]{7,40}`, "commit abc1234def5678",
			[]scankit.Match{{Id: 1, From: 7, To: 21}}},
		{"hex_color", `#[0-9a-fA-F]{6}\b`, "颜色 #FFAABB 和 #00FF00",
			[]scankit.Match{{Id: 1, From: 7, To: 14}, {Id: 1, From: 19, To: 26}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s, err := scankit.Compile([]scankit.Expression{{Id: 1, Pattern: c.pat}})
			if err != nil {
				t.Fatalf("compile: %v", err)
			}
			got, err := s.Scan([]byte(c.data))
			if err != nil {
				t.Fatalf("scan: %v", err)
			}
			if !matchEqual(got, c.want) {
				t.Fatalf("%s: got %v want %v", c.name, got, c.want)
			}
		})
	}
}

// BenchmarkSyntaxCoverage 逐条复用 SyntaxCases，与 Go 标准库 regexp 做吞吐对比。
//
// 跑法：`go test -bench=BenchmarkSyntaxCoverage -benchtime=1s -count=1 .`
//
// 输出形如：
//
//	BenchmarkSyntaxCoverage/literal_byte/scankit-15   1000000   2.5 ns/op
//	BenchmarkSyntaxCoverage/literal_byte/regexp-15     500000   5.0 ns/op
//
// 注意：
//   - scankit 输出 []Match，regexp 输出 [][]byte，结构不同，本基准只看耗时与吞吐；
//   - 部分语法（lookbehind、backreference、conditional、control verb、possessive 等）
//     regexp 不支持或语义不同，对应的 regexp 子基准会跳过；
//   - 部分转义（\e、\c、\N、\C、\h、\H、\V、\R）在 Go regexp 中虽能编译但语义与 scankit 不一致，
//     本基准同样跳过，避免拿不同语义做吞吐对比；
//   - scankit 编译期已经成功，编译时间不计入基准内部；regexp 同理。
func BenchmarkSyntaxCoverage(b *testing.B) {
	for _, c := range SyntaxCases {
		s, err := scankit.Compile([]scankit.Expression{{Id: 1, Pattern: c.Pat, Flags: c.Flags}})
		if err != nil {
			b.Run(c.Name+"/scankit", func(b *testing.B) {
				b.Fatalf("scankit compile failed: %v", err)
			})
			continue
		}
		data := []byte(c.Data)
		// 扩出一段重复输入，让 MB/s 数量级稳定。
		buf := make([]byte, 0, len(data))
		for i := 0; i < 1; i++ {
			buf = append(buf, data...)
		}
		b.Run(c.Name+"/scankit", func(b *testing.B) {
			b.SetBytes(int64(len(buf)))
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := s.Scan(buf); err != nil {
					b.Fatal(err)
				}
			}
		})
		if re, ok := tryCompileRegexp(c); ok {
			b.Run(c.Name+"/regexp", func(b *testing.B) {
				b.SetBytes(int64(len(buf)))
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					_ = re.FindAll(buf, -1)
				}
			})
		}
	}
}

// tryCompileRegexp 把 scankit 模式直接交给 regexp.Compile；只有 regexp 不支持
// 或语义与 scankit 不一致的子集返回 false。语义差异由 containsAny 显式列举，
// 避免误把语义不同的模式拿来做吞吐对比。
func tryCompileRegexp(c SyntaxCase) (*regexp.Regexp, bool) {
	// UTF-8 / UCP 模式 regexp 不等价，跳过。
	if c.Flags&(scankit.CompileUTF8|scankit.CompileUCP) != 0 {
		return nil, false
	}
	// regexp 不支持或语义与 scankit 不一致的关键字集合。
	incompatible := []string{
		// 不被 regexp 支持的语法
		"(?=", "(?!", "(?<=", "(?<!", // 环视（lookbehind 不支持）
		"\\k<", "\\g<", "\\g{", // 命名反向引用
		"\\1", "\\2", "\\3", "\\4", "\\5", "\\6", "\\7", "\\8", "\\9", // 数字反向引用
		"(?(", // 条件引用
		"(*",  // 控制动词
		"(?>", // 原子组
		"++",  // 占有量词
		// scankit 与 regexp 语义不同的转义
		"\\e",                      // scankit=ESC(0x1b)，regexp=字面 e
		"\\c",                      // scankit 控制字符，regexp 字面 \cX
		"\\N",                      // scankit 非换行，regexp 字面 N
		"\\C",                      // scankit 任意单字节，regexp 字面 C
		"\\h",                      // scankit 水平空白，regexp 字面 h
		"\\H", "\\v", "\\V", "\\R", // 同上
		"\\Z",  // scankit 在末尾换行前结束，regexp 同 \z
		"\\Q",  // scankit 引用字面量，regexp 字面 Q
		"\\o{", // scankit 大括号八进制，regexp 不支持
		"\\x{", // scankit 大括号十六进制，regexp 不支持
		// atomic + 复杂控制
		"(?#", // scankit 注释
	}
	for _, s := range incompatible {
		if contains(c.Pat, s) {
			return nil, false
		}
	}
	re, err := regexp.Compile(c.Pat)
	if err != nil {
		return nil, false
	}
	return re, true
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// ---- 以下用例来自原根目录的 backref_test.go ----

func TestBackreference(t *testing.T) {
	s, err := scankit.Compile([]scankit.Expression{{Id: 1, Pattern: `(ab)\1`}})
	if err != nil {
		t.Fatal(err)
	}
	m, err := s.Scan([]byte("xxabab"))
	if err != nil || len(m) != 1 || m[0].From != 2 || m[0].To != 6 {
		t.Fatalf("%v %#v", err, m)
	}
}

func TestNamedBackreference(t *testing.T) {
	s, err := scankit.Compile([]scankit.Expression{{Id: 1, Pattern: `(?<part>ab)\k<part>`}})
	if err != nil {
		t.Fatal(err)
	}
	matches, err := s.Scan([]byte("abab abac"))
	if err != nil || len(matches) != 1 || matches[0] != (scankit.Match{Id: 1, From: 0, To: 4}) {
		t.Fatalf("named backreference mismatch: %v %#v", err, matches)
	}
}

func TestNonGreedyRepeat(t *testing.T) {
	s, err := scankit.Compile([]scankit.Expression{{Id: 1, Pattern: `a+?`}})
	if err != nil {
		t.Fatal(err)
	}
	m, err := s.Scan([]byte("aaa"))
	if err != nil || len(m) != 3 || m[0].To-m[0].From != 1 {
		t.Fatalf("%v %#v", err, m)
	}
}

func TestNestedCaptureBackreference(t *testing.T) {
	s, err := scankit.Compile([]scankit.Expression{{Id: 1, Pattern: `((a)b)\1`}})
	if err != nil {
		t.Fatal(err)
	}
	matches, err := s.Scan([]byte("abab abba"))
	if err != nil || len(matches) != 1 || matches[0].From != 0 || matches[0].To != 4 {
		t.Fatalf("nested capture mismatch: %v %#v", err, matches)
	}
}

func TestCapturedAlternationBackreference(t *testing.T) {
	s, err := scankit.Compile([]scankit.Expression{{Id: 1, Pattern: `((a|b))\1`}})
	if err != nil {
		t.Fatal(err)
	}
	matches, err := s.Scan([]byte("aa bb ab"))
	if err != nil || len(matches) != 2 {
		t.Fatalf("captured alternation mismatch: %v %#v", err, matches)
	}
}

func TestBackreferenceUsesCaselessComparison(t *testing.T) {
	s, err := scankit.Compile([]scankit.Expression{{Id: 1, Pattern: `(ab)\1`, Flags: scankit.CompileCaseless}})
	if err != nil {
		t.Fatal(err)
	}
	matches, err := s.Scan([]byte("aBAB abAc"))
	if err != nil || len(matches) != 1 || matches[0] != (scankit.Match{Id: 1, From: 0, To: 4}) {
		t.Fatalf("caseless backreference mismatch: %v %#v", err, matches)
	}
}

func TestRepeatedCaptureClearsNonParticipatingInnerGroup(t *testing.T) {
	s, err := scankit.Compile([]scankit.Expression{{Id: 1, Pattern: `(?:(a)|(b))+\2`}})
	if err != nil {
		t.Fatal(err)
	}
	matches, err := s.Scan([]byte("bab bbb"))
	if err != nil || len(matches) != 1 || matches[0] != (scankit.Match{Id: 1, From: 4, To: 7}) {
		t.Fatalf("repeated capture mismatch: %v %#v", err, matches)
	}
}

func TestPositiveLookaroundPreservesCapture(t *testing.T) {
	s, err := scankit.Compile([]scankit.Expression{{Id: 1, Pattern: `(?=(a))\1`}})
	if err != nil {
		t.Fatal(err)
	}
	matches, err := s.Scan([]byte("a b"))
	if err != nil || len(matches) != 1 || matches[0] != (scankit.Match{Id: 1, From: 0, To: 1}) {
		t.Fatalf("lookaround capture mismatch: %v %#v", err, matches)
	}
}

func TestAtomicGroupPreventsAlternativeBacktracking(t *testing.T) {
	s, err := scankit.Compile([]scankit.Expression{{Id: 1, Pattern: `(?>ab|a)b`}})
	if err != nil {
		t.Fatal(err)
	}
	matches, err := s.Scan([]byte("ab"))
	if err != nil || len(matches) != 0 {
		t.Fatalf("atomic group backtracked: %v %#v", err, matches)
	}
}

func TestNullableUnboundedRepeatTerminates(t *testing.T) {
	s, err := scankit.Compile([]scankit.Expression{{Id: 1, Pattern: `(?:a?)*b`}})
	if err != nil {
		t.Fatal(err)
	}
	matches, err := s.Scan([]byte("aaab"))
	if err != nil || len(matches) != 1 || matches[0] != (scankit.Match{Id: 1, From: 0, To: 4}) {
		t.Fatalf("nullable repeat mismatch: %v %#v", err, matches)
	}
}

func TestPossessiveRepeatPreventsBacktracking(t *testing.T) {
	s, err := scankit.Compile([]scankit.Expression{{Id: 1, Pattern: `a*+a`}, {Id: 2, Pattern: `a*+b`}})
	if err != nil {
		t.Fatal(err)
	}
	matches, err := s.Scan([]byte("aaa aab"))
	if err != nil || len(matches) != 1 || matches[0] != (scankit.Match{Id: 2, From: 4, To: 7}) {
		t.Fatalf("possessive repeat backtracked: %v %#v", err, matches)
	}
}

// ---- 以下用例来自原根目录的 conditional_test.go ----

func TestConditionalReference(t *testing.T) {
	s, err := scankit.Compile([]scankit.Expression{{Id: 1, Pattern: `(a)(?(1)b|c)`}})
	if err != nil {
		t.Fatal(err)
	}
	m, err := s.Scan([]byte("ab ac"))
	if err != nil || len(m) != 1 {
		t.Fatalf("%v %#v", err, m)
	}
}

func TestNamedConditionalReference(t *testing.T) {
	s, err := scankit.Compile([]scankit.Expression{{Id: 1, Pattern: `(?<part>a)(?(<part>)b|c)`}})
	if err != nil {
		t.Fatal(err)
	}
	matches, err := s.Scan([]byte("ab ac"))
	if err != nil || len(matches) != 1 || matches[0] != (scankit.Match{Id: 1, From: 0, To: 2}) {
		t.Fatalf("named conditional mismatch: %v %#v", err, matches)
	}
}

// ---- 以下用例来自原根目录的 controlverb_test.go ----

func TestControlVerbFail(t *testing.T) {
	s, err := scankit.Compile([]scankit.Expression{{Id: 1, Pattern: `a(*FAIL)`}})
	if err != nil {
		t.Fatal(err)
	}
	m, err := s.Scan([]byte("a"))
	if err != nil || len(m) != 0 {
		t.Fatalf("%v %#v", err, m)
	}
}

func TestControlVerbAcceptStopsSequence(t *testing.T) {
	s, err := scankit.Compile([]scankit.Expression{{Id: 2, Pattern: `a(*ACCEPT)b`}})
	if err != nil {
		t.Fatal(err)
	}
	matches, err := s.Scan([]byte("ab"))
	if err != nil || len(matches) != 1 || matches[0].To != 1 {
		t.Fatalf("ACCEPT 语义=%v,%v", matches, err)
	}
}

// ---- 以下用例来自原根目录的 negated_class_test.go ----

func TestNegatedClassScan(t *testing.T) {
	s, err := scankit.Compile([]scankit.Expression{{Id: 1, Pattern: `[^a]`}})
	if err != nil {
		t.Fatal(err)
	}
	m, err := s.Scan([]byte("ab"))
	if err != nil || len(m) != 1 || m[0].From != 1 {
		t.Fatalf("%v %#v", err, m)
	}
}

func TestUppercaseAndWhitespaceCharacterClasses(t *testing.T) {
	cases := []struct {
		pattern string
		input   string
		count   int
	}{
		{`\D+`, "1ab2", 1},
		{`\W+`, "a_!9", 1},
		{`\S+`, " a\t", 1},
		{`[\D]+`, "1ab2", 1},
		{`\h+`, "\t  x", 1},
		{`\v+`, "x\n\ry", 1},
	}
	for _, tc := range cases {
		s, err := scankit.Compile([]scankit.Expression{{Id: 1, Pattern: tc.pattern}})
		if err != nil {
			t.Fatalf("compile %q: %v", tc.pattern, err)
		}
		matches, err := s.Scan([]byte(tc.input))
		if err != nil || len(matches) != tc.count {
			t.Fatalf("scan %q against %q: %v %#v", tc.pattern, tc.input, err, matches)
		}
	}
}

// ---- 以下用例来自原根目录的 unicode_test.go ----

func TestUnicodeProperty(t *testing.T) {
	s, err := scankit.Compile([]scankit.Expression{{Id: 1, Pattern: `\p{L}+`, Flags: scankit.CompileUTF8 | scankit.CompileUCP}})
	if err != nil {
		t.Fatal(err)
	}
	m, err := s.Scan([]byte("123 中文 abc"))
	if err != nil || len(m) != 2 {
		t.Fatalf("%v %#v", err, m)
	}
}

func TestUnicodePropertyAliases(t *testing.T) {
	s, err := scankit.Compile([]scankit.Expression{{Id: 1, Pattern: `\p{ASCII}+`, Flags: scankit.CompileUTF8}})
	if err != nil {
		t.Fatal(err)
	}
	m, err := s.Scan([]byte("abc"))
	if err != nil || len(m) == 0 {
		t.Fatalf("%v %#v", err, m)
	}
}

func TestUnicodeScriptProperty(t *testing.T) {
	s, err := scankit.Compile([]scankit.Expression{{Id: 1, Pattern: `\p{Greek}`, Flags: scankit.CompileUTF8}})
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
		s, err := scankit.Compile([]scankit.Expression{{Id: 1, Pattern: pattern, Flags: scankit.CompileUTF8}})
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
		s, err := scankit.Compile([]scankit.Expression{{Id: 1, Pattern: tc.pattern, Flags: scankit.CompileUTF8 | scankit.CompileUCP}})
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
	s, err := scankit.Compile([]scankit.Expression{{Id: 1, Pattern: `\x{4e2d}\x{6587}`, Flags: scankit.CompileUTF8}})
	if err != nil {
		t.Fatal(err)
	}
	matches, err := s.Scan([]byte("中文"))
	if err != nil || len(matches) != 1 || matches[0] != (scankit.Match{Id: 1, From: 0, To: 6}) {
		t.Fatalf("braced hex UTF8 mismatch: %v %#v", err, matches)
	}
}

func TestUTF8NegatedClassConsumesWholeRune(t *testing.T) {
	s, err := scankit.Compile([]scankit.Expression{{Id: 1, Pattern: `\N+`, Flags: scankit.CompileUTF8}})
	if err != nil {
		t.Fatal(err)
	}
	matches, err := s.Scan([]byte("中文\n"))
	if err != nil || len(matches) != 1 || matches[0] != (scankit.Match{Id: 1, From: 0, To: 6}) {
		t.Fatalf("UTF8 non-newline mismatch: %v %#v", err, matches)
	}
}

func TestUCPWordBoundaryTreatsCombiningMarkAsWord(t *testing.T) {
	s, err := scankit.Compile([]scankit.Expression{{Id: 1, Pattern: "a\\b\u0301", Flags: scankit.CompileUTF8 | scankit.CompileUCP}})
	if err != nil {
		t.Fatal(err)
	}
	matches, err := s.Scan([]byte("a\u0301"))
	if err != nil || len(matches) != 0 {
		t.Fatalf("UCP word boundary split combining mark: %v %#v", err, matches)
	}
}

// ---- 以下用例来自原根目录的 assertion_test.go ----

func TestLookaroundAndBoundaries(t *testing.T) {
	cases := []struct {
		p, s string
		n    int
	}{{`foo(?=bar)`, "foobar foox", 1}, {`foo(?!bar)`, "foobar foox", 1}, {`(?<=foo)bar`, "foobar xxbar", 1}, {`(?<!foo)bar`, "foobar xxbar", 1}, {`^a$`, "a", 1}}
	for _, c := range cases {
		s, e := scankit.Compile([]scankit.Expression{{Id: 1, Pattern: c.p}})
		if e != nil {
			t.Fatal(c.p, e)
		}
		m, e := s.Scan([]byte(c.s))
		if e != nil || len(m) != c.n {
			t.Fatalf("%s: %v %#v", c.p, e, m)
		}
	}
}

func TestLookaroundReportsConsumedSpanOnly(t *testing.T) {
	s, err := scankit.Compile([]scankit.Expression{{Id: 1, Pattern: `foo(?=bar)`}})
	if err != nil {
		t.Fatal(err)
	}
	matches, err := s.Scan([]byte("foobar"))
	if err != nil || len(matches) != 1 || matches[0] != (scankit.Match{Id: 1, From: 0, To: 3}) {
		t.Fatalf("lookaround span mismatch: %v %#v", err, matches)
	}
}

func TestUTF8LookbehindUsesByteOffsets(t *testing.T) {
	s, err := scankit.Compile([]scankit.Expression{{Id: 1, Pattern: `(?<=\p{L})x`, Flags: scankit.CompileUTF8 | scankit.CompileUCP}})
	if err != nil {
		t.Fatal(err)
	}
	matches, err := s.Scan([]byte("中文x"))
	if err != nil || len(matches) != 1 || matches[0] != (scankit.Match{Id: 1, From: 6, To: 7}) {
		t.Fatalf("utf8 lookbehind mismatch: %v %#v", err, matches)
	}
}

func TestEndBeforeFinalNewline(t *testing.T) {
	for _, input := range []string{"a", "a\n", "a\r\n"} {
		s, err := scankit.Compile([]scankit.Expression{{Id: 1, Pattern: `a\Z`}})
		if err != nil {
			t.Fatal(err)
		}
		matches, err := s.Scan([]byte(input))
		if err != nil || len(matches) != 1 || matches[0] != (scankit.Match{Id: 1, From: 0, To: 1}) {
			t.Fatalf("input %q: %v %#v", input, err, matches)
		}
	}
}

func TestLookaroundVariableWidthAndWordBoundaryNegation(t *testing.T) {
	cases := []struct {
		pattern string
		input   string
		want    []scankit.Match
	}{
		{`(?<!ab)z`, "abz axz z", []scankit.Match{{Id: 1, From: 6, To: 7}, {Id: 1, From: 8, To: 9}}},
		{`\Bcat\B`, "scat cat scatter", []scankit.Match{{Id: 1, From: 10, To: 13}}},
		{`foo(?!bar|baz)`, "foobat foobar foobaz", []scankit.Match{{Id: 1, From: 0, To: 3}}},
	}
	for _, tc := range cases {
		scanner, err := scankit.Compile([]scankit.Expression{{Id: 1, Pattern: tc.pattern}})
		if err != nil {
			t.Fatalf("compile %q: %v", tc.pattern, err)
		}
		got, err := scanner.Scan([]byte(tc.input))
		if err != nil || len(got) != len(tc.want) {
			t.Fatalf("pattern %q: err=%v got=%#v want=%#v", tc.pattern, err, got, tc.want)
		}
		for i := range tc.want {
			if got[i] != tc.want[i] {
				t.Fatalf("pattern %q: got=%#v want=%#v", tc.pattern, got, tc.want)
			}
		}
	}
}

func TestSOMLeftmostKeepsEarliestStartPerEnd(t *testing.T) {
	scanner, err := scankit.Compile([]scankit.Expression{{Id: 1, Pattern: `a*`, Flags: scankit.CompileAllowEmpty | scankit.CompileSOMLeftmost}})
	if err != nil {
		t.Fatal(err)
	}
	matches, err := scanner.Scan([]byte("a"))
	if err != nil {
		t.Fatal(err)
	}
	for _, match := range matches {
		if match.To == 1 && match.From != 0 {
			t.Fatalf("SOM 未保留最左起点: %#v", matches)
		}
	}
}

// ---- 以下用例来自原根目录的 combination_runtime_test.go ----

func TestCombinationNegativeReportsAtEndOfData(t *testing.T) {
	s, err := scankit.Compile([]scankit.Expression{{Id: 1, Pattern: "a", Flags: scankit.CompileQuiet}, {Id: 2, Pattern: "!1", Flags: scankit.CompileCombination}})
	if err != nil {
		t.Fatal(err)
	}
	matches, err := s.Scan([]byte("bbb"))
	if err != nil || len(matches) != 1 || matches[0] != (scankit.Match{Id: 2, From: 3, To: 3}) {
		t.Fatalf("negative EOD match mismatch: %v %#v", err, matches)
	}
	matches, err = s.Scan([]byte("aba"))
	if err != nil || len(matches) != 0 {
		t.Fatalf("negative combination ignored operand hit: %v %#v", err, matches)
	}
}

func TestCombinationOnlyReportsForRelevantOperands(t *testing.T) {
	s, err := scankit.Compile([]scankit.Expression{
		{Id: 1, Pattern: "a", Flags: scankit.CompileQuiet},
		{Id: 2, Pattern: "b", Flags: scankit.CompileQuiet},
		{Id: 3, Pattern: "c", Flags: scankit.CompileQuiet},
		{Id: 4, Pattern: "1&2", Flags: scankit.CompileCombination},
	})
	if err != nil {
		t.Fatal(err)
	}
	matches, err := s.Scan([]byte("abcc"))
	if err != nil || len(matches) != 1 || matches[0] != (scankit.Match{Id: 4, From: 1, To: 2}) {
		t.Fatalf("unrelated operand retriggered combination: %v %#v", err, matches)
	}
}

func TestCombinationSingleMatchAndExtensionGate(t *testing.T) {
	s, err := scankit.Compile([]scankit.Expression{
		{Id: 1, Pattern: "a"},
		{Id: 2, Pattern: "1", Flags: scankit.CompileCombination | scankit.CompileQuiet | scankit.CompileSingleMatch},
		{Id: 3, Pattern: "1", Flags: scankit.CompileCombination, Ext: &scankit.ExpressionExt{Flags: scankit.ExtFlagMinOffset, MinOffset: 2}},
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
	scanner, err := scankit.Compile([]scankit.Expression{
		{Id: 1, Pattern: "a", Flags: scankit.CompileQuiet},
		{Id: 2, Pattern: "b", Flags: scankit.CompileQuiet},
		{Id: 3, Pattern: "c", Flags: scankit.CompileQuiet},
		{Id: 4, Pattern: "(1&2)|3", Flags: scankit.CompileCombination},
	})
	if err != nil {
		t.Fatal(err)
	}
	matches, err := scanner.Scan([]byte("ab"))
	if err != nil || len(matches) != 1 || matches[0] != (scankit.Match{Id: 4, From: 1, To: 2}) {
		t.Fatalf("嵌套组合位置错误: %#v err=%v", matches, err)
	}
}

func TestCombinationPositiveBranchDoesNotRepeatAtEOD(t *testing.T) {
	scanner, err := scankit.Compile([]scankit.Expression{
		{Id: 1, Pattern: "a", Flags: scankit.CompileQuiet},
		{Id: 2, Pattern: "b", Flags: scankit.CompileQuiet},
		{Id: 3, Pattern: "1|!2", Flags: scankit.CompileCombination},
	})
	if err != nil {
		t.Fatal(err)
	}
	matches, err := scanner.Scan([]byte("a"))
	if err != nil || len(matches) != 1 || matches[0] != (scankit.Match{Id: 3, From: 0, To: 1}) {
		t.Fatalf("组合 EOD 重复报告=%v err=%v", matches, err)
	}
}

// ---- 以下用例来自原根目录的 scan_conformance_test.go ----

func TestSingleBlockConformanceMatrix(t *testing.T) {
	cases := []struct {
		name string
		expr scankit.Expression
		data []byte
		want int
	}{
		{"utf8", scankit.Expression{Id: 1, Pattern: "é", Flags: scankit.CompileUTF8}, []byte("xé"), 1},
		{"boundary", scankit.Expression{Id: 2, Pattern: `\bcat\b`}, []byte("cat scatter"), 1},
		{"backref", scankit.Expression{Id: 3, Pattern: `(ab)\1`}, []byte("abab"), 1},
		{"repeat", scankit.Expression{Id: 4, Pattern: `a{2,3}`}, []byte("aaa"), 1},
		{"hamming", scankit.Expression{Id: 5, Pattern: "abcd", Ext: &scankit.ExpressionExt{Flags: scankit.ExtFlagHammingDistance, HammingDistance: 1}}, []byte("abxd"), 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			scanner, err := scankit.Compile([]scankit.Expression{tc.expr})
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
	scanner, err := scankit.Compile([]scankit.Expression{{Id: 1, Pattern: "\\xff"}})
	if err != nil {
		t.Fatal(err)
	}
	matches, err := scanner.Scan([]byte{0xff})
	if err != nil || len(matches) != 1 || matches[0] != (scankit.Match{Id: 1, From: 0, To: 1}) {
		t.Fatalf("二进制模式错误: %#v, %v", matches, err)
	}
}

func TestSingleScanEmptyAndNULConformance(t *testing.T) {
	scanner, err := scankit.Compile([]scankit.Expression{
		{Id: 1, Pattern: `\x00`},
		{Id: 2, Pattern: `a*`, Flags: scankit.CompileAllowEmpty},
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
	scanner, err := scankit.Compile([]scankit.Expression{{
		Id:      7,
		Pattern: `ab{2}`,
		Ext:     &scankit.ExpressionExt{Flags: scankit.ExtFlagHammingDistance, HammingDistance: 1},
	}})
	if err != nil {
		t.Fatal(err)
	}
	matches, err := scanner.Scan([]byte("abb abx"))
	if err != nil || len(matches) != 2 {
		t.Fatalf("固定重复模糊匹配=%v err=%v", matches, err)
	}
	if matches[0] != (scankit.Match{Id: 7, From: 0, To: 3}) || matches[1] != (scankit.Match{Id: 7, From: 4, To: 7}) {
		t.Fatalf("固定重复模糊区间=%v", matches)
	}
}

func TestAlternationFuzzyConfirmsEachLiteralBranch(t *testing.T) {
	scanner, err := scankit.Compile([]scankit.Expression{{Id: 8, Pattern: `cat|dog`, Ext: &scankit.ExpressionExt{Flags: scankit.ExtFlagHammingDistance, HammingDistance: 1}}})
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

func TestHammingFixedClassUsesDirectConfirmation(t *testing.T) {
	scanner, err := scankit.Compile([]scankit.Expression{{
		Id:      9,
		Pattern: `a[bc]d`,
		Ext:     &scankit.ExpressionExt{Flags: scankit.ExtFlagHammingDistance, HammingDistance: 1},
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
	scanner, err := scankit.Compile([]scankit.Expression{{
		Id:      10,
		Pattern: `a[bc]d`,
		Ext:     &scankit.ExpressionExt{Flags: scankit.ExtFlagEditDistance, EditDistance: 1},
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
	scanner, err := scankit.Compile([]scankit.Expression{{
		Id: 11, Pattern: `[a-c]x`, Flags: scankit.CompileCaseless,
		Ext: &scankit.ExpressionExt{Flags: scankit.ExtFlagHammingDistance, HammingDistance: 0},
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
	scanner, err := scankit.Compile([]scankit.Expression{{
		Id:      91,
		Pattern: `[^a]`,
		Ext:     &scankit.ExpressionExt{Flags: scankit.ExtFlagHammingDistance, HammingDistance: 0},
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
	scanner, err := scankit.Compile([]scankit.Expression{{
		Id: 12, Pattern: `[A-C]x`, Flags: scankit.CompileCaseless,
		Ext: &scankit.ExpressionExt{Flags: scankit.ExtFlagHammingDistance, HammingDistance: 0},
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
	scanner, err := scankit.Compile([]scankit.Expression{{
		Id: 13, Pattern: `(?:a[bc]d|x[yz])`,
		Ext: &scankit.ExpressionExt{Flags: scankit.ExtFlagHammingDistance, HammingDistance: 0},
	}})
	if err != nil {
		t.Fatal(err)
	}
	matches, err := scanner.Scan([]byte("abd axd xyy xzd"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 3 || matches[0] != (scankit.Match{Id: 13, From: 0, To: 3}) || matches[1] != (scankit.Match{Id: 13, From: 8, To: 10}) || matches[2] != (scankit.Match{Id: 13, From: 12, To: 14}) {
		t.Fatalf("带字符类分支的汉明确认结果错误: %#v", matches)
	}
}

func TestFuzzyAlternationWithClassesSupportsEditWidthChanges(t *testing.T) {
	scanner, err := scankit.Compile([]scankit.Expression{{
		Id: 14, Pattern: `(?:ab[cd]|x[yz])`,
		Ext: &scankit.ExpressionExt{Flags: scankit.ExtFlagEditDistance, EditDistance: 1},
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

func TestFuzzyWildcardAtomHonorsDotAll(t *testing.T) {
	without, err := scankit.Compile([]scankit.Expression{{Id: 31, Pattern: `a.b`, Ext: &scankit.ExpressionExt{Flags: scankit.ExtFlagHammingDistance, HammingDistance: 0}}})
	if err != nil {
		t.Fatal(err)
	}
	got, err := without.Scan([]byte("a\nb axb"))
	if err != nil || len(got) != 1 || got[0].From != 4 {
		t.Fatalf("通配原子换行语义错误: %#v, %v", got, err)
	}
	with, err := scankit.Compile([]scankit.Expression{{Id: 32, Pattern: `a.b`, Flags: scankit.CompileDotAll, Ext: &scankit.ExpressionExt{Flags: scankit.ExtFlagHammingDistance, HammingDistance: 0}}})
	if err != nil {
		t.Fatal(err)
	}
	got, err = with.Scan([]byte("a\nb"))
	if err != nil || len(got) != 1 || got[0].From != 0 || got[0].To != 3 {
		t.Fatalf("通配原子 DotAll 语义错误: %#v, %v", got, err)
	}
}

// ---- 以下用例来自原根目录的 required_index_scan_test.go（其中仅用公开 API 表达的差分用例）----

// TestEmailLiteralCollapseMatchesRegexp 用确定性随机语料对 Email 规则做差分验证：
// 命中集合必须与 Go 标准库 regexp 逐条一致。语料覆盖空/超长局部部分、非法局部字符、
// 空标签、超长标签、尾随连字符标签与多标签域名，用于约束 Email 规则在字面量折叠
// （collapse head）快路径上的行为。
func TestEmailLiteralCollapseMatchesRegexp(t *testing.T) {
	pattern := `[A-Za-z0-9.!#$%&'*+/?^_` + "`" + `{|}~-]{1,64}@[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?(?:\.[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?)+\b`
	scanner, err := scankit.Compile([]scankit.Expression{{Id: 1, Pattern: pattern}})
	if err != nil {
		t.Fatal(err)
	}
	reference := regexp.MustCompile(pattern)
	rng := rand.New(rand.NewPCG(7, 11))
	pick := func(alphabet string, limit int) string {
		length := rng.IntN(limit)
		buf := make([]byte, length)
		for index := range buf {
			buf[index] = alphabet[rng.IntN(len(alphabet))]
		}
		return string(buf)
	}
	filler := " \t=,:;中文\nlevel=INFO service=payment "
	hits := 0
	for range 4000 {
		var builder strings.Builder
		for part := rng.IntN(3) + 1; part > 0; part-- {
			builder.WriteString(pick(filler, 4))
			switch rng.IntN(6) {
			case 0:
				builder.WriteString("")
			case 1:
				builder.WriteString(strings.Repeat("z", 65))
			case 2:
				builder.WriteString("..")
			default:
				builder.WriteString(pick("abzAZ09.-_+!~", 12))
			}
			builder.WriteString("@")
			for label := rng.IntN(3) + 1; label > 0; label-- {
				switch rng.IntN(8) {
				case 0:
					builder.WriteString(".")
				case 1:
					builder.WriteString(strings.Repeat("q", 63))
				case 2:
					builder.WriteString("-")
				default:
					builder.WriteString(pick("abzAZ09-", 6))
				}
				builder.WriteString(".")
			}
			builder.WriteString(pick("abzAZ09", 4))
		}
		data := []byte(builder.String())
		want := make([]scankit.Match, 0)
		for _, index := range reference.FindAllIndex(data, -1) {
			want = append(want, scankit.Match{Id: 1, From: uint64(index[0]), To: uint64(index[1])})
		}
		got, err := scanner.Scan(data)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(got, want) {
			t.Fatalf("随机语料 %q 命中不一致:\n got=%v\nwant=%v", data, got, want)
		}
		hits += len(want)
	}
	if hits == 0 {
		t.Fatal("随机语料没有产出任何命中，未能覆盖命中路径")
	}
	t.Logf("随机语料累计命中=%d", hits)
}
