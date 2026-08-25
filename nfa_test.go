package scankit

import (
	"errors"
	"slices"
	"testing"
)

func TestCompileNFAProducesClosedPrograms(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		pattern       string
		minimumStates int
	}{
		{name: "literal", pattern: "ab", minimumStates: 3},
		{name: "alternation", pattern: "foo|bar", minimumStates: 8},
		{name: "unbounded repeat", pattern: "[a-z]*", minimumStates: 3},
		{name: "bounded repeat", pattern: "\\d{2,4}", minimumStates: 5},
		{name: "anchors", pattern: "^id-[0-9]+$", minimumStates: 7},
		{name: "empty branch", pattern: "foo|", minimumStates: 5},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root, err := parseRegex(tt.pattern)
			if err != nil {
				t.Fatal(err)
			}
			program, err := compileNFA(root)
			if err != nil {
				t.Fatalf("compileNFA(%q) error = %v", tt.pattern, err)
			}
			if len(program.states) < tt.minimumStates {
				t.Fatalf("state count = %d, want at least %d", len(program.states), tt.minimumStates)
			}
			if tt.pattern == "^id-[0-9]+$" && program.epsilonClosure != nil {
				t.Fatal("anchored NFA unexpectedly received assertion-free closure cache")
			}
			if tt.pattern == "^id-[0-9]+$" && program.verifierDFA != nil {
				t.Fatal("anchored NFA unexpectedly received verifier DFA")
			}
			if tt.pattern == "ab" && program.epsilonClosure == nil {
				t.Fatal("small assertion-free NFA did not receive closure cache")
			}
			if tt.pattern == "ab" && program.verifierDFA == nil {
				t.Fatal("small assertion-free NFA did not receive verifier DFA")
			}
			assertNFAClosed(t, program)
		})
	}
}

func TestCompileNFARejectsExcessiveRepeat(t *testing.T) {
	t.Parallel()

	root, err := parseRegex("a{4097}")
	if err != nil {
		t.Fatal(err)
	}
	_, err = compileNFA(root)
	if !errors.Is(err, ErrRegexTooComplex) {
		t.Fatalf("compileNFA() error = %v, want regex too complex", err)
	}
}

func TestCompileNFABoundsEpsilonClosureCache(t *testing.T) {
	t.Parallel()

	root, err := parseRegex("a{513}")
	if err != nil {
		t.Fatal(err)
	}
	program, err := compileNFA(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(program.states) <= maxCachedClosureStates {
		t.Fatalf("state count = %d, want more than cache limit %d", len(program.states), maxCachedClosureStates)
	}
	if program.epsilonClosure != nil {
		t.Fatal("large NFA unexpectedly allocated epsilon closure cache")
	}
	if program.verifierDFA != nil {
		t.Fatal("large NFA unexpectedly allocated verifier DFA")
	}
}

func TestNFAVerifierDFATailAssertionMatchesDynamicVerifier(t *testing.T) {
	t.Parallel()

	tests := []struct {
		pattern string
		data    string
	}{
		{pattern: `(?:foo|bar)+\b`, data: ` foofoobar barx bar `},
		{pattern: `(?:foo|bar)+\B`, data: `afoobarz foo bar`},
		{pattern: `id-(?:[0-9]{2}|[a-z]{2})$`, data: "id-12\nid-ab\nxid-12"},
		{pattern: `foo$`, data: "foo\nfoo"},
		{pattern: `(?:id|end)\z`, data: "id\nend"},
		{pattern: `(?:id|last)\Z`, data: "id\nlast\n"},
	}
	for _, tt := range tests {
		t.Run(tt.pattern, func(t *testing.T) {
			root, err := parseRegex(tt.pattern)
			if err != nil {
				t.Fatal(err)
			}
			program, err := compileNFA(root)
			if err != nil {
				t.Fatal(err)
			}
			if program.verifierDFA == nil || !program.verifierDFA.hasTailAssertion {
				t.Fatal("NFA did not receive tail assertion DFA")
			}
			dynamic := program
			dynamic.verifierDFA = nil
			for start := range len(tt.data) + 1 {
				got := nfaMatchFrom(program, []byte(tt.data), start)
				want := nfaMatchFrom(dynamic, []byte(tt.data), start)
				if !slices.Equal(got, want) {
					t.Fatalf("nfaMatchFrom(%q, start=%d) = %v, want %v", tt.data, start, got, want)
				}
			}
		})
	}
}

func TestNFAVerifierDFATailAssertionHonorsMultiline(t *testing.T) {
	root, err := parseRegex(`foo$`)
	if err != nil {
		t.Fatal(err)
	}
	applyRegexFlags(root, CompileMultiline)
	program, err := compileNFA(root)
	if err != nil {
		t.Fatal(err)
	}
	if program.verifierDFA == nil || !program.verifierDFA.hasTailAssertion {
		t.Fatal("multiline tail assertion did not receive verifier DFA")
	}
	dynamic := program
	dynamic.verifierDFA = nil
	data := []byte("foo\nfoo")
	for start := range len(data) + 1 {
		got := nfaMatchFrom(program, data, start)
		want := nfaMatchFrom(dynamic, data, start)
		if !slices.Equal(got, want) {
			t.Fatalf("nfaMatchFrom(start=%d) = %v, want %v", start, got, want)
		}
	}
}

func TestNFAVerifierDFARejectsNonTailAssertion(t *testing.T) {
	for _, pattern := range []string{`\bfoo\b`, `a?\b`} {
		t.Run(pattern, func(t *testing.T) {
			root, err := parseRegex(pattern)
			if err != nil {
				t.Fatal(err)
			}
			program, err := compileNFA(root)
			if err != nil {
				t.Fatal(err)
			}
			if program.verifierDFA != nil {
				t.Fatal("NFA unexpectedly received verifier DFA")
			}
		})
	}
}

func TestNFAVerifierDFATailAssertionEmailPlan(t *testing.T) {
	root, err := parseRegex("[A-Za-z0-9.!#$%&'*+/=?^_`{|}~-]{1,64}@[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?(?:\\.[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?)+\\b")
	if err != nil {
		t.Fatal(err)
	}
	program, err := compileNFA(root)
	if err != nil {
		t.Fatal(err)
	}
	if program.verifierDFA == nil || !program.verifierDFA.hasTailAssertion {
		t.Fatalf("complex tail-assertion NFA did not receive verifier DFA: states=%d", len(program.states))
	}
}

func TestCompileNFAQuotedUTF8Literal(t *testing.T) {
	root, err := parseRegex("\\Qė")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := compileNFA(root); err != nil {
		t.Fatalf("compileNFA() error = %v", err)
	}
}

func FuzzCompileNFA(f *testing.F) {
	for _, seed := range []string{"", "token", "(foo|bar)+", "[a-z]{1,8}", "^\\d+$", "a{4097}"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, pattern string) {
		if len(pattern) > 1_024 {
			t.Skip()
		}
		root, err := parseRegex(pattern)
		if err != nil {
			return
		}
		program, err := compileNFA(root)
		if err != nil {
			if !errors.Is(err, ErrRegexTooComplex) {
				t.Fatalf("compileNFA(%q) error = %v", pattern, err)
			}
			return
		}
		assertNFAClosed(t, program)
	})
}

func FuzzNFAVerifierDFATailAssertionMatchesDynamicVerifier(f *testing.F) {
	for _, seed := range []struct {
		pattern string
		data    string
	}{
		{`(?:foo|bar)+\b`, ` foofoobar barx bar `},
		{`(?:foo|bar)+\B`, `afoobarz foo bar`},
		{`id-(?:[0-9]{2}|[a-z]{2})$`, "id-12\nid-ab\nxid-12"},
		{`(?:id|end)\z`, "id\nend"},
	} {
		f.Add(seed.pattern, seed.data)
	}
	f.Fuzz(func(t *testing.T, pattern, data string) {
		if len(pattern) > 512 || len(data) > 1_024 {
			t.Skip()
		}
		root, err := parseRegex(pattern)
		if err != nil {
			return
		}
		program, err := compileNFA(root)
		if err != nil || program.verifierDFA == nil || !program.verifierDFA.hasTailAssertion {
			return
		}
		dynamic := program
		dynamic.verifierDFA = nil
		bytes := []byte(data)
		for start := range len(bytes) + 1 {
			got := nfaMatchFrom(program, bytes, start)
			want := nfaMatchFrom(dynamic, bytes, start)
			if !slices.Equal(got, want) {
				t.Fatalf("nfaMatchFrom(%q, %q, start=%d) = %v, want %v", pattern, data, start, got, want)
			}
		}
	})
}

func assertNFAClosed(t testing.TB, program nfaProgram) {
	t.Helper()
	if int(program.start) >= len(program.states) || int(program.match) >= len(program.states) {
		t.Fatalf("invalid start/match indexes: %#v", program)
	}
	if program.states[program.match].kind != nfaMatch {
		t.Fatalf("state %d kind = %d, want nfaMatch", program.match, program.states[program.match].kind)
	}
	for index, state := range program.states {
		if state.kind == nfaMatch {
			continue
		}
		if state.out1 == nfaNoState || int(state.out1) >= len(program.states) {
			t.Fatalf("state %d has invalid out1 %d", index, state.out1)
		}
		if state.kind == nfaSplit && state.out2 != nfaNoState && int(state.out2) >= len(program.states) {
			t.Fatalf("split state %d has invalid out2 %d", index, state.out2)
		}
	}
}
