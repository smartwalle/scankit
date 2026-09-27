package nfa

import (
	"fmt"
	"testing"

	"github.com/smartwalle/scankit/internal/nfagraph"
	"github.com/smartwalle/scankit/internal/parser"
)

// TestEngineFamilyConformanceMatrix 使用同一组字节输入校验各专用执行模型。
// 该矩阵覆盖空匹配、分支、字符类、NUL、重叠起点和多结束位置。
func TestEngineFamilyConformanceMatrix(t *testing.T) {
	cases := []struct {
		name    string
		pattern string
		inputs  []string
	}{
		{"literal", `ab`, []string{"", "ab", "a", "zab", "abab"}},
		{"alternation", `a|b`, []string{"", "a", "b", "c", "ab"}},
		{"class", `[a-c][0-2]`, []string{"a0", "c2", "d0", "a3", "za0"}},
		{"overlap", `a*`, []string{"", "a", "aa", "ba", "aaa"}},
		{"mixed", `(?:ab|a[0-9])`, []string{"ab", "a0", "ax", "zab", "a9"}},
		{"nul", `\x00a`, []string{"\x00a", "x\x00a", "\x00b"}},
	}
	kinds := []EngineKind{EngineCastle, EngineGough, EngineLimEx, EngineSheng, EngineMcSheng, EngineTamarama, EngineVermicelli, EngineShufti, EngineTruffle, EngineLBR}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root, err := parser.Parse(tc.pattern)
			if err != nil {
				t.Fatal(err)
			}
			g, err := nfagraph.NewBuilder().Build(root)
			if err != nil {
				t.Fatal(err)
			}
			baseline, err := CompileEngine(g, EngineCastle)
			if err != nil {
				t.Fatal(err)
			}
			for _, kind := range kinds {
				engine, err := CompileEngine(g, kind)
				if err != nil {
					t.Fatalf("%s 编译失败: %v", kind, err)
				}
				if err := engine.Validate(); err != nil {
					t.Fatalf("%s 布局校验失败: %v", kind, err)
				}
				for _, input := range tc.inputs {
					data := []byte(input)
					for start := 0; start <= len(data); start++ {
						got, want := engine.MatchAt(data, start), baseline.MatchAt(data, start)
						if fmt.Sprint(got) != fmt.Sprint(want) {
							t.Fatalf("%s 输入=%q 起点=%d: got=%v want=%v", kind, input, start, got, want)
						}
					}
				}
			}
		})
	}
}

func TestCompareEngineFamiliesReportsStableCorpus(t *testing.T) {
	root, err := parser.Parse(`(?:foo|f[0-9])`)
	if err != nil {
		t.Fatal(err)
	}
	g, err := nfagraph.NewBuilder().Build(root)
	if err != nil {
		t.Fatal(err)
	}
	cases := []ConformanceCase{{Name: "文字", Data: []byte("foo")}, {Name: "字符类", Data: []byte("f7")}, {Name: "无命中", Data: []byte("bar")}}
	if err := CompareEngineFamilies(g, []EngineKind{EngineCastle, EngineLimEx, EngineSheng, EngineMcSheng, EngineTamarama, EngineShufti, EngineTruffle, EngineVermicelli}, cases); err != nil {
		t.Fatal(err)
	}
}

// TestEngineFamilyBudgetAndRoundTripMatrix 确认限额和序列化不会改变专用路径结果。
func TestEngineFamilyBudgetAndRoundTripMatrix(t *testing.T) {
	root, err := parser.Parse(`(?:a|b)*`)
	if err != nil {
		t.Fatal(err)
	}
	g, err := nfagraph.NewBuilder().Build(root)
	if err != nil {
		t.Fatal(err)
	}
	data := []byte("ababa")
	for kind := EngineCastle; kind <= EngineLBR; kind++ {
		engine, err := CompileEngine(g, kind)
		if err != nil {
			t.Fatal(kind, err)
		}
		raw, err := engine.Dump()
		if err != nil {
			t.Fatalf("%s 序列化失败: %v", kind, err)
		}
		loaded, err := LoadEngine(raw)
		if err != nil {
			t.Fatalf("%s 恢复失败: %v", kind, err)
		}
		if got, want := loaded.MatchAt(data, 0), engine.MatchAt(data, 0); fmt.Sprint(got) != fmt.Sprint(want) {
			t.Fatalf("%s 恢复结果=%v want=%v", kind, got, want)
		}
		if got, steps, stopped := loaded.MatchAtBudget(data, 0, 1, 0); !stopped || steps <= 1 || got != nil {
			t.Fatalf("%s 步骤限额未生效: got=%v steps=%d stopped=%v", kind, got, steps, stopped)
		}
	}
}
