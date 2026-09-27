package rose

import (
	"testing"
	"unicode"
	"unicode/utf8"
)

// lowerPreimage 返回「ToLower(u) == L」的全部 rune，按 u 升序。
// 只在测试中构建：一次遍历全 Unicode，供穷举校验使用。
func lowerPreimage() map[rune][]rune {
	out := make(map[rune][]rune)
	for u := rune(0); u <= 0x10FFFF; u++ {
		if u >= 0xD800 && u <= 0xDFFF {
			continue
		}
		l := unicode.ToLower(u)
		out[l] = append(out[l], u)
	}
	return out
}

// TestRoleFirstByteSetExhaustive 穷举全 Unicode，确认 roleFirstByteSet 给出的首字节
// 集合覆盖 Role.MatchAt 语义下所有可命中的首 rune 首字节，即预过滤不会漏判。
func TestRoleFirstByteSetExhaustive(t *testing.T) {
	preimage := lowerPreimage()
	literals := 0
	for r := rune(0); r <= 0x10FFFF; r++ {
		if r >= 0xD800 && r <= 0xDFFF {
			continue
		}
		buf := utf8.AppendRune(nil, r)
		if !utf8.Valid(buf) {
			continue
		}
		role := Role{ID: 1, Literal: buf, CaseInsensitive: true}
		set := roleFirstByteSet(role)
		literals++
		if set == nil {
			// 无可建集合时回退逐偏移，语义安全。
			continue
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
				t.Fatalf("漏判：字面量首 rune %#U 的等价类含 %#U，首字节 %#x 不在集合 %v 中", r, u, ub[0], set)
			}
		}
	}
	if literals < 0x10FFFF-2048 {
		t.Fatalf("穷举覆盖不足：只检查了 %d 个 rune", literals)
	}
}

// TestCaselessUnicodeFirstBytesAgreement 用独立的逐偏移 Eligible 参考实现，逐一比对
// RoleFirstBytes 预过滤后的 Program 结果，覆盖大小写变体、整段命中、KELVIN/İ 特例与
// 无效 UTF-8 输入。
func TestCaselessUnicodeFirstBytesAgreement(t *testing.T) {
	literals := []string{
		"é", "É", "école", "ÉCOLE", "café", "Straße", "STRASSE", "münchen",
		"Здравствуйте", "İstanbul", "içerik", "ﬁle", "K", "Kelvin", "Ångström",
		"ångström", "Ω", "ω", "ς", "σ", "Σ", "日本語", "İ", "ß",
	}
	corpora := []string{
		"",
		"....",
		"ÉCOLE école École E\u0301COLE",
		"café CAFÉ Café cafe",
		"STRASSE straße Straße STRASSE",
		"MÜNCHEN münchen München",
		"İSTANBUL İstanbul istanbul içerik IÇERIK",
		"ﬁle FILE file",
		"kelvin Kelvin KELVIN KK \u212A",
		"ÅNGSTRÖM Ångström ångström \u212B",
		"ω Ω \u2126 Σ σ ς",
		"日本語 日本語",
		"\xff\xfe école \x80",
		"the quick brown fox jumps over the lazy dog while packing boxes",
		"écoleÉCOLEcaféStraßeЗдравствуйтеİstanbul",
	}
	for _, lit := range literals {
		roles := []Role{{ID: 1, Literal: []byte(lit), CaseInsensitive: true}}
		p := New(roles)
		for _, corpus := range corpora {
			data := []byte(corpus)
			got := p.FindMatchesInto(data, nil)
			want := refMatches(roles, data)
			if !sameStates(got, want) {
				t.Fatalf("字面量 %q 语料 %q：预过滤结果 %v，参考 %v", lit, corpus, got, want)
			}
		}
	}
}

// TestCaselessUnicodeMultiRoleFirstBytesAgreement 覆盖多角色（含一个无法建自动机的
// 非 ASCII 折叠角色）时全部角色都会落到逐偏移回退的形态。
func TestCaselessUnicodeMultiRoleFirstBytesAgreement(t *testing.T) {
	roles := []Role{
		{ID: 1, Literal: []byte("café"), CaseInsensitive: true},
		{ID: 2, Literal: []byte("xyz")},
		{ID: 3, Literal: []byte("İstanbul"), CaseInsensitive: true},
		{ID: 4, Literal: []byte("MÜNCHEN"), CaseInsensitive: true},
		{ID: 5, Literal: []byte("Ω")},
	}
	corpora := []string{
		"",
		"xyz xyz café CAFÉ İstanbul istanbul MÜNCHEN münchen ω Ω",
		"CAFÉXYZİSTANBULmünchenΩ",
		"\xffcafé\x80xyz",
	}
	p := New(roles)
	for _, corpus := range corpora {
		data := []byte(corpus)
		got := p.FindMatchesInto(data, nil)
		want := refMatches(roles, data)
		if !sameStates(got, want) {
			t.Fatalf("语料 %q：预过滤结果 %v，参考 %v", corpus, got, want)
		}
	}
}

// refMatches 是与 nextRoleOffset 无关的参考实现：对每个角色的每个偏移直接调用
// Role.Eligible，再按 (Offset, RoleID) 稳定排序，复现优化前的语义。
func refMatches(roles []Role, data []byte) []State {
	out := make([]State, 0)
	for _, role := range roles {
		length := len(role.Literal)
		for off := 0; off+length <= len(data); off++ {
			if role.Eligible(data, off) {
				out = append(out, State{RoleID: role.ID, Offset: uint64(off)})
			}
		}
	}
	sortStatesInPlace(out)
	return out
}

func sameStates(a, b []State) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
