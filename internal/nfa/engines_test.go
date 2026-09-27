package nfa

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/smartwalle/scankit/internal/graph"
	"github.com/smartwalle/scankit/internal/nfagraph"
	"github.com/smartwalle/scankit/internal/parser"
)

func TestCompileEngineWithBudgetsRejectsStateAndMemoryOverflow(t *testing.T) {
	root, err := parser.Parse("abcdef")
	if err != nil {
		t.Fatal(err)
	}
	g, err := nfagraph.NewBuilder().Build(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := CompileEngineWithBudgets(g, EngineLimEx, 1, 0); !errors.Is(err, ErrStateLimit) {
		t.Fatalf("状态预算错误=%v", err)
	}
	if _, err := CompileEngineWithBudgets(g, EngineLimEx, 0, 1); !errors.Is(err, ErrMemoryLimit) {
		t.Fatalf("内存预算错误=%v", err)
	}
	if _, err := CompileEngineWithBudgets(g, EngineLimEx, 0, 1<<20); err != nil {
		t.Fatalf("充足预算不应失败: %v", err)
	}
}

func TestEngineMemoryEstimateTracksSpecializedLayoutScale(t *testing.T) {
	r, err := parser.Parse(strings.Repeat("a", 160))
	if err != nil {
		t.Fatal(err)
	}
	g, err := nfagraph.NewBuilder().Build(r)
	if err != nil {
		t.Fatal(err)
	}
	if small, large := estimateEngineMemory(g, EngineCastle), estimateEngineMemory(g, EngineLimEx); small == 0 || large <= small {
		t.Fatalf("专用布局估算不合理: castle=%d limex=%d", small, large)
	}
	if _, err := CompileEngineWithBudgets(g, EngineLimEx, 0, 1); !errors.Is(err, ErrMemoryLimit) {
		t.Fatalf("预分配内存限制未生效: %v", err)
	}
}

func TestEngineBudgetReportsNaturalCompletionSeparately(t *testing.T) {
	r, err := parser.Parse(`ab`)
	if err != nil {
		t.Fatal(err)
	}
	g, err := nfagraph.NewBuilder().Build(r)
	if err != nil {
		t.Fatal(err)
	}
	e, err := CompileEngine(g, EngineLimEx)
	if err != nil {
		t.Fatal(err)
	}
	ends, steps, stopped := e.MatchAtBudget([]byte("ab"), 0, 3, 0)
	if stopped || len(ends) != 1 || ends[0] != 2 || steps != 3 {
		t.Fatalf("自然完成预算语义错误: ends=%v steps=%d stopped=%v", ends, steps, stopped)
	}
	_, _, stopped = e.MatchAtBudget([]byte("ab"), 0, 2, 0)
	if !stopped {
		t.Fatal("不足步骤预算应报告提前终止")
	}
}

func TestEngineKindsConformanceOnByteGraph(t *testing.T) {
	root, err := parser.Parse(`(?:ab|a[0-9])`)
	if err != nil {
		t.Fatal(err)
	}
	g, err := nfagraph.NewBuilder().Build(root)
	if err != nil {
		t.Fatal(err)
	}
	want := []int{2}
	data := []byte("ab")
	for kind := EngineCastle; kind <= EngineLBR; kind++ {
		e, err := CompileEngine(g, kind)
		if err != nil {
			t.Fatalf("kind=%s compile: %v", kind, err)
		}
		got := e.MatchAt(data, 0)
		if len(got) != len(want) || got[0] != want[0] {
			t.Fatalf("kind=%s got=%v want=%v model=%s", kind, got, want, e.ExecutionModel())
		}
	}
}

func TestEngineFamiliesConformanceCorpus(t *testing.T) {
	cases := []struct {
		pattern string
		data    []byte
	}{
		{`(?:ab|cd)`, []byte("abcd")},
		{`[a-c][0-2]`, []byte("a1b2c3")},
		{`a*`, []byte("aa")},
		{`(?:xy){1,3}`, []byte("xyxyxy")},
		{`(?:a|)`, []byte("a")},
	}
	kinds := []EngineKind{EngineCastle, EngineGough, EngineLimEx, EngineSheng, EngineMcSheng, EngineTamarama, EngineVermicelli, EngineShufti, EngineTruffle, EngineRepeat, EngineMPV, EngineLBR}
	for _, tc := range cases {
		r, err := parser.Parse(tc.pattern)
		if err != nil {
			t.Fatal(err)
		}
		g, err := nfagraph.NewBuilder().Build(r)
		if err != nil {
			t.Fatal(err)
		}
		ref := (&Program{Graph: g}).Spans(tc.data)
		for _, kind := range kinds {
			e, err := CompileEngine(g, kind)
			if err != nil {
				t.Fatalf("%s compile %q: %v", kind, tc.pattern, err)
			}
			if got := e.Spans(tc.data); fmt.Sprint(got) != fmt.Sprint(ref) {
				t.Fatalf("%s pattern=%q got=%v want=%v model=%s", kind, tc.pattern, got, ref, e.ExecutionModel())
			}
		}
	}
}

func TestEngineFamiliesConformanceOverlapsAndEmpty(t *testing.T) {
	cases := []struct {
		pattern string
		data    []byte
	}{
		{`a?`, []byte("aa")},
		{`(?:a|aa)`, []byte("aa")},
		{`[\x00-\x02]`, []byte{0, 1, 2, 3}},
	}
	kinds := []EngineKind{EngineLimEx, EngineSheng, EngineMcSheng, EngineTamarama, EngineShufti, EngineTruffle, EngineVermicelli}
	for _, tc := range cases {
		r, err := parser.Parse(tc.pattern)
		if err != nil {
			t.Fatal(err)
		}
		g, err := nfagraph.NewBuilder().Build(r)
		if err != nil {
			t.Fatal(err)
		}
		ref := (&Program{Graph: g}).Spans(tc.data)
		for _, kind := range kinds {
			e, err := CompileEngine(g, kind)
			if err != nil {
				t.Fatalf("%s compile: %v", kind, err)
			}
			if got := e.Spans(tc.data); fmt.Sprint(got) != fmt.Sprint(ref) {
				t.Fatalf("%s pattern=%q got=%v want=%v", kind, tc.pattern, got, ref)
			}
		}
	}
}

func TestEngineFamiliesBudgetResultContract(t *testing.T) {
	r, err := parser.Parse(`a{1,3}`)
	if err != nil {
		t.Fatal(err)
	}
	g, err := nfagraph.NewBuilder().Build(r)
	if err != nil {
		t.Fatal(err)
	}
	data := []byte("aaa")
	for _, kind := range []EngineKind{EngineCastle, EngineGough, EngineLimEx, EngineSheng, EngineMcSheng, EngineTamarama, EngineVermicelli, EngineShufti, EngineTruffle, EngineRepeat, EngineMPV, EngineLBR} {
		e, err := CompileEngine(g, kind)
		if err != nil {
			t.Fatalf("%s 编译失败: %v", kind, err)
		}
		want := e.MatchAt(data, 0)
		if len(want) == 0 {
			t.Fatalf("%s 未产生基础结果", kind)
		}
		got, _, stopped := e.MatchAtBudget(data, 0, 0, 1)
		if len(got) != 1 || got[0] != want[0] || !stopped {
			t.Fatalf("%s 结果预算不一致: got=%v want=%v stopped=%v", kind, got, want, stopped)
		}
	}
}

func TestSpecializedBudgetCountsDistinctEndOffsets(t *testing.T) {
	r, err := parser.Parse(`(?:a|a)`)
	if err != nil {
		t.Fatal(err)
	}
	g, err := nfagraph.NewBuilder().Build(r)
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []EngineKind{EngineLimEx, EngineSheng, EngineMcSheng, EngineTamarama, EngineVermicelli, EngineShufti, EngineTruffle} {
		e, err := CompileEngine(g, kind)
		if err != nil {
			t.Fatalf("%s 编译失败: %v", kind, err)
		}
		ends, _, stopped := e.MatchAtBudget([]byte("a"), 0, 0, 1)
		if len(ends) != 1 || ends[0] != 1 || !stopped {
			t.Fatalf("%s 结果预算错误: ends=%v stopped=%v", kind, ends, stopped)
		}
	}
}

func TestSpecializedLayoutsRejectMissingClassPayload(t *testing.T) {
	for _, tc := range []struct {
		name  string
		check func() error
	}{
		{"nibble", func() error {
			return (&nibbleNFAProgram{states: []nibbleNFAState{{kind: nfagraph.KindClass}}, start: 0, closures: [][]int{{0}}, masks: [][16]uint16{{}}, accept: []bool{false}}).validate()
		}},
		{"range", func() error {
			return (&rangeNFAProgram{states: []rangeNFAState{{kind: nfagraph.KindClass}}, start: 0, closures: [][]int{{0}}, trans: [][]rangeTransition{{}}, masks: [][4]uint64{{}}, accept: []bool{false}}).validate()
		}},
		{"sparse", func() error {
			return (&sparseNFAProgram{states: []sparseNFAState{{kind: nfagraph.KindClass}}, start: 0, closures: [][]int{{0}}, trans: []map[byte][]int{{}}, accept: []bool{false}}).validate()
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.check() == nil {
				t.Fatal("缺少字符类负载未被拒绝")
			}
		})
	}
}

func TestSpecializedDeadStateMapsRejectCorruption(t *testing.T) {
	r, err := parser.Parse(`(?:a|b)c`)
	if err != nil {
		t.Fatal(err)
	}
	g, err := nfagraph.NewBuilder().Build(r)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		kind EngineKind
		flip func(*Engine)
	}{
		{EngineLimEx, func(e *Engine) { e.bitNFA.dead[0] ^= 1 }},
		{EngineSheng, func(e *Engine) { e.tableNFA.dead[0] = !e.tableNFA.dead[0] }},
		{EngineMcSheng, func(e *Engine) { e.sparseNFA.dead[0] = !e.sparseNFA.dead[0] }},
		{EngineTamarama, func(e *Engine) { e.rangeNFA.dead[0] = !e.rangeNFA.dead[0] }},
		{EngineShufti, func(e *Engine) { e.nibbleNFA.dead[0] = !e.nibbleNFA.dead[0] }},
		{EngineTruffle, func(e *Engine) { e.nibbleNFA.dead[0] = !e.nibbleNFA.dead[0] }},
	}
	for _, tc := range cases {
		e, err := CompileEngine(g, tc.kind)
		if err != nil {
			t.Fatalf("%s 编译失败: %v", tc.kind, err)
		}
		tc.flip(e)
		if err := e.Validate(); err == nil {
			t.Fatalf("%s 未拒绝损坏的死状态图", tc.kind)
		}
	}
}

func TestSpecializedStartMetadataRejectsCorruption(t *testing.T) {
	r, err := parser.Parse(`ab`)
	if err != nil {
		t.Fatal(err)
	}
	g, err := nfagraph.NewBuilder().Build(r)
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []EngineKind{EngineCastle, EngineGough, EngineLimEx, EngineSheng, EngineMcSheng, EngineTamarama, EngineShufti, EngineTruffle} {
		e, err := CompileEngine(g, kind)
		if err != nil {
			t.Fatalf("%s 编译失败: %v", kind, err)
		}
		switch kind {
		case EngineCastle:
			e.castle.firstMask[0] ^= 1
		case EngineGough:
			e.gough.firstMask[0] ^= 1
		case EngineLimEx:
			e.bitNFA.firstMask[0] ^= 1
		case EngineSheng:
			e.tableNFA.firstMask[0] ^= 1
		case EngineMcSheng:
			e.sparseNFA.firstMask[0] ^= 1
		case EngineTamarama:
			e.rangeNFA.firstMask[0] ^= 1
		case EngineShufti, EngineTruffle:
			e.nibbleNFA.firstMask[0] ^= 1
		default:
		}
		if err := e.Validate(); err == nil {
			t.Fatalf("%s 未拒绝损坏的起始元数据", kind)
		}
	}
	r, err = parser.Parse(`^a$`)
	if err != nil {
		t.Fatal(err)
	}
	g, err = nfagraph.NewBuilder().Build(r)
	if err != nil {
		t.Fatal(err)
	}
	e, err := CompileEngine(g, EngineLBR)
	if err != nil || e.lbr == nil {
		t.Fatalf("LBR 编译失败: %v", err)
	}
	e.lbr.firstMask[0] ^= 1
	if err := e.Validate(); err == nil {
		t.Fatal("LBR 未拒绝损坏的起始元数据")
	}
}

func TestCorruptedLayoutSpansFallsBackSafely(t *testing.T) {
	r, err := parser.Parse(`ab`)
	if err != nil {
		t.Fatal(err)
	}
	g, err := nfagraph.NewBuilder().Build(r)
	if err != nil {
		t.Fatal(err)
	}
	e, err := CompileEngine(g, EngineLimEx)
	if err != nil || e.bitNFA == nil {
		t.Fatalf("位布局编译失败: %v", err)
	}
	e.bitNFA.sourceMask['a'] = nil
	if got := e.bitNFA.Spans([]byte("ab"), 0); got != nil {
		t.Fatalf("损坏布局仍返回结果: %v", got)
	}
}

func TestProgramDeadMapValidation(t *testing.T) {
	r, err := parser.Parse(`ab`)
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
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	p.dead[g.Start] = true
	if err := p.Validate(); err == nil {
		t.Fatal("程序未拒绝损坏的死状态映射")
	}
}

func TestCorruptedPrefixedLayoutDoesNotLoop(t *testing.T) {
	r, err := parser.Parse(`ab`)
	if err != nil {
		t.Fatal(err)
	}
	g, err := nfagraph.NewBuilder().Build(r)
	if err != nil {
		t.Fatal(err)
	}
	e, err := CompileEngine(g, EngineLimEx)
	if err != nil || e.bitNFA == nil {
		t.Fatalf("位布局编译失败: %v", err)
	}
	e.bitNFA.trans = nil
	if got := e.SpansLimit([]byte("ab"), 0); fmt.Sprint(got) != "[{0 2}]" {
		t.Fatalf("损坏前缀布局回退结果错误: %v", got)
	}
}

func TestDeadStateCorruptionFallsBackWithoutResultLoss(t *testing.T) {
	r, _ := parser.Parse(`[a-z][0-9]`)
	g, _ := nfagraph.NewBuilder().Build(r)
	e, err := CompileEngine(g, EngineLimEx)
	if err != nil || e.bitNFA == nil {
		t.Fatalf("LimEx 编译失败: %v", err)
	}
	e.bitNFA.dead[0] ^= 1
	if got := e.MatchAt([]byte("a7"), 0); fmt.Sprint(got) != "[2]" {
		t.Fatalf("损坏布局回退丢失结果: %v", got)
	}
}

func TestSpecializedStartCandidateFilterKeepsEmptyAndMultibyteMatches(t *testing.T) {
	for _, tc := range []struct {
		pattern string
		input   []byte
	}{
		{`a?`, []byte("xba")},
		{`é`, []byte("xé")},
		{`[a-c]`, []byte("zbc")},
	} {
		r, err := parser.Parse(tc.pattern)
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
		for _, kind := range []EngineKind{EngineLimEx, EngineSheng, EngineMcSheng, EngineTamarama, EngineShufti} {
			e, err := CompileEngine(g, kind)
			if err != nil {
				t.Fatal(err)
			}
			if got, want := e.Spans(tc.input), base.Spans(tc.input); fmt.Sprint(got) != fmt.Sprint(want) {
				t.Fatalf("%s/%q: got=%v want=%v", kind, tc.pattern, got, want)
			}
		}
	}
}

func TestByteFallbackStartCandidateFilterKeepsEmptyAndClassMatches(t *testing.T) {
	cases := []struct {
		pattern string
		input   []byte
	}{
		{`[a-c]`, []byte("zbc")},
	}
	for _, tc := range cases {
		r, err := parser.Parse(tc.pattern)
		if err != nil {
			t.Fatal(err)
		}
		g, err := nfagraph.NewBuilder().Build(r)
		if err != nil {
			t.Fatal(err)
		}
		bp := compileByteNFA(g)
		if bp == nil {
			t.Fatalf("字节回退布局无效: %q", tc.pattern)
		}
		if err := bp.validate(); err != nil {
			t.Fatalf("字节回退布局校验失败 %q: %v", tc.pattern, err)
		}
		got := bp.Spans(tc.input, 0)
		if fmt.Sprint(got) != "[{1 2} {2 3}]" {
			t.Fatalf("字节回退结果错误: got=%v", got)
		}
	}
}

func TestByteLayoutValidatesClassMasks(t *testing.T) {
	r, err := parser.Parse(`[a-z]`)
	if err != nil {
		t.Fatal(err)
	}
	g, err := nfagraph.NewBuilder().Build(r)
	if err != nil {
		t.Fatal(err)
	}
	p := compileByteNFA(g)
	if p == nil {
		t.Fatal("字节布局未生成")
	}
	if err := p.validate(); err != nil {
		t.Fatal(err)
	}
	for i, st := range p.states {
		if st.kind == nfagraph.KindClass {
			p.classMask[i][0] ^= 1
			if err := p.validate(); err == nil {
				t.Fatal("字符类掩码损坏未被检测")
			}
			return
		}
	}
	t.Fatal("未找到字符类状态")
}

func TestBitLayoutValidatesSourceMasks(t *testing.T) {
	r, err := parser.Parse(`ab`)
	if err != nil {
		t.Fatal(err)
	}
	g, err := nfagraph.NewBuilder().Build(r)
	if err != nil {
		t.Fatal(err)
	}
	p := compileBitNFA(g)
	if p == nil {
		t.Fatal("位布局未生成")
	}
	if err := p.validate(); err != nil {
		t.Fatal(err)
	}
	p.sourceMask['a'][0] = 0
	if err := p.validate(); err == nil {
		t.Fatal("源状态掩码损坏未被检测")
	}
}

func TestBitLayoutPrunesUnreachableAcceptBranches(t *testing.T) {
	r, err := parser.Parse(`(?:a|b)c`)
	if err != nil {
		t.Fatal(err)
	}
	g, err := nfagraph.NewBuilder().Build(r)
	if err != nil {
		t.Fatal(err)
	}
	p := compileBitNFA(g)
	if p == nil {
		t.Fatal("位布局未生成")
	}
	if err := p.validate(); err != nil {
		t.Fatal(err)
	}
	if got := p.MatchAt([]byte("ac"), 0); fmt.Sprint(got) != "[2]" {
		t.Fatalf("有效分支被错误剪枝: %v", got)
	}
	if got := p.MatchAt([]byte("ab"), 0); len(got) != 0 {
		t.Fatalf("死分支产生结果: %v", got)
	}
}

func TestBitLayoutValidatesDeterministicPrefix(t *testing.T) {
	r, err := parser.Parse(`abcd`)
	if err != nil {
		t.Fatal(err)
	}
	g, err := nfagraph.NewBuilder().Build(r)
	if err != nil {
		t.Fatal(err)
	}
	e, err := CompileEngine(g, EngineLimEx)
	if err != nil || e.bitNFA == nil {
		t.Fatalf("位布局编译失败: %v", err)
	}
	if got := string(e.bitNFA.prefix); got != "abcd" {
		t.Fatalf("确定性前缀=%q", got)
	}
	e.bitNFA.prefix[0] = 'x'
	if err := e.Validate(); err == nil {
		t.Fatal("前缀布局损坏未被检测")
	}
}

func TestBitPrefixDoesNotSuppressEmptyAcceptance(t *testing.T) {
	r, err := parser.Parse(`a*`)
	if err != nil {
		t.Fatal(err)
	}
	g, err := nfagraph.NewBuilder().Build(r)
	if err != nil {
		t.Fatal(err)
	}
	e, err := CompileEngine(g, EngineLimEx)
	if err != nil {
		t.Fatal(err)
	}
	if e.bitNFA != nil && len(e.bitNFA.prefix) != 0 {
		t.Fatalf("可空图不应设置文字前缀: %q", e.bitNFA.prefix)
	}
	if got := e.MatchAt([]byte("b"), 0); len(got) == 0 || got[0] != 0 {
		t.Fatalf("空匹配被前缀过滤: %v", got)
	}
}

func TestRequiredLiteralPrefixStopsAtVariableLengthLoop(t *testing.T) {
	for _, pattern := range []string{`a+`, `(?:ab)+`} {
		r, err := parser.Parse(pattern)
		if err != nil {
			t.Fatal(err)
		}
		g, err := nfagraph.NewBuilder().Build(r)
		if err != nil {
			t.Fatal(err)
		}
		prefix := string(requiredLiteralPrefix(g))
		if prefix != "" {
			t.Fatalf("%s 含可变长度循环时不应设置固定前缀: %q", pattern, prefix)
		}
		e, err := CompileEngine(g, EngineLimEx)
		if err != nil {
			t.Fatal(err)
		}
		if got := e.MatchAt([]byte("ab"), 0); len(got) == 0 {
			t.Fatalf("%s 循环匹配被前缀过滤", pattern)
		}
	}
}

func TestCorruptDedicatedLayoutFallsBackToGenericMatcher(t *testing.T) {
	r, err := parser.Parse(`ab`)
	if err != nil {
		t.Fatal(err)
	}
	g, err := nfagraph.NewBuilder().Build(r)
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []EngineKind{EngineLimEx, EngineSheng, EngineMcSheng, EngineTamarama, EngineShufti, EngineTruffle} {
		t.Run(kind.String(), func(t *testing.T) {
			e, err := CompileEngine(g, kind)
			if err != nil {
				t.Fatal(err)
			}
			switch kind {
			case EngineLimEx:
				e.bitNFA.closure[0] = nil
			case EngineSheng:
				e.tableNFA.closure[0] = nil
			case EngineMcSheng:
				e.sparseNFA.closures[0] = nil
			case EngineTamarama:
				e.rangeNFA.closures[0] = nil
			case EngineShufti, EngineTruffle:
				e.nibbleNFA.closures[0] = nil
			default:
			}
			if got := e.MatchAt([]byte("ab"), 0); len(got) != 1 || got[0] != 2 {
				t.Fatalf("损坏专用布局未安全回退: %v", got)
			}
		})
	}
}

func TestMPVUsesCommonPrefixWithoutChangingAlternationResults(t *testing.T) {
	r, err := parser.Parse(`(?:abc|abd)`)
	if err != nil {
		t.Fatal(err)
	}
	g, err := nfagraph.NewBuilder().Build(r)
	if err != nil {
		t.Fatal(err)
	}
	e, err := CompileEngine(g, EngineMPV)
	if err != nil || e.mpv == nil {
		t.Fatalf("MPV 编译失败: %v", err)
	}
	if got := string(e.mpv.prefix); got != "ab" {
		t.Fatalf("MPV 公共前缀=%q", got)
	}
	if got := e.Spans([]byte("xxabdyy")); len(got) != 1 || got[0] != (Span{From: 2, To: 5}) {
		t.Fatalf("MPV 前缀过滤改变交替结果: %v", got)
	}
	e.mpv.prefix[0] = 'x'
	if err := e.Validate(); err == nil {
		t.Fatal("损坏的 MPV 前缀未被检测")
	}
}

func TestMPVResultBudgetPreservesEndOffsetOrder(t *testing.T) {
	r, err := parser.Parse(`(?:abcd|a)`)
	if err != nil {
		t.Fatal(err)
	}
	g, err := nfagraph.NewBuilder().Build(r)
	if err != nil {
		t.Fatal(err)
	}
	e, err := CompileEngine(g, EngineMPV)
	if err != nil {
		t.Fatal(err)
	}
	got, _, stopped := e.MatchAtBudget([]byte("abcd"), 0, 0, 1)
	if !stopped || len(got) != 1 || got[0] != 1 {
		t.Fatalf("MPV 结果预算未按结束偏移排序: got=%v stopped=%v", got, stopped)
	}
}

func TestDedicatedEngineCloneKeepsExecutionModel(t *testing.T) {
	r, err := parser.Parse(`[a-c][0-2]`)
	if err != nil {
		t.Fatal(err)
	}
	g, err := nfagraph.NewBuilder().Build(r)
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []EngineKind{EngineLimEx, EngineSheng, EngineMcSheng, EngineTamarama, EngineShufti, EngineTruffle} {
		t.Run(kind.String(), func(t *testing.T) {
			e, err := CompileEngine(g, kind)
			if err != nil {
				t.Fatal(err)
			}
			clone := e.Clone()
			if clone == nil || clone.ExecutionModel() != e.ExecutionModel() || !clone.Independent() {
				t.Fatalf("克隆后端退化: 原=%s 克隆=%s", e.ExecutionModel(), clone.ExecutionModel())
			}
			if got, want := clone.MatchAt([]byte("b1"), 0), e.MatchAt([]byte("b1"), 0); fmt.Sprint(got) != fmt.Sprint(want) {
				t.Fatalf("克隆结果不一致: got=%v want=%v", got, want)
			}
		})
	}
}

func TestLBRResultBudgetKeepsEndOrder(t *testing.T) {
	r, err := parser.Parse(`^(?:a|aa)`)
	if err != nil {
		t.Fatal(err)
	}
	g, err := nfagraph.NewBuilder().Build(r)
	if err != nil {
		t.Fatal(err)
	}
	e, err := CompileEngine(g, EngineLBR)
	if err != nil || e.lbr == nil {
		t.Fatalf("LBR 编译失败: %v", err)
	}
	got, _, stopped := e.MatchAtBudget([]byte("aa"), 0, 0, 1)
	if !stopped || len(got) != 1 || got[0] != 1 {
		t.Fatalf("LBR 结果预算顺序错误: got=%v stopped=%v", got, stopped)
	}
}

func TestBitLayoutTracksConsumableAndDeadStates(t *testing.T) {
	r, err := parser.Parse(`ab`)
	if err != nil {
		t.Fatal(err)
	}
	g, err := nfagraph.NewBuilder().Build(r)
	if err != nil {
		t.Fatal(err)
	}
	e, err := CompileEngine(g, EngineLimEx)
	if err != nil || e.bitNFA == nil {
		t.Fatalf("位布局编译失败: %v", err)
	}
	if err := e.bitNFA.validate(); err != nil {
		t.Fatal(err)
	}
	e.bitNFA.dead[0] ^= 1
	if err := e.bitNFA.validate(); err == nil {
		t.Fatal("死状态掩码损坏未被检测")
	}
}

func TestBitLayoutRejectsOutOfRangeStateMasks(t *testing.T) {
	r, err := parser.Parse(`a`)
	if err != nil {
		t.Fatal(err)
	}
	g, err := nfagraph.NewBuilder().Build(r)
	if err != nil {
		t.Fatal(err)
	}
	e, err := CompileEngine(g, EngineLimEx)
	if err != nil || e.bitNFA == nil {
		t.Fatalf("位布局编译失败: %v", err)
	}
	e.bitNFA.accept[len(e.bitNFA.accept)-1] |= ^uint64(0) << uint(len(e.bitNFA.trans)%64)
	if err := e.bitNFA.validate(); err == nil {
		t.Fatal("越界接受状态掩码未被检测")
	}
}

func TestMcShengClassExecutionUsesSparseStatePayload(t *testing.T) {
	r, err := parser.Parse(`[a-c][0-2]`)
	if err != nil {
		t.Fatal(err)
	}
	g, err := nfagraph.NewBuilder().Build(r)
	if err != nil {
		t.Fatal(err)
	}
	e, err := CompileEngine(g, EngineMcSheng)
	if err != nil || e.sparseNFA == nil {
		t.Fatalf("稀疏布局编译失败: %v", err)
	}
	for i, state := range e.sparseNFA.states {
		if state.kind == nfagraph.KindClass && len(e.sparseNFA.trans[i]) != 0 {
			t.Fatalf("字符类不应展开为字节映射: state=%d", i)
		}
	}
	if got := e.MatchAt([]byte("b1"), 0); len(got) != 1 || got[0] != 2 {
		t.Fatalf("字符类稀疏执行结果=%v", got)
	}
}

func TestVermicelliPrefixAndClassUseSparsePath(t *testing.T) {
	r, err := parser.Parse(`ab[0-2]`)
	if err != nil {
		t.Fatal(err)
	}
	g, err := nfagraph.NewBuilder().Build(r)
	if err != nil {
		t.Fatal(err)
	}
	e, err := CompileEngine(g, EngineVermicelli)
	if err != nil || e.sparseNFA == nil {
		t.Fatalf("稀疏文字布局编译失败: %v", err)
	}
	if string(e.sparseNFA.prefix) != "ab" {
		t.Fatalf("文字前缀=%q", e.sparseNFA.prefix)
	}
	for i, state := range e.sparseNFA.states {
		if state.kind == nfagraph.KindClass && len(e.sparseNFA.trans[i]) != 0 {
			t.Fatalf("字符类状态 %d 仍展开了稀疏映射", i)
		}
	}
	if got := e.MatchAt([]byte("ab2"), 0); len(got) != 1 || got[0] != 3 {
		t.Fatalf("稀疏字符类结果=%v", got)
	}
}

func TestTableLayoutValidatesSourceMasks(t *testing.T) {
	r, err := parser.Parse(`ab`)
	if err != nil {
		t.Fatal(err)
	}
	g, err := nfagraph.NewBuilder().Build(r)
	if err != nil {
		t.Fatal(err)
	}
	p := compileTableNFA(g)
	if p == nil {
		t.Fatal("表布局未生成")
	}
	if err := p.validate(); err != nil {
		t.Fatal(err)
	}
	p.sourceMask['a'][0] = 0
	if err := p.validate(); err == nil {
		t.Fatal("表源状态掩码损坏未被检测")
	}
}

func TestGenericProgramDeduplicatesAcceptOffsets(t *testing.T) {
	r, err := parser.Parse(`(?:a|a)`)
	if err != nil {
		t.Fatal(err)
	}
	g, err := nfagraph.NewBuilder().Build(r)
	if err != nil {
		t.Fatal(err)
	}
	p := &Program{Graph: g}
	if got := p.MatchAt([]byte("a"), 0); len(got) != 1 || got[0] != 1 {
		t.Fatalf("通用路径接受偏移重复: %v", got)
	}
}

func TestRepeatBackendConformanceForLiteralUnits(t *testing.T) {
	patterns := []string{`a{2,4}`, `(?:ab){2,4}`, `(?:abc){1,3}`, `a{0,3}`}
	inputs := [][]byte{[]byte(""), []byte("a"), []byte("aaaaa"), []byte("ababab"), []byte("abcabcabcx")}
	for _, pattern := range patterns {
		r, err := parser.Parse(pattern)
		if err != nil {
			t.Fatal(err)
		}
		g, err := nfagraph.NewBuilder().Build(r)
		if err != nil {
			t.Fatal(err)
		}
		e, err := CompileEngine(g, EngineRepeat)
		if err != nil {
			t.Fatal(err)
		}
		for _, data := range inputs {
			want := (&Program{Graph: g}).Spans(data)
			got := e.Spans(data)
			if !sameSpanResults(got, want) {
				t.Fatalf("pattern=%q data=%q got=%v want=%v model=%s", pattern, data, got, want, e.ExecutionModel())
			}
		}
	}
}

func TestVermicelliPrefixFilterPreservesAlternationResults(t *testing.T) {
	r, err := parser.Parse(`(?:abc|abd)`)
	if err != nil {
		t.Fatal(err)
	}
	g, err := nfagraph.NewBuilder().Build(r)
	if err != nil {
		t.Fatal(err)
	}
	e, err := CompileEngine(g, EngineVermicelli)
	if err != nil || e.sparseNFA == nil {
		t.Fatalf("Vermicelli 编译失败: %v", err)
	}
	if string(e.sparseNFA.prefix) != "ab" {
		t.Fatalf("公共前缀提取错误: %q", e.sparseNFA.prefix)
	}
	if got := e.MatchAt([]byte("abx"), 0); len(got) != 0 {
		t.Fatalf("错误前缀产生结果: %v", got)
	}
	if got := e.MatchAt([]byte("abd"), 0); len(got) != 1 || got[0] != 3 {
		t.Fatalf("有效前缀结果错误: %v", got)
	}
}

func TestLBRCommonPrefixAcrossAssertionsAndBranches(t *testing.T) {
	r, err := parser.Parse(`^(?:abc|abd)$`)
	if err != nil {
		t.Fatal(err)
	}
	g, err := nfagraph.NewBuilder().Build(r)
	if err != nil {
		t.Fatal(err)
	}
	e, err := CompileEngine(g, EngineLBR)
	if err != nil || e.lbr == nil {
		t.Fatalf("LBR 编译失败: %v", err)
	}
	if string(e.lbr.prefix) != "ab" {
		t.Fatalf("LBR 公共前缀=%q", e.lbr.prefix)
	}
	if got := e.MatchAt([]byte("abx"), 0); len(got) != 0 {
		t.Fatalf("错误分支产生结果: %v", got)
	}
	if got := e.MatchAt([]byte("abd"), 0); len(got) != 1 || got[0] != 3 {
		t.Fatalf("有效分支结果=%v", got)
	}
}

func TestEngineFamiliesRangeAndSerializationConformance(t *testing.T) {
	r, err := parser.Parse(`(?:a[0-2]|ab)`)
	if err != nil {
		t.Fatal(err)
	}
	g, err := nfagraph.NewBuilder().Build(r)
	if err != nil {
		t.Fatal(err)
	}
	data := []byte("za0abx")
	from, to := 1, 5
	ref := make([]Span, 0)
	for _, span := range (&Program{Graph: g}).Spans(data) {
		if span.From >= from && span.To <= to {
			ref = append(ref, span)
		}
	}
	for _, kind := range []EngineKind{EngineCastle, EngineGough, EngineLimEx, EngineSheng, EngineMcSheng, EngineTamarama, EngineVermicelli, EngineShufti, EngineTruffle, EngineLBR} {
		e, err := CompileEngine(g, kind)
		if err != nil {
			t.Fatalf("%s 编译失败: %v", kind, err)
		}
		if got := e.SpansRange(data, from, to); fmt.Sprint(got) != fmt.Sprint(ref) {
			t.Fatalf("%s 区间结果不一致: got=%v want=%v", kind, got, ref)
		}
		raw, err := e.Dump()
		if err != nil {
			t.Fatalf("%s 序列化失败: %v", kind, err)
		}
		loaded, err := LoadEngine(raw)
		if err != nil {
			t.Fatalf("%s 恢复失败: %v", kind, err)
		}
		if got := loaded.Spans(data); fmt.Sprint(got) != fmt.Sprint(e.Spans(data)) {
			t.Fatalf("%s 恢复后结果不一致: got=%v want=%v", kind, got, e.Spans(data))
		}
	}
}

func BenchmarkByteNFA(b *testing.B) {
	r, _ := parser.Parse(`(?:ab|a[0-9])`)
	g, _ := nfagraph.NewBuilder().Build(r)
	e, _ := CompileEngine(g, EngineLimEx)
	data := []byte("ab a7 ab a3 ab")
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		_ = e.MatchAt(data, 0)
	}
}

func BenchmarkEngineFamilies(b *testing.B) {
	r, _ := parser.Parse(`[a-z][0-9]`)
	g, _ := nfagraph.NewBuilder().Build(r)
	data := []byte("a7 z9 q1")
	for _, kind := range []EngineKind{EngineLimEx, EngineSheng, EngineMcSheng, EngineTamarama, EngineShufti, EngineTruffle, EngineVermicelli} {
		b.Run(kind.String(), func(b *testing.B) {
			e, err := CompileEngine(g, kind)
			if err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if len(e.Spans(data)) == 0 {
					b.Fatal("未产生匹配")
				}
			}
		})
	}
}

func BenchmarkSelectEngineKind(b *testing.B) {
	r, err := parser.Parse(`(?:ab|a[0-9]|xy){1,3}`)
	if err != nil {
		b.Fatal(err)
	}
	g, err := nfagraph.NewBuilder().Build(r)
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		_ = SelectEngineKind(g)
	}
}

func TestSelectEngineKindMatrix(t *testing.T) {
	cases := []struct {
		name string
		pat  string
		want EngineKind
	}{
		{name: "literal", pat: `abc`, want: EngineMPV},
		{name: "alternation", pat: `(?:cat|car|dog)`, want: EngineMPV},
		{name: "repeat", pat: `(?:ab){2,4}`, want: EngineRepeat},
		{name: "class", pat: `[a-z][0-9]`, want: EngineLimEx},
		{name: "mixed", pat: `(?:ab|a[0-9])`, want: EngineLimEx},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, err := parser.Parse(tc.pat)
			if err != nil {
				t.Fatal(err)
			}
			g, err := nfagraph.NewBuilder().Build(r)
			if err != nil {
				t.Fatal(err)
			}
			got := SelectEngineKind(g)
			if got != tc.want {
				t.Fatalf("选择=%s, want=%s", got, tc.want)
			}
			e, err := CompileEngine(g, got)
			if err != nil || !e.SupportsGraph(g) {
				t.Fatalf("选择的后端不可执行: %v", err)
			}
		})
	}
}

func TestExplainEngineSelectionReportsCostAndModel(t *testing.T) {
	r, err := parser.Parse(`(?:ab|cd)+`)
	if err != nil {
		t.Fatal(err)
	}
	g, err := nfagraph.NewBuilder().Build(r)
	if err != nil {
		t.Fatal(err)
	}
	info := ExplainEngineSelection(g)
	if !info.Kind.Valid() || info.States == 0 || info.Edges == 0 || info.Memory == 0 || info.Reason == "" || !info.Independent {
		t.Fatalf("选择诊断不完整: %#v", info)
	}
}

func TestSelectEngineKindRejectsInvalidGraph(t *testing.T) {
	if got := SelectEngineKind(nil); got != 0 {
		t.Fatalf("空图选择=%d", got)
	}
	g := &nfagraph.Graph{}
	if got := SelectEngineKind(g); got != 0 {
		t.Fatalf("非法图选择=%d", got)
	}
}

func TestExecutionModelReportsFallbackForUnsupportedDedicatedGraph(t *testing.T) {
	r, _ := parser.Parse(`^a`)
	g, _ := nfagraph.NewBuilder().Build(r)
	e, err := CompileEngine(g, EngineShufti)
	if err != nil {
		t.Fatal(err)
	}
	if e.ExecutionModel() != "generic-nfa-fallback" {
		t.Fatalf("不支持图误报专用模型: %s", e.ExecutionModel())
	}
}

func TestSelectEngineKindAppliesCastleScaleLimit(t *testing.T) {
	r, err := parser.Parse(strings.Repeat("a", 4200))
	if err != nil {
		t.Fatal(err)
	}
	g, err := nfagraph.NewBuilder().Build(r)
	if err != nil {
		t.Fatal(err)
	}
	if got := SelectEngineKind(g); got == EngineCastle {
		t.Fatal("超大图不应选择 Castle")
	}
}

func TestCompileAutoBuildsSelectedBackend(t *testing.T) {
	r, err := parser.Parse(`(?:ab|cd)`)
	if err != nil {
		t.Fatal(err)
	}
	g, err := nfagraph.NewBuilder().Build(r)
	if err != nil {
		t.Fatal(err)
	}
	e, err := CompileAuto(g)
	if err != nil || !e.Independent() || e.ExecutionModel() == "generic-nfa-fallback" {
		t.Fatalf("自动引擎=%v", err)
	}
	if _, err := CompileAuto(nil); err == nil {
		t.Fatal("非法图未拒绝")
	}
	assertion, _ := parser.Parse(`^a`)
	assertionGraph, _ := nfagraph.NewBuilder().Build(assertion)
	fallback, err := CompileAuto(assertionGraph)
	if err != nil || fallback.Independent() || fallback.ExecutionModel() != "generic-nfa-fallback" {
		t.Fatalf("不支持图未安全回退: engine=%v err=%v", fallback, err)
	}
}

func TestUnsupportedSpecializedGraphReportsGenericFallback(t *testing.T) {
	r, err := parser.Parse(`^a`)
	if err != nil {
		t.Fatal(err)
	}
	g, err := nfagraph.NewBuilder().Build(r)
	if err != nil {
		t.Fatal(err)
	}
	e, err := CompileEngine(g, EngineLimEx)
	if err != nil {
		t.Fatal(err)
	}
	if e.Independent() || e.ExecutionModel() != "generic-nfa-fallback" {
		t.Fatalf("不支持图错误标记: independent=%v model=%q", e.Independent(), e.ExecutionModel())
	}
}

func TestSpecializedSpansAndBudgetBoundaries(t *testing.T) {
	r, _ := parser.Parse(`[a-z]+`)
	g, _ := nfagraph.NewBuilder().Build(r)
	e, err := CompileEngine(g, EngineShufti)
	if err != nil {
		t.Fatal(err)
	}
	if got := e.SpansLimit([]byte("ab cd"), 2); len(got) != 2 || got[0] != (Span{0, 1}) {
		t.Fatalf("表后端区间=%v", got)
	}
	if got, steps, stopped := e.MatchAtBudget([]byte("abc"), -1, 10, 0); got != nil || steps != 0 || stopped {
		t.Fatalf("非法起点预算=%v,%d,%v", got, steps, stopped)
	}
	if got, steps, stopped := e.MatchAtBudget([]byte("abc"), 0, 1, 0); !stopped || steps <= 1 || got != nil {
		t.Fatalf("步骤预算=%v,%d,%v", got, steps, stopped)
	}
}

func TestEngineSpansBudgetUsesPerStartBudget(t *testing.T) {
	r, _ := parser.Parse(`a*`)
	g, _ := nfagraph.NewBuilder().Build(r)
	e, err := CompileEngine(g, EngineRepeat)
	if err != nil {
		t.Fatal(err)
	}
	spans, steps, stopped := e.SpansBudget([]byte("aaa"), 3, 0)
	if !stopped || steps < 3 || len(spans) != 0 {
		t.Fatalf("预算结果=%v,%d,%v", spans, steps, stopped)
	}
	spans, steps, stopped = e.SpansBudget([]byte("aaa"), 0, 2)
	if !stopped || len(spans) != 2 || steps == 0 {
		t.Fatalf("结果预算=%v,%d,%v", spans, steps, stopped)
	}
}

// 有限长度图在输入不足时应在进入后端前拒绝，避免无效状态步。
func TestFiniteLengthBoundsRejectShortInput(t *testing.T) {
	r, err := parser.Parse(`abcdef`)
	if err != nil {
		t.Fatal(err)
	}
	g, err := nfagraph.NewBuilder().Build(r)
	if err != nil {
		t.Fatal(err)
	}
	e, err := CompileEngine(g, EngineLimEx)
	if err != nil {
		t.Fatal(err)
	}
	if got, steps, stopped := e.MatchAtBudget([]byte("abc"), 0, 100, 0); got != nil || steps != 0 || stopped {
		t.Fatalf("短输入未提前拒绝: got=%v steps=%d stopped=%v", got, steps, stopped)
	}
}

func TestProgramClassMaskMetadataRoundTrip(t *testing.T) {
	r, err := parser.Parse(`[^a-c]x`)
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
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	clone := p.Clone()
	if err := clone.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, input := range [][]byte{[]byte("dx"), []byte("ax"), []byte("\x00x")} {
		if got, want := clone.MatchAt(input, 0), p.MatchAt(input, 0); fmt.Sprint(got) != fmt.Sprint(want) {
			t.Fatalf("掩码克隆结果=%v want=%v", got, want)
		}
	}
}

func TestProgramCloneNilGraphIsSafe(t *testing.T) {
	if got := (&Program{}).Clone(); got != nil {
		t.Fatalf("空图克隆应返回 nil，得到 %v", got)
	}
}

func TestEngineFamiliesHonorFiniteLengthBounds(t *testing.T) {
	r, err := parser.Parse(`abc[0-9]`)
	if err != nil {
		t.Fatal(err)
	}
	g, err := nfagraph.NewBuilder().Build(r)
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []EngineKind{EngineCastle, EngineGough, EngineLimEx, EngineSheng, EngineMcSheng, EngineTamarama, EngineShufti, EngineTruffle} {
		e, err := CompileEngine(g, kind)
		if err != nil {
			t.Fatal(kind, err)
		}
		if got, steps, stopped := e.MatchAtBudget([]byte("abc"), 0, 100, 0); got != nil || steps != 0 || stopped {
			t.Fatalf("%s 短输入未剪枝: got=%v steps=%d stopped=%v", kind, got, steps, stopped)
		}
	}
}

func TestEngineMatrixConsistency(t *testing.T) {
	r, _ := parser.Parse("(?:ab|cd)+")
	g, _ := nfagraph.NewBuilder().Build(r)
	for k := EngineCastle; k <= EngineLBR; k++ {
		e, err := CompileEngine(g, k)
		if err != nil {
			t.Fatal(k, err)
		}
		if len(e.MatchAt([]byte("abcd"), 0)) == 0 {
			t.Fatal(k)
		}
	}
}

func TestEngineValidationAndClone(t *testing.T) {
	g, _ := nfagraph.NewBuilder().Build(parser.Literal{Value: []byte("x")})
	e, err := CompileEngine(g, EngineCastle)
	if err != nil || e.Validate() != nil || e.StateCount() == 0 {
		t.Fatal(err)
	}
	if e.Clone() == nil {
		t.Fatal("引擎克隆失败")
	}
	if _, err := CompileEngine(g, EngineKind(99)); err == nil {
		t.Fatal("非法引擎类型未拒绝")
	}
}

func TestEngineValidationRejectsBackendKindMismatch(t *testing.T) {
	r, _ := parser.Parse(`a{2}`)
	g, _ := nfagraph.NewBuilder().Build(r)
	e, _ := CompileEngine(g, EngineRepeat)
	e.Kind = EngineMPV
	if err := e.Validate(); err == nil {
		t.Fatal("后端类型错配未被拒绝")
	}
}

func TestDedicatedLayoutsRejectCorruptClosureTargets(t *testing.T) {
	r, err := parser.Parse(`[a-z]`)
	if err != nil {
		t.Fatal(err)
	}
	g, err := nfagraph.NewBuilder().Build(r)
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []EngineKind{EngineLimEx, EngineSheng, EngineMcSheng, EngineTamarama, EngineShufti, EngineTruffle} {
		e, err := CompileEngine(g, kind)
		if err != nil {
			t.Fatalf("%s 编译失败: %v", kind, err)
		}
		switch {
		case e.bitNFA != nil:
			e.bitNFA.closure[0] = []uint64{^uint64(0)}
		case e.rangeNFA != nil:
			e.rangeNFA.closures[0] = []int{len(e.rangeNFA.states) + 1}
		case e.sparseNFA != nil:
			e.sparseNFA.closures[0] = []int{len(e.sparseNFA.states) + 1}
		case e.tableNFA != nil:
			e.tableNFA.closure[0] = []int{len(e.tableNFA.trans) + 1}
		case e.nibbleNFA != nil:
			e.nibbleNFA.closures[0] = []int{len(e.nibbleNFA.states) + 1}
		default:
			t.Fatalf("%s 未建立专用布局", kind)
		}
		if err := e.Validate(); err == nil {
			t.Fatalf("%s 损坏闭包未拒绝", kind)
		}
	}
}

func TestLBRBudgetStopsByStateExpansion(t *testing.T) {
	r, err := parser.Parse(`^a$`)
	if err != nil {
		t.Fatal(err)
	}
	g, err := nfagraph.NewBuilder().Build(r)
	if err != nil {
		t.Fatal(err)
	}
	e, err := CompileEngine(g, EngineLBR)
	if err != nil || e.lbr == nil {
		t.Fatalf("LBR 编译失败: %v", err)
	}
	if got, steps, stopped := e.MatchAtBudget([]byte("a"), 0, 1, 0); !stopped || got != nil || steps <= 1 {
		t.Fatalf("状态预算=%v,%d,%v", got, steps, stopped)
	}
	if got, _, stopped := e.MatchAtBudget([]byte("a"), 0, 100, 1); !stopped || len(got) != 1 {
		t.Fatalf("结果预算=%v,%v", got, stopped)
	}
}

func TestLBRRejectsUnsupportedAssertionKind(t *testing.T) {
	r, err := parser.Parse(`(?=a)b`)
	if err != nil {
		t.Fatal(err)
	}
	g, err := nfagraph.NewBuilder().Build(r)
	if err != nil {
		t.Fatal(err)
	}
	e, err := CompileEngine(g, EngineLBR)
	if err != nil {
		t.Fatal(err)
	}
	if e.lbr != nil || e.SupportsGraph(g) {
		t.Fatal("不支持的前瞻断言进入 LBR")
	}
}

func TestGoughBitLayoutRejectsMaskMismatch(t *testing.T) {
	r, err := parser.Parse(`ab`)
	if err != nil {
		t.Fatal(err)
	}
	g, err := nfagraph.NewBuilder().Build(r)
	if err != nil {
		t.Fatal(err)
	}
	e, err := CompileEngine(g, EngineGough)
	if err != nil || e.gough == nil {
		t.Fatalf("Gough 编译失败: %v", err)
	}
	if len(e.gough.predMask) == 0 {
		t.Fatal("Gough 前驱掩码为空")
	}
	e.gough.predMask[0][0] ^= 1
	if err := e.Validate(); err == nil {
		t.Fatal("损坏的 Gough 前驱掩码未被检测")
	}
}

func TestCastleRejectsInvalidExecutionLimit(t *testing.T) {
	r, err := parser.Parse(`ab`)
	if err != nil {
		t.Fatal(err)
	}
	g, err := nfagraph.NewBuilder().Build(r)
	if err != nil {
		t.Fatal(err)
	}
	e, err := CompileEngine(g, EngineCastle)
	if err != nil || e.castle == nil {
		t.Fatalf("Castle 编译失败: %v", err)
	}
	e.castle.queueLimit = 0
	if err := e.Validate(); err == nil {
		t.Fatal("无效队列上限未被拒绝")
	}
}

func TestCastleGoughComplexBranchAndLoopConformance(t *testing.T) {
	r, err := parser.Parse(`(?:a|ab)*`)
	if err != nil {
		t.Fatal(err)
	}
	g, err := nfagraph.NewBuilder().Build(r)
	if err != nil {
		t.Fatal(err)
	}
	ref := (&Program{Graph: g}).Spans([]byte("ababa"))
	for _, kind := range []EngineKind{EngineCastle, EngineGough} {
		e, err := CompileEngine(g, kind)
		if err != nil {
			t.Fatalf("%s 编译失败: %v", kind, err)
		}
		if err := e.Validate(); err != nil {
			t.Fatalf("%s 布局校验失败: %v", kind, err)
		}
		if got := e.Spans([]byte("ababa")); !sameSpanResults(got, ref) {
			t.Fatalf("%s 复杂分支循环结果=%v want=%v", kind, got, ref)
		}
		if got, _, stopped := e.MatchAtBudget([]byte("ababa"), 0, 2, 0); !stopped || got != nil {
			t.Fatalf("%s 步骤预算结果=%v stopped=%v", kind, got, stopped)
		}
	}
}

func TestLBRRejectsInvalidQueueLimit(t *testing.T) {
	r, err := parser.Parse(`^a$`)
	if err != nil {
		t.Fatal(err)
	}
	g, err := nfagraph.NewBuilder().Build(r)
	if err != nil {
		t.Fatal(err)
	}
	e, err := CompileEngine(g, EngineLBR)
	if err != nil || e.lbr == nil {
		t.Fatalf("LBR 编译失败: %v", err)
	}
	e.lbr.queueLimit = 0
	if err := e.Validate(); err == nil {
		t.Fatal("无效队列上限未被拒绝")
	}
}

func TestLBRPrefixFilterPreservesAnchoredResults(t *testing.T) {
	r, err := parser.Parse(`^abc$`)
	if err != nil {
		t.Fatal(err)
	}
	g, err := nfagraph.NewBuilder().Build(r)
	if err != nil {
		t.Fatal(err)
	}
	e, err := CompileEngine(g, EngineLBR)
	if err != nil || e.lbr == nil {
		t.Fatalf("LBR 编译失败: %v", err)
	}
	if string(e.lbr.prefix) != "abc" {
		t.Fatalf("LBR 前缀=%q", e.lbr.prefix)
	}
	if got, steps, stopped := e.MatchAtBudget([]byte("xbc"), 0, 0, 0); len(got) != 0 || steps != 0 || stopped {
		t.Fatalf("错误前缀未被快速排除: got=%v steps=%d stopped=%v", got, steps, stopped)
	}
	if got := e.MatchAt([]byte("abc"), 0); len(got) != 1 || got[0] != 3 {
		t.Fatalf("有效前缀结果=%v", got)
	}
}

func TestCastleBudgetReportsActualWork(t *testing.T) {
	r, err := parser.Parse(`(?:ab|a[0-9])`)
	if err != nil {
		t.Fatal(err)
	}
	g, err := nfagraph.NewBuilder().Build(r)
	if err != nil {
		t.Fatal(err)
	}
	e, err := CompileEngine(g, EngineCastle)
	if err != nil || e.castle == nil {
		t.Fatalf("Castle 编译失败: %v", err)
	}
	ctx := &Context{MaxSteps: 100, MaxResults: 1}
	if got := e.MatchAtWithContext([]byte("ab"), 0, ctx); len(got) != 1 || ctx.Results != 1 || ctx.Steps == 0 {
		t.Fatalf("Castle 预算=%v,%+v", got, ctx)
	}
}

func TestEngineCloneHandlesMissingProgram(t *testing.T) {
	clone := (&Engine{Kind: EngineCastle}).Clone()
	if clone == nil || clone.Program != nil {
		t.Fatalf("克隆结果=%#v", clone)
	}
}

func TestEngineMatchLimit(t *testing.T) {
	g, _ := nfagraph.NewBuilder().Build(parser.Literal{Value: []byte("abc")})
	e, _ := CompileEngine(g, EngineCastle)
	if got := e.MatchAt([]byte("abc"), 0); len(got) != 1 {
		t.Fatal(got)
	}
	if e.StateCount() < 2 {
		t.Fatal(e.StateCount())
	}
}

func TestEngineContextAndCapabilities(t *testing.T) {
	r, _ := parser.Parse("a")
	g, _ := nfagraph.NewBuilder().Build(r)
	e, _ := CompileEngine(g, EngineLimEx)
	ctx := &Context{MaxResults: 1}
	if got := e.MatchAtWithContext([]byte("a"), 0, ctx); len(got) != 1 || ctx.Results != 1 || !ctx.Stopped || !e.Capabilities()["simd"] {
		t.Fatalf("上下文=%v,%#v", got, ctx)
	}
}

func TestEngineSupportsGraph(t *testing.T) {
	r, _ := parser.Parse("a")
	g, _ := nfagraph.NewBuilder().Build(r)
	e, _ := CompileEngine(g, EngineRepeat)
	if !e.SupportsGraph(g) {
		t.Fatal("重复引擎拒绝基础图")
	}
}

func TestRepeatEngineUsesDedicatedProgram(t *testing.T) {
	r, err := parser.Parse(`a{2,4}`)
	if err != nil {
		t.Fatal(err)
	}
	g, err := nfagraph.NewBuilder().Build(r)
	if err != nil {
		t.Fatal(err)
	}
	e, err := CompileEngine(g, EngineRepeat)
	if err != nil {
		t.Fatal(err)
	}
	if e.repeat == nil || e.ExecutionModel() != "repeat" {
		t.Fatalf("执行模型=%s", e.ExecutionModel())
	}
	got := e.MatchAt([]byte("aaaaa"), 0)
	want := []int{2, 3, 4}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("结果=%v, want=%v", got, want)
	}
	if !e.SupportsGraph(g) {
		t.Fatal("重复图未被接受")
	}
}

func TestRepeatEnginePreservesGreedyMode(t *testing.T) {
	r, err := parser.Parse(`a{2,4}?`)
	if err != nil {
		t.Fatal(err)
	}
	g, err := nfagraph.NewBuilder().Build(r)
	if err != nil {
		t.Fatal(err)
	}
	e, err := CompileEngine(g, EngineRepeat)
	if err != nil || e.repeat == nil || e.repeat.Greedy {
		t.Fatalf("非贪婪属性丢失: %v", err)
	}
	if end, ok := e.repeat.MatchFirst([]byte("aaaa"), 0); !ok || end != 2 {
		t.Fatalf("最短结果=%d,%v", end, ok)
	}
}

func TestRepeatEngineSupportsMultiByteUnit(t *testing.T) {
	r, err := parser.Parse(`(?:ab){2,3}`)
	if err != nil {
		t.Fatal(err)
	}
	g, err := nfagraph.NewBuilder().Build(r)
	if err != nil {
		t.Fatal(err)
	}
	e, err := CompileEngine(g, EngineRepeat)
	if err != nil || e.repeat == nil {
		t.Fatalf("多字节重复未编译: %v", err)
	}
	if got := e.MatchAt([]byte("ababab"), 0); fmt.Sprint(got) != "[4 6]" {
		t.Fatalf("结束偏移=%v", got)
	}
}

func TestRepeatEngineRejectsPrefixedMixedUnit(t *testing.T) {
	r, _ := parser.Parse(`ab{2,3}`)
	g, _ := nfagraph.NewBuilder().Build(r)
	e, _ := CompileEngine(g, EngineRepeat)
	if e.repeat != nil || e.SupportsGraph(g) {
		t.Fatal("带前缀的混合重复不应误识别")
	}
}

func TestGoughBudgetStopsAndPreservesResults(t *testing.T) {
	r, err := parser.Parse(`ab`)
	if err != nil {
		t.Fatal(err)
	}
	g, err := nfagraph.NewBuilder().Build(r)
	if err != nil {
		t.Fatal(err)
	}
	e, err := CompileEngine(g, EngineGough)
	if err != nil {
		t.Fatal(err)
	}
	if got, steps, stopped := e.MatchAtBudget([]byte("ab"), 0, 1, 0); !stopped || got != nil || steps <= 1 {
		t.Fatalf("步骤预算未生效: got=%v steps=%d stopped=%v", got, steps, stopped)
	}
	if got, _, stopped := e.MatchAtBudget([]byte("ab"), 0, 0, 1); !stopped || fmt.Sprint(got) != "[2]" {
		t.Fatalf("结果预算语义错误: got=%v stopped=%v", got, stopped)
	}
}

func TestSparseBudgetMatchesUnboundedResult(t *testing.T) {
	r, err := parser.Parse(`ab`)
	if err != nil {
		t.Fatal(err)
	}
	g, err := nfagraph.NewBuilder().Build(r)
	if err != nil {
		t.Fatal(err)
	}
	e, err := CompileEngine(g, EngineSheng)
	if err != nil || e.sparseNFA == nil {
		t.Skip("当前图由表布局消费")
	}
	want := e.MatchAt([]byte("ab"), 0)
	got, _, stopped := e.MatchAtBudget([]byte("ab"), 0, 0, 0)
	if stopped || fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("稀疏预算结果不一致: got=%v want=%v stopped=%v", got, want, stopped)
	}
}

func TestMPVEngineVerifiesLiteralAlternation(t *testing.T) {
	r, err := parser.Parse(`(?:cat|car|dog)`)
	if err != nil {
		t.Fatal(err)
	}
	g, err := nfagraph.NewBuilder().Build(r)
	if err != nil {
		t.Fatal(err)
	}
	e, err := CompileEngine(g, EngineMPV)
	if err != nil {
		t.Fatal(err)
	}
	if e.mpv == nil || e.ExecutionModel() != "mpv" {
		t.Fatalf("执行模型=%s", e.ExecutionModel())
	}
	if got := e.MatchAt([]byte("car"), 0); fmt.Sprint(got) != "[3]" {
		t.Fatalf("结果=%v", got)
	}
	if got := e.MatchAt([]byte("cow"), 0); len(got) != 0 {
		t.Fatalf("误报=%v", got)
	}
}

func TestMPVEngineDeduplicatesEquivalentBranches(t *testing.T) {
	r, err := parser.Parse(`(?:foo|foo|bar)`)
	if err != nil {
		t.Fatal(err)
	}
	g, err := nfagraph.NewBuilder().Build(r)
	if err != nil {
		t.Fatal(err)
	}
	e, err := CompileEngine(g, EngineMPV)
	if err != nil || e.mpv == nil {
		t.Fatalf("MPV 编译失败: %v", err)
	}
	if got := e.MatchAt([]byte("foo"), 0); fmt.Sprint(got) != "[3]" {
		t.Fatalf("重复分支结果=%v", got)
	}
}

func TestMPVSpansBudgetIsGlobal(t *testing.T) {
	r, _ := parser.Parse(`(?:ab|cd)`)
	g, _ := nfagraph.NewBuilder().Build(r)
	e, err := CompileEngine(g, EngineMPV)
	if err != nil {
		t.Fatal(err)
	}
	spans, steps, stopped := e.SpansBudget([]byte("ab cd"), 0, 1)
	if !stopped || len(spans) != 1 || steps == 0 {
		t.Fatalf("MPV 全局预算=%v,%d,%v", spans, steps, stopped)
	}
}

func TestByteEngineFamiliesMatchReference(t *testing.T) {
	r, err := parser.Parse(`(?:ab|a[0-9])`)
	if err != nil {
		t.Fatal(err)
	}
	g, err := nfagraph.NewBuilder().Build(r)
	if err != nil {
		t.Fatal(err)
	}
	ref, err := CompileEngine(g, EngineCastle)
	if err != nil {
		t.Fatal(err)
	}
	for k := EngineLimEx; k <= EngineLBR; k++ {
		e, err := CompileEngine(g, k)
		if err != nil {
			t.Fatal(k, err)
		}
		if e.ExecutionModel() == "generic-nfa-fallback" {
			// 专用布局无法承载的图必须明确回退。
			continue
		}
		if !e.Independent() {
			t.Fatalf("引擎 %s 未建立专用状态", k)
		}
		for _, input := range []string{"ab", "a7", "ax", ""} {
			got, want := e.MatchAt([]byte(input), 0), ref.MatchAt([]byte(input), 0)
			if fmt.Sprint(got) != fmt.Sprint(want) {
				t.Fatalf("引擎 %s 输入 %q: got=%v want=%v", k, input, got, want)
			}
		}
	}
}

func TestTableBackendsCoverShengFamilies(t *testing.T) {
	r, err := parser.Parse(`[a-z][0-9]`)
	if err != nil {
		t.Fatal(err)
	}
	g, err := nfagraph.NewBuilder().Build(r)
	if err != nil {
		t.Fatal(err)
	}
	checks := []struct {
		kind EngineKind
		ok   func(*Engine) bool
	}{
		{EngineSheng, func(e *Engine) bool { return e.tableNFA != nil }},
		{EngineMcSheng, func(e *Engine) bool { return e.sparseNFA != nil }},
		{EngineTamarama, func(e *Engine) bool { return e.rangeNFA != nil }},
	}
	for _, check := range checks {
		e, err := CompileEngine(g, check.kind)
		if err != nil || !check.ok(e) {
			t.Fatalf("%s 未建立独立状态布局: %v", check.kind, err)
		}
		if got := e.MatchAt([]byte("a7"), 0); len(got) != 1 || got[0] != 2 {
			t.Fatalf("%s 匹配结果=%v", check.kind, got)
		}
	}
}

func TestByteEngineExpandsMultiByteLiteral(t *testing.T) {
	r, _ := parser.Parse(`abcd`)
	g, _ := nfagraph.NewBuilder().Build(r)
	e, _ := CompileEngine(g, EngineLimEx)
	if e.byteNFA == nil && e.bitNFA == nil || e.StateCount() < 4 {
		t.Fatalf("多字节文字未展开: states=%d", e.StateCount())
	}
	if got := e.MatchAt([]byte("abcd"), 0); fmt.Sprint(got) != "[4]" {
		t.Fatalf("结果=%v", got)
	}
}

func TestBitEngineBudgetAndSerialization(t *testing.T) {
	r, _ := parser.Parse(`[a-z][0-9]`)
	g, _ := nfagraph.NewBuilder().Build(r)
	e, _ := CompileEngine(g, EngineLimEx)
	if e.bitNFA == nil {
		t.Fatal("位集合后端未建立")
	}
	if end, steps, stopped := e.MatchAtBudget([]byte("a7"), 0, 100, 1); len(end) != 1 || steps == 0 || !stopped {
		t.Fatalf("预算=%v,%d,%v", end, steps, stopped)
	}
	raw, err := e.Dump()
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadEngine(raw)
	if err != nil || loaded.bitNFA == nil {
		t.Fatalf("恢复失败: %v", err)
	}
}

func TestBitEngineStateAndEdgeMetrics(t *testing.T) {
	r, _ := parser.Parse(`(?:ab|a[0-9])`)
	g, _ := nfagraph.NewBuilder().Build(r)
	e, _ := CompileEngine(g, EngineLimEx)
	if e.bitNFA == nil || e.StateCount() == 0 || e.EdgeCount() == 0 || e.MemoryBytes() == 0 {
		t.Fatalf("位集合指标=%d,%d,%d", e.StateCount(), e.EdgeCount(), e.MemoryBytes())
	}
	if got := e.SpansLimit([]byte("ab a7"), 2); len(got) == 0 {
		t.Fatal("位集合未返回区间")
	}
}

func TestByteTransitionCacheMatchesReferenceAcrossBytes(t *testing.T) {
	r, _ := parser.Parse(`[a-z][0-9]`)
	g, _ := nfagraph.NewBuilder().Build(r)
	e, _ := CompileEngine(g, EngineLimEx)
	ref, _ := CompileEngine(g, EngineCastle)
	for a := range byte(128) {
		for b := range byte(128) {
			input := []byte{a, b}
			if fmt.Sprint(e.MatchAt(input, 0)) != fmt.Sprint(ref.MatchAt(input, 0)) {
				t.Fatalf("输入=%v", input)
			}
		}
	}
}

func TestTableEngineFamiliesMatchReference(t *testing.T) {
	r, _ := parser.Parse(`[a-z][0-9]`)
	g, _ := nfagraph.NewBuilder().Build(r)
	ref, _ := CompileEngine(g, EngineCastle)
	for _, k := range []EngineKind{EngineShufti, EngineTruffle, EngineVermicelli} {
		e, _ := CompileEngine(g, k)
		if !e.Independent() {
			t.Fatalf("引擎 %s 未建立专用状态", k)
		}
		for _, input := range []string{"a7", "z0", "A1", "aX"} {
			if fmt.Sprint(e.MatchAt([]byte(input), 0)) != fmt.Sprint(ref.MatchAt([]byte(input), 0)) {
				t.Fatalf("引擎 %s 输入=%q", k, input)
			}
		}
	}
}

func TestTableEngineCloneAndMetrics(t *testing.T) {
	r, _ := parser.Parse(`[a-z][0-9]`)
	g, _ := nfagraph.NewBuilder().Build(r)
	e, _ := CompileEngine(g, EngineShufti)
	clone := e.Clone()
	if clone == nil || clone.nibbleNFA == nil || clone.StateCount() != e.StateCount() || clone.MemoryBytes() == 0 {
		t.Fatal("表引擎克隆或指标错误")
	}
	if err := clone.Validate(); err != nil {
		t.Fatalf("表引擎克隆校验失败: %v", err)
	}
}

func TestTableEngineRejectsUnicodeGraph(t *testing.T) {
	r, _ := parser.Parse(`\p{L}`)
	g, _ := nfagraph.NewBuilder().Build(r)
	e, _ := CompileEngine(g, EngineShufti)
	if e.tableNFA != nil || e.SupportsGraph(g) {
		t.Fatal("Unicode 图不应进入表引擎")
	}
}

func TestTableEngineBudgetAndRange(t *testing.T) {
	r, _ := parser.Parse(`[a-z][0-9]`)
	g, _ := nfagraph.NewBuilder().Build(r)
	e, _ := CompileEngine(g, EngineShufti)
	ends, steps, stopped := e.MatchAtBudget([]byte("a7"), 0, 100, 1)
	if len(ends) != 1 || steps == 0 || !stopped {
		t.Fatalf("表引擎预算=%v,%d,%v", ends, steps, stopped)
	}
	if got := e.SpansRange([]byte("xab1"), 1, 4); len(got) == 0 {
		t.Fatal("表引擎区间结果为空")
	}
}

func TestSpecializedLayoutsRejectCorruptTransitions(t *testing.T) {
	r, _ := parser.Parse(`[a-z]`)
	g, _ := nfagraph.NewBuilder().Build(r)
	e, err := CompileEngine(g, EngineShufti)
	if err != nil || e.nibbleNFA == nil {
		t.Fatalf("半字节布局未建立: %v", err)
	}
	clone := e.Clone()
	clone.nibbleNFA.states[0].next = []int{len(clone.nibbleNFA.states) + 1}
	if err := clone.Validate(); err == nil {
		t.Fatal("损坏的半字节转移未被拒绝")
	}
	shufti, err := CompileEngine(g, EngineShufti)
	if err != nil || shufti.nibbleNFA == nil {
		t.Fatalf("半字节布局未建立: %v", err)
	}
	mc, err := CompileEngine(g, EngineMcSheng)
	if err != nil || mc.sparseNFA == nil {
		t.Fatalf("稀疏布局未建立: %v", err)
	}
	mc.sparseNFA.trans[0][byte('a')] = []int{len(mc.sparseNFA.states) + 1}
	if err := mc.Validate(); err == nil {
		t.Fatal("越界稀疏转移未被拒绝")
	}
	b, err := CompileEngine(g, EngineLimEx)
	if err != nil || b.bitNFA == nil {
		t.Fatalf("位集合布局未建立: %v", err)
	}
	b.bitNFA.trans[0] = b.bitNFA.trans[0][:1]
	if err := b.Validate(); err == nil {
		t.Fatal("损坏的位集合宽度未被拒绝")
	}
}

func TestSpecializedFamiliesKeepDedicatedExecutionLayouts(t *testing.T) {
	r, _ := parser.Parse(`(?:[a-z]|[0-9])x`)
	g, _ := nfagraph.NewBuilder().Build(r)
	cases := []struct {
		kind  EngineKind
		check func(*Engine) bool
	}{
		{EngineSheng, func(e *Engine) bool { return e.tableNFA != nil && e.sparseNFA == nil && e.rangeNFA == nil }},
		{EngineMcSheng, func(e *Engine) bool { return e.sparseNFA != nil }},
		{EngineTamarama, func(e *Engine) bool { return e.rangeNFA != nil }},
		{EngineShufti, func(e *Engine) bool { return e.nibbleNFA != nil && e.rangeNFA == nil && e.tableNFA == nil }},
		{EngineTruffle, func(e *Engine) bool { return e.nibbleNFA != nil && e.rangeNFA == nil && e.tableNFA == nil }},
		{EngineVermicelli, func(e *Engine) bool { return e.sparseNFA != nil && e.tableNFA == nil && e.rangeNFA == nil }},
	}
	for _, tc := range cases {
		e, err := CompileEngine(g, tc.kind)
		if err != nil || !tc.check(e) {
			t.Fatalf("%s 未保留专用布局: %v", tc.kind, err)
		}
		if got := e.MatchAt([]byte("ax"), 0); fmt.Sprint(got) != "[2]" {
			t.Fatalf("%s 结果=%v", tc.kind, got)
		}
	}
}

func TestShuftiUsesNibbleLayoutForAllEntryPoints(t *testing.T) {
	r, err := parser.Parse(`[a-f][0-9]`)
	if err != nil {
		t.Fatal(err)
	}
	g, err := nfagraph.NewBuilder().Build(r)
	if err != nil {
		t.Fatal(err)
	}
	e, err := CompileEngine(g, EngineShufti)
	if err != nil || e.nibbleNFA == nil || e.rangeNFA != nil || e.tableNFA != nil {
		t.Fatalf("半字节布局未建立: %v", err)
	}
	data := []byte("a7 bq")
	want := e.nibbleNFA.MatchAt(data, 0)
	if got := e.MatchAt(data, 0); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("入口未使用半字节结果: got=%v want=%v", got, want)
	}
	if got := e.Spans(data); fmt.Sprint(got) != "[{0 2}]" {
		t.Fatalf("区间结果=%v", got)
	}
	ctx := &Context{MaxSteps: 64}
	if got := e.MatchAtWithContext(data, 0, ctx); fmt.Sprint(got) != "[2]" || ctx.Steps == 0 {
		t.Fatalf("上下文入口结果=%v steps=%d", got, ctx.Steps)
	}
}

func TestRangeBackendRoundTripPreservesResultsAndBudget(t *testing.T) {
	r, _ := parser.Parse(`[a-z][a-z]`)
	g, _ := nfagraph.NewBuilder().Build(r)
	e, err := CompileEngine(g, EngineTamarama)
	if err != nil || e.rangeNFA == nil {
		t.Fatalf("范围后端未建立: %v", err)
	}
	raw, err := e.Dump()
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadEngine(raw)
	if err != nil || loaded.rangeNFA == nil {
		t.Fatalf("范围后端恢复失败: %v", err)
	}
	data := []byte("abz")
	if fmt.Sprint(loaded.Spans(data)) != fmt.Sprint(e.Spans(data)) {
		t.Fatalf("恢复结果不一致: %v %v", loaded.Spans(data), e.Spans(data))
	}
	if got, steps, stopped := loaded.MatchAtBudget(data, 0, 1, 0); !stopped || got != nil || steps <= 1 {
		t.Fatalf("恢复预算未生效: %v,%d,%v", got, steps, stopped)
	}
}

func TestLBREngineEvaluatesBoundaryNodes(t *testing.T) {
	r, err := parser.Parse(`^abc$`)
	if err != nil {
		t.Fatal(err)
	}
	g, err := nfagraph.NewBuilder().Build(r)
	if err != nil {
		t.Fatal(err)
	}
	e, err := CompileEngine(g, EngineLBR)
	if err != nil || e.lbr == nil {
		t.Fatalf("LBR 编译失败: %v", err)
	}
	if got := e.MatchAt([]byte("abc"), 0); fmt.Sprint(got) != "[3]" {
		t.Fatalf("边界匹配=%v", got)
	}
	if got := e.MatchAt([]byte("xabc"), 1); len(got) != 0 {
		t.Fatalf("错误接受=%v", got)
	}
}

// 首节点为边界断言时无法静态推导首字节，扫描不能因空掩码而跳过所有起点。
func TestLBRBoundaryStartDoesNotDisableScanning(t *testing.T) {
	root, err := parser.Parse(`\bfoo`)
	if err != nil {
		t.Fatal(err)
	}
	g, err := nfagraph.NewBuilder().Build(root)
	if err != nil {
		t.Fatal(err)
	}
	e, err := CompileEngine(g, EngineLBR)
	if err != nil {
		t.Fatal(err)
	}
	data := []byte("x foo foo1")
	want := (&Program{Graph: g}).Spans(data)
	got := e.Spans(data)
	if !sameSpanResults(got, want) {
		t.Fatalf("边界首节点结果不一致: got=%v want=%v", got, want)
	}
}

func TestLBREngineEvaluatesByteClasses(t *testing.T) {
	r, err := parser.Parse(`^[a-z][0-9]$`)
	if err != nil {
		t.Fatal(err)
	}
	g, err := nfagraph.NewBuilder().Build(r)
	if err != nil {
		t.Fatal(err)
	}
	e, err := CompileEngine(g, EngineLBR)
	if err != nil || e.lbr == nil {
		t.Fatalf("LBR 编译失败: %v", err)
	}
	if got := e.MatchAt([]byte("a7"), 0); fmt.Sprint(got) != "[2]" {
		t.Fatalf("字符类匹配=%v", got)
	}
	if got := e.MatchAt([]byte("A7"), 0); len(got) != 0 {
		t.Fatalf("字符类错误接受=%v", got)
	}
}

func TestByteEngineHonorsExecutionBudgets(t *testing.T) {
	r, _ := parser.Parse(`a[0-9]`)
	g, _ := nfagraph.NewBuilder().Build(r)
	e, _ := CompileEngine(g, EngineLimEx)
	ctx := &Context{MaxResults: 1, MaxSteps: 100}
	if got := e.MatchAtWithContext([]byte("a7"), 0, ctx); len(got) != 1 || ctx.Results != 1 || ctx.Steps == 0 {
		t.Fatalf("预算统计=%v,%+v", got, ctx)
	}
	ctx = &Context{MaxSteps: 1}
	if got := e.MatchAtWithContext([]byte("a7"), 0, ctx); got != nil || ctx.Steps <= ctx.MaxSteps {
		t.Fatalf("步骤预算未终止=%v,%+v", got, ctx)
	}
}

func TestByteEngineRejectsOversizedGraph(t *testing.T) {
	pattern := strings.Repeat("a", 4200)
	r, err := parser.Parse(pattern)
	if err != nil {
		t.Fatal(err)
	}
	g, err := nfagraph.NewBuilder().Build(r)
	if err != nil {
		t.Fatal(err)
	}
	e, err := CompileEngine(g, EngineLimEx)
	if err != nil {
		t.Fatal(err)
	}
	if e.byteNFA != nil || e.SupportsGraph(g) {
		t.Fatal("超限图不应进入专用状态机")
	}
	if len(e.MatchAt([]byte(pattern), 0)) == 0 {
		t.Fatal("通用回退未保留匹配")
	}
}

func TestEngineResourceMetrics(t *testing.T) {
	r, _ := parser.Parse(`a[0-9]`)
	g, _ := nfagraph.NewBuilder().Build(r)
	e, _ := CompileEngine(g, EngineLimEx)
	if e.StateCount() == 0 || e.EdgeCount() == 0 || e.MemoryBytes() == 0 {
		t.Fatalf("资源指标无效: states=%d edges=%d bytes=%d", e.StateCount(), e.EdgeCount(), e.MemoryBytes())
	}
}

func TestSpecializedEngineClonePreservesPath(t *testing.T) {
	r, _ := parser.Parse(`a{2,3}`)
	g, _ := nfagraph.NewBuilder().Build(r)
	e, _ := CompileEngine(g, EngineRepeat)
	clone := e.Clone()
	if clone == nil || clone.repeat == nil || fmt.Sprint(clone.MatchAt([]byte("aaa"), 0)) != "[2 3]" {
		t.Fatal("重复引擎克隆不一致")
	}
	r, _ = parser.Parse(`(?:ab|cd)`)
	g, _ = nfagraph.NewBuilder().Build(r)
	e, _ = CompileEngine(g, EngineMPV)
	clone = e.Clone()
	if clone == nil || clone.mpv == nil || fmt.Sprint(clone.MatchAt([]byte("cd"), 0)) != "[2]" {
		t.Fatal("MPV 克隆不一致")
	}
}

func TestEngineSupportsGraphRejectsInvalidInput(t *testing.T) {
	e := &Engine{Kind: EngineLimEx}
	if e.SupportsGraph(nil) || e.Validate() == nil {
		t.Fatal("无效引擎或图未拒绝")
	}
}

func TestEngineSupportsGraphRejectsDifferentStructure(t *testing.T) {
	first, _ := parser.Parse(`ab`)
	firstGraph, _ := nfagraph.NewBuilder().Build(first)
	second, _ := parser.Parse(`ac`)
	secondGraph, _ := nfagraph.NewBuilder().Build(second)
	e, err := CompileEngine(firstGraph, EngineLimEx)
	if err != nil {
		t.Fatal(err)
	}
	if e.SupportsGraph(secondGraph) {
		t.Fatal("不同图结构错误复用当前引擎")
	}
}

func TestProgramUnicodeMetadataMatchesGraph(t *testing.T) {
	r, err := parser.Parse(`\p{Greek}`)
	if err != nil {
		t.Fatal(err)
	}
	g, err := nfagraph.NewBuilder().Build(r)
	if err != nil {
		t.Fatal(err)
	}
	p, err := Compile(g)
	if err != nil || !p.HasUnicode() {
		t.Fatalf("Unicode 元数据缺失: %v", err)
	}
	p.hasUnicode = false
	if err := p.Validate(); err == nil {
		t.Fatal("损坏的 Unicode 元数据未被检测")
	}
}

func TestEngineDumpLoadPreservesSpecializedSelection(t *testing.T) {
	r, _ := parser.Parse(`a{2,3}`)
	g, _ := nfagraph.NewBuilder().Build(r)
	e, _ := CompileEngine(g, EngineRepeat)
	raw, err := e.Dump()
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadEngine(raw)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.repeat == nil || fmt.Sprint(loaded.MatchAt([]byte("aaa"), 0)) != "[2 3]" {
		t.Fatalf("恢复结果=%v", loaded.MatchAt([]byte("aaa"), 0))
	}
}

func TestEngineLoadRejectsCorruptPayload(t *testing.T) {
	if _, err := LoadEngine([]byte(`{"version":99}`)); err == nil {
		t.Fatal("损坏载荷未拒绝")
	}
}

func TestEngineValidateRejectsBackendKindMismatch(t *testing.T) {
	r, _ := parser.Parse(`a{1,2}`)
	g, _ := nfagraph.NewBuilder().Build(r)
	e, _ := CompileEngine(g, EngineRepeat)
	e.Kind = EngineMPV
	if e.Validate() == nil {
		t.Fatal("后端类型错配未拒绝")
	}
}

func TestByteEngineMatchesNULBytes(t *testing.T) {
	r, _ := parser.Parse(`\x00a`)
	g, _ := nfagraph.NewBuilder().Build(r)
	e, _ := CompileEngine(g, EngineLimEx)
	if got := e.MatchAt([]byte{0, 'a'}, 0); fmt.Sprint(got) != "[2]" {
		t.Fatalf("NUL 匹配=%v", got)
	}
}

func TestEngineSpansLimit(t *testing.T) {
	r, _ := parser.Parse(`a`)
	g, _ := nfagraph.NewBuilder().Build(r)
	e, _ := CompileEngine(g, EngineLimEx)
	if got := e.SpansLimit([]byte("aaa"), 2); fmt.Sprint(got) != "[{0 1} {1 2}]" {
		t.Fatalf("区间=%v", got)
	}
	r, _ = parser.Parse(`(?:ab){1,2}`)
	g, _ = nfagraph.NewBuilder().Build(r)
	e, _ = CompileEngine(g, EngineRepeat)
	if got := e.SpansLimit([]byte("abab"), 0); len(got) != 3 {
		t.Fatalf("重复区间=%v", got)
	}
}

func TestEngineRangeQueriesRespectBounds(t *testing.T) {
	r, _ := parser.Parse(`a{1,3}`)
	g, _ := nfagraph.NewBuilder().Build(r)
	e, _ := CompileEngine(g, EngineRepeat)
	if got := e.MatchAtRange([]byte("aaa"), 0, 1, 2, 1); fmt.Sprint(got) != "[1]" {
		t.Fatalf("结束区间=%v", got)
	}
	if got := e.SpansRangeLimit([]byte("aaa"), 1, 3, 2); len(got) != 2 {
		t.Fatalf("区间结果=%v", got)
	}
	if e.SpansRangeLimit([]byte("aaa"), 2, 1, 0) != nil {
		t.Fatal("非法区间未拒绝")
	}
}

func TestEngineSpansRangeIncludesOnlyContainedMatches(t *testing.T) {
	r, _ := parser.Parse(`ab`)
	g, _ := nfagraph.NewBuilder().Build(r)
	e, _ := CompileEngine(g, EngineLimEx)
	if got := e.SpansRange([]byte("zabx"), 1, 3); len(got) != 1 || got[0] != (Span{1, 3}) {
		t.Fatalf("区间=%v", got)
	}
}

func TestEngineSpansBudgetAcrossStarts(t *testing.T) {
	r, _ := parser.Parse(`a`)
	g, _ := nfagraph.NewBuilder().Build(r)
	e, _ := CompileEngine(g, EngineLimEx)
	spans, steps, stopped := e.SpansBudget([]byte("aaa"), 100, 2)
	if len(spans) != 2 || steps != 2 || !stopped {
		t.Fatalf("全局预算=%v,%d,%v", spans, steps, stopped)
	}
}

func TestProgramSpansBudgetStopsWhenRemainingStepsAreZero(t *testing.T) {
	r, err := parser.Parse(`a`)
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
	// 取第一个起点的实际成本作为全局预算，后续起点不应因剩余
	// 预算为零而切换到无限预算模式。
	_, budget, cut := p.MatchAtBudget([]byte("aa"), 0, 0, 0)
	if cut || budget <= 0 {
		t.Fatalf("首个起点预算=%d,%v", budget, cut)
	}
	spans, steps, stopped := p.SpansBudget([]byte("aa"), budget, 0)
	if !stopped || steps != budget || len(spans) != 1 || spans[0] != (Span{0, 1}) {
		t.Fatalf("通用全局步骤预算=%v,%d,%v", spans, steps, stopped)
	}
}

func TestSpecializedSpansBudgetHonorsStepLimit(t *testing.T) {
	r, err := parser.Parse(`ab`)
	if err != nil {
		t.Fatal(err)
	}
	g, err := nfagraph.NewBuilder().Build(r)
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []EngineKind{EngineCastle, EngineGough} {
		e, err := CompileEngine(g, kind)
		if err != nil {
			t.Fatal(err)
		}
		spans, steps, stopped := e.SpansBudget([]byte("ab"), 1, 0)
		if !stopped || spans != nil || steps <= 1 {
			t.Fatalf("%s 步骤限额=%v,%d,%v", kind, spans, steps, stopped)
		}
	}
}

func TestEngineFirstAndLongestQueries(t *testing.T) {
	r, _ := parser.Parse(`a{1,3}`)
	g, _ := nfagraph.NewBuilder().Build(r)
	e, _ := CompileEngine(g, EngineRepeat)
	if end, ok := e.MatchFirst([]byte("aaa"), 0); !ok || end != 1 {
		t.Fatalf("最短结果=%d,%v", end, ok)
	}
	if end, ok := e.MatchLongest([]byte("aaa"), 0); !ok || end != 3 {
		t.Fatalf("最长结果=%d,%v", end, ok)
	}
}

func TestEngineCapabilitiesExposeExecutionShape(t *testing.T) {
	r, _ := parser.Parse(`a{1,2}`)
	g, _ := nfagraph.NewBuilder().Build(r)
	e, _ := CompileEngine(g, EngineRepeat)
	caps := e.Capabilities()
	if !caps["specialized"] || !caps["bounded"] {
		t.Fatalf("能力=%v", caps)
	}
}

func TestSaturatingMemoryArithmetic(t *testing.T) {
	maxValue := ^uint64(0)
	if saturatingAdd(maxValue-1, 2) != maxValue || saturatingMul(maxValue, 2) != maxValue {
		t.Fatalf("内存估算溢出未饱和: add=%d mul=%d", saturatingAdd(maxValue-1, 2), saturatingMul(maxValue, 2))
	}
}

func TestCompileEngineWithBudgetsRejectsNilGraph(t *testing.T) {
	if _, err := CompileEngineWithBudgets(nil, EngineLimEx, 0, 0); err == nil {
		t.Fatal("空图未返回编译错误")
	}
}

func TestEngineFamilyMetricsUsePrimaryLayout(t *testing.T) {
	r, err := parser.Parse(`(?:ab|a[0-9])`)
	if err != nil {
		t.Fatal(err)
	}
	g, err := nfagraph.NewBuilder().Build(r)
	if err != nil {
		t.Fatal(err)
	}
	mcsheng, err := CompileEngine(g, EngineMcSheng)
	if err != nil || mcsheng.sparseNFA == nil {
		t.Fatalf("McSheng 编译失败: %v", err)
	}
	if got, want := mcsheng.MemoryBytes(), sparseNFAMemoryBytes(mcsheng.sparseNFA); got != want {
		t.Fatalf("McSheng 内存统计未使用稀疏布局: got=%d want=%d", got, want)
	}
	shufti, err := CompileEngine(g, EngineShufti)
	if err != nil || shufti.nibbleNFA == nil {
		t.Fatalf("Shufti 编译失败: %v", err)
	}
	if got, want := shufti.MemoryBytes(), nibbleNFAMemoryBytes(shufti.nibbleNFA); got != want {
		t.Fatalf("Shufti 内存统计未使用半字节布局: got=%d want=%d", got, want)
	}
}

func TestRangeLayoutUsesByteMasksForClasses(t *testing.T) {
	r, err := parser.Parse(`[a-c][a-c]`)
	if err != nil {
		t.Fatal(err)
	}
	g, err := nfagraph.NewBuilder().Build(r)
	if err != nil {
		t.Fatal(err)
	}
	e, err := CompileEngine(g, EngineTamarama)
	if err != nil || e.rangeNFA == nil {
		t.Fatalf("范围布局编译失败: %v", err)
	}
	if err := e.rangeNFA.validate(); err != nil {
		t.Fatalf("范围布局校验失败: %v", err)
	}
	if got := e.MatchAt([]byte("ab"), 0); fmt.Sprint(got) != "[2]" {
		t.Fatalf("字符类掩码结果=%v", got)
	}
	if got := e.MatchAt([]byte("z"), 0); len(got) != 0 {
		t.Fatalf("字符类掩码错误接受: %v", got)
	}
}

func TestRangeLayoutRejectsCorruptedByteMask(t *testing.T) {
	r, err := parser.Parse(`[a-c][d-f]`)
	if err != nil {
		t.Fatal(err)
	}
	g, err := nfagraph.NewBuilder().Build(r)
	if err != nil {
		t.Fatal(err)
	}
	e, err := CompileEngine(g, EngineTamarama)
	if err != nil || e.rangeNFA == nil {
		t.Fatalf("范围布局编译失败: %v", err)
	}
	e.rangeNFA.masks[0][0] ^= 1
	if err := e.rangeNFA.validate(); err == nil {
		t.Fatal("损坏的字符掩码未拒绝")
	}
}

func TestTamaramaRangeBucketsRespectGaps(t *testing.T) {
	r, err := parser.Parse(`[a-cx-z]`)
	if err != nil {
		t.Fatal(err)
	}
	g, err := nfagraph.NewBuilder().Build(r)
	if err != nil {
		t.Fatal(err)
	}
	e, err := CompileEngine(g, EngineTamarama)
	if err != nil || e.rangeNFA == nil {
		t.Fatalf("范围布局编译失败: %v", err)
	}
	for _, value := range []byte{'a', 'c', 'x', 'z'} {
		if got := e.MatchAt([]byte{value}, 0); len(got) != 1 || got[0] != 1 {
			t.Fatalf("范围边界 %q 结果=%v", value, got)
		}
	}
	if got := e.MatchAt([]byte("d"), 0); len(got) != 0 {
		t.Fatalf("范围间隙错误接受=%v", got)
	}
	if err := e.Validate(); err != nil {
		t.Fatalf("范围布局校验失败: %v", err)
	}
}

func TestNibbleLayoutExecutesAndValidates(t *testing.T) {
	r, err := parser.Parse(`[a-c][0-9]`)
	if err != nil {
		t.Fatal(err)
	}
	g, err := nfagraph.NewBuilder().Build(r)
	if err != nil {
		t.Fatal(err)
	}
	e, err := CompileEngine(g, EngineShufti)
	if err != nil || e.nibbleNFA == nil {
		t.Fatalf("半字节布局未建立: %v", err)
	}
	if got := e.nibbleNFA.MatchAt([]byte("b7"), 0); fmt.Sprint(got) != "[2]" {
		t.Fatalf("半字节布局结果=%v", got)
	}
	e.nibbleNFA.masks[0][0] ^= 1
	if err := e.nibbleNFA.validate(); err == nil {
		t.Fatal("损坏的半字节掩码未拒绝")
	}
}

func TestTruffleUsesNibbleClassMasks(t *testing.T) {
	r, err := parser.Parse(`[a-f][0-9]`)
	if err != nil {
		t.Fatal(err)
	}
	g, err := nfagraph.NewBuilder().Build(r)
	if err != nil {
		t.Fatal(err)
	}
	e, err := CompileEngine(g, EngineTruffle)
	if err != nil || e.nibbleNFA == nil {
		t.Fatalf("Truffle 布局编译失败: %v", err)
	}
	if !e.Independent() || e.ExecutionModel() != "truffle" {
		t.Fatalf("执行模型=%s independent=%v", e.ExecutionModel(), e.Independent())
	}
	if e.nibbleNFA.mode != 1 {
		t.Fatalf("Truffle 掩码模式=%d", e.nibbleNFA.mode)
	}
	if got := e.MatchAt([]byte("c7"), 0); len(got) != 1 || got[0] != 2 {
		t.Fatalf("半字节掩码结果=%v", got)
	}
	if got := e.MatchAt([]byte("g7"), 0); len(got) != 0 {
		t.Fatalf("半字节掩码错误接受=%v", got)
	}
	if err := e.nibbleNFA.validate(); err != nil {
		t.Fatalf("半字节布局校验失败: %v", err)
	}
	clone := e.Clone()
	if clone == nil || clone.nibbleNFA == nil || clone.nibbleNFA.mode != 1 {
		t.Fatal("Truffle 克隆未保留掩码模式")
	}
}

func TestNibbleLayoutRoundTripPreservesMatches(t *testing.T) {
	r, err := parser.Parse(`(?:[a-f][0-9]|[x-z][a-c])`)
	if err != nil {
		t.Fatal(err)
	}
	g, err := nfagraph.NewBuilder().Build(r)
	if err != nil {
		t.Fatal(err)
	}
	e, err := CompileEngine(g, EngineTruffle)
	if err != nil || e.nibbleNFA == nil {
		t.Fatalf("半字节后端未建立: %v", err)
	}
	raw, err := e.Dump()
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadEngine(raw)
	if err != nil || loaded.nibbleNFA == nil {
		t.Fatalf("半字节后端恢复失败: %v", err)
	}
	data := []byte("a7 yb")
	if fmt.Sprint(loaded.Spans(data)) != fmt.Sprint(e.Spans(data)) {
		t.Fatalf("半字节恢复结果不一致: %#v %#v", loaded.Spans(data), e.Spans(data))
	}
}

func TestEngineSemanticCapabilities(t *testing.T) {
	r, _ := parser.Parse(`a*`)
	g, _ := nfagraph.NewBuilder().Build(r)
	e, _ := CompileEngine(g, EngineRepeat)
	if !e.AcceptsEmpty() {
		t.Fatal("引擎空匹配判断错误")
	}
	r, _ = parser.Parse(`\p{L}`)
	g, _ = nfagraph.NewBuilder().Build(r)
	e, _ = CompileEngine(g, EngineLimEx)
	if !e.HasUnicode() {
		t.Fatal("引擎 Unicode 判断错误")
	}
}

func TestEngineMatchAtBudgetContract(t *testing.T) {
	r, _ := parser.Parse(`a[0-9]`)
	g, _ := nfagraph.NewBuilder().Build(r)
	e, _ := CompileEngine(g, EngineLimEx)
	ends, steps, stopped := e.MatchAtBudget([]byte("a7"), 0, 100, 1)
	if len(ends) != 1 || steps == 0 || !stopped {
		t.Fatalf("预算契约=%v,%d,%v", ends, steps, stopped)
	}
}

func FuzzEngineFamiliesNeverPanic(f *testing.F) {
	f.Add("ab", "ab")
	f.Add("a7", "a7")
	f.Add("", "")
	f.Fuzz(func(_ *testing.T, pattern, input string) {
		r, err := parser.Parse(pattern)
		if err != nil {
			return
		}
		g, err := nfagraph.NewBuilder().Build(r)
		if err != nil {
			return
		}
		for k := EngineCastle; k <= EngineLBR; k++ {
			e, err := CompileEngine(g, k)
			if err != nil {
				continue
			}
			_ = e.MatchAt([]byte(input), 0)
		}
	})
}

func TestSpecializedEngineRejectsUnsupportedGraphNodes(t *testing.T) {
	r, err := parser.Parse(`\p{L}`)
	if err != nil {
		t.Fatal(err)
	}
	g, err := nfagraph.NewBuilder().Build(r)
	if err != nil {
		t.Fatal(err)
	}
	e, err := CompileEngine(g, EngineShufti)
	if err != nil {
		t.Fatal(err)
	}
	if e.SupportsGraph(g) {
		t.Fatal("专用引擎接受了包含断言的图")
	}
}

func TestGoughReverseMatching(t *testing.T) {
	cases := []struct {
		name string
		expr string
		data []byte
		want []int
	}{
		{name: "literal", expr: "abc", data: []byte("abcx"), want: []int{3}},
		{name: "branch", expr: "(?:ab|cd)", data: []byte("cd"), want: []int{2}},
		{name: "class", expr: "a[0-9]", data: []byte("a7"), want: []int{2}},
		{name: "negative", expr: "abc", data: []byte("abd"), want: nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, err := parser.Parse(tc.expr)
			if err != nil {
				t.Fatal(err)
			}
			g, err := nfagraph.NewBuilder().Build(r)
			if err != nil {
				t.Fatal(err)
			}
			e, err := CompileEngine(g, EngineGough)
			if err != nil {
				t.Fatal(err)
			}
			if e.gough == nil {
				t.Fatal("未建立反向状态程序")
			}
			got := e.MatchAt(tc.data, 0)
			if len(got) != len(tc.want) {
				t.Fatalf("结束位置=%v, want=%v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("结束位置=%v, want=%v", got, tc.want)
				}
			}
			clone := e.Clone()
			if clone.gough == nil || fmt.Sprint(clone.MatchAt(tc.data, 0)) != fmt.Sprint(got) {
				t.Fatal("克隆引擎结果不一致")
			}
		})
	}
}

func TestGoughLimitAndContext(t *testing.T) {
	r, _ := parser.Parse("(?:a|aa)")
	g, _ := nfagraph.NewBuilder().Build(r)
	e, err := CompileEngine(g, EngineGough)
	if err != nil {
		t.Fatal(err)
	}
	if got := e.MatchAtLimit([]byte("aa"), 0, 1); len(got) != 1 {
		t.Fatalf("结果限制未生效=%v", got)
	}
	ctx := &Context{MaxResults: 1, MaxSteps: 2}
	if got := e.MatchAtWithContext([]byte("aa"), 0, ctx); len(got) != 1 || ctx.Results != 1 || ctx.Steps != 2 {
		t.Fatalf("上下文结果=%v, steps=%d, results=%d", got, ctx.Steps, ctx.Results)
	}
	ctx = &Context{MaxSteps: 1}
	if got := e.MatchAtWithContext([]byte("aa"), 0, ctx); got != nil {
		t.Fatalf("超出步骤预算仍返回结果=%v", got)
	}
}

func TestGoughDeterministicPrefixRejectsEarly(t *testing.T) {
	r, err := parser.Parse(`abcdef`)
	if err != nil {
		t.Fatal(err)
	}
	g, err := nfagraph.NewBuilder().Build(r)
	if err != nil {
		t.Fatal(err)
	}
	e, err := CompileEngine(g, EngineGough)
	if err != nil || e.gough == nil {
		t.Fatalf("Gough 编译失败: %v", err)
	}
	if got, steps, stopped := e.gough.MatchAtBudget([]byte("xbcdef"), 0, 0, 0); len(got) != 0 || steps != 0 || stopped {
		t.Fatalf("前缀不匹配未快速结束: got=%v steps=%d stopped=%v", got, steps, stopped)
	}
}

func TestCompiledClosureTablesRejectUnknownVertices(t *testing.T) {
	r, err := parser.Parse("(?:ab|cd)")
	if err != nil {
		t.Fatal(err)
	}
	g, err := nfagraph.NewBuilder().Build(r)
	if err != nil {
		t.Fatal(err)
	}
	castle, err := CompileEngine(g, EngineCastle)
	if err != nil || castle.castle == nil {
		t.Fatalf("Castle 编译失败: %v", err)
	}
	castle.castle.closureTable[castle.castle.graph.Start] = append(castle.castle.closureTable[castle.castle.graph.Start], graph.Vertex(1<<30))
	if err := castle.Validate(); err == nil {
		t.Fatal("Castle 未拒绝非法闭包目标")
	}
	gough, err := CompileEngine(g, EngineGough)
	if err != nil || gough.gough == nil {
		t.Fatalf("Gough 编译失败: %v", err)
	}
	gough.gough.reverseTable[gough.gough.graph.Start] = append(gough.gough.reverseTable[gough.gough.graph.Start], graph.Vertex(1<<30))
	if err := gough.Validate(); err == nil {
		t.Fatal("Gough 未拒绝非法反向闭包目标")
	}
}

func TestEngineValidationRejectsMultipleExecutionLayouts(t *testing.T) {
	r, err := parser.Parse(`ab`)
	if err != nil {
		t.Fatal(err)
	}
	g, err := nfagraph.NewBuilder().Build(r)
	if err != nil {
		t.Fatal(err)
	}
	e, err := CompileEngine(g, EngineLimEx)
	if err != nil || e.bitNFA == nil {
		t.Fatalf("LimEx 编译失败: %v", err)
	}
	e.tableNFA = compileTableNFA(g)
	if err := e.Validate(); err == nil {
		t.Fatal("同时挂载多个执行布局时应拒绝")
	}
}

func TestEngineValidationRejectsStateKindMismatch(t *testing.T) {
	r, err := parser.Parse(`ab`)
	if err != nil {
		t.Fatal(err)
	}
	g, err := nfagraph.NewBuilder().Build(r)
	if err != nil {
		t.Fatal(err)
	}
	e, err := CompileEngine(g, EngineMcSheng)
	if err != nil || e.sparseNFA == nil {
		t.Fatalf("McSheng 编译失败: %v", err)
	}
	e.sparseNFA.states[0].kind = nfagraph.KindAccept
	if err := e.Validate(); err == nil {
		t.Fatal("状态类型损坏时应拒绝")
	}
}

func TestSpecializedPrefixFiltersCandidates(t *testing.T) {
	r, err := parser.Parse(`abcdef`)
	if err != nil {
		t.Fatal(err)
	}
	g, err := nfagraph.NewBuilder().Build(r)
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []EngineKind{EngineSheng, EngineTamarama, EngineShufti, EngineTruffle} {
		e, err := CompileEngine(g, kind)
		if err != nil {
			t.Fatal(err)
		}
		if got, steps, stopped := e.MatchAtBudget([]byte("xbcdef"), 0, 0, 0); len(got) != 0 || steps != 0 || stopped {
			t.Fatalf("%s 前缀过滤失败: got=%v steps=%d stopped=%v", kind, got, steps, stopped)
		}
	}
}

func TestSparseClassMaskRejectsCorruption(t *testing.T) {
	r, err := parser.Parse(`[a-c]`)
	if err != nil {
		t.Fatal(err)
	}
	g, err := nfagraph.NewBuilder().Build(r)
	if err != nil {
		t.Fatal(err)
	}
	e, err := CompileEngine(g, EngineMcSheng)
	if err != nil || e.sparseNFA == nil {
		t.Fatalf("McSheng 编译失败: %v", err)
	}
	e.sparseNFA.classMask[0][0] ^= 1
	if err := e.Validate(); err == nil {
		t.Fatal("字符类掩码损坏时应拒绝")
	}
}

func TestPrefixOptimizationNeverRejectsEmptyAcceptingGraph(t *testing.T) {
	r, err := parser.Parse(`a*`)
	if err != nil {
		t.Fatal(err)
	}
	g, err := nfagraph.NewBuilder().Build(r)
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []EngineKind{EngineLimEx, EngineSheng, EngineMcSheng, EngineTamarama, EngineShufti, EngineTruffle, EngineVermicelli} {
		e, err := CompileEngine(g, kind)
		if err != nil {
			t.Fatal(err)
		}
		if got := e.MatchAt([]byte(""), 0); len(got) == 0 || got[0] != 0 {
			t.Fatalf("%s 空接受被前缀优化误杀: %v", kind, got)
		}
	}
}

func TestSpecializedClonePreservesOptimizedExecution(t *testing.T) {
	r, err := parser.Parse(`[a-c]def`)
	if err != nil {
		t.Fatal(err)
	}
	g, err := nfagraph.NewBuilder().Build(r)
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []EngineKind{EngineLimEx, EngineSheng, EngineMcSheng, EngineTamarama, EngineShufti, EngineTruffle} {
		e, err := CompileEngine(g, kind)
		if err != nil {
			t.Fatal(err)
		}
		clone := e.Clone()
		if clone == nil || clone.ExecutionModel() != e.ExecutionModel() || fmt.Sprint(clone.MatchAt([]byte("bdef"), 0)) != fmt.Sprint(e.MatchAt([]byte("bdef"), 0)) {
			t.Fatalf("%s 克隆后专用路径或结果不一致", kind)
		}
	}
}

func TestLBRClassMaskRejectsCorruption(t *testing.T) {
	r, err := parser.Parse(`^[a-z]`)
	if err != nil {
		t.Fatal(err)
	}
	g, err := nfagraph.NewBuilder().Build(r)
	if err != nil {
		t.Fatal(err)
	}
	e, err := CompileEngine(g, EngineLBR)
	if err != nil || e.lbr == nil {
		t.Fatalf("LBR 编译失败: %v", err)
	}
	for id := range e.lbr.classMask {
		mask := e.lbr.classMask[id]
		mask[0] ^= 1
		e.lbr.classMask[id] = mask
		break
	}
	if err := e.Validate(); err == nil {
		t.Fatal("LBR 字符类掩码损坏时应拒绝")
	}
}
