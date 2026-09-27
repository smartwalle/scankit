package nfa

import (
	"fmt"

	"github.com/smartwalle/scankit/internal/nfagraph"
)

// ResourceLimits 描述单次匹配允许使用的资源上限。
// 零值表示不对该项附加限制；编译阶段仍执行布局自身的硬上限。
type ResourceLimits struct {
	States  int
	Edges   int
	Memory  uint64
	Steps   int
	Results int
}

// ResourceUsage 是一次引擎执行或编译布局的资源快照。
type ResourceUsage struct {
	States  int
	Edges   int
	Memory  uint64
	Steps   int
	Results int
}

// DefaultLimits 返回引擎族的默认编译资源门槛。运行步骤和结果不设
// 固定值，由调用方按单次扫描场景传入 Context 预算。
func DefaultLimits(kind EngineKind) ResourceLimits {
	limits := ResourceLimits{States: sparseStateLimit, Edges: 16384, Memory: maxLayoutMemory}
	switch kind {
	case EngineLimEx:
		limits.States = limExStateLimit
	case EngineSheng:
		limits.States = shengStateLimit
	case EngineMcSheng, EngineVermicelli, EngineTamarama, EngineShufti, EngineTruffle:
		limits.States = sparseStateLimit
	case EngineCastle, EngineGough, EngineLBR:
		limits.States = 4096
	default:
	}
	return limits
}

// Validate 检查资源限制和用量是否为合法非负值。
func (l ResourceLimits) Validate() error {
	if l.States < 0 || l.Edges < 0 || l.Steps < 0 || l.Results < 0 {
		return fmt.Errorf("invalid nfa resource limits")
	}
	return nil
}

// Within 判断用量是否完全落在限制内。
func (u ResourceUsage) Within(l ResourceLimits) bool {
	return l.Validate() == nil && (l.States == 0 || u.States <= l.States) && (l.Edges == 0 || u.Edges <= l.Edges) && (l.Memory == 0 || u.Memory <= l.Memory) && (l.Steps == 0 || u.Steps <= l.Steps) && (l.Results == 0 || u.Results <= l.Results)
}

// Usage 返回编译布局的状态、边和内存用量。
func (e *Engine) Usage() ResourceUsage {
	if e == nil {
		return ResourceUsage{}
	}
	return ResourceUsage{States: e.StateCount(), Edges: e.EdgeCount(), Memory: e.MemoryBytes()}
}

// CheckLimits 在执行前统一检查编译布局资源；步骤和结果由 MatchAtWithContext
// 在运行时逐步计数，超限时返回安全的空结果或已确认结果。
func (e *Engine) CheckLimits(l ResourceLimits) error {
	if e == nil || e.Validate() != nil {
		return ErrInvalidGraph
	}
	if err := l.Validate(); err != nil {
		return err
	}
	u := e.Usage()
	if l.States > 0 && u.States > l.States {
		return ErrStateLimit
	}
	if l.Edges > 0 && u.Edges > l.Edges {
		return ErrEdgeLimit
	}
	if l.Memory > 0 && u.Memory > l.Memory {
		return ErrMemoryLimit
	}
	return nil
}

// CheckDefaultLimits 校验引擎是否满足其布局族的默认门槛。
func (e *Engine) CheckDefaultLimits() error {
	if e == nil {
		return ErrInvalidGraph
	}
	return e.CheckLimits(DefaultLimits(e.Kind))
}

// CompileEngineWithLimits 在一次调用中应用完整的状态、边和内存门槛，
// 避免调用方先编译大布局再发现资源不满足。
func CompileEngineWithLimits(g *nfagraph.Graph, kind EngineKind, limits ResourceLimits) (*Engine, error) {
	if err := limits.Validate(); err != nil {
		return nil, err
	}
	if g == nil || g.Validate() != nil {
		return nil, ErrInvalidGraph
	}
	expanded := nfagraph.ExpandLiterals(g)
	if expanded == nil || expanded.Flow == nil {
		return nil, ErrInvalidGraph
	}
	if limits.States > 0 && len(expanded.Nodes) > limits.States {
		return nil, ErrStateLimit
	}
	if limits.Edges > 0 && expanded.Flow.EdgeCount() > limits.Edges {
		return nil, ErrEdgeLimit
	}
	e, err := CompileEngineWithBudgets(g, kind, limits.States, limits.Memory)
	if err != nil {
		return nil, err
	}
	if err := e.CheckLimits(limits); err != nil {
		return nil, err
	}
	return e, nil
}

// CheckExecutionLimits 校验一次执行上下文的步骤和结果用量。
func CheckExecutionLimits(ctx Context, limits ResourceLimits) error {
	if err := limits.Validate(); err != nil {
		return err
	}
	if err := ctx.Validate(); err != nil {
		return err
	}
	if ctx.Steps < 0 || ctx.Results < 0 {
		return fmt.Errorf("invalid nfa resource usage")
	}
	usage := ResourceUsage{Steps: ctx.Steps, Results: ctx.Results}
	if limits.Steps > 0 && usage.Steps > limits.Steps {
		return ErrExecutionLimit
	}
	if limits.Results > 0 && usage.Results > limits.Results {
		return ErrExecutionLimit
	}
	return nil
}
