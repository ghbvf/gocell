# ADR 003 — PG-REPO-AMBIENT-TX-01 双轴 Hard 化（type-aware discovery + typed marker）

## Status

Accepted (2026-05-24)

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
精确集合（diff-friendly 失败模式）。

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

**archtest 形态**：R3(b) 删除 `r3ExecDirectAllowlist` map，改为同 body marker
presence 检查 — `bodyHasApprovedExecDirectMarker(body, info)`。Marker 形态
通过 (callee, arg) form-uniqueness 验证：

1. callee 解析（via `*types.Info.Uses`）到 `*types.Func` 满足
   `Name() == "ApprovedExecDirect"` AND `Pkg().Path() == "github.com/ghbvf/gocell/pkg/pgrepoapproved"`
2. 第 1 参数的 `*types.Info.Types` TypeAndValue.Value 是
   `constant.String`（const literal） — `fmt.Sprintf` / 字符串拼接 / 变量
   均产生 non-constant 而被拒绝

理由：与 `PANIC-REGISTERED-01` / `panic(panicregister.Approved)` 是相同 Hard
范本 — "typed marker funnel for unbounded ops"。允许 = "存在 marker"，
形态唯一性来自 callee 名 + 包路径 + arg 类型，无 "像但不是"的灰区。

## Funnel 双向锁评级表

| 维度 | 升级前 | 升级后 | 形态 |
|------|--------|--------|------|
| 上游（package discovery） | Soft（hand-maintained list） | **Medium** | archtest-bound type-aware：`Scope().Lookup("pgExecutor")` |
| 下游 R1/R2（pool field / wrap funnel） | Hard | Hard | `*types.Info` 解析（不变） |
| 下游 R3(a)（pool field access） | Hard | Hard | `*types.Info` 解析（不变） |
| 下游 R3(b)（ExecDirect callsite） | Soft（hand-maintained map） | **Hard** | typed marker funnel：(callee, arg) form-uniqueness |

**上游为何不是 Hard**：同 PANIC-REGISTERED-01 / SPAN-SETATTR-REDACT-01 上游
package-internal 形态评级 — `pgExecutor` 当前是 package-private struct（包内
sibling 可见），Go 编译器无法在包内阻止"非 pgExecutor struct 持 *pgxpool.Pool
字段"。Hard terminal state 需 seal `pgExecutor` behind exported interface +
私有构造，使包外不可表达跳过；工作量大（4 个 pgExecutor 实现 + 调用方 + R3
type-resolution 逻辑改造），不在本 PR 范围。

**跟踪 Hard terminal state**：同 PR 开 gh issue #916 (`PGEXECUTOR-SEAL-INTERFACE-01`)
（依 ai-robust.md §"Funnel 双向锁评级"过渡形态要求），archtest godoc
点名该 issue 号，让审查者能直接追到升级路径。

## Out of scope

- **`pgExecutor` sealed interface 改造**：见上文 Hard terminal state，gh
  issue 跟踪。
- **configcore `Session`/`DBTX` funnel formalization**：configcore 用结构性
  不同的另一种 ambient-tx funnel（local `DBTX` interface + `*Session` 包装）。
  其 `Session` struct 持有 `*pgxpool.Pool`，与 `pgExecutor` 角色对等但命名
  不同。若需等价治理，需独立 ADR + 独立 archtest（命名建议
  `PG-REPO-AMBIENT-TX-02`），不在 #823 ask 范围。本 PR 只 commit 到"该包不
  在 PG-REPO-AMBIENT-TX-01 范围"这个事实，不强制迁移。

## Threat Model

| 威胁 | 缓解 |
|------|------|
| 新 PG 适配包沿用 `pgExecutor` 范式 | 自动被 `discoverPGAdapterPackages` 发现，R1/R2/R3 自动覆盖 ✓ |
| 新 PG 适配包发明新 funnel（如 configcore Session） | 本 archtest 不覆盖；约定 + code-review + new-pattern-requires-ADR 兜底（不是回归 — 升级前也不覆盖） |
| Refactor 把 pgExecutor 改名 / 移出包 scope | `TestPGRepoAmbientTx_DiscoveryCoverage` exact-set match 失败 + `TestPGRepoAmbientTx` 的 coverage floor (≥3) 兜底 ✓ |
| 新 ExecDirect callsite 漏加 marker | R3(b) 同 body marker presence 检查失败 ✓ |
| Marker reason 写成 `fmt.Sprintf` 隐藏审计串 | `bodyHasApprovedExecDirectMarker` 验证 `constant.String` 拒绝 non-constant arg ✓ |
| 移到不相关 func body 放 marker 制造"看似 approved"的错觉 | BS-8 反向自检：marker 必须与同 body 的 `pgExecutor.ExecDirect` 调用共存 — 孤立 marker 失败 ✓ |
| 拷贝 `pgrepoapproved` 包到其他位置绕过 funnel | callee 解析锁 `Pkg().Path()` 到精确字符串 `"github.com/ghbvf/gocell/pkg/pgrepoapproved"`，重定向 import 不改 package path ✓ |

## Implementation matrix

```
Contract: PG-REPO-AMBIENT-TX-01 archtest（上游 Soft → Medium；下游 R3(b) Soft → Hard）
Change: 引入 pkg/pgrepoapproved typed marker + 类型感知 discovery；删除 hand-maintained list + allowlist map
Implementations: [x] adapters/postgres (refresh_store.go marker)  [x] cells/accesscore/internal/adapters/postgres（自动发现）  [x] examples/iotdevice/cells/devicecell/internal/adapters/postgres（自动发现）
Conformance test: tools/archtest.TestPGRepoAmbientTx + TestPGRepoAmbientTx_RedFixtureDetected + TestPGRepoAmbientTx_SelfCheck + TestPGRepoAmbientTx_DiscoveryCoverage
Repro: go test ./tools/archtest/... -run TestPGRepoAmbientTx
Dependent contracts (governance scan): none
```

## References

- `.claude/rules/gocell/ai-robust.md` §"Hard 范本目录" (#2 typed marker funnel for unbounded ops, #4 single sanctioned holder)
- `.claude/rules/gocell/ai-robust.md` §"Funnel 双向锁评级"
- `pkg/panicregister`（sibling typed marker funnel deployment）
- ADR `docs/architecture/202605051800-adr-refresh-store-ambient-tx-and-idle-grace.md`（revokeSessionDetachedAt 的 ExecDirect bypass 起源）
- ADR `docs/architecture/202605231400-002-required-dep-nil-guard-codegen-funnel.md`（最近一次 Soft → Hard 升级，同期参照）
