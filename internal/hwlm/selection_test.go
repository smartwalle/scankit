package hwlm

import "testing"

func TestSelect(t *testing.T) {
	if Select(nil) != "none" || Select([]Literal{{Value: []byte("a")}}) != "fdr" || Select([]Literal{{Value: []byte("abcdefgh")}, {Value: []byte("x")}}) != "teddy" || Select([]Literal{{Value: []byte("a")}, {Value: []byte("b")}, {Value: []byte("c")}, {Value: []byte("d")}, {Value: []byte("e")}}) != "noodle" {
		t.Fatal()
	}
}

func TestExplainSelection(t *testing.T) {
	d := ExplainSelection([]Literal{{ID: 1, Value: []byte("abcdefgh"), CaseInsensitive: true}})
	if d.Backend != "teddy" || d.Longest != 8 || !d.Caseless {
		t.Fatalf("选择说明=%#v", d)
	}
}
