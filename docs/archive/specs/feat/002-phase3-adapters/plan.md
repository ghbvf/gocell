# Plan — Phase 3: Adapters

> 来源: spec.md (v2, post-decisions), decisions.md, kernel-constraints.md

---

## 架构概览

Phase 3 在 `adapters/` 层新增 6 个包，实现 `kernel/outbox` 和 `kernel/idempotency` 接口，并提供独立的 OIDC/S3/WebSocket 基础设施。同时重构 `runtime/bootstrap` 以支持接口注入，重构 3 个 Cell 的 7 处 Publish 调用改用 outbox.Writer 事务模式。

### 模块依赖图

```
kernel/outbox (interfaces, doc-only change)
kernel/idempotency (interfaces, no change)
    │
    ├── adapters/postgres ← pgx/v5
    │     ├── pool.go, tx_manager.go, migrator.go
    │     ├── outbox_writer.go  (implements outbox.Writer)
    │     ├── outbox_relay.go   (implements outbox.Relay + worker.Worker)
    │     └── helpers.go        (RowScanner, QueryBuilder)
    │
    ├── adapters/redis ← go-redis/v9
    │     ├── client.go
    │     ├── distlock.go
    │     ├── idempotency.go    (implements idempotency.Checker)
    │     └── cache.go
    │
    ├── adapters/rabbitmq ← amqp091-go
    │     ├── connection.go
    │     ├── publisher.go      (implements outbox.Publisher)
    │     ├── subscriber.go     (implements outbox.Subscriber)
    │     └── consumer_base.go  (DLQ + retry + idempotency)
    │
    ├── adapters/oidc
    │     ├── provider.go, token.go, verifier.go, userinfo.go
    │
    ├── adapters/s3
    │     ├── client.go, presigned.go
    │
    └── adapters/websocket ← nhooyr.io/websocket
          ├── hub.go, handler.go
```

### 关键设计决策

1. **事务传播**: context-embedded pattern（TxManager 存 tx 到 ctx，Writer 从 ctx 提取）
2. **Relay → Publisher**: Relay 接受 `outbox.Publisher` kernel 接口，序列化 Entry 为 JSON payload
3. **Bootstrap**: `WithPublisher` + `WithSubscriber` 替代 `WithEventBus` 具体类型
4. **ArchiveStore**: S3 提供通用 ObjectStore，Cell 内部封装为 ArchiveStore
5. **Cell Repository**: Phase 3 仅实现 AuditRepo + ConfigRepo PG 版本（最小 L2 证明）

### 交付波次

- **Wave 0**: Bootstrap 重构 + kernel doc + 安全前置（UUID）
- **Wave 1**: postgres/redis/rabbitmq 基础设施 + Docker Compose
- **Wave 2**: outbox 链路 + oidc/s3/websocket + Cell PG Repo
- **Wave 3**: Cell outbox.Writer 重构 + 安全加固 + tech-debt
- **Wave 4**: 集成测试 + Journey E2E + 文档

---

## 数据模型

### outbox_entries 表

```sql
CREATE TABLE outbox_entries (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    aggregate_id   TEXT NOT NULL,
    aggregate_type TEXT NOT NULL,
    event_type     TEXT NOT NULL,
    payload        JSONB NOT NULL,
    metadata       JSONB DEFAULT '{}',
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    published_at   TIMESTAMPTZ,
    published      BOOLEAN NOT NULL DEFAULT false
);

CREATE INDEX idx_outbox_unpublished ON outbox_entries (created_at)
    WHERE published = false;
```

### schema_migrations 表

```sql
CREATE TABLE schema_migrations (
    version    BIGINT PRIMARY KEY,
    dirty      BOOLEAN NOT NULL DEFAULT false,
    applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
```
