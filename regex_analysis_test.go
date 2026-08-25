package scankit

import (
	"errors"
	"testing"
)

func TestAnalyzeRegexSelectsFixedMandatoryAnchors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		pattern    string
		wantAnchor regexAnchor
		wantOK     bool
	}{
		{name: "literal prefix", pattern: `token\d+`, wantAnchor: regexAnchor{literal: "token"}, wantOK: true},
		{name: "anchor after fixed class", pattern: `[A-Z]{2}-token-[0-9]{2}`, wantAnchor: regexAnchor{literal: "-token-", minOffset: 2, maxOffset: 2}, wantOK: true},
		{name: "common alternate literal", pattern: `(?:foo|foo)-\d+`, wantAnchor: regexAnchor{literal: "foo-"}, wantOK: true},
		{name: "suffix after fixed width alternate", pattern: `(?:foo|bar)-x`, wantAnchor: regexAnchor{literal: "-x", minOffset: 3, maxOffset: 3}, wantOK: true},
		{name: "bounded variable prefix", pattern: `[a-z]{1,64}@example\.com`, wantAnchor: regexAnchor{literal: "@example.com", minOffset: 1, maxOffset: 64}, wantOK: true},
		{name: "no mandatory literal", pattern: `\d+`, wantOK: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root, err := parseRegex(tt.pattern)
			if err != nil {
				t.Fatal(err)
			}
			analysis, err := analyzeRegex(root)
			if err != nil {
				t.Fatal(err)
			}
			got, ok := selectRegexAnchor(analysis)
			if ok != tt.wantOK {
				t.Fatalf("selectRegexAnchor(%q) ok = %v, want %v", tt.pattern, ok, tt.wantOK)
			}
			if ok && got != tt.wantAnchor {
				t.Fatalf("selectRegexAnchor(%q) = %#v, want %#v", tt.pattern, got, tt.wantAnchor)
			}
		})
	}
}

func TestAnalyzeRegexRejectsFiniteWidthsBeyondCompilerBudget(t *testing.T) {
	t.Parallel()
	root, err := parseRegex(`(?:a{4096}){4096}`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := analyzeRegex(root); !errors.Is(err, ErrRegexTooComplex) {
		t.Fatalf("analyzeRegex() error = %v, want ErrRegexTooComplex", err)
	}
}

func TestCompileByteRegexPlanPreservesBoundedAndUnboundedLengths(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name        string
		pattern     string
		flags       CompileFlag
		wantMinimum int
		wantMaximum int
		wantAnchor  bool
	}{
		{name: "bounded anchor", pattern: `id-[0-9]{2,4}`, wantMinimum: 5, wantMaximum: 7, wantAnchor: true},
		{name: "unbounded repeat", pattern: `\d+`, wantMinimum: 1, wantMaximum: unboundedRepeat},
		{name: "multiline anchor", pattern: `^token$`, flags: CompileMultiline, wantMinimum: 5, wantMaximum: 5, wantAnchor: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			root, err := parseRegex(test.pattern)
			if err != nil {
				t.Fatal(err)
			}
			applyRegexFlags(root, test.flags)
			plan, err := compileByteRegexPlan(root, test.flags)
			if err != nil {
				t.Fatal(err)
			}
			if plan.analysis.min != test.wantMinimum || plan.analysis.max != test.wantMaximum {
				t.Fatalf("length = {%d,%d}, want {%d,%d}", plan.analysis.min, plan.analysis.max, test.wantMinimum, test.wantMaximum)
			}
			if plan.hasBoundedAnchor != test.wantAnchor {
				t.Fatalf("hasBoundedAnchor = %t, want %t", plan.hasBoundedAnchor, test.wantAnchor)
			}
			if plan.program.multiline != (test.flags&CompileMultiline != 0) {
				t.Fatalf("program.multiline = %t", plan.program.multiline)
			}
		})
	}
}

func TestRegexMatchLimitBoundsUnboundedProgramsByRemainingInput(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		maximum   int
		remaining int
		want      int
	}{
		{maximum: unboundedRepeat, remaining: 17, want: 17},
		{maximum: 8, remaining: 17, want: 8},
		{maximum: 32, remaining: 17, want: 17},
	} {
		if got := regexMatchLimit(test.maximum, test.remaining); got != test.want {
			t.Fatalf("regexMatchLimit(%d, %d) = %d, want %d", test.maximum, test.remaining, got, test.want)
		}
	}
}

func FuzzAnalyzeRegexResourceBounds(f *testing.F) {
	for _, pattern := range []string{`(?:a{4096}){4096}`, `(?:ab){2,128}`, `(?:foo|bar)+`, `\d+`, `\AID:[0-9]{2}\z`} {
		f.Add(pattern)
	}
	f.Fuzz(func(t *testing.T, pattern string) {
		if len(pattern) > 4_096 {
			t.Skip()
		}
		root, err := parseRegex(pattern)
		if err != nil {
			return
		}
		analysis, err := analyzeRegex(root)
		if err != nil {
			if !errors.Is(err, ErrRegexTooComplex) && !errors.Is(err, ErrUnsupportedExpression) {
				t.Fatalf("analyzeRegex(%q) error = %v", pattern, err)
			}
			return
		}
		if analysis.min < 0 || analysis.max != unboundedRepeat && (analysis.max < analysis.min || analysis.max > maxRegexFiniteWidth) {
			t.Fatalf("invalid analysis %#v for %q", analysis, pattern)
		}
		for _, anchor := range analysis.anchors {
			if anchor.minOffset < 0 || anchor.maxOffset != unboundedRepeat && anchor.maxOffset < anchor.minOffset {
				t.Fatalf("invalid anchor %#v for %q", anchor, pattern)
			}
		}
	})
}

func TestLeadingBoundedPrefixClassRecognizesEmailShape(t *testing.T) {
	t.Parallel()

	root, err := parseRegex(`[A-Za-z0-9._%+-]{1,64}@[A-Za-z0-9-]{1,63}\.com`)
	if err != nil {
		t.Fatal(err)
	}
	analysis, err := analyzeRegex(root)
	if err != nil {
		t.Fatal(err)
	}
	var prefix byteClass
	ok := false
	for _, anchor := range analysis.anchors {
		prefix, ok = leadingBoundedPrefixClass(root, anchor)
		if ok {
			break
		}
	}
	if !ok || !prefix.contains('a') || !prefix.contains('.') || prefix.contains('@') {
		t.Fatalf("leadingBoundedPrefixClass() = %#v, %v", prefix, ok)
	}
}

func TestLeadingAnchorSuffixClassRecognizesPositiveClassRepeat(t *testing.T) {
	root, err := parseRegex(`account=[AB]{4}[0-9]{4}`)
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
	suffix, ok := leadingAnchorSuffixClass(root, anchor)
	if !ok || !suffix.contains('A') || !suffix.contains('B') || suffix.contains('C') {
		t.Fatalf("leadingAnchorSuffixClass() = %#v, %t; want [AB]", suffix, ok)
	}
}
