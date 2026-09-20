package parser

import "testing"

func TestRunConformance(t *testing.T) {
	cases := []ConformanceCase{
		{Name: "literal", Pattern: "abc", Valid: true, Kind: KindSequence, MinWidth: 3, MaxWidth: 3},
		{Name: "alternation", Pattern: "a|bc", Valid: true, Kind: KindAlternation, MinWidth: 1, MaxWidth: 2},
		{Name: "repeat", Pattern: "a{2,4}", Valid: true, Kind: KindRepeat, MinWidth: 2, MaxWidth: 4},
		{Name: "invalid", Pattern: "[", Valid: false},
	}
	if err := RunConformance(cases); err != nil {
		t.Fatal(err)
	}
}

func TestDefaultConformanceCases(t *testing.T) {
	if err := RunConformance(DefaultConformanceCases()); err != nil {
		t.Fatal(err)
	}
}
