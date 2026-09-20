package scankit

import "testing"

func TestConditionalReference(t *testing.T) {
	s, err := Compile([]Expression{{Id: 1, Pattern: `(a)(?(1)b|c)`}})
	if err != nil {
		t.Fatal(err)
	}
	m, err := s.Scan([]byte("ab ac"))
	if err != nil || len(m) != 1 {
		t.Fatalf("%v %#v", err, m)
	}
}

func TestNamedConditionalReference(t *testing.T) {
	s, err := Compile([]Expression{{Id: 1, Pattern: `(?<part>a)(?(<part>)b|c)`}})
	if err != nil {
		t.Fatal(err)
	}
	matches, err := s.Scan([]byte("ab ac"))
	if err != nil || len(matches) != 1 || matches[0] != (Match{Id: 1, From: 0, To: 2}) {
		t.Fatalf("named conditional mismatch: %v %#v", err, matches)
	}
}
