# ADR: Archtest CI 入口 process-isolated sharding（CI K=16 / 本地默认 K=1）

> Status: Accepted (Amended 2026-05-23)
> Date: 2026-05-12
> Implementation: fix/307-archtest-verify-process-isolation
> Source plan: docs/plans/202605110830-305-archtest-verify-process-isolation.md
> Amendment: 见末尾 §Amendment（2026-05-23, branch 573-archtest-default-shard）

## Context

PR #445（PR-Φ EachNode funnel + `SCANNER-FRAMEWORK-USAGE-01` type-aware）合并到 develop 后，PR Check `build-test (tools)` shard 在 GHA 2-core 7GB runner 上稳定 SIGTERM exit 143（OOM）。临界点对齐 PR #445 merge：merge 前 100% pass，merge 后 100% fail。

层层根因：

1. **L1 直接**：`SCANNER-FRAMEWORK-USAGE-01` 升 type-aware，`forbiddenWalkRefs` 用 `*types.Info` 做 receiver method 识别。
2. **L2 机制**：`tools/archtest/internal/typeseval.SharedResolver` 按 cacheKey `(modRoot, tests, tags, patterns)` 缓存 `*types.Info`。PR #445 引入新 cacheKey 维度，每 cacheKey 持有完整 module type graph（200-500 MB）。
3. **L3 架构**：`go test ./tools/archtest/...` 是**单进程**跑 296 个 archtest 函数（实测，非源 plan 估的 70），所有 `*types.Info` 缓存累加在同一 process heap。Go GC 无法跨 test function 释放包级 var (`sharedCache`) 持有的 type graph 引用。
4. **L4 设计**：把 296 个 governance 静态检查塞进 `go test` unit-test pipeline 本质上不可扩展。

Phase 0 本地实测（macOS local，BSD `/usr/bin/time -l` maximum resident set size）：

| 形态 | wall | peak RSS |
|---|---|---|
| single-process 全跑 296 test | 70.23s | **23.94 GB** |
| K=6 modulo shards (max shard) | 16.92s | **11.04 GB** |
| K=8 modulo shards (max shard) | 13.47s | **6.30 GB** |
| K=16 modulo shards (max shard) | 12.10s | **4.22 GB** |

K=16 是首个稳定低于 GHA 7GB OOM 阈值的分片粒度。K=8 留余量不足（Linux RSS 通常高于 macOS RSS）。

## Decisions

### D1. CI 入口改为 process-isolated 16-shard 矩阵（CI 显式 K=16 / 本地默认 K=1）

`hack/verify-archtest.sh` 整体重写：discovery via `go test -list '^Test' ./tools/archtest`，按字母序 modulo `SHARD_COUNT` 分片（**CI: 16 explicit；本地默认: 1**，见 §Amendment 2026-05-23），每 shard 独立 `go test -run '^(name1|name2|...)$'` 调用。SHARD_TARGET 单 shard 模式给 GHA matrix 用；无 SHARD_TARGET 时串行跑 `SHARD_COUNT` 个 shard（本地 `make verify` 路径默认 K=1 单 shard）。

`.github/workflows/_build-lint.yml` 新增 `verify-archtest` job：`matrix.shard: [0..15]` + 显式 `env: SHARD_COUNT: 16`（GHA 7 GB shard RSS 约束）；每 shard 独立 ubuntu-latest runner。`fail-fast: false` 对齐 K8s `hack/make-rules/verify.sh` continue-on-failure 范式。CI explicit `SHARD_COUNT=16` 由 `ARCHTEST-CI-EXPLICIT-SHARD-COUNT-01` archtest 守卫（见 §Amendment）。

### D2. tools shard 不再 enumerate archtest，pkgs 运行时计算

`.github/workflows/_build-lint.yml` tools shard `pkgs` 改为 sentinel `_dynamic_archtest_excluded`；Test step 用 `go list ./tools/...` + grep 排除 archtest **顶层包**（`github.com/ghbvf/gocell/tools/archtest`）。`tools/archtest/internal/{scanner,typeseval,rawparamfixture,wrapfixture}` 子包保留在 tools shard 执行——它们的测试是轻量单元测试（11+3+0+0 \_test.go），不触发 `typeseval.SharedResolver` 的 type graph 累加（296 archtest 顶层函数才是 OOM 触发源）。

**AI-rebust 评级**：按 `.claude/rules/gocell/ai-collab.md` 载体定义严格分类为 **Medium**（shell runtime guard + go list subprocess + grep 过滤），不是 codegen funnel / type system / sealed interface 的 Hard。但从覆盖保证角度等效 Hard：「新 `tools/<sub>` 包被遗漏」**在 type system 不可表达**——`go list ./tools/...` 是 ground truth，新包自动入列，archtest 是唯一显式排除目标。AI 仍能通过手工改 yml 回硬编码列表绕过 runtime 计算，但这种回退要改 yml + 改注释 + 通过 diff review，可观测性足够。在本 PR 范围内接受为 **Hard-effective**，不立 archtest 元规则守卫（守卫本身也是 Soft 的字符串扫，反而开倒车）。

### D3. discovery vs AST 一致性元规则

`tools/archtest/archtest_verify_coverage_test.go::TestArchtestVerifyCoverage01`（INVARIANT `ARCHTEST-VERIFY-COVERAGE-01`）：shell-out `DRY_RUN=1 bash hack/verify-archtest.sh` → 与 `scanner.EachInSubtree[ast.FuncDecl]` AST 扫到的 top-level Test* 函数集合做对称 diff，非空则 fail。守的风险：维护者改脚本加 `grep -v TestFoo` debug 过滤忘删 → CI silent unenforce（local `go test ./tools/archtest/...` 仍捕获，但 PR Check 漏过）。AI-rebust **Medium**（runtime cross-check 双重源）。

### D4. `make verify` 委托 archtest 给 matrix gate（D6 single-owner 落地形态）

`.github/workflows/governance.yml::make verify` 保留 `env: VERIFY_SKIP: archtest` 显式委托给 `_build-lint.yml::verify-archtest` matrix gate，避免 push/PR 上双跑 archtest（详见 §D6 single-owner 原则）。timeout-minutes 维持 15 容纳其它 verify-*.sh 子脚本耗时。

本地 `make verify`（无 `VERIFY_SKIP` env）仍包含 `verify-archtest.sh`，按 `SHARD_COUNT` 默认 K=1 单进程跑（见 §D1 + §Amendment 2026-05-23）；PR Check 的 matrix-parallel `verify-archtest` job (SHARD_COUNT=16 explicit) 是 CI 上唯一权威 archtest gate。

> **历史**：本节原文为 "governance.yml 删 VERIFY_SKIP env" + "verify-archtest.sh serial 16-shard"。§D6 加入时反转此决策（恢复 VERIFY_SKIP 避免双跑）；§Amendment 2026-05-23 进一步把脚本默认 K 改为 1。本节文本同 PR 重写以与现状一致（per ai-collab.md §"ADR amendment 落地必查"）。

### D5. slowgate 重接

`hack/verify-archtest.sh` 内 shard 路径：若 `$SLOWGATE_BIN` executable，`go test ... -json -run '...'` tee 到 `$RUNNER_TEMP/archtest-shard-N.json` 再管道入 slowgate（与 `_build-lint.yml` 旧 tools shard 同范式）；否则 plain `go test`（local dev）。matrix job 内 `go build -o "$RUNNER_TEMP/slowgate" ./tools/slowgate` 后注入 env；`if: failure()` artifact 上传保留 json event stream 供失败诊断。

### D6. Single-owner 原则：matrix is the sole archtest gate on CI

`_build-lint.yml::verify-archtest` matrix（16 shard）是 push / pull_request 上的 **唯一权威 archtest gate**。`governance.yml::make verify` 通过 `env: VERIFY_SKIP: archtest` 显式委托，**不再双跑**。

理由（K8s + Watermill 范式对照）：
- K8s 每个 verify-*.sh 是独立 Prow job（一 owner / 一 gate）；aggregator `hack/make-rules/verify.sh` 是开发者本地一键入口，不是 CI 上的二次 gate
- Watermill 用单个 reusable workflow 作为 PR/master 共同实现，调用方只做薄包装；语义差异通过显式 input 表达，不靠 caller-injected env 改 script 行为
- 若 governance 在 PR 上也跑 archtest：(a) CI 资源 ×2；(b) 同 script 在两个 caller 下行为分叉（matrix 注入 SLOWGATE_BIN 有 budget 门，governance 无）—— 同名 gate 双契约破坏 reproducibility

本地路径不变：
- `make verify` 不带 `VERIFY_SKIP` env，仍调 `hack/verify-archtest.sh`，作为开发者一键全跑
- 脚本因 `SLOWGATE_BIN` 缺失走 plain go test 路径——by-design 单本地路径（slowgate budget gate 是 CI 关注点，本地 dev 关注正确性）
- 故 script 仍有「`SLOWGATE_BIN` 在则 pipe；不在则 plain」的内部分支，但 CI 只有一个 caller（matrix）注入 `SLOWGATE_BIN`，没有 caller-divergent 契约

### D7. 元规则覆盖 dispatch 路径，不仅 discovery

`tools/archtest/archtest_verify_coverage_test.go::TestArchtestVerifyCoverage01` 包含两条断言：

1. **Discovery cross-check**（原 D3）：`DRY_RUN=1 bash hack/verify-archtest.sh` 输出 == AST scan `tools/archtest/*_test.go` 中 top-level `func TestX(t *testing.T)` 集合
2. **Partition exactly-once**（新增）：`LIST_SHARD_TESTS=1 SHARD_COUNT=k SHARD_TARGET=s bash hack/verify-archtest.sh` 对 s ∈ [0, k) 各 emit 一次该 shard 的 assignment，断言（a）所有 shard 并集 == discovery 全集；（b）任何 test 不出现在 ≥ 2 shard

测试用 K=4（任意小 K，算法正确性与具体 K 无关，K=4 跑得快）。**事实源单源**：脚本里 `shard_assignment()` 是唯一 modulo 算法实现，`run_shard()` 与 `LIST_SHARD_TESTS` 路径都调用它；Go 测试不**复制**算法，只**调用**脚本验证算法性质。负 TDD：用 `awk 'NR % (n+1) == s'`（cover-break）替换 → partition 断言报告 ~50 测试未分配，恢复后立即绿。

刻意不验证：CI yaml `_build-lint.yml::matrix.shard: [0..15]` 与同文件 `env: SHARD_COUNT: 16` 的内部一致性——那是 deployment value 漂移，是另一类问题，不在算法正确性范围内。（2026-05-23 amendment：script 默认值已改 `SHARD_COUNT=1`（本地友好），CI yaml 维持 explicit `SHARD_COUNT=16`，两值差异是 by-design，编码"本地 vs CI 上下文"；不再是"应一致"目标，`ARCHTEST-SHARDCOUNT-SYNC-GUARD-01` 同 PR 关闭，详见末尾 §Amendment。）

## K8s 范式对照

| K8s 路径 | 作用 | GoCell 对应 |
|---|---|---|
| `hack/make-rules/verify.sh` | top-level verify dispatcher (continue-on-failure) | `make verify` → `hack/make-rules/verify.sh`（保留）|
| `hack/verify-golangci-lint.sh` | single-process verify-X.sh entry pattern | `hack/verify-archtest.sh`（新形态，无内部并行；并行委托给 CI matrix）|
| Prow per-job parallelism (`pull-kubernetes-verify-*`) | 多 job 拆分长 verify | GHA `matrix.shard: [0..15]`（更轻量等价） |
| No `GOMEMLIMIT` in verify scripts | 内存隔离由 process 边界完成 | 同上，process exit 释放堆 |

GoCell 偏离点：K8s `hack/verify-staticcheck.sh` 已折并入 `verify-golangci-lint.sh`，单进程跑全部 staticcheck。K8s 不面临 GoCell 这种**function-level type-info accumulation**（K8s staticcheck 在 analyzer DAG 内复用 type graph）。GoCell 的 `go test -list` → 函数级 modulo 分片是为 296 个独立 cacheKey 累加场景特化，无直接 K8s 对标；最近的 OSS 范式是 unkeyed/unkey `scripts/shard-test`（package-level），手段相同方向不同。

ref:
- [kubernetes/kubernetes hack/make-rules/verify.sh](https://github.com/kubernetes/kubernetes/blob/master/hack/make-rules/verify.sh)
- [kubernetes/kubernetes hack/verify-golangci-lint.sh](https://github.com/kubernetes/kubernetes/blob/master/hack/verify-golangci-lint.sh)
- [kubernetes/kubernetes build/root/Makefile `verify` target](https://github.com/kubernetes/kubernetes/blob/master/build/root/Makefile)

## Rollback

structural rollback（恢复 single-process）：

1. `.github/workflows/_build-lint.yml` tools shard `pkgs` 改回 `./tools/...`；删 `verify-archtest` job
2. `.github/workflows/governance.yml` 加回 `VERIFY_SKIP: archtest` env、timeout-minutes 改回 5
3. `hack/verify-archtest.sh` 整体 revert 到 single-process body
4. 删 `tools/archtest/archtest_verify_coverage_test.go`

`develop` 立即回到 OOM 状态——这是预期降级。重启 D 路径前必须先重测 Phase 0 baseline 是否仍 24 GB（PR-Φ amortize 落地后可能下降）。

## 实测数据（fix/307 worktree, macOS local）

| 指标 | 改造前 | 改造后（K=16） |
|---|---|---|
| Single shard wall (max) | 70.23s (全跑) | 12.10s |
| Single shard peak RSS (max) | 23.94 GB | 4.22 GB |
| Total serial wall | 70.23s | ~45s (16 shard 串行) |
| Total parallel wall (CI matrix) | — | ~13s (16 shard 并行) |
| PR Check tools shard wall | OOM SIGTERM | < 1 min（archtest 移走） |
| Discovery 函数数 | 296 | 296（一致） |

phase0-baseline.txt 留在 worktree 但不入 PR（一次性 artifact）。

> 注：上表 K=16 是 CI 路径指标。本地 K=1 路径 wall-time 基线参考：Phase 0 改造前无 TestMain 预热实测 70.23s 全跑（23.94 GB peak RSS）；ADR 202605190000 TestMain 预热落地后 `*types.Info` cache 在 K=1 单进程内跨 test function 复用，预计 wall-time 改善，待本地重测更新数值。

## Amendment 2026-05-23: 默认值 SHARD_COUNT 16→1（本地友好）

### 根因

原 ADR D1 落地时 `hack/verify-archtest.sh:47` 默认值 `SHARD_COUNT=16` 与 `.github/workflows/_build-lint.yml::verify-archtest` 的 `env: SHARD_COUNT: 16` 双源真值（仅靠 `# SYNC:` 注释维护），登记 backlog `ARCHTEST-SHARDCOUNT-SYNC-GUARD-01`。

进一步观察：CI 已 explicit 设 `SHARD_COUNT=16`，所以默认值在 CI 路径**根本不被读取**。脚本默认值实际只服务"无显式 caller"场景——即本地开发者一键调用。原默认值 16 是把 CI 的 RSS 约束（GHA 2-core 7 GB）反向耦合到本地默认行为，导致 18-core / 128 GB 本地机器跑 `make verify` / 裸跑脚本时 CPU 满载（K=16 process-isolated 重复 `packages.Load` ~16×，本地无 RSS 约束）。

### 决策

`hack/verify-archtest.sh:47` 默认 `${SHARD_COUNT:-16}` → `${SHARD_COUNT:-1}`。

- CI 路径完全不变：`_build-lint.yml::verify-archtest` 已 explicit `SHARD_COUNT: 16`（GHA 7 GB RSS 约束，K=8 Linux RSS 留余量不足，见 Phase 0 表）。amendment 显式注明 CI 必设。
- 本地路径：`bash hack/verify-archtest.sh` 默认单进程跑（K=1），~300 个 Test* 共享 `typeseval.SharedResolver` 内的 `*types.Info` cache，CPU 工作 ~1× 而非 ~16×。`make verify` 透传链路同步生效。
- 双源 SYNC 消除：CI yaml 与脚本默认值不再要求相等，是 by-design 差异（"CI 上下文"显式表达 vs "本地上下文"默认服务）；`ARCHTEST-SHARDCOUNT-SYNC-GUARD-01` backlog 同 PR 关闭。

### 威胁矩阵重评（per `.claude/rules/gocell/ai-collab.md` §"ADR amendment 落地必查"）

逐行核对原 ADR 论点在 amendment 后的有效性：

| 原论点 | 在 amendment 下是否仍成立 | 补偿措施 |
|--------|-----------------------|---------|
| Phase 0 表「K=16 是首个稳定低于 GHA 7GB OOM 阈值的分片粒度」 | ✅ CI 仍 K=16，论点不变 | 无 |
| §D1「整体重写：discovery + modulo 分片」 | ✅ 算法不变 | 无 |
| §D7 partition exactly-once（K=4 任意小 K 验算法） | ✅ test 显式 set SHARD_COUNT=4，不依赖默认值 | 无 |
| §K8s 范式对照「process-isolated 多 job 拆分」 | ✅ CI 路径不变 | 无 |
| §Rollback「structural rollback 恢复 single-process」 | ✅ rollback 步骤不变；amendment 仅改 default | 无 |
| §实测数据表（K=16） | ✅ CI 指标，全部成立 | 表行已隐含为 CI 指标；amendment 显式 |

无变 ❌/⚠️ 项，amendment 与原文论点正交。

### 新约束 Medium 守卫（同 PR 闭环，per ai-collab.md §"立项硬门槛 ≥ Medium"）

Amendment 改默认 16→1 产生一个新隐式约束：**CI yaml `verify-archtest` job 必须 explicit 在 invocation step 自身 env 中设 `SHARD_COUNT: 16`**（否则 CI 以 K=1 单进程跑全部 archtest，~20 GB peak RSS 撞 GHA 7 GB shard OOM）。

> 落地载体迁移：本节 amendment 落地时 yaml 路径是 `.github/workflows/_build-lint.yml::verify-archtest`。后续 §Amendment 2026-05-23-pr-time-to-nightly 把 matrix 迁到 `.github/workflows/archtest-nightly.yml`，archtest 守卫的 yaml 解析路径同 PR 更新；约束语义不变。

按 ai-collab.md "新引入 Soft → 直接 reject，要求改 ≥ Medium"，此约束**同 PR 内** Medium 化，不允许靠注释维护或 backlog 延期：

- 新增 archtest `ARCHTEST-CI-EXPLICIT-SHARD-COUNT-01`（`tools/archtest/archtest_ci_shard_count_test.go`）：解析 nightly workflow YAML，**step-scoped match-all** 形态——遍历 `jobs.verify-archtest.steps[*]`，对每个 `run` 含 `hack/verify-archtest.sh` 的 step 断言其**自身** `env.SHARD_COUNT == "16"`；缺失、值漂移、无 invocation step 立即 fail。
- Step-scoped 必要性：GHA step env 是 step-scoped——sibling 步骤（如 Build slowgate 步骤）的 env 不传递给 verify-archtest.sh 执行进程；"any step has env=16" 形态会被 sibling shadow 误绿（开发者把 `SHARD_COUNT=16` 错写在 setup step，真实 invocation step 仍漏 env → CI 跑 K=1 → OOM）。
- 形态 Medium：runtime guard via archtest，CI 跑时立即捕获。违反不可通过单边修改 yaml 静默达成——必须同 PR 修改 archtest 或 fixture，diff 可视。
- 6 个 fixture（1 正/5 反）覆盖：正确形状 / 缺 env / 值漂移（K=8 反例）/ 缺 job / sibling step env shadow / no invocation step。

### Backlog 关闭

`ARCHTEST-SHARDCOUNT-SYNC-GUARD-01`（双源真值守卫升级）→ ✅ closed by this amendment：双源 SYNC 假设通过"CI 必 explicit / 本地默认服务"消除；新约束由上节 Medium archtest 守卫。详见 `docs/backlog/20260520/cap-02-metadata-governance.md`。

### 跨载体同步（同 PR 闭环）

- `hack/verify-archtest.sh` line 19-21 header + line 46-47 注释 + 默认值
- `.github/workflows/_build-lint.yml` line 305 SYNC 注释改述
- `CLAUDE.md:78`、`.claude/rules/gocell/ai-collab.md:69` K=16 描述
- `tools/archtest/archtest_ci_shard_count_test.go` 新 Medium 守卫（ARCHTEST-CI-EXPLICIT-SHARD-COUNT-01）
- `docs/backlog/20260520/cap-02-metadata-governance.md` ARCHTEST-SHARDCOUNT-SYNC-GUARD-01 关闭

## Amendment 2026-05-23-pr-time-to-nightly: archtest CI matrix moved to nightly + local parallel fan-out

### 根因

PR-time 16-shard matrix（PR / push 上 `_build-lint.yml::verify-archtest`）累积出三类 brittleness，已在最近 6-8 周反复修：

1. **modulo-shift SLOW**——新 test 函数加入 → discovery 顺序 modulo 重排 → 某个 type-heavy test 被推到不同 shard → 跨 20s slowgate budget。每加一个 test 就可能复发，无根本解。
2. **tagGroup 范本 OOM**——`for tagGroup := range KnownNonDefaultTags() { RunTyped(...) }` 范本被复抄到多处，每次 `RunTyped` 加载全模块 RSS 累积超 GHA 7 GB 上限 SIGTERM（PR #870 + TAGGROUP-LOOP-FORBIDS-RUNTYPED-01 archtest 已防再发）。
3. **GHA runner 平台不稳**——PR #878 最终 merge 时 verify-archtest shard 5 `runner has received a shutdown signal`（spot instance preempt），非 archtest 逻辑失败。16-shard matrix 把单 runner 故障风险放大 16×。

三类合计：archtest 在 PR-time 阻塞流红灯的有效信号噪音比下降。开发者修复路径变为"等 nightly 重跑"或人工 rerun matrix，反馈周期反而延长。

同时 PR #878 把脚本默认 `SHARD_COUNT` 改为 1（本地友好），但实测发现 K=1 单进程跑 605 Test* wall ~5 min；K=N 串行 for-loop 更慢（每 shard 独立 packages.Load 无 cross-shard cache 共享）。本地缺乏快速 archtest 反馈手段。

### 决策

1. **删除 PR-time matrix**——`.github/workflows/_build-lint.yml::verify-archtest` job 整段删除（含 16 个 matrix entries）。`governance.yml::make verify` 的 `VERIFY_SKIP: archtest` env 保留，注释更新为指向新 nightly yaml。

2. **新建 nightly schedule**——`.github/workflows/archtest-nightly.yml`：`cron: '0 18 * * *'`（UTC ≈ 北京 02:00）+ `workflow_dispatch`；16-shard matrix 结构 1:1 复用旧 PR-time job；新增 `alert-on-failure` job 在 `if: failure()` 时用 hosted runner 内置 `gh` CLI 调 `gh issue create` 开 P0 issue（labels: `nightly-failure` / `pri-p0` / `cap-02-metadata-governance`）。

3. **本地 pre-push 跑全量 archtest**——`hack/verify-archtest.sh` 新增"Execution modes"语义：
   - `SHARD_TARGET` 设 → 单 shard 单 process（CI matrix 路径不变）
   - `SHARD_TARGET` 未设 + `SHARD_COUNT=1` → 单 shard 流式输出（local default，PR #878 落地）
   - `SHARD_TARGET` 未设 + `SHARD_COUNT>1` → **并行 fan-out**（background `&` + wait barrier + 每 shard 临时文件收集 stdout，wait 后按 shard 序输出）
   - 旧 K=N 串行 for-loop 模式删除——实测它总比 K=1 慢（每 shard 重新 packages.Load 无 cache 共享）且无 caller 用它。

4. **pre-push 调用 K=4**——`hack/githooks/pre-push` 中替换原 `TEST-TIME-LITERAL-01` 单条采样为 `env SHARD_COUNT=4 bash hack/verify-archtest.sh`。

### Workstation 标定（K 值选择实证）

18-core / 128 GB Apple Silicon / macOS / ~605 Test*：

| K | real wall | max shard | peak RSS | 评估 |
|---|----------|-----------|----------|------|
| K=1 单进程 | ~5min（用户实测）| 单进程 | ~20 GB | cache 共享但无并发 |
| K=2 并行 | 248s（4min 8s）| 246s（302 tests）| 51 GB | 每 shard 太大 |
| **K=4 并行** | **187s（3min 7s）**| **185s（152 tests）** | **43 GB** | **sweet spot**|
| K=8 并行 | ~196s（3min 16s）| 196s（76 tests）| ~67 GB | 每 shard go test 内 `-p` 取不到足够 core，反而慢 |

K=4 全胜：18-core 给 4 process 各 ~4.5 core，`go test` 内 `t.Parallel` 充分用 CPU；K=2 单 shard tests 太多内部串行多；K=8 over-subscribe core 拖慢。

### 威胁矩阵重评（per `.claude/rules/gocell/ai-collab.md` §"ADR amendment 落地必查"）

| 原 ADR 论点 | 在 amendment 下状态 | 补偿措施 |
|------------|------------------|---------|
| §D1 CI matrix 16-shard | ✅ K=16 不变，载体 `_build-lint.yml` → `archtest-nightly.yml` | ARCHTEST-CI-EXPLICIT-SHARD-COUNT-01 守卫 yaml 路径同 PR 迁移 |
| §D3 ARCHTEST-VERIFY-COVERAGE-01 元守卫 | ✅ 不变，运行时间从 PR-time → nightly | discovery drift 触发条件单一（改脚本），PR diff 显著 |
| §D4 governance.yml VERIFY_SKIP | ✅ env 保留不变 | 注释更新指向新 nightly yaml |
| §D5 slowgate | ⚠️ 仍跑但延迟暴露 | brittleness 转 nightly 兜底 + 自动开 issue |
| § "PR-time fast-feedback gate" | ❌ 失效 | pre-push 本地 K=4 并行替代（实测 ~3 min wall） |
| § "single authoritative owner" | ✅ owner 从 `_build-lint.yml::verify-archtest` 平移至 `archtest-nightly.yml::verify-archtest`（同名 job，不同 yaml）| 注释 + ADR 文本同 PR 重写 |

§D4 / §D6 段原文同 PR 重写以与现状一致（per ai-collab.md §"ADR amendment 落地必查" 禁止"原文保留作历史脉络"）。

### 跨载体同步（同 PR 闭环）

- `.github/workflows/archtest-nightly.yml` 新建（16-shard schedule + alert-on-failure）
- `.github/workflows/_build-lint.yml` 删 verify-archtest job + tools shard 注释指向 nightly
- `.github/workflows/governance.yml` VERIFY_SKIP 注释指向 nightly
- `hack/verify-archtest.sh` "Execution modes" 文档 + 并行 fan-out 分支
- `hack/githooks/pre-push` 用 K=4 并行 + 注释重写（deviation 4 / Tier 4）
- `tools/archtest/archtest_ci_shard_count_test.go` yaml 路径迁移到 `archtest-nightly.yml`
- `CLAUDE.md:78`、`.claude/rules/gocell/ai-collab.md:69` archtest 入口描述更新
