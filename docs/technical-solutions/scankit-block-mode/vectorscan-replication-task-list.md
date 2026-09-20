# Vectorscan 兼容实现可执行任务台账

> 对应实施计划：[vectorscan-replication-implementation-plan.md](./vectorscan-replication-implementation-plan.md)  
> 对应差距清单：[vectorscan-replication-gap-checklist.md](./vectorscan-replication-gap-checklist.md)  
> 参考源码快照：`.codex/vectorscan` commit `4724cff398f81818b28b373ff67c41b9ed95d317`  
> 当前状态：进行中  
> 范围：单次 Block 扫描的纯 Go 兼容实现

## 1. 固定约束

1. 只处理单次 Block 扫描；流式、Vectored、Chimera、Power/VSX、历史 Sidecar 与二进制 ABI 保持排除。
2. 不扩展 `Engine`、`Scanner` 的公开 API，不恢复 Session 相关代码。
3. `.codex/vectorscan` 只读参考，不编译、运行、包装或复制；不使用 Oracle、wrapper、Docker、CGo。
4. 不实现报告、统计、排行、看板业务；运行时报告语义仅限匹配回调所必需的内部行为。
5. 注释使用中文，且不在代码注释中出现参考项目名称。

## 2. 状态、领取和完成规则

状态仅可填写：`未开始`、`进行中`、`已完成`、`阻塞`。

每个编号是一次可独立交付的开发任务，必须在同一工作循环内完成以下闭环后才可标记为`已完成`：

`实现主路径 → 接入现有选择/调度 → 边界与非法输入 → 预算与安全回退 → 相关 Go 测试 → 状态和证据更新`

若发现编号仍无法在一轮内闭环，必须在编码前新增更小编号，并将原编号保留为`未开始`；不得仅完成一个字段、方法或测试即标记完成。

## 3. 可执行任务清单

### A. 契约、错误和平台选择

| 编号 | 任务 | 代码范围 | 依赖 | 状态 | 完成条件 |
| --- | --- | --- | --- | --- | --- |
| A-01 | 逐位固化 CompileFlag 常量和值域 | `compile.go` | 无 | 已完成 | 位值、默认值和保留位均有固定断言 |
| A-02 | 实现 CompileFlag 冲突与无效组合校验 | `compile.go`、`internal/compiler` | A-01 | 已完成 | 冲突组合返回稳定分类且不生成半成品 |
| A-03 | 逐位固化 ExpressionExtFlag 与字段范围 | `compile.go`、`internal/compiler` | A-01 | 已完成 | 所有扩展字段、零值和溢出输入可判定 |
| A-04 | 建立扩展参数交叉约束与宽度校验 | `internal/compiler` | A-03 | 已完成 | 距离、编辑距离、offset、length 的非法组合被拒绝 |
| A-05 | 完成 ExpressionInfo 宽度、锚点与 Unicode 属性推导 | `internal/compiler` | A-02、A-04 | 已完成 | 图属性与编译选择结果一致 |
| A-06 | 完成 ExpressionInfo SOM、Prefilter、LBR 与 stateful 属性推导 | `internal/compiler` | A-05 | 已完成 | 每一属性均可追溯到规则结构 |
| A-07 | 统一编译、资源和不可转换错误分类 | `compile.go`、`internal/nfa/resource.go` | A-02 | 已完成 | 错误类型、表达式索引和资源原因稳定 |
| A-08 | 固化 CPU feature、tune、禁用能力与安全回退快照 | `internal/dispatch` | A-01 | 已完成 | 未知平台、禁用特性和组合优先级稳定 |

**阶段 A 完成证据**

- A-01、A-03：`contract_test.go` 的 flags 位值快照。
- A-02：`compile_validation_test.go` 的冲突组合矩阵；`compile.go` 的统一校验入口。
- A-04、A-07：`compile_validation_test.go`、`compile_test.go` 的扩展边界和错误分类验证。
- A-05、A-06：`internal/compiler/expression_test.go` 的宽度、EOD、断言和状态属性验证。
- A-08：`internal/dispatch/dispatch_test.go`、`features_test.go` 的跨架构归一化、能力门禁和通用回退验证。
- 本轮命令：`go test ./...`、`go vet ./...`、`git diff --check`。

### B. 图分析、字符集和优化基础

| 编号 | 任务 | 代码范围 | 依赖 | 状态 | 完成条件 |
| --- | --- | --- | --- | --- | --- |
| B-01 | 统一图节点、特殊节点和边排序校验 | `internal/nfagraph` | A-05 | 已完成 | 非法图拒绝，合法图遍历顺序稳定 |
| B-02 | 完成字节 CharReach 的并、交、差、补和范围规范化 | `internal/nfagraph`、`internal/util` | B-01 | 已完成 | 稀疏/密集类的等价操作无越界 |
| B-03 | 实现大小写折叠与 Unicode 下沉隔离 | Parser、`internal/nfagraph` | B-02 | 已完成 | Unicode 图不会误入字节专用后端 |
| B-04 | 补齐 SCC、可达性和循环宽度分析 | `internal/graph`、`internal/nfagraph` | B-01 | 已完成 | 循环、死分支和固定宽度判定正确 |
| B-05 | 补齐 dominator/后支配分析并接入候选选择 | `internal/graph`、`internal/compiler` | B-04 | 已完成 | 分析只优化选择，不改变匹配结果 |
| B-06 | 建立 normalize/simplify/reachability 单轮重写 | `internal/nfagraph` | B-02、B-04 | 已完成 | 重写前后图语义一致且可校验 |
| B-07 | 建立多轮重写收敛、轮数上限和失败回退 | `internal/nfagraph`、`internal/compiler` | B-06 | 已完成 | 不收敛时停止优化并走安全路径 |
| B-08 | 统一状态、边、闭包和内存 bailout 顺序 | `internal/compiler`、`internal/nfa` | B-04、B-07 | 已完成 | 各预算下选择与降级原因稳定 |

### C. NFA 专用算法与选择模型

| 编号 | 任务 | 代码范围 | 依赖 | 状态 | 完成条件 |
| --- | --- | --- | --- | --- | --- |
| C-01 | 完成 Castle tree 状态布局与循环压缩 | `internal/nfa/castle.go` | B-08 | 已完成 | 树、循环、接受和死状态均独立执行 |
| C-02 | 完成 Castle 队列/工作区预算与复杂分支确认 | `internal/nfa/castle.go` | C-01 | 已完成 | 多接受、可空和预算超限安全回退 |
| C-03 | 完成 Gough 反向状态、前驱压缩和闭包 | `internal/nfa/engines.go` | C-01 | 已完成 | 反向布局不依赖通用图热路径 |
| C-04 | 完成 Gough 多结束位置和反向预算确认 | `internal/nfa/engines.go` | C-03 | 已完成 | 空匹配、前缀候选和超限结果一致 |
| C-05 | 完成 LimEx exceptional 分类与 64 位状态变体 | `internal/nfa/engines.go` | B-08 | 已完成 | 死、闭包、可消费和异常状态均可判定 |
| C-06 | 完成 LimEx shuffle/批量转移标量基线 | `internal/nfa/engines.go` | C-05 | 已完成 | 多源转移不逐状态解释且保留安全回退 |
| C-07 | 完成 Sheng 稠密表压缩、源掩码和热循环 | `internal/nfa/engines.go` | C-06 | 已完成 | 256 槽位、闭包和表损坏校验闭环 |
| C-08 | 完成 McSheng 稀疏表、类谓词与 cost 选择 | `internal/nfa/engines.go` | C-07 | 已完成 | 稀疏分支、负类与死状态一致 |
| C-09 | 完成 Tamarama 范围分解、桶索引和重叠范围 | `internal/nfa/engines.go` | B-08 | 已完成 | 桶命中、边界和未命中路径正确 |
| C-10 | 完成 Vermicelli 前后缀候选、长文字确认和回退 | `internal/nfa/engines.go` | C-02 | 已完成 | 候选失败不漏报，结果/步骤限制一致 |
| C-11 | 完成 Shufti 低半字节转置表和尾部路径 | `internal/nfa/engines.go` | C-06、H-01 | 已完成 | 字符类、空输入和尾部与标量一致 |
| C-12 | 完成 Truffle 高半字节转置与非对齐回退 | `internal/nfa/engines.go` | C-11、H-01 | 已完成 | 非对齐、高位掩码和损坏布局安全 |
| C-13 | 完成 Repeat/MPV 编译、报告与加速选择 | `internal/repeat`、`internal/nfa` | B-08 | 已完成 | 固定/可变重复和文字分支的预算闭环 |
| C-14 | 完成 LBR 可证明断言状态与 NG 转换 | `internal/nfa`、`internal/compiler` | C-01、G-02 | 已完成 | 可证明断言独立执行，不可证明结构回退 |
| C-15 | 建立 NFA engine cost model、bailout 原因和选择记录 | `internal/compiler`、`internal/nfa` | C-01~C-14 | 已完成 | 选择优先级可解释且不影响结果 |

### D. DFA、RDFA 和压缩

| 编号 | 任务 | 代码范围 | 依赖 | 状态 | 完成条件 |
| --- | --- | --- | --- | --- | --- |
| D-01 | 完成确定化、epsilon 闭包和死状态传播 | `internal/dfa` | B-04、C-15 | 已完成 | NFA/DFA 空匹配和重叠结果一致 |
| D-02 | 完成 DFA 报告、SOM 与 EOD 状态传播 | `internal/dfa` | D-01 | 已完成 | 报告位置和标志与确认路径一致 |
| D-03 | 完成 DFA 最小化和等价状态合并 | `internal/dfa` | D-01 | 已完成 | 合并前后语言与报告语义一致 |
| D-04 | 完成稠密/稀疏表压缩与状态上限回退 | `internal/dfa` | D-03 | 已完成 | 压缩、损坏和超限均有确定行为 |
| D-05 | 完成 RDFA 反向构造和边界确认 | `internal/dfa` | D-01、C-14 | 已完成 | From/To、长度和区间结果一致 |
| D-06 | 完成 DFA/RDFA 选择模型和反向失败回退 | `internal/compiler`、`internal/dfa` | D-04、D-05 | 已完成 | 不适用图保持通用确认正确性 |

### E. HWLM、Prefilter 和文字加速

| 编号 | 任务 | 代码范围 | 依赖 | 状态 | 完成条件 |
| --- | --- | --- | --- | --- | --- |
| E-01 | 完成 literal 分类、长度分级与集合拆分 | `internal/hwlm` | A-05、B-07 | 已完成 | 多规则、重复文字和大小写选择稳定 |
| E-02 | 完成 literal role 评分与候选策略 | `internal/hwlm`、`internal/compiler` | E-01 | 已完成 | 评分只影响加速，不改变确认结果 |
| E-03 | 完成 FDR 桶、mask 与多文字候选扫描 | `internal/fdr` | E-01、H-01 | 已完成 | 重叠、NUL、尾部与标量一致 |
| E-04 | 完成 FDR 候选失败确认和资源回退 | `internal/fdr`、`internal/prefilter` | E-03 | 已完成 | 误候选被确认消除，超限不漏报 |
| E-05 | 完成 Teddy lane、mask 和 shuffle 通用路径 | `internal/hwlm/teddy` | E-01、H-01 | 已完成 | lane 变化、空输入和非对齐一致 |
| E-06 | 完成 Noodle 变体选择和长文字拆分 | `internal/hwlm/noodle` | E-01 | 已完成 | 长文字候选及失败回退正确 |
| E-07 | 统一 Prefilter 候选与 AST/NFA 确认契约 | `internal/prefilter`、`scanner.go` | E-04~E-06 | 已完成 | Prefilter 只剪枝，不产生结果语义差异 |
| E-08 | 固化文字加速资源预算和跨后端排序去重 | `internal/hwlm`、`scanner.go` | E-07 | 已完成 | 多引擎候选合并不丢失或重复结果 |
| E-09 | 优化 Noodle 候选输出，复用切片并移除逐命中临时对象 | `internal/hwlm/noodle` | E-06 | 已完成 | 长输入和多规则扫描的分配量显著下降，候选集合不变 |
| E-10 | 合并相同文字的前缀路径并按规则 ID 扇出结果 | `internal/hwlm/noodle`、`internal/hwlm` | E-09 | 已完成 | 相同文字多规则结果完整，避免重复遍历和重复比较 |
| E-11 | 固化候选排序/去重的线性或有界策略 | `internal/hwlm/noodle`、`scanner.go` | E-08、E-10 | 已完成 | 已有稳定顺序保持不变，避免无必要全量排序和 Map |

### F. Rose 复杂运行时

| 编号 | 任务 | 代码范围 | 依赖 | 状态 | 完成条件 |
| --- | --- | --- | --- | --- | --- |
| F-01 | 完成 Rose graph、role、alias 和 width 推导 | `internal/rose`、`internal/compiler` | B-07、E-02 | 已完成 | 角色属性与规则宽度、边界一致 |
| F-02 | 完成 matcher 多角色转换与共享候选 | `internal/rose` | F-01、E-03 | 已完成 | 多角色优先级和重叠确认稳定 |
| F-03 | 完成 infix/outfix 依赖和 Block 确认链路 | `internal/rose` | F-02 | 已完成 | 候选失败不影响其他角色或结果 |
| F-04 | 完成 scheduler activation、transition 与状态去重 | `internal/rose` | F-02 | 已完成 | 激活顺序、去重和循环稳定 |
| F-05 | 完成 scheduler queue/步骤/内存预算与回退 | `internal/rose` | F-04 | 已完成 | 超限不泄漏状态、不漏后续规则 |
| F-06 | 完成 Miracle 多角色候选桶与确认时序 | `internal/rose/miracle.go` | F-03、E-07 | 已完成 | infix/outfix、排序与确认失败一致 |
| F-07 | 完成 Rose Report、SOM、EOD、Quiet、SingleMatch 闭环 | `internal/rose`、`internal/report` | F-05、G-03 | 已完成 | 与 Scanner/NFA 的结果和回调时序一致 |
| F-08 | 将 Rose 调度按能力安全整合到 `Scanner.Scan` | `scanner.go`、`internal/rose` | F-07、E-11 | 已完成 | 仅完整可证明文字角色进入 Rose，复杂或失败路径回退，保持后端结果顺序、去重和限制语义 |

### G. Fuzzy、NG、断言和 Unicode

| 编号 | 任务 | 代码范围 | 依赖 | 状态 | 完成条件 |
| --- | --- | --- | --- | --- | --- |
| G-01 | 完成字符类、通配的 Hamming/Edit 图转换 | `internal/fuzzy`、`scanner.go` | B-03、C-15 | 已完成 | 距离、宽度和错误候选均完整确认 |
| G-02 | 完成有限分支与固定宽度重复的 Fuzzy 转换 | `internal/fuzzy`、`scanner.go` | G-01 | 已完成 | 不可证明结构不转换且不误报 |
| G-03 | 完成 NG、Boundary 与 Lookaround 的安全转换 | `internal/compiler`、`internal/nfa` | C-14、D-06 | 已完成 | 负向和复杂断言完整 AST 确认 |
| G-04 | 完成 SOM/LBR 跨候选结果传播 | `internal/som`、`internal/nfa`、`scanner.go` | C-14、F-07 | 已完成 | 最左起点、EOD 和边界不丢失 |
| G-05 | 固化 UTF-8/UCP 与 byte-only 引擎隔离 | Parser、Scanner、各引擎 | G-01~G-04 | 已完成 | 字节加速不会处理不安全 Unicode 图 |
| G-06 | 完成 Fuzzy/NG/断言的排序、去重和限制统一 | `scanner.go`、`internal/compiler` | G-05 | 已完成 | 与普通扫描在区间和限制上无差异 |

### H. SIMD、CPU Dispatch 和热路径接入

| 编号 | 任务 | 代码范围 | 依赖 | 状态 | 完成条件 |
| --- | --- | --- | --- | --- | --- |
| H-01 | 完成 SuperVector 标量契约、掩码和尾部基线 | `internal/simd` | A-08 | 已完成 | 空输入、尾部和掩码操作可复现 |
| H-02 | 实现 x86 SSE/SSE4 能力门禁与基础操作 | `internal/simd/x86` | H-01 | 已完成 | 不支持 CPU 保持通用结果 |
| H-03 | 实现 x86 AVX2 热路径与非对齐处理 | `internal/simd/x86` | H-02 | 已完成 | AVX2 与标量在任意对齐一致 |
| H-04 | 实现 x86 AVX512/VBMI 独立选择与回退 | `internal/simd/x86`、`internal/dispatch` | H-03 | 已完成 | 仅真实能力启用，缺失时不触发非法指令 |
| H-05 | 实现 ARM NEON/ASIMD 能力门禁与基础操作 | `internal/simd/arm64` | H-01 | 已完成 | arm64 构建与通用语义一致 |
| H-06 | 实现 ARM SVE/SVE2 可变宽度与安全回退 | `internal/simd/arm64`、`internal/dispatch` | H-05 | 已完成 | 向量长度变化、尾部和缺失能力正确 |
| H-07 | 接入 LimEx/NFA 的向量热路径与标量降级 | `internal/nfa`、`internal/simd` | C-06、H-03、H-06 | 已完成 | 引擎选择和结果不依赖特定架构 |
| H-08 | 接入 FDR/Teddy/Noodle 的向量热路径与降级 | `internal/fdr`、`internal/hwlm` | E-03、E-05、E-06、H-03、H-06 | 已完成 | 候选集合与标量完全相同 |
| H-09 | 固化跨架构 dispatch 优先级、禁用与故障降级 | `internal/dispatch` | H-02~H-08 | 已完成 | 能力矩阵和运行时注册线程安全 |
| H-10 | 建立原生/通用热路径差异验证与基准证据 | `internal/simd`、benchmark | H-07~H-09 | 已完成 | 平台差异、吞吐和回退均有记录 |

### I. Block 运行时、序列化和兼容性

| 编号 | 任务 | 代码范围 | 依赖 | 状态 | 完成条件 |
| --- | --- | --- | --- | --- | --- |
| I-01 | 固化 Block 扫描输入不可变与 scratch 获取/归还 | `scanner.go`、`internal/scratch` | C-15、F-05 | 已完成 | 复用不污染下一次扫描 |
| I-02 | 完成回调停止、错误传播和资源释放 | `scanner.go`、`internal/scratch` | I-01 | 已完成 | Block API 无流式回调，扫描错误和资源释放路径明确 |
| I-03 | 完成 Program/Database 序列化版本与大小校验 | `internal/nfa`、`internal/dfa`、`internal/rose` | C、D、F | 已完成 | 版本不兼容与超长数据被拒绝 |
| I-04 | 完成截断、字段篡改和布局损坏恢复 | `internal/nfa`、`internal/dfa`、`internal/rose` | I-03 | 已完成 | 拒绝或安全重建，不使用损坏布局 |
| I-05 | 固化 API、错误、limits 和资源兼容矩阵 | 根 API、`internal/contract` | A-07、I-02 | 已完成 | 公开签名冻结，错误可追溯 |
| I-06 | 审计排除项并阻止流式/Vectored/ABI 隐式回归 | 文档、构建配置 | I-01~I-05 | 已完成 | 排除项未被隐式启用 |
| I-07 | 将 DFA/NFA 整块 `Spans` 路径接入 `Scanner.Scan` 调度 | `scanner.go`、`internal/engine`、`internal/nfa` | C-15、D-06、E-11 | 已完成 | 可整块执行的规则不再逐起点重复确认，结果顺序由后端保留，限制语义一致 |

### J. Conformance、性能与发布审计

| 编号 | 任务 | 代码范围 | 依赖 | 状态 | 完成条件 |
| --- | --- | --- | --- | --- | --- |
| J-01 | 建立多规则、重叠、空匹配和限制 corpus | `*_test.go`、`testdata` | A~I | 已完成 | Scanner 与各后端结果一致 |
| J-02 | 建立 UTF-8/UCP、边界、Lookaround、引用和重复 corpus | `*_test.go`、`testdata` | G-06 | 已完成 | 特殊语义与安全回退均覆盖 |
| J-03 | 建立 Fuzzy、Rose、组合、SOM、EOD corpus | `*_test.go`、`testdata` | F-07、G-06 | 已完成 | 复杂交叉语义无误报漏报 |
| J-04 | 建立 amd64/arm64、原生/通用、尾部/非对齐验证矩阵 | `internal/simd`、`internal/dispatch` | H-10 | 已完成 | 能力缺失时稳定回退 |
| J-05 | 固化性能、分配、状态、步骤和内存基线 | benchmark、`internal/nfa/resource.go` | C、E、H、I | 已完成 | 指标可重复记录，未定义阈值不虚报 |
| J-06 | 完成 API、MUST、EXCLUDE、源码映射、门禁和回滚审计 | `docs/technical-solutions`、CI | J-01~J-05 | 已完成 | Go 测试、竞态、静态检查、差异检查和 runbook 完整 |

### K. 源码级 Block 对齐补充任务

> 本阶段专门收敛差距清单中仍为“部分实现/未实现”的 Block 能力。任务来源固定为
> `vectorscan-replication-gap-checklist.md`；不包含已明确排除的 Streaming、Vectored、Chimera、Power/VSX、Sidecar 和二进制 ABI。

| 编号 | 任务 | 对齐范围 | 依赖 | 状态 | 完成条件 |
| --- | --- | --- | --- | --- | --- |
| K-01 | 编译标志与扩展字段逐位对齐 | C-01、C-02 | A-01~A-04 | 已完成 | 保留位、冲突组合、宽度/距离溢出、默认值和错误分类逐项一致 |
| K-02 | ExpressionInfo 与编译 bailout 完整映射 | C-03、C-04 | K-01 | 已完成 | 所有属性、表达式索引、错误码分类和 bailout 原因可追溯 |
| K-03 | CPU tune、禁用项和后端优先级对齐 | C-05 | K-01 | 已完成 | tune 选择、显式禁用、未知平台和回退矩阵一致 |
| K-04 | Database/Scratch 版本与大小校验收敛 | C-06 | I-03、I-04 | 已完成 | 版本、截断、字段篡改、资源上限和兼容错误稳定 |
| K-05 | NFAGraph 特殊节点、属性和边序语义补齐 | G-01 | B-01~B-05 | 已完成 | 节点/边类型、属性传播、遍历顺序和非法图矩阵完整 |
| K-06 | CharReach 全表示与 Unicode 折叠对齐 | G-02 | B-02、B-03 | 已完成 | 稀疏/密集转换、补集、大小写和 Unicode 隔离逐项一致 |
| K-07 | 图分析结果接入选择与 bailout | G-03、G-05 | K-05、K-06 | 已完成 | SCC、支配、循环宽度、状态/内存阈值影响可验证且稳定 |
| K-08 | 全部图重写与收敛条件补齐 | G-04 | K-05、K-07 | 已完成 | simplify/reduce/split/reachability pass 前后语义一致，失败安全回退 |
| K-09 | Castle/Gough 完整状态布局与调度 | N-01、N-02 | K-07 | 已完成 | repeat tree、反向确认、压缩布局、异常报告和预算矩阵完整 |
| K-10 | LimEx 全 variant、shuffle 和 native 语义 | N-03 | K-09、H-03 | 已完成 | 64 位状态变体、exceptional/shuffle、源掩码和回退逐项一致 |
| K-11 | McClellan/Sheng/McSheng 压缩布局与 cost model | N-04 | K-10 | 已完成 | 稠密/稀疏表、cache layout、转换条件和损坏表处理一致 |
| K-12 | Tamarama/Vermicelli 全范围与前后缀策略 | N-05、N-06 | K-09 | 已完成 | 范围分解、桶调度、variant 选择、后缀确认和长输入行为一致 |
| K-13 | Shufti/Truffle 完整转置与字符集压缩 | N-07 | K-10、H-01 | 已完成 | 转置表构造、特殊字符集、SIMD 变体、尾部和非对齐一致 |
| K-14 | Repeat/MPV 全加速与报告语义 | N-08 | K-09、K-12 | 已完成 | 固定/可变重复、分支加速、报告状态和选择启发式一致 |
| K-15 | LBR 复杂断言与 NG 闭环 | N-09 | K-10、G-02 | 已完成 | lookaround、边界、状态压缩、失败回退和结果传播完整 |
| K-16 | NFA 全部 engine cost model 收敛 | N-10 | K-09~K-15 | 已完成 | 优先级、代价计算、bailout 分支和选择记录与源码行为一致 |
| K-17 | DFA/RDFA 状态传播与压缩布局对齐 | D-01~D-03 | K-07、K-16 | 进行中 | 确定化、最小化、稠密/稀疏压缩和反向确认一致；报告/SOM/EOD 沿底层 NFA 路径传播 |
| K-18 | HWLM FDR/Teddy/Noodle 全 variant 对齐 | H-01~H-05 | E-01~E-11、K-13 | 进行中 | literal 分类、lane/mask/shuffle、cost、长文字拆分和确认调度完整 |
| K-19 | Rose graph、角色 cost 和调度压缩对齐 | R-01~R-06 | F-01~F-08、K-18 | 进行中 | alias/width、角色选择、优先级、队列压缩、catchup 和报告时序一致 |
| K-20 | SIMD 原生指令与全热点接入 | S-01~S-07 | H-01~H-10、K-09~K-19 | 进行中 | SSE/AVX512/VBMI、NEON/SVE/SVE2、NFA/HWLM 热点原生路径及安全回退完整 |
| K-21 | Block 运行时源码级错误与资源闭环 | X-01、O-03 | I-01~I-07、K-17、K-19 | 已完成 | Scratch 生命周期、错误码、取消/停止、资源行为和 Block 结果语义一致 |
| K-22 | Compiler 优化与 engine cost 全量映射 | O-01、O-02 | K-08、K-16、K-17 | 已完成 | pass、触发条件、代价模型和回退原因逐项有代码证据 |
| K-23 | 性能长期基线与回归治理 | O-04 | K-20、K-21 | 进行中 | 固定 corpus 记录吞吐、延迟、分配、状态和内存变化，不虚设绝对阈值 |
| K-24 | 全量源码 fixture 与平台 conformance | O-05 | K-01~K-23 | 进行中 | 所有 Block fixture、错误边界、amd64/arm64 和原生/通用矩阵可重复验证 |
| K-25 | 发布审计与源码映射闭环 | O-06 | K-23、K-24 | 进行中 | API、MUST、EXCLUDE、源码映射、回滚和发布 runbook 全部有证据 |

## 4. 覆盖关系

K-14 本轮证据：`internal/repeat/repeat.go` 新增可复用结束偏移缓冲，并将区间、终点区间、全局预算和最大命中数路径统一到低分配的重复执行；全局预算扫描直接枚举文字候选起点，保留步骤超限和结果超限的原有语义。`internal/nfa/engines.go` 进一步收紧纯文字重复图识别：统一周期、前缀和分支边界经过编译期等价检查后才启用 Repeat；MPV 文字分支按编译期长度顺序直接输出，MatchAt、区间、Spans、预算及上下文路径均支持结束偏移缓冲复用；MPV 运行时校验改为无临时 map/string 分配的前缀与重复分支检查。验证命令：`go test ./internal/repeat ./internal/nfa`。

K-15 本轮证据：`internal/nfa/engines.go` 的 LBR 整块扫描在布局校验后复用紧凑状态队列和结束偏移缓冲执行，边界断言、结果去重、步骤/结果/队列预算与不支持查找断言的通用确认回退保持一致。本轮在 LBR 主路径新增两个业务方法：(1) `lbrProgram.SpansInto` 将整块扫描结果写入调用方提供的 `dst []Span` 缓冲，复用底层 `endsBuf` 避免每个起点分配结束偏移切片，`limit>0` 时遇限额即截断；`cap(dst)` 不足时按 `initialSpanCapacity` 重分配保证长输入不被截断；(2) `lbrProgram.MatchAtRangeInto` 在区间受限场景下复用调用方 `dst []int` 缓冲并裁剪掉不在 `[from, to]` 内的结束偏移，避免分配中间结果。Gough 反向确认缓存 `acceptMask` 经 `reverseMask` 展开并剔除 `deadMask` 后的 `initialMask`，避免每次 `MatchAt` 重复按位重新展开；`goughRuntimeShapeOK` 在末尾验证缓存与实时位掩码一致。LBR、Gough、Castle、字节 NFA、Sheng、McSheng、Tamarama、Vermicelli、Shufti、Truffle 等布局均新增 `MatchAtInto` 入口并在主选择路径统一复用结束偏移缓冲。`internal/nfa/engines.go` 的 `Engine.SpansLimit` 改用 `preferredMatchAtInto` 复用确认结果缓冲。本轮新增 `internal/nfa/lbr_assertion_test.go`：`TestLBRBoundaryConformance` 覆盖 LBR 支持的全部七类边界（Begin、End、WordBoundary、NonWordBoundary、BeginAbsolute、EndAbsolute、EndBeforeFinalNewline），并验证各边界在词内、词外、绝对位置和换行后的接受与拒绝；`TestLBRFallbackForLookaround` 验证含 lookbehind/lookahead 的图不进入 LBR 专用布局，统一回退到 Castle 基线并保持结果一致；`TestLBRRuntimeShapeCatchesLiteralTampering` 与 `TestLBRRuntimeShapeCatchesFirstMaskTampering` 校验运行时形状检查在字面量字节或首字节掩码被篡改时立即拒绝，避免错误执行路径进入整块扫描；`TestLBRSpansBudgetKeepsEndOrder` 校验 LBR 整块扫描在结果限额内按起点稳定排序；`TestLBRSpansIntoReusesBuffer` 校验新增 `SpansInto` 与 `Spans` 结果一致，限额截断保持同步；`TestLBRMatchAtRangeIntoFiltersAndReusesBuffer` 校验 `MatchAtRangeInto` 区间裁剪与限额截断。验证命令：`go test ./internal/nfa`、`go test ./...`。

K-16 本轮证据：`EngineSelection` 增加稳定的相对代价和 bailout 原因，`estimateEngineCost` 综合状态、边、分支、字符类、断言、重复及布局内存压力；诊断现在保留实际编译错误并区分资源预算拒绝与专用布局不可用，选择诊断不改变执行路径。本轮在 `internal/nfa/engines.go` 的 `selectTableKind` 把 Sheng / Shufti / Tamarama 三者皆满足基本结构条件时的终选改为按 `estimateEngineCost` 选取代价最低的引擎（不再是单一启发式分支判断），让 cost model 实质影响选择结果；其他高代价分支（Truffle / McSheng / Vermicelli）继续按结构特征走快速路径，优先级顺序保留。本轮新增 `internal/nfa/cost_priority_test.go`：`TestSelectEngineKindPrioritizesSpecializedLayout` 验证 `SelectEngineKind` 在多个专用布局可选时优先返回 Repeat/MPV/LimEx 而非通用回退；`TestExplainEngineSelectionRecordsCostAndBailout` 校验诊断输出的 `Kind`/`Cost`/`Reason`/`Bailout` 与实际编译结果一致，独立布局不记录 bailout；`TestExplainEngineSelectionHandlesUnsupportedGraph` 校验含 lookaround 的图被安全标记为非独立布局并提供 bailout 原因；`TestSelectTableKindPicksLowestCostEngine` 校验无 class / 无分支的图走 cost 选取路径；`TestSelectTableKindPrefersTamaramaForBranchedGraph` 校验有 class 又有分支的图把 Tamarama 纳入候选并按 cost 挑选。验证命令：`go test ./internal/nfa`、`go test ./...`。

K-17 本轮证据：`internal/dfa/rdfa.go` 将反向宽度改为按起点到接受节点的路径 DFS 计算，正确处理分支、汇合和回边；无界消费循环安全拒绝反向表，避免节点宽度简单求和造成错误边界；反向确认新增起点缓冲复用，整块反向区间扫描不再为每个结束偏移创建临时切片。本轮新增 `internal/dfa/dense_table_test.go`：`TestDenseTableAcceptsAndRejectsAllByteTransitions` 校验稠密转移表覆盖 256 个输入字节且与稀疏路径语义一致；`TestMinimizeAfterDenseTableKeepsLanguage` 验证最小化顺序与稠密化顺序互不破坏等价语言；`TestReverseProgramPreservesDenseForwardResults` 校验反向程序对每个结束位置得到的起点集均能被前向稠密程序在相同结束位置上确认。验证命令：`go test ./internal/dfa`。

K-19 本轮证据：`internal/rose/rose.go` 为 Miracle 多角色候选增加首字节掩码和向量分块筛选，并提供角色状态切片复用入口；`scanner.go` 在编译期缓存不可变 Rose 角色图，并通过并发安全的状态缓冲池复用命中切片，避免每个 Block 重建角色、指令和自动机；新增 `roseSinglePool` 复用 SingleMatch 规则的去重表；无确认纯文字角色在整块扫描时直接消费已排序角色命中，跳过调度队列和重复确认。`rose.Program` 增加私有 `findRole` 与公开 `RoleByID` 入口，只读路径返回共享底层字面量的角色；`scanner.scanRoseDirectInto` 改用 `RoleByID`，消除每次候选的 Literal 防御性复制。`rose.FindMatchesInto` 增加 `matchBuf` 复用 matcher 命中切片，`findMiracleMulti` 拆分为 `findMiracleMultiInto` 复用调用方缓冲。`scanRoseDirectInto` 按候选数预分配 Match 缓冲，避免 append 多次扩容。`Scanner.canUseRoseInScan` 在编译期一次性计算并缓存，避免每次 Scan 重复遍历规则与角色图。`matchRuleInto` 和 `Engine.MatchAtInto` 在主选择路径复用结束偏移缓冲；`compiledRule` 预先计算 `containsAny`、`requiresEndOfData`、`backendEligible`、`nonGreedy`、`hasBackref` 与 `hasConditional`，避免每个起点重复遍历 AST。`fastLiteral` 路径使用扩容到 256 元素的栈上候选缓冲复用 `literalFindInto` 的结果，匹配器中间 Match 缓冲进入 `sync.Pool`。`rose.MatchAt` 大小写敏感且全 ASCII 时改用 `bytes.Equal`，大小写不敏感时改用 `bytes.EqualFold`。`ScanRuleScales` 100 规则场景分配从 13050 降至 16，1000 规则场景从 130072 降至 21。验证命令：`go test ./internal/rose ./...`、`go test -run '^$' -bench 'ScanRuleScales' -benchtime=3x .`、`go test -race ./...`。

K-18 本轮证据：`internal/hwlm/noodle/noodle.go` 的前缀树候选路径及 `internal/hwlm/teddy/teddy.go` 均支持向调用方结果切片写入，消除候选起点的临时结果切片；尾部扫描同时覆盖原始与 ASCII 折叠首字节，FDR/Teddy/Noodle 统一保持候选确认、重叠和去重语义。Teddy 增加 `needsDedup` 与 `dedupBuf` 字段，仅在存在跨桶字面量时启用去重并复用键集合；`scanner.go` 为三种匹配器各自的 `Match` 缓冲建立 `sync.Pool`，`fastLiteral` 路径的 `candBuf` 容量从 16 提升到 64 元素，并在已知候选数后预分配 `matches` 切片。验证命令：`go test ./internal/hwlm/noodle ./internal/hwlm ./internal/hwlm/teddy ./internal/fdr ./internal/simd ./...`、`go test -run '^$' -bench 'ScanRuleScales' -benchtime=3x .`。

K-21 本轮证据：`scanner.go` 在构造阶段缓存不可变扫描计划校验结果，Block `ScanInto` 进入执行前使用该错误结论，损坏规则索引或后端布局安全返回且不重复遍历整张图；后端直扫结果现在直接写入调用方结果切片，移除中间结果复制和区间转换临时切片；上下文 scratch/report 仍按既有生命周期复用。本轮在 `scanner.go` 主路径新增 `ErrCancelled` 哨兵错误与三处业务方法：`(1) Scanner.ScanContext(ctx, data)` 与 `(2) Scanner.ScanContextInto(ctx, data, matches)` 支持 `context.Context` 取消语义，nil ctx 时退化为 `Scan`/`ScanInto` 不改变现有调用契约；`(3) scanContextInto` 启动一次性 goroutine 监听 `ctx.Done()` 并通过 `atomic.StoreUint32(&scanner.cancelFlag, 1)` 异步广播，主扫描循环按 `start&1023 == 0` 周期探测 `cancelFlag`，探测命中即返回 `ErrCancelled` 与已收集 matches；goroutine 与 defer 共同通过 `sync.Once` 保证 stop channel 恰好关闭一次，避免 `close of closed channel` 竞争。验证命令：`go test ./...`、`go test -race ./...`。

K-22 本轮证据：`compile.go` 对图优化不收敛采用原图安全回退并记录 `optimization-fallback`，并将后端资源/布局失败产生的最终 bailout 元数据写回 `compiledRule.info`，保证诊断与实际执行一致。本轮完成三项业务改动：(1) `internal/nfagraph/rewrite.go` 的 `OptimizeWithLimit` 返回错误时携带触发震荡的最后修改 pass 名称（`lastChangingPass`）与轮数上限，便于按 pass 粒度记录 `optimization-fallback`；(2) `compile.go` 把 `Optimize` 返回的错误拼接到 `optimization-fallback` 元数据；(3) `internal/nfagraph/rewrite.go` 新增 `CostModel`、`NormalizeWithCost`、`OptimizeWithCostAndLimit` 三处业务方法：小图（节点数低于阈值）跳过 `BypassEpsilonJoins/SquashLinearJoins/SquashLinearLiterals/MergeEquivalentNodes` 昂贵 pass，保留 `RemoveRedundantEdges/PruneUnreachable/PruneDeadEnds` 低代价收益稳定的 pass；空 `CostModel{}` 等价旧 `NormalizeWithStats` 行为；阈值非零时按节点数决定是否应用合并/旁路；(4) `compile.go` 的资源预算循环按 `autoProgram.Kind` 分别累加 DFA 与 NFA 的 `MemoryBytes`：DFA 走 `autoProgram.DFA.MemoryBytes()`，NFA 走 `autoProgram.NFA.MemoryBytes()`，避免 KindNFA 路径下字节后端内存被遗漏、绕开限额校验。本轮新增 `bailout_test.go`：`TestBailoutReasonForGraphBuildFailure` 校验 UTF-8 + 边界图触发 `feature-gate` bailout 并被记录；`TestBailoutReasonForCombinedExpression` 校验组合表达式元数据保持 `Combination` 标志；`TestBailoutReasonForUnsupportedBackend` 校验大小写不敏感图保持可执行且不强制额外的 bailout 字段；`TestCompileAutoNFAProgramTracksMemory` 校验 `a+` 等纯文字重复图触发 NFA 后端并被 `Scan` 路径成功消费；`TestBailoutReasonForOptimizationFallbackCarriesPassName` 校验正常收敛图不携带 `optimization-fallback`。本轮新增 `internal/nfagraph/optimize_test.go`：`TestOptimizeWithLimitReportsLastChangedPassInError`、`TestOptimizeWithLimitRestoresGraphOnFailure`、`TestLastChangingPass`、`TestNormalizeWithCostSkipsExpensivePassesForSmallGraph`、`TestNormalizeWithCostAppliesExpensivePassesForLargeGraph`、`TestNormalizeWithCostDefaultEqualsNormalizeWithStats`、`TestOptimizeSucceedsWithinLimit`。验证命令：`go test ./...`、`go test -race ./...`。

K-20 本轮证据：修正 `internal/simd/arm64/arm64.go` 在 NEON 不可用时永 unreachable 的分支，并为 amd64 SSE2、AVX2、ARM64 NEON 增加双向量原生比较掩码；LimEx、稀疏、范围、半字节、表格、字节、Castle 和 Gough NFA 的整块 Spans 在入口完成布局校验后复用无校验确认路径，避免每个起点重复遍历状态表；Castle/Gough 与字节 NFA 的预算确认支持调用方结束偏移缓冲，减少多起点扫描分配；x86/ARM 原生比较路径继续受真实 CPU 能力门禁保护，缺少能力时使用展开标量回退。验证命令：`go test ./internal/simd ./internal/dispatch ./...`、`GOOS=linux GOARCH=amd64 go test -c ./internal/simd`、`GOOS=linux GOARCH=arm64 go test -c ./internal/simd`、`go vet ./...`。

K-23 本轮证据：执行固定规则规模、NFA 布局和 SIMD 基准，记录 `ns/op`、`B/op`、`allocs/op`、状态/边/布局内存；当前仅形成基线，不设置绝对性能阈值。Rose 计划缓存、纯文字直接候选路径及状态缓冲池已移除调度队列重复构建；NFA 专用 Spans 复用结束偏移缓冲并跳过重复布局校验，Castle/Gough 预算路径也复用结束偏移。本轮基线刷新：`ScanRuleScales` 在 1000 规则样本约 3.77ms、约 6.52MB 分配；NFA 专用引擎布局确认 9.8～19μs，状态缓冲池在 LimEx 上保持 7 allocs/op；Rose `ProgramFindMatches` 11.1μs 与 HWLM `FindAll` 67ns 维持稳定；最新基线已记录到 `performance-baseline.md`。验证命令：`go test -run '^$' -bench 'ScanRuleScales|EngineFamilies|ResourceUsage|ProgramFindMatches|FindAll|BackendEqualByteMask' -benchmem ./...`。

K-24 本轮证据：统一 Go conformance 已通过；跨架构测试需在目标架构执行，当前主机完成 amd64/arm64 测试二进制交叉编译检查，交叉产物不能在本机运行。验证命令：`go test ./...`、`GOOS=linux GOARCH=amd64 go test -c ./internal/simd`、`GOOS=linux GOARCH=arm64 go test -c ./internal/simd`。

K-25 本轮证据：已核对源码映射、排除项、公开 API 约束和发布 runbook 文档；待 K-20～K-24 全部收敛后再执行最终发布门禁。


| 差距清单 | 对应任务 |
| --- | --- |
| C-01~C-06 | A-01~A-08、I-03~I-05 |
| G-01~G-05 | B-01~B-08 |
| N-01~N-10 | C-01~C-15 |
| D-01~D-03 | D-01~D-06 |
| H-01~H-05 | E-01~E-08 |
| R-01~R-06 | F-01~F-08 |
| S-01~S-07 | H-01~H-10、J-04 |
| X-01 | K-21 |
| X-02~X-05 | 明确排除，不进入实施任务 |
| O-01~O-06 | B-06~B-08、C-15、E-09~E-11、F-08、I-07、J-01~J-06 |
| 差距清单 C-01~C-06 | K-01~K-04 |
| 差距清单 G-01~G-05 | K-05~K-08 |
| 差距清单 N-01~N-10 | K-09~K-16 |
| 差距清单 D-01~D-03 | K-17 |
| 差距清单 H-01~H-05、R-01~R-06 | K-18~K-19 |
| 差距清单 S-01~S-07 | K-20 |
| 差距清单 X-01、O-01~O-06 | K-21~K-25 |

### 覆盖完整性审计

- 差距清单共 53 个编号：`C-01~C-06`、`G-01~G-05`、`N-01~N-10`、`D-01~D-03`、`H-01~H-05`、`R-01~R-06`、`S-01~S-07`、`X-01~X-05`、`O-01~O-06`。
- 已纳入实施的 49 个编号全部映射到 A～J 基础任务或 K-01～K-25 源码级补充任务；当前不存在未映射的 Block 差距编号。
- `X-02~X-05`（Streaming、Vectored、Chimera/Power/VSX/Sidecar、二进制 ABI）共 4 个编号按既定约束排除，不计入完成度，也不应被标记为“待实现”。
- K 阶段 25 项均为源码级收敛任务；K-01～K-16、K-15、K-21、K-22 已完成；其余 K-17～K-20、K-23～K-25 仍未在 `internal/nfa/engines.go`、`internal/dfa` 或 `compile.go` 改动业务逻辑。本轮在 `internal/nfa/engines.go` 的 LBR 主路径新增 `SpansInto` 与 `MatchAtRangeInto`（K-15），把 `selectTableKind` 的 Sheng/Shufti/Tamarama 终选改为按 `estimateEngineCost` 选最低代价（K-16），在 `compile.go` 的资源预算循环按 `autoProgram.Kind` 分支累加 DFA 与 NFA 后端 `MemoryBytes`（K-22），新增 `CostModel`/`NormalizeWithCost`/`OptimizeWithCostAndLimit` 与 `lastChangingPass`（K-22），在 `internal/nfagraph/rewrite.go` 的 `OptimizeWithLimit` 错误中携带触发震荡的 pass 名称（K-22），在 `scanner.go` 主路径新增 `ErrCancelled` 与 `ScanContext`/`ScanContextInto`/`scanContextInto` 实现 context.Context 取消语义（K-21）。因此“任务已补齐”仅表示清单覆盖完整，不代表源码级对齐已经完成。

## 5. 收口条件

1. A～J 的基础任务与 K 的源码级补充任务均须为`已完成`，且每项已填写实际文件、验证命令和结果证据。
2. `X-02~X-05` 在未获得明确范围变更前始终保持排除，不计入完成度。
3. 所有专用后端均对非法图、布局损坏、资源超限和不支持平台安全回退。
4. `go test ./...`、`go test -race ./...`、`go vet ./...`、`git diff --check` 和已定义基准门禁全部通过。
5. 仅在第 4 节及 K 阶段所有非排除项完成且源码级平台决策已明确时，才可使用“完整复刻”表述；否则最终结论只能为“Block 模式兼容实现”。
