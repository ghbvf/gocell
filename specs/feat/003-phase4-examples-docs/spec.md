# Spec — Phase 4: Examples + Documentation

> 分支: `feat/003-phase4-examples-docs`
> 输入: phase-charter.md, product-context.md, roadmap Phase 4, Phase 3 tech-debt.md (12 条), Phase 3 kernel-review-report.md (3 条 must-fix), Phase 3 product-review-report.md (3 条 must-fix)

---

## 1. 概述

Phase 4 交付 3 个端到端示例项目、README Getting Started、6 个项目模板，并关闭 Phase 3 遗留的验证缺口（testcontainers 集成测试、RS256 安全迁移、outboxWriter fail-fast、S3 环境变量、CI 工作流）。

交付后 GoCell 从"可编译可运行但缺少使用指引的框架"升级为"可评估可采纳的完整框架产品"。Phase 4 Gate：一个未接触过 GoCell 的 Go 开发者按 README 指引在 30 分钟内创建第一个 cell + slice + journey 并跑通。

---

## 2. 功能需求 (FR)

### FR-1: SSO-BFF 示例项目 (`examples/sso-bff/`)

系统必须提供一个可独立运行的 SSO BFF 示例项目，演示 GoCell 内建 Cell 的组合使用：

| 子模块 | 说明 |
|--------|------|
| FR-1.1 项目结构 | 独立 `main.go` + `go.mod`（或引用根 module）+ `docker-compose.yml`（PostgreSQL + Redis + RabbitMQ）+ `README.md` |
| FR-1.2 Assembly 注册 | 注册 access-core + audit-core + config-core 三个内建 Cell 到单个 Assembly，注入 `postgres.NewOutboxWriter(pool)` + `postgres.NewTxManager(pool)` + `redis.NewClient()` + `rabbitmq.NewPublisher()` + `rabbitmq.NewSubscriber()` 等真实 adapter（不使用 nil 或 in-memory fallback，展示生产模式配线）(决策 KG-05) |
| FR-1.3 密码登录流程 | 演示：`POST /api/v1/auth/login` → session 创建 → JWT 签发 → `event.session.created` 发布（outbox） |
| FR-1.4 Session 管理 | 演示：`POST /api/v1/auth/refresh` → token 刷新 → 新 JWT；`POST /api/v1/auth/logout` → session 吊销 → `event.session.revoked` 发布 |
| FR-1.5 审计追踪 | 演示：login/logout 事件被 audit-core 消费，写入审计日志，HMAC-SHA256 hash chain 验证 |
| FR-1.6 配置热更新 | 演示：`PUT /api/v1/config/{key}` → config 变更 → `event.config.changed` → subscriber 重载 |
| FR-1.7 curl 命令文档 | README.md 包含完整 curl 命令序列（login → 获取 me → refresh → config update → logout → 审计查询），每条命令附预期响应 |

### FR-2: Todo-Order 示例项目 (`examples/todo-order/`)

系统必须提供一个可独立运行的自定义 Cell 示例项目，作为"从零创建业务 Cell"的 golden path：

| 子模块 | 说明 |
|--------|------|
| FR-2.1 自定义 Cell 定义 | `order-cell` 包含 `cell.yaml`（type: core, consistencyLevel: L2）+ Cell 接口实现 + 2 个 Slice（order-create, order-query）|
| FR-2.2 目录结构示范 | 遵循 CLAUDE.md 约定：`cells/order-cell/cell.yaml`、`cells/order-cell/slices/order-create/`、`cells/order-cell/internal/domain/`、`cells/order-cell/internal/ports/`。元数据文件必须通过 `gocell validate` 零 error（决策 KG-06） |
| FR-2.3 handler→service→repository 接线 | 演示 HTTP handler → service（业务逻辑）→ repository（数据访问）的三层结构，repository 实现 PostgreSQL adapter |
| FR-2.4 Outbox 事件发布 | 演示 L2 一致性：`order.Create` 在 TxManager.RunInTx 中同时写入 order 记录和 outbox 条目，确保事务原子性。main.go 通过 `postgres.NewTxManager(pool)` 注入 TxManager 到 order-cell Option（决策 A-05） |
| FR-2.5 RabbitMQ 消费 | 演示 event 消费：`event.order.created` 被消费者处理（如发送通知），使用 ConsumerBase + 幂等检查 |
| FR-2.6 Contract 定义 | 示例 contract YAML：`http.order.v1`（CRUD endpoints）、`event.order.created.v1`（event contract） |
| FR-2.7 Journey 定义 | 示例 Journey YAML：`J-order-create`（创建订单 → 事件发布 → 消费者处理） |
| FR-2.8 curl 命令文档 | README.md 包含 CRUD curl 命令 + 事件消费验证步骤 |

### FR-3: IoT-Device 示例项目 (`examples/iot-device/`)

系统必须提供一个可独立运行的 L4 设备管理示例项目，演示高延迟一致性模式：

| 子模块 | 说明 |
|--------|------|
| FR-3.1 自定义 Cell 定义 | `device-cell` 包含 `cell.yaml`（type: edge, consistencyLevel: L4）+ Cell 接口实现 + 3 个 Slice（device-register, device-command, device-status）。**注意**: L4 使用命令队列模式而非 outbox pattern（L2），device-cell 不注入 outboxWriter（决策 KG-07）。v1.0 的 L4 命令原语为应用层实现，框架 kernel/command 一等支持计划 v1.1（决策 1） |
| FR-3.2 设备注册 | 演示：`POST /api/v1/devices` → 设备注册 → 证书生成 → `event.device.registered` |
| FR-3.3 命令下发 | 演示：`POST /api/v1/devices/{id}/commands` → 命令入队 → 设备轮询取命令（L4 高延迟模式） |
| FR-3.4 回执确认 | 演示：设备上报命令执行结果 → `event.device.command.ack` → 状态更新 |
| FR-3.5 WebSocket 推送 | 演示：设备状态变更通过 WebSocket hub 实时推送给管理端 |
| FR-3.6 curl + wscat 文档 | README.md 包含设备注册/命令下发的 curl 命令 + WebSocket 连接验证步骤 |

### FR-4: README Getting Started

系统必须提供框架级 README.md，作为开发者首次接触 GoCell 的入口：

| 子模块 | 说明 |
|--------|------|
| FR-4.1 项目简介 | GoCell 一句话定义 + 核心价值（Cell-native 治理 + 一致性保证 + 开箱即用） |
| FR-4.2 架构概览 | 分层架构图（text/ASCII），标注 kernel/runtime/cells/adapters/examples 关系 |
| FR-4.3 核心概念 | Cell、Slice、Contract、Assembly、Journey、一致性等级（L0-L4）的简明解释 |
| FR-4.4 快速开始（5 分钟） | `git clone` + `cd examples/todo-order` + `docker compose up -d && go run .` + curl 验证 HTTP 200（决策 7：私有仓库不适合 go get 作为首次体验路径）。`go get` 场景作为独立子节"已有项目集成"，附 GOPRIVATE/.netrc 认证配置说明 |
| FR-4.5 教程（30 分钟） | 从零创建自定义 Cell + Slice + Contract + Assembly，注册到 Assembly，编译运行 |
| FR-4.6 示例项目索引 | 3 个示例的简介 + 链接 + 适用场景 |
| FR-4.7 目录结构说明 | 顶层目录用途一览表（kernel/、cells/、contracts/、runtime/、adapters/、examples/、cmd/） |

### FR-5: 项目模板 (`templates/`)

系统必须提供 6 个项目模板，降低团队协作规范成本：

| 子模块 | 说明 |
|--------|------|
| FR-5.1 ADR 模板 | `templates/adr.md` — Architecture Decision Record，含 Context/Decision/Consequences 结构 |
| FR-5.2 Cell Design 模板 | `templates/cell-design.md` — Cell 设计文档，含 cell.yaml 字段说明、Slice 划分理由、Contract 清单、一致性等级选择理由 |
| FR-5.3 Contract Review 模板 | `templates/contract-review.md` — Contract 审查清单，含 schema 兼容性、角色匹配、一致性约束检查项 |
| FR-5.4 Runbook 模板 | `templates/runbook.md` — 运维手册，含 Cell 健康检查、常见故障排查、回滚步骤 |
| FR-5.5 Postmortem 模板 | `templates/postmortem.md` — 事故复盘，含 Timeline/Impact/RootCause/ActionItems 结构 |
| FR-5.6 Grafana Dashboard 模板 | `templates/grafana-dashboard.json` — Grafana dashboard JSON，含 Cell 健康面板、outbox lag 面板、HTTP 延迟面板。数据源使用 Prometheus 兼容查询语法（兼容 InMemoryCollector / 未来 VictoriaMetrics adapter），面板标注为 placeholder（v1.0 无时序存储 adapter）（决策 8） |

### FR-6: Phase 3 Testcontainers 集成测试补全

系统必须将 Phase 3 的 `t.Skip` 集成测试 stub 升级为真实 testcontainers 测试：

| 子模块 | 说明 |
|--------|------|
| FR-6.1 testcontainers-go 引入 | `go.mod` 添加 `github.com/testcontainers/testcontainers-go` 依赖 |
| FR-6.2 PostgreSQL 集成测试 | `adapters/postgres/integration_test.go` — 测试 Pool 连接、TxManager RunInTx（含 commit/rollback/panic）、Migrator Up/Down、OutboxWriter 事务内写入 |
| FR-6.3 Redis 集成测试 | `adapters/redis/integration_test.go` — 测试 Client 连接、DistLock 加锁/释放、IdempotencyChecker IsProcessed/MarkProcessed |
| FR-6.4 RabbitMQ 集成测试 | `adapters/rabbitmq/integration_test.go` — 测试 Connection 建立、Publisher Publish、Subscriber Subscribe + ACK、ConsumerBase DLQ |
| FR-6.5 Outbox 全链路测试 | `TestIntegration_OutboxFullChain` — 验证 business write + outbox write（同一事务）→ relay poll → RabbitMQ publish → consumer consume → idempotency check |
| FR-6.6 postgres 覆盖率 ≥80% | 通过 testcontainers 覆盖 Pool/TxManager/Migrator 真实路径，提升 postgres adapter 覆盖率从 46.6% 至 ≥80% |

### FR-7: 安全加固完成

系统必须关闭 Phase 2-3 遗留的安全 tech-debt：

| 子模块 | 说明 |
|--------|------|
| FR-7.1 RS256 默认化 | `runtime/auth/jwt.go` 的 `NewIssuer`/`NewVerifier` 默认使用 RS256。无 RSA key pair 时 fail-fast（返回错误，不降级 HS256）。HS256 保留为显式 Option（`WithHS256(secret)`）用于测试场景。**Breaking API change**（决策 2）：`WithSigningKey([]byte)` 标记 `// Deprecated`，新增 `WithRSAKeyPair(priv *rsa.PrivateKey, pub *rsa.PublicKey)`。提供 `auth.MustGenerateTestKeyPair() (*rsa.PrivateKey, *rsa.PublicKey)` helper 用于测试迁移 |
| FR-7.2 access-core 切换 | access-core 三个 slice（sessionlogin, sessionrefresh, sessionvalidate）构造函数从 `signingKey []byte` 改为接受 `auth.JWTIssuer`/`auth.JWTVerifier` 接口。`AccessCore` 新增 `WithJWTIssuer(auth.JWTIssuer)` + `WithJWTVerifier(auth.JWTVerifier)` Option。单元测试使用 `auth.MustGenerateTestKeyPair()` 生成 RSA test key pair（决策 2） |
| FR-7.3 outboxWriter fail-fast | 对声明 consistencyLevel >= L2 的 Cell，Cell.Init 阶段校验 outboxWriter != nil，缺失时返回 `ERR_CELL_MISSING_OUTBOX`。L0/L1 Cell 保留 publisher 直接路径。删除静默 fallback，添加 slog.Warn 作为过渡可观测性 |
| FR-7.4 S3 env prefix | `adapters/s3/config.go` 的 `ConfigFromEnv()` 优先读取 `GOCELL_S3_*` 前缀变量，fallback 读取旧 `S3_*` 前缀并输出 `slog.Warn` deprecation 警告。同步更新 `.env.example` 和 `client_test.go` 的 `t.Setenv` 调用（决策 6, KG-09） |

### FR-8: DevOps — CI 工作流 + Docker Compose 修复

系统必须建立持续集成基础设施：

| 子模块 | 说明 |
|--------|------|
| FR-8.1 CI workflow | `.github/workflows/ci.yml` — PR 推送触发，执行 `go build ./...`、`go test ./...`、`go vet ./...`、`gocell validate`、`gocell validate --root examples/todo-order`（覆盖示例元数据 KG-08）、`grep -r "cells/.*/internal" examples/` 分层违反检查（KG-03）、kernel/ 覆盖率 ≥90% 自动门控（KG-10）。集成测试作为独立 job（`-tags=integration`），需 Docker 服务 |
| FR-8.2 docker-compose start_period | 为 rabbitmq 和 minio 服务添加 `start_period: 15s` 健康检查参数 |
| FR-8.3 WithEventBus Deprecated | `runtime/bootstrap/bootstrap.go` 的 `WithEventBus` 函数添加 `// Deprecated: Use WithPublisher and WithSubscriber instead.` 注释 |

### FR-9: 文档完善

系统必须确保文档体系完整：

| 子模块 | 说明 |
|--------|------|
| FR-9.1 示例 godoc | 3 个示例项目的每个导出类型/函数有 godoc 注释 |
| FR-9.2 CHANGELOG | 更新 CHANGELOG.md 记录 Phase 4 全部变更 |
| FR-9.3 能力清单 | 更新 `docs/capability-inventory.md` 反映 Phase 4 新增能力（examples、templates、CI） |
| FR-9.4 v1.0 Scope Cut 声明 | 更新 master-plan v1.0/v1.1 边界：7 个 kernel 子模块（webhook/reconcile/replay/rollback/consumed/trace/wrapper）+ 4 个 runtime 子模块（scheduler/retry/tls/keymanager）+ VictoriaMetrics adapter 正式记录为 v1.1 延迟。在 capability-inventory.md 增加"v1.0 Scope Cut"附录（决策 5, R-01, R-02） |

### FR-10: 测试需求

系统必须确保测试覆盖：

| 子模块 | 说明 |
|--------|------|
| FR-10.1 示例编译测试 | 3 个示例项目 `go build ./...` 全部通过 |
| FR-10.2 集成测试标签 | testcontainers 测试使用 `//go:build integration` 标签，默认 `go test` 不运行，`go test -tags=integration` 运行 |
| FR-10.3 kernel 无退化 | kernel/ 覆盖率维持 ≥90%，go vet 零警告 |
| FR-10.4 分层零违反 | `go build ./...` 通过 + 分层 grep 验证零匹配 |

---

## 3. 非功能需求 (NFR)

| # | 需求 | 说明 |
|---|------|------|
| NFR-1 | 分层隔离 | examples/ 可依赖所有层；其他层分层规则不退化 |
| NFR-2 | 错误规范 | 示例代码使用 `pkg/errcode`，不使用裸 `errors.New` |
| NFR-3 | 日志规范 | 示例代码使用 `slog` 结构化日志 |
| NFR-4 | go vet 零警告 | `go vet ./...` 零输出 |
| NFR-5 | 独立运行 | 每个示例 `docker compose up -d && go run .` 可独立启动 |
| NFR-6 | 配置模式 | 示例演示 Option pattern 注入 + 环境变量切换（in-memory vs real adapter） |

---

## 4. 用户场景与验收

### US-1: 框架评估者首次体验（Priority: P1）

框架评估者 clone GoCell 仓库后，按 README Getting Started 指引：
1. 运行 todo-order 示例（`cd examples/todo-order && docker compose up -d && go run .`）
2. 执行 curl 命令创建订单，看到 HTTP 201 响应
3. 查看日志确认事件发布和消费
4. 总耗时 ≤ 5 分钟

**Why this priority**: 这是 Phase 4 Gate 的核心验证路径。如果评估者 5 分钟内无法看到运行结果，后续 30 分钟教程无意义。

**Independent Test**: `docker compose up -d && go run .` + curl 命令返回预期响应。

**Acceptance Scenarios**:
1. **Given** 已安装 Go 1.22+ 和 Docker, **When** 执行 `cd examples/todo-order && docker compose up -d && go run .`, **Then** HTTP server 启动并监听端口
2. **Given** 服务运行中, **When** 执行 `curl -X POST /api/v1/orders -d '{"item":"test"}'`, **Then** 返回 HTTP 201 + JSON body 含 order ID
3. **Given** 订单已创建, **When** 等待 3 秒后检查应用日志（docker logs）, **Then** 日志包含 `event.order.created consumed` 或等效消费确认信息（决策 PM-01）

---

### US-2: 开发者从零创建自定义 Cell（Priority: P1）

Cell 开发者按 README 30 分钟教程：
1. 创建自定义 Cell 目录结构和 cell.yaml
2. 实现 Cell 接口
3. 添加 Slice + handler + service
4. 注册到 Assembly
5. 编译运行，看到 HTTP 响应
6. 总耗时 ≤ 30 分钟

**Why this priority**: 这是 Phase 4 Gate 的直接验证。

**Independent Test**: 按教程步骤从空目录到 HTTP 200。

**Acceptance Scenarios**:
1. **Given** 已完成快速开始, **When** 按 30 分钟教程创建新 Cell, **Then** `go build .` 编译通过
2. **Given** 新 Cell 编译通过, **When** 启动 Assembly, **Then** 新 Cell 注册成功 + HTTP handler 响应 200
3. **Given** 教程完整, **When** 统计教程步骤数, **Then** 总步骤 ≤ 15 步，每步有明确预期输出，无需外部文档跳转（决策 PM-04: 30 分钟通过步骤数量上限间接保证）

---

### US-3: 架构师验证 L2 一致性全链路（Priority: P1）

架构师运行 testcontainers 集成测试验证 outbox 全链路：

**Why this priority**: Phase 3 的核心承诺（L2 一致性）因 testcontainers 缺失而未验证，这是跨 Phase 延迟的关键债务。

**Independent Test**: `go test ./adapters/... -tags=integration` 全部 PASS。

**Acceptance Scenarios**:
1. **Given** Docker 运行中, **When** 执行 `go test ./adapters/postgres/... -tags=integration`, **Then** Pool/TxManager/Migrator/Outbox 测试全 PASS
2. **Given** Docker 运行中, **When** 执行全链路 outbox 测试, **Then** write→relay→publish→consume→idempotency PASS
3. **Given** `NewJWTIssuer` 被调用且未提供 RSA private key, **When** 构造 JWTIssuer, **Then** 返回包含明确错误信息的 error（不降级 HS256）（决策 PM-02）
4. **Given** Cell 声明 consistencyLevel >= L2, **When** Cell.Init 被调用且 outboxWriter == nil, **Then** 返回 `ERR_CELL_MISSING_OUTBOX` 错误，Cell 不注册到 Assembly（决策 PM-02）

---

### US-4: SSO 完整流程演示（Priority: P2）

评估者通过 sso-bff 示例体验完整 SSO 流程。

**Why this priority**: SSO 是最常见的框架评估场景。

**Independent Test**: sso-bff README 的 curl 命令序列全部返回预期响应。

**Acceptance Scenarios**:
1. **Given** sso-bff 运行中, **When** 执行 login curl 命令, **Then** 返回 JWT token
2. **Given** 已登录, **When** 执行 logout curl 命令, **Then** session 吊销 + audit 记录可查

---

### US-5: IoT 设备管理演示（Priority: P2）

评估者通过 iot-device 示例体验 L4 设备管理。

**Why this priority**: 演示框架的高级一致性能力。

**Independent Test**: iot-device README 的 curl + wscat 命令返回预期结果。

**Acceptance Scenarios**:
1. **Given** iot-device 运行中, **When** 注册设备 + 下发命令, **Then** 命令入队成功
2. **Given** WebSocket 连接建立, **When** 设备上报回执, **Then** WebSocket 推送状态更新

---

### US-6: 项目模板使用（Priority: P3）

Tech Lead 使用项目模板编写 ADR 和 Cell 设计文档。

**Why this priority**: 模板降低协作成本但非 Gate 关键路径。

**Independent Test**: 模板文件存在且结构完整。

**Acceptance Scenarios**:
1. **Given** 需要编写 ADR, **When** 复制 `templates/adr.md`, **Then** 结构清晰可填充

---

### Edge Cases

- 示例项目 Docker Compose 启动失败时，README 应包含故障排查提示
- 示例项目在无 Docker 环境下应能切换到 in-memory adapter 运行（降级模式）
- testcontainers 在 CI 环境中可能需要 DinD（Docker-in-Docker），CI workflow 应处理此场景
- README 教程中的 `go get` 路径指向私有仓库，需说明认证方式

---

## 5. 成功标准

| # | 标准 | 量化指标 | 验证方式 |
|---|------|---------|---------|
| SC-1 | 30 分钟首个 Cell | 未接触 GoCell 的 Go 开发者按 README 从 clone 到 HTTP 200 ≤ 30 分钟 | 手动验证 |
| SC-2 | 3 示例可编译可运行 | `go build ./examples/...` 全部 PASS | go build |
| SC-3 | testcontainers 集成测试 | `go test ./adapters/... -tags=integration` 全部 PASS（≥3 个 adapter） | testcontainers |
| SC-4 | postgres 覆盖率 | `go test -cover ./adapters/postgres/...` ≥ 80% | go test -cover |
| SC-5 | kernel 无退化 | kernel/ 覆盖率 ≥ 90%、go vet 零警告 | go test + go vet |
| SC-6 | 零分层违反 | `go build ./...` 通过 + 分层 grep 零匹配 | go build + grep |

---

## 6. 关键实体

- **Example Project**: 独立可运行的 Go 项目，包含 main.go、docker-compose.yml、README.md
- **Template**: Markdown/JSON 模板文件，包含结构化占位符和使用说明
- **Integration Test**: 使用 testcontainers 连接真实基础设施的 Go 测试
- **CI Workflow**: GitHub Actions YAML 配置，定义构建/测试/校验流水线

---

## 7. 假设

- Go 1.22+ 和 Docker 已安装作为开发环境前提
- 示例项目使用根目录 `go.mod`（module 路径 `github.com/ghbvf/gocell`），不创建独立 module
- testcontainers-go 支持当前 Go 版本（1.22+）
- CI 使用 GitHub Actions（项目托管在 GitHub）
- 框架评估者具备基础 Go 开发经验（了解 Go module、HTTP handler、context）
- 示例项目的 Docker Compose 配置复用根目录已有的服务定义（PostgreSQL/Redis/RabbitMQ/MinIO）
