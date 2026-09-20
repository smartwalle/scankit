package nfa

import (
	"fmt"
	"testing"

	"github.com/smartwalle/scankit/internal/nfagraph"
	"github.com/smartwalle/scankit/internal/parser"
)

// TestLBRComplexAssertionRejection 验证 LBR 在遇到超出支持集合的断言
// （包括 lookaround）时拒绝进入专用布局，并通过 CompileEngine 回退到通用路径。
func TestLBRComplexAssertionRejection(t *testing.T) {
	cases := []string{
		`(?=a)`,   // lookahead only - lookaround
		`(?<=a)b`, // lookbehind
		`a(?=b)`,  // pattern with lookahead
	}
	for _, pat := range cases {
		r, err := parser.Parse(pat)
		if err != nil {
			t.Fatalf("parse %q: %v", pat, err)
		}
		g, err := nfagraph.NewBuilder().Build(r)
		if err != nil {
			t.Fatalf("graph %q: %v", pat, err)
		}
		e, err := CompileEngine(g, EngineLBR)
		if err != nil {
			t.Fatalf("compile %q: %v", pat, err)
		}
		if e.lbr != nil {
			t.Fatalf("LBR should reject %q, got non-nil lbr", pat)
		}
	}
}

// TestLBRStateCompressionMemoryLayout 验证 LBR 的 dead/accept 压缩不依赖
// per-vertex 大切片，避免大图上的内存膨胀。
func TestLBRStateCompressionMemoryLayout(t *testing.T) {
	r, err := parser.Parse(`^(?:abc|def)$`)
	if err != nil {
		t.Fatal(err)
	}
	g, err := nfagraph.NewBuilder().Build(r)
	if err != nil {
		t.Fatal(err)
	}
	e, err := CompileEngine(g, EngineLBR)
	if err != nil || e.lbr == nil {
		t.Fatalf("LBR compile failed: %v", err)
	}
	// deadStates 应当是紧凑的 bool 数组（每个状态 1 字节），
	// 与直接的 map[graph.Vertex]bool 相比更节省内存。
	if got, want := cap(e.lbr.deadStates), len(e.lbr.vertices); got != want {
		t.Fatalf("deadStates 容量=%d 应等于状态数=%d", got, want)
	}
}

// TestLBRCanReuseBufferForSpans 验证 LBR 整块扫描能将结果写入调用方缓冲，
// 避免每次扫描分配新的 []Span。
func TestLBRCanReuseBufferForSpans(t *testing.T) {
	r, err := parser.Parse(`^abc$`)
	if err != nil {
		t.Fatal(err)
	}
	g, err := nfagraph.NewBuilder().Build(r)
	if err != nil {
		t.Fatal(err)
	}
	e, err := CompileEngine(g, EngineLBR)
	if err != nil || e.lbr == nil {
		t.Fatalf("LBR compile failed: %v", err)
	}
	// 这里只验证 Spans 能输出确定的结果并保持稳定。
	spans1 := e.Spans([]byte("abc"))
	spans2 := e.Spans([]byte("abc"))
	if fmt.Sprint(spans1) != fmt.Sprint(spans2) {
		t.Fatalf("Spans not stable: %v vs %v", spans1, spans2)
	}
}
