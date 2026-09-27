package dispatch

import (
	"reflect"
	"testing"
)

func envLookup(values map[string]string) func(string) (string, bool) {
	return func(key string) (string, bool) {
		value, ok := values[key]
		return value, ok
	}
}

// TestParseTuneFamilyRoundTrip 验证调优族名称解析与字符串输出一致，未知名称被拒绝。
func TestParseTuneFamilyRoundTrip(t *testing.T) {
	for _, family := range []TuneFamily{TuneGeneric, TuneSNB, TuneHSW, TuneSLM, TuneSKL, TuneSKX, TuneICX} {
		name := family.String()
		parsed, ok := ParseTuneFamily(name)
		if !ok || parsed != family {
			t.Fatalf("调优族往返失败: %s -> %v ok=%v", name, parsed, ok)
		}
		if upper, ok := ParseTuneFamily("  " + upperASCII(name) + " "); !ok || upper != family {
			t.Fatalf("调优族名称应大小写不敏感并忽略空白: %q -> %v ok=%v", name, upper, ok)
		}
	}
	if _, ok := ParseTuneFamily("unknown"); ok {
		t.Fatal("未知调优族不应解析成功")
	}
	if _, ok := ParseTuneFamily(""); ok {
		t.Fatal("空调优族不应解析成功")
	}
	if got := TuneFamily(12345).String(); got != "unknown" {
		t.Fatalf("越界调优族名称错误: %s", got)
	}
}

func upperASCII(value string) string {
	out := []byte(value)
	for i := range out {
		if out[i] >= 'a' && out[i] <= 'z' {
			out[i] -= 'a' - 'A'
		}
	}
	return string(out)
}

// TestParseEnvOverridesDisableForceAndTune 验证环境变量解析结果。
func TestParseEnvOverridesDisableForceAndTune(t *testing.T) {
	overrides, err := ParseEnvOverrides(envLookup(map[string]string{
		EnvDisableBackends: "x86, arm64 ,",
		EnvForceBackend:    "Generic",
		EnvTuneFamily:      "SKL",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(overrides.Disabled, []Backend{BackendX86, BackendARM64}) {
		t.Fatalf("禁用后端解析错误: %#v", overrides.Disabled)
	}
	if !overrides.HasForce || overrides.Forced != BackendGeneric {
		t.Fatalf("强制后端解析错误: %#v", overrides)
	}
	if !overrides.HasTune || overrides.Tune != TuneSKL {
		t.Fatalf("调优族解析错误: %#v", overrides)
	}

	empty, err := ParseEnvOverrides(envLookup(map[string]string{}))
	if err != nil || empty.HasForce || empty.HasTune || len(empty.Disabled) != 0 {
		t.Fatalf("未设置环境变量时不应产生覆盖: %#v err=%v", empty, err)
	}
	blank, err := ParseEnvOverrides(envLookup(map[string]string{EnvForceBackend: "  ", EnvTuneFamily: " "}))
	if err != nil || blank.HasForce || blank.HasTune {
		t.Fatalf("空白环境变量应被忽略: %#v err=%v", blank, err)
	}
	for _, tc := range []map[string]string{
		{EnvDisableBackends: "mmx"},
		{EnvForceBackend: "mmx"},
		{EnvTuneFamily: "atom"},
	} {
		if _, err := ParseEnvOverrides(envLookup(tc)); err == nil {
			t.Fatalf("非法环境变量应报错: %#v", tc)
		}
	}
}

// TestEnvOverridesApplyDisablesBackends 验证禁用与强制后端会改变注册表选择结果。
func TestEnvOverridesApplyDisablesBackends(t *testing.T) {
	amdFeatures := Features{Arch: "amd64", SSE: true, SSE4: true, AVX2: true}
	registry := DefaultRegistry()
	if got := registry.Select(amdFeatures); got != BackendX86 {
		t.Fatalf("默认注册表应选择 x86: %v", got)
	}
	disabled := DefaultRegistry()
	EnvOverrides{Disabled: []Backend{BackendX86}}.Apply(disabled)
	if got := disabled.Select(amdFeatures); got != BackendGeneric {
		t.Fatalf("禁用 x86 后应回退 generic: %v", got)
	}
	forced := DefaultRegistry()
	EnvOverrides{Forced: BackendGeneric, HasForce: true}.Apply(forced)
	if got := forced.Select(amdFeatures); got != BackendGeneric {
		t.Fatalf("强制 generic 后仍选中 %v", got)
	}
	armFeatures := Features{Arch: "arm64", NEON: true}
	armForced := DefaultRegistry()
	EnvOverrides{Forced: BackendARM64, HasForce: true}.Apply(armForced)
	if got := armForced.Select(armFeatures); got != BackendARM64 {
		t.Fatalf("强制 arm64 后应选中 arm64: %v", got)
	}
	if got := armForced.Select(amdFeatures); got != BackendGeneric {
		t.Fatalf("强制 arm64 时 x86 主机应回退 generic: %v", got)
	}
}

// TestEnvOverridesApplyFeatures 验证调优族会裁剪对应微架构不具备的指令集。
func TestEnvOverridesApplyFeatures(t *testing.T) {
	full := Features{Arch: "amd64", SSE: true, SSE4: true, AVX2: true, AVX512: true, AVX512VBMI: true}
	cases := []struct {
		tune     TuneFamily
		wantAVX2 bool
		wantAVX  bool
		wantVBMI bool
	}{
		{TuneGeneric, false, false, false},
		{TuneSNB, false, false, false},
		{TuneSLM, false, false, false},
		{TuneHSW, true, false, false},
		{TuneSKL, true, false, false},
		{TuneSKX, true, true, true},
		{TuneICX, true, true, true},
	}
	for _, tc := range cases {
		got := EnvOverrides{Tune: tc.tune, HasTune: true}.ApplyFeatures(full)
		if got.AVX2 != tc.wantAVX2 || got.AVX512 != tc.wantAVX || got.AVX512VBMI != tc.wantVBMI {
			t.Fatalf("调优族 %s 裁剪错误: %#v", tc.tune, got)
		}
		if !got.SSE || !got.SSE4 {
			t.Fatalf("调优族 %s 不应清除基础能力: %#v", tc.tune, got)
		}
	}
	arm := Features{Arch: "arm64", NEON: true, SVE: true, SVE2: true}
	if got := (EnvOverrides{Tune: TuneGeneric, HasTune: true}).ApplyFeatures(arm); !got.SVE2 || !got.NEON {
		t.Fatalf("调优族不应裁剪 ARM 能力: %#v", got)
	}
	if got := (EnvOverrides{}).ApplyFeatures(full); !reflect.DeepEqual(got, full.Normalize()) {
		t.Fatalf("未设置调优族时应保持能力集合: %#v", got)
	}
}
