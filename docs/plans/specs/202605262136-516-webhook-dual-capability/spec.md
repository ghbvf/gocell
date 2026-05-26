# Feature Specification: Webhook 双向能力（Receiver + Dispatcher）

**Feature Branch**: `516-webhook-dual-capability`
**Created**: 2026-05-26
**Status**: Draft
**Input**: User description: "使用ship技能和speckit技能对webhook进行分析，生成实施计划，每个PR控制在 2000行以内"

## User Scenarios & Testing *(mandatory)*

GoCell 是 cell-native Go 框架，本特性的"用户"是**框架消费者（构建业务系统的开发者）**与**业务系统的运维**。
特性目标：让消费者声明式地为某个 Cell 接入 Webhook，无需亲手处理签名 / 防重 / SSRF / 重试等公共问题。

### User Story 1 - 业务 Cell 安全接收外部 Webhook 回调（Priority: P1） 🎯 MVP

作为框架消费者，我希望在 slice 配置中声明"本 slice 接收来自外部系统（如支付/SaaS/CI 提供商）的 webhook 回调"，框架自动完成签名校验、时间窗校验、防重放，业务 handler 只关心已经验真的有效负载。

**Why this priority**: 接收能力是 webhook 价值链的起点——业务系统通常先有"需要被通知"的需求，再考虑"主动通知别人"。MVP 必须先解决"安全接收 + 不重复处理 + 来源隔离"三个最难自己写对的事；缺失任何一项都会让消费者要么放弃 webhook，要么自己写出有漏洞的版本。

**Independent Test**: 单独部署一个仅声明 webhook receiver 的最小 Cell；用真实凭据从对端发送一次有效回调，业务 handler 被调用一次且收到原始负载；重复发同一回调，handler 不被二次调用；篡改签名后回调，handler 不被调用且响应 401。

**Acceptance Scenarios**:

1. **Given** 业务 Cell 已声明接收来自 source "stripe" 的 webhook 且配置了对应密钥，**When** Stripe 发出一次签名正确、时间在容忍窗口内的回调，**Then** 业务 handler 被调用一次并收到原始负载，HTTP 响应为 2xx
2. **Given** 同一回调（同一 delivery_id）刚刚已被成功处理，**When** 对端因网络原因重发同一回调，**Then** 业务 handler **不**被再次调用，HTTP 响应为 2xx（幂等返回）
3. **Given** 攻击者截获了一个有效回调，**When** 在 timestamp 容忍窗口外重放，**Then** HTTP 响应为 401，业务 handler 不被调用
4. **Given** 攻击者构造了一个未经签名或签名错误的请求，**When** 请求到达 webhook 端点，**Then** HTTP 响应为 401，错误信息不暴露密钥或签名比对的字节差异
5. **Given** 两个不同 source（如 stripe 与 shopify）共用一个 Cell，**When** stripe 的密钥被对端用于伪造 shopify 的回调，**Then** shopify 端点拒绝该请求（来源隔离）

---

### User Story 2 - 业务 Cell 安全向外部 Webhook 主动推送（Priority: P2）

作为框架消费者，我希望在 outbox event 上声明"本事件需要推送到外部 URL"，框架自动完成签名注入、SSRF 防御、超时控制、失败退避重试、永久失败转死信，业务代码只关心"事件已发布"。

**Why this priority**: 推送能力依赖外部 URL（消费者侧关心），需要 outbox 已稳定（依赖项已就绪）；同时安全面更宽（SSRF 是云原生环境的高危漏洞类别），但消费者通常先有"被通知"的需求才会想到"主动通知"。P2 是合适的优先级。

**Independent Test**: 单独部署一个仅声明 dispatcher 的最小 Cell；向 outbox 发布一个声明了出站目标的事件；架设一个 fake HTTP 服务接收 POST 并独立验证签名，能收到一次签名正确的请求；将目标 URL 改为内网/loopback 地址，事件不被发出且 outbox 记录 SSRF 拒绝原因；将目标设为持续返回 5xx 的服务，触发退避重试并最终进入死信通道。

**Acceptance Scenarios**:

1. **Given** Cell 声明 outbox event 类型 X 推送到外部 URL，**When** 业务发布该事件，**Then** 目标 URL 在 30 秒内收到一次 POST，请求体即事件负载，请求头携带签名/时间戳/delivery_id，且签名校验通过
2. **Given** 配置的目标 URL 解析到内网/loopback/link-local 地址，**When** 调度试图投递，**Then** 不发起任何 TCP 连接，事件被标记为永久失败，原因记录为"SSRF 拒绝"
3. **Given** 目标服务持续返回 5xx，**When** 调度按既定退避策略多次重试，**Then** 重试间隔逐步拉长（不是固定/指数失控），重试预算耗尽后事件进入死信通道
4. **Given** 目标服务返回 4xx 永久错误，**When** 调度收到该响应，**Then** 不再重试，事件直接进入死信通道
5. **Given** 业务负载包含密钥字段，**When** 事件被发布到 outbox，**Then** outbox 持久化的负载不含签名密钥（密钥仅在 dispatcher 即时签名时从凭据存读取）

---

### User Story 3 - 运维能观测 Webhook 健康并验证安全闭环（Priority: P3）

作为业务系统的运维，我希望从指标面板和健康探针看到 webhook 各 source 的成功率/延迟/拒绝原因，并能信任"密钥/签名不会出现在任何 log/span/trace/audit 出口"，发生攻击时能快速定位但不会反向泄漏。

**Why this priority**: 没有可观测性，故障定位与安全审计无从开展；但前两个 story 验真后才有意义触发 observability 工作。同时安全闭环（redaction + archtest + ADR）是 GoCell 的硬性框架约束，不能省。

**Independent Test**: 触发各类失败路径（签名错 / timestamp 过期 / SSRF 拒绝 / 5xx 重试 / 死信），从指标面板能看到对应计数与延迟分布；从结构化日志能看到含 request_id 关联字段但**不含**密钥/签名值；在 PR 上跑 archtest，所有 webhook invariant 全部通过。

**Acceptance Scenarios**:

1. **Given** webhook receiver 与 dispatcher 已部署，**When** 运维查看健康探针，**Then** 看到 `webhook_receiver_ready` / `webhook_dispatcher_ready` 状态，依赖故障时探针变红且原因可追踪
2. **Given** 一段时间内有多次签名失败，**When** 运维查看指标面板，**Then** 看到 `webhook_signature_failures_total` 按 source/reason 的计数趋势
3. **Given** 发生任意路径异常（panic / 签名错 / SSRF），**When** 运维查看错误日志或 trace span，**Then** 看不到任何 webhook 密钥、签名值、Authorization header 等敏感字段
4. **Given** 团队成员尝试新增一处不经签名 funnel 的 HMAC 调用或不经 SSRF 守卫的 HTTP 出站，**When** CI 跑 archtest invariants，**Then** CI 红、PR 阻塞，错误消息明确指向违反的 invariant
5. **Given** 审计模块从 webhook 事件衍生出审计条目，**When** 审计条目从查询出口下发，**Then** 出口经统一脱敏，密钥与签名不出现在响应中

---

### Edge Cases

- 对端轮换密钥（短时间内同一 source 的两把密钥同时有效）：receiver 必须支持多签名头/多密钥同时校验，任一匹配即通过
- 对端时钟漂移到 5 分钟边界：恰好等于容忍窗口边界的请求按"通过"处理，超出 1 秒（即 5min + 1s）按"拒绝"
- 对端时间戳"未来时间"：拒绝（防止恶意构造未来时间戳让请求"永远不过期"）
- 业务 handler panic：必须经统一 recovery 兜底，返回 5xx；不能让 receiver goroutine 崩溃
- 同一 delivery_id 第一次处理中崩溃（claim 未 commit）：下次重发可重新处理（不锁死成"已处理"）
- DNS rebinding：解析时返回公网 IP 通过校验，TCP 连接时换成内网 IP——dispatcher 必须在 dial 时再校验一次解析 IP
- HTTP redirect 到内网：dispatcher 必须**拒绝**自动跟随 3xx，避免 redirect SSRF
- 目标服务返回巨大 response body 试图撑爆内存：dispatcher 必须限制读取上限
- 业务负载是合法但极度嵌套的 JSON 试图撑爆解析栈：标准库默认深度兜底，不额外保护亦可接受
- 注册 webhook 接收时 path 含 `..` 等路径穿越尝试：路由层统一规范化拒绝
- secret 在 outbox 入库前漏配：业务系统启动 fail-fast（required 依赖缺失），不允许 silent 降级

## Requirements *(mandatory)*

### Functional Requirements

**Receiver（US1）**

- **FR-001**: 框架 MUST 允许业务 Cell 在 slice 配置中以声明方式注册一个 webhook 接收端点（含 source 标识与对应密钥引用），无需手写 HTTP 路由注册代码
- **FR-002**: 框架 MUST 在业务 handler 被调用前完成：原始负载读取、签名校验、时间戳容忍校验、来源隔离校验、防重放幂等校验
- **FR-003**: 框架 MUST 对相同 delivery_id 的重复回调只调用一次业务 handler，二次及以后请求返回幂等成功响应
- **FR-004**: 框架 MUST 在签名校验失败时返回 401，且响应体不暴露密钥、签名、内部细节
- **FR-005**: 框架 MUST 在 timestamp 超出容忍窗口（双向 5 分钟）时返回 401
- **FR-006**: 框架 MUST 支持单 source 下多密钥并存（密钥轮换过渡期）
- **FR-007**: 框架 MUST 在 handler panic 时由统一 recovery 兜底返回 5xx，receiver 进程不崩溃

**Dispatcher（US2）**

- **FR-008**: 框架 MUST 允许业务 Cell 在 slice 配置中声明"某 outbox event 类型推送到外部 URL"，自动派生注册代码，无需手写注册
- **FR-009**: 框架 MUST 在出站前对目标 URL 做 SSRF 校验（含 IPv4/IPv6 私网、loopback、link-local、文档段 CIDR），命中黑名单立即拒绝且记录原因
- **FR-010**: 框架 MUST 在 dial 时二次校验解析 IP（防 DNS rebinding）
- **FR-011**: 框架 MUST 禁止自动跟随 HTTP 3xx 重定向
- **FR-012**: 框架 MUST 在 dispatcher 即时计算签名（密钥不入 outbox 持久化负载）并注入对端可校验的 header
- **FR-013**: 框架 MUST 按可配置的退避计划重试 5xx 与连接错误，对 4xx 永久错误不重试直接转死信
- **FR-014**: 框架 MUST 对每次出站施加超时（默认 30 秒）与响应体上限

**Observability/治理（US3）**

- **FR-015**: 框架 MUST 暴露 webhook receiver 与 dispatcher 的健康探针，依赖不可用时探针变红
- **FR-016**: 框架 MUST 暴露按 source/result/reason 维度的成功/失败计数与延迟分布指标
- **FR-017**: 框架 MUST 保证密钥、签名值、Authorization 类敏感字段不出现在任何日志、span attribute、trace event、审计出口
- **FR-018**: 框架 MUST 通过静态约束（archtest / typed funnel）阻止"绕开签名 funnel 直接调用 HMAC"、"绕开 SSRF 守卫直接拨号"、"绕开幂等守卫直接调用业务 handler" 等绕过形态进入主干

**契约 / 演化**

- **FR-019**: 框架 MUST 提供 webhook 契约 schema 描述（用于跨 Cell 边界声明 webhook 端点的不可变约束）
- **FR-020**: 框架 MUST 在 v1 阶段允许 wire 契约直接演化（对齐项目宪法的"v1.0 GA 前不需 v2"政策）

### Key Entities

- **Webhook Source**: 一个外部系统的逻辑标识（如 "stripe"），持有对应密钥与允许的密钥轮换集合。一个 Cell 可注册接收多个 Source。
- **Webhook Endpoint**: 业务 Cell 暴露的可被外部 POST 的路径，与一个或多个 Source 绑定。
- **Delivery**: 一次外部到本系统（receiver 视角）或本系统到外部（dispatcher 视角）的回调单元，唯一键是 `(direction, source/target, delivery_id)`，幂等的基础单位。
- **Dispatch Target**: 一个出站目标的逻辑名（业务 Cell 在 slice 中声明），运行时映射到具体 URL + 凭据。
- **Retry Schedule**: 一组按时间步进的重试间隔（共享 outbox 重试预算），消费者可覆盖但需在框架允许集合内。

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: 业务消费者新接入一个 webhook source（receiver）的配置代码量 ≤ 10 行 slice.yaml + 1 个 handler 函数（不含业务逻辑本身）
- **SC-002**: 业务消费者新接入一个 dispatcher 目标的配置代码量 ≤ 10 行 slice.yaml + 1 个 target selector 函数
- **SC-003**: 在端到端集成测试中，受签名校验保护的 receiver 拒绝 100% 的篡改请求与 100% 的 5min 外重放请求
- **SC-004**: 在端到端集成测试中，dispatcher 拒绝向 100% 的内网/loopback/link-local IP 发起连接（含 DNS rebinding 场景）
- **SC-005**: 一次 receiver 端到端处理（含签名 + 幂等 + handler 调用）的 p99 额外开销 < 5ms（不计 handler 本身）
- **SC-006**: dispatcher 失败重试间隔的最大间隔不超过 10 小时，重试预算耗尽后 100% 转死信而非无限重试
- **SC-007**: 在故意触发的全部失败路径中（签名错 / 过期 / SSRF / 5xx / panic），生产日志/trace/span 出口中密钥与签名值出现次数为 0
- **SC-008**: CI 上 webhook 相关 archtest invariants 全绿率 100%，且任何"绕过 funnel"的反向自检测试都能触发 CI 红
- **SC-009**: 6 个 PR 全部合并后，backlog 中 `KERNEL-WEBHOOK-01` 条目状态变更为已完成，且不留任何 follow-up 的 TODO/Cx3/Cx4 标记（仅留显式 follow-up issue 编号）

## Assumptions

- 项目宪法 `.specify/memory/constitution.md` 与 CLAUDE.md 的分层规则、AI-robust 评级、archtest 三档分级、契约扇出闭环规则适用于本特性，所有产物 MUST 满足之
- 依赖项 Outbox Relay（AL-01）与 DistLock（AL-02）已稳定（已合入），dispatcher 可直接复用现有 outbox 消费链路
- 初版仅支持 HMAC-SHA256 单签名算法；Ed25519 与 KMS（如 Vault Transit）签名作为后续 follow-up issue 显式跟踪，不进本特性
- 初版仅提供内存版 source 密钥存储 + 可替换 `SourceStore` 抽象；密钥持久化到 configcore / vault 作为 follow-up
- 初版不引入完整 circuit breaker 状态机；用现有 outbox 重试预算耗尽 + endpoint 标记永久失败的简化版即可，完整状态机作为 follow-up
- 业务消费者通过对端 webhook 文档自行获取签名密钥与 source 标识；框架不提供"凭据交付协议"
- 业务 Cell 接入 webhook 的接受度以"声明式 slice 配置 + cellgen 派生"为目标体验，等同于现有 event subscribe 接入体验
- 本特性产物按"每个 PR ≤ 2000 行 diff"切片为 6 个 PR，PR 之间为线性依赖（个别可并行），切片细节见 plan.md
