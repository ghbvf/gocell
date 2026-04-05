# Changelog

All notable changes to GoCell are documented in this file.

Format follows [Keep a Changelog](https://keepachangelog.com/).

## [Unreleased] - Phase 3: Adapters

> Branch: `feat/002-phase3-adapters`
> 变更规模: 191 files changed, 16398 insertions(+), 284 deletions(-)
> git log base: `develop..HEAD`（`8dbc260` → `cbab9f3`）

### feat

- **adapters/postgres**: Pool (pgx/v5)、TxManager、Migrator、OutboxWriter、OutboxRelay (`b7bebb8`)
- **adapters/redis**: Client (go-redis/v9)、DistLock、IdempotencyChecker、Cache (`dd6fc82`)
- **adapters/rabbitmq**: Connection、Publisher、Subscriber、ConsumerBase (DLQ + retry + backoff) (`84d1531`)
- **adapters/oidc + s3 + websocket**: OIDC Provider Client + S3/MinIO Client + WebSocket Hub (`43b5bca`)
- **adapters/postgres (Cell repos)**: AuditRepository PG 实现、ConfigRepository PG 实现、outbox chain (`43b5bca`)
- **runtime/security**: RS256 JWTIssuer + JWTVerifier、trustedProxies RealIP、ServiceToken timestamp 防重放、认证中间件 (`1551c12`)
- **cells/access-core**: RS256 JWTIssuer/Verifier Option 注入、refresh token rotation + reuse detection、WithJWTIssuer/WithJWTVerifier (`44b1253`)
- **cells/audit-core + config-core**: outbox.Writer 重构（7 处 publisher.Publish 替换）、ArchiveStore Cell 内部封装 (`44b1253`)
- **pkg/uid**: crypto/rand UUID 生成器，替换 7 处 UnixNano ID (`3fe050a`)
- **runtime/bootstrap**: 接口化重构——WithPublisher + WithSubscriber 替代具体类型注入；WithEventBus 保留向后兼容 (`e1bf267`)
- **devops**: Docker Compose（PostgreSQL + Redis + RabbitMQ + MinIO）+ .env.example + Makefile + healthcheck (`9aabc62`)

### fix

- **kernel/lifecycle**: LIFO 关闭顺序 + BaseCell 互斥锁保护 + goroutine context 取消 (`6bda474`)
- **kernel/governance**: FMT-10 空 id 检查 + governance 规则修复 (`6bda474`)
- **kernel/errcode**: kernel 层 + eventbus 层统一接入 pkg/errcode，消除裸 errors.New (`6bda474`)
- **cells**: 7 处 publisher.Publish 替换为 outbox.Writer.Write（L2 一致性绑定）(`44b1253`)
- **runtime/auth**: RS256 集成、outbox transaction 绑定、env prefix 统一（GOCELL_*）、relay tx (`b8d7662`)
- **runtime/config**: config watcher 集成到 bootstrap 生命周期 (`b8d7662`)
- **cells/audit-core**: 审计查询 time.Parse 错误返回 400（替换静默忽略）(`44b1253`)
- **cells/access-core**: PATCH user 扩展可更新字段（不再仅 email）(`44b1253`)
- **go vet**: copylocks warning 修复；tasks/PRs 标记完成 (`67b060b`)
- **evidence**: validate result.txt pattern 匹配修复 (`3c6e4de`)

### chore

- Phase 3 specs 初始化（S0-S4 完整）(`8dbc260`)
- Wave 4：集成测试 stub、docs、KG verification (`c7a67c8`)
- S5/S6/S7 gate PASS 审计日志更新 (`af24e05`, `538b304`, `cbab9f3`)
- S6 review-findings + tech-debt + gate audit (`6414392`)
- S7 QA report + user-signoff + evidence (`31dd60c`)

---

## [Unreleased] - Phase 2: Runtime + Built-in Cells

### Added

- **runtime/http/middleware**: 7 个 chi 中间件 -- request_id, real_ip, recovery, access_log, security_headers, body_limit, rate_limit (`0c2e257`)
- **runtime/http/health**: /healthz 健康端点，聚合 Assembly.Health() (`0c2e257`)
- **runtime/http/router**: chi-based 路由构建器 + RouteMux 抽象 (`0c2e257`)
- **runtime/http/httputil**: 共享 WriteJSON / WriteDomainError 工具包，消除 12 处重复 (`eec1262`)
- **runtime/config**: YAML/env 配置加载 + fsnotify 文件变更 watcher (`0c2e257`)
- **runtime/bootstrap**: 统一启动器（config -> assembly -> HTTP -> workers） (`0c2e257`)
- **runtime/shutdown**: graceful shutdown（signal -> timeout -> 有序 teardown） (`0c2e257`)
- **runtime/observability**: Prometheus 指标注册 + OpenTelemetry tracing + slog handler (`0c2e257`)
- **runtime/worker**: 后台 worker 生命周期 + periodic job 框架 (`0c2e257`)
- **runtime/auth**: JWT 验证 + RBAC 抽象中间件 + ServiceToken HMAC 服务间认证 (`0c2e257`)
- **runtime/eventbus**: in-memory Pub/Sub（at-most-once + 3x 重试 + dead letter channel） (`0c2e257`)
- **cells/access-core**: 5 slices -- identity-manage / session-login / session-refresh / session-logout / authorization-decide (`0c2e257`)
- **cells/audit-core**: 3 slices -- audit-write / audit-verify / audit-archive + HMAC-SHA256 hash chain (`0c2e257`)
- **cells/config-core**: 4 slices -- config-manage / config-publish / config-subscribe / feature-flag (`0c2e257`)
- **kernel/outbox**: Subscriber 接口（与 Publisher 对称） (`0c2e257`)
- **kernel/cell**: HTTPRegistrar / EventRegistrar / RouteMux 可选接口 (`0c2e257`)
- **cmd/core-bundle**: 3 Cell 编排启动入口（config-core -> access-core -> audit-core） (`0c2e257`)
- **docs/guides**: Cell 开发指南 (`0c2e257`)
- 全量代码审查报告与审查基线计划 (`2014298`)

### Changed

- **CLAUDE.md**: 补充 `runtime/ 可依赖 kernel/ 和 pkg/` 依赖规则 (`0c2e257`)
- **README.md**: 更新架构图和模块列表，对齐 Phase 2 实际产物 (`0c2e257`, `ba88152`)
- **kernel/governance**: 回收 internal/meta 校验规则到 kernel/governance，删除 internal/ (`52cf8e3`)
- **kernel/governance**: validate/depcheck/targets 修复 P1 设计问题 (`eec1262`, `50558d5`)
- **kernel/assembly**: generator 遵守 entrypoint 约定 (`830ed6e`)
- YAML 元数据修正: slice.yaml / contract.yaml 补全 subscribe 声明、serving slice、contractUsage (`0c2e257`)
- workflow 迁移 + review 归档 + 垃圾文件清理 (`ab166c9`)

### Fixed

- **SEC-01** (P0): 密码从 subtle.ConstantTimeCompare 迁移到 bcrypt hash+compare (`0c2e257`)
- **SEC-02** (P0): 创建 UserResponse DTO，PasswordHash 不再泄露给客户端 (`0c2e257`)
- **ARCH-01**: 500 响应不再暴露 err.Error()，固定返回 "internal server error" (`eec1262`)
- **ARCH-08**: session refresh 后 persist session，旧 refresh token 失效 (`600460f`)
- **PM-01**: 错误响应统一包含 details 字段 (`eec1262`)
- **PM-02**: service 错误用 errors.As 区分 404 vs 500 (`eec1262`)
- **DX-01**: writeJSON/writeError 12 处重复抽取到 httputil 共享包 (`eec1262`)
- kernel 层 6 个 BUG 修复: assembly health 状态、governance rules 边界条件、metadata parser 容错 (`600460f`)
- targets 补 journeys/assemblies 路径 (`830ed6e`)
- YAML 资产自洽 + placeholder 命令 fail-closed (`2f83950`)

---

## Phase 0+1: Kernel (prior to workflow)

### Added

- **kernel/metadata**: YAML parser + types（cell.yaml / slice.yaml / contract.yaml / journey.yaml / assembly.yaml / actors.yaml） (`8fc3cba`)
- **kernel/governance**: validate（REF/TOPO/VERIFY/FMT/ADV 规则） + depcheck + select-targets (`8fc3cba`, `b9f89d6`)
- **kernel/journey**: catalog（Journey 加载 + 关联解析） (`8fc3cba`)
- **kernel/registry**: cell + contract 注册表 (`8fc3cba`)
- **kernel/scaffold**: cell / slice / contract / journey 骨架生成 (`8fc3cba`)
- **kernel/assembly**: generator（boundary.yaml + main.go 模板） (`b9f89d6`)
- **kernel/slice**: verify runner（单元 + 契约 + 冒烟测试执行） (`24913eb`)
- **kernel/outbox**: Publisher 接口 (`24913eb`)
- **kernel/idempotency**: IdempotencyChecker 接口 (`24913eb`)
- **cmd/gocell**: CLI 入口 -- validate / scaffold / generate / check / verify (`24913eb`)
