package scankit_test

import (
	"errors"
	"testing"
	"unicode/utf8"

	"github.com/smartwalle/scankit"
)

// 未实现的结构必须在编译期失败。表中包含反向引用、环视等本扫描器不支持的结构，确保它们
// 不会被误解释为字面量。
func TestUnsupportedRegexSyntaxIsNeverReinterpretedAsLiteral(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name    string
		pattern string
	}{
		{name: "backreference", pattern: `token-(a)\1`},
		{name: "single byte directive", pattern: `token\C`},
		{name: "start reset", pattern: `token\K`},
		{name: "unicode property without UCP", pattern: `\p{L}+`},
		{name: "named character", pattern: `\N{LATIN CAPITAL LETTER A}`},
		{name: "lookahead", pattern: `token(?=:)`},
		{name: "atomic group", pattern: `(?>token)`},
		{name: "negative lookahead", pattern: `token(?!:)`},
		{name: "positive lookbehind", pattern: `(?<=id-)token`},
		{name: "negative lookbehind", pattern: `(?<!id-)token`},
		{name: "possessive quantifier", pattern: `token++`},
		{name: "named backreference", pattern: `(?<token>token)\k<token>`},
		{name: "python named backreference", pattern: `(?P<token>token)(?P=token)`},
		{name: "subroutine", pattern: `(?1)`},
		{name: "recursion", pattern: `(?R)`},
		{name: "conditional", pattern: `(?(1)token|other)`},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := scankit.Compile([]scankit.Expression{{Id: 1, Pattern: test.pattern}})
			if !errors.Is(err, scankit.ErrUnsupportedExpression) {
				t.Fatalf("Compile(%q) error = %v, want ErrUnsupportedExpression", test.pattern, err)
			}
		})
	}
}

func FuzzUnsupportedRegexSyntaxStaysCategorized(f *testing.F) {
	patterns := []string{
		`token\C`, `token\K`, `token\X`, `\N{LATIN CAPITAL LETTER A}`,
		`(a)\1`, `(?<token>token)\k<token>`, `(?P<token>token)(?P=token)`, `token(?=:)`, `token(?!:)`,
		`(?<=id-)token`, `(?<!id-)token`, `(?>token)`, `token++`,
		`(?1)`, `(?R)`, `(?(1)token|other)`,
	}
	for index := range patterns {
		f.Add(byte(index))
	}
	f.Fuzz(func(t *testing.T, selector byte) {
		pattern := patterns[int(selector)%len(patterns)]
		_, err := scankit.Compile([]scankit.Expression{{Id: 1, Pattern: pattern}})
		if !errors.Is(err, scankit.ErrUnsupportedExpression) {
			t.Fatalf("Compile(%q) error = %v, want ErrUnsupportedExpression", pattern, err)
		}
	})
}

func TestLineBreakEscapeTreatsCRLFAsOneMatch(t *testing.T) {
	t.Parallel()
	database, err := scankit.Compile([]scankit.Expression{{Id: 1, Pattern: `\R`}})
	if err != nil {
		t.Fatal(err)
	}
	got, err := database.Scan([]byte("a\r\nb\rc\nd\ve\f"))
	if err != nil {
		t.Fatal(err)
	}
	assertMatchesEqual(t, got, []scankit.Match{
		{Id: 1, From: 1, To: 3},
		{Id: 1, From: 4, To: 5},
		{Id: 1, From: 6, To: 7},
		{Id: 1, From: 8, To: 9},
		{Id: 1, From: 10, To: 11},
	})
}

func TestControlEscapesMatchByteAndUnicodeInput(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		flags   scankit.CompileFlag
		pattern string
		data    []byte
		want    []scankit.Match
	}{
		{
			name:    "byte",
			pattern: `\a\e`,
			data:    []byte{'x', '\a', 0x1b, 'y'},
			want:    []scankit.Match{{Id: 1, From: 1, To: 3}},
		},
		{
			name:    "unicode",
			flags:   scankit.CompileUTF8 | scankit.CompileUnicodeProperties,
			pattern: `\a\e`,
			data:    append([]byte("张"), '\a', 0x1b, 'A'),
			want:    []scankit.Match{{Id: 1, From: 3, To: 5}},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			scanner, err := scankit.Compile([]scankit.Expression{{Id: 1, Pattern: test.pattern, Flags: test.flags}})
			if err != nil {
				t.Fatal(err)
			}
			got, err := scanner.Scan(test.data)
			if err != nil {
				t.Fatal(err)
			}
			assertMatchesEqual(t, got, test.want)
		})
	}
}

func TestControlLetterEscapesMatchByteAndUnicodeInput(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		flags   scankit.CompileFlag
		pattern string
		data    []byte
		want    []scankit.Match
	}{
		{
			name:    "byte class",
			pattern: `[\cA-\cC]+`,
			data:    []byte{0, 1, 2, 3, 4},
			want: []scankit.Match{
				{Id: 1, From: 1, To: 2},
				{Id: 1, From: 1, To: 3},
				{Id: 1, From: 2, To: 3},
				{Id: 1, From: 1, To: 4},
				{Id: 1, From: 2, To: 4},
				{Id: 1, From: 3, To: 4},
			},
		},
		{
			name:    "unicode",
			flags:   scankit.CompileUTF8 | scankit.CompileUnicodeProperties,
			pattern: `\cA\cz`,
			data:    append([]byte("张"), 1, 26, 'A'),
			want:    []scankit.Match{{Id: 1, From: 3, To: 5}},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			scanner, err := scankit.Compile([]scankit.Expression{{Id: 1, Pattern: test.pattern, Flags: test.flags}})
			if err != nil {
				t.Fatal(err)
			}
			got, err := scanner.Scan(test.data)
			if err != nil {
				t.Fatal(err)
			}
			assertMatchesEqual(t, got, test.want)
		})
	}
}

func TestControlLetterEscapesRejectInvalidForms(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name  string
		flags scankit.CompileFlag
	}{
		{name: "byte missing"},
		{name: "byte non-letter"},
		{name: "unicode missing", flags: scankit.CompileUTF8 | scankit.CompileUnicodeProperties},
		{name: "unicode non-letter", flags: scankit.CompileUTF8 | scankit.CompileUnicodeProperties},
	} {
		t.Run(test.name, func(t *testing.T) {
			pattern := `\c`
			if test.name == "byte non-letter" || test.name == "unicode non-letter" {
				pattern = `\c1`
			}
			_, err := scankit.Compile([]scankit.Expression{{Id: 1, Pattern: pattern, Flags: test.flags}})
			if !errors.Is(err, scankit.ErrUnsupportedExpression) {
				t.Fatalf("Compile(%q) error = %v, want ErrUnsupportedExpression", pattern, err)
			}
		})
	}
}

func TestOctalEscapesMatchByteAndUnicodeInput(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		flags   scankit.CompileFlag
		pattern string
		data    []byte
		want    []scankit.Match
	}{
		{
			name:    "byte",
			pattern: `\0\012\0377`,
			data:    []byte{'x', 0, '\n', 0xff, 'y'},
			want:    []scankit.Match{{Id: 1, From: 1, To: 4}},
		},
		{
			name:    "unicode and maximum digit count",
			flags:   scankit.CompileUTF8 | scankit.CompileUnicodeProperties,
			pattern: `\0123`,
			data:    []byte("张S"),
			want:    []scankit.Match{{Id: 1, From: 3, To: 4}},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			scanner, err := scankit.Compile([]scankit.Expression{{Id: 1, Pattern: test.pattern, Flags: test.flags}})
			if err != nil {
				t.Fatal(err)
			}
			got, err := scanner.Scan(test.data)
			if err != nil {
				t.Fatal(err)
			}
			assertMatchesEqual(t, got, test.want)
		})
	}
}

func TestBracedHexEscapesMatchByteAndUnicodeInput(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		flags   scankit.CompileFlag
		pattern string
		data    []byte
		want    []scankit.Match
	}{
		{
			name:    "byte boundaries",
			pattern: `\x{00}\x{80}\x{FF}`,
			data:    []byte{'x', 0, 0x80, 0xff, 'y'},
			want:    []scankit.Match{{Id: 1, From: 1, To: 4}},
		},
		{
			name:    "byte class",
			pattern: `[\x{80}-\x{82}]+`,
			data:    []byte{0x7f, 0x80, 0x81, 0x82, 0x83},
			want: []scankit.Match{
				{Id: 1, From: 1, To: 2}, {Id: 1, From: 1, To: 3}, {Id: 1, From: 2, To: 3},
				{Id: 1, From: 1, To: 4}, {Id: 1, From: 2, To: 4}, {Id: 1, From: 3, To: 4},
			},
		},
		{
			name:    "unicode scalar boundaries",
			flags:   scankit.CompileUTF8 | scankit.CompileUnicodeProperties,
			pattern: `\x{4E2D}\x{1F600}`,
			data:    []byte("A中😀B"),
			want:    []scankit.Match{{Id: 1, From: 1, To: 8}},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			scanner, err := scankit.Compile([]scankit.Expression{{Id: 1, Pattern: test.pattern, Flags: test.flags}})
			if err != nil {
				t.Fatal(err)
			}
			got, err := scanner.Scan(test.data)
			if err != nil {
				t.Fatal(err)
			}
			assertMatchesEqual(t, got, test.want)
		})
	}
}

func TestBracedHexEscapesRejectInvalidForms(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name    string
		flags   scankit.CompileFlag
		pattern string
	}{
		{name: "byte empty", pattern: `\x{}`},
		{name: "byte invalid digit", pattern: `\x{GG}`},
		{name: "byte unterminated", pattern: `\x{FF`},
		{name: "byte out of range", pattern: `\x{100}`},
		{name: "unicode empty", flags: scankit.CompileUTF8 | scankit.CompileUnicodeProperties, pattern: `\x{}`},
		{name: "unicode invalid digit", flags: scankit.CompileUTF8 | scankit.CompileUnicodeProperties, pattern: `\x{XYZ}`},
		{name: "unicode unterminated", flags: scankit.CompileUTF8 | scankit.CompileUnicodeProperties, pattern: `\x{10FFFF`},
		{name: "unicode out of range", flags: scankit.CompileUTF8 | scankit.CompileUnicodeProperties, pattern: `\x{110000}`},
		{name: "unicode surrogate", flags: scankit.CompileUTF8 | scankit.CompileUnicodeProperties, pattern: `\x{D800}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := scankit.Compile([]scankit.Expression{{Id: 1, Pattern: test.pattern, Flags: test.flags}}); err == nil {
				t.Fatalf("Compile(%q) error = nil", test.pattern)
			}
		})
	}
}

func TestBracedOctalEscapesMatchByteAndUnicodeInput(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		flags   scankit.CompileFlag
		pattern string
		data    []byte
		want    []scankit.Match
	}{
		{
			name:    "byte boundaries",
			pattern: `\o{0}\o{200}\o{377}`,
			data:    []byte{'x', 0, 0x80, 0xff, 'y'},
			want:    []scankit.Match{{Id: 1, From: 1, To: 4}},
		},
		{
			name:    "byte class",
			pattern: `[\o{200}-\o{202}]+`,
			data:    []byte{0x7f, 0x80, 0x81, 0x82, 0x83},
			want: []scankit.Match{
				{Id: 1, From: 1, To: 2}, {Id: 1, From: 1, To: 3}, {Id: 1, From: 2, To: 3},
				{Id: 1, From: 1, To: 4}, {Id: 1, From: 2, To: 4}, {Id: 1, From: 3, To: 4},
			},
		},
		{
			name:    "unicode scalar",
			flags:   scankit.CompileUTF8 | scankit.CompileUnicodeProperties,
			pattern: `\o{47055}\o{101}`,
			data:    []byte("A中AB"),
			want:    []scankit.Match{{Id: 1, From: 1, To: 5}},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			scanner, err := scankit.Compile([]scankit.Expression{{Id: 1, Pattern: test.pattern, Flags: test.flags}})
			if err != nil {
				t.Fatal(err)
			}
			got, err := scanner.Scan(test.data)
			if err != nil {
				t.Fatal(err)
			}
			assertMatchesEqual(t, got, test.want)
		})
	}
}

func TestBracedOctalEscapesRejectInvalidForms(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name    string
		flags   scankit.CompileFlag
		pattern string
	}{
		{name: "byte missing braces", pattern: `\o`},
		{name: "byte empty", pattern: `\o{}`},
		{name: "byte invalid digit", pattern: `\o{8}`},
		{name: "byte unterminated", pattern: `\o{377`},
		{name: "byte out of range", pattern: `\o{400}`},
		{name: "unicode missing braces", flags: scankit.CompileUTF8 | scankit.CompileUnicodeProperties, pattern: `\o`},
		{name: "unicode invalid digit", flags: scankit.CompileUTF8 | scankit.CompileUnicodeProperties, pattern: `\o{8}`},
		{name: "unicode out of range", flags: scankit.CompileUTF8 | scankit.CompileUnicodeProperties, pattern: `\o{4200000}`},
		{name: "unicode surrogate", flags: scankit.CompileUTF8 | scankit.CompileUnicodeProperties, pattern: `\o{154000}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := scankit.Compile([]scankit.Expression{{Id: 1, Pattern: test.pattern, Flags: test.flags}}); err == nil {
				t.Fatalf("Compile(%q) error = nil", test.pattern)
			}
		})
	}
}

func TestNegatedUnicodePropertyCaretForm(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		pattern string
		want    []scankit.Match
	}{
		{
			name:    "p caret is negated",
			pattern: `\p{^L}`,
			want:    []scankit.Match{{Id: 1, From: 4, To: 5}, {Id: 1, From: 5, To: 6}},
		},
		{
			name:    "P caret is positive",
			pattern: `\P{^L}+`,
			want: []scankit.Match{
				{Id: 1, From: 0, To: 1}, {Id: 1, From: 1, To: 4}, {Id: 1, From: 0, To: 4},
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			scanner, err := scankit.Compile([]scankit.Expression{{Id: 1, Pattern: test.pattern, Flags: scankit.CompileUTF8 | scankit.CompileUnicodeProperties}})
			if err != nil {
				t.Fatal(err)
			}
			got, err := scanner.Scan([]byte("A中1!"))
			if err != nil {
				t.Fatal(err)
			}
			assertMatchesEqual(t, got, test.want)
		})
	}
}

func TestNegatedUnicodePropertyCaretFormRejectsInvalidNames(t *testing.T) {
	t.Parallel()
	for _, pattern := range []string{`\p{^}`, `\P{^}`, `\p{^Unknown}`} {
		if _, err := scankit.Compile([]scankit.Expression{{Id: 1, Pattern: pattern, Flags: scankit.CompileUTF8 | scankit.CompileUnicodeProperties}}); err == nil {
			t.Fatalf("Compile(%q) error = nil", pattern)
		}
	}
}

func TestUnicodePropertyQualifiedAndLongNames(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		pattern string
		data    string
		want    []scankit.Match
	}{
		{
			name:    "general category long name",
			pattern: `\p{gc=Uppercase_Letter}+`,
			data:    "aAB张",
			want:    []scankit.Match{{Id: 1, From: 1, To: 2}, {Id: 1, From: 1, To: 3}, {Id: 1, From: 2, To: 3}},
		},
		{
			name:    "general category canonical separator",
			pattern: `\p{General_Category=decimal-number}+`,
			data:    "A1２!",
			want:    []scankit.Match{{Id: 1, From: 1, To: 2}, {Id: 1, From: 1, To: 5}, {Id: 1, From: 2, To: 5}},
		},
		{
			name:    "script qualified",
			pattern: `\p{sc=Han}+`,
			data:    "A中文!",
			want:    []scankit.Match{{Id: 1, From: 1, To: 4}, {Id: 1, From: 1, To: 7}, {Id: 1, From: 4, To: 7}},
		},
		{
			name:    "is script alias",
			pattern: `\p{IsGreek}+`,
			data:    "Aαβ!",
			want:    []scankit.Match{{Id: 1, From: 1, To: 3}, {Id: 1, From: 1, To: 5}, {Id: 1, From: 3, To: 5}},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			scanner, err := scankit.Compile([]scankit.Expression{{Id: 1, Pattern: test.pattern, Flags: scankit.CompileUTF8 | scankit.CompileUnicodeProperties}})
			if err != nil {
				t.Fatal(err)
			}
			got, err := scanner.Scan([]byte(test.data))
			if err != nil {
				t.Fatal(err)
			}
			assertMatchesEqual(t, got, test.want)
		})
	}
}

func TestUnicodePropertyQualifiedNamesRejectInvalidForms(t *testing.T) {
	t.Parallel()
	for _, pattern := range []string{
		`\p{gc=Unknown}`, `\p{sc=Unknown}`, `\p{General_Category=}`, `\p{Script=}`, `\p{IsUnknown}`,
	} {
		if _, err := scankit.Compile([]scankit.Expression{{Id: 1, Pattern: pattern, Flags: scankit.CompileUTF8 | scankit.CompileUnicodeProperties}}); err == nil {
			t.Fatalf("Compile(%q) error = nil", pattern)
		}
	}
}

func TestUnicodePropertyDerivedNames(t *testing.T) {
	t.Parallel()
	data := string([]rune{'A', '\n', 0x0378, '中', '!'})
	tests := []struct {
		name    string
		pattern string
		want    []scankit.Match
	}{
		{
			name:    "any includes newline and unassigned",
			pattern: `\p{Any}`,
			want: []scankit.Match{
				{Id: 1, From: 0, To: 1}, {Id: 1, From: 1, To: 2}, {Id: 1, From: 2, To: 4},
				{Id: 1, From: 4, To: 7}, {Id: 1, From: 7, To: 8},
			},
		},
		{
			name:    "not any has empty language",
			pattern: `\P{Any}`,
			want:    nil,
		},
		{
			name:    "assigned excludes Cn",
			pattern: `\p{Assigned}`,
			want: []scankit.Match{
				{Id: 1, From: 0, To: 1}, {Id: 1, From: 1, To: 2}, {Id: 1, From: 4, To: 7}, {Id: 1, From: 7, To: 8},
			},
		},
		{
			name:    "ascii",
			pattern: `\p{ASCII}`,
			want:    []scankit.Match{{Id: 1, From: 0, To: 1}, {Id: 1, From: 1, To: 2}, {Id: 1, From: 7, To: 8}},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			scanner, err := scankit.Compile([]scankit.Expression{{Id: 1, Pattern: test.pattern, Flags: scankit.CompileUTF8 | scankit.CompileUnicodeProperties}})
			if err != nil {
				t.Fatal(err)
			}
			got, err := scanner.Scan([]byte(data))
			if err != nil {
				t.Fatal(err)
			}
			assertMatchesEqual(t, got, test.want)
		})
	}
}

func TestUnicodePropertyDerivedNamesRejectInvalidForms(t *testing.T) {
	t.Parallel()
	for _, pattern := range []string{`\p{AnyThing}`, `\p{Assignedness}`, `\p{ASCIII}`} {
		if _, err := scankit.Compile([]scankit.Expression{{Id: 1, Pattern: pattern, Flags: scankit.CompileUTF8 | scankit.CompileUnicodeProperties}}); err == nil {
			t.Fatalf("Compile(%q) error = nil", pattern)
		}
	}
}

func FuzzUnicodePropertyDerivedNamesScanIntoMatchesScan(f *testing.F) {
	f.Add([]byte("A\n中!"))
	f.Add([]byte(string([]rune{0x0378, 'A'})))
	f.Add([]byte{0xff, 'A'})
	scanners := make([]*scankit.Scanner, 0, 3)
	for _, pattern := range []string{`\p{Any}`, `\p{Assigned}`, `\P{ASCII}`} {
		scanner, err := scankit.Compile([]scankit.Expression{{Id: 1, Pattern: pattern, Flags: scankit.CompileUTF8 | scankit.CompileUnicodeProperties}})
		if err != nil {
			f.Fatal(err)
		}
		scanners = append(scanners, scanner)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 1024 {
			t.Skip()
		}
		for _, scanner := range scanners {
			got, err := scanner.Scan(data)
			into, intoErr := scanner.ScanInto(data, make([]scankit.Match, 0, len(got)))
			if !utf8.Valid(data) {
				if !errors.Is(err, scankit.ErrInvalidUTF8) || !errors.Is(intoErr, scankit.ErrInvalidUTF8) {
					t.Fatalf("invalid UTF-8 errors = %v, %v", err, intoErr)
				}
				continue
			}
			if err != nil || intoErr != nil {
				t.Fatalf("Scan errors = %v, %v", err, intoErr)
			}
			assertMatchesEqual(t, into, got)
		}
	})
}

func FuzzUnicodePropertyQualifiedNamesScanIntoMatchesScan(f *testing.F) {
	f.Add([]byte("A中文１２αβ"))
	f.Add([]byte("\n\t张"))
	f.Add([]byte{0xff, 'A'})
	scanners := make([]*scankit.Scanner, 0, 3)
	for _, pattern := range []string{`\p{gc=Uppercase_Letter}+`, `\p{sc=Han}+`, `\p{IsGreek}+`} {
		scanner, err := scankit.Compile([]scankit.Expression{{Id: 1, Pattern: pattern, Flags: scankit.CompileUTF8 | scankit.CompileUnicodeProperties}})
		if err != nil {
			f.Fatal(err)
		}
		scanners = append(scanners, scanner)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 1024 {
			t.Skip()
		}
		for _, scanner := range scanners {
			got, err := scanner.Scan(data)
			into, intoErr := scanner.ScanInto(data, make([]scankit.Match, 0, len(got)))
			if !utf8.Valid(data) {
				if !errors.Is(err, scankit.ErrInvalidUTF8) || !errors.Is(intoErr, scankit.ErrInvalidUTF8) {
					t.Fatalf("invalid UTF-8 errors = %v, %v", err, intoErr)
				}
				continue
			}
			if err != nil || intoErr != nil {
				t.Fatalf("Scan errors = %v, %v", err, intoErr)
			}
			assertMatchesEqual(t, into, got)
		}
	})
}

func FuzzNegatedUnicodePropertyCaretFormScanIntoMatchesScan(f *testing.F) {
	f.Add([]byte("A中1!"))
	f.Add([]byte("\n\t张"))
	f.Add([]byte{0xff, 'A'})
	scanner, err := scankit.Compile([]scankit.Expression{{Id: 1, Pattern: `\p{^L}+`, Flags: scankit.CompileUTF8 | scankit.CompileUnicodeProperties}})
	if err != nil {
		f.Fatal(err)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 1024 {
			t.Skip()
		}
		got, err := scanner.Scan(data)
		into, intoErr := scanner.ScanInto(data, make([]scankit.Match, 0, len(got)))
		if !utf8.Valid(data) {
			if !errors.Is(err, scankit.ErrInvalidUTF8) || !errors.Is(intoErr, scankit.ErrInvalidUTF8) {
				t.Fatalf("invalid UTF-8 errors = %v, %v", err, intoErr)
			}
			return
		}
		if err != nil || intoErr != nil {
			t.Fatalf("Scan errors = %v, %v", err, intoErr)
		}
		assertMatchesEqual(t, into, got)
	})
}

func FuzzBracedOctalEscapesScanIntoMatchesScan(f *testing.F) {
	f.Add([]byte{0, 0x80, 0xff})
	f.Add([]byte("中A"))
	f.Add([]byte{0xff, 0, '\n'})

	byteScanner, err := scankit.Compile([]scankit.Expression{{Id: 1, Pattern: `\o{0}\o{200}\o{377}`}})
	if err != nil {
		f.Fatal(err)
	}
	unicodeScanner, err := scankit.Compile([]scankit.Expression{{Id: 1, Pattern: `\o{47055}\o{101}`, Flags: scankit.CompileUTF8 | scankit.CompileUnicodeProperties}})
	if err != nil {
		f.Fatal(err)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 1024 {
			t.Skip()
		}
		for index, scanner := range []*scankit.Scanner{byteScanner, unicodeScanner} {
			got, err := scanner.Scan(data)
			into, intoErr := scanner.ScanInto(data, make([]scankit.Match, 0, len(got)))
			if index == 1 && !utf8.Valid(data) {
				if !errors.Is(err, scankit.ErrInvalidUTF8) || !errors.Is(intoErr, scankit.ErrInvalidUTF8) {
					t.Fatalf("invalid UTF-8 errors = %v, %v", err, intoErr)
				}
				continue
			}
			if err != nil || intoErr != nil {
				t.Fatalf("Scan errors = %v, %v", err, intoErr)
			}
			assertMatchesEqual(t, into, got)
		}
	})
}

func FuzzBracedHexEscapesScanIntoMatchesScan(f *testing.F) {
	f.Add([]byte{0, 0x80, 0xff})
	f.Add([]byte("中😀"))
	f.Add([]byte{0xff, 0, '\n'})

	byteScanner, err := scankit.Compile([]scankit.Expression{{Id: 1, Pattern: `\x{00}\x{80}\x{FF}`}})
	if err != nil {
		f.Fatal(err)
	}
	unicodeScanner, err := scankit.Compile([]scankit.Expression{{Id: 1, Pattern: `\x{4E2D}\x{1F600}`, Flags: scankit.CompileUTF8 | scankit.CompileUnicodeProperties}})
	if err != nil {
		f.Fatal(err)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 1024 {
			t.Skip()
		}
		for index, scanner := range []*scankit.Scanner{byteScanner, unicodeScanner} {
			got, err := scanner.Scan(data)
			into, intoErr := scanner.ScanInto(data, make([]scankit.Match, 0, len(got)))
			if index == 1 && !utf8.Valid(data) {
				if !errors.Is(err, scankit.ErrInvalidUTF8) || !errors.Is(intoErr, scankit.ErrInvalidUTF8) {
					t.Fatalf("invalid UTF-8 errors = %v, %v", err, intoErr)
				}
				continue
			}
			if err != nil || intoErr != nil {
				t.Fatalf("Scan errors = %v, %v", err, intoErr)
			}
			assertMatchesEqual(t, into, got)
		}
	})
}

func TestNumericEscapesWithoutLeadingZeroRemainUnsupported(t *testing.T) {
	t.Parallel()
	for _, flags := range []scankit.CompileFlag{0, scankit.CompileUTF8 | scankit.CompileUnicodeProperties} {
		for _, pattern := range []string{`\1`, `\400`} {
			_, err := scankit.Compile([]scankit.Expression{{Id: 1, Pattern: pattern, Flags: flags}})
			if !errors.Is(err, scankit.ErrUnsupportedExpression) {
				t.Fatalf("Compile(%q) error = %v, want ErrUnsupportedExpression", pattern, err)
			}
		}
	}
}

func FuzzOctalEscapesScanIntoMatchesScan(f *testing.F) {
	f.Add([]byte{0, '\n', 0xff})
	f.Add(append([]byte("张"), '\n', '3'))
	f.Add([]byte{0xff, 0})

	byteScanner, err := scankit.Compile([]scankit.Expression{{Id: 1, Pattern: `\0\012\0377`}})
	if err != nil {
		f.Fatal(err)
	}
	unicodeScanner, err := scankit.Compile([]scankit.Expression{{Id: 1, Pattern: `\0123`, Flags: scankit.CompileUTF8 | scankit.CompileUnicodeProperties}})
	if err != nil {
		f.Fatal(err)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 1024 {
			t.Skip()
		}
		for index, scanner := range []*scankit.Scanner{byteScanner, unicodeScanner} {
			got, err := scanner.Scan(data)
			into, intoErr := scanner.ScanInto(data, make([]scankit.Match, 0, len(got)))
			if index == 1 && !utf8.Valid(data) {
				if !errors.Is(err, scankit.ErrInvalidUTF8) || !errors.Is(intoErr, scankit.ErrInvalidUTF8) {
					t.Fatalf("invalid UTF-8 errors = %v, %v", err, intoErr)
				}
				continue
			}
			if err != nil || intoErr != nil {
				t.Fatalf("Scan errors = %v, %v", err, intoErr)
			}
			assertMatchesEqual(t, into, got)
		}
	})
}

func TestNotNewlineEscapeIgnoresDotAll(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		flags scankit.CompileFlag
		data  []byte
		want  []scankit.Match
	}{
		{
			name:  "byte",
			flags: scankit.CompileDotAll,
			data:  []byte("a\nb"),
			want:  []scankit.Match{{Id: 1, From: 0, To: 1}, {Id: 1, From: 2, To: 3}},
		},
		{
			name:  "unicode",
			flags: scankit.CompileUTF8 | scankit.CompileUnicodeProperties | scankit.CompileDotAll,
			data:  []byte("张\nA"),
			want:  []scankit.Match{{Id: 1, From: 0, To: 3}, {Id: 1, From: 4, To: 5}},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			scanner, err := scankit.Compile([]scankit.Expression{{Id: 1, Pattern: `\N`, Flags: test.flags}})
			if err != nil {
				t.Fatal(err)
			}
			got, err := scanner.Scan(test.data)
			if err != nil {
				t.Fatal(err)
			}
			assertMatchesEqual(t, got, test.want)
		})
	}
}

func FuzzNotNewlineEscapeScanIntoMatchesScan(f *testing.F) {
	f.Add([]byte("a\nb"))
	f.Add([]byte("张\nA"))
	f.Add([]byte{0xff, '\n'})

	byteScanner, err := scankit.Compile([]scankit.Expression{{Id: 1, Pattern: `\N`, Flags: scankit.CompileDotAll}})
	if err != nil {
		f.Fatal(err)
	}
	unicodeScanner, err := scankit.Compile([]scankit.Expression{{Id: 1, Pattern: `\N`, Flags: scankit.CompileUTF8 | scankit.CompileUnicodeProperties | scankit.CompileDotAll}})
	if err != nil {
		f.Fatal(err)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 1024 {
			t.Skip()
		}
		for index, scanner := range []*scankit.Scanner{byteScanner, unicodeScanner} {
			got, err := scanner.Scan(data)
			into, intoErr := scanner.ScanInto(data, make([]scankit.Match, 0, len(got)))
			if index == 1 && !utf8.Valid(data) {
				if !errors.Is(err, scankit.ErrInvalidUTF8) || !errors.Is(intoErr, scankit.ErrInvalidUTF8) {
					t.Fatalf("invalid UTF-8 errors = %v, %v", err, intoErr)
				}
				continue
			}
			if err != nil || intoErr != nil {
				t.Fatalf("Scan errors = %v, %v", err, intoErr)
			}
			assertMatchesEqual(t, into, got)
		}
	})
}

func FuzzControlEscapesScanIntoMatchesScan(f *testing.F) {
	f.Add([]byte{'\a', 0x1b})
	f.Add([]byte{1, 26})
	f.Add([]byte("张\a"))
	f.Add([]byte{'A', 0xff, '\a', 0x1b})

	scanners := make([]*scankit.Scanner, 0, 4)
	for _, expression := range []scankit.Expression{
		{Id: 1, Pattern: `\a\e`},
		{Id: 1, Pattern: `\cA\cz`},
		{Id: 1, Pattern: `\a\e`, Flags: scankit.CompileUTF8 | scankit.CompileUnicodeProperties},
		{Id: 1, Pattern: `\cA\cz`, Flags: scankit.CompileUTF8 | scankit.CompileUnicodeProperties},
	} {
		scanner, err := scankit.Compile([]scankit.Expression{expression})
		if err != nil {
			f.Fatal(err)
		}
		scanners = append(scanners, scanner)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 1024 {
			t.Skip()
		}
		for index, scanner := range scanners {
			got, err := scanner.Scan(data)
			into, intoErr := scanner.ScanInto(data, make([]scankit.Match, 0, len(got)))
			if !utf8.Valid(data) && index >= 2 {
				if !errors.Is(err, scankit.ErrInvalidUTF8) || !errors.Is(intoErr, scankit.ErrInvalidUTF8) {
					t.Fatalf("invalid UTF-8 errors = %v, %v", err, intoErr)
				}
				continue
			}
			if err != nil || intoErr != nil {
				t.Fatalf("Scan errors = %v, %v", err, intoErr)
			}
			assertMatchesEqual(t, into, got)
		}
	})
}

func TestUCPLineBreakEscapeTreatsUnicodeNewlinesAndCRLFAsOneMatch(t *testing.T) {
	t.Parallel()
	database, err := scankit.Compile([]scankit.Expression{{
		Id:      1,
		Pattern: `\R`,
		Flags:   scankit.CompileUTF8 | scankit.CompileUnicodeProperties,
	}})
	if err != nil {
		t.Fatal(err)
	}
	data := []byte("a\r\nb\rc\nd\u0085e\u2028f\u2029")
	want := []scankit.Match{
		{Id: 1, From: 1, To: 3},
		{Id: 1, From: 4, To: 5},
		{Id: 1, From: 6, To: 7},
		{Id: 1, From: 8, To: 10},
		{Id: 1, From: 11, To: 14},
		{Id: 1, From: 15, To: 18},
	}
	got, err := database.Scan(data)
	if err != nil {
		t.Fatal(err)
	}
	assertMatchesEqual(t, got, want)
	got, err = database.ScanInto(data, make([]scankit.Match, 0, len(want)))
	if err != nil {
		t.Fatal(err)
	}
	assertMatchesEqual(t, got, want)
}

func TestASCIIHorizontalAndVerticalWhitespaceEscapes(t *testing.T) {
	t.Parallel()
	database, err := scankit.Compile([]scankit.Expression{
		{Id: 1, Pattern: `\h+`},
		{Id: 2, Pattern: `\v+`},
		{Id: 3, Pattern: `\H+`},
		{Id: 4, Pattern: `\V+`},
	})
	if err != nil {
		t.Fatal(err)
	}
	matches, err := database.Scan([]byte("a \t\n\rb"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) == 0 {
		t.Fatal("whitespace escapes produced no matches")
	}
	seen := make(map[uint32]bool)
	for _, match := range matches {
		seen[match.Id] = true
	}
	for _, id := range []uint32{1, 2, 3, 4} {
		if !seen[id] {
			t.Fatalf("escape rule %d produced no match", id)
		}
	}
}

func TestCurrentCompatibilityCapabilityFixturesCompile(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name    string
		pattern string
		flags   scankit.CompileFlag
	}{
		{name: "byte classes and quantifiers", pattern: `ID\x3A[[:digit:]]{2,4}`},
		{name: "anchors and boundaries", pattern: `\A\btoken\B\z`},
		{name: "end before final newline", pattern: `token\Z`},
		{name: "inline and scoped modifiers", pattern: `(?i:token)(?-i:VALUE)(?s:.)`},
		{name: "Unicode inline and scoped modifiers", pattern: "(?i:σ)(?m:^用户$)(?s:.)(?x: 用 # comment\n 户)", flags: scankit.CompileUTF8 | scankit.CompileUnicodeProperties},
		{name: "Unicode extended shorthand and quoted group", pattern: `(?<word>\Q用户\E)\h\v\W`, flags: scankit.CompileUTF8 | scankit.CompileUnicodeProperties},
		{name: "named and noncapturing groups", pattern: `(?<name>(?:token|secret))`},
		{name: "quoted literal and comment", pattern: `(?#tag)\Q[a-z]+\E`},
		{name: "unicode property", pattern: `\p{Han}+`, flags: scankit.CompileUTF8 | scankit.CompileUnicodeProperties},
		{name: "unicode caseless", pattern: `Σ+`, flags: scankit.CompileUTF8 | scankit.CompileUnicodeProperties | scankit.CompileCaseless},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := scankit.Compile([]scankit.Expression{{Id: 1, Pattern: test.pattern, Flags: test.flags}}); err != nil {
				t.Fatalf("Compile(%q) error = %v", test.pattern, err)
			}
		})
	}
}
