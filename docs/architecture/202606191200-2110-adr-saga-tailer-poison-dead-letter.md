# ADR 202606191200-2110 — saga-journal projection Tailer 的 poison-event 处置契约

- 状态：Accepted
- 日期：2026-06-19
- 关联：#2110；EPIC #1609（saga-journal projection source）；发现于 `/fix #2099` (Codex F5)
- 修订/重评：EPIC #1609 ADR `202606051200-1609-adr-saga-journal-projection-source.md` 的威胁矩阵
  「apply error 停摆 checkpoint」一行（见下文 §7）

## 1. 背景

saga-journal projection 消费者（如 orderfulfillment orderstatus `HandleOrderEvent`）对坏 payload /
未知 kind / 坏 EventID 返回 `outbox.NewPermanentError`。但底层 `runtime/saga/tailer.Tailer.commitEvent`
原先不区分 permanent vs transient：apply 一旦返错就整个事务回滚、checkpoint 不前进，下一 tick 在同一坏
事件上反复重试——**一个 poison 事件无限期冻结整条投影前进**，阻塞该 (cell, projection) 下**所有** saga
的状态投影，而不只那一条。

这与框架**早已文档化**的 `Apply` 契约（`kernel/projection/event.go`）相悖：transient → 重投，
**permanent → 路由到 dead-letter sink + 跳过，永不阻塞**。push-model 的 `Coordinator`（`classify` →
`DispositionReject` → broker DLX）兑现了该契约；pull-model 的 Tailer 没有 broker，是唯一违反者。

## 2. 决策

让 Tailer 兑现 `Apply` 契约的 pull-model 形态：permanent apply error（`outbox.IsPermanent`）→
在**一个事务**里把 poison event 记入专用 dead-letter 表 `saga_projection_dead_letters` **并**把
checkpoint 推过它（skip）；transient → 维持现状（停摆 + 退避重试）。

- 单一 permanent 判定 predicate = `outbox.IsPermanent`（两个投影 driver 共用，删除 Coordinator 私有副本）。
- dead-letter 落点 = **DB 表**（不是仅日志）：`record + advance` 在同一 `TxRunner.RunInTx` 提交，
  保证 **skipped ⟺ recorded**。
- 指标 = 复用 `saga_journal_tailer_checkpoint_advance_total{result="poison_skip"}`（不新增 instrument）+
  脱敏 Warn 日志。
- 落点接线经 `cellmodules/sagaprojectiondeps.Resolve`（topology-gated，第 5 个 resolved dep）。

## 3. 开源对标

| 框架 | 生产默认行为 | dead-letter 落点 |
|------|------|------|
| JasperFx/marten async daemon | `SkipApplyErrors=true`（连续模式默认）skip + dead-letter | PG 表 `mt_doc_deadletterevent` |
| AxonFramework streaming processor | 配 DLQ 后 skip sequence + 继续 | DB 表 `dead_letter_entry` |
| Kafka Connect/Streams | `errors.tolerance=all` 后 skip | DLQ topic |
| EventStoreDB persistent sub | retry → park | parked stream |
| Watermill | PoisonQueue middleware skip | Publisher → topic |

共识：① 永久停摆是反模式（仅 ESDB JS projection 如此，社区视为缺陷 EventStore#3436）；② dead-letter 必须有
**结构化耐久落点（表/topic）而非仅日志**（日志无事务保证、不可编程重处理、无结构化可观测——event-driven.io
论述）；③ dead-letter 写入与 checkpoint advance **同事务**；④ skip gated 在显式 permanent 判定。

`ref:` 见 §References。

## 4. 机制

`commitEvent` 分类 apply error；permanent → `skipPoisonEvent`（独立方法，保 `commitEvent` 认知复杂度 ≤15）：

```
apply tx 回滚（poison event 不留 read-model 副作用）
  → 干净事务： Record(dead-letter) ; AdvanceIfOwner(past pos)   // 同 RunInTx，原子
  → ObserveCheckpointAdvance(poison_skip) + 脱敏 Warn 日志
  → drained++（有进展）
record/advance 任一失败 → fail-closed：checkpoint 不动，下个 tick 重驱动（sink 恢复后仍可 skip）
```

表 `saga_projection_dead_letters`：自然复合 PK `(cell_id, projection_id, global_seq)` 兼作幂等键
（`ON CONFLICT DO NOTHING`，re-driven skip 不重复）；`error_type`（errcode Code，非 PII）+
`error_message`（脱敏）；serving role `REVOKE UPDATE, DELETE`（append-only 纵深防御）。

## 5. 不做 sequence-aware DLQ（偏离 Axon）

Axon 按 aggregate sequence 整体入队（防「后续事件应用到不一致状态」），且**明确不支持 saga 的 DLQ**。
GoCell saga journal 是单一全序流、Apply 幂等（`FoldStatus` 终态吸收）、无 per-aggregate 序依赖，poison 是
apply 层永久错误——故 **skip 单个事件**即可。与 GoCell saga-tailer / saga-engine 边界吻合。

## 6. L3 概念一致性：两个 dead-letter 实现非不一致

同一 `Apply` 契约现有两个 dead-letter 实现——Coordinator → broker DLX、Tailer → DB 表。这**不是**矛盾：
统一概念是「permanent → 耐久 dead-letter + 永不阻塞」，**实现按 transport 分化**（push/broker vs
pull/DB）。这正是 Marten/Axon（pull/DB-centric）选 DB 表、而 broker 消费者用 broker DLQ 的同因。故**不**做
「统一 disposition 机器」的重构（会引入跨模型抽象，过度工程）。

## 7. 威胁矩阵重评（EPIC #1609 ADR amendment）

原 #1609 ADR 把「permanent apply error → Tailer checkpoint fail-closed 停摆」记为既定行为。本 ADR
**改写**该行：

- 旧：permanent → 停摆，待人工介入（可用性风险：单坏事件冻结全投影）。
- 新：permanent → dead-letter + skip（可用性保住）；**transient → 停摆重试**（不变，correctness 优先）。
- 数据完整性：poison event 的 read-model mutation 被 apply tx 完整回滚（无 partial state）；事件本体仍在
  durable journal 的 global_seq 处，dead-letter 表是可查询的 poison 索引。skip ⟺ recorded 由
  `record + advance` 同事务保证。

## 8. 恢复（无 rebuild）

Tailer **无** rebuild/reset 路径（与 Coordinator 不同）。恢复 = 修根因 → **手动 rewind**
`projection_checkpoints.offset_seq` 到 poison `global_seq` 之前（Apply 幂等，重放安全，维护窗口内执行）→
admin 清理已恢复的 dead-letter 行。详见 `docs/ops/saga-runbook.md` §5b。

programmatic replay/evict 运维工具（对标 Marten replay / Axon `processAny`+`evict`）需独立 contract+ABAC
设计面，归 EPIC #1609 后续 backlog，本 ADR 不含。

## 9. 无 tenant RLS

`saga_projection_dead_letters` 与 `projection_checkpoints`(045) / `projection_events`(058) 同属框架内部、
cell/projection-keyed、在 tailer 的 system 身份下写入的运维表，非租户业务表，故不设 tenant_id / RLS。

## 10. AI-robust enforcement

| 机制 | 评级 | 载体 |
|------|------|------|
| `record + advance` 同 ambient tx | Medium | `PROJECTION-CHECKPOINT-TX-BOUND-01` 扩展（DeadLetterStore impl 不得持 `*pgxpool.Pool`，必经 `pgexec` ambient-tx） |
| `Record` 单一 sanctioned caller = `skipPoisonEvent` | Medium（下游 Hard go/types caller-allowlist，上游 Medium Go 可见性天花板） | archtest `SAGA-TAILER-DEAD-LETTER-WRITER-01`（+ RED/GREEN/NonVacuity fixture） |
| `AdvanceIfOwner` sanctioned callers = `{commitEvent, skipPoisonEvent}` | Medium | `SAGA-TAILER-CHECKPOINT-ADVANCER-CALLER-01` allowlist 扩展 |
| `poison_skip` 冻结标签值 | Hard（外部 golden 见证） | `SAGA-METRIC-LABEL-VALUES-FROZEN-01` 的 `sagaLabelEnumWant` |
| skip ⟺ recorded（store 必填） | Medium（runtime fail-fast） | `NewTailer` nil-guard + bootstrap `checkSagaProjectionDeps` |
| 表 shape | Hard-ish（启动校验） | `schema_guard` 列/PK 注册 |
| serving role 不可改/删 dead-letter | Hard（DB 引擎 REVOKE） | migration 067 |

## References

- ref: marten async-daemon error-handling SkipApplyErrors @martendb.io/events/projections/async-daemon.html
- ref: marten issue#2938 continuous-vs-rebuild-default @github.com/JasperFx/marten/issues/2938
- ref: axon SequencedDeadLetterQueue @docs.axoniq.io/axon-framework-reference/4.11/events/event-processors/dead-letter-queue/
- ref: watermill PoisonQueue middleware @github.com/ThreeDotsLabs/watermill/blob/master/message/router/middleware/poison.go
- ref: event-driven.io rebuilding-read-models-skipping-events @event-driven.io/en/rebuilding_read_models_skipping_events/
- 既有对标：projection_checkpoints(045) AxonFramework JdbcTokenStore 同事务 commit。
