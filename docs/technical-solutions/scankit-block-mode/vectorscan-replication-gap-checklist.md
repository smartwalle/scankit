# Vectorscan 复刻差距清单

> 用途：明确当前纯 Go Block 实现与固定参考源码快照之间的差距。
>
> 参考快照：`.codex/vectorscan` commit `4724cff398f81818b28b373ff67c41b9ed95d317`。
>
> 本清单只做能力盘点，不改变项目已确认的范围约束。状态含义：**已实现**=已有主链路代码和验证；**部分实现**=存在可用路径但仍缺少源码级完整性；**未实现**=尚未具备等价能力；**排除**=明确不纳入本项目。

## 1. 编译器、公共契约和平台

| 编号 | 参考能力 | 参考源码范围 | 当前状态 | 差距/验收条件 |
| --- | --- | --- | --- | --- |
| C-01 | `CompileFlag` 全量位定义及冲突校验 | `src/hs_compile.h`、`src/compiler` | 部分实现 | 已对齐常用 Block flags；需逐位核对保留位、冲突组合、错误码和默认值 |
| C-02 | `ExpressionExtFlag`、宽度和距离类型 | `src/hs_compile.h` | 部分实现 | 已支持主要扩展字段；需覆盖所有无符号宽度、溢出和非法组合行为 |
| C-03 | `ExpressionInfo` 完整元数据 | `src/compiler` | 部分实现 | 已有最小/最大宽度、UTF、SOM 等字段；需补齐每个 compiler 属性和 bailout 原因 |
| C-04 | 编译错误模型 | `src/hs_common.h`、`src/compiler` | 部分实现 | Go 错误已分类；尚未保证错误码、表达式索引和消息分类与参考实现逐项一致 |
| C-05 | CPU 平台信息与调优选择 | `src/hs_platform.h`、`src/dispatcher.c` | 部分实现 | 已有 Go feature 探测/注册；尚未等价覆盖 tune 值、禁用特性和所有后端优先级 |
| C-06 | 数据库、scratch 版本和兼容性 | `src/database.*`、`src/scratch.*` | 部分实现 | 已有纯 Go 序列化与校验；尚未实现二进制 ABI、跨版本兼容和完整大小校验 |

## 2. 图、分析和优化基础设施

| 编号 | 参考能力 | 参考源码范围 | 当前状态 | 差距/验收条件 |
| --- | --- | --- | --- | --- |
| G-01 | NFAGraph 节点/边/容器语义 | `src/nfagraph` | 部分实现 | 基础图、闭包和校验已具备；需逐项覆盖所有特殊节点、属性和边排序语义 |
| G-02 | CharReach/字符集操作 | `src/util/charreach` | 部分实现 | 字节类掩码已实现；Unicode、大小写折叠和稀疏/密集表示尚非完全等价 |
| G-03 | SCC、dominator、循环分析 | `src/nfagraph`、`src/util` | 部分实现 | 已有图遍历；需补齐所有分析结果对编译选择和 bailout 的影响 |
| G-04 | Graph rewrite/optimization pass | `src/nfagraph`、`src/compiler` | 部分实现 | 已有有限重写和收敛门禁；未覆盖全部 simplify、reduce、split、reachability pass |
| G-05 | 规模、状态和内存 bailout | `src/compiler`、各 engine compiler | 部分实现 | Go 侧有资源门禁；阈值、触发顺序和 fallback 原因尚未逐项与源码快照对齐 |

## 3. NFA 引擎内部算法

| 编号 | 参考能力 | 参考源码范围 | 当前状态 | 差距/验收条件 |
| --- | --- | --- | --- | --- |
| N-01 | Castle 完整 castle tree、repeat、queue 和确认 | `src/nfa/castle.*` | 部分实现 | 有独立路径；尚未证明所有 castle tree 优化和异常报告语义等价 |
| N-02 | Gough 反向/正向确认和状态压缩 | `src/nfa/gough.*` | 部分实现 | 有反向确认；尚未覆盖全部压缩布局、特殊边界和源码级调度策略 |
| N-03 | LimEx 64 位 state、exceptional/shuffle 和 SIMD | `src/nfa/limex.*` | 部分实现 | 有位集合、异常状态、源掩码和预算闭环；尚未等价实现全部 shuffle、native 宏路径和每种 LimEx variant |
| N-04 | McClellan/Sheng/McSheng 状态布局 | `src/nfa/mcclellan.*`、`sheng.*` | 部分实现 | 已有稠密/稀疏 Go 布局；尚未覆盖源码中的压缩表、cache layout 和全部转换策略 |
| N-05 | Tamarama 范围分解和多桶调度 | `src/nfa/tamarama.*` | 部分实现 | 有范围合并、桶索引和闭包；尚未覆盖所有范围分解启发式和特殊状态布局 |
| N-06 | Vermicelli 前缀/后缀和确认策略 | `src/nfa/vermicelli.*` | 部分实现 | 有前缀候选状态机；尚未实现全部 vermicelli variant、后缀策略和源码级选择条件 |
| N-07 | Shufti/Truffle 转置半字节表 | `src/nfa/shufti.*`、`truffle.*` | 部分实现 | 有高低半字节掩码；尚未覆盖全部转置表构造、SIMD 指令变体和特殊字符集压缩 |
| N-08 | Repeat/MPV 专用编译和运行时 | `src/nfa/repeat.*`、`mpv.*` | 部分实现 | 固定重复和文字路径可用；尚未覆盖所有 repeat acceleration、报告和选择启发式 |
| N-09 | LBR 编译器、断言和运行时 | `src/nfa/lbr.*` | 部分实现 | 已支持可证明边界断言、后继表和失败回退；复杂 lookaround、全部 LBR 加速和源码级状态压缩仍有差距 |
| N-10 | NFA engine selection 全部启发式 | `src/compiler`、`src/nfa` | 部分实现 | 已按图形态和预算选择；尚未逐条复刻源码的优先级、cost model 和所有 bailout 分支 |

## 4. DFA、RDFA 和压缩

| 编号 | 参考能力 | 参考源码范围 | 当前状态 | 差距/验收条件 |
| --- | --- | --- | --- | --- |
| D-01 | Determinisation、epsilon closure、dead state | `src/dfa` | 部分实现 | 基础确定化可用；需覆盖所有报告状态、宽度限制和失败回退 |
| D-02 | Minimization、compression、sparse/dense table | `src/dfa` | 部分实现 | 有稠密/稀疏表；尚未等价实现源码压缩布局和成本选择 |
| D-03 | Reverse DFA/RDFA 边界确认 | `src/dfa`、`src/nfa` | 部分实现 | 有反向构造和确认；尚未证明全部边界、SOM 和报告传播一致 |

## 5. HWLM、Prefilter 和文字加速

| 编号 | 参考能力 | 参考源码范围 | 当前状态 | 差距/验收条件 |
| --- | --- | --- | --- | --- |
| H-01 | Literal analysis、literal set、角色选择 | `src/hwlm` | 部分实现 | 基础文字候选已接入；尚未覆盖所有 literal 分类、评分和拆分启发式 |
| H-02 | FDR matcher | `src/fdr` | 部分实现 | 有 Go 实现和候选确认；尚未逐项对齐全部桶布局、mask 变体和 SIMD 路径 |
| H-03 | Teddy matcher | `src/hwlm/teddy` | 部分实现 | 有多 lane 候选路径；尚未覆盖所有 lane 数、shuffle 和平台实现 |
| H-04 | Noodle matcher | `src/hwlm/noodle` | 部分实现 | 有 matcher 选择与回退；尚未覆盖全部 Noodle variant 和源码 cost model |
| H-05 | Long literal confirmation | `src/hwlm`、`src/rose` | 部分实现 | 候选后执行完整确认；尚未覆盖全部长文字拆分和确认调度优化 |

## 6. Rose、Miracle 和报告运行时

| 编号 | 参考能力 | 参考源码范围 | 当前状态 | 差距/验收条件 |
| --- | --- | --- | --- | --- |
| R-01 | Rose graph、role、alias、width | `src/rose` | 部分实现 | 角色图和文字转换可用；尚未覆盖全部 alias/width 推导和角色 cost 选择 |
| R-02 | Rose matcher conversion | `src/rose`、`src/compiler` | 部分实现 | 已支持文字、分支和有限重复转换；复杂图仍由 NFA/AST 确认 |
| R-03 | Scheduler queue、activation、transition | `src/rose` | 部分实现 | 单次调度、去重、排序和指令已实现；尚未等价覆盖全部优先级和队列压缩策略 |
| R-04 | Infix/outfix/catchup | `src/rose` | 部分实现 | 已有单次 Block 确认和区间路径；跨块 catchup 属于排除范围 |
| R-05 | Miracle 单/多角色加速 | `src/rose` | 部分实现 | 已有共享首字节候选桶和统一确认；尚未覆盖全部多角色加速图和源码级角色选择 |
| R-06 | Report/SOM/EOD/Quiet/SingleMatch | `src/rose`、`src/report` | 部分实现 | 主要语义已接入；尚未逐项完成与源码回调时序、取消和报告标志的等价验证 |

## 7. SIMD、CPU Dispatch 和原生后端

| 编号 | 参考能力 | 参考源码范围 | 当前状态 | 差距/验收条件 |
| --- | --- | --- | --- | --- |
| S-01 | SuperVector/向量基本操作 | `src/util/supervector` | 部分实现 | 通用 Go 向量契约已实现；尚未覆盖所有向量宽度和指令级语义 |
| S-02 | x86 SSE/SSE4 | `src/*/x86` | 部分实现 | 有纯 Go/部分原生入口和能力门禁；尚未覆盖全部 SSE 指令组合 |
| S-03 | x86 AVX2/AVX512/VBMI | `src/*/x86` | 部分实现 | AVX2 路径可用，部分高级能力复用安全回退；尚未实现完整 AVX512/VBMI 专用算法 |
| S-04 | ARM NEON/ASIMD | `src/*/arm` | 部分实现 | 有 NEON 路径和回退；尚未覆盖全部 ARM 特性组合 |
| S-05 | ARM SVE/SVE2 | `src/*/arm` | 部分实现 | 已纳入能力分派和安全回退；尚未实现可变向量长度的完整原生路径 |
| S-06 | 跨架构 dispatch 优先级和禁用项 | `src/dispatcher.c` | 部分实现 | 有线程安全注册表和快照；尚未完全复刻 tune、环境变量和运行时禁用语义 |
| S-07 | NFA/HWLM 全热路径原生接入 | 各 engine、`src/util` | 未实现 | 仍有通用 Go 热路径；需逐热点证明原生与标量结果、尾部和非对齐一致 |

## 8. 运行时 API、序列化和范围边界

| 编号 | 参考能力 | 参考源码范围 | 当前状态 | 差距/验收条件 |
| --- | --- | --- | --- | --- |
| X-01 | Block `hs_scan` 等价语义 | `src/runtime.c` | 部分实现 | `Scanner.Scan`/`ScanInto` 可用；尚未完成 ABI、scratch 生命周期和回调错误码级等价 |
| X-02 | Streaming `hs_scan_stream` | `src/runtime.c` | 排除 | 项目明确暂停并删除/不接入 Session 逻辑 |
| X-03 | Vectored `hs_scan_vector` | `src/runtime.c` | 排除 | 项目明确不处理 vectored 场景 |
| X-04 | Chimera、Power/VSX、Sidecar | 对应 engine/runtime | 排除 | 项目范围明确排除 |
| X-05 | 数据库/scratch 二进制 ABI | `src/database.*`、`src/scratch.*` | 未实现 | 当前为独立 Go 序列化，不承诺与参考二进制互读 |

## 9. 优化、性能和发布证据

| 编号 | 参考能力 | 参考源码范围 | 当前状态 | 差距/验收条件 |
| --- | --- | --- | --- | --- |
| O-01 | 全部 compiler optimization pass | `src/compiler`、`src/nfagraph` | 部分实现 | 已有安全 pass；未完成源码级 pass 一一映射和触发条件证明 |
| O-02 | 完整 engine cost model | 各 compiler | 未实现 | 当前主要按能力和固定资源门禁选择，未复刻全部代价模型 |
| O-03 | Scratch/工作区复用优化 | `src/scratch`、各 runtime | 部分实现 | Go 池化和调用级复用已接入；尚未达到源码布局和分配行为等价 |
| O-04 | 性能阈值与回归治理 | `src/unit`、`src/benchmarks` | 部分实现 | 有 Go benchmark 指标；参考源码没有统一绝对阈值，尚未完成同 corpus 长期基线 |
| O-05 | 全量源码级 conformance | `src/unit`、`src/tools` | 未实现 | 已有 Go corpus，但尚未覆盖参考源码全部 fixture、错误和平台矩阵 |
| O-06 | 发布审计和 API/EXCLUDE 闭环 | `src/tools`、发布脚本 | 部分实现 | 有文档和 Go 门禁；尚未达到完整源码构建、ABI 和平台发布矩阵 |

## 10. 结论

当前项目可以表述为：**已完成单次 Block 扫描范围内的独立纯 Go 兼容实现**；不能表述为：**已完成 Vectorscan 全部内部算法、全部原生 SIMD/CPU 后端和所有优化细节的等价复刻**。

若目标升级为“完整复刻”，必须优先处理 `N-01~N-10`、`S-01~S-07`、`O-01~O-06` 和 `C-01~C-06`，并重新定义是否解除 `X-02~X-05` 的排除约束；在这些条件未满足前，只能按“Block 子集兼容实现”验收。
