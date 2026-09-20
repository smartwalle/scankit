package util

import "testing"

func TestCharReachComplement(t *testing.T) {
	var c CharReach
	c.Add('a')
	d := c.Complement()
	if d.Contains('a') || !d.Contains('b') {
		t.Fatal()
	}
}
