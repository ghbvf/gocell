# 038 P0/P1 阻塞项实施计划（独立于 034 accesscore 路线）

**生成日期**：2026-05-12
**最后更新**：2026-05-14（v4：剩余 PR 按 040 依赖分流 — 7 PR 独立于 040 可立即起 / 3 PR 等 040 阶段 1 (PR-4/PR-5/Wave 4 ADAPTER-ERR-CLASS)；v3 #5/#7 决策过时已标注；PR-5 合并决策收口（保持合并）。Wave 1 5/8 ship — PR-1/PR-2/PR-6/PR-7/PR-8 全部 closed；PR #487 review 派生 plan 040 archtest Pass-Driver 范式）
**关系**：
- [`docs/plans/202605082145-034-pg-corecell-b-route-plan.md`](202605082145-034-pg-corecell-b-route-plan.md) 已覆盖 accesscore PG 链（S3+S5/S3F/S4.0/**S4a** 已 ship by PR #482；S4b→S4c 串行推进）。**本计划不重复 accesscore 路线**，仅引用 034 S4b/S4c 作为下游闭环
- 本计划聚焦 backlog 中**未被 034 路线覆盖**的 P0/🔴 阻塞项 + 高密度可合并 P1，按依赖关系 + 文件物理重叠 + 同 ADR 概念模型三原则给合并决策

**触发**：用户 2026-05-12 要求把 P0/P1/🔴 项整理成实施计划，要求给合并决策的依据，不能没分析就合并。

**v2 进度同步（2026-05-13）**：自 baseline `8d213883` 起 develop 上 5 个 merge 中 4 个属本计划（5th 是 034 S4a）：
- ✅ PR-1 → PR #486 (OTEL-HARDEN-4，4 of 5；B2-R-05 因 kernel `metrics.Counter/Histogram` 接口故意省略 ctx → 同 PR 内 split 出 `METRICS-CTX-FUNNEL-01` Cx4 跨层条目)
- ✅ PR-2 → PR #484 (含 review 升 Hard funnel `PROM-CELL-LABEL-FUNNEL-01`)
- ✅ PR-7 → PR #483 (含 review type-aware 升 Hard，覆盖 4 个 Contract-expression 形态)
- ✅ PR-8 → PR #485 (4 adapters 直接 ManagedResource + adapterutil helper + archtest gate + s3 状态机)
- ✅ PR-6 → PR #487 merged 2026-05-13 (archtest 反射枚举 + ValidateStrict 单入口 + rulecodes.go + `; fix:` 后缀 + INV-1/2/3 同源遍历 `typeseval.EachFileInPackage` helper + 4 follow-up entries 登记 cap-02)；review 派生 plan 040 archtest Pass-Driver 入口收口范式
- ⏳ PR-3 / PR-4 / PR-5 / PR-9 / PR-11 / Wave 3 / Wave 4 未启动；PR-5 (GOV-NEW-RULES) 现解除 PR-6 阻塞，可起

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

**包含**：B2-X-05 / B2-X-06 / B2-X-07
**依据**：`cmd/gocell/app/{generate.go,verify.go,dispatch.go,main.go}`，CLI 入口治理（unimpl 隐藏 / verify ctx / signal ctx）
**Cx**：Cx2

#### PR-4 PR-JOURNEY-LIFECYCLE-GOV（合并 3 个 P0/🔴）

**包含**：K-02 + JOURNEY-CONTRACT-EXISTENCE-VALIDATE-01 + JOURNEY-STATUS-BOARD-LIFECYCLE-CONSISTENCY-01
**依据**：K-02 (c) 与 JOURNEY-CONTRACT-EXISTENCE 是同一规则的两种描述；3 项都改 `kernel/governance/rules_journey.go` + `journeys/J-*.yaml` + `kernel/verify/`
**依赖**（v4 2026-05-14 复核）：
- PR-6 (G-13) ✅ PR #487 已落：新 rule 注册直接走 `kernel/governance/rulecodes.go` 单源 + `validateJourney*()` 方法范式 + SeverityError `; fix:` 后缀（参照 ADV-06）；无 rebase 成本
- **建议等 040 阶段 1**：journey YAML 完整性 / lifecycle 守护高概率新增 archtest 文件，等 Pass-Driver 范式落地后走新入口，避免新文件进 LegacyAllowlist 后再二次重写
**Cx**：Cx2-Cx3

#### PR-5 PR-GOV-NEW-RULES（合并 2 个 governance 新规则，PR-6 ✅ 阻塞解除）

**包含**：GOVERNANCE-AUTH-PUBLIC-INTERNAL-FORBIDDEN + V-A11 GOVERNANCE-EXAMPLES-COVERAGE-01
**依据**：都在 `kernel/governance/`（rules_fmt.go / rules_examples.go 兄弟文件），都是新增 rule，PR-6 注册框架已落
**依赖**（v4 2026-05-14 复核）：
- PR-6 (G-13) ✅ PR #487 已落：直接用 `rulecodes.go` + `validateXxx()` + `; fix:` 后缀范式
- **建议等 040 阶段 1**：V-A11 EXAMPLES-COVERAGE 倾向新 archtest 文件守 examples cell/contract 完整性，走 Pass-Driver 入口避免二次返工
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

#### PR-9 PR-REPO-READYZ

**包含**：REPO-HEALTHCHECKER-01 + B2-R-02
**依据**：backlog 主表 cap-12 line 225 显式注「同 PR」；都改 `cells/{configcore,auditcore}/cell.go` HealthCheckers
**Cx**：Cx2

---

## 3. 依赖图与执行 Wave

```
Wave 1（独立并行，8 PR） — 5/8 ship：
  PR-1 OTEL-HARDEN-5         ✅ PR #486 (OTEL-HARDEN-4，B2-R-05 split)
  PR-2 PROM-HARDEN-3         ✅ PR #484
  PR-3 CLI-HARDEN            ⏳ 未启动
  PR-4 JOURNEY-LIFECYCLE-GOV ⏳ 未启动
  PR-6 G-13 元治理 guard     ✅ PR #487 merged 2026-05-13
  PR-7 BOOTSTRAP-CLIENTS-MUTEX ✅ PR #483
  PR-8 OIDC-MR-COMPLETENESS  ✅ PR #485
  PR-9 REPO-READYZ           ⏳ 未启动

Wave 2（依赖 Wave 1，2 PR） — 0/2 ship：
  PR-5 GOV-NEW-RULES (GOVERNANCE-AUTH-PUBLIC + V-A11)
       ↑ 依赖 PR-6 ✅；**前置已达成，可立即排期**
  PR-11 OIDC-JWKS-ROTATION-WORKER-01
       ↑ 依赖 PR-8 ✅；**前置已达成，可立即排期** (Refresh API 已落在 oidc Adapter)

Wave 3（依赖 Wave 1）：
  TEST-JOURNEY-ROOT-HARNESS-01      ← 依赖 PR-4（未启动）

Wave 4（独立小 PR，触发型 / 与上面 wave 并行不冲突） — 0/5 ship：
  R-01 + G-08 同 batch（分 PR review）
  C-02 CONFIGSUBSCRIBE-CACHE-LIFECYCLE
  STARTUP-ROLLBACK-ERR-JOIN-01
  ADAPTER-ERROR-CLASSIFICATION-TRANSIENT-01
  ADAPTER-CONNECT-BUDGET-01

Wave 5（架构性重构，独立排期，不阻塞发布）：
  G-10 KERNEL-CELL-PACKAGE-DECOMPOSE
  SEALED-MARKER-DEFENSE-EXPANSION-BUNDLE (7 子条)
  BOOTSTRAP-CONTROL-PLANE-DECOUPLE-BUNDLE (3 子条)

外部进行中（不在本计划内但相关）：
  034 S4a ✅ PR #482 → S4b → S4c     (accesscore PG 链，串行；S4a 已 ship)
  040 archtest Pass-Driver           (PR #487 review 派生 plan，4 阶段 ~10 PR；阶段 1 建议
                                      优先于 038 PR-4/PR-5/Wave 4 ADAPTER-ERR-CLASS ship)

新增条目（038 内 ship 衍生，不另开 backlog 主线）：
  METRICS-CTX-FUNNEL-01  ← 由 PR #486 (PR-1) 同 PR split 出，Cx4 跨层；backlog cap-13 line 21 已登记
                          (kernel metrics 接口 ctx-bearing 化，影响 prometheus + otel + outbox + http + wrapper)
```

### 文件冲突核查（Wave 1 真并行）

| PR pair | 共享路径 | 冲突 |
|---|---|---|
| PR-1 vs PR-2 | 都有 cmd/corebundle | PR-1 不动 corebundle；PR-2 仅 metrics.go → ✅ 不冲突 |
| PR-4 vs PR-6 ✅ | 都有 kernel/governance | PR-6 ✅ PR #487 已落（2026-05-13），PR-4 写 rules_journey.go 直接用新范式（rulecodes.go + validateJourney*() + ; fix: 后缀），无 rebase 成本 → ✅ 已解锁 |
| PR-4 / PR-5 / Wave 4 ADAPTER-ERR-CLASS vs 040 阶段 1 | tools/archtest/* | 三者均高概率新增 archtest 文件；040 阶段 1 落 Pass-Driver 范式后业务 archtest 0 改动，新增 archtest 走 Pass-Driver → 建议三者等 040 阶段 1 ship 后再起，避免进 LegacyAllowlist 后二次重写 |
| PR-4 vs PR-3 | 都可能涉 cmd/gocell | PR-4 不动 cmd/gocell；PR-3 仅 cmd/gocell/app → ✅ 不冲突 |
| 所有 PR vs 034 S4a | 034 S4a 在 cmd/corebundle/access_module.go + cells/accesscore | 与本计划全部 PR 文件域互斥 → ✅ 不冲突 |
| PR-7 vs 034 S4a | 都含 runtime/auth | PR-7 仅 route.go validateBypassCompatibility；034 S4a 不动 route.go → ✅ 不冲突 |

---

## 4. 工时粗估

| PR | dev | review | 状态 | 备注 |
|---|---|---|---|---|
| PR-1 OTEL-HARDEN-5 | 8h | 4h | ✅ PR #486 | 实际 4 of 5（B2-R-05 split → METRICS-CTX-FUNNEL-01） |
| PR-2 PROM-HARDEN-3 | 4h | 2h | ✅ PR #484 | +review Hard funnel 升级 |
| PR-3 CLI-HARDEN | 4h | 2h | ⏳ 未启动 | 3 项 |
| PR-4 JOURNEY-LIFECYCLE-GOV | 6h | 3h | ⏳ 等 040 阶段 1 | K-02 束；新 rule 走 PR-6 ✅ 范式；新增 archtest 走 040 Pass-Driver |
| PR-6 G-13 元治理 guard | 6h | 3h | ✅ PR #487 | 注册框架；review 派生 plan 040 archtest Pass-Driver；4 follow-up 登记 cap-02 |
| PR-7 BOOTSTRAP-CLIENTS-MUTEX | 3h | 1.5h | ✅ PR #483 | +review type-aware Hard 全形态覆盖 |
| PR-8 OIDC-MR-COMPLETENESS | 18h | 8h | ✅ PR #485 | A-01 + A-07 + A-08 束 |
| PR-9 REPO-READYZ | 4h | 2h | ⏳ 未启动 | 2 项 |
| PR-5 GOV-NEW-RULES | 4h | 2h | ⏳ 等 040 阶段 1 | PR-6 ✅；保持合并；V-A11 archtest 走 040 Pass-Driver |
| PR-11 OIDC-JWKS-ROTATION-WORKER（依赖 PR-8 ✅） | 4h | 2h | ⏳ 可起 | 后台 worker；PR-8 unblock |
| Wave 3 TEST-JOURNEY-ROOT-HARNESS-01 | 8h | 4h | ⏳ 依赖 PR-4 | integration harness |
| Wave 4 小 PR 合计（5 项，精算） | ~27h | ~13.5h | ⏳ 0/5 | R-01+G-08 batch 10h+5h / C-02 4h+2h / STARTUP-ROLLBACK 3h+1.5h / **ADAPTER-ERR-CLASS 6h+3h（等 040 阶段 1，可能新增 archtest）** / ADAPTER-CONN-BUDGET 4h+2h |
| Wave 5 架构重构 | 独立排期 | — | — | G-10 / SEALED / BOOTSTRAP 束 |

**累计**：
- ✅ shipped (5 PR): ~39h dev / ~18.5h review（PR-1/2/6/7/8）
- ⏳ 待启动 (Wave 1 剩余 + Wave 2/3/4): ~63h dev / ~31.5h review（PR-3/4/5/9/11 + Wave 3 + Wave 4）
- 进度：Wave 1 5/8 ship（62.5%）；038 整体 dev 进度 38%（按原计划 102h 总分母）

---

## 5. 决策点

1. **Wave 1 8 PR 真并行**，文件冲突已核查，仅 PR-4 ↔ PR-6 建议串行（PR-6 先 merge，PR-4 rebase）
2. **PR-5 暂合并 GOVERNANCE-AUTH-PUBLIC + V-A11，PR-6 ship 后回看** — 合并理由偏弱时主动留再决定窗口
3. **R-01 / G-08 不合并**，分 PR review；这是 wave 4 小 PR
4. **架构重构 wave 5 独立排期**，不进本计划主线

### v2 后续决策（2026-05-13）

5. **下一波优先级**：PR-6 (PR #487) 等 review 期间，应起 PR-3 (CLI-HARDEN, 4h) + PR-9 (REPO-READYZ, 4h) — 两者文件域互斥（cmd/gocell/app/ vs cells/{configcore,auditcore}/cell.go），可双 worktree 并行
6. **PR-11 可立即起**：PR-8 已 ship 且 commit body 显式 unblock，oidc Adapter `Refresh(ctx)` API 已保留切口；不必等到 PR-6/PR-5 收口
7. **PR-4 排期等 PR-6 merge**：rules 注册框架 ship 前先启动会产生 rebase 成本，等 PR-6 出来再排
8. **METRICS-CTX-FUNNEL-01 不纳入 038 收口范围**：Cx4 跨 5+ 包接口重构离开本计划「阻塞项 + 高密度可合并 P1」scope；走独立 plan 或长期 backlog 触发型

### v3 后续决策（2026-05-13）

5. ~~**下一波优先级**：PR-6 (PR #487) 等 review 期间...~~ — 决策 #5 v3 写入时 PR-6 in review；2026-05-13 PR-6 ✅ 已 ship，背景过时但建议本身（PR-3 / PR-9 双 worktree 并行）仍有效（不依赖 040）
6. **PR-11 可立即起**：PR-8 已 ship 且 commit body 显式 unblock，oidc Adapter `Refresh(ctx)` API 已保留切口；不必等到 PR-6/PR-5 收口
7. ~~**PR-4 排期等 PR-6 merge**~~ — 决策 #7 已过时：PR-6 ✅ 已落，PR-4 当前阻塞改为 040 阶段 1（见 v4 #11）
8. **METRICS-CTX-FUNNEL-01 不纳入 038 收口范围**：Cx4 跨 5+ 包接口重构离开本计划「阻塞项 + 高密度可合并 P1」scope；走独立 plan 或长期 backlog 触发型

### v4 后续决策（2026-05-14，040 依赖分流）

9. **PR-6 ✅ 已 ship (PR #487)**：注册新规则走 `kernel/governance/rulecodes.go` 单源 + `validateXxx()` 方法 + SeverityError `; fix:` 后缀范式；PR-4 / PR-5 阻塞解除（但叠加 040 依赖见 #11）
10. **plan 040 (archtest Pass-Driver) 整体并行于 038**：PR #487 review 派生，4 阶段 ~10 PR；阶段 2/3 重写 `tools/archtest/*_test.go` 自身，与 038 剩余业务 PR 文件域完全互斥，可滚动并行推进
11. **038 剩余 PR 按 040 依赖分流**（核心决策）：
    - **独立于 040（可立即起）**：PR-3 CLI-HARDEN / PR-9 REPO-READYZ / PR-11 OIDC-JWKS-ROTATION-WORKER / Wave 3 TEST-JOURNEY-ROOT-HARNESS-01 / Wave 4 R-01+G-08 / C-02 / STARTUP-ROLLBACK / ADAPTER-CONNECT-BUDGET（共 ~7 PR） — 均为 functional change，不新增 archtest 文件
    - **建议等 040 阶段 1**：PR-4 JOURNEY-LIFECYCLE-GOV / PR-5 GOV-NEW-RULES / Wave 4 ADAPTER-ERROR-CLASSIFICATION-TRANSIENT-01（共 3 PR） — 三者均高概率新增 archtest 文件；等 Pass-Driver 范式落地后走新入口，避免新文件进 LegacyAllowlist 后被 040 阶段 2/3 二次重写
12. **040 阶段 1 PR 优先级最高**：业务 archtest 0 改动（depguard + Pass struct 设计 + 元治理 archtest 三层 enforcement），不阻塞任何 038 剩余 PR，但 ship 后立即解锁 #11 第二组三个 PR；建议 040 阶段 1 与 038 PR-3 / PR-9 双 worktree 并行起

---

## 6. 引用

- [`docs/plans/202605082145-034-pg-corecell-b-route-plan.md`](202605082145-034-pg-corecell-b-route-plan.md)：accesscore PG 链（S4a/S4b/S4c 串行未 ship）
- [`docs/backlog.md`](../backlog.md) + 4 子表：本计划承担项的 backlog 来源
- [`docs/plans/202605121750-037-wave4-advance-plan.md`](202605121750-037-wave4-advance-plan.md)：036 charter wave 4 触发型 3 条提前推进（独立于本计划）
