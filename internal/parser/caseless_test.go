package parser

import (
	"bytes"
	"testing"
)

func mustParse(t *testing.T, pattern string) Node {
	t.Helper()
	root, err := Parse(pattern)
	if err != nil {
		t.Fatalf("Parse(%q): %v", pattern, err)
	}
	return root
}

func TestUniformCaseless(t *testing.T) {
	cases := []struct {
		pattern string
		want    bool
	}{
		{`password`, false},
		{`(?i)password`, true},
		{`(?i)password\s*[:=]\s*\S+`, true},
		{`(?i:abc)`, true},
		{`abc(?i:def)`, false},
		{`(?i)abc(?-i:def)`, false},
		{`(?i)(a|B)`, true},
		{`(?i)`, false},
		{`(?i)(?s)a.b`, true},
		{`(?i)a(?=b)`, false},
	}
	for _, tc := range cases {
		got := UniformCaseless(mustParse(t, tc.pattern))
		if got != tc.want {
			t.Errorf("UniformCaseless(%q) = %v, want %v", tc.pattern, got, tc.want)
		}
	}
}

func TestFirstBytesCaseless(t *testing.T) {
	cases := []struct {
		pattern string
		want    []byte
	}{
		{`(?i)password`, []byte{'P', 'p'}},
		{`(?i)[a-c]x`, []byte{'A', 'B', 'C', 'a', 'b', 'c'}},
		{`(?i)PaSsWoRd`, []byte{'P', 'p'}},
	}
	for _, tc := range cases {
		got := FirstBytesCaseless(mustParse(t, tc.pattern))
		if !bytes.Equal(got, tc.want) {
			t.Errorf("FirstBytesCaseless(%q) = %v, want %v", tc.pattern, got, tc.want)
		}
	}
}

func TestFirstBytesCaselessFallsBackToFull(t *testing.T) {
	for _, pattern := range []string{`(?i)é`, `(?i).`, `(?i)a?`} {
		got := FirstBytesCaseless(mustParse(t, pattern))
		if len(got) != 256 {
			t.Errorf("FirstBytesCaseless(%q) 返回 %d 个字节，期望完整集合", pattern, len(got))
		}
	}
}

func TestHasCaselessClear(t *testing.T) {
	cases := []struct {
		pattern string
		want    bool
	}{
		{`password`, false},
		{`(?i)password`, false},
		{`(?i)abc(?-i:def)`, true},
		{`(?-i)abc`, true},
	}
	for _, tc := range cases {
		got := HasCaselessClear(mustParse(t, tc.pattern))
		if got != tc.want {
			t.Errorf("HasCaselessClear(%q) = %v, want %v", tc.pattern, got, tc.want)
		}
	}
}
