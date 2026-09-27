package compiler

import "testing"

func TestUsageAdd(t *testing.T) {
	u := Usage{GraphVertices: 1}
	u.Add(Usage{GraphVertices: 2})
	if u.GraphVertices != 3 {
		t.Fatal()
	}
}

func TestCompileLimitsTightestAndConstrained(t *testing.T) {
	l := CompileLimits{DFAStates: 8, MemoryBytes: 4}
	if !l.Constrained() {
		t.Fatal("有限预算未识别")
	}
	name, value := l.Tightest()
	if name != "memory_bytes" || value != 4 {
		t.Fatalf("最紧预算=%s,%d", name, value)
	}
}
