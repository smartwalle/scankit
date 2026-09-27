// Package compiler 提供编译阶段共享的上下文、错误和资源计数模型。
package compiler

import (
	"context"
	"fmt"
)

// CompileLimits 描述编译资源预算。零值表示不额外施加项目级限制，
// 具体 bailout/错误语义由上层按参考源码实现。
type CompileLimits struct {
	GraphVertices uint64
	GraphEdges    uint64
	DFAStates     uint64
	RoseRoles     uint64
	ProgramBytes  uint64
	MemoryBytes   uint64
}

// Dimension 返回指定预算维度上限，零表示不限制或未知维度。
func (l CompileLimits) Dimension(name string) uint64 {
	switch name {
	case "graph_vertices":
		return l.GraphVertices
	case "graph_edges":
		return l.GraphEdges
	case "dfa_states":
		return l.DFAStates
	case "rose_roles":
		return l.RoseRoles
	case "program_bytes":
		return l.ProgramBytes
	case "memory_bytes":
		return l.MemoryBytes
	default:
	}
	return 0
}

// WithDefaults 用默认值填充未设置的预算维度。
func (l CompileLimits) WithDefaults(defaults CompileLimits) CompileLimits {
	if l.GraphVertices == 0 {
		l.GraphVertices = defaults.GraphVertices
	}
	if l.GraphEdges == 0 {
		l.GraphEdges = defaults.GraphEdges
	}
	if l.DFAStates == 0 {
		l.DFAStates = defaults.DFAStates
	}
	if l.RoseRoles == 0 {
		l.RoseRoles = defaults.RoseRoles
	}
	if l.ProgramBytes == 0 {
		l.ProgramBytes = defaults.ProgramBytes
	}
	if l.MemoryBytes == 0 {
		l.MemoryBytes = defaults.MemoryBytes
	}
	return l
}

// Remaining 返回扣除当前用量后的剩余预算；零值预算仍表示不限制。
func (l CompileLimits) Remaining(u Usage) CompileLimits {
	sub := func(limit, used uint64) uint64 {
		if limit == 0 || used >= limit {
			return 0
		}
		return limit - used
	}
	return CompileLimits{sub(l.GraphVertices, u.GraphVertices), sub(l.GraphEdges, u.GraphEdges), sub(l.DFAStates, u.DFAStates), sub(l.RoseRoles, u.RoseRoles), sub(l.ProgramBytes, u.ProgramBytes), sub(l.MemoryBytes, u.MemoryBytes)}
}

// RemainingDimension 返回指定预算维度剩余量及该维度是否受限。
func (l CompileLimits) RemainingDimension(name string, u Usage) (uint64, bool) {
	limit := l.Dimension(name)
	if limit == 0 {
		return 0, false
	}
	used := u.Dimension(name)
	if used >= limit {
		return 0, true
	}
	return limit - used, true
}

// Unlimited 判断是否未设置任何预算。
func (l CompileLimits) Unlimited() bool { return l == (CompileLimits{}) }

// Constrained 判断是否至少设置了一项有限预算。
func (l CompileLimits) Constrained() bool { return !l.Unlimited() }

// Tightest 返回当前限制中最小的非零数值及其维度名称。
func (l CompileLimits) Tightest() (string, uint64) {
	items := [...]struct {
		name  string
		value uint64
	}{{"graph_vertices", l.GraphVertices}, {"graph_edges", l.GraphEdges}, {"dfa_states", l.DFAStates}, {"rose_roles", l.RoseRoles}, {"program_bytes", l.ProgramBytes}, {"memory_bytes", l.MemoryBytes}}
	name, value := "", uint64(0)
	for _, item := range items {
		if item.value != 0 && (value == 0 || item.value < value) {
			name, value = item.name, item.value
		}
	}
	return name, value
}

// Merge 逐项取两者中更紧的非零预算，零值表示该项不限制。
func (l CompileLimits) Merge(other CompileLimits) CompileLimits {
	pick := func(a, b uint64) uint64 {
		if a == 0 {
			return b
		}
		if b == 0 {
			return a
		}
		if a < b {
			return a
		}
		return b
	}
	return CompileLimits{pick(l.GraphVertices, other.GraphVertices), pick(l.GraphEdges, other.GraphEdges), pick(l.DFAStates, other.DFAStates), pick(l.RoseRoles, other.RoseRoles), pick(l.ProgramBytes, other.ProgramBytes), pick(l.MemoryBytes, other.MemoryBytes)}
}

// Tighten 返回两个预算中更严格的逐维限制。
func (l CompileLimits) Tighten(other CompileLimits) CompileLimits { return l.Merge(other) }

// Usage 是当前编译阶段的资源使用量。
type Usage struct {
	GraphVertices uint64
	GraphEdges    uint64
	DFAStates     uint64
	RoseRoles     uint64
	ProgramBytes  uint64
	MemoryBytes   uint64
}

// Zero 判断资源用量是否全部为零。
func (u Usage) Zero() bool { return u == (Usage{}) }

// Exceeds 判断资源用量是否超过任一非零预算。
func (u Usage) Exceeds(l CompileLimits) bool {
	return l.GraphVertices > 0 && u.GraphVertices > l.GraphVertices ||
		l.GraphEdges > 0 && u.GraphEdges > l.GraphEdges ||
		l.DFAStates > 0 && u.DFAStates > l.DFAStates ||
		l.RoseRoles > 0 && u.RoseRoles > l.RoseRoles ||
		l.ProgramBytes > 0 && u.ProgramBytes > l.ProgramBytes ||
		l.MemoryBytes > 0 && u.MemoryBytes > l.MemoryBytes
}

// Add 将 v 累加到当前用量，逐项饱和以避免溢出。
func (u *Usage) Add(v Usage) {
	if u == nil {
		return
	}
	add := func(dst *uint64, value uint64) {
		if ^uint64(0)-*dst < value {
			*dst = ^uint64(0)
			return
		}
		*dst += value
	}
	add(&u.GraphVertices, v.GraphVertices)
	add(&u.GraphEdges, v.GraphEdges)
	add(&u.DFAStates, v.DFAStates)
	add(&u.RoseRoles, v.RoseRoles)
	add(&u.ProgramBytes, v.ProgramBytes)
	add(&u.MemoryBytes, v.MemoryBytes)
}

// Sub 从当前用量中扣除指定资源，结果不会下溢。
func (u *Usage) Sub(v Usage) {
	if u == nil {
		return
	}
	sub := func(dst *uint64, value uint64) {
		if value >= *dst {
			*dst = 0
			return
		}
		*dst -= value
	}
	sub(&u.GraphVertices, v.GraphVertices)
	sub(&u.GraphEdges, v.GraphEdges)
	sub(&u.DFAStates, v.DFAStates)
	sub(&u.RoseRoles, v.RoseRoles)
	sub(&u.ProgramBytes, v.ProgramBytes)
	sub(&u.MemoryBytes, v.MemoryBytes)
}

// Merge 返回两份资源用量的饱和和。
func (u Usage) Merge(other Usage) Usage { u.Add(other); return u }

// Total 返回用量各分项之和，溢出时返回最大值。
func (u Usage) Total() uint64 {
	var out Usage
	out.Add(u)
	var total uint64
	for _, n := range [...]uint64{out.GraphVertices, out.GraphEdges, out.DFAStates, out.RoseRoles, out.ProgramBytes, out.MemoryBytes} {
		if ^uint64(0)-total < n {
			return ^uint64(0)
		}
		total += n
	}
	return total
}

// Equal 判断两份资源用量是否一致。
func (u Usage) Equal(other Usage) bool { return u == other }

// Difference 返回逐维绝对差值，避免无符号下溢。
func (u Usage) Difference(other Usage) Usage {
	d := func(a, b uint64) uint64 {
		if a >= b {
			return a - b
		}
		return b - a
	}
	return Usage{d(u.GraphVertices, other.GraphVertices), d(u.GraphEdges, other.GraphEdges), d(u.DFAStates, other.DFAStates), d(u.RoseRoles, other.RoseRoles), d(u.ProgramBytes, other.ProgramBytes), d(u.MemoryBytes, other.MemoryBytes)}
}

// Max 返回各维度逐项较大值。
func (u Usage) Max(other Usage) Usage {
	m := func(a, b uint64) uint64 {
		if a > b {
			return a
		}
		return b
	}
	return Usage{m(u.GraphVertices, other.GraphVertices), m(u.GraphEdges, other.GraphEdges), m(u.DFAStates, other.DFAStates), m(u.RoseRoles, other.RoseRoles), m(u.ProgramBytes, other.ProgramBytes), m(u.MemoryBytes, other.MemoryBytes)}
}

// Dimension 返回指定资源维度的用量。
func (u Usage) Dimension(name string) uint64 {
	switch name {
	case "graph_vertices":
		return u.GraphVertices
	case "graph_edges":
		return u.GraphEdges
	case "dfa_states":
		return u.DFAStates
	case "rose_roles":
		return u.RoseRoles
	case "program_bytes":
		return u.ProgramBytes
	case "memory_bytes":
		return u.MemoryBytes
	default:
		return 0
	}
}

// HasDimension 判断资源维度名称是否受支持。
func (u Usage) HasDimension(name string) bool {
	switch name {
	case "graph_vertices", "graph_edges", "dfa_states", "rose_roles", "program_bytes", "memory_bytes":
		return true
	default:
	}
	return false
}

// Check 返回首个超限错误；零限制不检查该维度。
func (l CompileLimits) Check(u Usage) error {
	checks := [...]struct {
		name string
		used uint64
		max  uint64
	}{
		{"graph vertices", u.GraphVertices, l.GraphVertices},
		{"graph edges", u.GraphEdges, l.GraphEdges},
		{"dfa states", u.DFAStates, l.DFAStates},
		{"rose roles", u.RoseRoles, l.RoseRoles},
		{"program bytes", u.ProgramBytes, l.ProgramBytes},
		{"memory bytes", u.MemoryBytes, l.MemoryBytes},
	}
	for _, check := range checks {
		if check.max != 0 && check.used > check.max {
			return fmt.Errorf("compile resource limit exceeded: %s", check.name)
		}
	}
	return nil
}

// FirstExceeded 返回首个超限资源维度名称。
func (l CompileLimits) FirstExceeded(u Usage) string {
	checks := [...]struct {
		name      string
		used, max uint64
	}{{"graph_vertices", u.GraphVertices, l.GraphVertices}, {"graph_edges", u.GraphEdges, l.GraphEdges}, {"dfa_states", u.DFAStates, l.DFAStates}, {"rose_roles", u.RoseRoles, l.RoseRoles}, {"program_bytes", u.ProgramBytes, l.ProgramBytes}, {"memory_bytes", u.MemoryBytes, l.MemoryBytes}}
	for _, c := range checks {
		if c.max != 0 && c.used > c.max {
			return c.name
		}
	}
	return ""
}

// ErrorKind 是可稳定断言的编译错误分类。
type ErrorKind uint8

// ErrorSyntax 表示语法错误，其余常量对应其他可稳定断言的编译错误分类。
const (
	ErrorSyntax ErrorKind = iota + 1
	ErrorUnsupported
	ErrorInvalidReference
	ErrorResourceLimit
	ErrorInvalidExpression
	ErrorCancelled
)

// CompileError 不包含完整 pattern，避免错误日志泄露输入。
type CompileError struct {
	Kind       ErrorKind
	Expression int
	Position   int
	Message    string
}

func (e *CompileError) Error() string {
	if e == nil {
		return ""
	}
	if e.Expression >= 0 {
		return fmt.Sprintf("compile error (expression %d, position %d): %s", e.Expression, e.Position, e.Message)
	}
	return fmt.Sprintf("compile error (position %d): %s", e.Position, e.Message)
}

// PlatformCapabilities 描述编译目标可用的 SIMD 能力。
type PlatformCapabilities struct {
	X86SSE      bool
	X86SSE4     bool
	X86AVX2     bool
	X86AVX512   bool
	X86AVX512VB bool
	ARMNEON     bool
	ARMSVE      bool
	ARMSVE2     bool
	ARMSVE2Bit  bool
}

// AnySIMD 判断平台是否声明了任意一种向量能力。
func (p PlatformCapabilities) AnySIMD() bool {
	return p.X86SSE || p.X86SSE4 || p.X86AVX2 || p.X86AVX512 || p.X86AVX512VB || p.ARMNEON || p.ARMSVE || p.ARMSVE2 || p.ARMSVE2Bit
}

// Merge 合并两组平台能力，任一组支持即视为支持。
func (p PlatformCapabilities) Merge(other PlatformCapabilities) PlatformCapabilities {
	return PlatformCapabilities{p.X86SSE || other.X86SSE, p.X86SSE4 || other.X86SSE4, p.X86AVX2 || other.X86AVX2, p.X86AVX512 || other.X86AVX512, p.X86AVX512VB || other.X86AVX512VB, p.ARMNEON || other.ARMNEON, p.ARMSVE || other.ARMSVE, p.ARMSVE2 || other.ARMSVE2, p.ARMSVE2Bit || other.ARMSVE2Bit}
}

// CompileContext 聚合 limits、平台能力和当前资源用量。
type CompileContext struct {
	Limits       CompileLimits
	Capabilities PlatformCapabilities
	Usage        Usage
	Context      context.Context
}

// Check 返回取消、资源限额检查结果，供每个编译阶段统一调用。
func (c CompileContext) Check() error {
	if err := c.CheckCancelled(); err != nil {
		return &CompileError{Kind: ErrorCancelled, Expression: -1, Position: -1, Message: err.Error()}
	}
	if err := c.CheckLimits(); err != nil {
		return &CompileError{Kind: ErrorResourceLimit, Expression: -1, Position: -1, Message: err.Error()}
	}
	return nil
}

// CheckStage 在阶段边界执行统一取消和资源校验。
func (c CompileContext) CheckStage(stage string, usage Usage) error {
	if err := c.CheckCancelled(); err != nil {
		return &CompileError{Kind: ErrorCancelled, Expression: -1, Position: -1, Message: stage + ": " + err.Error()}
	}
	if err := c.Limits.Check(usage); err != nil {
		return &CompileError{Kind: ErrorResourceLimit, Expression: -1, Position: -1, Message: stage + ": " + err.Error()}
	}
	return nil
}

// ReserveChecked 预留资源并立即执行取消与限额校验。
func (c *CompileContext) ReserveChecked(u Usage) error {
	if c == nil {
		return &CompileError{Kind: ErrorInvalidExpression, Expression: -1, Position: -1, Message: "nil compile context"}
	}
	if err := c.CheckCancelled(); err != nil {
		return &CompileError{Kind: ErrorCancelled, Expression: -1, Position: -1, Message: err.Error()}
	}
	if !c.Reserve(u) {
		return &CompileError{Kind: ErrorResourceLimit, Expression: -1, Position: -1, Message: "compile resource limit exceeded"}
	}
	return nil
}

// Clone 返回编译上下文的副本，用量快照随副本独立演进。
func (c CompileContext) Clone() CompileContext { return c }

// WithContext 返回绑定取消上下文的独立编译上下文。
func (c CompileContext) WithContext(ctx context.Context) CompileContext {
	c.Context = ctx
	return c
}

// Remaining 返回当前资源预算扣除已用量后的快照。
func (c CompileContext) Remaining() CompileLimits { return c.Limits.Remaining(c.Usage) }

// ResetUsage 将资源用量清零，便于复用时重新统计。
func (c *CompileContext) ResetUsage() {
	if c != nil {
		c.Usage = Usage{}
	}
}

// AddUsage 累加资源并返回是否已超出预算。
func (c *CompileContext) AddUsage(u Usage) bool {
	if c == nil {
		return false
	}
	c.Usage.Add(u)
	return c.Usage.Exceeds(c.Limits)
}

// Reserve 预留资源并在超限时回滚，适合分阶段编译。
func (c *CompileContext) Reserve(u Usage) bool {
	if c == nil {
		return false
	}
	before := c.Usage
	c.Usage.Add(u)
	if c.Usage.Exceeds(c.Limits) {
		c.Usage = before
		return false
	}
	return true
}

// Release 释放已预留资源。
func (c *CompileContext) Release(u Usage) {
	if c != nil {
		c.Usage.Sub(u)
	}
}

// CheckLimits 检查当前资源用量。
func (c *CompileContext) CheckLimits() error { return c.Limits.Check(c.Usage) }

// CheckCancelled 返回编译上下文是否已被取消。
func (c CompileContext) CheckCancelled() error {
	if c.Context == nil {
		return nil
	}
	return c.Context.Err()
}
