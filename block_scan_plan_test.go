package scankit

import (
	"reflect"
	"strings"
	"testing"
)

func TestCompileBuildsBlockScanPlanWithObservableConsumersOnly(t *testing.T) {
	database, err := Compile([]Expression{
		{Id: 1, Pattern: `\d{2}`},
		{Id: 2, Pattern: `4[0-9]{15}|5[1-5][0-9]{14}`},
		{Id: 3, Pattern: `\d+`},
		{Id: 4, Pattern: `\d{2}`, Flags: CompileQuiet},
	})
	if err != nil {
		t.Fatal(err)
	}
	plan := database.blockScanPlan.unanchored
	if !plan.hasLanes() {
		t.Fatal("compiled database has no unanchored Block lanes")
	}
	if len(plan.fixed['1']) == 0 {
		t.Fatal("fixed byte regex has no digit trigger lane")
	}
	if len(plan.fixedAnchor['4']) == 0 || len(plan.fixedAnchor['5']) == 0 {
		t.Fatal("fixed anchor alternation has no expected trigger lanes")
	}
	if len(plan.always) == 0 || !plan.always[0].hasSimpleRepeat {
		t.Fatal("simple repeat has no always lane")
	}

	foundVisible, foundQuiet := false, false
	for _, lane := range plan.fixed['1'] {
		for _, expressionIndex := range lane.consumers {
			if database.expressions[expressionIndex].id == 1 {
				foundVisible = true
			}
			if database.expressions[expressionIndex].id == 4 {
				foundQuiet = true
			}
		}
	}
	if !foundVisible || foundQuiet {
		t.Fatalf("fixed lane consumers visible=%t quiet=%t, want true false", foundVisible, foundQuiet)
	}

	matches, err := database.Scan([]byte("12 4111111111111111"))
	if err != nil {
		t.Fatal(err)
	}
	for _, match := range matches {
		if match.Id == 4 {
			t.Fatalf("quiet expression was reported: %#v", match)
		}
	}
}

func TestCompileUsesFixedAnchorForLargeFixedWidthAlternation(t *testing.T) {
	const pattern = `(?:ab|cd|ef|gh){4}`
	scanner, err := Compile([]Expression{{Id: 1, Pattern: pattern}})
	if err != nil {
		t.Fatal(err)
	}
	if len(scanner.blockScanPlan.unanchored.always) != 0 {
		t.Fatal("large fixed-width alternation unexpectedly remained in always lane")
	}
	if len(scanner.blockScanPlan.unanchored.fixedAnchor['a']) != 1 || len(scanner.blockScanPlan.unanchored.fixedAnchor['c']) != 1 {
		t.Fatal("large fixed-width alternation has no expected fixed-anchor lanes")
	}
	reference, err := Compile([]Expression{{Id: 1, Pattern: `(?:)` + pattern}})
	if err != nil {
		t.Fatal(err)
	}
	data := []byte("abefcdgh xx abcdefgh cdabefgh ghghghgh")
	got, err := scanner.Scan(data)
	if err != nil {
		t.Fatal(err)
	}
	want, err := reference.Scan(data)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("fixed-anchor Scan() = %#v, want %#v", got, want)
	}
}

func TestDirectLiteralScanPreservesEventOrder(t *testing.T) {
	expressions := []Expression{
		{Id: 1, Pattern: `matched`},
		{Id: 2, Pattern: `matched`},
		{Id: 3, Pattern: `match`},
		{Id: 4, Pattern: `hed`},
	}
	direct, err := Compile(expressions)
	if err != nil {
		t.Fatal(err)
	}
	if !direct.directLiterals {
		t.Fatal("plain literal expressions did not select direct scan")
	}
	fallbackExpressions := append([]Expression(nil), expressions...)
	for index := range fallbackExpressions {
		fallbackExpressions[index].Pattern = `(?:)` + fallbackExpressions[index].Pattern
	}
	fallback, err := Compile(fallbackExpressions)
	if err != nil {
		t.Fatal(err)
	}
	data := []byte(`matched unmatched`)
	got, err := direct.ScanInto(data, []Match{{Id: 99, From: 1, To: 1}})
	if err != nil {
		t.Fatal(err)
	}
	want, err := fallback.Scan(data)
	if err != nil {
		t.Fatal(err)
	}
	want = append([]Match{{Id: 99, From: 1, To: 1}}, want...)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("direct ScanInto() = %#v, want %#v", got, want)
	}
}

func FuzzDirectLiteralScanMatchesRegexFallback(f *testing.F) {
	for _, seed := range [][]byte{[]byte(`matched unmatched`), []byte{}, {0xff, 'm', 'a', 't', 'c', 'h'}} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 512 {
			t.Skip()
		}
		expressions := []Expression{{Id: 1, Pattern: `matched`}, {Id: 2, Pattern: `match`}, {Id: 3, Pattern: `hed`}}
		direct, err := Compile(expressions)
		if err != nil {
			t.Fatal(err)
		}
		fallbackExpressions := append([]Expression(nil), expressions...)
		for index := range fallbackExpressions {
			fallbackExpressions[index].Pattern = `(?:)` + fallbackExpressions[index].Pattern
		}
		fallback, err := Compile(fallbackExpressions)
		if err != nil {
			t.Fatal(err)
		}
		got, err := direct.Scan(data)
		if err != nil {
			t.Fatal(err)
		}
		want, err := fallback.Scan(data)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("direct Scan(%q) = %#v, want %#v", data, got, want)
		}
	})
}

func TestFixedAnchorPreservesAdvancedEventSemantics(t *testing.T) {
	expressions := []Expression{
		{Id: 1, Pattern: `(?:ab|cd|ef|gh){4}`, Flags: CompileQuiet},
		{Id: 2, Pattern: `tag`, Flags: CompileQuiet},
		{Id: 3, Pattern: `1&2`, Flags: CompileCombination},
		{Id: 4, Pattern: `(?:ab|cd|ef|gh){4}`, Flags: CompileSingleMatch, Ext: &ExpressionExt{Flags: ExtMinOffset | ExtMaxOffset | ExtMinLength, MinOffset: 8, MaxOffset: 24, MinLength: 8}},
	}
	optimized, err := Compile(expressions)
	if err != nil {
		t.Fatal(err)
	}
	fallbackExpressions := append([]Expression(nil), expressions...)
	fallbackExpressions[0].Pattern = `(?:)` + fallbackExpressions[0].Pattern
	fallbackExpressions[3].Pattern = `(?:)` + fallbackExpressions[3].Pattern
	fallback, err := Compile(fallbackExpressions)
	if err != nil {
		t.Fatal(err)
	}
	data := []byte(`abcdefgh tag cdabefgh tag efghcdab trailing`)
	got, err := optimized.Scan(data)
	if err != nil {
		t.Fatal(err)
	}
	want, err := fallback.Scan(data)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("fixed anchor Scan() = %#v, want %#v", got, want)
	}
}

func FuzzFixedAnchorPreservesScannerSemantics(f *testing.F) {
	for _, seed := range [][]byte{
		[]byte(`abcdefgh tag cdabefgh tag efghcdab trailing`),
		[]byte{},
		{0xff, 'a', 'b', 'c', 'd', 'e', 'f', 'g', 'h'},
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 512 {
			t.Skip()
		}
		expressions := []Expression{
			{Id: 1, Pattern: `(?:ab|cd|ef|gh){4}`, Flags: CompileQuiet},
			{Id: 2, Pattern: `tag`, Flags: CompileQuiet},
			{Id: 3, Pattern: `1&2`, Flags: CompileCombination},
			{Id: 4, Pattern: `(?:ab|cd|ef|gh){4}`, Flags: CompileSingleMatch, Ext: &ExpressionExt{Flags: ExtMinOffset | ExtMaxOffset | ExtMinLength, MinOffset: 8, MaxOffset: 256, MinLength: 8}},
		}
		optimized, err := Compile(expressions)
		if err != nil {
			t.Fatal(err)
		}
		fallbackExpressions := append([]Expression(nil), expressions...)
		fallbackExpressions[0].Pattern = `(?:)` + fallbackExpressions[0].Pattern
		fallbackExpressions[3].Pattern = `(?:)` + fallbackExpressions[3].Pattern
		fallback, err := Compile(fallbackExpressions)
		if err != nil {
			t.Fatal(err)
		}
		got, err := optimized.Scan(data)
		if err != nil {
			t.Fatal(err)
		}
		want, err := fallback.Scan(data)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("optimized Scan(%q) = %#v, want %#v", data, got, want)
		}
	})
}

func TestCompileResolvesACOutputsToBlockTriggerLanes(t *testing.T) {
	database, err := Compile([]Expression{
		{Id: 1, Pattern: `audit=`},
		{Id: 2, Pattern: `value=\d{8}`},
		{Id: 3, Pattern: `value=\d{8}`, Flags: CompileQuiet},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(database.blockScanPlan.triggers) != len(database.triggers) {
		t.Fatalf("compiled trigger lane count = %d, want %d", len(database.blockScanPlan.triggers), len(database.triggers))
	}

	var foundLiteral, foundAnchored bool
	for triggerIndex, trigger := range database.triggers {
		lane := database.blockScanPlan.triggers[triggerIndex]
		switch trigger.kind {
		case scanLiteral:
			if lane.kind != blockTriggerLiteral || lane.literal.expressionIndex != trigger.expressionIndex {
				t.Fatalf("literal trigger %d was not resolved: %#v", triggerIndex, lane)
			}
			foundLiteral = true
		case scanRegex:
			if lane.kind != blockTriggerAnchored {
				t.Fatalf("anchored trigger %d was not resolved: %#v", triggerIndex, lane)
			}
			if lane.anchored.contextIndex != database.anchoredGroups[trigger.regexGroupIndex][0] {
				t.Fatalf("anchored lane context index = %d, want group representative", lane.anchored.contextIndex)
			}
			if len(lane.anchored.consumers) != 1 || database.expressions[lane.anchored.consumers[0]].id != 2 {
				t.Fatalf("anchored lane consumers = %v, want only observable ID 2", lane.anchored.consumers)
			}
			foundAnchored = true
		}
	}
	if !foundLiteral || !foundAnchored {
		t.Fatalf("resolved trigger lanes literal=%t anchored=%t, want both", foundLiteral, foundAnchored)
	}

	matches, err := database.Scan([]byte("audit=value=12345678"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 2 || matches[0].Id != 1 || matches[1].Id != 2 {
		t.Fatalf("matches = %#v, want literal ID 1 and anchored ID 2", matches)
	}
}

func TestCompileAllocatesVerifierForEveryAnchoredTriggerLane(t *testing.T) {
	scanner, err := Compile([]Expression{
		{Id: 1, Pattern: `value=\d{8}`},
		{Id: 2, Pattern: `token=[a-z]{4,8}`},
		{Id: 3, Pattern: `value=\d{8}`, Flags: CompileQuiet},
	})
	if err != nil {
		t.Fatal(err)
	}

	ctx := scanner.newContext()
	found := false
	for triggerIndex, lane := range scanner.blockScanPlan.triggers {
		if lane.kind != blockTriggerAnchored {
			continue
		}
		found = true
		index := lane.anchored.contextIndex
		if int(index) >= len(ctx.regexVerifiers) {
			t.Fatalf("anchored trigger %d context index %d exceeds verifier count %d", triggerIndex, index, len(ctx.regexVerifiers))
		}
		if ctx.regexVerifiers[index] == nil {
			t.Fatalf("anchored trigger %d has no verifier at context index %d", triggerIndex, index)
		}
	}
	if !found {
		t.Fatal("compiled scanner has no anchored trigger lane")
	}
}

func TestBlockTriggerLanesPreserveAdvancedDeliverySemantics(t *testing.T) {
	database, err := Compile([]Expression{
		{Id: 1, Pattern: `ID:\d{2}`, Flags: CompileQuiet},
		{Id: 2, Pattern: "token", Flags: CompileQuiet},
		{Id: 3, Pattern: "1&2", Flags: CompileCombination},
		{Id: 4, Pattern: `ID:[0-9]{2}`, Flags: CompileSingleMatch, Ext: &ExpressionExt{Flags: ExtMinOffset, MinOffset: 6}},
		{Id: 5, Pattern: `ID:[0-9]{2}`, Ext: &ExpressionExt{Flags: ExtMaxOffset, MaxOffset: 5}},
	})
	if err != nil {
		t.Fatal(err)
	}

	got, err := database.Scan([]byte("ID:12 token ID:34 token"))
	if err != nil {
		t.Fatal(err)
	}
	want := []Match{
		{Id: 5, From: 0, To: 5},
		{Id: 3, From: 0, To: 11},
		{Id: 4, From: 12, To: 17},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("matches = %#v, want %#v", got, want)
	}
}

// 本测试覆盖普通混合字面量/正则规则集使用的四根字节机器字跳过。复制的数据库具有相同编译计划，
// 但禁用跳过，使常规逐字节执行器成为本地行为参考。
func TestRootByteAnchoredWordScanPreservesByteScannerEvents(t *testing.T) {
	database, err := Compile([]Expression{
		{Id: 1, Pattern: "payment"},
		{Id: 2, Pattern: `ID:\d{2}`},
		{Id: 3, Pattern: `\btoken\b`},
		{Id: 4, Pattern: `trace=req-[a-z0-9]{4,8}`},
		{Id: 5, Pattern: `\bERROR\b`},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !database.automaton.rootByteFast || database.automaton.rootByteCount != 4 {
		t.Fatalf("root-byte fast path = enabled:%t count:%d, want enabled with four bytes", database.automaton.rootByteFast, database.automaton.rootByteCount)
	}
	if len(database.unanchoredGroups) != 0 || database.advancedEvents {
		t.Fatalf("database must select anchored-only execution, unanchored=%d advanced=%t", len(database.unanchoredGroups), database.advancedEvents)
	}

	reference := *database
	reference.automaton.rootByteFast = false
	data := []byte("普通日志 payment ignored ID:42 token=secret trace=req-a12b\\n" +
		"ERROR token trace=req-7f3a2c payment ID:77\\n" +
		"near=paymentx ID:9 trace=req-abc tokenx")
	got, err := database.Scan(data)
	if err != nil {
		t.Fatal(err)
	}
	want, err := reference.Scan(data)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("word-skip matches = %#v, byte scanner matches = %#v", got, want)
	}
}

func TestRootByteAnchoredWordSingleScanPreservesByteScannerEvents(t *testing.T) {
	database, err := Compile([]Expression{{Id: 1, Pattern: `62[0-9]{14,17}`}})
	if err != nil {
		t.Fatal(err)
	}
	if !database.automaton.rootByteFast || database.automaton.rootByteCount != 1 {
		t.Fatalf("root-byte fast path = enabled:%t count:%d, want enabled with one byte", database.automaton.rootByteFast, database.automaton.rootByteCount)
	}
	if !database.singleRootFixedAnchor {
		t.Fatal("single root fixed-anchor specialization was not selected")
	}
	reference := *database
	reference.automaton.rootByteFast = false
	data := []byte("日志 bank=6222021234567890 invalid=6122021234567890 repeated=6222021234567890123")
	got, err := database.Scan(data)
	if err != nil {
		t.Fatal(err)
	}
	want, err := reference.Scan(data)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("single-root word-skip matches = %#v, byte scanner matches = %#v", got, want)
	}
}

func TestRootByteSingleSpecializationRequiresFixedAnchor(t *testing.T) {
	for _, test := range []struct {
		name    string
		pattern string
		want    bool
	}{
		{name: "fixed_leading_anchor", pattern: `62[0-9]{14,17}`, want: true},
		{name: "one_byte_phone_anchor", pattern: `1[3-9][0-9]{9}`, want: false},
		{name: "floating_email_anchor", pattern: `[A-Za-z0-9._%+-]{1,64}@[A-Za-z0-9-]{1,63}\\.com`, want: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			database, err := Compile([]Expression{{Id: 1, Pattern: test.pattern}})
			if err != nil {
				t.Fatal(err)
			}
			if database.singleRootFixedAnchor != test.want {
				t.Fatalf("singleRootFixedAnchor = %t, want %t", database.singleRootFixedAnchor, test.want)
			}
		})
	}
}

func TestRootByteAnchoredWordScanSupportsProductionRuleSet(t *testing.T) {
	expressions := []Expression{
		{Id: 1, Pattern: "payment"},
		{Id: 2, Pattern: `ID:\d{2}`},
		{Id: 3, Pattern: `\btoken\b`},
		{Id: 4, Pattern: `trace=req-[a-z0-9]{4,8}`},
		{Id: 5, Pattern: `\bERROR\b`},
	}
	for index := 5; index < 10; index++ {
		expressions = append(expressions, Expression{Id: uint32(index + 1), Pattern: "rule-never-hit"})
	}
	database, err := Compile(expressions)
	if err != nil {
		t.Fatal(err)
	}
	if !database.automaton.rootByteFast || database.automaton.rootByteCount != 5 {
		t.Fatalf("production root-byte fast path = enabled:%t count:%d, want enabled with five bytes", database.automaton.rootByteFast, database.automaton.rootByteCount)
	}

	reference := *database
	reference.automaton.rootByteFast = false
	data := []byte("普通日志 payment ignored ID:42 token=secret trace=req-a12b\\n" +
		"ERROR token trace=req-7f3a2c payment ID:77\\n" +
		"rule-never-hit near=paymentx ID:9 trace=req-abc tokenx")
	got, err := database.Scan(data)
	if err != nil {
		t.Fatal(err)
	}
	want, err := reference.Scan(data)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("word-skip matches = %#v, byte scanner matches = %#v", got, want)
	}
}

func FuzzBlockScanPlanMatchesIndividualDatabases(f *testing.F) {
	expressions := []Expression{
		{Id: 1, Pattern: `\d{2}`},
		{Id: 2, Pattern: `4[0-9]{15}|5[1-5][0-9]{14}`},
		{Id: 3, Pattern: `\d+`},
	}
	combined, err := Compile(expressions)
	if err != nil {
		f.Fatal(err)
	}
	individual := make([]*Scanner, len(expressions))
	for index, expression := range expressions {
		individual[index], err = Compile([]Expression{expression})
		if err != nil {
			f.Fatal(err)
		}
	}
	for _, seed := range [][]byte{
		[]byte("12 4111111111111111 51"),
		[]byte("a12b34"),
		[]byte("no digits"),
		{0x00, '4', '1', '1'},
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 4_096 {
			t.Skip()
		}
		combinedMatches, err := combined.Scan(data)
		if err != nil {
			t.Fatal(err)
		}
		want := make(map[Match]int)
		for _, database := range individual {
			matches, err := database.Scan(data)
			if err != nil {
				t.Fatal(err)
			}
			for _, match := range matches {
				want[match]++
			}
		}
		got := make(map[Match]int)
		for _, match := range combinedMatches {
			got[match]++
		}
		if len(got) != len(want) {
			t.Fatalf("combined distinct matches = %d, individual = %d", len(got), len(want))
		}
		for match, count := range want {
			if got[match] != count {
				t.Fatalf("combined match count for %#v = %d, want %d", match, got[match], count)
			}
		}
	})
}

// FuzzBlockTriggerLanesMatchIndividualDatabases 覆盖 AC 触发通道及投递控制。单独数据库
// 是本地参考：没有组合规则时，即使匹配重叠或扩展在验证后过滤事件，合并其 Match 多重集也必须
// 与单个编译数据库等价。
func FuzzBlockTriggerLanesMatchIndividualDatabases(f *testing.F) {
	expressions := []Expression{
		{Id: 1, Pattern: `ID:\d{2}`},
		{Id: 2, Pattern: `ID:[0-9]{2}`, Ext: &ExpressionExt{Flags: ExtMinOffset | ExtMaxOffset, MinOffset: 3, MaxOffset: 96}},
		{Id: 3, Pattern: "token", Flags: CompileSingleMatch},
		{Id: 4, Pattern: "audit="},
		{Id: 5, Pattern: `\d{2}`},
	}
	combined, err := Compile(expressions)
	if err != nil {
		f.Fatal(err)
	}
	individual := make([]*Scanner, len(expressions))
	for index, expression := range expressions {
		individual[index], err = Compile([]Expression{expression})
		if err != nil {
			f.Fatal(err)
		}
	}
	for _, seed := range [][]byte{
		[]byte("ID:12 token audit= ID:34"),
		[]byte("ID:1 ID:123 token token 12"),
		[]byte("无命中 ID:56"),
		{0x00, 'I', 'D', ':', '9', '9'},
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 4_096 {
			t.Skip()
		}
		gotMatches, err := combined.Scan(data)
		if err != nil {
			t.Fatal(err)
		}
		got := matchMultiset(gotMatches)
		want := make(map[Match]int)
		for _, database := range individual {
			matches, err := database.Scan(data)
			if err != nil {
				t.Fatal(err)
			}
			for _, match := range matches {
				want[match]++
			}
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("combined matches = %#v, individual matches = %#v for %q", got, want, data)
		}
	})
}

func FuzzRootByteAnchoredWordScanMatchesByteScanner(f *testing.F) {
	database, err := Compile([]Expression{
		{Id: 1, Pattern: "payment"},
		{Id: 2, Pattern: `ID:\d{2}`},
		{Id: 3, Pattern: `\btoken\b`},
		{Id: 4, Pattern: `trace=req-[a-z0-9]{4,8}`},
		{Id: 5, Pattern: `\bERROR\b`},
	})
	if err != nil {
		f.Fatal(err)
	}
	if !database.automaton.rootByteFast || database.automaton.rootByteCount != 4 {
		f.Fatalf("test fixture did not select four-byte root fast path: enabled=%t count=%d", database.automaton.rootByteFast, database.automaton.rootByteCount)
	}
	reference := *database
	reference.automaton.rootByteFast = false
	for _, seed := range [][]byte{
		[]byte("payment ID:42 token trace=req-a12b ERROR"),
		[]byte("无命中 ordinary log record"),
		[]byte("paymentpayment ID:99 tokenx ERROR! trace=req-abcdef"),
		{0x00, 'I', 'D', ':', '4', '2', 'p', 'a', 'y'},
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 4_096 {
			t.Skip()
		}
		got, err := database.Scan(data)
		if err != nil {
			t.Fatal(err)
		}
		want, err := reference.Scan(data)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("word-skip matches = %#v, byte scanner matches = %#v for %q", got, want, data)
		}
	})
}

func FuzzRootByteAnchoredWordSingleScanMatchesByteScanner(f *testing.F) {
	database, err := Compile([]Expression{{Id: 1, Pattern: `62[0-9]{14,17}`}})
	if err != nil {
		f.Fatal(err)
	}
	reference := *database
	reference.automaton.rootByteFast = false
	for _, seed := range [][]byte{
		[]byte("6222021234567890"),
		[]byte("6122021234567890"),
		[]byte("prefix=62 suffix=6222021234567890123"),
		{0x00, '6', '2', '2', '2', '0'},
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 4_096 {
			t.Skip()
		}
		got, err := database.Scan(data)
		if err != nil {
			t.Fatal(err)
		}
		want, err := reference.Scan(data)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("single-root word-skip matches = %#v, byte scanner matches = %#v for %q", got, want, data)
		}
	})
}

// TestHighByteAnchorsPreserveBinaryLiterals 验证正则锚点始终按原始字节而非 UTF-8
// 字符串编码处理。该场景同时覆盖十六进制、八进制和原始二进制字面量，以及长输入的
// 根字节快路径。
func TestHighByteAnchorsPreserveBinaryLiterals(t *testing.T) {
	raw80 := string([]byte{0x80}) + "A"
	rawFF := string([]byte{0xff}) + "B"
	scanner, err := Compile([]Expression{
		{Id: 1, Pattern: `\x80A`},
		{Id: 2, Pattern: `\0200A`},
		{Id: 3, Pattern: `\xffB`},
		{Id: 4, Pattern: `\0377B`},
		{Id: 5, Pattern: raw80},
		{Id: 6, Pattern: rawFF},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !scanner.automaton.rootByteFast || scanner.automaton.rootByteCount != 2 {
		t.Fatalf("root-byte fast path = enabled:%t count:%d, want two binary roots", scanner.automaton.rootByteFast, scanner.automaton.rootByteCount)
	}

	data := append([]byte(strings.Repeat("ordinary-log ", 512)), 0x80, 'A', ' ', 0xff, 'B', ' ', 0x80, 'A')
	reference := *scanner
	reference.automaton.rootByteFast = false

	want, err := reference.Scan(data)
	if err != nil {
		t.Fatal(err)
	}
	got, err := scanner.Scan(data)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("binary root-word scan matches = %#v, byte scanner matches = %#v", got, want)
	}
	into, err := scanner.ScanInto(data, make([]Match, 0, len(got)))
	if err != nil {
		t.Fatal(err)
	}
	if !sameMatchSequence(into, got) {
		t.Fatalf("ScanInto matches = %#v, Scan matches = %#v", into, got)
	}

	counts := matchMultiset(got)
	for _, id := range []uint32{1, 2, 5} {
		if counts[Match{Id: id, From: uint64(len(data) - 2), To: uint64(len(data))}] != 1 {
			t.Fatalf("ID %d did not match the final 0x80 literal: %#v", id, got)
		}
	}
}

func FuzzHighByteAnchoredRulesMatchByteScanner(f *testing.F) {
	raw80 := string([]byte{0x80}) + "A"
	rawFF := string([]byte{0xff}) + "B"
	scanner, err := Compile([]Expression{
		{Id: 1, Pattern: `\x80A`},
		{Id: 2, Pattern: `\0200A`},
		{Id: 3, Pattern: `\xffB`},
		{Id: 4, Pattern: `\0377B`},
		{Id: 5, Pattern: raw80},
		{Id: 6, Pattern: rawFF},
	})
	if err != nil {
		f.Fatal(err)
	}
	if !scanner.automaton.rootByteFast || scanner.automaton.rootByteCount != 2 {
		f.Fatalf("test fixture did not select two binary root bytes: enabled=%t count=%d", scanner.automaton.rootByteFast, scanner.automaton.rootByteCount)
	}
	reference := *scanner
	reference.automaton.rootByteFast = false
	for _, seed := range [][]byte{
		{0x80, 'A', 0xff, 'B'},
		append([]byte(strings.Repeat("x", 512)), 0xff, 'B'),
		{0, '\n', 0x80, 'A', 0xff, 'B', 0},
		{0x7f, 0x80, 0xfe, 0xff},
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 4_096 {
			t.Skip()
		}
		want, err := reference.Scan(data)
		if err != nil {
			t.Fatal(err)
		}
		got, err := scanner.Scan(data)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("binary root-word scan matches = %#v, byte scanner matches = %#v for %q", got, want, data)
		}
		into, err := scanner.ScanInto(data, make([]Match, 0, len(got)))
		if err != nil {
			t.Fatal(err)
		}
		if !sameMatchSequence(into, got) {
			t.Fatalf("ScanInto matches = %#v, Scan matches = %#v for %q", into, got, data)
		}
	})
}

func sameMatchSequence(got, want []Match) bool {
	if len(got) != len(want) {
		return false
	}
	for index := range got {
		if got[index] != want[index] {
			return false
		}
	}
	return true
}

func matchMultiset(matches []Match) map[Match]int {
	result := make(map[Match]int, len(matches))
	for _, match := range matches {
		result[match]++
	}
	return result
}
