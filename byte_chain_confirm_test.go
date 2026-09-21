package scankit

import (
	"slices"
	"testing"
)

// byteChainCase 是一组确认快路径与通用 AST 求值的对照输入。
type byteChainCase struct {
	pattern string
	inputs  []string
}

func byteChainCases() []byteChainCase {
	piiShapes := []string{
		`1[3-9][0-9]{9}`,
		`(?:\b|^)(?:\+86|86)?1[3-9]\d{9}(?:\b|$)`,
		`\b(?:86)?1[3-9][0-9]{9}\b`,
		"[A-Za-z0-9.!#$%&'*+/?^_`{|}~-]{1,64}@[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?(?:\\.[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?)+\\b",
		`[1-9][0-9]{5}(18|19|20)[0-9]{2}(0[1-9]|1[0-2])(0[1-9]|[12][0-9]|3[01])[0-9]{3}[0-9Xx]`,
		`62[0-9]{14,17}`,
		`4[0-9]{15}|5[1-5][0-9]{14}|3[47][0-9]{13}`,
		`[z][a-z]{9,}`,
	}
	piiValues := []string{
		"13700000000", "13700000000", "13700000000",
		"user001@sample01.com", "110105200001010001", "6200000000000000",
		"4000000000000000", "zredaction",
	}
	cases := make([]byteChainCase, 0, len(piiShapes)+3)
	for index, shape := range piiShapes {
		value := piiValues[index]
		cases = append(cases, byteChainCase{
			pattern: "field" + string(rune('0'+index)) + "=(?:" + shape + ")",
			inputs: []string{
				"field" + string(rune('0'+index)) + "=" + value,
				"prefix field" + string(rune('0'+index)) + "=" + value + " suffix",
				"no value here",
			},
		})
	}
	cases = append(cases,
		byteChainCase{
			pattern: `a(?:b|c){2,3}d`,
			inputs:  []string{"abbbcd", "abcbcd", "acd", "a", "xxabbbcdxx"},
		},
		byteChainCase{
			pattern: `\bword\b`,
			inputs:  []string{"word", "a word!", "sword", "wordword", ""},
		},
		byteChainCase{
			pattern: `^(?:ab)?c+d$`,
			inputs:  []string{"abcccd", "cccd", "cd", "abdccd", "c+d"},
		},
	)
	return cases
}

func TestMatchByteChainMatchesGenericEvaluation(t *testing.T) {
	for _, test := range byteChainCases() {
		t.Run(test.pattern, func(t *testing.T) {
			scanner, err := Compile([]Expression{{Id: 1, Pattern: test.pattern}})
			if err != nil {
				t.Fatalf("compile %q: %v", test.pattern, err)
			}
			rule := scanner.rules[0]
			for _, input := range test.inputs {
				data := []byte(input)
				arena := &byteChainArena{}
				for start := 0; start <= len(data); start++ {
					got, ok := matchByteChain(arena, rule.root, data, start, rule.flags)
					if !ok {
						t.Fatalf("pattern %q input %q start %d: fast path unavailable", test.pattern, input, start)
					}
					want := dedup(matchNode(rule.root, data, start, rule.flags))
					if !slices.Equal(got, want) {
						t.Fatalf("pattern %q input %q start %d: fast path ends %v, generic ends %v", test.pattern, input, start, got, want)
					}
				}
			}
		})
	}
}

// TestMatchByteChainFallsBackForUnsupported 确认不回退到通用求值的结构会
// 明确返回 false，而不是给出不完整的结果。
func TestMatchByteChainFallsBackForUnsupported(t *testing.T) {
	for _, pattern := range []string{
		`(?i:abc)`,
		`(?=abc)abc`,
		`(?>ab)c`,
		`(a)\1`,
	} {
		scanner, err := Compile([]Expression{{Id: 1, Pattern: pattern}})
		if err != nil {
			t.Fatalf("compile %q: %v", pattern, err)
		}
		rule := scanner.rules[0]
		arena := &byteChainArena{}
		if _, ok := matchByteChain(arena, rule.root, []byte("abc"), 0, rule.flags); ok {
			t.Fatalf("pattern %q: fast path reported support for an unsupported structure", pattern)
		}
	}
}

// TestScannerUsesByteChainFastPath 保证规模场景中的规则确实走确认快路径，
// 避免快路径被静默禁用后基准数据失去意义。
func TestScannerUsesByteChainFastPath(t *testing.T) {
	patterns := map[string]string{
		"phone":   `field00=(?:1[3-9][0-9]{9})`,
		"email":   `field03=(?:[A-Za-z0-9.!#$%&'*+/?^_` + "`" + `{|}~-]{1,64}@[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?(?:\.[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?)+\b)`,
		"token":   `field07=(?:[z][a-z]{9,})`,
		"id":      `field04=(?:[1-9][0-9]{5}(18|19|20)[0-9]{2}(0[1-9]|1[0-2])(0[1-9]|[12][0-9]|3[01])[0-9]{3}[0-9Xx])`,
		"bank":    `field05=(?:62[0-9]{14,17})`,
		"credit":  `field06=(?:4[0-9]{15}|5[1-5][0-9]{14}|3[47][0-9]{13})`,
		"phone86": `field01=(?:(?:\b|^)(?:\+86|86)?1[3-9]\d{9}(?:\b|$))`,
		"phone3":  `field02=(?:\b(?:86)?1[3-9][0-9]{9}\b)`,
	}
	for name, pattern := range patterns {
		t.Run(name, func(t *testing.T) {
			scanner, err := Compile([]Expression{{Id: 1, Pattern: pattern}})
			if err != nil {
				t.Fatalf("compile %q: %v", pattern, err)
			}
			rule := scanner.rules[0]
			arena := &byteChainArena{}
			if _, ok := matchByteChain(arena, rule.root, []byte("field00=13700000000"), 0, rule.flags); !ok {
				t.Fatalf("pattern %q: expected the byte-chain fast path to be available", pattern)
			}
		})
	}
}
