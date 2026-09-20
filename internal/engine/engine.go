// Package engine 为编译图选择通用执行后端。
package engine

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/smartwalle/scankit/internal/dfa"
	"github.com/smartwalle/scankit/internal/nfa"
	"github.com/smartwalle/scankit/internal/nfagraph"
	"sort"
)

type Kind uint8

const (
	KindNFA Kind = iota + 1
	KindDFA
)

func (k Kind) String() string {
	switch k {
	case KindNFA:
		return "nfa"
	case KindDFA:
		return "dfa"
	default:
		return "unknown"
	}
}

type Program struct {
	Kind Kind
	NFA  *nfa.Program
	DFA  *dfa.Program
}

// Stats 描述后端程序的静态规模信息。
type Stats struct {
	Kind           Kind
	GraphVertices  int
	GraphEdges     int
	DFAStates      int
	DFATransitions int
	MemoryBytes    uint64
}

// Stats 返回后端规模快照，用于选择和预算审计。
func (p *Program) Stats() Stats {
	if p == nil {
		return Stats{}
	}
	s := Stats{Kind: p.Kind}
	if p.NFA != nil {
		s.GraphVertices, s.GraphEdges = p.NFA.NodeCount(), p.NFA.EdgeCount()
	}
	if p.DFA != nil {
		s.GraphVertices, s.GraphEdges = p.DFA.NodeCount(), p.DFA.EdgeCount()
		s.DFAStates, s.DFATransitions, s.MemoryBytes = p.DFA.StateCount(), p.DFA.TransitionCount(), p.DFA.MemoryBytes()
	}
	return s
}

// KindValue 返回当前执行后端类型；空程序返回零值。
func (p *Program) KindValue() Kind {
	if p == nil {
		return 0
	}
	return p.Kind
}

// Empty 判断程序是否缺少可执行后端。
func (p *Program) Empty() bool { return p == nil || (p.NFA == nil && p.DFA == nil) }

const Version = 2

func (p *Program) Validate() error {
	if p == nil {
		return fmt.Errorf("nil engine program")
	}
	if p.Kind != KindNFA && p.Kind != KindDFA {
		return fmt.Errorf("invalid engine kind")
	}
	if p.Kind == KindNFA && p.NFA == nil {
		return fmt.Errorf("missing NFA backend")
	}
	if p.Kind == KindNFA && p.DFA != nil {
		return fmt.Errorf("unexpected DFA backend")
	}
	if p.NFA != nil && p.NFA.Graph != nil {
		if err := p.NFA.Validate(); err != nil {
			return err
		}
	}
	if p.DFA != nil && p.DFA.Graph != nil {
		if err := p.DFA.Validate(); err != nil {
			return err
		}
	}
	if p.Kind == KindDFA && p.DFA == nil {
		return fmt.Errorf("missing DFA backend")
	}
	if p.Kind == KindDFA && p.NFA != nil {
		return fmt.Errorf("unexpected NFA backend")
	}
	if p.Kind == KindNFA && p.NFA != nil && p.DFA != nil {
		return fmt.Errorf("ambiguous engine backends")
	}
	if p.Kind == KindDFA && p.NFA != nil && p.DFA != nil {
		return fmt.Errorf("ambiguous engine backends")
	}
	return nil
}
func (p *Program) Dump() ([]byte, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	var raw []byte
	var err error
	if p.NFA != nil {
		raw, err = p.NFA.Dump()
	}
	if p.DFA != nil {
		raw, err = p.DFA.Dump()
	}
	if err != nil {
		return nil, err
	}
	return json.Marshal(struct {
		Version int    `json:"version"`
		Kind    Kind   `json:"kind"`
		Program []byte `json:"program"`
	}{Version, p.Kind, raw})
}
func Load(data []byte) (*Program, error) {
	if len(data) > 64<<20 {
		return nil, fmt.Errorf("engine payload exceeds size limit")
	}
	var d struct {
		Version int    `json:"version"`
		Kind    Kind   `json:"kind"`
		Graph   []byte `json:"graph"`
		Program []byte `json:"program"`
	}
	if err := json.Unmarshal(data, &d); err != nil {
		return nil, err
	}
	if d.Version != 1 && d.Version != Version {
		return nil, fmt.Errorf("unsupported engine version %d", d.Version)
	}
	if d.Version == 1 {
		g, err := nfagraph.Load(d.Graph)
		if err != nil {
			return nil, err
		}
		return Build(g, d.Kind)
	}
	switch d.Kind {
	case KindNFA:
		p, err := nfa.Load(d.Program)
		if err != nil {
			return nil, err
		}
		return &Program{Kind: KindNFA, NFA: p}, nil
	case KindDFA:
		p, err := dfa.Load(d.Program)
		if err != nil {
			return nil, err
		}
		return &Program{Kind: KindDFA, DFA: p}, nil
	default:
		return nil, fmt.Errorf("invalid engine kind %d", d.Kind)
	}
}

func Build(g *nfagraph.Graph, kind Kind) (*Program, error) {
	return BuildWithLimits(g, kind, 0)
}

// BuildWithLimits 构建指定后端，并对确定性后端施加状态数量限制。
func BuildWithLimits(g *nfagraph.Graph, kind Kind, dfaLimit int) (*Program, error) {
	return BuildWithBudgets(g, kind, dfaLimit, 0)
}

// BuildWithBudgets 构建后端并对确定性状态和估算内存施加预算。
func BuildWithBudgets(g *nfagraph.Graph, kind Kind, dfaLimit int, memoryLimit uint64) (*Program, error) {
	switch kind {
	case KindDFA:
		p, err := dfa.CompileWithBudgets(g, dfaLimit, memoryLimit)
		if err != nil {
			return nil, err
		}
		return &Program{Kind: kind, DFA: p}, nil
	case KindNFA:
		p, err := nfa.Compile(g)
		if err != nil {
			return nil, err
		}
		if memoryLimit > 0 && p.MemoryBytes() > memoryLimit {
			return nil, nfa.ErrMemoryLimit
		}
		return &Program{Kind: KindNFA, NFA: p}, nil
	default:
		return nil, fmt.Errorf("invalid engine kind %d", kind)
	}
}

// BuildWithDFAOptions 使用确定化选项构建后端，非 DFA 后端忽略该选项。
func BuildWithDFAOptions(g *nfagraph.Graph, kind Kind, options dfa.CompileOptions) (*Program, error) {
	if kind == KindDFA {
		p, err := dfa.CompileWithOptions(g, options)
		if err != nil {
			return nil, err
		}
		return &Program{Kind: KindDFA, DFA: p}, nil
	}
	return Build(g, kind)
}

func (p *Program) Clone() *Program {
	if p == nil {
		return nil
	}
	out := &Program{Kind: p.Kind}
	if p.NFA != nil {
		out.NFA = p.NFA.Clone()
	}
	if p.DFA != nil {
		out.DFA = p.DFA.Clone()
	}
	return out
}
func (p *Program) MatchAt(data []byte, start int) []int {
	return p.MatchAtLimit(data, start, 0)
}

// RunExact 仅在完整输入被当前后端接受时返回成功。
func (p *Program) RunExact(data []byte) bool {
	if p == nil {
		return false
	}
	if p.DFA != nil {
		_, ok := p.DFA.RunExact(data)
		return ok
	}
	if p.NFA == nil {
		return false
	}
	for _, end := range p.NFA.MatchAt(data, 0) {
		if end == len(data) {
			return true
		}
	}
	return false
}

// LongestMatchAt 返回指定起点的最长接受结束偏移。
func (p *Program) LongestMatchAt(data []byte, start int) (int, bool) {
	if p == nil {
		return 0, false
	}
	ends := p.MatchAt(data, start)
	if len(ends) == 0 {
		return 0, false
	}
	return ends[len(ends)-1], true
}

// StartBytes 返回后端可接受的起始字节集合。
func (p *Program) StartBytes() []byte {
	if p == nil {
		return nil
	}
	if p.DFA != nil {
		return p.DFA.StartBytes()
	}
	return nil
}

// MatchAtLimit 使用指定状态或读取预算执行一次匹配。
func (p *Program) MatchAtLimit(data []byte, start int, limit int) []int {
	if p == nil {
		return nil
	}
	if limit < 0 {
		return nil
	}
	if p.DFA != nil {
		if p.DFA.ByteTable {
			return p.DFA.TableMatchAtLimit(data, start, limit)
		}
		if limit == 0 {
			return p.DFA.MatchAt(data, start)
		}
		return p.DFA.MatchAtLimit(data, start, limit)
	}
	if p.NFA != nil {
		return p.NFA.MatchAtLimit(data, start, limit)
	}
	return nil
}

// MatchAtResultLimit 返回指定起点的接受结束位置，并限制结果数量。
func (p *Program) MatchAtResultLimit(data []byte, start, limit int) []int {
	if p == nil || limit < 0 {
		return nil
	}
	ends := p.MatchAt(data, start)
	if limit > 0 && len(ends) > limit {
		return ends[:limit]
	}
	return ends
}

// SpansRange 返回完全位于半开区间内的匹配区间。
func (p *Program) SpansRange(data []byte, from, to int) [][2]int {
	if p == nil || from < 0 || to < from || to > len(data) {
		return nil
	}
	out := make([][2]int, 0)
	for _, span := range p.Spans(data) {
		if span[0] >= from && span[1] <= to {
			out = append(out, span)
		}
	}
	return out
}

func (p *Program) Match(data []byte) []int {
	if p == nil {
		return nil
	}
	out := []int{}
	seen := map[int]struct{}{}
	for i := 0; i <= len(data); i++ {
		for _, end := range p.MatchAt(data, i) {
			if _, ok := seen[end]; ok {
				continue
			}
			seen[end] = struct{}{}
			out = append(out, end)
		}
	}
	return out
}

// MatchLimit 返回不超过 limit 个结束偏移；零值表示不限制。
func (p *Program) MatchLimit(data []byte, limit int) []int {
	if limit < 0 {
		return nil
	}
	out := p.Match(data)
	if limit > 0 && len(out) > limit {
		return out[:limit]
	}
	return out
}
func (p *Program) MatchFirst(data []byte) (start, end int, ok bool) {
	if p == nil {
		return 0, 0, false
	}
	for i := 0; i <= len(data); i++ {
		if ends := p.MatchAt(data, i); len(ends) > 0 {
			return i, ends[0], true
		}
	}
	return 0, 0, false
}

// Spans 返回输入中的全部起止区间，并按起点和终点排序。
func (p *Program) Spans(data []byte) [][2]int {
	if p == nil {
		return nil
	}
	out := make([][2]int, 0)
	seen := map[[2]int]struct{}{}
	for start := 0; start <= len(data); start++ {
		for _, end := range p.MatchAt(data, start) {
			span := [2]int{start, end}
			if _, ok := seen[span]; ok {
				continue
			}
			seen[span] = struct{}{}
			out = append(out, span)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i][0] != out[j][0] {
			return out[i][0] < out[j][0]
		}
		return out[i][1] < out[j][1]
	})
	return out
}

// SpansLimit 返回不超过 limit 个区间；零值表示不限制。
func (p *Program) SpansLimit(data []byte, limit int) [][2]int {
	if limit < 0 {
		return nil
	}
	if limit == 0 {
		return p.Spans(data)
	}
	if p == nil {
		return nil
	}
	out := make([][2]int, 0, limit)
	seen := make(map[[2]int]struct{}, limit)
	for start := 0; start <= len(data) && len(out) < limit; start++ {
		for _, end := range p.MatchAt(data, start) {
			span := [2]int{start, end}
			if _, ok := seen[span]; ok {
				continue
			}
			seen[span] = struct{}{}
			out = append(out, span)
			if len(out) >= limit {
				break
			}
		}
	}
	return out
}

// ScanEach 按起点、终点顺序回调程序命中区间。
func (p *Program) ScanEach(data []byte, fn func([2]int) bool) {
	if p == nil || fn == nil {
		return
	}
	seen := map[[2]int]struct{}{}
	for start := 0; start <= len(data); start++ {
		for _, end := range p.MatchAt(data, start) {
			span := [2]int{start, end}
			if _, ok := seen[span]; ok {
				continue
			}
			seen[span] = struct{}{}
			if !fn(span) {
				return
			}
		}
	}
}

// AcceptsAt 判断指定起止区间是否被后端接受。
func (p *Program) AcceptsAt(data []byte, start, end int) bool {
	if p == nil || start < 0 || end < start || end > len(data) {
		return false
	}
	for _, got := range p.MatchAt(data, start) {
		if got == end {
			return true
		}
	}
	return false
}

// EquivalentResults 比较当前后端与另一后端在输入上的匹配区间。
func (p *Program) EquivalentResults(other *Program, data []byte) bool {
	if p == nil || other == nil {
		return p == other
	}
	a, b := p.Spans(data), other.Spans(data)
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// MatchAtRange 返回指定起点且结束偏移位于区间内的结果。
func (p *Program) MatchAtRange(data []byte, start, from, to, limit int) []int {
	if p == nil || limit < 0 || from < 0 || to < from {
		return nil
	}
	out := make([]int, 0)
	for _, end := range p.MatchAt(data, start) {
		if end < from || end > to {
			continue
		}
		out = append(out, end)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out
}

// SpansRangeLimit 返回完全位于指定区间内的匹配区间。
func (p *Program) SpansRangeLimit(data []byte, from, to, limit int) [][2]int {
	if p == nil || from < 0 || to < from || to > len(data) || limit < 0 {
		return nil
	}
	out := make([][2]int, 0)
	seen := map[[2]int]struct{}{}
	for start := from; start <= to; start++ {
		for _, end := range p.MatchAt(data, start) {
			if end > to {
				continue
			}
			span := [2]int{start, end}
			if _, ok := seen[span]; ok {
				continue
			}
			seen[span] = struct{}{}
			out = append(out, span)
			if limit > 0 && len(out) >= limit {
				return out
			}
		}
	}
	return out
}

func SelectKind(g *nfagraph.Graph) Kind {
	if g == nil || !dfa.ByteTableEligible(g) {
		return KindNFA
	}
	a := nfagraph.Analyze(g)
	if len(g.Accepts()) == 0 {
		return KindNFA
	}
	// 过宽循环会造成确定化状态爆炸，优先保留线性状态机。
	for _, width := range a.LoopWidth {
		if width > 256 {
			return KindNFA
		}
	}
	// 大图或循环出口稳定可收敛时优先确定化；构造预算不足时安全回退。
	cyclic := a.RegionCount() > 0
	stableExit := false
	if accepts := g.Accepts(); len(accepts) == 1 {
		stableExit = a.PostDominates(accepts[0], g.Start)
	}
	if len(g.Nodes) > 64 || cyclic && stableExit {
		return KindDFA
	}
	return KindNFA
}

// SelectionInfo 描述自动后端选择依据。
type SelectionInfo struct {
	Kind         Kind
	ByteTable    bool
	Vertices     int
	SCC          int
	LoopNodes    int
	MaxLoopWidth int
	Accepts      int
	Reason       string
}

// SelectionFeatures 描述图之外会影响后端适用性的运行能力。
// 这些能力无法由字节状态表完整表达时，调用方应保留 AST 确认路径。
type SelectionFeatures struct {
	Stateful   bool
	Assertions bool
	UTF8       bool
	UCP        bool
}

// SelectKindForFeatures 按图特征和运行能力选择图后端。
// 需要状态或 Unicode 语义的规则不进入 DFA，调用方可直接使用 AST 路径。
func SelectKindForFeatures(g *nfagraph.Graph, features SelectionFeatures) Kind {
	if g == nil || features.Stateful || features.Assertions || features.UTF8 || features.UCP {
		return KindNFA
	}
	return SelectKind(g)
}

// ExplainSelection 返回后端选择诊断信息。
func ExplainSelection(g *nfagraph.Graph) SelectionInfo {
	info := SelectionInfo{Kind: SelectKind(g)}
	if g == nil {
		info.Reason = "nil graph"
		return info
	}
	info.Vertices = len(g.Nodes)
	info.ByteTable = dfa.ByteTableEligible(g)
	a := nfagraph.Analyze(g)
	info.SCC = len(a.SCC)
	info.Accepts = len(g.Accepts())
	for node, width := range a.LoopWidth {
		if a.InCycle(node) {
			info.LoopNodes++
		}
		if width > info.MaxLoopWidth {
			info.MaxLoopWidth = width
		}
	}
	if !info.ByteTable {
		info.Reason = "non-byte graph"
	} else if info.MaxLoopWidth > 256 {
		info.Reason = "loop width exceeds deterministic budget"
	} else if info.Kind == KindDFA {
		info.Reason = "large or cyclic graph"
	} else {
		info.Reason = "small deterministic candidate"
	}
	return info
}
func SelectKindWithLimit(g *nfagraph.Graph, limit int) Kind {
	return SelectKindWithStateBudget(g, limit)
}

// SelectKindWithStateBudget 在预算不足时选择 NFA，避免自动构造超限 DFA。
func SelectKindWithStateBudget(g *nfagraph.Graph, limit int) Kind {
	if limit < 0 {
		return KindNFA
	}
	if SelectKind(g) != KindDFA || limit == 0 {
		return SelectKind(g)
	}
	states, err := dfa.Determinize(g, limit)
	if err != nil || len(states) > limit {
		return KindNFA
	}
	return KindDFA
}
func CompileAuto(g *nfagraph.Graph) (*Program, error) { return Build(g, SelectKind(g)) }

// CompileAutoWithLimits 按图特征选择后端并应用确定性状态限制。
func CompileAutoWithLimits(g *nfagraph.Graph, dfaLimit int) (*Program, error) {
	return CompileAutoWithBudgets(g, dfaLimit, 0)
}

// CompileAutoWithBudgets 自动选择后端并在确定性构造超限时安全回退。
func CompileAutoWithBudgets(g *nfagraph.Graph, dfaLimit int, memoryLimit uint64) (*Program, error) {
	kind := SelectKind(g)
	p, err := BuildWithBudgets(g, kind, dfaLimit, memoryLimit)
	if kind == KindDFA && (errors.Is(err, dfa.ErrStateLimit) || errors.Is(err, dfa.ErrMemoryLimit)) {
		return Build(g, KindNFA)
	}
	return p, err
}

// CompileAutoWithFeatures 根据运行能力选择图后端并在预算不足时回退。
func CompileAutoWithFeatures(g *nfagraph.Graph, features SelectionFeatures, dfaLimit int, memoryLimit uint64) (*Program, error) {
	kind := SelectKindForFeatures(g, features)
	p, err := BuildWithBudgets(g, kind, dfaLimit, memoryLimit)
	if kind == KindDFA && (errors.Is(err, dfa.ErrStateLimit) || errors.Is(err, dfa.ErrMemoryLimit)) {
		return Build(g, KindNFA)
	}
	return p, err
}
func (p *Program) BackendName() string {
	if p == nil {
		return ""
	}
	if p.Kind == KindDFA {
		return "dfa"
	}
	return "nfa"
}
func (p *Program) StateCount() int {
	if p == nil {
		return 0
	}
	if p.NFA != nil {
		if p.NFA.Graph == nil {
			return 0
		}
		return len(p.NFA.Graph.Nodes)
	}
	if p.DFA != nil {
		return p.DFA.StateCount()
	}
	return 0
}
