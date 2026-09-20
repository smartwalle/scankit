# Block 模式基准基线

采集日期：2026-09-20
环境：darwin/arm64，Apple M5 Pro；x86 行在 darwin/amd64 + Rosetta 2 下采集（Rosetta 暴露 SSE2/SSSE3/SSE4.2，
CPUID 不暴露 AVX/AVX2 但可执行 AVX2 指令，不支持 AVX512；AVX2 行仅用于正确性验证，不作为性能证据）

## 1. 固定语料与基准命令

固定语料由 `conformance_matrix_test.go` 的 `conformanceMatrixExpressions`（12 条 Block 规则）与
`conformanceMatrixCorpus`（确定性字节串，含尾部截断、非对齐起点与跨窗口长前缀）定义；
`perf_baseline_test.go` 的 `fixedBenchCorpus` 把同一语料确定性重复到约 64KiB 作为吞吐输入。
基准与一致性矩阵共用同一份规则和语料，避免“基准输入”和“正确性输入”长期分叉。

```text
go test -run '^$' -bench 'Benchmark(Scan|EngineFamilies|ResourceUsage|ProgramFindMatches|FindAll|BackendEqualByteMask|ScanRuleScales|FixedCorpusScan|QueuePushAll|FindIntoByteMask|InRangeMask)' -benchmem ./...
go test -run '^$' -bench 'Benchmark(ByteNFA|SelectEngineKind)' -benchmem ./internal/nfa/
go test -run '^$' -bench BenchmarkBackendEqualByteMask -benchmem -benchtime=100ms ./internal/simd
go test -run '^$' -bench 'BenchmarkNativeByteSetMask|BenchmarkByteSetMaskPrepared|BenchmarkNativeByteSetMask32|BenchmarkNativeByteSetMask64|BenchmarkWindowMask' -benchmem ./internal/simd/...
go test -run '^$' -bench 'BenchmarkForEachNFAStartFirstByte|BenchmarkMiracleWindowCandidates' -benchmem ./internal/nfa/ ./internal/rose/
```

AVX2 内核在 Rosetta 下需显式开启（仅用于正确性与指令级执行验证）：

```bash
SCANKIT_AVX2_TESTS=1 GOOS=darwin GOARCH=amd64 CGO_ENABLED=0 go test -count=1 -run 'AVX2' ./internal/simd/x86/
SCANKIT_AVX2_TESTS=1 GOOS=darwin GOARCH=amd64 CGO_ENABLED=0 go test -count=1 -run '^$' -bench BenchmarkNativeByteSetMask32 -benchmem ./internal/simd/x86/
```

x86 掩码基准需在 amd64 产物上运行：

```bash
GOOS=darwin GOARCH=amd64 CGO_ENABLED=0 go test -c ./internal/simd/x86 -o /tmp/x86simd.test
arch -x86_64 /tmp/x86simd.test -test.run '^$' -test.bench BenchmarkNativeMasks -test.benchmem
```

## 2. 代表性结果

| 基准 | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: |
| ScanRuleScales/rules_1 | 161,633 | 22,565 | 21 |
| ScanRuleScales/rules_10 | 183,754 | 33,058 | 8 |
| ScanRuleScales/rules_100 | 435,055 | 313,914 | 8 |
| ScanRuleScales/rules_1000 | 3,099,492 | 3,198,690 | 9 |
| FixedCorpusScan/generic | 96,093,969（0.68 MB/s） | 5,650,264 | 20,287 |
| FixedCorpusScan/host | 92,600,458（0.71 MB/s） | 5,651,578 | 20,287 |
| NFA ByteNFA | 3,891 | 8 | 1 |
| NFA LimEx | 10,513 | 64 | 1 |
| NFA Sheng | 16,341 | 64 | 1 |
| NFA McSheng | 9,735 | 64 | 1 |
| NFA Tamarama | 1,032 | 192 | 17 |
| NFA Shufti | 14,932 | 64 | 1 |
| NFA Truffle | 19,923 | 64 | 1 |
| NFA Vermicelli | 9,745 | 64 | 1 |
| NFA SelectEngineKind | 231,972 | 441,599 | 9,146 |
| NFA ResourceUsage Castle | 58,243 | 57,179 | 648 |
| NFA ResourceUsage LimEx | 325,580 | 2,033 | 7 |
| Rose ProgramFindMatches | 213～279 | 600 | 8 |
| Rose QueuePushAll/bulk | 440,460 | 299,080 | 18 |
| Rose QueuePushAll/sequential | 9,225,258 | 298,984 | 15 |
| HWLM FindAll | 86.8～98.0 | 24 | 2 |
| HWLM Teddy FindInto（多 lane / 单 lane） | 2,022～2,876（1,424～2,026 MB/s） / 31,940～33,116（124～128 MB/s） | 24 | 1 |
| HWLM Noodle FindInto（多 lane / 单 lane） | 2,181～2,769（1,479～1,878 MB/s） / 60,359～60,651（68 MB/s） | 24 | 1 |
| HWLM Teddy WindowMask（宿主原生 / 通用标量） | 848（2,416 MB/s） / 4,931（415 MB/s） | 0 | 0 |
| SIMD WindowMask 32B（NEON / 通用，lanes1） | 1,091（7,506 MB/s） / 4,979（1,645 MB/s） | 0 | 0 |
| SIMD WindowMask 32B（NEON / 通用，lanes4） | 3,004（2,727 MB/s） / 19,670（416 MB/s） | 0 | 0 |
| SIMD 32B 字节集合内核（NEON / SSE 半区拼接） | 1.78 / 3.55 | 0 | 0 |
| SIMD 32B 字节集合内核（AVX2，Rosetta 模拟，仅供参考） | 12.10 | 0 | 0 |
| SIMD WindowMask64 64B（NEON 拼接 / 通用标量，lanes1） | 1,816（9,024 MB/s） / 8,992（1,822 MB/s） | 0 | 0 |
| SIMD WindowMask64 64B（NEON 拼接 / 通用标量，lanes4） | 5,150（3,181 MB/s） / 41,224（397 MB/s） | 0 | 0 |
| SIMD 宽窗口内核同字节对照（NEON 64B 拼接 / 32B 内核，4KiB，宿主后端） | 573 / 819（lanes1）、901 / 1,243（lanes2） | 0 | 0 |
| SIMD 宽窗口内核同字节对照（通用标量 64B / 32B，4KiB） | 1,486 / 1,199（lanes1）、785 / 711（lanes2）、410 / 423（lanes4，MB/s） | 0 | 0 |
| SIMD EqualByteMask（通用） | 8.79 | 0 | 0 |
| SIMD EqualByteMask（主机分派） | 2.50 | 0 | 0 |
| SIMD EqualByteMask（NEON 原生 / 展开标量） | 1.32 / 9.55 | 0 | 0 |
| SIMD EqualMask（NEON 原生 / 展开标量） | 0.99 / 4.77 | 0 | 0 |
| SIMD InRangeMask（NEON 原生 / 展开标量） | 1.91 / 7.22 | 0 | 0 |
| SIMD ByteSetMask（NEON 原生 / 展开标量） | 1.68 / 7.30 | 0 | 0 |
| SIMD ByteSetMask（SSE PSHUFB / 展开标量，Rosetta amd64） | 2.52 / 14.50 | 0 | 0 |
| SIMD InRangeMask（SSE2 原生 / 展开标量，Rosetta amd64） | 2.00 / 6.00 | 0 | 0 |

`FixedCorpusScan` 的吞吐偏低来自规则集合同时包含文字、范围、锚定、重复与 NFA/DFA 确认路径，
且语料高度重复、命中密集，属于确认路径的最坏情况；该数字用于同机回归比较，不代表目标环境吞吐。

## 3. 回归治理

确定性指标由 `TestFixedCorpusDeterministicMetrics` 强制校验，不依赖机器性能：

- 命中集合：`1=1,2=2,3=15,4=1,5=1,6=1,7=1,8=2,9=1,10=1,11=1,12=11`。
- 执行后端与布局：
  `1=nfa/11/1/60,2=literal,3=nfa/11/2/68,4=nfa/3/10/22831,5=dfa/12/14096,6=literal,7=literal,8=nfa/10/3/33,9=nfa/11/1/100,10=nfa/11/1/66,11=literal,12=dfa/6/9096`
  （格式：`<路径>/<引擎类型>/<状态数>/<布局内存字节>`）。
- 分配上限：单次完整扫描 `<= 100` 次分配（当前 97），只允许下降不允许上升。

时间指标（`ns/op`、吞吐、`B/op`）只用于记录和同机对比，不写入断言，避免把某一台机器的性能当成绝对阈值。

## 4. 说明

本次单次采样命令输出作为当前可重复基线；多规则纯文字扫描继续消费候选切片，避免逐起点遍历和候选区间映射。
NFA 各引擎在固定 corpus 上报告的状态、边、布局内存与执行步数由 `TestFixedCorpusDeterministicMetrics` 一起固化。
跨后端一致性由 `TestScanConformanceAcrossBackendMatrix`、`TestScanConformanceAcrossBackendMatrixWindows`、
`TestScanFixturesAcrossBackendMatrix` 和 `TestBackendInRangeMaskCoversAllValues` 重复验证，保证原生路径与回退路径结果一致。
正式阈值需由部署环境另行定义。

## 5. 64 字节宽窗口接入的对照与边界

第 5 轮把 `Backend.WindowMask64`（64 字节宽窗口）接入 Teddy、Noodle、Rose 与 NFA 的候选枚举热路径，
并把手写尾零位循环换成 `math/bits.TrailingZeros32/64`。对照数据全部来自同一台 darwin/arm64（M5 Pro），
`-benchtime 4000x -count 3`：

| 基准 | 32 字节基线（改前） | 64 字节宽窗口（改后） | 变化 |
| --- | --- | --- | --- |
| Teddy `FindIntoByteMask/second_mask` | 3,485～4,207 ns | 1,759～2,020 ns | 约 2.0 倍 |
| Teddy `FindIntoByteMask/first_only` | 44,641～46,976 ns | 31,114～32,102 ns | 约 1.45 倍 |
| Noodle `FindIntoByteMask/second_mask` | 4,390～5,536 ns | 2,495～3,467 ns | 约 1.75 倍 |
| Noodle `FindIntoByteMask/first_only` | 76,588～77,996 ns | 57,050～57,782 ns | 约 1.34 倍 |
| NFA `BenchmarkForEachNFAStartFirstByte/window32` | 54,425～60,796 ns | 42,434～46,134 ns | 约 1.3 倍 |
| Rose `BenchmarkMiracleWindowCandidates` | 190,833～199,689 ns | 155,249～157,740 ns | 约 1.24 倍 |
| Rose `New`+`Normalize`+`FindMatches`（64KiB，候选器路径可达后） | 765,059 ns | 309,149 ns | 约 2.5 倍 |

重要的排查结论：宽窗口刚接入时 Teddy/Noodle 的稠密命中基准反而变慢（`first_only` 由约 45μs 升到约 66μs）。
CPU profile 显示瓶颈不在 `WindowMask64` 内核（同字节数下 64 字节内核比 32 字节内核快约 1.4 倍），
而是手写尾零位循环 `for v&1 == 0 { v >>= 1 }` 在稠密掩码场景下占 `FindInto` 约 23% CPU。
替换为 `math/bits.TrailingZeros32/64`（单个 `RBIT`/`TZCNT` 指令）后，四个热点全部快于改前的 32 字节基线。
该结论已写入任务清单 K-20 第四轮证据，避免后续再把手写位循环引入热路径。

512 位（AVX512BW/VBMI）内核在本机无法执行（Rosetta 的 AVX512 探针直接触发非法指令），
因此它们只有交叉编译与一次性运行时自检证据，不纳入性能基线。
