# Backlog Completed Items Archive

> Archived from `docs/backlog.md` on 2026-04-28 05:37.
>
> Source baseline at cleanup time: `origin/develop @ 2e722ee9`.
>
> Initial archived completed rows: 41; split/superseded notes: 2; inline notes: 3.
>
> 2026-04-29 incremental append: 6 completed rows after pulling `origin/develop @ ad986cad`.

## Incremental Completed Rows — 2026-04-29

| # | 归档口径 | 落地 |
|---|----------|------|
| PR-CFG-6 | `OUTBOX-EMIT-FAILOPEN-DROP-COUNTER-01` 已关闭：`NewDirectEmitter` 注册 `outbox_emit_failopen_dropped_total{cell, topic}`，fail-open 分支自增并接入 ratio readiness checker；outbox topic archtest 通过 typed const evaluation 覆盖 literal / 同包 const / 跨包 selector。 | PR #321 |
| MULTI-REVIEW-RES-2 | `OUTBOX-FAILOPEN-SECURITY-EVENT-ALERT-01` 剩余索引指针关闭：per-entry fail-open 策略、三 cell fail-closed 默认、security/audit topic archtest、relay readyz 聚合、drop counter、ratio readiness checker、const topic resolver 均已落地。 | PR #321 |
| READYZ-PUBLIC-ERRCODE-TAXONOMY-EVAL-01 | `/readyz` 503 public code 统一为 `ERR_SERVICE_UNAVAILABLE`，原 unhealthy / drain 机器语义迁入 `error.details.status` + `error.details.reason`。 | PR #323 |
| PR237-A6 | `BOOTSTRAP-LISTENER-SLICE-01` 的 phase7 listener runtime 已物理拆到 `runtime/bootstrap/bootstrap_phase7.go`，并按 `listenerConfigs` 迭代绑定/启动，不再把 phase7 装配混在 `bootstrap_phases.go` 中。 | PR #326 |
| S23 | `AUTH-WALKTHROUGH-COMPOSE-01` 已关闭：`examples/ssobff` 抽出 `NewSSOBFFApp`，`main.go` 与 walkthrough test 共用同一 bootstrap 组装路径。 | PR #325 |
| PR220-e2 | generated boundary / metrics schema regenerate-and-diff gate 已落地，包含 untracked generated artifact 检查；本地 `make verify` 也通过 `hack/verify-generated.sh` 覆盖。 | PR #321 |

## Inline Completed Notes

- P0: L0 / L1 已合入，主 backlog 仅保留“无 P0 阻塞项”状态。
- T8 PUBLIC-ENDPOINT-STRUCT-MIGRATE-01: 已在 PR #201 触发并完成。
- CONTRACT-META-01: 已在 PR #239 合入。

## Split / Superseded Items

| # | 归档口径 | 残余 |
|---|----------|------|
| S13-follow | `4XX-LOG-SAMPLING-01` 的 list-boundary 范围已由 PR #319 闭合：分页/list handler 通过 request context sampling 降低 4xx 日志噪音。 | 非 list endpoint / route-level error policy 未关闭，主 backlog 保留 `ROUTE-ERROR-POLICY-01`。 |
| PR220-e2 | `assemblies/corebundle/generated/boundary.yaml` 生成文件本身已由 PR #278 落地并纳入 git tracking，#319 也补 parser/status-board drift 相关覆盖。 | generated artifact drift hard gate 仍保留在主 backlog，拆为 `GENERATED-BOUNDARY-DRIFT-GATE-01`，对齐 PR-CFG-I.X2 / PR-CI-1。 |

## P1 待办

| # | 任务 | 工时 | 文件 | 来源 |
|---|------|------|------|------|
| ~~PR298-RESIDUAL~~ | ~~**INPUT-CONSTRAINT-AND-TRANSPORT-HARDENING-01**~~ ✅ **FIXED @ PR #301 / commits `ac3f3e74`, `caf1051f`**: PR#298 残余六席位审查阻塞项已收口：(1) FMT-25 覆盖 `number`、JSON Schema `type` 数组与 `unevaluatedItems`；(2) 非本地 / 不可解析 `$ref` 与深度阈值截断 fail-closed；(3) `minLength<=maxLength` / `minimum<=maximum` 关系校验；(4) Redis Sentinel `rediss://` 地址解析进真实 `FailoverOptions.TLSConfig`，并修复共享 SNI 与 Sentinel URL 凭据语义；(5) WebSocket upgrade 失败响应体脱敏，且非 hijacker writer 在写 101 前失败；(6) env / metadata / scaffold 文档同步输入约束与 UUID 参数约定。验证：`go build ./...`、`go test ./...`、修改包 `golangci-lint` 0 issues、`go run ./cmd/gocell validate --strict`。 | — | `kernel/governance/rules_strict_extra.go` + `adapters/redis/client.go` + `adapters/websocket/handler.go` + docs/schema/templates | PR#298 residual review + /fix + /ship review |
| ~~PR302-FIX-1~~ | ~~**CONFIGPG-WITHPOOL-NIL-POOL-FAILFAST-01**~~ ✅ **FIXED @ PR #302 / commit `9d76f564`**: `cells/configcore/postgres.WithPool` 不再接受 nil pool 并构造潜伏坏仓储；API 改为 error-first `(configcore.Option, error)`，corebundle PG 组装阶段传播 `ErrCellInvalidConfig` 并关闭已打开资源，避免 request-path nil deref。 | — | `cells/configcore/postgres/options.go` + `cmd/corebundle/bundle.go` | PR #302 review P1-1 + /fix |
| ~~PR302-FIX-2~~ | ~~**LAYER10-TYPED-LOAD-FAIL-CLOSED-01**~~ ✅ **FIXED @ PR #302 / commit `9d76f564`**: LAYER-10 对 root cell package 的 typed load/type error、缺 `Types`/`TypesInfo`/syntax、导出符号缺 type info 全部产出 violation，不再在不可信输入上静默跳过；新增 synthetic regression 覆盖 ill-typed / partial package。 | — | `tools/archtest/archtest_test.go` | PR #302 review P1-2 + /fix |
| ~~PR258-RES-4~~ | ~~**BIND-FAILURE-PORT-REBIND-ASSERT-01**~~ ✅ **DONE in PR-A52**：`runtime/bootstrap/dual_listener_test.go::TestTripleListener_MidBindFailure_RollsBackEarlierBindings` 已从"只断言 Run 返回错误 + 不 hang"升级为端口回收实证。复核发现当前 `phase7BindListeners` 按 `ref.String()` 绑定，顺序为 `health -> internal -> primary`，因此实现改为让 `primary` 最后碰撞失败，并从 bind log 捕获 `health/internal` 的真实 `:0` 地址；rollback 后立即 `net.Listen("tcp", addr)` 成功，证明 `closeOwnedSockets` 释放了早先绑定的 bootstrap-owned sockets。 | — | `runtime/bootstrap/dual_listener_test.go` | PR-A52 |
| ~~PR-CFG-1~~ | ✅ **READYZ-RELAY-PROBE-FORWARD-01 已关闭**：2026-04-27 基于 `develop @ b5131358` 复核确认，relay 已在 `cmd/corebundle/bundle.go::buildConfigCoreOpts` 中通过 `bootstrap.WithManagedResource(relayWorker)` 独立注册；`bootstrap.expandManagedResources()` 会自动把 `Relay.Checkers()` 的 `outbox-relay-poll/reclaim/cleanup` 纳入 `/readyz?verbose`。继续把 relay checker 合并进 `PGResource.Checkers()` 会与独立 relay ManagedResource 产生重复 checker 并导致启动 fail-fast。保留 `PGResource` 只负责 postgres pool probe + Close，relay 继续独立负责 Worker/Close/Checkers；`docs/patterns/pg-cell-template.md` 已同步该 wiring 模式。验证：`go test ./runtime/bootstrap -run TestRelay_AsManagedResource`、`go test ./adapters/postgres -run TestPGResource_`、`go test -tags=integration ./cmd/corebundle -run 'TestBuildConfigCoreOpts_Postgres_SchemaMatched|TestBuildConfigCoreOpts_PGMode_ManagedResourceNonNil|TestConfigCoreModule_Provide_UsesConfigCoreDatabaseURL'`。 | 0 | 已由 `runtime/bootstrap/managed_resource_test.go` + `runtime/outbox/relay_test.go` + corebundle integration wiring 覆盖 | 2026-04-27 PR-A53 narrowed 复核 + PR #304 |
| S-5xx-code-mask | ✅ #317 **5XX-ERRCODE-RESPONSE-MASK-01** (Cx2, **安全加固**, 🟡 可延后): 所有 5xx 错误响应的 `error.code` 字段直接透传 `errcode.Code` 字符串（如 `ERR_KEY_PROVIDER_AUTH_FAILED` / `ERR_AUTH_ROLE_FETCH_FAILED` / `ERR_VAULT_AUTH_FAILED` / `ERR_CONFIG_DECRYPT_FAILED` / `ERR_CONFIG_ENCRYPT_FAILED`（PR#279 新增）等 20+ 个 500/503 code）。`httputil.WriteError` 已把 message 统一成 `"internal server error"`，但 code 字符串未屏蔽，攻击者可据此区分基础设施故障类型（密钥服务 / Vault / 角色仓库 / 加解密），构成轻度枚举泄漏。**修复**：`pkg/httputil/response.go` `writeErrcodeError` 在 `status>=500` 时把响应中的 `code` 统一改为 `ERR_INTERNAL`（或 503 改为 `ERR_SERVICE_UNAVAILABLE`），原 code 保留到 slog 供 operator 观测；客户端只见通用码。**实现选项**：(a) 最小改动 — `writeErrcodeError` 内 5xx 分支硬编码替换；(b) 推荐 — `pkg/errcode/errcode.go` 给每个 5xx 码加 `PublicCode` 标注（默认 `ERR_INTERNAL`，可白名单透出 e.g. `ERR_CLIENT_CANCELED`），让客户端语义可控（重试/分流提示不丢）。**影响面**：`pkg/httputil/response_test.go` 多个 `wantStatus: 5xx` + code 断言用例需要翻新；一并审计 `ErrKeyProviderTransient` / `ErrVaultAuthFailed` / `ErrCircuitOpen` 503 是否统一 `ERR_SERVICE_UNAVAILABLE`。**落地**：#317 将 public 5xx taxonomy 上提到 `pkg/errcode.PublicCode/PublicCodeForStatus`，500 默认 `ERR_INTERNAL`，503 `ERR_SERVICE_UNAVAILABLE`，504 保留 `ERR_SERVER_TIMEOUT` 公开机器语义，原始内部 code 保留在 slog。**触发**：每次新加 5xx 码时此问题面被动扩大（PR#241、PR#279 均补证），到第 30 个码时启动；或外部安全审计提出。**配套源 finding**：PR#241 六维度审查 F2+F3、PR#279 review §4 议题 A（2026-04-26 重审复证）。 | 3h | `pkg/httputil/response.go` + `pkg/errcode/errcode.go` + `pkg/httputil/response_test.go` | PR#241 六维度审查 F2+F3（OUT_OF_SCOPE）+ PR#279 review §4-A |
| ~~PR220-3~~ | ~~**JOURNEY-VERIFY-FAIL-CLOSED-01**~~ ✅ **DONE @ PR #295**：`kernel/verify/ref.go:52-58` 已将 journey `RunPattern` 固定为 `^Test{JourneyID}{Suffix}$`；`kernel/verify/gotest.go:19-20,40,71-74` + `kernel/verify/runner.go:308-315` 已把 “matched only skipped tests” 视为失败（fail-closed）。旧的 `tests/integration/journey_test.go` 路径已不存在。 | — | `kernel/verify/ref.go` + `kernel/verify/gotest.go` + `kernel/verify/runner.go` | PR #295 |
| ~~PR-CFG-A-DEFER-1~~ | ~~**READYZ-VERBOSE-FAILOPEN-FIX-01**~~ ✅ **DONE @ PR #297**：`runtime/http/health/health.go:557-565` 现已在 `token==""` 时拒绝 verbose（401），不再 fail-open 泄漏内部依赖拓扑。`WithReadyzVerboseDisabled` 仍可显式关闭 verbose 通道。 | — | `runtime/http/health/health.go` | PR #297（commit `debd6bfb`） |
| ~~V-A15~~ | ~~**INITIALADMIN-OPTION-FAILFAST-UNSUPPORTED-01**~~ ✅ **DONE @ PR #289**：`WithInitialAdminBootstrap` 在 unsupported platform 构造路径已由 `initialadmin.CheckPlatformSupported` / unsupported lifecycle path fail-fast，错误码区分为 platform unsupported。 | — | `cells/accesscore/cell_initialadmin_unsupported_test.go` + `cells/accesscore/initialadmin/platform_unsupported.go` + `cells/accesscore/initialadmin/lifecycle_unsupported.go` | PR #289 |
| ~~PR244-F1~~ | ~~**MULTIPOD-INMEMORY-NONCE-WARN-01**~~ ✅ **DONE @ PR #289**：real mode + in-memory NonceStore 需要 `GOCELL_SINGLE_POD=1` 显式承认；启动成功路径记录 single-pod replay protection acknowledgement，未承认时 fail-fast。 | — | `cmd/corebundle/main.go` + `cmd/corebundle/shared_deps.go` + `docs/ops/env-vars.md` | PR #289 |
| ~~PR244-F9~~ | ~~**AUTH-REPLAY-ERRCODE-GRANULARITY-01**~~ ✅ **DONE @ PR #289**：新增 `ErrAuthReplayDetected`，service-token replay 响应与 slog 均使用专用 code，ops alerting 也有 replay-specific 规则。 | — | `pkg/errcode/errcode.go` + `pkg/errcode/status.go` + `runtime/auth/servicetoken.go` + `docs/ops/alerting-rules.md` | PR #289 |
| ~~PR250-F1~~ | ~~**CONFIG-VERSION-PUBLISHED-SENSITIVE-FIELD-EVAL-01**~~ ✅ **DONE @ PR #292/#266 source audit**：config version-published wire schema 不再暴露 `sensitive` 字段，contract test 对 `sensitive` additional property 有拒收覆盖。 | — | `contracts/event/config/version-published/v1/payload.schema.json` + `cells/auditcore/slices/auditappend/contract_test.go` | PR #266 / PR #292 |
| ~~PR250-F2~~ | ~~**EVENT-USER-FLAG-CAMELCASE-FOLLOWUP-01**~~ ✅ **DONE @ PR #250/#292 source audit**：user/flag event schema 与 DTO 已使用 camelCase，auditappend 只提取 `actorId`/`userId`，不再保留 `UserIDSnake` 兼容字段。 | — | `contracts/event/{user,flag}/**/payload.schema.json` + `cells/accesscore/internal/dto/user_events.go` + `cells/auditcore/slices/auditappend/service.go` | PR #250 / PR #292 |

## kernel / runtime

| # | 任务 | 工时 | 文件 | 来源 |
|---|------|------|------|------|
| ~~A5a-R9~~ | ~~**INITIALADMIN-CREDPATH-NAMING-CHAIN-01**~~ ✅ **DONE @ PR #322**：删除 `accesscore.ResolveBootstrapCredentialPath` thin facade，`cmd/corebundle` / `examples/*` 直接使用 `initialadmin.ResolveCredentialPath`。 | — | `cells/accesscore/cell.go` + `cmd/corebundle/` + `examples/ssobff/main.go` | PR #322 |
| ~~A5a-R10~~ | ~~**INITIALADMIN-ERRCODE-CONSISTENCY-01**~~ ✅ **DONE @ PR #322**：`initialadmin.newBootstrapper` 配置校验统一返回 `errcode.ErrCellInvalidConfig`。 | — | `cells/accesscore/initialadmin/bootstrap.go` | PR #322 |
| ~~A5a-R11~~ | ~~**LIFECYCLECONTRIBUTOR-BLOCKING-SEMANTICS-WARN-01**~~ ✅ **DONE @ PR #322**：`runtime/bootstrap` 对接近有效 timeout 的 OnStart hook 记录 slow-start warn，并补单测覆盖。 | — | `runtime/bootstrap/lifecycle.go` + `runtime/bootstrap/lifecycle_test.go` | PR #322 |
| ~~PR-CFG-G1-FU3~~ | ~~**ACCESSLOG-LISTENER-DIMENSION-01**~~ ✅ **DONE in PR-A52**：`runtime/http/router/router.NewForListener` 通过 runtime-local context middleware 注入 `ref.String()`；`runtime/http/middleware/access_log.go` 在值非空时追加 `slog.String("listener", ref)`。`router.New()` / standalone middleware 的 zero listener 不输出空字段。新增 `tools/archtest/listener_dx_test.go` 防止旧 listener API / `Delegated` 示例回流。 | — | `runtime/http/router/router.go` + `runtime/http/middleware/access_log.go` + `tools/archtest/listener_dx_test.go` | PR-CFG-G1 六角色 review 运维席 #8 / PR-A52 |
| ~~PR-CFG-G2-FU1~~ | ~~**AUTH-ROLE-INTERNAL-CLIENTS-AUDIENCE-01**~~ ✅ **DONE @ PR #293**：`assign/revoke` 两个 internal contract 已修正为 `endpoints.clients: []`（内部路径不再误标 external actor），`list` 保持 public `/api/v1/...` + `edge-bff`，语义已对齐。 | — | `contracts/http/auth/role/{assign,revoke,list}/v1/contract.yaml` | PR #293（REF-17） |
| ~~PR-CFG-G1-FU7~~ | ~~**E2E-COMPOSE-SECRETS-CI-INJECTION-01**~~ ✅ **DONE @ PR #292 (commit 7390da4e — SonarCloud blocker triage)**: PR-CFG-G1 自闭环 — docker-compose 3 处 PG password 改 `${E2E_PG_PASSWORD:?required}` 强制 env 必传（无软回退）；`.github/workflows/_build-lint.yml::e2e` job env 显式注入 E2E_PG_PASSWORD；新增 `tests/e2e/.env.e2e.example` + `.gitignore` 加 `*.env.e2e` 模式；bootstrap-admin.sh 默认密码改注释明示 e2e 临时凭证。剩余 5 个 GOCELL_*_KEY/SECRET/TOKEN 仍为字面量（test fixture 性质，已加 e2e fixture 声明注释 + Sonar e8 规则忽略 examples/**） — 下次有 GH Secrets 真注入需求时再做。 | — | — | — |

## adapter

| # | 任务 | 工时 | 文件 | 来源 |
|---|------|------|------|------|
| ~~A8~~ | ~~**CI-DIGEST-01**~~ ✅ **DONE @ PR #305**：testcontainers image pinning/digest baseline 随 Vault/RMQ diagnostic PR 收口。 | — | `tests/testutil/images.go` | PR #305 |
| ~~A9~~ | ~~**CI-LINT-PIN-01**~~ ✅ **DONE @ PR #305**：CI lint pinning baseline 随 Vault/RMQ diagnostic PR 收口。 | — | `.github/workflows/` | PR #305 |
| ~~A10~~ | ~~**OBS-LGTM-INTEGRATION-01**~~ ✅ **DONE @ PR #320**：新增真实 OTLP/gRPC Collector integration test、PR/push smoke gate 与 nightly/manual workflow 诊断，Docker strict 由 `GOCELL_TEST_DOCKER_REQUIRED=1` 驱动。 | — | `adapters/otel/integration_test.go` + `.github/workflows/otel-collector-nightly.yml` + `.github/workflows/_build-lint.yml` | PR #320 |
| ~~A21~~ | ~~**HEALTH-CHECKER-CTX-BUDGET-01**~~ ✅ **DONE @ PR #228（后续由 PR #297 收口 fail-closed 细节）**：`runtime/http/health/health.go` 现为 `type Checker = func(context.Context) error`，`runProbesParallel` 按统一 `h.deadline` 并发执行；`kernel/lifecycle/managed_resource.go:24` 已同步 `Checkers() map[string]func(context.Context) error`。 | — | `runtime/http/health/health.go` + `kernel/lifecycle/managed_resource.go` | PR #228 + PR #297 |
| ~~LATER-AL-R~~ | ~~**RMQ-STATUS-DIAGNOSTIC-FIELDS-01**~~ ✅ **DONE @ PR #305**：RabbitMQ connection status 诊断字段随 Vault/RMQ diagnostic PR 落地。 | — | `adapters/rabbitmq/connection.go` | PR #305 |
| ~~LATER-T-1~~ | ~~**ADAPTER-TSKIP-BACKFILL-01**~~ ✅ **DONE @ PR #320**：Postgres/Vault/Corebundle/RabbitMQ/OTel integration false-green skip 收口，`tests/testutil.RequireDocker` 统一 strict Docker 语义，RabbitMQ testcontainer helper 复用，OTel collector 增 PR smoke + nightly/manual workflow。 | — | `adapters/*` + `tests/testutil/` + `.github/workflows/_build-lint.yml` | PR #320 |
| ~~PR237-A2~~ | ~~**ROUTEDECL-DELEGATED-RENAME-01**~~ ✅ **DONE @ PR #278（PR-CFG-E）**：`kernel/cell.AuthRouteMeta` 已移除 `Delegated` 语义并切到 `IsInternal()` + `InternalPathPrefix` 结构化判定，`runtime/http/router` 侧一致性校验也已同步。 | — | `kernel/cell/registrar.go` + `runtime/http/router/router.go` + `cells/accesscore/slices/rbacassign/handler.go` | PR #278（commit `50cf9fde`） |
| ~~PR239-S2~~ | ~~**INTERNAL-GUARD-REQUIRED-01**~~ ✅ **DONE @ PR #318**：internal listener 声明 `/internal/v1/*` route 时必须配置 replay-safe `cell.AuthServiceToken` guard；Noop/nil/短 keyring/混合 AuthPlan 等错误配置 fail-fast；corebundle runtime path 始终要求 internal addr + guard。 | — | `kernel/cell/auth_plan.go` + `runtime/bootstrap/` + `cmd/corebundle/` + `tools/archtest/security_defaults_test.go` | PR #318 |

## P2 PR-A9 follow-up

| # | 任务 | 工时 | 文件 | 来源 |
|---|------|------|------|------|
| ~~PR239-S1~~ | ~~**PATH-PARAM-UUID-RUNTIME-VALIDATION-01**~~ ✅ **DONE @ PR #288**：UUID path-param runtime 校验已通过 `httputil.ParseUUIDPathParam` 接入 identitymanage/sessionlogout/rbaccheck 等 handler，非法 UUID 返回 400；CH-05 静态规则防回流。 | — | `cells/accesscore/slices/{identitymanage,sessionlogout,rbaccheck}/handler.go` + `pkg/httputil/path_param.go` + `kernel/governance/rules_http_pathparam_uuid.go` | PR #288 |
| ~~PR239-S3~~ | ~~**CONTRACT-4XX-COMPLETENESS-01**~~ ✅ **DONE @ PR #288**：contract 4xx/5xx 声明完整性由 CH-04 + helper-written response scanner 覆盖，48+ contract 补齐声明，`gocell check contract-health` 0 warning。 | — | `contracts/http/**/contract.yaml` + `kernel/governance/rules_http_response_alignment.go` | PR #288 |
| ~~PR239-T1~~ | ~~**SCAFFOLD-SMOKE-01**~~ ✅ **DONE @ PR #288**：scaffold 模板生成含 UUID 校验样板的 handler，并有 smoke/fixture 覆盖 FMT-13/CH-05 期望。 | — | `kernel/scaffold/` + `kernel/scaffold/templates/handler.go.tpl` | PR #288 |
| ~~PR239-T2~~ | ~~**EXTRACTPATHPLACEHOLDERS-WHITEBOX-01**~~ ✅ **DONE @ PR #288**：path placeholder / CH-05 相关白盒与 wrong-function-placement 回归测试已覆盖。 | — | `kernel/governance/rules_http_pathparam_uuid_test.go` + `kernel/governance/rules_fmt*_test.go` | PR #288 |

## P2 PR-A14a/A18 follow-up

| # | 任务 | 工时 | 文件 | 来源 |
|---|------|------|------|------|
| ~~PR240-A1~~ | ~~**MANAGEDRESOURCE-CONTRACT-FORMALIZE-01**~~ ✅ **DONE @ PR #305**：`kernel/lifecycle/doc.go` 正式化 ManagedResource 契约，并由 archtest 覆盖 adapters/corebundle ManagedResource 实现面。 | — | `kernel/lifecycle/doc.go` + `tools/archtest/managed_resource_contract_test.go` | PR #305 |
| ~~PR240-A2~~ | ~~**PROBE-NAMING-CONVENTION-RULE-01**~~ ✅ **DONE @ PR #305**：readyz probe 命名约定写入 observability 规则文档。 | — | `.claude/rules/gocell/observability.md` + `docs/ops/readyz.md` | PR #305 |
| ~~PR240-OB1~~ | ~~**VAULT-CACHE-VERSION-METRIC-01**~~ ✅ **DONE @ PR #305**：Vault transit cached key version 暴露为 Prometheus metric，并由 corebundle provider metrics wiring 覆盖。 | — | `adapters/vault/transit_provider.go` + `cmd/corebundle/config_module_metrics_test.go` | PR #305 |
| ~~PR240-DX1~~ | ~~**RMQ-MANAGEDRESOURCE-WIRING-EXAMPLE-01**~~ ✅ **DONE @ PR #305**：RabbitMQ ManagedResource wiring 示例 / migration note 已补。 | — | `adapters/rabbitmq/doc.go` | PR #305 |

## slice / cell 收口

| # | 任务 | 工时 | 文件 | 来源 |
|---|------|------|------|------|
| ~~L8~~ | ~~**PAGINATION-HELPER-EXTRACT-01**~~ ✅ **DONE @ PR #319**：`pkg/httputil` shared pagination/list helpers 落地，audit/config/featureflag/rbaccheck list handlers 对齐。 | — | `pkg/httputil/pagination.go` + `pkg/httputil/request.go` + list handlers | PR #319 |
| ~~FEAT-1-残余~~ | ~~**RBACCHECK-PAGINATION-REAL-01**~~ ✅ **DONE @ PR #319**：rbaccheck role repo 补分页快照，handler 使用真实分页结果；`P1-8 DEVICE-LIST-API` 只保留 devicelist 主线。 | — | `cells/accesscore/internal/mem/role_repo.go` + `cells/accesscore/slices/rbaccheck/handler.go` | PR #319 |
| ~~PR220-e3~~ | ~~**STATUS-BOARD-J-ORDERCREATE-01**~~ ✅ **DONE @ PR #319/#295 source audit**：`examples/todoorder/journeys/J-ordercreate.yaml` 已补 `checkRef`，`journeys/status-board.yaml` 已登记 `J-ordercreate`，verify runner 对 active journey auto checkRef 有回归覆盖。 | — | `examples/todoorder/journeys/J-ordercreate.yaml` + `journeys/status-board.yaml` + `kernel/verify/runner_test.go` | PR #319 / PR #295 |

## 设计决策记录（历史 — 不修，避免重复审查）

| PR | Finding | 结论 | 理由 |
|---|---------|------|------|
| #137 | assembly 层不校验零值 + 非法枚举旁路 | ✅ 已修 | assembly.startInternal 加 ValidateMode 入口闸门 + CheckNotNoop 改为 allowlist |
| #140 | Request/Trace ID 生成不做 `rand.Read` 错误分支 | ✅ 已修 | Go 1.24+ `crypto/rand.Read` always-succeed-or-fatal，`_, _ =` 是死代码 |
| #140 | JSON unknown field 用字符串匹配 + guard test | ✅ 已修 | Go 标准库 `encoding/json` 至 1.25 仍用文本错误，无 typed error |
