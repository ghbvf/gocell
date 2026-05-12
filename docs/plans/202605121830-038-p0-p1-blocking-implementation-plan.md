# 038 P0/P1 阻塞项实施计划（独立于 034 accesscore 路线）

**生成日期**：2026-05-12
**关系**：
- [`docs/plans/202605082145-034-pg-corecell-b-route-plan.md`](202605082145-034-pg-corecell-b-route-plan.md) 已覆盖 accesscore PG 链（S3+S5/S3F/S4.0 已 ship；S4a→S4b→S4c 串行推进）。**本计划不重复 accesscore 路线**，仅引用 034 S4a/S4b/S4c 作为下游闭环
- 本计划聚焦 backlog 中**未被 034 路线覆盖**的 P0/🔴 阻塞项 + 高密度可合并 P1，按依赖关系 + 文件物理重叠 + 同 ADR 概念模型三原则给合并决策

**触发**：用户 2026-05-12 要求把 P0/P1/🔴 项整理成实施计划，要求给合并决策的依据，不能没分析就合并。

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

#### PR-1 PR-OTEL-HARDEN-5（合并 5 个 P1）

**包含**：B2-R-05 / B2-R-06 / B2-R-07 / B2-R-08 / B2-R-09
**依据**：5 文件全部在 `adapters/otel/`（metric_provider.go / tracer.go / pool_collector.go），同一 adapter 的 hardening 主题
**Cx**：Cx3

#### PR-2 PR-PROM-HARDEN-3（合并 3 个 P1）

**包含**：B2-A-22 / B2-A-23 / B2-A-24
**依据**：`cmd/corebundle/metrics.go` + `adapters/prometheus/{hook_observer.go,metric_provider_test.go}`，Prometheus 出口面 hardening
**Cx**：Cx2

#### PR-3 PR-CLI-HARDEN（合并 3 个 P1）

**包含**：B2-X-05 / B2-X-06 / B2-X-07
**依据**：`cmd/gocell/app/{generate.go,verify.go,dispatch.go,main.go}`，CLI 入口治理（unimpl 隐藏 / verify ctx / signal ctx）
**Cx**：Cx2

#### PR-4 PR-JOURNEY-LIFECYCLE-GOV（合并 3 个 P0/🔴）

**包含**：K-02 + JOURNEY-CONTRACT-EXISTENCE-VALIDATE-01 + JOURNEY-STATUS-BOARD-LIFECYCLE-CONSISTENCY-01
**依据**：K-02 (c) 与 JOURNEY-CONTRACT-EXISTENCE 是同一规则的两种描述；3 项都改 `kernel/governance/rules_journey.go` + `journeys/J-*.yaml` + `kernel/verify/`
**Cx**：Cx2-Cx3

#### PR-5 PR-GOV-NEW-RULES（合并 2 个 governance 新规则，依赖 PR-6）

**包含**：GOVERNANCE-AUTH-PUBLIC-INTERNAL-FORBIDDEN + V-A11 GOVERNANCE-EXAMPLES-COVERAGE-01
**依据**：都在 `kernel/governance/`（rules_fmt.go / rules_examples.go 兄弟文件），都是新增 rule，PR-6 注册框架落地后同时挂入
**依赖**：PR-6（G-13）先 ship；合并理由偏弱，PR-6 ship 后可回看是否拆
**Cx**：Cx2

#### PR-6 G-13 GOVERNANCE-RULES-REGISTRATION-GUARD（独立 P1）

**包含**：仅 G-13 单条
**依据**：元治理框架（archtest 反射枚举 + `ValidateStrict` 单入口 + 提取 `rulecodes.go`），PR-5 的前置
**改动**：`kernel/governance/{rules.go,rulecodes.go(新)}` + `tools/archtest/`
**Cx**：Cx2

#### PR-7 AUTH-BOOTSTRAP-CLIENTS-MUTEX-01（独立判断）

**包含**：仅 AUTH-BOOTSTRAP-CLIENTS-MUTEX-01
**改动**：`runtime/auth/route.go:validateBypassCompatibility` + 测试矩阵
**与 034 边界**：034 S4a 不动 route.go，无 file 冲突
**Cx**：Cx2

#### PR-8 PR-OIDC-MR-COMPLETENESS（A-01 含 A-07/A-08 束）

**包含**：A-01 OIDC-FAILFAST-MR-COMPLETENESS（backlog cap-13 line 41 已聚合为束）
**子项**：(1) OIDC 同步 discover；(2) 4 adapter (postgres/redis/s3/oidc) Checkers；(3) s3 状态机 + 后台 health-check；(4) archtest MANAGED-RESOURCE-COMPLETENESS-01；(5) postgres.Pool 升 ManagedResource；(6) `adapters/adapterutil/`(新) helper
**依据**：backlog 已聚合束；4 adapter 同时升 MR 才能挂 archtest 完整性闸门，缺一不可
**Cx**：Cx3-Cx4

#### PR-9 PR-REPO-READYZ

**包含**：REPO-HEALTHCHECKER-01 + B2-R-02
**依据**：backlog 主表 cap-12 line 225 显式注「同 PR」；都改 `cells/{configcore,auditcore}/cell.go` HealthCheckers
**Cx**：Cx2

---

## 3. 依赖图与执行 Wave

```
Wave 1（独立并行，8 PR）：
  PR-1 OTEL-HARDEN-5         独立  ──┐
  PR-2 PROM-HARDEN-3         独立  ──┤
  PR-3 CLI-HARDEN            独立  ──┤
  PR-4 JOURNEY-LIFECYCLE-GOV 独立  ──┼── 全部并行，无内部依赖
  PR-6 G-13 元治理 guard     独立  ──┤
  PR-7 BOOTSTRAP-CLIENTS-MUTEX 独立 ──┤
  PR-8 OIDC-MR-COMPLETENESS  独立 ──┤
  PR-9 REPO-READYZ           独立 ──┘

Wave 2（依赖 Wave 1，2 PR）：
  PR-5 GOV-NEW-RULES (GOVERNANCE-AUTH-PUBLIC + V-A11)
       ↑ 依赖 PR-6（G-13 注册框架）
  PR-11 OIDC-JWKS-ROTATION-WORKER-01
       ↑ 依赖 PR-8（A-01 ManagedResource Worker 切口）

Wave 3（依赖 Wave 1）：
  TEST-JOURNEY-ROOT-HARNESS-01      ← 依赖 PR-4（journey lifecycle 校验）

Wave 4（独立小 PR，触发型 / 与上面 wave 并行不冲突）：
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
  034 S4a → S4b → S4c     (accesscore PG 链，串行)
```

### 文件冲突核查（Wave 1 真并行）

| PR pair | 共享路径 | 冲突 |
|---|---|---|
| PR-1 vs PR-2 | 都有 cmd/corebundle | PR-1 不动 corebundle；PR-2 仅 metrics.go → ✅ 不冲突 |
| PR-4 vs PR-6 | 都有 kernel/governance | PR-4 加 rules_journey.go（新文件）；PR-6 改 rules.go 注册框架 → ⚠️ 注册框架变更可能要求 PR-4 用新 API；建议 PR-6 先 merge，PR-4 rebase |
| PR-4 vs PR-3 | 都可能涉 cmd/gocell | PR-4 不动 cmd/gocell；PR-3 仅 cmd/gocell/app → ✅ 不冲突 |
| 所有 PR vs 034 S4a | 034 S4a 在 cmd/corebundle/access_module.go + cells/accesscore | 与本计划全部 PR 文件域互斥 → ✅ 不冲突 |
| PR-7 vs 034 S4a | 都含 runtime/auth | PR-7 仅 route.go validateBypassCompatibility；034 S4a 不动 route.go → ✅ 不冲突 |

---

## 4. 工时粗估

| PR | dev | review | 备注 |
|---|---|---|---|
| PR-1 OTEL-HARDEN-5 | 8h | 4h | 5 项 |
| PR-2 PROM-HARDEN-3 | 4h | 2h | 3 项 |
| PR-3 CLI-HARDEN | 4h | 2h | 3 项 |
| PR-4 JOURNEY-LIFECYCLE-GOV | 6h | 3h | K-02 束 |
| PR-6 G-13 元治理 guard | 6h | 3h | 注册框架 |
| PR-7 BOOTSTRAP-CLIENTS-MUTEX | 3h | 1.5h | route.go |
| PR-8 OIDC-MR-COMPLETENESS | 18h | 8h | A-01 + A-07 + A-08 束 |
| PR-9 REPO-READYZ | 4h | 2h | 2 项 |
| PR-5 GOV-NEW-RULES（依赖 PR-6） | 4h | 2h | 2 rule |
| PR-11 OIDC-JWKS-ROTATION-WORKER（依赖 PR-8） | 4h | 2h | 后台 worker |
| Wave 3 TEST-JOURNEY-ROOT-HARNESS-01 | 8h | 4h | integration harness |
| Wave 4 小 PR 合计（5 项，精算） | ~27h | ~13.5h | R-01+G-08 batch 10h+5h / C-02 4h+2h / STARTUP-ROLLBACK 3h+1.5h / ADAPTER-ERR-CLASS 6h+3h / ADAPTER-CONN-BUDGET 4h+2h |
| Wave 5 架构重构 | 独立排期 | — | G-10 / SEALED / BOOTSTRAP 束 |
| **本计划合计（Wave 1-3）** | **~69h** | **~33.5h** | 10 PR + 1 wave 3 |

---

## 5. 决策点

1. **Wave 1 8 PR 真并行**，文件冲突已核查，仅 PR-4 ↔ PR-6 建议串行（PR-6 先 merge，PR-4 rebase）
2. **PR-5 暂合并 GOVERNANCE-AUTH-PUBLIC + V-A11，PR-6 ship 后回看** — 合并理由偏弱时主动留再决定窗口
3. **R-01 / G-08 不合并**，分 PR review；这是 wave 4 小 PR
4. **架构重构 wave 5 独立排期**，不进本计划主线

---

## 6. 引用

- [`docs/plans/202605082145-034-pg-corecell-b-route-plan.md`](202605082145-034-pg-corecell-b-route-plan.md)：accesscore PG 链（S4a/S4b/S4c 串行未 ship）
- [`docs/backlog.md`](../backlog.md) + 4 子表：本计划承担项的 backlog 来源
- [`docs/plans/202605121750-037-wave4-advance-plan.md`](202605121750-037-wave4-advance-plan.md)：036 charter wave 4 触发型 3 条提前推进（独立于本计划）
