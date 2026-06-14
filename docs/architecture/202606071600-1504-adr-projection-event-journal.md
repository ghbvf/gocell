# ADR: Retained Projection Event Journal — model-a durable source for outbox-event projections (#1504)

- 状态：Accepted（设计先行 / PR-00）
- 日期：2026-06-07
- Issue：#1504（EPIC — Projection event retention：faithful rebuild-from-0 needs a retained journal）
- Builds on：
  - `docs/architecture/202605261620-adr-cqrs-projection-lifecycle-harness.md`（#1100 投影 harness Q1–Q5：Coordinator / CheckpointStore / ReplaySource / Cursor / Apply；本 ADR 即其 §Amendment 2026-06-03 retention-boundary 标注的 C1 设计项）
  - `docs/architecture/202606051200-1609-adr-saga-journal-projection-source.md`（saga 事件的 model-a 投影源：`GlobalReader` + `global_seq` + `cellvocab.ProjectionEvent` 载体——本 ADR 复用其已落地地基，载体不同）
- Amends：`docs/architecture/202605261620-adr-cqrs-projection-lifecycle-harness.md` §Amendment 2026-06-03 retention-boundary 段 + §6 威胁矩阵 Row 1（同 PR 原地重写，见 §Amendment 段）；`docs/architecture/202606051200-1609-adr-saga-journal-projection-source.md` §1.2 加 back-pointer
- 范围：EPIC #1504 的 **PR-00**（ADR）。PR-01..05 + PR-PG 见 §9 子 PR 映射；本 ADR 是该 EPIC 的设计权威源。三条新 enforcement invariant 中 `PROJECTION-EVENT-JOURNAL-APPEND-CALLER-01`（I2）+ `…-TOPIC-ALLOWLIST-DERIVED-01`（I5）**已随 PR-02（#1769）落地**（权威盲区/反向自检活在各 archtest godoc）；`PROJECTION-EVENT-JOURNAL-NO-DELETE-01`（I4 archtest 纵深）随 PR-05 落地（I4 主守卫 DB-REVOKE 已在 PR-01）。`eventbus.md` projection 段已加导航指针。

> **真值边界**：本 ADR 决策以本文为准。`202605261620` §Amendment 2026-06-03 retention-boundary 段与 §6 Row 1 在同 PR 内原地重写为指向本 ADR 的指针（per `ai-robust.md` §"ADR amendment 落地必查"——原文与 amendment 不得两套真理源共存）。

---

## 1. 上下文与问题

### 1.1 缺口：投影 harness 坐在 transient relay 上

已落地的 #1100 投影 harness（`202605261620`）的生产 `ReplaySource`/`Cursor` 从 **outbox journal** 取流位置：

- `adapters/postgres/projection_replay_source.go` 的 `Replay` 扫 `SELECT … FROM outbox_entries WHERE seq > $1 ORDER BY seq`；`Cursor.Position` 做 `SELECT seq FROM outbox_entries WHERE id = $1`（`cursorPositionSQL`），位置源 = migration 049 的 `seq BIGINT GENERATED ALWAYS AS IDENTITY`。
- 但 **outbox 是 transient relay**：`runtime/outbox/relay.go` 的 cleanup loop 调 `Store.CleanupPublished`（默认 retention 72h）/ `CleanupDead`（30d），物理 `DELETE FROM outbox_entries`（`adapters/postgres/outbox_store.go`）。

后果（`202605261620` §Amendment 2026-06-03 已锐化为两条，非仅 rebuild）：

1. **rebuild-from-0 不健全**：full rebuild 只能重放未被 cleanup 的历史；consume-lag 超 retention 的旧事件已被删，无法重建。
2. **live 路径也不安全**：`Cursor.Position` 在已删行上 `SELECT seq WHERE id=…` → `pgx.ErrNoRows` → 包成 **permanent error** → live 事件被 dead-letter（丢弃）、rebuild 中止。

现状是 **fail-closed**：`cmd/corebundle` 默认不 wire PG reader，仅 `GOCELL_PROJECTION_PG_JOURNAL_PREVIEW=true`（dev/preview，带 NOT-production-safe WARN）才接，否则 phase6 `checkProjectionDeps` fail-fast。所以今天没有不安全运行，但**生产投影完全跑不起来**。本 ADR 落地去掉该 gate 的 durable 解法。

### 1.2 与 #1609 的关系（设计权威边界）

同问题形状、**不同载体**。#1609（saga journal projection source）给 **saga 事件**提供 model-a durable 投影源（`saga_events` 表本就 append-only 永不删）；#1609 §1.2 **明确声明不关闭 #1504**——#1504 是 **outbox 派生事件**投影的 durable 源问题，载体不同（`saga_events` 只装 saga 事件）。

本 ADR **复用 #1609 已落地（PR-01/02/03）的地基**：

- **载体 `cellvocab.ProjectionEvent`**（5 方法 `EventID/Payload/OccurredAt/Stream/RestoreContext`，#1609 PR-01 已把 harness `Apply`/`ReplaySource.Replay`/`Cursor.Position` 泛化为收该接口）——本 ADR 的新 source 直接产出它，**harness 公开 API 零改动**。
- **model-a 结构模板**：`SagaJournalSource`（`kernel/saga/sagaprojection/source.go`）"一个类型同时实现 `ReplaySource`+`Cursor`、`Position` 读事件自带的 `global_seq`（无删行查找）"——本 ADR 的 outbox-event source 照此形态。
- **身份/impersonation 修复**（`clearAmbientPrincipal` / `PROJECTION-SYSTEM-PRINCIPAL-INSTALL-CALLER-01`，#1627 已落）——outbox carrier 的 `RestoreContext` 在 rebuild detach 边界已被 clean ctx 保护，无需重做。

本 ADR **自定义、不复用** 的：

- **平行的 journal reader**，**不**泛化 saga 的 `journal.GlobalReader`：其 `GlobalEvent.InstanceID` 是 saga 专属字段，两个 journal 携带的 envelope 本质不同（saga：instance+version；本表：完整 outbox obs/principal/payload 信封）。rule-of-three 前不投机抽象（同 #1609 "新窄接口、不并入 JournalCore" 的纪律）。
- **leader-fencing CAS**：`AdvanceIfOwner` 在 #1609 PR-04/PR-PG **尚未落地**（grep 确认零生产引用），无法复用；详见 D6(b)。

**关键运行时差异**：#1504 的 outbox 事件本就在消息总线 live 推送（经 `ConsumerBase` 串行单 pod，`PROJECTION-SERIAL-DELIVERY-ENFORCEMENT-01` 守），durable journal 只做 rebuild/`Position` 源——**本 ADR 不引入新的长驻 Tailer**。#1609 需要独立 Tailer 是因为 compensation-pure 下被补偿/失败的 saga 在总线上无任何事件可订阅，只能 tail journal；#1504 无此约束。

### 1.3 现状不是 bug——是 fail-closed 的已知 v1 限制

`202605261620` §Amendment 2026-06-03 已把它从"doc note"升级为"HARD GATE"：生产配置 fail-closed，无静默不安全运行。本 ADR 是"去掉该限制、贴近生产形态"的升级，而非修正一个活跃 bug。

---

## 2. 对标（开源框架）

### 2.1 投影 token / high-water mark 坐在哪里

| 框架 | 投影位置源 | 是否 transient |
|------|-----------|---------------|
| **Axon Framework** | `JdbcEventStore` + `TrackingToken`：retained 事件存储即真值源，TrackingEventProcessor 重放+tail | 否（retained event store） |
| **Marten Async Daemon**（PG，最贴近） | `mt_events` retained 存储 + `mt_event_progression` high-water mark；per-projection advisory lock | 否 |
| **EventStoreDB / Kurrent** | `$all` 流 + per-subscription position，retained log | 否 |
| **Debezium / transactional-outbox-as-CDC** | relay/connector 把已提交行复制到**下游 retained log**；投影读下游 log | 下游是 retained（outbox 本身 transient） |

**共识**：durable 投影 token 坐在 **retained 事件存储**，不坐在 publish-transit 的 outbox 上（`202605261620` §Amendment 2026-06-03 已援引 Axon/Marten 佐证）。GoCell `saga_events` 已是 model-a（#1609 §2.1）；本 ADR 给 outbox 派生事件补上同款 retained 源，使 GoCell 两条投影源路径概念统一。

### 2.2 写路径：event-store-primary vs CDC-copy

- **event-store-primary**（Axon/Marten/ESDB）：事件在**业务事务里**写进存储，即 source of truth；投影派生。→ 对应本 ADR 的 **emit 期同事务双写**（D4）。
- **CDC-copy**（Debezium）：relay/connector 在 publish 后把行复制到下游 retained log。→ 对应被否决的 relay-copy-on-publish（脱离业务提交、live-path 重引入排序竞态，§7）。

ref: axoniq/AxonFramework org.axonframework.eventsourcing.eventstore.jdbc.JdbcEventStore + TrackingToken
ref: JasperFx/marten src/Marten/Events/Daemon — Async Projection Daemon over mt_events（PG 后端最贴近范本）
ref: EventStoreDB/Kurrent `$all` stream + persistent-subscription position
ref: debezium/debezium outbox-event-router（transactional-outbox-as-CDC，下游 retained log）

---

## 3. 决策

| # | 决策 | enforcement 载体 | AI-robust 档位 |
|---|------|-----------------|----------------|
| **D1** | **model-a，专用 retained journal**：新增 append-only `projection_events` 表作 durable 投影源，**不**复用 transient `outbox_entries`。rebuild-from-0 与 live `Cursor.Position` 都解析 retained 行。否决"继续读 relay"（即 #1504 bug）+ 否决"让 relay 永不 cleanup"（破坏 relay 的 transient 契约、混淆两类语义） | 设计决策；下游由 D2–D7 载体守 | — |
| **D2** | **载体复用、harness API 零改动**：新 source 实现 `projection.ReplaySource`+`projection.Cursor`，喂既有 `cellvocab.ProjectionEvent`（5 方法）。无 `Apply`/`Subscribe`/`Coordinator` 签名变化（#1609 PR-01 已泛化载体） | 既有 `PROJECTION-EVENT-CARRIER-TYPED-01`（type-system Hard，已 green） | 复用——无新评级 |
| **D3** | **表 + 位置 + reader 形态**：`projection_events` 带 `global_seq BIGINT GENERATED ALWAYS AS IDENTITY`（位置，复刻 migration 049 / `saga_events.global_seq`）+ `id` 唯一索引 + 重建 `ProjectionEvent` 所需列（payload/topic/occurred_at/observability/principal/created_at，对齐 `kernel/outbox/reconstruct.go::EntryScan`）。`Position` 读行自带 `global_seq`，**无 `SELECT seq WHERE id=…` 删行查找**（这是相对现状的结构性修复）。**两个直接 source impl（mem + PG）各自实现 `ReplaySource`+`Cursor`，不引入 saga `GlobalReader` 式中间接口**（无共享逻辑可抽，rule-of-three 前不投机抽象） | migration（only-add）+ `schema_guard` 表注册 + 既有 `RunReplaySourceConformance`/`RunCursorConformance` 入列（I1） | conformance = Medium（I1）；表 shape = `schema_guard` Medium |
| **D4** | **写路径 = emit 期同事务双写（event-store-primary）**，落地为 `adapters/postgres` 内一个 journaling Writer **装饰器**包住基础 `OutboxWriter`：`WriterEmitter.Emit → decorator.Write` 在 producer 既有业务 `RunInTx`（已 ambient-tx，`adapters/postgres/outbox_writer.go`）里先写 `outbox_entries`、**同事务**再走**未导出** `appendProjectionEvent(ctx, tx, entry)` 写 `projection_events`。**topic-filtered**：仅对 cellgen 从 `slice.yaml contractUsages[projection=...]` 静态派生的 projection-source topic 集双写，set 经 composition root 注入装饰器 → **增长有界 by construction**（只 journal 真会被 replay 的事件）。idempotency：append `ON CONFLICT (id) DO NOTHING`（防业务 tx 应用层重试重复 append） | append funnel = caller-allowlist（I2）+ topic-allowlist 派生（I5） | append funnel **Hard/Hard**（I2，见 §6） |
| **D5** | **reader/cursor 复用 harness 槽位**：经既有 `bootstrap.WithProjectionReplaySource` / `WithProjectionCursor` / `WithProjectionCheckpointStore` / `WithProjectionTxRunner` 接入（同实例填 Replay+Cursor 两槽，对称 #1609）。无新 bootstrap option、无新 drain phase；corebundle 仅替换其构造的 source | 既有 `checkProjectionDeps` fail-fast + `PROJECTION-CONSUMERBASE-WIRING-01` | 复用——无新评级 |
| **D6** | **exactly-once，分两层**——(a) **同 owner 内**：apply 与 checkpoint advance 同事务，**复用** `projection.CheckpointStore.SaveOffset`（`PROJECTION-CHECKPOINT-TX-BOUND-01` 守），不变。(b) **leader 交接 fencing**：**#1504 须自定义、不能复用 #1609 的 `AdvanceIfOwner`**（grep 确认其零生产引用、#1609 PR-04/PR-PG 未落）。但 #1504 的 live 投递本就经 `ConsumerBase` 串行单 pod（无新长驻 Tailer，见 §1.2），故 D6(b) 仅作用于 rebuild-time leader 安全；**v1 继承 #1100 Q5 单 pod 边界**（`projection_checkpoints.owner` 列保留不写），多 pod fencing CAS 推迟到真有多 pod 消费者（PR-PG，届时定义 #1504 自己的 `AdvanceIfOwner`） | (a) 既有 `PROJECTION-CHECKPOINT-TX-BOUND-01`；(b) v1 文档化单 pod 边界，CAS 留 PR-PG | (a) 复用 Hard；(b) leader-交接威胁行 ⚠️（v1 单 pod 边界，§5） |
| **D7** | **append-only / no-DELETE 保证**：`projection_events` 永不被任何 relay 式 cleanup 删除。三层守（递增）：(i) store 接口**不声明** Cleanup/Delete 方法（仅约束 sanctioned store **类型**——**不**覆盖 raw/dynamic SQL、migration、其它 adapter 的 `pgx.Exec`、包内旁路）；(ii) archtest 禁裸 `DELETE`/`TRUNCATE` 字面量打该表（I4，纵深，有盲区）；(iii) **DB 引擎 Hard（已落 PR-01 migration 058）** = serving DB role `REVOKE UPDATE, DELETE ON projection_events`（DB 引擎强制不可绕，同 #1676 restricted role）。**权限集校正**：`10-restricted-role.sh` 默认只 `GRANT SELECT,INSERT,UPDATE,DELETE`——TRUNCATE 从未默认授予（留 table owner，无需撤），UPDATE 被默认授予且违反 append-only（必须撤），故正确集 = `REVOKE UPDATE, DELETE`（非旧文「DELETE,TRUNCATE」）；保留 SELECT（replay/Position 读）+ INSERT（同事务 journaling writer，`ON CONFLICT DO NOTHING` 不需 UPDATE） | (i)+(ii) 接口/archtest（有盲区，Medium 纵深）；(iii) DB-role REVOKE（**Hard，PR-01 migration 058 已在位**） | I4 **Hard（DB 引擎 REVOKE）已在位**；archtest = Medium 纵深（见 §6） |
| **D8** | **retention/归档约束**：未来归档/截断 `projection_events` 须 ≥ 最慢投影 checkpoint（`MIN(projection_checkpoints.offset_seq)`），否则丢失可重建历史。归档能力本身 **out-of-scope**（镜像 #1609 D7：本 ADR 只交付 retained 源 + 该下界约束，不交付归档/截断机制）。topic-filter（D4）已使增长仅限 projection-relevant 事件，归档延后可接受 | 设计约束（归档机制 out-of-scope）+ `docs/ops` runbook 记录 | —（约束，非机制） |
| **D9** | **gate 移除 = fail-closed→production-safe 的 flip，须 gated on 证明**：去掉 `GOCELL_PROJECTION_PG_JOURNAL_PREVIEW` env gate + preview WARN（`cmd/corebundle/bundle_options.go`）的动作**置于 PR-04**（在 T-06-2 PG e2e 证 rebuild 健全 **+** PR-05 no-DELETE 守卫到位之后，同 PR 内 flip）；**不**在 PR-03（PR-03 只把 durable source 挂到 gate 下 + 删旧 source，posture 不变）。删 gate 后 `202605261620` 的 fail-closed compensation 被"默认 production-safe"取代 | gate 移除前置 = {PR-01 source/probe + PR-02 双写测试 + PR-04 T-06-2 e2e + PR-05 no-DELETE + runbook/rollback} | —（删除，无新机制；前置门 gated） |

---

## 4. 接口层

### 4.1 表（D3）

```sql
-- migration 0NN（next available）: create projection_events
-- 持久、append-only、永不删的投影事件 journal（model-a retained event store；#1504）。
CREATE TABLE IF NOT EXISTS projection_events (
    global_seq     BIGINT      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,  -- 位置（复刻 mig-049 / saga_events.global_seq）
    id             TEXT        NOT NULL,            -- Entry.ID() → EventID()（幂等 + cursor 键）
    aggregate_id   TEXT        NOT NULL DEFAULT '',
    aggregate_type TEXT        NOT NULL DEFAULT '',
    event_type     TEXT        NOT NULL,
    topic          TEXT        NOT NULL DEFAULT '',  -- RoutingTopic() → Stream()
    payload        JSONB       NOT NULL,
    metadata       JSONB       DEFAULT '{}',
    observability  JSONB,                            -- RestoreContext obs 信封
    principal      JSONB       NOT NULL DEFAULT '{}', -- RestoreContext principal 信封（background/system ctx 无 auth principal 时序列化为 {} 非 null，对齐 outbox_entries；at-rest 永久留存见 §5 PII 行）
    created_at     TIMESTAMPTZ NOT NULL,
    occurred_at    TIMESTAMPTZ NOT NULL              -- OccurredAt()（域事件时间）
);
-- 新表，建表即建唯一索引，无锁争用——故 NOT CONCURRENTLY（CONCURRENTLY 用于给现有热表加索引、且不能在事务型 migration 内运行；#1609 给已存在的 saga_events 加 global_seq 列才用 CONCURRENTLY，形态不同）。
CREATE UNIQUE INDEX IF NOT EXISTS idx_projection_events_id ON projection_events (id);
-- global_seq 为 PK，replay 的 WHERE global_seq > $1 ORDER BY global_seq 由 PK btree 直接服务，无需额外索引。
```

- 列集 = `outbox_entries` **减去 relay 内部投递状态列**（`status`/`attempts`/`next_retry_at`/`claimed_at`/`last_error`/`dead_at`/`lease_id`/`published_at`）——journal 不发布、不重试，不需要它们；只保留 `EntryScan` 重建所需列。
- mem 后端：append-only slice + 单调 `int64` 计数器（`global_seq` = 1-based 密集索引），与 `MemReplaySource` / saga `MemJournal` 同形。

### 4.2 source（D2/D3）

- `PGProjectionEventSource`（`adapters/postgres`）+ `MemProjectionEventSource`（kernel/test）各自实现 `projection.ReplaySource`（`Replay`/`Head`）+ `projection.Cursor`（`Position`）：
  - `Replay(ctx, fromOffset, fn)`：`SELECT <列> FROM projection_events WHERE global_seq > $1 ORDER BY global_seq`，逐行经 `EntryScan.ToEntry` 重建载体、调 `fn`，分批。
  - `Head(ctx)`：`SELECT COALESCE(MAX(global_seq), 0) FROM projection_events`。
  - `Position(entry)`：**读载体自带的 `global_seq`**（source 把行包成携带 `global_seq` 的 journal 载体，照 `sagaProjectionEvent` 形态）——非 `SELECT seq WHERE id=` DB 往返。不可解析载体返回 `outbox.NewPermanentError`（Cursor 不变式 #4）。
- **live-carrier resolution（rebuild 与 live 的载体协议统一；resolver 落 PR-03 wiring）**：`Position` 只认 `*JournalEvent` 自带 `global_seq`，故 **rebuild 路径**（`Replay` 自身产出 `JournalEvent`）天然闭合；但 **live 投递**经 `ConsumerBase` 推的是**裸 `outbox.Entry`、不带 `global_seq`**，直接喂 `Position` 会判 `PermanentError`。当 durable source 被 wire 成 Coordinator 的 live Cursor（PR-03）时，须在**投递边界**先把裸 `outbox.Entry` 解析成携带 `global_seq` 的 `JournalEvent`：`SELECT global_seq FROM projection_events WHERE id = entry.EventID()`。该查找**安全**——journal 永不 cleanup，且 D4 emit 期同事务双写令该行在事件投递前已提交，故必不 `ErrNoRows`（正是 transient outbox 路径破掉、产生 #1504 根 bug 的失败模式）。「rebuild 用 retained 载体、live 用 transient 载体」的割裂由此消除（对标 Axon `TrackingToken` / Marten high-water mark：live 与 replay 都围绕 retained stream position）。**PR-01 只交付 rebuild source + carrier-read `Position`；live-carrier resolver 及其 live-path 回归落 PR-03**（裸 `outbox.Entry` 必须解析、不得 permanent-error）。
- per-spec topic 过滤仍在 Coordinator（`entry.Stream() == c.spec.Topic`，#1482 已落，与位置源正交，不改）。

### 4.3 写路径（D4，装饰器）

```go
// adapters/postgres：journaling Writer 装饰器（emit 期同事务双写）
// projectionTopics = composition root 从 cellgen 派生的 projection-source topic 集注入。
// 保留基础 *OutboxWriter 的 BatchWriter 能力（Write + WriteBatch），两者经单一
// chokepoint journalProjectionSubset 收口，故 I2 caller-allowlist 唯一项即该 chokepoint。
type journalingOutboxWriter struct {
    inner            *OutboxWriter            // 基础 outbox 写入（保持通用）
    projectionTopics map[string]struct{}      // topic-filtered（D4）
}

func (w *journalingOutboxWriter) Write(ctx context.Context, e outbox.Entry) error {
    if err := w.inner.Write(ctx, e); err != nil {           // 写 outbox_entries（已 ambient-tx）
        return err
    }
    return w.journalProjectionSubset(ctx, []outbox.Entry{e})
}

func (w *journalingOutboxWriter) WriteBatch(ctx context.Context, es []outbox.Entry) error {
    if err := w.inner.WriteBatch(ctx, es); err != nil {     // 保留多行 INSERT 批写
        return err
    }
    return w.journalProjectionSubset(ctx, es)
}

// journalProjectionSubset：过滤 projection-source topic，是 appendProjectionEvents 的唯一调用点。
// appendProjectionEvents 未导出、批量 INSERT ... ON CONFLICT (id) DO NOTHING（I2 forge 封口）。
```

- `appendProjectionEvents` **未导出**：包外无任何导出 append API（source 是只读，conformance 走 seed-persists 契约）→ 写侧 forge 封口为 Hard/Hard（I2，§6）。批量多行 INSERT（复刻 `outbox_writer.go` 的分块），单写经 1 元素切片走同一路径。
- 装饰器作为 `Writer` 注入 `WriterEmitter`，复用既有 emit 漏斗——**无新 emit 路径、producer 零改动**；保留 `BatchWriter` 故批写不静默退化为顺序写。

### 4.4 wiring（D5/D9，**分两步：先挂 gate 后默认化**）

- **PR-03（wiring，posture 不变）**：`cmd/corebundle/bundle_options.go::projectionRuntimeOptions` 构造新 durable source 填 `WithProjectionReplaySource`+`WithProjectionCursor`（保留 `WithProjectionCheckpointStore`/`WithProjectionTxRunner`），但**仍挂在既有 `GOCELL_PROJECTION_PG_JOURNAL_PREVIEW` gate 下**（gate 现选 durable source，非旧 outbox reader）——生产 posture **保持 fail-closed**，gate-on 即可跑 T-06-2 e2e。**删除** outbox-backed `PGProjectionReplaySource`/`Cursor`（无双路径）。
- **PR-04（默认化，gated on 证明）**：T-06-2 PG e2e 证 rebuild 健全 + PR-05 no-DELETE 守卫到位**之后**，**删 gate** → durable source 成默认（无需 preview）。这是 fail-closed→production-safe 的安全 flip（同 PR 内 e2e 绿 = 已证明）。
- `checkProjectionDeps` fail-fast 全程作为安全网保留。

---

## 5. 威胁矩阵

| 威胁 | 机制 | 覆盖 | 遗留 |
|------|------|------|------|
| **rebuild-from-0 不健全**（读 transient outbox，删行 → permanent error / rebuild 中止）——#1504 根 bug | D1+D3：专用 append-only `projection_events`；`Position` = 行自带 `global_seq`、无删行查找；永不 cleanup（D7/I4；serving role 自 PR-01 经 migration 058 `REVOKE UPDATE,DELETE`、DB 引擎强制 append-only——覆盖加强，非降级） | ✅ | **bootstrap gap（v1 已知 limitation，见 §8）**：journal 仅从 PR-02 装饰器部署起 append，部署前历史事件不在表内——新投影/新 topic 的 full rebuild 只覆盖部署后历史（同 Debezium start-from-now 结构性形态，非 bug） |
| **live-path `Cursor.Position` gap**（live 事件的 outbox 行先被 cleanup → dead-letter） | 同上 + D4 emit 期同事务双写：journal 行在事件投递前已提交，故 §4.2 的 **live-carrier resolver（PR-03）** 用 `id → global_seq` 查找必然命中、live `Position` 解析成功，删行 permanent-error 路径结构上不可达 | ✅（结构性） | resolver impl + live-path 回归落 **PR-03**（#1770）；PR-01 无 live wiring 故当前不可触发（裸 `outbox.Entry` 直喂 `Position` 会 permanent-error，由 PR-03 投递边界解析闭合） |
| **写路径原子性**（journal 行是否与业务事实同提交） | D4 装饰器在 producer 既有 `RunInTx` 内同事务 append（`persistence.TxFromContext`，无新 tx 边界）+ `ON CONFLICT (id) DO NOTHING` | ✅ | — |
| **leader 交接 mid-rebuild**（多 pod 两实例推进同一 checkpoint） | D6(a) apply+advance 同事务；D6(b) **`AdvanceIfOwner` 尚不存在**（grep 确认）。v1 继承 #1100 Q5 单 pod 边界 | ⚠️ | **文档化 v1 单 pod 边界**（`projection_checkpoints.owner` 保留不写），多 pod fencing CAS = 前向扩展（PR-PG，届时定义 #1504 自己的 `AdvanceIfOwner`）。**精确范围**：`ON CONFLICT DO NOTHING` 幂等只覆盖 **journal 行写入**（D4）；**checkpoint advance** 是无条件 upsert（无 CAS）；**Apply fn 业务幂等性由各投影实现保证、非 framework**。`ConsumerBase` 串行只序列化**同 pod live 路径**，**不**覆盖跨 pod rebuild——故双 pod 并发 rebuild 会 double-apply。单 pod 是**运维层约束**（无代码级 mitigation），多 pod 须待 PR-PG CAS |
| **无界增长**（append-only 永不删；归档截断到 checkpoint 之下丢事件） | D8：归档须 ≥ 最慢投影 checkpoint；D4 topic-filter 使增长仅限 projection-relevant 事件 | ⚠️ | 归档能力本身 out-of-scope（同 #1609 §5 增长行）；D8 记下界约束待归档落地 |
| **伪造 / 越界投影事件**（业务包注入 source 会重放为真值的行） | I2 append caller-allowlist（**Hard/Hard**：未导出 append + 包内 allowlist）——唯一 append 路径是 sanctioned 装饰器；source 只读 sealed `projection_events`；载体 `ProjectionEvent` 只读。forge 防护在 **wiring 层**（同 #1609 §5 forge 行） | ✅ | — |
| **身份 / impersonation**（rebuild 触发 admin 身份 / 后台 ctx 穿透进 Apply / audit） | **复用已落地 #1627 修复**：`rebuild.go` detach 边界 `clearAmbientPrincipal(context.WithoutCancel(ctx))`；outbox carrier `RestoreContext` 在 clean ctx 上 no-overwrite。`PROJECTION-SYSTEM-PRINCIPAL-INSTALL-CALLER-01` 已 green | ✅（#1627 已落） | — |
| **`principal`/`observability` 信封 at-rest 永久留存（PII）**——`outbox_entries` 72h/30d 后删行，journal **永不删**（D7），故 actor/subject/tenant/`session_id` 永久落盘 | at-rest **不脱敏是既有姿态**：`audit_entries` 已永久存 principal 派生数据，redaction 在 **sink 侧**（slog/span/wire，observability.md），DB 列 by-design 存原值；`projection_events` 是**内部表、永不 wire 出站**，承袭同姿态；`session_id` 命中 `IsSensitiveKey` 仅 sink 脱敏 | ⚠️（接受） | 若未来数据保留/合规要求 principal 过期，须**对称**作用于 `audit_entries`（同 at-rest 永久姿态），out-of-scope 本 ADR；D4 topic-filter 已把留存面限到 projection-relevant 事件 |
| **乱序 / 非 exactly-once** | `global_seq` 稳定全序（非 `created_at`）；Coordinator 按 position 处理；幂等 upsert + `pos <= checkpoint` skip | ✅ | 继承 #1100 串行交付前提（已 enforce） |

---

## 6. AI-robust 评级

> 每条 invariant 的完整盲区清单 + 反向自检（RED/GREEN fixture + anti-vacuity）活在落地 PR 的 archtest package godoc（单源，per `ai-robust.md`）；本表只导航。所有新约束 ≥ Medium（无 Soft 立项）；Medium 上游天花板均开 gh 跟踪。

| ID（占位，落地 PR 定型） | 摘要 | 评级（双向锁分轴） |
|---|---|---|
| **I1 — `PROJECTION-EVENT-JOURNAL-SOURCE-CONFORMANCE-ENROLL`** | 新 source（mem + PG）入既有 `RunReplaySourceConformance`/`RunCursorConformance`——**骑现有** `PROJECTION-REPLAY-SOURCE-CONFORMANCE-ENROLL-01`/`PROJECTION-CURSOR-CONFORMANCE-ENROLL-01`，新 impl 自动纳入，无新 archtest 文件 | **Medium**（typed impl-discovery + conformance 调用扫描；Go 无法编译期要求某类型有 `_test.go`。Hard 路径 = codegen golden 枚举 impl，**共享 gh #1003**） |
| **I2 — `PROJECTION-EVENT-JOURNAL-APPEND-CALLER-01`**（#1504 forge 防护，封写侧） | append `projection_events` 收口单一 sanctioned 写路径 | **Hard/Hard fully-closed**：上游 = append 是 `adapters/postgres` 包内**未导出**方法 `appendProjectionEvents`（**包外**调用编译不可表达）；下游 = archtest caller-allowlist 扫 `adapters/postgres` **包内所有** callsite、锁到唯一 chokepoint `journalingOutboxWriter.journalProjectionSubset`（`Write` 与 `WriteBatch` 都经此单一收口 append——闭合"同包其他函数直接调 `appendProjectionEvents`"盲区，上游 unexported 只防包外，包内由下游 whole-package scan 闭环）。生产无任何导出 append API（read-only source + seed-persists conformance）。比 #851/#893/#1282 族更紧：那些 append 与 caller **跨包**、受 Go 可见性天花板限制只能 Medium 上游；本条同包，下游 archtest 可完整闭合 |
| **I3 — `…-APPEND-TX-BOUND`** | ~~append 经 ambient tx 同事务~~ **不单独立项**：被 I2 + 既有 `PG-REPO-AMBIENT-TX-01` 包含——append 就在已 ambient-tx 的 `Write` 体内，同事务是结构性的，独立 archtest 冗余（最小化 enforcement 集） | —（subsumed） |
| **I4 — `PROJECTION-EVENT-JOURNAL-NO-DELETE-01`**（D7 append-only） | serving role 不能 UPDATE/DELETE `projection_events`（主守卫）；生产代码不发 `DELETE`/`TRUNCATE` 字面量（纵深） | **Hard（DB 引擎 REVOKE，PR-01 已在位）+ Medium archtest 纵深（PR-05）**——主守卫 = migration 058 `REVOKE UPDATE, DELETE ON projection_events FROM gocell_app`，DB 引擎层不可绕（同 #1676 `gocell_app` restricted-role 范式），自 PR-01 起对 serving role 强制 append-only。下游 archtest（PR-05）SQL-literal scan 锁 `DELETE`/`TRUNCATE … projection_events` 字面量为**纵深防御**，补 DB-REVOKE 够不着的盲区：**owner/migration 上下文误删**（migration 以 table owner 跑、保留 DELETE）、动态拼接 SQL、其它 adapter 包的 raw `pgx.Exec`、包内旁路。盲区清单 + 反向自检 RED/GREEN fixture 活在落地 archtest godoc。**gate-flip 阻塞前置**（PR-04 删 gate 前 no-DELETE 守卫须在位）**已由 PR-01 DB 引擎 REVOKE 满足**；PR-05 archtest 为纵深、非阻塞前置。未来 archive 落地需在 allowlist 加 DELETE callsite（须引 archive ADR 章节号 per `contract-fanout.md`） |
| **I5 — `PROJECTION-EVENT-JOURNAL-TOPIC-ALLOWLIST-DERIVED-01`**（D4 topic-filter） | 双写的 topic 集从投影合约 metadata 派生（cellgen），非手写字面量列表——加投影自动纳入 journal | **Hard**（双轴，PR-02 落地，原计划 Medium 已上修——见 §Amendment 2026-06-12）：上游 = `generatedProjectionSourceTopics()` 由 `kernel/assembly.GenerateModulesGen` 从 `slice.yaml contractUsages` 派生、`gocell generate assembly --verify`（`tools/generatedverify`）字节锁 `modules_gen.go`，metadata 改而未 regen 即 CI 红；派生正确性由 `TestCollectOutboxProjectionTopics` 守。下游 = 本 archtest 强制每个 `NewJournalingOutboxWriter` callsite 的 topic 实参经 go/types 解析为 `generatedProjectionSourceTopics()`（手写 `[]string{…}` 字面量、别的函数、变量均 fail-closed），闭合"在 callsite 手打绕过 golden 派生"盲区 |

**I2 vs 既有 `PROJECTION-EVENT-CARRIER-TYPED-01`**：后者是**单轴 type-system Hard（API shape）**——只 gate"公开 API 不再裸收 `outbox.Entry`"，`ProjectionEvent` 全导出可实现、载体来源**不**封闭。**I2 才是 #1504 的真 forge 防护**（封*写侧*：只有 sanctioned 装饰器能把行放进 source 读的 journal），与 #1609 §5 forge 行同款 wiring-层（非 interface-构造层）保护。

---

## 7. 拒绝的备选

| 备选 | 拒绝理由 |
|------|---------|
| **沿用 `outbox_entries.seq`（现状，preview-gated）** | transient relay 删行（`CleanupPublished`/`CleanupDead`）→ rebuild + live-path 两 gap 持续。即 #1504 bug 本身 |
| **让 relay 对 projection-consumed topic 永不 cleanup（retention floor，issue 选项 1）** | 需一个当前不存在的 topic registry；outbox 对这些 topic 无界增长；把 relay buffer 与 event store 两种语义混在一张表——`202605261620` §Amendment 已称复用 transient relay 是 "wrong foundation" |
| **write-path = relay copy-on-publish（CDC 式）** | append 脱离业务提交（在 relay publish tx，非 producer tx）；live-path 重引入 at-least-once + 排序竞态（journal-append vs broker-delivery 顺序）。生产者透明是其唯一优势，但 outbox writer 本就是框架漏斗，该优势不成立。D4 emit 期双写按构造关闭 live-path gap |
| **write-path = journal-all（不 topic-filter）** | 写死无人读的行；增长无界、只能靠尚未实现的 archive 才有界 = 留尾，违 彻底/优雅。D4 topic-filter 增长有界 by construction |
| **泛化 saga `journal.GlobalReader` 出 saga（共享接口）** | `GlobalEvent.InstanceID` saga 专属；两 journal 信封不同，泛化会泄漏/裁剪字段并制造跨域契约耦合，无当前第二消费者。镜像 #1609 "新窄接口、不并入 JournalCore" → 平行 reader |
| **改 harness `CoordinatorConfig` / 让 source-agnostic** | 无必要——`CoordinatorConfig` 已收任意 `ReplaySource`/`Cursor`，新 source 零 harness API 改动（#1609 已依赖同性质） |
| **本 PR 内做归档/快照** | out-of-scope（同 `202605261620` Q4 与 #1609 D7 的归档延后）；只交付 D8 下界约束 |
| **snapshot-based rebuild（issue 选项 3）** | issue 自标 orthogonal 的独立 Q4 trigger（winmdm Stage 1 ≥30min full rebuild），不直接解决 #1504 的 replay 缺陷 |

---

## 8. 后果

- 新增 `projection_events` 表（PG migration only-add）+ mem 等价；schema_guard 表注册。
- 新增 journaling Writer 装饰器（emit 期同事务双写，topic-filtered）；producer 零改动。
- harness 载体/API 零改动（复用 `cellvocab.ProjectionEvent` + `WithProjection*` 槽）。
- 删除 `GOCELL_PROJECTION_PG_JOURNAL_PREVIEW` gate + outbox-backed `PGProjectionReplaySource`/`Cursor`（无双路径）。
- journal retention 多一约束（D8）：归档须 ≥ 最慢投影 checkpoint。
- 解锁 T-06-2（#1368 follow-up，real PG e2e rebuild 测试，原 blocked on 本 retention 模型）。
- v1 单 pod 边界保持（同 #1100 Q5）；多 pod fencing CAS 待真实消费者（PR-PG）。
- **已知 v1 limitation — bootstrap gap**：`projection_events` 仅从 PR-02 装饰器部署时刻起 append，之前产生的 outbox 事件不在 journal——v1 full rebuild 仅覆盖部署后事件；需覆盖完整历史须部署前经 broker replay / snapshot 补全（Q4 snapshot #1100，YAGNI）。生产投影当前 hard-gated OFF，故首个真实投影自然从 journal 起点 rebuild，无遗留状态待迁移。
- **运维注意 — lag gauge 语义**：首次 full rebuild 后 `projection_event_replay_lag_seconds` 快速降至 ~0 仅表示读完 journal，**不**代表历史数据完整（bootstrap gap）；数据完整性须经 `projection_checkpoints.offset_seq` + 业务校验确认，不能仅看 lag（对齐 `eventbus.md` rebuild-lag 盲区注意范式）。
- **运维注意 — 增长监控**：append-only 表，归档（D8）out-of-scope 前持续增长（D4 topic-filter 已限到 projection-relevant 事件）；运营须监控 `pg_relation_size('projection_events')` / 行数并接入告警，`docs/ops` runbook 留对应条目（随实现 PR）。

---

## 9. 子 PR 映射（EPIC #1504，每 PR ≤ ~2000 行）

| PR | 范围 | 本 ADR 决策 | 依赖 / 解锁 |
|----|------|------------|-------------|
| **PR-00（本 PR）** | 本 ADR + `202605261620` §Amendment 2026-06-03 retention-boundary 段 + §6 Row 1 原地重写 + `202606051200-1609` §1.2 back-pointer + `eventbus.md` nav。`Refs #1504`（**不 Closes**，镜像 #1609 PR-00 不关闭 #1609） | D1–D9（设计） | 无；解锁 PR-01..05 |
| **PR-01** | `projection_events` migration（only-add `global_seq IDENTITY` + `idx`）+ `schema_guard` 表注册 + mem source + PG source（`Position` 读 `global_seq`）+ 入既有 conformance（I1，**含新增 "Position 返回 carrier 自带 `global_seq`、无额外 DB 往返" 场景断言**——回归 #1504 根 fix，可经 mock tx / statement 计数）+ **`projection_journal_ready` readyz probe**（`RepoReady()` + `CELL-REPO-READYZ-PROBE-01` 入列 + `PROBENAME-SEALED-FUNNEL-01` typed const）+ **扩 `OUTBOX-RECONSTRUCTION-CALLER-01` allowlist +1**（`PGProjectionEventSource` 调 `EntryScan.ToEntry` 重建载体）+ **serving-role `REVOKE UPDATE, DELETE`（migration 058，DB 引擎 append-only Hard，I4 主守卫前移）+ append-only 集成回归**（`TestProjectionEvents_AppendOnly_ServingRoleRevoked`：catalog `has_table_privilege` + 连 `gocell_app` 实测 INSERT 过 / UPDATE·DELETE 返 42501） | D2/D3/D7 | 依赖 PR-00 |
| **PR-02**（#1769，已落地） | emit 期同事务双写装饰器（D4，topic-filtered，**保留 `BatchWriter`**：`Write`+`WriteBatch` 经 `journalProjectionSubset` 收口）+ `PROJECTION-EVENT-JOURNAL-APPEND-CALLER-01`（I2，Hard/Hard）+ `…-TOPIC-ALLOWLIST-DERIVED-01`（I5，**Hard**：cellgen `generatedProjectionSourceTopics()` golden + cap_wiring 消费 archtest）+ composition-root 接线（always-decorate，topic 集 corebundle 今为空）+ `NewJournalingOutboxWriter` 纳入 `CAPABILITY-PROVIDER-FUNNEL-01` + **写路径集成回归测试**（`//go:build integration`，覆盖 `Write`+`WriteBatch`，**不甩 PR-04**）：①双写原子性——回滚两表同回滚；②topic-filter——非 projection-source topic 不入 `projection_events`（含 mixed-batch 子集）；③`ON CONFLICT (id) DO NOTHING` 幂等——同 `id` 二次 append `global_seq` 不变 | D4 | 依赖 PR-01 |
| **PR-03（#1770，已落地）** | corebundle wiring：durable source 填 `WithProjection*` 槽但**仍挂既有 gate 下**（gate 现选 durable，posture **保持 fail-closed**）+ **删 outbox-backed source**（无双路径）+ **live-carrier resolver**（投递边界把裸 `outbox.Entry` 经 `id → global_seq` 查找包成 `JournalEvent`，§4.2）+ **live-path 回归**（裸 `outbox.Entry` 必须解析、不得 permanent-error）。**不删 gate**（移到 PR-04）。**载体**：resolver 落为强制 `projection.LiveCursor`（`Cursor + LiveCarrierResolver`）——`Coordinator` cursor 槽位类型化 LiveCursor → 编译期 HARD、无 type-assert 软回退；saga tailer 仍取裸 `Cursor`，唯 Coordinator-wired 的 `SagaJournalSource` 补一个协议声明式 `ResolveCarrier`（无 bare push → 仅 intrinsic 载体幂等、余者 permanent）（详见 §Amendment 2026-06-14） | D5 | 依赖 PR-01+02；posture 不变 |
| **PR-04** | **T-06-2 PG e2e rebuild 证明 + 删 gate（production-default）+ runbook/rollback**：testcontainers cold-start / crash-restart / full-rebuild-from-0 over `projection_events` 证 cleaned-outbox 行不再破坏 rebuild；**e2e 绿后同 PR 删 gate** → fail-closed→production-safe 安全 flip（D9）→ finalize `202605261620` compensation 重写 | D1/D9（验证 + flip） | 依赖 PR-03 **+ PR-05**（no-DELETE 须先到位）；**解锁 T-06-2** |
| **PR-05** | `PROJECTION-EVENT-JOURNAL-NO-DELETE-01`（I4 archtest，Medium **纵深防御**）+ anti-vacuity + RED/GREEN fixture——锁 code-level `DELETE`/`TRUNCATE` 字面量，补 PR-01 DB 引擎 REVOKE 够不着的 owner/migration 上下文盲区 | D7 | 依赖 PR-01；**非删 gate 阻塞前置**（该前置已由 PR-01 DB 引擎 REVOKE 满足）；纵深守卫，宜在 PR-04 前落地但不阻塞 |
| **PR-PG（deferred）** | 多 pod fencing：定义 #1504 自己的 `AdvanceIfOwner` CAS + 激活 `projection_checkpoints.owner`（D6b）。**gated on 真实多 pod 消费者**（同 #1609 PR-PG 姿态） | D6(b) | deferred；v1 单 pod 边界保持至此 |

依赖：PR-01→02→03 串行；**PR-04 依赖 PR-03**（gate 移除前 T-06-2 e2e 证明）+ no-DELETE 守卫前置——该前置**已由 PR-01 的 DB 引擎 REVOKE（migration 058）满足**（serving role append-only 自 PR-01 在位），PR-05 archtest 为 code-level 纵深、不再是 gate-flip 阻塞门；**PR-05 依赖 PR-01**。**关键不变式**：fail-closed gate 的移除（production-default flip）**只在 PR-04**、且在 T-06-2 e2e 绿之后——绝不在证明前默认启用。#1482（per-spec replay filtering）已落（Coordinator 层，与位置源正交，无需改）。

---

## 10. ref

ref: axoniq/AxonFramework JdbcEventStore + TrackingToken — retained event store 即投影源
ref: JasperFx/marten Async Projection Daemon over mt_events — PG 后端最贴近范本
ref: EventStoreDB/Kurrent `$all` + persistent-subscription position
ref: debezium/debezium outbox-event-router — transactional-outbox-as-CDC（下游 retained log，被否决的 copy-on-publish 范本）
ref: docs/architecture/202605261620-adr-cqrs-projection-lifecycle-harness.md §Amendment 2026-06-03（#1504 retention-boundary，本 ADR 关闭其设计项）
ref: docs/architecture/202606051200-1609-adr-saga-journal-projection-source.md（saga model-a，复用其 `cellvocab.ProjectionEvent` + #1627 身份修复地基）

---

## §Amendment to parent ADR `202605261620`（同 PR 内重写）

per `ai-robust.md` §"ADR amendment 落地必查"，本 PR 在 `202605261620-adr-cqrs-projection-lifecycle-harness.md` 内**原地重写**（不留两套真理源）：

1. **§Amendment 2026-06-03 "Retention boundary" 段**：原文"The durable append-only projection journal that removes the limitation is tracked at gh #1504 (P1) as the C1 design item" → 改为指向本 ADR：设计**已接受**（本 ADR），**能力**由其 PR-01..04 交付（PR-00 仅 ADR，未实现）；T-06-2 待 PR-04 解锁。compensation 段（hard gate）标注：本 ADR **PR-04**（在 T-06-2 e2e 证明 + PR-05 no-DELETE 之后）删 gate 后被"默认 production-safe"取代——**PR-03 不删 gate**（只把 durable source 挂 gate 下、posture 不变）。
2. **§6 威胁矩阵 Row 1 + §Amendment 2026-06-03 的 Row-1 重评**：transient-journal residual 由本 ADR 的 `projection_events`（D1）**解决**；状态 transient→durable，**显式声明不翻 ⚠️/❌**（是加强）；#1504 指针从"P1 backlog 设计项"改"已接受 ADR、实现进行中"；当 **PR-04**（非 PR-03）删 gate 后 Row 1 落"durable, ungated"。**逐行重评（当前完整 8 行）**：**Rows 2–7**（2026-06-03 块原列）`unchanged`；**Row 8**（whole-journal replay interleaving / catchup termination，由 §Amendment 2026-06-04 #1482 + #1574 引入的独立行，**不在** 2026-06-03 块内）亦 `unchanged`——本 ADR 只换位置源 transient→durable，不触 crash-recovery / rebuild-read-consistency / serial-delivery / GAP-8 / multi-pod / catchup-termination 任一机制。

`202606021000` saga ADR 与 `saga-runbook.md` **不受本 ADR 影响**（saga_events 载体不同），无 saga 侧 amendment。

---

## §Amendment 2026-06-11（PR-01 #1825，原地重评）

PR-01（#1825）落地时把 D7(iii) 的 **DB 引擎 serving-role REVOKE 前移进 migration 058**（原计划为「未来 gh 跟踪的真 Hard 升级」、PR-05 仅承诺 Medium archtest），并补全 F1 的 live-carrier 协议规约。per `ai-robust.md`「ADR amendment 落地必查——同步重评威胁矩阵/安全模型，冲突段落同改动重写」，本段记录重评（D7 / §4.2 / §5 / §6 I4 / §9 已就地重写，本段为审计痕）：

1. **I4 主守卫升 Hard（D7(iii) / §6 I4 / §9 PR-01·PR-05）**：append-only 主守卫从「PR-05 Medium archtest」升为「**PR-01 DB 引擎 REVOKE（Hard）**」。理由：建表即下 REVOKE 是最干净时机（不必日后再发改权限迁移），DB 引擎层不可绕、强于 archtest（AI-HARD：Hard>Medium）。archtest（PR-05）**不删**，重定位为 code-level 字面量**纵深防御**——覆盖 DB-REVOKE 够不着的盲区（owner/migration 上下文误删、动态 SQL、跨 adapter raw `pgx.Exec`、包内旁路）。两守卫覆盖面互补，非冗余。
2. **权限集校正（D7(iii) / §6 I4）**：原文写「REVOKE **DELETE, TRUNCATE**」有误。`deploy/postgres/init/10-restricted-role.sh` 默认只 `GRANT SELECT,INSERT,UPDATE,DELETE`——TRUNCATE 从未默认授予（留 table owner，无需撤）；UPDATE 被默认授予且违反 append-only（必须撤）。正确集 = `REVOKE UPDATE, DELETE`（migration 058 已实装）。
3. **§9 gate-flip 硬序重评**：原「PR-04 依赖 PR-05（no-DELETE 守卫前置）」——该前置现**由 PR-01 的 DB 引擎 REVOKE 满足**（serving role append-only 自 PR-01 在位）。PR-05 archtest 降为纵深、非阻塞门。删 gate 仍只在 PR-04、仍 gated on T-06-2 e2e 绿——此不变式不动。
4. **§5 威胁矩阵**：rebuild 行的「永不 cleanup（D7/I4）」加强为「serving role DB 引擎强制 append-only（PR-01）」；**显式声明是覆盖加强、非降级**，无威胁行翻 ⚠️/❌。
5. **F1 — §4.2 / §5 row 2 / §9 PR-03 补全**：补 live-carrier resolution 协议规约（裸 `outbox.Entry` 经 `id → global_seq` 在投递边界解析、包成 `JournalEvent`，落 PR-03），使「live Position 必然解析成功」的断言有规约支撑、PR-03（#1770）有显式验收项；统一 rebuild/live 载体协议。

来源：Codex `pm:pr-review` PR #1825（F1 live 载体 / F2 append-only 权限，均 Cx3），经 `/fix #1825` 收口。

---

## §Amendment 2026-06-14（PR-03 #1770，原地重评）

PR-03（#1770）落地 §9 PR-03 范围：corebundle 切到 durable `PGProjectionEventSource`（单实例同填
`WithProjectionReplaySource`+`WithProjectionCursor`，挂既有 `GOCELL_PROJECTION_PG_JOURNAL_PREVIEW` gate 下、
posture 保持 fail-closed，并注册 `projection_journal_ready` probe）+ 删 outbox-backed
`PGProjectionReplaySource`/`PGProjectionCursor`（无双路径）+ 落 live-carrier resolver。per `ai-robust.md`
「ADR amendment 落地必查」，记录本次**载体决策与威胁矩阵重评**：

1. **live-carrier resolver 载体 = 强制 `projection.LiveCursor`（编译期 HARD）**：§4.2 只规定协议（投递边界裸
   `outbox.Entry` 经 `id → global_seq` 解析成 `JournalEvent`），未定 enforcement 载体。PR-03 选**新增
   `LiveCarrierResolver` 接口 + 组合接口 `LiveCursor = Cursor + LiveCarrierResolver`**，并把
   `Coordinator`/`CoordinatorConfig.Cursor` 与 `bootstrap.WithProjectionCursor` 的 cursor 槽位类型化为
   `LiveCursor`。效果：任何接入 `Coordinator` 的 cursor 漏实现 `ResolveCarrier` **不编译**（最强档），
   `buildHandler` live 路径单一调用 `c.cursor.ResolveCarrier`、**无 type-assert 软回退、无运行期 fail-fast**。
   行为回归经既有 `RunCursorConformance`（加 resolver 一致性 sub-test，凡实现 resolver 的 enrolled impl 自动覆盖）
   + per-impl 测试 + coordinator live-path 回归。
2. **不折进基 `Cursor` 接口（最小化 saga 触达）**：`projection.Cursor` 被 saga 读模型复用
   （`kernel/saga/sagaprojection.SagaJournalSource`，生产经 `runtime/saga/tailer` replay/pull-only、无 live push）。
   组合接口 `LiveCursor` 只约束 `Coordinator` 的 cursor 槽位——故 saga **tailer**（取裸 `Cursor`）、enroll fixture、
   tailer 测试 fake **全不受影响**；若改折进基 `Cursor` 则它们也被强制实现。**唯一例外**：`SagaJournalSource` 本身
   在 `rebuild_wiring_test.go` 经 `projection.NewCoordinator` 装配（测 Coordinator rebuild 身份行为），故须满足
   `LiveCursor`——为它补一个**协议声明式** `ResolveCarrier`：intrinsic `*sagaProjectionEvent` 载体幂等返回，其余
   permanent（saga 无 bare-entry live push，非死代码而是显式声明其载体协议）。生产 saga 读模型仍走 tailer、不经
   Coordinator，故此方法生产路径不触发但语义正确。
3. **威胁矩阵 / 安全模型重评 = 无翻转**：resolver 协议与 §4.2 / §5 Row 2 一致（D4 双写令行投递前已提交 + journal
   永不 cleanup → `id` 查找必命中；genuinely-absent → permanent，查询故障 → transient）。本 PR 只**指定其
   enforcement 载体**、不改协议或位置源，故 §5 / §6 各行 `unchanged`；gate 移除仍只在 PR-04（gated on T-06-2
   e2e）、posture 不变。
4. **MANAGED-RESOURCE-COMPLETENESS-01 opt-out 同步**：删 `PGProjectionReplaySource`/`PGProjectionCursor` 条目；
   补 `PGProjectionEventSource`（PR-01 漏登）。同 PR 顺带补 `WebhookSourceRepository`（#1540，pre-existing
   nightly red、同 map、storage-facade 分类无歧义）使该 all-or-nothing 完整性规则回绿。

来源：`/ship #1770`（EPIC #1504 PR-03），调整自 codex review F1（gate 移除 gated on 证明，Discovered via /fix #1765）。

---

## §Amendment 2026-06-12（PR-02 #1769，原地重评）

PR-02（#1769）落地 D4 写路径装饰器 + I2/I5 时做了两处相对 PR-00 设计的强化，per
`ai-robust.md`「ADR amendment 落地必查——同步重评威胁矩阵/安全模型」，本段记录（§4.3 /
§6 I2·I5 / §9 PR-02 / §0 已就地重写，本段为审计痕）：

1. **I5 升 Medium→Hard（§6 I5 / §9 PR-02）**：原计划「Medium（metadata 派生成员）；Hard 路径 =
   cellgen golden 字节锁，开 gh 跟踪」。落地时发现唯一可行派生口就是 codegen（composition-root
   provisioning 期无运行时 metadata），而既有 `gocell generate assembly --verify`（`generatedverify`）
   对 `modules_gen.go` 的字节锁是**免费自带**的——故 golden Hard 即得，无需另开 gh 延后。新增
   `generatedProjectionSourceTopics()`（`GenerateModulesGen` 派生）+ cap_wiring 消费 archtest
   （`TOPIC-ALLOWLIST-DERIVED-01`）构成双轴 Hard。**无 gh 跟踪项需关闭**（原 Hard 路径从未开
   issue）。**威胁矩阵无行翻转**：I5 强化只收紧 D4 topic-filter 的派生闭环，不触 §5 任一行。
2. **装饰器保留 `BatchWriter`（§4.3）**：PR-00 §4.3 草图仅示 `Write`。落地保留基础
   `*OutboxWriter` 的 `BatchWriter`（`Write`+`WriteBatch`），二者经单一 chokepoint
   `journalProjectionSubset` → 未导出 `appendProjectionEvents`（批量 INSERT ... ON CONFLICT）收口，
   故 I2 caller-allowlist 唯一项 = 该 chokepoint（非裸 `Write`）。理由：装饰器是 assembly 唯一
   outbox writer，丢弃基础类型的 `BatchWriter` 能力会让未来批量 emit 路径静默退化为顺序写——
   保留是 `不留小尾巴`，且 chokepoint 设计使 I2 仍单入口。**威胁矩阵无行翻转**（写路径原子性行的
   同事务双写机制不变，`WriteBatch` 同样在 ambient tx 内）。
3. **always-decorate + 接线归属（§9 PR-02/PR-03）**：corebundle 今无 outbox 投影，
   `generatedProjectionSourceTopics()` 派生空集；cap_wiring **无条件**包装装饰器（空集即 forward
   原样），故装饰器在生产路径被真实行使、非死代码，且加投影即自动 journal。**「生产 provider 的
   writer 必是 journaling 装饰器」的 Hard 守卫不在 PR-02**：该守卫只在读侧消费 journal 时才有意义
   （PR-03 wiring / PR-04 e2e），ADR enforcement 集刻意不含（`最小化 enforcement 集`），PR-04 T-06-2
   e2e 为下游网——登记为 **PR-03 验收项**。`NewJournalingOutboxWriter` 已纳入
   `CAPABILITY-PROVIDER-FUNNEL-01` 禁构造集（仅 cap_wiring + tests）。

来源：`/ship 1769`（内置 review 前自审：彻底/不向后兼容/优雅简洁/AI-HARD 四原则三层自查）。
