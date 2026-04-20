# GoCell 域驱动实施计划

> 生成日期: 2026-04-18 17:00
> 更新: 2026-04-20（已完成条目清理；PR#203 基准）
> 基准: develop@781f756（PR#203 合并后）
> 策略: 按**功能域**聚合，每域所有层（kernel/runtime/adapter/slice）改动一次性在一个 PR 串内闭合
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

**域间依赖**：无前置域，是其他域的安全基础。

| # | 任务 | 工时 | 优先级 | 文件 | 来源 |
|---|------|------|--------|------|------|
| X10 | **AUTH-REFRESH-OPAQUE-01** 🟠（PG-REPO 域上线后触发）：refresh token 改 opaque string + server-side rotation store（RFC 6819 §5.2.2.2）| 1-2d | 🟠 | `runtime/auth/` + `adapters/postgres/` | PR#166 R1-F2-7 |
| S18 | **JWT-AUDIENCE-ENV-VAR-01** (P1, Cx2, 🟠 条件延后，多环境部署前触发): `jwtAudience` 为编译期常量，多环境无法通过 env var 区分；`GOCELL_JWT_AUDIENCE` env var + `WithTokenAudience(string)` option；`real` 模式下非空强制；sessionlogin/sessionrefresh 同步注入。前置：独立 ADR | 3h | 🟠 | `cmd/core-bundle/main.go` + `cells/access-core/cell.go` + sessionlogin/sessionrefresh | PR#170 review F-O-1 |
| S19 | **JWT-AUDIENCE-DRIFT-INTEG-TEST-01** (P2, Cx1, 🟡 可延后，S18 落地后易实现): 集成测试调用 sessionlogin.Service.Login → 解析 token → VerifyIntent，检测 audience drift 编译失败 | 2h | 🟡 | `cmd/core-bundle/` + `cells/access-core/slices/sessionlogin/` | PR#170 review F-T-3 |
| S20 | **JWT-AUDIENCE-STARTUP-LOG-01** (P3, Cx1, 🟡 可延后): `buildJWTDeps` 构建时打印 effective audience | 0.5h | 🟡 | `cmd/core-bundle/main.go` | PR#170 review F-S-5 |
| S21 | **JWT-AUD-TEST-TABLE-DRIVEN-01** (P3, Cx1, 🟡 可延后): `runtime/auth/jwt_aud_test.go` 9 个场景重构为 table-driven | 1h | 🟡 | `runtime/auth/jwt_aud_test.go` | PR#170 review F-T-5 |
| S22 | **REFRESH-AUD-REAL-ROUTE-TEST-01** (P2, Cx2, 🟡 可延后): 补真实 HTTP 路由测试：POST `{"refreshToken": wrong-aud-token}` → `/api/v1/access/sessions/refresh` 断言 401 | 2h | 🟡 | `cells/access-core/auth_integration_test.go` | PR#171 外部审查 F2 |
| S31 | **JWT-ISSUER-STRICT-01** 🟡 (P2, Cx2)：`VerifyIntent` 缺 issuer 强约束；`WithExpectedIssuer` option + real 模式非空强制 + 负向测试。建议搭车 S18 | 1h | 🟡 | `runtime/auth/jwt.go` + `cmd/core-bundle/main.go` | 2026-04-18 六席审查 |
| S32 | **CONTROLPLANE-TOKEN-PROD-GATE-01** 🟠（real 模式部署前触发）：`real` 下断言 service-token/mTLS 至少一项 + CI real-mode smoke | 1h | 🟠 | `cmd/core-bundle/main.go` + `runtime/bootstrap/bootstrap.go` | 2026-04-18 六席审查 |
| S39 | **INITIALADMIN-IFACE-NAMING-01** (P3, Cx1, 🟡 可延后): `initialadmin/clock.go` 与 `scheduler.go` 单方法接口不符合 Go `-er` 命名约定 | 15min | 🟡 | `cells/access-core/internal/initialadmin/clock.go` + `scheduler.go` | SonarCloud 2026-04-18 |
| P1-17 | **REFRESH-JTI-UNIQUENESS-01** (P1, Cx2): refresh token 同秒并发可产出同值 token，reuse 检测被绕过。**修复**: Issue 引入 `jti: uuid` 唯一因子 | 3h | P1 | `runtime/auth/jwt.go` + `cells/access-core/slices/sessionrefresh/service.go` | 2026-04-18 Auth 域审查 |
| S41 | **MARSHAL-ERR-IGNORE-01** (P2, Cx1): access-core 事件发布路径 `json.Marshal` 错误被 `_ = ` 静默丢弃。**修复**: 显式处理 marshal 错误，带上下文返回调用方 | 1h | P2 | `cells/access-core/slices/*/service.go`（涉及事件发布 slice）| 2026-04-18 Auth 域审查 |
| S42 | **ROLELIST-CURSOR-01** (P2, Cx1): `GET /api/v1/access/roles` 缺 `nextCursor` 字段。**修复**: v1 增量补 `nextCursor`，同步更新 response.schema.json | 1h | P2 | `cells/access-core/slices/*/handler.go` + `contracts/http/auth/roles/list/v1/response.schema.json` | 2026-04-18 Auth 域审查 |

**域内 PR 拆分**：

| PR | 内容 | 工时 |
|----|------|------|
| PR-AUTH-ISSUER | S31 + S32：issuer 强约束 + controlplane 生产门禁（搭车 S18 PR）| 2h |
| PR-AUTH-JWT-AUDIENCE | S18：JWT audience env var + WithTokenAudience option（🟠 多环境部署前）| 3h |
| PR-AUTH-JWT-TEST | S19 + S20 + S21 + S22：audience drift 集成测试 + 启动日志 + table-driven（🟡）| 5.5h |
| PR-AUTH-OPAQUE | X10：refresh token 不透明化（🟠 PG-REPO 后触发）| 1-2d |
| PR-AUTH-SERVICE-HARDEN | P1-17 + S41：refresh token jti 唯一性 + marshal 错误显式处理 | 4h |
| PR-AUTH-ROLELIST-CURSOR | S42：role list nextCursor 补全（向后兼容）| 1h |

---

## 域 2：Config-core 域 ✅

**全部完成** PR#175（P1-2 fail-open 删除）+ PR#181（S2/S13：contract 401/403 + 4xx observability）+ commit 8552b6c（S11）；S10 ❌ 废弃。

---

## 域 3：PG 加固域 ✅

**全部完成** PR#173（A12 schema guard + A3 relay 集成测试 + A4 migration 006 + T7 config_versions index）。

---

## 域 4：Outbox/RabbitMQ 域

**目标**：补齐 broker health check、完善 subscriber 并发/退避策略，使 outbox 链路端到端可靠。

**域间依赖**：A10 已迁出至域 9。

| # | 任务 | 工时 | 优先级 | 文件 | 来源 |
|---|------|------|--------|------|------|
| A13 | **SUBSCRIBER-CONCURRENCY-DECISION-01** 🟠（PR#180 review 暴露）：`consumeLoop` 同步调用 `processDelivery`，PrefetchCount 并发语义退化为串行。需裁决：(a) 文档明确串行；或 (b) `go s.processDelivery(...)` 真并发并补并发安全测试 | 1-3h | 🟠 | `adapters/rabbitmq/subscriber.go` | PR#180 reviewer |
| A14 | **BACKOFF-DEDUP-01** 🟡：`exponentialDelay` 在 `adapters/rabbitmq/backoff.go` 与 `kernel/outbox/consumer_base.go` 重复实现。export `kernel/outbox.ExponentialDelay` 并删 adapters 副本 | 1-2h | 🟡 | `adapters/rabbitmq/backoff.go` + `kernel/outbox/consumer_base.go` | PR#180 reviewer |

**域内 PR 拆分**：

| PR | 内容 | 工时 |
|----|------|------|
| PR-OUTBOX-A13-CONCURRENCY | A13：subscriber 串行/并发裁决 | 1-3h |
| PR-OUTBOX-A14-DEDUP | A14：exponentialDelay 去重（ExponentialDelay 单一真源）| 1-2h |

> **注意**：BOOTSTRAP-WIRE-RMQ-BROKER-HEALTH-01 是条件延后项 — `cmd/core-bundle` 接入真实 RabbitMQ connection 时触发（2h，🟠）。

---

## 域 5：RBAC 域 ✅

**全部完成** PR#176（S8 transactional outbox）；S5/S6 早于计划落地。

---

## 域 6：HTTP/Router 域

**目标**：独立 internal listener 或 route-group 分流，完善路由层安全边界。

**域间依赖**：R4 改 `runtime/bootstrap/bootstrap.go`，与域 9 已完成的 R1 同文件，可独立进行。

| # | 任务 | 工时 | 优先级 | 文件 | 来源 |
|---|------|------|--------|------|------|
| R4 | **INTERNAL-LISTENER-01** 🟡 (Cx4)：`/internal/v1/` 独立 listener 或 service-token/mTLS 策略。扩展方案：`runtime/http/router/router.go` 引入 `RouteGroup`，每组独立 middleware chain（`/api/v1/*` JWT、`/internal/v1/*` ServiceToken、healthz 无认证）。影响 router 核心 + 所有 Cell 路由注册 API，500+ 行变更 | 4-8h | 🟡 | `runtime/http/router/router.go` + `runtime/bootstrap/bootstrap.go` + 全部 Cell 路由注册 | PR#143 review F1 |

**域内 PR 拆分**：

| PR | 内容 | 工时 |
|----|------|------|
| PR-ROUTER-INTERNAL | R4：internal listener 独立（🟡 规模评估后排期）| 4-8h |

**风险说明**：R4 若实施 blast radius 超预期，降级到 PG-REPO 大项后处理，不阻塞主线。

---

## 域 7：Events/DTO 域

**目标**：将 6 个 slice 的事件 payload 从 `map[string]any` 升级为 typed event struct，提升事件契约稳定性和类型安全。

**域间依赖**：S4 改动 3 个 cell（access-core + config-core + audit-core），建议 Auth 域稳定后再做，避免 rebase 冲突。

| # | 任务 | 工时 | 优先级 | 文件 | 来源 |
|---|------|------|--------|------|------|
| S4 | **EVENT-PAYLOAD-TYPED-01** (Cx2)：sessionlogin/sessionlogout/configwrite/configpublish/auditappend/auditverify 事件 payload `map[string]any` → typed event struct | 3h | P2 | 6 个 `service.go` + event contract schemas | PR#133 re-review |

**域内 PR 拆分**：

| PR | 内容 | 工时 |
|----|------|------|
| PR-EVENTS-TYPED | S4：6 个 service.go typed event struct + schemas | 3h |

---

## 域 8：Features 域

**目标**：补齐 setup endpoint、walkthrough DX 修复、Auth-DX 文档收口。

**域间依赖**：P1-19/P1-13 无强依赖；F2（SYSTEM-TOPOLOGY-API）可独立延后。

| # | 任务 | 工时 | 优先级 | 文件 | 来源 |
|---|------|------|--------|------|------|
| P1-19 | **AUTH-SETUP-ENDPOINT-01** (P1, Cx2): ① `GET /api/v1/setup/status`；② `POST /api/v1/setup/admin`（无 admin 时创建，已有则 409）；③ `cells/access-core/slices/setup/` slice + `contracts/http/auth/setup/` 合约；④ 两端点加入 `WithPublicEndpoints` | 4h | P1 | `cells/access-core/slices/setup/`（新）+ `contracts/http/auth/setup/`（新）+ `cmd/core-bundle/main.go` | P1-12 拆出 2026-04-18 |
| P1-8 | **FEAT-1 DEVICE-LIST-API**：新建 `device-list` slice + `GET /api/v1/devices` 分页 + contract + contract_test | 3h | P1 | `cells/device-cell/slices/device-list/` + `contracts/http/device/list/v1/` | backend_issues.md #1 |
| P1-9 | **FEAT-2 FLAG-WRITE-API**：`PUT /api/v1/config/flags/{key}` 写入端点 + contract + contract_test | 3h | P1 | `cells/config-core/slices/configwrite/` + `contracts/http/config/flags/write/v1/` | backend_issues.md #2 |
| P1-13 | **SSO-BFF-WALKTHROUGH-JWT-FIX-01** (P0 DX, Cx1)：README walkthrough 第 10/11 步未携带 JWT，401；补 Bearer header + walkthrough test 断言 | 1h | P1 | `examples/sso-bff/README.md` + `examples/sso-bff/walkthrough_test.go` | 2026-04-18 六席审查 |
| S23 | **AUTH-WALKTHROUGH-COMPOSE-01** (P2, Cx3, 🟡 可延后): 提取 `NewSSOBFFApp(opts...)` 被 `main.go` 和 walkthrough test 共用；test server 走同一 bootstrap + Start/Stop 路径 | 4h | 🟡 | `examples/sso-bff/bootstrap.go`（新）+ `main.go` + `walkthrough_test.go` | PR#172 review F3 |
| S34 | **AUTH-ADMIN-FORCE-RESET-01** (P2, Cx2, 🟡 可延后): Admin 强制重置端点 `POST /api/v1/access/users/{id}/password/reset`；生成临时密码 + `PasswordResetRequired=true` | 4h | 🟡 | `cells/access-core/slices/identitymanage/` + `contracts/http/auth/user/reset/v1/`（新）| PR#183 reviewer round 2 P1-2 |
| F2 | **SYSTEM-TOPOLOGY-API** 🟡：`GET /internal/v1/system/topology` 返回 cell/slice/contract 拓扑 JSON | 4h | 🟡 | 新 slice 或 `runtime/bootstrap/` | 历史 Batch 8 |

**域内 PR 拆分**：

| PR | 内容 | 工时 |
|----|------|------|
| PR-FEAT-SETUP-ENDPOINT | P1-19：setup HTTP 端点（GET /setup/status + POST /setup/admin + slice + contract）| 4h |
| PR-FEAT-DEVICE | P1-8：device-list slice + 分页 API | 3h | 🚫 延期至产品发布后 |
| PR-FEAT-FLAG | P1-9：flag-write 端点 | 3h | 🚫 延期至产品发布后 |
| PR-FEAT-WALKTHROUGH-FIX | P1-13：README JWT 补全 + walkthrough test 断言（P0 DX，独立小 PR）| 1h |
| PR-FEAT-APP-BUILDER | S23：walkthrough 共享 bootstrap（🟡）| 4h |
| PR-FEAT-ADMIN-RESET | S34：admin 强制密码重置端点（🟡）| 4h |
| PR-FEAT-TOPOLOGY | F2：topology API（🟡 可延后）| 4h |

---

## 域 9：可观测性/Bootstrap 域（🟡 全部可延后）

**目标**：自动接线 HTTP collector、统一三 adapter PoolStats 接口、夜间 OTLP 协议兼容测试。

| # | 任务 | 工时 | 优先级 | 文件 | 来源 |
|---|------|------|--------|------|------|
| R2 | **OBS-HTTP-COLLECTOR-AUTOWIRE-01** 🟡 (Cx3)：`bootstrap.WithMetricsProvider` 自动构造 `NewProviderCollector`；`WithHTTPCollectorCellID` option | 2h | 🟡 | `runtime/bootstrap/bootstrap.go` + `runtime/http/middleware/` | PR#157 review S4-01 |
| R3 | **OB-02** 🟡：safe_observe broken logger 注入测试 | 1h | 🟡 | `runtime/http/middleware/safe_observe_test.go` | 历史 backlog 0-J |
| A10 | **OBS-LGTM-INTEGRATION-01** 🟡（从域 4 迁入）：`//go:build integration` 夜间 OTel collector 真实 OTLP 协议兼容性测试 | 2h | 🟡 | `adapters/otel/integration_test.go` | PR#157 review S6-04 |
| A7 | **POOLSTATS-IFACE-01** 🟡：三个 adapter PoolStats 公共接口（OTel collector 消费）；建议搭车 Outbox 域 PR-OUTBOX-WIRE | 1h | 🟡 | `adapters/postgres/pool.go` + `redis/client.go` + `rabbitmq/connection.go` | PR#134 review |
| S2-follow | **CONTRACT-ERROR-SCHEMA-EXTEND-01** (P2, Cx1, 🟡 可延后): 其余 HTTP contract 补充 401/403 responses 声明，使错误响应 schema 全库覆盖 | 2h | 🟡 | `contracts/http/**/{publish,get,write,flags}/v*/contract.yaml` | PR-CONFIG-POLISH 后续 |
| S13-follow | **4XX-LOG-SAMPLING-01** (P3, Cx1, 🟡 可延后): 高频端点 4xx 日志采样（1%）；仅在运维告警通道过载时再做 | 1h | 🟡 | `pkg/httputil/response.go` | PR-CONFIG-POLISH 后续 |
| A14 | **DISTLOCK-LOST-METRIC-01** (P3, Cx2, 🟡 可延后，🟠 首个 distlock 消费方后触发): `Lock.Release` ErrLockLost 路径仅写 slog；发射 `distlock_release_total{outcome}` | 2h | 🟡 | `runtime/distlock/` + `adapters/redis/distlock.go` | PR#178 round-4 |
| A15 | **DISTLOCK-RENEW-JITTER-01** (P3, Cx2, 🟡 可延后，生产 Redis 并发峰值告警触发): `renewLoop` ticker 硬编码 `ttl/2`；加 ±10-20% jitter | 2h | 🟡 | `adapters/redis/distlock.go` | PR#178 round-4 |
| A16 | **DISTLOCK-RENEW-RATIO-CONFIGURABLE-01** (P3, Cx2, 🟡 可延后，首个 caller 调优诉求触发): 增 `WithRenewRatio(float64)` option | 2h | 🟡 | `adapters/redis/distlock.go` | PR#178 round-4 |

**域内 PR 拆分**：

| PR | 内容 | 工时 |
|----|------|------|
| PR-OBS-BOOTSTRAP | R2 + R3 + A10：HTTP collector 自动接线 + safe_observe 测试 + OTLP 夜间测试 | 5h |
| A7 搭车 Outbox 域 | POOLSTATS-IFACE-01 合并到 PR-OUTBOX-WIRE | 1h |
| PR-OBS-CONTRACTS | S2-follow：全库 HTTP contract 401/403 响应声明（🟡）| 2h |
| PR-OBS-DISTLOCK | A14 + A15 + A16：distlock metric + jitter + ratio（🟡 P3 搭车）| 6h |

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
| S33 | **WALKTHROUGH-CI-SMOKE-01** 🟡 (P2, Cx1)：`examples/sso-bff/walkthrough_test.go` 带 `//go:build integration` 未进 CI 主门禁；增 `walkthrough-smoke` job 或拆 unit subtests | 1h | 🟡 | `.github/workflows/ci.yml` + `examples/sso-bff/walkthrough_test.go` | 2026-04-18 六席审查 |
| F3 | **P2-T-02 audit e2e 测试**：Journey 级验收（audit-core + access-core 全链路）| 2h | P2 | `journeys/` + integration test | 历史 Batch 8 |
| F10 | **TEST-JOURNEY-ASSEMBLY-HARNESS-01** (Cx3, 🟡 可延后): `tests/integration/journey_test.go` 全部 28 条均 `t.Skip`；建 full-assembly harness 后统一恢复 | 8h | 🟡 | `tests/integration/` + assembly fixture | PR#166 R2-P2 |

**域内 PR 拆分**：

| PR | 内容 | 工时 |
|----|------|------|
| PR-DX-VALIDATOR | P1-4 + K1：JSON/SARIF 输出 + ProjectLocator 接口 | 9h |
| PR-DX-METAPERF | P1-5：metadata parser 性能基准 | 4h |
| PR-DX-CI | S7 + A8 + A9 + S33：CI metadata gate + 镜像固定 + lint 固定 + walkthrough smoke | 4h |
| PR-DX-LINT-MODERN | X9：全仓现代化清理（独立 PR）| 6h |
| PR-DX-JOURNEY | F3 + F10：audit e2e 测试 + full-assembly journey harness（🟡 Wave 4）| 10h |

---

## 域 11：PG-REPO 大项（Phase X，独立排期）

**目标**：完成 access-core/audit-core/device-cell 的 PostgreSQL Repository 实现，激活 Redis session cache，完成 RBAC level 升级，使所有 cell 脱离内存存储。

**域间依赖**：最重量级的独立大项。本域完成后可触发 Auth 域 X10（AUTH-REFRESH-OPAQUE-01）。

**待做**：

| # | 任务 | 工时 | 备注 |
|---|------|------|------|
| X1-DDL | migration DDL：users / sessions / roles / devices+commands | 0.5d | `adapters/postgres/migrations/006+.sql` |
| X1-ACCESS | access-core PG repo（User / Session / Role）| 1d | `cells/access-core/internal/adapters/postgres/`（新建）|
| X1-AUDIT | audit-core / device-cell / order-cell PG repo | 0.5-1d | `cells/*/internal/adapters/postgres/` |
| X1-LINK | 落地联动：RBAC-ASSIGN-LEVEL-UPGRADE-01 L0→L1 + SEED-ROLE-IFACE-01 去 type assertion + ACCESS-LEVEL-AUDIT-01 slice.yaml 校正 + AUTH-CACHE-01 激活 Redis session cache | 1-2d | 同 PR 或紧邻 PR |
| S16 | **RUNTIME-TOPOLOGY-SINGLE-SOURCE-01** 🟡 (P2, Cx3)：运行拓扑单一事实源，消除 ENV 分裂（最小修复已合 PR#169；彻底方案随本域）| 6h | PR#169 review F-NEW-2 |
| S17 | **POOL-FRAMEWORK-LIFECYCLE-01** 🟡 (P2, Cx3)：外部资源提升为 bootstrap 托管，统一 LIFO shutdown + 自动 health checker 注册（最小修复已合；彻底方案随本域）| 4h | PR#169 review F-NEW-3 |
| S15 | **ERROR-CTX-CANCELLED-CLASSIFY** 🟡 (P3, Cx2)：`ctx.Canceled` 归类 `ErrContextCanceled` | 1h | PR#169 review F-T-3 |

**域内 PR 拆分**：

| PR | 内容 | 工时 |
|----|------|------|
| PR-PGREPO-DDL | X1-DDL：006+ migration DDL | 0.5d |
| PR-PGREPO-ACCESS | X1-ACCESS：access-core PG repo + 集成测试 | 1d |
| PR-PGREPO-AUDIT-DEVICE | X1-AUDIT：audit-core + device-cell PG repo | 0.5-1d |
| PR-PGREPO-LINK | X1-LINK：落地联动（RBAC level + seed + cache 激活）| 1-2d |
| PR-PGREPO-TOPOLOGY | S16 + S17：runtime topology 单一事实源 + pool 托管（🟡 搭车）| 10h |

**域估算（主线 DDL + ACCESS + AUDIT + LINK）**：约 3-5d；含 S16/S17 彻底方案 +10h。

---

## 推荐执行 Wave

```
Wave 3（当前）— 约 2-3 工作日
  ├── Events 域   PR-EVENTS-TYPED (3h)
  ├── Auth 域     PR-AUTH-SERVICE-HARDEN (P1-17 + S41, 4h)
  │              + PR-AUTH-JWT-AUDIENCE (S18, 3h, 🟠 条件)
  │              + PR-AUTH-ISSUER (S31+S32, 2h)
  │              + PR-AUTH-JWT-TEST (S19+S22, 4h, 🟡)
  └── Features 域 PR-FEAT-SETUP-ENDPOINT (P1-19, 4h)
                  + PR-FEAT-WALKTHROUGH-FIX (P1-13, 1h)
                  [P1-8 DEVICE-LIST / P1-9 FLAG-WRITE 延期至产品发布后]

Wave 4（🟡 可延后，机会性纳入）— 按资源排期
  ├── 可观测性/Bootstrap 域  R2 + R3 + A10 (5h)
  ├── DX/CI/工具链域         S7 + A8 + A9 + S33 (4h) + P1-4/K1 (9h) + P1-5 (4h) + X9 (6h)
  ├── Outbox 域              A13 + A14 (~5h)
  ├── HTTP 域                PR-ROUTER-INTERNAL (4-8h)
  ├── Features 域            PR-FEAT-TOPOLOGY (4h) + PR-FEAT-APP-BUILDER (S23, 4h) + PR-FEAT-ADMIN-RESET (S34, 4h)
  ├── DX/CI 域               PR-DX-JOURNEY (F3+F10, 10h)
  └── Observability 域       PR-OBS-CONTRACTS (S2-follow, 2h) + PR-OBS-DISTLOCK (A14+A15+A16, 6h)

Wave 5（Phase X，独立大项，发布后按需排期）
  PG-REPO 大项   DDL → ACCESS → AUDIT-DEVICE → LINK → TOPOLOGY
                 约 3-5d + S16/S17 10h

  触发后做：
    X10 AUTH-REFRESH-OPAQUE     （PG-REPO 上线后触发，1-2d）
```

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
| T9 | AUTH-BYPASS-METRICS-01 | observability 专项落地（R2），或 401 baseline 需 method 维度审计 |
| T10 | RMQ-STATTER-RENAME-01 | 独立小 PR，可随时做（无阻塞，15min）|
| T11 | AUTH-LOADKEYSFROMENV-UNEXPORT-01 | 独立小 PR，可随时做（无阻塞，30min）|
| P1-3a | CORS-OPTIONS-PUBLIC-ENDPOINT-01 | 新增 CORS middleware 时评估 OPTIONS * 是否加入公共端点 |
| A13 | BOOTSTRAP-WIRE-RMQ-BROKER-HEALTH-01 | cmd/core-bundle 接入真实 RabbitMQ connection（替换 in-memory eventbus）|
| S14a | CONFIG-VALUE-KMS-AWS-PROVIDER-01 | 明确生产云平台 + 通过 KMS 安全评审（前置 S14 ✅ PR#195 已完成）|
