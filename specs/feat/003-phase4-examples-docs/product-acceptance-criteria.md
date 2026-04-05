# Product Acceptance Criteria -- Phase 4: Examples + Documentation

> 产出角色: 产品经理
> 日期: 2026-04-06
> 输入: spec.md, tasks.md, product-context.md, kernel-constraints.md, decisions.md
> Phase 4 Gate: 未接触过 GoCell 的 Go 开发者按 README 指引在 30 分钟内创建第一个 Cell + Slice + Journey 并跑通

---

## 1. 优先级定义

| 优先级 | 定义 | Phase 4 具体标准 | 通过要求 |
|--------|------|-----------------|---------|
| P1（核心功能） | Phase 4 Gate（30 分钟首个 Cell）直接相关 + tech-debt must-fix | Gate 验证路径上的每个环节 | 100% PASS，不允许 FAIL 或 SKIP |
| P2（增强功能） | 提升评估者体验但非 Gate 阻塞 | SSO/IoT 示例、godoc、模板等 | 允许 SKIP 附理由，不允许 FAIL |
| P3（基础设施） | 模板/文档/CI 等支撑性工作 | 模板文件、CHANGELOG、scope cut 声明等 | 允许 SKIP |

---

## 2. 验收标准（AC）

### AC-1: RS256 默认化（FR-7.1）

来源: FR-7.1, Phase 3 tech-debt P3-TD-09 / P2-SEC-04, 决策 2
关联 Journey: 无（基础设施修复）
任务映射: T01, T02, T03

**AC-1.1** [P1] RS256 fail-fast
- Given: 调用 `auth.NewIssuer()` 时未提供 RSA private key
- When: 构造 JWTIssuer
- Then: 返回包含明确错误信息的 error（如 `ERR_AUTH_MISSING_KEY`），不降级到 HS256
- 验证方式: [go test] `go test ./runtime/auth/...` 中含覆盖此路径的测试用例

**AC-1.2** [P1] HS256 保留为显式 Deprecated Option
- Given: 开发者查看 `runtime/auth/jwt.go` 的 `WithSigningKey([]byte)` 函数
- When: 阅读 godoc 注释
- Then: 函数标注 `// Deprecated: Use WithRSAKeyPair instead.`
- 验证方式: [代码审查] 检查函数注释

**AC-1.3** [P1] 测试 key pair 工具可用
- Given: 测试代码需要 RSA key pair
- When: 调用 `auth.MustGenerateTestKeyPair()`
- Then: 返回有效的 `*rsa.PrivateKey` 和 `*rsa.PublicKey`，可用于 JWT 签发和验证
- 验证方式: [go test] `go test ./runtime/auth/...` 中含覆盖此函数的测试

---

### AC-2: access-core RS256 切换（FR-7.2）

来源: FR-7.2, 决策 2, KG-01
关联 Journey: 无（安全加固）
任务映射: T04, T05, T06, T07, T08

**AC-2.1** [P1] access-core 接受 JWTIssuer/JWTVerifier 接口
- Given: 构造 AccessCore
- When: 使用 `WithJWTIssuer(issuer)` + `WithJWTVerifier(verifier)` Option
- Then: Cell Init 成功，三个 slice（sessionlogin, sessionrefresh, sessionvalidate）使用注入的接口进行 JWT 操作
- 验证方式: [go test] `go test ./cells/access-core/...` 全 PASS

**AC-2.2** [P1] access-core 旧 API Deprecated
- Given: 开发者查看 `cells/access-core/cell.go` 的 `WithSigningKey([]byte)` Option
- When: 阅读 godoc 注释
- Then: 函数标注 `// Deprecated`
- 验证方式: [代码审查]

**AC-2.3** [P1] 全部测试使用 RSA key pair
- Given: access-core 全部单元测试（预估 60+ 个）
- When: 执行 `go test ./cells/access-core/...`
- Then: 全部 PASS，无 HS256 硬编码 key
- 验证方式: [go test] + [代码审查] 确认无 `[]byte("test-secret")` 残留

---

### AC-3: outboxWriter fail-fast（FR-7.3）

来源: FR-7.3, Phase 3 MF-3（产品评审）, 决策 3, KG-02
关联 Journey: 无（一致性保证加固）
任务映射: T09, T10, T11, T12, T13

**AC-3.1** [P1] L2+ Cell 缺 outboxWriter 时 Init 失败
- Given: Cell 声明 consistencyLevel >= L2（access-core / audit-core / config-core）
- When: Cell.Init 被调用且 outboxWriter == nil
- Then: 返回 `ERR_CELL_MISSING_OUTBOX` 错误，Cell 不注册到 Assembly
- 验证方式: [go test] 三个 Cell 各有测试覆盖此失败路径

**AC-3.2** [P1] L0/L1 Cell 不受影响
- Given: Cell 声明 consistencyLevel < L2
- When: Cell.Init 被调用且 outboxWriter == nil
- Then: Init 正常通过，不报错
- 验证方式: [go test] 或 [代码审查]

**AC-3.3** [P1] 现有测试注入 noop outboxWriter
- Given: access-core / audit-core / config-core 的现有测试
- When: 执行 `go test ./cells/...`
- Then: 全部 PASS（测试注入了 noop outboxWriter 以满足 Init 校验）
- 验证方式: [go test] `go test ./cells/...` 零 FAIL

---

### AC-4: S3 环境变量前缀修复（FR-7.4）

来源: FR-7.4, Phase 3 MF-3, 决策 6, KG-09
关联 Journey: 无
任务映射: T14, T15

**AC-4.1** [P1] GOCELL_S3_* 前缀优先
- Given: 环境变量 `GOCELL_S3_ENDPOINT=http://localhost:9000` 已设置
- When: 调用 `s3.ConfigFromEnv()`
- Then: Config.Endpoint 为 `http://localhost:9000`
- 验证方式: [go test] `go test ./adapters/s3/...` 中含覆盖新前缀的测试

**AC-4.2** [P2] 旧前缀 fallback + deprecation 警告
- Given: 仅设置旧前缀 `S3_ENDPOINT=http://localhost:9000`（无 GOCELL_S3_* 前缀）
- When: 调用 `s3.ConfigFromEnv()`
- Then: Config.Endpoint 为 `http://localhost:9000`，且输出 `slog.Warn` deprecation 警告
- 验证方式: [go test] 测试验证 fallback 行为

**AC-4.3** [P2] .env.example 同步更新
- Given: 开发者查看 `.env.example`
- When: 搜索 S3 相关配置
- Then: 变量名为 `GOCELL_S3_*` 前缀
- 验证方式: [代码审查]

---

### AC-5: Docker Compose + Deprecated 标注（FR-8.2, FR-8.3）

来源: FR-8.2, FR-8.3, Phase 3 P3-TD-05, P3-TD-08
任务映射: T16, T17

**AC-5.1** [P2] docker-compose 健康检查 start_period
- Given: `docker-compose.yml` 中 rabbitmq 和 minio 服务
- When: 查看健康检查配置
- Then: 包含 `start_period: 15s` 参数
- 验证方式: [代码审查]

**AC-5.2** [P2] WithEventBus Deprecated 标注
- Given: `runtime/bootstrap/bootstrap.go` 的 `WithEventBus` 函数
- When: 阅读 godoc 注释
- Then: 标注 `// Deprecated: Use WithPublisher and WithSubscriber instead.`
- 验证方式: [代码审查]

---

### AC-6: testcontainers 集成测试（FR-6）

来源: FR-6.1 ~ FR-6.6, Phase 3 MF-1, KG-04
关联 Journey: 无（验证基础设施）
任务映射: T18, T19, T20, T21, T22, T23, T24, T25, T26

**AC-6.1** [P1] testcontainers-go 依赖引入
- Given: `go.mod`
- When: 检查依赖列表
- Then: 包含 `github.com/testcontainers/testcontainers-go`
- 验证方式: [代码审查] 检查 go.mod

**AC-6.2** [P1] PostgreSQL 集成测试全 PASS
- Given: Docker 运行中
- When: 执行 `go test ./adapters/postgres/... -tags=integration`
- Then: Pool 连接、TxManager（commit/rollback/panic）、Migrator（Up/Down）、OutboxWriter（事务内写入）测试全 PASS
- 验证方式: [集成测试] testcontainers

**AC-6.3** [P1] Redis 集成测试全 PASS
- Given: Docker 运行中
- When: 执行 `go test ./adapters/redis/... -tags=integration`
- Then: Client 连接、DistLock、IdempotencyChecker 测试全 PASS
- 验证方式: [集成测试] testcontainers

**AC-6.4** [P1] RabbitMQ 集成测试全 PASS
- Given: Docker 运行中
- When: 执行 `go test ./adapters/rabbitmq/... -tags=integration`
- Then: Connection、Publisher、Subscriber、ConsumerBase DLQ 测试全 PASS
- 验证方式: [集成测试] testcontainers

**AC-6.5** [P1] Outbox 全链路测试 PASS
- Given: Docker 运行中（PostgreSQL + RabbitMQ + Redis 容器）
- When: 执行 `TestIntegration_OutboxFullChain`
- Then: business write + outbox write（同一事务）-> relay poll -> RabbitMQ publish -> consumer consume -> idempotency check 全链路 PASS
- 验证方式: [集成测试] testcontainers

**AC-6.6** [P1] postgres adapter 覆盖率 >= 80%
- Given: testcontainers 集成测试就绪
- When: 执行 `go test -cover -tags=integration ./adapters/postgres/...`
- Then: 覆盖率 >= 80%（Phase 3 为 46.6%）
- 验证方式: [go test] 覆盖率报告

**AC-6.7** [P1] build tag 隔离
- Given: 无 Docker 环境
- When: 执行 `go test ./adapters/...`（不带 -tags=integration）
- Then: 集成测试被跳过，不报 FAIL
- 验证方式: [go test] 确认无 Docker 时 `go test ./...` 全 PASS

---

### AC-7: todo-order 示例项目（FR-2）-- Phase 4 Gate 核心路径

来源: FR-2.1 ~ FR-2.8, US-1, US-2, SC-2, SC-4
关联 Journey: J-order-create
任务映射: T27, T28, T29, T30, T31, T32, T33, T34, T35, T36, T37, T38

**AC-7.1** [P1] 元数据合规
- Given: `examples/todo-order/cells/order-cell/cell.yaml` 和 `slice.yaml` 文件
- When: 执行 `gocell validate --root examples/todo-order`
- Then: 零 error。cell.yaml 含全部必填字段（id/type/consistencyLevel/owner/schema.primary/verify.smoke），type=core, consistencyLevel=L2
- 验证方式: [go build] `gocell validate`
- 手动验证步骤:
  1. 打开终端，进入项目根目录
  2. 执行 `gocell validate --root examples/todo-order`
  3. 确认输出 `0 error(s)`
  4. 打开 `examples/todo-order/cells/order-cell/cell.yaml`，逐项确认 6 个必填字段存在

**AC-7.2** [P1] 自定义 Cell 实现完整
- Given: todo-order 项目
- When: 检查目录结构
- Then: 包含 `cells/order-cell/cell.go`（实现 Cell 接口）+ `slices/order-create/`（handler + service）+ `slices/order-query/`（handler + service）+ `internal/domain/`（Order entity + Repository 接口）+ `internal/adapters/postgres/`（Repository 实现）
- 验证方式: [代码审查] 目录结构 + 接口实现

**AC-7.3** [P1] Contract 和 Journey 注册
- Given: todo-order 定义了 HTTP 和 event contract
- When: 检查 `contracts/` 目录
- Then: 存在 `contracts/http/order/v1/contract.yaml` 和 `contracts/event/order-created/v1/contract.yaml`。存在 `journeys/J-order-create.yaml`
- 验证方式: [代码审查] 文件存在性

**AC-7.4** [P1] Outbox 事件发布（L2 一致性演示）
- Given: todo-order main.go
- When: 审查 Assembly 配线代码
- Then: order-cell 通过 Option 注入 `postgres.NewTxManager(pool)` + `postgres.NewOutboxWriter(pool)`，order.Create 在 `TxManager.RunInTx` 中同时写入 order 记录和 outbox 条目
- 验证方式: [代码审查] 确认 L2 事务原子性模式

**AC-7.5** [P1] 编译通过
- Given: todo-order 项目
- When: 执行 `cd examples/todo-order && go build .`
- Then: 编译成功，零 error
- 验证方式: [go build]

**AC-7.6** [P1] docker-compose + README curl 命令
- Given: todo-order 项目
- When: 查看 README.md
- Then: 包含 (1) `docker compose up -d && go run .` 运行步骤 (2) CRUD curl 命令序列 (3) 每条 curl 附预期 HTTP 状态码和响应体示例 (4) 事件消费验证步骤（日志检查）
- 验证方式: [手动验证]
- 手动验证步骤:
  1. 打开 `examples/todo-order/README.md`
  2. 确认包含 `docker compose up -d` 和 `go run .` 启动命令
  3. 确认包含至少 2 条 curl 命令（POST 创建 + GET 查询）
  4. 确认每条 curl 命令下方有预期响应示例（含 HTTP 状态码）
  5. 确认有事件消费验证段落（如"查看日志确认 event.order.created consumed"）

**AC-7.7** [P1] 端到端运行验证
- Given: Docker 已安装，执行 `cd examples/todo-order && docker compose up -d && go run .`
- When: 执行 README 中的 curl 命令 `curl -X POST localhost:{port}/api/v1/orders -d '{"item":"test"}'`
- Then: 返回 HTTP 201 + JSON body 含 order ID
- 验证方式: [手动验证]
- 手动验证步骤:
  1. 确认 Docker Desktop 已运行
  2. 执行 `cd examples/todo-order && docker compose up -d`
  3. 等待所有容器健康（`docker compose ps` 全部 healthy）
  4. 执行 `go run .`，确认输出包含 HTTP 端口监听信息
  5. 按 README 中的 curl 命令执行 POST 请求
  6. 确认返回 HTTP 201 + JSON body 含 order ID
  7. 等待 3 秒，检查应用日志确认事件消费（`docker logs` 或终端输出含 `event.order.created`）
  8. 执行 `docker compose down` 清理

---

### AC-8: sso-bff 示例项目（FR-1）

来源: FR-1.1 ~ FR-1.7, US-4, SC-3
关联 Journey: 无（演示内建 Cell 组合）
任务映射: T39, T40, T41, T42

**AC-8.1** [P2] Assembly 配线正确
- Given: sso-bff main.go
- When: 审查代码
- Then: 注册 access-core + audit-core + config-core 三个 Cell，注入真实 adapter（`postgres.NewOutboxWriter(pool)` + `postgres.NewTxManager(pool)` + `redis.NewClient()` + `rabbitmq.NewPublisher()` + `rabbitmq.NewSubscriber()`），使用 RSA key pair（非 `WithSigningKey([]byte)`）
- 验证方式: [代码审查]

**AC-8.2** [P2] 编译通过
- Given: sso-bff 项目
- When: 执行 `cd examples/sso-bff && go build .`
- Then: 编译成功，零 error
- 验证方式: [go build]

**AC-8.3** [P2] curl 命令序列完整
- Given: sso-bff README.md
- When: 检查文档内容
- Then: 包含完整 curl 命令序列：login -> 获取 me -> refresh -> config update -> logout -> 审计查询。每条命令附预期响应
- 验证方式: [手动验证]
- 手动验证步骤:
  1. 打开 `examples/sso-bff/README.md`
  2. 确认包含至少 6 条 curl 命令覆盖 login/me/refresh/config/logout/audit
  3. 确认每条 curl 命令下方有预期响应（含 JWT token 示例、HTTP 状态码）
  4. 确认有 `docker compose up -d && go run .` 启动说明

**AC-8.4** [P2] SSO 端到端运行
- Given: Docker 已安装
- When: 执行 `cd examples/sso-bff && docker compose up -d && go run .` 并按 README curl 命令执行登录
- Then: 返回 JWT token
- 验证方式: [手动验证]
- 手动验证步骤:
  1. 执行 `cd examples/sso-bff && docker compose up -d`
  2. 等待容器健康
  3. 执行 `go run .`
  4. 按 README 执行 login curl 命令
  5. 确认返回含 JWT token 的 JSON 响应
  6. 按 README 执行 logout curl 命令
  7. 确认 session 吊销（返回 204 或等效成功状态）
  8. 执行 `docker compose down`

---

### AC-9: iot-device 示例项目（FR-3）

来源: FR-3.1 ~ FR-3.6, US-5, SC-5, 决策 1
关联 Journey: 无
任务映射: T43, T44, T45, T46, T47, T48, T49

**AC-9.1** [P2] L4 Cell 元数据合规
- Given: `examples/iot-device/cells/device-cell/cell.yaml`
- When: 检查元数据
- Then: type=edge, consistencyLevel=L4，含全部必填字段（id/type/consistencyLevel/owner/schema.primary/verify.smoke）
- 验证方式: [go build] `gocell validate --root examples/iot-device`

**AC-9.2** [P2] L4 不注入 outboxWriter
- Given: device-cell 的 cell.go
- When: 审查 Init 方法和 Option 列表
- Then: 不注入 outboxWriter，不 fallback 到 outbox pattern。使用命令队列模式（应用层实现）
- 验证方式: [代码审查]

**AC-9.3** [P2] WebSocket 推送实现
- Given: iot-device 项目
- When: 审查 main.go 和 device-status slice
- Then: 包含 WebSocket hub 集成，设备状态变更通过 WebSocket 推送
- 验证方式: [代码审查]

**AC-9.4** [P2] 编译通过
- Given: iot-device 项目
- When: 执行 `cd examples/iot-device && go build .`
- Then: 编译成功，零 error
- 验证方式: [go build]

**AC-9.5** [P2] README 含 curl + wscat 命令
- Given: iot-device README.md
- When: 检查内容
- Then: 包含设备注册 curl 命令 + 命令下发 curl 命令 + WebSocket 连接验证步骤（wscat 或等效）+ L4 disclaimer（v1.0 为应用层实现，v1.1 计划 kernel/command 一等支持）
- 验证方式: [手动验证]
- 手动验证步骤:
  1. 打开 `examples/iot-device/README.md`
  2. 确认包含设备注册 `POST /api/v1/devices` curl 命令
  3. 确认包含命令下发 `POST /api/v1/devices/{id}/commands` curl 命令
  4. 确认包含 WebSocket 连接说明（wscat 或浏览器 DevTools）
  5. 确认包含 L4 一致性说明段落，明确 v1.0 vs v1.1 边界

**AC-9.6** [P2] Contract YAML 注册
- Given: iot-device 定义了 command 和 event contract
- When: 检查 `contracts/` 目录
- Then: 存在 iot-device 相关契约定义（如 command.device.v1, event.device-status.v1 等）
- 验证方式: [代码审查] 文件存在性

---

### AC-10: README Getting Started（FR-4）

来源: FR-4.1 ~ FR-4.7, US-1, US-2, SC-1, SC-6, 决策 7
关联 Journey: 无
任务映射: T52, T53, T54, T55

**AC-10.1** [P1] 项目简介 + 架构概览
- Given: 框架评估者首次打开 `README.md`
- When: 阅读前两屏内容
- Then: 包含 (1) GoCell 一句话定义（Cell-native + 一致性保证 + 开箱即用）(2) ASCII 架构图标注 kernel/runtime/cells/adapters/examples 关系 (3) 核心概念简明解释（Cell, Slice, Contract, Assembly, Journey, L0-L4）
- 验证方式: [手动验证]
- 手动验证步骤:
  1. 打开 `README.md`
  2. 确认首段包含 GoCell 价值主张（一句话可理解"GoCell 是什么"）
  3. 确认有 ASCII 架构图（或等效文本图），标注至少 5 个层级
  4. 确认核心概念部分覆盖 Cell/Slice/Contract/Assembly/Journey/一致性等级
  5. 确认一致性等级 L0-L4 各有一句话解释 + 场景示例

**AC-10.2** [P1] 快速开始（5 分钟路径）
- Given: 评估者已安装 Go 1.22+ 和 Docker
- When: 按 README "快速开始" 部分操作
- Then: 步骤为 `git clone` -> `cd examples/todo-order` -> `docker compose up -d && go run .` -> curl 验证 HTTP 200/201。无需 `go get`（私有仓库限制，决策 7）。`go get` 作为独立子节附 GOPRIVATE/.netrc 说明
- 验证方式: [手动验证]
- 手动验证步骤:
  1. 确认 "快速开始" 章节存在
  2. 确认首条命令是 `git clone`（非 `go get`）
  3. 确认步骤数 <= 5 步
  4. 确认含 curl 验证命令 + 预期输出
  5. 确认 `go get` 场景在独立子节，附 GOPRIVATE 配置说明

**AC-10.3** [P1] 30 分钟教程（从零创建 Cell）
- Given: 评估者完成快速开始
- When: 按 "30 分钟教程" 部分从零创建自定义 Cell
- Then: 教程覆盖 (1) 创建 Cell 目录结构 + cell.yaml (2) 实现 Cell 接口 (3) 添加 Slice + handler + service (4) 注册到 Assembly (5) 编译运行 + HTTP 响应。步骤总数 <= 15 步，每步有明确预期输出，无需外部文档跳转
- 验证方式: [手动验证]
- 手动验证步骤:
  1. 确认 "30 分钟教程" 章节存在
  2. 逐步计数，确认步骤数 <= 15
  3. 确认每一步结尾有 "预期输出" 或 "你应该看到" 段落
  4. 确认无外部链接跳转（如 "详见 docs/xxx.md"），所有信息自包含
  5. 确认最终步骤以 HTTP 200 响应结束

**AC-10.4** [P1] 示例项目索引 + 目录结构
- Given: 评估者想了解全貌
- When: 阅读 README 示例索引 + 目录结构部分
- Then: 包含 3 个示例的简介 + 适用场景 + 链接。顶层目录用途一览表覆盖 kernel/cells/contracts/runtime/adapters/examples/cmd
- 验证方式: [代码审查]

---

### AC-11: 项目模板（FR-5）

来源: FR-5.1 ~ FR-5.6, US-6, SC-7, 决策 8
任务映射: T56, T57, T58, T59, T60, T61

**AC-11.1** [P3] 6 个模板文件存在且结构完整
- Given: `templates/` 目录
- When: 列出文件
- Then: 包含 `adr.md`, `cell-design.md`, `contract-review.md`, `runbook.md`, `postmortem.md`, `grafana-dashboard.json`，共 6 个文件
- 验证方式: [代码审查] `ls templates/`

**AC-11.2** [P3] 模板包含使用说明
- Given: 任一模板文件
- When: 阅读内容
- Then: 包含结构化占位符（如 `[填写]`、`<!-- 替换为... -->`）和使用说明注释
- 验证方式: [代码审查]

**AC-11.3** [P3] Grafana dashboard 使用 Prometheus 兼容查询
- Given: `templates/grafana-dashboard.json`
- When: 检查 datasource 和查询语法
- Then: 数据源标注为 "Prometheus-compatible"，面板标注为 placeholder（v1.0 无时序存储 adapter），查询使用 PromQL 语法
- 验证方式: [代码审查]

---

### AC-12: CI Workflow（FR-8.1）

来源: FR-8.1, Phase 3 P3-TD-03, SC-10, KG-03, KG-08, KG-10
任务映射: T50, T51

**AC-12.1** [P1] CI workflow 覆盖核心质量门控
- Given: `.github/workflows/ci.yml` 文件
- When: 检查 workflow 定义
- Then: PR 推送触发，包含以下步骤: (1) `go build ./...` (2) `go test ./...` (3) `go vet ./...` (4) `gocell validate` (5) `gocell validate --root examples/todo-order`（覆盖示例元数据）
- 验证方式: [代码审查]

**AC-12.2** [P1] 分层违反自动检测
- Given: CI workflow
- When: 检查分层检查步骤
- Then: 包含 `grep -r "cells/.*/internal" examples/` 和 `grep -r "adapters/.*/internal" examples/`，非零匹配时 CI FAIL
- 验证方式: [代码审查]

**AC-12.3** [P1] kernel 覆盖率自动门控
- Given: CI workflow
- When: 检查覆盖率步骤
- Then: kernel/ 覆盖率 < 90% 时 CI FAIL（自动化门控，非仅信息输出）
- 验证方式: [代码审查]

**AC-12.4** [P2] 集成测试独立 job
- Given: CI workflow
- When: 检查 job 定义
- Then: 集成测试作为独立 job（`-tags=integration`），与默认 `go test` 分离，需 Docker 服务（ubuntu-latest）
- 验证方式: [代码审查]

---

### AC-13: 文档完善（FR-9）

来源: FR-9.1 ~ FR-9.4, SC-6, SC-11, 决策 5
任务映射: T62, T63, T64, T65

**AC-13.1** [P2] 示例 godoc 可读
- Given: 3 个示例项目的导出类型和函数
- When: 执行 `go doc ./examples/...` 或查看源码注释
- Then: 每个导出类型/函数有 godoc 注释，注释可指导开发者理解使用方式
- 验证方式: [代码审查]

**AC-13.2** [P3] CHANGELOG Phase 4 更新
- Given: `CHANGELOG.md`
- When: 检查内容
- Then: 包含 Phase 4 全部变更：3 示例项目、RS256 默认化、outboxWriter fail-fast、testcontainers、CI workflow、6 模板、README
- 验证方式: [代码审查]

**AC-13.3** [P3] 能力清单更新 + v1.0 Scope Cut 声明
- Given: `docs/capability-inventory.md`
- When: 检查内容
- Then: (1) 反映 Phase 4 新增能力（examples、templates、CI）(2) 包含 "v1.0 Scope Cut" 附录，列出 7 个 kernel 子模块 + 4 个 runtime 子模块 + VictoriaMetrics adapter 为 v1.1 延迟
- 验证方式: [代码审查]

---

### AC-14: 全局测试验证（FR-10）

来源: FR-10.1 ~ FR-10.4, SC-2, SC-5, SC-6, SC-12, SC-13
任务映射: T66, T67, T68, T69, T70, T71, T72

**AC-14.1** [P1] go build 全通过
- Given: 项目根目录
- When: 执行 `go build ./...`
- Then: 零 error（含 examples/ 下全部代码）
- 验证方式: [go build]

**AC-14.2** [P1] go test 全通过（不含 integration）
- Given: 项目根目录
- When: 执行 `go test ./...`
- Then: 零 FAIL（集成测试因无 build tag 被跳过）
- 验证方式: [go test]

**AC-14.3** [P1] go vet 零警告
- Given: 项目根目录
- When: 执行 `go vet ./...`
- Then: 零输出
- 验证方式: [go test] `go vet`

**AC-14.4** [P1] gocell validate 零 error
- Given: 项目根目录
- When: 执行 `gocell validate`
- Then: 零 error
- 验证方式: [go build] `gocell validate`

**AC-14.5** [P1] 分层 grep 零违反
- Given: 项目根目录
- When: 执行 `grep -r "cells/.*/internal" examples/` 和 `grep -r "adapters/.*/internal" examples/`
- Then: 均为 0 匹配
- 验证方式: [手动验证]
- 手动验证步骤:
  1. 执行 `grep -r "cells/.*/internal" examples/`
  2. 确认输出为空（0 匹配）
  3. 执行 `grep -r "adapters/.*/internal" examples/`
  4. 确认输出为空（0 匹配）

**AC-14.6** [P1] kernel 覆盖率 >= 90%
- Given: 项目根目录
- When: 执行 `go test -cover ./kernel/...`
- Then: 每个包覆盖率 >= 90%（Phase 3 基线: assembly>=95%, cell>=99%, governance>=96%, metadata>=97%, registry>=100%, scaffold>=93%, slice>=94%）
- 验证方式: [go test] 覆盖率报告

**AC-14.7** [P1] postgres adapter 覆盖率 >= 80%（含 integration）
- Given: Docker 运行中
- When: 执行 `go test -cover -tags=integration ./adapters/postgres/...`
- Then: 覆盖率 >= 80%
- 验证方式: [go test] 覆盖率报告

---

### AC-15: 编码规范合规（NFR-2, NFR-3）

来源: NFR-2, NFR-3, C-28, C-29
任务映射: T88

**AC-15.1** [P1] examples/ 使用 errcode
- Given: examples/ 下全部 Go 源文件
- When: 执行 `grep -rn "errors\.New" examples/`
- Then: 零匹配（或仅 test 文件），确认使用 `pkg/errcode`
- 验证方式: [手动验证]
- 手动验证步骤:
  1. 执行 `grep -rn "errors\.New" examples/`
  2. 确认零匹配（test 文件中的匹配可接受但需逐条审查）

**AC-15.2** [P1] examples/ 使用 slog
- Given: examples/ 下全部 Go 源文件
- When: 执行 `grep -rn "fmt\.Println\|log\.Printf" examples/`
- Then: 零匹配
- 验证方式: [手动验证]
- 手动验证步骤:
  1. 执行 `grep -rn "fmt.Println\|log.Printf" examples/`
  2. 确认零匹配

---

### AC-16: kernel 稳定性（C-19, C-23, C-24）

来源: C-19, C-23, C-24, SC-13
任务映射: T75, T86

**AC-16.1** [P1] kernel/ 零代码修改
- Given: Phase 4 分支 vs develop 基线
- When: 执行 `git diff develop -- kernel/` (或 `src/kernel/`)
- Then: 零行非注释修改（允许纯注释/文档修改）
- 验证方式: [代码审查] git diff

**AC-16.2** [P1] kernel/cell.Cell 接口签名不变
- Given: Phase 4 分支 vs develop 基线
- When: 执行 `git diff develop -- src/kernel/cell/interfaces.go`（路径以实际为准）
- Then: 零 diff 或仅注释变更
- 验证方式: [代码审查] git diff

**AC-16.3** [P1] kernel/outbox 接口签名不变
- Given: Phase 4 分支 vs develop 基线
- When: 执行 `git diff develop -- src/kernel/outbox/outbox.go`（路径以实际为准）
- Then: 零 diff 或仅注释变更
- 验证方式: [代码审查] git diff

---

### AC-17: 30 分钟 Gate 验证（SC-1）

来源: SC-1, US-1, US-2, Phase 4 Gate 定义
任务映射: T73

**AC-17.1** [P1] 30 分钟首个 Cell -- 端到端 Gate
- Given: 一个未接触过 GoCell 的 Go 开发者（具备基础 Go 开发经验），已安装 Go 1.22+ 和 Docker
- When: 按 README Getting Started 指引，从 `git clone` 开始
- Then: 在 30 分钟内完成 (1) 运行 todo-order 示例看到 HTTP 201 (2) 按 30 分钟教程从零创建自定义 Cell + Slice (3) 注册到 Assembly (4) 编译运行看到 HTTP 200
- 验证方式: [手动验证]
- 手动验证步骤:
  1. 计时开始
  2. 执行 `git clone {repo-url} && cd gocell`
  3. 按 README "快速开始" 运行 todo-order 示例（预期 5 分钟内完成）
  4. 按 README "30 分钟教程" 逐步创建自定义 Cell
  5. 每完成一步记录时间点
  6. 教程最后一步执行 `go run .` 并 curl 验证 HTTP 200
  7. 计时结束，确认总耗时 <= 30 分钟
  8. 记录遇到的问题（如有）和对应的教程步骤编号

---

## 3. AC 到 Task 映射汇总

| AC | 优先级 | 任务 | FR/SC 来源 |
|----|--------|------|-----------|
| AC-1.1 | P1 | T01, T02, T03 | FR-7.1 |
| AC-1.2 | P1 | T02 | FR-7.1 |
| AC-1.3 | P1 | T01 | FR-7.1 |
| AC-2.1 | P1 | T04, T05, T06, T07 | FR-7.2 |
| AC-2.2 | P1 | T04 | FR-7.2 |
| AC-2.3 | P1 | T08 | FR-7.2 |
| AC-3.1 | P1 | T09, T10, T11, T12 | FR-7.3 |
| AC-3.2 | P1 | T10, T11, T12 | FR-7.3 |
| AC-3.3 | P1 | T13 | FR-7.3 |
| AC-4.1 | P1 | T14 | FR-7.4 |
| AC-4.2 | P2 | T14 | FR-7.4 |
| AC-4.3 | P2 | T15 | FR-7.4 |
| AC-5.1 | P2 | T16 | FR-8.2 |
| AC-5.2 | P2 | T17 | FR-8.3 |
| AC-6.1 | P1 | T18 | FR-6.1 |
| AC-6.2 | P1 | T19, T20, T21, T22 | FR-6.2 |
| AC-6.3 | P1 | T23 | FR-6.3 |
| AC-6.4 | P1 | T24 | FR-6.4 |
| AC-6.5 | P1 | T25 | FR-6.5 |
| AC-6.6 | P1 | T26 | FR-6.6 |
| AC-6.7 | P1 | T18-T25 | FR-10.2 |
| AC-7.1 | P1 | T27 | FR-2.1, FR-2.2 |
| AC-7.2 | P1 | T28, T29, T30, T31, T32 | FR-2.1, FR-2.3 |
| AC-7.3 | P1 | T33, T34 | FR-2.6, FR-2.7 |
| AC-7.4 | P1 | T28, T35 | FR-2.4 |
| AC-7.5 | P1 | T38 | FR-10.1 |
| AC-7.6 | P1 | T36, T37 | FR-2.8 |
| AC-7.7 | P1 | T37, T38 | US-1, SC-2 |
| AC-8.1 | P2 | T39 | FR-1.2 |
| AC-8.2 | P2 | T42 | FR-10.1 |
| AC-8.3 | P2 | T41 | FR-1.7 |
| AC-8.4 | P2 | T40, T41, T42 | US-4 |
| AC-9.1 | P2 | T43 | FR-3.1 |
| AC-9.2 | P2 | T44 | FR-3.1, KG-07 |
| AC-9.3 | P2 | T45, T47 | FR-3.5 |
| AC-9.4 | P2 | T49 | FR-10.1 |
| AC-9.5 | P2 | T48 | FR-3.6 |
| AC-9.6 | P2 | T87 | C-25 |
| AC-10.1 | P1 | T52 | FR-4.1, FR-4.2, FR-4.3 |
| AC-10.2 | P1 | T53 | FR-4.4, SC-1 |
| AC-10.3 | P1 | T54 | FR-4.5, SC-1 |
| AC-10.4 | P1 | T55 | FR-4.6, FR-4.7 |
| AC-11.1 | P3 | T56-T61 | FR-5, SC-7 |
| AC-11.2 | P3 | T56-T61 | FR-5 |
| AC-11.3 | P3 | T61 | FR-5.6 |
| AC-12.1 | P1 | T50 | FR-8.1, SC-10 |
| AC-12.2 | P1 | T50 | FR-8.1, KG-03 |
| AC-12.3 | P1 | T50 | FR-8.1, KG-10 |
| AC-12.4 | P2 | T51 | FR-8.1 |
| AC-13.1 | P2 | T62 | FR-9.1, SC-11 |
| AC-13.2 | P3 | T63 | FR-9.2 |
| AC-13.3 | P3 | T64, T65 | FR-9.3, FR-9.4 |
| AC-14.1 | P1 | T66 | FR-10.1, SC-2, SC-12 |
| AC-14.2 | P1 | T67 | FR-10.1 |
| AC-14.3 | P1 | T68 | NFR-4, SC-13 |
| AC-14.4 | P1 | T69 | FR-10.4, SC-6 |
| AC-14.5 | P1 | T70, T77 | FR-10.4, C-05, C-06 |
| AC-14.6 | P1 | T71 | FR-10.3, SC-13 |
| AC-14.7 | P1 | T72, T26 | FR-6.6, SC-4 |
| AC-15.1 | P1 | T88 | NFR-2, C-28 |
| AC-15.2 | P1 | T88 | NFR-3, C-29 |
| AC-16.1 | P1 | T75 | C-19, SC-13 |
| AC-16.2 | P1 | T86 | C-23 |
| AC-16.3 | P1 | T86 | C-24 |
| AC-17.1 | P1 | T73 | SC-1, US-1, US-2 |

---

## 4. 优先级统计

| 优先级 | AC 条数 | 通过要求 |
|--------|---------|---------|
| P1 | 40 | 100% PASS，零 FAIL 零 SKIP |
| P2 | 16 | 零 FAIL，SKIP 必须附理由 |
| P3 | 5 | 允许 SKIP |
| **总计** | **61** | |

---

## 5. 产品验收确认清单

- [ ] 产品上下文已定义（4 persona + 13 成功标准）-- product-context.md
- [ ] 验收标准已分级（P1: 40 条 / P2: 16 条 / P3: 5 条）
- [ ] P1 验收标准 = 100% PASS
- [ ] P2 无 FAIL（SKIP 必须附理由）
- [ ] 产品评审报告无红色维度（7 维度）
- [ ] 30 分钟 Gate 手动验证 PASS（AC-17.1）

全部通过 -> **产品 PASS**；否则 -> **产品 FAIL**（列出未达标项 + 修复建议）

---

## 6. 关键产品风险标注

| 风险 | 关联 AC | 影响 | 缓解 |
|------|---------|------|------|
| [验收标准缺失] 30 分钟 Gate 仅能手动验证，无法自动回归 | AC-17.1 | 后续 Phase 无法持续验证此标准 | 决策 R-06 已延迟到 v1.1 的 CI 自动化代理指标 |
| [开发者体验] 教程步骤跳转外部文档会打断心流 | AC-10.3 | 评估者中途放弃 | 教程自包含，禁止外部跳转（AC-10.3 验收条件已约束） |
| [兼容性风险] RS256 默认化是 Breaking Change | AC-1.1, AC-2.1 | 已使用 HS256 的消费者需迁移 | WithSigningKey 保留 Deprecated（AC-1.2），提供迁移路径 |
| [兼容性风险] outboxWriter fail-fast 破坏测试 | AC-3.1, AC-3.3 | 60+ 测试需更新 | T13 统一注入 noop outboxWriter |
| [范围偏移] iot-device L4 无 kernel 一等支持 | AC-9.2, AC-9.5 | 评估者可能认为 L4 "不完整" | README disclaimer 明确 v1.0/v1.1 边界（AC-9.5） |

---

*Generated by Product Manager on 2026-04-06*
*Input: spec.md + tasks.md + product-context.md + kernel-constraints.md + decisions.md*
