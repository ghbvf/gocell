# GoCell Backlog

> 只含待办事项。已完成项归档至 `docs/reviews/archive/202604180035-backlog-pre-cleanup.md`。
> 更新日期: 2026-04-18（PR#184 同步）
> 基线: develop@fbd4244（PR#184 合并后）
> 最近合入概览: Wave 1 ✅ 全部完成 / Post-Wave 1 PR#141-165 按层偿债 + 外部审查回灌 / PR#170+171 auth aud 强验证 + S9 可观测 / PR#172 SSO-BFF config walkthrough + HMAC doc / PR#174 outbox broker health nil fail-fast + e2e wire guard / PR#175 P1-2 configpublish fail-open 删除 / PR#176 S8 RBAC-OUTBOX-MIGRATION transactional outbox / PR#177 S30+S28+X7 outbox store abstraction / PR#178 X8 distlock hoist to runtime / PR#180 A5/A6/X6 outbox harden + RMQ polish / PR#181 S2+S13 config-core polish / PR#182 P1-3 公共端点 method-aware / PR#184 Subscription first-class + lease-lost fence + Commit→Ack ordering
> 未合入外部 PR: PR#129 Sentinel DSN redaction / PR#130 Bolt journey catalog
> 标记说明:
> 🟡 可延后 = 不卡正确性或安全；latent risk / DX / 测试覆盖 / 纯 tech debt / 供应链加固 / 架构打磨 — 可机会性纳入或 v1.0 后做
> 🟠 条件延后 = 有明确触发条件（如首次 prod migration / PG 接线），触发前可延

---

## P1 待办

| # | 任务 | 工时 | 文件 | 来源 |
|---|------|------|------|------|
| P1-3a | **CORS-OPTIONS-PUBLIC-ENDPOINT-01** (P2, 🟠 条件延后，触发条件：新增 CORS middleware 时): 当前无 CORS middleware，OPTIONS 预检请求未处理。若未来引入 CORS middleware，需评估 `OPTIONS *` 是否应自动加入公共端点，或由 CORS middleware 自身短路（推荐）。引入 CORS 时同步更新 `.claude/rules/gocell/runtime-api.md` 和 `WithPublicEndpoints` godoc | 2h | `runtime/bootstrap/bootstrap.go` + `runtime/http/middleware/`（新 CORS 文件）+ `runtime/http/router/router.go` | PR#182 |
| P1-4 | **OUTPUT-JSON-SARIF-01** (Cx3, 🟡 可延后): `gocell validate` 缺机器可读输出通道（JSON/SARIF）。统一诊断模型（单一 `Issue` struct → 多 printer 映射）。对标 golangci-lint / staticcheck / ESLint / kubectl print flags | 6h | `cmd/gocell/` + `kernel/governance/` 序列化 | PR#152 round-2 review |
| P1-5 | **METADATA-PERF-BENCH-01** (Cx3, 🟡 可延后): `BenchmarkParseFS_500Files` 性能基准 + goccy/go-yaml 单次解码迁移成本评估 | 4h | `kernel/metadata/parser_test.go` | PR#152 seat-4 |
| P1-8 | **FEAT-1 DEVICE-LIST-API**: 新建 `device-list` slice + `GET /api/v1/devices` 分页 + contract + contract_test；同步触发 CONTRACT-LIST-LINT-01 规则 | 3h | `cells/device-cell/slices/device-list/` + `contracts/http/device/list/v1/` | backend_issues.md #1 |
| P1-9 | **FEAT-2 FLAG-WRITE-API**: `PUT /api/v1/config/flags/{key}` 写入端点 + contract + contract_test | 3h | `cells/config-core/slices/configwrite/` + `contracts/http/config/flags/write/v1/` | backend_issues.md #2 |
| ~~P1-10~~ | ~~**AUTH-DX-01**~~ ✅ PR#172：sso-bff walkthrough 修复（refreshToken curl drift、.timestamp audit 字段、随机 seed 密码）；config-core walkthrough + HMAC key 注释（S20/S21）；seed admin 改用 generateDevPassword() 消除明文日志 | — | — | PR#172 |
| ~~P1-12~~ | ~~**AUTH-SETUP-01 First-Run Setup 模式**~~ ✅ AUTH-SETUP-01 完成（含 P1-12a/b 完整改密硬强制 + admin-driven RequirePasswordReset 字段闭环）：`initialadmin` 工具包 + 24h TTL cleaner worker + Bootstrapper + domain.PasswordResetRequired + JWT claim + AuthMiddleware 拦截 + ChangePassword 自动脱困 + IssueOptions T2 重构 + sso-bff/cmd/core-bundle cutover；明文密码 slog PR#172 F1 彻底解决 | — | — | PR feat/159 |
| ~~P1-11~~ | ~~**PR-R-AUTH-AUD-VALIDATION**~~ ✅ PR#170 + PR#293: `WithExpectedAudiences` + `VerifyIntent` aud check (RFC 8725 §3.3) + `DefaultJWTAudience` 常量统一 + sessionlogin/sessionrefresh 引用；PR#293 round-2 追加：`TokenVerifier` 接口 + `Verify()` 方法删除（双 API 合并为单一 `VerifyIntent`）+ `ErrAuthVerifierConfig` 构造期 fail-fast + `msgInvalidServiceTokenFormat` 常量提取 | — | — | PR#166 R1-F2-5 |
| P1-13 | **SSO-BFF-WALKTHROUGH-JWT-FIX-01** (P0 DX, Cx1): README walkthrough 第 10/11 步（读配置/feature flags）未携带 Bearer JWT，但这两个端点非 public，按文档操作直接 401。同时 `walkthrough_test.go` 与运行时 `WithPublicEndpoints` 列表无绑定，文档/测试/白名单三处无单一真源。**修复**: README 补 `-H "Authorization: Bearer $ACCESS_TOKEN"`；walkthrough test 覆盖对应路径需鉴权的断言 | 1h | `examples/sso-bff/README.md` + `examples/sso-bff/walkthrough_test.go` | 2026-04-18 六席审查 |
| P1-14 | **ENVELOPE-FAILCLOSED-01** (P1, Cx2): relay 发布 `outboxMessage` envelope 后，eventbus consumer 遇 unknown action 直接 `continue`（静默 ACK），envelope 解析不完整时走 legacy fallback，两者组合 fail-open——业务事件看似投递成功实际未处理。**修复**: ① envelope 加版本位/magic field，"形似但不完整" → `DispositionReject`（DLX）而非 fallback；② consumer dispatch unknown action → `DispositionRequeue`（可恢复）而非静默跳过 | 2h | `runtime/outbox/envelope.go` + `runtime/eventbus/` consumer dispatch + 覆盖测试 | 2026-04-18 六席审查 |
| P1-15 | **RELAY-READINESS-BUDGET-01** (P1, Cx3): relay 轮询/发布/回写连续失败仅写日志并继续循环，进程 `/readyz` 仍健康——形成"活着但业务不可服务"假健康。**修复**: relay 引入连续失败计数器，超阈值（5 次 / 持续 30s）降级 readiness；`/readyz` 侧暴露 unhealthy；对标 K8s workqueue 失败预算 + go-micro critical probe | 3h | `runtime/outbox/relay.go` + `runtime/bootstrap/bootstrap.go` + health checker 接线 | 2026-04-18 六席审查 |

---

## P2 待办

### kernel / runtime

| # | 任务 | 工时 | 文件 | 来源 |
|---|------|------|------|------|
| K1 | **METADATA-PROJECTLOC-IFACE-01** (Cx3, 🟡 可延后): 提取 `ProjectLocator interface { Locate(file, path string) Position }` 隐藏 yaml.v3 AST；`ProjectMeta.FileNodes` 不再泄漏 | 3h | `kernel/metadata/` + `kernel/governance/` + `cmd/gocell/` | PR#152 seat-1 |
| K2 | **OBS-RELAY-REGISTER-ATOMIC-01** (Cx3, 🟡 可延后): `outbox.NewProviderRelayCollector` 5 个 metric 原子注册，需 `Provider.Unregister` 支持或文档化契约 | 2h | `kernel/outbox/` + `kernel/observability/metrics/` | PR#157 review S3-05 |
| R1 | **BOOTSTRAP-RUN-COGNIT-01** (Cx3, 🟡 可延后): `bootstrap.go::Run()` 认知复杂度 225（`//nolint:gocognit` 抑制），拆 `validateOptions()` / `buildRouter()` / `startServers()` 三段式 | 4h | `runtime/bootstrap/bootstrap.go` | PR#163 agent 报告 |
| R2 | **OBS-HTTP-COLLECTOR-AUTOWIRE-01** (Cx3, 🟡 可延后): `bootstrap.WithMetricsProvider` 自动为默认 HTTP 中间件构造 `NewProviderCollector`；设计 `WithHTTPCollectorCellID` option | 2h | `runtime/bootstrap/bootstrap.go` + `runtime/http/middleware/` | PR#157 review S4-01 |
| R3 | **OB-02** (🟡 可延后): safe_observe broken logger 注入测试 | 1h | `runtime/http/middleware/safe_observe_test.go` | 历史 backlog 0-J |
| R4 | **INTERNAL-LISTENER-01** (Cx4, 🟡 可延后): `/internal/v1/` 与公网共用 listener + Bearer JWT；独立 listener 或 service-token/mTLS 策略 | 4-8h | `runtime/bootstrap/bootstrap.go` + cell 路由注册拆分 | PR#143 review F1 |

### adapter

| # | 任务 | 工时 | 文件 | 来源 |
|---|------|------|------|------|
| A1 | **READYZ-BROKER-HEALTH-01** (Cx3): `Connection.Health() error` + bootstrap health checker 自动注册；`WithBrokerHealth(opts...)` 开关。对标 K8s readiness probe | 2h | `adapters/rabbitmq/connection.go` + `runtime/bootstrap/` + `runtime/http/health/` | 2026-04-18 外部审查 |
| A2 | **P4-TD-05** (🟡 可延后): outbox 全链路 3-container 集成测试（PG+RMQ+app） | 2h | `adapters/postgres/` + `adapters/rabbitmq/` | Phase 4 review |
| A7 | **POOLSTATS-IFACE-01** (🟡 可延后): 三个 adapter PoolStats 公共接口（OTel collector 消费） | 1h | `adapters/postgres/pool.go` + `redis/client.go` + `rabbitmq/connection.go` | PR#134 review |
| A8 | **CI-DIGEST-01** (🟡 可延后): testcontainers 镜像 tag+digest 双固定 | 1h | `adapters/*/integration_test.go` | PR#139 review |
| A9 | **CI-LINT-PIN-01** (🟡 可延后): golangci-lint patch 级固定 + dependabot | 1h | `.github/workflows/ci.yml` | PR#139 review |
| A10 | **OBS-LGTM-INTEGRATION-01** (Cx3, 🟡 可延后): `//go:build integration` 夜间 OTel collector 真实 OTLP 协议兼容性测试 | 2h | `adapters/otel/integration_test.go` | PR#157 review S6-04 |
| A13 | **BOOTSTRAP-WIRE-RMQ-BROKER-HEALTH-01** (🟠 条件延后): `cmd/core-bundle` 当前 publisher 是 in-memory eventbus（outbox relay 将 PG entries 转发至此）；`bootstrap.WithBrokerHealth` 未接线，/readyz 缺 RMQ 健康检查。触发条件：core-bundle 接入真实 RabbitMQ connection（替换 in-memory eventbus 为 rabbitmq.Publisher），此时同步通过 `bootstrap.WithBrokerHealth` 将 RMQ readiness 纳入 /readyz。当前 in-memory eventbus 无需 broker health probe。 | 2h | `cmd/core-bundle/main.go` + `runtime/bootstrap/` | PR#174 review F8 |
| A14 | **DISTLOCK-LOST-METRIC-01** (P3, Cx2, 🟡 可延后, 🟠 出现首个 distlock 消费方后触发): `Lock.Release` 返回 `ErrLockLost` 路径（past-expiry skip / Lua result==0 / double-Release）目前仅写 slog，无独立 metric label；对标 Redsync/Consul 惯例，应发射 `distlock_release_total{outcome="success|lost|error"}` 让 Grafana 能监控"锁失主"率。前置：runtime/observability 层需要先设计 label taxonomy（与 outbox_relayed_total 同族）。独立 PR 评审更聚焦。| 2h | `runtime/distlock/` + `adapters/redis/distlock.go` + collector wiring | PR#178 round-4 dev agent 自创 |
| A15 | **DISTLOCK-RENEW-JITTER-01** (P3, Cx2, 🟡 可延后): `adapters/redis/distlock.go::renewLoop` ticker 硬编码 `ttl/2`，多 holder 并发拿锁会同步续租 → Redis 侧 thundering-herd。对标 Redsync `driftFactor`，续租间隔应加 ±10-20% jitter。独立 PR 改 renewLoop + 加 miniredis 压测覆盖。触发条件：生产出现 Redis 并发 Eval 峰值告警。| 2h | `adapters/redis/distlock.go` + test harness | PR#178 round-4 dev agent 自创 |
| A16 | **DISTLOCK-RENEW-RATIO-CONFIGURABLE-01** (P3, Cx2, 🟡 可延后): renewLoop 续租比例写死 `ttl/2`，caller 无法按负载调成 `ttl/3`（更保守）或 `ttl*3/4`（低开销）。需要在 `DistLock` 构造器上增 `WithRenewRatio(float64)` option 并在 `Acquire` 传给 renewLoop。API surface 演进属于独立 PR 评审范畴。触发条件：首个 caller 提出续租开销或可靠性调优诉求。| 2h | `adapters/redis/distlock.go` 构造 + renewLoop + Acquire 参数 | PR#178 round-4 dev agent 自创 |

### slice / cell 收口

| # | 任务 | 工时 | 文件 | 来源 |
|---|------|------|------|------|
| S3 | **DTO-NIL-SEMANTIC-01** (Cx2): 12+ handler 写成功响应前校验领域对象非 nil，避免 converter nil guard 把上游不变量异常"平滑"为空 data 成功响应 | 3h | 12+ `cells/*/slices/*/handler.go` | PR#158 six-seat review |
| S4 | **EVENT-PAYLOAD-TYPED-01** (Cx2): sessionlogin/sessionlogout/configwrite/configpublish/auditappend/auditverify 事件 payload `map[string]any` → typed event struct | 3h | 6 个 `service.go` + event contract schemas | PR#133 re-review |
| S7 | **VALIDATE-EVIDENCE-CI-01** (Cx2, 根治声明-代码漂移): CI 新增独立 `metadata-check` job（`gocell validate` + `check contract-health`），失败阻断 PR | 1h | `.github/workflows/ci.yml` + PR template | PR#155 review F7 |
| S10 | **MODE-SEMANTIC-SPLIT-01** (Cx2, 🟡 可延后): 读路径 `query.RunMode`（cursor 容错）与写路径 `configpublish.WithRunMode`（publisher fail-open）当前共用同一枚举，为后续任一方向演进埋下耦合。保留"Init 单点翻译"前提，新增写路径独立类型（如 `configpublish.PublishFailureMode` with `FailClosed`/`FailOpen`），Cell Init 并行映射 `DurabilityMode → (RunMode, PublishFailureMode)` 后注入。触发条件：任一方向需要新增非二元模式值时。对标 Uber fx Provide/Decorate — 每个决策独立类型注入。 | 3h | `pkg/query/runmode.go` + `cells/config-core/slices/configpublish/service.go` + 4 处 `cell.go` Init | PR#167 round-2 review（finding 3 改进项，发现时建议暂缓） |
| S12 | **AUTH-GUARD-INLINE-UNIFY-01** ✅ PR#513: 已完成 — 21 处 handler guard boilerplate 替换为 `auth.Guard(w, r, auth.AnyRole(...))` / `auth.SelfOr(...)` Policy 抽象 | ✅ | `runtime/auth/guard.go` + 7 handler files | PR#168 review P2 |
| S2-follow | **CONTRACT-ERROR-SCHEMA-EXTEND-01** (P2, Cx1, 🟡 可延后): 其余 HTTP contract (config/write、config/get、auth/login 等) 补充 401/403 responses 声明，使错误响应 schema 覆盖全库所有受保护端点 | 2h | `contracts/http/**/{publish,get,write,flags}/v*/contract.yaml` | PR-CONFIG-POLISH 后续 |
| S13-follow | **4XX-LOG-SAMPLING-01** (P3, Cx1, 🟡 可延后): 高频端点（如 GET config）4xx 日志可加采样（1%）或在 rate-limiter 触发后限速，避免大量 429 日志淹没告警通道。注：PR#181 F3 已把 log4xx 字段最小化（移除 client message，仅保留 code/status/correlation IDs/internal），采样必要性已大幅下降；仅在真实运维告警通道过载时再做 | 1h | `pkg/httputil/response.go` | PR-CONFIG-POLISH 后续 |
| S14 | **CONFIG-VALUE-ENCRYPTION-01** (P1↑, Cx3): `config_entries.value` + `config_versions.value` 明文写库；sensitive=true 脱敏只在响应层（`config_entry.go` + `handler.go`），持久化边界无保护，DB 读权限即可拖取全量历史值。修法：加密放到 repo 写边界之前，同时覆盖 entries + versions；需 KMS 选型 + key rotation 独立 ADR，不混入普通 bugfix。优先级从 P2 升 P1。来源: PR#169 review F-S-2 + 2026-04-18 静态审查 | — | `cells/config-core/internal/adapters/postgres/config_repo.go` + `adapters/postgres/migrations/` | PR#169 + 2026-04-18 |
| S15 | **ERROR-CTX-CANCELLED-CLASSIFY** (P3, Cx2, 🟡 可延后): `ctx.Canceled` 在 config_repo 及其他 PG 路径未单独归类为 `ErrContextCanceled`，当前统一为 `ErrConfigRepoQuery`；reviewer 可接受当前状态，但长期会影响 caller 区分超时 vs 业务错误。来源: PR#169 review F-T-3 | 1h | `cells/config-core/internal/adapters/postgres/config_repo.go` | PR#169 review F-T-3 |
| S16 | **RUNTIME-TOPOLOGY-SINGLE-SOURCE-01** (P2, Cx3, 🟡 可延后): PR#169 只做了最小修复（validateModeCoupling fail-fast + adapterInfo 派生自 cellAdapterMode），彻底方案是把"已解析的实际运行拓扑"抽象为单一事实源 struct，同时驱动 repo wiring / adapterInfo / /readyz / /metrics / 生产门禁，避免未来新增 adapter 再一次出现 `GOCELL_ADAPTER_MODE` vs `GOCELL_CELL_ADAPTER_MODE` 分裂。对标 go-zero serviceconf / Kratos config。来源: PR#169 review F-NEW-2 彻底方案 | 6h | `cmd/core-bundle/main.go` + 新 runtime 抽象 | PR#169 review F-NEW-2（最小修复已合；彻底方案待） |
| S17 | **POOL-FRAMEWORK-LIFECYCLE-01** (P2, Cx3, 🟡 可延后): PR#169 在 cmd/core-bundle 层手工接 `defer pgPool.Close()` + `bootstrap.WithHealthChecker("postgres", pool.Health)`；彻底方案是把 `*adapterpg.Pool`（以及 Redis / RMQ 等外部资源）提升为 bootstrap/assembly 层面的托管资源，统一 shutdown LIFO + 自动 health checker 注册，对标 Uber fx OnStart/OnStop + K8s readyz storage_readiness_hook。来源: PR#169 review F-NEW-3 彻底方案 | 4h | `runtime/bootstrap/` + `kernel/assembly/` | PR#169 review F-NEW-3（最小修复已合；彻底方案待） |
| S18 | **JWT-AUDIENCE-ENV-VAR-01** (P1, Cx2, 🟠 条件延后，多环境部署前触发): `jwtAudience` 为编译期常量，多环境（staging/prod）无法通过 env var 区分（如 `gocell-staging` / `gocell-prod`）；同时 sessionlogin/sessionrefresh 内 audience 需同步注入，否则 issuer 与 verifier drift 风险仍在。**修法**：新增 `GOCELL_JWT_AUDIENCE` env var；`adapterMode=real` 时强制要求；access-core cell 新增 `WithTokenAudience(string)` option 让 Init 注入而非硬编码。**前置**：与 F-O-1/F-A-1 配套，需独立 ADR。来源: PR#170 review F-O-1 + F-A-1 | 3h | `cmd/core-bundle/main.go` + `cells/access-core/cell.go` + `cells/access-core/slices/sessionlogin/service.go` + `sessionrefresh/service.go` | PR#170 review |
| S22 | **REFRESH-AUD-REAL-ROUTE-TEST-01** (P2, Cx2, 🟡 可延后): `runtime/auth/middleware_aud_test.go` 的 refresh-path audience 回归测试手造 fake handler 直接调 `VerifyIntent`，不经过真实 route registration / body binding / `Service.Refresh`，无法检测 public-route bypass 或 body decode 回归。**修法**：在 `cells/access-core/auth_integration_test.go` 补一条真实 HTTP 测试：POST `{"refreshToken": wrong-aud-token}` 到 `/api/v1/access/sessions/refresh`，断言返回 401；再补 missing-aud token 场景。**前置**：可独立于 S18 实施。来源: PR#171 外部审查 F2 | 2h | `cells/access-core/auth_integration_test.go` + `runtime/auth/middleware_aud_test.go` | PR#171 外部审查 |
| S19 | **JWT-AUDIENCE-DRIFT-INTEG-TEST-01** (P2, Cx1, 🟡 可延后): `cmd/core-bundle/jwt_aud_integration_test.go` 直接调 `deps.issuer.Issue(…, []string{jwtAudience}, …)` 而非真实 sessionlogin 路径，无法检测 sessionlogin/sessionrefresh 硬编码与 `DefaultJWTAudience` 的 drift。**修法**：集成测试调用 sessionlogin.Service.Login → 解析响应 token → VerifyIntent，使 drift 编译失败而非运行时静默放行。**前置**：S18（access-core WithTokenAudience 注入）落地后更易实现。来源: PR#170 review F-T-3 | 2h | `cmd/core-bundle/` + `cells/access-core/slices/sessionlogin/` | PR#170 review F-T-3 |
| S20 | **JWT-AUDIENCE-STARTUP-LOG-01** (P3, Cx1, 🟡 可延后): `buildJWTDeps` 构建时未打印 effective audience 值；ops 无法通过启动日志或 `/readyz?verbose` 确认运行时 audience 配置。**修法**：`slog.Info("JWT audience enforcement enabled", slog.String("audience", jwtAudience))` 紧接 verifier 构造后添加。来源: PR#170 review F-S-5 | 0.5h | `cmd/core-bundle/main.go` | PR#170 review F-S-5 |
| S21 | **JWT-AUD-TEST-TABLE-DRIVEN-01** (P3, Cx1, 🟡 可延后): `runtime/auth/jwt_aud_test.go` 9 个场景为独立函数，违反 CLAUDE.md table-driven 规范。**修法**：重构为 `struct { name; expectedAuds []string; tokenAud []string; wantErr bool; errContains string }` 结构。来源: PR#170 review F-T-5 | 1h | `runtime/auth/jwt_aud_test.go` | PR#170 review F-T-5 |
| S23 | **AUTH-WALKTHROUGH-COMPOSE-01** (P2, Cx3, 🟡 可延后): `examples/sso-bff/walkthrough_test.go` 手装精简 server（无 bootstrap lifecycle），与 `main.go` 的真实组装路径、README 步骤三份语义各自独立，可掩盖 public-endpoint wiring / config-core 接线 / audit event delivery 的回归。**修法**：提取 `NewSSOBFFApp(opts...)` 组装函数被 `main.go` 和 walkthrough test 共用；test server 走同一 bootstrap + Start/Stop 路径。来源: PR#172 review F3（OUT_OF_SCOPE） | 4h | `examples/sso-bff/bootstrap.go`（新）+ `main.go` + `walkthrough_test.go` | PR#172 review F3 |
| S22 | **AUTH-MIDDLEWARE-AUD-REFRESH-E2E-01** (P3, Cx2, 🟡 可延后): `TestAuthMiddleware_WrongAudience_RefreshPath_Returns401` 直接调 `verifier.VerifyIntent()`，未经过 `AuthMiddleware` 真实中间件链（parse req → call verifier → write error）。本质是在测 verifier 行为而非 middleware 集成。生产 refresh 路径确实也是 `VerifyIntent` 直调，无实际安全风险；但若未来 middleware 有 early-return/short-circuit 改动，该测试无法检测 regression。**修法**：用 `httptest.NewServer` + 真实 `AuthMiddleware` + `makeTokenWithAud` 覆盖 refresh-path wrong-audience 场景，使测试打到完整链路。来源: PR#293 round-2 /fix 分析 | 1h | `runtime/auth/middleware_aud_test.go` | PR#293 round-2 |
| S29 | **CORE-BUNDLE-APP-BUILDER-01** (P2, Cx3, 🟡 可延后): `cmd/core-bundle/main.go` 的装配逻辑与 integration tests 里的装配是两条代码路径；`buildConfigCoreOpts` 是共享点，但 bootstrap options 列表（publisher/subscriber/workers/adapterInfo/public endpoints/durability mode）目前只在 main.go 维护，测试靠手工复制。抽出 `cmd/core-bundle.BuildApp(opts...) (*bootstrap.Bootstrap, func(), error)` 被 main 和 integration test 共用，避免拓扑事实源再次分裂。对标 Uber fx `App.New` + Kratos `App.Run` 统一入口模式 | 6-8h | `cmd/core-bundle/app.go`（新）+ `main.go` + `outbox_e2e_integration_test.go` | PR#174 review F1 跟进 |
| ~~S30~~ | ~~**OUTBOX-STORE-ABSTRACTION-01**~~ ✅ PR#511: `runtime/outbox.Store` 7 方法 interface，`adapters/postgres.PGOutboxStore` 实现；relay 编排搬到 `runtime/outbox/relay.go`；`outboxtest.FakeStore` public；同 PR 搭车 S28 + X7。净减 ~2.8kloc | — | — | PR#511 |
| S31 | **BOOTSTRAP-INRUN-COMPENSATION-01** (P3, Cx2, 🟠 条件延后，PG 多副本 + ops 反馈触发): 采纳方向 B（orphan-user 重启自愈），**不做** in-run 补偿。触发条件：X1 PG-DOMAIN-REPO 上线后，若运维反馈中间态造成真实可见性问题（诊断日志混乱、监控误报），再评估 in-run 回滚 | 2h | `cells/access-core/internal/initialadmin/bootstrap.go` | PR feat/159 round 5 review |
| S32 | **JWT-ISSUER-STRICT-01** (P2, Cx2, 🟡 可延后): `VerifyIntent` 缺 issuer 强约束；跨环境密钥复用时 staging token 可通过 prod 验证。`NewJWTVerifier` 增 `WithExpectedIssuer(string)` option；`real` 模式下 issuer 非空强制；补负向测试。建议搭车 S18 | 1h | `runtime/auth/jwt.go` + `cmd/core-bundle/main.go` | 2026-04-18 六席审查 |
| S33 | **CONTROLPLANE-TOKEN-PROD-GATE-01** (P2, Cx2, 🟠 条件延后，real 模式部署前触发): bootstrap 启动时 `real` 模式断言 service-token/mTLS 至少一项；补 CI real-mode smoke | 1h | `cmd/core-bundle/main.go` + `runtime/bootstrap/bootstrap.go` | 2026-04-18 六席审查 |
| S34 | **WALKTHROUGH-CI-SMOKE-01** (P2, Cx1, 🟡 可延后): `examples/sso-bff/walkthrough_test.go` 带 `//go:build integration` 未进 CI 主门禁；增 `walkthrough-smoke` job 或拆 unit subtests | 1h | `.github/workflows/ci.yml` + `examples/sso-bff/walkthrough_test.go` | 2026-04-18 六席审查 |
| S35 | **AUTH-ADMIN-FORCE-RESET-01** (P2, Cx2, 🟡 可延后): admin-only 强制密码重置端点 `POST /api/v1/access/users/{id}/password/reset`；生成临时密码 + `PasswordResetRequired=true` | 4h | `cells/access-core/slices/identitymanage/` + `contracts/http/auth/user/reset/v1/`（新）| PR#183 reviewer round 2 |
| S36 | **AUTH-MIDDLEWARE-EXEMPT-INJECTION-01** ✅ 发现已完成（PR#513 核查）: `runtime/auth/middleware.go:189` 完全由 `cfg.passwordResetExempt` matcher 注入；composition roots 通过 `bootstrap.WithPasswordResetExemptEndpoints` 接线 | ✅ | `runtime/auth/middleware.go` | PR#183 reviewer round 2 |
| S37 | **WORKER-LAZY-HOIST-01** (P3, Cx2, 🟡 可延后): `lazyBootstrapWorker` / `ssoBFFLazyWorker` / `e2eLazyWorker` 三处完全相同；提到 `runtime/worker.Lazy(get func() Worker) Worker` 框架级抽象 | 2h | `runtime/worker/lazy.go`（新）+ `cmd/core-bundle/main.go` + `examples/sso-bff/main.go` | PR#183 reviewer round 2 |
| S38 | **ROUTER-USE-AUTH-01** (P3, Cx3, 🟡 可延后, 中期): `kcell.RouteMux` 暴露 `UseAuth(p auth.Policy)` 路由级绑定 (对标 chi r.Use / Kratos selector MatchFunc)；RegisterRoutes 一行声明替代 handler 内 guard inline。触发：同一 slice > 3 处重复 Policy 或新增组合场景 (AnyOf/AllOf) | 4-6h | `kernel/cell/` + runtime/http/router + 7 cells handler | PR#513 follow-up |
| S39 | **ROUTER-METADATA-POLICY-01** (P3, Cx3, 🟡 可延后, 远期): Policy 声明移到 `contract.yaml` / `slice.yaml` `security` 字段；`gocell validate` 静态校验形态 + `gocell generate` 从元数据生成路由绑定；对标 K8s RBAC manifest + OpenAPI security schema。触发：Policy 种类 > 5 种或合规审计需统一视图。前置：ROUTER-USE-AUTH-01 稳定 | 10-16h | `kernel/metadata/` + `kernel/governance/` + `cmd/gocell/` + `contracts/**/contract.yaml` | PR#513 follow-up (远期) |

### 发布 + 文档

| # | 任务 | 工时 | 文件 | 来源 |
|---|------|------|------|------|
| F2 | **SYSTEM-TOPOLOGY-API** (🟡 可延后): `GET /internal/v1/system/topology` 返回 cell/slice/contract 拓扑 JSON；基于 `kernel/registry` | 4h | 新 slice 或 `runtime/bootstrap/` | 历史 Batch 8 |
| F3 | **P2-T-02 audit e2e 测试**: Journey 级验收 | 2h | `journeys/` + integration test | 历史 Batch 8 |
| F4 | Review cells/ | 4h | — | Wave 4 |
| F5 | Review examples/ | 2h | — | Wave 4 |
| F6 | Review 报告汇总 | 2h | — | Wave 4 |
| F7 | 发布文档 | 4h | — | Wave 4 |
| F8 | 性能基准 | 4h | — | Wave 4 |
| F9 | **v1.0 tag** | — | — | Wave 4 |
| F10 | **TEST-JOURNEY-ASSEMBLY-HARNESS-01** (Cx3, 🟡 可延后): `tests/integration/journey_test.go` 全部 28 条均 `t.Skip("stub: requires full assembly")`；需要建 full-assembly harness 后统一恢复（J-session-refresh / J-session-logout / J-account-lockout / J-audit-login-trail / J-config-* / J-sso-login / J-user-onboarding），不只 refresh 一条 | 8h | `tests/integration/` + assembly fixture | PR#166 R2-P2（范围扩大） |
| F11 | **README-CURL-EXTRACT-01** (P2, Cx2, 🟡 可延后): `examples/sso-bff/README.md` 的 bash 代码块与 `walkthrough_test.go` 是两条独立事实源——README 烂了测试仍然过（PR feat/159 round 5 P2-1/2 就是这类漂移）。实现 bash 代码块解析器（或改用 Go testable example + 外部 sh-embed），让 CI 把 README 里的 curl 真的跑一遍。**工程量**：bash 变量替换 + jq 解析 + HTTP 拨测，~半天。触发条件：README 第二次出现鉴权/命令漂移；或 v1.0 发布前把示例文档纳入 smoke gate | 4-6h | `examples/sso-bff/` 新 `readme_smoke_test.go` + bash parser helper | PR feat/159 round 5 review P2-1/2 |
| F12 | **RUNTIME-AUTH-GODOC-LITERAL-01** (P3, Cx1, 🟡 可延后): `runtime/auth/middleware.go:202-204` + `runtime/auth/exempt.go:76` 的 godoc 注释里仍用 `/api/v1/access/users/{id}/password` 作为 `matchPathTemplate` 行为说明的例子。非功能代码，不影响行为，reviewer P2-4 关切已通过 `WithPasswordResetChangeEndpointHint` 解决；仅在顺手打磨 runtime/auth 文档时泛化为 `/path/{id}/action` 之类中性例子 | 0.5h | `runtime/auth/middleware.go` + `runtime/auth/exempt.go` | PR feat/159 round 5 review P2-4 后续 |

---

## P3 长期 / 大型独立

| # | 任务 | 工时 | 前置 | 来源 |
|---|------|------|------|------|
| X1 | **PG-DOMAIN-REPO**: 5 个域 Repository PostgreSQL 实现（User/Session/Role/Device/Command）。前置: ① 4 migration DDL + **CONFIG-VERSIONS-MIGRATION-01** (`004_create_config_entries_and_versions.sql`)；② `ports.RoleRepository.CreateRole`。**落地联动**（同 PR 或紧邻 PR）: **RBAC-ASSIGN-LEVEL-UPGRADE-01** L0→L1；**SEED-ROLE-IFACE-01** 去 type assertion；**ACCESS-LEVEL-AUDIT-01** slice.yaml 校正；**AUTH-CACHE-01 激活** Redis session cache + 撤销失效路径 | 3-5d | — | PR#155 review F4 + 多 PR 汇总 |
| X2 | **WM-35 BFF handler 接入 cookie session** ★ | 2d | WM-2-F1 ✅ | 长期 roadmap |
| X3 | **WM-36 SecureCookie key rotation** ★ | 1.5d | WM-35 | 长期 roadmap |
| X4 | **WM-7 泛型 BulkResult** | 1d | — | 历史 Batch 8 |
| X5 | **P3-TD-11 access-core domain 拆分** User/Session/Role | 4h | X1 | 历史 Batch 8 |
| X9 | **LINT-MODERN-01** (Cx2, P3): 全仓库 modernization baseline 清理（rangeint / stringsseq / forvar / inline / testingcontext / any / nhooyr.io→coder/websocket）。独立 PR，不混入功能 | 6h | — | PR#163 post-review |
| X10 | **AUTH-REFRESH-OPAQUE-01** (Cx3, 🟠 条件延后，X1 PG-DOMAIN-REPO 上线后触发): refresh token 由 JWT 改为 opaque string + server-side rotation store（RFC 6819 §5.2.2.2）；减小 JWT 承载、允许即时撤销 | 1-2d | `runtime/auth/` + `adapters/postgres/` 新 refresh_token_store | PR#166 R1-F2-7 |

---

## 设计决策记录（历史 — 不修，避免重复审查）

### PR#137 review

| # | Finding | 结论 | 理由 |
|---|---------|------|------|
| F1-2 | assembly 层不校验零值 + 非法枚举旁路 | ✅ PR#137 | assembly.startInternal 加 ValidateMode 入口闸门 + CheckNotNoop 改为 allowlist |
| F1-3 | core-bundle DurabilityDurable + in-memory | 不修 | 语义正确：Durable 拒绝 Nooper 标记类型，nil 和 `eventbus.New()` 合法通过；effectiveMode + adapterInfo + slog 日志已透明标注；PG-DOMAIN-REPO 排队中为真修路径；2026-04-18 外部审查复核维持 |
| F3-1 | durability_test 非 table-driven | 不修 | 8 个测试断言模式差异大，table-driven 需参数化断言增加复杂度不增加覆盖率 |
| CI-DUP | SonarCloud 5.6% duplication | 不修 | cell-per-package 固有结构相似，5 cell 的 CheckNotNoop 参数列表各不相同，不可提取 |

### PR#140 对标确认

| # | 主题 | 结论 | 理由 |
|---|------|------|------|
| A | Request/Trace ID 生成不做 `rand.Read` 错误分支 | ✅ PR#140 | chi / Kratos / OTel / Go 1.24+ `crypto/rand.Read` always-succeed-or-fatal，`_, _ =` 是死代码 |
| B | JSON unknown field 用字符串匹配 + guard test | ✅ PR#140 | Gin / Echo / go-zero / Go 标准库 `encoding/json` 至 1.25 仍用文本错误，无 typed error |

### PR#143 review

| # | Finding | 结论 | 理由 |
|---|---------|------|------|
| 4.1 | Seed admin password 通过环境变量传入 | 不修 | dev 模式标准做法（Casdoor/Zitadel 同模式）；生产改用 secrets manager 在 PG-DOMAIN-REPO 时处理 |
| 1.2 | Slice 目录命名 kebab vs no-dash | 不修 | CLAUDE.md "Cell 开发规则"约定 |
| 5.3 | `TestContext` 从非 `_test.go` 文件导出 | 不修 | 跨包测试需要，`_test.go` 函数 package-scoped 无法跨包调用 |
| 6.1 | POST /assign 返回 200 而非 201 | 不修 | 幂等操作返回 200（Casbin 模式），201 暗示每次创建新资源 |

### PR#146 review

| # | Finding | 结论 | 理由 |
|---|---------|------|------|
| I2 | computeBoundaryContracts 遍历全量 contracts | 不修 | generator 必须遍历全量才能发现 imported contracts；FMT-09 validate 阶段已保证合法，generator fail-fast 是正确的 defense-in-depth |

### PR#155 review

| # | Finding | 结论 | 理由 |
|---|---------|------|------|
| F6 | `slices/*/slice.yaml` allowedFiles 双路径（kebab + no-dash）| 不修 | 全项目统一惯例，FMT-14 治理规则守护，`gocell scaffold slice` 模板默认产出双路径 |

### PR#159 review

| # | Finding | 结论 | 理由 |
|---|---------|------|------|
| F1.2 | 7 个 Option type 分散 | 不修 | 每个 Option 针对不同构造函数，合并会丧失类型安全性；对标 Kratos jwt.Option 每个中间件独立 |
| F2.5 | GenerateServiceToken `crypto/rand` 失败静默返回 "" | 不修 | Go 1.24+ `crypto/rand.Read` always-succeed-or-fatal，空串触发下游 401 是 fail-closed |
| F4.2 | ServiceTokenMiddleware nil ring 每请求 500 | 不修 | 返回 error 违背 Go 标准 middleware 签名惯例（net/http / go-chi / Kratos 均不返回 error）；构造时 panic 不符合编码规范 |
| F6.2 | 缺 service token duration metric | 不修 | HMAC-SHA256 <0.1ms，histogram 分桶无意义信息；等 Redis nonce store 引入网络 I/O 后再加 |

---

## 触发条件项（仅在条件满足时做）

| # | 任务 | 工时 | 触发条件 |
|---|------|------|----------|
| T1 | **AUTH-PROVIDER-EXPORT-01** `authProvider` 接口 unexported，需移动出 `runtime/bootstrap` | 1h | 第二个 auth provider cell |
| ~~T2~~ | ~~**AUTH-ISSUE-OPTIONS-01**~~ ✅ JWTIssuer.Issue → IssueOptions struct 重构落地（PR feat/159 一并完成）：`Issue(intent, subject, IssueOptions)` + `IssueOptions{Roles, Audience, SessionID, PasswordResetRequired}`；消除参数列表膨胀 tech debt | — | — | PR feat/159 |
| T3 | **DEVICE-ENQUEUE-RBAC** HandleEnqueue 无设备维度鉴权 | 2h | 多租户 operator |
| T4 | **CB-RESILIENCE-PACKAGE-01** 把 `Allower` / `CircuitBreakerRetryAfter` 从 `runtime/http/middleware` 迁移到 `runtime/resilience/circuitbreaker/` 独立包 | 4h | 出现第二个非 HTTP 的 CB 消费方 |
| T5 | **AUTH-SIGNER-01** `SigningKeyProvider` 返回 `crypto.Signer` 替代 `*rsa.PrivateKey` | 2h | golang-jwt v6 发布 |
| T8 | **PUBLIC-ENDPOINT-STRUCT-MIGRATE-01** `WithPublicEndpoints([]string)` 迁移为 `[]PublicEndpoint{{Method,Path, ...}}` 结构体（go-zero 风格）；当前字符串方案对齐 Go 1.22 stdlib ServeMux + otelhttp 预测函数，启动期 `CompilePublicEndpoints` fail-fast 已覆盖手误；保留结构体方案作为触发项以便扩展元数据 | 3h | 满足任一：(1) 公共端点数量 > 20（当前 2）(2) 需要 per-endpoint 元数据（rate-limit 豁免 / audit skip flag 等）(3) 线上复盘出现 method 字符串手误绕过 fail-fast (4) tracing/auth bypass 语义需独立配置时（review I-10 结论：三边界共用 predicate 的解耦触发） |
| T9 | **AUTH-BYPASS-METRICS-01** public endpoint 命中率指标 `auth_bypass_total{method,path}`；collector 层暴露信任边界偏离信号 | 2h | 触发条件：observability 专项落地（配合 R2 OBS-HTTP-COLLECTOR-AUTOWIRE-01），或 401 baseline 需要 method 维度审计时 |
| T10 | **RMQ-STATTER-RENAME-01** 删除 `(*rabbitmq.Connection).Statter` 旧方法（旧名暗示 DB pool 语义错误，`ChannelStatter` 清楚表达 AMQP channel pool）。迁移 `adapters/rabbitmq/connection_statter_test.go:37` 的唯一调用点到 `ChannelStatter`。宪法不考虑向后兼容——直接删，不标 Deprecated | 15min | 独立小 PR，可随时做（无阻塞） |
| T11 | **AUTH-LOADKEYSFROMENV-UNEXPORT-01** `runtime/auth.LoadKeysFromEnv` 实为内部基础构件（仅被同包 `LoadKeySetFromEnv` 和 4 个测试引用，无外部生产调用）。unexport 为 `loadKeysFromEnv`，移除 `// Deprecated:` 注释（宪法不留兼容期）。改 `LoadKeySetFromEnv` 内部调用 + 4 个测试（改走 `LoadKeySetFromEnv` 或直接测 unexported） | 30min | 独立小 PR，可随时做（无阻塞） |
| ~~T6~~ | ~~**GOCELL-PER-CELL-ADAPTER-01**~~ **不做**：决策全量 PG 接入，per-cell 覆盖仅过渡期有价值，全量接完后变死代码 | — | — | 2026-04-18 设计裁决 |
| ~~T7~~ | ~~**CONFIG-VERSIONS-CONFIG-ID-INDEX**~~ ✅ PR#173：`006_add_config_versions_config_id_index.sql` + TestMigration006 | — | — | PR#173 |
| T-PG-ADVISORY-LOCK-01 | **多副本 PG bootstrap 竞态防护**：`pg_try_advisory_lock(hashtext('gocell_initial_admin'))` 确保多 pod 启动时只有一个进程进入 bootstrap 临界段 | 2h | `adapters/postgres/user_repo.go`（X1 落地时）| X1 PG-DOMAIN-REPO 上线 |

---

## v1.1+ 长期规划

> **详细内容请阅读: [backlog_later_detail.md](./backlog_later_detail.md)**
>
> metadata 校验规则 (G-1~G-6) / Kernel 子模块 (wrapper/command/webhook/reconcile/scheduler/replay/rollback)
> adapters 分层重整 (AL-03~AL-04, RMQ-STATUS-01) / 架构风险 (Cell 接口拆分, adapter t.Skip, ER-ARCH-01)
> 契约增强: CONTRACT-BREAKING-01 / CONTRACT-CODEGEN-01 / CONTRACT-STUB-01
> spec tech-debt (C-AC7 jti / C-L6 contract ID / C-DC9 auditarchive stub / DURABLE-TYPE-01 / CONTRACT-META-01)
> winmdm defer (WM-18/32/4/5/22/23/16) / winmdm reject (WM-3/14/21/24/25/26/30/31/34b)
> v2+ (WM-28/29, GAP-1/2/5/6/8/11/12/13/14)

---

## 工时汇总

| 分类 | 工时 |
|------|------|
| P0 阻塞 | ~6.5h |
| P1 | ~38.5h（+6h: P1-13/14/15） |
| P2 kernel/runtime | ~12-16h |
| P2 adapter | ~16h |
| P2 slice/cell | ~21h（+3h: S31/S32/S33） |
| P2 发布 + 文档 | ~23h + v1.0 tag |
| **核心路径合计（不含 P3）** | **~117-121h（约 14-15 工作日）** |
| P3 长期 | 3-5d + 4.5d + 若干独立项 |

---

## 实施计划

按层执行顺序见: [docs/plans/20260418-bottom-up-implementation-plan.md](plans/20260418-bottom-up-implementation-plan.md)

关键路径：P0 阻塞 → kernel/runtime 偿债 → pkg/adapter 偿债 → slice/cell 收口 → 功能 + 发布 → v1.0 tag
