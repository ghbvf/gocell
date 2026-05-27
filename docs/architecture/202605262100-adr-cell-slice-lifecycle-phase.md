# ADR: Cell/Slice 生命周期相位 + Outbox 显式状态机（M2-LIFECYCLE）

- 日期：2026-05-26
- 状态：Accepted
- 关联：issue #686（M2-LIFECYCLE）；来源 ADR-202605041430 M2 + 030 §3 F-08；cap-02 / cap-13
- Amends：`docs/architecture/202605101800-adr-cell-interface-isp-split.md` §D1（CellIdentity 新增 `Lifecycle()` 方法，见 §Decision-E）
- Builds on：`kernel/saga/status.go` / `kernel/command/status.go`（transition-table 范式）、`kernel/fsm`（泛型 helper）、`kernel/cellvocab`（YAML 词汇枚举漏斗）

## Context

GoCell 此前有两处**隐含**状态，缺少显式声明与机器校验：

1. **cell/slice 缺成熟度相位**：cell.yaml / slice.yaml 无 governance 维度的成熟度字段，catalog / 治理层无法表达「实验中」vs「稳定资产」。
2. **outbox entry 状态隐含在 SQL**：`pending / claiming / published / dead` 仅是 `adapters/postgres/outbox_db.go` 的字符串常量，转移规则散落 5 个 SQL（ClaimPending / MarkPublished / MarkRetry / MarkDead / ReclaimStale），Go 层无 enum + 单源 + 转移校验。

issue 文字要求两者都建「state enum + transition map」。本 ADR 在落地时**对 cell 相位做了关键裁决**（见 §Decision-A）：声明式单值不是状态机，强行建转移表是投机抽象。

## Decision

### A. Cell/Slice 相位 = 有序 enum，**不建** transition map

`kernel/cellvocab.CellLifecycle`（`type CellLifecycle string`，与 `cellvocab.ContractLifecycle`/`Level` 同源的 YAML 词汇漏斗）：
`experimental → candidate → asset → maintenance → retired`，外加有序数组 `AllCellLifecycles()` + `CellLifecycleRank(s string) int` + `ParseCellLifecycle`。

**为何不建 transition map（与 issue 文字偏离的裁决）**：grep 证据显示 `command.Transition` / `saga.Transition` 有真实运行时调用者（`adapters/postgres/command_queue.go:358,408`）——设备命令 / saga 真会在运行时变状态。而 **cell 相位是声明式单值**：cell.yaml 声明一个静态相位，系统中不存在「cell 从 experimental 转 candidate」的运行时事件（推进成熟度是人改 YAML）。给它建 transition map + 完备性 archtest 会守护一张**无人遍历的表** —— 投机脚手架，违反「优雅简洁」与「前提消失即删抽象」。

这是 **won't-build 裁决**（非推迟工作），故**不登记 backlog**；本 ADR 即为永久理由记录。若未来出现真实的相位变更路径（如 `gocell` CLI 相位迁移命令），届时再以新 ADR 引入转移机器并定义其真实 consumer。

### B. Outbox 状态机 = 单源 + 全量改写 + relay 真实转移断言

outbox entry **确实在运行时转移**（relay 在 Go 侧据 attempts 决定 claiming→published/pending/dead，SQL UPDATE 即转移），与 command/saga 同形 → 建完整状态机：

`kernel/outbox.State`（`uint8` + `iota+1`，零值非法）+ `String()`（wire/DB status 字符串**唯一来源**）+ `ParseState` + 转移表 `stateTransitions`（终态作空 slice key 自表达）+ `Transition`，镜像 `kernel/saga/status.go`。`State` 是 status **字符串**的单源；`stateTransitions` 是**文档化 + COMPLETENESS 守全的合法图**，不是运行时 SQL 的绑定源。

**单源全量改写**（删旧无 shim）：删 `adapters/postgres/outbox_db.go` 的 4 个 `statusXxx` 常量 + `runtime/outbox/outboxtest` 的 `rowStatus` 并行常量；`runtime/outbox.Store.OldestEligibleAt` 签名改 typed `State`；adapters/postgres + runtime/outbox 全部经 `State.String()` / typed `State`。relay 的 3 个 Go 侧 settlement 决策点（writeBackOne / handleFailedEntry）调 `Transition(StateClaiming, target)`——其运行期价值是 `OUTBOX-STATE-TRANSITION-GUARD-01` 的 **Mark↔target 配对交叉校验**（确保调 `MarkPublished` 的函数体里 target 恰为 `StatePublished`，防复制粘贴错配），**而非**合法性校验（常量实参恒过，本身 tautological）。

**重要边界说明（诚实模型）**：relay 的 `Transition` 之所以留存，是因为存在 `Mark*` 调用可供配对交叉校验。store 的 `ClaimPending`（pending→claiming）与 `ReclaimStale`（claiming→pending/dead）同样是真实转移，但**没有 `Mark*` 配对**——在那里接入 `Transition` 既无合法性价值（与 relay 一样常量恒过）也无配对价值，故 Go 侧不重复。其正确性由 **SQL CAS 谓词（`WHERE status='claiming'/'pending'`）+ conformance 行为测试 + `OUTBOX-STATE-LITERAL-BAN-01`（无裸字面量）** 三层守；SQL-text↔table-edge 的机器绑定受 SQL 实参 `...any` 限制，是**明确接受的 Medium 天花板**，不做脆弱的静态 SQL↔边解析。整张合法图由 `OUTBOX-STATE-TRANSITION-COMPLETENESS-01`（每个 State const 必须是 `stateTransitions` 的 key）守卫。

### C. Governance CELL-LIFECYCLE-01（合法性，lifecycle base / error）

`kernel/governance/rules_lifecycle.go`：①成员合法性（cell/slice `lifecycle` 非空时必须是合法相位）；②cell↔slice 一致性（slice 相位 `Rank` 不得 > 父 cell；empty 默认 experimental，匹配 NewBaseCell）。声明式单值无运行时 from→to，故「转移合法性」落点 = 静态合法性（成员 + slice≤cell），镜像 SLICE-CONSISTENCY-01。

### D. 运行时暴露 + catalog 消费方（单源不双写）

单一真值源 = `CellMeta.Lifecycle` / `SliceMeta.Lifecycle`（构造期一次 `ParseCellLifecycle`）。两个真实 consumer 都从它派生，不双写：
- **catalog wire**：`CellSpec.Lifecycle` / `SliceSpec.Lifecycle`（json/yaml `lifecycle`，与 `ContractSpec.Lifecycle` 同名 wire key），`build.go` 从 metadata 投影。这是设计期 catalog 暴露面。
- **运行时 LifecycleAggregator**：`runtime/lifecycle.LifecycleAggregator` 从注册 cells 的 `cell.Lifecycle()` 取 live 相位快照；`cmd/corebundle/run.go` 启动期消费它打印 assembly 成熟度分布 Info 日志（「差距由消费方计算」= 分布由 consumer 算）。这是运行期 introspection 面（live registered set vs 设计期声明）。

### E. 排序与 ISP amendment

- slice≤cell 排序：`experimental(0) < candidate(1) < asset(2) < maintenance(3) < retired(4)`。`retired` 取最高序数（终态/生命终点）；该排序只禁「slice 比 cell 更成熟」，允许 retired cell 容纳低相位 slice（退役场景）。
- **ISP amendment**：`CellIdentity` 接口新增 `Lifecycle() cellvocab.CellLifecycle`（与 `Type()`/`ConsistencyLevel()` 同类声明式属性），是 202605101800 ISP-split ADR §D1 的方法集变更，由 `CELL-IFACE-ISP-METHODSETS-01` 源驱动 hash 守卫（已更新）。`BaseSlice` **不加** `Lifecycle()` 访问器——无 consumer（governance/catalog 直接读 `SliceMeta.Lifecycle`），加了即死代码。

## AI-robust 评级（诚实，全 Medium，无 compiler-Hard）

**本 PR 无 compiler-Hard 机制**——Go 拦不住裸字符串比较 / SQL `...any` 实参无法 type-constrain，删常量 ≠ 编译期 Hard。诚实分级：

| 机制 | 评级 | 真实强保证来源 |
|------|------|---------------|
| cell/slice 相位成员合法性 | Medium | 构造期 `ParseCellLifecycle` fail-fast（非法值 → cell 启动失败）+ governance 规则镜像（validate 时）+ schema enum（测试期，运行时不跑 JSON-schema，仅 KnownFields 拒未知键） |
| `CELL-LIFECYCLE-01`（成员 + slice≤cell） | Medium | governance 引擎规则 + `goldenRuleIDs` 锁 + `GOVERNANCE-RULES-REGISTRATION-GUARD-01` |
| `CELL-LIFECYCLE-RANK-COMPLETENESS-01` | Medium | AST set-difference：每个 `CellLifecycle` 常量 ∈ 有序 `AllCellLifecycles()`（守 `CellLifecycleRank` 真实消费链） |
| outbox enum 单源（wire 字符串） | Medium | 删常量 + typed `OldestEligibleAt` 签名 + `OUTBOX-STATE-LITERAL-BAN-01`（adapters/postgres outbox 文件禁裸 status 字面量，`String()` 唯一 producer；scope 内零误报，列名 `*_at` 不 exact-match） |
| outbox 状态机转移 | Medium | `OUTBOX-STATE-TRANSITION-GUARD-01`（relay.go settlement 函数调 `Mark*` 时必须有匹配的 `Transition(_, target)`，Mark↔target 配对交叉校验）+ `OUTBOX-STATE-TRANSITION-COMPLETENESS-01`（每个 State const 必须是 `stateTransitions` 的 key，终态以空 slice key 自表达）。ClaimPending / ReclaimStale 无 `Mark*` 配对，由 SQL CAS 谓词 + conformance + LITERAL-BAN 守；SQL-text↔table-edge 绑定是 `...any` 天花板，明确接受 |

4 条新 archtest 各含盲区清单 + 反向自检（`missingKeys` / literal matcher 单元测试 + blind-spot shape 断言），活在各 archtest 的 godoc。LITERAL-BAN 是 SQL `...any` 实参形态下「status 来自 String()」可达的最高档（无法 type-constrain）。

## L3 概念一致性：为何引入第 4 套 lifecycle 词汇

代码库现有三个「lifecycle/phase」概念，本 PR 加第 4 个，**有意正交、不收敛**：

| 概念 | 取值 | 语义轴 | 载体 |
|------|------|--------|------|
| runtime `cellState` | New/Initialized/Started/Stopped | cell 在启停序列的位置（运行时） | `kernel/cell` 私有 |
| `cellvocab.ContractLifecycle`（合约） | draft/active/deprecated | Contract wire 稳定性 | `ContractMeta.Lifecycle` |
| `JourneyMeta.Lifecycle` | active/experimental | Journey 交付状态 | `JourneyMeta` |
| **`cellvocab.CellLifecycle`（本 PR）** | experimental→…→retired | cell/slice **成熟度** | `Cell/SliceMeta.Lifecycle` |

成熟度（"这个 cell 多 production-ready"）与合约稳定性（draft/active/deprecated）、运行时启停态语义不同，强行复用一套枚举会塌缩这些独立轴。命名上 Go 类型用 `CellLifecycle`（避免与 `cellvocab.ContractLifecycle` 撞名），YAML/wire key 用 `lifecycle`（与合约/journey 一致 + DTO name-correlation）；类型名与字段名分离已有先例（`consistencyLevel` YAML → `Level` 类型）。

## 威胁模型

| 威胁 | 覆盖 |
|------|------|
| cell.yaml 笔误 `lifecycle: stbale` | schema enum（测试期）+ governance CELL-LIFECYCLE-01（validate 期）+ 构造期 `ParseCellLifecycle` fail-fast（boot 期）三重 |
| slice 比 cell 更成熟（asset slice in experimental cell） | CELL-LIFECYCLE-01 slice≤cell（error，阻断） |
| 未来加 `CellLifecycle` 枚举漏配 `AllCellLifecycles()` 数组 → `CellLifecycleRank`=-1 静默破坏排序 | CELL-LIFECYCLE-RANK-COMPLETENESS-01（CI 红） |
| 未来加 outbox `State` 漏配转移表 → 静默死状态 | OUTBOX-STATE-TRANSITION-COMPLETENESS-01（CI 红） |
| 未来在 PG outbox SQL 写裸 `"published"` 而非 `State.String()` | OUTBOX-STATE-LITERAL-BAN-01（CI 红）。盲区：非 outbox-命名的新 SQL 文件（已记 godoc） |
| relay 新增 Go 侧 settlement 路径漏 `Transition` 配对断言 | OUTBOX-STATE-TRANSITION-GUARD-01。盲区：断言放在非 relay.go 文件（已记 godoc，真·Medium 天花板）。注：ClaimPending / ReclaimStale 无 `Mark*` 配对，不在 GUARD 覆盖范围，由 SQL CAS + conformance + LITERAL-BAN + COMPLETENESS 守卫 |

## Consequences

- **正向**：两处隐含状态显式化；outbox status 单源（删并行常量）；相位可在 catalog wire + 启动日志可观测；新增枚举漏配即 CI 红。
- **负向 / 接受代价**：`runtime/outbox.Store` 接口签名 breaking change（仅本仓实现，blast radius 有界，符合「无外部消费方」前提）；LITERAL-BAN / TRANSITION-GUARD 是 Medium（非 Hard）——SQL `...any` 与 Go 字符串比较的形态天花板，已诚实标注。
- **中性**：cell 相位是声明式有序 enum（非状态机）——与 outbox/saga/command 的运行时状态机刻意不同形，因其无运行时转移。
