package scankit_test

import (
	"bytes"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/smartwalle/scankit"
)

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

					b.Run("ScannerScanInto", func(b *testing.B) {
						matches := make([]scankit.Match, 0, len(fixture.matches))
						if _, err := fixture.scanner.ScanInto(fixture.data, matches); err != nil {
							b.Fatal(err)
						}
						startPIIBenchmarkTimer(b, fixture)
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

					b.Run("EngineReplace", func(b *testing.B) {
						if _, err := fixture.engine.Replace(fixture.data, writePIIMask); err != nil {
							b.Fatal(err)
						}
						startPIIBenchmarkTimer(b, fixture)
						for range b.N {
							result, err := fixture.engine.Replace(fixture.data, writePIIMask)
							if err != nil {
								b.Fatal(err)
							}
							piiBenchmarkBytesSink = result
						}
					})

					b.Run("EngineMask", func(b *testing.B) {
						data := make([]byte, len(fixture.data))
						copy(data, fixture.data)
						if _, err := fixture.engine.Mask(data, maskPIIValue); err != nil {
							b.Fatal(err)
						}
						startPIIBenchmarkTimer(b, fixture)
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
			name:        "Phone",
			expressions: []scankit.Expression{{Id: 1, Pattern: logChinesePhonePattern}},
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
	engine, err := scankit.New(scenario.expressions)
	if err != nil {
		b.Fatal(err)
	}
	goRegexp, err := regexp.Compile(piiBenchmarkAlternation(scenario.expressions))
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
	if _, err := engine.Mask(maskedInput, maskPIIValue); err != nil {
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
			payload = strings.Join(piiBenchmarkFields(scenario.name, index, true), " ")
		} else if index%4 == 0 {
			payload = strings.Join(piiBenchmarkFields(scenario.name, index, false), " ")
		}
		switch index % 4 {
		case 0:
			builder.WriteString(`{"ts":"2026-08-25T10:30:00+08:00","level":"INFO","service":"payment","用户":"张三","message":"支付完成 payment completed","context":"` + payload + `"}` + "\n")
		case 1:
			builder.WriteString("ts=2026-08-25T10:30:00+08:00 level=WARN service=order " + payload + " 服务=订单中心 user=alice message=库存不足 inventory retry\n")
		case 2:
			builder.WriteString("10.12.0." + fmt.Sprint(index%255) + " - - [25/Aug/2026:10:30:00 +0800] \"POST /api/v1/pay HTTP/1.1\" 200 421 query=order upstream=payment " + payload + " note=支付成功\n")
		default:
			builder.WriteString("ERROR service=inventory 服务=库存中心 trace=req-" + fmt.Sprint(index) + " message=同步失败 sync failed\nstack=inventory.reserve:42 cause=timeout " + payload + "\n")
		}
	}
	return []byte(builder.String())
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
