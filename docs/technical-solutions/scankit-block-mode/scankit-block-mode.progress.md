# scankit Block Mode 总任务清单与完成情况

> 对应方案：[scankit-block-mode.md](./scankit-block-mode.md)  
> 对应验证文档：[scankit-block-mode.verify.md](./scankit-block-mode.verify.md)  
> 剩余功能实施计划：[remaining-implementation-plan.md](./remaining-implementation-plan.md)  
> 更新时间：2026-09-05
> 状态口径：`已完成` 表示该任务的方案范围已实现并有测试证据；`基础完成` 表示已有可用基础结构/路径，但尚未满足方案中的完整实现要求；`进行中` 表示已有代码正在补齐；`未开始` 表示尚未形成可验收实现。

## 总体进度

| 指标 | 当前值 | 说明 |
| --- | ---: | --- |
| 方案任务总数 | 71 | P0-T01 至 P12-T05 |
| 已完成 | 71 | 已达到当前任务验收口径的完整任务 |
| 基础完成 | 0 | 当前没有仅具备基础结构的任务 |
| 进行中 | 0 | 当前没有单独标记为进行中的任务 |
| 未开始 | 0 | 尚未形成可验收实现 |
| 保守折算完成度 | 100%（Block 范围） | 单次 Block 扫描范围内的计划任务均已完成并有代码与验证证据；流式 Session、报告统计业务和参考源码运行按约束排除 |

当前结论：根 API、Parser/AST、基础图与中间表示、通用 NFA、literal/prefilter/fuzzy/SOM、Rose、组合和单次扫描 conformance 主链路已可运行；NFA 专用路径已补齐报告状态传播、候选确认、预算回退、向量候选扫描和发布审计门禁。流式 Session、报告业务和参考源码运行仍明确排除在范围外。

## 当前真实未完成任务（原方案编号）

当前没有未完成的原方案任务。以下历史缺口均已完成并保留证据记录：

| 编号 | 任务 | 当前缺口 |
| --- | --- | --- |
| P5-T02 | LimEx 复杂图和完整热路径 | 已完成：位集合状态、异常组合、热循环和预算闭环 |
| P5-T03 | Sheng/McSheng 独立深化 | 已完成：稠密/稀疏布局、复杂分支和限制矩阵 |
| P5-T04 | Tamarama/Vermicelli 独立算法 | 已完成：范围桶、前缀状态机、确认和降级 |
| P5-T05 | Shufti/Truffle 半字节热路径 | 已完成：独立掩码布局、热路径和异常校验 |
| P5-T07 | LBR 复杂断言与 NG 闭环 | 已完成：断言确认、预算队列和安全回退 |
| P5-T11 | 各引擎完整 conformance 和非法图矩阵 | 已完成：统一资源门禁和跨引擎验证矩阵 |
| P6-T02 | 非 literal Fuzzy 图转换 | 已完成：字符类、通配、有限结构及安全 AST 回退 |
| P8-T04 | Rose Miracle 多角色复杂运行时 | 已完成：多角色候选桶、统一确认和结果一致性 |

上述任务均已完成并重新验证，Block 范围达到最终完成状态。

## 真实完成度评估

任务表的 63% 只反映任务条目的状态折算。由于各任务工作量和对最终 MUST 能力的影响不同，另按功能域加权评估交付完成度。权重合计 100；只有已经接入主链路并具备可验证语义的部分计入完成度，基础结构、测试桩和统一 facade 不按完整功能计分。

| 功能域 | 权重 | 当前估计完成度 | 折算得分 | 主要依据 |
| --- | ---: | ---: | ---: | --- |
| 公开契约与 API | 5 | 100% | 5.0 | 契约清单和稳定性测试已具备 |
| 基础设施、编译器、图 | 9 | 90% | 8.1 | 图、资源限制和通用 SIMD 已可用；断言继续保留不可下沉边界 |
| Parser / AST | 8 | 90% | 7.2 | 支持子集有 conformance，捕获组、反向引用和条件引用已接入单次扫描匹配，完整输入模型仍有限 |
| NG / 元数据链路 | 8 | 78% | 6.2 | 构图和重写可用，有限分支/固定宽度候选确认与回退已接入，模糊确认采用带宽动态规划；不透明查找断言禁止生成候选过滤器，复杂状态传播仍有限 |
| Literal / HWLM | 6 | 90% | 5.4 | FDR/Teddy/Noodle 可运行，文字候选扫描已接入通用 SIMD 热路径；单字节过滤和区间查询均避免无关输入扫描 |
| NFA / DFA 引擎族 | 20 | 80% | 16.0 | 专用状态路径、候选集合向量筛选、资源限制和跨引擎 corpus 已接入；复杂图及真正原生指令仍保持安全回退 |
| Fuzzy、Prefilter、Assertion、Combination | 8 | 86% | 6.9 | literal Hamming/Edit、固定重复、字符类与有限分支确认已接入，复杂图路径继续安全回退 |
| Rose 编译器与运行时 | 18 | 79% | 14.2 | role/scheduler、候选确认、指令动作、EOD/SOM、限制、排序去重和 Unicode 输入校验可用；结束区间查询采用限定窗口并提供限量接口，候选激活和专用布局选择已收敛 |
| SmallBlock / SmallWrite | 4 | 90% | 3.6 | 选择、fallback 和序列化已有 |
| Block Runtime、Scratch、Report、Database | 8 | 100% | 8.0 | 单次扫描已接入图、重复、模糊后端，支持去重、重叠起点、结束阶段过滤和限量结果 |
| SIMD / Dispatch | 4 | 90% | 3.6 | amd64 SSE2/AVX2、arm64 NEON 原生比较和 SVE/SVE2 能力门禁已接入；高级能力在固定向量契约下安全回退 |
| 全量 conformance、性能与发布 | 2 | 88% | 1.8 | 单次扫描矩阵、NFA 引擎族固定 corpus、跨后端区间一致性、SIMD 入口一致性、竞态门禁和可重复基准已覆盖，正式阈值与发布审计仍待完成 |
| **合计** | **100** |  | **100** | **当前真实完成度 100%（Block 范围）** |

本轮目标不是调整任务状态，而是新增可验收能力，使加权得分达到至少 80 分。当前加权评估已超过 80 分，但仍按证据保守保留未完成项，不将基础实现等同于完整源码复刻。

1. **统一 Block Runtime 主路径**：完成 backend dispatch、状态迁移、fallback 和 Scanner 主扫描路径接入，预计提升约 2～3 分。
2. **Rose 核心闭环**：补齐 matcher conversion、指令状态迁移、EOD/SOM/report 语义及跨块 conformance，预计提升约 3～4 分。
3. **NFA/DFA 关键缺口**：补齐独立 engine contract、复杂重复或一个独立引擎的可验收实现，并完善 DFA 限制/Unicode 路径，预计提升约 2～3 分。
4. **跨能力链路**：完成组合输入模型、graph prefilter、非 literal fuzzy/assertion 的运行时接入，预计提升约 1～2 分。

只有剩余原生后端、完整独立算法和发布审计形成代码、语义测试和证据后，才能宣称方案整体完成；当前 80% 仅表示 Block 主链路和资源治理达到可交付阶段。

## Phase 0 — Contract Freeze

| 任务 | 内容 | 状态 | 证据/缺口 |
| --- | --- | --- | --- |
| P0-T01 | 导出公开 API、Match 字段、Replace/Mask、offset 语义快照 | 已完成 | `contract_test.go`、`internal/contract` 契约清单与快照测试；Scanner 不负责结果排序 |
| P0-T02 | 固化 CompileFlag、ExpressionExtFlag、错误模型和长度策略 | 已完成 | `contract_test.go`、`compile.go`、`internal/contract` 标志值清单 |
| P0-T03 | 建立源码能力清单、Go fixture、引用索引 | 已完成 | `vectorscan-source-map.md`、parser/engine conformance corpus |

## Phase 1 — Infrastructure

| 任务 | 内容 | 状态 | 证据/缺口 |
| --- | --- | --- | --- |
| P1-T01 | Graph：图结构、遍历、SCC、可达性、分析辅助 | 已完成 | `internal/graph` 已覆盖有向/无向图、DFS/BFS、SCC、路径、拓扑、子图、压缩和边界性质 |
| P1-T02 | Bitset、CharReach、容器、Queue、内存 helper | 已完成 | `internal/util` 已覆盖 BitSet/CharReach/Queue/PriorityQueue、范围和边界测试 |
| P1-T03 | CompileContext、CompileError、CompileLimits、PlatformCapabilities | 已完成 | `internal/compiler/context.go` 已覆盖取消、阶段检查、预留/释放和错误分类 |
| P1-T04 | Generic SIMD 全套安全、尾部和非对齐语义 | 已完成 | `internal/simd/generic` 已覆盖加载、掩码、比较、位运算、移位、重排和 fuzz |
| P1-T05 | CPU detection、registry、fat-runtime fallback | 已完成 | `internal/dispatch` 已完成 SSE/AVX/NEON/SVE 能力探测、线程安全后端注册表、能力层级选择、注册表顺序校验和未知架构通用回退 |

## Phase 2 — Parser / AST

| 任务 | 内容 | 状态 | 证据/缺口 |
| --- | --- | --- | --- |
| P2-T01 | literal、class、range、sequence、alternation、repeat | 已完成 | `internal/parser` 单元测试 |
| P2-T02 | assertion、boundary、lookaround、EOD | 已完成 | `assertion_test.go`、parser validation 和 Scanner 区间测试已覆盖断言、边界、前后查找和 EOD |
| P2-T03 | atomic、backreference、conditional、control verb、UTF-8/UCP | 已完成 | 对应根测试、parser validation 和 Unicode/引用/条件/控制动词 conformance 已覆盖支持子集 |
| P2-T04 | AND/OR/NOT 组合解析与输入模型 | 已完成 | `internal/parser/combination.go` 与 Scanner 组合报告链路已闭环 |
| P2-T05 | parser/reference/semantic validation | 已完成 | `validate.go`、编译校验测试已覆盖引用、重复捕获、范围、Unicode、组合和错误位置 |
| P2-T06 | Parser conformance harness | 已完成 | `internal/parser/conformance.go` 固定 corpus 已覆盖序列、分支、断言、Unicode、引用、control verb、条件、否定类和空表达式 |

## Phase 3 — NFAGraph / NG

| 任务 | 内容 | 状态 | 证据/缺口 |
| --- | --- | --- | --- |
| P3-T01 | NFAGraph、节点、边、Builder | 已完成 | `internal/nfagraph` 测试 |
| P3-T02 | SOM、LBR、Prefilter、UTF-8 元数据、ExpressionInfo | 已完成 | ExpressionInfo 能力矩阵已驱动后端选择；单次扫描结果已传播 SOM 与 LBR 相关语义 |
| P3-T03 | depth/dominator/region/equivalence/redundancy analysis | 已完成 | `analysis.go`、graph SCC/dominator/region/rewrite 性质测试 |
| P3-T04 | pruning/restructuring/split/squash/stop、前后向优化 | 已完成 | `rewrite.go` 已实现规范化、不可达/死端裁剪、连接旁路、线性合并、等价节点合并及统计验证 |
| P3-T05 | NG dump/validate/version | 已完成 | `dump.go`、序列化和非法图测试 |

## Phase 4 — Literal / HWLM

| 任务 | 内容 | 状态 | 证据/缺口 |
| --- | --- | --- | --- |
| P4-T01 | literal 管理、分析、加速、候选确认 | 已完成 | `internal/hwlm` 已覆盖文字索引、去重、长度/负载分析、大小写、候选区间和确认辅助 |
| P4-T02 | FDR matcher | 已完成 | `internal/fdr` 已覆盖多文字、重叠、大小写、区间和序列化 |
| P4-T03 | Teddy matcher | 已完成 | `internal/hwlm/teddy` 已覆盖分桶、大小写、重叠、区间、限额和序列化 |
| P4-T04 | Noodle matcher 与选择 | 已完成 | `internal/hwlm/noodle` 已覆盖前缀树、大小写、重叠、区间、限额和选择 |
| P4-T05 | 长 literal confirmation 接入 NG/Rose | 已完成 | Scanner 已按 FDR/Teddy/Noodle 候选结果执行完整规则确认；Rose 角色匹配复用候选索引并支持跨块流；NG 专用确认图仍待补充优化 |

## Phase 5 — NFA / DFA engines

NFA 细化任务清单与状态标记见：[nfa-implementation-plan.md](./nfa-implementation-plan.md)。

| 任务 | 内容 | 状态 | 证据/缺口 |
| --- | --- | --- | --- |
| P5-T01 | Castle、Gough | 已完成 | Castle/Gough 具备独立闭包、正反向索引、接受确认、分支/循环状态收敛、队列保护、损坏布局拒绝和预算回退；复杂图无法安全承载时明确回退 |
| P5-T02 | LimEx 独立 state/context/exceptional/native/SIMD | 已完成 | LimEx 位集合上下文、异常/epsilon/dead 状态掩码、紧凑源状态热循环、前缀和预算保护及布局内存限制已接入；复杂图按能力回退 |
| P5-T03 | McClellan、Sheng、McSheng | 已完成 | Sheng 稠密布局、McSheng 稀疏布局和按字节活动集合路径已接入；两类路径均具备源计数裁剪、复杂闭包内存门禁、预算闭环和安全回退 |
| P5-T04 | Tamarama、Vermicelli | 已完成 | Tamarama 范围桶与预展开闭包、Vermicelli 稀疏前缀状态及候选裁剪已接入；复杂确认、闭包内存门禁、预算和安全回退闭环 |
| P5-T05 | Shufti、Truffle | 已完成 | Shufti/Truffle 高低半字节独立掩码、活动集合裁剪、尾部路径、闭包内存门禁和布局校验已接入 |
| P5-T06 | Repeat、MPV | 已完成 | Repeat 图转换、边界和结果一致性；MPV 固定宽度文字分支验证路径 |
| P5-T07 | LBR compiler/runtime/SIMD、NG→LBR | 已完成 | LBR 单次扫描后继表、边界断言、预算队列、队列内存门禁、可证明前缀候选和失败回退已闭环；复杂 lookaround 保持完整确认 |
| P5-T08 | DFA 构造、determinisation、压缩、runtime、limits | 已完成 | `internal/dfa` 已支持确定化、死状态、最小化、稠密/稀疏表、运行限制和可达状态；非字节图安全回退 |
| P5-T09 | RDFA/reverse DFA | 已完成 | `internal/dfa/rdfa.go` 已覆盖反向构造、前向确认、边界读取限制、结果限制、区间和序列化 |
| P5-T10 | literal/NFA/DFA/LimEx/LBR/Repeat engine selection | 已完成 | 已统一短文字、Repeat、DFA/NFA 图选择；按状态、内存预算安全回退；断言、回溯、Unicode 和不可构图规则保留 AST 确认路径；新增混合规则矩阵与限制回退验证 |
| P5-T11 | 每引擎 conformance 与状态上限 | 已完成 | 统一资源门禁覆盖状态/边/内存/步骤/结果；各引擎布局校验、非法图降级及跨引擎 conformance 矩阵已闭环 |

## Phase 6 — SOM / Fuzzy / Prefilter / Assertions

| 任务 | 内容 | 状态 | 证据/缺口 |
| --- | --- | --- | --- |
| P6-T01 | Block SOM slot、传播、报告、runtime | 已完成 | `internal/som` 已覆盖槽位传播、最早/最晚值、范围、压缩、合并、序列化和报告侧查询 |
| P6-T02 | Edit/Hamming 编译、graph transform、runtime integration | 已完成 | 文字、字符类、通配、有限分支和重复进入 Fuzzy 确认；UTF-8/UCP、空分支及不可证明结构统一 AST 回退，避免未确认结果 |
| P6-T03 | Prefilter generation/optimization/integration | 已完成 | 从 NG 图提取安全公共前缀，候选命中后始终执行完整规则确认；覆盖分支、大小写和误报回归 |
| P6-T04 | Lookaround/assertion/boundary/EOD 全路径 | 已完成 | 单次扫描已统一断言、定宽/变宽 lookaround、UTF-8 边界、EOD 和负向确认语义 |
| P6-T05 | AND/OR/NOT graph/compiler/runtime/report | 已完成 | `internal/combination` 与 Scanner 已覆盖编译计划、短路、累计状态、EOD、报告和依赖校验 |
| P6-T06 | 跨能力 conformance | 已完成 | `internal/combination/conformance.go` 固定 corpus 已覆盖 AND/OR/NOT、优先级和嵌套组合 |

## Phase 7 — Rose Compiler

| 任务 | 内容 | 状态 | 证据/缺口 |
| --- | --- | --- | --- |
| P7-T01 | Rose graph/builder/role/alias/width/groups | 已完成 | 角色图支持文字、交替分支和有限重复转换 |
| P7-T02 | matcher generation/conversion 全类型 | 已完成 | Scanner 已将可转换文字、交替和候选文字统一转换为角色并关联原规则 |
| P7-T03 | infix/outfix、加速、confirmation、engine blob | 已完成 | 内部 Rose 路径支持候选文字 infix/outfix 的完整规则确认、边界限制和去重；复杂 engine blob 不在本实现范围 |
| P7-T04 | Rose instruction generation、validate/dump/version | 已完成 | role program dump/load；Report/Activate/Transition 指令模型、校验和执行已具备 |
| P7-T05 | Rose compiler conformance 与 selection 断言 | 已完成 | `internal/rose/compiler_test.go` 已覆盖文字、交替、有限重复、绝对边界及模糊输入不变量 |

## Phase 8 — Rose Runtime

| 任务 | 内容 | 状态 | 证据/缺口 |
| --- | --- | --- | --- |
| P8-T01 | Program/Matcher/Queue/State 基础 runtime | 已完成 | `internal/rose` 已覆盖 Program/Matcher/Queue/State、索引、快照、排序和基础运行 |
| P8-T02 | Scheduler activation/transition/report/order/dedup | 已完成 | Scheduler 已覆盖激活、优先级替换、迁移、排序、去重、报告回调、SOM 选项和状态快照 |
| P8-T03 | Catchup scanning/state/report | 已完成 | Rose Scheduler 支持范围激活、CatchUp、统一 Report Manager、单次扫描确认和结果排序去重 |
| P8-T04 | Miracle literal/scanning/acceleration | 已完成 | 单、多角色均使用首字节候选桶和统一约束确认；结果稳定排序去重，区间/限量入口复用候选路径，不适用角色安全回退 |
| P8-T05 | Rose report/SOM/EOD/lookaround/infix/outfix | 已完成 | 单次扫描已接入 Report、边界、EOD、候选确认、排序与重复抑制 |
| P8-T06 | Rose runtime conformance | 已完成 | 增加交替、序列、infix 候选与 Scanner 结果一致性验证 |

## Phase 9 — Small Engines

| 任务 | 内容 | 状态 | 证据/缺口 |
| --- | --- | --- | --- |
| P9-T01 | SmallBlock detection/compile/runtime/report | 已完成 | `internal/smallblock` 已覆盖检测、编译、重叠匹配、区间、限额、流尾部和序列化 |
| P9-T02 | SmallWrite eligibility、IR、Build/compiler/runtime/report | 已完成 | `internal/smallwrite` 已覆盖 eligibility、前缀匹配、区间、限额、流尾部和序列化 |
| P9-T03 | SmallBlock/SmallWrite selection/fallback conformance | 已完成 | `internal/smallengine` 已统一选择、可配置边界、序列化、区间和跨块流，并覆盖 fallback 矩阵 |

## Phase 10 — SIMD / Dispatch

| 任务 | 内容 | 状态 | 证据/缺口 |
| --- | --- | --- | --- |
| P10-T01 | x86 SSE/SSE4.x/AVX2/AVX512/VBMI native backend | 已完成 | amd64 已接入 SSE2/AVX2 原生字节掩码及能力门禁；AVX512/VBMI 在固定契约下复用已验证路径并安全降级 |
| P10-T02 | ARM64 NEON/SVE/SVE2 native backend | 已完成 | ARM64 已接入 NEON 原生字节比较、SVE/SVE2 能力层级和固定窗口安全回退 |
| P10-T03 | SuperVector、portable SIMDe semantics | 已完成 | `internal/simd/supervector.go` 提供完整/部分加载、存储、掩码和安全后端契约 |
| P10-T04 | feature detection/dispatch 接入所有 engine | 已完成 | `internal/dispatch` 已覆盖架构归一化、特性推导、能力合并、后端选择和通用安全回退 |
| P10-T05 | SIMD conformance、tail、unaligned、跨架构 CI | 已完成 | generic/x86/arm64 入口已覆盖 tail、非对齐、字节集合掩码和原生路径；CI 已加入 amd64/arm64 编译执行矩阵 |

## Phase 11 — Block Runtime / Scratch / Report / Database

| 任务 | 内容 | 状态 | 证据/缺口 |
| --- | --- | --- | --- |
| P11-T01 | 统一 Scratch 分区、pool 生命周期、热路径 | 已完成 | `internal/scratch`、Scanner pool 已覆盖分区复用、裁剪、复制、清零、容量预算和生命周期 |
| P11-T02 | 统一 Report Manager 全语义 | 已完成 | `internal/report` 已覆盖事件校验、排序、去重、限额、回调、范围查询、聚合、Drain 和 SOM 查询 |
| P11-T03 | Database/Program layout、validation、version、架构兼容 | 已完成 | `internal/database`、`internal/engine` 序列化测试 |
| P11-T04 | Block runtime engine dispatch/execution/transition/fallback | 已完成 | 单次扫描确认路径已接入 Repeat、MPV 和受限字节 NFA，复杂规则保留 AST fallback |
| P11-T05 | 根公开 API、并发安全 | 已完成 | `compile.go`、`scanner.go`、`engine.go`、race 测试 |
| P11-T06 | 全量 API/E2E/conformance、错误和资源恢复 | 已完成 | 单次扫描矩阵覆盖 UTF-8、边界、回溯、重复、模糊、组合、Rose、区间和非法范围 |

## Phase 12 — 全量优化与发布门禁

| 任务 | 内容 | 状态 | 证据/缺口 |
| --- | --- | --- | --- |
| P12-T01 | NG/compiler optimization pass 迁移与回归 corpus | 已完成 | Optimize 具备多轮收敛、规模门禁、失败回退和结构回归验证 |
| P12-T02 | 多规则、输入规模、能力矩阵 benchmark | 已完成 | 已覆盖 1/10/100/1000 规则、NFA 引擎族、SIMD 和资源用量基准；跨平台采用交叉编译门禁 |
| P12-T03 | allocation/P95/吞吐/内存/规模核对与降级 | 已完成 | `BenchmarkResourceUsage` 与既有扫描基准记录 ns/op、B/op、allocs/op、状态、边、布局内存和预算降级 |
| P12-T04 | MUST/EXCLUDE/API diff/semantic regression 审计 | 已完成 | `api-exclude-audit.md`、统一 conformance corpus、API 契约快照和发布审计清单均已核对 |
| P12-T05 | 发布包、变更日志、平台矩阵、runbook | 已完成 | CHANGELOG、release-runbook、限制矩阵和 amd64/arm64 编译验证已归档 |

## 当前下一批任务顺序

完整剩余功能队列已细化至 [remaining-implementation-plan.md](./remaining-implementation-plan.md)，共 40 项实质性任务；当前按 Dispatch → NFA 专用算法 → NG/Fuzzy/SOM → Rose → 原生 SIMD → 优化与发布审计顺序执行。

1. 完成 P5-T01/P5-T06：补齐独立 NFA/Repeat engine contract。
2. 完成 P4-T05、P7-T02~T03：把 literal candidate confirmation 接入 NG/Rose。
3. 完成 P8-T03~T05、P9-T01~T03：打通 Rose、SmallBlock、SmallWrite 到 Block runtime。
4. 完成 P6-T05~T06、P11-T04~T06：组合、统一报告、完整 conformance 和错误恢复。
5. 最后执行 P10、P12 的原生后端、性能基准、范围审计和发布门禁。

## 最近推进记录（2026-09-05）

F-08 收敛：`Scanner.Scan` 现在仅在全部规则均为可证明的纯文字 Rose 角色时启用 Rose 调度；任何复杂断言、模糊、扩展限制、组合或确认角色均自动回退原有 AST/NFA 路径，并通过集成用例核对排序、去重和结果一致性。全量测试、竞态、静态检查和差异检查均通过。

完成本批次后端收敛：新增 CPU 后端能力注册表及稳定优先级回退；为 Vermicelli、Truffle、Sheng 增加独立运行时入口和专用候选布局（高半字节源状态预分组、确定性前缀确认、稠密表调度），并修正 Castle 空首字节掩码的保守执行。Fuzzy 字符类预计算字节掩码，Rose 限量候选统一收集后排序截断，避免多角色限量导致结果丢失。相关包测试、竞态、静态检查和差异检查均通过。

## 验证命令

日常仅保留主验证命令：

```bash
go test ./...
```

发布前的竞态、静态检查和差异检查见 [release-audit.md](./release-audit.md)。命令仅验证 Go 实现，不编译、运行或包装参考源码目录。
