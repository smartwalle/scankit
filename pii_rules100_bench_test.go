package scankit_test

import (
	"bytes"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/smartwalle/scankit"
)

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
	if _, err := engine.Mask(masked, maskPIIValue); err != nil {
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
				if _, err := fixture.engine.Mask(data, maskPIIValue); err != nil {
					b.Fatal(err)
				}
				startPIIRules100Timer(b, fixture)
				for range b.N {
					copy(data, fixture.data)
					result, err := fixture.engine.Mask(data, maskPIIValue)
					if err != nil {
						b.Fatal(err)
					}
					piiBenchmarkBytesSink = result
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
