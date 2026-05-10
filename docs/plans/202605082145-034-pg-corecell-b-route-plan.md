# 034 PG / accesscore / auditcore / configcore B 路线实施计划

**生成日期**: 2026-05-08
**最后更新**: 2026-05-10（v3 修订：typed-Go-heavy 范式重写，删 ADR-B）
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
| **S0** | CI integration discovery | infra | — | ✅ | (路线门禁) |
| **ADR-Typed** | typed-Go-heavy 协议 primitive 范式 | ADR 文档 | — | ✅ **已落** `202605101200-...` | (B 路线全体前置) |
| **S1** | Credential/Session/Admin 协议 → typed Option 列表 | ADR + Go 头文件 | ADR-Typed | ✅ | 三个待决策问题 |
| **S2** | `runtime/auth/session` typed Protocol + mem + storetest | typed Go primitive | S1 | ✅ 可与 S0 并行起 | — |
| **S3+S5** | PG session store conform + users/roles schema + admin 不变量 | adapter + migration | S2 | — | B2-C-02 / admin UNIQUE / A26-R4 |
| **S4** | accesscore composition-root 显式构造 + cell 注入 + 残留 P1/P2 | cell 接入 | S3+S5 | — | C 表 11 项（详见 §4） |
| **S6** | `runtime/state/cas` typed Protocol + configcore + accesscore user version 接入 | typed primitive + 双消费 | S1 | ✅ 与 S2-S5 并行 | E 表 2 项 + C 表 1 项 |
| **S7** | `runtime/audit/ledger` typed Protocol + PG + auditcore 接入 | typed primitive + adapter + cell | S1 | ✅ 与 S2-S6 并行 | D 表 9 项 |
| **W9** | outbox factory adoption | 机械迁移 | — | ✅ 完全独立 | 033 W9 |
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
  S2 ──→ S3+S5 ──→ S4 ──→ B5.FU 消化
                              
  S6（state/cas 路径）         [与 S2-S5 并行]
  S7（audit ledger 路径）       [与 S2-S6 并行]
```

**worktree 容量**：Wave 0 三个 + Wave 1 S1 = 4 worktree；Wave 2 起后 S2/S6/S7 三框架并行 + S3+S5 串行（共 4 worktree）；B5.FU 在 S4 内消化。

**关键路径**：**S1 是 B 路线唯一关键路径**（ADR-Typed 已落）。S1 不通过，S2-S7 不能起。

**v3 简化**：v2 的 ADR-B（接口归属 6 条边界规则）已被 ADR-Typed 替代，关键路径从"两份 ADR"简化为"一份 S1"。

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
- **PR444-FU-SESSIONSTORE-BENCH-01** 🟡 P2（PR #444 review carry-over）：`runtime/auth/session/storetest/` 新增 benchmark suite — 1000+ session × subject scope `RevokeForSubject` 与 mixed Create/Get/Revoke 并发场景，PG store 与 mem store 共跑共享 baseline；PG 负载下确认索引 + 单 update 路径性能符合预期，mem 顺路验收 O(n) 仍在 dev/test 可接受档位

---

### S4 accesscore composition-root 显式构造 + cell 注入 + 残留 P1/P2

**v3 修订**：从"cell 内 import runtime"模式改为"composition root 显式构造 typed Protocol → 注入 cell"。cell 内不再有"接入 runtime"的工作，仅消费注入的 typed Protocol + Store；省下"接入"工时。

**文件域**：
- `cmd/corebundle/access_module.go`：postgres 分支显式 `session.MustNewProtocol(...)` 构造，参数从 S1 决议；`accesscore.New(WithSessionProtocol(...), WithSessionStore(pgSessionStore))` 注入；删 `WithInMemoryDefaults`（B5.FU 消化）
- `cells/accesscore/cell.go` + `cell_options.go`：新增 `WithSessionProtocol` / `WithSessionStore` option（强依赖 wiring 范式，typed-nil reject）
- `cells/accesscore/slices/{sessionlogin,sessionlogout,refresh}/`：service 接受注入的 `*session.Protocol` + `session.Store`，不再 import `runtime/auth/session/mem`
- `cells/accesscore/cell_init.go`：Redis session cache adapter 注入
- `cells/accesscore/internal/{ports,domain,mem}/`：5 联动激活

**收口 backlog**（PR #449 review carry-over entries）：
- LASTADMINGUARD-SERVICE-WIRING-S4 🟠 P1（PR #449 review carry-over）：本 PR (S3+S5) 仅落 LastAdminGuard struct + 单测 + DB trigger 兜底；service-level wiring 在 S4 — identitymanage.DeleteUser / ChangeUserStatus(Locked) / rbacassign.RevokeRole 三个入口调用 LastAdminGuard.CheckRemove，把 DB trigger 触发的 P0001 raw exception 转成 ErrAuthLastAdminProtected 精准 errcode（ADR-admin §4 migration table 锁定）
- S4-PG-SESSION-REFRESH-WIRING-COMPLETE-01 🟠 P0（PR #449 review carry-over）：S3+S5 仅 wiring user/role/outbox PG，session/refresh repo 仍是 mem；当前 PG 模式下 sessionlogin.persistSessionWithRefresh 在真 PG tx 里写 mem session/refresh，rollback 不回滚 mem 状态（pre-existing hazard，S3+S5 PG TxManager wiring 让区域更显眼）。S4 必须同 PR：(a) cell consume runtime session.Store + adapters/postgres PG session store；(b) PG refresh store 接入；(c) 删除 cell-private SessionRepository + cell-internal mem session 路径；(d) 启动期 forced re-login 全员 session
- JWT-AUTHZEPOCH-CLOSED-LOOP-S4-01 🟠 P0（PR #449 review carry-over）：S3+S5 仅落 schema (users.authz_epoch + sessions.authz_epoch_at_issue + sessions.jti) + Protocol primitive；JWT issue/validate 闭环在 S4：(a) runtime/auth/jwt issuer 写 jti + epoch claim；(b) verifier 读 epoch；(c) sessionvalidate 加 user.authz_epoch lookup + 比对；(d) 4 个 CredentialEvent 撤销路径在每个 slice 接入；(e) ADR-credential D2 在 S4 闭环前不真实生效，旧 access JWT 仍只靠 session revoke + 自然过期失效

**收口 backlog**（原有）：
- ACCESSCORE-ACCOUNT-LOCKOUT-AUTO-LOCK-01 🔴 P1（session 状态机一并）
- CELLS-IDENTITYMANAGE-LEVEL-MISLABEL-01 🔴 Cx1（ACCESS-LEVEL-AUDIT 同主题）
- B5-FU-PG-RUNTIME-WIRING-AND-ARCHTEST-TYPE-AWARE-01 🟠 P1
- PR338-FU-LOGIN-DURABLE-TX-ATOMICITY-TEST 🟠
- P3-TD-10 TOCTOU 竞态修复 🟠 P2（session 状态机协议吸收）
- B2-PROVISIONER-MUTEX-REVIEW 🟠 P2（PG users 落地后 mutex 不再需要）
- B2-T-02 RBACASSIGN event contract waiver expiry 🟠 P1
- B2-T-07-FU-1 RBACASSIGN caller wiring 🟠
- B2-C-06 SessionLogout consumer action 无验证 🟡 P1
- PR267-FU-AUTHTEST-INTERNAL 🟡
- PR250-F3 Event wire byte pinning 🟡
- B2-C-13 L2 跨层 e2e 回归不足 🟡 P2（accesscore 接入完成后顺路）
- **PR449-FU-SETUP-PG-E2E-REAL-WRITER-01** 🟡 P2（PR #449 review F6 carry-over）：S3+S5 落地的 `cmd/corebundle/setup_pg_integration_test.go` 用 `outbox.NoopWriter{}`，未实测 L2 outbox 原子性；S4 cell 切到 runtime Store 时同 PR 落 `TestSetupEndpoints_FirstRunFlow_PG_WithRealWriter`，使用 `adapterpg.NewOutboxWriter` + outbox migrations + relay worker，端到端验证 setup user 写入 + `user.created` 事件原子提交（同 tx 失败回滚）

**联动激活**（033 B2.A 4 项重新组织）：
- RBAC-ASSIGN-LEVEL-UPGRADE-01：rbacassign L0 → L1
- SEED-ROLE-IFACE-01：去 type assertion，改接口注入
- ACCESS-LEVEL-AUDIT-01：consistencyLevel 校正
- AUTH-CACHE-01：Redis session cache adapter 注入

**B5.FU 消化**：S4 内后段完成 runtime wiring 切换；不再作为独立 PR。

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
| S0 | 4h | 2h | CI 改造 |
| ~~ADR-B~~ | ~~6h~~ | ~~4h~~ | **v3 删除**（typed Go 类型系统替代） |
| ADR-Typed | — | — | ✅ 已落 (`202605101200-...`) |
| S1 | 10h | 5h | 协议 ADR + typed Option Go 头文件骨架（+2h vs v2） |
| S2 | 16h | 8h | typed Protocol primitive + mem + storetest |
| S3+S5 | 18h | 10h | PG store conform + 3 migration + admin schema（archtest 大部分由 typed signature 替代，-6h） |
| S4 | 24h | 12h | composition root 显式构造 + cell 注入 + 12 个收口项（"接入"工时省下，-8h） |
| S6 | 12h | 6h | typed CAS Protocol + 双消费 composition root 注入（-4h） |
| S7 | 22h | 11h | typed audit Protocol + PG + 9 个收口项（-6h） |
| W9 | 6h | 3h | 机械迁移 |
| B2.B | 8h | 6h | device PG repo |
| **合计** | **~120h** | **~63h** | 4 worktree 并行 wall-clock 约 1-1.5 周 |

**v2 → v3 工时变化**：
- 删 ADR-B：-6h dev / -4h review
- S1 加 Go 头文件骨架：+2h dev / +1h review
- S3+S5 archtest 降级：-6h dev / -2h review
- S4 省"接入"工时：-8h dev / -4h review
- S6 / S7 同上：-10h dev / -5h review
- **净降**：-28h dev / -14h review（约 19% 缩减）

A 路线工时（033 残）47-49h dev / B 路线 v3 ~120h dev —— **约 2.5 倍**，但收口项从 5 项扩到 ~50 项（C/D/E/F + 033 残），且把"在 cell 内重做框架"问题一次解决，并把 PR#417 5 个 P0/P1 协议缺口压到启动期 fail-fast。

---

## 7. 决策点

ship S2 / S6 / S7 任一前必须先 ship S1（ADR 决策三个待决问题 + typed Option Go 头文件骨架）。**S1 是本计划唯一关键路径**（v2 的 ADR-B 已被 v3 ADR-Typed 替代，不再是关键路径节点）。

S0 / W9 / B2.B / 路线外独立项可立即起，不等 S1。

Wave 1 启动条件：S0 通过 + S1 ADR 评审通过（含 Go 头文件骨架）。

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
