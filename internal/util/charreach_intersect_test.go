package util

import "testing"

func TestCharReachIntersects(t *testing.T) {
	var a, b CharReach
	a.Add('a')
	b.Add('b')
	if a.Intersects(b) {
		t.Fatal()
	}
	b.Add('a')
	if !a.Intersects(b) {
		t.Fatal()
	}
}
