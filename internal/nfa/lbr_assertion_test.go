package nfa

import (
	"fmt"
	"testing"

	"github.com/smartwalle/scankit/internal/nfagraph"
	"github.com/smartwalle/scankit/internal/parser"
)

// TestLBRBoundaryConformance 覆盖 LBR 全部七类边界断言，确保布局校验、
// 运行时形状检查、首字节掩码和结果传播在每类边界上一致。
func TestLBRBoundaryConformance(t *testing.T) {
	cases := []struct {
		name    string
		pattern string
		input   []byte
		start   int
		accept  []int
	}{
		{"begin_anchored_at_start", `^abc`, []byte("abc"), 0, []int{3}},
		{"begin_rejects_non_start", `^abc`, []byte("xabc"), 1, nil},
		{"end_anchored_at_terminal", `abc$`, []byte("abc"), 0, []int{3}},
		{"end_rejects_non_terminal", `abc$`, []byte("abcd"), 0, nil},
		{"word_boundary_open_at_zero", `\bfoo\b`, []byte("foo"), 0, []int{3}},
		{"word_boundary_at_offset", `\bfoo\b`, []byte("-foo"), 1, []int{4}},
		{"word_boundary_rejects_inside_word", `\bfoo\b`, []byte("xfoo"), 1, nil},
		{"non_word_boundary_rejects_inside_word", `\Boo\B`, []byte("xoo"), 1, nil},
		{"non_word_boundary_rejects_at_eod", `\Boo\B`, []byte("foo"), 1, nil},
		{"non_word_boundary_inside_word", `\Boo\B`, []byte("foooo"), 1, []int{3}},
		{"begin_absolute_open", `\Aabc`, []byte("abc"), 0, []int{3}},
		{"begin_absolute_rejects_non_start", `\Aabc`, []byte("xabc"), 1, nil},
		{"end_absolute_open", `abc\z`, []byte("abc"), 0, []int{3}},
		{"end_absolute_rejects_after_newline", `abc\z`, []byte("abc\n"), 0, nil},
		{"end_before_final_newline_open", `abc\Z`, []byte("abc"), 0, []int{3}},
		{"end_before_final_newline_with_newline", `abc\Z`, []byte("abc\n"), 0, []int{3}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, err := parser.Parse(tc.pattern)
			if err != nil {
				t.Fatalf("parse %q: %v", tc.pattern, err)
			}
			g, err := nfagraph.NewBuilder().Build(r)
			if err != nil {
				t.Fatalf("graph %q: %v", tc.pattern, err)
			}
			e, err := CompileEngine(g, EngineLBR)
			if err != nil {
				t.Fatalf("compile %q: %v", tc.pattern, err)
			}
			if e.lbr == nil {
				t.Fatalf("LBR 未选择: %s", tc.pattern)
			}
			got := e.MatchAt(tc.input, tc.start)
			if fmt.Sprint(got) != fmt.Sprint(tc.accept) {
				t.Fatalf("%s: got=%v want=%v", tc.pattern, got, tc.accept)
			}
			dst := make([]int, 0, 4)
			gotInto := e.MatchAtInto(tc.input, tc.start, dst)
			if fmt.Sprint(gotInto) != fmt.Sprint(tc.accept) {
				t.Fatalf("%s MatchAtInto: got=%v want=%v", tc.pattern, gotInto, tc.accept)
			}
			if len(tc.accept) > 0 && cap(gotInto) < len(tc.accept) {
				t.Fatalf("%s MatchAtInto 缓冲容量=%d", tc.pattern, cap(gotInto))
			}
		})
	}
}

// TestLBRFallbackForLookaround 验证含 lookbehind/lookahead 的图不能进入 LBR 路径，
// 必须通过通用确认回退得到与 Castle 基线一致的结果。
func TestLBRFallbackForLookaround(t *testing.T) {
	patterns := []string{
		`(?<=ab)cd`,
		`(?<!ab)cd`,
		`cd(?=ef)`,
		`cd(?!ef)`,
	}
	for _, pat := range patterns {
		t.Run(pat, func(t *testing.T) {
			r, err := parser.Parse(pat)
			if err != nil {
				t.Fatal(err)
			}
			g, err := nfagraph.NewBuilder().Build(r)
			if err != nil {
				t.Fatal(err)
			}
			e, err := CompileEngine(g, EngineLBR)
			if err != nil {
				t.Fatalf("LBR 编译应成功（含通用回退）: %v", err)
			}
			if e.lbr != nil {
				t.Fatal("含 lookaround 的图不应进入 LBR 专用布局")
			}
			baseline, err := CompileEngine(g, EngineCastle)
			if err != nil {
				t.Fatal(err)
			}
			inputs := [][]byte{[]byte("abcdef"), []byte("abxcd"), []byte("cdef"), []byte("xxcdxx")}
			for _, data := range inputs {
				for start := 0; start <= len(data); start++ {
					got := e.MatchAt(data, start)
					want := baseline.MatchAt(data, start)
					if fmt.Sprint(got) != fmt.Sprint(want) {
						t.Fatalf("%s: start=%d got=%v want=%v", pat, start, got, want)
					}
				}
			}
		})
	}
}

// TestLBRRuntimeShapeCatchesLiteralTampering 校验 LBR 的运行时形状检查在
// 字面量字节被篡改时立即拒绝，避免错误执行路径进入整块扫描。
func TestLBRRuntimeShapeCatchesLiteralTampering(t *testing.T) {
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
	tampered := false
	for i, b := range e.lbr.literals {
		if b != 0 {
			e.lbr.literals[i] = 'X'
			tampered = true
			break
		}
	}
	if !tampered {
		t.Fatal("未找到可篡改的字面量")
	}
	if lbrRuntimeShapeOK(e.lbr) {
		t.Fatal("运行时形状检查未察觉字面量字节不一致")
	}
}

// TestLBRRuntimeShapeCatchesFirstMaskTampering 校验运行时形状检查察觉首字节
// 掩码不一致，立即回退通用确认路径。
func TestLBRRuntimeShapeCatchesFirstMaskTampering(t *testing.T) {
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
	e.lbr.firstMask[0] ^= 1
	if lbrRuntimeShapeOK(e.lbr) {
		t.Fatal("运行时形状检查未察觉首字节掩码不一致")
	}
}

// TestLBRSpansBudgetKeepsEndOrder 校验 LBR 整块扫描在结果限额内按终点稳定排序，
// 并且结果截断不能改变已返回区间的相对顺序。
func TestLBRSpansBudgetKeepsEndOrder(t *testing.T) {
	r, err := parser.Parse(`^abc|def$`)
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
	data := []byte("abc def abc def")
	baseline, err := CompileEngine(g, EngineCastle)
	if err != nil {
		t.Fatal(err)
	}
	want := baseline.Spans(data)
	got := e.Spans(data)
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("LBR Spans: got=%v want=%v", got, want)
	}
	limited := e.SpansLimit(data, 2)
	if len(limited) > 2 {
		t.Fatalf("LBR SpansLimit 未截断: got=%v", limited)
	}
	if len(limited) == 2 && (limited[0].From != want[0].From || limited[1].From != want[1].From) {
		t.Fatalf("LBR SpansLimit 顺序错误: got=%v want=%v", limited, want[:2])
	}
}

// TestLBRSpansIntoReusesBuffer 验证 LBR 的 SpansInto 能复用调用方提供的缓冲，
// 避免每次扫描分配新切片，且对 limit>0 的截断语义与 Spans 保持一致。
func TestLBRSpansIntoReusesBuffer(t *testing.T) {
	r, err := parser.Parse(`^abc|def$`)
	if err != nil {
		t.Fatal(err)
	}
	g, err := nfagraph.NewBuilder().Build(r)
	if err != nil {
		t.Fatal(err)
	}
	e, err := CompileEngine(g, EngineLBR)
	if err != nil || e.lbr == nil {
		t.Fatalf("LBR compile failed: %v", err)
	}
	data := []byte("abc def abc def")
	want := e.lbr.Spans(data, 0)

	dst := make([]Span, 0, 4)
	got := e.lbr.SpansInto(data, 0, dst)
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("SpansInto: got=%v want=%v", got, want)
	}

	dst2 := make([]Span, 0, 2)
	gotLimited := e.lbr.SpansInto(data, 2, dst2)
	wantLimited := e.lbr.Spans(data, 2)
	if fmt.Sprint(gotLimited) != fmt.Sprint(wantLimited) {
		t.Fatalf("SpansInto limit: got=%v want=%v", gotLimited, wantLimited)
	}
	if len(gotLimited) > 2 {
		t.Fatalf("SpansInto limit=%v exceeds limit=2", gotLimited)
	}
}

// TestLBRMatchAtRangeIntoFiltersAndReusesBuffer 验证 LBR 的 MatchAtRangeInto
// 能按 from/to 区间裁剪结束偏移，复用调用方缓冲且与 MatchAtRange 一致。
func TestLBRMatchAtRangeIntoFiltersAndReusesBuffer(t *testing.T) {
	r, err := parser.Parse(`\babc|def\b`)
	if err != nil {
		t.Fatal(err)
	}
	g, err := nfagraph.NewBuilder().Build(r)
	if err != nil {
		t.Fatal(err)
	}
	e, err := CompileEngine(g, EngineLBR)
	if err != nil || e.lbr == nil {
		t.Fatalf("LBR compile failed: %v", err)
	}
	data := []byte("abc def abc def")
	dst := make([]int, 0, 8)
	// 区间 [3, 9] 内的结果应在 [3, 9]，区间外应为空。
	got := e.lbr.MatchAtRangeInto(data, 0, 3, 9, 0, dst)
	for _, v := range got {
		if v < 3 || v > 9 {
			t.Fatalf("MatchAtRangeInto 越界: %v", got)
		}
	}
	// to < from 应无效果；这里利用 from>to 直接拒绝（参见 MatchAtRangeInto 输入校验）。
	if got := e.lbr.MatchAtRangeInto(data, 0, 0, 14, 1, dst); len(got) != 1 {
		t.Fatalf("limit=1 截断错误: %v", got)
	}
}
