# Saga 运维 Runbook

L3 saga 编排引擎（`runtime/saga`）的故障诊断与处置手册。三个互补载体：

- **本文件** — 故障场景的诊断 SQL、决策树、人工处置流程。
- `docs/ops/alerting-rules.md` §Saga — 指标（`gocell_saga_*`）告警规则 + `kind` 速查生成区。
- `docs/ops/readyz.md` §Saga probes — `saga_coordinator_ready` 探针语义 + status 状态表生成区。

设计决策真值源：ADR `docs/architecture/202606021000-adr-saga-l3-orchestration-engine.md`。

> **Saga instance status / event kind 速查**：状态表见 `docs/ops/readyz.md` §Saga（`saga-status-table` 生成区），`kind` 数值映射见 `docs/ops/alerting-rules.md` §Saga（`saga-event-kind-legend` 生成区）。两处均由 `gocell generate saga-coverage` 单源生成，本 runbook 不复制以免漂移。

---

## 场景 1：lease 卡死 / stale leader（实例停滞）

**症状**：某 saga instance 长时间停在 `Running` / `Compensating` 不推进；`gocell_saga_heartbeat_failed_total{reason="stale_lease"}` 持续增长。

**根因**：saga 每 instance 单 leader 经 `runtime/distlock` 选举；lease 由 per-step heartbeat goroutine 续租。leader 进程崩溃 / 网络分区 / heartbeat 失败 → lease 不再续租。

**诊断 SQL**：

```sql
-- 停滞实例：非终态但 updated_at 超过预期 lease 周期
SELECT id, definition_id, status, lease_id, lease_expires_at, updated_at
FROM saga_instances
WHERE status IN (2, 3)            -- Running / Compensating
  AND updated_at < NOW() - INTERVAL '5 minutes'
ORDER BY updated_at ASC;
```

**Log 诊断**：每条 per-instance saga 日志（心跳 / drive / 补偿 / leader 选举）均结构性保证携带 `instance_id` + `lease_id` 字段（`SAGA-SLOG-INSTANCE-FIELDS-CALLER-01`）。运维人员可通过 `instance_id=<id>` 过滤结构化日志，关联某卡死实例的 claim / 心跳活动，无需查询数据库。

**处置**：

1. **正常自愈路径（优先）**：lease TTL 过期后，任一存活 Coordinator 的 `ClaimPending` 会自动接管过期 lease 并继续驱动——**无需人工介入**。先确认是否有存活 Coordinator（检查 `saga_coordinator_ready` 探针 / leader 选举日志）。
2. **graceful handoff**：滚动重启 / 缩容时，退出中的 Coordinator 应调用 `distlock.Lock.Orphan()` 主动释放 lease（停止续租，key 按其 TTL 自然过期），让新 leader 在 bounded-TTL 内接管，而非等满超时。这是 `Orphan()` 的设计用途（graceful shutdown/handoff，见 `runtime/distlock` godoc）。
3. **无存活 Coordinator**：若集群无任何 Coordinator 在跑（全部崩溃），实例会停滞直到至少一个 Coordinator 恢复。恢复后经 `ClaimPending` 自动接管，无需手工改库。
4. **leader 未注入（unsafe 模式）**：若 Coordinator 启动日志含 `UnsafeModeLabel` 警告，说明未经 `WithLeaderElect` 注入 distlock——生产装配 bug。修复装配，不要手工改库绕过。

---

## 场景 2：补偿失败（StatusCompensationFailed，status=8）

**症状**：`saga_instances` 表中出现 `status=8`（`StatusCompensationFailed`）的行 —— 至少一个 saga 实例的补偿阶段本身失败（≥1 个 `CompensateFunc` 返回非 nil）。这与 `status=failed`（前向阶段失败，未进入补偿）是不同的终态；两者可通过终态值本身区分，无需读取事件日志。

**检测**：无 instance-level status counter 指标；CompensationFailed 是终态，经下方诊断 SQL 直接查 `saga_instances`，或在前向 step 失败率告警（`gocell_saga_step_outcome_total{outcome="failed"}`，见 `docs/ops/alerting-rules.md` §Saga）触发后下钻确认是否进入补偿失败终态。

**诊断 SQL**：

```sql
-- 列出近 1h 进入 CompensationFailed 的实例（status=8）
SELECT id, definition_id, started_at, updated_at
FROM saga_instances
WHERE status = 8 AND updated_at > NOW() - INTERVAL '1 hour'
ORDER BY updated_at DESC;

-- 查看特定实例的失败补偿步骤（kind=10 = KindStepCompensationFailed）
SELECT instance_id, version, step_name, created_at
FROM saga_events
WHERE kind = 10 AND instance_id = '<instance_id>'
ORDER BY version ASC;

-- 对比同实例的完整事件日志（kind 1-11，按 version 升序）
SELECT version, kind, step_name, created_at
FROM saga_events
WHERE instance_id = '<instance_id>'
ORDER BY version ASC;
```

> `kind` 数值速查见 `docs/ops/alerting-rules.md` §Saga（`saga-event-kind-legend` 生成区，`kind=10` = `step_compensation_failed`，`kind=11` = `saga_compensation_failed`）。

> **⚠️ 手工改库前提（所有分支通用）**：下列处置涉及直接写 `saga_instances` / `saga_events`，绕过 `MarkTerminal` 的 lease fencing CAS。执行前**必须**：(1) 确认无 Coordinator 仍在驱动该 instance——lease 已过期或经 `Orphan()` 主动释放（否则 `ClaimPending` 会与你的手工写产生竞争）；验证 lease 状态用场景 1 的诊断 SQL（查 `lease_id` / `lease_expires_at`）；(2) 在维护窗口内操作；(3) **不要手工 INSERT 终态 kind**（`kind` ∈ {6,7,8,9,11}）——终态事件只能由 `MarkTerminal` 原子写入并翻转 status 投影，手插会使 event log 折叠结果与 `saga_instances.status` 不一致，破坏 append-only journal 不变式。处置决策优先写 audit log，而非改 `saga_events`。

**决策树**：

1. **失败步骤是幂等外部副作用**（如发 HTTP 请求、写远端系统）：
   - 验证外部系统当前状态，确认副作用是否已生效。
   - 若已生效，视为幂等成功——处置决策（operator / ticket / instance_id）写
     audit log（见下方审计要求）；`saga_events` 可补一条**非终态** `kind=4`
     (step_compensated) 审计记录。status 由 8 改为 6 (compensated) 须经运维
     工具走正常 journal 路径，不直接 UPDATE。
   - 若未生效，重新触发补偿动作后同上标记。

2. **失败步骤是不可逆操作**（如已下发的物理动作、已消费的外部资源）：
   - 评估业务影响范围，判断是否需要人工补偿（线下流程）。
   - **保留 `status=8` 不动**（它已是合规终态，本身就是运维 audit trail）；
     处置决策与 operator_note / ticket 写 audit log，**不**向 `saga_events`
     插入终态 `kind=11`（见上方前提，终态 kind 仅 MarkTerminal 可写）。
   - 通知相关业务方走线下补偿流程。

3. **失败步骤是基础设施故障**（DB 宕机、外部服务不可达）：
   - 等待基础设施自愈（监控 `saga_coordinator_ready` 探针）。
   - 基础设施恢复后，该 saga 实例已是终态（`status=8`），**不会自动重试**。
     自动重试入口尚未实现（无专属 issue，需另开 backlog）；在它落地前，重新
     驱动须经运维工具走正常 journal 路径，谨慎评估幂等性，不直接 UPDATE status。

---

## 场景 3：journal 增长失控

**症状**：`saga_events` / `saga_instances` 表体积持续增长，无界。

**根因**：journal 是 append-only（D2）——每个状态变化一行，刻意不就地更新。终态实例的事件历史保留用于 replay / forensic。

**诊断 SQL**：

```sql
-- 终态实例占比（可归档候选）
SELECT status, count(*) FROM saga_instances GROUP BY status ORDER BY status;

-- 事件表增长热点（按 definition 聚合）
SELECT definition_id, count(*) AS events
FROM saga_events e JOIN saga_instances i ON e.instance_id = i.id
GROUP BY definition_id ORDER BY events DESC;
```

**处置**：

- 单事件已有 `Event.MaxPayloadBytes`（64 KiB）上限，单行不会无界。
- 整表归档 / 截断属 **EPIC #1609 Projection / Replay**（从 `saga_events` replay，ADR `202606051200-1609-adr-saga-journal-projection-source.md`，model-a）的范畴：**replay 投影源底座 PR-02..05 已落地（载体 / `GlobalReader` / `SagaJournalSource` / `Tailer` + bootstrap wiring），真实消费者 PR-06 待落地**；**归档/截断本身仍未实现**。归档能力落地前不要手工 `DELETE` 终态实例的事件（破坏 replay/forensic）；落地后截断亦须 **≥ 最慢投影 checkpoint**（#1609 D7），否则丢未投影事件。
- 短期容量压力：扩 PG 存储 / 调整 retention 策略，不删 saga_events。
- **告警引导**：无专属增长指标；用 PG 表体积监控（`pg_total_relation_size('saga_events')`）设容量阈值告警，或监控 `saga_instances` 中长期非终态行数（`status IN (1,2,3)` 且 `updated_at` 老化）作为驱动停滞的间接信号。

---

## 场景 4：coordinator 推进受阻 / loop 故障（#1109 coordinator 指标）

对应告警 `GoCellSagaInstanceStuckSkipping`、`GoCellSagaLockAcquireFailures`（4a/4b，leader-elect 故障）与 `GoCellSagaTickErrors`（4c，coordinator loop ClaimPending 故障）（`docs/ops/alerting-rules.md`）。4a/4b 仅在 leader-elect 模式（`WithLeaderElect`）下有意义；三条均需接入真实 metrics Provider 后才触发——saga 接入生产 cell（PR-09 / #978）前沉默。

### 4a：stuck skipping（`GoCellSagaInstanceStuckSkipping`）

**症状**：某 `(cell, definition_id)` 的 `saga_leader_elect_skip_total{reason="contended"}` 持续 >0，而同维度 `saga_drive_total{result="ok"}` ≈ 0——这个 coordinator 一直抢不到 per-instance distlock，实例不被本节点推进。告警按 `definition_id` 分组，故单个 definition 卡住不会被同 cell 其他 definition 的正常 drive 掩盖。

**根因（按概率）**：

1. **正常多副本竞争**：另一个 coordinator 正持锁推进该实例——此时它的 `drive{result="ok"}` 在涨，本告警的 `unless` 条件不成立、不应触发。若触发说明**没有任何**副本在推进。
2. **stale distlock**：持锁的 coordinator 崩溃/卡死，但 distlock key 尚未过期（TTL = `Config.LeaseDuration`），其它副本只能 skip 到 key 过期。
3. **distlock 后端分区**：见 4b。

**诊断**：

```sql
-- 该 definition 的非终态实例是否在 ClaimPending 但无 step 进展（updated_at 老化）
SELECT id, status, lease_id, updated_at
FROM saga_instances
WHERE definition_id = '<definition_id>' AND status IN (1,2,3)
ORDER BY updated_at ASC LIMIT 20;
```

- 检查 distlock 后端（Redis）中 `saga:<len>:<def>:<inst>` key 的 TTL；若一个已死副本持有，等其 TTL（≤ `LeaseDuration`）自然过期，或确认该副本进程确实终止后手工删 key。
- 交叉看 `saga_tick_total{cell}`：若 tick 在涨说明 coordinator loop 存活、只是 skip；若 tick 平坦见场景 1（loop/journal 停滞）。

**处置**：确认无存活副本在推进后，等 distlock TTL 过期（最稳）或在确认死副本后删对应 distlock key；不要在不确定持锁方是否存活时强删（会重新打开双驱动窗口——journal lease_id CAS 仍兜底正确性，但应优先让 TTL 自然收敛）。

### 4b：lock-acquire 失败（`GoCellSagaLockAcquireFailures`）

**症状**：`saga_leader_elect_skip_total{reason="backend_error"}` 持续 >0——`distlock.Locker.Acquire` 返回的不是 contention 超时、也不是 ctx 取消，而是后端 I/O 故障。fail-closed：无法确认 leadership 时本节点一律 skip，**所有**实例停止被该 cell 推进。

**根因**：distlock 后端（Redis）不可达 / 认证失败 / 命令被拒。

**处置**：按 Redis 连接故障处置（网络、ACL、连接池、TLS）。恢复后 skip 自动回落、drive 恢复。该路径下 backend error 文本经 `redaction.RedactAny` 脱敏后入 slog（`reason=backend_error`），用 `instance_id` / `lock_key` 关联具体实例。

### 4c：tick 持续 error（`GoCellSagaTickErrors`）

**症状**：`saga_tick_total{result="error"}` 持续 >0——coordinator loop 仍在 tick（goroutine 存活），但每次 `ClaimPending` 返回错误、抢不到工作。区别于 4a/4b（leader-elect skip，loop 健康只是不持锁）与场景 1（tick 平坦 = loop 停滞）：这里 tick 在涨、但全落 error 分支。不依赖 leader-elect 模式——无论单进程还是 leader-elect，ClaimPending 故障都会触发。

**根因**：`ClaimPending` 失败——journal/DB 不可达、migration drift、连接池耗尽、查询超时等 journal 后端故障。错误经 `slog.Warn("saga: tick failed")` 落日志（`runtime/saga/coordinator.go::tickLoop`）。

**诊断**：查 PG/journal 可用性（连接、迁移状态、连接池水位）；对照 server log 的 "saga: tick failed" WARN 行取脱敏后 last_error；`saga_tick_total{result="claimed"|"empty"}` 应同时停止增长（所有 tick 落 error 分支）。

**处置**：恢复 journal 后端连通性 / 修复 migration drift。loop 是 level-triggered，journal 恢复后下一个 tick 自动回到 claimed/empty，无需人工重启 coordinator。

---

## 场景 5：投影 tailer 停滞（#1609 PR-04）

对应告警 `GoCellSagaTailerStalled` / `GoCellSagaTailerLagHigh` / `GoCellSagaTailerDrainErrors` / `GoCellSagaTailerLockAcquireFailures` / `GoCellSagaTailerCheckpointAdvanceFailures`（`docs/ops/alerting-rules.md`）。`runtime/saga/tailer.Tailer` 是 saga 终态 model-A 投影的 catch-up 驱动；与场景 4 的 saga Coordinator 是**独立组件、独立 distlock key**（`saga-journal-tailer:<len>:<cell>:<len>:<proj>`，per-(cellID, projectionID) 粒度——两个 cell 可合法共用同一 projectionID，故 key 必须带 cellID；区别于 Coordinator 的 per-instance `saga:<len>:<def>:<inst>`）。PR-05 已将 Tailer 接入 bootstrap phase6 drain（声明式 `projectionSource: saga-journal`）；告警在部署了 saga-journal 投影的 assembly 上即生效。接入真实消费者（orderfulfillment，PR-06）后可预期首次实际触发。

**症状**：读投影读到陈旧 saga 终态；`last_success_timestamp` 不前进 / `pending_events` 持续增长。

**根因（按概率）**：

1. **无 leader 在跑**：没有 tailer pod 抢到 per-projection distlock（部署缩到 0 副本 / 全部副本崩溃）。
2. **drain 反复失败**：`drain_total{result="head_error"}` 持续 >0——saga journal Head 读取失败（journal / DB 不可达），drain 在 replay 前即中止，`pending_events` 可能停留在上次干净 tick 的旧值，`GoCellSagaTailerLagHigh` 不一定触发；`drain_total{result="store_error"}` 持续 >0——checkpoint `LoadOffset` 失败；`drain_total{result="apply_error"}` 或 `checkpoint_advance_total{result="error"}` 持续 >0——replay/apply 路径或 checkpoint 事务故障。
3. **distlock 后端故障**：`lock_acquire_failed_total{reason="backend_error"}` 持续 >0（Redis 不可达），fail-closed → 无 drain。

**诊断**：

```sql
-- 积压 = HeadSeq − checkpoint。HeadSeq（saga_events 全局序最大值）：
SELECT MAX(global_seq) AS head_seq FROM saga_events;

-- 该投影已提交 checkpoint + 当前 owner（PG owner 列 PR-PG 落地后才有值；
-- 此前 owner 为空，仅看 offset）：
SELECT cell_id, projection_id, offset_seq, owner
FROM projection_checkpoints
WHERE cell_id = '<cell>' AND projection_id = '<projection>';
-- pending = head_seq − offset_seq；持续 > 0 且 offset_seq 不动 = 停滞。
```

- **distlock key 持有方**：在 distlock 后端（Redis）查 `saga-journal-tailer:<len>:<cell>:<len>:<projection>` key 是否存在。
  - key **不存在** 且 checkpoint 不动 → 没有 tailer 在跑（部署问题，根因 1）：检查 tailer pod 副本数 / 启动日志。
  - key **存在** 但 checkpoint 不动 → 持锁 pod 的 drain 在失败：看该 pod 的 `drain_total{result}` / `checkpoint_advance_total{result}` 与 `projection.apply` trace span / DB 健康（根因 2）。
- 交叉看 `lock_acquire_failed_total{reason}`：`backend_error` 持续 >0 = 根因 3（按 Redis 故障处置，同场景 4b）。

**处置**：

- 根因 1：恢复 tailer 副本；leader 抢锁后自动 catch-up 全量 `(checkpoint, Head]`。
- 根因 2：修复 apply/DB 故障；checkpoint advance 与 apply 同事务，故修复后从断点续推，不丢不重（exactly-once within owner）。
- 根因 3：恢复 distlock 后端（同场景 4b）。
- **`stale_owner` 不是故障**：`checkpoint_advance_total{result="stale_owner"}` 在 leader 交接窗口出现是设计预期（旧 leader 被 CAS fence，ADR D5(b)），不需处置；只有 `result="error"` 才是真故障。
- **重复 apply 提示**：leader 交接窗口可能产生有界重复 apply（投影 Apply 幂等兜底，非 exactly-once；完整 monotonic fencing 待 PR-PG PG owner 列）——若读模型出现重复，确认 Apply 实现幂等，不要手工改 checkpoint。

---

## 场景 6：Coordinator / Tailer 优雅关闭超时（`ErrSagaStopTimeout`，#1950）

`Coordinator.Stop` / `Tailer.Stop` 在 bootstrap LIFO Close 期间耗尽传入的 teardown 预算，返回
`errcode` Code = `ERR_SAGA_STOP_TIMEOUT`（Kind `KindDeadlineExceeded`）。两条触发路径：
① 等待 `Start` 进入 running 超时；② drain/loop-exit 超时（多为非协作 `Step.Run` / apply
goroutine 忽略 ctx、超出 drain 预算未退出）。与 lifo_teardown 时长告警关联（`docs/ops/graceful-shutdown-k8s.md`）。

> **不是冲突**：该码刻意区别于 `ERR_CONFLICT` —— 这是 deadline 而非状态冲突，日志/告警按超时分级，
> 不要按资源冲突处置。这些是内部 lifecycle 错误（返回给 bootstrap Close 并被日志记录），不产生 HTTP 响应。

**诊断**：

- 看关闭日志：`saga coordinator stop: timed out [waiting for start]` / `tailer stop: timed out ...` + bootstrap LIFO teardown 日志。
- drain 超时通常是某个 `Step.Run` 长时间不返回（外部 IO 阻塞、未传播 ctx）。在途 step 数量**不暴露为指标**（`inflightLocks` 是 Coordinator 内部状态）；用 `saga_drive_total{cell,definition_id,result}` 定位——卡住的 `Step.Run` 使其 `driveOne` 长时间不计入完成，drive 计数相对 `saga_tick_total{cell,result}`（ClaimPending 周期）停滞即指向被 wedge 的 step。

**处置**：

- 偶发、随后副本正常退出：teardown 预算偏紧，调大 bootstrap tearCtx budget。
- 反复出现：排查阻塞的 `Step.Run`（让其响应 ctx 取消 / 加超时）；关闭超时不会损坏 journal——
  在途 step 的 lease 经 `Orphan()` 释放、`FencedWriter` CAS 兜底，新 leader 按 lease TTL 接管收敛。

---

## 审计要求

场景 2 / 场景 3 的所有人工决策与处置动作必须在 audit log 中留存，最低字段集：

```json
{
  "instance_id": "<id>",
  "definition_id": "<def>",
  "decision": "accepted_irreversible | re_triggered | awaiting_retry",
  "operator": "<email>",
  "ticket": "<jira/linear ticket>",
  "timestamp": "<ISO8601>",
  "notes": "<optional free text>"
}
```

若系统已接入 `auditcore`，通过 `corecells/auditcore` 的 auditappend 切片（`slices/auditappend*`）写入；
否则直接写运维 audit log 系统，保存 ≥ 90 天。
