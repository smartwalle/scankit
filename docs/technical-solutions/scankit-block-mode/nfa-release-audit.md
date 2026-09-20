# NFA 发布审计清单

## 范围核对

- [x] 仅包含单次 Block 扫描。
- [x] `BlockSession`、`RoseSession`、`EngineSession` 保持暂停。
- [x] 未扩展 `Engine`、`Scanner` 公开 API。
- [x] 不包含报告、统计等业务逻辑。
- [x] 参考源码仅作只读语义参考，未编译、运行或包装。

## 实现核对

- [x] Castle、Gough、LimEx、Sheng、McSheng、Tamarama、Vermicelli、Shufti、Truffle、LBR 具备独立布局或明确安全回退。
- [x] 起点、长度、前缀、空匹配、重叠、排序、去重和结果上限语义已统一。
- [x] 状态、步骤、结果、队列和内存预算具备上限保护。
- [x] 布局校验失败、非法图和序列化版本不兼容时不会执行损坏状态。
- [x] Unicode/断言/不可转换结构不会进入不保真的字节专用路径。

## 验证门禁

```bash
go test ./...
go test -race ./...
go vet ./...
git diff --check
```

性能基准及阈值见 [nfa-performance-thresholds.md](./nfa-performance-thresholds.md)。

本次收口已执行 `go test ./...`、`go test -race ./internal/nfa`、`go vet ./...` 和 `git diff --check`，结果均通过。

## 收口记录

- R049～R059 已完成对应代码路径、回退契约和验证口径。
- R060 的最终门禁以仓库当前代码和以下命令结果为准，不把未纳入范围的流式 Session、报告业务或参考源码运行计入 NFA 完成度。
- 发现新的语义差异时，应先恢复相应任务为“进行中”，修复并重新执行门禁。
