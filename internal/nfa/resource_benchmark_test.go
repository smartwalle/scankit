package nfa

import (
	"testing"

	"github.com/smartwalle/scankit/internal/nfagraph"
	"github.com/smartwalle/scankit/internal/parser"
)

// BenchmarkResourceUsage 记录不同专用布局的状态、边、内存和运行成本。
// 结果只用于同一环境的回归比较，不作为跨平台绝对阈值。
func BenchmarkResourceUsage(b *testing.B) {
	root, err := parser.Parse(`(?:foo|bar|[a-z]{1,3})+`)
	if err != nil {
		b.Fatal(err)
	}
	g, err := nfagraph.NewBuilder().Build(root)
	if err != nil {
		b.Fatal(err)
	}
	data := []byte("foo bar baz foobar")
	for _, kind := range []EngineKind{EngineCastle, EngineLimEx, EngineSheng, EngineTamarama, EngineShufti} {
		e, err := CompileEngine(g, kind)
		if err != nil {
			b.Fatal(err)
		}
		b.Run(kind.String(), func(b *testing.B) {
			usage := e.Usage()
			b.ReportMetric(float64(usage.States), "states")
			b.ReportMetric(float64(usage.Edges), "edges")
			b.ReportMetric(float64(usage.Memory), "bytes/layout")
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_, _, _ = e.SpansBudget(data, 0, 0)
			}
		})
	}
}
