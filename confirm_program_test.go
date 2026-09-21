package scankit

import (
	"testing"
)

// confirmExpectedEnd 用通用 AST 求值计算 record 唯一使用的偏好结束偏移。
func confirmExpectedEnd(rule compiledRule, data []byte, start int) int {
	ends := dedup(matchNode(rule.root, data, start, rule.flags))
	if len(ends) == 0 {
		return -1
	}
	if rule.nonGreedy {
		return ends[0]
	}
	return ends[len(ends)-1]
}

// TestConfirmProgramMatchesGenericEvaluation 逐起点对照确认程序与通用求值，
// 两者必须给出完全一致的偏好结束偏移。
func TestConfirmProgramMatchesGenericEvaluation(t *testing.T) {
	for _, test := range byteChainCases() {
		t.Run(test.pattern, func(t *testing.T) {
			scanner, err := Compile([]Expression{{Id: 1, Pattern: test.pattern}})
			if err != nil {
				t.Fatalf("compile %q: %v", test.pattern, err)
			}
			rule := scanner.rules[0]
			if rule.confirm == nil {
				t.Fatalf("pattern %q: confirm program unavailable", test.pattern)
			}
			stack := make([]confirmFrame, confirmMaxStack)
			for _, input := range test.inputs {
				data := []byte(input)
				for start := 0; start <= len(data); start++ {
					got, ok := rule.confirm.preferredEnd(data, start, rule.flags, stack)
					if !ok {
						t.Fatalf("pattern %q input %q start %d: confirm program unavailable at runtime", test.pattern, input, start)
					}
					if want := confirmExpectedEnd(rule, data, start); got != want {
						t.Fatalf("pattern %q input %q start %d: confirm end %d, generic end %d", test.pattern, input, start, got, want)
					}
				}
			}
		})
	}
}

// TestConfirmProgramCoversPIIShapes 保证规模场景中的规则确实由确认程序执行，
// 避免基准数据在快路径静默失效后失去意义。
func TestConfirmProgramCoversPIIShapes(t *testing.T) {
	for _, test := range byteChainCases()[:8] {
		scanner, err := Compile([]Expression{{Id: 1, Pattern: test.pattern}})
		if err != nil {
			t.Fatalf("compile %q: %v", test.pattern, err)
		}
		rule := scanner.rules[0]
		if rule.confirm == nil {
			t.Fatalf("pattern %q: expected a compiled confirm program", test.pattern)
		}
		data := []byte(test.inputs[0])
		got, ok := rule.confirm.preferredEnd(data, 0, rule.flags, make([]confirmFrame, confirmMaxStack))
		if !ok || got != confirmExpectedEnd(rule, data, 0) {
			t.Fatalf("pattern %q: confirm result (%d, %v) does not match generic evaluation", test.pattern, got, ok)
		}
	}
}

// TestConfirmProgramGreedyPreference 覆盖贪婪与非贪婪规则在存在多个结束
// 偏移时的取值方向，并与 Scanner 实际产出的结束偏移对照。
func TestConfirmProgramGreedyPreference(t *testing.T) {
	for _, test := range []struct {
		pattern string
		input   string
	}{
		{pattern: `a+`, input: "aaaa"},
		{pattern: `a+?`, input: "aaaa"},
		{pattern: `a{2,4}`, input: "aaaa"},
		{pattern: `a{2,4}?`, input: "aaaa"},
		{pattern: `a+`, input: "aaab"},
		{pattern: `(?:a|ab)c?`, input: "abc"},
		{pattern: `[a-z]{2,5}`, input: "abcdef"},
	} {
		t.Run(test.pattern+"_"+test.input, func(t *testing.T) {
			scanner, err := Compile([]Expression{{Id: 1, Pattern: test.pattern}})
			if err != nil {
				t.Fatalf("compile %q: %v", test.pattern, err)
			}
			rule := scanner.rules[0]
			if rule.confirm == nil {
				t.Fatalf("pattern %q: confirm program unavailable", test.pattern)
			}
			data := []byte(test.input)
			ends := dedup(matchNode(rule.root, data, 0, rule.flags))
			if len(ends) == 0 {
				t.Fatalf("pattern %q input %q: generic evaluation found no match", test.pattern, test.input)
			}
			want := ends[len(ends)-1]
			if rule.nonGreedy {
				want = ends[0]
			}
			got, ok := rule.confirm.preferredEnd(data, 0, rule.flags, make([]confirmFrame, confirmMaxStack))
			if !ok {
				t.Fatalf("pattern %q: confirm program unavailable at runtime", test.pattern)
			}
			if got != want {
				t.Fatalf("pattern %q input %q: confirm end %d, want %d", test.pattern, test.input, got, want)
			}
			matches, err := scanner.Scan(data)
			if err != nil {
				t.Fatal(err)
			}
			if len(matches) == 0 {
				t.Fatalf("pattern %q input %q: Scan found no match", test.pattern, test.input)
			}
			if int(matches[0].To) != want {
				t.Fatalf("pattern %q input %q: Scan end %d, want %d", test.pattern, test.input, matches[0].To, want)
			}
		})
	}
}

// TestConfirmProgramFallsBackForUnsupported 确认无法保证语义一致的规则不会
// 编译出确认程序，从而保留通用确认路径。
func TestConfirmProgramFallsBackForUnsupported(t *testing.T) {
	for _, pattern := range []string{
		`(?i:abc)`,
		`(?=abc)abc`,
		`(?>ab)c`,
		`(a)\1`,
		`a{1,300}`,
	} {
		scanner, err := Compile([]Expression{{Id: 1, Pattern: pattern}})
		if err != nil {
			t.Fatalf("compile %q: %v", pattern, err)
		}
		if scanner.rules[0].confirm != nil {
			t.Fatalf("pattern %q: expected the confirm program to be unavailable", pattern)
		}
	}
}

// TestConfirmProgramBudgetFallsBack 校验回溯规模超限时虚机明确返回 false，
// 由调用方回退通用确认路径，而不是给出不完整结果。
func TestConfirmProgramBudgetFallsBack(t *testing.T) {
	pattern := `(?:aa|a){1,40}`
	scanner, err := Compile([]Expression{{Id: 1, Pattern: pattern}})
	if err != nil {
		t.Fatalf("compile %q: %v", pattern, err)
	}
	rule := scanner.rules[0]
	if rule.confirm == nil {
		t.Fatalf("pattern %q: confirm program unavailable", pattern)
	}
	data := []byte("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaab")
	stack := make([]confirmFrame, confirmMaxStack)
	if _, ok := rule.confirm.preferredEnd(data, 0, rule.flags, stack); ok {
		t.Fatalf("pattern %q: expected the confirm program to exceed its budget", pattern)
	}
	// 回退路径必须给出与通用求值一致的结果。
	matches, err := scanner.Scan(data)
	if err != nil {
		t.Fatal(err)
	}
	for _, match := range matches {
		if match.From != 0 || match.To != uint64(confirmExpectedEnd(rule, data, 0)) {
			t.Fatalf("pattern %q: fallback match %#v diverges from generic evaluation", pattern, match)
		}
	}
}

// TestConfirmRemainBounds 验证按字节数剪枝所需的上界：有界程序给出精确的
// 最大消费长度，含无上界重复或循环的程序必须禁用剪枝。
func TestConfirmRemainBounds(t *testing.T) {
	cases := []struct {
		pattern string
		want    int
		prune   bool
	}{
		{pattern: `[0-9]{5}`, want: 5, prune: true},
		{pattern: `1[3-9][0-9]{9}`, want: 11, prune: true},
		{pattern: `(?:ab|cd)`, want: 2, prune: true},
		{pattern: `a(?:b|c){2,3}d`, want: 5, prune: true},
		{pattern: `62[0-9]{14,17}`, want: 19, prune: true},
		{pattern: `[0-9]+`, want: 0, prune: false},
		{pattern: `[z][a-z]{9,}`, want: 0, prune: false},
	}
	for _, tc := range cases {
		t.Run(tc.pattern, func(t *testing.T) {
			scanner, err := Compile([]Expression{{Id: 1, Pattern: tc.pattern}})
			if err != nil {
				t.Fatalf("compile %q: %v", tc.pattern, err)
			}
			program := scanner.rules[0].confirm
			if program == nil {
				t.Fatalf("pattern %q: confirm program unavailable", tc.pattern)
			}
			if !tc.prune {
				if program.remain != nil {
					t.Fatalf("pattern %q: unbounded program must disable pruning", tc.pattern)
				}
				if program.bounded {
					t.Fatalf("pattern %q: program must be reported as unbounded", tc.pattern)
				}
				return
			}
			if program.remain == nil {
				t.Fatalf("pattern %q: bounded program must expose a byte upper bound", tc.pattern)
			}
			if got := program.remain[program.entry]; got != tc.want {
				t.Fatalf("pattern %q: entry bound = %d, want %d", tc.pattern, got, tc.want)
			}
			for pc, limit := range program.remain {
				if limit >= confirmRemainUnbounded {
					t.Fatalf("pattern %q: instruction %d has unbounded limit %d", tc.pattern, pc, limit)
				}
			}
		})
	}
}

// TestConfirmProgramPruningKeepsPreferredEnd 在同一程序上对照启用与禁用按
// 字节数剪枝的结果，确认剪枝只丢弃不可能改善偏好结束偏移的回溯点。
func TestConfirmProgramPruningKeepsPreferredEnd(t *testing.T) {
	exercised := 0
	for _, test := range byteChainCases() {
		t.Run(test.pattern, func(t *testing.T) {
			scanner, err := Compile([]Expression{{Id: 1, Pattern: test.pattern}})
			if err != nil {
				t.Fatalf("compile %q: %v", test.pattern, err)
			}
			rule := scanner.rules[0]
			if rule.confirm == nil {
				return
			}
			pruned := *rule.confirm
			if pruned.remain == nil {
				return
			}
			plain := pruned
			plain.remain = nil
			exercised++
			prunedStack := make([]confirmFrame, confirmMaxStack)
			plainStack := make([]confirmFrame, confirmMaxStack)
			for _, input := range test.inputs {
				data := []byte(input)
				for start := 0; start <= len(data); start++ {
					got, ok := pruned.run(data, pruned.entry, start, rule.flags, prunedStack)
					if !ok {
						t.Fatalf("pattern %q input %q start %d: pruned run must stay applicable", test.pattern, input, start)
					}
					want, wantOK := plain.run(data, plain.entry, start, rule.flags, plainStack)
					if !wantOK {
						continue
					}
					if got != want {
						t.Fatalf("pattern %q input %q start %d: pruned end %d, unpruned end %d", test.pattern, input, start, got, want)
					}
				}
			}
		})
	}
	if exercised == 0 {
		t.Fatal("no bounded pattern exercised byte-based pruning")
	}
}
