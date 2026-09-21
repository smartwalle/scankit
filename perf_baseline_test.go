package scankit

import (
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/smartwalle/scankit/internal/dispatch"
	"github.com/smartwalle/scankit/internal/simd"
)

// fixedCorpusScanMetrics 记录固定语料上的确定性指标。
// 时间指标只用于人工记录，不参与断言，避免把机器性能当成回归门禁。
type fixedCorpusScanMetrics struct {
	// Matches 是每条规则的命中数量。
	Matches map[uint32]int
	// Backends 是每条规则的执行后端描述：NFA 专用引擎记录类型、状态数和布局内存。
	Backends map[uint32]string
	// Allocs 是一次完整扫描的分配次数。
	Allocs float64
}

// measureFixedCorpusScan 在固定规则与固定语料上采集确定性指标。
func measureFixedCorpusScan(tb testing.TB) fixedCorpusScanMetrics {
	tb.Helper()
	scanner, err := Compile(conformanceMatrixExpressions())
	if err != nil {
		tb.Fatalf("编译失败: %v", err)
	}
	corpus := conformanceMatrixCorpus()
	matches, err := scanner.Scan(corpus)
	if err != nil {
		tb.Fatalf("扫描失败: %v", err)
	}
	metrics := fixedCorpusScanMetrics{
		Matches:  make(map[uint32]int),
		Backends: make(map[uint32]string, len(scanner.rules)),
	}
	for _, match := range matches {
		metrics.Matches[match.Id]++
	}
	for _, rule := range scanner.rules {
		switch {
		case rule.nfaEngine != nil:
			metrics.Backends[rule.id] = fmt.Sprintf("nfa/%d/%d/%d",
				rule.nfaEngine.Kind, rule.nfaEngine.StateCount(), rule.nfaEngine.MemoryBytes())
		case rule.program != nil:
			stats := rule.program.Stats()
			metrics.Backends[rule.id] = fmt.Sprintf("dfa/%d/%d", stats.DFAStates, stats.MemoryBytes)
		default:
			metrics.Backends[rule.id] = "literal"
		}
	}
	metrics.Allocs = testing.AllocsPerRun(5, func() {
		if _, err := scanner.Scan(corpus); err != nil {
			tb.Fatalf("扫描失败: %v", err)
		}
	})
	return metrics
}

func formatFixedCorpusMatches(matches map[uint32]int) string {
	ids := make([]int, 0, len(matches))
	for id := range matches {
		ids = append(ids, int(id))
	}
	sort.Ints(ids)
	parts := make([]string, 0, len(ids))
	for _, id := range ids {
		parts = append(parts, fmt.Sprintf("%d=%d", id, matches[uint32(id)]))
	}
	return strings.Join(parts, ",")
}

func formatFixedCorpusBackends(backends map[uint32]string) string {
	ids := make([]int, 0, len(backends))
	for id := range backends {
		ids = append(ids, int(id))
	}
	sort.Ints(ids)
	parts := make([]string, 0, len(ids))
	for _, id := range ids {
		parts = append(parts, fmt.Sprintf("%d=%s", id, backends[uint32(id)]))
	}
	return strings.Join(parts, ",")
}

// fixedCorpusGoldenMatches 与 fixedCorpusGoldenBackends 是固定语料上的回归基线。
// 命中集合与引擎布局发生变化说明行为或选择逻辑回归，必须显式更新基线。
const (
	fixedCorpusGoldenMatches  = "1=1,2=2,3=15,4=1,5=1,6=1,7=1,8=2,9=1,10=1,11=1,12=11"
	fixedCorpusGoldenBackends = "1=nfa/11/1/60,2=literal,3=nfa/11/2/68,4=nfa/3/10/22831,5=nfa/3/15/33091,6=literal,7=literal,8=nfa/10/3/33,9=nfa/11/1/100,10=nfa/11/1/66,11=literal,12=nfa/3/8/19097"
	// fixedCorpusAllocCeiling 是固定语料单次扫描允许的最大分配次数，
	// 只允许下降不允许上升，避免把机器性能写成绝对阈值。
	fixedCorpusAllocCeiling = 100
)

// TestFixedCorpusDeterministicMetrics 校验固定语料上的命中集合、执行后端和
// 分配次数等确定性指标；时间指标不设阈值，只记录在性能基线文档中。
func TestFixedCorpusDeterministicMetrics(t *testing.T) {
	metrics := measureFixedCorpusScan(t)
	if got := formatFixedCorpusMatches(metrics.Matches); got != fixedCorpusGoldenMatches {
		t.Fatalf("固定语料命中集合变化:\n got=%s\nwant=%s", got, fixedCorpusGoldenMatches)
	}
	if got := formatFixedCorpusBackends(metrics.Backends); got != fixedCorpusGoldenBackends {
		t.Fatalf("固定语料执行后端变化:\n got=%s\nwant=%s", got, fixedCorpusGoldenBackends)
	}
	// 竞态插桩会额外分配，分配上限只在非竞态测试中作为回归门禁。
	if !raceEnabled && metrics.Allocs > fixedCorpusAllocCeiling {
		t.Fatalf("固定语料扫描分配次数超出上限: %.1f > %d", metrics.Allocs, fixedCorpusAllocCeiling)
	}
	t.Logf("固定语料确定性指标: matches=%s backend=%s allocs=%.1f",
		formatFixedCorpusMatches(metrics.Matches), formatFixedCorpusBackends(metrics.Backends), metrics.Allocs)
}

// fixedBenchCorpus 把固定语料确定性重复到约 64KiB，用于吞吐基线；
// 重复只放大测量区间，不改变命中模式与选择路径。
func fixedBenchCorpus() []byte {
	block := conformanceMatrixCorpus()
	repeats := (64 << 10) / len(block)
	if repeats < 1 {
		repeats = 1
	}
	out := make([]byte, 0, repeats*len(block))
	for i := 0; i < repeats; i++ {
		out = append(out, block...)
	}
	return out
}

// BenchmarkFixedCorpusScan 在固定语料上测量各后端的扫描吞吐、延迟和分配，
// 供性能基线文档记录与回归比较使用。
func BenchmarkFixedCorpusScan(b *testing.B) {
	expressions := conformanceMatrixExpressions()
	corpus := fixedBenchCorpus()
	entries := []struct {
		name    string
		backend simd.Backend
	}{
		{"generic", simd.Default()},
		{"host", dispatch.DefaultBackend()},
	}
	for _, entry := range entries {
		b.Run(entry.name, func(b *testing.B) {
			dispatch.SetBackendOverride(entry.backend)
			defer dispatch.SetBackendOverride(nil)
			scanner, err := Compile(expressions)
			if err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			b.SetBytes(int64(len(corpus)))
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := scanner.Scan(corpus); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
