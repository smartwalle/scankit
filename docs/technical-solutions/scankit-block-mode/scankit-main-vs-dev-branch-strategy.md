# scankit_main (main 分支) vs scankit (dev 分支) 对比与后续维护策略

> 适用范围：决定后续在哪个 checkout / 分支上继续投入开发
>
> 文档状态：战略分析（事实记录 + 建议）
>
> 最后更新：2026-09-24
>
> 触发问题：用户询问 `/Users/smartwalle/Desktop/smartwalle/scankit_main` 与 `/Users/smartwalle/Desktop/smartwalle/scankit` 的关系，以及后续应在哪个版本上继续维护开发

## 0. 关系澄清

两个 checkout **指向同一个 Git 远程**（`git@github.com:smartwalle/scankit.git`），模块名均为 `github.com/smartwalle/scankit`，包名均为 `scankit`。它们是同一仓库的两个分支的工作副本：

| 本地路径 | 当前 HEAD | 当前分支 | 分支定位 |
| --- | --- | --- | --- |
| `/Users/smartwalle/Desktop/smartwalle/scankit` | `ebc79cb` | `dev` | 持续开发分支（含 Rose、NFA、DFA、Small 等完整引擎族） |
| `/Users/smartwalle/Desktop/smartwalle/scankit_main` | `b13a344` | `main` | 稳定分支（更精简的 fast-byte 路径） |

两个分支的祖先是 `c3ffead`（"perf(fixed_byte_regex): 为多分支下一字符类建立稀疏位集加速扫描"，2026-08-28）。从该提交之后开始分叉。

| 维度 | main（scankit_main） | dev（scankit） |
| --- | --- | --- |
| c3ffead 之后的新增提交数 | **3** | **41** |
| 最近 14 天提交数 | 3 | 39 |
| 最近 7 天提交数 | 3 | 39 |

本质上，main 已经基本冻结（最近两周只有 3 个测试改动提交），dev 才是实际投入开发资源的地方。

## 1. 量化对比

### 1.1 代码规模

| 指标 | main（scankit_main） | dev（scankit） |
| --- | ---: | ---: |
| 总 `.go` 文件数 | 69 | 257 |
| 顶层 `.go` 文件数 | 66 | 45 |
| `internal/` 子包数 | 3（hwlm、prefilter、simd） | 25（含 combination、compiler、contract、database、dfa、dispatch、engine、fdr、fuzzy、graph、hwlm、nfa、nfagraph、parser、prefilter、repeat、report、rose、scratch、simd、smallblock、smallengine、smallwrite、som、util） |
| 总代码行数 | 24,890 | 61,145 |

### 1.2 测试覆盖

| 指标 | main | dev |
| --- | ---: | ---: |
| 总 `func Test` 数 | 221 | 791 |
| 顶层 `func Test` 数 | 221 | 190 |
| `internal/` 子包 `func Test` 数 | 0 | 601 |

dev 通过 25 个内部子包各自携带的 600+ 测试，对子模块契约进行了深入覆盖；main 把所有逻辑集中在 66 个顶层文件里，测试与实现紧耦合。

### 1.3 文档体系

| 指标 | main | dev |
| --- | ---: | ---: |
| `docs/technical-solutions/scankit-block-mode/` 下的设计/进度/验证文档数 | 0（仅 `.DS_Store`） | 19（含 scankit-block-mode 方案、progress、verify、vectorscan-replication 任务台账、剩余功能实施计划、问题与排查计划、rose-role-utility-audit 等） |

dev 拥有完整的设计 → 任务台账 → 进度 → 验证文档体系；main 没有设计文档。

### 1.4 最近 14 天提交主题

| 分支 | 主题 |
| --- | --- |
| dev | `dffec00 feat(noodle): 支持 4 字节主键扁平索引` `b03ac0e perf(prefilter): 增加 64 字节 NEON 字节集判定并优化候选提取` `4b78cd0 fix(prefilter): 无上界前缀候选与固定分隔符回退` `7ce8cd5 perf(fdr): 收敛必需文字候选定位与跳过逻辑` `b0deee1 refactor: 用现代 Go 语法简化代码并补充文档注释` 等等 |
| main | `b13a344 test: 新增 PII 脱敏稳定性对比测试` `16b0b1e refactor: 重命名 MaskFunc 参数为 matched` `c997f36 test: 切换 PII 基准测试中启用的用例` |

dev 持续在引擎族核心做性能/正确性投入；main 已经停止引擎层投入，只剩测试维护。

## 2. 引擎族覆盖差异

dev 通过 `internal/` 子包提供了**完整的 Vectorscan Block Mode 复刻**：

| 引擎 / 组件 | main | dev |
| --- | --- | --- |
| HWLM（FDR / Noodle / Teddy） | ✅ `internal/hwlm/{noodle,teddy}` | ✅ `internal/hwlm/{noodle,teddy}` + `internal/fdr` |
| Prefilter（required、prefix、class） | ✅ `internal/prefilter`（更精简） | ✅ `internal/prefilter`（更完整，含 `buildRequiredIndex`、`requiredIndexDriven`） |
| SIMD（arm64/x86/generic、32/64 字节窗口） | ✅ `internal/simd` | ✅ `internal/simd/{arm64,generic,x86}`（更细分） |
| NFA 通用 | ❌ | ✅ `internal/nfa` |
| DFA / RDFA | ❌ | ✅ `internal/dfa` |
| NFagraph / Graph Infra | ❌ | ✅ `internal/nfagraph`、`internal/graph` |
| Parser / Compiler | 自带在 `regex_parser.go` / `compile.go` 里（更简单） | ✅ `internal/parser`、`internal/compiler`（独立子包，向量式） |
| Rose 角色调度 | ❌ | ✅ `internal/rose`（含 Rose、Miracle、Scheduler、Queue；**注意：上一轮审计已确认其大部分是死代码**） |
| SmallWrite / SmallBlock | ❌ | ✅ `internal/smallwrite`、`internal/smallblock`、`internal/smallengine` |
| SOM | ❌ | ✅ `internal/som` |
| Fuzzy（编辑 / 汉明距离） | ❌ | ✅ `internal/fuzzy` |
| Combination | 顶层 `combination.go` | ✅ `internal/combination` |
| Scratch / Report / Database / Dispatch | ❌ | ✅ `internal/scratch`、`internal/report`、`internal/database`、`internal/dispatch` |
| Contract（API 快照测试） | ❌ | ✅ `internal/contract`（API 快照强制门禁） |

main 用顶层单文件的方式把 NFA、DFA、固定字节正则、字面量扫描统一在 `scanner.go` 里（约 110KB / 3012 行），加上 `nfa.go`、`fixed_byte_regex.go`、`byte_regex_plan.go`、`execution_roles.go`、`block_scan_plan` 等辅助文件实现有限的引擎组合。

dev 把每一种引擎拆为独立子包，依赖关系更清晰，但代码体量更大。

## 3. 正则语法覆盖差异

dev 的 parser 支持更多 Vectorscan 语法子集（来自 `internal/parser/*.go`）：

| 语法 | main | dev |
| --- | --- | --- |
| Literal / Class / Range / Alternation / Sequence | ✅ | ✅ |
| Repeat（含 `?`、`+`、`*`、`{n}`、`{n,}`、`{n,m}`、lazy） | ✅ | ✅ |
| Group（含捕获、非捕获、命名、原子组） | ✅（部分） | ✅ |
| `.` / `^` / `$` / `\A` / `\z` / `\Z` / `\b` / `\B` | ✅ | ✅ |
| ASCII 简写 `\d` `\w` `\s` `\h` `\v` `\R` | ✅ | ✅ |
| Unicode property + POSIX class（`\p{...}`、`[[:alpha:]]` 等） | 部分（`unicode_property.go`） | ✅（`internal/parser`） |
| UTF-8 / UCP 语义 | 部分 | ✅ |
| **Lookaround（`(?=...)` `(?<=...)`）** | ❌ | ✅（`Lookaround` AST 节点） |
| **Backreference（`\1` `(?P<n>...)`）** | ❌ | ✅（`Backreference` AST 节点） |
| **Conditional（`(?(cond)...)`）** | ❌ | ✅（`Conditional` AST 节点） |
| **Control verb（`(*ACCEPT)` `(*FAIL)` 等）** | ❌ | ✅（`ControlVerb` AST 节点） |
| **Combination 操作数（`!` `&` `|`、括号组合）** | ✅（`combination.go`） | ✅（`internal/combination`） |
| `(?#...)` / 内联 flags（`(?i:...)` `(?m:...)` 等） | ✅ | ✅ |

main 是"足够用"路线：不实现 lookaround / backref / conditional / control verb，但保留了组合规则和大部分常用语法。dev 是"尽量复刻 Vectorscan"路线：实现了 lookaround、backref、conditional、control verb 等更完整的子集。

## 4. 公共 API 差异

虽然两个分支的 `package scankit` 与模块路径都相同，但具体 API 命名/签名存在不可忽略的差异：

### 4.1 编译标志命名

| 概念 | main | dev（重命名目标） |
| --- | --- | --- |
| 大小写无关 | `CompileCaseless` | `CompileCaseless` |
| `.` 匹配换行 | `CompileDotAll` | `CompileDotAll` |
| 多行模式 | `CompileMultiline` | `CompileMultiline` |
| 单次匹配 | `CompileSingleMatch` | `CompileSingleMatch` |
| 允许空匹配 | `CompileAllowEmpty` | `CompileAllowEmpty` |
| UTF-8 校验 | `CompileUTF8` | `CompileUTF8` |
| Unicode 属性 | `CompileUnicodeProperties` | `CompileUCP` |
| 预过滤 | `CompilePrefilter` | `CompilePrefilter` |
| 最左起点 | `CompileLeftmostStart` | `CompileLeftmostStart` |
| 组合规则 | `CompileCombination` | `CompileCombination` |
| 静默规则 | `CompileQuiet` | `CompileQuiet` |

main 与 dev 重命名目标统一使用 `CompileXxx` 命名（去掉 `Flag` 前缀），对齐 Go 标准库 `regexp/syntax` 的 `FoldCase`、`MatchNL` 等习惯。dev 当前 11 个编译标志全部需要去掉 `Flag` 前缀；类型本身保留 `CompileFlag`。需要同步更新 `internal/contract.CompileFlags()` 快照的 11 项 `Name` 字段，并把 `CompileFlag.String()` 的名称映射改成新值。

### 4.2 `Engine.Mask` / 顶层 `Mask` 签名

| 入口 | main 签名 | dev 签名 |
| --- | --- | --- |
| `Engine.Mask` | `func (engine *Engine) Mask(data []byte, fn MaskFunc) ([]byte, error)` | `func (engine *Engine) Mask(data []byte, fn MaskFunc) error` |
| 顶层 `Mask` | `func Mask(data []byte, matches []Match, fn MaskFunc) []byte` | `func Mask(data []byte, matches []Match, fn MaskFunc)`（无返回值，in-place） |

`Mask` 在设计意图上就是 **in-place 处理**原数据，调用方拿到的就是入参 `data` 本身，签名返回 `[]byte` 会让"返回的是脱敏后副本还是原切片"产生歧义。dev 的 `error`-only（顶层 `Mask` 不返回任何值）保持这一语义、避免误用，是正确选择；main 的 `([]byte, error)` 形态反而属于过度设计。**dev 的签名不需要回退到 main 的形态**。

### 4.4 错误模型

main 显式导出 10 个 `Err*` 哨兵错误供调用方 `errors.Is`（用于横向对比 dev 的错误分类）：

```text
ErrEmptyExpressions、ErrDuplicateExpression、ErrInvalidExpression、
ErrUnsupportedFlag、ErrInvalidUTF8、ErrUnsupportedExpression、
ErrRegexTooComplex、ErrInvalidExtension、ErrUnsupportedExtension、
ErrInvalidCombination
```

dev 不导出这些哨兵错误，错误由 `Compile` 返回的 `error` 直接传递，调用方只能通过字符串匹配或 `errors.As` 解包 `parser.ParseError` 等内部类型。dev 通过 `internal/contract` 强制 CompileFlag / ExtFlag 的数值与名称稳定，但**没有把错误分类契约固化**。

### 4.5 其他

| 维度 | main | dev |
| --- | --- | --- |
| `Scanner.Validate()` | ❌ 没有公开方法 | ✅ `func (engine *Engine) Validate() error` |
| `internal/contract` 快照测试 | ❌ | ✅（API 冻结门禁） |


## 5. 工程化与可维护性

| 维度 | main | dev |
| --- | --- | --- |
| 设计文档 | 无 | 19 篇（完整方案 + 进度 + 验证 + 任务台账） |
| 任务台账 | 无 | `vectorscan-replication-task-list.md`、`remaining-implementation-plan.md` |
| 进度追踪 | 无 | `scankit-block-mode.progress.md`、`CHANGELOG.md` |
| 验证文档 | 无 | `scankit-block-mode.verify.md`、`remaining-implementation-plan.verify.md`、`nfa-release-audit.md`、`release-audit.md`、`release-runbook.md`、`nfa-performance-thresholds.md` |
| 性能排查记录 | 无 | `问题与排查计划.md`（约 3000 行） |
| 审计 | 无 | `rose-role-utility-audit.md`、`api-exclude-audit.md` |
| API 契约门禁 | 无 | `internal/contract` 快照测试 + `go test ./internal/contract ./...` |
| 资源门禁 | 隐式（`maxEditLiteralDistance` 等内部常量） | 显式（`internal/contract`、`limits`） |
| 跨模块契约 | 无显式定义 | "Expression → parser.AST → compiler.ExpressionInfo → nfagraph.Graph → engine.Program → runtime → report → Match" |

dev 把"先设计、再实现、再验证"的工程闭环完整化；main 更像是一个"先把能跑的写出来"的最小实现。

## 6. 性能基线

dev 上一次（本次会话之前）跑出的基线已经记录在 `docs/technical-solutions/scankit-block-mode/问题与排查计划.md`（§22 与 §31），目前 dev 的关键吞吐档位：

| 场景 | dev 吞吐（§31 后） | 量级 |
| --- | ---: | --- |
| `TestEngineScanStable` 中位 | `~430 MB/s`（1KB 档）/ `~430 MB/s`（1MB 档）/ `~440 MB/s`（10MB 档） | 与 §22 基线对比中位改善 −45% |
| `TestHyperscanGo100RulesStable` / EngineMask | `~520 MB/s`（Size=1024） | 与 §22 基线对比改善 −52% |
| `TestPIIRedactionStable` / EngineMask | `~5000 MB/s`（NoMatch） | 与 §22 基线对比改善 −18% |

main 的同等基准暂时没有公开数字，但 dev 的最近 PR 主题（`dffec00` noodle 4 字节主键、`b03ac0e` 64 字节 NEON）显示 dev 仍在推进性能，而 main 没有任何等价改动。

## 7. 风险点

### 7.1 dev 分支

1. **代码体量大**：61K 行 + 25 个 `internal/` 子包，新人上手成本高。
2. **Rose 死代码**：上一轮审计（`rose-role-utility-audit.md`）已经确认 `internal/rose` 的 `Scheduler`、`scanRoseInto` 等组件在生产 `Scan` 路径零调用方，建议清理。

### 7.2 main 分支

1. **缺少长期维护**：最近 14 天只有 3 个测试改动，没有引擎层投入。
2. **不支持重要语法**：lookaround、backreference、conditional、control verb 在 parser 层完全缺失。
3. **没有 API 契约门禁**：增加/删除公共方法不会被自动检测。
4. **没有设计文档**：后续接手者很难快速理解扫描路径的设计意图。

## 8. 维护策略建议

**推荐：在 `dev` 分支（当前 `/Users/smartwalle/Desktop/smartwalle/scankit`）上继续作为主要开发分支**，但吸收 main 分支的少量有效改进。

### 8.1 长期主线：dev 的完整 Vectorscan 复刻

理由：

1. dev 的引擎族完整覆盖（HWLM、Prefilter、NFA、DFA、Small、Rose、SOM、Fuzzy、Combination）对应 Vectorscan 的 Block Mode 范围，是这个项目最初的设计目标（参见 `scankit-block-mode.md` R-004）。
2. dev 的 791 个测试与 `internal/contract` 快照门禁为 API 稳定提供了机械保证。
3. dev 的文档体系（19 篇）让"功能在哪、为什么这么做、怎么验证"都可追溯。
4. dev 最近 14 天 39 个提交集中在性能与正确性提升，是当前投入回报最高的路径。

### 8.2 从 main 折回的少量改进

本节早期列出的两项改进已落地，不再作为后续待办追踪：

- `FlagXxx` → `CompileXxx` 的 11 个编译标志重命名已合并到 dev（修改 23 个 .go 文件、327 处替换，类型名 `CompileFlag` 不变；`internal/report/` 内部独立枚举 `FlagSingleMatch`/`FlagQuiet` 与 `internal/parser/ast.go` 的 `GroupFlagXxx` 不受影响）。
- 10 个 `Err*` 哨兵错误（按 dev 实际错误出口评估后落地）已合并到 `compile.go`，`internal/contract` 的契约测试覆盖名称列表；调用方可通过 `errors.Is(err, scankit.ErrXxx)` 做错误分类断言。详见本仓库 git log。

唯一可继续评估但本期不进入 §8.3 的项：

- 借鉴 main 的"小巧块扫描路径"（`scanBlockFixedOnly`、`scanBlockSparseLiterals`、`scanBlockRootByteAnchoredWord` 等）作为补充：如 dev 的 `byte_regex_plan.go` 没有等价实现，可择优移植。

### 8.3 立即处理（dev 分支的内部清理）

1. **删除 Rose 死代码**（按 `rose-role-utility-audit.md` 的建议）：下线 `internal/rose`、删除 `scanner.go` 里所有 `rose.*` 字段与函数、相关测试。
2. **合并 `dev` 经常被误以为"production-ready"的提交进 `main`**：当前 `main` HEAD 是 `b13a344`（2026-09-22），`dev` 已经领先 41 个 commits 且全部经过测试 + benchmark 验证。把 `dev` 的 41 个 commits 经过一轮审阅后 fast-forward 到 `main`，避免长期分叉。
3. **明确 README 里的"哪个分支是 stable"**：在 `README.md` 里写明 `main` 是 stable，`dev` 是 active；下次发布前从 `dev` 同步过去。

### 8.4 长期不推荐的动作

1. **不要把 main 当作主分支推回 dev**：main 的简化设计丢弃了 Rose、NFA、DFA 等完整引擎族，合并回 dev 等于回退 41 个 commits 的引擎层投入。
2. **不要继续冻结 main**：main 现在的"轻量 + 无文档 + 零引擎投入"定位，对一个目标向量块引擎的项目来说缺乏长期价值。
3. **不要把 scankit 和 scankit_main 视为两个独立项目**：它们是同一仓库的不同分支，每次只该在其中一个 checkout 上编辑并推送。

## 9. 结论（一句话）

**继续在 `dev` 分支（`/Users/smartwalle/Desktop/smartwalle/scankit`）投入开发，按上一轮 Rose 审计的建议清理 Rose 死代码，dev 阶段性同步到 main，使 main 始终反映 dev 的稳定快照。** main 分支（`/Users/smartwalle/Desktop/smartwalle/scankit_main`）停止独立投入，最终只作为"dev 的稳定镜像"存在。

