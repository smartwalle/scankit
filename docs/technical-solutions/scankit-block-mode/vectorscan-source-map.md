# Vectorscan 源码参考索引

本索引只用于阅读和实现对照。严禁在 scankit 构建、测试或运行时编译、链接、执行 `.codex/vectorscan`，也不通过 Go/CGo 包装其代码。

参考快照：`4724cff398f81818b28b373ff67c41b9ed95d317`

| scankit 实现域 | 参考目录 | 阅读重点 |
| --- | --- | --- |
| Parser / Compiler | `.codex/vectorscan/src/parser`、`src/compiler` | 语法、flags、ExpressionInfo、错误和 engine selection |
| NFAGraph / NG | `.codex/vectorscan/src/nfagraph` | 图节点、分析、rewrite、limits |
| Graph / util | `.codex/vectorscan/src/util` | 容器、bit、遍历、内存和通用工具 |
| HWLM / FDR / Teddy / Noodle | `.codex/vectorscan/src/hwlm`、`src/fdr` | literal 管理、加速、候选和 confirmation |
| NFA engines | `.codex/vectorscan/src/nfa` | Castle、Gough、LimEx、McClellan、Sheng、McSheng、Tamarama、Vermicelli、Shufti、Truffle、Repeat、MPV、LBR |
| Rose | `.codex/vectorscan/src/rose` | graph、matcher、scheduler、queue、catchup、miracle、infix/outfix、report |
| SmallWrite | `.codex/vectorscan/src/smallwrite` | eligibility、program、runtime、dump |
| SOM | `.codex/vectorscan/src/som` | slot、tracking、propagation、report |
| SIMD / SuperVector | `.codex/vectorscan/src/util/supervector`、`src/*/x86`、`src/*/arm`、`simde` | 向量操作、平台分派、tail 和 fallback |
| Public contract | `.codex/vectorscan/src/hs_compile.h`、`src/hs_runtime.h`、`src/database.h`、`src/scratch.h` | flags、错误、数据库、scratch、Block API 语义 |

## 使用规则

1. 先阅读对应源码和注释，再在 Go 中独立实现；不得复制可执行 C/C++ 代码或引入链接依赖。
2. 仅实现主方案规定的 Block 能力；源码中存在的 Streaming、Vectored、Chimera、Power/VSX、历史 Sidecar 仍不在范围内。
3. 每个 Go fixture 必须注明参考源码路径和 commit；测试命令只能是 Go 命令。
