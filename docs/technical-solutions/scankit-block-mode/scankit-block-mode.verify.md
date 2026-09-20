# scankit Vectorscan Block Mode 实现验证文档

本轮完成了一批实现与回归检查，明细见同目录 `scankit-block-mode.progress.md` 的阶段任务记录。

> 对应技术方案：[`scankit-block-mode.md`](./scankit-block-mode.md)  
> 功能模块：`scankit-block-mode`  
> 文档状态：草案  
> 验证环境：Go 1.27 本地/CI 环境；直接运行 Go 测试，不依赖 Oracle、wrapper、Docker 或 CGo
> 最后更新：2026-09-01

## 1. 前置条件与测试数据

| 项 | 值/操作 | 验证方式 |
| --- | --- | --- |
| 服务与依赖 | `go test ./...`；源码参考为 `.codex/vectorscan`；无数据库/MQ、无 Oracle/wrapper/Docker/CGo | Go 测试、源码快照输出 |
| 测试数据 | `testdata/corpus`：按 parser、engine、边界、平台分目录；随机 seed 固定 | fixture 初始断言、checksum |
| 副作用控制 | 仅隔离输入；禁止生产数据；callback 使用 fake；每用例清空 scratch/cache | CI 环境变量与清理断言 |
| 阻塞决策 | Q-001～Q-005 均已按 `.codex/vectorscan` 对齐；该目录仅作源码参考，不编译、运行或包装其中代码 | 检查 contract manifest、源码 commit 和 flags/feature 映射 |

Vectorscan 约束基线：从仓库内 `.codex/vectorscan` 固定源码快照导出 `min_width/max_width`（32 位，无界为 `UINT_MAX`）、扩展 `min_offset/max_offset/min_length`（64 位）、Edit/Hamming distance（无符号整数）、`HS_EXT_FLAG_*` 位和 `hs_compile_error_t` 错误模型；吞吐/P95、alloc/op 及项目级内存/规则上限遵循该源码的 benchmark/fixture 口径。

## 2. 实现与验证状态

| 关联项 | 功能/用例 | 关联需求 | 开发状态 | 测试状态 | 验证状态 | 证据 |
| --- | --- | --- | --- | --- | --- | --- |
| P0-T01/U-01 | Contract snapshot | R-001/R-007 | 代码完成 | 通过 | 未开始 | `contract_test.go` |
| P1-T01~T05 | Infrastructure/SIMD generic/dispatch | R-004/R-005 | 基础完成 | 通过 | 已执行 | `internal/{graph,util,simd,dispatch}` |
| P2-T01~T06 | Parser/AST/validation | R-004/R-005 | 完成 | 通过 | 已执行 | `internal/parser`（高级引用、条件、UTF-8/UCP） |
| P3-T01~T05 | NG/analysis/rewrite | R-004 | 完成 | 通过 | 已执行 | `internal/nfagraph`（analysis、normalize、dump/validate） |
| P4-T01~T05 | HWLM/FDR/Teddy/Noodle | R-004 | 完成 | 通过 | 已执行 | `internal/{hwlm,fdr}`（literal selection/order/dedup） |
| P5-T01~T11 | NFA/DFA/RDFA/selection | R-004/R-005 | 基础完成 | 通过 | 已执行 | `internal/{nfa,dfa,engine}`（engine matrix/auto selection/fallback；DFA 活跃状态快照、输入上限和引擎族 corpus 已补齐，独立布局和原生路径仍有限） |
| P6-T01~T06 | SOM/fuzzy/prefilter/assertion/combination | R-004/R-005 | 基础完成 | 通过 | 已执行 | `internal/{som,fuzzy,prefilter,combination}`、lookaround/Unicode/规则过滤集成；复杂图继续 AST 回退 |
| P7-T01~T05 | Rose compiler | R-004 | 基础完成 | 通过 | 已执行 | `internal/rose`（role/build/validate/dump） |
| P8-T01~T06 | Rose runtime/scheduler/catchup/miracle | R-003/R-004 | 基础完成 | 通过 | 已执行 | `internal/rose` |
| P9-T01~T03 | SmallBlock/SmallWrite | R-004 | 基础完成 | 通过 | 已执行 | `internal/{smallblock,smallwrite}` |
| P10-T01~T05 | SIMD/Dispatch backend contract | R-004/R-005 | 基础完成 | 通过 | 已执行 | `internal/simd`, `internal/dispatch`、架构入口一致性测试；原生指令和跨架构 CI 未完成 |
| P11-T01~T06 | Scratch/report/database/runtime/public API | R-001~R-005 | 完成 | 通过 | 已执行 | `internal/{scratch,report,database,engine}`（version/architecture/serialization）、root API tests、并发 race、callback |
| P12-T01~T05 | Optimization/benchmark/release gate | R-002/R-006 | 基础门禁完成 | 通过 | 已执行（无固定阈值） | `release_gate_test.go`、DFA/Rose 流基准、NFA corpus、invalid-offset guard |

## 2.1 源码参考最小交付定义

| 组件 | 必须内容 | 完成证据 |
| --- | --- | --- |
| 源码基线 | `.codex/vectorscan` commit `4724cff398f81818b28b373ff67c41b9ed95d317` | `git rev-parse HEAD` 输出归档 |
| 源码 | 记录固定 commit，建立 Block compiler/runtime、flags、边界和 engine 的引用索引 | commit 与索引文件 |
| 测试 | Go fixture 覆盖源码中已确认的语义；可选使用现有 Vectorscan 工具人工核对 | `go test` 结果 |
| 范围 | 只验证方案规定的 Block 能力；不因源码中存在其他目录而扩大范围 | V-029 审计 |

本项目不交付 Oracle、wrapper、Docker 或 CGo 依赖。P0-T03 只需建立 `.codex/vectorscan` 源码能力清单、引用索引和 Go fixture；Go 测试直接验证已实现语义。

## 3. 自动化测试清单

| 用例 | 关联项 | 层级 | 场景与关键断言 | 命令/文件 | 结果 |
| --- | --- | --- | --- | --- | --- |
| T-001 | P0-T01 | 契约 | 公开签名/类型快照无 diff | `go test ./...` | 未执行 |
| T-002 | P1-T01~T02 | 单元 | 图遍历、SCC、dominator、bitset/CharReach 性质 | `go test ./internal/graph ./internal/util` | 未执行 |
| T-003 | P1-T04 | fuzz | SIMD 操作与标量参考；空/短/NUL/非对齐/尾部 | `go test -run=Fuzz -fuzz=FuzzSIMD ./internal/simd/...` | 未执行 |
| T-004 | P2 | 单元/fuzz | 全语法节点、非法引用、UTF8/UCP、lookaround/EOD | `go test ./internal/parser/...` | 未执行 |
| T-005 | P3 | 单元 | AST→NG 结构、分析、dump/load 与可达性 | `go test ./internal/nfagraph/...` | 通过 |
| T-006 | P4 | 单元 | literal 提取、FDR/Teddy/Noodle 候选 | `go test ./internal/hwlm/... ./internal/fdr/...` | 通过 |
| T-007 | P5 | 单元/集成 | Castle/Gough/LimEx/McClellan/Sheng/McSheng/Tamarama/Vermicelli/Shufti/Truffle/Repeat/MPV/LBR/DFA/RDFA | `go test ./internal/{nfa,dfa}/...` | 通过 |
| T-008 | P6 | 集成 | SOM、Edit/Hamming、prefilter、assertion | `go test ./internal/{som,fuzzy,prefilter}/...` | 通过 |
| T-009 | P7~P8 | 集成 | Rose role/program；scheduler/order/dedup | `go test ./internal/rose/...` | 通过 |
| T-010 | P9 | 集成 | SmallBlock 与 SmallWrite 独立选择 | `go test ./internal/{smallblock,smallwrite}/...` | 通过 |
| T-011 | P10 | 契约/平台 | feature mask 下 backend 与 generic 结果一致 | `go test ./internal/dispatch ./internal/simd/...` | 通过 |
| T-012 | P11 | 并发/race | Scanner 并发、scratch 隔离、callback 错误清理 | `go test -race ./...` | 通过 |
| T-013 | P11 | E2E | 现有 PII fixtures、Replace/Mask 重叠规则、ScanInto 复用 | `go test ./...` | 通过 |
| T-014 | P12 | fuzz | 边界全集：empty/empty-match/NUL/offset/SOM/EOD/distance/duplicate/order | `go test -run=Fuzz -fuzztime=10m ./...` | 部分覆盖 |

## 4. 接口、数据与并发验证

| 验证编号 | 关联项 | 操作 | 响应断言 | 数据/状态断言 | 清理 | 结果/证据 |
| --- | --- | --- | --- | --- | --- | --- |
| V-001 | F-01/F-06/U-01 | 编译相同 expressions 两次 | 成功或同类 CompileError | Program 指纹/版本稳定、不可变 | 丢弃 Scanner | 未执行 |
| V-002 | F-01/U-01 | 非法 pattern、超限、冲突 flags | 错误分类稳定，不 panic | 错误不含完整 Pattern | 清理缓存 | 未执行 |
| V-003 | U-01 | 逐项触发 Graph/State/DFA/Rose/Program/Memory limit | 返回明确超限或批准 fallback | 无部分可执行 Program 泄漏 | GC/释放 | 未执行 |
| V-004 | F-02/F-03/U-02 | `Scan` 与 `ScanInto`（含非空容量） | Match 列表一致 | 输入不修改，offset 合法 | 清空 matches | 未执行 |
| V-005 | F-02/U-02 | 空输入、空匹配、NUL、UTF8 边界 | 与 `.codex/vectorscan` 源码定义的 ID/From/To/顺序一致 | dedup/SOM/EOD 一致 | 清理 scratch | 未执行 |
| V-006 | F-02/U-02 | MinOffset/MaxOffset/MinLength | 仅返回约束范围内匹配 | offset 不溢出 | 清理 | 未执行 |
| V-007 | F-02/U-02 | EditDistance/HammingDistance | 距离约束精确 | fuzzy engine/report 一致 | 清理 | 未执行 |
| V-008 | F-04/U-03 | 重叠匹配 Replace | 起点最早、同起点跨度最长 | 返回切片不共享输入 | 丢弃结果 | 未执行 |
| V-009 | F-05/U-03 | 重叠匹配 Mask | 仅命中片段定长修改 | 片段外字节不变 | 恢复副本 | 未执行 |
| V-010 | P4/U-02 | literal-heavy corpus | 与源码 fixture 一致 | FDR/Teddy/Noodle/long confirmation 选择可追踪 | 清理 | 未执行 |
| V-011 | P5/U-02 | 每 NFA engine fixture | 结果、报告、状态一致 | limits/fallback 记录 | 清理 | 未执行 |
| V-012 | P5/U-02 | DFA/RDFA fixture | 结果与 NFA 参考 fixture 一致 | state compression 不改变语义 | 清理 | 未执行 |
| V-013 | P6/U-02 | SOM | From/To/SOM 与源码定义一致 | slot propagation 正确 | 清理 | 未执行 |
| V-014 | P6/U-02 | prefilter on/off | 结果完全一致 | prefilter 仅剪枝候选 | 清理 | 未执行 |
| V-015 | P6/U-02 | lookaround/assertion/EOD | 边界断言精确 | 不跨 Block 输入越界 | 清理 | 未执行 |
| V-016 | P6/U-02 | AND/OR/NOT | 组合报告语义一致 | 子图报告去重 | 清理 | 未执行 |
| V-017 | P7/U-01 | Rose compile corpus | 成功 Program 或受控 fallback | role/matcher/infix/outfix 完整 | 清理 | 未执行 |
| V-018 | P8/U-02 | Rose scheduler/catchup/miracle | 顺序/重复/调度一致 | queue 清空、状态归还 | 清理 | 未执行 |
| V-019 | P9/U-02 | SmallBlock/SmallWrite | 与完整 runtime 一致 | SmallWrite dump 可重放 | 清理 | 未执行 |
| V-020 | P11/U-02 | `AllocsPerRun` 与并发扫描 | 无数据竞争、分配达标 | scratch 不串扰 | 清理 | 未执行 |
| V-021 | P10/U-02 | x86/ARM feature matrix | native/generic 结果一致 | safe tail/unaligned 不崩溃 | 清理 | 未执行 |
| V-022 | P11/U-02 | callback 返回错误/中途停止 | 错误可见、无脏状态 | 下次扫描结果不受影响 | 清理 | 未执行 |
| V-023 | P11/U-02 | database/program 篡改或架构不兼容 | validation 拒绝执行 | 版本/scratch 错误明确 | 清理 | 未执行 |
| V-024 | P11/F-02 | 现有 PII tests 与 benchmark fixture | 全部通过 | Match 与 Go regexp 及源码 fixture 约束一致 | 清理 | 未执行 |

## 5. 性能与源码 conformance 验证

| 验证编号 | 场景 | 从接口发起的操作 | 预期指标/对账 | 结果/证据 |
| --- | --- | --- | --- | --- |
| V-025 | 1/10/100/1000 rules | `Compile` + `ScanInto` | 按源码 benchmark/fixture 口径记录编译时延、内存、扫描吞吐/P95 | 未执行 |
| V-026 | short/medium/large input | F-02/F-03 | P95、alloc/op、峰值 scratch；无 OOM | 未执行 |
| V-027 | literal/regex/UTF8/SOM/fuzzy/Rose/NFA/DFA | F-02 | 各 engine 选择、吞吐和源码 fixture 结果 | 未执行 |
| V-028 | 重复、乱序、重放 | 同一输入多次 Scan | Match 集合/顺序稳定，report 去重 | 未执行 |
| V-029 | 排除范围审计 | `rg` 检查 API、目录、dispatch | 无 Streaming/Vectored/Chimera/Power/VSX/Sidecar 路径 | 未执行 |
| V-030 | 全 corpus conformance | 按 `.codex/vectorscan` 源码/测试建立 corpus，调用 scankit F-01/F-02 | ID、From/To、SOM、顺序、错误分类符合源码定义 | 未执行 |

## 6. 需求追踪

| 需求 | 技术建议 | 实现项 | 测试/验证 | 通过条件 | 最终状态 |
| --- | --- | --- | --- | --- | --- |
| R-001/R-007 | S-008 | P0-T01~T03、P11-T05 | T-001/T-013、V-001/V-024 | public API diff=0 | 未完成 |
| R-002/R-004 | S-001~S-007 | P1~P11 | T-002~T-011、V-003/V-010~V-023/V-030 | 每个 MUST 有代码和证据 | 未完成 |
| R-003/R-005 | S-002/S-004/S-007 | P2/P6/P8/P11 | T-003/T-014、V-004~V-009/V-013~V-022 | 符合 `.codex/vectorscan` 源码定义的语义 | 未完成 |
| R-006 | 无 | P12-T04 | V-029 | EXCLUDE=0 accidental | 未完成 |

## 7. 清理结果

| 项目 | 结果 |
| --- | --- |
| 测试数据与副作用 | 每个用例使用隔离 fixture；执行后清空 scratch 和可选编译缓存；待执行 |
| 证据归档 | CI 保存 `test-results/`、benchmark、参考源码 commit 和 feature matrix；待执行 |
