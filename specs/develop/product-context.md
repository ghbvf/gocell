# Product Context — Phase 2: Runtime + Built-in Cells

## 目标用户画像（Persona）

### P1: Cell 开发者（Go 后端工程师）

日常在团队中负责业务 Cell 开发的 Go 工程师。他需要框架提供开箱即用的 HTTP 中间件、配置加载、认证鉴权等基础设施，从而专注于业务逻辑而非重复搭建脚手架。他的核心痛点是：每个新服务都要从零配置中间件链、健康检查、graceful shutdown，且各服务实现方式不一致，维护成本高。

Phase 2 对他的价值：`runtime/` 层提供标准化的 HTTP、配置、可观测性、认证模块，他只需声明 Cell/Slice 元数据并实现业务接口，框架自动完成启动编排、中间件注入和生命周期管理。

### P2: 平台架构师（技术负责人）

负责制定微服务技术栈和治理规范的架构师。他需要确保团队的所有服务遵循统一的分层约束、依赖规则和一致性等级语义，并且有内建的认证、审计、配置管理能力作为平台公共服务。他的核心痛点是：治理规则只存在于文档中，缺乏运行时强制执行的机制。

Phase 2 对他的价值：3 个内建 Cell（access-core / audit-core / config-core）提供平台级公共能力，开发团队无需自建认证和审计系统；kernel 层的元数据治理与 runtime 层的运行时约束形成端到端闭环。

### P3: 团队 Tech Lead（交付负责人）

管理 3-8 人后端团队的 Tech Lead，需要评估框架是否能降低新服务上线周期、减少跨服务联调成本。他的核心痛点是：新成员上手慢，每个服务的启动流程、配置方式、错误处理各不相同。

Phase 2 对他的价值：统一启动器（bootstrap）+ 标准化配置（config）+ 声明式 Journey 验收，使新服务的搭建和验证流程可预测、可复用，新成员按声明式模型即可理解服务边界和交互关系。

---

## 成功标准（可量化）

| # | 标准 | 量化指标 |
|---|------|---------|
| S1 | 3 个内建 Cell 可在 core-bundle assembly 中启动运行 | `gocell verify cell --id=access-core`、`audit-core`、`config-core` 全部 PASS |
| S2 | 8 条 Journey 端到端通过 | `gocell verify journey` 对 J-sso-login、J-session-refresh、J-session-logout、J-user-onboarding、J-account-lockout、J-audit-login-trail、J-config-hot-reload、J-config-rollback 全部 PASS |
| S3 | runtime/ 层覆盖率达标 | runtime/ 包 go test 覆盖率 >= 80% |
| S4 | cells/ 层覆盖率达标 | cells/ 包 go test 覆盖率 >= 80% |
| S5 | 零 kernel 层退化 | kernel/ 包 go test 覆盖率维持 >= 90%，无新增编译错误 |
| S6 | 从空项目到首个自定义 Cell 注册并启动 | 使用 bootstrap + runtime/http，10 分钟内完成（不含 adapter 接入） |
| S7 | 依赖规则零违反 | runtime/ 无 import cells/ 或 adapters/；cells/ 无 import adapters/；kernel/ 无 import runtime/ |
| S8 | 外部依赖可控 | Phase 2 新增外部依赖仅 `go-chi/chi/v5` 和 `golang.org/x/crypto`，不引入其他第三方库 |

---

## 范围边界

### 目标

Phase 2 的核心交付价值是将 GoCell 从"可编译的元数据治理框架"升级为"可运行的 Cell-native Go 框架"：

1. **运行时基础设施就绪** — 提供 HTTP 中间件链、健康检查、路由构建器、YAML/env 配置加载与热更新、统一启动器与优雅关闭、Prometheus/OTel/slog 可观测性、后台 worker、JWT+RBAC 认证鉴权，使开发者具备构建生产级服务的全部 runtime 能力。

2. **平台公共能力内建** — 交付 access-core（身份管理 + 会话全生命周期 + RBAC 授权判定）、audit-core（防篡改审计链 + 事件消费 + 归档）、config-core（配置 CRUD + 热更新事件 + Feature Flag），作为框架开箱即用的平台服务。

3. **端到端验收闭环** — 8 条 Journey 覆盖单 Cell 内操作（登录、刷新、登出、用户入职、账号锁定、配置热更新、配置回滚）和跨 Cell 场景（登录审计追踪），证明 Cell 间通过 contract 协作的模型在运行时真正可行。

4. **开发者体验一致化** — 统一启动流程（parse config -> init assembly -> start HTTP -> start workers -> graceful shutdown），开发者只需实现 Cell/Slice 接口并声明元数据，runtime 层自动编排。

### 非目标声明

| 非目标 | 理由 |
|--------|------|
| 实现 adapters/ 层（PostgreSQL、Redis、RabbitMQ、OIDC、S3、WebSocket） | Phase 3 交付，Phase 2 的 Cell 业务逻辑通过 kernel/ 定义的接口解耦，不依赖具体适配器实现 |
| 提供 Docker / docker-compose / CI/CD 部署配置 | Phase 3+ 交付，Phase 2 聚焦可编译可测试，不涉及容器化部署 |
| 编写 examples/ 示例项目（sso-bff / todo-order / iot-device） | Phase 4 交付，Phase 2 通过内建 Cell + Journey 验证框架能力 |
| 前端代码或 UI 界面 | GoCell 是纯后端框架，无前端交付物 |
| 对外发布或版本号管理 | Phase 2 仍为内部开发阶段，不面向外部用户发布 |
| 性能基准测试和调优 | Phase 2 目标是功能正确性，性能优化在功能稳定后进行 |
| 可选适配器（MySQL、Kafka、gRPC、搜索、通知等） | 超出 Phase 3 一等适配器范围，按需在后续版本添加 |
