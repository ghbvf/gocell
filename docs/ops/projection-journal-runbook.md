# Projection Journal 运维 Runbook

outbox 派生投影的 durable journal（`projection_events`，EPIC #1504）的故障诊断与处置手册。三个互补载体：

- **本文件** — 运维场景的诊断 SQL、rebuild 操作流程、监控要点、已知限制。
- `docs/ops/readyz.md` §Projection probes — `projection_journal_ready` 探针语义。
- `docs/ops/alerting-rules.md` §Projection — 指标告警规则（待补充）。

设计决策真值源：ADR `docs/architecture/202606071600-1504-adr-projection-event-journal.md`。

---

## Posture：Durable-by-Default

自 PR-04（#1771）起，`GOCELL_PROJECTION_PG_JOURNAL_PREVIEW` gate **已删除**，durable `projection_events` journal 成为生产默认：

- **无 env opt-out**（per 不留软回退宪法）。
- PG 模式且在 `slice.yaml` 中声明了 projection 时，corebundle 自动接 durable source 并注册 `projection_journal_ready` readyz probe。
- 未声明任何 projection 时不接（无空转 probe）。
- Rollback = 重新部署上一版本二进制（见下文 §Rollback）。

### projection_events 表特性

`projection_events` 是 **append-only** 永久 journal（migration 045 建表，migration 058 serving-role 权限收紧）：

- `global_seq BIGINT GENERATED ALWAYS AS IDENTITY`——单调递增位置，从不复用（gaps 合法）。
- DB 引擎强制 append-only：migration 058 对 serving role `gocell_app` 执行 `REVOKE UPDATE, DELETE ON projection_events`（Hard，不可绕）。
- code-level `DELETE`/`TRUNCATE projection_events` 字面量由 archtest `PROJECTION-EVENT-JOURNAL-NO-DELETE-01`（Medium 纵深）守卫。
- 写路径唯一收口：emit 期同事务双写装饰器（`journalingOutboxWriter`，`PROJECTION-EVENT-JOURNAL-APPEND-CALLER-01` Hard/Hard），仅对 `slice.yaml contractUsages` 声明的 projection-source topic 集双写（topic-filter，增长有界 by construction）。
- `projection_checkpoints` 表（migration 044）存每个投影的 offset 与 owner（v1 不写 owner）。

---

## Rebuild 运维

### 触发 Rebuild

Rebuild 是**异步**操作，通过 AdminListener 的框架控制面端点触发：

```
POST /admin/v1/projection/{cell}/{projection}/rebuild
```

以 accesscore / session_registry 投影为例：

```bash
# 使用 operator 凭据（HTTP Basic Auth，GOCELL_OPERATOR_ADMIN_USER / GOCELL_OPERATOR_ADMIN_PASSWORD）
curl -X POST \
  --user "$GOCELL_OPERATOR_ADMIN_USER:$GOCELL_OPERATOR_ADMIN_PASSWORD" \
  "http://127.0.0.1:9093/admin/v1/projection/accesscore/session_registry/rebuild"
```

AdminListener 默认监听 `127.0.0.1:9093`（loopback-isolated，仅本地可达）。

### HTTP 响应语义

| 状态码 | 含义 |
|--------|------|
| `202 Accepted` | Rebuild 已接受，后台异步执行（`Coordinator.Rebuild` 立即返回，状态机在后台运行） |
| `409 Conflict` | 已有 rebuild 在进行中（`ErrRebuildInProgress`），不可重复触发 |
| `404 Not Found` | 未知的 cell 或 projection 名称 |

202 响应体携带当前快照：

```json
{
  "data": {
    "phase": "replay",
    "pendingEvents": 1432,
    "replayLagSeconds": 12.5
  }
}
```

### Rebuild 生命周期

Rebuild 经过 Coordinator 的四阶段状态机（对齐 Axon TrackingEventProcessor）：

1. **PhaseStopped** — 暂停 live 投递，准备重置。
2. **PhaseReset** — 调用 `onReset` hook（业务清空读模型），checkpoint 清零。
3. **PhaseReplay** — 从 `projection_events` 全量重放（`global_seq > 0`），topic-filtered，逐行经 `Apply` 写入读模型，同事务 advance checkpoint。
4. **PhaseCatchup** — 追赶 `(head0, head1]` 区间（replay 期间新增的事件），追完后恢复 live 投递（PhaseLive）。

Rebuild 期间读模型处于 stale 状态（非 503，业务 read 仍可服务旧数据，参见 Q4-supplement）。`Coordinator.Phase()` 暴露当前阶段；业务 read path 可按需检查并返回 503。

### 诊断 Rebuild 进度

```sql
-- 查当前 checkpoint offset（0 = 还未推进 / reset 后）
SELECT cell_id, projection_id, offset_seq, updated_at
FROM projection_checkpoints
WHERE cell_id = 'accesscore' AND projection_id = 'session_registry';

-- 查 journal 总行数（rebuild 需重放的总量）
SELECT COUNT(*) AS total_events FROM projection_events;

-- 查最大 global_seq（Head，用于估算 replay 进度）
SELECT MAX(global_seq) AS head FROM projection_events;

-- 查 session_registry 相关 topic 的事件量（topic-filter 限定范围）
SELECT COUNT(*) FROM projection_events WHERE topic = 'event.session.created.v1';
```

---

## 监控

### projection_event_replay_lag_seconds

**语义**：当前 checkpoint offset 与 journal head（`MAX(global_seq)`）之间的差值，经估算转换为秒数。

**解读注意（bootstrap-gap 盲区）**：

- lag 降至 ~0 仅表示**读完了 journal 内的全部事件**，**不代表历史数据完整**。
- `projection_events` 仅从 PR-02（双写装饰器部署时刻）起 append——该时刻之前产生的 outbox 事件不在 journal 内（bootstrap gap，同 Debezium start-from-now 结构性形态，非 bug）。
- 数据完整性须经 `projection_checkpoints.offset_seq` 对比业务侧基准校验，**不能仅看 lag**。

### projection_events 增长告警

`projection_events` 是 append-only，无归档机制（D8 out-of-scope，topic-filter 已限制增长至 projection-relevant 事件）。运营须监控：

```sql
-- 表行数
SELECT COUNT(*) FROM projection_events;

-- 表物理大小（Bytes）
SELECT pg_relation_size('projection_events');

-- 含 TOAST、索引的总大小
SELECT pg_total_relation_size('projection_events');
```

建议在持续增长超预期阈值时接入告警。未来归档须满足：`归档截断 global_seq ≥ MIN(projection_checkpoints.offset_seq)`（D8 下界约束），否则丢失可重建历史。当前归档能力 out-of-scope。

---

## Rollback

自 PR-04（#1771）删 gate 后，**不提供 env opt-out**（per 不留软回退宪法）。

Rollback 路径 = **重新部署上一版本二进制**：

1. 将 corebundle 镜像回滚到 PR-03（#1770）或更早版本（包含 `GOCELL_PROJECTION_PG_JOURNAL_PREVIEW` gate）。
2. 若需禁用 durable projection，设置 `GOCELL_PROJECTION_PG_JOURNAL_PREVIEW=` 空（旧版本默认 gate-off）。
3. `projection_events` 表数据保留（append-only，不会因回滚被删），再次部署新版本后可继续从最新 checkpoint 恢复。

---

## 已知 v1 Limitation

### Bootstrap Gap

`projection_events` 仅从 PR-02（双写装饰器首次部署时刻）起 append。部署前产生的历史 outbox 事件不在 journal 内。

**影响**：v1 full rebuild 仅能重放部署后的事件。首个真实投影（accesscore `session_registry`，`event.session.created.v1`）从 journal 起点 rebuild，生产投影之前处于 hard-gated OFF 状态，无遗留状态待迁移，实际影响为零。

若未来需覆盖完整历史，须部署前经 broker replay 或 snapshot 补全（Q4 snapshot #1100，YAGNI）。

### 单 Pod 边界

v1 投影运行在 **单 pod** 边界（继承 #1100 Q5）：

- `projection_checkpoints.owner` 列保留但 v1 不写。
- 双 pod 并发 rebuild 会 double-apply（无 CAS fencing）。
- 多 pod fencing CAS（`AdvanceIfOwner`）推迟到 PR-PG，待真实多 pod 消费者出现。
- 生产运维须保证单 pod 消费同一 projection；`ConsumerBase` 串行只序列化同 pod live 路径，不覆盖跨 pod rebuild。
