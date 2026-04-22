# 产品审查 — Phase 2: Runtime + Built-in Cells

## 审查人: 产品经理
## 日期: 2026-04-05

## 审查意见

### PM-1: S8 外部依赖白名单与 spec 自相矛盾
- **类别**: [范围偏移]
- **严重度**: 高
- **问题**: product-context.md 成功标准 S8 声明"Phase 2 新增外部依赖仅 `go-chi/chi/v5` 和 `golang.org/x/crypto`，不引入其他第三方库"。但 spec.md NFR-2 白名单还列出了 `fsnotify/fsnotify`、`prometheus/client_golang`、`go.opentelemetry.io/otel` 共 5 个新依赖。二者存在明确冲突：按 S8 原文，Prometheus 和 OTel 不可引入；按 NFR-2 原文，它们是计划内依赖。这个矛盾在开发中会导致验收标准无法判定 PASS/FAIL。
- **建议**: 将 S8 对齐至 NFR-2 的 5 项依赖白名单，或者将 Prometheus/OTel/fsnotify 推迟到 Phase 3 并从 spec 功能需求中移除可观测性和 watcher 相关条目。二选一，但必须消除自相矛盾。

### PM-2: RateLimit 中间件缺少可验证的验收标准
- **类别**: [验收标准缺失]
- **严重度**: 中
- **问题**: FR-1.1 对 RateLimit 中间件仅描述为"基于 token bucket 的限流（per-IP，可配置 rate/burst）"，缺少关键验收行为定义：超限时返回什么状态码？响应体是否包含 `Retry-After` 头？token bucket 的默认 rate 和 burst 是多少？多个 IP 的隔离性如何验证？没有这些定义，测试用例无法编写，开发者也无法知道自己实现得"对不对"。
- **建议**: 补充验收标准：(1) 超限请求返回 429 + `Retry-After` 头；(2) 明确默认 rate/burst 值（如 100 req/s, burst 200）；(3) 不同 IP 的限流桶相互隔离。

### PM-3: Feature Flag 的"灰度/rollout"能力缺少行为定义
- **类别**: [验收标准缺失]
- **严重度**: 高
- **问题**: FR-10 config-core 的 feature-flag slice 声称"支持开关/灰度/rollout"，但 spec 没有定义灰度的具体语义：按用户 ID 百分比？按租户？按 IP 段？rollout 是渐进式还是一次性？没有行为定义就没法写测试，也没法评估交付质量。这个功能看似简单，实际上可以从最简单的布尔开关到复杂的 A/B 测试基础设施，范围弹性极大。
- **建议**: 明确 Phase 2 的 Feature Flag 范围为最小可用集：(1) 布尔开关（on/off）；(2) 百分比 rollout（按 subject hash 取模）。将基于规则的灰度（租户/IP/属性）标记为 Phase 3+ 非目标。每种模式给出一条验收标准和测试用例。

### PM-4: J-sso-login 引用 OIDC 重定向但 OIDC 适配器是 Phase 3 非目标
- **类别**: [范围偏移]
- **严重度**: 高
- **问题**: Journey J-sso-login 的 passCriteria 包含"OIDC 重定向完成"（checkRef: `journey.J-sso-login.oidc-redirect`），但 product-context.md 非目标声明明确将 OIDC 适配器列为 Phase 3 交付。FR-8 session-login slice 也同时声称支持"OIDC/密码登录"。在 Phase 2 没有 OIDC 适配器的情况下，这条 auto 模式的 passCriteria 无法通过，会导致 Journey 验证失败，进而影响 Gate 验证的 S2 标准。
- **建议**: Phase 2 的 J-sso-login 中将 OIDC 重定向改为 mock/stub 验证（如 `oidc-redirect-stub`），或将该 passCriteria 改为 `mode: manual` 并注明"Phase 2 使用密码登录 fallback，OIDC 流程在 Phase 3 补充"。同时修正 FR-8 session-login 的描述，明确 Phase 2 仅实现密码登录 + JWT 签发，OIDC 登录留给 Phase 3 的 OIDC 适配器。

### PM-5: 服务间认证（FR-7.3 ServiceToken）没有对应 Journey 和 AC
- **类别**: [验收标准缺失]
- **严重度**: 中
- **问题**: FR-7.3 定义了基于 HMAC shared secret 的 ServiceToken 中间件用于服务间调用，但 8 条 Journey 没有任何一条覆盖服务间认证场景。Gate 验证的测试需求（FR-13）也没有提及 ServiceToken 的测试要求。这意味着该功能可能被实现但永远不被验证，或者反过来，开发者可能跳过它而不影响 Gate PASS。
- **建议**: 要么补充一条 passCriteria（可挂在 J-audit-login-trail 上，验证 access-core 向 audit-core 发事件时的服务间认证），要么将 FR-7.3 标记为 Phase 2 scope 但 Journey 验证在 Phase 3（需在非目标声明中说明原因）。

### PM-6: 配置热更新的端到端验收路径不完整
- **类别**: [验收标准缺失]
- **严重度**: 中
- **问题**: FR-2.2 定义了 fsnotify 文件变更 watcher，FR-10 config-core 的 config-subscribe slice 消费 `event.config.changed.v1` 更新本地缓存，J-config-hot-reload 验证"access-core 接收并应用新配置"。但 spec 没有定义 access-core 如何订阅 config-core 的配置变更事件 -- access-core 的 7 个 slice.yaml 中没有任何一个声明消费 `event.config.changed.v1` contract。这意味着 Journey 的 passCriteria 在当前元数据模型下无法实现。
- **建议**: (1) 在 access-core 中新增一个 config subscription 机制（或在某个现有 slice 的 contractUsages 中补充 `event.config.changed.v1` 的 subscribe 角色）；(2) 明确配置变更后 Cell 行为变化的可观测验收点（如日志输出、/healthz 返回新配置版本号）。

### PM-7: audit-core hash chain 完整性验证的密钥管理未定义
- **类别**: [验收标准缺失]
- **严重度**: 中
- **问题**: FR-9 audit-append slice 使用 HMAC-SHA256 构建 hash chain，但 spec 没有定义 HMAC 密钥的来源和管理策略。密钥从哪里来？是启动时配置注入还是从 config-core 获取？密钥轮换时如何处理旧链条的验证？这些问题直接影响 J-audit-login-trail 的"hash chain 完整性验证通过"这条 AC 能否真正验证安全性而非只是逻辑正确性。
- **建议**: 明确 Phase 2 的 HMAC 密钥管理策略为最简方案：启动配置注入，不支持运行时轮换。在 FR-9 中补充验收标准：(1) 密钥配置为空时启动失败并给出明确错误；(2) hash chain 中任一条目被篡改后 verify 返回失败及首个不一致的位置。

### PM-8: 成功标准 S6 "10 分钟" 不可自动验证
- **类别**: [验收标准缺失]
- **严重度**: 低
- **问题**: product-context.md S6 定义"从空项目到首个自定义 Cell 注册并启动，使用 bootstrap + runtime/http，10 分钟内完成"。这是一个开发者体验标准，无法通过 `gocell verify` 或 `go test` 自动验证，且"10 分钟"高度依赖开发者经验水平。Gate 验证章节没有提及如何验证 S6。
- **建议**: 将 S6 拆分为两部分：(1) 可自动验证：scaffold 生成的 Cell 骨架代码可编译通过（`gocell scaffold cell --id=demo && go build ./cells/demo/...`）；(2) 手动验收：编写一个 quickstart 文档并由非项目成员在 15 分钟内完成（时间放宽，降低随机性）。

### PM-9: in-process event bus 的 at-least-once 语义模拟缺少失败场景定义
- **类别**: [开发者体验]
- **严重度**: 中
- **问题**: spec 5.3 节提到 Phase 2 使用内存 channel 实现 Publisher/Subscriber 接口并"支持 at-least-once 语义模拟"，但没有定义模拟的边界：consumer 返回 error 时是否重试？重试几次？重试间隔？dead letter 如何处理？如果这些行为在 Phase 2 (in-memory) 和 Phase 3 (RabbitMQ) 之间差异太大，开发者在 Phase 2 编写的 consumer 逻辑可能在 Phase 3 切换后出现非预期行为。
- **建议**: 明确 Phase 2 in-memory event bus 的行为契约：(1) consumer 返回 error 时重试 N 次（建议 3 次），超限路由 dead letter channel；(2) 进程重启丢失未消费事件（明确声明，不模拟持久化）；(3) 在 Publisher/Subscriber 接口的 godoc 中标注 Phase 2 与 Phase 3 的行为差异。

### PM-10: J-session-refresh 引用了错误的 contract
- **类别**: [范围偏移]
- **严重度**: 低
- **问题**: Journey J-session-refresh 的 contracts 列表中只包含 `http.auth.login.v1`，但 session refresh 操作在语义上不应复用 login contract。当前的 contract YAML 中也没有 `http.auth.refresh.v1` 或类似的 refresh 专用 contract。session-refresh slice 的 slice.yaml 中也没有声明任何 contractUsages。这意味着 refresh 操作的接口边界没有被 contract 治理覆盖。
- **建议**: (1) 新增 `http.auth.refresh.v1` contract，由 session-refresh slice serve；(2) 更新 J-session-refresh 的 contracts 列表引用新 contract；(3) 更新 session-refresh slice.yaml 补充 contractUsages。如果有意复用 login contract（如 refresh 是 login endpoint 的一个子操作），需在 spec 中明确说明理由。

## 总体评价

Phase 2 的 spec 在架构分层、依赖隔离、并行化策略方面设计扎实，runtime 层的模块划分和对标参考矩阵为实施提供了清晰的方向。16 个 slice 和 8 条 Journey 的覆盖面基本合理，能够证明 Cell-native 模型在运行时的可行性。

但 spec 在验收标准的精确度上存在系统性缺陷：多个功能点（RateLimit、Feature Flag 灰度、HMAC 密钥管理、in-memory event bus 语义）只给出了概括性描述而缺少可判定 PASS/FAIL 的具体行为定义。此外，product-context.md 与 spec.md 之间存在至少两处硬矛盾（外部依赖白名单 S8 vs NFR-2、OIDC 重定向 vs 非目标声明），如果不在开发启动前解决，将在 Gate 验收时造成争议。建议在 spec 定稿前逐条对齐成功标准与功能需求，确保每条 auto 模式的 passCriteria 都有对应的可执行测试路径。
