package scankit

import (
	"slices"
	"strings"
	"testing"
)

// confirmDFACorpusPatterns 是断言敏感确认路径的等价性语料：既有真正启用该路径的
// 「末尾词边界」形态，也有必须被适用性检查拒绝的前置/中段断言形态。
func confirmDFACorpusPatterns() []string {
	return []string{
		`[a-z]+@[a-z]+\.[a-z]{2,}\b`,
		"[A-Za-z0-9.!#$%&'*+/?^_`{|}~-]{1,64}@[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?(?:\\.[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?)+\\b",
		`[a-z]+@[a-z]+\.(?:com|org|net)\b`,
		`(?:[a-z]+\b|\d+)`,
		`[a-z]{2,5}\b`,
		`(?:a|ab|abc)\b`,
		`x(?:y|z){1,3}\b`,
		`[a-z]+(?:-[a-z]+){0,3}\b`,
		`@[a-z]+\b`,
		`[a-z]+\B`,
		`(?:[a-z]+|\d+)\b`,
		`[a-z]{1,3}[0-9]{1,3}\b`,
		`\bfoo\b`,
		`\Bfoo\B`,
		`foo\bbar`,
		`(?:\b|^)1[3-9][0-9]{9}(?:\b|$)`,
	}
}

// confirmDFACorpusInputs 覆盖数据边界、词边界、分隔符与类外字节。
func confirmDFACorpusInputs() [][]byte {
	return [][]byte{
		[]byte(""),
		[]byte("a"),
		[]byte("_"),
		[]byte("ab@cd.com"),
		[]byte(" ab@cd.com "),
		[]byte("xab@cd.comY"),
		[]byte("mail:ab@cd.com;next"),
		[]byte("a.b-c_d+e@sub.domain.co.uk more"),
		[]byte("123 456\t789\n"),
		[]byte("foo@bar..baz qux@quux.io"),
		[]byte("___@@@..."),
		[]byte("user@host\u957f"),
		[]byte("z1@a.b z2@a.b z3@a.b"),
	}
}

// TestConfirmDFAEquivalentToProgram 是确认表的核心正确性契约：对语料中每个候选
// 起点，断言敏感确认表必须与确认程序虚机给出完全相同的偏好结束偏移；不满足
// 适用性边界的规则必须停留在虚机路径。
func TestConfirmDFAEquivalentToProgram(t *testing.T) {
	for _, pattern := range confirmDFACorpusPatterns() {
		for inputIndex, data := range confirmDFACorpusInputs() {
			scanner, err := Compile([]Expression{{Id: 1, Pattern: pattern}})
			if err != nil {
				t.Fatalf("pattern %q input %d: compile: %v", pattern, inputIndex, err)
			}
			rule := &scanner.rules[0]
			if rule.confirm == nil {
				t.Fatalf("pattern %q: confirm program missing", pattern)
			}
			if rule.confirmDFA == nil {
				continue
			}
			ctx := scanner.acquireScanContext()
			frames := ctx.confirmFrames()
			for start := 0; start <= len(data); start++ {
				want, ok := rule.confirm.preferredEnd(data, start, rule.flags, frames)
				if !ok {
					t.Fatalf("pattern %q input %d: confirm program exceeded budget at start=%d", pattern, inputIndex, start)
				}
				got, valid := rule.confirmDFA.preferredEnd(data, start)
				if !valid {
					t.Fatalf("pattern %q: confirm table rejected start=%d", pattern, start)
				}
				if got != want {
					t.Fatalf("pattern %q input %d start=%d: dfa=%d program=%d window=%q",
						pattern, inputIndex, start, got, want, windowAt(data, start, 20))
				}
			}
			scanner.releaseScanContext(ctx)
		}
	}
}

// windowAt 返回起点附近的窗口文本，便于断言失败时定位。
func windowAt(data []byte, start, span int) string {
	from := max(0, start-4)
	to := min(len(data), start+span)
	return strings.ToValidUTF8(string(data[from:to]), "?")
}

// TestConfirmDFAApplicability 固化适用性边界：只有「断言必然落在匹配末尾」的图
// 才能构建确认表，前置或中段断言必须回退到确认程序虚机。
func TestConfirmDFAApplicability(t *testing.T) {
	enabled := []string{
		`[a-z]+@[a-z]+\.[a-z]{2,}\b`,
		`[a-z]+\B`,
		`@[a-z]+\b`,
	}
	rejected := []string{
		`\bfoo\b`,
		`\Bfoo\B`,
		`foo\bbar`,
		`(?:\b|^)1[3-9][0-9]{9}(?:\b|$)`,
		`\bfoo`,
	}
	for _, pattern := range enabled {
		scanner, err := Compile([]Expression{{Id: 1, Pattern: pattern}})
		if err != nil {
			t.Fatalf("pattern %q: compile: %v", pattern, err)
		}
		if scanner.rules[0].confirmDFA == nil {
			t.Errorf("pattern %q: expected tail-assertion confirm table", pattern)
		}
	}
	for _, pattern := range rejected {
		scanner, err := Compile([]Expression{{Id: 1, Pattern: pattern}})
		if err != nil {
			t.Fatalf("pattern %q: compile: %v", pattern, err)
		}
		if scanner.rules[0].confirmDFA != nil {
			t.Errorf("pattern %q: confirm table must not be built for non-tail assertions", pattern)
		}
	}
}

// TestConfirmDFAScanUnchanged 用同一规则的「启用确认表」与「强制回退确认程序」
// 两条链路对扫，校验整条扫描结果集合逐项一致。这比固定期望值更能覆盖候选枚举、
// 重叠抑制与偏好结束偏移的组合行为。
func TestConfirmDFAScanUnchanged(t *testing.T) {
	patterns := []string{
		`[a-z]+@[a-z]+\.[a-z]{2,}\b`,
		"[A-Za-z0-9.!#$%&'*+/?^_`{|}~-]{1,64}@[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?(?:\\.[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?)+\\b",
		`[a-z]{2,5}\b`,
	}
	inputs := [][]byte{
		[]byte("first=ab@cd.com second=xab@cd.comY third=zz@q.io end=no@no..x \u4e2d\u6587"),
		[]byte("email=user000@sample00.com note=ok"),
		[]byte("a@b.co c@d.coX e@f.co"),
		[]byte(""),
		[]byte("alpha beta gamma-alpha delta_zeta"),
	}
	for _, pattern := range patterns {
		for index, data := range inputs {
			withTable, err := Compile([]Expression{{Id: 7, Pattern: pattern}})
			if err != nil {
				t.Fatalf("pattern %q: compile: %v", pattern, err)
			}
			withoutTable, err := Compile([]Expression{{Id: 7, Pattern: pattern}})
			if err != nil {
				t.Fatalf("pattern %q: compile: %v", pattern, err)
			}
			if withTable.rules[0].confirmDFA == nil {
				continue
			}
			withoutTable.rules[0].confirmDFA = nil
			got, err := withTable.Scan(data)
			if err != nil {
				t.Fatal(err)
			}
			want, err := withoutTable.Scan(data)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(got, want) {
				t.Fatalf("pattern %q input %d: with table %#v, without %#v", pattern, index, got, want)
			}
		}
	}
}

// TestConfirmDFATableShape 校验确认表的形状不变量：哨兵必须大于全部真实状态，
// 且内存计入编译预算。
func TestConfirmDFATableShape(t *testing.T) {
	scanner, err := Compile([]Expression{{Id: 1, Pattern: `[a-z]+@[a-z]+\.[a-z]{2,}\b`}})
	if err != nil {
		t.Fatal(err)
	}
	table := scanner.rules[0].confirmDFA
	if table == nil {
		t.Fatal("confirm table missing")
	}
	if states := uint32(len(table.table) / 256); confirmDFADead < states {
		t.Fatalf("dead sentinel %d collides with %d real states", confirmDFADead, states)
	}
	if len(table.table)%256 != 0 {
		t.Fatalf("transition table length %d is not a whole number of states", len(table.table))
	}
	if got := table.memoryBytes(); got == 0 {
		t.Fatal("confirm table memory accounting must be non-zero")
	}
}
