package scankit

import (
	"testing"

	"github.com/smartwalle/scankit/internal/parser"
)

func TestLookaroundAndBoundaries(t *testing.T) {
	cases := []struct {
		p, s string
		n    int
	}{{`foo(?=bar)`, "foobar foox", 1}, {`foo(?!bar)`, "foobar foox", 1}, {`(?<=foo)bar`, "foobar xxbar", 1}, {`(?<!foo)bar`, "foobar xxbar", 1}, {`^a$`, "a", 1}}
	for _, c := range cases {
		s, e := Compile([]Expression{{Id: 1, Pattern: c.p}})
		if e != nil {
			t.Fatal(c.p, e)
		}
		m, e := s.Scan([]byte(c.s))
		if e != nil || len(m) != c.n {
			t.Fatalf("%s: %v %#v", c.p, e, m)
		}
	}
}

func TestLookaroundReportsConsumedSpanOnly(t *testing.T) {
	s, err := Compile([]Expression{{Id: 1, Pattern: `foo(?=bar)`}})
	if err != nil {
		t.Fatal(err)
	}
	matches, err := s.Scan([]byte("foobar"))
	if err != nil || len(matches) != 1 || matches[0] != (Match{Id: 1, From: 0, To: 3}) {
		t.Fatalf("lookaround span mismatch: %v %#v", err, matches)
	}
}

func TestUTF8LookbehindUsesByteOffsets(t *testing.T) {
	s, err := Compile([]Expression{{Id: 1, Pattern: `(?<=\p{L})x`, Flags: CompileUTF8 | CompileUCP}})
	if err != nil {
		t.Fatal(err)
	}
	matches, err := s.Scan([]byte("中文x"))
	if err != nil || len(matches) != 1 || matches[0] != (Match{Id: 1, From: 6, To: 7}) {
		t.Fatalf("utf8 lookbehind mismatch: %v %#v", err, matches)
	}
}

func TestUTF8LiteralRejectsInvalidDataAndContinuationStart(t *testing.T) {
	s, err := Compile([]Expression{{Id: 1, Pattern: "é", Flags: CompileUTF8}})
	if err != nil {
		t.Fatal(err)
	}
	if got, err := s.Scan([]byte("é")); err != nil || len(got) != 1 {
		t.Fatalf("valid UTF-8=%#v %v", got, err)
	}
	data := []byte("é")
	if got := matchNode(parser.Literal{Value: []byte("é")}, data, 1, CompileUTF8); len(got) != 0 {
		t.Fatalf("续字节起点=%v", got)
	}
	if got, err := s.Scan([]byte{0xff, 'x'}); err != nil || len(got) != 0 {
		t.Fatalf("非法 UTF-8=%#v %v", got, err)
	}
}

func TestEndBeforeFinalNewline(t *testing.T) {
	for _, input := range []string{"a", "a\n", "a\r\n"} {
		s, err := Compile([]Expression{{Id: 1, Pattern: `a\Z`}})
		if err != nil {
			t.Fatal(err)
		}
		matches, err := s.Scan([]byte(input))
		if err != nil || len(matches) != 1 || matches[0] != (Match{Id: 1, From: 0, To: 1}) {
			t.Fatalf("input %q: %v %#v", input, err, matches)
		}
	}
}

func TestLookaroundVariableWidthAndWordBoundaryNegation(t *testing.T) {
	cases := []struct {
		pattern string
		input   string
		want    []Match
	}{
		{`(?<!ab)z`, "abz axz z", []Match{{Id: 1, From: 6, To: 7}, {Id: 1, From: 8, To: 9}}},
		{`\Bcat\B`, "scat cat scatter", []Match{{Id: 1, From: 10, To: 13}}},
		{`foo(?!bar|baz)`, "foobat foobar foobaz", []Match{{Id: 1, From: 0, To: 3}}},
	}
	for _, tc := range cases {
		scanner, err := Compile([]Expression{{Id: 1, Pattern: tc.pattern}})
		if err != nil {
			t.Fatalf("compile %q: %v", tc.pattern, err)
		}
		got, err := scanner.Scan([]byte(tc.input))
		if err != nil || len(got) != len(tc.want) {
			t.Fatalf("pattern %q: err=%v got=%#v want=%#v", tc.pattern, err, got, tc.want)
		}
		for i := range tc.want {
			if got[i] != tc.want[i] {
				t.Fatalf("pattern %q: got=%#v want=%#v", tc.pattern, got, tc.want)
			}
		}
	}
}

func TestSOMLeftmostKeepsEarliestStartPerEnd(t *testing.T) {
	scanner, err := Compile([]Expression{{Id: 1, Pattern: `a*`, Flags: CompileAllowEmpty | CompileSOMLeftmost}})
	if err != nil {
		t.Fatal(err)
	}
	matches, err := scanner.Scan([]byte("a"))
	if err != nil {
		t.Fatal(err)
	}
	for _, match := range matches {
		if match.To == 1 && match.From != 0 {
			t.Fatalf("SOM 未保留最左起点: %#v", matches)
		}
	}
}
