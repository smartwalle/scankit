package scankit

import (
	"testing"
)

const sparseLiteralRuleCount = 40_000

func TestLiteralAutomatonSparseTraversal(t *testing.T) {
	automaton := buildSparseLiteralTestAutomaton()
	if !automaton.sparse {
		t.Fatal("large literal automaton did not select sparse representation")
	}
	for _, index := range []int{0, 1, 12_345, sparseLiteralRuleCount - 1} {
		state := uint32(0)
		for _, value := range sparseLiteralPattern(index) {
			state = automaton.nextSparse(state, value)
		}
		start, end := automaton.outputStart[state], automaton.outputEnd[state]
		if end-start != 1 || automaton.outputs[start] != uint32(index) {
			t.Fatalf("rule %d resolved outputs %v", index, automaton.outputs[start:end])
		}
	}
}

func TestScanLargeSparseLiteralDatabase(t *testing.T) {
	database, err := Compile(sparseLiteralExpressions())
	if err != nil {
		t.Fatal(err)
	}
	data := []byte("before rule-00000 middle rule-12345 after rule-39999")
	matches, err := database.Scan(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 3 {
		t.Fatalf("match count = %d, want 3", len(matches))
	}
	for index, id := range []uint32{1, 12_346, sparseLiteralRuleCount} {
		if matches[index].Id != id {
			t.Fatalf("match %d id = %d, want %d", index, matches[index].Id, id)
		}
	}
}

func FuzzLiteralAutomatonSparseTraversal(f *testing.F) {
	automaton := buildSparseLiteralTestAutomaton()
	for _, index := range []int{0, 12_345, sparseLiteralRuleCount - 1} {
		f.Add(sparseLiteralPattern(index))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 256 {
			t.Skip()
		}
		state := uint32(0)
		for _, value := range data {
			state = automaton.nextSparse(state, value)
			start, end := automaton.outputStart[state], automaton.outputEnd[state]
			for _, output := range automaton.outputs[start:end] {
				if output >= sparseLiteralRuleCount {
					t.Fatalf("invalid output index %d", output)
				}
			}
		}
	})
}

func FuzzScanLargeSparseLiteralDatabase(f *testing.F) {
	database, err := Compile(sparseLiteralExpressions())
	if err != nil {
		f.Fatal(err)
	}
	f.Add([]byte("rule-00000 rule-39999"))
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 512 {
			t.Skip()
		}
		matches, err := database.Scan(data)
		if err != nil {
			t.Fatal(err)
		}
		for _, match := range matches {
			if match.Id == 0 || match.Id > sparseLiteralRuleCount || match.From >= match.To || match.To > uint64(len(data)) {
				t.Fatalf("invalid match %#v for %d-byte input", match, len(data))
			}
		}
	})
}

func buildSparseLiteralTestAutomaton() literalAutomaton {
	builder := newLiteralBuilder()
	for index := 0; index < sparseLiteralRuleCount; index++ {
		builder.add(sparseLiteralPattern(index), uint32(index))
	}
	return builder.freeze()
}

func sparseLiteralExpressions() []Expression {
	expressions := make([]Expression, sparseLiteralRuleCount)
	for index := range expressions {
		expressions[index] = Expression{Id: uint32(index + 1), Pattern: string(sparseLiteralPattern(index))}
	}
	return expressions
}

func sparseLiteralPattern(index int) []byte {
	pattern := []byte("rule-00000")
	for position := len(pattern) - 1; position >= len("rule-"); position-- {
		pattern[position] = byte('0' + index%10)
		index /= 10
	}
	return pattern
}
