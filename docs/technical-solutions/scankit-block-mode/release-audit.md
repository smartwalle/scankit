# Block 模式发布审计

更新时间：2026-09-20

发布操作步骤见 [release-runbook.md](./release-runbook.md)，变更记录见 [CHANGELOG.md](./CHANGELOG.md)。

## 接口与兼容性

- `Engine`、`Scanner` 的既有导出方法保持不变，新增能力均通过内部编译和执行路径接入。
- 序列化数据携带版本号；加载时拒绝未知版本、损坏载荷和不匹配的后端类型。
- 偏移采用半开区间 `[From, To)`；Scanner 不重新排序结果，需要稳定顺序时由调用方按起点、规则编号和终点排序；去重仍由执行路径负责。

## 能力与回退

| 能力 | 专用路径 | 不支持时行为 |
| --- | --- | --- |
| 受限字节 NFA | 位集合、稠密表、稀疏表、范围表、半字节表 | 回退通用图确认 |
| 重复文字 | Repeat | 回退通用图确认 |
| 文字候选 | FDR/Teddy/Noodle、通用向量筛选、区间窗口扫描 | 完整规则确认 |
| 模糊匹配 | 文字、固定宽度字节类 | 复杂图走 AST 确认 |
| 断言与 Unicode | 通用 AST | 不下沉到字节专用后端 |
| Rose | 角色、指令、候选确认、角色 cost 选择、队列压缩 | 规则级确认 |

## 资源与错误边界

- 编译阶段检查节点数、边数、状态数和内存预算，超限返回明确错误。
- 执行阶段支持步骤预算、结果预算、扫描区间和非法范围校验。
- 预算耗尽只返回已确认结果；无法安全确认的图不产生候选误报。

## 本轮能力证据

- HWLM：Teddy 与 Noodle 的首字节掩码升级为最多 4 个 lane 的连续字节窗口掩码，窗口尾部退回首字节判定避免漏报；
  长文字拆分、重叠与去重语义不变（`internal/hwlm/teddy/teddy_mask_test.go`、`internal/hwlm/noodle/noodle_mask_test.go`）。
- Rose：新增角色 cost 模型（`RoleCost`/`Role.Weight`）、按报告编号的代价索引（`Program.RolesForReport`、`Program.CheapestRoleForReport`）、
  等价角色合并（`scanner.dedupeRoseRoles`）与队列压缩（`Queue.PushAll`、`Queue.Compact`、`Scheduler.activateBatch`）；
  `(?:ab)c|a(?:bc)` 这类等价分支合并为单个角色且扫描结果不变（`internal/rose/role_cost_test.go`、`internal/rose/queue_compact_test.go`、`rose_role_selection_test.go`）。
- SIMD：ARM64 新增 NEON 原生无符号闭区间掩码（`nativeInRangeMask`），一次完成 16 字节比较并用 SWAR 压缩为 16 位掩码；
  能力不足或非 ARM64 平台回退通用实现（`internal/simd/arm64/native_inrange_arm64_test.go`、`internal/simd/backend_conformance_test.go`）。
- 分派：新增调优族与后端覆盖语义（`internal/dispatch/env.go` 的 `ParseEnvOverrides`、`EnvOverrides.Apply`、`EnvOverrides.ApplyFeatures`、
  `SetBackendOverride`）；非法配置保留完整注册表并继续安全回退（`internal/dispatch/env_test.go`）。
- 一致性与基线：`conformance_matrix_test.go` 在 generic、x86 六个能力层级与 ARM64 四个能力层级上复核规则、fixture 与错误边界一致性；
  `perf_baseline_test.go` 固定语料回归门禁与 `performance-baseline.md` 记录吞吐、延迟、分配、状态和布局内存。

## 验证门禁

```text
go test ./...
go test -race ./...
go vet ./...
git diff --check
```

以上门禁仅针对 Go 实现；参考源码目录不参与编译、运行或链接。

最近一次门禁已在当前工作区执行并通过；基准采样同步记录在 `performance-baseline.md`。专用状态表在构建阶段执行结构校验，输入上限收紧会裁剪跨块状态，前缀距离查询、流限量待发结果和跨引擎区间限量均使用统一结果顺序。

## 可重复基线

- 基准命令与最近一次结果记录在 `performance-baseline.md`；纯文字多规则路径直接消费候选切片，基线包含该路径。
- 基线用于检测同一环境下的回归，不替代部署方定义的绝对性能阈值。
- 每次改变执行布局或候选路径后，需重新采集 `ns/op`、`B/op` 和 `allocs/op`，并保留命令参数。

## 平台构建核验

- 当前运行平台 `darwin/arm64` 已完成完整 Go 门禁（`go test ./...`、`go test -race ./...`、`go vet ./...`、`git diff --check`）。
- `GOOS=linux GOARCH=amd64 go vet ./...` 与 `GOOS=linux GOARCH=arm64 go vet ./...` 已完成交叉静态检查。
- 本机已安装 Rosetta 2，`GOOS=darwin GOARCH=amd64 go test ./...` 的产物可直接执行，
  因此 x86 SSE2/SSE4 原生路径（`nativeInRangeMask`、`nativeByteSetMask`、`nativeByteSetMask32`）在 darwin/amd64 下真实运行并参与完整测试套件，
  不再只是交叉静态检查。
- Rosetta 2 的 CPUID 隐藏 AVX/AVX2，但能够正确执行 AVX2 指令：AVX2 内核（`nativeByteSetMask32AVX2`）通过
  `SCANKIT_AVX2_TESTS=1 GOOS=darwin GOARCH=amd64 go test ./internal/simd/...` 在本地真实执行，
  并与 SSSE3 半区拼接内核、通用标量参考逐位对照；该开关仅用于测试，运行时分派仍严格依赖 CPUID 能力探测。
- Rosetta 不执行 AVX512：`VPXORQ`/`VPCMPEQB ZMM`/`KMOVQ` 指令级探针在 darwin/amd64 下直接触发非法指令，
  因此 AVX512/VBMI 内核在本机不存在可执行验证路径，需要具备 AVX512 的目标主机或 CI runner 复核。
- x86/ARM64 后端均保持同一可移植实现契约；x86 原生路径同时通过 `SetBackendOverride` 作为回退行参与一致性矩阵。

## 已知限制

- 原生入口按运行时能力门禁启用；未检测到对应能力时使用可移植实现并安全回退。
- x86 的 `ByteSetMask`/`InRangeMask` 已收敛到 SSE2/SSSE3 原生实现：`InRangeMask` 使用饱和减法一次完成 16 字节闭区间判定，
  `ByteSetMaskPrepared` 使用 `PSHUFB` 半字节查表；ARM64 对应 `ByteSetMaskPrepared` 使用 `TBL` 半字节查表，
  并把 `EqualByteMask`/`EqualByteMaskFold`/`EqualMask` 从“写回向量再逐字节重建”改为直接返回位置掩码。
  未检测到所需能力时回退到可移植实现。
- 原生覆盖现状：SIMD 契约已扩展到 64 字节宽窗口（`simd.ByteSetTables` + `Backend.WindowMask` + `Backend.WindowMask64`），
  x86 提供 SSSE3 半区拼接、AVX2 256 位内核与 AVX512BW/VBMI 512 位内核，ARM64 提供 NEON 合并内核；
  Teddy、Noodle、Rose 候选枚举与 NFA `forEachNFAStart` 四条热路径全部切换为“64 字节宽窗口 → 32 字节超向量窗口补齐 → 逐字节回退”的三段无缝平铺；
  未检测到 AVX512 时回退到 AVX2，未检测到 AVX2 时回退到 SSSE3 路径，未检测到 SSSE3/NEON 时回退到通用标量参考实现。
- AVX512BW/VBMI 内核的额外门禁：`detectedTier` 只有在同时具备 `AVX512BW` 时才给出 AVX512 层级（仅具备 AVX512F 的机器退回 AVX2），
  且 `WindowMask64` 首次使用时对 512 位内核做穷举字节的一次性运行时自检，自检失败即永久退回经验证的 256 位拼接路径，
  不会在未知硬件上产出错误候选集合。
- AVX512/VBMI 专用内核缺少可执行验证平台（本机 Rosetta 仅支持到 AVX2，无 AVX512 主机），
  当前证据为交叉编译 + 穷举字节自检 + 标量参考逐位比对，属于“构造正确 + 运行时自检兜底”而非本机实测，
  仍需具备 AVX512 的目标主机或 CI runner 复核一次。
- SVE/SVE2 已显式排除（属于对 K-20 原始判据的修订，**待维护者确认**）：探针 `PTRUE P0.B, ALL` 在 darwin/arm64 与 linux/arm64 下、
  加与不加 `GOEXPERIMENT=simd` 均报 `unrecognized instruction`，即 Go 汇编器无法产出 SVE 指令；
  `golang.org/x/sys/cpu` 的 `cpu.ARM64` 也不提供 SVE 能力位，连运行时探测都无法实现；本机更无 SVE 硬件可验证。
  三点叠加使 SVE 无法满足“测试通过、边界已验证”的完成要求，故不写入不可验证的汇编。覆盖影响：Go 把 ASIMD 视为 arm64 基线能力，
  Apple Silicon 与主流 arm64 服务器均具备，NEON 内核已覆盖全部实际 arm64 目标；任何未识别能力仍回退到 NEON 或通用标量参考实现。
- Miracle 候选器覆盖多角色纯文字程序；`FindMatches`/`FindMatchesRange` 在候选器就绪时不再重建通用匹配器
  （该缺陷由 K-26 修复，结果与匹配器路径逐位一致）。
- 复杂 Unicode、变宽 lookaround 和非文字模糊图继续使用通用确认路径。
- 最低工具链为 Go 1.27（`go.mod` 声明 `go 1.27`）：arm64 SIMD 内核使用 `VCMHS`/`VUSHL` 等 Go 1.27 才提供的
  NEON 助记符（Go 1.26 的 arm64 汇编器助记符表中不存在这两条，构建会报 `unrecognized instruction "VCMHS"`）。
  低版本工具链在 `GOTOOLCHAIN=auto`（默认）下会自动切换到 1.27；`GOTOOLCHAIN=local` 下会在加载 `go.mod` 阶段给出明确错误。
  x86 侧的 `internal/simd/x86/*.s` 已审计确认只使用 1.26 起即支持的 SSE/AVX 指令，不受该约束影响。
- 性能阈值需由部署环境定义，基准数据不得替代正式阈值判定。
