package nfagraph

import "testing"

func TestNodeKindString(t *testing.T) {
	if KindLiteral.String() != "literal" {
		t.Fatal()
	}
}
