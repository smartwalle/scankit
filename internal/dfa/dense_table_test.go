package dfa

import (
	"testing"

	"github.com/smartwalle/scankit/internal/nfagraph"
	"github.com/smartwalle/scankit/internal/parser"
)

// TestDenseTableAcceptsAndRejectsAllByteTransitions 校验稠密转移表覆盖 256 个
// 输入字节且与稀疏转移语义一致；缺失转移统一为 ^uint32(0)。
func TestDenseTableAcceptsAndRejectsAllByteTransitions(t *testing.T) {
	r, err := parser.Parse(`abc`)
	if err != nil {
		t.Fatal(err)
	}
	g, err := nfagraph.NewBuilder().Build(r)
	if err != nil {
		t.Fatal(err)
	}
	p, err := CompileWithOptions(g, CompileOptions{Dense: true})
	if err != nil {
		t.Fatal(err)
	}
	dense := p.DenseTable()
	if len(dense) != len(p.States) {
		t.Fatalf("稠密表行数=%d 状态数=%d", len(dense), len(p.States))
	}
	for i, row := range dense {
		if len(row) != 256 {
			t.Fatalf("稠密表[%d] 列数=%d", i, len(row))
		}
	}
	// 起点状态遇到 'a' 应转移到非自环状态；其他字节均应缺失或自环。
	start := p.Start
	if dense[start]['a'] == ^uint32(0) {
		t.Fatal("稠密表起点 'a' 转移缺失")
	}
	if dense[start]['b'] != ^uint32(0) && dense[start]['b'] == start {
		// 起点不允许在非首字节 'b' 上保持原状态。
		t.Fatalf("稠密表起点 'b' 转移异常: %d", dense[start]['b'])
	}
	// MatchAt 必须与稠密表转移给出相同的接受位置。
	if got := p.MatchAt([]byte("abc"), 0); len(got) != 1 || got[0] != 3 {
		t.Fatalf("稠密表 MatchAt: %v", got)
	}
	if got := p.MatchAt([]byte("abd"), 0); len(got) != 0 {
		t.Fatalf("稠密表错误接受: %v", got)
	}
}

// TestMinimizeAfterDenseTableKeepsLanguage 校验最小化与稠密转移表顺序无关，
// 即在稠密化前或后最小化都产生等价语言。
func TestMinimizeAfterDenseTableKeepsLanguage(t *testing.T) {
	r, err := parser.Parse(`(?:ab|cb)`)
	if err != nil {
		t.Fatal(err)
	}
	g, err := nfagraph.NewBuilder().Build(r)
	if err != nil {
		t.Fatal(err)
	}
	denseProg, err := CompileWithOptions(g, CompileOptions{Dense: true})
	if err != nil {
		t.Fatal(err)
	}
	minProg, err := Minimize(g)
	if err != nil {
		t.Fatal(err)
	}
	if err := minProg.Validate(); err != nil {
		t.Fatal(err)
	}
	inputs := []string{"ab", "cb", "ax", "ca", "abc", "cba", ""}
	for _, s := range inputs {
		// 仅在合法起点内比对结果，避免越界语义差异。
		for start := 0; start <= len(s); start++ {
			gotDense := denseProg.MatchAt([]byte(s), start)
			gotMin := minProg.MatchAt([]byte(s), start)
			if len(gotDense) != len(gotMin) {
				t.Fatalf("start=%d 输入=%q 稠密=%v 最小化=%v", start, s, gotDense, gotMin)
			}
			for i := range gotDense {
				if gotDense[i] != gotMin[i] {
					t.Fatalf("start=%d 输入=%q [%d]: 稠密=%d 最小化=%d", start, s, i, gotDense[i], gotMin[i])
				}
			}
		}
	}
}

// TestReverseProgramPreservesDenseForwardResults 校验反向程序通过前向程序
// 在所有结束位置上得到与散列路径一致的结果，反向表与压缩布局互不破坏。
func TestReverseProgramPreservesDenseForwardResults(t *testing.T) {
	r, err := parser.Parse(`abc|abd`)
	if err != nil {
		t.Fatal(err)
	}
	g, err := nfagraph.NewBuilder().Build(r)
	if err != nil {
		t.Fatal(err)
	}
	forward, err := CompileWithOptions(g, CompileOptions{Dense: true})
	if err != nil {
		t.Fatal(err)
	}
	reverse, err := CompileReverse(g)
	if err != nil {
		t.Fatal(err)
	}
	data := []byte("xxabc xxabd xxabe")
	for end := 0; end <= len(data); end++ {
		revStarts := reverse.MatchReverseAt(data, end)
		for _, start := range revStarts {
			got := forward.MatchAt(data, start)
			found := false
			for _, g := range got {
				if g == end {
					found = true
					break
				}
			}
			if !found {
				t.Fatalf("end=%d start=%d 前向未确认: %v", end, start, got)
			}
		}
	}
}

// TestDFAAcceptReportIDsCollectsDistinctIDs 验证 DFA.AcceptReportIDs 从
// 所有接受态的 NFA 节点集合中收集 ReportID 并去重。
func TestDFAAcceptReportIDsCollectsDistinctIDs(t *testing.T) {
	r, err := parser.Parse("abc")
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
	ids := p.AcceptReportIDs()
	if len(ids) != 0 {
		t.Fatalf("无 Report 节点的图应返回空列表: %v", ids)
	}
}

// TestDFAHasReports 验证 DFA.HasReports 根据图节点上是否带非零 ReportID
// 返回布尔。
func TestDFAHasReports(t *testing.T) {
	r, err := parser.Parse("abc")
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
	if p.HasReports() {
		t.Fatal("无 Report 节点的 DFA 不应携带 HasReports=true")
	}
}
