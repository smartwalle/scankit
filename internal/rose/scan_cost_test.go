package rose

// scan_cost_test.go 固定 §57 的 Rose 候选路径尺寸扫描口径，供后续回归复测使用。
// 语料与程序构造在优化前后保持一致，因此可直接与「问题与排查计划」§57 的表格比对。

import (
	"fmt"
	"testing"

	"github.com/smartwalle/scankit/internal/fdr"
)

var scanCostSizes = []int{8, 16, 64, 256, 1 << 10, 4 << 10, 16 << 10, 64 << 10}

func scanCostData(n int) []byte {
	data := make([]byte, n)
	for i := range data {
		data[i] = '.'
	}
	return data
}

// scanCostEnglishData 生成确定性的英文日志型语料：自然字母分布，且不含任何
// kw_lit_/kw_single 完整子串。
func scanCostEnglishData(n int) []byte {
	const sentence = "The quick brown fox jumps over the lazy dog while packing boxes. "
	data := make([]byte, 0, n+len(sentence))
	for len(data) < n {
		data = append(data, sentence...)
	}
	return data[:n]
}

func scanCostSingleASCII() *Program {
	return New([]Role{{ID: 1, Literal: []byte("xyz")}})
}

func scanCostSingleUnicode() *Program {
	return New([]Role{{ID: 1, Literal: []byte("é"), CaseInsensitive: true}})
}

func scanCostMultiRolesList() []Role {
	roles := make([]Role, 0, 100)
	for i := 0; i < 99; i++ {
		roles = append(roles, Role{ID: uint32(i + 1), Literal: []byte(fmt.Sprintf("kw_lit_%03d", i))})
	}
	roles = append(roles, Role{ID: 100, Literal: []byte("kw_single")})
	return roles
}

func scanCostSingleCharRoles() []Role {
	const alphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	roles := make([]Role, 0, len(alphabet))
	for i := 0; i < len(alphabet); i++ {
		roles = append(roles, Role{ID: uint32(i + 1), Literal: []byte{alphabet[i]}})
	}
	return roles
}

func scanCostAlphabetData(n int) []byte {
	const alphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	data := make([]byte, n)
	for i := range data {
		data[i] = alphabet[i%len(alphabet)]
	}
	return data
}

func scanCostSpreadRoles() []Role {
	var roles []Role
	for i := 0; i < 100; i++ {
		roles = append(roles, Role{ID: uint32(i + 1), Literal: []byte{byte('a' + i%26), byte('A' + i%26), byte('0' + i%10)}})
	}
	return roles
}

// scanCostScanBench 对给定程序与语料测 FindMatchesInto，命中数用于防止基准空转。
func scanCostScanBench(b *testing.B, roles []Role, data []byte, wantMatches int) {
	scanCostScanBenchRaw(b, New(roles), data, wantMatches)
}

func scanCostScanBenchRaw(b *testing.B, program *Program, data []byte, wantMatches int) {
	b.ReportAllocs()
	b.SetBytes(int64(len(data)))
	b.ResetTimer()
	for b.Loop() {
		if got := program.FindMatchesInto(data, nil); len(got) != wantMatches {
			b.Fatalf("期望 %d 命中，实得 %d", wantMatches, len(got))
		}
	}
}

func scanCostLocateBench(b *testing.B, roles []Role, data []byte) {
	matcher := buildRoleMatcher(roles)
	if matcher == nil {
		b.Fatal("matcher 构建失败")
	}
	var buf []fdr.Match
	b.ReportAllocs()
	b.SetBytes(int64(len(data)))
	b.ResetTimer()
	for b.Loop() {
		buf = matcher.FindInto(data, buf[:0])
	}
}

func BenchmarkSingleRoleASCII(b *testing.B) {
	for _, n := range scanCostSizes {
		b.Run(fmt.Sprintf("%dB", n), func(b *testing.B) {
			scanCostScanBench(b, []Role{{ID: 1, Literal: []byte("xyz")}}, scanCostData(n), 0)
		})
	}
}

func BenchmarkSingleRoleUnicode(b *testing.B) {
	for _, n := range scanCostSizes {
		b.Run(fmt.Sprintf("%dB", n), func(b *testing.B) {
			// 非 ASCII 大小写不敏感角色无法建字节自动机，刻意走逐偏移确认路径。
			scanCostScanBenchRaw(b, scanCostSingleUnicode(), scanCostData(n), 0)
		})
	}
}

func BenchmarkMultiRoleEnglish(b *testing.B) {
	for _, n := range scanCostSizes {
		b.Run(fmt.Sprintf("%dB", n), func(b *testing.B) {
			scanCostScanBench(b, scanCostMultiRolesList(), scanCostEnglishData(n), 0)
		})
	}
}

func BenchmarkMultiRoleSparse(b *testing.B) {
	for _, n := range scanCostSizes {
		b.Run(fmt.Sprintf("%dB", n), func(b *testing.B) {
			scanCostScanBench(b, scanCostMultiRolesList(), scanCostData(n), 0)
		})
	}
}

func BenchmarkMultiRoleLocate(b *testing.B) {
	for _, n := range scanCostSizes {
		b.Run(fmt.Sprintf("%dB", n), func(b *testing.B) {
			scanCostLocateBench(b, scanCostMultiRolesList(), scanCostEnglishData(n))
		})
	}
}

func BenchmarkDenseRoles(b *testing.B) {
	for _, n := range []int{1 << 10, 8 << 10, 64 << 10} {
		b.Run(fmt.Sprintf("%dB", n), func(b *testing.B) {
			scanCostScanBench(b, scanCostSingleCharRoles(), scanCostAlphabetData(n), n)
		})
	}
}

func BenchmarkSpreadRoles(b *testing.B) {
	for _, n := range []int{1 << 10, 8 << 10, 64 << 10} {
		b.Run(fmt.Sprintf("%dB", n), func(b *testing.B) {
			program := New(scanCostSpreadRoles())
			data := scanCostEnglishData(n)
			b.ReportAllocs()
			b.SetBytes(int64(n))
			b.ResetTimer()
			for b.Loop() {
				program.FindMatchesInto(data, nil)
			}
		})
	}
}

// BenchmarkDenseReuse 用复用 dst 的口径测密集命中，模拟 scanner.go 通过
// roseStatePool 复用结果缓冲的真实调用方式；与 BenchmarkDense 的 dst=nil
// 口径一起，区分“候选定位”与“结果切片扩容”两段成本。
func BenchmarkDenseRolesReuse(b *testing.B) {
	for _, n := range []int{1 << 10, 8 << 10, 64 << 10} {
		b.Run(fmt.Sprintf("%dB", n), func(b *testing.B) {
			program := New(scanCostSingleCharRoles())
			data := scanCostAlphabetData(n)
			var dst []State
			b.ReportAllocs()
			b.SetBytes(int64(n))
			b.ResetTimer()
			for b.Loop() {
				dst = program.FindMatchesInto(data, dst[:0])
				if len(dst) != n {
					b.Fatalf("期望 %d 命中，实得 %d", n, len(dst))
				}
			}
		})
	}
}

// TestShapeReport 断言三条路径确实落在预期的程序形态上，避免基准空转。
func TestScanCostShapeReport(t *testing.T) {
	if p := scanCostSingleASCII(); p.matcher == nil {
		t.Fatal("单角色 ASCII 应构建共享自动机")
	}
	if p := scanCostSingleUnicode(); p.matcher != nil {
		t.Fatal("单角色非 ASCII 折叠无法建字节自动机，matcher 应为 nil")
	}
	if p := New(scanCostMultiRolesList()); p.matcher == nil {
		t.Fatal("多角色纯文字应构建共享自动机")
	}
	if p := New(scanCostSpreadRoles()); p.matcher == nil {
		t.Fatal("多角色分散首字节应构建共享自动机")
	}
}
