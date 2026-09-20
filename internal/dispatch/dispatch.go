// Package dispatch 提供运行时 CPU 能力探测与安全降级。
package dispatch

import (
	"fmt"
	"runtime"
	"strings"
	"sync"

	"github.com/smartwalle/scankit/internal/simd"
	armbackend "github.com/smartwalle/scankit/internal/simd/arm64"
	x86backend "github.com/smartwalle/scankit/internal/simd/x86"
	"golang.org/x/sys/cpu"
)

// Features 是可用于选择 SIMD 后端的能力集合。
type Features struct {
	Arch       string
	SSE        bool
	SSE4       bool
	AVX2       bool
	AVX512     bool
	AVX512VBMI bool
	NEON       bool
	SVE        bool
	SVE2       bool
	SVE2Bit    bool
}

// CPUFeatureMask 与编译目标中的硬件能力位保持稳定映射。
type CPUFeatureMask uint64

const (
	CPUFeatureAVX2       CPUFeatureMask = 1 << 2
	CPUFeatureAVX512     CPUFeatureMask = 1 << 3
	CPUFeatureAVX512VBMI CPUFeatureMask = 1 << 4
)

// TuneFamily 表示编译时的微架构调优族。
type TuneFamily uint32

const (
	TuneGeneric TuneFamily = iota
	TuneSNB
	TuneIVB
	TuneHSW
	TuneSLM
	TuneBDW
	TuneSKL
	TuneSKX
	TuneGLM
	TuneICL
	TuneICX
)

const maxTuneFamily = TuneICX

// Platform 是编译目标的平台快照。它只影响后端选择，不改变匹配语义。
type Platform struct {
	Tune        TuneFamily
	CPUFeatures CPUFeatureMask
}

// Validate 检查平台快照中的保留位和调优编号。
func (p Platform) Validate() error {
	const all = CPUFeatureAVX2 | CPUFeatureAVX512 | CPUFeatureAVX512VBMI
	if p.CPUFeatures&^all != 0 {
		return fmt.Errorf("invalid cpu features: 0x%x", uint64(p.CPUFeatures&^all))
	}
	if p.Tune > maxTuneFamily {
		return fmt.Errorf("invalid tuning family: %d", p.Tune)
	}
	return nil
}

// Features 返回平台快照对应的能力集合，并补齐隐含的低级能力。
func (p Platform) Features() Features {
	if p.Validate() != nil {
		return Features{}
	}
	return (Features{Arch: "amd64", AVX2: p.CPUFeatures&CPUFeatureAVX2 != 0,
		AVX512:     p.CPUFeatures&CPUFeatureAVX512 != 0,
		AVX512VBMI: p.CPUFeatures&CPUFeatureAVX512VBMI != 0}).Normalize()
}

// PlatformFromFeatures 将能力集合编码为平台硬件位；非 x86 能力不写入 x86 位。
func PlatformFromFeatures(f Features) Platform {
	f = f.Normalize()
	if f.Arch != "amd64" {
		return Platform{}
	}
	var mask CPUFeatureMask
	if f.AVX2 {
		mask |= CPUFeatureAVX2
	}
	if f.AVX512 {
		mask |= CPUFeatureAVX512
	}
	if f.AVX512VBMI {
		mask |= CPUFeatureAVX512VBMI
	}
	return Platform{CPUFeatures: mask}
}

// Valid 判断能力集合的架构字段和特性是否匹配。
func (f Features) Valid() bool {
	a := NormalizeArch(f.Arch)
	if a == "" {
		return true
	}
	if a != "amd64" && a != "arm64" {
		return !f.SSE && !f.SSE4 && !f.AVX2 && !f.AVX512 && !f.AVX512VBMI && !f.NEON && !f.SVE && !f.SVE2 && !f.SVE2Bit
	}
	if a == "amd64" {
		return !f.NEON && !f.SVE && !f.SVE2 && !f.SVE2Bit
	}
	return !f.SSE && !f.SSE4 && !f.AVX2 && !f.AVX512 && !f.AVX512VBMI
}

// Detect 返回当前架构的保守能力集合。具体高级特性可由调用方注入覆盖。
func Detect() Features {
	switch runtime.GOARCH {
	case "amd64":
		return Features{Arch: "amd64", SSE: cpu.X86.HasSSE2, SSE4: cpu.X86.HasSSE41 || cpu.X86.HasSSE42, AVX2: cpu.X86.HasAVX2, AVX512: cpu.X86.HasAVX512, AVX512VBMI: cpu.X86.HasAVX512VBMI}
	case "arm64":
		return Features{Arch: "arm64", NEON: cpu.ARM64.HasASIMD, SVE: cpu.ARM64.HasSVE, SVE2: cpu.ARM64.HasSVE2}
	default:
		return Features{Arch: runtime.GOARCH}
	}
}

// Backend 表示选中的实现后端。
type Backend uint8

const (
	BackendGeneric Backend = iota + 1
	BackendX86
	BackendARM64
)

// Valid 判断后端枚举是否有效。
func (b Backend) Valid() bool { return b >= BackendGeneric && b <= BackendARM64 }

// Registry 保存按优先级排列的后端能力契约，并在运行时选择第一个可用后端。
// 注册表只描述能力，不直接执行架构指令，因此在未知平台上始终可安全回退。
type Registry struct {
	mu       sync.RWMutex
	entries  []registryEntry
	disabled map[Backend]bool
}

type registryEntry struct {
	backend Backend
	target  Features
}

// BackendSpec 是注册表项的只读快照，便于启动诊断和矩阵审计。
type BackendSpec struct {
	Backend Backend
	Target  Features
}

// NewRegistry 创建空注册表。
func NewRegistry() *Registry { return &Registry{disabled: make(map[Backend]bool)} }

// SetDisabled 设置后端是否被显式禁用。禁用只影响后续选择，不修改已返回的后端。
func (r *Registry) SetDisabled(backend Backend, disabled bool) {
	if r == nil || !backend.Valid() {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.disabled == nil {
		r.disabled = make(map[Backend]bool)
	}
	if disabled {
		r.disabled[backend] = true
	} else {
		delete(r.disabled, backend)
	}
}

// Disabled 返回后端当前是否被显式禁用。
func (r *Registry) Disabled(backend Backend) bool {
	if r == nil {
		return false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.disabled[backend]
}

// Register 添加一个后端契约；无效能力或后端会被忽略。
func (r *Registry) Register(backend Backend, target Features) {
	if r == nil || !backend.Valid() || !target.Valid() {
		return
	}
	target = target.Normalize()
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := range r.entries {
		if r.entries[i].backend == backend && r.entries[i].target == target {
			return
		}
	}
	r.entries = append(r.entries, registryEntry{backend: backend, target: target})
}

// Entries 返回按优先级排列的注册表快照，调用方修改结果不会影响注册表。
func (r *Registry) Entries() []BackendSpec {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]BackendSpec, len(r.entries))
	for i, entry := range r.entries {
		out[i] = BackendSpec{Backend: entry.backend, Target: entry.target}
	}
	return out
}

// Select 在注册表中选择当前能力可支持的最高优先级后端。
func (r *Registry) Select(current Features) Backend {
	if r == nil {
		return BackendGeneric
	}
	current = current.Normalize()
	if !current.Valid() {
		return BackendGeneric
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, entry := range r.entries {
		if r.disabled[entry.backend] {
			continue
		}
		if entry.backend == BackendGeneric || current.Supports(entry.target) {
			return entry.backend
		}
	}
	return BackendGeneric
}

// Validate 检查注册表顺序和能力契约，防止低级后端遮蔽高级后端。
func (r *Registry) Validate() error {
	if r == nil {
		return fmt.Errorf("nil backend registry")
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	for i, entry := range r.entries {
		if !entry.backend.Valid() || !entry.target.Valid() {
			return fmt.Errorf("invalid backend registry entry %d", i)
		}
		if entry.backend == BackendGeneric {
			continue
		}
		for j := i + 1; j < len(r.entries); j++ {
			if r.entries[j].backend == entry.backend && r.entries[j].target.Arch == entry.target.Arch && featureScore(r.entries[j].target) > featureScore(entry.target) {
				return fmt.Errorf("backend registry order is not descending")
			}
		}
	}
	return nil
}

func featureScore(f Features) int {
	n := 0
	for _, on := range []bool{f.SSE, f.SSE4, f.AVX2, f.AVX512, f.AVX512VBMI, f.NEON, f.SVE, f.SVE2, f.SVE2Bit} {
		if on {
			n++
		}
	}
	return n
}

// DefaultRegistry 返回包含所有已知能力层级的稳定注册表。
func DefaultRegistry() *Registry {
	r := NewRegistry()
	// 高级能力优先，基础能力作为逐级回退。
	r.Register(BackendX86, Features{Arch: "amd64", AVX512VBMI: true})
	r.Register(BackendX86, Features{Arch: "amd64", AVX512: true})
	r.Register(BackendX86, Features{Arch: "amd64", AVX2: true})
	r.Register(BackendX86, Features{Arch: "amd64", SSE4: true})
	r.Register(BackendX86, Features{Arch: "amd64", SSE: true})
	r.Register(BackendARM64, Features{Arch: "arm64", SVE2Bit: true})
	r.Register(BackendARM64, Features{Arch: "arm64", SVE2: true})
	r.Register(BackendARM64, Features{Arch: "arm64", SVE: true})
	r.Register(BackendARM64, Features{Arch: "arm64", NEON: true})
	r.Register(BackendGeneric, Features{})
	return r
}

var defaultBackendRegistry = DefaultRegistry()
var detectedBackend = struct {
	sync.Once
	value simd.Backend
}{}

// DefaultBackend 返回当前主机的稳定后端实例。能力探测只执行一次，避免每次扫描重复读取 CPU 状态。
func DefaultBackend() simd.Backend {
	detectedBackend.Do(func() { detectedBackend.value = SelectBackend(Detect()) })
	return detectedBackend.value
}

// String 返回后端名称。
func (b Backend) String() string { return BackendName(b) }

// Select 返回可安全使用的后端；未知或未启用特性时使用通用实现。
func Select(features Features) Backend {
	features = features.Normalize()
	return defaultBackendRegistry.Select(features)
}

// SelectBackend 返回满足可移植 SIMD 契约的实现；架构后端在不具备原生实现时仍保持同一语义。
func SelectBackend(features Features) simd.Backend {
	features = features.Normalize()
	switch Select(features) {
	case BackendX86:
		tier := x86backend.TierSSE
		switch {
		case features.AVX512VBMI:
			tier = x86backend.TierAVX512VBMI
		case features.AVX512:
			tier = x86backend.TierAVX512
		case features.AVX2:
			tier = x86backend.TierAVX2
		case features.SSE4:
			tier = x86backend.TierSSE4
		}
		return x86backend.NewWithTier(tier)
	case BackendARM64:
		tier := armbackend.TierNEON
		if features.SVE2 {
			tier = armbackend.TierSVE2
		} else if features.SVE {
			tier = armbackend.TierSVE
		}
		return armbackend.NewWithTier(tier)
	default:
		return simd.Default()
	}
}
func BackendName(b Backend) string {
	switch b {
	case BackendX86:
		return "x86"
	case BackendARM64:
		return "arm64"
	default:
		return "generic"
	}
}

// ParseBackend 按名称解析后端枚举。
func ParseBackend(name string) (Backend, bool) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "generic":
		return BackendGeneric, true
	case "x86", "amd64":
		return BackendX86, true
	case "arm64", "aarch64":
		return BackendARM64, true
	default:
		return 0, false
	}
}

// BackendAvailable 判断能力集合是否支持指定后端。
func BackendAvailable(features Features, backend Backend) bool {
	return Select(features) == backend || backend == BackendGeneric
}
func NormalizeArch(arch string) string {
	arch = strings.ToLower(strings.TrimSpace(arch))
	switch arch {
	case "x86_64", "x64":
		return "amd64"
	case "aarch64":
		return "arm64"
	default:
		return arch
	}
}
func FeaturesForArch(arch string) Features { return Features{Arch: NormalizeArch(arch)} }

func (f Features) HasSIMD() bool { return Select(f) != BackendGeneric }

// FeatureNames 返回已启用特性的稳定名称列表。
func (f Features) FeatureNames() []string {
	out := make([]string, 0, 9)
	if f.SSE {
		out = append(out, "sse")
	}
	if f.SSE4 {
		out = append(out, "sse4")
	}
	if f.AVX2 {
		out = append(out, "avx2")
	}
	if f.AVX512 {
		out = append(out, "avx512")
	}
	if f.AVX512VBMI {
		out = append(out, "avx512vbmi")
	}
	if f.NEON {
		out = append(out, "neon")
	}
	if f.SVE {
		out = append(out, "sve")
	}
	if f.SVE2 {
		out = append(out, "sve2")
	}
	if f.SVE2Bit {
		out = append(out, "sve2bit")
	}
	return out
}

// BackendFor 返回适配目标能力的安全后端；目标缺失时回退通用实现。
func (f Features) BackendFor(target Features) Backend {
	if !f.Supports(target) {
		return BackendGeneric
	}
	return Select(target)
}

// SelectChecked 在能力不满足目标后端时明确返回通用后端，避免调用方
// 将未检测到的高级指令误当作可执行路径。
func (f Features) SelectChecked(target Features) (Backend, error) {
	if !f.Valid() || !target.Valid() {
		return BackendGeneric, fmt.Errorf("invalid cpu feature set")
	}
	if !f.Compatible(target) {
		return BackendGeneric, fmt.Errorf("incompatible cpu architectures %q and %q", f.Arch, target.Arch)
	}
	if !f.Supports(target) {
		return BackendGeneric, fmt.Errorf("cpu features do not satisfy target: have=%s target=%s", f, target)
	}
	return Select(target), nil
}

// IsNative 判断当前后端是否依赖目标架构的专用实现。
func (b Backend) IsNative() bool { return b == BackendX86 || b == BackendARM64 }

// Compatible 判断两个能力集合是否可在同一架构上安全互换。
func (f Features) Compatible(other Features) bool {
	left, right := NormalizeArch(f.Arch), NormalizeArch(other.Arch)
	return left == "" || right == "" || left == right
}

// Merge 合并两组能力，架构取非空且一致的一方。
func (f Features) Merge(other Features) Features {
	arch := NormalizeArch(f.Arch)
	if arch == "" {
		arch = NormalizeArch(other.Arch)
	}
	if arch == "" {
		arch = NormalizeArch(runtime.GOARCH)
	}
	return Features{Arch: arch, SSE: f.SSE || other.SSE, SSE4: f.SSE4 || other.SSE4, AVX2: f.AVX2 || other.AVX2, AVX512: f.AVX512 || other.AVX512, AVX512VBMI: f.AVX512VBMI || other.AVX512VBMI, NEON: f.NEON || other.NEON, SVE: f.SVE || other.SVE, SVE2: f.SVE2 || other.SVE2, SVE2Bit: f.SVE2Bit || other.SVE2Bit}
}

// MergeChecked 在架构冲突时返回错误，避免把不同目标的能力误合并。
func (f Features) MergeChecked(other Features) (Features, error) {
	if !f.Compatible(other) {
		return Features{}, fmt.Errorf("incompatible architectures %q and %q", f.Arch, other.Arch)
	}
	return f.Merge(other), nil
}

// Supports 判断当前能力是否覆盖目标能力。
func (f Features) Supports(target Features) bool {
	return f.Compatible(target) && (!target.SSE || f.SSE) && (!target.SSE4 || f.SSE4) && (!target.AVX2 || f.AVX2) && (!target.AVX512 || f.AVX512) && (!target.AVX512VBMI || f.AVX512VBMI) && (!target.NEON || f.NEON) && (!target.SVE || f.SVE) && (!target.SVE2 || f.SVE2) && (!target.SVE2Bit || f.SVE2Bit)
}
func (f Features) String() string {
	return fmt.Sprintf("%s:sse=%t,sse4=%t,avx2=%t,avx512=%t,avx512vbmi=%t,neon=%t,sve=%t,sve2=%t,sve2bit=%t", f.Arch, f.SSE, f.SSE4, f.AVX2, f.AVX512, f.AVX512VBMI, f.NEON, f.SVE, f.SVE2, f.SVE2Bit)
}

// Clone 返回能力快照。
func (f Features) Clone() Features { return f }

// Normalize 清除与目标架构不匹配的特性标志。
func (f Features) Normalize() Features {
	f.Arch = NormalizeArch(f.Arch)
	if f.Arch == "" {
		f.Arch = NormalizeArch(runtime.GOARCH)
	}
	if f.Arch == "amd64" {
		f.NEON, f.SVE, f.SVE2, f.SVE2Bit = false, false, false, false
	}
	if f.Arch == "arm64" {
		f.SSE, f.SSE4, f.AVX2, f.AVX512, f.AVX512VBMI = false, false, false, false, false
	}
	if f.AVX512VBMI {
		f.AVX512, f.AVX2, f.SSE4, f.SSE = true, true, true, true
	} else if f.AVX512 {
		f.AVX2, f.SSE4, f.SSE = true, true, true
	} else if f.AVX2 {
		f.SSE4, f.SSE = true, true
	} else if f.SSE4 {
		f.SSE = true
	}
	if f.SVE2Bit {
		f.SVE2, f.SVE = true, true
	} else if f.SVE2 {
		f.SVE = true
	}
	return f
}
