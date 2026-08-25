package scankit

import (
	"reflect"
	"testing"
)

func TestNFAMatchFromReportsExpectedEnds(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		pattern string
		data    string
		start   int
		want    []int
	}{
		{name: "alternation", pattern: "foo|bar", data: "foobar", start: 0, want: []int{3}},
		{name: "unbounded repeat", pattern: "a+", data: "baaa", start: 1, want: []int{2, 3, 4}},
		{name: "bounded repeat", pattern: "\\d{2,4}", data: "12345", start: 0, want: []int{2, 3, 4}},
		{name: "start and end anchor", pattern: "^ab$", data: "ab", start: 0, want: []int{2}},
		{name: "absolute anchors", pattern: `\Aab\z`, data: "ab", start: 0, want: []int{2}},
		{name: "absolute anchors reject multiline interior", pattern: `\Aab\z`, data: "ab\nab", start: 3, want: nil},
		{name: "start anchor rejects nonzero offset", pattern: "^ab", data: "zab", start: 1, want: nil},
		{name: "end anchor rejects trailing data", pattern: "ab$", data: "abc", start: 0, want: nil},
		{name: "word boundary", pattern: `\btoken\b`, data: " token!", start: 1, want: []int{6}},
		{name: "non word boundary", pattern: `\Btoken\B`, data: "_token_", start: 1, want: []int{6}},
		{name: "class", pattern: "[A-C]+", data: "ABCz", start: 0, want: []int{1, 2, 3}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root, err := parseRegex(tt.pattern)
			if err != nil {
				t.Fatal(err)
			}
			program, err := compileNFA(root)
			if err != nil {
				t.Fatal(err)
			}
			if got := nfaMatchFrom(program, []byte(tt.data), tt.start); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("nfaMatchFrom(%q, %q, %d) = %v, want %v", tt.pattern, tt.data, tt.start, got, tt.want)
			}
		})
	}
}

func TestNFAMatchFromLimitReturnsOnlyReachableBoundedEnds(t *testing.T) {
	t.Parallel()
	root, err := parseRegex(`\d{2,4}`)
	if err != nil {
		t.Fatal(err)
	}
	program, err := compileNFA(root)
	if err != nil {
		t.Fatal(err)
	}
	context := newNFAVerifierContext(program)
	got := context.matchFromLimit(program, []byte("12345"), 0, 3)
	if want := []int{2, 3}; !reflect.DeepEqual(got, want) {
		t.Fatalf("matchFromLimit() = %v, want %v", got, want)
	}
}

func FuzzNFAMatchFrom(f *testing.F) {
	for _, seed := range []struct {
		pattern string
		data    string
		start   int
		limit   int
	}{
		{"a+", "baaa", 1, 2},
		{"^ab$", "ab", 0, 2},
		{"[a-z]{1,4}", "token", 0, 3},
		{"foo|bar", "foobar", 0, 3},
		{`\btoken\b`, " token!", 1, 5},
	} {
		f.Add(seed.pattern, []byte(seed.data), seed.start, seed.limit)
	}
	f.Fuzz(func(t *testing.T, pattern string, data []byte, start, limit int) {
		if len(pattern) > 512 || len(data) > 512 {
			t.Skip()
		}
		root, err := parseRegex(pattern)
		if err != nil {
			return
		}
		program, err := compileNFA(root)
		if err != nil {
			return
		}
		if start < 0 {
			start = -start
		}
		if len(data) != 0 {
			start %= len(data) + 1
		}
		ends := nfaMatchFrom(program, data, start)
		if limit < 0 {
			limit = -limit
		}
		if limit > len(data)-start {
			limit = len(data) - start
		}
		limited := newNFAVerifierContext(program).matchFromLimit(program, data, start, limit)
		wantLimited := make([]int, 0, len(ends))
		for _, end := range ends {
			if end <= start+limit {
				wantLimited = append(wantLimited, end)
			}
		}
		if len(limited) != len(wantLimited) {
			t.Fatalf("limited ends = %v, want %v", limited, wantLimited)
		}
		for index := range limited {
			if limited[index] != wantLimited[index] {
				t.Fatalf("limited ends = %v, want %v", limited, wantLimited)
			}
		}
		previous := start
		for _, end := range ends {
			if end <= previous || end > len(data) {
				t.Fatalf("invalid match end %d for start %d and input length %d", end, start, len(data))
			}
			previous = end
		}
	})
}
