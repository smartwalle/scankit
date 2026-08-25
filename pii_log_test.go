package scankit_test

import (
	"testing"

	"github.com/smartwalle/scankit"
)

// 这些规则描述日志扫描测试使用的五类敏感信息。
// 它们只匹配原始值；生产调用方可按日志格式增加字段名或边界约束。
const (
	logChinesePhonePattern = `1[3-9][0-9]{9}`
	logEmailPattern        = "[A-Za-z0-9.!#$%&'*+/?^_`{|}~-]{1,64}@[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?(?:\\.[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?)+\\b"
	logChineseIDPattern    = `[1-9][0-9]{5}(18|19|20)[0-9]{2}(0[1-9]|1[0-2])(0[1-9]|[12][0-9]|3[01])[0-9]{3}[0-9Xx]`
	logBankCardPattern     = `62[0-9]{14,17}`
	logCreditCardPattern   = `4[0-9]{15}|5[1-5][0-9]{14}|3[47][0-9]{13}`
)

func TestLogPIIFixtures(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name        string
		expressions []scankit.Expression
		record      string
		want        int
	}{
		{"ChinesePhone", []scankit.Expression{{Id: 1, Pattern: logChinesePhonePattern}}, logPIIRecord("mobile", "13800138000"), 1},
		{"Email", []scankit.Expression{{Id: 1, Pattern: logEmailPattern}}, logPIIRecord("email", "alice.zhang@example.com"), 1},
		{"ChineseID", []scankit.Expression{{Id: 1, Pattern: logChineseIDPattern}}, logPIIRecord("identity_no", "11010520000101002X"), 1},
		{"BankCard", []scankit.Expression{{Id: 1, Pattern: logBankCardPattern}}, logPIIRecord("bank_card", "6222021234567890"), 1},
		{"CreditCard", []scankit.Expression{{Id: 1, Pattern: logCreditCardPattern}}, logPIIRecord("credit_card", "4111111111111111"), 1},
		{"Mixed", logPIIMixedExpressions(), logPIIMixedRecord(), 5},
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
		"bank_card=6222021234567890 credit_card=4111111111111111 " +
		"message=支付成功 payment_completed audit=日志脱敏\n"
}

func logPIIMixedExpressions() []scankit.Expression {
	return []scankit.Expression{
		{Id: 1, Pattern: logChinesePhonePattern},
		{Id: 2, Pattern: logEmailPattern},
		{Id: 3, Pattern: logChineseIDPattern},
		{Id: 4, Pattern: logBankCardPattern},
		{Id: 5, Pattern: logCreditCardPattern},
	}
}
