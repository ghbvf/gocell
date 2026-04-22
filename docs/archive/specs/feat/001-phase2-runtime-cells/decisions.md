# Decisions — Phase 2: Runtime + Built-in Cells

## 裁决日期
2026-04-05

## 审查来源
- 架构师: review-architect.md (10 条建议)
- Roadmap 规划师: review-roadmap.md (10 条建议)
- Kernel Guardian: kernel-constraints.md (9 条建议)
- 产品经理: review-product-manager.md (10 条建议)

---

## 重要决策

### 决策 1: 事件订阅接口定义 — kernel/outbox 新增 Subscriber 接口

- **决策**: 在 `kernel/outbox/outbox.go` 中新增 `Subscriber` 接口（`Subscribe(ctx, topic, handler) error`），与已有 `Publisher` 对称。in-process EventBus 放在 `runtime/eventbus/` 中，同时实现 `outbox.Publisher` 和 `outbox.Subscriber`。
- **理由**: 4 方审查一致认为缺少 Subscriber 接口是最高风险卡点（A-3, R-5, KG-6）。kernel/ 层定义抽象接口、runtime/ 层提供内存实现、Phase 3 adapters/ 提供 RabbitMQ 实现，依赖方向正确。
- **被否决的替代方案**: 仅在 runtime/ 定义 EventBus 接口 — 否决，因为 kernel/ 应拥有核心契约接口的完备定义。

### 决策 2: 内存事件总线语义 — at-most-once + 简单重试

- **决策**: Phase 2 in-memory EventBus 诚实标记为 **at-most-once**（进程重启丢失未消费事件）。consumer 返回 error 时重试 3 次（指数退避），超限路由 dead letter channel（内存，可观测但不持久化）。
- **理由**: 内存无法真正实现 at-least-once（A-3, PM-9）。诚实标注语义避免 Phase 3 迁移时行为意外。3 次重试 + dead letter 提供最小可用的错误处理能力。
- **被否决的替代方案**: 模拟 at-least-once（WAL 持久化）— 否决，Phase 2 不引入存储依赖，过度工程。

### 决策 3: Slice 依赖注入 — 构造时注入模式

- **决策**: 采用方案 B（KG-4 建议）。`Slice.Init(ctx)` 签名不变，依赖通过 `NewXxxSlice(repo XxxRepository, publisher outbox.Publisher, logger *slog.Logger)` 构造函数注入。`Init` 仅做状态检查。Cell.Init 内部构造 Slice 实例并传入 Cell 级共享依赖。
- **理由**: 与 Uber fx 构造时注入风格一致，保持 kernel/ Slice 接口稳定，16 个 slice 的注入点在编译时类型安全。
- **被否决的替代方案**: 扩展 Slice.Init 签名为 `Init(ctx, SliceDependencies)` — 否决，修改 kernel 接口影响面太大且 SliceDependencies 会退化为万能容器。

### 决策 4: runtime/auth 定位 — 抽象中间件框架

- **决策**: `runtime/auth` 只提供抽象中间件框架：`AuthMiddleware(verifier TokenVerifier)` 和 `RequireRole(authorizer Authorizer)`，其中 `TokenVerifier` 和 `Authorizer` 是 runtime/ 定义的接口。access-core 的 session-validate 提供 `TokenVerifier` 实现（JWT RS256 验证 + session 状态查询），authorization-decide 提供 `Authorizer` 实现（RBAC 策略判定）。Bootstrap 阶段注入。
- **理由**: runtime/ 不应耦合具体认证策略（A-6, R-7），否则换认证方案需改 runtime/。access-core 拥有认证鉴权的完整领域逻辑。
- **被否决的替代方案**: runtime/auth 包含完整 JWT+RBAC 实现 — 否决，导致 runtime/ 与 access-core 逻辑重复且 runtime/ 耦合密钥管理策略。

### 决策 5: runtime/config vs config-core 职责边界

- **决策**: `runtime/config` = 框架启动配置（server.http.port, log.level 等），纯本地 YAML+env，消费者 = runtime 自身和 Cell.Init。`config-core` = 业务配置管理（feature flag, 租户配置, 发布/回滚），数据持久化在 DB，通过 contract 暴露给其他 Cell。二者不交叉 import。
- **理由**: A-2 正确识别了边界模糊风险。分层职责清晰后，runtime/config 的 watcher 监听本地文件，config-core 的事件机制处理业务配置变更，互不干扰。

### 决策 6: Cell 注册顺序 — Phase 2 硬编码

- **决策**: Phase 2 在 cmd/core-bundle 中硬编码注册顺序：config-core → access-core → audit-core（provider 先于 consumer）。自动拓扑排序延迟到 Phase 3。
- **理由**: A-10 建议的自动拓扑排序合理但 Phase 2 只有 3 个 Cell，硬编码成本低且可靠。kernel/governance 已有依赖图数据可用于 Phase 3 自动化。

### 决策 7: Feature Flag 范围 — 最小可用集

- **决策**: Phase 2 Feature Flag 仅支持：(1) 布尔开关 (on/off)；(2) 百分比 rollout（按 subject hash 取模）。基于规则的灰度（租户/IP/属性匹配）延迟到 Phase 3+。
- **理由**: PM-3 正确指出"灰度/rollout"范围弹性极大。最小可用集可测试、可验证，避免 Phase 2 范围蔓延。

### 决策 8: OIDC — Phase 2 仅密码登录

- **决策**: Phase 2 session-login 仅实现密码登录 + JWT 签发。J-sso-login 中 OIDC 相关 passCriteria 改为 `mode: manual`，注明"Phase 3 OIDC 适配器就绪后验证"。FR-8 描述更新。
- **理由**: PM-4 正确指出 OIDC 适配器是 Phase 3 非目标，Phase 2 强制 OIDC 验证不可行。

### 决策 9: Journey 分级 — Hard Gate + Soft Gate

- **决策**: 8 条 Journey 分为两级：
  - **Hard Gate (5 条)**: J-sso-login, J-session-refresh, J-session-logout, J-user-onboarding, J-account-lockout — 单 Cell 或密码登录路径，Phase 2 必须 PASS
  - **Soft Gate (3 条)**: J-audit-login-trail, J-config-hot-reload, J-config-rollback — 跨 Cell 事件驱动，Phase 2 通过 in-memory EventBus 验证，允许 stub/mock 辅助
- **理由**: R-6 正确识别了跨 Cell Journey 依赖 EventBus 完备度的风险。分级确保核心路径不被阻塞。

### 决策 10: YAML 元数据修正 — Wave 0

- **决策**: 在实施代码前安排 "Wave 0: Metadata Alignment" 批次，修正所有 slice.yaml / contract.yaml 遗漏（KG-1/KG-2/KG-3/KG-8/KG-9/PM-6/PM-10），确保 `gocell validate` 零 error。
- **理由**: KG-1 审查发现 audit-core 缺少 subscribe 声明、config-subscribe 无 contractUsage、http.auth.me.v1 无 serving slice 等问题。如果元数据不一致，gocell validate 持续报错阻碍每个 batch 的 gate 检查。

### 决策 11: runtime/ 可依赖 kernel/ — 明确合规

- **决策**: 确认 runtime/ 可以 import kernel/ 和 pkg/（CLAUDE.md 现有规则未禁止，但也未明确允许）。在 CLAUDE.md 依赖规则中补充 `runtime/ 可依赖 kernel/ 和 pkg/`。
- **理由**: KG-5 识别的问题。runtime/http/health 需要 kernel/cell.HealthStatus，runtime/bootstrap 需要 kernel/assembly.CoreAssembly，这些依赖方向正确（runtime 使用 kernel 接口）。

### 决策 12: Cell 路由/事件注册钩子 — 可选接口

- **决策**: 在 kernel/cell 新增两个可选接口 `HTTPRegistrar` 和 `EventRegistrar`（A-1 建议）。Cell 级聚合 Slice 路由后统一注册，Slice 不直接触碰 Router。接口参数使用 kernel/ 定义的抽象类型（不 import chi）。Bootstrap 对 Cell 做类型断言调用。
- **理由**: Cell 接口需要从治理模型扩展到运行时模型，但通过可选接口保持向后兼容。

### 决策 13: Session 4 Slice 保留但共享 Cell 级 domain

- **决策**: 保留 session-login / session-refresh / session-logout / session-validate 4 个独立 Slice。明确：4 个 Slice 共享 Cell 级 `internal/domain/` 和 `internal/ports/`（allowedFiles 扩展为 `cells/access-core/**`），Cell.Init 构造 SessionRepository 实例后注入所有 4 个 Slice。
- **理由**: YAML 已存在 4 个 slice，合并需要删除 YAML + 修改元数据，不如保留。但必须明确共享领域模型的机制（A-4 建议的核心关切）。

### 决策 14: 外部依赖白名单 — 6 个

- **决策**: Phase 2 外部依赖白名单统一为 6 个：
  1. `github.com/go-chi/chi/v5` — HTTP 路由
  2. `golang.org/x/crypto` — 密码哈希
  3. `github.com/fsnotify/fsnotify` — 配置文件 watcher
  4. `github.com/prometheus/client_golang` — Prometheus 指标
  5. `go.opentelemetry.io/otel` — OpenTelemetry tracing
  6. `github.com/golang-jwt/jwt/v5` — JWT RS256 解析验证
  
  同步更新 product-context.md S8 和 phase-charter.md。
- **理由**: A-9/R-4/KG-7/PM-1 四方一致指出矛盾。Prometheus/OTel 是可观测性核心需求，纯标准库无法替代。JWT 库避免手写 RS256+JWKS 的安全风险。

---

## Kernel Guardian 约束裁决

| 约束项 | 裁决 | 理由 |
|--------|------|------|
| KG-1: audit-core 缺 subscribe 声明 | accept | Wave 0 修正全部 slice.yaml + contract.yaml |
| KG-2: config-subscribe 缺 contractUsage | accept | Wave 0 修正，config-subscribe 声明 subscribe event.config.changed.v1 供其他 Cell 使用 |
| KG-3: http.auth.me.v1 无 serving slice | accept | 分配给 identity-manage slice 的 serve 声明 |
| KG-4: Slice Init 缺依赖注入 | accept | 构造时注入模式（决策 3） |
| KG-5: Assembly 缺 runtime 集成点 | accept | Bootstrap 为顶层编排器，runtime/ 可依赖 kernel/（决策 11） |
| KG-6: 无 Subscriber 接口 | accept | kernel/outbox 新增 Subscriber（决策 1） |
| KG-7: 外部依赖不一致 | accept | 统一为 6 依赖白名单（决策 14） |
| KG-8: session-validate/auth-decide 无 HTTP contract | defer | Phase 2 仅 Cell 内部使用，跨 Cell 需求在 Phase 3 评估 |
| KG-9: J-config-hot-reload 引用 audit-core 但无 contract | accept | Wave 0 修正：event.config.changed.v1 subscribers 加入 audit-core |

---

## 延迟到后续 Phase 的项目

| 项目 | 来源 | 延迟理由 | 计划 Phase |
|------|------|---------|-----------|
| Assembly 自动拓扑排序 | A-10 | Phase 2 仅 3 Cell，硬编码成本低 | Phase 3 |
| 分布式限流（Redis-backed） | A-7 | 需 Redis adapter | Phase 3 |
| session-validate/auth-decide 跨 Cell contract | KG-8 | Phase 2 仅 Cell 内使用 | Phase 3 |
| OIDC 登录流程 | PM-4 | OIDC 适配器是 Phase 3 | Phase 3 |
| ServiceToken Journey 验证 | PM-5 | 需 adapter 层支持完整测试 | Phase 3 |
| Feature Flag 规则引擎（租户/IP/属性） | PM-3 | 超出最小可用集 | Phase 3+ |
| OTel tracing 完整 exporter | R-10 | Phase 2 提供接口 + stdout exporter | Phase 3 |

---

## 被拒绝的建议

| 建议 | 来源 | 拒绝理由 |
|------|------|---------|
| A-4: 合并 4 个 session slice 为 session-manage | review-architect.md | YAML 已存在 4 个独立 slice，合并成本 > 收益。改为明确共享 Cell 级 domain/ports |
| R-10 部分: 将 Feature Flag 降为 stretch goal | review-roadmap.md | Feature Flag 是 config-core 的核心 slice，有 http.config.flags.v1 contract 依赖，不可降级 |
| PM-8: S6 时间标准改为 15 分钟 | review-product-manager.md | 保持 10 分钟目标但拆为 auto + manual 两部分验证，不放宽 |
