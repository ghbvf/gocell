# Phase Charter — Phase 3: Adapters

## Phase 目标

在已完成的 runtime 层和 3 个内建 Cell（Phase 2: 173 文件, 48 测试包全绿）基础上，实现 6 个外部系统适配器（postgres, redis, oidc, s3, rabbitmq, websocket），将 Phase 2 的 in-memory 实现替换为真实基础设施集成。同时系统性处理 Phase 2 遗留的 tech-debt，完成安全加固（密钥管理、JWT RS256 迁移、认证中间件）和测试补全。

交付物包括：PostgreSQL 连接池 + TxManager + Migrator + Outbox 实现、Redis 连接 + 分布式锁 + 幂等检查、OIDC provider client + token exchange、S3/MinIO client + presigned URL、RabbitMQ Publisher + Consumer（ConsumerBase + DLQ + retry）、WebSocket hub + signal-first 模式，以及 Docker Compose 集成测试环境和 testcontainers 全链路验证。

## 范围

### 目标（In Scope）

- **adapters/postgres/**: 连接池（pgx/v5）、TxManager、Migrator、outbox.Writer + outbox.Relay 实现
- **adapters/redis/**: 连接（go-redis/v9）、分布式锁、idempotency.Checker 实现
- **adapters/oidc/**: OIDC provider client、token exchange、JWKS 验证
- **adapters/s3/**: S3/MinIO client、presigned URL 生成
- **adapters/rabbitmq/**: Publisher + Consumer（amqp091-go）、ConsumerBase + DLQ + retry
- **adapters/websocket/**: WebSocket hub（nhooyr.io/websocket）、signal-first 推送模式
- **Docker Compose**: 集成测试环境（PostgreSQL + Redis + RabbitMQ + MinIO）
- **testcontainers**: 全链路集成测试（outbox→relay→consume、OIDC login、RabbitMQ DLQ、WebSocket push）
- **Phase 2 tech-debt 处理**: 80 条分层处理（详见连续性处理章节）
- **安全加固**: 密钥环境变量化、JWT RS256 迁移、认证中间件、ServiceToken 协议加固
- **新增外部依赖**: `pgx/v5`, `go-redis/v9`, `amqp091-go`, `nhooyr.io/websocket`, `testcontainers-go`

### 非目标（Out of Scope）

- examples/ 示例项目 — Phase 4
- 生产级 Kubernetes 部署配置 — Phase 4+
- 前端代码 — 项目无前端
- 性能基准测试 — Phase 4
- 多租户支持 — 未在 roadmap 中

### N/A 声明

| 标准文件 | 理由 |
|---------|------|
| evidence/playwright/result.txt | N/A:SCOPE_IRRELEVANT — Phase 3 无 UI，前端开发者 OFF |
| playwright.config.ts | N/A:SCOPE_IRRELEVANT — 同上 |

## 连续性处理

### 从上一 Phase 继承的必须修复项

**来源: kernel-review-report.md (3 条)**

| # | 来源文件 | 项目 | 处理方式 |
|---|---------|------|---------|
| K1 | kernel-review-report.md | config watcher 未集成 bootstrap 生命周期（#20） | 纳入本 Phase — adapter 引入真实配置热更新场景后必须修复 |
| K2 | kernel-review-report.md | 10/16 slices handler 层覆盖率 < 80%（#13/#32） | 纳入本 Phase — 认证中间件上线后需验证认证链正确性 |
| K3 | kernel-review-report.md | 密钥硬编码 + JWT HS256（#5/#6） | 纳入本 Phase — Docker 部署前必须完成安全迁移 |

**来源: product-review-report.md (3 条)**

| # | 来源文件 | 项目 | 处理方式 |
|---|---------|------|---------|
| P1 | product-review-report.md | AC-8.2 签名算法文档对齐 | 纳入本 Phase — 文档修订 |
| P2 | product-review-report.md | router.go 覆盖率 78.8% | 纳入本 Phase — 补 1-2 个测试用例 |
| P3 | product-review-report.md | 审计查询 time.Parse 静默忽略 | 纳入本 Phase — 返回 400 |

**来源: tech-debt.md (80 条) — 分层处理策略**

| 优先级 | 范围 | 条数 | 处理方式 |
|--------|------|------|---------|
| P0-必须 | 安全类: SEC-03/04/06/07/08/09/10/11, #30/#31/#33/#34 | 12 | 纳入本 Phase，adapter 基础设施就绪后立即修复 |
| P0-必须 | Adapter 直接相关: ARCH-07(outbox事务), #37(hash chain 断链), #16(in-memory repo), #64(audit archive stub) | 4 | 纳入本 Phase，adapter 实现的核心目标 |
| P1-应当 | 架构/生命周期: ARCH-04(BaseSlice), ARCH-06(goroutine ctx), #48-50(Start/Stop/线程安全), #70-71(shutdown), #74(Worker.Stop) | 10 | 纳入本 Phase，与 adapter 集成一并重构 |
| P1-应当 | 代码质量: #27(errcode), #79(cells fmt.Errorf), #73(eventbus errcode), #52(contract ID 格式), #53(issueToken 重复) | 8 | 纳入本 Phase，编码规范修正 |
| P1-应当 | 测试补全: T-01/#32(handler覆盖率), T-02(端到端), T-03(bootstrap), T-05(集成), T-06(copylocks), T-07(冒烟测试) | 8 | 纳入本 Phase，adapter 就绪后可执行真实测试 |
| P2-可选 | 治理规则完善: #28-29(VERIFY/FMT规则), #36-46(governance 杂项), #44-47(registry/catalog) | 16 | 纳入本 Phase 尽力处理，溢出则 DEFERRED 至 Phase 4 |
| P2-可选 | 运维/DX: D-06/07/09(生命周期), DX-02/03(doc.go/常量), #65-68(middleware), #76-78(CLI/WriteJSON) | 14 | 纳入本 Phase 尽力处理，溢出则 DEFERRED 至 Phase 4 |
| P3-延迟 | 产品体验: PM-03(Retry-After), #25(time.Parse→P1已提升), #26(Update user), #35(metrics format) | 4 | 纳入本 Phase，低优先级 |
| P3-延迟 | 高风险重构: #54(TOCTOU竞态), #56-59(access-core domain), #60-62(config/audit 边界) | 8 | DEFERRED — 需 adapter 持久化稳定后再处理 |

**合计**: 纳入本 Phase 约 72 条，DEFERRED 至 Phase 4 约 8 条。

### 延迟处理

| 项目 | 延迟理由 | 计划修复 Phase |
|------|---------|---------------|
| #54 Session refresh TOCTOU 竞态 | 需 Redis 分布式锁 + 持久化 session 稳定后才能正确实现 | Phase 4 |
| #56 Service 层 Create 返回含 PasswordHash | 需重新设计 Service 接口返回类型，影响面大 | Phase 4 |
| #57 Session.ExpiresAt 语义 | 需重新定义 Session 生命周期模型 | Phase 4 |
| #58 UserRepository.Update byName 索引残留 | 需 DB migration 后在真实存储层修复 | Phase 4 |
| #59 无 JWT jti claim | 需 token 撤销基础设施（Redis blacklist）稳定后 | Phase 4 |
| #60 configsubscribe unmarshal 失败 ACK | 需 RabbitMQ DLQ 稳定后修复 | Phase 3 后期或 Phase 4 |
| #61 auditappend publish 失败仅 log | 需 outbox 保证就绪后修复 | Phase 3 后期或 Phase 4 |
| #62 configpublish.Rollback 不校验 version | 需持久化 version 管理稳定后 | Phase 4 |

## Gate 验证

```bash
# Phase 3 Gate: 全链路 outbox→relay→consume, OIDC login, RabbitMQ DLQ, WebSocket push
docker compose up -d && go test ./adapters/... -tags=integration
```
