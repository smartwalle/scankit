package scankit

import (
	"bytes"
	"testing"
)

func TestMostSelectiveByteClass(t *testing.T) {
	var one byteClass
	one.add('a')
	three := classRange('0', '2')

	tests := []struct {
		name    string
		classes []byteClass
		want    int
	}{
		{
			name:    "first class wins equal cardinality",
			classes: []byteClass{one, one},
			want:    0,
		},
		{
			name:    "selects empty class",
			classes: []byteClass{allBytes(), byteClass{}, one},
			want:    1,
		},
		{
			name:    "selects smallest non-empty class",
			classes: []byteClass{allBytes(), three, one},
			want:    2,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := mostSelectiveByteClass(test.classes); got != test.want {
				t.Fatalf("mostSelectiveByteClass() = %d, want %d", got, test.want)
			}
		})
	}
}

func TestExtractFixedByteRegexAnchorWithoutExpansion(t *testing.T) {
	root, err := parseRegex(`(?:ab|cd|ef|gh){4}`)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := extractFixedByteRegex(root); ok {
		t.Fatal("branch expansion unexpectedly fit fixed executor limit")
	}
	anchor, ok := extractFixedByteRegexAnchor(root)
	if !ok {
		t.Fatal("fixed-width alternation did not produce a class anchor")
	}
	if anchor.width != 8 || anchor.offset != 0 || !anchor.class.contains('a') || !anchor.class.contains('c') || anchor.class.contains('z') {
		t.Fatalf("anchor = %#v, want width 8 and first-byte branch union", anchor)
	}
}

func TestExtractFixedByteRegexAnchorSelectsAdditionalNecessaryChecks(t *testing.T) {
	root, err := parseRegex(`[1-9][0-9]{5}(18|19|20)[0-9]{2}(0[1-9]|1[0-2])(0[1-9]|[12][0-9]|3[01])[0-9]{3}[0-9Xx]`)
	if err != nil {
		t.Fatal(err)
	}
	anchor, ok := extractFixedByteRegexAnchor(root)
	if !ok || anchor.checks == nil || anchor.checks.count == 0 {
		t.Fatalf("anchor = %#v, want additional necessary checks", anchor)
	}
	if anchor.checks.count > maxFixedByteRegexChecks {
		t.Fatalf("additional check count = %d, limit = %d", anchor.checks.count, maxFixedByteRegexChecks)
	}
	for index := 0; index < int(anchor.checks.count); index++ {
		check := anchor.checks.values[index]
		if check.offset == anchor.offset || byteClassSize(check.class) > maxFixedByteRegexCheckSize {
			t.Fatalf("check %d = %#v is not a selective non-trigger position", index, check)
		}
	}
}

func TestExtractFixedByteRegexAnchorRejectsNonFixedWidthStructure(t *testing.T) {
	for _, pattern := range []string{
		`(?:ab|cde){4}`,
		`(?:ab|cd){1,4}`,
		`(?:ab|cd)+`,
		`\b(?:ab|cd){4}`,
		`(?:ab|cd){4}(?:ef)?`,
	} {
		t.Run(pattern, func(t *testing.T) {
			root, err := parseRegex(pattern)
			if err != nil {
				t.Fatal(err)
			}
			if _, ok := extractFixedByteRegexAnchor(root); ok {
				t.Fatalf("extractFixedByteRegexAnchor(%q) unexpectedly succeeded", pattern)
			}
		})
	}
}

func BenchmarkFixedByteRegexLargeAlternation(b *testing.B) {
	const pattern = `(?:ab|cd|ef|gh){4}`
	data := bytes.Repeat([]byte("xxabcdefghyy"), 256)
	optimized, err := Compile([]Expression{{Id: 1, Pattern: pattern}})
	if err != nil {
		b.Fatal(err)
	}
	fallback, err := Compile([]Expression{{Id: 1, Pattern: `(?:)` + pattern}})
	if err != nil {
		b.Fatal(err)
	}
	want, err := fallback.Scan(data)
	if err != nil {
		b.Fatal(err)
	}
	for _, test := range []struct {
		name    string
		scanner *Scanner
	}{
		{name: "FixedAnchor", scanner: optimized},
		{name: "AlwaysNFA", scanner: fallback},
	} {
		b.Run(test.name, func(b *testing.B) {
			matches := make([]Match, 0, len(want))
			got, err := test.scanner.ScanInto(data, matches)
			if err != nil {
				b.Fatal(err)
			}
			if !equalMatches(got, want) {
				b.Fatalf("ScanInto() = %#v, want %#v", got, want)
			}
			b.SetBytes(int64(len(data)))
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				matches = matches[:0]
				matches, err = test.scanner.ScanInto(data, matches)
				if err != nil {
					b.Fatal(err)
				}
			}
			if len(matches) != len(want) {
				b.Fatalf("match count = %d, want %d", len(matches), len(want))
			}
		})
	}
}
