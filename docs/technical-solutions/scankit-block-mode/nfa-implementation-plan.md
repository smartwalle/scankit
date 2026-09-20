# NFA 专用引擎推进实施计划

> 适用范围：单次 Block 扫描 NFA 能力
>
> 全项目复刻任务台账：[vectorscan-replication-task-list.md](./vectorscan-replication-task-list.md)（NFA 对应 C-01～C-15）
>
> 当前基线：约 78%（基础专用布局已存在，完整独立算法仍未闭环）
>
> 文档状态：实施计划

## 1. 目标与边界

本文件定义 NFA 阶段的技术目标和门禁；具体开发领取、状态更新、代码范围及验收证据统一记录在复刻任务台账的 C 阶段，避免同一任务在多个文档中重复维护。

### 1.1 最终目标

完成 NFA 引擎族的编译、状态布局、执行、限制、降级和结果语义，使每个已选择的专用引擎都具备可独立运行的算法路径；不能以共享通用字节 NFA、测试桩或仅增加字段作为完成依据。

### 1.2 固定约束

1. 仅处理单次 Block 扫描，不实现或恢复流式处理。
2. `BlockSession`、`RoseSession`、`EngineSession` 保持暂停，不纳入本计划。
3. 不扩展 `Engine`、`Scanner` 的公开 API；已有 API 仅做必要的内部收敛。
4. 不实现报告、统计等业务功能。
5. `.codex/vectorscan` 仅作为只读语义参考，不编译、运行、包装或直接复制其中代码。
6. 新增或修改的代码注释使用中文；不在注释中引用参考项目名称。
7. 不新增无意义测试；只保留能证明功能、边界、回退或资源契约的验证。

## 2. 完成定义

一个引擎只有同时满足以下条件，才计为“完成”：

- 编译阶段能识别其适用图，并建立独立状态布局；不适用图明确拒绝或降级。
- 运行阶段不依赖通用图解释器作为主路径。
- 正常匹配、空匹配、重叠、边界、非法输入、长度限制、结果限制和步骤限制语义闭环。
- 状态、队列、内存和输入规模均有上限；超限时返回可判定结果并安全回退。
- 文字、字符类、分支、重复、断言和不可转换结构不会产生误报或漏报。
- 与 `Scanner.Scan`、普通 NFA 和其他可用后端保持排序、去重、offset/length 语义一致。
- 布局损坏、序列化恢复失败或运行时校验失败时自动切换到安全确认路径。

## 3. 实施顺序与阶段门禁

### 阶段一：NFA 公共契约和图准备收敛

**目的**：固定所有专用引擎共享的输入、输出和预算契约，避免后续各引擎重复修正语义。

**实施内容**：

- 统一图展开、可消费节点识别、epsilon 闭包、死状态和最小/最大可接受长度分析。
- 固定起点过滤、前缀过滤、空匹配、重叠结果、排序去重和 bounded input 规则。
- 统一状态、队列、内存、步骤、结果上限的错误与降级行为。
- 统一 `CompileEngine`、`preferredMatchAt`、`MatchAtWithContext` 的主路径选择。
- 清理专用布局校验中可能导致越界、错误接受或错误拒绝的分支。

**阶段门禁**：所有引擎都能复用同一套契约；不改变公开 API；不支持的图仍可由通用确认路径处理。

### 阶段二：Castle/Gough 完整化

**目的**：完成正向 Castle 和反向 Gough 的独立状态机，而非仅依赖图表解释。

**实施内容**：

- Castle：完成整数顶点索引、epsilon 闭包、消费转移、接受状态、死状态剪枝和工作区复用。
- Castle：补齐分支汇合、多接受点、可空路径、最大长度和队列超限行为。
- Gough：完成反向闭包、前驱集合、接受掩码、消费掩码和反向确认路径。
- Gough：补齐多结束位置确认、空匹配、前缀候选和反向步骤预算。
- 两者均增加布局损坏检测，并确保校验失败不继续使用半损坏状态。

**阶段门禁**：Castle/Gough 的正常路径不访问通用图边；复杂或不支持图能安全降级；结果与统一 NFA 语义一致。

### 阶段三：LimEx 位集合引擎完整化

**目的**：把 LimEx 从受限位集合表提升为完整的独立状态执行模型。

**实施内容**：

- 固定 64 位状态编号、活动集合、源状态掩码、接受集合和 epsilon 闭包布局。
- 完成 exceptional 状态分类：不可消费、仅闭包、可消费和死状态分别处理。
- 实现按字节的批量转移合并，减少逐状态解释；保留标量安全路径。
- 补齐分支、汇合、重复和多接受状态的转移语义。
- 完成步骤、结果、状态、内存和输入长度限制；超限返回部分结果或安全降级。
- 仅在纯 Go 且可证明一致时接入向量化；否则保持标量位集合路径。

**阶段门禁**：LimEx 不再以普通字节 NFA 作为主实现；所有异常状态都可分类处理；转移和闭包不越界。

### 阶段四：表驱动 NFA 引擎族完成

**覆盖引擎**：Sheng、McSheng、Tamarama、Vermicelli、Shufti、Truffle。

**实施内容**：

- Sheng：完成稠密 256 字节转移表、源状态掩码、闭包展开和活动集合交换。
- McSheng：完成稀疏文字转移、状态边列表、字符类谓词和稀疏闭包。
- Vermicelli：在独立布局中完成文字前缀候选、稀疏转移和确认路径；不能仅以 McSheng 加前缀字段计完成。
- Tamarama：完成范围转移压缩、桶索引、重叠范围处理和范围闭包。
- Shufti：完成低半字节掩码、字符类转移和尾部输入处理。
- Truffle：完成高半字节转置掩码、非对齐输入和标量回退。
- 所有引擎统一处理空输入、尾部、非法状态、状态超限和不支持图回退。

**阶段门禁**：六类引擎均有自己的状态布局和执行分派；不得把共享 `sparseNFAProgram` 或通用字节表标记为全部完成。

### 阶段五：LBR 与断言关联路径

**目的**：完成 LBR 的单次扫描能力，同时保持复杂 Lookaround 的安全确认。

**实施内容**：

- 完成 LBR 整数状态、断言数组、文字/字符类掩码、后继表和队列预算。
- 支持 Begin、End、绝对边界、单词边界和最终换行边界等可证明断言。
- 对 Lookaround、反向引用和不可表达断言保留 AST 确认，不将零值断言当作无条件 epsilon。
- 统一 LBR 与 Scanner 的排序、去重、offset/length、EOD 和结果限制语义。
- 完成确认失败、布局损坏、队列超限和跨调用数据隔离；不引入流式 Session。

**阶段门禁**：可证明断言由 LBR 独立执行；不可证明结构始终安全回退；不存在断言误报。

### 阶段六：引擎选择、降级和跨后端一致性

**目的**：把所有专用引擎接入同一选择器，并验证选择失败时的行为可预测。

**实施内容**：

- 固化 Literal、Repeat、Castle/Gough、LimEx、表驱动 NFA、LBR 和通用 NFA 的优先级。
- 按图类型、状态规模、内存、输入长度和断言能力选择主路径。
- 专用布局校验失败时只影响当前后端，不影响后续起点和其他后端。
- 统一 `MatchAt`、`MatchAtBudget`、`Spans`、`Scan` 的结果顺序和限制语义。
- 处理多规则、重叠、空匹配、UTF-8/UCP、边界、EOD、重复和模糊结果。

**阶段门禁**：任意专用后端都不能静默跳过规则；选择失败必须进入可验证确认路径。

### 阶段七：资源治理与最终审计

**目的**：完成发布前的 NFA 功能闭环和真实进度审计。

**实施内容**：

- 固化状态数、边数、闭包大小、队列、步骤、结果和内存预算的计算方式。
- 检查短输入、长输入、稀疏匹配、密集匹配和超限输入的降级行为。
- 核对序列化/克隆后的状态布局重建和校验。
- 审计公开 API 差异、注释、错误模型、能力矩阵和非目标范围。
- 更新总任务清单，只按实际代码证据计入完成度。

**阶段门禁**：所有纳入范围的 NFA 功能均有代码路径；没有未处理 TODO/FIXME；剩余限制明确记录且不被虚报为完成。

## 4. 进度核算方式

| 阶段 | 权重 | 完成判定 |
| --- | ---: | --- |
| 公共契约和图准备 | 10% | 公共语义、预算和回退契约闭环 |
| Castle/Gough | 15% | 两类引擎独立运行和复杂图降级 |
| LimEx | 20% | 位集合、异常状态、预算和批量转移闭环 |
| Sheng/McSheng/Tamarama/Vermicelli/Shufti/Truffle | 25% | 六类引擎各自具备独立状态与执行路径 |
| LBR 与断言关联 | 10% | 可证明断言独立执行，不可证明结构安全回退 |
| 选择、降级和一致性 | 10% | 所有入口结果和限制语义一致 |
| 资源治理与最终审计 | 10% | 预算、序列化、API 和剩余限制审计完成 |

只有阶段门禁全部通过，才可将 NFA 标记为 100%；单个方法、字段或少量测试不单独形成阶段。

## 5. 持续执行规则

每个阶段内部按“实现主路径 → 接入调度 → 处理边界 → 处理回退 → Review”循环推进；阶段门禁未满足时继续修复，不切换为汇报任务。除非出现无法由仓库代码自行解决的外部阻塞，否则不暂停开发。

## 6. 实质性任务清单与状态标记

状态只允许使用：`已完成`、`进行中`、`未开始`、`阻塞`。任务必须完成对应的代码、接入和验收条件后才能标记为`已完成`；单个字段、方法或测试用例不单独计为任务。

### 6.1 公共契约与图准备

| 编号 | 实质性任务 | 当前状态 | 代码/证据 | 完成标记条件 |
| --- | --- | --- | --- | --- |
| NFA-001 | 图节点、文字展开与可消费节点分类统一 | 已完成 | `internal/nfagraph`、`internal/nfa/engines.go` | 所有专用编译器使用同一展开结果 |
| NFA-002 | epsilon 闭包构建、排序和源状态包含规则统一 | 已完成 | 各引擎 `closures`/`closureTable` | 闭包不重复、不越界且包含源状态 |
| NFA-003 | 接受态、死状态和空匹配元数据统一 | 已完成 | `compute*Dead`、`acceptsEmpty` | 各后端元数据与图分析一致 |
| NFA-004 | 最小/最大可接受长度分析与输入裁剪 | 已完成 | `graphLengthBounds`、`boundedBackendEnd` | 有界图不读取无效输入，循环图不被错误截断 |
| NFA-005 | 起点首字节和固定前缀候选过滤 | 已完成 | `firstByteMask`、`requiredLiteralPrefix` | 过滤只排除不可能起点，不漏掉空匹配 |
| NFA-006 | 结果排序、去重、重叠和空结果语义统一 | 已完成 | 各 `MatchAt*`、`Spans*` | 所有后端输出顺序完全一致 |
| NFA-007 | 状态、队列、步骤、结果和内存预算契约统一 | 已完成 | `CompileEngineWithBudgets`、各 `MatchAtBudget` | 超限行为和错误模型一致 |
| NFA-008 | 专用布局损坏后的安全回退 | 已完成 | `*RuntimeShapeOK`、`preferredMatchAt` | 任一布局损坏均不误报、不阻断后续起点 |

### 6.2 Castle/Gough

| 编号 | 实质性任务 | 当前状态 | 代码/证据 | 完成标记条件 |
| --- | --- | --- | --- | --- |
| NFA-009 | Castle 整数顶点索引与反向映射 | 已完成 | `castleProgram.index`、`closureIndex` | 索引与图顶点双向校验通过 |
| NFA-010 | Castle 整数 epsilon 闭包执行 | 已完成 | `closureIndexWithWork` | 热路径不依赖顶点映射 |
| NFA-011 | Castle 文字/字符类消费转移 | 已完成 | `transitionIndex`、`classMasks` | 多分支消费结果完整 |
| NFA-012 | Castle 死状态、接受态和队列预算 | 已完成 | `deadByIndex`、`acceptByIndex` | 超限安全停止，布局损坏安全回退 |
| NFA-013 | Castle 分支汇合、可空路径和多接受位置 | 已完成 | `MatchAt`、`MatchAtBudget` | 与通用 NFA 的所有结束位置一致 |
| NFA-014 | Gough 反向闭包和前驱状态布局 | 已完成 | `reverseMask`、`predMask` | 反向状态集合与图一致 |
| NFA-015 | Gough 消费掩码和位集合确认 | 已完成 | `consumeMask`、`acceptsBit` | 反向确认不重复访问图节点 |
| NFA-016 | Gough 多结束位置、空匹配和步骤预算 | 已完成 | `goughProgram.MatchAtBudget` | 结果限制与步骤限制均可预测 |

### 6.3 LimEx

| 编号 | 实质性任务 | 当前状态 | 代码/证据 | 完成标记条件 |
| --- | --- | --- | --- | --- |
| NFA-017 | 64 位状态编号、活动集合和闭包布局 | 已完成 | `bitNFAProgram` | 状态位宽和边界校验完整 |
| NFA-018 | 按输入字节建立源状态掩码 | 已完成 | `sourceMask` | 每个字节的可消费源集合准确 |
| NFA-019 | consumable、exceptional、dead 状态分类 | 已完成 | `consumable`、`exceptional`、`epsilonOnly`、`dead` | 四类状态的编译与运行分派闭环 |
| NFA-020 | 位集合批量转移与闭包传播 | 已完成 | `bitApplyTransitions`、`MatchAtBudget` | 不逐状态回退到通用解释 |
| NFA-021 | 分支、汇合、重复和多接受态处理 | 已完成 | `compileBitNFA`、运行时 | 复杂可转换图结果一致 |
| NFA-022 | LimEx 状态/步骤/结果/内存限制 | 已完成 | `CompileEngineWithBudgets`、预算路径 | 各预算边界有确定行为 |
| NFA-023 | LimEx 标量向量化抽象和安全回退 | 已完成 | `bitApplyTransitions` | 不支持平台保持同一语义 |
| NFA-024 | LimEx 布局损坏、序列化和克隆恢复 | 已完成 | `validate`、`Clone`、`LoadEngine` | 恢复后布局重新校验且不复用旧指针 |

### 6.4 Sheng/McSheng/Tamarama/Vermicelli/Shufti/Truffle

| 编号 | 实质性任务 | 当前状态 | 代码/证据 | 完成标记条件 |
| --- | --- | --- | --- | --- |
| NFA-025 | Sheng 稠密 256 字节转移表 | 已完成 | `tableNFAProgram` | 所有字节槽位和目标集合有效 |
| NFA-026 | Sheng 源状态掩码和预展开闭包 | 已完成 | `sourceMask`、`transClosure` | 热路径跳过不可消费状态 |
| NFA-027 | Sheng 活动集合交换和预算处理 | 已完成 | `tableNFAProgram.MatchAtBudget`、`sourceMask` | 结果/步骤限制与契约一致 |
| NFA-028 | McSheng 稀疏文字状态布局 | 已完成 | `sparseNFAProgram` | 文字转移不依赖稠密表 |
| NFA-029 | McSheng 字符类谓词和闭包 | 已完成 | `classMask`、`classClosure` | 类匹配和负类边界正确 |
| NFA-030 | McSheng 稀疏分支和死状态处理 | 已完成 | `computeSparseDead`、`sourceMask`、`MatchAtBudget` | 分支汇合结果不丢失 |
| NFA-031 | Vermicelli 独立候选和确认布局 | 已完成 | `compileVermicelliNFA`、`candidateStates`、`layoutKind` | 不再仅以 McSheng 加前缀字段计完成 |
| NFA-032 | Vermicelli 前缀过滤与确认失败回退 | 已完成 | 稀疏确认路径、候选状态校验 | 候选误报全部由完整图确认消除 |
| NFA-033 | Tamarama 范围转移压缩和桶索引 | 已完成 | `rangeTransition`、`bucket` | 范围覆盖和未命中路径正确 |
| NFA-034 | Tamarama 重叠范围、闭包和边界输入 | 已完成 | `transitionClosure`、`rangeRuntimeShapeOK` | 重叠范围不覆盖错误 |
| NFA-035 | Shufti 低半字节掩码路径 | 已完成 | `nibbleNFAProgram` mode 0 | 类匹配和尾部输入正确 |
| NFA-036 | Truffle 高半字节转置路径 | 已完成 | `nibbleNFAProgram` mode 1 | 非对齐和标量回退正确 |
| NFA-037 | 半字节引擎预算、死状态和空匹配 | 已完成 | `MatchAtBudget`、空匹配快速路径、`nibbleRuntimeShapeOK` | 两种模式行为一致 |

### 6.5 LBR 与断言

| 编号 | 实质性任务 | 当前状态 | 代码/证据 | 完成标记条件 |
| --- | --- | --- | --- | --- |
| NFA-038 | LBR 整数节点、后继和字符类掩码布局 | 已完成 | `lbrProgram` | 后继表、索引和掩码一致 |
| NFA-039 | Begin/End/绝对边界断言执行 | 已完成 | `assertion`、LBR runtime | 单次扫描边界语义一致 |
| NFA-040 | 单词边界和最终换行边界 | 已完成 | `assertion`、LBR runtime | 正负边界结果一致 |
| NFA-041 | LBR 队列、步骤和结果预算 | 已完成 | `MatchAtBudget` | 队列超限不泄漏状态 |
| NFA-042 | Lookaround、反向引用和不可表达结构回退 | 已完成 | `compileLBR`、通用确认 | 零值断言不得被当作无条件转移 |
| NFA-043 | LBR 与 Scanner 的排序、去重和 EOD 语义 | 已完成 | `Engine.SpansLimit` LBR 专用调度 | 所有入口结果一致 |

### 6.6 调度、一致性与资源审计

| 编号 | 实质性任务 | 当前状态 | 代码/证据 | 完成标记条件 |
| --- | --- | --- | --- | --- |
| NFA-044 | 专用引擎选择优先级固化 | 已完成 | `SelectEngineKind`、`preferredMatchAt` | 选择结果可解释且稳定 |
| NFA-045 | 选择失败后的通用确认回退 | 已完成 | `Engine.MatchAt*` | 不误用其他引擎缓存布局 |
| NFA-046 | 多规则和重叠扫描调度 | 已完成 | `Engine.SpansLimit` | 不因单一起点失败而中断全局扫描 |
| NFA-047 | UTF-8/UCP 与字节专用路径边界 | 已完成 | `dfaLikeGraph`、通用 NFA | Unicode 图不进入字节专用路径 |
| NFA-048 | 序列化、克隆和布局重建 | 已完成 | `Dump`、`LoadEngine`、`Clone` | 恢复后所有后端重新构建 |
| NFA-049 | 状态数、边数、闭包和内存估算 | 已完成 | `estimateEngineMemory`、`MemoryBytes` | 估算保守且不整数溢出 |
| NFA-050 | 跨后端结果一致性闭环 | 已完成 | `internal/nfa/conformance.go` | 所有纳入引擎通过统一 corpus |
| NFA-051 | 性能基准与正式阈值 | 已完成 | `internal/nfa`、`nfa-performance-thresholds.md` | 记录吞吐、分配、步骤和降级行为 |
| NFA-052 | NFA 发布审计和任务清单收口 | 已完成 | 本文档及发布审计 | 无未处理任务，剩余限制明确 |

### 6.7 状态更新规则

- 任务完成后必须同时更新“当前状态”和“代码/证据”，不能只修改百分比。
- `进行中`任务必须继续推进，不能以已有基础布局替代完整算法。
- `未开始`任务不得计入完成度。
- 只有 NFA-001～NFA-052 全部标记为`已完成`，并满足第 3 节各阶段门禁，才允许宣布 NFA 完整完成。

## 7. 单轮执行粒度约束

第 6 节任务清单就是后续每一轮开发的任务队列，不允许再把一个任务拆成多轮会话执行，也不允许用“本轮只完成其中一部分”更新为完成。为保证任务可在单轮内交付，执行时遵循以下约束：

1. 每轮只领取一个编号连续的任务；该任务必须在本轮完成代码修改、内部接入、边界处理和状态标记。
2. 一个任务的范围最多覆盖一个引擎的一条完整子链路（编译布局、运行路径、预算回退三者必须同时闭环），不得使用“完善整个引擎”“补齐全部语义”等无法在单轮完成的描述。
3. 如果实现过程中发现任务仍然过大，必须在开始编码前将其改写成多个新的单轮任务，并为每个新任务补充独立的代码位置、验收条件和状态；原任务保持`未开始`，不得跨轮保留`进行中`。
4. 单轮任务完成后立即进入下一个编号任务，不等待用户确认，不另起“总结轮”。
5. 任务状态更新必须记录：本轮变更文件、可观察行为、边界/回退处理和剩余限制；只有这些内容齐全才可标记`已完成`。

### 7.1 单轮任务模板

后续新增任务统一使用以下格式，确保任务本身就是一次可执行交付：

| 字段 | 要求 |
| --- | --- |
| 任务编号 | 唯一编号，按执行顺序递增 |
| 单轮目标 | 一句话描述一个可独立交付的行为 |
| 代码范围 | 明确到文件和函数族，不跨无关模块 |
| 运行时行为 | 正常、边界、非法或回退行为至少覆盖一项 |
| 验收条件 | 可通过代码审查或已有验证直接判断 |
| 完成状态 | 本轮结束时只能是`已完成`或`阻塞` |

### 7.2 当前大任务的单轮化调整

以下任务原描述过大，执行时必须先按单轮目标拆开，拆分结果追加到清单末尾并替换原任务：

| 原任务 | 单轮拆分要求 |
| --- | --- |
| NFA-013 | 分为分支汇合、可空路径、多接受位置三个独立任务 |
| NFA-019～NFA-024 | 按状态分类、批量转移、复杂分支、预算、恢复五个独立任务拆分 |
| NFA-030～NFA-037 | 每个引擎分别拆为布局、运行、限制/回退任务 |
| NFA-041～NFA-043 | 分为队列预算、断言回退、Scanner 结果语义三个独立任务 |
| NFA-046～NFA-052 | 分为调度、Unicode 边界、序列化、资源估算、一致性、性能、审计七个独立任务 |

拆分后，原任务只有在其全部单轮子任务完成并完成阶段门禁后，才能从清单中标记为`已完成`；在此之前不得虚报阶段完成度。

## 8. 可直接执行的单轮任务队列

以下队列是实际执行粒度。每行代表一轮完整开发，不再继续向下拆分；完成一行后立即进入下一行。

| 顺序 | 单轮任务 | 代码范围 | 当前状态 | 本轮完成条件 |
| ---: | --- | --- | --- | --- |
| R001 | 统一所有专用后端的起点合法性和长度下界判断 | `internal/nfa/engines.go` 各 `MatchAtBudget` | 已完成 | 所有后端对负起点、越界起点和不足最小长度返回一致 |
| R002 | 统一固定前缀和首字节过滤逻辑 | `internal/nfa/engines.go` | 已完成 | 过滤不误杀空匹配和重叠起点 |
| R003 | 统一结果排序、去重和结果上限 | `internal/nfa` 各后端结果出口 | 已完成 | `normalizeNFAEnds` 统一结束偏移序列 |
| R004 | 统一步骤预算耗尽返回行为 | `internal/nfa/engines.go` | 已完成 | `nfaBudgetExceeded` 统一预算判断 |
| R005 | 统一状态/队列/内存预算错误映射 | `CompileEngineWithBudgets`、各布局编译器 | 已完成 | 预估与实际双重门禁返回确定错误 |
| R006 | Castle 整数索引构建与图顶点映射校验 | `internal/nfa/castle.go` | 已完成 | 索引、闭包和转移目标双向一致 |
| R007 | Castle 整数 epsilon 闭包工作区 | `internal/nfa/castle.go` | 已完成 | 热路径只使用整数闭包，工作区可复用 |
| R008 | Castle 分支汇合转移和重复目标去重 | `internal/nfa/castle.go` | 已完成 | 运行时归一化分支目标后展开闭包 |
| R009 | Castle 可空路径和多接受位置 | `internal/nfa/castle.go` | 已完成 | 位置扫描保留空匹配及全部合法结束位置 |
| R010 | Castle 队列超限和布局损坏回退 | `internal/nfa/castle.go`、`engines.go` | 已完成 | 损坏或超限时不执行半成品布局 |
| R011 | Gough 反向闭包位掩码布局 | `internal/nfa/engines.go` | 已完成 | 接受节点反向闭包与图一致 |
| R012 | Gough 消费掩码和前驱传播 | `internal/nfa/engines.go` | 已完成 | 反向确认不再逐节点读取图结构 |
| R013 | Gough 多结束位置确认 | `internal/nfa/engines.go` | 已完成 | 反向确认遍历全部合法结束偏移 |
| R014 | Gough 空匹配和边界长度裁剪 | `internal/nfa/engines.go` | 已完成 | 空匹配、最短和最长边界均裁剪 |
| R015 | LimEx 状态位编号和闭包存储 | `internal/nfa/engines.go` | 已完成 | 状态位宽、闭包宽度和起点合法 |
| R016 | LimEx 源状态掩码按字节生成 | `internal/nfa/engines.go` | 已完成 | 每个输入字节只激活可消费源 |
| R017 | LimEx exceptional 状态分类 | `internal/nfa/engines.go` | 已完成 | 不可消费、可消费、epsilon-only 和死状态分类一致 |
| R018 | LimEx 位集合批量转移 | `internal/nfa/engines.go` | 已完成 | 单字节转移不回退到通用图遍历 |
| R019 | LimEx 分支汇合和多接受传播 | `internal/nfa/engines.go` | 已完成 | 闭包并集合并分支及多接受结束位置 |
| R020 | LimEx 步骤/结果/内存预算 | `internal/nfa/engines.go` | 已完成 | 三类预算均能安全停止或拒绝编译 |
| R021 | LimEx 序列化和克隆重建 | `internal/nfa/engines.go` | 已完成 | 恢复后重新构建位布局，不复用旧切片 |
| R022 | Sheng 稠密转移表和闭包行 | `internal/nfa/engines.go` | 已完成 | 256 字节槽位和闭包行完整 |
| R023 | Sheng 源状态掩码分派 | `internal/nfa/engines.go` | 已完成 | 不可消费状态不进入字节转移循环 |
| R024 | Sheng 结果与预算出口 | `internal/nfa/engines.go` | 已完成 | 结果上限和步骤上限复用统一预算判断 |
| R025 | McSheng 稀疏文字状态执行 | `internal/nfa/engines.go` | 已完成 | 文字路径使用稀疏状态而非稠密表 |
| R026 | McSheng 字符类谓词和负类 | `internal/nfa/engines.go` | 已完成 | 256 字节类掩码与谓词一致 |
| R027 | McSheng 分支汇合和死状态 | `internal/nfa/engines.go` | 已完成 | 源掩码过滤并保留分支汇合结果 |
| R028 | Vermicelli 独立候选状态布局 | `internal/nfa/engines.go` | 已完成 | `layoutKind` 与 `candidateStates` 独立候选状态集合 |
| R029 | Vermicelli 前缀候选与确认路径 | `internal/nfa/engines.go` | 已完成 | 前缀末端进入稀疏状态确认，失败安全回退 |
| R030 | Tamarama 范围压缩和桶索引 | `internal/nfa/engines.go` | 已完成 | 范围桶索引覆盖所有有效字节 |
| R031 | Tamarama 重叠范围和闭包传播 | `internal/nfa/engines.go` | 已完成 | 编译阶段合并重叠范围并校验闭包目标 |
| R032 | Shufti 低半字节掩码执行 | `internal/nfa/engines.go` | 已完成 | 低半字节路径和字符类结果一致 |
| R033 | Truffle 高半字节转置执行 | `internal/nfa/engines.go` | 已完成 | 高半字节路径、尾部和标量回退一致 |
| R034 | 半字节引擎预算和损坏布局回退 | `internal/nfa/engines.go` | 已完成 | 两种模式均受状态、步骤和结果预算保护 |
| R035 | LBR 整数节点和后继布局 | `internal/nfa/engines.go` | 已完成 | 后继索引和字符类掩码一致 |
| R036 | LBR 可证明边界断言执行 | `internal/nfa/engines.go` | 已完成 | Begin/End/绝对边界结果正确 |
| R037 | LBR 单词边界和最终换行边界 | `internal/nfa/engines.go` | 已完成 | 正负边界语义一致 |
| R038 | LBR 不可表达断言安全回退 | `internal/nfa/engines.go`、`nfa.go` | 已完成 | 不支持断言拒绝专用布局并进入通用确认 |
| R039 | LBR 队列预算和多结束确认 | `internal/nfa/engines.go` | 已完成 | 队列超限可停止且不泄漏状态 |
| R040 | 专用引擎选择优先级和主路径 | `internal/nfa/engines.go` | 已完成 | 选择结果稳定且不误用其他布局 |
| R041 | 专用布局失效后的通用确认 | `internal/nfa/engines.go` | 已完成 | 失效后只回退当前引擎，不跳过规则 |
| R042 | 多规则、重叠和全局起点调度 | `internal/nfa/engines.go`、`scanner.go` | 已完成 | 单一起点失败不影响后续起点 |
| R043 | UTF-8/UCP 图与字节后端隔离 | `internal/nfa/engines.go`、`nfa.go` | 已完成 | Unicode 图始终走可保真路径 |
| R044 | 序列化/克隆后的专用布局重建 | `internal/nfa/engines.go` | 已完成 | Dump/Load/Clone 后布局可重新校验 |
| R045 | 各引擎状态和内存估算收敛 | `internal/nfa/engines.go`、`internal/nfagraph/rewrite.go` | 已完成 | 估算覆盖专用布局、切片/映射开销及展开阶段溢出保护 |
| R046 | NFA 跨后端结果一致性收口 | `internal/nfa/conformance.go` | 已完成 | 覆盖起点、区间、结果上限、负限额及跨后端结果对照 |
| R047 | NFA 性能数据和资源阈值固化 | `internal/nfa`、`nfa-performance-thresholds.md` | 已完成 | 固化规则/输入规模、资源阈值、记录字段和判定规则 |
| R048 | NFA 发布审计和清单收口 | 本计划、发布审计文档 | 已完成 | R001～R047 已完成并执行阶段门禁 |

R001～R048 是执行队列，不是建议列表。任何一项处于`进行中`或`未开始`时，整体 NFA 都不能标记为完成；每轮结束必须把对应行改为`已完成`或`阻塞`并填写证据。

## 9. 范围完整性核对与补充任务

对照技术方案中的 NFA、NG、Prefilter、SOM、Rose 和资源限制要求后，原队列还缺少以下可独立交付的 NFA 关联任务。它们不是对已有任务的重复，而是保证 NFA 功能闭环所必需的边界任务。

| 顺序 | 单轮任务 | 代码范围 | 当前状态 | 本轮完成条件 |
| ---: | --- | --- | --- | --- |
| R049 | 内部 Report 节点的 NFA 状态传播 | `internal/nfa`、结果转换层 | 已完成 | 通用状态携带内部报告标识，Report 零宽传播并按标识/结束位置去重；专用布局遇到 Report 自动走确认路径 |
| R050 | NFA Prefilter 候选生成与确认接入 | `internal/prefilter`、`internal/nfa` | 已完成 | 固定前缀仅枚举候选，专用布局失效时仍对候选执行完整 NFA 确认，避免误报和漏报 |
| R051 | NFA SOM 起点/终点元数据传播 | `internal/som`、`internal/nfa` | 已完成 | SOM_LEFTMOST 规则不走无状态后端快路，由逐起点确认维护结束位置最左起点；不引入跨块状态 |
| R052 | NFA 加速接口与标量回退契约 | `internal/nfa`、`internal/dispatch` | 已完成 | 上下文执行在所有专用布局失效时保留步骤/结果预算并统一回退通用确认器 |
| R053 | LimEx/Shufti/Truffle/Tamarama 的纯 Go 向量化接入 | `internal/nfa`、`internal/simd` | 已完成 | 固定前缀候选使用可移植向量掩码，尾部和不支持路径自动标量回退 |
| R054 | 复杂重复图与 NFA 专用路径隔离 | `internal/nfa`、`internal/nfagraph` | 已完成 | 重复布局仅接受可识别的文字单元与一致贪婪属性；混合断言、字符类或不一致结构拒绝专用布局并回退通用确认 |
| R055 | NFA 与 Rose matcher 的确认边界 | `internal/nfa`、`internal/rose` | 已完成 | Rose 候选确认失败时按报告编号回到规则完整确认器，并沿用偏移、长度和去重契约 |
| R056 | NFA 与组合规则的状态/结果边界 | `internal/nfa`、`internal/combination` | 已完成 | 组合求值只消费已确认事件，普通 NFA 起点/结束位置不被组合状态改写，EOD 统一补求值 |
| R057 | NFA 错误模型和不支持节点分类 | `internal/nfa`、`internal/nfagraph` | 已完成 | 非法图、状态/内存超限、布局损坏和不支持版本分别通过稳定错误分类返回 |
| R058 | NFA 序列化版本与兼容性策略 | `internal/nfa/engines.go` | 已完成 | Dump 写入版本字段，Load 对不兼容版本返回 ErrUnsupported，兼容版本重新构建并校验专用布局 |
| R059 | NFA 基准场景与正式阈值记录 | `internal/nfa` 基准入口 | 已完成 | 已提供引擎族基准入口并固化资源、步骤、结果和吞吐阈值记录口径 |
| R060 | NFA 最终发布审计与排除项核对 | 本计划、发布审计文档 | 已完成 | R001～R059 证据、非目标和限制已核对，门禁命令通过 |

### 9.1 已覆盖范围

- Castle、Gough、LimEx、Sheng、McSheng、Tamarama、Vermicelli、Shufti、Truffle、Repeat、MPV、LBR。
- 编译布局、运行路径、闭包、死状态、前缀/首字节过滤、状态/步骤/结果/内存限制和安全回退。
- `MatchAt`、`MatchAtBudget`、`Spans`、`Scanner.Scan` 的单次扫描调度和结果语义。
- UTF-8/UCP 隔离、边界/EOD、重叠、空匹配、序列化和克隆重建。

### 9.2 明确不纳入范围

- `BlockSession`、`RoseSession`、`EngineSession` 及其他流式状态恢复逻辑。
- 报告、统计、排行、看板等业务功能；R049 只处理内部 Report 节点的规则标识传播。
- 参考源码的编译、运行、包装或直接复制。
- 公开 `Engine`、`Scanner` API 扩展。
- McClellan/DFA/RDFA 的独立实现；它们属于 DFA 计划，不计入 NFA 任务完成度，但 NFA 必须保留到 DFA 的安全回退边界。

R001～R060 共同构成完整的 NFA 单轮执行队列。只有 60 项全部标记为`已完成`，并满足第 3 节阶段门禁，才允许宣布 NFA 功能完成。
