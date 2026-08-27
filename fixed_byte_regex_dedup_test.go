package scankit

import "testing"

func TestFixedByteRegexDuplicateAnalysis(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name      string
		pattern   string
		duplicate bool
	}{
		{name: "DisjointAlternatives", pattern: `4[0-9]{3}|5[1-5][0-9]{2}|3[47][0-9]{2}`, duplicate: false},
		{name: "DifferentWidths", pattern: `a[0-9]|a[0-9]{2}`, duplicate: false},
		{name: "OverlappingAlternatives", pattern: `a[0-9]|a[0-5]`, duplicate: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			root, err := parseRegexWithFlags(test.pattern, 0)
			if err != nil {
				t.Fatal(err)
			}
			applyRegexFlags(root, 0)
			fixed, ok := extractFixedByteRegex(root)
			if !ok {
				t.Fatalf("extractFixedByteRegex(%q) = false", test.pattern)
			}
			if fixed.mayDuplicateMatches != test.duplicate {
				t.Fatalf("mayDuplicateMatches = %t, want %t", fixed.mayDuplicateMatches, test.duplicate)
			}
		})
	}
}

func TestFixedByteRegexDedupFastPathPreservesMatches(t *testing.T) {
	t.Parallel()
	for _, pattern := range []string{
		`4[0-9]{3}|5[1-5][0-9]{2}|3[47][0-9]{2}`,
		`a[0-9]|a[0-9]{2}`,
		`a[0-9]|a[0-5]`,
	} {
		scanner, err := Compile([]Expression{{Id: 1, Pattern: pattern}})
		if err != nil {
			t.Fatal(err)
		}
		data := []byte("a09 a012 4123 5123 3712")
		want, err := scanner.Scan(data)
		if err != nil {
			t.Fatal(err)
		}
		for index := range scanner.regexPrograms {
			if scanner.regexPrograms[index].fixed == nil {
				continue
			}
			reference := *scanner
			fixed := *reference.regexPrograms[index].fixed
			fixed.mayDuplicateMatches = true
			reference.regexPrograms = append([]compiledRegexProgram(nil), scanner.regexPrograms...)
			reference.regexPrograms[index].fixed = &fixed
			plan := scanner.blockScanPlan
			for value, lanes := range scanner.blockScanPlan.unanchored.fixed {
				if len(lanes) == 0 {
					continue
				}
				lanes = append([]blockFixedLane(nil), lanes...)
				for laneIndex := range lanes {
					lane := &lanes[laneIndex]
					if lane.contextIndex == uint32(index) {
						lane.fixed = &fixed
					}
				}
				plan.unanchored.fixed[value] = lanes
			}
			reference.blockScanPlan = plan
			got, err := reference.Scan(data)
			if err != nil {
				t.Fatal(err)
			}
			if !equalMatches(got, want) {
				t.Fatalf("pattern %q optimized=%#v reference=%#v", pattern, want, got)
			}
		}
	}
}
