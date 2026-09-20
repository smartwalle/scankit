# Block 模式基准基线

采集日期：2026-09-01  
环境：darwin/arm64，Apple M5 Pro  
命令：

```text
go test -run '^$' -bench 'Benchmark(Scan|Rose|NFA)' -benchmem ./...
```

## 代表性结果

| 基准 | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: |
| Scanner 1 规则 | 15,896 | 24,360 | 1,185 |
| Scanner 10 规则 | 53,035 | 182,608 | 98 |
| Scanner 100 规则 | 533,997 | 1,827,721 | 135 |
| Scanner 1000 规则 | 5,520,402 | 24,971,678 | 629 |
| Edit 字符类确认 | 34.10 | 16 | 1 |
| Fuzzy EditDistance | 84.78 | 160 | 2 |
| NFA LimEx | 332.4 | 352 | 33 |
| NFA Sheng | 377.0 | 400 | 29 |
| NFA McSheng | 523.0 | 400 | 29 |
| NFA Tamarama | 377.4 | 400 | 29 |
| NFA Shufti | 382.6 | 400 | 29 |
| NFA Truffle | 382.2 | 400 | 29 |
| NFA Vermicelli | 379.9 | 400 | 29 |
| Rose FindMatches | 450.4 | 888 | 18 |
| HWLM FindAll | 63.00 | 24 | 2 |
| SIMD EqualByteMask（通用） | 8.84 | 0 | 0 |
| SIMD EqualByteMask（主机分派） | 14.47 | 32 | 2 |

本次单次采样命令输出作为当前可重复基线；多规则纯文字扫描改为直接消费候选切片，避免逐起点遍历和候选区间映射，重新采样。数据用于回归比较，不代表达成性能阈值。正式阈值需由部署环境另行定义。

SIMD 掩码基准命令：

```text
go test ./internal/simd -run '^$' -bench BenchmarkBackendEqualByteMask -benchmem -benchtime=100ms
```
