# Projection Journal 运维 Runbook

outbox 派生投影的 durable journal（`projection_events`，EPIC #1504）的故障诊断与处置手册。三个互补载体：

- **本文件** — 运维场景的诊断 SQL、rebuild 操作流程、监控要点、已知限制。
- `docs/ops/readyz.md` §Projection probes — `projection_journal_ready` 探针语义。
- `docs/ops/alerting-rules.md` §Projection — 指标告警规则（待补充）。

设计决策真值源：ADR `docs/architecture/202606071600-1504-adr-projection-event-journal.md`。

---

## Posture：Durable-by-Default

自 PR-04（#1771）起，`GOCELL_PROJECTION_PG_JOURNAL_PREVIEW` gate **已删除**，durable `projection_events` journal 成为生产默认：

- **无 env opt-out**（per 不留软回退宪法）：此处"无 env opt-out"专指不再保留 `GOCELL_PROJECTION_PG_JOURNAL_PREVIEW` 式环境开关来禁用 durable journal。回退路径仍可通过**重新部署旧版本二进制**实现（见下文 §Rollback）；两者不矛盾。
- PG 模式且在 `slice.yaml` 中声明了 projection 时，corebundle 自动接 durable source 并注册 `projection_journal_ready` readyz probe。
- 未声明任何 projection 时不接（无空转 probe）。
- Rollback = 重新部署上一版本二进制（见下文 §Rollback）。

> **多租户注意**：super-admin 持 TenantID 调用 `registry-summary` 等端点，仅得其**自身租户**的计数，不返回跨租户聚合结果。跨租户隔离由 principal 派生的 `RowScope` 在数据层独立执行，与路由门禁正交。

### projection_events 表特性

`projection_events` 是 **append-only** 永久 journal（migration 058 建表 + serving-role 权限收紧；`projection_checkpoints` 见 migration 045）：

- `global_seq BIGINT GENERATED ALWAYS AS IDENTITY`——单调递增位置，从不复用（gaps 合法）。
- DB 引擎强制 append-only：migration 058 对 serving role `gocell_app` 执行 `REVOKE UPDATE, DELETE ON projection_events`（Hard，不可绕）。
- code-level `DELETE`/`TRUNCATE projection_events` 字面量由 archtest `PROJECTION-EVENT-JOURNAL-NO-DELETE-01`（Medium 纵深）守卫。
- 写路径唯一收口：emit 期同事务双写装饰器（`journalingOutboxWriter`，`PROJECTION-EVENT-JOURNAL-APPEND-CALLER-01` Hard/Hard），仅对 `slice.yaml contractUsages` 声明的 projection-source topic 集双写（topic-filter，增长有界 by construction）。
- `projection_checkpoints` 表（migration 045）存每个投影的 offset 与 owner（v1 不写 owner）。

---

## Rebuild 运维

### 触发 Rebuild

Rebuild 是**异步**操作，由框架提供的控制面端点触发：

```
POST /admin/v1/projection/{cell}/{name}/rebuild
```

> **仅限已挂载 AdminListener 的部署（corebundle 暂不支持，见 #2209）**
>
> 该端点是 framework-owned（`bootstrap.WithProjectionRebuildEndpoint`），挂在 `cell.AdminListener` 上、用 operator 凭据（`AuthOperator`）鉴权。**corebundle 本版（#1771）只声明 Primary / Internal / Health 三个 listener，未加 AdminListener**，故未启用该端点——在 corebundle 接 AdminListener + operator auth + rebuild 端点是独立 follow-up（#2209）。在 corebundle 部署上执行下面的 curl 会返回 404，请勿在生产 corebundle 实例上执行。
>
> `examples/todoorder` 演示了条件化接线（env 有 operator 凭据时才挂，见 `examples/todoorder/run.go` + `auth.go`）。rebuild 正确性（cold-start / rebuild-from-0 / cleaned-outbox 独立性 / 幂等）由 `adapters/postgres/projection_rebuild_e2e_integration_test.go` 白盒集成测试证明（T-06-2）。

以下操作步骤**仅适用于已挂载 AdminListener 的部署**（如 todoorder 范式）。以 accesscore / session_registry 投影为例：

```bash
# 使用 operator 凭据（HTTP Basic Auth，GOCELL_OPERATOR_ADMIN_USERNAME / GOCELL_OPERATOR_ADMIN_PASSWORD）
curl -X POST \
  --user "$GOCELL_OPERATOR_ADMIN_USERNAME:$GOCELL_OPERATOR_ADMIN_PASSWORD" \
  "http://<admin-listener-addr>/admin/v1/projection/accesscore/session_registry/rebuild"
```

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

Rebuild 期间业务 read 的语义取决于读模型是否持久化。`session_registry`（及任何进程内读模型）在 **PhaseReset** 经 `onReset` hook 清空整张读模型表，因此 reset 后到 replay 追平之前，query **返回空 / 低估值（如 `totalSessions: 0`），而非旧数据**——不要按「stale 但仍服务旧值」排障。`Coordinator.Phase()` 暴露当前阶段：若暴露不完整读模型不可接受，业务 read path 应据此在 rebuild 期间显式返回 503。要让 rebuild 期间仍服务旧数据，需持久化读模型或双 buffer rebuild（per-投影产品决策；`session_registry` 当前不持久化，故 rebuild 期返回空/低估值）。

### 诊断 Rebuild 进度

**运维注意（rebuild 期 lag 盲区）**：rebuild 过程中，`projection_event_replay_lag_seconds` **不反映** rebuild 整体进度。原因：foreign-stream 条目（非本投影 topic 的事件）仅推进 checkpoint offset，**不更新** lag gauge（见 `kernel/projection/rebuild.go` `advanceOffsetPastForeign` 注释）。在 journal 中含有大量 foreign-stream 条目时，lag 可能在相当长的 rebuild 阶段内保持静止，即便 checkpoint 实际在推进。**判断 rebuild 进度应优先看 `projection_pending_events`（= head − checkpoint，未应用条数）+ `projection_checkpoints.offset_seq`**，而非 lag 指标。

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
-- topic 名以 cmd/corebundle/modules_gen.go 的 generatedProjectionSourceTopics()（cellgen golden）为准，勿手工推测
SELECT COUNT(*) FROM projection_events WHERE topic = 'event.session.created.v1';
```

---

## 监控

### projection_event_replay_lag_seconds

**语义**：**当前墙钟时间与最后一次成功 Apply 的事件 `OccurredAt` 之差（秒）**，即事件域的时间陈旧度。实现为 `c.clk.Since(entry.OccurredAt()).Seconds()`（`framework/kernel/projection/coordinator.go`）。

> lag 反映的是「最后被投影成功处理的事件距今多久发生」，是**事件域时间陈旧度**指标，**不是** journal head 与 checkpoint 之间的 offset 差值。

与 `projection_pending_events` 的区分（**告警配置时勿混淆**）：

| 指标 | 语义 | 典型用途 |
|------|------|---------|
| `projection_event_replay_lag_seconds` | 墙钟时间 − `lastApplied.OccurredAt()`，时间陈旧度（秒） | 检测投影是否停止推进（消费者挂起、事件停发等） |
| `projection_pending_events` | `Head − checkpoint`，未应用事件条数 | 检测 backlog 积压量、rebuild 进度 |

两者正交，lag 高可以是生产写入速率低（无新事件），pending 高说明积压未消化。配置告警时分别独立评估，不用其中一个替代另一个。

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

自 PR-04（#1771）删 gate 后，**不提供 env opt-out**（per 不留软回退宪法；见 §Posture 说明）。

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
