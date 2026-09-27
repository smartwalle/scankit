package parser

import "testing"

func TestIsEmpty(t *testing.T) {
	if !IsEmpty(Sequence{}) || IsEmpty(Literal{Value: []byte("a")}) {
		t.Fatal()
	}
}
