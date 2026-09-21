package scankit

import (
	"math/rand/v2"
	"testing"
)

func TestPrefixGuardSets(t *testing.T) {
	cases := []struct {
		name    string
		pattern string
		flags   CompileFlag
		length  int
		allow   string
		reject  string
	}{
		{name: "hex digest", pattern: `\b[0-9A-Fa-f]{32}\b`, length: 32, allow: "0123456789abcdefABCDEF0123456789", reject: "0123456789abcdefABCDEF012345678g"},
		{name: "grouped digits", pattern: `[0-9]{4}(-[0-9]{4}){3}`, length: 19, allow: "1234-5678-9012-3456", reject: "1234-5678-9012-345x"},
		{name: "mobile", pattern: `1[3-9][0-9]{9}`, length: 11, allow: "13800138000", reject: "12800138000"},
		{name: "word boundary", pattern: `\b[0-9]{10}\b`, length: 10, allow: "0123456789", reject: "012345678a"},
		{name: "fixed prefix", pattern: `https?://[a-z]+`, length: 4, allow: "http://host", reject: "htxp://host"},
		{name: "lookahead", pattern: `(?=abc)abc`, length: 3, allow: "abc", reject: "abd"},
		{name: "unbounded class prefix", pattern: `[A-Za-z0-9._/-]+\.go`, length: 1, allow: "a.go", reject: " a.go"},
		{name: "empty body", pattern: `a*`, flags: FlagAllowEmpty, length: 0},
		{name: "optional head", pattern: `a?b`, length: 0},
		{name: "alternate heads", pattern: `(?:ab|cd)e`, length: 0},
		{name: "caseless", pattern: `abc`, flags: FlagCaseless, length: 0},
		{name: "utf8", pattern: `abc`, flags: FlagUTF8, length: 0},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			scanner, err := Compile([]Expression{{Id: 1, Pattern: testCase.pattern, Flags: testCase.flags}})
			if err != nil {
				t.Fatalf("编译 %q 失败: %v", testCase.pattern, err)
			}
			guard := scanner.rules[0].guard
			if testCase.length == 0 {
				if guard != nil {
					t.Fatalf("模式 %q 不应建立前缀约束: %d 个位置", testCase.pattern, len(guard.sets))
				}
				return
			}
			if guard == nil {
				t.Fatalf("模式 %q 应建立 %d 个位置的前缀约束", testCase.pattern, testCase.length)
			}
			if len(guard.sets) != testCase.length {
				t.Fatalf("模式 %q 前缀约束=%d，期望 %d", testCase.pattern, len(guard.sets), testCase.length)
			}
			if !guard.allows([]byte(testCase.allow), 0) {
				t.Fatalf("模式 %q 应接受 %q", testCase.pattern, testCase.allow)
			}
			if guard.allows([]byte(testCase.reject), 0) {
				t.Fatalf("模式 %q 应拒绝 %q", testCase.pattern, testCase.reject)
			}
			// 起点越界时约束不可能满足，必须拒绝而不是越界读取。
			if guard.allows([]byte(testCase.allow[:len(testCase.allow)-1]), 0) && testCase.length > 0 {
				if len(testCase.allow) <= testCase.length-1 {
					t.Fatalf("模式 %q 在数据不足时应拒绝", testCase.pattern)
				}
			}
		})
	}
}

// unguardedScanner 返回关闭前缀字节约束的扫描器副本，用于逐位比对过滤前后的命中。
func unguardedScanner(scanner *Scanner) *Scanner {
	reference := *scanner
	reference.rules = append([]compiledRule(nil), scanner.rules...)
	for index := range reference.rules {
		reference.rules[index].guard = nil
	}
	return &reference
}

func assertGuardAgreesWithUnguardedScan(t *testing.T, scanner *Scanner, corpus [][]byte) {
	t.Helper()
	reference := unguardedScanner(scanner)
	for _, data := range corpus {
		want, err := reference.Scan(data)
		if err != nil {
			t.Fatal(err)
		}
		got, err := scanner.Scan(data)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != len(want) {
			t.Fatalf("语料 %q 命中数量不一致: got=%v want=%v", data, got, want)
		}
		for index := range got {
			if got[index] != want[index] {
				t.Fatalf("语料 %q 第 %d 个命中不一致: got=%v want=%v", data, index, got[index], want[index])
			}
		}
	}
}

// TestPrefixGuardLongestEqualWindow 验证约束窗口取自任意偏移上的最长同集合
// 片段，而不是要求整条约束都是同一集合的重复。
func TestPrefixGuardLongestEqualWindow(t *testing.T) {
	cases := []struct {
		pattern string
		offset  int
		length  int
		allow   string
		reject  string
	}{
		{pattern: `[A-Z][0-9]{8}`, offset: 1, length: 8, allow: "A12345678", reject: "A1234567B"},
		{pattern: `[0-9]{17}[0-9Xx]`, offset: 0, length: 17, allow: "12345678901234567X", reject: "1234567890123456aX"},
		{pattern: `[0-9]{3}-[0-9]{2}-[0-9]{4}`, offset: 7, length: 4, allow: "123-45-6789", reject: "123-45-678x"},
		{pattern: `[0-9a-fA-F]{8}-[0-9a-fA-F]{4}`, offset: 0, length: 8, allow: "0123abcd-ef45", reject: "0123abcg-ef45"},
	}
	for _, testCase := range cases {
		t.Run(testCase.pattern, func(t *testing.T) {
			scanner, err := Compile([]Expression{{Id: 1, Pattern: testCase.pattern}})
			if err != nil {
				t.Fatalf("编译 %q 失败: %v", testCase.pattern, err)
			}
			guard := scanner.rules[0].guard
			if guard == nil {
				t.Fatalf("模式 %q 应建立前缀约束", testCase.pattern)
			}
			_, offset, length, ok := guard.longestEqualWindow()
			if !ok {
				t.Fatalf("模式 %q 应派生出同集合窗口", testCase.pattern)
			}
			if offset != testCase.offset || length != testCase.length {
				t.Fatalf("模式 %q 窗口=(%d,%d)，期望 (%d,%d)", testCase.pattern, offset, length, testCase.offset, testCase.length)
			}
			if guardRun, ok := guardRunFor(0, &scanner.rules[0]); !ok {
				t.Fatalf("模式 %q 应能派生约束枚举起点", testCase.pattern)
			} else if guardRun.offset != testCase.offset || guardRun.length != testCase.length {
				t.Fatalf("模式 %q 约束枚举窗口=(%d,%d)，期望 (%d,%d)", testCase.pattern, guardRun.offset, guardRun.length, testCase.offset, testCase.length)
			}
			if !guard.allows([]byte(testCase.allow), 0) {
				t.Fatalf("模式 %q 应接受 %q", testCase.pattern, testCase.allow)
			}
			if guard.allows([]byte(testCase.reject), 0) {
				t.Fatalf("模式 %q 应拒绝 %q", testCase.pattern, testCase.reject)
			}
		})
	}
}

func TestPrefixGuardAgreesWithUnguardedScan(t *testing.T) {
	patterns := []string{
		`\b[0-9A-Fa-f]{32}\b`,
		`\b[0-9a-fA-F]{40}\b`,
		`[0-9]{4}(-[0-9]{4}){3}`,
		`[0-9A-Fa-f]{2}(:[0-9A-Fa-f]{2}){5}`,
		`1[3-9][0-9]{9}`,
		`[A-Za-z0-9._/-]+\.go`,
		`https?://[A-Za-z0-9.-]+(/[A-Za-z0-9._~:/?#\[\]@!$&'()*+,;=%-]*)?`,
		`[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z`,
		`(?i)abc`,
		`a?b`,
		`[^0-9]{4}`,
	}
	expressions := make([]Expression, 0, len(patterns))
	for index, pattern := range patterns {
		expressions = append(expressions, Expression{Id: uint32(index + 1), Pattern: pattern})
	}
	scanner, err := Compile(expressions)
	if err != nil {
		t.Fatal(err)
	}
	assertGuardAgreesWithUnguardedScan(t, scanner, prefixGuardCorpus())
}

// prefixGuardCorpus 生成确定性噪声加真实样本的语料，覆盖“噪声里到处是候选、
// 只有样本才命中”的场景。
func prefixGuardCorpus() [][]byte {
	rng := rand.New(rand.NewPCG(0x9e3779b97f4a7c15, 0xbf58476d1ce4e5b9))
	const alphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789 ,.;:!?-_ "
	noise := make([]byte, 4096)
	for index := range noise {
		noise[index] = alphabet[rng.IntN(len(alphabet))]
	}
	samples := []string{
		"deadbeefdeadbeefdeadbeefdeadbeef",
		"0123456789abcdef0123456789abcdef01234567",
		"1234-5678-9012-3456",
		"00:11:22:33:44:55",
		"13800138000",
		"main.go",
		"https://example.com/a/b?c=d",
		"2024-01-02T03:04:05Z",
		"ABC",
		"ab",
	}
	corpus := [][]byte{noise}
	for _, sample := range samples {
		corpus = append(corpus, []byte(sample), []byte("prefix "+sample+" suffix"), append(append([]byte(nil), noise[:512]...), append([]byte(" "), sample...)...))
	}
	return corpus
}

// TestPrefixGuardWordBoundaryConstraints 验证首尾 \b 断言只在紧邻必然单词字节
// 的消费位置才折算成相邻字节约束，长度可变的尾部一律放弃推导。
func TestPrefixGuardWordBoundaryConstraints(t *testing.T) {
	cases := []struct {
		name        string
		pattern     string
		flags       CompileFlag
		hasBefore   bool
		hasAfter    bool
		afterOffset int
	}{
		{name: "digits", pattern: `\b[0-9]{10}\b`, hasBefore: true, hasAfter: true, afterOffset: 10},
		{name: "long hex digest", pattern: `\b[0-9a-fA-F]{128}\b`, hasBefore: true, hasAfter: true, afterOffset: 128},
		{name: "short literal", pattern: `\bcat\b`, hasBefore: true, hasAfter: true, afterOffset: 3},
		{name: "variable tail", pattern: `\b[A-Za-z]+\b`, hasBefore: true},
		{name: "leading only", pattern: `\bcat`, hasBefore: true},
		{name: "trailing only", pattern: `cat\b`, hasAfter: true, afterOffset: 3},
		{name: "caseless", pattern: `(?i)\bcat\b`},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			scanner, err := Compile([]Expression{{Id: 1, Pattern: testCase.pattern, Flags: testCase.flags}})
			if err != nil {
				t.Fatalf("编译 %q 失败: %v", testCase.pattern, err)
			}
			guard := scanner.rules[0].guard
			if !testCase.hasBefore && !testCase.hasAfter {
				if guard != nil && guard.hasBoundary() {
					t.Fatalf("模式 %q 不应折算相邻字节约束", testCase.pattern)
				}
				return
			}
			if guard == nil {
				t.Fatalf("模式 %q 应建立前缀约束", testCase.pattern)
			}
			if guard.hasBefore != testCase.hasBefore || guard.hasAfter != testCase.hasAfter {
				t.Fatalf("模式 %q 边界约束=(before=%v, after=%v)，期望 (before=%v, after=%v)",
					testCase.pattern, guard.hasBefore, guard.hasAfter, testCase.hasBefore, testCase.hasAfter)
			}
			if testCase.hasAfter && guard.afterOffset != testCase.afterOffset {
				t.Fatalf("模式 %q afterOffset=%d，期望 %d", testCase.pattern, guard.afterOffset, testCase.afterOffset)
			}
		})
	}
}

// TestPrefixGuardWordBoundaryAllows 验证相邻字节约束的接受与拒绝语义，包含
// 数据两端越界的处理：越界一侧不存在相邻字节，断言视为满足。
func TestPrefixGuardWordBoundaryAllows(t *testing.T) {
	scanner, err := Compile([]Expression{{Id: 1, Pattern: `\b[0-9]{10}\b`}})
	if err != nil {
		t.Fatal(err)
	}
	guard := scanner.rules[0].guard
	if guard == nil || !guard.hasBoundary() {
		t.Fatal("模式应折算相邻字节约束")
	}
	cases := []struct {
		name  string
		data  string
		start int
		allow bool
	}{
		{name: "两边都是分隔符", data: " 0123456789 ", start: 1, allow: true},
		{name: "起点在数据开头", data: "0123456789 ", start: 0, allow: true},
		{name: "结束在数据末尾", data: " 0123456789", start: 1, allow: true},
		{name: "左邻单词字节", data: "x0123456789 ", start: 1, allow: false},
		{name: "右邻单词字节", data: " 0123456789x", start: 1, allow: false},
		{name: "整串都是数字", data: "0123456789", start: 0, allow: true},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			data := []byte(testCase.data)
			if got := guard.allowsBoundary(data, testCase.start); got != testCase.allow {
				t.Fatalf("allowsBoundary(%q, %d)=%v，期望 %v", testCase.data, testCase.start, got, testCase.allow)
			}
			if got := guard.allows(data, testCase.start); got != testCase.allow {
				t.Fatalf("allows(%q, %d)=%v，期望 %v", testCase.data, testCase.start, got, testCase.allow)
			}
		})
	}
}

// TestPrefixGuardBoundaryOnlyPathAgreesWithUnguardedScan 覆盖"约束窗口已覆盖全部
// 前缀字节集合"的展开路径：此时候选起点只再校验首尾断言折算出的相邻字节约束，
// 结果必须与关闭约束的逐起点扫描逐位一致。
func TestPrefixGuardBoundaryOnlyPathAgreesWithUnguardedScan(t *testing.T) {
	patterns := []string{
		`\b[0-9a-fA-F]{32}\b`,
		`\b[0-9a-fA-F]{40}\b`,
		`\b[0-9]{10}\b`,
	}
	expressions := make([]Expression, 0, len(patterns))
	for index, pattern := range patterns {
		expressions = append(expressions, Expression{Id: uint32(index + 1), Pattern: pattern})
	}
	scanner, err := Compile(expressions)
	if err != nil {
		t.Fatal(err)
	}
	for index := range scanner.rules {
		if !scanner.guardRunCovers(index) {
			t.Fatalf("模式 %q 应由前缀字节约束枚举候选起点", patterns[index])
		}
		run, ok := guardRunFor(index, &scanner.rules[index])
		if !ok {
			t.Fatalf("模式 %q 应能派生约束窗口", patterns[index])
		}
		if !run.prefixCovered || !run.guard.hasBoundary() {
			t.Fatalf("模式 %q 应走只校验相邻字节约束的展开路径: prefixCovered=%v hasBoundary=%v",
				patterns[index], run.prefixCovered, run.guard.hasBoundary())
		}
	}
	assertGuardAgreesWithUnguardedScan(t, scanner, boundaryCorpus())
}

// boundaryCorpus 覆盖断言成立与不成立两类窗口：整段都是单词字节的十六进制串
// 内部起点必须被相邻字节约束拒绝，只有整段首尾落在非单词字节上才算命中。
func boundaryCorpus() [][]byte {
	digest := "deadbeefdeadbeefdeadbeefdeadbeef"
	long := "0123456789abcdef0123456789abcdef01234567"
	return [][]byte{
		[]byte(digest),
		[]byte(" " + digest + " "),
		[]byte("x" + digest + " "),
		[]byte(" " + digest + "x"),
		[]byte("0" + digest + "0"),
		[]byte(long),
		[]byte(" " + long + " "),
		[]byte("x" + long + "x"),
		[]byte("call 13800138000 now"),
		[]byte("call 138001380000 now"),
		[]byte("13800138000"),
	}
}
