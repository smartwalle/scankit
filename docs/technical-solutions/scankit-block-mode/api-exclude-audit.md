# Block 模式 API 与排除项审计

## API 冻结

公开接口以 `internal/contract` 的快照测试为准。实现变更不得扩展 `Engine` 或 `Scanner` 的导出方法；内部调度和专用后端不改变调用方契约。

## 明确排除项

- `BlockSession`、`RoseSession`、`EngineSession` 及其他流式状态恢复逻辑暂停。
- 报告、统计、排行和看板业务不属于本模块。
- `.codex/vectorscan` 仅供语义参考，不编译、运行、包装或复制其中源码。
