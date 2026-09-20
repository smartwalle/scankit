package nfagraph

import (
	"testing"

	"github.com/smartwalle/scankit/internal/parser"
)

func TestOptimizeConvergesAndPreservesGraphValidity(t *testing.T) {
	node, err := parser.Parse(`(?:a|a)b`)
	if err != nil {
		t.Fatal(err)
	}
	graph, err := NewBuilder().Build(node)
	if err != nil {
		t.Fatal(err)
	}
	stats, err := Optimize(graph)
	if err != nil {
		t.Fatal(err)
	}
	if err := graph.Validate(); err != nil {
		t.Fatal(err)
	}
	if stats.MergedLiterals+stats.MergedNodes+stats.SquashedJoins == 0 {
		t.Fatal("优化未产生可观测收敛结果")
	}
}

func TestOptimizeWithLimitRestoresOnNonConvergence(t *testing.T) {
	node, err := parser.Parse("(?:a|b)+")
	if err != nil {
		t.Fatal(err)
	}
	graph, err := NewBuilder().Build(node)
	if err != nil {
		t.Fatal(err)
	}
	original := graph.Clone()
	if _, err := OptimizeWithLimit(graph, 1); err == nil {
		t.Fatal("过小重写轮数未报告未收敛")
	}
	if !graph.Equal(original) {
		t.Fatal("未收敛时未恢复原图")
	}
}
