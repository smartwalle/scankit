package nfa

import (
	"testing"

	"github.com/smartwalle/scankit/internal/nfagraph"
	"github.com/smartwalle/scankit/internal/parser"
)

// 固定语义样例覆盖空匹配、重叠、NUL、负类和重复路径。
func TestEngineFamiliesExtendedConformance(t *testing.T) {
	cases := []struct {
		name string
		pat  string
		data [][]byte
	}{
		{"empty", "a*", [][]byte{nil, []byte("a"), []byte("baa")}},
		{"overlap", "aba", [][]byte{[]byte("ababa"), []byte("xaba")}},
		{"nul", "a\\x00b", [][]byte{[]byte("a\x00b"), []byte("a\x00bx")}},
		{"class", "[a-c]+", [][]byte{[]byte("abcx"), []byte("zcc")}},
		{"negated", "[^a]+", [][]byte{[]byte("bbb"), []byte("ab")}},
		{"repeat", "ab{2,4}", [][]byte{[]byte("abb"), []byte("abbbb"), []byte("abbbbb")}},
		{"unicode", `\p{L}+`, [][]byte{[]byte("éx"), []byte("123"), []byte("中a")}},
	}
	kinds := []EngineKind{EngineLimEx, EngineSheng, EngineMcSheng, EngineTamarama, EngineVermicelli, EngineShufti, EngineTruffle, EngineRepeat, EngineLBR}
	for _, tc := range cases {
		root, err := parser.Parse(tc.pat)
		if err != nil {
			t.Fatalf("%s parse: %v", tc.name, err)
		}
		g, err := nfagraph.NewBuilder().Build(root)
		if err != nil {
			t.Fatalf("%s graph: %v", tc.name, err)
		}
		inputs := make([]ConformanceCase, len(tc.data))
		for i, data := range tc.data {
			inputs[i] = ConformanceCase{Name: tc.name, Data: data}
		}
		if err := CompareEngineFamilies(g, kinds, inputs); err != nil {
			b, _ := Compile(g)
			re, _ := CompileEngine(g, EngineRepeat)
			t.Logf("base=%v repeat=%v", b.MatchAt(tc.data[0], 1), re.MatchAt(tc.data[0], 1))
			t.Fatalf("%s: %v", tc.name, err)
		}
	}
}

// TestUnicodePropertyTablesCoverScriptsAndBinaryProperties 验证完整 Unicode 表索引，
// 防止仅支持少量快捷别名导致合法属性静默失配。
func TestUnicodePropertyTablesCoverScriptsAndBinaryProperties(t *testing.T) {
	cases := []struct {
		name string
		want []byte
		no   []byte
	}{
		{name: `\p{Greek}`, want: []byte("Ω"), no: []byte("A")},
		{name: `\p{Assigned}`, want: []byte("中"), no: []byte{0xef, 0xbf, 0xbf}},
	}
	for _, tc := range cases {
		root, err := parser.Parse(tc.name)
		if err != nil {
			t.Fatal(err)
		}
		g, err := nfagraph.NewBuilder().Build(root)
		if err != nil {
			t.Fatal(err)
		}
		p, err := Compile(g)
		if err != nil {
			t.Fatal(err)
		}
		if !p.AcceptsAt(tc.want, 0, len(tc.want)) {
			t.Fatalf("属性 %s 未匹配 %q", tc.name, tc.want)
		}
		if p.AcceptsAt(tc.no, 0, len(tc.no)) {
			t.Fatalf("属性 %s 错误匹配 %q", tc.name, tc.no)
		}
		spans := p.Spans(append(append([]byte{}, tc.no...), tc.want...))
		if len(spans) == 0 {
			t.Fatalf("属性 %s 全局扫描未产生结果", tc.name)
		}
	}
}
