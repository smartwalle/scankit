// Package dfa 根据通用图构造确定性状态表。
package dfa

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/smartwalle/scankit/internal/graph"
	"github.com/smartwalle/scankit/internal/nfa"
	"github.com/smartwalle/scankit/internal/nfagraph"
	"github.com/smartwalle/scankit/internal/parser"
)

const version = 3

// ErrStateLimit 表示确定化状态数超过调用方预算。
var ErrStateLimit = errors.New("dfa state limit exceeded")

// ErrMemoryLimit 表示确定化过程的估算内存超过预算。
var ErrMemoryLimit = errors.New("dfa memory limit exceeded")

// State 是确定化后的 DFA 状态，Nodes 为对应的 NFA 顶点集合。
type State struct {
	ID     uint32
	Nodes  []graph.Vertex
	Accept bool
	// Reports 保存本状态在任意位置即可触发的报告编号，按升序去重。
	// 与底层图上的报告节点一一对应，供确定化后在当前偏移直接上报。
	Reports []uint32 `json:"reports,omitempty"`
	// ReportsEOD 保存本状态只在数据末尾（EOD）触发的报告编号，按升序去重。
	// 这些编号来自必须经过末尾断言才能到达的报告节点。
	ReportsEOD  []uint32 `json:"reports_eod,omitempty"`
	Transitions map[byte]uint32
}

// Program 是字节状态表形式的确定性程序。
type Program struct {
	*nfa.Program
	States    []State
	Start     uint32
	ByteTable bool
	// Minimized 表示状态节点已按语言等价类合并，不能再逐节点重放转移校验。
	Minimized bool
	dense     [][256]uint32
}

// CompileOptions 控制确定化过程中的状态、内存和表布局策略。
type CompileOptions struct {
	StateLimit  int
	MemoryLimit uint64
	Dense       bool
	DeadState   bool
}

// Validate 检查状态编号、转移目标和底层图。
func (p *Program) Validate() error {
	if p == nil || p.Program == nil {
		return fmt.Errorf("nil dfa program")
	}
	if err := p.Graph.Validate(); err != nil {
		return err
	}
	if int(p.Start) >= len(p.States) {
		return fmt.Errorf("invalid dfa start state")
	}
	if len(p.dense) != 0 && len(p.dense) != len(p.States) {
		return fmt.Errorf("invalid dense dfa table")
	}
	if !p.ByteTable && len(p.dense) != 0 {
		return fmt.Errorf("dense dfa table requires byte table mode")
	}
	for i, state := range p.States {
		if int(state.ID) != i {
			return fmt.Errorf("invalid dfa state id")
		}
		accept := false
		last := graph.Vertex(0)
		for index, node := range state.Nodes {
			if index > 0 && node <= last {
				return fmt.Errorf("dfa state nodes are not ordered")
			}
			last = node
			graphNode := p.Graph.Nodes[node]
			if graphNode == nil {
				return fmt.Errorf("dfa state references unknown node")
			}
			accept = accept || graphNode.Kind == nfagraph.KindAccept
		}
		if accept != state.Accept {
			return fmt.Errorf("dfa state acceptance mismatch")
		}
		if err := validateReportSet("reports", state.Reports, false); err != nil {
			return err
		}
		if err := validateReportSet("reports_eod", state.ReportsEOD, false); err != nil {
			return err
		}
		for _, id := range state.ReportsEOD {
			if index := sort.Search(len(state.Reports), func(i int) bool { return state.Reports[i] >= id }); index < len(state.Reports) && state.Reports[index] == id {
				return fmt.Errorf("dfa state reports overlap for id %d", id)
			}
		}
		// 报告编号必须能追溯到本状态节点集合中的报告节点，
		// 避免篡改后的状态表携带无法解释的报告。
		if len(state.Reports) != 0 || len(state.ReportsEOD) != 0 {
			available := make(map[uint32]struct{}, len(state.Nodes))
			for _, nodeID := range state.Nodes {
				if node := p.Graph.Nodes[nodeID]; node != nil && node.ReportID != 0 {
					available[node.ReportID] = struct{}{}
				}
			}
			for _, id := range state.Reports {
				if _, ok := available[id]; !ok {
					return fmt.Errorf("dfa state reports untraceable report id %d", id)
				}
			}
			for _, id := range state.ReportsEOD {
				if _, ok := available[id]; !ok {
					return fmt.Errorf("dfa state eod reports untraceable report id %d", id)
				}
			}
		}
		if len(state.Nodes) == 0 {
			if state.Accept {
				return fmt.Errorf("dead state cannot accept")
			}
			if p.ByteTable {
				for value := range 256 {
					if state.Transitions[byte(value)] != state.ID {
						return fmt.Errorf("invalid dead state transition")
					}
				}
			}
			continue
		}
		for _, to := range state.Transitions {
			if int(to) >= len(p.States) {
				return fmt.Errorf("invalid dfa transition target")
			}
		}
		if len(p.dense) == len(p.States) {
			for value := range 256 {
				to := p.dense[i][value]
				if to == ^uint32(0) {
					if _, exists := state.Transitions[byte(value)]; exists {
						return fmt.Errorf("dense dfa omits sparse transition")
					}
					continue
				}
				if int(to) >= len(p.States) {
					return fmt.Errorf("dense dfa transition target out of bounds")
				}
				if sparse, exists := state.Transitions[byte(value)]; !exists || sparse != to {
					return fmt.Errorf("dense dfa differs from sparse transition")
				}
			}
		}
		if p.ByteTable && !p.Minimized {
			for value := range 256 {
				expected := epsilonClosure(p.Graph, move(p.Graph, state.Nodes, byte(value)))
				to, exists := state.Transitions[byte(value)]
				if len(expected) == 0 {
					if exists {
						return fmt.Errorf("unexpected dfa transition")
					}
					if len(p.dense) == len(p.States) && p.dense[i][value] != ^uint32(0) {
						return fmt.Errorf("dense dfa transition mismatch")
					}
					continue
				}
				if !exists || !sameVertices(expected, p.States[to].Nodes) {
					return fmt.Errorf("dfa transition does not match state nodes")
				}
				if len(p.dense) == len(p.States) && p.dense[i][value] != to {
					return fmt.Errorf("dense dfa transition mismatch")
				}
			}
		}
	}
	return nil
}

func sameVertices(a, b []graph.Vertex) bool {
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

// validateReportSet 校验报告编号列表按升序严格去重且不含零值占位。
func validateReportSet(field string, ids []uint32, allowZero bool) error {
	for i, id := range ids {
		if id == 0 && !allowZero {
			return fmt.Errorf("dfa state %s contains zero report id", field)
		}
		if i > 0 && ids[i-1] >= id {
			return fmt.Errorf("dfa state %s is not sorted or unique", field)
		}
	}
	return nil
}

// reportIDsOf 收集节点集合中可直接触发的报告编号，并区分末尾断言之后才可见的部分。
// 只有接受状态的节点集合会携带报告：报告节点必须与接受节点同处一个闭包，
// 才会在某个偏移真正触发，这一点与源码级确定化的接受状态报告语义一致。
// 返回值保证升序去重，且两个集合互不重叠。
func reportIDsOf(g *nfagraph.Graph, nodes []graph.Vertex, eodGuarded map[graph.Vertex]bool) (reports, reportsEOD []uint32) {
	if g == nil {
		return nil, nil
	}
	for _, id := range nodes {
		node := g.Nodes[id]
		if node == nil || node.ReportID == 0 {
			continue
		}
		if eodGuarded[id] {
			reportsEOD = append(reportsEOD, node.ReportID)
			continue
		}
		reports = append(reports, node.ReportID)
	}
	reports = compactReports(reports)
	reportsEOD = compactReports(reportsEOD)
	reportsEOD = subtractReports(reportsEOD, reports)
	return reports, reportsEOD
}

// compactReports 就地排序去重报告编号，保持升序不变式。
func compactReports(ids []uint32) []uint32 {
	if len(ids) < 2 {
		return ids
	}
	slices.Sort(ids)
	write := 1
	for _, id := range ids[1:] {
		if id == ids[write-1] {
			continue
		}
		ids[write] = id
		write++
	}
	return ids[:write]
}

// subtractReports 从有序集合中移除另一个有序集合包含的编号。
func subtractReports(ids, remove []uint32) []uint32 {
	if len(ids) == 0 || len(remove) == 0 {
		return ids
	}
	out := ids[:0]
	index := 0
	for _, id := range ids {
		for index < len(remove) && remove[index] < id {
			index++
		}
		if index < len(remove) && remove[index] == id {
			continue
		}
		out = append(out, id)
	}
	return out
}

// Dump 序列化确定性程序及其图。
func (p *Program) Dump() ([]byte, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	raw, err := nfagraph.Dump(p.Graph)
	if err != nil {
		return nil, err
	}
	return json.Marshal(struct {
		Version   int     `json:"version"`
		Graph     []byte  `json:"graph"`
		Start     uint32  `json:"start"`
		ByteTable bool    `json:"byte_table"`
		Minimized bool    `json:"minimized"`
		States    []State `json:"states"`
	}{version, raw, p.Start, p.ByteTable, p.Minimized, p.States})
}

// Load 反序列化确定性程序，并校验保存的状态表与图结构。
func Load(data []byte) (*Program, error) {
	if len(data) > 64<<20 {
		return nil, fmt.Errorf("dfa payload exceeds size limit")
	}
	var d struct {
		Version   int     `json:"version"`
		Graph     []byte  `json:"graph"`
		Start     uint32  `json:"start"`
		ByteTable bool    `json:"byte_table"`
		Minimized bool    `json:"minimized"`
		States    []State `json:"states"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&d); err != nil {
		return nil, err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, fmt.Errorf("dfa payload contains multiple values")
		}
		return nil, err
	}
	if d.Version != 1 && d.Version != 2 && d.Version != version {
		return nil, fmt.Errorf("unsupported dfa version %d", d.Version)
	}
	g, err := nfagraph.Load(d.Graph)
	if err != nil {
		return nil, err
	}
	base, err := nfa.Compile(g)
	if err != nil {
		return nil, err
	}
	if d.Version == 1 {
		p, err := Compile(g)
		if err != nil {
			return nil, err
		}
		if d.Start >= uint32(len(p.States)) {
			return nil, fmt.Errorf("invalid dfa start state")
		}
		p.Start = d.Start
		if !d.ByteTable {
			p.ByteTable = false
			p.dense = nil
		}
		return p, p.Validate()
	}
	p := &Program{Program: base, States: cloneStates(d.States), Start: d.Start, ByteTable: d.ByteTable, Minimized: d.Minimized}
	p.rebuildDense()
	if err := p.Validate(); err != nil {
		return nil, err
	}
	return p, nil
}

// StateCount 返回状态数量，nil 程序返回 0。
func (p *Program) StateCount() int {
	if p == nil {
		return 0
	}
	return len(p.States)
}

// TransitionCount 返回所有稀疏转移数量。
func (p *Program) TransitionCount() int {
	if p == nil {
		return 0
	}
	n := 0
	for _, state := range p.States {
		n += len(state.Transitions)
	}
	return n
}

// MemoryBytes 返回状态表及其转移存储的保守内存估算值。
func (p *Program) MemoryBytes() uint64 {
	if p == nil {
		return 0
	}
	const stateBytes = uint64(64)
	const transitionBytes = uint64(8)
	const denseStateBytes = uint64(256 * 4)
	states := uint64(len(p.States))
	if states > ^uint64(0)/stateBytes {
		return ^uint64(0)
	}
	total := states * stateBytes
	for _, state := range p.States {
		count := uint64(len(state.Transitions))
		if count > (^uint64(0)-total)/transitionBytes {
			return ^uint64(0)
		}
		total += count * transitionBytes
		nodes := uint64(len(state.Nodes))
		if nodes > (^uint64(0)-total)/8 {
			return ^uint64(0)
		}
		total += nodes * 8
		reports := uint64(len(state.Reports) + len(state.ReportsEOD))
		if reports > (^uint64(0)-total)/4 {
			return ^uint64(0)
		}
		total += reports * 4
	}
	if len(p.dense) == 0 {
		return total
	}
	if states > (^uint64(0)-total)/denseStateBytes {
		return ^uint64(0)
	}
	return total + states*denseStateBytes
}

// AcceptStates 返回接受状态编号快照。
func (p *Program) AcceptStates() []uint32 {
	if p == nil {
		return nil
	}
	out := make([]uint32, 0)
	for _, state := range p.States {
		if state.Accept {
			out = append(out, state.ID)
		}
	}
	return out
}

// AcceptCount 返回接受状态数量。
func (p *Program) AcceptCount() int { return len(p.AcceptStates()) }

// AcceptReportIDs 收集所有接受状态关联的报告编号，升序去重。
// 报告编号在确定化阶段就已按底层图节点传播到各状态，
// 这里只做跨状态汇总，保证编译期可追溯的报告集合稳定有序。
func (p *Program) AcceptReportIDs() []uint32 {
	if p == nil {
		return nil
	}
	seen := make(map[uint32]struct{}, len(p.States))
	out := make([]uint32, 0, len(p.States))
	for _, state := range p.States {
		if !state.Accept {
			continue
		}
		for _, id := range state.Reports {
			if _, ok := seen[id]; ok {
				continue
			}
			seen[id] = struct{}{}
			out = append(out, id)
		}
		for _, id := range state.ReportsEOD {
			if _, ok := seen[id]; ok {
				continue
			}
			seen[id] = struct{}{}
			out = append(out, id)
		}
	}
	slices.Sort(out)
	return out
}

// ReportsFor 返回指定状态在当前偏移可触发的报告编号快照。
func (p *Program) ReportsFor(state uint32) []uint32 {
	if p == nil || int(state) >= len(p.States) {
		return nil
	}
	return append([]uint32(nil), p.States[state].Reports...)
}

// EODReportsFor 返回指定状态仅在数据末尾可触发的报告编号快照。
func (p *Program) EODReportsFor(state uint32) []uint32 {
	if p == nil || int(state) >= len(p.States) {
		return nil
	}
	return append([]uint32(nil), p.States[state].ReportsEOD...)
}

// HasReports 报告 DFA 程序是否存在任意位置可触发的报告。
func (p *Program) HasReports() bool {
	if p == nil {
		return false
	}
	for i := range p.States {
		if len(p.States[i].Reports) != 0 {
			return true
		}
	}
	return false
}

// HasEODReports 报告 DFA 程序是否存在仅在数据末尾触发的报告。
func (p *Program) HasEODReports() bool {
	if p == nil {
		return false
	}
	for i := range p.States {
		if len(p.States[i].ReportsEOD) != 0 {
			return true
		}
	}
	return false
}

// StartBytes 返回能从起始状态消费的字节集合快照。
func (p *Program) StartBytes() []byte {
	if p == nil || int(p.Start) >= len(p.States) {
		return nil
	}
	out := make([]byte, 0, len(p.States[p.Start].Transitions))
	for b := range p.States[p.Start].Transitions {
		out = append(out, b)
	}
	slices.Sort(out)
	return out
}

// TransitionsCopy 返回指定状态的转移表副本。
func (p *Program) TransitionsCopy(state uint32) map[byte]uint32 {
	if p == nil || int(state) >= len(p.States) {
		return nil
	}
	out := make(map[byte]uint32, len(p.States[state].Transitions))
	maps.Copy(out, p.States[state].Transitions)
	return out
}

// StateAt 返回指定状态的深拷贝快照。
func (p *Program) StateAt(id uint32) (State, bool) {
	if p == nil || int(id) >= len(p.States) {
		return State{}, false
	}
	s := p.States[id]
	s.Nodes = append([]graph.Vertex(nil), s.Nodes...)
	s.Transitions = make(map[byte]uint32, len(s.Transitions))
	maps.Copy(s.Transitions, p.States[id].Transitions)
	return s, true
}

// HasState 判断状态编号是否存在。
func (p *Program) HasState(id uint32) bool { return p != nil && int(id) < len(p.States) }

// DenseTable 返回稠密字节转移表，缺失转移使用 ^uint32(0)。
func (p *Program) DenseTable() [][256]uint32 {
	if p == nil {
		return nil
	}
	if len(p.dense) == len(p.States) {
		return append([][256]uint32(nil), p.dense...)
	}
	missing := ^uint32(0)
	out := make([][256]uint32, len(p.States))
	for i := range out {
		for b := range out[i] {
			out[i][b] = missing
		}
		for b, to := range p.States[i].Transitions {
			out[i][b] = to
		}
	}
	return out
}

// Transition 返回状态在字节 b 上的后继；状态或转移非法时返回 false。
func (p *Program) Transition(state uint32, b byte) (uint32, bool) {
	if p == nil || int(state) >= len(p.States) {
		return 0, false
	}
	if len(p.dense) == len(p.States) {
		next := p.dense[state][b]
		return next, next != ^uint32(0)
	}
	next, ok := p.States[state].Transitions[b]
	return next, ok
}

// Step 执行一次字节转移并返回新状态。
func (p *Program) Step(state uint32, b byte) (uint32, bool) { return p.Transition(state, b) }

// Run 从起始状态消费输入，返回最终状态及实际消费长度。
func (p *Program) Run(data []byte) (uint32, int, bool) {
	if p == nil || int(p.Start) >= len(p.States) {
		return 0, 0, false
	}
	state := p.Start
	for i, b := range data {
		next, ok := p.Transition(state, b)
		if !ok {
			return state, i, false
		}
		state = next
	}
	return state, len(data), true
}

// RunExact 仅在完整输入被接受时返回成功。
func (p *Program) RunExact(data []byte) (uint32, bool) {
	state, consumed, ok := p.Run(data)
	return state, ok && consumed == len(data) && p.IsAccept(state)
}

// LongestMatchAt 返回指定起点的最长接受结束偏移。
func (p *Program) LongestMatchAt(data []byte, start int) (int, bool) {
	if p == nil || start < 0 || start > len(data) {
		return 0, false
	}
	ends := p.MatchAt(data, start)
	if len(ends) == 0 {
		return 0, false
	}
	return ends[len(ends)-1], true
}

// ReachableStates 返回从起始状态沿转移可达的状态编号。
func (p *Program) ReachableStates() []uint32 {
	if p == nil || int(p.Start) >= len(p.States) {
		return nil
	}
	seen := map[uint32]struct{}{p.Start: {}}
	queue := []uint32{p.Start}
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		for _, next := range p.States[id].Transitions {
			if _, ok := seen[next]; ok {
				continue
			}
			seen[next] = struct{}{}
			queue = append(queue, next)
		}
	}
	out := make([]uint32, 0, len(seen))
	for id := range seen {
		out = append(out, id)
	}
	slices.Sort(out)
	return out
}

// IsDeadState 判断状态是否为显式不可接受死状态。
func (p *Program) IsDeadState(state uint32) bool {
	if p == nil || int(state) >= len(p.States) {
		return false
	}
	s := p.States[state]
	return len(s.Nodes) == 0 && !s.Accept
}

// RunLimit 在最多消费 limit 个字节的范围内运行。
func (p *Program) RunLimit(data []byte, limit int) (uint32, int, bool) {
	if limit < 0 {
		return 0, 0, false
	}
	if limit > len(data) || limit == 0 {
		limit = len(data)
	}
	return p.Run(data[:limit])
}

// IsAccept 判断状态是否为接受状态。
func (p *Program) IsAccept(state uint32) bool {
	return p != nil && int(state) < len(p.States) && p.States[state].Accept
}

// TableMatchAt 按字节状态表返回指定起点的全部结束偏移。
func (p *Program) TableMatchAt(data []byte, start int) []int {
	return p.TableMatchAtLimit(data, start, 0)
}

// TableMatchAtLimit 使用状态表执行匹配并限制最多读取的字节数。
func (p *Program) TableMatchAtLimit(data []byte, start int, limit int) []int {
	if p == nil || start < 0 || start > len(data) || limit < 0 {
		return nil
	}
	state := p.Start
	var out []int
	if p.IsAccept(state) {
		out = append(out, start)
	}
	for i := start; i < len(data); i++ {
		if limit > 0 && i-start >= limit {
			break
		}
		next, ok := p.Transition(state, data[i])
		if !ok {
			break
		}
		state = next
		if p.IsAccept(state) {
			out = append(out, i+1)
		}
	}
	return out
}

// MatchAt 返回指定起点的全部结束偏移，不限制上界。
func (p *Program) MatchAt(data []byte, start int) []int {
	return p.MatchAtLimit(data, start, 0)
}

// MatchAtWithOptions 执行带多行和 ASCII 大小写折叠选项的匹配。
func (p *Program) MatchAtWithOptions(data []byte, start int, multiline, caseless bool) []int {
	if p == nil || p.Program == nil {
		return nil
	}
	if p.ByteTable && caseless {
		return p.Program.MatchAtWithOptions(data, start, multiline, caseless)
	}
	if p.ByteTable && !multiline {
		return p.TableMatchAt(data, start)
	}
	return p.Program.MatchAtWithOptions(data, start, multiline, caseless)
}

// MatchAtLimit 使用状态表或通用执行器执行有界匹配。
func (p *Program) MatchAtLimit(data []byte, start int, limit int) []int {
	if p == nil {
		return nil
	}
	if p.ByteTable {
		return p.TableMatchAtLimit(data, start, limit)
	}
	if p.Program == nil {
		return nil
	}
	return p.Program.MatchAtLimit(data, start, limit)
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

// MatchLimit 返回不超过 limit 个结束偏移；零值表示不限制。
func (p *Program) MatchLimit(data []byte, limit int) []int {
	if limit < 0 {
		return nil
	}
	out := make([]int, 0)
	seen := map[int]struct{}{}
	for i := 0; i <= len(data); i++ {
		for _, end := range p.MatchAt(data, i) {
			if _, ok := seen[end]; ok {
				continue
			}
			seen[end] = struct{}{}
			out = append(out, end)
			if limit > 0 && len(out) >= limit {
				return out
			}
		}
	}
	if limit > 0 && len(out) > limit {
		return out[:limit]
	}
	return out
}

// MatchFirst 返回按起点顺序找到的首个匹配区间。
func (p *Program) MatchFirst(data []byte) (int, int, bool) {
	if p == nil {
		return 0, 0, false
	}
	for start := 0; start <= len(data); start++ {
		if ends := p.MatchAt(data, start); len(ends) > 0 {
			return start, ends[0], true
		}
	}
	return 0, 0, false
}

// Spans 返回全部匹配区间，并按起点和终点排序。
func (p *Program) Spans(data []byte) []nfa.Span {
	if p == nil {
		return nil
	}
	out := make([]nfa.Span, 0)
	seen := map[nfa.Span]struct{}{}
	for start := 0; start <= len(data); start++ {
		if p.ByteTable && !p.IsAccept(p.Start) {
			if start == len(data) {
				continue
			}
			if _, ok := p.Transition(p.Start, data[start]); !ok {
				continue
			}
		}
		for _, end := range p.MatchAt(data, start) {
			span := nfa.Span{From: start, To: end}
			if _, ok := seen[span]; ok {
				continue
			}
			seen[span] = struct{}{}
			out = append(out, span)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].From != out[j].From {
			return out[i].From < out[j].From
		}
		return out[i].To < out[j].To
	})
	return out
}

// SpansLimit 返回不超过 limit 个匹配区间；零值表示不限制。
func (p *Program) SpansLimit(data []byte, limit int) []nfa.Span {
	if limit < 0 {
		return nil
	}
	if limit == 0 {
		return p.Spans(data)
	}
	if p == nil {
		return nil
	}
	out := make([]nfa.Span, 0, limit)
	seen := make(map[nfa.Span]struct{}, limit)
	for start := 0; start <= len(data) && len(out) < limit; start++ {
		for _, end := range p.MatchAt(data, start) {
			span := nfa.Span{From: start, To: end}
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

// ScanEach 按起点、终点顺序回调匹配区间。
func (p *Program) ScanEach(data []byte, fn func(nfa.Span) bool) {
	if p == nil || fn == nil {
		return
	}
	seen := map[nfa.Span]struct{}{}
	for start := 0; start <= len(data); start++ {
		for _, end := range p.MatchAt(data, start) {
			span := nfa.Span{From: start, To: end}
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

// Clone 深拷贝程序，结果与原程序不共享状态表。
func (p *Program) Clone() *Program {
	if p == nil {
		return nil
	}
	var base *nfa.Program
	if p.Program != nil {
		base = p.Program.Clone()
	}
	out := &Program{Program: base, Start: p.Start, ByteTable: p.ByteTable, Minimized: p.Minimized, States: cloneStates(p.States)}
	out.rebuildDense()
	return out
}

func cloneStates(states []State) []State {
	out := make([]State, len(states))
	for i, s := range states {
		out[i] = State{
			ID:          s.ID,
			Nodes:       append([]graph.Vertex(nil), s.Nodes...),
			Accept:      s.Accept,
			Reports:     append([]uint32(nil), s.Reports...),
			ReportsEOD:  append([]uint32(nil), s.ReportsEOD...),
			Transitions: make(map[byte]uint32, len(s.Transitions)),
		}
		maps.Copy(out[i].Transitions, s.Transitions)
	}
	return out
}

func (p *Program) rebuildDense() {
	if p == nil || !p.ByteTable || len(p.States) == 0 {
		return
	}
	missing := ^uint32(0)
	p.dense = make([][256]uint32, len(p.States))
	for i := range p.dense {
		for b := range p.dense[i] {
			p.dense[i][b] = missing
		}
		for b, to := range p.States[i].Transitions {
			p.dense[i][b] = to
		}
	}
}

// Compile 使用默认预算确定化整张图。
func Compile(g *nfagraph.Graph) (*Program, error) {
	return CompileWithLimit(g, 0)
}

// CompileWithLimit 编译图并限制最多生成的确定性状态数量。
func CompileWithLimit(g *nfagraph.Graph, limit int) (*Program, error) {
	return CompileWithBudgets(g, limit, 0)
}

// CompileWithBudgets 在确定化过程中同时限制状态数和估算内存。
func CompileWithBudgets(g *nfagraph.Graph, stateLimit int, memoryLimit uint64) (*Program, error) {
	return CompileWithOptions(g, CompileOptions{StateLimit: stateLimit, MemoryLimit: memoryLimit})
}

// CompileWithOptions 编译图并按选项控制状态表生成。
func CompileWithOptions(g *nfagraph.Graph, options CompileOptions) (*Program, error) {
	if options.StateLimit < 0 {
		return nil, fmt.Errorf("invalid state limit")
	}
	expanded := nfagraph.ExpandLiterals(g)
	base, err := nfa.Compile(expanded)
	if err != nil {
		return nil, err
	}
	states, err := determinizeWithOptions(expanded, options)
	if err != nil {
		return nil, err
	}
	byteTable := byteTableCompatible(expanded)
	if options.Dense {
		if !byteTable {
			return nil, fmt.Errorf("dense table requires byte-compatible graph")
		}
		byteTable = true
	}
	p := &Program{Program: base, States: states, ByteTable: byteTable}
	p.rebuildDense()
	return p, nil
}

func determinizeWithOptions(g *nfagraph.Graph, options CompileOptions) ([]State, error) {
	states, err := DeterminizeWithOptions(g, options)
	if err != nil {
		return nil, err
	}
	if options.MemoryLimit > 0 {
		p := &Program{States: states, ByteTable: options.Dense || byteTableCompatible(g)}
		if p.MemoryBytes() > options.MemoryLimit {
			return nil, ErrMemoryLimit
		}
	}
	return states, nil
}

// Determinize 构造带空闭包的字节转移表；非字节结构保留在通用执行器中。
func Determinize(g *nfagraph.Graph, limit int) ([]State, error) {
	return DeterminizeWithOptions(g, CompileOptions{StateLimit: limit})
}

// DeterminizeWithOptions 构造带空闭包的字节转移表，可选加入显式死状态。
func DeterminizeWithOptions(g *nfagraph.Graph, options CompileOptions) ([]State, error) {
	limit := options.StateLimit
	if limit < 0 {
		return nil, fmt.Errorf("invalid state limit")
	}
	if g == nil {
		return nil, fmt.Errorf("nil graph")
	}
	if err := g.Validate(); err != nil {
		return nil, err
	}
	start := epsilonClosure(g, []graph.Vertex{g.Start})
	var states []State
	index := map[string]uint32{}
	byteTable := options.Dense || byteTableCompatible(g)
	eodGuarded := eodGuardedNodes(g)
	add := func(nodes []graph.Vertex) (uint32, bool, error) {
		key := stateKey(nodes)
		if id, ok := index[key]; ok {
			return id, false, nil
		}
		if limit > 0 && len(states) >= limit {
			return 0, false, ErrStateLimit
		}
		if options.MemoryLimit > 0 {
			count := uint64(len(states) + 1)
			perState := uint64(64)
			if byteTable {
				perState += 256 * 4
			}
			if count > options.MemoryLimit/perState {
				return 0, false, ErrMemoryLimit
			}
		}
		id := uint32(len(states))
		state := State{ID: id, Nodes: nodes, Transitions: map[byte]uint32{}}
		for _, v := range nodes {
			if n := g.Nodes[v]; n != nil && n.Kind == nfagraph.KindAccept {
				state.Accept = true
				break
			}
		}
		if state.Accept {
			state.Reports, state.ReportsEOD = reportIDsOf(g, nodes, eodGuarded)
		}
		states = append(states, state)
		index[key] = id
		return id, true, nil
	}
	if _, _, err := add(start); err != nil {
		return nil, err
	}
	for cursor := 0; cursor < len(states); cursor++ {
		for value := range 256 {
			next := move(g, states[cursor].Nodes, byte(value))
			if len(next) == 0 {
				continue
			}
			next = epsilonClosure(g, next)
			id, _, err := add(next)
			if err != nil {
				return nil, err
			}
			states[cursor].Transitions[byte(value)] = id
		}
	}
	if options.DeadState {
		deadID := uint32(len(states))
		if limit > 0 && len(states) >= limit {
			return nil, ErrStateLimit
		}
		dead := State{ID: deadID, Nodes: nil, Transitions: make(map[byte]uint32, 256)}
		for value := range 256 {
			dead.Transitions[byte(value)] = deadID
		}
		for i := range states {
			for value := range 256 {
				if _, ok := states[i].Transitions[byte(value)]; !ok {
					states[i].Transitions[byte(value)] = deadID
				}
			}
		}
		states = append(states, dead)
	}
	return states, nil
}

func epsilonClosure(g *nfagraph.Graph, seed []graph.Vertex) []graph.Vertex {
	seen := map[graph.Vertex]bool{}
	queue := append([]graph.Vertex(nil), seed...)
	for len(queue) > 0 {
		v := queue[0]
		queue = queue[1:]
		if seen[v] {
			continue
		}
		seen[v] = true
		n := g.Nodes[v]
		if n == nil || consuming(n) {
			continue
		}
		queue = append(queue, g.Flow.Successors(v)...)
	}
	out := make([]graph.Vertex, 0, len(seen))
	for v := range seen {
		out = append(out, v)
	}
	slices.Sort(out)
	return out
}

func move(g *nfagraph.Graph, nodes []graph.Vertex, b byte) []graph.Vertex {
	var out []graph.Vertex
	seen := map[graph.Vertex]bool{}
	for _, v := range nodes {
		n := g.Nodes[v]
		if n == nil || !matchesByte(n, b) {
			continue
		}
		for _, to := range g.Flow.Successors(v) {
			if !seen[to] {
				seen[to] = true
				out = append(out, to)
			}
		}
	}
	return out
}
func consuming(n *nfagraph.Node) bool {
	return n.Kind == nfagraph.KindLiteral || n.Kind == nfagraph.KindClass
}

// isEODAssertion 判断断言是否只在数据末尾成立。
func isEODAssertion(kind parser.AssertionKind) bool {
	switch kind {
	case parser.End, parser.EndAbsolute, parser.EndBeforeFinalNewline:
		return true
	default:
		return false
	}
}

// eodGuardedNodes 返回只能经过末尾断言到达的节点集合。
// 该集合用于把报告划分到“任意位置触发”和“仅在数据末尾触发”两类，
// 与底层图上的末尾断言保持一致。
func eodGuardedNodes(g *nfagraph.Graph) map[graph.Vertex]bool {
	if g == nil || g.Flow == nil {
		return nil
	}
	// 绝大多数图不含末尾断言，这里先做一次廉价扫描，避免为普通图分配可达集合。
	hasEOD := false
	hasReports := false
	for _, node := range g.Nodes {
		if node == nil {
			continue
		}
		if node.ReportID != 0 {
			hasReports = true
		}
		if node.Kind == nfagraph.KindAssertion && isEODAssertion(node.Assertion) {
			hasEOD = true
		}
	}
	if !hasEOD || !hasReports {
		return nil
	}
	reachable := map[graph.Vertex]bool{g.Start: true}
	queue := []graph.Vertex{g.Start}
	for len(queue) > 0 {
		vertex := queue[0]
		queue = queue[1:]
		for _, next := range g.Flow.Successors(vertex) {
			if reachable[next] {
				continue
			}
			reachable[next] = true
			queue = append(queue, next)
		}
	}
	// 不经过末尾断言可达的节点集合，从全量可达集合中排除后即为末尾受控节点。
	plain := map[graph.Vertex]bool{g.Start: true}
	queue = append(queue[:0], g.Start)
	for len(queue) > 0 {
		vertex := queue[0]
		queue = queue[1:]
		for _, next := range g.Flow.Successors(vertex) {
			if plain[next] {
				continue
			}
			if node := g.Nodes[next]; node != nil && node.Kind == nfagraph.KindAssertion && isEODAssertion(node.Assertion) {
				continue
			}
			plain[next] = true
			queue = append(queue, next)
		}
	}
	out := make(map[graph.Vertex]bool)
	for vertex := range reachable {
		if !plain[vertex] {
			out[vertex] = true
		}
	}
	return out
}

func matchesByte(n *nfagraph.Node, b byte) bool {
	switch n.Kind {
	case nfagraph.KindLiteral:
		return len(n.Literal) == 1 && n.Literal[0] == b
	case nfagraph.KindClass:
		if n.Unicode != nil {
			return false
		}
		hit := false
		for _, r := range n.Class.Ranges {
			if b >= r.Lo && b <= r.Hi {
				hit = true
				break
			}
		}
		return hit != n.Class.Negated
	default:
	}
	return false
}
func byteTableCompatible(g *nfagraph.Graph) bool {
	if g == nil || g.Flow == nil {
		return false
	}
	for _, id := range g.Flow.Vertices() {
		n := g.Nodes[id]
		if n == nil {
			continue
		}
		if n.Kind == nfagraph.KindLiteral && len(n.Literal) != 1 {
			return false
		}
		if n.Kind == nfagraph.KindClass && n.Unicode != nil {
			return false
		}
		if n.Kind == nfagraph.KindAssertion {
			return false
		}
	}
	return true
}

// ByteTableEligible 判断图是否可以完全使用字节状态表执行。
func ByteTableEligible(g *nfagraph.Graph) bool {
	return byteTableCompatible(nfagraph.ExpandLiterals(g))
}
func stateKey(nodes []graph.Vertex) string {
	var out strings.Builder
	for _, v := range nodes {
		out.WriteString(strconv.Itoa(int(v)))
		out.WriteByte(',')
	}
	return out.String()
}

// StateCount 估算确定化后的状态数量，用于预算评估。
func StateCount(g *nfagraph.Graph) int {
	if g == nil {
		return 0
	}
	return len(g.Nodes)
}

// EdgeCount 估算确定化后的转移数量，用于预算评估。
func EdgeCount(g *nfagraph.Graph) int {
	if g == nil {
		return 0
	}
	return g.EdgeCount()
}

// IsDeterministic 判断图是否已满足确定化前提。
func IsDeterministic(g *nfagraph.Graph) bool {
	if g == nil || g.Flow == nil {
		return false
	}
	for _, id := range g.Flow.Vertices() {
		n := g.Nodes[id]
		if n == nil {
			return false
		}
		if !consuming(n) && len(g.Flow.Successors(id)) > 1 {
			return false
		}
		seen := make([][2]byte, 0, len(g.Flow.Successors(id)))
		for _, to := range g.Flow.Successors(id) {
			dst := g.Nodes[to]
			if dst == nil || !consuming(dst) {
				continue
			}
			addRange := func(lo, hi byte) bool {
				for _, pair := range seen {
					if lo <= pair[1] && pair[0] <= hi {
						return false
					}
				}
				seen = append(seen, [2]byte{lo, hi})
				return true
			}
			if dst.Kind == nfagraph.KindLiteral && len(dst.Literal) == 1 {
				if !addRange(dst.Literal[0], dst.Literal[0]) {
					return false
				}
			}
			if dst.Kind == nfagraph.KindClass && dst.Unicode == nil {
				for _, r := range dst.Class.Ranges {
					if !addRange(r.Lo, r.Hi) {
						return false
					}
				}
			}
		}
	}
	return true
}

// Minimize 构造确定性状态表并合并行为等价状态。
func Minimize(g *nfagraph.Graph) (*Program, error) {
	p, err := Compile(g)
	if err != nil || !p.ByteTable || len(p.States) < 2 {
		return p, err
	}
	// 初始划分必须区分报告行为：报告集合不同的接受状态不能合并，
	// 否则最小化会丢失某个状态本应触发的报告。
	part := make([]int, len(p.States))
	initial := map[string]int{}
	for i, state := range p.States {
		key := reportPartitionKey(state)
		if id, ok := initial[key]; ok {
			part[i] = id
			continue
		}
		id := len(initial)
		initial[key] = id
		part[i] = id
	}
	for {
		groups := map[string]int{}
		next := make([]int, len(part))
		for i, state := range p.States {
			var key strings.Builder
			key.WriteString(strconv.Itoa(part[i]))
			for b := range 256 {
				to, ok := state.Transitions[byte(b)]
				if !ok {
					key.WriteString(",-")
				} else {
					key.WriteByte(',')
					key.WriteString(strconv.Itoa(part[to]))
				}
			}
			k := key.String()
			if id, ok := groups[k]; ok {
				next[i] = id
			} else {
				id := len(groups)
				groups[k] = id
				next[i] = id
			}
		}
		stable := len(groups) == maxPart(part)+1
		if stable {
			for i := range part {
				if part[i] != next[i] {
					stable = false
					break
				}
			}
		}
		if stable {
			part = next
			break
		}
		part = next
	}
	count := maxPart(part) + 1
	states := make([]State, count)
	for old, group := range part {
		states[group].ID = uint32(group)
		states[group].Accept = states[group].Accept || p.States[old].Accept
		states[group].Reports = mergeReports(states[group].Reports, p.States[old].Reports)
		states[group].ReportsEOD = mergeReports(states[group].ReportsEOD, p.States[old].ReportsEOD)
		states[group].Nodes = append(states[group].Nodes, p.States[old].Nodes...)
		for b, to := range p.States[old].Transitions {
			states[group].Transitions = ensureTransitionMap(states[group].Transitions)
			states[group].Transitions[b] = uint32(part[to])
		}
	}
	for i := range states {
		slices.Sort(states[i].Nodes)
		states[i].Nodes = compactVertices(states[i].Nodes)
		states[i].ReportsEOD = subtractReports(states[i].ReportsEOD, states[i].Reports)
	}
	out := &Program{Program: p.Program, States: states, Start: uint32(part[p.Start]), ByteTable: true, Minimized: true}
	out.rebuildDense()
	return out, nil
}

// MinimizeWithLimit 最小化并限制最终状态数量。
func MinimizeWithLimit(g *nfagraph.Graph, limit int) (*Program, error) {
	if limit < 0 {
		return nil, ErrStateLimit
	}
	p, err := Minimize(g)
	if err != nil {
		return nil, err
	}
	if limit > 0 && p.StateCount() > limit {
		return nil, ErrStateLimit
	}
	return p, nil
}

// MinimizeWithBudgets 最小化并同时限制最终状态数和压缩表内存。
func MinimizeWithBudgets(g *nfagraph.Graph, stateLimit int, memoryLimit uint64) (*Program, error) {
	if stateLimit < 0 {
		return nil, ErrStateLimit
	}
	p, err := Minimize(g)
	if err != nil {
		return nil, err
	}
	if stateLimit > 0 && p.StateCount() > stateLimit {
		return nil, ErrStateLimit
	}
	if memoryLimit > 0 && p.MemoryBytes() > memoryLimit {
		return nil, ErrMemoryLimit
	}
	if err := p.Validate(); err != nil {
		return nil, err
	}
	return p, nil
}

func compactVertices(nodes []graph.Vertex) []graph.Vertex {
	if len(nodes) < 2 {
		return nodes
	}
	write := 1
	for _, node := range nodes[1:] {
		if node == nodes[write-1] {
			continue
		}
		nodes[write] = node
		write++
	}
	return nodes[:write]
}

func maxPart(values []int) int {
	maxValue := -1
	for _, value := range values {
		if value > maxValue {
			maxValue = value
		}
	}
	return maxValue
}

// reportPartitionKey 生成最小化初始划分键，把接受标记和报告集合一起编码。
func reportPartitionKey(state State) string {
	var key strings.Builder
	if state.Accept {
		key.WriteByte('A')
	} else {
		key.WriteByte('N')
	}
	for _, id := range state.Reports {
		key.WriteByte('|')
		key.WriteString(strconv.FormatUint(uint64(id), 10))
	}
	key.WriteByte(';')
	for _, id := range state.ReportsEOD {
		key.WriteByte('|')
		key.WriteString(strconv.FormatUint(uint64(id), 10))
	}
	return key.String()
}

// mergeReports 合并两个按升序排列的报告集合，返回升序去重结果。
func mergeReports(current, add []uint32) []uint32 {
	if len(add) == 0 {
		return current
	}
	return compactReports(append(current, add...))
}

func ensureTransitionMap(in map[byte]uint32) map[byte]uint32 {
	if in == nil {
		return map[byte]uint32{}
	}
	return in
}
