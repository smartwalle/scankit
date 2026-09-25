// compile_test：编译契约、错误与限制
//
// 覆盖 Compile/New 的入参校验、错误哨兵、扩展限制、内联 flag 语义与工具链约束。
// 由原根目录的 compile_test.go、compile_validation_test.go、contract_test.go、
// toolchain_guard_test.go、clone_api_test.go 合并而成。

package tests

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/smartwalle/scankit"
)

// ---- 以下用例来自原根目录的 compile_test.go ----

func TestCompileRejectsDuplicateIDs(t *testing.T) {
	_, err := scankit.Compile([]scankit.Expression{{Id: 1, Pattern: "a"}, {Id: 1, Pattern: "b"}})
	if !errors.Is(err, scankit.ErrDuplicateExpression) {
		t.Fatalf("%v", err)
	}
}
func TestCompileFuzzyLiteral(t *testing.T) {
	s, err := scankit.Compile([]scankit.Expression{{Id: 1, Pattern: "hello", Ext: &scankit.ExpressionExt{Flags: scankit.ExtFlagEditDistance, EditDistance: 1}}})
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Scan([]byte("hallo"))
	if err != nil || len(got) != 1 {
		t.Fatalf("%v %#v", err, got)
	}
}

func TestCompileFuzzyCaselessLiteral(t *testing.T) {
	cases := []scankit.Expression{
		{Id: 1, Pattern: "Hello", Flags: scankit.CompileCaseless, Ext: &scankit.ExpressionExt{Flags: scankit.ExtFlagEditDistance, EditDistance: 1}},
		{Id: 2, Pattern: "Hello", Flags: scankit.CompileCaseless, Ext: &scankit.ExpressionExt{Flags: scankit.ExtFlagHammingDistance, HammingDistance: 1}},
	}
	s, err := scankit.Compile(cases)
	if err != nil {
		t.Fatal(err)
	}
	matches, err := s.Scan([]byte("hELLo hallo"))
	if err != nil || len(matches) != 4 {
		t.Fatalf("fuzzy caseless mismatch: %v %#v", err, matches)
	}
}

func TestLeadingInlineFlagsCanDisableExpressionFlag(t *testing.T) {
	s, err := scankit.Compile([]scankit.Expression{{Id: 1, Pattern: `(?-i)abc`, Flags: scankit.CompileCaseless}})
	if err != nil {
		t.Fatal(err)
	}
	matches, err := s.Scan([]byte("ABC abc"))
	if err != nil || len(matches) != 1 || matches[0] != (scankit.Match{Id: 1, From: 4, To: 7}) {
		t.Fatalf("inline disable mismatch: %v %#v", err, matches)
	}
}

func TestMultipleLiteralRulesShareCandidateIndexWithoutLosingIDs(t *testing.T) {
	s, err := scankit.Compile([]scankit.Expression{{Id: 1, Pattern: "same"}, {Id: 2, Pattern: "same"}, {Id: 3, Pattern: "other"}})
	if err != nil {
		t.Fatal(err)
	}
	matches, err := s.Scan([]byte("same other"))
	if err != nil || len(matches) != 3 || matches[0].Id != 1 || matches[1].Id != 2 || matches[2].Id != 3 {
		t.Fatalf("shared literal candidate mismatch: %v %#v", err, matches)
	}
}

func TestPOSIXCharacterClassesExecute(t *testing.T) {
	s, err := scankit.Compile([]scankit.Expression{{Id: 1, Pattern: `[[:digit:]]+`}, {Id: 2, Pattern: `[[:^digit:]]+`}})
	if err != nil {
		t.Fatal(err)
	}
	matches, err := s.Scan([]byte("a12"))
	if err != nil || len(matches) != 2 || matches[0] != (scankit.Match{Id: 2, From: 0, To: 1}) || matches[1] != (scankit.Match{Id: 1, From: 1, To: 3}) {
		t.Fatalf("POSIX class mismatch: %#v, %v", matches, err)
	}
}

func TestAnyByteAndNewlineEscapesExecute(t *testing.T) {
	s, err := scankit.Compile([]scankit.Expression{{Id: 1, Pattern: `\C`}, {Id: 2, Pattern: `\R`}})
	if err != nil {
		t.Fatal(err)
	}
	matches, err := s.Scan([]byte("\r\n"))
	if err != nil || len(matches) != 3 {
		t.Fatalf("escape execution mismatch: %#v, %v", matches, err)
	}
	found := false
	for _, match := range matches {
		if match == (scankit.Match{Id: 2, From: 0, To: 2}) {
			found = true
		}
	}
	if !found {
		t.Fatalf("newline escape result missing: %#v", matches)
	}
}

func TestNonNewlineEscapeExecutes(t *testing.T) {
	s, err := scankit.Compile([]scankit.Expression{{Id: 1, Pattern: `\N+`}})
	if err != nil {
		t.Fatal(err)
	}
	matches, err := s.Scan([]byte("ab\ncd"))
	if err != nil || len(matches) != 2 || matches[0] != (scankit.Match{Id: 1, From: 0, To: 2}) || matches[1] != (scankit.Match{Id: 1, From: 3, To: 5}) {
		t.Fatalf("non-newline escape mismatch: %#v, %v", matches, err)
	}
}

func TestBracedOctalEscapeExecutes(t *testing.T) {
	s, err := scankit.Compile([]scankit.Expression{{Id: 1, Pattern: `\o{101}\a`}})
	if err != nil {
		t.Fatal(err)
	}
	matches, err := s.Scan([]byte("A\a"))
	if err != nil || len(matches) != 1 || matches[0] != (scankit.Match{Id: 1, From: 0, To: 2}) {
		t.Fatalf("octal escape mismatch: %v %#v", err, matches)
	}
}

func TestFixedLiteralScanUsesNFAConfirmationWithoutSemanticDrift(t *testing.T) {
	s, err := scankit.Compile([]scankit.Expression{{Id: 1, Pattern: `ab`}})
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Scan([]byte("zab ab"))
	if err != nil || len(got) != 2 || got[0] != (scankit.Match{Id: 1, From: 1, To: 3}) || got[1] != (scankit.Match{Id: 1, From: 4, To: 6}) {
		t.Fatalf("扫描结果=%v err=%v", got, err)
	}
}

func TestClassPatternUsesSpecializedNFAConfirmation(t *testing.T) {
	s, err := scankit.Compile([]scankit.Expression{{Id: 1, Pattern: `a[0-9]`}})
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Scan([]byte("a1 xa9 ax"))
	if err != nil || len(got) != 2 || got[0] != (scankit.Match{Id: 1, From: 0, To: 2}) || got[1] != (scankit.Match{Id: 1, From: 4, To: 6}) {
		t.Fatalf("类别扫描=%v err=%v", got, err)
	}
}

func TestGlobalInlineFlagsApplyToScanner(t *testing.T) {
	scanner, err := scankit.Compile([]scankit.Expression{{Id: 1, Pattern: "(?i)abc"}, {Id: 2, Pattern: "(?m)^a$"}})
	if err != nil {
		t.Fatal(err)
	}
	matches, err := scanner.Scan([]byte("ABC\na\nb"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 2 || matches[0].Id != 1 || matches[1].Id != 2 {
		t.Fatalf("全局修饰符匹配=%#v", matches)
	}
}

func TestExtendedInlineFlags(t *testing.T) {
	scanner, err := scankit.Compile([]scankit.Expression{
		{Id: 1, Pattern: "(?x)a # first\n b [c]"},
		{Id: 2, Pattern: `(?x:a # first
 b)(?-x: )c`},
	})
	if err != nil {
		t.Fatal(err)
	}
	matches, err := scanner.Scan([]byte("abc ab c"))
	if err != nil || len(matches) != 2 || matches[0] != (scankit.Match{Id: 1, From: 0, To: 3}) || matches[1] != (scankit.Match{Id: 2, From: 4, To: 8}) {
		t.Fatalf("extended matches: %v %#v", err, matches)
	}
}

// TestCompileErrorsExposeSentinels 验证 [Compile] 在各种失败情况下都能通过 errors.Is
// 命中对应的公开哨兵错误，保证调用方可以稳定做错误分类断言。
func TestCompileErrorsExposeSentinels(t *testing.T) {
	cases := []struct {
		name string
		expr []scankit.Expression
		want error
	}{
		{
			name: "empty",
			expr: nil,
			want: scankit.ErrEmptyExpressions,
		},
		{
			name: "duplicate id",
			expr: []scankit.Expression{
				{Id: 1, Pattern: "abc"},
				{Id: 1, Pattern: "def"},
			},
			want: scankit.ErrDuplicateExpression,
		},
		{
			name: "unknown flag bit",
			expr: []scankit.Expression{
				{Id: 1, Pattern: "abc", Flags: scankit.CompileFlag(1 << 20)},
			},
			want: scankit.ErrUnsupportedFlag,
		},
		{
			name: "unknown extension flag",
			expr: []scankit.Expression{
				{Id: 1, Pattern: "abc", Ext: &scankit.ExpressionExt{Flags: scankit.ExpressionExtFlag(1 << 20)}},
			},
			want: scankit.ErrInvalidExtension,
		},
		{
			name: "invalid UTF-8 pattern",
			expr: []scankit.Expression{
				{Id: 1, Pattern: "abc\xff", Flags: scankit.CompileUTF8},
			},
			want: scankit.ErrInvalidUTF8,
		},
		{
			name: "combination referencing self",
			expr: []scankit.Expression{
				{Id: 1, Pattern: "1", Flags: scankit.CompileCombination},
			},
			want: scankit.ErrInvalidCombination,
		},
		{
			name: "singlematch with som-leftmost",
			expr: []scankit.Expression{
				{Id: 1, Pattern: "abc", Flags: scankit.CompileSingleMatch | scankit.CompileSOMLeftmost},
			},
			want: scankit.ErrUnsupportedFlag,
		},
		{
			name: "edit and hamming distance are mutually exclusive",
			expr: []scankit.Expression{
				{Id: 1, Pattern: "abc", Ext: &scankit.ExpressionExt{Flags: scankit.ExtFlagEditDistance | scankit.ExtFlagHammingDistance, EditDistance: 1, HammingDistance: 1}},
			},
			want: scankit.ErrInvalidExtension,
		},
		{
			name: "minimum length exceeds maximum offset",
			expr: []scankit.Expression{
				{Id: 1, Pattern: "abc", Ext: &scankit.ExpressionExt{Flags: scankit.ExtFlagMinLength | scankit.ExtFlagMaxOffset, MinLength: 100, MaxOffset: 10}},
			},
			want: scankit.ErrInvalidExtension,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := scankit.Compile(c.expr)
			if err == nil {
				t.Fatalf("期望错误，得到 nil")
			}
			if !errors.Is(err, c.want) {
				t.Fatalf("errors.Is(err, %T) 未命中: err=%v", c.want, err)
			}
		})
	}
}

// ---- 以下用例来自原根目录的 compile_validation_test.go ----

func TestCompileRejectsInvalidFlagsAndOffsets(t *testing.T) {
	if _, err := scankit.Compile([]scankit.Expression{{Id: 1, Pattern: "x", Flags: scankit.CompileFlag(1 << 31)}}); err == nil {
		t.Fatal("unknown compile flag accepted")
	}
	if _, err := scankit.Compile([]scankit.Expression{{Id: 1, Pattern: "x", Flags: scankit.CompileUCP}}); err != nil {
		t.Fatalf("UCP without UTF8 rejected: %v", err)
	}
	if _, err := scankit.Compile([]scankit.Expression{{Id: 1, Pattern: "x", Ext: &scankit.ExpressionExt{Flags: scankit.ExtFlagMinOffset | scankit.ExtFlagMaxOffset, MinOffset: 8, MaxOffset: 2}}}); err == nil {
		t.Fatal("reversed offsets accepted")
	}
	if _, err := scankit.Compile([]scankit.Expression{{Id: 1, Pattern: ""}}); err == nil {
		t.Fatal("empty match accepted")
	}
	if _, err := scankit.Compile([]scankit.Expression{{Id: 1, Pattern: "", Flags: scankit.CompileAllowEmpty}}); err != nil {
		t.Fatalf("allow empty rejected: %v", err)
	}
}

func TestCompileRejectsCombinationDependencyCycle(t *testing.T) {
	_, err := scankit.Compile([]scankit.Expression{
		{Id: 1, Pattern: "2", Flags: scankit.CompileCombination},
		{Id: 2, Pattern: "3", Flags: scankit.CompileCombination},
		{Id: 3, Pattern: "1", Flags: scankit.CompileCombination},
	})
	if err == nil {
		t.Fatal("combination dependency cycle accepted")
	}
}

func TestCompileRejectsIncompatibleFlags(t *testing.T) {
	cases := []scankit.CompileFlag{scankit.CompileSingleMatch | scankit.CompileSOMLeftmost, scankit.CompileQuiet | scankit.CompileSOMLeftmost, scankit.CompilePrefilter | scankit.CompileSOMLeftmost, scankit.CompileCombination | scankit.CompileCaseless}
	for _, flags := range cases {
		if _, err := scankit.Compile([]scankit.Expression{{Id: 1, Pattern: "a", Flags: flags}}); err == nil {
			t.Fatalf("incompatible flags accepted: %s", flags)
		}
	}
}

func TestCompileRejectsUnsupportedCombinationOperandsAndExtensions(t *testing.T) {
	cases := [][]scankit.Expression{
		{{Id: 1, Pattern: "a", Flags: scankit.CompilePrefilter}, {Id: 2, Pattern: "1", Flags: scankit.CompileCombination}},
		{{Id: 1, Pattern: "a", Flags: scankit.CompileSOMLeftmost}, {Id: 2, Pattern: "1", Flags: scankit.CompileCombination}},
		{{Id: 1, Pattern: "a"}, {Id: 2, Pattern: "1", Flags: scankit.CompileCombination}, {Id: 3, Pattern: "2", Flags: scankit.CompileCombination}},
		{{Id: 1, Pattern: "1", Flags: scankit.CompileCombination, Ext: &scankit.ExpressionExt{Flags: scankit.ExtFlagMinLength, MinLength: 1}}},
	}
	for _, expressions := range cases {
		if _, err := scankit.Compile(expressions); err == nil {
			t.Fatalf("unsupported combination accepted: %#v", expressions)
		}
	}
}

func TestCompileRejectsMinimumLengthAboveMaximumOffset(t *testing.T) {
	_, err := scankit.Compile([]scankit.Expression{{Id: 1, Pattern: "a", Ext: &scankit.ExpressionExt{Flags: scankit.ExtFlagMinLength | scankit.ExtFlagMaxOffset, MinLength: 2, MaxOffset: 1}}})
	if err == nil {
		t.Fatal("invalid extension bounds accepted")
	}
}

func TestCompileAcceptsPatternLengthWithinSourceSemantics(t *testing.T) {
	pattern := make([]byte, 16001)
	for i := range pattern {
		pattern[i] = 'a'
	}
	if _, err := scankit.Compile([]scankit.Expression{{Id: 1, Pattern: string(pattern)}}); err != nil {
		t.Fatalf("不应额外限制表达式长度: %v", err)
	}
}

// ---- 以下用例来自原根目录的 contract_test.go ----

func TestCompileFlagsValues(t *testing.T) {
	tests := []struct {
		name  string
		value scankit.CompileFlag
		want  scankit.CompileFlag
	}{
		{"caseless", scankit.CompileCaseless, 1},
		{"dotall", scankit.CompileDotAll, 2},
		{"multiline", scankit.CompileMultiline, 4},
		{"singlematch", scankit.CompileSingleMatch, 8},
		{"allowempty", scankit.CompileAllowEmpty, 16},
		{"utf8", scankit.CompileUTF8, 32},
		{"ucp", scankit.CompileUCP, 64},
		{"prefilter", scankit.CompilePrefilter, 128},
		{"som_leftmost", scankit.CompileSOMLeftmost, 256},
		{"combination", scankit.CompileCombination, 512},
		{"quiet", scankit.CompileQuiet, 1024},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if test.value != test.want {
				t.Fatalf("%s = %d, want %d", test.name, test.value, test.want)
			}
		})
	}
}

func TestExpressionExtFlagsValues(t *testing.T) {
	if scankit.ExtFlagMinOffset != 1 || scankit.ExtFlagMaxOffset != 2 || scankit.ExtFlagMinLength != 4 || scankit.ExtFlagEditDistance !=
		8 || scankit.ExtFlagHammingDistance != 16 {
		t.Fatalf("unexpected ExpressionExtFlag values")
	}
}

func TestScanIntoPreservesExistingPrefix(t *testing.T) {
	s, err := scankit.Compile([]scankit.Expression{{Id: 2, Pattern: "b"}, {Id: 1, Pattern: "a"}})
	if err != nil {
		t.Fatal(err)
	}
	prefix := []scankit.Match{{Id: 9, From: 99, To: 100}}
	matches, err := s.ScanInto([]byte("ab"), prefix)
	if err != nil || len(matches) != 3 || matches[0] != prefix[0] || matches[1].Id != 1 || matches[2].Id != 2 {
		t.Fatalf("scan into mismatch: %v %#v", err, matches)
	}
}

// ---- 以下用例来自原根目录的 toolchain_guard_test.go ----

// repoRoot 返回模块根目录：本包位于 <root>/tests，因此用本文件路径回退一级。
func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("无法定位测试源文件路径")
	}
	return filepath.Dir(filepath.Dir(file))
}

// arm64Go127Mnemonics 是 Go 1.27 才加入汇编器、1.26 缺失的助记符。
var arm64Go127Mnemonics = []string{"VCMHS", "VUSHL"}

func moduleGoDirective(t *testing.T, path string) (major, minor int) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取 %s 失败: %v", path, err)
	}
	for line := range strings.SplitSeq(string(data), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "go ") {
			continue
		}
		value := strings.TrimSpace(strings.TrimPrefix(line, "go "))
		parts := strings.SplitN(value, ".", 2)
		if len(parts) != 2 {
			t.Fatalf("go 指令 %q 不是 major.minor 形式", value)
		}
		major, err = strconv.Atoi(parts[0])
		if err != nil {
			t.Fatalf("go 指令主版本 %q 解析失败: %v", value, err)
		}
		minor, err = strconv.Atoi(parts[1])
		if err != nil {
			t.Fatalf("go 指令次版本 %q 解析失败: %v", value, err)
		}
		return major, minor
	}
	t.Fatalf("%s 缺少 go 指令", path)
	return 0, 0
}

// TestArm64KernelsRequireGo127Toolchain 守住 arm64 SIMD 内核与最低工具链版本的一致性：
// 内核使用 Go 1.27 才支持的 NEON 助记符，因此 go.mod 的 go 指令不得低于 1.27，
// 否则低版本工具链会在汇编阶段报错，而不是给出可定位的版本错误。
func TestArm64KernelsRequireGo127Toolchain(t *testing.T) {
	root := repoRoot(t)
	major, minor := moduleGoDirective(t, filepath.Join(root, "go.mod"))
	if major < 1 || (major == 1 && minor < 27) {
		t.Fatalf("go.mod 声明 go %d.%d，但 arm64 内核使用 Go 1.27 才支持的助记符 %v，需要 go >= 1.27",
			major, minor, arm64Go127Mnemonics)
	}

	files, err := filepath.Glob(filepath.Join(root, "internal", "simd", "arm64", "*.s"))
	if err != nil {
		t.Fatalf("枚举 arm64 汇编文件失败: %v", err)
	}
	if len(files) == 0 {
		t.Fatal("未找到 internal/simd/arm64 下的汇编文件，测试断言已失效")
	}

	// 反向确认：确实存在依赖 1.27 助记符的内核，保证上面的版本下界不被误删。
	used := make(map[string][]string)
	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("读取 %s 失败: %v", file, err)
		}
		body := string(data)
		for _, mnemonic := range arm64Go127Mnemonics {
			if strings.Contains(body, "\t"+mnemonic+" ") || strings.Contains(body, "\t"+mnemonic+"\t") {
				used[mnemonic] = append(used[mnemonic], filepath.Base(file))
			}
		}
	}
	if len(used) == 0 {
		t.Fatalf("未在任何 arm64 汇编文件中找到 %v，请同步更新最低工具链断言", arm64Go127Mnemonics)
	}
	t.Logf("arm64 内核使用 Go 1.27 助记符: %v", used)
}

// ---- 以下用例来自原根目录的 clone_api_test.go ----

func TestNilEngineAPIsAreSafe(t *testing.T) {
	var e *scankit.Engine
	if got, err := e.Scan(nil); got != nil || err != nil {
		t.Fatal("空 Engine 接口应安全返回")
	}
}
