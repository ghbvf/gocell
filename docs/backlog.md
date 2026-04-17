# GoCell Backlog

> 只含待办事项。已完成项归档至 `docs/reviews/archive/202604180035-backlog-pre-cleanup.md`。
> 更新日期: 2026-04-18
> 基线: develop@dde5cae（PR#165 合并后）
> 最近合入概览: Wave 1 ✅ 全部完成 / Post-Wave 1 PR#141-165 按层偿债 + 外部审查回灌
> 未合入外部 PR: PR#129 Sentinel DSN redaction / PR#130 Bolt journey catalog
> 标记说明:
> 🟡 可延后 = 不卡正确性或安全；latent risk / DX / 测试覆盖 / 纯 tech debt / 供应链加固 / 架构打磨 — 可机会性纳入或 v1.0 后做
> 🟠 条件延后 = 有明确触发条件（如首次 prod migration / PG 接线），触发前可延

---

## P0 阻塞项（2026-04-18 外部审查 + PR#157 post-merge，~11h）

| # | 任务 | 工时 | 文件 | 来源 |
|---|------|------|------|------|
| P0-1 | **AUTH-TOKEN-INTENT-01** (Cx3, 阻塞): login/refresh 共用 `JWTIssuer.Issue`，token 无 audience/purpose claim；全局 Bearer 中间件只做 `verifier.Verify`，session-validate 只看 `sid`，不区分用途 → **refresh token 可直接访问业务接口**。**修复**: Issue() 增 `TokenIntent`（"access"/"refresh"）映射到 JWT `aud`；verifier 按请求 scope 拒绝 intent 不匹配的 token；`/auth/refresh` 只接受 `intent=refresh`，其余路径只接受 `intent=access`。补 2 条集成测试。对标 K8s TokenRequest audience 绑定、go-micro access/refresh purpose claim | 5h | `runtime/auth/jwt.go` + `runtime/auth/middleware.go` + `cells/access-core/slices/{sessionlogin,sessionrefresh,sessionvalidate}/service.go` | 2026-04-18 外部审查 |
| P0-2 | **AUTHZ-WRITE-CONFIG-WRITE-01** (Cx2, 阻塞): configwrite create/update/delete 三端点无 `auth.RequireAnyRole(ctx, "admin")`，与 configpublish publish/rollback admin gate 不一致（同一资源域授权策略漂移）。**修复**: 复用 `roleAdmin` const（提取到 `cells/config-core/internal/dto/authz.go` 共享），三端点入口加 gate + 401/403/200 测试 | 1.5h | `cells/config-core/slices/configwrite/handler.go` + `cells/config-core/internal/dto/authz.go`（新建）| PR#157 post-merge + 2026-04-18 外部审查 re-confirm |

---

## P1 待办

| # | 任务 | 工时 | 文件 | 来源 |
|---|------|------|------|------|
| P1-1 | **AUTH-INT-REACHABILITY-01** (Cx2): `auth_integration_test.go` 只断言匿名→401、public→非401，未覆盖带合法 token 的 handler 到达性；路由丢失/方法错误/handler 500 不会被发现。**修复**: 补带合法 token 的到达性断言（200 + 响应体关键字段）+ public handler 精确状态/响应断言 | 1.5h | `cells/access-core/slices/*/auth_integration_test.go` | 2026-04-18 外部审查 |
| P1-2 | **CONFIG-DEMO-FAILOPEN-01** (Cx2, PR-X1 进行中): `configpublish/service.go:188-194` demo 模式 publisher 发布失败仅 `logger.Warn` 后 `return nil`，与 cell.yaml L2 声明不符。**修复**: durable 模式移除 fail-open；demo fail-open 仅保留在显式 `DiscardPublisher{}` 或 `Assembly.Mode == Demo` | 2h | `cells/config-core/slices/configpublish/service.go` | PR#157 post-merge |
| P1-3 | **PUBLIC-ENDPOINT-METHOD-MATCH-01** (Cx3, 🟡 可延后): 公共端点匹配为 path-only，不含 method 维度（latent risk）。升级为 `"METHOD /path"` 格式（向后兼容：无 method 前缀匹配所有 method）| 4h | `runtime/http/router/router.go` + `runtime/auth/middleware.go` + `runtime/bootstrap/bootstrap.go` + 调用方 | PR#158 six-seat review |
| P1-4 | **OUTPUT-JSON-SARIF-01** (Cx3, 🟡 可延后): `gocell validate` 缺机器可读输出通道（JSON/SARIF）。统一诊断模型（单一 `Issue` struct → 多 printer 映射）。对标 golangci-lint / staticcheck / ESLint / kubectl print flags | 6h | `cmd/gocell/` + `kernel/governance/` 序列化 | PR#152 round-2 review |
| P1-5 | **METADATA-PERF-BENCH-01** (Cx3, 🟡 可延后): `BenchmarkParseFS_500Files` 性能基准 + goccy/go-yaml 单次解码迁移成本评估 | 4h | `kernel/metadata/parser_test.go` | PR#152 seat-4 |
| P1-6 | **PR#160 PR-X2 pkg/query 稳定性** (Cx2): codec nil 构造期 fail-fast（Service 层）+ `ParsePageRequest` cursor 长度上限 + `PR#165 F1-2` `WithDemoFailOpen` 与 `query.RunMode` 语义整合 + `PR#165 F3-1` `loadCursorCodec` helper 单测 + `PR#165 F5-1` `RunModeForDemo` godoc "Do not extend" 警告 | 3h | `pkg/query/` + `cmd/core-bundle/` + `cells/*/slices/*/service.go` | PR#160 六席位 + PR#165 reviewer |
| P1-7 | **PR#160 PR-X3 cursor key rotation 接线** (Cx2, 🟡 可延后): `NewCursorCodec(current, previous)` 启动接线 + `GOCELL_CURSOR_PREVIOUS_SIGNING_KEY` 双 env + 轮换兼容回归。对标 K8s `--service-account-key-file`、gorilla/securecookie `CodecsFromPairs`。依赖 P1-6 | 4h | `pkg/query/codec.go` + `cmd/core-bundle/main.go` | PR#160 六席位 |
| P1-8 | **FEAT-1 DEVICE-LIST-API**: 新建 `device-list` slice + `GET /api/v1/devices` 分页 + contract + contract_test；同步触发 CONTRACT-LIST-LINT-01 规则 | 3h | `cells/device-cell/slices/device-list/` + `contracts/http/device/list/v1/` | backend_issues.md #1 |
| P1-9 | **FEAT-2 FLAG-WRITE-API**: `PUT /api/v1/config/flags/{key}` 写入端点 + contract + contract_test | 3h | `cells/config-core/slices/configwrite/` + `contracts/http/config/flags/write/v1/` | backend_issues.md #2 |
| P1-10 | **#5 AUTH-DX-01 README** + seed 用户 + sso-bff walkthrough。具体漂移: refresh curl `sessionId`→`refreshToken`；logout 204 空 body jq 失败；audit `.createdAt` 实为 `.Timestamp`。**前置**: 等 P0/P1 auth 面最终形态稳定 | 4h | `README.md` + `cells/access-core/internal/mem/` + `examples/sso-bff/README.md` | 6B + P4 review |

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
| A3 | **RL-INT-01** (🟡 可延后): Relay PG 集成测试 | 2h | `adapters/postgres/outbox_relay_test.go` | PR#46 review |
| A4 | **RL-MIG-01** (🟠 条件延后，首次 prod migration 前必做): `CREATE INDEX CONCURRENTLY` online-safe 索引 | 2h | `adapters/postgres/migrations/` | PR#46 review |
| A5 | **RL-SUB-01** (🟡 可延后): 入站 ID 校验（空/过长 message ID） | 1h | `adapters/rabbitmq/subscriber.go` | PR#46 review |
| A6 | **#31 RabbitMQ backoff + FailOpen enum 清理** (🟡 可延后) | 2h | `adapters/rabbitmq/` | Wave 2 残留 |
| A7 | **POOLSTATS-IFACE-01** (🟡 可延后): 三个 adapter PoolStats 公共接口（OTel collector 消费） | 1h | `adapters/postgres/pool.go` + `redis/client.go` + `rabbitmq/connection.go` | PR#134 review |
| A8 | **CI-DIGEST-01** (🟡 可延后): testcontainers 镜像 tag+digest 双固定 | 1h | `adapters/*/integration_test.go` | PR#139 review |
| A9 | **CI-LINT-PIN-01** (🟡 可延后): golangci-lint patch 级固定 + dependabot | 1h | `.github/workflows/ci.yml` | PR#139 review |
| A10 | **OBS-LGTM-INTEGRATION-01** (Cx3, 🟡 可延后): `//go:build integration` 夜间 OTel collector 真实 OTLP 协议兼容性测试 | 2h | `adapters/otel/integration_test.go` | PR#157 review S6-04 |

### slice / cell 收口

| # | 任务 | 工时 | 文件 | 来源 |
|---|------|------|------|------|
| S1 | **REPO-SCAN-CLASSIFY-01** (Cx2, 🟠 条件延后，PG-DOMAIN-REPO 接线前必做): `config_repo.go::GetByKey` / `GetVersion` 把所有 Scan 错误映射为 `ErrConfigRepoNotFound`；改用 `errors.Is(err, sql.ErrNoRows)` 判 not found，其他返回 `ErrInternal` 保留 `InternalMessage` | 2h | `cells/config-core/internal/adapters/postgres/config_repo.go` | PR#157 post-merge |
| S2 | **CONTRACT-ERROR-SCHEMA-01** (Cx1, 🟡 可延后): `publish/v1` + `rollback/v1` contract.yaml `responses` 新增 401/403 entries 引用共享错误 schema | 1h | `contracts/http/config/{publish,rollback}/v1/contract.yaml` | PR#157 post-merge |
| S3 | **DTO-NIL-SEMANTIC-01** (Cx2): 12+ handler 写成功响应前校验领域对象非 nil，避免 converter nil guard 把上游不变量异常"平滑"为空 data 成功响应 | 3h | 12+ `cells/*/slices/*/handler.go` | PR#158 six-seat review |
| S4 | **EVENT-PAYLOAD-TYPED-01** (Cx2): sessionlogin/sessionlogout/configwrite/configpublish/auditappend/auditverify 事件 payload `map[string]any` → typed event struct | 3h | 6 个 `service.go` + event contract schemas | PR#133 re-review |
| S5 | **RBAC-REVOKE-POST-01** (🟡 可延后): `DELETE /internal/v1/access/roles/revoke` 改为 `POST` 避免 DELETE body 代理兼容问题 | 1h | `cells/access-core/slices/rbacassign/handler.go` + `contracts/http/auth/role/revoke/v1/contract.yaml` | PR#143 review 6.2 |
| S6 | **RBAC-LAST-ADMIN-GUARD**: `service.Revoke` 检查剩余 admin 数量；`ports.RoleRepository` 新增 `CountByRole` | 1h | `cells/access-core/slices/rbacassign/service.go` + `ports/` | PR#143 review 2.3 |
| S7 | **VALIDATE-EVIDENCE-CI-01** (Cx2, 🟡 可延后): CI 新增独立 `metadata-check` job（`gocell validate` + `check contract-health`），失败阻断 PR | 1h | `.github/workflows/ci.yml` + PR template | PR#155 review F7 |
| S8 | **H1-7 RBAC-OUTBOX-MIGRATION**: `rbacassign.Service` "角色变更 → 会话失效"双写 → transactional outbox 原子写入 + consumer 异步失效 session。前置 outbox consumer 基础设施 | 6h | `cells/access-core/slices/rbacassign/service.go` + `cells/access-core/slices/sessionlogout/consumer.go`（新）+ contract event schemas | PR#149 review round 2 |
| S9 | **AUTH-LEGACY-TOKEN-STRICT-01** (Cx2, 🟡 可延后): PR#162 已删 2-part 分支；本项改为增 strict 模式开关 + 淘汰计划 + legacy 占比 metrics 看板。待产品确认迁移窗口 | 1h | `runtime/auth/servicetoken.go` | PR#159 外部审查 |

### 发布 + 文档

| # | 任务 | 工时 | 文件 | 来源 |
|---|------|------|------|------|
| F1 | **ADR-RUNMODE-TRANSLATION-01** (Cx1): 记录 `kernel/cell.DurabilityMode → pkg/query.RunMode` 分层翻译模式 — pkg 不 import kernel，cell 构造期翻译；对标 go-zero `ServiceConf.Mode` | 1h | `docs/architecture/` 新 ADR | PR#165 reviewer F1-1 |
| F2 | **SYSTEM-TOPOLOGY-API** (🟡 可延后): `GET /internal/v1/system/topology` 返回 cell/slice/contract 拓扑 JSON；基于 `kernel/registry` | 4h | 新 slice 或 `runtime/bootstrap/` | 历史 Batch 8 |
| F3 | **P2-T-02 audit e2e 测试**: Journey 级验收 | 2h | `journeys/` + integration test | 历史 Batch 8 |
| F4 | Review cells/ | 4h | — | Wave 4 |
| F5 | Review examples/ | 2h | — | Wave 4 |
| F6 | Review 报告汇总 | 2h | — | Wave 4 |
| F7 | 发布文档 | 4h | — | Wave 4 |
| F8 | 性能基准 | 4h | — | Wave 4 |
| F9 | **v1.0 tag** | — | — | Wave 4 |

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
| X7 | **AL-01 outbox_relay.go 轮询调度 → `runtime/outbox/relay.go`** | 2h | — | 依赖替换分析 |
| X8 | **AL-02 distlock.go 续期/TTL → `runtime/`** | 2h | — | 依赖替换分析 |
| X9 | **LINT-MODERN-01** (Cx2, P3): 全仓库 modernization baseline 清理（rangeint / stringsseq / forvar / inline / testingcontext / any / nhooyr.io→coder/websocket）。独立 PR，不混入功能 | 6h | — | PR#163 post-review |

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
