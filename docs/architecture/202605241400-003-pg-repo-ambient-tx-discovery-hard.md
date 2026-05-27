# ADR 003 — PG-REPO-AMBIENT-TX-01 双轴 Hard 化（type-aware discovery + typed marker）

## Status

Accepted (2026-05-24); **Amended 2026-05-27** — upstream Medium → Hard via
sealed `internal/pgexec/` sub-package + cross-package wrap funnel. Closes
gh #738 / #916. See §Amendment 2026-05-27 below.

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

双轴升级：

### 轴 A — 上游 type-aware 自动发现（Soft → Medium）

**发现信号**：production package 在 archtest 扫描范围内 ⇔ 该包 scope 内
声明了名为 `pgExecutor` 的类型。

实现：`discoverPGAdapterPackages(t) []string` 用 `RunTyped` 遍历
`prodscan.Patterns(root)` 加载的所有生产包，过滤
`p.Pkg.Scope().Lookup(pgExecutorName) != nil` 的包返回其 import path 集合。
`TestPGRepoAmbientTx` 用该集合代替原人工维护列表。

理由：

- 当前 3 个在册包都声明 `pgExecutor` 作为包局部 funnel；这本就是 R1/R2/R3
  依赖的结构性不变量，把它升级为发现信号 = 让"funnel 入口"与"扫描范围"
  共用同一类型符号，从根上消除"列表漏同步"失败模式。
- 不声明 `pgExecutor` 的包无 funnel 符号可拦截，扫描无意义。
- configcore (`Session` + `DBTX` 另一套 funnel) 被信号正确排除 — 这是另一种
  funnel 模式，需独立 ADR + 独立 archtest，不在 PG-REPO-AMBIENT-TX-01 范围。
- 新作者要么沿用 `pgExecutor` 范式（自动发现，R1/R2/R3 自动覆盖），要么发明新
  funnel（需 ADR + 新 archtest）。

**Coverage floor**：`TestPGRepoAmbientTx` 在调用 rule 前断言
`len(discovered) ≥ 3`，防 discovery signal 被破坏的静默回归。
`TestPGRepoAmbientTx_DiscoveryCoverage` 进一步断言发现集 == 当前 3 个已知包
精确集合（diff-friendly 失败模式）。`expectedPGAdapterPackageMin` 是固定下限（不随新包自动增大）。`TestPGRepoAmbientTx_DiscoveryCoverage` 的 exact-set match 才是主保护；floor 仅防 `prodscan.Patterns` 被破坏导致 discovery 静默返回空。新增 PG 包时需手动同时更新 `expected` slice 和 `expectedPGAdapterPackageMin`。

### 轴 B — 下游 R3 allowlist typed marker（Soft → Hard）

**marker package**：新建 `pkg/pgrepoapproved`（sibling deployment of
`pkg/panicregister`），含单一 no-op marker 函数：

```go
package pgrepoapproved

func ApprovedExecDirect(reason string) {
    _ = reason
}
```

**callsite 形态**：合法 ExecDirect 调用必须在自身 FuncDecl body 内有 sibling
marker 调用：

```go
func (s *PGRefreshStore) revokeSessionDetachedAt(...) error {
    pgrepoapproved.ApprovedExecDirect("revoke-session-cascade")
    // ...
    _, err := s.db.ExecDirect(cascadeCtx, revokeSessionSQL, revokedAt, sessionID)
    return err
}
```

**archtest 形态**：R3(b) 删除 `r3ExecDirectAllowlist` map，改为 same-scope
marker presence 检查 — `bodyHasApprovedExecDirectMarker(body, info)` 用
`inspectStopAtFuncLit` 把扫描限定到当前 FuncDecl/FuncLit body（不下降到
nested closure）。`scanR3UsagePoints` 在 FuncDecl 之外递归进入每个 nested
FuncLit body，treat each as its own approval scope。Marker 5-门 form-uniqueness
（每门一个 reject 分支，对标 panicregister 6-rule chain）：

1. callee 解析（via `*types.Info.Uses`）到 `*types.Func` 满足
   `Name() == "ApprovedExecDirect"` AND
   `Pkg().Path() == "github.com/ghbvf/gocell/pkg/pgrepoapproved"`
2. `arg[0]` 必须是 `*ast.BasicLit` AND `Kind == token.STRING`（拒 const
   identifier / 常量折叠 `"a" + "b"` BinaryExpr / 变量 / `fmt.Sprintf`）
3. `strconv.Unquote(arg[0])` 必须 match `^[a-z][a-z0-9-]+$`（kebab-case，
   长度 ≥ 2，首字符小写字母）
4. 不在占位符集 `^(todo|fixme|tbd|xxx|placeholder|wip)(-|$)`
5. scope 必须是当前 FuncDecl/FuncLit body — 通过 `inspectStopAtFuncLit` 强制

理由：与 `PANIC-REGISTERED-01` 是相同 Hard 范本 — "typed marker funnel for
unbounded ops"。允许 = "存在严格形态的 marker 在同 scope"，5-门串行消除"像但
不是 const literal"灰区，scope-bounded 消除 nested-closure 走私 — 任何其他
形态 archtest 即失败。BS-8 反向自检也升级为 scope-bounded，覆盖 spurious
marker（marker 存在但同 scope 无 ExecDirect → audit-trail 退化）。

## Funnel 双向锁评级表（amended 2026-05-27）

| 维度 | 原始（2026-05-24） | Amended（2026-05-27） | 形态 |
|------|--------|--------|------|
| 上游（package discovery） | Medium（archtest type-aware：`Scope().Lookup("pgExecutor")`） | **Hard** | sealed `internal/pgexec/` sub-package：`pgExecutor` 结构体 + `*pgxpool.Pool` 字段在不可导出符号下；parent-pkg 包外无法引用类型，无法 type-assert，无 .pool 访问路径。Discovery 已删除（rules 改为全局谓词，按 `*_repo.go` / `*_store.go` 文件扩展名过滤层边界）|
| 下游 R1（pool field in repo/store layer） | Hard（`*types.Info` 字段类型解析） | Hard | `*types.Info` 字段类型解析（不变）+ 全局 predicate（删除 hand-maintained list） |
| 下游 R2（wrap funnel `New*` → `pgexec.New`） | Hard | Hard | `*types.Info` callee 解析至 `*types.Func`，要求 `Pkg().Path()` 以 `/internal/pgexec` 结尾 AND `Name() == "New"` |
| 下游 R3(a)（pool field access） | Hard（archtest-bound） | **retired** | compile-time impossible — `pgexec.PGExecutor` interface 不暴露 `.pool` 字段；parent-pkg `.pool` 访问编译错误 |
| 下游 R3(b)（ExecDirect callsite） | Hard（typed marker funnel） | Hard | typed marker funnel（不变）；receiver 类型解析从 same-pkg `pgExecutor` struct 改为 cross-pkg `pgexec.PGExecutor` interface |

**Amendment compensation column**（per ai-robust.md §"ADR amendment 落地必查"）：

- 上游 Medium → Hard：升级（无 ✅→⚠️ 回退），不需要补偿措施。
- R3(a) retired：升级到 compile-time（不可表达性优于 archtest form-uniqueness）；BS-7（pool 局部赋值反向自检）一并删除——若 sub-pkg 内部新增 sibling 文件触发 .pool 局部赋值，单一 sub-pkg 文件内部小范围，code review 兜底，不再单独 archtest。

**上游 Hard 实现机制**：
- `internal/pgexec/pgexec.go` 是包外不可 import 的位置（Go `internal/` 可见性规则）。
- `pgExecutor` struct 与 `*pgxpool.Pool` 字段在 sub-pkg 内 unexported；parent-pkg 包外无法引用类型名进行字段声明、类型断言、composite literal 构造。
- 唯一获取 `pgexec.PGExecutor` 实例的路径是 `pgexec.New(*pgxpool.Pool)` 工厂；其返回的 interface 类型不暴露 `.pool` 字段。
- 4 个 PG adapter package（adapters/postgres、adapters/postgres/saga、cells/accesscore/internal/adapters/postgres、examples/iotdevice/cells/devicecell/internal/adapters/postgres）各自有一份镜像 `internal/pgexec/` 子包（CLAUDE.md 分层规则 `cells/ ❌ adapters/` 阻止单一共享位置）。
- archtest R1/R2 retained as defense-in-depth：global predicate over production module，scoped by `*_repo.go` / `*_store.go` file extension（基础设施层 `pool.go`/`tx_manager.go` 与 composition root pass-through helpers 通过文件后缀过滤排除）。

**与同形态 funnel 的对照**：HEALTH-REDACTED-ERROR-MSG-FUNNEL-01 的 `SlogDependencyEntry` unexported fields 是同款"sealed construction" Hard 形态（ai-robust.md §Hard 范本 #6 "sealed construction"）。

## Out of scope

- **configcore `Session`/`DBTX` funnel formalization**：configcore 用结构性
  不同的另一种 ambient-tx funnel（local `DBTX` interface + `*Session` 包装）。
  其 `Session` struct 持有 `*pgxpool.Pool`，与 `pgExecutor` 角色对等但命名
  不同。若需等价治理，需独立 ADR + 独立 archtest（命名建议
  `PG-REPO-AMBIENT-TX-02`），不在 #823 ask 范围。本 PR 只 commit 到"该包不
  在 PG-REPO-AMBIENT-TX-01 范围"这个事实，不强制迁移。**重要：configcore 目前无等价 archtest 治理**；其 `Session`/`DBTX` 的 ambient-tx 安全边界依赖 code review。需要 archtest 覆盖时新开 gh issue 跟踪 `PG-REPO-AMBIENT-TX-02`。

## Threat Model

| 威胁 | 缓解 |
|------|------|
| 新 PG 适配包沿用 `pgExecutor` 范式 | R1 / R2 全局谓词（_repo.go / _store.go scope）自动覆盖；新 sub-pkg 通过路径后缀 /internal/pgexec 自动入 funnel ✓ |
| 新 PG 适配包发明新 funnel（如 configcore Session） | 本 archtest 不覆盖；约定 + code-review + new-pattern-requires-ADR 兜底（不是回归 — 升级前也不覆盖） |
| 新 ExecDirect callsite 漏加 marker | R3(b) 同 body marker presence 检查失败 ✓ |
| Marker reason 写成 `fmt.Sprintf` / 变量 / 函数返回值隐藏审计串 | `bodyHasApprovedExecDirectMarker` 要求 arg[0] 为 `*ast.BasicLit + token.STRING`；non-literal 表达式 AST 节点不是 BasicLit → 拒绝 ✓（与"const ident / 常量折叠"威胁同源，见下文专行）|
| 移到不相关 func body 放 marker 制造"看似 approved"的错觉 | BS-8 反向自检：marker 必须与同 body 的 `pgexec.PGExecutor.ExecDirect` 调用共存 — 孤立 marker 失败 ✓ |
| 拷贝 `pgrepoapproved` 包到其他位置绕过 funnel | callee 解析锁 `Pkg().Path()` 到精确字符串 `"github.com/ghbvf/gocell/pkg/pgrepoapproved"`，重定向 import 不改 package path ✓ |
| import 别名绕过 marker callee 识别 | callee 解析锁 `fn.Pkg().Path()`（via *types.Info.Uses），import alias 不改 package path ✓ |
| build-tag 隔离的条件性 ExecDirect 调用 | RunTyped 用 default build context；如需覆盖须扩展 TypedOpts；当前 production 代码库无 build-tag 隔离 ExecDirect 先例；以 code review 兜底（accepted threat） |
| ExecDirect 仅 adapters/postgres 当前持有 | accesscore 的 pgexec.PGExecutor 包含 ExecDirect（当前无 production callsite）；devicecell 的省略（compile-blocks future bypass）；saga 的省略（使用 AcquireTx 进行 tx ownership） ✓ |
| nested closure 走私 marker / ExecDirect（marker 在内层 closure 批准外层 ExecDirect，或反向） | `bodyHasApprovedExecDirectMarker` / `scanR3ExecDirect` 用 `inspectStopAtFuncLit` 把扫描限定到当前 FuncDecl/FuncLit body；`scanR3UsagePoints` 把每个 nested FuncLit body 视为独立 approval scope；RED fixture `badR3MarkerInNestedClosure` + `badR3MarkerOuterExecInNestedClosure` 守 ✓ |
| marker reason 退化（const ident / `"a"+"b"` concat / 空串 / 占位符 todo/fixme/…） | arg[0] 必须 `*ast.BasicLit + token.STRING` + kebab regex + placeholder ban 5-门串行（落地对标 panicregister）；RED fixture `badR3ApprovedConstIdent` / `badR3ApprovedConcat` / `badR3ApprovedEmpty` / `badR3ApprovedPlaceholder` 4 个用例守 ✓ |

## Implementation matrix

```
Contract: PG-REPO-AMBIENT-TX-01 archtest（上游 Medium → Hard via sealed internal/pgexec sub-package; closes gh #738 / #916）
Change: 把 pgExecutor 移到 <adapter-pkg>/internal/pgexec sub-package；parent-pkg 持有 pgexec.PGExecutor interface；archtest 删除 discovery，R1/R2/R3 改为全局谓词
Implementations: [x] adapters/postgres/internal/pgexec  [x] adapters/postgres/saga/internal/pgexec  [x] cells/accesscore/internal/adapters/postgres/internal/pgexec  [x] examples/iotdevice/cells/devicecell/internal/adapters/postgres/internal/pgexec
Conformance test: tools/archtest.TestPGRepoAmbientTx + TestPGRepoAmbientTx_RedFixtureDetected + TestPGRepoAmbientTx_SelfCheck
Repro: go -C worktrees/522-pgexec-seal-hard test ./tools/archtest/ -run 'TestPGRepoAmbientTx'
Dependent contracts (governance scan): none
```

## Amendment 2026-05-27 — upstream Medium → Hard via sealed sub-package

**触发**：gh #738 (`PG-REPO-AMBIENT-TX-UPSTREAM-HARD-01`，触发型 backlog) +
gh #916 (`PGEXECUTOR-SEAL-INTERFACE-01`)。Trigger (b)（"新增 cell adapter
package 需要同等保护"）已结构性触发——4 个 PG adapter packages 各自镜像
pgExecutor funnel。

**Decision**：把每个 PG adapter package 内的 `pgExecutor` struct + `newPGExecutor`
factory 整体迁移到 `<adapter-pkg>/internal/pgexec/` sub-package。Sub-package
导出 `PGExecutor` interface（Exec/Query/QueryRow/ExecDirect，saga 多 AcquireTx）
+ exported `New(*pgxpool.Pool) PGExecutor` 工厂。parent-pkg repos/stores 持有
`pgexec.PGExecutor`（interface 字段），构造点调 `pgexec.New(pool)`。

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

## References

- `.claude/rules/gocell/ai-robust.md` §"Hard 范本目录" (#2 typed marker funnel for unbounded ops, #4 single sanctioned holder, #6 sealed construction)
- `.claude/rules/gocell/ai-robust.md` §"Funnel 双向锁评级"
- `.claude/rules/gocell/ai-robust.md` §"ADR amendment 落地必查"
- `pkg/panicregister`（sibling typed marker funnel deployment）
- ADR `docs/architecture/202605051800-adr-refresh-store-ambient-tx-and-idle-grace.md`（revokeSessionDetachedAt 的 ExecDirect bypass 起源）
- ADR `docs/architecture/202605231400-002-required-dep-nil-guard-codegen-funnel.md`（Soft → Hard 升级形态参照）
- ADR `docs/architecture/202605101200-adr-typed-go-heavy-protocol-primitives.md` §4.5.1（cross-ref，跟踪 PG-REPO-AMBIENT-TX-01 演化）
- gh #738 / #916（本 amendment closes）
