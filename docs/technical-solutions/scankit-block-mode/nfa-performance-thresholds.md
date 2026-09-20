# NFA 基准与资源阈值

## 基准范围

基准覆盖 1、10、100、1000 条规则，以及短（64B）、中（4KiB）、长（1MiB）输入；专用后端至少包含 LimEx、Sheng、McSheng、Tamarama、Vermicelli、Shufti、Truffle 和 LBR。

执行入口：

```bash
go test ./internal/nfa -run '^$' -bench 'Benchmark(ByteNFA|EngineFamilies|SelectEngineKind)$' -benchmem -count=5
```

## 资源阈值

- 专用布局编译前估算不得溢出；超过 16 MiB 必须降级到安全确认路径。
- 状态数、边数、闭包和队列均不得因单次输入无限增长。
- `MatchAtBudget` 的步骤或结果预算耗尽时必须设置停止标记，不得返回未确认结果。
- 不支持的图、损坏布局和不兼容序列化版本必须安全回退或拒绝加载。

## 结果记录

每次基准记录 `ns/op`、`B/op`、`allocs/op`、状态数、边数、布局内存、步骤数和是否降级。阈值只用于比较同一环境下的回归，不跨平台直接比较绝对吞吐。

## 判定规则

1. 先确认结果与基线一致，再比较性能数据。
2. 任何结果差异、预算越界或布局校验失败均判定为回归。
3. 仅性能下降且未超过同环境历史波动范围时记录观察项，不修改语义阈值。

## 当前基线（单次运行）

在 darwin/arm64 环境执行 `-benchtime=1x` 的结果约为：LimEx 25.8µs、Sheng 72.0µs、McSheng 27.1µs、Tamarama 8.0µs、Shufti 8.9µs、Truffle 10.1µs、Vermicelli 25.0µs。该数据仅作为后续同环境回归比较基线。
