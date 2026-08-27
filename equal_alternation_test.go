package scankit

import "testing"

func TestEqualAlternationDecisionGraphPreservesNFAResults(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		pattern string
		data    string
	}{
		{`(?:18|19|20)[0-9]{2}`, `1812 1999 2001 2111`},
		{`(?:ab|ac|zz)-X`, `ab-X ac-X zz-X ax-X`},
	} {
		test := test
		t.Run(test.pattern, func(t *testing.T) {
			scanner, err := Compile([]Expression{{Id: 1, Pattern: test.pattern}})
			if err != nil {
				t.Fatal(err)
			}
			if len(scanner.regexPrograms) == 0 || scanner.regexPrograms[0].alternation == nil {
				t.Fatal("equal alternation graph was not compiled")
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
			program, err := compileNFA(root)
			if err != nil {
				t.Fatal(err)
			}
			want := normalizeReferenceEvents(scanWithNFAScheduler(program, data))
			if len(got) != len(want) {
				t.Fatalf("matches=%#v want=%#v", got, want)
			}
			for index := range got {
				if got[index] != want[index] {
					t.Fatalf("match %d = %#v want=%#v", index, got[index], want[index])
				}
			}
		})
	}
}

func FuzzEqualAlternationDecisionGraph(f *testing.F) {
	f.Add([]byte("1812 1999 2001"))
	f.Add([]byte("ab-X ac-X zz-X"))
	mixedScanners := make([]*Scanner, 0, 2)
	for _, expressions := range [][]Expression{
		{{Id: 1, Pattern: `(?:18|19|20)[0-9]{2}`}, {Id: 2, Pattern: `3x`}, {Id: 3, Pattern: `4x`}, {Id: 4, Pattern: `@`}},
		{{Id: 1, Pattern: `(?:18|38)[0-9]{2}`}, {Id: 2, Pattern: `@`}},
	} {
		scanner, err := Compile(expressions)
		if err != nil {
			f.Fatal(err)
		}
		if !scanner.blockScanPlan.mixed.wordScan || scanner.regexPrograms[0].alternation == nil {
			f.Fatal("mixed equal-alternation fixture did not select its fast path")
		}
		mixedScanners = append(mixedScanners, scanner)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 512 {
			t.Skip()
		}
		const pattern = `(?:18|19|20)[0-9]{2}`
		scanner, err := Compile([]Expression{{Id: 1, Pattern: pattern}})
		if err != nil {
			t.Fatal(err)
		}
		got, err := scanner.ScanInto(data, nil)
		if err != nil {
			t.Fatal(err)
		}
		root, err := parseRegexWithFlags(pattern, 0)
		if err != nil {
			t.Fatal(err)
		}
		program, err := compileNFA(root)
		if err != nil {
			t.Fatal(err)
		}
		want := normalizeReferenceEvents(scanWithNFAScheduler(program, data))
		if len(got) != len(want) {
			t.Fatalf("got=%#v want=%#v", got, want)
		}
		for index := range got {
			if got[index] != want[index] {
				t.Fatalf("match %d = %#v want=%#v", index, got[index], want[index])
			}
		}
		for _, scanner := range mixedScanners {
			reference := *scanner
			reference.blockScanPlan.mixed.wordScan = false
			got, err := scanner.ScanInto(data, nil)
			if err != nil {
				t.Fatal(err)
			}
			want, err := reference.ScanInto(data, nil)
			if err != nil {
				t.Fatal(err)
			}
			if !equalMatches(got, want) {
				t.Fatalf("mixed equal-alternation got=%#v want=%#v", got, want)
			}
		}
	})
}

func TestEqualAlternationDecisionGraphUsedByAllMixedDispatches(t *testing.T) {
	for _, test := range []struct {
		name        string
		expressions []Expression
		data        []byte
		wantRange   bool
	}{
		{
			name: "range-single",
			expressions: []Expression{
				{Id: 1, Pattern: `(?:18|19|20)[0-9]{2}`},
				{Id: 2, Pattern: `3x`}, {Id: 3, Pattern: `4x`}, {Id: 4, Pattern: `@`},
			},
			data:      []byte(`1812 1999 2001 3x 4x @`),
			wantRange: true,
		},
		{
			name: "mixed",
			expressions: []Expression{
				{Id: 1, Pattern: `(?:18|38)[0-9]{2}`},
				{Id: 2, Pattern: `@`},
			},
			data: []byte(`1812 3899 @`),
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			scanner, err := Compile(test.expressions)
			if err != nil {
				t.Fatal(err)
			}
			if !scanner.blockScanPlan.mixed.wordScan || scanner.blockScanPlan.mixed.rangeFast != test.wantRange {
				t.Fatalf("mixed plan = %#v", scanner.blockScanPlan.mixed)
			}
			if scanner.regexPrograms[0].alternation == nil {
				t.Fatal("equal alternation graph was not compiled")
			}
			// 破坏仅供 NFA verifier 使用的程序状态。扫描仍然成功，证明所选 mixed
			// 分派确实走了决策图而非悄然回退到 verifier。
			optimized := *scanner
			plan := scanner.blockScanPlan
			for value, lanes := range plan.unanchored.fixedAnchor {
				if len(lanes) == 0 {
					continue
				}
				lanes = append([]blockFixedAnchorLane(nil), lanes...)
				for index := range lanes {
					if lanes[index].alternation != nil {
						lanes[index].program.states = nil
					}
				}
				plan.unanchored.fixedAnchor[value] = lanes
			}
			optimized.blockScanPlan = plan
			got, err := optimized.Scan(test.data)
			if err != nil {
				t.Fatal(err)
			}
			want, err := scanner.Scan(test.data)
			if err != nil {
				t.Fatal(err)
			}
			if !equalMatches(got, want) {
				t.Fatalf("optimized=%#v want=%#v", got, want)
			}
		})
	}
}
