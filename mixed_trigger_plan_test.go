package scankit

import (
	"bytes"
	"reflect"
	"testing"
)

func TestMixedTriggeredBlockScanPreservesGenericSemantics(t *testing.T) {
	expressions := []Expression{
		{Id: 1, Pattern: `0[0-9]{5}`, Flags: CompileSingleMatch},
		{Id: 2, Pattern: `(?:1a|2b|3c|4d){4}`},
		{Id: 3, Pattern: `[a-z]{0,64}@example\.com`},
		{Id: 4, Pattern: `0[0-9]{5}`, Flags: CompileQuiet},
		{Id: 5, Pattern: `1|2`, Flags: CompileCombination},
	}
	scanner, err := Compile(expressions)
	if err != nil {
		t.Fatal(err)
	}
	if len(scanner.blockScanPlan.unanchored.always) != 0 {
		t.Fatalf("mixed fixture unexpectedly has always lanes: %d", len(scanner.blockScanPlan.unanchored.always))
	}
	if scanner.blockScanPlan.mixed.count == 0 {
		t.Fatal("mixed fixture has no merged trigger bytes")
	}
	if singleByteWordScanAvailable && !scanner.blockScanPlan.mixed.wordScan {
		t.Fatalf("mixed fixture did not select word scan: triggers=%d", scanner.blockScanPlan.mixed.count)
	}

	reference := *scanner
	reference.blockScanPlan.mixed.wordScan = false
	data := []byte("ordinary text 012345 1a2b3c4d alice@example.com 054321 4d3c2b1a x@example.com")
	got, err := scanner.ScanInto(data, make([]Match, 0, 16))
	if err != nil {
		t.Fatal(err)
	}
	want, err := reference.ScanInto(data, make([]Match, 0, 16))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("mixed triggered ScanInto() = %#v, generic ScanInto() = %#v", got, want)
	}
}

func TestMixedTriggerPlanRejectsByteStepLanes(t *testing.T) {
	for _, test := range []struct {
		name        string
		expressions []Expression
	}{
		{
			name: "always repeat",
			expressions: []Expression{
				{Id: 1, Pattern: `0[0-9]{5}`},
				{Id: 2, Pattern: `[a-z]+`},
			},
		},
		{
			name: "empty expression",
			expressions: []Expression{
				{Id: 1, Pattern: `0[0-9]{5}`},
				{Id: 2, Pattern: `a*`, Flags: CompileAllowEmpty},
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			scanner, err := Compile(test.expressions)
			if err != nil {
				t.Fatal(err)
			}
			if scanner.blockScanPlan.mixed.wordScan {
				t.Fatal("mixed word scan unexpectedly selected a byte-step fixture")
			}
		})
	}
}

func TestMixedTriggerPlanFallsBackForSensitiveTokenRule(t *testing.T) {
	scanner, err := Compile([]Expression{
		{Id: 1, Pattern: `(?:\b|^)(?:\+86|86)?1[3-9]\d{9}(?:\b|$)`},
		{Id: 2, Pattern: "[A-Za-z0-9.!#$%&'*+/?^_`{|}~-]{1,64}@[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?(?:\\.[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?)+\\b"},
		{Id: 3, Pattern: `[1-9][0-9]{5}(18|19|20)[0-9]{2}(0[1-9]|1[0-2])(0[1-9]|[12][0-9]|3[01])[0-9]{3}[0-9Xx]`},
		{Id: 4, Pattern: `62[0-9]{14,17}`},
		{Id: 5, Pattern: `4[0-9]{15}|5[1-5][0-9]{14}|3[47][0-9]{13}`},
		{Id: 6, Pattern: `[z][a-z]{9,}`},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(scanner.blockScanPlan.unanchored.always) != 0 {
		t.Fatalf("always lane count = %d, want 0", len(scanner.blockScanPlan.unanchored.always))
	}
	if !scanner.blockScanPlan.unanchored.hasPrefixRepeatLanes() {
		t.Fatal("fixture did not select a prefix-repeat lane")
	}
	if singleByteWordScanAvailable && !scanner.blockScanPlan.mixed.wordScan {
		t.Fatal("mixed word scan did not select a prefix-repeat fixture")
	}
	if got := string(scanner.blockScanPlan.mixed.values[:scanner.blockScanPlan.mixed.count]); got != "123456@z" {
		t.Fatalf("mixed trigger bytes = %q, want %q", got, "123456@z")
	}
}

func TestMixedTriggerPlanRejectsMoreThanEightBytes(t *testing.T) {
	expressions := []Expression{{Id: 1, Pattern: `0[0-9]{5}`}}
	for index, value := range []string{"1", "2", "3", "4", "5", "6", "7", "8"} {
		expressions = append(expressions, Expression{Id: uint32(index + 2), Pattern: value})
	}
	scanner, err := Compile(expressions)
	if err != nil {
		t.Fatal(err)
	}
	if scanner.blockScanPlan.mixed.wordScan || scanner.blockScanPlan.mixed.count != 0 {
		t.Fatalf("mixed plan accepted more than eight bytes: enabled=%t count=%d", scanner.blockScanPlan.mixed.wordScan, scanner.blockScanPlan.mixed.count)
	}
}

func TestMixedTriggerPlanRejectsLetterTriggers(t *testing.T) {
	scanner, err := Compile([]Expression{
		{Id: 1, Pattern: `P[0-9]{5}`},
		{Id: 2, Pattern: `(?:a1|b2|c3|d4){4}`},
	})
	if err != nil {
		t.Fatal(err)
	}
	if scanner.blockScanPlan.mixed.wordScan || scanner.blockScanPlan.mixed.count != 0 {
		t.Fatalf("mixed plan accepted letter triggers: enabled=%t count=%d", scanner.blockScanPlan.mixed.wordScan, scanner.blockScanPlan.mixed.count)
	}
}

func TestMixedTriggerPlanCoversLogRuleShape(t *testing.T) {
	expressions := []Expression{
		{Id: 1, Pattern: `(?:\b|^)(?:\+86|86)?1[3-9]\d{9}(?:\b|$)`},
		{Id: 2, Pattern: "[A-Za-z0-9.!#$%&'*+/?^_`{|}~-]{1,64}@[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?(?:\\.[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?)+\\b"},
		{Id: 3, Pattern: `[1-9][0-9]{5}(18|19|20)[0-9]{2}(0[1-9]|1[0-2])(0[1-9]|[12][0-9]|3[01])[0-9]{3}[0-9Xx]`},
		{Id: 4, Pattern: `62[0-9]{14,17}`},
		{Id: 5, Pattern: `4[0-9]{15}|5[1-5][0-9]{14}|3[47][0-9]{13}`},
	}
	scanner, err := Compile(expressions)
	if err != nil {
		t.Fatal(err)
	}
	if len(scanner.blockScanPlan.unanchored.always) != 0 {
		t.Fatalf("log fixture has always lanes: %d", len(scanner.blockScanPlan.unanchored.always))
	}
	if scanner.blockScanPlan.mixed.count == 0 {
		t.Fatal("log fixture has no merged triggers")
	}
	if singleByteWordScanAvailable && !scanner.blockScanPlan.mixed.wordScan {
		t.Fatalf("log fixture did not select mixed word scan: triggers=%d", scanner.blockScanPlan.mixed.count)
	}
	if got := string(scanner.blockScanPlan.mixed.values[:scanner.blockScanPlan.mixed.count]); got != "123456@" {
		t.Fatalf("log mixed trigger bytes = %q, want %q", got, "123456@")
	}
	mixed := scanner.blockScanPlan.mixed
	if !mixed.rangeFast || mixed.rangeMinimum != '1' || mixed.rangeMaximum != '6' || mixed.remainderLen != 1 || mixed.remainder[0] != '@' {
		t.Fatalf("log mixed range plan = %#v, want range 1-6 plus @", mixed)
	}
}

func FuzzMixedTriggeredBlockScanPreservesGenericSemantics(f *testing.F) {
	expressions := []Expression{
		{Id: 1, Pattern: `0[0-9]{5}`, Flags: CompileSingleMatch},
		{Id: 2, Pattern: `(?:1a|2b|3c|4d){4}`},
		{Id: 3, Pattern: `[a-z]{0,64}@example\.com`},
		{Id: 4, Pattern: `0[0-9]{5}`, Flags: CompileQuiet},
		{Id: 5, Pattern: `1|2`, Flags: CompileCombination},
	}
	scanner, err := Compile(expressions)
	if err != nil {
		f.Fatal(err)
	}
	if singleByteWordScanAvailable && !scanner.blockScanPlan.mixed.wordScan {
		f.Fatal("mixed fuzz fixture did not select word scan")
	}
	reference := *scanner
	reference.blockScanPlan.mixed.wordScan = false
	for _, seed := range [][]byte{
		[]byte("012345 1a2b3c4d alice@example.com"),
		[]byte("ordinary log without candidates"),
		[]byte("012345xxxxxxxalice@example.com 4d3c2b1a"),
		{0, '0', '1', '2', '3', '4', '5', 0xff, '@'},
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 4_096 {
			t.Skip()
		}
		got, err := scanner.ScanInto(data, make([]Match, 0, 16))
		if err != nil {
			t.Fatal(err)
		}
		want, err := reference.ScanInto(data, make([]Match, 0, 16))
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("mixed ScanInto(%q) = %#v, generic ScanInto() = %#v", data, got, want)
		}
	})
}

// BenchmarkProfileMixedTriggerPlan 比较同一编译规则集是否启用混合触发窗口跳过。它只
// 描述 AC、fixed 和 class-anchor 的通用组合，用于定位该调度层本身，不是业务基准。
func BenchmarkProfileMixedTriggerPlan(b *testing.B) {
	expressions := []Expression{
		{Id: 1, Pattern: `0[0-9]{5}`},
		{Id: 2, Pattern: `(?:1a|2b|3c|4d){4}`},
		{Id: 3, Pattern: `[a-z]{0,64}@example\.com`},
	}
	scanner, err := Compile(expressions)
	if err != nil {
		b.Fatal(err)
	}
	if !scanner.blockScanPlan.mixed.wordScan {
		b.Fatalf("fixture did not select mixed word scan: triggers=%d", scanner.blockScanPlan.mixed.count)
	}
	reference := *scanner
	reference.blockScanPlan.mixed.wordScan = false
	data := bytes.Repeat([]byte(`service=payment message=ordinary-log record=processed 用户=张三 `), 384)
	want, err := scanner.Scan(data)
	if err != nil {
		b.Fatal(err)
	}
	for _, test := range []struct {
		name    string
		scanner *Scanner
	}{
		{name: "Generic", scanner: &reference},
		{name: "MixedTriggered", scanner: scanner},
	} {
		b.Run(test.name, func(b *testing.B) {
			benchmarkProfileScanInto(b, test.scanner, data, want)
		})
	}
}
