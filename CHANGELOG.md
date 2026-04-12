# Changelog

All notable changes to GoCell are documented in this file.

Format follows [Keep a Changelog](https://keepachangelog.com/).

## [Unreleased] - Decode 严格化 + 回归锁定

> PR: #89
> Scope: SF-01/SF-02/SF-03/SF-04/HT-01/HT-02

### Breaking

- **pkg/httputil**: All struct-targeting handlers now use `DecodeJSONStrict`, which rejects unknown JSON fields with HTTP 400. Clients sending extra fields will receive:
  ```json
  {"error": {"code": "ERR_VALIDATION_FAILED", "message": "invalid request body", "details": {"reason": "unknown field", "field": "<name>"}}}
  ```
  Affected endpoints: POST /devices, POST /devices/{id}/commands, POST /orders, POST /sessions/login, POST /sessions/refresh, POST /identities, PUT /identities/{id}, PUT /configs/{key}, POST /configs, POST /configs/{key}/rollback, POST /flags/{key}/evaluate.

- **pkg/httputil**: `WriteDecodeError` now includes `details` in 4xx responses (previously always `{}`). This surfaces the error reason (empty body, malformed JSON, type mismatch, unknown field) to API clients.

### Added

- **pkg/httputil**: `DecodeJSONStrict(r *http.Request, dst any) error` — strict JSON decoder that rejects unknown fields via `json.Decoder.DisallowUnknownFields()`. Map destinations are unaffected.
- **pkg/httputil**: `classifyDecodeError` now detects unknown field errors and returns `ErrValidationFailed` with `{"reason": "unknown field", "field": "<name>"}`.

### Design

- ref: gin-gonic/gin `binding/json.go` — adopted `DisallowUnknownFields` approach; diverged from global toggle to per-call `DecodeJSONStrict` function for granular migration.
- `identitymanage.handlePatch` (JSON merge patch) intentionally kept on `DecodeJSON` — map targets accept any key by design.

## [Unreleased] - PR-Cleanup: Kernel 架构整理

> PR: #79
> Scope: K-1/K-2/K-3/K-5

### Breaking

- **kernel/cell**: `Dependencies` struct 移除 `Cells map[string]Cell` 和 `Contracts map[string]Contract` 字段（零调用方使用）。仅保留 `Config map[string]any`。迁移：删除 `Dependencies{}` 字面量中的 `Cells:` 和 `Contracts:` 字段。

### Added

- **kernel/outbox**: `BatchWriter` 接口（嵌入 `Writer`，新增 `WriteBatch` 方法）+ `WriteBatchFallback` helper（自动检测 batch 支持，降级到顺序写入）
- **adapters/postgres**: `OutboxWriter` 实现 `BatchWriter`，使用多行 INSERT，超过 7000 条自动分片

### Documentation

- **kernel/cell/registrar.go**: net/http ADR 注释（CS-AR-3，已存在，标记完成）
- **kernel/cell/interfaces.go**: `Dependencies` 冻结 ADR 注释

## [Unreleased] - Phase 4: Examples + Documentation

> Branch: `feat/003-phase4-examples-docs`
> 变更规模: 20 commits
> git log base: `28ac80f..e15462d`

### feat

- **examples/todo-order**: 自定义 order-cell golden-path 示例（L2-L3，outbox pattern + RabbitMQ + in-memory repo），附 docker-compose.yml + README (`8d03190`)
- **examples/sso-bff**: 3 个内置 Cell（access-core + audit-core + config-core）组合示例（L1-L2），附 docker-compose.yml + README (`764c179`)
- **examples/iot-device**: L4 DeviceLatent 一致性示例（命令回执 + WebSocket 推送），附 docker-compose.yml + README (`3a3f8ca`)
- **cells/access-core**: RS256 完整迁移——引入 WithJWTIssuer/WithJWTVerifier Option；三个 slice（sessionlogin / sessionvalidate / sessionrefresh）迁移到 JWTIssuer/JWTVerifier 接口 (`01d49f1`)
- **cells/access-core + audit-core + config-core**: outboxWriter fail-fast——L2+ Cell 的 Init 阶段检查 outboxWriter 是否注入，缺失时返回 ERR_CELL_MISSING_OUTBOX (`2ff2acc`)
- **runtime/auth**: 新增 MustGenerateTestKeyPair + LoadRSAKeyPairFromPEM RSA helper（测试辅助工具） (`d52336c`)
- **ci**: GitHub Actions CI 工作流——build / test / vet / gocell validate / integration / kernel coverage gate (`3d79543`)
- **Wave 0**: S3 GOCELL_S3_* env prefix 对齐（fallback to S3_* + slog.Warn deprecation）；root docker-compose.yml 补全 start_period: 15s；testcontainers-go v0.41.0 加入 go.mod (`2a0f7cb`)

### test

- **adapters/postgres**: 替换全部 t.Skip stub 为 testcontainers 集成测试（Pool / TxManager / Migrator / OutboxWriter / OutboxRelay） (`84aa617`)
- **adapters/redis + rabbitmq**: 替换全部 t.Skip stub 为 testcontainers 集成测试（Client / DistLock / IdempotencyChecker / Publisher / Subscriber / ConsumerBase / DLQ） (`494856a`)
- **integration**: 新增 outbox 全链路集成测试——postgres outbox write → relay → rabbitmq publish → consumer → redis idempotency check（FR-6.5） (`b1dfddc`)

### docs

- **README**: Getting Started——5 分钟快速开始 + 30 分钟完整教程（core concepts / consistency levels / Cell 结构说明） (`67f2a3e`)
- **templates**: 6 个项目模板——adr.md / cell-design.md / contract-review.md / runbook.md / postmortem.md / grafana-dashboard.json (`638feca`)
- **specs**: Phase 4 S0-S4 规格文档——charter / reviews / decisions / plan / tasks (`0d49743`)

### fix

- **S6 P0 fixes**: errcode 常量替换 string literal；access-core 移除 ephemeral RSA key 生成路径 (`eace83a`)
- **metadata**: 新 contract / slice 的 YAML 格式对齐 (`f59d0dc`)
- **tests**: httptest sandbox TCP 监听跳过 guard（非沙箱环境自动跳过） (`26d34ac`)
- **evidence**: S7 evidence 格式修正——journey/result.txt + validate PASS 行 (`e15462d`)

### chore

- Phase 4 S7 QA 验证报告 + user-signoff + evidence (`a93fb0b`)
- tech-debt.md 添加 [TECH]/[PRODUCT] 分类标签 (`ad68298`)

---

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
- **runtime/bootstrap**: 接口化重构——WithPublisher + WithSubscriber 替代具体类型注入；WithEventBus 已删除 (`e1bf267`, PR#83)
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
