package hwlm

import "testing"

func TestDeduplicate(t *testing.T) {
	if len(Deduplicate([]Literal{{Value: []byte("a")}, {Value: []byte("a")}})) != 1 {
		t.Fatal()
	}
}

func TestDeduplicateKeepsDistinctRuleIDs(t *testing.T) {
	literals := Deduplicate([]Literal{{ID: 1, Value: []byte("a")}, {ID: 2, Value: []byte("a")}})
	if len(literals) != 2 || literals[0].ID != 1 || literals[1].ID != 2 {
		t.Fatalf("distinct rule ids were merged: %#v", literals)
	}
}
