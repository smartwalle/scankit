package scankit

import (
	"reflect"
	"testing"
)

// 本测试比较编译前缀状态复用路径与禁用该可选缓存后的同一数据库。模式刻意使用结构化形式
// 而非 PII 专用形式：有界字节类位于字面量触发器之前，后接可变后缀。
func TestAnchoredPrefixDFAReuseMatchesFullVerifier(t *testing.T) {
	database, err := Compile([]Expression{{
		Id:      1,
		Pattern: `[a-z]{1,8}@[a-z]{1,8}\.(com|net)`,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(database.regexPrograms) != 1 || len(database.regexPrograms[0].prefixDFAStates) != 9 {
		t.Fatalf("prefix DFA states = %d, want 9", len(database.regexPrograms[0].prefixDFAStates))
	}

	reference := *database
	reference.regexPrograms = append([]compiledRegexProgram(nil), database.regexPrograms...)
	reference.regexPrograms[0].prefixDFAStates = nil
	data := []byte("ab@cd.com x ab@cdef.net malformed=abcdef@domain.invalid a@b.com")
	got, err := database.Scan(data)
	if err != nil {
		t.Fatal(err)
	}
	want, err := reference.Scan(data)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("prefix-state matches = %#v, full verifier matches = %#v", got, want)
	}
}

func TestSharedAnchoredPrefixDFAReuseMatchesFullVerifier(t *testing.T) {
	expressions := []Expression{
		{Id: 1, Pattern: `[a-z]{1,8}@[a-z]{1,8}\.(com|net)`},
		{Id: 2, Pattern: `[a-z]{1,8}@[a-z]{1,8}\.(com|net)`},
	}
	database, err := Compile(expressions)
	if err != nil {
		t.Fatal(err)
	}
	if database.singleByteSimple || len(database.regexPrograms) != 2 {
		t.Fatal("fixture did not select the shared single-byte trigger path")
	}
	reference := *database
	reference.regexPrograms = append([]compiledRegexProgram(nil), database.regexPrograms...)
	for index := range reference.regexPrograms {
		reference.regexPrograms[index].prefixDFAStates = nil
	}
	data := []byte("ab@cd.com x abc@def.net malformed=abcdef@domain.invalid")
	got, err := database.Scan(data)
	if err != nil {
		t.Fatal(err)
	}
	want, err := reference.Scan(data)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("shared prefix-state matches = %#v, full verifier matches = %#v", got, want)
	}
}

func FuzzAnchoredPrefixDFAReuseMatchesFullVerifier(f *testing.F) {
	database, err := Compile([]Expression{
		{Id: 1, Pattern: `[a-z]{1,8}@[a-z]{1,8}\.(com|net)`},
		{Id: 2, Pattern: `[a-z]{1,8}@[a-z]{1,8}\.(com|net)`},
	})
	if err != nil {
		f.Fatal(err)
	}
	if database.singleByteSimple || len(database.regexPrograms) != 2 || len(database.regexPrograms[0].prefixDFAStates) == 0 {
		f.Fatal("fixture did not select prefix DFA state reuse")
	}
	reference := *database
	reference.regexPrograms = append([]compiledRegexProgram(nil), database.regexPrograms...)
	for index := range reference.regexPrograms {
		reference.regexPrograms[index].prefixDFAStates = nil
	}
	for _, seed := range [][]byte{
		[]byte("ab@cd.com abc@def.net"),
		[]byte("a@b.com invalid@domain.invalid"),
		[]byte("abcdefgh@abcdefgh.net"),
		{0xff, 'a', '@', 'b', '.', 'c', 'o', 'm'},
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
			t.Fatalf("prefix-state matches = %#v, full verifier matches = %#v for %q", got, want, data)
		}
	})
}

func TestRootByteAnchoredPrefixDFAReuseMatchesFullVerifier(t *testing.T) {
	optimized, err := Compile([]Expression{{
		Id:      1,
		Pattern: `[A-Z]{1,8}acct=[A-Z]{4}[0-9]{4}`,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(optimized.regexPrograms) != 1 || len(optimized.regexPrograms[0].prefixDFAStates) == 0 || optimized.singleByteOnly {
		t.Fatal("fixture did not select the root-byte prefix DFA path")
	}
	reference := withoutRootByteAnchoredPrefixDFA(optimized)
	data := []byte(`Xacct=ABCD1234 QRacct=WXYZ9876 acct=ABCD1234 Xacct=ABCDxxxx`)
	got, err := optimized.Scan(data)
	if err != nil {
		t.Fatal(err)
	}
	want, err := reference.Scan(data)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("root-byte prefix-state matches = %#v, full verifier matches = %#v", got, want)
	}
}

func FuzzRootByteAnchoredPrefixDFAReuseMatchesFullVerifier(f *testing.F) {
	optimized, err := Compile([]Expression{{
		Id:      1,
		Pattern: `[A-Z]{1,8}acct=[A-Z]{4}[0-9]{4}`,
	}})
	if err != nil {
		f.Fatal(err)
	}
	if len(optimized.regexPrograms) != 1 || len(optimized.regexPrograms[0].prefixDFAStates) == 0 || optimized.singleByteOnly {
		f.Fatal("fixture did not select the root-byte prefix DFA path")
	}
	reference := withoutRootByteAnchoredPrefixDFA(optimized)
	for _, seed := range [][]byte{
		[]byte(`Xacct=ABCD1234 QRacct=WXYZ9876`),
		[]byte(`acct=ABCD1234 Xacct=ABCDxxxx`),
		[]byte(`ABCDEFGHacct=ABCD1234`),
		{0xff, 'X', 'a', 'c', 'c', 't', '=', 'A', 'B', 'C', 'D', '1', '2', '3', '4'},
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 4_096 {
			t.Skip()
		}
		got, err := optimized.Scan(data)
		if err != nil {
			t.Fatal(err)
		}
		want, err := reference.Scan(data)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("root-byte prefix-state matches = %#v, full verifier matches = %#v for %q", got, want, data)
		}
	})
}

func withoutRootByteAnchoredPrefixDFA(scanner *Scanner) *Scanner {
	reference := *scanner
	reference.regexPrograms = append([]compiledRegexProgram(nil), scanner.regexPrograms...)
	for index := range reference.regexPrograms {
		reference.regexPrograms[index].prefixDFAStates = nil
	}
	reference.blockScanPlan.triggers = append([]blockTriggerLane(nil), scanner.blockScanPlan.triggers...)
	for index := range reference.blockScanPlan.triggers {
		reference.blockScanPlan.triggers[index].anchored.prefixDFAStates = nil
	}
	return &reference
}

func TestLeadingAnchorSuffixClassPrefilter(t *testing.T) {
	root, err := parseRegex(`1[3-9][0-9]{9}`)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := compileByteRegexPlan(root, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.hasSuffixClass {
		t.Fatal("phone-shaped expression did not retain a suffix-class prefilter")
	}
	if !plan.suffixClass.contains('3') || !plan.suffixClass.contains('9') || plan.suffixClass.contains('2') {
		t.Fatalf("suffix prefilter class = %#v, want [3-9]", plan.suffixClass)
	}
}

func TestLeadingAnchorSuffixChecksRecognizeFixedSelectiveRun(t *testing.T) {
	for _, test := range []struct {
		name      string
		pattern   string
		wantCount int
	}{
		{name: "fixed repeat", pattern: `account=[AB]{4}[0-9]{4}`, wantCount: 4},
		{name: "variable repeat retains minimum", pattern: `account=[AB]{1,4}[0-9]{4}`, wantCount: 1},
		{name: "wide class is skipped", pattern: `account=[0-9]{4}`, wantCount: 0},
		{name: "alternation remains verifier only", pattern: `account=(?:AB|BA)[0-9]{2}`, wantCount: 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			root, err := parseRegex(test.pattern)
			if err != nil {
				t.Fatal(err)
			}
			analysis, err := analyzeRegex(root)
			if err != nil {
				t.Fatal(err)
			}
			anchor, ok := selectRegexAnchor(analysis)
			if !ok {
				t.Fatal("selectRegexAnchor() did not select the literal prefix")
			}
			checks := leadingAnchorSuffixChecks(root, anchor)
			if int(checks.count) != test.wantCount {
				t.Fatalf("suffix check count = %d, want %d", checks.count, test.wantCount)
			}
			for index := 0; index < int(checks.count); index++ {
				class := anchoredSuffixCheckClass(checks, index)
				if !class.contains('A') || !class.contains('B') || class.contains('C') {
					t.Fatalf("suffix check %d = %#v, want [AB]", index, class)
				}
			}
		})
	}
}

func anchoredSuffixCheckClass(checks anchoredSuffixCheckPlan, index int) byteClass {
	if index == 0 {
		return checks.first
	}
	return checks.additional[index-1]
}

func TestLeadingAnchorSuffixClassRepeatMatchesFullVerifier(t *testing.T) {
	optimized, err := Compile([]Expression{{Id: 1, Pattern: `account=[AB]{4}[0-9]{4}`}})
	if err != nil {
		t.Fatal(err)
	}
	if len(optimized.regexPrograms) != 1 || !optimized.regexPrograms[0].hasSuffixClass {
		t.Fatal("positive suffix repeat did not select a class prefilter")
	}
	reference, err := Compile([]Expression{{Id: 1, Pattern: `(?:)account=[AB]{4}[0-9]{4}`}})
	if err != nil {
		t.Fatal(err)
	}
	data := []byte(`account=ABAB1234 account=CBAA1234 account=BABA9876`)
	got, err := optimized.Scan(data)
	if err != nil {
		t.Fatal(err)
	}
	want, err := reference.Scan(data)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("suffix-prefilter matches = %#v, full verifier matches = %#v", got, want)
	}
}

func FuzzLeadingAnchorSuffixClassRepeatMatchesFullVerifier(f *testing.F) {
	optimized, err := Compile([]Expression{{Id: 1, Pattern: `account=[AB]{4}[0-9]{4}`}})
	if err != nil {
		f.Fatal(err)
	}
	reference, err := Compile([]Expression{{Id: 1, Pattern: `(?:)account=[AB]{4}[0-9]{4}`}})
	if err != nil {
		f.Fatal(err)
	}
	for _, seed := range [][]byte{[]byte(`account=ABAB1234`), []byte(`account=CBAA1234`), []byte{}, {0xff, 'a', 'c', 'c'}} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 4_096 {
			t.Skip()
		}
		got, err := optimized.Scan(data)
		if err != nil {
			t.Fatal(err)
		}
		want, err := reference.Scan(data)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("suffix-prefilter matches = %#v, full verifier matches = %#v for %q", got, want, data)
		}
	})
}

func TestLeadingAnchorSuffixChecksMatchFullVerifier(t *testing.T) {
	optimized, err := Compile([]Expression{
		{Id: 1, Pattern: `account=[AB]{4}[0-9]{4}`},
		{Id: 2, Pattern: `account=[AB]{4}[0-9]{4}`, Flags: CompileQuiet},
		{Id: 3, Pattern: `1|2`, Flags: CompileCombination},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(optimized.regexPrograms) != 2 || optimized.regexPrograms[0].suffixChecks == nil {
		t.Fatal("fixture did not select multi-byte suffix checks")
	}
	reference := withoutAnchoredSuffixChecks(optimized)
	data := []byte(`account=ABAB1234 account=ABCB1234 account=BABA9876 account=ABAA12x4 account=ABAB1234`)
	got, err := optimized.Scan(data)
	if err != nil {
		t.Fatal(err)
	}
	want, err := reference.Scan(data)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("suffix-check matches = %#v, full verifier matches = %#v", got, want)
	}
}

func FuzzLeadingAnchorSuffixChecksMatchFullVerifier(f *testing.F) {
	optimized, err := Compile([]Expression{
		{Id: 1, Pattern: `account=[AB]{4}[0-9]{4}`},
		{Id: 2, Pattern: `account=[AB]{4}[0-9]{4}`, Flags: CompileQuiet},
		{Id: 3, Pattern: `1|2`, Flags: CompileCombination},
	})
	if err != nil {
		f.Fatal(err)
	}
	reference := withoutAnchoredSuffixChecks(optimized)
	for _, seed := range [][]byte{
		[]byte(`account=ABAB1234 account=ABCB1234`),
		[]byte(`account=BABA9876 account=ABAA12x4`),
		[]byte(`account=ABAB`),
		{0xff, 'a', 'c', 'c', 'o', 'u', 'n', 't', '='},
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 4_096 {
			t.Skip()
		}
		got, err := optimized.Scan(data)
		if err != nil {
			t.Fatal(err)
		}
		want, err := reference.Scan(data)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("suffix-check matches = %#v, full verifier matches = %#v for %q", got, want, data)
		}
	})
}

func withoutAnchoredSuffixChecks(scanner *Scanner) *Scanner {
	reference := *scanner
	reference.regexPrograms = append([]compiledRegexProgram(nil), scanner.regexPrograms...)
	for index := range reference.regexPrograms {
		reference.regexPrograms[index].hasSuffixClass = false
		reference.regexPrograms[index].suffixChecks = nil
	}
	reference.blockScanPlan.triggers = append([]blockTriggerLane(nil), scanner.blockScanPlan.triggers...)
	for index := range reference.blockScanPlan.triggers {
		anchored := &reference.blockScanPlan.triggers[index].anchored
		anchored.hasSuffixClass = false
		anchored.suffixChecks = nil
	}
	return &reference
}
