package prefilter

import "testing"

// TestVariantClassFlag 验证候选文字携带"是否来自字符类展开"的标记：
// 调用方依赖该标记决定忽略大小写的规则能否直接复用候选字节。
func TestVariantClassFlag(t *testing.T) {
	literal := mustRequired(t, `password\s*[:=]\s*\S+`)
	for _, variant := range literal.Variants {
		if variant.Class {
			t.Fatalf("字面量来源的候选 %q 不应标记为字符类展开", variant.Value)
		}
	}
	class := mustRequired(t, `[a-c]x`)
	if len(class.Variants) != 3 {
		t.Fatalf("字符类候选数量=%d，期望 3", len(class.Variants))
	}
	for _, variant := range class.Variants {
		if !variant.Class {
			t.Fatalf("字符类来源的候选 %q 应标记为字符类展开", variant.Value)
		}
	}
}
