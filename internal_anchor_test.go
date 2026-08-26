package scankit

import "testing"

func TestInternalLiteralAnchorMatchesNFABaseline(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name    string
		pattern string
		data    []byte
	}{
		{name: "separator after unbounded class", pattern: `[A-Z]+@[a-z]+`, data: []byte(`A@x AB@xy C@z`)},
		{name: "separator after unbounded literal", pattern: `a+@[a-z]+`, data: []byte(`a@x aa@xy b@z`)},
		{name: "separator after single-byte alternation", pattern: `(?:a|b)+@[a-z]+`, data: []byte(`a@x ab@xy c@z`)},
		{name: "literal before unbounded class", pattern: `tenant=[A-Z]+@[a-z]+`, data: []byte(`tenant=A@x tenant=AB@xy other=Z@q`)},
		{name: "multi-byte separator", pattern: `[a-z]+://[a-z]+`, data: []byte(`http://api ftp://cdn invalid:/host`)},
		{name: "class contains separator", pattern: `[A-Z@]+@[a-z]+`, data: []byte(`A@@x AB@xy`)},
		{name: "suffix alternation", pattern: `[A-Z]+@(foo|bar)[0-9]+`, data: []byte(`A@foo1 AB@bar22 C@baz3`)},
		{name: "suffix boundary", pattern: `[A-Z]+@[a-z]+\b`, data: []byte(`A@x AB@xy! C@z9`)},
	} {
		t.Run(test.name, func(t *testing.T) {
			scanner, err := Compile([]Expression{{Id: 1, Pattern: test.pattern}})
			if err != nil {
				t.Fatal(err)
			}
			got, err := scanner.Scan(test.data)
			if err != nil {
				t.Fatal(err)
			}
			want := internalAnchorNFABaseline(t, test.pattern, test.data)
			if !equalMatches(got, want) {
				t.Fatalf("Scan() = %#v, want %#v", got, want)
			}
		})
	}
}

func TestCompileSelectsOnlySafeInternalLiteralAnchors(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name    string
		pattern string
		want    bool
	}{
		{name: "unbounded class plus distinct byte", pattern: `[A-Z]+@[a-z]+`, want: true},
		{name: "unbounded literal plus distinct byte", pattern: `a+@[a-z]+`, want: true},
		{name: "unbounded single-byte alternation plus distinct byte", pattern: `(?:a|b)+@[a-z]+`, want: true},
		{name: "literal before unbounded class", pattern: `tenant=[A-Z]+@[a-z]+`, want: true},
		{name: "unbounded multi-byte alternation remains fallback", pattern: `(?:a|bc)+@[a-z]+`, want: false},
		{name: "multi byte literal", pattern: `[a-z]+://[a-z]+`, want: true},
		{name: "anchor in prefix class remains fallback", pattern: `[A-Z@]+@[a-z]+`, want: false},
		{name: "leading literal ends in prefix class remains fallback", pattern: `A[A-Z]+@[a-z]+`, want: false},
		{name: "non-literal gap before anchor remains fallback", pattern: `tenant=[A-Z]+(?:x)?@[a-z]+`, want: false},
		{name: "leading assertion remains fallback", pattern: `\b[A-Z]+@[a-z]+`, want: false},
		{name: "bounded prefix uses existing path", pattern: `[A-Z]{1,64}@[a-z]+`, want: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			root, err := parseRegex(test.pattern)
			if err != nil {
				t.Fatal(err)
			}
			plan, err := compileByteRegexPlan(root, 0)
			if err != nil {
				t.Fatal(err)
			}
			if plan.hasInternalAnchor != test.want {
				t.Fatalf("hasInternalAnchor = %t, want %t", plan.hasInternalAnchor, test.want)
			}
			if !test.want {
				return
			}
			scanner, err := Compile([]Expression{{Id: 1, Pattern: test.pattern}})
			if err != nil {
				t.Fatal(err)
			}
			if len(scanner.unanchoredGroups) != 0 || len(scanner.regexPrograms) != 1 || !scanner.regexPrograms[0].internalAnchor {
				t.Fatalf("internal anchor did not replace always lane: groups=%d programs=%#v", len(scanner.unanchoredGroups), scanner.regexPrograms)
			}
			if len(scanner.blockScanPlan.triggers) != 1 || scanner.blockScanPlan.triggers[0].kind != blockTriggerInternalAnchored {
				t.Fatalf("internal anchor trigger kind = %d, want internal lane", scanner.blockScanPlan.triggers[0].kind)
			}
		})
	}
}

func TestLeadingWordBoundaryFallsBackFromFloatingAnchor(t *testing.T) {
	scanner, err := Compile([]Expression{{Id: 1, Pattern: `\b[A-Za-z0-9._%+-]{1,64}@[A-Za-z0-9-]+(?:\.[A-Za-z0-9-]+)+\b`}})
	if err != nil {
		t.Fatal(err)
	}
	if len(scanner.unanchoredGroups) != 1 || len(scanner.blockScanPlan.triggers) != 0 {
		t.Fatalf("leading word-boundary scanner selected anchored plan: unanchored=%d triggers=%d", len(scanner.unanchoredGroups), len(scanner.blockScanPlan.triggers))
	}
}

func TestInternalLiteralAnchorPreservesEventSemantics(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name        string
		expressions []Expression
		data        []byte
	}{
		{
			name:        "single match",
			expressions: []Expression{{Id: 1, Pattern: `[A-Z]+@[a-z]+`, Flags: CompileSingleMatch}},
			data:        []byte(`A@x AB@xy`),
		},
		{
			name:        "leftmost start and minimum length",
			expressions: []Expression{{Id: 1, Pattern: `[A-Z]+@[a-z]+`, Flags: CompileLeftmostStart, Ext: &ExpressionExt{Flags: ExtMinLength, MinLength: 4}}},
			data:        []byte(`A@x AB@xy C@z`),
		},
		{
			name: "quiet combination",
			expressions: []Expression{
				{Id: 1, Pattern: `[A-Z]+@[a-z]+`, Flags: CompileQuiet},
				{Id: 2, Pattern: `tag`, Flags: CompileQuiet},
				{Id: 3, Pattern: `1&2`, Flags: CompileCombination},
			},
			data: []byte(`A@x tag B@y tag`),
		},
		{
			name: "leading literal with offset constraints",
			expressions: []Expression{{
				Id: 1, Pattern: `tenant=[A-Z]+@[a-z]+`, Flags: CompileSingleMatch,
				Ext: &ExpressionExt{Flags: ExtMinOffset | ExtMaxOffset | ExtMinLength, MinOffset: 10, MaxOffset: 20, MinLength: 10},
			}},
			data: []byte(`tenant=A@x tenant=AB@xy tenant=C@z`),
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			optimized, err := Compile(test.expressions)
			if err != nil {
				t.Fatal(err)
			}
			fallbackExpressions := append([]Expression(nil), test.expressions...)
			for index := range fallbackExpressions {
				if fallbackExpressions[index].Id == 1 {
					fallbackExpressions[index].Pattern = `(?:)` + fallbackExpressions[index].Pattern
				}
			}
			fallback, err := Compile(fallbackExpressions)
			if err != nil {
				t.Fatal(err)
			}
			got, err := optimized.Scan(test.data)
			if err != nil {
				t.Fatal(err)
			}
			want, err := fallback.Scan(test.data)
			if err != nil {
				t.Fatal(err)
			}
			if !equalMatches(got, want) {
				t.Fatalf("optimized Scan() = %#v, fallback Scan() = %#v", got, want)
			}
		})
	}
}

func FuzzInternalLiteralAnchorMatchesNFABaseline(f *testing.F) {
	for _, seed := range [][]byte{
		[]byte(`A@x AB@xy C@z`),
		[]byte(`http://api ftp://cdn invalid:/host`),
		[]byte{},
		{0xff, 'A', '@', 'x'},
	} {
		f.Add(seed)
	}
	patterns := []string{
		`[A-Z]+@[a-z]+`,
		`a+@[a-z]+`,
		`(?:a|b)+@[a-z]+`,
		`tenant=[A-Z]+@[a-z]+`,
		`(?:a|bc)+@[a-z]+`,
		`[a-z]+://[a-z]+`,
		`[A-Z@]+@[a-z]+`,
		`[A-Z]+@(foo|bar)[0-9]+`,
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 512 {
			t.Skip()
		}
		for _, pattern := range patterns {
			scanner, err := Compile([]Expression{{Id: 1, Pattern: pattern}})
			if err != nil {
				t.Fatal(err)
			}
			got, err := scanner.Scan(data)
			if err != nil {
				t.Fatal(err)
			}
			want := internalAnchorNFABaseline(t, pattern, data)
			if !equalMatches(got, want) {
				t.Fatalf("Scan(%q, %q) = %#v, want %#v", pattern, data, got, want)
			}
		}
	})
}

func FuzzLeadingWordBoundaryFloatingAnchorMatchesNFABaseline(f *testing.F) {
	const pattern = `\b[A-Za-z0-9._%+-]{1,64}@[A-Za-z0-9-]+(?:\.[A-Za-z0-9-]+)+\b`
	scanner, err := Compile([]Expression{{Id: 1, Pattern: pattern}})
	if err != nil {
		f.Fatal(err)
	}
	for _, seed := range [][]byte{
		[]byte(`email=alice.smith42@example.cn other=12345678@qq.com`),
		[]byte(`prefix_alice.smith42@example.cn invalid=user@example..invalid`),
		[]byte(`email=a@example.com email=b@example.net`),
		[]byte(`00000000000000000000@0000000.0.0`),
		{},
		{0xff, 'a', '@', 'b', '.', 'c', 'n'},
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 1_024 {
			t.Skip()
		}
		got, err := scanner.ScanInto(data, nil)
		if err != nil {
			t.Fatal(err)
		}
		want := internalAnchorNFABaseline(t, pattern, data)
		if !equalMatches(got, want) {
			t.Fatalf("ScanInto(%q) = %#v, NFA baseline = %#v", data, got, want)
		}
	})
}

func internalAnchorNFABaseline(t testing.TB, pattern string, data []byte) []Match {
	t.Helper()
	root, err := parseRegex(pattern)
	if err != nil {
		t.Fatal(err)
	}
	program, err := compileNFA(root)
	if err != nil {
		t.Fatal(err)
	}
	starts := make([]int, len(data)+1)
	for index := range starts {
		starts[index] = -1
	}
	for start := range data {
		for _, end := range nfaMatchFrom(program, data, start) {
			if starts[end] == -1 || start < starts[end] {
				starts[end] = start
			}
		}
	}
	matches := make([]Match, 0)
	for end, start := range starts {
		if start >= 0 {
			matches = append(matches, Match{Id: 1, From: uint64(start), To: uint64(end)})
		}
	}
	return matches
}
