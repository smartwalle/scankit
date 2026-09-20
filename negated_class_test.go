package scankit

import "testing"

func TestNegatedClassScan(t *testing.T) {
	s, err := Compile([]Expression{{Id: 1, Pattern: `[^a]`}})
	if err != nil {
		t.Fatal(err)
	}
	m, err := s.Scan([]byte("ab"))
	if err != nil || len(m) != 1 || m[0].From != 1 {
		t.Fatalf("%v %#v", err, m)
	}
}

func TestUppercaseAndWhitespaceCharacterClasses(t *testing.T) {
	cases := []struct {
		pattern string
		input   string
		count   int
	}{
		{`\D+`, "1ab2", 1},
		{`\W+`, "a_!9", 1},
		{`\S+`, " a\t", 1},
		{`[\D]+`, "1ab2", 1},
		{`\h+`, "\t  x", 1},
		{`\v+`, "x\n\ry", 1},
	}
	for _, tc := range cases {
		s, err := Compile([]Expression{{Id: 1, Pattern: tc.pattern}})
		if err != nil {
			t.Fatalf("compile %q: %v", tc.pattern, err)
		}
		matches, err := s.Scan([]byte(tc.input))
		if err != nil || len(matches) != tc.count {
			t.Fatalf("scan %q against %q: %v %#v", tc.pattern, tc.input, err, matches)
		}
	}
}
