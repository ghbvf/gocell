# archtest 入口合并方案：Pass-Driver 范式 + 零二次返工迁移

**最后更新**：2026-05-15（阶段 1.5 框架完备化落地；阶段 2 PR-3 已 shipped via PR #493）

## 进度状态

| 阶段 | PR 数 | 状态 | 备注 |
|------|------|------|------|
| 1 — Pass 框架 + 三重 Hard 防线 | 1 | ✅ PR #492 (2026-05-14) | 业务文件 0 改动；review 三轮 in-PR 收口 |
| **1.5 — Pass 框架完备化 + 单路径 enforcement** | 1 | ✅ PR #495 (2026-05-15) | Stage 2/3/dual 全后续 PR 零框架返工前置；见下方摘要 |
| 2 — A 类 EachFile 主题分批迁移 | 4 | 🔄 PR-3 ✅ PR #493 (contract/codegen)；余 PR-2/4/5 待起 | 与阶段 3 并行；前置 = 阶段 1.5 |
| 3 — E 类 for-range 主题分批迁移 | 5 | ⏳ 未启动 | 与阶段 2 并行；前置 = 阶段 1.5 |
| 4 — 收尾（删 allowlist + scanner/typeseval 深 internal 化） | 1 | ⏳ 等阶段 2+3 全部 ship | — |

**阶段 1.5 ship 摘要（本 PR，2026-05-15）**：

> **根因**：PR #492 定型 `Pass`/`Run`/`RunTyped`/façade 时未基于全部存量 archtest（24 A-class + 32 E-class + 6 dual-class）真实取数需求完整盘点就定型 API。只补表面 gap 是 L1 补丁思维（PR #493 contract/codegen 迁移已被迫在 `codegen_invariants`/`listener_dx` 内手写 `parser.ParseFile(ParseComments)` 绕过，即将产生二次返工）。本阶段一次定型完备端态 + 同 PR 封死旧路径。

- `archtest.Run`（AST 路径）`collectASTFiles` parse mode 增 `parser.ParseComments`；typed 路径**已带注释**（go/packages 默认 ParseFile = `parser.AllErrors|parser.ParseComments`，**不改 typeseval**，仅加 `TestRunTyped_CommentsRegressionLock` 回归锁定）
- `Pass` 加 `Abs func(*ast.File) string`（与 `Rel` 同源 `fset.Position`，零新状态）+ `(*Pass).IsFileInScope` / `IsGenerated` 方法（收 typeseval build-constraint helper，对齐 plan 轴 4）
- 新建 `tools/archtest/resolve.go`：`type ImportBan = scanner.ImportBan` + `ResolvePackageRef`/`ResolveMethodCall`/`EvaluateConstString`/`FlatNonDefaultTags`/`KnownNonDefaultTags` 薄委托；**6 个 loader 符号故意不重导出**——经 `RunTyped` 唯一可达（funnel Hard 防线本体）
- 新增元治理 `PASS-FUNNEL-RESOLVE-01`（type-aware via `*types.Info`，复用 `diagsLoadPackages` 符号集机制，`// AI-rebust: Medium` + 盲区清单 + reverse self-check）：ban 业务 archtest 直引 8 个 typeseval helper + `scanner.ImportBan`；豁免存量（LegacyAllowlist +2 → `build_constraint_test.go` / `ci_integration_discovery_invariants_test.go`），Stage 4 清零。**勘误（PR #495 后修）**：`RESOLVE-01` 初始实现的 `resolveBarePkgSymbol` 仅处理 `*types.Func`，dot-import `ImportBan{}` 的 bare-Ident 被 `*types.TypeName` 而非 `*types.Func` resolve，导致 `ResolvePackageRef` 对该形态返回 `("","",false)`；`TestPassFunnel_FixtureCoverage` 因 qualified+alias 两路已产生诊断而误报通过。PR #495 在 `resolveBarePkgSymbol` 加 `*types.TypeName` 分支修复，并将 ImportBan 断言从 `≥1` 升为精确计数 `==3`（qualified+alias+dot-import），确保单形态回归即失败。
- `TestFacadeDoesNotLeakLoaders`（防线 #1 Hard 反向盲区自检）：静态断言 façade 零 loader / 零 `*packages.Package` 暴露
- 端态不变式：此后 24 A-class + 32 E-class + 6 dual-class 迁移**只需** import `tools/archtest`，零 `internal/*`/`x/tools/go/packages` 直引，零框架返工
- 验证：`pass_test.go` +10 TDD（RED→GREEN）；verify-archtest.sh PASS（16 shard / 372 test）；golangci-lint 0 issues；build incl `-tags=integration` 绿

**勘误（基于 develop@2fd2976e 复核）**：dual-class 实测 **6** 文件（auth_bootstrap / errcode_invariants / outbox_invariants / panic_invariants / production_loader_funnel / role_admin_literal），非原预估 ≤3，归后续单一 PR 整体迁移；LegacyAllowlist 当前 **47** 条目（PR #493 删 10 个已迁移 contract/codegen 文件后）。

**勘误 — RESOLVE-01 façade 出口完备性（PR #495 补修）**：Stage 1.5 未对 RESOLVE-01 被禁 8 个 symbol 的 façade 出口作穷举验证，导致两处 gap：`ParseBuildConstraint` — `build_constraint_test.go` / `ci_integration_discovery_invariants_test.go` 两个文件调用时需获取原始 `constraint.Expr` 做三路求值，`Pass.IsFileInScope` 只返回单 bool、无法覆盖；`IsGeneratedRelPath` — `outbox_invariants_test.go::TestOutboxHandleResultFactoryPreferred_GeneratedLoadAnchor_Wave3` 传入 raw string rel，`Pass.IsGenerated(f *ast.File)` 无法覆盖。PR #495 在 `resolve.go` 补 `func ParseBuildConstraint(filePath string) (constraint.Expr, error)` 和 `func IsGeneratedRelPath(rel string) bool` 两个薄委托自由函数，两符号继续留在 RESOLVE-01 禁止映射（ban 的是 `typeseval.` 直调，业务侧改用 `archtest.` 门面）。当前不变式：**RESOLVE-01 每个被禁符号均有语义充分的门面出口，覆盖所有已知调用形态**。

**后续迁移强制约定**：Stage 2/3 迁移后 Rule 必须返回 `[]Diagnostic` + `archtest.Report`（对标 go/analysis 端态，禁止保留 inline `t.Errorf` 形态，确保每文件一次到位 0 二次返工）。

**阶段 1 PR-1 ship 摘要（PR #492，2026-05-14）**：

- 新建 `tools/archtest/{pass,walk,scope,content,module_root}.go`（façade 重导出 + Run/RunTyped driver；`Pass.Pkg *types.Package`，**不暴露** `.Syntax`）
- 新建 `tools/archtest/internal/archtestmeta/legacy_allowlist.go`（53 文件 + `FixtureBuildTag` const）
- 新建 `tools/archtest/internal/passfunnelfixture/redfixture.go`（build-tag 隔离的 red fixture，覆盖 qualified-import / alias-import / dot-import 三路 + var-reference 形态）
- 新建元治理 archtest `tools/archtest/pass_funnel_test.go`：5 条规则 (a) `PASS-FUNNEL-EACHFILE-01` / (b) `LOADPACKAGES-01` / (c) `PACKAGES-IMPORT-01` / (d) `FIXTURE-COVERAGE` 反向自检 / (e) `GUARD-LIST-SYNC`（解析 `.golangci.yml` 与 Go LegacyAllowlist 三向同步断言）
- 新建 `tools/archtest/pass_test.go`：11 个单元测试覆盖 Run / RunTyped / buildTypedPass / newPackageRel / isPackageWithTestFiles，archtest 生产代码覆盖率 ~12% → ~71%
- `.golangci.yml` 增 `archtest-no-direct-packages-load` depguard rule，仅 deny `golang.org/x/tools/go/packages`（符号级 scanner/typeseval 由元治理 archtest 拦截，避免 38 个仅用 walk helper 的合法文件误报）
- 新建 ADR `docs/architecture/202605141519-adr-archtest-pass-funnel.md`：业界对标 + 三重 Hard 防线 + 迁移路径

**Round-1 review follow-up（F1-F5，PR #492 round-1 in-PR）**：
- F1：depguard 防线 #2 收窄至仅 `packages` 包（scanner/typeseval 符号级由防线 #3 元治理 archtest 通过 `*types.Info` resolve 精确拦截），消除 38 文件误报
- F2：新增 `TestPassFunnelGuardListSync` 解析 `.golangci.yml` 在测试时断言三向不变式（`yaml-exempt ⊆ LegacyAllowlist ∪ {self}` / `packages-importers ⊆ yaml-exempt` / `yaml-exempt ∖ {self} ⊆ packages-importers`），消除阶段 2/3 迁移漂移盲点
- F3：`Pass.TypesInfo` godoc 删除误导性链接
- F4：`newPackageRel` 加 `abs == ""` synthetic file 守卫
- F5：`scope.go` FileContext 重导出 godoc 标注 legacy 用途

**Round-2 review follow-up（F1-F6，PR #492 round-2 in-PR）**：
- F1：redfixture 扩 import 形态覆盖（qualified / aliased / dot-import 三路 × banned symbol 全交叉）；改用 var-reference 形态去除 testing 依赖
- F2：`Pass.Files` 语义统一为 ONE Pass per package（与 RunTyped + go/analysis 一致），消除"per-file callback + `pass.Files[0]` 在 typed mode 静默丢文件"风险；引入 `collectASTFiles` + `scanner.Scope.ModRoot()` accessor
- F3：`buildTypedPass` dedup 按"test-variant pkgs 优先"sort 保证确定性
- F4：抽 `archtestmeta.FixtureBuildTag` const，消除 build-tag 字面量重复
- F5：passfunnelfixture 删除 `testing` 依赖（var-reference only）
- F6：补 `pass_test.go` 11 单元测试，archtest 生产代码覆盖 12% → 71%

**Develop 同步**（最后一次 round in-PR）：
- 把 PR #490 派生的两个新 archtest 加入 LegacyAllowlist：`credential_invalidate_funnel_invariants_test.go`（用 scanner.EachFile + typeseval.SharedResolver + 直接 import packages，三处都进 yaml/Go 双 allowlist）+ `sessionvalidate_epoch_compare_test.go`（不用 banned symbol，仅进 Go allowlist 不进 yaml）
- `pass_test.go` 加 `// INVARIANT: ARCHTEST-PASS-DRIVER-UNIT-01` 合成 anchor（参 `helpers_test.go ARCHTEST-HELPERS-01` 范本）

**当前 LegacyAllowlist 总数**：54 文件（53 baseline + 1 PR #490 派生），等阶段 2/3 PR 一一删除。

## Context

GoCell 的 archtest 体系当前有两个并列入口：

- `scanner.EachFile(t, scope, mode, fn)` — 纯 AST 遍历，秒级，按目录树作用域，36 处调用 / 25 个 archtest
- `typeseval.LoadPackages(modRoot, tests, tags, patterns...)` + 裸 `for _, file := range pkg.Syntax` — 全类型图加载，分钟级，按 import path，33 个 archtest 用 LoadPackages、其中 48 处裸 for-range 分布在 30 个文件

两个入口在 Go 闭包层是合法可组合的，曾出过 INV-1 类 bug：archtest 作者用 scanner 入口遍历文件、却闭包捕获 typeseval 一侧的 `*types.Info`，AST 节点指针不同源 → `info.Types[node]` 静默 miss、规则永远不触发。当前 PR 通过引入 `typeseval.EachFileInPackage` 把 typeseval 一侧的"裸 for-range + 外部 info 捕获"挤掉，是 Medium 形态的局部收口。

业界 Go 静态分析框架（go/analysis、staticcheck、golangci-lint、wire）的共识做法是 **Pass-Driver 范式**：单一 Pass struct 把 Files / TypesInfo / Fset 同源注入，**用户拿不到自由构造 Pass 的能力，只能写 Rule 函数让框架调度**。本方案把 archtest 收敛到 `archtest.Pass + Rule` 单一编程模型，**业务文件零二次返工** —— 每个文件只在它的语义迁移 PR 中被改写一次。

## 业界对标关键发现

| 项目 | 入口设计 | INV-1 防御 |
|------|---------|-----------|
| **go/analysis** | `analysis.Pass` 字段公开（Files / TypesInfo / Fset / Pkg），driver-private construction（`checker/checker.go` 单 struct literal 同源注入），**Pkg 类型是 `*types.Package` 不是 `*packages.Package`** | 用户写 `Analyzer.Run(pass)` 接收 Pass、不能自由 new；driver 唯一构造路径同源 |
| **staticcheck** | 沿用 `analysis.Pass`，helper `IsCallTo(pass, ...)` 函数式入口 | 函数只接 Pass、不接独立 TypesInfo |
| **golangci-lint** | 不额外封装 | 经济效益（多次 packages.Load 显著变慢） |
| **wire** | `*gen{pkg *packages.Package}` 单字段 source of truth | 所有方法通过同一 pkg 访问 AST + TypesInfo |
| **ArchUnit (Java)** | `JavaClasses` 物理表示（.class 字节码合并） | 源头消除双视图 |

**关键观察**：go/analysis 的 `Pass.Pkg` 类型是 `*types.Package`（go/types 标准库），**不是 `*packages.Package`（golang.org/x/tools）**。这关键——用户拿到 Pass 看不到 `.Syntax` 字段，也就无法写出"裸 for-range pkg.Syntax + 外部捕获 info"的 INV-1 形态。

## 目标端态

```go
package archtest

// Pass is driver-constructed; users only receive it as Rule parameter.
// Fields are exported (per go/analysis convention); INV-1 defense comes from:
//   (1) Pkg type is *types.Package, NOT *packages.Package — no .Syntax access
//   (2) Construction path is single — Run / RunTyped driver
//   (3) depguard bans archtest *_test.go from importing packages directly
type Pass struct {
    Fset      *token.FileSet
    Files     []*ast.File
    Pkg       *types.Package    // go/types — exposes Name/Path/Imports/Scope, NO .Syntax
    TypesInfo *types.Info       // nil in AST-only mode
    Rel       func(*ast.File) string  // fset-bound rel-path helper
}

type Rule func(*Pass) []Diagnostic

// Driver entries — framework controls packages.Load / parser.ParseFile timing.
func Run(t *testing.T, scope Scope, rule Rule)                       // AST-only
func RunTyped(t *testing.T, opts TypedOpts, patterns []string, rule Rule)  // Typed

type TypedOpts struct {
    Tests bool
    Tags  []string
}
```

**Hard 防线三重组合**（满足 AI-rebust 章程 ≥ Medium 立项门槛，组合后构成 Hard）：

| # | 防线 | 等级 | 违反代价 |
|---|------|------|---------|
| 1 | `Pass.Pkg` 是 `*types.Package`，**Pass struct 不暴露 `*packages.Package`** | **Hard**（违反不可表达：archtest 作者拿到 Pass 写不出 `.Syntax`，编译失败） | 必须显式 import `golang.org/x/tools/go/packages` 并自己 Load |
| 2 | depguard 配置：`tools/archtest/*_test.go` 禁 import `golang.org/x/tools/go/packages`、`tools/archtest/internal/scanner`、`tools/archtest/internal/typeseval` | **Hard**（违反需修改 `.golangci.yml` allowlist，构成显著扩大改动面 + 声明意图） | 必须同 PR 改 depguard 配置，review 必抓 |
| 3 | 元治理 archtest `PASS-FUNNEL-01`（type-aware）：archtest 包外 `*_test.go` 调用 `scanner.EachFile` / `typeseval.LoadPackages` 触发 fail，迁移期 allowlist 暂豁免 | **Hard**（type-aware 检查，调用 target 经 `*types.Info` resolve；allowlist 是 declared path constant set，参考 ai-collab.md "string-typed concept funnel" 范本） | 不可绕过；新增违反必须改 allowlist |

#1 + #2 同时拦住"在新文件写 INV-1 形态"——AI 必须同时编辑业务文件 + depguard config 才能引入双视图捕获，构成不可表达级别的护栏。#3 是迁移期过渡，阶段 4 后清空（无 enforcement 残留）。

## 六轴取数现状与去向

| 轴 | 现状入口 | Pass 端态去向 |
|----|---------|--------------|
| 1 Go 源码 | `scanner.EachFile` + `typeseval.LoadPackages` + 裸 for-range | **合并到 `archtest.Pass + Run/RunTyped`** |
| 2 非 .go 内容 | `scanner.EachContentFile` / `LoadContentFiles` | 重命名 `archtest.EachContentFile`，签名不变 |
| 3 模块元数据 | `modfile.Parse` 读 go.mod | 独立保留 |
| 4 Build constraint | `typeseval.ParseBuildConstraint` / `IsGeneratedRelPath` | 收进 `Pass.IsFileInScope(file)` 方法 |
| 5 依赖图视图 | `kernel/depgraph` | 独立保留 + ADR 划界 |
| 6 depguard 配置 | `.golangci.yml` 解析 | 独立保留 |

原始 IO 已被 `SCANNER-FRAMEWORK-USAGE-01` 在 Path A/A'/B 三路 type-aware 收口，不在合并范围。

## 实施计划（4 阶段、~10 PR）

### 阶段 1 — PR-1 落地 Pass 框架 + 三重 Hard 防线（Cx3，业务文件 0 改动）— ✅ shipped as PR #492 (2026-05-14)

**关键设计**：业务 *_test.go 在阶段 1 **完全不被 touch**。enforcement 推到 `Pass` 类型设计 + depguard 配置 + 元治理 archtest 三层，allowlist 临时豁免存量。阶段 3 每个迁移 PR 是对应业务文件的**首次 + 唯一**改写。

**范围**：

1. 新建 `tools/archtest/pass.go`：`Pass` struct（`Pkg *types.Package`，**不是** `*packages.Package`）、`Rule` 类型、`Run` / `RunTyped` driver、`TypedOpts`、`Diagnostic`、`Report`
2. 新建 `tools/archtest/walk.go`：导出 `EachInSubtree[N]` / `EachInChildren[N]` / `StringLitValue` / `ReceiverTypeName`（实现委托到 internal/scanner，对外仅 archtest 包导出）
3. 新建 `tools/archtest/scope.go`：导出 Scope + helper（同上委托）
4. 新建 `tools/archtest/content.go`：导出 `EachContentFile` / `LoadContentFiles`
5. **scanner / typeseval 包保持原状**——保留导出 API，由 depguard + 元治理 archtest 在调用方阻挡新使用
6. 新建 `tools/archtest/internal/archtestmeta/legacy_allowlist.go`：

```go
package archtestmeta
// LegacyAllowlist lists archtest files that have not yet migrated to
// archtest.Pass. Each entry is deleted by the PR that migrates the
// corresponding file. MUST be empty by stage-4 completion.
var LegacyAllowlist = []string{
    "tools/archtest/accesscore_facade_test.go",
    // ... 30 个文件（脚本自动列出阶段 1 时点的存量）
}
```

7. 新建 `tools/archtest/pass_funnel_test.go`：元治理 archtest，三条规则
   - `PASS-FUNNEL-EACHFILE-01`：archtest *_test.go 调用 `scanner.EachFile` → 若文件不在 LegacyAllowlist 则 fail（type-aware via `*types.Info`）
   - `PASS-FUNNEL-LOADPACKAGES-01`：调用 `typeseval.LoadPackages` / `typeseval.SharedResolver` → 同上规则
   - `PASS-FUNNEL-PACKAGES-IMPORT-01`：`tools/archtest/*_test.go` 文件 import `golang.org/x/tools/go/packages` → 若文件不在 LegacyAllowlist 则 fail（覆盖未来潜在绕过）

8. 修改 `.golangci.yml`：depguard rule
```yaml
linters-settings:
  depguard:
    rules:
      archtest-no-direct-packages-load:
        files: ["tools/archtest/*_test.go"]
        deny:
          - pkg: "golang.org/x/tools/go/packages"
          - pkg: "github.com/ghbvf/gocell/tools/archtest/internal/scanner"
          - pkg: "github.com/ghbvf/gocell/tools/archtest/internal/typeseval"
        # allowlist exception: 30 legacy files (synchronized with LegacyAllowlist)
```

允许暂存 allowlist 在 yaml 注释 + Go 代码两处冗余声明，Stage-4 清零时一并删。

9. 新建 ADR `docs/architecture/<ts>-adr-archtest-pass-funnel.md`：业界对标 + 三重 Hard 防线 + 迁移路径

**业务 archtest 改动**：**零**。

**验证**：
- `go build ./...` + `go test ./tools/archtest/...` 全绿
- 元治理 archtest 三条规则全绿（allowlist 内文件被豁免，allowlist 外文件目前无违反）
- 故意在新建 archtest 中调 `scanner.EachFile` → 三重防线（depguard + PASS-FUNNEL-EACHFILE-01 + Pass.Pkg 不暴露 .Syntax）任一触发 fail

### 阶段 2 — PR-2 ~ PR-N：A 类 36 处 EachFile 主题分批迁移（Cx3 × 4 PR，可并行）

按 archtest 主题分批，每 PR 约 8-10 个文件，**与阶段 3 互相独立**（不同代码模式，可同期推进）：

- **PR-2**: governance / metadata 主题
- **PR-3**: contract / codegen 主题  
- **PR-4**: observability / health 主题
- **PR-5**: lifecycle / errcode / panic 等其余

**每个迁移 PR 的完整动作**（业务文件的首次 + 唯一改写）：

1. 把目标 archtest 从 `scanner.EachFile(t, scope, mode, fn)` 改写为 `archtest.Run(t, scope, func(pass *Pass) { ... })`
2. 同时改 import：去掉 `tools/archtest/internal/scanner`，加 `tools/archtest`（B/D 类 EachInSubtree / Scope / Diagnostic 等同时换前缀，**该文件相关的机械重命名一次性完成**）
3. 从 `archtestmeta.LegacyAllowlist` + `.golangci.yml` 注释 allowlist 删除对应文件
4. 元治理 archtest 立即对该文件生效

**关键性质**：每个文件的 import 路径 + API 前缀 + 语义迁移 **在同一 commit 中完成**，业务文件 **0 二次返工**。

### 阶段 3 — PR-(N+1) ~ PR-(N+5)：E 类 48 处裸 for-range 主题分批迁移（Cx3 × 5 PR，可并行）

按 archtest 主题分批，每 PR 约 6-8 个文件：

- **PR-6**: `clock_invariants_test.go`（6+ for-range，单 PR）
- **PR-7**: `errcode_invariants_test.go` + `auth_bootstrap_invariants_test.go`
- **PR-8 ~ PR-10**: 其余 27 个文件分 3 批

每 PR 范式同阶段 2：业务文件首次 + 唯一改写，`typeseval.LoadPackages` → `archtest.RunTyped`，import 切换 + allowlist 删除一次完成。

**性能**：`RunTyped` 内部继承现有 `typeseval.SharedResolver` 的 singleflight + process-wide cache，多 archtest 共享一次 packages.Load。迁移不会导致 CI 时长退化。

**与阶段 2 的关系**：阶段 2 (EachFile) 与阶段 3 (for-range) 改的是不同代码模式，**可整体并行**。同一 archtest 同时用两种入口的情况罕见（盘点：≤ 3 个文件）；这些文件归入主题对应的 PR 一次性完整迁移。

### 阶段 4 — PR-(N+6) 收尾（Cx1）

1. 验证 `archtestmeta.LegacyAllowlist` 与 `.golangci.yml` allowlist 均为空（应当如此）
2. 删除 `archtestmeta` 包整体（仅服务于迁移期）
3. 删除 `.golangci.yml` 的 allowlist 例外（保留 deny 规则）
4. 删除元治理 archtest `pass_funnel_test.go` 中的 allowlist 引用（保留 `Pass.Pkg 不暴露 packages.Package` 与 depguard 作为永久防线）
5. 把 `tools/archtest/internal/scanner` + `tools/archtest/internal/typeseval` 包名重命名（如 `_legacy_*`）或直接挪进更深的 internal——可选，因为 depguard 已经永久封死外部访问
6. 更新 `.claude/rules/gocell/ai-collab.md`：把 archtest.Pass 列为 Go 源码扫描的唯一推荐路径

## 并行分析

| 阶段 | PR 数 | 内部并行 | 阶段间并行 |
|------|------|---------|-----------|
| 1 | 1 | — | — |
| 2 (EachFile 主题分批) | 4 | **可全并行**（不同 archtest 文件 PR） | **可与阶段 3 并行** |
| 3 (for-range 主题分批) | 5 | **可全并行** | **可与阶段 2 并行** |
| 4 | 1 | — | 等阶段 2 + 3 全部完成 |

**并行度峰值**：阶段 2 + 阶段 3 同时进行时理论 9 个 PR 互相并行。

**实际推荐节奏**：
- 阶段 1 单 PR，merge 后启动阶段 2/3
- 阶段 2/3 维持 2-3 个并发 PR（受 review 资源限制）
- 9 个 PR 滚动推进，约 2-3 周完成阶段 2+3
- 阶段 4 单 PR 收尾

**冲突点**：同一 archtest 文件若同时被阶段 2 + 阶段 3 改（罕见，≤3 个文件），归入 EachFile 主题的 PR 一次性完整迁移，不分两次。

## 原则符合性核对

| 原则 | 符合性 | 说明 |
|------|-------|------|
| **彻底** | ✅ | 阶段 4 删 allowlist + scanner/typeseval 全部 internal 化，0 残留 |
| **不向后兼容** | ✅ | 阶段 1 不引入"deprecated 旧 API 还能用"的过渡通道；scanner.EachFile 在 archtest 包外被 depguard 立即封锁，allowlist 是有终结点的迁移清单（todo list），不是兼容垫片 |
| **0 二次返工** | ✅ | 业务文件只在它的语义迁移 PR 中被 touch 一次（import + API + 语义同 commit） |
| **优雅简洁** | ✅ | Pass + Rule + Run/RunTyped 三个核心概念；对标 go/analysis；helper 函数零额外发明 |
| **AI HARD** | ✅ | 三重防线组合 Hard：(1) Pass.Pkg 不暴露 `*packages.Package` 让 INV-1 形态在新代码中编译失败；(2) depguard 锁住 packages 直接 import；(3) 元治理 archtest type-aware 拦截存量入口 |

## 关键设计决策

### D1 — `Pass.Pkg` 用 `*types.Package`，不用 `*packages.Package`

业界共识（go/analysis、staticcheck）。`*types.Package` 暴露 Name / Path / Imports / Scope 等类型层信息，**不暴露 `.Syntax`**。用户拿到 Pass 在 archtest 包外**写不出**裸 for-range Syntax 的代码——编译失败而非 archtest 拦截，是真正的 Hard。

### D2 — 不私有化 `*types.Info`、不挂 Pass 方法

业界共识。`pass.TypesInfo` 公开访问，helper 函数 `archtest.ResolveSelector(info, expr)` 等接 info 而非 Pass，与现有 `typeseval.ResolvePackageRef` 范式延续。私有化方案改造面大且 INV-1 真正护栏在 D1（不暴露 `*packages.Package`），D2 是简洁性选择。

### D3 — allowlist 用文件路径常量数组（非 // 注释豁免）

写在 `archtestmeta.LegacyAllowlist` 单文件 Go 数组中：

- 单源、可 grep、可 diff 跟踪
- 删一项是单行 diff，review 直观
- 不污染业务 archtest 文件
- 与 ai-collab.md "string-typed concept funnel" 设计范本一致

### D4 — depguard 与元治理 archtest 双层并存

depguard 是 lint 期路径级拦截（Hard，需修配置才能绕）；元治理 archtest 是 type-aware 调用拦截（Hard，需修 allowlist）。两层独立失效模式：

- 若 AI 试图通过 vendor 或 type alias 绕过 depguard，元治理 archtest 的 type-aware resolve 仍然抓到
- 若 AI 试图修改元治理 archtest，depguard 拦住 import 第一步

冗余防线在生产代码中是 over-engineering，在元治理中**是正确做法**——元治理失效成本极高（INV-1 类 bug 会污染所有依赖的规则）。

### D5 — 不需要"AST-only Pass 也能调 ResolveSelector"的伪类型路径

AST-only 模式下 `pass.TypesInfo == nil` / `pass.Pkg == nil`，helper 函数自然短路返回 `(zero, false)`。调用方必须在选择 `Run` (AST-only) vs `RunTyped` 时明确 input domain，编译时不可混淆。

## 验收方式

### 单元验证（每 PR 必跑）

```bash
go build ./...
go build -tags=integration ./...
hack/verify-archtest.sh
golangci-lint run --new-from-rev=main  # 验 depguard 规则正确触发
```

### 阶段切换验证

```bash
go test -tags=integration ./...
make verify
```

### 范式回归验证

每个迁移 PR：
1. production source pass 行为零变化（migration 是重写不是改逻辑）
2. negative fixture pass：构造 INV-1 形态 mock，确认 D1 编译失败 + D2 / D3 拦截
3. 性能基线：`go test ./tools/archtest/...` wall-clock 不超过现状 1.1x

### 阶段 4 收尾端到端验证

1. 新建 archtest 试图 `import "golang.org/x/tools/go/packages"` → depguard fail
2. 新建 archtest 试图 `Pass.Pkg.Syntax` → 编译 fail（D1 Hard）
3. `archtestmeta.LegacyAllowlist == nil` 静态断言
4. `git log --grep "allowlist"` 应能逐 PR 追踪每个文件的迁移轨迹

## 范围外（明确不做）

1. 私有化 `*types.Info`（D2 决策）
2. 收口 `kernel/depgraph` 视图到 Pass（轴 5 独立 ADR 划界）
3. 合并 `.golangci.yml` depguard 配置到 Pass 体系（轴 6 独立）
4. 重写 `SCANNER-FRAMEWORK-USAGE-01` 元治理（已是 Hard 形态）
5. 引入"per-file 回调"形态（用户在 Rule 内自己 `for _, file := range pass.Files`，与 go/analysis 一致）

## 关键文件清单

**新建**:

| 文件 | 阶段 | 内容 |
|------|------|------|
| `tools/archtest/pass.go` | 1 | Pass struct + Rule + Run / RunTyped driver + TypedOpts |
| `tools/archtest/walk.go` | 1 | EachInSubtree / EachInChildren / StringLitValue / ReceiverTypeName 重导出 |
| `tools/archtest/scope.go` | 1 | Scope + 配置 helpers 重导出 |
| `tools/archtest/content.go` | 1 | EachContentFile / LoadContentFiles 重导出 |
| `tools/archtest/diagnostic.go` | 1 | Diagnostic / Report 重导出 |
| `tools/archtest/internal/archtestmeta/legacy_allowlist.go` | 1 | LegacyAllowlist 数组（阶段 4 删除） |
| `tools/archtest/pass_funnel_test.go` | 1 | PASS-FUNNEL-01/02/03 元治理 archtest |
| `docs/architecture/<ts>-adr-archtest-pass-funnel.md` | 1 | 范式 ADR |

**修改**:

| 文件 | 阶段 | 改动 |
|------|------|------|
| `.golangci.yml` | 1 | 新增 depguard rule `archtest-no-direct-packages-load` + allowlist |
| `tools/archtest/*_test.go`（约 30 个迁移 + 30 个仅 import 切换） | 2/3 | import 切换 + EachFile / for-range 改写 + allowlist 删除（每文件一次性 commit） |
| `.claude/rules/gocell/ai-collab.md` | 4 | 把 archtest.Pass 列入载体决策原则推荐路径 |

**复用**（已有、不需改）:

| 文件 | 用法 |
|------|------|
| `tools/archtest/internal/scanner/parse.go` `EachFile` | Run driver 内部委托 |
| `tools/archtest/internal/scanner/eachnode.go` `EachInSubtree` / `EachInChildren` | walk.go 转发 |
| `tools/archtest/internal/scanner/scope.go` `Scope` / `ModuleScope` / `DirsScope` | scope.go 转发 |
| `tools/archtest/internal/typeseval/typeseval.go` `SharedResolver` | RunTyped driver 内部复用 |
| `tools/archtest/scanner_framework_usage_test.go` | 已有 Path A/A'/B 元治理范式，pass_funnel_test.go 参考其结构 |

## 风险与权衡

| 风险 | 概率 | 影响 | 缓解 |
|------|------|------|------|
| 阶段 1 PR-1 体量过大 | 中 | review 负担 | 拆为 1a (Pass + Rule + Run/RunTyped + ADR) / 1b (walk + scope + content + diagnostic 重导出) / 1c (allowlist + 元治理 archtest + depguard) 三个小 PR，串行推进 |
| 阶段 2/3 并行 PR git 冲突 | 低 | merge 痛苦 | 主题分批本身减少冲突面；同文件冲突的 ≤3 个 archtest 归入单 PR |
| 元治理 archtest 误判 false positive | 低 | CI 误阻 | 阶段 1 PR-1 merge 后先观察 3-5 天再启动阶段 2 |
| RunTyped 性能退化 | 极低 | CI 时长 | SharedResolver singleflight 不变，packages.Load 仍一次性 |
| Allowlist 遗忘条目残留 | 中 | 长期半成品 | 阶段 4 必须 `archtestmeta.LegacyAllowlist == nil` 静态断言；project-manager 跟踪每个迁移 PR 是否删 allowlist 项 |
| `*types.Package` 不够用、需要回到 `*packages.Package` | 极低 | 设计退化 | 业界 go/analysis 验证 `*types.Package` 已覆盖所有 archtest 用例；真有特例可加 `Pass.Imports() []string` 等具名方法暴露所需子集，不暴露 `*packages.Package` 整体 |
