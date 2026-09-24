package scankit

import "testing"

func TestAutoNFASelectionMatchesGenericScanner(t *testing.T) {
	cases := []struct {
		name  string
		pat   string
		data  []byte
		flags CompileFlag
	}{
		{name: "alternation", pat: `(?:ab|cd)`, data: []byte("xxabxcdcd")},
		{name: "class", pat: `[a-z][0-9]`, data: []byte("a1 z9 ax")},
		{name: "repeat", pat: `(?:xy){1,3}`, data: []byte("xyxy zxyxyxy")},
		{name: "boundary", pat: `\bcat\b`, data: []byte("cat scatter cat")},
		{name: "lookaround", pat: `(?=ab)ab`, data: []byte("zab ab")},
		{name: "empty", pat: `a*`, data: []byte("ba"), flags: CompileAllowEmpty},
		{name: "class-repeat", pat: `[a-c]{2,3}`, data: []byte("ab cde zz")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			scanner, err := Compile([]Expression{{Id: 1, Pattern: tc.pat, Flags: tc.flags}})
			if err != nil {
				t.Fatal(err)
			}
			got, err := scanner.Scan(tc.data)
			if err != nil {
				t.Fatal(err)
			}
			fallback, err := Compile([]Expression{{Id: 1, Pattern: tc.pat, Flags: tc.flags}})
			if err != nil {
				t.Fatal(err)
			}
			for i := range fallback.rules {
				fallback.rules[i].nfaEngine = nil
				fallback.rules[i].smallWrite = nil
				fallback.rules[i].smallBlock = nil
				fallback.rules[i].repeat = nil
			}
			fallback.literalFind = nil
			fallback.candidateIDs = map[uint32]struct{}{}
			fallback.literalIDs = map[uint32]struct{}{}
			want, err := fallback.Scan(tc.data)
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != len(want) {
				t.Fatalf("自动后端结果数量=%v，范围确认数量=%v", got, want)
			}
			for i := range got {
				if got[i] != want[i] {
					t.Fatalf("自动后端结果=%v，范围确认结果=%v", got, want)
				}
			}
		})
	}
}
