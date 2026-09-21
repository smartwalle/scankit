package scankit

import (
	"strings"
	"testing"
)

// requiredIndexPatterns 覆盖带字段锚点的多条规则：既有整块后端可处理的纯类规则，
// 也包含只能靠 AST 确认的断言规则，确保两条确认路径都参与候选驱动扫描。
func requiredIndexPatterns() []Expression {
	return []Expression{
		{Id: 1, Pattern: `field00=1[3-9][0-9]{9}`},
		{Id: 2, Pattern: `field01=\b(?:86)?1[3-9][0-9]{9}\b`},
		{Id: 3, Pattern: `field02=[A-Za-z0-9.]{1,16}@[A-Za-z0-9]{1,8}\b`},
		{Id: 4, Pattern: `field03=[1-9][0-9]{5}(18|19|20)[0-9]{2}(0[1-9]|1[0-2])(0[1-9]|[12][0-9]|3[01])[0-9]{3}[0-9Xx]`},
	}
}

func requiredIndexCorpus() []byte {
	return []byte("ts=2026-08-25 level=INFO field00=13800138000 done\n" +
		"ts=2026-08-25 level=INFO field01=12112312311 done\n" +
		"ts=2026-08-25 level=INFO field02=user@example.com done\n" +
		"ts=2026-08-25 level=INFO field03=110105200001010025 done\n" +
		"ts=2026-08-25 level=INFO field03=11010520000101002Z done\n" +
		"ts=2026-08-25 level=INFO order=20260825123456 done\n")
}

// TestRequiredIndexScanSkipsLiteralIndex 验证必须文字候选索引覆盖全部规则时不再
// 重复执行共享文字索引，并断言两条路径的命中集合逐位一致。
func TestRequiredIndexScanSkipsLiteralIndex(t *testing.T) {
	scanner, err := Compile(requiredIndexPatterns())
	if err != nil {
		t.Fatal(err)
	}
	if scanner.requiredFindInto == nil {
		t.Fatal("带字段锚点的规则集应建立必须文字候选索引")
	}
	for i := range scanner.rules {
		if !scanner.requiredCovered[i] {
			t.Fatalf("规则 %d 应被必须文字候选索引覆盖", i)
		}
	}
	if scanner.literalFind == nil {
		t.Fatal("测试语料应同时具备共享文字索引")
	}
	data := requiredIndexCorpus()
	want, err := scanner.Scan(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(want) == 0 {
		t.Fatal("测试语料应至少产生一条命中")
	}
	clone := *scanner
	called := 0
	clone.literalFindInto = func(_ []byte, _ []literalCandidate) []literalCandidate {
		called++
		return nil
	}
	got, err := clone.Scan(data)
	if err != nil {
		t.Fatal(err)
	}
	if called != 0 {
		t.Fatalf("必须文字候选索引已覆盖全部规则，不应重复执行共享文字索引: 调用 %d 次", called)
	}
	if len(got) != len(want) {
		t.Fatalf("候选驱动路径命中数量不一致: got=%d want=%d", len(got), len(want))
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("第 %d 个命中不一致: got=%v want=%v", i, got[i], want[i])
		}
	}
}

// TestRequiredIndexFallbackKeepsLiteralFilter 验证候选起点超出上限而退回逐起点
// 确认时，会补建共享文字索引作为过滤，而不是退化成“每个起点 × 每条规则”。
func TestRequiredIndexFallbackKeepsLiteralFilter(t *testing.T) {
	const ruleCount = 40
	expressions := make([]Expression, 0, ruleCount)
	for index := range ruleCount {
		expressions = append(expressions, Expression{Id: uint32(index + 1), Pattern: `a[0-9]{3}`})
	}
	scanner, err := Compile(expressions)
	if err != nil {
		t.Fatal(err)
	}
	if scanner.requiredFindInto == nil {
		t.Fatal("共享锚点文字的规则集应建立必须文字候选索引")
	}
	data := []byte(strings.Repeat("a0", 2048))
	ctx := scanner.contextPool.Get().(*scanContext)
	_, ok := scanner.requiredScanStarts(ctx, data, true, true)
	scanner.contextPool.Put(ctx)
	if ok {
		t.Fatal("候选起点密集时应触发上限回退")
	}
	got, err := scanner.Scan(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("a0a0 语料不应命中 a[0-9]{3}: got=%v", got)
	}
	hit, err := scanner.Scan([]byte("a123"))
	if err != nil {
		t.Fatal(err)
	}
	if len(hit) != ruleCount {
		t.Fatalf("回退路径应命中全部同形规则: got=%d want=%d", len(hit), ruleCount)
	}
}

// perStartReferenceScanner 返回禁用候选索引与整块后端扫描的参照扫描器：
// 全部规则都走逐起点确认，用于逐位比对候选驱动路径的命中集合与顺序。
func perStartReferenceScanner(scanner *Scanner) *Scanner {
	reference := *scanner
	reference.requiredFindInto = nil
	reference.requiredCovered = nil
	reference.backendOnly = false
	reference.buildConfirmRuleLists()
	reference.startBytes = nil
	reference.startBytesAll = nil
	return &reference
}

func assertScanAgreesWithReference(t *testing.T, scanner *Scanner, corpus [][]byte) {
	t.Helper()
	reference := perStartReferenceScanner(scanner)
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

// TestRequiredIndexBackPrefixBoundsWindow 验证类重复前缀把候选起点收缩到命中
// 位置左侧的连续类区间，而不是整个数据窗口。
func TestRequiredIndexBackPrefixBoundsWindow(t *testing.T) {
	scanner, err := Compile([]Expression{{Id: 1, Pattern: `[A-Za-z]{1,16}@[a-z]{2,4}\b`}})
	if err != nil {
		t.Fatal(err)
	}
	if scanner.requiredFindInto == nil || len(scanner.requiredLiterals) != 1 {
		t.Fatal("类重复前缀规则应建立必须文字候选索引")
	}
	if entry := scanner.requiredLiterals[0].entries[0]; entry.back == nil {
		t.Fatal("类重复前缀规则应携带左侧字节集合")
	}
	ctx := scanner.contextPool.Get().(*scanContext)
	starts, ok := scanner.requiredScanStarts(ctx, []byte("ts=abcde@xy done"), false, false)
	scanner.contextPool.Put(ctx)
	if !ok {
		t.Fatal("候选起点未超出上限时不应回退")
	}
	want := []int{3, 4, 5, 6, 7}
	if len(starts) != len(want) {
		t.Fatalf("候选起点数量应为 %d: %v", len(want), starts)
	}
	for index, key := range starts {
		start, rule := requiredStartParts(key)
		if start != want[index] || rule != 0 {
			t.Fatalf("第 %d 个候选起点应为 (%d, 0): got=(%d, %d)", index, want[index], start, rule)
		}
	}
}

// TestRequiredIndexBackPrefixAgreesWithPerStartScan 断言类区间内的候选起点与
// 逐起点确认结果完全一致，包含最左起点被前一次命中抑制的情形。
func TestRequiredIndexBackPrefixAgreesWithPerStartScan(t *testing.T) {
	scanner, err := Compile([]Expression{{Id: 1, Pattern: `[A-Za-z]{1,16}@[a-z]{2,4}\b`}})
	if err != nil {
		t.Fatal(err)
	}
	if scanner.requiredFindInto == nil {
		t.Fatal("类重复前缀规则应建立必须文字候选索引")
	}
	assertScanAgreesWithReference(t, scanner, [][]byte{
		[]byte("ts=abcde@xy done"),
		[]byte("ts=@xy done"),
		[]byte("ts=a@abc done"),
		[]byte("ts=abcdefghijklmnopq@ab done"),
		[]byte("ts=abc@xy ts=de@abc done"),
		[]byte("no anchor here"),
	})
}

// TestRequiredIndexBackPrefixAfterSuppression 覆盖最左起点被抑制后区间内更靠右
// 的起点仍会产生命中的情形：候选索引必须保留同一段类区间，否则会漏报。
func TestRequiredIndexBackPrefixAfterSuppression(t *testing.T) {
	scanner, err := Compile([]Expression{{Id: 1, Pattern: `[A-Za-z0-9.-]+:[0-9]{1,5}`}, {Id: 2, Pattern: `[0-9a-fA-F]{32}`}})
	if err != nil {
		t.Fatal(err)
	}
	if scanner.requiredFindInto == nil {
		t.Fatal("类重复前缀规则应建立必须文字候选索引")
	}
	assertScanAgreesWithReference(t, scanner, [][]byte{
		[]byte("server ipv6=2001:0db8:85a3:0000:0000:8a2e:0370:7334\n"),
		[]byte("host 192.168.1.10:8080 and 10.0.0.1:22"),
		[]byte("port:1234 node-1.example.com:443"),
		[]byte("a:b c:12 d:3456789"),
		[]byte("no colon here"),
	})
}
