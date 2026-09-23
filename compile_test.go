package scankit

import (
	"context"
	"errors"
	"github.com/smartwalle/scankit/internal/compiler"
	"github.com/smartwalle/scankit/internal/parser"
	"testing"
)

func TestCompileRejectsDuplicateIDs(t *testing.T) {
	_, err := Compile([]Expression{{Id: 1, Pattern: "a"}, {Id: 1, Pattern: "b"}})
	var ce *compiler.CompileError
	if !errors.As(err, &ce) || ce.Kind != compiler.ErrorInvalidExpression {
		t.Fatalf("%v", err)
	}
}
func TestCompileFuzzyLiteral(t *testing.T) {
	s, err := Compile([]Expression{{Id: 1, Pattern: "hello", Ext: &ExpressionExt{Flags: ExtFlagEditDistance, EditDistance: 1}}})
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Scan([]byte("hallo"))
	if err != nil || len(got) != 1 {
		t.Fatalf("%v %#v", err, got)
	}
}

func TestCompileFuzzyCaselessLiteral(t *testing.T) {
	cases := []Expression{
		{Id: 1, Pattern: "Hello", Flags: FlagCaseless, Ext: &ExpressionExt{Flags: ExtFlagEditDistance, EditDistance: 1}},
		{Id: 2, Pattern: "Hello", Flags: FlagCaseless, Ext: &ExpressionExt{Flags: ExtFlagHammingDistance, HammingDistance: 1}},
	}
	s, err := Compile(cases)
	if err != nil {
		t.Fatal(err)
	}
	matches, err := s.Scan([]byte("hELLo hallo"))
	if err != nil || len(matches) != 4 {
		t.Fatalf("fuzzy caseless mismatch: %v %#v", err, matches)
	}
}

func TestFuzzyDistanceConversionSaturates(t *testing.T) {
	if got := uint32ToInt(^uint32(0)); got < 0 {
		t.Fatalf("模糊距离转换发生符号溢出: %d", got)
	}
}

func TestCompileLimits(t *testing.T) {
	_, err := compileWithContext(context.Background(), []Expression{{Id: 1, Pattern: "abcdef"}}, compiler.CompileLimits{GraphVertices: 1})
	var ce *compiler.CompileError
	if !errors.As(err, &ce) || ce.Kind != compiler.ErrorResourceLimit {
		t.Fatalf("%v", err)
	}
}

func TestLiteralPrefixAcrossSequenceAndAlternation(t *testing.T) {
	r, err := parser.Parse(`(?:foobar|foobaz)qux`)
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := literalPrefix(r); !ok || string(got) != "fooba" {
		t.Fatalf("公共前缀错误: %q %v", got, ok)
	}
	r, err = parser.Parse(`(?:foo|bar)baz`)
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := literalPrefix(r); ok || len(got) != 0 {
		t.Fatalf("无公共前缀不应生成候选: %q %v", got, ok)
	}
}

func TestCompileEnforcesRoleBudget(t *testing.T) {
	_, err := compileWithContext(context.Background(), []Expression{{Id: 1, Pattern: "a(b|c)"}}, compiler.CompileLimits{RoseRoles: 1})
	var ce *compiler.CompileError
	if !errors.As(err, &ce) || ce.Kind != compiler.ErrorResourceLimit {
		t.Fatalf("unexpected role budget result: %v", err)
	}
}

func TestCompileEnforcesDFAStateMemoryBudget(t *testing.T) {
	_, err := compileWithContext(context.Background(), []Expression{{Id: 1, Pattern: `(?:a|b){40}`}}, compiler.CompileLimits{MemoryBytes: 64})
	var ce *compiler.CompileError
	if !errors.As(err, &ce) || ce.Kind != compiler.ErrorResourceLimit {
		t.Fatalf("unexpected DFA memory budget result: %v", err)
	}
}

func TestCompileDFAStateLimitFallsBackToNFA(t *testing.T) {
	s, err := compileWithContext(context.Background(), []Expression{{Id: 1, Pattern: `(?:a|b){40}`}}, compiler.CompileLimits{DFAStates: 1})
	if err != nil {
		t.Fatal(err)
	}
	if got := s.backendName(1); got != "nfa" {
		t.Fatalf("state limit backend=%q", got)
	}
	matches, err := s.Scan([]byte("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"))
	if err != nil || len(matches) != 1 || matches[0].To != 40 {
		t.Fatalf("fallback result=%#v err=%v", matches, err)
	}
}

func TestCompileMemoryBudgetSkipsSpecializedBackend(t *testing.T) {
	s, err := compileWithContext(context.Background(), []Expression{{Id: 1, Pattern: `(?:ab|cd)`}}, compiler.CompileLimits{MemoryBytes: 1})
	if err != nil {
		var ce *compiler.CompileError
		if !errors.As(err, &ce) || ce.Kind != compiler.ErrorResourceLimit {
			t.Fatalf("错误分类=%v", err)
		}
		return
	}
	if s.backendName(1) == "mpv" || s.backendName(1) == "limex" {
		t.Fatalf("内存预算下仍选择专用后端=%s", s.backendName(1))
	}
}

func TestLeadingInlineFlagsApplyToCompiledRule(t *testing.T) {
	s, err := Compile([]Expression{{Id: 1, Pattern: `(?i)abc`}, {Id: 2, Pattern: `(?s)a.b`}, {Id: 3, Pattern: `(?m)^x$`}})
	if err != nil {
		t.Fatal(err)
	}
	matches, err := s.Scan([]byte("ABC a\nb\nx\n"))
	if err != nil || len(matches) != 3 || matches[0].Id != 1 || matches[1].Id != 2 || matches[2].Id != 3 {
		t.Fatalf("inline flags mismatch: %v %#v", err, matches)
	}
	flags, ok := s.ruleFlags(1)
	if !ok || flags&FlagCaseless == 0 {
		t.Fatalf("inline flag metadata missing: %v %v", flags, ok)
	}
}

func TestLeadingInlineFlagsCanDisableExpressionFlag(t *testing.T) {
	s, err := Compile([]Expression{{Id: 1, Pattern: `(?-i)abc`, Flags: FlagCaseless}})
	if err != nil {
		t.Fatal(err)
	}
	matches, err := s.Scan([]byte("ABC abc"))
	if err != nil || len(matches) != 1 || matches[0] != (Match{Id: 1, From: 4, To: 7}) {
		t.Fatalf("inline disable mismatch: %v %#v", err, matches)
	}
}

func TestCompileContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := compileWithContext(ctx, []Expression{{Id: 1, Pattern: "abc"}}, compiler.CompileLimits{})
	var compileErr *compiler.CompileError
	if !errors.As(err, &compileErr) || compileErr.Kind != compiler.ErrorCancelled {
		t.Fatalf("unexpected cancellation error: %v", err)
	}
}

func TestCompileBudgetRejectsEnginePlan(t *testing.T) {
	scanner, err := compileWithContext(context.Background(), []Expression{{Id: 1, Pattern: "abc"}}, compiler.CompileLimits{GraphVertices: 1})
	if scanner != nil || err == nil {
		t.Fatalf("资源限制下编译不应成功: %#v %v", scanner, err)
	}
}

func TestLiteralRulesUseSpecializedBlockPaths(t *testing.T) {
	s, err := Compile([]Expression{{Id: 1, Pattern: "short"}, {Id: 2, Pattern: "long-literal-value"}})
	if err != nil {
		t.Fatal(err)
	}
	if s.backendName(1) != "smallwrite" || s.backendName(2) != "smallblock" {
		t.Fatalf("unexpected literal backends: %q %q", s.backendName(1), s.backendName(2))
	}
	matches, err := s.Scan([]byte("short long-literal-value"))
	if err != nil || len(matches) != 2 || matches[0].Id != 1 || matches[1].Id != 2 {
		t.Fatalf("specialized literal scan mismatch: %v %#v", err, matches)
	}
}

func TestBackendSelectionMatrixUsesSafeFallbacks(t *testing.T) {
	rules := []Expression{
		{Id: 1, Pattern: "short"},
		{Id: 2, Pattern: "long-literal-value"},
		{Id: 3, Pattern: `a{2,4}`},
		{Id: 4, Pattern: `(?:ab|cd)+`},
		{Id: 5, Pattern: `(?<=a)b`},
		{Id: 6, Pattern: `\p{L}+`, Flags: FlagUTF8},
		{Id: 7, Pattern: `(a)\1`},
	}
	s, err := Compile(rules)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := s.backendName(1), "smallwrite"; got != want {
		t.Fatalf("rule 1 backend=%q, want %q", got, want)
	}
	if got, want := s.backendName(2), "smallblock"; got != want {
		t.Fatalf("rule 2 backend=%q, want %q", got, want)
	}
	if got, want := s.backendName(3), "repeat"; got != want {
		t.Fatalf("rule 3 backend=%q, want %q", got, want)
	}
	if got := s.backendName(4); got != "dfa" && got != "nfa" {
		t.Fatalf("rule 4 graph backend=%q", got)
	}
	for _, id := range []uint32{5, 6, 7} {
		if got := s.backendName(id); got != "ast" {
			t.Fatalf("rule %d unsafe backend=%q", id, got)
		}
	}
	data := []byte("short long-literal-value aaaa abcd ab A")
	matches, err := s.Scan(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) == 0 {
		t.Fatal("selection matrix produced no matches")
	}
	for _, m := range matches {
		if m.From > m.To || m.To > uint64(len(data)) {
			t.Fatalf("invalid match from backend %d: %#v", m.Id, m)
		}
	}
}

func TestMultipleLiteralRulesShareCandidateIndexWithoutLosingIDs(t *testing.T) {
	s, err := Compile([]Expression{{Id: 1, Pattern: "same"}, {Id: 2, Pattern: "same"}, {Id: 3, Pattern: "other"}})
	if err != nil {
		t.Fatal(err)
	}
	matches, err := s.Scan([]byte("same other"))
	if err != nil || len(matches) != 3 || matches[0].Id != 1 || matches[1].Id != 2 || matches[2].Id != 3 {
		t.Fatalf("shared literal candidate mismatch: %v %#v", err, matches)
	}
}

func TestLiteralCandidateSelectionExecutesSelectedMatcher(t *testing.T) {
	cases := []struct {
		name        string
		expressions []Expression
		data        []byte
		backend     string
		count       int
	}{
		{"短文字", []Expression{{Id: 1, Pattern: "ab"}, {Id: 2, Pattern: "cd"}}, []byte("ab cd"), "fdr", 2},
		{"长文字", []Expression{{Id: 1, Pattern: "long-literal"}, {Id: 2, Pattern: "short"}}, []byte("short long-literal"), "noodle", 2},
		{"共享前缀", []Expression{{Id: 1, Pattern: "hero"}, {Id: 2, Pattern: "her"}, {Id: 3, Pattern: "hers"}, {Id: 4, Pattern: "she"}, {Id: 5, Pattern: "his"}}, []byte("hers she his"), "noodle", 4},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, err := Compile(tc.expressions)
			if err != nil {
				t.Fatal(err)
			}
			matches, err := s.Scan(tc.data)
			if err != nil || s.literalCandidateBackend() != tc.backend || len(matches) != tc.count {
				t.Fatalf("backend=%q matches=%#v err=%v", s.literalCandidateBackend(), matches, err)
			}
		})
	}
}

func TestLiteralCandidatesConfirmNonLiteralRules(t *testing.T) {
	s, err := Compile([]Expression{
		{Id: 1, Pattern: "long-header-[0-9]+"},
		{Id: 2, Pattern: "long-footer-[A-Z]+"},
	})
	if err != nil {
		t.Fatal(err)
	}
	matches, err := s.Scan([]byte("long-header-42 long-header-x long-footer-OK long-footer-no"))
	if err != nil || s.literalCandidateBackend() != "teddy" || len(matches) != 2 || matches[0].Id != 1 || matches[1].Id != 2 {
		t.Fatalf("candidate confirmation mismatch: backend=%q matches=%#v err=%v", s.literalCandidateBackend(), matches, err)
	}
}

func TestPOSIXCharacterClassesExecute(t *testing.T) {
	s, err := Compile([]Expression{{Id: 1, Pattern: `[[:digit:]]+`}, {Id: 2, Pattern: `[[:^digit:]]+`}})
	if err != nil {
		t.Fatal(err)
	}
	matches, err := s.Scan([]byte("a12"))
	if err != nil || len(matches) != 2 || matches[0] != (Match{Id: 2, From: 0, To: 1}) || matches[1] != (Match{Id: 1, From: 1, To: 3}) {
		t.Fatalf("POSIX class mismatch: %#v, %v", matches, err)
	}
}

func TestAnyByteAndNewlineEscapesExecute(t *testing.T) {
	s, err := Compile([]Expression{{Id: 1, Pattern: `\C`}, {Id: 2, Pattern: `\R`}})
	if err != nil {
		t.Fatal(err)
	}
	matches, err := s.Scan([]byte("\r\n"))
	if err != nil || len(matches) != 3 {
		t.Fatalf("escape execution mismatch: %#v, %v", matches, err)
	}
	found := false
	for _, match := range matches {
		if match == (Match{Id: 2, From: 0, To: 2}) {
			found = true
		}
	}
	if !found {
		t.Fatalf("newline escape result missing: %#v", matches)
	}
}

func TestNonNewlineEscapeExecutes(t *testing.T) {
	s, err := Compile([]Expression{{Id: 1, Pattern: `\N+`}})
	if err != nil {
		t.Fatal(err)
	}
	matches, err := s.Scan([]byte("ab\ncd"))
	if err != nil || len(matches) != 2 || matches[0] != (Match{Id: 1, From: 0, To: 2}) || matches[1] != (Match{Id: 1, From: 3, To: 5}) {
		t.Fatalf("non-newline escape mismatch: %#v, %v", matches, err)
	}
}

func TestBracedOctalEscapeExecutes(t *testing.T) {
	s, err := Compile([]Expression{{Id: 1, Pattern: `\o{101}\a`}})
	if err != nil {
		t.Fatal(err)
	}
	matches, err := s.Scan([]byte("A\a"))
	if err != nil || len(matches) != 1 || matches[0] != (Match{Id: 1, From: 0, To: 2}) {
		t.Fatalf("octal escape mismatch: %v %#v", err, matches)
	}
}

func TestPureLiteralRepeatUsesIndependentBackend(t *testing.T) {
	s, err := Compile([]Expression{{Id: 1, Pattern: `(?:ab){2,3}`}, {Id: 2, Pattern: `x+`}})
	if err != nil {
		t.Fatal(err)
	}
	if s.backendName(1) != "repeat" || s.backendName(2) != "repeat" {
		t.Fatalf("unexpected repeat backends: %q %q", s.backendName(1), s.backendName(2))
	}
	matches, err := s.Scan([]byte("ababab xxx"))
	if err != nil || len(matches) != 2 || matches[0] != (Match{Id: 1, From: 0, To: 6}) || matches[1] != (Match{Id: 2, From: 7, To: 10}) {
		t.Fatalf("repeat backend mismatch: %#v, %v", matches, err)
	}
}

func TestFixedLiteralScanUsesNFAConfirmationWithoutSemanticDrift(t *testing.T) {
	s, err := Compile([]Expression{{Id: 1, Pattern: `ab`}})
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Scan([]byte("zab ab"))
	if err != nil || len(got) != 2 || got[0] != (Match{Id: 1, From: 1, To: 3}) || got[1] != (Match{Id: 1, From: 4, To: 6}) {
		t.Fatalf("扫描结果=%v err=%v", got, err)
	}
}

func TestClassPatternUsesSpecializedNFAConfirmation(t *testing.T) {
	s, err := Compile([]Expression{{Id: 1, Pattern: `a[0-9]`}})
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Scan([]byte("a1 xa9 ax"))
	if err != nil || len(got) != 2 || got[0] != (Match{Id: 1, From: 0, To: 2}) || got[1] != (Match{Id: 1, From: 4, To: 6}) {
		t.Fatalf("类别扫描=%v err=%v", got, err)
	}
}

func TestSingleLiteralRuleUsesSpecializedWholeInputSearch(t *testing.T) {
	for _, tc := range []struct {
		pattern string
		data    string
		backend string
		count   int
	}{
		{"abab", "abab xx abab", "smallwrite", 2},
		{"long-literal", "long-literal long-literal", "smallblock", 2},
	} {
		s, err := Compile([]Expression{{Id: 1, Pattern: tc.pattern}})
		if err != nil {
			t.Fatal(err)
		}
		matches, err := s.Scan([]byte(tc.data))
		if err != nil || s.backendName(1) != tc.backend || len(matches) != tc.count {
			t.Fatalf("single literal search %q: backend=%q matches=%#v err=%v", tc.pattern, s.backendName(1), matches, err)
		}
	}
}

func TestScopedInlineFlags(t *testing.T) {
	scanner, err := Compile([]Expression{
		{Id: 1, Pattern: `a(?i:bc)d`},
		{Id: 2, Pattern: `(?s:a.b)`},
		{Id: 3, Pattern: `(?m:^a$)`},
		{Id: 4, Pattern: `(?-i:ab)`, Flags: FlagCaseless},
	})
	if err != nil {
		t.Fatal(err)
	}
	matches, err := scanner.Scan([]byte("aBCd a\nb x\na\n AB"))
	if err != nil {
		t.Fatal(err)
	}
	got := map[uint32]int{}
	for _, match := range matches {
		got[match.Id]++
	}
	if got[1] != 1 || got[2] != 1 || got[3] != 1 || got[4] != 0 {
		t.Fatalf("matches=%#v", matches)
	}
	info, ok := scanner.expressionInfo(1)
	if !ok || !info.RequiresStatefulRuntime() {
		t.Fatalf("scoped flags must bypass byte-table backend: %#v", info)
	}
}

func TestGlobalInlineFlagsApplyToScanner(t *testing.T) {
	scanner, err := Compile([]Expression{{Id: 1, Pattern: "(?i)abc"}, {Id: 2, Pattern: "(?m)^a$"}})
	if err != nil {
		t.Fatal(err)
	}
	matches, err := scanner.Scan([]byte("ABC\na\nb"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 2 || matches[0].Id != 1 || matches[1].Id != 2 {
		t.Fatalf("全局修饰符匹配=%#v", matches)
	}
}

func TestExtendedInlineFlags(t *testing.T) {
	scanner, err := Compile([]Expression{
		{Id: 1, Pattern: "(?x)a # first\n b [c]"},
		{Id: 2, Pattern: `(?x:a # first
 b)(?-x: )c`},
	})
	if err != nil {
		t.Fatal(err)
	}
	matches, err := scanner.Scan([]byte("abc ab c"))
	if err != nil || len(matches) != 2 || matches[0] != (Match{Id: 1, From: 0, To: 3}) || matches[1] != (Match{Id: 2, From: 4, To: 8}) {
		t.Fatalf("extended matches: %v %#v", err, matches)
	}
}
