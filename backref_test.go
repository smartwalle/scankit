package scankit

import "testing"

func TestBackreference(t *testing.T) {
	s, err := Compile([]Expression{{Id: 1, Pattern: `(ab)\1`}})
	if err != nil {
		t.Fatal(err)
	}
	m, err := s.Scan([]byte("xxabab"))
	if err != nil || len(m) != 1 || m[0].From != 2 || m[0].To != 6 {
		t.Fatalf("%v %#v", err, m)
	}
}

func TestNamedBackreference(t *testing.T) {
	s, err := Compile([]Expression{{Id: 1, Pattern: `(?<part>ab)\k<part>`}})
	if err != nil {
		t.Fatal(err)
	}
	matches, err := s.Scan([]byte("abab abac"))
	if err != nil || len(matches) != 1 || matches[0] != (Match{Id: 1, From: 0, To: 4}) {
		t.Fatalf("named backreference mismatch: %v %#v", err, matches)
	}
}

func TestNonGreedyRepeat(t *testing.T) {
	s, err := Compile([]Expression{{Id: 1, Pattern: `a+?`}})
	if err != nil {
		t.Fatal(err)
	}
	m, err := s.Scan([]byte("aaa"))
	if err != nil || len(m) != 3 || m[0].To-m[0].From != 1 {
		t.Fatalf("%v %#v", err, m)
	}
}

func TestNestedCaptureBackreference(t *testing.T) {
	s, err := Compile([]Expression{{Id: 1, Pattern: `((a)b)\1`}})
	if err != nil {
		t.Fatal(err)
	}
	matches, err := s.Scan([]byte("abab abba"))
	if err != nil || len(matches) != 1 || matches[0].From != 0 || matches[0].To != 4 {
		t.Fatalf("nested capture mismatch: %v %#v", err, matches)
	}
}

func TestCapturedAlternationBackreference(t *testing.T) {
	s, err := Compile([]Expression{{Id: 1, Pattern: `((a|b))\1`}})
	if err != nil {
		t.Fatal(err)
	}
	matches, err := s.Scan([]byte("aa bb ab"))
	if err != nil || len(matches) != 2 {
		t.Fatalf("captured alternation mismatch: %v %#v", err, matches)
	}
}

func TestBackreferenceUsesCaselessComparison(t *testing.T) {
	s, err := Compile([]Expression{{Id: 1, Pattern: `(ab)\1`, Flags: CompileCaseless}})
	if err != nil {
		t.Fatal(err)
	}
	matches, err := s.Scan([]byte("aBAB abAc"))
	if err != nil || len(matches) != 1 || matches[0] != (Match{Id: 1, From: 0, To: 4}) {
		t.Fatalf("caseless backreference mismatch: %v %#v", err, matches)
	}
}

func TestRepeatedCaptureClearsNonParticipatingInnerGroup(t *testing.T) {
	s, err := Compile([]Expression{{Id: 1, Pattern: `(?:(a)|(b))+\2`}})
	if err != nil {
		t.Fatal(err)
	}
	matches, err := s.Scan([]byte("bab bbb"))
	if err != nil || len(matches) != 1 || matches[0] != (Match{Id: 1, From: 4, To: 7}) {
		t.Fatalf("repeated capture mismatch: %v %#v", err, matches)
	}
}

func TestPositiveLookaroundPreservesCapture(t *testing.T) {
	s, err := Compile([]Expression{{Id: 1, Pattern: `(?=(a))\1`}})
	if err != nil {
		t.Fatal(err)
	}
	matches, err := s.Scan([]byte("a b"))
	if err != nil || len(matches) != 1 || matches[0] != (Match{Id: 1, From: 0, To: 1}) {
		t.Fatalf("lookaround capture mismatch: %v %#v", err, matches)
	}
}

func TestAtomicGroupPreventsAlternativeBacktracking(t *testing.T) {
	s, err := Compile([]Expression{{Id: 1, Pattern: `(?>ab|a)b`}})
	if err != nil {
		t.Fatal(err)
	}
	matches, err := s.Scan([]byte("ab"))
	if err != nil || len(matches) != 0 {
		t.Fatalf("atomic group backtracked: %v %#v", err, matches)
	}
}

func TestNullableUnboundedRepeatTerminates(t *testing.T) {
	s, err := Compile([]Expression{{Id: 1, Pattern: `(?:a?)*b`}})
	if err != nil {
		t.Fatal(err)
	}
	matches, err := s.Scan([]byte("aaab"))
	if err != nil || len(matches) != 1 || matches[0] != (Match{Id: 1, From: 0, To: 4}) {
		t.Fatalf("nullable repeat mismatch: %v %#v", err, matches)
	}
}

func TestPossessiveRepeatPreventsBacktracking(t *testing.T) {
	s, err := Compile([]Expression{{Id: 1, Pattern: `a*+a`}, {Id: 2, Pattern: `a*+b`}})
	if err != nil {
		t.Fatal(err)
	}
	matches, err := s.Scan([]byte("aaa aab"))
	if err != nil || len(matches) != 1 || matches[0] != (Match{Id: 2, From: 4, To: 7}) {
		t.Fatalf("possessive repeat backtracked: %v %#v", err, matches)
	}
}
