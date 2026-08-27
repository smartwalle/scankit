package scankit

import "testing"

func TestExecutionRoleGraphMatchesBlockPlan(t *testing.T) {
	t.Parallel()
	scanner, err := Compile([]Expression{
		{Id: 1, Pattern: `phone=1[3-9][0-9]{9}`},
		{Id: 2, Pattern: `62[0-9]{14,17}`},
		{Id: 3, Pattern: `[z][a-z]{9,}`},
		{Id: 4, Pattern: `alice@example\.com`},
	})
	if err != nil {
		t.Fatal(err)
	}
	graph := scanner.roleGraph
	if !graph.valid(scanner.expressions, scanner.eventNeeded) {
		t.Fatalf("invalid role graph: %#v", graph)
	}
	if graph.budget.triggerCount != uint32(len(scanner.triggers)) {
		t.Fatalf("trigger count = %d, want %d", graph.budget.triggerCount, len(scanner.triggers))
	}
	if graph.budget.reportCount != uint32(len(scanner.expressions)) {
		t.Fatalf("report count = %d, want %d", graph.budget.reportCount, len(scanner.expressions))
	}
	if graph.budget.verifierCount == 0 {
		t.Fatal("role graph has no verifier")
	}
}

func TestExecutionRoleGraphKeepsQuietCombinationOperands(t *testing.T) {
	t.Parallel()
	scanner, err := Compile([]Expression{
		{Id: 1, Pattern: `foo`, Flags: CompileQuiet},
		{Id: 2, Pattern: `bar`},
		{Id: 3, Pattern: `1&2`, Flags: CompileCombination},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !scanner.roleGraph.valid(scanner.expressions, scanner.eventNeeded) {
		t.Fatalf("invalid role graph: %#v", scanner.roleGraph)
	}
	if scanner.roleGraph.budget.reportCount != 3 {
		t.Fatalf("report count = %d, want 3", scanner.roleGraph.budget.reportCount)
	}
}

func TestExecutionRoleGraphCapturesDistanceDeduplicationAndDelay(t *testing.T) {
	anchored, err := Compile([]Expression{{Id: 1, Pattern: `tag=[A-Z]{2}`}})
	if err != nil {
		t.Fatal(err)
	}
	if !roleGraphHasEdge(anchored.roleGraph, executionRoleBoundedDistance) ||
		!roleGraphHasEdge(anchored.roleGraph, executionRoleDeduplicatesAtEnd) ||
		!roleGraphHasEdge(anchored.roleGraph, executionRoleMustDelay) {
		t.Fatalf("anchored role graph omits delivery relations: %#v", anchored.roleGraph)
	}

	direct, err := Compile([]Expression{{Id: 1, Pattern: `tag`}})
	if err != nil {
		t.Fatal(err)
	}
	if roleGraphHasEdge(direct.roleGraph, executionRoleMustDelay) {
		t.Fatalf("direct literal unexpectedly requires delayed delivery: %#v", direct.roleGraph)
	}
}

func roleGraphHasEdge(graph executionRoleGraph, kind executionRoleEdgeKind) bool {
	for _, edge := range graph.edges {
		if edge.kind == kind {
			return true
		}
	}
	return false
}
