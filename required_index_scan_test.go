package scankit

import (
	"bytes"
	"fmt"
	"math/rand/v2"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/smartwalle/scankit/internal/dispatch"
	"github.com/smartwalle/scankit/internal/hwlm"
	"github.com/smartwalle/scankit/internal/simd"
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
		// 首字节用分支而非固定文字，避免前缀字节约束提前拦掉全部候选，
		// 保证本用例真正走到“候选数量超过上限”的回退分支。
		expressions = append(expressions, Expression{Id: uint32(index + 1), Pattern: `(?:a|b)[0-9]{3}`})
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
	_, ok := scanner.requiredScanStarts(ctx, data)
	scanner.contextPool.Put(ctx)
	if ok {
		t.Fatal("候选起点密集时应触发上限回退")
	}
	got, err := scanner.Scan(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("a0a0 语料不应命中 (?:a|b)[0-9]{3}: got=%v", got)
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
	reference.guardRunCovered = nil
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
	starts, ok := scanner.requiredScanStarts(ctx, []byte("ts=abcde@xy done"))
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

// TestRequiredIndexGuardRunWindowAgreesWithPerStartScan 验证"局部同集合窗口"
// （窗口不从头开始，或只覆盖规则前缀的一部分）的规则改用约束枚举起点后，命中
// 集合仍与逐起点确认逐位一致。
func TestRequiredIndexGuardRunWindowAgreesWithPerStartScan(t *testing.T) {
	scanner, err := Compile([]Expression{
		{Id: 1, Pattern: `[A-Z][0-9]{8}`},
		{Id: 2, Pattern: `[0-9]{17}[0-9Xx]`},
		{Id: 3, Pattern: `[A-Za-z0-9._/-]+:[0-9]{1,5}`},
		{Id: 4, Pattern: `sha256:[0-9a-fA-F]{64}`},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !scanner.guardRunCovers(0) || !scanner.guardRunCovers(1) {
		t.Fatal("带局部定长重复的规则应改由前缀字节约束枚举候选起点")
	}
	assertScanAgreesWithReference(t, scanner, [][]byte{
		[]byte("code A12345678 and B00000001 ok"),
		[]byte("code A1234567B and A1234567 ok"),
		[]byte("id 12345678901234567X ok"),
		[]byte("id 123456789012345678 ok"),
		[]byte("host 192.168.1.10:8080 no-op"),
		[]byte("digest sha256:" + strings.Repeat("0a", 32) + " end"),
		[]byte("no candidate here"),
	})
}

// TestRequiredIndexGuardRunWindowOffsets 验证约束枚举只在窗口完整落在同集合连续段
// 内时派生起点，窗口右侧越界的位置不能成为候选。
func TestRequiredIndexGuardRunWindowOffsets(t *testing.T) {
	scanner, err := Compile([]Expression{
		{Id: 1, Pattern: `[A-Z][0-9]{8}`},
		{Id: 2, Pattern: `[0-9]{17}[0-9Xx]`},
	})
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name string
		data string
		want map[int][]int
	}{
		{
			// 规则 1 的数字窗口是 [起点+1, 起点+9)：数字连续段 [2,10) 只允许起点 1；
			// 规则 2 需要 17 个数字，此处没有候选。
			name: "offset window",
			data: "AB12345678",
			want: map[int][]int{0: {1}},
		},
		{
			// 数字连续段 [1,18)：规则 1 的窗口允许起点 0..9，但完整前缀约束还要求
			// 起点处是大写字母，只有起点 0 成立；规则 2 的 17 字节窗口只允许起点 1。
			name: "window inside long run",
			data: "A12345678901234567X",
			want: map[int][]int{0: {0}, 1: {1}},
		},
		{
			// 数字连续段不足窗口长度时不派生任何候选。
			name: "too short run",
			data: "A1234567",
			want: map[int][]int{},
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			starts, ok := scanner.appendGuardRunStarts([]byte(testCase.data), nil, 1<<20)
			if !ok {
				t.Fatal("候选起点未超出上限时不应回退")
			}
			got := map[int][]int{}
			for _, key := range starts {
				rule := int(uint32(key))
				start := int(uint32(key >> 32))
				got[rule] = append(got[rule], start)
			}
			if len(got) != len(testCase.want) {
				t.Fatalf("候选规则集合=%v，期望 %v", got, testCase.want)
			}
			for rule, want := range testCase.want {
				value, ok := got[rule]
				if !ok || len(value) != len(want) {
					t.Fatalf("规则 %d 候选=%v，期望 %v", rule, value, want)
				}
				for index := range want {
					if value[index] != want[index] {
						t.Fatalf("规则 %d 候选=%v，期望 %v", rule, value, want)
					}
				}
			}
		})
	}
}

// TestRequiredIndexSplitsSingleByteLiterals 验证候选文字按长度拆分后，单字节文字
// 与多字节文字仍共同产出候选起点，扫描结果与逐起点确认一致。
func TestRequiredIndexSplitsSingleByteLiterals(t *testing.T) {
	short, long := splitRequiredLiterals([]hwlm.Literal{
		{ID: 1, Value: []byte(":")},
		{ID: 2, Value: []byte(".go")},
		{ID: 3, Value: []byte("#")},
		{ID: 4, Value: []byte("sha256:")},
	})
	if len(short) != 2 || short[0].Value[0] != ':' || short[1].Value[0] != '#' {
		t.Fatalf("单字节分组=%v", short)
	}
	if len(long) != 2 || string(long[0].Value) != ".go" || string(long[1].Value) != "sha256:" {
		t.Fatalf("多字节分组=%v", long)
	}

	scanner, err := Compile([]Expression{
		{Id: 1, Pattern: `#[0-9a-fA-F]{6}`},
		{Id: 2, Pattern: `[A-Za-z0-9._/-]+\.go`},
		{Id: 3, Pattern: `sha256:[0-9a-fA-F]{64}`},
		{Id: 4, Pattern: `[A-Za-z0-9.-]+:[0-9]{1,5}`},
	})
	if err != nil {
		t.Fatal(err)
	}
	if scanner.requiredFindInto == nil {
		t.Fatal("应建立必须文字候选索引")
	}
	assertScanAgreesWithReference(t, scanner, [][]byte{
		[]byte("color #1a2b3c and path a/b.go"),
		[]byte("digest sha256:" + strings.Repeat("ab", 32) + " end"),
		[]byte("host 10.0.0.1:8080 end"),
		[]byte("#000000 #ffffff"),
		[]byte("no candidate here"),
	})
}

// TestRequiredIndexGuardRunKeepsEntryLiteral 验证约束枚举只校验约束窗口、不校验
// 规则入口文字，因此确认程序不能跳过入口指令：窗口命中但入口字节不匹配的位置
// 必须走完整确认，不能产生误报。
func TestRequiredIndexGuardRunKeepsEntryLiteral(t *testing.T) {
	scanner, err := Compile([]Expression{{Id: 1, Pattern: `1[3-9][0-9]{9}`}})
	if err != nil {
		t.Fatal(err)
	}
	if !scanner.guardRunCovers(0) {
		t.Fatal("定长数字窗口应改由前缀字节约束枚举候选起点")
	}
	if scanner.literalIndexCovered(0) {
		t.Fatal("改由约束枚举的规则不应保留必须文字索引覆盖标记")
	}
	cases := []struct {
		name string
		data string
		want []Match
	}{
		{
			// 数字窗口 [2,11) 命中，但入口字节是 '-' 而非 '1'。
			name: "entry literal missing",
			data: "-4466554400",
			want: nil,
		},
		{
			name: "entry literal present",
			data: "13812345678",
			want: []Match{{Id: 1, From: 0, To: 11}},
		},
		{
			name: "window hit inside longer noise",
			data: "x-4466554400y",
			want: nil,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			data := []byte(testCase.data)
			got, err := scanner.Scan(data)
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != len(testCase.want) {
				t.Fatalf("命中数量不一致: got=%v want=%v", got, testCase.want)
			}
			for index := range got {
				if got[index] != testCase.want[index] {
					t.Fatalf("第 %d 个命中不一致: got=%v want=%v", index, got[index], testCase.want[index])
				}
			}
			assertScanAgreesWithReference(t, scanner, [][]byte{data})
		})
	}
}

// TestGuardRunStartsMatchesScalar 对宽窗口掩码路径与逐字节回退路径做差分：连续段
// 跨宽窗口边界、尾部不足一个宽窗口、多个段同处一个窗口、以及窗口完全落在段内或
// 越过段端点等情形都必须给出完全相同的候选起点集合。
func TestGuardRunStartsMatchesScalar(t *testing.T) {
	var digitSet [4]uint64
	for value := byte('0'); value <= '9'; value++ {
		digitSet[value>>6] |= uint64(1) << uint(value&63)
	}
	group := guardRunGroup{
		set:    digitSet,
		tables: simd.NewByteSetTables([4][4]uint64{digitSet}),
		runs: []guardRun{
			{ruleIndex: 0, set: digitSet, offset: 0, length: 4},
			{ruleIndex: 1, set: digitSet, offset: 2, length: 8},
			{ruleIndex: 2, set: digitSet, offset: 5, length: 17},
		},
		// minLength 与 groupGuardRuns 的构造保持一致（runs 已按长度升序），
		// 这样差分用例同时覆盖"段长不足最短窗口直接跳过"的分支。
		minLength: 4,
	}
	backend := dispatch.DefaultBackend()
	const limit = 1 << 20
	check := func(name string, data []byte) {
		t.Helper()
		want, ok := group.appendStartsScalar(data, nil, limit)
		if !ok {
			t.Fatalf("%s: 参照实现不应溢出上限", name)
		}
		got, ok := group.appendStarts(backend, data, nil, limit)
		if !ok {
			t.Fatalf("%s: 宽窗口实现不应溢出上限", name)
		}
		if len(got) != len(want) {
			t.Fatalf("%s: 候选数量不一致 got=%d want=%d", name, len(got), len(want))
		}
		sort.Slice(got, func(i, j int) bool { return got[i] < got[j] })
		sort.Slice(want, func(i, j int) bool { return want[i] < want[j] })
		for index := range got {
			if got[index] != want[index] {
				t.Fatalf("%s: 第 %d 个候选不一致 got=%v want=%v", name, index, got[index], want[index])
			}
		}
	}

	const size = 320
	starts := []int{0, 1, 2, 3, 55, 61, 62, 63, 64, 65, 66, 67, 120, 126, 127, 128, 129, 130, 190, 252, 300, 315, 319}
	lengths := []int{1, 2, 3, 4, 5, 7, 8, 9, 16, 17, 18, 31, 32, 33, 63, 64, 65, 70}
	for _, start := range starts {
		for _, length := range lengths {
			if start+length > size {
				continue
			}
			data := bytes.Repeat([]byte("x"), size)
			for index := start; index < start+length; index++ {
				data[index] = '0' + byte(index%10)
			}
			check(fmt.Sprintf("start=%d length=%d", start, length), data)
		}
	}

	// 同一窗口内的多个短段、间隔 1 字节的段、以及贯穿多个宽窗口的长段。
	mixed := []byte(strings.Repeat("x", size))
	for _, span := range [][2]int{{0, 3}, {5, 3}, {10, 20}, {40, 1}, {62, 70}, {200, 40}, {260, 3}, {300, 20}} {
		for index := span[0]; index < span[0]+span[1]; index++ {
			mixed[index] = '0' + byte(index%10)
		}
	}
	check("mixed", mixed)
	check("empty", nil)
	check("all set", bytes.Repeat([]byte("7"), size))
	check("no set", bytes.Repeat([]byte("x"), size))
}

// TestSortRequiredStartsMatchesSlicesSort 用确定性随机构造覆盖分桶排序的各类
// 规模：命中计数门槛之下走比较排序，之上走分桶加桶内插入排序，两者结果必须与
// slices.Sort 逐位一致。
func TestSortRequiredStartsMatchesSlicesSort(t *testing.T) {
	cases := []struct {
		name     string
		keyCount int
		dataLen  int
		spread   int
	}{
		{name: "低于门槛", keyCount: 64, dataLen: 4096},
		{name: "正好一个下限", keyCount: 8191, dataLen: 65536},
		{name: "单字节起点", keyCount: 8192, dataLen: 300},
		{name: "密集起点", keyCount: 20000, dataLen: 4096},
		{name: "稀疏起点", keyCount: 20000, dataLen: 1 << 20},
		{name: "同起点多规则", keyCount: 12000, dataLen: 1024},
		{name: "超过键上限", keyCount: requiredCountingSortMaxKeys + 1, dataLen: 1 << 20},
		{name: "超过语料上限", keyCount: 16000, dataLen: requiredCountingSortMaxData + 1},
	}
	rng := rand.New(rand.NewPCG(0x5deece66d, 0x1234567))
	scanner := &Scanner{}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			starts := make([]uint64, testCase.keyCount)
			for index := range starts {
				start := rng.IntN(testCase.dataLen)
				if testCase.keyCount > testCase.dataLen {
					// 起点数量超过语料长度时制造同起点多规则的密集重复，
					// 让桶内插入排序面对大量相等键。
					start = index % testCase.dataLen
				}
				starts[index] = requiredStartKey(start, rng.IntN(64))
			}
			want := append([]uint64(nil), starts...)
			slices.Sort(want)
			ctx := &scanContext{}
			scanner.sortRequiredStarts(ctx, starts, testCase.dataLen)
			for index := range starts {
				if starts[index] != want[index] {
					t.Fatalf("第 %d 个键不一致: got=%d want=%d", index, starts[index], want[index])
				}
			}
		})
	}
}

// TestSortRequiredStartsEndOfDataStart 固定住分桶下标的边界：起点取到数据
// 长度本身（数据末尾起点）时仍必须落在桶范围内。
func TestSortRequiredStartsEndOfDataStart(t *testing.T) {
	const dataLen = 4096
	starts := make([]uint64, 0, 8192)
	for start := 0; start <= dataLen; start++ {
		starts = append(starts, requiredStartKey(start, 0))
		starts = append(starts, requiredStartKey(start, 1))
	}
	want := append([]uint64(nil), starts...)
	slices.Sort(want)
	scanner := &Scanner{}
	scanner.sortRequiredStarts(&scanContext{}, starts, dataLen)
	for index := range starts {
		if starts[index] != want[index] {
			t.Fatalf("第 %d 个键不一致: got=%d want=%d", index, starts[index], want[index])
		}
	}
}

// TestRequiredIndexGuardRunMergesWithStartByteIndex 覆盖"只有前缀字节约束枚举
// 候选、同时存在需要首字节索引的未覆盖规则"这一混合形态：约束候选必须与候选
// 文字路径一样按 (起点, 规则) 升序整理，否则与首字节索引归并时会错过出现在
// 数据靠前位置的约束候选而漏报。
func TestRequiredIndexGuardRunMergesWithStartByteIndex(t *testing.T) {
	scanner, err := Compile([]Expression{
		{Id: 1, Pattern: `\b[0-9]{10}\b`},
		{Id: 2, Pattern: `\b[0-9a-fA-F]{32}\b`},
		{Id: 3, Pattern: `(?i)[a-z]{5}`},
	})
	if err != nil {
		t.Fatal(err)
	}
	if scanner.requiredFindInto != nil {
		t.Fatal("该规则集合不应建立必须文字候选索引")
	}
	if scanner.startBytes == nil {
		t.Fatal("大小写折叠规则应保留首字节索引")
	}
	for index := range scanner.rules[:2] {
		if !scanner.guardRunCovers(index) {
			t.Fatalf("规则 %d 应由前缀字节约束枚举候选起点", index)
		}
	}
	digest := "deadbeefdeadbeefdeadbeefdeadbeef"
	cases := [][]byte{
		// 约束候选出现在数据靠前位置，归并游标必须先看到它。
		[]byte("aa " + digest + " bb 1380013800 cc HELLO dd"),
		[]byte("aa 1380013800 bb " + digest + " cc HELLO dd"),
		[]byte(digest + " 1380013800 HELLO"),
		[]byte("HELLO " + digest + " 1380013800"),
		[]byte("nothing to confirm here"),
	}
	assertScanAgreesWithReference(t, scanner, cases)
}

// TestRequiredIndexScientificNotationAgreesWithPerStartScan 验证科学计数法规则
// 的候选文字升级为多字节后，候选驱动扫描的命中集合仍与逐起点确认逐位一致。
// 触发背景见 docs/technical-solutions/scankit-block-mode/问题与排查计划.md §20。
func TestRequiredIndexScientificNotationAgreesWithPerStartScan(t *testing.T) {
	scanner, err := Compile([]Expression{
		{Id: 1, Pattern: `[+-]?[0-9]+(\.[0-9]+)?[eE][+-]?[0-9]+`},
		{Id: 2, Pattern: `[0-9]+x[0-9]+`},
	})
	if err != nil {
		t.Fatal(err)
	}
	if scanner.requiredFindInto == nil {
		t.Fatal("科学计数法规则应建立必须文字候选索引")
	}
	if !scanner.requiredCovered[0] {
		t.Fatal("科学计数法规则应被必须文字候选索引覆盖")
	}
	for index := range scanner.requiredLiterals {
		for _, entry := range scanner.requiredLiterals[index].entries {
			if entry.back == nil {
				t.Fatalf("第 %d 个候选条目缺少左侧字节集合", index)
			}
		}
	}
	assertScanAgreesWithReference(t, scanner, [][]byte{
		[]byte("value=1e5 end"),
		[]byte("value=+1.5E-3 end"),
		[]byte("value=-2.0e+10 end"),
		[]byte("value=12e10 and 34E-7 end"),
		[]byte("email a@b.com noise e e e e e"),
		[]byte("no digits here, only letters e E"),
		[]byte("size 1024x768 and 640X480"),
	})
}

// TestRetainBufferPolicy 验证扫描期缓冲的池内保留判据。四个缓冲（候选起点、
// 文字命中、直接结果、文字匹配）共用同一条判据：容量不超过各自的绝对上限即保留。
// "大输入之后的小输入"必须保留 —— 释放它会让下一次大输入整块重新 grow（实测单次
// 多分配 147 MB）；超过上限才释放，避免异常大的缓冲长期驻留。
// 背景见 docs/technical-solutions/scankit-block-mode/问题与排查计划.md §21.7 P1、§26。
func TestRetainBufferPolicy(t *testing.T) {
	retainers := []struct {
		name  string
		limit int
		fn    func(int) bool
	}{
		{name: "requiredStarts", limit: requiredStartsRetainLimit, fn: retainRequiredStarts},
		{name: "literalMatches", limit: literalMatchesRetainLimit, fn: retainLiteralMatches},
		{name: "requiredHits", limit: requiredHitsRetainLimit, fn: retainRequiredHits},
		{name: "directMatches", limit: directMatchesRetainLimit, fn: retainDirectMatches},
	}
	for _, retainer := range retainers {
		t.Run(retainer.name, func(t *testing.T) {
			cases := []struct {
				name     string
				capacity int
				retain   bool
			}{
				// 10MB 密集输入把候选起点撑到 2,581,504 条目：旧固定阈值
				// （>1<<20 即释放）会让每次扫描都从 0 重新 grow。
				{name: "below-limit", capacity: 2_581_504, retain: true},
				{name: "old-threshold", capacity: 1 << 20, retain: true},
				{name: "at-limit", capacity: retainer.limit, retain: true},
				{name: "over-limit", capacity: retainer.limit + 1, retain: false},
				{name: "empty", capacity: 0, retain: true},
			}
			for _, testCase := range cases {
				if got := retainer.fn(testCase.capacity); got != testCase.retain {
					t.Fatalf("%s: %s(capacity=%d)=%v，期望 %v",
						retainer.name, testCase.name, testCase.capacity, got, testCase.retain)
				}
			}
		})
	}
}

// disableRequiredCollapse 复制扫描器并关闭「窗口内复用确认结果」优化，作为
// 逐起点确认的对照实现。
func disableRequiredCollapse(scanner *Scanner) *Scanner {
	clone := *scanner
	clone.rules = append([]compiledRule(nil), scanner.rules...)
	for index := range clone.rules {
		clone.rules[index].collapseHead = nil
	}
	clone.hasCollapse = false
	return &clone
}

// TestRequiredIndexCollapseSharesWindow 验证「起点无关窗口」优化：同一次文字命中
// 摊开出的候选起点共享一次确认程序求值，而报告结果必须与逐起点确认逐位一致。
func TestRequiredIndexCollapseSharesWindow(t *testing.T) {
	pattern := "[A-Za-z0-9.!#$%&'*+/?^_`{|}~-]{1,64}@[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?(?:\\.[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?)+\\b"
	scanner, err := Compile([]Expression{{Id: 1, Pattern: pattern}})
	if err != nil {
		t.Fatal(err)
	}
	if len(scanner.rules) != 1 || scanner.rules[0].collapseHead == nil {
		t.Fatal("类重复前缀 + 左侧回退集合的规则应识别出起点无关窗口")
	}
	if !scanner.hasCollapse {
		t.Fatal("存在可复用窗口的扫描器应预留确认窗口缓存")
	}
	head := scanner.rules[0].collapseHead

	// 单窗口、必失败：局部部分 "user" 让一次 "@" 命中摊开 4 个候选起点，
	// 域名 "example..invalid" 使全部起点都确认失败，因此不存在重叠抑制，
	// 求值次数可以精确断言。
	windowData := []byte("email=user@example..invalid")
	ctx := &scanContext{}
	starts, ok := scanner.requiredScanStarts(ctx, windowData)
	if !ok {
		t.Fatal("候选起点枚举不应超限")
	}
	if len(starts) != 4 {
		t.Fatalf("候选起点数 = %d，期望一次命中摊开 4 个起点", len(starts))
	}

	// 按候选起点顺序模拟确认：落在窗口内的起点复用首起点的求值结果。
	state := &blockScanState{scanner: scanner, data: windowData, ctx: ctx}
	state.ctx.collapseWindows = reserveCollapseWindows(nil, len(scanner.rules))
	evaluated, reused := 0, 0
	for _, key := range starts {
		start := int(key >> 32)
		memo := &state.ctx.collapseWindows[0]
		if start >= memo.from && start <= memo.to {
			reused++
			continue
		}
		end, ok := state.confirmEnd(0, start)
		state.recordCollapseWindow(head, 0, start, end, ok)
		evaluated++
	}
	if evaluated != 1 {
		t.Fatalf("单窗口应只求值 1 次，实际 %d 次（候选 %d 个）", evaluated, len(starts))
	}
	if reused != len(starts)-1 {
		t.Fatalf("复用次数 = %d，期望 %d", reused, len(starts)-1)
	}

	// 复用必须逐位等价：与关闭优化的扫描器、以及逐起点参考实现对齐。
	expanded := disableRequiredCollapse(scanner)
	corpus := [][]byte{
		windowData,
		[]byte("email=user@example..invalid and more"),
		[]byte("no at sign here"),
		[]byte("@"),
		[]byte("a@b.c"),
		[]byte(".@b.c"),
		[]byte("user@example..invalid"),
		[]byte("user.name+tag@sub.domain.co.uk tail"),
		[]byte(strings.Repeat("y", 64) + "@a.bc"),
		[]byte(strings.Repeat("y", 65) + "@a.bc"),
		[]byte(strings.Repeat("y", 130) + "@a.bc"),
		[]byte("x@a-b.c and y@d.e"),
		[]byte("multi a@b.co b@c.co c@d.co"),
		[]byte("trailing user@example.com."),
		[]byte("中文邮箱 user@example.com 结束"),
		[]byte(":e ..@909bZ.zbz.9-!!@9zA." + strings.Repeat("q", 63) + ".0az9.9Z"),
	}
	for _, data := range corpus {
		want, err := expanded.Scan(data)
		if err != nil {
			t.Fatal(err)
		}
		got, err := scanner.Scan(data)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(got, want) {
			t.Fatalf("语料 %q 复用前后命中不一致:\n got=%v\nwant=%v", data, got, want)
		}
	}
	assertScanAgreesWithReference(t, scanner, corpus)
}

// TestRequiredIndexCollapseMatchesRegexp 用随机生成的类邮箱结构把窗口收缩后的
// 扫描结果与 Go 正则实现对齐，覆盖局部部分长度、标签数量与非法字符的边界组合。
func TestRequiredIndexCollapseMatchesRegexp(t *testing.T) {
	pattern := "[A-Za-z0-9.!#$%&'*+/?^_`{|}~-]{1,64}@[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?(?:\\.[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?)+\\b"
	scanner, err := Compile([]Expression{{Id: 1, Pattern: pattern}})
	if err != nil {
		t.Fatal(err)
	}
	reference := regexp.MustCompile(pattern)
	rng := rand.New(rand.NewPCG(7, 11))
	pick := func(alphabet string, limit int) string {
		length := rng.IntN(limit)
		buf := make([]byte, length)
		for index := range buf {
			buf[index] = alphabet[rng.IntN(len(alphabet))]
		}
		return string(buf)
	}
	filler := " \t=,:;中文\nlevel=INFO service=payment "
	hits := 0
	for round := 0; round < 4000; round++ {
		var builder strings.Builder
		for part := rng.IntN(3) + 1; part > 0; part-- {
			builder.WriteString(pick(filler, 4))
			// 局部部分：可能为空、超长，或含非法字符。
			switch rng.IntN(6) {
			case 0:
				builder.WriteString("")
			case 1:
				builder.WriteString(strings.Repeat("z", 65))
			case 2:
				builder.WriteString("..")
			default:
				builder.WriteString(pick("abzAZ09.-_+!~", 12))
			}
			builder.WriteString("@")
			// 域名：1~3 个标签，标签长度覆盖 0、1、超长与尾随连字符。
			for label := rng.IntN(3) + 1; label > 0; label-- {
				switch rng.IntN(8) {
				case 0:
					builder.WriteString(".")
				case 1:
					builder.WriteString(strings.Repeat("q", 63))
				case 2:
					builder.WriteString("-")
				default:
					builder.WriteString(pick("abzAZ09-", 6))
				}
				builder.WriteString(".")
			}
			builder.WriteString(pick("abzAZ09", 4))
		}
		data := []byte(builder.String())
		want := make([]Match, 0)
		for _, index := range reference.FindAllIndex(data, -1) {
			want = append(want, Match{Id: 1, From: uint64(index[0]), To: uint64(index[1])})
		}
		got, err := scanner.Scan(data)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(got, want) {
			t.Fatalf("随机语料 %q 命中不一致:\n got=%v\nwant=%v", data, got, want)
		}
		hits += len(want)
	}
	if hits == 0 {
		t.Fatal("随机语料没有产出任何命中，未能覆盖命中路径")
	}
	t.Logf("随机语料累计命中=%d", hits)
}

// TestGuardRunStartsRandomizedMatchesScalar 用确定性随机语料与随机约束组做差分：
// 段长在最短窗口上下、窗口边界前后、跨多个宽窗口的长段、以及无命中窗口
// 等情形都必须与逐字节回退实现给出完全相同的候选起点集合。
func TestGuardRunStartsRandomizedMatchesScalar(t *testing.T) {
	rng := rand.New(rand.NewPCG(0x9e3779b97f4a7c15, 0xbf58476d1ce4e5b9))
	backend := dispatch.DefaultBackend()
	const limit = 1 << 20

	for round := 0; round < 3000; round++ {
		// 随机字节集合：从 256 个取值里抽 1~10 个，覆盖极稀疏与较稠密两类掩码。
		var set [4]uint64
		for picked := rng.IntN(10) + 1; picked > 0; picked-- {
			value := byte(rng.IntN(256))
			set[value>>6] |= uint64(1) << uint(value&63)
		}
		// 随机约束组：1~3 条规则，窗口长度覆盖 1、宽窗口附近与超过宽窗口。
		runCount := rng.IntN(3) + 1
		runs := make([]guardRun, 0, runCount)
		minLength := 0
		for index := 0; index < runCount; index++ {
			length := []int{1, 2, 3, 4, 8, 9, 16, 17, 63, 64, 65}[rng.IntN(11)]
			runs = append(runs, guardRun{
				ruleIndex: index,
				set:       set,
				offset:    rng.IntN(4),
				length:    length,
			})
			if minLength == 0 || length < minLength {
				minLength = length
			}
		}
		slices.SortStableFunc(runs, func(a, b guardRun) int { return a.length - b.length })
		group := guardRunGroup{
			set:       set,
			tables:    simd.NewByteSetTables([4][4]uint64{set}),
			runs:      runs,
			minLength: minLength,
		}
		// 语料长度特意覆盖 0、单个宽窗口内、宽窗口边界与多个宽窗口。
		size := []int{0, 1, 3, 16, 33, 63, 64, 65, 127, 128, 129, 191, 192, 193, 257}[rng.IntN(15)]
		data := make([]byte, size)
		for index := range data {
			if rng.IntN(3) == 0 {
				// 提高集合成员占比，制造长度落在最短窗口上下的连续段。
				for {
					value := byte(rng.IntN(256))
					if set[value>>6]&(uint64(1)<<(value&63)) != 0 {
						data[index] = value
						break
					}
				}
				continue
			}
			data[index] = byte(rng.IntN(256))
		}

		want, ok := group.appendStartsScalar(data, nil, limit)
		if !ok {
			t.Fatalf("round=%d 参照实现意外溢出", round)
		}
		got, ok := group.appendStarts(backend, data, nil, limit)
		if !ok {
			t.Fatalf("round=%d 宽窗口实现意外溢出", round)
		}
		if len(got) != len(want) {
			t.Fatalf("round=%d size=%d minLength=%d runs=%+v data=%q 候选数量不一致 got=%d want=%d",
				round, size, minLength, runs, data, len(got), len(want))
		}
		slices.Sort(got)
		slices.Sort(want)
		for index := range got {
			if got[index] != want[index] {
				t.Fatalf("round=%d size=%d minLength=%d runs=%+v data=%q 第 %d 个候选不一致 got=%v want=%v",
					round, size, minLength, runs, data, index, got[index], want[index])
			}
		}
	}
}
