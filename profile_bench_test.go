package scankit

import (
	"bytes"
	"testing"
)

var profileBenchmarkMatchesSink []Match
var profileBenchmarkBoolSink bool

// BenchmarkProfileScanPaths 为 AC、锚点验证、无锚定 DFA 和事件收集提供可重复的定位入口。
// 它们不构成发布门槛，也不代表任何业务工作负载。
func BenchmarkProfileScanPaths(b *testing.B) {
	for _, test := range []struct {
		name        string
		expressions []Expression
		data        []byte
		want        []Match
	}{
		{
			name: "ACTraversal",
			expressions: []Expression{
				{Id: 1, Pattern: `metric-key-00`},
				{Id: 2, Pattern: `metric-key-01`},
				{Id: 3, Pattern: `metric-key-02`},
				{Id: 4, Pattern: `metric-key-03`},
			},
			data: bytes.Repeat([]byte(`metric-key-xx `), 256),
		},
		{
			name:        "AnchoredVerifier",
			expressions: []Expression{{Id: 1, Pattern: `account=[A-Z]{4}[0-9]{4}`}},
			data:        bytes.Repeat([]byte(`account=ABCD1234 `), 128),
			want:        repeatedProfileMatches(`account=ABCD1234 `, 0, 0, 16, 128, []uint32{1}),
		},
		{
			name:        "UnanchoredVerifierDFA",
			expressions: []Expression{{Id: 1, Pattern: `(?:ab|ac)[0-9]{1,2}`}},
			data:        bytes.Repeat([]byte(`ab12 `), 256),
			want:        repeatedProfileMatches(`ab12 `, 0, 3, 4, 256, []uint32{1}),
		},
		{
			name:        "EventCollection",
			expressions: profileEventCollectionExpressions(),
			data:        bytes.Repeat([]byte(`matched `), 64),
			want:        repeatedProfileMatches(`matched `, 0, 0, 7, 64, profileEventCollectionIDs()),
		},
	} {
		b.Run(test.name, func(b *testing.B) {
			scanner, err := Compile(test.expressions)
			if err != nil {
				b.Fatal(err)
			}
			benchmarkProfileScanInto(b, scanner, test.data, test.want)
		})
	}
}

// BenchmarkProfileSingleByteAnchorWord 将单字节锚点的机器字扫描从业务规则中拆出。
// 它覆盖一个或两个根锚点、无命中、稀疏命中、高密度命中，以及验证器产生未来事件的情形；
// 仅用于 profile 和前后对比，不表示日志脱敏的发布指标。
func BenchmarkProfileSingleByteAnchorWord(b *testing.B) {
	for _, test := range []struct {
		name        string
		expressions []Expression
		data        []byte
	}{
		{
			name:        "OneRoot/NoMatch",
			expressions: []Expression{{Id: 1, Pattern: `a[0-9]{4}`}},
			data:        bytes.Repeat([]byte(`message=payment-completed `), 256),
		},
		{
			name:        "OneRoot/SparseMatch",
			expressions: []Expression{{Id: 1, Pattern: `a[0-9]{4}`}},
			data:        append(bytes.Repeat([]byte(`message=payment-completed `), 255), []byte(`a1234 `)...),
		},
		{
			name:        "OneRoot/HighMatchPending",
			expressions: []Expression{{Id: 1, Pattern: `a[0-9]{4}`}},
			data:        bytes.Repeat([]byte(`a1234 `), 512),
		},
		{
			name: "TwoRoots/NoMatch",
			expressions: []Expression{
				{Id: 1, Pattern: `a[0-9]{4}`},
				{Id: 2, Pattern: `b[0-9]{4}`},
			},
			data: bytes.Repeat([]byte(`message=payment-completed `), 256),
		},
		{
			name: "TwoRoots/HighMatchPending",
			expressions: []Expression{
				{Id: 1, Pattern: `a[0-9]{4}`},
				{Id: 2, Pattern: `b[0-9]{4}`},
			},
			data: bytes.Repeat([]byte(`a1234 b5678 `), 256),
		},
	} {
		b.Run(test.name, func(b *testing.B) {
			scanner, err := Compile(test.expressions)
			if err != nil {
				b.Fatal(err)
			}
			if !scanner.singleByteFast {
				b.Fatal("expected single-byte anchor fast path")
			}
			want, err := scanner.Scan(test.data)
			if err != nil {
				b.Fatal(err)
			}
			benchmarkProfileScanInto(b, scanner, test.data, want)
		})
	}
}

// BenchmarkProfileRootByteAnchoredWord 隔离多字节字面量锚点的根字节跳过与 verifier
// 成本。它覆盖无根字节、高密度触发但验证失败，以及高密度有效匹配三种调度形态。
func BenchmarkProfileRootByteAnchoredWord(b *testing.B) {
	for _, test := range []struct {
		name string
		data []byte
	}{
		{
			name: "NoRootByte",
			data: bytes.Repeat([]byte(`message=payment-completed `), 256),
		},
		{
			name: "CandidateRejected",
			data: bytes.Repeat([]byte(`Xacct=ABCDxxxx `), 256),
		},
		{
			name: "HighMatch",
			data: bytes.Repeat([]byte(`Xacct=ABCD1234 `), 256),
		},
	} {
		b.Run(test.name, func(b *testing.B) {
			scanner, err := Compile([]Expression{{Id: 1, Pattern: `[A-Z]{1,8}acct=[A-Z]{4}[0-9]{4}`}})
			if err != nil {
				b.Fatal(err)
			}
			if !scanner.automaton.rootByteFast || scanner.singleByteOnly {
				b.Fatal("expected multi-byte root-anchor fast path")
			}
			want, err := scanner.Scan(test.data)
			if err != nil {
				b.Fatal(err)
			}
			b.Run("PrefixDFA", func(b *testing.B) {
				benchmarkProfileScanInto(b, scanner, test.data, want)
			})
			b.Run("FullVerifier", func(b *testing.B) {
				benchmarkProfileScanInto(b, withoutRootByteAnchoredPrefixDFA(scanner), test.data, want)
			})
		})
	}
}

// BenchmarkProfileFixedByteRegex 隔离固定宽度正则的候选分派与逐类验证成本。CreditCard
// 规则没有公共字面量锚点，正是该通道的典型输入。
func BenchmarkProfileFixedByteRegex(b *testing.B) {
	for _, test := range []struct {
		name string
		data []byte
	}{
		{
			name: "NoCandidate",
			data: bytes.Repeat([]byte(`card=xxxxxxxxxxxxxxxx `), 256),
		},
		{
			name: "CandidateRejected",
			data: bytes.Repeat([]byte(`card=4xxxxxxxxxxxxxxx `), 256),
		},
		{
			name: "HighMatch",
			data: bytes.Repeat([]byte(`card=4111111111111111 `), 256),
		},
	} {
		b.Run(test.name, func(b *testing.B) {
			scanner, err := Compile([]Expression{{Id: 1, Pattern: `4[0-9]{15}|5[1-5][0-9]{14}|3[47][0-9]{13}`}})
			if err != nil {
				b.Fatal(err)
			}
			if profileFixedByteRegexLaneCount(scanner) == 0 {
				b.Fatal("expected fixed-width scan plan")
			}
			want, err := scanner.Scan(test.data)
			if err != nil {
				b.Fatal(err)
			}
			benchmarkProfileScanInto(b, scanner, test.data, want)
		})
	}
}

// BenchmarkProfileFixedAnchor 将超过定宽展开上限时的 class-anchor 通道单独暴露出来。
// NoCandidate、CandidateRejected 与 HighMatch 分别定位触发类未命中、必要条件拒绝和
// 完整 verifier 接受的成本；规则仅描述通用定宽字节结构。
func BenchmarkProfileFixedAnchor(b *testing.B) {
	const pattern = `(?:a[0-9]|b[0-9]|c[0-9]|d[0-9]){4}`
	for _, test := range []struct {
		name string
		data []byte
	}{
		{
			name: "NoCandidate",
			data: bytes.Repeat([]byte(`zzzzzzzzz `), 256),
		},
		{
			name: "CandidateRejected",
			data: bytes.Repeat([]byte(`aXbXcXdX `), 256),
		},
		{
			name: "HighMatch",
			data: bytes.Repeat([]byte(`a0b1c2d3 `), 256),
		},
	} {
		b.Run(test.name, func(b *testing.B) {
			scanner, err := Compile([]Expression{{Id: 1, Pattern: pattern}})
			if err != nil {
				b.Fatal(err)
			}
			if profileFixedByteRegexAnchorLaneCount(scanner) == 0 {
				b.Fatal("expected fixed-anchor scan plan")
			}
			want, err := scanner.Scan(test.data)
			if err != nil {
				b.Fatal(err)
			}
			benchmarkProfileScanInto(b, scanner, test.data, want)
		})
	}
}

// BenchmarkProfileFixedAnchorChecks 对比单个与多个必要条件的过滤成本。它直接测量
// 已编译 anchor 的检查函数，以免 AC、NFA/DFA 与事件投递掩盖这一小段热路径。
func BenchmarkProfileFixedAnchorChecks(b *testing.B) {
	root, err := parseRegex(`(?:a[0-9]|b[0-9]|c[0-9]|d[0-9]){4}`)
	if err != nil {
		b.Fatal(err)
	}
	anchor, ok := extractFixedByteRegexAnchor(root)
	if !ok || anchor.checks == nil || anchor.checks.count < 2 {
		b.Fatal("expected multiple fixed-anchor checks")
	}
	oneCheck := *anchor.checks
	oneCheck.count = 1
	for _, test := range []struct {
		name   string
		data   []byte
		checks *fixedByteRegexAnchorChecks
	}{
		{name: "FirstCheckReject/OneCheck", data: []byte(`aXbXcXdX`), checks: &oneCheck},
		{name: "FirstCheckReject/MultipleChecks", data: []byte(`aXbXcXdX`), checks: anchor.checks},
		{name: "AllPass/OneCheck", data: []byte(`a0b1c2d3`), checks: &oneCheck},
		{name: "AllPass/MultipleChecks", data: []byte(`a0b1c2d3`), checks: anchor.checks},
	} {
		b.Run(test.name, func(b *testing.B) {
			if !fixedByteRegexAnchorChecksMatch([]byte(`a0b1c2d3`), 0, test.checks) {
				b.Fatal("valid fixed anchor did not pass checks")
			}
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				profileBenchmarkBoolSink = fixedByteRegexAnchorChecksMatch(test.data, 0, test.checks)
			}
		})
	}
	for _, test := range []struct {
		name string
		data []byte
	}{
		{name: "FirstCheckReject", data: []byte(`aXbXcXdX`)},
		{name: "AllPass", data: []byte(`a0b1c2d3`)},
	} {
		b.Run(test.name+"/PointerAccess", func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				profileBenchmarkBoolSink = fixedByteRegexAnchorChecksMatch(test.data, 0, anchor.checks)
			}
		})
		b.Run(test.name+"/ValueCopyReference", func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				profileBenchmarkBoolSink = profileFixedByteRegexAnchorChecksMatchCopy(test.data, 0, anchor.checks)
			}
		})
	}
}

// profileFixedByteRegexAnchorChecksMatchCopy 是仅用于微基准的旧值复制参考实现，不能被
// 扫描热路径调用。
func profileFixedByteRegexAnchorChecksMatchCopy(data []byte, start int, checks *fixedByteRegexAnchorChecks) bool {
	for index := 0; index < int(checks.count); index++ {
		check := checks.values[index]
		if !check.class.contains(data[start+check.offset]) {
			return false
		}
	}
	return true
}

func profileFixedByteRegexLaneCount(scanner *Scanner) int {
	count := 0
	for _, lanes := range scanner.blockScanPlan.unanchored.fixed {
		count += len(lanes)
	}
	return count
}

func profileFixedByteRegexAnchorLaneCount(scanner *Scanner) int {
	count := 0
	for _, lanes := range scanner.blockScanPlan.unanchored.fixedAnchor {
		count += len(lanes)
	}
	return count
}

// BenchmarkProfileMixedPII 聚合五类 PII 规则，供 profile 区分 AC、锚定验证、固定宽度
// 分派和事件投递的成本。它不替代外部包中的端到端脱敏基准。
func BenchmarkProfileMixedPII(b *testing.B) {
	expressions := []Expression{
		{Id: 1, Pattern: `1[3-9][0-9]{9}`},
		{Id: 2, Pattern: `[A-Za-z0-9._%+-]{1,64}@[A-Za-z0-9-]+(?:\.[A-Za-z0-9-]+)+\b`},
		{Id: 3, Pattern: `[1-9][0-9]{5}(18|19|20)[0-9]{2}(0[1-9]|1[0-2])(0[1-9]|[12][0-9]|3[01])[0-9]{3}[0-9Xx]`},
		{Id: 4, Pattern: `62[0-9]{14,17}`},
		{Id: 5, Pattern: `4[0-9]{15}|5[1-5][0-9]{14}|3[47][0-9]{13}`},
	}
	for _, test := range []struct {
		name string
		data []byte
	}{
		{
			name: "NoMatch",
			data: bytes.Repeat([]byte(`服务=支付网关 level=INFO event=payment-completed audit=日志脱敏\n`), 128),
		},
		{
			name: "HighMatch",
			data: bytes.Repeat([]byte(`mobile=13800138000 email=alice@example.com identity=11010520000101002X bank=6222021234567890 card=4111111111111111\n`), 128),
		},
	} {
		b.Run(test.name, func(b *testing.B) {
			scanner, err := Compile(expressions)
			if err != nil {
				b.Fatal(err)
			}
			want, err := scanner.Scan(test.data)
			if err != nil {
				b.Fatal(err)
			}
			benchmarkProfileScanInto(b, scanner, test.data, want)
		})
	}
}

func benchmarkProfileScanInto(b *testing.B, scanner *Scanner, data []byte, want []Match) {
	b.Helper()
	matches := make([]Match, 0, len(want))
	got, err := scanner.ScanInto(data, matches)
	if err != nil {
		b.Fatal(err)
	}
	if !equalMatches(got, want) {
		b.Fatalf("ScanInto() = %#v, want %#v", got, want)
	}
	b.SetBytes(int64(len(data)))
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		matches = matches[:0]
		matches, err = scanner.ScanInto(data, matches)
		if err != nil {
			b.Fatal(err)
		}
	}
	profileBenchmarkMatchesSink = matches
}

func repeatedProfileMatches(record string, from, minimumTo, maximumTo, count int, ids []uint32) []Match {
	matches := make([]Match, 0, count*len(ids))
	for index := range count {
		base := index * len(record)
		for _, id := range ids {
			if minimumTo != 0 {
				matches = append(matches, Match{Id: id, From: uint64(base + from), To: uint64(base + minimumTo)})
			}
			matches = append(matches, Match{Id: id, From: uint64(base + from), To: uint64(base + maximumTo)})
		}
	}
	return matches
}

func profileEventCollectionExpressions() []Expression {
	expressions := make([]Expression, 32)
	for index := range expressions {
		expressions[index] = Expression{Id: uint32(index + 1), Pattern: `matched`}
	}
	return expressions
}

func profileEventCollectionIDs() []uint32 {
	ids := make([]uint32, 32)
	for index := range ids {
		ids[index] = uint32(index + 1)
	}
	return ids
}
