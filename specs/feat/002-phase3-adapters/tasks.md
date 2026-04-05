# Tasks — Phase 3: Adapters

> 生成来源: spec.md v2 + decisions.md + plan.md
> 标记: [P] = 可并行, [S] = 串行, [DEP:Txx] = 依赖

---

## Wave 0: 前置准备

- [x] T01 [S] FR-15.1: 重构 `runtime/bootstrap` — `WithPublisher(outbox.Publisher)` + `WithSubscriber(outbox.Subscriber)` 替代 `WithEventBus` 具体类型。保留 `WithEventBus` 便利方法。ref: Uber fx app.go 接口注入模式
- [x] T02 [P] FR-15.3: kernel/outbox doc 增强 — `outbox.Writer.Write` godoc 增加 context-embedded tx 约定; `outbox.Entry.ID` 标注为 idempotency identifier
- [x] T03 [P] FR-9.5: 7 处 UnixNano → `crypto/rand` UUID（identitymanage, sessionlogin, auditappend, configwrite, configpublish, eventbus, sessionrefresh）
- [x] T04 [P] 后端: 种子数据脚本 `scripts/seed-test-data.sh`（创建测试用户/角色/config 条目，供集成测试使用）
- [x] T05 [S] go test ./... 回归验证 Wave 0 不破坏 Phase 2

## Wave 1: 基础设施 Adapter + DevOps

### Docker Compose + DevOps

- [x] T06 [P] FR-7.1+FR-13.1: `docker-compose.yml` — PostgreSQL 15 + Redis 7 + RabbitMQ 3.12(management) + MinIO。ref: Kratos docker-compose 模式
- [x] T07 [P] FR-7.4: `.env.example` — 所有 adapter 连接参数默认值
- [x] T08 [P] FR-13.2: `Makefile` — `make test-integration` (docker compose up --wait + go test -tags=integration + cleanup)
- [x] T09 [P] FR-7.2: healthcheck 配置 + `docker compose up -d --wait` 30s 验证脚本

### PostgreSQL Adapter (基础)

- [x] T10 [P] FR-1.1: `adapters/postgres/pool.go` — pgx/v5 连接池 + DSN/env config + Health()。ref: Watermill watermill-sql connection
- [x] T11 [S] [DEP:T10] FR-1.2: `adapters/postgres/tx_manager.go` — RunInTx + savepoint + panic 回滚 + context-embedded tx
- [x] T12 [S] [DEP:T10] FR-1.3: `adapters/postgres/migrator.go` — embed.FS migration + up/down/status + schema_migrations 表
- [x] T13 [S] [DEP:T10] FR-13.4: `adapters/postgres/migrations/` — 001_outbox_entries.sql + 002_schema_migrations.sql
- [x] T14 [P] FR-1.6: `adapters/postgres/helpers.go` — RowScanner + QueryBuilder
- [x] T15 [P] 单元测试: `adapters/postgres/*_test.go` (mock pgx)

### Redis Adapter

- [x] T16 [P] FR-2.1: `adapters/redis/client.go` — go-redis/v9 连接 + standalone/sentinel + Health()。ref: go-micro store/redis
- [x] T17 [P] [DEP:T16] FR-2.2: `adapters/redis/distlock.go` — Redlock TTL + 续租 + Acquire/Release
- [x] T18 [P] [DEP:T16] FR-2.3: `adapters/redis/idempotency.go` — 实现 kernel/idempotency.Checker (SET NX + TTL)
- [x] T19 [P] [DEP:T16] FR-2.4: `adapters/redis/cache.go` — Get/Set/Delete + TTL + JSON 泛型
- [x] T20 [P] 单元测试: `adapters/redis/*_test.go` (mock redis)

### RabbitMQ Adapter

- [x] T21 [P] FR-5.1: `adapters/rabbitmq/connection.go` — AMQP URL + auto-reconnect + channel 池 + Health()。ref: Watermill watermill-amqp
- [x] T22 [S] [DEP:T21] FR-5.2: `adapters/rabbitmq/publisher.go` — 实现 outbox.Publisher + confirm mode
- [x] T23 [S] [DEP:T21] FR-5.3: `adapters/rabbitmq/subscriber.go` — 实现 outbox.Subscriber + SubscriberConfig(QueueName, PrefetchCount)
- [x] T24 [S] [DEP:T21,T18] FR-5.4: `adapters/rabbitmq/consumer_base.go` — ConsumerBase + idempotency.Checker + 3x retry + DLQ
- [x] T25 [S] [DEP:T24] FR-5.5: DLQ 可观测 — slog 日志(event_id, topic, error, retry_count) + 计数
- [x] T26 [P] 单元测试: `adapters/rabbitmq/*_test.go`

## Wave 2: 应用 Adapter + Outbox 链路

### PostgreSQL Outbox

- [x] T27 [S] [DEP:T11,T13] FR-1.4: `adapters/postgres/outbox_writer.go` — 实现 outbox.Writer + context-embedded tx + fail-fast ERR_ADAPTER_NO_TX
- [x] T28 [S] [DEP:T27,T22] FR-1.5: `adapters/postgres/outbox_relay.go` — 实现 outbox.Relay + worker.Worker + SKIP LOCKED + batch 100 + poll 1s + cleanup 72h
- [x] T29 [P] 单元测试: outbox_writer_test.go + outbox_relay_test.go

### OIDC Adapter

- [x] T30 [P] FR-3.1: `adapters/oidc/provider.go` — OIDC Discovery + metadata 缓存。ref: coreos/go-oidc
- [x] T31 [P] [DEP:T30] FR-3.2: `adapters/oidc/token.go` — ExchangeCode
- [x] T32 [P] [DEP:T30] FR-3.3: `adapters/oidc/verifier.go` — JWKS + kid rotation + RS256
- [x] T33 [P] [DEP:T30] FR-3.4: `adapters/oidc/userinfo.go` — UserInfo
- [x] T34 [P] 单元测试: `adapters/oidc/*_test.go`

### S3 Adapter

- [x] T35 [P] FR-4.1: `adapters/s3/client.go` — S3/MinIO endpoint + credentials + Health()。ref: minio/minio-go
- [x] T36 [P] [DEP:T35] FR-4.2: 对象操作 Upload/Download/Delete
- [x] T37 [P] [DEP:T35] FR-4.3: `adapters/s3/presigned.go` — PresignedPut/PresignedGet
- [x] T38 [P] 单元测试: `adapters/s3/*_test.go`

### WebSocket Adapter

- [x] T39 [P] FR-6.1+6.2: `adapters/websocket/hub.go` — Hub + signal-first 模式。ref: nhooyr.io/websocket examples/chat
- [x] T40 [P] [DEP:T39] FR-6.3: `adapters/websocket/handler.go` — UpgradeHandler + origin 检查
- [x] T41 [P] [DEP:T39] FR-6.4: 心跳 ping/pong + 超时断开
- [x] T42 [P] 单元测试: `adapters/websocket/*_test.go`

### Cell PG Repository（最小 L2 证明）

- [x] T43 [S] [DEP:T10,T11] 决策4: `cells/audit-core/internal/adapters/postgres/audit_repo.go` — 实现 AuditRepository
- [x] T44 [S] [DEP:T10,T11] 决策4: `cells/config-core/internal/adapters/postgres/config_repo.go` — 实现 ConfigRepository
- [x] T45 [S] [DEP:T35] 决策2: `cells/audit-core/internal/adapters/s3archive/archive.go` — 封装 S3 Client 为 ArchiveStore
- [x] T46 [P] 单元测试: audit_repo_test.go + config_repo_test.go + archive_test.go

## Wave 3: Cell 集成 + 安全 + Tech-Debt

### Cell Outbox Writer 重构

- [x] T47 [S] [DEP:T27,T01] FR-10.2/KS-05: 重构 access-core 3 处 Publish → outbox.Writer.Write (session-login, session-logout, identity-manage) + WithOutboxWriter Option
- [x] T48 [S] [DEP:T27,T01] FR-10.2/KS-05: 重构 config-core 2 处 Publish → outbox.Writer.Write (config-write, config-publish) + WithOutboxWriter Option
- [x] T49 [S] [DEP:T27,T01] FR-10.2/KS-05: 重构 audit-core 2 处 Publish → outbox.Writer.Write (audit-append, audit-verify)
- [x] T50 [S] [DEP:T01,T22,T23] cmd/core-bundle main.go: 接线真实 adapter（postgres Pool + redis Client + rabbitmq Publisher/Subscriber → bootstrap）

### 安全加固 (FR-9)

- [x] T51 [P] FR-9.1: 密钥 → 环境变量 + fail-fast（SEC-03）
- [x] T52 [P] FR-9.2: JWT HS256 → RS256 迁移 + Cell.Init 注入公私钥对（SEC-04）
- [x] T53 [P] FR-9.3: RealIP trustedProxies 配置（SEC-06）
- [x] T54 [P] FR-9.4: ServiceToken +timestamp +5min 窗口（SEC-07）
- [x] T55 [P] FR-9.6: refresh token signing method 显式校验（SEC-09）
- [x] T56 [P] FR-9.7: refresh token rotation + reuse detection（SEC-10）
- [x] T57 [P] FR-9.8: API 端点认证中间件 — 公开/保护端点列表（SEC-11）
- [x] T58 [P] 安全加固单元测试（每条 FR-9 的 Given/When/Then）

### Tech-Debt P0+P1

- [x] T59 [P] FR-10.1: errcode 统一 — kernel 7 处 + cells 15 处 + eventbus (#27, #79, #73)
- [x] T60 [P] FR-10.3: 生命周期修复 — shutdown LIFO (#71), Worker.Stop 串行反序 (#74), Assembly.Stop 守卫 (#19), BaseCell 线程安全 (#50)
- [x] T61 [P] FR-10.5: 治理规则 — VERIFY-01 扩展 (#28), FMT projection (#29), 禁用字段名 (#41), Parser 空 id (#44)
- [x] T62 [P] FR-10.6: config watcher 集成 bootstrap (#20), eventbus 健康 (#21), TopicConfigChanged 常量 (#23)
- [x] T63 [P] FR-10.7: #60 configsubscribe unmarshal→DLQ + #61 auditappend→outbox.Writer [DEP:T24,T27]
- [x] T64 [P] FR-10.2: ARCH-04 BaseSlice 评估 + ARCH-06 goroutine shutdownCtx
- [x] T65 [P] FR-10 P2-High: #63 config handler chi 耦合, #65 statusRecorder 重复, #52 contract ID 格式, DX-02 doc.go

### 产品修复 (FR-11)

- [x] T66 [P] FR-11.1: 审计查询 time.Parse → 400 ERR_VALIDATION_INVALID_TIME_FORMAT
- [x] T67 [P] FR-11.2: RateLimit Retry-After 动态计算
- [x] T68 [P] FR-11.3: Update user PATCH 语义 + name/email/status 字段
- [x] T69 [P] FR-11.4: AC-8.2 签名算法文档对齐

## Wave 4: 测试 + 文档

### 集成测试 (FR-8, FR-14)

- [x] T70 [S] [DEP:T06,T10-T28] FR-8.1: 每 adapter 独立 integration_test.go (testcontainers)
- [x] T71 [S] [DEP:T27,T28,T22,T18] FR-8.2: Outbox 全链路 testcontainers 测试（写入→relay→publish→consume→idempotency）
- [x] T72 [S] [DEP:T24] FR-8.3: DLQ testcontainers 测试（消费失败→3x重试→DLQ→可读取）
- [x] T73 [S] [DEP:T43,T44,T47-T49] FR-8.4: Journey 集成测试 — J-audit-login-trail + J-config-hot-reload + J-config-rollback
- [x] T74 [S] [DEP:T50] FR-8.5: Assembly 组合集成测试（multi-adapter 注入 CoreAssembly 全生命周期）
- [x] T75 [S] [DEP:T47-T49] FR-10.4: handler httptest 补全 >= 80% + bootstrap >= 70% + router >= 80% + core-bundle 冒烟测试
- [x] T76 [S] FR-14.4: Phase 2 回归 `go test ./...` + kernel/ >= 90%

### 文档

- [x] T77 [P] FR-12.1: 6 个 adapter 包 doc.go + 导出类型注释 (godoc)
- [x] T78 [P] FR-12.2: 11 个 runtime 包补全 doc.go (#22)
- [x] T79 [P] FR-12.3: 集成测试指南（docker compose + go test -tags=integration）
- [x] T80 [P] FR-12.4: adapter 配置参考文档（env/YAML 参数 + 默认值表）
- [x] T81 [P] FR-12.5: Cell 开发指南更新 — contract test + 错误处理模式
- [x] T82 [P] FR-10.6 partial: P2-Low tech-debt doc.go 补全已在 T78 覆盖

### 元数据 + 验证

- [x] T83 [P] 更新 cell.yaml / slice.yaml / contract.yaml 反映 adapter 集成变更
- [x] T84 [S] `gocell validate` 零 error 验证
- [x] T85 [S] go build ./... + go vet ./... 全量验证

### Kernel Guardian 追加: 分层与合规验证

> 以下 5 条任务由 Kernel Guardian 审查 C-01~C-25 约束覆盖时追加，覆盖原任务清单的隐性缺口。

- [x] T86 [S] [DEP:T85] C-01~C-05 分层隔离 grep 验证 — 对 adapters/**/*.go 执行 `grep "github.com/ghbvf/gocell/cells"` 确认 0 匹配 (C-02); 对 kernel/**/*.go 执行 `grep "github.com/ghbvf/gocell/adapters\|github.com/ghbvf/gocell/runtime"` 确认 0 匹配 (C-03, C-05); 对 runtime/**/*.go 执行 `grep "github.com/ghbvf/gocell/adapters\|github.com/ghbvf/gocell/cells"` 确认 0 匹配 (C-04)。注: `go build` 不能捕获非循环的策略违规（如 adapters 导入 cells 可编译但违规），必须显式 grep。
- [x] T87 [S] [DEP:T10,T16,T21,T30,T35,T39] C-12 Adapter Close 验证 — 确认 6 个 adapter 包（postgres, redis, rabbitmq, oidc, s3, websocket）的所有连接持有 struct 均实现 `Close(ctx context.Context) error` 方法。验证方法: 每个 adapter 包至少 1 个 `var _ io.Closer` 或等效编译断言。
- [x] T88 [S] [DEP:T85] C-18 go.mod 依赖白名单检查 — `go.mod` diff 对比 Phase 2 基线，新增直接依赖必须限于白名单: pgx/v5, go-redis/v9, amqp091-go, nhooyr.io/websocket, testcontainers-go（+ 各自的间接依赖）。任何超出白名单的直接依赖需书面理由。
- [x] T89 [S] [DEP:T15,T20,T26,T29,T34,T38,T42] C-19+C-20 Adapter 错误规范验证 — (1) grep `errors\.New\|fmt\.Errorf` in adapters/**/*.go（排除 _test.go），所有非测试代码的对外错误必须使用 `pkg/errcode` 且前缀为 `ERR_ADAPTER_*`; (2) grep 裸露的 `pgx\.\|redis\.\|amqp\.` 错误类型在 return 语句中，确认全部被 `fmt.Errorf("...: %w", err)` 或 `errcode.Wrap` 包装。
- [x] T90 [S] [DEP:T83] C-25 禁用字段名检查 — grep Phase 3 所有新增/修改的 .go 和 .yaml 文件，搜索 `cellId|sliceId|contractId|assemblyId|ownedSlices|authoritativeData|producer[^s]|consumers[^(]|callsContracts|publishes[^(]|consumes[^(]`（作为 struct tag / YAML key / 变量名），确认 0 匹配。注: Go 变量名 `cellID`（大写 ID）合法，仅禁 camelCase 旧字段名。

---

## 统计

| Wave | 任务数 | 并行度 | 关键路径 |
|------|--------|--------|----------|
| Wave 0 | T01-T05 (5) | 4 并行 + T05 串行 | T01 (bootstrap 重构) |
| Wave 1 | T06-T26 (21) | 高并行（3 adapter 独立） | T10→T11→T13 (postgres chain) |
| Wave 2 | T27-T46 (20) | 中并行 | T27→T28 (outbox chain)→T43/T44 |
| Wave 3 | T47-T69 (23) | 高并行（安全+debt 独立） | T47-T50 (Cell 重构 + 接线) |
| Wave 4 | T70-T90 (21) | 中并行 | T71→T73→T74 (集成测试链), T86-T90 (KG 验证) |
| **总计** | **90 任务** | | |

---

## Kernel Guardian 审查确认

### C-01~C-25 覆盖映射

| 约束 | 覆盖任务 | 覆盖方式 |
|------|---------|---------|
| C-01 | T86 | 显式 grep adapters/ import 路径 |
| C-02 | T45 (ArchiveStore 间接层) + T86 | 架构设计 + grep 验证 |
| C-03 | T86 | 显式 grep kernel/ import 路径 |
| C-04 | T86 | 显式 grep runtime/ import 路径 |
| C-05 | T86 | 显式 grep kernel/ import 路径 |
| C-06 | T27 | outbox_writer.go 编译断言 |
| C-07 | T28 | outbox_relay.go 编译断言 |
| C-08 | T22 | publisher.go 编译断言 |
| C-09 | T23 | subscriber.go 编译断言 |
| C-10 | T18 | idempotency.go 编译断言 |
| C-11 | T02 + T85 | kernel doc 明确 + go build 编译 |
| C-12 | T87 | 显式 Close 方法审查 |
| C-13 | T60 (lifecycle LIFO) + T74 (assembly 集成测试) | 实现 + 测试 |
| C-14 | T28 | outbox_relay.go 实现 worker.Worker |
| C-15 | T76 | Phase 2 回归 + kernel/ >= 90% |
| C-16 | T83 + T84 | 元数据更新 + gocell validate |
| C-17 | 架构设计 | adapters 实现 kernel 接口，不走 contract |
| C-18 | T88 | 显式 go.mod diff 白名单检查 |
| C-19 | T59 + T89 | errcode 统一 + adapter 专项 grep |
| C-20 | T89 | adapter 驱动错误包装 grep |
| C-21 | T47 + T48 + T49 | Cell outbox.Writer 重构 |
| C-22 | T49 + T28 + T71 | audit-core 重构 + relay + 全链路测试 |
| C-23 | T76 | kernel/ >= 90% 覆盖率 |
| C-24 | T85 | go vet ./... 全量 |
| C-25 | T61 (治理规则) + T90 | 禁用字段名规则 + 显式 grep |

KG 审查通过: C-01~C-25 全部有对应任务覆盖（其中 5 条约束由本次审查追加 T86-T90 补齐）。日期: 2026-04-05
