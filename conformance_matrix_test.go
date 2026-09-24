package scankit

import (
	"testing"

	"github.com/smartwalle/scankit/internal/dispatch"
	"github.com/smartwalle/scankit/internal/simd"
	armbackend "github.com/smartwalle/scankit/internal/simd/arm64"
	x86backend "github.com/smartwalle/scankit/internal/simd/x86"
)

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
func conformanceMatrixExpressions() []Expression {
	return []Expression{
		{Id: 1, Pattern: "needle"},
		{Id: 2, Pattern: "NeEdLe", Flags: FlagCaseless},
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
func sameMatches(a, b []Match) bool {
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

func scanWithBackend(t *testing.T, backend simd.Backend, expressions []Expression, corpus []byte) []Match {
	t.Helper()
	dispatch.SetBackendOverride(backend)
	defer dispatch.SetBackendOverride(nil)
	scanner, err := Compile(expressions)
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
	expr      Expression
	data      []byte
	compileOK bool
} {
	return []struct {
		name      string
		expr      Expression
		data      []byte
		compileOK bool
	}{
		{"utf8", Expression{Id: 1, Pattern: "é", Flags: FlagUTF8}, []byte("xé"), true},
		{"boundary", Expression{Id: 2, Pattern: `\bcat\b`}, []byte("cat scatter"), true},
		{"backref", Expression{Id: 3, Pattern: `(ab)\1`}, []byte("abab"), true},
		{"repeat", Expression{Id: 4, Pattern: `a{2,3}`}, []byte("aaa"), true},
		{"hamming", Expression{Id: 5, Pattern: "abcd", Ext: &ExpressionExt{Flags: ExtFlagHammingDistance, HammingDistance: 1}}, []byte("abxd"), true},
		{"nul", Expression{Id: 6, Pattern: `\x00`}, []byte{0x00, 0x01}, true},
		{"empty", Expression{Id: 7, Pattern: `a*`, Flags: FlagAllowEmpty}, []byte("ba"), true},
		{"caseless-utf8", Expression{Id: 8, Pattern: "ÄÖÜ", Flags: FlagCaseless}, []byte("äöü ÄÖÜ"), true},
		{"invalid-pattern", Expression{Id: 9, Pattern: `(`}, nil, false},
	}
}

// TestScanFixturesAcrossBackendMatrix 在全部后端上复核 Block 一致性用例与
// 错误边界，确保原生与回退路径在确认路径和编译错误上同样一致。
func TestScanFixturesAcrossBackendMatrix(t *testing.T) {
	for _, fixture := range conformanceMatrixFixtures() {
		t.Run(fixture.name, func(t *testing.T) {
			type result struct {
				matches []Match
				failed  bool
			}
			run := func(backend simd.Backend) result {
				dispatch.SetBackendOverride(backend)
				defer dispatch.SetBackendOverride(nil)
				scanner, err := Compile([]Expression{fixture.expr})
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
