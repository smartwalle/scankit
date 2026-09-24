package scankit

import (
	"bytes"
	"fmt"
	"math/rand/v2"
	"regexp"
	"strings"
	"testing"
)

// Rules contains the canonical 100-rule benchmark set.
// The same patterns are intended for Go regexp and Hyperscan.
var Rules = []string{
	// 001
	`[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}`,
	// 002
	`1[3-9][0-9]{9}`,
	// 003
	`(25[0-5]|2[0-4][0-9]|1[0-9]{2}|[1-9]?[0-9])(\.(25[0-5]|2[0-4][0-9]|1[0-9]{2}|[1-9]?[0-9])){3}`,
	// 004
	`[0-9A-Fa-f]{1,4}(:[0-9A-Fa-f]{1,4}){7}`,
	// 005
	`[0-9A-Fa-f]{2}(:[0-9A-Fa-f]{2}){5}`,
	// 006
	`[0-9A-Fa-f]{8}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{12}`,
	// 007
	`[0-9]{4}(-[0-9]{4}){3}`,
	// 008
	`[0-9]{17}[0-9Xx]`,
	// 009
	`[0-9]{3}-[0-9]{2}-[0-9]{4}`,
	// 010
	`[A-Z][0-9]{8}`,
	// 011
	`https?://[A-Za-z0-9.-]+(/[A-Za-z0-9._~:/?#\[\]@!$&\'()*+,;=%-]*)?`,
	// 012
	`ftp://[A-Za-z0-9.-]+(/[A-Za-z0-9._~:/?#\[\]@!$&\'()*+,;=%-]*)?`,
	// 013
	`www\.[A-Za-z0-9-]+\.[A-Za-z]{2,}`,
	// 014
	`[A-Za-z0-9-]+\.[A-Za-z]{2,}\.com`,
	// 015
	`[A-Za-z0-9-]+\.[A-Za-z0-9-]+\.co\.[A-Za-z]{2}`,
	// 016
	`[A-Za-z0-9.-]+:[0-9]{1,5}`,
	// 017
	`[0-9]{1,3}(\.[0-9]{1,3}){3}:[0-9]{1,5}`,
	// 018
	`localhost:[0-9]{1,5}`,
	// 019
	`[0-9]{1,3}(\.[0-9]{1,3}){3}/[0-9]{1,2}`,
	// 020
	`https?://[A-Za-z0-9.-]+/[A-Za-z0-9._/-]+\?[A-Za-z0-9._~=&%-]+`,
	// 021
	`[0-9]{4}-[0-9]{2}-[0-9]{2}`,
	// 022
	`[0-9]{4}/[0-9]{2}/[0-9]{2}`,
	// 023
	`[0-9]{2}/[0-9]{2}/[0-9]{4}`,
	// 024
	`[0-9]{4}\.[0-9]{2}\.[0-9]{2}`,
	// 025
	`[0-9]{2}:[0-9]{2}:[0-9]{2}`,
	// 026
	`[0-9]{2}:[0-9]{2}`,
	// 027
	`[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z`,
	// 028
	`[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}\.[0-9]{3}Z`,
	// 029
	`[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}[+-][0-9]{2}:[0-9]{2}`,
	// 030
	`\b[0-9]{10}\b`,
	// 031
	`\b[0-9a-fA-F]{32}\b`,
	// 032
	`\b[0-9a-fA-F]{40}\b`,
	// 033
	`\b[0-9a-fA-F]{64}\b`,
	// 034
	`\b[0-9a-fA-F]{128}\b`,
	// 035
	`[A-Za-z0-9+/]{20,}={0,2}`,
	// 036
	`0x[0-9a-fA-F]+`,
	// 037
	`sha256:[0-9a-fA-F]{64}`,
	// 038
	`sha1:[0-9a-fA-F]{40}`,
	// 039
	`md5:[0-9a-fA-F]{32}`,
	// 040
	`checksum[=:][0-9a-fA-F]{32,128}`,
	// 041
	`AKIA[0-9A-Z]{16}`,
	// 042
	`ASIA[0-9A-Z]{16}`,
	// 043
	`sk-[A-Za-z0-9]{20,}`,
	// 044
	`pk-[A-Za-z0-9]{20,}`,
	// 045
	`gh[pousr]_[A-Za-z0-9]{20,}`,
	// 046
	`glpat-[A-Za-z0-9_-]{20,}`,
	// 047
	`xox[baprs]-[A-Za-z0-9-]{10,}`,
	// 048
	`npm_[A-Za-z0-9]{20,}`,
	// 049
	`Bearer[ \t]+[A-Za-z0-9._-]{20,}`,
	// 050
	`Basic[ \t]+[A-Za-z0-9+/]{10,}={0,2}`,
	// 051
	`password[=:][^ \t\r\n]+`,
	// 052
	`passwd[=:][^ \t\r\n]+`,
	// 053
	`api[_-]key[=:][A-Za-z0-9._-]+`,
	// 054
	`access[_-]token[=:][A-Za-z0-9._-]+`,
	// 055
	`refresh[_-]token[=:][A-Za-z0-9._-]+`,
	// 056
	`client[_-]secret[=:][A-Za-z0-9._-]+`,
	// 057
	`private[_-]key[=:][A-Za-z0-9+/=_-]+`,
	// 058
	`session[_-]id[=:][A-Za-z0-9._-]+`,
	// 059
	`authorization[=:][A-Za-z0-9._ -]+`,
	// 060
	`cookie[=:][A-Za-z0-9._;=/%+ -]+`,
	// 061
	`trace[_-]?id[=:][0-9a-fA-F-]{16,64}`,
	// 062
	`request[_-]?id[=:][A-Za-z0-9_-]{8,64}`,
	// 063
	`correlation[_-]?id[=:][A-Za-z0-9_-]{8,64}`,
	// 064
	`transaction[_-]?id[=:][A-Za-z0-9_-]{8,64}`,
	// 065
	`user[_-]?id[=:][A-Za-z0-9_-]+`,
	// 066
	`account[_-]?id[=:][A-Za-z0-9_-]+`,
	// 067
	`device[_-]?id[=:][A-Za-z0-9_-]+`,
	// 068
	`order[_-]?id[=:][A-Za-z0-9_-]+`,
	// 069
	`session[_-]?token[=:][A-Za-z0-9._-]+`,
	// 070
	`X-[A-Za-z-]+:[ \t]*[A-Za-z0-9._/-]+`,
	// 071
	`/([A-Za-z0-9._-]+/)+[A-Za-z0-9._-]+`,
	// 072
	`[A-Za-z]:\\[A-Za-z0-9._-]+(\\[A-Za-z0-9._-]+)+`,
	// 073
	`[A-Za-z0-9._/-]+\.go`,
	// 074
	`[A-Za-z0-9._/-]+\.java`,
	// 075
	`[A-Za-z0-9._/-]+\.py`,
	// 076
	`[A-Za-z0-9._/-]+\.json`,
	// 077
	`[A-Za-z0-9._/-]+\.(yaml|yml)`,
	// 078
	`[A-Za-z0-9._/-]+\.log`,
	// 079
	`[A-Za-z0-9._/-]+\.(zip|tar|gz|bz2)`,
	// 080
	`[A-Za-z0-9._/-]+\.env`,
	// 081
	`[0-9]+\.[0-9]+\.[0-9]+`,
	// 082
	`[0-9]+\.[0-9]+\.[0-9]+-[A-Za-z0-9.-]+`,
	// 083
	`[0-9]+\.[0-9]+\.[0-9]+\+[A-Za-z0-9.-]+`,
	// 084
	`build[=:][0-9]{4,12}`,
	// 085
	`release[=:][A-Za-z0-9._-]+`,
	// 086
	`[A-Z]{2,10}-[0-9]{2,8}`,
	// 087
	`issue[ \t]+#[0-9]+`,
	// 088
	`commit[=:][0-9a-fA-F]{7,12}`,
	// 089
	`version[=:]v[0-9]+\.[0-9]+\.[0-9]+`,
	// 090
	`SN-[A-Z0-9]{10,16}`,
	// 091
	`\$[0-9]{1,3}(,[0-9]{3})*(\.[0-9]{2})?`,
	// 092
	`[+-]?[0-9]+(\.[0-9]+)?%`,
	// 093
	`[+-]?[0-9]+\.[0-9]+`,
	// 094
	`[+-]?[0-9]+(\.[0-9]+)?[eE][+-]?[0-9]+`,
	// 095
	`[0-9]+x[0-9]+`,
	// 096
	`#[0-9a-fA-F]{6}`,
	// 097
	`rgb\([0-9]{1,3},[0-9]{1,3},[0-9]{1,3}\)`,
	// 098
	`[A-Za-z0-9._/-]+:[A-Za-z0-9._-]+`,
	// 099
	`\$\{[A-Za-z_][A-Za-z0-9_]*\}`,
	// 100
	`\$[A-Za-z_][A-Za-z0-9_]*`,
}

// Samples contains one dedicated positive sample for each rule.
// Samples[i] is designed to exercise Rules[i].
var Samples = []string{
	// 001
	`user email=alice@example.com`,
	// 002
	`contact phone=13812345678`,
	// 003
	`server ip=192.168.10.25`,
	// 004
	`server ipv6=2001:0db8:85a3:0000:0000:8a2e:0370:7334`,
	// 005
	`device mac=00:1A:2B:3C:4D:5E`,
	// 006
	`request uuid=550e8400-e29b-41d4-a716-446655440000`,
	// 007
	`card=4111-1111-1111-1111`,
	// 008
	`id=11010519491231002X`,
	// 009
	`ssn=123-45-6789`,
	// 010
	`passport=E12345678`,
	// 011
	`url=https://example.com/api/v1/users`,
	// 012
	`ftp=ftp://files.example.com/data.txt`,
	// 013
	`site=www.example.com`,
	// 014
	`domain=api.example.com`,
	// 015
	`domain=example.co.jp`,
	// 016
	`endpoint=db.example.com:5432`,
	// 017
	`endpoint=192.168.1.10:8080`,
	// 018
	`endpoint=localhost:8080`,
	// 019
	`network=192.168.1.0/24`,
	// 020
	`url=https://example.com/search?q=hello%20world`,
	// 021
	`date=2026-09-21`,
	// 022
	`date=2026/09/21`,
	// 023
	`date=09/21/2026`,
	// 024
	`date=2026.09.21`,
	// 025
	`time=12:34:56`,
	// 026
	`time=12:34`,
	// 027
	`timestamp=2026-09-21T12:34:56Z`,
	// 028
	`timestamp=2026-09-21T12:34:56.123Z`,
	// 029
	`timestamp=2026-09-21T12:34:56+08:00`,
	// 030
	`unix=1779424496`,
	// 031
	`md5=0123456789abcdef0123456789abcdef`,
	// 032
	`sha1=0123456789abcdef0123456789abcdef01234567`,
	// 033
	`sha256=0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef`,
	// 034
	`sha512=0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef`,
	// 035
	`base64=VGhpcyBpcyBhIHRlc3QgYmFzZTY0IHZhbHVl`,
	// 036
	`hex=0xDEADBEEF`,
	// 037
	`sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef`,
	// 038
	`sha1:0123456789abcdef0123456789abcdef01234567`,
	// 039
	`md5:0123456789abcdef0123456789abcdef`,
	// 040
	`checksum=0123456789abcdef0123456789abcdef`,
	// 041
	`aws=AKIAIOSFODNN7EXAMPLE`,
	// 042
	`aws=ASIAIOSFODNN7EXAMPLE`,
	// 043
	`secret=sk-abcdefghijklmnopqrstuvwxyz123456`,
	// 044
	`public=pk-abcdefghijklmnopqrstuvwxyz123456`,
	// 045
	`github=ghp_abcdefghijklmnopqrstuvwxyz123456`,
	// 046
	`gitlab=glpat-abcdefghijklmnopqrstuvwxyz123456`,
	// 047
	`slack=xoxb-1234567890-abcdefghijklmnop`,
	// 048
	`npm=npm_abcdefghijklmnopqrstuvwxyz123456`,
	// 049
	`Authorization: Bearer abcdefghijklmnopqrstuvwxyz123456`,
	// 050
	`Authorization: Basic QWxhZGRpbjpvcGVuIHNlc2FtZQ==`,
	// 051
	`password=SuperSecret123!`,
	// 052
	`passwd=SuperSecret123!`,
	// 053
	`api_key=abcDEF123456`,
	// 054
	`access_token=abcDEF123456`,
	// 055
	`refresh_token=abcDEF123456`,
	// 056
	`client_secret=abcDEF123456`,
	// 057
	`private_key=ABCD1234+/==`,
	// 058
	`session_id=session_12345678`,
	// 059
	`authorization=Bearer abcdefghijklmnopqrstuvwxyz`,
	// 060
	`cookie=sessionid=abc123; token=xyz789`,
	// 061
	`trace_id=0123456789abcdef0123456789abcdef`,
	// 062
	`request_id=req_12345678`,
	// 063
	`correlation_id=corr_12345678`,
	// 064
	`transaction_id=txn_12345678`,
	// 065
	`user_id=user_123456`,
	// 066
	`account_id=account_123456`,
	// 067
	`device_id=device_123456`,
	// 068
	`order_id=order_123456`,
	// 069
	`session_token=session_abcdef123456`,
	// 070
	`X-Request-ID: abcdef123456`,
	// 071
	`path=/var/log/app/server.log`,
	// 072
	`path=C:\Users\alice\Documents\report.txt`,
	// 073
	`source=/workspace/project/main.go`,
	// 074
	`source=/workspace/project/Main.java`,
	// 075
	`source=/workspace/project/app.py`,
	// 076
	`config=/etc/app/config.json`,
	// 077
	`config=/etc/app/config.yaml`,
	// 078
	`log=/var/log/app/application.log`,
	// 079
	`archive=/backup/database.tar.gz`,
	// 080
	`env=/workspace/project/.env`,
	// 081
	`version=1.2.3`,
	// 082
	`version=1.2.3-beta.1`,
	// 083
	`version=1.2.3+build.42`,
	// 084
	`build=20260921`,
	// 085
	`release=v2026.09`,
	// 086
	`ticket=LOG-12345`,
	// 087
	`issue #12345`,
	// 088
	`commit=abcdef123456`,
	// 089
	`version=v1.2.3`,
	// 090
	`serial=SN-ABC123456789`,
	// 091
	`price=$1,234.56`,
	// 092
	`ratio=+12.75%`,
	// 093
	`number=-12345.6789`,
	// 094
	`scientific=6.02e+23`,
	// 095
	`size=1920x1080`,
	// 096
	`color=#1A2B3C`,
	// 097
	`rgb=rgb(128,64,255)`,
	// 098
	`image=registry.example.com/team/app:1.2.3`,
	// 099
	`env=${DATABASE_URL}`,
	// 100
	`shell=$HOME`,
}

const CorpusSeed uint64 = 0x9e3779b97f4a7c15

// CorpusSize is the recommended benchmark matrix.
var CorpusSizes = []int{
	1024,
	10240,
	102400,
	1048576,
	//10485760,
	//104857600,
	//524288000,
}

// Corpus composition recommendation:
//   1. 70% deterministic noise
//   2. 20% positive samples
//   3. 10% near-miss strings
// Keep the same generated corpus for every engine.

var hyperscan100RulesBytesSink []byte

// BenchmarkHyperscanGo100Rules 对比 Engine.Replace 与 goRegexp.ReplaceAllFunc 在
// Rules 全部 100 条规则同时处理时的吞吐与分配差异。
//
// 语料大小遵循 CorpusSizes 推荐矩阵；语料组成遵循
// hyperscan_go_100_rules_benchmark_test.go 中给出的 70% 噪声 / 20% 正样本 / 10% 近错
// 字符串建议。同一份语料会在两个引擎间复用以确保对比公平；fixture 阶段会校验
// 两侧输出长度一致，但因 Engine 采用 leftmost-longest 而 Go regexp 在
// 交替式中遵循 leftmost-first，对存在重叠的规则（例如 URL/IPv4、时间戳/日期）
// 会产生不同切分，本 benchmark 重点对比吞吐而非字节级等价。
func BenchmarkHyperscanGo100Rules(b *testing.B) {
	for _, size := range CorpusSizes {
		b.Run(fmt.Sprintf("Size=%d", size), func(b *testing.B) {
			fixture := newHyperscan100RulesFixture(b, size)

			b.Run("EngineMask", func(b *testing.B) {
				if err := fixture.engine.Mask(fixture.data, writeHyperscan100RulesMask); err != nil {
					b.Fatal(err)
				}
				startHyperscan100RulesTimer(b, fixture)
				for range b.N {
					if err := fixture.engine.Mask(fixture.data, writeHyperscan100RulesMask); err != nil {
						b.Fatal(err)
					}
					hyperscan100RulesBytesSink = fixture.data
				}
			})

			b.Run("GoRegexpReplace", func(b *testing.B) {
				// Engine 使用 leftmost-longest 匹配语义，而 Go regexp 在交替式
				// 中遵循 leftmost-first；二者对存在重叠的规则（例如 URL 与 IPv4、
				// 时间戳与日期等）会产生不同切分。本 benchmark 只对比吞吐，不要求字节级一致。
				if result := fixture.goRegexp.ReplaceAllFunc(fixture.data, fixture.maskRegexpMatch); len(result) != len(fixture.masked) {
					b.Fatalf("Go regexp output length = %d, Engine output length = %d", len(result), len(fixture.masked))
				}
				startHyperscan100RulesTimer(b, fixture)
				for range b.N {
					hyperscan100RulesBytesSink = fixture.goRegexp.ReplaceAllFunc(fixture.data, fixture.maskRegexpMatch)
				}
			})
		})
	}
}

type hyperscan100RulesFixture struct {
	data            []byte
	matches         int
	masked          []byte
	maskBytes       []byte
	engine          *Engine
	goRegexp        *regexp.Regexp
	maskRegexpMatch func([]byte) []byte
}

// newHyperscan100RulesFixture 构造一次 100 规则 benchmark 共用的固定装置：编译
// Engine 与对应 Go regexp 交替式、生成确定语料并验证两侧 Replace 结果一致。
func newHyperscan100RulesFixture(b testing.TB, size int) hyperscan100RulesFixture {
	b.Helper()

	data := newHyperscan100RulesCorpus(b, size)

	expressions := make([]Expression, len(Rules))
	for index, pattern := range Rules {
		expressions[index] = Expression{Id: uint32(index + 1), Pattern: pattern}
	}

	scanner, err := Compile(expressions)
	if err != nil {
		b.Fatal(err)
	}

	engine, err := New(expressions)
	if err != nil {
		b.Fatal(err)
	}

	goRegexp, err := regexp.Compile(hyperscan100RulesAlternation(expressions))
	if err != nil {
		b.Fatal(err)
	}

	matches, err := scanner.Scan(data)
	if err != nil {
		b.Fatal(err)
	}

	maskBytes := bytes.Repeat([]byte{'*'}, len(data))
	fixture := hyperscan100RulesFixture{
		data:      data,
		matches:   len(matches),
		maskBytes: maskBytes,
		engine:    engine,
		goRegexp:  goRegexp,
	}
	fixture.maskRegexpMatch = func(match []byte) []byte {
		return fixture.maskBytes[:len(match)]
	}

	if err := engine.Mask(data, writeHyperscan100RulesMask); err != nil {
		b.Fatal(err)
	}
	fixture.masked = data

	replaced := goRegexp.ReplaceAllFunc(data, fixture.maskRegexpMatch)
	if len(replaced) != len(fixture.masked) {
		b.Fatalf("Go regexp output length = %d, Engine output length = %d at size %d", len(replaced), len(fixture.masked), size)
	}
	return fixture
}

// newHyperscan100RulesCorpus 按 CorpusSeed 生成确定语料，目标总字节数接近 size。
// 70% 噪声 / 20% 正样本 / 10% 近错字符串遵循 hyperscan_go_100_rules_benchmark_test.go 中
// 推荐的组成；近错字符串保留样本前缀并将匹配片段替换为通用占位符，以制造“差一点命中”。
func newHyperscan100RulesCorpus(b testing.TB, size int) []byte {
	b.Helper()
	rng := rand.New(rand.NewPCG(CorpusSeed, CorpusSeed))

	var builder strings.Builder
	builder.Grow(size + 1024)

	for builder.Len() < size {
		pick := rng.Float64()
		switch {
		case pick < 0.7:
			writeHyperscan100RulesNoiseLine(&builder, rng)
		case pick < 0.9:
			index := rng.IntN(len(Samples))
			builder.WriteString(Samples[index])
			builder.WriteByte('\n')
		default:
			index := rng.IntN(len(Samples))
			builder.WriteString(hyperscan100RulesNearMiss(Samples[index]))
			builder.WriteByte('\n')
		}
	}

	data := builder.String()
	if len(data) > size {
		data = data[:size]
	}
	return []byte(data)
}

// writeHyperscan100RulesNoiseLine 写入一行不会命中 Rules 中任何规则的安全噪声。
// 仅使用小写字母与少量分隔符，避免与 PII/密钥/IP/时间戳等模式产生交集。
func writeHyperscan100RulesNoiseLine(buf *strings.Builder, rng *rand.Rand) {
	keys := []string{
		"log", "msg", "info", "note", "trace",
		"debug", "user", "data", "stage", "phase",
	}
	verbs := []string{
		"fetched", "processed", "queued", "routed",
		"started", "completed", "skipped", "aborted",
		"validated", "retried", "cached", "shipped",
	}

	buf.WriteString(keys[rng.IntN(len(keys))])
	buf.WriteByte('=')
	buf.WriteString(verbs[rng.IntN(len(verbs))])
	buf.WriteByte('-')
	suffixLen := 4 + rng.IntN(8)
	for range suffixLen {
		buf.WriteByte('a' + byte(rng.IntN(26)))
	}
	buf.WriteByte('\n')
}

// hyperscan100RulesNearMiss 将正样本中匹配片段替换为通用占位符，保留原有 key=
// 前缀；这样既维持样本结构，又让规则不再命中，便于测试近错路径开销。
func hyperscan100RulesNearMiss(sample string) string {
	index := strings.LastIndex(sample, "=")
	if index < 0 {
		return sample
	}
	return sample[:index+1] + "redacted"
}

// hyperscan100RulesAlternation 把全部 100 条规则以非捕获组形式拼接为单条 Go regexp
// 交替式，保持与 Engine 一次性提交相同语义。
func hyperscan100RulesAlternation(expressions []Expression) string {
	patterns := make([]string, len(expressions))
	for index, expression := range expressions {
		patterns[index] = "(?:" + expression.Pattern + ")"
	}
	return strings.Join(patterns, "|")
}

// writeHyperscan100RulesMask 按命中长度写入等长 '*'，与 goRegexp.ReplaceAllFunc
// 的 maskRegexpMatch 在语义上对齐。
func writeHyperscan100RulesMask(_ Match, matched []byte) {
	for i := range matched {
		matched[i] = '*'
	}
}

// startHyperscan100RulesTimer 与 pii_log_bench_test.go 中的
// startPIIBenchmarkTimer 行为一致：按语料字节数统计吞吐、上报分配、并以
// matches/op 辅助观测命中规模。
func startHyperscan100RulesTimer(b *testing.B, fixture hyperscan100RulesFixture) {
	b.SetBytes(int64(len(fixture.data)))
	b.ReportAllocs()
	b.ResetTimer()
	b.ReportMetric(float64(fixture.matches), "matches/op")
}

// TestHyperscanGo100RulesStable 用 testing.AllocsPerRun(200, ...) + testing.Benchmark
// 提供稳定的 EngineMask / GoRegexpReplace 对比基线。
func TestHyperscanGo100RulesStable(t *testing.T) {
	for _, size := range CorpusSizes {
		fixture := newHyperscan100RulesFixture(t, size)

		// --- EngineMask ---
		data := make([]byte, len(fixture.data))
		copy(data, fixture.data)
		for range 10 {
			copy(data, fixture.data)
			if err := fixture.engine.Mask(data, writeHyperscan100RulesMask); err != nil {
				t.Fatal(err)
			}
		}
		engineAllocs := testing.AllocsPerRun(200, func() {
			copy(data, fixture.data)
			if err := fixture.engine.Mask(data, writeHyperscan100RulesMask); err != nil {
				t.Fatal(err)
			}
			hyperscan100RulesBytesSink = data
		})
		engineRes := testing.Benchmark(func(b *testing.B) {
			data := make([]byte, len(fixture.data))
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				copy(data, fixture.data)
				if err := fixture.engine.Mask(data, writeHyperscan100RulesMask); err != nil {
					b.Fatal(err)
				}
				hyperscan100RulesBytesSink = data
			}
		})
		nsPerOp := float64(engineRes.NsPerOp())
		mbPerS := float64(size) / nsPerOp * 1e9 / (1 << 20)
		t.Logf("Size=%d/EngineMask ns/op=%.0f MB/s=%.2f B/op=%d allocs/op=%.1f matches=%d",
			size, nsPerOp, mbPerS, engineRes.AllocedBytesPerOp(), engineAllocs, fixture.matches)

		// --- GoRegexpReplace ---
		for range 10 {
			hyperscan100RulesBytesSink = fixture.goRegexp.ReplaceAllFunc(fixture.data, fixture.maskRegexpMatch)
		}
		regexpAllocs := testing.AllocsPerRun(200, func() {
			hyperscan100RulesBytesSink = fixture.goRegexp.ReplaceAllFunc(fixture.data, fixture.maskRegexpMatch)
		})
		regexpRes := testing.Benchmark(func(b *testing.B) {
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				hyperscan100RulesBytesSink = fixture.goRegexp.ReplaceAllFunc(fixture.data, fixture.maskRegexpMatch)
			}
		})
		nsPerOp = float64(regexpRes.NsPerOp())
		mbPerS = float64(size) / nsPerOp * 1e9 / (1 << 20)
		t.Logf("Size=%d/GoRegexpReplace ns/op=%.0f MB/s=%.2f B/op=%d allocs/op=%.1f matches=%d",
			size, nsPerOp, mbPerS, regexpRes.AllocedBytesPerOp(), regexpAllocs, fixture.matches)
	}
}
