// Package combination evaluates boolean expressions over rule hits.
package combination

import (
	"fmt"
	"sort"

	"github.com/smartwalle/scankit/internal/parser"
)

type HitSet map[uint32]bool

// Program 是经过验证的组合表达式执行计划。
type Program struct {
	Root parser.Node
	IDs  []uint32
}

// Compile 验证组合表达式并提取稳定依赖编号。
func Compile(root parser.Node) (*Program, error) {
	if err := Validate(root); err != nil {
		return nil, err
	}
	if NodeRoot(root) == nil {
		return nil, fmt.Errorf("invalid combination expression")
	}
	return &Program{Root: Normalize(root), IDs: append([]uint32(nil), IDs(root)...)}, nil
}

// Evaluate 计算给定命中集合下的表达式结果。
func (p *Program) Evaluate(hits HitSet) bool {
	return p != nil && Evaluate(p.Root, hits)
}

// NewAccumulator 创建可持续接收基础规则命中的状态机。
func (p *Program) NewAccumulator() *Accumulator {
	if p == nil {
		return nil
	}
	return &Accumulator{Program: p, Hits: make(HitSet)}
}

// Accumulator 保存组合表达式跨报告批次的命中状态。
type Accumulator struct {
	Program *Program
	Hits    HitSet
}

// Add 记录一个基础规则命中，并返回表达式是否在本次调用后首次满足。
func (a *Accumulator) Add(id uint32) bool {
	if a == nil || a.Program == nil {
		return false
	}
	allowed := false
	for _, dependency := range a.Program.IDs {
		if dependency == id {
			allowed = true
			break
		}
	}
	if !allowed {
		return false
	}
	before := a.Program.Evaluate(a.Hits)
	if a.Hits == nil {
		a.Hits = make(HitSet)
	}
	a.Hits.Add(id)
	return !before && a.Program.Evaluate(a.Hits)
}

// Has 判断规则是否已在累计集合中命中。
func (a *Accumulator) Has(id uint32) bool {
	return a != nil && a.Hits[id]
}

// EvaluateAfterAdd 记录命中并返回当前表达式结果及是否发生状态变化。
func (a *Accumulator) EvaluateAfterAdd(id uint32) (bool, bool) {
	if a == nil || a.Program == nil {
		return false, false
	}
	before := a.Program.Evaluate(a.Hits)
	allowed := false
	for _, dependency := range a.Program.IDs {
		if dependency == id {
			allowed = true
			break
		}
	}
	if !allowed {
		return before, false
	}
	wasPresent := a.Has(id)
	if wasPresent {
		return before, false
	}
	if a.Hits == nil {
		a.Hits = make(HitSet)
	}
	a.Hits.Add(id)
	after := a.Program.Evaluate(a.Hits)
	return after, !wasPresent
}

// Reset 清除已累计的命中。
func (a *Accumulator) Reset() {
	if a != nil {
		clear(a.Hits)
	}
}

// Snapshot 返回命中集合的排序快照。
func (a *Accumulator) Snapshot() []uint32 {
	if a == nil {
		return nil
	}
	return a.Hits.Snapshot()
}

// Restore 用排序或非排序的规则编号恢复累计状态。
func (a *Accumulator) Restore(ids []uint32) error {
	if a == nil || a.Program == nil {
		return fmt.Errorf("nil combination accumulator")
	}
	allowed := make(map[uint32]struct{}, len(a.Program.IDs))
	for _, id := range a.Program.IDs {
		allowed[id] = struct{}{}
	}
	restored := make(HitSet, len(ids))
	for _, id := range ids {
		if _, ok := allowed[id]; !ok {
			return fmt.Errorf("unknown combination rule %d", id)
		}
		restored.Add(id)
	}
	a.Hits = restored
	return nil
}

// NodeRoot 返回组合节点包裹的实际表达式。
func NodeRoot(root parser.Node) parser.Node {
	if value, ok := root.(parser.Combination); ok {
		return value.Expr
	}
	return root
}

// Clone 创建命中集合副本。
func (h HitSet) Clone() HitSet {
	if h == nil {
		return nil
	}
	out := make(HitSet, len(h))
	for id, hit := range h {
		out[id] = hit
	}
	return out
}

// Add 标记规则编号为已命中。
func (h HitSet) Add(id uint32) {
	if h != nil {
		h[id] = true
	}
}

// Remove 清除规则编号的命中状态。
func (h HitSet) Remove(id uint32) {
	if h != nil {
		delete(h, id)
	}
}

// Count 返回命中规则数量。
func (h HitSet) Count() int {
	count := 0
	for _, hit := range h {
		if hit {
			count++
		}
	}
	return count
}

// HasAny 判断集合中是否至少有一个编号命中。
func (h HitSet) HasAny(ids ...uint32) bool {
	for _, id := range ids {
		if h[id] {
			return true
		}
	}
	return false
}

// HasAll 判断集合中是否所有编号均命中。
func (h HitSet) HasAll(ids ...uint32) bool {
	for _, id := range ids {
		if !h[id] {
			return false
		}
	}
	return true
}

// Merge 将另一份命中快照并入当前集合。
func (h HitSet) Merge(other HitSet) {
	if h == nil {
		return
	}
	for id, hit := range other {
		if hit {
			h[id] = true
		}
	}
}

// Snapshot 返回排序后的命中规则编号。
func (h HitSet) Snapshot() []uint32 {
	out := make([]uint32, 0, len(h))
	for id, hit := range h {
		if hit {
			out = append(out, id)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// Equal 判断两个命中集合的布尔状态是否一致。
func (h HitSet) Equal(other HitSet) bool {
	if h.Count() != other.Count() {
		return false
	}
	for id, hit := range h {
		if hit != other[id] {
			return false
		}
	}
	for id, hit := range other {
		if hit != h[id] {
			return false
		}
	}
	return true
}

// Filter 返回指定编号集合对应的命中快照。
func (h HitSet) Filter(ids []uint32) HitSet {
	out := make(HitSet, len(ids))
	for _, id := range ids {
		if h[id] {
			out[id] = true
		}
	}
	return out
}

func Evaluate(root parser.Node, hits HitSet) bool {
	switch v := root.(type) {
	case parser.Combination:
		return Evaluate(v.Expr, hits)
	case parser.CombinationOperand:
		return hits[v.ID]
	case parser.CombinationOperator:
		if v.Op == '&' {
			return Evaluate(v.Left, hits) && Evaluate(v.Right, hits)
		}
		if v.Op == '|' {
			return Evaluate(v.Left, hits) || Evaluate(v.Right, hits)
		}
	case parser.CombinationNot:
		return !Evaluate(v.Child, hits)
	}
	return false
}

// EvaluateTrace 在求值同时返回实际访问到的基础规则编号。
func EvaluateTrace(root parser.Node, hits HitSet) (bool, []uint32) {
	seen := make(map[uint32]struct{})
	var walk func(parser.Node) bool
	walk = func(node parser.Node) bool {
		switch v := node.(type) {
		case parser.Combination:
			return walk(v.Expr)
		case parser.CombinationOperand:
			seen[v.ID] = struct{}{}
			return hits[v.ID]
		case parser.CombinationOperator:
			if v.Op == '&' {
				left := walk(v.Left)
				if !left {
					return false
				}
				return walk(v.Right)
			}
			if v.Op == '|' {
				left := walk(v.Left)
				if left {
					return true
				}
				return walk(v.Right)
			}
		case parser.CombinationNot:
			return !walk(v.Child)
		}
		return false
	}
	result := walk(root)
	ids := make([]uint32, 0, len(seen))
	for id := range seen {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return result, ids
}

func Validate(root parser.Node) error { return parser.Validate(root) }

func IDs(root parser.Node) []uint32 {
	seen := map[uint32]bool{}
	out := []uint32{}
	var walk func(parser.Node)
	walk = func(n parser.Node) {
		switch v := n.(type) {
		case parser.Combination:
			walk(v.Expr)
		case parser.CombinationOperand:
			if !seen[v.ID] {
				seen[v.ID] = true
				out = append(out, v.ID)
			}
		case parser.CombinationOperator:
			walk(v.Left)
			walk(v.Right)
		case parser.CombinationNot:
			walk(v.Child)
		}
	}
	walk(root)
	return out
}

// Dependencies 返回组合规则直接引用的规则编号，按首次出现的顺序去重。
func Dependencies(root parser.Node) []uint32 { return IDs(root) }

// DependencyCycle 在组合规则依赖图中查找循环；返回的编号序列首尾相同。
func DependencyCycle(dependencies map[uint32][]uint32) []uint32 {
	ids := make([]uint32, 0, len(dependencies))
	for id := range dependencies {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	const (
		unseen uint8 = iota
		visiting
		done
	)
	state := make(map[uint32]uint8, len(dependencies))
	path := make([]uint32, 0, len(dependencies))
	var visit func(uint32) []uint32
	visit = func(id uint32) []uint32 {
		state[id] = visiting
		path = append(path, id)
		for _, child := range dependencies[id] {
			if _, isCombination := dependencies[child]; !isCombination {
				continue
			}
			switch state[child] {
			case visiting:
				for index, value := range path {
					if value == child {
						cycle := append([]uint32(nil), path[index:]...)
						return append(cycle, child)
					}
				}
			case unseen:
				if cycle := visit(child); len(cycle) > 0 {
					return cycle
				}
			}
		}
		path = path[:len(path)-1]
		state[id] = done
		return nil
	}
	for _, id := range ids {
		if state[id] == unseen {
			if cycle := visit(id); len(cycle) > 0 {
				return cycle
			}
		}
	}
	return nil
}

// ValidateDependencies 验证组合规则依赖图不存在循环引用。
func ValidateDependencies(dependencies map[uint32][]uint32) error {
	if cycle := DependencyCycle(dependencies); len(cycle) > 0 {
		return fmt.Errorf("combination dependency cycle: %v", cycle)
	}
	return nil
}

func Missing(root parser.Node, hits HitSet) []uint32 {
	missing := []uint32{}
	for _, id := range IDs(root) {
		if !hits[id] {
			missing = append(missing, id)
		}
	}
	return missing
}

// MissingSorted 返回按编号排序的未命中规则编号。
func MissingSorted(root parser.Node, hits HitSet) []uint32 {
	out := Missing(root, hits)
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// SatisfiedIDs 返回表达式中当前已满足的基础规则编号。
func SatisfiedIDs(root parser.Node, hits HitSet) []uint32 {
	out := make([]uint32, 0)
	for _, id := range IDs(root) {
		if hits[id] {
			out = append(out, id)
		}
	}
	return out
}

// IsContradiction 判断表达式是否必然为假（仅识别字面同项冲突）。
func IsContradiction(root parser.Node) bool {
	root = Normalize(root)
	if v, ok := root.(parser.Combination); ok {
		return IsContradiction(v.Expr)
	}
	if v, ok := root.(parser.CombinationOperator); ok && v.Op == '&' {
		return (isOperandNegPair(v.Left, v.Right) || isOperandNegPair(v.Right, v.Left)) || IsContradiction(v.Left) || IsContradiction(v.Right)
	}
	return false
}

func isOperandNegPair(a, b parser.Node) bool {
	left, ok := a.(parser.CombinationOperand)
	if !ok {
		return false
	}
	right, ok := b.(parser.CombinationNot)
	if !ok {
		return false
	}
	inner, ok := right.Child.(parser.CombinationOperand)
	return ok && left.ID == inner.ID
}

// IsTautology 判断由直接项与其否定构成的析取恒真式。
func IsTautology(root parser.Node) bool {
	root = Normalize(root)
	if v, ok := root.(parser.Combination); ok {
		return IsTautology(v.Expr)
	}
	if v, ok := root.(parser.CombinationOperator); ok && v.Op == '|' {
		return isOperandNegPair(v.Left, v.Right) || isOperandNegPair(v.Right, v.Left) || IsTautology(v.Left) || IsTautology(v.Right)
	}
	return false
}

// Normalize 消除恒定的双重否定组合节点。
func Normalize(root parser.Node) parser.Node {
	switch v := root.(type) {
	case parser.Combination:
		v.Expr = Normalize(v.Expr)
		return v
	case parser.CombinationNot:
		child := Normalize(v.Child)
		if nested, ok := child.(parser.CombinationNot); ok {
			return Normalize(nested.Child)
		}
		v.Child = child
		return v
	case parser.CombinationOperator:
		v.Left, v.Right = Normalize(v.Left), Normalize(v.Right)
		return v
	default:
		return root
	}
}
