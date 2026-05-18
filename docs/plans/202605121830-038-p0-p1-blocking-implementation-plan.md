# 038 P0/P1 阻塞项实施计划（独立于 034 accesscore 路线）

**生成日期**：2026-05-12
**最后更新**：2026-05-17 v2（状态回灌：**Wave 6 +1 — PR-W6-1 CONTRACT-TEST-GATE ✅ PR #543**（038 Wave 6 PR-W6-1 一并 ship 3 子项：A SCHEMAREF-FAILFAST-01 zero-code close + B ENDPOINT-TEST-MAPPING-01 governance rule + C PATH-QUERY-EXECUTABLE-01 contracttest 4 API + 全覆盖 23 contract + archtest GATE；衍生 Hard 升级 backlog cap-14 `CONTRACT-ENDPOINT-TEST-MAPPING-HARD-CODEGEN-01`）。**Wave 4 4/5 ship**（新增 ADAPTER-CONNECT-BUDGET-01 ✅ **PR #541**），剩 R-01+G-08 batch ⏳；**Wave 5 1/3 ship — BOOTSTRAP-CONTROL-PLANE-DECOUPLE-BUNDLE ✅ PR #531**，剩 G-10 / SEALED-MARKER-DEFENSE 束；**Wave 6 2/? — S3-FAILURE-INJECTION-01 ✅ PR #538 + PR-W6-1 ✅ PR #543**。Wave 1 8/8 / Wave 2 PR-11 ✅ 不变。**前一版**：2026-05-17 v1 仅 Wave 6 1/?（S3 #538）；2026-05-16 Wave 4 3/5；Wave 5 0/3；无 Wave 6。）
**关系**：
- [`docs/plans/202605082145-034-pg-corecell-b-route-plan.md`](202605082145-034-pg-corecell-b-route-plan.md)：accesscore PG 链（S3+S5/S3F/S4.0/S4a/S4b 已 ship；S4c 串行推进）。本计划不重复 accesscore 路线
- 本计划聚焦 backlog 中**未被 034 路线覆盖**的 P0/🔴 阻塞项 + 高密度可合并 P1，按依赖关系 + 文件物理重叠 + 同 ADR 概念模型三原则给合并决策

**触发**：用户 2026-05-12 要求把 P0/P1/🔴 项整理成实施计划，要求给合并决策的依据。

---

## 1. 本计划承担的范围

剔除 034 已覆盖的 accesscore 链（见 §3 引用）后，本计划覆盖以下 backlog 项：

**P0 + 🔴**：
- A-01 OIDC-FAILFAST-MR-COMPLETENESS 🔴 P0（含 A-07/A-08，cap-13）
- K-02 JOURNEY-LIFECYCLE-CI-CLOSE 🔴 P0（cap-14）
- GOVERNANCE-AUTH-PUBLIC-INTERNAL-FORBIDDEN 🔴 P1（cap-02）
- TEST-JOURNEY-ROOT-HARNESS-01 🔴（cap-14）
- JOURNEY-CONTRACT-EXISTENCE-VALIDATE-01 🔴（cap-14）
- V-A11 GOVERNANCE-EXAMPLES-COVERAGE-01 🔴（cap-14）

**P1 高密度可合并**：
- B2-R-05/06/07/08/09 OTel 5 件套（cap-13）
- B2-A-22/23/24 Prometheus 3 件套（cap-13）
- B2-X-05/06/07 gocell CLI 收口（cap-14）
- G-13 GOVERNANCE-RULES-REGISTRATION-GUARD（cap-02）
- AUTH-BOOTSTRAP-CLIENTS-MUTEX-01（cap-05，与 034 边界讨论见 §2 PR-7）
- REPO-HEALTHCHECKER-01 + B2-R-02（cap-13，backlog 注「同 PR」）

**P1 独立小 PR**（不合并）：
- OIDC-JWKS-ROTATION-WORKER-01（cap-05，依赖 A-01）
- ADAPTER-ERROR-CLASSIFICATION-TRANSIENT-01 🟠 P1（cap-x-cross）
- ADAPTER-CONNECT-BUDGET-01 🟡 P1（cap-x-cross）
- R-01 EVENT-OBSERVABILITY-METRIC-PACK + G-08 OUTBOX-FAILOPEN-COUNTER（同 batch ship 但分 PR review）
- C-02 CONFIGSUBSCRIBE-CACHE-LIFECYCLE
- STARTUP-ROLLBACK-ERR-JOIN-01

**架构性大重构**（Wave 4 独立排期）：
- G-10 KERNEL-CELL-PACKAGE-DECOMPOSE（与 PR-A22 协同）
- SEALED-MARKER-DEFENSE-EXPANSION-BUNDLE（已是束，7 子条独立排期）
- BOOTSTRAP-CONTROL-PLANE-DECOUPLE-BUNDLE（已是束，3 子条）

---

## 2. PR 合并决策（每条带依据）

### ✅ 合并 PR

#### PR-1 PR-OTEL-HARDEN-5（合并 5 个 P1）— ✅ shipped as PR #486 (PR-OTEL-HARDEN-4)

**包含**：B2-R-05 / B2-R-06 / B2-R-07 / B2-R-08 / B2-R-09
**依据**：5 文件全部在 `adapters/otel/`（metric_provider.go / tracer.go / pool_collector.go），同一 adapter 的 hardening 主题
**Cx**：Cx3
**ship 摘要（PR #486, 2026-05-13）**：
- 实际 4 of 5 子项落地（B2-R-06/07/08/09）。**B2-R-05 在 PR 内 split 出 `METRICS-CTX-FUNNEL-01`**：原 backlog 提议「ctx 透传」无法在 adapters/otel 内独立实现，kernel `metrics.Counter/Histogram` 接口故意省略 ctx（Prom 形态 label-binding 覆盖 label 维度），对齐 OTel 原生 ctx-bearing 形态需要 Cx4 跨 adapters/prometheus + kernel/outbox + runtime/http + kernel/wrapper 接口重构 → 离开 adapter scope
- B2-R-06: `NewTracer` 末尾注册全局 TracerProvider + CompositeTextMapPropagator(TraceContext, Baggage)；错误路径不污染全局
- B2-R-07: `defaultShutdownTimeout=10s` + `shutdownTracerProvider(ctx, tp, timeout)` helper（rationale: 5s 初版在 BSP shutdown race，10s 稳定，仍小于 OTel BSP ExportTimeout 30s 默认）
- B2-R-08: 删 public `RegisterPoolMetrics`，单一出口 `NewPoolMetricsResource(meter, statters) (lifecycle.ManagedResource, error)`；compile-time Hard 守卫 + Close → registration.Unregister
- B2-R-09: `attrCache.maxSize=2000` cap-and-overflow（非 LRU）+ `overflowOpt` sentinel，包外类型系统不可达（AI-rebust Hard）

#### PR-2 PR-PROM-HARDEN-3（合并 3 个 P1）— ✅ shipped as PR #484

**包含**：B2-A-22 / B2-A-23 / B2-A-24
**依据**：`cmd/corebundle/metrics.go` + `adapters/prometheus/{hook_observer.go,metric_provider_test.go}`，Prometheus 出口面 hardening
**Cx**：Cx2
**ship 摘要（PR #484, 2026-05-13）**：
- B2-A-22: `/metrics` handler 加 `promhttp.HandlerOpts.Timeout(10s)` + `MaxRequestsInFlight(3)` + self-instrument Registry
- B2-A-23: cell-id no-dash invariant 上提至 Hard — `cell.schema.json ^[a-z][a-z0-9]+$` + `metadata.CellIDPattern` 单源 + `MatchCellID` helper；新增 `adapters/prometheus/cell_label.go` `promCellLabel` typed-function funnel + archtest `PROM-CELL-LABEL-FUNNEL-01`
- B2-A-24: MetricProvider/HookObserver race tests + `adapters/prometheus` 纳入 `.github/workflows/test-race.yml`
- **review 升级**：archtest 由字符串名匹配升至 `*types.Info.Uses` 包路径解析（type-aware Hard，charter §4 Wave 2 范式）

#### PR-3 PR-CLI-HARDEN（合并 3 个 P1）

**包含**：B2-X-05 / B2-X-06 / B2-X-07（+ root-cause 升级 CLI-UNIMPL-HIDE-01 闭环 Hard，+ 派生 follow-up CLI-TOPLEVEL-HELP-REGISTRY-01）
**依据**：`cmd/gocell/app/{generate.go,verify.go,dispatch.go,main.go}`，CLI 入口治理（unimpl 隐藏 / verify ctx / signal ctx）
**Cx**：Cx2-Cx3
**ship 摘要（PR #502 CLI-HARDEN, 2026-05-15）**：
- **L3 根因升级（用户裁决）**：原计划仅 generate 单树 → 发现 4 棵子命令树（generate/verify/scaffold/check）同为 `switch args[0]` + 手写 `printXxxHelp` helpEntry 双源漂移结构；仅修 generate 是修实例不修根因类。统一为泛型 `subcommand[H]` typed registry 单源（`cmd/gocell/app/subcommand.go`），删 4 switch + 4 手写 helpEntry 列表；`subNames/findSub/renderSubHelp` 共享 dispatch/help 派生
- B2-X-05：`generate indexes`（V2.1 C18 废弃概念，V3 由 export/graph 取代）彻底移除，无 `[experimental]` 软标签；落 unknown-type
- B2-X-06：`runVerify(ctx)` + 4 处 `context.Background()`→形参；下层已 ctx-aware，全链路取消闭环
- B2-X-07：(b) `main.go signal.NotifyContext` + `Dispatch(ctx,args)` + `commands` map ctx 化 + 7 子命令统一 ctx；`context.Canceled`→`interrupted`/ExitRuntime；`TestDispatch_CanceledContext` 回归
- CLI-UNIMPL-HIDE-01：archtest `tools/archtest/cli_unimpl_hide_test.go`（Pass-Driver `archtest.Run`，不进 LegacyAllowlist）全 `cmd/gocell/app` 强制上游 Hard（4 dispatch 无 switch + 必 findSub）+ 下游 Hard（无 string-literal name helpEntry）+ 无 `not implemented` 字面量 + 4 项反向 fixture 自检；declared blind spot（顶层 PrintUsage prose）补偿断言 + 显式 backlog `CLI-TOPLEVEL-HELP-REGISTRY-01`（非 silent carryover）
- ctx 透传逐个核实非假设：validate/verify/generate-metricschema 真透传；scaffold/check/graph/export 无 cancelable 下游（depgraph.Load 非 ctx-native 等），统一形参 + godoc 注明

#### PR-4 PR-JOURNEY-LIFECYCLE-GOV（合并 3 个 P0/🔴）— ✅ shipped as PR #520

**包含**：K-02 + JOURNEY-CONTRACT-EXISTENCE-VALIDATE-01 + JOURNEY-STATUS-BOARD-LIFECYCLE-CONSISTENCY-01
**依据**：K-02 (c) 与 JOURNEY-CONTRACT-EXISTENCE 是同一规则的两种描述；3 项都改 `kernel/governance/rules_journey.go` + `journeys/J-*.yaml` + `kernel/verify/`
**依赖**（v5 2026-05-14 复核）：
- PR-6 (G-13) ✅ PR #487 已落：新 rule 注册直接走 `kernel/governance/rulecodes.go` 单源 + `validateJourney*()` 方法范式 + SeverityError `; fix:` 后缀（参照 ADV-06）；无 rebase 成本
- 040 阶段 1 ✅ PR #492 已落（2026-05-14）：journey YAML 完整性 / lifecycle 守护新增 archtest 走 `archtest.Run` / `archtest.RunTyped` 入口，**不进** LegacyAllowlist
**Cx**：Cx2-Cx3
**ship 摘要（PR #520，2026-05-16）**：
- K-02 (a): `journeys/J-ssologin.yaml` lifecycle: experimental → active；新建 `tests/integration/journey_ssologin_session_db_test.go::TestJSsologinSessionDb`（in-memory session.MemStore + storetest 同源 fixture）实现 `journey.J-ssologin.session-db` checkRef 闭环
- K-02 (b): `kernel/verify/runner.go::RunActiveJourneys` 非 nil project + activeCount=0 时 fail + `; fix:` 后缀；`TestRunActiveJourneys_SkipsInactiveJourneys` 重命名 `TestRunActiveJourneys_EmptyActiveSetFails` 并改断言为 Passed=false
- K-02 (c) / JOURNEY-CONTRACT-EXISTENCE-VALIDATE-01: 新建 `kernel/governance/rules_journey.go` `validateJOURNEYCONTRACTEXISTENCE01`（反向 REF-07，active 非 examples/ 平台 contract 必须被至少 1 个 journey.contracts[] 引用）；扇出 5 个平台 journey 扩 contracts[] 覆盖 37 个原孤立 active contract（user/role mgmt → J-useronboarding；lock/unlock → J-accountlockout；audit list → J-auditlogintrail；config CRUD + flags → J-confighotreload；config rollback → J-configrollback；setup admin/status + change-password → J-ssologin）
- STATUS-BOARD-LIFECYCLE-CONSISTENCY: `validateJOURNEYSTATUSLIFECYCLE01` `validBoardLifecycleMatrix` 三态强映射（todo→{experimental} / doing→{experimental,active} / done→{active}）+ active+doing Warning（in-transit reminder, 非阻断）；矩阵与 rules_fmt.go validJourneyLifecycles + journey.schema.json enum 单源对齐，不引入 stable phantom state
- 顺带修 `validateADV01` 加 examples/ 豁免（对齐 CONTRACT-CONSISTENCY-EMIT-01 已有 examples-exempt 模式）；删 platform `journeys/status-board.yaml` 中 J-ordercreate orphan entry（J-ordercreate 是 example journey 不应在 platform board）
- AI-rebust Medium（INV-1/2/3 自动守）；同 PR 显式登记两条 Hard 升级 backlog：`JOURNEY-METADATA-STATE-LIFECYCLE-TYPED-CONST-01`（P2/Cx3 🟠）+ `JOURNEY-CONTRACT-EXISTENCE-CODEGEN-DERIVE-01`（P3/Cx4 🟢）；rules_journey.go godoc 点名两个 backlog ID（参照 cap-14 PASS-PRODUCTION-UPSTREAM-HARD-01 / USAGE-02-HARD-UPGRADE-01 范式）
- 验证：`gocell validate` 0 errors / 1 warning（J-ssologin active+doing 提醒）；`gocell verify journey --active` 双 active journey (J-ssologin platform + J-ordercreate example) 自动 checkRef 全 PASS

#### PR-5 PR-GOV-NEW-RULES（合并 2 个 governance 新规则，PR-6 ✅ 阻塞解除）

**包含**：GOVERNANCE-AUTH-PUBLIC-INTERNAL-FORBIDDEN + V-A11 GOVERNANCE-EXAMPLES-COVERAGE-01
**依据**：都在 `kernel/governance/`（rules_fmt.go / rules_examples.go 兄弟文件），都是新增 rule，PR-6 注册框架已落
**依赖**（v5 2026-05-14 复核）：
- PR-6 (G-13) ✅ PR #487 已落：直接用 `rulecodes.go` + `validateXxx()` + `; fix:` 后缀范式
- 040 阶段 1 ✅ PR #492 已落（2026-05-14）：V-A11 EXAMPLES-COVERAGE 新增 archtest 走 `archtest.Run` / `archtest.RunTyped`，不进 LegacyAllowlist
**合并决策**（v4 2026-05-14 收口）：**保持合并**——两条 governance 兄弟规则（rules_fmt.go / rules_examples.go），文件物理重叠 + 同 ADR 概念（governance 规则注册框架），单 PR review 合理；PR-6 落后回看结论：注册框架 ship 后两条挂入路径完全一致，无拆分必要
**Cx**：Cx2

#### PR-6 G-13 GOVERNANCE-RULES-REGISTRATION-GUARD（独立 P1）— ✅ shipped as PR #487

**包含**：仅 G-13 单条
**依据**：元治理框架（archtest 反射枚举 + `ValidateStrict` 单入口 + 提取 `rulecodes.go`），PR-5 的前置
**改动**：`kernel/governance/{rules.go,rulecodes.go(新)}` + `tools/archtest/`
**Cx**：Cx2
**ship 摘要（PR #487, 2026-05-13）**：
- governance: 抽 `kernel/governance/rulecodes.go` 单源 rule code + 138 行新文件；`ValidateStrict(strict, failFast bool)` 合并双入口；SeverityError 规则全部追加 `; fix: ...` 后缀（参照 ADV-06 范式）
- archtest: `tools/archtest/governance_rules_invariants_test.go` 反射枚举注册一致性 (INV-1) + ValidationResult Code/Message 完整性 (INV-2) + SeverityError fix 后缀 (INV-3)，共 11 RED fixtures（governance_{registration_guard,rulecode_single_source,fix_anchor}_fixtures/）
- review 派生：新增 `typeseval.EachFileInPackage(root, pkg, skipTestFiles, fn)` 同源遍历 helper（消除 scanner.EachFile + pkg.TypesInfo 混用入口，对标 go/analysis Pass）；4 follow-up 登记 cap-02（G-13-FU-INV1-SAMESOURCE ✅ / -H2 / -H3 / -H1-REJECTED）
- 派生新 plan：`docs/plans/202605141519-040-archtest-pass-funnel-plan.md`（archtest.Pass + Run/RunTyped 范式收口 INV-1 双入口问题，4 阶段 ~10 PR）
- merge 后解除 PR-5 ship 阻塞

#### PR-7 AUTH-BOOTSTRAP-CLIENTS-MUTEX-01（独立判断）— ✅ shipped as PR #483

**包含**：仅 AUTH-BOOTSTRAP-CLIENTS-MUTEX-01
**改动**：`runtime/auth/route.go:validateBypassCompatibility` + 测试矩阵
**与 034 边界**：034 S4a 不动 route.go，无 file 冲突
**Cx**：Cx2
**ship 摘要（PR #483, 2026-05-13）**：
- runtime: `validateBypassCompatibility` 加 4th mutex branch — BootstrapAuth (HTTP Basic Auth via env, FMT-28 `/api/v1/*/setup/admin`) 与 Contract.Clients (service-token caller-cell allowlist, FMT-31 `/internal/v1/*`) 互斥
- archtest: `TestAuthRouteBootstrapClientsMutex` 静态扫描整仓 `auth.Route` composite literal（含 `generated/`）
- 矩阵测试覆盖 `{Public, PasswordResetExempt, BootstrapAuth, Policy, Contract.Clients}` 全 pairwise + singleton + 触发顺序断言
- **review 升级**：archtest type-aware Hard 全覆盖 4 个 Contract-expression 形态（file-scope var / inline literal / func-body-local := / cross-package SelectorExpr，0 KNOWN-GAP）
- 文件重命名 `setup_admin_bootstrap_closure_test.go → auth_bootstrap_invariants_test.go`（ai-collab.md theme-file 范式，≥3 同主题 invariant）

#### PR-8 PR-OIDC-MR-COMPLETENESS（A-01 含 A-07/A-08 束）— ✅ shipped as PR #485

**包含**：A-01 OIDC-FAILFAST-MR-COMPLETENESS（backlog cap-13 line 41 已聚合为束）
**子项**：(1) OIDC 同步 discover；(2) 4 adapter (postgres/redis/s3/oidc) Checkers；(3) s3 状态机 + 后台 health-check；(4) archtest MANAGED-RESOURCE-COMPLETENESS-01；(5) postgres.Pool 升 ManagedResource；(6) `adapters/adapterutil/`(新) helper
**依据**：backlog 已聚合束；4 adapter 同时升 MR 才能挂 archtest 完整性闸门，缺一不可
**Cx**：Cx3-Cx4
**ship 摘要（PR #485, 2026-05-13）**：
- (1) `oidc.New(ctx, cfg)` 同步 discover (`force=true`)，unreachable issuer fail-fast at boot；OIDC Adapter 直接实现 lifecycle.ManagedResource
- (2-5) 4 adapter 全部直接实现 ManagedResource：postgres (drop PGResource wrapper) / redis (collapse WithHealthChecker+WithManagedCloser to single WithManagedResource) / s3 (+250 行状态机 + 后台 health-check goroutine) / oidc (Refresh API 保留给 PR-11 worker)
- (4) archtest `MANAGED-RESOURCE-COMPLETENESS-01`（rename from MANAGED-RESOURCE-CONTRACT-01）+ drop 4 opt-outs
- (6) `adapters/adapterutil/health.go` `HealthToCheckers` helper 下沉 4 adapter 复用
- **unblock**：PR-11 OIDC-JWKS-ROTATION-WORKER 前置依赖已达成（commit body 显式："auto-rotation worker is PR-11/A-02"）

#### PR-9 PR-REPO-READYZ — ✅ shipped as PR #506

**包含**：REPO-HEALTHCHECKER-01 + B2-R-02
**依据**：backlog 主表 cap-12 line 225 显式注「同 PR」；都改 `cells/{configcore,auditcore}/cell.go` HealthCheckers
**Cx**：Cx2 → Cx3（范围扩展：typed funnel + real-failure conformance + archtest）
**ship 摘要（PR #506，2026-05-16）**：
- 新增 `kernel/cell.RepoHealthProber` interface + `cell.RegisterRepoReadiness(reg, name, prober)` typed funnel（Hard form-uniqueness，对标 `panic(panicregister.Approved(...))` 范本）
- 3 cell 统一注册：configcore `config_repo_ready`（queries `config_entries` + `feature_flags`）/ accesscore `session_store_ready`（queries `sessions`）/ auditcore `audit_ledger_ready`（复用 `Tail`，queries `audit_entries`）
- accesscore dead-code duck-type probe 修复（匿名 `interface{ Health(context.Context) error }` 从未触发，本 PR 替换为有类型 funnel wiring）
- auditcore `LedgerStore.Probes()` 特殊路径删除，统一到 funnel（PR #450 F6 部分覆盖已吸收）
- `kernel/cell/celltest.RunRepoReadinessConformance` real-failure-injection harness（healthy→nil；PG DROP TABLE→non-nil；mem→skip）作为 differentiated probe 行为正确性的 Hard 载体
- archtest `CELL-REPO-READYZ-PROBE-01`：funnel 形态锁 + conformance wiring Medium backstop
- Cx2→Cx3 revision note：AI-rebust self-audit 发现需要三件套（typed funnel + conformance + archtest），不是原估的两项
- ADR `docs/architecture/202605161030-adr-cell-repo-readyz-probe.md`

---

## 3. 依赖图与执行 Wave

```
Wave 1（独立并行，8 PR） — 8/8 ship：
  PR-1 OTEL-HARDEN-5         ✅ PR #486 (OTEL-HARDEN-4，B2-R-05 split)
  PR-2 PROM-HARDEN-3         ✅ PR #484
  PR-3 CLI-HARDEN            ✅ PR #502 (038 Wave 1, 2026-05-15) — +CLI-UNIMPL-HIDE-01 闭环 Hard 升级
  PR-4 JOURNEY-LIFECYCLE-GOV ✅ PR #520 (2026-05-16) — JOURNEY rule 系列新建 + Hard upgrade backlog 登记
  PR-6 G-13 元治理 guard     ✅ PR #487 merged 2026-05-13
  PR-7 BOOTSTRAP-CLIENTS-MUTEX ✅ PR #483
  PR-8 OIDC-MR-COMPLETENESS  ✅ PR #485
  PR-9 REPO-READYZ           ✅ PR #506 (2026-05-16) — +CELL-REPO-READYZ-PROBE-01 Hard funnel

Wave 2（依赖 Wave 1，2 PR） — 1/2 ship：
  PR-11 OIDC-JWKS-ROTATION-WORKER-01 ✅ PR #504 (2026-05-16) — periodic re-discovery worker + refresh metric (A-02)
  PR-5 GOV-NEW-RULES (GOVERNANCE-AUTH-PUBLIC + V-A11)
       ↑ 依赖 PR-6 ✅；**前置已达成，可立即排期**（Wave 2 唯一遗留）

Wave 3（依赖 Wave 1）：
  TEST-JOURNEY-ROOT-HARNESS-01      ← 依赖 PR-4 ✅ #520 → 前置已达成，可起（harness 本体未启动）

Wave 4（独立小 PR，触发型 / 与上面 wave 并行不冲突） — 4/5 ship：
  R-01 + G-08 同 batch（分 PR review）              ⏳ 未起
  C-02 CONFIGSUBSCRIBE-CACHE-LIFECYCLE              ✅ PR #518 (035 PR-CFG-CACHE-LIFECYCLE)
  STARTUP-ROLLBACK-ERR-JOIN-01                      ✅ PR #499
  ADAPTER-ERROR-CLASSIFICATION-TRANSIENT-01         ✅ PR #517
  ADAPTER-CONNECT-BUDGET-01                         ✅ PR #541 (Wave-4-B, 2026-05-17) — PG/RMQ ConnectTimeout 码 + RMQ classifyConnectError funnel + Health/AcquireChannel explicit-close 不对称同 PR 闭环（ErrAdapterAMQPClosed + c.closed 前置）

Wave 5（架构性重构，独立排期，不阻塞发布） — 1/3 ship：
  G-10 KERNEL-CELL-PACKAGE-DECOMPOSE
  SEALED-MARKER-DEFENSE-EXPANSION-BUNDLE (7 子条)
  BOOTSTRAP-CONTROL-PLANE-DECOUPLE-BUNDLE (3 子条) ✅ PR #531 (2026-05-17) — C.1 时钟二分 / C.2 OwnerCtx / C.3 sweep 可观测；ADR 202605170000 supersede 202605102000 §D1/§D3

Wave 6（cap-x-cross 触发型小 PR 簇 / 发布前 contract 测试门禁簇）：
  PR-W6-1 CONTRACT-TEST-GATE                        ✅ PR #543 (2026-05-17) — 3 子项一并 ship (激进自审 v2 收口)：
                                                       (A) SCHEMAREF-FAILFAST-01 zero-code close（行为已 RED 锁于 mockTB 反向测）；
                                                       (B) ENDPOINT-TEST-MAPPING-01 governance rule (kernel/governance/rules_contract_test_mapping.go, SeverityError + ; fix: 后缀, ADV-06 同形态) — 反向校验 33 个 active HTTP contract 全部已合规，0 slice.yaml 修补；
                                                       (C) PATH-QUERY-EXECUTABLE-01 contracttest 新 4 API (ValidatePathParam/MustRejectPathParam/ValidateQueryParam/MustRejectQueryParam) + 全覆盖 23 个声明 path/queryParams 的 active HTTP contract (10 contract_test.go 补 rejected 用例) + archtest `CONTRACT-PATH-QUERY-COVERAGE-01` Medium GATE (RunTypedProduction + scanner.EachContentFile + 反向自检 fixture)。
                                                       衍生 Hard 升级 backlog cap-14 `CONTRACT-ENDPOINT-TEST-MAPPING-HARD-CODEGEN-01`（rule godoc 已点名，非 silent carryover）。
  S3-FAILURE-INJECTION-01                           ✅ PR #538 (2026-05-17) — MinIO testcontainer + transport mock 故障注入；Upload/Health/Worker × {403/5xx/timeout/recovery} 11 单元 + 3 集成；ReadyProbeName const 单源；衍生 backlog S3-CLASSIFYERROR-CONN-REFUSED-01 + OPS-CONTRACT-STRING-FUNNEL-01

外部进行中（不在本计划内但相关）：
  034 S4a ✅ PR #482 → S4b ✅ PR #490 → S4c (accesscore PG 链，串行；S4a/S4b 已 ship)
  040 archtest Pass-Driver           (PR #487 review 派生 plan，4 阶段 ~10 PR；
                                      阶段 1 ✅ PR #492 (2026-05-14)，PR-4/PR-5/Wave 4
                                      ADAPTER-ERR-CLASS 阻塞解除；阶段 2/3 业务 archtest
                                      主题分批迁移可起，与 038 剩余文件域互斥可并行)

新增条目（038 内 ship 衍生，不另开 backlog 主线）：
  METRICS-CTX-FUNNEL-01  ← 由 PR #486 (PR-1) 同 PR split 出，Cx4 跨层；backlog cap-13 line 21 已登记
                          (kernel metrics 接口 ctx-bearing 化，影响 prometheus + otel + outbox + http + wrapper)
```

### 文件冲突核查（Wave 1 真并行）

| PR pair | 共享路径 | 冲突 |
|---|---|---|
| PR-1 vs PR-2 | 都有 cmd/corebundle | PR-1 不动 corebundle；PR-2 仅 metrics.go → ✅ 不冲突 |
| PR-4 vs PR-6 ✅ | 都有 kernel/governance | PR-6 ✅ PR #487 已落，PR-4 直接用 rulecodes.go + validateJourney*() + ; fix: 后缀 → ✅ 已解锁 |
| PR-4 / PR-5 / Wave 4 ADAPTER-ERR-CLASS vs 040 阶段 1 ✅ | tools/archtest/* | 040 阶段 1 ✅ PR #492 已落，三者新增 archtest 走 `archtest.Run`/`RunTyped` 入口（不进 LegacyAllowlist），与 040 阶段 2/3 文件域互斥可并行 → ✅ 已解锁 |
| PR-4 vs PR-3 | 都可能涉 cmd/gocell | PR-4 不动 cmd/gocell；PR-3 仅 cmd/gocell/app → ✅ 不冲突 |
| 所有 PR vs 034 S4a | 034 S4a 在 cmd/corebundle/access_module.go + cells/accesscore | 与本计划全部 PR 文件域互斥 → ✅ 不冲突 |
| PR-7 vs 034 S4a | 都含 runtime/auth | PR-7 仅 route.go validateBypassCompatibility；034 S4a 不动 route.go → ✅ 不冲突 |

---

## 4. 工时粗估

| PR | dev | review | 状态 | 备注 |
|---|---|---|---|---|
| PR-1 OTEL-HARDEN-5 | 8h | 4h | ✅ PR #486 | 实际 4 of 5（B2-R-05 split → METRICS-CTX-FUNNEL-01） |
| PR-2 PROM-HARDEN-3 | 4h | 2h | ✅ PR #484 | +review Hard funnel 升级 |
| PR-3 CLI-HARDEN | 8h | 4h | ✅ PR #502 (2026-05-15) | 3 项 + L3 根因升级 CLI-UNIMPL-HIDE-01 闭环 Hard（4 树统一 registry）+ follow-up CLI-TOPLEVEL-HELP-REGISTRY-01 |
| PR-4 JOURNEY-LIFECYCLE-GOV | 10h | 4h | ✅ PR #520 | K-02 三子项 (a)(b)(c) + 2 新 governance rule (JOURNEY-CONTRACT-EXISTENCE-01 + JOURNEY-STATUS-LIFECYCLE-01) + ADV-01 examples 豁免 + 5 journey 扇出扩 contracts[] + integration test for session-db checkRef + 2 Hard 升级 backlog 同 PR 登记 |
| PR-6 G-13 元治理 guard | 6h | 3h | ✅ PR #487 | 注册框架；review 派生 plan 040 archtest Pass-Driver；4 follow-up 登记 cap-02 |
| PR-7 BOOTSTRAP-CLIENTS-MUTEX | 3h | 1.5h | ✅ PR #483 | +review type-aware Hard 全形态覆盖 |
| PR-8 OIDC-MR-COMPLETENESS | 18h | 8h | ✅ PR #485 | A-01 + A-07 + A-08 束 |
| PR-9 REPO-READYZ | 4h | 2h | ✅ PR #506 | typed funnel + conformance harness + 3-cell unification；Cx2→Cx3 |
| PR-11 OIDC-JWKS-ROTATION-WORKER（依赖 PR-8 ✅） | 4h | 2h | ✅ PR #504 | periodic re-discovery worker + refresh metric (A-02)；PR-8 unblock |
| PR-5 GOV-NEW-RULES | 4h | 2h | ⏳ 可起（040 阶段 1 ✅ PR #492 解锁）| PR-6 ✅；保持合并；V-A11 archtest 走 `archtest.Run`/`RunTyped`（Wave 2 唯一遗留）|
| Wave 3 TEST-JOURNEY-ROOT-HARNESS-01 | 8h | 4h | ⏳ 可起（PR-4 ✅ #520 解锁）| integration harness |
| Wave 4 小 PR 合计（5 项，精算） | ~27h | ~13.5h | 🟡 4/5 | ✅ **ADAPTER-ERR-CLASS PR #517** / **C-02 PR #518**（035 PR-CFG-CACHE-LIFECYCLE）/ **STARTUP-ROLLBACK PR #499** / **ADAPTER-CONN-BUDGET PR #541**；剩 1 项 ⏳：R-01+G-08 batch 10h+5h |
| Wave 5 架构重构 | 独立排期 | — | 🟡 1/3 | ✅ BOOTSTRAP-CONTROL-PLANE-DECOUPLE-BUNDLE PR #531；剩 G-10 / SEALED 束 |
| Wave 6 cap-x-cross 触发型 + 发布前 contract 测试门禁簇 | 独立排期 | — | 🟡 2/? | ✅ S3-FAILURE-INJECTION-01 PR #538；✅ PR-W6-1 CONTRACT-TEST-GATE PR #543（A/B/C 三子项一并 ship + Hard 升级 backlog 同 PR 登记）|

**累计**：
- ✅ shipped (9 Wave1 PR + Wave 2 PR-11 + Wave 4 4 项 + Wave 5 1 项 + Wave 6 1 项): ~87h dev / ~42h review（PR-1/2/3/4/6/7/8/9/11 + Wave 4: ADAPTER-ERR-CLASS #517 / C-02 #518 / STARTUP-ROLLBACK #499 / ADAPTER-CONN-BUDGET #541 + Wave 5: #531 + Wave 6: #538）
- ⏳ 待启动 (Wave 2 剩余 + Wave 3 + Wave 4 剩余): ~22h dev / ~11h review（PR-5 + Wave 3 TEST-JOURNEY-ROOT-HARNESS + Wave 4: R-01+G-08）
- 进度：Wave 1 8/8 ship（100%）；Wave 2 1/2 ship；Wave 4 4/5 ship；Wave 5 1/3 ship；038 整体 dev 进度 ~82%（按原计划 102h 总分母）

---

## 5. 决策点

1. **Wave 1 8 PR 真并行**：文件冲突已核查，所有 PR 间无 rebase 阻塞（含 040 阶段 1 ✅ 解锁）
2. **PR-5 保持合并**（GOVERNANCE-AUTH-PUBLIC + V-A11）：rules_fmt.go / rules_examples.go 兄弟规则，注册框架 (PR-6 ✅) ship 后挂入路径一致
3. **R-01 / G-08 不合并**，分 PR review（Wave 4 小 PR）
4. **架构重构 Wave 5 独立排期**，不进本计划主线
5. **METRICS-CTX-FUNNEL-01 不纳入本计划**：Cx4 跨 5+ 包接口重构，登记 cap-13 走触发型
6. **下一波建议（2026-05-16 回灌后）**：Wave 1 8/8 + Wave 2 PR-11 + Wave 4 3/5（#517/#518/#499）已 ship；剩 **PR-5 GOV-NEW-RULES**（Wave 2 唯一遗留，前置 PR-6 ✅ + 040 阶段 1 ✅ 全解锁，随时可起）+ **Wave 3 TEST-JOURNEY-ROOT-HARNESS-01**（PR-4 ✅ #520 解锁）+ **Wave 4 剩 2 项**（R-01+G-08 batch / ADAPTER-CONNECT-BUDGET-01，触发型小 PR）
7. **plan 040 与 038 并行**：阶段 2/3 重写 `tools/archtest/*_test.go` 自身，与 038 业务 PR 文件域互斥

---

## 6. 引用

- [`docs/plans/202605082145-034-pg-corecell-b-route-plan.md`](202605082145-034-pg-corecell-b-route-plan.md)：accesscore PG 链（S4a/S4b/S4c 串行未 ship）
- [`docs/backlog.md`](../backlog.md) + 4 子表：本计划承担项的 backlog 来源
- [`docs/plans/archive/202605121750-037-wave4-advance-plan.md`](archive/202605121750-037-wave4-advance-plan.md)：036 charter wave 4 触发型 3 条提前推进（独立于本计划，已归档）
