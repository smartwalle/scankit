// scan_test：扫描与替换契约
//
// 覆盖跨后端一致性矩阵、预过滤等价性、扫描偏移/长度约束与替换边界。
// 跨后端矩阵用 internal/dispatch 的 SetBackendOverride + internal/simd 各层级实现
// 强制指定后端（公开 API 无此开关），其余用例只调用 scankit 公开 API。
// 由原根目录的 conformance_matrix_test.go、prefilter_test.go、release_gate_test.go、
// replace_boundary_test.go 合并而成。

package tests

import (
	"bytes"
	"fmt"
	"strconv"
	"testing"

	"github.com/smartwalle/scankit"
	"github.com/smartwalle/scankit/internal/dispatch"
	"github.com/smartwalle/scankit/internal/simd"
	armbackend "github.com/smartwalle/scankit/internal/simd/arm64"
	x86backend "github.com/smartwalle/scankit/internal/simd/x86"
)

// ---- 以下用例来自原根目录的 conformance_matrix_test.go ----

// backendMatrixEntries 返回可用于一致性矩阵的后端集合：
// 通用实现作为参照，x86 与 ARM64 的每一级能力层级都显式列出；
// 当前主机不具备的能力会在后端内部安全裁剪，因此同一份用例
// 同时覆盖原生路径与回退路径。
func backendMatrixEntries() []struct {
	name    string
	backend simd.Backend
} {
	return []struct {
		name    string
		backend simd.Backend
	}{
		{"generic", simd.Default()},
		{"x86/scalar", x86backend.NewWithTier(x86backend.TierScalar)},
		{"x86/sse", x86backend.NewWithTier(x86backend.TierSSE)},
		{"x86/sse4", x86backend.NewWithTier(x86backend.TierSSE4)},
		{"x86/avx2", x86backend.NewWithTier(x86backend.TierAVX2)},
		{"x86/avx512", x86backend.NewWithTier(x86backend.TierAVX512)},
		{"x86/avx512vbmi", x86backend.NewWithTier(x86backend.TierAVX512VBMI)},
		{"arm64/scalar", armbackend.NewWithTier(armbackend.TierScalar)},
		{"arm64/neon", armbackend.NewWithTier(armbackend.TierNEON)},
		{"arm64/sve", armbackend.NewWithTier(armbackend.TierSVE)},
		{"arm64/sve2", armbackend.NewWithTier(armbackend.TierSVE2)},
	}
}

// conformanceMatrixExpressions 覆盖文字、范围、重叠、锚定、重复、
// 长文字、UTF-8 与 NFA 专用路径，保证矩阵不只是重复同一条候选路径。
func conformanceMatrixExpressions() []scankit.Expression {
	return []scankit.Expression{
		{Id: 1, Pattern: "needle"},
		{Id: 2, Pattern: "NeEdLe", Flags: scankit.CompileCaseless},
		{Id: 3, Pattern: `ab|cd`},
		{Id: 4, Pattern: `[0-9]{3}-[0-9]{4}`},
		{Id: 5, Pattern: `[a-z]+@[a-z]+\.example`},
		{Id: 6, Pattern: `^token`},
		{Id: 7, Pattern: `done$`},
		{Id: 8, Pattern: `x{2,4}`},
		{Id: 9, Pattern: `0123456789abcdefghijklmnop`},
		{Id: 10, Pattern: `ÜNÏCODE`},
		{Id: 11, Pattern: `\bword\b`},
		{Id: 12, Pattern: `[[:alpha:]]{5,}`},
	}
}

// conformanceMatrixCorpus 返回固定语料：包含全部命中、尾部截断、
// 非对齐起点和超宽窗口边界，保证原生与回退路径在同一输入上比较。
func conformanceMatrixCorpus() []byte {
	corpus := "token needle NeEdLe ab cd 123-4567 user@host.example xx xxxx " +
		"0123456789abcdefghijklmnop ÜNÏCODE word alphabet "
	// 追加长度跨越多个向量窗口的长前缀，触发窗口与尾部混合路径。
	for range 5 {
		corpus += "0123456789abcdef"
	}
	corpus += "done"
	return []byte(corpus)
}

// sameMatches 比较两组命中是否完全一致。
func sameMatches(a, b []scankit.Match) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func scanWithBackend(t *testing.T, backend simd.Backend, expressions []scankit.Expression, corpus []byte) []scankit.Match {
	t.Helper()
	dispatch.SetBackendOverride(backend)
	defer dispatch.SetBackendOverride(nil)
	scanner, err := scankit.Compile(expressions)
	if err != nil {
		t.Fatalf("编译失败: %v", err)
	}
	matches, err := scanner.Scan(corpus)
	if err != nil {
		t.Fatalf("扫描失败: %v", err)
	}
	return matches
}

// TestScanConformanceAcrossBackendMatrix 验证同一份规则与语料在
// 通用后端、每一级 x86 能力层级和每一级 ARM64 能力层级上产生完全一致的结果。
func TestScanConformanceAcrossBackendMatrix(t *testing.T) {
	expressions := conformanceMatrixExpressions()
	corpus := conformanceMatrixCorpus()
	want := scanWithBackend(t, simd.Default(), expressions, corpus)
	if len(want) == 0 {
		t.Fatal("固定语料未产生任何命中")
	}
	for _, entry := range backendMatrixEntries() {
		t.Run(entry.name, func(t *testing.T) {
			got := scanWithBackend(t, entry.backend, expressions, corpus)
			if len(got) != len(want) {
				t.Fatalf("命中数量不一致: got=%d want=%d\ngot=%v\nwant=%v", len(got), len(want), got, want)
			}
			for i := range got {
				if got[i] != want[i] {
					t.Fatalf("第 %d 个命中不一致: got=%v want=%v", i, got[i], want[i])
				}
			}
		})
	}
}

// TestScanConformanceAcrossBackendMatrixWindows 验证语料在逐个偏移截断后
// 仍保持跨后端一致，覆盖向量窗口尾部、非对齐和短输入回退。
func TestScanConformanceAcrossBackendMatrixWindows(t *testing.T) {
	expressions := conformanceMatrixExpressions()
	corpus := conformanceMatrixCorpus()
	backends := backendMatrixEntries()
	for cut := 1; cut < len(corpus); cut += 7 {
		window := corpus[:cut]
		want := scanWithBackend(t, simd.Default(), expressions, window)
		for _, entry := range backends {
			got := scanWithBackend(t, entry.backend, expressions, window)
			if !sameMatches(got, want) {
				t.Fatalf("截断长度 %d 后端 %s 结果不一致: got=%v want=%v", cut, entry.name, got, want)
			}
		}
	}
}

// conformanceMatrixFixtures 复现 Block 一致性用例（UTF-8、边界、反向引用、
// 重复、模糊匹配）与错误边界，保证矩阵同时覆盖候选路径和确认路径。
func conformanceMatrixFixtures() []struct {
	name      string
	expr      scankit.Expression
	data      []byte
	compileOK bool
} {
	return []struct {
		name      string
		expr      scankit.Expression
		data      []byte
		compileOK bool
	}{
		{"utf8", scankit.Expression{Id: 1, Pattern: "é", Flags: scankit.CompileUTF8}, []byte("xé"), true},
		{"boundary", scankit.Expression{Id: 2, Pattern: `\bcat\b`}, []byte("cat scatter"), true},
		{"backref", scankit.Expression{Id: 3, Pattern: `(ab)\1`}, []byte("abab"), true},
		{"repeat", scankit.Expression{Id: 4, Pattern: `a{2,3}`}, []byte("aaa"), true},
		{"hamming", scankit.Expression{Id: 5, Pattern: "abcd", Ext: &scankit.ExpressionExt{Flags: scankit.ExtFlagHammingDistance, HammingDistance: 1}}, []byte("abxd"), true},
		{"nul", scankit.Expression{Id: 6, Pattern: `\x00`}, []byte{0x00, 0x01}, true},
		{"empty", scankit.Expression{Id: 7, Pattern: `a*`, Flags: scankit.CompileAllowEmpty}, []byte("ba"), true},
		{"caseless-utf8", scankit.Expression{Id: 8, Pattern: "ÄÖÜ", Flags: scankit.CompileCaseless}, []byte("äöü ÄÖÜ"), true},
		{"invalid-pattern", scankit.Expression{Id: 9, Pattern: `(`}, nil, false},
	}
}

// TestScanFixturesAcrossBackendMatrix 在全部后端上复核 Block 一致性用例与
// 错误边界，确保原生与回退路径在确认路径和编译错误上同样一致。
func TestScanFixturesAcrossBackendMatrix(t *testing.T) {
	for _, fixture := range conformanceMatrixFixtures() {
		t.Run(fixture.name, func(t *testing.T) {
			type result struct {
				matches []scankit.Match
				failed  bool
			}
			run := func(backend simd.Backend) result {
				dispatch.SetBackendOverride(backend)
				defer dispatch.SetBackendOverride(nil)
				scanner, err := scankit.Compile([]scankit.Expression{fixture.expr})
				if err != nil {
					return result{failed: true}
				}
				if !fixture.compileOK {
					return result{}
				}
				matches, err := scanner.Scan(fixture.data)
				if err != nil {
					return result{failed: true}
				}
				return result{matches: matches}
			}
			want := run(simd.Default())
			if want.failed != !fixture.compileOK {
				t.Fatalf("参照结果与用例预期不一致: failed=%v compileOK=%v", want.failed, fixture.compileOK)
			}
			for _, entry := range backendMatrixEntries() {
				got := run(entry.backend)
				if got.failed != want.failed || !sameMatches(got.matches, want.matches) {
					t.Fatalf("后端 %s 结果不一致: got=%v failed=%v want=%v failed=%v",
						entry.name, got.matches, got.failed, want.matches, want.failed)
				}
			}
		})
	}
}

// ---- 以下用例来自原根目录的 prefilter_test.go ----

func TestPrefilterPreservesResults(t *testing.T) {
	expr := scankit.Expression{Id: 1, Pattern: `foo[0-9]+bar`}
	a, e := scankit.Compile([]scankit.Expression{expr})
	if e != nil {
		t.Fatal(e)
	}
	expr.Flags = scankit.CompilePrefilter
	b, e := scankit.Compile([]scankit.Expression{expr})
	if e != nil {
		t.Fatal(e)
	}
	data := []byte("foo12bar xfoo9bar yfooXbar")
	ma, _ := a.Scan(data)
	mb, _ := b.Scan(data)
	if len(ma) != len(mb) {
		t.Fatalf("%#v %#v", ma, mb)
	}
	for i := range ma {
		if ma[i] != mb[i] {
			t.Fatalf("%#v %#v", ma, mb)
		}
	}
}

func TestPrefilterPreservesCaselessResults(t *testing.T) {
	expr := scankit.Expression{Id: 1, Pattern: `foo[0-9]+bar`, Flags: scankit.CompileCaseless}
	without, err := scankit.Compile([]scankit.Expression{expr})
	if err != nil {
		t.Fatal(err)
	}
	expr.Flags |= scankit.CompilePrefilter
	with, err := scankit.Compile([]scankit.Expression{expr})
	if err != nil {
		t.Fatal(err)
	}
	data := []byte("FOO12BAR fOo9BaR fooXbar")
	want, err := without.Scan(data)
	if err != nil {
		t.Fatal(err)
	}
	got, err := with.Scan(data)
	if err != nil || len(got) != len(want) {
		t.Fatalf("prefilter changed caseless result: %v want=%#v got=%#v", err, want, got)
	}
	for index := range want {
		if want[index] != got[index] {
			t.Fatalf("prefilter changed caseless match: want=%#v got=%#v", want, got)
		}
	}
}

func TestPrefilterDoesNotRejectUnicodeCaselessLiteral(t *testing.T) {
	s, err := scankit.Compile([]scankit.Expression{{Id: 9, Pattern: "ÄBC", Flags: scankit.CompileUTF8 | scankit.CompileCaseless | scankit.CompilePrefilter}})
	if err != nil {
		t.Fatal(err)
	}
	matches, err := s.Scan([]byte("äbc"))
	if err != nil || len(matches) != 1 || matches[0].Id != 9 {
		t.Fatalf("Unicode 大小写候选过滤错误: %v %#v", err, matches)
	}
}

func TestPrefilterBranchCandidateStillRunsFullConfirmation(t *testing.T) {
	with, err := scankit.Compile([]scankit.Expression{{Id: 1, Pattern: `(?:foo|foobar)baz`, Flags: scankit.CompilePrefilter}})
	if err != nil {
		t.Fatal(err)
	}
	without, err := scankit.Compile([]scankit.Expression{{Id: 1, Pattern: `(?:foo|foobar)baz`}})
	if err != nil {
		t.Fatal(err)
	}
	data := []byte("foobaz fooqux foobarbaz foobazx")
	want, err := without.Scan(data)
	if err != nil {
		t.Fatal(err)
	}
	got, err := with.Scan(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(want) {
		t.Fatalf("候选过滤改变命中数量: want=%#v got=%#v", want, got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("候选过滤改变确认结果: want=%#v got=%#v", want, got)
		}
	}
}

func TestSinglePrefilterUsesAllCandidateStarts(t *testing.T) {
	scanner, err := scankit.Compile([]scankit.Expression{{Id: 91, Pattern: `foo[0-9]`, Flags: scankit.CompilePrefilter}})
	if err != nil {
		t.Fatal(err)
	}
	got, err := scanner.Scan([]byte("foo1 xfoo2 foo3"))
	if err != nil {
		t.Fatal(err)
	}
	want := []scankit.Match{{Id: 91, From: 0, To: 4}, {Id: 91, From: 6, To: 10}, {Id: 91, From: 11, To: 15}}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("候选起点结果=%v, want=%v", got, want)
	}
}

// ---- 以下用例来自原根目录的 release_gate_test.go ----

func TestSingleMatchAndInputImmutability(t *testing.T) {
	s, err := scankit.Compile([]scankit.Expression{{Id: 1, Pattern: "a+", Flags: scankit.CompileSingleMatch}})
	if err != nil {
		t.Fatal(err)
	}
	data := []byte("aa baaa")
	original := append([]byte(nil), data...)
	got, err := s.Scan(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].From != 0 || got[0].To != 2 {
		t.Fatalf("matches=%#v", got)
	}
	if !bytes.Equal(data, original) {
		t.Fatal("scanner modified input")
	}
}

func TestOffsetAndLengthConstraints(t *testing.T) {
	s, err := scankit.Compile([]scankit.Expression{{Id: 1, Pattern: "abc", Ext: &scankit.ExpressionExt{Flags: scankit.ExtFlagMinOffset | scankit.ExtFlagMaxOffset | scankit.ExtFlagMinLength, MinOffset: 2, MaxOffset: 6, MinLength: 3}}})
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Scan([]byte("xxabc abc"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].From != 2 || got[0].To != 5 {
		t.Fatalf("matches=%#v", got)
	}
}

func TestOffsetConstraintsUseMatchEnd(t *testing.T) {
	s, err := scankit.Compile([]scankit.Expression{{Id: 1, Pattern: "abc", Ext: &scankit.ExpressionExt{Flags: scankit.ExtFlagMinOffset | scankit.ExtFlagMaxOffset, MinOffset: 3, MaxOffset: 3}}})
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Scan([]byte("abc"))
	if err != nil || len(got) != 1 || got[0] != (scankit.Match{Id: 1, From: 0, To: 3}) {
		t.Fatalf("end-offset constraints mismatch: %v %#v", err, got)
	}
}

func BenchmarkScanRuleScales(b *testing.B) {
	data := bytes.Repeat([]byte("prefix abc suffix "), 64)
	for _, count := range []int{1, 10, 100, 1000} {
		b.Run("rules_"+strconv.Itoa(count), func(b *testing.B) {
			expressions := make([]scankit.Expression, count)
			for i := range expressions {
				expressions[i] = scankit.Expression{Id: uint32(i + 1), Pattern: "abc"}
			}
			s, err := scankit.Compile(expressions)
			if err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_, err = s.Scan(data)
				if err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// ---- 以下用例来自原根目录的 replace_boundary_test.go ----

func TestReplaceIgnoresInvalidOffsets(t *testing.T) {
	data := []byte("abc")
	got := scankit.Replace(data, []scankit.Match{{From: 9, To: 10}, {From: 2, To: 1}}, func(b *bytes.Buffer, _ scankit.Match, _ []byte) { b.WriteString("x") })
	if string(got) != "abc" {
		t.Fatal(string(got))
	}
}
