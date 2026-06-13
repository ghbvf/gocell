# ADR: L3 Saga / Workflow 编排引擎

- 日期：2026-06-02
- 状态：Accepted
- 范围：framework-capability-roadmap **W6** / 实施计划 `docs/plans/202605230231-046-saga-l3-workflow-implementation-plan.md`（PR-00 … PR-10，本 ADR 是 PR-10 收尾交付物）
- 关联规则导航：`.claude/rules/gocell/saga.md`（archtest / governance 索引，单源）

> **本 ADR 与 046 实施计划的真值边界**：046 计划是**执行视图**（PR 切片 / LOC / ship 阶段），其顶部已声明「单条 PR 的最终行为以 PR body + ADR + 落地 godoc 为准」。本 PR 已修正 046 §2 的事实漂移（终态图 line 64 由 3 终态补全为 5 终态）并在 §2 加指向本 ADR 的权威指针；D 表与本 ADR D1–D8 对应，D2/D3/D6 的细节演进与新增 D9/D10 以本 ADR 为准。不存在两套真理源。

---

## 1. 上下文与问题

`kernel/cellvocab.L3 = WorkflowEventual` 这个 enum 早已存在，但运行时长期"名实分离"——没有 `runtime/workflow`，所有跨 cell 编排场景靠业务手撕事件消费 + 手工补偿。004 capability-gap 缺口 2 与产品 roadmap V11-3 要求补齐一个**可复用的 L3 saga 编排引擎**：声明步骤序列、自动正向驱动、失败时反向补偿、跨进程单 leader 驱动、durable 状态可 replay。

本 ADR 记录该引擎落地的 10 个关键决策（D1–D8 核心 + D9/D10 为 046 §2 凝固后追加）、拒绝的备选、终态模型、威胁矩阵与演进路径。每条决策附其 **enforcement 载体 + AI-robust 档位**，使决策可机器追溯到守卫，而非纯文字约定。

---

## 2. 决策 D1–D8

| # | 决策 | enforcement 载体 | AI-robust 档位 |
|---|------|-----------------|---------------|
| D1 | **编程式优先**：Step 是 Go func（`Run`/`Compensate`），声明式 YAML DSL 留 v1.2+。业务编排不可避免读外部 state、调多 ports，声明式覆盖率低且 YAML 反成新约束源 | 无机器守卫（设计取向）；contract `kind: saga` 只声明步骤元数据，行为在 Go | **Soft / 文档约定**（刻意——这是取向不是不变式） |
| D2 | **state journal = append-only**：每个状态变化一条 `journal.Event`，instance/step 状态是事件折叠的投影，非独立可变行。replay / forensic / time-travel 自然得到。journal 接口按消费者能力**窄接口拆分**（见 §3） | `journal.Journal`/`JournalCore`/`Enqueuer`/`Reader`/`ProducerReader`/`Heartbeater` 接口分层 + `SAGA-JOURNAL-HOLDER-SEAL-01`（仅 `runtime/saga.Coordinator` 可持有 `JournalCore`） | **Medium**（archtest 字段类型解析；Hard 路径 = sealed construction，gh **#982**；业务 cell 持 `JournalCore` 未机器拦截 = gh **#1415**） |
| D3 | **每 saga instance 单 leader**：distlock key 颗粒到 instance；无 leader 不驱动。leader 经 `runtime/distlock` 复用，含 per-lock `Orphan()` 优雅交接（graceful shutdown/handoff，bounded-TTL takeover） | `SAGA-DRIVE-BEHIND-LEADER-GATE-01`（`driveOne` 仅在 `tickOnce`/`acquireLead` 门控下调用）；`distlock.Lock.Orphan()` | **Medium**（AST selector + 控制依赖门控；Hard 路径 = typed gate token 穿入 `driveOne` 签名，gh **#1110**） |
| D4 | **Step dispatch 与 journal append 共享事务边界**，步骤**不在持有 DB 事务时执行**（避免长事务持锁）。与 L2 outbox-fact 模型同源 | `SAGA-STEP-RUN-OUTSIDE-TX-01`（`StepFunc` 调用只在 `safeRun`；任何传递可达 `safeRun` 的调用禁出现在 `RunInTx` closure 内） | A1 **Medium**（typed 签名身份扫描；别名/defined-type/裸签名逃逸结构免疫，gh **#979** 将旧 B1 折入 A1）/ A2 **Medium**（跨包传递 taint，含 helper/FuncLit-var 间接，gh **#980**）；真·Hard（StepContext/TxContext 能力类型分裂）成本不成比例**已否决**，gh **#1997** |
| D5 | **Compensate 必须幂等 + 纯反向**：不读外部 state、不持事务层。dtm/Temporal 实战经验：Compensate 带分支 = bug 温床 | `SAGA-STEP-COMPENSATE-PURE-01`（`CompensateFunc` 赋值槽函数体禁调 `outbox.Writer/Emitter`、`persistence.TxRunner`、`*sql.Tx`、`pgx.Tx`） | **Hard**（类型识别赋值槽 + `types.Implements` 识别禁用 receiver；import alias 无效） |
| D6 | **三层 timeout**：`Step.Timeout`（单步执行）/ `SagaDefinition.Timeout`（总）/ `Heartbeat`（长步续租）。复用 `kernel/command` 三层 timeout 模板。心跳由 `runtime/saga/executor` 的 per-step goroutine 独立维护，Coordinator **不持集中式心跳循环** | `SAGA-COORDINATOR-NO-HEARTBEAT-LOOP-01`（`Coordinator.journal` 收窄为无 `Heartbeat` 方法的 `JournalCore`）+ `SAGA-EXECUTOR-RAND-INJECTED-01`（jitter 源可注入） | **Hard 上游**（`JournalCore` 无 Heartbeat，集中循环 compile 不可表达）+ **Medium 下游**（callsite 残留扫描） |
| D7 | **L3 cell 必须声明 saga contractUsage**：`role: orchestrate` ⟹ `cell.yaml consistencyLevel: L3`（**单向**蕴含，L3 ≠ saga，详见 §5） | governance rule `SAGA-CELL-LEVEL-L3-DECLARE-01`（`gocell validate` PhaseBase CI gate） | **Medium**（governance rule） |
| D8 | **AfterCommit hook 仅 transient 副作用**：cache invalidation / metric / log / WS broadcast / saga dispatcher kick。allow stateful 必复活 outbox 想消灭的反模式 | `AFTERCOMMIT-HOOK-PURE-TRANSIENT-01`（PR-00 落地，funnel 白名单） | A1 **Hard**（callee=RegisterAfterCommit + arg=FuncLit 形态唯一）/ A2 **Medium**（closure body 禁调持久化接口，one-level helper 启发，深层链未覆盖）/ A3 **Hard 下游**（callsite allowlist）+ **Medium 上游**（五 runner 跨包，无 type-seal） |

**追加决策（046 §2 凝固后落地，本 ADR 首次正式记录）：**

| # | 决策 | enforcement | 档位 |
|---|------|-------------|------|
| D9 | **`StatusCompensationFailed` 独立终态**（#1210）：补偿阶段本身失败（≥1 个 `CompensateFunc` 返回非 nil）落入第 8 态，与 `StatusFailed`（前向失败无回滚）结构区分，运维只读终态即可辨根因。**废弃** 早期"dead-letter saga 表"方案 | `SAGA-STATUS-FANOUT-COVERAGE-01`（新 status/eventkind 必须同步四载体：conformance 终态 / readyz 状态表 / alerting kind legend / `TerminalEventKind` 映射） | **Hard**（codegen funnel + golden + keyless literal 编译穷举） |
| D10 | **构造器必填 interface 依赖 nil 守卫**：`NewCoordinator` / `NewExecutor` 的非 variadic interface 位置参数经 `validation.IsNilInterface` 守，`clock.Clock` 经 `clock.MustHaveClock` | `SAGA-CONSTRUCTOR-NIL-GUARD-01` | **Medium**（非 funnel 单轴；Hard 路径 = `gocell:"required"` tag funnel，gh **#1317**） |

---

## 3. journal 窄接口分层（D2 细化）

`kernel/saga/journal` 按消费者**最小能力**拆分接口，配合 `SAGA-JOURNAL-HOLDER-SEAL-01` 的"单一许可持有者"约束：

| 接口 | 能力 | 许可持有者 |
|------|------|-----------|
| `JournalCore` | 全协调面：`Enqueue` / `Append` / `Load` / `ClaimPending` / `MarkTerminal` / `RepoReady`（无 `Heartbeat`） | **仅** `runtime/saga.Coordinator` |
| `Heartbeater` | `Heartbeat`（lease 续租，#1209 拆出） | `runtime/saga/executor` per-step goroutine（声明自己同构的 `Heartbeater`，不 import kernel journal） |
| `Journal` | `JournalCore` + `Heartbeater`（PG / mem 实现满足全集） | 实现侧 |
| `Enqueuer` | 仅 `Enqueue` | 业务 producer（如下单 HTTP slice） |
| `Reader` | 仅 `Load` | 状态查询 slice |
| `ProducerReader` | `Enqueuer` + `Reader` | 既要登记又要读的业务 cell（结构上无法驱动 saga） |

> **已知盲区**：`SAGA-JOURNAL-HOLDER-SEAL-01` 当前只扫 `runtime/saga`；业务 cell（`cells/`、`examples/`）持有 `JournalCore` 尚未机器拒绝，gh **#1415** 追踪。

---

## 4. 终态模型（D9 扇出真值，机器守卫 `SAGA-STATUS-FANOUT-COVERAGE-01`）

> 下表是决策快照（便于 ADR 自包含阅读）。**权威渲染**在 `docs/ops/readyz.md` §Saga 与 `alerting-rules.md` §Saga 的生成区（受 fanout archtest 守）。本表**不在**生成区、不受 `SAGA-STATUS-FANOUT-COVERAGE-01` 自动守护——新增 status/eventkind 时需随 §8 演进手工跟随本表。

`saga.Status`（`kernel/saga/status.go`，iota+1，零值非法）8 个值，其中 5 个终态：

| Status | 值 | 阶段 | 终态 |
|--------|----|------|------|
| `Pending` | 1 | 未启动 | 否 |
| `Running` | 2 | 正向执行 | 否 |
| `Compensating` | 3 | 反向补偿中 | 否 |
| `Succeeded` | 4 | 全部步骤提交 | ✅ |
| `Failed` | 5 | 前向失败，未进补偿 | ✅ |
| `Compensated` | 6 | 回滚干净完成 | ✅ |
| `Expired` | 7 | 总超时 | ✅ |
| `CompensationFailed` | 8 | 回滚本身遇步骤失败（仅从 `Compensating` 可达） | ✅ |

`journal.EventKind`（`kernel/saga/journal/event.go`）11 个，终态事件经 `TerminalEventKind` 1:1 映射（`MarkTerminal` 专属写）。**状态表/kind legend 的权威渲染**活在 `docs/ops/readyz.md` §Saga（`saga-status-table` 生成区）与 `docs/ops/alerting-rules.md` §Saga（`saga-event-kind-legend` 生成区）——均由 `gocell generate saga-coverage` 单源生成，**禁手改**。

---

## 5. L3 ≠ Saga（单向蕴含澄清）

D7 是**单向**：用 saga 编排 ⟹ L3；但 L3 不等价于 saga。`accesscore` / `configcore` / `examples/todoorder/ordercell` 是 L3 查询投影 / CQRS，**不用** saga 引擎。`SAGA-CELL-LEVEL-L3-DECLARE-01` 只在 cell 存在 `kind: saga` contractUsage 时触发，不误伤投影型 L3 cell。详见 `.claude/rules/gocell/saga.md` §"L3 与 Saga 的关键澄清"。

---

## 6. 拒绝的备选

| 备选 | 拒绝理由 | 对应决策 |
|------|---------|---------|
| YAML DSL 第一版 | 业务编排读外部 state，DSL 覆盖率低，反成新约束源 | D1 |
| mutable instance row | 与 outbox/audit hash chain 不一致，丢 replay/forensic | D2 |
| 无 leader / 分片路由 | 多进程同驱一 instance 复杂化 lease fencing | D3 |
| in-proc 同步直调 Step.Run | 破坏与 L2 outbox-fact 一致性 + 事务安全 | D4 |
| 复杂分支 Compensate | dtm/Temporal 实证 = bug 温床 | D5 |
| 单层 Step timeout | 长步无续租机制 | D6 |
| 集中式心跳循环（Coordinator 持有） | 单点心跳 = 失败放大；per-step goroutine 隔离更稳 | D6 |
| **dead-letter saga 表** | 已被 `StatusCompensationFailed` 独立终态取代——终态值本身承载根因，无需第二张表 | D9 |

---

## 7. 威胁矩阵

| 威胁 | 当前补偿 | 覆盖 | 遗留 |
|------|---------|------|------|
| lease 失效 / leader 假死 | per-instance lease CAS + `Heartbeat` 续租 + `ClaimPending` 接管过期 lease；`gocell_saga_heartbeat_failed_total{reason="stale_lease"}` 可观测 | ✅ | — |
| 旧 leader 复活继续驱动 | lease fencing（旧 lease 的 `Append`/`MarkTerminal` 必 `ErrSagaStaleLease`）+ `SAGA-DRIVE-BEHIND-LEADER-GATE-01` | ✅ | gate token 仍 AST 门控（Hard 路径 gh #1110） |
| Compensate 读外部 state / 持事务 | `SAGA-STEP-COMPENSATE-PURE-01` Hard | ✅ | — |
| 补偿本身失败 | `StatusCompensationFailed` 终态 + `KindStepCompensationFailed`/`KindSagaCompensationFailed`；运维经 `saga_events` 人工介入（runbook `docs/ops/saga-runbook.md`） | ⚠️ | 无自动二级补偿（刻意）——人工 runbook 兜底；自动重试入口未做 |
| step 在持锁事务内长执行 | `SAGA-STEP-RUN-OUTSIDE-TX-01` A1 Medium（typed 签名身份 + executor-pkg sanction 绑定）+ A2 Medium（跨包传递 taint，含 callback-var sink） | ✅ | A2 helper/FuncLit-var/callback-var 传递链已覆盖（gh #980/#1998）；残留三类（func-literal 经函数参数间接 / method-value var / RunInTx 名字匹配）**完整清单以 archtest INVARIANT godoc 为单源**，均 SSA 范畴或保守可接受；真·Hard（能力类型分裂）成本否决 gh #1997 |
| journal 无界增长 | `Event.MaxPayloadBytes` 64KiB cap；append-only 增长需归档 | ⚠️ | **replay 设计已立项（accepted）**——ADR `202606051200-1609-adr-saga-journal-projection-source.md`（EPIC #1609，model-a）锁定 `saga_events` 投影源设计，**能力本身待 PR-02..06 落地**（#1609 PR-00 仅 ADR，尚未实现/可运维）；**归档/截断** 仍未做，且 #1609 D7 增约束「归档须 ≥ 最慢投影 checkpoint」 |
| Coordinator 无 leader 误并发 | `WithLeaderElect` option 注入 distlock；缺省 unsafe 模式 `Start()` 打 `UnsafeModeLabel` 警告 | ⚠️ | 刻意设计取舍（非待修缺陷，故无 issue）：unsafe 模式供单进程/开发；生产装配契约 = 必须经 `WithLeaderElect` 注入 leader |
| coordinator readiness 不可观测 | `ProbeCoordinatorReady` (`saga_coordinator_ready`) 已声明 | ❌ | **未 wired**——coordinator 尚非一等 Cell，cell-side `RegisterReadiness` 待 saga-as-cell 迁移（gh **#978**） |

> ⚠️/❌ 行的遗留项均有 gh issue 跟踪、属显式 out-of-scope（#1609 replay 设计已立项/能力待落地 + 归档未做 / 人工 runbook），或为刻意设计取舍（unsafe-mode leader）；无 silent 缺口。

**Amendment 2026-06-13（gh #979 + #980，威胁矩阵重评 D4 / §7「step 在持锁事务内长执行」行）：**
`SAGA-STEP-RUN-OUTSIDE-TX-01` 硬化后，A1 由「精确 `*types.Named` 匹配」改为 **typed 签名身份**（`types.Identical`）——
别名 / defined-type / 裸结构同签名的 StepFunc 调用全部结构性免疫，**旧 B1 反向自检折入 A1 后删除**；A1 评级由原 ADR 记的
「Hard」**校正为 Medium**（typed CI 扫描的本档；原「Hard」系对 typed scan 的过誉）。A2 由「闭包体直接 `safeRun()` 的 AST 扫描」
升为 **跨包 reverse-reachability taint**（coordinator + executor 双包，按 `types.Object` 身份），覆盖命名 wrapper 与
func-literal-var 间接（含 cx-1）。#1998 内审再补两处 typed 收口：A1 的 sanctioned range 由名字版改为**绑定 executor 包**
（`*types.Func` 身份，impostor 同名 safeRun 不再放行）；A2 的 RunInTx callback sink 解析**扩到 callback-var**（`cb := func(){…}; RunInTx(ctx, cb)`）。
**残留完整清单（三类：func-literal 经函数参数间接 / method-value var / RunInTx 名字匹配）以 archtest INVARIANT godoc 为单源**，
本 ADR 不再另抄以免漂移——均属 SSA 范畴或保守可接受。真·Hard 唯一载体（`StepContext`/`TxContext` 能力类型分裂，令在 `RunInTx`
闭包跑 step = 编译错）须重做 kernel 级 `persistence.TxRunner` 闭包签名、影响全仓 consumer，**成本不成比例已否决**，仅 gh **#1997** 跟踪。
安全模型不变：step 仍永不在持锁事务内执行，纵深 = A1 签名身份 + executor-pkg sanction + A2 跨包 taint + `safeRun` 在 executor 包未导出。

---

## 8. 演进路径

| 方向 | 触发条件 / 追踪 |
|------|----------------|
| saga-as-cell 迁移（coordinator 成一等 Cell + `saga_coordinator_ready` wiring） | gh **#978** |
| 业务 cell 持 `JournalCore` 机器拒绝 | gh **#1415** |
| `JournalCore` sealed construction（D2 上游 Hard 化） | gh **#982** |
| `driveOne` typed gate token（D3 下游 Hard 化） | gh **#1110** |
| `safeRun` helper/FuncLit-var 传递链覆盖（D4 A2） | **已落 → §Amendment 2026-06-13（#980）** |
| D4 真·Hard 化（StepContext/TxContext 能力类型分裂，令 step-in-tx 编译错） | gh **#1997**（成本否决，远期跟踪） |
| saga 构造器接入 `gocell:"required"` funnel（D10 Hard 化） | gh **#1317** |
| journal conformance codegen golden 枚举（实现自动入列 Hard 化） | gh **#1003** |
| 声明式 Saga DSL（D1 v2） | 业务场景 ≥ 3 个相似 saga 后立项 |
| 跨 cell child workflow / nested saga | v1.2+ ADR（outbox 触发新 saga 已覆盖，parent-child 引用 + 联合 compensate 待做） |
| 有界 tick 并发（一个 claim 批内并发驱动 claimed 实例） | **已落 → §Amendment 2026-06-07（#983）** |
| claim/drive 解耦的常驻 worker pool（消除批 barrier head-of-line） | tick 级有界并发已落地（§Amendment 2026-06-07）；进程内常驻 worker pool 待真实瓶颈再做 |
| Projection / Replay（从 `saga_events` replay 任意时点状态） | **已立项 → ADR `202606051200-1609-adr-saga-journal-projection-source.md`（EPIC #1609，model-a）**；原「W10 独立 wave」由该 ADR superseded |

---

## 9. ref

ref: docs/architecture/202605051600-adr-pg-outbox-fencing.md (in-repo) — adapted lease_id CAS fencing pattern
ref: dtm-labs/dtm dtmsvr/storage saga_branch.go — saga branch state + CompensateFailed
ref: temporalio/temporal service/history/workflow — workflow state machine
ref: ThreeDotsLabs/watermill components — outbox-driven message dispatch

---

## Amendment 2026-06-07 — 有界 tick 并发（#983）

### 决策

`tickOnce` 此前**串行**驱动一个 claim 批次（`for ci := range claimed { driveOne(ci) }`），把单个
tick 节流到 `ClaimBatchSize × step-latency`（默认 16×）。改为**并发 fan-out**：每个通过 leader gate
的 claimed 实例在自己的 goroutine 内 `driveOne`，**并发度 = 本批 claimed 实例数 ≤ `ClaimBatchSize`**
——claim 数即并发上限，无独立旋钮（见下「为何 claim 数 = 并发数」）。tick 仍 `wg.Wait` 整批完成后
才返回——**「一次一个批次」语义不变**，仅批内驱动并行化，因此 `Stop` 的 inflight drain（轮询
`inflightLocks` 至空）与 leader-elect 接管语义均不受影响。

载体：`sync.WaitGroup`（**不用 errgroup**——其 first-error 取消语义与「每个 driveOne 错误是记录并
继续、非致命」相悖；**不设并发 semaphore**——claim 批本身即并发上界，二次封顶要么不安全要么冗余，
见下）。

### 关键不变式

- **`acquireLead` + 浮 gate 保持串行**：`acquireLead(ctx, ci)` 与 `if !lead { continue }` 留在循环体内，
  每个 drive 在 spawn goroutine 前先过 leader gate（`SAGA-DRIVE-BEHIND-LEADER-GATE-01` A1/A2/A3 不变——
  唯一 `driveOne` callsite 仍词法位于 `tickOnce` 内的 goroutine 闭包；评级 Medium 不变，未被削弱）。
- **`inflightLocks.Store` 串行先于 `go`**：goroutine 可运行的瞬间 entry 已可见，关闭 Stop-drain 可见性缺口。
- **`wg.Done()` 为最外层 defer**：`inflightLocks.Delete` + `release()` 在其之前执行，`wg.Wait()`/drain 永不
  观测到半清理 entry。

### 为何 claim 数 = 并发数（单旋钮，无独立 MaxConcurrentDrives）

一个 claimed 实例当场即持 journal lease，而 lease 续租只由 `driveOne` 内部启动的 executor per-step
heartbeat 维持。因此 claim 数**必须**等于并发 drive 数：在已 claim 的整批上再套一个更小的 semaphore
（amendment 早期形态的 `MaxConcurrentDrives < ClaimBatchSize`）会让排队实例**持锁空等、无 heartbeat
续租**，前序慢 step 超过 `LeaseDuration` 后它们的 lease 已 stale（与 SQS 批量 receive + 慢处理的
visibility-timeout 坑同类）；而 `MaxConcurrentDrives ≥ ClaimBatchSize` 又使该 semaphore 永不阻塞 =
死代码、旋钮等价于 `ClaimBatchSize`。两种取值要么不安全、要么冗余，故 **#1714 删除该旋钮**：tickOnce
直接整批并发 drive，峰值并发 = `ClaimBatchSize`（=「想降并发就降 `ClaimBatchSize`」，与 `doc.go` /
`Config.ClaimBatchSize` godoc 一致）。真正「claim 批量 ≠ drive 并发」的解耦需常驻 worker pool
（claim-on-free-slot，有空闲 slot 才 claim 一个 → 持锁即驱动），对标 temporalio/sdk-go 的
slot-before-poll 与 kubernetes client-go workqueue 的 per-worker pull（队列项无 lease），属更大的
lifecycle 重构，见 §8 演进路径，本 PR 不折叠。

### deliberate 残留（非 silent 缺口）

1. tick `wg.Wait` 整批后才 claim 下一批 → 单个慢实例 head-of-line 阻塞下批 claim。
2. `acquireLead` 串行（非 issue 所指的慢 *step* 瓶颈，且维持 leader-gate AST 形态）。
3. drive 并发不可独立于 claim 批调（峰值并发恒 = `ClaimBatchSize`）——降并发只能降 `ClaimBatchSize`。

彻底消除① + 让 drive 并发独立于 claim 批（②/③），均需 claim/drive 解耦的常驻 worker pool（更大的
lifecycle 重构），见 §8 演进路径对应行——本 PR 刻意不折叠（YAGNI / 优雅简洁）。

### enforcement / AI-robust 评级

- 新并发不变式（「Store 串行先于 go」「wg.Done 最外层」「Observer 并发安全」）Go 类型系统无法表达；为
  其写按 defer 顺序匹配的 AST archtest 属 **Soft（章程严禁立项）**。其机器载体 = **`-race` + 真实触发
  竞态的 drain/并发测试（Medium）**：`TestTickOnce_DrivesConcurrently` /
  `TestTickOnce_ConcurrencyBoundedByClaimBatchSize` / `TestObserveDrive_ConcurrentCallsRaceSafe`（并发
  ObserveDrive）/ `TestStop_DrainsInflight` 在 `go test -race` 下运行，Store/defer 顺序或非并发安全
  Observer 回归即竞态报红。这是 Go 下该形状 enforcement 的天花板，与 leader-gate 的 Medium 同族
  （typed gate token Hard 化仍由 §8 / gh #1110 独立追踪）。
- **Observer 并发契约（#1714 F2）**：tick 并发驱动使 `executor.Observer` 的 `ObserveDrive` 及 step 级方法
  被多 goroutine 并发调用；接口 godoc 显式声明「实现 MUST 并发安全」，`SagaCollector`（OTel SDK
  instrument）满足，上面的 race 测试守回归。
- **panic 安全**：`executor.safeRun` / `safeRunCompensate` / `Coordinator.safeObserve` 三处 recover 使业务
  step/compensate/observer panic 转为 error，慢/坏 step 不会击穿 sibling drive——并发未引入 blast-radius
  回归；刻意不加 per-goroutine 兜底 recover（避免吞掉 charter 要求 surface 的 infra panic）。

### §7 威胁矩阵逐行重评（AI-robust「ADR amendment 落地必查」）

并发改动与威胁矩阵各行正交，**无格子从 ✅ 翻转**；并新**关闭**一条 amendment 早期版本曾引入的风险：

- 「claimed lease 空等无续租」（早期 `MaxConcurrentDrives < ClaimBatchSize` 形态，#1714 修复前）：本版
  **由构造消除**——claim 数 = 并发数，每个 claimed 实例立即 drive、heartbeat 即时启动，不再有 parked
  无续租 lease 可 stale。
- 「无 leader 误并发」「旧 leader 复活继续驱动」：`acquireLead` 仍串行门控**每个** drive，`ci.LeaseID`
  per-instance fencing CAS 不变——并发是 per-instance 隔离，不放宽 leader/lease 任一保证。
- 「step 在持锁事务内长执行」（`SAGA-STEP-RUN-OUTSIDE-TX-01`）/「Compensate 持事务」
  （`SAGA-STEP-COMPENSATE-PURE-01`）：step/compensate 执行路径未变，仍 tx-free / 纯净。
- 「lease 失效 / leader 假死」：每个并发 drive 仍由 executor per-step heartbeat 独立续租（executor 本就
  per-call 隔离、字段只读，并发安全）。

ref: `sync.WaitGroup` 批内 fan-out（无 semaphore——claim 批即并发上界）；claim/drive 解耦 worker pool
（slot-before-claim，持锁即驱动）的对标 = temporalio/sdk-go（slot-before-poll）/ kubernetes client-go
workqueue（per-worker pull，队列项无 lease）。
