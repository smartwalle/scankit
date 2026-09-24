package dispatch

import (
	"fmt"
	"os"
	"strings"
)

// 环境变量用于在不重新编译的情况下覆盖后端选择与调优族，
// 便于在目标平台验证原生路径和安全回退路径。
const (
	// EnvDisableBackends 是以逗号分隔的后端禁用列表，例如 "x86,arm64"。
	EnvDisableBackends = "SCANKIT_DISABLE_BACKENDS"
	// EnvForceBackend 强制只使用指定后端，例如 "generic"。
	EnvForceBackend = "SCANKIT_FORCE_BACKEND"
	// EnvTuneFamily 覆盖调优族，例如 "skl"。
	EnvTuneFamily = "SCANKIT_TUNE_FAMILY"
)

var tuneFamilyNames = []struct {
	family TuneFamily
	name   string
}{
	{TuneGeneric, "generic"},
	{TuneSNB, "snb"},
	{TuneIVB, "ivb"},
	{TuneHSW, "hsw"},
	{TuneSLM, "slm"},
	{TuneBDW, "bdw"},
	{TuneSKL, "skl"},
	{TuneSKX, "skx"},
	{TuneGLM, "glm"},
	{TuneICL, "icl"},
	{TuneICX, "icx"},
}

// String 返回调优族的稳定小写名称。
func (t TuneFamily) String() string {
	for _, entry := range tuneFamilyNames {
		if entry.family == t {
			return entry.name
		}
	}
	return "unknown"
}

// ParseTuneFamily 解析调优族名称，名称大小写不敏感。
func ParseTuneFamily(name string) (TuneFamily, bool) {
	trimmed := strings.ToLower(strings.TrimSpace(name))
	if trimmed == "" {
		return TuneGeneric, false
	}
	for _, entry := range tuneFamilyNames {
		if entry.name == trimmed {
			return entry.family, true
		}
	}
	return TuneGeneric, false
}

// tuneSupportsAVX2 表示调优族对应的微架构是否具备 AVX2。
func tuneSupportsAVX2(t TuneFamily) bool {
	switch t {
	case TuneHSW, TuneBDW, TuneSKL, TuneSKX, TuneGLM, TuneICL, TuneICX:
		return true
	default:
		return false
	}
}

// tuneSupportsAVX512 表示调优族对应的微架构是否具备 AVX512。
func tuneSupportsAVX512(t TuneFamily) bool {
	switch t {
	case TuneSKX, TuneICL, TuneICX:
		return true
	default:
		return false
	}
}

// EnvOverrides 保存环境变量解析结果，便于在测试中脱离全局环境验证覆盖语义。
type EnvOverrides struct {
	// Disabled 是显式禁用的后端列表。
	Disabled []Backend
	// Forced 是强制使用的后端，仅在 HasForce 为真时生效。
	Forced Backend
	// HasForce 表示设置了强制后端。
	HasForce bool
	// Tune 是覆盖后的调优族，仅在 HasTune 为真时生效。
	Tune TuneFamily
	// HasTune 表示设置了调优族覆盖。
	HasTune bool
}

// ParseEnvOverrides 解析环境变量。lookup 为 nil 时读取进程环境。
// 未知后端名或调优名返回错误，调用方应保留默认注册表继续安全回退。
func ParseEnvOverrides(lookup func(string) (string, bool)) (EnvOverrides, error) {
	if lookup == nil {
		lookup = os.LookupEnv
	}
	var out EnvOverrides
	if raw, ok := lookup(EnvDisableBackends); ok {
		for item := range strings.SplitSeq(raw, ",") {
			item = strings.TrimSpace(item)
			if item == "" {
				continue
			}
			backend, ok := ParseBackend(item)
			if !ok {
				return EnvOverrides{}, fmt.Errorf("unknown backend %q in %s", item, EnvDisableBackends)
			}
			out.Disabled = append(out.Disabled, backend)
		}
	}
	if raw, ok := lookup(EnvForceBackend); ok && strings.TrimSpace(raw) != "" {
		backend, ok := ParseBackend(strings.TrimSpace(raw))
		if !ok {
			return EnvOverrides{}, fmt.Errorf("unknown backend %q in %s", raw, EnvForceBackend)
		}
		out.Forced = backend
		out.HasForce = true
	}
	if raw, ok := lookup(EnvTuneFamily); ok && strings.TrimSpace(raw) != "" {
		family, ok := ParseTuneFamily(raw)
		if !ok {
			return EnvOverrides{}, fmt.Errorf("unknown tuning family %q in %s", raw, EnvTuneFamily)
		}
		out.Tune = family
		out.HasTune = true
	}
	return out, nil
}

// Apply 把覆盖结果写入注册表：显式禁用优先，强制后端会禁用其余后端。
func (o EnvOverrides) Apply(r *Registry) {
	if r == nil {
		return
	}
	for _, backend := range o.Disabled {
		r.SetDisabled(backend, true)
	}
	if !o.HasForce {
		return
	}
	for _, backend := range allBackends() {
		if backend != o.Forced {
			r.SetDisabled(backend, true)
		}
	}
}

// ApplyFeatures 按调优族裁剪能力集合：调优族不具备的指令集会被清除，
// 避免在受限平台上选中不安全的原生路径。未设置调优族时原样返回。
func (o EnvOverrides) ApplyFeatures(f Features) Features {
	if !o.HasTune {
		return f
	}
	f = f.Normalize()
	if !tuneSupportsAVX2(o.Tune) {
		// AVX512 隐含 AVX2，调优族不支持 AVX2 时必须同时清空更高层级。
		f.AVX2 = false
		f.AVX512 = false
		f.AVX512VBMI = false
	} else if !tuneSupportsAVX512(o.Tune) {
		f.AVX512 = false
		f.AVX512VBMI = false
	}
	return f.Normalize()
}

func allBackends() []Backend {
	return []Backend{BackendGeneric, BackendX86, BackendARM64}
}
