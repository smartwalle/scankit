# 性能测试结果记录

本文件是 scankit Block 模式**性能测试结果的唯一记录处**。原先（2026-09-20）的基线内容已作废，
不再保留；作废说明见 §7。

## 1. 维护规则

1. **只在产生新的最优值时更新本文件**：没有新最优值的实验（含被否决的方向）不写进本文件，
   写进 `问题与排查计划.md` 对应章节。
2. 一次更新固定包含三件事：
   - 在 §4「演进记录」**追加**一行：日期 / 基准 / 前值 → 新值 / 变化 / 依据章节；
   - 同步修改 §3「当前最优值」对应行的数值、采集日期与「依据」列；
   - 不改动 §4 里已有的历史行，也不删除被替换掉的旧值。
3. 数值必须来自**实际运行**；禁止用占比（pprof）或推演替代实测。
4. 绝对值与相对值分栏记录：**绝对值只在与它同一次采集内可比**，跨会话的绝对差值不可比；
   「本次提升多少」只能用**同一次采集内的两棵树交错 A/B** 差值表达（见 §2）。

## 2. 测量规范

环境（历次记录均为此环境，换机器必须在 §4 注明）：

- CPU：Apple M5 Pro；OS：darwin/arm64；Go 1.27。

**结论性对比（判定「是否提升」）必须满足**：

- `caffeinate -i` 包裹；
- **一次只跑一个基准**（`-test.bench` 只匹配一个名字），不并发、不后台跑其他任务；
- 两棵树交错执行（base → new → base → new …），轮间 `sleep 10`；
- 多轮取最小值（至少 3 轮）。

**本文件 §3 的采集方法**（建立/刷新「当前最优值」）：

- 同一进程内 `-test.count 5`，每个基准取 5 次采样中的最小值；
- 仍然是 `caffeinate -i`、一次只跑一个基准族；
- 命令见 §5。该方法给出的是「可复现的参考值」，不是跨版本可比量。

**噪声底**：在结构性 no-op 的基准上（见 §6）两棵树交错实测差值落在 **±1%** 以内，
因此小于 1% 的差异一律不记为提升或回退。

## 3. 当前最优值

采集时间：**2026-09-26**；代码版本：`dev` 分支 `9c1dbfc perf: 入口文字之后续跑确认表`；
采集方法：§2 的「当前最优值」方法（`-count 5` 取最小，`caffeinate -i`）。

### 3.1 固定语料（64 KiB + 12 规则）

基准：`BenchmarkFixedCorpusScan`（`conformanceMatrixExpressions` 的 12 条 Block 规则，
`fixedBenchCorpus` 确定性重复到约 64 KiB，含尾部截断、非对齐起点与跨窗口长前缀）。

| 后端 | ns/op | MB/s | B/op | allocs/op | 依据 |
| --- | ---: | ---: | ---: | ---: | --- |
| `/host` | 266,572 | 245.58 | 287,017 | 1 | §54、§56.6 均为结构性 no-op |
| `/generic` | 400,681 | 163.38 | 287,294 | 1 | §54 P0-6 后复测 |

### 3.2 PII 100 规则（`BenchmarkPIIRedactionRules100`）

100 条规则 = 8 种 PII 形态 × 独立字段锚点 `field%02d=`；三个密度共用同一套规则：

| 密度 | 语料字节 | 命中数 | 基准 | ns/op | MB/s | allocs/op | 依据 |
| --- | ---: | ---: | --- | ---: | ---: | ---: | --- |
| NoMatch | 26,337 | 0 | `ScannerScanInto` | 2,745 | 9,595 | 0 | §56.6 |
| NoMatch | 26,337 | 0 | `EngineMask` | 2,931 | 8,985 | 3 | §56.6 |
| NoMatch | 26,337 | 0 | `GoRegexpReplace` | 1,473 | 17,881 | 2 | 外部参照 |
| LowMatch | 30,805 | 200 | `ScannerScanInto` | 10,497 | 2,935 | 0 | §56.6 |
| LowMatch | 30,805 | 200 | `EngineMask` | 13,142 | 2,344 | 3 | §56.6 |
| LowMatch | 30,805 | 200 | `GoRegexpReplace` | 122,506 | 251 | 11 | 外部参照 |
| HighMatch | 169,316 | 6,400 | `ScannerScanInto` | 247,360 | 684 | 0 | **§56.6 P0-8** |
| HighMatch | 169,316 | 6,400 | `EngineMask` | 305,518 | 554 | 3 | §56.6 |
| HighMatch | 169,316 | 6,400 | `GoRegexpReplace` | 3,702,395 | 46 | 22 | 外部参照 |

`HighMatch` 是「高命中密度最坏情况」：169,316 字节里 6,400 个真实命中，即 26.5 字节/命中。

> 同日交错 A/B 里 `HighMatch/ScannerScanInto` 采到过 244,958 ns（§4）。与上表的 247,360 ns
> 不是矛盾：一个是「两棵树交错取最小」，一个是「同进程 5 次采样取最小」，差异来自采样方式
> 与机器状态，量级都在噪声底附近。判断提升只认 §4 记录的**成对差值**。

### 3.3 PII 单形态（`BenchmarkPIIRedaction`）

每条规则单独跑；`EngineMask` 是端到端（扫描 + 原地脱敏），`GoRegexpReplace` 是外部参照基线。

| 场景 | 密度 | EngineMask ns/op（MB/s） | GoRegexpReplace ns/op（MB/s） |
| --- | --- | ---: | ---: |
| Phone1 | NoMatch / LowMatch / HighMatch | 4,357（5,634） / 4,456（5,508） / 6,252（3,640） | 24,709（993） / 26,270（934） / 41,746（545） |
| Phone2 | NoMatch / LowMatch / HighMatch | 3,289（7,464） / 3,402（7,215） / 5,709（3,986） | 460,548（53.3） / 465,861（52.7） / 450,331（50.5） |
| Phone3 | NoMatch / LowMatch / HighMatch | 3,202（7,666） / 3,298（7,443） / 5,111（4,453） | 294,548（83.3） / 299,967（81.8） / 290,386（78.4） |
| Email | NoMatch / LowMatch / HighMatch | 1,697（14,635） / 1,789（13,876） / 4,017（5,792） | 1,273,456（19.5） / 1,264,235（19.6） / 1,190,046（19.6） |
| ChineseID | NoMatch / LowMatch / HighMatch | 5,006（4,980） / 5,078（4,909） / 6,959（3,380） | 311,162（80.1） / 311,443（80.0） / 322,435（73.0） |
| BankCard | NoMatch / LowMatch / HighMatch | 2,494（9,945） / 2,576（9,629） / 4,141（5,619） | 4,096（6,055） / 6,478（3,829） / 32,335（720） |
| CreditCard | NoMatch / LowMatch / HighMatch | 4,371（5,688） / 4,410（5,638） / 6,097（3,837） | 507,156（49.0） / 519,720（47.8） / 503,640（46.5） |
| Password | NoMatch / LowMatch / HighMatch | 4,079（6,049） / 4,168（5,920） / 5,993（3,840） | 271,226（91.0） / 274,274（90.0） / 277,113（83.0） |
| AllPIITypes | NoMatch / LowMatch / HighMatch | 15,079（1,910） / 15,425（1,868） / 31,211（1,018） | 3,448,172（8.35） / 3,453,929（8.34） / 3,978,980（7.99） |

`EngineMask` 在全部 9 个场景上均为 96 B/op、3 allocs/op；`NoMatch` 行无分配。

## 4. 演进记录

按时间倒序；只记录**新最优值**。相对变化一律取自同一次采集内的两棵树交错 A/B。

| 日期 | 基准 | 前值 → 新值 | 变化 | 依据 |
| --- | --- | --- | --- | --- |
| 2026-09-26 | `PIIRedactionRules100/HighMatch/ScannerScanInto` | 259,477 → 244,958 ns | **−5.6%** | §56.6（P0-8 确认表续跑） |
| 2026-09-26 | 同上，探针 `confirmEnd` 分段 | 155.5 → 122.0 µs | **−21.5%** | §56.6 |
| 2026-09-26 | 同上，Email 形态确认（13 条规则 / 830 余候选） | 52 → 21 µs | **−60%** | §56.6 |
| 2026-09-26 | `FixedCorpusScan/host` | 265,780 → 263,456 ns | −0.9%（no-op，§6） | §56.6 |
| 2026-09-26 | `PIIRedaction/{Email,AllPIITypes}/HighMatch/EngineMask` | 3,980 → 4,014 ns / 31,573 → 31,519 ns | ±1%（no-op，§6） | §56.6 |
| 2026-09-2x | `FixedCorpusScan/host` | 271,840 → 252,553 ns | **−7.1%**（allocs 2→1） | §54（P0-6 统一两字节前缀匹配器） |
| 2026-09-2x | `FixedCorpusScan/generic` | 557,288 → 392,964 ns | **−29.5%** | §54 |
| 2026-09-2x | `PIIRedaction/Password/HighMatch` | 6,091 → 5,768 ns | **−5.3%** | §54 |
| 2026-09-2x | `PIIRedaction/Password/NoMatch` | 4,342 → 3,946 ns | **−9.1%** | §54 |

> P0-6 的绝对值与 §3 的绝对值来自不同会话，**不可直接相减**：例如
> `FixedCorpusScan/host` 在 §3 记为 266,572 ns，而 P0-6 会话的最优值是 252,553 ns。
> 两个数字都成立，差别来自采集会话的机器状态；判断「是否提升」只看同会话交错 A/B 的差值。

## 5. 复现命令

构建被测二进制（`tests` 包承载全部公开 API 基准）：

```bash
go test -c -o /tmp/scankit_bench.test ./tests/
```

采集「当前最优值」（§3 用的方法）：

```bash
caffeinate -i /tmp/scankit_bench.test -test.run '^$' \
  -test.bench 'BenchmarkFixedCorpusScan/' -test.benchmem -test.count 5

caffeinate -i /tmp/scankit_bench.test -test.run '^$' \
  -test.bench 'BenchmarkPIIRedactionRules100/' -test.benchmem -test.count 5

caffeinate -i /tmp/scankit_bench.test -test.run '^$' \
  -test.bench 'BenchmarkPIIRedaction/' -test.benchmem -test.count 5
```

两棵树交错 A/B（判定提升/回退的标准做法；`<BENCH>` 一次只放一个基准名）：

```bash
for round in 1 2 3; do
  for tree in base new; do
    caffeinate -i /tmp/<tree>.test -test.run '^$' -test.bench '<BENCH>' -test.benchmem -test.count 1
    sleep 10
  done
done
```

## 6. 结构性 no-op 与噪声底

以下基准对特定改动是**结构性 no-op**，它们的实测差值构成噪声底（**±1%**），
不作为提升或回退证据：

| 基准 | 为什么是 no-op |
| --- | --- |
| `FixedCorpusScan/*` | 12 条规则中没有任何一条同时具备确认表与入口文字（`\bword\b` 的 `confirmSkip = 0`，其余规则无断言），因此 §56 的确认表续跑不生效 |
| `PIIRedaction/Email/*`、`PIIRedaction/AllPIITypes/*` | 模式没有入口文字（`confirmSkip = 0`），确认表续跑不生效 |
| `PIIRedactionRules100/{NoMatch,LowMatch}` | 候选数很少（0 / 200），收益被固定开销摊薄到噪声内 |

## 7. 变更说明

- **2026-09-26**：本文件整体重写为「性能测试结果记录」。删除原先（2026-09-20）的基线表，
  包括 `ScanRuleScales`、NFA 引擎、Rose、HWLM、SIMD 掩码等条目——那些数字属于一次性采样，
  既没有可复现的采集口径，也不属于当前回归基准集（见 §1），继续保留只会误导对比。
- **保留**：文件名与路径不变（`docs/technical-solutions/scankit-block-mode/performance-baseline.md`），
  因为 `release-runbook.md`、`release-audit.md`、`vectorscan-replication-task-list.md` 引用该路径。
- **不纳入本文件**：`BenchmarkEngineScan`（已删除）、`internal/*` 包内基准（只作实现内部证据，
  不作为回归基线）、以及被否决方向的数据（写在 `问题与排查计划.md`）。
