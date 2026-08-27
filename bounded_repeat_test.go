package scankit

import (
	"sort"
	"testing"
)

func TestBoundedRepeatScanMatchesRegexp(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		pattern string
		data    string
	}{
		{`[A-Z]{2,3}[0-9]`, `A1 AB2 ABC3 ABCD4`},
		{`[A-Z][0-9]{2,3}[A-Z]`, `A12X B123Y C1234Z`},
		{`[0-9]{2,3}[A-Z][a-z]`, `12Ab 123Cd 1234Ef`},
		{`[0].{1,2}`, `0a 0ab 0abc`},
	} {
		test := test
		t.Run(test.pattern, func(t *testing.T) {
			scanner, err := Compile([]Expression{{Id: 1, Pattern: test.pattern}})
			if err != nil {
				t.Fatal(err)
			}
			if !scanner.blockScanPlan.unanchored.hasBoundedRepeatLanes() {
				t.Fatal("bounded repeat lane was not selected")
			}
			data := []byte(test.data)
			got, err := scanner.Scan(data)
			if err != nil {
				t.Fatal(err)
			}
			root, err := parseRegexWithFlags(test.pattern, 0)
			if err != nil {
				t.Fatal(err)
			}
			applyRegexFlags(root, 0)
			program, err := compileNFA(root)
			if err != nil {
				t.Fatal(err)
			}
			reference := normalizeReferenceEvents(scanWithNFAScheduler(program, data))
			if len(got) != len(reference) {
				t.Fatalf("matches=%#v reference=%#v", got, reference)
			}
			for index := range got {
				if got[index] != reference[index] {
					t.Fatalf("match %d = %#v, reference=%#v", index, got[index], reference[index])
				}
			}
		})
	}
}

func TestBoundedRepeatPreservesAllEndsAndFlags(t *testing.T) {
	t.Parallel()
	scanner, err := Compile([]Expression{
		{Id: 1, Pattern: `[A-Z][0-9]{1,3}`},
		{Id: 2, Pattern: `[A-Z][0-9]{1,3}`, Flags: CompileLeftmostStart},
		{Id: 3, Pattern: `[A-Z][0-9]{1,3}`, Flags: CompileSingleMatch},
	})
	if err != nil {
		t.Fatal(err)
	}
	data := []byte(`A123 A45`)
	got, err := scanner.Scan(data)
	if err != nil {
		t.Fatal(err)
	}
	want := []Match{
		{Id: 1, From: 0, To: 2}, {Id: 2, From: 0, To: 2}, {Id: 3, From: 0, To: 2},
		{Id: 1, From: 0, To: 3}, {Id: 2, From: 0, To: 3},
		{Id: 1, From: 0, To: 4}, {Id: 2, From: 0, To: 4},
		{Id: 1, From: 5, To: 7}, {Id: 2, From: 5, To: 7},
		{Id: 1, From: 5, To: 8}, {Id: 2, From: 5, To: 8},
	}
	if len(got) != len(want) {
		t.Fatalf("matches = %#v, want %#v", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("match %d = %#v, want %#v", index, got[index], want[index])
		}
	}
}

func FuzzBoundedRepeatScanInto(f *testing.F) {
	for _, seed := range []string{`[A-Z]{2,3}[0-9]`, `[A-Z][0-9]{2,3}[A-Z]`, `[0-9]{2,3}[A-Z][a-z]`} {
		f.Add(seed, []byte("AB2 A12X 123Cd"))
	}
	f.Fuzz(func(t *testing.T, pattern string, data []byte) {
		if len(data) > 512 {
			t.Skip()
		}
		scanner, err := Compile([]Expression{{Id: 1, Pattern: pattern}})
		if err != nil || !scanner.blockScanPlan.unanchored.hasBoundedRepeatLanes() {
			t.Skip()
		}
		got, err := scanner.ScanInto(data, nil)
		if err != nil {
			t.Fatal(err)
		}
		root, err := parseRegexWithFlags(pattern, 0)
		if err != nil {
			t.Fatal(err)
		}
		applyRegexFlags(root, 0)
		program, err := compileNFA(root)
		if err != nil {
			t.Fatal(err)
		}
		reference := normalizeReferenceEvents(scanWithNFAScheduler(program, data))
		if len(got) != len(reference) {
			t.Fatalf("pattern=%q got=%#v reference=%#v", pattern, got, reference)
		}
		for index := range got {
			if got[index] != reference[index] {
				t.Fatalf("pattern=%q match %d = %#v reference=%#v", pattern, index, got[index], reference[index])
			}
		}
	})
}

func scanWithNFAScheduler(program nfaProgram, data []byte) []Match {
	runner := newNFASchedulerContext(program, false)
	matches := make([]Match, 0)
	for offset, value := range data {
		for _, thread := range runner.advance(program, value, data, offset, uint64(offset)) {
			matches = append(matches, Match{Id: 1, From: thread.start, To: uint64(offset + 1)})
		}
	}
	return matches
}

func normalizeReferenceEvents(matches []Match) []Match {
	sort.Slice(matches, func(left, right int) bool {
		if matches[left].To != matches[right].To {
			return matches[left].To < matches[right].To
		}
		return matches[left].From < matches[right].From
	})
	result := matches[:0]
	for _, match := range matches {
		if len(result) != 0 && result[len(result)-1].To == match.To {
			continue
		}
		result = append(result, match)
	}
	return result
}
