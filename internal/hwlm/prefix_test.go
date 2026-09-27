package hwlm

import "testing"

func TestPrefix(t *testing.T) {
	if string(Prefix([]Literal{{Value: []byte("abc")}, {Value: []byte("abd")}})) != "ab" {
		t.Fatal()
	}
}
