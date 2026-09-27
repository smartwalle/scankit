package nfa

import (
	"testing"

	"github.com/smartwalle/scankit/internal/nfagraph"
	"github.com/smartwalle/scankit/internal/parser"
)

func TestCastleIndependentByteExecution(t *testing.T) {
	r, _ := parser.Parse("ab|ac")
	g, _ := nfagraph.NewBuilder().Build(r)
	e, err := CompileEngine(g, EngineCastle)
	if err != nil {
		t.Fatal(err)
	}
	if !e.Independent() || e.ExecutionModel() != "castle" {
		t.Fatalf("模型=%s", e.ExecutionModel())
	}
	if got := e.MatchAt([]byte("ac"), 0); len(got) != 1 || got[0] != 2 {
		t.Fatalf("结果=%v", got)
	}
	clone := e.Clone()
	if !clone.Independent() {
		t.Fatal("克隆丢失独立引擎")
	}
}

func TestCastleDeterministicPrefixRejectsEarly(t *testing.T) {
	r, err := parser.Parse(`abcdef`)
	if err != nil {
		t.Fatal(err)
	}
	g, err := nfagraph.NewBuilder().Build(r)
	if err != nil {
		t.Fatal(err)
	}
	e, err := CompileEngine(g, EngineCastle)
	if err != nil || e.castle == nil {
		t.Fatalf("Castle 编译失败: %v", err)
	}
	if got, steps, stopped := e.castle.MatchAtBudget([]byte("xbcdef"), 0, 0, 0); len(got) != 0 || steps != 0 || stopped {
		t.Fatalf("前缀不匹配未快速结束: got=%v steps=%d stopped=%v", got, steps, stopped)
	}
}

func TestCompiledProgramUsesCastleForByteGraph(t *testing.T) {
	r, _ := parser.Parse("ab")
	g, _ := nfagraph.NewBuilder().Build(r)
	p, err := Compile(g)
	if err != nil {
		t.Fatal(err)
	}
	if p.castle == nil {
		t.Fatal("字节图未建立独立状态执行路径")
	}
	if got := p.Spans([]byte("zab")); len(got) != 1 || got[0] != (Span{1, 3}) {
		t.Fatalf("结果=%v", got)
	}
}
