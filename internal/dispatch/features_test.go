package dispatch

import "testing"

func TestFeatureNormalizationAndNames(t *testing.T) {
	f := Features{Arch: "x86_64", SSE: true, NEON: true}
	if Select(f) != BackendX86 || f.Normalize().NEON {
		t.Fatal("架构能力规范化失败")
	}
	names := f.FeatureNames()
	if len(names) != 2 || names[0] != "sse" {
		t.Fatalf("能力名称=%v", names)
	}
}

func TestSelectCheckedRejectsUnsupportedTarget(t *testing.T) {
	have := Features{Arch: "amd64", SSE: true}
	if _, err := have.SelectChecked(Features{Arch: "amd64", AVX2: true}); err == nil {
		t.Fatal("未满足高级特性时未返回错误")
	}
	if got, err := have.SelectChecked(Features{Arch: "amd64", SSE: true}); err != nil || got != BackendX86 {
		t.Fatalf("已满足特性选择错误: backend=%v err=%v", got, err)
	}
}
