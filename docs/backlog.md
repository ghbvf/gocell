# GoCell Backlog

> 只含待办事项。已完成项归档至 `docs/reviews/archive/202604180035-backlog-pre-cleanup.md`。
> 更新日期: 2026-04-18（PR#511 S30 落地同步）
> 基线: develop@fdb6561（PR#174 合并 + backlog sync 后）
> 最近合入概览: Wave 1 ✅ 全部完成 / Post-Wave 1 PR#141-165 按层偿债 + 外部审查回灌 / PR#170+171 auth aud 强验证 + S9 可观测 / PR#172 SSO-BFF config walkthrough + HMAC doc / PR#174 outbox broker health nil fail-fast + e2e wire guard / PR#511 S30+S28+X7 outbox store abstraction (runtime/outbox.Store interface + envelope 共享 + 删除 ~2.8kloc adapter relay)
> 未合入外部 PR: PR#129 Sentinel DSN redaction / PR#130 Bolt journey catalog
> 标记说明:
> 🟡 可延后 = 不卡正确性或安全；latent risk / DX / 测试覆盖 / 纯 tech debt / 供应链加固 / 架构打磨 — 可机会性纳入或 v1.0 后做
> 🟠 条件延后 = 有明确触发条件（如首次 prod migration / PG 接线），触发前可延

---

## P0 阻塞项（2026-04-18 外部审查 + PR#157 post-merge，~11h）

| # | 任务 | 工时 | 文件 | 来源 |
|---|------|------|------|------|
| P0-1 | ✅ **AUTH-TOKEN-INTENT-01** (Cx3): login/refresh 共用 `JWTIssuer.Issue`，token 无 audience/purpose claim；全局 Bearer 中间件只做 `verifier.Verify`，session-validate 只看 `sid`，不区分用途 → **refresh token 可直接访问业务接口**。**修复**: Issue() 增 `TokenIntent`（"access"/"refresh"）→ `token_use` claim + JOSE `typ` header；新增 `IntentTokenVerifier.VerifyIntent`；`AuthMiddleware` 签名收紧为 `IntentTokenVerifier`（编译期强制）；`/auth/refresh` 只接 refresh、其它只接 access。对标 RFC 9068 / AWS Cognito token_use / Keycloak TokenUtil. | 5h | `runtime/auth/jwt.go` + `runtime/auth/middleware.go` + `cells/access-core/slices/{sessionlogin,sessionrefresh,sessionvalidate}/service.go` | PR#166 ✅ |
| P0-2 | ✅ **AUTHZ-WRITE-CONFIG-WRITE-01** (Cx2, 阻塞): configwrite create/update/delete 三端点无 `auth.RequireAnyRole(ctx, "admin")`，与 configpublish publish/rollback admin gate 不一致（同一资源域授权策略漂移）。**修复**: 复用 `roleAdmin` const（提取到 `cells/config-core/internal/dto/authz.go` 共享），三端点入口加 gate + 401/403/200 测试 | 1.5h | `cells/config-core/slices/configwrite/handler.go` + `cells/config-core/internal/dto/authz.go`（新建）| PR#168 ✅ |

---

## P1 待办

| # | 任务 | 工时 | 文件 | 来源 |
|---|------|------|------|------|
| ~~P1-1~~ | ~~**AUTH-INT-REACHABILITY-01**~~ ✅ `cells/access-core/auth_integration_test.go`：4 测试覆盖合法 access 到达、refresh 被拦、access/refresh 路径互拦、refresh token 轮转；`loginAndGetPair` 精确断言 201 + accessToken/refreshToken/expiresAt 三字段 | — | — | 2026-04-18 外部审查 |
| P1-2 | **CONFIG-DEMO-FAILOPEN-01** (Cx2, PR-X1 进行中): `configpublish/service.go:188-194` demo 模式 publisher 发布失败仅 `logger.Warn` 后 `return nil`，与 cell.yaml L2 声明不符。**修复**: durable 模式移除 fail-open；demo fail-open 仅保留在显式 `DiscardPublisher{}` 或 `Assembly.Mode == Demo` | 2h | `cells/config-core/slices/configpublish/service.go` | PR#157 post-merge |
| P1-3 | **PUBLIC-ENDPOINT-METHOD-MATCH-01** (Cx3, 🟡 可延后): 公共端点匹配为 path-only，不含 method 维度（latent risk）。升级为 `"METHOD /path"` 格式（向后兼容：无 method 前缀匹配所有 method）| 4h | `runtime/http/router/router.go` + `runtime/auth/middleware.go` + `runtime/bootstrap/bootstrap.go` + 调用方 | PR#158 six-seat review |
| P1-4 | **OUTPUT-JSON-SARIF-01** (Cx3, 🟡 可延后): `gocell validate` 缺机器可读输出通道（JSON/SARIF）。统一诊断模型（单一 `Issue` struct → 多 printer 映射）。对标 golangci-lint / staticcheck / ESLint / kubectl print flags | 6h | `cmd/gocell/` + `kernel/governance/` 序列化 | PR#152 round-2 review |
| P1-5 | **METADATA-PERF-BENCH-01** (Cx3, 🟡 可延后): `BenchmarkParseFS_500Files` 性能基准 + goccy/go-yaml 单次解码迁移成本评估 | 4h | `kernel/metadata/parser_test.go` | PR#152 seat-4 |
| ~~P1-6~~ | ~~**PR#160 PR-X2 pkg/query 稳定性**~~ ✅ PR-P-QUERY: codec nil Service 层 fail-fast（5 slice 各自 panic）/ `ParsePageRequest` cursor 长度上限 / `WithDemoFailOpen`→`WithRunMode` 整合 / `loadCursorCodec` wrap 链单测 / `RunModeForDemo` godoc "Do not extend" 警告 | — | — | PR-P-QUERY 合入 |
| ~~P1-7~~ | ~~**PR#160 PR-X3 cursor key rotation 接线**~~ ✅ PR-P-QUERY: `NewCursorCodec(current, previous)` 接线 + `GOCELL_{AUDIT,CONFIG}_CURSOR_PREVIOUS_KEY` 双 env + 轮换生效 slog.Info + 7 条 loadCursorCodec 测试覆盖 3 步 rotation lifecycle | — | — | PR-P-QUERY 合入 |
| P1-8 | **FEAT-1 DEVICE-LIST-API**: 新建 `device-list` slice + `GET /api/v1/devices` 分页 + contract + contract_test；同步触发 CONTRACT-LIST-LINT-01 规则 | 3h | `cells/device-cell/slices/device-list/` + `contracts/http/device/list/v1/` | backend_issues.md #1 |
| P1-9 | **FEAT-2 FLAG-WRITE-API**: `PUT /api/v1/config/flags/{key}` 写入端点 + contract + contract_test | 3h | `cells/config-core/slices/configwrite/` + `contracts/http/config/flags/write/v1/` | backend_issues.md #2 |
| ~~P1-10~~ | ~~**AUTH-DX-01**~~ ✅ PR#172：sso-bff walkthrough 修复（refreshToken curl drift、.timestamp audit 字段、随机 seed 密码）；config-core walkthrough + HMAC key 注释（S20/S21）；seed admin 改用 generateDevPassword() 消除明文日志 | — | — | PR#172 |
| P1-12 | **AUTH-SETUP-01 First-Run Setup 模式**：`GET /api/v1/setup/status`（返回 `{"data":{"setupRequired":bool}}`）+ `POST /api/v1/setup/admin`（仅在无 admin 时有效，之后返回 409）；两个端点加入 `WithPublicEndpoints`；新 `setup` slice + contract；前端后续可检测 `setupRequired=true` 自动跳转建账页面。**去掉 sso-bff seed 用户**（含 `slog.Info` 明文密码日志 — PR#172 review F1 deferred here）改由 setup 流程创建 | 6h | `cells/access-core/slices/setup/` + `contracts/http/auth/setup/` + `examples/sso-bff/` | AUTH-DX-01 讨论 + PR#172 F1 |
| ~~P1-11~~ | ~~**PR-R-AUTH-AUD-VALIDATION**~~ ✅ PR#170 + PR#293: `WithExpectedAudiences` + `VerifyIntent` aud check (RFC 8725 §3.3) + `DefaultJWTAudience` 常量统一 + sessionlogin/sessionrefresh 引用；PR#293 round-2 追加：`TokenVerifier` 接口 + `Verify()` 方法删除（双 API 合并为单一 `VerifyIntent`）+ `ErrAuthVerifierConfig` 构造期 fail-fast + `msgInvalidServiceTokenFormat` 常量提取 | — | — | PR#166 R1-F2-5 |

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
| ~~A3~~ | ~~**RL-INT-01**~~ ✅ PR#173：5 个 testcontainers PG+RMQ 测试 + 真实 TCP 断连/恢复测试移至 rabbitmq integration test | — | — | PR#173 |
| ~~A4~~ | ~~**RL-MIG-01**~~ ✅ PR#173：`CREATE INDEX CONCURRENTLY` migration 006 + Up() 边界 INVALID index pre-check | — | — | PR#173 |
| A5 | **RL-SUB-01** (🟡 可延后): 入站 ID 校验（空/过长 message ID） | 1h | `adapters/rabbitmq/subscriber.go` | PR#46 review |
| A6 | **#31 RabbitMQ backoff + FailOpen enum 清理** (🟡 可延后) | 2h | `adapters/rabbitmq/` | Wave 2 残留 |
| A7 | **POOLSTATS-IFACE-01** (🟡 可延后): 三个 adapter PoolStats 公共接口（OTel collector 消费） | 1h | `adapters/postgres/pool.go` + `redis/client.go` + `rabbitmq/connection.go` | PR#134 review |
| A8 | **CI-DIGEST-01** (🟡 可延后): testcontainers 镜像 tag+digest 双固定 | 1h | `adapters/*/integration_test.go` | PR#139 review |
| A9 | **CI-LINT-PIN-01** (🟡 可延后): golangci-lint patch 级固定 + dependabot | 1h | `.github/workflows/ci.yml` | PR#139 review |
| A10 | **OBS-LGTM-INTEGRATION-01** (Cx3, 🟡 可延后): `//go:build integration` 夜间 OTel collector 真实 OTLP 协议兼容性测试 | 2h | `adapters/otel/integration_test.go` | PR#157 review S6-04 |
| ~~A11~~ | ~~**OUTBOX-RELAY-WIRE-PG-01**~~ ✅ PR#174（S25+S26+S27）: relay worker 接入 bootstrap OnStart/OnStop（S25 relayWorker≠nil guard）；eventbus envelope 解包修复事件丢失（S26）；pool leak on metrics fail 修复（S27）；e2e test 重写为真实 PG→eventbus→subscriber 链路（移除 RMQ 依赖）| — | — | PR#174 |
| ~~A12~~ | ~~**READYZ-PG-SCHEMA-01**~~ ✅ PR#173：启动期 fail-fast — `VerifyExpectedVersion` 比对 goose_db_version vs embed FS max，不匹配直接 return err → os.Exit(1) | — | — | PR#173 |
| A13 | **BOOTSTRAP-WIRE-RMQ-BROKER-HEALTH-01** (🟠 条件延后): `cmd/core-bundle` 当前 publisher 是 in-memory eventbus（outbox relay 将 PG entries 转发至此）；`bootstrap.WithBrokerHealth` 未接线，/readyz 缺 RMQ 健康检查。触发条件：core-bundle 接入真实 RabbitMQ connection（替换 in-memory eventbus 为 rabbitmq.Publisher），此时同步通过 `bootstrap.WithBrokerHealth` 将 RMQ readiness 纳入 /readyz。当前 in-memory eventbus 无需 broker health probe。 | 2h | `cmd/core-bundle/main.go` + `runtime/bootstrap/` | PR#174 review F8 |

### slice / cell 收口

| # | 任务 | 工时 | 文件 | 来源 |
|---|------|------|------|------|
| ~~S1~~ | ✅ **REPO-SCAN-CLASSIFY-01**: `errors.Is(err, sql.ErrNoRows)` 判 not found，其他返回 `ErrInternal` | — | — | PR#169 合入 |
| S2 | **CONTRACT-ERROR-SCHEMA-01** (Cx1, 🟡 可延后): `publish/v1` + `rollback/v1` contract.yaml `responses` 新增 401/403 entries 引用共享错误 schema | 1h | `contracts/http/config/{publish,rollback}/v1/contract.yaml` | PR#157 post-merge |
| S3 | **DTO-NIL-SEMANTIC-01** (Cx2): 12+ handler 写成功响应前校验领域对象非 nil，避免 converter nil guard 把上游不变量异常"平滑"为空 data 成功响应 | 3h | 12+ `cells/*/slices/*/handler.go` | PR#158 six-seat review |
| S4 | **EVENT-PAYLOAD-TYPED-01** (Cx2): sessionlogin/sessionlogout/configwrite/configpublish/auditappend/auditverify 事件 payload `map[string]any` → typed event struct | 3h | 6 个 `service.go` + event contract schemas | PR#133 re-review |
| S5 | **RBAC-REVOKE-POST-01** (🟡 可延后): `DELETE /internal/v1/access/roles/revoke` 改为 `POST` 避免 DELETE body 代理兼容问题 | 1h | `cells/access-core/slices/rbacassign/handler.go` + `contracts/http/auth/role/revoke/v1/contract.yaml` | PR#143 review 6.2 |
| S6 | **RBAC-LAST-ADMIN-GUARD**: `service.Revoke` 检查剩余 admin 数量；`ports.RoleRepository` 新增 `CountByRole` | 1h | `cells/access-core/slices/rbacassign/service.go` + `ports/` | PR#143 review 2.3 |
| S7 | **VALIDATE-EVIDENCE-CI-01** (Cx2, 根治声明-代码漂移): CI 新增独立 `metadata-check` job（`gocell validate` + `check contract-health`），失败阻断 PR | 1h | `.github/workflows/ci.yml` + PR template | PR#155 review F7 |
| S8 | **H1-7 RBAC-OUTBOX-MIGRATION**: `rbacassign.Service` "角色变更 → 会话失效"双写 → transactional outbox 原子写入 + consumer 异步失效 session。前置 outbox consumer 基础设施 | 6h | `cells/access-core/slices/rbacassign/service.go` + `cells/access-core/slices/sessionlogout/consumer.go`（新）+ contract event schemas | PR#149 review round 2 |
| ~~S9~~ | ~~**AUTH-LEGACY-TOKEN-STRICT-01**~~ ✅ PR#162 删 2-part 分支；PR#293 落地可观测性（warn log + metrics `legacy_format` label）；项目无外部消费方、不向后兼容，strict 模式开关和淘汰计划不再需要 | — | — | PR#159 外部审查 |
| S10 | **MODE-SEMANTIC-SPLIT-01** (Cx2, 🟡 可延后): 读路径 `query.RunMode`（cursor 容错）与写路径 `configpublish.WithRunMode`（publisher fail-open）当前共用同一枚举，为后续任一方向演进埋下耦合。保留"Init 单点翻译"前提，新增写路径独立类型（如 `configpublish.PublishFailureMode` with `FailClosed`/`FailOpen`），Cell Init 并行映射 `DurabilityMode → (RunMode, PublishFailureMode)` 后注入。触发条件：任一方向需要新增非二元模式值时。对标 Uber fx Provide/Decorate — 每个决策独立类型注入。 | 3h | `pkg/query/runmode.go` + `cells/config-core/slices/configpublish/service.go` + 4 处 `cell.go` Init | PR#167 round-2 review（finding 3 改进项，发现时建议暂缓） |
| S11 | **CONFIG-CORE-INIT-COGNIT-01** (Cx3, 🟡 可延后): `cell.go::Init()` 认知复杂度 19（`//nolint:gocognit` 临时抑制）；拆 `validateDeps()` / `buildCursorCodec()` / per-slice builder 三段式，降至 ≈9。来源：PR#168 /fix 诊断 | 3h | `cells/config-core/cell.go` | PR#168 发现（nolint 临时静音） |
| S12 | **AUTH-GUARD-INLINE-UNIFY-01** (Cx3, 🟡 可延后): `RequireAnyRole → WriteDomainError → return` 三行 guard 在全库 11 处 inline；若统一提取为 `httputil` 辅助需同改 access-core + config-core + configpublish，横跨 3 cell。局部提取反而不一致，建议全库同期统一或维持现状。来源：PR#168 code-review P2 | 2h | `cells/*/slices/*/handler.go` × 11 处 | PR#168 review P2（维持现状待全库统一） |
| S13 | **CONFIGWRITE-4XX-OBSERVABILITY-01** (Cx2, 🟡 可延后): `WriteDomainError` 对 4xx（含 401/403）缺少服务端诊断日志；建议在 `writeErrcodeError` 4xx 分支增加 code+path+request_id 采样日志，避免权限漂移场景排障慢。来源：PR#168 六席审查 P2 | 1h | `pkg/httputil/response.go` | PR#168 六席审查 P2（OUT_OF_SCOPE，留下迭代） |
| S14 | **CONFIG-VALUE-ENCRYPTION-01** (P1↑, Cx3): `config_entries.value` + `config_versions.value` 明文写库；sensitive=true 脱敏只在响应层（`config_entry.go` + `handler.go`），持久化边界无保护，DB 读权限即可拖取全量历史值。修法：加密放到 repo 写边界之前，同时覆盖 entries + versions；需 KMS 选型 + key rotation 独立 ADR，不混入普通 bugfix。优先级从 P2 升 P1。来源: PR#169 review F-S-2 + 2026-04-18 静态审查 | — | `cells/config-core/internal/adapters/postgres/config_repo.go` + `adapters/postgres/migrations/` | PR#169 + 2026-04-18 |
| S15 | **ERROR-CTX-CANCELLED-CLASSIFY** (P3, Cx2, 🟡 可延后): `ctx.Canceled` 在 config_repo 及其他 PG 路径未单独归类为 `ErrContextCanceled`，当前统一为 `ErrConfigRepoQuery`；reviewer 可接受当前状态，但长期会影响 caller 区分超时 vs 业务错误。来源: PR#169 review F-T-3 | 1h | `cells/config-core/internal/adapters/postgres/config_repo.go` | PR#169 review F-T-3 |
| S16 | **RUNTIME-TOPOLOGY-SINGLE-SOURCE-01** (P2, Cx3, 🟡 可延后): PR#169 只做了最小修复（validateModeCoupling fail-fast + adapterInfo 派生自 cellAdapterMode），彻底方案是把"已解析的实际运行拓扑"抽象为单一事实源 struct，同时驱动 repo wiring / adapterInfo / /readyz / /metrics / 生产门禁，避免未来新增 adapter 再一次出现 `GOCELL_ADAPTER_MODE` vs `GOCELL_CELL_ADAPTER_MODE` 分裂。对标 go-zero serviceconf / Kratos config。来源: PR#169 review F-NEW-2 彻底方案 | 6h | `cmd/core-bundle/main.go` + 新 runtime 抽象 | PR#169 review F-NEW-2（最小修复已合；彻底方案待） |
| S17 | **POOL-FRAMEWORK-LIFECYCLE-01** (P2, Cx3, 🟡 可延后): PR#169 在 cmd/core-bundle 层手工接 `defer pgPool.Close()` + `bootstrap.WithHealthChecker("postgres", pool.Health)`；彻底方案是把 `*adapterpg.Pool`（以及 Redis / RMQ 等外部资源）提升为 bootstrap/assembly 层面的托管资源，统一 shutdown LIFO + 自动 health checker 注册，对标 Uber fx OnStart/OnStop + K8s readyz storage_readiness_hook。来源: PR#169 review F-NEW-3 彻底方案 | 4h | `runtime/bootstrap/` + `kernel/assembly/` | PR#169 review F-NEW-3（最小修复已合；彻底方案待） |
| S18 | **JWT-AUDIENCE-ENV-VAR-01** (P1, Cx2, 🟠 条件延后，多环境部署前触发): `jwtAudience` 为编译期常量，多环境（staging/prod）无法通过 env var 区分（如 `gocell-staging` / `gocell-prod`）；同时 sessionlogin/sessionrefresh 内 audience 需同步注入，否则 issuer 与 verifier drift 风险仍在。**修法**：新增 `GOCELL_JWT_AUDIENCE` env var；`adapterMode=real` 时强制要求；access-core cell 新增 `WithTokenAudience(string)` option 让 Init 注入而非硬编码。**前置**：与 F-O-1/F-A-1 配套，需独立 ADR。来源: PR#170 review F-O-1 + F-A-1 | 3h | `cmd/core-bundle/main.go` + `cells/access-core/cell.go` + `cells/access-core/slices/sessionlogin/service.go` + `sessionrefresh/service.go` | PR#170 review |
| ~~S20~~ | ~~**SSO-BFF-CONFIG-WALKTHROUGH-TEST-01**~~ ✅ PR#172: config-core 接入测试服务器，补 POST/PUT/GET config + feature flags 4 个子测试，10/10 PASS | — | — | PR#172 review P2 |
| ~~S21~~ | ~~**SSO-BFF-HMAC-KEY-DOC-01**~~ ✅ PR#172: `auditHMACKey` 加注释说明 32 字节原因（SHA-256 block size） | — | — | PR#172 review P2 |
| S22 | **REFRESH-AUD-REAL-ROUTE-TEST-01** (P2, Cx2, 🟡 可延后): `runtime/auth/middleware_aud_test.go` 的 refresh-path audience 回归测试手造 fake handler 直接调 `VerifyIntent`，不经过真实 route registration / body binding / `Service.Refresh`，无法检测 public-route bypass 或 body decode 回归。**修法**：在 `cells/access-core/auth_integration_test.go` 补一条真实 HTTP 测试：POST `{"refreshToken": wrong-aud-token}` 到 `/api/v1/access/sessions/refresh`，断言返回 401；再补 missing-aud token 场景。**前置**：可独立于 S18 实施。来源: PR#171 外部审查 F2 | 2h | `cells/access-core/auth_integration_test.go` + `runtime/auth/middleware_aud_test.go` | PR#171 外部审查 |
| S19 | **JWT-AUDIENCE-DRIFT-INTEG-TEST-01** (P2, Cx1, 🟡 可延后): `cmd/core-bundle/jwt_aud_integration_test.go` 直接调 `deps.issuer.Issue(…, []string{jwtAudience}, …)` 而非真实 sessionlogin 路径，无法检测 sessionlogin/sessionrefresh 硬编码与 `DefaultJWTAudience` 的 drift。**修法**：集成测试调用 sessionlogin.Service.Login → 解析响应 token → VerifyIntent，使 drift 编译失败而非运行时静默放行。**前置**：S18（access-core WithTokenAudience 注入）落地后更易实现。来源: PR#170 review F-T-3 | 2h | `cmd/core-bundle/` + `cells/access-core/slices/sessionlogin/` | PR#170 review F-T-3 |
| S20 | **JWT-AUDIENCE-STARTUP-LOG-01** (P3, Cx1, 🟡 可延后): `buildJWTDeps` 构建时未打印 effective audience 值；ops 无法通过启动日志或 `/readyz?verbose` 确认运行时 audience 配置。**修法**：`slog.Info("JWT audience enforcement enabled", slog.String("audience", jwtAudience))` 紧接 verifier 构造后添加。来源: PR#170 review F-S-5 | 0.5h | `cmd/core-bundle/main.go` | PR#170 review F-S-5 |
| S21 | **JWT-AUD-TEST-TABLE-DRIVEN-01** (P3, Cx1, 🟡 可延后): `runtime/auth/jwt_aud_test.go` 9 个场景为独立函数，违反 CLAUDE.md table-driven 规范。**修法**：重构为 `struct { name; expectedAuds []string; tokenAud []string; wantErr bool; errContains string }` 结构。来源: PR#170 review F-T-5 | 1h | `runtime/auth/jwt_aud_test.go` | PR#170 review F-T-5 |
| S23 | **AUTH-WALKTHROUGH-COMPOSE-01** (P2, Cx3, 🟡 可延后): `examples/sso-bff/walkthrough_test.go` 手装精简 server（无 bootstrap lifecycle），与 `main.go` 的真实组装路径、README 步骤三份语义各自独立，可掩盖 public-endpoint wiring / config-core 接线 / audit event delivery 的回归。**修法**：提取 `NewSSOBFFApp(opts...)` 组装函数被 `main.go` 和 walkthrough test 共用；test server 走同一 bootstrap + Start/Stop 路径。来源: PR#172 review F3（OUT_OF_SCOPE） | 4h | `examples/sso-bff/bootstrap.go`（新）+ `main.go` + `walkthrough_test.go` | PR#172 review F3 |
| S22 | **AUTH-MIDDLEWARE-AUD-REFRESH-E2E-01** (P3, Cx2, 🟡 可延后): `TestAuthMiddleware_WrongAudience_RefreshPath_Returns401` 直接调 `verifier.VerifyIntent()`，未经过 `AuthMiddleware` 真实中间件链（parse req → call verifier → write error）。本质是在测 verifier 行为而非 middleware 集成。生产 refresh 路径确实也是 `VerifyIntent` 直调，无实际安全风险；但若未来 middleware 有 early-return/short-circuit 改动，该测试无法检测 regression。**修法**：用 `httptest.NewServer` + 真实 `AuthMiddleware` + `makeTokenWithAud` 覆盖 refresh-path wrong-audience 场景，使测试打到完整链路。来源: PR#293 round-2 /fix 分析 | 1h | `runtime/auth/middleware_aud_test.go` | PR#293 round-2 |
| ~~S24~~ | ~~**BROKER-HEALTH-NIL-FAILFAST-01**~~ ✅ PR#174 6462614: `WithBrokerHealth(nil)` 和 typed-nil 检测 + `brokerHealthNil` 标记 + Run step 0 fail-fast，与 `WithCircuitBreaker` 模式对齐；测试覆盖 plain nil 和 typed-nil 两种场景 | — | — | PR#174 review /fix 派生 |
| ~~S25~~ | ~~**OUTBOX-E2E-REAL-WIRE-GUARD-01**~~ ✅ PR#174 2949487: `cmd/core-bundle/outbox_e2e_integration_test.go` 改为调用真实 `buildConfigCoreOpts`，assert `relayWorker != nil` 作为 A11 regression guard，取代原来手工 `adapterpg.NewOutboxRelay` 绕过生产路径的写法 | — | — | PR#174 review /fix 派生 |
| ~~S26~~ | ~~**OUTBOX-ENVELOPE-EVENTBUS-UNWRAP-01**~~ ✅ PR#174 81dba4f: 真实 PG 生产路径用 in-memory eventbus 作为 relay publisher，relay 封 `outboxMessage` envelope 后 eventbus 不解包，subscribers 解析 envelope 当业务 payload → Action 空 → default "unknown action" 静默 ACK → 事件丢失。修复：eventbus.Publish 识别 envelope 并解包，镜像 rabbitmq.unmarshalDelivery；测试覆盖 envelope + 直发两路径；e2e test 重写为真实 PG→eb→subscriber 链路（移除 RMQ，core-bundle 不构造 RMQ 连接） | — | — | PR#174 review F1 |
| ~~S27~~ | ~~**OUTBOX-POOL-LEAK-ON-METRICS-FAIL-01**~~ ✅ PR#174 faefc29: `buildConfigCoreOpts` NewPool 成功后 NewProviderRelayCollector 失败路径返回 pool=nil 导致 DB 连接泄漏。修复：metrics 错误路径加 pool.Close()，acquire/rollback 职责收在同层 | — | — | PR#174 review F2 |
| ~~S28~~ | ~~**OUTBOX-ENVELOPE-KERNEL-SHARE-01**~~ ✅ PR#511（S30 搭车）：envelope struct + UnmarshalEnvelope/MarshalEnvelope 提升到 `runtime/outbox/envelope.go`（不是 kernel/outbox —— 因 Store interface 同期落在 runtime 层，envelope 与 Store 共包更内聚）；`runtime/eventbus` + `adapters/rabbitmq` 全部改调 `outbox.UnmarshalEnvelope`；rabbitmq 保留 PascalCase legacy Entry fallback | — | — | PR#174 review F1 跟进 |
| S29 | **CORE-BUNDLE-APP-BUILDER-01** (P2, Cx3, 🟡 可延后): `cmd/core-bundle/main.go` 的装配逻辑与 integration tests 里的装配是两条代码路径；`buildConfigCoreOpts` 是共享点，但 bootstrap options 列表（publisher/subscriber/workers/adapterInfo/public endpoints/durability mode）目前只在 main.go 维护，测试靠手工复制。抽出 `cmd/core-bundle.BuildApp(opts...) (*bootstrap.Bootstrap, func(), error)` 被 main 和 integration test 共用，避免拓扑事实源再次分裂。对标 Uber fx `App.New` + Kratos `App.Run` 统一入口模式 | 6-8h | `cmd/core-bundle/app.go`（新）+ `main.go` + `outbox_e2e_integration_test.go` | PR#174 review F1 跟进 |
| ~~S30~~ | ~~**OUTBOX-STORE-ABSTRACTION-01**~~ ✅ PR#511: `runtime/outbox.Store` 7 方法 interface（ClaimPending/MarkPublished/MarkRetry/MarkDead/ReclaimStale/CleanupPublished/CleanupDead），`adapters/postgres.PGOutboxStore` 实现并通过 `runtime/outbox/outboxtest.RunStoreConformanceSuite` 验证；relay 编排从 adapter 搬到 `runtime/outbox/relay.go`（三 goroutine 架构保留）；`outboxtest.FakeStore` 暴露为 public in-memory 实现（cell/slice 单测可用，覆盖 91.8%）；同 PR 搭车 S28 envelope 共享 + X7（AL-01）outbox_relay.go 搬迁。净减 ~2.8kloc adapter 代码 | — | — | PR-OUTBOX-WIRE 方案 α 裁定 + PR#511 落地 |

### 发布 + 文档

| # | 任务 | 工时 | 文件 | 来源 |
|---|------|------|------|------|
| ~~F1~~ | ~~**ADR-RUNMODE-TRANSLATION-01**~~ ✅ PR-P-QUERY: `docs/architecture/202604180100-adr-runmode-translation.md` 记录 pkg 不依赖 kernel、cell Init 单次翻译、禁止二次翻译，对标 go-zero `ServiceConf.Mode` | — | — | PR-P-QUERY 合入 |
| F2 | **SYSTEM-TOPOLOGY-API** (🟡 可延后): `GET /internal/v1/system/topology` 返回 cell/slice/contract 拓扑 JSON；基于 `kernel/registry` | 4h | 新 slice 或 `runtime/bootstrap/` | 历史 Batch 8 |
| F3 | **P2-T-02 audit e2e 测试**: Journey 级验收 | 2h | `journeys/` + integration test | 历史 Batch 8 |
| F4 | Review cells/ | 4h | — | Wave 4 |
| F5 | Review examples/ | 2h | — | Wave 4 |
| F6 | Review 报告汇总 | 2h | — | Wave 4 |
| F7 | 发布文档 | 4h | — | Wave 4 |
| F8 | 性能基准 | 4h | — | Wave 4 |
| F9 | **v1.0 tag** | — | — | Wave 4 |
| F10 | **TEST-JOURNEY-ASSEMBLY-HARNESS-01** (Cx3, 🟡 可延后): `tests/integration/journey_test.go` 全部 28 条均 `t.Skip("stub: requires full assembly")`；需要建 full-assembly harness 后统一恢复（J-session-refresh / J-session-logout / J-account-lockout / J-audit-login-trail / J-config-* / J-sso-login / J-user-onboarding），不只 refresh 一条 | 8h | `tests/integration/` + assembly fixture | PR#166 R2-P2（范围扩大） |

---

## P3 长期 / 大型独立

| # | 任务 | 工时 | 前置 | 来源 |
|---|------|------|------|------|
| X1 | **PG-DOMAIN-REPO**: 5 个域 Repository PostgreSQL 实现（User/Session/Role/Device/Command）。前置: ① 4 migration DDL + **CONFIG-VERSIONS-MIGRATION-01** (`004_create_config_entries_and_versions.sql`)；② `ports.RoleRepository.CreateRole`。**落地联动**（同 PR 或紧邻 PR）: **RBAC-ASSIGN-LEVEL-UPGRADE-01** L0→L1；**SEED-ROLE-IFACE-01** 去 type assertion；**ACCESS-LEVEL-AUDIT-01** slice.yaml 校正；**AUTH-CACHE-01 激活** Redis session cache + 撤销失效路径 | 3-5d | — | PR#155 review F4 + 多 PR 汇总 |
| X2 | **WM-35 BFF handler 接入 cookie session** ★ | 2d | WM-2-F1 ✅ | 长期 roadmap |
| X3 | **WM-36 SecureCookie key rotation** ★ | 1.5d | WM-35 | 长期 roadmap |
| X4 | **WM-7 泛型 BulkResult** | 1d | — | 历史 Batch 8 |
| X5 | **P3-TD-11 access-core domain 拆分** User/Session/Role | 4h | X1 | 历史 Batch 8 |
| X6 | **28 SOL-B-01 Claimer lease 续租** | 4h | L4 API ✅ | Wave 2 |
| ~~X7~~ | ~~**AL-01 outbox_relay.go 轮询调度 → `runtime/outbox/relay.go`**~~ ✅ PR#511（S30 搭车）：`adapters/postgres/outbox_relay.go` + `safe_relay_collector.go` 已删除，relay 循环住在 `runtime/outbox/relay.go` | — | — | 依赖替换分析 |
| ~~X8~~ | ~~**AL-02 distlock.go 续期/TTL → `runtime/`**~~ ✅ PR-DISTLOCK-HOIST (PR#178): `runtime/distlock` 新增 `Locker`/`Lock` provider-neutral 接口（镜像 PR#177 `runtime/outbox.Store` 分层模式）；`adapters/redis.DistLock`/`Lock` 实现新接口；新增 `Lock.Lost() <-chan struct{}` 续租失败信号；修正 `ERR_ADAPTER_REDIS_LOCK_ACQUIRED` 拼写 typo → `ERR_DISTLOCK_ACQUIRE` | — | — | 依赖替换分析 |
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
| T2 | **AUTH-ISSUE-OPTIONS-01** `JWTIssuer.Issue()` 重构为 `IssueOptions` struct | 1h | Issue() 第 5 个参数 |
| T3 | **DEVICE-ENQUEUE-RBAC** HandleEnqueue 无设备维度鉴权 | 2h | 多租户 operator |
| T4 | **CB-RESILIENCE-PACKAGE-01** 把 `Allower` / `CircuitBreakerRetryAfter` 从 `runtime/http/middleware` 迁移到 `runtime/resilience/circuitbreaker/` 独立包 | 4h | 出现第二个非 HTTP 的 CB 消费方 |
| T5 | **AUTH-SIGNER-01** `SigningKeyProvider` 返回 `crypto.Signer` 替代 `*rsa.PrivateKey` | 2h | golang-jwt v6 发布 |
| ~~T6~~ | ~~**GOCELL-PER-CELL-ADAPTER-01**~~ **不做**：决策全量 PG 接入（所有 cell 共用 `GOCELL_CELL_ADAPTER_MODE` 全局开关），per-cell 覆盖仅过渡期有价值，全量接完后变死代码。`buildAccessCoreOpts` 等直接复用全局开关。 | — | — | 2026-04-18 设计裁决 |
| ~~T7~~ | ~~**CONFIG-VERSIONS-CONFIG-ID-INDEX**~~ ✅ PR#173：`006_add_config_versions_config_id_index.sql` + TestMigration006 | — | — | PR#173 |

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
| P1 | ~32.5h |
| P2 kernel/runtime | ~12-16h |
| P2 adapter | ~16h |
| P2 slice/cell | ~18h |
| P2 发布 + 文档 | ~23h + v1.0 tag |
| **核心路径合计（不含 P3）** | **~108-112h（约 13-14 工作日）** |
| P3 长期 | 3-5d + 4.5d + 若干独立项 |

---

## 实施计划

按层执行顺序见: [docs/plans/20260418-bottom-up-implementation-plan.md](plans/20260418-bottom-up-implementation-plan.md)

关键路径：P0 阻塞 → kernel/runtime 偿债 → pkg/adapter 偿债 → slice/cell 收口 → 功能 + 发布 → v1.0 tag
