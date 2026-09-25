// bench_test：性能基准
//
// 覆盖 PII 语料（单条规则与 100 条规则集两种形态）的吞吐基准，并保留稳定基准（Stable）供同机对比。
// BenchmarkFixedCorpusScan 用 internal/dispatch 的 SetBackendOverride 对比通用后端与
// 主机后端（公开 API 无此开关），其余基准只调用 scankit 公开 API。
// 由原根目录的 pii_log_bench_test.go、pii_rules100_bench_test.go、perf_baseline_test.go 合并而成。

package tests

import (
	"bytes"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/smartwalle/scankit"
	"github.com/smartwalle/scankit/internal/dispatch"
	"github.com/smartwalle/scankit/internal/simd"
)

// ---- 以下用例来自原根目录的 pii_log_bench_test.go ----

const piiBenchmarkRecordCount = 128

var (
	piiBenchmarkBytesSink   []byte
	piiBenchmarkMatchesSink []scankit.Match
)

// BenchmarkPIIRedaction 衡量 Block 日志脱敏链路中的扫描与替换操作。
// 所有场景均使用相同结构的固定中英文合成日志。
func BenchmarkPIIRedaction(b *testing.B) {
	for _, scenario := range piiBenchmarkScenarios() {
		b.Run(scenario.name, func(b *testing.B) {
			for _, density := range piiBenchmarkDensities() {
				b.Run(density.name, func(b *testing.B) {
					fixture := newPIIBenchmarkFixture(b, scenario, density)

					//b.Run("ScannerScanInto", func(b *testing.B) {
					//	matches := make([]scankit.Match, 0, len(fixture.matches))
					//	if _, err := fixture.scanner.ScanInto(fixture.data, matches); err != nil {
					//		b.Fatal(err)
					//	}
					//	startPIIBenchmarkTimer(b, fixture)
					//	for range b.N {
					//		matches = matches[:0]
					//		var err error
					//		matches, err = fixture.scanner.ScanInto(fixture.data, matches)
					//		if err != nil {
					//			b.Fatal(err)
					//		}
					//	}
					//	piiBenchmarkMatchesSink = matches
					//})

					//b.Run("EngineReplace", func(b *testing.B) {
					//	if _, err := fixture.engine.Replace(fixture.data, writePIIMask); err != nil {
					//		b.Fatal(err)
					//	}
					//	startPIIBenchmarkTimer(b, fixture)
					//	for range b.N {
					//		result, err := fixture.engine.Replace(fixture.data, writePIIMask)
					//		if err != nil {
					//			b.Fatal(err)
					//		}
					//		piiBenchmarkBytesSink = result
					//	}
					//})

					b.Run("EngineMask", func(b *testing.B) {
						data := make([]byte, len(fixture.data))
						copy(data, fixture.data)
						if err := fixture.engine.Mask(data, maskPIIValue); err != nil {
							b.Fatal(err)
						}
						startPIIBenchmarkTimer(b, fixture)
						for range b.N {
							copy(data, fixture.data)
							if err := fixture.engine.Mask(data, maskPIIValue); err != nil {
								b.Fatal(err)
							}
							piiBenchmarkBytesSink = data
						}
					})

					b.Run("GoRegexpReplace", func(b *testing.B) {
						if result := fixture.goRegexp.ReplaceAllFunc(fixture.data, fixture.maskRegexpMatch); !bytes.Equal(result, fixture.masked) {
							b.Fatal("Go regexp replacement does not match the verified masked output")
						}
						startPIIBenchmarkTimer(b, fixture)
						for range b.N {
							piiBenchmarkBytesSink = fixture.goRegexp.ReplaceAllFunc(fixture.data, fixture.maskRegexpMatch)
						}
					})
				})
			}
		})
	}
}

type piiBenchmarkScenario struct {
	name        string
	expressions []scankit.Expression
}

type piiBenchmarkDensity struct {
	name          string
	expectedHits  int
	hasMatchAtRow func(int) bool
}

func piiBenchmarkDensities() []piiBenchmarkDensity {
	return []piiBenchmarkDensity{
		{name: "NoMatch", expectedHits: 0, hasMatchAtRow: func(int) bool { return false }},
		{name: "LowMatch", expectedHits: 2, hasMatchAtRow: func(index int) bool { return index%64 == 0 }},
		{name: "HighMatch", expectedHits: 64, hasMatchAtRow: func(index int) bool { return index%2 == 0 }},
	}
}

func piiBenchmarkScenarios() []piiBenchmarkScenario {
	return []piiBenchmarkScenario{
		{
			name:        "Phone1",
			expressions: []scankit.Expression{{Id: 1, Pattern: logChinesePhonePattern1}},
		},
		{
			name:        "Phone2",
			expressions: []scankit.Expression{{Id: 1, Pattern: logChinesePhonePattern2}},
		},
		{
			name:        "Phone3",
			expressions: []scankit.Expression{{Id: 1, Pattern: logChinesePhonePattern3}},
		},
		{
			name:        "Email",
			expressions: []scankit.Expression{{Id: 1, Pattern: logEmailPattern}},
		},
		{
			name:        "ChineseID",
			expressions: []scankit.Expression{{Id: 1, Pattern: logChineseIDPattern}},
		},
		{
			name:        "BankCard",
			expressions: []scankit.Expression{{Id: 1, Pattern: logBankCardPattern}},
		},
		{
			name:        "CreditCard",
			expressions: []scankit.Expression{{Id: 1, Pattern: logCreditCardPattern}},
		},
		{
			name:        "AllPIITypes",
			expressions: logPIIMixedExpressions(),
		},
	}
}

type piiBenchmarkFixture struct {
	data            []byte
	matches         []scankit.Match
	masked          []byte
	maskBytes       []byte
	scanner         *scankit.Scanner
	engine          *scankit.Engine
	goRegexp        *regexp.Regexp
	maskRegexpMatch func([]byte) []byte
}

func newPIIBenchmarkFixture(b testing.TB, scenario piiBenchmarkScenario, density piiBenchmarkDensity) piiBenchmarkFixture {
	b.Helper()
	data := newPIIBenchmarkLog(scenario, density)
	scanner, err := scankit.Compile(scenario.expressions)
	if err != nil {
		b.Fatal(err)
	}
	goRegexp, err := regexp.Compile(piiBenchmarkAlternation(scenario.expressions))
	if err != nil {
		b.Fatal(err)
	}
	engine, err := scankit.New(scenario.expressions)
	if err != nil {
		b.Fatal(err)
	}
	expected := piiBenchmarkExpectedMatches(scenario.expressions, data)
	if want := density.expectedHits * len(scenario.expressions); len(expected) != want {
		b.Fatalf("%s/%s expected match count = %d, want %d", scenario.name, density.name, len(expected), want)
	}
	matches, err := scanner.ScanInto(data, make([]scankit.Match, 0, len(expected)))
	if err != nil {
		b.Fatal(err)
	}
	if !slices.Equal(matches, expected) {
		b.Fatalf("Scanner matches = %#v, want %#v", matches, expected)
	}

	maskBytes := bytes.Repeat([]byte{'*'}, len(data))
	fixture := piiBenchmarkFixture{
		data:      data,
		matches:   expected,
		maskBytes: maskBytes,
		scanner:   scanner,
		engine:    engine,
		goRegexp:  goRegexp,
	}
	fixture.maskRegexpMatch = func(match []byte) []byte {
		return fixture.maskBytes[:len(match)]
	}

	fixture.masked, err = engine.Replace(data, writePIIMask)
	if err != nil {
		b.Fatal(err)
	}
	maskedInput := append([]byte(nil), data...)
	if err := engine.Mask(maskedInput, maskPIIValue); err != nil {
		b.Fatal(err)
	}
	if !bytes.Equal(maskedInput, fixture.masked) {
		b.Fatal("Engine Mask does not match Engine Replace")
	}
	if replaced := goRegexp.ReplaceAllFunc(data, fixture.maskRegexpMatch); !bytes.Equal(replaced, fixture.masked) {
		b.Fatal("Go regexp replacement does not match Engine Replace")
	}
	return fixture
}

func newPIIBenchmarkLog(scenario piiBenchmarkScenario, density piiBenchmarkDensity) []byte {
	var builder strings.Builder
	builder.Grow(piiBenchmarkRecordCount * 300)
	for index := range piiBenchmarkRecordCount {
		payload := piiBenchmarkNeutralPayload(index)
		if density.hasMatchAtRow(index) {
			payload = strings.Join(piiBenchmarkFields(piiBenchmarkScenarioType(scenario.name), index, true), " ")
		} else if index%4 == 0 {
			payload = strings.Join(piiBenchmarkFields(piiBenchmarkScenarioType(scenario.name), index, false), " ")
		}
		switch index % 4 {
		case 0:
			fmt.Fprintf(&builder, "{\"ts\":\"2026-08-25T10:30:00+08:00\",\"level\":\"INFO\",\"service\":\"payment\",\"用户\":\"张三\",\"message\":\"支付完成 payment completed\",\"context\":\"%s\"}\n", payload)
		case 1:
			fmt.Fprintf(&builder, "ts=2026-08-25T10:30:00+08:00 level=WARN service=order %s 服务=订单中心 user=alice message=库存不足 inventory retry\n", payload)
		case 2:
			fmt.Fprintf(&builder, "10.12.0.%d - - [25/Aug/2026:10:30:00 +0800] \"POST /api/v1/pay HTTP/1.1\" 200 421 query=order upstream=payment %s note=支付成功\n", index%255, payload)
		default:
			fmt.Fprintf(&builder, "ERROR service=inventory 服务=库存中心 trace=req-%d message=同步失败 sync failed\nstack=inventory.reserve:42 cause=timeout %s\n", index, payload)
		}
	}
	return []byte(builder.String())
}

func piiBenchmarkScenarioType(name string) string {
	switch name {
	case "Phone", "Phone1", "Phone2", "Phone3":
		return "Phone"
	default:
		return name
	}
}

func TestPIIBenchmarkScenarioType(t *testing.T) {
	for _, test := range []struct {
		name string
		want string
	}{
		{name: "Phone", want: "Phone"},
		{name: "Phone1", want: "Phone"},
		{name: "Phone2", want: "Phone"},
		{name: "Phone3", want: "Phone"},
		{name: "Email", want: "Email"},
		{name: "AllPIITypes", want: "AllPIITypes"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := piiBenchmarkScenarioType(test.name); got != test.want {
				t.Fatalf("piiBenchmarkScenarioType(%q) = %q, want %q", test.name, got, test.want)
			}
		})
	}
}

func piiBenchmarkNeutralPayload(index int) string {
	return "order_id=ORD-" + fmt.Sprintf("%06d", 100000+index) +
		" request_id=req-" + fmt.Sprintf("%04x", 0x7000+index) +
		" status=processed details=" + strings.Repeat("ok-", index%5+1)
}

func piiBenchmarkFields(scenario string, index int, valid bool) []string {
	if scenario == "AllPIITypes" {
		return []string{
			piiBenchmarkField("Phone", index, valid),
			piiBenchmarkField("Email", index, valid),
			piiBenchmarkField("ChineseID", index, valid),
			piiBenchmarkField("BankCard", index, valid),
			piiBenchmarkField("CreditCard", index, valid),
			piiBenchmarkField("SensitiveToken", index, valid),
		}
	}
	return []string{piiBenchmarkField(scenario, index, valid)}
}

func piiBenchmarkField(scenario string, index int, valid bool) string {
	if !valid {
		switch scenario {
		case "Phone":
			return "mobile=12112312311"
		case "Email":
			return "email=user@example..invalid"
		case "ChineseID":
			return "identity_no=11010520000101002Z"
		case "BankCard":
			return "bank_card=6122021234567890"
		case "CreditCard":
			return "credit_card=2111111111111111"
		case "SensitiveToken":
			return "sensitive_token=0"
		default:
		}
	}
	switch scenario {
	case "Phone":
		return fmt.Sprintf("mobile=131123%05d", index)
	case "Email":
		return fmt.Sprintf("email=user%03d@sample%02d.com", index, index%97)
	case "ChineseID":
		return fmt.Sprintf("identity_no=11010520000101%04d", index)
	case "BankCard":
		return fmt.Sprintf("bank_card=62%014d", index)
	case "CreditCard":
		return fmt.Sprintf("credit_card=4%015d", index)
	case "SensitiveToken":
		return "sensitive_token=zredaction"
	default:
		panic("unknown PII benchmark scenario: " + scenario)
	}
}

func piiBenchmarkAlternation(expressions []scankit.Expression) string {
	patterns := make([]string, len(expressions))
	for index, expression := range expressions {
		patterns[index] = "(?:" + expression.Pattern + ")"
	}
	return strings.Join(patterns, "|")
}

func piiBenchmarkExpectedMatches(expressions []scankit.Expression, data []byte) []scankit.Match {
	matches := make([]scankit.Match, 0)
	for _, expression := range expressions {
		re := regexp.MustCompile(expression.Pattern)
		for _, index := range re.FindAllIndex(data, -1) {
			matches = append(matches, scankit.Match{Id: expression.Id, From: uint64(index[0]), To: uint64(index[1])})
		}
	}
	sort.Slice(matches, func(i, j int) bool {
		if matches[i].To != matches[j].To {
			return matches[i].To < matches[j].To
		}
		if matches[i].From != matches[j].From {
			return matches[i].From < matches[j].From
		}
		return matches[i].Id < matches[j].Id
	})
	return matches
}

func writePIIMask(buf *bytes.Buffer, _ scankit.Match, matched []byte) {
	for range matched {
		buf.WriteByte('*')
	}
}

func maskPIIValue(_ scankit.Match, value []byte) {
	for index := range value {
		value[index] = '*'
	}
}

func startPIIBenchmarkTimer(b *testing.B, fixture piiBenchmarkFixture) {
	b.SetBytes(int64(len(fixture.data)))
	b.ReportAllocs()
	b.ResetTimer()
	b.ReportMetric(float64(len(fixture.matches)), "matches/op")
}

// TestPIIRedactionStable 用 testing.AllocsPerRun(200, ...) + testing.Benchmark
// 提供稳定的 EngineMask / GoRegexpReplace 对比基线。
func TestPIIRedactionStable(t *testing.T) {
	for _, scenario := range piiBenchmarkScenarios() {
		for _, density := range piiBenchmarkDensities() {
			fixture := newPIIBenchmarkFixture(t, scenario, density)

			// --- EngineMask ---
			data := make([]byte, len(fixture.data))
			copy(data, fixture.data)
			for range 10 {
				copy(data, fixture.data)
				if err := fixture.engine.Mask(data, maskPIIValue); err != nil {
					t.Fatal(err)
				}
			}
			engineAllocs := testing.AllocsPerRun(200, func() {
				copy(data, fixture.data)
				if err := fixture.engine.Mask(data, maskPIIValue); err != nil {
					t.Fatal(err)
				}
				piiBenchmarkBytesSink = data
			})
			engineRes := testing.Benchmark(func(b *testing.B) {
				data := make([]byte, len(fixture.data))
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					copy(data, fixture.data)
					if err := fixture.engine.Mask(data, maskPIIValue); err != nil {
						b.Fatal(err)
					}
					piiBenchmarkBytesSink = data
				}
			})
			nsPerOp := float64(engineRes.NsPerOp())
			mbPerS := float64(len(fixture.data)) / nsPerOp * 1e9 / (1 << 20)
			t.Logf("%s/%s/EngineMask ns/op=%.0f MB/s=%.2f B/op=%d allocs/op=%.1f matches=%d",
				scenario.name, density.name, nsPerOp, mbPerS,
				engineRes.AllocedBytesPerOp(), engineAllocs, len(fixture.matches))

			// --- GoRegexpReplace ---
			for range 10 {
				piiBenchmarkBytesSink = fixture.goRegexp.ReplaceAllFunc(fixture.data, fixture.maskRegexpMatch)
			}
			regexpAllocs := testing.AllocsPerRun(200, func() {
				piiBenchmarkBytesSink = fixture.goRegexp.ReplaceAllFunc(fixture.data, fixture.maskRegexpMatch)
			})
			regexpRes := testing.Benchmark(func(b *testing.B) {
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					piiBenchmarkBytesSink = fixture.goRegexp.ReplaceAllFunc(fixture.data, fixture.maskRegexpMatch)
				}
			})
			nsPerOp = float64(regexpRes.NsPerOp())
			mbPerS = float64(len(fixture.data)) / nsPerOp * 1e9 / (1 << 20)
			t.Logf("%s/%s/GoRegexpReplace ns/op=%.0f MB/s=%.2f B/op=%d allocs/op=%.1f matches=%d",
				scenario.name, density.name, nsPerOp, mbPerS,
				regexpRes.AllocedBytesPerOp(), regexpAllocs, len(fixture.matches))
		}
	}
}

// ---- 以下用例来自原根目录的 pii_rules100_bench_test.go ----

// PIIRules100 规模化场景把 8 种 PII 形态复制到 100 条带独立字段锚点的规则上，
// 用来衡量 Block 扫描在规则数量放大后的吞吐。该场景与本文件的全部测试都自包含，
// 不复用 pii_log_bench_test.go 的场景登记表。
const (
	piiRules100RuleCount   = 100
	piiRules100RecordCount = piiBenchmarkRecordCount
)

type piiRules100Density struct {
	name          string
	expectedRows  int
	hasMatchAtRow func(int) bool
}

func piiRules100Densities() []piiRules100Density {
	return []piiRules100Density{
		{name: "NoMatch", expectedRows: 0, hasMatchAtRow: func(int) bool { return false }},
		{name: "LowMatch", expectedRows: 2, hasMatchAtRow: func(record int) bool { return record%64 == 0 }},
		{name: "HighMatch", expectedRows: 64, hasMatchAtRow: func(record int) bool { return record%2 == 0 }},
	}
}

func piiRules100BasePatterns() []string {
	return []string{
		logChinesePhonePattern1,
		logChinesePhonePattern2,
		logChinesePhonePattern3,
		logEmailPattern,
		logChineseIDPattern,
		logBankCardPattern,
		logCreditCardPattern,
		logSensitiveTokenPattern,
	}
}

func piiRules100FieldName(rule int) string {
	return fmt.Sprintf("field%02d", rule)
}

func piiRules100Expressions() []scankit.Expression {
	patterns := piiRules100BasePatterns()
	expressions := make([]scankit.Expression, 0, piiRules100RuleCount)
	for rule := range piiRules100RuleCount {
		// 字段锚点必须覆盖整条形态；不分组时 `a|b|c` 只会锚定第一个分支。
		expressions = append(expressions, scankit.Expression{
			Id:      uint32(rule + 1),
			Pattern: piiRules100FieldName(rule) + "=(?:" + patterns[rule%len(patterns)] + ")",
		})
	}
	return expressions
}

func piiRules100Value(rule, record int) string {
	switch rule % 8 {
	case 0, 1, 2:
		return fmt.Sprintf("131123%05d", record)
	case 3:
		return fmt.Sprintf("user%03d@sample%02d.com", record, record%97)
	case 4:
		return fmt.Sprintf("11010520000101%04d", record)
	case 5:
		return fmt.Sprintf("62%014d", record)
	case 6:
		return fmt.Sprintf("4%015d", record)
	case 7:
		return "zredaction"
	default:
		panic("unreachable PII rule index")
	}
}

// piiRules100NearMissValue 返回与对应形态同前缀但必然不匹配的取值，
// 用来确认候选确认程序会拒绝而不是误报。
func piiRules100NearMissValue(rule int) string {
	switch rule % 8 {
	case 0, 1, 2:
		return "12112312311"
	case 3:
		return "user@example..invalid"
	case 4:
		return "11010520000101002Z"
	case 5:
		return "6122021234567890"
	case 6:
		return "2111111111111111"
	case 7:
		return "0"
	default:
		panic("unreachable PII rule index")
	}
}

func piiRules100Fields(record int) []string {
	fields := make([]string, 0, piiRules100RuleCount)
	for rule := range piiRules100RuleCount {
		fields = append(fields, piiRules100FieldName(rule)+"="+piiRules100Value(rule, record))
	}
	return fields
}

func piiRules100Corpus(density piiRules100Density) []byte {
	var builder strings.Builder
	builder.Grow(piiRules100RecordCount * (256 + piiRules100RuleCount*24))
	for record := range piiRules100RecordCount {
		payload := piiBenchmarkNeutralPayload(record)
		if density.hasMatchAtRow(record) {
			payload = strings.Join(piiRules100Fields(record), " ")
		}
		builder.WriteString(piiRules100LogLine(record, payload))
	}
	return []byte(builder.String())
}

func piiRules100LogLine(record int, payload string) string {
	switch record % 4 {
	case 0:
		return `{"ts":"2026-08-25T10:30:00+08:00","level":"INFO","service":"payment","用户":"张三","message":"支付完成 payment completed","context":"` + payload + `"}` + "\n"
	case 1:
		return "ts=2026-08-25T10:30:00+08:00 level=WARN service=order " + payload + " 服务=订单中心 user=alice message=库存不足 inventory retry\n"
	case 2:
		return "10.12.0." + fmt.Sprint(record%255) + " - - [25/Aug/2026:10:30:00 +0800] \"POST /api/v1/pay HTTP/1.1\" 200 421 query=order upstream=payment " + payload + " note=支付成功\n"
	default:
		return "ERROR service=inventory 服务=库存中心 trace=req-" + fmt.Sprint(record) + " message=同步失败 sync failed\nstack=inventory.reserve:42 cause=timeout " + payload + "\n"
	}
}

type piiRules100Fixture struct {
	data      []byte
	matches   []scankit.Match
	masked    []byte
	maskBytes []byte
	scanner   *scankit.Scanner
	engine    *scankit.Engine
	goRegexp  *regexp.Regexp
	maskFn    func([]byte) []byte
}

func newPIIRules100Fixture(tb testing.TB, density piiRules100Density) piiRules100Fixture {
	tb.Helper()
	expressions := piiRules100Expressions()
	data := piiRules100Corpus(density)
	scanner, err := scankit.Compile(expressions)
	if err != nil {
		tb.Fatal(err)
	}
	engine, err := scankit.New(expressions)
	if err != nil {
		tb.Fatal(err)
	}
	goRegexp, err := regexp.Compile(piiBenchmarkAlternation(expressions))
	if err != nil {
		tb.Fatal(err)
	}
	expected := piiBenchmarkExpectedMatches(expressions, data)
	if want := density.expectedRows * piiRules100RuleCount; len(expected) != want {
		tb.Fatalf("%s expected match count = %d, want %d", density.name, len(expected), want)
	}
	matches, err := scanner.ScanInto(data, make([]scankit.Match, 0, len(expected)))
	if err != nil {
		tb.Fatal(err)
	}
	if !slices.Equal(matches, expected) {
		tb.Fatalf("%s Scanner ScanInto match count = %d, want %d", density.name, len(matches), len(expected))
	}
	fixture := piiRules100Fixture{
		data:      data,
		matches:   expected,
		maskBytes: bytes.Repeat([]byte{'*'}, len(data)),
		scanner:   scanner,
		engine:    engine,
		goRegexp:  goRegexp,
	}
	fixture.maskFn = func(match []byte) []byte {
		return fixture.maskBytes[:len(match)]
	}
	fixture.masked, err = engine.Replace(data, writePIIMask)
	if err != nil {
		tb.Fatal(err)
	}
	masked := append([]byte(nil), data...)
	if err := engine.Mask(masked, maskPIIValue); err != nil {
		tb.Fatal(err)
	}
	if !bytes.Equal(masked, fixture.masked) {
		tb.Fatal("Engine Mask does not match Engine Replace")
	}
	if replaced := goRegexp.ReplaceAllFunc(data, fixture.maskFn); !bytes.Equal(replaced, fixture.masked) {
		tb.Fatal("Go regexp replacement does not match Engine Replace")
	}
	return fixture
}

func BenchmarkPIIRedactionRules100(b *testing.B) {
	for _, density := range piiRules100Densities() {
		b.Run(density.name, func(b *testing.B) {
			fixture := newPIIRules100Fixture(b, density)

			b.Run("ScannerScanInto", func(b *testing.B) {
				matches := make([]scankit.Match, 0, len(fixture.matches))
				if _, err := fixture.scanner.ScanInto(fixture.data, matches); err != nil {
					b.Fatal(err)
				}
				startPIIRules100Timer(b, fixture)
				for range b.N {
					matches = matches[:0]
					var err error
					matches, err = fixture.scanner.ScanInto(fixture.data, matches)
					if err != nil {
						b.Fatal(err)
					}
				}
				piiBenchmarkMatchesSink = matches
			})

			b.Run("EngineMask", func(b *testing.B) {
				data := make([]byte, len(fixture.data))
				copy(data, fixture.data)
				if err := fixture.engine.Mask(data, maskPIIValue); err != nil {
					b.Fatal(err)
				}
				startPIIRules100Timer(b, fixture)
				for range b.N {
					copy(data, fixture.data)
					if err := fixture.engine.Mask(data, maskPIIValue); err != nil {
						b.Fatal(err)
					}
					piiBenchmarkBytesSink = data
				}
			})

			b.Run("GoRegexpReplace", func(b *testing.B) {
				startPIIRules100Timer(b, fixture)
				for range b.N {
					piiBenchmarkBytesSink = fixture.goRegexp.ReplaceAllFunc(fixture.data, fixture.maskFn)
				}
			})
		})
	}
}

func startPIIRules100Timer(b *testing.B, fixture piiRules100Fixture) {
	b.SetBytes(int64(len(fixture.data)))
	b.ReportAllocs()
	b.ResetTimer()
	b.ReportMetric(float64(len(fixture.matches)), "matches/op")
}

func TestPIIRules100FieldAnchorsMatchOnlyOwnField(t *testing.T) {
	scanner, err := scankit.Compile(piiRules100Expressions())
	if err != nil {
		t.Fatal(err)
	}
	record := []byte(strings.Join(piiRules100Fields(0), " ") + "\n")
	matches, err := scanner.Scan(record)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != piiRules100RuleCount {
		t.Fatalf("match count = %d, want %d", len(matches), piiRules100RuleCount)
	}
	for _, match := range matches {
		rule := int(match.Id) - 1
		want := piiRules100FieldName(rule) + "=" + piiRules100Value(rule, 0)
		if got := string(record[match.From:match.To]); got != want {
			t.Fatalf("rule %d matched %q, want %q", match.Id, got, want)
		}
	}
}

func TestPIIRules100BareValuesDoNotMatch(t *testing.T) {
	scanner, err := scankit.Compile(piiRules100Expressions())
	if err != nil {
		t.Fatal(err)
	}
	values := make([]string, 0, piiRules100RuleCount)
	for rule := range piiRules100RuleCount {
		values = append(values, piiRules100Value(rule, 7))
	}
	record := []byte(strings.Join(values, " ") + "\n")
	matches, err := scanner.Scan(record)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("bare values matched %d times, want 0; first = %q", len(matches), record[matches[0].From:matches[0].To])
	}
}

func TestPIIRules100NearMissValuesDoNotMatch(t *testing.T) {
	scanner, err := scankit.Compile(piiRules100Expressions())
	if err != nil {
		t.Fatal(err)
	}
	fields := make([]string, 0, piiRules100RuleCount)
	for rule := range piiRules100RuleCount {
		fields = append(fields, piiRules100FieldName(rule)+"="+piiRules100NearMissValue(rule))
	}
	record := []byte(strings.Join(fields, " ") + "\n")
	matches, err := scanner.Scan(record)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("near miss values matched %d times, want 0; first = %q", len(matches), record[matches[0].From:matches[0].To])
	}
}

func TestPIIRules100CorpusMatchesGoRegexp(t *testing.T) {
	for _, density := range piiRules100Densities() {
		t.Run(density.name, func(t *testing.T) {
			fixture := newPIIRules100Fixture(t, density)
			want := density.expectedRows * piiRules100RuleCount
			if len(fixture.matches) != want {
				t.Fatalf("match count = %d, want %d", len(fixture.matches), want)
			}
			if got := len(fixture.goRegexp.FindAllIndex(fixture.data, -1)); got != want {
				t.Fatalf("Go regexp match count = %d, want %d", got, want)
			}
		})
	}
}

func TestPIIRules100CorpusShape(t *testing.T) {
	for _, density := range piiRules100Densities() {
		fixture := newPIIRules100Fixture(t, density)
		t.Logf("%s: bytes=%d rules=%d matches=%d bytes/match=%.1f",
			density.name, len(fixture.data), piiRules100RuleCount, len(fixture.matches),
			float64(len(fixture.data))/float64(max(len(fixture.matches), 1)))
	}
}

// TestPIIRedactionRules100Stable 用 testing.AllocsPerRun(200, ...) + testing.Benchmark
// 提供稳定的 ScannerScanInto / EngineMask / GoRegexpReplace 对比基线。
func TestPIIRedactionRules100Stable(t *testing.T) {
	for _, density := range piiRules100Densities() {
		fixture := newPIIRules100Fixture(t, density)
		dataLen := len(fixture.data)

		// --- ScannerScanInto ---
		matches := make([]scankit.Match, 0, len(fixture.matches))
		for range 10 {
			matches = matches[:0]
			var err error
			matches, err = fixture.scanner.ScanInto(fixture.data, matches)
			if err != nil {
				t.Fatal(err)
			}
		}
		scanAllocs := testing.AllocsPerRun(200, func() {
			matches = matches[:0]
			var err error
			matches, err = fixture.scanner.ScanInto(fixture.data, matches)
			if err != nil {
				t.Fatal(err)
			}
			piiBenchmarkMatchesSink = matches
		})
		scanRes := testing.Benchmark(func(b *testing.B) {
			matches := make([]scankit.Match, 0, len(fixture.matches))
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				matches = matches[:0]
				var err error
				matches, err = fixture.scanner.ScanInto(fixture.data, matches)
				if err != nil {
					b.Fatal(err)
				}
			}
			piiBenchmarkMatchesSink = matches
		})
		nsPerOp := float64(scanRes.NsPerOp())
		mbPerS := float64(dataLen) / nsPerOp * 1e9 / (1 << 20)
		t.Logf("%s/ScannerScanInto ns/op=%.0f MB/s=%.2f B/op=%d allocs/op=%.1f matches=%d",
			density.name, nsPerOp, mbPerS, scanRes.AllocedBytesPerOp(), scanAllocs, len(fixture.matches))

		// --- EngineMask ---
		data := make([]byte, dataLen)
		copy(data, fixture.data)
		for range 10 {
			copy(data, fixture.data)
			if err := fixture.engine.Mask(data, maskPIIValue); err != nil {
				t.Fatal(err)
			}
		}
		engineAllocs := testing.AllocsPerRun(200, func() {
			copy(data, fixture.data)
			if err := fixture.engine.Mask(data, maskPIIValue); err != nil {
				t.Fatal(err)
			}
			piiBenchmarkBytesSink = data
		})
		engineRes := testing.Benchmark(func(b *testing.B) {
			data := make([]byte, dataLen)
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				copy(data, fixture.data)
				if err := fixture.engine.Mask(data, maskPIIValue); err != nil {
					b.Fatal(err)
				}
				piiBenchmarkBytesSink = data
			}
		})
		nsPerOp = float64(engineRes.NsPerOp())
		mbPerS = float64(dataLen) / nsPerOp * 1e9 / (1 << 20)
		t.Logf("%s/EngineMask ns/op=%.0f MB/s=%.2f B/op=%d allocs/op=%.1f matches=%d",
			density.name, nsPerOp, mbPerS, engineRes.AllocedBytesPerOp(), engineAllocs, len(fixture.matches))

		// --- GoRegexpReplace ---
		for range 10 {
			piiBenchmarkBytesSink = fixture.goRegexp.ReplaceAllFunc(fixture.data, fixture.maskFn)
		}
		regexpAllocs := testing.AllocsPerRun(200, func() {
			piiBenchmarkBytesSink = fixture.goRegexp.ReplaceAllFunc(fixture.data, fixture.maskFn)
		})
		regexpRes := testing.Benchmark(func(b *testing.B) {
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				piiBenchmarkBytesSink = fixture.goRegexp.ReplaceAllFunc(fixture.data, fixture.maskFn)
			}
		})
		nsPerOp = float64(regexpRes.NsPerOp())
		mbPerS = float64(dataLen) / nsPerOp * 1e9 / (1 << 20)
		t.Logf("%s/GoRegexpReplace ns/op=%.0f MB/s=%.2f B/op=%d allocs/op=%.1f matches=%d",
			density.name, nsPerOp, mbPerS, regexpRes.AllocedBytesPerOp(), regexpAllocs, len(fixture.matches))
	}
}

// ---- 以下用例来自原根目录的 perf_baseline_test.go ----

// fixedCorpusScanMetrics 记录固定语料上的确定性指标。
// 时间指标只用于人工记录，不参与断言，避免把机器性能当成回归门禁。

// measureFixedCorpusScan 在固定规则与固定语料上采集确定性指标。

// fixedCorpusGoldenMatches 与 fixedCorpusGoldenBackends 是固定语料上的回归基线。
// 命中集合与引擎布局发生变化说明行为或选择逻辑回归，必须显式更新基线。

// TestFixedCorpusDeterministicMetrics 校验固定语料上的命中集合、执行后端和
// 分配次数等确定性指标；时间指标不设阈值，只记录在性能基线文档中。

// fixedBenchCorpus 把固定语料确定性重复到约 64KiB，用于吞吐基线；
// 重复只放大测量区间，不改变命中模式与选择路径。
func fixedBenchCorpus() []byte {
	block := conformanceMatrixCorpus()
	repeats := max((64<<10)/len(block), 1)
	out := make([]byte, 0, repeats*len(block))
	for range repeats {
		out = append(out, block...)
	}
	return out
}

// BenchmarkFixedCorpusScan 在固定语料上测量各后端的扫描吞吐、延迟和分配，
// 供性能基线文档记录与回归比较使用。
func BenchmarkFixedCorpusScan(b *testing.B) {
	expressions := conformanceMatrixExpressions()
	corpus := fixedBenchCorpus()
	entries := []struct {
		name    string
		backend simd.Backend
	}{
		{"generic", simd.Default()},
		{"host", dispatch.DefaultBackend()},
	}
	for _, entry := range entries {
		b.Run(entry.name, func(b *testing.B) {
			dispatch.SetBackendOverride(entry.backend)
			defer dispatch.SetBackendOverride(nil)
			scanner, err := scankit.Compile(expressions)
			if err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			b.SetBytes(int64(len(corpus)))
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := scanner.Scan(corpus); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
