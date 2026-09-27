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

	// 前缀字节约束枚举（guardRun）在「整条规则就是约束集合的无上界贪婪重复」时
	// 只登记连续段首起点；以下用例锁定该收敛的语义边界。
	// 一条连续段只产出一个最左命中：贪婪重复消费到段尾，段内其它起点都被重叠抑制。
	{"guard_run_unbounded_repeat_single_run", `[[:alpha:]]{5,}`, 0, "abcdefghij",
		[]scankit.Match{{Id: 1, From: 0, To: 10}}},
	// 多条连续段各自产出一个命中，非连续段不产出。
	{"guard_run_unbounded_repeat_multi_run", `[[:alpha:]]{5,}`, 0, "abcdefg12hijklmn",
		[]scankit.Match{{Id: 1, From: 0, To: 7}, {Id: 1, From: 9, To: 16}}},
	// 收敛判据必须排除「窗口不在规则起点」：这里合法起点是 1 而不是数字段起点之前的 0，
	// 只登记窗口对应的最左起点会漏报。
	{"guard_run_offset_prefix_keeps_later_start", `[0ab][[:digit:]]{8,}`, 0, "c0123456789",
		[]scankit.Match{{Id: 1, From: 1, To: 11}}},
	// 尾约束不影响段首起点：贪婪重复仍然消费到段尾，各起点的尾约束位置相同。
	{"guard_run_unbounded_repeat_trailing_class", `[[:alpha:]]{5,}[0-9]`, 0, "abcdefgh1",
		[]scankit.Match{{Id: 1, From: 0, To: 9}}},
	// 收敛只适用于无上界重复：有上界时同一条连续段会被切成多个命中，不能只登记段首。
	{"guard_run_bounded_repeat_keeps_all_starts", `[[:alpha:]]{2,4}`, 0, "aaaaaa",
		[]scankit.Match{{Id: 1, From: 0, To: 4}, {Id: 1, From: 4, To: 6}}},

	// 入口是「集合重复 + 接受」时确认求值直接由连续段求出（见 §51）：
	// 贪婪取最大消费长度、非贪婪取最小消费长度、长度不足下限则不命中。
	{"set_repeat_accept_greedy_bounded", `x{2,4}`, 0, "axxxxb",
		[]scankit.Match{{Id: 1, From: 1, To: 5}}},
	{"set_repeat_accept_truncated_at_end", `x{2,4}`, 0, "xx",
		[]scankit.Match{{Id: 1, From: 0, To: 2}}},
	{"set_repeat_accept_below_minimum", `x{2,4}`, 0, "axb",
		nil},
	{"set_repeat_accept_lazy_bounded", `x{2,4}?`, 0, "xxxx",
		[]scankit.Match{{Id: 1, From: 0, To: 2}, {Id: 1, From: 2, To: 4}}},
	{"set_repeat_accept_unbounded", `[[:alpha:]]{5,}`, 0, "ab12cdefgh",
		[]scankit.Match{{Id: 1, From: 4, To: 10}}},
	{"set_repeat_accept_caseless", `(?i)x{2,4}`, 0, "xXxx",
		[]scankit.Match{{Id: 1, From: 0, To: 4}}},

	// 有上界自重复按重复上界收敛候选起点（见 §52）：段内被报告的起点恰好是
	// 「段首 + k*上界」且不超过 段尾-下限，与整段逐起点确认的结果逐位相同。
	{"set_repeat_bounded_stride_long_run", `x{2,4}`, 0, "a" + strings.Repeat("x", 10) + "b",
		[]scankit.Match{{Id: 1, From: 1, To: 5}, {Id: 1, From: 5, To: 9}, {Id: 1, From: 9, To: 11}}},
	{"set_repeat_bounded_stride_class_long_run", `\d{2,4}`, 0, "a" + strings.Repeat("7", 9) + "b",
		[]scankit.Match{{Id: 1, From: 1, To: 5}, {Id: 1, From: 5, To: 9}}},
	{"set_repeat_bounded_stride_shorter_than_max", `x{3,5}`, 0, "axxxb",
		[]scankit.Match{{Id: 1, From: 1, To: 4}}},

	// 首字节约束是「集合的无上界贪婪重复」时窗口只登记连续段首起点（见 §53）：
	// 段内更靠右的起点与前一次命中的结束位置重合时必须补齐，否则会漏报。
	{"dense_run_head_email", `[a-z]+@[a-z]+\.example`, 0, "user@host.example",
		[]scankit.Match{{Id: 1, From: 0, To: 17}}},
	{"dense_run_head_match_ends_inside_run", `[a-z]+\.x`, 0, "x.xb.x",
		[]scankit.Match{{Id: 1, From: 0, To: 3}, {Id: 1, From: 3, To: 6}}},
	{"dense_run_head_second_run_head_rejected", `[a-z]+\.x`, 0, "x.xb.a.x",
		[]scankit.Match{{Id: 1, From: 0, To: 3}, {Id: 1, From: 5, To: 8}}},
	{"dense_run_head_adjacent_run", `[a-z]+@[a-z]+\.example`, 0, "a@b.examplecd@f.example",
		[]scankit.Match{{Id: 1, From: 0, To: 11}, {Id: 1, From: 11, To: 23}}},
	{"dense_run_head_suffix_class", `[a-z]+[0-9]`, 0, "ab12",
		[]scankit.Match{{Id: 1, From: 0, To: 3}}},
	// 重复之后的消费字节仍属于重复集合时不能收敛：回溯可能在同一连续段内部
	// 命中，段内起点不再共享同一个结束偏移。
	{"dense_run_head_backtrack_inside_run", `[a-z]+x`, 0, "aax",
		[]scankit.Match{{Id: 1, From: 0, To: 3}}},
	{"dense_run_head_backtrack_prefers_run_end", `[a-z]+x`, 0, "axb",
		[]scankit.Match{{Id: 1, From: 0, To: 2}}},

	// 入口是一组等长文字分支时确认程序会跳过入口比较；交替命中仍按左最左、
	// 每个起点取最长分支报告（见 §50）。
	{"alternation_literal_variants", `ab|cd`, 0, "xabzcdy",
		[]scankit.Match{{Id: 1, From: 1, To: 3}, {Id: 1, From: 4, To: 6}}},
	{"alternation_literal_variants_with_suffix", `(?:ab|cd)ef`, 0, "zabefy",
		[]scankit.Match{{Id: 1, From: 1, To: 5}}},
	{"alternation_literal_variants_three", `ab|cd|ef`, 0, "zefab",
		[]scankit.Match{{Id: 1, From: 1, To: 3}, {Id: 1, From: 3, To: 5}}},
	{"alternation_literal_variants_caseless", `(?i)ab|cd`, 0, "xABycd",
		[]scankit.Match{{Id: 1, From: 1, To: 3}, {Id: 1, From: 4, To: 6}}},
	// 分支长度不一致时必须退回顾有确认路径：`abc|abcd` 在起点 0 取最长分支。
	{"alternation_unequal_variant_lengths", `abc|abcd`, 0, "zabcdy",
		[]scankit.Match{{Id: 1, From: 1, To: 5}}},

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

	// 忽略大小写：内联 (?i) 与表达式级标志必须产生相同的折叠语义。
	{"caseless_inline_literal", `(?i)cat`, 0, "aCATb", []scankit.Match{{Id: 1, From: 1, To: 4}}},
	{"caseless_flag_literal", `cat`, scankit.CompileCaseless, "aCATb", []scankit.Match{{Id: 1, From: 1, To: 4}}},
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

// TestBoundedSelfRepeatStrideMatchesReference 验证「有上界自重复」的候选收敛
// （见 §52：段内只登记「段首 + k*上界」）在长连续段、跨 64 字节宽窗口的连续段、
// 尾部截断与最小长度边界上与参考实现逐位一致，并且在标量宽窗口后端
// （不同候选来源）与原生后端上给出同一结果。
func TestBoundedSelfRepeatStrideMatchesReference(t *testing.T) {
	// 只取下限小于上限的形态：定长重复（`x{2,2}`）被解析成精确文字，其重叠语义
	// 由 TestLiteralOverlapConsistentAcrossCompilationShapes 单独覆盖，这里只比较
	// 可变长度重复的候选收敛。
	patterns := []string{
		`x{2,4}`, `x{3,5}`, `x{2,8}`,
		`\d{2,4}`, `\d{3,6}`, `[a-z]{2,5}`, `[[:alpha:]]{4,6}`, `[0-9a-f]{2,7}`,
	}
	inputs := []string{
		"",
		"x",
		"xx",
		"xxx",
		strings.Repeat("x", 200),
		"a" + strings.Repeat("x", 137) + "b",
		strings.Repeat("x", 63) + "ax" + strings.Repeat("x", 63),
		strings.Repeat("7", 129),
		"ab" + strings.Repeat("Z7", 70),
		strings.Repeat("ab", 100),
		strings.Repeat("abc123", 40),
		strings.Repeat("\n", 40),
	}
	backends := backendMatrixEntries()
	for _, pattern := range patterns {
		re := regexp.MustCompile(pattern)
		for index, input := range inputs {
			want := re.FindAllStringIndex(input, -1)
			var reference []scankit.Match
			for _, span := range want {
				reference = append(reference, scankit.Match{Id: 1, From: uint64(span[0]), To: uint64(span[1])})
			}
			for _, backend := range backends {
				got := scanWithBackend(t, backend.backend, []scankit.Expression{{Id: 1, Pattern: pattern}}, []byte(input))
				if !matchEqual(got, reference) {
					t.Fatalf("%q input=%d backend=%s: got %v want %v", pattern, index, backend.name, got, reference)
				}
			}
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
	// 表达式级忽略大小写在 scankit 中由 Flags 承载，regexp 需要用内联 (?i)
	// 表达同一个语义，否则两侧的工作量不可比。
	pattern := c.Pat
	if c.Flags&scankit.CompileCaseless != 0 && !strings.HasPrefix(pattern, "(?i)") {
		pattern = "(?i)" + pattern
	}
	re, err := regexp.Compile(pattern)
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

// TestDenseRunHeadMatchesRegexp 用随机语料对「首字节约束是无上界贪婪重复」的
// 段首收敛路径（见 §53）做差分验证：候选窗口只登记连续段首起点，段内被重叠抑制
// 水位线切断的起点必须在确认阶段补登记（retryCollapsedCandidate）。语料刻意让
// 前一次命中结束在连续段内部，覆盖水位线落在段中的形态。
func TestDenseRunHeadMatchesRegexp(t *testing.T) {
	patterns := []string{
		`[a-z]+\.x`,
		`[a-z]+@[a-z]+\.example`,
		`[a-z]+@`,
		`[a-z]+[0-9]`,
		`[a-z]+x`,
		`[a-z]+xy`,
		`[a-z]+?x`,
		`(?i)[a-z]+x`,
		`[a-z]+x\b`,
		`[[:alpha:]]+\.x`,
		`[^a]+\.x`,
	}
	alphabets := []string{"abx@.", "abx@.AB", "abcxyz@.", "ab@.", "abcXYZ019@. \t", "aaaa"}
	rng := rand.New(rand.NewPCG(53, 59))
	pick := func(alphabet string, limit int) string {
		length := rng.IntN(limit)
		buf := make([]byte, length)
		for index := range buf {
			buf[index] = alphabet[rng.IntN(len(alphabet))]
		}
		return string(buf)
	}
	fixed := []string{
		"", "a", "x", "x.x", "x.xb.x", "aa.x", "a.xb.x", "user@host.example",
		"x@y@z.example", "a@b.examplecd@f.example",
		"bbb@xx@abx@bxaaab@@@b@b@.@.a@aa.@.@@@@aba@@..aaab.@@@.@@xab@b@.@@x.xba.xxbb@xba@a@b@b",
		"user@host.example",
		strings.Repeat("a", 70) + ".x" + strings.Repeat("b", 70) + ".x",
		strings.Repeat("x@y@z.example ", 20),
	}
	for _, pattern := range patterns {
		scanner, err := scankit.Compile([]scankit.Expression{{Id: 1, Pattern: pattern}})
		if err != nil {
			t.Fatalf("%q: compile: %v", pattern, err)
		}
		reference := regexp.MustCompile(pattern)
		inputs := append([]string(nil), fixed...)
		for range 600 {
			inputs = append(inputs, pick(alphabets[rng.IntN(len(alphabets))], 48))
		}
		hits := 0
		for _, input := range inputs {
			data := []byte(input)
			want := make([]scankit.Match, 0)
			for _, index := range reference.FindAllIndex(data, -1) {
				want = append(want, scankit.Match{Id: 1, From: uint64(index[0]), To: uint64(index[1])})
			}
			got, err := scanner.Scan(data)
			if err != nil {
				t.Fatalf("%q %q: scan: %v", pattern, input, err)
			}
			if !slices.Equal(got, want) {
				t.Fatalf("%q 在 %q 上命中不一致:\n got=%v\nwant=%v", pattern, input, got, want)
			}
			hits += len(want)
		}
		if hits == 0 {
			t.Fatalf("%q: 语料没有产出任何命中，未能覆盖命中路径", pattern)
		}
	}
}

// TestLiteralPrefixAssertionMatchesRegexp 用确定性随机语料对「入口文字 + 末尾词边界」
// 形态做差分验证。这类规则的候选起点由入口文字索引派生，确认阶段在入口文字之后
// 续跑确认表（见 confirmDFAStart）：既跳过入口文字的重复比较，又避免从规则起点
// 重走整条图。语料刻意混合合法主体与非法主体（空主体、超长重复、非法字符、末尾
// 断言失败），覆盖「入口文字出现但规则不成立」的失败路径。
func TestLiteralPrefixAssertionMatchesRegexp(t *testing.T) {
	patterns := []string{
		// Rules100 基准里的 Email 形态（入口文字 + 长字符类重复 + 末尾词边界）。
		`field03=(?:[A-Za-z0-9.!#$%&'*+/?^_` + "`" + `{|}~-]{1,64}@[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?(?:\.[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?)+\b)`,
		`token=[a-z]{3,8}\b`,
		`k=[0-9]{2,4}\b`,
		`p=[a-z]+\b`,
		`v=(?:ab|cd)\b`,
	}
	// 每条前缀配一组「合法主体」模板，其余字符从这个字母表里随机取，用来制造
	// 入口文字命中但确认失败的近失配。
	validBodies := map[string][]string{
		"field03=": {"user000@sample00.com", "a.b+c@x-y.example", "local@a.b.c.d"},
		"token=":   {"abc", "abcdefgh"},
		"k=":       {"12", "1234"},
		"p=":       {"abc", "zzz"},
		"v=":       {"ab", "cd"},
	}
	prefixes := []string{"field03=", "token=", "k=", "p=", "v="}
	rng := rand.New(rand.NewPCG(94, 17))
	pick := func(alphabet string, limit int) string {
		length := rng.IntN(limit)
		buf := make([]byte, length)
		for index := range buf {
			buf[index] = alphabet[rng.IntN(len(alphabet))]
		}
		return string(buf)
	}
	fixed := []string{
		"", "field03=", "field03=user000@sample00.com", "field03=user000@sample00.com.",
		"field03=@example.com", "field03=a@b.com ", "token=", "token=abc", "token=abc!",
		"k=12", "k=1", "k=12345", "p=", "p=abc", "v=ab", "v=cd", "v=ae",
		"field03=" + strings.Repeat("z", 65) + "@b.com",
		"field03=user000@sample00.com " + "field03=user001@sample01.com ",
		strings.Repeat("field03=a@b.com ", 8),
		strings.Repeat("token=abcdef ", 8),
	}
	for _, pattern := range patterns {
		scanner, err := scankit.Compile([]scankit.Expression{{Id: 1, Pattern: pattern}})
		if err != nil {
			t.Fatalf("%q: compile: %v", pattern, err)
		}
		reference := regexp.MustCompile(pattern)
		inputs := append([]string(nil), fixed...)
		// 先补一批「入口文字 + 合法主体」的确定性输入，保证每条模式都覆盖到
		// 确认成功路径，并让前后的分隔字节覆盖词边界断言的两个方向。
		for _, prefix := range prefixes {
			for _, body := range validBodies[prefix] {
				for range 60 {
					inputs = append(inputs, prefix+body)
					inputs = append(inputs, "x "+prefix+body+" y")
					inputs = append(inputs, prefix+body+"!")
				}
			}
		}
		for range 1500 {
			var builder strings.Builder
			for part := rng.IntN(3) + 1; part > 0; part-- {
				builder.WriteString(pick(" \t=,:;中文\n", 3))
				prefix := prefixes[rng.IntN(len(prefixes))]
				builder.WriteString(prefix)
				switch rng.IntN(6) {
				case 0:
					// 合法主体：覆盖确认成功路径。
					bodies := validBodies[prefix]
					builder.WriteString(bodies[rng.IntN(len(bodies))])
				case 1:
					// 空主体或超长重复：覆盖重复下界/上界失败。
					if rng.IntN(2) == 0 {
						break
					}
					builder.WriteString(strings.Repeat("z", 65))
				default:
					builder.WriteString(pick("abzAZ09.-_+!~@=", 14))
				}
			}
			inputs = append(inputs, builder.String())
		}
		hits := 0
		for _, input := range inputs {
			data := []byte(input)
			want := make([]scankit.Match, 0)
			for _, index := range reference.FindAllIndex(data, -1) {
				want = append(want, scankit.Match{Id: 1, From: uint64(index[0]), To: uint64(index[1])})
			}
			got, err := scanner.Scan(data)
			if err != nil {
				t.Fatalf("%q %q: scan: %v", pattern, input, err)
			}
			if !slices.Equal(got, want) {
				t.Fatalf("%q 在 %q 上命中不一致:\n got=%v\nwant=%v", pattern, input, got, want)
			}
			hits += len(want)
		}
		if hits < 200 {
			t.Fatalf("%q: 语料命中 %d，未充分覆盖命中路径", pattern, hits)
		}
		t.Logf("%q 命中=%d", pattern, hits)
	}
}

// TestEntryAssertionLiteralMatchesRegexp 差分验证「入口断言链 + 紧随入口文字」形态。
//
// 这类规则的确认程序入口是零宽断言（`\b`、`\B`、`(?m:^)` …）而不是文字比较，扫描
// 期改为「先在候选起点求值断言，再从入口文字之后续跑」。续跑的正确性依赖候选索引
// 已经证明入口文字成立，因此必须覆盖断言成立 / 不成立、候选位于输入首尾、以及行边界
// 与词边界的全部组合。
func TestEntryAssertionLiteralMatchesRegexp(t *testing.T) {
	patterns := []string{
		// 词边界入口
		`\bzzzzzzzz`, `\bword`, `\bword\b`, `\Bword`, `\bbbbb`,
		`\bzzzz`, `\b[a-z]{4}\b`, `\bword\b|\bother\b`, `\bzzzzzzzzz*`,
		// 2~3 字节入口文字走专用短文字匹配器，它同样把入口词边界折进首字节
		// 掩码（见 TestWordBoundaryShortSingleLiteralNarrow），需要独立的 Go
		// 差分覆盖，尤其是命中落在 64 字节窗口起点、前一位是单词字节的进位。
		`\bzz`, `\bzzz`, `\Bzz`, `\Bzzz`, `\bzz\b`, `\Bzzz\b`,
		// 入口断言之后没有可整体跳过的固定文字（confirmSkip 为 0）时，入口文字
		// 由字符类展开、偏移窗口又宽（可选前缀 86 让窗口跨 0~2），候选起点必须
		// 逐条求值入口断言；`\b(?:86)?1[3-9][0-9]{9}\b` 是这条路径的原型形态
		// （见 §69），`\b\d{3}\b` 覆盖同形态的更短文字。
		`\b(?:86)?1[3-9][0-9]{9}\b`, `\b\d{3}\b`,
		// 多行行首入口（入口断言 + 入口文字，作用域修饰符只影响断言）
		`(?m:^zzzzzzzz)`, `(?m:^token=[a-z]{3,8}\b)`, `(?-m:^zzz)`,
		`(?m:^z)`, `(?m:^ab|^cd)`,
		// 行首断言的尾巴形态：整行、任意尾巴、行尾断言、重复行首断言。
		`(?m:^zzzzzzzz$)`, `(?m:^word$)`, `(?m:^word.*)`, `(?m:^zz$)`,
		`(?m:^zz)$`,
		`(?m:^zz\nzz)`, `(?m:^token)=`,
	}
	rng := rand.New(rand.NewPCG(2026, 926))
	// 词元里既有入口文字的完整命中，也有被切断、被非词字符包围、跨行等情况，
	// 保证每条规则都能拿到足够多的命中与失败路径。
	tokens := []string{
		"word", "zzzzzzzz", "zzzz", "token=abc", "token=abcdefghij",
		"ab", "cd", "bbbbb", "aaaa", "abcd", "z", "x", " ", "\n", "\t", "_", ".", "0", "B",
		"13800138000", "8613800138000", "123",
	}
	separators := []string{" ", "\n", "\t", "x", ".", "-"}
	inputs := make([]string, 0, 8000)
	for i := 0; i < 8000; i++ {
		var builder strings.Builder
		for j := rng.IntN(10); j > 0; j-- {
			builder.WriteString(tokens[rng.IntN(len(tokens))])
			if rng.IntN(2) == 0 {
				builder.WriteString(separators[rng.IntN(len(separators))])
			}
		}
		inputs = append(inputs, builder.String())
	}
	// 补上断言最容易出错的确定性边界：空输入、纯命中、行首行尾、词边界前后。
	inputs = append(inputs,
		"", "z", "zzzzzzzz", "word", " word ", "xwordx", "wordword",
		"zzzzzzzzzzzz", "\nzzzzzzzz\n", "word\nword", "\n\n\n",
		"token=abc", "token=abcdefghij", " token=abc ", "ab", "cd", "^ab",
		// 多行行首断言最容易漏掉「整行命中」与「连续行首」两类输入：前者要求
		// 入口文字恰好占满一行，后者让相邻两行的起点只差一个换行。
		strings.Repeat("zzzzzzzz\n", 300),
		strings.Repeat("word\n", 300),
		strings.Repeat("zzzzzzzz", 300),
		strings.Repeat("zz\n", 300),
	)
	// 词边界入口的候选收窄把「前一位字节是否属于单词集合」折进宽窗口掩码，
	// 因此必须补一批命中恰好落在 64 字节窗口边界、且前一位字节是单词字节的输入：
	// `\Bword` 只有在这里才会跨越窗口取前一位，(错位的进位会直接漏报)。
	// 命中落在偏移 64 上时只有输入足够长（≥129 字节）才会进入第二个宽窗口，
	// 因此尾部必须继续补字节，否则该位置会走逐字节回退路径，测不到进位。
	inputs = append(inputs,
		strings.Repeat("a", 64)+"word"+strings.Repeat("a", 100),
		strings.Repeat("a", 65)+"word"+strings.Repeat("a", 100),
		strings.Repeat("a", 128)+"word"+strings.Repeat("a", 100),
		"a"+strings.Repeat("z", 63)+"word"+strings.Repeat("a", 100),
		strings.Repeat("z", 64)+"word"+strings.Repeat("z", 100),
	)
	// 再补一批「行首 + 输入末尾」定向输入：入口断言只在行首成立时，命中恰好
	// 落在输入末尾（`$`/`\z` 尾巴）的形态必须有足够多的样本。
	for i := 0; i < 300; i++ {
		inputs = append(inputs, strings.Repeat("y", i%23)+"\nzz")
	}
	for _, pattern := range patterns {
		reference, err := regexp.Compile(pattern)
		if err != nil {
			t.Fatalf("%q: regexp 编译失败: %v", pattern, err)
		}
		scanner, err := scankit.Compile([]scankit.Expression{{Id: 1, Pattern: pattern}})
		if err != nil {
			t.Fatalf("%q: compile: %v", pattern, err)
		}
		hits := 0
		for _, input := range inputs {
			data := []byte(input)
			want := make([]scankit.Match, 0)
			for _, index := range reference.FindAllIndex(data, -1) {
				want = append(want, scankit.Match{Id: 1, From: uint64(index[0]), To: uint64(index[1])})
			}
			got, err := scanner.Scan(data)
			if err != nil {
				t.Fatalf("%q %q: scan: %v", pattern, input, err)
			}
			if !slices.Equal(got, want) {
				t.Fatalf("%q 在 %q 上命中不一致:\n got=%v\nwant=%v", pattern, input, got, want)
			}
			hits += len(want)
		}
		if hits < 100 {
			t.Fatalf("%q: 语料命中 %d，未充分覆盖命中路径", pattern, hits)
		}
		t.Logf("%q 命中=%d", pattern, hits)
	}
}

// TestLineStartChannelSharedIndexMatchesSingleRule 验证行首通道与其它规则共用候选索引
// 时逐条规则的结果不变。
//
// 行首通道把换行并进候选文字后，候选文字会与同一批规则里的其它候选文字共享匹配器与
// 候选编号；编号、分组、命中顺序任一环节出错都会让某条规则少报或多报。这里对每条规则
// 分别用「单规则扫描」和「与其它规则一同扫描」两种编解码方式比较命中集合。
func TestLineStartChannelSharedIndexMatchesSingleRule(t *testing.T) {
	patterns := []string{
		`(?m:^zzzzzzzz)`, `(?m:^word$)`, `(?m:^token)=`, `(?m:^z)`,
		`\bzzzzzzzz`, `zzzzzzzz`, `needle`, `(?m:^ab|^cd)`,
	}
	// 语料刻意不出现 9 个以上连续同字节：本测试只比较不重叠的情形；重叠命中在
	// 各编译形态下的一致性由 TestLiteralOverlapConsistentAcrossCompilationShapes
	// 单独锁定，避免两类断言混在一起。
	corpus := strings.Join([]string{
		"zzzzzzzz", "\nzzzzzzzz\n", "word\n", "token=abc\n",
		"needle", "zzzzzzzz|zzzzzzzz", "\nword", "token=", "ab\ncd",
		"xzzzzzzzz@z", "\nzzzzzzzz|zzzzzzzz", "zzzzzzzz\nzzzzzzzz\nzzzzzzzz",
	}, "|")
	expressions := make([]scankit.Expression, 0, len(patterns))
	for index, pattern := range patterns {
		expressions = append(expressions, scankit.Expression{Id: uint32(index + 1), Pattern: pattern})
	}
	combined, err := scankit.Compile(expressions)
	if err != nil {
		t.Fatalf("combined compile: %v", err)
	}
	combinedMatches, err := combined.Scan([]byte(corpus))
	if err != nil {
		t.Fatalf("combined scan: %v", err)
	}
	for index, pattern := range patterns {
		id := uint32(index + 1)
		single, err := scankit.Compile([]scankit.Expression{{Id: id, Pattern: pattern}})
		if err != nil {
			t.Fatalf("%q: compile: %v", pattern, err)
		}
		want, err := single.Scan([]byte(corpus))
		if err != nil {
			t.Fatalf("%q: scan: %v", pattern, err)
		}
		got := make([]scankit.Match, 0, len(want))
		for _, match := range combinedMatches {
			if match.Id == id {
				got = append(got, match)
			}
		}
		if !slices.Equal(got, want) {
			t.Fatalf("%q 在共享候选索引下命中不一致:\n got=%v\nwant=%v", pattern, got, want)
		}
	}
}

// TestWordBoundaryLiteralSharedWithPlainRule 验证词边界入口的候选收窄不会把共享
// 同一候选文字的普通规则一起收窄。
//
// 收窄作用在候选文字的宽窗口掩码上（只对「入口是单条 \b/\B 且入口文字首字节属于
// 单词集合」的文字启用），会同时影响共用这条文字的全部规则：一旦普通规则与词边界
// 规则共用同一条文字，就必须整体放弃收窄，否则普通规则在单词内部的位置会被整批丢掉。
// 语料取连续重复文字，正是收窄会剔除最多位置、而普通规则必须全部报出的形态；两种
// 登记顺序都要覆盖，因为共享文字的收窄标记是逐条规则增量合并出来的。
func TestWordBoundaryLiteralSharedWithPlainRule(t *testing.T) {
	batches := [][]string{
		{`\babcdefgh`, `abcdefgh`},
		{`abcdefgh`, `\babcdefgh`},
		{`\Babcdefgh`, `abcdefgh`},
		{`\bword`, `word`},
		{`\bword`, `\Bword`, `word`},
	}
	// 文字取非周期串，且每次出现互不重叠：这里只比较收窄在单词内部的剔除行为，
	// 重叠命中在各编译形态下的一致性由
	// TestLiteralOverlapConsistentAcrossCompilationShapes 单独锁定。
	corpus := strings.Join([]string{
		"abcdefghabcdefghabcdefgh",
		"xabcdefghabcdefgh",
		"abcdefgh abcdefgh",
		"_abcdefgh",
		"abcdefgh_",
		"wordwordword",
		"word word",
		" word ",
		"xwordx",
		// 命中恰好落在 64 字节窗口边界上：收窄用的是「单词集合掩码左移一位」，
		// 第 0 位要由窗口前一位字节补齐，跨窗口的进位一旦算错就会在这里漏报。
		strings.Repeat("a", 64) + "abcdefgh",
		strings.Repeat("a", 64) + "word",
		strings.Repeat("a", 63) + " abcdefgh",
		strings.Repeat("a", 128) + " word",
	}, "|")
	for _, patterns := range batches {
		expressions := make([]scankit.Expression, 0, len(patterns))
		for index, pattern := range patterns {
			expressions = append(expressions, scankit.Expression{Id: uint32(index + 1), Pattern: pattern})
		}
		combined, err := scankit.Compile(expressions)
		if err != nil {
			t.Fatalf("%v: combined compile: %v", patterns, err)
		}
		combinedMatches, err := combined.Scan([]byte(corpus))
		if err != nil {
			t.Fatalf("%v: combined scan: %v", patterns, err)
		}
		for index, pattern := range patterns {
			id := uint32(index + 1)
			single, err := scankit.Compile([]scankit.Expression{{Id: id, Pattern: pattern}})
			if err != nil {
				t.Fatalf("%v %q: compile: %v", patterns, pattern, err)
			}
			want, err := single.Scan([]byte(corpus))
			if err != nil {
				t.Fatalf("%v %q: scan: %v", patterns, pattern, err)
			}
			got := make([]scankit.Match, 0, len(want))
			for _, match := range combinedMatches {
				if match.Id == id {
					got = append(got, match)
				}
			}
			if !slices.Equal(got, want) {
				t.Fatalf("%v 中 %q 在共享候选索引下命中不一致:\n got=%v\nwant=%v", patterns, pattern, got, want)
			}
			if len(want) == 0 {
				t.Fatalf("%v %q: 语料没有命中，测试失去意义", patterns, pattern)
			}
		}
	}
}

// TestWordBoundaryShortSingleLiteralNarrow 验证 2~3 字节与单字节入口文字的
// 词边界收窄正确性。
//
// 宽窗口掩码收窄最初只覆盖统一匹配器（长度不小于 4 的文字），随后复用到 2~3 字节
// 与单字节专用匹配器。单字节匹配器只在「候选集合里同时存在多字节文字」时才启用
// （全部候选都是单字节时走通用匹配器，它的跳过能力更好），因此这里把单字节、
// 2~3 字节与长文字组合编译，才能让三段收窄同时生效。收窄复用同一张首字节掩码，
// 必须整组约束一致才启用，所以同时覆盖「一致」（整组都是 `\b` 或整组都是 `\B`）
// 与「不一致」（与无断言的普通规则共享候选文字）两种登记。
//
// 语料刻意避开同一文字的重叠出现，只比较不重叠情形；重叠命中在各编译形态下的
// 一致性由 TestLiteralOverlapConsistentAcrossCompilationShapes 单独锁定。
func TestWordBoundaryShortSingleLiteralNarrow(t *testing.T) {
	batches := [][]string{
		// 整组 `\b`：单字节、2 字节、3 字节与长文字全部收窄为「前一位为非单词」。
		{`\bz`, `\bzz`, `\bzzz`, `\bzzzzzzzz`},
		// 整组 `\B`：同样三段都收窄，方向相反（前一位必须是单词字节）。
		{`\Ba`, `\Bqz`, `\Bqwz`, `\Bqqwweerr`},
		// 单字节与 2 字节候选同时被无断言规则共享：两个分组都退回不收窄，
		// 结果仍须与逐规则编译一致。
		{`\bz`, `z`, `\bxy`, `xy`, `\bword`},
		// 单字节候选达到 5 个：单字节匹配器从逐文字 memchr 切换到字节集合扫描，
		// 收窄因此落在窗口掩码上，与 memchr 的逐命中判定是两条不同的代码路径。
		{`\bz`, `\by`, `\bw`, `\bv`, `\bu`, `\bzzwwvv`},
		// 同一分组内混有可收窄与不可收窄的**不同**文字（单字节组 `z`/`a`、
		// 短文字组 `zz`/`qp`）：整组必须退回不收窄，否则普通规则在单词内部的
		// 位置会被掩码剔掉。与「同一条文字被两条规则共享」是两条不同的判据。
		{`\bz`, `a`, `\bzz`, `qp`, `\bword`},
	}
	corpus := strings.Join([]string{
		// 密集重复段：收窄剔除最多的形态，同时覆盖各种非词边界。
		"zzzzzzzzzz",
		"z", "zz", "zzz", "zzzz", "zzzzzzzz",
		" zz ", ".zz.", "_zz", "zz_", "-z-",
		"azzzzzzzz", "zzzzzzzzz", "zzazz", "zazaz",
		"aaazzz", "aaaazz",
		// `\B` 入口文字的独立出现（相互不重叠）。
		"aqz", "aqzb", "zaqz", "aqzqz",
		"aqwz", "aqqwz",
		"aqqwweerr", "zaqqwweerrz",
		"za", "aaza", "xa",
		// 普通 2 字节候选的独立出现（`xy` 不紧邻出现，避免重叠）。
		"xy", "xyx", "axyb", " xy ", "zxy",
		"word", "sword", "wordy",
		// 其余单字节入口文字的独立出现（前一位是非词字节，`\b` 成立）。
		" y ", " w ", " v ", " u ", ".y.", "-w-", "_v_", " u ", "zyxwvu",
		"zzwwvv", " zzwwvv", "-zzwwvv-",
		// 不可收窄的普通候选文字（与可收窄文字混在同一分组）。
		"aqp", "qpb", "qp", " qp ", "zqp", "aaaaaa", "baaa",
		strings.Repeat("z", 200),
		strings.Repeat("a", 200),
		strings.Repeat("word", 40),
		// 命中恰好落在 64/128 字节窗口起点：收窄用的是「单词集合掩码左移一位」，
		// 第 0 位要由窗口前一位字节补齐，跨窗口的进位一旦算错就会在这里漏报。
		strings.Repeat("z", 64) + "zz",
		strings.Repeat("a", 64) + "qz" + strings.Repeat("a", 100),
		strings.Repeat("a", 64) + "qwz" + strings.Repeat("a", 100),
		strings.Repeat("a", 64) + "qqwweerr" + strings.Repeat("a", 100),
		strings.Repeat("a", 64) + "a" + strings.Repeat("z", 100),
		strings.Repeat("a", 128) + "qz" + strings.Repeat("a", 100),
		// `\b` 成立需要前一位是非单词字节，改用空格把窗口前一位变成非词字节。
		strings.Repeat("a", 63) + " zz" + strings.Repeat("a", 100),
		strings.Repeat("a", 63) + " z" + strings.Repeat("a", 100),
		strings.Repeat("a", 63) + " xy" + strings.Repeat("a", 100),
		// 单字节集合扫描分支的窗口进位：命中恰好在偏移 64，前一位是空格。
		strings.Repeat("a", 63) + " v" + strings.Repeat("a", 100),
		strings.Repeat("a", 64) + "v" + strings.Repeat("a", 100),
		"zzzz zzzz\nzzzz",
		"\nzzzz\nzzzz\n",
	}, "|")
	for _, patterns := range batches {
		expressions := make([]scankit.Expression, 0, len(patterns))
		for index, pattern := range patterns {
			expressions = append(expressions, scankit.Expression{Id: uint32(index + 1), Pattern: pattern})
		}
		combined, err := scankit.Compile(expressions)
		if err != nil {
			t.Fatalf("%v: combined compile: %v", patterns, err)
		}
		combinedMatches, err := combined.Scan([]byte(corpus))
		if err != nil {
			t.Fatalf("%v: combined scan: %v", patterns, err)
		}
		for index, pattern := range patterns {
			id := uint32(index + 1)
			single, err := scankit.Compile([]scankit.Expression{{Id: id, Pattern: pattern}})
			if err != nil {
				t.Fatalf("%v %q: compile: %v", patterns, pattern, err)
			}
			want, err := single.Scan([]byte(corpus))
			if err != nil {
				t.Fatalf("%v %q: scan: %v", patterns, pattern, err)
			}
			got := make([]scankit.Match, 0, len(want))
			for _, match := range combinedMatches {
				if match.Id == id {
					got = append(got, match)
				}
			}
			if !slices.Equal(got, want) {
				t.Fatalf("%v 中 %q 在收窄候选索引下命中不一致:\n got=%v\nwant=%v", patterns, pattern, got, want)
			}
			if len(want) == 0 {
				t.Fatalf("%v %q: 语料没有命中，测试失去意义", patterns, pattern)
			}
		}
	}
}

// TestScopedExtendedFlagsMatchesUnscoped 验证 `(?x:…)` 作用域修饰符不影响匹配结果。
// 扩展模式在解析期生效，因此逐步去掉 `x` 模式下的空白与注释后，两者必须给出完全相同
// 的命中集合（与 Go 标准库 regexp 无关，后者不支持 `x`）。
func TestScopedExtendedFlagsMatchesUnscoped(t *testing.T) {
	cases := []struct {
		scoped     string
		equivalent string
	}{
		{`(?x:^a b)`, `^ab`},
		{`(?x:\ba b\b)`, `\bab\b`},
		{`(?x:(?m:^z))`, `(?m:^z)`},
	}
	inputs := []string{
		"", "ab", " ab ", "\nab", "z", "z\nz", "\nz\n", "abz", " a b ",
	}
	for _, c := range cases {
		scoped, err := scankit.Compile([]scankit.Expression{{Id: 1, Pattern: c.scoped}})
		if err != nil {
			t.Fatalf("%q: compile: %v", c.scoped, err)
		}
		plain, err := scankit.Compile([]scankit.Expression{{Id: 1, Pattern: c.equivalent}})
		if err != nil {
			t.Fatalf("%q: compile: %v", c.equivalent, err)
		}
		for _, input := range inputs {
			data := []byte(input)
			got, err := scoped.Scan(data)
			if err != nil {
				t.Fatalf("%q %q: scan: %v", c.scoped, input, err)
			}
			want, err := plain.Scan(data)
			if err != nil {
				t.Fatalf("%q %q: scan: %v", c.equivalent, input, err)
			}
			if !slices.Equal(got, want) {
				t.Fatalf("%q 与 %q 在 %q 上不一致: got=%v want=%v", c.scoped, c.equivalent, input, got, want)
			}
		}
	}
}

// TestLiteralOverlapConsistentAcrossCompilationShapes 锁定「精确文字规则的重叠命中」
// 在纯文字与混合编译下的一致语义（见问题与排查计划 §66.7 第 4 项）。
//
// 修复前精确文字规则有两条互不一致的出口：纯文字编译（Rose 直通、fastLiteral 直通、
// 候选索引直通）按「出现即命中」报出全部重叠命中，混合编译走确认程序的重叠水位线，
// 同一条规则只报不重叠命中。现在两条出口统一到参考实现（Go regexp）的不重叠语义，
// 这里用同一文字分别与「无伴随规则 / 纯文字伴随规则 / 行首通道 / 词边界 / 混合规则集」
// 一起编译，逐条与 regexp 差分。
func TestLiteralOverlapConsistentAcrossCompilationShapes(t *testing.T) {
	const pattern = `zzzzzzzz`
	companions := [][]string{
		nil,                                      // 单规则：Rose 直通
		{`needle`},                               // 多规则纯文字：fastLiteral / 候选索引直通
		{`(?m:^zzzzzzzz)`},                       // 行首通道参与，走候选确认路径
		{`\bzzzzzzzz`},                           // 词边界收窄参与
		{`q2zz`, `q3zz`, `q4zz`, `q5zz`, `q6zz`}, // 多条纯文字，扩大候选索引
		{`(?m:^word$)`, `(?m:^token)=`, `(?m:^z)`, `\bzzzzzzzz`, `needle`, `(?m:^ab|^cd)`},
	}
	inputs := []string{
		"",
		"z",
		"zzzzzzzz",
		"zzzzzzzzzzzzzzzzzzzzzzzzzzzzzz",
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" + "zzzzzzzzzzzzzzzzzzzzzzzzzzzzzz",
		"zzzzzzzz|zzzzzzzz",
		"xzzzzzzzz@z",
		"\nzzzzzzzz\n",
		"zzzzzzzz\nzzzzzzzz\nzzzzzzzz",
		strings.Repeat("z", 200),
	}
	reference := regexp.MustCompile(pattern)
	for _, extra := range companions {
		expressions := make([]scankit.Expression, 0, len(extra)+1)
		expressions = append(expressions, scankit.Expression{Id: 1, Pattern: pattern})
		for index, companion := range extra {
			expressions = append(expressions, scankit.Expression{Id: uint32(index + 2), Pattern: companion})
		}
		scanner, err := scankit.Compile(expressions)
		if err != nil {
			t.Fatalf("Compile(%v): %v", extra, err)
		}
		for _, input := range inputs {
			matches, err := scanner.Scan([]byte(input))
			if err != nil {
				t.Fatalf("Scan(%q): %v", input, err)
			}
			got := make([][2]int, 0, len(matches))
			for _, match := range matches {
				if match.Id == 1 {
					got = append(got, [2]int{int(match.From), int(match.To)})
				}
			}
			want := indexOffsets(reference.FindAllStringIndex(input, -1))
			if !sameOffsets(got, want) {
				t.Fatalf("伴随规则 %v 输入 %q：scankit=%v，regexp=%v", extra, input, got, want)
			}
		}
	}
}
