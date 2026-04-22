# Roadmap 审查 — Phase 2: Runtime + Built-in Cells

## 审查人: Roadmap 规划师
## 日期: 2026-04-05

## 审查意见

### R-1: Spec 与 Roadmap 的 Slice 计数不一致 — access-core
- **严重度**: 高
- **类别**: PRD对齐
- **问题**: Roadmap（PRD）明确定义 access-core 为 5 slices（identity-manage / session-login / session-refresh / session-logout / authorization-decide），Phase Charter 也写明 "5 slices"。但 Spec FR-8 列出了 7 个 slice（新增 session-validate 和 rbac-check）。同时，Roadmap 中提到"现有 session-validate 和 rbac-check slice 合并到 master-plan 定义的 slice 中"，但 Spec 将其保留为独立 slice 而非合并。实际仓库中 YAML 也存在 7 个 slice。Spec 和 Roadmap 对 access-core 的 slice 数量和合并策略存在直接矛盾。
- **建议**: 必须在 Spec 中明确二选一：(A) 遵循 Roadmap 将 session-validate 合并入 session-login（校验逻辑是登录验证的子集），rbac-check 合并入 authorization-decide（角色检查是授权判定的组成部分），并更新或删除多余的 slice.yaml；(B) 若 7 slices 是经过深思熟虑的设计决策，则回溯更新 Roadmap 和 Phase Charter 中的 slice 计数，并补充拆分理由。推荐方案 A，因为合并可降低 Phase 2 的交付面积，且两对功能确实高度耦合。

### R-2: Spec 与 Roadmap 的 Slice 计数不一致 — audit-core
- **严重度**: 高
- **类别**: PRD对齐
- **问题**: Roadmap 定义 audit-core 为 3 slices（audit-write / audit-verify / audit-archive），Phase Charter 也写 "3 slices"。但 Spec FR-9 列出 4 个 slice（audit-append / audit-verify / audit-archive / audit-query），新增了 audit-query。实际仓库也存在 4 个 slice YAML。同时注意 Roadmap 用 "audit-write" 命名，Spec 用 "audit-append"，名称也不一致。
- **建议**: (A) audit-query 是否属于 Phase 2 必须明确决策。从 Journey 验收角度看，J-audit-login-trail 很可能需要查询能力，如此 audit-query 应该纳入。(B) 若保留 4 slices，需回溯更新 Roadmap 和 Phase Charter。(C) 统一命名：确定 "audit-write" 还是 "audit-append"，在 Roadmap、Spec、YAML 三处保持一致。考虑到仓库 YAML 已使用 "audit-append"，建议以 Spec 为准统一为 "audit-append"。

### R-3: Spec 与 Roadmap 的 Slice 计数不一致 — config-core
- **严重度**: 中
- **类别**: PRD对齐
- **问题**: Roadmap 定义 config-core 为 4 slices（config-manage / config-publish / config-subscribe / feature-flag），Phase Charter 也写 "4 slices"。但 Spec FR-10 列出 5 个 slice（config-write / config-read / config-publish / config-subscribe / feature-flag），将 Roadmap 的 "config-manage" 拆分为 "config-write" + "config-read"。实际仓库也是 5 个 slice YAML。
- **建议**: 读写分离是合理的设计决策（读路径可独立扩展），但 Roadmap 和 Phase Charter 的 4 slices 计数需要同步更新。建议保留 Spec 的 5 slices 拆分方案，回溯更新 Roadmap。

### R-4: 外部依赖白名单在 Spec、Roadmap、Product Context 三处不一致
- **严重度**: 高
- **类别**: PRD对齐
- **问题**: 存在三个不同版本的外部依赖声明：
  - Roadmap Phase 2 节："新增外部依赖: `go-chi/chi/v5`, `golang.org/x/crypto`"（2 个）
  - Product Context S8："Phase 2 新增外部依赖仅 `go-chi/chi/v5` 和 `golang.org/x/crypto`，不引入其他第三方库"（2 个）
  - Phase Charter："新增外部依赖: `go-chi/chi/v5`, `golang.org/x/crypto`"（2 个）
  - Spec NFR-2："白名单包含 go-chi/chi/v5、golang.org/x/crypto、fsnotify/fsnotify、prometheus/client_golang、go.opentelemetry.io/otel"（5 个）
  
  Spec 中多出 fsnotify、prometheus 和 OTel 三个依赖。FR-2.2（文件 watcher）需要 fsnotify，FR-5.1（Prometheus 指标）需要 prometheus，FR-5.2（OTel tracing）需要 OTel。这些功能在 Spec 中被明确要求，但依赖在 Roadmap/Product Context 中被隐式排除。
- **建议**: 这是需要决策的核心矛盾。两条路径：(A) 保留 5 个依赖，但回溯更新 Roadmap 和 Product Context 的依赖声明和 S8 指标——这是推荐方案，因为 Prometheus 和 OTel 是可观测性的核心需求，难以用纯标准库替代；(B) 如果严格执行"仅 2 个外部依赖"的约束，则必须将 FR-2.2 改为轮询式 watcher（不依赖 fsnotify），FR-5.1/FR-5.2 降级为接口定义 + 标准库桩实现（Prometheus/OTel 作为可选 adapter 推迟到 Phase 3）。方案 B 会显著削弱 Phase 2 的可观测性价值主张。

### R-5: in-process event bus 的定位模糊，有范围蔓延风险
- **严重度**: 中
- **类别**: 范围蔓延
- **问题**: Spec 5.3 节声明 "Cell 间通信通过 in-process event bus（内存实现）"，Phase 3 替换为 RabbitMQ adapter。但这个 in-process event bus 的实现位置和接口归属不明确：它属于 kernel/（已有 outbox 接口）、runtime/、还是一个临时 mock？FR-9 要求 audit-core 消费 6 种跨 Cell 事件（session.created、user.created 等），这意味着 event bus 必须具备订阅路由、at-least-once 模拟等非平凡能力。如果实现不慎，这个"临时"组件可能演变为一个小型消息中间件，超出 Phase 2 的预期范围。
- **建议**: 在 Spec 中显式定义 in-process event bus 的位置（建议放在 `runtime/eventbus/` 下，作为 kernel/outbox.Publisher 接口的内存实现）、接口边界（仅支持 topic-based pub/sub，不支持持久化/重试/DLQ）、和明确的限制声明（"仅用于开发和测试，生产环境必须使用 Phase 3 的 RabbitMQ adapter"）。同时在并行策略中将其作为 Wave 1 的独立任务项列出。

### R-6: 8 条 Journey 端到端验证的可行性依赖 in-process event bus 的完备度
- **严重度**: 中
- **类别**: Phase依赖
- **问题**: Gate 验证要求 8 条 Journey 全部 PASS，其中 J-audit-login-trail（跨 Cell）、J-config-hot-reload、J-config-rollback 涉及跨 Cell 事件驱动流程。但 Phase 2 没有真实的消息中间件（Phase 3 才有 RabbitMQ），端到端验证依赖 in-process event bus 的功能完备度。如果 event bus 仅为简单 mock，这些跨 Cell Journey 可能无法真正验证事件驱动逻辑（如 at-least-once 语义、事件路由正确性）。
- **建议**: (A) 对 8 条 Journey 进行分级：单 Cell 内 Journey（J-sso-login、J-session-refresh、J-session-logout、J-user-onboarding、J-account-lockout）为 Phase 2 Hard Gate；跨 Cell Journey（J-audit-login-trail、J-config-hot-reload、J-config-rollback）为 Phase 2 Soft Gate（允许通过 mock/stub 方式验证，真正的端到端验证推迟到 Phase 3 adapter 就绪后）。(B) 或者，在 Spec 中明确 in-process event bus 必须支持的最小能力集，确保跨 Cell Journey 在内存模式下也能真正执行。

### R-7: runtime/auth 的 JWT/RBAC 与 cells/access-core 的 session-validate/authorization-decide 功能重叠
- **严重度**: 中
- **类别**: 范围蔓延
- **问题**: Spec 同时定义了两套认证鉴权实现：
  - runtime/auth（FR-7）：JWT RS256 验证 + RBAC 中间件 + 服务间认证
  - cells/access-core（FR-8）：session-validate（Token 校验 + Claims 返回）、authorization-decide（RBAC 权限判定）、rbac-check（角色检查）
  
  runtime/auth 是无状态的 HTTP 中间件层校验，access-core 是有状态的业务 Cell。但 JWT 验证逻辑、Claims 解析、角色匹配等核心代码存在高度重叠。如果不明确分工边界，两处实现可能在 Phase 2 开发中产生冲突或重复工作。
- **建议**: 在 Spec 中明确分层职责：runtime/auth 是纯粹的 HTTP 中间件（验证 token 签名、解析 Claims、注入 context），不涉及 session 存储和查询；access-core 的 session-validate 负责 session 状态管理（是否被吊销、是否过期等有状态校验），authorization-decide 负责基于策略的细粒度权限判定（超出简单角色匹配）。runtime/auth 应调用 access-core 提供的 contract 接口来完成有状态校验。建议在 Spec FR-7 和 FR-8 中各增加一段"与 FR-X 的分工边界"说明。

### R-8: Wave 3 三个 Cell 并行开发的前置条件未充分定义
- **严重度**: 低
- **类别**: 优先级
- **问题**: 并行策略（Spec 第 6 节）将 3 个 Cell 放在 Wave 3 并行开发，但 Wave 2 → Wave 3 的交接条件不明确。具体而言，Cell 开发需要 runtime/bootstrap 提供的启动编排能力和 runtime/http/router 提供的路由注册能力。如果 Wave 2 交付延迟，Wave 3 将被完全阻塞。
- **建议**: (A) 定义 Wave 2 → Wave 3 的最小可启动条件（minimum viable bootstrap）：至少 config 加载 + router 注册 + Cell Init/Start 生命周期可运行，即使 observability、auth 等中间件尚未完成。(B) 考虑将 Cell 开发中不依赖 HTTP 的部分（domain model、ports 接口、service 层纯逻辑 + 单元测试）提前到 Wave 2 期间并行，仅将 handler 层（依赖 router）留到 Wave 3。

### R-9: 文档需求（FR-11）和 DevOps 需求（FR-12）缺乏验收标准
- **严重度**: 低
- **类别**: 后续影响
- **问题**: FR-11 要求 "每个 package 的 doc.go 包含使用示例" 和 "Cell 开发指南文档"，但没有定义文档的最低内容要求、示例的复杂度标准。FR-12 要求 Makefile 包含 build/test/validate/generate 目标，但 Roadmap 中 Phase 2 的 Gate 验证没有包含文档检查。这些需求容易在交付压力下被草率完成或跳过，影响 Phase 4（Examples + 文档）的质量。
- **建议**: (A) 为 FR-11 定义最小文档集：runtime/ 层至少 3 个 package 的 doc.go 含可编译示例（http/middleware、config、bootstrap）；Cell 开发指南至少包含"从零创建 Cell"的完整步骤。(B) 将文档存在性检查加入 Gate 验证脚本（如 `test -f runtime/http/middleware/doc.go`）。(C) 如果 Phase 2 时间紧张，可将详细文档降级为 Phase 4 目标，Phase 2 仅交付 GoDoc 级别的函数注释。

### R-10: Roadmap 的 Phase 2 时间窗口（35 天）与 Spec 的 4-Wave 工作量存在紧张
- **严重度**: 中
- **类别**: 后续影响
- **问题**: Roadmap 分配给 Phase 2 的时间是 Days 29-63（35 天），需要交付约 20 个 runtime 包（含测试）+ 3 个 Cell（含 16 个 slice 的 handler/service/test）+ 8 条 Journey 验证 + 文档 + DevOps。按 Spec 目录结构估算，新增文件约 80-100 个，代码量 8000-12000 行。即使按 4 波并行策略，每波约 8-9 天，留给 Cell 集成和 Journey 端到端验证的时间很紧张。如果 Phase 2 延期，将直接挤压 Phase 3（Adapters, 14 天）和 Phase 4（Examples + 文档, 14 天）的时间窗口。
- **建议**: (A) 识别 Phase 2 中的可推迟项：FR-7.3（服务间认证 ServiceToken）可推迟到 Phase 3（与 adapter 集成时更有意义）；FR-5.2（OTel tracing）可降级为接口定义 + 空实现（真正的 tracing 需要 adapter 支持）；Feature Flag（config-core 的 feature-flag slice）功能独立性强，可作为 Phase 2 的 stretch goal 而非 hard requirement。(B) 在 Roadmap 层面预留 Phase 2 的 3-5 天缓冲区，从 Phase 3 或 Phase 4 中借调（Phase 3 的 6 个 adapter 本身也可以进一步并行化压缩时间）。

## 总体评价

Phase 2 Spec 在功能需求的深度和技术架构的清晰度上表现良好。runtime 层的 7 个中间件定义精确，对标参考矩阵完善，并行化策略合理。Cell 的领域模型和端口接口设计遵循了 kernel 层确立的分层约束，依赖隔离规则严格。Gate 验证脚本具体且可执行。

核心问题集中在 Spec 与 Roadmap/Product Context 之间的数据不一致：三个 Cell 的 slice 计数全部与 Roadmap 不同（7 vs 5、4 vs 3、5 vs 4），外部依赖白名单从 2 个膨胀到 5 个但未回溯更新上游文档。这些不一致会在后续 Phase 的规划和验收中制造混乱。另外，in-process event bus 的定位模糊和 runtime/auth 与 access-core 的职责重叠是两个需要在实施前澄清的设计问题，否则有范围蔓延和返工的风险。建议在进入 Stage 3（Decide）前，先统一 Spec 和 Roadmap 的 slice 计数和依赖清单，再对 R-5/R-6/R-7 三项做出明确的设计决策。
