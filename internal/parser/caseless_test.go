package parser

import (
	"bytes"
	"testing"
	"unicode"
	"unicode/utf8"
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

// TestFirstBytesCaselessFallsBackToFull 固定首字节推导无法限定的情形：可空、任意
// 字节，以及非法 UTF-8 的忽略大小写字面量。非法 UTF-8 无法按 rune 还原折叠等价
// 类，必须整体放弃首字节推导。
func TestFirstBytesCaselessFallsBackToFull(t *testing.T) {
	for _, pattern := range []string{`(?i).`, `(?i)a?`, `(?i)\xc3`} {
		got := FirstBytesCaseless(mustParse(t, pattern))
		if len(got) != 256 {
			t.Errorf("FirstBytesCaseless(%q) 返回 %d 个字节，期望完整集合", pattern, len(got))
		}
	}
}

// TestFirstBytesCaselessNonASCIILeading 固定非 ASCII 忽略大小写字面量的首字节
// 收窄：解析器把多字节文字拆成逐字节 Literal，推导时必须先拼回首个 rune，再按
// 折叠轨道给出首字节超集，而不是整体退回完整集合。
func TestFirstBytesCaselessNonASCIILeading(t *testing.T) {
	cases := []struct {
		pattern string
		want    []byte
	}{
		{`(?i)é`, []byte{0xC3}},
		{`(?i)É`, []byte{0xC3}},
		{`(?i)école`, []byte{0xC3}},
		{`(?i)Здравствуйте`, []byte{0xD0}},
		// 首 rune 的 ToLower 为 ASCII 'i'，等价类额外含 U+0130 'İ'（首字节 0xC4）。
		{`(?i)içerik`, []byte{0x49, 0x69, 0xC4}},
	}
	for _, tc := range cases {
		got := FirstBytesCaseless(mustParse(t, tc.pattern))
		if !bytes.Equal(got, tc.want) {
			t.Errorf("FirstBytesCaseless(%q) = %v, want %v", tc.pattern, got, tc.want)
		}
	}
}

// TestCaselessLeadingBytesExhaustive 穷举全 Unicode，确认 CaselessLeadingBytes 给出
// 的首字节集合覆盖「ToLower 等价类」中所有 rune 的首字节，即起点过滤不会漏判。
func TestCaselessLeadingBytesExhaustive(t *testing.T) {
	preimage := make(map[rune][]rune)
	for u := rune(0); u <= 0x10FFFF; u++ {
		if u >= 0xD800 && u <= 0xDFFF {
			continue
		}
		lower := unicode.ToLower(u)
		preimage[lower] = append(preimage[lower], u)
	}
	covered := 0
	for r := rune(0); r <= 0x10FFFF; r++ {
		if r >= 0xD800 && r <= 0xDFFF {
			continue
		}
		literal := utf8.AppendRune(nil, r)
		set := CaselessLeadingBytes(literal)
		if set == nil {
			t.Fatalf("字面量 %#U 无法推导首字节", r)
		}
		has := func(b byte) bool {
			for _, v := range set {
				if v == b {
					return true
				}
			}
			return false
		}
		for _, u := range preimage[unicode.ToLower(r)] {
			ub := utf8.AppendRune(nil, u)
			if len(ub) == 0 {
				continue
			}
			if !has(ub[0]) {
				t.Fatalf("漏判：字面量首 rune %#U 的等价类含 %#U，首字节 %#x 不在 %v 中", r, u, ub[0], set)
			}
		}
		covered++
	}
	if covered < 0x10FFFF-2048 {
		t.Fatalf("穷举覆盖不足：只检查了 %d 个 rune", covered)
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
