# 034 PG / accesscore / auditcore / configcore B 路线实施计划

**生成日期**: 2026-05-08
**最后更新**: 2026-05-14（v6 状态同步：**S4b** ship by PR #490 — authz_epoch closed loop + credentialinvalidate 3-op funnel + JWT jti/authz_epoch claims + sessionvalidate 503 + refresh reuse cascade + S4a 遗留 FU-1/FU-2 一并修复；剩余 **S4c** 串行收口 cleanup / L2 e2e + S4a FU-3b archtest Soft → Medium 升级；**D4** docs/contracts sync 仍未起步，含 S4a FU-3a login/refresh contract 403 漂移；新登记 PR #490 review FU 两条 backlog 走单独触发：REQUIRED-DEP-NIL-GUARD-01（Soft → Hard，archtest 触发）+ ENFORCESESSIONSTATE-HOTPATH-OPT-01（QPS 阈值触发））
**前一版**: 2026-05-13 v5（S4a ship by PR #482）
**对接来源**:
- `docs/reviews/202605082044-pr417-pg-corecell-framework-analysis.md`（B 路线源）
- `docs/plans/202605071200-033-pg-implementation-plan.md`（A 路线，已被本计划取代）
- `docs/plans/202605082130-pg-corecell-open-issues.md`（C/D/E/F 待办清单）
- `docs/architecture/202605101200-adr-typed-go-heavy-protocol-primitives.md`（v3 修订的范式锚点）

**关系**：本计划以 B 路线（typed-Go-heavy 协议 primitive + cell 消费）替代 033 A 路线（在 cell 内直接落 PG）；033 中的 migration / wiring 工作不废弃，重新组织为本计划下的 PR 子任务；033 §6 archtest 设计大部分降级删除（由 typed signature 覆盖）。

**v2 修订（2026-05-08）**：删除 v1 错误加入的"治理增强前置"；新增 ADR-B 接口归属决议。

**v3 修订（2026-05-10）**：基于 typed-Go-heavy ADR：
- **删除 ADR-B**：6 条边界规则由 Go 类型系统天然画出（sealed interface / import 方向 / 强依赖 wiring），不再需要文字 ADR
- **改写 S2/S6/S7**：从"runtime library 抽取"改为"typed Protocol primitive"
- **改写 S3+S5**：PG store 是 Protocol 的 conform 实现；archtest 大部分降级
- **改写 S4**：composition root 显式构造 typed Protocol，cell 注入消费
- **新增 ADR-Typed**（已落 `docs/architecture/202605101200-...`）：作为 B 路线真正的架构前置
- **工时降低**：~148h → ~102h dev（archtest 工作由 typed signature 替代）

---

## 1. 路线选择与原则

### 1.1 为什么必须先抽框架（业务形态根因）

accesscore（认证授权）/ configcore（配置控制面）/ auditcore（证据链）三个 cell 本质是**协议状态机**，不是普通业务 CRUD。把 session 状态机 / CAS / hash chain 写在 cell 内，每接一种存储介质就要重做一次协议设计 —— PR#417 是该模式的爆破点（5 个 P0/P1 协议缺口同时冒出）。

archive `202604201800-pg-pilot-layering-refactor-plan.md` 是同形态的前一次循环（加密/生命周期/Cell组装/治理 4 维度漂移），R1a-R1e 五 PR 解决。本次 PG 接入面对的是同一模式的新维度。

### 1.2 B 路线核心：typed-Go-heavy 协议 primitive

**协议决策必须在最早能用类型系统检查的层级（typed Go primitive）**，由 sealed interface + Option 范式 + composition-root 显式构造组装；不允许藏在 implementation 层（cell 内部 / SQL / mem store 字段）。详见 `docs/architecture/202605101200-adr-typed-go-heavy-protocol-primitives.md`。

具体形态：
- 每个跨 cell 协议（session / cas / audit ledger）落地为 `runtime/{auth,state,audit}/*` 下的 typed `Protocol` struct + sealed Option
- mem 与 PG store 共享 `storetest.Run(t, factory, protocol)` conformance suite
- composition root（`cmd/corebundle/`）显式 `MustNewProtocol(...)` 构造并注入 cell
- typed-nil reject + phase0 fail-fast 让"忘了构造协议"在启动期暴露

### 1.3 与 K-04 / funnel-first / Option 范式 的对齐

- **K-04**（cells 留 framework 仓）：cell 仍是协议本体（消费 typed Protocol + 业务决策），无矛盾
- **funnel-first**（CLAUDE.md `## 新增 invariant 决策原则`）：typed Go 是优先级 #2 "type system 自然拦"的具体形式；archtest 退到 #3 兜底
- **Option 范式分层**（`runtime-api.md`）：typed Protocol Option 沿用强依赖 wiring vs 累加式 builder 的现有判定规则

### 1.4 真正的架构前置（已收敛到两份）

- **ADR-Typed**（已落 `docs/architecture/202605101200-...`）：typed-Go-heavy 范式锁定。**v3 通过本 ADR 替换 v2 的 ADR-B 6 条文字边界规则**——边界由 Go 类型系统天然画出，无需文字规则。
- **S1 协议 ADR**：PR#417 §12 三个待决问题（admin 不变量 / login vs role revoke 排序 / access token 状态模型）+ credential 失效协议；产物形态是 **typed Option 列表 + Go 头文件骨架**（不只是文字决议）

### 1.5 PR 包决策原则

1. **协议决策落 typed Option**（S1 产物含 Go 头文件，不只是文字 ADR）
2. **typed Protocol primitive + mem + storetest 同 PR**（不留半成品）
3. **PG store 实现 + schema migration 同 PR**（schema 一次落地）
4. **cell 接入 PR 把同主题 backlog 顺路收**（不留小尾巴）
5. **composition root 显式构造**（不允许"默认能跑"）
6. **路线外独立项**（adapter 通用 / 各 cell 死代码清理）走自己的小 PR，不并入主线

---

## 2. PR 包总览

| PR | 主题 | 类型 | 依赖 | 并行可行 | 收口 backlog 项 |
|---|---|---|---|---|---|
| **S0** ✅ | CI integration discovery | infra | — | **shipped #433** | (路线门禁) |
| **ADR-Typed** ✅ | typed-Go-heavy 协议 primitive 范式 | ADR 文档 | — | **已落** `202605101200-...` | (B 路线全体前置) |
| **S1** ✅ | Credential/Session/Admin 协议 → typed Option 列表 | ADR + Go 头文件 | ADR-Typed | **shipped #439** | 三个待决策问题 |
| **S2** ✅ | `runtime/auth/session` typed Protocol + mem + storetest | typed Go primitive | S1 | **shipped #444** | — |
| **S3+S5** ✅ | PG session store conform + users/roles schema + admin 不变量 | adapter + migration | S2 | **shipped #449** | B2-C-02 / admin UNIQUE / A26-R4 |
| **S3F** ✅ | PG migration/schema hardening (DetectInvalidIndexes 锁内 + schema_guard 全表 shape + destructive Down GUC + users CHECK 状态/source) | adapter 加固 | S3+S5 | **shipped #465** | B2-X-03 readyz 联动半边 / 17-19 destructive guard |
| **S4.0** ✅ | effective last-admin invariant (status=active∧admin 原子语义 + DB trigger 一致) | cell + adapter | S3+S5 | **shipped #476** | PR#449/#459 review last-admin 语义漏洞 |
| **S4a** ✅ | PG session/refresh durable wiring (删 mem session/refresh + composition-root 显式 protocol + forced re-login + refresh stable-sid + sessionlogout 503/404 区分) | cell 接入 | S3F / S4.0 ✅ | **shipped #482** | S4-PG-SESSION-REFRESH-WIRING / PR338-FU / B5-FU |
| **S4b** ✅ | authz_epoch + credential event closed loop (JWT jti+authz_epoch claims + credentialinvalidate 3-op funnel + 5 credential events 路由 + sessionvalidate epoch 比对 + refresh reuse cascade) **+ S4a FU-1/FU-2**：rbacassign 删 `syncSessionRevocation` 二态 + same-tx revoke 恢复（ADR D5 合规）；sessionvalidate `enforceSessionState` 路径 store/userRepo infra error 改 `KindUnavailable` → 503 (`ErrAuthServiceUnavailable`) | cell + JWT | S4a ✅ | **shipped #490** | JWT-AUTHZEPOCH-CLOSED-LOOP / B2-C-06 SessionLogout action 校验 / TOCTOU-10（ChangePassword in-tx + bcrypt + epoch bump 原子） |
| **S4c** | accesscore cleanup / race / L2 e2e (level audit / RBAC waiver / AUTH-CACHE / L2 e2e) **+ S4a FU-3b**：`session_protocol_composition_root_test.go` / `refresh_invariants_test.go` Soft → Medium (type-aware) 升级 | cleanup | S4b ✅ | — | LEVEL-MISLABEL / B2-T-02/07 / B2-C-13 / AUTH-CACHE / PR250-F3 / PR267-FU |
| **S6** ✅ | `runtime/state/cas` typed Protocol + configcore + accesscore password_version 接入 | typed primitive + 双消费 | S1 | **shipped #464** | E 表 2 项 + C 表 1 项 |
| **S7** ✅ | `runtime/audit/ledger` typed Protocol + PG + auditcore 接入 | typed primitive + adapter + cell | S1 | **shipped #450** | D 表 9 项 + PR392-FU |
| **W9** ✅ | outbox factory adoption | 机械迁移 | — | **shipped #434** | 033 W9 |
| **B2.B** | PG-DEVICECELL-REPO | adapter + migration | — | ✅ 与 examples 业务无关 | 033 B2.B |

**~~ADR-B 接口归属决议~~**：v3 删除。6 条边界规则由 typed Go 类型系统天然画出（sealed interface 决定接口归属，import 方向决定路径归属，`runtime-api.md` Option 范式决定事务关系），无需文字 ADR。

**B5.FU PG-REFRESH-RUNTIME-WIRING 自然消化**：在 S4 后段完成（access_module postgres 分支删 `WithInMemoryDefaults`），不再独立。

---

## 3. 依赖图与执行波次

```
Wave 0（立即并行起）：
  S0 ──┐
  W9 ──┼── 全部独立，无下游
  B2.B─┘

Wave 1（关键路径，单份 ADR）：
  ADR-Typed  ✅ 已落 (202605101200)
  S1         协议 ADR + typed Option 列表（产物含 Go 头文件骨架）

Wave 2（S1 通过后）：
  S2 ✅ ──→ S3+S5 ✅ ──→ S3F ✅ ──→ S4.0 ✅ ──→ S4a ✅ ──→ S4b ✅ ──→ S4c
                                                              
  S6 ✅（state/cas 路径）                  [与 S2-S5 并行已完成]
  S7 ✅（audit ledger 路径）               [与 S2-S6 并行已完成]
```

**worktree 容量**：Wave 0 三个 + Wave 1 S1 = 4 worktree；Wave 2 已完成框架抽取（S2/S6/S7）、PG adapter（S3+S5/S3F）、S4.0 effective last-admin invariant、S4a PG session/refresh durable wiring、**S4b authz_epoch + credential event closed loop**。当前 wave 焦点：**S4c 单 PR 收口** accesscore 余下 cleanup / L2 e2e / archtest 升级；D4 docs sync 与 DX4 maintainability 可并行小 PR。

**关键路径（v6）**：S2/S3+S5/S3F/S6/S7/S4.0/S4a/**S4b** 已 ship；当前关键路径 = **S4c (cleanup / race / L2 e2e + S4a FU-3b archtest Soft → Medium 升级)**。

**v3 简化**：v2 的 ADR-B（接口归属 6 条边界规则）已被 ADR-Typed 替代，关键路径从"两份 ADR"简化为"一份 S1"。

**v4 状态同步（2026-05-12）**：S3+S5 (PR#449) / S7 (PR#450) / S6 (PR#464) / S3F (PR#465) / S4.0 (PR#476) / S4 部分 carry-over (PR#459 LastAdminGuard + setup_pg_integration_test outbox event 断言) 全部已 ship；S4 单巨型 PR 改为 S4.0/S4a/S4b/S4c 四个 correctness-first PR，避免 PR #445 类多维捆绑教训。

**v5 状态同步（2026-05-13）**：S4a (PR #482) 已 ship —— PG session/refresh durable wiring + refresh stable-sid (OAuth2/OIDC sid 稳定，撤回 fd954cb8 revoke-old+create-new 反模式) + sessionlogout 503/404 区分 + outbox-fail integration spy + ADR-credential D4.1/D4.2。PR #482 review 暴露三件未在 S4a 同 PR 修复的 finding：(1) rbacassign `syncSessionRevocation` 二态使 ADR D5 same-tx credential invalidation 在 durable mode 退化，(2) sessionvalidate 路径 store.Get infra fault 仍统一返回 401 而非 503（PR #482 只修了 sessionlogout 路径），(3) login/refresh contract.yaml 403 description / 状态码声明漂移 + `session_protocol_composition_root_test.go` / `refresh_invariants_test.go` 仍是 Soft archtest。这三件**不立 backlog 条目**，按 PR review findings 默认 in-scope 原则直接折进 S4b（P1 二件）/ D4（contract 漂移）/ S4c（archtest 升级）同 PR 处理。

**v6 状态同步（2026-05-14）**：S4b (PR #490) 已 ship —— JWT 写入 `jti` + `authz_epoch` claims（删 `AuthzEpochAtIssue: 0` 硬编 placeholder）；新增 `credentialinvalidate.Invalidator` 3-op 原子 funnel (`BumpAuthzEpoch` + `RevokeForSubject` + `RevokeUser`)；identitymanage Lock/Delete/ChangePassword/Update demotion + rbacassign Revoke 共 5 处 credential event 全部路由进 funnel；sessionvalidate `enforceSessionState` 加 `userRepo.GetByID` epoch 比对 + session/userRepo infra error → `KindUnavailable` 503 (`ErrAuthServiceUnavailable`)；refresh reuse 检测命中走 `CredentialEventRefreshReuse` cascade revoke；sessionlogout consumer 降级 audit/ack-only + Action enum 白名单（B2-C-06 闭环）；migration 025 删 `sessions.authz_epoch_at_issue` 列（claim 已带，行内 pin 零额外防御）；archtest 新增 `credential_invalidate_funnel_invariants_test.go` + `sessionvalidate_epoch_compare_test.go`；ADR-credential §D2 完整生效。S4a 遗留 FU-1（rbacassign same-tx revoke 恢复，删 `syncSessionRevocation`）/ FU-2（sessionvalidate 503 区分）一并 in-scope 闭环。

PR #490 review 暴露两件**未在 S4b 同 PR 修复**的 finding，因属"未来触发型"，落 backlog 独立条目（非"PR review findings 默认 in-scope"违反——已通过工时显著超 S4b 范围 + AI-rebust 升级 ADR / QPS 阈值前不可观测的明确触发条件论证）：
- **REQUIRED-DEP-NIL-GUARD-01**（cap-14）— OUTBOX-SERVICE-01 archtest scope 只守 `txRunner` 一个字段，PR #490 第五轮 review 手动补齐五处 service 的 `validation.IsNilInterface` guard；改 typeseval-based Soft → Hard archtest 触发条件为"下次新增 service"
- **ENFORCESESSIONSTATE-HOTPATH-OPT-01**（cap-14）— `enforceSessionState` 两次串行 PG 读（`sessionStore.Get` + `userRepo.GetByID`）；S4b §HIGH-4 决策不包 read-only tx；触发条件为"生产 QPS / P99 延迟阈值"

**剩余进度（v6）**：
- **S4c**：accesscore cleanup 单 PR（S4a FU-3b archtest 升级 + 9 项 cleanup backlog）
- **D4**：accesscore docs/contracts sync 并行小 PR（含 S4a FU-3a login/refresh 403 description 漂移）
- **DX4**：PG adapter maintainability（type assertion 消除 / archtest 自动扫 PG repo）
- **B2.B**：PG-DEVICECELL-REPO 独立 worktree（与 S4c 并行）

---

## 4. 各 PR 包详细内容

### S0 CI integration discovery

**目的**：先修验证基础设施，避免后续 PR 因 CI integration 误判返工。

**内容**：按 `//go:build integration|e2e` 文件发现 package，archtest 防退化。

**收口**：路线门禁（不直接收 backlog 条目，但所有后续 PR integration test 依赖）。

---

### ~~ADR-B B 路线接口归属决议~~（v3 删除）

**v3 删除理由**：6 条边界规则由 typed Go 类型系统天然画出：

| 原 ADR-B 边界规则 | typed-Go-heavy 自动答案 |
|---|---|
| 1. K-04 张力（cell 本体 vs 示范） | runtime/{auth,state,audit}/* 暴露 typed Protocol primitive；cell 消费 + 业务决策；无歧义 |
| 2. Repository / Store 接口归属准则 | 接口形态决定：sealed Protocol 在 runtime（typed primitive），业务实体 Repository 在 cell 私有；调用方 import 方向自然画出 |
| 3. `adapters/postgres` 路径归属 | runtime Protocol 的 PG conform 实现在 `adapters/postgres/`；cell 私有 entity repo 留 `cells/{cell}/internal/adapters/postgres/`（混合形态，但由 import 方向唯一确定） |
| 4. Store 接口与事务的关系 | 沿用 033 §5.3 ambient TX；archtest `PG-REPO-AMBIENT-TX-01` 保留（typed signature 不能拦调用形态） |
| 5. storetest conformance 位置 | 每协议自带 `runtime/{auth,state,audit}/*/storetest/`，suite 接受 typed Protocol 实例（参见 ADR-Typed §4.3） |
| 6. `runtime/auth` 子包结构 | runtime/auth/session 提供 typed Protocol + Store + storetest；与 jwt/oidc/federated 对等并列 |

详见 `docs/architecture/202605101200-adr-typed-go-heavy-protocol-primitives.md` D2/D3。

---

### S1 Credential / Session / Admin 协议 → typed Option 列表

**v3 修订**：S1 产物从"纯文字 ADR"升级为"ADR + Go 头文件骨架"。文字 ADR 决议产品语义；Go 头文件落 typed Option 形态，供 S2 直接消费。

**目的**：把三个待决策问题一次决定，避免 S2-S5 边写边改。

**内容（文字 ADR 部分）**：
- access token 不落明文 / session 表存 HMAC fingerprint 还是 jti
- password reset / lock / delete / role revoke 对旧凭据的统一失效协议
- login 与 role revoke 的排序点（per-user advisory lock / authz epoch / role version）
- refresh chain 与 session revoke 的边界
- **admin 不变量**：至少一个 vs 只能一个（PR#417 §12 决策点）
- session/refresh revoke 与事务边界

**Go 头文件骨架（必含）**：
```go
// runtime/auth/session/protocol_options.go (S1 产物，body 留 S2 实现)

type FingerprintMode interface{ fingerprintModeOK() }      // sealed
type CredentialEvent int                                    // typed enum
type OrderingModel interface{ orderingModelOK() }          // sealed

func WithFingerprintHMACSha256(key []byte) Option           // 决议 1
func WithFingerprintJTI() Option                            // 决议 1（备选）
func WithRevokeOn(events ...CredentialEvent) Option        // 决议 2/3
func WithOrdering(om OrderingModel) Option                 // 决议 3
// ... admin 不变量在 cell.yaml 还是 typed Go protocol，S1 决议
```

**输出**：
- `docs/architecture/202605xx-adr-credential-session-protocol.md`（文字 ADR）
- `docs/architecture/202605xx-adr-admin-invariant.md`（文字 ADR）
- `runtime/auth/session/protocol_options.go`（typed Option 头文件骨架，S2 PR 内填实现）

---

### S2 `runtime/auth/session` typed Protocol primitive

**v3 修订**：S2 产物形态从"runtime library 接口集合"改为"typed Protocol primitive"，参见 ADR-Typed §4 session protocol 原型。

**目录**：
```
runtime/auth/session/
  protocol.go              ← typed Protocol struct + Option 实现（S1 已落骨架，S2 填实现）
  protocol_options.go      ← S1 已落骨架（sealed interface / typed enum / Option 签名）
  store.go                 ← Store interface（方法形态由 Protocol 决定）
  mem_store.go             ← mem 实现
  storetest/suite.go       ← Run(t, factory, protocol *Protocol) 即 Protocol-driven suite
```

**内容**：
- typed `Protocol` struct + sealed Option（实现 S1 头文件骨架）
- `NewProtocol(opts ...Option) (*Protocol, error)` fail-fast 校验互斥与必填
- `MustNewProtocol(...)` composition-root 包装
- `Store` interface（Create / Get / Revoke / RevokeForSubject）
- mem 实现
- `storetest.Run(t, factory, protocol)` Protocol-driven 派生 test cases
- 不含 admin/role name/password policy 等产品语义（cell 内）

**约束**：
- 不接 accesscore，PR 独立可审查
- 协议 Option 不允许在 `cells/` / 内部代码构造（仅 `cmd/` composition root），由新增 archtest `SESSION-PROTOCOL-COMPOSITION-ROOT-01` 兜底守

**收口**：暂无 backlog（铺路，S4 消费）。

---

### S3+S5 PG session store conform + users/roles schema + admin 不变量（合并 PR）

**v3 修订**：PG `session_store.go` 是 S2 typed Protocol 的 conform 实现，签名由 typed Protocol 推出；users/roles 是 cell 私有 entity repo（不升 runtime），仍按 cell 内 Repository interface 落实。033 §6 archtest 大部分降级删除（由 typed signature 覆盖）。

**合并理由**：schema 一次落地，admin UNIQUE 与 users 表是 DDL 层共生关系，不应拆 PR；migration 017-019 三条 SQL 同 PR 避免 schema_guard 文本合并冲突。

**文件域**：
- `adapters/postgres/session_store.go`（实现 S2 typed Store；构造函数签名 `NewSessionStore(pool, txMgr, *session.Protocol) (*SessionStore, error)`，phase0 fail-fast on nil deps）
- `adapters/postgres/{user,role}_repo.go`（cell 私有 entity repo 的 PG 实现；从 `cells/accesscore/internal/ports/` import 接口）
- `adapters/postgres/migrations/017_users.sql` / `018_sessions.sql` / `019_roles.sql`
- `adapters/postgres/schema_guard.go` 首落主体 + 3 表
- `adapters/postgres/errcode.go` append PG 错误码
- `cmd/corebundle/setup_integration_test.go` 加 testcontainer e2e
- `runtime/auth/session/storetest/suite.go` 在 `pg_session_store_integration_test.go` 中以 PG factory + 同一 Protocol 实例运行

**archtest 调整（vs 033 §6）**：

| 原 INVARIANT | 处置 |
|---|---|
| `PG-REPO-CONSTRUCTOR-FAIL-FAST-01` | **删除**：由 typed `(*T, error)` 签名 + body 顶层校验直接覆盖 |
| `PG-REPO-AMBIENT-TX-01` | **保留**：调用形态约束（写路径强制 `txRunner.RunInTx`），typed signature 不能拦 |
| `PG-REPO-INVARIANT-LIST` 索引 | **删除**：由 storetest 注册派生，不需 grep 锚点 |

**新增 archtest（兜底）**：
- `SESSION-PROTOCOL-COMPOSITION-ROOT-01`：`session.NewProtocol` / `MustNewProtocol` 仅在 `cmd/` 调用（防止 cell 自定义协议绕过 composition-root 决策）

**收口 backlog**：
- B2-C-02 SETUP-ADMIN-PUBLIC-ROUTE-PERMANENT 🔴 P0（schema + setup 边界一起处理）
- A26-R4 SETUP-ORPHAN-E2E-01（顺路 testcontainer e2e）
- ACCESSCORE-PG-USERS-MIGRATION-01（B3 历史项，admin UNIQUE 在此 PR 决议）
- B2-X-03 PG invalid index warn continue（PG schema 启动 fail-fast 在此 PR 配套）
- B2-A-13 PG pool tx rollback 日志泄漏（顺路，PG adapter 同主题）
- PR-V1-PG-STARTUP-HARDEN-FU-RACE-COVERAGE（PG integration test 加 -race）
- **PR444-FU-SESSIONSTORE-BENCH-01** 🟡 P2（PR #444 review carry-over）：不进 S3+S5/S4 correctness 主线，移入 DX4/后续 benchmark PR；等 durable session/refresh 正确性落定后，再在 `runtime/auth/session/storetest/` 新增 1000+ session × subject scope `RevokeForSubject` 与 mixed Create/Get/Revoke 并发 benchmark suite

---

### S3F PG migration/schema hardening（PR #449/#459 review follow-up，S4 前置）

**目的**：把 PG migration / schema guard / destructive down 的基础安全面先补齐，避免 S4 的 session/refresh wiring 建在可漂移 schema 上。

**依赖**：S3+S5 已落地后；必须早于 S4a。

**文件域**：
- `adapters/postgres/migrator.go`
- `adapters/postgres/schema_guard.go`
- `adapters/postgres/migrations/017_users.sql` / `018_sessions.sql` / `019_roles.sql`
- 新增后续 migration（如 `020_*`）修补已合入 schema 的 constraint / trigger / guard
- `adapters/postgres/*integration_test.go`

**内容**：
- `DetectInvalidIndexes` 移入与 goose session locker 同一 migration 临界区；或在锁内区分 in-progress concurrent index 与 orphan invalid index，避免多实例 rollout 误杀正常 `CREATE INDEX CONCURRENTLY`
- `schema_guard.VerifyExpectedShape` 扩到 `users` / `sessions` / `roles` / `role_assignments` 的真实 catalog shape：列类型 / nullability / default / PK / unique / FK / index / trigger enabled / trigger function / CHECK constraint
- 017/018/019 destructive `Down` 增加 SQL-side fail-closed GUC guard，避免 direct goose/sql 绕过 Go API `DestructiveDownPermit`
- `users.status` / `users.creation_source` 增加 DB `CHECK`；repo write + scan 走 domain validator，非法状态启动或读写期 fail-fast
- 更新 schema guard 表清单注释，避免"守护实现已扩展、文档清单仍旧"漂移

**验收**：
- integration test 覆盖 wrong-shape 同名表、缺 FK/unique/trigger、disabled trigger、非法 status/source
- migration test 覆盖 direct Down 默认失败，显式 GUC 才允许
- race/integration lane 至少覆盖 adapters/postgres 的 migration guard 路径，并避免 `[no tests to run]` 静默通过

**不放入 S4 的原因**：这是 PG 基础设施硬化，不依赖 accesscore session/refresh 语义；前置可降低 S4a/S4b 的回归噪音。

---

### S4.0 effective last-admin invariant ✅ shipped (PR #476)

**目的**：修复 PR #449/#459 review 发现的 last-admin 语义漏洞：当前 guard 只统计 role assignment，不能保证至少一个可登录 admin。

**依赖**：S3+S5；可与 S3F 串行或小范围并行，但必须早于 S4b 的 credential event 闭环。

**证据**：PR #476 `feat(accesscore): effective last-admin invariant (S4.0)`：
- migration `024_effective_admin_invariant.sql` 新建 effective-admin trigger（status=active∧admin role），覆盖 direct SQL / cascade delete / 并发 revoke·delete·lock
- `cells/accesscore/internal/domain/admin.go` 重写 invariant 计算，统一 service + repo 语义
- `identitymanage.{Lock,Delete}` / `rbacassign.Revoke` 改为 "if another effective admin remains" 原子语义
- mem repo 同一 mutex 下等价语义；PG repo 复用 trigger 让 raw SQL 通过
- 三条 contract（`user/delete` / `user/lock` / `role/revoke`）补 403 error code
- `identitymanage_last_admin_protection_test.go` 新增 archtest 锁定语义

**文件域**：
- `cells/accesscore/internal/domain/admin.go`
- `cells/accesscore/slices/identitymanage/service.go`
- `cells/accesscore/slices/rbacassign/service.go`
- `cells/accesscore/internal/{adapters/postgres,mem}/role_repo.go`
- `cells/accesscore/internal/{adapters/postgres,mem}/user_repo.go`
- 新增后续 migration（如 `02x_last_admin_effective_guard.sql`）修补 DB trigger/function
- `contracts/http/auth/{user/delete,user/lock,role/revoke}/v1/contract.yaml`

**内容**：
- 将 invariant 明确定义为：至少一个 `status=active` 且持有 `admin` role 的 effective admin；`locked` / `suspended` 不计入可用 admin
- `identitymanage.Lock/Delete`、`rbacassign.Revoke` 统一使用 "if another effective admin remains" 的原子语义；避免 service 先 count、repo 后 mutate 的 TOCTOU
- PG trigger / SQL path 也按 effective admin 统计，覆盖 direct SQL、cascade delete、并发 revoke/delete/lock
- mem repo 在同一 mutex 下实现等价语义，避免测试与 PG 语义分叉
- 合同更新：last-admin 保护的 403 error code / description 在 delete、lock、role revoke 三条 contract 中显式声明

**验收**：
- 两个 admin 连续 lock：第二次必须拒绝
- 两个 admin 并发 lock/delete/revoke：最多一个成功，最终至少一个 effective admin
- 一个 active admin + 一个 locked/suspended admin：不能删除/锁定/撤销 active admin 的最后 admin role
- raw SQL / cascade delete integration test 验证 DB trigger 与 service guard 一致

**不放入 S4a 的原因**：它是 admin availability invariant，不依赖 durable session store；先修可立即消除"锁光管理员"风险，并给 S4b 的 credential event 提供稳定前提。

---

### S4 accesscore composition-root 显式构造 + cell 注入 + 残留 P1/P2

**v3 修订**：从"cell 内 import runtime"模式改为"composition root 显式构造 typed Protocol → 注入 cell"。cell 内不再有"接入 runtime"的工作，仅消费注入的 typed Protocol + Store；省下"接入"工时。

**文件域**：
- `cmd/corebundle/access_module.go`：postgres 分支显式 `session.MustNewProtocol(...)` 构造，参数从 S1 决议；`accesscore.New(WithSessionProtocol(...), WithSessionStore(pgSessionStore))` 注入；删 `WithInMemoryDefaults`（B5.FU 消化）
- `cells/accesscore/cell.go` + `cell_options.go`：新增 `WithSessionProtocol` / `WithSessionStore` option（强依赖 wiring 范式，typed-nil reject）
- `cells/accesscore/slices/{sessionlogin,sessionlogout,refresh}/`：service 接受注入的 `*session.Protocol` + `session.Store`，不再 import `runtime/auth/session/mem`
- `cells/accesscore/cell_init.go`：Redis session cache adapter 注入
- `cells/accesscore/internal/{ports,domain,mem}/`：5 联动激活

**PR449 follow-up 已拉前（不再作为 S4 carry-over）**：LastAdminGuard 基础 service wiring 已接入 `identitymanage.Delete` / `Lock`（`rbacassign` 既有 `RemoveFromUserIfNotLast` 继续负责 revoke），DB trigger 增加 transaction-scoped advisory lock 覆盖 direct SQL / cascade delete 并发；`cmd/corebundle/setup_pg_integration_test.go` 已改用 `adapterpg.NewOutboxWriter` 并断言已提交的 `event.user.created.v1` outbox row，另用 writer 失败注入验证 setup user/role/outbox 在真实 PG 事务内同步回滚。2026-05-11 review 新发现的 effective-admin 语义缺口由 **S4.0 (PR #476)** 收口（status=active∧admin role effective invariant + DB trigger + contract 403）。

**2026-05-11 范围裁剪（PR #449/#459 review 追加）**：S4 不再作为单个巨型 PR 承载所有残留项；拆成 S4a/S4b/S4c 三个 correctness-first PR。判断标准：必须与 session store、refresh store、JWT epoch、credential invalidation 的事务/撤权语义同闭环，才进入 S4；纯 migration、docs、DX、benchmark 不塞入 S4 主线。

#### S4a PG session/refresh durable wiring ✅ shipped (PR #482)

**ship 摘要（2026-05-13）**：
- B1-B5 全部完成：cell 内 SessionRepository / mem session 路径删除，slices 改消费 `runtime/auth/session.Store`；rbacassign + identitymanage 改 `sessionStore.RevokeForSubject(ctx, subjectID, CredentialEvent)`
- `cmd/corebundle/access_module.go` postgres 分支删 `WithInMemoryDefaults` for session/refresh，显式 `session.MustNewProtocol(...)`
- refresh 改 stable-sid 模型（撤回 fd954cb8 "revoke-old + create-new"），对齐 RFC 6749 §6 + OIDC sid stability + ory/fosite + zitadel + keycloak；`SESSIONREFRESH-NO-SESSION-CREATE-01` (Medium type-aware archtest) 静态拦截 refresh slice 调用 `session.Store.Create/Revoke/RevokeForSubject`
- sessionlogout 区分 503 (`ErrAuthLogoutUnavailable` KindUnavailable) vs 404 (`ErrSessionNotFound` CategoryDomain)；mem + PG store 同步用 `CategoryDomain` 标记 not-found，`IsInfraError` 正确分类
- `TestSessionLogin_OutboxFailureRollsBackPGRows`（PG + spy 命中 `event.session.created.v1` 失败注入）+ `TestSessionRefresh_TwoHops_PG`（多跳 refresh 链 stable-sid 回归）
- ADR `docs/architecture/202605101400-adr-credential-session-protocol.md` 加 D4.1 (refresh session-stable) + D4.2 (session retention，无 per-cell GC worker)
- `.env.example` `GOCELL_CELL_ADAPTER_MODE` 文档化 accesscore session/refresh 覆盖 + 首次切 PG 强制 re-login

**ship 后 in-scope follow-up（不开新 backlog 条目）**：PR #482 review 暴露三件未在 S4a 同 PR 修复，按"PR review findings 默认 in-scope"直接挂下游 PR 范围 —
- **FU-1（→ S4b 同 PR）**：rbacassign 删 `syncSessionRevocation bool` 二态字段；durable mode 恢复 same-tx `sessionStore.RevokeForSubject + refreshStore.RevokeUser`（与 identitymanage ChangePassword 对齐，ADR-credential §D5 合规）；`event.role.{assigned,revoked}.v1` consumer 降格为 fanout/audit，不再作为 primary credential invalidation 路径
- **FU-2（→ S4b 同 PR）**：sessionvalidate `enforceSessionState` 把 `sessionStore.Get` 的 infra error 从 `ErrAuthInvalidToken`（KindUnauthenticated）改为 `KindUnavailable` + 新错误码 `ErrAuthServiceUnavailable`；`runtime/auth/middleware.go` AuthMiddleware 按 Kind 分流 401 / 503；防枚举仍统一文案（redaction 在 wire 层做，不在 errcode.Kind 层做）。与 PR #482 sessionlogout 的 503/404 区分对齐到 sessionvalidate 路径
- **FU-3a（→ D4 并行小 PR）**：`contracts/http/auth/login/v1/contract.yaml` 403 description 由 `ERR_AUTH_PASSWORD_RESET_REQUIRED` 改为反映实际代码路径（`ErrAuthUserNotActive` for suspended/locked）；`contracts/http/auth/refresh/v1/contract.yaml` 补 403 声明（refresh 返回 `ErrAuthUserNotActive`）；CH-04 双源校验通过
- **FU-3b（→ S4c 同 PR）**：`tools/archtest/session_protocol_composition_root_test.go` 升级为 type-aware（`typeseval.ResolvePackageRef`），拒绝 `import sess "..."; sess.NewProtocol(...)` 绕过；`_test.go` 排除策略显式记入 godoc。`tools/archtest/refresh_invariants_test.go` 把守护从旧 `sessionRepo.*` API 改守 `Peek → sessionStore.Get → userRepo.GetByID → Rotate` 新形态。AI-rebust 评级 Soft → Medium

**范围**：
- `cmd/corebundle/access_module.go` postgres 分支删除 `WithInMemoryDefaults` 对 session/refresh 的隐式兜底；显式构造 `session.MustNewProtocol(...)`
- accesscore 注入 `session.Store` + refresh store；PG backend 下 session/refresh 全部落 PG
- 删除或隔离 cell-private `SessionRepository` / cell-internal mem session 在 PG 模式下的路径
- `sessionlogin.persistSessionWithRefresh` 的 session + refresh 写入与 PG tx 同一回滚边界
- 启动/升级期 forced re-login 策略：旧 mem session 不迁移，PG backend 首次启用要求全员重新登录
- **refresh 保持 session.ID 稳定**：refresh 不创建 / 不撤销 / 不更新 session 行；access JWT 跨 refresh 共享同一 sid claim，仅 jti / exp 推进。对齐 OAuth2 RFC 6749 §6 + OIDC Back-Channel Logout sid stability + ory/fosite / zitadel / keycloak 业界惯例（PR #482 review 撤回 fd954cb8 "revoke-old + create-new" 反模式，详见 ADR-credential D4.1）
- AuthzEpoch / role snapshot 在 refresh 时通过 user state 重新签发 claims（claims-at-sign），但**不写回 session 行**；真正的 epoch 闭环留给 S4b

**收口项**（PR #482 已 ship）：
- `S4-PG-SESSION-REFRESH-WIRING-COMPLETE-01` ✅
- `PR338-FU-LOGIN-DURABLE-TX-ATOMICITY-TEST` ✅ backlog 已打标 PR #482
- `B5-FU-PG-RUNTIME-WIRING-AND-ARCHTEST-TYPE-AWARE-01` ✅ backlog 已打标 PR #482（archtest 类型化的 `session_protocol_composition_root` / `refresh_invariants` 升级折入 S4c，不另开 backlog）
- `SESSIONREFRESH-NO-SESSION-CREATE-01` ✅ Medium type-aware archtest 已落 `tools/archtest/sessionrefresh_no_session_create_test.go`

**验收**：
- login happy path 创建 PG session + PG refresh row
- refresh/logout/revoke 对 PG 行生效，重启后状态仍一致
- refresh 链可连续多跳：login → refresh → 用返回的 refreshToken 再 refresh → 200（PR #482 P1 复现测试 `TestSessionRefresh_TwoHops_PG`）
- 故障注入证明 PG tx rollback 会回滚 session/refresh row，不再留下 mem-in-tx 脏状态；`TestSessionLogin_OutboxFailureRollsBackPGRows` 通过 spy + topic 断言定位失败注入点在 `event.session.created.v1` emit
- PG backend 启动时若 session/refresh store 未注入，phase0 fail-fast
- `SESSIONREFRESH-NO-SESSION-CREATE-01` archtest 静态拦截 `cells/accesscore/slices/sessionrefresh/` 内 session.Store.Create / Revoke / RevokeForSubject 调用

#### S4b authz_epoch + credential event closed loop ✅ shipped (PR #490)

**ship 摘要（2026-05-14）**：
- Batch 1A：`runtime/auth/jwt.go` issue 写入 `jti` (UUIDv4) + `authz_epoch` (int64 from `users.authz_epoch`) claims；verifier 暴露 typed claims；删除 `sessionlogin` / `sessionrefresh` 中 `Session.AuthzEpochAtIssue: 0` 硬编 placeholder；新增 `ErrAuthServiceUnavailable` + `KindUnavailable` 在 `pkg/errcode`；`runtime/auth/middleware.go` AuthMiddleware 按 Kind 分流 401 / 503
- Batch 1B：`domain.User.AuthzEpoch int64` + `ports.UserRepository.BumpAuthzEpoch(ctx, userID) (int64, error)`；PG impl 用 `UPDATE ... RETURNING`；mem impl mutex-guarded ++；TDD RED→GREEN 覆盖 double-bump / not-found / concurrent-100 goroutine
- Batch 1C：migration `025_drop_sessions_authz_epoch_at_issue.sql` 删 `sessions.authz_epoch_at_issue` 列（claim 已带，行内 pin 提供零额外防御，新 migration 走 S3F destructive Down GUC 模式）
- Batch 1D：`runtime/auth/refresh/errors.go` 新增 `ErrReused` typed error（独立于 `ErrRejected`，`errors.Is` 链不嵌套）；PG/mem refresh store 实现 reuse detection；ref: ory/fosite issue #442 + zitadel issue #8288
- Batch 2：新增 `cells/accesscore/internal/credentialinvalidate` 包 — `Invalidator.Apply(ctx, subjectID, event)` 3-op 原子 funnel (BumpAuthzEpoch + RevokeForSubject + RevokeUser)；nil-guard `New`/`MustNew`；`session.CredentialEventRefreshReuse` 加入 enum + ValidateCredentialEvent（不在 `allCredentialEvents` — security-response event，不是 protocol-level）
- Batch 3F：identitymanage Lock/Delete/ChangePassword/Update demotion + rbacassign Revoke 共 5 处 credential event 全部在既有 `RunInTx` 闭包内调 `credentialinvalidate.Invalidator.Apply`；rbacassign Assign **不**调 funnel（HIGH-3 决策：role grant 不是 credential invalidation event，对齐 Keycloak/zitadel 实践，Assign 仅 emit outbox fanout）；sessionlogout consumer 删 `RevokeForSubject` 调用（rbacassign 已在 same-tx revoke，consumer 重复 bump 会错误失效无关 access JWT），降级 audit/ack-only + Action enum 白名单（B2-C-06 闭环）
- Batch 3G：sessionlogin + sessionrefresh 把 `user.AuthzEpoch` 透传进 `sessionmint.Request.AuthzEpoch` → `IssueOptions.AuthzEpoch`；sessionrefresh reuse 分支 `refresh.Store.Rotate` 返回 `ErrReused` 时调 `invalidator.Apply(txCtx, subjectID, CredentialEventRefreshReuse)`，3-op funnel 覆盖 session + refresh-chain cascade（detached cascadeRevoke 路径删除）
- Batch 3H：sessionvalidate `enforceSessionState` 加 `userRepo ports.UserRepository` 入参 + `NewService` 改 `(*Service, error)` 形态；session store / userRepo infra error → `KindUnavailable` 503；userRepo.GetByID epoch check（`user.AuthzEpoch > claim` → 401）；8 个新测试覆盖 stale/equal/zero/high epoch、session infra 503、user repo infra 503、domain not-found 401、防枚举统一 body
- contract.yaml 共 26 条 internal-auth + auth + audit + config endpoint 加 `auth.responses: [503]` 声明
- archtest 新增 `tools/archtest/credential_invalidate_funnel_invariants_test.go`（守护 5 处 credential event 必走 funnel）+ `sessionvalidate_epoch_compare_test.go`（守护 `userRepo.GetByID` epoch 比对路径）
- ADR `202605101400-adr-credential-session-protocol.md` 加 +59 行（D2 完整生效 + 5 credential event 路由表）

**收口项**（PR #490 已 ship）：
- `JWT-AUTHZEPOCH-CLOSED-LOOP-S4-01` ✅
- `B2-C-06 SessionLogout consumer action 无验证` ✅（consumer 降级 audit/ack-only + Action 白名单）
- `P3-TD-10 TOCTOU 竞态修复` ✅（ChangePassword in-tx GetByID+Compare+Generate + bcrypt + epoch bump 原子）
- S4a FU-1 (rbacassign 删 `syncSessionRevocation` + same-tx revoke 恢复) ✅ — PR review findings 默认 in-scope
- S4a FU-2 (sessionvalidate 503 区分 + `ErrAuthServiceUnavailable`) ✅ — PR review findings 默认 in-scope

**ship 后 review FU（独立 backlog 触发型，不立即排期）**：PR #490 第五轮 review 暴露两件**未触发"in-scope 默认修"原则**的 finding（理由：工时显著超 S4b 范围 + AI-rebust 升级 ADR / QPS 阈值前不可观测的明确触发条件论证），按 ai-collab.md "既有 Soft 的补丁优先升级到 Hard/Medium" + memory `feedback_pr_findings_default_inscope` 例外条款独立登记：
- **REQUIRED-DEP-NIL-GUARD-01**（cap-14-tooling）— Service required-dep nil-guard archtest（Soft→Hard 升级）。OUTBOX-SERVICE-01 archtest scope 只守 `txRunner` 一个字段，PR #490 第五轮 review 手动补齐五处 service (rbacassign / authorizationdecide / rbaccheck) `validation.IsNilInterface` guard；触发条件 = 下次新增 service。修复方向：用 `typeseval` 扫每个 `func NewXxx(...) (*Xxx, error)` 入参签名 + 对照 body 前 30 行 nil guard 调用集合 + RED fixture
- **ENFORCESESSIONSTATE-HOTPATH-OPT-01**（cap-14-tooling）— sessionvalidate `enforceSessionState` 两次串行 PG 读优化。每次 access token 校验走 `sessionStore.Get(sid)` + `userRepo.GetByID(sub)` 两次串行 PG 读 (~2 round-trip / 请求)；plan §HIGH-4 决策为不包 read-only tx（无快照保证）；触发条件 = 生产 QPS / P99 延迟阈值（暂未设定）。修复方向：(a) 单 SQL JOIN sessions + users（cell-private SQL，避免跨 repo 协议化）/ (b) Redis epoch snapshot cache（TTL ≤ JWT 短 exp）/ (c) 请求合并 cache；需先写设计 ADR

**ACCOUNT-LOCKOUT-AUTO-LOCK-01 状态澄清**（v6 修订）：v5 plan 把 `ACCESSCORE-ACCOUNT-LOCKOUT-AUTO-LOCK-01` 列入 S4b 收口项是误判 — 该项是"连续失败达阈值后账户自动锁定"业务能力，与 S4b 的 authz_epoch + credential event 协议闭环属不同维度（前者是登录失败窗口/阈值/清零策略，后者是 protocol invariant）。S4b ship 后 LOCKOUT-AUTO-LOCK 仍 100% 未做，是独立 backlog 条目（archive/backlog.md，🔴 发布前必做硬约束），由后续独立 PR 实施（需先设计失败窗口/阈值/清零策略/错误码/审计与 outbox 语义；改用户持久化模型 + sessionlogin 错误路径 + lock/unlock 交互 + journey integration harness）。

**验收**（PR #490 已通过）：
- role revoke 后旧 access JWT 立即失效；rbacassign durable mode 单 tx 内完成 role 写入 + session/refresh revoke（不依赖 outbox consumer ack）✅
- lock/delete/change-password 后旧 access JWT 与 refresh token 均不可继续使用 ✅
- refresh reuse 检测命中走 cascade revoke（access session + refresh chain 同 tx 撤销）✅
- 并发 credential event 不产生 epoch 回退或 session 残留 ✅ （sessionvalidate_concurrent_epoch_race_test.go 覆盖）
- PG session store / userRepo 注入故障（pool 断开 / query timeout）→ sessionvalidate 返回 503 `ErrAuthServiceUnavailable` ✅
- 防枚举文案保持 `invalid or expired authentication token` ✅

#### S4c accesscore cleanup / race / L2 e2e

**范围**：
- `CELLS-IDENTITYMANAGE-LEVEL-MISLABEL-01` / `ACCESS-LEVEL-AUDIT-01`
- `B2-T-02 RBACASSIGN event contract waiver expiry`
- `B2-T-07-FU-1 RBACASSIGN caller wiring`
- `B2-C-13 L2 跨层 e2e 回归不足`
- `AUTH-CACHE-01` 仅在 durable correctness 完成后接入，默认关闭；不进入 S4a/S4b
- `PR267-FU-AUTHTEST-INTERNAL` / `PR250-F3 Event wire byte pinning`
- **S4a 遗留 FU-3b**：`tools/archtest/session_protocol_composition_root_test.go` 升 type-aware（`typeseval.ResolvePackageRef`），拒绝 `import sess "..."; sess.NewProtocol(...)` 绕过；`_test.go` 排除策略显式记入 godoc。`tools/archtest/refresh_invariants_test.go` 守护从旧 `sessionRepo.*` API 改守 `Peek → sessionStore.Get → userRepo.GetByID → Rotate` 新形态。AI-rebust Soft → Medium

**验收**：
- CI race lane 覆盖 `cmd/corebundle` PG 组合层或关键并发回归下沉到现有 race package，且加 no-tests guard
- L2 e2e 覆盖 login → refresh → revoke/logout → validate fail-closed 的跨层路径
- Redis session cache 不改变 PG durable source-of-truth 语义，cache miss / stale / outage 均 fail-safe
- **FU-3b 验收**：archtest 内置 RED fixture 覆盖 `import sess "..."` aliased import 与 production / `_test.go` 分流；`refresh_invariants_test.go` AST 守护命中 sessionrefresh 真实 lookup 链顺序

**收口 backlog**（PR #449 review carry-over entries）：
- S4-PG-SESSION-REFRESH-WIRING-COMPLETE-01 ✅ closed by PR #482 (S4a, 2026-05-13)：cell consume runtime session.Store + adapters/postgres PG session store；PG refresh store 接入；cell-private SessionRepository + mem session 路径删除；postgres 分支 `WithInMemoryDefaults` 删除；`TestSessionLogin_OutboxFailureRollsBackPGRows` 故障注入证明 PG tx rollback 完整回滚 session/refresh 行。**(forced re-login 通过 ADR D4.2 session retention 模型隐式覆盖：首次切 PG 时全员重新登录 — .env.example 已文档化)**
- JWT-AUTHZEPOCH-CLOSED-LOOP-S4-01 🟠 P0（PR #449 review carry-over）：S3+S5 仅落 schema (users.authz_epoch + sessions.authz_epoch_at_issue + sessions.jti) + Protocol primitive；JWT issue/validate 闭环在 S4b：(a) runtime/auth/jwt issuer 写 jti + epoch claim；(b) verifier 读 epoch；(c) sessionvalidate 加 user.authz_epoch lookup + 比对；(d) 4 个 CredentialEvent 撤销路径在每个 slice 接入；(e) ADR-credential D2 在 S4b 闭环前不真实生效，旧 access JWT 仍只靠 session revoke + 自然过期失效；(f) 删除 sessionlogin/sessionrefresh 中 `AuthzEpochAtIssue: 0` 硬编 placeholder

**收口 backlog**（原有）：
- ACCESSCORE-ACCOUNT-LOCKOUT-AUTO-LOCK-01 🔴 P1 — **改独立 PR**（非 S4b/S4c，业务能力维度不同；v6 修订澄清）
- CELLS-IDENTITYMANAGE-LEVEL-MISLABEL-01 🔴 Cx1（ACCESS-LEVEL-AUDIT 同主题）— S4c
- B5-FU-PG-RUNTIME-WIRING-AND-ARCHTEST-TYPE-AWARE-01 ✅ closed by PR #482（archtest 类型化的 composition-root + refresh_invariants 升级折入 S4c，不另开 backlog）
- PR338-FU-LOGIN-DURABLE-TX-ATOMICITY-TEST ✅ closed by PR #482
- P3-TD-10 TOCTOU 竞态修复 ✅ closed by PR #490（ChangePassword in-tx + bcrypt + epoch bump 原子）
- B2-PROVISIONER-MUTEX-REVIEW 🟠 P2（PG users 落地后 mutex 不再需要）— S4c
- B2-T-02 RBACASSIGN event contract waiver expiry 🟠 P1 — S4c
- B2-T-07-FU-1 RBACASSIGN caller wiring 🟠 — S4c
- B2-C-06 SessionLogout consumer action 无验证 ✅ closed by PR #490（consumer 降级 audit/ack-only + Action 白名单）
- PR267-FU-AUTHTEST-INTERNAL 🟡 — S4c
- PR250-F3 Event wire byte pinning 🟡 — S4c
- B2-C-13 L2 跨层 e2e 回归不足 🟡 P2（accesscore 接入完成后顺路）— S4c
- **REQUIRED-DEP-NIL-GUARD-01** 🟡 P2 — **独立触发型**（下次新增 service / Soft → Hard archtest 升级 ADR 决策；PR #490 第五轮 review）
- **ENFORCESESSIONSTATE-HOTPATH-OPT-01** 🟡 P3 — **独立触发型**（QPS / P99 延迟阈值；PR #490 第五轮 review）

**联动激活**（033 B2.A 4 项重新组织）：
- RBAC-ASSIGN-LEVEL-UPGRADE-01：rbacassign L0 → L1
- SEED-ROLE-IFACE-01：去 type assertion，改接口注入
- ACCESS-LEVEL-AUDIT-01：consistencyLevel 校正
- AUTH-CACHE-01：Redis session cache adapter 注入（S4c 或后续独立 PR，默认关闭；不进入 S4a/S4b correctness PR）

**B5.FU 消化**：S4 内后段完成 runtime wiring 切换；不再作为独立 PR。

---

### D4 accesscore docs/contracts sync（可并行小 PR）

**目的**：修复 PR #449/#459 review 发现的文档与 contract 漂移，避免 S4 实现期间继续按旧语义开发。

**并行性**：可与 S3F / S4.0 / S4a 并行；不依赖代码闭环，除非 contract test 需要等 S4.0 的 last-admin error code 定稿。

**文件域**：
- `docs/architecture/202605101400-adr-admin-invariant.md`
- `contracts/http/auth/setup/admin/v1/contract.yaml`
- `contracts/http/auth/user/delete/v1/contract.yaml`
- `contracts/http/auth/user/lock/v1/contract.yaml`
- `contracts/http/auth/role/revoke/v1/contract.yaml`
- README / bootstrap 相关运行文档
- `adapters/postgres/schema_guard.go` 顶部表清单注释（若未在 S3F 同步）

**内容**：
- setup admin 已初始化统一为实现/contract 的 `410 ERR_SETUP_ALREADY_INITIALIZED`，删除 ADR 中旧 `409 ERR_AUTH_ADMIN_ALREADY_EXISTS` 表述
- README bootstrap 文档从旧 temp credential / `GOCELL_ACCESSCORE_ADMIN_PROVISION_MODE=bootstrap|interactive` 更新到当前 `GOCELL_BOOTSTRAP_ADMIN_USERNAME/PASSWORD` 语义
- delete / lock / role revoke contract 明确 last-admin 保护的 403 error code 与触发条件
- schema guard 文档清单列出 `users` / `sessions` / `roles` / `role_assignments`
- **S4a 遗留 FU-3a**：`contracts/http/auth/login/v1/contract.yaml` 403 description 由 `ERR_AUTH_PASSWORD_RESET_REQUIRED` 改为反映实际代码路径（sessionlogin 返回 `ErrAuthUserNotActive` for suspended/locked，与 password-reset 解耦）；`contracts/http/auth/refresh/v1/contract.yaml` 补 403 声明（refresh 也会返回 `ErrAuthUserNotActive`）。CH-04 双源校验通过

**验收**：
- contract generation / verify 通过
- 文档不再描述不存在的 setup admin GET 路由或旧 bootstrap mode
- login/refresh contract 403 description 与代码实际返回的 errcode 对齐

---

### DX4 PG adapter maintainability（S4 后或低风险并行）

**目的**：降低 PG accesscore wiring 的维护风险，但不阻塞 S4 correctness 主线。

**建议排序**：S3F 与 S4a 之后；若人力充足可并行，但不与 S4a/S4b 共 PR。

**内容**：
- `cells/accesscore/postgres.NewDeps(pool any, ...)` 改为 typed `*pgxpool.Pool` 或小接口，消除运行时 type assertion
- PG user/role repo 的 query/constraint 错误码与 `adapters/postgres` 统一分类，避免全部映射成泛化 `ErrInternal`
- ambient tx archtest 从手写文件清单改为自动扫描 PG repo，或引入显式 marker / executor abstraction
- `PR444-FU-SESSIONSTORE-BENCH-01` 从 S3+S5/S4 correctness 主线移出，等 durable session/refresh 正确性落定后单独跑 benchmark

**验收**：
- 类型签名能在编译期拦错 pool 注入
- 新增/迁移 PG repo 时 archtest 不需要手动补文件清单
- benchmark PR 只报告性能与索引验证，不夹带行为语义改动

---

### S6 `runtime/state/cas` typed Protocol + configcore + accesscore 接入

**v3 修订**：从"runtime library 接口"改为"typed CAS Protocol primitive"；configcore / accesscore composition root 显式构造 + 注入。

**目录**：
```
runtime/state/cas/
  protocol.go              ← typed CAS Protocol（VersionField / ConflictPolicy / Strict|Lax）
  protocol_options.go      ← sealed Option（WithVersionField / WithConflictPolicy / WithStrict）
  store.go                 ← Store interface（CompareAndSwap / Get）
  mem_store.go             ← mem 实现
  storetest/suite.go       ← Run(t, factory, *Protocol) Protocol-driven
```

**内容**：
- typed `Protocol` struct + sealed Option（version field 名 / conflict 策略 / strict 模式）
- `NewProtocol(opts ...Option) (*Protocol, error)` fail-fast
- `Store` interface 由 Protocol 决定 conflict 行为
- mem 实现 + Protocol-driven storetest
- **composition root 显式构造**：configcore / accesscore 各 cell module 在 `cmd/corebundle/` 显式 `cas.MustNewProtocol(...)`
- **configcore 接入**：现有 PG version 行为重构为消费 cas Protocol；cell 内 service 接受注入
- **accesscore user version 接入**：identitymanage ChangePassword 用注入的 cas Protocol 解决并发

**收口 backlog**：
- B2-T-01 Config rollback 乐观锁缺 🟡 P1
- P3-TD-12 configpublish.Rollback 版本校验 🟠 P2
- PR280-FU1 CHANGEPASSWORD-CONCURRENT-SEMANTICS-01 🟡 P2

**为什么三件一起**：CAS Protocol 不能只抽不接入（违反"不留半成品"），两个消费点同时接入证明 typed Protocol 接口可用。

**S3+S5 PR449-F7 维护责任**：S6 落 `runtime/state/cas` PG migration 时（如新增 `version` 列、CAS conflict 索引等），必须同步更新 `adapters/postgres/schema_guard.go::VerifyExpectedShape` 的 `required` / `forbidden` 列清单，让 phase0 fail-fast 同步覆盖新 schema 形态。该清单是 hardcode 列表（非声明式派生），是 ADR-credential §5.1.3 部署 playbook 的契约一部分。

---

### S7 `runtime/audit/ledger` typed Protocol + PG + auditcore 接入

**v3 修订**：append-only / hash chain / restart 恢复 / idempotency 等协议属性升 typed Protocol；auditcore composition root 显式构造 + 注入。

**目录**：
```
runtime/audit/ledger/
  protocol.go              ← typed Protocol（ChainMode / IdempotencyMode / RestartRecovery）
  protocol_options.go      ← sealed Option（WithChainHMAC / WithIdempotencyKey / WithRestartRecovery）
  entry.go                 ← Entry typed struct
  store.go                 ← Store interface（Append / Get / Verify / TailHash）
  mem_store.go             ← mem 实现
  storetest/suite.go       ← Run(t, factory, *Protocol)

adapters/postgres/
  audit_ledger_store.go    ← conform 实现，构造签名 NewLedgerStore(pool, txMgr, *ledger.Protocol)
  migrations/02x_audit_entries.sql
```

**内容**：
- typed `Protocol` struct + sealed Option（chain 模式 / idempotency / restart 恢复策略）
- `NewProtocol(opts ...Option) (*Protocol, error)` fail-fast
- `Store` interface（Append / Get / Verify / TailHash）由 Protocol 决定方法形态
- restart 链头恢复（解 B2-C-01 P0）：Protocol 声明 `WithRestartRecovery(StrictTailVerify)` / `WithRestartRecovery(LazyOnFirstAppend)`，Store 实现按 Protocol 决策
- mem + PG 共享 storetest conformance（Protocol-driven，append-only / chain integrity / idempotency 派生 cases）
- **composition root 显式构造**：`cmd/corebundle/audit_module.go` 显式 `ledger.MustNewProtocol(...)`
- **auditcore 接入**：cell 内 slices 接受注入的 `*ledger.Protocol` + `ledger.Store`，slice 改为编排

**收口 backlog**：
- B2-C-01 Audit hashchain 重启未恢复尾节点 🔴 P0（chain 设计核心）
- AUDITAPPEND-L2-FAILURE-PROOF-01 🟡 P1
- B2-C-05 Auditappend actor 缺失降级不安全 🟡 P1（fail-closed 协议入框架）
- B2-C-09 Auditquery raw payload 直接回传 🟡 P1
- B2-C-10 Auditappend 全局 mutex 串行化 13 topic 🟡 P1（chain shard 设计）
- B2-C-14 Hash-chain 跨重启连续性测试缺 🟡 P2
- C-DC9 auditarchive 死代码靶子打通 🟡 P2
- PR266-AUDITAPPEND-STRICT 🟡 P2（strict 模式入框架）
- CELLS-SLICE-MULTI-VERB-DECOMPOSE-01（auditappend）🟡 P1（拆分顺路）
- PR392-FU-AUDIT-CHAIN-WIRING 🟠 P2（auditcore framework 化后 onAuthFail 接入自然）

**为什么单 cell 框架抽与接入合并**：auditcore 是 ledger 唯一消费者，拆"先框架后接入"两 PR 没收益（mem store 已是 storetest 实现），合并 PR 边界更清。

**S3+S5 PR449-F7 维护责任**：S7 落 `adapters/postgres/migrations/02x_audit_entries.sql` 时，必须同步在 `adapters/postgres/schema_guard.go::VerifyExpectedShape` 的 `required` 列加入 `audit_entries.{prev_hash, idempotency_key, ...}` 等关键列，让 binary 启动期 fail-fast 拒绝缺列的 partial migration。

---

### W9 outbox factory adoption（独立机械迁移）

033 W9 原样保留，与 B 路线无关，独立 worktree。~150 处 HandleResult struct literal → factory。

---

### B2.B PG-DEVICECELL-REPO（独立）

033 B2.B 原样保留，examples/iotdevice 业务，与 accesscore/auditcore/configcore 框架抽取无关。可在 Wave 0 同期并行起。

---

## 5. 路线外独立项（保留 backlog 单线，不进本计划）

按 cell 拆的 trivial 死代码 / 配置项，单 PR 各自处理（每个 ≤ 4h）：

**configcore 单独**：
- CONFIGCORE-CACHE-LIFECYCLE-OWNER-01
- C-02 CONFIGSUBSCRIBE-CACHE-LIFECYCLE
- B2-C-11 Configsubscribe tombstone 无 TTL
- CONFIGCORE-RECEIVE-PLACEHOLDER-CLEANUP-01
- PR-CFG-A-DEFER-2
- C-05 CELLS-CELLROUTES-PLACEHOLDER-DELETE
- PR320-FU-CONFIGCORE-CI-NOOP
- PR-CFG-G1-FU6
- PR238-FU4 / PR238-FU8（test 修补）
- CELLS-SLICE-MULTI-VERB-DECOMPOSE-01（configread）

**横切独立 PR**：
- A-01 OIDC-FAILFAST-MR-COMPLETENESS 🔴 P0（已在 030 §2 规划，独立大 PR）
- ADAPTER-ERROR-CLASSIFICATION-TRANSIENT-01 🟠 P1（030 W4，独立）
- ADAPTER-CONNECT-BUDGET-01 🟡 P1
- ADAPTER-MANAGED-RESOURCE-COMPLETENESS-01（A-01 同 PR）
- REPO-HEALTHCHECKER-01 🟡 P1（在 S3+S5 / S7 顺路落，不单 PR）
- B2-R-02 Readyz 缺少 repo probe（同 REPO-HEALTHCHECKER-01）
- C-04 CELLS-INIT-TEMPLATE-CONVERGE
- C-09 CELL-SPLIT-LAYOUT-NORMALIZE
- M1-OBSERVED HEALTHZ-INTERFACE-PACKAGE-01

**触发条件式不做**：
- X5 P3-TD-11 accesscore domain 拆分（X1 后）
- X13 REFRESH-PARTITION-01（生产流量阈值）

---

## 6. 工时粗估（v3）

| PR | dev | review | 备注 |
|---|---|---|---|
| S0 ✅ | 4h | 2h | CI 改造（PR#433）|
| ~~ADR-B~~ | ~~6h~~ | ~~4h~~ | **v3 删除**（typed Go 类型系统替代） |
| ADR-Typed ✅ | — | — | 已落 (`202605101200-...`) |
| S1 ✅ | 10h | 5h | 协议 ADR + typed Option Go 头文件骨架（PR#439）|
| S2 ✅ | 16h | 8h | typed Protocol primitive + mem + storetest（PR#444）|
| S3+S5 ✅ | 18h | 10h | PG store conform + 3 migration + admin schema（PR#449）|
| S3F ✅ | 8h | 4h | PG migration/schema hardening（PR#465，S4a 前置）|
| S4.0 ✅ | 6h | 3h | effective last-admin invariant（PR#476）|
| S4a ✅ | 12h | 6h | PG session/refresh durable wiring + refresh stable-sid + sessionlogout 503/404（PR#482，含 review 反转 fd954cb8） |
| S4b ✅ | 14h | 7h | authz_epoch + credential event closed loop + credentialinvalidate funnel + sessionvalidate 503 + refresh reuse cascade（PR#490，含 S4a FU-1/FU-2 in-scope 闭环） |
| S4c | 7h | 3h | cleanup / race / L2 e2e **+ S4a 遗留 FU-3b**（archtest Soft → Medium 升级） |
| S6 ✅ | 12h | 6h | typed CAS Protocol + 双消费 composition root 注入（PR#464）|
| S7 ✅ | 22h | 11h | typed audit Protocol + PG + 9 个收口项（PR#450）|
| W9 ✅ | 6h | 3h | 机械迁移（PR#434）|
| B2.B | 8h | 6h | device PG repo |
| D4 | 4h | 2h | accesscore docs/contracts sync **+ S4a 遗留 FU-3a**（login/refresh 403 description / 状态码声明） |
| **已 ship 累计** | **~148h** | **~75h** | S0/S1/S2/S3+S5/S3F/S4.0/S4a/**S4b**/S6/S7/W9 |
| **剩余** | **~11h** | **~5h** | S4c + B2.B + D4/DX4 并行小 PR（REQUIRED-DEP-NIL-GUARD-01 / ENFORCESESSIONSTATE-HOTPATH-OPT-01 独立触发型不计入剩余） |

**v2 → v3 工时变化**：
- 删 ADR-B：-6h dev / -4h review
- S1 加 Go 头文件骨架：+2h dev / +1h review
- S3+S5 archtest 降级：-6h dev / -2h review
- S4 省"接入"工时：-8h dev / -4h review
- S6 / S7 同上：-10h dev / -5h review
- **净降**：-28h dev / -14h review（约 19% 缩减）

**v3 → v4 工时变化**（PR#449/#459 review 教训）：
- S4 24h+12h（巨型 PR）→ 拆 S4.0 + S4a + S4b + S4c 共 30h+15h（多增量来自 review 新发现的 last-admin 语义漏洞 + JWT epoch 闭环未做）
- 新增 S3F 8h+4h（PG migration/schema hardening 不阻塞主线但 S4a 前置）
- 新增 D4 docs/contracts sync 与 DX4 maintainability 可并行小 PR

**v4 → v5 工时变化**（PR#482 ship + review finding in-scope 折入下游 PR）：
- S4a 10h+5h → 12h+6h（PR#482 实际工时含 review 反转 fd954cb8 / stable-sid 改造 / SESSIONREFRESH-NO-SESSION-CREATE-01 archtest）
- S4b 8h+4h → 10h+5h（加 S4a 遗留 FU-1 rbacassign same-tx revoke 恢复 + FU-2 sessionvalidate 503 区分；按 in-scope 处理而非新 backlog）
- S4c 6h+3h → 7h+3h（加 S4a 遗留 FU-3b archtest 升级）
- D4 4h+2h（S4a 遗留 FU-3a 折入：login/refresh contract 403 漂移 + 原 admin-invariant docs sync）
- **finding 不开新 backlog 条目**：rbacassign 二态 / sessionvalidate 401 / contract 403 漂移 / archtest Soft 全部按 in-scope 工时下放给 S4b/S4c/D4，避免 backlog 噪音

**v5 → v6 工时变化**（PR#490 ship + review FU 走独立 backlog 触发型，而非 in-scope）：
- S4b 10h+5h → 14h+7h（PR#490 实际工时含 credentialinvalidate funnel 包新建 + 5 处 credential event 路由 + sessionvalidate epoch compare + refresh reuse cascade + 26 条 contract.yaml 503 声明 + 2 新 archtest）
- S4b 收口项调整：**LOCKOUT-AUTO-LOCK 不属 S4b**（v5 误判，业务能力维度不同，澄清为独立 PR）；**B2-C-06** + **P3-TD-10** ✅ closed by PR #490；**FU-1 + FU-2** ✅ in-scope 闭环
- **PR #490 review FU 走独立触发型 backlog**（违反"in-scope 默认修"原则，但有充分论证）：REQUIRED-DEP-NIL-GUARD-01（Soft → Hard 升级需 typeseval ADR）+ ENFORCESESSIONSTATE-HOTPATH-OPT-01（QPS / P99 延迟阈值前不可观测）— 不增加 S4c 工时估算
- D4 工时不变（S4a FU-3a login/refresh 403 漂移仍未做；PR #490 只加了 26 条 internal/auth 端点的 503 auth.responses，login/refresh 公开端点的 403 description 未触达）

A 路线工时（033 残）47-49h dev / B 路线 v6 ~159h dev —— **约 3.2 倍**，但收口项从 5 项扩到 ~50 项（C/D/E/F + 033 残），且把"在 cell 内重做框架"问题一次解决，并把 PR#417 5 个 P0/P1 协议缺口压到启动期 fail-fast。

---

## 7. 决策点

**v6 关键路径**（2026-05-14）：S2/S6/S7/S3+S5/S3F/S4.0/S4a/**S4b** 已 ship（PR#490）；当前关键路径 = **S4c (cleanup / race / L2 e2e + S4a FU-3b archtest Soft → Medium 升级)** 单 PR 收口。每个 S4.x 都是 correctness-first，按 PR #445 教训不再单 PR 巨型化；PR #490 review FU 走独立触发型 backlog（REQUIRED-DEP-NIL-GUARD-01 / ENFORCESESSIONSTATE-HOTPATH-OPT-01）—— "in-scope 默认修"原则有充分论证下例外（Soft → Hard 升级 ADR / QPS 阈值前不可观测）。

B2.B / D4 / DX4 / 路线外独立项可与 S4c 并行；D4 同 PR 吸收 S4a 遗留 FU-3a (login/refresh contract 403 漂移)。**ACCOUNT-LOCKOUT-AUTO-LOCK-01** 不属于 034 plan 收口范围（v6 修订澄清），由独立 PR 实施。

~~ship S2 / S6 / S7 任一前必须先 ship S1~~（已完成）。
~~S4b authz_epoch closed loop~~（已 ship PR #490）。

---

## 8. 引用

- `docs/architecture/202605101200-adr-typed-go-heavy-protocol-primitives.md`：**v3 范式锚点**（typed-Go-heavy 协议 primitive）
- `docs/reviews/202605082044-pr417-pg-corecell-framework-analysis.md`：B 路线源
- `docs/plans/202605071200-033-pg-implementation-plan.md`：A 路线（被本计划主线取代，PG migration 子任务保留；§6 archtest 大部分降级删除）
- `docs/plans/202605082130-pg-corecell-open-issues.md`：C/D/E/F 待办源
- `docs/plans/archive/202604201800-pg-pilot-layering-refactor-plan.md`：历史 PG pilot 分层重构（同形态前例）
- `.claude/rules/gocell/runtime-api.md`：Option 范式分层 / sealed AuthPlan / 强依赖 wiring
- `CLAUDE.md` `## 核心架构约束` / `## 新增 invariant 决策原则`
- `docs/plans/202605051600-030-review-0504-implementation.md` K-04 ADR 决议（cells 留 framework 仓）
