package parser

import "testing"

func TestParseCombination(t *testing.T) {
	if _, err := ParseCombination("(101&102&103)|(104&!105)"); err != nil {
		t.Fatal(err)
	}
	if _, err := ParseCombination("101&"); err == nil {
		t.Fatal("expected error")
	}
}

func TestParseCombinationWhitespace(t *testing.T) {
	if _, err := ParseCombination(" (1 & 2) | !3 "); err != nil {
		t.Fatal(err)
	}
}
