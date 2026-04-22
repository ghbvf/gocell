# Outbox Relay 三阶段重写计划

> 日期: 2026-04-07
> 状态: 设计完成，待实施
> 前置: PR#46 已合并（R-01 fail-fast + R-02 startedCh 修复）

---

## 1. 动机

当前 relay 在单个长事务内完成 claim + publish + mark：

```
BEGIN TX
  SELECT ... FOR UPDATE SKIP LOCKED     ← claim
  for each entry:
    publish(entry)                      ← 网络 IO 在事务内
    UPDATE SET published = true         ← mark
COMMIT
```

**问题**：
- 事务持有行锁期间做网络 IO，publish 慢时锁持续 N 秒
- 无重试计数，失败条目下次 poll 立即重试（无退避）
- 无 DLQ，永久失败条目反复重试
- 无可观测指标

**目标语义**：at-least-once，靠消费端幂等兜底（已有 `idempotency.Checker`）。

## 2. 标准模式：三阶段

```
Phase 1: Claim（短事务）
  BEGIN TX
    SELECT ... FOR UPDATE SKIP LOCKED
    UPDATE SET status = 'claiming', claimed_at = now()
  COMMIT                                ← 锁立即释放

Phase 2: Publish（事务外，逐条）
  for each claimed entry:
    err := publish(entry)

Phase 3: 回写结果（短事务）
  成功 → status = 'published', published_at = now()
  失败 → status = 'pending', attempts++, next_retry_at = now() + backoff(attempts)
  attempts >= max → status = 'dead', 记指标
```

**参考**：
- Watermill SQL forwarder（claim + forward + mark 分离）
- Uber fx lifecycle（enum 状态机）
- nikolayk812/pgx-outbox（fail-fast on mark）

## 3. Schema 变更

```sql
-- migrations/003_outbox_status_columns.sql
-- +goose Up
ALTER TABLE outbox_entries
  ADD COLUMN status       TEXT        NOT NULL DEFAULT 'pending',
  ADD COLUMN attempts     INT         NOT NULL DEFAULT 0,
  ADD COLUMN next_retry_at TIMESTAMPTZ,
  ADD COLUMN claimed_at   TIMESTAMPTZ;

UPDATE outbox_entries SET status = 'published' WHERE published = true;

DROP INDEX IF EXISTS idx_outbox_unpublished;
CREATE INDEX idx_outbox_pending ON outbox_entries (next_retry_at NULLS FIRST, created_at)
  WHERE status = 'pending';

-- +goose Down
DROP INDEX IF EXISTS idx_outbox_pending;
CREATE INDEX idx_outbox_unpublished ON outbox_entries (created_at) WHERE published = false;
ALTER TABLE outbox_entries
  DROP COLUMN status,
  DROP COLUMN attempts,
  DROP COLUMN next_retry_at,
  DROP COLUMN claimed_at;
```

`published` 列保留（向后兼容），后续 migration 删除。

## 4. 状态机

```
pending ──Start()──→ claiming ──publish──→ published
   ↑                    │                      │
   │                    │ (fail, attempts<max)  │ (cleanup)
   └────────────────────┘                      ↓
                        │                   deleted
                        │ (fail, attempts>=max)
                        ↓
                       dead
```

| 状态 | 含义 |
|------|------|
| `pending` | 待发布（含首次和重试） |
| `claiming` | 被 relay 实例锁定，正在发布 |
| `published` | 已成功投递到 broker |
| `dead` | 超过最大重试次数，需人工介入 |

## 5. RelayConfig 变更

```go
type RelayConfig struct {
    PollInterval    time.Duration // default 1s
    BatchSize       int           // default 100
    RetentionPeriod time.Duration // default 72h
    MaxAttempts     int           // default 5（新增）
    BaseRetryDelay  time.Duration // default 5s（新增）, backoff: base * 2^attempts
    ClaimTTL        time.Duration // default 60s（新增）, 超时自动回到 pending
}
```

## 6. 核心实现

### 6.1 pollOnce 三阶段

```go
func (r *OutboxRelay) pollOnce(ctx context.Context) error {
    // Phase 1: Claim（短事务）
    entries, err := r.claim(ctx)
    if err != nil || len(entries) == 0 {
        return err
    }

    // Phase 2: Publish（事务外）
    results := r.publishAll(ctx, entries)

    // Phase 3: 回写（短事务）
    return r.writeBack(ctx, results)
}
```

### 6.2 claim

```sql
UPDATE outbox_entries
  SET status = 'claiming', claimed_at = now()
WHERE id IN (
  SELECT id FROM outbox_entries
  WHERE status = 'pending'
    AND (next_retry_at IS NULL OR next_retry_at <= now())
  ORDER BY created_at
  LIMIT $1
  FOR UPDATE SKIP LOCKED
)
RETURNING id, aggregate_id, aggregate_type, event_type,
          topic, payload, metadata, created_at, attempts;
```

### 6.3 publishAll（无事务）

```go
type publishResult struct {
    entry outbox.Entry
    err   error
}

func (r *OutboxRelay) publishAll(ctx context.Context, entries []outbox.Entry) []publishResult {
    results := make([]publishResult, len(entries))
    for i, e := range entries {
        payload, marshalErr := json.Marshal(e)
        if marshalErr != nil {
            results[i] = publishResult{entry: e, err: marshalErr}
            continue
        }
        results[i] = publishResult{
            entry: e,
            err:   r.pub.Publish(ctx, e.RoutingTopic(), payload),
        }
    }
    return results
}
```

### 6.4 writeBack

```go
func (r *OutboxRelay) writeBack(ctx context.Context, results []publishResult) error {
    tx, err := r.db.Begin(ctx)
    if err != nil {
        return errcode.Wrap(ErrAdapterPGConnect, "writeBack: begin tx", err)
    }
    committed := false
    defer func() {
        if !committed {
            _ = tx.Rollback(context.WithoutCancel(ctx))
        }
    }()

    for _, res := range results {
        if res.err == nil {
            _, err = tx.Exec(ctx,
                `UPDATE outbox_entries SET status = 'published', published_at = now() WHERE id = $1`,
                res.entry.ID)
        } else {
            newAttempts := res.entry.Attempts + 1
            if newAttempts >= r.config.MaxAttempts {
                _, err = tx.Exec(ctx,
                    `UPDATE outbox_entries SET status = 'dead', attempts = $1 WHERE id = $2`,
                    newAttempts, res.entry.ID)
                slog.Error("outbox relay: entry dead-lettered",
                    slog.String("entry_id", res.entry.ID),
                    slog.Int("attempts", newAttempts),
                    slog.Any("last_error", res.err))
            } else {
                delay := r.config.BaseRetryDelay * (1 << newAttempts)
                _, err = tx.Exec(ctx,
                    `UPDATE outbox_entries SET status = 'pending', attempts = $1, next_retry_at = now() + $2::interval WHERE id = $3`,
                    newAttempts, delay, res.entry.ID)
            }
        }
        if err != nil {
            return errcode.Wrap(ErrAdapterPGQuery, "writeBack: update entry", err)
        }
    }

    if err := tx.Commit(ctx); err != nil {
        return errcode.Wrap(ErrAdapterPGConnect, "writeBack: commit", err)
    }
    committed = true
    return nil
}
```

### 6.5 Claim 超时恢复

```go
func (r *OutboxRelay) reclaimStale(ctx context.Context) error {
    const q = `UPDATE outbox_entries
        SET status = 'pending', claimed_at = NULL
        WHERE status = 'claiming' AND claimed_at < now() - $1::interval`
    _, err := r.db.Exec(ctx, q, r.config.ClaimTTL)
    return err
}
```

加入 `cleanupLoop`，与 `deletePublishedBefore` 同频执行。

### 6.6 OutboxWriter 适配

`Write()` 当前写 `published = false`。新增显式写 `status = 'pending'`（依赖 DEFAULT 也可以，但显式更安全）。

### 6.7 Entry 结构扩展

`outbox.Entry` 需新增 `Attempts int` 字段供 relay 读取，writeBack 使用。

## 7. 可观测性

在 `pollOnce` 末尾通过 slog 记录：

```go
slog.Info("outbox relay: poll complete",
    slog.Int("published", publishedCount),
    slog.Int("retried", retriedCount),
    slog.Int("dead_lettered", deadCount),
    slog.Duration("claim_ms", claimDuration),
    slog.Duration("publish_ms", publishDuration))
```

后续可接入 `metrics.Collector`：
- `relay_published_total`
- `relay_retried_total`
- `relay_dead_lettered_total`
- `relay_claim_duration_seconds`
- `relay_publish_duration_seconds`

## 8. 生命周期改进（附带）

重写时顺便将 relay 状态机升级为 enum，解决当前 `go relay.Start(); relay.Stop()` 的最早窗口问题：

```go
type relayState int32
const (
    relayStopped relayState = iota
    relayStarting
    relayRunning
    relayStopping
)
```

Stop 在 `relayStopped` 状态下直接返回 nil；在 `relayStarting` 状态下 spin-wait 直到转为 `relayRunning`。

## 9. 测试计划

| 测试 | 覆盖场景 |
|------|---------|
| `TestRelay_ThreePhase_Success` | claim → publish all → mark published |
| `TestRelay_ThreePhase_PartialFailure` | 3 条中 1 条 publish 失败 → 2 published + 1 pending with attempts=1 |
| `TestRelay_ExponentialBackoff` | 验证 next_retry_at = base * 2^attempts |
| `TestRelay_MaxAttempts_DeadLetter` | attempts >= max → status = 'dead' |
| `TestRelay_ReclaimStale` | claiming 超过 ClaimTTL → 回到 pending |
| `TestRelay_ConcurrentRelays` | 两个 relay 实例不会 claim 同一批条目（SKIP LOCKED） |
| `TestRelay_StopBeforeStart` | 保留现有回归测试 |
| `TestRelay_Migration003` | 新旧 schema 兼容 |

## 10. 执行计划

| 步骤 | 内容 | 预估 |
|------|------|------|
| 1 | migration `003_outbox_status_columns.sql` | 0.5h |
| 2 | `RelayConfig` + defaults | 0.5h |
| 3 | 重写 `pollOnce` 三阶段 + `reclaimStale` | 2h |
| 4 | `OutboxWriter.Write` 显式写 `status` | 0.5h |
| 5 | `outbox.Entry` 加 `Attempts` 字段 | 0.5h |
| 6 | relay 状态机 enum（替换 bool running + startedCh） | 1h |
| 7 | slog 指标 | 0.5h |
| 8 | 测试覆盖（8 个场景） | 2h |
| **合计** | | **~1.5d** |

## 11. 与其他 PR 的关系

- **PR#46** ✅ 已合并 — R-01 fail-fast + R-02 startedCh 修复，本次重写将替换这些实现
- **PR-A3** (Solution B) — 无依赖，可并行
- **PR-B** (RabbitMQ 重写) — 无依赖，可并行
- **0-D S-01~S-06** — ConsumerBase 重写在消费端，与 relay 在发布端，互不影响
