# GoCell Backlog

> 只含待办事项。已完成项归档至 `docs/reviews/archive/202604180035-backlog-pre-cleanup.md`。
> 更新日期: 2026-04-20（PR#203 同步）
> 基线: develop@dde5cae（PR#165 合并后）
> 最近合入概览: PR#175-200 分层重构 + auth F系列 + config-core PG pilot 全部完成；详见 git log
> 未合入外部 PR: PR#129 Sentinel DSN redaction / PR#130 Bolt journey catalog
> 标记说明:
> 🟡 可延后 = 不卡正确性或安全；latent risk / DX / 测试覆盖 / 纯 tech debt / 供应链加固 / 架构打磨 — 可机会性纳入或 v1.0 后做
> 🟠 条件延后 = 有明确触发条件（如首次 prod migration / PG 接线），触发前可延

---

## P0 阻塞项（~2h）

| # | 任务 | 工时 | 文件 | 来源 |
|---|------|------|------|------|
| L1 | **AUDIT-ROUTE-POLICY-01** (Cx2): `cells/audit-core/cell.go:241` 裸挂 `http.HandlerFunc(c.queryHandler.HandleQuery)`，`auditQueryPolicy` 未经 `auth.Secured()` 包装，非 admin 用户可横向读取他人审计记录。**修复**：`handler.go` 新增 `RegisterRoutes(mux cell.RouteMux)`，内部调用 `auth.Secured(h.HandleQuery, auditQueryPolicy)`；`cell.go` 改为 `c.queryHandler.RegisterRoutes(sub)`；补 401/403/200/admin 跨用户四条测试。 | 2h | `cells/audit-core/cell.go` + `cells/audit-core/slices/auditquery/handler.go` + `handler_test.go` | 2026-04-20 分层审查 |

---

## P1 待办

| # | 任务 | 工时 | 文件 | 来源 |
|---|------|------|------|------|
| P1-4 | **OUTPUT-JSON-SARIF-01** (Cx3, 🟡 可延后): `gocell validate` 缺机器可读输出通道（JSON/SARIF）。统一诊断模型（单一 `Issue` struct → 多 printer 映射）。对标 golangci-lint / staticcheck / ESLint / kubectl print flags | 6h | `cmd/gocell/` + `kernel/governance/` 序列化 | PR#152 round-2 review |
| P1-5 | **METADATA-PERF-BENCH-01** (Cx3, 🟡 可延后): `BenchmarkParseFS_500Files` 性能基准 + goccy/go-yaml 单次解码迁移成本评估 | 4h | `kernel/metadata/parser_test.go` | PR#152 seat-4 |
| P1-8 | **FEAT-1 DEVICE-LIST-API**: 新建 `device-list` slice + `GET /api/v1/devices` 分页 + contract + contract_test；同步触发 CONTRACT-LIST-LINT-01 规则 | 3h | `cells/device-cell/slices/device-list/` + `contracts/http/device/list/v1/` | backend_issues.md #1 |
| L2 | **ROUTE-POLICY-REGISTRY-01** (Cx3, 前置 L1): 无法在编译/启动期发现"路由注册但无策略"，L1 类问题可再次出现。**修复**：`runtime/http/router/policy_registry.go` 新增 `PolicyRegistry`（`RecordSecured(method, path)` + `Verify(registeredRoutes, whitelist) error`）；`runtime/bootstrap/bootstrap.go` 启动期调用 `Verify`，发现裸路由直接 `return error`。白名单格式复用 L3 统一后的 `parseEndpointRule`。 | 5h | `runtime/http/router/policy_registry.go`（新）+ `runtime/bootstrap/bootstrap.go` | 2026-04-20 分层审查 |
| L4 | **ID-VALIDATION-SINGLE-SOURCE-01** (Cx2): `runtime/http/middleware/request_id.go` 有 `maxRequestIDLen=128` + `isSafeID()` 校验；`kernel/outbox/observability_metadata.go` 恢复路径无长度/字符集校验，两条路径规则不一致。**修复**：新增 `pkg/idutil/id.go`（`MaxIDLen=128`, `IsSafeID(s string) bool`）；`runtime/http/middleware/request_id.go` + `kernel/outbox/observability_metadata.go` 均改为引用 `pkg/idutil`，单一事实源。 | 2h | `pkg/idutil/id.go`（新）+ `runtime/http/middleware/request_id.go` + `kernel/outbox/observability_metadata.go` | 2026-04-20 分层审查 |
| L5 | **L2-CONSTRAINT-ERROR-SEMANTIC-01** (Cx1): `cells/config-core/slices/flagwrite/service.go:77` L2 依赖约束违反时调用 `panic(...)`，与 `audit-core/cell.go:144`（`return errcode.New(...)`）语义分裂。**修复**：将 `panic` 改为 `return nil, errcode.New(errcode.ErrCellMissingOutbox, "...")`；补构造期错误测试。 | 1h | `cells/config-core/slices/flagwrite/service.go` + `service_test.go` | 2026-04-20 分层审查 |
| L6 | **CONTRACTTEST-MODEL-ALIGN-01** (Cx3): `pkg/contracttest` 自定义 schema 解析结构，与 `kernel/metadata` 元模型不一致，`schemaRefs.Extra` 等扩展键在 contracttest 路径静默丢失。`pkg/` 不可依赖 `kernel/`，需共享类型落在 `pkg/`。**修复**：新增 `pkg/contracts/schema_types.go`（共享 schema 结构体）；`pkg/contracttest/contracttest.go` 引用共享类型；`kernel/metadata/` 中对应本地定义替换为 `pkg/contracts` 引用；补解析一致性测试。 | 3h | `pkg/contracts/schema_types.go`（新）+ `pkg/contracttest/contracttest.go` + `kernel/metadata/` | 2026-04-20 分层审查 |
| L7 | **EXAMPLES-STARTUP-SMOKE-01** (Cx2): `examples/sso-bff/main.go` 的 audit cursor key `"sso-bff-audit-cursor-key-32b!!"` 仅 30 字节，config cursor key 31 字节，`examples/todo-order/main.go` 同类 31 字节；`pkg/query.NewCursorCodec` 要求 ≥ 32 字节（注释声明 HMAC-SHA256 block size），**examples 实际 `os.Exit(1)` 启动失败**。CI 只 `go build`，字符串长度是运行时 error，lint 不查，bug 潜伏至今。**根因**：(a) examples 不在治理护栏（`gocell validate` 不管 main.go）；(b) 无 smoke test 跑 example binary；(c) 字符串 label `-32b` / `-32bytes` 与实际字节数错位，肉眼数不准。**修复**：(1) cursor key 改用可计算/常量保证长度，如 `[]byte(fmt.Sprintf("%-32s", "sso-bff-audit"))` 或 `bytes.Repeat`；(2) CI 增 `examples-smoke` job：`timeout 5s go run ./examples/sso-bff` 期望正常启动再 ctx cancel 退出；(3) 治理扩展：`cmd/gocell/check_examples.go` 扫描 `examples/*/main.go` 静态检查 `NewCursorCodec([]byte("..."))` 字面量长度。 | 2h | `examples/sso-bff/main.go` + `examples/todo-order/main.go` + `.github/workflows/ci.yml` + `cmd/gocell/` | 2026-04-20 PR#204 旁路发现 |

---

## P2 待办

### kernel / runtime

| # | 任务 | 工时 | 文件 | 来源 |
|---|------|------|------|------|
| K1 | **METADATA-PROJECTLOC-IFACE-01** (Cx3, 🟡 可延后): 提取 `ProjectLocator interface { Locate(file, path string) Position }` 隐藏 yaml.v3 AST；`ProjectMeta.FileNodes` 不再泄漏 | 3h | `kernel/metadata/` + `kernel/governance/` + `cmd/gocell/` | PR#152 seat-1 |
| K2 | **OBS-RELAY-REGISTER-ATOMIC-01** (Cx3, 🟡 可延后): `outbox.NewProviderRelayCollector` 5 个 metric 原子注册，需 `Provider.Unregister` 支持或文档化契约 | 2h | `kernel/outbox/` + `kernel/observability/metrics/` | PR#157 review S3-05 |
| ~~R1~~ | ~~**BOOTSTRAP-RUN-COGNIT-01**~~ ✅ PR#200/#202/#203（R1a ManagedResource+Worker→kernel + R1b KeyProvider→kernel/crypto + R1d CellModule+BuildApp 三步完成）: ~~`bootstrap.go::Run()` 认知复杂度 225（`//nolint:gocognit` 抑制），拆 `validateOptions()` / `buildRouter()` / `startServers()` 三段式~~ | ~~4h~~ | ~~`runtime/bootstrap/bootstrap.go`~~ | PR#163 agent 报告 |
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
| A13 | **VAULT-TOKEN-RENEW-01** (**P1**, Cx2): 生产长运行时 Vault token 过期导致 provider 失效；PR#204 恢复 `VAULT_TOKEN` 构造期 fail-fast 后，对 token 生命周期敏感度提升。方案：`NewTransitKeyProviderFromEnv` 启动时创建 `vaultapi.NewLifetimeWatcher`，后台 goroutine 持续续期 token，续期失败映射 transient → EventBus Requeue；暴露 `vault_token_renew_success_total` / `vault_token_renew_failure_total` 观测指标。排期：下两个 Sprint 前落地 | 2h | `adapters/vault/transit_provider.go` | R1c (PR #204) 追加审查 P1 |
| A19 | **VAULT-READINESS-HEALTH-01** (P2, Cx3, 🟡 可延后): `NewTransitKeyProviderFromEnv` 启动自检把 403（auth 失败）/ 404（mount 或 key 不存在）/ 5xx（网络）统一折成 "key not found"，运行期无 readiness 探测，Vault 不可用只能靠业务调用失败被动暴露。方案：启动 self-check 按错误分类返回不同 errcode（`ErrVaultAuthFailed` / `ErrVaultKeyNotFound` / `ErrKeyProviderTransient`）；`TransitKeyProvider` 接入 `kernel/lifecycle.ManagedResource` 或 `runtime/http/health`，暴露 ready/not-ready；integration test 覆盖 token-revoked / mount-missing / key-missing 三种场景分别返回 non-ready | 4h | `adapters/vault/transit_provider.go` + `runtime/http/health/` | R1c (PR #204) 追加审查 P2 |
| A20 | **VAULT-KEYID-CONTRACT-STRONG-01** (P2, Cx3, 🟡 可延后): PR#204 已在 Decrypt 入口加"最小校验"（`keyID` 与 `edk` 前缀 `vault:v{N}:...` 的版本号比对）；跟进**契约文档级强化**：`kernel/crypto/key_provider.go` godoc 声明 `keyID` 为"可验证元数据"（不是弱观测标签）；新增 `kernel/crypto/verifykeyid.go` 通用 helper（版本号解析 + 比对），LocalAES / Vault 两实现统一切到 helper；table-driven test 覆盖伪造 / 漂移 keyID 被拒场景，未来 KMS provider 漏校验必 fail | 3h | `kernel/crypto/key_provider.go` + `kernel/crypto/verifykeyid.go`（新）+ `adapters/vault/transit_provider.go` + `runtime/crypto/local_aes_provider.go` | R1c (PR #204) 追加审查 P1 最小修复已合，契约强化跟进 |
| A14 | **VAULT-AUTH-PLUGGABLE-01** (P2, 🟡 可延后): 当前硬编码 VAULT_TOKEN，生产需 AppRole / Kubernetes auth / JWT auth。方案：构造函数加 `api.AuthMethod` 参数；env 读取模式保留 token 作 dev fallback | 3h | `adapters/vault/transit_provider.go` | R1c (PR #204)，plan U2 `/Users/shengming/.claude/plans/r1c-agents-explorer-3-tdd-sequential-torvalds.md` |
| A15 | **VAULT-NAMESPACE-MULTITENANT-01** (P2, 🟡 可延后): HCP Vault / Enterprise 多租户场景，当前无 namespace 支持，低优先级。方案：`client.SetNamespace(ns)` + 环境变量 `GOCELL_VAULT_NAMESPACE` | 1h | `adapters/vault/transit_provider.go` | R1c (PR #204)，plan U3 `/Users/shengming/.claude/plans/r1c-agents-explorer-3-tdd-sequential-torvalds.md` |
| A16 | **VAULT-DATAKEY-ENDPOINT-01** (P2, 🟡 可延后, 🟠 条件延后：S14a 或 S3 对象加密需求触发): `datakey/plaintext` endpoint 支持，大 blob 场景才需要（当前小 config 值场景本地 DEK + encrypt(DEK) 已最优）。关联：S14a / 未来 S3 加密需求 | 2h | `adapters/vault/transit_provider.go` | R1c (PR #204)，plan U4 `/Users/shengming/.claude/plans/r1c-agents-explorer-3-tdd-sequential-torvalds.md` |
| A17 | **VAULT-AEAD-UTIL-EXTRACT-01** (P2, Cx1, 🟡 可延后): `adapters/vault/aead.go` 与 `runtime/crypto/local_aes_provider.go` 的 AES-GCM helpers 存在双拷贝。方案：新建 `pkg/aeadutil/gcm.go` 含 `EncryptSplit` / `Decrypt` 两个纯函数，两端 import | 1h | `pkg/aeadutil/gcm.go`（新）+ `adapters/vault/aead.go` + `runtime/crypto/local_aes_provider.go` | R1c (PR #204)，reviewer FID-008 |
| A18 | **VAULT-ROTATE-OPTIMISTIC-LOCK-01** (P2, Cx2, 🟡 可延后): `TransitKeyProvider.Rotate` 当前持写锁期间执行 2 次 Vault HTTP 调用，阻塞并发读。方案：无锁执行 rotate + readLatestVersion，完成后 Lock 仅更新 version cache；需补充并发测试 | 2h | `adapters/vault/transit_provider.go` | R1c (PR #204)，reviewer FID-011 |
| A21 | **HEALTH-CHECKER-CTX-BUDGET-01** (P2, Cx3, 🟡 可延后): `runtime/http/health.Checker` 签名为 `func() error`，聚合层 (`Handler.ReadyzHandler`) 顺序执行所有 checker 且无统一超时/并行策略。`TransitKeyProvider.Checkers["vault_transit_ready"]` 内部自控 3s context，但其他 checker（PGResource、broker health）预算各自为政；未来 checker 退化或新增时，`/readyz` 尾延迟会叠加放大。**方案**：将 `Checker` 升级为 `func(ctx.Context) error`，`ReadyzHandler` 为整个聚合注入统一 deadline（如 2s）并考虑并发执行；失败时日志/响应输出每个 checker 的耗时与错误名称，用于运维定位。前置：需要 `kernel/lifecycle.ManagedResource.Checkers()` 签名同步升级。排期建议 R2 之后独立 PR 做。 | 3h | `runtime/http/health/health.go` + `kernel/lifecycle/managed_resource.go` + 所有 `Checkers()` 实现 | PR #205 review 2026-04-20 P2 finding |

### slice / cell 收口

| # | 任务 | 工时 | 文件 | 来源 |
|---|------|------|------|------|
| S2-follow | **CONTRACT-ERROR-SCHEMA-EXTEND-01** (P2, Cx1, 🟡 可延后): 其余 HTTP contract (config/write、config/get、auth/login 等) 补充 401/403 responses 声明，使错误响应 schema 覆盖全库所有受保护端点 | 2h | `contracts/http/**/{publish,get,write,flags}/v*/contract.yaml` | PR-CONFIG-POLISH 后续 |
| S4 | **EVENT-PAYLOAD-TYPED-01** (Cx2): sessionlogin/sessionlogout/configwrite/configpublish/auditappend/auditverify 事件 payload `map[string]any` → typed event struct | 3h | 6 个 `service.go` + event contract schemas | PR#133 re-review |
| S5 | **RBAC-REVOKE-POST-01** (🟡 可延后): `DELETE /internal/v1/access/roles/revoke` 改为 `POST` 避免 DELETE body 代理兼容问题 | 1h | `cells/access-core/slices/rbacassign/handler.go` + `contracts/http/auth/role/revoke/v1/contract.yaml` | PR#143 review 6.2 |
| S6 | **RBAC-LAST-ADMIN-GUARD**: `service.Revoke` 检查剩余 admin 数量；`ports.RoleRepository` 新增 `CountByRole` | 1h | `cells/access-core/slices/rbacassign/service.go` + `ports/` | PR#143 review 2.3 |
| S7 | **VALIDATE-EVIDENCE-CI-01** (Cx2, 根治声明-代码漂移): CI 新增独立 `metadata-check` job（`gocell validate` + `check contract-health`），失败阻断 PR | 1h | `.github/workflows/ci.yml` + PR template | PR#155 review F7 |
| S10 | **MODE-SEMANTIC-SPLIT-01** (Cx2, 🟡 可延后): 读路径 `query.RunMode`（cursor 容错）与写路径 `configpublish.WithRunMode`（publisher fail-open）当前共用同一枚举，为后续任一方向演进埋下耦合。保留"Init 单点翻译"前提，新增写路径独立类型（如 `configpublish.PublishFailureMode` with `FailClosed`/`FailOpen`），Cell Init 并行映射 `DurabilityMode → (RunMode, PublishFailureMode)` 后注入。触发条件：任一方向需要新增非二元模式值时。对标 Uber fx Provide/Decorate — 每个决策独立类型注入。 | 3h | `pkg/query/runmode.go` + `cells/config-core/slices/configpublish/service.go` + 4 处 `cell.go` Init | PR#167 round-2 review（finding 3 改进项，发现时建议暂缓） |
| S11 | **CONFIG-CORE-INIT-COGNIT-01** (Cx3, 🟡 可延后): `cell.go::Init()` 认知复杂度 19（`//nolint:gocognit` 临时抑制）；拆 `validateDeps()` / `buildCursorCodec()` / per-slice builder 三段式，降至 ≈9。来源：PR#168 /fix 诊断 | 3h | `cells/config-core/cell.go` | PR#168 发现（nolint 临时静音） |
| S13-follow | **4XX-LOG-SAMPLING-01** (P3, Cx1, 🟡 可延后): 高频端点（如 GET config）4xx 日志可加采样（1%）或在 rate-limiter 触发后限速，避免大量 429 日志淹没告警通道。注：PR#181 F3 已把 log4xx 字段最小化（移除 client message，仅保留 code/status/correlation IDs/internal），采样必要性已大幅下降；仅在真实运维告警通道过载时再做 | 1h | `pkg/httputil/response.go` | PR-CONFIG-POLISH 后续 |
| S14a | **CONFIG-VALUE-KMS-AWS-PROVIDER-01** 🟠（明确部署目标云平台后触发）：基于 KeyProvider 接口加 AWS-KMS / GCP-KMS adapter；envelope encryption（DEK 本地 + KEK 在 KMS）；rotation 走 KMS API；dev fallback LocalAES。**前置 S14 CONFIG-VALUE-ENCRYPTION-01 ✅ PR#195 已完成（LocalAES + VaultTransit）**。**触发**：明确生产云平台 + 通过 KMS 安全评审 | 6h | 🟠 | `runtime/crypto/aws_kms_provider.go`（新）+ `gcp_kms_provider.go`（新）+ `cmd/core-bundle/bundle.go` env 解析 | PR-CC-VALUE-ENCRYPT 后续 |
| S15 | **ERROR-CTX-CANCELLED-CLASSIFY** (P3, Cx2, 🟡 可延后): `ctx.Canceled` 在 config_repo 及其他 PG 路径未单独归类为 `ErrContextCanceled`，当前统一为 `ErrConfigRepoQuery`；reviewer 可接受当前状态，但长期会影响 caller 区分超时 vs 业务错误。来源: PR#169 review F-T-3 | 1h | `cells/config-core/internal/adapters/postgres/config_repo.go` | PR#169 review F-T-3 |
| S18 | **JWT-AUDIENCE-ENV-VAR-01** (P1, Cx2, 🟠 条件延后，多环境部署前触发): `jwtAudience` 为编译期常量，多环境（staging/prod）无法通过 env var 区分（如 `gocell-staging` / `gocell-prod`）；同时 sessionlogin/sessionrefresh 内 audience 需同步注入，否则 issuer 与 verifier drift 风险仍在。**修法**：新增 `GOCELL_JWT_AUDIENCE` env var；`adapterMode=real` 时强制要求；access-core cell 新增 `WithTokenAudience(string)` option 让 Init 注入而非硬编码。**前置**：与 F-O-1/F-A-1 配套，需独立 ADR。来源: PR#170 review F-O-1 + F-A-1 | 3h | `cmd/core-bundle/main.go` + `cells/access-core/cell.go` + `cells/access-core/slices/sessionlogin/service.go` + `sessionrefresh/service.go` | PR#170 review |
| S19 | **JWT-AUDIENCE-DRIFT-INTEG-TEST-01** (P2, Cx1, 🟡 可延后): `cmd/core-bundle/jwt_aud_integration_test.go` 直接调 `deps.issuer.Issue(…, []string{jwtAudience}, …)` 而非真实 sessionlogin 路径，无法检测 sessionlogin/sessionrefresh 硬编码与 `DefaultJWTAudience` 的 drift。**修法**：集成测试调用 sessionlogin.Service.Login → 解析响应 token → VerifyIntent，使 drift 编译失败而非运行时静默放行。**前置**：S18（access-core WithTokenAudience 注入）落地后更易实现。来源: PR#170 review F-T-3 | 2h | `cmd/core-bundle/` + `cells/access-core/slices/sessionlogin/` | PR#170 review F-T-3 |
| S20 | **JWT-AUDIENCE-STARTUP-LOG-01** (P3, Cx1, 🟡 可延后): `buildJWTDeps` 构建时未打印 effective audience 值；ops 无法通过启动日志或 `/readyz?verbose` 确认运行时 audience 配置。**修法**：`slog.Info("JWT audience enforcement enabled", slog.String("audience", jwtAudience))` 紧接 verifier 构造后添加。来源: PR#170 review F-S-5 | 0.5h | `cmd/core-bundle/main.go` | PR#170 review F-S-5 |
| S21 | **JWT-AUD-TEST-TABLE-DRIVEN-01** (P3, Cx1, 🟡 可延后): `runtime/auth/jwt_aud_test.go` 9 个场景为独立函数，违反 CLAUDE.md table-driven 规范。**修法**：重构为 `struct { name; expectedAuds []string; tokenAud []string; wantErr bool; errContains string }` 结构。来源: PR#170 review F-T-5 | 1h | `runtime/auth/jwt_aud_test.go` | PR#170 review F-T-5 |
| S22 | **REFRESH-AUD-REAL-ROUTE-TEST-01** (P2, Cx2, 🟡 可延后): `runtime/auth/middleware_aud_test.go` 的 refresh-path audience 回归测试手造 fake handler 直接调 `VerifyIntent`，不经过真实 route registration / body binding / `Service.Refresh`，无法检测 public-route bypass 或 body decode 回归。**修法**：在 `cells/access-core/auth_integration_test.go` 补一条真实 HTTP 测试：POST `{"refreshToken": wrong-aud-token}` 到 `/api/v1/access/sessions/refresh`，断言返回 401；再补 missing-aud token 场景。**前置**：可独立于 S18 实施。来源: PR#171 外部审查 F2 | 2h | `cells/access-core/auth_integration_test.go` + `runtime/auth/middleware_aud_test.go` | PR#171 外部审查 |
| S23 | **AUTH-WALKTHROUGH-COMPOSE-01** (P2, Cx3, 🟡 可延后): `examples/sso-bff/walkthrough_test.go` 手装精简 server（无 bootstrap lifecycle），与 `main.go` 的真实组装路径、README 步骤三份语义各自独立，可掩盖 public-endpoint wiring / config-core 接线 / audit event delivery 的回归。**修法**：提取 `NewSSOBFFApp(opts...)` 组装函数被 `main.go` 和 walkthrough test 共用；test server 走同一 bootstrap + Start/Stop 路径。来源: PR#172 review F3（OUT_OF_SCOPE） | 4h | `examples/sso-bff/bootstrap.go`（新）+ `main.go` + `walkthrough_test.go` | PR#172 review F3 |
| S24 | **AUTH-MIDDLEWARE-AUD-REFRESH-E2E-01** (P3, Cx2, 🟡 可延后): `TestAuthMiddleware_WrongAudience_RefreshPath_Returns401` 直接调 `verifier.VerifyIntent()`，未经过 `AuthMiddleware` 真实中间件链（parse req → call verifier → write error）。本质是在测 verifier 行为而非 middleware 集成。生产 refresh 路径确实也是 `VerifyIntent` 直调，无实际安全风险；但若未来 middleware 有 early-return/short-circuit 改动，该测试无法检测 regression。**修法**：用 `httptest.NewServer` + 真实 `AuthMiddleware` + `makeTokenWithAud` 覆盖 refresh-path wrong-audience 场景，使测试打到完整链路。来源: PR#293 round-2 /fix 分析 | 1h | `runtime/auth/middleware_aud_test.go` | PR#293 round-2 |
| L7 | **FMT15-NEXTCURSOR-ENFORCE-01** (Cx1, 🟡 可延后): 列表响应治理规则（FMT-14）只强制 `hasMore`，未强制 `nextCursor`，导致接口形态漂移（部分 slice 返回 `nextCursor` 为空字符串而非 omitempty）。**修复**：`kernel/governance/rules_fmt.go` 新增 FMT-15：响应含 `hasMore` 时必须同时含 `nextCursor`（可为空但字段必须存在）；补对应 validate 测试。 | 2h | `kernel/governance/rules_fmt.go` + 对应 validate 测试 | 2026-04-20 分层审查 |
| L8 | **PAGINATION-HELPER-EXTRACT-01** (Cx2, 🟡 可延后): `ParsePageRequest` + `slog.Warn` + `WriteDomainError` 三行分页错误处理模式在 auditquery、configwrite、flagwrite 等多处 slice handler 重复，观测语义分散。**修复**：`pkg/httputil/pagination.go` 提取公共 helper；各 slice handler 统一引用，消除重复。 | 2h | `pkg/httputil/pagination.go`（新）+ 各 `cells/*/slices/*/handler.go` | 2026-04-20 分层审查 |
| L9 | **EXAMPLES-CONTEXT-NOOP-01** (Cx1, 🟡 可延后): `examples/` 下自定义 `noopTxRunner` 吞掉上游 context，破坏取消传播语义；且与 `persistence.NoopTxRunner` 形成分叉。**修复**：删除 examples 层自定义实现，改用 `persistence.NoopTxRunner`（或等价基础实现），恢复 context 传播。 | 1h | `examples/*/` 相关文件 | 2026-04-20 分层审查 |

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
| X11 | **REFRESH-HMAC-SPLIT-01** (Cx3, 🟠 条件延后，**X15 之前必须完成**): HMAC-split token 格式 (selector\|verifier)，DB 存 selector 明文 + SHA-256(verifier)。需 migration ALTER + Store 接口变更。防 DB 泄漏后 token 被直接使用。**必须在 X15 上线前完成**——明文 token 入库后改格式需数据迁移 | 4h | `runtime/auth/refresh/` + `adapters/postgres/refresh_store.go` + migration | 开源对标 Hydra 双层防护 |
| X12 | **REFRESH-IDLE-EXPIRE-01** (Cx2, 🟠 条件延后，X15 集成后): `idle_expires_at` 滑动窗口列 + Policy.MaxIdle。长时间未使用的 token 提前过期 | 3h | `runtime/auth/refresh/types.go` + `adapters/postgres/` + migration | 开源对标 Zitadel 双过期列 |
| X13 | **REFRESH-PARTITION-01** (Cx2, 🟠 条件延后，生产流量达阈值后): `expires_at` range 分区，用 DROP PARTITION 替代批量 DELETE 进行 GC | 3h | migration + DBA ops runbook | 通用 PG 高吞吐模式 |
| X14 | **REFRESH-GRACE-COUNTER-01** (Cx2, 🟠 条件延后，X15 集成后): `first_used_at` + `used_times` 列，grace 窗口内限制重用次数上限 | 2h | `adapters/postgres/refresh_store.go` + migration | 开源对标 Hydra COALESCE 模式 |
| X15 | **REFRESH-OPAQUE-INTEGRATION-01** (Cx3, 🟠 条件延后，**X11 完成后**): sessionrefresh/sessionlogin 切换 opaque token + 接线 access_module.go。替换当前 JWT refresh token 为 opaque + refresh.Store。前置: X11 HMAC-split | 6h | `cells/access-core/slices/sessionrefresh/` + `cmd/core-bundle/access_module.go` | F2 plan 后续集成 |

---

## 设计决策记录（历史 — 不修，避免重复审查）

| PR | Finding | 结论 | 理由 |
|---|---------|------|------|
| #137 | assembly 层不校验零值 + 非法枚举旁路 | ✅ 已修 | assembly.startInternal 加 ValidateMode 入口闸门 + CheckNotNoop 改为 allowlist |
| #137 | core-bundle DurabilityDurable + in-memory | 不修 | 语义正确：Durable 拒绝 Nooper 标记类型，nil 和 `eventbus.New()` 合法通过；effectiveMode + adapterInfo + slog 日志已透明标注 |
| #137 | durability_test 非 table-driven | 不修 | 8 个测试断言模式差异大，table-driven 增加复杂度不增加覆盖率 |
| #137 | SonarCloud 5.6% duplication | 不修 | cell-per-package 固有结构相似，5 cell 的 CheckNotNoop 参数列表各不相同，不可提取 |
| #140 | Request/Trace ID 生成不做 `rand.Read` 错误分支 | ✅ 已修 | Go 1.24+ `crypto/rand.Read` always-succeed-or-fatal，`_, _ =` 是死代码 |
| #140 | JSON unknown field 用字符串匹配 + guard test | ✅ 已修 | Go 标准库 `encoding/json` 至 1.25 仍用文本错误，无 typed error |
| #143 | Seed admin password 通过环境变量传入 | 不修 | dev 模式标准做法（Casdoor/Zitadel 同模式）；生产改用 secrets manager 在 PG-DOMAIN-REPO 时处理 |
| #143 | Slice 目录命名 kebab vs no-dash | 不修 | CLAUDE.md "Cell 开发规则"约定 |
| #143 | `TestContext` 从非 `_test.go` 文件导出 | 不修 | 跨包测试需要，`_test.go` 函数 package-scoped 无法跨包调用 |
| #143 | POST /assign 返回 200 而非 201 | 不修 | 幂等操作返回 200（Casbin 模式），201 暗示每次创建新资源 |
| #146 | computeBoundaryContracts 遍历全量 contracts | 不修 | generator 必须遍历全量才能发现 imported contracts；FMT-09 validate 阶段已保证合法，generator fail-fast 是正确的 defense-in-depth |
| #155 | `slices/*/slice.yaml` allowedFiles 双路径（kebab + no-dash） | 不修 | 全项目统一惯例，FMT-14 治理规则守护，`gocell scaffold slice` 模板默认产出双路径 |
| #159 | 7 个 Option type 分散 | 不修 | 每个 Option 针对不同构造函数，合并会丧失类型安全性；对标 Kratos jwt.Option 每个中间件独立 |
| #159 | GenerateServiceToken `crypto/rand` 失败静默返回 "" | 不修 | Go 1.24+ `crypto/rand.Read` always-succeed-or-fatal，空串触发下游 401 是 fail-closed |
| #159 | ServiceTokenMiddleware nil ring 每请求 500 | 不修 | 返回 error 违背 Go 标准 middleware 签名惯例（net/http / go-chi / Kratos 均不返回 error）；构造时 panic 不符合编码规范 |
| #159 | 缺 service token duration metric | 不修 | HMAC-SHA256 <0.1ms，histogram 分桶无意义信息；等 Redis nonce store 引入网络 I/O 后再加 |

---

## 触发条件项（仅在条件满足时做）

> 已触发并完成项：T8 PUBLIC-ENDPOINT-STRUCT-MIGRATE-01 ✅ PR#201（已在 PR#201 触发并完成）

| # | 任务 | 工时 | 触发条件 |
|---|------|------|----------|
| T1 | **AUTH-PROVIDER-EXPORT-01** `authProvider` 接口 unexported，需移动出 `runtime/bootstrap` | 1h | 第二个 auth provider cell |
| T2 | **AUTH-ISSUE-OPTIONS-01** `JWTIssuer.Issue()` 重构为 `IssueOptions` struct | 1h | Issue() 第 5 个参数 |
| T3 | **DEVICE-ENQUEUE-RBAC** HandleEnqueue 无设备维度鉴权 | 2h | 多租户 operator |
| T4 | **CB-RESILIENCE-PACKAGE-01** 把 `Allower` / `CircuitBreakerRetryAfter` 从 `runtime/http/middleware` 迁移到 `runtime/resilience/circuitbreaker/` 独立包 | 4h | 出现第二个非 HTTP 的 CB 消费方 |
| T5 | **AUTH-SIGNER-01** `SigningKeyProvider` 返回 `crypto.Signer` 替代 `*rsa.PrivateKey` | 2h | golang-jwt v6 发布 |

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
| P0 阻塞 | ~2h |
| P1 | ~18h（P1-4 6h + P1-5 4h + P1-8 3h + L2 5h + L4 2h + L5 1h + L6 3h）|
| P2 kernel/runtime | ~16h |
| P2 adapter | ~9h |
| P2 slice/cell | ~30h |
| P2 发布 + 文档 | ~25h + v1.0 tag |
| **核心路径合计（不含 P3）** | **~100h（约 12-13 工作日）** |
| P3 长期 | 3-5d + 若干独立项 |
