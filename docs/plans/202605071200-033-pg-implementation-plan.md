# 033 PG 实施计划

**生成日期**: 2026-05-07
**最后更新**: 2026-05-08（激进合并：删 6 个切片视图，合一为单一任务表 + 实施叙事；W4-W8 全部 ship 同步状态）
**对接来源**:
- `docs/plans/202605011500-029-master-roadmap.md` Track B（B2 拆分行 + B5.FU + 已 ship 的 B1/B4/B5/B6/B7/B8/B9）
- `docs/backlog.md` cap-07（事务性事件发布）+ cap-10（持久化与加密）
- `docs/backlog2.md` §5.1 PostgreSQL 路由
- 029 关键路径已 ship 项：B1 PR#373+#384 / B4+B8 PR#401 / B5 PR#399 / B6 PR#380 / B7+B9 PR#388+#395 / #12 PR#369+#374

**关系**：本计划是 029 roadmap Track B 中所有未 ship PG 项的实施落地文档；不重复已 ship 项细节，仅引用。

---

## 0. 背景

029 roadmap 已 ship 的 PG 项构成了基础设施层（B1 outbox fencing / B4+B8 startup harden + ConnectTimeout / B5+B7+B9 refresh harden）。剩余主线工作是 **PG 接入业务层**：5 域 Repository（User/Session/Role/Device/Command）+ 配套 migration，以及 `B5.FU` runtime wiring 闭口（`WithInMemoryDefaults` 切换为 PG 实例）。

backlog cap-10 中 `X1 PG-DOMAIN-REPO` 与 029 B2 是同一项；`ACCESSCORE-PG-USERS-MIGRATION-01` (=B3) 已合并到 B2.A；`A26-R4 SETUP-ORPHAN-E2E-01` 触发条件是 PG users adapter 落地，建议 B2.A 顺路收。

---

## 1. 目标

1. **5 域 PG Repository 全部落地**：User / Session / Role / Device / Command
2. **migration 序列推进** 017→022（已存在 016 是 `refresh_tokens_idle_grace`）
3. **联动激活** 4 项 accesscore 历史悬挂项：`RBAC-ASSIGN-LEVEL-UPGRADE` / `SEED-ROLE-IFACE` / `ACCESS-LEVEL-AUDIT` / `AUTH-CACHE`
4. **CONFIG-VERSIONS-MIGRATION-01** 联动激活（configcore，migration 004 表已建只缺 wiring）
5. **B5.FU runtime wiring 闭口**：`cmd/corebundle/access_module.go` postgres 模式切真实 PG 实例
6. **顺路收**：`A26-R4 SETUP-ORPHAN-E2E-01` 真实 DB e2e（仅 B2.A）
7. **不做的事**：见 §8

---

## 2. 任务表（唯一事实表）

| ID | Worktree | Cap | Status | 工时 dev+review | 文件域 / 说明 |
|---|---|---|---|---|---|
| **B2.A PG-ACCESSCORE-REPO** | 1 | cap-10 | ⏳ | 17h+12.5h | User+Session+Role；含 4 联动激活（RBAC-ASSIGN-LEVEL-UPGRADE / SEED-ROLE-IFACE / ACCESS-LEVEL-AUDIT / AUTH-CACHE）+ A26-R4 SETUP-ORPHAN-E2E-01 顺路收；migration 017-019；**关键路径解锁 B5.FU**；详见 §3 |
| **B2.B PG-DEVICECELL-REPO** | 2 | cap-10 | ⏳ | 8h+6h | Device+Command；migration 020-021；Command 形态待 explorer 核（如不独立缩到 6h+4h）；与 B2.A 文件域 0 重叠 |
| **B2.C PG-CONFIGCORE-VERSIONS-WIRING** | 3 | cap-10 | ⏳ | 4h+2h ~ 6h+4h | config_repo + flag_repo；migration 022 按需；实际范围待 explorer 核（若仅 wiring 缩到 4h+2h，需补 schema/index 6h+4h）；与 B2.A/B 文件域 0 重叠 |
| **B5.FU PG-REFRESH-RUNTIME-WIRING** | 10 | cap-05 | 卡 B2.A | 12h+6h | 029 row 153 完整定义；access_module postgres 分支删 `WithInMemoryDefaults`；service-level PG integration test；`refresh_cross_store_tx_test` 升 `golang.org/x/tools/go/packages` |
| W9 `OUTBOX-FACTORY-ADOPTION-01` | 9 | cap-07 | ⏳ | 6h+3h | ~150 处 `HandleResult` struct literal → factory 机械迁移（`adapters/rabbitmq/` ~85 + `runtime/eventbus/` ~26 + `runtime/wrapper/` ~14 + `runtime/eventrouter/` ~14 + `cmd/corebundle/` ~6 + `kernel/cell` ~1 + `kernel/bootstrap` ~5）；与 B2.A/B/C 文件域 0 重叠 |
| W4 `G-12 CRYPTO-INTERFACE-HARDENING` | 4 | cap-10 | ✅ PR#413 | — | `kernel/crypto/`；ConstantTimeCompare + nonce uniqueness contract test + EncryptResult 统一签名 |
| W5 `B2-A-28 Redis password fail-open` | 5 | cap-10 | ✅ PR#416 | — | `adapters/redis/` + `cmd/corebundle/redis.go` + `_build-lint.yml`；含 `B2-A-29 race stress` + `B2-A-30 stale 关闭` 顺路 |
| W6 `B2-C-12 Audit HMAC key 最小长度` | 6 | cap-10 | ✅ PR#414 | — | `cells/auditcore/cell.go`；32 字节最小长度 + Validate |
| W7 `G-07 OUTBOX-WRITER-MUST-CONTRACT` | 7 | cap-07 | ✅ PR#415 | — | `kernel/outbox/` + `kernel/command/` + `kernel/metautil/`；Writer.Write SHOULD→MUST + extract metautil + result factories |
| W8 PR#415 review follow-ups | 8 | cap-08 | ✅ PR#420 | — | `kernel/outbox/`；OBS-TOTAL-CAP-DEAD-BRANCH + OUTBOX-ERR-LAYER-FMT-ERRORF + OUTBOX-READY-TEST-TIMING-FLAKE + DUAL-BARRIER + SUBWITHMW-CTOR-FAILFAST 一次性收 |

**剩余主线**：B2.A/B/C + W9 + B5.FU = **47-49h dev + 29.5-31.5h review**；并行 wall-clock 约 2-3 天（4 worktree 容量），单线串行 ≈ 79 工时。

**ship 顺序**：B2.A 先 merge 解锁 B5.FU；B2.B / B2.C / W9 任意顺序；B5.FU 接 B2.A。

**reviewer 优先级（剩余）**：B2.A > B2.B = B2.C > W9 factory 机械迁移 > B5.FU。

---

## 3. B2.A 关键路径展开

唯一需要单独叙事的 task — 文件域大、含 4 联动激活、关键路径锁 B5.FU。

**文件域**：
- `adapters/postgres/{user,session,role}_repo.go` + `*_integration_test.go`
- `adapters/postgres/migrations/017_users.sql`（含 B3 `UNIQUE(role=admin)`）/ `018_sessions.sql` / `019_roles.sql`
- `cells/accesscore/internal/mem/{user,session,role}_repo.go`：抽 domain-side interface（`UserRepository` / `SessionRepository` / `RoleRepository`），mem 与 PG 双实现
- `cmd/corebundle/access_module.go`：声明 PG provider（**不切 `WithInMemoryDefaults`**，wiring 切归 B5.FU）
- `tools/archtest/pg_repo_invariants_test.go`：3 INVARIANT 主体首落（详见 §6）
- `adapters/postgres/schema_guard.go`：append 3 表名（B2.A 首落主体，B2.B/C 后续 append）
- `adapters/postgres/errcode.go`：append `ErrAdapterPGUserDuplicate` / `ErrAdapterPGRoleNotFound` / `ErrAdapterPGAdminUniqueViolation`

**联动激活 4 项**：
- `RBAC-ASSIGN-LEVEL-UPGRADE-01`：`cells/accesscore/slices/rbacassign/` consistencyLevel L0 → L1（PG users 落地后真有事务边界）
- `SEED-ROLE-IFACE-01`：去 `seedrole` slice 内 type assertion，改 `RoleRepository` 接口注入
- `ACCESS-LEVEL-AUDIT-01`：accesscore 各 slice consistencyLevel 与 cell 声明对齐校正（不动业务逻辑）
- `AUTH-CACHE-01`：accesscore cell 构造期注入 Redis session cache adapter（不引入新 cache 协议）

**A26-R4 SETUP-ORPHAN-E2E-01 顺路**：`cmd/corebundle/setup_integration_test.go` 加 testcontainer e2e 用例（PG users 落地后条件天然具备，~1h+0.5h）。

**风险**：accesscore 联动 4 项扩文件域；users migration `UNIQUE(role=admin)` 选型（partial unique index vs CHECK）需 PR 内决议 + ADR 1-2 段。如 review 阻塞可拆 `B2.A.LINK` 联动激活独立 PR，但首选不拆（联动激活与 PG repo 落地共生）。

**B5 PR#399 加强**：`refresh_outer_tx_atomicity_integration_test.go` 三场景中 session 侧 row rollback 由 `PGSessionRepository` 真实生效（B2.A 落地后自动加强；B5 PR description 已标 honest test-scope boundary）。

---

## 4. migration 编号预分配（防三 PR 同选）

```
014 ✅ outbox lease_id              （B1 已 ship）
015 ✅ outbox claiming lease check   （B1 N8 已 ship）
016 ✅ refresh_tokens idle/grace     （B7+B9 已 ship）
─────────────────────────────────
017    users                        ← B2.A
018    sessions                     ← B2.A
019    roles                        ← B2.A
020    devices                      ← B2.B
021    commands                     ← B2.B（如 Command 独立）
022+   config_*（如需）              ← B2.C
```

约束：每条 migration 必须 `IF NOT EXISTS` / `IF EXISTS` / `DO` 块（`tools/archtest/migration_no_transaction_rerun_safe_test.go` 守，B1 N8 已立）。

---

## 5. 接口设计原则

### 5.1 Repository 接口位置

- **接口**：`cells/{cell}/internal/{domain|mem}/` 内（domain-driven，repository 是 cell 私有抽象）
- **mem 实现**：`cells/{cell}/internal/mem/`（已存在 struct，本计划改造为 interface + struct）
- **PG 实现**：`adapters/postgres/{domain}_repo.go`（adapter 实现 cell-side 接口）

### 5.2 构造期 fail-fast（对齐 PR#388 范式）

```go
func NewUserRepository(pool *pgxpool.Pool, txRunner persistence.TxRunner) (*UserRepository, error) {
    if pool == nil {
        return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
            "user repo: pool required")
    }
    if txRunner == nil {
        return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
            "user repo: txRunner required")
    }
    return &UserRepository{pool: pool, txRunner: txRunner}, nil
}
```

archtest `PG-REPO-CONSTRUCTOR-FAIL-FAST-01` 守该模式（见 §6）。

### 5.3 Ambient TX（对齐 B7 PR#388 范式）

PG repo 写路径全部走 `txRunner.RunInTx`，禁止 `pool.Begin` / `pool.Exec` 直裸调（除明确无事务边界的只读路径）。`TxFromContext` savepoint 由 `postgres.TxManager` 提供，业务调用方零感知。

archtest `PG-REPO-AMBIENT-TX-01` 守该模式。

---

## 6. archtest 设计（funnel-first 单文件主题）

按 CLAUDE.md `## 新增 invariant 决策原则`：单文件 `tools/archtest/pg_repo_invariants_test.go` 聚并所有 PG repo 主题约束，每条规则函数前 godoc 加 `// INVARIANT: PG-REPO-*-01` 锚点。

**初始 3 条**（B2.A 落）：

| INVARIANT ID | 守的范式 | 不能 funnel 的原因 |
|---|---|---|
| `PG-REPO-CONSTRUCTOR-FAIL-FAST-01` | `NewXxxRepository` 必须 `(*XxxRepository, error)` 签名 + body 顶层 nil 校验 | Go 无强制构造器签名约束，函数签名是 contract 但实现里能漏检 |
| `PG-REPO-AMBIENT-TX-01` | 写路径必须 `txRunner.RunInTx`，禁 `pool.Begin/Commit` 直调 | 调用形态级约束，无类型钩子 |
| `PG-REPO-INVARIANT-LIST` 索引 | `// INVARIANT:` 锚点必须在 inventory.md 出现 | grep 锚点串联 |

**后续 append**（B2.B 落 1 条 / B2.C 按需）：
- `PG-REPO-LIST-PARAMS-VALIDATE-01`（如分页参数校验需统一守）

**不准建**：Registry / 中心化注册表 / `gocell check pg-invariants` CLI（funnel-fix roadmap §2 已立）。

---

## 7. 协调点（PR 开启前商定 / 不冲突约定）

| 约定 | B2.A | B2.B | B2.C |
|---|---|---|---|
| migration 编号 | 017-019 | 020-021 | 022+（按需）|
| `tools/archtest/pg_repo_invariants_test.go` | **首落主体 + 3 条 INVARIANT** | append 1-2 条 | append 0-1 条 |
| `adapters/postgres/schema_guard.go` 表清单 | **首落主体 + 3 表** | append 1-2 表 | append 0-1 表 |
| `adapters/postgres/errcode.go` | append-only | append-only | append-only |
| `docs/audit/archtest-inventory.md` | append PG-REPO-* 主题行 | append | append |
| `cmd/corebundle/access_module.go` | **改 wiring 声明**（不切 InMemoryDefaults） | 不碰 | 不碰 |

git append-only 文件（errcode / inventory / schema_guard 表清单）三 PR 同时 append 走 git 文本合并，无逻辑冲突。

---

## 8. 不做的事

| 事项 | 不做理由 |
|---|---|
| 引入 invariant Registry | funnel-fix roadmap §2 已立，主流项目无 prior art |
| 分布式锁实现 | PR#392 设计回滚后，admin provision 走 DB UNIQUE 而非分布式锁 |
| in-memory↔PG 双跑 fallback | 不留软回退（memory `feedback_no_soft_fallback`），postgres 模式 fail-fast on mem repo 出现 |
| backward-compat shim | 当前只有 gocell 自身，没有外部调用方（CLAUDE.md） |
| `X13 REFRESH-PARTITION-01` | 触发条件未达（生产流量阈值），保留 backlog |
| `S14a AWS KMS provider` | 触发条件未达（云平台部署），保留 backlog |
| `KERNEL-ROLLBACK-01` | V1.1+ 触发，保留 backlog_later |
| `OUTBOX-READY-DUAL-BARRIER-01` 接口级重构 | 已被 W8 直接删 silent fallback 替代（PR#420，见任务表） |

---

## 9. 风险与缓解

| 风险 | 缓解 |
|---|---|
| B2.A 文件域大（accesscore 4 联动），review 复杂度高 | L3 评级单 reviewer + 必要时上 `/ultrareview` |
| B2.B Command 域形态未定 | PR 开起前派 explorer 30min 核实 `devicecommand` slice 持久化需求 |
| B2.C 实际范围未定（4h vs 6h+） | PR 开起前派 explorer 30min 核实 migration 004 现状 |
| `UNIQUE(role=admin)` 选型（partial index vs CHECK） | B2.A 内决议：partial unique index 更标准（PG 主流），ADR 简短记 1-2 段 |
| migration 017-019 三条 PR 同时 append schema_guard 表清单 | B2.A 先落 schema_guard 主体 + 3 表，B2.B/B2.C 仅 append 表名（git 文本合并）|
| accesscore 4 联动激活把 PR 撑大 | 如 review 阻塞可拆 `B2.A.LINK`（联动激活独立 PR），但首选不拆（共生关系） |
| B5.FU 端到端测试在 mem session repo 时无法验证 cross-store rollback | B5 PR#399 已在 PR description 标 honest test-scope boundary，B2.A 落地后自动消解 |

---

## 10. 下一步

1. **派 2 个 explorer 并行**：
   - explorer-1：核 B2.B Command 域是否独立 repo（30min）
   - explorer-2：核 B2.C `CONFIG-VERSIONS-MIGRATION-01` 实际范围（30min）
2. explorer 回报后 **同步开 4 worktree**（B2.A/B/C + W9）
3. B2.A 优先派 reviewer，merge 后立即起 B5.FU worktree

历史 worktree（W4-W8）已全部 merge（PR#413/#414/#415/#416/#420），不再占调度容量。

---

## 11. 引用

- `docs/plans/202605011500-029-master-roadmap.md`：029 master roadmap（全 PG 项历史）
- `docs/plans/202605070431-pr403-funnel-fix-roadmap.md`：funnel-first 原则源（archtest 单文件主题约束依据）
- `docs/backlog.md` cap-07 / cap-10：backlog 单源
- `docs/architecture/202605051600-adr-pg-outbox-fencing.md`：B1 outbox fencing ADR
- `docs/architecture/202605061600-adr-bootstrap-admin-boundary.md`：B3 admin UNIQUE 路线决议
- `CLAUDE.md` `## 新增 invariant 决策原则`：archtest 设计原则
