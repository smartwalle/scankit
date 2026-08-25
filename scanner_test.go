package scankit_test

import (
	"bytes"
	"errors"
	"fmt"
	"regexp"
	"sync"
	"testing"
	"unicode/utf8"

	"github.com/smartwalle/scankit"
)

func TestCompileAndScanReportsAllMatchesInStableOrder(t *testing.T) {
	t.Parallel()

	db, err := scankit.Compile([]scankit.Expression{
		{Id: 10, Pattern: "foo"},
		{Id: 20, Pattern: "bar"},
		{Id: 30, Pattern: "foobar"},
		{Id: 40, Pattern: "oba"},
	})
	if err != nil {
		t.Fatalf("Compile() error = %v", err)
	}

	got, err := db.Scan([]byte("foobar"))
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}

	want := []scankit.Match{
		{Id: 10, From: 0, To: 3},
		{Id: 40, From: 2, To: 5},
		{Id: 20, From: 3, To: 6},
		{Id: 30, From: 0, To: 6},
	}
	assertMatchesEqual(t, got, want)
}

func TestScanAndScanIntoReturnStableEvents(t *testing.T) {
	t.Parallel()

	db, err := scankit.Compile([]scankit.Expression{
		{Id: 10, Pattern: "foo"},
		{Id: 20, Pattern: `bar[0-9]+`},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []scankit.Match{
		{Id: 10, From: 0, To: 3},
		{Id: 20, From: 4, To: 8},
		{Id: 20, From: 4, To: 9},
	}
	got, err := db.Scan([]byte("foo bar12"))
	if err != nil {
		t.Fatal(err)
	}
	assertMatchesEqual(t, got, want)

	prefix := []scankit.Match{{Id: 99, From: 9, To: 9}}
	got, err = db.ScanInto([]byte("foo bar12"), prefix)
	if err != nil {
		t.Fatal(err)
	}
	assertMatchesEqual(t, got, append(prefix, want...))

}

func TestCompileAndScanVerifiesRegexFromLiteralAnchors(t *testing.T) {
	t.Parallel()

	db, err := scankit.Compile([]scankit.Expression{
		{Id: 10, Pattern: `id-[0-9]{2,4}`},
		{Id: 20, Pattern: "token"},
		{Id: 30, Pattern: `[A-Z]{2}-token-[0-9]{2}`},
	})
	if err != nil {
		t.Fatalf("Compile() error = %v", err)
	}

	got, err := db.Scan([]byte("id-12 token XX-token-34 id-1234"))
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}

	want := []scankit.Match{
		{Id: 10, From: 0, To: 5},
		{Id: 20, From: 6, To: 11},
		{Id: 20, From: 15, To: 20},
		{Id: 30, From: 12, To: 23},
		{Id: 10, From: 24, To: 29},
		{Id: 10, From: 24, To: 30},
		{Id: 10, From: 24, To: 31},
	}
	assertMatchesEqual(t, got, want)
}

func TestScanDeliversRegexEventsAtTheirActualEndOffset(t *testing.T) {
	t.Parallel()

	db, err := scankit.Compile([]scankit.Expression{
		{Id: 10, Pattern: `token[0-9]{4}`},
		{Id: 20, Pattern: "token1234"},
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := db.Scan([]byte("token1234"))
	if err != nil {
		t.Fatal(err)
	}
	assertMatchesEqual(t, got, []scankit.Match{
		{Id: 10, From: 0, To: 9},
		{Id: 20, From: 0, To: 9},
	})
}

func TestScanSupportsASCIIWordBoundaryAssertions(t *testing.T) {
	t.Parallel()

	db, err := scankit.Compile([]scankit.Expression{
		{Id: 1, Pattern: `\btoken\b`},
		{Id: 2, Pattern: `\Btoken\B`},
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := db.Scan([]byte("token token1 _token_ atokenb token- token!"))
	if err != nil {
		t.Fatal(err)
	}
	assertMatchesEqual(t, got, []scankit.Match{
		{Id: 1, From: 0, To: 5},
		{Id: 2, From: 14, To: 19},
		{Id: 2, From: 22, To: 27},
		{Id: 1, From: 29, To: 34},
		{Id: 1, From: 36, To: 41},
	})
}

func TestScanSupportsHexEscapesAndPOSIXCharacterClasses(t *testing.T) {
	t.Parallel()

	db, err := scankit.Compile([]scankit.Expression{
		{Id: 1, Pattern: `ID\x3A[[:digit:]]{2}`},
		{Id: 2, Pattern: `[[:alpha:]]\x2D[[:xdigit:]]{2}`},
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := db.Scan([]byte("ID:42 ID;43 A-1F z-gg"))
	if err != nil {
		t.Fatal(err)
	}
	assertMatchesEqual(t, got, []scankit.Match{
		{Id: 1, From: 0, To: 5},
		{Id: 2, From: 12, To: 16},
	})
}

func TestScanSupportsAbsoluteTextAnchors(t *testing.T) {
	t.Parallel()

	db, err := scankit.Compile([]scankit.Expression{
		{Id: 1, Pattern: `^ID:\d{2}$`, Flags: scankit.CompileMultiline},
		{Id: 2, Pattern: `\AID:\d{2}\z`, Flags: scankit.CompileMultiline},
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := db.Scan([]byte("ID:42\nID:99"))
	if err != nil {
		t.Fatal(err)
	}
	assertMatchesEqual(t, got, []scankit.Match{
		{Id: 1, From: 0, To: 5},
		{Id: 1, From: 6, To: 11},
	})

	db, err = scankit.Compile([]scankit.Expression{{Id: 2, Pattern: `\AID:\d{2}\z`}})
	if err != nil {
		t.Fatal(err)
	}
	got, err = db.Scan([]byte("ID:42"))
	if err != nil {
		t.Fatal(err)
	}
	assertMatchesEqual(t, got, []scankit.Match{{Id: 2, From: 0, To: 5}})
}

func TestScanSupportsEndBeforeFinalNewlineAnchor(t *testing.T) {
	t.Parallel()

	database, err := scankit.Compile([]scankit.Expression{{Id: 1, Pattern: `\Atoken\Z`}})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name string
		data []byte
		want []scankit.Match
	}{
		{name: "absolute end", data: []byte("token"), want: []scankit.Match{{Id: 1, From: 0, To: 5}}},
		{name: "final LF", data: []byte("token\n"), want: []scankit.Match{{Id: 1, From: 0, To: 5}}},
		{name: "final CRLF", data: []byte("token\r\n"), want: []scankit.Match{{Id: 1, From: 0, To: 5}}},
		{name: "non-final newline", data: []byte("token\nnext"), want: nil},
		{name: "trailing CR only", data: []byte("token\r"), want: nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := database.Scan(test.data)
			if err != nil {
				t.Fatal(err)
			}
			assertMatchesEqual(t, got, test.want)
		})
	}
}

func TestScanSupportsInlineAndScopedFlags(t *testing.T) {
	t.Parallel()
	database, err := scankit.Compile([]scankit.Expression{
		{Id: 1, Pattern: `(?i)token`},
		{Id: 2, Pattern: `(?i:token)VALUE`},
		{Id: 3, Pattern: `(?s:a.b)`},
		{Id: 4, Pattern: `(?m:^token$)`},
		{Id: 5, Pattern: "(?x) token # ignored\n VALUE"},
		{Id: 6, Pattern: `(?-i:token)`, Flags: scankit.CompileCaseless},
	})
	if err != nil {
		t.Fatal(err)
	}
	data := []byte("TOKEN tokenVALUE TOKENVALUE a\nb\nline\ntoken\nTOKEN\ntokenVALUE")
	got, err := database.Scan(data)
	if err != nil {
		t.Fatal(err)
	}
	assertMatchesEqual(t, got, []scankit.Match{
		{Id: 1, From: 0, To: 5},
		{Id: 1, From: 6, To: 11},
		{Id: 6, From: 6, To: 11},
		{Id: 2, From: 6, To: 16},
		{Id: 5, From: 6, To: 16},
		{Id: 1, From: 17, To: 22},
		{Id: 2, From: 17, To: 27},
		{Id: 3, From: 28, To: 31},
		{Id: 1, From: 37, To: 42},
		{Id: 4, From: 37, To: 42},
		{Id: 6, From: 37, To: 42},
		{Id: 1, From: 43, To: 48},
		{Id: 1, From: 49, To: 54},
		{Id: 6, From: 49, To: 54},
		{Id: 2, From: 49, To: 59},
		{Id: 5, From: 49, To: 59},
	})
}

func TestInlineFlagsRespectScopeAndExpressionFlags(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		pattern string
		flags   scankit.CompileFlag
		data    string
		want    []scankit.Match
	}{
		{
			name:    "scoped caseless does not leak",
			pattern: `(?i:token)VALUE`,
			data:    "tokenVALUE tokenvalue TOKENVALUE",
			want:    []scankit.Match{{Id: 1, From: 0, To: 10}, {Id: 1, From: 22, To: 32}},
		},
		{
			name:    "global caseless inside group does not leak",
			pattern: `(?:(?i)token)VALUE`,
			data:    "TOKENVALUE TOKENvalue tokenVALUE",
			want:    []scankit.Match{{Id: 1, From: 0, To: 10}, {Id: 1, From: 22, To: 32}},
		},
		{
			name:    "global caseless can be disabled",
			pattern: `(?i)token(?-i)VALUE`,
			data:    "TOKENVALUE TOKENvalue tokenVALUE",
			want:    []scankit.Match{{Id: 1, From: 0, To: 10}, {Id: 1, From: 22, To: 32}},
		},
		{
			name:    "scoped dotall",
			pattern: `a(?s:.)b`,
			data:    "a\nb aXb",
			want:    []scankit.Match{{Id: 1, From: 0, To: 3}, {Id: 1, From: 4, To: 7}},
		},
		{
			name:    "expression dotall can be disabled",
			pattern: `a(?-s:.)b`,
			flags:   scankit.CompileDotAll,
			data:    "a\nb aXb",
			want:    []scankit.Match{{Id: 1, From: 4, To: 7}},
		},
		{
			name:    "scoped multiline",
			pattern: `(?m:^token$)`,
			data:    "x\ntoken\ny",
			want:    []scankit.Match{{Id: 1, From: 2, To: 7}},
		},
		{
			name:    "expression multiline can be disabled",
			pattern: `(?-m:^token$)`,
			flags:   scankit.CompileMultiline,
			data:    "x\ntoken\ny",
			want:    nil,
		},
		{
			name:    "extended scoped comments whitespace and classes",
			pattern: "(?x: token # ignored\n [ #] )\\#",
			data:    "token # token#",
			want:    []scankit.Match{{Id: 1, From: 0, To: 7}},
		},
		{
			name:    "extended mode can be disabled locally",
			pattern: `(?x)token(?-x: )VALUE`,
			data:    "token VALUE tokenVALUE",
			want:    []scankit.Match{{Id: 1, From: 0, To: 11}},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			database, err := scankit.Compile([]scankit.Expression{{Id: 1, Pattern: test.pattern, Flags: test.flags}})
			if err != nil {
				t.Fatal(err)
			}
			got, err := database.Scan([]byte(test.data))
			if err != nil {
				t.Fatal(err)
			}
			assertMatchesEqual(t, got, test.want)
		})
	}
}

func TestCompileRejectsInvalidInlineFlags(t *testing.T) {
	t.Parallel()
	for _, pattern := range []string{`(?q)token`, `(?-)token`, `(?i-:token)`, `(?i-token)`, `(?i-sx-token)`} {
		if _, err := scankit.Compile([]scankit.Expression{{Id: 1, Pattern: pattern}}); err == nil {
			t.Fatalf("Compile(%q) error = nil", pattern)
		}
	}
}

func TestScanSupportsInlineFlagsForUCP(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		pattern string
		flags   scankit.CompileFlag
		data    string
		want    []scankit.Match
	}{
		{name: "scoped Unicode caseless", pattern: `(?i:σ)`, flags: scankit.CompileUTF8 | scankit.CompileUnicodeProperties, data: "Σ σ", want: []scankit.Match{{Id: 1, From: 0, To: 2}, {Id: 1, From: 3, To: 5}}},
		{name: "disable expression Unicode caseless", pattern: `(?-i:σ)`, flags: scankit.CompileUTF8 | scankit.CompileUnicodeProperties | scankit.CompileCaseless, data: "Σ σ", want: []scankit.Match{{Id: 1, From: 3, To: 5}}},
		{name: "scoped Unicode dotall", pattern: `甲(?s:.)乙`, flags: scankit.CompileUTF8 | scankit.CompileUnicodeProperties, data: "甲\n乙", want: []scankit.Match{{Id: 1, From: 0, To: 7}}},
		{name: "disable expression Unicode dotall", pattern: `甲(?-s:.)乙`, flags: scankit.CompileUTF8 | scankit.CompileUnicodeProperties | scankit.CompileDotAll, data: "甲\n乙 甲x乙", want: []scankit.Match{{Id: 1, From: 8, To: 15}}},
		{name: "scoped Unicode multiline", pattern: `(?m:^甲$)`, flags: scankit.CompileUTF8 | scankit.CompileUnicodeProperties, data: "x\n甲\ny", want: []scankit.Match{{Id: 1, From: 2, To: 5}}},
		{name: "Unicode extended", pattern: "(?x: 甲 # comment\n 乙 )", flags: scankit.CompileUTF8 | scankit.CompileUnicodeProperties, data: "甲乙", want: []scankit.Match{{Id: 1, From: 0, To: 6}}},
		{name: "Unicode end before final newline", pattern: `用户\Z`, flags: scankit.CompileUTF8 | scankit.CompileUnicodeProperties, data: "用户\n", want: []scankit.Match{{Id: 1, From: 0, To: 6}}},
		{name: "Unicode horizontal whitespace", pattern: `甲\h乙`, flags: scankit.CompileUTF8 | scankit.CompileUnicodeProperties, data: "甲　乙", want: []scankit.Match{{Id: 1, From: 0, To: 9}}},
		{name: "Unicode vertical whitespace", pattern: `甲\v乙`, flags: scankit.CompileUTF8 | scankit.CompileUnicodeProperties, data: "甲\u2028乙", want: []scankit.Match{{Id: 1, From: 0, To: 9}}},
		{name: "Unicode named and quoted groups", pattern: `(?<word>\Q用户.+\E)`, flags: scankit.CompileUTF8 | scankit.CompileUnicodeProperties, data: "用户.+", want: []scankit.Match{{Id: 1, From: 0, To: 8}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			database, err := scankit.Compile([]scankit.Expression{{Id: 1, Pattern: test.pattern, Flags: test.flags}})
			if err != nil {
				t.Fatal(err)
			}
			got, err := database.Scan([]byte(test.data))
			if err != nil {
				t.Fatal(err)
			}
			assertMatchesEqual(t, got, test.want)
		})
	}
}

func TestScanAppliesExpressionExtensionsBeforeSingleMatch(t *testing.T) {
	t.Parallel()

	db, err := scankit.Compile([]scankit.Expression{
		{Id: 1, Pattern: "token", Flags: scankit.CompileSingleMatch, Ext: &scankit.ExpressionExt{Flags: scankit.ExtMinOffset, MinOffset: 6}},
		{Id: 2, Pattern: `\d{3}`, Ext: &scankit.ExpressionExt{Flags: scankit.ExtMinOffset | scankit.ExtMaxOffset | scankit.ExtMinLength, MinOffset: 17, MaxOffset: 19, MinLength: 3}},
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := db.Scan([]byte("token token 123 4567"))
	if err != nil {
		t.Fatal(err)
	}
	assertMatchesEqual(t, got, []scankit.Match{
		{Id: 1, From: 6, To: 11},
		{Id: 2, From: 16, To: 19},
	})
}

func TestCompileRejectsInvalidOrUnsupportedExtensions(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		ext  scankit.ExpressionExt
		want error
	}{
		{name: "unknown flag", ext: scankit.ExpressionExt{Flags: scankit.ExpressionExtFlag(1 << 30)}, want: scankit.ErrInvalidExtension},
		{name: "reversed offsets", ext: scankit.ExpressionExt{Flags: scankit.ExtMinOffset | scankit.ExtMaxOffset, MinOffset: 9, MaxOffset: 8}, want: scankit.ErrInvalidExtension},
		{name: "regular expression edit distance", ext: scankit.ExpressionExt{Flags: scankit.ExtEditDistance, EditDistance: 1}, want: scankit.ErrUnsupportedExtension},
		{name: "regular expression Hamming distance", ext: scankit.ExpressionExt{Flags: scankit.ExtHammingDistance, HammingDistance: 1}, want: scankit.ErrUnsupportedExtension},
		{name: "both approximate distances", ext: scankit.ExpressionExt{Flags: scankit.ExtEditDistance | scankit.ExtHammingDistance, EditDistance: 1, HammingDistance: 1}, want: scankit.ErrInvalidExtension},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			pattern := "token"
			if test.name == "regular expression edit distance" || test.name == "regular expression Hamming distance" {
				pattern = `token+`
			}
			_, err := scankit.Compile([]scankit.Expression{{Id: 1, Pattern: pattern, Ext: &test.ext}})
			if !errors.Is(err, test.want) {
				t.Fatalf("Compile() error = %v, want errors.Is(_, %v)", err, test.want)
			}
		})
	}
}

func TestScanEvaluatesCombinationExpressions(t *testing.T) {
	t.Parallel()

	db, err := scankit.Compile([]scankit.Expression{
		{Id: 1, Pattern: "user", Flags: scankit.CompileQuiet},
		{Id: 2, Pattern: "token", Flags: scankit.CompileQuiet},
		{Id: 3, Pattern: "1&2", Flags: scankit.CompileCombination},
		{Id: 4, Pattern: "1&!2", Flags: scankit.CompileCombination},
		{Id: 5, Pattern: "(1|2)", Flags: scankit.CompileCombination},
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := db.Scan([]byte("user token"))
	if err != nil {
		t.Fatal(err)
	}
	assertMatchesEqual(t, got, []scankit.Match{
		{Id: 4, From: 0, To: 4},
		{Id: 5, From: 0, To: 4},
		{Id: 3, From: 0, To: 10},
	})
}

func TestScanAppliesConstraintsBeforeCombinationDelivery(t *testing.T) {
	t.Parallel()

	db, err := scankit.Compile([]scankit.Expression{
		{Id: 1, Pattern: "token", Flags: scankit.CompileQuiet, Ext: &scankit.ExpressionExt{Flags: scankit.ExtMinOffset, MinOffset: 11}},
		{Id: 2, Pattern: "1", Flags: scankit.CompileCombination},
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := db.Scan([]byte("token token"))
	if err != nil {
		t.Fatal(err)
	}
	assertMatchesEqual(t, got, []scankit.Match{{Id: 2, To: 11}})
}

func TestCompileRejectsInvalidCombinationExpressions(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		expressions []scankit.Expression
	}{
		{name: "missing operand", expressions: []scankit.Expression{{Id: 1, Pattern: "token"}, {Id: 2, Pattern: "1&", Flags: scankit.CompileCombination}}},
		{name: "unknown operand", expressions: []scankit.Expression{{Id: 1, Pattern: "token"}, {Id: 2, Pattern: "1&99", Flags: scankit.CompileCombination}}},
		{name: "self reference", expressions: []scankit.Expression{{Id: 1, Pattern: "token"}, {Id: 2, Pattern: "2", Flags: scankit.CompileCombination}}},
		{name: "nested combination", expressions: []scankit.Expression{{Id: 1, Pattern: "token"}, {Id: 2, Pattern: "1", Flags: scankit.CompileCombination}, {Id: 3, Pattern: "2", Flags: scankit.CompileCombination}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := scankit.Compile(test.expressions)
			if !errors.Is(err, scankit.ErrInvalidCombination) {
				t.Fatalf("Compile() error = %v, want invalid combination", err)
			}
		})
	}
}

func TestCompileRejectsInvalidMilestoneInputs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		expressions []scankit.Expression
		want        error
	}{
		{name: "no expressions", want: scankit.ErrEmptyExpressions},
		{name: "duplicate id", expressions: []scankit.Expression{{Id: 1, Pattern: "abc"}, {Id: 1, Pattern: "def"}}, want: scankit.ErrDuplicateExpression},
		{name: "empty pattern", expressions: []scankit.Expression{{Id: 1, Pattern: ""}}, want: scankit.ErrInvalidExpression},
		{name: "UCP compile flag", expressions: []scankit.Expression{{Id: 1, Pattern: "abc", Flags: scankit.CompileUnicodeProperties}}, want: scankit.ErrUnsupportedFlag},
		{name: "empty matching expression", expressions: []scankit.Expression{{Id: 1, Pattern: "a*"}}, want: scankit.ErrUnsupportedExpression},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := scankit.Compile(tt.expressions)
			if !errors.Is(err, tt.want) {
				t.Fatalf("Compile() error = %v, want errors.Is(_, %v)", err, tt.want)
			}
		})
	}
}

func TestScanRunsUnanchoredRegexInOneInputPass(t *testing.T) {
	t.Parallel()

	db, err := scankit.Compile([]scankit.Expression{{Id: 1, Pattern: `\d+`}})
	if err != nil {
		t.Fatal(err)
	}
	got, err := db.Scan([]byte("a12b3"))
	if err != nil {
		t.Fatal(err)
	}
	assertMatchesEqual(t, got, []scankit.Match{
		{Id: 1, From: 1, To: 2},
		{Id: 1, From: 1, To: 3},
		{Id: 1, From: 2, To: 3},
		{Id: 1, From: 4, To: 5},
	})
}

func TestUnanchoredRegexReportsOneMatchPerEndOffset(t *testing.T) {
	t.Parallel()
	db, err := scankit.Compile([]scankit.Expression{{Id: 1, Pattern: `\d+`}})
	if err != nil {
		t.Fatal(err)
	}
	got, err := db.Scan([]byte("ts=2026"))
	if err != nil {
		t.Fatal(err)
	}
	assertMatchesEqual(t, got, []scankit.Match{
		{Id: 1, From: 3, To: 4},
		{Id: 1, From: 3, To: 5},
		{Id: 1, From: 3, To: 6},
		{Id: 1, From: 3, To: 7},
	})
}

func TestScanRunsSingleClassRepeatsWithOverlapAndLeftmostSemantics(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		pattern  string
		flags    scankit.CompileFlag
		data     string
		expected []scankit.Match
	}{
		{
			name:    "unbounded overlap",
			pattern: `\d+`,
			data:    "a12b3",
			expected: []scankit.Match{
				{Id: 1, From: 1, To: 2},
				{Id: 1, From: 1, To: 3},
				{Id: 1, From: 2, To: 3},
				{Id: 1, From: 4, To: 5},
			},
		},
		{
			name:    "bounded overlap",
			pattern: `\d{2,3}`,
			data:    "1234",
			expected: []scankit.Match{
				{Id: 1, From: 0, To: 2},
				{Id: 1, From: 0, To: 3},
				{Id: 1, From: 1, To: 3},
				{Id: 1, From: 1, To: 4},
				{Id: 1, From: 2, To: 4},
			},
		},
		{
			name:    "leftmost",
			pattern: `\d+`,
			flags:   scankit.CompileLeftmostStart,
			data:    "123",
			expected: []scankit.Match{
				{Id: 1, From: 0, To: 1},
				{Id: 1, From: 0, To: 2},
				{Id: 1, From: 0, To: 3},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db, err := scankit.Compile([]scankit.Expression{{Id: 1, Pattern: tt.pattern, Flags: tt.flags}})
			if err != nil {
				t.Fatal(err)
			}
			got, err := db.Scan([]byte(tt.data))
			if err != nil {
				t.Fatal(err)
			}
			assertMatchesEqual(t, got, tt.expected)
		})
	}
}

func FuzzScanSingleClassRepeatPreservesEndOffsetsAndLeftmostSemantics(f *testing.F) {
	f.Add([]byte("a12b3"))
	f.Add([]byte("1234"))
	f.Add([]byte(""))
	f.Add([]byte("abc"))

	tests := []struct {
		pattern  string
		flags    scankit.CompileFlag
		minimum  int
		maximum  int
		leftmost bool
	}{
		{pattern: `\d+`, minimum: 1, maximum: -1},
		{pattern: `\d{2,3}`, minimum: 2, maximum: 3},
		{pattern: `\d+`, flags: scankit.CompileLeftmostStart, minimum: 1, maximum: -1, leftmost: true},
	}
	databases := make([]*scankit.Scanner, len(tests))
	for index, tt := range tests {
		db, err := scankit.Compile([]scankit.Expression{{Id: 1, Pattern: tt.pattern, Flags: tt.flags}})
		if err != nil {
			f.Fatal(err)
		}
		databases[index] = db
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 4_096 {
			t.Skip()
		}
		for index, tt := range tests {
			got, err := databases[index].Scan(data)
			if err != nil {
				t.Fatal(err)
			}
			assertMatchesEqual(t, got, simpleDigitRepeatMatches(data, tt.minimum, tt.maximum, tt.leftmost))
		}
	})
}

func simpleDigitRepeatMatches(data []byte, minimum, maximum int, leftmost bool) []scankit.Match {
	matches := make([]scankit.Match, 0)
	runStart := 0
	for end := 1; end <= len(data); end++ {
		if data[end-1] < '0' || data[end-1] > '9' {
			runStart = end
			continue
		}
		start := runStart
		if maximum >= 0 && end-start > maximum {
			start = end - maximum
		}
		if end-start < minimum {
			continue
		}
		matches = append(matches, scankit.Match{Id: 1, From: uint64(start), To: uint64(end)})
	}
	return matches
}

func TestScanSupportsASCIICaselessExpressions(t *testing.T) {
	t.Parallel()

	db, err := scankit.Compile([]scankit.Expression{{Id: 1, Pattern: `token-[a-f]{2}`, Flags: scankit.CompileCaseless}})
	if err != nil {
		t.Fatal(err)
	}
	got, err := db.Scan([]byte("TOKEN-aF token-ZZ"))
	if err != nil {
		t.Fatal(err)
	}
	assertMatchesEqual(t, got, []scankit.Match{{Id: 1, From: 0, To: 8}})
}

func TestScanAppliesDotAllMultilineSingleMatchAndQuiet(t *testing.T) {
	t.Parallel()

	db, err := scankit.Compile([]scankit.Expression{
		{Id: 1, Pattern: `a.b`},
		{Id: 2, Pattern: `a.b`, Flags: scankit.CompileDotAll},
		{Id: 3, Pattern: `^token$`, Flags: scankit.CompileMultiline},
		{Id: 4, Pattern: "token", Flags: scankit.CompileSingleMatch},
		{Id: 5, Pattern: "secret", Flags: scankit.CompileQuiet},
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := db.Scan([]byte("a\nb\ntoken\ntoken secret secret\ntoken"))
	if err != nil {
		t.Fatal(err)
	}
	assertMatchesEqual(t, got, []scankit.Match{
		{Id: 2, From: 0, To: 3},
		{Id: 3, From: 4, To: 9},
		{Id: 4, From: 4, To: 9},
		{Id: 3, From: 30, To: 35},
	})
}

func TestScanUsesInternalContext(t *testing.T) {
	t.Parallel()

	db, err := scankit.Compile([]scankit.Expression{{Id: 1, Pattern: "match"}})
	if err != nil {
		t.Fatal(err)
	}
	matches, err := db.Scan([]byte("match"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 {
		t.Fatalf("matches = %d, want 1", len(matches))
	}
}

func TestScannerSupportsConcurrentInternalPooledScans(t *testing.T) {
	db, err := scankit.Compile([]scankit.Expression{
		{Id: 1, Pattern: "token"},
		{Id: 2, Pattern: "secret"},
	})
	if err != nil {
		t.Fatal(err)
	}

	const workers = 32
	var group sync.WaitGroup
	errorsByWorker := make(chan error, workers)
	for range workers {
		group.Add(1)
		go func() {
			defer group.Done()
			matches, err := db.Scan([]byte("token secret token"))
			if err != nil {
				errorsByWorker <- err
				return
			}
			if len(matches) != 3 {
				errorsByWorker <- fmt.Errorf("match count = %d, want 3", len(matches))
			}
		}()
	}
	group.Wait()
	close(errorsByWorker)
	for err := range errorsByWorker {
		t.Error(err)
	}
}

func FuzzScanReportsValidByteRanges(f *testing.F) {
	db, err := scankit.Compile([]scankit.Expression{
		{Id: 1, Pattern: "foo"},
		{Id: 2, Pattern: "bar"},
		{Id: 3, Pattern: "你好"},
	})
	if err != nil {
		f.Fatal(err)
	}
	f.Add([]byte("foo and bar"))
	f.Add([]byte(""))
	f.Add([]byte("你好foo"))
	f.Add([]byte{0xff, 'f', 'o', 'o'})

	f.Fuzz(func(t *testing.T, data []byte) {
		previousEnd := uint64(0)
		matches, err := db.Scan(data)
		for _, match := range matches {
			if match.From >= match.To || match.To > uint64(len(data)) {
				t.Fatalf("invalid range [%d,%d) for %d bytes", match.From, match.To, len(data))
			}
			if match.To < previousEnd {
				t.Fatalf("match end moved backward from %d to %d", previousEnd, match.To)
			}
			previousEnd = match.To
		}
		if err != nil {
			t.Fatalf("Scan() error = %v", err)
		}
	})
}

func FuzzScanRegexRulesReportsValidByteRanges(f *testing.F) {
	db, err := scankit.Compile([]scankit.Expression{
		{Id: 1, Pattern: `id-[0-9]{2,4}`},
		{Id: 2, Pattern: `[A-Z]{2}-token-[0-9]{2}`},
	})
	if err != nil {
		f.Fatal(err)
	}
	f.Add([]byte("id-12 XX-token-34"))
	f.Add([]byte(""))
	f.Add([]byte("id-1"))

	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 4_096 {
			t.Skip()
		}
		previousEnd := uint64(0)
		matches, err := db.Scan(data)
		for _, match := range matches {
			if match.From >= match.To || match.To > uint64(len(data)) {
				t.Fatalf("invalid range [%d,%d) for %d bytes", match.From, match.To, len(data))
			}
			if match.To < previousEnd {
				t.Fatalf("match end moved backward from %d to %d", previousEnd, match.To)
			}
			previousEnd = match.To
		}
		if err != nil {
			t.Fatalf("Scan() error = %v", err)
		}
	})
}

func FuzzScanUnanchoredRegexReportsValidByteRanges(f *testing.F) {
	db, err := scankit.Compile([]scankit.Expression{{Id: 1, Pattern: `\d+`}})
	if err != nil {
		f.Fatal(err)
	}
	f.Add([]byte("123"))
	f.Add([]byte("a1b2"))
	f.Add([]byte(""))

	f.Fuzz(func(t *testing.T, data []byte) {
		// 连续 n 个数字会产生 n*(n+1)/2 个可观察重叠匹配，限制随机工作量以覆盖调度器不变量。
		if len(data) > 128 {
			t.Skip()
		}
		previousEnd := uint64(0)
		matches, err := db.Scan(data)
		for _, match := range matches {
			if match.From >= match.To || match.To > uint64(len(data)) {
				t.Fatalf("invalid range [%d,%d) for %d bytes", match.From, match.To, len(data))
			}
			if match.To < previousEnd {
				t.Fatalf("match end moved backward from %d to %d", previousEnd, match.To)
			}
			previousEnd = match.To
		}
		if err != nil {
			t.Fatal(err)
		}
	})
}

func FuzzScanAndScanIntoReportSameEvents(f *testing.F) {
	f.Add([]byte("foo id-12"))
	f.Add([]byte(""))
	f.Add([]byte{0xff, 'f', 'o', 'o'})

	db, err := scankit.Compile([]scankit.Expression{
		{Id: 1, Pattern: "foo"},
		{Id: 2, Pattern: `id-[0-9]{2,4}`},
	})
	if err != nil {
		f.Fatal(err)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 4_096 {
			t.Skip()
		}
		want, err := db.Scan(data)
		if err != nil {
			t.Fatal(err)
		}
		got, err := db.ScanInto(data, nil)
		if err != nil {
			t.Fatal(err)
		}
		assertMatchesEqual(t, got, want)
	})
}

func FuzzScanFlagRulesReportsValidEvents(f *testing.F) {
	db, err := scankit.Compile([]scankit.Expression{
		{Id: 1, Pattern: `a.b`, Flags: scankit.CompileDotAll},
		{Id: 2, Pattern: `^token$`, Flags: scankit.CompileMultiline | scankit.CompileSingleMatch},
		{Id: 3, Pattern: "secret", Flags: scankit.CompileQuiet},
	})
	if err != nil {
		f.Fatal(err)
	}
	f.Add([]byte("a\nb\ntoken\nsecret"))
	f.Add([]byte(""))
	f.Add([]byte{0xff, '\n', 't', 'o', 'k', 'e', 'n', '\n'})

	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 4_096 {
			t.Skip()
		}
		seenSingle := 0
		matches, err := db.Scan(data)
		for _, match := range matches {
			if match.From >= match.To || match.To > uint64(len(data)) {
				t.Fatalf("invalid range [%d,%d) for %d bytes", match.From, match.To, len(data))
			}
			if match.Id == 3 {
				t.Fatal("quiet expression produced a match event")
			}
			if match.Id == 2 {
				seenSingle++
				if seenSingle > 1 {
					t.Fatal("single-match expression produced multiple events")
				}
			}
		}
		if err != nil {
			t.Fatal(err)
		}
	})
}

func FuzzScanWordBoundaryAssertionsMatchGoRegexp(f *testing.F) {
	f.Add([]byte("token _token_ atokenb token!"))
	f.Add([]byte(""))
	f.Add([]byte{0xff, 't', 'o', 'k', 'e', 'n', '_'})

	patterns := []string{`\btoken\b`, `\Btoken\B`}
	db, err := scankit.Compile([]scankit.Expression{{Id: 1, Pattern: patterns[0]}, {Id: 2, Pattern: patterns[1]}})
	if err != nil {
		f.Fatal(err)
	}
	goRegexps := []*regexp.Regexp{regexp.MustCompile(patterns[0]), regexp.MustCompile(patterns[1])}
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 4_096 {
			t.Skip()
		}
		matches, err := db.Scan(data)
		if err != nil {
			t.Fatal(err)
		}
		got := [2][][2]int{}
		for _, match := range matches {
			if match.Id < 1 || match.Id > 2 || match.From >= match.To || match.To > uint64(len(data)) {
				t.Fatalf("invalid match %#v for %d bytes", match, len(data))
			}
			got[match.Id-1] = append(got[match.Id-1], [2]int{int(match.From), int(match.To)})
		}
		for index, goRegexp := range goRegexps {
			assertRangesEqual(t, got[index], goRegexp.FindAllIndex(data, -1))
		}
	})
}

func FuzzScanHexEscapesAndPOSIXClassesMatchGoRegexp(f *testing.F) {
	f.Add([]byte("ID:42 A-1F"))
	f.Add([]byte(""))
	f.Add([]byte{0xff, 'I', 'D', ':', '0', '1'})

	patterns := []string{`ID\x3A[[:digit:]]{2}`, `[[:alpha:]]\x2D[[:xdigit:]]{2}`}
	db, err := scankit.Compile([]scankit.Expression{{Id: 1, Pattern: patterns[0]}, {Id: 2, Pattern: patterns[1]}})
	if err != nil {
		f.Fatal(err)
	}
	goRegexps := []*regexp.Regexp{regexp.MustCompile(patterns[0]), regexp.MustCompile(patterns[1])}
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 4_096 {
			t.Skip()
		}
		matches, err := db.Scan(data)
		if err != nil {
			t.Fatal(err)
		}
		got := [2][][2]int{}
		for _, match := range matches {
			if match.Id < 1 || match.Id > 2 || match.From >= match.To || match.To > uint64(len(data)) {
				t.Fatalf("invalid match %#v for %d bytes", match, len(data))
			}
			got[match.Id-1] = append(got[match.Id-1], [2]int{int(match.From), int(match.To)})
		}
		for index, goRegexp := range goRegexps {
			assertRangesEqual(t, got[index], findAllOverlapping(goRegexp, data))
		}
	})
}

func FuzzScanAbsoluteAnchorsMatchGoRegexp(f *testing.F) {
	f.Add([]byte("ID:42"))
	f.Add([]byte("ID:42\n"))
	f.Add([]byte(""))

	pattern := `\AID:\d{2}\z`
	db, err := scankit.Compile([]scankit.Expression{{Id: 1, Pattern: pattern, Flags: scankit.CompileMultiline}})
	if err != nil {
		f.Fatal(err)
	}
	goRegexp := regexp.MustCompile(pattern)
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 4_096 {
			t.Skip()
		}
		matches, err := db.Scan(data)
		if err != nil {
			t.Fatal(err)
		}
		got := make([][2]int, len(matches))
		for index, match := range matches {
			if match.Id != 1 || match.From >= match.To || match.To > uint64(len(data)) {
				t.Fatalf("invalid match %#v for %d bytes", match, len(data))
			}
			got[index] = [2]int{int(match.From), int(match.To)}
		}
		assertRangesEqual(t, got, goRegexp.FindAllIndex(data, -1))
	})
}

func FuzzScanEndBeforeFinalNewlineAnchor(f *testing.F) {
	for _, seed := range [][]byte{[]byte("token"), []byte("token\n"), []byte("token\r\n"), []byte("token\nnext"), []byte("other")} {
		f.Add(seed)
	}
	database, err := scankit.Compile([]scankit.Expression{{Id: 1, Pattern: `\Atoken\Z`}})
	if err != nil {
		f.Fatal(err)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 4_096 {
			t.Skip()
		}
		matches, err := database.Scan(data)
		if err != nil {
			t.Fatal(err)
		}
		want := bytes.Equal(data, []byte("token")) || bytes.Equal(data, []byte("token\n")) || bytes.Equal(data, []byte("token\r\n"))
		if want {
			assertMatchesEqual(t, matches, []scankit.Match{{Id: 1, From: 0, To: 5}})
			return
		}
		assertMatchesEqual(t, matches, nil)
	})
}

func FuzzScanInlineScopedFlagsScanIntoMatchesScan(f *testing.F) {
	f.Add([]byte("TOKEN a\nb tokenVALUE\n"))
	f.Add([]byte{0xff, 'T', 'O', 'K', 'E', 'N', '\n'})
	expressions := []scankit.Expression{
		{Id: 1, Pattern: `(?i:token)`},
		{Id: 2, Pattern: `(?s:a.b)`},
		{Id: 3, Pattern: `(?m:^token$)`},
		{Id: 4, Pattern: "(?x) token # comment\n VALUE"},
	}
	database, err := scankit.Compile(expressions)
	if err != nil {
		f.Fatal(err)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 4_096 {
			t.Skip()
		}
		want, err := database.Scan(data)
		if err != nil {
			t.Fatal(err)
		}
		got, err := database.ScanInto(data, make([]scankit.Match, 0, len(want)))
		if err != nil {
			t.Fatal(err)
		}
		assertMatchesEqual(t, got, want)
	})
}

func FuzzScanUCPInlineScopedFlagsScanIntoMatchesScan(f *testing.F) {
	f.Add([]byte("Σ σ\n甲\n乙\n"))
	f.Add([]byte("用户 用户\n"))
	expressions := []scankit.Expression{
		{Id: 1, Pattern: `(?i:σ)`, Flags: scankit.CompileUTF8 | scankit.CompileUnicodeProperties},
		{Id: 2, Pattern: `甲(?s:.)乙`, Flags: scankit.CompileUTF8 | scankit.CompileUnicodeProperties},
		{Id: 3, Pattern: `(?m:^用户$)`, Flags: scankit.CompileUTF8 | scankit.CompileUnicodeProperties},
		{Id: 4, Pattern: "(?x: 用 # note\n 户)", Flags: scankit.CompileUTF8 | scankit.CompileUnicodeProperties},
		{Id: 5, Pattern: `(?<word>\Q用户\E)\h\v\W`, Flags: scankit.CompileUTF8 | scankit.CompileUnicodeProperties},
	}
	database, err := scankit.Compile(expressions)
	if err != nil {
		f.Fatal(err)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 4_096 || !utf8.Valid(data) {
			t.Skip()
		}
		want, err := database.Scan(data)
		if err != nil {
			t.Fatal(err)
		}
		got, err := database.ScanInto(data, make([]scankit.Match, 0, len(want)))
		if err != nil {
			t.Fatal(err)
		}
		assertMatchesEqual(t, got, want)
	})
}

func FuzzScanExtensionsRespectConstraints(f *testing.F) {
	f.Add([]byte("a1234567890"))
	f.Add([]byte(""))
	f.Add([]byte{0xff, '1', '2', '3'})

	ext := &scankit.ExpressionExt{Flags: scankit.ExtMinOffset | scankit.ExtMaxOffset | scankit.ExtMinLength, MinOffset: 2, MaxOffset: 32, MinLength: 2}
	db, err := scankit.Compile([]scankit.Expression{{Id: 1, Pattern: `\d+`, Ext: ext}})
	if err != nil {
		f.Fatal(err)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 128 {
			t.Skip()
		}
		matches, err := db.Scan(data)
		if err != nil {
			t.Fatal(err)
		}
		for _, match := range matches {
			if match.Id != 1 || match.To < ext.MinOffset || match.To > ext.MaxOffset || match.To-match.From < ext.MinLength || match.To > uint64(len(data)) {
				t.Fatalf("match %#v violates %#v for %d bytes", match, ext, len(data))
			}
		}
	})
}

func FuzzScanCombinationProducesAtMostOneRisingEdge(f *testing.F) {
	f.Add([]byte("token secret"))
	f.Add([]byte("secret token"))
	f.Add([]byte(""))

	db, err := scankit.Compile([]scankit.Expression{
		{Id: 1, Pattern: "token", Flags: scankit.CompileQuiet},
		{Id: 2, Pattern: "secret", Flags: scankit.CompileQuiet},
		{Id: 3, Pattern: "1&2", Flags: scankit.CompileCombination},
	})
	if err != nil {
		f.Fatal(err)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 4_096 {
			t.Skip()
		}
		matches, err := db.Scan(data)
		if err != nil {
			t.Fatal(err)
		}
		if len(matches) > 1 {
			t.Fatalf("combination match count = %d, want at most one", len(matches))
		}
		for _, match := range matches {
			if match.Id != 3 || match.From != 0 || match.To > uint64(len(data)) {
				t.Fatalf("invalid combination match %#v for %d bytes", match, len(data))
			}
		}
	})
}

func assertMatchesEqual(t *testing.T, got, want []scankit.Match) {
	t.Helper()
	want = oneMatchPerExpressionEnd(want)
	if len(got) != len(want) {
		t.Fatalf("match length = %d, want %d; got = %#v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("match %d = %#v, want %#v", i, got[i], want[i])
		}
	}
}

// oneMatchPerExpressionEnd 将旧的全区间夹具转换为公开结果列表契约：每条表达式在每个结束偏移
// 只有一个匹配，并保留最小起点作为确定性的 From 值。
func oneMatchPerExpressionEnd(matches []scankit.Match) []scankit.Match {
	type key struct {
		id uint32
		to uint64
	}
	result := make([]scankit.Match, 0, len(matches))
	indexes := make(map[key]int, len(matches))
	for _, match := range matches {
		matchKey := key{id: match.Id, to: match.To}
		if index, ok := indexes[matchKey]; ok {
			if match.From < result[index].From {
				result[index] = match
			}
			continue
		}
		indexes[matchKey] = len(result)
		result = append(result, match)
	}
	return result
}
