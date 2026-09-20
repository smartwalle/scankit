# Block 模式发布检查手册

## 发布前门禁

```bash
go test ./...
go test -race ./...
go vet ./...
git diff --check
```

工具链最低版本为 Go 1.27（`go.mod` 声明 `go 1.27`）：arm64 SIMD 内核使用 Go 1.27 才提供的 NEON 助记符
（`VCMHS`、`VUSHL` 等无符号排序与变长移位指令）。默认 `GOTOOLCHAIN=auto` 下，本地工具链早于 1.27 时会自动
切换到 1.27 并继续构建；`GOTOOLCHAIN=local` 下会在加载 `go.mod` 阶段直接报
`go: go.mod requires go >= 1.27`，不会退化为“汇编器无法识别指令”的构建失败。

## API 与范围审计

- 对照公开 API 冻结快照，确认 `Engine`、`Scanner` 未新增导出方法。
- 确认流式 Session、报告/统计业务和参考源码执行均未接入发布构建。
- 确认所有编译失败、损坏布局、资源超限和平台能力不足均有分类错误或安全回退。

## 平台回退

运行时能力探测失败或目标特性不足时选择通用后端；不得执行未检测的高级指令。

可用以下环境变量在目标环境复核回退路径（只影响后端选择与调优裁剪，不改变匹配语义）：

```bash
SCANKIT_TUNE_FAMILY=slm      # 按调优族清除该微架构不具备的 AVX2/AVX512/VBMI
SCANKIT_DISABLE_BACKENDS=x86 # 禁用指定后端，逗号分隔
SCANKIT_FORCE_BACKEND=generic
```

一致性复核（任意架构均可执行，非本机能力层级会安全回退）：

```bash
go test -run 'TestScanConformanceAcrossBackendMatrix|TestScanFixturesAcrossBackendMatrix' .
go test -run 'TestBackendInRangeMaskCoversAllValues|TestBackendPreparedByteSet|TestBackendEqualMasksCoverAllValues|TestBackendWindowMaskMatchesScalar|TestBackendWindowMaskCoversAllBytes|TestBackendWindowMask64MatchesScalar|TestBackendWindowMask64CoversAllBytes' ./internal/simd
go test -run 'TestForEachNFAStartWindow|TestEngineFamiliesWindowBoundaryConformance' ./internal/nfa
go test -run 'TestMiracle|TestNormalizePreservesMiracleCandidatePath' ./internal/rose
go test -run 'TestFindMatchesWideWindowBoundary' ./internal/hwlm/...
go test -run TestFixedCorpusDeterministicMetrics .
```

x86 原生路径的真实执行复核（Apple Silicon 主机已安装 Rosetta 2 时）：

```bash
softwareupdate --install-rosetta --agree-to-license   # 仅首次需要
GOOS=darwin GOARCH=amd64 CGO_ENABLED=0 go test ./...
GOOS=darwin GOARCH=amd64 CGO_ENABLED=1 go test -race ./internal/simd/...
```

Rosetta 的 CPUID 只暴露 SSE2/SSSE3/SSE4.2，因此上面的命令覆盖 x86 的 SSE 原生掩码路径。
Rosetta 能够执行 AVX2 指令但不申报该能力，因此 AVX2 内核需显式开启复核（该开关只影响测试，不影响运行时分派）：

```bash
SCANKIT_AVX2_TESTS=1 GOOS=darwin GOARCH=amd64 CGO_ENABLED=0 go test -count=1 ./internal/simd/... ./internal/hwlm/...
SCANKIT_AVX2_TESTS=1 GOOS=darwin GOARCH=amd64 CGO_ENABLED=0 go test -count=1 -race ./...
```

Rosetta 不支持 AVX512（ZMM/K 寄存器指令直接触发非法指令），因此 AVX512/VBMI 层级必须在带对应能力的 x86 硬件上复核；
复核时需要求 `go test ./...` 与 `SCANKIT_AVX2_TESTS=1` 的差分测试全部通过。
该主机上 AVX512 相关用例会自动跳过（`HasAVX512BW`/`HasAVX512VBMI` 为假），跳过的用例清单为
`TestNativeByteSetMask64AVX512MatchesReference`、`TestNativeByteSetMask64VBMIMatchesReference`、
`TestNativeByteSetMask64KernelsAgree`、`TestWideKernelSelfCheckPasses`、`TestBackendWindowMask64AVX512MatchesScalar`；
若使用会在 CPUID 中隐藏 AVX512 的虚拟化环境，可用 `SCANKIT_AVX512_TESTS=1` 显式开启（不具备该指令的主机会以非法指令终止测试进程）。

## 性能基线

发布前重新采集固定语料与既有基准，并把 `ns/op`、`B/op`、`allocs/op` 写入 `performance-baseline.md`：

```bash
go test -run '^$' -bench 'Benchmark(Scan|EngineFamilies|ResourceUsage|ProgramFindMatches|FindAll|BackendEqualByteMask|ScanRuleScales|FixedCorpusScan|QueuePushAll|FindIntoByteMask|InRangeMask|WindowMask|NativeByteSetMask|ForEachNFAStartFirstByte|MiracleWindowCandidates)' -benchmem ./...
```

确定性指标（命中集合、执行后端、状态数、布局内存、分配上限）由 `TestFixedCorpusDeterministicMetrics` 强制校验，时间指标只记录不设阈值。

## 回滚步骤

1. 停止发布并保留门禁输出。
2. 使用上一版本的 Go 模块和编译产物恢复服务。
3. 记录失败平台、规则规模、输入规模和错误分类后再重新验证。
