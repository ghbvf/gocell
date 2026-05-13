# 039 P2 OPEN 整理与实施计划（独立于 034/038 主线）

**生成日期**：2026-05-13
**关系**：
- [`docs/plans/202605082145-034-pg-corecell-b-route-plan.md`](202605082145-034-pg-corecell-b-route-plan.md) accesscore PG 链（S4b/S4c 串行未 ship）
- [`docs/plans/202605121830-038-p0-p1-blocking-implementation-plan.md`](202605121830-038-p0-p1-blocking-implementation-plan.md) P0/P1 阻塞项（Wave 1 4/8 ship + 1/8 in review，Wave 2/3/4 未启动）
- **本计划在 034 + 038 全部完结后启动**（用户裁决 2026-05-13：039 不与 034/038 并行）

**触发**：用户 2026-05-13 要求把 66 项 P2 OPEN 整理成实施计划，全量纳入，严格按合并三原则判定（不能简单合并）。

**合并判定方法**（沿用 038 三原则）：

候选合并必须同时满足下列两条以上才立项：
1. **依赖关系**：A ship 是 B 启动的硬前置
2. **文件物理重叠**：共享至少一个核心 file（不是仅同包同 cap）
3. **同 ADR 概念模型**：同一 ADR / 同一 design decision / 同源 PR follow-up

仅满足"同 cap"、"同主题"、"同包不同 file" 的合并理由偏弱，按 038 PR-5 标准 reject — 走独立小 PR。

---

## 1. 本计划承担的范围

66 项 P2 OPEN 全部纳入（取 `develop @ ad98b8b7`，2026-05-13 backlog 快照）：

| Wave | 描述 | 项数 | PR 数 |
|---|---|---|---|
| **Wave 1** | trigger 已达成 → 立即可起 | 2 | 2 |
| **Wave 2** | 严格合并依据强 → 合并 PR | 4 | 2 |
| **Wave 3** | 独立小 PR（无强合并依据） | 50 | 50 |
| **Wave 4** | trigger 未到 🟠 → watch only，留 backlog 等触发 | 10 | 0（当前） |
| **合计** | | **66** | **54 active + 10 watch** |

### 合并候选审查（reject 名单，供 review 复核）

下列同主题候选**不立合并 PR**，按"文件物理重叠 + 同 ADR concept" 判定不充分：

| 候选合并 | reject 理由 |
|---|---|
| 5 项 archtest 升级 (M4-COVERAGE / PR-FIXTURE-CELLID / PR464-FU-CAS / K07-SUBSCRIPTION / 等) | 同 tools/archtest/ 包但 5 个独立 *_test.go + 5 个独立 invariant，concept 不重叠 |
| 6 项 contract codegen 工具 (BREAKING / CODEGEN / STUB / C-L6 / SHARED-ERROR-SCHEMA / SHARED-MIXIN-FUNNEL) | 6 个独立 CLI 子命令/特性 + 6 个独立 ADR，仅"同包"层级 |
| 7 项 metadata governance (DURABLE / M3 / G-1 / G-15 / B2-K-05 / PR411-AUTH / M2) | 同 kernel/metadata/ + kernel/governance/ 但 7 个独立子系统 concept 不重叠 |
| 2 项 outbox refactor (PR341-FU + OUTBOX-HANDLERESULT-SLIM) | conformance.go vs outbox.go+consumer_base.go 不同 file + 测试覆盖 vs 字段精简 concept 不同 |
| 2 项 OTel adapter (PR283-OTEL-SLOG + B2-A-21 collector) | 同 adapters/otel/ 但 logging bridge vs messaging collector format concept 不同 |
| 3 项 doc (F-04 + F-05 + F-06) | cmd/CLAUDE.md vs GitHub workflow vs 新建 docs/requirements/ — file 全分散 |
| 2 项新包 (KERNEL-WEBHOOK + RUNTIME-SCHEDULER) | webhook vs scheduler concept 完全不同 |
| 3 项 observability log fix (B2-A-13 + B2-A-21 + B2-A-25) | 三个不同 adapter，三个不同 file，三个不同 concept |
| 3 项 config / cache (B2-A-33 + B2-C-11 + PR-CFG-A-DEFER-2) | adapter/redis vs cells/configcore 不同层 + concept 不同 |
| 1 项 cmd/corebundle/bundle_test.go (D2-FU-01 + CELL-WIRING-ASSEMBLY-REGRESSION) | file 重叠但 CELL-WIRING 是 🟠 trigger 未到（出现第 2 个装配回归事故），不能与 trigger 已达成的 D2-FU-01 同步 ship |

---

## 2. PR 合并决策（每条带依据）

### ✅ Wave 1 — trigger 已达成（2 独立 PR）

#### W1-1 B2-PROVISIONER-MUTEX-REVIEW（独立）

**包含**：仅 B2-PROVISIONER-MUTEX-REVIEW
**改动**：`cells/accesscore/internal/adminprovision/provisioner.go`
**Cx**：Cx1
**trigger 达成**：PR #482 (S4a) ship PG accesscore wiring；PG row-level lock + UNIQUE constraint 已覆盖并发场景，in-process mutex 多半冗余 → 立即可审视

#### W1-2 PR392-FU-AUDIT-CHAIN-WIRING（独立）

**包含**：仅 PR392-FU-AUDIT-CHAIN-WIRING (BOOTSTRAP-AUDIT-CHAIN-WIRING-01)
**改动**：`cmd/corebundle/access_module.go` — onAuthFail 从 slog 升级为 `audit.AppendBootstrapAuthFail`
**Cx**：Cx2
**trigger 达成**：PR #450 (S7) ship `runtime/audit/ledger.Protocol` + auditcore framework；cross-cell wiring 接口已就绪

### ✅ Wave 2 — 严格合并 PR（2 PR，4 项）

#### W2-M1 Cells Layout Normalize（合并 C-04 + C-09）

**包含**：C-04 CELLS-INIT-TEMPLATE-CONVERGE（含 C-07 emitter health probe helper） + C-09 CELL-SPLIT-LAYOUT-NORMALIZE
**合并依据**：
- (2) 文件物理重叠 **强**：两项共享 `cells/{accesscore,configcore}/*` + `scaffold` 模板（重叠面 ≥ 75%）；C-04 改 `kernel/cell/BaseCell.RegisterStandard` 模板 + 3 cell 改造 + scaffold；C-09 改 cells/accesscore/ + cells/configcore/ 三文件范式 + scaffold 模板同步
- (3) 同 ADR concept **强**：两项都是 "cells/* layout normalize"（Init 模板 + cell_routes/cell_lifecycle/cell_helpers 命名惯例 + scaffold 预生成 internal/{ports,domain,dto,events,mem} 五目录），同一 cell template 重构 ADR
**Cx**：Cx2（合并后 Cx2-3，scaffold 模板共享避免双重协调）
**前置依赖**：K-06（template register API）+ K-07（lifecycle 命名）落地后

#### W2-M2 PR411 Auth Route Policy Ownership Hard Guard（合并 PR411-HANDLER-POLICY + PR411-SERVICEOWNED）

**包含**：PR411-HANDLER-POLICY-TYPEAWARE-SCANNER-01 + PR411-SERVICEOWNED-OWNERSHIP-GUARD-01
**合并依据**：
- (3) 同源 PR# follow-up **强**：两项都是 PR #411 同次 review 派生 + 同主题 "auth route policy ownership 静态守卫升 Hard"
- (2) 文件物理重叠 **中**：PR411-HANDLER-POLICY 改 `tools/archtest/handler_policy_required_test.go`；PR411-SERVICEOWNED 改 `kernel/governance/` + `tools/archtest/` + `pkg/contracttest/`；共享 `tools/archtest/` 包 + 部分共享 archtest fixture
- 参考 038 PR-5（GOVERNANCE-AUTH-PUBLIC + V-A11，"都在 `kernel/governance/` 兄弟文件，都是新增 rule"）的合并依据强度 — 本 PR-M2 概念耦合度更高（同 PR#411 派生 vs 038 PR-5 仅同位置）
**Cx**：Cx3（两项各 Cx3，合并后 archtest fixture 共享降低重复）
**前置**：无（独立可起）

### ⏳ Wave 3 — 独立小 PR（50 项，各 PR 单条）

按文件域/包路径分类（便于批次推进），但**每条仍是独立 PR**（不合并）：

#### W3.a `kernel/` core（11 项）

| ID | Cx | 描述要点 |
|---|---|---|
| C-06 | Cx1 | L0-CELL-DECISION（doc-only 或升 pkg/query.CursorCodec 为示例 L0 cell） |
| KERNEL-RECONCILE-01 | Cx3 | kernel/reconcile L3 收敛循环（新包） |
| OUTBOX-HANDLERESULT-SLIM-01 | Cx2 | HandleResult ProcessReason/SettlementObservers 字段精简 |
| PR341-FU-OUTBOXTEST-CLOSE-BUDGET-COVERAGE | Cx1 | conformance suite 走 closeWithBudget |
| K07-SUBSCRIPTION-REGISTRY-WRAPPER-BAN-01 | Cx2 | 新增 REGISTRY-SUBSCRIBE-NO-WRAPPER-01 archtest |
| DURABLE-TYPE-01 | Cx3 | L2/L3 持久化级别静态保护 |
| M3-RULE-ENGINE | Cx3 | GOVERNANCE-RULE-ENGINE-DATA-DRIVEN-01 |
| G-1 | Cx2 | FMT-11 dynamic-status-field 隔离 |
| G-15 | Cx2 | KERNEL-METADATA-CODEGEN-OVERLAY |
| B2-K-05 | Cx2 | Metadata parser error 路径 PII 泄漏 |
| B2-K-08 | Cx2 | Assembly race test 认知复杂度超限 |

#### W3.b `runtime/` 与 observability（10 项）

| ID | Cx | 描述要点 |
|---|---|---|
| M1-OBSERVED | Cx3 | HEALTHZ-INTERFACE-PACKAGE-01（kernel/healthz 新包） |
| P4-TD-10 | Cx2 | Metrics path label cardinality（chi route template） |
| B2-A-20 | Cx2 | OTel simple tracer propagation 不对称 |
| R-02 | Cx1 | EVENTBUS-DROP-CONTEXTUAL-LOG（broadcast/roundRobin drop 升 Error 级 + 三字段） |
| WM-32 | Cx2 | mTLS 中间件（runtime/http/middleware/） |
| C-AC7 | Cx2 | JWT jti claim 支持（runtime/auth/） |
| B2-R-01 | Cx2 | HealthListener 缺失时静默回退 → fail-fast |
| RUNTIME-SCHEDULER-01 | Cx3 | runtime/scheduler Cron 调度（新包） |
| KERNEL-WEBHOOK-01 | Cx3 | kernel/webhook 出站请求（新包） |
| PR-V1-PG-STARTUP-HARDEN-FU-RACE-COVERAGE | Cx3 | adapters/postgres 路径加 -race |

#### W3.c `adapters/`（6 项）

| ID | Cx | 描述要点 |
|---|---|---|
| B2-A-13 | Cx2 | PG pool tx rollback 日志泄漏 |
| B2-A-21 | Cx1 | OTel messaging collector format % |
| B2-A-25 | Cx2 | Prometheus lookup vec 99% 重复 |
| B2-A-33 | Cx2 | Redis sentinel env & logvalue 缺 |
| B2-A-34 | Cx3 | Redis cluster CI live gate |
| WM-18 | Cx2 | 延迟消息原语（RMQ x-delayed-message） |

#### W3.d `cells/`（4 项）

| ID | Cx | 描述要点 |
|---|---|---|
| B2-C-11 | Cx2 | Configsubscribe tombstone 无 TTL |
| PR266-AUDITAPPEND-STRICT | Cx2 | AUDITAPPEND-STRICT-UNMARSHAL-01 |
| C-DC9 | Cx2 | auditarchive 死代码靶子打通（S3 adapter） |
| USER-REPO-UPDATE-USE-CASE-SPLIT-01 | Cx3 | user_repo.Update generic → use-case 专用方法拆分 |

#### W3.e `contracts/` + tooling（9 项）

| ID | Cx | 描述要点 |
|---|---|---|
| B2-T-04 | Cx2 | Contract userId 风格混用 → 统一 camelCase |
| B2-T-08 | Cx1 | Config publish 失败码声明不完整 |
| PR411-AUTH-SCHEMA-GOVERNANCE-BOOL-SEMANTICS-01 | Cx2 | auth mode schema/governance boolean semantics |
| SHARED-ERROR-SCHEMA-GENERATION-01 | Cx3 | 共享 error schema 单源 |
| CONTRACT-BREAKING-01 | Cx3 | gocell check contract-breaking |
| CONTRACT-CODEGEN-01 | Cx3 | Go DTO ↔ JSON Schema 双向推断 |
| CONTRACT-STUB-01 | Cx3 | Consumer-Driven Contract Stub |
| C-L6 | Cx2 | Contract ID 解析标准统一 |
| CONTRACT-SHARED-MIXIN-FUNNEL-01 | Cx3 | contracts/shared schema mixin codegen funnel |

#### W3.f `cmd/` / CI / `tests/`（7 项）

| ID | Cx | 描述要点 |
|---|---|---|
| B2-X-01 | Cx1 | Outbox E2E 固定 sleep |
| B2-X-03 | Cx2 | PG invalid index warn → fail-fast |
| B2-X-08 | Cx2 | cmdrun Windows 进程组杀不完 |
| D2-FU-01 | Cx2 | defaultRuntimeOptions wiring 语义断言加强 |
| P2-T-02 | Cx2 | J-auditlogintrail 端到端集成测试 |
| L3-EXAMPLE-PROJECTION-01 | Cx2 | examples L3 投影 reference |
| B2-C-13 | Cx3 | L2 跨层 e2e 回归不足 |

#### W3.g `docs/`（3 项）

| ID | Cx | 描述要点 |
|---|---|---|
| F-04 | Cx1 | CMD-GOCELL-VS-COREBUNDLE-DOC |
| F-05 | Cx1 | QODANA-WORKFLOW-AUDIT |
| F-06 | Cx3 | REQUIREMENTS-TRACEABILITY-CHAIN |

### 🟠 Wave 4 — trigger 未到 / watch only（10 项，不排期）

留 backlog 等触发，每条 trigger 字段保持当前状态：

| ID | Cx | Trigger（等待中） |
|---|---|---|
| P3-TD-10 TOCTOU 竞态修复 | Cx3 | 034 S4b authz_epoch 闭环 ship — 一并吸收 |
| CONFIGCORE-RECEIVE-PLACEHOLDER-CLEANUP-01 | Cx2 | 业务侧 JWT TTL hot-reload / key rotation 需求 |
| PR-FIXTURE-CELLID-TYPED-BUILDER-01 | Cx2 | typeseval helper 就绪（依赖 cap-02.2） |
| M2-LIFECYCLE | Cx3 | lifecycle 字段空率 ≥ 30%（cap-02.1 触发） |
| PR283-OTEL-SLOG-ERROR-ATTR | Cx2 | 首次 OTEL slog bridge 接入 |
| M4-COVERAGE | Cx3 | 反向覆盖度审计窗口（cap-14 主题 PR 时） |
| CELL-WIRING-ASSEMBLY-REGRESSION-TEMPLATE-01 | Cx3 | 出现第 2 个装配回归事故 |
| PR464-FU-CHANGEPASSWORD-BCRYPT-TX-CONCURRENCY-LIMIT-01 | Cx2 | bcrypt-in-tx 并发吞吐压力（K-08 + B2-K-08 race 调整后复测） |
| PR464-FU-CAS-PROTOCOL-ARCHTEST-TYPESEVAL-UPGRADE-01 | Cx2 | typeseval helper 就绪 |
| PR464-FU-CAS-PROTOCOL-CONSUMPTION-BINDING-01 | Cx3 | CAS Protocol 第 3 个消费方出现 |

---

## 3. 依赖图与执行 Wave

```
前置（外部）：
  034 S4b → S4c     ✅ 全部 ship 后才起 039
  038 PR-3/4/5/6/9/11 + Wave 3/4     ✅ 全部 ship 后才起 039

Wave 1（独立并行，2 PR）：
  W1-1 B2-PROVISIONER-MUTEX-REVIEW    ──┬── 文件域互斥，可并行起
  W1-2 PR392-FU-AUDIT-CHAIN-WIRING    ──┘

Wave 2（独立并行，2 合并 PR）：
  W2-M1 Cells Layout Normalize       ← 需 K-06/K-07 前置（cap-01 backlog）
  W2-M2 PR411 Auth Route Policy Hard ← 无前置，可与 W2-M1 并行

Wave 3（50 独立 PR，按文件域批次推进）：
  W3.a kernel/ core (11) ──┐
  W3.b runtime/observability (10) ──┤
  W3.c adapters/ (6) ──┤── 文件域互斥，可分批多 worktree 并行
  W3.d cells/ (4) ──┤
  W3.e contracts/+tooling (9) ──┤
  W3.f cmd/+CI+tests (7) ──┤
  W3.g docs/ (3) ──┘

Wave 4（10 项 watch only，不排期）：
  P3-TD-10 → 034 S4b 一并吸收（authz_epoch closed loop 覆盖）
  其余 9 项留 backlog 等触发，不进 039 工时
```

### 文件冲突核查（Wave 1/2 真并行）

| PR pair | 共享路径 | 冲突 |
|---|---|---|
| W1-1 vs W1-2 | 无共享 | ✅ 不冲突 |
| W1 全部 vs W2-M1 | W1-1 在 cells/accesscore/internal/adminprovision/；W2-M1 在 cells/{accesscore,configcore}/ + scaffold | ⚠️ accesscore 同目录树但子包不同 (`adminprovision` vs cell-root layout) → ✅ 不冲突 |
| W1 全部 vs W2-M2 | W2-M2 在 tools/archtest/ + kernel/governance/ + pkg/contracttest/ | ✅ 不冲突 |
| W2-M1 vs W2-M2 | 无共享（cells/+scaffold vs archtest+governance） | ✅ 不冲突 |
| Wave 3 内部 | 按 W3.a-W3.g 文件域分批 | 同 W3.x 内的多个独立 PR 可能争 file，按批次顺序 ship |

### 与 034/038 边界

- 034 + 038 全部完结 → 039 启动
- **不与 034 S4b/S4c 并行**：034 在 cells/accesscore + runtime/auth；039 部分 PR 也触 cells/accesscore（W1-1 / W3.d USER-REPO-UPDATE）+ runtime/auth (W3.b C-AC7 / WM-32) → 文件域重叠风险高，串行更安全
- **不与 038 Wave 1/2/3/4 并行**：038 PR-3/4 触 cmd/gocell + kernel/governance；039 W3.a M3-RULE-ENGINE / G-1 / G-15 / B2-K-05 同触 kernel/metadata + kernel/governance → 串行避免 rebase 成本

---

## 4. 工时粗估

按 Cx 标准：Cx1=2h+1h / Cx2=4h+2h / Cx3=6h+3h（dev+review）

| Wave | 项数 | dev | review | 备注 |
|---|---|---|---|---|
| Wave 1 (2 PR) | 2 | 6h | 3h | W1-1 Cx1 + W1-2 Cx2 |
| Wave 2 (2 合并 PR) | 4 | 16h | 8h | W2-M1 6h+3h（C-04+C-09 Cx2 合并）+ W2-M2 10h+5h（PR411x2 Cx3 合并） |
| Wave 3 W3.a kernel/ | 11 | 50h | 25h | 1×Cx1 + 5×Cx2 + 5×Cx3 |
| Wave 3 W3.b runtime/ | 10 | 44h | 22h | 1×Cx1 + 6×Cx2 + 3×Cx3 |
| Wave 3 W3.c adapters/ | 6 | 22h | 11h | 1×Cx1 + 4×Cx2 + 1×Cx3 |
| Wave 3 W3.d cells/ | 4 | 18h | 9h | 3×Cx2 + 1×Cx3 |
| Wave 3 W3.e contracts/+tooling | 9 | 44h | 22h | 1×Cx1 + 2×Cx2 + 6×Cx3 |
| Wave 3 W3.f cmd/+CI+tests | 7 | 28h | 14h | 1×Cx1 + 5×Cx2 + 1×Cx3 |
| Wave 3 W3.g docs/ | 3 | 10h | 5h | 2×Cx1 + 1×Cx3 |
| Wave 3 小计 | 50 | 216h | 108h | |
| Wave 4 watch only | 10 | 0h | 0h | 不排期 |
| **039 active 合计** | **56** | **238h** | **119h** | Wave 1+2+3 active |

**与既有 plan 对比**：
- 034 B 路线 v4: ~146h dev（accesscore 整链）
- 038 Wave 1-5: ~102h dev（P0/P1 阻塞）
- 039 Wave 1-3: ~238h dev（P2 整理，最大）
- 三 plan 累计：~486h dev — 反映 P2 即使每条 Cx 低，但 50+ 独立 PR 累计工时高

**实际节奏建议**：Wave 3 不必一次全 ship，按文件域 W3.a-W3.g 分批，每批 10-20h dev，配合 review 节奏。

---

## 5. 决策点

1. **66 全量纳入**（用户裁决 2026-05-13）：包括 trigger 未到的 10 项 🟠，作为 Wave 4 watch only 列条目，不排期工时
2. **严格合并依据**（用户裁决 2026-05-13）：仅 2 个合并 PR 站住脚（W2-M1 Cells Layout + W2-M2 PR411 Auth Route Policy），其余 50 项独立小 PR；reject 名单见 §1
3. **038/034 优先**（用户裁决 2026-05-13）：039 在 034 + 038 全部完结后启动，不并行
4. **Wave 3 分批 ship**：按 W3.a-W3.g 文件域批次推进，不必一次全 ship；每批文件域内 PR 可并行 worktree
5. **Wave 4 watch only**：trigger 未到的 10 项留 backlog 等触发，不进 039 工时累计；触发时各自走独立小 PR
6. **不为 P2 引入 backlog 新条目**：本 plan 不衍生新 backlog 项，所有 PR 都映射回已有 backlog ID

---

## 6. 引用

- [`docs/plans/202605082145-034-pg-corecell-b-route-plan.md`](202605082145-034-pg-corecell-b-route-plan.md)：accesscore PG 链（S4a ✅ PR #482；S4b/S4c 待 ship）
- [`docs/plans/202605121830-038-p0-p1-blocking-implementation-plan.md`](202605121830-038-p0-p1-blocking-implementation-plan.md)：P0/P1 阻塞项（Wave 1 4/8 ship；其余待 ship）
- [`docs/backlog.md`](../backlog.md) + 4 子表：本计划承担项的 backlog 来源（`develop @ ad98b8b7` 快照）
- 合并三原则参考：038 plan §2 + ai-collab.md "Review checklist"
