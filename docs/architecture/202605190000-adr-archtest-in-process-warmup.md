# ADR 202605190000: archtest 进程内 cacheKey 摊销 + tagGroup 循环范本拆除

## 状态

Accepted (2026-05-19)

## 上下文

`tools/archtest/` 16-shard process-isolated 矩阵（ADR 202605120000）下，每 shard 独立 go test 进程，`typeseval.SharedResolver` process-wide 缓存随进程死。R2-P3 PR-b #530 CI 实证两轴 trigger（详见 backlog `ARCHTEST-SHAREDRESOLVER-AMORTIZATION-01`）：

### A 轴 — modulo-shift SLOW

同 shard 内 N 个 Test* 各自首次触发 `SharedResolver(modRoot, false, nil, "./...")` cache miss，`packages.Load NeedTypesInfo` 全模块 ~10-30s。`TestUserRepoConformanceEnrollment` 22.3s / `TestCellRepoReadyzProbe` 20.43s 连续跨 20s slowgate budget。

### B 轴 — pattern-choice OOM

`panic_invariants_test.go::TestPanicRegistered` 范本 `for tagGroup := range KnownNonDefaultTags() { RunTyped(./..., Tags: tagGroup) }` 各自加载全模块累积 RSS → GHA 2-CPU 7GB shard 12 OOM SIGTERM。本范本被 2 处复抄（panic_invariants_test:380 + cellgen_errcode_funnel_test:284）。fresh-instance Claude 看到这两处会继续抄，B 轴必复现。

### 实测分布

探索阶段对 `tools/archtest/` 全部 `SharedResolver` / `LoadPackages` / `RunTyped` / `RunTypedProduction` / `RunTypedDir` 调用点统计 patterns 维度：
- `"./..."`：22 次 (16.5%)
- 主模块 subpath（`./cells/...`, `./cmd/...`, `./runtime/...` 等）：111 次 (83.5%)
- 独立 fixture modRoot (RunTypedDir)：91 个 fixture 子模块

## 决策

3 改造一体化：

### 改造 1 — TestMain in-process warm-up（A 轴）

`tools/archtest/testmain_test.go` 加 `TestMain(m *testing.M)`，所有 Test* 跑前调用 `typeseval.LoadProductionPackages(root, modPath, false, nil)` 预热最高频 cacheKey。失败 fail-fast `os.Exit(1)`。

只预热 1 个 cacheKey：探索阶段实测显示 `(false, nil, "./...")` = LoadProductionPackages 主路径覆盖最广（LAYER-* + 多数业务 Test*）；subpath patterns 各自只被 1-3 个 Test 使用，预热成本 > 收益。其他高频 cacheKey 如 `SharedResolver(root, true, nil, "./tools/archtest/...")` 会撞 PASS-FUNNEL-LOADPACKAGES-01（archtest *_test.go 禁直调 SharedResolver），testmain_test.go 不是 funnel 实现无 exempt 资格。

### 改造 2 — tagGroup 循环范本拆除（B 轴：两次 Load + 共享 seen dedup）

把 `for tagGroup := range KnownNonDefaultTags() { RunTyped(...) }` 替换为两次 RunTyped：
1. `RunTyped(t, TypedOpts{}, patterns, scan)` — tags=nil 覆盖默认 build + 反向 directive 文件
2. `RunTyped(t, TypedOpts{Tags: archtest.ProductionFlatTags()}, patterns, scan)` — 覆盖所有正向 tag 激活文件 union

共享 seen map dedup（文件集有重合，dedup 保证 violations 唯一）。N=7 → 2，RSS 峰值从 N×全模块 → 单次峰值（GC 可在两次 Load 间释放中间体）。

### 改造 3 — AI Hard archtest 防复抄

新建 `tools/archtest/taggroup_loop_no_runtyped_test.go`，`TAGGROUP-LOOP-FORBIDS-RUNTYPED-01` 用 (callee, body) 双因子 form-uniqueness 锁 RangeStmt+RunTyped 范本。fresh Claude 复抄 = CI 红。

## 开源对标参考

| 对标项 | 来源 | 本 ADR 对应 |
|--------|------|------------|
| `golang.org/x/tools/go/analysis/checker` DAG memoization (mkAction map) | https://github.com/golang/tools/blob/master/go/analysis/checker/checker.go | SharedResolver (sync.Map + singleflight) 已是同构架构；TestMain 是触发时机优化 |
| `golangci-lint pkg/goanalysis/runner.go` runner 单例 + union load mode | https://github.com/golangci/golangci-lint/blob/main/pkg/goanalysis/runner.go | runner 单例 = SharedResolver；union load mode = TestMain 预热 LoadProductionPackages |
| `staticcheck SA4000` AST + `types.Info.Uses` resolve callee form-uniqueness | https://github.com/dominikh/go-tools/blob/master/staticcheck/sa4000/sa4000.go | 改造 3 直接同构 |
| `go/build/build.go::matchTag` `-tags=A,B,C` 语义 | https://github.com/golang/go/blob/master/src/go/build/build.go | 改造 2 两次 Load 必要性的依据（反向 directive 静默排除陷阱） |
| ai-collab.md §"Hard 范本" 第 2 条 `PANIC-REGISTERED-01` | GoCell 自有 | 改造 3 typed-function-call funnel 同构 |

## Alternatives 拒绝理由（含对标反向）

- **包级 `sync.Once` 替代 TestMain**（对标 explorer 建议）：拒绝。slowgate budget 维度下 sync.Once 在 borderline test 内首次触发仍跨 20s budget；TestMain 是把"首次 cache miss 时机"从 budget 内移到 budget 外的唯一机制。sync.Once 适用于 perf 视角，不适用于 budget 工程化视角。
- **TestMain 失败重试**：拒绝。`packages.Load` 失败几乎全是本地代码问题（语法错、import 循环、go.mod 缺失），不是网络抖动；module proxy 在 load 之前已由 `go build` 解析完成。fail-fast 是正确选择，retry 会掩盖真实问题。
- **单次 union Load** (改造 2 simpler)：拒绝。`go/build/build.go::matchTag` `-tags=A,B,C` 下 `BuildTags = {A,B,C}`，`//go:build !pg` 在 union 含 `pg` 时静默排除。GoCell 当前 3 处反向 directive (`!catalog_gen` / `!unix && !windows` / `!windows`) 均不引用 KnownNonDefaultTags tag，**单次 union Load 当前不漏覆盖**；但未来若有人加 `//go:build !integration` 类文件会漏，无报错。两次 Load 是默认安全网。
- **SharedResolverFullModule + SharedResolverPatterns 拆 typed**：拒绝。`LoadProductionPackages` 已是 `"./..."` 形态 typed wrapper，等价目标已达成。
- **剥离 cacheKey patterns 维度**：拒绝。83.5% subpath patterns 必须保留 patterns 维度。
- **剥离 tests 维度**：拒绝。`Tests:true/false` packages.Load 返回的 *types.Info 不兼容。
- **保留 GOCELL_ARCHTEST_NO_WARMUP escape hatch**：拒绝。违反"不引入双路径"；按 trigger 单独改造而非预留逃生口。
- **B 轴拆 backlog 单独 PR**：拒绝。与 A 轴同 root cause；分拆 = 治标；fresh Claude 仍会复抄范本。
- **磁盘 cache 持久化 SharedResolver**（对标 staticcheck runner）：拒绝。archtest 进程极短生命周期（每 shard 单次 `go test`），磁盘 cache I/O 反而拖累。
- **改造 3 用 SSA 层而非 AST + types.Info**：拒绝。staticcheck SA4000 AST + types.Info 形态足够；SSA 适用于跨块流敏感分析。

## 威胁矩阵 / 资源边界变化

| 维度 | Before | After | 变化 |
|------|--------|-------|------|
| shard startup wall | 0 | +15-25s 固定 | TestMain 预热 1 个 cacheKey |
| Test* 首次 SharedResolver wall | 10-30s cache miss | <100ms cache hit | A 轴解 |
| B 轴 RSS 峰值 | N×全模块 (N=7) cumulative | 2×全模块 + GC 间隔回收 | B 轴解 |
| 反向 build directive 监控 | 不感知 | 未来加 `//go:build !X` (X ∈ KnownNonDefaultTags) 需 review 两次 Load 设计完整性 | 新增隐含约束 |
| 复抄范本 | 已 2 处实证 + fresh Claude 必复发 | archtest 静态拦截 | 改造 3 解 |
| `warm.go` 直调 typeseval | 不存在 | 不受 PASS-FUNNEL-LOADPACKAGES-01 保护（该规则仅扫 `_test.go`） | 已接受：warm.go 是 archtest 包内 unexported helper，仅 TestMain 调用，无外部 _test.go 滥用风险；future-proof 升级路径见 backlog `PASS-FUNNEL-NONTESTGO-EXEMPT-UPGRADE-01` |
| TAGGROUP-LOOP scope gap | 不存在 | `tools/archtest/internal/<subpkg>/*_test.go` (e.g. internal/scanner/, internal/typeseval/) 不被 TAGGROUP-LOOP-FORBIDS-RUNTYPED-01 扫描 | 已接受：internal 子包测试内部符号，不调 archtest.RunTyped；若未来某 internal _test.go 加直调 RunTyped 形态需扩 scope |
| CI log path exposure | 不存在 | TestMain fail-fast 时 slog.Error 输出含完整 modRoot/cwd 路径，会出现在 GHA artifact 中 | 已接受：路径非凭据，且 testmain 是 perf/bootstrap 路径，无 PII；如未来仓库公开化需重评 |
| Total CI wall (16 shard) | 受 borderline test 跨 budget 影响 + B 轴 OOM SIGTERM 重跑 | 预期：单 shard wall ≤ 5s (warm) + 0-3 个 borderline ≤ 10s (warm 后)；16 shard parallel wall ~30-40s + 启动开销 | warmup 在轻 shard (e.g. shard 0 = 33 tests / 4.78s) 上付 +15-25s 启动成本是 wash 或微亏；在重 shard / borderline shard 上净赚；实测需 CI 矩阵观察 2-3 次 |

### 16 shard 总 wall 详细分析

CI 矩阵 16 shard parallel，总 wall = max(各 shard wall) + GHA queue overhead。
- **重 shard / borderline shard**（含 TestUserRepoConformanceEnrollment / TestCellRepoReadyzProbe 等）：原来 20-22s borderline + 其他 Test* 各自首次 cache miss 5-15s = 总 wall ~35-50s。预热后所有 Test* cache hit < 100ms，重 shard wall 降到 ~startup 15-25s + 测试 5-10s = ~25-35s。
- **轻 shard**（e.g. shard 0 = 33 tests / 4.78s 实测）：原来 ~5s。预热后 startup +15-25s 拖到 ~20-30s，是净亏。但因 16 shard parallel 总 wall 取 max，轻 shard 拖慢不影响总 wall（仍由重 shard 决定）。

净结论：CI 总 wall 预期下降（重 shard 主导），轻 shard 单独跑（如开发者本地 `go test ./tools/archtest/ -run MyTest`）会增加 wall — 由 testmain_test.go godoc 提示，开发者可接受。

实测 baseline 由 PR 合并后 CI 矩阵连续 2-3 次运行采集，记录到 PR `#584` thread 或 follow-up backlog `ARCHTEST-SLOWGATE-ALLOWLIST-CLEANUP-01` 内。

## 回退路径

- **整体回退**：本 PR 合并若 squash 为单 commit，直接 `git revert <sha>` 即可；无 schema/migration/接口变化。
- **selective 回退**：本 PR 包含 2 个语义 wave commit（Wave 0 RED = archtest + fixture + ProductionFlatTags helper；Wave 1 GREEN = TestMain 预热 + B 轴两次 Load + ADR）。若 Wave 1 GREEN 出 regression 而 Wave 0 archtest 无问题，按逆序 revert：先 Wave 1 GREEN，再 Wave 0 RED；revert Wave 1 后 archtest 会重新命中现有复抄变 RED，需同时 revert Wave 0 才回到 main 健康。
- 任何 revert 后需重跑 `hack/verify-archtest.sh` 全 16 shard 矩阵确认。

## slowgate allowlist follow-up

本 PR 的核心收益之一是让 borderline test（TestUserRepoConformanceEnrollment、TestCellRepoReadyzProbe 等）的首次 cache miss 时机从 budget 内移到 shard startup 外，预期 wall 从 20-22s 降到 5-10s。这意味着 `tools/slowgate/allowlist.txt` 的相关条目（TestPanicRegistered / TestUserRepoConformanceEnrollment / TestCellRepoReadyzProbe / TestCellgenErrcodeFunnelNoBuildTagFiles）应可缩减。

本 PR **不同步缩减 allowlist**，原因：(i) 实测 wall 取决于 GHA runner 性能（本地实测无代表性）；(ii) 需观察 CI 16-shard 矩阵 2-3 次连续运行后再判断每条 test 是否稳定 < 20s。同 PR 缩减 allowlist 若实测仍跨 budget 会触发 false positive CI 红。

follow-up 由 backlog `ARCHTEST-SLOWGATE-ALLOWLIST-CLEANUP-01` 跟踪。

### PR #584 落地实测调整（2026-05-19）

PR #584 CI 首次运行 shard 12 触发 slowgate fail：`TestArchtestVerifyCoverage01` 跑 27.14s 跨 20s budget。根因诊断：该 test 跑 1 次 `verify-archtest.sh DRY_RUN` + K=4 次 `LIST_SHARD_TESTS` 子进程，每次子进程独立 `go test -list ./tools/archtest` ≈ 5-6s on GHA → 累积 ~27s。

**与本 PR 的关系**：该 test 的子进程开销与本 PR 的 in-process TestMain 预热**正交**——子进程 cache 独立，TestMain 预热无法摊销子进程 `go test -list`。modulo 重排把 TestArchtestVerifyCoverage01 推进 shard 12 暴露了这条 pre-existing 子进程开销。

**应急处理**：同 PR 把 `TestArchtestVerifyCoverage01` 加进 `tools/slowgate/allowlist.txt`，并加注释明确"subprocess-overhead，不在 TestMain 预热范围"。这条 allowlist 是**结构性必要**而非 cache-miss workaround，不被 `ARCHTEST-SLOWGATE-ALLOWLIST-CLEANUP-01` cleanup wave 覆盖。

**根本修复路径** 由新 backlog `ARCHTEST-VERIFY-COVERAGE-DISPATCH-OPTIMIZE-01` 跟踪：K=4 → K=2 / bash 脚本侧合并 DRY_RUN + LIST_SHARD_TESTS / 用 `go list -test` 一次性拿全集。

## AI-rebust 评级

- 改造 3 `TAGGROUP-LOOP-FORBIDS-RUNTYPED-01` = typed-function-call funnel **Hard**（对齐 ai-collab.md §"Hard 范本" 第 2 条 panic 范本同构 + staticcheck SA4000 同构）
- 改造 1 (TestMain) / 改造 2 (两次 Load) = perf refactor，不在 ai-collab.md §适用范围

## 参考

- ADR `docs/architecture/202605120000-adr-archtest-process-isolation.md`（前置 ADR — process isolation 与本 ADR 叠加非冲突）
- `tools/archtest/internal/typeseval/typeseval.go` — SharedResolver godoc 同 PR 补"为什么保留 patterns 维度"段
- `tools/archtest/taggroup_loop_no_runtyped_test.go` — 改造 3 实现
- `tools/archtest/internal/taggrouploopfixtures/` — 改造 3 fixture self-check
- Backlog `ARCHTEST-SHAREDRESOLVER-AMORTIZATION-01` (cap-14-tooling.md) — 关闭于本 ADR
