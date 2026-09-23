package noodle

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/smartwalle/scankit/internal/hwlm"
)

// TestFlatEligibleRejectsUnsupportedLiterals 验证扁平索引的适用范围：只接受
// 大小写敏感、长度落在 [FlatKeyShort, FlatMaxLiteral] 的文字。最短文字不足
// 8 字节时退到 4 字节主键，仍有一段更短的文字或超长文字才回退前缀树。
func TestFlatEligibleRejectsUnsupportedLiterals(t *testing.T) {
	cases := []struct {
		name     string
		literals []hwlm.Literal
		want     int
	}{
		{name: "empty", literals: nil, want: 0},
		{name: "tooShort", literals: []hwlm.Literal{{ID: 1, Value: []byte("fie")}}, want: 0},
		{name: "shortKey", literals: []hwlm.Literal{{ID: 1, Value: []byte("field0=")}}, want: hwlm.FlatKeyShort},
		{name: "keyLength", literals: []hwlm.Literal{{ID: 1, Value: []byte("field000")}}, want: hwlm.FlatKeyLong},
		{name: "maxLength", literals: []hwlm.Literal{{ID: 1, Value: []byte("field00000000000")}}, want: hwlm.FlatKeyLong},
		{name: "tooLong", literals: []hwlm.Literal{{ID: 1, Value: []byte("field000000000000")}}, want: 0},
		{name: "caseless", literals: []hwlm.Literal{{ID: 1, Value: []byte("field000"), CaseInsensitive: true}}, want: 0},
		{name: "mixedLengths", literals: []hwlm.Literal{
			{ID: 1, Value: []byte("field000")},
			{ID: 2, Value: []byte("field0000000")},
		}, want: hwlm.FlatKeyLong},
		{name: "mixedShortKey", literals: []hwlm.Literal{
			{ID: 1, Value: []byte("field000")},
			{ID: 2, Value: []byte("field00")},
		}, want: hwlm.FlatKeyShort},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := hwlm.FlatKeyWidth(tc.literals); got != tc.want {
				t.Fatalf("FlatKeyWidth = %d, want %d", got, tc.want)
			}
			table := newFlatTable(tc.literals)
			if (tc.want != 0) != (table != nil) {
				t.Fatalf("newFlatTable != nil = %v, want %v", table != nil, tc.want != 0)
			}
		})
	}
}

// flatLiterals 覆盖共享 8 字节主键、长度边界、高位字节与缓冲区末尾截断
// 所需的最小文字集合。
func flatLiterals() []hwlm.Literal {
	return []hwlm.Literal{
		{ID: 1, Value: []byte("field000")},
		{ID: 2, Value: []byte("field000=1")},
		{ID: 3, Value: []byte("field000=1234567")},
		{ID: 4, Value: []byte{0xFF, 0xFE, 0xFD, 0xFC, 0xFB, 0xFA, 0xF9, 0xF8, 0xFF}},
		{ID: 5, Value: []byte("field001=")},
		{ID: 6, Value: []byte("\x00\x01\x02\x03\x04\x05\x06\x07")},
		{ID: 7, Value: []byte("\x00\x01\x02\x03\x04\x05\x06\x07\x08")},
	}
}

// TestFlatTableMatchesNaiveScan 在逐字节位置上对比扁平索引与朴素前缀判定，
// 覆盖共享主键、长度边界、非 ASCII 字节与缓冲区末尾不足一个定长窗口的场景。
func TestFlatTableMatchesNaiveScan(t *testing.T) {
	literals := flatLiterals()
	table := newFlatTable(literals)
	if table == nil {
		t.Fatal("flat table is nil")
	}
	data := []byte("field000=1234567 field000=1 field000 field001= tail\xFF\xFE\xFD\xFC\xFB\xFA\xF9\xF8\xFF\x00\x01\x02\x03\x04\x05\x06\x07\x08")
	out := make([]Match, 0, 32)
	index := 0
	for from := 0; from < len(data); from++ {
		got := table.appendMatches(nil, data, from)
		want := make([]Match, 0, 2)
		for _, literal := range literals {
			if bytes.HasPrefix(data[from:], literal.Value) {
				want = append(want, Match{ID: literal.ID, From: from, To: from + len(literal.Value)})
			}
		}
		if len(got) != len(want) {
			t.Fatalf("起点 %d 匹配数量 = %d, want %d (%v vs %v)", from, len(got), len(want), got, want)
		}
		for i := range got {
			if got[i] != want[i] {
				t.Fatalf("起点 %d 第 %d 个匹配 = %+v, want %+v", from, i, got[i], want[i])
			}
		}
		out = append(out[:0], got...)
		index++
	}
	if index != len(data) {
		t.Fatalf("扫描位置 = %d, want %d", index, len(data))
	}
}

// TestFlatTableTailFallback 单独验证缓冲区末尾不足 16 字节时的逐字节回退：
// 只有完整落在缓冲区内的文字才算命中，跨界的文字必须被丢弃。
func TestFlatTableTailFallback(t *testing.T) {
	literals := []hwlm.Literal{
		{ID: 1, Value: []byte("field000=")},
		{ID: 2, Value: []byte("field000=1234567")},
	}
	table := newFlatTable(literals)
	if table == nil {
		t.Fatal("flat table is nil")
	}
	// 缓冲区恰好 15 字节：短文字完整命中，长文字因越界必须被丢弃。
	short := []byte("field000=aaaaaa")
	got := table.appendMatches(nil, short, 0)
	if len(got) != 1 || got[0].ID != 1 || got[0].To != 9 {
		t.Fatalf("短缓冲区匹配 = %v, want 仅短文字命中", got)
	}
	// 缓冲区承载完整长文字时两者都应命中。
	long := []byte("field000=1234567")
	got = table.appendMatches(nil, long, 0)
	if len(got) != 2 {
		t.Fatalf("长缓冲区匹配 = %v, want 两条命中", got)
	}
	// 不足主键宽度时不做任何判定。
	if got := table.appendMatches(nil, []byte("field00"), 0); len(got) != 0 {
		t.Fatalf("短于主键宽度的缓冲区匹配 = %v, want 空", got)
	}
}

// TestFlatMatcherMatchesBruteForce 验证启用扁平索引的匹配器在候选集合上与
// 朴素实现一致，并确认热路径退回单 lane 首字节掩码。
func TestFlatMatcherMatchesBruteForce(t *testing.T) {
	m := New(flatLiterals())
	if m.flat == nil {
		t.Fatal("预期启用扁平索引")
	}
	if m.Lanes() != 1 {
		t.Fatalf("扁平索引下 lane 数 = %d, want 1", m.Lanes())
	}
	if m.laneSets[1][0]|m.laneSets[1][1]|m.laneSets[1][2]|m.laneSets[1][3] != 0 {
		t.Fatal("扁平索引下不应构建次 lane 掩码")
	}
	inputs := []string{
		"",
		"field000",
		"field000=",
		"field000=1234567",
		"field000=1234567field000=1field000",
		"prefix field000=1234567 suffix field000=1",
		"field000=123456 field000=1234567",
		"zzzfield001=zzz",
		"\x00\x01\x02\x03\x04\x05\x06\x07\x08",
		"\xFF\xFE\xFD\xFC\xFB\xFA\xF9\xF8\xFF",
		"field0000=1field000=1234567",
		fmt.Sprintf("%s%s", bytes.Repeat([]byte("f"), 70), "field000=1"),
	}
	for _, input := range inputs {
		data := []byte(input)
		got := m.Find(data)
		want := bruteForceMatches(data, m.literals)
		if len(got) != len(want) {
			t.Fatalf("输入 %q 候选数量 = %d, want %d (%v vs %v)", input, len(got), len(want), got, want)
		}
		for i := range got {
			if got[i] != want[i] {
				t.Fatalf("输入 %q 第 %d 个候选 = %+v, want %+v", input, i, got[i], want[i])
			}
		}
	}
}

// TestFlatMatcherKeyCollision 验证主键相同、尾部不同的文字进入同一桶后仍能
// 逐条区分，且共享前缀的更短文字不会被更长文字吞掉。
func TestFlatMatcherKeyCollision(t *testing.T) {
	literals := []hwlm.Literal{
		{ID: 1, Value: []byte("field000=1")},
		{ID: 2, Value: []byte("field000=2")},
		{ID: 3, Value: []byte("field000=1234567")},
		{ID: 4, Value: []byte("field000=1abcdef")},
	}
	m := New(literals)
	if m.flat == nil {
		t.Fatal("预期启用扁平索引")
	}
	if buckets := len(m.flat.buckets); buckets > 1 {
		t.Fatalf("共享 8 字节主键应落在同一桶，实际桶数 = %d", buckets)
	}
	data := []byte("field000=2 field000=1abcdef field000=1234567 field000=1")
	got := m.Find(data)
	want := bruteForceMatches(data, m.literals)
	if len(got) != len(want) {
		t.Fatalf("候选数量 = %d, want %d (%v vs %v)", len(got), len(want), got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("第 %d 个候选 = %+v, want %+v", i, got[i], want[i])
		}
	}
}

// TestFlatTableOpenAddressingLoad 验证开放寻址在高装载下仍能区分全部主键，
// 并覆盖线性探测跨越槽位边界的回绕。
func TestFlatTableOpenAddressingLoad(t *testing.T) {
	literals := make([]hwlm.Literal, 0, 64)
	for value := range 64 {
		literals = append(literals, hwlm.Literal{
			ID:    uint32(value + 1),
			Value: []byte(fmt.Sprintf("field%03d=", value)),
		})
	}
	table := newFlatTable(literals)
	if table == nil {
		t.Fatal("flat table is nil")
	}
	for value := range 64 {
		key := []byte(fmt.Sprintf("field%03d=", value))
		entries := table.bucket(table.keyAt(key, 0))
		if len(entries) == 0 {
			t.Fatalf("主键 field%03d= 没有命中桶", value)
		}
	}
	if entries := table.bucket(table.keyAt([]byte("no-such-"), 0)); len(entries) != 0 {
		t.Fatalf("未知主键命中桶 = %v, want 空", entries)
	}
}

// flatShortLiterals 覆盖 4 字节主键路径：最短文字 4 字节，最长 16 字节，
// 尾部跨越 hi/hi2 两个定长视图。
func flatShortLiterals() []hwlm.Literal {
	return []hwlm.Literal{
		{ID: 1, Value: []byte("abcd")},
		{ID: 2, Value: []byte("abcd" + "\x00" + "\x01")},
		{ID: 3, Value: []byte("abcd" + "\x00" + "\x01" + "efghij")},
		{ID: 4, Value: []byte("abcd" + "\x00" + "\x01" + "efghijkl")},
		{ID: 5, Value: []byte{0xFF, 0xFE, 0xFD, 0xFC, 0x00, 0x01}},
		{ID: 6, Value: []byte("abcdzzzzzzzzzzz")},
	}
}

// flatShortCorpus 构造同时覆盖共享主键、跨 hi/hi2 尾部比较与缓冲区末尾
// 不足一个定长窗口的数据。
func flatShortCorpus() []byte {
	data := []byte("abcd" + "\x00" + "\x01" + "efghijkl abcd" + "\x00" + "\x01" + "efghij abcd" + "\x00" + "\x01" + " abcd abcdzzzzzzzzzzz ")
	return append(data, 0xFF, 0xFE, 0xFD, 0xFC, 0x00, 0x01)
}

// TestFlatShortKeyMatchesNaiveScan 用 4 字节主键覆盖共享主键、跨 hi/hi2 的
// 尾部比较与缓冲区末尾不足一个定长窗口的回退路径。
func TestFlatShortKeyMatchesNaiveScan(t *testing.T) {
	literals := flatShortLiterals()
	table := newFlatTable(literals)
	if table == nil {
		t.Fatal("预期启用扁平索引")
	}
	if table.keyBytes != hwlm.FlatKeyShort {
		t.Fatalf("主键宽度 = %d, want %d", table.keyBytes, hwlm.FlatKeyShort)
	}
	data := flatShortCorpus()
	for from := 0; from < len(data); from++ {
		got := table.appendMatches(nil, data, from)
		want := make([]Match, 0, 2)
		for _, literal := range literals {
			if bytes.HasPrefix(data[from:], literal.Value) {
				want = append(want, Match{ID: literal.ID, From: from, To: from + len(literal.Value)})
			}
		}
		if len(got) != len(want) {
			t.Fatalf("起点 %d 匹配数量 = %d, want %d (%v vs %v)", from, len(got), len(want), got, want)
		}
		for i := range got {
			if got[i] != want[i] {
				t.Fatalf("起点 %d 第 %d 个匹配 = %+v, want %+v", from, i, got[i], want[i])
			}
		}
	}
}

// TestFlatShortKeyMatcherMatchesBruteForce 验证 4 字节主键的匹配器在多 lane
// 掩码下的候选集合与朴素实现一致。
func TestFlatShortKeyMatcherMatchesBruteForce(t *testing.T) {
	m := New(flatShortLiterals())
	if m.flat == nil {
		t.Fatal("预期启用扁平索引")
	}
	if m.flat.keyBytes != hwlm.FlatKeyShort {
		t.Fatalf("主键宽度 = %d, want %d", m.flat.keyBytes, hwlm.FlatKeyShort)
	}
	if m.Lanes() != hwlm.FlatKeyShort {
		t.Fatalf("扁平索引下 lane 数 = %d, want %d", m.Lanes(), hwlm.FlatKeyShort)
	}
	inputs := [][]byte{
		nil,
		[]byte("abcd"),
		[]byte("abcd" + "\x00" + "\x01"),
		[]byte("prefix abcd" + "\x00" + "\x01" + "efghijkl suffix"),
		[]byte("abcd" + "\x00" + "\x01" + "efghi abcd" + "\x00" + "\x01" + "efghij abcd" + "\x00" + "\x01" + "efghijkl"),
		bytes.Repeat([]byte("abcd"), 32),
		[]byte(strings.Repeat("x", 70) + "abcd"),
	}
	for _, data := range inputs {
		got := m.Find(data)
		want := bruteForceMatches(data, m.literals)
		if len(got) != len(want) {
			t.Fatalf("输入 %q 候选数量 = %d, want %d (%v vs %v)", data, len(got), len(want), got, want)
		}
		for i := range got {
			if got[i] != want[i] {
				t.Fatalf("输入 %q 第 %d 个候选 = %+v, want %+v", data, i, got[i], want[i])
			}
		}
	}
}
