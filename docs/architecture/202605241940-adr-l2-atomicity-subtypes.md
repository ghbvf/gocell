# ADR — L2 原子性测试分类体系 + Hard enforcement（L2-OUTBOX-ATOMICITY-COVERAGE-01）

## Status

Accepted (2026-05-24)

## Implementation

PR #950（分支 `062-l2-atomicity-coverage`）

## Closes

issue #876

---

## Context

**L2（OutboxFact）一致性等级**要求"本地事务 + outbox 发布"原子提交：domain row 与 outbox row 必须在同一 `RunInTx` 块内落盘，任一侧失败则全部回滚。`.claude/rules/gocell/go-standards.md` §各级测试要求明确规定 L2 必须有"outbox 原子性测试 + consumer 幂等测试"。

**升级前是 Soft enforcement**（人工记忆 + review）：

- 12 个 L2 单元中，只有 rbacassign（E2E 通路，#873）和 auditcore store 层（`adapters/postgres/audit_ledger_store_test.go`）提供了原子性证明，其余 10 个静默漂移。
- issue #655 即因漏报 L2 原子性证据而被标记；无机器护栏，AI 实施者随时可绕过。
- issue #876 原文把 auditappend 族 4 个 slice（auditappendconfig / auditappendrole / auditappendsession / auditappenduser）称作"consumer-only"，暗示其不需要原子性测试。

**本 ADR 核实并修正**：auditappend 族不是纯消费者。4 个 slice 通过 Go type alias 共享同一 `cells/auditcore/internal/appender/service.go::Service`：

```go
// cells/auditcore/slices/auditappendconfig/service.go
type Service = appender.Service
```

`appender.Service.HandleEvent` 在同一 `RunInTx` 内执行 `store.Append + outbox.Emit`，并实现 ConsumerBase 幂等 replay 语义。因此它是 **hybrid**（消费域事件 **并** 发布 `event.audit.appended.v1`），同时需要原子性测试与 consumer 幂等测试。

---

## Decision

### D1 — L2 subtype 三分类法

| 子类型 | 定义 | 实例 | 适用规则 |
|--------|------|------|---------|
| **producer** | HTTP/command 触发 domain write + outbox publish 同 tx | sessionlogin / sessionlogout / identitymanage / setup / rbacassign / configpublish / configwrite | 原子性测试（必须）；无 consumer 幂等要求 |
| **hybrid** | event ingest + ledger/projection write + outbox publish + 幂等 replay 同 tx | auditappendconfig / auditappendrole / auditappendsession / auditappenduser（折叠到 `appender`） | 原子性测试（必须）+ consumer 幂等测试（必须） |
| **store-level** | cell 级 store 原子性，无 service 编排 | auditcore cell | 原子性测试（必须）；无 consumer 幂等要求 |

**原子性测试断言要求（producer / hybrid / store-level 共同）**：

1. 正向控制：outbox writer fail → domain row 不持久（事务整体回滚）。
2. 负向控制：清除 outbox writer 注入 → domain row 正常持久（确认 row 本身是合法写入，而非测试副作用导致 0 行）。

**consumer 幂等测试断言要求（hybrid 专属）**：

1. 正常路径：相同 `EventID` 第二次 replay → `Ack` 且 `audit_entries` 行数不变（无重复 row）。

### D2 — 期望测试名从代码 SoR 确定性派生（Hard）

archtest `L2-OUTBOX-ATOMICITY-COVERAGE-01` 的期望测试名不靠手工维护的 map、命名公约或注释豁免，而是通过以下确定性派生链计算：

**上游枚举**（枚举集 SoR）：从 `cells/*/cell.yaml` + `cells/*/slices/*/slice.yaml` 的 `consistencyLevel: L2` 字段提取 L2 单元集合。metadata 字段非空强制（`gocell validate` 的既有 MSL-01 规则兜底），任何 L2 声明无需人工维护即被自动纳入枚举。

**定义包名派生**（期望名 SoR）：对每个 L2 slice，archtest 用 `go/types` 加载该 slice 的 Service 类型，调用 `types.Unalias` 解析别名后，取类型的**定义包名**（`Obj().Pkg().Name()`）作为 `<P>`。期望测试名规则：

- slice 级 producer → `TestL2Atomicity_<P>_RollsBack`
- slice 级 hybrid（Service 有 `HandleEvent(ctx context.Context, entry outbox.Entry) outbox.HandleResult` 方法签名）→ `TestL2Atomicity_<P>_RollsBack` + `TestL2Atomicity_<P>_ReplayIdempotent`
- cell 级单元（无 slice Service，由 cell.yaml `consistencyLevel: L2` 直接声明）→ `TestL2Atomicity_<cellID>_RollsBack`

archtest 将期望名集合与实际 `go/ast` FuncDecl 精确名 match，任何缺失即 CI 红。

### D3 — shared-impl 折叠用 go/types alias 解析（Hard）

4 个 auditappend slice 的 Service 字段类型是 `type Service = appender.Service`（Go type alias）。archtest 经 `types.Unalias` 解析后全部归结到定义包 `appender`，期望名统一为 `TestL2Atomicity_appender_RollsBack` 和 `TestL2Atomicity_appender_ReplayIdempotent`，天然折叠为 1 份测试，无需手工 allowlist。

若未来有人去掉 alias（改为 4 个 slice 各自定义 Service 结构体），期望集自动扩展为 4 个独立包名的期望名集合，强制各 slice 提供独立测试，不会静默漏掉。反向自检测试 `_AliasFoldsToSinglePackage` 断言当前 4 个 slice 的 Unalias 结果均指向 `appender` 包。

### D4 — 既有测试归一重命名（不向后兼容，单源）

| 旧名 | 新名 |
|------|------|
| `TestAuditLedgerStore_OutboxAtomicityFailureProof` | `TestL2Atomicity_auditcore_RollsBack` |
| `TestL2_RbacAssign_OutboxWriteFailure_RollsBack` | `TestL2Atomicity_rbacassign_RollsBack` |
| configcore 各 slice 现有 rollback 测试 | 并入 canonical `TestL2Atomicity_<P>_RollsBack` |

旧名不留别名。项目无外部消费方，不考虑向后兼容。

---

## AI-robust 评级

| 维度 | 机制 | 评级 |
|------|------|------|
| 上游枚举（L2 单元发现） | `cells/*/cell.yaml` + `slice.yaml` `consistencyLevel: L2` 字段，`gocell validate` MSL-01 强制非空，元数据 SoR 不可绕过 | **Hard** |
| typed 期望名派生 | `go/types` 加载 + `types.Unalias` 确定性解析，无启发式字符串锚点 | **Hard** |
| alias 折叠 | `types.Unalias` 确定性，go/types 结果与 AST 字符串形态无关 | **Hard** |
| 下游 exact-name AST match | FuncDecl 精确名匹配，无 string-comment 逃逸路径 | **Hard** |
| `_BodyAssertsRollback` 自检 | 测试体是否断言了 rollback 语义是静态分析不可判定问题，非可升级 carve-out，仅尽力而为（见下文说明） | **Medium**（接受，不入 backlog） |

**Funnel 双向锁评级**（依 `.claude/rules/gocell/ai-robust.md` §"Funnel 双向锁评级"）：

| 方向 | 评级 |
|------|------|
| 上游（L2 单元枚举） | **Hard**（metadata SoR + gocell validate 强制） |
| 下游（期望名 match） | **Hard**（FuncDecl 精确名 AST match） |

双侧均 Hard，构成闭环 funnel。

**`_BodyAssertsRollback` 为何不升 Hard**：判定"函数体是否真的断言了 rollback 语义"等价于判定函数语义等价性，属 Go 静态分析不可判定（undecidable）范围；不存在低成本且正确的 Hard 路径。该 Medium 项是实际语义边界，非技术债务，不开 backlog 追踪，文档注明即可。

形态参照 `.claude/rules/gocell/ai-robust.md` §"Hard 范本目录"中的"codegen funnel + golden"（metadata-driven 枚举 + 代码 SoR 派生）与"string-typed concept funnel"（类型信息精确解析代替字符串启发式）的近亲组合。

**Enforcement 运行位置（nightly，非 PR-time）**：`L2-OUTBOX-ATOMICITY-COVERAGE-01` 由 `archtest-nightly.yml`（16-shard）执行；**不**纳入 PR-time `hack/verify-archtest-invariants.sh`。后者由 ADR `202605120000` §Amendment 2026-05-23 §D8 冻结为 4 类核心运行时 invariant（clock / duration / testtime / panic），扩充该集合需另行 amend 该 ADR，不在本 PR 范围。本 coverage gate 选择 nightly 的理由：(1) 它守护的是 integration 测试的**存在性**，而被守护的测试本身 `//go:build integration` 依赖 Docker、只在 CI integration 分片实跑，PR-time 无法验证其真实通过；(2) `RunTypedProduction` 全量 typed-load 成本与 nightly 预算更匹配。新 L2 单元漏测试 → 当晚 nightly 红 + 本地 `make verify` 即时红。若未来需 PR-time 即时阻断，走 ADR `202605120000` §D8 amendment 把本 ID 加入冻结集（届时按 §"ADR amendment 落地必查"逐行重评威胁矩阵）。

**Gate 粒度 = per-unit（非 per-mutation）**：本 archtest 对每个 L2 slice 强制**一个** canonical `TestL2Atomicity_<pkg>_RollsBack`（hybrid 另加 `_ReplayIdempotent`），对齐 issue #876 的明文粒度「按 `consistencyLevel: L2` 枚举所有 L2 单元」。同一 slice 的多条 outbox mutation 路径（如 configwrite 的 Create/Update/Delete、configpublish 的 Publish/Rollback）各自的 `*_RollsBack_<Mutation>` 测试是**有意保留的 defense-in-depth，不在本 gate 的 Hard 期望集内**——删除它们不会触发 archtest 红。这是**已知且文档化**的边界，不是隐式 false-green：

- per-unit gate 已完整满足 #876 的 Hard 验收（每个 L2 单元必须有原子性证明）。
- per-mutation Hard 强制是 #876 之上的增强；其唯一 AI-robust 正解是从 Service「调 `RunInTx` 的 exported emitting method」typed 派生期望名（避免手列 suffix 这一 Medium 倒退）。该派生对 identitymanage（6 个 emitting method）等多 mutation slice 会显著扩展 E2E 测试面，且 `sessionlogin.IssueForUser` 等非-HTTP internal 路径无法经 E2E harness 驱动——实为 Cx4，与本 PR 体量不成比例。
- 故按 `feedback_pr_scope_carveouts_must_backlog` 登记为独立 backlog **#957**（cap-14 / type-test / pri-p2）跟踪 per-mutation typed 派生升级，不在本 PR 内做、也不以手列 suffix 临时打补丁。

---

## 威胁矩阵

| 威胁 | 缓解 |
|------|------|
| 新增 L2 slice 漏写原子性测试 | 上游枚举自动从 slice.yaml `consistencyLevel: L2` 发现；下游期望名 match 失败 → CI 红 ✓ |
| L2 降级为 L1/L0 逃逸检测 | `consistencyLevel` 变更触发 `gocell validate` MSL-01 + 其他治理规则 + code review；降级本身即语义变更，需显式决策 ✓ |
| 测试函数改名绕过 match | exact-name FuncDecl 匹配，任何改名立即导致期望集 diff 失败 ✓ |
| 空 body / `t.Skip` 假装测试存在 | `_BodyAssertsRollback` 自检（Medium，尽力而为）；full review gate 兜底 ⚠️（接受，见上文） |
| auditappend 去 alias 绕过折叠 | `_AliasFoldsToSinglePackage` 反向自检：断言 4 个 slice 的 Unalias 均指向 `appender` 包，去 alias 后自检失败 ✓ |
| 测试漏 `//go:build integration` tag | archtest 解析 build constraint，要求 producer/hybrid 的 `_RollsBack` / `_ReplayIdempotent` 测试携带 integration tag（否则无 PG 实例的 CI 会静默 skip，无原子性保护） ✓ |
| L2 slice 的 Service 改名/删除致期望集静默缩水 | `_ServiceLookupTotal` 反向自检：断言 go/types 加载到的 L2 Service 总数 ≥ 已知下限，防 Service 符号消失导致枚举静默归零 ✓ |
| 期望名派生公式依赖 go/types，但 go/types 加载失败静默跳过 | archtest 在 `RunTyped` 错误路径 `t.Fatal`，不静默跳过；`_ServiceLookupTotal` 额外守 ✓ |

---

## Implementation matrix

```
Contract: L2-OUTBOX-ATOMICITY-COVERAGE-01 archtest（Soft → Hard enforcement）
Change: 引入三分类法 + metadata-driven 期望名派生 + alias 折叠；删除人工 map / 命名公约
Implementations:
  [x] accesscore L2 slices（sessionlogin / sessionlogout / identitymanage / setup / rbacassign）
      → tests/integration/l2atomicity/ 下各 _RollsBack 函数归一命名
  [x] configcore L2 slices（configpublish / configwrite）
      → 各 slice service_integration_test.go 归一命名
  [x] auditcore L2 hybrid（auditappend* 4 个 slice 折叠到 appender）
      → cells/auditcore/internal/appender/service_integration_test.go
         TestL2Atomicity_appender_RollsBack + TestL2Atomicity_appender_ReplayIdempotent
  [x] auditcore store-level
      → adapters/postgres/audit_ledger_store_test.go
         TestL2Atomicity_auditcore_RollsBack（旧名: TestAuditLedgerStore_OutboxAtomicityFailureProof）
Conformance test: tools/archtest.TestL2OutboxAtomicityCoverage
Repro: go test ./tools/archtest/... -run TestL2OutboxAtomicityCoverage
Dependent contracts (governance scan): none
```

---

## 参考

- `.claude/rules/gocell/go-standards.md` §各级测试要求
- `.claude/rules/gocell/ai-robust.md` §"Hard 范本目录" / §"Funnel 双向锁评级"
- `tools/archtest/seed_role_iface_test.go`（metadata-driven Hard 枚举范本）
- `tools/archtest/l2_outbox_atomicity_coverage_test.go`（本 ADR 的 enforcement 载体）
- issue #876（原始 ask：补齐 12 个 L2 单元原子性证明 + Machine enforcement）
- PR #873（rbacassign L2 单点补齐，`TestL2_RbacAssign_OutboxWriteFailure_RollsBack` 先驱）
- issue #655（Soft 漏报证据：L2 slice 无原子性测试且无拦截机制）
- ADR `docs/architecture/202605241400-003-pg-repo-ambient-tx-discovery-hard.md`（同期 Soft→Hard 升级参照，metadata type-aware discovery 范本）
