package scankit

import (
	"bytes"
	"testing"
)

// BenchmarkUnboundedDigitRepeat 定位 \d+ 的无锚定连续字符类扫描成本。
func BenchmarkUnboundedDigitRepeat(b *testing.B) {
	benchmarkUnboundedRegex(b, unboundedRegexBenchmark{
		pattern:      `\d+`,
		noMatchRow:   "服务=订单中心 状态=成功\n",
		matchRow:     "服务=订单中心 code=2026 状态=成功\n",
		alwaysLanes:  1,
		simpleRepeat: true,
	})
}

// BenchmarkUnboundedLetterRepeat 定位 [a-z]+ 的无锚定连续字符类扫描成本。
func BenchmarkUnboundedLetterRepeat(b *testing.B) {
	benchmarkUnboundedRegex(b, unboundedRegexBenchmark{
		pattern:      `[a-z]+`,
		noMatchRow:   "服务=订单中心，状态=成功。\n",
		matchRow:     "service=payment completed\n",
		alwaysLanes:  1,
		simpleRepeat: true,
	})
}

// BenchmarkUnboundedDotStarLiteral 定位 .*error 的可变前缀与字面量后缀扫描成本。
func BenchmarkUnboundedDotStarLiteral(b *testing.B) {
	benchmarkUnboundedRegex(b, unboundedRegexBenchmark{
		pattern:    `.*error`,
		noMatchRow: "service=payment completed\n",
		matchRow:   "service=payment error\n",
	})
}

// BenchmarkPrefixRepeatLeadingByte 比较相同 prefix-repeat 结构在不同前导字节频率下的
// 成本。三个规则共享同一个混合调度形状；差异只来自输入中 a、m、z 的出现频率。
func BenchmarkPrefixRepeatLeadingByte(b *testing.B) {
	data := prefixRepeatMixedBenchmarkData()
	for _, prefix := range []byte{'a', 'm', 'z'} {
		pattern := "[" + string(prefix) + `][a-z]{9,}`
		b.Run(string(prefix), func(b *testing.B) {
			scanner, err := Compile(prefixRepeatMixedExpressions(pattern))
			if err != nil {
				b.Fatal(err)
			}
			if !scanner.blockScanPlan.mixed.wordScan || !scanner.blockScanPlan.mixed.prefixOnly || scanner.blockScanPlan.mixed.prefixByte != prefix {
				b.Fatalf("prefix %q did not select the mixed prefix-repeat path: %#v", prefix, scanner.blockScanPlan.mixed)
			}
			want, err := scanner.Scan(data)
			if err != nil {
				b.Fatal(err)
			}
			benchmarkProfileScanInto(b, scanner, data, want)
		})
	}
}

type unboundedRegexBenchmark struct {
	pattern      string
	noMatchRow   string
	matchRow     string
	alwaysLanes  int
	simpleRepeat bool
}

func benchmarkUnboundedRegex(b *testing.B, test unboundedRegexBenchmark) {
	b.Helper()
	scanner, err := Compile([]Expression{{Id: 1, Pattern: test.pattern}})
	if err != nil {
		b.Fatal(err)
	}
	if got := len(scanner.blockScanPlan.unanchored.always); got != test.alwaysLanes {
		b.Fatalf("always lane count = %d, want %d", got, test.alwaysLanes)
	}
	for _, density := range []struct {
		name     string
		hasMatch func(int) bool
	}{
		{name: "NoMatch", hasMatch: func(int) bool { return false }},
		{name: "LowMatch", hasMatch: func(index int) bool { return index%128 == 0 }},
		{name: "HighMatch", hasMatch: func(index int) bool { return index%2 == 0 }},
	} {
		b.Run(density.name, func(b *testing.B) {
			data := unboundedRegexBenchmarkData(test.noMatchRow, test.matchRow, density.hasMatch)
			want, err := scanner.Scan(data)
			if err != nil {
				b.Fatal(err)
			}
			benchmarkProfileScanInto(b, scanner, data, want)
		})
	}
}

func unboundedRegexBenchmarkData(noMatchRow, matchRow string, hasMatch func(int) bool) []byte {
	var data bytes.Buffer
	data.Grow(256 * len(matchRow))
	for index := 0; index < 256; index++ {
		if hasMatch(index) {
			data.WriteString(matchRow)
			continue
		}
		data.WriteString(noMatchRow)
	}
	return data.Bytes()
}

func TestUnboundedRegexBenchmarkFixtures(t *testing.T) {
	for _, test := range []unboundedRegexBenchmark{
		{pattern: `\d+`, noMatchRow: "服务=订单中心 状态=成功\n", matchRow: "服务=订单中心 code=2026 状态=成功\n", alwaysLanes: 1, simpleRepeat: true},
		{pattern: `[a-z]+`, noMatchRow: "服务=订单中心，状态=成功。\n", matchRow: "service=payment completed\n", alwaysLanes: 1, simpleRepeat: true},
		{pattern: `.*error`, noMatchRow: "service=payment completed\n", matchRow: "service=payment error\n"},
	} {
		t.Run(test.pattern, func(t *testing.T) {
			scanner, err := Compile([]Expression{{Id: 1, Pattern: test.pattern}})
			if err != nil {
				t.Fatal(err)
			}
			if got := len(scanner.blockScanPlan.unanchored.always); got != test.alwaysLanes {
				t.Fatalf("always lane count = %d, want %d", got, test.alwaysLanes)
			}
			if scanner.simpleRepeatOnly != test.simpleRepeat {
				t.Fatalf("simple repeat path = %t, want %t", scanner.simpleRepeatOnly, test.simpleRepeat)
			}
			reference := *scanner
			reference.simpleRepeatOnly = false
			for _, data := range [][]byte{
				unboundedRegexBenchmarkData(test.noMatchRow, test.matchRow, func(int) bool { return false }),
				unboundedRegexBenchmarkData(test.noMatchRow, test.matchRow, func(index int) bool { return index%128 == 0 }),
				unboundedRegexBenchmarkData(test.noMatchRow, test.matchRow, func(index int) bool { return index%2 == 0 }),
			} {
				want, err := reference.ScanInto(data, nil)
				if err != nil {
					t.Fatal(err)
				}
				got, err := scanner.ScanInto(data, make([]Match, 0, len(want)))
				if err != nil {
					t.Fatal(err)
				}
				if !equalMatches(got, want) {
					t.Fatalf("ScanInto() = %#v, want %#v", got, want)
				}
			}
		})
	}
}

func TestSimpleRepeatOnlyBlockScanPreservesGenericSemantics(t *testing.T) {
	for _, test := range []struct {
		name        string
		expressions []Expression
		data        []byte
	}{
		{
			name:        "unbounded digits",
			expressions: []Expression{{Id: 1, Pattern: `\d+`}},
			data:        []byte("文字 12 345678 x9"),
		},
		{
			name:        "minimum length letters",
			expressions: []Expression{{Id: 1, Pattern: `[a-z]{2,}`}},
			data:        []byte("中文 ab abcdef gh"),
		},
		{
			name:        "word boundary",
			expressions: []Expression{{Id: 1, Pattern: `\b\d+\b`}},
			data:        []byte("x12 34 567 y"),
		},
		{
			name: "constraints and single match",
			expressions: []Expression{{
				Id:      1,
				Pattern: `\d+`,
				Flags:   CompileSingleMatch,
				Ext:     &ExpressionExt{Flags: ExtMinOffset, MinOffset: 4},
			}},
			data: []byte("12 345 678"),
		},
		{
			name: "combination",
			expressions: []Expression{
				{Id: 1, Pattern: `\d+`},
				{Id: 2, Pattern: `1`, Flags: CompileCombination},
			},
			data: []byte("12 345"),
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			scanner, err := Compile(test.expressions)
			if err != nil {
				t.Fatal(err)
			}
			if !scanner.simpleRepeatOnly {
				t.Fatal("simple repeat fixture did not select the dedicated path")
			}
			reference := *scanner
			reference.simpleRepeatOnly = false
			want, err := reference.ScanInto(test.data, nil)
			if err != nil {
				t.Fatal(err)
			}
			got, err := scanner.ScanInto(test.data, nil)
			if err != nil {
				t.Fatal(err)
			}
			if !equalMatches(got, want) {
				t.Fatalf("ScanInto() = %#v, generic ScanInto() = %#v", got, want)
			}
		})
	}
}

func TestPrefixRepeatScanPreservesGenericSemantics(t *testing.T) {
	for _, prefix := range []byte{'a', 'm', 'z'} {
		pattern := "[" + string(prefix) + `][a-z]{9,}`
		t.Run(string(prefix), func(t *testing.T) {
			optimized, err := Compile([]Expression{{Id: 1, Pattern: pattern}})
			if err != nil {
				t.Fatal(err)
			}
			if !optimized.blockScanPlan.unanchored.hasPrefixRepeatLanes() {
				t.Fatal("prefix-repeat fixture did not select the dedicated lane")
			}
			reference, err := Compile([]Expression{{Id: 1, Pattern: "(?:" + pattern + `){1}`}})
			if err != nil {
				t.Fatal(err)
			}
			for _, data := range [][]byte{
				[]byte(string(prefix) + "redaction"),
				[]byte(string(prefix) + string(prefix) + "redaction " + string(prefix) + "abcdefghijklmnop"),
				[]byte(string(prefix) + "short " + string(prefix) + "redaction\n" + string(prefix) + "redaction"),
				[]byte("服务=支付 token=" + string(prefix) + "redaction message=同步完成"),
			} {
				assertPrefixRepeatMatchesReference(t, optimized, reference, data)
			}
		})
	}
}

func TestMixedPrefixRepeatLeadingBytePreservesGenericSemantics(t *testing.T) {
	data := prefixRepeatMixedBenchmarkData()
	for _, prefix := range []byte{'a', 'm', 'z'} {
		pattern := "[" + string(prefix) + `][a-z]{9,}`
		t.Run(string(prefix), func(t *testing.T) {
			scanner, err := Compile(prefixRepeatMixedExpressions(pattern))
			if err != nil {
				t.Fatal(err)
			}
			if !scanner.blockScanPlan.mixed.wordScan || !scanner.blockScanPlan.mixed.prefixOnly || scanner.blockScanPlan.mixed.prefixByte != prefix {
				t.Fatalf("mixed prefix-repeat path = %#v, want prefix %q", scanner.blockScanPlan.mixed, prefix)
			}
			reference := *scanner
			reference.blockScanPlan.mixed.wordScan = false
			assertPrefixRepeatMatchesReference(t, scanner, &reference, data)
		})
	}
}

func TestMixedPrefixRepeatWithoutLeadingByteUsesRangeFallback(t *testing.T) {
	scanner, err := Compile(prefixRepeatMixedExpressions(`[z][a-z]{9,}`))
	if err != nil {
		t.Fatal(err)
	}
	if !scanner.blockScanPlan.mixed.wordScan || !scanner.blockScanPlan.mixed.prefixOnly || scanner.blockScanPlan.mixed.prefixByte != 'z' {
		t.Fatalf("mixed prefix plan = %#v", scanner.blockScanPlan.mixed)
	}
	// 输入包含普通范围和 @ 候选，但没有 z；快路径应安全退回范围加单离散字节循环。
	data := []byte("service=payment code=100001 contact=user@example.com status=completed\n")
	reference := *scanner
	reference.blockScanPlan.mixed.prefixOnly = false
	got, err := scanner.ScanInto(data, nil)
	if err != nil {
		t.Fatal(err)
	}
	want, err := reference.ScanInto(data, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !equalMatches(got, want) {
		t.Fatalf("ScanInto() = %#v, generic range path = %#v", got, want)
	}
}

func assertPrefixRepeatMatchesReference(t *testing.T, scanner, reference *Scanner, data []byte) {
	t.Helper()
	got, err := scanner.ScanInto(data, nil)
	if err != nil {
		t.Fatal(err)
	}
	want, err := reference.ScanInto(data, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !equalMatches(got, want) {
		t.Fatalf("ScanInto(%q) = %#v, reference = %#v", data, got, want)
	}
}

func prefixRepeatMixedExpressions(pattern string) []Expression {
	return []Expression{
		{Id: 1, Pattern: `1[0-9]{5}`},
		{Id: 2, Pattern: `2[0-9]{5}`},
		{Id: 3, Pattern: `3[0-9]{5}`},
		{Id: 4, Pattern: `@`},
		{Id: 5, Pattern: pattern},
	}
}

func prefixRepeatMixedBenchmarkData() []byte {
	row := []byte("ts=2026-08-27 service=payment-gateway message=acknowledgment management " +
		"code=100001 contact=user@example.com token=zredaction status=completed\n")
	return bytes.Repeat(row, 256)
}

func FuzzUnboundedRegexScanInto(f *testing.F) {
	patterns := []struct {
		pattern   string
		reference string
	}{
		{pattern: `\d+`},
		{pattern: `[a-z]+`},
		{pattern: `.*error`, reference: `(?:.*){1}error`},
		{pattern: `[a][a-z]{9,}`, reference: `(?:[a][a-z]{9,}){1}`},
		{pattern: `[m][a-z]{9,}`, reference: `(?:[m][a-z]{9,}){1}`},
		{pattern: `[z][a-z]{9,}`, reference: `(?:[z][a-z]{9,}){1}`},
	}
	for _, seed := range [][]byte{
		[]byte("服务=订单中心 code=2026 状态=成功\n"),
		[]byte("service=payment completed\n"),
		[]byte("service=payment error\n"),
		{0, 'e', 'r', 'r', 'o', 'r', 0xff},
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 4_096 {
			t.Skip()
		}
		for _, test := range patterns {
			scanner, err := Compile([]Expression{{Id: 1, Pattern: test.pattern}})
			if err != nil {
				t.Fatal(err)
			}
			reference := scanner
			if test.reference != "" {
				reference, err = Compile([]Expression{{Id: 1, Pattern: test.reference}})
				if err != nil {
					t.Fatal(err)
				}
			} else {
				// 简单重复规则必须绕过仅含该规则的专用扫描循环，才能与通用 always-lane
				// 调度交叉验证。两条路径共享语言 IR，但事件推进方式不同。
				generic := *scanner
				generic.simpleRepeatOnly = false
				reference = &generic
			}
			want, err := reference.ScanInto(data, nil)
			if err != nil {
				t.Fatal(err)
			}
			got, err := scanner.ScanInto(data, make([]Match, 0, len(want)))
			if err != nil {
				t.Fatal(err)
			}
			if !equalMatches(got, want) {
				t.Fatalf("pattern %q: ScanInto() = %#v, want %#v", test.pattern, got, want)
			}
		}
	})
}
