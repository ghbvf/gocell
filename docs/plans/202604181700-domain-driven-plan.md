# GoCell 域驱动实施计划

> 生成日期: 2026-04-18 17:00
> 更新: 2026-04-18（同步 PR#170~174）
> 基准: develop@dde5cae（PR#165-169 合并后）
> 策略: 按**功能域**聚合，每域所有层（kernel/runtime/adapter/slice）改动一次性在一个 PR 串内闭合
> 替代: `20260418-bottom-up-implementation-plan.md`（按层组织，Phase K/R/P/A/S/F）
>
> 标记说明:
> 🟡 可延后 = 不卡正确性或安全（latent risk / DX / tech debt / 打磨）
> 🟠 条件延后 = 有明确触发条件，触发前可延

---

## 设计决策维持（不重开）

| 决策 | 结论 |
|------|------|
| F1-3 DurabilityDurable + in-memory 合法 | 维持（effectiveMode + adapterInfo + slog 日志已透明；PG-REPO 是真修路径）|
| F1.2/F2.5/F4.2/F6.2 auth 四项 | 维持（详见 backlog 设计决策记录 PR#159）|
| F6 slice.yaml allowedFiles 双路径 | 维持（项目惯例，FMT-14 守护）|
| I2 generator 全量遍历 contracts | 维持（FMT-09 守护 + fail-fast defense-in-depth）|
| T6 GOCELL-PER-CELL-ADAPTER-01 不做 | 已裁决：全量 PG 接入，全局开关直接复用 |

---

## 域 1：Auth 域

**目标**：完成 aud claim 强验证、auth 集成测试覆盖、AUTH-DX 文档，使认证层对标 RFC 8725 / RFC 9068。

**域间依赖**：无前置域，是其他域的安全基础，Wave 1 优先启动。

| # | 任务 | 工时 | 优先级 | 文件 | 来源 |
|---|------|------|--------|------|------|
| X10 | **AUTH-REFRESH-OPAQUE-01** 🟠（PG-REPO 域上线后触发）：refresh token 改 opaque string + server-side rotation store（RFC 6819 §5.2.2.2）| 1-2d | 🟠 | `runtime/auth/` + `adapters/postgres/` | PR#166 R1-F2-7 |
| S18 | **JWT-AUDIENCE-ENV-VAR-01** (P1, Cx2, 🟠 条件延后，多环境部署前触发): `jwtAudience` 为编译期常量，多环境无法通过 env var 区分；`GOCELL_JWT_AUDIENCE` env var + `WithTokenAudience(string)` option；`real` 模式下非空强制；sessionlogin/sessionrefresh 同步注入。前置：独立 ADR | 3h | 🟠 | `cmd/core-bundle/main.go` + `cells/access-core/cell.go` + sessionlogin/sessionrefresh | PR#170 review F-O-1 |
| S19 | **JWT-AUDIENCE-DRIFT-INTEG-TEST-01** (P2, Cx1, 🟡 可延后，S18 落地后易实现): 集成测试调用 sessionlogin.Service.Login → 解析 token → VerifyIntent，检测 audience drift 编译失败 | 2h | 🟡 | `cmd/core-bundle/` + `cells/access-core/slices/sessionlogin/` | PR#170 review F-T-3 |
| S20 | **JWT-AUDIENCE-STARTUP-LOG-01** (P3, Cx1, 🟡 可延后): `buildJWTDeps` 构建时打印 effective audience；`slog.Info("JWT audience enforcement enabled", slog.String("audience", jwtAudience))` | 0.5h | 🟡 | `cmd/core-bundle/main.go` | PR#170 review F-S-5 |
| S21 | **JWT-AUD-TEST-TABLE-DRIVEN-01** (P3, Cx1, 🟡 可延后): `runtime/auth/jwt_aud_test.go` 9 个场景重构为 table-driven，符合 CLAUDE.md 规范 | 1h | 🟡 | `runtime/auth/jwt_aud_test.go` | PR#170 review F-T-5 |
| S22 | **REFRESH-AUD-REAL-ROUTE-TEST-01** (P2, Cx2, 🟡 可延后): 补真实 HTTP 路由测试：POST `{"refreshToken": wrong-aud-token}` → `/api/v1/access/sessions/refresh` 断言 401；missing-aud token 场景 | 2h | 🟡 | `cells/access-core/auth_integration_test.go` | PR#171 外部审查 F2 |
| S31 | **JWT-ISSUER-STRICT-01** 🟡 (P2, Cx2)：`VerifyIntent` 缺 issuer 强约束，跨环境密钥复用时 staging token 可通过 prod 验证；`WithExpectedIssuer` option + real 模式非空强制 + 负向测试。建议搭车 S18 | 1h | 🟡 | `runtime/auth/jwt.go` + `cmd/core-bundle/main.go` | 2026-04-18 六席审查 |
| S32 | **CONTROLPLANE-TOKEN-PROD-GATE-01** 🟠（real 模式部署前触发）：non-real 模式 `/internal/v1/` 依赖部署隔离；`real` 下断言 service-token/mTLS 至少一项 + CI real-mode smoke；对标 Kratos auth/selector 默认拒绝 | 1h | 🟠 | `cmd/core-bundle/main.go` + `runtime/bootstrap/bootstrap.go` | 2026-04-18 六席审查 |
| S35 | **AUTH-MIDDLEWARE-EXEMPT-INJECTION-01** (P3, Cx3, 🟡 可延后): `isPasswordResetExempt` hardcode 业务路径，违反 runtime/cells 分层；对标 Kratos selector，用 `auth.WithSkipPasswordResetCheck(func(method, path) bool)` Option 由 cell 注入 | 3h | 🟡 | `runtime/auth/middleware.go` + `cells/access-core/cell.go` | PR#183 reviewer round 2 P1-7 |

**域内 PR 拆分**：

| PR | 内容 | 工时 |
|----|------|------|
| PR-AUTH-ISSUER | S31 + S32：issuer 强约束 + controlplane 生产门禁（搭车 S18 PR）| 2h |
| PR-AUTH-JWT-AUDIENCE | S18：JWT audience env var + WithTokenAudience option（🟠 多环境部署前）| 3h |
| PR-AUTH-JWT-TEST | S19 + S20 + S21 + S22：audience drift 集成测试 + 启动日志 + table-driven（🟡）| 5.5h |
| PR-AUTH-OPAQUE | X10：refresh token 不透明化（🟠 PG-REPO 后触发）| 1-2d |

**主线工时**：0h（P1-10 ✅）；🟠 3h（S18）+ 🟡 8.5h（S19/S20/S21/S22/S31/S32/S35）；条件项另算。

---

## 域 2：Config-core 域

**目标**：修复 demo fail-open 语义漂移、完善契约错误 schema、降低 Init 认知复杂度，使 config-core 发布路径语义正确。

**域间依赖**：无强前置，与 Auth 域可并行（改不同文件）。

**全部完成** ✅ PR#175（P1-2 fail-open 删除）+ PR#181（S2/S13）+ commit 8552b6c（S11）；S10 ❌ 废弃。

**主线工时**：0h。

---

## 域 3：PG 加固域

**目标**：强化 PostgreSQL readiness 检查、补齐在线安全索引、完善 relay 集成测试，使 PG adapter 生产就绪。

**域间依赖**：A12 无前置，Wave 1 可立即启动。A4/A3 可搭车或独立排期。

**全部完成** ✅ PR#173（A12 schema guard + A3 relay 集成测试 + A4 migration 006 + T7 config_versions index）。

**主线工时**：0h。

---

## 域 4：Outbox/RabbitMQ 域

**目标**：修复 outbox relay worker 未接入 PG 模式、补齐 broker health check、完善 subscriber 校验、将 relay/distlock 生命周期上抬到 runtime 层，使 outbox 链路端到端可靠。

**域间依赖**：RBAC 域 S8 依赖本域 A11（已完成）。A10 已迁出至域 9。

| # | 任务 | 工时 | 优先级 | 文件 | 来源 |
|---|------|------|--------|------|------|
| A13 | **SUBSCRIBER-CONCURRENCY-DECISION-01** 🟠（PR#180 review 暴露）：`subscriber.go::consumeLoop` 同步调用 `processDelivery`，使 `PrefetchCount=10` 默认值的并发语义退化为串行。需先决定 PrefetchCount 真实意图：(a) 改文档明确串行；或 (b) 改 `go s.processDelivery(...)` 走真并发并补并发安全测试（审 settleReceipt / Receipt.Commit/Release 多 goroutine 安全）| 1-3h（视方向）| 🟠 | `adapters/rabbitmq/subscriber.go` | PR#180 reviewer |
| A14 | **BACKOFF-DEDUP-01** 🟡（PR#180 review 暴露）：`exponentialDelay` 在 `adapters/rabbitmq/backoff.go` 与 `kernel/outbox/consumer_base.go` 重复实现。两路径：(a) export `kernel/outbox.ExponentialDelay` 并删 adapters 副本；或 (b) 抽 `pkg/backoff` 共享。需调整 `adapters/rabbitmq/connection.go` 调用点 | 1-2h | 🟡 | `adapters/rabbitmq/backoff.go` + `adapters/rabbitmq/connection.go` + `kernel/outbox/consumer_base.go`（或新建 `pkg/backoff/`）| PR#180 reviewer |
| P1-14 | **ENVELOPE-FAILCLOSED-01** (P1, Cx2)：relay 发布 envelope 后 consumer 遇 unknown action 静默跳过 + 解析不完整走 legacy fallback，两者组合 fail-open；加版本判别位 + unknown action → DispositionRequeue | 2h | P1 | `runtime/outbox/envelope.go` + `runtime/eventbus/` consumer dispatch | 2026-04-18 六席审查 |
| P1-15 | **RELAY-READINESS-BUDGET-01** (P1, Cx3)：relay 连续失败仅日志不降级 `/readyz`；引入失败预算计数器超阈值 → unhealthy；对标 K8s workqueue 失败预算 + go-micro critical probe | 3h | P1 | `runtime/outbox/relay.go` + `runtime/bootstrap/bootstrap.go` + health checker 接线 | 2026-04-18 六席审查 |

**搭车说明**：A7（POOLSTATS-IFACE-01）可单独 PR 或搭车域 9。

**域内 PR 拆分**：

| PR | 内容 | 工时 |
|----|------|------|
| PR-OUTBOX-ENVELOPE-FAILCLOSED | P1-14：envelope 协议边界 fail-closed + unknown action → Requeue | 2h |
| PR-OUTBOX-RELAY-READINESS | P1-15：relay 失败预算 → readiness 降级 + health checker 接线 | 3h |
| PR-OUTBOX-A13-CONCURRENCY | A13：subscriber 串行/并发裁决（🟠，需先定 PrefetchCount 意图）| 1-3h |
| PR-OUTBOX-A14-DEDUP | A14：exponentialDelay 去重（🟡）| 1-2h |

> **注意**：backlog A13（BOOTSTRAP-WIRE-RMQ-BROKER-HEALTH-01）是条件延后项 — `cmd/core-bundle` 接入真实 RabbitMQ connection 时，通过 `bootstrap.WithBrokerHealth` 将 RMQ readiness 纳入 `/readyz`（2h，🟠）。触发前不占主线。

**主线工时**：5h（P1-14/15）；全做约 12h（含 A7/A13/A14 follow-up）。

---

## 域 5：RBAC 域

**目标**：修复 revoke HTTP 方法、补齐最后 admin 保护、将角色变更接入 transactional outbox，使 RBAC 操作原子安全。

**域间依赖**：S5/S6 无前置，Wave 2 可提前并行；S8（RBAC-OUTBOX-MIGRATION）强依赖 Outbox/RabbitMQ 域（A11 outbox consumer 基础设施）。

**全部完成** ✅ PR#176（S8 transactional outbox）；S5/S6 早于计划落地。

**主线工时**：0h。

---

## 域 6：HTTP/Router 域

**目标**：升级公共端点 method-aware 匹配、收口 handler nil 语义、统一 auth guard inline，消除路由层 latent risk。

**域间依赖**：P1-3/S3 改 `runtime/http/router/router.go` + handler，与 Auth 域（改 `runtime/auth/jwt.go`）文件不重叠，Wave 2 可并行。R4（INTERNAL-LISTENER）改 `runtime/bootstrap/bootstrap.go`，与域 9 R1 同文件，建议 R4 先于 R1 或同域合并。

| # | 任务 | 工时 | 优先级 | 文件 | 来源 |
|---|------|------|--------|------|------|
| S3 | **DTO-NIL-SEMANTIC-01** (Cx2)：12+ handler 写成功响应前校验领域对象非 nil，避免空 data 成功响应 | 3h | P2 | 12+ `cells/*/slices/*/handler.go` | PR#158 six-seat review |
| S12 | **AUTH-GUARD-INLINE-UNIFY-01** 🟡 (Cx3)：全库 11 处 `RequireAnyRole → WriteDomainError → return` 统一提取，横跨 3 cell | 2h | 🟡 | `cells/*/slices/*/handler.go` × 11 | PR#168 review P2 |
| R4 | **INTERNAL-LISTENER-01** 🟡 (Cx4)：`/internal/v1/` 独立 listener 或 service-token/mTLS 策略；4-8h 上界，规模风险 | 4-8h | 🟡 | `runtime/bootstrap/bootstrap.go` + 路由注册拆分 | PR#143 review F1 |

**风险说明**：R4 若实施 blast radius 超预期，降级到 PG-REPO 大项后处理，不阻塞主线。

**域内 PR 拆分**：

| PR | 内容 | 工时 |
|----|------|------|
| PR-ROUTER-DTO-NIL | S3：DTO nil 语义 12+ handler | 3h |
| PR-ROUTER-GUARD | S12：auth guard inline 统一（🟡）| 2h |
| PR-ROUTER-INTERNAL | R4：internal listener 独立（🟡 规模评估后排期）| 4-8h |

**主线工时**：3h（S3 剩余）；全做约 13h。

---

## 域 7：Events/DTO 域

**目标**：将 6 个 slice 的事件 payload 从 `map[string]any` 升级为 typed event struct，提升事件契约稳定性和类型安全。

**域间依赖**：S4 改动 3 个 cell（access-core + config-core + audit-core），建议 Auth 域和 Config-core 域稳定后再做，避免 rebase 冲突。Wave 3 启动。

| # | 任务 | 工时 | 优先级 | 文件 | 来源 |
|---|------|------|--------|------|------|
| S4 | **EVENT-PAYLOAD-TYPED-01** (Cx2)：sessionlogin/sessionlogout/configwrite/configpublish/auditappend/auditverify 事件 payload `map[string]any` → typed event struct | 3h | P2 | 6 个 `service.go` + event contract schemas | PR#133 re-review |

**域内 PR 拆分**：

| PR | 内容 | 工时 |
|----|------|------|
| PR-EVENTS-TYPED | S4：6 个 service.go typed event struct + schemas | 3h |

**主线工时**：3h。

---

## 域 8：Features 域

**目标**：实现 Device List API 和 Flag Write API 补齐前后端联调缺口，AUTH-DX 文档更新在 Auth 域稳定后收口。

**域间依赖**：P1-8/P1-9 无强依赖，Wave 2 可启动；P1-10（AUTH-DX）前置 Auth 域（PR-AUTH-AUD + PR-AUTH-INT）稳定；F2（SYSTEM-TOPOLOGY-API）可独立延后。

| # | 任务 | 工时 | 优先级 | 文件 | 来源 |
|---|------|------|--------|------|------|
| P1-12 | **AUTH-SETUP-01 First-Run Setup 模式** (P1, Cx3): `GET /api/v1/setup/status`（返回 `{"data":{"setupRequired":bool}}`）+ `POST /api/v1/setup/admin`（仅在无 admin 时有效，之后返回 409）；两端点加入 `WithPublicEndpoints`；新 `setup` slice + contract；**去掉 sso-bff seed 用户**（含明文密码 slog.Info — PR#172 F1 defer）改由 setup 流程创建 | 6h | P1 | `cells/access-core/slices/setup/` + `contracts/http/auth/setup/` + `examples/sso-bff/` | AUTH-DX-01 讨论 + PR#172 F1 |
| P1-8 | **FEAT-1 DEVICE-LIST-API**：新建 `device-list` slice + `GET /api/v1/devices` 分页 + contract + contract_test | 3h | P1 | `cells/device-cell/slices/device-list/` + `contracts/http/device/list/v1/` | backend_issues.md #1 |
| P1-9 | **FEAT-2 FLAG-WRITE-API**：`PUT /api/v1/config/flags/{key}` 写入端点 + contract + contract_test | 3h | P1 | `cells/config-core/slices/configwrite/` + `contracts/http/config/flags/write/v1/` | backend_issues.md #2 |
| P1-13 | **SSO-BFF-WALKTHROUGH-JWT-FIX-01** (P0 DX, Cx1)：README walkthrough 第 10/11 步（读配置/flags）未携带 JWT，401；文档/测试/白名单三处无单一真源；补 Bearer header + walkthrough test 断言 | 1h | P1 | `examples/sso-bff/README.md` + `examples/sso-bff/walkthrough_test.go` | 2026-04-18 六席审查 |
| S23 | **AUTH-WALKTHROUGH-COMPOSE-01** (P2, Cx3, 🟡 可延后): 提取 `NewSSOBFFApp(opts...)` 被 `main.go` 和 walkthrough test 共用；test server 走同一 bootstrap + Start/Stop 路径 | 4h | 🟡 | `examples/sso-bff/bootstrap.go`（新）+ `main.go` + `walkthrough_test.go` | PR#172 review F3 |
| S29 | **CORE-BUNDLE-APP-BUILDER-01** (P2, Cx3, 🟡 可延后): 抽出 `cmd/core-bundle.BuildApp(opts...) (*bootstrap.Bootstrap, func(), error)` 被 main 和 integration test 共用，避免 bootstrap options 拓扑事实源再次分裂。对标 Uber fx `App.New` + Kratos `App.Run` | 6-8h | 🟡 | `cmd/core-bundle/app.go`（新）+ `main.go` + `outbox_e2e_integration_test.go` | PR#174 review F1 |
| S34 | **AUTH-ADMIN-FORCE-RESET-01** (P2, Cx2, 🟡 可延后): Admin 无旧密码强制重置端点 `POST /api/v1/access/users/{id}/password/reset`；生成临时密码 + `PasswordResetRequired=true`；前置：凭据传递通道决策 + P1-12b first-login enforcement 配套 | 4h | 🟡 | `cells/access-core/slices/identitymanage/` + `contracts/http/auth/user/reset/v1/`（新）| PR#183 reviewer round 2 P1-2 |
| F2 | **SYSTEM-TOPOLOGY-API** 🟡：`GET /internal/v1/system/topology` 返回 cell/slice/contract 拓扑 JSON | 4h | 🟡 | 新 slice 或 `runtime/bootstrap/` | 历史 Batch 8 |

**域内 PR 拆分**：

| PR | 内容 | 工时 |
|----|------|------|
| PR-FEAT-SETUP | P1-12：First-Run Setup 模式（GET setup/status + POST setup/admin）| 6h |
| PR-FEAT-DEVICE | P1-8：device-list slice + 分页 API | 3h |
| PR-FEAT-FLAG | P1-9：flag-write 端点 | 3h |
| PR-FEAT-WALKTHROUGH-FIX | P1-13：README JWT 补全 + walkthrough test 断言（P0 DX，独立小 PR）| 1h |
| PR-FEAT-APP-BUILDER | S29：BuildApp 统一装配函数 + S23：walkthrough 共享 bootstrap（🟡）| 8-12h |
| PR-FEAT-ADMIN-RESET | S34：admin 强制密码重置端点（🟡）| 4h |
| PR-FEAT-TOPOLOGY | F2：topology API（🟡 可延后）| 4h |

**主线工时**：13h（P1-8 + P1-9 + P1-12 + P1-13）；全做约 27h（含 S23/S29/S34）。

---

## 域 9：可观测性/Bootstrap 域（🟡 全部可延后）

**目标**：拆分 bootstrap.go 认知复杂度、自动接线 HTTP collector、统一三 adapter PoolStats 接口、夜间 OTLP 协议兼容测试。

**域间依赖**：R1 改 `runtime/bootstrap/bootstrap.go`，与 HTTP/Router 域 R4 同文件，建议 R4 完成后再做 R1。A7（POOLSTATS-IFACE-01）改 `adapters/rabbitmq/connection.go`，建议搭车 Outbox 域 PR-OUTBOX-WIRE（同文件）。A10 从域 4 迁入（`adapters/otel/` 与 outbox 无直接耦合，归属可观测性域更合适）。

| # | 任务 | 工时 | 优先级 | 文件 | 来源 |
|---|------|------|--------|------|------|
| R1 | **BOOTSTRAP-RUN-COGNIT-01** 🟡 (Cx3)：`bootstrap.go::Run()` 认知复杂度 225；拆 `validateOptions()` / `buildRouter()` / `startServers()` 三段式 | 4h | 🟡 | `runtime/bootstrap/bootstrap.go` | PR#163 agent 报告 |
| R2 | **OBS-HTTP-COLLECTOR-AUTOWIRE-01** 🟡 (Cx3)：`bootstrap.WithMetricsProvider` 自动构造 `NewProviderCollector`；`WithHTTPCollectorCellID` option | 2h | 🟡 | `runtime/bootstrap/bootstrap.go` + `runtime/http/middleware/` | PR#157 review S4-01 |
| R3 | **OB-02** 🟡：safe_observe broken logger 注入测试 | 1h | 🟡 | `runtime/http/middleware/safe_observe_test.go` | 历史 backlog 0-J |
| A10 | **OBS-LGTM-INTEGRATION-01** 🟡（从域 4 迁入）：`//go:build integration` 夜间 OTel collector 真实 OTLP 协议兼容性测试 | 2h | 🟡 | `adapters/otel/integration_test.go` | PR#157 review S6-04 |
| A7 | **POOLSTATS-IFACE-01** 🟡：三个 adapter PoolStats 公共接口（OTel collector 消费）；**建议搭车 Outbox 域 PR-OUTBOX-WIRE** | 1h | 🟡 | `adapters/postgres/pool.go` + `redis/client.go` + `rabbitmq/connection.go` | PR#134 review |
| S36 | **WORKER-LAZY-HOIST-01** (P3, Cx2, 🟡 可延后): `lazyBootstrapWorker` / `ssoBFFLazyWorker` / `e2eLazyWorker` 三处完全相同；提到 `runtime/worker.Lazy(get func() Worker) Worker` 框架级抽象。对标 Watermill Null Object 模式 | 2h | 🟡 | `runtime/worker/lazy.go`（新）+ `cmd/core-bundle/main.go` + `examples/sso-bff/main.go` | PR#183 reviewer round 2 P2-3 |
| S2-follow | **CONTRACT-ERROR-SCHEMA-EXTEND-01** (P2, Cx1, 🟡 可延后): 其余 HTTP contract（config/write、config/get、auth/login 等）补充 401/403 responses 声明，使错误响应 schema 全库覆盖 | 2h | 🟡 | `contracts/http/**/{publish,get,write,flags}/v*/contract.yaml` | PR-CONFIG-POLISH 后续 |
| S13-follow | **4XX-LOG-SAMPLING-01** (P3, Cx1, 🟡 可延后): 高频端点 4xx 日志加采样（1%）；PR#181 F3 已最小化 log4xx 字段，采样必要性已大幅下降；仅在运维告警通道过载时再做 | 1h | 🟡 | `pkg/httputil/response.go` | PR-CONFIG-POLISH 后续 |
| A14 | **DISTLOCK-LOST-METRIC-01** (P3, Cx2, 🟡 可延后，🟠 首个 distlock 消费方后触发): `Lock.Release` ErrLockLost 路径仅写 slog；应发射 `distlock_release_total{outcome="success|lost|error"}`；前置：runtime/observability label taxonomy 设计 | 2h | 🟡 | `runtime/distlock/` + `adapters/redis/distlock.go` + collector wiring | PR#178 round-4 |
| A15 | **DISTLOCK-RENEW-JITTER-01** (P3, Cx2, 🟡 可延后，生产 Redis 并发峰值告警触发): `renewLoop` ticker 硬编码 `ttl/2`，并发续租 thundering-herd；加 ±10-20% jitter。对标 Redsync `driftFactor` | 2h | 🟡 | `adapters/redis/distlock.go` | PR#178 round-4 |
| A16 | **DISTLOCK-RENEW-RATIO-CONFIGURABLE-01** (P3, Cx2, 🟡 可延后，首个 caller 调优诉求触发): 续租比例写死 `ttl/2`；增 `WithRenewRatio(float64)` option | 2h | 🟡 | `adapters/redis/distlock.go` + `Acquire` 参数 | PR#178 round-4 |

**域内 PR 拆分**：

| PR | 内容 | 工时 |
|----|------|------|
| PR-OBS-BOOTSTRAP | R1 + R2 + R3 + A10：bootstrap 拆分 + HTTP collector 自动接线 + safe_observe 测试 + OTLP 夜间测试 | 9h |
| A7 搭车 Outbox 域 | POOLSTATS-IFACE-01 合并到 PR-OUTBOX-WIRE | 1h |
| PR-OBS-CONTRACTS | S2-follow：全库 HTTP contract 401/403 响应声明（🟡）| 2h |
| PR-OBS-DISTLOCK | A14 + A15 + A16：distlock metric + jitter + ratio（🟡 P3 搭车）| 6h |
| PR-WORKER-LAZY | S36：worker.Lazy 框架抽象（🟡 P3）| 2h |

**主线工时**：10h（R1/R2/R3/A10/A7 全部 🟡 可延后）；含新增项约 25h（S36/S2-follow/S13-follow/A14/A15/A16）。

---

## 域 10：DX/CI/工具链域（🟡 全部可延后，P3 优先级）

**目标**：机器可读验证输出、metadata 解析性能基准、CI 供应链加固、全仓库现代化清理。

**域间依赖**：P1-4 + K1 改 `kernel/governance/` + `cmd/gocell/`，相关合并一 PR；S7/A8/A9 改 `.github/workflows/ci.yml`，合并一 PR；X9 全仓清理独立 PR，不混功能 PR。

| # | 任务 | 工时 | 优先级 | 文件 | 来源 |
|---|------|------|--------|------|------|
| P1-4 | **OUTPUT-JSON-SARIF-01** 🟡 (P1, Cx3)：`gocell validate` 新增 JSON + SARIF 输出通道；统一诊断模型 `Issue` struct → 多 printer | 6h | 🟡 P1 | `cmd/gocell/` + `kernel/governance/` | PR#152 round-2 review |
| K1 | **METADATA-PROJECTLOC-IFACE-01** 🟡 (P2, Cx3)：提取 `ProjectLocator interface` 隐藏 yaml.v3 AST；`FileNodes` 不再泄漏 | 3h | 🟡 | `kernel/metadata/` + `kernel/governance/` + `cmd/gocell/` | PR#152 seat-1 |
| P1-5 | **METADATA-PERF-BENCH-01** 🟡 (P1, Cx3)：`BenchmarkParseFS_500Files` 性能基准 + goccy/go-yaml 迁移成本评估 | 4h | 🟡 P1 | `kernel/metadata/parser_test.go` | PR#152 seat-4 |
| S7 | **VALIDATE-EVIDENCE-CI-01** (P2, Cx2)：CI 新增独立 `metadata-check` job 失败阻断 PR；PR template 增 metadata gate 勾选项 | 1h | P2 | `.github/workflows/ci.yml` + PR template | PR#155 review F7 |
| A8 | **CI-DIGEST-01** 🟡：testcontainers 镜像 tag+digest 双固定 | 1h | 🟡 | `adapters/*/integration_test.go` | PR#139 review |
| A9 | **CI-LINT-PIN-01** 🟡：golangci-lint patch 级固定 + dependabot | 1h | 🟡 | `.github/workflows/ci.yml` | PR#139 review |
| X9 | **LINT-MODERN-01** 🟡 (P3, Cx2)：全仓 modernization 清理（rangeint / stringsseq / nhooyr.io→coder/websocket）；独立 PR，不混功能 | 6h | 🟡 P3 | 全仓 | PR#163 post-review |
| S33 | **WALKTHROUGH-CI-SMOKE-01** 🟡 (P2, Cx1)：`examples/sso-bff/walkthrough_test.go` 带 `//go:build integration` 未进 CI 主门禁；增 `walkthrough-smoke` job 或拆 unit subtests 接入默认 `go test ./...` | 1h | 🟡 | `.github/workflows/ci.yml` + `examples/sso-bff/walkthrough_test.go` | 2026-04-18 六席审查 |
| F3 | **P2-T-02 audit e2e 测试**：Journey 级验收（audit-core + access-core 全链路）| 2h | P2 | `journeys/` + integration test | 历史 Batch 8 |
| F10 | **TEST-JOURNEY-ASSEMBLY-HARNESS-01** (Cx3, 🟡 可延后): `tests/integration/journey_test.go` 全部 28 条均 `t.Skip("stub: requires full assembly")`；建 full-assembly harness 后统一恢复（J-session-refresh / J-session-logout / J-account-lockout / J-audit-login-trail / J-config-* 等）| 8h | 🟡 | `tests/integration/` + assembly fixture | PR#166 R2-P2 |

**域内 PR 拆分**：

| PR | 内容 | 工时 |
|----|------|------|
| PR-DX-VALIDATOR | P1-4 + K1：JSON/SARIF 输出 + ProjectLocator 接口 | 9h |
| PR-DX-METAPERF | P1-5：metadata parser 性能基准 | 4h |
| PR-DX-CI | S7 + A8 + A9 + S33：CI metadata gate + 镜像固定 + lint 固定 + walkthrough smoke | 4h |
| PR-DX-LINT-MODERN | X9：全仓现代化清理（独立 PR）| 6h |
| PR-DX-JOURNEY | F3 + F10：audit e2e 测试 + full-assembly journey harness（🟡 Wave 4）| 10h |

**主线工时**：25h（全部 🟡/P3 可延后，Wave 4 机会性纳入或 v1.0 后做）。

---

## 域 11：PG-REPO 大项（Phase X，独立排期）

**目标**：完成 access-core/audit-core/device-cell 的 PostgreSQL Repository 实现，激活 Redis session cache，完成 RBAC level 升级，使所有 cell 脱离内存存储。

**域间依赖**：最重量级的独立大项。本域完成后可触发 Auth 域 X10（AUTH-REFRESH-OPAQUE-01）。RBAC 域 S8 依赖 Outbox 域，不依赖本域。

**已完成（PR#169）**：`004_create_config_entries_and_versions.sql`、`005_recreate_outbox_pending_concurrent.sql`、config-core PG repo + 集成测试、`GOCELL_CELL_ADAPTER_MODE` config-core 接线。

**待做**：

| # | 任务 | 工时 | 备注 |
|---|------|------|------|
| X1-DDL | migration DDL：users / sessions / roles / devices+commands | 0.5d | `adapters/postgres/migrations/006+.sql` |
| X1-ACCESS | access-core PG repo（User / Session / Role）| 1d | `cells/access-core/internal/adapters/postgres/`（新建）|
| X1-AUDIT | audit-core / device-cell / order-cell PG repo | 0.5-1d | `cells/*/internal/adapters/postgres/` |
| X1-LINK | 落地联动：RBAC-ASSIGN-LEVEL-UPGRADE-01 L0→L1 + SEED-ROLE-IFACE-01 去 type assertion + ACCESS-LEVEL-AUDIT-01 slice.yaml 校正 + AUTH-CACHE-01 激活 Redis session cache | 1-2d | 同 PR 或紧邻 PR |
| S16 | **RUNTIME-TOPOLOGY-SINGLE-SOURCE-01** 🟡 (P2, Cx3)：运行拓扑单一事实源，消除 ENV 分裂（最小修复已合 PR#169；彻底方案随本域）| 6h | PR#169 review F-NEW-2 |
| S17 | **POOL-FRAMEWORK-LIFECYCLE-01** 🟡 (P2, Cx3)：外部资源提升为 bootstrap 托管，统一 LIFO shutdown + 自动 health checker 注册（最小修复已合；彻底方案随本域）| 4h | PR#169 review F-NEW-3 |
| S14 | **CONFIG-VALUE-ENCRYPTION-01** (P1↑, Cx3)：sensitive=true 明文存库；需 KMS 选型 + key rotation 独立 ADR，独立 PR | TBD | PR#169 + 2026-04-18 静态审查 |
| S15 | **ERROR-CTX-CANCELLED-CLASSIFY** 🟡 (P3, Cx2)：`ctx.Canceled` 归类 `ErrContextCanceled` | 1h | PR#169 review F-T-3 |

**域内 PR 拆分**：

| PR | 内容 | 工时 |
|----|------|------|
| PR-PGREPO-DDL | X1-DDL：006+ migration DDL | 0.5d |
| PR-PGREPO-ACCESS | X1-ACCESS：access-core PG repo + 集成测试 | 1d |
| PR-PGREPO-AUDIT-DEVICE | X1-AUDIT：audit-core + device-cell PG repo | 0.5-1d |
| PR-PGREPO-LINK | X1-LINK：落地联动（RBAC level + seed + cache 激活）| 1-2d |
| PR-PGREPO-TOPOLOGY | S16 + S17：runtime topology 单一事实源 + pool 托管（🟡 搭车）| 10h |
| PR-PGREPO-ENCRYPT | S14：config value 加密（独立 PR，需 KMS ADR）| TBD |

**域估算（主线 DDL + ACCESS + AUDIT + LINK）**：约 3-5d；含 S16/S17 彻底方案 +10h；S14 独立排期。

---

## 推荐执行 Wave

```
Wave 2（进行中）— 部分并行
  ├── Outbox 域   PR-OUTBOX-ENVELOPE-FAILCLOSED (P1-14, 2h)
  │              + PR-OUTBOX-RELAY-READINESS (P1-15, 3h)
  └── Features 域 PR-FEAT-SETUP (P1-12, 6h, P1) + PR-FEAT-DEVICE (3h) + PR-FEAT-FLAG (3h)
                  + PR-FEAT-WALKTHROUGH-FIX (P1-13, 1h)

Wave 3（依赖 Wave 2 完成）— 约 2-3 工作日
  ├── Events 域   PR-EVENTS-TYPED (3h)
  ├── HTTP 域     PR-ROUTER-DTO-NIL (3h)
  └── Auth 域     PR-AUTH-JWT-AUDIENCE (S18, 3h, 🟠 条件) + PR-AUTH-ISSUER (S31+S32, 2h)
                  + PR-AUTH-JWT-TEST (S19+S22, 4h, 🟡)

Wave 4（🟡 可延后，机会性纳入）— 按资源排期
  ├── 可观测性/Bootstrap 域  R1 + R2 + R3 + A10 (9h)
  ├── DX/CI/工具链域         S7 + A8 + A9 + S33 (4h) + P1-4/K1 (9h) + P1-5 (4h) + X9 (6h)
  ├── Outbox 域              PR-OUTBOX-HARDEN (~7h)
  ├── HTTP 域                PR-ROUTER-GUARD (2h) + PR-ROUTER-INTERNAL (4-8h)
  ├── Features 域            PR-FEAT-TOPOLOGY (4h) + PR-FEAT-APP-BUILDER (S29+S23, 8-12h) + PR-FEAT-ADMIN-RESET (S34, 4h)
  ├── DX/CI 域               PR-DX-JOURNEY (F3+F10, 10h)
  └── Observability 域       PR-OBS-CONTRACTS (S2-follow, 2h) + PR-OBS-DISTLOCK (A14+A15+A16, 6h)
                             + PR-WORKER-LAZY (S36, 2h) + S20/S21/S35 (🟡 P3, 机会性)

Wave 5（Phase X，独立大项，发布后按需排期）
  PG-REPO 大项   DDL → ACCESS → AUDIT-DEVICE → LINK → TOPOLOGY → ENCRYPT
                 约 3-5d + S16/S17 10h + S14 TBD

  触发后做：
    X10 AUTH-REFRESH-OPAQUE     （PG-REPO 上线后触发，1-2d）
```

---

## 域间依赖总览

```
Auth 域 ──────────────────────────────────────────────────────────► Features 域（P1-10 前置）
                                                                     ▲
Config-core 域 ───────────────────────────────────────────────────► Events/DTO 域（稳定后再做）

PG 加固域（A12 独立）

Outbox/RabbitMQ 域（K2 作为 WIRE 头部 commit；吸收原 A2；A10 迁出至域 9）
  └──► RBAC 域（S8 强依赖 A11 outbox consumer 基础设施）

HTTP/Router 域（R4）
  └──► 可观测性/Bootstrap 域（R1 同文件，R4 先于 R1）

PG-REPO 大项（Phase X，独立）
  └──► 触发 Auth 域 X10（AUTH-REFRESH-OPAQUE-01）
```

---

## 工时汇总表

| 域 | P1 主线工时 | 🟡 可延后工时 | 🟠 条件项工时 | 总计 | 状态 |
|----|------------|--------------|--------------|------|------|
| 1. Auth 域 | **0h**（P1-10 ✅）| 8.5h（S19/S20/S21/S22/S31/S32/S35）| 3h（S18）+ 1-2d | ~17h | Wave 1 核心 ✅；P1-10 ✅ PR#172 |
| 2. Config-core 域 | 0h | 7h | 3h | ~10h | P1-2/S2/S13 ✅ PR#181 |
| 3. PG 加固域 | 0h（A12 ✅）| 2h | 2.5h | ~5.5h | PR#173 ✅ 全部完成 |
| 4. Outbox/RabbitMQ 域 | **5h**（P1-14+P1-15；A11/K2/A1/X7/S37/S38 ✅）| 9h | 2h（A13 RMQ wire） | **~16h** | **A11/K2/A1/X7/S37/S38 ✅** |
| 5. RBAC 域 | 0h | — | — | ~6h | **S5/S6/S8 ✅ 全部完成** |
| 6. HTTP/Router 域 | 3h（S3 剩余；P1-3 ✅ PR#182）| 10h | — | ~13h | P1-3 ✅ PR#182 |
| 7. Events/DTO 域 | 3h | — | — | 3h | — |
| 8. Features 域 | 13h（P1-8+P1-9+P1-12+P1-13）| 14h（S23/S29/S34）| — | 27h | P1-10 ✅；P1-12 新增 |
| 9. 可观测性/Bootstrap 域 | — | 25h（含 S36/S2-follow/S13-follow/A14/A15/A16）| — | 25h（全🟡）| — |
| 10. DX/CI/工具链域 | 1h | 24h（+F3+F10）| — | 25h | — |
| 11. PG-REPO 大项（Phase X）| — | 3-5d + 10h | TBD | 独立排期 | — |
| **Wave 1-3 核心路径合计（更新）** | **~31h（约 4-5 工作日）** | | | | P1-12 +6h 新增 |
| **Wave 1-4 全量（不含 Phase X）** | | | | **~145h（约 18 工作日）** | |

---

## 触发条件项（不占主线排期）

| # | 任务 | 触发条件 |
|---|------|----------|
| T1 | AUTH-PROVIDER-EXPORT-01 | 第二个 auth provider cell |
| T2 | AUTH-ISSUE-OPTIONS-01 | `Issue()` 第 5 个参数 |
| T3 | DEVICE-ENQUEUE-RBAC | 多租户 operator |
| T4 | CB-RESILIENCE-PACKAGE-01 | 非 HTTP 的 CB 消费方 |
| T5 | AUTH-SIGNER-01 | golang-jwt v6 发布 |
| X10 | AUTH-REFRESH-OPAQUE-01 | PG-REPO 域上线后 |
| T8 | PUBLIC-ENDPOINT-STRUCT-MIGRATE-01 | 公共端点 > 20 个，或需 per-endpoint 元数据（rate-limit 豁免 / audit skip），或 method 字符串手误绕过 fail-fast |
| T9 | AUTH-BYPASS-METRICS-01 | observability 专项落地（R2 OBS-HTTP-COLLECTOR-AUTOWIRE-01），或 401 baseline 需 method 维度审计 |
| T10 | RMQ-STATTER-RENAME-01 | 独立小 PR，可随时做（无阻塞，15min）|
| T11 | AUTH-LOADKEYSFROMENV-UNEXPORT-01 | 独立小 PR，可随时做（无阻塞，30min）|
| P1-3a | CORS-OPTIONS-PUBLIC-ENDPOINT-01 | 新增 CORS middleware 时评估 OPTIONS * 是否加入公共端点 |
| A13 | BOOTSTRAP-WIRE-RMQ-BROKER-HEALTH-01 | cmd/core-bundle 接入真实 RabbitMQ connection（替换 in-memory eventbus）|
