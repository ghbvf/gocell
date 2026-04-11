# GoCell Backlog

> 只含待办事项。历史完成记录已归档至 `docs/reviews/archive/202604111438-backlog-full-history.md`。
> 更新日期: 2026-04-11（PR#74-77 合并后更新）

---

## 已合并（本轮）

| 任务 | PR | 说明 |
|------|-----|------|
| G-7 auto-derive belongsToCell/ownerCell | PR#67 ✅ | metadata parser 自动推导 + table-driven test |
| 0-F + 0-G 部分: Solution B 接口 + Subscriber 重写 | PR#68 ✅ | kernel/outbox Disposition/Receipt/HandleResult + idempotency Claimer + eventbus ConsumerMiddleware + rabbitmq processDelivery |
| WM-9/10/11 errcode 三合一 | PR#69 ✅ | errcode 内外分离 + TraceID + OTel 4xx/5xx 分类 |
| P0 止血: bootstrap rollback + ClaimFailOpen + ctx cancel | PR#70 ✅ | claimWithRetry + fail-closed 默认 + 最后重试 ctx cancel → Requeue |
| governance rules: G-2/G-4/G-6 + review fixes | PR#71 ✅ | TOPO-07 + deprecated 阻断 + assembly boundary 校验 |
| HandleResult 零值改 invalid (SOL-B-04/R-1) | PR#72 ✅ | DispositionAck = iota+1, Disposition.Valid() |
| WM-12 archtest 边界守护 | PR#73 ✅ | 5 条 LAYER 规则 + 49 tests，go list -json -e，零新依赖 |
| Phase 1: PermanentError → kernel/outbox | PR#74 ✅ | PermanentError 提升到 kernel + WrapLegacyHandler 直接检测 + InMemoryEventBus 对齐 |
| 0-G B-03 RabbitMQ 重连 backoff | PR#75 ✅ | 三态连接 (connected/reconnecting/terminal) + permanent error 传播 + 结构化错误分类 + 指数退避+jitter |
| Phase 2: EventRouter 引入 | PR#76 ✅ | cell.EventRouter 接口 + runtime/eventrouter 实现 + 3 Cell 迁移 + bootstrap 集成 + celltest.StubEventRouter |
| WM-1 CSRF + BFF cookie session | PR#77 ✅ | SecureCookie(→pkg/) + CSRF Origin/Referer校验 + CookieSession JWT bridge，65 tests 97.5% |

## 进行中

| 任务 | PR | 说明 |
|------|-----|------|
| WM-2 密钥轮换 — JWT kid + HMAC key ring | PR#81 | 待合并，4 commits，所有 P0/P1 已修 |

### PR 队列（跨框架分析后修订，PR#70-77 已全部合并）

> 2026-04-11 跨框架分析结论: 对比 7 个主流框架后，识别出 2 个架构根因。
> 修订: R-3 废弃（被 Phase 2 EventRouter 取代）、R-5 废弃（合入 Phase 2 Cell 迁移）。
> 分析文档: `docs/reviews/20260411-architecture-root-cause-analysis.md`, `docs/reviews/20260411-cross-framework-architecture-analysis.md`

**架构修复（跨框架分析产出，取代部分 PR-Rollout）**

| 阶段 | 任务 | 文件 | 预估 | 依赖 |
|------|------|------|------|------|
| Phase 1 | ~~PermanentError 提升到 kernel/outbox~~ | — | — | PR#74 ✅ |
| Phase 2 | ~~EventRouter 引入~~ | — | — | PR#76 ✅ |
| Phase 3 | Checker 清理 + Receipt 加固 — 删 legacy Checker + sync.Once + LeaseTTL 校验 | `adapters/rabbitmq/consumer_base.go`, `kernel/idempotency/`, `adapters/redis/` | 3h | Phase 2 |

> 执行顺序: Phase 1 → Phase 2 → Phase 3（串行）
> 对标框架: Watermill Router, Sarama ConsumerGroupHandler, Temporal Worker, NATS JetStream, Kratos transport.Server
> 根因 1: Subscribe() 混合 setup/run → Phase 2 EventRouter 修复
> 根因 2: PermanentError 在 adapter 层 → Phase 1 修复

**PR-Rollout 剩余（独立任务）**

| 序号 | ID | 任务 | 文件 | 预估 | 依赖 |
|------|-----|------|------|------|------|
| R-4 | SOL-B-01 | lease 续租 Receipt.Renew / 后台 renew loop | `kernel/idempotency/idempotency.go`, `adapters/redis/idempotency.go`, `adapters/rabbitmq/consumer_base.go` | 4h | Phase 3 |

> ~~R-1~~: PR#72 ✅ 已合并
> ~~R-2~~: 合入 Phase 3（删 Checker）
> ~~R-3~~: **废弃** → Phase 2 EventRouter 取代
> ~~R-5~~: **废弃** → Phase 2 Cell 迁移包含

**PR-Cleanup: Kernel 架构整理**

| 序号 | ID | 任务 | 文件 | 预估 | 依赖 |
|------|-----|------|------|------|------|
| ~~K-1~~ | CS-AR-2 | ~~Dependencies 精简 + 冻结注释~~ | — | — | ✅ PR#79 |
| ~~K-2~~ | CS-AR-3 | ~~net/http ADR 注释~~ | — | — | ✅ PR#79（ADR 已存在于 registrar.go） |
| ~~K-3~~ | F-OB-01 | ~~BatchWriter 接口 + WriteBatchFallback~~ | — | — | ✅ PR#79 |
| K-4 | SOL-B-02 | Receipt 移回 idempotency 包 | `kernel/idempotency/idempotency.go`, `kernel/outbox/outbox.go`, adapters + tests | 3h | Phase 3 |
| ~~K-5~~ | — | ~~三角色审查 mandatory actions~~ | — | — | ✅ 含在 PR#79 |

> ~~K-1/K-2/K-3~~ ✅ 已合并（PR#79）；K-4 依赖 Phase 3

---

## P0 — v1.0 阻塞

### 0-B2: Outbox Relay 三阶段重写（1.5d）

| # | 任务 | 预估 |
|---|------|------|
| RL-01 | migration `003_outbox_status_columns.sql`（status/attempts/next_retry_at/claimed_at） | 0.5h |
| RL-02 | `RelayConfig` 新增 MaxAttempts / BaseRetryDelay / ClaimTTL | 0.5h |
| RL-03 | 重写 `pollOnce` 三阶段（claim → publish → writeBack） | 2h |
| RL-04 | `reclaimStale` 加入 cleanupLoop（超时 claiming → pending） | 0.5h |
| RL-05 | `OutboxWriter.Write` 显式写 `status = 'pending'` | 0.5h |
| RL-06 | relay 状态机 enum（替换 bool running + startedCh） | 1h |
| RL-07 | slog 指标 + `outbox.Entry.Attempts` 字段 | 0.5h |
| RL-08 | 测试覆盖（8 个场景） | 2h |

> 设计文档: `docs/reviews/202604072154-outbox-relay-three-phase-plan.md`

### ~~0-G 剩余: RabbitMQ 重连 backoff~~ PR#75 ✅

| # | 任务 | 预估 |
|---|------|------|
| ~~B-03~~ | ~~`connection.go`: setup 错误分类（recoverable vs permanent）+ anti-hot-loop backoff~~ | ~~2h~~ PR#75 ✅ |

---

## P1 — v1.0 强烈建议

### Tier 0 收尾

| # | 任务 | 预估 | 说明 |
|---|------|------|------|
| HT-01 | handler 级 decode 回归测试（order-create/device-register/device-command） | 2h | 0-I |
| HT-02 | `WriteDecodeError` 显式测试（400/413/500 + fallback） | 1h | 0-I |
| OB-02 | safe_observe_test.go 注入 panicking slog handler 测试 | 1h | 0-J 剩余 |
| SF-01 | `DecodeJSONStrict` 启用 DisallowUnknownFields | 1h | 0-H |
| SF-02 | handler 逐个切到 `DecodeJSONStrict`（10 个 struct） | 1h | 0-H |
| SF-03 | `WriteDecodeError` 适配严格/宽松模式 | 0.5h | 0-H |
| SF-04 | CHANGELOG / API 文档标注 breaking change | 0.5h | 0-H |
| HR-01 | RealIP / trustedProxies 产品决策 + WithTrustedProxies 或移除 | 2h | 0-K |
| HR-02 | 统一 route pattern 元数据，修复 metrics label 基数爆炸 (PROM-01) | 2h | 0-K |
| HR-03 | chi/middleware.RequestID bridge 替换自研 UUID 生成 | 1h | 0-K |
| HR-04 | tracing 官方能力决策（WithTracer 或补文档） | 1h | 0-K |

### Review Findings 未修

| ID | 文件 | 问题 | 预估 |
|----|------|------|------|
| F-5 | `kernel/journey/catalog.go` | Journey catalog 不校验引用 | 2h |
| R1C2-F01 | `runtime/eventbus/eventbus.go` | Close()+Subscribe() 竞态 | 2h |
| R1C2-F03 | `runtime/worker/worker.go` | WorkerGroup.Start 首个失败不取消其余 worker | 2h |
| ~~F-OB-01~~ | `kernel/outbox/outbox.go` | ~~无批量写支持~~ PR#79 ✅ | ~~2h~~ |
| TX-NIL-01 | `cells/*/service.go` | txRunner nil-safe 未文档化 | 1h |
| OTEL-COV-01 | `adapters/otel/` | 覆盖率 67.3%（PR#72 删除了依赖内部 API 的旧测试后回升，但仍 < 80%；成功路径需 gRPC OTLP endpoint，需 testcontainers 集成测试） | 2h |
| SUB-SETUP-01 | `kernel/outbox`, `cells/*/cell.go` | RegisterSubscriptions 用 100ms 启发式区分 setup 失败与正常阻塞消费。**已被 Phase 2 EventRouter 解决**——Router.Run() 同步返回 setup error，消除启发式 | ~~4h~~ → Phase 2 |
| ER-P2-01 | `runtime/eventrouter/router.go` | ~~Close() 正常关停缺 elapsed 日志~~ PR#76 ✅ | ~~15min~~ |
| ER-P2-02 | `kernel/cell/celltest/eventrouter.go` | ~~stubEventRouter 重复 → 提取到 celltest~~ PR#76 ✅ | ~~30min~~ |
| ER-P2-03 | `runtime/eventrouter/router.go`, `runtime/bootstrap/bootstrap.go` | Running() 是一次性信号，无持续 health check 集成（OPS-3 readiness 探针可复用） | 1h |
| ER-P2-04 | `runtime/bootstrap/bootstrap_test.go` | ~~缺 Router 正路径集成测试~~ PR#76 ✅ | ~~1h~~ |
| CLEANUP-01 | `runtime/bootstrap/bootstrap.go` | 删除 `WithEventBus` deprecated wrapper，调用方改用 `WithPublisher` + `WithSubscriber` | 30min |
| CLEANUP-02 | `cells/access-core/cell.go` | 删除 `WithSigningKey` + `signingKey` backward compat 字段，统一用 `WithJWTIssuer`/`WithJWTVerifier` | 1h |
| ER-ARCH-01 | `runtime/eventrouter/router.go`, `kernel/outbox/outbox.go` | **Readiness heuristic**: Router startup detection 仍用 time.After(500ms)，RabbitMQ Subscribe 的 topology setup (Qos+Declare+Bind+Consume) 可能超过此超时。彻底修复需 Subscriber 接口拆分 Setup()+Run()，**C4 架构级**。当前 500ms 对本地 broker 足够（InMemory 即时，RabbitMQ local declare < 50ms），仅跨网络集群场景才会触发 | **v1.1** |
| ER-ARCH-02 | `kernel/cell/registrar.go`, `runtime/eventrouter/router.go` | **Competing consumers**: EventRouter.AddHandler 只有 topic+handler，无 consumer group identity。audit-core + config-core 都订阅 event.config.changed.v1，RabbitMQ 下退化为 competing consumers 而非 fan-out。方案：`AddHandler(topic, handler, ...HandlerOption)` + `WithConsumerGroup(cg)`，**C3** | **Batch 5**（与 WM-17 lifecycle hooks 同期改 kernel/cell 接口），2h |
| RMQ-75-01 | `adapters/rabbitmq/rabbitmq_test.go:717` | Flaky test: `time.Sleep(20ms)` 等待 terminal state，CI 高负载下不稳定 → 改 `require.Eventually` | 15min |
| RMQ-75-02 | `adapters/rabbitmq/connection.go` | `MaxReconnectAttempts` 配置缺失，无限重连无上界（运维保底） | 1h |
| RMQ-75-03 | `adapters/rabbitmq/connection.go` | 命名改善：`failed→terminalCh`, `safeExp→maxSafeShift`, `permanentDialKeywords→permanentDialSubstrings` | 15min |
| RMQ-75-04 | `adapters/rabbitmq/connection.go` | `WaitConnected` godoc 缺调用方指引（permanent vs transient 区分） | 15min |
| RMQ-75-05 | `runtime/bootstrap/bootstrap.go` | `RegisterChecker("rabbitmq", conn.Health)` 未接入 readiness — permanent error 后 Pod 继续接流量 | 30min |
| P3-DEFER-01 | `adapters/rabbitmq/consumer_base.go`, `connection.go` | safeDelay 与 backoffDelay 核心逻辑重复（bits.Len64 overflow guard），应提取到 pkg/backoff | 2h |
| P3-DEFER-02 | `adapters/rabbitmq/consumer_base.go` | ClaimFailOpen `*bool` 不符合 Go 习惯，应改为 enum (`ClaimFailMode`) | 1h |
| P3-DEFER-03 | `examples/` | 新 API（WithHealthChecker、NewConsumerBase(Claimer)、MaxReconnectAttempts）无示例项目演示 | 2h |
| P3-DEFER-04 | `kernel/idempotency/idempotency.go`, `kernel/outbox/outbox.go` | Receipt 定义在 outbox 包造成 idempotency→outbox 耦合，考虑移到 idempotency 包 | 3h（C3 kernel 接口） |
| P3-DEFER-05 | `adapters/rabbitmq/connection.go` | Health() 在 reconnecting 和 terminal 状态下返回相同 error code，运维无法区分 | 3h（C3 状态机设计） |

### winmdm Accept P1

| # | 任务 | 包位置 | 预估 | 前置依赖 |
|---|------|--------|------|---------|
| ~~WM-1~~ | ~~CSRF 中间件 — Origin/Referer 校验 + BFF cookie session~~ | `runtime/http/middleware` + `pkg/securecookie` | ~~0.5d~~ | PR#77 ✅ |
| WM-35 | BFF handler 接入 — login/refresh/logout 接 SessionCookieWriter + BFF 模式不返回 token body | `cells/access-core/slices/session*` | 2d | WM-1 |
| WM-36 | SecureCookie key rotation — active+previous 双 key ring，灰度轮换 | `pkg/securecookie` | 1.5d | WM-1 |
| WM-6 | 游标分页 — keyset pagination | `pkg/query` | 1.5d | 无 |
| ~~WM-2~~ | ~~密钥轮换 — JWT kid 轮换 + HMAC（范围限定，扩展 keys.go）~~ | `runtime/auth` | ~~2d~~ | PR#81 🔄 |
| WM-34 | 配置热更新回调 — Cell 级 OnConfigReload | `runtime/config` | 1d | 无 |
| WM-2-F1 | KeyProvider 接口抽象 — JWTIssuer/JWTVerifier 解耦 *KeySet，为 auto-rotation/JWKS 预留接缝 | `runtime/auth` | 1d | WM-34 (discovered via PR#81 review P1-4) |
| WM-20 | TestPubSub 测试套件 — TestPublisher/TestSubscriber 标准套件 | `kernel/outbox/outboxtest/` | 1.5d | PR#68 |
| WM-17 | 生命周期钩子 — BeforeStart/AfterStart/BeforeStop/AfterStop 可选接口 | `kernel/cell` | 1d | 无 |
| WM-15 | L4 队列状态机 — 合入 0-B2 | `kernel/outbox` | 1.5d | 0-B2 |
| WM-33b | 熔断器 — sony/gobreaker 包装 | `adapters/` | 0.5d | 无 |

### 运维发现的生产缺口

| # | 问题 | 建议 |
|---|------|------|
| OPS-2 | 日志缺 trace_id 关联 — AccessLog/ConsumerBase 无 trace_id | 随 WM-10 一并补充 |
| OPS-3 | readiness 探针未接 adapter — postgres/redis/rabbitmq 不报告健康状态 | rabbitmq 提前至 Batch 3（RMQ-75-05）；postgres/redis 在 Batch 6 补 Health() + 注册 |

### Tech Debt P1

| ID | 问题 | 预估 | 归属 PR |
|----|------|------|---------|
| ~~SOL-B-04~~ | ~~HandleResult 零值静默 ACK~~ | ~~1h~~ | PR#72 ✅ |
| SOL-B-03 | cells Checker → Claimer 迁移 + 删除 legacy Checker 路径 | 2h | **合入 Phase 3** |
| ~~SOL-B-05~~ | ~~bootstrap 统一包装 subscriber/middleware~~ | ~~3h~~ | **废弃 → Phase 2 EventRouter 取代** |
| SOL-B-01 | Claimer lease 续租 — handler/retryLoop 超 LeaseTTL 后 Commit stale，需 Receipt.Renew 或后台续租（参考 distlock.go renewLoop） | 4h（C3，改 kernel 接口） | R-4 |
| SOL-B-02 | `idempotency → outbox` 依赖方向反转 — Receipt 移到 idempotency 包，outbox 反向依赖 idempotency | 3h（C3，10+ 文件） | K-4 |
| SOL-B-06 | `claimWithRetry` / `retryLoop` 的指数退避在超大重试次数下仍可能先发生 `time.Duration` 溢出；需改为饱和计算并补极值边界测试 | 1h | Phase 3 附近 |
| ~~P4-TD-03~~ | ~~`IssueTestToken` HS256 死代码（测试陷阱）~~ | ~~30min~~ | PR#81 ✅ (移到 helpers_test.go，删除 HS256 分支) |
| P4-TD-04 | order-cell 声明 L2 但无 outboxWriter enforce — order-create/service.go:50-71 + device-register/service.go:50-71 直接 Publish 违反 outbox 规则 | 2h | — |
| P4-TD-05 | 缺少 outbox 全链路 3-container 集成测试 | 2h | — |
| P3-TD-10 | Session refresh TOCTOU 竞态 | 4h（高风险） | — |
| P2-T-02 | J-audit-login-trail e2e 测试 | 2h | — |

---

## P2 — v1.0 后

### winmdm Accept P2

| # | 任务 | 包位置 | 预估 |
|---|------|--------|------|
| WM-7 | 批量操作 helper — BulkResult + 部分成功语义 | `pkg/httputil` 或 `pkg/bulk` | 1d |

### Tech Debt P2

| ID | 问题 | 预估 |
|----|------|------|
| P4-TD-01 | 缺少共享 NoopOutboxWriter | 30min |
| P4-TD-09 | List 端点缺分页且无 pageSize≤500 强制（order-query / configread / featureflag / device-command / auditquery）— WM-6 游标分页可解决 | 3h |
| ~~P4-TD-10~~ | ~~POST 201 响应未包装 `{"data":...}`~~ | ~~2h~~ | ✅ 已修复（device-register + device-command） |
| P4-TD-11 | in-memory repository 缺并发测试 | 1h |
| P4-TD-13 | Entity 直接作为 API 响应（order-query / configread / configwrite / featureflag / configpublish / device-status / device-register / device-command），需 DTO 转换 | 4h |
| P4-TD-14 | audit-core/auditappend/service.go:90 `_ = json.Unmarshal` 静默忽略错误，需显式处理或记录日志 | 30min |
| WM-2-F2 | ServiceToken HMAC message 不含 query string，可跨参数 replay | 2h (discovered via PR#81 review P2-1) |
| WM-2-F3 | runtime/auth 无 Prometheus metrics（key lifecycle counters/gauges），需通过接口注入避免 runtime→adapters 依赖 | 2h (discovered via PR#81 review P2-11，前置 WM-34) |
| P3-TD-11 | access-core domain 模型重构 | 4h（高风险） |
| P3-TD-12 | configpublish.Rollback version 校验 | 2h |

### WM-2 Review Accept（PR#81 六席位审查，不修理由）

| ID | Finding | Accept 理由 |
|----|---------|-------------|
| P2-3 | 缺 RFC 7638 external known-good test vector | Thumbprint 仅 3 行无分支（base64url + SHA-256）。已有 determinism + length(43) + encoding 测试。引入 RFC 附录固定密钥收益低。 |
| P2-4 | kid 未截断，log flooding 风险 | kid = base64url(SHA-256) = 固定 43 chars，由 Thumbprint 产生，不受外部输入控制。无任意长度 flooding 场景。 |
| P2-8 | 缺 non-RSA key type 拒绝路径测试 | `parseRSAPublicKey` 的 "PKIX key is not RSA" 分支在 develop 上已存在，非本 PR 引入。预存 tech debt。 |
| P2-9 | Lifecycle log 测试未用 table-driven | 3 个测试验证不同生命周期事件（激活/降级/修剪），场景差异大。table-driven 反而降低可读性。风格偏好。 |
| P2-10 | init() 模式有 package-level 副作用风险 | cell_test.go 已消除 init()。剩余 3 个 slice test 的 init 仅做 NewJWTIssuer/NewJWTVerifier（需 error 处理，无法用 var 替代）。Go 测试标准做法。 |
| P2-16 | env loader 仅支持 1 个 prev key | By design — spec FR-005 明确 "env loader 0-1"。KeySet API 支持 0-N。列表型 env 配置属 WM-34 scope。 |
| P4-TD-12 | demo cell `TestDemo_Startup` t.Skip 占位 | 30min |

### metadata parser follow-up（PR#67）

| ID | 问题 | 预估 |
|----|------|------|
| META-67-01 | `kernel/metadata/parser_test.go`: 补 `parseContract()` parser 层集成用例，覆盖“省略 `ownerCell` 且 `kind` 未知或为空时保持空值” | 30min |
| META-67-02 | `kernel/metadata/parser_test.go`, `kernel/governance/*`: 补跨模块断言，锁住“provider 为空 -> parser 保持 `ownerCell` 为空 -> governance 报错（REF-03/FMT-07）”链路 | 1h |
| META-67-03 | `kernel/metadata/types.go`, `kernel/metadata/parser.go`, `kernel/metadata/parser_test.go`: 收敛 `kind -> provider endpoint` 映射的维护点，减少注释/测试/实现三处漂移 | 1h |

### 运维 P2

| # | 问题 | 建议 |
|---|------|------|
| OPS-4 | 优雅关闭缺 drain 期 | bootstrap shutdown 增加 drain 阶段 |
| OPS-5 | 连接池无指标 | 暴露 PG/Redis/RabbitMQ pool stats |

---

## Review 待完成

### 代码审查进度

| 层 | 状态 |
|---|------|
| R1E cells | 待审 |
| R1F+G delivery + YAML | 待审 |
| R2 数据流合并 | 待审 |
| R3-R5 PR追溯/集成/裁决 | 待审 |

### Review 任务

| # | 任务 | 预估 |
|---|------|------|
| T1-3 | Review cells/（6 cell，5,811 行） | 4h |
| T1-6 | Review examples/（3 项目，233 行） | 2h |
| T1-7 | CI 加 golangci-lint / staticcheck | 2h |
| T1-8 | 产出 review 报告 + 汇总 findings | 2h |

---

## v1.1 — 核心能力完善

### metadata-model-v3 校验规则

| # | 缺失规则 | 优先级 |
|---|---------|--------|
| G-1 | FMT-11: 动态状态字段禁入非 status-board 文件 | HIGH |
| G-2 | TOPO-07: actor.maxConsistencyLevel 约束 | MEDIUM |
| G-4 | deprecated contract 引用阻断（非仅 warning） | MEDIUM |
| G-6 | Assembly boundary.yaml 存在性校验 | LOW |

### 未实现的 Kernel 子模块

| 子模块 | 说明 | 优先级 |
|--------|------|--------|
| kernel/wrapper | 契约级可观测 traced wrapper | P1 |
| kernel/command | 命令队列接口（L4 框架支持） | P1 |
| kernel/webhook | receiver + dispatcher | P2 |
| kernel/reconcile | 最终状态收敛 | P2 |
| runtime/scheduler | cron/定时任务 | P2 |
| kernel/replay | projection rebuild | P3 |
| kernel/rollback | rollback metadata | P3 |

### adapters/ 与 runtime/ 分层重整

| # | 问题 | 方向 |
|---|------|------|
| AL-01 | outbox_relay.go 轮询调度逻辑属于 runtime | 拆出 `runtime/outbox/relay.go` |
| AL-02 | distlock.go 续期 goroutine 属于 runtime | 拆出通用 distlock 接口 |
| AL-04 | runtime/auth 直接 import golang-jwt | 评估是否值得拆 |

### 架构风险

| ID | 问题 | 归属 PR | 状态 |
|----|------|---------|------|
| ~~CS-AR-2~~ | ~~Dependencies 精简~~ — 移除 `Cells`/`Contracts` 字段（零 caller），加冻结注释 | PR#79 | ✅ |
| ~~CS-AR-3~~ | ~~kernel/cell net/http 决策~~ — ADR 注释已存在于 registrar.go | PR#79 | ✅ |
| ~~F-OB-01~~ | ~~BatchWriter 接口 + WriteBatchFallback helper~~ — 独立接口，向后兼容 | PR#79 | ✅ |
| Cell 接口 | 12 个方法，考虑拆分为 Cell + CellLifecycle + CellMetadata | — | 暂缓（assembly 需全部 facet） |
| adapter 测试 | 15 个 t.Skip 集成测试待补全 | — | TODO |

### winmdm Defer v1.1

> 六席位审查裁决为 Defer 的 7 项需求。Accept 票数不足或有前置依赖未满足。

| # | 需求 | 票数 | 理由 |
|---|------|------|------|
| WM-18 | 延迟消息原语 — outbox Entry 标记延迟时间 | 3/6 | 架构/安全/产品 Accept，但 DX/测试/运维 Defer。InMemoryEventBus 无法支持延迟投递（需 timer heap），RabbitMQ 需 x-delayed-message 插件增加运维复杂度。等 0-B2 Outbox Relay 三阶段重写稳定后再评估 |
| WM-32 | mTLS 中间件 — TLS 客户端证书提取+验证 | 4/6 | 安全席认为 MDM 场景安全阻塞项，但架构/DX 认为 mTLS 通常由反向代理/service mesh 处理，非框架核心。v1.1 做通用 `runtime/tls` 配置构建器 + mTLS 验证中间件 |
| WM-4 | Webhook 出站 adapter — HMAC 签名/重试/健康检查 | 4/6 | 通用能力，但依赖 outbox relay 稳定作为投递基础。安全注意 SSRF 防护（URL 白名单/禁内网 IP）。backlog 已列 kernel/webhook 为 P2 |
| WM-5 | 查询过滤语言 (OData $filter) — pkg/query 过滤 DSL | 2/6 | OData 是微软/MDM 生态偏好，大多数 Go 微服务用简单 query parameter。当前 `pkg/query/builder.go` 已提供参数化 SQL builder。如需过滤语言，建议用成熟 OData 库包装而非自建解析器 |
| WM-22 | Visibility Query API — 统一查询异步任务状态 | 1/6 | `/healthz` + `/readyz` + logs + traces 已覆盖运维需求。依赖 WM-17 生命周期钩子和 HR-02 路由元数据先稳定。运维/诊断能力，v1.1 diagnostics 范畴 |
| WM-23 | 模块化单体→微服务 — Assembly API 对等验证 | 2/6 | Assembly 已天然支持多 Cell 单进程部署。拆分为独立二进制需要服务发现（WM-28）、分布式追踪等基础设施。当前优先验证 + 文档，而非新建 |
| WM-16 | 投影按需重算 — RecalcTrigger + debounce + checkpoint | 1/6 | backlog 已列 kernel/replay 为 P3，标注"无实际需求验证"。需先稳定 outbox relay + consumer middleware，投影重算建立在这之上 |

---

## v2+ — 长期

| # | 需求 | 票数 | 理由 |
|---|------|------|------|
| WM-28 | 服务发现 Registry | 0/6 | K8s Service 已提供服务发现。框架内置 Registry 是过早抽象。与 WM-23（单体→微服务）绑定评估：若 WM-23 提升优先级，需同步评估 |
| WM-29 | Saga 补偿 — 跨 Cell 分布式事务补偿链 | 0/6 | GoCell L2 outbox + L3 WorkflowEventual 最终一致模式已覆盖设计范围。Saga 引入补偿事务管理和分布式状态，显著增加 kernel/ 复杂度。等有真实多步补偿场景再引入 |

---

## 发布准备

| # | 任务 | 说明 |
|---|------|------|
| R-1 | 仓库公开或 GOPRIVATE 配置文档 | `go get` 无法使用 |
| R-2 | v1.0.0 tag | pkg.go.dev 无法索引 |
| R-3 | CONTRIBUTING.md | 无贡献指南 |
| R-4 | 性能基准 | 无 benchmark |
| R-5 | 棕地迁移指南 | 已有项目如何接入 GoCell |
| R-6 | 错误码目录 | 统一 errcode 文档 |

---

## winmdm Reject 项（附详细理由 + 替代方案）

> 六席位审查裁决为 Reject 的 9 项需求。判断标准："如果换一个非 MDM 项目（电商/SaaS/IoT），至少 2 个领域也需要，才是框架能力。"

| # | 需求 | 票数 | 裁决理由 | winmdm 替代方案 |
|---|------|------|---------|----------------|
| WM-3 | X.509 证书管理 — CA/CSR 签发/CRL/SCEP | 1/6 | MDM 专属。CA 签发、CRL 发布、SCEP 协议是设备管理/IoT 特有场景。X.509 PKI 是一个完整子系统，不应嵌入框架内核。安全席虽标为阻塞项，但这是 winmdm 部署的阻塞项，不是 GoCell 框架的阻塞项 | winmdm 在 `cells/cert-core` 自建，或集成 step-ca/Vault PKI 通过 adapter |
| WM-14 | Codec 注册表 — Content-Type 协商 JSON/XML/SOAP | 1/6 | YAGNI。GoCell 是 JSON-first 框架，现有 DecodeJSON/WriteJSON 覆盖所有使用场景。SOAP/SyncML 是 MDM OMA-DM 协议专属。引入 Codec 注册表需要重构全部编解码路径，影响 ~20 个 handler_test.go | winmdm handler 层自行实现 SyncML 编解码 |
| WM-21 | Mixin 共享逻辑 — kernel/mixin Hook 注册点+组合 | 2/6 | 非 Go-idiomatic。Go 的组合通过 embedding + interface 实现，BaseCell embedding 已是 Go 风格的 mixin。新建 kernel/mixin 引入非 Go 惯用概念，增加认知负担。如需跨 Cell 共享横切关注点，正确做法是中间件（HTTP + ConsumerMiddleware PR#68） | 用 BaseCell embedding + middleware 实现横切关注点 |
| WM-24 | Policy Engine — 证书签发 DNS/IP 白名单策略 | 1/6 | MDM 专属（CA 签发策略）。通用策略引擎（OPA/Casbin/Cedar）是独立产品，嵌入框架会引入 DSL 运行时和策略管理生命周期。GoCell 最多做 `PolicyEvaluator` 钩子接口 | winmdm 集成 OPA/Casbin 作为 adapter |
| WM-25 | 短期证书 — 5 分钟有效期免 CRL/OCSP | 1/6 | MDM/PKI 领域。运维成本极高（签发基础设施变为 5 分钟 RTO 硬依赖，需多区域签发、sub-minute 告警）。依赖 WM-3 X.509（已 Reject） | winmdm 集成 step-ca |
| WM-26 | FanOut/FanIn 消息拓扑 | 0/6 | RabbitMQ 原生 fanout exchange 是 broker 级能力，框架不应重复实现。应用级 fan-out 在 consumer handler 中 publish 到多个 topic 即可 | 使用 RabbitMQ exchange 类型配置 |
| WM-30 | 编译期 Contract 验证 — proto-gen-validate 风格 | 2/6 | 已并入 WM-12 archtest 作为扩展规则集。YAML 验证 + governance rules（40+ 条已实现）+ archtest 已覆盖。编译期验证需 go generate，增加构建复杂度 | — |
| WM-31 | 跨协议元数据同步 — HTTP/gRPC/WS 间 metadata 传播 | 0/6 | GoCell 当前纯 HTTP，无第二协议。MDM 的 SyncML 可在 MDM 侧的 handler 中处理 | 等引入 gRPC 时再做 |
| WM-34b | Kratos 两层中间件 — 协议无关层 + HTTP 专用层统一 | 2/6 | GoCell 已有两套中间件：HTTP `func(http.Handler) http.Handler` + Event `TopicHandlerMiddleware`。两者 handler 签名本质不同，强制统一增加抽象层无实际收益。DX 席明确反对："GoCell 不是 Kratos，现有中间件模型是 DX 优势" | 保持现有两套中间件各自演进 |

---

## 执行路线图（六席位审查后更新）

> 来源: 2026-04-11 架构/安全/测试/运维/DX/产品六席位独立探索后汇总
> 原则: 自底向上（pkg → kernel → runtime → adapters）、每 batch 独立可交付、安全风险递减
> 总周期: ~2.5 周（5 个 Batch）

### Batch 1: pkg 层稳定 ✅ 已完成

| 轨道 | 任务 | PR | 状态 |
|------|------|-----|------|
| A | WM-9/10/11 errcode 三合一 | PR#69 | ✅ |
| B | WM-12 archtest 边界守护（5 条 LAYER 规则，49 tests） | PR#73 | ✅ |

### Batch 2: 架构修复 ✅ 已完成

| 轨道 | 任务 | PR | 状态 |
|------|------|-----|------|
| A | Phase 1: PermanentError → kernel | PR#74 | ✅ |
| A | Phase 2: EventRouter 引入 | PR#76 | ✅ |
| B | 0-G B-03 RabbitMQ 重连 backoff | PR#75 | ✅ |
| B | K-1/K-2/K-3/K-5 Kernel 架构整理 | PR#79 | ✅ |
| — | device-cell 测试对齐 data 信封 + celltest 覆盖率 | PR#78 | ✅ |

### Batch 3: Tier 0 收尾 + 基础稳定（2d，Batch 2 后）

> Batch 2 完成架构修复后，继续 Relay 重写 + HTTP 产品化。

| 轨道 | 任务 | 预估 | 交付物 |
|------|------|------|--------|
| A | 0-B2 RL-01~08 Outbox Relay 三阶段重写 | 1.5d | claim→publish→writeBack + 8 场景测试 |
| A | **Phase 3: Checker 清理 + Receipt 加固** | 3h | 删 legacy + sync.Once + LeaseTTL 校验 |
| A | RMQ-75-01~04 PR#75 review 收尾 | 1.5h | flaky test + MaxReconnectAttempts + 命名 + godoc（搭 Phase 3 同包改动） |
| A | RMQ-75-05 readiness 接 rabbitmq Health() | 30min | `RegisterChecker("rabbitmq", conn.Health)` — 提前自 Batch 6 |
| B | HR-02 metrics 基数爆炸修复 (PROM-01) | 2h | route pattern 元数据替代 r.URL.Path |
| B | HR-01/03/04 HTTP 产品化收尾 | 4h | RealIP 决策 + RequestID bridge + tracing 决策 |
| B | 0-H SF-01~04 DecodeJSONStrict | 3h | 严格模式 + handler 迁移 |
| C | HT-01/02 handler decode 回归测试 | 3h | order/device handler 兼容性锁定 |
| C | OB-02 safe_observe broken logger 测试 | 1h | panic guard 补全 |
| C | 25+ handler WriteError → WriteErrorWithContext 批量改造 | 2h | WM-10 全量适配 |

**安全底线**: outbox 消息不丢失；decode 恶意 payload 有防护
**测试策略**: Relay 8 场景覆盖 ≥ 90%；handler decode 路径 ≥ 80%
**运维增益**: metrics 基数可控（< 100 series）；outbox 状态可观测；rabbitmq permanent error 可被 readiness 探针感知
**DX 增益**: 所有 handler 统一错误响应格式

### Batch 4: P1 功能 — 安全 + 查询（2d，Batch 3 后或并行后期）

> 安全席位要求 CSRF + 密钥轮换尽早；DX 要求游标分页尽早。
> 可与 Batch 3 后期部分并行。WM-1 已提前完成。

| 轨道 | 任务 | 预估 | 交付物 |
|------|------|------|--------|
| A | ~~WM-1 CSRF 中间件~~ | — | PR#77 ✅ |
| A | WM-2 密钥轮换（JWT kid + HMAC，范围限定） | 2d | `runtime/auth` KeyRotator + kid 支持 |
| B | WM-6 游标分页 | 1.5d | `pkg/query` KeysetPagination + HMAC cursor |
| B | WM-34 配置热更新回调 | 1d | `runtime/config` Cell 级 OnConfigReload |

**安全底线**: CSRF 防护覆盖所有 POST/PUT/DELETE；密钥泄露后可轮换止损
**测试策略**: CSRF table-driven；cursor HMAC 签名验证 + 边界条件；key rotation 并发测试
**DX 增益**: 分页从 O(n) → O(log n)；配置变更无需重启

### Batch 5: P1 功能 — 事件 + 生命周期（1.5d，Batch 3 后）

> 测试席位强调 WM-20 TestPubSub 是测试基础设施投资（ROI 最高之一）。
> 架构师要求 WM-17 必须用可选接口，不改 Cell 核心方法。

| 轨道 | 任务 | 预估 | 交付物 |
|------|------|------|--------|
| A | WM-20 TestPubSub 认证测试套件 | 1.5d | `kernel/outbox/outboxtest/` 标准 Publisher/Subscriber 套件 |
| A | WM-33b 熔断器 | 0.5d | `adapters/` sony/gobreaker 包装 |
| B | WM-17 生命周期钩子（BeforeStart/AfterStart/BeforeStop/AfterStop） | 1d | `kernel/cell` 可选接口（type assertion） |
| B | WM-15 L4 队列状态机 | 1.5d | `kernel/outbox` 合入 0-B2，状态 enum + 超时检测 |
| B | ER-ARCH-02 EventRouter ConsumerGroup 支持 | 2h | `AddHandler(...HandlerOption)` + `WithConsumerGroup`，修复 competing consumers |

**安全底线**: 熔断器防级联故障；AfterStop 清理敏感资源
**测试策略**: TestPubSub 标准套件 12 场景；lifecycle hooks 回归验证（不注册钩子时行为不变）
**运维增益**: OPS-3 readiness 探针可通过 AfterStart 注册；adapter 健康检查
**DX 增益**: Cell 开发有标准事件测试模板；启动流程清晰可调试

### Batch 6: Review Findings + Tech Debt（2d）

| 任务 | 预估 | 说明 |
|------|------|------|
| F-5 Journey catalog 校验 | 2h | kernel/journey |
| R1C2-F01 eventbus Close+Subscribe 竞态 | 2h | runtime/eventbus（需 -race 验证） |
| R1C2-F03 WorkerGroup 首个失败 | 2h | runtime/worker |
| ~~F-OB-01 outbox 批量写~~ | ~~2h~~ | ~~kernel/outbox~~ PR#79 ✅ |
| TX-NIL-01 txRunner nil-safe 文档 | 1h | cells/ |
| F-OB-02 outbox UUID nil guard | 30min | adapters/postgres — 拒绝全零 UUID 防幂等碰撞 (discovered via PR#79 review F-2.3) |
| P4-TD-01 noopWriter 去重 | 1h | cells/*/cell_test.go → 提取到 kernel/outbox/outboxtest (discovered via PR#79 review F-5.2) |
| CI-01 integration job 补 tests/integration/ | 30min | .github/workflows/ci.yml 只跑 ./adapters/...，漏掉 tests/integration/ (discovered via PR#79 review) |
| OB-UUID-01 cells evt-\<uuid\> 与 Writer UUID 校验冲突 | 2h | cells 生成 `evt-<uuid>` 前缀 ID，但 outbox_writer.go 只接受 canonical UUID (discovered via PR#79 review) |
| P3-TD-10 Session refresh TOCTOU | 4h | 高风险，乐观锁方案 |
| P4-TD-03 IssueTestToken 死代码 | 30min | runtime/auth |
| OPS-3 readiness 探针接 postgres/redis | 2h | 实现 Health() + 注册 HealthChecker（rabbitmq 已提前至 Batch 3） |
| OPS-4 优雅关闭 drain 期 | 1h | bootstrap shutdown |
| CI-01 integration job 补 tests/integration/ | 30min | .github/workflows/ci.yml:101-102 只跑 ./adapters/...，漏掉 src/tests/integration/ (discovered via PR#79 review) |
| OB-UUID-01 cells evt-<uuid> 与 Writer UUID 校验冲突 | 2h | cells 生成 `evt-<uuid>` 前缀 ID，但 outbox_writer.go 只接受 canonical UUID；需统一 ID 生成策略或放宽校验 (discovered via PR#79 review) |

### 总时间线（修订后，+6 个 Batch）

```
Week 1:
  Day 1-2: Batch 1 (pkg: errcode 三合一 + archtest) ✅ 已完成

Week 2:
  Day 1:   Batch 2 (架构修复: Phase 1 + Phase 2 + B-03) ✅ 已完成
  Day 2-3: Batch 3 (Tier 0 收尾: Phase 3 + Relay + HTTP + handler)
  Day 4-5: Batch 4 (CSRF + 密钥轮换 + 游标分页 + 配置热更新)
           ┊ Batch 5 部分并行启动 (WM-17 lifecycle hooks 无前置依赖)

Week 3:
  Day 1-2: Batch 5 (TestPubSub + 熔断器 + L4 状态机)
  Day 3-4: Batch 6 (Review Findings + Tech Debt)
  Day 5:   → v1.0 Release Candidate

后续:
  Review R1E/R1F+G/R2-R5 → Tech Debt P2 → 发布准备 → v1.0
```

### 价值递增里程碑

| 阶段 | winmdm 可感知价值 | 运维成熟度 |
|------|-----------------|-----------|
| Batch 1 后 | 框架有"安全带"：错误链路清晰，分层守护自动化 | L1: 可追踪 |
| **Batch 2 后** ✅ | **事件消费架构对齐行业标准**: 100ms 竞态消除、PermanentError 全栈生效、goroutine 有监管 | **L2: 可监控** |
| Batch 3 后 | 依赖可预测：outbox 不丢消息，metrics 有意义，legacy 清理完成 | L2.5: 可运维 |
| Batch 4 后 | 开发效率提升：分页 O(1)，配置热更新，密钥可轮换 | L3: 可自愈 |
| Batch 5 后 | 事件系统有标准测试，生命周期清晰，熔断防护 | L3.5: 可保证 |
| Batch 6 后 | 所有已知 bug 修复，Tech Debt 收敛 | L4: 生产就绪 |
