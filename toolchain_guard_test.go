package scankit

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// arm64Go127Mnemonics 是 Go 1.27 才加入汇编器、1.26 缺失的助记符。
var arm64Go127Mnemonics = []string{"VCMHS", "VUSHL"}

func moduleGoDirective(t *testing.T, path string) (major, minor int) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取 %s 失败: %v", path, err)
	}
	for line := range strings.SplitSeq(string(data), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "go ") {
			continue
		}
		value := strings.TrimSpace(strings.TrimPrefix(line, "go "))
		parts := strings.SplitN(value, ".", 2)
		if len(parts) != 2 {
			t.Fatalf("go 指令 %q 不是 major.minor 形式", value)
		}
		major, err = strconv.Atoi(parts[0])
		if err != nil {
			t.Fatalf("go 指令主版本 %q 解析失败: %v", value, err)
		}
		minor, err = strconv.Atoi(parts[1])
		if err != nil {
			t.Fatalf("go 指令次版本 %q 解析失败: %v", value, err)
		}
		return major, minor
	}
	t.Fatalf("%s 缺少 go 指令", path)
	return 0, 0
}

// TestArm64KernelsRequireGo127Toolchain 守住 arm64 SIMD 内核与最低工具链版本的一致性：
// 内核使用 Go 1.27 才支持的 NEON 助记符，因此 go.mod 的 go 指令不得低于 1.27，
// 否则低版本工具链会在汇编阶段报错，而不是给出可定位的版本错误。
func TestArm64KernelsRequireGo127Toolchain(t *testing.T) {
	major, minor := moduleGoDirective(t, "go.mod")
	if major < 1 || (major == 1 && minor < 27) {
		t.Fatalf("go.mod 声明 go %d.%d，但 arm64 内核使用 Go 1.27 才支持的助记符 %v，需要 go >= 1.27",
			major, minor, arm64Go127Mnemonics)
	}

	files, err := filepath.Glob(filepath.Join("internal", "simd", "arm64", "*.s"))
	if err != nil {
		t.Fatalf("枚举 arm64 汇编文件失败: %v", err)
	}
	if len(files) == 0 {
		t.Fatal("未找到 internal/simd/arm64 下的汇编文件，测试断言已失效")
	}

	// 反向确认：确实存在依赖 1.27 助记符的内核，保证上面的版本下界不被误删。
	used := make(map[string][]string)
	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("读取 %s 失败: %v", file, err)
		}
		body := string(data)
		for _, mnemonic := range arm64Go127Mnemonics {
			if strings.Contains(body, "\t"+mnemonic+" ") || strings.Contains(body, "\t"+mnemonic+"\t") {
				used[mnemonic] = append(used[mnemonic], filepath.Base(file))
			}
		}
	}
	if len(used) == 0 {
		t.Fatalf("未在任何 arm64 汇编文件中找到 %v，请同步更新最低工具链断言", arm64Go127Mnemonics)
	}
	t.Logf("arm64 内核使用 Go 1.27 助记符: %v", used)
}
