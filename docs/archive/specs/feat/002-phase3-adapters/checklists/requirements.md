# Requirements Checklist — Phase 3: Adapters

> 来源: spec.md FR-1 ~ FR-14, NFR-1 ~ NFR-8
> 用途: S4 plan 编排 + S7 验收追踪

---

## FR-1: PostgreSQL Adapter

- [ ] FR-1.1 Pool: pgx/v5 连接池 + DSN/env 配置 + Health()
- [ ] FR-1.2 TxManager: RunInTx + savepoint + panic 回滚
- [ ] FR-1.3 Migrator: embed.FS migration + up/down/status
- [ ] FR-1.4 Outbox Writer: 实现 kernel/outbox.Writer，事务内写入
- [ ] FR-1.5 Outbox Relay: 实现 kernel/outbox.Relay，轮询+发布+标记
- [ ] FR-1.6 Repository 基础设施: RowScanner / QueryBuilder

## FR-2: Redis Adapter

- [ ] FR-2.1 Client: go-redis/v9 连接 + Health()
- [ ] FR-2.2 DistLock: 分布式锁 Acquire/Release + TTL
- [ ] FR-2.3 Idempotency Checker: 实现 kernel/idempotency.Checker
- [ ] FR-2.4 Cache: Get/Set/Delete + TTL + JSON 泛型 helper

## FR-3: OIDC Adapter

- [ ] FR-3.1 Provider Client: OIDC Discovery + metadata 缓存
- [ ] FR-3.2 Token Exchange: Authorization Code → tokens
- [ ] FR-3.3 JWKS Verifier: 公钥拉取 + kid rotation + RS256 验证
- [ ] FR-3.4 UserInfo: accessToken → 用户信息

## FR-4: S3 Adapter

- [ ] FR-4.1 Client: S3/MinIO endpoint + credentials 配置 + Health()
- [ ] FR-4.2 对象操作: Upload/Download/Delete
- [ ] FR-4.3 Presigned URL: Put/Get + TTL
- [ ] FR-4.4 ArchiveStore: 实现 audit-core ArchiveStore 接口

## FR-5: RabbitMQ Adapter

- [ ] FR-5.1 连接管理: AMQP URL + 自动重连 + channel 池 + Health()
- [ ] FR-5.2 Publisher: 实现 kernel/outbox.Publisher + confirm mode
- [ ] FR-5.3 Subscriber: 实现 kernel/outbox.Subscriber + consumer group
- [ ] FR-5.4 ConsumerBase: 幂等检查 + 3x 重试 + DLQ 路由
- [ ] FR-5.5 DLQ 可观测: slog 日志 + 计数

## FR-6: WebSocket Adapter

- [ ] FR-6.1 Hub: 连接管理（注册/注销/广播/单播）
- [ ] FR-6.2 Signal-First: 轻量信号推送模式
- [ ] FR-6.3 HTTP 升级: UpgradeHandler + origin 检查
- [ ] FR-6.4 心跳: ping/pong + 超时断开

## FR-7: Docker Compose

- [ ] FR-7.1 服务定义: PostgreSQL + Redis + RabbitMQ + MinIO
- [ ] FR-7.2 健康检查: 30 秒内全部 healthy
- [ ] FR-7.3 数据卷: named volume + CI tmpfs
- [ ] FR-7.4 环境变量: .env.example

## FR-8: Testcontainers 集成测试

- [ ] FR-8.1 每 adapter 独立 integration_test.go
- [ ] FR-8.2 Outbox 全链路测试
- [ ] FR-8.3 DLQ 测试
- [ ] FR-8.4 Journey 集成测试 (J-audit-login-trail, J-config-hot-reload)

## FR-9: 安全加固 (8 条)

- [ ] FR-9.1 SEC-03: 密钥 → 环境变量 + fail-fast
- [ ] FR-9.2 SEC-04: JWT HS256 → RS256
- [ ] FR-9.3 SEC-06: trustedProxies 配置
- [ ] FR-9.4 SEC-07: ServiceToken +timestamp +5min 窗口
- [ ] FR-9.5 SEC-08: 7 处 UnixNano → crypto/rand UUID
- [ ] FR-9.6 SEC-09: 显式校验 signing method
- [ ] FR-9.7 SEC-10: refresh token rotation + reuse detection
- [ ] FR-9.8 SEC-11: API 端点认证中间件

## FR-10: Tech-Debt 偿还

- [ ] FR-10.1 编码规范: errcode 统一 (kernel 7 处 + cells 15 处 + eventbus)
- [ ] FR-10.2 架构修复: BaseSlice / goroutine ctx / L2 outbox
- [ ] FR-10.3 生命周期: shutdown LIFO / Worker.Stop / Assembly.Stop / BaseCell 线程安全
- [ ] FR-10.4 测试补全: handler httptest / bootstrap / core-bundle / router
- [ ] FR-10.5 治理规则: VERIFY-01 / FMT projection / 禁用字段名 / Parser 空 id
- [ ] FR-10.6 运维/DX: config watcher / eventbus health / doc.go / 常量统一

## FR-11: 产品修复

- [ ] FR-11.1 审计查询 time.Parse → 400
- [ ] FR-11.2 RateLimit Retry-After 动态计算
- [ ] FR-11.3 Update user 扩展字段
- [ ] FR-11.4 AC-8.2 文档对齐

## FR-12: 文档

- [ ] FR-12.1 adapter godoc (6 包 doc.go)
- [ ] FR-12.2 runtime doc.go 补全 (11 包)
- [ ] FR-12.3 集成测试指南
- [ ] FR-12.4 adapter 配置参考
- [ ] FR-12.5 Cell 开发指南更新

## FR-13: DevOps

- [ ] FR-13.1 docker-compose.yml + .env.example
- [ ] FR-13.2 Makefile make test-integration
- [ ] FR-13.3 go.mod 新增 5 个依赖
- [ ] FR-13.4 SQL migrations (outbox_entries + schema_migrations)

## FR-14: 测试

- [ ] FR-14.1 单元测试 (每 adapter 包)
- [ ] FR-14.2 集成测试 (testcontainers)
- [ ] FR-14.3 Journey 端到端
- [ ] FR-14.4 回归测试 (Phase 2 不退化)

---

## NFR 检查

- [ ] NFR-1 分层隔离: adapters/ 不 import cells/, kernel/ 不 import adapters/
- [ ] NFR-2 接口契约: 接口赋值检查编译通过
- [ ] NFR-3 覆盖率: adapters >= 80%, kernel >= 90%, cells >= 80%, runtime >= 80%
- [ ] NFR-4 外部依赖: 5 个白名单内, testcontainers 仅 test
- [ ] NFR-5 连接韧性: 自动重连 + Health()
- [ ] NFR-6 配置可注入: Config struct + env 支持
- [ ] NFR-7 可观测性: slog 结构化 + 日志级别合规
- [ ] NFR-8 优雅关闭: Close(ctx) + 顺序控制
