# 项目代办事项整理（按 架构 → 问题 → 功能 分层）

## Context

当前 GoCell 未完成事项分散在多处：
- `docs/backlog.md`（主 backlog，按 P0/P1/P2/P3 分类，更新日期 2026-04-23）
- `docs/reviews/202604221954-review-from-other-verification.md`（外部 review 验证，14 条 CONFIRMED）
- `docs/202604232229-220-pr-issue-split-report.md`（PR#220 遗留，5 PR 拆分建议）
- `docs/backlog_later_detail.md`（v1.1+ 长期规划详解）
- `docs/plans/*.md`（15 份 plan 文档，含未吸收项 P1-19 / T6 / P1-A）
- `docs/reviews/202604221030-308-step1-common-issues-six-seat-report.md`（CI-01~CI-12 批次）

用户需要按「架构优先 → 问题其次 → 功能最后」重新分层，便于快速看清下一步投入方向。

**扫描覆盖**：
- 最近 PR git log（PR#211..#222，共 12 条）✅
- `docs/plans/` 15 份 plan 文档 ✅
- `docs/reviews/` 活跃 review（非 archive）17 份 ✅
- PR#220 遗留报告 ✅

基准：`develop @ f32d54d`（PR#222 ctxkeys 迁移合入后）

分类原则：
- **架构**：分层约束、接口抽象、治理规则、重复模式抽取、模块拆分
- **问题**：安全漏洞、兼容性缺口、测试/CI 不足、tech debt、bug、死代码、文档事实漂移
- **功能**：新端点/新能力、新模块、发布与文档、长期规划

---

## PR#211–#222 核销对照（含 backlog 状态同步提示）

| PR | Commit | 关闭 backlog | 状态 | 残留 |
|---|---|---|---|---|
| #211 | a717960 | **L4** ID-VALIDATION-SINGLE-SOURCE-01 | backlog.md 已标 ✅ | — |
| #212 | 85b4cba | **L6** CONTRACTTEST-MODEL-ALIGN + **K1** (won't-do) | backlog.md 已标 ✅ | — |
| #213 | 639aacf | **F2** PGRefreshStore + 新增 X11-X15 4 项 | backlog.md 已登记 | service 切换（X15）待做 |
| #214 | a4066fb | **L7 FMT15**-NEXTCURSOR-ENFORCE | backlog.md 已标 ✅ | L7-FMT15b（单/列表 oneOf 拆分）独立项 |
| #216 | 2675aad | **S10** MODE-SEMANTIC-SPLIT | backlog.md 已标 ✅ | — |
| #217 | 76d918a | **L1** AUDIT-ROUTE-POLICY + **L2** ROUTE-POLICY-REGISTRY | backlog.md 已标 ✅ | — |
| #218 | 627a8e6 | **L10** INTERNAL-ROUTE-POLICY-ALIGN | ⚠️ **backlog.md:98 仍列为开放**（需同步核销） | — |
| #219 | （未列） | device-list review fixes | — | 需单独核销 |
| #220 | a74c487 | **P2-12** NAMING + 新增 FMT-16/C1/A1 治理规则 | verification 已标 ✅ | **PR#220 遗留报告 6 项**，见下 |
| #221 | f7f5a6e | **P1-8** FEAT-1 DEVICE-LIST-API 主体 | backlog.md 已登记 | rbaccheck 伪分页残余 |
| #222 | f32d54d | **A1 P0-2** PKG-CTXKEYS-LAYER | verification 已标 ✅ | — |

**Action：** backlog.md 需把 L10 从开放状态改为已关闭（核销 PR#218）。

---

## 一、架构（Architecture）

> 分层约束 / 接口抽象 / 治理规则 / 模式抽取 / 模块拆分。先修架构，局部问题自然减少。

### P1 — 近期

| ID | 任务 | 工时 | 关键文件 |
|---|---|---|---|
| P1-4 | **OUTPUT-JSON-SARIF-01** 诊断模型统一（单一 `Issue` struct → 多 printer 映射） | 6h 🟡 | `cmd/gocell/` + `kernel/governance/` |
| L7-FMT15b | **CONFIG-GET-DUAL-MODE-SPLIT-01** 拆 `contracts/http/config/get/v1` oneOf 合并 | 2h | `contracts/http/config/get/v1/` + `cells/configcore/slices/configread/` |
| A11 verif. | **GOVERNANCE-EXAMPLES-COVERAGE-01**（P1-17）governance 规则扫 `examples/` 硬编码 — ✅ PR-A1：parser `fs.WalkDir(".", ...)` 已自然覆盖 `examples/**`，根 `gocell validate --strict` 即拉全；新增 `TestProjectWalksExamples` 回归测试固化；放弃新建 `rules_examples.go` | resolved | `kernel/governance/validate_test.go` |
| A5 verif. | **VALIDATION-HELPER-EXTRACT-01**（P1-4）抽 `pkg/validation.RequireNotBlank` | 2h | `pkg/validation/`（新） |
| A8 verif. | **CMD-THICK-ENTRY-REDUCE-01**（P1-13 PARTIALLY）继续缩减 `cmd/corebundle/main.go` | 2h | `cmd/corebundle/` |
| **新·P1-A** | **PRINCIPAL-UNIFIED-CONTRACT-01**（auth-federated-whistle F7）统一 Principal 契约，运行时鉴权语义收口 | 4h | `runtime/auth/` + 各 cell middleware |
| **新·PR220-5** | **EVENTROUTER-SUBSCRIPTION-IDENTITY-SPLIT-01** `EventRouter.AddHandler` 拆 `ConsumerGroup`（broker/dedupe）与 `CellID`（observability），消除注释"consumerGroup 必须传 cell ID"与实现矛盾 | 3h | `runtime/eventrouter/` + `kernel/outbox/` + `cells/*/cell.go` |

### P2 — 本版本内

#### Kernel / Runtime

| ID | 任务 | 工时 | 关键文件 |
|---|---|---|---|
| R2 | **OBS-HTTP-COLLECTOR-AUTOWIRE-01** `WithMetricsProvider` 自动构造默认 HTTP collector | 2h 🟡 | `runtime/bootstrap/bootstrap.go` |
| R4 | **INTERNAL-LISTENER-01** `/internal/v1/*` 独立 listener 或 service-token/mTLS | 4-8h 🟡 | `runtime/bootstrap/bootstrap.go` |
| A21 | **HEALTH-CHECKER-CTX-BUDGET-01** `Checker` 升级 `func(ctx) error` + 统一 deadline + 并行 | 3h 🟡 | `runtime/http/health/` + `kernel/lifecycle/` |
| L7 | **FMT15-NEXTCURSOR-ENFORCE-01** 治理规则强制 `hasMore`+`nextCursor` 同时存在 | 2h 🟡 | `kernel/governance/rules_fmt.go` |
| L8 | **PAGINATION-HELPER-EXTRACT-01** 抽 `pkg/httputil/pagination.go` 公共 helper | 2h 🟡 | `pkg/httputil/pagination.go`（新） |
| L11 | **GOVERNANCE-CI-MAINBRANCH-01** governance workflow 扩展到 `main`/`release/**` — ✅ PR-A1 | resolved | `.github/workflows/governance.yml` |
| **新·PR220-2** | **DOC-NAMING-GUARD-01** 建 `cmd/gocell/app/naming_docs_test.go` + `naming-guard.yaml`，扫活动文档禁旧 `my-app`/`sso-bff`/`core-bundle`/旧 slice 名 | 3h | `cmd/gocell/app/` + CI |
| **新·PR220-4** | **CI-LINT-EVENT-SEMANTIC-SPLIT-01** `push` 全量 lint / `pull_request` 保留 diff 降噪；修 merge-base 退化 — ✅ PR-A1：`_build-lint.yml` reusable `workflow_call` + `ci.yml`（push 全量）+ `pr-check.yml`（PR 降噪） | resolved | `.github/workflows/_build-lint.yml` + `ci.yml` + `pr-check.yml` |

#### 🔄 本 PR 拆出条目（PR-A1 lint 彻底化衍生）

| ID | 任务 | 工时 | 关键文件 |
|---|---|---|---|
| V-A11b | **EXAMPLES-HARDCODE-STRING-SCAN-01** governance 扫 examples/ 源码里 cell id 字面量（如 `"sso-bff"`、`"core-bundle"`），防硬编码漂移。PR-A1 仅覆盖 parser walk 维度，字符串扫描延后。 | 3h | `kernel/governance/` + CI |
| LINT-WEBSOCKET-MIGRATION-01 | **nhooyr.io/websocket → coder/websocket 库迁移** 21 staticcheck SA1019；PR-A1 临时 `.golangci.yml` exclude，独立 PR 完成迁移 | 4-6h | `adapters/websocket/` + `runtime/websocket/` |

#### Adapter

| ID | 任务 | 工时 | 关键文件 |
|---|---|---|---|
| A7 | **POOLSTATS-IFACE-01** 三 adapter 公共 PoolStats 接口 | 1h 🟡 | `adapters/postgres/pool.go` + `redis/client.go` + `rabbitmq/connection.go` |
| A14 verif. | **ADAPTER-CLOSE-HELPER-01**（P2-5）抽 `adapterutil.CloseWithDeadline` | 2h | `adapters/adapterutil/`（新） |
| A14 | **VAULT-AUTH-PLUGGABLE-01** AppRole / K8s auth（并入 S4b + DEGRADATION-GAUGE） | 3h 🟡 | `adapters/vault/transit_provider.go` |
| A15 | **VAULT-NAMESPACE-MULTITENANT-01** | 1h 🟡 | `adapters/vault/transit_provider.go` |
| A16 | **VAULT-DATAKEY-ENDPOINT-01** 🟠 S14a 触发 | 2h 🟡 | `adapters/vault/transit_provider.go` |
| A18 | **VAULT-ROTATE-OPTIMISTIC-LOCK-01** 无锁 rotate + 写锁仅更新 version cache | 2h 🟡 | `adapters/vault/transit_provider.go` |

#### Slice / Cell

| ID | 任务 | 工时 | 关键文件 |
|---|---|---|---|
| A15 verif. | **CELL-GO-SPLIT-01**（P2-7）access-core/cell.go 582 行 + config-core/cell.go 431 行拆 `cell_routes.go` + `cell_events.go` + `cell_lifecycle.go` | 2h×2 | `cells/accesscore/cell.go` / `cells/configcore/cell.go` |
| A16 verif. | **RUN-IN-TX-HELPER-01**（P2-8）`kernel/persistence.TxRunner` 加 helper | 2h | `kernel/persistence/` |
| A17 verif. | **FETCH-ROLE-NAMES-DEDUP-01**（P2-9）抽 `cells/accesscore/internal/rolefetch`，`Strict`/`Lenient` | 2h | `cells/accesscore/internal/rolefetch/`（新） |
| S4 | **EVENT-PAYLOAD-TYPED-01** 6 个 event 的 `map[string]any` → typed struct | 3h | 6 个 `service.go` + event contract schemas |
| S23 | **AUTH-WALKTHROUGH-COMPOSE-01** 抽 `NewSSOBFFApp(opts...)` 给 main + test 复用 | 4h 🟡 | `examples/ssobff/bootstrap.go`（新） |
| L9 | **EXAMPLES-CONTEXT-NOOP-01** 删除 examples 自定义 `noopTxRunner`，用 `persistence.NoopTxRunner` | 1h 🟡 | `examples/*/` |
| **新·plan** | **T6 GOCELL-PER-CELL-ADAPTER-01** 全局 env 拆为单 cell（PR-X-PG-REPO-ACCESS 强制前置） | 2h | `cmd/corebundle/` + cell 配置 |

### P3 — 长期架构演进

| ID | 任务 | 前置 |
|---|---|---|
| X1 | **PG-DOMAIN-REPO** 5 个域 Repository PG 实现；联动 RBAC-ASSIGN-LEVEL-UPGRADE + SEED-ROLE-IFACE + ACCESS-LEVEL-AUDIT + AUTH-CACHE 激活 | — |
| X11 | **REFRESH-HMAC-SPLIT-01** HMAC-split token（selector\|verifier）🟠 **X15 前必须** | — |
| X12 | **REFRESH-IDLE-EXPIRE-01** 滑动窗口 `idle_expires_at` 🟠 X15 后 | — |
| X13 | **REFRESH-PARTITION-01** range 分区 🟠 流量阈值后 | — |
| X14 | **REFRESH-GRACE-COUNTER-01** grace 窗口重用次数上限 🟠 X15 后 | — |
| X10 | **AUTH-REFRESH-OPAQUE-01** JWT → opaque + rotation store 🟠 X1 后 | X1 |
| X15 | **REFRESH-OPAQUE-INTEGRATION-01** sessionrefresh/login 接线 opaque 🟠 X11 后 | X11 |
| — | **Kernel 子模块补全** wrapper (P1) / command (P1) / webhook (P2) / reconcile (P2) / scheduler (P2) / replay (P3) / rollback (P3) | 详见 backlog_later_detail.md §2 |
| — | **Adapter 分层重整** AL-01 Outbox Relay 调度 → runtime / AL-02 DistLock 抽象 / AL-04 auth 依赖隔离 / RMQ-STATUS-01 结构化 ConnectionState | §3 |
| — | **ER-ARCH-01** Router `time.After(500ms)` 探测 → Subscriber `Setup()`/`Run()` 双阶段 | §4 |
| — | **Cell 接口 ISP 拆分** 12 方法基础接口 → `Cell` + `CellLifecycle` + `CellMetadata` | §4 |
| — | **CONTRACT-META-01** 传输层描述一等公民（Method/Path/Params/Status/NoContent） | §6 |
| — | **Metadata 治理规则补全** G-1 FMT-11 ✅ PR-A1（parser `KnownFields(true)` 已在解析期拒绝，加 `parser_strict_test.go` 7×5 回归） / G-2 TOPO-07 ✅ PR-A1（源码已为 SeverityError，加 `TestTOPO07_EnforcesMaxConsistencyLevel` 回归）/ G-4 deprecated break ✅ PR-A1（源码已为 SeverityError + IssueForbidden，加 `TestTOPO08_BlocksDeprecatedReference` 回归）/ G-6 boundary（待 PR-A24） | §1 |

### 触发器（Triggers，条件延后）

| ID | 任务 | 触发条件 |
|---|---|---|
| T1 | AUTH-PROVIDER-EXPORT-01 | 第二个 auth provider cell |
| T2 | AUTH-ISSUE-OPTIONS-01 | `JWTIssuer.Issue()` 第 5 个参数 |
| T4 | CB-RESILIENCE-PACKAGE-01 | 第二个非 HTTP CB 消费方 |
| T5 | AUTH-SIGNER-01 | golang-jwt v6 发布 |

---

## 二、问题（Problems）

> 安全 / 兼容性 / 测试 / CI / bug / tech debt / 死代码 / 文档事实漂移。点状修复。

### P1 — 近期（含安全 P1）

#### 安全

| ID | 任务 | 工时 | 关键文件 |
|---|---|---|---|
| S-nonce | **SERVICE-TOKEN-NONCE-STORE-01** 🟠 多 pod 生产前触发 | 3h | `runtime/auth/authenticator.go` + `cmd/corebundle/` |
| S4b | **VAULT-TOKEN-STATIC-REAL-GUARD-01** 🟠（随 A14 批量） | 1h | `adapters/vault/transit_provider.go` |
| A9 verif. | **DEMO-KEY-REJECT-LOADKEYSET-01**（P1-14）`runtime/auth/keys.go:328` 补 `rejectDemoKey` + 模板黑名单 | 1h | `runtime/auth/keys.go` |
| A19 verif. | **DEMO-KEY-WARN-COMMENT-01**（P2-11）`demo_keys.go` + `examples/ssobff/main.go` 加警告注释（与 A9 同 PR） | 0.5h | 同上 |
| **新·plan** | **P1-19 AUTH-SETUP-ENDPOINT-01** 首次启动 setup 端点（从 v1.0-pre-release-plan Batch 5） | 4h | `cells/accesscore/` + 新 slice |

#### Windows / 供应链 兼容性

| ID | 任务 | 工时 | 关键文件 |
|---|---|---|---|
| A2 verif. | **HARDCODED-UNIX-PATH-01**（P0-4）`examples/ssobff/main.go:150` → `os.UserConfigDir()` | 0.5h | `examples/ssobff/main.go` |
| A3 verif. | **WINDOWS-SIGNAL-01**（P1-1）补 `shutdown_sigterm_unix_test.go` | 0.5h | `runtime/shutdown/` |
| A4 verif. | **SYMLINK-TEST-PRIVILEGE-01**（P1-2）6 处 Windows skip | 1h | `runtime/config/watcher_test.go` + `kernel/governance/validate_test.go` |
| A10 verif. | **GOVERNANCE-WINDOWS-PATH-01**（P1-15）examples 拼接改 `filepath.Join` | 0.5h | `examples/ssobff/main.go` |
| A6 verif. | **WEBSOCKET-DEPRECATED-01**（P1-5）`nhooyr.io/websocket` → `coder/websocket` | 2h | `go.mod` + 全仓 import |
| A7 verif. | **DEPRECATED-ADAPTER-METHODS-01**（P1-8）`Statter()`/`InitializeSubscription()` 清理 | 1h | `adapters/*/` |

#### 文档事实漂移（PR#220 遗留 P1）

| ID | 任务 | 工时 | 关键文件 |
|---|---|---|---|
| **新·PR220-1** | **DOC-CAPABILITY-INVENTORY-REWRITE-01** `capability-inventory.md` 端点漂移（4 处 logout/validate/rbac/check/authorize），改为真实 route + contract | 2h | `docs/design/capability-inventory.md` + `docs/design/capability-map.md` + `docs/architecture/module-dependency-report.md` + `docs/architecture/glossary.md` + `docs/design/master-plan.md` |
| **新·PR220-1b** | **DOC-IOTDEVICE-README-ENVELOPE-01** register/enqueue/ack 响应示例补 `data` 包装 | 0.5h | `examples/iotdevice/README.md` |
| **新·PR220-3** | **JOURNEY-VERIFY-FAIL-CLOSED-01** 修 `J-sessionrefresh` RunPattern 命名（驼峰不一致）+ 解析 `go test -json` 识别 `--- SKIP`（当前 `t.Skip("stub...")` 假绿） | 4h | `kernel/verify/ref.go` + `gotest.go` + `runner.go` + `tests/integration/journey_test.go` + `docs/guides/integration-testing.md` |
| **新·PR220-e1** | **NAMING-BASELINE-CONTRADICTION-01** `docs/architecture/naming-baseline.md` 自身前后矛盾（"no-dash" vs "kebab-case"），与 CLAUDE.md / metadata-model-v3.md 冲突 | 0.5h | `docs/architecture/naming-baseline.md` |
| **新·PR220-e3** | **STATUS-BOARD-J-ORDERCREATE-01** `J-ordercreate.yaml` 存在但 status-board 无条目（ADV-01 warning）+ auto criterion 缺 checkRef | 0.5h | `journeys/status-board.yaml` + `journeys/J-ordercreate.yaml` |

#### 性能基线

| ID | 任务 | 工时 | 关键文件 |
|---|---|---|---|
| P1-5 | **METADATA-PERF-BENCH-01** `BenchmarkParseFS_500Files` + goccy/go-yaml 评估 | 4h 🟡 | `kernel/metadata/parser_test.go` |

### P2 — 本版本内

#### Bug 残余

| ID | 任务 | 工时 | 关键文件 |
|---|---|---|---|
| FEAT-1-残余 | **RBACCHECK-PAGINATION-REAL-01** `handleListRoles` 迁到 `query.PageResult[RoleResponse]` + 真实游标 + `MustRejectResponse` | 3h | `cells/accesscore/slices/rbaccheck/handler.go:79` + `rbaccheck/contract_test.go` |
| **新·PR220-e2** | **GENERATED-BOUNDARY-STRATEGY-01** `assemblies/corebundle/generated/boundary.yaml` 缺失（REF-16 warning）—— 决策：纳入 regenerate-and-diff 门禁 或 降级 warning | 1h | `kernel/governance/` + `.github/workflows/` |

#### 测试覆盖

| ID | 任务 | 工时 | 关键文件 |
|---|---|---|---|
| L7-ex | **EXAMPLES-STARTUP-SMOKE-01** CI 加 `examples-smoke` job | 0.5h | `.github/workflows/ci.yml` |
| R3 | **OB-02** safe_observe broken logger 注入测试 | 1h 🟡 | `runtime/http/middleware/safe_observe_test.go` |
| S19 | **JWT-AUDIENCE-DRIFT-INTEG-TEST-01** 真实 sessionlogin 路径 drift 检测 | 2h 🟡 | `cmd/corebundle/` |
| S21 | **JWT-AUD-TEST-TABLE-DRIVEN-01** 9 场景改 table-driven | 1h 🟡 | `runtime/auth/jwt_aud_test.go` |
| S22 | **REFRESH-AUD-REAL-ROUTE-TEST-01** 真实 HTTP refresh wrong-aud 测试 | 2h 🟡 | `cells/accesscore/auth_integration_test.go` |
| S24 | **AUTH-MIDDLEWARE-AUD-REFRESH-E2E-01** `httptest.NewServer` + 真实 `AuthMiddleware` | 1h 🟡 | `runtime/auth/middleware_aud_test.go` |
| F10 | **TEST-JOURNEY-ASSEMBLY-HARNESS-01** 28 条 `t.Skip` journey 集成测试 | 8h 🟡 | `tests/integration/` + assembly fixture |
| A10 | **OBS-LGTM-INTEGRATION-01** 夜间 OTel collector OTLP 兼容性 | 2h 🟡 | `adapters/otel/integration_test.go` |

#### CI / 供应链

| ID | 任务 | 工时 | 关键文件 |
|---|---|---|---|
| A8 | **CI-DIGEST-01** testcontainers 镜像 tag+digest 双固定 | 1h 🟡 | `adapters/*/integration_test.go` |
| A9 | **CI-LINT-PIN-01** golangci-lint patch 级固定 + dependabot | 1h 🟡 | `.github/workflows/ci.yml` |

#### 死代码 / 卫生（一次性 housekeeping，不入 backlog，留 verification 报告追踪）

| ID | 任务 | 工时 | 关键文件 |
|---|---|---|---|
| A12 verif. | **DEPRECATED-SHIM-CLEAR** 删 `kernel/outbox/outbox.go:536,543,552` | 0.5h | `kernel/outbox/outbox.go` |
| A13 verif. | **KERNEL-GODOC-BACKFILL** `kernel/cell/` 20-25% 公开项补 godoc | 2h | `kernel/cell/` |
| A18 verif. | **SLOG-ERROR-UNIFY** `adapters/` 统一 `slog.Any("error", err)` | 1h | `adapters/*/` |

> 建议一次性 PR 清完，不占 backlog ID 追踪。完成后在 verification 报告对应条目标注 RESOLVED。

#### 合约 / 错误归类

| ID | 任务 | 工时 | 关键文件 |
|---|---|---|---|
| S2-follow | **CONTRACT-ERROR-SCHEMA-EXTEND-01** 其余 HTTP contract 补 401/403 | 2h 🟡 | `contracts/http/**/contract.yaml` |
| S13-follow | **4XX-LOG-SAMPLING-01** 高频 4xx 日志采样（仅告警通道过载再做） | 1h 🟡 | `pkg/httputil/response.go` |
| S15 | **ERROR-CTX-CANCELLED-CLASSIFY** config_repo `ctx.Canceled` 归类 | 1h 🟡 | `cells/configcore/internal/adapters/postgres/config_repo.go` |

### P3 — 长期 / 待扫描

| ID | 任务 | 前置 |
|---|---|---|
| P2-6 verif. | **DEAD-VARIABLE-01** `golangci-lint --enable=unused,deadcode` 批量扫 | golangci-lint 配置 |
| X9 | **LINT-MODERN-01** 全仓 modernization baseline | — |
| — | 6 adapter 15 处 `t.Skip` 补齐 Testcontainer（backlog_later_detail §4） | — |
| — | spec debt：C-AC7 jti / C-L6 Contract ID 统一 / C-DC9 auditarchive S3 接线 / DURABLE-TYPE-01 | — |

---

## 三、功能（Features）

> 新端点 / 新能力 / 发布 / 文档 / 长期新模块。

### P1 — 近期

FEAT-1 DEVICE-LIST-API 主体已由 PR#221 交付；残余见「问题 P2 / Bug 残余」。

### P2 — 本版本内

| ID | 任务 | 工时 |
|---|---|---|
| F2 | **SYSTEM-TOPOLOGY-API** `GET /internal/v1/system/topology` | 4h 🟡 |
| F3 | **P2-T-02 audit e2e 测试** Journey 级验收 | 2h |
| F4 | Review cells/ | 4h |
| F5 | Review examples/ | 2h |
| F6 | Review 报告汇总 | 2h |
| F7 | 发布文档 | 4h |
| F8 | 性能基准 | 4h |
| F9 | **v1.0 tag** | — |

### P3 — 长期功能

| ID | 任务 | 前置 |
|---|---|---|
| X2 | **WM-35 BFF handler 接入 cookie session** | WM-2-F1 ✅ |
| X3 | **WM-36 SecureCookie key rotation** | X2 |
| X4 | **WM-7 泛型 BulkResult** | — |
| X5 | **P3-TD-11 accesscore domain 拆分** | X1 |
| S14a | **CONFIG-VALUE-KMS-AWS-PROVIDER-01** 🟠 明确云平台后 | — |

### v1.1+ 长期规划（`docs/backlog_later_detail.md`）

Kernel 子模块 / Metadata G-1~G-6 / Adapter 重整 / 契约增强 (BREAKING/CODEGEN/STUB) / WinMDM defer (WM-32/4/18) / WinMDM reject (WM-3/14/21/24/25/26/30/31/34b) / V2+ (WM-28/29, GAP-1~14)

---

## PR#220 遗留专项（`docs/202604232229-220-pr-issue-split-report.md`）

遗留 6 项已分到上面三层，建议按原报告推荐的 5 PR 顺序执行：

1. **PR1** 活动文档事实源重写 → 问题/P1/DOC-* × 2（PR220-1 / PR220-1b）
2. **PR2** 命名基线 + 文档 guard → 架构/P2/DOC-NAMING-GUARD（PR220-2）+ 问题/P1/NAMING-BASELINE-CONTRADICTION（PR220-e1）
3. **PR3** Journey verify fail-closed → 问题/P1/JOURNEY-VERIFY-FAIL-CLOSED（PR220-3）
4. **PR4** CI lint 事件语义拆分 → 架构/P2/CI-LINT-EVENT-SEMANTIC-SPLIT（PR220-4），可与 PR1/PR2 并行
5. **PR5** 运行时订阅身份语义收口 → 架构/P1/EVENTROUTER-SUBSCRIPTION-IDENTITY-SPLIT（PR220-5），PR3 后

优先级：若只做两项，选 PR1（对外认知）+ PR3（验收信号）。

---

## 决策记录（已与用户对齐）

1. **verification 14 条分级处理**：
   - **应回灌 backlog（单独 PR，本轮不改）**：A2 / A3 / A4 / A5 / A6 / A7 / A8 / A9 / A10 / A11 / A14(close-helper) / A15(cell-split) / A16(tx-helper) / A17(rolefetch) / A19 —— 架构、安全、兼容性类，保留 ID 追踪价值
   - **housekeeping 保留在 verification 报告，不入 backlog**：A12 DEPRECATED-SHIM-CLEAR / A13 KERNEL-GODOC-BACKFILL / A18 SLOG-ERROR-UNIFY —— 纯卫生类，一次性清理即可
2. **CI-01~CI-12 编号废止**：直接按现有 backlog ID（L7-ex / F10 / S* 等）追踪；CI-* 仅作历史来源记录，不在本整理视图出现
3. **本轮只产整理视图**：`docs/backlog.md` 保持不动；后续单独 PR 再做 backlog 同步（L10 核销 + P1-A / P1-19 / T6 / PR220-* 新增登记）

---

## 关键文件路径一览（按层）

- 治理/诊断：`kernel/governance/rules_fmt.go`、`kernel/governance/rules_examples.go`（新）、`cmd/gocell/app/`、`cmd/gocell/`
- 运行时：`runtime/bootstrap/bootstrap.go`、`runtime/http/health/`、`runtime/auth/`、`runtime/shutdown/`、`runtime/eventrouter/`
- 适配器：`adapters/vault/transit_provider.go`、`adapters/postgres/pool.go`、`adapters/redis/client.go`、`adapters/rabbitmq/connection.go`、`adapters/adapterutil/`（新）
- Cell：`cells/accesscore/cell.go`、`cells/configcore/cell.go`、`cells/accesscore/slices/rbaccheck/handler.go`、`cells/accesscore/internal/rolefetch/`（新）
- 合约：`contracts/http/config/get/v1/`
- Journey / 验证：`kernel/verify/`、`tests/integration/journey_test.go`、`journeys/status-board.yaml`、`journeys/J-ordercreate.yaml`
- 入口/示例：`cmd/corebundle/`、`examples/ssobff/`、`examples/iotdevice/README.md`
- 活动文档：`docs/design/capability-inventory.md`、`docs/design/capability-map.md`、`docs/architecture/naming-baseline.md`、`docs/architecture/module-dependency-report.md`、`docs/architecture/glossary.md`
- CI：`.github/workflows/ci.yml`、`.github/workflows/governance.yml`

## 验证方式

- 回溯 `docs/backlog.md` + verification 14 条 CONFIRMED + PR220 遗留 6 项 + plans 5 条未登记（P1-19 / T6 / P1-A / L8 / L10）+ reviews CI-01~CI-12 批次，确认无遗漏
- 与 `git log` PR#211..#222 核销对照
- backlog.md 同步点：L10 核销 + 新增 P1-A / P1-19 / T6 / PR220-* 登记（见待沟通第 3 项）
