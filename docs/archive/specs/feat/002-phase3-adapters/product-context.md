# Product Context -- Phase 3: Adapters

## 目标用户画像（Persona）

### P1: Cell 开发者（Go 后端工程师）-- 直接用户

日常负责业务 Cell 开发的 Go 工程师。Phase 2 为他提供了 in-memory 实现的 runtime 层和 3 个内建 Cell，但他无法将服务连接到真实基础设施（数据库、消息队列、对象存储）。他需要在不改变业务代码的前提下，将 Phase 2 的 in-memory 存储替换为 PostgreSQL 持久化、将 in-memory EventBus 替换为 RabbitMQ 消息传递、将硬编码认证替换为 OIDC 联合登录。

他的核心痛点是：Phase 2 的 in-memory 实现仅能用于单元测试和本地演示，无法评估框架在生产级场景下的可行性。他需要一行 `docker compose up` 即可启动包含 PostgreSQL + Redis + RabbitMQ + MinIO 的完整开发环境，并通过框架提供的 adapter 接口（而非自行封装 pgx/redis client）与基础设施交互。

Phase 3 对他的价值：6 个 adapter 包提供开箱即用的基础设施集成，每个 adapter 实现 kernel/ 或 runtime/ 已定义的接口（`outbox.Writer`、`outbox.Relay`、`outbox.Publisher`、`outbox.Subscriber`、`idempotency.Checker`），开发者只需在 assembly 层注入具体 adapter 实现，业务代码零改动。同时，Phase 2 遗留的安全问题（密钥硬编码、HS256 对称签名、端点无认证保护）在 adapter 基础设施就绪后得到修复，开发者不再需要在"先上安全还是先上持久化"之间权衡。

### P2: 平台架构师（技术负责人）-- 直接用户

负责制定微服务技术栈和基础设施选型的架构师。Phase 2 证明了 Cell 模型在运行时可行，但他无法验证关键架构承诺：outbox 事务性保证（L2 一致性）、跨 Cell 事件最终一致（L3）、分布式幂等消费、消息死信路由。这些能力在 in-memory 实现下无法真实验证。

他的核心痛点是：框架声称支持 L0-L4 一致性等级，但 Phase 2 的 in-memory EventBus 无法证明 L2 outbox 的事务原子性、L3 projection 的最终一致性。他需要看到 "写业务数据 + 写 outbox 在同一个 PostgreSQL 事务中" 的端到端证据，以及 RabbitMQ 消费失败后自动重试和死信路由的真实行为。

Phase 3 对他的价值：`adapters/postgres/` 的 TxManager + outbox.Writer 实现事务内写入，`adapters/rabbitmq/` 的 ConsumerBase 实现 DLQ + retry 机制，testcontainers 集成测试提供 outbox->relay->consume 全链路可验证证据。Phase 2 遗留的 80 条 tech-debt 中 72 条在本 Phase 系统性处理，架构师可评估框架的技术债务管理能力。

### P3: 团队 Tech Lead（交付负责人）-- 间接受益者

管理 3-8 人后端团队的 Tech Lead。他关注框架的集成测试策略是否能降低跨服务联调成本，以及 Docker Compose 开发环境是否能让新成员快速搭建本地完整栈。

Phase 3 对他的价值：Docker Compose 一键启动 + testcontainers 集成测试覆盖关键链路，新成员无需手动配置 PostgreSQL/Redis/RabbitMQ 即可运行全链路测试。Phase 2 的 Soft Gate Journey（J-audit-login-trail、J-config-hot-reload、J-config-rollback）在 adapter 就绪后获得真实端到端验证，团队对框架的信心从"单元测试通过"升级为"集成测试通过"。

### P4: 框架评估者（潜在采用者）-- 间接受益者

正在评估 Go 微服务框架的外部开发者或团队。他会查看 GoCell 的 adapter 层是否支持主流基础设施，接口设计是否遵循 Go 社区惯例（如 pgx/v5 风格），以及是否有可运行的集成测试证明框架不是"玩具"。

Phase 3 对他的价值：6 个一等 adapter 覆盖 Go 微服务最常见的基础设施需求，每个 adapter 有 godoc、有 testcontainers 集成测试、有 Docker Compose 示例环境。Phase 4 的 examples/ 将在这些 adapter 之上构建端到端示例，但 Phase 3 的 adapter 本身已足够让评估者理解框架的集成能力。

---

## 成功标准（可量化）

| # | 标准 | 量化指标 | 验证方式 |
|---|------|---------|---------|
| S1 | 6 个 adapter 全部实现并通过集成测试 | `go test ./adapters/... -tags=integration` 全部 PASS，每个 adapter 包至少 1 个 testcontainers 测试 | testcontainers 集成测试 |
| S2 | outbox 全链路端到端验证 | testcontainers 测试覆盖：业务写入 + outbox 写入在同一事务 -> relay 轮询 -> RabbitMQ publish -> consumer 消费 -> idempotency 去重，全链路 PASS | testcontainers 集成测试 |
| S3 | Phase 2 Soft Gate Journey 真实验证 | J-audit-login-trail（跨 Cell 事件：login -> audit 写入 -> hash chain 验证）、J-config-hot-reload（config 变更 -> event publish -> subscriber 重载）、J-config-rollback（回滚 -> event -> subscriber 重载）通过 adapter 端到端验证，不再依赖 in-memory stub | journey 集成测试 |
| S4 | adapters/ 层覆盖率达标 | adapters/ 每个包 go test 覆盖率 >= 80%（含单元测试 + 集成测试） | `go test -cover` |
| S5 | 零分层违反 | adapters/ 仅 import kernel/ + runtime/ + pkg/ + 外部依赖；不 import cells/；kernel/ 不 import adapters/；`go build ./...` 通过 | `go build` + 依赖分析 |
| S6 | Phase 2 安全类 tech-debt 清零 | P2-SEC-03（密钥环境变量化）、P2-SEC-04（JWT RS256 迁移）、P2-SEC-06（trustedProxies）、P2-SEC-07（ServiceToken timestamp）、P2-SEC-08（UUID 替换 UnixNano）、P2-SEC-09（signing method 校验）、P2-SEC-10（refresh rotation）、P2-SEC-11（认证中间件保护）全部 RESOLVED | 代码审查 + 测试 |
| S7 | Phase 2 tech-debt 系统性处理 | 80 条中至少 60 条 RESOLVED，剩余条目状态为 DEFERRED 且有明确理由和计划 Phase | tech-debt-registry.md 状态统计 |
| S8 | Docker Compose 开发环境一键启动 | `docker compose up -d` 启动 PostgreSQL + Redis + RabbitMQ + MinIO，30 秒内全部 healthy；`go test ./adapters/... -tags=integration` 在该环境下全部 PASS | 手动验证 + CI |
| S9 | 外部依赖可控 | Phase 3 新增直接依赖 5 个（`pgx/v5`、`go-redis/v9`、`amqp091-go`、`nhooyr.io/websocket`、`testcontainers-go`），不主动引入白名单外的直接依赖 | `go.mod` 审查 |
| S10 | kernel/ 层零退化 | kernel/ 包 go test 覆盖率维持 >= 90%，无新增编译错误，无新增 `go vet` 警告 | `go test -cover` + `go vet` |
| S11 | RabbitMQ DLQ 可观测 | 消费失败消息路由到死信队列，死信消息有计数指标或 slog 日志记录，可通过日志/指标确认死信存在 | testcontainers 集成测试 + 日志验证 |
| S12 | adapter godoc 完整 | 6 个 adapter 包均有 doc.go，每个导出类型/函数有注释，`go doc ./adapters/...` 输出可读 | 代码审查 |

---

## 范围边界

### 目标

Phase 3 的核心交付价值是将 GoCell 从"可运行但仅有 in-memory 实现的 Cell-native 框架"升级为"可连接真实基础设施的生产就绪框架"：

1. **一等 Adapter 实现** -- 交付 6 个外部系统适配器，每个 adapter 实现 kernel/ 层已定义的接口或提供 runtime/ 层所需的基础设施能力：
   - `adapters/postgres/`: 连接池（pgx/v5）、TxManager（事务管理）、Migrator（Schema 迁移）、outbox.Writer + outbox.Relay（实现 `kernel/outbox` 接口）
   - `adapters/redis/`: 连接（go-redis/v9）、分布式锁（用于 session refresh TOCTOU 等场景）、idempotency.Checker（实现 `kernel/idempotency` 接口）
   - `adapters/oidc/`: OIDC provider client、token exchange、JWKS 验证（替代 Phase 2 硬编码认证）
   - `adapters/s3/`: S3/MinIO client、presigned URL 生成（用于审计归档等场景）
   - `adapters/rabbitmq/`: Publisher + Consumer（amqp091-go）、ConsumerBase + DLQ + retry（实现 `kernel/outbox.Publisher` + `kernel/outbox.Subscriber`）
   - `adapters/websocket/`: WebSocket hub（nhooyr.io/websocket）、signal-first 推送模式

2. **L2 一致性承诺兑现** -- 通过 PostgreSQL outbox + RabbitMQ relay 实现事务内事件发布的端到端链路，证明框架的 L2 OutboxFact 一致性等级不是纸面概念。testcontainers 集成测试提供可重复的验证证据。

3. **Phase 2 安全加固** -- 在 adapter 基础设施就绪的前提下，系统性处理 8 条安全类 tech-debt：密钥从硬编码迁移到环境变量、JWT 从 HS256 迁移到 RS256、API 端点加装认证中间件、ServiceToken 协议加入 timestamp 防重放、Session/User ID 改用 crypto/rand UUID。

4. **Phase 2 tech-debt 系统性偿还** -- 80 条技术债务分 P0/P1/P2/P3 四层处理，约 72 条纳入本 Phase 修复范围，覆盖架构退化、测试缺失、编码规范违反、运维/DX 改进。8 条高风险重构项（TOCTOU 竞态、Service 接口返回类型、Session 生命周期模型等）DEFERRED 至 Phase 4。

5. **集成测试基础设施** -- 交付 Docker Compose 配置（PostgreSQL + Redis + RabbitMQ + MinIO）和 testcontainers 集成测试套件，使 Phase 2 的 Soft Gate Journey 获得真实端到端验证，同时为 Phase 4 的 examples/ 提供可复用的基础设施环境。

### 非目标声明

| 非目标 | 理由 |
|--------|------|
| 编写 examples/ 示例项目（sso-bff / todo-order / iot-device） | Phase 4 交付。Phase 3 聚焦 adapter 实现和集成测试，示例项目依赖 adapter 稳定后才有意义 |
| 生产级 Kubernetes 部署配置 | Phase 4+ 交付。Phase 3 的 Docker Compose 仅用于开发和集成测试环境，不面向生产部署 |
| 前端代码或 UI 界面 | GoCell 是纯后端框架，无前端交付物（role-roster.md 已声明前端开发者 OFF） |
| 性能基准测试和调优 | Phase 4 交付。Phase 3 目标是功能正确性和集成完整性，连接池大小、消费者并发数等性能参数使用合理默认值 |
| 多租户支持 | 未在 roadmap 中。adapter 设计不排斥多租户扩展，但 Phase 3 不实现租户隔离逻辑 |
| 可选 adapter（MySQL、Kafka、gRPC、搜索引擎、通知服务等） | 超出一等 adapter 范围。Phase 3 交付的 6 个 adapter 覆盖 roadmap 定义的一等基础设施，其他 adapter 按需在后续版本添加 |
| Phase 2 DEFERRED 的 8 条高风险重构项 | 需 adapter 持久化稳定后才能正确实现（如 Session refresh TOCTOU 需 Redis 分布式锁 + 持久化 session 稳定；JWT jti claim 需 Redis blacklist 稳定）。计划 Phase 4 处理 |
| OIDC provider 服务端实现 | `adapters/oidc/` 实现的是 OIDC client（连接外部 IdP），不实现 GoCell 作为 OIDC provider 的能力 |
| 消息 Schema 注册中心 | Phase 3 的 RabbitMQ adapter 使用 JSON 序列化 + 版本化 topic 名，不引入 Schema Registry（Avro/Protobuf） |
