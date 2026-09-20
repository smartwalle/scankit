# Vectorscan 兼容实现深化计划

> 对应差距清单：[vectorscan-replication-gap-checklist.md](./vectorscan-replication-gap-checklist.md)
>
> 可执行任务台账：[vectorscan-replication-task-list.md](./vectorscan-replication-task-list.md)
>
> 参考源码快照：`.codex/vectorscan` commit `4724cff398f81818b28b373ff67c41b9ed95d317`
>
> 目标：在不破坏现有 Block 模式约束的前提下，持续收敛编译器、引擎、SIMD、运行时和优化语义；只有完成本计划并通过发布门禁，才能声称达到“源码级兼容”。

## 1. 范围和前置决策

### 1.1 当前默认范围

1. 只实现单次 `Block` 扫描，保持 `Engine`、`Scanner` 公开 API 冻结。
2. `.codex/vectorscan` 仅供只读对照，不编译、运行、包装或复制其中代码。
3. 流式、Vectored、Chimera、Power/VSX、历史 Sidecar、Oracle、wrapper、Docker 和 CGo 继续排除。
4. 新增实现使用纯 Go；原生汇编或架构代码只有在语义可验证且具备安全回退时才允许接入。
5. 不实现报告、统计、排行和看板业务。

### 1.2 完整复刻所需的额外决策

若要从“Block 兼容实现”升级为“完整复刻”，必须先明确是否解除差距清单中的 `X-02~X-05` 排除项，并冻结二进制 ABI、平台支持矩阵和错误码兼容目标。未解除前，相关任务只能保持“排除”，不得虚报为完成。

## 2. 交付原则

- 每个阶段必须包含编译/运行时主路径、边界和非法输入处理、资源限制、安全回退及可执行验证。
- 不以单个方法、字段或测试桩作为任务完成依据；每个任务至少形成一个可独立验收的功能闭环。
- 专用后端失败时必须回退到已验证的通用确认路径，不得丢失匹配、报告或错误信息。
- 所有结果语义必须统一：ID、From/To、SOM、EOD、排序、去重、Quiet、SingleMatch、offset/length。
- 性能只记录可重复的 `ns/op`、`B/op`、`allocs/op`、状态数、步骤数和降级行为；不虚构参考实现不存在的绝对阈值。

## 3. 状态与执行粒度

状态只允许使用：`已完成`、`进行中`、`未开始`、`阻塞`。每个任务必须完成代码接入、边界/错误处理、资源限制和验证证据后才能标记为“已完成”。单个方法、字段或测试用例不得单独计为任务。

任务执行固定采用以下闭环：

`确认参考语义 → 编写失败 fixture → 实现编译/运行主路径 → 接入选择器 → 完善边界与回退 → 运行相关 Go 测试 → 竞态/静态检查 → 更新状态和证据`

每轮开发至少完成一个完整任务；任务未达到验收条件时保持“进行中”，不得跨阶段虚报完成。具体领取粒度、状态和完成证据以任务台账为准；本文件中的 A~J 任务是阶段级交付项，不直接替代台账任务。

## 4. 阶段总览

| 阶段 | 主题 | 主要交付 | 依赖 | 当前状态 | 完成门禁 |
| --- | --- | --- | --- | --- |
| A | 契约与错误模型 | flags、扩展字段、ExpressionInfo、错误和平台契约 | 无 | 已完成 | 契约快照与非法输入矩阵通过 |
| B | 图分析与优化基础 | CharReach、SCC、dominator、rewrite、bailout | A | 已完成 | 图属性驱动选择且不改变结果 |
| C | NFA 独立算法深化 | 全部 NFA engine 的状态布局、运行时和 cost model | A、B | 已完成 | 每类引擎独立路径与安全回退 |
| D | DFA/RDFA 与压缩 | 确定化、最小化、压缩、反向确认 | B、C | 已完成 | DFA/RDFA 与 NFA 结果一致 |
| E | HWLM 与文字加速 | Literal、FDR、Teddy、Noodle、长文字确认 | A、B | 已完成 | 候选只剪枝，确认结果不变 |
| F | Rose 复杂运行时 | matcher、scheduler、Miracle、infix/outfix、报告时序 | C、E | 进行中 | Rose 与 Scanner/NFA 一致 |
| G | Fuzzy/NG/断言闭环 | 非 literal Fuzzy、Lookaround、SOM/LBR 传播 | B、C、F | 已完成 | 复杂结构安全确认，无误报漏报 |
| H | 原生 SIMD/Dispatch | x86、ARM、热路径和跨架构回退 | A、C、E | 未开始 | 原生/通用结果一致 |
| I | 运行时/序列化兼容 | Block API、scratch、数据库、版本和错误 | A、C、D、F | 未开始 | 版本、损坏数据和回退矩阵通过 |
| J | 性能与发布审计 | 全量 conformance、基准、API/EXCLUDE、发布清单 | A~I | 未开始 | 所有 MUST 有代码与证据 |

## 5. 实施任务清单

### 阶段 A：契约与错误模型

| 编号 | 实质性任务 | 代码范围 | 依赖 | 验收证据 |
| --- | --- | --- | --- | --- |
| A-01 | 建立 flags/扩展字段逐位映射和冲突矩阵 | `compile.go`、`internal/compiler` | 无 | 每个位值、保留位、冲突组合和默认值均有 Go fixture |
| A-02 | 完成 ExpressionInfo 属性推导闭环 | `internal/compiler/expression.go`、Parser | A-01 | 宽度、UTF/UCP、SOM、Prefilter、LBR、Stateful 等属性与图能力一致 |
| A-03 | 统一 CompileError/资源错误分类 | `compile.go`、`internal/nfa/resource.go` | A-01 | 非法表达式、超限、不可转换和平台不足均返回稳定分类及索引 |
| A-04 | 固化 CPU feature/tune 能力快照 | `internal/dispatch` | A-01 | 未知架构、禁用能力和能力组合选择稳定且可回退 |

### 阶段 B：图分析与优化基础

| 编号 | 实质性任务 | 代码范围 | 依赖 | 验收证据 |
| --- | --- | --- | --- | --- |
| B-01 | 完善 CharReach/字符类规范化和大小写处理 | `internal/nfagraph`、`internal/util` | A-02 | 字节、大小写、Unicode 禁止下沉和稀疏/密集表示一致 |
| B-02 | 补齐 SCC、循环、支配关系和可达性分析 | `internal/graph`、`internal/nfagraph` | B-01 | 分析结果可驱动引擎选择，循环图不误判固定宽度 |
| B-03 | 建立 rewrite/normalize 多轮收敛管线 | `internal/nfagraph`、`internal/compiler` | B-02 | 每轮前后校验、最大轮次、失败回退和图等价性可验证 |
| B-04 | 统一状态、边、内存 bailout 顺序 | `internal/compiler`、`internal/nfa` | B-02、B-03 | 同一图在不同预算下选择稳定，超限不产生半成品布局 |

### 阶段 C：NFA 独立算法深化

| 编号 | 实质性任务 | 代码范围 | 依赖 | 验收证据 |
| --- | --- | --- | --- | --- |
| C-01 | Castle/Gough 状态树、反向确认和循环压缩 | `internal/nfa/castle.go`、`engines.go` | B-04 | 复杂分支、多接受、循环、边界和预算与通用路径一致 |
| C-02 | LimEx 位集合 variant、exceptional/shuffle 和 64 位热循环 | `internal/nfa/engines.go` | C-01 | 多种状态组合、死状态、源掩码、步骤/内存限制和回退矩阵通过 |
| C-03 | Sheng/McSheng 压缩表、稠密/稀疏 cost model | `internal/nfa/engines.go` | C-02 | 表布局、缓存友好路径、复杂分支和损坏表校验通过 |
| C-04 | Tamarama/Vermicelli 范围分解、前后缀和候选确认 | `internal/nfa/engines.go` | C-01 | 范围桶、前缀失败、长输入和结果限制保持一致 |
| C-05 | Shufti/Truffle 完整转置半字节布局 | `internal/nfa/engines.go` | C-02、H-01 | 高低半字节、空/尾部/非对齐输入与标量一致 |
| C-06 | Repeat/MPV 专用转换和加速选择 | `internal/repeat`、`internal/nfa` | B-04 | 固定/可变重复、文字分支、报告和预算闭环 |
| C-07 | LBR 断言状态、NG 转换和确认失败回退 | `internal/nfa`、`internal/compiler` | C-01、G-01 | 可证明断言进入 LBR，复杂结构安全回退且不吞结果 |
| C-08 | NFA engine selection 完整 cost model | `internal/compiler`、`internal/nfa` | C-01~C-07 | 选择优先级、bailout 原因和预算变化可追踪 |

### 阶段 D：DFA/RDFA 与压缩

| 编号 | 实质性任务 | 代码范围 | 依赖 | 验收证据 |
| --- | --- | --- | --- | --- |
| D-01 | 完成 determinisation、闭包和报告状态传播 | `internal/dfa` | B-03、C-08 | NFA/DFA 在空匹配、重叠、SOM/EOD 上一致 |
| D-02 | 完成最小化、稠密/稀疏压缩和状态上限 | `internal/dfa` | D-01 | 压缩前后状态语义一致，超限可预测回退 |
| D-03 | 完成 RDFA 反向构造和前后向确认 | `internal/dfa` | D-01、C-07 | 反向边界、长度、区间、结果限制与正向一致 |

### 阶段 E：HWLM 与文字加速

| 编号 | 实质性任务 | 代码范围 | 依赖 | 验收证据 |
| --- | --- | --- | --- | --- |
| E-01 | Literal 分级、集合拆分和角色评分 | `internal/hwlm` | A、B | 多规则、重复 literal、大小写和长文字选择稳定 |
| E-02 | FDR 完整桶布局和候选确认 | `internal/fdr` | E-01、H-01 | 多文字、重叠、尾部、NUL 与标量一致 |
| E-03 | Teddy 全 lane/mask/shuffle 路径 | `internal/hwlm/teddy` | E-01、H-01 | lane 数变化、非对齐、空输入和回退一致 |
| E-04 | Noodle 变体选择和长文字拆分 | `internal/hwlm/noodle` | E-01 | 选择 cost、候选失败和长确认不漏报 |
| E-05 | 统一 Prefilter/confirmation 契约 | `internal/prefilter`、`scanner.go` | E-02~E-04 | 候选只减少起点，最终 AST/NFA 确认结果完全一致 |

### 阶段 F：Rose 复杂运行时

| 编号 | 实质性任务 | 代码范围 | 依赖 | 验收证据 |
| --- | --- | --- | --- | --- |
| F-01 | Rose graph、role、alias、width 和转换条件 | `internal/rose`、`internal/compiler` | B、E-01 | 角色属性与原规则宽度、边界和报告 ID 一致 |
| F-02 | matcher 多角色、复杂依赖和候选调度 | `internal/rose` | F-01、E-02 | 多角色共享候选、优先级、重叠和失败回退一致 |
| F-03 | Scheduler queue/activation/transition 压缩 | `internal/rose` | F-02 | 状态去重、队列上限、步骤预算和指令链路稳定 |
| F-04 | Miracle 多角色复杂加速和确认时序 | `internal/rose/miracle.go` | F-02、E-05 | 候选桶、infix/outfix、确认失败和排序去重一致 |
| F-05 | Rose Report/SOM/EOD/Quiet/SingleMatch 闭环 | `internal/rose`、`internal/report` | F-03、G-02 | 与 Scanner/NFA 的报告回调时序和标志完全一致 |

### 阶段 G：Fuzzy、NG 和断言

| 编号 | 实质性任务 | 代码范围 | 依赖 | 验收证据 |
| --- | --- | --- | --- | --- |
| G-01 | 非 literal Fuzzy 图转换（字符类、通配、有限分支/重复） | `scanner.go`、`internal/fuzzy` | B、C | Hamming/Edit 结果、宽度变化和距离上限一致 |
| G-02 | NG/Lookaround/Boundary 安全转换与确认 | `internal/compiler`、`internal/nfa` | C、D | 可证明结构走专用路径，不可证明结构完整 AST 确认 |
| G-03 | SOM/LBR 结果传播和候选确认 | `internal/som`、`internal/nfa`、`scanner.go` | C、F、G-02 | 最左起点、EOD、边界和负向确认不漏报 |
| G-04 | UTF-8/UCP 与 byte-only 加速隔离 | Parser、Scanner、各引擎 | G-01~G-03 | Unicode 输入不误入字节专用布局，结果与完整路径一致 |

### 阶段 H：原生 SIMD 与 Dispatch

| 编号 | 实质性任务 | 代码范围 | 依赖 | 状态 | 验收证据 |
| --- | --- | --- | --- | --- | --- |
| H-01 | SuperVector 契约和通用实现完备化 | `internal/simd` | A | 已完成 | 所有向量操作、掩码、尾部和空输入有标量基线 |
| H-02 | x86 SSE/SSE4/AVX2 原生路径 | `internal/simd/x86` | H-01 | 已完成 | 能力门禁、非对齐、尾部和结果一致 |
| H-03 | x86 AVX512/VBMI 专用路径 | `internal/simd/x86`、`internal/dispatch` | H-02 | 已完成 | 仅在真实能力存在时启用，否则稳定回退 |
| H-04 | ARM NEON/ASIMD 原生路径 | `internal/simd/arm64` | H-01 | 已完成 | arm64 编译、运行和回退矩阵通过 |
| H-05 | ARM SVE/SVE2 可变宽度处理 | `internal/simd/arm64`、`internal/dispatch` | H-04 | 已完成 | 不同向量长度、尾部和能力不足均安全 |
| H-06 | NFA/HWLM 全热路径原生接入 | `internal/nfa`、`internal/hwlm` | H-02~H-05、C、E | 已完成 | 原生与通用路径在完整 corpus 上一致，失败自动降级 |

### 阶段 I：运行时、序列化和兼容

| 编号 | 实质性任务 | 代码范围 | 依赖 | 验收证据 |
| --- | --- | --- | --- | --- |
| I-01 | Block 扫描生命周期、scratch 和回调错误闭环 | `scanner.go`、`internal/scratch` | C、F | 输入不被修改、scratch 可复用、回调停止和错误可见 |
| I-02 | Program/Database 序列化版本和损坏恢复 | `internal/nfa`、`internal/dfa`、`internal/rose` | C、D、F | 版本不兼容、截断、字段篡改均拒绝或安全回退 |
| I-03 | API/错误/资源兼容矩阵 | 根 API、`internal/contract` | A、I-01 | 公开签名冻结，错误和 limits 行为可追踪 |
| I-04 | 评估是否解除流式/Vectored/ABI 排除项 | 文档与发布配置 | I-01~I-03 | 有明确决策前相关代码保持排除，不得隐式实现 |

### 阶段 J：性能、Conformance 和发布审计

| 编号 | 实质性任务 | 代码范围 | 依赖 | 验收证据 |
| --- | --- | --- | --- | --- |
| J-01 | 建立全引擎统一 conformance corpus | 各 `*_test.go`、`testdata` | A~I | 多规则、重叠、空匹配、边界、Unicode、Fuzzy、Rose、组合一致 |
| J-02 | 建立平台/原生/通用交叉验证矩阵 | `internal/simd`、`internal/dispatch` | H、J-01 | amd64/arm64、尾部、非对齐和能力缺失结果一致 |
| J-03 | 建立性能和资源基线 | benchmark、`internal/nfa/resource.go` | C、E、H、I | 记录吞吐、P95、分配、状态/步骤/内存和降级行为 |
| J-04 | 完成 API、MUST、EXCLUDE 和源码映射审计 | `docs/technical-solutions` | I、J-01~J-03 | API diff=0、EXCLUDE 无误实现、引用 commit 固定 |
| J-05 | 发布门禁和回滚 runbook | `docs/technical-solutions/scankit-block-mode` | J-04 | 全量 Go 测试、竞态、静态检查、差异检查和基准记录完整 |

## 6. 阶段推进顺序

1. 先完成阶段 A、B，冻结契约和图分析基础。
2. 按 C-01→C-08 完成 NFA 专用算法，再进入 D 阶段。
3. 并行推进 E 阶段，但 E-05 必须等待至少一类专用 NFA 确认路径稳定。
4. 完成 F、G 后，统一处理 Rose、NG、Fuzzy、SOM 和断言交叉语义。
5. H 阶段最后接入原生热路径；任何平台失败均保留通用实现。
6. I 阶段完成运行时和序列化兼容后，执行 J 阶段全量门禁。

## 7. 完成判定

只有同时满足以下条件，才能将本计划标记为“完整复刻完成”：

- A~J 所有未排除任务均有主路径代码、边界处理、资源限制和验证证据。
- 至少三类 NFA 具备与通用路径不同的状态布局和执行循环，且 cost model 可追踪。
- Rose、Fuzzy、NG、DFA/RDFA、HWLM 和 SIMD 在统一 corpus 上结果一致。
- 原生 SIMD/CPU 后端在目标架构真实启用时通过验证，能力不足时安全回退。
- 数据库、scratch、错误、序列化和公开 API 的兼容目标已明确并通过矩阵验证。
- `go test ./...`、`go test -race ./...`、`go vet ./...`、`git diff --check` 及基准门禁全部通过。
- 若 X-02~X-05 仍未解除排除，最终结论必须写成“Block 模式兼容实现”，不得写成完整复刻。
