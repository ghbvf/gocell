# ADR: Saga-Journal Projection Source — model-A event-sourced projection over `saga_events` (#1609)

- 状态：Accepted
- 日期：2026-06-05
- Issue：#1609（EPIC — Saga-Journal Projection Source）
- Builds on：
  - `docs/architecture/202605261620-adr-cqrs-projection-lifecycle-harness.md`（投影 harness Q1–Q5：Coordinator / CheckpointStore / ReplaySource / Cursor / Apply）
  - `docs/architecture/202606021000-adr-saga-l3-orchestration-engine.md`（saga journal 设计 D2/D4/D5 + 终态模型）
- Amends：`docs/architecture/202606021000-adr-saga-l3-orchestration-engine.md` §8 演进路径「Projection / Replay」行（同 PR 内原地重写）+ §7 威胁矩阵 row6 逐行重评
- 范围：EPIC #1609 的 **PR-00**（ADR）。PR-01..06 + PR-PG 见 §9 子 PR 映射；本 ADR 是该 EPIC 的设计权威源，已在 `.claude/rules/gocell/saga.md` §参考 登记本 ADR 的导航指针；三条新 enforcement invariant（`PROJECTION-EVENT-CARRIER-TYPED-01` / `SAGA-TAILER-CHECKPOINT-ADVANCER-CALLER-01` / `SAGA-GLOBALREADER-CONFORMANCE-ENROLL-01`）**尚未落地**，随各实现 PR（PR-01 / PR-04 / PR-02）增量回灌 saga.md 的 Archtest Invariants 表。

> **真值边界**：本 ADR 决策以本文为准。`202606021000` §8 的「Projection / Replay（从 `saga_events` replay）」行在同 PR 内重写为指向本 ADR 的指针（per `ai-robust.md` §"ADR amendment 落地必查"——原文与 amendment 不得两套真理源共存）。

---

## 1. 上下文与问题

### 1.1 缺口：saga 终态没有可消费的异步表示

`examples/orderfulfillment`（#1391）需要把订单 saga 的终态（succeeded / compensated / failed / expired / compensationFailed）投影到一个读模型，HTTP 查询读投影而非实时扫 journal。但：

- saga 引擎**永不向 outbox 发终态事件**：终态只经 `journal.JournalCore.MarkTerminal` 写进 `saga_events`（`runtime/saga/coordinator.go`），引擎仅对每步发 `step_completed`。
- compensation 步骤被 `SAGA-STEP-COMPENSATE-PURE-01`（Hard）禁止任何 emit ⟹ **被补偿/失败的 saga 在消息总线上没有任何事件**——唯一记录在 `saga_events` journal。
- 任何「订阅 outbox 事件」的投影（含「Apply 收到 step_completed 时回读 journal」的 hybrid）都会卡在 running，**永远观察不到 compensated/failed**。

故 #1391「消费 saga 终态 → 投影」在现状下没有可消费源。saga ADR D2/D4/D5 + §8 line132 已选定模型 = **replay-from-journal**（journal 即真值源），本 ADR 落地该模型。

### 1.2 与 #1100 投影 harness 的关系

已落地的 #1100 harness（`202605261620`）只支持「订阅 outbox 事件」live 路径（todoorder `orderprojection` 消费 cell 自发的 `event.order-created.v1` / `event.order-status-changed.v1`，**无 saga**）。其 rebuild 路径的 `PGProjectionReplaySource`/`Cursor` 用 `outbox_entries.seq` 作 position——但 outbox 是 **transient relay**（`CleanupPublished`/`CleanupDead` 删行），consume-lag 超 retention 时 `Cursor.Position` 在已删行上 `SELECT seq WHERE id=…` → 永久错误（#1100 ADR Amendment 2026-06-03 的 Retention-boundary，gated off by default，追踪 **gh #1504 P1**）。

`saga_events` 是 **append-only、永不删**的 journal——天然规避 #1504 的 transient-relay 缺陷。**注意范围**：本 ADR 为 **saga 事件**投影提供 durable 源，**不**声称关闭 #1504（#1504 是 outbox-event 投影的 durable 源问题，载体不同——saga_events 只装 saga 事件）。**back-pointer（#1504 设计已立项）**：outbox-event 投影的 durable 源现有独立 ADR `202606071600-1504-adr-projection-event-journal.md`（专用 append-only `projection_events` 表，复用本 ADR 已落地的 `cellvocab.ProjectionEvent` 载体 + #1627 身份修复地基；平行 reader，**不**泛化本 ADR 的 saga 专属 `journal.GlobalReader`）。

### 1.3 现状不是 bug——是工业合法形态，本 ADR 是「贴近生产形态」升级

当前 orderstatus 的「实时扫 journal + `deriveStatus` fold」即 **model-c**（直查存储），与 dtm / Temporal Visibility 同款合法工业形态，对 demo/ops 足够。本 ADR 把它升级为 **model-a**（物化投影 + replay），动机是给 GoCell 一个一等公民的 saga replay/历史投影能力参考实现，**不是修正确性**。

---

## 2. 对标（开源框架）

### 2.1 model-a vs model-b（终态如何到读模型）

| 类型 | 模型 | 补偿/失败终态是否产生总线事件 |
|------|------|------------------------------|
| Axon / EventStoreDB（事件溯源） | **model-a**：journal/event-store 即投影源，Tracking Event Processor 重放并 tail | 是——补偿是 store 里可重放的域事件 |
| MassTransit / NServiceBus / Eventuate（状态机 saga） | model-b：saga 显式 `PublishAsync` 终态事件（state store 非事件存储、不能重放） | 需显式 publish |
| Temporal | model-c：Visibility 索引 + 显式 activity | 引擎不自动发 |
| dtm | model-c：直查 trans_global | 无总线事件 |

GoCell `saga_events` 架构上就是事件存储 → 属 **model-a**（Axon 阵营），saga ADR §8 line132 已选此路。**否决 model-b**（要让引擎对所有 saga 发终态事件 = 反转 ADR line132/D2，且 compensation-pure 下补偿态本就无事件可发）。

### 2.2 tailing 处理器结构（5 框架 5:0 = 独立组件）

| 框架 | 投影处理器是否独立组件 | 自有 checkpoint/token | 是否共享写侧 coordinator/leader | coordination |
|---|---|---|---|---|
| Axon TEP/PSEP | 是（独立线程池） | 是（per-segment TokenStore claim + extendClaim 心跳） | 否 | per-segment claim |
| EventStoreDB persistent sub | 是 | 是（per-consumer-group position） | 否 | server competing consumers |
| **Marten Async Daemon**（PG 后端，最贴近） | 是（独立 IHostedService） | 是（HWM + per-projection progress） | 否（**PG advisory lock 独立 integer namespace**，与写侧行锁不重叠） | per-projection advisory lock leader-election |
| Kafka consumer group | 是 | 是（per group+partition offset） | 否 | broker group coordinator |
| Akka Projections | 是 | 是（per-ProjectionId offset） | 否（共享 cluster infra，claim 独立） | ShardedDaemonProcess singleton |

**没有任何框架把投影 tailing 耦合进写侧 coordinator**。协调粒度共识 = **per-projection / per-segment**（比写侧 per-instance 锁粗、比全局 leader 细，各投影互不阻塞）。Marten 是最贴近 GoCell 的范本：Postgres 后端 + 独立 daemon + per-projection advisory lock（与写侧锁不同命名空间）。

ref: axoniq/AxonFramework TrackingEventProcessor + TokenStore（per-segment claim）
ref: JasperFx/marten Async Projection Daemon + pg_advisory_lock per-projection leader election
ref: EventStoreDB/Kurrent persistent subscriptions（per-consumer-group position）
ref: akka/akka-projection ShardedDaemonProcess（per-ProjectionId offset）

---

## 3. 决策

| # | 决策 | enforcement 载体 | AI-robust 档位 |
|---|------|-----------------|----------------|
| D1 | **model-a**：`saga_events` 作 durable 投影源，投影 replay + tail journal 派生读模型；**不**让引擎发终态事件（否决 model-b） | 设计决策；下游由 D2–D4 载体守 | — |
| D2 | **载体 SHARED-INTERFACE**：harness `Apply`/`ReplaySource`/`Cursor` 的 event 载体泛化为最小 typed 只读接口 `projection.ProjectionEvent`（多态 `RestoreContext`/`Stream`，非 type-assert）；`outbox.Entry` 与 saga 事件各自实现；**同 PR 迁移 todoorder，无双路径、不伪造 `outbox.Entry`**（否决 UNIFY，否决 sealed-Entry 桥接 shim） | 接口 = type-system Hard；下游 archtest 禁投影公开 API 裸收 `outbox.Entry` | **type-system Hard（API shape，单轴非 funnel）**：公开 API 形参 = `ProjectionEvent` 接口，archtest 下游禁裸收 `outbox.Entry`。**不是 carrier-source-sealing funnel**——`ProjectionEvent` 全导出方法、任意包可实现，载体来源不封闭；forge 保护在 wiring 层（§5 forge 行 + §6 F4 评级），无 sealed-carrier 上游 Hard 可声明 |
| D3 | **saga journal 全局有序扫描**：新增窄接口 `journal.GlobalReader`（**不并入 `JournalCore`**，保 `SAGA-JOURNAL-HOLDER-SEAL-01`）+ `saga_events` 全局有序序列（PG `global_seq BIGINT GENERATED ALWAYS AS IDENTITY`；mem 全局计数器；`Event.GlobalSeq` additive） | 接口 + conformance enroll | conformance = Medium（Hard 路径 = codegen golden 枚举实现，**gh #1003** 同源） |
| D4 | **tailing = 独立 `Tailer`（Option B）**：自有 `Start/Stop/tickLoop`，持 `journal.GlobalReader`，**共享同一 `distlock.Locker` 实例但用 per-projection key**（如 `saga-journal-tailer:{projection}`）；**不** piggyback 进 saga `Coordinator.tickOnce`（saga distlock 是 per-instance 粒度、无 process-level global leader 可复用；5 框架对标一致） | 单 checkpoint-advancer funnel + leader-gate | advancer funnel 下游 **Hard**（caller-allowlist，go/types 解析）+ 上游 **Medium**（Go 可见性天花板；Hard 化追踪 **gh #1612**） |
| D5 | **exactly-once**，分两层独立机制——**(a) 同 owner 内 exactly-once**：apply 与 checkpoint advance 同事务提交，**复用**现有 `projection.CheckpointStore.SaveOffset`（`PROJECTION-CHECKPOINT-TX-BOUND-01` 守）。**(b) leader 交接 fencing**：现有 `CheckpointStore` 仅 `LoadOffset`/`SaveOffset`（无条件 upsert，无 CAS；`projection_checkpoints.owner` 列 v1.1 预留、写侧刻意不写），**无法**表达「旧 leader advance 必败」——故 lease-token CAS **是新契约、非复用**：扩 `CheckpointStore` 加谓词式 `AdvanceIfOwner(ctx, cellID, projectionID, token, offset)`（或激活 owner 列 + CAS 谓词），token 源 = D4 per-projection distlock lease 的 fencing token；语义对齐 `OUTBOX-LEASE-ID-CAS-01` 但**载体不同**。position = `global_seq` 稳定全序（**非 `created_at`**——同毫秒两实例无稳定序） | (a) `PROJECTION-CHECKPOINT-TX-BOUND-01`（复用）；(b) 新 CAS API + advancer funnel（D4），PR-04/PR-PG 定义 | (a) 复用既有 Hard；(b) **新契约**——落地前 leader-交接威胁行降 ⚠️（见 §5） |
| D6 | **增量 per-event apply**（非终态缓冲）：投影 fold 每条 saga 事件（含 Running 期 step 事件），语义等价当前 `deriveStatus` fold，但物化、异步、可重放 | 设计决策 | — |
| D7 | **retention 约束**：`saga_events` append-only 永不截断到任何投影 checkpoint 之下；归档/截断须 ≥ 最慢投影 checkpoint（投影引入后 journal retention 多一约束） | 设计约束（归档能力本身仍 out-of-scope，见 §5/§7） | — |

---

## 4. 接口层

### 4.1 载体（D2，SHARED-INTERFACE）

> **§Amendment 2026-06-06（PR-01 落地，原地修订，per `ai-robust.md` §"ADR amendment 落地必查"）**：
> 1. **接口/func-type 家 = `kernel/cellvocab`，非本节原写的 `kernel/projection`**。原因：`kernel/cell.ProjectionApply`
>    须引用载体，而 bootstrap 对 `cell.ProjectionApply` → `projection.Apply` 做具名类型转换（要求底层类型一致）；接口
>    放 `kernel/projection` 会逼 `kernel/cell` import `kernel/projection` 成环（`kernel/projection` 已 import
>    `kernel/cell`）。`cellvocab` 是纯叶子（cell + projection + outbox 均可 import 而无环），故 `ProjectionEvent` +
>    `ProjectionApply` + `ProjectionResetHook` 三类型定义在 `cellvocab`；`projection.ProjectionEvent` / `projection.Apply` /
>    `projection.OnReset` 与 `cell.ProjectionApply` / `cell.ProjectionResetHook` 全部是 `cellvocab.*` 的 **type alias**
>    （保 ADR 的 `projection.ProjectionEvent` 拼写）。
> 2. **镜像塌缩**：上述 alias 使 `cell.ProjectionApply` 与 `projection.Apply` 成为同一底层类型，故 PR-01 顺带删除
>    bootstrap 的两处具名转换（`projection.Apply(req.Apply)` / `projection.OnReset(req.OnReset)`）——这两个镜像类型
>    本只为绕环存在，cellvocab 从根上解环后即冗余。
> 3. **codegen 0 文本 diff**：`projection.tmpl` 按*名字*引用 `cell.ProjectionApply`，底层签名变化不改 18 个
>    `projection_gen.go` 与 todoorder `cell_gen.go` 的生成文本——PR-01 实际改动 ~450 行，**不拆 PR-01a/01b**（下方第 1
>    bullet 的拆分条件未触发）。
> 4. **`projection.SystemPrincipal` + saga 身份 archtest 推迟 PR-03**（非本节/§9 原写的 PR-01）：唯一消费者是 saga 载体的
>    `RestoreContext`（PR-03 才落地）；PR-01 无 saga 事件流经 Apply，impersonation 威胁尚不存在，现加 = 无消费者死代码 +
>    vacuous archtest（违 anti-vacuity）。本节下方「身份安全约束」末句「随 PR-01 落地」+ §9 PR-01 行 + §5 威胁矩阵 impersonation 行
>    的「(PR-01)」据此同改 PR-03。本节「备选 carrier-vs-config 复核」结论：维持 carrier-method 方案（PR-03 实现时仍可复核）。

```go
// package kernel/cellvocab  (§Amendment 2026-06-06: was kernel/projection — cycle-forced move)
//
// ProjectionEvent 是投影 Apply 的最小只读载体。outbox.Entry（live 总线投递 +
// outbox-store replay）与 saga journal 事件（saga-log replay）各自实现它。
type ProjectionEvent interface {
    EventID() string                                       // 流内唯一标识
    Payload() []byte                                       // 事件体原始 JSON
    OccurredAt() time.Time                                 // 域时间戳
    Stream() string                                        // 路由/主题等价（outbox: RoutingTopic；saga: 流 id）
    RestoreContext(ctx context.Context) context.Context    // outbox: 还原 obs+principal；saga: 安装 system/saga-projection 身份 + 清空 ambient principal（见下「身份安全约束」）
}
```

- `Apply`、`ReplaySource.Replay` 的 fn、`Cursor.Position` 均改收 `ProjectionEvent`（替换 `outbox.Entry`），`kernel/cell.ProjectionApply` 同步。**codegen 扇出（PR-01 主体量）**：`cell.ProjectionApply` 被 contractgen 为**每个 event contract** 派生的 `generated/contracts/event/*/v*/projection_gen.go::NewProjectionRequest(apply cell.ProjectionApply, …)` 引用——当前 **18 个** `projection_gen.go`（不止 todoorder 用到的 2 个）。PR-01 必须重跑 contractgen 重生成全部 18 个；若 18-file 重生成 + 载体迁移超 ~2000 行，PR-01 拆为 PR-01a（kernel/projection 载体接口 + 手写迁移）/ PR-01b（codegen 重生成 byte-diff）两子 PR。
- rebuild 路径 `rebuild.go` 的 `entry.Observability().RestoreToContext` / `Principal().RestoreToContext` / `RoutingTopic()==spec.Topic` 三处 outbox-only 调用，改走载体多态：`evt.RestoreContext(ctx)` + `evt.Stream()==spec.Topic`（**避免 explorer 建议的 type-assert 两层**——多态优于 type-switch）。
- **身份安全约束（saga）**：saga journal 事件**无 wire principal**，saga `RestoreContext` **绝不能原样返回 ctx**。否则身份穿透 Apply：① **rebuild 路径** `drainGap` 的 ctx 经 `context.WithoutCancel(triggerReq)` 继承**触发 rebuild 的 admin 身份**（且 outbox `Principal().RestoreToContext` 设计为不覆盖既有 principal，no-op restore 留住 admin）；② **live-tail 路径** Tailer 跑在后台 ctx、**无任何 principal**。两种情形下 Apply 若 emit/audit，前者归因到 admin（**审计 impersonation，P1**，见 `pkg/ctxkeys` principal 伪造风险），后者归因空。故 saga `RestoreContext` **主动清空 ctx 既有 principal 并安装显式 `projection.SystemPrincipal`**（system actor/subject、无 session、无 tenant），使 saga 投影 Apply 始终以稳定的 system/saga-projection 身份运行、与触发者彻底解耦。`projection.SystemPrincipal` helper + archtest（限定 saga carrier 为该身份唯一安装点）随 **PR-03** 落地。备选「身份作 Tailer/Processor 配置而非 carrier 方法」（更解耦）记录于拒绝表脚注，PR-03 实现时复核。
- **同 PR 迁移 todoorder** `orderprojection`（`HandleOrderCreated(ctx, ProjectionEvent)`，`entry.Payload()` 不变）+ 重生成受影响 `generated/contracts/event/*/projection_gen.go`；删 `outbox.Entry` 专用签名。无双路径。

### 4.2 全局有序扫描（D3）

```go
// package kernel/saga/journal
//
// GlobalReader 是投影 tailing 的全局扫描接口，区别于 Reader（per-instance Load）。
// 不并入 JournalCore：Tailer 持 GlobalReader 不违反 SAGA-JOURNAL-HOLDER-SEAL-01。
type GlobalReader interface {
    LoadSince(ctx context.Context, afterGlobalSeq int64, limit int) ([]GlobalEvent, error)
    HeadSeq(ctx context.Context) (int64, error)
}

type GlobalEvent struct {
    GlobalSeq  int64          // 跨所有实例单调（PG IDENTITY / mem 计数器）
    InstanceID idutil.SafeID
    Event      Event          // 既有 Event（+ GlobalSeq 字段 additive）
}
```

PG：`saga_events` 加 `global_seq BIGINT GENERATED ALWAYS AS IDENTITY`（migration 只增不改 + `CREATE INDEX CONCURRENTLY`）。mem：append 时分配全局计数器。两实现入 `sagajournaltest` conformance（全局序单调性）。

### 4.3 Tailer（D4）+ SagaJournalSource（接 harness）

- `runtime/saga/tailer.Tailer`：`Start(ctx)`/`Stop()`，持 `journal.GlobalReader` + `distlock.Locker`（per-projection key）+ `projection.CheckpointStore`；per-tick：acquire 锁 → `LoadSince(checkpoint, batch)` → 逐事件 apply（同事务 advance checkpoint，lease-token CAS = D5(b) **新契约**，token = distlock lease）→ release/续期。
- `SagaJournalSource` 实现 `projection.ReplaySource`（`Replay` 经 `LoadSince` 喂 `ProjectionEvent`）+ `projection.Cursor`（`Position` = 事件自带 `GlobalSeq`，**无 #1504 式删行查找**），入 `RunReplaySourceConformance` / `PROJECTION-CURSOR-CONFORMANCE-ENROLL-01`。
- **Tailer 运行面（observability，PR-04 验收项，非可选）**：Tailer 是新长驻组件、**不经 ConsumerBase / Coordinator 路径**，运维信号**不自动继承**（既有投影 readiness/lag 在 `kernel/projection/probe.go`、saga 告警/runbook 在 `docs/ops/alerting-rules.md` / `saga-runbook.md`，Tailer 都触达不到）。故必须自带数据面之外的运行面：
  - **readiness probe**：`<cell>_saga_tailer_<proj>_ready`（由 `healthz.SagaTailerReadyProbeName(cellID, projectionID)` 构造，形如 `auditcore_saga_tailer_sagastatus_ready`），形态对齐 `kernel/projection/probe.go` 投影 readiness/lag probe，经 healthz typed funnel 注册（`PROBENAME-SEALED-FUNNEL-01`）。
  - **metrics**：lock-acquire-failure / drain-error / last-success-timestamp / pending-lag（`HeadSeq − checkpoint`）/ checkpoint-advance-failure，label 值集冻结（沿用 `SAGA-METRIC-LABEL-VALUES-FROZEN-01` 范式）。
  - **alerting + runbook**：tailer 停摆（checkpoint 长期不动 / lock 反复抢占失败 / lag 持续增长）→ `docs/ops/alerting-rules.md`（毗邻既有 saga coordinator 告警）+ `docs/ops/saga-runbook.md` 新增「投影 tailer 停滞」诊断场景（`HeadSeq` vs checkpoint SQL + lease 持有方排查）。
  - **失败语义**：缺 probe/metric/alert 会让「checkpoint 静默停摆」退化成无一等诊断入口的故障——这正是把本 checklist 列为 PR-04 验收项的原因。

---

## 5. 威胁矩阵

| 威胁 | 机制 | 覆盖 | 遗留 |
|------|------|------|------|
| leader 交接 mid-drain，checkpoint 推进不安全 | per-projection distlock + apply/advance 同事务（窄 `OwnerCheckpointStore`）+ 单调-offset CAS（旧 leader 落后 advance 必败，语义同 `OUTBOX-LEASE-ID-CAS-01`） | ⚠️ | **PR-04 已交付 mem CAS**：新窄接口 `projection.OwnerCheckpointStore.AdvanceIfOwner(ownerToken, offset)`（semantics B：`token==owner` 或 `offset>committed` 才接受，否则 `ErrStaleOwner`）+ `MemOwnerCheckpointStore` + `RunOwnerCheckpointConformance`。distlock 不暴露 fence token，故 Tailer 每次抢锁自生成 owner token（`idutil.NewUUID`）；CAS 封住 checkpoint **回退**（落后旧 leader 被拒）。**仍 ⚠️**：deposed-yet-ahead 旧 leader 在 lock-expiry 窗口仍可 re-claim → **有界重复 apply**（投影 Apply 幂等兜底，非 exactly-once）。**完整 monotonic fencing（PG `projection_checkpoints.owner` 列 fence token）延后 PR-PG**（gated on 真生产消费者；在此之前 `PROJECTION-CHECKPOINT-OWNER-COLUMN-V1-RESERVED-01` 守 owner 列不写） |
| 乱序 / 非 exactly-once | `global_seq` 稳定全序（非 `created_at`）；投影按 `global_seq` 序处理；idempotent upsert | ✅ | — |
| Running saga 的部分 step 事件先于终态到达 | 增量 per-event apply（D6），投影 fold 容忍中间态 | ✅ | — |
| rebuild 触发者（admin）/ 后台 Tailer 身份穿透进投影 Apply（审计 impersonation）——**saga 投影**与 **outbox 投影**两条路径同源（§Amendment 2026-06-06 拓宽，原仅列 saga） | saga `RestoreContext` 清空 ambient principal + 安装显式 `projection.SystemPrincipal`（不继承触发请求身份，见 §4.1 身份安全约束）；archtest `PROJECTION-SYSTEM-PRINCIPAL-INSTALL-CALLER-01` 限定 saga carrier 为 `projection.InstallSystemPrincipal` 唯一安装点（PR-03 已落，#1627）。**outbox 投影** rebuild 路径：`rebuild.go` 在 detach 边界调 `clearAmbientPrincipal(context.WithoutCancel(ctx))` 清空 ambient principal，确保 outbox carrier `RestoreContext`（no-overwrite）在 clean ctx 上工作而非继承 admin 身份（PR-03 已落） | ✅（PR-03 已落，#1627） | — |
| checkpoint advance 多写入路径 | 单 advancer funnel：`AdvanceIfOwner` 唯一生产 callsite = `runtime/saga/tailer.(*Tailer).commitEvent`，由 `SAGA-TAILER-CHECKPOINT-ADVANCER-CALLER-01` caller-allowlist 锁（下游 Hard，go/types） | ✅（PR-04 已交付） | 上游 Medium（Go 可见性天花板——`AdvanceIfOwner` 是导出接口方法，无法编译期强制单一 caller；Hard 化追踪 **gh #1612**，同 #851/#893/#1282 族） |
| 长驻 Tailer 故障无诊断入口（checkpoint 静默停摆） | Tailer 自带 readiness probe（`<cell>_saga_tailer_<proj>_ready`）+ lock-acquire/drain/checkpoint-advance counters + pending/last-success gauges + alerting + runbook（不继承 ConsumerBase/Coordinator 信号，见 §4.3 运行面 checklist） | ✅（PR-04 已交付） | — |
| `saga_events` 无界增长 + 归档截断到投影 checkpoint 之下丢事件 | D7：归档须 ≥ 最慢投影 checkpoint | ⚠️ | 归档能力本身仍未做（saga ADR §7 row6 的归档部分；本 ADR 只交付 replay 设计，不交付归档/截断） |
| 越界/伪造 saga 事件进投影 | 载体 `ProjectionEvent` 只读；事件唯一源 = sealed append-only `saga_events`（经 `GlobalReader`），Tailer 是 Apply 的唯一 feeder 且只从 journal 读 → 业务包无法注入伪造事件（**保护在 wiring 层、非接口构造层**——`ProjectionEvent` 接口全导出可实现，见 §6 F4 评级） | ✅ | — |

---

## 6. AI-robust 评级

| ID（占位，落地 PR 定型） | 摘要 | 评级（双向锁分轴） |
|---|---|---|
| `PROJECTION-EVENT-CARRIER-TYPED-01` | 投影公开 API（`Apply`/`ReplaySource.Replay` fn/`Cursor.Position`）只收 `ProjectionEvent`，禁裸 `outbox.Entry` | **type-system Hard（API shape，单轴——非 funnel 双向锁）**：公开 API 形参类型 = `ProjectionEvent` 接口，archtest 下游禁裸收 `outbox.Entry`（go/types）。**不是 carrier-source-sealing funnel**——`ProjectionEvent` 全导出方法、任意包可实现，载体来源**不封闭**；它只 gate「公开 API 不再收具体 `outbox.Entry`」，**不** gate「只有 sanctioned outbox/saga carrier 可进入」。投影载体是只读 carrier、无 `outbox.Entry` 式跨边界身份信任（forge 保护在 Tailer 唯一从 sealed journal 喂 Apply 的 wiring 层，§5 forge 行），故**不声明** sealed-carrier 上游 Hard。如未来需封闭载体来源，须在 `ProjectionEvent` 加 unexported marker method（sealed interface）后再升上游 Hard |
| `SAGA-TAILER-CHECKPOINT-ADVANCER-CALLER-01`（PR-04 已落） | saga 投影 checkpoint advance（`OwnerCheckpointStore.AdvanceIfOwner`）只在单一 sanctioned 函数 `runtime/saga/tailer.(*Tailer).commitEvent` 调用（FuncLit 下降归属）。落地于 `tools/archtest/saga_invariants_test.go`（`SAGA-INVARIANTS-FILE-CONSOLIDATED-01` 要求 `SAGA-*` 同文件） | 下游 **Hard**（caller-allowlist，go/types）+ 上游 **Medium**（Go 天花板；Hard 化追踪 **gh #1612**，PR-04 archtest godoc 点名该 issue） |
| `SAGA-OWNER-CHECKPOINT-CONFORMANCE-ENROLL-01`（PR-04 已落） | 每个 `projection.OwnerCheckpointStore` 实现入 `projectiontest.RunOwnerCheckpointConformance`（cold claim / same-owner re-advance / new-leader claim-ahead / stale-owner reject）。今日唯一实现 = `MemOwnerCheckpointStore`（已 enroll，非 vacuous）；PG impl 落地 PR-PG 时自动纳入 | **Medium**（Hard 路径 = codegen golden 枚举实现，gh #1003 同源） |
| `SAGA-GLOBALREADER-CONFORMANCE-ENROLL-01` | 每个 `journal.GlobalReader` 实现 + `SagaJournalSource` 入 conformance（全局序单调 + replay/cursor 一致） | **Medium**（Hard 路径 = codegen golden 枚举实现，gh #1003 同源） |
| `PROJECTION-SYSTEM-PRINCIPAL-INSTALL-CALLER-01` | `projection.InstallSystemPrincipal` 生产引用点锁定在 `kernel/saga/sagaprojection/source.go`（saga carrier `RestoreContext` 唯一安装点）；`clearAmbientPrincipal`（unexported，同文件定义）在 `kernel/projection/rebuild.go` detach 边界调用——限定在 `kernel/projection` 包内，Go 可见性天花板即上游 Hard。落地于 `tools/archtest/projection_system_principal_install_caller_test.go`（PR-03，#1627） | 下游 **Hard**（go/types caller-allowlist，1 成员，alias/dot-import 不可绕）+ 上游 **Medium**（`InstallSystemPrincipal` 公开方法，Go 包可见性无法防止包外意外调用，同 #851/#893/#1282 族；Hard 化路径 = unexported marker / sealed ctx-key，追踪 **gh #1702**） |

> 每条 invariant 的完整盲区清单 + 反向自检活在落地 PR 的 archtest package godoc（单源，`ai-robust.md`）；本表只导航。所有新约束 ≥ Medium（无 Soft 立项）；Medium 上游天花板均开 gh 跟踪。

---

## 7. 拒绝的备选

| 备选 | 拒绝理由 |
|------|---------|
| **model-b**（引擎 `MarkTerminal` 补发 `SagaTerminated` outbox 事件） | 反转 saga ADR line132/D2（journal = 真值源、replay-from-journal）；引擎对所有 saga 生效、blast radius 大；compensation-pure 下补偿态本无事件可发，得为终态破例——两套真理源 |
| **UNIFY**（Coordinator source-agnostic、砍总线订阅 live 路径改 tail-the-source） | 打爆 todoorder（NoopWriter 无东西可 poll）+ 作废 `PROJECTION-SERIAL-DELIVERY` / `PROJECTION-CONSUMERBASE-WIRING` 两 archtest + 混淆 push-delivery 与 pull-replay；blast radius 极大。SHARED-INTERFACE 等价收益、6-8 文件 |
| **Option A**（投影 drain piggyback 进 saga `Coordinator.tickOnce`） | saga distlock 是 per-instance 粒度（无 process-level global leader 可复用），投影仍需另开 per-projection 锁；混两种锁粒度、撑大 `SAGA-DRIVE-BEHIND-LEADER-GATE` / `SAGA-STEP-RUN-OUTSIDE-TX` 守护的复杂函数、往 coordinator-only `JournalCore` 加方法；5 框架对标一致选独立组件 |
| **载体方案 A**（桥接 `journal.Event → outbox.Entry`） | 为迁就旧 harness 形状伪造 sealed `outbox.Entry`，在 `OUTBOX-ENTRY-SEALED-CONSTRUCTION-01` Hard seal 打洞——compat shim，违 AI-HARD |
| **沿用 outbox-backed reader 做 saga 投影** | outbox transient-relay 删行 → #1504 缺陷；saga 投影本就该读 append-only 的 `saga_events` |

---

## 8. 后果

- harness 载体从 `outbox.Entry` 泛化为 `ProjectionEvent`（PR-01 一次性迁移 todoorder，无兼容期——pre-v1.0 直接演化）。
- `saga_events` schema 加 `global_seq`（PG migration）；`Event` 加 `GlobalSeq`（additive，mem 路径迁移前为零值）。
- 多一个长驻组件（Tailer），与 saga Coordinator 同 distlock 服务、不同 key；运维上 per-projection 独立 leader/checkpoint。
- journal retention 多一约束（D7）：归档须 ≥ 最慢投影 checkpoint。
- `examples/orderfulfillment` orderstatus 改读投影、删 `deriveStatus` journal-scan（PR-06，单读路径）。

---

## 9. 子 PR 映射（EPIC #1609，每 PR ≤ ~2000 行，特殊可超）

| PR | 范围 | 本 ADR 决策 |
|----|------|------------|
| PR-00（本 PR） | 本 ADR + saga ADR §8/§7 同 PR 重写 | D1–D7 |
| PR-01（已落，#1625） | 载体泛化 `ProjectionEvent`（家=`cellvocab`，§Amendment 2026-06-06）+ 迁移 todoorder + PG impl + `PROJECTION-EVENT-CARRIER-TYPED-01` + 塌缩 `cell.ProjectionApply`↔`projection.Apply` 镜像。**`projection.SystemPrincipal` + saga 身份 archtest 移 PR-03**（真实 blocker：消费者 saga `RestoreContext` 在 PR-03，PR-01 无 impersonation 面） | D2 |
| PR-02 | `journal.GlobalReader` + `global_seq`（mem + PG migration）+ conformance | D3 |
| PR-03（已落，#1627） | `SagaJournalSource`（ReplaySource + Cursor）+ conformance enroll **+ `projection.SystemPrincipal`（`InstallSystemPrincipal` / `clearAmbientPrincipal`）+ `PROJECTION-SYSTEM-PRINCIPAL-INSTALL-CALLER-01` archtest + `rebuild.go` detach 边界 `clearAmbientPrincipal` 修复 impersonation bug**（D2 身份安全约束全闭环）。**pre-existing `CTXKEYS-PRINCIPAL-WRITE-CALLER-01` violation fix**：`configcoretest/fakes.go` 补入 `WithTenantID` allowlist（#1632 miss）。依赖 PR-01（#1625）+ PR-02 已落 | D2/D3/D5 |
| PR-04 | `Tailer`（Option B）+ per-projection distlock + 单 advancer funnel + **checkpoint CAS API**（`AdvanceIfOwner` / owner-列 CAS，token = distlock lease；D5(b)）+ **Tailer 运行面**（probe / metrics / alert / runbook，见 §4.3 observability checklist） | D4/D5 |
| PR-05 | wiring：扩展 subscribe 角色加 projection-source 选择子（单一路径）+ cellgen 派生 | D2/D4 |
| PR-06 | #1391 落地：orderfulfillment 读投影 + 删 `deriveStatus` + dev guide | D6 |
| PR-PG（deferred） | PG saga-journal source 生产化 + 激活 `projection_checkpoints.owner` 列 CAS（D5(b) PG 侧 fencing），gated on 真实生产消费者 | D3/D5 |

---

## 10. ref

ref: axoniq/AxonFramework org.axonframework.eventhandling.TrackingEventProcessor + tokenstore.TokenStore — per-segment claim + extendClaim 心跳
ref: JasperFx/marten src/Marten/Events/Daemon — Async Projection Daemon + pg_advisory_lock per-projection leader election（PG 后端最贴近范本）
ref: EventStoreDB/Kurrent persistent-subscriptions — per-consumer-group server-side position
ref: akka/akka-projection ShardedDaemonProcess — per-ProjectionId offset，独立 singleton claim
ref: docs/architecture/202605261620-adr-cqrs-projection-lifecycle-harness.md §Amendment 2026-06-03（#1504 retention-boundary）
ref: docs/architecture/202606021000-adr-saga-l3-orchestration-engine.md D2/D4/D5 + §8 line132（journal = 真值源 / replay-from-journal）

---

## §Amendment to saga ADR `202606021000`（同 PR 内重写）

per `ai-robust.md` §"ADR amendment 落地必查"，本 PR 在 `202606021000-adr-saga-l3-orchestration-engine.md` 内**原地重写**：

1. **§8 演进路径**「Projection / Replay（从 `saga_events` replay 任意时点状态）」行：`W10 独立 wave` → 指向本 ADR（#1609）。
2. **§7 威胁矩阵 row6**（journal 无界增长 ⚠️「归档/replay 截断 = W10 独立 wave，未做」）逐行重评：**replay** 部分的**设计由本 ADR（model-a 投影源）立项（accepted）**，**能力本身待 EPIC #1609 PR-02..06 落地**（PR-00 仅 ADR，未实现）；**归档/截断** 部分仍未做（保 ⚠️，且本 ADR D7 增「归档须 ≥ 最慢投影 checkpoint」约束）。故该行**保持 ⚠️**（replay 设计 accepted ≠ 能力 delivered；归档未做）——`saga-runbook.md` §journal 增长场景同步改 #1609 引用 + D7 截断下界。rows 1–5、7–8 不受本 ADR 影响（本 ADR 只加 `saga_events` 读路径 + 一个全局有序列，写侧 Coordinator/heartbeat/lease fencing 不变），标 unchanged。
