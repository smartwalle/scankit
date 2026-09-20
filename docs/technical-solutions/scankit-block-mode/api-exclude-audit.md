# Block 模式 API 与排除项审计

## API 冻结

公开接口以 `internal/contract` 的快照测试为准。实现变更不得扩展 `Engine` 或 `Scanner` 的导出方法；内部调度和专用后端不改变调用方契约。

## 明确排除项

- `BlockSession`、`RoseSession`、`EngineSession` 及其他流式状态恢复逻辑暂停。
- 报告、统计、排行和看板业务不属于本模块。
- `.codex/vectorscan` 仅供语义参考，不编译、运行、包装或复制其中源码。

## 本轮 API 复核

- `Engine`、`Scanner`、`Compiler` 的导出方法保持不变；本轮新增能力全部落在内部包：
  `internal/rose`（`RoleCost`、`Role.Weight`、`Role.ScanEquivalent`、`Program.RolesForReport`、`Program.CheapestRoleForReport`、`Queue.Compact`）、
  `internal/dispatch`（`ParseTuneFamily`、`TuneFamily.String`、`ParseEnvOverrides`、`EnvOverrides.Apply`、`EnvOverrides.ApplyFeatures`、`SetBackendOverride`、`BackendOverride`）、
  `internal/simd/arm64`（内部原生入口）、
  `internal/simd`（`ByteSet`、`NewByteSet`、`ByteSet.TableVector`、`ByteSet.Mask`、`Backend.ByteSetMaskPrepared`、`GenericBackend.ByteSetMaskPrepared`、
  `ByteSetTables`、`NewByteSetTables`、`ClampLanes`、`ComposeWindowMask`、`WindowMaskScalar`、`Backend.WindowMask`、`GenericBackend.WindowMask`、
  `WideWidth`、`FirstByteTables`、`ComposeWindowMask64`、`WindowMask64Scalar`、`Backend.WindowMask64`、`GenericBackend.WindowMask64`）、
  `internal/simd/x86`（`Tier`、`NewWithTier`、`EffectiveTierOf`，以及 `nativeByteSetMask32`/`nativeByteSetMask32AVX2`/`nativeByteSetMask64AVX512`/`nativeByteSetMask64VBMI` 等内部原生入口）、
  `internal/simd/arm64`（`Backend.WindowMask64` 等宽窗口入口）、
  `internal/simd/arm64`（`Tier`、`NewWithTier`、`EffectiveTierOf` 与内部原生入口）。
- 公开契约仍以 `internal/contract` 的快照测试为准，门禁命令：`go test ./internal/contract ./...`。
- 运行时可调项只影响后端选择与调优裁剪，不改变匹配语义；非法环境变量不会改变默认注册表。
