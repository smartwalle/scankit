package scankit

import (
	"reflect"
	"testing"
)

func TestUnanchoredDFASchedulerMatchesNFA(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name    string
		pattern string
		data    string
	}{
		{name: "alternation", pattern: `(?:ab|ac)\d?`, data: "zabac2ab3"},
		{name: "bounded repeat", pattern: `\d{2,4}`, data: "a12345b67"},
		{name: "class and suffix", pattern: `[A-C]+[0-9]`, data: "A1BB2C3"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			program := compileUnanchoredDFATestProgram(t, tt.pattern)
			runner := newNFASchedulerContext(program, false)
			if runner.dfa == nil {
				t.Fatal("small assertion-free program did not select DFA scheduler")
			}
			assertUnanchoredDFASchedulerMatchesNFA(t, program, runner, []byte(tt.data))
		})
	}
}

func TestUnanchoredDFASchedulerLeavesLeftmostOnNFAScheduler(t *testing.T) {
	t.Parallel()
	program := compileUnanchoredDFATestProgram(t, `\d{2,4}`)
	runner := newNFASchedulerContext(program, true)
	if runner.dfa != nil || runner.leftmost == nil {
		t.Fatal("SOM_LEFTMOST must retain its start-merging NFA scheduler")
	}
}

func TestDatabaseUsesDFASchedulerForNonSimpleUnanchoredRegex(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		expressions []Expression
		data        []byte
		want        []Match
	}{
		{
			name:        "byte scan",
			expressions: []Expression{{Id: 1, Pattern: `\d\w{1,2}`}},
			data:        []byte("a1bc2D"),
			want:        []Match{{Id: 1, From: 1, To: 3}, {Id: 1, From: 1, To: 4}, {Id: 1, From: 4, To: 6}},
		},
		{
			name: "UCP mixed scan",
			expressions: []Expression{
				{Id: 1, Pattern: `\p{Han}+`, Flags: CompileUTF8 | CompileUnicodeProperties},
				{Id: 2, Pattern: `\d\w{1,2}`},
			},
			data: []byte("张1bc"),
			want: []Match{{Id: 1, From: 0, To: 3}, {Id: 2, From: 3, To: 5}, {Id: 2, From: 3, To: 6}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db, err := Compile(tt.expressions)
			if err != nil {
				t.Fatal(err)
			}
			ctx := db.newContext()
			representative := db.unanchoredGroups[0][0]
			if ctx.regexRunners[representative] == nil || ctx.regexRunners[representative].dfa == nil {
				t.Fatal("database did not select DFA scheduler")
			}
			got, err := db.Scan(tt.data)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("Scan() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestFixedByteRegexMatchesNFA(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name    string
		pattern string
		data    []byte
	}{
		{name: "fixed digit width", pattern: `\d{2}`, data: []byte("a1234")},
		{name: "class alternation", pattern: `(?:[1-2]A|[1-2]B)\d`, data: []byte("x1A2 2B3 1A4")},
		{name: "overlapping alternatives", pattern: `(?:[1-2]A|[1-2]A)\d`, data: []byte("1A2")},
	} {
		t.Run(test.name, func(t *testing.T) {
			root, err := parseRegex(test.pattern)
			if err != nil {
				t.Fatal(err)
			}
			fixed, ok := extractFixedByteRegex(root)
			if !ok {
				t.Fatal("extractFixedByteRegex() did not select fixed executor")
			}
			program, err := compileNFA(root)
			if err != nil {
				t.Fatal(err)
			}
			assertFixedByteRegexMatchesNFA(t, fixed, program, test.data)
			if anchor, ok := fixedByteRegexClassAnchor(fixed.sequences); ok {
				assertFixedByteRegexAnchorMatchesNFA(t, anchor, program, test.data)
			}
		})
	}
}

func FuzzUnanchoredDFASchedulerMatchesNFA(f *testing.F) {
	for _, seed := range [][]byte{[]byte("zabac2ab3"), []byte("12345"), []byte(""), {0xff, 'A', '1'}} {
		f.Add(seed)
	}
	programs := []nfaProgram{
		compileUnanchoredDFATestProgram(f, `(?:ab|ac)\d?`),
		compileUnanchoredDFATestProgram(f, `\d{2,4}`),
		compileUnanchoredDFATestProgram(f, `[A-C]+[0-9]`),
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 512 {
			t.Skip()
		}
		for _, program := range programs {
			assertUnanchoredDFASchedulerMatchesNFA(t, program, newNFASchedulerContext(program, false), data)
		}
	})
}

func FuzzFixedByteRegexMatchesNFA(f *testing.F) {
	for _, seed := range [][]byte{[]byte("1234"), []byte("1A2 2B3"), []byte(""), {0xff, '1', 'A', '2'}} {
		f.Add(seed)
	}
	patterns := []string{`\d{2}`, `(?:[1-2]A|[1-2]B)\d`, `(?:[1-2]A|[1-2]A)\d`}
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 512 {
			t.Skip()
		}
		for _, pattern := range patterns {
			root, err := parseRegex(pattern)
			if err != nil {
				t.Fatal(err)
			}
			fixed, ok := extractFixedByteRegex(root)
			if !ok {
				t.Fatal("extractFixedByteRegex() did not select fixed executor")
			}
			program, err := compileNFA(root)
			if err != nil {
				t.Fatal(err)
			}
			assertFixedByteRegexMatchesNFA(t, fixed, program, data)
			if anchor, ok := fixedByteRegexClassAnchor(fixed.sequences); ok {
				assertFixedByteRegexAnchorMatchesNFA(t, anchor, program, data)
			}
		}
	})
}

func FuzzFixedByteRegexAnchorMatchesNFA(f *testing.F) {
	for _, seed := range [][]byte{[]byte("abcdefgh cdabefgh"), []byte(""), {0xff, 'a', 'b', 'c', 'd'}} {
		f.Add(seed)
	}
	patterns := []string{
		`(?:ab|cd|ef|gh){4}`,
		`(?:[1-2]A|[3-4]B|[5-6]C){3}`,
		`(?:(?:ab|cd){2}|(?:ef|gh){2}){3}`,
		`(?:[A-C](?:12|34)|[D-F](?:56|78)){3}`,
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 512 {
			t.Skip()
		}
		for _, pattern := range patterns {
			root, err := parseRegex(pattern)
			if err != nil {
				t.Fatal(err)
			}
			anchor, ok := extractFixedByteRegexAnchor(root)
			if !ok {
				t.Fatalf("extractFixedByteRegexAnchor(%q) did not select an anchor", pattern)
			}
			program, err := compileNFA(root)
			if err != nil {
				t.Fatal(err)
			}
			assertFixedByteRegexAnchorMatchesNFA(t, anchor, program, data)
		}
	})
}

type nfaTestHelper interface {
	Helper()
	Fatal(...any)
}

func compileUnanchoredDFATestProgram(t nfaTestHelper, pattern string) nfaProgram {
	t.Helper()
	root, err := parseRegex(pattern)
	if err != nil {
		t.Fatal(err)
	}
	program, err := compileNFA(root)
	if err != nil {
		t.Fatal(err)
	}
	if program.verifierDFA == nil {
		t.Fatal("test program did not build a verifier DFA")
	}
	return program
}

func assertUnanchoredDFASchedulerMatchesNFA(t *testing.T, program nfaProgram, runner *nfaSchedulerContext, data []byte) {
	t.Helper()
	for offset, value := range data {
		gotThreads := runner.advance(program, value, data, offset, uint64(offset))
		got := make([]uint64, len(gotThreads))
		for index, thread := range gotThreads {
			got[index] = thread.start
		}
		want := unanchoredNFAStartsAtEnd(program, data, offset+1)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("end %d starts = %v, want %v for %q", offset+1, got, want, data)
		}
	}
}

func unanchoredNFAStartsAtEnd(program nfaProgram, data []byte, end int) []uint64 {
	starts := make([]uint64, 0)
	for start := 0; start < end; start++ {
		for _, matchEnd := range nfaMatchFrom(program, data, start) {
			if matchEnd == end {
				starts = append(starts, uint64(start))
			}
		}
	}
	return starts
}

func assertFixedByteRegexMatchesNFA(t testing.TB, fixed *fixedByteRegex, program nfaProgram, data []byte) {
	t.Helper()
	run := fixedByteRegexRun{}
	got := make(map[[2]int]struct{})
	for offset := range data {
		for _, match := range run.advance(fixed, data, offset) {
			got[[2]int{match.start, match.end}] = struct{}{}
		}
	}
	want := make(map[[2]int]struct{})
	for start := range data {
		for _, end := range nfaMatchFrom(program, data, start) {
			want[[2]int{start, end}] = struct{}{}
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("fixed matches = %v, want NFA matches %v for %q", got, want, data)
	}
}

func assertFixedByteRegexAnchorMatchesNFA(t testing.TB, anchor fixedByteRegexAnchor, program nfaProgram, data []byte) {
	t.Helper()
	verifier := newNFAVerifierContext(program)
	got := make(map[[2]int]struct{})
	for offset, value := range data {
		if !anchor.class.contains(value) {
			continue
		}
		start := offset - anchor.offset
		if start < 0 || start+anchor.width > len(data) {
			continue
		}
		if anchor.checks != nil && !fixedByteRegexAnchorChecksMatch(data, start, anchor.checks) {
			continue
		}
		for _, end := range verifier.matchFromLimit(program, data, start, anchor.width) {
			got[[2]int{start, end}] = struct{}{}
		}
	}
	want := make(map[[2]int]struct{})
	for start := range data {
		for _, end := range nfaMatchFrom(program, data, start) {
			want[[2]int{start, end}] = struct{}{}
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("fixed anchor matches = %v, want NFA matches %v for %q", got, want, data)
	}
}
