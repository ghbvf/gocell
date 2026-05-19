# PG / accesscore / auditcore / configcore 待办清单 — 2026-05-18 闭环快照

**归档时间**: 2026-05-18 15:30
**原始清单**: `docs/plans/202605082130-pg-corecell-open-issues.md`（2026-05-08 生成）
**归档原因**: 10 天闭环核对完成；主文件已精简为只含未完成项；本快照保留完整闭环证据链。

## 闭环统计

原始 52 条（accesscore 16 + auditcore 10 + configcore 13 + 横切 13） → **✅ closed 38 条 + 仍 ⏳ open / 🟡 部分 13 条**（PR392-FU-AUDIT-CHAIN-WIRING 在 accesscore 与 auditcore 节重复出现，主文件仅保留一次）。

### 4 轮核查发现的新增闭环（22 项，超出 auditcore S7 原始 9 条之外）

| 区段 | ID | 关键证据 |
|---|---|---|
| accesscore | B2-C-02 SETUP-ADMIN-PUBLIC-ROUTE-PERMANENT | setup/service.go 410 Gone + setupLock advisory + adminprovision race-safe + bootstrap-only lifecycle |
| accesscore | CELLS-IDENTITYMANAGE-LEVEL-MISLABEL-01 | codegen funnel 锁定 cell.yaml consistencyLevel（backlog T2） |
| accesscore | B5-FU-PG-RUNTIME-WIRING-AND-ARCHTEST-TYPE-AWARE-01 | PR #482 + #587-t3 |
| accesscore | PR338-FU-LOGIN-DURABLE-TX-ATOMICITY-TEST | PR #482 S4a setup_pg_integration_test.go |
| accesscore | P3-TD-10 TOCTOU 竞态修复 | S4d credential 重构（ADR-credential-session-protocol §A8） |
| accesscore | B2-T-02 RBACASSIGN event contract waiver | S4c-T1 contract_test.go 重写 |
| accesscore | B2-T-07-FU-1 RBACASSIGN caller wiring | S4c-T1 handler.go RequireCallerCell |
| accesscore | B2-C-06 SessionLogout consumer action | consumer.go:79-97 switch + default Reject |
| accesscore | PR280-FU1 CHANGEPASSWORD-CONCURRENT | service.go:862-869 RunInTx + PasswordVersion CAS（S6） |
| accesscore | PR267-FU-AUTHTEST-INTERNAL | cells/accesscore/internal/authtest/ 已 internal |
| configcore | CONFIGCORE-CACHE-LIFECYCLE-OWNER-01 | won't-do（2026-05-16）：cache service-private |
| configcore | B2-T-01 + P3-TD-12 Rollback OCC | service.go:149,199 expectedVersion CAS + handler.go:68 409 |
| configcore | C-02 CONFIGSUBSCRIBE-CACHE-LIFECYCLE | service.go:230-298 Start/StopTombstoneGC + AfterStart |
| configcore | B2-C-11 Configsubscribe tombstone TTL | service.go:24-30 defaultTombstoneTTL = idempotency.DefaultTTL |
| configcore | C-05 CELLS-CELLROUTES-PLACEHOLDER-DELETE | find 0 命中 configcore/cell_routes.go |
| configcore | PR238-FU4 CONFIGREPO-LEGACY-NOTFOUND-TEST-DEDUP | config_repo_test.go:347/390/430 NotFound 测试 dedupe |
| configcore | CELLS-SLICE-MULTI-VERB-DECOMPOSE configread | cell_init.go:14-15,202-219 双 slice 拆分 |
| 横切 | A-01 OIDC-FAILFAST-MR-COMPLETENESS | 7 adapter + websocket opt-out + archtest |
| 横切 | ADAPTER-ERROR-CLASSIFICATION-TRANSIENT-01 | PR #517 |
| 横切 | ADAPTER-CONNECT-BUDGET-01 | 4/4 adapter ConnectTimeout 已就位 |
| 横切 | REPO-HEALTHCHECKER-01 + B2-R-02 Readyz | PR-REPO-READYZ（2026-05-16） |
| 横切 | ADAPTER-MANAGED-RESOURCE-COMPLETENESS | archtest opt-out allowlist 制度 |
| 横切 | B2-X-03 PG invalid index | schema_guard.go:948-969 VerifyNoInvalidIndexes fail-fast |
| 横切 | B2-A-13 PG pool tx 日志泄漏 | tx_manager.go:96-154 redaction.RedactError/RedactAny |
| 横切 | PR-V1-PG-STARTUP-HARDEN-FU-RACE-COVERAGE | test-race.yml:87-128 race-pg-integration job |
| 横切 | B2-C-13 L2 跨层 e2e | harness_test.go:115 三 cell 全 wire |
| 横切 | M1-OBSERVED HEALTHZ-INTERFACE-PACKAGE-01 | 38 → 7（typed funnel + lifecycle.ManagedResource 接口分层） |

### auditcore S7 原始闭环（9 项，PR #450 refactor/554-pg-s7-audit-ledger）

| ID | 落地点 |
|---|---|
| B2-C-01 Audit hashchain 重启未恢复尾节点 | runtime/audit/ledger.Store.Tail() + strict-tail-verify-on-startup |
| AUDITAPPEND-L2-FAILURE-PROOF-01 | adapters/postgres/audit_ledger_store_test.go testcontainer outbox-writer fail injection |
| B2-C-05 Auditappend actor 缺失降级不安全 | sub-slice service.go actor 缺失 → outbox.Reject(NewPermanentError) fail-closed |
| B2-C-09 Auditquery raw payload 直接回传 | auditquery/handler.go 走 pkg/redaction.RedactPayload |
| B2-C-10 Auditappend 全局 mutex 串行化 | mutex 上提到 ledger.Store；PG 用 pg_advisory_xact_lock + SELECT FOR UPDATE |
| B2-C-14 Hash-chain 跨重启连续性测试 | testcontainer cross-pool restart test |
| C-DC9 auditarchive 死代码 | 整 slice + s3archive adapter + ports/mem 全部删除 |
| PR266-AUDITAPPEND-STRICT | Protocol 默认 strict（json.Decoder.DisallowUnknownFields） |
| CELLS-SLICE-MULTI-VERB-DECOMPOSE auditappend | 13 topic → 4 sub-slice（auditappend-session/user/config/role） |

## 仍未完成项（13 条，已迁移至主文件）

主文件 `docs/plans/202605082130-pg-corecell-open-issues.md` 仅保留以下 13 条 active tracking。详细描述与证据见主文件。

- 🔴 P1 ACCESSCORE-ACCOUNT-LOCKOUT-AUTO-LOCK-01
- 🟠 P2 PR392-FU-AUDIT-CHAIN-WIRING（accesscore + auditcore 共用，待 contract 发布）
- 🟠 P2 B2-PROVISIONER-MUTEX-REVIEW
- 🟡 P3 PR250-F3 Event wire byte pinning
- 🟡 P3 X5 P3-TD-11 accesscore domain 拆分
- 🟠 P3 X13 REFRESH-PARTITION-01
- 🟡 P1 CONFIGCORE-RECEIVE-PLACEHOLDER-CLEANUP-01（ADV-05 合规需要）
- 🟡 Cx1 PR-CFG-A-DEFER-2 ConfigCore L2 divergence
- 🟡 P3 PR320-FU-CONFIGCORE-CI-NOOP
- 🟡 Cx2 PR-CFG-G1-FU6
- 🟠 P3 PR238-FU8 CONFIGREPO-UPDATE-ROLLBACK-OP-LABEL-TEST-01
- 🟡 P2 C-04 CELLS-INIT-TEMPLATE-CONVERGE（含 C-07）
- 🟡 P2 C-09 CELL-SPLIT-LAYOUT-NORMALIZE

## 核查方法学回顾

本次闭环核查跨 4 轮，逐步从「backlog 标记 + commit 扫描」收紧到「亲眼读源码」：

| 轮次 | 方法 | 误判修正 |
|---|---|---|
| 1 | backlog.md 标记 + git log 标题扫描 | — |
| 2 | Explore agent 派遣，引用文件:行 | — |
| 3 | 亲眼读 setup/service.go + adapter ManagedResource + grep lockout 字段 | B2-C-02（agent 误判完全未动 → 实际已 ship）、A-01（agent 误判 5/7 → 实际 100%）、C-05（agent 误判未删 → 实际已删） |
| 4 | 亲眼读 ChangePassword + configsubscribe + L2 harness + PG schema_guard + test-race.yml | C-02 / B2-C-13 / B2-X-03 / B2-A-13 / PR-V1-PG-RACE / ADAPTER-CONNECT-BUDGET / M1 HEALTHZ 共 7 项 agent 漏报 |

**教训**：agent 引用具体文件:行不等于看了代码；需 trust-but-verify。

## 当时清单原文（archive 完整快照）

> 以下为 2026-05-18 15:30 归档时 `docs/plans/202605082130-pg-corecell-open-issues.md` 的完整内容，含所有 ✅ closed marker 与证据。

---

# PG / accesscore / auditcore / configcore 待办问题清单

**生成日期**: 2026-05-08
**来源**: 整理自 `docs/backlog.md` + `docs/plans/archive/202605071200-033-pg-implementation-plan.md` + `docs/reviews/202605082044-pr417-pg-corecell-framework-analysis.md`
**用途**: 快速查阅，不重复 backlog 详情

---

## C. accesscore

| ID | 优先级 | 一句话 |
|---|---|---|
| ~~B2-C-02 SETUP-ADMIN-PUBLIC-ROUTE-PERMANENT~~ | ✅ closed | setup/service.go `setupRetiredError()`（410 Gone）+ fast-path Status + tx-scoped advisory `setupLock` + `adminprovision.Ensure` race-safe；handler.go bootstrap-only lifecycle（ADR §D1） |
| ACCESSCORE-ACCOUNT-LOCKOUT-AUTO-LOCK-01 | 🔴 P1 | sessionlogin 无失败累计 + 阈值 + auto-lock |
| ~~CELLS-IDENTITYMANAGE-LEVEL-MISLABEL-01~~ | ✅ closed | codegen funnel 锁定 cell.yaml consistencyLevel 真值（backlog T2） |
| ~~B5-FU-PG-RUNTIME-WIRING-AND-ARCHTEST-TYPE-AWARE-01~~ | ✅ closed | PR #482 + #587-t3：corebundle PG outbox/session 接通 + archtest 类型化 |
| ~~PR338-FU-LOGIN-DURABLE-TX-ATOMICITY-TEST~~ | ✅ closed | PR #482 S4a：setup_pg_integration_test.go TestSessionLogin_OutboxFailureRollsBackPGRows |
| PR392-FU-AUDIT-CHAIN-WIRING | 🟠 P2 待合同（event.session.auth-failed.v1 schema 未发布；S7 sub-slice 留接入 stub）|
| ~~P3-TD-10 TOCTOU 竞态修复~~ | ✅ closed | S4d credential 重构 → row-level provenance（ADR-credential-session-protocol §A8） |
| B2-PROVISIONER-MUTEX-REVIEW | 🟠 P2 | PG adapter 落地后审视 mutex 是否仍需 |
| ~~B2-T-02 RBACASSIGN event contract waiver expiry~~ | ✅ closed | S4c-T1：rbacassign contract_test.go 重写，删 waiver |
| ~~B2-T-07-FU-1 RBACASSIGN caller wiring~~ | ✅ closed | S4c-T1：handler.go RequireCallerCell 装载完成 |
| ~~B2-C-06 SessionLogout consumer action 无验证~~ | ✅ closed | `cells/accesscore/slices/sessionlogout/consumer.go:79-97` switch payload.Action + default DispositionReject |
| ~~PR280-FU1 CHANGEPASSWORD-CONCURRENT-SEMANTICS-01~~ | ✅ closed | service.go:862-869 RunInTx 包旧密码校验 + PasswordVersion CAS（S6 重构） |
| ~~PR267-FU-AUTHTEST-INTERNAL~~ | ✅ closed | `cells/accesscore/internal/authtest/`（admin.go/role.go/user.go）已 internal 化 |
| PR250-F3 Event wire byte pinning | 🟡 | 缺 byte 级回归 |
| X5 P3-TD-11 accesscore domain 拆分 | 🟡 P3 | User/Session/Role 拆分（卡 X1） |
| X13 REFRESH-PARTITION-01 | 🟠 P3 | `expires_at` range 分区，触发条件未达 |

## D. auditcore — S7 大部分 closed by PR #450（refactor/554-pg-s7-audit-ledger）

| ID | 状态 | 落地点 |
|---|---|---|
| ~~B2-C-01 Audit hashchain 重启未恢复尾节点~~ | ✅ closed S7 | runtime/audit/ledger.Store.Tail() + cell.Init 启动期 strict-tail-verify-on-startup |
| ~~AUDITAPPEND-L2-FAILURE-PROOF-01~~ | ✅ closed S7 | adapters/postgres/audit_ledger_store_test.go testcontainer outbox-writer fail injection |
| ~~B2-C-05 Auditappend actor 缺失降级不安全~~ | ✅ closed S7 | sub-slice service.go：actor 缺失 → outbox.Reject(NewPermanentError) fail-closed |
| ~~B2-C-09 Auditquery raw payload 直接回传~~ | ✅ closed S7 | auditquery/handler.go 走 pkg/redaction.RedactPayload 过滤敏感字段 |
| ~~B2-C-10 Auditappend 全局 mutex 串行化~~ | ✅ closed S7 | mutex 上提到 ledger.Store；PG 用 pg_advisory_xact_lock + SELECT FOR UPDATE |
| ~~B2-C-14 Hash-chain 跨重启连续性测试缺~~ | ✅ closed S7 | testcontainer cross-pool restart test in audit_ledger_store_test.go |
| ~~C-DC9 auditarchive 死代码~~ | ✅ closed S7 | 整 slice + s3archive adapter + ports/mem 全部删除（不留 stub） |
| ~~PR266-AUDITAPPEND-STRICT~~ | ✅ closed S7 | Protocol 默认 strict（json.Decoder.DisallowUnknownFields），删除 toggle 设计 |
| ~~CELLS-SLICE-MULTI-VERB-DECOMPOSE-01（auditappend）~~ | ✅ closed S7 | 13 topic → 4 sub-slice（auditappend-session/user/config/role），无共享 dispatch |
| PR392-FU-AUDIT-CHAIN-WIRING | 🟠 P2 待合同 | event.session.auth-failed.v1 schema 未发布；S7 auditappendsession sub-slice 留接入 stub，待合同发布后补充订阅 |

## E. configcore

| ID | 优先级 | 一句话 |
|---|---|---|
| ~~B2-T-01 Config rollback 乐观锁缺~~ | ✅ closed | service.go:149,199 `UpdateForRollback(... expectedVersion ...)` + handler.go:68 409 Rollback409ErrorResponse |
| ~~P3-TD-12 configpublish.Rollback 版本校验~~ | ✅ closed | 同上：expectedVersion CAS 已实现 |
| ~~CONFIGCORE-CACHE-LIFECYCLE-OWNER-01~~ | ✅ closed | won't-do（2026-05-16）：cache service-private，不需独立 Lifecycle |
| ~~C-02 CONFIGSUBSCRIBE-CACHE-LIFECYCLE~~ | ✅ closed | service.go:230-298 Start/StopTombstoneGC 完整状态机 + AfterStart 挂 Lifecycle；service.go:58 active entries 业务上天然有界（设计选择不做 LRU） |
| ~~B2-C-11 Configsubscribe tombstone 无 TTL~~ | ✅ closed | service.go:24-30 `defaultTombstoneTTL = idempotency.DefaultTTL`，sweepTombstones GC 已实现 |
| CONFIGCORE-RECEIVE-PLACEHOLDER-CLEANUP-01 | 🟡 P1 | 删 `accesscore/configreceive` 占位 |
| PR-CFG-A-DEFER-2 ConfigCore L2 divergence | 🟡 Cx1 | L2 与 L1 表项 schema 偏差 |
| ~~C-05 CELLS-CELLROUTES-PLACEHOLDER-DELETE~~ | ✅ closed | `configcore/cell_routes.go` 已删除（find 0 命中） |
| PR320-FU-CONFIGCORE-CI-NOOP | 🟡 P3 | noop publisher CI 路径未覆盖 |
| PR-CFG-G1-FU6 | 🟡 Cx2 | 余项 |
| ~~PR238-FU4 CONFIGREPO-LEGACY-NOTFOUND-TEST-DEDUP-01~~ | ✅ closed | config_repo.go:103 注释 + Test{Update,UpdateForRollback,Delete}_NotFound (line 347/392/430) 已 dedupe；`legacyNotFound` 0 命中证残留已删 |
| PR238-FU8 CONFIGREPO-UPDATE-ROLLBACK-OP-LABEL-TEST-01 | 🟠 P3 | 部分修复：PR#553 抽 opUpdate/opUpdateForRollback const + Update_NotFound 双向 NotContains（Medium）；Hard 升级 typed enum 跟 `CONFIGREPO-OP-LABEL-TYPED-ENUM-HARD-01` |
| ~~CELLS-SLICE-MULTI-VERB-DECOMPOSE-01（configread）~~ | ✅ closed | cell_init.go:14-15,202-219：configread (primary) + configreadinternal (internal) 两个独立 slice + 独立 Service instance |

## F. 横切（≥2 cell 或 PG 通用）

| ID | 优先级 | 一句话 |
|---|---|---|
| ~~A-01 OIDC-FAILFAST-MR-COMPLETENESS（含 A-07/A-08）~~ | ✅ closed | 7 adapter 实现 ManagedResource（postgres/redis/oidc/rabbitmq/s3/otel/vault）；websocket subresource/config 经 `adapterManagedResourceOptOut` 显式豁免；archtest `TestAdaptersExportedTypesManagedResourceOrOptOut` 静态守 |
| ~~ADAPTER-ERROR-CLASSIFICATION-TRANSIENT-01~~ | ✅ closed | PR #517：postgres/redis/s3 classifier + archtest Hard 闭环 |
| ~~ADAPTER-CONNECT-BUDGET-01~~ | ✅ closed | 4/4 adapter ConnectTimeout 已就位：postgres (PR#401) + redis (client.go:34,84 DialTimeout 5s) + rabbitmq (connection.go:78 ConnectTimeout 5s) + s3 (s3.go:167 HTTPClient.Timeout) |
| ~~REPO-HEALTHCHECKER-01~~ | ✅ closed | PR-REPO-READYZ（fix/202-repo-readyz, 2026-05-16）：typed funnel `cell.RegisterRepoReadiness` 就位 |
| ~~B2-R-02 Readyz 缺少 repo probe~~ | ✅ closed | PR-REPO-READYZ：configcore + auditcore 接入 config_repo_ready / audit_ledger_ready |
| ~~ADAPTER-MANAGED-RESOURCE-COMPLETENESS-01~~ | ✅ closed | 与 A-01 同源：`adapterManagedResourceOptOut` allowlist + archtest `TestAdaptersExportedTypesManagedResourceOrOptOut` 静态守 |
| ~~B2-X-03 PG invalid index warn continue~~ | ✅ closed | `adapters/postgres/schema_guard.go:948-969` `VerifyNoInvalidIndexes` fail-fast + `ErrAdapterPGInvalidIndex` |
| ~~B2-A-13 PG pool tx rollback 日志泄漏~~ | ✅ closed | `tx_manager.go:96-154` 全部走 `redaction.RedactError/RedactAny` |
| ~~PR-V1-PG-STARTUP-HARDEN-FU-RACE-COVERAGE~~ | ✅ closed | `.github/workflows/test-race.yml:87-128` `race-pg-integration` job 覆盖 adapters/postgres + cells/accesscore PG repos + l2atomicity |
| ~~B2-C-13 L2 跨层 e2e 回归不足~~ | ✅ closed | `tests/integration/l2atomicity/harness_test.go:115` "boots a full PG-backed assembly (accesscore + configcore + auditcore)" + auditStore 暴露给测试断言 audit chain |
| C-04 CELLS-INIT-TEMPLATE-CONVERGE（含 C-07） | 🟡 P2 | 3 cell Init 切分各异 + emitter health probe helper |
| C-09 CELL-SPLIT-LAYOUT-NORMALIZE | 🟡 P2 | accesscore + configcore 三文件范式不一致 |
| ~~M1-OBSERVED HEALTHZ-INTERFACE-PACKAGE-01~~ | ✅ closed | Health 实现已从 38 → 7（kernel/cell/registry + runtime/config/watcher + runtime/eventrouter/router + 4 adapter ManagedResource）；通过 `cell.RegisterRepoReadiness` typed funnel + `lifecycle.ManagedResource` 接口分层 |
