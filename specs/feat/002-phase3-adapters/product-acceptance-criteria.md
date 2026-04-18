# Product Acceptance Criteria -- Phase 3: Adapters

> 来源: spec.md v2 + tasks.md + product-context.md + decisions.md
> 日期: 2026-04-05
> 角色: 产品经理

---

## 优先级定义

| 优先级 | 定义 | 通过要求 |
|--------|------|---------|
| P1 | Phase 3 Gate 直接相关: 6 adapter 功能 + outbox 全链路 + 安全加固 | 100% PASS，不允许 FAIL 或 SKIP |
| P2 | 提升体验但非 Gate: tech-debt 偿还、产品修复、DX 改进 | 允许 SKIP 附理由，不允许 FAIL |
| P3 | 支撑性: 文档、DevOps 配置、工具链 | 允许 SKIP |

---

## 成功标准覆盖索引

| 成功标准 | 描述 | 覆盖 AC |
|----------|------|---------|
| S1 | 6 adapter 全部实现并通过集成测试 | AC-1.1 ~ AC-6.4, AC-8.1 |
| S2 | outbox 全链路端到端验证 | AC-8.2 |
| S3 | Phase 2 Soft Gate Journey 真实验证 | AC-8.4 |
| S4 | adapters/ 层覆盖率达标 | AC-14.1 |
| S5 | 零分层违反 | AC-NFR-1.1, AC-NFR-1.2 |
| S6 | Phase 2 安全类 tech-debt 清零 | AC-9.1 ~ AC-9.8 |
| S7 | Phase 2 tech-debt 系统性处理 | AC-10.8 |
| S8 | Docker Compose 一键启动 | AC-7.1, AC-7.2 |
| S9 | 外部依赖可控 | AC-13.3 |
| S10 | kernel/ 层零退化 | AC-14.4 |
| S11 | RabbitMQ DLQ 可观测 | AC-5.5 |
| S12 | adapter godoc 完整 | AC-12.1 |

---

## FR-1: PostgreSQL Adapter

### AC-1.1 -- 连接池 [P1]

- **来源**: FR-1.1 / S1
- **任务映射**: T10
- **验证方式**: [集成测试]
- **验收条件**:
  - Given PostgreSQL 实例可用且 DSN 配置正确; When `postgres.NewPool(cfg)` 被调用; Then 返回 `Pool` 实例，`Pool.Health()` 返回 nil
  - Given PostgreSQL 实例不可达; When `Pool.Health()` 被调用; Then 返回非 nil error，错误码前缀为 `ERR_ADAPTER_PG_`
  - Given Pool 配置项（pool max conns = 10, idle timeout = 5m, max conn lifetime = 1h）; When 使用默认配置构造; Then 三项参数匹配默认值

### AC-1.2 -- TxManager [P1]

- **来源**: FR-1.2 / S2
- **任务映射**: T11
- **验证方式**: [集成测试]
- **验收条件**:
  - Given 已连接的 Pool; When `TxManager.RunInTx(ctx, func)` 内 func 返回 nil; Then 事务提交，func 内的写入持久化
  - Given RunInTx 执行中; When func 返回 error; Then 事务回滚，数据库无变更
  - Given RunInTx 执行中; When func 内 panic; Then 事务回滚，panic 不泄漏到调用者（recover 后返回 error）
  - Given RunInTx 嵌套调用; When 内层调用 RunInTx; Then 内层使用 savepoint 而非新事务

### AC-1.3 -- Migrator [P1]

- **来源**: FR-1.3
- **任务映射**: T12
- **验证方式**: [集成测试]
- **验收条件**:
  - Given embed.FS 包含 migration 文件; When `Migrator.Up()` 被调用; Then `schema_migrations` 表记录版本号，DDL 语句生效
  - Given 已执行到 version N; When `Migrator.Status()` 被调用; Then 返回当前版本 N 和 pending 列表
  - Given 已执行到 version N; When `Migrator.Down()` 被调用; Then 回滚到 version N-1

### AC-1.4 -- Outbox Writer [P1]

- **来源**: FR-1.4 / S2 / 决策 1
- **任务映射**: T27
- **验证方式**: [集成测试]
- **验收条件**:
  - Given `TxManager.RunInTx` 活跃事务; When `OutboxWriter.Write(ctx, entry)` 被调用; Then entry 写入 `outbox_entries` 表，与业务写入在同一事务内
  - Given context 无嵌入 tx; When `OutboxWriter.Write(ctx, entry)` 被调用; Then fail-fast 返回 `ERR_ADAPTER_NO_TX`
  - Given `var _ outbox.Writer = (*PostgresOutboxWriter)(nil)`; When 编译; Then 通过（接口合规）

### AC-1.5 -- Outbox Relay [P1]

- **来源**: FR-1.5 / S2 / 决策 9 / KS-07
- **任务映射**: T28
- **验证方式**: [集成测试]
- **验收条件**:
  - Given outbox_entries 表有未发布条目; When Relay 轮询; Then 条目被序列化为 JSON payload 调用 `outbox.Publisher.Publish`，条目标记为已发布
  - Given 多个 Relay 实例并发运行; When 轮询同批条目; Then `SELECT ... FOR UPDATE SKIP LOCKED` 保证无重复发布
  - Given 默认配置; When Relay 运行; Then poll interval = 1s, batch size = 100
  - Given 已发布条目超过 72h; When 清理周期到达; Then 条目被删除
  - Given `var _ outbox.Relay = (*PostgresOutboxRelay)(nil)` 且 `var _ worker.Worker = (*PostgresOutboxRelay)(nil)`; When 编译; Then 通过

### AC-1.6 -- Repository 基础设施 [P2]

- **来源**: FR-1.6
- **任务映射**: T14
- **验证方式**: [单元测试]
- **验收条件**:
  - Given adapters/postgres 包; When Cell 开发者实现 Repository; Then 可使用 `RowScanner` 减少样板代码
  - Given pkg/query 包; When Cell 开发者构建参数化 SQL; Then 可使用 `query.Builder` 减少样板代码
  - Given godoc 文档; When 开发者查看; Then RowScanner 和 query.Builder 用法有注释说明

---

## FR-2: Redis Adapter

### AC-2.1 -- 连接 [P1]

- **来源**: FR-2.1 / S1
- **任务映射**: T16
- **验证方式**: [集成测试]
- **验收条件**:
  - Given Redis 实例可用; When `redis.NewClient(cfg)` 被调用; Then 返回 Client 实例，`Client.Health()` 返回 nil
  - Given Redis 实例不可达; When `Client.Health()` 被调用; Then 返回非 nil error，错误码前缀为 `ERR_ADAPTER_REDIS_`
  - Given standalone 或 sentinel 配置; When 构造 Client; Then 连接模式匹配配置

### AC-2.2 -- 分布式锁 [P1]

- **来源**: FR-2.2
- **任务映射**: T17
- **验证方式**: [集成测试]
- **验收条件**:
  - Given 锁未被持有; When `DistLock.Acquire(ctx, key, ttl)` 被调用; Then 返回 Lock，锁在 Redis 中设置 TTL
  - Given 锁已被持有; When 另一个调用者 Acquire 同 key; Then 返回 error（`ERR_DISTLOCK_TIMEOUT`，或 Redis I/O 失败时 `ERR_DISTLOCK_ACQUIRE`）
  - Given 持有 Lock; When `Lock.Release(ctx)` 被调用; Then Redis 中锁被删除
  - Given TTL = 30s（默认）; When 锁到期; Then 锁自动释放

### AC-2.3 -- Idempotency Checker [P1]

- **来源**: FR-2.3 / S2
- **任务映射**: T18
- **验证方式**: [集成测试]
- **验收条件**:
  - Given key 未处理; When `IsProcessed(ctx, key)` 被调用; Then 返回 false
  - Given key 已 MarkProcessed; When `IsProcessed(ctx, key)` 被调用; Then 返回 true
  - Given MarkProcessed 设置 TTL 24h; When 24h 后; Then key 自动过期，IsProcessed 返回 false
  - Given `var _ idempotency.Checker = (*RedisIdempotencyChecker)(nil)`; When 编译; Then 通过

### AC-2.4 -- Cache [P1]

- **来源**: FR-2.4
- **任务映射**: T19
- **验证方式**: [集成测试]
- **验收条件**:
  - Given Cache 实例; When `Set(ctx, key, value, ttl)` 后 `Get(ctx, key)` 被调用; Then 返回相同 value
  - Given TTL 过期; When `Get(ctx, key)` 被调用; Then 返回 cache miss
  - Given 已缓存 key; When `Delete(ctx, key)` 被调用; Then 后续 Get 返回 miss

---

## FR-3: OIDC Adapter

### AC-3.1 -- Provider Client [P1]

- **来源**: FR-3.1 / S1
- **任务映射**: T30
- **验证方式**: [单元测试] [集成测试]
- **验收条件**:
  - Given OIDC provider URL; When `NewProvider(ctx, issuerURL)` 被调用; Then 从 `/.well-known/openid-configuration` 拉取并缓存 metadata
  - Given provider metadata 已缓存; When 再次请求; Then 使用缓存不重复 HTTP 请求

### AC-3.2 -- Token Exchange [P1]

- **来源**: FR-3.2
- **任务映射**: T31
- **验证方式**: [单元测试]
- **验收条件**:
  - Given 有效 authorization code; When `ExchangeCode(ctx, code, redirectURI)` 被调用; Then 返回 `TokenResponse`（含 access_token, id_token, refresh_token）
  - Given 无效 code; When ExchangeCode; Then 返回 `ERR_ADAPTER_OIDC_TOKEN_EXCHANGE`

### AC-3.3 -- JWKS 验证 [P1]

- **来源**: FR-3.3
- **任务映射**: T32
- **验证方式**: [单元测试]
- **验收条件**:
  - Given RS256 签名的 ID Token 且 JWKS 包含对应 kid; When `Verifier.Verify(ctx, token)` 被调用; Then 验证通过
  - Given kid 不在当前 JWKS 缓存; When Verify; Then 自动刷新 JWKS（kid rotation 支持）
  - Given token exp 已过期; When Verify; Then 返回错误

### AC-3.4 -- UserInfo [P1]

- **来源**: FR-3.4
- **任务映射**: T33
- **验证方式**: [单元测试]
- **验收条件**:
  - Given 有效 access_token; When `UserInfo(ctx, accessToken)` 被调用; Then 返回 UserInfoResponse
  - Given 无效 token; When UserInfo; Then 返回 error

---

## FR-4: S3 Adapter

### AC-4.1 -- Client [P1]

- **来源**: FR-4.1 / S1 / 决策 2
- **任务映射**: T35
- **验证方式**: [集成测试]
- **验收条件**:
  - Given MinIO 实例可用; When `s3.NewClient(cfg)` 被调用; Then 返回 Client 实例，`Client.Health()` 返回 nil
  - Given S3 Client; When 开发者查看包结构; Then 包不 import cells/（通用 ObjectStore，不含 ArchiveStore）

### AC-4.2 -- 对象操作 [P1]

- **来源**: FR-4.2
- **任务映射**: T36
- **验证方式**: [集成测试]
- **验收条件**:
  - Given bucket 存在; When `Upload(ctx, bucket, key, reader)` 后 `Download(ctx, bucket, key)` 被调用; Then 返回相同内容
  - Given 已上传对象; When `Delete(ctx, bucket, key)` 后 Download; Then 返回 `ERR_ADAPTER_S3_NOT_FOUND`

### AC-4.3 -- Presigned URL [P1]

- **来源**: FR-4.3
- **任务映射**: T37
- **验证方式**: [集成测试]
- **验收条件**:
  - Given bucket 和 key; When `PresignedPut(ctx, bucket, key, ttl)` 被调用; Then 返回可用的 presigned PUT URL
  - Given bucket 和 key; When `PresignedGet(ctx, bucket, key, ttl)` 被调用; Then 返回可用的 presigned GET URL
  - Given TTL 过期; When 使用 presigned URL; Then 请求被拒绝

---

## FR-5: RabbitMQ Adapter

### AC-5.1 -- 连接管理 [P1]

- **来源**: FR-5.1 / S1
- **任务映射**: T21
- **验证方式**: [集成测试]
- **验收条件**:
  - Given RabbitMQ 实例可用; When `rabbitmq.NewConnection(cfg)` 被调用; Then 返回 Connection 实例，`Health()` 返回 nil
  - Given 连接断开; When RabbitMQ 恢复; Then 自动重连（exponential backoff，max 30s）
  - Given Channel 池; When 多 goroutine 并发 Publish; Then channel 池安全复用

### AC-5.2 -- Publisher [P1]

- **来源**: FR-5.2 / S2
- **任务映射**: T22
- **验证方式**: [集成测试]
- **验收条件**:
  - Given 有效连接; When `Publisher.Publish(ctx, topic, payload)` 被调用; Then 消息投递到 RabbitMQ exchange，confirm mode 确认
  - Given `var _ outbox.Publisher = (*RabbitMQPublisher)(nil)`; When 编译; Then 通过

### AC-5.3 -- Subscriber [P1]

- **来源**: FR-5.3 / 决策 PM-08
- **任务映射**: T23
- **验证方式**: [集成测试]
- **验收条件**:
  - Given `NewSubscriber(conn, SubscriberConfig{DLXExchange: "dlx", QueuePrefix: "app"})`; When `Subscribe(ctx, topic, handler)` 被调用; Then 消息消费正常
  - Given `NewSubscriber(conn, SubscriberConfig{})`（DLXExchange 为空）; When `Subscribe` 被调用; Then 返回 `ERR_ADAPTER_AMQP_SUBSCRIBE` 错误（运行时必填校验）
  - Given `var _ outbox.Subscriber = (*RabbitMQSubscriber)(nil)`; When 编译; Then 通过

### AC-5.4 -- ConsumerBase [P1]

- **来源**: FR-5.4 / S2 / S11
- **任务映射**: T24
- **验证方式**: [集成测试]
- **验收条件**:
  - Given ConsumerBase 包装 handler; When handler 返回 nil; Then ACK
  - Given ConsumerBase 包装 handler; When handler 返回 error; Then NACK + exponential backoff 重试（最多 3 次）
  - Given 3 次重试耗尽; When 仍失败; Then 消息路由到 DLQ exchange + queue
  - Given ConsumerBase 持有 `idempotency.Checker`; When 收到已处理消息; Then 跳过，直接 ACK
  - Given unmarshal 失败; When handler 收到消息; Then 直接路由到死信队列（不重试）

### AC-5.5 -- DLQ 可观测 [P1]

- **来源**: FR-5.5 / S11
- **任务映射**: T25
- **验证方式**: [集成测试]
- **验收条件**:
  - Given 消息路由到 DLQ; When 查看日志; Then slog 记录包含 event_id、topic、error、retry_count 字段
  - Given DLQ 中有消息; When 检查计数; Then 死信计数可通过日志或指标确认

---

## FR-6: WebSocket Adapter

### AC-6.1 -- Hub [P1]

- **来源**: FR-6.1 / S1
- **任务映射**: T39
- **验证方式**: [单元测试]
- **验收条件**:
  - Given Hub 实例; When 客户端连接; Then Hub 注册连接（connectionID + userID）
  - Given 已注册连接; When 客户端断开; Then Hub 注销连接
  - Given 多个连接; When 广播消息; Then 所有连接收到
  - Given 特定 connectionID; When 单播消息; Then 仅该连接收到

### AC-6.2 -- Signal-First 模式 [P1]

- **来源**: FR-6.2
- **任务映射**: T39
- **验证方式**: [单元测试]
- **验收条件**:
  - Given Hub 推送消息; When 客户端接收; Then payload 为轻量信号（如 `{"type":"refresh","resource":"config"}`），不含完整数据

### AC-6.3 -- HTTP 升级 [P1]

- **来源**: FR-6.3
- **任务映射**: T40
- **验证方式**: [单元测试]
- **验收条件**:
  - Given `UpgradeHandler(hub)` 注册到路由; When 合法 WebSocket 握手请求; Then 升级成功，连接注册到 Hub
  - Given origin 不在允许列表; When 握手请求; Then 拒绝升级

### AC-6.4 -- 心跳 [P1]

- **来源**: FR-6.4
- **任务映射**: T41
- **验证方式**: [单元测试]
- **验收条件**:
  - Given 连接活跃; When ping interval（默认 30s）到达; Then 发送 ping
  - Given 发送 ping; When pong timeout（默认 10s）内未收到 pong; Then 连接断开

---

## FR-7: Docker Compose 集成环境

### AC-7.1 -- 服务定义 [P3]

- **来源**: FR-7.1 / S8
- **任务映射**: T06
- **验证方式**: [手动验证]
- **操作指南**:
  1. 进入项目根目录
  2. 执行 `docker compose up -d --wait`
  3. 验证 4 个服务 running: `docker compose ps` 显示 PostgreSQL 15+, Redis 7+, RabbitMQ 3.12+(management), MinIO 均为 healthy 状态
  4. 验证 RabbitMQ management UI 可访问: `curl -s http://localhost:15672` 返回 200

### AC-7.2 -- 健康检查 [P3]

- **来源**: FR-7.2 / S8 / 决策 PM-07
- **任务映射**: T09
- **验证方式**: [手动验证]
- **操作指南**:
  1. 执行 `docker compose down -v` 清理
  2. 执行 `docker compose up -d --wait`
  3. 计时: 从命令执行到所有服务 healthy 应在 30 秒内
  4. 若超 30 秒，`make test-integration` 脚本应 exit 1

### AC-7.3 -- 环境变量 [P3]

- **来源**: FR-7.4
- **任务映射**: T07
- **验证方式**: [代码审查]
- **验收条件**:
  - Given `.env.example` 文件; When 开发者查看; Then 所有 adapter 连接参数有默认值（DSN、URL、端口、凭据）
  - Given 开发者复制 `.env.example` 为 `.env`; When 运行 `docker compose up`; Then 无需修改即可启动

---

## FR-8: Testcontainers 集成测试

### AC-8.1 -- 每 adapter 独立测试 [P1]

- **来源**: FR-8.1 / S1
- **任务映射**: T70
- **验证方式**: [集成测试]
- **验收条件**:
  - Given 6 个 adapter 包; When 每包运行 `go test -tags=integration`; Then 至少 1 个 `_integration_test.go` 文件存在且 PASS
  - Given 测试文件; When 检查 build tag; Then 顶部有 `//go:build integration`

### AC-8.2 -- Outbox 全链路测试 [P1]

- **来源**: FR-8.2 / S2
- **任务映射**: T71
- **验证方式**: [集成测试]
- **验收条件**:
  - Given testcontainers 启动 PostgreSQL + RabbitMQ + Redis; When 执行全链路测试; Then 以下步骤全部 PASS:
    1. 业务写入 + outbox 写入在同一 PostgreSQL 事务内
    2. Relay 轮询 outbox_entries 表取出条目
    3. Relay 调用 RabbitMQ Publisher 发布消息
    4. Consumer 从 RabbitMQ 消费消息
    5. IdempotencyChecker 标记已处理
    6. 相同消息重复消费时被幂等跳过

### AC-8.3 -- DLQ 测试 [P1]

- **来源**: FR-8.3 / S11
- **任务映射**: T72
- **验证方式**: [集成测试]
- **验收条件**:
  - Given testcontainers 启动 RabbitMQ; When handler 持续返回 error; Then 消息经 3 次重试后路由到 DLQ
  - Given DLQ 中有消息; When 从 DLQ 读取; Then 消息内容完整可读

### AC-8.4 -- Journey 集成测试 [P1]

- **来源**: FR-8.4 / S3 / 决策 4
- **任务映射**: T73, T43, T44
- **验证方式**: [集成测试]
- **验收条件**:
  - Given AuditRepository + ConfigRepository PG 实现就绪; When 运行 J-audit-login-trail 测试; Then login 事件 -> audit event -> audit 写入 DB -> hash chain 验证全部 PASS
  - Given adapter 就绪; When 运行 J-config-hot-reload 测试; Then config 变更 -> RabbitMQ event -> subscriber 重载全部 PASS
  - Given adapter 就绪; When 运行 J-config-rollback 测试; Then config rollback -> event -> subscriber 重载为旧版本 -> DB 验证全部 PASS
  - Given 三个 Journey; When 测试完成; Then 不依赖 in-memory stub，全部使用真实 adapter

### AC-8.5 -- Assembly 组合集成测试 [P1]

- **来源**: FR-8.5 / 决策 11
- **任务映射**: T74
- **验证方式**: [集成测试]
- **验收条件**:
  - Given postgres Pool + TxManager + OutboxWriter + rabbitmq Publisher + redis IdempotencyChecker 同时注入 CoreAssembly; When 执行 Start -> 业务写入 -> outbox relay -> consume -> idempotency -> Stop; Then 全生命周期 PASS，无资源泄漏

---

## FR-9: Phase 2 安全加固

### AC-9.1 -- 密钥环境变量化 [P1]

- **来源**: FR-9.1 / SEC-03 / S6
- **任务映射**: T51
- **验证方式**: [单元测试]
- **验收条件**:
  - Given `GOCELL_JWT_PRIVATE_KEY` 未设置; When 服务启动; Then 5s 内退出，slog Error 含 "missing required config"
  - Given `GOCELL_JWT_PRIVATE_KEY` 已设置; When 服务启动; Then 正常运行，不使用硬编码密钥

### AC-9.2 -- JWT RS256 迁移 [P1]

- **来源**: FR-9.2 / SEC-04 / S6
- **任务映射**: T52
- **验证方式**: [单元测试]
- **验收条件**:
  - Given RS256 签名的 token; When 验证; Then PASS
  - Given HS256 签名的 token; When 验证; Then 返回 401
  - Given Cell.Init; When 初始化; Then 注入 RS256 公私钥对

### AC-9.3 -- RealIP trustedProxies [P1]

- **来源**: FR-9.3 / SEC-06 / S6
- **任务映射**: T53
- **验证方式**: [单元测试]
- **验收条件**:
  - Given XFF header 来自非 trustedProxies IP; When 请求到达; Then RealIP 使用 RemoteAddr 而非 XFF 值
  - Given XFF header 来自 trustedProxies IP; When 请求到达; Then RealIP 使用 XFF 值

### AC-9.4 -- ServiceToken timestamp [P1]

- **来源**: FR-9.4 / SEC-07 / S6
- **任务映射**: T54
- **验证方式**: [单元测试]
- **验收条件**:
  - Given ServiceToken 签名包含当前 timestamp; When 验证; Then 通过
  - Given timestamp 为 5m01s 之前; When 验证; Then 拒绝
  - Given timestamp 为 4m59s 之前; When 验证; Then 通过（5 分钟窗口含边界）

### AC-9.5 -- UUID 替换 UnixNano [P1]

- **来源**: FR-9.5 / SEC-08 / S6
- **任务映射**: T03
- **验证方式**: [单元测试]
- **验收条件**:
  - Given 7 处 ID 生成点（identitymanage, sessionlogin, auditappend, configwrite, configpublish, eventbus, sessionrefresh）; When 生成 ID; Then 使用 `crypto/rand` UUID
  - Given 100 次并发调用; When 生成 ID; Then 100 个 ID 全部不同

### AC-9.6 -- Refresh token signing method 校验 [P1]

- **来源**: FR-9.6 / SEC-09 / S6
- **任务映射**: T55
- **验证方式**: [单元测试]
- **验收条件**:
  - Given refresh token with alg=RS256; When 验证; Then 通过
  - Given refresh token with alg=none; When 验证; Then 拒绝
  - Given refresh token with alg=HS256; When 验证; Then 拒绝

### AC-9.7 -- Refresh token rotation + reuse detection [P1]

- **来源**: FR-9.7 / SEC-10 / S6
- **任务映射**: T56
- **验证方式**: [单元测试]
- **验收条件**:
  - Given refresh_v1 已成功换取 refresh_v2; When 重放 refresh_v1; Then 拒绝 + 该 session 所有 token 失效
  - Given refresh_v2; When 正常 refresh; Then 返回新 access_token + refresh_v3，refresh_v2 失效

### AC-9.8 -- API 端点认证中间件 [P1]

- **来源**: FR-9.8 / SEC-11 / S6
- **任务映射**: T57
- **验证方式**: [单元测试]
- **验收条件**:
  - Given 无 token; When GET /api/v1/users; Then 返回 401
  - Given 有效 token; When GET /api/v1/users; Then 返回 200
  - Given 无 token; When GET /healthz; Then 返回 200（公开端点）
  - Given 无 token; When GET /readyz; Then 返回 200（公开端点）
  - Given 无 token; When POST /api/v1/auth/login; Then 返回 200（公开端点）
  - Given 无 token; When GET /api/v1/auth/callback; Then 返回 200（公开端点）

---

## FR-10: Phase 2 Tech-Debt 偿还

### AC-10.1 -- errcode 统一 [P2]

- **来源**: FR-10.1
- **任务映射**: T59
- **验证方式**: [代码审查]
- **验收条件**:
  - Given kernel/ 目录; When `grep -rn "fmt.Errorf" kernel/` 对外暴露路径; Then 0 匹配（7 处已改为 errcode）
  - Given cells/ 目录; When `grep -rn "fmt.Errorf" cells/` 对外暴露路径; Then 15 处已改为 `errcode.Wrap`
  - Given eventbus; When "bus is closed" 错误; Then 使用 errcode 而非裸字符串

### AC-10.2 -- 架构修复 [P2]

- **来源**: FR-10.2
- **任务映射**: T64, T47, T48, T49
- **验证方式**: [代码审查] [单元测试]
- **验收条件**:
  - Given ARCH-04 BaseSlice; When 评估; Then 有明确结论（重构或保留附理由）
  - Given ARCH-06 goroutine; When context 使用; Then 改用 shutdownCtx
  - Given 7 处 Cell Publish; When 重构完成; Then 全部改为 outbox.Writer.Write + WithOutboxWriter Option

### AC-10.3 -- 生命周期修复 [P2]

- **来源**: FR-10.3
- **任务映射**: T60
- **验证方式**: [单元测试]
- **验收条件**:
  - Given shutdown.Manager; When Stop; Then 按 LIFO 顺序关闭（#71）
  - Given Worker.Stop; When 多个 worker; Then 串行反序停止（#74）
  - Given Assembly.Stop; When 重复调用; Then 状态机守卫，第二次无操作（#19）
  - Given BaseCell; When 并发调用; Then 线程安全（#50）

### AC-10.4 -- 测试补全 [P2]

- **来源**: FR-10.4
- **任务映射**: T75
- **验证方式**: [单元测试]
- **验收条件**:
  - Given handler 层; When `go test -cover`; Then 覆盖率 >= 80%
  - Given bootstrap.go; When `go test -cover`; Then 覆盖率 >= 70%
  - Given router.go; When `go test -cover`; Then 覆盖率 >= 80%
  - Given cmd/core-bundle; When 冒烟测试运行; Then PASS

### AC-10.5 -- 治理规则 [P2]

- **来源**: FR-10.5
- **任务映射**: T61
- **验证方式**: [单元测试]
- **验收条件**:
  - Given VERIFY-01; When 扩展; Then 覆盖所有角色（#28）
  - Given FMT projection; When replayable 校验; Then 通过（#29）
  - Given 禁用字段名; When 检测; Then 违规报错（#41）
  - Given Parser; When 空 id 输入; Then 校验失败（#44）

### AC-10.6 -- 运维/DX [P2]

- **来源**: FR-10.6
- **任务映射**: T62
- **验证方式**: [单元测试] [代码审查]
- **验收条件**:
  - Given config watcher; When 集成; Then 注册到 bootstrap lifecycle（#20）
  - Given eventbus; When 健康检查; Then 暴露健康状态（#21）
  - Given TopicConfigChanged; When 引用; Then 使用统一常量（#23）

### AC-10.7 -- 边界修复 [P2]

- **来源**: FR-10.7 / 决策 8
- **任务映射**: T63
- **验证方式**: [集成测试]
- **验收条件**:
  - Given configsubscribe 收到 unmarshal 失败消息; When 处理; Then 路由到 DLQ（依赖 FR-5.4 ConsumerBase）
  - Given auditappend publish; When 写入; Then 使用 outbox.Writer 事务内写入（依赖 FR-1.4 OutboxWriter）

### AC-10.8 -- Tech-Debt 计数达标 [P2]

- **来源**: S7
- **任务映射**: T59-T65
- **验证方式**: [手动验证]
- **操作指南**:
  1. 打开 tech-debt-registry.md
  2. 统计总条目: 80 条
  3. 扣除 DEFERRED 6 条（#54, #56-59, #62），有效分母 = 74 条
  4. 统计 RESOLVED 条目数 >= 60
  5. 剩余未 RESOLVED 条目状态为 DEFERRED，且每条有明确理由和计划 Phase

---

## FR-11: Phase 2 产品修复

### AC-11.1 -- 审计查询时间格式校验 [P2]

- **来源**: FR-11.1
- **任务映射**: T66
- **验证方式**: [单元测试]
- **验收条件**:
  - Given 无效时间格式参数; When 审计查询 API 调用; Then 返回 400 + `ERR_VALIDATION_INVALID_TIME_FORMAT`
  - Given 有效时间格式参数; When 审计查询; Then 正常返回结果

### AC-11.2 -- RateLimit Retry-After 动态计算 [P2]

- **来源**: FR-11.2
- **任务映射**: T67
- **验证方式**: [单元测试]
- **验收条件**:
  - Given 限流触发; When 返回 429; Then Retry-After header 值基于实际限流窗口动态计算，非硬编码 1

### AC-11.3 -- Update user PATCH 语义 [P2]

- **来源**: FR-11.3 / 决策 PM-09
- **任务映射**: T68
- **验证方式**: [单元测试]
- **验收条件**:
  - Given user status=active; When PATCH `{"name":"new"}`; Then 仅 name 更新，email/status 不变
  - Given user; When PATCH `{"email":"new@example.com"}`; Then 仅 email 更新（需唯一性校验）
  - Given user; When PATCH `{"status":"suspended"}`; Then 仅 status 更新
  - Given 现有仅发 email 的客户端; When 使用旧格式 PATCH; Then 行为不变（向后兼容）

### AC-11.4 -- 签名算法文档对齐 [P3]

- **来源**: FR-11.4
- **任务映射**: T69
- **验证方式**: [代码审查]
- **验收条件**:
  - Given product-acceptance-criteria.md; When 查看签名算法描述; Then HS256 -> RS256 迁移历史已记录

---

## FR-12: 文档需求

### AC-12.1 -- adapter godoc [P3]

- **来源**: FR-12.1 / S12
- **任务映射**: T77
- **验证方式**: [代码审查]
- **验收条件**:
  - Given 6 个 adapter 包; When 检查; Then 每包有 `doc.go` 文件
  - Given 每个导出类型/函数; When `go doc` 查看; Then 有注释说明

### AC-12.2 -- runtime doc.go 补全 [P3]

- **来源**: FR-12.2
- **任务映射**: T78
- **验证方式**: [代码审查]
- **验收条件**:
  - Given 11 个 runtime 包; When 检查; Then 每包有 `doc.go` 文件

### AC-12.3 -- 集成测试指南 [P3]

- **来源**: FR-12.3
- **任务映射**: T79
- **验证方式**: [手动验证]
- **操作指南**:
  1. 按指南文档执行 `docker compose up -d`
  2. 执行 `go test ./adapters/... -tags=integration`
  3. 验证所有测试 PASS
  4. 新开发者（无预装基础设施）应能在 5 分钟内完成全流程

### AC-12.4 -- adapter 配置参考 [P3]

- **来源**: FR-12.4
- **任务映射**: T80
- **验证方式**: [代码审查]
- **验收条件**:
  - Given 配置参考文档; When 开发者查看; Then 每个 adapter 的环境变量/YAML 配置项、默认值均有说明
  - Given spec 附录 A 默认值; When 对比文档; Then 一致

### AC-12.5 -- Cell 开发指南更新 [P3]

- **来源**: FR-12.5
- **任务映射**: T81
- **验证方式**: [代码审查]
- **验收条件**:
  - Given Cell 开发指南; When 查看; Then 包含 contract test 编写指引和错误处理模式说明

---

## FR-13: DevOps 需求

### AC-13.1 -- Docker Compose 配置 [P3]

- **来源**: FR-13.1
- **任务映射**: T06
- **验证方式**: [代码审查]
- **验收条件**:
  - Given 项目根目录; When 检查; Then 存在 `docker-compose.yml` + `.env.example`

### AC-13.2 -- Makefile [P3]

- **来源**: FR-13.2
- **任务映射**: T08
- **验证方式**: [手动验证]
- **操作指南**:
  1. 执行 `make test-integration`
  2. 验证脚本自动: 启动 Docker Compose -> 等待 healthy -> 运行 `go test -tags=integration` -> 清理
  3. 所有测试 PASS

### AC-13.3 -- go.mod 依赖可控 [P1]

- **来源**: FR-13.3 / S9 / NFR-4
- **任务映射**: T88
- **验证方式**: [代码审查]
- **验收条件**:
  - Given go.mod; When 对比 Phase 2 基线; Then 新增直接依赖仅限 5 个: `pgx/v5`, `go-redis/v9`, `amqp091-go`, `nhooyr.io/websocket`, `testcontainers-go`
  - Given 白名单外直接依赖; When 存在; Then 必须有书面理由

### AC-13.4 -- SQL Migrations [P1]

- **来源**: FR-13.4
- **任务映射**: T13
- **验证方式**: [代码审查]
- **验收条件**:
  - Given `adapters/postgres/migrations/`; When 检查; Then 存在 001_outbox_entries.sql + 002_schema_migrations.sql
  - Given migration 文件; When `Migrator.Up()` 执行; Then 表结构正确创建

---

## FR-14: 测试需求

### AC-14.1 -- 覆盖率达标 [P1]

- **来源**: FR-14.1 / S4 / NFR-3
- **任务映射**: T15, T20, T26, T29, T34, T38, T42
- **验证方式**: [单元测试]
- **验收条件**:
  - Given adapters/ 每个包; When `go test -cover`; Then 覆盖率 >= 80%

### AC-14.2 -- 集成测试 tag [P1]

- **来源**: FR-14.2
- **任务映射**: T70-T74
- **验证方式**: [代码审查]
- **验收条件**:
  - Given 集成测试文件; When 检查 build tag; Then 使用 `//go:build integration`
  - Given `go test ./...`（无 tag）; When 运行; Then 不触发集成测试（不依赖外部服务）

### AC-14.3 -- Journey 端到端 [P1]

- **来源**: FR-14.3 / S3
- **任务映射**: T73
- **验证方式**: [集成测试]
- **验收条件**:
  - Given J-audit-login-trail + J-config-hot-reload + J-config-rollback; When 运行; Then 使用真实 adapter 端到端 PASS（与 AC-8.4 同测试）

### AC-14.4 -- 回归测试 [P1]

- **来源**: FR-14.4 / S10
- **任务映射**: T76
- **验证方式**: [单元测试]
- **验收条件**:
  - Given Phase 2 代码; When `go test ./...`; Then 全部 PASS，无退化
  - Given kernel/ 包; When `go test -cover`; Then 覆盖率 >= 90%

---

## FR-15: Bootstrap 重构

### AC-15.1 -- 接口化 [P1]

- **来源**: FR-15.1 / 决策 3 / S1
- **任务映射**: T01
- **验证方式**: [单元测试]
- **验收条件**:
  - Given `runtime/bootstrap`; When 使用 `WithPublisher(outbox.Publisher)` 注入 RabbitMQ Publisher; Then bootstrap 使用注入的 Publisher 实例
  - Given `WithSubscriber(outbox.Subscriber)` 注入; When bootstrap 启动; Then subscriber 正常工作
  - Given 不使用新 Option; When 使用 `WithEventBus(inMemoryBus)`; Then 仍正常工作（向后兼容）

### AC-15.2 -- 向后兼容 [P1]

- **来源**: FR-15.2 / 决策 3
- **任务映射**: T01
- **验证方式**: [单元测试]
- **验收条件**:
  - Given `WithEventBus(bus)` 调用; When 内部实现; Then 等价于 `WithPublisher(bus)` + `WithSubscriber(bus)`
  - Given Phase 2 现有使用 `WithEventBus` 的代码; When 编译; Then 无破坏性变更

### AC-15.3 -- kernel doc [P2]

- **来源**: FR-15.3 / 决策 1 / KS-01 / KS-03
- **任务映射**: T02
- **验证方式**: [代码审查]
- **验收条件**:
  - Given `outbox.Writer.Write` godoc; When 查看; Then 包含 context-embedded transaction 约定说明
  - Given `outbox.Entry.ID` godoc; When 查看; Then 标注为 canonical idempotency identifier

---

## NFR 验收标准

### AC-NFR-1.1 -- 分层隔离（编译级） [P1]

- **来源**: NFR-1 / S5
- **任务映射**: T85, T86
- **验证方式**: [代码审查]
- **验收条件**:
  - Given 全量代码; When `go build ./...`; Then 编译通过
  - Given adapters/**/*.go; When grep `github.com/ghbvf/gocell/cells`; Then 0 匹配
  - Given kernel/**/*.go; When grep `github.com/ghbvf/gocell/adapters` 或 `github.com/ghbvf/gocell/runtime`; Then 0 匹配
  - Given runtime/**/*.go; When grep `github.com/ghbvf/gocell/adapters` 或 `github.com/ghbvf/gocell/cells`; Then 0 匹配

### AC-NFR-1.2 -- 接口合规 [P1]

- **来源**: NFR-2
- **任务映射**: T87
- **验证方式**: [代码审查]
- **验收条件**:
  - Given 6 adapter 包; When 检查; Then 每个连接持有 struct 实现 `Close(ctx context.Context) error`
  - Given 接口赋值检查; When 编译; Then `var _ outbox.Writer = (...)`, `var _ outbox.Publisher = (...)` 等全部通过

### AC-NFR-2.1 -- 错误规范 [P1]

- **来源**: NFR / 4.3 / C-19 / C-20
- **任务映射**: T89
- **验证方式**: [代码审查]
- **验收条件**:
  - Given adapters/**/*.go（非 _test.go）; When grep `errors.New` 或 `fmt.Errorf` 对外路径; Then 全部使用 `pkg/errcode` 且前缀为 `ERR_ADAPTER_*`
  - Given 底层驱动错误; When return; Then 全部被 `fmt.Errorf("...: %w", err)` 或 `errcode.Wrap` 包装

### AC-NFR-3.1 -- 禁用字段名 [P2]

- **来源**: C-25
- **任务映射**: T90
- **验证方式**: [代码审查]
- **验收条件**:
  - Given Phase 3 新增/修改的 .go 和 .yaml 文件; When grep 禁用字段名（cellId, sliceId, contractId, assemblyId, ownedSlices, authoritativeData, producer, consumers, callsContracts, publishes, consumes）; Then 0 匹配

### AC-NFR-4.1 -- go vet 通过 [P1]

- **来源**: C-24 / S10
- **任务映射**: T85
- **验证方式**: [单元测试]
- **验收条件**:
  - Given 全量代码; When `go vet ./...`; Then 0 警告

---

## Cell PG Repository（最小 L2 证明）

### AC-REPO-1 -- AuditRepository PG 实现 [P1]

- **来源**: 决策 4 / AC-8.4 前置
- **任务映射**: T43
- **验证方式**: [集成测试]
- **验收条件**:
  - Given `cells/audit-core/internal/adapters/postgres/audit_repo.go`; When 实现 AuditRepository 接口; Then 可在 TxManager 事务内与 outbox.Writer 配合使用
  - Given AuditRepository 位于 `cells/audit-core/internal/`; When 外部包尝试 import; Then Go `internal` 可见性阻止编译

### AC-REPO-2 -- ConfigRepository PG 实现 [P1]

- **来源**: 决策 4 / AC-8.4 前置
- **任务映射**: T44
- **验证方式**: [集成测试]
- **验收条件**:
  - Given `cells/config-core/internal/adapters/postgres/config_repo.go`; When 实现 ConfigRepository 接口; Then 可在 TxManager 事务内与 outbox.Writer 配合使用

### AC-REPO-3 -- ArchiveStore S3 封装 [P1]

- **来源**: 决策 2
- **任务映射**: T45
- **验证方式**: [单元测试]
- **验收条件**:
  - Given `cells/audit-core/internal/adapters/s3archive/archive.go`; When 封装 S3 Client 为 ArchiveStore; Then 不 import adapters/ 包（通过接口解耦）
  - Given ArchiveStore; When Upload/Download; Then 委托给 S3 Client 实现

---

## Assembly 接线

### AC-ASSY-1 -- cmd/core-bundle 真实 adapter 接线 [P1]

- **来源**: T50
- **任务映射**: T50
- **验证方式**: [集成测试]
- **验收条件**:
  - Given cmd/core-bundle main.go; When 编译; Then 正确接线: postgres Pool + redis Client + rabbitmq Publisher/Subscriber 注入 bootstrap
  - Given bootstrap 启动; When 使用 WithPublisher + WithSubscriber; Then 替代原 WithEventBus 具体类型注入

---

## 统计总览

| 类别 | P1 | P2 | P3 | 合计 |
|------|-----|-----|-----|------|
| FR-1 PostgreSQL | 5 | 1 | 0 | 6 |
| FR-2 Redis | 4 | 0 | 0 | 4 |
| FR-3 OIDC | 4 | 0 | 0 | 4 |
| FR-4 S3 | 3 | 0 | 0 | 3 |
| FR-5 RabbitMQ | 5 | 0 | 0 | 5 |
| FR-6 WebSocket | 4 | 0 | 0 | 4 |
| FR-7 Docker Compose | 0 | 0 | 3 | 3 |
| FR-8 集成测试 | 5 | 0 | 0 | 5 |
| FR-9 安全加固 | 8 | 0 | 0 | 8 |
| FR-10 Tech-Debt | 0 | 8 | 0 | 8 |
| FR-11 产品修复 | 0 | 3 | 1 | 4 |
| FR-12 文档 | 0 | 0 | 5 | 5 |
| FR-13 DevOps | 2 | 0 | 2 | 4 |
| FR-14 测试 | 4 | 0 | 0 | 4 |
| FR-15 Bootstrap | 2 | 1 | 0 | 3 |
| NFR | 4 | 1 | 0 | 5 |
| Cell Repo | 3 | 0 | 0 | 3 |
| Assembly | 1 | 0 | 0 | 1 |
| **合计** | **54** | **14** | **11** | **79** |

---

## 产品验收判定规则

1. P1 全部 54 条 = PASS -> 通过
2. P2 无 FAIL（SKIP 必须附理由）-> 通过
3. 产品评审报告无红色维度 -> 通过
4. S1-S12 成功标准全部达成 -> 通过

以上四项全部满足 -> **产品 PASS**；任一不满足 -> **产品 FAIL**（列出未达标项 + 修复建议）
