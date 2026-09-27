package hwlm

import "testing"

func TestLongest(t *testing.T) {
	if string(Longest([]Literal{{Value: []byte("a")}, {Value: []byte("abc")}}).Value) != "abc" {
		t.Fatal()
	}
}
