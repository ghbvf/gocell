# 046 — Saga / L3 Workflow Engine 分阶段实施计划（2026-05-23）

> **范围**：004 capability-gap 缺口 2「L3 Saga / Workflow 运行时」+ 005 framework-capability-roadmap **W6** + 产品 roadmap 001 **V11-3 PR-V11-SAGA-KERNEL**。
> 一次性把"`cellvocab.L3` 名实分离"补齐——`runtime/workflow` 不存在、L3 场景全手撕的问题在本计划闭环。
>
> **真值源**：本文件是执行视图，单条 PR 的最终行为以 PR body + ADR + 落地 godoc 为准。条目演进回 PR / issue 改，merge 后同步勾选本文件 ✅。
>
> **PR 物理上限**：净增删 ≤ 2000 行（按 `git diff --numstat origin/develop | awk '{i+=$1; d+=$2} END{print i+d}'` 度量；下列不计入预算：codegen 派生产物、golden 文件、**跨 PR 复用的 conformance 套件**（`*test/` 包内挂在 interface 上、被多个实现 PR 复用的契约测试，如 `kernel/saga/sagajournaltest/conformance.go`））。下表所列 LOC 是预估上沿，单 PR（按上述口径）超 2000 行必须二次拆分。
>
> > **豁免登记（PR-02）**：conformance 套件与 golden/codegen 同属"摊销的测试基础设施"——`sagajournaltest/conformance.go`（~1171 行）由 PR-02 memjournal 与 PR-04 PG store 共同复用，且 contract-fanout 规则强制它与 interface 同 PR、不可拆分；它是安全网而非变更风险源。PR-02 计入预算口径下实现仅 688 行、含单测 1140 行，远低于 2000；conformance 套件按本豁免不计入。

---

## 0. 目标与不在范围

### 0.1 in scope
- `kernel/saga/` —— 状态机原语（`Step{Run, Compensate, Timeout, Retries}` + `SagaDefinition` + `SagaInstance` + `Journal` 接口）
- `kernel/persistence` 新增 `RunTxWithBestEffortAfterCommit` —— 004 W1 缺口 5（**Saga 引擎依赖**）
- `runtime/saga/` —— Coordinator（state journal + leader-elect 通过 `runtime/distlock` + 步骤 dispatch 通过 `kernel/outbox`）
- `adapters/postgres/saga/` —— `saga_instances` / `saga_events` 表 + 迁移 + lease_id CAS fencing（计划期暂记为 `saga_steps`，落地为 append-only `saga_events`）
- `contracts/saga/` schema + `tools/codegen` `kind: saga` 派生 typed `SagaDefinition`/`Step` 接口
- governance rules + archtest（FMT/REF/TOPO `kind:saga` ↔ `consistencyLevel:L3` 约束 + Coordinator funnel + Compensate 纯函数 archtest）
- example：`examples/orderfulfillment` 全链 saga journey
- ADR + godoc + ops runbook + CLAUDE.md 同步

### 0.2 out of scope（写明 blocker 否则进 backlog）
- **Projection / Replay**（W10）—— 需 Schema Registry（W5）+ Saga（本 PR），独立 Wave。
- **声明式 Saga DSL（YAML 描述编排）**—— 第一版只支持编程式（Go func 实现 `Step.Run/Compensate`）；声明式留 ADR §"演进路径"，无 enforcement 改动。
- **跨进程 child workflow**（Temporal 风格嵌套）—— 第一版单 saga = 单 instance；嵌套通过 outbox 事件触发新 saga，不引入 parent-child journal 关系。
- **Activity / Workflow worker pool 解耦**（Temporal 风格）—— 第一版 Coordinator 直接调度 Step，不分 Activity worker；触发条件 = step concurrency 出现明确瓶颈再做。

---

## 1. 现状盘点（必须先读，避免重复工作）

| 现状 | 文件 | 复用判断 |
|---|---|---|
| `cellvocab.L3 = WorkflowEventual` enum 已存在 | `kernel/cellvocab/levels.go:50` | ✅ 名词在，运行时缺 |
| `kernel/command` L4 状态机（Pending/Sent/Delivered/Succeeded/...）+ 三层 timeout + lease-based dequeue + sweeper | `kernel/command/{entry,advance,sweeper,queue}.go` + `doc.go` | ✅ **state machine 模板**：本计划 saga 设计骨架直接镜像该模式（lease + status transition + sweeper），不重新设计 |
| `runtime/distlock.Manager` 已实现 leader-elect（heap-based renewal、token fencing） | `runtime/distlock/{manager,locker,lock}.go` | ✅ Coordinator 直接拿来用，不二开 |
| `kernel/outbox.Emit[T any]` typed funnel + WireMessage envelope v1 + PG outbox fencing（`lease_id` CAS）| `kernel/outbox/{emit,envelope}.go` + ADR `202605051600-adr-pg-outbox-fencing.md` | ✅ Saga step dispatch 走 outbox，envelope 加 `kind=saga.step` 头 |
| `persistence.TxRunner.RunInTx(ctx, fn)` | `kernel/persistence/tx.go:18` | ⚠️ **缺 AfterCommit hook**（gap 5 / W1，本计划 **PR-00** 先补） |
| `kernel/idempotency.Claimer`（两阶段 Claim/Commit/Release） | `kernel/idempotency/...` | ✅ Coordinator 处理重复 step 触发用同一原语 |
| PG outbox `lease_id` UUID fencing（旧 worker mark 必失败的 CAS）| ADR `202605051600` | ✅ Saga step 出战 lease 完全复用同一形态 |
| 现有 L3 cell（**auditcore**）实际是 L2 outbox-fact 消费者驱动的 eventual 投影 | `cells/auditcore/...` | ⚠️ 本计划 ship 后**不强制**迁回 saga 模型；L3 saga 是给后续 enrollment/inventory/orderfulfillment 等编排型用例 |

---

## 2. 设计骨架（一句话总结）

> **权威指针**：本节是计划期设计骨架快照。落地后的权威决策（D1–D10 + enforcement 档位 + 威胁矩阵 + 演进路径）见 ADR `docs/architecture/202606021000-adr-saga-l3-orchestration-engine.md`；与下文冲突时以 ADR + 代码 godoc 为准。

```
Saga Instance (PG/mem) ── lease_id CAS ──► Coordinator (leader-elect via distlock)
        │
        │  for each Step in definition.Steps:
        │      ┌──► Step.Run(ctx, state)
        │      │      ├─ ok  → Journal.MarkStepSuccess + outbox.Emit("saga.step.completed")
        │      │      └─ err → retry up to Step.Retries
        │      │              └─ exhausted → trigger compensation
        │      │
        │      └──► (on saga failure) reverse iterate completed steps:
        │              Step.Compensate(ctx, state)
        │
        ▼
Journal.MarkSagaTerminal(Succeeded | Failed | Compensated | Expired | CompensationFailed)
```

> 终态共 5 个（`saga.Status` 8 态中的终态子集）：`Succeeded` / `Failed`（前向失败无回滚）/ `Compensated`（回滚干净）/ `Expired`（总超时）/ `CompensationFailed`（回滚本身失败，#1210 新增）。早期"dead-letter saga 表"方案已被 `CompensationFailed` 独立终态取代。

**关键决策**（每条决策都对应一个 ADR 段落）：

| # | 决策 | 拒绝的备选 | 理由 |
|---|---|---|---|
| D1 | **编程式（Go func Step）** 优先；声明式 DSL 留 v1.2+ | YAML DSL 第一版 | 业务编排不可避免读外部 state、调多个 ports；声明式覆盖率低，YAML 反而成新约束源 |
| D2 | **state journal = append-only**（每个状态变化一行），instance/step 状态 = 投影 | mutable instance row | 与现有 outbox / audit hash chain 一致；replay / forensic / time-travel 自然得到 |
| D3 | **每个 saga instance 单 leader**（distlock key = `saga:{definitionID}:{instanceID}`） | 无 leader / 分片路由 | 反应式编排里多个进程同时驱动同一 instance 会复杂化 lease fencing；现有 distlock 复用零成本 |
| D4 | **Step dispatch 走 outbox**（payload 含 step 名 + input snapshot），不直接 in-proc 调 〔计划期形态；实际 step 执行/事务边界以 ADR D4 + `SAGA-STEP-RUN-OUTSIDE-TX-01` 为准〕 | 直接同步调用 Step.Run | 一致性：与 L2 outbox-fact 模型同源；transactional safety：journal append + outbox emit 共享同一 Tx |
| D5 | **Compensate 必须幂等 + 纯反向**（不读外部 state）| 复杂 Compensate（含分支） | dtm / Temporal 实战经验：Compensate 有分支 = bug 温床；archtest 静态拦 |
| D6 | **三层 timeout**：`Step.Timeout`（执行）/`SagaDefinition.Timeout`（总）/`Heartbeat`（长 Step 续期） | 单层 Step timeout | 复用 `kernel/command` 三层 timeout 模板（已 production-proven） |
| D7 | **L3 cell.yaml** 强制声明 `saga.*` contractUsage，governance rule 拦 | 让 L3 cell 隐式可用 | 与 `consistencyLevel` 名实对齐：L3 不持有 saga 声明 = governance error |
| D8 | **AfterCommit hook**（W1 / gap 5）**仅 transient 副作用**（cache invalidation、metric、log、WS broadcast、saga 内 dispatcher kick）| 通用 stateful hook | 004 已识别"诱惑陷阱"——allow stateful 必复活 outbox 想消灭的反模式；archtest funnel 白名单 |

对标参考（每个 PR commit message 必填 `ref:`）：

| 形态 | 对标 |
|---|---|
| Saga state machine + compensation | `dtm-labs/dtm` `dtmsvr/storage` (`saga_branch.go`)；`temporalio/temporal` `service/history/workflow` |
| Activity timeout 三层 | `temporalio/sdk-go` `internal/internal_workflow.go`（ScheduleToStart / StartToClose / Heartbeat）|
| Outbox-driven step dispatch | `ThreeDotsLabs/watermill` `components/saga/`（基于 message handler）|
| Leader-elect coordinator | `temporalio/temporal` `service/history` shard ownership；已落地的 `runtime/distlock` 直接复用 |
| Journal append-only | `eventuate-foundation/eventuate-client-java` `saga-orchestration` event sourcing |

---

## 3. PR 序列总览（10 PR × ≤ 2000 LOC）

```
PR-00 ─┐
       │ AfterCommit 钩子（W1 / gap 5）
PR-01 ─┴──► kernel/saga skeleton（types + state machine）
              │
PR-02 ────────► kernel/saga/journal（接口 + memjournal）
                 │
PR-03 ───────────► runtime/saga Coordinator（单进程，无 leader）
                    │
PR-04 ───────────────► adapters/postgres/saga（PG Journal + 迁移）
                       │
PR-05 ───────────────────► runtime/saga leader-elect（套 distlock）
PR-06 ───────────────────► runtime/saga executor（重试 + heartbeat + per-step timeout）
                          │
PR-07 ────────────────────► contracts/saga schema + contractgen kind:saga 派生
                             │
PR-08 ────────────────────────► governance + archtest（FMT/REF/TOPO + funnels）
                                │
PR-09 ────────────────────────────► example orderfulfillment + journey + ADR + docs
PR-10 ────────────────────────────► ops runbook + CLAUDE.md + capability-inventory 更新
```

依赖结构上 PR-00 → PR-01 → PR-02 → {PR-03, PR-04} 并行 → PR-05/PR-06 并行 → PR-07 → PR-08 → PR-09/PR-10 并行。

---

## 4. 逐 PR 详细切片

> 每个 PR 章节按 **ship 9 阶段** 组织。每个 PR 走完 ship 全流程后再启下一个。
> speckit 阶段映射：spec/clarify/plan/tasks/analyze/implement 由 ship 阶段 1+2（探索+计划）覆盖；本计划本身已替代 speckit 顶层 spec.md。

### PR-00 · AfterCommit 钩子（gap 5 / W1 前置）

**LOC 预估**：800–1100（含 archtest funnel + golden）
**Ship level**：L2（单 explorer + 1 reviewer）—— 改动局限 `kernel/persistence` + adapter

| 阶段 | 内容 |
|---|---|
| 1 探索 | 1 explorer：对标 `eventuate-foundation` afterCommitHooks + `spring-tx` `TransactionSynchronizationManager.registerSynchronization`，提取签名/失败语义 |
| 2 计划 | 新增 `persistence.TxRunner.RunInTxAfterCommit(ctx, fn, hooks...)`（**不**改 `RunInTx` 现有签名）；Hook 签名 `func(ctx context.Context)` **无返回值**；archtest 限定 Hook 内不能调任何 `*sql.Tx`/`pgx.Tx`/`outbox.Writer` |
| 3 worktree | `worktrees/200-aftercommit-hook` from `origin/develop` |
| 4 TDD | `tx_aftercommit_test.go`：commit 成功 → hooks 全跑；commit 失败 → hooks 不跑；hook panic → 不影响 commit；hook 错误（如有）→ 仅 slog.Warn，不回滚 |
| 5 实施 | `kernel/persistence/tx.go`（接口扩 1 方法）+ `adapters/postgres/tx_manager.go`（实现，hooks 注册到 commit 后异步 goroutine）+ `kernel/persistence/cell_marker.go` 包装 |
| 6 PR | title: `feat(persistence): add RunInTxAfterCommit hook for transient post-commit side effects (gap 5)` |
| 7 review | 1 reviewer（diff < 1200）覆盖六维度 |
| 8 fix | 预计 Cx2 finding：archtest 必须 callsite-level（funnel 形态 single sanctioned holder） |
| 9 人工确认 | 列出 AFTERCOMMIT-HOOK-PURE-TRANSIENT-01 archtest 白名单首批 |

**Files**：
- `kernel/persistence/tx.go` +30
- `kernel/persistence/aftercommit.go` +80（新 Hook 类型 + 错误处理） +120 test
- `adapters/postgres/tx_manager.go` +150 +200 test
- `tools/archtest/aftercommit_pure_transient_test.go` +250（新）
- `docs/architecture/202605230300-adr-aftercommit-hook-narrow-scope.md` +180

**ref**：`ref: eventuate-foundation/eventuate-client-java AfterCommitHook + spring-tx TransactionSynchronizationManager`

---

### PR-01 · `kernel/saga` skeleton（state machine + types）

**LOC 预估**：1500–1800
**Ship level**：L3（默认 3 explorer + 1-2 reviewer）—— 引入新原语，需要审视

| 阶段 | 内容 |
|---|---|
| 1 探索（3 并行） | (a) 对标 dtm `saga_branch.go` + temporal `service/history` state machine 提取 enum/transition；(b) 测试策略：state machine table-driven test（参考 `kernel/command/advance.go`）；(c) 边界：concurrent advance、panic recovery、Compensate 失败 |
| 2 计划 | 类型骨架：`SagaDefinition{ID, Steps, Timeout, RetryPolicy}` / `Step{Name, Run, Compensate, Timeout, RetryPolicy}` / `Instance{ID, DefinitionID, State, CurrentStep, StartedAt, ...}` / `Status` enum（`Pending` / `Running` / `Compensating` / `Succeeded` / `Failed` / `Compensated` / `Expired`）。**不**含 Journal/persistence（PR-02）。**不**含 runner（PR-03）。 |
| 3 worktree | `worktrees/060-saga-kernel-skeleton` |
| 4 TDD | `advance_test.go` table-driven: 所有合法 transition + 所有非法 transition fail-closed；`compensate_test.go`：reverse order + idempotency invariant |
| 5 实施 | `kernel/saga/{doc.go, status.go, step.go, definition.go, instance.go, advance.go, errors.go}` |
| 6 PR | title: `feat(kernel/saga): state machine + Step/Definition primitives (W6 step 1/10)` |
| 7 review | ≥ 1500 → 3 reviewer（架构/测试 + 安全/产品 + 运维/DX）|
| 8 fix | 预计 Cx1：state enum 漏 transition；Cx2：Step.Compensate 接口签名应该返回 `error` 而非 panic |
| 9 人工确认 | 与 PR-V11-SAGA 章程对齐：编程式优先、Compensate 幂等约束 |

**Files**：
- `kernel/saga/doc.go` +120（含 ref / 设计骨架 godoc）
- `kernel/saga/status.go` +80 +150 test
- `kernel/saga/step.go` +100 +200 test
- `kernel/saga/definition.go` +120 +180 test
- `kernel/saga/instance.go` +100 +150 test
- `kernel/saga/advance.go` +180 +300 test
- `kernel/saga/errors.go` +60

**ref**：`ref: dtm-labs/dtm dtmsvr/storage/sql/saga_branch.go + temporalio/temporal service/history/workflow/state_rebuilder.go`

---

### PR-02 · `kernel/saga/journal` 接口 + memjournal

**LOC 预估**：1400–1700
**Ship level**：L3

| 阶段 | 内容 |
|---|---|
| 1 探索（3 并行） | (a) 对标 outbox `Writer/Store` 接口 + 现有 `runtime/audit/ledger.Store` append-only 实现；(b) 测试策略：conformance test pattern（仿 `kernel/outbox/outboxtest`）；(c) 边界：concurrent Append、ClaimPending lease 冲突、ReplayFromVersion |
| 2 计划 | 接口：`Journal.Append(ctx, instanceID, event) → version`；`Journal.Load(ctx, instanceID) → []Event`；`Journal.ClaimPending(ctx, leaseDuration) → ([]Instance, leaseID)`；`Journal.Heartbeat(ctx, instanceID, leaseID)`；`Journal.MarkTerminal(ctx, instanceID, leaseID, finalStatus)`。Event 类型：`StepStarted`/`StepCompleted`/`StepFailed`/`StepCompensated`/`SagaTerminal`。Append-only：D2 决策落地。 |
| 3 worktree | `worktrees/061-saga-journal` |
| 4 TDD | `memjournal_test.go` + 新建 `sagajournaltest/conformance.go` —— 任何 Journal 实现都跑同一组 conformance |
| 5 实施 | `kernel/saga/journal/{interface,event,memjournal,memjournal_test}.go` + `kernel/saga/sagajournaltest/conformance.go` |
| 6 PR | title: `feat(kernel/saga): append-only Journal interface + memjournal + conformance suite (W6 step 2/10)` |
| 7 review | 2 reviewer |
| 8 fix | Cx2：lease_id 类型应该 alias `idutil.SafeID` 不能裸 string |
| 9 人工确认 | conformance suite 是否齐全（leader handoff、stale lease reject）|

**Files**：
- `kernel/saga/journal/doc.go` +80
- `kernel/saga/journal/interface.go` +120 +60 test（compile-time assertion）
- `kernel/saga/journal/event.go` +180 +200 test
- `kernel/saga/journal/memjournal.go` +280 +320 test
- `kernel/saga/sagajournaltest/conformance.go` +400（real-failure injection）
- `kernel/saga/journal/errors.go` +50

**ref**：`ref: eventuate-foundation/eventuate-client-java EventStore + ThreeDotsLabs/watermill components/forwarder`

---

### PR-03 · `runtime/saga` Coordinator（单进程，无 leader）

**LOC 预估**：1500–1800
**Ship level**：L3
**依赖**：PR-00（AfterCommit）+ PR-01 + PR-02

| 阶段 | 内容 |
|---|---|
| 1 探索（3 并行） | (a) 对标 Watermill saga component + dtm `dtmsvr/svr.go` engine loop；(b) 测试策略：integration test 包含 mock TxRunner + memjournal + memoutbox；(c) 边界：Coordinator 重启后 resume from journal、Step.Run panic → 自动 retry 计数 + 退避、saga 总 timeout 触发 → 强制 compensate |
| 2 计划 | `Coordinator{journal, outbox, txrunner, clock}`；核心 loop `Tick(ctx)`：(1) `ClaimPending` → 拿 leases；(2) 对每个 instance Load 状态 + 算 next step；(3) 执行 step（同 Tx 中：journal.Append + outbox.Emit）；(4) commit + AfterCommit dispatcher.kick；(5) Heartbeat 续 lease。**不**含 leader-elect（PR-05）。**不**含 retry policy 复杂逻辑（PR-06，本 PR retry = 0 / fail-fast）。 |
| 3 worktree | `worktrees/062-saga-coordinator` |
| 4 TDD | `coordinator_test.go`：8 个 scenario（happy path / step fail / compensate / timeout / heartbeat / claim contention / resume / panic recovery）|
| 5 实施 | `runtime/saga/{doc.go, coordinator.go, dispatcher.go, errors.go}` + integration test |
| 6 PR | title: `feat(runtime/saga): Coordinator engine (single-process, append-only journal) (W6 step 3/10)` |
| 7 review | 3 reviewer |
| 8 fix | Cx1：commit + outbox.Emit 必须同 Tx，AfterCommit hook 之外**不能**做任何 mutation |
| 9 人工确认 | 单进程 Coordinator 不能跑生产（明确 docs 警告 + readyz probe 暴露"unsafe_no_leader"标签）|

**Files**：
- `runtime/saga/doc.go` +140
- `runtime/saga/coordinator.go` +400 +500 test
- `runtime/saga/dispatcher.go` +150 +200 test
- `runtime/saga/errors.go` +50
- integration `runtime/saga/integration_test.go` +400

**ref**：`ref: ThreeDotsLabs/watermill components/saga + dtm-labs/dtm dtmsvr/svr.go`

**Round-2 fixes (post-review, #977)**：12 finding 全部本 PR 收口（C1 fail-fast / C2 deadline+drain / C3 PII / C4 archtest scope / C5 atomicity testfake）。详见 `/Users/shengming/.claude/plans/c1-definition-abstract-nova.md`。关键决策：F5 删除 `Step.Compensate` 字段 + `CompensateFunc` 类型 + `SAGA-STEP-COMPENSATE-PURE-01` archtest（PR-06 重新引入，含 named-function resolution）；F3 Stop minimal drain（等 activeLeases 归零，复用 stopCtx）；F8 引入 `stagedJournal` test wrapper 实现真原子性测试。

---

### PR-04 · `adapters/postgres/saga` Journal 实现

**LOC 预估**：1500–1800
**Ship level**：L3
**可与 PR-03 并行**（不同包，无 import 交叉）

| 阶段 | 内容 |
|---|---|
| 1 探索 | (a) 对标 `adapters/postgres/outbox`（lease_id CAS pattern）+ Temporal `common/persistence/sql/sqlplugin`；(b) 迁移工具：复用 `tools/pg-migrate`；(c) 边界：concurrent ClaimPending（FOR UPDATE SKIP LOCKED 模式）+ heartbeat 超时 lease 回收 |
| 2 计划 | 表 `saga_instances`：id / definition_id / status / current_version / lease_id / lease_expires_at / started_at / updated_at / payload_state（JSONB）。表 `saga_events`：instance_id / version / kind / step_name / payload（JSONB） / created_at；`PRIMARY KEY (instance_id, version)`。Migration `040_create_saga_tables.up.sql` / `.down.sql`。 |
| 3 worktree | `worktrees/063-saga-pg-journal` |
| 4 TDD | integration test（testcontainers PG）：跑 `kernel/saga/sagajournaltest.RunConformance`；lease_id CAS 旧 worker 写失败 |
| 5 实施 | `adapters/postgres/saga/{store.go, migrations/040_create_saga_tables.up.sql, .down.sql}` |
| 6 PR | title: `feat(adapters/postgres/saga): durable Journal with lease_id CAS fencing (W6 step 4/10)` |
| 7 review | 3 reviewer |
| 8 fix | Cx2：JSONB payload 索引；Cx2：迁移必须可逆 |
| 9 人工确认 | 是否需要 `saga_events` partition by month |

**Files**：
- `adapters/postgres/saga/store.go` +500 +600 test
- `adapters/postgres/saga/migrations/040_create_saga_tables.up.sql` +80
- `adapters/postgres/saga/migrations/040_create_saga_tables.down.sql` +20
- `adapters/postgres/saga/migrations_test.go` +60
- `adapters/postgres/saga/integration_test.go` +400

**ref**：`ref: adapters/postgres/outbox/store.go (lease_id CAS pattern) + temporalio/temporal common/persistence/sql/sqlplugin/postgresql`

---

### PR-05 · `runtime/saga` leader-elect（套 distlock）

**LOC 预估**：800–1100
**Ship level**：L2
**依赖**：PR-03

| 阶段 | 内容 |
|---|---|
| 1 探索 | 1 explorer：复用 `runtime/distlock.Manager`，提取 lock key 命名约定 + ttl 与 lease_expires_at 关系 |
| 2 计划 | Coordinator 上包一层 `LeaderElectCoordinator{inner, distlockManager, clock}`；启动期对每个新 ClaimPending 来的 instance 申请 distlock key=`saga:{definitionID}:{instanceID}`；获不到锁 → skip；持锁期间 heartbeat 同步续 distlock + journal lease |
| 3 worktree | `worktrees/064-saga-leader` |
| 4 TDD | 双 process 模拟（goroutine + 不同 distlock client）：同 instance 只有一个进程 advance |
| 5 实施 | `runtime/saga/leader_elect.go` + integration test 与 PR-04 PG store 一起跑 |
| 6 PR | title: `feat(runtime/saga): leader-elect coordinator wrapping distlock (W6 step 5/10)` |
| 7 review | 2 reviewer |
| 8 fix | Cx2：lock 失释（process crash）→ TTL 兜底；Cx2：distlock manager 注入必须 fail-fast nil |
| 9 人工确认 | distlock key 命名规则确认 |

**Files**：
- `runtime/saga/leader_elect.go` +250 +350 test
- `runtime/saga/leader_elect_integration_test.go` +280

**ref**：`ref: runtime/distlock/manager.go (in-repo) + temporal service/history shard ownership`

---

### PR-06 · `runtime/saga/executor` retry + heartbeat + per-step timeout

**LOC 预估**：1500–1800
**Ship level**：L3
**可与 PR-05 并行**（executor 独立子包）

| 阶段 | 内容 |
|---|---|
| 1 探索（3 并行） | (a) 对标 temporal `internal/internal_workflow.go` retry policy + jitter；(b) 测试策略：clock injection（已有 `kernel/clock`）跑确定性 backoff 序列；(c) 边界：retry 耗尽 → 触发 compensation；Step.Run 中途 cancel；heartbeat 错过 → 视为 step failed |
| 2 计划 | `Executor{step, retryPolicy, clock, journal, heartbeatInterval}`；`Execute(ctx, instance, step) → Result`；retry policy = exponential backoff + jitter（max attempts / max interval）；per-step timeout 用 `context.WithTimeout` + journal Heartbeat goroutine。**重新引入 `Step.Compensate` 字段 + `CompensateFunc` 类型**（PR-03 round-2 删除）并实现 dispatch；同步重新引入 `SAGA-STEP-COMPENSATE-PURE-01` archtest，覆盖 function-literal AND named-function（`info.ObjectOf(ident).(*types.Func)` 解析 + recursive body scan）AND 通过 CompensateFunc-typed 参数传入的命名函数；新增红 fixture `red_named_func/`。 |
| 3 worktree | `worktrees/065-saga-executor` |
| 4 TDD | `executor_test.go`：retry 序列正确性（deterministic clock）/ heartbeat goroutine cleanup / Compensate 失败处理 |
| 5 实施 | `runtime/saga/executor/{executor.go, retry_policy.go, heartbeat.go}` |
| 6 PR | title: `feat(runtime/saga/executor): retry + heartbeat + per-step timeout (W6 step 6/10)` |
| 7 review | 3 reviewer |
| 8 fix | Cx1：jitter 不能用 math/rand 全局 seed；Cx2：heartbeat goroutine 必须 select on ctx.Done() |
| 9 人工确认 | retry policy 配置形态（per-step vs global）|

**Files**：
- `runtime/saga/executor/executor.go` +350 +400 test
- `runtime/saga/executor/retry_policy.go` +200 +250 test
- `runtime/saga/executor/heartbeat.go` +180 +250 test

**ref**：`ref: temporalio/sdk-go internal/retrypolicy.go + temporal common/backoff/retrypolicy.go`

---

### PR-07 · `contracts/saga/` schema + `contractgen` `kind: saga` 派生

**LOC 预估**：1500–1800
**Ship level**：L3
**依赖**：PR-01 + PR-02（kernel 接口稳定后才好 codegen）

| 阶段 | 内容 |
|---|---|
| 1 探索 | (a) 对标 `contracts/event/`、`contracts/http/` 现有 schema + `tools/codegen/contractgen`；(b) 测试策略：codegen golden test；(c) 边界：schema $ref 引用 / shared/errors 复用 |
| 2 计划 | `contracts/saga/{name}/v1/saga.yaml` schema 字段：`id` / `kind: saga` / `steps[]{name, input/output schema $ref, timeout, retries}` / `timeout` / `compensationOrder: reverse` (default)。`contractgen` 派生 typed `SagaDefinition_{Name}` + `Step_{Name}_{StepName}_Input/Output` struct + `Register(ctx, registry, impl SagaImpl_{Name})` 注册函数 |
| 3 worktree | `worktrees/066-saga-contractgen` |
| 4 TDD | golden test：取 example `orderfulfillment.yaml` → 对比 generated 输出；schema validation：缺 `steps` / step 无 `name` → validate 拒 |
| 5 实施 | `contracts/saga/_schema.yaml`（meta-schema）+ `tools/codegen/contractgen/saga/{generator.go, templates/}` + `gocell validate` 加 `kind:saga` 支持 |
| 6 PR | title: `feat(contractgen): kind:saga schema + typed SagaDefinition codegen (W6 step 7/10)` |
| 7 review | 3 reviewer |
| 8 fix | Cx2：schema 必须 `additionalProperties:false` for request；response 允许扩展 |
| 9 人工确认 | typed Step input/output schema 是否复用 `contracts/shared/` mixin |

**Files**：
- `contracts/saga/_schema.yaml` +200
- `tools/codegen/contractgen/saga/generator.go` +400 +400 test
- `tools/codegen/contractgen/saga/templates/saga.tmpl` +250
- `tools/codegen/contractgen/saga/golden/orderfulfillment.golden.go` +300（codegen 派生产物，**不计入 PR 行数预算**——但仍要 commit）
- `kernel/governance/rules/saga_rules.go` +180 +250 test（新 FMT-SAGA / TOPO-SAGA）

**ref**：`ref: tools/codegen/contractgen/event/generator.go (in-repo) + go-zero goctl api`

---

### PR-08 · governance + archtest

**LOC 预估**：1200–1500
**Ship level**：L3
**依赖**：PR-03/04/06/07 全部稳定

| 阶段 | 内容 |
|---|---|
| 1 探索（3 并行） | (a) 复用 `tools/archtest` Hard 范本（typed function call / single sanctioned holder）；(b) 测试策略：archtest 自带反向 self-test；(c) 边界：codegen 派生 vs 业务手写的边界 |
| 2 计划 | 新增 archtest：（i）`SAGA-CONSTRUCTOR-NIL-GUARD-01`：所有 `runtime/saga.NewXxx` 必须有 `if x == nil { return nil, err }` 顶层校验（Hard 范本第 N 条 "Service 构造模式 fail-fast on nil"）；（ii）`SAGA-STEP-COMPENSATE-PURE-01`：Compensate 函数体不能调任何 `*sql.Tx` / `pgx.*` / 写 outbox（**Compensate 必须纯反向**——D5）；（iii）`SAGA-JOURNAL-HOLDER-SEAL-01`：`runtime/saga.Coordinator` 是唯一持有 `kernel/saga/journal.Journal` 字段的 struct（single sanctioned holder）；（iv）`SAGA-CELL-LEVEL-L3-DECLARE-01`：cell.yaml `consistencyLevel: L3` ↔ 至少一个 slice `contractUsages` 含 `role: orchestrate`（governance rule） |

> Note: 2 of these 4 archtests (SAGA-STEP-RUN-OUTSIDE-TX-01, SAGA-JOURNAL-HOLDER-SEAL-01) were pulled forward to PR-03 (#977) since the lockable type surface materializes there.
>
> SAGA-STEP-COMPENSATE-PURE-01 was also initially pulled forward to PR-03 but **removed in PR-03 round-2** along with `Step.Compensate` field and `CompensateFunc` type: PR-03 does not execute compensation, so exposing the field would have been a soft fallback ("reserved + Validate rejects non-nil") that violates the AI-HARD principle (`feedback_no_soft_fallback.md`). Removing the field makes "Compensate present on a Step" Go-compile-time inexpressible (Hard) — strictly stronger than archtest+Validate guard. PR-06 re-adds `Step.Compensate` + `CompensateFunc` when wiring the compensation executor, and re-introduces `SAGA-STEP-COMPENSATE-PURE-01` archtest at that time — with the named-function-resolution path baked in from day one (no Soft fallback like the PR-03 version had).
>
> PR-08 now owns: SAGA-CONSTRUCTOR-NIL-GUARD-01 + SAGA-CELL-LEVEL-L3-DECLARE-01.
> PR-06 owns: SAGA-STEP-COMPENSATE-PURE-01 (re-introduction).
| 3 worktree | `worktrees/067-saga-archtest` |
| 4 TDD | 反向 self-test：故意写违反每条规则的 fixture，archtest 必须 FAIL |
| 5 实施 | 4 个 archtest + 1 governance rule（TOPO-SAGA-L3-DECLARE） |
| 6 PR | title: `feat(archtest,governance): saga funnels (constructor/compensate-purity/journal-seal/L3-declare) (W6 step 8/10)` |
| 7 review | 2 reviewer（archtest 主题集中）|
| 8 fix | Cx2：archtest Compensate 纯函数检测要走 typed-aware（`*types.Info.Selections`）|
| 9 人工确认 | 评级：每条 archtest 要明确 Hard / Medium；Soft 严禁立项 |

**Files**：
- `tools/archtest/saga_constructor_nil_guard_test.go` +200
- `tools/archtest/saga_compensate_pure_test.go` +280
- `tools/archtest/saga_journal_holder_seal_test.go` +220
- `tools/archtest/saga_l3_declare_test.go` +180
- `kernel/governance/rules/topo_saga_l3_test.go` +250
- `.claude/rules/gocell/saga.md` +120（新规则文件，记录所有 saga 强约束）

> **更新（#1213 / PR #1280）**：saga 主题 archtest 已合并入单一
> `tools/archtest/saga_invariants_test.go`，并由 `SAGA-INVARIANTS-FILE-CONSOLIDATED-01`
> 守卫强制单文件布局。上列已落地的 `saga_compensate_pure_test.go` /
> `saga_journal_holder_seal_test.go` 现为该合并文件内的 section。PR-08 新增的
> saga archtest（`SAGA-CONSTRUCTOR-NIL-GUARD-01` / `SAGA-CELL-LEVEL-L3-DECLARE-01` 等）
> **必须直接写入 `saga_invariants_test.go`**，不能新建独立 `saga_*_test.go`——否则
> 守卫报红。

**ref**：`ref: .claude/rules/gocell/ai-robust.md Hard 范本目录 (in-repo)`

---

### PR-09 · example `examples/orderfulfillment` L3 saga journey

**LOC 预估**：1500–1800
**Ship level**：L3
**依赖**：PR-07（contractgen 已能 emit typed SagaDefinition）+ PR-08（archtest 已能拦劣化）

| 阶段 | 内容 |
|---|---|
| 1 探索（3 并行） | (a) 对标 `examples/iotdevice` 工程结构 + `examples/todoorder`；(b) 测试策略：journey verify（fixture-driven，`run-journey` runner）；(c) 边界：4 step（reserveInventory → chargePayment → ship → notifyUser）每个都需 Compensate |
| 2 计划 | 4 step saga：`PlaceOrder`；状态机：每 step 完成后写 outbox event，consumer 是下一个 step；任一 step failed → 反向 Compensate（释放库存、退款、取消发货）|
| 3 worktree | `worktrees/068-saga-example` |
| 4 TDD | `journeys/J-orderfulfillment-saga-happy.yaml` + `J-orderfulfillment-saga-compensate.yaml`（charge fail → 库存释放）|
| 5 实施 | `examples/orderfulfillment/{main.go, run.go, cells/ordercell, contracts/saga/orderfulfillment/v1/saga.yaml, assemblies/orderfulfillment.yaml, journeys/}` |
| 6 PR | title: `feat(examples/orderfulfillment): L3 saga end-to-end example (W6 step 9/10)` |
| 7 review | 3 reviewer |
| 8 fix | Cx2：Compensate 顺序必须 reverse；Cx3：example README 缺 docker mode |
| 9 人工确认 | example 是否双模式（mem + PG）|

**Files**：
- `examples/orderfulfillment/main.go` +50（codegen）
- `examples/orderfulfillment/run.go` +250
- `examples/orderfulfillment/cells/ordercell/cell.go` +250
- `examples/orderfulfillment/cells/ordercell/slices/placeorder/*.go` +400
- `examples/orderfulfillment/contracts/saga/orderfulfillment/v1/saga.yaml` +120
- `journeys/J-orderfulfillment-saga-happy.yaml` +100
- `journeys/J-orderfulfillment-saga-compensate.yaml` +100
- `examples/orderfulfillment/README.md` +200

**ref**：`ref: examples/iotdevice (in-repo) + dtm-labs/dtm sample/saga`

---

### PR-10 · ops runbook + ADR + CLAUDE.md + capability-inventory 更新

**LOC 预估**：600–1000
**Ship level**：L1（仅文档）

| 阶段 | 内容 |
|---|---|
| 1 探索 | 跳过（L1） |
| 2 计划 | 跳过（L1） |
| 3 worktree | `worktrees/069-saga-docs` |
| 4 TDD | n/a |
| 5 实施 | (i) ADR `202606021000-adr-saga-l3-orchestration-engine.md`（实际落地名；计划期钦定的 `202605231400-...` 因时间戳撞 `202605231400-002-required-dep...` 且不合 `yyyyMMddHHmm-编号-名` 格式而改名）：决策 D1–D10 + 拒绝的备选 + 威胁矩阵；(ii) `docs/ops/saga-runbook.md`：故障排查（lease 卡死 / 补偿失败 / journal 满）；(iii) `CLAUDE.md` 加一节 "L3 Saga"；(iv) `docs/design/capability-inventory.md` 加 saga 一节；(v) `.claude/rules/gocell/saga.md` 终稿 |
| 6 PR | title: `docs(saga): ADR + ops runbook + CLAUDE.md + capability-inventory (W6 step 10/10)` |
| 7 review | 1 reviewer |
| 8 fix | Cx3：CLAUDE.md 加章节位置 |
| 9 人工确认 | ADR 威胁矩阵覆盖 |

**Files**：
- `docs/architecture/202606021000-adr-saga-l3-orchestration-engine.md` +400
- `docs/ops/saga-runbook.md` +200
- `CLAUDE.md` +30
- `docs/design/capability-inventory.md` +50
- `.claude/rules/gocell/saga.md` finalize +80

**ref**：`ref: docs/architecture/202605051600-adr-pg-outbox-fencing.md (in-repo) — adapted CAS pattern`

---

## 5. PR 间累积验证

每个 PR 单独跑通 `go -C worktrees/<NNN> test ./...` + `golangci-lint run ./...` + `hack/verify-archtest.sh`。**整链路验收**在 PR-09 ship 后跑：

```bash
# End-to-end happy + compensate
go test -tags=integration ./examples/orderfulfillment/... -run TestPlaceOrder_HappyPath
go test -tags=integration ./examples/orderfulfillment/... -run TestPlaceOrder_CompensateOnChargeFail

# Journey verify
go run ./cmd/gocell run-journey -f journeys/J-orderfulfillment-saga-happy.yaml
go run ./cmd/gocell run-journey -f journeys/J-orderfulfillment-saga-compensate.yaml

# Archtest（全量）
hack/verify-archtest.sh  # CI 跑 SHARD_COUNT=16；本地默认 1

# Governance
go run ./cmd/gocell validate  # 0 error
```

---

## 6. 风险与缓解

| # | 风险 | 概率 | 影响 | 缓解 |
|---|---|---|---|---|
| R1 | PR-01 state machine enum 设计不稳，PR-03+ 返工 | 中（30%） | 阻塞 PR-03–06 | PR-01 探索阶段 3 explorer，AskUserQuestion 确认 enum 完整性后再 ship |
| R2 | PR-04 PG migration 与现有 outbox migration 命名冲突 | 低（10%） | 单 PR delay | 用 040+（避开当前 030 段位） |
| R3 | PR-07 contractgen 派生 typed API 与现有 event/http codegen 风格不一致 | 中（25%） | DX 差 | PR-07 探索阶段强制对标 `contracts/event/` golden |
| R4 | PR-08 archtest `SAGA-STEP-COMPENSATE-PURE-01` 误伤合法用例 | 中（30%） | 假阳性 | 反向 self-test 覆盖 happy + 边缘；evaluation 走 typed `*types.Info.Selections` 而非字符串锚点 |
| R5 | 单 saga instance 单 leader 在生产 N 进程下产生瓶颈 | 低（10%） | 性能问题 | 第一版 leader 颗粒度到 instance（不是 definition），并行度自然 = active instance 数；如需更高并行再做 step-level sharding（不属本 PR 链）|
| R6 | AfterCommit hook 被滥用做 stateful 副作用 | 高（60%）| 反模式扩散 | PR-00 同 PR 落 archtest `AFTERCOMMIT-HOOK-PURE-TRANSIENT-01` Hard 拦截 |
| R7 | Compensate 失败的二级补偿 | 中（35%） | 长尾失败 | 已由 #1210 C6 落地 StatusCompensationFailed 独立终态闭环；dead-letter saga 表方案废弃。CompensateFunc 失败时写 KindStepCompensationFailed + 终态 StatusCompensationFailed，ops 通过 journal 查询定位，手工介入 runbook 待补（参见 docs/ops/alerting-rules.md §Saga）。 |

---

## 7. 进度跟踪表

```
PR-00  AfterCommit hook          [x]  #923
PR-01  kernel/saga skeleton      [x]  #932
PR-02  kernel/saga/journal       [x]  #952
PR-03  runtime/saga Coordinator  [x]  #977
PR-04  adapters/postgres/saga    [x]  #1004
PR-05  runtime/saga leader-elect [x]  #1108
PR-06  runtime/saga/executor     [x]  #1179
PR-07  contracts/saga + codegen  [x]  #1283
PR-08  governance + archtest     [x]  #1316
PR-09  example orderfulfillment  [x]  #1374
PR-10  ADR + runbook + docs      [x]  #1433
```

合并后回此处把 `[ ]` 改 `[x]`，注明 PR 号 + merge SHA。

---

## 8. 与 speckit 流程的映射

本计划等价于 speckit 顶层 `spec.md` + `plan.md` + `tasks.md` 三阶段输出的组合：

| speckit 阶段 | 本计划对应章节 |
|---|---|
| `/speckit.specify` | §0 目标 / §1 现状 / §2 设计骨架（WHAT/WHY） |
| `/speckit.clarify` | §2 关键决策 D1–D8（已凝固，不再 NEEDS CLARIFICATION）|
| `/speckit.plan` | §3 PR 序列 / §6 风险（HOW）|
| `/speckit.tasks` | §4 逐 PR 切片 |
| `/speckit.analyze` | §1 现状盘点 + §5 累积验证 |
| `/speckit.implement` | 每个 PR 各自走 ship 阶段 3–9 |

`/speckit.tasks` 颗粒度本应到 task 级，本计划已凝合到 "PR + 文件 + LOC + ship 阶段" 维度，是更密集的执行视图。如未来 PR-01 复杂度突破 2000 行需要二次拆分，再在对应 PR 章节插入子 PR-01a / PR-01b 即可。

---

## 9. 后续 wave 衔接（不在本计划范围）

- **W10 Projection / Replay**（基于 W5 Schema Registry + 本计划 saga）：从 `saga_events` table replay 出任意时点状态。独立 wave。
- **声明式 Saga DSL**（V11-3 v2 增量）：YAML `saga.yaml` 描述编排，contractgen emit 默认 Step.Run/Compensate 桩。触发条件 = 业务场景 ≥ 3 个相似 saga 后立项。
- **跨 cell child workflow / nested saga**：outbox 触发新 saga 的能力本计划已覆盖；正式的 parent-child 引用 + 联合 compensate 留 v1.2+ ADR。
- **Activity / Workflow worker pool 分层**：当 step concurrency 出现明确瓶颈再做。

---

**Owner**：platform · **Reviewer**：architect + reviewer · **生效日期**：2026-05-23
