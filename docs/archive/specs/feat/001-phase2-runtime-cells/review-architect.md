# 架构审查 -- Phase 2: Runtime + Built-in Cells

## 审查人: 架构师
## 日期: 2026-04-05

## 审查意见

### A-1: Cell 接口缺少路由/事件注册钩子，Slice 无法向 runtime 声明自己的 HTTP 路由和事件订阅

- **严重度**: 高
- **类别**: DDD分层 / 兼容性
- **问题**: 现有 `cell.Cell` 接口只有 `Init/Start/Stop/Health/Ready` 生命周期方法和 `OwnedSlices/ProducedContracts/ConsumedContracts` 元数据方法。spec 中每个 Slice 都有 `handler.go`（负责 HTTP 路由注册）和事件发布/订阅，但 Cell 接口没有定义"向路由器注册路由"或"向事件总线注册订阅"的钩子。`Bootstrap` 启动器无法通过类型安全的方式发现和注册 Cell 暴露的 HTTP 端点和事件消费者。如果靠 `Init(ctx, deps)` 的 `Dependencies.Config` 塞入路由器/事件总线实例，则 `Dependencies` 结构体退化为万能容器，破坏类型安全。
- **建议**: 在 `kernel/cell` 中新增两个可选接口（按需实现，不破坏现有 Cell 接口的向后兼容性）：
  ```go
  // HTTPRegistrar is implemented by Cells that expose HTTP endpoints.
  type HTTPRegistrar interface {
      RegisterRoutes(router chi.Router)
  }

  // EventRegistrar is implemented by Cells that subscribe to events.
  type EventRegistrar interface {
      RegisterSubscriptions(bus EventBus)
  }
  ```
  `Bootstrap` 在 Start 阶段对每个 Cell 做类型断言 `if r, ok := cell.(HTTPRegistrar); ok { ... }`。这样 kernel 层只定义接口，不 import `chi` 或具体 event bus（接口参数用 kernel 自己定义的抽象类型），依赖方向正确。同时 spec 的目录结构中 `slices/{name}/handler.go` 的路由注册方式需要明确：是 Slice 级注册还是 Cell 级聚合注册。建议 Cell 聚合其所有 Slice 的路由后统一注册，Slice 本身不直接触碰路由器。

### A-2: runtime/config 与 config-core Cell 职责重叠，边界模糊

- **严重度**: 高
- **类别**: 聚合边界 / 耦合风险
- **问题**: `runtime/config` 负责"从 YAML 文件 + 环境变量加载配置"并提供 `Config` 接口（Get/Scan），而 `cells/config-core` 也提供"配置 CRUD + 版本管理 + 热更新事件 + Feature Flag"。两者在"配置读取"和"配置变更通知"上语义高度重叠。spec 没有明确界定二者的边界：`runtime/config` 是框架自身启动配置的加载器，还是也服务于业务运行时的配置查询？如果 `config-core` 的 `config-subscribe` slice 消费 `event.config.changed.v1` 后更新本地缓存，那它与 `runtime/config` 的 Watcher 机制是什么关系？
- **建议**: 明确分层职责：
  - `runtime/config` -- 框架启动配置（server.http.port、log.level 等），纯本地 YAML+env，生命周期 = 进程级，消费者 = runtime 自身。
  - `config-core` Cell -- 业务配置管理（feature flag、租户配置、发布/回滚等），生命周期 = 业务域，数据持久化在 DB，通过 contract 暴露给其他 Cell。
  - 二者交汇点：`runtime/config` 的 Watcher 可以监听文件变更来调整框架行为（如 log level），但不应成为 `config-core` 的底层实现。spec 应在 FR-2 和 FR-10 中分别加一段"与 FR-10/FR-2 的关系"说明段落，禁止 `config-core` 内部 import `runtime/config`（它们的数据源和生命周期不同）。

### A-3: 内存事件总线的 at-least-once 语义模拟缺乏定义，影响 audit-core hash chain 正确性

- **严重度**: 高
- **类别**: DDD分层 / 性能
- **问题**: spec 5.3 节指出 Phase 2 使用"内存 channel 实现 Publisher/Subscriber 接口，支持 at-least-once 语义模拟"。但 at-least-once 的本质是"失败时重投"，内存 channel 丢失即永久丢失，无法真正实现 at-least-once。`audit-core` 的 `audit-append` slice 使用 HMAC-SHA256 hash chain，如果事件丢失导致审计条目缺失，hash chain 将断裂且无法自愈。此外，`kernel/outbox` 已定义了 `Writer/Relay/Publisher` 接口，spec 没有说明 Phase 2 的内存事件总线与 outbox 接口的关系。
- **建议**:
  1. 将 Phase 2 内存实现的语义诚实地标记为 **at-most-once**（进程内无持久化），并在 audit-core 的设计中明确：Phase 2 的 hash chain 验证仅限于进程不中断的场景，进程重启后 chain 需要重建。
  2. 内存事件总线应实现 `kernel/outbox.Publisher` 接口，使得 Phase 3 切换到 RabbitMQ 时只需替换实现，不需要修改 Cell 代码。spec 应在 FR 中新增一个小节定义 `runtime/eventbus` 包，声明它实现 `outbox.Publisher` + 一个新的 `Subscriber` 接口（kernel/outbox 目前只有 Publisher，缺少 Subscriber）。
  3. 在 `kernel/outbox` 中补充 `Subscriber` 接口定义，保持 kernel 层接口完备性。

### A-4: access-core 的 7 个 Slice 聚合边界过细，session 生命周期被拆成 4 个独立 Slice

- **严重度**: 中
- **类别**: 聚合边界
- **问题**: Session 的 login/refresh/logout/validate 被拆成 4 个独立的 Slice，每个 Slice 有自己的 `handler.go + service.go`。但 Session 是一个聚合根（Aggregate Root），login 创建 Session、refresh 延续 Session、logout 销毁 Session、validate 读取 Session -- 它们操作的是同一个聚合的不同命令。将其拆成 4 个 Slice 意味着：
  - Session 的领域模型（`internal/domain/session.go`）被 4 个 Slice 共享，但 Slice 的 `allowedFiles` 约定是 `cells/{cell-id}/slices/{slice-id}/**`，这 4 个 Slice 如何共享 `internal/domain/session.go`？
  - `SessionRepository` 接口被 4 个 Slice 的 service 层分别依赖，事务边界不清晰（例如 refresh 需要原子地使旧 token 失效 + 签发新 token）。
  - Slice 间如果需要共享状态（如 session 缓存），要么打破 Slice 隔离，要么通过 Cell 级别的共享状态传递。
- **建议**: 将 session-login、session-refresh、session-logout、session-validate 合并为一个 `session-manage` Slice，内部按 command/query 分组。这与 Cell 模型的语义更一致：一个 Slice 对应一个一致性域（Session 聚合）的所有操作。如果出于部署粒度考虑必须保留 4 个 Slice，则 spec 需要明确声明：这 4 个 Slice 共享 Cell 级别的 `internal/domain/` 和 `internal/ports/`（即 `allowedFiles` 需要包含 `cells/access-core/internal/**`），并且 Cell.Init 阶段将 SessionRepository 实例注入所有 4 个 Slice，而不是每个 Slice 自行构造。

### A-5: Dependencies 注入机制能力不足，Phase 2 的 Cell 需要更丰富的依赖

- **严重度**: 中
- **类别**: 兼容性 / 耦合风险
- **问题**: 现有 `cell.Dependencies` 结构体只有 `Cells map[string]Cell`、`Contracts map[string]Contract`、`Config map[string]any` 三个字段。Phase 2 的 Cell 实现需要注入的依赖远超此范围：`SessionRepository`、`UserRepository`、`AuditRepository`（Phase 2 是 mock/in-memory）、`outbox.Publisher`（事件发布）、`slog.Logger`（带 cell_id 的结构化日志器）、JWT 签发器等。如果把这些全塞进 `Config map[string]any`，则丧失类型安全；如果在 Cell 内部自行构造，则破坏可测试性。
- **建议**: 扩展 `Dependencies` 结构体或引入 Provider 模式：
  ```go
  type Dependencies struct {
      Cells     map[string]Cell
      Contracts map[string]Contract
      Config    map[string]any
      // Phase 2 新增
      Logger    *slog.Logger
      Publisher outbox.Publisher
      // 按需扩展...
  }
  ```
  或者，在 `Dependencies` 中新增一个 `Services map[string]any` 字段作为服务定位器，配合泛型辅助函数实现类型安全的查找。但更推荐的方式是：不修改 kernel 层的 `Dependencies`，而是在各 Cell 的构造函数 `NewAccessCore(opts ...Option)` 中通过 Option 模式注入具体依赖，`Dependencies` 只保留跨 Cell 的运行时协作信息。spec 需要明确选择哪种方案，并在"技术架构"一节中说明。

### A-6: runtime/auth 放在 runtime/ 层违反了关注点分离，认证/鉴权应属于 access-core Cell 的能力

- **严重度**: 中
- **类别**: DDD分层 / 耦合风险
- **问题**: spec 将 JWT 验证、RBAC 中间件、服务间认证放在 `runtime/auth/` 下。但认证鉴权的策略（JWKS key 来源、角色权限模型、服务间 secret 管理）属于业务域逻辑，本应由 `access-core` Cell 负责。将其放在 runtime 层意味着：
  - runtime 层耦合了认证策略的具体实现（RS256、HMAC），其他项目使用 GoCell 框架时可能需要不同的认证方案（如 mTLS、API Key）。
  - `access-core` 的 `session-validate` Slice 和 `authorization-decide` Slice 与 `runtime/auth` 的 JWT 验证和 RBAC 中间件功能重复。
  - runtime 层的 RBAC 中间件如何获取角色数据？如果从 `access-core` 的 `rbac-check` Slice 获取，则 runtime 反向依赖了 Cell，违反 NFR-1。
- **建议**: runtime 层只提供认证/鉴权的抽象中间件框架（如 `AuthMiddleware(verifier TokenVerifier)` 接受一个接口参数），具体的 JWT 验证器和 RBAC 决策器由 `access-core` Cell 提供实现，在 Bootstrap 阶段注入。这样 runtime 层保持策略无关，access-core 拥有认证鉴权的完整领域逻辑。spec 中 FR-7 和 FR-8 需要重新划分职责边界。

### A-7: rate limiter per-IP 策略在单进程内存实现下不可扩展，且缺少 Cell/Slice 粒度的限流

- **严重度**: 低
- **类别**: 性能 / 可扩展性
- **问题**: FR-1.1 的 RateLimit 中间件定义为"基于 token bucket 的限流（per-IP，可配置 rate/burst）"。Phase 2 的内存实现在单进程下可工作，但存在两个隐患：
  - 多实例部署时 per-IP 限流无法共享状态，相当于限流阈值乘以实例数，失去保护作用。
  - 只有全局 per-IP 维度，没有 per-Cell 或 per-endpoint 的限流支持。access-core 的登录接口（高安全敏感）和 config-core 的配置读取接口（高频低风险）应有不同的限流策略。
- **建议**:
  1. RateLimit 中间件接受 `RateLimiter` 接口（`Allow(key string) bool`），Phase 2 提供 in-memory 实现，Phase 3 可替换为 Redis 实现。
  2. 支持 per-route 或 per-group 的限流配置，允许在 `router.Group` 级别指定不同的 rate/burst。
  3. spec 中注明 per-IP 内存限流的局限性，将分布式限流列为 Phase 3 非目标声明中的"已知限制"。

### A-8: audit-core 的 hash chain 写入与业务事件消费在 Phase 2 的事务保证不清晰

- **严重度**: 中
- **类别**: 聚合边界 / 兼容性
- **问题**: `audit-core` 消费来自 `access-core` 和 `config-core` 的 6 种事件，通过 `audit-append` 写入 hash chain。在 Phase 2 无 DB 的条件下，hash chain 的存储只能是内存。这意味着：
  - hash chain 的 `Verify(range)` 验证只在进程生命周期内有意义。
  - `audit-append` 消费事件后写入 hash chain 不需要事务（内存写入），但 Phase 3 切到 DB 后需要事务保证（审计条目写入 + hash 更新在同一事务内）。如果 Phase 2 的 `audit-append` service 代码没有预留事务边界的抽象，Phase 3 会大面积重写。
  - `audit-archive` 在无持久化时语义不明。
- **建议**: 
  1. `AuditRepository` 接口的方法签名应包含事务上下文抽象（如 `Append(ctx context.Context, entry AuditEntry) error`，ctx 中携带事务），Phase 2 的内存实现忽略事务，Phase 3 的 DB 实现从 ctx 提取事务。
  2. `audit-archive` 和 `audit-verify` 在 Phase 2 可标记为 stub 实现（返回 not-implemented），避免为没有持久化的场景编写无意义的归档和验证逻辑。spec 应明确哪些 Slice 在 Phase 2 是 full 实现、哪些是 stub。

### A-9: NFR-2 外部依赖白名单与 product-context S8 不一致

- **严重度**: 低
- **类别**: 兼容性
- **问题**: spec NFR-2 列出 5 个外部依赖（chi、x/crypto、fsnotify、prometheus、otel），但 product-context.md 的 S8 只列了 2 个（chi、x/crypto），声称"Phase 2 新增外部依赖仅这两个"。这是明显矛盾。此外，JWT 验证（RS256 + JWKS）通常需要 `golang.org/x/crypto` 之外的库（如 `github.com/golang-jwt/jwt/v5` 或 `github.com/lestrrat-go/jwx`），spec 未将其列入白名单。
- **建议**: 
  1. 统一 spec.md NFR-2 和 product-context.md S8 的依赖列表，确保二者一致。
  2. 评估 JWT 实现是否需要额外库。如果选择手写 RS256 验证（仅用 `crypto/rsa` + `encoding/json`），需要在 spec 中明确说明且确保覆盖 JWKS kid 轮换场景。如果需要引入 `golang-jwt/jwt/v5`，则补入白名单。
  3. Prometheus client 和 OTel SDK 各自会带入大量传递依赖，S8 的"不引入其他第三方库"表述应改为"不主动引入除白名单以外的直接依赖"，承认传递依赖不可控。

### A-10: Bootstrap 启动流程未包含 Cell 的 Init 依赖拓扑排序

- **严重度**: 中
- **类别**: 性能 / 兼容性
- **问题**: FR-3 定义 Bootstrap 启动流程为 `parse config -> init assembly -> register cells -> start HTTP server -> start workers -> block until signal`。当前 `CoreAssembly.Start` 按注册顺序 FIFO Init/Start Cell。但 Phase 2 的 3 个 Cell 存在事件依赖：`audit-core` 消费 `access-core` 和 `config-core` 的事件。如果 `audit-core` 先于 `access-core` Start，它的事件订阅可能在 `access-core` 的 Publisher 就绪前就开始监听，导致启动阶段的事件丢失。随着 Cell 数量增长（Phase 4 examples 可能引入更多 Cell），手动控制注册顺序不可维护。
- **建议**: 
  1. `CoreAssembly` 应基于 contract 依赖关系自动计算 Init/Start 拓扑顺序：provider Cell 先 Start，consumer Cell 后 Start。kernel 层的 `DependencyChecker.checkDEP02` 已经构建了 Cell 依赖图（基于 contract 的 provider/consumer 关系），可以复用此图计算拓扑排序。
  2. 如果 Phase 2 暂不实现自动拓扑排序，spec 至少需要明确 3 个 Cell 的注册顺序（access-core -> config-core -> audit-core）并在 `cmd/core-bundle` 入口中硬编码此顺序，同时将自动拓扑排序标注为 Phase 3 改进项。

## 总体评价

Phase 2 spec 的功能范围定义清晰，runtime 层的模块划分（HTTP/config/bootstrap/shutdown/observability/worker/auth）与对标框架对齐度高，并行化策略（4 波）合理。3 个内建 Cell 的 Slice 拆分基本遵循了 Cell 模型的一致性域语义，8 条 Journey 覆盖了核心业务场景。

主要架构风险集中在三点：(1) kernel 层的 Cell/Slice 接口在 Phase 1 为纯治理模型设计，缺少运行时注册钩子（路由、事件），Phase 2 需要扩展接口但 spec 未给出方案；(2) runtime/auth 与 access-core 的职责边界、runtime/config 与 config-core 的职责边界需要更精确的界定，否则会导致逻辑重复和层间耦合；(3) 内存事件总线的语义限制以及 audit-core hash chain 在无持久化场景下的正确性保证需要在 spec 中诚实标注，避免 Phase 3 迁移时大范围返工。建议在开始实施前先解决 A-1、A-2、A-3 三个高严重度问题的方案选型。
