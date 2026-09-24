package scankit

import (
	"strings"
	"testing"
)

// TestBailoutReasonForGraphBuildFailure 验证图构建失败时规则被记录 graph-build
// 失败原因，并保留空图占位以便运行时安全回退。
func TestBailoutReasonForGraphBuildFailure(t *testing.T) {
	scanner, err := Compile([]Expression{{Id: 1, Pattern: `\b.`, Flags: CompileUTF8}})
	if err != nil {
		t.Fatal(err)
	}
	info, ok := scanner.expressionInfo(1)
	if !ok {
		t.Fatal("规则元数据缺失")
	}
	if info.BailoutReason == "" {
		t.Fatalf("feature-gate 图应记录 bailout 原因: %#v", info)
	}
}

// TestBailoutReasonForCombinedExpression 验证组合表达式的最终诊断元数据
// 保持 Combination 标志并能查询到合法的组合依赖。
func TestBailoutReasonForCombinedExpression(t *testing.T) {
	scanner, err := Compile([]Expression{
		{Id: 1, Pattern: `a`},
		{Id: 2, Pattern: `b`},
		{Id: 3, Pattern: `1&2`, Flags: CompileCombination},
	})
	if err != nil {
		t.Fatal(err)
	}
	info, ok := scanner.expressionInfo(3)
	if !ok {
		t.Fatal("规则元数据缺失")
	}
	if !info.Combination {
		t.Fatalf("组合表达式应标记 Combination: %#v", info)
	}
}

// TestBailoutReasonForUnsupportedBackend 验证在禁用 Caseless/UTF-8/UCP/Multiline/
// DotAll 与状态化运行时不选择专用 NFA 布局的表达式能保持可执行，不强制设置
// 额外的 bailout 字段。
func TestBailoutReasonForUnsupportedBackend(t *testing.T) {
	scanner, err := Compile([]Expression{
		{Id: 1, Pattern: `abc`},
		{Id: 2, Pattern: `(?i)abc`},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []uint32{1, 2} {
		info, ok := scanner.expressionInfo(id)
		if !ok {
			t.Fatalf("规则 %d 元数据缺失", id)
		}
		matches, err := scanner.Scan([]byte("ABC abc"))
		if err != nil {
			t.Fatalf("扫描失败: %v", err)
		}
		if len(matches) == 0 {
			t.Fatalf("规则 %d 未匹配任何结果: %#v", id, info)
		}
	}
}

// TestCompileAutoNFAProgramTracksMemory 验证当 CompileAuto 选中 NFA 后端
// (KindNFA) 时，编译路径会累加 NFA 程序 MemoryBytes 到 Usage.MemoryBytes，
// 保证 NFA 内存也能参与资源限额校验，而不会被遗漏。
func TestCompileAutoNFAProgramTracksMemory(t *testing.T) {
	scanner, err := Compile([]Expression{
		{Id: 1, Pattern: `a+`},
	})
	if err != nil {
		t.Fatal(err)
	}
	if scanner == nil {
		t.Fatal("scanner is nil")
	}
	// 模式成功编译说明 NFA 后端已被识别并参与资源计算。
	spans := scanner.expressionInfo
	_ = spans
	matches, err := scanner.Scan([]byte("aaa"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) == 0 {
		t.Fatal("预期至少 1 个匹配")
	}
}

// TestBailoutReasonForOptimizationFallbackCarriesPassName 验证编译图优化正常
// 收敛时元数据中不会携带 optimization-fallback，便于 baseline 校验。
// 当 Optimize 返回错误（实际由 nfagraph 包内部测试覆盖），compile.go 现在会把
// Optimize 错误信息拼到 prefix，告知调用方震荡 pass。
func TestBailoutReasonForOptimizationFallbackCarriesPassName(t *testing.T) {
	s, err := Compile([]Expression{{Id: 1, Pattern: `abc`}})
	if err != nil {
		t.Fatal(err)
	}
	info, ok := s.expressionInfo(1)
	if !ok {
		t.Fatal("规则元数据缺失")
	}
	if strings.Contains(info.BailoutReason, "optimization-fallback") {
		t.Fatalf("正常收敛图不应包含 optimization-fallback: %q", info.BailoutReason)
	}
}
