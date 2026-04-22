# Product Context -- Phase 4: Examples + Documentation

## 目标用户画像（Persona）

### P4: 框架评估者（潜在采用者）-- 主要用户（PRIMARY）

正在评估 Go 微服务框架的外部开发者或技术决策者。他可能来自一个 3-15 人的后端团队，正在对比 GoCell 与 go-zero、Kratos、go-micro 等框架。他不会阅读 kernel/ 源码来理解框架能力，而是通过三个动作做出判断：（1）克隆仓库，运行示例项目，看到真实输出；（2）阅读 README Getting Started，评估 30 分钟内能否跑通第一个自定义 Cell；（3）浏览 examples/ 目录结构，判断框架是否覆盖自己的业务场景（SSO、CRUD+事件驱动、IoT/高延迟设备）。

他的核心痛点是：Phase 0-3 产出了 200+ Go 源文件、11 个 kernel 包、11 个 runtime 子包、6 个 adapter，但没有一个可运行的端到端示例。框架的 README 缺少 Getting Started 指引，`examples/` 目录为空。他无法在 30 分钟内从零构建一个 Cell 并看到 HTTP 响应，因此无法评估框架的实际可用性。他需要的不是 godoc 和 API 签名，而是"复制、粘贴、运行、看到结果"的体验。

Phase 4 对他的价值：3 个梯度示例覆盖从简单到复杂的真实场景。sso-bff 演示认证全流程（他最关心的"框架能不能做 SSO"）；todo-order 演示自定义 Cell + CRUD + 事件驱动（他最关心的"我的业务 Cell 怎么写"）；iot-device 演示 L4 高延迟设备管理（他最关心的"框架能不能处理复杂一致性场景"）。README Getting Started 提供从 `go get` 到第一个 Cell 运行的完整路径，30 分钟内可完成。

**为什么 P4 是 PRIMARY persona**：Phase 4 是框架补充计划的最终阶段。之前三个 Phase 构建了内核、运行时、适配器，但这些只是"能力"。Phase 4 的 examples/ 和文档是唯一直接面向框架外部受众的交付物——它们决定了一个潜在采用者是否会 `go get github.com/ghbvf/gocell`。如果评估者在 30 分钟内跑不通，框架的技术深度再强也不会被采用。

### P1: Cell 开发者（Go 后端工程师）-- 直接用户

日常负责业务 Cell 开发的 Go 工程师。Phase 2-3 为他提供了 runtime 层、3 个内建 Cell 和 6 个 adapter，但他缺乏"从零开始写一个自定义 Cell"的端到端参考。现有的 `docs/guides/cell-development-guide.md` 提供了代码片段，但没有可运行的完整项目。他需要一个 `examples/todo-order/` 这样的项目，展示如何定义 cell.yaml、实现 Cell 接口、注册路由、发布事件、注入 adapter——所有步骤在一个可编译可运行的 Go 项目中串联。

他的核心痛点是：阅读 cell-development-guide.md 后仍不确定 handler→service→repository→adapter 的接线方式，特别是 outbox.Writer 的注入时机和 TxManager 的使用模式。他需要一个"golden path"示例，而非分散在 20 个包中的 API 文档。

Phase 4 对他的价值：`examples/todo-order/` 展示自定义 Cell 从零到运行的完整路径，包括目录结构、cell.yaml 声明、Cell 接口实现、HTTP handler 注册、事件发布（outbox pattern）、adapter 注入（PostgreSQL + RabbitMQ）。他可以直接复制 todo-order 的结构来创建自己的业务 Cell。`examples/sso-bff/` 展示如何组合内建 Cell（access-core + audit-core + config-core）构建 BFF 服务。`examples/iot-device/` 展示 L4 一致性等级的实现模式。

### P2: 平台架构师（技术负责人）-- 直接用户

负责制定微服务技术栈和基础设施选型的架构师。Phase 2-3 证明了 Cell 模型在运行时可行且 adapter 层可连接真实基础设施，但他仍需要以下证据才能批准团队采用 GoCell：（1）3 个不同复杂度的示例项目证明框架的通用性（不只是 SSO 场景）；（2）templates/ 提供 ADR、runbook、postmortem 等工程模板，证明框架考虑了团队协作规范；（3）Phase 3 遗留的 15 条 tech-debt 中的 must-fix 项已关闭，证明框架的债务管理能力。

他的核心痛点是：框架声称支持 L0-L4 五级一致性，但 Phase 3 的 5 条 P1 验收标准因缺少 testcontainers 而 SKIP，S1/S2/S3 成功标准 NOT_VERIFIED。他需要看到集成测试在真实基础设施上通过的证据，才能信任框架的一致性承诺不是纸面概念。

Phase 4 对他的价值：testcontainers 集成测试补全（MF-1），postgres adapter 覆盖率提升到 >= 80%（MF-2），S3 环境变量前缀修复（MF-3），RS256 完全默认化（P3-TD-09），CI workflow 建立（P3-TD-03）。这些 must-fix 项的关闭为架构师提供了"框架质量可信"的信号。3 个示例项目的可运行性证明框架不是"玩具"而是"可生产使用的工程底座"。

### P3: 团队 Tech Lead（交付负责人）-- 间接受益者

管理 3-8 人后端团队的 Tech Lead。他关注框架能否降低新项目启动成本和新成员上手时间。他的核心痛点是：团队新成员加入后，需要多长时间才能理解 Cell 模型并交付第一个功能？如果框架缺少示例和模板，新成员只能通过阅读源码学习，上手周期可能超过 1 周。

Phase 4 对他的价值：README Getting Started 提供 30 分钟 onboarding 路径。`templates/` 提供 ADR、cell-design、contract-review、runbook、postmortem、Grafana dashboard 模板，团队可直接复用而非从零编写。3 个示例项目按复杂度递增（sso-bff: 组合内建 Cell → todo-order: 自定义 Cell + 事件 → iot-device: L4 设备管理），新成员可按需学习。

---

## 成功标准（可量化）

| # | 标准 | 量化指标 | 验证方式 |
|---|------|---------|---------|
| S1 | 30 分钟首个 Cell 可运行（Phase 4 Gate） | 一个未接触过 GoCell 的 Go 开发者，按 README Getting Started 指引，从 `git clone` 到第一个自定义 Cell 注册到 Assembly 并返回 HTTP 200 响应，总耗时 <= 30 分钟 | 手动验证（按 README 步骤执行） |
| S2 | 3 个示例项目全部可编译可运行 | `cd examples/sso-bff && go build .` / `cd examples/todo-order && go build .` / `cd examples/iot-device && go build .` 全部 PASS；每个示例有 README.md 说明运行步骤；`docker compose up -d && go run .` 可启动并响应 HTTP 请求 | `go build` + 手动运行验证 |
| S3 | sso-bff 示例覆盖 SSO 完整流程 | 示例演示：密码登录 -> JWT 签发 -> session refresh -> logout -> 审计记录写入 -> 配置热更新，涉及 access-core + audit-core + config-core 三个内建 Cell 协作 | 手动验证 + 示例 README 中的 curl 命令可执行 |
| S4 | todo-order 示例覆盖自定义 Cell + 事件驱动 | 示例演示：自定义 order Cell 的完整实现（cell.yaml + Cell 接口 + Slice + handler + service + repository）、CRUD 操作、outbox 事件发布、RabbitMQ 消费、幂等处理 | 手动验证 + 代码审查 |
| S5 | iot-device 示例覆盖 L4 设备管理 | 示例演示：设备注册、命令下发、回执确认、L4 DeviceLatent 一致性模式（高延迟闭环）、WebSocket 实时推送 | 手动验证 + 代码审查 |
| S6 | README Getting Started 完整性 | README.md 包含：项目简介、安装方式（`go get`）、快速开始（30 分钟路径）、架构概览图、3 个示例项目索引、API 文档链接、贡献指南链接 | 代码审查 |
| S7 | 项目模板全部交付 | `templates/` 目录包含 6 个模板：ADR、cell-design、contract-review、runbook、postmortem、Grafana dashboard。每个模板有使用说明注释 | 代码审查 |
| S8 | Phase 3 must-fix 项关闭 | MF-1（testcontainers 集成测试）: postgres + rabbitmq + redis 各至少 1 个 testcontainers 测试 PASS；MF-2（postgres 覆盖率 >= 80%）: `go test -cover ./adapters/postgres/...` >= 80%；MF-3（S3 env prefix）: `GOCELL_S3_*` 环境变量前缀统一 | testcontainers 测试 + `go test -cover` + 代码审查 |
| S9 | Phase 3 tech-debt 系统性处理 | 15 条 OPEN/PARTIAL tech-debt 中：must-fix 3 条 RESOLVED；P3-TD-09（RS256 默认化）RESOLVED；P3-TD-03（CI workflow）RESOLVED；其余标注 DEFERRED 或 RESOLVED 并更新 tech-debt-registry.md | tech-debt-registry.md 状态统计 |
| S10 | CI workflow 可用 | `.github/workflows/` 包含 CI 配置，覆盖 `go build`、`go test`、`go vet`、`gocell validate`；PR 推送触发 CI 并阻断失败 | CI pipeline 验证 |
| S11 | 示例项目 godoc 可读 | 3 个示例项目的每个导出类型/函数有注释，`go doc ./examples/...` 输出可指导开发者理解使用方式 | `go doc` + 代码审查 |
| S12 | 零分层违反 | examples/ 可以依赖所有层；其他层分层规则不退化；`go build ./...` 通过 | `go build` + 依赖分析 |
| S13 | kernel/ 层零退化 | kernel/ 包 go test 覆盖率维持 >= 90%，无新增编译错误，无新增 `go vet` 警告 | `go test -cover` + `go vet` |

---

## 范围边界

### 目标

Phase 4 的核心交付价值是将 GoCell 从"可编译可运行但缺少使用指引的 Cell-native 框架"升级为"可评估可采用的完整框架产品"。Phase 0-3 构建了全部技术能力（kernel + runtime + cells + adapters），Phase 4 将这些能力转化为开发者可感知的价值。

1. **3 个梯度示例项目** -- 覆盖从简单到复杂的真实场景，每个示例都是独立可运行的 Go 项目：

   - `examples/sso-bff/`（复杂度：中）: SSO 完整登录流程。组合 access-core + audit-core + config-core 三个内建 Cell，演示密码登录、JWT 签发、session 管理、审计追踪、配置热更新。使用 PostgreSQL + Redis + RabbitMQ adapter。这是"如何使用 GoCell 内建能力构建 BFF 服务"的参考实现。

   - `examples/todo-order/`（复杂度：中高）: CRUD + 事件驱动 + 自定义 Cell。演示开发者如何从零创建一个业务 Cell（order-cell），包括 cell.yaml 声明、Cell 接口实现、Slice 划分、HTTP handler、service 层、repository 接口、adapter 注入（PostgreSQL + RabbitMQ）、outbox 事件发布与消费、幂等处理。这是"如何用 GoCell 写自己的业务"的 golden path 示例。

   - `examples/iot-device/`（复杂度：高）: L4 设备管理。演示 L4 DeviceLatent 一致性模式——设备注册、命令下发、高延迟回执确认、WebSocket 实时状态推送。这是"GoCell 如何处理复杂一致性场景"的参考实现。

2. **README Getting Started** -- 框架的"前门"。从项目简介到 `go get` 安装到第一个 Cell 运行的完整路径。目标是一个未接触过 GoCell 的 Go 开发者在 30 分钟内完成 onboarding。包含架构概览图、核心概念解释（Cell/Slice/Contract/Assembly/Journey）、快速开始步骤、示例项目索引。

3. **项目模板** -- 6 个工程模板降低团队协作成本：
   - `templates/adr/`: Architecture Decision Record 模板
   - `templates/cell-design/`: Cell 设计文档模板（cell.yaml + 设计理由）
   - `templates/contract-review/`: Contract 审查清单
   - `templates/runbook/`: 运维手册模板
   - `templates/postmortem/`: 事故复盘模板
   - `templates/grafana/`: Grafana dashboard JSON 模板（Cell health + outbox lag）

4. **Phase 3 must-fix tech-debt 关闭** -- 3 条 must-fix + 关键 tech-debt 偿还：
   - MF-1: testcontainers 集成测试实现（postgres + rabbitmq + redis），填充 Phase 3 留下的 `t.Skip` stub，引入 testcontainers-go 到 go.mod
   - MF-2: postgres adapter 覆盖率从 46.6% 提升到 >= 80%，通过 testcontainers 覆盖 Pool/TxManager/Migrator 真实路径
   - MF-3: S3 adapter `ConfigFromEnv` 环境变量前缀统一为 `GOCELL_S3_*`
   - P3-TD-09: RS256 完全默认化（jwt.NewIssuer/NewVerifier 默认使用 RS256，HS256 降为显式 Option）
   - P3-TD-03: CI workflow（`.github/workflows/ci.yml`）
   - P3-TD-06: outboxWriter nil guard 添加 slog.Warn（fail-fast 模式）
   - P3-TD-05: docker-compose.yml 添加 `start_period` 健康检查参数

5. **示例项目基础设施** -- 每个示例项目包含独立的 `docker-compose.yml`（或引用根目录 compose）和 `README.md`，开发者可以独立运行任何一个示例而不需要理解框架全貌。

### 非目标声明

| 非目标 | 理由 |
|--------|------|
| 生产级 Kubernetes 部署配置 | 超出框架补充计划范围。Phase 4 的 Docker Compose 仅用于示例运行和集成测试，不面向生产部署。Kubernetes 配置是框架消费者按自己的基础设施定制的工作 |
| 前端代码或 UI 界面 | GoCell 是纯后端框架，无前端交付物。示例项目通过 curl 命令和 HTTP API 演示功能 |
| 性能基准测试和调优 | Phase 4 目标是功能完整性和开发者体验，性能基准在框架稳定后按需添加 |
| Optional adapter 实现（MySQL / Kafka / gRPC / SSE / search / notification / tenant） | master-plan 定义为 v1.1 交付。Phase 4 不实现 optional adapter 的具体代码，仅在示例中使用 Phase 3 已交付的 6 个一等 adapter |
| Optional adapter 接口桩 | master-plan Week 12 提及 "Optional adapter 接口留桩"，但考虑到 Phase 4 时间约束（Days 78-91）和 must-fix tech-debt 关闭的优先级，接口桩降为 DEFERRED。空接口定义对评估者无价值，不如将时间投入示例质量 |
| WinMDM 引用 GoCell 的 POC | master-plan Week 12 提及，但属于外部项目集成。Phase 4 聚焦框架自身的示例和文档完整性。WinMDM 集成由外部项目按需进行 |
| VictoriaMetrics adapter | master-plan 定义为一等 adapter，但实际 Phase 3 未实现（以 InMemoryCollector 替代）。Phase 4 不补实现，DEFERRED 到后续版本 |
| 多租户支持 | 未在 roadmap 中。adapter 和示例设计不排斥多租户扩展，但 Phase 4 不实现租户隔离逻辑 |
| Phase 3 DEFERRED 的高风险重构项 | P3-TD-10（TOCTOU 竞态）、P3-TD-11（domain 模型重构）、P3-TD-12（Rollback 版本校验）需要大规模重构且风险高，Phase 4 的示例和文档交付优先级更高。维持 DEFERRED 状态 |
| 自动化验收测试覆盖所有示例场景 | 示例项目的目的是指导开发者使用框架，而非成为测试套件。每个示例应可编译可运行（`go build`），但不要求完整的自动化测试覆盖。示例项目内的测试以 smoke test 为主 |
| godoc.org / pkg.go.dev 发布 | Phase 4 仍为内部开发阶段，不面向外部 Go 模块注册中心发布。README 中的 `go get` 路径指向私有仓库 |

---

## Phase 3 继承债务（必须在 Phase 4 处理）

以下条目从 `docs/tech-debt-registry.md` 和 Phase 3 产品评审报告继承，是 Phase 4 范围内的硬性要求：

| 来源 | 编号 | 问题 | Phase 4 处置 |
|------|------|------|-------------|
| Phase 3 MF-1 | P3-TD-01 + P3-TD-07 | testcontainers 集成测试全为 stub；testcontainers-go 未在 go.mod | 实现真实 testcontainers 测试（postgres + rabbitmq + redis） |
| Phase 3 MF-2 | P3-TD-02 | postgres adapter 覆盖率 46.6% | 通过 testcontainers 提升到 >= 80% |
| Phase 3 MF-3 | 产品评审 | S3 ConfigFromEnv 环境变量前缀 GOCELL_S3_* 不匹配 | 修复前缀统一 |
| Phase 3 | P3-TD-09 | RS256 Option 注入但默认仍 HS256 | RS256 完全默认化 |
| Phase 3 | P3-TD-03 | 无 CI workflow | 创建 .github/workflows/ci.yml |
| Phase 3 | P3-TD-06 | outboxWriter nil guard 静默 fallback | 添加 slog.Warn |
| Phase 3 | P3-TD-05 | docker-compose.yml 缺 start_period | 添加健康检查参数 |
| Phase 3 | P3-TD-08 | WithEventBus 未标注 Deprecated | 添加 // Deprecated 注释 |
| Phase 2 | P2-T-02 | 无 J-audit-login-trail 端到端集成测试 | testcontainers 环境下实现 |
| Phase 2 | P2-SEC-04 | JWT HS256 默认（PARTIAL） | 与 P3-TD-09 合并，RS256 完全默认 |

---

## 示例项目设计约束

每个示例项目必须满足以下约束，确保对评估者和 Cell 开发者的指导价值：

1. **独立可运行** -- 每个示例有自己的 `main.go`，`go build .` 可编译，`docker compose up -d && go run .` 可启动。不依赖根目录 `cmd/core-bundle`。
2. **目录结构规范** -- 遵循 CLAUDE.md 定义的 Cell 目录约定（cell.yaml / slices/ / internal/domain / internal/ports）。
3. **adapter 注入模式** -- 演示构造时注入（Option pattern）和环境变量切换（in-memory vs real adapter）。
4. **错误处理规范** -- 使用 `pkg/errcode`，不使用裸 `errors.New`。错误响应遵循统一格式 `{"error": {"code": "...", "message": "..."}}`。
5. **可观测性** -- 使用 `slog` 结构化日志，启动时输出 Cell 注册信息和 HTTP 端口。
6. **README 含 curl 命令** -- 每个示例的 README 包含可直接复制执行的 curl 命令，展示 API 调用和预期响应。
7. **分层依赖合规** -- examples/ 可依赖所有层（kernel/ + runtime/ + adapters/ + cells/ + pkg/），但示例内部代码遵循 Cell 隔离规则。

---

## Persona-功能映射

| 功能 | P4（评估者） | P1（Cell 开发者） | P2（架构师） | P3（Tech Lead） |
|------|:-----------:|:----------------:|:-----------:|:--------------:|
| examples/sso-bff | 评估框架能否做 SSO | 学习内建 Cell 组合模式 | 验证跨 Cell 协作 | 新成员 onboarding 起点 |
| examples/todo-order | 评估自定义 Cell 开发体验 | golden path 参考 | 验证 outbox 事件模式 | 团队项目模板 |
| examples/iot-device | 评估 L4 场景支持 | 学习高级一致性模式 | 验证 L4 架构可行性 | 评估是否覆盖 IoT 需求 |
| README Getting Started | 30 分钟判断是否采用 | 快速 onboarding | 评估框架成熟度 | 新成员指引 |
| templates/ | 评估工程规范完整度 | 日常使用 | 团队规范基线 | 降低协作成本 |
| testcontainers 补全 | -- | -- | 信任一致性承诺 | 降低联调风险 |
| CI workflow | -- | -- | 评估质量门控 | 持续集成基线 |
| RS256 默认化 | -- | 安全默认 | 安全合规 | -- |
