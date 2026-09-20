package nfa

import (
	"fmt"
	"testing"

	"github.com/smartwalle/scankit/internal/nfagraph"
	"github.com/smartwalle/scankit/internal/parser"
)

// TestEngineFamilyGeneratedConformance 使用固定字节语料覆盖分支、闭包、字符类和空结果。
func TestEngineFamilyGeneratedConformance(t *testing.T) {
	patterns := []string{`a`, `ab`, `a|b`, `(?:ab|a[0-9])`, `[a-c]+`, `[^x]y`, `(?:a|b)*c`, `(?:foo|f[0-9])`, `a?b`, `(?:ab|cd)e`, `\x00a`, `a[0-9][a-z]`}
	inputs := [][]byte{nil, []byte("a"), []byte("ab"), []byte("a7"), []byte("abc"), []byte("bbbc"), []byte("foo"), []byte("f7"), []byte("xx"), []byte("\x00a")}
	kinds := []EngineKind{EngineCastle, EngineGough, EngineLimEx, EngineSheng, EngineMcSheng, EngineTamarama, EngineVermicelli, EngineShufti, EngineTruffle, EngineLBR}
	for _, pattern := range patterns {
		r, err := parser.Parse(pattern)
		if err != nil {
			t.Fatalf("解析 %q: %v", pattern, err)
		}
		g, err := nfagraph.NewBuilder().Build(r)
		if err != nil {
			t.Fatalf("构图 %q: %v", pattern, err)
		}
		base, err := CompileEngine(g, EngineCastle)
		if err != nil {
			t.Fatalf("基线 %q: %v", pattern, err)
		}
		for _, kind := range kinds {
			e, err := CompileEngine(g, kind)
			if err != nil {
				t.Fatalf("%s 编译 %q: %v", kind, pattern, err)
			}
			for _, input := range inputs {
				for start := 0; start <= len(input); start++ {
					got, want := e.MatchAt(input, start), base.MatchAt(input, start)
					if fmt.Sprint(got) != fmt.Sprint(want) {
						t.Fatalf("%s/%q 输入=%q 起点=%d: got=%v want=%v", kind, pattern, input, start, got, want)
					}
				}
			}
		}
	}
}

// TestReleaseConformanceCorpus 覆盖发布前容易遗漏的边界和回退语义。
func TestReleaseConformanceCorpus(t *testing.T) {
	cases := []struct {
		name string
		pat  string
		data [][]byte
	}{
		{"anchor", `^a$`, [][]byte{[]byte("a"), []byte("ba"), []byte("a\n")}},
		{"eod", `a\z`, [][]byte{[]byte("a"), []byte("a\n")}},
		{"boundary", `\ba+\b`, [][]byte{[]byte("a"), []byte("a1"), []byte("_a")}},
		{"lookaround", `(?=ab)ab`, [][]byte{[]byte("ab"), []byte("axb")}},
		{"unicode", `\p{L}+`, [][]byte{[]byte("abc"), []byte("é"), []byte("123")}},
		{"repeat", `(?:ab?){1,3}`, [][]byte{[]byte("a"), []byte("ababa"), []byte("ababab")}},
		{"nul", `\x00[^\x00]`, [][]byte{{0, 1}, {0, 0}, {1, 2}}},
	}
	kinds := []EngineKind{EngineCastle, EngineGough, EngineLimEx, EngineSheng, EngineMcSheng, EngineTamarama, EngineVermicelli, EngineShufti, EngineTruffle, EngineLBR}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root, err := parser.Parse(tc.pat)
			if err != nil {
				t.Fatal(err)
			}
			g, err := nfagraph.NewBuilder().Build(root)
			if err != nil {
				t.Fatal(err)
			}
			inputs := make([]ConformanceCase, len(tc.data))
			for i, data := range tc.data {
				inputs[i] = ConformanceCase{Name: tc.name, Data: data}
			}
			if err := CompareEngineFamilies(g, kinds, inputs); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestConformanceRejectsDuplicateOrInvalidEngineKinds(t *testing.T) {
	r, err := parser.Parse(`ab`)
	if err != nil {
		t.Fatal(err)
	}
	g, err := nfagraph.NewBuilder().Build(r)
	if err != nil {
		t.Fatal(err)
	}
	cases := []ConformanceCase{{Name: "basic", Data: []byte("ab")}}
	if err := CompareEngineFamilies(g, []EngineKind{EngineCastle, EngineCastle}, cases); err == nil {
		t.Fatal("应拒绝重复后端")
	}
	if err := CompareEngineFamilies(g, []EngineKind{EngineKind(255)}, cases); err == nil {
		t.Fatal("应拒绝非法后端")
	}
}

func TestDeepBranchAndNestedRepeatConformance(t *testing.T) {
	patterns := []string{`(?:(?:ab|a[0-9])(?:c|d)?)+`, `(?:[a-c]|x(?:y|z)){1,3}`, `(?:a|bc|def)*g`}
	inputs := [][]byte{[]byte("abacd"), []byte("a1bd"), []byte("abxyzg"), []byte("defdefg"), []byte("xyaac")}
	kinds := []EngineKind{EngineCastle, EngineGough, EngineLimEx, EngineSheng, EngineMcSheng, EngineTamarama, EngineVermicelli, EngineShufti, EngineTruffle}
	for _, pattern := range patterns {
		r, err := parser.Parse(pattern)
		if err != nil {
			t.Fatal(err)
		}
		g, err := nfagraph.NewBuilder().Build(r)
		if err != nil {
			t.Fatal(err)
		}
		base, err := CompileEngine(g, EngineCastle)
		if err != nil {
			t.Fatal(err)
		}
		for _, kind := range kinds {
			e, err := CompileEngine(g, kind)
			if err != nil {
				t.Fatal(err)
			}
			for _, input := range inputs {
				if got, want := e.Spans(input), base.Spans(input); fmt.Sprint(got) != fmt.Sprint(want) {
					t.Fatalf("%s/%q: got=%v want=%v", kind, pattern, got, want)
				}
			}
		}
	}
}

func TestMalformedUTF8BoundaryIsRejected(t *testing.T) {
	r, err := parser.Parse(`\b.`)
	if err != nil {
		t.Fatal(err)
	}
	g, err := nfagraph.NewBuilder().Build(r)
	if err != nil {
		t.Fatal(err)
	}
	p, err := Compile(g)
	if err != nil {
		t.Fatal(err)
	}
	if got := p.Spans([]byte{0xc3, 0x28}); len(got) != 0 {
		t.Fatalf("非法 UTF-8 边界产生误匹配: %v", got)
	}
}
