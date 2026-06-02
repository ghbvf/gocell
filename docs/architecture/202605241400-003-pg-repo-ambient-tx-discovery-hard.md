# ADR 003 — PG-REPO-AMBIENT-TX-01 双轴 Hard 化（type-aware discovery + typed marker）

## Status

Accepted (2026-05-24); **Amended 2026-05-27** — upstream Medium → Hard via
sealed `internal/pgexec/` sub-package + cross-package wrap funnel. Closes
gh #738 / #916. See §Amendment 2026-05-27 below.

**Amended 2026-05-28** (#1194 leftover C1–C4) — R3(b) sibling-marker →
**call-bound typed approval argument** (`pgexec.ExecDirect(pgrepoapproved.Approve("…"), …)`);
R3 scope file-extension → **global**; `PGExecutor` interface **sealed** via
unexported marker method (+ archtest regression backstop); R2 counts **unnamed**
pool params. Removes the entire same-approval-scope co-location machinery
(BS-8 spurious-marker, nested-closure scope, per-callsite M==E counting). See
§Amendment 2026-05-28 below. R1/R2 file-extension Soft scope unchanged (#1206).

## Context

**Issue #823 (F-A-002)** 跟踪 `PG-REPO-AMBIENT-TX-01` archtest 从"上游 Soft +
下游 Hard"升级到闭环 Hard funnel（依 `.claude/rules/gocell/ai-robust.md`
§"Funnel 双向锁评级"）。

升级前的状态（`tools/archtest/pg_repo_ambient_tx_test.go`）：

- **下游 R1/R2/R3 Hard** ✓：用 `*types.Info` 解析 `*pgxpool.Pool` /
  `newPGExecutor` / `pgExecutor` 命名类型；无字符串锚点。
- **上游 Soft**：`pgRepoPackagePatterns` 是 3 条人工维护列表（`adapters/postgres` /
  accesscore 私有 / iotdevice 私有）。新增 cell 的 PG 适配包需开发者记得追加，
  漏写 = coverage 静默清零。**PR #575 F-A-001 就是这个漂移**：iotdevice 模式
  最初遗漏，导致该包所有 repo 文件逃过 R1/R2 检查。
- **下游 R3(b) Soft**：`r3ExecDirectAllowlist map[string]struct{}` 单条字符串
  锚点 entry `"adapters/postgres/refresh_store.go::revokeSessionDetachedAt"`。
  改文件名 / 改函数名 / map key 写错 = 静默失效。

> **2026-05-24 PR #917 round-2 修订**：本 ADR 第一版把 R3(b) 升级形态写为
> "typed marker funnel：(callee, arg) form-uniqueness Hard"，但实施版本的 arg
> 检查仅 `tv.Value.Kind() == constant.String`，仍接受 const ident /
> 常量折叠 `"a"+"b"` / 空串 / 占位符 — 这只是 form-uniqueness 假象，落地评级
> 实际仍 Soft（"像但不是 const literal"灰区）。同上， marker 检查通过
> `EachInSubtree` 穿透 `*ast.FuncLit`，nested closure 内的 marker 可批准外层
> ExecDirect（scope 漂移）。Round-2 修复（见下"轴 B 实现细节"）把 arg 校验改
> 为 `*ast.BasicLit` + kebab regex + placeholder ban 5-门串行，marker 扫描收
> 紧到 `inspectStopAtFuncLit` scope-bounded — R3(b) 才真正达到 Hard。

**Soft → Hard 升级动机**：按 `ai-robust.md` "Soft 严禁立项 / 既有 Soft 优先升
Hard"原则，两处 Soft 必须同 PR 升级。

## Decision

> **2026-05-27 重写**：原 §轴 A "上游 type-aware 自动发现（Soft → Medium）"决策已被 amendment 取代。原文（discoverPGAdapterPackages + TestPGRepoAmbientTx_DiscoveryCoverage + expectedPGAdapterPackageMin floor）描述的全部机制已在本 PR 删除。下方为重写后的 §轴 A。
>
> 原文历史脉络归入版本控制（git show <pre-amendment-commit>:docs/architecture/202605241400-003...md），不留在文档主线避免双真理源。

双轴升级：

### 轴 A — 上游 sub-package seal（Medium → Hard，amended 2026-05-27）

**核心改造**：把 `pgExecutor` struct + `*pgxpool.Pool` 字段移到每个 PG adapter 包的 `internal/pgexec/` 子包内，并保持类型与字段 unexported。

| 形态属性 | Pre-amendment（2026-05-24） | Post-amendment（2026-05-27） |
|---------|---------------------------|---------------------------|
| `pgExecutor` struct 位置 | 各 PG adapter 包顶层（包 private） | `<adapter>/internal/pgexec/pgexec.go`（sub-pkg private）|
| 构造路径 | `newPGExecutor(pool)` 包内私有 | `pgexec.New(pool) PGExecutor` 跨包 typed factory |
| Parent-pkg 字段类型 | `db pgExecutor`（concrete struct value）| `db pgexec.PGExecutor`（interface）|
| ExecDirect 形态 | `(e pgExecutor) ExecDirect(...)` method | `pgexec.ExecDirect(e PGExecutor, ...)` top-level function（F2-Hard，无 subset-interface bypass）|
| `.pool` 字段可达性（包外）| 同包 sibling 文件可达 | **compile-time impossible**：unexported struct + unexported field + cross-pkg unreachable |
| Discovery 机制 | type-aware scope lookup `pgExecutor` | **删除**——R1/R2/R3 改为全局谓词 |

**Hard 实现机制**（ai-robust.md §Hard 范本 #6 "sealed construction"）：

- `internal/pgexec/` 子包受 Go `internal/` 可见性规则约束——只有以 `<adapter>/internal/pgexec/` 为前缀的祖先包可 import；外部包无法 import，无法引用 unexported `pgExecutor` 类型名。
- `pgExecutor` struct 与 `*pgxpool.Pool` 字段在 sub-pkg 内 unexported——parent-pkg 包外无法字段声明、type assertion、composite literal 构造。
- 唯一获取 `pgexec.PGExecutor` 实例的路径是 `pgexec.New(*pgxpool.Pool)` exported factory——其返回的 interface 不暴露 `.pool` 字段。
- 与 `HEALTH-REDACTED-ERROR-MSG-FUNNEL-01`（`SlogDependencyEntry` unexported fields）同款形态。

**Discovery 删除后的 archtest scope**（amended 2026-05-28：R3 移出 file-extension scope）：

- R1（pool field）走全局谓词 + `*_repo.go` / `*_store.go` 文件后缀过滤——文件后缀是 **pre-existing Soft scope boundary**（PR #917 时就在），本 amendment 未触碰也未引入新 Soft；其升级路径见 §"已知 Soft scope" + backlog issue 跟踪。
- R2（pool wrap funnel）走全局谓词 + 同 file extension 过滤——callee 解析到 `pgexec.New` cross-package typed function。
- R3（ExecDirect callsite）**自 2026-05-28 amendment 起为 global**（无 file-extension scope）——call-bound approval + callee identity 使 file scope 不再必要，详见 §轴 B。
- R3 callsite identity（amendment 之前是 `pgExecutor.ExecDirect` 方法 receiver type；amendment 之后是 `pgexec.ExecDirect` 函数 callee identity）—— **F2-Hard 闭合 subset-interface bypass 漏洞**。

**配套 archtest 变化**：

- 删除 `discoverPGAdapterPackages` + `TestPGRepoAmbientTx_DiscoveryCoverage` + `expectedPGAdapterPackageMin` 常量——sub-pkg seal 提供 compile-time identity，archtest 无需 type-aware 发现层。
- 删除 R3(a)（`.pool` 字段直接访问）—— interface 不暴露 `.pool`，compile-time 不可表达；archtest 检查冗余。
- 删除 BS-7（pool 局部赋值反向自检）—— 同 R3(a) 退役。
- BS-6 形态从"method-value indirection of pgExecutor.ExecDirect"改写为"function-value indirection of pgexec.ExecDirect"——与 BS-3（pgexec.New function value）同源 helper。

### 轴 B — 下游 R3 call-bound typed approval（Soft → Hard；2026-05-28 重写）

> **2026-05-28 重写**：原 §轴 B 的「同 scope sibling marker」形态
> (`pgrepoapproved.ApprovedExecDirect("…")` 独立语句 + scope co-location 检查)
> 已被 **call-bound typed approval 参数**取代——审批绑定到 ExecDirect 调用表达式
> 的首参，不再是独立 marker 语句。原文（sibling marker / `bodyHasApprovedExecDirectMarker`
> / scope-bounded / BS-8 spurious-marker）归入版本控制，不留文档主线避免双真理源。

**marker package**：`pkg/pgrepoapproved`（sibling deployment of
`pkg/panicregister`）暴露 sealed `Approval` interface + 单一 token minter：

```go
package pgrepoapproved

type Approval interface{ approvedExecDirect() } // sealed: 包外不可实现/构造
type approval struct{}
func (approval) approvedExecDirect() {}
func Approve(reason string) Approval { _ = reason; return approval{} }
```

**callsite 形态**：审批是 `pgexec.ExecDirect` 的**第一参数**，inline 构造：

```go
func (s *PGRefreshStore) revokeSessionDetachedAt(...) error {
    // ...
    _, err := pgexec.ExecDirect(pgrepoapproved.Approve("revoke-session-cascade"),
        s.db, cascadeCtx, revokeSessionSQL, revokedAt, sessionID)
    return err
}
```

「调用无审批」= 编译错误（首参类型 `Approval`）；「审批无调用」= 不可表达
（无独立 marker 语句可错放 / 复用 / 漂移到错误 scope）。

**archtest 形态**：R3 改为 **global**（无 file-extension scope）——对每个 callee
identity 解析到 `pgexec.ExecDirect`（`Pkg().Path()` ending `/internal/pgexec`
AND `Name() == "ExecDirect"`）的 CallExpr，断言 `arg[0]` 是 inline CallExpr 到
`pgrepoapproved.Approve`，且 `Approve` 的 `arg[0]` 满足 5-门 form-uniqueness：

1. `call.Args[0]` 是 `*ast.CallExpr`（拒复用变量 / 预构造 `Approval`——`*ast.Ident`）
2. 该 CallExpr 的 callee 解析（via `*types.Info.Uses`）到 `*types.Func` 满足
   `Name() == "Approve"` AND `Pkg().Path() == "github.com/ghbvf/gocell/pkg/pgrepoapproved"`
3. `Approve` 的 `arg[0]` 是 `*ast.BasicLit` AND `Kind == token.STRING`
   （拒 const identifier / `"a"+"b"` BinaryExpr / 变量 / `fmt.Sprintf`）
4. `strconv.Unquote` 后 match `^[a-z][a-z0-9-]+$`（kebab-case，长度 ≥ 2）
5. 不在占位符集 `^(todo|fixme|tbd|xxx|placeholder|wip)(-|$)`

理由：与 `PANIC-REGISTERED-01` 同 Hard 范本——"typed marker funnel for
unbounded ops"，但更强：审批与调用是同一表达式（call-bound），消除了原
sibling-marker 形态全部的 scope 复杂度（co-location / nested-closure /
per-callsite M==E）。`scanR3ExecDirectInScope` / `countApprovedExecDirectMarkers`
/ `stopAtFuncLit` / BS-8 spurious-marker 全部删除——standalone marker 在
call-bound 形态下结构上不存在。`Approve`-as-function-value（`f := Approve;
ExecDirect(f("x"), …)`）由 gate #2 callee 解析失败兜住（main rule 即 flag），
无需独立反向自检（盲区 BS-7，见 archtest godoc）。

## Funnel 双向锁评级表（amended 2026-05-27）

| 维度 | 原始（2026-05-24） | Amended（latest，2026-05-28） | 形态 |
|------|--------|--------|------|
| **上游 — pgExecutor 形态包外不可达** | Medium（archtest type-aware：`Scope().Lookup("pgExecutor")`）| **Hard**（compile-time，Go visibility）| sealed `internal/pgexec/` sub-package：`pgExecutor` 结构体 + `*pgxpool.Pool` 字段 unexported；parent-pkg 包外无法引用类型，无 type-assert 路径，无 .pool 访问路径 |
| **上游 — PGExecutor 接口包外不可实现**（2026-05-28 新增） | —（接口无 seal，包外可自实现）| **Hard**（Go visibility，sealed interface unexported marker method）**+ archtest 回归守卫** | `PGExecutor` 加 unexported `sealPGExecutor()` marker → 包外无法声明 parallel impl，"所有 PGExecutor 值来自 New" 成为 type-system 保证（backstop R1 file-scope gap）；`TestPGRepoAmbientTx_InterfaceSealed` 断言 marker 在场（防静默删除）+ `*pgExecutor` 实现 |
| **下游 R1 — pool field in repo/store layer** | Hard（`*types.Info` 字段类型解析）| Hard via `*types.Info` 字段解析（不变），**archtest scope by `*_repo.go`/`*_store.go` file extension is pre-existing Soft**（见 §已知 Soft scope）| 全局谓词 + Soft file-extension scope；compile-time Hard 来自 sub-pkg seal，archtest 是 defense-in-depth |
| **下游 R2 — wrap funnel `New*` → `pgexec.New`** | Hard | Hard via callee identity（`Pkg().Path()` 以 `/internal/pgexec` 结尾 AND `Name() == "New"`）；同 Soft file extension scope；**unnamed pool param 也计入**（2026-05-28，原静默丢弃） | 全局谓词 + Soft file-extension scope |
| **下游 R3(a) — pool field access** | Hard（archtest-bound）| **retired** | compile-time impossible — `pgexec.PGExecutor` interface 不暴露 `.pool` |
| **下游 R3(b) — ExecDirect callsite** | Hard（typed marker funnel，**method receiver type check**）| **Hard via callee identity + call-bound approval**（2026-05-28：sibling marker → 首参 typed approval token；scope file-extension → global）| ExecDirect 是 `pgexec.ExecDirect(approval, e, ctx, sql, args...)` 顶层函数；callee identity ending `/internal/pgexec` AND `Name() == "ExecDirect"`；`arg[0]` 必须 inline `pgrepoapproved.Approve(<kebab-literal>)`（call-bound，无 scope co-location / marker 复用 / nested-closure 走私）；全局扫描无 file-extension scope |

**Amendment compensation column**（per ai-robust.md §"ADR amendment 落地必查"）：

- 上游 Medium → Hard：升级，无 ✅→⚠️ 回退。
- R3(a) retired：升级到 compile-time（不可表达性优于 archtest form-uniqueness）；BS-7 一并退役。
- R3(b) receiver-type 检查 → callee-identity 检查：**关闭 PR 引入的 subset-interface bypass surface**——pre-PR pgExecutor 是 unexported struct，外部不可获取值；post-PR PGExecutor 是 exported interface，外部 `pgexec.New(pool)` 可拿到值，理论上声明 subset interface 即可绕过 receiver-type 检查。F2-Hard 把 ExecDirect 从 method 提到 top-level function，subset interface 无法 invoke 不存在的 method——bypass 在 type system 层不可表达。
- R1/R2 archtest scope（file extension）保持 pre-existing Soft 状态——本 amendment 未触碰，亦未引入新 Soft。升级路径见 §已知 Soft scope + backlog issue。

**上游 Hard 实现机制**：

- `internal/pgexec/pgexec.go` 是包外不可 import 的位置（Go `internal/` 可见性规则）。
- `pgExecutor` struct 与 `*pgxpool.Pool` 字段在 sub-pkg 内 unexported；parent-pkg 包外无法引用类型名进行字段声明、类型断言、composite literal 构造。
- 唯一获取 `pgexec.PGExecutor` 实例的路径是 `pgexec.New(*pgxpool.Pool)` 工厂；其返回的 interface 类型不暴露 `.pool` 字段，也不暴露 `ExecDirect` 方法。
- 4 个 PG adapter package（adapters/postgres、adapters/postgres/saga、cells/accesscore/internal/adapters/postgres、examples/iotdevice/cells/devicecell/internal/adapters/postgres）各自一份镜像 `internal/pgexec/` 子包（CLAUDE.md 分层规则 `cells/ ❌ adapters/` 阻止单一共享位置）。

**与同形态 funnel 的对照**：HEALTH-REDACTED-ERROR-MSG-FUNNEL-01 的 `SlogDependencyEntry` unexported fields 是同款"sealed construction" Hard 形态（ai-robust.md §Hard 范本 #6 "sealed construction"）；`pgexec.ExecDirect` top-level function + call-bound `pgrepoapproved.Approve` 首参（2026-05-28）与 `panicregister.Approved` 同款 "typed marker funnel for unbounded ops"（ai-robust.md §Hard 范本 #2，approval 绑定到调用表达式）。

## 已知 Soft scope（pre-existing，allowed暂留 + backlog 跟踪）

本 ADR amendment **未引入**新 Soft，但显化记录 pre-existing Soft scope 边界 + 升级路径。

### Pre-existing Soft #1: archtest file-extension scope filter（仅 R1/R2，2026-05-28 起）

- **位置**：`tools/archtest/pg_repo_ambient_tx_test.go::isRepoOrStoreFile()`——**R1/R2** 仅扫描 `*_repo.go` / `*_store.go` 后缀文件。**R3 自 2026-05-28 改为 global**（call-bound approval + callee identity 使 file scope 不再必要），不再受此 Soft scope 约束。
- **历史**：PR #917（2026-05-24，本 ADR 原始版本时）就已存在；2026-05-27 amendment 未触碰；2026-05-28 amendment 把 R3 移出该 scope（R1/R2 仍在）。
- **为什么是 Soft**：file 名是字符串约定，per ai-robust.md §"Soft 形态" / §"Soft → Hard 改造方向 字符串锚点 → typed function call"。
- **当前实际影响**：infrastructure 层（`pool.go` / `tx_manager.go`）合法持有 `*pgxpool.Pool`，文件名后缀的"非 _repo.go/_store.go = 基础设施层"约定承担了 R1/R2 的 scope 区分职责。
- **真 Hard 升级路径**：把 6 种 pool-holder 形态（Executor / Pool / TxManager / Bundle pass-through / cmd composition wiring / configcore Session parallel funnel）全部 seal 到各自的 `internal/<noun>/` 子包，R1/R2 改为"任何 `*pgxpool.Pool` 字段的 package path 必须在 sealed sub-pkg 集合内"——全局谓词无文件名 scope。
- **Backlog 跟踪**：gh issue [#1206 PG-INFRA-FULL-SEAL-01](https://github.com/ghbvf/gocell/issues/1206)（labels: `backlog pri-p2 cap-14 flag-cond type-arch-opt`）。

### Pre-existing Medium #2: same-pkg sibling 绕过（intra-sub-pkg）

- **形态**：sub-pkg `internal/pgexec/` 内部 sibling 文件（如未来加 `pgexec_helper.go`）可直接访问 `pgExecutor.pool` field。Go visibility 在同包内不限制。
- **当前实际影响**：sub-pkg 极小（每个仅 1 个 pgexec.go 文件 ~80 行），review 范围可控。
- **真 Hard 升级路径**：把 `pgExecutor.pool` 字段放入更深的 `internal/pgexec/internal/<storage>/` 子子包——但收益边际递减，未规划。
- **Backlog 跟踪**：暂不立项（acceptable threat per code review）。

## Out of scope

- **configcore `Session`/`DBTX` funnel formalization**：configcore 用结构性
  不同的另一种 ambient-tx funnel（local `DBTX` interface + `*Session` 包装）。
  其 `Session` struct 持有 `*pgxpool.Pool`，与 `pgExecutor` 角色对等但命名
  不同。若需等价治理，需独立 ADR + 独立 archtest（命名建议
  `PG-REPO-AMBIENT-TX-02`），不在 #823 ask 范围。本 PR 只 commit 到"该包不
  在 PG-REPO-AMBIENT-TX-01 范围"这个事实，不强制迁移。**重要：configcore 目前无等价 archtest 治理**；其 `Session`/`DBTX` 的 ambient-tx 安全边界依赖 code review。需要 archtest 覆盖时新开 gh issue 跟踪 `PG-REPO-AMBIENT-TX-02`。

## Threat Model

> **2026-05-28 重评**（per ai-robust §"ADR amendment 落地必查"）：随 R3 改为
> call-bound approval，「移到不相关 func body 放 marker / BS-8 spurious」「nested
> closure 走私 marker」两行**删除**——standalone marker 在 call-bound 形态下结构上
> 不存在（审批是调用首参，不是独立语句）。新增「复用/预构造 Approval 变量」「包外
> 自实现 PGExecutor」「ExecDirect 在非 _repo.go 文件」三行。无 ✅→⚠️/❌ 回退（均为升级或新增覆盖）。

| 威胁 | 缓解 |
|------|------|
| 新 PG 适配包沿用 `pgExecutor` 范式 | R1 / R2 全局谓词（_repo.go / _store.go scope）自动覆盖；新 sub-pkg 通过路径后缀 /internal/pgexec 自动入 funnel ✓ |
| 新 PG 适配包发明新 funnel（如 configcore Session） | 本 archtest 不覆盖；约定 + code-review + new-pattern-requires-ADR 兜底（不是回归 — 升级前也不覆盖） |
| 新 ExecDirect callsite 漏传审批 | 编译错误——首参类型 `pgrepoapproved.Approval` 必填，无法省略 ✓ |
| ExecDirect callsite 传了审批但形态退化（const ident / `"a"+"b"` concat / 空串 / 占位符 / `fmt.Sprintf` / 变量） | R3 gate #3-5：`Approve` 的 arg[0] 必须 `*ast.BasicLit + token.STRING` + kebab regex + placeholder ban；RED fixture `badR3ConstIdentReason` / `badR3ConcatReason` / `badR3EmptyReason` / `badR3PlaceholderReason` 守 ✓ |
| 复用 / 预构造 `Approval` 变量绕过 inline 约束（`a := Approve("x"); ExecDirect(a, …)`，或共享一个 token 给多个 callsite）（2026-05-28 新增） | R3 gate #1：`call.Args[0]` 必须是 inline `*ast.CallExpr`；变量是 `*ast.Ident` → flag；RED fixture `badR3ReusedApproval` 守 ✓ |
| 包外自实现 `PGExecutor`（parallel impl 持 raw pool + 跳过 ambient-tx routing）（2026-05-28 新增） | 接口 sealed（unexported `sealPGExecutor` marker method）→ 包外编译不可实现；`TestPGRepoAmbientTx_InterfaceSealed` 回归守卫断言 marker 在场（防静默删除）+ `*pgExecutor` 实现 ✓ |
| ExecDirect callsite 出现在非 `_repo.go`/`_store.go` 文件（如 service.go）（2026-05-28 新增） | R3 改为 global——扫描所有 production 文件，不受 file-extension scope 约束；RED fixture `fixture_service.go::serviceLayerBadExecDirect` 守 ✓ |
| 拷贝 `pgrepoapproved` 包到其他位置绕过 funnel | callee 解析锁 `Pkg().Path()` 到精确字符串 `"github.com/ghbvf/gocell/pkg/pgrepoapproved"`，重定向 import 不改 package path ✓ |
| import 别名绕过 `Approve` callee 识别 | callee 解析锁 `fn.Pkg().Path()`（via *types.Info.Uses），import alias 不改 package path ✓ |
| `Approve` 当函数值间接调用（`f := Approve; ExecDirect(f("x"), …)`） | R3 gate #2：`Approve` callsite 的 callee `f` 解析到 `*types.Var` 而非 `*types.Func` → `resolveCalleeFunc` 返回 nil → flag；main rule 即捕获（盲区 BS-7，见 archtest godoc）✓ |
| build-tag 隔离的条件性 ExecDirect 调用 | `Run(t, Typed(...))` 用 default build context；如需覆盖须扩展 TypedOpts；当前 production 代码库无 build-tag 隔离 ExecDirect 先例；以 code review 兜底（accepted threat） |
| ExecDirect 仅 adapters/postgres 当前持有 | accesscore 的 pgexec.PGExecutor 暴露 ExecDirect（仅集成测试用，无 production callsite）；devicecell / saga 不暴露（saga 用 AcquireTx 进行 tx ownership） ✓ |

## Implementation matrix

```
Contract: PG-REPO-AMBIENT-TX-01 archtest（上游 Medium → Hard via sealed internal/pgexec sub-package; closes gh #738 / #916）
Change (2026-05-27): 把 pgExecutor 移到 <adapter-pkg>/internal/pgexec sub-package；parent-pkg 持有 pgexec.PGExecutor interface；archtest 删除 discovery，R1/R2/R3 改为全局谓词
Change (2026-05-28, #1194 C1-C4): R3 sibling marker → call-bound pgrepoapproved.Approve 首参 + R3 global scope；PGExecutor 接口 seal（sealPGExecutor marker）+ InterfaceSealed 回归守卫；R2 计入 unnamed pool param；ExecDirect !ok 分支改 A-class panic（删 error sentinel）
Implementations: [x] adapters/postgres/internal/pgexec  [x] adapters/postgres/saga/internal/pgexec  [x] cells/accesscore/internal/adapters/postgres/internal/pgexec  [x] examples/iotdevice/cells/devicecell/internal/adapters/postgres/internal/pgexec
Conformance test: tools/archtest.TestPGRepoAmbientTx + TestPGRepoAmbientTx_RedFixtureDetected + TestPGRepoAmbientTx_SelfCheck + TestPGRepoAmbientTx_InterfaceSealed + TestPGRepoApprovedSealed
Repro: go test ./tools/archtest/ -run 'TestPGRepoAmbientTx|TestPGRepoApprovedSealed' -count=1
Dependent contracts (governance scan): none
```

## Amendment 2026-05-27 — upstream Medium → Hard via sealed sub-package

**触发**：gh #738 (`PG-REPO-AMBIENT-TX-UPSTREAM-HARD-01`，触发型 backlog) +
gh #916 (`PGEXECUTOR-SEAL-INTERFACE-01`)。Trigger (b)（"新增 cell adapter
package 需要同等保护"）已结构性触发——4 个 PG adapter packages 各自镜像
pgExecutor funnel。

**Decision**：把每个 PG adapter package 内的 `pgExecutor` struct + `newPGExecutor`
factory 整体迁移到 `<adapter-pkg>/internal/pgexec/` sub-package。Sub-package
导出 `PGExecutor` interface（**Exec/Query/QueryRow 三方法**，saga 多 AcquireTx；
ExecDirect 不在 interface 上）+ exported `New(*pgxpool.Pool) PGExecutor` 工厂。
ExecDirect 是 sub-pkg 顶层函数 `pgexec.ExecDirect(e PGExecutor, ctx, sql, args...)`，
不是 interface method——F2-Hard 关闭 subset-interface bypass。parent-pkg repos/
stores 持有 `pgexec.PGExecutor`（interface 字段），构造点调 `pgexec.New(pool)`，
ADR-approved bypass 站点显式调 `pgexec.ExecDirect(s.db, ...)` + 同 scope per-
callsite marker `pgrepoapproved.ApprovedExecDirect("<reason>")`（round-3 C4：
M==E 1:1 配对）。

> **⚠️ 已被 2026-05-28 amendment 取代**：上段「`pgexec.ExecDirect(s.db, ...)` + 同 scope
> sibling marker `ApprovedExecDirect` / M==E 1:1 配对」与 `ExecDirect` 签名
> `(e PGExecutor, ctx, ...)` 是 2026-05-27 当时形态。现 ExecDirect 首参为
> `pgrepoapproved.Approval`，审批是 call-bound（`pgexec.ExecDirect(pgrepoapproved.Approve("…"), s.db, …)`），
> 无独立 marker / 无 scope co-location。以 §Amendment 2026-05-28 为准。

**Implementation summary**：
- 新增 4 个 sub-package（adapters/postgres/internal/pgexec/、adapters/postgres/saga/internal/pgexec/、cells/accesscore/internal/adapters/postgres/internal/pgexec/、examples/iotdevice/cells/devicecell/internal/adapters/postgres/internal/pgexec/）；
- 删除 4 个旧 pg_executor.go（root + saga + accesscore 重命名为 tx_assert.go 保留 assertAmbientTx helper + devicecell）；
- ~12 个 *_repo.go / *_store.go 把 `db pgExecutor` 字段改为 `db pgexec.PGExecutor`，`newPGExecutor(pool)` 改为 `pgexec.New(pool)`；
- adapters/postgres/command_queue.go 额外清理 `pool *pgxpool.Pool` 直接字段绕过（refactor `q.pool.Query(...)` 为 `q.db.Query(...)`，pool 字段删除）；
- saga `acquireTx` 方法重命名为 `AcquireTx`（导出，sub-pkg 外部调用）；
- archtest `tools/archtest/pg_repo_ambient_tx_test.go` 重写：删除 discovery / `_DiscoveryCoverage` 测试 / `expectedPGAdapterPackageMin` 常量 / `r3PoolAccess`（R3(a)）；R1 简化（删除 struct-name check，repo/store 层禁持 pool）；R2 callee 改为跨包 `pgexec.New`；R3(b) receiver 改为 `pgexec.PGExecutor` interface；BS-7 删除（compile-impossible），BS-5 保留（cells/accesscore Bundle helper）；
- RED fixture 重构为 `internal/pgexec/`（GREEN holder）+ `fixture_repo.go`（所有 RED + GREEN repo controls，文件后缀触发 archtest 扫描）+ `fixture.go`（仅 package godoc）；
- ADR `docs/architecture/202605101200-adr-typed-go-heavy-protocol-primitives.md` §4.5.1 cross-ref 添加 2026-05-27 amendment paragraph 指向本 amendment。

**Compensation**：
- R3(a) archtest retired（升级到 compile-time），BS-7（pool 局部赋值反向自检）一并删除；sub-pkg 内部新增文件触发 .pool 访问由 sub-pkg-internal code review 兜底——sub-pkg 文件极少（pgexec.go 一个），review 成本可接受。
- 调试时若需 pool（如 ops 排查），从 composition root（cmd/）注入新 helper，不应通过 type assertion 反向访问。
- 不引入 backwards-compat shim（CLAUDE.md "不考虑向后兼容"）；老的 `pgExecutor` package-private struct + `newPGExecutor` 全删，不留 alias。

**Threat Model 重评**：
- "Refactor 把 pgExecutor 改名 / 移出包 scope" 旧威胁 ❌：discovery 删除，无 `_DiscoveryCoverage` 集合断言；新威胁形态 = parent-pkg 重新引入 `*pgxpool.Pool` 字段（被 R1 全局谓词捕获）或 New* 构造函数绕过 `pgexec.New`（被 R2 全局谓词捕获）。原威胁矩阵中 "Refactor 改名" 行需删除（discovery 不再存在）；R1 / R2 自身 caller-side 捕获机制已包含。
- 新威胁："parent-pkg 在非 _repo.go/_store.go 文件中 newPGExecutor-like 重新构造 *pgExecutor"：sub-pkg unexported 类型，parent-pkg 包外不可写出 `pgexec.pgExecutor{pool: ...}` composite literal（Go 编译错误）。✓
- 新威胁："parent-pkg 通过 reflect 反向获取 .pool"：超出静态分析范围（与 BS-2 同款 accepted threat），accepted。

**Out-of-scope 同 PR 变化**：
- 删除 "pgExecutor sealed interface 改造" Out-of-scope 条目（已完成）。

## Amendment 2026-05-28 — R3 call-bound approval + global scope + interface seal

**触发**：PR #1194 review 复盘（C1–C4 / F1–F5）暴露 2026-05-27 amendment 留下的残留：
R3 审批是同 scope sibling marker（可错放 / 复用 / nested-closure 走私，靠 scope
co-location 检查兜，复杂且非 call-bound）；R3 scope 仍受 file-extension Soft 约束；
`PGExecutor` 接口本身未 seal（包外可自实现 parallel impl 持 raw pool）；R2 静默丢弃
unnamed pool param。

**Decision**：
1. **R3 call-bound approval（C2/F4）**：`pgrepoapproved` 从 no-op marker `ApprovedExecDirect(reason)`
   改为 sealed `Approval` interface + `Approve(reason) Approval` token minter；
   `pgexec.ExecDirect` 首参改为 `pgrepoapproved.Approval`。审批绑定到调用表达式
   （inline `Approve("<kebab>")`），「调用无审批」= 编译错误、「审批无调用」= 不可表达。
   archtest R3 改为 global（无 file-extension scope），断言每个 `pgexec.ExecDirect`
   callsite 的 arg[0] 是 inline `Approve(<kebab-literal>)`（5-门 form-uniqueness）。
2. **接口 seal（C1/F3）**：4 个 pgexec 包的 `PGExecutor` 加 unexported `sealPGExecutor()`
   marker method → 包外不可实现。新增 `TestPGRepoAmbientTx_InterfaceSealed` 回归守卫
   （断言恰好一个 unexported marker method + `*pgExecutor` 实现），防 marker 被静默删除。
3. **R2 unnamed param（C1/F1）**：`collectPGPoolParams` 计入匿名 `*pgxpool.Pool` 参数（"_" sentinel）。
4. **RED oracle 收紧（C3/F2）**：`TestPGRepoAmbientTx_RedFixtureDetected` diagKey 加文件名 + exact multiset 计数。
5. **ExecDirect 不可达分支（软点 2）**：删除 `errExecDirectOnNonSealedExecutor` + `execDirectMisuseError`；
   `e.(*pgExecutor)` `!ok` 走 A-class `panic(panicregister.Approved("pgexec-execdirect-non-sealed", errcode.Assertion(...)))`
   —— 接口 seal 后该分支结构上不可达（仅 in-package test mock 可触发）。

**Implementation summary**：
- `pkg/pgrepoapproved`：`ApprovedExecDirect` → `Approval` interface + `Approve`；
- `adapters/postgres/internal/pgexec` + accesscore 镜像：ExecDirect 首参 `Approval` + 删 error sentinel + A-class panic + seal marker；saga / devicecell：仅加 seal marker；
- `adapters/postgres/refresh_store.go`：callsite 改 `pgexec.ExecDirect(pgrepoapproved.Approve("revoke-session-cascade"), ...)`；
- `role_repo_integration_test.go`：7 callsite 传 Approve token；pgexec unit test：misuse → A-class panic 断言；
- archtest 重写：R3 global + call-bound（删 `scanR3ExecDirectInScope` / `countApprovedExecDirectMarkers` / `stopAtFuncLit` / BS-8）；R2 unnamed；新增 `TestPGRepoAmbientTx_InterfaceSealed`；godoc 重写 + 盲区清单（BS-7 Approve-as-value）；
- RED fixture：call-bound 形态 + unnamed-param R2 + `fixture_service.go`（非 _repo.go R3 global 证明）。

**Compensation / Threat Model 重评**：见上 §Threat Model 2026-05-28 重评 block——删「BS-8 spurious marker」「nested closure 走私」两行（call-bound 下 standalone marker 结构上不存在），新增「复用 Approval 变量」「包外自实现 PGExecutor」「ExecDirect 非 _repo.go 文件」三行覆盖。无 ✅→⚠️/❌ 回退。

**Out-of-scope（不变）**：R1/R2 file-extension Soft scope 移除仍由 #1206 跟踪（需 seal Pool/TxManager/Bundle/configcore 4 种 holder）；本 amendment 只把 R3 移出该 scope。

## Amendment 2026-05-29 — R4 orphan ban + typed ApprovalReason catalog + accesscore ExecDirect build-tag isolation

**触发**：PR #1224 review 复盘（C1–C3 / F1–F8）暴露 2026-05-28 amendment 留下的盲区：

- ADR §轴A 残留 R3 受 file-extension scope 描述（与 §轴B 双真理源；F1）
- §Implementation matrix 漏列 `TestPGRepoApprovedSealed`（F4）
- ADR `202605101200` §4.5.1 fixture oracle 描述仍是旧 exact-set 形态（F5）
- `assertSealedInterface` 只验「marker 在场 + sanctioned impl」，未验「ONLY sanctioned impl」——包内 sibling impl 可漂入（F3）
- `Approve(reason string)` 仅 kebab format check；reviewer 援 K8s ValidatingAdmissionPolicy 要求把 reason 升到典型审批 catalog（F2）
- accesscore `role_repo_integration_test.go` 7 callsite 复用单一 `"integration-test-direct-write"` reason，per-callsite 语义退化（F6）
- ADR §轴B 承诺「审批无调用 = 不可表达」与实际 gap：`_ = pgrepoapproved.Approve(<reason>)` 是 expression 可 dead-code 化，archtest 未守反向（F7）
- accesscore `pgexec.ExecDirect` production 0 callsite 但仍在 production API 面（F8）

**Decision**：

1. **typed ApprovalReason catalog（F2/F6）**：把 `Approve(reason string)` 升级到 `Approve(reason ApprovalReason) Approval`，`ApprovalReason string` newtype + 4 个 catalog 常量（`RevokeSessionCascade` / `IntegrationTestDeleteRoleAssignment` / `IntegrationTestLockUser` / `IntegrationTestDeleteUser`）集中声明在 `pkg/pgrepoapproved`。archtest R3 gate #3-5 改为「arg 必须解析到 `*types.Const`，Pkg path == pgrepoapproved AND 类型为 ApprovalReason」（替换旧 BasicLit + kebab regex + placeholder check）。callsite 全部改为 typed const 引用——integration test 7 callsite 按 SQL 操作分配到 3 个 typed const（per-callsite 语义恢复）。这是 ai-robust.md §Hard 范本 #2（typed marker funnel）+ #3（string-typed concept funnel）的组合升级；sibling `panicregister.Approved` 暂保持范本 #2 形态（catalog 升级跨 funnel scope，由独立 backlog 跟踪）。
2. **R4 orphan Approve 反向规则（F7）**：新增 `scanR4OrphanApprove`，walk-with-parent 扫所有 `pgrepoapproved.Approve` callsite，断言其 syntactic parent 是 `*ast.CallExpr` AND `parent.Args[0]` 是该 call AND parent callee resolves to `pgexec.ExecDirect`。orphan 形态（`_ = Approve(...)`、`a := Approve(...); ... (a 不传给 ExecDirect)`、传给其他函数）全部 flag。R4 与 R3 形成双向闭环：R3 = 每个 ExecDirect 都经过 Approve；R4 = 每个 Approve 都流入 ExecDirect。ADR §轴B 承诺「审批无调用 = 不可表达」现由 R4 静态守住。
3. **assertSealedInterface 唯一 impl 检查（F3）**：扩展 helper 扫 `pkg.Scope()` 所有 named types，对每个实现 sealed iface 的 named type（且非 interface 自身、非 alias）断言 name == sanctioned impl name。覆盖 Go visibility 拦不住的 in-package sibling impl 漂入路径；同一 helper 兼容 `TestPGRepoApprovedSealed` (approval) 与 `TestPGRepoAmbientTx_InterfaceSealed` (pgExecutor) 双场景。
4. **accesscore ExecDirect build-tag 隔离（F8）**：`cells/accesscore/internal/adapters/postgres/internal/pgexec/pgexec.go` 删除 `ExecDirect` 函数 + 相关 import（panicregister / errcode / pgrepoapproved）；新建 `exec_direct_integration.go` 带 `//go:build integration`，含 `ExecDirect` 完整实现（含 A-class panic 兜底）。对应 unit test (`TestExecDirect_PanicsOnNonSealedExecutor`) 挪到 `exec_direct_integration_test.go` 同 build-tag。默认 build 不编译 ExecDirect — production API 面零暴露；integration build 仍有完整功能。这与 saga / devicecell `pgexec` 包 `ExecDirect` 一律不暴露形成阶梯：「production 用 → 暴露；test 用 → build-tag；从不用 → 不声明」。
5. **ADR 文档同步（F1/F4/F5）**：§轴A 重写 R3 不再受 file-extension scope；§Implementation matrix conformance test 列表补 `TestPGRepoApprovedSealed`，repro -run regex 同步；ADR `202605101200` line 379 fixture oracle 描述改为 "exact multiset assertion on (filename, rulePrefix, line)"（反映 2026-05-28 已落地形态）。

**Implementation summary**：

- `pkg/pgrepoapproved/pgrepoapproved.go`：加 `ApprovalReason string` + 4 catalog const；`Approve` 签名 `(ApprovalReason)`；godoc 描述「新增 reason 流程」。
- `adapters/postgres/refresh_store.go`：callsite 改 `pgrepoapproved.Approve(pgrepoapproved.RevokeSessionCascade)`。
- `cells/accesscore/internal/adapters/postgres/role_repo_integration_test.go`：7 callsite → 3 typed const（DELETE role_assignments / UPDATE users lock / DELETE users）。
- `adapters/postgres/internal/pgexec/pgexec_test.go` + `cells/accesscore/internal/adapters/postgres/internal/pgexec/pgexec_test.go`：mock-panic test 内的 Approve 调用改用 catalog const。
- `cells/accesscore/internal/adapters/postgres/internal/pgexec/`：拆出 `exec_direct_integration.go` + `exec_direct_integration_test.go`（`//go:build integration`），`pgexec.go` 删 ExecDirect + 关联 import；`pgexec_test.go` 同步 import 清理。
- `tools/archtest/pg_repo_ambient_tx_test.go`：R3 gate 改 typed-const lookup（删 kebab regex / placeholder regex / strconv.Unquote / regexp import）；新增 `scanR4OrphanApprove` + `orphanApproveWalker` + `approveIsBoundToExecDirect` helper；rulePrefix 解析器加 R4；`assertSealedInterface` 加唯一 impl 扫描；`expectedFixtureViolations` 全集更新。
- `tools/archtest/internal/pgrepoambienttxfixture/fixture_repo.go`：R3 RED cases 重组——`badR3LocalConst`（本地声明 ApprovalReason const）+ `badR3TypeConversion`（`ApprovalReason("...")` 类型转换）+ `badR3ReusedApproval`（变量复用）；新增 R4 RED `badR4OrphanDiscarded` / `badR4OrphanAssigned`。
- `tools/archtest/internal/pgrepoambienttxfixture/fixture_service.go`：reused-approval callsite 用 catalog const（仍触发 R3+R4）。

**Compensation / Threat Model 重评**：

| 威胁形态 | 防线 | 状态 |
|---------|------|------|
| ExecDirect callsite 漏传 approval | `Approval` 类型首参 → 编译错误 | ✓（不变） |
| ExecDirect 传 approval 但 reason 非 catalog（本地 const / 类型转换 / 字面量） | R3 typed-const lookup → flag | ✓（升级） |
| 包外自实现 `PGExecutor` 持 raw pool | `sealPGExecutor` unexported marker + `assertSealedInterface` 唯一 impl 检查 | ✓（F3 升级） |
| 包内漂入 sibling impl 持 raw pool | `assertSealedInterface` 唯一 impl 检查 | ✓（F3 新增） |
| orphan Approve dead code（`_ = Approve(...)` / 变量赋值不传 ExecDirect） | R4 反向规则 → flag | ✓（F7 新增） |
| `panicregister.Approved` 同款 orphan 盲区 | 暂留 — sibling funnel 升级跨 PR scope | ⚠️（backlog 跟踪） |
| accesscore production ExecDirect 调用 | 不存在；ExecDirect 仅 integration build 编译 | ✓（F8 升级） |
| sibling typed marker funnel（panicregister）catalog 升级 | 暂留 — 跨 codegen 模板 + 100+ callsite scope | ⚠️（backlog 跟踪） |

无 ✅→⚠️/❌ 回退；新增 2 行 ⚠️ 由 backlog issue 跟踪。

**Out-of-scope（不变）**：

- R1/R2 file-extension Soft scope 移除（#1206，需 seal 4 种 holder）。
- panicregister catalog 升级 + 反向规则（独立 backlog issue，跨 contractgen 模板 + 100+ callsite）。

## References

- `.claude/rules/gocell/ai-robust.md` §"Hard 范本目录" (#2 typed marker funnel for unbounded ops, #4 single sanctioned holder, #6 sealed construction)
- `.claude/rules/gocell/ai-robust.md` §"Funnel 双向锁评级"
- `.claude/rules/gocell/ai-robust.md` §"ADR amendment 落地必查"
- `pkg/panicregister`（sibling typed marker funnel deployment）
- ADR `docs/architecture/202605051800-adr-refresh-store-ambient-tx-and-idle-grace.md`（revokeSessionDetachedAt 的 ExecDirect bypass 起源）
- ADR `docs/architecture/202605231400-002-required-dep-nil-guard-codegen-funnel.md`（Soft → Hard 升级形态参照）
- ADR `docs/architecture/202605101200-adr-typed-go-heavy-protocol-primitives.md` §4.5.1（cross-ref，跟踪 PG-REPO-AMBIENT-TX-01 演化）
- gh #738 / #916（本 amendment closes）
