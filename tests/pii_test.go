// pii_test：日志场景的 PII 扫描与替换
//
// 覆盖手机号、邮箱、身份证、银行卡等形态在日志语料上的扫描与脱敏。
// 由原根目录的 pii_log_test.go、pii_phone_email_test.go 合并而成。

package tests

import (
	"fmt"
	"regexp"
	"testing"

	"github.com/smartwalle/scankit"
)

// ---- 以下用例来自原根目录的 pii_log_test.go ----

// 这些规则描述日志扫描测试使用的五类敏感信息。
// 它们只匹配原始值；生产调用方可按日志格式增加字段名或边界约束。
const (
	logChinesePhonePattern1  = `1[3-9][0-9]{9}`
	logChinesePhonePattern2  = `(?:\b|^)(?:\+86|86)?1[3-9]\d{9}(?:\b|$)`
	logChinesePhonePattern3  = `\b(?:86)?1[3-9][0-9]{9}\b`
	logEmailPattern          = "[A-Za-z0-9.!#$%&'*+/?^_`{|}~-]{1,64}@[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?(?:\\.[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?)+\\b"
	logChineseIDPattern      = `[1-9][0-9]{5}(18|19|20)[0-9]{2}(0[1-9]|1[0-2])(0[1-9]|[12][0-9]|3[01])[0-9]{3}[0-9Xx]`
	logBankCardPattern       = `62[0-9]{14,17}`
	logCreditCardPattern     = `4[0-9]{15}|5[1-5][0-9]{14}|3[47][0-9]{13}`
	logSensitiveTokenPattern = `[z][a-z]{9,}`

	// logPasswordPattern 与前五类"只匹配原始值"的规则形态不同：它按字段名锚定，
	// 不区分大小写地匹配 password 后跟 `:` 或 `=`（中间允许空白）以及一段非空白口令。
	// 用于覆盖"字段名 + 分隔符 + 值"这类日志口令形态。
	logPasswordPattern = `(?i)password\s*[:=]\s*\S+`
)

func TestLogPIIFixtures(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name        string
		expressions []scankit.Expression
		record      string
		want        int
	}{
		{"ChinesePhonePattern1", []scankit.Expression{{Id: 1, Pattern: logChinesePhonePattern1}}, logPIIRecord("mobile", "13800138000"), 1},
		{"ChinesePhonePattern2", []scankit.Expression{{Id: 1, Pattern: logChinesePhonePattern2}}, logPIIRecord("mobile", "13800138000"), 1},
		{"ChinesePhonePattern3", []scankit.Expression{{Id: 1, Pattern: logChinesePhonePattern3}}, logPIIRecord("mobile", "13800138000"), 1},
		{"Email", []scankit.Expression{{Id: 1, Pattern: logEmailPattern}}, logPIIRecord("email", "alice.zhang@example.com"), 1},
		{"ChineseID", []scankit.Expression{{Id: 1, Pattern: logChineseIDPattern}}, logPIIRecord("identity_no", "11010520000101002X"), 1},
		{"BankCard", []scankit.Expression{{Id: 1, Pattern: logBankCardPattern}}, logPIIRecord("bank_card", "6222021234567890"), 1},
		{"CreditCard", []scankit.Expression{{Id: 1, Pattern: logCreditCardPattern}}, logPIIRecord("credit_card", "4111111111111111"), 1},
		{"SensitiveToken", []scankit.Expression{{Id: 1, Pattern: logSensitiveTokenPattern}}, logPIIRecord("sensitive_token", "zredaction"), 1},
		{"Password", []scankit.Expression{{Id: 1, Pattern: logPasswordPattern}}, logPIIRecord("password", "P@ssw0rd!2026"), 1},
		{"Mixed", logPIIMixedExpressions(), logPIIMixedRecord(), 6},
	} {
		t.Run(test.name, func(t *testing.T) {
			database, err := scankit.Compile(test.expressions)
			if err != nil {
				t.Fatal(err)
			}
			matches, err := database.Scan([]byte(test.record))
			if err != nil {
				t.Fatal(err)
			}
			if len(matches) != test.want {
				t.Fatalf("match count = %d, want %d", len(matches), test.want)
			}
		})
	}
}

func TestLogEmailPatternWithWordBoundariesMatchesEachAddress(t *testing.T) {
	pattern := `\b` + logEmailPattern
	scanner, err := scankit.Compile([]scankit.Expression{{Id: 1, Pattern: pattern}})
	if err != nil {
		t.Fatal(err)
	}
	data := []byte(`phone=13800138000 12345678@qq.com email=alice.smith42@example.cn invalid=12345678901`)
	matches, err := scanner.Scan(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 2 {
		t.Fatalf("match count = %d, want 2; matches=%#v", len(matches), matches)
	}
	for _, match := range matches {
		if got := string(data[match.From:match.To]); got != "12345678@qq.com" && got != "alice.smith42@example.cn" {
			t.Fatalf("unexpected email match %q", got)
		}
	}
}

// 折叠窗口（入口集合重复 + 固定文字）只登记最左起点，窗口内更靠右的起点由确认
// 阶段的推迟逻辑在 blockedUntil 位置补齐。这里用 Go regexp 作为参照，覆盖「前一个
// 命中的结束位置落在下一个窗口内部」的形态：窗口最左起点被重叠抑制，真正命中的
// 起点在窗口内部，漏掉这一步会少报一条匹配。
func TestLogEmailPatternResumesInsideCollapsedWindow(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name    string
		pattern string
		record  string
	}{
		{"DomainTailBeforeLocalByte", logEmailPattern, "a@b.co-.@x.com"},
		{"AdjacentAddresses", logEmailPattern, "u1@a.co.u2@b.co"},
		{"SeparatedByLocalBytes", logEmailPattern, "u1@a.co-u2@b.co"},
		{"BoundedLocalPart", `[a-z]{1,4}@[a-z]+`, "ab@cd@e"},
		{"BoundedLocalPartDense", `[a-z]{1,4}@[a-z]+`, "a@b@cd"},
	} {
		t.Run(test.name, func(t *testing.T) {
			data := []byte(test.record)
			scanner, err := scankit.Compile([]scankit.Expression{{Id: 1, Pattern: test.pattern}})
			if err != nil {
				t.Fatal(err)
			}
			matches, err := scanner.Scan(data)
			if err != nil {
				t.Fatal(err)
			}
			want := regexp.MustCompile(test.pattern).FindAllIndex(data, -1)
			if len(matches) != len(want) {
				t.Fatalf("match count = %d, want %d; matches=%#v", len(matches), len(want), matches)
			}
			for index, span := range want {
				if matches[index].From != uint64(span[0]) || matches[index].To != uint64(span[1]) {
					t.Fatalf("match[%d] = (%d,%d), want (%d,%d)", index, matches[index].From, matches[index].To, span[0], span[1])
				}
			}
		})
	}
}

func TestLogChinesePhonePatternMatchesBoundedNumbers(t *testing.T) {
	scanner, err := scankit.Compile([]scankit.Expression{{Id: 1, Pattern: logChinesePhonePattern2}})
	if err != nil {
		t.Fatal(err)
	}
	data := []byte("+8613800138000 mobile=13800138000 mobile=8613800138000 invalid=x13800138000 invalid=8613800138000x invalid=a8613800138000")
	matches, err := scanner.Scan(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 3 {
		t.Fatalf("match count = %d, want 3; matches=%#v", len(matches), matches)
	}
	if got := string(data[matches[0].From:matches[0].To]); got != "+8613800138000" {
		t.Fatalf("first phone = %q, want +8613800138000", got)
	}
	if got := string(data[matches[1].From:matches[1].To]); got != "13800138000" {
		t.Fatalf("second phone = %q, want 13800138000", got)
	}
	if got := string(data[matches[2].From:matches[2].To]); got != "8613800138000" {
		t.Fatalf("third phone = %q, want 8613800138000", got)
	}
}

func FuzzLogPIIRulesScanInto(f *testing.F) {
	database, err := scankit.Compile(logPIIMixedExpressions())
	if err != nil {
		f.Fatal(err)
	}
	f.Add([]byte("服务=支付网关 mobile=13800138000 message=payment completed"))
	f.Add([]byte("email=alice.zhang@example.cn identity_no=11010520000101002X"))
	f.Add([]byte("ts=2026-08-20T09:30:00+08:00 service=payment 用户=张三 mobile=13800138000 email=alice.zhang@example.cn"))
	f.Add([]byte{0xff, 0x00, '1', '3', '8'})

	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 4_096 {
			t.Skip()
		}
		want, err := database.Scan(data)
		if err != nil {
			t.Fatal(err)
		}
		got, err := database.ScanInto(data, nil)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != len(want) {
			t.Fatalf("ScanInto match count = %d, Scan match count = %d", len(got), len(want))
		}
		for index := range want {
			if got[index] != want[index] {
				t.Fatalf("ScanInto match %d = %#v, Scan match = %#v", index, got[index], want[index])
			}
		}
	})
}

func logPIIRecord(field, value string) string {
	return "ts=2026-08-20T09:30:00+08:00 level=INFO service=payment-gateway " +
		"服务=支付网关 用户=张三 request_id=req-7f3a2c " + field + "=" + value +
		" message=支付成功 payment_completed audit=日志脱敏\n"
}

func logPIIMixedRecord() string {
	return "ts=2026-08-20T09:30:00+08:00 level=INFO service=payment-gateway " +
		"服务=支付网关 用户=张三 request_id=req-7f3a2c " +
		"mobile=13800138000 email=alice.zhang@example.com identity_no=11010520000101002X " +
		"bank_card=6222021234567890 credit_card=4111111111111111 sensitive_token=zredaction " +
		"message=支付成功 payment_completed audit=日志脱敏\n"
}

func logPIIMixedExpressions() []scankit.Expression {
	return []scankit.Expression{
		{Id: 1, Pattern: logChinesePhonePattern2},
		{Id: 2, Pattern: logEmailPattern},
		{Id: 3, Pattern: logChineseIDPattern},
		{Id: 4, Pattern: logBankCardPattern},
		{Id: 5, Pattern: logCreditCardPattern},
		{Id: 6, Pattern: logSensitiveTokenPattern},
	}
}

// ---- 以下用例来自原根目录的 pii_phone_email_test.go ----

const phonePattern = `1[3-9][0-9]{9}`

// emailTLDPattern 匹配示例中的常见单级顶级域名。
const emailTLDPattern = `(com|net|org|cn|io|dev|app|edu|gov|info|biz|me|xyz|online|site|tech|store|cloud|ai|pro|mobi|name|tv|cc|hk|jp|uk|de|fr|au|ca|us)`

// emailPattern 限制本地部分和域名长度，支持 .com、.cn、.net、.org 等常见域名后缀。
const emailPattern = `[A-Za-z0-9._%+-]{1,64}@[A-Za-z0-9-]{1,63}\.` + emailTLDPattern

func TestPhonePatternMatchesGoRegexp(t *testing.T) {
	t.Parallel()

	data := []byte("valid=13800138000 invalid=12345678901 valid=19912345678")
	got := scanPhoneWithScankit(t, data)
	want := findAllOverlapping(regexp.MustCompile(phonePattern), data)
	assertRangesEqual(t, got, want)
}

func TestScannerConcurrentScan(t *testing.T) {
	s, err := scankit.Compile([]scankit.Expression{{Id: 1, Pattern: `1[0-9]{2}`}})
	if err != nil {
		t.Fatal(err)
	}
	data := []byte("x123 y1456")
	const workers = 8
	done := make(chan error, workers)
	for range workers {
		go func() {
			got, e := s.Scan(data)
			if e == nil && len(got) != 2 {
				e = fmt.Errorf("matches=%d", len(got))
			}
			done <- e
		}()
	}
	for range workers {
		if e := <-done; e != nil {
			t.Fatal(e)
		}
	}
}

func TestPhoneAndEmailPatternsMatchGoRegexp(t *testing.T) {
	t.Parallel()

	data := []byte("phone=13800138000 com=alice.smith42@example.com cn=bob@example.cn org=ops@example.org invalid=bad@domain.invalid phone=19912345678")
	database, err := scankit.Compile([]scankit.Expression{
		{Id: 1, Pattern: phonePattern},
		{Id: 2, Pattern: emailPattern},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := map[uint32][][2]int{}
	matches, err := database.Scan(data)
	if err != nil {
		t.Fatal(err)
	}
	for _, match := range matches {
		got[match.Id] = append(got[match.Id], [2]int{int(match.From), int(match.To)})
	}
	assertRangesEqual(t, got[1], findAllOverlapping(regexp.MustCompile(phonePattern), data))
	assertRangesEqual(t, got[2], regexp.MustCompile(emailPattern).FindAllIndex(data, -1))
}

func FuzzPhonePatternMatchesGoRegexp(f *testing.F) {
	f.Add([]byte("13800138000"))
	f.Add([]byte("invalid=12345678901"))
	f.Add([]byte("before 19912345678 after"))
	f.Add([]byte{0xff, '1', '3', '8', '0', '0', '1', '3', '8', '0', '0'})

	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 4_096 {
			t.Skip()
		}
		got := scanPhoneWithScankit(t, data)
		want := findAllOverlapping(regexp.MustCompile(phonePattern), data)
		assertRangesEqual(t, got, want)
	})
}

func FuzzPhoneAndEmailPatternsMatchGoRegexp(f *testing.F) {
	f.Add([]byte("phone=13800138000 email=alice.smith42@example.com"))
	f.Add([]byte("bob@domain.cn ops@domain.org bad@domain.invalid 19912345678"))
	f.Add([]byte{})

	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 4_096 {
			t.Skip()
		}
		database, err := scankit.Compile([]scankit.Expression{
			{Id: 1, Pattern: phonePattern},
			{Id: 2, Pattern: emailPattern},
		})
		if err != nil {
			t.Fatal(err)
		}
		got := map[uint32][][2]int{}
		matches, err := database.Scan(data)
		if err != nil {
			t.Fatal(err)
		}
		for _, match := range matches {
			got[match.Id] = append(got[match.Id], [2]int{int(match.From), int(match.To)})
		}
		assertRangesEqual(t, got[1], findAllOverlapping(regexp.MustCompile(phonePattern), data))
		assertRangesSubset(t, got[2], findAllOverlapping(regexp.MustCompile(emailPattern), data))
	})
}

func scanPhoneWithScankit(t testing.TB, data []byte) [][2]int {
	t.Helper()
	database, err := scankit.Compile([]scankit.Expression{{Id: 1, Pattern: phonePattern}})
	if err != nil {
		t.Fatal(err)
	}
	got, err := database.Scan(data)
	if err != nil {
		t.Fatal(err)
	}
	matches := make([][2]int, len(got))
	for index, match := range got {
		matches[index] = [2]int{int(match.From), int(match.To)}
	}
	return matches
}

func findAllOverlapping(re *regexp.Regexp, data []byte) [][]int {
	var matches [][]int
	for offset := 0; offset < len(data); {
		match := re.FindIndex(data[offset:])
		if match == nil {
			break
		}
		match[0] += offset
		match[1] += offset
		matches = append(matches, match)
		offset = match[0] + 1
	}
	return matches
}

func assertRangesSubset(t testing.TB, got [][2]int, candidates [][]int) {
	t.Helper()
	allowed := make(map[[2]int]struct{}, len(candidates))
	for _, candidate := range candidates {
		allowed[[2]int{candidate[0], candidate[1]}] = struct{}{}
	}
	for _, match := range got {
		if _, ok := allowed[match]; !ok {
			t.Fatalf("match %v is not accepted by Go regexp", match)
		}
	}
}

func assertRangesEqual(t testing.TB, got [][2]int, want [][]int) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("match count = %d, want %d; got = %v; want = %v", len(got), len(want), got, want)
	}
	for index := range want {
		if got[index][0] != want[index][0] || got[index][1] != want[index][1] {
			t.Fatalf("match %d = %v, want %v", index, got[index], want[index])
		}
	}
}
