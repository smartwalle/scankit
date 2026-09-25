# Rose 角色运行时实际效用审计

> 适用范围：单次 Block 扫描中 `internal/rose` 包及其在 `scanner.go` 中的接入路径
>
> 文档状态：审计报告（事实记录）
>
> 最后更新：2026-09-24
>
> 触发问题：用户提出疑问 —— "Rose 分支在本项目中作用是不是等于没有"

## 0. 命名澄清

本项目 Git 分支只有 `main / dev / gpt`，**并不存在名为 `rose` 的分支**。本文档讨论的是 `internal/rose` 包及其在 `scanner.go` 中作为一条"角色调度路径"的接入（文档上下文统一称之为 *Rose 角色运行时*，或简写 Rose）。这是一次仅基于代码与运行时探测的审计，不是设计改动的提案。

## 1. 核心结论（先看这里）

> **Rose 角色运行时在本项目当前的 Block 扫描里基本不起作用。**

要点：

1. **生产路径几乎不命中**：只有"全部规则都是纯字面量 + 至少一条带 `FlagSingleMatch` 或 `FlagQuiet`"的极窄场景才会走 Rose。任何出现字符类、量词、锚定、环视、简单交替或扩展约束的规则都会让 Rose 直接出局。
2. **即便命中也比被它取代的 fastLiteral 慢约两个数量级**：实测 100 条纯字面量规则、64 KiB 输入，fastLiteral 吞吐 5102 MB/s，Rose 路径吞吐 37 MB/s（约 138× 差距）。
3. **Rose 包内多数组件是死代码**：`Scheduler.RunReports`、`scanRoseInto`、整个角色调度框架在生产 `Scan` 路径上零调用方，只被自身测试调用。
4. **唯一被生产使用的 Rose 入口最终也落回 FDR**：真正进入 Rose 的扫描调用 `program.FindMatchesInto`，内部 `buildRoleMatcher` 把所有命中工作交给 `fdr.Matcher` 完成 —— 与 fastLiteral 路径用的是同一类底层引擎。
5. **唯一不可替代的能力**是支持 `FlagSingleMatch` / `FlagQuiet` 这两条 fastLiteral 不接受的规则，但在该路径上吞吐下降 138×，需要权衡是否值得保留。

## 2. Scan 主路径上的调用顺序

`Scanner.Scan`（`scanner.go:1512` 起）按下列顺序选择后端：

```text
1. fastLiteral && literalFind != nil   → literalFindInto(data)   返回
2. canUseRoseInScan                    → scanRoseDirectInto(...) 返回
3. backendOnly                         → 整块后端扫描（DFA/NFA/SmallWrite 等）
4. 通用路径：候选索引 + 起点确认 + 报告管理
```

只有 `fastLiteral=false` 且 `canUseRoseInScan=true` 时才会真正进入 Rose，且此后**立即 `return`**（`scanner.go:1540-1541`），不会落到第 3、4 步。

## 3. 触发条件的精确判定

### 3.1 `canUseRoseInScan`（`scanner.go:2810-2846`）

必须同时满足：

| 条件 | 出处 |
| --- | --- |
| `len(rules) > 0` | `scanner.go:2812` |
| `!hasCombo` | `scanner.go:2812` |
| `program != nil`（rose.New 返回非空） | `scanner.go:2815` |
| 每条规则都能转换为 `Confirm: false` 角色 | `scanner.go:2822` |
| `len(covered) == len(rules)`（全部规则都有角色） | `scanner.go:2827` |
| 每条规则 `ext == nil`（无偏移/长度扩展） | `scanner.go:2830` |
| 每条规则有且仅有 1 个 `roseLiteralAlternatives` 分支 | `scanner.go:2833-2836` |
| 该分支必须是 `direct` 字面量（`roseLiteralPattern` 第四个返回值为 true） | `scanner.go:2837` |

`roseLiteralPattern` 接受的形式（`scanner.go:1035` 起）只有：

- `parser.Literal`（纯字面量）
- `parser.Group`（无 scoped flags，非 atomic，仅下传）
- `parser.Repeat`（**必须是固定重复**：`Max >= 0 && Min == Max && Min <= 32`）
- `parser.Sequence` 中只允许 `parser.Assertion`（`^` 或 `$`）和上面三类

也就是说 **任何 `[]`、`{n,}`、`?`、`*`、`+`（非固定重复）、`|`（出现在不是单一分支的 alternation）、`(?<=)` 等一旦出现，Rose 就出局**。

### 3.2 `fastLiteral`（`scanner.go:468-476`）

| 条件 |
| --- |
| `!hasCombo` |
| `len(rules) > 1` |
| `len(candidateIDs) == len(rules)` |
| `len(literalIDs) == len(rules)` |
| 所有规则 `flags & (FlagQuiet \| FlagSingleMatch) == 0` |

只要任一条规则带 `FlagSingleMatch` 或 `FlagQuiet`，fastLiteral 立刻置 false —— 这正是 fastLiteral 与 Rose 的"分工"。

### 3.3 真实场景探测结果

通过反射读取 `scanner.canUseRoseInScan`、`scanner.fastLiteral` 字段并扫描实际数据：

| 规则集 | rose | fastLit | Rose 真激活 |
| --- | --- | --- | --- |
| 100 条纯字面量 | true | true | ❌（被 fastLiteral 抢先） |
| 100 条纯字面量 + 部分 `FlagSingleMatch` | true | false | ✅ |
| 100 条纯字面量 + 部分 `FlagQuiet` | true | false | ✅ |
| 真实 PII（电话/邮箱/身份证/银行卡/信用卡/敏感 token） | false | false | ❌ |
| 含 `[]`、`+`、`{n}` 的模式 | false | — | ❌ |
| 含 `^` 或 `$` 锚定的纯字面量 | false | — | ❌ |
| 含 `(?<=...)` / `(?=...)` 环视 | false | — | ❌ |
| 含 `Ext`（MinOffset/MaxOffset/MinLength） | false | — | ❌ |
| 含组合（`hasCombo`） | false | false | ❌ |

可以看到，**真实业务里几乎所有常见正则都不能进入 Rose**。能进入的场景只剩下"多条字符串完全字面 + 至少一条带 SingleMatch 或 Quiet"这种边缘用例。

## 4. 性能对比（实测）

测试条件：Go 1.x、darwin/arm64、100 条规则、64 KiB 输入（`abcdefgh...` 循环填充），`go test -bench`：

| 路径 | 触发条件 | 吞吐 | 分配 |
| --- | --- | --- | --- |
| `fastLiteral`（noodle/fdr/teddy） | 100 条纯字面量、无 SingleMatch/Quiet | **5102 MB/s** | 1 alloc/op |
| `canUseRoseInScan` 路径（`scanRoseDirectInto`） | 100 条纯字面量 + SingleMatch/Quiet | **37 MB/s** | 0 alloc/op |

差距约 **138×**。唯一的"优势"是少一次分配，但每次分配本身极小（一个候选缓冲），相对于 138× 的吞吐差距来说完全不构成收益。

## 5. Rose 包内的死代码 / 边缘组件

| 组件 | 位置 | 生产 `Scan` 路径是否调用 |
| --- | --- | --- |
| `Scanner.scanRoseInto` | `scanner.go:1098-1233` | ❌（仅 `rose_constraints_test.go`、`rose_role_selection_test.go`、`rose_scan_integration_test.go` 调用） |
| `rose.NewScheduler` + `Scheduler.RunReports` | `scanner.go:1104-1105`（仅在 `scanRoseInto` 内） | ❌（同上） |
| `rose.Scheduler` 大部分公开方法（`Activate`、`Transition`、`Run`、`RunLimit`、`CatchUp` 等） | `internal/rose/rose.go:1337-1753` | ❌（仅 `internal/rose/runtime_test.go` 与包内测试调用） |
| `internal/rose/miracle.go` 全文 | `internal/rose/miracle.go` | ❌（仅 `internal/rose/miracle_test.go` 等调用，生产路径走 `program.FindMatchesInto` 不经过 miracle） |
| `Scanner.scanRoseDirectInto` | `scanner.go:2848-2932` | ✅（唯一在生产中被调用的 Rose 函数） |
| `Scanner.buildRoseProgram` / `dedupeRoseRoles` / `roseLiteralAlternatives` / `roseLiteralPattern` | `scanner.go:863-1095`、`963-985`、`988-1080` | ✅（构造期调用一次，缓存到 `scanner.rosePlan`） |
| `Scanner.computeCanUseRoseInScan` | `scanner.go:2810-2846` | ✅（构造期调用一次，缓存到 `scanner.canUseRoseInScan`） |
| `Scanner.roseStatePool`、`Scanner.roseSinglePool` | `scanner.go:79-80` | ✅（仅 `scanRoseDirectInto` 使用） |

`scanRoseDirectInto` 调用的是 `program.FindMatchesInto`（`internal/rose/rose.go:297`），最终通过 `buildRoleMatcher` → `fdr.Matcher` 完成匹配（`internal/rose/rose.go:807`）。也就是说 **生产 Rose 路径的"角色调度"语义在运行时已经被旁路**，真正的匹配仍由 FDR 完成；与 fastLiteral 路径上的 FDR/noodle 是同一类底层引擎。

## 6. Rose 在历史文档中的定位

`scankit-block-mode.md` 把 Rose 列为 MUST 能力（任务 P7-T01~T05、P8-T01~T06），`remaining-implementation-plan.md` 中 REM-023~REM-025 均标注"已完成"。本文档不挑战这些任务的实现完整性，只指出 **任务完成后形成的运行时路径在当前 `Scan` 入口中的实际效用** 与原始设计目标（提供独立于 HWLM 的角色调度加速）存在偏离：

- 角色调度（Scheduler、Queue、State、Activate/Transition）确实实现了，但 `Scan` 主路径从未调用。
- Matcher（`fdr.Matcher` 包装）确实实现了，且 `scanRoseDirectInto` 在使用它——但这只是一种"重命名后的 FDR 调用"，并不构成独立的角色调度加速。

## 7. 可能的处理方向（仅供后续讨论，本文档不发起）

1. **完全下线 Rose**：删除 `internal/rose` 包、`scanner.go` 里所有 `rose.*` 相关字段与函数、相关测试。如果确认 `FlagSingleMatch` / `FlagQuiet` + 纯字面量的用户场景可走 fastLiteral 的其他扩展（例如 `literalFind` 的逐条 confirm 包装），则可以接受。
2. **保留 Rose 但删除死代码**：保留 `scanRoseDirectInto` / `buildRoseProgram` / `computeCanUseRoseInScan` 与对应测试，删除 `Scheduler.RunReports` 系列、`scanRoseInto`、`miracle.go` 中未被生产引用的部分。
3. **保留全部代码并补充真实场景**：让 Rose 在 `FlagSingleMatch` / `FlagQuiet` + 纯字面量场景下提供与 fastLiteral 等价或更优的实现，需要先证明该场景在用户侧是真实需求。
4. **重新评估 Rose 的角色**：把 Rose 重新定位为"组合（`hasCombo=true`）规则下独立角色调度的入口"，而不是"纯字面量场景的备选 fastLiteral"，但这需要回到设计层面。

无论选哪条路径，都建议先在 README 或用户文档里把"哪些输入会走 Rose 路径"这一信息明确写出来，避免"组件已实现 ⇒ 任何场景都会用上"的误解。

## 附录 A：探测脚本（用于复现第 3.3 节表格）

```go
package main

import (
	"fmt"
	"reflect"

	"github.com/smartwalle/scankit"
)

func main() {
	exprs := []scankit.Expression{
		{Id: 1, Pattern: "ERROR"},
		{Id: 2, Pattern: "WARNING", Flags: scankit.FlagSingleMatch},
	}
	s, _ := scankit.Compile(exprs)
	v := reflect.ValueOf(s).Elem()
	fmt.Println("rose =", v.FieldByName("canUseRoseInScan").Bool())
	fmt.Println("fastLit =", v.FieldByName("fastLiteral").Bool())
}
```

预期：`rose = true`、`fastLit = false`，第一条规则带 `FlagSingleMatch` 让 fastLiteral 出局，但规则全部为纯字面量使 Rose 仍可激活。

## 附录 B：Rose 接入点速查

| 角色 | 文件 / 函数 | 行号 |
| --- | --- | --- |
| 字段 | `Scanner.rosePlan` | `scanner.go:126` |
| 字段 | `Scanner.roseStatePool` / `roseSinglePool` | `scanner.go:79-80` |
| 字段 | `Scanner.canUseRoseInScan` | `scanner.go:82` |
| 构造 | `buildRoseProgram` | `scanner.go:863-957` |
| 构造 | `dedupeRoseRoles` | `scanner.go:963-985` |
| 构造 | `roseLiteralAlternatives` | `scanner.go:988-1029` |
| 构造 | `roseLiteralPattern` | `scanner.go:1035-1080` |
| 构造 | `computeCanUseRoseInScan` | `scanner.go:2810-2846` |
| 调度（生产） | `scanRoseDirectInto` | `scanner.go:2848-2932` |
| 调度（死代码） | `scanRoseInto` | `scanner.go:1098-1233` |
| 接入 | Scan 入口判断 | `scanner.go:1536-1542` |
| 接入 | 构造期一次性构建 | `scanner.go:488-489` |

