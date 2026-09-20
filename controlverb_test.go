package scankit

import "testing"

func TestControlVerbFail(t *testing.T) {
	s, err := Compile([]Expression{{Id: 1, Pattern: `a(*FAIL)`}})
	if err != nil {
		t.Fatal(err)
	}
	m, err := s.Scan([]byte("a"))
	if err != nil || len(m) != 0 {
		t.Fatalf("%v %#v", err, m)
	}
}

func TestControlVerbAcceptStopsSequence(t *testing.T) {
	s, err := Compile([]Expression{{Id: 2, Pattern: `a(*ACCEPT)b`}})
	if err != nil {
		t.Fatal(err)
	}
	matches, err := s.Scan([]byte("ab"))
	if err != nil || len(matches) != 1 || matches[0].To != 1 {
		t.Fatalf("ACCEPT 语义=%v,%v", matches, err)
	}
}
