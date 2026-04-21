# adapters/ 层规则

adapters/ 实现 `kernel/` 或 `runtime/` 定义的接口，对接外部系统（postgres / redis / rabbitmq / s3 / oidc / vault 等）。

## 依赖约束

**允许**：`kernel/`、`runtime/`（实现其接口）、`pkg/`
**严禁**：`cells/`

接口定义必须在 `kernel/` 或 `runtime/`，adapters/ 只提供实现，不定义接口。

## 集成测试

- 必须使用 **testcontainers + 真实依赖**，禁止 mock DB / mock broker
- 集成测试文件用 `//go:build integration` tag 隔离
- 加密/签名/鉴权优先复用 `runtime/crypto/` 或 `kernel/crypto/` 封装，禁止自建

## postgres

- migration 必须有 **up/down 对**，禁止修改已有 migration 文件
- 新字段必须有默认值或允许 NULL
- 大表索引使用 `CREATE INDEX CONCURRENTLY`
- 文件命名：`{序号}_{动词}_{对象}.sql`（例：`0003_add_session_expires_at.sql`）

## rabbitmq

每个新 consumer 在代码注释中声明：

```go
// Consumer: cg-{service}-{event-type}
// Idempotency: Claimer (two-phase Claim/Commit/Release), TTL 24h
// Disposition: Ack on success / Requeue on transient / Reject on permanent
// DLX: broker-native via DispositionReject → Nack(requeue=false)
```

L2 consumer 必须配置 `SubscriberConfig.DLXExchange`。

## 通用约束

- 构造函数出口保证所有字段非 nil——可选依赖在构造函数内 fallback，禁止 nil 传播到方法调用处
- 空实现 / no-op / fallback 必须写明业务原因（注释说明）
