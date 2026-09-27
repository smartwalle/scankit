package dispatch

import "testing"

func TestNormalizeArch(t *testing.T) {
	if NormalizeArch("x86_64") != "amd64" || NormalizeArch("aarch64") != "arm64" {
		t.Fatal()
	}
}
