# Block 模式基准基线

采集日期：2026-09-20
环境：darwin/arm64，Apple M5 Pro  
命令：

```text
go test -run '^$' -bench 'Benchmark(Scan|EngineFamilies|ResourceUsage|ProgramFindMatches|FindAll|BackendEqualByteMask|ScanRuleScales)' -benchmem ./...
```

## 代表性结果

| 基准 | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: |
| ScanRuleScales/rules_1 | 200,292 | 25,450 | 25 |
| ScanRuleScales/rules_10 | 216,458 | 54,816 | 14 |
| ScanRuleScales/rules_100 | 516,722 | 566,826 | 17 |
| ScanRuleScales/rules_1000 | 3,773,264 | 6,523,093 | 21 |
| NFA ByteNFA | 3,955 | 8 | 1 |
| NFA LimEx | 10,562 | 64 | 1 |
| NFA Sheng | 16,666 | 64 | 1 |
| NFA McSheng | 9,854 | 64 | 1 |
| NFA Tamarama | 1,084 | 192 | 17 |
| NFA Shufti | 13,569 | 64 | 1 |
| NFA Truffle | 18,954 | 64 | 1 |
| NFA Vermicelli | 9,802 | 64 | 1 |
| NFA SelectEngineKind | 229,794 | 441,597 | 9,146 |
| NFA ResourceUsage Castle | 58,391 | 57,180 | 648 |
| NFA ResourceUsage LimEx | 324,590 | 2,033 | 7 |
| Rose ProgramFindMatches | 11,136 | 79,704 | 44 |
| HWLM FindAll | 67.26 | 88 | 6 |
| SIMD EqualByteMask（通用） | 8.53 | 0 | 0 |
| SIMD EqualByteMask（主机分派） | 14.32 | 32 | 2 |

本次单次采样命令输出作为当前可重复基线；多规则纯文字扫描继续消费候选切片，避免逐起点遍历和候选区间映射。NFA 各引擎在固定 corpus 上报告的状态、边、布局内存与执行步数与最新选择诊断一致。数据用于回归比较，不代表达成性能阈值。正式阈值需由部署环境另行定义。

SIMD 掩码基准命令：

```text
go test ./internal/simd -run '^$' -bench BenchmarkBackendEqualByteMask -benchmem -benchtime=100ms
```
