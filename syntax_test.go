package scankit

import (
	"regexp"
	"testing"
)

// TestSyntaxCoverage 每条用例只验证一种正则语法；输入数据经过挑选使期望匹配位置唯一，
// 便于在错误信息中直接看出是哪种语义的回退或支持缺失。
//
// 数据来源：docs/technical-solutions/scankit-block-mode/scankit-main-vs-dev-branch-strategy.md
// Section 3 of scankit-main-vs-dev-branch-strategy.md, all dev=YES rows.
type SyntaxCase struct {
	Name  string
	Pat   string
	Flags CompileFlag
	Data  string
	Want  []Match
}

// SyntaxCases 列出 dev 当前支持的每条正则语法，每个用例只验证一种语法。
// 由 syntax_test.go 与 syntax_bench_test.go 共同消费，避免重复定义。
var SyntaxCases = []SyntaxCase{
	// 字节字面量与转义
	{"literal_byte", `x`, 0, "ax", []Match{{1, 1, 2}}},
	{"escape_quoted", `\Qabc\E`, 0, "ababc", []Match{{1, 2, 5}}},
	{"escape_n", `\n`, 0, "a\n", []Match{{1, 1, 2}}},
	{"escape_r", `\r`, 0, "a\r", []Match{{1, 1, 2}}},
	{"escape_t", `\t`, 0, "a\t", []Match{{1, 1, 2}}},
	{"escape_f", `\f`, 0, "a\f", []Match{{1, 1, 2}}},
	{"escape_a", `\a`, 0, "a\a", []Match{{1, 1, 2}}},
	{"escape_e", `\e`, 0, "a\x1b" + "b", []Match{{1, 1, 2}}},
	{"escape_c", `\cA`, 0, "a\x01" + "b", []Match{{1, 1, 2}}},
	{"escape_hex", `\x41`, 0, "aAb", []Match{{1, 1, 2}}},
	{"escape_hex_braced", `\x{41}`, 0, "aAb", []Match{{1, 1, 2}}},
	{"escape_octal", `\101`, 0, "aAb", []Match{{1, 1, 2}}},
	{"escape_octal_braced", `\o{101}`, 0, "aAb", []Match{{1, 1, 2}}},
	{"escape_N_negated_newline", `\N+`, 0, "\nz", []Match{{1, 1, 2}}},
	{"escape_C_any_byte", `\Ca`, 0, "za", []Match{{1, 0, 2}}},

	// 通配符与锚点
	{"wildcard_dot", `.z`, 0, "az", []Match{{1, 0, 2}}},
	{"anchor_begin", `^z`, 0, "z", []Match{{1, 0, 1}}},
	{"anchor_end", `z$`, 0, "z", []Match{{1, 0, 1}}},
	{"anchor_AbsoluteStart", `\Az`, 0, "z", []Match{{1, 0, 1}}},
	{"anchor_AbsoluteEnd", `z\z`, 0, "z", []Match{{1, 0, 1}}},
	{"anchor_EndBeforeFinalNewline", `z\Z`, 0, "z", []Match{{1, 0, 1}}},
	{"boundary_word", `\bcat\b`, 0, "1 cat 5", []Match{{1, 2, 5}}},
	{"boundary_nonword", `\Bcat\B`, 0, "ocat5", []Match{{1, 1, 4}}},

	// 字符类
	{"class_simple", `[abc]z`, 0, "zbz", []Match{{1, 1, 3}}},
	{"class_negated", `[^x]z`, 0, "zz", []Match{{1, 0, 2}}},
	{"class_range", `[a-z]z`, 0, "1az", []Match{{1, 1, 3}}},
	{"class_posix_alpha", `[[:alpha:]]z`, 0, "1az", []Match{{1, 1, 3}}},
	{"class_posix_negated_alpha", `[[:^alpha:]]z`, 0, "1z", []Match{{1, 0, 2}}},
	{"class_posix_digit", `[[:digit:]]z`, 0, "5z", []Match{{1, 0, 2}}},
	{"class_posix_alnum", `[[:alnum:]]z`, 0, "5z", []Match{{1, 0, 2}}},
	{"class_posix_xdigit", `[[:xdigit:]]z`, 0, "fz", []Match{{1, 0, 2}}},
	{"class_posix_blank", `[[:blank:]]z`, 0, " z", []Match{{1, 0, 2}}},
	{"class_posix_cntrl", `[[:cntrl:]]z`, 0, "\x01z", []Match{{1, 0, 2}}},
	{"class_posix_graph", `[[:graph:]]z`, 0, "az", []Match{{1, 0, 2}}},
	{"class_posix_lower", `[[:lower:]]z`, 0, "az", []Match{{1, 0, 2}}},
	{"class_posix_print", `[[:print:]]z`, 0, "az", []Match{{1, 0, 2}}},
	{"class_posix_punct", `[[:punct:]]z`, 0, "!z", []Match{{1, 0, 2}}},
	{"class_posix_space", `[[:space:]]z`, 0, " z", []Match{{1, 0, 2}}},
	{"class_posix_upper", `[[:upper:]]z`, 0, "Az", []Match{{1, 0, 2}}},
	{"class_posix_word", `[[:word:]]z`, 0, "_z", []Match{{1, 0, 2}}},
	{"class_shorthand_digit", `\dz`, 0, "5z", []Match{{1, 0, 2}}},
	{"class_shorthand_non_digit", `\Dz`, 0, "az", []Match{{1, 0, 2}}},
	{"class_shorthand_word", `\wz`, 0, "az", []Match{{1, 0, 2}}},
	{"class_shorthand_non_word", `\Wz`, 0, "!z", []Match{{1, 0, 2}}},
	{"class_shorthand_space", `\sz`, 0, " z", []Match{{1, 0, 2}}},
	{"class_shorthand_non_space", `\Sz`, 0, "1z", []Match{{1, 0, 2}}},
	{"class_shorthand_hspace", `\hz`, 0, " z", []Match{{1, 0, 2}}},
	{"class_shorthand_non_hspace", `\Hz`, 0, "1z", []Match{{1, 0, 2}}},
	{"class_shorthand_vspace", `\vz`, 0, "\nz", []Match{{1, 0, 2}}},
	{"class_shorthand_non_vspace", `\Vz`, 0, "az", []Match{{1, 0, 2}}},
	{"class_linebreak_R", `\Rz`, 0, "\nz", []Match{{1, 0, 2}}},

	// Unicode 属性
	{"unicode_property_L", `\p{L}+z`, CompileUTF8 | CompileUCP, "12中文z", []Match{{1, 2, 9}}},
	{"unicode_negated_property", `\P{L}+z`, CompileUTF8 | CompileUCP, "12z", []Match{{1, 0, 3}}},

	// 数字序列匹配（以匹配数字为主要目的的规则）
	{"digit_run_shorthand", `\d+`, 0, "abc1234xy", []Match{{1, 3, 7}}},
	{"digit_run_range", `[0-9]+`, 0, "abc1234xy", []Match{{1, 3, 7}}},
	{"digit_run_posix", `[[:digit:]]+`, 0, "abc1234xy", []Match{{1, 3, 7}}},
	{"digit_run_unicode_property", `\p{Nd}+`, CompileUTF8 | CompileUCP, "abc１２３４xy", []Match{{1, 3, 15}}},
	{"integer_literal_plus", `1+`, 0, "abc111222xy", []Match{{1, 3, 6}}},
	{"float_like", `\d+\.\d+`, 0, "v=3.14 x", []Match{{1, 2, 6}}},

	// 量词
	{"quantifier_question", `ab?c`, 0, "abc", []Match{{1, 0, 3}}},
	{"quantifier_plus", `a+`, 0, "baaa", []Match{{1, 1, 4}}},
	{"quantifier_star_allowempty", `a*x`, CompileAllowEmpty, "aaax", []Match{{1, 0, 4}}},
	{"quantifier_exact_count", `a{3}`, 0, "baaa", []Match{{1, 1, 4}}},
	{"quantifier_min_count", `a{2,}`, 0, "baa", []Match{{1, 1, 3}}},
	{"quantifier_range_count", `a{2,3}x`, 0, "baaax", []Match{{1, 1, 5}}},
	{"quantifier_lazy_question", `ab??c`, 0, "abc", []Match{{1, 0, 3}}},
	{"quantifier_lazy_plus", `a+?z`, 0, "aaz", []Match{{1, 0, 3}}},
	{"quantifier_lazy_star_allowempty", `a*?z`, CompileAllowEmpty, "aaz", []Match{{1, 0, 3}}},
	{"quantifier_lazy_range", `a{2,3}?x`, 0, "baaax", []Match{{1, 1, 5}}},
	{"quantifier_possessive_plus", `a++z`, 0, "aaaz", []Match{{1, 0, 4}}},

	// 分组
	{"group_capture", `(ab)+x`, 0, "ababx", []Match{{1, 0, 5}}},
	{"group_non_capture", `(?:ab)+x`, 0, "ababx", []Match{{1, 0, 5}}},
	{"group_named", `(?<n>ab)+x`, 0, "ababx", []Match{{1, 0, 5}}},
	{"group_python_named", `(?P<n>ab)+x`, 0, "ababx", []Match{{1, 0, 5}}},
	{"group_atomic", `(?>ab)+x`, 0, "ababx", []Match{{1, 0, 5}}},

	// 注释与内联 flag
	{"comment", `(?#note)abc`, 0, "abc", []Match{{1, 0, 3}}},
	{"inline_flag_caseless", `(?i:abc)`, 0, "ABC", []Match{{1, 0, 3}}},
	{"inline_flag_multiline", `(?m:^z)`, 0, "z\nz", []Match{{1, 0, 1}, {1, 2, 3}}},
	{"inline_flag_dotall", `(?s:a.b)`, 0, "a\nb", []Match{{1, 0, 3}}},
	{"inline_flag_extended", `(?x:a b)`, 0, "ab", []Match{{1, 0, 2}}},
	{"inline_flag_toggle", `(?i:X)`, 0, "x", []Match{{1, 0, 1}}},

	// 环视
	{"lookahead", `a(?=b)`, 0, "ab", []Match{{1, 0, 1}}},
	{"negative_lookahead", `a(?!b)`, 0, "ax", []Match{{1, 0, 1}}},
	{"lookbehind", `(?<=a)b`, 0, "ab", []Match{{1, 1, 2}}},
	{"negative_lookbehind", `(?<!a)b`, 0, "xb", []Match{{1, 1, 2}}},

	// 反向引用
	{"backref_numeric", `(a)\1`, 0, "aa", []Match{{1, 0, 2}}},
	{"backref_named_k", `(?<n>a)\k<n>`, 0, "aa", []Match{{1, 0, 2}}},
	{"backref_named_g_angle", `(?<n>a)\g<n>`, 0, "aa", []Match{{1, 0, 2}}},
	{"backref_named_g_brace", `(?<n>a)\g{n}`, 0, "aa", []Match{{1, 0, 2}}},

	// 条件引用
	{"conditional_numeric", `(a)(?(1)b|c)`, 0, "ab", []Match{{1, 0, 2}}},
	{"conditional_named", `(?<n>a)(?(<n>)b|c)`, 0, "ab", []Match{{1, 0, 2}}},

	// 控制动词
	{"control_verb_ACCEPT", `a(*ACCEPT)`, 0, "ab", []Match{{1, 0, 1}}},
	{"control_verb_FAIL", `a(*FAIL)`, 0, "ab", nil},

	// Sequence 多字面量隐式连接
	{"sequence_literal_concat", `abc`, 0, "_abc_", []Match{{1, 1, 4}}},

	// 3+ 分支交替
	{"alternation_three_branches", `a|b|c`, 0, "xb", []Match{{1, 1, 2}}},

	// 反向引用 + 量词
	{"backref_with_quantifier", `(a)\1+`, 0, "aaaa", []Match{{1, 0, 4}}},

	// 边界符位置组合
	{"boundary_word_start_only", `\bcat`, 0, "cat", []Match{{1, 0, 3}}},
	{"boundary_word_end_only", `cat\b`, 0, "cat", []Match{{1, 0, 3}}},
	{"boundary_nonword_start", `\Bcat`, 0, "ocat", []Match{{1, 1, 4}}},
	{"boundary_nonword_end", `cat\B`, 0, "cat_", []Match{{1, 0, 3}}},

	// 交替
	{"alternation", `a|z`, 0, "zy", []Match{{1, 0, 1}}},
}

func TestSyntaxCoverage(t *testing.T) {
	for _, c := range SyntaxCases {
		s, err := Compile([]Expression{{Id: 1, Pattern: c.Pat, Flags: c.Flags}})
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

func matchEqual(got, want []Match) bool {
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
		expr []Expression
		data string
		want []Match
	}
	cases := []comboCase{
		// 1|2：组合规则的 Id 在每个操作数位置都会产生匹配。
		{
			name: "combinator_or",
			expr: []Expression{
				{Id: 1, Pattern: "foo"},
				{Id: 2, Pattern: "bar"},
				{Id: 3, Pattern: "1|2", Flags: CompileCombination},
			},
			data: "xfoo bar x",
			want: []Match{
				{Id: 1, From: 1, To: 4}, {Id: 3, From: 1, To: 4},
				{Id: 2, From: 5, To: 8}, {Id: 3, From: 5, To: 8},
			},
		},
		// 1&2：组合规则仅在两个操作数都命中的位置触发。
		{
			name: "combinator_and",
			expr: []Expression{
				{Id: 1, Pattern: "foo"},
				{Id: 2, Pattern: "bar"},
				{Id: 3, Pattern: "1&2", Flags: CompileCombination},
			},
			data: "foobar",
			want: []Match{
				{Id: 1, From: 0, To: 3},
				{Id: 2, From: 3, To: 6},
				{Id: 3, From: 3, To: 6},
			},
		},
		// !1：操作数完全无命中时，组合规则在数据末尾报告一次。
		{
			name: "combinator_not_no_operand",
			expr: []Expression{
				{Id: 1, Pattern: "foo", Flags: CompileQuiet},
				{Id: 2, Pattern: "!1", Flags: CompileCombination},
			},
			data: "xbarx",
			want: []Match{{Id: 2, From: 5, To: 5}},
		},
		// (1|2)&!1：先按括号优先级取 1 或 2，再与"非 1"求与。等价于"2"在无 1 命中的子串。
		{
			name: "combinator_parens_and_not",
			expr: []Expression{
				{Id: 1, Pattern: "foo"},
				{Id: 2, Pattern: "bar"},
				{Id: 3, Pattern: "(1|2)&!1", Flags: CompileCombination},
			},
			data: "xbar foo y",
			want: []Match{
				{Id: 2, From: 1, To: 4},
				{Id: 3, From: 1, To: 4},
				{Id: 1, From: 5, To: 8},
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s, err := Compile(c.expr)
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
		want []Match
	}
	cases := []tc{
		// === 联系方式 ===
		{"email", `[\w.+-]+@[\w.-]+\.[a-zA-Z]{2,}`, "联系 support@example.com 或 info@test.co.cn",
			[]Match{{1, 7, 26}, {1, 31, 46}}},
		{"url_http", `https?://[\w./-]+`, "访问 https://example.com/path 或 http://a.b/c",
			[]Match{{1, 7, 31}, {1, 36, 48}}},
		{"phone_cn_mobile", `1[3-9]\d{9}`, "电话 13800138000",
			[]Match{{1, 7, 18}}},
		{"phone_with_dash", `1[3-9]\d{1}-?\d{4}-?\d{4}`, "13912345678 或 139-1234-5678",
			[]Match{{1, 0, 11}, {1, 16, 29}}},

		// === 网络标识 ===
		{"ipv4", `\d+\.\d+\.\d+\.\d+`, "服务器 192.168.1.1",
			[]Match{{1, 10, 21}}},
		{"ipv4_with_port", `\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}:\d{1,5}`, "连接 192.168.1.1:8080",
			[]Match{{1, 7, 23}}},
		{"ipv4_cidr", `\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}/\d{1,2}`, "网段 10.0.0.0/24",
			[]Match{{1, 7, 18}}},
		{"mac_address", `[0-9a-fA-F]{2}(:[0-9a-fA-F]{2}){5}`, "MAC AA:BB:CC:DD:EE:FF",
			[]Match{{1, 4, 21}}},
		{"uuid", `[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`,
			"id=550e8400-e29b-41d4-a716-446655440000",
			[]Match{{1, 3, 39}}},
		{"http_status", `\b\d{3}\b`, "状态 200 404 500",
			[]Match{{1, 7, 10}, {1, 11, 14}, {1, 15, 18}}},

		// === 时间日期 ===
		{"date_iso", `\d{4}-\d{2}-\d{2}`, "日期 2026-09-24",
			[]Match{{1, 7, 17}}},
		{"time_hms", `\d{2}:\d{2}:\d{2}`, "时间 12:34:56",
			[]Match{{1, 7, 15}}},
		{"date_slash", `\d{1,2}/\d{1,2}/\d{4}`, "日期 9/24/2026",
			[]Match{{1, 7, 16}}},

		// === 数值 ===
		{"float_number", `-?\d+\.\d+`, "温度 -3.14",
			[]Match{{1, 7, 12}}},
		{"percentage", `\d+\.\d+%`, "增长 12.5%",
			[]Match{{1, 7, 12}}},
		{"number_with_comma", `\d{1,3}(,\d{3})+`, "金额 1,234,567",
			[]Match{{1, 7, 16}}},

		// === 路径与文件 ===
		{"unix_path", `/[\w./-]+`, "路径 /usr/local/bin/scankit",
			[]Match{{1, 7, 29}}},
		{"file_ext", `\w+\.\w{2,4}`, "文件 README.md",
			[]Match{{1, 7, 16}}},

		// === HTML / 标记 ===
		{"html_tag", `<[a-z]+[^>]*>`, `<div class="x">`,
			[]Match{{1, 0, 15}}},
		{"hex_value_word_boundary", `\b0x[0-9a-fA-F]+\b`, "地址 0xDEADBEEF",
			[]Match{{1, 7, 17}}},
		{"quoted_string", `"[^"]*"`, `他说 "hello world"`,
			[]Match{{1, 7, 20}}},
		{"json_string_escaped", `"[^"\\]*(?:\\.[^"\\]*)*"`, `"hello \"world\""`,
			[]Match{{1, 0, 17}}},

		// === 中文场景 ===
		{"chinese_chars", `[\p{Han}]+`, "中文 abc 字符",
			[]Match{{1, 7, 8}}},
		{"chinese_id_18", `\d{17}[\dXx]`, "身份证 11010120000101123X",
			[]Match{{1, 10, 28}}},

		// === 编程语言 ===
		{"c_identifier", `[a-zA-Z_]\w*`, "变量 my_var123",
			[]Match{{1, 7, 16}}},
		{"semver", `\d+\.\d+\.\d+`, "版本 1.2.3",
			[]Match{{1, 7, 12}}},
		{"git_sha", `[0-9a-f]{7,40}`, "commit abc1234def5678",
			[]Match{{1, 7, 21}}},
		{"hex_color", `#[0-9a-fA-F]{6}\b`, "颜色 #FFAABB 和 #00FF00",
			[]Match{{1, 7, 14}, {1, 19, 26}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s, err := Compile([]Expression{{Id: 1, Pattern: c.pat}})
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
		s, err := Compile([]Expression{{Id: 1, Pattern: c.Pat, Flags: c.Flags}})
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
	if c.Flags&(CompileUTF8|CompileUCP) != 0 {
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
