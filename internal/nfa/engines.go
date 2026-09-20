package nfa

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math/bits"
	"sort"
	"sync"

	"github.com/smartwalle/scankit/internal/dispatch"
	"github.com/smartwalle/scankit/internal/graph"
	"github.com/smartwalle/scankit/internal/nfagraph"
	"github.com/smartwalle/scankit/internal/parser"
	"github.com/smartwalle/scankit/internal/repeat"
	"github.com/smartwalle/scankit/internal/simd"
)

var (
	ErrStateLimit     = fmt.Errorf("nfa state limit exceeded")
	ErrEdgeLimit      = fmt.Errorf("nfa edge limit exceeded")
	ErrMemoryLimit    = fmt.Errorf("nfa memory limit exceeded")
	ErrInvalidGraph   = fmt.Errorf("invalid nfa graph")
	ErrUnsupported    = fmt.Errorf("unsupported nfa graph")
	ErrLayout         = fmt.Errorf("invalid nfa execution layout")
	ErrExecutionLimit = fmt.Errorf("nfa execution limit exceeded")
)

const (
	limExStateLimit  = 512
	shengStateLimit  = 1024
	sparseStateLimit = 4096
	maxLayoutMemory  = uint64(1 << 24)
)

type EngineKind uint8

const (
	EngineCastle EngineKind = iota + 1
	EngineGough
	EngineLimEx
	EngineSheng
	EngineMcSheng
	EngineTamarama
	EngineVermicelli
	EngineShufti
	EngineTruffle
	EngineRepeat
	EngineMPV
	EngineLBR
)

type Engine struct {
	Kind      EngineKind
	Program   *Program
	castle    *castleProgram
	gough     *goughProgram
	repeat    *repeat.Program
	mpv       *mpvProgram
	byteNFA   *byteNFAProgram
	tableNFA  *tableNFAProgram
	sheng     *shengProgram
	bitNFA    *bitNFAProgram
	sparseNFA *sparseNFAProgram
	// vermicelli 保存带确定性前缀候选的专用执行器；sparseNFA 仅保留兼容快照。
	vermicelli *vermicelliProgram
	rangeNFA   *rangeNFAProgram
	nibbleNFA  *nibbleNFAProgram
	truffle    *truffleProgram
	lbr        *lbrProgram
}

// vermicelliProgram 将前缀候选筛选与稀疏状态确认拆成独立运行时。
// 前缀不满足时直接返回，满足后才进入状态布局，避免短输入反复遍历状态集合。
type vermicelliProgram struct {
	core   *sparseNFAProgram
	prefix []byte
}

// truffleProgram 使用高半字节优先的转置掩码，在进入通用闭包执行前完成首字节筛选。
type truffleProgram struct {
	core      *nibbleNFAProgram
	firstMask [4]uint64
	// highSource 按高半字节预分组源状态，减少候选字节下的状态扫描。
	highSource [16][]uint64
	startMask  []uint64
}

func newTruffleProgram(core *nibbleNFAProgram) *truffleProgram {
	if core == nil {
		return nil
	}
	p := &truffleProgram{core: core, firstMask: core.firstMask}
	words := (len(core.states) + 63) / 64
	p.startMask = make([]uint64, words)
	for _, state := range core.closures[core.start] {
		p.startMask[state/64] |= 1 << uint(state%64)
	}
	for high := range p.highSource {
		p.highSource[high] = make([]uint64, words)
	}
	for value := 0; value < 256; value++ {
		high := value >> 4
		for i, word := range core.sourceMask[value] {
			p.highSource[high][i] |= word
		}
	}
	return p
}

// shengProgram 为稠密状态表提供独立调度入口，按输入字节批量读取预展开闭包。
type shengProgram struct{ core *tableNFAProgram }

func (p *shengProgram) validate() bool {
	return p != nil && p.core != nil && tableRuntimeShapeOK(p.core)
}
func (p *shengProgram) MatchAtBudget(data []byte, start, maxSteps, maxResults int) ([]int, int, bool) {
	if !p.validate() {
		return nil, 0, false
	}
	return p.core.MatchAtBudget(data, start, maxSteps, maxResults)
}

func (p *truffleProgram) validate() bool {
	if p == nil || p.core == nil || p.core.mode != 1 || !nibbleRuntimeShapeOK(p.core) || p.firstMask != p.core.firstMask {
		return false
	}
	for high := 0; high < 16; high++ {
		if len(p.highSource[high]) != (len(p.core.states)+63)/64 {
			return false
		}
	}
	return len(p.startMask) == (len(p.core.states)+63)/64
}

func (p *truffleProgram) MatchAtBudget(data []byte, start, maxSteps, maxResults int) ([]int, int, bool) {
	if !p.validate() || start < 0 || start > len(data) {
		return nil, 0, false
	}
	if start < len(data) && !p.core.acceptsEmpty && !byteMaskEmpty(p.firstMask) && !hasFirstByte(p.firstMask, data[start]) {
		return nil, 0, false
	}
	if start < len(data) && !p.core.acceptsEmpty && !bitIntersects(p.startMask, p.highSource[data[start]>>4]) {
		return nil, 0, false
	}
	return p.core.MatchAtBudget(data, start, maxSteps, maxResults)
}

func (p *vermicelliProgram) validate() bool {
	return p != nil && p.core != nil && sparseRuntimeShapeOK(p.core) && len(p.prefix) > 0 && bytes.Equal(p.prefix, p.core.prefix)
}

func (p *vermicelliProgram) MatchAtBudget(data []byte, start, maxSteps, maxResults int) ([]int, int, bool) {
	if !p.validate() || start < 0 || start > len(data) || !prefixMatchesAt(data, start, p.prefix) {
		return nil, 0, false
	}
	return p.core.MatchAtBudget(data, start, maxSteps, maxResults)
}

func (p *vermicelliProgram) MatchAt(data []byte, start int) []int {
	ends, _, _ := p.MatchAtBudget(data, start, 0, 0)
	return ends
}

func repeatUsable(e *Engine) bool {
	return e != nil && e.repeat != nil && e.Program != nil && graphHasRepeat(e.Program.Graph)
}

// nibbleNFAProgram 使用高低半字节交叉表表达字节类转移，减少稠密字节表的访问。
// 每个低半字节记录允许的高半字节集合，匹配时只需一次掩码判断。
type nibbleNFAProgram struct {
	states       []nibbleNFAState
	start        int
	closures     [][]int
	nextClosure  [][]int
	masks        [][16]uint16
	accept       []bool
	dead         []bool
	sourceMask   [256][]uint64
	firstMask    [4]uint64
	acceptsEmpty bool
	minBytes     int
	maxBytes     int
	// mode 为 0 时按低半字节索引，为 1 时按高半字节索引。
	mode    uint8
	prefix  []byte
	backend simd.Backend
}

// nibbleNFAState 保存半字节掩码后端的独立节点数据。
type nibbleNFAState struct {
	kind  nfagraph.NodeKind
	lit   byte
	class *nfagraph.Node
	next  []int
	eps   []int
}

// nfaWork 保存表格类后端的一次匹配所需临时缓冲区。工作区只在调用期间
// 借出，归还池后不会携带任何匹配结果，因此可安全用于并发扫描。
type nfaWork struct {
	marks      []uint32
	active     []int
	next       []int
	activeBits []uint64
	nextBits   []uint64
}

var nfaWorkPool sync.Pool

// boundedBackendEnd 计算后端在有限最大长度下允许访问的结束位置，
// 使用差值比较避免 start+maxBytes 溢出。
func boundedBackendEnd(data []byte, start, maxBytes int) int {
	if start < 0 || start > len(data) || maxBytes < 0 || maxBytes > len(data)-start {
		return len(data)
	}
	return start + maxBytes
}

func acquireNFAWork(states int) *nfaWork {
	w, _ := nfaWorkPool.Get().(*nfaWork)
	if w == nil {
		w = &nfaWork{}
	}
	if cap(w.marks) < states {
		w.marks = make([]uint32, states)
	} else {
		w.marks = w.marks[:states]
		clear(w.marks)
	}
	if cap(w.active) < states {
		w.active = make([]int, 0, states)
	} else {
		w.active = w.active[:0]
	}
	if cap(w.next) < states {
		w.next = make([]int, 0, states)
	} else {
		w.next = w.next[:0]
	}
	return w
}

// nfaWorkBits 仅在稠密状态集路径按需准备位图，避免普通匹配额外分配内存。
func nfaWorkBits(w *nfaWork, states int) ([]uint64, []uint64) {
	if w == nil || states <= 0 {
		return nil, nil
	}
	words := (states + 63) / 64
	if cap(w.activeBits) < words {
		w.activeBits = make([]uint64, words)
	} else {
		w.activeBits = w.activeBits[:words]
		clear(w.activeBits)
	}
	if cap(w.nextBits) < words {
		w.nextBits = make([]uint64, words)
	} else {
		w.nextBits = w.nextBits[:words]
		clear(w.nextBits)
	}
	return w.activeBits, w.nextBits
}

func releaseNFAWork(w *nfaWork) {
	if w == nil {
		return
	}
	w.active = w.active[:0]
	w.next = w.next[:0]
	w.activeBits = w.activeBits[:0]
	w.nextBits = w.nextBits[:0]
	if cap(w.marks) > 1<<16 || cap(w.active) > 1<<16 || cap(w.next) > 1<<16 || cap(w.activeBits) > 1<<12 || cap(w.nextBits) > 1<<12 {
		w.marks, w.active, w.next, w.activeBits, w.nextBits = nil, nil, nil, nil, nil
	}
	nfaWorkPool.Put(w)
}

func compileNibbleNFA(g *nfagraph.Graph) *nibbleNFAProgram {
	return compileNibbleNFAWithMode(g, 0)
}

func compileNibbleNFAWithMode(g *nfagraph.Graph, mode uint8) *nibbleNFAProgram {
	if g == nil || mode > 1 || g.Validate() != nil {
		return nil
	}
	g = nfagraph.ExpandLiterals(g)
	if !dfaLikeGraph(g) || len(g.Nodes) > sparseStateLimit || g.Flow == nil || g.Flow.EdgeCount() > 16384 {
		return nil
	}
	// 半字节掩码和闭包表的总预算限制在 16MiB 内，超限时交给通用路径。
	if uint64(len(g.Nodes))*32+uint64(g.Flow.EdgeCount())*16 > maxLayoutMemory {
		return nil
	}
	ids := g.NodeIDs()
	index := make(map[graph.Vertex]int, len(ids))
	for i, id := range ids {
		index[id] = i
	}
	states := make([]nibbleNFAState, len(ids))
	for i, id := range ids {
		n := g.Nodes[id]
		st := nibbleNFAState{kind: n.Kind, class: n}
		if n.Kind == nfagraph.KindLiteral {
			if len(n.Literal) != 1 {
				return nil
			}
			st.lit = n.Literal[0]
		}
		for _, to := range g.Flow.Successors(id) {
			j, ok := index[to]
			if !ok {
				return nil
			}
			if n.Kind == nfagraph.KindLiteral || n.Kind == nfagraph.KindClass {
				st.next = append(st.next, j)
			} else {
				st.eps = append(st.eps, j)
			}
		}
		st.next = normalizeStateEdges(st.next)
		st.eps = normalizeStateEdges(st.eps)
		states[i] = st
	}
	closures := make([][]int, len(states))
	for i := range states {
		closures[i] = nibbleClosure(states, i)
	}
	closureBytes := uint64(0)
	for _, closure := range closures {
		closureBytes = saturatingAdd(closureBytes, saturatingMul(uint64(len(closure)), 4))
	}
	estimateWords := uint64((len(states) + 63) / 64)
	sourceBytes := 256 * estimateWords * 8
	if closureBytes+sourceBytes+saturatingMul(uint64(len(states)), 16) > maxLayoutMemory {
		return nil
	}
	start := index[g.Start]
	p := &nibbleNFAProgram{states: states, start: start, closures: closures, nextClosure: make([][]int, len(states)), masks: make([][16]uint16, len(states)), accept: make([]bool, len(states)), dead: make([]bool, len(states)), firstMask: firstByteMask(g), mode: mode, prefix: requiredLiteralPrefix(g), backend: dispatch.DefaultBackend()}
	words := (len(states) + 63) / 64
	for value := range p.sourceMask {
		p.sourceMask[value] = make([]uint64, words)
	}
	p.minBytes, p.maxBytes = graphLengthBounds(g)
	for i, state := range states {
		p.accept[i] = state.kind == nfagraph.KindAccept
		p.nextClosure[i] = mergeClosureTargets(closures, state.next)
		if state.kind != nfagraph.KindLiteral && state.kind != nfagraph.KindClass {
			continue
		}
		var reach nfagraph.CharReach
		if state.kind == nfagraph.KindClass {
			reach = nfagraph.CharReachFromClass(state.class.Class)
		}
		for value := 0; value < 256; value++ {
			matched := (state.kind == nfagraph.KindLiteral && state.lit == byte(value) || state.kind == nfagraph.KindClass && reach.Contains(byte(value))) && len(state.next) > 0
			if matched {
				p.sourceMask[value][i/64] |= 1 << uint(i%64)
				low, high := byte(value)&0x0f, byte(value)>>4
				if mode == 1 {
					p.masks[i][high] |= 1 << low
				} else {
					p.masks[i][low] |= 1 << high
				}
			}
		}
	}
	p.dead = computeNibbleDead(states)
	for _, id := range p.closures[p.start] {
		if p.accept[id] {
			p.acceptsEmpty = true
			break
		}
	}
	if err := p.validate(); err != nil {
		return nil
	}
	return p
}

// computeNibbleDead 计算无法到达接受节点的状态，避免在半字节热路径上保留无效分支。
func computeNibbleDead(states []nibbleNFAState) []bool {
	reverse := make([][]int, len(states))
	for i, st := range states {
		for _, to := range append(append([]int(nil), st.next...), st.eps...) {
			if to >= 0 && to < len(states) {
				reverse[to] = append(reverse[to], i)
			}
		}
	}
	reachable := make([]bool, len(states))
	queue := make([]int, 0, len(states))
	for i, st := range states {
		if st.kind == nfagraph.KindAccept {
			reachable[i] = true
			queue = append(queue, i)
		}
	}
	for head := 0; head < len(queue); head++ {
		for _, from := range reverse[queue[head]] {
			if !reachable[from] {
				reachable[from] = true
				queue = append(queue, from)
			}
		}
	}
	dead := make([]bool, len(states))
	for i := range dead {
		dead[i] = !reachable[i]
	}
	return dead
}

func nibbleClosure(states []nibbleNFAState, start int) []int {
	if start < 0 || start >= len(states) {
		return nil
	}
	seen := make([]bool, len(states))
	queue := []int{start}
	seen[start] = true
	for head := 0; head < len(queue); head++ {
		id := queue[head]
		st := states[id]
		if st.kind == nfagraph.KindLiteral || st.kind == nfagraph.KindClass {
			continue
		}
		for _, next := range st.eps {
			if next >= 0 && next < len(states) && !seen[next] {
				seen[next] = true
				queue = append(queue, next)
			}
		}
	}
	sort.Ints(queue)
	return queue
}

func normalizeStateEdges(values []int) []int {
	if len(values) < 2 {
		return values
	}
	sort.Ints(values)
	return dedupInts(values)
}

func (p *nibbleNFAProgram) validate() error {
	if p == nil || p.start < 0 || p.start >= len(p.states) || len(p.closures) != len(p.states) || len(p.nextClosure) != len(p.states) || len(p.masks) != len(p.states) || len(p.accept) != len(p.states) || len(p.dead) != len(p.states) {
		return fmt.Errorf("invalid nibble nfa layout")
	}
	if p.minBytes < 0 || p.maxBytes < -1 || p.maxBytes >= 0 && p.maxBytes < p.minBytes {
		return fmt.Errorf("nibble length bounds are invalid")
	}
	if p.mode > 1 {
		return fmt.Errorf("invalid nibble mask mode")
	}
	if len(p.prefix) > len(p.states) {
		return fmt.Errorf("nibble prefix exceeds state count")
	}
	for i, state := range p.states {
		if p.dead[i] && p.accept[i] {
			return fmt.Errorf("nibble accept state marked dead")
		}
		if state.kind == nfagraph.KindClass && state.class == nil {
			return fmt.Errorf("nibble class state missing payload")
		}
		if (state.kind == nfagraph.KindLiteral || state.kind == nfagraph.KindClass) && len(state.eps) != 0 || state.kind != nfagraph.KindLiteral && state.kind != nfagraph.KindClass && len(state.next) != 0 {
			return fmt.Errorf("nibble state transition kind mismatch")
		}
		for _, target := range state.next {
			if target < 0 || target >= len(p.states) {
				return fmt.Errorf("nibble transition target out of bounds")
			}
		}
		wantClosure := mergeClosureTargets(p.closures, state.next)
		if !equalInts(p.nextClosure[i], wantClosure) {
			return fmt.Errorf("nibble transition closure mismatch")
		}
		for j := 1; j < len(state.next); j++ {
			if state.next[j-1] >= state.next[j] {
				return fmt.Errorf("nibble state transitions are not strictly ordered")
			}
		}
		for j := 1; j < len(state.eps); j++ {
			if state.eps[j-1] >= state.eps[j] {
				return fmt.Errorf("nibble epsilon transitions are not strictly ordered")
			}
		}
		for _, target := range append(append([]int(nil), state.next...), state.eps...) {
			if target < 0 || target >= len(p.states) {
				return fmt.Errorf("nibble state transition target out of bounds")
			}
		}
		for _, target := range p.closures[i] {
			if target < 0 || target >= len(p.states) {
				return fmt.Errorf("nibble closure target out of bounds")
			}
		}
		for j := 1; j < len(p.closures[i]); j++ {
			if p.closures[i][j-1] >= p.closures[i][j] {
				return fmt.Errorf("nibble closure is not strictly ordered")
			}
		}
		if !containsInt(p.closures[i], i) {
			return fmt.Errorf("nibble closure misses source state")
		}
		for value := 0; value < 256; value++ {
			expected := (state.kind == nfagraph.KindLiteral && state.lit == byte(value) || state.kind == nfagraph.KindClass && castleMatches(state.class, byte(value))) && len(state.next) > 0
			index, bit := byte(value)&0x0f, byte(value)>>4
			if p.mode == 1 {
				index, bit = bit, index
			}
			actual := p.masks[i][index]&(1<<bit) != 0
			if actual != expected {
				return fmt.Errorf("nibble mask mismatch at state %d", i)
			}
		}
	}
	words := (len(p.states) + 63) / 64
	for value := range p.sourceMask {
		if len(p.sourceMask[value]) != words {
			return fmt.Errorf("nibble source mask width mismatch")
		}
		for i, state := range p.states {
			want := (state.kind == nfagraph.KindLiteral && state.lit == byte(value) || state.kind == nfagraph.KindClass && castleMatches(state.class, byte(value))) && len(state.next) > 0
			got := p.sourceMask[value][i/64]&(1<<uint(i%64)) != 0
			if want != got {
				return fmt.Errorf("nibble source mask mismatch")
			}
		}
	}
	if !equalBools(p.dead, computeNibbleDead(p.states)) {
		return fmt.Errorf("nibble dead-state map mismatch")
	}
	if p.firstMask != firstMaskFromNibble(p) || p.acceptsEmpty != nibbleClosureAccepts(p) {
		return fmt.Errorf("nibble start candidate metadata mismatch")
	}
	return nil
}

func nibbleClosureAccepts(p *nibbleNFAProgram) bool {
	if p == nil || p.start < 0 || p.start >= len(p.closures) {
		return false
	}
	for _, id := range p.closures[p.start] {
		if id >= 0 && id < len(p.accept) && p.accept[id] {
			return true
		}
	}
	return false
}

func firstMaskFromNibble(p *nibbleNFAProgram) [4]uint64 {
	var mask [4]uint64
	if p == nil || p.start < 0 || p.start >= len(p.closures) {
		return mask
	}
	for _, id := range p.closures[p.start] {
		if id < 0 || id >= len(p.states) {
			continue
		}
		for b := 0; b < 256; b++ {
			if len(p.next(id, byte(b))) > 0 {
				mask[b/64] |= 1 << uint(b%64)
			}
		}
	}
	return mask
}

func equalBools(a, b []bool) bool {
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

func equalUint64s(a, b []uint64) bool {
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

// filterDeadStates 在闭包展开后移除无法到达接受态的状态，减少后续预算消耗。
func filterDeadStates(active []int, dead []bool) []int {
	if len(active) == 0 || len(dead) == 0 {
		return active
	}
	out := active[:0]
	for _, id := range active {
		if id >= 0 && id < len(dead) && !dead[id] {
			out = append(out, id)
		}
	}
	return out
}

func (p *nibbleNFAProgram) StateCount() int {
	if p == nil {
		return 0
	}
	return len(p.states)
}

func (p *nibbleNFAProgram) EdgeCount() int {
	if p == nil {
		return 0
	}
	n := 0
	for _, state := range p.states {
		n += len(state.eps) + len(state.next)
	}
	return n
}

func (p *nibbleNFAProgram) closureSet(seed []int) []int {
	if p == nil || len(seed) == 0 {
		return nil
	}
	seen := make([]bool, len(p.states))
	out := make([]int, 0, len(seed))
	for _, id := range seed {
		if id < 0 || id >= len(p.closures) {
			continue
		}
		for _, item := range p.closures[id] {
			if !seen[item] {
				seen[item] = true
				out = append(out, item)
			}
		}
	}
	return out
}

func (p *nibbleNFAProgram) next(id int, value byte) []int {
	if p == nil || id < 0 || id >= len(p.states) {
		return nil
	}
	index, bit := value&0x0f, value>>4
	if p.mode == 1 {
		index, bit = bit, index
	}
	if p.masks[id][index]&(1<<bit) == 0 {
		return nil
	}
	return p.nextClosure[id]
}

func (p *nibbleNFAProgram) MatchAt(data []byte, start int) []int {
	ends, _, _ := p.MatchAtBudget(data, start, 0, 0)
	return ends
}

func (p *nibbleNFAProgram) MatchAtBudget(data []byte, start, maxSteps, maxResults int) ([]int, int, bool) {
	return p.MatchAtBudgetInto(data, start, maxSteps, maxResults, nil)
}

func (p *nibbleNFAProgram) MatchAtBudgetInto(data []byte, start, maxSteps, maxResults int, dst []int) ([]int, int, bool) {
	if p == nil || start < 0 || start > len(data) || maxSteps < 0 || maxResults < 0 || !nibbleRuntimeShapeOK(p) {
		return dst[:0], 0, false
	}
	return p.matchAtBudgetUnchecked(data, start, maxSteps, maxResults, dst)
}

// matchAtBudgetUnchecked 在调用方已完成布局校验后执行一次半字节匹配。
// 整块扫描会复用同一不可变布局，跳过每个起点重复的 256×状态校验。
func (p *nibbleNFAProgram) matchAtBudgetUnchecked(data []byte, start, maxSteps, maxResults int, dst []int) ([]int, int, bool) {
	if len(p.prefix) > 0 && !prefixMatchesAt(data, start, p.prefix) {
		return nil, 0, false
	}
	if len(p.prefix) == 0 && start < len(data) && !p.acceptsEmpty && !byteMaskEmpty(p.firstMask) && !hasFirstByte(p.firstMask, data[start]) {
		return nil, 0, false
	}
	if p.minBytes > 0 && len(data)-start < p.minBytes {
		return nil, 0, false
	}
	work := acquireNFAWork(len(p.states))
	defer releaseNFAWork(work)
	marks := work.marks
	generation := uint32(0)
	active := collectClosuresWithWork(p.closures, []int{p.start}, marks, &generation, work.active)
	active = filterDeadStates(active, p.dead)
	activeBits, nextBits := nfaWorkBits(work, len(p.states))
	for _, id := range active {
		if id >= 0 && id/64 < len(activeBits) {
			activeBits[id/64] |= 1 << uint(id%64)
		}
	}
	ends := dst[:0]
	next := work.next
	steps := len(active)
	for _, id := range active {
		if p.dead[id] || !p.accept[id] {
			continue
		}
		var ok bool
		ends, ok = appendNFAEnd(ends, start, maxResults)
		if !ok {
			return nfaBudgetEnds(ends, maxResults), steps, true
		}
	}
	if maxResults > 0 && len(ends) >= maxResults {
		return ends, steps, true
	}
	if nfaBudgetExceeded(steps, maxSteps) {
		return nil, steps, true
	}
	end := boundedBackendEnd(data, start, p.maxBytes)
	for pos := start; pos < end && len(active) > 0; pos++ {
		generation++
		if generation == 0 {
			clear(marks)
			generation = 1
		}
		next = next[:0]
		clear(nextBits)
		value := data[pos]
		sources := p.sourceMask[value]
		if !bitIntersects(activeBits, sources) {
			break
		}
		consumable := false
		if len(active) >= len(p.states)/4 {
			for wi, word := range activeBits {
				if wi >= len(sources) {
					break
				}
				word &= sources[wi]
				for word != 0 {
					bit := bits.TrailingZeros64(word)
					id := wi*64 + bit
					if id < len(p.states) {
						consumable = true
						steps++
						if nfaBudgetExceeded(steps, maxSteps) {
							return nil, steps, true
						}
						for _, to := range p.next(id, value) {
							if marks[to] != generation {
								marks[to] = generation
								next = append(next, to)
								nextBits[to/64] |= 1 << uint(to%64)
							}
						}
					}
					word &= word - 1
				}
			}
		} else {
			for _, id := range active {
				if p.dead[id] {
					continue
				}
				index, bit := value&0x0f, value>>4
				if p.mode == 1 {
					index, bit = bit, index
				}
				if p.masks[id][index]&(1<<uint(bit)) == 0 {
					continue
				}
				consumable = true
				steps++
				if nfaBudgetExceeded(steps, maxSteps) {
					return nil, steps, true
				}
				for _, to := range p.next(id, value) {
					if marks[to] != generation {
						marks[to] = generation
						next = append(next, to)
						nextBits[to/64] |= 1 << uint(to%64)
					}
				}
			}
		}
		if !consumable {
			break
		}
		active, next = next, active
		activeBits, nextBits = nextBits, activeBits
		work.next, work.active = next, active
		work.nextBits, work.activeBits = nextBits, activeBits
		active = filterDeadStates(active, p.dead)
		clear(activeBits)
		for _, id := range active {
			if id >= 0 && id/64 < len(activeBits) {
				activeBits[id/64] |= 1 << uint(id%64)
			}
		}
		steps += len(active)
		if nfaBudgetExceeded(steps, maxSteps) {
			return nil, steps, true
		}
		for _, id := range active {
			if p.dead[id] {
				continue
			}
			var ok bool
			if p.accept[id] {
				ends, ok = appendNFAEnd(ends, pos+1, maxResults)
			}
			if p.accept[id] && !ok {
				return ends, steps, true
			}
			if maxResults > 0 && len(ends) >= maxResults {
				return ends, steps, true
			}
		}
	}
	sort.Ints(ends)
	return normalizeNFAEnds(ends, maxResults), steps, false
}

func nibbleRuntimeShapeOK(p *nibbleNFAProgram) bool {
	if p == nil || p.start < 0 || p.start >= len(p.states) || len(p.states) != len(p.closures) || len(p.states) != len(p.nextClosure) || len(p.states) != len(p.masks) || len(p.states) != len(p.accept) || len(p.states) != len(p.dead) || p.mode > 1 {
		return false
	}
	if p.minBytes < 0 || p.maxBytes < -1 || p.maxBytes >= 0 && p.maxBytes < p.minBytes {
		return false
	}
	words := (len(p.states) + 63) / 64
	for value := range p.sourceMask {
		if len(p.sourceMask[value]) != words {
			return false
		}
		for i, state := range p.states {
			want := (state.kind == nfagraph.KindLiteral && state.lit == byte(value) || state.kind == nfagraph.KindClass && castleMatches(state.class, byte(value))) && len(state.next) > 0
			if (p.sourceMask[value][i/64]&(1<<uint(i%64)) != 0) != want {
				return false
			}
		}
	}
	for i, st := range p.states {
		if p.dead[i] && p.accept[i] {
			return false
		}
		for _, id := range st.next {
			if id < 0 || id >= len(p.states) {
				return false
			}
		}
		for _, id := range st.eps {
			if id < 0 || id >= len(p.states) {
				return false
			}
		}
		for _, id := range p.nextClosure[i] {
			if id < 0 || id >= len(p.states) {
				return false
			}
		}
		if len(p.closures[i]) == 0 {
			return false
		}
		for _, id := range p.closures[i] {
			if id < 0 || id >= len(p.states) {
				return false
			}
		}
	}
	return p.firstMask == firstMaskFromNibble(p) && p.acceptsEmpty == nibbleClosureAccepts(p)
}

func (p *nibbleNFAProgram) Spans(data []byte, limit int) []Span {
	if p == nil || limit < 0 || !nibbleRuntimeShapeOK(p) {
		return nil
	}
	out := make([]Span, 0, initialSpanCapacity(data, limit))
	endsBuf := make([]int, 0, 8)
	forEachNFAStart(data, p.prefix, p.firstMask, p.acceptsEmpty, p.backend, func(start int) bool {
		ends, _, _ := p.matchAtBudgetUnchecked(data, start, 0, 0, endsBuf[:0])
		endsBuf = ends
		for _, end := range ends {
			out = append(out, Span{From: start, To: end})
			if limit > 0 && len(out) >= limit {
				return false
			}
		}
		return true
	})
	return out
}

// rangeNFAProgram 以连续字节范围压缩转移，适合字符类密集的执行路径。
type rangeNFAProgram struct {
	states            []rangeNFAState
	start             int
	closures          [][]int
	trans             [][]rangeTransition
	transitionClosure [][][]int
	nextClosure       [][]int
	masks             [][4]uint64
	bucket            [][256]int16
	accept            []bool
	dead              []bool
	firstMask         [4]uint64
	acceptsEmpty      bool
	minBytes          int
	maxBytes          int
	prefix            []byte
	backend           simd.Backend
}

// rangeNFAState 仅保留范围后端需要的节点信息。
type rangeNFAState struct {
	kind  nfagraph.NodeKind
	lit   byte
	class *nfagraph.Node
	next  []int
	eps   []int
}

type rangeTransition struct {
	lo, hi byte
	next   []int
}

func (p *rangeNFAProgram) validate() error {
	if p == nil || p.start < 0 || p.start >= len(p.states) || len(p.trans) != len(p.states) || len(p.transitionClosure) != len(p.states) || len(p.closures) != len(p.states) || len(p.nextClosure) != len(p.states) || len(p.masks) != len(p.states) || len(p.bucket) != len(p.states) || len(p.accept) != len(p.states) || len(p.dead) != len(p.states) {
		return fmt.Errorf("invalid range nfa layout")
	}
	if len(p.prefix) > len(p.states) {
		return fmt.Errorf("range prefix exceeds state count")
	}
	if p.minBytes < 0 || p.maxBytes < -1 || p.maxBytes >= 0 && p.maxBytes < p.minBytes {
		return fmt.Errorf("range length bounds are invalid")
	}
	for i, row := range p.trans {
		for value, bucket := range p.bucket[i] {
			if bucket < -1 || int(bucket) >= len(row) {
				return fmt.Errorf("range bucket index out of bounds")
			}
			if bucket >= 0 {
				tr := row[bucket]
				if byte(value) < tr.lo || byte(value) > tr.hi {
					return fmt.Errorf("range bucket points outside transition")
				}
			}
		}
		if len(p.transitionClosure[i]) != len(row) {
			return fmt.Errorf("range transition closure width mismatch")
		}
		state := p.states[i]
		if p.dead[i] && p.accept[i] {
			return fmt.Errorf("range accept state marked dead")
		}
		if state.kind == nfagraph.KindClass && state.class == nil {
			return fmt.Errorf("range class state missing payload")
		}
		if (state.kind == nfagraph.KindLiteral || state.kind == nfagraph.KindClass) && len(state.eps) != 0 || state.kind != nfagraph.KindLiteral && state.kind != nfagraph.KindClass && len(state.next) != 0 {
			return fmt.Errorf("range state transition kind mismatch")
		}
		if state.kind != nfagraph.KindLiteral && state.kind != nfagraph.KindClass && len(row) != 0 {
			return fmt.Errorf("range epsilon state has byte transitions")
		}
		for _, id := range p.closures[i] {
			if id < 0 || id >= len(p.states) {
				return fmt.Errorf("range closure target out of bounds")
			}
		}
		for j := 1; j < len(p.closures[i]); j++ {
			if p.closures[i][j-1] >= p.closures[i][j] {
				return fmt.Errorf("range closure is not strictly ordered")
			}
		}
		if !containsInt(p.closures[i], i) {
			return fmt.Errorf("range closure misses source state")
		}
		for j := 1; j < len(state.next); j++ {
			if state.next[j-1] >= state.next[j] {
				return fmt.Errorf("range state transitions are not strictly ordered")
			}
		}
		wantClosure := mergeClosureTargets(p.closures, state.next)
		if !equalInts(p.nextClosure[i], wantClosure) {
			return fmt.Errorf("range transition closure mismatch")
		}
		for j := 1; j < len(state.eps); j++ {
			if state.eps[j-1] >= state.eps[j] {
				return fmt.Errorf("range epsilon transitions are not strictly ordered")
			}
		}
		last := -1
		for _, tr := range row {
			if tr.lo > tr.hi {
				return fmt.Errorf("range transition bounds invalid")
			}
			if tr.lo > tr.hi || int(tr.lo) <= last || len(tr.next) == 0 {
				return fmt.Errorf("invalid range transition at state %d", i)
			}
			last = int(tr.hi)
			for _, to := range tr.next {
				if to < 0 || to >= len(p.states) {
					return fmt.Errorf("range transition target out of bounds")
				}
			}
			for j := 1; j < len(tr.next); j++ {
				if tr.next[j-1] >= tr.next[j] {
					return fmt.Errorf("range transition targets are not strictly ordered")
				}
			}
			if !equalInts(tr.next, state.next) {
				return fmt.Errorf("range transition targets disagree with state layout")
			}
		}
		for j, tr := range row {
			want := mergeClosureTargets(p.closures, tr.next)
			if !equalInts(p.transitionClosure[i][j], want) {
				return fmt.Errorf("range transition closure mismatch at state %d", i)
			}
		}
		for j := 1; j < len(row); j++ {
			if row[j-1].hi >= row[j].lo {
				return fmt.Errorf("range transitions overlap or are unsorted")
			}
		}
		for value := 0; value < 256; value++ {
			expected := false
			if state.kind == nfagraph.KindLiteral {
				expected = state.lit == byte(value) && len(state.next) > 0
			} else if state.kind == nfagraph.KindClass {
				expected = castleMatches(state.class, byte(value)) && len(state.next) > 0
			}
			rowExpected := false
			for _, tr := range row {
				if byte(value) >= tr.lo && byte(value) <= tr.hi {
					rowExpected = true
					break
				}
			}
			if rowExpected != expected {
				return fmt.Errorf("range transition mismatch at state %d", i)
			}
			actual := p.masks[i][value/64]&(1<<uint(value%64)) != 0
			if actual != expected {
				return fmt.Errorf("range mask mismatch at state %d", i)
			}
			bucketIndex := p.bucket[i][value]
			if expected {
				if bucketIndex < 0 || int(bucketIndex) >= len(row) || byte(value) < row[bucketIndex].lo || byte(value) > row[bucketIndex].hi {
					return fmt.Errorf("range bucket mismatch at state %d", i)
				}
			} else if bucketIndex != -1 {
				return fmt.Errorf("range empty bucket mismatch at state %d", i)
			}
		}
	}
	if !equalBools(p.dead, computeRangeDead(p.states)) {
		return fmt.Errorf("range dead-state map mismatch")
	}
	if p.firstMask != firstMaskFromRange(p) || p.acceptsEmpty != rangeClosureAccepts(p) {
		return fmt.Errorf("range start candidate metadata mismatch")
	}
	return nil
}

func rangeClosureAccepts(p *rangeNFAProgram) bool {
	if p == nil || p.start < 0 || p.start >= len(p.closures) {
		return false
	}
	for _, id := range p.closures[p.start] {
		if id >= 0 && id < len(p.accept) && p.accept[id] {
			return true
		}
	}
	return false
}

func firstMaskFromRange(p *rangeNFAProgram) [4]uint64 {
	var mask [4]uint64
	if p == nil || p.start < 0 || p.start >= len(p.closures) {
		return mask
	}
	for _, id := range p.closures[p.start] {
		if id < 0 || id >= len(p.trans) {
			continue
		}
		for _, tr := range p.trans[id] {
			for b := tr.lo; ; b++ {
				mask[b/64] |= 1 << uint(b%64)
				if b == tr.hi {
					break
				}
			}
		}
	}
	return mask
}

func compileRangeNFA(g *nfagraph.Graph) *rangeNFAProgram {
	if g == nil || g.Validate() != nil {
		return nil
	}
	g = nfagraph.ExpandLiterals(g)
	if !dfaLikeGraph(g) || len(g.Nodes) > sparseStateLimit || g.Flow == nil || g.Flow.EdgeCount() > 16384 {
		return nil
	}
	// 范围索引在构建阶段需要 256 字节分类槽位，预估超出预算则安全降级。
	if uint64(len(g.Nodes))*256*10+uint64(g.Flow.EdgeCount())*16 > maxLayoutMemory {
		return nil
	}
	ids := g.NodeIDs()
	index := make(map[graph.Vertex]int, len(ids))
	for i, id := range ids {
		index[id] = i
	}
	states := make([]rangeNFAState, len(ids))
	for i, id := range ids {
		n := g.Nodes[id]
		st := rangeNFAState{kind: n.Kind, class: n}
		if n.Kind == nfagraph.KindLiteral {
			if len(n.Literal) != 1 {
				return nil
			}
			st.lit = n.Literal[0]
		}
		for _, to := range g.Flow.Successors(id) {
			j, ok := index[to]
			if !ok {
				return nil
			}
			if n.Kind == nfagraph.KindLiteral || n.Kind == nfagraph.KindClass {
				st.next = append(st.next, j)
			} else {
				st.eps = append(st.eps, j)
			}
		}
		st.next = normalizeStateEdges(st.next)
		st.eps = normalizeStateEdges(st.eps)
		states[i] = st
	}
	closures := make([][]int, len(states))
	for i := range states {
		closures[i] = rangeClosure(states, i)
	}
	closureBytes := uint64(0)
	for _, closure := range closures {
		closureBytes = saturatingAdd(closureBytes, saturatingMul(uint64(len(closure)), 4))
	}
	// 范围桶之外的闭包与转移确认同样计入布局预算，超限时交由通用确认器处理。
	if closureBytes+saturatingMul(uint64(len(states)), 256*2+32) > maxLayoutMemory {
		return nil
	}
	start := index[g.Start]
	p := &rangeNFAProgram{states: states, start: start, closures: closures, trans: make([][]rangeTransition, len(states)), transitionClosure: make([][][]int, len(states)), nextClosure: make([][]int, len(states)), masks: make([][4]uint64, len(states)), bucket: make([][256]int16, len(states)), accept: make([]bool, len(states)), dead: make([]bool, len(states)), firstMask: firstByteMask(g), prefix: requiredLiteralPrefix(g), backend: dispatch.DefaultBackend()}
	p.minBytes, p.maxBytes = graphLengthBounds(g)
	for i := range p.bucket {
		for value := range p.bucket[i] {
			p.bucket[i][value] = -1
		}
	}
	for i, state := range states {
		p.accept[i] = state.kind == nfagraph.KindAccept
		p.nextClosure[i] = mergeClosureTargets(closures, state.next)
		values := make([][]int, 256)
		if state.kind == nfagraph.KindLiteral {
			values[state.lit] = state.next
			if len(state.next) > 0 {
				p.masks[i][state.lit/64] |= 1 << uint(state.lit%64)
			}
		} else if state.kind == nfagraph.KindClass {
			reach := nfagraph.CharReachFromClass(state.class.Class)
			for b := 0; b < 256; b++ {
				if reach.Contains(byte(b)) {
					values[b] = state.next
					if len(state.next) > 0 {
						p.masks[i][b/64] |= 1 << uint(b%64)
					}
				}
			}
		}
		for b := 0; b < len(values); {
			if len(values[b]) == 0 {
				b++
				continue
			}
			end := b + 1
			for end < len(values) && equalInts(values[b], values[end]) {
				end++
			}
			next := append([]int(nil), values[b]...)
			sort.Ints(next)
			next = dedupInts(next)
			p.trans[i] = append(p.trans[i], rangeTransition{lo: byte(b), hi: byte(end - 1), next: next})
			b = end
		}
		for index, tr := range p.trans[i] {
			if p.transitionClosure[i] == nil {
				p.transitionClosure[i] = make([][]int, len(p.trans[i]))
			}
			p.transitionClosure[i][index] = mergeClosureTargets(p.closures, tr.next)
			for value := tr.lo; value <= tr.hi; value++ {
				p.bucket[i][value] = int16(index)
				if value == 255 {
					break
				}
			}
		}
	}
	p.dead = computeRangeDead(states)
	for _, id := range p.closures[p.start] {
		if p.accept[id] {
			p.acceptsEmpty = true
			break
		}
	}
	return p
}

// computeRangeDead 标记不能到达接受节点的范围状态，减少无效状态传播。
func computeRangeDead(states []rangeNFAState) []bool {
	reverse := make([][]int, len(states))
	for i, st := range states {
		for _, to := range append(append([]int(nil), st.next...), st.eps...) {
			if to >= 0 && to < len(states) {
				reverse[to] = append(reverse[to], i)
			}
		}
	}
	reachable := make([]bool, len(states))
	queue := make([]int, 0, len(states))
	for i, st := range states {
		if st.kind == nfagraph.KindAccept {
			reachable[i] = true
			queue = append(queue, i)
		}
	}
	for head := 0; head < len(queue); head++ {
		for _, from := range reverse[queue[head]] {
			if !reachable[from] {
				reachable[from] = true
				queue = append(queue, from)
			}
		}
	}
	dead := make([]bool, len(states))
	for i := range dead {
		dead[i] = !reachable[i]
	}
	return dead
}

func rangeClosure(states []rangeNFAState, start int) []int {
	if start < 0 || start >= len(states) {
		return nil
	}
	seen := make([]bool, len(states))
	queue := []int{start}
	seen[start] = true
	for head := 0; head < len(queue); head++ {
		id := queue[head]
		st := states[id]
		if st.kind == nfagraph.KindLiteral || st.kind == nfagraph.KindClass {
			continue
		}
		for _, next := range st.eps {
			if next >= 0 && next < len(states) && !seen[next] {
				seen[next] = true
				queue = append(queue, next)
			}
		}
	}
	sort.Ints(queue)
	return queue
}

func equalInts(a, b []int) bool {
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

func (p *rangeNFAProgram) StateCount() int {
	if p == nil {
		return 0
	}
	return len(p.states)
}
func (p *rangeNFAProgram) EdgeCount() int {
	if p == nil {
		return 0
	}
	n := 0
	for _, state := range p.states {
		n += len(state.eps)
	}
	for _, transitions := range p.trans {
		for _, transition := range transitions {
			n += len(transition.next)
		}
	}
	return n
}
func (p *rangeNFAProgram) closureSet(seed []int) []int {
	if p == nil || len(seed) == 0 {
		return nil
	}
	seen := make([]bool, len(p.states))
	out := make([]int, 0, len(seed))
	for _, id := range seed {
		if id >= 0 && id < len(p.closures) {
			for _, item := range p.closures[id] {
				if !seen[item] {
					seen[item] = true
					out = append(out, item)
				}
			}
		}
	}
	return out
}
func (p *rangeNFAProgram) next(id int, value byte) []int {
	if p == nil || id < 0 || id >= len(p.trans) {
		return nil
	}
	row := p.trans[id]
	index := int(p.bucket[id][value])
	if index < 0 || index >= len(row) {
		return nil
	}
	if value >= row[index].lo && value <= row[index].hi {
		return p.transitionClosure[id][index]
	}
	return nil
}
func (p *rangeNFAProgram) MatchAt(data []byte, start int) []int {
	ends, _, _ := p.MatchAtBudget(data, start, 0, 0)
	return ends
}
func (p *rangeNFAProgram) MatchAtBudget(data []byte, start, maxSteps, maxResults int) ([]int, int, bool) {
	return p.MatchAtBudgetInto(data, start, maxSteps, maxResults, nil)
}

func (p *rangeNFAProgram) MatchAtBudgetInto(data []byte, start, maxSteps, maxResults int, dst []int) ([]int, int, bool) {
	if p == nil || start < 0 || start > len(data) || maxSteps < 0 || maxResults < 0 || !rangeRuntimeShapeOK(p) {
		return dst[:0], 0, false
	}
	return p.matchAtBudgetUnchecked(data, start, maxSteps, maxResults, dst)
}

func (p *rangeNFAProgram) matchAtBudgetUnchecked(data []byte, start, maxSteps, maxResults int, dst []int) ([]int, int, bool) {
	if len(p.prefix) > 0 && !prefixMatchesAt(data, start, p.prefix) {
		return nil, 0, false
	}
	if len(p.prefix) == 0 && start < len(data) && !p.acceptsEmpty && !byteMaskEmpty(p.firstMask) && !hasFirstByte(p.firstMask, data[start]) {
		return nil, 0, false
	}
	if p.minBytes > 0 && len(data)-start < p.minBytes {
		return nil, 0, false
	}
	work := acquireNFAWork(len(p.states))
	defer releaseNFAWork(work)
	marks := work.marks
	generation := uint32(0)
	active := collectClosuresWithWork(p.closures, []int{p.start}, marks, &generation, work.active)
	active = filterDeadStates(active, p.dead)
	ends := dst[:0]
	next := work.next
	steps := len(active)
	for _, id := range active {
		if p.dead[id] || !p.accept[id] {
			continue
		}
		var ok bool
		ends, ok = appendNFAEnd(ends, start, maxResults)
		if !ok {
			return ends, steps, true
		}
	}
	if maxResults > 0 && len(ends) >= maxResults {
		return ends, steps, true
	}
	if nfaBudgetExceeded(steps, maxSteps) {
		return nil, steps, true
	}
	end := boundedBackendEnd(data, start, p.maxBytes)
	for pos := start; pos < end && len(active) > 0; pos++ {
		generation++
		if generation == 0 {
			clear(marks)
			generation = 1
		}
		next = next[:0]
		value := data[pos]
		consumable := false
		for _, id := range active {
			if p.dead[id] {
				continue
			}
			if p.masks[id][value/64]&(1<<uint(value%64)) == 0 {
				continue
			}
			consumable = true
			steps++
			if nfaBudgetExceeded(steps, maxSteps) {
				return nil, steps, true
			}
			for _, to := range p.next(id, value) {
				if marks[to] != generation {
					marks[to] = generation
					next = append(next, to)
				}
			}
		}
		if !consumable {
			break
		}
		active, next = next, active
		work.next, work.active = next, active
		active = filterDeadStates(active, p.dead)
		steps += len(active)
		if nfaBudgetExceeded(steps, maxSteps) {
			return nil, steps, true
		}
		for _, id := range active {
			if p.dead[id] {
				continue
			}
			var ok bool
			if p.accept[id] {
				ends, ok = appendNFAEnd(ends, pos+1, maxResults)
			}
			if p.accept[id] && !ok {
				return ends, steps, true
			}
			if maxResults > 0 && len(ends) >= maxResults {
				return ends, steps, true
			}
		}
	}
	sort.Ints(ends)
	return normalizeNFAEnds(ends, maxResults), steps, false
}

func rangeRuntimeShapeOK(p *rangeNFAProgram) bool {
	if p == nil || p.start < 0 || p.start >= len(p.states) || len(p.states) != len(p.closures) || len(p.states) != len(p.trans) || len(p.states) != len(p.nextClosure) || len(p.states) != len(p.masks) || len(p.states) != len(p.bucket) || len(p.states) != len(p.accept) || len(p.states) != len(p.dead) {
		return false
	}
	if p.minBytes < 0 || p.maxBytes < -1 || p.maxBytes >= 0 && p.maxBytes < p.minBytes {
		return false
	}
	for i, st := range p.states {
		if p.dead[i] && p.accept[i] {
			return false
		}
		if len(p.bucket[i]) != 256 || len(p.closures[i]) == 0 {
			return false
		}
		for _, id := range st.next {
			if id < 0 || id >= len(p.states) {
				return false
			}
		}
		for _, id := range st.eps {
			if id < 0 || id >= len(p.states) {
				return false
			}
		}
		for _, id := range p.closures[i] {
			if id < 0 || id >= len(p.states) {
				return false
			}
		}
		for _, id := range p.nextClosure[i] {
			if id < 0 || id >= len(p.states) {
				return false
			}
		}
		for _, tr := range p.trans[i] {
			if len(tr.next) == 0 {
				return false
			}
			for _, id := range tr.next {
				if id < 0 || id >= len(p.states) {
					return false
				}
			}
		}
		if len(p.transitionClosure[i]) != len(p.trans[i]) {
			return false
		}
		for j, targets := range p.transitionClosure[i] {
			for k, id := range targets {
				if id < 0 || id >= len(p.states) || k > 0 && targets[k-1] >= id {
					return false
				}
			}
			if !equalInts(targets, mergeClosureTargets(p.closures, p.trans[i][j].next)) {
				return false
			}
		}
	}
	if p.firstMask != firstMaskFromRange(p) || p.acceptsEmpty != rangeClosureAccepts(p) {
		return false
	}
	return true
}
func (p *rangeNFAProgram) Spans(data []byte, limit int) []Span {
	if p == nil || limit < 0 || !rangeRuntimeShapeOK(p) {
		return nil
	}
	out := make([]Span, 0, initialSpanCapacity(data, limit))
	endsBuf := make([]int, 0, 8)
	forEachNFAStart(data, p.prefix, p.firstMask, p.acceptsEmpty, p.backend, func(start int) bool {
		ends, _, _ := p.matchAtBudgetUnchecked(data, start, 0, 0, endsBuf[:0])
		endsBuf = ends
		for _, end := range ends {
			out = append(out, Span{From: start, To: end})
			if limit > 0 && len(out) >= limit {
				return false
			}
		}
		return true
	})
	return out
}

// sparseNFAProgram 保存文字稀疏转移与字符类谓词，适合分支较少的专用引擎。
type sparseNFAProgram struct {
	states         []sparseNFAState
	start          int
	closures       [][]int
	trans          []map[byte][]int
	accept         []bool
	classMask      [][4]uint64
	literalClosure [][]int
	classClosure   [][]int
	dead           []bool
	sourceMask     [256][]uint64
	sourceCount    [256]uint16
	firstMask      [4]uint64
	acceptsEmpty   bool
	minBytes       int
	maxBytes       int
	// prefix 是可证明所有路径都必须经过的起始文字，用于快速排除候选输入。
	prefix []byte
	// layoutKind 区分普通稀疏布局与带确定性前缀候选的布局。
	layoutKind uint8
	// candidateStates 保存前缀每一层的可达状态集合。
	candidateStates [][]int
	backend         simd.Backend
}

// sparseNFAState 保存稀疏后端的独立节点布局，避免执行时依赖通用字节状态。
type sparseNFAState struct {
	kind  nfagraph.NodeKind
	lit   byte
	class *nfagraph.Node
	eps   []int
	next  []int
}

func (p *sparseNFAProgram) validate() error {
	if p == nil || p.start < 0 || p.start >= len(p.states) || len(p.trans) != len(p.states) || len(p.closures) != len(p.states) || len(p.accept) != len(p.states) || len(p.classMask) != len(p.states) || len(p.literalClosure) != len(p.states) || len(p.classClosure) != len(p.states) || len(p.dead) != len(p.states) {
		return fmt.Errorf("invalid sparse nfa layout")
	}
	if p.minBytes < 0 || p.maxBytes < -1 || p.maxBytes >= 0 && p.maxBytes < p.minBytes {
		return fmt.Errorf("sparse length bounds are invalid")
	}
	if p.layoutKind > 1 || p.layoutKind == 1 && len(p.prefix) == 0 {
		return fmt.Errorf("sparse candidate layout is invalid")
	}
	for i, row := range p.trans {
		state := p.states[i]
		if state.kind == nfagraph.KindClass && state.class == nil {
			return fmt.Errorf("sparse class state missing payload")
		}
		if (state.kind == nfagraph.KindLiteral || state.kind == nfagraph.KindClass) && len(state.eps) != 0 || state.kind != nfagraph.KindLiteral && state.kind != nfagraph.KindClass && len(state.next) != 0 {
			return fmt.Errorf("sparse state transition kind mismatch")
		}
		if state.kind != nfagraph.KindLiteral && state.kind != nfagraph.KindClass && len(row) != 0 {
			return fmt.Errorf("sparse epsilon state has byte transitions")
		}
		if state.kind == nfagraph.KindLiteral {
			for value := range row {
				if value != state.lit {
					return fmt.Errorf("sparse literal transition mismatch")
				}
			}
		} else if state.kind == nfagraph.KindClass {
			if len(row) != 0 {
				return fmt.Errorf("sparse class transition must use predicate")
			}
		} else if len(row) != 0 {
			return fmt.Errorf("sparse epsilon state has byte transitions")
		}
		if p.dead[i] && p.accept[i] {
			return fmt.Errorf("sparse accept state marked dead")
		}
		for _, id := range p.closures[i] {
			if id < 0 || id >= len(p.states) {
				return fmt.Errorf("sparse closure target out of bounds")
			}
		}
		for j := 1; j < len(p.closures[i]); j++ {
			if p.closures[i][j-1] >= p.closures[i][j] {
				return fmt.Errorf("sparse closure is not strictly ordered")
			}
		}
		if !containsInt(p.closures[i], i) {
			return fmt.Errorf("sparse closure misses source state")
		}
		for j := 1; j < len(state.next); j++ {
			if state.next[j-1] >= state.next[j] {
				return fmt.Errorf("sparse state transitions are not strictly ordered")
			}
		}
		for j := 1; j < len(state.eps); j++ {
			if state.eps[j-1] >= state.eps[j] {
				return fmt.Errorf("sparse epsilon transitions are not strictly ordered")
			}
		}
		for _, to := range append(append([]int(nil), state.next...), state.eps...) {
			if to < 0 || to >= len(p.states) {
				return fmt.Errorf("sparse state transition target out of bounds")
			}
		}
		for _, next := range row {
			last := -1
			for _, to := range next {
				if to < 0 || to >= len(p.states) {
					return fmt.Errorf("sparse transition target out of bounds")
				}
				if to <= last {
					return fmt.Errorf("sparse transition targets are not strictly ordered")
				}
				last = to
			}
		}
		for value, next := range row {
			if state.kind != nfagraph.KindLiteral || value != state.lit || !equalInts(next, state.next) {
				return fmt.Errorf("sparse literal map disagrees with state transition")
			}
		}
		if state.kind == nfagraph.KindLiteral {
			want := mergeClosureTargets(p.closures, state.next)
			if !equalInts(p.literalClosure[i], want) {
				return fmt.Errorf("sparse literal closure mismatch")
			}
		} else if state.kind == nfagraph.KindClass {
			want := mergeClosureTargets(p.closures, state.next)
			if !equalInts(p.classClosure[i], want) {
				return fmt.Errorf("sparse class closure mismatch")
			}
		} else if len(p.classClosure[i]) != 0 {
			return fmt.Errorf("sparse non-class closure present")
		}
		for value := 0; value < 256; value++ {
			expected := state.kind == nfagraph.KindClass && castleMatches(state.class, byte(value)) && len(state.next) > 0
			actual := p.classMask[i][value/64]&(1<<uint(value%64)) != 0
			if actual != expected {
				return fmt.Errorf("sparse class mask mismatch at state %d", i)
			}
		}
	}
	for value := range p.sourceMask {
		if len(p.sourceMask[value]) != (len(p.states)+63)/64 {
			return fmt.Errorf("sparse source mask width mismatch")
		}
		count := 0
		for _, word := range p.sourceMask[value] {
			count += bits.OnesCount64(word)
		}
		if int(p.sourceCount[value]) != count {
			return fmt.Errorf("sparse source count mismatch for byte %d", value)
		}
	}
	if !equalBools(p.dead, computeSparseDead(p.states)) {
		return fmt.Errorf("sparse dead-state map mismatch")
	}
	if p.firstMask != firstMaskFromSparse(p) {
		return fmt.Errorf("sparse start candidate mask mismatch")
	}
	wantEmpty := false
	for _, id := range p.closures[p.start] {
		if id >= 0 && id < len(p.accept) && p.accept[id] {
			wantEmpty = true
			break
		}
	}
	if p.acceptsEmpty != wantEmpty {
		return fmt.Errorf("sparse empty-match metadata mismatch")
	}
	if len(p.prefix) > 0 {
		current := append([]int(nil), p.closures[p.start]...)
		for _, want := range p.prefix {
			next := make([]int, 0)
			seen := make(map[int]struct{})
			for _, id := range current {
				if id < 0 || id >= len(p.states) {
					return fmt.Errorf("sparse prefix state out of bounds")
				}
				st := p.states[id]
				if st.kind != nfagraph.KindLiteral || st.lit != want {
					continue
				}
				for _, target := range p.literalClosure[id] {
					if _, ok := seen[target]; !ok {
						seen[target] = struct{}{}
						next = append(next, target)
					}
				}
			}
			if len(next) == 0 {
				return fmt.Errorf("sparse prefix disagrees with state layout")
			}
			current = next
		}
		if p.layoutKind == 1 && len(p.candidateStates) != len(p.prefix) {
			return fmt.Errorf("sparse candidate state depth mismatch")
		}
		if p.layoutKind == 1 {
			for _, states := range p.candidateStates {
				if len(states) == 0 {
					return fmt.Errorf("sparse candidate state set is empty")
				}
				for j := 1; j < len(states); j++ {
					if states[j-1] >= states[j] {
						return fmt.Errorf("sparse candidate states are not ordered")
					}
				}
				for _, id := range states {
					if id < 0 || id >= len(p.states) {
						return fmt.Errorf("sparse candidate state out of bounds")
					}
				}
			}
		}
	}
	return nil
}

func compileSparseNFA(g *nfagraph.Graph) *sparseNFAProgram {
	if g == nil || g.Validate() != nil {
		return nil
	}
	g = nfagraph.ExpandLiterals(g)
	if !dfaLikeGraph(g) || len(g.Nodes) > sparseStateLimit || g.Flow == nil || g.Flow.EdgeCount() > 16384 {
		return nil
	}
	// 稀疏布局只保存实际文字映射和字符类谓词，超过 16MiB 时避免继续分配。
	if uint64(len(g.Nodes))*72+uint64(g.Flow.EdgeCount())*16 > maxLayoutMemory {
		return nil
	}
	ids := g.NodeIDs()
	index := make(map[graph.Vertex]int, len(ids))
	for i, id := range ids {
		index[id] = i
	}
	states := make([]sparseNFAState, len(ids))
	for i, id := range ids {
		n := g.Nodes[id]
		st := sparseNFAState{kind: n.Kind}
		if n.Kind == nfagraph.KindLiteral {
			if len(n.Literal) != 1 {
				return nil
			}
			st.lit = n.Literal[0]
		} else if n.Kind == nfagraph.KindClass {
			st.class = n
		}
		for _, to := range g.Flow.Successors(id) {
			j, ok := index[to]
			if !ok {
				return nil
			}
			if n.Kind == nfagraph.KindLiteral || n.Kind == nfagraph.KindClass {
				st.next = append(st.next, j)
			} else {
				st.eps = append(st.eps, j)
			}
		}
		st.next = normalizeStateEdges(st.next)
		st.eps = normalizeStateEdges(st.eps)
		states[i] = st
	}
	closures := make([][]int, len(states))
	for i := range states {
		closures[i] = sparseClosure(states, i)
	}
	closureBytes := uint64(0)
	for _, closure := range closures {
		closureBytes = saturatingAdd(closureBytes, saturatingMul(uint64(len(closure)), 4))
	}
	// 闭包和类掩码属于稀疏布局的固定运行时开销，纳入同一内存门禁，
	// 防止复杂分支在构建后才被迫降级。
	if closureBytes+saturatingMul(uint64(len(states)), 32) > maxLayoutMemory {
		return nil
	}
	p := &sparseNFAProgram{states: states, start: index[g.Start], closures: closures, trans: make([]map[byte][]int, len(states)), accept: make([]bool, len(states)), classMask: make([][4]uint64, len(states)), literalClosure: make([][]int, len(states)), classClosure: make([][]int, len(states)), dead: make([]bool, len(states)), firstMask: firstByteMask(g), backend: dispatch.DefaultBackend()}
	words := (len(states) + 63) / 64
	for value := range p.sourceMask {
		p.sourceMask[value] = make([]uint64, words)
	}
	p.minBytes, p.maxBytes = graphLengthBounds(g)
	for i, st := range states {
		p.accept[i] = st.kind == nfagraph.KindAccept
		m := make(map[byte][]int)
		if st.kind == nfagraph.KindLiteral {
			m[st.lit] = append([]int(nil), st.next...)
			if len(st.next) > 0 {
				p.sourceMask[st.lit][i/64] |= 1 << uint(i%64)
			}
			if len(st.next) > 0 {
				p.literalClosure[i] = mergeClosureTargets(p.closures, st.next)
			}
		} else if st.kind == nfagraph.KindClass && len(st.next) > 0 {
			p.classClosure[i] = mergeClosureTargets(p.closures, st.next)
			for value := 0; value < 256; value++ {
				if castleMatches(st.class, byte(value)) {
					p.sourceMask[value][i/64] |= 1 << uint(i%64)
					p.classMask[i][value/64] |= 1 << uint(value%64)
				}
			}
		}
		p.trans[i] = m
	}
	for value := range p.sourceMask {
		count := 0
		for _, word := range p.sourceMask[value] {
			count += bits.OnesCount64(word)
		}
		p.sourceCount[value] = uint16(count)
	}
	p.dead = computeSparseDead(states)
	for _, id := range closures[p.start] {
		if p.accept[id] {
			p.acceptsEmpty = true
			break
		}
	}
	return p
}

// computeSparseDead 标记无法沿任意路径到达接受状态的节点，执行时可安全剪枝。
func computeSparseDead(states []sparseNFAState) []bool {
	reverse := make([][]int, len(states))
	for i, st := range states {
		for _, to := range append(append([]int(nil), st.next...), st.eps...) {
			if to >= 0 && to < len(states) {
				reverse[to] = append(reverse[to], i)
			}
		}
	}
	reachable := make([]bool, len(states))
	queue := make([]int, 0, len(states))
	for i, st := range states {
		if st.kind == nfagraph.KindAccept {
			reachable[i] = true
			queue = append(queue, i)
		}
	}
	for head := 0; head < len(queue); head++ {
		for _, from := range reverse[queue[head]] {
			if !reachable[from] {
				reachable[from] = true
				queue = append(queue, from)
			}
		}
	}
	dead := make([]bool, len(states))
	for i := range dead {
		dead[i] = !reachable[i]
	}
	return dead
}

func mergeClosureTargets(closures [][]int, targets []int) []int {
	seen := make(map[int]struct{})
	for _, target := range targets {
		if target < 0 || target >= len(closures) {
			continue
		}
		for _, id := range closures[target] {
			seen[id] = struct{}{}
		}
	}
	out := make([]int, 0, len(seen))
	for id := range seen {
		out = append(out, id)
	}
	sort.Ints(out)
	return out
}

// compileVermicelliNFA 在稀疏状态表上附加确定性文字前缀，
// 使该后端能够先进行无歧义的候选过滤，再进入状态机确认。
func compileVermicelliNFA(g *nfagraph.Graph) *sparseNFAProgram {
	p := compileSparseNFA(g)
	if p == nil {
		return nil
	}
	p.prefix = requiredLiteralPrefix(g)
	if len(p.prefix) == 0 {
		return p
	}
	p.layoutKind = 1
	current := append([]int(nil), p.closures[p.start]...)
	p.candidateStates = make([][]int, 0, len(p.prefix))
	for _, want := range p.prefix {
		nextSet := make(map[int]struct{})
		for _, id := range current {
			if id < 0 || id >= len(p.states) {
				continue
			}
			st := p.states[id]
			if st.kind != nfagraph.KindLiteral || st.lit != want {
				continue
			}
			for _, target := range p.literalClosure[id] {
				nextSet[target] = struct{}{}
			}
		}
		next := make([]int, 0, len(nextSet))
		for id := range nextSet {
			next = append(next, id)
		}
		sort.Ints(next)
		if len(next) == 0 {
			return nil
		}
		p.candidateStates = append(p.candidateStates, next)
		current = next
	}
	return p
}

func requiredLiteralPrefix(g *nfagraph.Graph) []byte {
	if g == nil || g.Flow == nil {
		return nil
	}
	if len(g.Nodes) > 65536 {
		return nil
	}
	g = nfagraph.ExpandLiterals(g)
	if !dfaLikeGraph(g) {
		return nil
	}
	// 可通过纯 epsilon 路径到达接受态时，任何文字前缀都会误杀空匹配。
	if graphAcceptsEmpty(g) {
		return nil
	}
	frontier := []graph.Vertex{g.Start}
	prefix := make([]byte, 0, 8)
	visited := make(map[graph.Vertex]struct{}, 16)
	for depth := 0; depth < 64 && len(frontier) > 0; depth++ {
		closure := epsilonClosureVertices(g, frontier)
		if len(closure) == 0 {
			return prefix
		}
		// 一旦前沿重新进入已经处理过的节点，说明存在循环或路径汇合。
		// 循环只能证明当前前缀，继续展开会把可变长度重复错误地当成固定前缀。
		for _, id := range closure {
			if _, ok := visited[id]; ok {
				return prefix
			}
		}
		for _, id := range closure {
			visited[id] = struct{}{}
		}
		var next []graph.Vertex
		var value byte
		for _, id := range closure {
			n := g.Nodes[id]
			if n == nil || n.Kind == nfagraph.KindAccept {
				return prefix
			}
			if n.Kind != nfagraph.KindLiteral || len(n.Literal) != 1 {
				return prefix
			}
			if len(next) == 0 {
				value = n.Literal[0]
			} else if n.Literal[0] != value {
				return prefix
			}
			next = append(next, g.Flow.Successors(id)...)
		}
		if len(next) == 0 {
			return prefix
		}
		prefix = append(prefix, value)
		frontier = dedupVerticesSorted(next)
	}
	return prefix
}

// firstByteMask 返回从起点出发第一步可消费的字节集合，用于扫描阶段候选过滤。
func firstByteMask(g *nfagraph.Graph) [4]uint64 {
	var mask [4]uint64
	if g == nil || g.Flow == nil {
		return mask
	}
	// Unicode 字符的 UTF-8 首字节取决于完整码点，无法压缩成
	// 单字节字符集合；返回全量掩码表示不做候选过滤。
	if graphHasUnicode(g) {
		for i := range mask {
			mask[i] = ^uint64(0)
		}
		return mask
	}
	// 超大图不为候选掩码付出 O(|V|×256) 的构建成本；全 1 掩码
	// 表示“不做过滤”，保持正确性并避免编译阶段资源尖峰。
	if len(g.Nodes) > 65536 {
		for i := range mask {
			mask[i] = ^uint64(0)
		}
		return mask
	}
	for _, id := range epsilonClosureVertices(g, []graph.Vertex{g.Start}) {
		n := g.Nodes[id]
		if n == nil {
			continue
		}
		if n.Kind == nfagraph.KindLiteral && len(n.Literal) > 0 && len(g.Flow.Successors(id)) > 0 {
			b := n.Literal[0]
			mask[b/64] |= 1 << uint(b%64)
			continue
		}
		if n.Kind == nfagraph.KindClass && n.Unicode == nil && len(g.Flow.Successors(id)) > 0 {
			reach := nfagraph.CharReachFromClass(n.Class)
			for b := 0; b < 256; b++ {
				if reach.Contains(byte(b)) {
					mask[b/64] |= 1 << uint(b%64)
				}
			}
		}
	}
	return mask
}

func hasFirstByte(mask [4]uint64, b byte) bool {
	return mask[b/64]&(1<<uint(b%64)) != 0
}

// byteMaskEmpty 判断首字节掩码是否没有任何可消费字节。
// 空掩码并不总是表示“永远不匹配”：带断言的布局可能无法静态推导
// 首字节，此时必须关闭过滤并交给执行器确认。
func byteMaskEmpty(mask [4]uint64) bool {
	return mask[0] == 0 && mask[1] == 0 && mask[2] == 0 && mask[3] == 0
}

func epsilonClosureVertices(g *nfagraph.Graph, seed []graph.Vertex) []graph.Vertex {
	seen := make(map[graph.Vertex]struct{}, len(seed))
	queue := append([]graph.Vertex(nil), seed...)
	out := make([]graph.Vertex, 0, len(seed))
	for head := 0; head < len(queue); head++ {
		id := queue[head]
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		n := g.Nodes[id]
		if n == nil {
			continue
		}
		if n.Kind == nfagraph.KindLiteral || n.Kind == nfagraph.KindClass || n.Kind == nfagraph.KindAccept {
			out = append(out, id)
			continue
		}
		queue = append(queue, g.Flow.Successors(id)...)
	}
	return dedupVerticesSorted(out)
}

func dedupVerticesSorted(values []graph.Vertex) []graph.Vertex {
	if len(values) < 2 {
		return values
	}
	sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })
	return dedupVertices(values)
}

func graphAcceptsEmpty(g *nfagraph.Graph) bool {
	if g == nil || g.Flow == nil {
		return false
	}
	seen := map[graph.Vertex]struct{}{g.Start: {}}
	queue := []graph.Vertex{g.Start}
	for head := 0; head < len(queue); head++ {
		id := queue[head]
		n := g.Nodes[id]
		if n != nil && n.Kind == nfagraph.KindAccept {
			return true
		}
		if n == nil || n.Kind == nfagraph.KindLiteral || n.Kind == nfagraph.KindClass {
			continue
		}
		for _, to := range g.Flow.Successors(id) {
			if _, ok := seen[to]; ok {
				continue
			}
			seen[to] = struct{}{}
			queue = append(queue, to)
		}
	}
	return false
}

func sparseClosure(states []sparseNFAState, start int) []int {
	if start < 0 || start >= len(states) {
		return nil
	}
	seen := make([]bool, len(states))
	queue := []int{start}
	seen[start] = true
	for head := 0; head < len(queue); head++ {
		id := queue[head]
		state := states[id]
		if state.kind == nfagraph.KindLiteral || state.kind == nfagraph.KindClass {
			continue
		}
		for _, next := range state.eps {
			if next >= 0 && next < len(states) && !seen[next] {
				seen[next] = true
				queue = append(queue, next)
			}
		}
	}
	sort.Ints(queue)
	return queue
}

func (p *sparseNFAProgram) StateCount() int {
	if p == nil {
		return 0
	}
	return len(p.states)
}
func (p *sparseNFAProgram) EdgeCount() int {
	if p == nil {
		return 0
	}
	n := 0
	for _, st := range p.states {
		n += len(st.eps) + len(st.next)
	}
	return n
}
func (p *sparseNFAProgram) MatchAt(data []byte, start int) []int {
	ends, _, _ := p.MatchAtBudget(data, start, 0, 0)
	return ends
}
func (p *sparseNFAProgram) closureSet(seed []int) []int {
	if len(seed) == 0 {
		return nil
	}
	seen := make([]bool, len(p.states))
	out := []int{}
	for _, id := range seed {
		if id < 0 || id >= len(p.closures) {
			continue
		}
		for _, v := range p.closures[id] {
			if !seen[v] {
				seen[v] = true
				out = append(out, v)
			}
		}
	}
	return out
}
func (p *sparseNFAProgram) Spans(data []byte, limit int) []Span {
	if p == nil || limit < 0 || !sparseRuntimeShapeOK(p) {
		return nil
	}
	out := make([]Span, 0, initialSpanCapacity(data, limit))
	endsBuf := make([]int, 0, 8)
	forEachNFAStart(data, p.prefix, p.firstMask, p.acceptsEmpty, p.backend, func(start int) bool {
		ends, _, _ := p.matchAtBudgetUnchecked(data, start, 0, 0, endsBuf[:0])
		endsBuf = ends
		for _, end := range ends {
			out = append(out, Span{start, end})
			if limit > 0 && len(out) >= limit {
				return false
			}
		}
		return true
	})
	return out
}

// MatchAtBudget 在步骤和结果预算内执行稀疏状态机匹配。
func (p *sparseNFAProgram) MatchAtBudget(data []byte, start, maxSteps, maxResults int) ([]int, int, bool) {
	return p.MatchAtBudgetInto(data, start, maxSteps, maxResults, nil)
}

func (p *sparseNFAProgram) MatchAtBudgetInto(data []byte, start, maxSteps, maxResults int, dst []int) ([]int, int, bool) {
	if p == nil || start < 0 || start > len(data) || maxSteps < 0 || maxResults < 0 || !sparseRuntimeShapeOK(p) {
		return dst[:0], 0, false
	}
	return p.matchAtBudgetUnchecked(data, start, maxSteps, maxResults, dst)
}

func (p *sparseNFAProgram) matchAtBudgetUnchecked(data []byte, start, maxSteps, maxResults int, dst []int) ([]int, int, bool) {
	if len(p.prefix) > 0 && !prefixMatchesAt(data, start, p.prefix) {
		return nil, 0, false
	}
	if len(p.prefix) == 0 && start < len(data) && !p.acceptsEmpty && !byteMaskEmpty(p.firstMask) && !hasFirstByte(p.firstMask, data[start]) {
		return nil, 0, false
	}
	if p.minBytes > 0 && len(data)-start < p.minBytes {
		return nil, 0, false
	}
	work := acquireNFAWork(len(p.states))
	defer releaseNFAWork(work)
	marks := work.marks
	generation := uint32(0)
	active := collectClosuresWithWork(p.closures, []int{p.start}, marks, &generation, work.active)
	active = filterDeadStates(active, p.dead)
	activeBits, nextBits := nfaWorkBits(work, len(p.states))
	for _, id := range active {
		if id >= 0 && id/64 < len(activeBits) {
			activeBits[id/64] |= 1 << uint(id%64)
		}
	}
	ends := dst[:0]
	next := work.next
	steps := len(active)
	posStart := start
	if p.layoutKind == 1 && len(p.prefix) > 0 {
		if p.maxBytes >= 0 && len(p.prefix) > p.maxBytes {
			return nil, steps, false
		}
		active = append(active[:0], p.candidateStates[len(p.candidateStates)-1]...)
		active = filterDeadStates(active, p.dead)
		clear(activeBits)
		for _, id := range active {
			if id >= 0 && id/64 < len(activeBits) {
				activeBits[id/64] |= 1 << uint(id%64)
			}
		}
		posStart += len(p.prefix)
		steps += len(p.prefix)
		for _, id := range active {
			if !p.accept[id] {
				continue
			}
			var ok bool
			ends, ok = appendNFAEnd(ends, posStart, maxResults)
			if !ok {
				return ends, steps, true
			}
		}
	}
	if posStart == start {
		for _, id := range active {
			if p.dead[id] {
				continue
			}
			var ok bool
			if p.accept[id] {
				ends, ok = appendNFAEnd(ends, start, maxResults)
			}
			if p.accept[id] && !ok {
				return ends, steps, true
			}
		}
	}
	if nfaBudgetExceeded(steps, maxSteps) {
		return nil, steps, true
	}
	end := boundedBackendEnd(data, start, p.maxBytes)
	for pos := posStart; pos < end && len(active) > 0; pos++ {
		generation++
		if generation == 0 {
			clear(marks)
			generation = 1
		}
		next = next[:0]
		clear(nextBits)
		sources := p.sourceMask[data[pos]]
		if len(active) >= len(p.states)/4 {
			for wi, word := range activeBits {
				if wi >= len(sources) {
					break
				}
				word &= sources[wi]
				for word != 0 {
					bit := bits.TrailingZeros64(word)
					id := wi*64 + bit
					if id < len(p.states) {
						steps++
						if nfaBudgetExceeded(steps, maxSteps) {
							return nil, steps, true
						}
						var transitions []int
						state := p.states[id]
						switch state.kind {
						case nfagraph.KindLiteral:
							transitions = p.literalClosure[id]
						case nfagraph.KindClass:
							if p.classMask[id][data[pos]/64]&(1<<uint(data[pos]%64)) == 0 {
								transitions = nil
							} else {
								transitions = p.classClosure[id]
							}
						}
						if state.kind == nfagraph.KindLiteral && state.lit != data[pos] {
							transitions = nil
						}
						for _, to := range transitions {
							if marks[to] != generation {
								marks[to] = generation
								next = append(next, to)
								nextBits[to/64] |= 1 << uint(to%64)
							}
						}
					}
					word &= word - 1
				}
			}
		} else {
			for _, id := range active {
				if p.dead[id] {
					continue
				}
				if id < 0 || id/64 >= len(sources) || sources[id/64]&(1<<uint(id%64)) == 0 {
					continue
				}
				steps++
				if nfaBudgetExceeded(steps, maxSteps) {
					return nil, steps, true
				}
				var transitions []int
				state := p.states[id]
				switch state.kind {
				case nfagraph.KindLiteral:
					if state.lit == data[pos] {
						transitions = p.literalClosure[id]
					}
				case nfagraph.KindClass:
					if p.classMask[id][data[pos]/64]&(1<<uint(data[pos]%64)) != 0 {
						transitions = p.classClosure[id]
					}
				default:
					// epsilon 节点已在 closures 中展开，本轮无需查询字节映射。
					continue
				}
				for _, to := range transitions {
					if marks[to] != generation {
						marks[to] = generation
						next = append(next, to)
						nextBits[to/64] |= 1 << uint(to%64)
					}
				}
			}
		}
		active, next = next, active
		activeBits, nextBits = nextBits, activeBits
		work.next, work.active = next, active
		work.nextBits, work.activeBits = nextBits, activeBits
		active = filterDeadStates(active, p.dead)
		clear(activeBits)
		for _, id := range active {
			if id >= 0 && id/64 < len(activeBits) {
				activeBits[id/64] |= 1 << uint(id%64)
			}
		}
		steps += len(active)
		if nfaBudgetExceeded(steps, maxSteps) {
			return nil, steps, true
		}
		for _, id := range active {
			if p.dead[id] {
				continue
			}
			var ok bool
			if p.accept[id] {
				ends, ok = appendNFAEnd(ends, pos+1, maxResults)
			}
			if p.accept[id] && !ok {
				return ends, steps, true
			}
		}
	}
	sort.Ints(ends)
	return normalizeNFAEnds(ends, maxResults), steps, false
}

func sparseRuntimeShapeOK(p *sparseNFAProgram) bool {
	if p == nil || p.start < 0 || p.start >= len(p.states) || len(p.states) != len(p.closures) || len(p.states) != len(p.trans) || len(p.states) != len(p.accept) || len(p.states) != len(p.classMask) || len(p.states) != len(p.literalClosure) || len(p.states) != len(p.classClosure) || len(p.states) != len(p.dead) {
		return false
	}
	if p.minBytes < 0 || p.maxBytes < -1 || p.maxBytes >= 0 && p.maxBytes < p.minBytes {
		return false
	}
	words := (len(p.states) + 63) / 64
	for value := range p.sourceMask {
		if len(p.sourceMask[value]) != words {
			return false
		}
		count := 0
		for _, word := range p.sourceMask[value] {
			count += bits.OnesCount64(word)
		}
		if int(p.sourceCount[value]) != count {
			return false
		}
	}
	if p.layoutKind > 1 || p.layoutKind == 1 && len(p.prefix) == 0 || p.layoutKind == 1 && len(p.candidateStates) != len(p.prefix) {
		return false
	}
	for i, st := range p.states {
		if len(p.closures[i]) == 0 {
			return false
		}
		for _, id := range st.next {
			if id < 0 || id >= len(p.states) {
				return false
			}
		}
		for _, id := range st.eps {
			if id < 0 || id >= len(p.states) {
				return false
			}
		}
		for _, id := range p.closures[i] {
			if id < 0 || id >= len(p.states) {
				return false
			}
		}
		for _, next := range p.trans[i] {
			for _, id := range next {
				if id < 0 || id >= len(p.states) {
					return false
				}
			}
		}
		for _, id := range p.literalClosure[i] {
			if id < 0 || id >= len(p.states) {
				return false
			}
		}
		for _, id := range p.classClosure[i] {
			if id < 0 || id >= len(p.states) {
				return false
			}
		}
		if p.dead[i] && p.accept[i] {
			return false
		}
		for value := 0; value < 256; value++ {
			want := (st.kind == nfagraph.KindLiteral && len(st.next) > 0 && st.lit == byte(value)) || (st.kind == nfagraph.KindClass && len(st.next) > 0 && p.classMask[i][value/64]&(1<<uint(value%64)) != 0)
			marked := i/64 < len(p.sourceMask[value]) && p.sourceMask[value][i/64]&(1<<uint(i%64)) != 0
			if want != marked {
				return false
			}
		}
	}
	if p.layoutKind == 1 {
		for _, states := range p.candidateStates {
			if len(states) == 0 {
				return false
			}
			for j, id := range states {
				if id < 0 || id >= len(p.states) || j > 0 && states[j-1] >= id {
					return false
				}
			}
		}
	}
	if p.firstMask != firstMaskFromSparse(p) {
		return false
	}
	wantEmpty := false
	for _, id := range p.closures[p.start] {
		if id >= 0 && id < len(p.accept) && p.accept[id] {
			wantEmpty = true
			break
		}
	}
	if p.acceptsEmpty != wantEmpty {
		return false
	}
	return true
}

func firstMaskFromSparse(p *sparseNFAProgram) [4]uint64 {
	var mask [4]uint64
	if p == nil || p.start < 0 || p.start >= len(p.closures) {
		return mask
	}
	for _, id := range p.closures[p.start] {
		if id < 0 || id >= len(p.states) {
			continue
		}
		st := p.states[id]
		if st.kind == nfagraph.KindLiteral && len(st.next) > 0 {
			if int(st.lit)/64 < len(mask) && id/64 < len(p.sourceMask[st.lit]) && p.sourceMask[st.lit][id/64]&(1<<uint(id%64)) != 0 {
				mask[st.lit/64] |= 1 << uint(st.lit%64)
			}
		} else if st.kind == nfagraph.KindClass && len(st.next) > 0 {
			for b := 0; b < 256; b++ {
				if id/64 < len(p.sourceMask[b]) && p.sourceMask[b][id/64]&(1<<uint(id%64)) != 0 && p.classMask[id][b/64]&(1<<uint(b%64)) != 0 {
					mask[b/64] |= 1 << uint(b%64)
				}
			}
		}
	}
	return mask
}

func equalByteMask(a, b [4]uint64) bool {
	return a == b
}

type lbrProgram struct {
	graph        *nfagraph.Graph
	successors   map[graph.Vertex][]graph.Vertex
	classMask    map[graph.Vertex][4]uint64
	vertices     []graph.Vertex
	index        map[graph.Vertex]int
	start        int
	kinds        []nfagraph.NodeKind
	assertions   []parser.AssertionKind
	literals     []byte
	classMasks   [][4]uint64
	next         [][]int
	deadStates   []bool
	queueLimit   int
	queueBytes   uint64
	firstMask    [4]uint64
	acceptsEmpty bool
	dead         map[graph.Vertex]bool
	prefix       []byte
	minBytes     int
	maxBytes     int
}

const (
	lbrQueueLimit = 1 << 16
	lbrStateBytes = 16
)

type lbrState struct {
	id  int
	pos int
}

type lbrWork struct {
	queue   []lbrState
	seen    map[lbrState]struct{}
	emitted map[int]struct{}
}

var lbrWorkPool sync.Pool

// validate 检查 LBR 的节点索引、转移表、字符类掩码和前缀布局。
// 运行前完成完整校验，避免损坏的断言图进入队列执行器。
func (p *lbrProgram) validate() error {
	if p == nil || p.graph == nil || p.graph.Flow == nil {
		return fmt.Errorf("invalid lbr graph")
	}
	if err := p.graph.Validate(); err != nil {
		return err
	}
	if p.queueLimit <= 0 || p.queueBytes == 0 || len(p.successors) != len(p.graph.Nodes) {
		return fmt.Errorf("invalid lbr execution limits or successors")
	}
	if !bytes.Equal(p.prefix, lbrLiteralPrefix(p.graph)) {
		return fmt.Errorf("lbr prefix mismatch")
	}
	if p.firstMask != firstByteMask(p.graph) || p.acceptsEmpty != graphAcceptsEmpty(p.graph) {
		return fmt.Errorf("lbr start candidate metadata mismatch")
	}
	minBytes, maxBytes := graphLengthBounds(p.graph)
	if p.minBytes != minBytes || p.maxBytes != maxBytes {
		return fmt.Errorf("lbr length bounds mismatch")
	}
	wantDead := computeGraphDead(p.graph)
	if len(p.dead) != len(p.graph.Nodes) {
		return fmt.Errorf("lbr dead-state map size mismatch")
	}
	if p.queueBytes < uint64(p.queueLimit)*lbrStateBytes {
		return fmt.Errorf("lbr queue byte budget mismatch")
	}
	for id := range p.graph.Nodes {
		_, want := wantDead[id]
		if p.dead[id] != want {
			return fmt.Errorf("lbr dead-state map mismatch")
		}
	}
	for _, id := range p.graph.NodeIDs() {
		n := p.graph.Nodes[id]
		if n == nil {
			return fmt.Errorf("lbr node missing")
		}
		switch n.Kind {
		case nfagraph.KindAssertion:
			switch n.Assertion {
			case parser.Begin, parser.End, parser.WordBoundary, parser.NonWordBoundary, parser.BeginAbsolute, parser.EndAbsolute, parser.EndBeforeFinalNewline:
			default:
				return fmt.Errorf("lbr assertion kind unsupported")
			}
		case nfagraph.KindLiteral:
			if len(n.Literal) != 1 {
				return fmt.Errorf("lbr literal width unsupported")
			}
		case nfagraph.KindClass:
			if n.Unicode != nil {
				return fmt.Errorf("lbr unicode class unsupported")
			}
		case nfagraph.KindStart, nfagraph.KindAccept, nfagraph.KindSplit, nfagraph.KindJoin, nfagraph.KindReport:
		default:
			return fmt.Errorf("lbr node kind unsupported")
		}
		row, ok := p.successors[id]
		if !ok {
			return fmt.Errorf("lbr successor row missing")
		}
		for i := 1; i < len(row); i++ {
			if row[i-1] >= row[i] {
				return fmt.Errorf("lbr successors are not strictly ordered")
			}
		}
		want := append([]graph.Vertex(nil), p.graph.Flow.Successors(id)...)
		sort.Slice(want, func(i, j int) bool { return want[i] < want[j] })
		want = dedupVertices(want)
		if len(row) != len(want) {
			return fmt.Errorf("lbr successor row mismatch")
		}
		for i := range row {
			if row[i] != want[i] || p.graph.Nodes[row[i]] == nil {
				return fmt.Errorf("lbr successor target mismatch")
			}
		}
		if n.Kind == nfagraph.KindClass {
			mask, ok := p.classMask[id]
			if !ok {
				return fmt.Errorf("lbr class mask missing")
			}
			for value := 0; value < 256; value++ {
				expected := castleMatches(n, byte(value))
				actual := mask[value/64]&(1<<uint(value%64)) != 0
				if expected != actual {
					return fmt.Errorf("lbr class mask mismatch")
				}
			}
		}
	}
	return nil
}

func acquireLBRWork() *lbrWork {
	w, _ := lbrWorkPool.Get().(*lbrWork)
	if w == nil {
		w = &lbrWork{seen: make(map[lbrState]struct{}), emitted: make(map[int]struct{})}
	}
	if w.seen == nil {
		w.seen = make(map[lbrState]struct{})
	}
	if w.emitted == nil {
		w.emitted = make(map[int]struct{})
	}
	w.queue = w.queue[:0]
	if cap(w.queue) < 256 {
		w.queue = make([]lbrState, 0, 256)
	}
	for key := range w.seen {
		delete(w.seen, key)
	}
	for key := range w.emitted {
		delete(w.emitted, key)
	}
	return w
}

func releaseLBRWork(w *lbrWork) {
	if w == nil {
		return
	}
	w.queue = w.queue[:0]
	if cap(w.queue) > 1<<16 {
		w.queue = nil
	}
	if len(w.seen) > 1<<16 {
		w.seen = nil
	}
	if len(w.emitted) > 1<<16 {
		w.emitted = nil
	}
	lbrWorkPool.Put(w)
}

func (p *lbrProgram) MatchAtBudget(data []byte, start, maxSteps, maxResults int) ([]int, int, bool) {
	if p == nil || p.graph == nil || start < 0 || start > len(data) || maxSteps < 0 || maxResults < 0 || !lbrRuntimeShapeOK(p) {
		return nil, 0, false
	}
	return p.matchAtBudgetInto(data, start, maxSteps, maxResults, nil)
}

// matchAtBudget 在调用方已完成布局校验后执行一次匹配。
// 整块扫描会对同一不可变布局重复调用该路径，避免逐起点重复校验整张图。
func (p *lbrProgram) matchAtBudget(data []byte, start, maxSteps, maxResults int) ([]int, int, bool) {
	return p.matchAtBudgetInto(data, start, maxSteps, maxResults, nil)
}

func (p *lbrProgram) matchAtBudgetInto(data []byte, start, maxSteps, maxResults int, dst []int) ([]int, int, bool) {
	if len(p.prefix) == 0 && start < len(data) && !p.acceptsEmpty && !byteMaskEmpty(p.firstMask) && !hasFirstByte(p.firstMask, data[start]) {
		return nil, 0, false
	}
	if len(p.prefix) > 0 && !prefixMatchesAt(data, start, p.prefix) {
		return nil, 0, false
	}
	if p.minBytes > 0 && len(data)-start < p.minBytes {
		return nil, 0, false
	}
	work := acquireLBRWork()
	defer releaseLBRWork(work)
	end := boundedBackendEnd(data, start, p.maxBytes)
	q := append(work.queue, lbrState{p.start, start})
	head := 0
	seen := work.seen
	emitted := work.emitted
	queueLimit := p.queueLimit
	if queueLimit <= 0 {
		return nil, 0, false
	}
	ends := dst[:0]
	steps := 0
	for head < len(q) {
		cur := q[head]
		head++
		if head > 1024 && head*2 >= len(q) {
			copy(q, q[head:])
			q = q[:len(q)-head]
			head = 0
		}
		if _, exists := seen[cur]; exists {
			continue
		}
		seen[cur] = struct{}{}
		if len(seen) > queueLimit {
			return ends, steps, true
		}
		steps++
		if nfaBudgetExceeded(steps, maxSteps) {
			return nil, steps, true
		}
		if cur.id < 0 || cur.id >= len(p.kinds) || p.deadStates[cur.id] {
			continue
		}
		if cur.pos < start || cur.pos > end {
			continue
		}
		if p.kinds[cur.id] == nfagraph.KindAccept {
			if _, ok := emitted[cur.pos]; ok {
				continue
			}
			if maxResults > 0 && len(ends) >= maxResults {
				return orderedEnds(ends), steps, true
			}
			emitted[cur.pos] = struct{}{}
			ends = append(ends, cur.pos)
			if maxResults > 0 && len(ends) >= maxResults {
				return orderedEnds(ends), steps, true
			}
			continue
		}
		if p.kinds[cur.id] == nfagraph.KindAssertion {
			if !assertion(p.assertions[cur.id], data, cur.pos, false) {
				continue
			}
			for _, to := range p.next[cur.id] {
				q = append(q, lbrState{to, cur.pos})
				if len(q)-head > queueLimit {
					return nil, steps, true
				}
			}
			continue
		}
		if p.kinds[cur.id] == nfagraph.KindLiteral {
			if cur.pos >= end || p.literals[cur.id] != data[cur.pos] {
				continue
			}
			for _, to := range p.next[cur.id] {
				q = append(q, lbrState{to, cur.pos + 1})
				if len(q)-head > queueLimit {
					return nil, steps, true
				}
			}
			continue
		}
		if p.kinds[cur.id] == nfagraph.KindClass {
			if cur.pos >= end || p.classMasks[cur.id][data[cur.pos]/64]&(1<<uint(data[cur.pos]%64)) == 0 {
				continue
			}
			for _, to := range p.next[cur.id] {
				q = append(q, lbrState{to, cur.pos + 1})
				if len(q)-head > queueLimit {
					return nil, steps, true
				}
			}
			continue
		}
		for _, to := range p.next[cur.id] {
			q = append(q, lbrState{to, cur.pos})
			if len(q)-head > queueLimit {
				return nil, steps, true
			}
		}
	}
	sort.Ints(ends)
	return normalizeNFAEnds(ends, maxResults), steps, false
}

func lbrRuntimeShapeOK(p *lbrProgram) bool {
	if p == nil || p.graph == nil || p.graph.Flow == nil || p.queueLimit <= 0 || p.queueBytes < uint64(p.queueLimit)*lbrStateBytes || len(p.successors) != len(p.graph.Nodes) || len(p.vertices) != len(p.graph.Nodes) || len(p.index) != len(p.graph.Nodes) || p.start < 0 || p.start >= len(p.vertices) || len(p.kinds) != len(p.vertices) || len(p.assertions) != len(p.vertices) || len(p.literals) != len(p.vertices) || len(p.classMasks) != len(p.vertices) || len(p.next) != len(p.vertices) || len(p.deadStates) != len(p.vertices) {
		return false
	}
	if len(p.dead) != len(p.graph.Nodes) {
		return false
	}
	minBytes, maxBytes := graphLengthBounds(p.graph)
	if p.minBytes != minBytes || p.maxBytes != maxBytes {
		return false
	}
	for _, id := range p.graph.NodeIDs() {
		i, ok := p.index[id]
		if !ok || i < 0 || i >= len(p.vertices) || p.vertices[i] != id {
			return false
		}
		n := p.graph.Nodes[id]
		if n == nil {
			return false
		}
		switch n.Kind {
		case nfagraph.KindStart, nfagraph.KindAccept, nfagraph.KindLiteral, nfagraph.KindClass, nfagraph.KindAssertion, nfagraph.KindSplit, nfagraph.KindJoin, nfagraph.KindReport:
		default:
			return false
		}
		if n.Kind == nfagraph.KindLiteral && len(n.Literal) != 1 {
			return false
		}
		if p.kinds[i] != n.Kind || p.assertions[i] != n.Assertion || n.Kind == nfagraph.KindLiteral && p.literals[i] != n.Literal[0] || n.Kind == nfagraph.KindClass && p.classMasks[i] != p.classMask[id] || p.deadStates[i] != p.dead[id] {
			return false
		}
		if n.Kind == nfagraph.KindClass && n.Unicode != nil {
			return false
		}
		if n.Kind == nfagraph.KindAssertion {
			switch n.Assertion {
			case parser.Begin, parser.End, parser.WordBoundary, parser.NonWordBoundary, parser.BeginAbsolute, parser.EndAbsolute, parser.EndBeforeFinalNewline:
			default:
				return false
			}
		}
		if _, ok := p.successors[id]; !ok {
			return false
		}
		for _, target := range p.successors[id] {
			if p.graph.Nodes[target] == nil {
				return false
			}
		}
		row := p.successors[id]
		for i := 1; i < len(row); i++ {
			if row[i-1] >= row[i] {
				return false
			}
		}
		want := append([]graph.Vertex(nil), p.graph.Flow.Successors(id)...)
		sort.Slice(want, func(i, j int) bool { return want[i] < want[j] })
		want = dedupVertices(want)
		if len(row) != len(want) {
			return false
		}
		for i := range row {
			if row[i] != want[i] {
				return false
			}
		}
		if len(p.next[p.index[id]]) != len(row) {
			return false
		}
		for j, target := range row {
			if p.next[p.index[id]][j] < 0 || p.next[p.index[id]][j] >= len(p.vertices) || p.vertices[p.next[p.index[id]][j]] != target {
				return false
			}
		}
		if n.Kind == nfagraph.KindClass {
			mask, ok := p.classMask[id]
			if !ok {
				return false
			}
			for value := 0; value < 256; value++ {
				expected := castleMatches(n, byte(value))
				actual := mask[value/64]&(1<<uint(value%64)) != 0
				if expected != actual {
					return false
				}
			}
		}
	}
	for id := range p.successors {
		if p.graph.Nodes[id] == nil {
			return false
		}
	}
	_, ok := p.graph.Nodes[p.graph.Start]
	if !ok {
		return false
	}
	if index, ok := p.index[p.graph.Start]; !ok || index != p.start {
		return false
	}
	return p.firstMask == firstByteMask(p.graph) && p.acceptsEmpty == graphAcceptsEmpty(p.graph)
}

func (p *lbrProgram) MatchAt(data []byte, start int) []int {
	ends, _, _ := p.MatchAtBudget(data, start, 0, 0)
	return ends
}

func (p *lbrProgram) Spans(data []byte, limit int) []Span {
	if p == nil || limit < 0 || !lbrRuntimeShapeOK(p) {
		return nil
	}
	out := make([]Span, 0, initialSpanCapacity(data, limit))
	forEachNFAStart(data, p.prefix, p.firstMask, p.acceptsEmpty, dispatch.DefaultBackend(), func(start int) bool {
		ends, _, _ := p.matchAtBudget(data, start, 0, 0)
		for _, end := range ends {
			out = append(out, Span{From: start, To: end})
			if limit > 0 && len(out) >= limit {
				return false
			}
		}
		return true
	})
	return out
}

func compileLBR(g *nfagraph.Graph) *lbrProgram {
	if g == nil || g.Validate() != nil || len(g.Nodes) > 1024 || g.Flow == nil || g.Flow.EdgeCount() > 4096 {
		return nil
	}
	if uint64(len(g.Nodes))*48+uint64(g.Flow.EdgeCount())*16 > maxLayoutMemory {
		return nil
	}
	has := false
	for _, id := range g.NodeIDs() {
		n := g.Nodes[id]
		switch n.Kind {
		case nfagraph.KindAssertion:
			switch n.Assertion {
			case parser.Begin, parser.End, parser.WordBoundary, parser.NonWordBoundary, parser.BeginAbsolute, parser.EndAbsolute, parser.EndBeforeFinalNewline:
				has = true
			default:
				return nil
			}
		case nfagraph.KindLiteral:
			if len(n.Literal) != 1 {
				return nil
			}
		case nfagraph.KindClass:
			if n.Unicode != nil {
				return nil
			}
		case nfagraph.KindStart, nfagraph.KindAccept, nfagraph.KindSplit, nfagraph.KindJoin, nfagraph.KindReport:
		default:
			return nil
		}
	}
	if !has {
		return nil
	}
	successors := make(map[graph.Vertex][]graph.Vertex, len(g.Nodes))
	classMask := make(map[graph.Vertex][4]uint64)
	for _, id := range g.NodeIDs() {
		next := append([]graph.Vertex(nil), g.Flow.Successors(id)...)
		sort.Slice(next, func(i, j int) bool { return next[i] < next[j] })
		successors[id] = dedupVertices(next)
		if node := g.Nodes[id]; node != nil && node.Kind == nfagraph.KindClass {
			var mask [4]uint64
			for value := 0; value < 256; value++ {
				if castleMatches(node, byte(value)) {
					mask[value/64] |= 1 << uint(value%64)
				}
			}
			classMask[id] = mask
		}
	}
	dead := make(map[graph.Vertex]bool, len(g.Nodes))
	for _, id := range g.NodeIDs() {
		dead[id] = false
	}
	for id := range computeGraphDead(g) {
		dead[id] = true
	}
	minBytes, maxBytes := graphLengthBounds(g)
	ids := g.NodeIDs()
	queueLimit := lbrQueueLimit
	if budget := int(maxLayoutMemory / lbrStateBytes); queueLimit > budget {
		queueLimit = budget
	}
	p := &lbrProgram{graph: g, successors: successors, classMask: classMask, vertices: append([]graph.Vertex(nil), ids...), index: make(map[graph.Vertex]int, len(ids)), start: 0, kinds: make([]nfagraph.NodeKind, len(ids)), assertions: make([]parser.AssertionKind, len(ids)), literals: make([]byte, len(ids)), classMasks: make([][4]uint64, len(ids)), next: make([][]int, len(ids)), deadStates: make([]bool, len(ids)), queueLimit: queueLimit, queueBytes: uint64(queueLimit) * lbrStateBytes, prefix: lbrLiteralPrefix(g), firstMask: firstByteMask(g), acceptsEmpty: graphAcceptsEmpty(g), dead: dead, minBytes: minBytes, maxBytes: maxBytes}
	for i, id := range ids {
		p.index[id] = i
	}
	for i, id := range ids {
		p.kinds[i] = g.Nodes[id].Kind
		p.assertions[i] = g.Nodes[id].Assertion
		p.deadStates[i] = dead[id]
		if g.Nodes[id].Kind == nfagraph.KindLiteral && len(g.Nodes[id].Literal) == 1 {
			p.literals[i] = g.Nodes[id].Literal[0]
		}
		if g.Nodes[id].Kind == nfagraph.KindClass {
			p.classMasks[i] = classMask[id]
		}
		next := successors[id]
		p.next[i] = make([]int, 0, len(next))
		for _, to := range next {
			p.next[i] = append(p.next[i], p.index[to])
		}
	}
	p.start = p.index[g.Start]
	return p
}

func dedupVertices(values []graph.Vertex) []graph.Vertex {
	if len(values) < 2 {
		return values
	}
	out := values[:1]
	for _, value := range values[1:] {
		if value != out[len(out)-1] {
			out = append(out, value)
		}
	}
	return out
}

func lbrLiteralPrefix(g *nfagraph.Graph) []byte {
	if g == nil || g.Flow == nil {
		return nil
	}
	// 存在零宽接受路径时不能使用文字前缀过滤，否则会漏掉空匹配。
	if graphAcceptsEmpty(g) {
		return nil
	}
	frontier := []graph.Vertex{g.Start}
	prefix := make([]byte, 0, 8)
	visited := make(map[graph.Vertex]struct{}, 16)
	for depth := 0; depth < 64 && len(frontier) > 0; depth++ {
		closure := lbrEpsilonClosure(g, frontier)
		if len(closure) == 0 {
			return prefix
		}
		// 重新进入已处理节点表示存在循环或汇合，不能把循环次数
		// 误判为固定前缀长度；保留已确认的前缀并停止展开。
		for _, id := range closure {
			if _, ok := visited[id]; ok {
				return prefix
			}
		}
		for _, id := range closure {
			visited[id] = struct{}{}
		}
		var next []graph.Vertex
		var value byte
		for _, id := range closure {
			n := g.Nodes[id]
			if n == nil || n.Kind == nfagraph.KindAccept || n.Kind != nfagraph.KindLiteral || len(n.Literal) != 1 {
				return prefix
			}
			if len(next) == 0 {
				value = n.Literal[0]
			} else if n.Literal[0] != value {
				return prefix
			}
			next = append(next, g.Flow.Successors(id)...)
		}
		if len(next) == 0 {
			return prefix
		}
		prefix = append(prefix, value)
		frontier = dedupVerticesSorted(next)
	}
	return prefix
}

func lbrEpsilonClosure(g *nfagraph.Graph, seed []graph.Vertex) []graph.Vertex {
	seen := make(map[graph.Vertex]struct{}, len(seed))
	queue := append([]graph.Vertex(nil), seed...)
	out := make([]graph.Vertex, 0, len(seed))
	for head := 0; head < len(queue); head++ {
		id := queue[head]
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		n := g.Nodes[id]
		if n == nil {
			continue
		}
		if n.Kind == nfagraph.KindLiteral || n.Kind == nfagraph.KindClass || n.Kind == nfagraph.KindAccept {
			out = append(out, id)
			continue
		}
		queue = append(queue, g.Flow.Successors(id)...)
	}
	return dedupVerticesSorted(out)
}

// forEachPrefixStart 只枚举可能命中固定前缀的起点；空前缀时保留所有起点。
// 回调返回 false 可立即终止枚举，用于结果预算提前结束。
func forEachPrefixStart(data, prefix []byte, fn func(int) bool) {
	if fn == nil {
		return
	}
	if len(prefix) == 0 {
		for start := 0; start <= len(data); start++ {
			if !fn(start) {
				return
			}
		}
		return
	}
	if len(prefix) == 1 {
		// 使用可移植向量后端批量提取候选掩码；尾部不足一个向量时
		// 仍使用标量路径，确保所有架构和非对齐输入保持相同语义。
		backend := dispatch.DefaultBackend()
		width := 16
		for from := 0; from+width <= len(data); from += width {
			vector, ok := backend.Load(data, from)
			if !ok {
				break
			}
			mask := backend.EqualByteMask(vector, prefix[0])
			for mask != 0 {
				bit := bits.TrailingZeros16(mask)
				if !fn(from + bit) {
					return
				}
				mask &^= 1 << uint(bit)
			}
		}
		for start := len(data) - len(data)%width; start < len(data); start++ {
			if data[start] == prefix[0] && !fn(start) {
				return
			}
		}
		return
	}
	for from := 0; from <= len(data); {
		offset := bytes.Index(data[from:], prefix)
		if offset < 0 {
			return
		}
		start := from + offset
		if !fn(start) {
			return
		}
		from = start + 1
	}
}

// forEachNFAStart 使用前缀或首字节集合枚举候选起点，避免专用引擎
// 在大输入上为每个偏移创建完整状态工作区。
func forEachNFAStart(data, prefix []byte, first [4]uint64, acceptsEmpty bool, backend simd.Backend, fn func(int) bool) {
	if fn == nil {
		return
	}
	if len(prefix) > 0 {
		forEachPrefixStart(data, prefix, fn)
		return
	}
	if acceptsEmpty {
		for start := 0; start <= len(data); start++ {
			if !fn(start) {
				return
			}
		}
		return
	}
	// 空掩码表示编译阶段无法证明首字节（例如仅含边界断言的图），
	// 不能把它误当成“无匹配”而跳过整个输入。
	if byteMaskEmpty(first) {
		for start := 0; start < len(data); start++ {
			if !fn(start) {
				return
			}
		}
		return
	}
	if backend == nil {
		backend = dispatch.DefaultBackend()
	}
	const width = 16
	for off := 0; off+width <= len(data); off += width {
		vector, ok := backend.Load(data, off)
		if !ok {
			break
		}
		mask := backend.ByteSetMask(vector, first)
		for mask != 0 {
			bit := bits.TrailingZeros16(mask)
			if !fn(off + bit) {
				return
			}
			mask &^= 1 << uint(bit)
		}
	}
	for off := len(data) - len(data)%width; off < len(data); off++ {
		if first[data[off]/64]&(1<<uint(data[off]%64)) != 0 && !fn(off) {
			return
		}
	}
}

type byteNFAState struct {
	kind  nfagraph.NodeKind
	lit   byte
	class *nfagraph.Node
	eps   []int
	next  []int
}

func (p *byteNFAProgram) validate() error {
	if p == nil || p.start < 0 || p.start >= len(p.states) || len(p.closures) != len(p.states) {
		return fmt.Errorf("invalid byte nfa layout")
	}
	if p.minBytes < 0 || p.maxBytes < -1 || p.maxBytes >= 0 && p.maxBytes < p.minBytes {
		return fmt.Errorf("byte length bounds are invalid")
	}
	if p.trans != nil && (len(p.trans) != len(p.states) || len(p.transClosure) != len(p.states)) {
		return fmt.Errorf("invalid byte transition rows")
	}
	if len(p.classMask) != len(p.states) {
		return fmt.Errorf("invalid byte class masks")
	}
	for i, state := range p.states {
		if state.kind == nfagraph.KindClass && state.class == nil {
			return fmt.Errorf("byte class state missing payload")
		}
		if state.kind == nfagraph.KindClass {
			for b := 0; b < 256; b++ {
				want := castleMatches(state.class, byte(b))
				if (p.classMask[i][b/64]&(1<<uint(b%64)) != 0) != want {
					return fmt.Errorf("byte class mask mismatch")
				}
			}
		}
		if (state.kind == nfagraph.KindLiteral || state.kind == nfagraph.KindClass) && len(state.eps) != 0 {
			return fmt.Errorf("byte consumable state has epsilon transitions")
		}
		if state.kind != nfagraph.KindLiteral && state.kind != nfagraph.KindClass && len(state.next) != 0 {
			return fmt.Errorf("byte epsilon state has byte transitions")
		}
		for _, to := range p.closures[i] {
			if to < 0 || to >= len(p.states) {
				return fmt.Errorf("byte closure target out of bounds at state %d", i)
			}
		}
		if !containsInt(p.closures[i], i) {
			return fmt.Errorf("byte closure misses source state")
		}
		for j := 1; j < len(p.closures[i]); j++ {
			if p.closures[i][j-1] >= p.closures[i][j] {
				return fmt.Errorf("byte closure is not strictly ordered")
			}
		}
		for j := 1; j < len(state.eps); j++ {
			if state.eps[j-1] >= state.eps[j] {
				return fmt.Errorf("byte epsilon transitions are not strictly ordered")
			}
		}
		for j := 1; j < len(state.next); j++ {
			if state.next[j-1] >= state.next[j] {
				return fmt.Errorf("byte transitions are not strictly ordered")
			}
		}
		for _, to := range append(append([]int(nil), state.eps...), state.next...) {
			if to < 0 || to >= len(p.states) {
				return fmt.Errorf("byte transition target out of bounds at state %d", i)
			}
		}
		if p.trans != nil && len(p.trans[i]) != 256 {
			return fmt.Errorf("invalid byte transition width")
		}
		if p.transClosure != nil && len(p.transClosure[i]) != 256 {
			return fmt.Errorf("invalid byte closure transition width")
		}
		if p.trans != nil {
			for value, targets := range p.trans[i] {
				for j := 1; j < len(targets); j++ {
					if targets[j-1] >= targets[j] {
						return fmt.Errorf("byte transition targets are not strictly ordered")
					}
				}
				for _, target := range targets {
					if target < 0 || target >= len(p.states) {
						return fmt.Errorf("byte transition target out of bounds")
					}
				}
				if p.transClosure != nil && !equalInts(p.transClosure[i][value], mergeClosureTargets(p.closures, targets)) {
					return fmt.Errorf("byte transition closure mismatch")
				}
				if state.kind == nfagraph.KindLiteral && byte(value) != state.lit && len(targets) > 0 {
					return fmt.Errorf("byte literal transition mismatch")
				}
				if state.kind == nfagraph.KindClass {
					expected := castleMatches(state.class, byte(value)) && len(state.next) > 0
					if (len(targets) > 0) != expected {
						return fmt.Errorf("byte class transition mismatch")
					}
				}
				expectedByte := state.kind == nfagraph.KindLiteral && byte(value) == state.lit || state.kind == nfagraph.KindClass && castleMatches(state.class, byte(value))
				if expectedByte && !sameIntSet(targets, state.next) {
					return fmt.Errorf("byte transition targets disagree with state layout at state %d byte %d", i, value)
				}
			}
		}
	}
	return nil
}

func sameIntSet(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	if len(a) == 0 {
		return true
	}
	left := append([]int(nil), a...)
	right := append([]int(nil), b...)
	sort.Ints(left)
	sort.Ints(right)
	return equalInts(left, right)
}

type tableNFAProgram struct {
	closure [][]int
	trans   [][][]int
	// transClosure 预先展开每个字节转移的 epsilon 闭包，降低执行阶段的闭包开销。
	transClosure [][][]int
	accept       []bool
	dead         []bool
	firstMask    [4]uint64
	acceptsEmpty bool
	start        int
	sourceMask   [256][]uint64
	// sourceCount 记录每个字节可消费的源状态数，在稠密活动集合时改用
	// 源掩码驱动循环，避免逐个检查无关状态。
	sourceCount [256]int
	prefix      []byte
	minBytes    int
	maxBytes    int
}

func (p *tableNFAProgram) validate() error {
	if p == nil || p.start < 0 || p.start >= len(p.trans) || len(p.closure) != len(p.trans) || len(p.transClosure) != len(p.trans) || len(p.accept) != len(p.trans) || len(p.dead) != len(p.trans) {
		return fmt.Errorf("invalid table nfa layout")
	}
	for i, row := range p.trans {
		if p.dead[i] && p.accept[i] {
			return fmt.Errorf("table accept state marked dead")
		}
		for _, id := range p.closure[i] {
			if id < 0 || id >= len(p.trans) {
				return fmt.Errorf("table closure target out of bounds")
			}
		}
		for j := 1; j < len(p.closure[i]); j++ {
			if p.closure[i][j-1] >= p.closure[i][j] {
				return fmt.Errorf("table closure is not strictly ordered")
			}
		}
		if !containsInt(p.closure[i], i) {
			return fmt.Errorf("table closure misses source state")
		}
		if len(row) != 256 {
			return fmt.Errorf("invalid table width at state %d", i)
		}
		if len(p.transClosure[i]) != 256 {
			return fmt.Errorf("invalid table closure width at state %d", i)
		}
		for _, next := range row {
			last := -1
			for _, to := range next {
				if to < 0 || to >= len(p.trans) {
					return fmt.Errorf("table transition target out of bounds")
				}
				if to <= last {
					return fmt.Errorf("table transition targets are not strictly ordered")
				}
				last = to
			}
		}
		for value, next := range p.transClosure[i] {
			want := mergeClosureTargets(p.closure, row[value])
			if !equalInts(next, want) {
				return fmt.Errorf("table transition closure mismatch at state %d byte %d", i, value)
			}
		}
	}
	words := (len(p.trans) + 63) / 64
	for value := range p.sourceMask {
		if len(p.sourceMask[value]) != words {
			return fmt.Errorf("table source mask width mismatch")
		}
		for i := range p.trans {
			hasTransition := len(p.trans[i][byte(value)]) != 0
			marked := p.sourceMask[value][i/64]&(1<<uint(i%64)) != 0
			if hasTransition != marked {
				return fmt.Errorf("table source mask mismatch at state %d", i)
			}
		}
	}
	for value, count := range p.sourceCount {
		if count < 0 || count > len(p.trans) {
			return fmt.Errorf("table source count is invalid")
		}
		actual := 0
		for _, word := range p.sourceMask[value] {
			actual += bits.OnesCount64(word)
		}
		if actual != count {
			return fmt.Errorf("table source count mismatch")
		}
	}
	if !equalBools(p.dead, computeTableDead(p)) {
		return fmt.Errorf("table dead-state map mismatch")
	}
	if p.firstMask != firstMaskFromTable(p) || p.acceptsEmpty != tableClosureAccepts(p, p.start) {
		return fmt.Errorf("table start candidate metadata mismatch")
	}
	if rem := len(p.trans) % 64; rem != 0 {
		mask := ^uint64(0) >> uint(64-rem)
		for _, source := range p.sourceMask {
			if source[len(source)-1]&^mask != 0 {
				return fmt.Errorf("table source mask contains out-of-range state")
			}
		}
	}
	return nil
}

type bitNFAProgram struct {
	words      int
	start      int
	accept     []uint64
	closure    [][]uint64
	trans      [][][]uint64
	prefix     []byte
	consumable []uint64
	// exceptional 标记没有可消费转移的节点，热路径可直接跳过。
	exceptional []uint64
	// epsilonOnly 标记仅参与闭包传播且不直接消费字节的中间节点。
	epsilonOnly  []uint64
	dead         []uint64
	deadHash     uint64
	firstMask    [4]uint64
	acceptsEmpty bool
	minBytes     int
	maxBytes     int
	// sourceMask 按输入字节索引可消费状态，减少热路径上的无效状态检查。
	sourceMask [256][]uint64
	// sourceCount 记录每个字节对应的可消费状态数，避免仅为判断空集合而遍历位图。
	sourceCount [256]uint16
	// sourceStates 保存每个字节的紧凑源状态列表；稀疏输入时直接遍历列表。
	sourceStates [256][]uint16
	backend      simd.Backend
}

// limexContext 保存一次位集合匹配的活动、转移和闭包工作区。
// 工作区按调用借用，避免每个起点重复分配固定大小的状态切片。
type limexContext struct {
	active []uint64
	next   []uint64
}

var limexContextPool sync.Pool

func acquireLimexContext(words int) *limexContext {
	c, _ := limexContextPool.Get().(*limexContext)
	if c == nil {
		c = &limexContext{}
	}
	if cap(c.active) < words {
		c.active = make([]uint64, words)
	} else {
		c.active = c.active[:words]
		clear(c.active)
	}
	if cap(c.next) < words {
		c.next = make([]uint64, words)
	} else {
		c.next = c.next[:words]
		clear(c.next)
	}
	return c
}

func releaseLimexContext(c *limexContext) {
	if c == nil {
		return
	}
	c.active = c.active[:0]
	c.next = c.next[:0]
	if cap(c.active) > 1<<16 || cap(c.next) > 1<<16 {
		c.active, c.next = nil, nil
	}
	limexContextPool.Put(c)
}

func (p *bitNFAProgram) validate() error {
	if p == nil || p.words <= 0 || p.start < 0 || p.start >= len(p.trans) || len(p.closure) != len(p.trans) || len(p.accept) != p.words || len(p.consumable) != p.words || len(p.exceptional) != p.words || len(p.epsilonOnly) != p.words || len(p.dead) != p.words {
		return fmt.Errorf("invalid bit nfa layout")
	}
	for i, row := range p.trans {
		if len(row) != 256 {
			return fmt.Errorf("invalid bit width at state %d", i)
		}
		for _, bits := range row {
			if len(bits) != p.words {
				return fmt.Errorf("invalid bit transition width")
			}
		}
	}
	for value := range p.sourceMask {
		if len(p.sourceMask[value]) != p.words {
			return fmt.Errorf("invalid bit source mask width")
		}
	}
	for i := range p.trans {
		var hasTransition bool
		for value := 0; value < 256; value++ {
			for _, bits := range p.trans[i][value] {
				if bits != 0 {
					hasTransition = true
					break
				}
			}
		}
		markedConsumable := p.consumable[i/64]&(1<<uint(i%64)) != 0
		if markedConsumable != hasTransition {
			return fmt.Errorf("bit consumable mask mismatch at state %d", i)
		}
		markedExceptional := p.exceptional[i/64]&(1<<uint(i%64)) != 0
		if markedExceptional == hasTransition {
			return fmt.Errorf("bit exceptional mask mismatch at state %d", i)
		}
		markedEpsilonOnly := p.epsilonOnly[i/64]&(1<<uint(i%64)) != 0
		wantEpsilonOnly := !hasTransition && p.accept[i/64]&(1<<uint(i%64)) == 0
		if markedEpsilonOnly != wantEpsilonOnly {
			return fmt.Errorf("bit epsilon-only mask mismatch at state %d", i)
		}
	}
	for value := 0; value < 256; value++ {
		count := 0
		for _, word := range p.sourceMask[value] {
			count += bits.OnesCount64(word)
		}
		if int(p.sourceCount[value]) != count {
			return fmt.Errorf("bit source count mismatch for byte %d", value)
		}
		if len(p.sourceStates[value]) != count {
			return fmt.Errorf("bit source state list mismatch for byte %d", value)
		}
		for _, state := range p.sourceStates[value] {
			if int(state) >= len(p.trans) || p.sourceMask[value][state/64]&(1<<uint(state%64)) == 0 {
				return fmt.Errorf("bit source state out of bounds for byte %d", value)
			}
		}
		for i := 1; i < len(p.sourceStates[value]); i++ {
			if p.sourceStates[value][i-1] >= p.sourceStates[value][i] {
				return fmt.Errorf("bit source states are not strictly ordered for byte %d", value)
			}
		}
		for i, state := range p.trans {
			hasTransition := false
			for _, bits := range state[value] {
				if bits != 0 {
					hasTransition = true
					break
				}
			}
			marked := p.sourceMask[value][i/64]&(1<<uint(i%64)) != 0
			if hasTransition != marked {
				return fmt.Errorf("bit source mask mismatch at state %d", i)
			}
		}
	}
	for _, closure := range p.closure {
		if len(closure) != p.words {
			return fmt.Errorf("invalid bit closure width")
		}
	}
	for i, closure := range p.closure {
		if i/64 >= len(closure) || closure[i/64]&(1<<uint(i%64)) == 0 {
			return fmt.Errorf("bit closure misses source state")
		}
	}
	if p.start/64 >= p.words {
		return fmt.Errorf("invalid bit start")
	}
	stateCount := len(p.trans)
	if rem := stateCount % 64; rem != 0 {
		mask := ^uint64(0) >> uint(64-rem)
		if p.accept[len(p.accept)-1]&^mask != 0 || p.consumable[len(p.consumable)-1]&^mask != 0 || p.exceptional[len(p.exceptional)-1]&^mask != 0 || p.epsilonOnly[len(p.epsilonOnly)-1]&^mask != 0 || p.dead[len(p.dead)-1]&^mask != 0 {
			return fmt.Errorf("bit state mask contains out-of-range state")
		}
		for _, source := range p.sourceMask {
			if source[len(source)-1]&^mask != 0 {
				return fmt.Errorf("bit source mask contains out-of-range state")
			}
		}
		for _, bits := range p.closure {
			if bits[len(bits)-1]&^mask != 0 {
				return fmt.Errorf("bit closure contains out-of-range state")
			}
		}
		for _, row := range p.trans {
			for _, targets := range row {
				if targets[len(targets)-1]&^mask != 0 {
					return fmt.Errorf("bit transition contains out-of-range state")
				}
			}
		}
	}
	if !equalUint64s(p.dead, computeBitDead(p)) {
		return fmt.Errorf("bit dead mask mismatch")
	}
	if p.deadHash != hashUint64s(p.dead) {
		return fmt.Errorf("bit dead mask checksum mismatch")
	}
	if p.firstMask != firstMaskFromBit(p) || p.acceptsEmpty != bitIntersects(p.closure[p.start], p.accept) {
		return fmt.Errorf("bit start candidate metadata mismatch")
	}
	if p.minBytes < 0 || p.maxBytes < -1 || p.maxBytes >= 0 && p.maxBytes < p.minBytes {
		return fmt.Errorf("bit length bounds are invalid")
	}
	return nil
}

func (p *bitNFAProgram) StateCount() int {
	if p == nil {
		return 0
	}
	return len(p.trans)
}
func (p *bitNFAProgram) EdgeCount() int {
	if p == nil {
		return 0
	}
	n := 0
	for _, row := range p.trans {
		for _, bits := range row {
			for _, w := range bits {
				n += bitsCount(w)
			}
		}
	}
	return n
}
func bitsCount(v uint64) int {
	n := 0
	for v != 0 {
		v &= v - 1
		n++
	}
	return n
}

func compileBitNFA(g *nfagraph.Graph) *bitNFAProgram {
	if g == nil || g.Validate() != nil {
		return nil
	}
	g = nfagraph.ExpandLiterals(g)
	// 位集合布局的上限由实际转移表字节数决定，不能仅按固定状态数拒绝。
	// 512 个状态仍可在既定 16MiB 布局预算内安全构建。
	if !bitNFAEligibleGraph(g) || len(g.Nodes) > limExStateLimit {
		return nil
	}
	ids := g.NodeIDs()
	// 表格同时保存原始转移和预展开闭包，按每个字节槽位预留指针与切片开销。
	stateCount := uint64(len(ids))
	wordCount := uint64((len(ids) + 63) / 64)
	transitionBytes := stateCount * 256 * wordCount * 8
	transitionMeta := stateCount * 256 * 24
	closureBytes := stateCount * wordCount * 8
	sourceBytes := 256 * wordCount * 8
	sourceStateBytes := stateCount * 256 * 2
	if transitionBytes > maxLayoutMemory || transitionMeta > maxLayoutMemory ||
		closureBytes > maxLayoutMemory || sourceBytes > maxLayoutMemory || sourceStateBytes > maxLayoutMemory ||
		transitionBytes+transitionMeta+closureBytes+sourceBytes+sourceStateBytes > maxLayoutMemory {
		return nil
	}
	idx := map[graph.Vertex]int{}
	for i, id := range ids {
		idx[id] = i
	}
	words := (len(ids) + 63) / 64
	if uint64(len(ids))*256*uint64(words)*8 > maxLayoutMemory {
		return nil
	}
	p := &bitNFAProgram{words: words, start: idx[g.Start], accept: make([]uint64, words), closure: make([][]uint64, len(ids)), trans: make([][][]uint64, len(ids)), prefix: requiredLiteralPrefix(g), consumable: make([]uint64, words), exceptional: make([]uint64, words), epsilonOnly: make([]uint64, words), dead: make([]uint64, words), firstMask: firstByteMask(g), backend: dispatch.DefaultBackend()}
	p.minBytes, p.maxBytes = graphLengthBounds(g)
	transitionStorage := make([]uint64, len(ids)*256*words)
	for b := range p.sourceMask {
		p.sourceMask[b] = make([]uint64, words)
	}
	for i := range ids {
		p.closure[i] = bitClosure(g, ids, idx, i, words)
	}
	for i, id := range ids {
		n := g.Nodes[id]
		p.trans[i] = make([][]uint64, 256)
		if n.Kind == nfagraph.KindAccept {
			p.accept[i/64] |= 1 << uint(i%64)
		}
		for b := 0; b < 256; b++ {
			offset := (i*256 + b) * words
			bits := transitionStorage[offset : offset+words]
			for _, to := range g.Flow.Successors(id) {
				j := idx[to]
				if (n.Kind == nfagraph.KindLiteral && len(n.Literal) == 1 && n.Literal[0] == byte(b)) || (n.Kind == nfagraph.KindClass && castleMatches(n, byte(b))) {
					p.sourceMask[b][i/64] |= 1 << uint(i%64)
					// 将目标的 epsilon 闭包预先并入转移，热路径无需再次遍历闭包。
					bitOr(bits, p.closure[j])
				}
			}
			p.trans[i][b] = bits
		}
		for b := 0; b < 256; b++ {
			if bitAny(p.trans[i][b]) {
				p.consumable[i/64] |= 1 << uint(i%64)
				break
			}
		}
		if p.consumable[i/64]&(1<<uint(i%64)) == 0 {
			p.exceptional[i/64] |= 1 << uint(i%64)
			if p.accept[i/64]&(1<<uint(i%64)) == 0 {
				p.epsilonOnly[i/64] |= 1 << uint(i%64)
			}
		}
	}
	for b := range p.sourceMask {
		var count int
		for _, word := range p.sourceMask[b] {
			count += bits.OnesCount64(word)
		}
		if count > int(^uint16(0)) {
			count = int(^uint16(0))
		}
		p.sourceCount[b] = uint16(count)
		if count > 0 {
			states := make([]uint16, 0, count)
			for wi, word := range p.sourceMask[b] {
				for word != 0 {
					bit := bits.TrailingZeros64(word)
					states = append(states, uint16(wi*64+bit))
					word &= word - 1
				}
			}
			p.sourceStates[b] = states
		}
	}
	p.dead = computeBitDead(p)
	p.deadHash = hashUint64s(p.dead)
	p.acceptsEmpty = bitIntersects(p.closure[p.start], p.accept)
	return p
}

// bitNFAEligibleGraph 允许 Repeat 作为零宽控制状态进入位集合闭包；
// Repeat 的边界由图构建阶段展开为分支/回边，消费状态仍只有文字和字符类。
func bitNFAEligibleGraph(g *nfagraph.Graph) bool {
	if g == nil || g.Flow == nil {
		return false
	}
	for _, id := range g.NodeIDs() {
		n := g.Nodes[id]
		if n == nil {
			return false
		}
		switch n.Kind {
		case nfagraph.KindStart, nfagraph.KindAccept, nfagraph.KindLiteral, nfagraph.KindClass, nfagraph.KindSplit, nfagraph.KindJoin, nfagraph.KindRepeat, nfagraph.KindReport:
			if n.Kind == nfagraph.KindClass && n.Unicode != nil {
				return false
			}
			if n.Kind == nfagraph.KindLiteral && len(n.Literal) != 1 {
				return false
			}
		default:
			return false
		}
	}
	return true
}

// computeBitDead 反向传播可达接受状态，避免把仅指向死分支的节点留在热路径。
func computeBitDead(p *bitNFAProgram) []uint64 {
	dead := make([]uint64, p.words)
	reachable := make([]bool, len(p.trans))
	for i := range p.trans {
		reachable[i] = bitIntersects(p.closure[i], p.accept)
	}
	changed := true
	for changed {
		changed = false
		for i := range p.trans {
			if reachable[i] {
				continue
			}
			for b := 0; b < 256 && !reachable[i]; b++ {
				for wi, word := range p.trans[i][b] {
					for word != 0 {
						bit := bits.TrailingZeros64(word)
						id := wi*64 + bit
						if id < len(reachable) && reachable[id] {
							reachable[i] = true
							changed = true
							break
						}
						word &^= 1 << uint(bit)
					}
					if reachable[i] {
						break
					}
				}
			}
		}
	}
	for i, ok := range reachable {
		if !ok {
			dead[i/64] |= 1 << uint(i%64)
		}
	}
	return dead
}

func hashUint64s(values []uint64) uint64 {
	h := uint64(1469598103934665603)
	for _, value := range values {
		h ^= value
		h *= 1099511628211
	}
	return h
}
func bitClosure(g *nfagraph.Graph, ids []graph.Vertex, idx map[graph.Vertex]int, start, words int) []uint64 {
	out := make([]uint64, words)
	q := []int{start}
	out[start/64] |= 1 << uint(start%64)
	for h := 0; h < len(q); h++ {
		n := g.Nodes[ids[q[h]]]
		if n.Kind == nfagraph.KindLiteral || n.Kind == nfagraph.KindClass {
			continue
		}
		for _, to := range g.Flow.Successors(ids[q[h]]) {
			j := idx[to]
			if out[j/64]&(1<<uint(j%64)) == 0 {
				out[j/64] |= 1 << uint(j%64)
				q = append(q, j)
			}
		}
	}
	return out
}
func bitOr(dst, src []uint64) {
	limit := len(dst)
	if len(src) < limit {
		limit = len(src)
	}
	for i := 0; i < limit; i++ {
		dst[i] |= src[i]
	}
}

func bitAny(values []uint64) bool {
	for _, value := range values {
		if value != 0 {
			return true
		}
	}
	return false
}
func bitNFAHas(bits []uint64, id int) bool {
	return id >= 0 && id/64 < len(bits) && bits[id/64]&(1<<uint(id%64)) != 0
}

// bitApplyTransitions 批量合并当前字节的位转移。
// 先用源状态掩码裁剪，再排除异常节点，保持标量实现与可选向量实现共享同一语义。
func bitApplyTransitions(p *bitNFAProgram, active, next []uint64, value byte) {
	if p == nil || p.sourceCount[value] == 0 {
		return
	}
	if sources := p.sourceStates[value]; len(sources) > 0 && len(sources) < len(p.trans)/4 {
		for _, state := range sources {
			i := int(state)
			if bitHas(active, i) {
				bitOr(next, p.trans[i][value])
			}
		}
		return
	}
	sources := p.sourceMask[value]
	for wi, word := range active {
		if wi >= len(sources) || wi >= len(p.exceptional) || wi >= len(p.epsilonOnly) {
			break
		}
		word &= sources[wi]
		word &^= p.epsilonOnly[wi]
		word &^= p.exceptional[wi]
		for word != 0 {
			bit := bits.TrailingZeros64(word)
			i := wi*64 + bit
			if i < len(p.trans) {
				bitOr(next, p.trans[i][value])
			}
			word &= word - 1
		}
	}
}

func bitHas(values []uint64, state int) bool {
	return state >= 0 && state/64 < len(values) && values[state/64]&(1<<uint(state%64)) != 0
}

func (p *bitNFAProgram) MatchAt(data []byte, start int) []int {
	ends, _, _ := p.MatchAtBudget(data, start, 0, 0)
	return ends
}

func (p *bitNFAProgram) MatchAtBudget(data []byte, start, maxSteps, maxResults int) ([]int, int, bool) {
	return p.MatchAtBudgetInto(data, start, maxSteps, maxResults, nil)
}

func (p *bitNFAProgram) MatchAtBudgetInto(data []byte, start, maxSteps, maxResults int, dst []int) ([]int, int, bool) {
	if p == nil || start < 0 || start > len(data) || maxSteps < 0 || maxResults < 0 || !bitRuntimeShapeOK(p) {
		return dst[:0], 0, false
	}
	return p.matchAtBudgetUnchecked(data, start, maxSteps, maxResults, dst)
}

func (p *bitNFAProgram) matchAtBudgetUnchecked(data []byte, start, maxSteps, maxResults int, dst []int) ([]int, int, bool) {
	if p.minBytes > 0 && len(data)-start < p.minBytes {
		return nil, 0, false
	}
	if len(p.prefix) > 0 && !prefixMatchesAt(data, start, p.prefix) {
		return nil, 0, false
	}
	if len(p.prefix) == 0 && start < len(data) && !p.acceptsEmpty && !byteMaskEmpty(p.firstMask) && !hasFirstByte(p.firstMask, data[start]) {
		return nil, 0, false
	}
	ctx := acquireLimexContext(p.words)
	defer releaseLimexContext(ctx)
	active, next := ctx.active, ctx.next
	copy(active, p.closure[p.start])
	steps := p.words
	if nfaBudgetExceeded(steps, maxSteps) {
		return nil, steps, true
	}
	out := dst[:0]
	var ok bool
	if bitIntersects(active, p.accept) {
		out, ok = appendNFAEnd(out, start, maxResults)
	}
	if bitIntersects(active, p.accept) && !ok {
		return out, steps, true
	}
	if maxResults > 0 && len(out) >= maxResults {
		return out, steps, true
	}
	for i := range active {
		active[i] &^= p.dead[i]
	}
	if !bitIntersects(active, p.consumable) {
		return out, steps, false
	}
	end := boundedBackendEnd(data, start, p.maxBytes)
	for pos := start; pos < end; pos++ {
		// 当前字节没有任何可消费源状态时无需清空并遍历整张转移表；
		// 活动集合中若只剩接受/epsilon 状态，后续输入不可能产生新结果。
		if p.sourceCount[data[pos]] == 0 || !bitIntersects(active, p.sourceMask[data[pos]]) {
			break
		}
		clear(next)
		bitApplyTransitions(p, active, next, data[pos])
		steps++
		if nfaBudgetExceeded(steps, maxSteps) {
			return nil, steps, true
		}
		active, next = next, active
		// 转移结果先剔除无法到达接受态的分支，避免在接受检查和下一轮
		// 输入处理之间保留无效位集合。
		for i := range active {
			active[i] &^= p.dead[i]
		}
		if !bitsetAny(active) {
			break
		}
		if bitIntersects(active, p.accept) {
			out, ok = appendNFAEnd(out, pos+1, maxResults)
		}
		if bitIntersects(active, p.accept) && !ok {
			return out, steps, true
		}
		if maxResults > 0 && len(out) >= maxResults {
			return out, steps, true
		}
		if !bitIntersects(active, p.consumable) {
			break
		}
	}
	return out, steps, false
}

func bitRuntimeShapeOK(p *bitNFAProgram) bool {
	if p == nil || p.words <= 0 || p.words > limExStateLimit/64 || p.start < 0 || p.start >= len(p.trans) || len(p.closure) != len(p.trans) || len(p.accept) != p.words || len(p.consumable) != p.words || len(p.exceptional) != p.words || len(p.epsilonOnly) != p.words || len(p.dead) != p.words {
		return false
	}
	if p.minBytes < 0 || p.maxBytes < -1 || p.maxBytes >= 0 && p.maxBytes < p.minBytes {
		return false
	}
	for i := range p.trans {
		if len(p.trans[i]) != 256 || len(p.closure[i]) != p.words {
			return false
		}
		hasTransition := false
		for _, targets := range p.trans[i] {
			if bitAny(targets) {
				hasTransition = true
				break
			}
		}
		bit := uint(i % 64)
		markedEpsilonOnly := p.epsilonOnly[i/64]&(1<<bit) != 0
		markedAccept := p.accept[i/64]&(1<<bit) != 0
		if markedEpsilonOnly != (!hasTransition && !markedAccept) {
			return false
		}
		for _, targets := range p.trans[i] {
			if len(targets) != p.words {
				return false
			}
		}
	}
	for value := range p.sourceMask {
		if len(p.sourceMask[value]) != p.words {
			return false
		}
		count := 0
		for _, word := range p.sourceMask[value] {
			count += bits.OnesCount64(word)
		}
		if int(p.sourceCount[value]) != count {
			return false
		}
		if len(p.sourceStates[value]) != count {
			return false
		}
		for _, state := range p.sourceStates[value] {
			if int(state) >= len(p.trans) || p.sourceMask[value][state/64]&(1<<uint(state%64)) == 0 {
				return false
			}
		}
	}
	if p.deadHash != hashUint64s(p.dead) {
		return false
	}
	if rem := len(p.trans) % 64; rem != 0 {
		mask := ^uint64(0) >> uint(64-rem)
		last := len(p.exceptional) - 1
		if last < 0 || p.accept[last]&^mask != 0 || p.consumable[last]&^mask != 0 || p.exceptional[last]&^mask != 0 || p.epsilonOnly[last]&^mask != 0 || p.dead[last]&^mask != 0 {
			return false
		}
		for _, source := range p.sourceMask {
			if source[len(source)-1]&^mask != 0 {
				return false
			}
		}
	}
	return p.firstMask == firstMaskFromBit(p) && p.acceptsEmpty == bitIntersects(p.closure[p.start], p.accept)
}

func firstMaskFromBit(p *bitNFAProgram) [4]uint64 {
	var mask [4]uint64
	if p == nil || p.start < 0 || p.start >= len(p.closure) {
		return mask
	}
	for b := 0; b < 256; b++ {
		if bitIntersects(p.closure[p.start], p.sourceMask[b]) {
			mask[b/64] |= 1 << uint(b%64)
		}
	}
	return mask
}

func bitsetAny(bits []uint64) bool {
	for _, word := range bits {
		if word != 0 {
			return true
		}
	}
	return false
}

func (p *bitNFAProgram) Spans(data []byte, limit int) []Span {
	if p == nil || limit < 0 || !bitRuntimeShapeOK(p) {
		return nil
	}
	out := make([]Span, 0, initialSpanCapacity(data, limit))
	endsBuf := make([]int, 0, 8)
	forEachNFAStart(data, p.prefix, p.firstMask, p.acceptsEmpty, p.backend, func(start int) bool {
		ends, _, _ := p.matchAtBudgetUnchecked(data, start, 0, 0, endsBuf[:0])
		endsBuf = ends
		for _, end := range ends {
			out = append(out, Span{start, end})
			if limit > 0 && len(out) >= limit {
				return false
			}
		}
		return true
	})
	return out
}
func bitIntersects(a, b []uint64) bool {
	limit := len(a)
	if len(b) < limit {
		limit = len(b)
	}
	for i := 0; i < limit; i++ {
		if a[i]&b[i] != 0 {
			return true
		}
	}
	return false
}

func (p *tableNFAProgram) StateCount() int {
	if p == nil {
		return 0
	}
	return len(p.trans)
}
func (p *tableNFAProgram) EdgeCount() int {
	if p == nil {
		return 0
	}
	n := 0
	for _, row := range p.trans {
		for _, next := range row {
			n += len(next)
		}
	}
	return n
}

func compileTableNFA(g *nfagraph.Graph) *tableNFAProgram {
	if g == nil || g.Validate() != nil {
		return nil
	}
	g = nfagraph.ExpandLiterals(g)
	if !dfaLikeGraph(g) || len(g.Nodes) > shengStateLimit {
		return nil
	}
	ids := g.NodeIDs()
	if uint64(len(ids))*256*8 > maxLayoutMemory {
		return nil
	}
	index := make(map[graph.Vertex]int, len(ids))
	for i, id := range ids {
		index[id] = i
	}
	p := &tableNFAProgram{closure: make([][]int, len(ids)), trans: make([][][]int, len(ids)), transClosure: make([][][]int, len(ids)), accept: make([]bool, len(ids)), dead: make([]bool, len(ids)), firstMask: firstByteMask(g), start: index[g.Start], prefix: requiredLiteralPrefix(g)}
	p.minBytes, p.maxBytes = graphLengthBounds(g)
	words := (len(ids) + 63) / 64
	for b := range p.sourceMask {
		p.sourceMask[b] = make([]uint64, words)
	}
	for i, id := range ids {
		n := g.Nodes[id]
		p.trans[i] = make([][]int, 256)
		p.accept[i] = n.Kind == nfagraph.KindAccept
		for _, to := range g.Flow.Successors(id) {
			j := index[to]
			if n.Kind == nfagraph.KindLiteral {
				if len(n.Literal) != 1 {
					return nil
				}
				p.trans[i][n.Literal[0]] = append(p.trans[i][n.Literal[0]], j)
				p.sourceMask[n.Literal[0]][i/64] |= 1 << uint(i%64)
			} else if n.Kind == nfagraph.KindClass {
				for b := 0; b < 256; b++ {
					if castleMatches(n, byte(b)) {
						p.trans[i][b] = append(p.trans[i][b], j)
						p.sourceMask[b][i/64] |= 1 << uint(i%64)
					}
				}
			}
		}
	}
	for i := range ids {
		p.closure[i] = tableClosure(g, ids, index, i)
	}
	for i := range p.trans {
		p.transClosure[i] = make([][]int, 256)
		for b := range p.trans[i] {
			if len(p.trans[i][b]) > 1 {
				sort.Ints(p.trans[i][b])
				p.trans[i][b] = dedupInts(p.trans[i][b])
			}
			if len(p.trans[i][b]) > 0 {
				p.transClosure[i][b] = mergeClosureTargets(p.closure, p.trans[i][b])
			}
		}
	}
	for value := range p.sourceMask {
		for _, word := range p.sourceMask[value] {
			p.sourceCount[value] += bits.OnesCount64(word)
		}
	}
	p.dead = computeTableDead(p)
	p.acceptsEmpty = tableClosureAccepts(p, p.start)
	if p.EdgeCount() > 1<<18 {
		return nil
	}
	return p
}

func tableClosureAccepts(p *tableNFAProgram, start int) bool {
	if p == nil || start < 0 || start >= len(p.closure) {
		return false
	}
	for _, id := range p.closure[start] {
		if id >= 0 && id < len(p.accept) && p.accept[id] {
			return true
		}
	}
	return false
}

func firstMaskFromTable(p *tableNFAProgram) [4]uint64 {
	var mask [4]uint64
	if p == nil || p.start < 0 || p.start >= len(p.closure) {
		return mask
	}
	for _, id := range p.closure[p.start] {
		for b := 0; b < 256; b++ {
			if id >= 0 && id < len(p.trans) && len(p.trans[id][b]) > 0 {
				mask[b/64] |= 1 << uint(b%64)
			}
		}
	}
	return mask
}

// computeTableDead 基于闭包和转移表反向标记不可达接受状态。
func computeTableDead(p *tableNFAProgram) []bool {
	dead := make([]bool, len(p.trans))
	reachable := make([]bool, len(p.trans))
	for i := range p.trans {
		for _, id := range p.closure[i] {
			if id >= 0 && id < len(p.accept) && p.accept[id] && !reachable[i] {
				reachable[i] = true
			}
		}
	}
	changed := true
	for changed {
		changed = false
		for i := range p.trans {
			if reachable[i] {
				continue
			}
			for value := 0; value < 256 && !reachable[i]; value++ {
				for _, to := range p.trans[i][value] {
					if to >= 0 && to < len(p.trans) {
						for _, closureTarget := range p.closure[to] {
							if reachable[closureTarget] {
								reachable[i] = true
								changed = true
								break
							}
						}
					}
				}
			}
		}
	}
	for i := range dead {
		dead[i] = !reachable[i]
	}
	return dead
}

func tableClosure(g *nfagraph.Graph, ids []graph.Vertex, index map[graph.Vertex]int, start int) []int {
	seen := make([]bool, len(ids))
	q := []int{start}
	seen[start] = true
	for h := 0; h < len(q); h++ {
		id := ids[q[h]]
		n := g.Nodes[id]
		if n.Kind == nfagraph.KindLiteral || n.Kind == nfagraph.KindClass {
			continue
		}
		for _, to := range g.Flow.Successors(id) {
			j := index[to]
			if !seen[j] {
				seen[j] = true
				q = append(q, j)
			}
		}
	}
	sort.Ints(q)
	return q
}

func (p *tableNFAProgram) MatchAt(data []byte, start int) []int {
	ends, _, _ := p.MatchAtBudget(data, start, 0, 0)
	return ends
}

func (p *tableNFAProgram) MatchAtBudget(data []byte, start, maxSteps, maxResults int) ([]int, int, bool) {
	return p.MatchAtBudgetInto(data, start, maxSteps, maxResults, nil)
}

func (p *tableNFAProgram) MatchAtBudgetInto(data []byte, start, maxSteps, maxResults int, dst []int) ([]int, int, bool) {
	if p == nil || start < 0 || start > len(data) || maxSteps < 0 || maxResults < 0 || !tableRuntimeShapeOK(p) {
		return dst[:0], 0, false
	}
	return p.matchAtBudgetUnchecked(data, start, maxSteps, maxResults, dst)
}

func (p *tableNFAProgram) matchAtBudgetUnchecked(data []byte, start, maxSteps, maxResults int, dst []int) ([]int, int, bool) {
	if len(p.prefix) > 0 && !prefixMatchesAt(data, start, p.prefix) {
		return nil, 0, false
	}
	if len(p.prefix) == 0 && start < len(data) && !p.acceptsEmpty && !byteMaskEmpty(p.firstMask) && !hasFirstByte(p.firstMask, data[start]) {
		return nil, 0, false
	}
	if p.minBytes > 0 && len(data)-start < p.minBytes {
		return nil, 0, false
	}
	work := acquireNFAWork(len(p.trans))
	defer releaseNFAWork(work)
	marks := work.marks
	generation := uint32(0)
	active := collectClosuresWithWork(p.closure, []int{p.start}, marks, &generation, work.active)
	active = filterDeadStates(active, p.dead)
	activeBits, nextBits := nfaWorkBits(work, len(p.trans))
	for _, id := range active {
		if id >= 0 && id/64 < len(activeBits) {
			activeBits[id/64] |= 1 << uint(id%64)
		}
	}
	next := work.next
	ends := dst[:0]
	steps := len(active)
	for _, i := range active {
		if p.dead[i] {
			continue
		}
		var ok bool
		if p.accept[i] {
			ends, ok = appendNFAEnd(ends, start, maxResults)
		}
		if p.accept[i] && !ok {
			return ends, steps, true
		}
	}
	if nfaBudgetExceeded(steps, maxSteps) {
		return nil, steps, true
	}
	end := boundedBackendEnd(data, start, p.maxBytes)
	for pos := start; pos < end && len(active) > 0; pos++ {
		generation++
		if generation == 0 {
			clear(marks)
			generation = 1
		}
		next = next[:0]
		clear(nextBits)
		sources := p.sourceMask[data[pos]]
		if p.sourceCount[data[pos]] == 0 || !bitIntersects(activeBits, sources) {
			break
		}
		// 活动状态较密时直接按位图扫描源状态；稀疏场景仍走
		// 状态切片，避免为少量状态付出完整位图遍历成本。
		if len(active) >= len(p.trans)/4 {
			for wi, word := range activeBits {
				if wi >= len(sources) {
					break
				}
				word &= sources[wi]
				for word != 0 {
					bit := bits.TrailingZeros64(word)
					i := wi*64 + bit
					if i < len(p.trans) {
						steps++
						if nfaBudgetExceeded(steps, maxSteps) {
							return nil, steps, true
						}
						for _, j := range p.transClosure[i][data[pos]] {
							if marks[j] != generation {
								marks[j] = generation
								next = append(next, j)
								nextBits[j/64] |= 1 << uint(j%64)
							}
						}
					}
					word &= word - 1
				}
			}
		} else {
			for _, i := range active {
				if p.dead[i] {
					continue
				}
				if i < 0 || i >= len(p.trans) {
					continue
				}
				marked := i/64 < len(sources) && sources[i/64]&(1<<uint(i%64)) != 0
				if !marked {
					continue
				}
				steps++
				if nfaBudgetExceeded(steps, maxSteps) {
					return nil, steps, true
				}
				for _, j := range p.transClosure[i][data[pos]] {
					if marks[j] != generation {
						marks[j] = generation
						next = append(next, j)
					}
				}
			}
		}
		active, next = next, active
		activeBits, nextBits = nextBits, activeBits
		work.next, work.active = next, active
		work.nextBits, work.activeBits = nextBits, activeBits
		for i := range activeBits {
			activeBits[i] = 0
		}
		for _, id := range active {
			if id >= 0 && id/64 < len(activeBits) {
				activeBits[id/64] |= 1 << uint(id%64)
			}
		}
		active = filterDeadStates(active, p.dead)
		steps += len(active)
		if nfaBudgetExceeded(steps, maxSteps) {
			return nil, steps, true
		}
		for _, i := range active {
			if p.dead[i] {
				continue
			}
			var ok bool
			if p.accept[i] {
				ends, ok = appendNFAEnd(ends, pos+1, maxResults)
			}
			if p.accept[i] && !ok {
				return ends, steps, true
			}
		}
	}
	sort.Ints(ends)
	return normalizeNFAEnds(ends, maxResults), steps, false
}

func tableRuntimeShapeOK(p *tableNFAProgram) bool {
	if p == nil || p.start < 0 || p.start >= len(p.trans) || len(p.trans) != len(p.closure) || len(p.trans) != len(p.transClosure) || len(p.trans) != len(p.accept) || len(p.trans) != len(p.dead) {
		return false
	}
	if p.minBytes < 0 || p.maxBytes < -1 || p.maxBytes >= 0 && p.maxBytes < p.minBytes {
		return false
	}
	words := (len(p.trans) + 63) / 64
	for i := range p.trans {
		if p.dead[i] && p.accept[i] {
			return false
		}
		if len(p.trans[i]) != 256 || len(p.transClosure[i]) != 256 || len(p.closure[i]) == 0 {
			return false
		}
		for _, id := range p.closure[i] {
			if id < 0 || id >= len(p.trans) {
				return false
			}
		}
		for j := 1; j < len(p.closure[i]); j++ {
			if p.closure[i][j-1] >= p.closure[i][j] {
				return false
			}
		}
		if !containsInt(p.closure[i], i) {
			return false
		}
		for _, targets := range p.trans[i] {
			for _, id := range targets {
				if id < 0 || id >= len(p.trans) {
					return false
				}
			}
			for j := 1; j < len(targets); j++ {
				if targets[j-1] >= targets[j] {
					return false
				}
			}
		}
		for _, targets := range p.transClosure[i] {
			for j, id := range targets {
				if id < 0 || id >= len(p.trans) || j > 0 && targets[j-1] >= id {
					return false
				}
			}
		}
	}
	for value := range p.sourceMask {
		if len(p.sourceMask[value]) != words {
			return false
		}
		for i := range p.trans {
			has := len(p.trans[i][value]) > 0
			marked := p.sourceMask[value][i/64]&(1<<uint(i%64)) != 0
			if has != marked {
				return false
			}
		}
	}
	if p.firstMask != firstMaskFromTable(p) || p.acceptsEmpty != tableClosureAccepts(p, p.start) {
		return false
	}
	return true
}

func (p *tableNFAProgram) Spans(data []byte, limit int) []Span {
	if p == nil || limit < 0 || !tableRuntimeShapeOK(p) {
		return nil
	}
	out := make([]Span, 0, initialSpanCapacity(data, limit))
	endsBuf := make([]int, 0, 8)
	forEachNFAStart(data, p.prefix, p.firstMask, p.acceptsEmpty, nil, func(start int) bool {
		ends, _, _ := p.matchAtBudgetUnchecked(data, start, 0, 0, endsBuf[:0])
		endsBuf = ends
		for _, end := range ends {
			out = append(out, Span{From: start, To: end})
			if limit > 0 && len(out) >= limit {
				return false
			}
		}
		return true
	})
	return out
}
func tableCloseSet(p *tableNFAProgram, seed []int) []int {
	seen := make([]bool, len(p.closure))
	out := []int{}
	for _, i := range seed {
		for _, j := range p.closure[i] {
			if !seen[j] {
				seen[j] = true
				out = append(out, j)
			}
		}
	}
	return out
}

// byteNFAProgram 使用紧凑字节状态和预计算的空转移执行受限图。
type byteNFAProgram struct {
	states       []byteNFAState
	start        int
	closures     [][]int
	trans        [][][]int
	transClosure [][][]int
	classMask    [][4]uint64
	dead         []bool
	firstMask    [4]uint64
	acceptsEmpty bool
	prefix       []byte
	minBytes     int
	maxBytes     int
}

func (p *byteNFAProgram) StateCount() int {
	if p == nil {
		return 0
	}
	return len(p.states)
}
func (p *byteNFAProgram) EdgeCount() int { return totalByteNFAEdges(p) }

func compileByteNFA(g *nfagraph.Graph) *byteNFAProgram {
	states, start, closures := compileByteStates(g)
	if states == nil {
		return nil
	}
	p := &byteNFAProgram{states: states, start: start, closures: closures, prefix: requiredLiteralPrefix(g), dead: computeByteDead(states), firstMask: firstByteMask(g), classMask: make([][4]uint64, len(states))}
	p.minBytes, p.maxBytes = graphLengthBounds(g)
	if len(p.states) <= 512 {
		p.trans = make([][][]int, len(p.states))
		p.transClosure = make([][][]int, len(p.states))
	}
	for i := range p.states {
		if p.states[i].kind == nfagraph.KindClass {
			for b := 0; b < 256; b++ {
				if castleMatches(p.states[i].class, byte(b)) {
					p.classMask[i][b/64] |= 1 << uint(b%64)
				}
			}
		}
		if p.trans == nil {
			break
		}
		p.trans[i] = make([][]int, 256)
		p.transClosure[i] = make([][]int, 256)
		for b := 0; b < 256; b++ {
			if p.states[i].kind == nfagraph.KindLiteral && p.states[i].lit == byte(b) || p.states[i].kind == nfagraph.KindClass && castleMatches(p.states[i].class, byte(b)) {
				p.trans[i][b] = append([]int(nil), p.states[i].next...)
				p.transClosure[i][b] = mergeClosureTargets(p.closures, p.states[i].next)
			}
		}
	}
	for _, id := range p.closures[p.start] {
		if p.states[id].kind == nfagraph.KindAccept {
			p.acceptsEmpty = true
			break
		}
	}
	return p
}

func computeByteDead(states []byteNFAState) []bool {
	reverse := make([][]int, len(states))
	reachable := make([]bool, len(states))
	queue := make([]int, 0, len(states))
	for i, st := range states {
		if st.kind == nfagraph.KindAccept {
			reachable[i] = true
			queue = append(queue, i)
		}
		for _, to := range append(append([]int(nil), st.next...), st.eps...) {
			if to >= 0 && to < len(states) {
				reverse[to] = append(reverse[to], i)
			}
		}
	}
	for head := 0; head < len(queue); head++ {
		for _, from := range reverse[queue[head]] {
			if !reachable[from] {
				reachable[from] = true
				queue = append(queue, from)
			}
		}
	}
	dead := make([]bool, len(states))
	for i := range dead {
		dead[i] = !reachable[i]
	}
	return dead
}

// compileByteStates 构建所有字节型专用后端共用的图索引和闭包数据，
// 不包含任何具体转移表示，便于各后端独立生成自身状态布局。
func compileByteStates(g *nfagraph.Graph) (states []byteNFAState, start int, closures [][]int) {
	if g == nil || g.Validate() != nil {
		return nil, 0, nil
	}
	g = nfagraph.ExpandLiterals(g)
	if !dfaLikeGraph(g) {
		return nil, 0, nil
	}
	if len(g.Nodes) > sparseStateLimit || g.Flow == nil || g.Flow.EdgeCount() > 16384 {
		return nil, 0, nil
	}
	ids := g.NodeIDs()
	index := make(map[graph.Vertex]int, len(ids))
	for i, id := range ids {
		index[id] = i
	}
	states = make([]byteNFAState, len(ids))
	start = index[g.Start]
	for i, id := range ids {
		n := g.Nodes[id]
		st := byteNFAState{kind: n.Kind}
		if n.Kind == nfagraph.KindLiteral {
			if len(n.Literal) != 1 {
				return nil, 0, nil
			}
			st.lit = n.Literal[0]
		} else if n.Kind == nfagraph.KindClass {
			st.class = n
		}
		for _, to := range g.Flow.Successors(id) {
			j, ok := index[to]
			if !ok {
				return nil, 0, nil
			}
			if n.Kind == nfagraph.KindLiteral || n.Kind == nfagraph.KindClass {
				st.next = append(st.next, j)
			} else {
				st.eps = append(st.eps, j)
			}
		}
		st.next = normalizeStateEdges(st.next)
		st.eps = normalizeStateEdges(st.eps)
		states[i] = st
	}
	if totalByteNFAEdges(&byteNFAProgram{states: states}) > 16384 {
		return nil, 0, nil
	}
	closures = make([][]int, len(states))
	p := &byteNFAProgram{states: states}
	for i := range states {
		closures[i] = p.closure([]int{i})
		if len(closures[i]) > 1<<16 {
			return nil, 0, nil
		}
	}
	return states, start, closures
}

func totalByteNFAEdges(p *byteNFAProgram) int {
	if p == nil {
		return 0
	}
	total := 0
	for _, st := range p.states {
		total += len(st.eps) + len(st.next)
	}
	return total
}

func (p *byteNFAProgram) nextFor(state int, b byte) []int {
	if p == nil || state < 0 || state >= len(p.states) {
		return nil
	}
	if p.trans != nil && state < len(p.trans) {
		return p.trans[state][b]
	}
	st := p.states[state]
	if st.kind == nfagraph.KindLiteral && st.lit == b || st.kind == nfagraph.KindClass && p.classMask[state][b/64]&(1<<uint(b%64)) != 0 {
		return st.next
	}
	return nil
}

func (p *byteNFAProgram) closure(seed []int) []int {
	seen := make([]bool, len(p.states))
	q := append([]int(nil), seed...)
	for _, i := range q {
		if i >= 0 && i < len(seen) {
			seen[i] = true
		}
	}
	for h := 0; h < len(q); h++ {
		i := q[h]
		if i < 0 || i >= len(p.states) {
			continue
		}
		for _, j := range p.states[i].eps {
			if j >= 0 && j < len(seen) && !seen[j] {
				seen[j] = true
				q = append(q, j)
			}
		}
	}
	sort.Ints(q)
	return q
}

func (p *byteNFAProgram) cachedClosure(seed []int) []int {
	if len(seed) == 1 && seed[0] >= 0 && seed[0] < len(p.closures) && p.closures[seed[0]] != nil {
		return append([]int(nil), p.closures[seed[0]]...)
	}
	return p.closure(seed)
}

func (p *byteNFAProgram) closureSet(seed []int) []int {
	if len(seed) == 0 {
		return nil
	}
	seen := make([]bool, len(p.states))
	out := make([]int, 0, len(seed))
	for _, id := range seed {
		for _, v := range p.cachedClosure([]int{id}) {
			if !seen[v] {
				seen[v] = true
				out = append(out, v)
			}
		}
	}
	return out
}

// closureSetWithWork 计算 epsilon 闭包并复用调用方提供的标记和结果缓冲区。
// 该路径用于单次扫描热循环，避免每个输入字节重复分配布尔数组。
func (p *byteNFAProgram) closureSetWithWork(seed []int, marks []uint32, generation *uint32, out []int) []int {
	if p == nil || len(seed) == 0 {
		return out[:0]
	}
	*generation++
	if *generation == 0 {
		clear(marks)
		*generation = 1
	}
	out = out[:0]
	for _, id := range seed {
		if id < 0 || id >= len(p.closures) {
			continue
		}
		for _, target := range p.closures[id] {
			if target < 0 || target >= len(marks) {
				continue
			}
			if marks[target] == *generation {
				continue
			}
			marks[target] = *generation
			out = append(out, target)
		}
	}
	return out
}

func (p *byteNFAProgram) MatchAt(data []byte, start int) []int {
	if p == nil || start < 0 || start > len(data) || !byteRuntimeShapeOK(p) {
		return nil
	}
	return p.matchAtUnchecked(data, start)
}

func (p *byteNFAProgram) matchAtUnchecked(data []byte, start int) []int {
	if p.minBytes > 0 && len(data)-start < p.minBytes {
		return nil
	}
	if len(p.prefix) == 0 && start < len(data) && !p.acceptsEmpty && !byteMaskEmpty(p.firstMask) && !hasFirstByte(p.firstMask, data[start]) {
		return nil
	}
	if len(p.prefix) > 0 && !prefixMatchesAt(data, start, p.prefix) {
		return nil
	}
	work := acquireNFAWork(len(p.states))
	defer releaseNFAWork(work)
	marks := work.marks
	generation := uint32(0)
	activeBuf := work.active
	active := p.closureSetWithWork([]int{p.start}, marks, &generation, activeBuf)
	out := make([]int, 0, 2)
	next := work.next
	for _, i := range active {
		if p.dead[i] {
			continue
		}
		if p.states[i].kind == nfagraph.KindAccept {
			out = append(out, start)
		}
	}
	end := boundedBackendEnd(data, start, p.maxBytes)
	for pos := start; pos < end && len(active) > 0; pos++ {
		next = next[:0]
		generation++
		if generation == 0 {
			clear(marks)
			generation = 1
		}
		for _, i := range active {
			if p.dead[i] {
				continue
			}
			st := p.states[i]
			if st.kind != nfagraph.KindLiteral && st.kind != nfagraph.KindClass {
				continue
			}
			transitions := p.nextFor(i, data[pos])
			if p.transClosure != nil {
				transitions = p.transClosure[i][data[pos]]
			}
			if len(transitions) > 0 {
				for _, j := range transitions {
					if marks[j] != generation {
						marks[j] = generation
						next = append(next, j)
					}
				}
			}
		}
		if p.transClosure != nil {
			active, next = next, active
		} else {
			active, activeBuf = p.closureSetWithWork(next, marks, &generation, activeBuf), active
			next = activeBuf
		}
		for _, i := range active {
			if p.states[i].kind == nfagraph.KindAccept {
				out = append(out, pos+1)
			}
		}
	}
	sort.Ints(out)
	return dedupInts(out)
}

func (p *byteNFAProgram) MatchAtBudget(data []byte, start, maxSteps, maxResults int) ([]int, int, bool) {
	if p == nil || start < 0 || start > len(data) || maxSteps < 0 || maxResults < 0 || !byteRuntimeShapeOK(p) {
		return nil, 0, false
	}
	return p.matchAtBudgetUnchecked(data, start, maxSteps, maxResults, nil)
}

func (p *byteNFAProgram) matchAtBudgetUnchecked(data []byte, start, maxSteps, maxResults int, dst []int) ([]int, int, bool) {
	if p.minBytes > 0 && len(data)-start < p.minBytes {
		return nil, 0, false
	}
	if len(p.prefix) == 0 && start < len(data) && !p.acceptsEmpty && !byteMaskEmpty(p.firstMask) && !hasFirstByte(p.firstMask, data[start]) {
		return nil, 0, false
	}
	if len(p.prefix) > 0 && !prefixMatchesAt(data, start, p.prefix) {
		return nil, 0, false
	}
	work := acquireNFAWork(len(p.states))
	defer releaseNFAWork(work)
	marks := work.marks
	generation := uint32(0)
	activeBuf := work.active
	active := p.closureSetWithWork([]int{p.start}, marks, &generation, activeBuf)
	steps := len(active)
	if nfaBudgetExceeded(steps, maxSteps) {
		return nil, steps, true
	}
	out := dst[:0]
	next := work.next
	for _, i := range active {
		if p.dead[i] {
			continue
		}
		var ok bool
		if p.states[i].kind == nfagraph.KindAccept {
			out, ok = appendNFAEnd(out, start, maxResults)
		}
		if p.states[i].kind == nfagraph.KindAccept && !ok {
			return out, steps, true
		}
	}
	end := boundedBackendEnd(data, start, p.maxBytes)
	for pos := start; pos < end && len(active) > 0; pos++ {
		next = next[:0]
		generation++
		if generation == 0 {
			clear(marks)
			generation = 1
		}
		for _, i := range active {
			if p.dead[i] {
				continue
			}
			steps++
			if nfaBudgetExceeded(steps, maxSteps) {
				return nil, steps, true
			}
			st := p.states[i]
			if st.kind != nfagraph.KindLiteral && st.kind != nfagraph.KindClass {
				continue
			}
			transitions := p.nextFor(i, data[pos])
			if p.transClosure != nil {
				transitions = p.transClosure[i][data[pos]]
			}
			if len(transitions) > 0 {
				for _, j := range transitions {
					if marks[j] != generation {
						marks[j] = generation
						next = append(next, j)
					}
				}
			}
		}
		if p.transClosure != nil {
			active, next = next, active
		} else {
			active, activeBuf = p.closureSetWithWork(next, marks, &generation, activeBuf), active
			next = activeBuf
		}
		steps += len(active)
		if nfaBudgetExceeded(steps, maxSteps) {
			return nil, steps, true
		}
		for _, i := range active {
			if p.dead[i] {
				continue
			}
			var ok bool
			if p.states[i].kind == nfagraph.KindAccept {
				out, ok = appendNFAEnd(out, pos+1, maxResults)
			}
			if p.states[i].kind == nfagraph.KindAccept && !ok {
				return out, steps, true
			}
		}
	}
	sort.Ints(out)
	return dedupInts(out), steps, false
}

func byteRuntimeShapeOK(p *byteNFAProgram) bool {
	if p == nil || p.start < 0 || p.start >= len(p.states) || len(p.closures) != len(p.states) || len(p.dead) != len(p.states) {
		return false
	}
	if p.minBytes < 0 || p.maxBytes < -1 || p.maxBytes >= 0 && p.maxBytes < p.minBytes {
		return false
	}
	if len(p.classMask) != len(p.states) {
		return false
	}
	if p.trans != nil && len(p.trans) != len(p.states) {
		return false
	}
	for i, st := range p.states {
		if p.dead[i] && st.kind == nfagraph.KindAccept {
			return false
		}
		if len(p.closures[i]) == 0 {
			return false
		}
		for _, id := range p.closures[i] {
			if id < 0 || id >= len(p.states) {
				return false
			}
		}
		for _, id := range st.next {
			if id < 0 || id >= len(p.states) {
				return false
			}
		}
		for _, id := range st.eps {
			if id < 0 || id >= len(p.states) {
				return false
			}
		}
		if p.trans != nil && (len(p.trans[i]) != 256 || len(p.transClosure[i]) != 256) {
			return false
		}
		if p.trans != nil {
			for value, targets := range p.trans[i] {
				for _, id := range targets {
					if id < 0 || id >= len(p.states) {
						return false
					}
				}
				if !equalInts(p.transClosure[i][value], mergeClosureTargets(p.closures, targets)) {
					return false
				}
			}
		}
	}
	if !equalBools(p.dead, computeByteDead(p.states)) {
		return false
	}
	if p.firstMask != firstMaskFromByte(p) || p.acceptsEmpty != byteClosureAccepts(p) {
		return false
	}
	return true
}

func byteClosureAccepts(p *byteNFAProgram) bool {
	if p == nil || p.start < 0 || p.start >= len(p.closures) {
		return false
	}
	for _, id := range p.closures[p.start] {
		if id >= 0 && id < len(p.states) && p.states[id].kind == nfagraph.KindAccept {
			return true
		}
	}
	return false
}

func firstMaskFromByte(p *byteNFAProgram) [4]uint64 {
	var mask [4]uint64
	if p == nil || p.start < 0 || p.start >= len(p.closures) {
		return mask
	}
	for _, id := range p.closures[p.start] {
		if id < 0 || id >= len(p.states) {
			continue
		}
		st := p.states[id]
		if (st.kind != nfagraph.KindLiteral && st.kind != nfagraph.KindClass) || len(st.next) == 0 {
			continue
		}
		for b := 0; b < 256; b++ {
			if (st.kind == nfagraph.KindLiteral && st.lit == byte(b)) || (st.kind == nfagraph.KindClass && castleMatches(st.class, byte(b))) {
				mask[b/64] |= 1 << uint(b%64)
			}
		}
	}
	return mask
}

func (p *byteNFAProgram) Spans(data []byte, limit int) []Span {
	if p == nil || limit < 0 || !byteRuntimeShapeOK(p) {
		return nil
	}
	out := make([]Span, 0, initialSpanCapacity(data, limit))
	// 布局在进入整块扫描前已校验，后续起点直接复用确认路径，
	// 避免每个起点重复遍历整张转移表。
	forEachNFAStart(data, p.prefix, p.firstMask, p.acceptsEmpty, dispatch.DefaultBackend(), func(start int) bool {
		for _, end := range p.matchAtUnchecked(data, start) {
			out = append(out, Span{From: start, To: end})
			if limit > 0 && len(out) >= limit {
				return false
			}
		}
		return true
	})
	return out
}

// mpvProgram 保存仅由文字分支组成的多路径验证程序。
type mpvProgram struct {
	literals [][]byte
	prefix   []byte
}

func (p *mpvProgram) MatchAt(data []byte, start int) []int {
	return p.MatchAtInto(data, start, nil)
}

// MatchAtInto 将文字分支命中写入复用缓冲，避免逐起点分配结束偏移切片。
func (p *mpvProgram) MatchAtInto(data []byte, start int, dst []int) []int {
	if p == nil || start < 0 || start > len(data) || p.validate() != nil {
		return dst[:0]
	}
	if len(p.prefix) > 0 && !prefixMatchesAt(data, start, p.prefix) {
		return dst[:0]
	}
	ends := dst[:0]
	for _, lit := range p.literals {
		if len(lit) <= len(data)-start && bytes.Equal(data[start:start+len(lit)], lit) {
			end := start + len(lit)
			if len(ends) == 0 || ends[len(ends)-1] != end {
				ends = append(ends, end)
			}
		}
	}
	// literals 已在编译阶段按长度、字节序排列且禁止重复，
	// 因此结束偏移天然有序，不需要运行时排序或哈希去重。
	return ends
}

func (p *mpvProgram) validate() error {
	if p == nil || len(p.literals) == 0 {
		return fmt.Errorf("invalid mpv program")
	}
	for i, lit := range p.literals {
		if len(lit) == 0 {
			return fmt.Errorf("mpv literal must not be empty")
		}
		// 文字分支通常很少，使用两两比较避免每次确认都创建
		// map 和 string 临时对象，同时覆盖非相邻的重复分支。
		for j := 0; j < i; j++ {
			if bytes.Equal(p.literals[j], lit) {
				return fmt.Errorf("duplicate mpv literal")
			}
		}
	}
	if !commonLiteralPrefixEqual(p.prefix, p.literals) {
		return fmt.Errorf("mpv prefix mismatch")
	}
	return nil
}

// commonLiteralPrefixEqual 在不创建临时前缀的情况下校验公共前缀。
// 该检查位于每个起点的确认热路径，避免重复分配短字节切片。
func commonLiteralPrefixEqual(prefix []byte, literals [][]byte) bool {
	if len(literals) == 0 {
		return len(prefix) == 0
	}
	n := len(literals[0])
	for _, lit := range literals[1:] {
		if len(lit) < n {
			n = len(lit)
		}
	}
	for i := 0; i < n; i++ {
		for _, lit := range literals[1:] {
			if lit[i] != literals[0][i] {
				n = i
				break
			}
		}
		if i == n {
			break
		}
	}
	return len(prefix) == n && (n == 0 || bytes.Equal(prefix, literals[0][:n]))
}

func commonLiteralPrefix(literals [][]byte) []byte {
	if len(literals) == 0 {
		return nil
	}
	n := len(literals[0])
	for _, lit := range literals[1:] {
		if len(lit) < n {
			n = len(lit)
		}
	}
	for i := 0; i < n; i++ {
		for _, lit := range literals[1:] {
			if lit[i] != literals[0][i] {
				return append([]byte(nil), literals[0][:i]...)
			}
		}
	}
	return append([]byte(nil), literals[0][:n]...)
}

func (p *mpvProgram) MatchAtBudget(data []byte, start, maxSteps, maxResults int) ([]int, int, bool) {
	return p.MatchAtBudgetInto(data, start, maxSteps, maxResults, nil)
}

// MatchAtBudgetInto 在预算内执行文字分支确认，并复用结束偏移缓冲。
func (p *mpvProgram) MatchAtBudgetInto(data []byte, start, maxSteps, maxResults int, dst []int) ([]int, int, bool) {
	if p == nil || start < 0 || start > len(data) || maxSteps < 0 || maxResults < 0 || p.validate() != nil {
		return dst[:0], 0, false
	}
	if len(p.prefix) > 0 && !prefixMatchesAt(data, start, p.prefix) {
		return dst[:0], 0, false
	}
	ends := dst[:0]
	steps := 0
	for _, lit := range p.literals {
		steps++
		if nfaBudgetExceeded(steps, maxSteps) {
			return orderedEnds(ends), steps, true
		}
		if len(lit) <= len(data)-start && bytes.Equal(data[start:start+len(lit)], lit) {
			end := start + len(lit)
			if len(ends) == 0 || ends[len(ends)-1] != end {
				ends = append(ends, end)
			}
			if maxResults > 0 && len(ends) >= maxResults {
				return ends, steps, true
			}
		}
	}
	return normalizeNFAEnds(ends, maxResults), steps, false
}

func (p *mpvProgram) Spans(data []byte, limit int) []Span {
	if p == nil || limit < 0 {
		return nil
	}
	out := make([]Span, 0, initialSpanCapacity(data, limit))
	ends := make([]int, 0, len(p.literals))
	forEachPrefixStart(data, p.prefix, func(start int) bool {
		ends = p.MatchAtInto(data, start, ends[:0])
		for _, end := range ends {
			out = append(out, Span{From: start, To: end})
			if limit > 0 && len(out) >= limit {
				return false
			}
		}
		return true
	})
	return out
}

func dedupInts(in []int) []int {
	if len(in) < 2 {
		return in
	}
	out := in[:1]
	for _, v := range in[1:] {
		if v != out[len(out)-1] {
			out = append(out, v)
		}
	}
	return out
}

func appendNFAEnd(ends []int, pos, maxResults int) ([]int, bool) {
	if len(ends) > 0 && ends[len(ends)-1] == pos {
		return ends, true
	}
	if maxResults > 0 && len(ends) >= maxResults {
		return ends, false
	}
	return append(ends, pos), true
}

// normalizeNFAEnds 统一结束偏移的排序、去重和结果上限处理。
func normalizeNFAEnds(ends []int, maxResults int) []int {
	if len(ends) == 0 {
		return ends
	}
	sort.Ints(ends)
	ends = dedupInts(ends)
	if maxResults > 0 && len(ends) > maxResults {
		ends = ends[:maxResults]
	}
	return ends
}

// nfaBudgetExceeded 统一步骤预算判断，零值表示不限制。
func nfaBudgetExceeded(steps, maxSteps int) bool {
	return maxSteps > 0 && steps > maxSteps
}

// nfaBudgetEnds 在预算停止时保留已经确认的有序结果。
func nfaBudgetEnds(ends []int, maxResults int) []int {
	return normalizeNFAEnds(ends, maxResults)
}

// prefixMatchesAt 使用差值判断避免起点与前缀长度相加时溢出。
func prefixMatchesAt(data []byte, start int, prefix []byte) bool {
	if start < 0 || start > len(data) || len(prefix) > len(data)-start {
		return false
	}
	return len(prefix) == 0 || bytes.Equal(data[start:start+len(prefix)], prefix)
}

// collectClosuresWithWork 合并预计算闭包并复用标记缓冲区。
func collectClosuresWithWork(closures [][]int, seed []int, marks []uint32, generation *uint32, out []int) []int {
	*generation++
	if *generation == 0 {
		clear(marks)
		*generation = 1
	}
	out = out[:0]
	for _, id := range seed {
		if id < 0 || id >= len(closures) {
			continue
		}
		for _, target := range closures[id] {
			if target < 0 || target >= len(marks) {
				continue
			}
			if marks[target] == *generation {
				continue
			}
			marks[target] = *generation
			out = append(out, target)
		}
	}
	return out
}

// goughProgram 通过从接受节点反向传播活动状态确认匹配起点。
type goughProgram struct {
	graph        *nfagraph.Graph
	reverseTable map[graph.Vertex][]graph.Vertex
	index        map[graph.Vertex]int
	vertices     []graph.Vertex
	acceptNodes  []graph.Vertex
	predecessors map[graph.Vertex][]graph.Vertex
	words        int
	acceptMask   []uint64
	deadMask     []uint64
	reverseMask  [][]uint64
	predMask     [][]uint64
	consumeMask  [256][]uint64
	firstMask    [4]uint64
	acceptsEmpty bool
	minBytes     int
	maxBytes     int
	prefix       []byte
}

type goughWork struct {
	active []uint64
	next   []uint64
}

var goughWorkPool sync.Pool

func acquireGoughWork(words int) *goughWork {
	w, _ := goughWorkPool.Get().(*goughWork)
	if w == nil {
		w = &goughWork{}
	}
	if cap(w.active) < words {
		w.active = make([]uint64, words)
	} else {
		w.active = w.active[:words]
		clear(w.active)
	}
	if cap(w.next) < words {
		w.next = make([]uint64, words)
	} else {
		w.next = w.next[:words]
		clear(w.next)
	}
	return w
}

func releaseGoughWork(w *goughWork) {
	if w == nil {
		return
	}
	w.active = w.active[:0]
	w.next = w.next[:0]
	if cap(w.active) > 1<<16 || cap(w.next) > 1<<16 {
		w.active, w.next = nil, nil
	}
	goughWorkPool.Put(w)
}

func newGoughProgram(g *nfagraph.Graph) *goughProgram {
	if g == nil || g.Validate() != nil || g.Flow == nil || len(g.Nodes) > 4096 || g.Flow.EdgeCount() > 16384 {
		return nil
	}
	words := (len(g.Nodes) + 63) / 64
	if uint64(len(g.Nodes))*64+uint64(g.Flow.EdgeCount())*8+uint64(len(g.Nodes))*uint64(words)*16 > maxLayoutMemory {
		return nil
	}
	minBytes, maxBytes := graphLengthBounds(g)
	p := &goughProgram{
		graph:        g,
		reverseTable: make(map[graph.Vertex][]graph.Vertex, len(g.Nodes)),
		index:        make(map[graph.Vertex]int, len(g.Nodes)),
		acceptNodes:  append([]graph.Vertex(nil), g.Accepts()...),
		predecessors: make(map[graph.Vertex][]graph.Vertex, len(g.Nodes)),
		prefix:       requiredLiteralPrefix(g),
		firstMask:    firstByteMask(g),
		acceptsEmpty: graphAcceptsEmpty(g),
		minBytes:     minBytes,
		maxBytes:     maxBytes,
	}
	vertices := g.NodeIDs()
	p.vertices = append([]graph.Vertex(nil), vertices...)
	for i, v := range vertices {
		p.index[v] = i
		p.reverseTable[v] = p.computeReverseClosure(v)
		p.predecessors[v] = append([]graph.Vertex(nil), g.Predecessors(v)...)
		sort.Slice(p.predecessors[v], func(a, b int) bool { return p.predecessors[v][a] < p.predecessors[v][b] })
		if len(p.predecessors[v]) > 1 {
			uniq := p.predecessors[v][:1]
			for _, previous := range p.predecessors[v][1:] {
				if previous != uniq[len(uniq)-1] {
					uniq = append(uniq, previous)
				}
			}
			p.predecessors[v] = uniq
		}
	}
	p.words = (len(p.index) + 63) / 64
	p.acceptMask = make([]uint64, p.words)
	p.deadMask = make([]uint64, p.words)
	dead := computeGraphDead(g)
	p.reverseMask = make([][]uint64, len(p.index))
	p.predMask = make([][]uint64, len(p.index))
	for value := range p.consumeMask {
		p.consumeMask[value] = make([]uint64, p.words)
	}
	for id, index := range p.index {
		if g.IsAccept(id) {
			p.acceptMask[index/64] |= 1 << uint(index%64)
		}
		if dead[id] {
			p.deadMask[index/64] |= 1 << uint(index%64)
		}
		p.reverseMask[index] = make([]uint64, p.words)
		for _, item := range p.reverseTable[id] {
			if target, ok := p.index[item]; ok {
				p.reverseMask[index][target/64] |= 1 << uint(target%64)
			}
		}
		p.predMask[index] = make([]uint64, p.words)
		for _, item := range p.predecessors[id] {
			if target, ok := p.index[item]; ok {
				p.predMask[index][target/64] |= 1 << uint(target%64)
			}
		}
		n := g.Nodes[id]
		if n != nil && g.Flow != nil && len(g.Flow.Successors(id)) > 0 && (n.Kind == nfagraph.KindLiteral && len(n.Literal) == 1 || n.Kind == nfagraph.KindClass) {
			for value := 0; value < 256; value++ {
				if (n.Kind == nfagraph.KindLiteral && n.Literal[0] == byte(value)) || n.Kind == nfagraph.KindClass && castleMatches(n, byte(value)) {
					p.consumeMask[value][index/64] |= 1 << uint(index%64)
				}
			}
		}
	}
	return p
}

func (p *goughProgram) MatchAt(data []byte, start int) []int {
	if p == nil || p.graph == nil || start < 0 || start > len(data) || !goughRuntimeShapeOK(p) {
		return nil
	}
	if len(p.prefix) == 0 && start < len(data) && !p.acceptsEmpty && !byteMaskEmpty(p.firstMask) && !hasFirstByte(p.firstMask, data[start]) {
		return nil
	}
	if len(p.prefix) > 0 && !prefixMatchesAt(data, start, p.prefix) {
		return nil
	}
	out := make([]int, 0, 2)
	if p.minBytes > len(data)-start {
		return out
	}
	firstEnd := start + p.minBytes
	if len(p.prefix) > len(data)-start {
		return out
	}
	prefixEnd := start + len(p.prefix)
	if firstEnd < prefixEnd {
		firstEnd = prefixEnd
	}
	if firstEnd > len(data) {
		return out
	}
	lastEnd := boundedBackendEnd(data, start, p.maxBytes)
	for end := firstEnd; end <= lastEnd; end++ {
		if p.accepts(data, start, end) {
			out = append(out, end)
		}
	}
	return out
}

// MatchAtBudget 在步骤和结果预算内执行反向确认。
func (p *goughProgram) MatchAtBudget(data []byte, start, maxSteps, maxResults int) ([]int, int, bool) {
	if p == nil || p.graph == nil || start < 0 || start > len(data) || maxSteps < 0 || maxResults < 0 || !goughRuntimeShapeOK(p) {
		return nil, 0, false
	}
	return p.matchAtBudgetUnchecked(data, start, maxSteps, maxResults)
}

func (p *goughProgram) matchAtBudgetUnchecked(data []byte, start, maxSteps, maxResults int) ([]int, int, bool) {
	return p.matchAtBudgetUncheckedInto(data, start, maxSteps, maxResults, nil)
}

func (p *goughProgram) matchAtBudgetUncheckedInto(data []byte, start, maxSteps, maxResults int, dst []int) ([]int, int, bool) {
	if len(p.prefix) == 0 && start < len(data) && !p.acceptsEmpty && !byteMaskEmpty(p.firstMask) && !hasFirstByte(p.firstMask, data[start]) {
		return nil, 0, false
	}
	if len(p.prefix) > 0 && !prefixMatchesAt(data, start, p.prefix) {
		return nil, 0, false
	}
	ends := dst[:0]
	steps := 0
	// 预算模式保留逐结束位置计步语义；但超过有限最大长度的结束位置
	// 不可能接受，直接裁剪可避免无意义的反向确认。
	lastEnd := boundedBackendEnd(data, start, p.maxBytes)
	firstEnd := start
	if p.minBytes <= len(data)-start {
		firstEnd += p.minBytes
	} else {
		firstEnd = lastEnd + 1
	}
	if len(p.prefix) <= len(data)-start {
		if prefixEnd := start + len(p.prefix); firstEnd < prefixEnd {
			firstEnd = prefixEnd
		}
	} else {
		firstEnd = lastEnd + 1
	}
	for end := start; end <= lastEnd; end++ {
		steps++
		if nfaBudgetExceeded(steps, maxSteps) {
			if len(ends) == 0 {
				return nil, steps, true
			}
			return ends, steps, true
		}
		if end < firstEnd {
			continue
		}
		if p.accepts(data, start, end) {
			if maxResults > 0 && len(ends) >= maxResults {
				return ends, steps, true
			}
			ends = append(ends, end)
			if maxResults > 0 && len(ends) >= maxResults {
				return ends, steps, true
			}
		}
	}
	return nfaBudgetEnds(ends, maxResults), steps, false
}

func goughRuntimeShapeOK(p *goughProgram) bool {
	if p == nil || p.graph == nil || p.graph.Flow == nil || len(p.index) != len(p.graph.Nodes) || len(p.vertices) != len(p.index) || len(p.reverseTable) != len(p.index) || len(p.predecessors) != len(p.index) || p.words <= 0 || len(p.acceptMask) != p.words || len(p.deadMask) != p.words || len(p.reverseMask) != len(p.index) || len(p.predMask) != len(p.index) {
		return false
	}
	for value := range p.consumeMask {
		if len(p.consumeMask[value]) != p.words {
			return false
		}
	}
	dead := computeGraphDead(p.graph)
	for id, index := range p.index {
		if (p.deadMask[index/64]&(1<<uint(index%64)) != 0) != dead[id] {
			return false
		}
	}
	for i, id := range p.vertices {
		if p.graph.Nodes[id] == nil || p.index[id] != i || len(p.reverseMask[i]) != p.words || len(p.predMask[i]) != p.words {
			return false
		}
		acceptMarked := p.acceptMask[i/64]&(1<<uint(i%64)) != 0
		if acceptMarked != p.graph.IsAccept(id) {
			return false
		}
		for _, target := range p.reverseTable[id] {
			if p.graph.Nodes[target] == nil {
				return false
			}
		}
		found := false
		for j, target := range p.reverseTable[id] {
			if target == id {
				found = true
			}
			if j > 0 && p.reverseTable[id][j-1] >= target {
				return false
			}
		}
		if !found {
			return false
		}
		n := p.graph.Nodes[id]
		for value := 0; value < 256; value++ {
			want := false
			if len(p.graph.Flow.Successors(id)) > 0 && n.Kind == nfagraph.KindLiteral && len(n.Literal) == 1 {
				want = n.Literal[0] == byte(value)
			} else if len(p.graph.Flow.Successors(id)) > 0 && n.Kind == nfagraph.KindClass {
				want = castleMatches(n, byte(value))
			}
			got := p.consumeMask[value][i/64]&(1<<uint(i%64)) != 0
			if got != want {
				return false
			}
		}
		for _, target := range p.predecessors[id] {
			if p.graph.Nodes[target] == nil {
				return false
			}
		}
	}
	if len(p.acceptNodes) == 0 {
		return false
	}
	for _, id := range p.acceptNodes {
		if p.graph.Nodes[id] == nil {
			return false
		}
	}
	minBytes, maxBytes := graphLengthBounds(p.graph)
	if p.minBytes != minBytes || p.maxBytes != maxBytes {
		return false
	}
	if !bytes.Equal(p.prefix, requiredLiteralPrefix(p.graph)) {
		return false
	}
	if p.firstMask != firstByteMask(p.graph) || p.acceptsEmpty != graphAcceptsEmpty(p.graph) {
		return false
	}
	if p.graph.IsAccept(p.graph.Start) {
		// 接受起点时必须保留空匹配；反向表至少要覆盖起点自身。
		start, ok := p.index[p.graph.Start]
		if !ok || p.acceptMask[start/64]&(1<<uint(start%64)) == 0 {
			return false
		}
	}
	return true
}

func (p *goughProgram) Spans(data []byte, limit int) []Span {
	if p == nil || limit < 0 || !goughRuntimeShapeOK(p) {
		return nil
	}
	out := make([]Span, 0, initialSpanCapacity(data, limit))
	endsBuf := make([]int, 0, 2)
	forEachNFAStart(data, p.prefix, p.firstMask, p.acceptsEmpty, nil, func(start int) bool {
		ends, _, _ := p.matchAtBudgetUncheckedInto(data, start, 0, 0, endsBuf[:0])
		endsBuf = ends
		for _, end := range ends {
			out = append(out, Span{From: start, To: end})
			if limit > 0 && len(out) >= limit {
				return false
			}
		}
		return true
	})
	return out
}

func (p *goughProgram) accepts(data []byte, start, end int) bool {
	if p != nil && p.words > 0 && len(p.reverseMask) == len(p.index) && len(p.predMask) == len(p.index) {
		return p.acceptsBit(data, start, end)
	}
	active := make(map[graph.Vertex]struct{})
	for _, id := range p.acceptNodes {
		active[id] = struct{}{}
	}
	active = p.reverseClosure(active)
	for pos := end; pos > start; pos-- {
		next := make(map[graph.Vertex]struct{})
		for id := range active {
			node := p.graph.Nodes[id]
			if node == nil || (node.Kind != nfagraph.KindLiteral && node.Kind != nfagraph.KindClass) {
				continue
			}
			if node.Kind == nfagraph.KindLiteral {
				if len(node.Literal) != 1 || node.Literal[0] != data[pos-1] {
					continue
				}
			} else if !castleMatches(node, data[pos-1]) {
				continue
			}
			for _, previous := range p.predecessors[id] {
				next[previous] = struct{}{}
			}
		}
		active = p.reverseClosure(next)
		if len(active) == 0 {
			return false
		}
	}
	_, ok := active[p.graph.Start]
	return ok
}

func (p *goughProgram) acceptsBit(data []byte, start, end int) bool {
	work := acquireGoughWork(p.words)
	defer releaseGoughWork(work)
	active, next := work.active, work.next
	for wi, word := range p.acceptMask {
		for word != 0 {
			bit := bits.TrailingZeros64(word)
			index := wi*64 + bit
			if index < len(p.reverseMask) {
				bitOr(active, p.reverseMask[index])
			}
			word &= word - 1
		}
	}
	for i := range active {
		active[i] &^= p.deadMask[i]
	}
	for pos := end; pos > start; pos-- {
		clear(next)
		consume := p.consumeMask[data[pos-1]]
		for wi, word := range active {
			if wi < len(consume) {
				word &= consume[wi]
			}
			for word != 0 {
				bit := bits.TrailingZeros64(word)
				index := wi*64 + bit
				if index < len(p.predMask) {
					// consumeMask 已按当前字节筛选状态，直接传播前驱位集合。
					bitOr(next, p.predMask[index])
				}
				word &= word - 1
			}
		}
		for i := range active {
			active[i] = next[i]
		}
		clear(next)
		p.reverseClosureBitsInto(active, next)
		active, next = next, active
		for i := range active {
			active[i] &^= p.deadMask[i]
		}
		if !bitsetAny(active) {
			return false
		}
	}
	startIndex, ok := p.index[p.graph.Start]
	return ok && startIndex/64 < len(active) && active[startIndex/64]&(1<<uint(startIndex%64)) != 0
}

func (p *goughProgram) vertexAt(index int) graph.Vertex {
	if index >= 0 && index < len(p.vertices) {
		return p.vertices[index]
	}
	return 0
}

func (p *goughProgram) reverseClosureBits(seed []uint64) []uint64 {
	out := make([]uint64, p.words)
	p.reverseClosureBitsInto(seed, out)
	return out
}

func (p *goughProgram) reverseClosureBitsInto(seed, out []uint64) {
	clear(out)
	for wi, word := range seed {
		for word != 0 {
			bit := bits.TrailingZeros64(word)
			index := wi*64 + bit
			if index < len(p.reverseMask) {
				bitOr(out, p.reverseMask[index])
			}
			word &= word - 1
		}
	}
}

func (p *goughProgram) reverseClosure(seed map[graph.Vertex]struct{}) map[graph.Vertex]struct{} {
	if len(p.reverseTable) > 0 {
		active := make(map[graph.Vertex]struct{}, len(seed))
		for id := range seed {
			for _, item := range p.reverseTable[id] {
				active[item] = struct{}{}
			}
		}
		return active
	}
	active := make(map[graph.Vertex]struct{}, len(seed))
	queue := make([]graph.Vertex, 0, len(seed))
	for id := range seed {
		active[id] = struct{}{}
		queue = append(queue, id)
	}
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		node := p.graph.Nodes[id]
		if node != nil && (node.Kind == nfagraph.KindLiteral || node.Kind == nfagraph.KindClass) {
			continue
		}
		for _, previous := range p.predecessors[id] {
			if _, ok := active[previous]; ok {
				continue
			}
			active[previous] = struct{}{}
			queue = append(queue, previous)
		}
	}
	return active
}

func (p *goughProgram) computeReverseClosure(seed graph.Vertex) []graph.Vertex {
	seen := make(map[graph.Vertex]struct{})
	queue := []graph.Vertex{seed}
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		node := p.graph.Nodes[id]
		if node != nil && (node.Kind == nfagraph.KindLiteral || node.Kind == nfagraph.KindClass) {
			continue
		}
		queue = append(queue, p.graph.Predecessors(id)...)
	}
	out := make([]graph.Vertex, 0, len(seen))
	for id := range seen {
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// Independent 返回该引擎是否已具备独立状态布局，而非共享通用图解释器。
func (e *Engine) Independent() bool {
	if e == nil {
		return false
	}
	// byteNFA 仅是通用解释回退，不能作为专用引擎完成的依据；
	// 组合后端只按当前引擎的主状态布局判定独立性。
	switch e.Kind {
	case EngineCastle:
		return e.castle != nil && castleRuntimeShapeOK(e.castle)
	case EngineGough:
		return e.gough != nil && goughRuntimeShapeOK(e.gough)
	case EngineLimEx:
		return e.bitNFA != nil && bitRuntimeShapeOK(e.bitNFA)
	case EngineSheng:
		if e.sheng != nil {
			return e.sheng.validate()
		}
		return e.tableNFA != nil && tableRuntimeShapeOK(e.tableNFA)
	case EngineMcSheng, EngineVermicelli:
		if e.Kind == EngineVermicelli && e.vermicelli != nil {
			return e.vermicelli.validate()
		}
		return e.sparseNFA != nil && sparseRuntimeShapeOK(e.sparseNFA)
	case EngineTamarama:
		return e.rangeNFA != nil && rangeRuntimeShapeOK(e.rangeNFA)
	case EngineShufti, EngineTruffle:
		if e.Kind == EngineTruffle && e.truffle != nil {
			return e.truffle.validate()
		}
		return e.nibbleNFA != nil && nibbleRuntimeShapeOK(e.nibbleNFA)
	case EngineRepeat:
		return e.repeat != nil && e.repeat.Validate() == nil
	case EngineMPV:
		return e.mpv != nil && e.mpv.validate() == nil
	case EngineLBR:
		return e.lbr != nil && lbrRuntimeShapeOK(e.lbr)
	default:
		return false
	}
}

// ExecutionModel 返回当前引擎的实际执行模型，便于选择阶段避免误判。
func (e *Engine) ExecutionModel() string {
	if e == nil || e.Program == nil {
		return "none"
	}
	var specialized bool
	switch e.Kind {
	case EngineCastle:
		specialized = e.castle != nil && castleRuntimeShapeOK(e.castle)
	case EngineGough:
		specialized = e.gough != nil && goughRuntimeShapeOK(e.gough)
	case EngineRepeat:
		specialized = e.repeat != nil && e.repeat.Validate() == nil
	case EngineMPV:
		specialized = e.mpv != nil && e.mpv.validate() == nil
	case EngineLBR:
		specialized = e.lbr != nil && lbrRuntimeShapeOK(e.lbr)
	case EngineLimEx:
		specialized = e.bitNFA != nil && bitRuntimeShapeOK(e.bitNFA)
	case EngineSheng:
		specialized = e.sheng != nil && e.sheng.validate() || e.tableNFA != nil && tableRuntimeShapeOK(e.tableNFA)
	case EngineMcSheng, EngineVermicelli:
		specialized = e.Kind == EngineVermicelli && e.vermicelli != nil && e.vermicelli.validate() || e.sparseNFA != nil && sparseRuntimeShapeOK(e.sparseNFA)
	case EngineTamarama:
		specialized = e.rangeNFA != nil && rangeRuntimeShapeOK(e.rangeNFA)
	case EngineShufti, EngineTruffle:
		specialized = e.Kind == EngineTruffle && e.truffle != nil && e.truffle.validate() || e.nibbleNFA != nil && nibbleRuntimeShapeOK(e.nibbleNFA)
	}
	if specialized {
		return e.Kind.String()
	}
	return "generic-nfa-fallback"
}

// Context 保存一次引擎执行的可观测统计和预算。
type Context struct {
	MaxResults int
	MaxSteps   int
	Steps      int
	Results    int
	// Stopped 表示执行器因预算耗尽而提前终止，而非自然完成。
	Stopped bool
	ends    []int
}

// Validate 检查执行预算是否为非负值。
func (c Context) Validate() error {
	if c.MaxResults < 0 || c.MaxSteps < 0 {
		return fmt.Errorf("invalid nfa execution budget")
	}
	return nil
}

// Exhausted 判断任一运行预算是否已耗尽。
func (c Context) Exhausted() bool {
	return c.Stopped || c.MaxSteps > 0 && c.Steps >= c.MaxSteps || c.MaxResults > 0 && c.Results >= c.MaxResults
}

// Reset 清空上下文统计并保留预算配置。
func (c *Context) Reset() {
	if c == nil {
		return
	}
	c.Steps, c.Results, c.Stopped = 0, 0, false
}

// Validate 检查引擎类型和底层程序是否有效。
func (e *Engine) Validate() error {
	if e == nil || e.Program == nil {
		return fmt.Errorf("nil nfa engine")
	}
	if err := e.Program.Validate(); err != nil {
		return fmt.Errorf("invalid nfa program: %w", err)
	}
	if e.Kind < EngineCastle || e.Kind > EngineLBR {
		return fmt.Errorf("invalid nfa engine kind %d", e.Kind)
	}
	backendCount := 0
	for _, present := range []bool{e.castle != nil, e.gough != nil, e.repeat != nil, e.mpv != nil, e.byteNFA != nil, e.tableNFA != nil, e.bitNFA != nil, e.sparseNFA != nil, e.rangeNFA != nil, e.nibbleNFA != nil, e.lbr != nil} {
		if present {
			backendCount++
		}
	}
	if backendCount > 1 {
		return fmt.Errorf("multiple nfa backends attached")
	}
	if e.repeat != nil && e.Kind != EngineRepeat {
		return fmt.Errorf("repeat backend kind mismatch")
	}
	if e.castle != nil && e.Kind != EngineCastle {
		return fmt.Errorf("castle backend kind mismatch")
	}
	if e.castle != nil {
		if err := e.castle.validate(); err != nil {
			return err
		}
		if expanded := nfagraph.ExpandLiterals(e.Program.Graph); expanded == nil || !expanded.Equal(e.castle.graph) {
			return fmt.Errorf("castle graph differs from program")
		}
		if len(e.castle.closureTable) != len(e.castle.graph.Nodes) || len(e.castle.transitions) != len(e.castle.graph.Nodes) {
			return fmt.Errorf("castle table size mismatch")
		}
	}
	if e.gough != nil && e.Kind != EngineGough {
		return fmt.Errorf("gough backend kind mismatch")
	}
	if e.gough != nil {
		if e.gough.graph == nil || e.gough.graph.Validate() != nil || !castleEligible(e.gough.graph) {
			return fmt.Errorf("invalid gough graph")
		}
		if expanded := nfagraph.ExpandLiterals(e.Program.Graph); expanded == nil || !expanded.Equal(e.gough.graph) {
			return fmt.Errorf("gough graph differs from program")
		}
		if len(e.gough.index) != len(e.gough.graph.Nodes) || len(e.gough.predecessors) != len(e.gough.graph.Nodes) {
			return fmt.Errorf("invalid gough index")
		}
		if len(e.gough.reverseTable) != len(e.gough.graph.Nodes) {
			return fmt.Errorf("gough reverse table size mismatch")
		}
		for _, id := range e.gough.graph.NodeIDs() {
			if _, ok := e.gough.index[id]; !ok {
				return fmt.Errorf("gough vertex index missing")
			}
			if _, ok := e.gough.predecessors[id]; !ok {
				return fmt.Errorf("gough predecessor row missing")
			}
			if _, ok := e.gough.reverseTable[id]; !ok {
				return fmt.Errorf("gough reverse row missing")
			}
		}
		if e.gough.words != (len(e.gough.index)+63)/64 || len(e.gough.vertices) != len(e.gough.index) || len(e.gough.acceptMask) != e.gough.words || len(e.gough.deadMask) != e.gough.words || len(e.gough.reverseMask) != len(e.gough.index) || len(e.gough.predMask) != len(e.gough.index) {
			return fmt.Errorf("gough bit layout size mismatch")
		}
		dead := computeGraphDead(e.gough.graph)
		for id, index := range e.gough.index {
			if (e.gough.deadMask[index/64]&(1<<uint(index%64)) != 0) != dead[id] {
				return fmt.Errorf("gough dead mask mismatch")
			}
		}
		for i := range e.gough.reverseMask {
			if len(e.gough.reverseMask[i]) != e.gough.words || len(e.gough.predMask[i]) != e.gough.words {
				return fmt.Errorf("gough bit layout width mismatch")
			}
		}
		if rem := len(e.gough.index) % 64; rem != 0 && len(e.gough.acceptMask) > 0 {
			valid := ^uint64(0) >> uint(64-rem)
			if e.gough.acceptMask[len(e.gough.acceptMask)-1]&^valid != 0 {
				return fmt.Errorf("gough accept mask contains out-of-range state")
			}
		}
		if len(e.gough.acceptNodes) == 0 {
			return fmt.Errorf("gough accept set is empty")
		}
		if !bytes.Equal(e.gough.prefix, requiredLiteralPrefix(e.gough.graph)) {
			return fmt.Errorf("gough prefix mismatch")
		}
		if e.gough.firstMask != firstByteMask(e.gough.graph) || e.gough.acceptsEmpty != graphAcceptsEmpty(e.gough.graph) {
			return fmt.Errorf("gough start candidate metadata mismatch")
		}
		for _, accept := range e.gough.acceptNodes {
			index, ok := e.gough.index[accept]
			if !ok || e.gough.acceptMask[index/64]&(1<<uint(index%64)) == 0 {
				return fmt.Errorf("gough accept mask mismatch")
			}
		}
		for id, predecessors := range e.gough.predecessors {
			if e.gough.graph.Nodes[id] == nil {
				return fmt.Errorf("gough predecessor source out of bounds")
			}
			seenPrevious := make(map[graph.Vertex]struct{}, len(predecessors))
			var lastPrevious graph.Vertex
			for _, predecessor := range predecessors {
				if e.gough.graph.Nodes[predecessor] == nil {
					return fmt.Errorf("gough predecessor target out of bounds")
				}
				if _, exists := seenPrevious[predecessor]; exists {
					return fmt.Errorf("gough duplicate predecessor")
				}
				if len(seenPrevious) > 0 && predecessor <= lastPrevious {
					return fmt.Errorf("gough predecessors are not strictly ordered")
				}
				lastPrevious = predecessor
				seenPrevious[predecessor] = struct{}{}
			}
		}
		for source, closure := range e.gough.reverseTable {
			if e.gough.graph.Nodes[source] == nil {
				return fmt.Errorf("gough reverse closure source out of bounds")
			}
			for _, target := range closure {
				if e.gough.graph.Nodes[target] == nil {
					return fmt.Errorf("gough reverse closure target out of bounds")
				}
			}
			for i := 1; i < len(closure); i++ {
				if closure[i-1] >= closure[i] {
					return fmt.Errorf("gough reverse closure is not strictly ordered")
				}
			}
			found := false
			for _, target := range closure {
				if target == source {
					found = true
					break
				}
			}
			if !found {
				return fmt.Errorf("gough reverse closure misses source")
			}
		}
		for i, vertex := range e.gough.vertices {
			expectedReverse := make(map[graph.Vertex]struct{}, len(e.gough.reverseTable[vertex]))
			for _, item := range e.gough.reverseTable[vertex] {
				expectedReverse[item] = struct{}{}
			}
			expectedPred := make(map[graph.Vertex]struct{}, len(e.gough.predecessors[vertex]))
			for _, item := range e.gough.predecessors[vertex] {
				expectedPred[item] = struct{}{}
			}
			for j, candidate := range e.gough.vertices {
				markedReverse := e.gough.reverseMask[i][j/64]&(1<<uint(j%64)) != 0
				markedPred := e.gough.predMask[i][j/64]&(1<<uint(j%64)) != 0
				_, wantReverse := expectedReverse[candidate]
				_, wantPred := expectedPred[candidate]
				if markedReverse != wantReverse || markedPred != wantPred {
					return fmt.Errorf("gough bit mask mismatch at state %d", i)
				}
			}
		}
	}
	if e.mpv != nil && e.Kind != EngineMPV {
		return fmt.Errorf("mpv backend kind mismatch")
	}
	if e.mpv != nil {
		if err := e.mpv.validate(); err != nil {
			return err
		}
	}
	if e.lbr != nil && e.Kind != EngineLBR {
		return fmt.Errorf("lbr backend kind mismatch")
	}
	if e.lbr != nil {
		if err := e.lbr.validate(); err != nil {
			return err
		}
		if !e.Program.Graph.Equal(e.lbr.graph) {
			return fmt.Errorf("lbr graph differs from program")
		}
		if e.lbr.graph == nil {
			return fmt.Errorf("invalid lbr graph")
		}
		if e.lbr.queueLimit <= 0 {
			return fmt.Errorf("invalid lbr queue limit")
		}
		if !bytes.Equal(e.lbr.prefix, lbrLiteralPrefix(e.lbr.graph)) {
			return fmt.Errorf("lbr prefix mismatch")
		}
		if err := e.lbr.graph.Validate(); err != nil {
			return fmt.Errorf("invalid lbr graph: %w", err)
		}
		if len(e.lbr.successors) != len(e.lbr.graph.Nodes) {
			return fmt.Errorf("lbr successor table size mismatch")
		}
		for _, id := range e.lbr.graph.NodeIDs() {
			n := e.lbr.graph.Nodes[id]
			mask, hasMask := e.lbr.classMask[id]
			if n != nil && n.Kind == nfagraph.KindClass {
				if !hasMask {
					return fmt.Errorf("lbr class mask missing")
				}
				for value := 0; value < 256; value++ {
					expected := castleMatches(n, byte(value))
					actual := mask[value/64]&(1<<uint(value%64)) != 0
					if expected != actual {
						return fmt.Errorf("lbr class mask mismatch")
					}
				}
			} else if hasMask {
				return fmt.Errorf("lbr non-class mask present")
			}
		}
		if len(e.lbr.prefix) > len(e.lbr.graph.Nodes) {
			return fmt.Errorf("lbr prefix exceeds graph size")
		}
		for source, targets := range e.lbr.successors {
			if e.lbr.graph.Nodes[source] == nil {
				return fmt.Errorf("lbr successor source out of bounds")
			}
			seenTargets := make(map[graph.Vertex]struct{}, len(targets))
			var previous graph.Vertex
			for _, target := range targets {
				if e.lbr.graph.Nodes[target] == nil {
					return fmt.Errorf("lbr successor target out of bounds")
				}
				if _, exists := seenTargets[target]; exists {
					return fmt.Errorf("lbr duplicate successor")
				}
				if len(seenTargets) > 0 && target <= previous {
					return fmt.Errorf("lbr successors are not strictly ordered")
				}
				previous = target
				seenTargets[target] = struct{}{}
			}
		}
	}
	if e.tableNFA != nil && e.Kind != EngineSheng {
		return fmt.Errorf("table backend kind mismatch")
	}
	if e.sheng != nil && (e.Kind != EngineSheng || !e.sheng.validate()) {
		return fmt.Errorf("sheng backend layout mismatch")
	}
	if e.bitNFA != nil && e.Kind != EngineLimEx {
		return fmt.Errorf("bit backend kind mismatch")
	}
	if e.sparseNFA != nil && e.Kind != EngineMcSheng && e.Kind != EngineVermicelli {
		return fmt.Errorf("sparse backend kind mismatch")
	}
	if e.vermicelli != nil {
		if e.Kind != EngineVermicelli || !e.vermicelli.validate() {
			return fmt.Errorf("vermicelli backend layout mismatch")
		}
	}
	if e.rangeNFA != nil && e.Kind != EngineTamarama {
		return fmt.Errorf("range backend kind mismatch")
	}
	if e.nibbleNFA != nil && e.Kind != EngineShufti && e.Kind != EngineTruffle {
		return fmt.Errorf("nibble backend kind mismatch")
	}
	if e.truffle != nil && (e.Kind != EngineTruffle || !e.truffle.validate()) {
		return fmt.Errorf("truffle backend layout mismatch")
	}
	if e.rangeNFA != nil {
		if !bitNFAEligibleGraph(e.Program.Graph) {
			return fmt.Errorf("range backend graph is unsupported")
		}
		if err := e.rangeNFA.validate(); err != nil {
			return err
		}
		minBytes, maxBytes := graphLengthBounds(e.Program.Graph)
		if e.rangeNFA.minBytes != minBytes || e.rangeNFA.maxBytes != maxBytes {
			return fmt.Errorf("range length bounds mismatch")
		}
		if err := validateRangeStateKinds(e.Program.Graph, e.rangeNFA); err != nil {
			return err
		}
		if !bytes.Equal(e.rangeNFA.prefix, requiredLiteralPrefix(e.Program.Graph)) {
			return fmt.Errorf("range prefix mismatch")
		}
	}
	if e.nibbleNFA != nil {
		if !dfaLikeGraph(e.Program.Graph) {
			return fmt.Errorf("nibble backend graph is unsupported")
		}
		if err := e.nibbleNFA.validate(); err != nil {
			return err
		}
		minBytes, maxBytes := graphLengthBounds(e.Program.Graph)
		if e.nibbleNFA.minBytes != minBytes || e.nibbleNFA.maxBytes != maxBytes {
			return fmt.Errorf("nibble length bounds mismatch")
		}
		if err := validateNibbleStateKinds(e.Program.Graph, e.nibbleNFA); err != nil {
			return err
		}
		if !bytes.Equal(e.nibbleNFA.prefix, requiredLiteralPrefix(e.Program.Graph)) {
			return fmt.Errorf("nibble prefix mismatch")
		}
	}
	if e.sparseNFA != nil {
		if !dfaLikeGraph(e.Program.Graph) {
			return fmt.Errorf("sparse backend graph is unsupported")
		}
		if err := e.sparseNFA.validate(); err != nil {
			return err
		}
		minBytes, maxBytes := graphLengthBounds(e.Program.Graph)
		if e.sparseNFA.minBytes != minBytes || e.sparseNFA.maxBytes != maxBytes {
			return fmt.Errorf("sparse length bounds mismatch")
		}
		if err := validateSparseStateKinds(e.Program.Graph, e.sparseNFA); err != nil {
			return err
		}
		if e.Kind == EngineVermicelli && !bytes.Equal(e.sparseNFA.prefix, requiredLiteralPrefix(e.Program.Graph)) {
			return fmt.Errorf("vermicelli prefix mismatch")
		}
		if e.Kind == EngineMcSheng && e.sparseNFA.layoutKind != 0 {
			return fmt.Errorf("mcsheng candidate layout mismatch")
		}
		if e.Kind == EngineVermicelli && len(e.sparseNFA.prefix) > 0 && e.sparseNFA.layoutKind != 1 {
			return fmt.Errorf("vermicelli candidate layout mismatch")
		}
		if e.Kind == EngineVermicelli {
			if err := validateSparsePrefix(e.sparseNFA); err != nil {
				return err
			}
		}
	}
	if e.tableNFA != nil {
		if !dfaLikeGraph(e.Program.Graph) {
			return fmt.Errorf("table backend graph is unsupported")
		}
		if err := e.tableNFA.validate(); err != nil {
			return err
		}
		minBytes, maxBytes := graphLengthBounds(e.Program.Graph)
		if e.tableNFA.minBytes != minBytes || e.tableNFA.maxBytes != maxBytes {
			return fmt.Errorf("table length bounds mismatch")
		}
		if !bytes.Equal(e.tableNFA.prefix, requiredLiteralPrefix(e.Program.Graph)) {
			return fmt.Errorf("table prefix mismatch")
		}
		if err := validateTableStateKinds(e.Program.Graph, e.tableNFA); err != nil {
			return err
		}
	}
	if e.bitNFA != nil {
		if !bitNFAEligibleGraph(e.Program.Graph) {
			return fmt.Errorf("bit backend graph is unsupported")
		}
		if err := e.bitNFA.validate(); err != nil {
			return err
		}
		minBytes, maxBytes := graphLengthBounds(e.Program.Graph)
		if e.bitNFA.minBytes != minBytes || e.bitNFA.maxBytes != maxBytes {
			return fmt.Errorf("bit length bounds mismatch")
		}
		if !bytes.Equal(e.bitNFA.prefix, requiredLiteralPrefix(e.Program.Graph)) {
			return fmt.Errorf("bit backend prefix mismatch")
		}
		if err := validateBitStateKinds(e.Program.Graph, e.bitNFA); err != nil {
			return err
		}
	}
	if e.byteNFA != nil {
		if err := e.byteNFA.validate(); err != nil {
			return err
		}
		minBytes, maxBytes := graphLengthBounds(e.Program.Graph)
		if e.byteNFA.minBytes != minBytes || e.byteNFA.maxBytes != maxBytes {
			return fmt.Errorf("byte length bounds mismatch")
		}
		if !bytes.Equal(e.byteNFA.prefix, requiredLiteralPrefix(e.Program.Graph)) {
			return fmt.Errorf("byte backend prefix mismatch")
		}
		if e.byteNFA.firstMask != firstByteMask(e.Program.Graph) || e.byteNFA.acceptsEmpty != byteClosureAccepts(e.byteNFA) {
			return fmt.Errorf("byte backend start candidate metadata mismatch")
		}
		if err := validateByteStateKinds(e.Program.Graph, e.byteNFA); err != nil {
			return err
		}
	}
	return nil
}

func validateByteStateKinds(g *nfagraph.Graph, p *byteNFAProgram) error {
	expanded := nfagraph.ExpandLiterals(g)
	if expanded == nil {
		return fmt.Errorf("byte graph expansion failed")
	}
	ids := expanded.NodeIDs()
	if len(ids) != len(p.states) {
		return fmt.Errorf("byte state count mismatch")
	}
	if p.start != indexOfVertex(ids, expanded.Start) {
		return fmt.Errorf("byte start state mismatch")
	}
	for i, id := range ids {
		n := expanded.Nodes[id]
		if n == nil || p.states[i].kind != n.Kind {
			return fmt.Errorf("byte state kind mismatch at %d", i)
		}
	}
	return nil
}

func validateTableStateKinds(g *nfagraph.Graph, p *tableNFAProgram) error {
	expanded := nfagraph.ExpandLiterals(g)
	if expanded == nil {
		return fmt.Errorf("table graph expansion failed")
	}
	ids := expanded.NodeIDs()
	if len(ids) != len(p.trans) {
		return fmt.Errorf("table state count mismatch")
	}
	if p.start != indexOfVertex(ids, expanded.Start) {
		return fmt.Errorf("table start state mismatch")
	}
	for i, id := range ids {
		if expanded.IsAccept(id) != p.accept[i] {
			return fmt.Errorf("table accept state mismatch at %d", i)
		}
	}
	return nil
}

func validateBitStateKinds(g *nfagraph.Graph, p *bitNFAProgram) error {
	expanded := nfagraph.ExpandLiterals(g)
	if expanded == nil {
		return fmt.Errorf("bit graph expansion failed")
	}
	ids := expanded.NodeIDs()
	if len(ids) != len(p.trans) {
		return fmt.Errorf("bit state count mismatch")
	}
	index := make(map[graph.Vertex]int, len(ids))
	for i, id := range ids {
		index[id] = i
	}
	if p.start != indexOfVertex(ids, expanded.Start) {
		return fmt.Errorf("bit start state mismatch")
	}
	for i, id := range ids {
		want := expanded.IsAccept(id)
		got := p.accept[i/64]&(1<<uint(i%64)) != 0
		if want != got {
			return fmt.Errorf("bit accept state mismatch at %d", i)
		}
		n := expanded.Nodes[id]
		for value := 0; value < 256; value++ {
			wantTransition := n != nil && ((n.Kind == nfagraph.KindLiteral && len(n.Literal) == 1 && n.Literal[0] == byte(value)) || (n.Kind == nfagraph.KindClass && castleMatches(n, byte(value))))
			wantBits := make([]uint64, p.words)
			if wantTransition {
				for _, to := range expanded.Flow.Successors(id) {
					j, ok := index[to]
					if !ok || j >= len(p.closure) {
						return fmt.Errorf("bit transition target mismatch at state %d", i)
					}
					bitOr(wantBits, p.closure[j])
				}
			}
			for word := range wantBits {
				if p.trans[i][value][word] != wantBits[word] {
					return fmt.Errorf("bit transition mismatch at state %d byte %d", i, value)
				}
			}
			marked := p.sourceMask[value][i/64]&(1<<uint(i%64)) != 0
			if marked != wantTransition && len(expanded.Flow.Successors(id)) > 0 {
				return fmt.Errorf("bit source mask mismatch at state %d byte %d", i, value)
			}
		}
	}
	return nil
}

func validateSparseStateKinds(g *nfagraph.Graph, p *sparseNFAProgram) error {
	expanded := nfagraph.ExpandLiterals(g)
	if expanded == nil {
		return fmt.Errorf("sparse graph expansion failed")
	}
	ids := expanded.NodeIDs()
	if len(ids) != len(p.states) {
		return fmt.Errorf("sparse state count mismatch")
	}
	if p.start != indexOfVertex(ids, expanded.Start) {
		return fmt.Errorf("sparse start state mismatch")
	}
	for i, id := range ids {
		n := expanded.Nodes[id]
		st := p.states[i]
		if n == nil || st.kind != n.Kind {
			return fmt.Errorf("sparse state kind mismatch at %d", i)
		}
		if n.Kind == nfagraph.KindLiteral && (len(n.Literal) != 1 || st.lit != n.Literal[0]) {
			return fmt.Errorf("sparse literal state mismatch at %d", i)
		}
	}
	return nil
}

func validateRangeStateKinds(g *nfagraph.Graph, p *rangeNFAProgram) error {
	expanded := nfagraph.ExpandLiterals(g)
	if expanded == nil {
		return fmt.Errorf("range graph expansion failed")
	}
	ids := expanded.NodeIDs()
	if len(ids) != len(p.states) {
		return fmt.Errorf("range state count mismatch")
	}
	if p.start != indexOfVertex(ids, expanded.Start) {
		return fmt.Errorf("range start state mismatch")
	}
	for i, id := range ids {
		n := expanded.Nodes[id]
		if n == nil || p.states[i].kind != n.Kind {
			return fmt.Errorf("range state kind mismatch at %d", i)
		}
	}
	return nil
}

func validateNibbleStateKinds(g *nfagraph.Graph, p *nibbleNFAProgram) error {
	expanded := nfagraph.ExpandLiterals(g)
	if expanded == nil {
		return fmt.Errorf("nibble graph expansion failed")
	}
	ids := expanded.NodeIDs()
	if len(ids) != len(p.states) {
		return fmt.Errorf("nibble state count mismatch")
	}
	if p.start != indexOfVertex(ids, expanded.Start) {
		return fmt.Errorf("nibble start state mismatch")
	}
	for i, id := range ids {
		n := expanded.Nodes[id]
		if n == nil || p.states[i].kind != n.Kind {
			return fmt.Errorf("nibble state kind mismatch at %d", i)
		}
	}
	return nil
}

func indexOfVertex(ids []graph.Vertex, target graph.Vertex) int {
	for i, id := range ids {
		if id == target {
			return i
		}
	}
	return -1
}

func validateSparsePrefix(p *sparseNFAProgram) error {
	if p == nil || len(p.prefix) == 0 {
		return nil
	}
	if p.start < 0 || p.start >= len(p.states) {
		return fmt.Errorf("sparse prefix start out of bounds")
	}
	current := append([]int(nil), p.closures[p.start]...)
	for depth, want := range p.prefix {
		next := make([]int, 0)
		seen := make(map[int]struct{})
		for _, id := range current {
			if id < 0 || id >= len(p.states) {
				return fmt.Errorf("sparse prefix state out of bounds")
			}
			st := p.states[id]
			if st.kind != nfagraph.KindLiteral || st.lit != want {
				continue
			}
			for _, target := range p.literalClosure[id] {
				if _, ok := seen[target]; !ok {
					seen[target] = struct{}{}
					next = append(next, target)
				}
			}
		}
		if len(next) == 0 {
			return fmt.Errorf("sparse prefix disagrees with state layout")
		}
		sort.Ints(next)
		if p.layoutKind == 1 && !equalInts(p.candidateStates[depth], next) {
			return fmt.Errorf("sparse candidate states disagree with prefix")
		}
		current = next
	}
	return nil
}

// Dump 序列化引擎类型及其图程序。
func (e *Engine) Dump() ([]byte, error) {
	if err := e.Validate(); err != nil {
		return nil, err
	}
	raw, err := e.Program.Dump()
	if err != nil {
		return nil, err
	}
	return json.Marshal(struct {
		Version int        `json:"version"`
		Kind    EngineKind `json:"kind"`
		Program []byte     `json:"program"`
	}{version, e.Kind, raw})
}

// LoadEngine 恢复引擎并重新建立可用专用路径。
func LoadEngine(data []byte) (*Engine, error) {
	var raw struct {
		Version int        `json:"version"`
		Kind    EngineKind `json:"kind"`
		Program []byte     `json:"program"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	if raw.Version != version {
		return nil, fmt.Errorf("%w: version %d", ErrUnsupported, raw.Version)
	}
	p, err := Load(raw.Program)
	if err != nil {
		return nil, err
	}
	return CompileEngine(p.Graph, raw.Kind)
}

// Clone 创建引擎及其图程序的深拷贝。
func (e *Engine) Clone() *Engine {
	if e == nil {
		return nil
	}
	var program *Program
	if e.Program != nil {
		program = e.Program.Clone()
	}
	out := &Engine{Kind: e.Kind, Program: program}
	if e.castle != nil && e.castle.graph != nil {
		out.castle = newCastleProgram(e.castle.graph.Clone())
	}
	if e.gough != nil && e.gough.graph != nil {
		out.gough = newGoughProgram(e.gough.graph.Clone())
	}
	if repeatUsable(e) {
		out.repeat = &repeat.Program{Unit: append([]byte(nil), e.repeat.Unit...), Min: e.repeat.Min, Max: e.repeat.Max, Greedy: e.repeat.Greedy}
	}
	if e.mpv != nil && e.mpv.validate() == nil {
		out.mpv = &mpvProgram{literals: make([][]byte, len(e.mpv.literals)), prefix: append([]byte(nil), e.mpv.prefix...)}
		for i := range e.mpv.literals {
			out.mpv.literals[i] = append([]byte(nil), e.mpv.literals[i]...)
		}
	}
	if e.byteNFA != nil && out.Program != nil {
		out.byteNFA = compileByteNFA(out.Program.Graph)
	}
	if e.tableNFA != nil && out.Program != nil {
		out.tableNFA = compileTableNFA(out.Program.Graph)
		if e.Kind == EngineSheng && out.tableNFA != nil {
			out.sheng = &shengProgram{core: out.tableNFA}
		}
	}
	if e.bitNFA != nil && out.Program != nil {
		out.bitNFA = compileBitNFA(out.Program.Graph)
	}
	if e.sparseNFA != nil && out.Program != nil {
		if e.Kind == EngineVermicelli {
			out.sparseNFA = compileVermicelliNFA(out.Program.Graph)
			if out.sparseNFA != nil && len(out.sparseNFA.prefix) > 0 {
				out.vermicelli = &vermicelliProgram{core: out.sparseNFA, prefix: append([]byte(nil), out.sparseNFA.prefix...)}
			}
		} else {
			out.sparseNFA = compileSparseNFA(out.Program.Graph)
		}
	}
	if e.rangeNFA != nil && out.Program != nil {
		out.rangeNFA = compileRangeNFA(out.Program.Graph)
	}
	if e.nibbleNFA != nil && out.Program != nil {
		mode := uint8(0)
		if e.Kind == EngineTruffle {
			mode = 1
		}
		out.nibbleNFA = compileNibbleNFAWithMode(out.Program.Graph, mode)
		if e.Kind == EngineTruffle && out.nibbleNFA != nil {
			out.truffle = newTruffleProgram(out.nibbleNFA)
		}
	}
	if e.lbr != nil && out.Program != nil {
		out.lbr = compileLBR(out.Program.Graph)
	}
	return out
}

// StateCount 返回底层图节点数量。
func (e *Engine) StateCount() int {
	if e != nil && e.Kind == EngineVermicelli && e.vermicelli != nil && e.vermicelli.validate() {
		return e.vermicelli.core.StateCount()
	}
	if e != nil && (e.Kind == EngineMcSheng || e.Kind == EngineVermicelli) && e.sparseNFA != nil && sparseRuntimeShapeOK(e.sparseNFA) {
		return e.sparseNFA.StateCount()
	}
	if e != nil && (e.Kind == EngineShufti || e.Kind == EngineTruffle) && e.nibbleNFA != nil && nibbleRuntimeShapeOK(e.nibbleNFA) {
		return e.nibbleNFA.StateCount()
	}
	if e != nil && e.Kind == EngineTamarama && e.rangeNFA != nil && rangeRuntimeShapeOK(e.rangeNFA) {
		return e.rangeNFA.StateCount()
	}
	if e != nil && e.byteNFA != nil && byteRuntimeShapeOK(e.byteNFA) {
		return e.byteNFA.StateCount()
	}
	if e != nil && e.tableNFA != nil && tableRuntimeShapeOK(e.tableNFA) {
		return e.tableNFA.StateCount()
	}
	if e != nil && e.bitNFA != nil && bitRuntimeShapeOK(e.bitNFA) {
		return e.bitNFA.StateCount()
	}
	if e != nil && e.sparseNFA != nil && sparseRuntimeShapeOK(e.sparseNFA) {
		return e.sparseNFA.StateCount()
	}
	if e != nil && e.rangeNFA != nil && rangeRuntimeShapeOK(e.rangeNFA) {
		return e.rangeNFA.StateCount()
	}
	if e != nil && e.nibbleNFA != nil && nibbleRuntimeShapeOK(e.nibbleNFA) {
		return e.nibbleNFA.StateCount()
	}
	if e != nil && e.repeat != nil && e.repeat.Validate() == nil {
		return e.repeat.Min + 1
	}
	if e != nil && e.mpv != nil && e.mpv.validate() == nil {
		return len(e.mpv.literals)
	}
	if e != nil && e.castle != nil && castleRuntimeShapeOK(e.castle) {
		return len(e.castle.graph.Nodes)
	}
	if e != nil && e.gough != nil && goughRuntimeShapeOK(e.gough) {
		return len(e.gough.graph.Nodes)
	}
	if e != nil && e.lbr != nil && lbrRuntimeShapeOK(e.lbr) {
		return len(e.lbr.graph.Nodes)
	}
	if e == nil || e.Program == nil || e.Program.Graph == nil {
		return 0
	}
	return len(e.Program.Graph.Nodes)
}

// EdgeCount 返回引擎图中的边数量。
func (e *Engine) EdgeCount() int {
	if e == nil || e.Program == nil {
		return 0
	}
	if e.Kind == EngineVermicelli && e.vermicelli != nil && e.vermicelli.validate() {
		return e.vermicelli.core.EdgeCount()
	}
	if (e.Kind == EngineMcSheng || e.Kind == EngineVermicelli) && e.sparseNFA != nil && sparseRuntimeShapeOK(e.sparseNFA) {
		return e.sparseNFA.EdgeCount()
	}
	if (e.Kind == EngineShufti || e.Kind == EngineTruffle) && e.nibbleNFA != nil && nibbleRuntimeShapeOK(e.nibbleNFA) {
		return e.nibbleNFA.EdgeCount()
	}
	if (e.Kind == EngineTamarama || e.Kind == EngineShufti || e.Kind == EngineTruffle) && e.rangeNFA != nil {
		return e.rangeNFA.EdgeCount()
	}
	if e.byteNFA != nil && byteRuntimeShapeOK(e.byteNFA) {
		return e.byteNFA.EdgeCount()
	}
	if e.tableNFA != nil && tableRuntimeShapeOK(e.tableNFA) {
		return e.tableNFA.EdgeCount()
	}
	if e.bitNFA != nil && bitRuntimeShapeOK(e.bitNFA) {
		return e.bitNFA.EdgeCount()
	}
	if e.sparseNFA != nil && sparseRuntimeShapeOK(e.sparseNFA) {
		return e.sparseNFA.EdgeCount()
	}
	if e.rangeNFA != nil && rangeRuntimeShapeOK(e.rangeNFA) {
		return e.rangeNFA.EdgeCount()
	}
	if e.nibbleNFA != nil && nibbleRuntimeShapeOK(e.nibbleNFA) {
		return e.nibbleNFA.EdgeCount()
	}
	if repeatUsable(e) {
		return e.repeat.Min
	}
	if e.castle != nil && castleRuntimeShapeOK(e.castle) {
		return e.castle.graph.Flow.EdgeCount()
	}
	if e.gough != nil && goughRuntimeShapeOK(e.gough) {
		return e.gough.graph.Flow.EdgeCount()
	}
	if e.lbr != nil && lbrRuntimeShapeOK(e.lbr) {
		return e.lbr.graph.Flow.EdgeCount()
	}
	if e.mpv != nil {
		return len(e.mpv.literals)
	}
	return e.Program.EdgeCount()
}

// MemoryBytes 返回专用状态布局的近似内存占用。
func (e *Engine) MemoryBytes() uint64 {
	if e == nil {
		return 0
	}
	if e.castle != nil && castleRuntimeShapeOK(e.castle) {
		bytes := saturatingMul(uint64(len(e.castle.graph.Nodes)), 64)
		bytes = saturatingAdd(bytes, saturatingMul(uint64(e.castle.graph.Flow.EdgeCount()), 8))
		bytes = saturatingAdd(bytes, saturatingMul(uint64(len(e.castle.index)), 16))
		bytes = saturatingAdd(bytes, saturatingMul(uint64(len(e.castle.dead)), 2))
		bytes = saturatingAdd(bytes, saturatingMul(uint64(len(e.castle.deadByIndex)), 1))
		bytes = saturatingAdd(bytes, saturatingMul(uint64(len(e.castle.acceptByIndex)), 1))
		bytes = saturatingAdd(bytes, saturatingMul(uint64(len(e.castle.kinds)), 1))
		bytes = saturatingAdd(bytes, saturatingMul(uint64(len(e.castle.literals)), 1))
		bytes = saturatingAdd(bytes, saturatingMul(uint64(len(e.castle.classMasks)), 32))
		for _, closure := range e.castle.closureTable {
			bytes = saturatingAdd(bytes, saturatingMul(uint64(len(closure)), 8))
		}
		for _, closure := range e.castle.closureByIndex {
			bytes = saturatingAdd(bytes, saturatingMul(uint64(len(closure)), 8))
		}
		for _, row := range e.castle.transitionByIndex {
			bytes = saturatingAdd(bytes, saturatingMul(uint64(len(row)), 8))
		}
		for _, row := range e.castle.closureIndex {
			bytes = saturatingAdd(bytes, saturatingMul(uint64(len(row)), 4))
		}
		for _, row := range e.castle.transitionIndex {
			bytes = saturatingAdd(bytes, saturatingMul(uint64(len(row)), 4))
		}
		bytes = saturatingAdd(bytes, uint64(len(e.castle.prefix)))
		bytes = saturatingAdd(bytes, 40)
		return bytes
	}
	if e.lbr != nil && lbrRuntimeShapeOK(e.lbr) {
		bytes := saturatingAdd(saturatingMul(uint64(len(e.lbr.graph.Nodes)), 48), saturatingMul(uint64(e.lbr.graph.Flow.EdgeCount()), 16))
		bytes = saturatingAdd(bytes, saturatingMul(uint64(len(e.lbr.classMask)), 32))
		bytes = saturatingAdd(bytes, saturatingMul(uint64(len(e.lbr.kinds)), 1))
		bytes = saturatingAdd(bytes, saturatingMul(uint64(len(e.lbr.assertions)), 1))
		bytes = saturatingAdd(bytes, saturatingMul(uint64(len(e.lbr.literals)), 1))
		bytes = saturatingAdd(bytes, saturatingMul(uint64(len(e.lbr.classMasks)), 32))
		bytes = saturatingAdd(bytes, saturatingMul(uint64(len(e.lbr.vertices)), 8))
		for _, row := range e.lbr.next {
			bytes = saturatingAdd(bytes, saturatingMul(uint64(len(row)), 4))
		}
		bytes = saturatingAdd(bytes, saturatingMul(uint64(len(e.lbr.dead)), 2))
		bytes = saturatingAdd(bytes, e.lbr.queueBytes)
		bytes = saturatingAdd(bytes, 40)
		for _, row := range e.lbr.successors {
			bytes = saturatingAdd(bytes, saturatingMul(uint64(len(row)), 8))
		}
		return saturatingAdd(bytes, uint64(len(e.lbr.prefix)))
	}
	if e.gough != nil {
		bytes := saturatingMul(uint64(len(e.gough.graph.Nodes)), 64)
		bytes = saturatingAdd(bytes, saturatingMul(uint64(e.gough.graph.Flow.EdgeCount()), 8))
		bytes = saturatingAdd(bytes, saturatingMul(uint64(len(e.gough.index)), 16))
		for _, closure := range e.gough.reverseTable {
			bytes = saturatingAdd(bytes, saturatingMul(uint64(len(closure)), 8))
		}
		for _, predecessors := range e.gough.predecessors {
			bytes = saturatingAdd(bytes, saturatingMul(uint64(len(predecessors)), 8))
		}
		bytes = saturatingAdd(bytes, saturatingMul(uint64(len(e.gough.vertices)), 8))
		bytes = saturatingAdd(bytes, saturatingMul(uint64(len(e.gough.acceptMask)), 8))
		bytes = saturatingAdd(bytes, saturatingMul(uint64(len(e.gough.deadMask)), 8))
		bytes = saturatingAdd(bytes, uint64(len(e.gough.prefix)))
		bytes = saturatingAdd(bytes, 40)
		for _, mask := range e.gough.reverseMask {
			bytes = saturatingAdd(bytes, saturatingMul(uint64(len(mask)), 8))
		}
		for _, mask := range e.gough.predMask {
			bytes = saturatingAdd(bytes, saturatingMul(uint64(len(mask)), 8))
		}
		for _, mask := range e.gough.consumeMask {
			bytes = saturatingAdd(bytes, saturatingMul(uint64(len(mask)), 8))
		}
		return bytes
	}
	if (e.Kind == EngineMcSheng || e.Kind == EngineVermicelli) && e.sparseNFA != nil {
		return sparseNFAMemoryBytes(e.sparseNFA)
	}
	if (e.Kind == EngineShufti || e.Kind == EngineTruffle) && e.nibbleNFA != nil {
		return nibbleNFAMemoryBytes(e.nibbleNFA)
	}
	if (e.Kind == EngineTamarama || e.Kind == EngineShufti || e.Kind == EngineTruffle) && e.rangeNFA != nil {
		return rangeNFAMemoryBytes(e.rangeNFA)
	}
	if e.byteNFA != nil && byteRuntimeShapeOK(e.byteNFA) {
		bytes := saturatingMul(uint64(len(e.byteNFA.states)), 32)
		bytes = saturatingAdd(bytes, saturatingMul(uint64(len(e.byteNFA.classMask)), 32))
		bytes = saturatingAdd(bytes, saturatingMul(uint64(totalByteNFAEdges(e.byteNFA)), 8))
		if e.byteNFA.trans != nil {
			bytes = saturatingAdd(bytes, saturatingMul(saturatingMul(uint64(len(e.byteNFA.trans)), 256), 8))
			for _, row := range e.byteNFA.transClosure {
				for _, closure := range row {
					bytes = saturatingAdd(bytes, saturatingMul(uint64(len(closure)), 4))
				}
			}
		}
		bytes = saturatingAdd(bytes, uint64(len(e.byteNFA.dead)))
		return saturatingAdd(bytes, uint64(len(e.byteNFA.prefix)))
	}
	if e.tableNFA != nil && tableRuntimeShapeOK(e.tableNFA) {
		bytes := saturatingMul(saturatingMul(uint64(e.tableNFA.StateCount()), 256), 8)
		bytes = saturatingAdd(bytes, saturatingMul(uint64(e.tableNFA.EdgeCount()), 8))
		if len(e.tableNFA.sourceMask) > 0 {
			bytes = saturatingAdd(bytes, saturatingMul(uint64(len(e.tableNFA.sourceMask)), saturatingMul(uint64(len(e.tableNFA.sourceMask[0])), 8)))
		}
		bytes = saturatingAdd(bytes, uint64(len(e.tableNFA.prefix)))
		for _, closure := range e.tableNFA.closure {
			bytes = saturatingAdd(bytes, saturatingMul(uint64(len(closure)), 4))
		}
		for _, row := range e.tableNFA.transClosure {
			for _, closure := range row {
				bytes = saturatingAdd(bytes, saturatingMul(uint64(len(closure)), 4))
			}
		}
		bytes = saturatingAdd(bytes, uint64(len(e.tableNFA.dead)))
		bytes = saturatingAdd(bytes, 33)
		return bytes
	}
	if e.bitNFA != nil && bitRuntimeShapeOK(e.bitNFA) {
		bytes := saturatingMul(saturatingMul(saturatingMul(uint64(len(e.bitNFA.trans)), uint64(e.bitNFA.words)), 256), 8)
		bytes = saturatingAdd(bytes, saturatingMul(saturatingMul(256, uint64(e.bitNFA.words)), 8))
		for _, states := range e.bitNFA.sourceStates {
			bytes = saturatingAdd(bytes, saturatingMul(uint64(len(states)), 2))
		}
		bytes = saturatingAdd(bytes, saturatingMul(saturatingMul(uint64(len(e.bitNFA.closure)), uint64(e.bitNFA.words)), 8))
		bytes = saturatingAdd(bytes, saturatingMul(uint64(len(e.bitNFA.accept)), 8))
		bytes = saturatingAdd(bytes, saturatingMul(uint64(len(e.bitNFA.consumable)), 8))
		bytes = saturatingAdd(bytes, saturatingMul(uint64(len(e.bitNFA.exceptional)), 8))
		bytes = saturatingAdd(bytes, saturatingMul(uint64(len(e.bitNFA.epsilonOnly)), 8))
		bytes = saturatingAdd(bytes, saturatingMul(uint64(len(e.bitNFA.dead)), 8))
		bytes = saturatingAdd(bytes, 32+1+8)
		bytes = saturatingAdd(bytes, uint64(len(e.bitNFA.prefix)))
		return bytes
	}
	if e.sparseNFA != nil && sparseRuntimeShapeOK(e.sparseNFA) {
		return sparseNFAMemoryBytes(e.sparseNFA)
	}
	if e.rangeNFA != nil && rangeRuntimeShapeOK(e.rangeNFA) {
		return rangeNFAMemoryBytes(e.rangeNFA)
	}
	if e.nibbleNFA != nil && nibbleRuntimeShapeOK(e.nibbleNFA) {
		return nibbleNFAMemoryBytes(e.nibbleNFA)
	}
	if repeatUsable(e) {
		return uint64(len(e.repeat.Unit) + 32)
	}
	if e.mpv != nil {
		total := 32
		for _, lit := range e.mpv.literals {
			total += len(lit) + 16
		}
		total += len(e.mpv.prefix)
		return uint64(total)
	}
	return saturatingMul(uint64(e.StateCount()), 32)
}

func sparseNFAMemoryBytes(p *sparseNFAProgram) uint64 {
	if p == nil {
		return 0
	}
	bytes := saturatingMul(uint64(len(p.states)), 32)
	bytes = saturatingAdd(bytes, saturatingMul(uint64(len(p.classMask)), 32))
	bytes = saturatingAdd(bytes, saturatingMul(uint64(p.EdgeCount()), 8))
	for _, transitions := range p.trans {
		bytes = saturatingAdd(bytes, saturatingMul(uint64(len(transitions)), 16))
	}
	for _, closure := range p.closures {
		bytes = saturatingAdd(bytes, saturatingMul(uint64(len(closure)), 4))
	}
	for _, closure := range p.literalClosure {
		bytes = saturatingAdd(bytes, saturatingMul(uint64(len(closure)), 4))
	}
	for _, closure := range p.classClosure {
		bytes = saturatingAdd(bytes, saturatingMul(uint64(len(closure)), 4))
	}
	bytes = saturatingAdd(bytes, uint64(len(p.dead)))
	if len(p.sourceMask) > 0 {
		bytes = saturatingAdd(bytes, saturatingMul(uint64(len(p.sourceMask)), saturatingMul(uint64(len(p.sourceMask[0])), 8)))
	}
	bytes = saturatingAdd(bytes, 512)
	bytes = saturatingAdd(bytes, 33)
	bytes = saturatingAdd(bytes, uint64(len(p.prefix)))
	for _, states := range p.candidateStates {
		bytes = saturatingAdd(bytes, saturatingMul(uint64(len(states)), 4))
	}
	return bytes
}

func rangeNFAMemoryBytes(p *rangeNFAProgram) uint64 {
	if p == nil {
		return 0
	}
	bytes := saturatingMul(uint64(len(p.states)), 32)
	bytes = saturatingAdd(bytes, saturatingMul(uint64(p.EdgeCount()), 8))
	for _, row := range p.trans {
		bytes = saturatingAdd(bytes, saturatingMul(uint64(len(row)), 4))
		for _, tr := range row {
			bytes = saturatingAdd(bytes, saturatingMul(uint64(len(tr.next)), 4))
		}
	}
	for _, row := range p.transitionClosure {
		for _, closure := range row {
			bytes = saturatingAdd(bytes, saturatingMul(uint64(len(closure)), 4))
		}
	}
	for _, closure := range p.closures {
		bytes = saturatingAdd(bytes, saturatingMul(uint64(len(closure)), 4))
	}
	for _, closure := range p.nextClosure {
		bytes = saturatingAdd(bytes, saturatingMul(uint64(len(closure)), 4))
	}
	bytes = saturatingAdd(bytes, saturatingMul(uint64(len(p.masks)), 32))
	bytes = saturatingAdd(bytes, saturatingMul(uint64(len(p.bucket)), 256*2))
	bytes = saturatingAdd(bytes, uint64(len(p.dead)))
	bytes = saturatingAdd(bytes, 33)
	bytes = saturatingAdd(bytes, uint64(len(p.prefix)))
	return bytes
}

func nibbleNFAMemoryBytes(p *nibbleNFAProgram) uint64 {
	if p == nil {
		return 0
	}
	bytes := saturatingMul(uint64(len(p.states)), 32)
	bytes = saturatingAdd(bytes, saturatingMul(uint64(p.EdgeCount()), 8))
	bytes = saturatingAdd(bytes, saturatingMul(uint64(len(p.masks)), 32))
	for _, closure := range p.closures {
		bytes = saturatingAdd(bytes, saturatingMul(uint64(len(closure)), 4))
	}
	for _, closure := range p.nextClosure {
		bytes = saturatingAdd(bytes, saturatingMul(uint64(len(closure)), 4))
	}
	if len(p.sourceMask) > 0 {
		bytes = saturatingAdd(bytes, saturatingMul(uint64(len(p.sourceMask)), saturatingMul(uint64(len(p.sourceMask[0])), 8)))
	}
	bytes = saturatingAdd(bytes, uint64(len(p.dead)))
	bytes = saturatingAdd(bytes, 33)
	bytes = saturatingAdd(bytes, uint64(len(p.prefix)))
	return bytes
}

func saturatingAdd(a, b uint64) uint64 {
	if ^uint64(0)-a < b {
		return ^uint64(0)
	}
	return a + b
}

func saturatingMul(a, b uint64) uint64 {
	if a != 0 && b > ^uint64(0)/a {
		return ^uint64(0)
	}
	return a * b
}

func (k EngineKind) String() string {
	names := []string{"", "castle", "gough", "limex", "sheng", "mcsheng", "tamarama", "vermicelli", "shufti", "truffle", "repeat", "mpv", "lbr"}
	if int(k) < len(names) {
		return names[k]
	}
	return "unknown"
}

// Valid 判断引擎类型是否在支持范围内。
func (k EngineKind) Valid() bool { return k >= EngineCastle && k <= EngineLBR }

func CompileEngine(g *nfagraph.Graph, k EngineKind) (*Engine, error) {
	if k < EngineCastle || k > EngineLBR {
		return nil, fmt.Errorf("invalid nfa engine kind %d", k)
	}
	p, e := Compile(g)
	if e != nil {
		return nil, e
	}
	engine := compileEngineProgram(g, k, p)
	if err := engine.Validate(); err != nil {
		return nil, err
	}
	return engine, nil
}

// CompileEngineWithBudgets 构建指定后端并校验状态与内存预算，超限时返回可判定错误。
func CompileEngineWithBudgets(g *nfagraph.Graph, k EngineKind, maxStates int, maxMemory uint64) (*Engine, error) {
	if g == nil || g.Validate() != nil {
		return nil, ErrInvalidGraph
	}
	if !k.Valid() {
		return nil, fmt.Errorf("invalid nfa engine kind %d", k)
	}
	if maxStates < 0 {
		return nil, ErrStateLimit
	}
	if expanded := nfagraph.ExpandLiterals(g); expanded == nil || expanded.Flow == nil {
		return nil, ErrInvalidGraph
	} else {
		// 资源检查顺序固定为状态、边、内存，确保不同入口返回一致错误。
		if maxStates > 0 && k != EngineRepeat && k != EngineMPV && len(expanded.Nodes) > maxStates {
			return nil, ErrStateLimit
		}
		if limit := DefaultLimits(k).Edges; limit > 0 && expanded.Flow.EdgeCount() > limit {
			return nil, ErrEdgeLimit
		}
	}
	if maxMemory > 0 && estimateEngineMemory(g, k) > maxMemory {
		return nil, ErrMemoryLimit
	}
	engine, err := CompileEngine(g, k)
	if err != nil {
		return nil, err
	}
	if maxStates > 0 && engine.StateCount() > maxStates {
		return nil, ErrStateLimit
	}
	if maxMemory > 0 && engine.MemoryBytes() > maxMemory {
		return nil, ErrMemoryLimit
	}
	if err := engine.Validate(); err != nil {
		return nil, err
	}
	return engine, nil
}

// estimateEngineMemory 在建立专用转移表前给出保守的布局估算。
// 额外安全系数覆盖切片、映射和分配器元数据，避免低估实际占用。
func estimateEngineMemory(g *nfagraph.Graph, kind EngineKind) uint64 {
	raw := estimateEngineMemoryRaw(g, kind)
	if raw == ^uint64(0) || raw > ^uint64(0)/2 {
		return ^uint64(0)
	}
	return raw * 2
}

func estimateEngineMemoryRaw(g *nfagraph.Graph, kind EngineKind) uint64 {
	if g == nil {
		return ^uint64(0)
	}
	expanded := nfagraph.ExpandLiterals(g)
	if expanded == nil {
		return ^uint64(0)
	}
	states := uint64(len(expanded.Nodes))
	if states == 0 {
		return 0
	}
	mul := func(values ...uint64) uint64 {
		result := uint64(1)
		for _, value := range values {
			if value != 0 && result > ^uint64(0)/value {
				return ^uint64(0)
			}
			result *= value
		}
		return result
	}
	add := func(values ...uint64) uint64 {
		result := uint64(0)
		for _, value := range values {
			if result > ^uint64(0)-value {
				return ^uint64(0)
			}
			result += value
		}
		return result
	}
	words := (states + 63) / 64
	edges := uint64(expanded.EdgeCount())
	switch kind {
	case EngineLimEx:
		// 转移表、源状态掩码、闭包、接受集合和确定性前缀均计入预算。
		return add(mul(states, 256, words, 8), mul(256, words, 8), mul(states, 256, 2), mul(states, words, 8), mul(words, 8), mul(words, 8), mul(words, 8), mul(words, 8), mul(words, 8), states, 41)
	case EngineSheng:
		// 表驱动布局包含原始转移、预展开闭包、源状态掩码和闭包索引。
		return add(mul(states, 256, 8), mul(states, 256, 8), mul(states, 32), mul(edges, 8), mul(256, words, 8), states, states, 33)
	case EngineMcSheng:
		return add(mul(states, 72), mul(edges, 16), mul(states, 16), mul(states, 8), mul(256, words, 8), 512, states)
	case EngineVermicelli:
		return add(mul(states, 72), mul(edges, 16), mul(states, 16, 8), mul(states, 16), mul(states, 8), mul(states, 4), mul(256, words, 8), states, 512)
	case EngineTamarama:
		return add(mul(states, 48), mul(edges, 16), mul(edges, 8), mul(states, 256, 8), mul(states, 256, 2), states, states, 33)
	case EngineShufti, EngineTruffle:
		return add(mul(states, 48), mul(edges, 16), mul(states, 32), mul(256, words, 8), states, states, 33)
	case EngineCastle, EngineGough:
		return add(mul(states, 48), mul(edges, 16), mul(states, states, 8), mul(states, states, 4), mul(states, words, 8), mul(256, words, 8), mul(states, 32), states, 40)
	case EngineLBR:
		return add(mul(states, 80), mul(edges, 16), mul(states, 16), mul(edges, 4), 1<<20, 40)
	case EngineRepeat, EngineMPV:
		return add(mul(states, 32), mul(edges, 8), states)
	default:
		return mul(states, 64)
	}
}

func compileEngineProgram(g *nfagraph.Graph, k EngineKind, p *Program) *Engine {
	engine := &Engine{Kind: k, Program: p}
	if k == EngineCastle {
		expanded := nfagraph.ExpandLiterals(g)
		if castleEligible(expanded) {
			engine.castle = newCastleProgram(expanded)
		}
	}
	if k == EngineGough {
		expanded := nfagraph.ExpandLiterals(g)
		if castleEligible(expanded) {
			engine.gough = newGoughProgram(expanded)
		}
	}
	if k == EngineRepeat {
		engine.repeat = compileRepeatGraph(g)
	}
	if k == EngineMPV {
		engine.mpv = compileMPVGraph(g)
	}
	if k == EngineLBR {
		engine.lbr = compileLBR(g)
	}
	// 不同后端使用不同的状态布局，只有布局无法承载图时才回退到字节状态机。
	switch k {
	case EngineLimEx:
		engine.bitNFA = compileBitNFA(g)
	case EngineSheng:
		engine.tableNFA = compileTableNFA(g)
		if engine.tableNFA != nil {
			engine.sheng = &shengProgram{core: engine.tableNFA}
		}
	case EngineMcSheng:
		engine.sparseNFA = compileSparseNFA(g)
	case EngineTamarama:
		engine.rangeNFA = compileRangeNFA(g)
	case EngineShufti, EngineTruffle:
		mode := uint8(0)
		if k == EngineTruffle {
			mode = 1
		}
		engine.nibbleNFA = compileNibbleNFAWithMode(g, mode)
		if k == EngineTruffle && engine.nibbleNFA != nil {
			engine.truffle = newTruffleProgram(engine.nibbleNFA)
		}
	case EngineVermicelli:
		engine.sparseNFA = compileVermicelliNFA(g)
		if engine.sparseNFA != nil && len(engine.sparseNFA.prefix) > 0 {
			engine.vermicelli = &vermicelliProgram{core: engine.sparseNFA, prefix: append([]byte(nil), engine.sparseNFA.prefix...)}
		}
	}
	// 编译后立即校验布局；任何不完整或越界的专用表都丢弃，
	// 后续由统一字节状态机安全接管，避免带病状态进入执行路径。
	if engine.bitNFA != nil && engine.bitNFA.validate() != nil {
		engine.bitNFA = nil
	}
	if engine.tableNFA != nil && engine.tableNFA.validate() != nil {
		engine.tableNFA = nil
		engine.sheng = nil
	}
	if engine.sparseNFA != nil && engine.sparseNFA.validate() != nil {
		engine.sparseNFA = nil
		engine.vermicelli = nil
	}
	if engine.rangeNFA != nil && engine.rangeNFA.validate() != nil {
		engine.rangeNFA = nil
	}
	if engine.nibbleNFA != nil && engine.nibbleNFA.validate() != nil {
		engine.nibbleNFA = nil
		engine.truffle = nil
	}
	// 专用布局统一执行 16MiB 内存门槛，超限图保留通用字节回退。
	if engine.bitNFA != nil && engine.MemoryBytes() > maxLayoutMemory {
		engine.bitNFA = nil
	}
	if engine.tableNFA != nil && engine.MemoryBytes() > maxLayoutMemory {
		engine.tableNFA = nil
	}
	if engine.sparseNFA != nil && engine.MemoryBytes() > maxLayoutMemory {
		engine.sparseNFA = nil
	}
	if engine.rangeNFA != nil && engine.MemoryBytes() > maxLayoutMemory {
		engine.rangeNFA = nil
	}
	if engine.nibbleNFA != nil && engine.MemoryBytes() > maxLayoutMemory {
		engine.nibbleNFA = nil
		engine.truffle = nil
	}
	if engine.repeat == nil && engine.mpv == nil && engine.lbr == nil && engine.tableNFA == nil && engine.bitNFA == nil && engine.sparseNFA == nil && engine.rangeNFA == nil && engine.nibbleNFA == nil && k != EngineCastle && k != EngineGough {
		engine.byteNFA = compileByteNFA(g)
	}
	return engine
}

// SelectEngineKind 根据图结构选择最具体且可安全验证的执行后端。
func SelectEngineKind(g *nfagraph.Graph) EngineKind {
	if g == nil || g.Validate() != nil {
		return 0
	}
	if graphHasRepeat(g) && compileRepeatGraph(g) != nil {
		return EngineRepeat
	}
	if compileMPVGraph(g) != nil {
		return EngineMPV
	}
	if compileBitNFA(g) != nil {
		return EngineLimEx
	}
	if compileTableNFA(g) != nil {
		return selectTableKind(g)
	}
	// 位集合和完整字节表受状态规模限制时，继续尝试压缩布局，
	// 避免大型但结构简单的字节图直接退回通用解释器。
	if compileRangeNFA(g) != nil {
		return EngineTamarama
	}
	if compileSparseNFA(g) != nil {
		return EngineMcSheng
	}
	if compileNibbleNFA(g) != nil {
		return EngineShufti
	}
	if castleEligible(nfagraph.ExpandLiterals(g)) {
		return EngineCastle
	}
	return EngineLimEx
}

// EngineSelection 描述引擎选择的可追踪依据和资源估算。
type EngineSelection struct {
	Kind        EngineKind
	Reason      string
	Cost        uint64
	Bailout     string
	States      int
	Edges       int
	Memory      uint64
	Independent bool
}

// ExplainEngineSelection 返回不改变执行结果的选择诊断快照。
func ExplainEngineSelection(g *nfagraph.Graph) EngineSelection {
	if g == nil || g.Validate() != nil {
		return EngineSelection{Reason: "invalid graph"}
	}
	kind := SelectEngineKind(g)
	selection := EngineSelection{Kind: kind, States: g.NodeCount(), Edges: g.EdgeCount(), Memory: estimateEngineMemory(g, kind), Cost: estimateEngineCost(g, kind)}
	switch kind {
	case EngineRepeat:
		selection.Reason = "bounded repeat layout"
	case EngineMPV:
		selection.Reason = "literal vector layout"
	case EngineLimEx:
		selection.Reason = "bit state layout"
	case EngineSheng, EngineMcSheng:
		selection.Reason = "table cost model"
	case EngineTamarama:
		selection.Reason = "range bucket layout"
	case EngineShufti, EngineTruffle:
		selection.Reason = "nibble transpose layout"
	case EngineVermicelli:
		selection.Reason = "prefix candidate layout"
	case EngineCastle, EngineGough:
		selection.Reason = "tree closure layout"
	default:
		selection.Reason = "safe generic fallback"
		selection.Bailout = "no dedicated layout satisfied graph constraints"
	}
	if engine, err := CompileEngineWithBudgets(g, kind, 0, maxLayoutMemory); err == nil {
		selection.Independent = engine.Independent()
		if !selection.Independent {
			selection.Bailout = "专用布局不可用，使用通用确认"
		}
	} else {
		// 选择诊断必须保留实际编译失败原因，避免仅凭首选
		// 引擎名称误判已经启用专用执行路径。
		selection.Bailout = err.Error()
		if errors.Is(err, ErrStateLimit) || errors.Is(err, ErrEdgeLimit) || errors.Is(err, ErrMemoryLimit) {
			selection.Reason = "资源预算拒绝专用布局"
		}
	}
	return selection
}

// estimateEngineCost 给选择诊断提供稳定的相对代价；它只读取图结构，
// 不参与匹配结果计算。代价同时包含状态、边、分支和布局内存压力，
// 便于编译阶段解释为何在专用布局之间降级。
func estimateEngineCost(g *nfagraph.Graph, kind EngineKind) uint64 {
	if g == nil || kind == 0 {
		return 0
	}
	states, edges := uint64(g.NodeCount()), uint64(g.EdgeCount())
	cost := states*8 + edges*3
	for _, id := range g.NodeIDs() {
		n := g.Nodes[id]
		if n == nil {
			continue
		}
		succ := len(g.Flow.Successors(id))
		if succ > 1 {
			cost += uint64(succ * succ)
		}
		switch n.Kind {
		case nfagraph.KindClass:
			cost += uint64(len(n.Class.Ranges))
		case nfagraph.KindAssertion:
			cost += 16
		case nfagraph.KindRepeat:
			cost += 12
		}
	}
	memory := estimateEngineMemory(g, kind)
	if memory > maxLayoutMemory {
		cost += memory/maxLayoutMemory*1024 + 1
	}
	return cost
}

func selectTableKind(g *nfagraph.Graph) EngineKind {
	if g == nil {
		return EngineShufti
	}
	classes, ranges, branches := 0, 0, 0
	for _, id := range g.NodeIDs() {
		if n := g.Nodes[id]; n != nil {
			if n.Kind == nfagraph.KindClass {
				classes++
				ranges += len(n.Class.Ranges)
			}
			if len(g.Flow.Successors(id)) > 1 {
				branches++
			}
		}
	}
	if len(g.Nodes) > 256 {
		if candidate := compileVermicelliNFA(g); candidate != nil && len(candidate.prefix) > 0 {
			return EngineVermicelli
		}
		return EngineMcSheng
	}
	if classes > 0 && ranges/classes > 4 {
		return EngineTruffle
	}
	if branches >= 8 {
		return EngineMcSheng
	}
	if classes == 0 {
		return EngineSheng
	}
	if branches > 0 {
		return EngineTamarama
	}
	return EngineShufti
}

// CompileAuto 根据图能力构建可用的专用引擎；已验证但不满足专用条件的图
// 保留通用 NFA 执行路径，只有非法图才返回错误。
func CompileAuto(g *nfagraph.Graph) (*Engine, error) {
	kind := SelectEngineKind(g)
	if kind == 0 {
		return nil, fmt.Errorf("invalid graph for engine selection")
	}
	p, err := Compile(g)
	if err != nil {
		return nil, err
	}
	e := compileEngineProgram(g, kind, p)
	if err := e.Validate(); err != nil {
		return nil, err
	}
	return e, nil
}

func graphHasRepeat(g *nfagraph.Graph) bool {
	if g == nil {
		return false
	}
	for _, id := range g.NodeIDs() {
		if n := g.Nodes[id]; n != nil && n.Kind == nfagraph.KindRepeat {
			return true
		}
	}
	return false
}

func compileRepeatGraph(g *nfagraph.Graph) *repeat.Program {
	if g == nil || len(g.Nodes) > 128 {
		return nil
	}
	// Repeat 图由构造器按稳定 NodeID 顺序展开。这里只接受所有消费节点
	// 能组成同一周期的纯文字语言；一旦出现前缀、后缀或分支文字不一致，
	// 交给通用确认器，避免把近似图误识别为重复程序。
	ids := g.NodeIDs()
	var all, before []byte
	repeatIndex, repMin, repMax := -1, -1, -1
	greedy := true
	for idx, id := range ids {
		n := g.Nodes[id]
		if n == nil {
			return nil
		}
		switch n.Kind {
		case nfagraph.KindLiteral:
			if len(n.Literal) == 0 {
				return nil
			}
			all = append(all, n.Literal...)
			if repeatIndex < 0 {
				before = append(before, n.Literal...)
			}
		case nfagraph.KindStart, nfagraph.KindAccept, nfagraph.KindSplit, nfagraph.KindJoin, nfagraph.KindRepeat, nfagraph.KindReport:
		default:
			return nil
		}
		if n.Kind == nfagraph.KindRepeat {
			if repeatIndex < 0 {
				repeatIndex, repMin, repMax, greedy = idx, n.RepeatMin, n.RepeatMax, n.Greedy
			} else if n.RepeatMin != repMin || n.RepeatMax != repMax || n.Greedy != greedy {
				return nil
			}
		}
	}
	if repeatIndex < 0 {
		if len(all) == 0 {
			return nil
		}
		repMin, repMax = 1, 1
		before = append([]byte(nil), all...)
	}
	if len(all) == 0 {
		return nil
	}
	unit := primitiveRepeatUnit(all)
	if repeatIndex < 0 {
		// 没有 Repeat 控制节点时，图本身就是一个完整文字，不能
		// 将其中的周期误解为可变重复（例如 "aa" 不能降成 "a"）。
		unit = append([]byte(nil), all...)
	}
	if len(unit) == 0 {
		return nil
	}
	if repMin > 0 && len(before) > 0 && !bytes.Equal(before, bytes.Repeat(unit, repMin)) {
		return nil
	}
	p, err := repeat.New(unit, repMin, repMax, greedy)
	if err != nil {
		return nil
	}
	model := &Program{Graph: g}
	// 对所有可枚举重复次数及少量扰动输入做等价检查。该检查只发生在
	// 编译阶段，确保专用路径不会因 NodeID 顺序或分支形状误判语言。
	limit := 16
	if repMax >= 0 && repMax+2 < limit {
		limit = repMax + 2
	}
	for count := 0; count < limit; count++ {
		data := bytes.Repeat(unit, count)
		if !sameRepeatEnds(model.MatchAt(data, 0), p.MatchAt(data, 0)) {
			return nil
		}
		if len(data) > 0 {
			mutated := append([]byte(nil), data...)
			mutated[len(mutated)-1] ^= 0x80
			if !sameRepeatEnds(model.MatchAt(mutated, 0), p.MatchAt(mutated, 0)) {
				return nil
			}
		}
	}
	return p
}

func primitiveRepeatUnit(data []byte) []byte {
	if len(data) == 0 {
		return nil
	}
	for width := 1; width <= len(data); width++ {
		if len(data)%width != 0 {
			continue
		}
		ok := true
		for i := width; i < len(data); i++ {
			if data[i] != data[i%width] {
				ok = false
				break
			}
		}
		if ok {
			return append([]byte(nil), data[:width]...)
		}
	}
	return append([]byte(nil), data...)
}

func sameRepeatEnds(a, b []int) bool {
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

func containsEnd(ends []int, want int) bool {
	for _, end := range ends {
		if end == want {
			return true
		}
	}
	return false
}

func compileMPVGraph(g *nfagraph.Graph) *mpvProgram {
	if g == nil {
		return nil
	}
	if len(g.Nodes) > sparseStateLimit || g.Flow == nil || g.Flow.EdgeCount() > 16384 {
		return nil
	}
	var literalBytes uint64
	for _, id := range g.NodeIDs() {
		if n := g.Nodes[id]; n != nil && n.Kind == nfagraph.KindLiteral {
			literalBytes = saturatingAdd(literalBytes, uint64(len(n.Literal)))
			if literalBytes > maxLayoutMemory {
				return nil
			}
		}
	}
	g = nfagraph.ExpandLiterals(g)
	if !dfaLikeGraph(g) || g.AcceptCount() != 1 {
		return nil
	}
	for _, id := range g.NodeIDs() {
		if g.Nodes[id].Kind == nfagraph.KindClass {
			return nil
		}
	}
	accepts := make(map[graph.Vertex]bool)
	for _, id := range g.Accepts() {
		accepts[id] = true
	}
	out := &mpvProgram{}
	invalidEmpty := false
	var walk func(graph.Vertex, []byte, map[graph.Vertex]bool) bool
	walk = func(id graph.Vertex, lit []byte, seen map[graph.Vertex]bool) bool {
		if len(out.literals) >= 1024 {
			return false
		}
		if seen[id] {
			return false
		}
		seen[id] = true
		n := g.Nodes[id]
		if n == nil {
			return false
		}
		if n.Kind == nfagraph.KindClass {
			return false
		}
		if n.Kind == nfagraph.KindLiteral {
			if len(n.Literal) != 1 {
				return false
			}
			lit = append(lit, n.Literal...)
		}
		if accepts[id] {
			// 空接受分支需要保留每个起点的零长度结果，不能交给文字候选路径。
			if len(lit) == 0 {
				invalidEmpty = true
				delete(seen, id)
				return false
			}
			out.literals = append(out.literals, append([]byte(nil), lit...))
			delete(seen, id)
			return true
		}
		success := false
		for _, to := range g.Flow.Successors(id) {
			if walk(to, lit, seen) {
				success = true
			}
		}
		delete(seen, id)
		return success
	}
	if !walk(g.Start, nil, map[graph.Vertex]bool{}) || invalidEmpty || len(out.literals) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(out.literals))
	unique := out.literals[:0]
	for _, lit := range out.literals {
		key := string(lit)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		unique = append(unique, lit)
	}
	out.literals = unique
	var totalBytes uint64
	for _, lit := range out.literals {
		totalBytes = saturatingAdd(totalBytes, uint64(len(lit)))
	}
	if totalBytes > maxLayoutMemory {
		return nil
	}
	out.prefix = commonLiteralPrefix(out.literals)
	// 运行时在结果预算下可能提前返回，先按结束偏移对应的文字长度
	// 排序，保证提前截断与完整结果保持相同的稳定顺序。
	sort.SliceStable(out.literals, func(i, j int) bool {
		if len(out.literals[i]) != len(out.literals[j]) {
			return len(out.literals[i]) < len(out.literals[j])
		}
		return bytes.Compare(out.literals[i], out.literals[j]) < 0
	})
	return out
}

func castleEligible(g *nfagraph.Graph) bool {
	if g == nil || len(g.Nodes) > 4096 || g.Flow == nil || g.Flow.EdgeCount() > 16384 {
		return false
	}
	for _, id := range g.NodeIDs() {
		n := g.Nodes[id]
		if n == nil {
			return false
		}
		switch n.Kind {
		case nfagraph.KindStart, nfagraph.KindAccept, nfagraph.KindLiteral, nfagraph.KindClass, nfagraph.KindSplit, nfagraph.KindJoin, nfagraph.KindReport:
			if n.Kind == nfagraph.KindClass && n.Unicode != nil {
				return false
			}
			if n.Kind == nfagraph.KindLiteral && len(n.Literal) != 1 {
				return false
			}
		default:
			return false
		}
	}
	return true
}

// Capabilities 返回引擎支持的执行特性快照。
func (e *Engine) Capabilities() map[string]bool {
	if e == nil {
		return nil
	}
	vectorized := e.bitNFA != nil && bitRuntimeShapeOK(e.bitNFA) || e.rangeNFA != nil && rangeRuntimeShapeOK(e.rangeNFA) || e.nibbleNFA != nil && nibbleRuntimeShapeOK(e.nibbleNFA)
	return map[string]bool{
		"stateful":    e.Kind == EngineLimEx || e.Kind == EngineCastle || e.Kind == EngineGough || e.Kind == EngineLBR,
		"simd":        vectorized,
		"reverse":     e.Kind == EngineGough || e.Kind == EngineCastle,
		"bounded":     e.repeat != nil && e.repeat.Max >= 0,
		"specialized": e.Independent(),
	}
}

// SupportsGraph 判断当前引擎是否接受给定图的节点特性。
func (e *Engine) SupportsGraph(g *nfagraph.Graph) bool {
	if e == nil || g == nil || g.Validate() != nil {
		return false
	}
	if e.Program == nil || e.Program.Graph == nil || !e.Program.Graph.Equal(g) {
		// 布局索引与图节点一一对应，不能仅凭节点种类判断另一张
		// 结构不同的图也能复用当前引擎。
		return false
	}
	if e.Kind == EngineRepeat {
		// 基础字节图允许回退到通用路径；可识别的纯文字重复启用专用程序。
		return g.Validate() == nil && (compileRepeatGraph(g) != nil || dfaLikeGraph(g))
	}
	if e.Kind == EngineMPV {
		return compileMPVGraph(g) != nil
	}
	if e.Kind == EngineLBR {
		return e.lbr != nil && lbrRuntimeShapeOK(e.lbr) && compileLBR(g) != nil
	}
	if e.Kind == EngineCastle || e.Kind == EngineGough {
		if e.Kind == EngineCastle {
			return e.castle != nil && castleRuntimeShapeOK(e.castle) && castleEligible(nfagraph.ExpandLiterals(g))
		}
		return e.gough != nil && goughRuntimeShapeOK(e.gough) && castleEligible(nfagraph.ExpandLiterals(g))
	}
	if len(g.Nodes) > 4096 || g.Flow == nil || g.Flow.EdgeCount() > 16384 {
		return false
	}
	// 其余专用引擎只接受可消费的字节图；遇到 Unicode、断言或控制节点
	// 必须由调用方回退到通用确认路径，避免错误报告。
	if !dfaLikeGraph(g) {
		return false
	}
	switch e.Kind {
	case EngineLimEx:
		return e.bitNFA != nil && e.bitNFA.validate() == nil && validateBitStateKinds(g, e.bitNFA) == nil
	case EngineSheng:
		return e.tableNFA != nil && e.tableNFA.validate() == nil && validateTableStateKinds(g, e.tableNFA) == nil
	case EngineMcSheng, EngineVermicelli:
		return e.sparseNFA != nil && e.sparseNFA.validate() == nil && validateSparseStateKinds(g, e.sparseNFA) == nil
	case EngineTamarama:
		return e.rangeNFA != nil && e.rangeNFA.validate() == nil && validateRangeStateKinds(g, e.rangeNFA) == nil
	case EngineShufti, EngineTruffle:
		return e.nibbleNFA != nil && e.nibbleNFA.validate() == nil && validateNibbleStateKinds(g, e.nibbleNFA) == nil
	default:
		return false
	}
}

func dfaLikeGraph(g *nfagraph.Graph) bool {
	if g == nil {
		return false
	}
	for _, id := range g.NodeIDs() {
		n := g.Nodes[id]
		if n == nil {
			return false
		}
		switch n.Kind {
		case nfagraph.KindStart, nfagraph.KindAccept, nfagraph.KindLiteral, nfagraph.KindClass, nfagraph.KindSplit, nfagraph.KindJoin, nfagraph.KindReport:
			if n.Kind == nfagraph.KindClass && n.Unicode != nil {
				return false
			}
		default:
			return false
		}
	}
	return true
}

// preferredMatchAt 按引擎类型选择其主状态布局，避免多个编译布局并存时
// 意外落到通用表路径。返回值的最后一项表示是否存在对应专用布局。
func (e *Engine) preferredMatchAt(data []byte, start, maxSteps, maxResults int) ([]int, int, bool, bool) {
	return e.preferredMatchAtInto(data, start, maxSteps, maxResults, nil)
}

// preferredMatchAtInto 将可复用的结束偏移缓冲传给支持该布局的专用执行器。
func (e *Engine) preferredMatchAtInto(data []byte, start, maxSteps, maxResults int, dst []int) ([]int, int, bool, bool) {
	// 专用布局只会由编译阶段验证通过的字节图构建。扫描阶段重复遍历
	// 整张图会让短输入的调度成本超过实际匹配成本，因此只检查布局是否存在。
	if e == nil || e.Program == nil || e.Program.Graph == nil {
		return nil, 0, false, false
	}
	// Report 节点携带内部规则标识，专用位图/表布局只传播位置，
	// 因此包含此类节点时统一使用可保留标识的确认路径。
	if e.Program.HasReports() {
		v, n, stop := e.Program.matchAtLimitBudget(data, start, maxResults, false, false, maxSteps)
		return v, n, stop, true
	}
	switch e.Kind {
	case EngineCastle:
		if e.castle != nil && castleRuntimeShapeOK(e.castle) {
			v, n, stop := e.castle.MatchAtBudget(data, start, maxSteps, maxResults)
			return v, n, stop, true
		}
	case EngineGough:
		if e.gough != nil && goughRuntimeShapeOK(e.gough) {
			v, n, stop := e.gough.MatchAtBudget(data, start, maxSteps, maxResults)
			return v, n, stop, true
		}
	case EngineLBR:
		if e.lbr != nil && lbrRuntimeShapeOK(e.lbr) {
			v, n, stop := e.lbr.MatchAtBudget(data, start, maxSteps, maxResults)
			return v, n, stop, true
		}
	case EngineLimEx:
		if e.bitNFA != nil && bitRuntimeShapeOK(e.bitNFA) {
			v, n, stop := e.bitNFA.MatchAtBudgetInto(data, start, maxSteps, maxResults, dst)
			return v, n, stop, true
		}
	case EngineMcSheng:
		if e.sparseNFA != nil && sparseRuntimeShapeOK(e.sparseNFA) {
			v, n, stop := e.sparseNFA.MatchAtBudgetInto(data, start, maxSteps, maxResults, dst)
			return v, n, stop, true
		}
	case EngineTamarama:
		if e.rangeNFA != nil && rangeRuntimeShapeOK(e.rangeNFA) {
			v, n, stop := e.rangeNFA.MatchAtBudgetInto(data, start, maxSteps, maxResults, dst)
			return v, n, stop, true
		}
	case EngineShufti, EngineTruffle:
		if e.Kind == EngineTruffle && e.truffle != nil && e.truffle.validate() {
			v, n, stop := e.truffle.MatchAtBudget(data, start, maxSteps, maxResults)
			return v, n, stop, true
		}
		if e.nibbleNFA != nil && nibbleRuntimeShapeOK(e.nibbleNFA) {
			v, n, stop := e.nibbleNFA.MatchAtBudgetInto(data, start, maxSteps, maxResults, dst)
			return v, n, stop, true
		}
	case EngineSheng, EngineVermicelli:
		if e.Kind == EngineVermicelli && e.vermicelli != nil && e.vermicelli.validate() {
			v, n, stop := e.vermicelli.MatchAtBudget(data, start, maxSteps, maxResults)
			return v, n, stop, true
		}
		if e.Kind == EngineVermicelli && e.sparseNFA != nil && sparseRuntimeShapeOK(e.sparseNFA) {
			v, n, stop := e.sparseNFA.MatchAtBudgetInto(data, start, maxSteps, maxResults, dst)
			return v, n, stop, true
		}
		if e.Kind == EngineSheng && e.sheng != nil && e.sheng.validate() {
			v, n, stop := e.sheng.core.MatchAtBudgetInto(data, start, maxSteps, maxResults, dst)
			return v, n, stop, true
		}
		if e.tableNFA != nil && tableRuntimeShapeOK(e.tableNFA) {
			v, n, stop := e.tableNFA.MatchAtBudgetInto(data, start, maxSteps, maxResults, dst)
			return v, n, stop, true
		}
	}
	return nil, 0, false, false
}

func (e *Engine) MatchAt(data []byte, start int) []int {
	if e == nil || e.Program == nil {
		return nil
	}
	if !e.withinLengthBounds(data, start) {
		return nil
	}
	data = e.boundedInput(data, start)
	if ends, _, _, ok := e.preferredMatchAt(data, start, 0, 0); ok {
		return ends
	}
	if e.castle != nil && castleRuntimeShapeOK(e.castle) {
		return e.castle.MatchAt(data, start)
	}
	if e.gough != nil && goughRuntimeShapeOK(e.gough) {
		return e.gough.MatchAt(data, start)
	}
	if repeatUsable(e) {
		return e.repeat.MatchAt(data, start)
	}
	if e.mpv != nil && e.mpv.validate() == nil {
		return e.mpv.MatchAt(data, start)
	}
	if e.byteNFA != nil && byteRuntimeShapeOK(e.byteNFA) {
		return e.byteNFA.MatchAt(data, start)
	}
	if e.bitNFA != nil && bitRuntimeShapeOK(e.bitNFA) {
		return e.bitNFA.MatchAt(data, start)
	}
	if e.sparseNFA != nil && sparseRuntimeShapeOK(e.sparseNFA) {
		return e.sparseNFA.MatchAt(data, start)
	}
	if e.nibbleNFA != nil && nibbleRuntimeShapeOK(e.nibbleNFA) {
		return e.nibbleNFA.MatchAt(data, start)
	}
	if e.rangeNFA != nil && rangeRuntimeShapeOK(e.rangeNFA) {
		return e.rangeNFA.MatchAt(data, start)
	}
	if e.tableNFA != nil && tableRuntimeShapeOK(e.tableNFA) {
		return e.tableNFA.MatchAt(data, start)
	}
	if e.lbr != nil && lbrRuntimeShapeOK(e.lbr) {
		return e.lbr.MatchAt(data, start)
	}
	// 指定引擎没有可用专用布局时，使用通用图确认，避免误用
	// Program 内部缓存的其他引擎布局。
	return e.Program.matchAtLimit(data, start, 0, false, false)
}

// MatchAtLimit 在执行状态预算内返回指定起点的结束偏移。
func (e *Engine) MatchAtLimit(data []byte, start, limit int) []int {
	if e == nil || e.Program == nil || limit < 0 {
		return nil
	}
	if !e.withinLengthBounds(data, start) {
		return nil
	}
	data = e.boundedInput(data, start)
	if ends, _, _, ok := e.preferredMatchAt(data, start, 0, limit); ok {
		if limit > 0 && len(ends) > limit {
			ends = ends[:limit]
		}
		return ends
	}
	if e.castle != nil && castleRuntimeShapeOK(e.castle) {
		ends, _, _ := e.castle.MatchAtBudget(data, start, 0, limit)
		return ends
	}
	if e.gough != nil && goughRuntimeShapeOK(e.gough) {
		ends, _, _ := e.gough.MatchAtBudget(data, start, 0, limit)
		return ends
	}
	if repeatUsable(e) {
		return e.repeat.MatchAtLimit(data, start, limit)
	}
	if e.mpv != nil && e.mpv.validate() == nil {
		ends := e.mpv.MatchAt(data, start)
		if limit > 0 && len(ends) > limit {
			ends = ends[:limit]
		}
		return ends
	}
	if e.byteNFA != nil && byteRuntimeShapeOK(e.byteNFA) {
		ends := e.byteNFA.MatchAt(data, start)
		if limit > 0 && len(ends) > limit {
			ends = ends[:limit]
		}
		return ends
	}
	if e.tableNFA != nil && tableRuntimeShapeOK(e.tableNFA) {
		ends := e.tableNFA.MatchAt(data, start)
		if limit > 0 && len(ends) > limit {
			ends = ends[:limit]
		}
		return ends
	}
	if e.bitNFA != nil && bitRuntimeShapeOK(e.bitNFA) {
		ends := e.bitNFA.MatchAt(data, start)
		if limit > 0 && len(ends) > limit {
			ends = ends[:limit]
		}
		return ends
	}
	if e.sparseNFA != nil && sparseRuntimeShapeOK(e.sparseNFA) {
		ends := e.sparseNFA.MatchAt(data, start)
		if limit > 0 && len(ends) > limit {
			ends = ends[:limit]
		}
		return ends
	}
	if e.nibbleNFA != nil && nibbleRuntimeShapeOK(e.nibbleNFA) {
		ends := e.nibbleNFA.MatchAt(data, start)
		if limit > 0 && len(ends) > limit {
			ends = ends[:limit]
		}
		return ends
	}
	if e.rangeNFA != nil && rangeRuntimeShapeOK(e.rangeNFA) {
		ends := e.rangeNFA.MatchAt(data, start)
		if limit > 0 && len(ends) > limit {
			ends = ends[:limit]
		}
		return ends
	}
	if e.lbr != nil && lbrRuntimeShapeOK(e.lbr) {
		ends, _, _ := e.lbr.MatchAtBudget(data, start, 0, limit)
		return ends
	}
	return e.Program.matchAtLimit(data, start, limit, false, false)
}

// Spans 返回引擎在输入中的全部区间，并按起点、终点稳定排序。
func (e *Engine) Spans(data []byte) []Span {
	return e.SpansLimit(data, 0)
}

// SpansLimit 返回不超过 limit 个区间；零值表示不限制。
func (e *Engine) SpansLimit(data []byte, limit int) []Span {
	if e == nil || e.Program == nil || limit < 0 {
		return nil
	}
	// 纯文字图直接按候选位置扫描，避免每个起点重新创建 NFA 工作集。
	if e.Kind == EngineMPV && e.mpv != nil && e.mpv.validate() == nil {
		// MPV 的文字集合已在编译阶段完成归一化。扫描阶段直接复用，
		// 避免每次调用重新遍历图并构建临时状态布局。
		return e.mpv.spans(data, limit)
	}
	if e.Kind == EngineLBR && e.lbr != nil && lbrRuntimeShapeOK(e.lbr) {
		// 复用 LBR 自身的候选枚举，保证断言首节点的空掩码不会
		// 被错误过滤，同时避免引擎层重复维护一套扫描循环。
		return e.lbr.Spans(data, limit)
	}
	if e.Independent() && (e.Kind == EngineLimEx || e.Kind == EngineSheng || e.Kind == EngineMcSheng || e.Kind == EngineTamarama || e.Kind == EngineVermicelli || e.Kind == EngineShufti || e.Kind == EngineTruffle) {
		out := make([]Span, 0, initialSpanCapacity(data, limit))
		prefix, candidateMask, acceptsEmpty, backend := e.executionCandidates()
		forEachNFAStart(data, prefix, candidateMask, acceptsEmpty, backend, func(start int) bool {
			bounded := e.boundedInput(data, start)
			ends, _, _, ok := e.preferredMatchAt(bounded, start, 0, 0)
			if !ok {
				// 预过滤只能缩小候选集合，专用布局失效时必须对当前
				// 候选执行完整确认，不能把候选误当成最终结果。
				ends = e.Program.MatchAt(bounded, start)
			}
			if !ok && len(ends) == 0 {
				// 当前起点确认失败后继续检查后续候选，不能提前结束全局扫描。
				return true
			}
			for _, end := range ends {
				out = append(out, Span{From: start, To: end})
				if limit > 0 && len(out) >= limit {
					return false
				}
			}
			return true
		})
		if len(out) > 0 || e.Independent() {
			return out
		}
	}
	if e.byteNFA != nil {
		return e.byteNFA.Spans(data, limit)
	}
	if e.bitNFA != nil && bitRuntimeShapeOK(e.bitNFA) {
		return e.bitNFA.Spans(data, limit)
	}
	if e.sparseNFA != nil && sparseRuntimeShapeOK(e.sparseNFA) {
		return e.sparseNFA.Spans(data, limit)
	}
	if e.nibbleNFA != nil && nibbleRuntimeShapeOK(e.nibbleNFA) {
		return e.nibbleNFA.Spans(data, limit)
	}
	if e.rangeNFA != nil && rangeRuntimeShapeOK(e.rangeNFA) {
		return e.rangeNFA.Spans(data, limit)
	}
	if repeatUsable(e) {
		return e.repeatSpans(data, limit)
	}
	if e.tableNFA != nil && tableRuntimeShapeOK(e.tableNFA) {
		return e.tableNFA.Spans(data, limit)
	}
	if e.mpv != nil && e.mpv.validate() == nil {
		return e.mpv.Spans(data, limit)
	}
	if e.lbr != nil && lbrRuntimeShapeOK(e.lbr) {
		return e.lbr.Spans(data, limit)
	}
	if e.castle != nil && castleRuntimeShapeOK(e.castle) {
		return e.castle.Spans(data, limit)
	}
	if e.gough != nil && goughRuntimeShapeOK(e.gough) {
		return e.gough.Spans(data, limit)
	}
	return e.Program.SpansLimit(data, limit)
}

// executionCandidates 返回当前专用布局的起点过滤元数据。过滤仅用于减少
// 不可能的起点；缺少可证明元数据时返回空掩码，枚举器会保守地逐点确认。
func (e *Engine) executionCandidates() ([]byte, [4]uint64, bool, simd.Backend) {
	if e == nil {
		return nil, [4]uint64{}, false, nil
	}
	if e.bitNFA != nil && bitRuntimeShapeOK(e.bitNFA) {
		return e.bitNFA.prefix, e.bitNFA.firstMask, e.bitNFA.acceptsEmpty, e.bitNFA.backend
	}
	if e.tableNFA != nil && tableRuntimeShapeOK(e.tableNFA) {
		return e.tableNFA.prefix, e.tableNFA.firstMask, e.tableNFA.acceptsEmpty, nil
	}
	if e.sparseNFA != nil && sparseRuntimeShapeOK(e.sparseNFA) {
		return e.sparseNFA.prefix, e.sparseNFA.firstMask, e.sparseNFA.acceptsEmpty, e.sparseNFA.backend
	}
	if e.rangeNFA != nil && rangeRuntimeShapeOK(e.rangeNFA) {
		return e.rangeNFA.prefix, e.rangeNFA.firstMask, e.rangeNFA.acceptsEmpty, e.rangeNFA.backend
	}
	if e.nibbleNFA != nil && nibbleRuntimeShapeOK(e.nibbleNFA) {
		return e.nibbleNFA.prefix, e.nibbleNFA.firstMask, e.nibbleNFA.acceptsEmpty, e.nibbleNFA.backend
	}
	return e.executionPrefix(), [4]uint64{}, false, nil
}

func (e *Engine) executionPrefix() []byte {
	if e == nil {
		return nil
	}
	if e.bitNFA != nil && bitRuntimeShapeOK(e.bitNFA) {
		return e.bitNFA.prefix
	}
	if e.tableNFA != nil && tableRuntimeShapeOK(e.tableNFA) {
		return e.tableNFA.prefix
	}
	if e.sparseNFA != nil && sparseRuntimeShapeOK(e.sparseNFA) {
		return e.sparseNFA.prefix
	}
	if e.rangeNFA != nil && rangeRuntimeShapeOK(e.rangeNFA) {
		return e.rangeNFA.prefix
	}
	if e.nibbleNFA != nil && nibbleRuntimeShapeOK(e.nibbleNFA) {
		return e.nibbleNFA.prefix
	}
	if e.byteNFA != nil {
		return e.byteNFA.prefix
	}
	if e.castle != nil {
		return e.castle.prefix
	}
	if e.gough != nil {
		return e.gough.prefix
	}
	if e.lbr != nil && lbrRuntimeShapeOK(e.lbr) {
		return e.lbr.prefix
	}
	return nil
}

// initialSpanCapacity 为常见的少量结果预留空间，避免短输入扫描时反复扩容；
// 上限保持很小，防止长输入或无限制调用提前占用过多内存。
func initialSpanCapacity(data []byte, limit int) int {
	const commonSpanCount = 4
	if limit > 0 && limit < commonSpanCount {
		return limit
	}
	if len(data)+1 < commonSpanCount {
		return len(data) + 1
	}
	return commonSpanCount
}

func (p *mpvProgram) spans(data []byte, limit int) []Span {
	if p == nil || limit < 0 {
		return nil
	}
	out := make([]Span, 0, initialSpanCapacity(data, limit))
	for _, literal := range p.literals {
		if len(literal) == 0 {
			continue
		}
		for from := 0; from+len(literal) <= len(data); {
			offset := bytes.Index(data[from:], literal)
			if offset < 0 {
				break
			}
			start := from + offset
			out = append(out, Span{From: start, To: start + len(literal)})
			if limit > 0 && len(out) >= limit {
				return out
			}
			// 步进一个字节以保留重叠文字结果。
			from = start + 1
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].From != out[j].From {
			return out[i].From < out[j].From
		}
		return out[i].To < out[j].To
	})
	if len(out) < 2 {
		return out
	}
	uniq := out[:1]
	for _, span := range out[1:] {
		if span != uniq[len(uniq)-1] {
			uniq = append(uniq, span)
		}
	}
	if limit > 0 && len(uniq) > limit {
		uniq = uniq[:limit]
	}
	return uniq
}

// SpansRangeLimit 返回完全位于半开区间内的引擎结果。
func (e *Engine) SpansRangeLimit(data []byte, from, to, limit int) []Span {
	if e == nil || e.Program == nil || limit < 0 || from < 0 || to < from || to > len(data) {
		return nil
	}
	if repeatUsable(e) {
		hits := e.repeat.FindRangeLimit(data, from, to, limit)
		out := make([]Span, 0, len(hits))
		for _, hit := range hits {
			out = append(out, Span{From: hit[0], To: hit[1]})
		}
		return out
	}
	if e.mpv != nil && e.mpv.validate() == nil {
		out := make([]Span, 0)
		endsBuf := make([]int, 0, len(e.mpv.literals))
		for start := from; start <= to; start++ {
			if !e.candidateStartAllowed(data, start) {
				continue
			}
			endsBuf = e.mpv.MatchAtInto(data, start, endsBuf[:0])
			for _, end := range endsBuf {
				if end > to {
					continue
				}
				span := Span{From: start, To: end}
				out = append(out, span)
				if limit > 0 && len(out) >= limit {
					return out
				}
			}
		}
		return out
	}
	out := make([]Span, 0, initialSpanCapacity(data[to-from:], limit))
	seen := make(map[Span]struct{})
	visit := func(start int) bool {
		for _, end := range e.MatchAt(data, start) {
			if end > to {
				continue
			}
			span := Span{From: start, To: end}
			if _, ok := seen[span]; ok {
				continue
			}
			seen[span] = struct{}{}
			out = append(out, span)
			if limit > 0 && len(out) >= limit {
				return false
			}
		}
		return true
	}
	prefix := e.executionPrefix()
	if len(prefix) > 0 {
		for start := from; start+len(prefix) <= to; {
			offset := bytes.Index(data[start:to], prefix)
			if offset < 0 {
				break
			}
			if !visit(start + offset) {
				return out
			}
			start += offset + 1
		}
		return out
	}
	for start := from; start <= to; start++ {
		if !e.candidateStartAllowed(data, start) {
			continue
		}
		if !visit(start) {
			return out
		}
	}
	return out
}

// SpansRange 返回区间内全部结果。
func (e *Engine) SpansRange(data []byte, from, to int) []Span {
	return e.SpansRangeLimit(data, from, to, 0)
}

// SpansBudget 在全局步骤和结果预算内扫描引擎输出。
func (e *Engine) SpansBudget(data []byte, maxSteps, maxResults int) ([]Span, int, bool) {
	if e == nil || e.Program == nil || maxSteps < 0 || maxResults < 0 {
		return nil, 0, false
	}
	if repeatUsable(e) {
		hits, steps, stopped := e.repeat.FindBudget(data, maxSteps, maxResults)
		out := make([]Span, 0, len(hits))
		for _, hit := range hits {
			out = append(out, Span{From: hit[0], To: hit[1]})
		}
		return out, steps, stopped
	}
	if e.mpv != nil && e.mpv.validate() == nil {
		out := make([]Span, 0)
		endsBuf := make([]int, 0, len(e.mpv.literals))
		steps := 0
		for start := 0; start <= len(data); start++ {
			if !e.candidateStartAllowed(data, start) {
				continue
			}
			remaining := 0
			if maxSteps > 0 {
				remaining = maxSteps - steps
				if remaining <= 0 {
					return out, steps, true
				}
			}
			maxResult := 0
			if maxResults > 0 {
				maxResult = maxResults - len(out)
			}
			ends, used, stopped := e.mpv.MatchAtBudgetInto(data, start, remaining, maxResult, endsBuf[:0])
			endsBuf = ends
			steps += used
			for _, end := range ends {
				out = append(out, Span{From: start, To: end})
			}
			if stopped || maxResults > 0 && len(out) >= maxResults {
				return out, steps, true
			}
		}
		return out, steps, false
	}
	if e.castle != nil && castleRuntimeShapeOK(e.castle) || e.gough != nil && goughRuntimeShapeOK(e.gough) || e.lbr != nil && lbrRuntimeShapeOK(e.lbr) {
		out := make([]Span, 0)
		endsBuf := make([]int, 0, 4)
		steps := 0
		for start := 0; start <= len(data); start++ {
			if !e.candidateStartAllowed(data, start) {
				continue
			}
			remaining := 0
			if maxSteps > 0 {
				remaining = maxSteps - steps
				if remaining <= 0 {
					return out, steps, true
				}
			}
			resultLimit := 0
			if maxResults > 0 {
				resultLimit = maxResults - len(out)
				if resultLimit <= 0 {
					return out, steps, true
				}
			}
			var ends []int
			var used int
			var stopped bool
			switch {
			case e.castle != nil && castleRuntimeShapeOK(e.castle):
				ends, used, stopped = e.castle.matchAtBudgetUncheckedInto(data, start, remaining, resultLimit, endsBuf[:0])
				endsBuf = ends
			case e.gough != nil && goughRuntimeShapeOK(e.gough):
				ends, used, stopped = e.gough.matchAtBudgetUncheckedInto(data, start, remaining, resultLimit, endsBuf[:0])
				endsBuf = ends
			default:
				ends, used, stopped = e.lbr.matchAtBudgetInto(data, start, remaining, resultLimit, endsBuf[:0])
				endsBuf = ends
			}
			steps += used
			for _, end := range ends {
				out = append(out, Span{From: start, To: end})
			}
			if stopped || maxResults > 0 && len(out) >= maxResults {
				if stopped && len(out) == 0 {
					return nil, steps, true
				}
				return out, steps, true
			}
		}
		return out, steps, false
	}
	if e.byteNFA == nil && e.tableNFA == nil && e.bitNFA == nil && e.sparseNFA == nil && e.rangeNFA == nil && e.nibbleNFA == nil && e.repeat == nil && e.mpv == nil && e.lbr == nil {
		return e.Program.SpansBudget(data, maxSteps, maxResults)
	}
	out := make([]Span, 0)
	steps := 0
	// 复用每个起点确认所需的结束偏移缓冲，避免预算扫描为每个起点
	// 创建 Context 和结果切片。该缓冲只保存当前起点结果，写入 out 后即可复用。
	endsBuf := make([]int, 0, 8)
	for start := 0; start <= len(data); start++ {
		if !e.candidateStartAllowed(data, start) {
			continue
		}
		remaining := 0
		if maxSteps > 0 {
			remaining = maxSteps - steps
			if remaining <= 0 {
				return out, steps, true
			}
		}
		resultLimit := 0
		if maxResults > 0 {
			resultLimit = maxResults - len(out)
			if resultLimit <= 0 {
				return out, steps, true
			}
		}
		var stopped bool
		var ok bool
		// 优先复用已验证的专用布局；布局校验失败时走通用确认器，
		// 两条路径均保留相同的起点预算语义。
		var ends []int
		ends, _, stopped, ok = e.preferredMatchAtInto(data, start, remaining, resultLimit, endsBuf[:0])
		if !ok {
			ends, _, stopped = e.Program.matchAtLimitBudget(data, start, resultLimit, false, false, remaining)
		}
		endsBuf = ends
		// 全局扫描以每个起点的一次确认作为统一成本单位，避免
		// 不同布局的内部状态访问量改变既有块模式预算语义。
		steps++
		for _, end := range ends {
			out = append(out, Span{From: start, To: end})
		}
		if stopped {
			return out, steps, true
		}
	}
	return out, steps, false
}

// MatchAtRange 返回指定起点且结束偏移位于区间内的结果。
func (e *Engine) MatchAtRange(data []byte, start, from, to, limit int) []int {
	if e == nil || e.Program == nil || limit < 0 || start < 0 || start > len(data) || from < 0 || to < from || to > len(data) {
		return nil
	}
	out := make([]int, 0)
	for _, end := range e.MatchAt(data, start) {
		if end >= from && end <= to {
			out = append(out, end)
			if limit > 0 && len(out) >= limit {
				break
			}
		}
	}
	return out
}

// MatchFirst 返回指定起点最早接受的结束偏移。
func (e *Engine) MatchFirst(data []byte, start int) (int, bool) {
	ends := e.MatchAt(data, start)
	if len(ends) == 0 {
		return 0, false
	}
	return ends[0], true
}

// MatchLongest 返回指定起点最长接受的结束偏移。
func (e *Engine) MatchLongest(data []byte, start int) (int, bool) {
	ends := e.MatchAt(data, start)
	if len(ends) == 0 {
		return 0, false
	}
	return ends[len(ends)-1], true
}

// AcceptsEmpty 判断引擎是否可在空输入上接受。
func (e *Engine) AcceptsEmpty() bool { return e != nil && containsInt(e.MatchAt(nil, 0), 0) }

// HasUnicode 判断底层图是否依赖 Unicode 字符类。
func (e *Engine) HasUnicode() bool { return e != nil && e.Program != nil && e.Program.HasUnicode() }

func (e *Engine) repeatSpans(data []byte, limit int) []Span {
	if e == nil || e.repeat == nil || limit < 0 {
		return nil
	}
	hits := e.repeat.FindLimit(data, limit)
	out := make([]Span, 0, len(hits))
	for _, hit := range hits {
		out = append(out, Span{From: hit[0], To: hit[1]})
	}
	return out
}

// MatchAtWithContext 执行匹配并更新上下文统计；预算耗尽时提前返回。
func (e *Engine) MatchAtWithContext(data []byte, start int, ctx *Context) []int {
	if e == nil || e.Program == nil || start < 0 || start > len(data) {
		return nil
	}
	if !e.withinLengthBounds(data, start) {
		return nil
	}
	data = e.boundedInput(data, start)
	if ctx == nil {
		return e.MatchAt(data, start)
	}
	if err := ctx.Validate(); err != nil {
		return nil
	}
	ctx.Reset()
	defer func() {
		if ctx.MaxResults > 0 && ctx.Results >= ctx.MaxResults {
			ctx.Stopped = true
		}
	}()
	if ends, steps, stopped, ok := e.preferredMatchAtInto(data, start, ctx.MaxSteps, ctx.MaxResults, ctx.ends[:0]); ok {
		ctx.ends = ends
		ctx.Steps, ctx.Results, ctx.Stopped = steps, len(ends), stopped
		if stopped && ends == nil {
			return nil
		}
		return ends
	}
	if e.castle != nil && castleRuntimeShapeOK(e.castle) {
		ends, steps, stopped := e.castle.MatchAtBudget(data, start, ctx.MaxSteps, ctx.MaxResults)
		ctx.Steps, ctx.Results, ctx.Stopped = steps, len(ends), stopped
		if stopped && ends == nil {
			return nil
		}
		return ends
	}
	if e.gough != nil && goughRuntimeShapeOK(e.gough) {
		ends, steps, stopped := e.gough.MatchAtBudget(data, start, ctx.MaxSteps, ctx.MaxResults)
		ctx.Steps, ctx.Results, ctx.Stopped = steps, len(ends), stopped
		if stopped && ends == nil {
			return nil
		}
		return ends
	}
	if e.byteNFA != nil && byteRuntimeShapeOK(e.byteNFA) {
		ends, steps, stopped := e.byteNFA.matchAtBudgetUnchecked(data, start, ctx.MaxSteps, ctx.MaxResults, ctx.ends[:0])
		ctx.ends = ends
		ctx.Steps, ctx.Results, ctx.Stopped = steps, len(ends), stopped
		if stopped && ends == nil {
			return nil
		}
		return ends
	}
	if repeatUsable(e) {
		ends, steps, stopped := e.repeat.MatchAtBudgetInto(data, start, ctx.MaxSteps, ctx.MaxResults, ctx.ends[:0])
		if stopped && ends == nil {
			ctx.ends = ctx.ends[:0]
		} else {
			ctx.ends = ends
		}
		ctx.Steps, ctx.Results, ctx.Stopped = steps, len(ends), stopped
		if stopped && ends == nil {
			return nil
		}
		return ends
	}
	if e.bitNFA != nil && bitRuntimeShapeOK(e.bitNFA) {
		ends, steps, stopped := e.bitNFA.MatchAtBudgetInto(data, start, ctx.MaxSteps, ctx.MaxResults, ctx.ends[:0])
		ctx.ends = ends
		ctx.Steps, ctx.Results, ctx.Stopped = steps, len(ends), stopped
		if stopped && ends == nil {
			return nil
		}
		return ends
	}
	if e.sparseNFA != nil && sparseRuntimeShapeOK(e.sparseNFA) {
		ends, steps, stopped := e.sparseNFA.MatchAtBudgetInto(data, start, ctx.MaxSteps, ctx.MaxResults, ctx.ends[:0])
		ctx.ends = ends
		ctx.Steps, ctx.Results, ctx.Stopped = steps, len(ends), stopped
		if stopped && ends == nil {
			return nil
		}
		return ends
	}
	if e.rangeNFA != nil && rangeRuntimeShapeOK(e.rangeNFA) {
		ends, steps, stopped := e.rangeNFA.MatchAtBudgetInto(data, start, ctx.MaxSteps, ctx.MaxResults, ctx.ends[:0])
		ctx.ends = ends
		ctx.Steps, ctx.Results, ctx.Stopped = steps, len(ends), stopped
		if stopped && ends == nil {
			return nil
		}
		return ends
	}
	if e.nibbleNFA != nil && nibbleRuntimeShapeOK(e.nibbleNFA) {
		ends, steps, stopped := e.nibbleNFA.MatchAtBudgetInto(data, start, ctx.MaxSteps, ctx.MaxResults, ctx.ends[:0])
		ctx.ends = ends
		ctx.Steps, ctx.Results, ctx.Stopped = steps, len(ends), stopped
		if stopped && ends == nil {
			return nil
		}
		return ends
	}
	if e.tableNFA != nil && tableRuntimeShapeOK(e.tableNFA) {
		ends, steps, stopped := e.tableNFA.MatchAtBudgetInto(data, start, ctx.MaxSteps, ctx.MaxResults, ctx.ends[:0])
		ctx.ends = ends
		ctx.Steps, ctx.Results, ctx.Stopped = steps, len(ends), stopped
		if stopped && ends == nil {
			return nil
		}
		return ends
	}
	if e.lbr != nil && lbrRuntimeShapeOK(e.lbr) {
		ends, steps, stopped := e.lbr.matchAtBudgetInto(data, start, ctx.MaxSteps, ctx.MaxResults, ctx.ends[:0])
		ctx.ends = ends
		ctx.Steps, ctx.Results, ctx.Stopped = steps, len(ends), stopped
		if stopped && ends == nil {
			return nil
		}
		return ends
	}
	if e.mpv != nil && e.mpv.validate() == nil {
		ends, steps, stopped := e.mpv.MatchAtBudgetInto(data, start, ctx.MaxSteps, ctx.MaxResults, ctx.ends[:0])
		ctx.ends = ends
		ctx.Steps, ctx.Results, ctx.Stopped = steps, len(ends), stopped
		if stopped && ends == nil {
			return nil
		}
		return ends
	}
	// 所有加速布局均不可用时，统一回到通用确认器，并继续沿用
	// 当前调用的步骤/结果预算；布局存在但校验失败不能丢失预算语义。
	if e.Program != nil {
		ends, steps, stopped := e.Program.matchAtLimitBudget(data, start, ctx.MaxResults, false, false, ctx.MaxSteps)
		ctx.Steps, ctx.Results, ctx.Stopped = steps, len(ends), stopped
		if stopped && ends == nil {
			return nil
		}
		return ends
	}
	// 独立后端仍使用统一结果接口，步骤预算按实际扫描区间核算。
	ends := e.MatchAtLimit(data, start, 0)
	ctx.Steps = len(data) - start
	if ctx.MaxSteps > 0 && ctx.Steps > ctx.MaxSteps {
		ctx.Steps = ctx.MaxSteps
		ctx.Stopped = true
		return nil
	}
	ctx.Results = len(ends)
	if ctx.MaxResults > 0 && len(ends) > ctx.MaxResults {
		ends = ends[:ctx.MaxResults]
		ctx.Results = len(ends)
		ctx.Stopped = true
	}
	return ends
}

// MatchAtBudget 返回预算内的匹配结果和执行步数。
func (e *Engine) MatchAtBudget(data []byte, start, maxSteps, maxResults int) ([]int, int, bool) {
	if e == nil || e.Program == nil || !e.withinLengthBounds(data, start) {
		return nil, 0, false
	}
	ctx := &Context{MaxSteps: maxSteps, MaxResults: maxResults}
	ends := e.MatchAtWithContext(data, start, ctx)
	// 达到结果上限即视为预算截断；步骤上限只有执行器实际提前终止时才置位。
	return ends, ctx.Steps, ctx.Stopped || maxResults > 0 && ctx.Results >= maxResults
}

// withinLengthBounds 在进入具体后端前过滤不可能命中的起点。
// 未知上界（循环图）不会触发最大长度截断，确保保守性。
func (e *Engine) withinLengthBounds(data []byte, start int) bool {
	if e == nil || e.Program == nil || start < 0 || start > len(data) {
		return false
	}
	p := e.Program
	if !p.boundsKnown {
		return true
	}
	if p.minBytes > 0 && len(data)-start < p.minBytes {
		return false
	}
	return true
}

func (e *Engine) boundedInput(data []byte, start int) []byte {
	if e == nil || e.Program == nil || !e.Program.boundsKnown || e.Program.maxBytes < 0 || e.Program.HasAssertions() || start < 0 || start > len(data) {
		return data
	}
	if e.Program.maxBytes > len(data)-start {
		return data
	}
	end := start + e.Program.maxBytes
	if end >= len(data) {
		return data
	}
	return data[:end]
}

func (e *Engine) candidateStartAllowed(data []byte, start int) bool {
	if e == nil || e.Program == nil || start < 0 || start > len(data) {
		return false
	}
	prefix := e.executionPrefix()
	if len(prefix) > 0 {
		return prefixMatchesAt(data, start, prefix)
	}
	if e.Program.HasUnicode() {
		// 首字节掩码只描述单字节字符类，Unicode 节点的首个
		// UTF-8 字节无法由该掩码安全推导。
		return true
	}
	if start == len(data) || e.Program.metadataKnown && e.Program.acceptsEmpty {
		return true
	}
	if e.Program.metadataKnown {
		if byteMaskEmpty(e.Program.firstMask) {
			// 元数据无法推导首字节时保留候选，交给完整执行路径确认。
			return true
		}
		return hasFirstByte(e.Program.firstMask, data[start])
	}
	return true
}

// BackendName 返回引擎稳定名称。
func (e *Engine) BackendName() string {
	if e == nil {
		return ""
	}
	return e.Kind.String()
}
