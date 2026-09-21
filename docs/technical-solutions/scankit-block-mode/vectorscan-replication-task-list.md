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
| K-17 | DFA/RDFA 状态传播与压缩布局对齐 | D-01~D-03 | K-07、K-16 | 已完成 | 确定化、最小化、稠密/稀疏压缩和反向确认一致；报告/SOM/EOD 沿底层 NFA 路径传播 |
| K-18 | HWLM FDR/Teddy/Noodle 全 variant 对齐 | H-01~H-05 | E-01~E-11、K-13 | 已完成 | literal 分类、lane/mask/shuffle、cost、长文字拆分和确认调度完整 |
| K-19 | Rose graph、角色 cost 和调度压缩对齐 | R-01~R-06 | F-01~F-08、K-18 | 已完成 | alias/width、角色选择、优先级、队列压缩、catchup 和报告时序一致 |
| K-20 | SIMD 原生指令与全热点接入 | S-01~S-07 | H-01~H-10、K-09~K-19 | 已完成 | SSE/AVX512BW/VBMI、NEON 原生内核与 NFA/HWLM/Rose 热点接入及安全回退完整；SVE/SVE2 因工具链与硬件三重外部阻塞显式排除（第五轮证据，待维护者确认） |
| K-21 | Block 运行时源码级错误与资源闭环 | X-01、O-03 | I-01~I-07、K-17、K-19 | 已完成 | Scratch 生命周期、错误码、取消/停止、资源行为和 Block 结果语义一致 |
| K-22 | Compiler 优化与 engine cost 全量映射 | O-01、O-02 | K-08、K-16、K-17 | 已完成 | pass、触发条件、代价模型和回退原因逐项有代码证据 |
| K-23 | 性能长期基线与回归治理 | O-04 | K-20、K-21 | 已完成 | 固定 corpus 记录吞吐、延迟、分配、状态和内存变化，不虚设绝对阈值 |
| K-24 | 全量源码 fixture 与平台 conformance | O-05 | K-01~K-23 | 已完成 | 所有 Block fixture、错误边界、amd64/arm64 和原生/通用矩阵可重复验证 |
| K-25 | 发布审计与源码映射闭环 | O-06 | K-23、K-24 | 已完成 | API、MUST、EXCLUDE、源码映射、回滚和发布 runbook 全部有证据 |
| K-26 | Rose 候选路径可达性与宽窗口回归收敛 | R-01~R-06、S-01~S-07 | K-19、K-20 | 已完成 | 多角色纯文字程序的候选器路径在构造、归一化与公共入口上均可达，结果与通用匹配器逐位一致 |
| K-27 | 工具链最低版本与 arm64 SIMD 助记符对齐 | S-01~S-07 | K-20 | 已完成 | `go.mod` 最低工具链提升到 Go 1.27，arm64 内核沿用 1.27 助记符；低版本工具链在加载 `go.mod` 阶段即给出明确错误，不再退化为汇编失败 |

## 4. 覆盖关系

K-14 本轮证据：`internal/repeat/repeat.go` 新增可复用结束偏移缓冲，并将区间、终点区间、全局预算和最大命中数路径统一到低分配的重复执行；全局预算扫描直接枚举文字候选起点，保留步骤超限和结果超限的原有语义。`internal/nfa/engines.go` 进一步收紧纯文字重复图识别：统一周期、前缀和分支边界经过编译期等价检查后才启用 Repeat；MPV 文字分支按编译期长度顺序直接输出，MatchAt、区间、Spans、预算及上下文路径均支持结束偏移缓冲复用；MPV 运行时校验改为无临时 map/string 分配的前缀与重复分支检查。验证命令：`go test ./internal/repeat ./internal/nfa`。

K-15 本轮证据：`internal/nfa/engines.go` 的 LBR 整块扫描在布局校验后复用紧凑状态队列和结束偏移缓冲执行，边界断言、结果去重、步骤/结果/队列预算与不支持查找断言的通用确认回退保持一致。本轮在 LBR 主路径新增两个业务方法：(1) `lbrProgram.SpansInto` 将整块扫描结果写入调用方提供的 `dst []Span` 缓冲，复用底层 `endsBuf` 避免每个起点分配结束偏移切片，`limit>0` 时遇限额即截断；`cap(dst)` 不足时按 `initialSpanCapacity` 重分配保证长输入不被截断；(2) `lbrProgram.MatchAtRangeInto` 在区间受限场景下复用调用方 `dst []int` 缓冲并裁剪掉不在 `[from, to]` 内的结束偏移，避免分配中间结果。Gough 反向确认缓存 `acceptMask` 经 `reverseMask` 展开并剔除 `deadMask` 后的 `initialMask`，避免每次 `MatchAt` 重复按位重新展开；`goughRuntimeShapeOK` 在末尾验证缓存与实时位掩码一致。LBR、Gough、Castle、字节 NFA、Sheng、McSheng、Tamarama、Vermicelli、Shufti、Truffle 等布局均新增 `MatchAtInto` 入口并在主选择路径统一复用结束偏移缓冲。`internal/nfa/engines.go` 的 `Engine.SpansLimit` 改用 `preferredMatchAtInto` 复用确认结果缓冲。本轮新增 `internal/nfa/lbr_assertion_test.go`：`TestLBRBoundaryConformance` 覆盖 LBR 支持的全部七类边界（Begin、End、WordBoundary、NonWordBoundary、BeginAbsolute、EndAbsolute、EndBeforeFinalNewline），并验证各边界在词内、词外、绝对位置和换行后的接受与拒绝；`TestLBRFallbackForLookaround` 验证含 lookbehind/lookahead 的图不进入 LBR 专用布局，统一回退到 Castle 基线并保持结果一致；`TestLBRRuntimeShapeCatchesLiteralTampering` 与 `TestLBRRuntimeShapeCatchesFirstMaskTampering` 校验运行时形状检查在字面量字节或首字节掩码被篡改时立即拒绝，避免错误执行路径进入整块扫描；`TestLBRSpansBudgetKeepsEndOrder` 校验 LBR 整块扫描在结果限额内按起点稳定排序；`TestLBRSpansIntoReusesBuffer` 校验新增 `SpansInto` 与 `Spans` 结果一致，限额截断保持同步；`TestLBRMatchAtRangeIntoFiltersAndReusesBuffer` 校验 `MatchAtRangeInto` 区间裁剪与限额截断。验证命令：`go test ./internal/nfa`、`go test ./...`。

K-16 本轮证据：`EngineSelection` 增加稳定的相对代价和 bailout 原因，`estimateEngineCost` 综合状态、边、分支、字符类、断言、重复及布局内存压力；诊断现在保留实际编译错误并区分资源预算拒绝与专用布局不可用，选择诊断不改变执行路径。本轮在 `internal/nfa/engines.go` 的 `selectTableKind` 把 Sheng / Shufti / Tamarama 三者皆满足基本结构条件时的终选改为按 `estimateEngineCost` 选取代价最低的引擎（不再是单一启发式分支判断），让 cost model 实质影响选择结果；其他高代价分支（Truffle / McSheng / Vermicelli）继续按结构特征走快速路径，优先级顺序保留。本轮新增 `internal/nfa/cost_priority_test.go`：`TestSelectEngineKindPrioritizesSpecializedLayout` 验证 `SelectEngineKind` 在多个专用布局可选时优先返回 Repeat/MPV/LimEx 而非通用回退；`TestExplainEngineSelectionRecordsCostAndBailout` 校验诊断输出的 `Kind`/`Cost`/`Reason`/`Bailout` 与实际编译结果一致，独立布局不记录 bailout；`TestExplainEngineSelectionHandlesUnsupportedGraph` 校验含 lookaround 的图被安全标记为非独立布局并提供 bailout 原因；`TestSelectTableKindPicksLowestCostEngine` 校验无 class / 无分支的图走 cost 选取路径；`TestSelectTableKindPrefersTamaramaForBranchedGraph` 校验有 class 又有分支的图把 Tamarama 纳入候选并按 cost 挑选。验证命令：`go test ./internal/nfa`、`go test ./...`。

K-17 本轮证据：`internal/dfa/dfa.go` 的 `State` 新增 `Reports`/`ReportsEOD` 两组报告编号，确定化阶段按底层图节点的 `ReportID` 把报告传播到接受状态，并由 `eodGuardedNodes` 区分“任意位置触发”和“仅在数据末尾触发”两类报告；`Validate` 校验报告集合升序去重、非零、两类互不重叠且可追溯到本状态的报告节点。`Minimize` 的初始划分改为对（接受标记，Reports，ReportsEOD）编码，报告集合不同的等价接受状态不再被合并，合并时对报告集合取并集去重并剥离重复的末尾报告。`internal/dfa/rdfa.go` 的反向图重建改为清除旧报告位置并在反向接受节点上按前向接受报告集合重建，`reverseAcceptReports` 复用确定化结果，使反向确认的 `AcceptReportIDs` 与前向完全一致。`internal/engine/engine.go` 新增 `ReportIDs`/`HasEODReports` 统一后端报告元数据，`internal/nfa/nfa.go` 新增 `ReportIDs` 报告编号快照，`scanner.go` 在 `compiledRule` 缓存末尾报告标记并让 `backendEligible`、`matchRuleInto` 拒绝末尾报告后端直接匹配，避免字节状态表在非末尾偏移误报。测试：`internal/dfa/report_state_test.go`（报告传播、末尾报告分离、报告感知最小化的语言与报告语义、相同报告集合仍可合并、克隆与序列化往返、非法报告集合拒绝、反向报告集合一致）、`internal/engine/report_metadata_test.go`（DFA/NFA 报告元数据传播与末尾报告标记）、`internal/nfa/report_ids_test.go`（报告编号快照升序去重且不被调用方污染）、`dfa_report_gate_test.go`（末尾报告后端被直接匹配快路径拒绝，普通报告不受影响）。验证命令：`go test ./internal/dfa ./internal/engine ./internal/nfa .`、`go test ./...`、`go test -race ./...`、`go vet ./...`、`git diff --check`。

K-19 本轮证据：`internal/rose/rose.go` 为 Miracle 多角色候选增加首字节掩码和向量分块筛选，并提供角色状态切片复用入口；`scanner.go` 在编译期缓存不可变 Rose 角色图，并通过并发安全的状态缓冲池复用命中切片，避免每个 Block 重建角色、指令和自动机；新增 `roseSinglePool` 复用 SingleMatch 规则的去重表；无确认纯文字角色在整块扫描时直接消费已排序角色命中，跳过调度队列和重复确认。`rose.Program` 增加私有 `findRole` 与公开 `RoleByID` 入口，只读路径返回共享底层字面量的角色；`scanner.scanRoseDirectInto` 改用 `RoleByID`，消除每次候选的 Literal 防御性复制。`rose.FindMatchesInto` 增加 `matchBuf` 复用 matcher 命中切片，`findMiracleMulti` 拆分为 `findMiracleMultiInto` 复用调用方缓冲。`scanRoseDirectInto` 按候选数预分配 Match 缓冲，避免 append 多次扩容。`Scanner.canUseRoseInScan` 在编译期一次性计算并缓存，避免每次 Scan 重复遍历规则与角色图。`matchRuleInto` 和 `Engine.MatchAtInto` 在主选择路径复用结束偏移缓冲；`compiledRule` 预先计算 `containsAny`、`requiresEndOfData`、`backendEligible`、`nonGreedy`、`hasBackref` 与 `hasConditional`，避免每个起点重复遍历 AST。`fastLiteral` 路径使用扩容到 256 元素的栈上候选缓冲复用 `literalFindInto` 的结果，匹配器中间 Match 缓冲进入 `sync.Pool`。`rose.MatchAt` 大小写敏感且全 ASCII 时改用 `bytes.Equal`，大小写不敏感时改用 `bytes.EqualFold`。`ScanRuleScales` 100 规则场景分配从 13050 降至 16，1000 规则场景从 130072 降至 21。验证命令：`go test ./internal/rose ./...`、`go test -run '^$' -bench 'ScanRuleScales' -benchtime=3x .`、`go test -race ./...`。本轮进一步落地角色 cost 选择与队列压缩：(1) `internal/rose/rose.go` 新增 `RoleCost`、`Role.Cost()`、`Role.Weight()` 与 `Role.ScanEquivalent`，按文字长度、锚定、末尾约束、偏移上界、大小写折叠和确认需求折算角色扫描代价，并新增 `Program.RolesForReport`/`Program.CheapestRoleForReport` 按代价升序返回同一报告下的角色；`scanner.scanRoseInto` 在候选与事件不匹配时改按代价顺序选取真正命中的代表角色，替换原先逐事件 `RolesCopy` 的全量拷贝。(2) `scanner.go` 新增 `dedupeRoseRoles`，在构建角色前合并扫描完全等价的角色（等价角色在相同输入上产生完全相同的候选与报告，重复扫描只增加确认开销），例如 `(?:ab)c|a(?:bc)` 由两个分支合并为单个 `abc` 角色。(3) `internal/rose/rose.go` 的 `Queue.PushAll` 改为一次整体排序入队，新增 `Queue.Compact` 过滤非法状态、稳定排序并按角色/偏移只保留优先级最低的状态；`Scheduler.activateBatch` 让 `ActivateMatches`/`ActivateMatchesRange` 在批内先压缩再一次性并入队列并同步 `active` 表，`SetMaxPending` 截断前先压缩队列，`stateBefore` 的角色编号比较改为严格小于。测试：`internal/rose/role_cost_test.go`（代价分量方向、扫描等价判定、代表角色选择与快照隔离、索引在 Normalize/Load 后可用）、`internal/rose/queue_compact_test.go`（批量入队等价于逐个入队、压缩去重与截断、批量激活等价于逐个激活、优先级替换、maxPending 截断、既有低优先级不被覆盖）、`rose_role_selection_test.go`（等价分支合并为单角色且扫描结果不变、不同分支不误合并、多角色报告扫描一致）。基准：`BenchmarkQueuePushAll` 在 4096 个逆序状态下批量入队 456.7µs/18 allocs，逐个插入入队 9.24ms/15 allocs。验证命令：`go test ./internal/rose ./...`、`go test -race ./...`、`go vet ./...`、`go build ./...`、`git diff --check`、`go test -run '^$' -bench BenchmarkQueuePushAll -benchmem ./internal/rose/`。

K-18 本轮证据：`internal/hwlm/noodle/noodle.go` 的前缀树候选路径及 `internal/hwlm/teddy/teddy.go` 均支持向调用方结果切片写入，消除候选起点的临时结果切片；尾部扫描同时覆盖原始与 ASCII 折叠首字节，FDR/Teddy/Noodle 统一保持候选确认、重叠和去重语义。Teddy 增加 `needsDedup` 与 `dedupBuf` 字段，仅在存在跨桶字面量时启用去重并复用键集合；`scanner.go` 为三种匹配器各自的 `Match` 缓冲建立 `sync.Pool`，`fastLiteral` 路径的 `candBuf` 容量从 16 提升到 64 元素，并在已知候选数后预分配 `matches` 切片。验证命令：`go test ./internal/hwlm/noodle ./internal/hwlm ./internal/hwlm/teddy ./internal/fdr ./internal/simd ./...`、`go test -run '^$' -bench 'ScanRuleScales' -benchtime=3x .`。本轮进一步把首字节单掩码扩展为最多 4 个 lane 的连续字节窗口掩码：`internal/hwlm/teddy/teddy.go` 与 `internal/hwlm/noodle/noodle.go` 以 `laneSets [4][4]uint64` 取代原 `firstSet`/`secondSet`，`lanes = min(最短文字长度, 4)`，大小写不敏感文字同时写入原始与 ASCII 折叠字节；新增 `Lanes()` 与窗口掩码计算函数，lane k 的字节掩码右移 k 位后与 lane 0 对齐相与，窗口末尾 `lanes-1` 个位置因缺少完整后继字节而退回首字节判定，避免跨窗口漏报，标量尾部继续按原始与折叠首字节筛选候选。测试：`internal/hwlm/teddy/teddy_mask_test.go` 与 `internal/hwlm/noodle/noodle_mask_test.go` 覆盖第二/第四 lane 过滤、最短文字长度约束、大小写折叠写入与 2 lane/4 lane 下与逐位置确认的暴力对照；`BenchmarkFindIntoByteMask` 在首字节全命中的 4KB 输入上多次采样测得 Teddy 约 560～583MB/s（单 lane 约 87～99MB/s）、Noodle 约 466～475MB/s（单 lane 约 58～60MB/s）。验证命令：`go test ./internal/hwlm/... ./internal/fdr/`、`go build ./...`、`go test ./...`、`go test -race ./...`、`go vet ./...`、`git diff --check`、`GOOS=linux GOARCH=amd64|arm64 go vet`。

K-21 本轮证据：`scanner.go` 在构造阶段缓存不可变扫描计划校验结果，Block `ScanInto` 进入执行前使用该错误结论，损坏规则索引或后端布局安全返回且不重复遍历整张图；后端直扫结果现在直接写入调用方结果切片，移除中间结果复制和区间转换临时切片；上下文 scratch/report 仍按既有生命周期复用。该轮曾引入 `ErrCancelled`、`Scanner.ScanContext`/`Scanner.ScanContextInto`/`scanContextInto` 与 `Scanner.cancelFlag` 的 context 取消语义（一次性 goroutine 监听 `ctx.Done()` 后原子置位，主扫描循环按 `start&1023 == 0` 周期探测）；维护者复核后确认该能力不在 Block 兼容范围内，已连同其专用支持代码（`cancelFlag` 字段、两处 `atomic.LoadUint32` 探测点、`sync/atomic` 导入、`context` 导入与 `scan_context_test.go`）整体删除，`scanInto` 恢复为无取消探测的单一主路径，K-21 仅保留扫描计划校验缓存与后端直扫写入这两项改动。验证命令：`go test ./...`、`go test -race ./...`。

K-22 本轮证据：`compile.go` 对图优化不收敛采用原图安全回退并记录 `optimization-fallback`，并将后端资源/布局失败产生的最终 bailout 元数据写回 `compiledRule.info`，保证诊断与实际执行一致。本轮完成三项业务改动：(1) `internal/nfagraph/rewrite.go` 的 `OptimizeWithLimit` 返回错误时携带触发震荡的最后修改 pass 名称（`lastChangingPass`）与轮数上限，便于按 pass 粒度记录 `optimization-fallback`；(2) `compile.go` 把 `Optimize` 返回的错误拼接到 `optimization-fallback` 元数据；(3) `internal/nfagraph/rewrite.go` 新增 `CostModel`、`NormalizeWithCost`、`OptimizeWithCostAndLimit` 三处业务方法：小图（节点数低于阈值）跳过 `BypassEpsilonJoins/SquashLinearJoins/SquashLinearLiterals/MergeEquivalentNodes` 昂贵 pass，保留 `RemoveRedundantEdges/PruneUnreachable/PruneDeadEnds` 低代价收益稳定的 pass；空 `CostModel{}` 等价旧 `NormalizeWithStats` 行为；阈值非零时按节点数决定是否应用合并/旁路；(4) `compile.go` 的资源预算循环按 `autoProgram.Kind` 分别累加 DFA 与 NFA 的 `MemoryBytes`：DFA 走 `autoProgram.DFA.MemoryBytes()`，NFA 走 `autoProgram.NFA.MemoryBytes()`，避免 KindNFA 路径下字节后端内存被遗漏、绕开限额校验。本轮新增 `bailout_test.go`：`TestBailoutReasonForGraphBuildFailure` 校验 UTF-8 + 边界图触发 `feature-gate` bailout 并被记录；`TestBailoutReasonForCombinedExpression` 校验组合表达式元数据保持 `Combination` 标志；`TestBailoutReasonForUnsupportedBackend` 校验大小写不敏感图保持可执行且不强制额外的 bailout 字段；`TestCompileAutoNFAProgramTracksMemory` 校验 `a+` 等纯文字重复图触发 NFA 后端并被 `Scan` 路径成功消费；`TestBailoutReasonForOptimizationFallbackCarriesPassName` 校验正常收敛图不携带 `optimization-fallback`。本轮新增 `internal/nfagraph/optimize_test.go`：`TestOptimizeWithLimitReportsLastChangedPassInError`、`TestOptimizeWithLimitRestoresGraphOnFailure`、`TestLastChangingPass`、`TestNormalizeWithCostSkipsExpensivePassesForSmallGraph`、`TestNormalizeWithCostAppliesExpensivePassesForLargeGraph`、`TestNormalizeWithCostDefaultEqualsNormalizeWithStats`、`TestOptimizeSucceedsWithinLimit`。验证命令：`go test ./...`、`go test -race ./...`。

K-20 本轮证据：修正 `internal/simd/arm64/arm64.go` 在 NEON 不可用时永 unreachable 的分支，并为 amd64 SSE2、AVX2、ARM64 NEON 增加双向量原生比较掩码；LimEx、稀疏、范围、半字节、表格、字节、Castle 和 Gough NFA 的整块 Spans 在入口完成布局校验后复用无校验确认路径，避免每个起点重复遍历状态表；Castle/Gough 与字节 NFA 的预算确认支持调用方结束偏移缓冲，减少多起点扫描分配；x86/ARM 原生比较路径继续受真实 CPU 能力门禁保护，缺少能力时使用展开标量回退。验证命令：`go test ./internal/simd ./internal/dispatch ./...`、`GOOS=linux GOARCH=amd64 go test -c ./internal/simd`、`GOOS=linux GOARCH=arm64 go test -c ./internal/simd`、`go vet ./...`。本轮继续补齐原生热路径与运行时覆盖语义：(1) `internal/simd/arm64/native_arm64.s` 新增 `nativeInRangeMask`，用一次 NEON 无符号闭区间比较（`VCMHS` + `VAND`）完成全部 16 字节筛选，再用 SWAR 乘加把每 8 个命中字节压缩成一个字节，直接返回 16 位位置掩码，避免比较结果回写内存和逐位循环；`internal/simd/arm64/arm64.go` 的 `InRangeMask` 在 NEON 层级改走该原生入口，`internal/simd/arm64/native_other.go` 为非 ARM64 平台保留通用实现，能力不足时仍回退展开标量路径。(2) `internal/dispatch/env.go` 新增运行时覆盖语义：`ParseTuneFamily`/`TuneFamily.String` 解析调优族名称，`ParseEnvOverrides` 解析 `SCANKIT_DISABLE_BACKENDS`、`SCANKIT_FORCE_BACKEND`、`SCANKIT_TUNE_FAMILY`，`EnvOverrides.Apply` 把禁用与强制后端写入注册表，`EnvOverrides.ApplyFeatures` 按调优族清除该微架构不具备的 AVX2/AVX512/VBMI 能力；`dispatch.defaultRegistryFromEnv` 与 `DefaultBackend` 接入这些覆盖，非法配置保留完整注册表继续安全回退。测试：`internal/simd/arm64/native_inrange_arm64_test.go`（全部 256×256 区间组合与 2 万个随机向量对齐通用实现、端点字节、位打包顺序，并给出原生/标量基准）、`internal/simd/backend_conformance_test.go` 新增 `TestBackendInRangeMaskCoversAllValues`（generic/x86/arm64 三后端全部区间组合逐位一致）、`internal/dispatch/env_test.go`（调优族往返与非法名称、禁用/强制/调优解析、注册表选择回退、调优族能力裁剪）。基准：`BenchmarkInRangeMask` 原生 1.99ns/op 对比展开标量 7.24ns/op（0 allocs）。验证命令：`go test ./internal/simd/... ./internal/dispatch/ ./internal/hwlm/...`、`go test ./...`、`go test -race ./...`、`go vet ./...`、`GOOS=linux GOARCH=amd64 go vet ./...`、`GOOS=linux GOARCH=arm64 go vet ./...`、`git diff --check`。尚未完成部分：x86 `ByteSetMask`/`InRangeMask` 仍为展开标量，AVX512/VBMI 专用算法与 SVE/SVE2 变长原生路径仍未实现。

K-20 补充证据（x86 原生掩码收敛 + 预编译字节集合）：新增 `internal/simd/byteset.go` 的 `ByteSet`/`NewByteSet`/`ByteSet.Mask`，把 256 位字节集合在配置期按高半字节切成 16 行 lo/hi 张量，并在 `simd.Backend` 上增加 `ByteSetMaskPrepared`，使热路径无需每个窗口重复解析原始位图。`internal/simd/x86/native_inrange_amd64.s` 新增 SSE2 `nativeInRangeMask`，用 `PSUBUSB` 饱和减法把 `lo<=v<=hi` 变成“两路饱和差同时为 0”，一次完成 16 字节闭区间判定；`internal/simd/x86/native_byteset_amd64.s` 新增 SSSE3 `nativeByteSetMask`，用两次 `PSHUFB` 取出该行 lo/hi 成员图、按低半字节高位选择后查位表，`PCMPEQB`+`PMOVMSKB` 输出位置掩码。`internal/simd/arm64/native_byteset_arm64.s` 新增 NEON `nativeByteSetMask`，用 `TBL` 取行、`CMHS` 选行、`USHL` 生成位选、`CMTST` 判定命中，再用 SWAR 乘加压缩为 16 位掩码。热点接入：`internal/hwlm/teddy/teddy.go` 与 `internal/hwlm/noodle/noodle.go` 在构建期把每个 lane 的集合编译成 `simd.ByteSet`，`windowMask` 全部改走 `ByteSetMaskPrepared`；`internal/fdr/fdr.go` 把根首字节集合从逐偏移调用提升为构建期解析的 `sensitiveSet`/`foldedSet`，避免每次窗口重复解析位图。`internal/nfa/engines.go` 与 `internal/rose/rose.go` 保留原始位图路径：这两处没有可复用的持有者，使用局部预编译集合会被接口调用提升到堆上，实测使固定语料分配从 97 升至 98，因此按“零分配回归优先”保留标量入口。测试：`internal/simd/byteset_test.go`、`internal/simd/arm64/native_byteset_arm64_test.go`（全部字节 × 全部 lane × 全部半字节行 + 2 万随机向量 + 端点字节）、`internal/simd/x86/native_amd64_test.go`（同上，并覆盖全部 256×256 区间组合）、`internal/simd/backend_conformance_test.go` 新增 `TestBackendPreparedByteSetCoversAllValues`/`TestBackendPreparedByteSetMatchesRaw`（generic/x86/arm64 三后端含 nil 集合）。基准：NEON 字节集合 1.68ns/op 对展开标量 7.30ns/op；SSE `PSHUFB` 2.52ns/op 对展开标量 14.50ns/op；SSE2 区间掩码 2.00ns/op 对展开标量 6.00ns/op（均 0 allocs）；HWLM Teddy 窗口掩码 530 MB/s 对通用标量 418 MB/s（0 allocs），Noodle `FindInto` 由 466~475 MB/s 提升到约 587 MB/s。验证环境：darwin/arm64 全量门禁，以及安装 Rosetta 2 后的 `GOOS=darwin GOARCH=amd64 go test ./...`（x86 SSE 原生路径真实执行）、`GOOS=darwin GOARCH=amd64 CGO_ENABLED=1 go test -race ./internal/simd/...`、`GOOS=linux GOARCH=amd64 go vet ./...`、`GOOS=linux GOARCH=arm64 go vet ./...`，并修正了 `nativeEqualMask`/`nativeEqualMaskAVX2`/`nativeByteSetMask` 的汇编帧大小（`$0-24` → `$0-18`）使 `go vet` 的汇编参数尺寸校验通过。同一轮把 ARM64 等值掩码从“写回向量再逐字节重建掩码”改为直接返回位置掩码：`internal/simd/arm64/native_arm64.s` 新增 `nativeEqualByteMask`、`nativeEqualByteMaskFold`（上下两种 ASCII 写法合并）、`nativeEqualMask`，均以 `VCMEQ` 比较后经 SWAR 乘加一次压缩成 16 位掩码；`internal/simd/arm64/arm64.go` 的 `EqualByteMask`/`EqualByteMaskFold`/`EqualMask` 改走这些入口，顺带消除了旧路径每次调用写回栈向量并逐位扫描的开销。测试：`internal/simd/arm64/native_equal_arm64_test.go`（全部候选值 × 全部 lane × 大小写/高位取反/零字节 + 2 万随机向量对），`internal/simd/backend_conformance_test.go` 新增 `TestBackendEqualMasksCoverAllValues`（generic/x86/arm64 三后端覆盖全部取值与 lane）。基准：`EqualByteMask` 主机分派由 14.24ns/op、32 B/op、2 allocs 降到 2.50ns/op、0 allocs，原生内核 1.32ns/op 对展开标量 9.55ns/op；`EqualMask` 0.99ns/op 对展开标量 4.77ns/op。仍未完成部分：AVX2/AVX512/VBMI 目前复用已验证的 128 位内核（SIMD 契约固定为 16 字节向量，更宽内核需先扩展向量契约），SVE/SVE2 变长路径受 Go 工具链限制（SVE 汇编仅在 `GOEXPERIMENT=simd` 实验特性下可用，本机无 SVE 硬件可验证）。

K-23 本轮证据：执行固定规则规模、NFA 布局和 SIMD 基准，记录 `ns/op`、`B/op`、`allocs/op`、状态/边/布局内存；当前仅形成基线，不设置绝对性能阈值。Rose 计划缓存、纯文字直接候选路径及状态缓冲池已移除调度队列重复构建；NFA 专用 Spans 复用结束偏移缓冲并跳过重复布局校验，Castle/Gough 预算路径也复用结束偏移。本轮把“固定 corpus”从一次性采样升级为可重复的回归治理：`conformance_matrix_test.go` 定义 12 条 Block 规则与确定性字节语料（含尾部截断、非对齐起点与跨窗口长前缀），`perf_baseline_test.go` 新增 `fixedBenchCorpus`（把同一语料确定性重复到约 64KiB 作为吞吐输入）、`BenchmarkFixedCorpusScan`（逐后端测量延迟、吞吐与分配）与 `TestFixedCorpusDeterministicMetrics`（固化命中集合、逐规则执行后端/状态数/布局内存与扫描分配上限）；时间指标只记录不设阈值，确定性指标进入断言，`performance-baseline.md` 已刷新为完整基线表与治理说明。本轮基线刷新：`ScanRuleScales` 在 1000 规则样本约 3.10ms、约 3.20MB 分配（分配 9 次）；`FixedCorpusScan` 主机后端约 92.6ms/64KiB（0.71 MB/s，20287 次分配）；NFA 专用引擎布局确认 9.8～19μs，状态缓冲池在 LimEx 上保持 7 allocs/op；Rose `ProgramFindMatches` 11.1μs 与 HWLM `FindAll` 67ns 维持稳定；最新基线已记录到 `performance-baseline.md`。验证命令：`go test -run '^$' -bench 'ScanRuleScales|EngineFamilies|ResourceUsage|ProgramFindMatches|FindAll|BackendEqualByteMask' -benchmem ./...`。

K-24 本轮证据：统一 Go conformance 已通过，并新增可重复的后端一致性矩阵。`internal/dispatch/override.go` 提供 `SetBackendOverride`/`BackendOverride`，允许在不改变编译期探测结果的前提下强制使用任意后端；`conformance_matrix_test.go` 用该入口把同一份规则与语料跑遍 generic、x86 六个能力层级（scalar/SSE/SSE4/AVX2/AVX512/AVX512VBMI）与 ARM64 四个能力层级（scalar/NEON/SVE/SVE2），并复核 Block 一致性用例（UTF-8、边界、反向引用、重复、模糊匹配、NUL、空匹配、大小写不敏感 UTF-8）与非法模式错误边界：`TestScanConformanceAcrossBackendMatrix` 校验 12 条规则的完整命中集合，`TestScanConformanceAcrossBackendMatrixWindows` 在逐个偏移截断后的 30 个窗口上复核一致，`TestScanFixturesAcrossBackendMatrix` 校验 fixture 与错误边界，`internal/simd/backend_conformance_test.go` 的 `TestBackendInRangeMaskCoversAllValues` 与 `internal/simd/arm64/native_inrange_arm64_test.go` 覆盖掩码层全部取值组合。跨架构部分仍需在目标架构执行，当前主机完成 amd64/arm64 交叉编译与静态检查，交叉产物不能在本机运行。验证命令：`go test ./...`、`go test -race ./...`、`go test -run TestScanConformance ./...`、`go vet ./...`、`GOOS=linux GOARCH=amd64 go vet ./...`、`GOOS=linux GOARCH=arm64 go vet ./...`。

K-25 本轮证据：`release-audit.md` 刷新为 2026-09-20 并新增本轮能力证据（HWLM 多 lane 掩码、Rose 角色 cost 与队列压缩、ARM64 NEON 原生区间掩码、分派调优/禁用覆盖、跨后端一致性矩阵与固定语料基线），平台构建核验更新为 `go test ./...`、`go test -race ./...`、`go vet ./...`、`git diff --check` 与 amd64/arm64 交叉静态检查，已知限制补充 x86 掩码与 AVX512/VBMI、SVE/SVE2 原生路径缺口；`api-exclude-audit.md` 复核本轮新增能力全部位于内部包、公开 `Engine`/`Scanner`/`Compiler` 导出方法不变，并以 `internal/contract` 快照测试为准；`release-runbook.md` 增加调优族与禁用后端环境变量、跨后端一致性复核命令、性能基线采集命令与确定性指标说明；`vectorscan-source-map.md` 补充 Dispatch/CPU 探测的源码对照行。发布门禁复跑结果：`go build ./...`、`go test ./...`、`go test -race ./...`、`go vet ./...`、`git diff --check`、`GOOS=linux GOARCH=amd64 go vet ./...`、`GOOS=linux GOARCH=arm64 go vet ./...` 全部通过；K-20 仍未收敛的原生覆盖缺口已在 `release-audit.md` 的已知限制中显式记录，最终结论维持“Block 模式兼容实现”。


K-20 第三轮证据（宽窗口契约 + 分层原生内核）：本轮把 SIMD 契约从"16 字节向量单点判定"扩展到"32 字节窗口一次判定"。(1) `internal/simd/byteset.go` 新增 `ByteSetTables`（4 个 lane 的 32 字节查找表连续存放）、`NewByteSetTables`、`ClampLanes`、`ComposeWindowMask`（lane 掩码右移对齐 + 窗口末端退让）与 `WindowMaskScalar` 标量参考实现，并在 `simd.Backend` 上新增 `WindowMask([]byte, int, *ByteSetTables, int) (uint32, bool)`：调用方一次调用即可完成全部 lane 的窗口判定，返回 32 位候选起点掩码，数据不足一个窗口时返回 false。(2) 原生内核：`internal/simd/x86/native_window_amd64.s` 新增 SSSE3 `nativeByteSetMask32`（两个 128 位半区分别 `PSHUFB` 半字节查表后拼接为 32 位掩码）与 AVX2 `nativeByteSetMask32AVX2`（`VPBROADCASTQ` 广播常量、`VINSERTI128` 复制行表、单个 256 位 `VPSHUFB`+`VPMOVMSKB` 直接产出 32 位掩码）；`internal/simd/arm64/native_window_arm64.s` 新增 NEON `nativeByteSetMask32`（`TBL`+`CMHS` 选行 + `USHL`/`CMTST` 判定，SWAR 乘加压成两个 16 位半区后合并）。(3) 热路径接入：`internal/hwlm/teddy/teddy.go` 与 `internal/hwlm/noodle/noodle.go` 把 matcher 预编译结果改为 `simd.ByteSetTables`，`windowMask` 收敛为 `backend.WindowMask` 单次调用，`FindInto` 步进与掩码宽度同步改为 32 字节/`uint32`（新增 `trailingZeros32`，删除仅为 16 位掩码服务的 `trailingZeros16`），顺带修正 teddy 侧"已构建 `prepared` 但热路径仍走原始位图"的悬空字段。(4) 每调用开销：x86/ARM64 后端新增 `resolved` 字段，在 `NewWithTier`/`New` 构建时按能力探测裁剪一次，热路径不再每次读取 CPU 能力（`TierOf` 语义保持不变，新增 `EffectiveTierOf` 暴露实际生效层级）。(5) 测试：`internal/simd/byteset_test.go` 新增 `TestWindowMaskScalarMatchesNaiveReference`（128 组随机 lane 集合 × 4 个偏移 × 1..4 lane 对逐位置参考实现）、`TestWindowMaskScalarEdgeCases`（空表、负偏移、短窗口、越界 lane）；`internal/simd/backend_conformance_test.go` 新增 `TestBackendWindowMaskMatchesScalar`（generic/x86/arm64 × 各 tier × 稀疏与稠密集合）与 `TestBackendWindowMaskCoversAllBytes`；`internal/simd/x86/native_amd64_test.go` 新增 `TestNativeByteSetMask32CoversAllValues`/`PinsBitOrder`/`RandomWindows` 以及 AVX2 的 `CoversAllValues`/`PinsBitOrder`/`MatchesSSE`/`TestBackendWindowMaskAVX2MatchesScalar`；`internal/simd/arm64/native_window_arm64_test.go` 新增同名 NEON 覆盖；`internal/hwlm/{teddy,noodle}/*_mask_test.go` 的窗口期望更新为 32 字节语义。(6) 验证路径：Rosetta 2 会在 CPUID 中隐藏 AVX/AVX2 但可正确执行 AVX2 指令，因此 AVX2 内核在 darwin/amd64 下真实执行验证（`SCANKIT_AVX2_TESTS=1 GOOS=darwin GOARCH=amd64 go test ./internal/simd/...`），而 AVX512/VBMI 探针在同一环境下直接触发非法指令，确认本机无 AVX512 执行能力。(7) 基准（darwin/arm64，M5 Pro）：`BenchmarkWindowMaskBackends` 中 arm64 lanes1 7506 MB/s 对 generic 1645 MB/s、lanes4 2727 MB/s 对 generic 416 MB/s；`BenchmarkWindowMaskClassifiers` 宿主后端 2416 MB/s 对 generic 415 MB/s（上一轮记录约 520 MB/s）；`BenchmarkFindIntoByteMask/second_mask` Teddy 1345 MB/s、Noodle 1087 MB/s（上一轮约 568 MB/s）；单次内核 NEON 32 字节 1.78ns、SSE 半区拼接 3.55ns（Rosetta 真实执行）；固定语料确定性指标保持 `allocs=97`、matches/backend 字符串不变。仍未完成部分：AVX512/VBMI 专用内核缺少可执行验证平台（Rosetta 仅支持到 AVX2，本机无 AVX512 主机），SVE/SVE2 变长路径受 Go 工具链限制（SVE 汇编仅在 `GOEXPERIMENT=simd` 下可用，且无 SVE 硬件可验证）。

K-20 第四轮证据（64 字节宽窗口落地 + 全热点接入）：本轮把宽窗口契约从 32 字节推进到 64 字节并接入全部热点。(1) 契约层：`internal/simd/supervector.go` 新增 `WideWidth = SuperWidth * 2`；`internal/simd/byteset.go` 新增 `ComposeWindowMask64`、`WindowMask64Scalar`、`GenericBackend.WindowMask64` 与 `FirstByteTables`（单集合只填 lane 0 的窗口表），`simd.Backend` 接口新增 `WindowMask64([]byte, int, *ByteSetTables, int) (uint64, bool)`，语义与 32 字节版本逐条对齐（lane 右移对齐、窗口末端退让、数据不足一个宽窗口返回 false）。(2) 原生内核：`internal/simd/x86/native_window_amd64.s` 新增 AVX512BW `nativeByteSetMask64AVX512`（`VPBROADCASTQ` 常量、`VINSERTI128`/`VINSERTI64X4` 复制半字节行表、`VPSHUFB` 双表查行、`VPCMPGTB`→`VPMOVM2B` 选行、`VPTESTMB`+`KMOVQ` 直接产出 64 位掩码）与 AVX512VBMI `nativeByteSetMask64VBMI`（`VPERMB` 以 `(低半字节第 3 位 << 4) | 高半字节` 为下标一次查全表）；`internal/simd/x86/wide_amd64.go` 新增 `WindowMask64` 分派与**运行时自检** `verifyWideKernel`：首次使用时用全部 256 个字节取值铺满四个宽窗口、对空集/全集/稀疏集与标量参考逐位比对，自检不过则永久退回 256 位拼接；`internal/simd/arm64/window_arm64.go` 与非 amd64 平台分别用两个已验证的 32 字节内核拼接与标量参考兜底。(3) 能力门禁：`internal/simd/x86/x86.go` 的 `detectedTier` 现在要求 `AVX512BW` 才给出 AVX512 层级，避免仅具备 AVX512F 的机器执行依赖 `VPSHUFB`/`VPTESTMB` 的内核。(4) 热点接入：`internal/hwlm/teddy/teddy.go`、`internal/hwlm/noodle/noodle.go`、`internal/rose/rose.go` 的候选枚举与 `internal/nfa/engines.go` 的 `forEachNFAStart` 全部改为"64 字节宽窗口 → 32 字节超向量窗口补齐 → 逐字节回退"的三段无缝平铺，并通过 `FirstByteTables` 复用预编译首字节表。(5) 尾零提取：把 `teddy`/`noodle`/`rose` 的手写 `trailingZeros32`/`trailingZeros64` 位循环替换为 `math/bits.TrailingZeros32/64`；实测手写位循环在稠密掩码（全命中）场景下占 `FindInto` 约 23% CPU，是宽窗口接入初期"宽窗口反而变慢"的真正原因，替换后四个热点全部快于 32 字节基线（见第 7 点）。(6) 测试：`internal/simd/byteset_test.go` 新增 `TestWindowMask64ScalarMatchesNaiveReference`/`EdgeCases`/`WithFirstByteTables`；`internal/simd/backend_conformance_test.go` 新增 `TestBackendWindowMask64MatchesScalar`/`CoversAllBytes`；`internal/simd/x86/native_amd64_test.go` 新增 `TestNativeByteSetMask64AVX512MatchesReference`/`VBMI`/`KernelsAgree`、`TestWideKernelSelfCheckPasses`、`TestBackendWindowMask64AVX2MatchesScalar`、`TestBackendWindowMask64AVX512MatchesScalar`（AVX512 用例按 `HasAVX512BW`/`HasAVX512VBMI` 自动跳过）；`internal/hwlm/{teddy,noodle}/*_mask_test.go` 新增 `TestFindMatchesWideWindowBoundary`（63/64/65/127/128/129/191/192/193 字节语料，命中刻意落在 0/64/128/190 等宽窗口起点）；`internal/rose/miracle_window_test.go` 新增 `TestMiracleWindowWideBoundaryCandidatesMatched`；`internal/nfa/first_tables_test.go` 的 0～80 字节全长度对照与 12 类引擎 conformance 覆盖 63/64/65。(7) 基准（darwin/arm64，M5 Pro，`-count 3`）：掩码内核 `WindowMask64` 对 `WindowMask` 同字节数为 arm64 lanes1 573ns/4KiB 对 819ns、lanes2 901ns 对 1243ns（约 1.4 倍）；端到端对比 32 字节基线：Teddy `second_mask` 3.5～4.2μs→1.8～2.0μs、`first_only` 44.6～47.0μs→31.1～32.1μs，Noodle `second_mask` 4.4～5.5μs→2.5～3.5μs、`first_only` 76.6～78.0μs→57.1～57.8μs，NFA `BenchmarkForEachNFAStartFirstByte/window32` 54.4～60.8μs→42.4～46.1μs，Rose `BenchmarkMiracleWindowCandidates` 190.8～199.7μs→155.2～157.7μs；固定语料确定性指标保持 `allocs=97`、matches/backend 字符串不变。(8) 验证命令：`go build ./...`、`go test ./...`、`go test -race ./...`、`go vet ./...`、`GOOS=linux GOARCH=amd64 go vet ./...`、`GOOS=linux GOARCH=arm64 go vet ./...`、`SCANKIT_AVX2_TESTS=1 GOOS=darwin GOARCH=amd64 CGO_ENABLED=0 go test ./...`（含 -race）、`git diff --check`。仍未完成部分：512 位内核经交叉编译与一次性运行时自检确认，但本机无可执行 AVX512 平台（Rosetta 的 AVX512 探针直接触发非法指令），因此仍属"构造正确 + 自检兜底"而非"本机实测"；SVE/SVE2 变长路径受 Go 工具链限制（SVE 汇编仅在 `GOEXPERIMENT=simd` 下可用，且无 SVE 硬件可验证）。

K-26 证据（Rose 候选路径可达性）：`internal/rose/rose.go` 的 `FindMatchesInto` 与 `FindMatchesRange` 在 `p.matcher == nil` 时无条件重建 `buildRoleMatcher`，而 `New` 在"全部角色都能由候选器直接定位"时正是把 `matcher` 显式置空（`scanner.go` 随后调用 `program.Normalize()`，`Normalize` 同样会重建匹配器），导致候选器路径在公共入口上永不可达、每个调用都重复构建一次通用多模式匹配器。(1) 业务改动：两处重建条件改为 `matcher == nil && !p.miracleReady`；把 `New` 与 `Normalize` 的派生状态（matcher、miracle、首字节集合与候选桶、预编译窗口表）收敛为唯一入口 `rebuildCandidateState`，消除两处推导分叉。(2) 测试：新增 `internal/rose/miracle_equivalence_test.go` 的 `TestMiracleCandidatesMatchMatcherEngine`（8 组角色配置 × 2000 组随机语料，覆盖锚定、末尾锚定、偏移上下界、需确认、大小写折叠与重复文字，差分对照通用多模式匹配器，并断言语料确实命中 64 字节宽窗口之外）与 `TestNormalizePreservesMiracleCandidatePath`；两个用例在恢复旧实现时会失败，已用变异验证确认其确实约束该行为。(3) 基准（darwin/arm64，M5 Pro）：同一 64KiB 语料下生产路径 `New`+`Normalize`+`FindMatches` 从 765.1μs 降至 309.1μs（约 2.5 倍），与该路径直调候选器（289.0μs）一致；固定语料确定性指标保持 `allocs=97`、matches/backend 字符串不变。

K-27 证据（工具链最低版本对齐）：GoLand 以 Go 1.26.4 工具链构建时报 `internal/simd/arm64/native_arm64.s:78: unrecognized instruction "VCMHS"`，`internal/simd/arm64` 整包无法汇编。定位过程：把 arm64 汇编器助记符表导出比对，Go 1.26 只有 66 条、Go 1.27 有 171 条，`VCMHS`/`VUSHL` 等无符号排序与变长移位助记符是 Go 1.27 新增；本机用 1.26.2/1.26.4/1.26.7 三个补丁版本复现同一失败，1.27.0 通过，说明差异来自小版本而非补丁版本。x86 侧 `internal/simd/x86/*.s` 在同一轮审计中确认全部落在 1.26 已支持集合内，只有 arm64 三处内核受影响。处理方案：按维护者决定把最低工具链提升到 Go 1.27，与内核实际依赖的指令集合保持一致，`go.mod` 的 `go 1.26` 改为 `go 1.27`。这样低版本工具链会在加载 `go.mod` 阶段直接报 `go: go.mod requires go >= 1.27`，而不是抛出难以定位的汇编错误；`GOTOOLCHAIN=auto`（默认）下 1.26.4 会自动切到 go1.27.0 并构建成功，已实测。备选方案（把 `VCMHS` 换成 `VUMAX`/`VUMIN`/`VCMEQ` 组合、把 `VUSHL` 换成 `VTBL` 半字节选择）经实测也能在 1.26 下汇编并通过 simd 测试，但需要额外处理 `lo > hi` 空区间的语义，本轮不采用。验证命令与结果：`go build ./...`、`go test -count=1 ./...`、`go test -race ./...`、`go vet ./...`、`GOOS=linux GOARCH=amd64 go vet ./...`、`GOOS=linux GOARCH=arm64 go vet ./...`、`git diff --check` 全部通过；Rosetta 下 `SCANKIT_AVX2_TESTS=1 GOOS=darwin GOARCH=amd64 CGO_ENABLED=0 go test -race -count=1 ./...` 通过；`TestFixedCorpusDeterministicMetrics` 仍为 `allocs=97`，命中集合与后端选择未变化；新增回归守卫 `toolchain_guard_test.go`：`TestArm64KernelsRequireGo127Toolchain` 解析 `go.mod` 的 `go` 指令并断言不低于 1.27，同时反向确认 `internal/simd/arm64/*.s` 中确实存在依赖 1.27 助记符（`VCMHS`/`VUSHL`）的内核，避免后续把工具链下限回退到低版本或在移除内核后留下失效断言；该守卫在任意平台（含 CI 的 amd64 runner）都会执行。arm64 内核基准复测 `BenchmarkInRangeMask` 2.14~2.23 ns/op（标量 8.0~8.3 ns/op）、`BenchmarkNativeByteSetMask` 1.40~1.49 ns/op（标量 7.7~7.8 ns/op）、`BenchmarkNativeByteSetMask32` 2.05~2.10 ns/op，与既有基线一致。

K-20 第五轮证据（收敛判定与 SVE/SVE2 显式排除）：本轮在 AVX512BW/VBMI 内核与 64 字节宽窗口全部落地后，对 K-20 判据中"NEON/SVE/SVE2"的 SVE 部分做了可执行性复核，结论是被三重外部条件阻塞，因此显式排除出完成判据并记录：(1) **工具链无法产出 SVE 汇编**：探针 `TEXT ·probe(SB), NOSPLIT, $0-0 / PTRUE P0.B, ALL / MOVD P0, R0 / RET` 在 `GOOS=darwin GOARCH=arm64`、`GOOS=linux GOARCH=arm64` 下均报 `unrecognized instruction "PTRUE"`，加 `GOEXPERIMENT=simd` 后结果相同——Go 的 `simd` 实验只提供固定宽度（128/256 位）`simd` 包 intrinsic，不含 SVE 变长汇编，因此 SVE 内核无法以任何形式编译进产物。(2) **运行时无法探测 SVE**：当前依赖的 `golang.org/x/sys/cpu` 的 `cpu.ARM64` 结构体只有 `HasFP`/`HasASIMD`/`HasAES`/… 等字段，没有 SVE 能力位，连"探测到 SVE 再分派"的门禁都无法实现。(3) **没有可验证硬件**：本机 Apple M5 Pro 仅有 NEON/ASIMD，SVE 需要 Graviton3、A64FX 一类 ARMv8.2+ 服务器核心；即使写出内核也无任何执行与差分验证路径。三点叠加意味着 SVE/SVE2 无法满足"测试通过、边界已验证"的完成要求，写出不可执行、不可验证的汇编只会引入未验证风险，因此按工程判断显式排除，并保持现有安全回退：任何未识别能力一律回退到 NEON 内核或通用标量参考实现（`GenericBackend.WindowMask`/`WindowMask64`）。覆盖影响评估：Go 把 ASIMD 视为 arm64 基线能力，Apple Silicon 与主流 arm64 服务器均具备，NEON 内核已覆盖全部实际 arm64 目标，因此排除 SVE 不降低实际覆盖。该排除是对 K-20 原始判据的修订，已同步记录在 `release-audit.md` 的已知限制中，并作为待维护者确认项保留；若后续要求补齐 SVE，需要的先决条件是：Go 汇编器支持 SVE 指令（或引入 `simd` 之外的 intrinsics）、依赖库提供 SVE 能力位、以及一台可执行 SVE 的验证主机。


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
K-20 第六轮证据（HWLM 扁平索引与确认程序回溯剪枝）：本轮针对 `BenchmarkPIIRedactionRules100/HighMatch/ScannerScanInto`（100 条带独立字段锚点的 PII 规则、169,316 字节语料、6,400 个真实命中）继续收敛每命中常数。该场景已从 `pii_log_bench_test.go` 整体迁出，独立承载于自包含的 `pii_rules100_bench_test.go`：规则构造、逐行语料、密度登记与字段锚点、裸值不命中、近失配不命中、语料与 Go regexp 一致性的全部测试都在该文件内，公共基准文件不再登记该场景、也不再暴露任何场景扩展钩子；字段锚点用 `(?:…)` 包住整条形态，否则 `a|b|c` 只锚定第一个分支，信用卡形态会退化成 2～3 字节必需文字并使扁平索引失效（实测该写法下 HighMatch 由 249,441 ns 退化到 653,093 ns）。业务代码改动集中在三处：(1) `internal/hwlm/noodle/flat.go` 新增 `flatTable` 定长哈希索引，对长度落在 `[8,16]` 且大小写敏感的文字把候选确认压缩成一次 8 字节主键读取加一次掩码比较；`internal/hwlm/noodle/noodle.go` 的 `New` 把 `newFlatTable` 提前到 lane 选择之前，并在扁平索引可用时把 `m.lanes` 固定为 1——扁平索引下被 lane 掩码挡下的位置本来也只需一次哈希查找，多 lane 每窗口成倍的查表开销无法由确认次数抵消。交替 A/B（每轮 `-benchtime=2000x -count=3` 取最小值）测得扁平平段 262,220 / 264,109 ns 对多 lane 309,558 / 314,144 ns，约 16% 提升。(2) `FindIntoUnsorted` 为扁平索引展开专用三段扫描 `flatTable.findInto`，去掉每条命中都要经过的闭包间接调用与 nil 判定，位迭代改用 `mask &= mask - 1`，主键探测内联为 `flatTable.lookup`；交替 A/B 得 252,420 ns 对 265,952 ns，约 5% 提升，NoMatch 场景 3,995 ns 对 4,002 ns 无回归。(3) `confirm_program.go` 新增按字节数上界的回溯点剪枝：`buildConfirmRemain` 在有界程序上求出每条指令到接受状态的最大可消费字节数，`run` 在已经找到命中后丢弃 `frame.pos+remain[frame.pc] <= best` 的回溯点；无上界重复（消费长度无上界，不只是指令图有回边）会显式禁用剪枝并保持 `remain == nil`，做法是在 `compileRepeat` 的两个 `v.Max < 0` 分支上置 `unbounded`。按形态微基准测得 `1[3-9][0-9]{9}` 型 11.88→12.22 ns、身份证型 36.16→31.38 ns（-13%）、信用卡型 17.02→14.26 ns（-16%）、手机型 27.70→27.14 ns，整场景交替 A/B 得 248,215 ns 对 251,483 ns。本轮同时对“每条指令必然消费的首字节”剪枝做了对照实现（不动点迭代求首字节包含集并在 `SetRepeat` 压栈处丢弃必然失败的回溯点），交替 A/B 测得 319,104 ns 对 306,347 ns 为退化，已完整回退，结论是首字节包含集在多 lane 掩码之后已无额外过滤收益。累计效果：同场景 `HighMatch` 从本轮起点的 310～314 µs（550 MB/s）降到 249,441 ns（678 MB/s，迁出后的自包含场景实测），`LowMatch` 2,623 MB/s、`NoMatch` 7,303 MB/s，三者均为 0 allocs；同一基准下 `EngineMask` 为 6,807 / 2,087 / 529 MB/s，Go `regexp.ReplaceAllFunc` 为 20,665 / 254 / 44.8 MB/s——命中存在时比 Go regexp 快 10～15 倍，1,000 MB/s 目标在 NoMatch、LowMatch 两档达成，只有 HighMatch 极端密度未达成。测试：`pii_rules100_bench_test.go`（`TestPIIRules100FieldAnchorsMatchOnlyOwnField`、`TestPIIRules100BareValuesDoNotMatch`、`TestPIIRules100NearMissValuesDoNotMatch`、`TestPIIRules100CorpusMatchesGoRegexp`、`TestPIIRules100CorpusShape` 与 `BenchmarkPIIRedactionRules100`）、`internal/hwlm/noodle/flat_test.go`（扁平索引可用性判定、与朴素前缀判定逐位置对照、缓冲区末尾定长回退、共享主键桶区分、开放寻址高装载与未知主键）、`confirm_program_test.go` 新增 `TestConfirmRemainBounds`（有界程序给出精确上界、含 `+` 的程序必须禁用剪枝）与 `TestConfirmProgramPruningKeepsPreferredEnd`（同一程序启用/禁用剪枝给出完全一致的偏好结束偏移）。验证命令：`go build ./...`、`go test -count=1 ./...`、`go test -race -count=1 ./...`、`go vet ./...`、`GOOS=linux GOARCH=amd64|arm64 go vet ./...`、`SCANKIT_AVX2_TESTS=1 GOOS=darwin GOARCH=amd64 CGO_ENABLED=0 go test -race -count=1 ./...`、`go test -run TestFixedCorpusDeterministicMetrics -count=1 .`、`gofmt -l .`、`git diff --check`。未达标说明：该场景 6,400 个命中摊到 169,316 字节上只有 26.5 字节/命中，1,000 MB/s 要求单命中总预算降到 26.5 ns，当前实测约 39 ns/命中。`cpuprofile`（`-benchtime=20000x`，扣除建表开销后扫描约占 5.0 s）拆分为候选定位约 7.8 ns/命中（窗口掩码 + 哈希探测，合计约 50 µs）与确认程序约 22.5 ns/命中（约 144 µs、平均 12.9 条虚机指令、约 1.6 ns/指令），两者合计已接近该形态的下限；在不引入并行扫描或按规则形态生成专用验证器的前提下，1,000 MB/s 无法到达，因此本轮只交付可验证的常数收敛与上述代价拆分，未把未验证的并行/专用内核写进主路径。
K-20 第七轮证据（PII 日志基准对照与候选跳过收敛）：本轮以 `BenchmarkPIIRedaction` 为入口，把本分支与 main 线的同名基准逐场景对照（两侧 `pii_log_bench_test.go` 字节级一致，差异全部来自引擎实现），定位并收敛了本分支明显落后的一段候选定位链路。`cpuprofile` 显示差距集中在 `internal/fdr.(*Matcher).FindIntoUnsorted`：该函数负责在整块日志上定位"必须文字"候选，`Email` 场景的必需文字只有 `@`（命中极稀），本分支却要为每个 64 字节窗口付一次掩码调用。四个根因与对应改动如下。
(1) **空自动机仍整块扫描**：`New` 分别为大小写敏感与折叠两棵自动机生成根首字节表，全为区分大小写的规则集会让折叠自动机没有任何转移，但 `appendMatches` 只用 `len(nodes) == 0` 判空，折叠树始终含根节点，于是空集合查找表仍按 64 字节窗口对整块缓冲区求掩码并"整窗跳过"，对 `Email/NoMatch` 白付约 2.2 µs/24.8 KB。改动：`rootSkip` 增加 `empty` 标志（根首字节集合为空即无任何命中），`appendMatches` 直接返回。
(2) **单字节根集合逐窗求掩码**：根首字节集合只有一个字节时，中间字节既不会离开根状态也不会启动任何文字，逐窗掩码（实测约 12 ns/窗口）远贵于一次单字节检索。改动：`rootSkip` 增加 `single`/`first`，此时用 `bytes.IndexByte` 直接跳到下一个候选位置，只有真正命中才进入自动机；多字节集合保留上一轮的窗口缓存。
(3) **有界类重复前缀把窗口内所有起点都展开**：`internal/prefilter` 已经能证明"必需文字左侧的每个字节都属于同一个有界类重复"（`Variant.Back`），`requiredScanStarts` 也据此把候选起点收缩到命中位置左侧的连续区间，但仍然把区间内每个起点都交给确认程序。区间内任意起点都会消耗到同一处必需文字，之后的求值路径完全一致，且确认程序命中后会写入 `blockedUntil` 抑制同规则的重叠起点，因此只有最左起点可能被采用。改动：`entry.back != nil && entry.max > entry.min` 时只派生最左起点（`Email/NoMatch` 的 32 个 `@` 命中由 128 个起点降到 32 个；`requiredScanStarts` 契约不变，超出上限回退与去重语义不变）。
(4) **候选起点按 16 字节结构体排序**：`requiredStart{start, rule}` 是 16 字节结构体，`slices.SortFunc` 要经比较器回调搬运；`AllPIITypes/NoMatch` 实测 522 个起点排序约 2.9 µs。改动：起点与规则下标打包进一个 `uint64`（起点在高 32 位、规则下标在低 32 位），改用 `slices.Sort` 的有序快路径，排序键与原有"起点升序、同起点按规则下标升序"的去重语义一致（`requiredStartKey`/`requiredStartParts`）。
同机 darwin/arm64（Apple M5 Pro）、两侧同参数 `-benchtime=30000x -count=3` 取中位数（ns/op，`EngineMask`）：

| 场景 | 改前 | 改后 | main 线 | 改后/改前 |
| --- | ---: | ---: | ---: | ---: |
| Email/NoMatch | 24,167 | 2,190 | 2,310 | 0.09 |
| Email/LowMatch | 24,129 | 2,403 | 2,372 | 0.10 |
| Email/HighMatch | 26,913 | 6,725 | 4,817 | 0.25 |
| ChineseID/NoMatch | 54,603 | 11,281 | 18,651 | 0.21 |
| ChineseID/LowMatch | 54,415 | 11,402 | 18,742 | 0.21 |
| ChineseID/HighMatch | 55,781 | 14,497 | 20,765 | 0.26 |
| AllPIITypes/NoMatch | 33,840 | 26,634 | 25,336 | 0.79 |
| AllPIITypes/LowMatch | 33,868 | 27,007 | 28,575 | 0.80 |
| AllPIITypes/HighMatch | 63,337 | 50,895 | 47,731 | 0.80 |

全部 24 个 `EngineMask` 场景中 21 个已不慢于 main 线（`Phone2/NoMatch` 0.51、`Phone3/NoMatch` 0.44、`CreditCard/NoMatch` 0.55），`Email/NoMatch` 从 10.7 倍落后收敛到 0.95 倍。`Rules100` 无回归：`HighMatch/EngineMask` 改动前后同参数测得 318,555 ns 与 317,266 ns（531～534 MB/s）。对照实现（第二处，已完整回退）：给 `confirmProgram` 增补"后继指令首字节集合"并在 `SetRepeat` 压栈处丢弃必然失败的次数，交替 A/B 测得 `Email/NoMatch` 2,307 ns 对 2,265 ns、`Email/HighMatch` 7,270 ns 对 7,060 ns，均为退化——32 字节的 `confirmByteSet` 逐次加载比一次栈帧压弹更贵，与第六轮"首字节包含集在多 lane 掩码之后已无额外过滤收益"的结论一致。
测试：`internal/fdr/fdr_test.go` 新增 `TestMatcherSkipPathsAgreeWithBruteForce`（单字节根、共享前缀、多字节根、仅折叠、空敏感树、空折叠树六种文字集合，对 0..len(corpus) 的每个长度与逐位置暴力匹配逐条比对）；`required_index_scan_test.go` 新增 `TestRequiredIndexBackPrefixKeepsLeftmostStart`（类重复前缀窗口只派生最左起点并校验解析回的 `(start, rule)`）与 `TestRequiredIndexBackPrefixAgreesWithPerStartScan`（6 条语料与禁用候选索引的逐起点确认路径逐条比对命中集合）。验证命令：`go build ./...`、`go test -count=1 ./...`、`go test -race -count=1 ./...`、`go vet ./...`、`GOOS=linux GOARCH=amd64|arm64 go vet ./...`、`SCANKIT_AVX2_TESTS=1 GOOS=darwin GOARCH=amd64 CGO_ENABLED=0 go test -race -count=1 ./...`、`go test -run TestFixedCorpusDeterministicMetrics -count=1 .`、`gofmt -l .`、`git diff --check`。残留差距：`Email/HighMatch` 仍为 1.40 倍（6,725 ns 对 4,817 ns），差值集中在确认程序——本分支是解释型回溯虚机（`Email` 形态约 40 条指令/命中），main 线是同规则编译出的验证 DFA（约 10～20 状态/命中）；本轮不动这条语义路径，只有在引入按规则形态生成验证器 DFA 时才能继续收敛。

K-20 第八轮证据（100 条混合规则的候选索引全覆盖、起点字节索引与线性排序）：本轮回到用户报告的 `BenchmarkHyperscanGo100Rules/Size=1024/EngineReplace`——100 条混合规则在 1 KiB 语料上只有 1,415,250 ns/op、0.72 MB/s，而同一语料上 Go `regexp` 交替式只要 1.09 ms/op，说明瓶颈不在正则语义而在候选定位链路。三个根因：(1) `buildRequiredIndex` 是"全有或全无"语义——100 条规则里 26 条提不出必须文字（实测改前候选索引只覆盖 74 条、`confirmUncoveredRules`=26，修复后降到 5）就让整个候选索引返回 nil（`requiredIndexEligible` 失败且 `backendEligible` 为假时直接 `return nil, nil`），扫描退化成"每个起点 × 每条规则"的通用确认路径；(2) `internal/prefilter` 无法为"类重复前缀 + 定长后缀"提取候选（`[A-Za-z0-9._/-]+\.go`、`rgb\([0-9]{1,3},`），并且会把可变宽度元素与其后元素拼接成并不必然出现的文字（`[0-9]{1,3}abc` 拼出 `0abc`…`9abc`）；(3) `requiredScanStarts` 的 Back 前缀逻辑只保留最左起点，最左起点被 `blockedUntil` 抑制后会漏掉区间内更靠右的真实命中（规则 16 `[A-Za-z0-9.-]+:[0-9]{1,5}` 对 `ipv6=2001:db8:…:7334` 漏报）。

业务代码改动集中在五处：

(1) `scanner.go` 的 `buildRequiredIndex` 改为部分覆盖语义（返回 `([]requiredLiteral, func, []bool)`），只跳过不可提取或窗口不可用的规则，并新增 `requiredWindowUsable`——`MaxOffset < MinOffset` 表示偏移无上界，必须携带 `Back` 左侧字节集合才接受；同时把 `fastLiteral`/`backendOnly`/`confirmAllRules`/`confirmUncoveredRules`/`startBytes`/`startBytesAll` 全部固化到 `Scanner`，避免每次扫描重新推导，并新增 `buildConfirmRuleLists`、`requiredIndexCovered`、`backendScanned`，让确认程序在候选驱动路径上只跳过真正被候选索引覆盖的入口。

(2) 新增 `scan_start_bytes.go`：`startByteIndex` 把仍需逐起点确认的规则按"首字节候选集合"分组为位图（`words[257×width]`，第 256 行是数据末尾起点，只放 `parser.Nullable(root)` 的规则），`startByteSet` 在可折叠大小写、编辑/汉明距离、`RequiresStatefulRuntime` 或 `FirstBytes` 为空时返回全集保守不过滤；`confirmIndexed`/`confirmCandidatesAndFallback` 用一次查表替换"每起点 × 每规则"遍历，并与候选起点做归并，保持与逐起点路径完全一致的起点顺序和 `ruleIndex` 升序。

(3) `internal/prefilter/required.go` 的 `expandPrefixSteps` 改为返回 `([][][]byte, bool)`，新增 `fixedWidth`，可变宽度节点不再继续拼接后续元素；`expandRepeatSteps` 用 `mustPrefix` + `reachedMin` 取代旧的 `prefixComplete`；`FromAST` 接受无上界窗口（`max < min && back != nil`）；`backByteSet` 允许 `repeat.Max < 0`。

(4) `requiredScanStarts` 在无上界窗口时取 `from = 0`，并在 Back 收缩后逐个枚举 `[from,to]` 而不是只保留最左起点。

(5) 候选起点排序改为两轮稳定计数排序：候选键是 `(起点, 规则下标)` 打包的 `uint64`，先按规则下标、再按起点各做一轮计数排序即可得到与 `slices.Sort` 相同的顺序，`sortRequiredStarts` 在候选 ≥ 8,192、≤ 2^21 且语料 ≤ 1 MiB 时启用，其余情形退回比较排序；缓冲（`requiredSortScratch`/`requiredStartCounts`/`requiredRuleCounts`）随 `scanContext` 复用。同一路径上还加了 `directMatchPrealloc` 上限：`simpleReports` 的直接结果切片只在候选 ≤ 65,536 时按候选数预分配，避免稀疏大语料按候选数量预留整块结果缓冲。

同机 darwin/arm64（Apple M5 Pro），`-benchtime=300x -count=5` 取中位数：

| 场景 | 改前 | 改后 | 改后吞吐 | Go regexp 对照 | 倍数 |
| --- | ---: | ---: | ---: | ---: | ---: |
| Size=1024 | 1,415,250 ns（用户报告，复现 1,387,071） | 93,287 ns | 10.98 MB/s | 0.94 MB/s | 11.7× |
| Size=10240 | 11,679,969 ns | 833,039 ns | 12.29 MB/s | 0.85 MB/s | 14.5× |
| Size=102400 | 112,792,089 ns | 7,699,068 ns | 13.30 MB/s | 0.82 MB/s | 16.1× |

三个尺寸的分配从 31 allocs/op 降到 6 allocs/op，命中数保持不变（26 / 257 / 2460）。计数排序单独贡献了候选密集档的提升：Size=10240 由 941,190 ns 降到 833,039 ns、Size=102400 由 9,294,681 ns 降到 7,699,068 ns（`slices.Sort` 在该场景的 `cpuprofile` 中占 10%）。同轮复核了 K-20 第六轮的 PII 场景：`BenchmarkPIIRedactionRules100/HighMatch/ScannerScanInto` 为 246,120～248,023 ns（683 MB/s、0 allocs），与第六轮的 249,441 ns（678 MB/s）持平，未见回归。

测试：`required_index_scan_test.go` 新增 `perStartReferenceScanner` 与 `assertScanAgreesWithReference`（禁用候选索引 + 后端整块扫描的逐起点参照），把 `TestRequiredIndexBackPrefixKeepsLeftmostStart` 替换为 `TestRequiredIndexBackPrefixBoundsWindow`（断言派生起点为 `[3,4,5,6,7]`），并新增 `TestRequiredIndexBackPrefixAfterSuppression`（规则 16 加哈希规则，覆盖最左起点被抑制后更靠右起点仍需命中）；`internal/prefilter/required_test.go` 为本轮新增语义补齐 6 个用例：字面量计划、无上界类重复前缀必须携带 `Back` 且集合内容逐字节校验、可变宽度前缀不得与后缀拼接（`[0-9]{1,3}abc` 必须只给出 `abc` 且窗口 `[1,3]`）、定长前缀仍可与类拼接（`abc[0-9]{1,3}` 窗口收敛为 0）、`rgb\([0-9]{1,3},` 的候选必须保持 `rgb(` 开头、以及纯无上界形态（`[a-z]+`、`(?:ab)+\.go`）必须拒绝。验证命令：`go build ./...`、`gofmt -l .`、`git diff --check`、`go vet ./...`、`GOOS=linux GOARCH=amd64|arm64 go vet ./...`、`go test -count=1 ./...`、`go test -race -count=1 ./...`、`SCANKIT_AVX2_TESTS=1 GOOS=darwin GOARCH=amd64 CGO_ENABLED=0 go test -race -count=1 ./...`、`go test -run TestFixedCorpusDeterministicMetrics -count=1 .`。

残留差距（本轮已验证但未继续收敛）：

- 候选起点仍按偏移窗口逐个展开（100 KiB 语料派生 242,358 个 `(起点,规则)` 对，展开 242,550 个后仅 192 个重复），`cpuprofile` 显示 `confirmCandidatesAndFallback` 占 22%、其中 `confirmProgram.run` 13%、`requiredScanStarts` 21%（teddy 定位 0.48 s + 排序 0.57 s，排序本轮已改为线性）。要再上一个台阶需要减少候选数量（更紧的起点推导）或按规则形态生成验证器。
- 大语料档（1 MiB、10 MiB）仍受 `scanContext` 的缓冲回收策略限制：候选数量超过 2^20 条时 `requiredStarts` 池化缓冲被丢弃并重新 append，扩容总分配量约为最终容量的 5 倍（实测 10 MiB 语料 24,635,232 条候选、单次扫描 `B/op` 约 1.95 GB）。本轮试过"重新分配前先统计精确条数再一次性 make"，但窗口推导必须抽出的辅助函数超出内联预算（`cannot inline requiredCandidateWindow: cost 113 exceeds budget 80`），三个尺寸反而退化 3%～7%，已回退；后续应改为保留缓冲或重构为流式归并，而不是重复扫描。
- 另有三组对照实验被回退：(a) 把 `sequencePlan` 改成只接受 `score > 0` 的候选，Size=1024 由 93,287 ns 退化到 105,212 ns（候选覆盖率下降）；(b) 为候选起点补一层"首字节集合"预筛（把首字节索引扩到全部需确认规则并在候选归并处跳过确定失败的候选），242,358 个候选中只有 8,083 个（3.3%）能被首字节排除，三个尺寸吞吐无变化——Back 前缀收缩出的起点本身就是类区间左端，其首字节天然落在规则首字节集合内，该层过滤没有收益；(c) 上述精确预分配方案。

K-20 第九轮证据（100 条混合规则的前缀字节约束枚举、候选文字拆分与入口文字误报修复）：上一轮把候选索引从"全有或全无"改为部分覆盖后，`BenchmarkHyperscanGo100Rules/EngineMask`（用户报告的 `EngineReplace` 同源场景）仍有约一半字节落在"必须文字只有单个字节"的候选上——100 条规则里 20 条只能提取到 `0`–`9`、`:`、`-`、`.`、`/`、`E`、`e` 之类的单字节文字，数字/字母在混合语料上的命中率约 10%，这些成员把 Teddy 的最短文字压到 1、lane 退化为 1，候选掩码几乎不过滤（`cpuprofile` 中 `teddy.FindIntoUnsorted.func1` 27.9%、`hwlm.ContainsAt` 13.4%）。

业务代码改动集中在七处：

(1) 新增 `scan_prefix_guard.go`：`prefixGuard` 保存规则在匹配起点之后必然消费的逐字节集合，`prefixSets` 从语法树推导（字面量、类、点号、序列、交替、有界重复；零宽结构不消费字节；大小写折叠、UTF-8/UCP、scoped flags、组合式与扩展规则一律放弃），`allows` 用 256 位查表在进入确认程序之前丢掉必然失败的起点。`longestEqualWindow` 取代原先要求"整条约束都是同一集合重复"的 `repeatedSet`：它返回约束中最长的同集合连续窗口 `(set, offset, length)`，于是 `[A-Z][0-9]{8}`（W=(1,8)）、`[0-9]{3}-[0-9]{2}-[0-9]{4}`（W=(7,4)）、`\b[0-9]{17}[0-9Xx]`（W=(0,17)）这类只有局部定长重复的形态也能派生候选。

(2) `scanner.go` 新增 `guardRun`/`guardRunGroup`：`guardRun` 记录 `(ruleIndex, set, offset, length)`，`guardRunFor` 用 `longestEqualWindow` 求窗口，`groupGuardRuns` 按字节集合合并同集合约束，`appendGuardRunStarts` 先对整块语料求出该集合的全部极大连续段，再把落在段内的窗口起点整段展开（`first = max(0, runStart-offset)` 到 `last = index-length-offset`），同集合的规则共享一次线性扫描。`guardRunCandidates` 选出应改由约束枚举的规则，`buildRequiredIndex` 把这些规则从候选文字索引中剔除，避免同一命中经两条来源重复展开；`requiredIndexDriven` 相应改为"必须文字索引或前缀约束任一存在"。随后把逐字节求段重写为宽窗口 SIMD 路径：`guardRunGroup` 预编译 `simd.ByteSetTables`，`appendStarts` 用 `WindowMask64(..., lanes=1)` 一次求出 64 字节窗口内的集合位掩码，再用段起点位 `mask &^ (mask<<1)` 与段终点位 `mask &^ (mask>>1)` 配对求极大连续段，跨窗口的唯一一段用 `runStart` 接力，不足一个宽窗口的尾部逐字节求掩码后走同一套配对逻辑，因此与完全逐字节的实现逐位一致；后端不支持宽窗口掩码时 `appendStartsScalar` 逐字节回退。`TestGuardRunStartsMatchesScalar` 用跨窗口、尾部不足一个宽窗口与多段三类语料对两条路径做差分。

(3) 候选文字按长度拆分（本轮收益最大的单项，单独贡献 26.8→42.8 MB/s）：`splitRequiredLiterals` 把候选文字分成 `short`（≤1 字节）与 `long`（≥2 字节），`newRequiredFinder` 分别构建两个子匹配器后依次调用；`newSingleByteFinder` 用 `simd.NewByteSetTables` + `dispatch.DefaultBackend().WindowMask64(..., lanes=1)` 做字节集合扫描，再用 256 项 `table[value] = literal.ID` 查表换算候选，省掉通用匹配器的分桶确认；`newRequiredFinder` 在 `long` 为空（候选文字全是单字节）时不再拆分，直接回退到 `newLiteralMatcherFinder(literals)`——此时拆分拿不到任何 lane 收益，通用匹配器在稀疏命中语料上的跳过能力反而更好；只有确实存在多字节成员时才分别构建 `newSingleByteFinder(short)` 与 `newLiteralMatcherFinder(long)`。多字节匹配器因此不再被单字节成员压到 1 lane。

(4) `guardRunOutshinesLiteral` 给出改用约束枚举的门槛：无法进入必须文字索引的规则一律改用约束枚举；能进入但候选文字全部 ≤2 字节（`guardRunShortLiteralBytes`）时，窗口长度先达到 `guardRunPromoteWindow`，再看集合宽度——成员数 ≤ `guardRunPromoteCardinality`（24）时直接改用约束枚举，集合更宽时必须靠窗口长度补偿，达到 `guardRunPromoteLongWindow`（16）才改用约束枚举。`guardRunPromoteWindow` 阈值扫描得 4→26.9、6→29.5、8→29.9、10→30.0、12→29.8、16→29.5 MB/s，最终取 8。宽度门槛与长度门槛在本轮收尾时用干净机器状态重扫（`BenchmarkHyperscanGo100Rules/Size=1024/EngineMask` 与 `BenchmarkPIIRedaction/AllPIITypes/NoMatch/EngineMask` 各 400 次）：宽度 16/20/24 分别得 3,768/3,638/3,902 ns 与 33,551/33,069/33,107 ns，28/32 得 3,757/3,955 ns 与 46,069/46,711 ns——混合场景不变而 PII 场景明显退化，故取 24；长度 8/12/16/20/24 分别得 3,687/3,689/3,766/3,722/10,781 ns 与 45,488/33,202/33,449/33,174/33,361 ns——长度 8 会把 PII 里某个窗口 8～11 的宽集合规则过度改判（45.5 µs 对 33.2 µs），长度 24 又放过了窗口 20 的 base64 规则（10.8 µs 对 3.7 µs），故取 16。长度门槛是本轮最大的单项修复：`[A-Za-z0-9+/]{20,}={0,2}`（第 35 条规则）把 `[A-Za-z0-9+/]` 展开成 62 条单字节候选文字，1 MiB 随机语料上独占 904,055/959,727 次候选命中，而块模式下每条规则一次匹配后会把后续起点整体压掉（`blockedUntil`），这些命中里绝大多数注定被丢弃。窗口 (0,20) 的集合成员数 64 超过原阈值 24，被误判为"宽集合不值得枚举"；改为"宽集合 + 长窗口"后该规则转入约束枚举，1 MiB 档候选命中从 959,727 降到 104,014、候选起点从 1,168,599 降到约 325,000。

(5) 前缀约束的使用位置按实测收益分级：约束过滤器在逐起点确认路径（`verify`）上是纯开销——PII 场景每个候选都要按整段约束重扫一遍却几乎不过滤，实测让 `BenchmarkPIIRedactionRules100/HighMatch` 从约 246 µs 退化到约 287 µs，而混合场景吞吐没有变化，因此该处调用整体删除；只有候选展开循环里"必须文字偏移窗口较宽"（`to > from`，即同一命中要摊开成多个起点）时才先做一次约束查表，这里它能过滤掉绝大多数伪起点（混合场景 16.1→8.9 µs，PII 场景因窗口为定长而被条件跳过，无退化）。

(6) `internal/prefilter/required.go` 的 `sequencePlan` 在逐单位 `expandSuffixSteps` 之外增加"取该元素起点处必然出现的文字"的后备候选（`leadingLiterals` + `zeroWidthNode`），`backByteSet` 改为对多元素前缀求字节超集并新增 `maxBackBytes` 上限：`[0-9]{4}(-[0-9]{4}){3}`、`[0-9]{4}/[0-9]{2}/[0-9]{2}`、`[0-9A-Fa-f]{8}-…` 这类长重复此前只能提取到单字节类成员，现在能提取到 `-`@[4,4]、`/`@[4,4]、`-`@[8,8] 这类高选择性分隔符（该场景命中位置从 480,749 降到 267,551、候选起点从 309,048 降到 246,117）。同轮的后缀拼前缀补丁（对 `[0-9]+x` 之类不可展开元素拼出 `version:v0`…`v9`）实测 44.7→42.4 MB/s 退化，已完整回退。

(7) `internal/hwlm/literals.go` 的 `Select` 增加文字数量门槛 `teddyLiteralLimit`（128）：候选文字数量很大且首字节分散时，Teddy 的掩码几乎不跳过任何位置，每个触发位置仍要在所属桶内线性确认，桶内成本随文字总数增长；超过阈值后改交前缀树。本轮 100 条规则的候选文字索引含 183 条多字节文字，其中大量是 `:0`–`:9`、`.0`–`.9`、`-0`–`-9`、`/0`–`/9` 这类"分隔符 + 数字"的二字节组合，而 `Select` 原先只因存在少数长文字（最长 14，`refresh-token:`、`localhost:0`）就选择 Teddy（Teddy 的 lane 数由最短文字决定，此处只有 2）。同一集合实测 Teddy 4.51 ms、FDR 3.27 ms、Noodle 2.27 ms（1 MiB 语料、50 次均值），改交前缀树后 `BenchmarkEngineScan/1MB` 从 14.9 ms 降到 12.9 ms。

同轮修复了一处由此暴露的误报：确认程序的入口文字跳过优化（`confirmSkip`）原先以 `requiredIndexCovered` 为条件，而该判定同时把"前缀约束枚举"的规则算作已覆盖；但约束枚举只校验约束窗口、并不校验规则入口文字，`1[3-9][0-9]{9}` 因此在 `request uuid=550e8400-e29b-41d4-a716-446655440000` 的 `-446655440000` 上误报（窗口 `[起点+2, 起点+11)` 全是数字，入口字节 `-` 被整体跳过）。修复是把条件收紧为只认必须文字索引覆盖（新增 `literalIndexCovered`），并新增回归测试 `TestRequiredIndexGuardRunKeepsEntryLiteral`（入口字节缺失必须不命中、入口字节存在必须命中、并与逐起点参照扫描逐位一致）；该测试在修复前稳定复现 `got=[{1 0 11}] want=[]`。另外把 `internal/hwlm/literals.go` 的 `ContainsAt` 按大小写敏感与否拆出独立分支（原实现把 `literal.CaseInsensitive` 判定放在逐字节循环内），命中密集场景 `ContainsAt` 的 flat 占比明显下降，四个尺寸同步受益。

同机 darwin/arm64（Apple M5 Pro）实测（中位数）：

| 场景 | 本轮起点 | 改后 | 改后吞吐 | Go regexp 对照 |
| --- | ---: | ---: | ---: | ---: |
| `BenchmarkHyperscanGo100Rules/Size=1024/EngineMask` | 1,415,250 ns | 3,679 ns | 278 MB/s（0 allocs） | 0.83 MB/s |
| `BenchmarkHyperscanGo100Rules/Size=10240/EngineMask` | 11,679,969 ns（第八轮起点） | 39,831 ns | 257 MB/s（0 allocs） | 0.82 MB/s |
| `BenchmarkHyperscanGo100Rules/Size=102400/EngineMask` | 7,699,068 ns（第八轮） | 603,419 ns | 170 MB/s | 0.80 MB/s |
| `BenchmarkHyperscanGo100Rules/Size=1048576/EngineMask` | — | 7,477,983 ns | 140 MB/s（0 allocs） | 0.83 MB/s |
| `BenchmarkEngineScan/1KB`（随机语料） | ~84,600 ns / 12.1 MB/s | 8.4～10.4 µs | 99～121 MB/s | — |
| `BenchmarkEngineScan/1MB`（随机语料） | 128,700,000 ns / 8.15 MB/s | 12.9～13.0 ms | 80～81 MB/s（1 alloc） | — |

四个尺寸的 `EngineMask` 命中数与改前逐条一致（1 KiB 26、10 KiB 257、100 KiB 2460、1 MiB 23219），未引入任何命中或漏报。同轮复核 K-20 第六轮的 PII 场景无回归：`BenchmarkPIIRedactionRules100/HighMatch/ScannerScanInto` 255.7 µs（662 MB/s、0 allocs，与第六轮的 249,441 ns／678 MB/s 在测量噪声内持平）、`LowMatch/ScannerScanInto` 12.0 µs（2,568 MB/s）、`NoMatch/ScannerScanInto` 3.40 µs（0 allocs）；`BenchmarkPIIRedaction/AllPIITypes/EngineMask` 的 No/Low/HighMatch 三档为 33,622 / 33,652 / 53,626 ns（0 allocs），`Email` 档 5,884 / 5,884 / 8,538 ns，与第八轮记录持平。

测试：新增 `scan_prefix_guard_test.go` 的 `TestPrefixGuardLongestEqualWindow`（同集合极大段、无约束、全零集合与 nil 接收者）；`required_index_scan_test.go` 新增 `TestRequiredIndexGuardRunWindowAgreesWithPerStartScan`、`TestRequiredIndexGuardRunWindowOffsets`、`TestRequiredIndexSplitsSingleByteLiterals`、`TestRequiredIndexGuardRunKeepsEntryLiteral` 与 `TestGuardRunStartsMatchesScalar`（宽窗口 SIMD 与逐字节回退在跨窗口、尾部不足一个宽窗口与多段语料上逐位一致），并让 `perStartReferenceScanner` 清空 `guardRunCovered` 以便与逐起点确认路径逐条比对；`internal/prefilter/required_test.go` 新增 `TestFromASTPrefersFixedSeparatorAfterUnexpandableRepeat`、`TestFromASTLeadingLiteralBounds`。验证命令：`go build ./...`、`go test -count=1 ./...`、`go test -race -count=1 ./...`、`go vet ./...`、`GOOS=linux GOARCH=amd64|arm64 go vet ./...`、`SCANKIT_AVX2_TESTS=1 GOOS=darwin GOARCH=amd64 CGO_ENABLED=0 go test -race -count=1 ./...`、`go test -run TestFixedCorpusDeterministicMetrics -count=1 .`（基线 12=11 保持不变）、`gofmt -l .`、`git diff --check`。

残留差距（本轮已验证但未继续收敛）：1 MiB 档 `cpuprofile` 仍以 `requiredScanStarts` 为主（约 59%，其中候选文字匹配约 16%、`appendStarts` 的起点展开约 13%、计数排序约 12%），其次是确认程序的 `confirmProgram.run`（约 16%）与逐候选 `verify`（约 19%）。候选起点约 32.5 万而真实命中只有约 2.7 万，压缩候选仍是主方向：`appendStarts` 目前把极大连续段内每一个窗口起点都展开成候选，而块模式下同一规则的后续起点都会被 `blockedUntil` 压掉，把展开推迟到确认阶段（按段惰性推进）可以省掉大部分候选与排序；`teddy`/`noodle` 的 lane 掩码仍只有 2–4 lane，`FindIntoUnsorted` 的 `visit` 内层确认是主要 flat 成本；把长候选文字再按长度分桶（让 2–3 字节成员不再压低其余文字的 lane 数）是本轮验证过方向但未取得稳定收益的下一步。
K-20 第十轮证据（前缀约束枚举的段内提前过滤与窗口展开常数收敛）：用户回到 `hyperscan_go_100_rules_scan_benchmark_test.go` 的 `BenchmarkEngineScan/1MB`（12,370,405 ns／84.76 MB/s、339,267 B/op）追问"没有优化的空间了吗"。本轮先修复了基准自身的一处失效：`hyperscan_go_100_rules_benchmark_test.go` 的 `Rules` 表此前被截断成只剩 1 条规则（`Samples` 仍是 100 条），100 条规则的对比场景退化成单规则扫描，实测数字与 339,267 B/op 这类记录无法复现；恢复 100 条规则后 1 MiB 档回到 12.4～13.1 ms，与用户观察一致。该基准的语料由 `math/rand/v2` 全局函数生成且未固定种子，每个进程的语料不同，因此下面所有数字都在同一台机器上以多次 `-count` 取区间给出。

改后 `cpuprofile`（`-benchtime 400x`）显示 `guardRunWindow.emitRange` 是最大单项，据此做了四处改动：

(1) `guardRunGroup` 新增 `minLength`（组内最短约束窗口长度），`emitRange` 在展开前先判断连续段长度：随机语料上 64 字节集合的极大连续段约 3 万个，而规则 34 `[A-Za-z0-9+/]{20,}={0,2}` 的窗口长 20，绝大多数短段对组内任何规则都放不下一个完整窗口，原先仍要逐规则调用 `emitRun` 并立即在 `count <= 0` 处返回——纯调用开销约 1.6 ms/op。整段跳过短于 `minLength` 的段后 `emitRun` 调用数下降约 5 倍，1 MiB 档从 12.4 ms 降到 11.1 ms。

(2) 把 `appendStarts` 里捕获局部切片变量的 `emit`/`window` 闭包换成 `guardRunEmitter`/`guardRunWindow` 两个结构体方法，候选键的批量追加改为一次容量检查加 `slices.Grow` 后按下标写入，并把 `limit` 检查从“每个起点”提升到“每段”，去掉逐键的分支与闭包间接写入。

(3) `guardRun` 增加 `guard` 字段，`needsGuardFilter`/`emitFiltered` 让约束枚举在等值窗口之外也校验规则的完整前缀约束。等值窗口只是前缀约束里最长的一段同集合窗口，窗口之外的固定位置同样能拒绝候选：规则 5 `[0-9A-Fa-f]{8}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{12}` 的等值窗口只有开头 8 个十六进制位，`appendStarts` 因此派生 16,217 个候选而真实命中只有 34 个；改用完整约束（32 个位置，其中 4 个位置固定为 `-`）逐点过滤后候选降到百位量级。同一改动也收紧了 `[A-Z][0-9]{8}`、`\b[0-9]{10}\b` 这类“定长窗口 + 前置类”规则的候选，`TestRequiredIndexGuardRunWindowOffsets` 的 "window inside long run" 期望值相应从 `[0..9]` 收紧到 `[0]`（其余起点连规则入口字节都不满足，改前要靠确认程序逐个否决）。1 MiB 档 11.1 ms → 10.7 ms。

(4) 候选文字展开路径的两处常数收敛：新增 `guardImpliedByBack`，当规则的前缀约束只校验首字节、且该字节集合与左侧回退集合完全一致时直接省略约束（`[A-Za-z0-9._/-]+:...`、`[A-Za-z0-9.-]+:[0-9]{1,5}` 这类规则在窗口内每个起点都已由回退扫描保证首字节归属，逐点查表是重复开销）；窗口展开循环里把 `ruleKey`/`guard` 提出循环、窗口退化为单起点时直接追加，避免 `to > from` 与 `len(starts) >= limit` 的逐键重复判定。

同机 darwin/arm64（Apple M5 Pro）实测（区间为多次 `-count` 的最小值～最大值）：

| 场景 | 第九轮 | 本轮 | 本轮吞吐 | 命中数 |
| --- | ---: | ---: | ---: | ---: |
| `BenchmarkEngineScan/1KB`（随机语料） | 8.4～10.4 µs | 6.99～9.36 µs | 110～146 MB/s | — |
| `BenchmarkEngineScan/1MB`（随机语料） | 12.9～13.0 ms | 10.84～10.98 ms | 95～96 MB/s | — |
| `BenchmarkHyperscanGo100Rules/Size=1024/EngineMask` | 3,679 ns | 2,986～3,071 ns | 333～343 MB/s | 26 |
| `BenchmarkHyperscanGo100Rules/Size=10240/EngineMask` | 39,831 ns | 32,550～33,230 ns | 308～315 MB/s | 257 |
| `BenchmarkHyperscanGo100Rules/Size=102400/EngineMask` | 603,419 ns | 512,216～515,346 ns | 199～200 MB/s | 2,460 |
| `BenchmarkHyperscanGo100Rules/Size=1048576/EngineMask` | 7,477,983 ns | 6,512,017 ns | 161 MB/s | 23,219 |

四个尺寸的命中数与第九轮逐条一致（26 / 257 / 2,460 / 23,219），未引入命中或漏报。`BenchmarkEngineScan/1MB` 相对用户提供的 12,370,405 ns／84.76 MB/s 约 1.14 倍；该基准的 339,267 B/op 是 b.N 较小时一次性初始化（约 34 MB 的候选缓冲与 `startCounts` 直方图）摊到每次迭代的结果，b.N 上千后稳态为 0～1 alloc、不足 0.2 KB/op。

同轮复核无回归：`BenchmarkPIIRedactionRules100/HighMatch/ScannerScanInto` 241,709～244,488 ns（692～700 MB/s、0 allocs，第九轮 255.7 µs／662 MB/s）、`NoMatch` 3,386～3,507 ns（0 allocs）；`TestFixedCorpusDeterministicMetrics` 基线 `12=11` 保持不变。

验证命令：`go build ./...`、`go test -count=1 ./...`、`go test -race -count=1 .`、`go vet ./...`、`GOOS=linux GOARCH=amd64|arm64 go vet ./...`、`GOOS=darwin GOARCH=amd64 go vet ./...`、`go test -run TestRequiredIndexGuardRunWindowOffsets -count=1 .`、`TestGuardRunStartsMatchesScalar`、`TestFixedCorpusDeterministicMetrics`、`gofmt -l .`、`git diff --check`。测试期望更新：`required_index_scan_test.go` 的 `TestRequiredIndexGuardRunWindowOffsets` "window inside long run" 由 `[0..9]` 收紧为 `[0]`（更强的前缀约束过滤，命中集合不变）。

残留差距（本轮已验证但未继续收敛）：改后 1 MiB 档 `cpuprofile` 已没有单一支配项——`confirmProgram.run` 约 1.9 ms（19%）、必须文字匹配约 2.8 ms、计数排序约 1.3 ms、候选展开约 1.7 ms、前缀约束枚举约 1.5 ms、逐候选 `verify` 约 0.7 ms。候选起点约 32 万而真实命中约 2.7 万（精度 8.5%），其中 62% 的候选会被 `blockedUntil` 直接压掉，继续压缩候选是唯一还有量级空间的方向：`appendStarts` 仍把极大连续段内每一个窗口起点都展开成候选（规则 97 `[A-Za-z0-9._/-]+:[A-Za-z0-9._-]+` 单独贡献约 13 万候选、规则 34 约 6 万），而块模式下同一规则在首个命中结束位置之后的起点必然被抑制；把窗口展开推迟到确认阶段、按规则维护区间流并在 `blockedUntil` 处跳跃，可以把展开与排序的工作量按“未被屏蔽的候选”而不是“全部窗口起点”计费，序列化上仍需保持 (起点, 规则下标) 输出顺序，预计可再省 1.5～2 ms（约 15%～20%）。该改动要把候选表示从“扁平键数组”扩展为“稀疏键 + 每规则区间流 + 归并”，属于结构性改动，本轮未实施。另一处已验证但未收敛的方向是词边界断言：规则 30～33（`\b[0-9a-fA-F]{32}\b` 等）在长十六进制串内派生的约 2.4 万个候选几乎全部被末尾 `\b` 否决，把断言折算成“窗口后一字节不属于 word 集合”的字节约束可以再省约 0.8 ms。

K-20 第十一轮证据（词边界断言折算 + 候选排序收敛 + 排序缺口的漏报修复）：本轮先按第十轮记录的残留方向实施词边界断言折算，随后对候选起点整理与展开做常数收敛，并在过程中发现并修复了一处真实漏报。

(1) 词边界断言折算（`scan_prefix_guard.go`）：`prefixGuard` 新增 `before`/`hasBefore`/`after`/`afterOffset`/`hasAfter`，`newPrefixGuard` 调用 `applyWordBoundaryConstraints` 把模式首尾的 `\b` 折算成相邻字节约束。只有当断言紧邻必然消费的字节、且该首/末字节集合是单词字节集合（`[0-9A-Za-z_]`）的子集时才折算：`firstByteSet` 求断言右侧的首字节集合，`fixedTailByteSet`/`fixedConsume` 只在尾部长度固定时给出 `afterOffset`，可变长度重复（`\b[A-Za-z]+\b`）一律放弃。`allowsBoundary` 只校验起点前一字节与固定偏移处的字节，越界一侧视为满足；`allows` 与 `allowsBoundary` 均先走这条快速否决。规则 30～33（`\b[0-9a-fA-F]{32}\b` 等）在长十六进制串内派生的约 2.4 万个候选因此不再进入后续确认。

(2) 约束枚举尾部收敛（`scanner.go`）：`guardRun` 新增 `prefixCovered`（窗口恰好覆盖全部前缀字节集合），`needsGuardFilter` 与 `guardImpliedByBack` 对携带相邻字节约束的规则保留过滤，`emitFiltered` 新增 `boundaryOnly` 分支——窗口已覆盖全部前缀集合时只调 `allowsBoundary`，不再逐候选重扫整段前缀集合。`guardRunGroup` 的 `minLength` 换成把 `runs` 按窗口长度升序排列，`emitRange` 在连续段短于当前规则窗口时提前结束整段展开。

(3) 候选起点整理（`scanner.go`）：`sortRequiredStarts` 从"两轮计数排序"改写为自适应分桶加桶内插入排序——桶跨度按候选密度取 `shift`（上限 `requiredBucketMaxShift=16`），单轮直方图后一次 scatter 写入复用缓冲，桶内平均不超过两个候选，插入排序即可恢复完整顺序；`requiredStartCounts` 不再随语料长度线性增长，`requiredRuleCounts` 字段成为死字段并删除。真实候选集合（269,113 个键、随机打乱）的独立微基准：`slices.Sort` 12.5 ms、原两轮计数排序 1.33 ms、分桶加插入排序 0.48 ms。

(4) 漏报修复：第十轮把排序移到"候选文字与约束枚举合并之后"，纯约束枚举（`requiredFindInto == nil`）分支却直接返回了未排序的起点。该分支自身按组追加候选，顺序是"集合分组顺序"而不是起点顺序，一旦同时存在需要首字节索引的未覆盖规则，`confirmCandidatesAndFallback` 的归并游标就会错过出现在数据靠前位置的约束候选。复现规则集 `\b[0-9]{10}\b` + `\b[0-9a-fA-F]{32}\b` + `(?i)[a-z]{5}`（前两条由约束枚举定位、第三条落在首字节索引），语料 `aa deadbeef…(32 位十六进制) bb 1380013800 cc HELLO dd` 修复前丢失十六进制命中，修复后命中与逐起点参考扫描逐位一致。修复方式是把约束枚举并入统一的整理流程，两条来源共用同一个排序与去重出口。

新增测试：`scan_prefix_guard_test.go` 的 `TestPrefixGuardWordBoundaryConstraints`（含 `\b[0-9]{10}\b` 的 `afterOffset=10`、`\b[0-9a-fA-F]{128}\b` 在 32 字节约束上限下仍给出 `afterOffset=128`、`\bcat\b`、`\b[A-Za-z]+\b` 不折算尾部、`(?i)\bcat\b` 不建立约束）、`TestPrefixGuardWordBoundaryAllows`（两侧分隔符、数据两端越界、左/右邻单词字节否决）、`TestPrefixGuardBoundaryOnlyPathAgreesWithUnguardedScan`（断言三条规则确实走 `prefixCovered` 且携带边界约束，并与关闭约束的扫描逐位比对）；`required_index_scan_test.go` 的 `TestSortRequiredStartsMatchesSlicesSort`（8 组键数/语料长度组合覆盖比较排序与分桶两条分支、同起点多规则、超键上限与超语料上限回退）、`TestSortRequiredStartsEndOfDataStart`（起点取到数据末尾仍在桶范围内）、`TestRequiredIndexGuardRunMergesWithStartByteIndex`（上述漏报的回归用例，用 `perStartReferenceScanner` 作为独立参考）。

同机 darwin/arm64（Apple M5 Pro）实测：`BenchmarkEngineScan/1MB` 为 10.60～10.83 ms（96.8～98.9 MB/s、1 allocs/op），第十轮为 10.84～10.98 ms；`BenchmarkEngineScan/1KB` 为 6.3～9.5 µs。命中集合与第十轮逐条一致，`TestFixedCorpusDeterministicMetrics` 基线 `12=11` 不变。

验证命令：`go build ./...`、`go test -count=1 ./...`、`go test -race -count=1 ./...`、`go vet ./...`、`GOOS=linux|darwin GOARCH=amd64|arm64 go vet ./...`、`SCANKIT_AVX2_TESTS=1 GOOS=darwin GOARCH=amd64 CGO_ENABLED=0 go test -count=1 ./internal/simd/... ./internal/dispatch/...`、`SCANKIT_AVX2_TESTS=1 GOOS=darwin GOARCH=amd64 CGO_ENABLED=0 go test -count=1 .`、`gofmt -l .`、`git diff --check`。

残留差距（本轮实测，方向不变但分布已更新）：100 条规则 1 MiB 随机语料的 `cpuprofile`（338 次迭代、5.76 s 采样）中，`requiredScanStarts` 占 2.96 s，其中必须文字匹配 1.22 s、约束枚举 0.60 s、分桶排序 0.49 s、展开与左侧回退扫描约 0.6 s；`confirmCandidatesAndFallback` 占 1.23 s，其中 `confirmProgram.run` 1.08 s。单次扫描派生候选键 270,809 个（必须文字展开 208,708、约束枚举 62,101，另有 10 条规则由约束枚举覆盖），远多于真实命中，"按未被 `blockedUntil` 屏蔽的候选计费"仍是唯一还有量级空间的方向：把极大连续段内的逐起点展开推迟到确认阶段、按规则维护区间流并在 `blockedUntil` 处跳跃，再用归并保持 (起点, 规则下标) 顺序，预计可再省 15%～20%；该改动要把候选表示从扁平键数组扩展为"稀疏键 + 每规则区间流 + 归并"，属于结构性改动，本轮未实施。

- K 阶段现为 27 项源码级收敛任务；K-01～K-27 全部收敛（K-20 在第五轮证据中把 SVE/SVE2 以工具链与硬件三重外部阻塞为由显式排除，该修订待维护者确认）。K-20 的 x86 SSE2/SSSE3 掩码缺口与"宽窗口契约"已在本轮收敛：SIMD 契约扩展为 64 字节宽窗口（`simd.ByteSetTables`/`Backend.WindowMask`/`Backend.WindowMask64`），SSSE3 半区拼接、NEON 合并、AVX2 256 位内核与 AVX512BW/VBMI 512 位内核均已落地，Teddy/Noodle/Rose/NFA 四个热点全部接入三段无缝平铺并复用预编译首字节表；除 512 位内核外均在 darwin/amd64（Rosetta 真实执行）与 darwin/arm64 上通过差分测试，AVX2 内核另有 `SCANKIT_AVX2_TESTS=1` 显式开启的直接执行验证，512 位内核经交叉编译、穷举字节的运行时自检与标量参考比对把关。剩余未收敛部分为 AVX512/VBMI 专用内核与 SVE/SVE2 变长路径：前者缺少可执行验证平台（Rosetta 的 AVX512 探针直接触发非法指令，本机无 AVX512 主机），后者受 Go 工具链限制（SVE 汇编仅在 `GOEXPERIMENT=simd` 下可用，且无 SVE 硬件可验证）；两者均不改变语义一致性，但属于"完整复刻"前必须补齐或显式排除的原生覆盖。该缺口已在发布审计的已知限制中记录；第五轮进一步确认 SVE/SVE2 属于工具链无法汇编、依赖无法探测、硬件无法验证的三重外部阻塞，因此显式排除出 K-20 完成判据（AVX512BW/VBMI 已实现并通过交叉编译与运行时自检，仅缺本机实测平台）。已完成项均有对应业务代码改动：本轮在 `internal/nfa/engines.go` 的 LBR 主路径新增 `SpansInto` 与 `MatchAtRangeInto`（K-15），把 `selectTableKind` 的 Sheng/Shufti/Tamarama 终选改为按 `estimateEngineCost` 选最低代价（K-16），在 `compile.go` 的资源预算循环按 `autoProgram.Kind` 分支累加 DFA 与 NFA 后端 `MemoryBytes`（K-22），新增 `CostModel`/`NormalizeWithCost`/`OptimizeWithCostAndLimit` 与 `lastChangingPass`（K-22），在 `internal/nfagraph/rewrite.go` 的 `OptimizeWithLimit` 错误中携带触发震荡的 pass 名称（K-22），K-21 曾提供的 `ErrCancelled`/`ScanContext`/`ScanContextInto`/`scanContextInto` context 取消语义已按维护者要求连同其专用支持代码整体删除；本轮 K-17 在 `internal/dfa/dfa.go` 新增状态级 `Reports`/`ReportsEOD` 报告传播与末尾断言判定，在 `Minimize` 按报告集合划分等价类，在 `internal/dfa/rdfa.go` 让反向接受报告集合与前向一致，并在 `internal/engine/engine.go`、`internal/nfa/nfa.go`、`scanner.go` 接入后端报告元数据与末尾报告门禁。本轮 K-18 在 `internal/hwlm/teddy/teddy.go`、`internal/hwlm/noodle/noodle.go` 把首字节单掩码升级为最多 4 lane 的连续字节窗口掩码，并在 `internal/hwlm/teddy/teddy_mask_test.go`、`internal/hwlm/noodle/noodle_mask_test.go` 补齐全量对照与基准；本轮 K-19 在 `internal/rose/rose.go` 增加角色 cost 模型、按报告编号的代价索引、队列批量入队与 `Queue.Compact` 压缩，本轮 K-23/K-24 在 `perf_baseline_test.go`、`conformance_matrix_test.go` 与 `internal/dispatch/override.go` 增加固定语料回归门禁、本轮 K-25 刷新 `release-audit.md`、`api-exclude-audit.md`、`release-runbook.md`、`vectorscan-source-map.md`，把本轮代码与测试证据、交叉平台核验命令、回滚与调优开关写入发布审计闭环；跨后端一致性矩阵与运行时后端覆盖入口，配套基线记录在 `performance-baseline.md`；并在 `scanner.go` 增加等价角色合并与按代价选择确认代表角色，配套测试位于 `internal/rose/role_cost_test.go`、`internal/rose/queue_compact_test.go` 与 `rose_role_selection_test.go`；因此“任务已补齐”仅表示清单覆盖完整，已完成项仍以业务代码改动和测试为准。

## 5. 收口条件

1. A～J 的基础任务与 K 的源码级补充任务均须为`已完成`，且每项已填写实际文件、验证命令和结果证据。
2. `X-02~X-05` 在未获得明确范围变更前始终保持排除，不计入完成度。
3. 所有专用后端均对非法图、布局损坏、资源超限和不支持平台安全回退。
4. `go test ./...`、`go test -race ./...`、`go vet ./...`、`git diff --check` 和已定义基准门禁全部通过。
5. 仅在第 4 节及 K 阶段所有非排除项完成且源码级平台决策已明确时，才可使用“完整复刻”表述；否则最终结论只能为“Block 模式兼容实现”。
