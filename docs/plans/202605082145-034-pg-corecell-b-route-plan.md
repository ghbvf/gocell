# 034 PG / accesscore / auditcore / configcore B 路线实施计划

**生成日期**: 2026-05-08
**对接来源**:
- `docs/reviews/202605082044-pr417-pg-corecell-framework-analysis.md`（B 路线源）
- `docs/plans/202605071200-033-pg-implementation-plan.md`（A 路线，本计划取代主线）
- `docs/plans/202605082130-pg-corecell-open-issues.md`（C/D/E/F 待办清单）

**关系**：本计划以 B 路线（先抽 runtime 框架再接入）替代 033 A 路线（在 cell 内直接落 PG）；033 中的 migration / wiring / archtest 工作不废弃，重新组织为本计划下的 PR 子任务。

**v2 修订（2026-05-08）**：删除 v1 错误加入的"治理增强前置"（A-01 / kernel/healthz / errcode classification / cellinit / storetest 共享）——这些是质量改进项，不是 B 路线必须先做的架构问题，留 backlog 单独触发。新增 **ADR-B 接口归属决议** 作为 B 路线**真正**的架构前置（与 S1 协议 ADR 并列）。

---

## 1. 路线选择与原则

**B 路线核心**：accesscore / auditcore / configcore 三个 cell 是**框架自带能力**（K-04 决议），通用机制（session 状态机 / CAS / audit ledger）抽到 `runtime/`，cell 退化为示范实现。

**B 路线两个真正的架构前置**（必须先决议，否则 S2-S7 落地反复返工）：
- **S1 协议 ADR**：PR#417 §12 三个待决问题（admin 不变量 / login vs role revoke 排序 / access token 状态模型）+ credential 失效协议
- **ADR-B 接口归属 ADR**：B 路线自身的边界规则（K-04 张力 / Repository 归属 / 路径归属 / 事务关系 / storetest 位置 / runtime/auth 子包结构）

**PR 包决策原则**：
1. 协议决策与框架接口分离（两份 ADR 先行）
2. 框架接口 + mem 实现 + storetest conformance 同 PR（不留半成品）
3. PG 实现与 schema migration 同 PR（schema 一次落地）
4. cell 接入 PR 把同主题 backlog 顺路收（不留小尾巴）
5. 路线外独立项（adapter 通用 / 各 cell 死代码清理）走自己的小 PR，不并入主线

---

## 2. PR 包总览

| PR | 主题 | 类型 | 依赖 | 并行可行 | 收口 backlog 项 |
|---|---|---|---|---|---|
| **S0** | CI integration discovery | infra | — | ✅ | (路线门禁) |
| **S1** | Credential/Session/Admin 协议 ADR | ADR 文档 | — | ✅ | 三个待决策问题 |
| **ADR-B** | B 路线接口归属决议 | ADR 文档 | — | ✅ 与 S1 并行 | (B 路线全体前置) |
| **S2** | `runtime/auth/session` 框架 | runtime 抽象 | S1 + ADR-B | ✅ 可与 S0 并行起 | — |
| **S3+S5** | PG session/users/roles store + admin 不变量 schema | adapter+migration | S2 | — | B2-C-02 / admin UNIQUE / A26-R4 |
| **S4** | accesscore session/login 接入 + 残留 P1/P2 | cell 接入 | S3 | — | C 表 11 项（详见 §4） |
| **S6** | `runtime/state/cas` 框架 + configcore + accesscore user version 接入 | runtime + 双消费 | S1 + ADR-B | ✅ 与 S2-S5 并行 | E 表 2 项 + C 表 1 项 |
| **S7** | `runtime/audit/ledger` 框架 + PG audit_entries + auditcore 接入 | runtime+adapter+cell | S1 + ADR-B | ✅ 与 S2-S6 并行 | D 表 9 项 |
| **W9** | outbox factory adoption | 机械迁移 | — | ✅ 完全独立 | 033 W9 |
| **B2.B** | PG-DEVICECELL-REPO | adapter+migration | — | ✅ 与 examples 业务无关 | 033 B2.B |

**B5.FU PG-REFRESH-RUNTIME-WIRING 自然消化**：在 S4 后段完成（access_module postgres 分支删 `WithInMemoryDefaults`），不再独立。

---

## 3. 依赖图与执行波次

```
Wave 0（立即并行起）：
  S0 ──┐
  W9 ──┼── 全部独立，无下游
  B2.B─┘

Wave 1（关键路径，并行起两份 ADR）：
  S1     协议 ADR
  ADR-B  接口归属 ADR

Wave 2（两份 ADR 都过后）：
  S2 ──→ S3+S5 ──→ S4 ──→ B5.FU 消化
                              
  S6（state/cas 路径）         [与 S2-S5 并行]
  S7（audit ledger 路径）       [与 S2-S6 并行]
```

**worktree 容量**：Wave 0 三个 + Wave 1 两份 ADR = 5 worktree；Wave 2 起后 S2/S6/S7 三框架并行 + S3+S5 串行（共 4 worktree）；B5.FU 在 S4 内消化。

**关键路径**：S1 + ADR-B 是 B 路线**唯一**关键路径。两份 ADR 不通过，S2-S7 不能起。

---

## 4. 各 PR 包详细内容

### S0 CI integration discovery

**目的**：先修验证基础设施，避免后续 PR 因 CI integration 误判返工。

**内容**：按 `//go:build integration|e2e` 文件发现 package，archtest 防退化。

**收口**：路线门禁（不直接收 backlog 条目，但所有后续 PR integration test 依赖）。

---

### ADR-B B 路线接口归属决议（纯文档，B 路线架构前置）

**目的**：B 路线主张抽 runtime 框架，但抽到哪一层、怎么分包、cell 与 runtime 的接口怎么协调——这些边界规则不先定，S2/S6/S7 三框架 PR 内会反复争论"接口该长什么样"，违反"引入新约束必须同 PR 闭环"。

**必须回答的 6 条边界规则**：

1. **K-04 vs runtime 抽取的张力解决**
   K-04 决议 cells/{accesscore,auditcore,configcore} 留 framework 仓，定位 = "框架自带认证/配置/审计能力"。B 路线把通用机制抽到 runtime 后，cell 是 runtime 接口的**本体实现**（K-04 原意），还是退化为**示范实现**（PR#417 §8.5 措辞）？
   - 选项 A：cell 是本体 — runtime 暴露**最小协议接口**，cell 拥有完整业务能力，外部 cell 只在罕见场景重新实现接口
   - 选项 B：cell 是示范 — runtime 暴露**完整能力接口**，cell 退化为 thin wrapper，外部 cell 可平替
   - 决议影响：runtime/auth/session 的接口宽度（最小协议 vs 完整能力）

2. **Repository / Store 接口归属准则**
   033 §5.1 说 Repository 接口在 cells/{cell}/internal/{domain|mem}/（cell 私有）；PR#417 §8.1 说 SessionStore 接口在 runtime/auth/session/store.go（runtime 共享）。两者矛盾。需要一条**判定准则**：
   - 候选准则：接口被 ≥2 cell 消费 → 升 runtime；单 cell 消费 → 留 cell 私有
   - 候选准则：接口表达**协议状态**（session 状态机 / audit chain / cas version）→ 升 runtime；接口表达**业务实体**（user / role / device / config entry）→ 留 cell 私有
   - 决议影响：UserRepository / RoleRepository / DeviceRepository 不升 runtime；SessionStore / CASStore / AuditLedgerStore 升 runtime

3. **adapters/postgres 路径归属**
   现状：configcore 已经在 `cells/configcore/internal/adapters/postgres/` 放 PG repo；PR#417 §8.4 提议放 `adapters/postgres/`。两层路径同时存在是矛盾的，必须二选一：
   - 选项 A：所有 PG 实现统一在 adapters/postgres/（与依赖规则"adapters 实现 kernel/runtime 定义的接口"一致；要求把 configcore 现有 PG repo 上移）
   - 选项 B：cell 私有 PG 实现留 cells/{cell}/internal/adapters/postgres/，runtime 接口的 PG 实现放 adapters/postgres/（混合）
   - 决议影响：S3+S5 / S6 / S7 三 PR 的物理路径

4. **Store 接口与事务的关系**
   gocell 已有 `kernel/persistence.TxRunner` + `postgres.TxManager` 提供 ambient TX from context。runtime 框架接口（SessionStore / CASStore / AuditLedgerStore）：
   - 选项 A：透明 ambient TX — Store 方法不暴露 TxRunner，事务边界由调用方 `txRunner.RunInTx` 包，Store 内部读 `TxFromContext`
   - 选项 B：显式 TxRunner — Store 接口签名暴露 TxRunner，调用方传入
   - 033 §5.3 已明确选 A（ambient TX，archtest PG-REPO-AMBIENT-TX-01 守）；ADR-B 确认 B 路线沿用同一约束

5. **storetest conformance 位置**
   选项 A：每框架内 `runtime/auth/session/storetest/`、`runtime/state/cas/storetest/`、`runtime/audit/ledger/storetest/`（PR#417 提议）
   选项 B：共享 `kernel/persistence/storetest/` 提供 RunSuite helper，每框架的 storetest 只定义自己的 fixture
   - 决议影响：S2 首落 storetest 范式时是建独立子包还是消费共享 helper

6. **runtime/auth 子包结构**
   现状：runtime/auth/ 已有 jwt / federated / oidc 等子包。新加 runtime/auth/session/，与现有子包关系：
   - session 是 stateful（DB 持久），jwt 是 stateless（claim only）—— 是对偶？
   - jwt 签发依赖 session 还是反向？
   - 候选：runtime/auth/session 提供 SessionStore + Fingerprint helper；runtime/auth/jwt 消费 session 接口签发；revoke 协议在 runtime/auth/session 内
   - 决议影响：S2 包结构与 import 方向

**输出**：`docs/architecture/202605xx-adr-b-route-interface-boundaries.md`

**收口**：B 路线 S2/S3+S5/S4/S6/S7 全体前置（不通过则 5 个 PR 都不能起）。

---

### S1 Credential / Session / Admin 协议 ADR（纯文档）

**目的**：把三个待决策问题一次决定，避免 S2-S5 边写边改。

**内容**：
- access token 不落明文 / session 表存 HMAC fingerprint 还是 jti
- password reset / lock / delete / role revoke 对旧凭据的统一失效协议
- login 与 role revoke 的排序点（per-user advisory lock / authz epoch / role version）
- refresh chain 与 session revoke 的边界
- **admin 不变量**：至少一个 vs 只能一个（PR#417 §12 决策点）
- session/refresh revoke 与事务边界

**输出**：`docs/architecture/202605xx-adr-credential-session-protocol.md` + `docs/architecture/202605xx-adr-admin-invariant.md`

---

### S2 `runtime/auth/session` 框架

**目录**：
```
runtime/auth/session/
  types.go
  store.go
  fingerprint.go
  revoke.go
  storetest/suite.go
```

**内容**：
- session metadata 接口
- access token 不可重放引用 / HMAC fingerprint helper
- session revoke / subject-level revoke
- mem 实现 + storetest conformance suite
- 不含 admin/role name/password policy 等产品语义

**约束**：不接 accesscore，PR 独立可审查。

**收口**：暂无 backlog（铺路，S4 消费）。

---

### S3+S5 PG session/users/roles store + admin 不变量 schema（合并 PR）

**合并理由**：schema 一次落地，admin UNIQUE 与 users 表是 DDL 层共生关系，不应拆 PR；migration 017-019 三条 SQL 同 PR 避免 schema_guard 文本合并冲突。

**文件域**：
- `adapters/postgres/session_store.go`（实现 S2 接口）
- `adapters/postgres/{user,role}_repo.go`
- `adapters/postgres/migrations/017_users.sql` / `018_sessions.sql` / `019_roles.sql`
- `adapters/postgres/schema_guard.go` 首落主体 + 3 表
- `adapters/postgres/errcode.go` append PG 错误码
- `tools/archtest/pg_repo_invariants_test.go` 首落 3 INVARIANT
- `cmd/corebundle/setup_integration_test.go` 加 testcontainer e2e

**收口 backlog**：
- B2-C-02 SETUP-ADMIN-PUBLIC-ROUTE-PERMANENT 🔴 P0（schema + setup 边界一起处理）
- A26-R4 SETUP-ORPHAN-E2E-01（顺路 testcontainer e2e）
- ACCESSCORE-PG-USERS-MIGRATION-01（B3 历史项，admin UNIQUE 在此 PR 决议）
- B2-X-03 PG invalid index warn continue（PG schema 启动 fail-fast 在此 PR 配套）
- B2-A-13 PG pool tx rollback 日志泄漏（顺路，PG adapter 同主题）
- PR-V1-PG-STARTUP-HARDEN-FU-RACE-COVERAGE（PG integration test 加 -race）

---

### S4 accesscore session/login 接入 + 残留 P1/P2

**文件域**：
- `cells/accesscore/slices/{sessionlogin,sessionlogout,refresh}/` 改为消费 `runtime/auth/session`
- `cells/accesscore/cell_init.go` Redis session cache adapter 注入
- `cells/accesscore/internal/{ports,domain,mem}/` 5 联动激活
- `cmd/corebundle/access_module.go` postgres 分支删 `WithInMemoryDefaults`（B5.FU 消化）

**收口 backlog**：
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

**联动激活**（033 B2.A 4 项重新组织）：
- RBAC-ASSIGN-LEVEL-UPGRADE-01：rbacassign L0 → L1
- SEED-ROLE-IFACE-01：去 type assertion，改接口注入
- ACCESS-LEVEL-AUDIT-01：consistencyLevel 校正
- AUTH-CACHE-01：Redis session cache adapter 注入

**B5.FU 消化**：S4 内后段完成 runtime wiring 切换；不再作为独立 PR。

---

### S6 `runtime/state/cas` 框架 + configcore + accesscore 接入

**目录**：
```
runtime/state/cas/
  version.go
  conflict.go
  storetest/suite.go
```

**内容**：
- version / etag 接口
- compare-and-swap update 模式
- conflict error 标准化
- mem 实现 + storetest conformance
- **configcore 接入**：现有 PG version 行为重构为消费 cas
- **accesscore user version 接入**：identitymanage ChangePassword 用 cas 解决并发

**收口 backlog**：
- B2-T-01 Config rollback 乐观锁缺 🟡 P1
- P3-TD-12 configpublish.Rollback 版本校验 🟠 P2
- PR280-FU1 CHANGEPASSWORD-CONCURRENT-SEMANTICS-01 🟡 P2

**为什么三件一起**：CAS 框架不能只抽不接入（违反"不留半成品"），两个消费点同时接入证明框架接口可用。

---

### S7 `runtime/audit/ledger` 框架 + PG + auditcore 接入

**目录**：
```
runtime/audit/ledger/
  entry.go
  chain.go
  store.go
  storetest/suite.go

adapters/postgres/
  audit_ledger_store.go
  migrations/02x_audit_entries.sql
```

**内容**：
- append-only ledger 接口
- hash/HMAC chain
- restart 链头恢复（解 B2-C-01 P0）
- idempotency key
- verify / gap detection
- mem + PG storetest conformance
- auditcore 接入框架，slices 改为编排

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

## 6. 工时粗估

| PR | dev | review | 备注 |
|---|---|---|---|
| S0 | 4h | 2h | CI 改造 |
| S1 | 8h | 4h | 协议 ADR |
| ADR-B | 6h | 4h | 接口归属 ADR（6 条边界规则） |
| S2 | 16h | 8h | runtime 接口 + mem + storetest |
| S3+S5 | 24h | 12h | PG store + 3 migration + setup 边界 + admin schema |
| S4 | 32h | 16h | accesscore 接入 + 12 个收口项 |
| S6 | 16h | 8h | CAS 框架 + 双消费接入 |
| S7 | 28h | 14h | audit ledger + PG + 9 个收口项 |
| W9 | 6h | 3h | 机械迁移 |
| B2.B | 8h | 6h | device PG repo |
| **合计** | **~148h** | **~77h** | 4 worktree 并行 wall-clock 约 1.5-2 周 |

A 路线工时（033 残）47-49h dev / B 路线 142h dev — **约 3 倍**，但收口项也从 5 项扩到 ~50 项（C/D/E/F + 033 残），且把"在 cell 内重做框架"问题一次解决。

---

## 7. 决策点

ship S2 / S6 / S7 任一前必须先 ship S1（ADR 决策三个待决问题）。S1 ADR 决议是本计划真正的关键路径。

S0 / W9 / B2.B / 路线外独立项可立即起，不等 S1。

Wave 1 启动条件：S0 通过 + S1 ADR 评审通过。

---

## 8. 引用

- `docs/reviews/202605082044-pr417-pg-corecell-framework-analysis.md`：B 路线源
- `docs/plans/202605071200-033-pg-implementation-plan.md`：A 路线（被本计划主线取代，PG migration / archtest 子任务保留）
- `docs/plans/202605082130-pg-corecell-open-issues.md`：C/D/E/F 待办源
- `CLAUDE.md` `## 核心架构约束` / `## 新增 invariant 决策原则`
- `docs/plans/202605051600-030-review-0504-implementation.md` K-04 ADR 决议（cells 留 framework 仓）
