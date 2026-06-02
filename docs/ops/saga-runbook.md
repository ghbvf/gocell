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
- 整表归档 / 截断是 **W10 Projection / Replay**（从 `saga_events` replay 任意时点状态）的范畴，独立 wave，当前未实现——在它落地前不要手工 `DELETE` 终态实例的事件（会破坏 replay/forensic 能力）。
- 短期容量压力：扩 PG 存储 / 调整 retention 策略，不删 saga_events。
- **告警引导**：无专属增长指标；用 PG 表体积监控（`pg_total_relation_size('saga_events')`）设容量阈值告警，或监控 `saga_instances` 中长期非终态行数（`status IN (1,2,3)` 且 `updated_at` 老化）作为驱动停滞的间接信号。

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

若系统已接入 `auditcore`，通过 `cells/auditcore/slices/auditwrite` 写入；
否则直接写运维 audit log 系统，保存 ≥ 90 天。
