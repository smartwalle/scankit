package dfa

import (
	"fmt"
	"github.com/smartwalle/scankit/internal/graph"
	"github.com/smartwalle/scankit/internal/nfa"
	"github.com/smartwalle/scankit/internal/nfagraph"
	"sort"
)

// ReverseProgram 在输入右边界向左确认匹配。对字节状态表使用反向状态表，
// 其他图结构保留语义等价的前向回退路径。
type ReverseProgram struct {
	*Program
	forward *nfa.Program
}

// UsesReverseTable 判断当前反向程序是否真正使用了字节反向状态表。
func (p *ReverseProgram) UsesReverseTable() bool {
	return p != nil && p.Program != nil && p.Program.ByteTable
}

// ReverseEligible 判断图是否适合构造反向字节状态表。
// 含 Unicode、断言或无界循环的图保留正向确认路径，避免反向表改变语义。
func ReverseEligible(g *nfagraph.Graph) bool {
	if g == nil || g.Validate() != nil || !byteTableCompatible(nfagraph.ExpandLiterals(g)) {
		return false
	}
	_, max := reverseWidth(g)
	return max >= 0
}

func reverseWidth(g *nfagraph.Graph) (int, int) {
	if g == nil || g.Flow == nil {
		return 0, -1
	}
	// 宽度必须按起点到接受节点的路径计算，不能把分支上的节点简单求和。
	// DFS 同时检测回边；存在可消费循环时最大宽度记为无界，反向表拒绝该图。
	type result struct{ min, max int }
	const inf = int(^uint(0) >> 1)
	memo := make(map[graph.Vertex]result, len(g.Nodes))
	state := make(map[graph.Vertex]uint8, len(g.Nodes))
	var walk func(graph.Vertex) result
	walk = func(id graph.Vertex) result {
		if state[id] == 2 {
			return memo[id]
		}
		if state[id] == 1 {
			return result{min: 0, max: -1}
		}
		n := g.Nodes[id]
		if n == nil {
			return result{min: inf, max: -1}
		}
		state[id] = 1
		width := 0
		switch n.Kind {
		case nfagraph.KindLiteral:
			width = len(n.Literal)
		case nfagraph.KindClass:
			width = 1
		case nfagraph.KindRepeat:
			if n.RepeatMax < 0 {
				state[id] = 2
				// Repeat 节点本身是零宽控制节点，循环体的消费宽度
				// 由后继路径累计；只要存在无界回边，最大宽度即无界。
				memo[id] = result{min: 0, max: -1}
				return memo[id]
			}
			width = 0
		}
		if n.Kind == nfagraph.KindAccept {
			state[id] = 2
			memo[id] = result{}
			return memo[id]
		}
		succ := g.Flow.Successors(id)
		if len(succ) == 0 {
			state[id] = 2
			memo[id] = result{min: inf, max: -1}
			return memo[id]
		}
		bestMin, bestMax := inf, 0
		unbounded := false
		for _, to := range succ {
			r := walk(to)
			if r.min == inf {
				continue
			}
			if width+r.min < bestMin {
				bestMin = width + r.min
			}
			if r.max < 0 {
				unbounded = true
			} else if width+r.max > bestMax {
				bestMax = width + r.max
			}
		}
		state[id] = 2
		if bestMin == inf {
			memo[id] = result{min: inf, max: -1}
		} else if unbounded {
			memo[id] = result{min: bestMin, max: -1}
		} else {
			memo[id] = result{min: bestMin, max: bestMax}
		}
		return memo[id]
	}
	r := walk(g.Start)
	if r.min == inf {
		return 0, -1
	}
	return r.min, r.max
}

// Validate 校验反向程序及其前向确认程序。
func (p *ReverseProgram) Validate() error {
	if p == nil || p.Program == nil || p.forward == nil {
		return fmt.Errorf("invalid reverse program")
	}
	if err := p.Program.Validate(); err != nil {
		return err
	}
	return p.forward.Validate()
}

func (p *ReverseProgram) Clone() *ReverseProgram {
	if p == nil || p.Program == nil {
		return nil
	}
	var forward *nfa.Program
	if p.forward != nil {
		forward = p.forward.Clone()
	}
	return &ReverseProgram{Program: p.Program.Clone(), forward: forward}
}

func CompileReverse(g *nfagraph.Graph) (*ReverseProgram, error) {
	if g == nil {
		return nil, fmt.Errorf("nil graph")
	}
	reversed, err := reverseGraph(g)
	if err != nil {
		return nil, err
	}
	p, e := Compile(reversed)
	if e != nil {
		return nil, e
	}
	forward, err := nfa.Compile(g)
	if err != nil {
		return nil, err
	}
	return &ReverseProgram{Program: p, forward: forward}, nil
}

func reverseGraph(g *nfagraph.Graph) (*nfagraph.Graph, error) {
	if err := g.Validate(); err != nil {
		return nil, err
	}
	out := g.Clone()
	oldStart := g.Start
	accepts := g.Accepts()
	out.Flow = g.Flow.Reverse()
	maxID := graph.Vertex(0)
	for _, id := range out.NodeIDs() {
		if id > maxID {
			maxID = id
		}
	}
	newStart := maxID + 1
	newAccept := maxID + 2
	if node, ok := out.Nodes[oldStart]; ok {
		node.Kind = nfagraph.KindJoin
	}
	for _, id := range accepts {
		if node, ok := out.Nodes[id]; ok {
			node.Kind = nfagraph.KindJoin
		}
	}
	for _, node := range out.Nodes {
		if node.Kind != nfagraph.KindLiteral || len(node.Literal) < 2 {
			continue
		}
		for left, right := 0, len(node.Literal)-1; left < right; left, right = left+1, right-1 {
			node.Literal[left], node.Literal[right] = node.Literal[right], node.Literal[left]
		}
	}
	out.AddNode(nfagraph.Node{ID: newStart, Kind: nfagraph.KindStart})
	out.AddNode(nfagraph.Node{ID: newAccept, Kind: nfagraph.KindAccept})
	out.Start = newStart
	for _, id := range accepts {
		out.AddEdge(newStart, id)
	}
	out.AddEdge(oldStart, newAccept)
	return out, out.Validate()
}

// MatchReverseAt 返回恰好在 end 结束的匹配起点，结果按起点升序排列。
func (p *ReverseProgram) MatchReverseAt(data []byte, end int) []int {
	return p.MatchReverseAtLimit(data, end, 0)
}

// MatchReverseAtBounds 在指定右边界执行反向确认，同时独立限制读取字节数和结果数。
func (p *ReverseProgram) MatchReverseAtBounds(data []byte, end, maxRead, maxResults int) []int {
	if p == nil || maxRead < 0 || maxResults < 0 {
		return nil
	}
	starts := p.MatchReverseAtLimit(data, end, 0)
	if maxRead > 0 {
		minimum := end - maxRead
		filtered := starts[:0]
		for _, start := range starts {
			if start >= minimum {
				filtered = append(filtered, start)
			}
		}
		starts = filtered
	}
	if maxResults > 0 && len(starts) > maxResults {
		starts = starts[:maxResults]
	}
	return starts
}

// MatchReverseAtLimit 在指定右边界向左确认匹配，并限制最多读取的字节数。
func (p *ReverseProgram) MatchReverseAtLimit(data []byte, end, limit int) []int {
	return p.MatchReverseAtLimitInto(data, end, limit, nil)
}

// MatchReverseAtLimitInto 将反向确认起点写入复用缓冲，避免逐结束位置分配切片。
func (p *ReverseProgram) MatchReverseAtLimitInto(data []byte, end, limit int, dst []int) []int {
	if p == nil || p.Program == nil || p.forward == nil || end < 0 || end > len(data) || limit < 0 {
		return dst[:0]
	}
	if !p.ByteTable {
		out := dst[:0]
		startAt := 0
		if limit > 0 && end-limit > startAt {
			startAt = end - limit
		}
		for start := startAt; start <= end; start++ {
			for _, got := range p.forward.MatchAt(data, start) {
				if got == end {
					out = append(out, start)
					break
				}
			}
		}
		return out
	}
	state := p.Start
	out := dst[:0]
	if p.IsAccept(state) {
		out = append(out, end)
		if limit == 1 {
			return out
		}
	}
	minimum := 0
	if limit > 0 && end-limit > minimum {
		minimum = end - limit
	}
	for offset := end - 1; offset >= minimum; offset-- {
		next, ok := p.Transition(state, data[offset])
		if !ok {
			break
		}
		state = next
		if p.IsAccept(state) {
			out = append(out, offset)
			if limit > 0 && len(out) >= limit {
				break
			}
		}
	}
	sort.Ints(out)
	return out
}

// MatchReverseAtResultLimit 在固定右边界限制返回结果数量，不改变读取范围。
func (p *ReverseProgram) MatchReverseAtResultLimit(data []byte, end, maxResults int) []int {
	if p == nil || maxResults < 0 {
		return nil
	}
	starts := p.MatchReverseAtLimit(data, end, 0)
	if maxResults > 0 && len(starts) > maxResults {
		return starts[:maxResults]
	}
	return starts
}

// ReverseSpans 返回全部匹配区间，并按起点、终点稳定排序。
func (p *ReverseProgram) ReverseSpans(data []byte) []nfa.Span {
	return p.ReverseSpansLimit(data, 0)
}

// ReverseSpansLimit 返回不超过 limit 个按起点排序的反向确认区间。
func (p *ReverseProgram) ReverseSpansLimit(data []byte, limit int) []nfa.Span {
	if p == nil {
		return nil
	}
	if limit < 0 {
		return nil
	}
	out := make([]nfa.Span, 0)
	startsBuf := make([]int, 0, 4)
	for end := 0; end <= len(data); end++ {
		if limit > 0 && len(out) >= limit {
			break
		}
		startsBuf = p.MatchReverseAtLimitInto(data, end, 0, startsBuf[:0])
		for _, start := range startsBuf {
			out = append(out, nfa.Span{From: start, To: end})
			if limit > 0 && len(out) >= limit {
				break
			}
		}
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].From != out[j].From {
			return out[i].From < out[j].From
		}
		return out[i].To < out[j].To
	})
	if limit > 0 && len(out) > limit {
		return out[:limit]
	}
	return out
}

// MatchReverse 返回全部反向确认得到的匹配起点，按右边界从右至左排列。
func (p *ReverseProgram) MatchReverse(data []byte) []int {
	if p == nil {
		return nil
	}
	out := []int{}
	for end := len(data); end >= 0; end-- {
		starts := p.MatchReverseAt(data, end)
		for i := len(starts) - 1; i >= 0; i-- {
			out = append(out, starts[i])
		}
	}
	return out
}
func (p *ReverseProgram) MatchReverseFirst(data []byte) (int, bool) {
	all := p.MatchReverse(data)
	if len(all) == 0 {
		return 0, false
	}
	return all[0], true
}
