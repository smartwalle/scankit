package dispatch

import (
	"testing"

	"github.com/smartwalle/scankit/internal/simd"

	armbackend "github.com/smartwalle/scankit/internal/simd/arm64"
	x86backend "github.com/smartwalle/scankit/internal/simd/x86"
)

func TestSelectFallback(t *testing.T) {
	if got := Select(Features{Arch: "unknown"}); got != BackendGeneric {
		t.Fatalf("unknown backend = %v", got)
	}
	if got := Select(Features{Arch: "amd64", AVX2: true}); got != BackendX86 {
		t.Fatalf("x86 backend = %v", got)
	}
	if got := Select(Features{Arch: "arm64", NEON: true}); got != BackendARM64 {
		t.Fatalf("arm backend = %v", got)
	}
}

func TestSelectBackendSafeFallback(t *testing.T) {
	b := SelectBackend(Features{Arch: "unknown"})
	if _, ok := b.Load(make([]byte, 16), 0); !ok {
		t.Fatal(ok)
	}
}

func TestSelectBackendArchitectureSemantics(t *testing.T) {
	data := []byte("Abcabc")
	for _, features := range []Features{{Arch: "amd64", SSE: true}, {Arch: "arm64", NEON: true}, {Arch: "unknown"}} {
		backend := SelectBackend(features)
		vector := backend.PartialLoad(data, 0)
		if mask := backend.EqualByteMask(vector, 'A'); mask&1 == 0 {
			t.Fatalf("架构后端首字节比较错误: features=%v mask=%x", features, mask)
		}
	}
}

func TestDetectReportsBaselineVectorCapability(t *testing.T) {
	f := Detect()
	if f.Arch == "amd64" && !f.SSE {
		t.Fatal("amd64 基础向量能力未报告")
	}
	if f.Arch == "arm64" && !f.NEON {
		t.Fatal("arm64 基础向量能力未报告")
	}
}

func TestFeaturesCompatibilityTreatsEmptyArchitectureAsWildcard(t *testing.T) {
	if !(Features{}).Compatible(Features{Arch: "amd64"}) || !(Features{Arch: "arm64"}).Compatible(Features{}) {
		t.Fatal("空架构应作为未指定处理")
	}
}

func TestBackendForAndNative(t *testing.T) {
	f := Features{Arch: "amd64", AVX2: true}
	if !BackendX86.IsNative() || BackendGeneric.IsNative() {
		t.Fatal("后端属性错误")
	}
	if got := f.BackendFor(Features{Arch: "amd64", AVX2: true}); got != BackendX86 {
		t.Fatalf("后端=%v", got)
	}
	if got := f.BackendFor(Features{Arch: "arm64", NEON: true}); got != BackendGeneric {
		t.Fatalf("架构不兼容时应回退=%v", got)
	}
}

func TestBackendTierFollowsDetectedCapabilities(t *testing.T) {
	cases := []struct {
		name string
		f    Features
		want x86backend.Tier
	}{
		{"sse", Features{Arch: "amd64", SSE: true}, x86backend.TierSSE},
		{"sse4", Features{Arch: "amd64", SSE4: true}, x86backend.TierSSE4},
		{"avx2", Features{Arch: "amd64", AVX2: true}, x86backend.TierAVX2},
		{"avx512", Features{Arch: "amd64", AVX512: true}, x86backend.TierAVX512},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := x86backend.TierOf(SelectBackend(tc.f))
			if !ok || got != tc.want {
				t.Fatalf("能力层级=%v/%v, want=%v", got, ok, tc.want)
			}
		})
	}
	if _, ok := x86backend.TierOf(SelectBackend(Features{Arch: "unknown", AVX512VBMI: true})); ok {
		t.Fatal("未知架构不应返回 x86 后端")
	}
	if got, ok := armbackend.TierOf(SelectBackend(Features{Arch: "arm64", SVE2: true})); !ok || got != armbackend.TierSVE2 {
		t.Fatalf("ARM 能力层级=%v/%v", got, ok)
	}
}

func TestDefaultRegistryIsOrderedAndFallsBack(t *testing.T) {
	r := DefaultRegistry()
	if err := r.Validate(); err != nil {
		t.Fatal(err)
	}
	if got := r.Select(Features{Arch: "amd64"}); got != BackendGeneric {
		t.Fatalf("无特性 x86 应回退通用后端: %v", got)
	}
	if got := r.Select(Features{Arch: "amd64", AVX2: true}); got != BackendX86 {
		t.Fatalf("AVX2 未选择 x86 后端: %v", got)
	}
	if got := r.Select(Features{Arch: "unknown", AVX2: true}); got != BackendGeneric {
		t.Fatalf("未知架构不应启用 x86 后端: %v", got)
	}
	entries := r.Entries()
	if len(entries) < 2 || entries[len(entries)-1].Backend != BackendGeneric {
		t.Fatalf("注册表缺少通用兜底: %#v", entries)
	}
}

func TestRegistryCanDisableBackendAndRecover(t *testing.T) {
	r := DefaultRegistry()
	f := Features{Arch: "amd64", AVX2: true}
	if got := r.Select(f); got != BackendX86 {
		t.Fatalf("初始选择错误: %v", got)
	}
	r.SetDisabled(BackendX86, true)
	if got := r.Select(f); got != BackendGeneric {
		t.Fatalf("禁用后应回退通用后端: %v", got)
	}
	if !r.Disabled(BackendX86) {
		t.Fatal("禁用状态未保存")
	}
	r.SetDisabled(BackendX86, false)
	if got := r.Select(f); got != BackendX86 {
		t.Fatalf("恢复后未重新选择 x86: %v", got)
	}
}

func TestNormalizeInfersHostArchitectureBeforeFeatureSelection(t *testing.T) {
	features := Features{AVX2: true, NEON: true}
	normalized := features.Normalize()
	if normalized.Arch == "" {
		t.Fatal("未指定架构时未生成主机架构快照")
	}
	if normalized.Arch == "amd64" && (normalized.NEON || !normalized.AVX2) {
		t.Fatalf("amd64 能力未正确隔离: %#v", normalized)
	}
	if normalized.Arch == "arm64" && (normalized.AVX2 || !normalized.NEON) {
		t.Fatalf("arm64 能力未正确隔离: %#v", normalized)
	}
}

func TestSelectionMatrixNeverCrossesArchitectureOrDropsToUnsafeBackend(t *testing.T) {
	cases := []Features{
		{Arch: "amd64", SSE: true}, {Arch: "amd64", SSE4: true},
		{Arch: "amd64", AVX2: true}, {Arch: "amd64", AVX512VBMI: true},
		{Arch: "arm64", NEON: true}, {Arch: "arm64", SVE: true},
		{Arch: "arm64", SVE2: true}, {Arch: "unknown", AVX512VBMI: true},
	}
	for _, features := range cases {
		backend := SelectBackend(features)
		vector := backend.PartialLoad([]byte("unaligned-window"), 1)
		generic := simd.Default().PartialLoad([]byte("unaligned-window"), 1)
		if backend.EqualByteMask(vector, 'n') != (simd.GenericBackend{}).EqualByteMask(generic, 'n') {
			t.Fatalf("掩码语义不一致: features=%v", features)
		}
	}
}

func TestPlatformContractAndFeatureEncoding(t *testing.T) {
	p := Platform{CPUFeatures: CPUFeatureAVX512VBMI, Tune: TuneICX}
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	f := p.Features()
	if !f.AVX512VBMI || !f.AVX512 || !f.AVX2 || f.Arch != "amd64" {
		t.Fatalf("平台能力未展开: %#v", f)
	}
	if err := (Platform{CPUFeatures: 1}).Validate(); err == nil {
		t.Fatal("未知 CPU 能力位未拒绝")
	}
	if err := (Platform{Tune: maxTuneFamily + 1}).Validate(); err == nil {
		t.Fatal("未知调优族未拒绝")
	}
}
