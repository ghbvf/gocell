# ADR: Archtest CI 入口 process-isolated sharding（CI K=24 / 本地默认 K=1）

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

> 表格是 macOS Apple Silicon 历史 baseline，标定了"per-shard RSS 单调随 K 增加而降低"的方向性论点。原始结论 "K=16 是首个稳定低于 GHA 7GB OOM 阈值的分片粒度" 是该方向性论点的 **macOS-本地** 论证；K=8 macOS 6.30 GB 留余量不足是 macOS 实测，Linux RSS 通常高于 macOS RSS。**当前 CI 值是 K=24**（§Amendment 2026-05-28），是同一方向性论点向更细分片的延伸（per-shard tests 数从 47 降到 32，per-shard RSS 必然 ≤ K=16）。K=24 GHA Linux baseline 未在表中——待 §Amendment 2026-05-28 §D3 Phase A 诊断 step 收集 `/proc/meminfo` + `dmesg` 后回填。本节描述以"方向性论点"为权威，**不以表中 K=16 行**为当前 CI 值的真值源。

## Decisions

### D1. CI 入口改为 process-isolated 24-shard 矩阵（CI 显式 K=24 / 本地默认 K=1）

`hack/verify-archtest.sh` 整体重写：discovery via `go test -list '^Test' ./tools/archtest`，按字母序 modulo `SHARD_COUNT` 分片（**CI: 24 explicit；本地默认: 1**，见 §Amendment 2026-05-23 + §Amendment 2026-05-28），每 shard 独立 `go test -run '^(name1|name2|...)$'` 调用。三种 execution mode 由 env shape 派生（见 §Amendment 2026-05-23-pr-time-to-nightly §决策 3）：`SHARD_TARGET` 设 → 单 shard（GHA matrix）；`SHARD_COUNT=1` 无 `SHARD_TARGET` → 单进程流式（本地 `make verify` 默认）；`SHARD_COUNT>1` 无 `SHARD_TARGET` → 并行 fan-out（background `&` + wait barrier）。旧 K=N 串行 for-loop 已删（每 shard 重 packages.Load 比 K=1 慢）。

`.github/workflows/archtest-nightly.yml` 单一 `verify-archtest` job：`matrix.shard: [0..23]` + 显式 `env: SHARD_COUNT: 24`（GHA 7 GB shard RSS 约束）；每 shard 独立 ubuntu-latest runner。`fail-fast: false` 对齐 K8s `hack/make-rules/verify.sh` continue-on-failure 范式。CI explicit `SHARD_COUNT=24` 由 `ARCHTEST-CI-EXPLICIT-SHARD-COUNT-01` archtest 守卫（见 §Amendment 2026-05-28）。

> 历史：本节原文为 "无 SHARD_TARGET 时串行跑 SHARD_COUNT 个 shard" + "`.github/workflows/_build-lint.yml` 新增 `verify-archtest` job" + `K=16 / matrix.shard: [0..15]`。§Amendment 2026-05-23-pr-time-to-nightly §决策 3 删 K=N 串行 for-loop 并新增 "Execution modes" 三档，§决策 1 把 verify-archtest job 整段从 `_build-lint.yml` 迁到 `archtest-nightly.yml`。§Amendment 2026-05-28 把 K=16 提升为 K=24（per-shard RSS 方向性延伸 + slowgate threshold 20s → 25s）。本节同 PR 重写（per ai-robust.md §"ADR amendment 落地必查"）。

### D2. tools shard 不再 enumerate archtest，pkgs 运行时计算

`.github/workflows/_build-lint.yml` tools shard `pkgs` 改为 sentinel `_dynamic_archtest_excluded`；Test step 用 `go list ./tools/...` + grep 排除 archtest **顶层包**（`github.com/ghbvf/gocell/tools/archtest`）。`tools/archtest/internal/{scanner,typeseval,rawparamfixture,wrapfixture}` 子包保留在 tools shard 执行——它们的测试是轻量单元测试（11+3+0+0 \_test.go），不触发 `typeseval.SharedResolver` 的 type graph 累加（296 archtest 顶层函数才是 OOM 触发源）。

**AI-robust 评级**：按 `.claude/rules/gocell/ai-robust.md` 载体定义严格分类为 **Medium**（shell runtime guard + go list subprocess + grep 过滤），不是 codegen funnel / type system / sealed interface 的 Hard。但从覆盖保证角度等效 Hard：「新 `tools/<sub>` 包被遗漏」**在 type system 不可表达**——`go list ./tools/...` 是 ground truth，新包自动入列，archtest 是唯一显式排除目标。AI 仍能通过手工改 yml 回硬编码列表绕过 runtime 计算，但这种回退要改 yml + 改注释 + 通过 diff review，可观测性足够。在本 PR 范围内接受为 **Hard-effective**，不立 archtest 元规则守卫（守卫本身也是 Soft 的字符串扫，反而开倒车）。

### D3. discovery vs AST 一致性元规则

`tools/archtest/archtest_verify_coverage_test.go::TestArchtestVerifyCoverage01`（INVARIANT `ARCHTEST-VERIFY-COVERAGE-01`）：shell-out `DRY_RUN=1 bash hack/verify-archtest.sh` → 与 `scanner.EachInSubtree[ast.FuncDecl]` AST 扫到的 top-level Test* 函数集合做对称 diff，非空则 fail。守的风险：维护者改脚本加 `grep -v TestFoo` debug 过滤忘删 → CI silent unenforce（local `go test ./tools/archtest/...` 仍捕获，但 PR Check 漏过）。AI-robust **Medium**（runtime cross-check 双重源）。

### D4. `make verify` 委托 archtest 给 nightly gate（D6 single-owner 落地形态）

`.github/workflows/governance.yml::make verify` 保留 `env: VERIFY_SKIP: archtest` 显式委托给 `archtest-nightly.yml::verify-archtest` matrix gate，避免 push/PR 上重复跑 archtest（详见 §D6 single-owner 原则）。timeout-minutes 维持 15 容纳其它 verify-*.sh 子脚本耗时。

本地 `make verify`（无 `VERIFY_SKIP` env）仍包含 `verify-archtest.sh`，按 `SHARD_COUNT` 默认 K=1 单进程跑（见 §D1 + §Amendment 2026-05-23）；CI 上 `archtest-nightly.yml::verify-archtest` (schedule cron + workflow_dispatch，SHARD_COUNT=24 explicit per §Amendment 2026-05-28) 是唯一权威 archtest gate。

> **历史**：本节原文先后经历："governance.yml 删 VERIFY_SKIP env" + "verify-archtest.sh serial 16-shard" → §D6 加入时反转（恢复 VERIFY_SKIP 避免双跑）→ §Amendment 2026-05-23 把脚本默认 K 改为 1 → §Amendment 2026-05-23-pr-time-to-nightly 把 owner 从 `_build-lint.yml::verify-archtest` 平移至 `archtest-nightly.yml::verify-archtest`。本节同 PR 重写（per ai-robust.md §"ADR amendment 落地必查"）。

### D5. slowgate 重接

`hack/verify-archtest.sh` 内 shard 路径：若 `$SLOWGATE_BIN` executable，`go test ... -json -run '...'` tee 到 `$RUNNER_TEMP/archtest-shard-N.json` 再管道入 slowgate（与 `_build-lint.yml` 旧 tools shard 同范式）；否则 plain `go test`（local dev）。matrix job 内 `go build -o "$RUNNER_TEMP/slowgate" ./tools/slowgate` 后注入 env；`if: failure()` artifact 上传保留 json event stream 供失败诊断。

### D6. Single-owner 原则：nightly schedule is the sole archtest gate on CI

`.github/workflows/archtest-nightly.yml::verify-archtest` matrix（24 shard per §Amendment 2026-05-28，cron + `workflow_dispatch`）是 archtest 在 CI 上的 **唯一权威 gate**。push / pull_request 不再跑 archtest（PR-time matrix 已删，详见 §Amendment 2026-05-23-pr-time-to-nightly）。`governance.yml::make verify` 通过 `env: VERIFY_SKIP: archtest` 显式委托给 nightly，**不再双跑**。

> **§Amendment 2026-06-10 补强**：`VERIFY_SKIP=archtest` 只 skip 专用的 `verify-archtest.sh` gate，**不**覆盖对含 archtest 的 module 跑裸 `go test ./...` 的其它 gate。#1803 把 `tools/` 拆成 workspace 成员后，`verify-workspace-test.sh` 的通用遍历重新执行了 archtest（+~4min/lane），证明本段「push/PR 不再跑 archtest」仅靠 VERIFY_SKIP 并不成立。真正的编译级保证是 leaf 的 `//go:build archtest` tag（`ARCHTEST-LEAF-BUILD-TAG-01`）；VERIFY_SKIP 与 build tag 互补。详见末尾 §Amendment 2026-06-10。

理由（K8s + Watermill 范式对照）：
- K8s 每个 verify-*.sh 是独立 Prow job（一 owner / 一 gate）；aggregator `hack/make-rules/verify.sh` 是开发者本地一键入口，不是 CI 上的二次 gate
- Watermill 用单个 reusable workflow 作为 PR/master 共同实现，调用方只做薄包装；语义差异通过显式 input 表达，不靠 caller-injected env 改 script 行为
- 若 governance 在 push / PR 上也跑 archtest：(a) CI 资源 ×2；(b) 同 script 在两个 caller 下行为分叉（matrix 注入 SLOWGATE_BIN 有 budget 门，governance 无）—— 同名 gate 双契约破坏 reproducibility

本地路径：
- 开发者本地 `make verify`（无 `VERIFY_SKIP` env）仍调 `hack/verify-archtest.sh`，作为一键全跑
- `hack/githooks/pre-push` 因 CPU/RSS 预算（实测 ~3min wall + 43 GB RSS + 18-core 全打满，违反 sub-10s 预算 18×）不跑 archtest（详见 §"pre-push archtest 撤回"）；开发者要 PR-time archtest 反馈走 `make verify` 或 `bash hack/verify-archtest.sh` 显式触发
- 脚本因 `SLOWGATE_BIN` 缺失走 plain go test 路径——by-design 单本地路径（slowgate budget gate 是 CI 关注点，本地 dev 关注正确性）
- 故 script 仍有「`SLOWGATE_BIN` 在则 pipe；不在则 plain」的内部分支，但 CI 只有一个 caller（nightly matrix）注入 `SLOWGATE_BIN`，没有 caller-divergent 契约

> 历史：本节原文为 "`_build-lint.yml::verify-archtest` matrix 是 push / pull_request 上的唯一权威 archtest gate"。§Amendment 2026-05-23-pr-time-to-nightly 把 owner 平移至 `archtest-nightly.yml`；本节文本同 PR 重写（per ai-robust.md §"ADR amendment 落地必查"，禁止"原文保留作历史脉络"）。

### D7. 元规则覆盖 dispatch 路径，不仅 discovery

`tools/archtest/archtest_verify_coverage_test.go::TestArchtestVerifyCoverage01` 包含两条断言：

1. **Discovery cross-check**（原 D3）：`DRY_RUN=1 bash hack/verify-archtest.sh` 输出 == AST scan `tools/archtest/*_test.go` 中 top-level `func TestX(t *testing.T)` 集合
2. **Partition exactly-once**（新增）：`LIST_SHARD_TESTS=1 SHARD_COUNT=k SHARD_TARGET=s bash hack/verify-archtest.sh` 对 s ∈ [0, k) 各 emit 一次该 shard 的 assignment，断言（a）所有 shard 并集 == discovery 全集；（b）任何 test 不出现在 ≥ 2 shard

测试用 K=4（任意小 K，算法正确性与具体 K 无关，K=4 跑得快）。**事实源单源**：脚本里 `shard_assignment()` 是唯一 modulo 算法实现，`run_shard()` 与 `LIST_SHARD_TESTS` 路径都调用它；Go 测试不**复制**算法，只**调用**脚本验证算法性质。负 TDD：用 `awk 'NR % (n+1) == s'`（cover-break）替换 → partition 断言报告 ~50 测试未分配，恢复后立即绿。

刻意不验证：CI yaml `archtest-nightly.yml::matrix.shard: [0..23]` 与同文件 `env: SHARD_COUNT: 24` 的内部一致性——历史上是 deployment value 漂移问题。2026-05-23 amendment：script 默认值已改 `SHARD_COUNT=1`（本地友好），CI yaml 维持 explicit `SHARD_COUNT`，两值差异是 by-design，编码"本地 vs CI 上下文"；`ARCHTEST-SHARDCOUNT-SYNC-GUARD-01` 同 PR 关闭。**§Amendment 2026-05-28 重新接入此一致性验证**：`ARCHTEST-CI-EXPLICIT-SHARD-COUNT-01` 现同步校验 `matrix.shard` 必须等于 `[0..expectedShardCount-1]` 的连续序列，防止"SHARD_COUNT 改 24 但 matrix 仍 [0..15]"导致 8 shards' tests silent 不跑的回归（K=16→24 演化场景下 deployment value 一致性回归首次有机器保证）。

## K8s 范式对照

| K8s 路径 | 作用 | GoCell 对应 |
|---|---|---|
| `hack/make-rules/verify.sh` | top-level verify dispatcher (continue-on-failure) | `make verify` → `hack/make-rules/verify.sh`（保留）|
| `hack/verify-golangci-lint.sh` | single-process verify-X.sh entry pattern | `hack/verify-archtest.sh`（新形态，无内部并行；并行委托给 CI matrix）|
| Prow per-job parallelism (`pull-kubernetes-verify-*`) | 多 job 拆分长 verify | GHA `matrix.shard: [0..23]`（更轻量等价，§Amendment 2026-05-28） |
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

## 实测数据（fix/307 worktree, macOS local — historical K=16 baseline）

| 指标 | 改造前 | 改造后（K=16，历史 baseline） |
|---|---|---|
| Single shard wall (max) | 70.23s (全跑) | 12.10s |
| Single shard peak RSS (max) | 23.94 GB | 4.22 GB |
| Total serial wall | 70.23s | ~45s (16 shard 串行) |
| Total parallel wall (CI matrix) | — | ~13s (16 shard 并行) |
| PR Check tools shard wall | OOM SIGTERM | < 1 min（archtest 移走） |
| Discovery 函数数 | 296 | 296（一致） |

phase0-baseline.txt 留在 worktree 但不入 PR（一次性 artifact）。

> 注：上表 K=16 是 **fix/307 时的 CI 路径 macOS 指标，已转 historical baseline**。当前 CI 值是 K=24（§Amendment 2026-05-28）；K=24 GHA Linux baseline 待 §Amendment 2026-05-28 §D3 Phase A 诊断 step 收据后回填实测值。本地 K=1 路径 wall-time 基线参考：Phase 0 改造前无 TestMain 预热实测 70.23s 全跑（23.94 GB peak RSS）；ADR 202605190000 TestMain 预热落地后 `*types.Info` cache 在 K=1 单进程内跨 test function 复用，预计 wall-time 改善，待本地重测更新数值。

## Amendment 2026-05-23: 默认值 SHARD_COUNT 16→1（本地友好）

### 根因

原 ADR D1 落地时 `hack/verify-archtest.sh:47` 默认值 `SHARD_COUNT=16` 与 `.github/workflows/_build-lint.yml::verify-archtest` 的 `env: SHARD_COUNT: 16` 双源真值（仅靠 `# SYNC:` 注释维护），登记 backlog `ARCHTEST-SHARDCOUNT-SYNC-GUARD-01`。

进一步观察：CI 已 explicit 设 `SHARD_COUNT=16`，所以默认值在 CI 路径**根本不被读取**。脚本默认值实际只服务"无显式 caller"场景——即本地开发者一键调用。原默认值 16 是把 CI 的 RSS 约束（GHA 2-core 7 GB）反向耦合到本地默认行为，导致 18-core / 128 GB 本地机器跑 `make verify` / 裸跑脚本时 CPU 满载（K=16 process-isolated 重复 `packages.Load` ~16×，本地无 RSS 约束）。

### 决策

`hack/verify-archtest.sh:47` 默认 `${SHARD_COUNT:-16}` → `${SHARD_COUNT:-1}`。

- CI 路径完全不变：`_build-lint.yml::verify-archtest` 已 explicit `SHARD_COUNT: 16`（GHA 7 GB RSS 约束，K=8 Linux RSS 留余量不足，见 Phase 0 表）。amendment 显式注明 CI 必设。
- 本地路径：`bash hack/verify-archtest.sh` 默认单进程跑（K=1），~300 个 Test* 共享 `typeseval.SharedResolver` 内的 `*types.Info` cache，CPU 工作 ~1× 而非 ~16×。`make verify` 透传链路同步生效。
- 双源 SYNC 消除：CI yaml 与脚本默认值不再要求相等，是 by-design 差异（"CI 上下文"显式表达 vs "本地上下文"默认服务）；`ARCHTEST-SHARDCOUNT-SYNC-GUARD-01` backlog 同 PR 关闭。

### 威胁矩阵重评（per `.claude/rules/gocell/ai-robust.md` §"ADR amendment 落地必查"）

逐行核对原 ADR 论点在 amendment 后的有效性：

| 原论点 | 在 amendment 下是否仍成立 | 补偿措施 |
|--------|-----------------------|---------|
| Phase 0 表「K=16 是首个稳定低于 GHA 7GB OOM 阈值的分片粒度」 | ✅ 2026-05-23 时点 CI 仍 K=16，论点不变。**§Amendment 2026-05-28 升至 K=24**：方向性论点延伸成立（K=24 < K=16 < 7GB），不变。 | 无 |
| §D1「整体重写：discovery + modulo 分片」 | ✅ 算法不变 | 无 |
| §D7 partition exactly-once（K=4 任意小 K 验算法） | ✅ test 显式 set SHARD_COUNT=4，不依赖默认值 | 无 |
| §K8s 范式对照「process-isolated 多 job 拆分」 | ✅ CI 路径不变 | 无 |
| §Rollback「structural rollback 恢复 single-process」 | ✅ rollback 步骤不变；amendment 仅改 default | 无 |
| §实测数据表（K=16） | ✅ 2026-05-23 时点 CI 指标全部成立。**§Amendment 2026-05-28 升至 K=24**：表行已转 historical baseline，GHA Linux baseline 待 §Amendment 2026-05-28 §D3 Phase A 诊断收据 | 表行已隐含为 CI 指标；amendment 显式 |

无变 ❌/⚠️ 项，amendment 与原文论点正交。

### 新约束 Medium 守卫（同 PR 闭环，per ai-robust.md §"立项硬门槛 ≥ Medium"）

Amendment 改默认 16→1 产生一个新隐式约束：**CI yaml `verify-archtest` job 必须 explicit 在 invocation step 自身 env 中设 `SHARD_COUNT: 16`**（否则 CI 以 K=1 单进程跑全部 archtest，~20 GB peak RSS 撞 GHA 7 GB shard OOM）。

> 落地载体迁移：本节 amendment 落地时 yaml 路径是 `.github/workflows/_build-lint.yml::verify-archtest`。后续 §Amendment 2026-05-23-pr-time-to-nightly 把 matrix 迁到 `.github/workflows/archtest-nightly.yml`，archtest 守卫的 yaml 解析路径同 PR 更新；约束语义不变。

按 ai-robust.md "新引入 Soft → 直接 reject，要求改 ≥ Medium"，此约束**同 PR 内** Medium 化，不允许靠注释维护或 backlog 延期：

- 新增 archtest `ARCHTEST-CI-EXPLICIT-SHARD-COUNT-01`（`tools/archtest/archtest_ci_shard_count_test.go`）：解析 nightly workflow YAML，**step-scoped match-all** 形态——遍历 `jobs.verify-archtest.steps[*]`，对每个 `run` 含 `hack/verify-archtest.sh` 的 step 断言其**自身** `env.SHARD_COUNT == expectedShardCount`（2026-05-23 时为 `"16"`；§Amendment 2026-05-28 升至 `"24"`，由 const `expectedShardCount` 单源化）；缺失、值漂移、无 invocation step 立即 fail。
- Step-scoped 必要性：GHA step env 是 step-scoped——sibling 步骤（如 Build slowgate 步骤）的 env 不传递给 verify-archtest.sh 执行进程；"any step has env=24" 形态会被 sibling shadow 误绿（开发者把 `SHARD_COUNT=24` 错写在 setup step，真实 invocation step 仍漏 env → CI 跑 K=1 → OOM）。
- 形态 Medium：runtime guard via archtest，CI 跑时立即捕获。违反不可通过单边修改 yaml 静默达成——必须同 PR 修改 archtest 或 fixture，diff 可视。
- Fixture 覆盖（2026-05-23 时点 6: 1 正/5 反 — 正确形状 / 缺 env / 值漂移 / 缺 job / sibling step env shadow / no invocation step；**§Amendment 2026-05-28 扩为 8: 1 正/7 反**，新增三个 matrix 维度反例：missing-matrix / length-mismatch / non-contiguous-matrix，覆盖 K=24 + matrix 持续 [0..15] 这类 deployment 同步漏改场景）。

### Backlog 关闭

`ARCHTEST-SHARDCOUNT-SYNC-GUARD-01`（双源真值守卫升级）→ ✅ closed by this amendment：双源 SYNC 假设通过"CI 必 explicit / 本地默认服务"消除；新约束由上节 Medium archtest 守卫。详见 `docs/backlog/20260520/cap-02-metadata-governance.md`。

### 跨载体同步（同 PR 闭环）

- `hack/verify-archtest.sh` line 19-21 header + line 46-47 注释 + 默认值
- `.github/workflows/_build-lint.yml` line 305 SYNC 注释改述
- `CLAUDE.md:78`、`.claude/rules/gocell/ai-robust.md:69` K=16 描述
- `tools/archtest/archtest_ci_shard_count_test.go` 新 Medium 守卫（ARCHTEST-CI-EXPLICIT-SHARD-COUNT-01）
- `docs/backlog/20260520/cap-02-metadata-governance.md` ARCHTEST-SHARDCOUNT-SYNC-GUARD-01 关闭

## Amendment 2026-05-23-pr-time-to-nightly: archtest CI matrix moved to nightly + local parallel fan-out

### 根因

PR-time 16-shard matrix（PR / push 上 `_build-lint.yml::verify-archtest`）累积出三类 brittleness，已在最近 6-8 周反复修：

1. **modulo-shift SLOW**——新 test 函数加入 → discovery 顺序 modulo 重排 → 某个 type-heavy test 被推到不同 shard → 跨 20s slowgate budget。每加一个 test 就可能复发，无根本解。
2. **tagGroup 范本 OOM**——`for tagGroup := range KnownNonDefaultTags() { RunTyped(...) }` 范本被复抄到多处，每次 `RunTyped` 加载全模块 RSS 累积超 GHA 7 GB 上限 SIGTERM（PR #870 + TAGGROUP-LOOP-FORBIDS-TYPED-RUN-01 archtest 已防再发）。
3. **GHA runner 平台不稳**——PR #878 最终 merge 时 verify-archtest shard 5 `runner has received a shutdown signal`（spot instance preempt），非 archtest 逻辑失败。16-shard matrix 把单 runner 故障风险放大 16×。

三类合计：archtest 在 PR-time 阻塞流红灯的有效信号噪音比下降。开发者修复路径变为"等 nightly 重跑"或人工 rerun matrix，反馈周期反而延长。

同时 PR #878 把脚本默认 `SHARD_COUNT` 改为 1（本地友好），但实测发现 K=1 单进程跑 605 Test* wall ~5 min；K=N 串行 for-loop 更慢（每 shard 独立 packages.Load 无 cross-shard cache 共享）。本地缺乏快速 archtest 反馈手段。

### 决策

1. **删除 PR-time matrix**——`.github/workflows/_build-lint.yml::verify-archtest` job 整段删除（含 16 个 matrix entries）。`governance.yml::make verify` 的 `VERIFY_SKIP: archtest` env 保留，注释更新为指向新 nightly yaml。

2. **新建 nightly schedule**——`.github/workflows/archtest-nightly.yml`：`cron: '0 18 * * *'`（UTC ≈ 北京 02:00）+ `workflow_dispatch`；16-shard matrix 结构 1:1 复用旧 PR-time job。失败兜底走 GHA 平台自带反馈通道（workflow run 失败默认邮件通知 watchers、Actions UI 红 ✗、`workflow_dispatch` 手动 rerun），**不**自动开 issue——见下方 §"alert-on-failure 撤回"。

3. **`hack/verify-archtest.sh` 新增"Execution modes"语义**——为开发者本地手动触发与未来 caller 提供 K 值选择：
   - `SHARD_TARGET` 设 → 单 shard 单 process（CI matrix 路径不变）
   - `SHARD_TARGET` 未设 + `SHARD_COUNT=1` → 单 shard 流式输出（local default，PR #878 落地）
   - `SHARD_TARGET` 未设 + `SHARD_COUNT>1` → **并行 fan-out**（background `&` + wait barrier + 每 shard 临时文件收集 stdout，wait 后按 shard 序输出）
   - 旧 K=N 串行 for-loop 模式删除——实测它总比 K=1 慢（每 shard 重新 packages.Load 无 cache 共享）且无 caller 用它。

4. **pre-push 不跑 archtest**——初版决策曾把 `env SHARD_COUNT=4 bash hack/verify-archtest.sh` 接入 `hack/githooks/pre-push` 作为 PR-time 快速反馈替代，PR #887 review round-2 撤回，详见下方 §"pre-push archtest 撤回"。本地 archtest 反馈走 `make verify` 一键全跑或 `bash hack/verify-archtest.sh` 显式触发。

### Workstation 标定（K 值选择实证）

18-core / 128 GB Apple Silicon / macOS / ~605 Test*：

| K | real wall | max shard | peak RSS | 评估 |
|---|----------|-----------|----------|------|
| K=1 单进程 | ~5min（用户实测）| 单进程 | ~20 GB | cache 共享但无并发 |
| K=2 并行 | 248s（4min 8s）| 246s（302 tests）| 51 GB | 每 shard 太大 |
| **K=4 并行** | **187s（3min 7s）**| **185s（152 tests）** | **43 GB** | **sweet spot**|
| K=8 并行 | ~196s（3min 16s）| 196s（76 tests）| ~67 GB | 每 shard go test 内 `-p` 取不到足够 core，反而慢 |

K=4 全胜：18-core 给 4 process 各 ~4.5 core，`go test` 内 `t.Parallel` 充分用 CPU；K=2 单 shard tests 太多内部串行多；K=8 over-subscribe core 拖慢。

### 威胁矩阵重评（per `.claude/rules/gocell/ai-robust.md` §"ADR amendment 落地必查"）

| 原 ADR 论点 | 在 amendment 下状态 | 补偿措施 |
|------------|------------------|---------|
| §D1 CI matrix 16-shard | ✅ K=16 不变，载体 `_build-lint.yml` → `archtest-nightly.yml` | ARCHTEST-CI-EXPLICIT-SHARD-COUNT-01 守卫 yaml 路径同 PR 迁移 |
| §D3 ARCHTEST-VERIFY-COVERAGE-01 元守卫 | ✅ 不变，运行时间从 PR-time → nightly | discovery drift 触发条件单一（改脚本），PR diff 显著 |
| §D4 governance.yml VERIFY_SKIP | ✅ env 保留不变 | 注释更新指向新 nightly yaml |
| §D5 slowgate | ⚠️ 仍跑但延迟暴露 | brittleness 转 nightly 兜底；失败靠 GHA 平台邮件 + Actions UI + `workflow_dispatch` rerun |
| § "PR-time fast-feedback gate" | ❌ 失效（无替代） | 开发者本地按需 `make verify` 或 `bash hack/verify-archtest.sh`；nightly ≤24h 兜底。round-1 曾用 pre-push K=4 fan-out 替代，因 ~3min wall + 43 GB RSS + 18-core 全打满破 sub-10s 预算 round-2 撤回 |
| § "single authoritative owner" | ✅ owner 从 `_build-lint.yml::verify-archtest` 平移至 `archtest-nightly.yml::verify-archtest`（同名 job，不同 yaml）| 注释 + ADR 文本同 PR 重写 |

§D4 / §D6 段原文同 PR 重写以与现状一致（per ai-robust.md §"ADR amendment 落地必查" 禁止"原文保留作历史脉络"）。

### 跨载体同步（同 PR 闭环）

- `.github/workflows/archtest-nightly.yml` 新建（16-shard schedule + `workflow_dispatch`；alert-on-failure 自动开 issue 子方案见 §"alert-on-failure 撤回"）
- `.github/workflows/_build-lint.yml` 删 verify-archtest job + tools shard 注释指向 nightly
- `.github/workflows/governance.yml` VERIFY_SKIP 注释指向 nightly
- `hack/verify-archtest.sh` "Execution modes" 文档 + 并行 fan-out 分支
- `hack/githooks/pre-push` 撤回 archtest 调用与 governance trigger（详见 §"pre-push archtest 撤回"），保留 gofumpt / build / vet / golangci-lint / codegen-verify 等 sub-10s 友好 gate；deviation 4 重号为 golangci-lint，Tier 4 同步重号
- `tools/archtest/archtest_ci_shard_count_test.go` yaml 路径迁移到 `archtest-nightly.yml`
- `CLAUDE.md`、`.claude/rules/gocell/ai-robust.md` archtest 入口描述更新

### alert-on-failure 撤回（PR #887 review round）

初版决策 2 同时包含一个 `alert-on-failure` job：`if: failure()` 时调 `gh issue create` 自动开 P0 issue（labels `nightly-failure` / `pri-p0` / `cap-02-metadata-governance`，同标题 idempotency skip-if-exists）。PR #887 review 找到三处缺陷：

1. **`issues: write` 顶层泄漏**——workflow 顶层 `permissions: issues: write` 被 verify-archtest 16 个 matrix job 继承，权限面不必要扩大
2. **shell injection 面**——`branch="${{ github.ref_name }}"` 把 GHA expression 直接嵌进 shell，`workflow_dispatch` 触发的分支名可承载 shell 元字符
3. **alert job 无 repo context**——既无 checkout 也无 `GH_REPO` env，`gh issue list / create / label create` 在非 git 工作目录会失败，整条"补偿路径"本身失效

激进自审三层（per ai-robust.md §"激进自审三层覆盖"）：

- **L1 代码补丁**：F1+F2+F3 是给同一脆弱组件打三个补丁，治标不治本
- **L2 PR 整体决策组合**：自动开 issue 兜底是冗余运维债——GHA workflow run 失败默认邮件通知 watchers、Actions UI 红 ✗ 显示、`workflow_dispatch` 手动 rerun 已构成三条反馈通道；issue 语义是"工作项跟踪"，与 alert 通道语义错配；同标题 idempotency 导致同一 issue 长期开着反而失去信号
- **L3 概念模型**：GitHub Issues 不是 alert backbone；真正的 alert 应走 Slack / PagerDuty webhook（语义正确的 alert 通道）

裁决：同 PR 内撤回 alert-on-failure job 整段 + 顶层 `issues: write` 权限。nightly 失败靠 GHA 平台自带反馈。未来若真需要 alert backbone，使用 webhook 形式，不回到 GitHub Issues。

### pre-push archtest 撤回（PR #887 review round-2）

初版决策 4 把 `env SHARD_COUNT=4 bash hack/verify-archtest.sh` 接入 `hack/githooks/pre-push` 作为 PR-time 快速反馈替代（18-core / 128 GB workstation 标定 K=4 wall ~3 min / RSS 43 GB）。PR #887 review round-2 复测发现实际开发场景下：

- 18-core 在 archtest 跑期间 100% 打满，与开发者其他并行任务（IDE 索引、其它 build、agent）冲突
- 43 GB peak RSS 在 64 GB 机器上接近内存上限，触发 swap
- pre-push 头部注释自承"breaches the sub-10s budget by 18×, but accepted"——一个 sub-10s deterministic hook 不应承载 ~3min 的重 gate

激进自审三层（per ai-robust.md §"激进自审三层覆盖"）：

- **L1 代码补丁**：F4（governance trigger）+ F5（SHARD_COUNT 可配置）是对一个本身不该存在的接入打两个补丁
- **L2 PR 整体决策组合**：pre-push hook 设计目标是 sub-10s deterministic offline gate（其头部第一段明确）；把 ~3min CPU/RSS 重 gate 隐式塞进每次 `git push` 违反这个设计契约；开发者用 `git push --no-verify` 绕过的代价反向使 pre-push 形成 anti-pattern
- **L3 概念模型**：archtest 是开发者主动触发的合规体检，不是隐式 push gate；正确语义是 explicit `make verify` / `bash hack/verify-archtest.sh`（开发者承担 CPU/RSS 代价）+ nightly 兜底（≤24h，无开发者代价）

裁决：同 PR 内撤回 pre-push archtest 调用 + `archtest_governance_changed` 触发探测 + 头部 deviation 4 / Tier 4 标号；`hack/verify-archtest.sh` "Execution modes" 与并行 fan-out 能力保留（为 `make verify` 与显式调用提供选择）。未来若 CPU/RSS 代价显著下降（更小 archtest 集 / cross-shard cache）再评估接回 pre-push 的可能性。

## Amendment 2026-05-28: K=16 → K=24 + SLOWGATE_THRESHOLD 20s → 25s

### 触发

2026-05-24 至 2026-05-27 连续 4 天 `archtest-nightly` workflow 失败，混合两类信号：

1. **真实 slowgate breach（持续问题）**
   - 2026-05-25 shard 7: `TestPGRepoAmbientTx_SelfCheck 23.66s > 20s`
   - 2026-05-27 shard 10: `TestSagaJournalConformanceEnrollment 21.77s > 20s`
   - 共性：均为 type-graph-load 类（`packages.Load` 全模块 type-aware walk），单 test wall-time 直接超 20s budget；与 SHARD_COUNT / SharedResolver cache 摊销无关。
   - `archtest-nightly.yml` line 63-65 工作流 godoc 第一性原理已预留 revisit 触发：
     > "20s threshold inherited from `_build-lint.yml`; calibrated on macOS local Phase 0 (18-core Apple Silicon). GHA ubuntu nightly has no baseline data yet — revisit after ~30 consecutive nightly runs."
   - 当前 archtest-nightly 自 §Amendment 2026-05-23-pr-time-to-nightly 起累计 ~5 个 schedule run，已观察到稳定超标，触发预定 revisit。

2. **SIGTERM 143（双假说，无 ex-ante 决断能力）**
   - 2026-05-24 shards 3/0/7/9/15、2026-05-25 shards 1/2/10/15、2026-05-26 shards 8/9、2026-05-27 shards 1/3 均有 `##[error]The runner has received a shutdown signal`。`fail-fast: false` 已排除 sibling-fail-fast 级联；workflow 无 `concurrency:` 块排除 cancel-in-progress；step `timeout-minutes: 3` 与 job `timeout-minutes: 10` 未触发。
   - **OOM 假说**（§11 lineage）：多 shards 重复出现（shard 1/3/7 在多日复现，非完全随机分布）；shard 1 SIGTERM at 75 秒对齐 `packages.Load` 峰值时间窗口（**注意**：75 秒是 type-aware 测试 packages.Load 的典型时间窗口本身，spot preempt 也可能在该窗口触发，此条不构成 OOM 排他证据）。
   - **Spot preempt 假说**（§3 lineage）：workflow godoc line 7-10 已记录 `"GHA runner spot preemption ('runner has received a shutdown signal') with 16× amplification across the shard matrix"`；GHA spot 抢占可集中于某个 runner pool / availability zone，故"多 shards 重复出现"亦非完全反对 spot 的证据。
   - 无 runner-side `dmesg` / RSS 实测数据，ex-ante 不可区分。Phase A 诊断 step（同 PR `archtest-nightly.yml` `Pre-step memory snapshot` + `Post-step memory + dmesg`）捕获 `/proc/meminfo` + `ps --sort -rss` + `dmesg` 后下一轮 nightly 失败时可 ex-post 收敛。
   - **本 amendment K=24 对两类假说均有缓解**：OOM 路径下，per-shard tests 数从 47 降到 32，per-shard RSS 必然 ≤ K=16；Spot preempt 路径下，单 shard wall-time 缩短降低了暴露窗口面（虽不消除概率）。Phase A 诊断收据后若证伪 OOM 假说、锁定 spot preempt，单独 amendment 评估 runner tier 升级（`ubuntu-latest` → `ubuntu-latest-4-cores`）或 step retry 机制。

### 决策

#### D1. K=16 → K=24

- 保持 §Phase 0 "K 是 OOM 阈值的设计 knob" 论点；K=24 是 K=16 → 更细方向延伸（per-shard tests 数从 ~47 降到 ~32，shard 内同时 hold 的 `*types.Info` 子集减少，per-shard RSS 必然 ≤ K=16）。
- `SharedResolver` baseline cache 与 K 无关，是 RSS 主导项；K=16→24 增量降低集中在 t.Run subtests 持有的 typed objects 上（增量约 20-30%），并非根治 RSS（runner tier 升级才是激进降 RSS 路径，本 amendment 不采纳）。**归因修正（§Amendment 2026-06-03）**：该 baseline cache 的主导子项是 `packages.NeedDeps` 驱动的「依赖包 Syntax+TypesInfo 常驻」——**可摘**，非不可约的「全模块常驻」。原文「~3-4 GB macOS，packages.Load 全模块常驻部分」把可摘的 NeedDeps 增量误并入不可约项。实测单 `packages.Load` tests=F 954MB→149MB（↓84%）、tests=T 1338MB→508MB（↓62%）；剩余不可约下限 = 根包自身的 Types/TypesInfo/Syntax（archtest 本就需要）。详见末尾 §Amendment 2026-06-03。
- GHA Linux baseline 缺失（§Phase 0 是 macOS 测量），K=24 baseline 待 amendment 2026-06-?? Phase A 诊断收据后回填实测值。

#### D2. SLOWGATE_THRESHOLD 20s → 25s

- workflow godoc line 63-65 明文预留 "revisit after ~30 consecutive nightly runs" 触发点，当前已观察到 2 个不同 type-graph-load 测试稳定超标，符合 revisit 信号。
- 25s 仍能捕获回归（100ms sleep drift 到 25.1s+ 仍触发 slowgate）— slowgate 的根本意图（catch regression）不变质。
- 不采纳"单测试 allowlist"路径（添加 `TestSagaJournalConformanceEnrollment` 等单条 entry）：5/25 已暴露第 2 个测试超标，单点 allowlist 仅治标，下次 nightly 别的 type-graph-load 测试还会超阈。抬阈值是根因修复。

#### D3. Phase A 诊断 step（同 PR）

`archtest-nightly.yml` 加 pre/post-shard 诊断 step（`always()` 守卫）：
- Pre: `cat /proc/meminfo` 关键字段（MemTotal/MemFree/MemAvailable/Buffers/Cached/SwapTotal/SwapFree）
- Post: `cat /proc/meminfo` + `ps -eo pid,rss,vsz,cmd --sort -rss | head -10` + `sudo dmesg | tail -100` (fallback path 若无权限)

下一轮 143 复现时，artifact 与 step log 中的 RSS 峰值 + kernel OOM 证据可 ex-post 区分 OOM vs spot preempt 假说，回填本 amendment 决断证据。

### 同 PR 同步载体

K=16 ADR-mandated invariant 锚定 3 处，**必须同 PR 内一致更新**：

| 载体 | 更新内容 |
|---|---|
| `.github/workflows/archtest-nightly.yml` | `matrix.shard: [0..15]` → `[0..23]`；`SHARD_COUNT: 16` → `24`；`SLOWGATE_THRESHOLD: 20s` → `25s`；新增 `Pre-step memory snapshot` + `Post-step memory + dmesg` 诊断 step |
| `tools/archtest/archtest_ci_shard_count_test.go` | `validateVerifyArchtestExplicitShardCount` 期望值 `"16"` → `"24"`（通过新增 `expectedShardCount` const）；同步 5 处 yaml fixture |
| `docs/architecture/202605120000-adr-archtest-process-isolation.md` | 本 §Amendment 2026-05-28（本节） |

### 不变（§Phase 0 论点保持）

- "K 是 OOM 阈值的设计 knob"
- "K=16 是首个稳定低于 GHA 7GB OOM 阈值的分片粒度" — K=24 是更宽松方向延伸，论点延伸成立（不是反驳）
- "本地默认 K=1" 行为不变（`hack/verify-archtest.sh` 默认值不动）
- §Amendment 2026-05-23-pr-time-to-nightly：PR-time 不跑完整 archtest 不变；nightly 是 sole authoritative CI gate 不变

### §Phase 0 覆盖表更新

| §Phase 0 表行 | Amendment 2026-05-28 状态 |
|---|---|
| K=16 macOS RSS 4.22 GB | ⚠️ macOS 测量；K=24 GHA Linux baseline 待 Phase A 诊断 step 补 |
| K=4 macOS RSS 43 GB sweet spot | ✅ 论点不变（macOS workstation 场景） |
| K=8 留余量不足 | ✅ 论点不变（K=8 < K=16 < K=24，论点延伸成立） |
| K=16 是首个稳定低于 GHA 7GB OOM 阈值的分片粒度 | ✅ 论点不变；K=24 更宽松，是该论点的方向性延伸 |

### 范围外（不在本 amendment）

- **runner tier 升级 `ubuntu-latest` → `ubuntu-latest-4-cores`**：16GB runner 是激进降 RSS 路径（vs K 增量调整），但引入 GHA plan/cost 维度 + 不确定 large runner 可用性。Phase A 诊断后若证明 K=24 不足，再评估单独 amendment。
- **143 step retry 机制**：第三方 action 引入，独立 ADR 权衡（retry 缓解 vs 显式失败信号）。
- **`TestSagaJournalConformanceEnrollment` / `TestPGRepoAmbientTx_SelfCheck` 测试优化**：均为 type-aware whole-module `packages.Load`，设计本身无低成本优化；Hard 升级路径 = codegen funnel + golden（gh issue #1003 SAGA-JOURNAL-CONFORMANCE-ENROLLMENT-01）。

## Amendment 2026-06-03: typeseval 去 NeedDeps — RSS 主导项归因修正（#1499）

### 根因 / 归因修正

§Amendment 2026-05-28 §D1 把 RSS 主导项归因为「`SharedResolver` baseline cache（~3-4 GB
macOS，packages.Load **全模块常驻部分**）」，措辞暗示该 baseline 是**不可约**的全模块 type
graph 常驻。**修正**：baseline cache 的主导子项是唯一 typed loader（`tools/archtest/internal/
typeseval.LoadPackages`）的 load mode 携带的 `packages.NeedDeps`——它让 go/packages 为全部
~1135 个传递依赖包**常驻完整 Syntax(AST) + TypesInfo**。这部分**可摘**：去掉 NeedDeps 后
go/packages 经 `usesExportData` 快路径（`NeedTypes && !NeedDeps`）改由 export data
（gcexportdata）派生依赖的轻量 `Types`，根包的 Types/TypesInfo/Syntax 不受影响。不可约下限
只剩根包自身那部分，远小于原「全模块常驻」措辞所指。

### 决策

`typeseval` 默认 load mode 去掉 `packages.NeedDeps`（保留 `NeedImports`），一处改动
（`typeseval.go` 的 `loadMode` 常量）；所有 typed scope（`Typed` / `Production` / `Fixture` /
`StandaloneModule`）经 `SharedResolver` → `LoadPackages` 共享此 mode，无浅 typed 模式、无
per-rule opt-in 名单。

### 实测（本 PR 本地独立复现，go1.25 + 当前 x/tools，与 #1499 issue 表一致）

| 配置 | HeapAlloc WithDeps → NoDeps | 降幅 | pkgs |
|------|------|------|------|
| `./...` tests=F | 954 MB → **149 MB** | **↓84.4%（6.4×）** | 365（不变） |
| `./...` tests=T | 1338 MB → **508 MB** | **↓62.0%（2.6×）** | 923（不变） |

`loadErrs=0` 两种 mode 一致；pkgs 计数不变（同一 import 图，仅依赖包字段填充程度不同）。
未把内存问题转成编译时间——NoDeps 仍走 export data、无额外源码编译（issue #1499 实测单
`Load` wall-time ~1.6s vs ~1.3s 量级，build cache 命中时已在；本 amendment 未独立复测 wall-time）。

### 正确性

- 根包 Types/TypesInfo/Syntax 完全不受影响；依赖包符号经 `info.Uses` / `types.Implements` /
  `Scope().Lookup` 解析与带 NeedDeps **完全一致**。
- 唯一行为变化 = 依赖包的传递 `(*types.Package).Imports()` 闭包裁剪到 type-referenced 包
  （export-data importer 只物化实际被引用的依赖）。全仓 4 个做类型层传递 `.Imports()` BFS/DFS
  的 archtest 规则（`loadForbiddenIfacesFromPkg` / `resolveSagaStepFuncType` / `lookupInterface`
  / `findTypesPackageByPath`）在该裁剪下 **sound**：经 **命名类型引用**产生 finding 必然要求根包
  type-reference target 包，故 target 仍是直接 import、必在裁剪后闭包内；裁剪只丢对该根无关的包。
  **唯一 corpus-依赖路径** = cell raw-option 规则的 `types.Implements` structural-match
  （匹配 forbidden 方法集的匿名 interface 不必命名 forbidden 包）——其依赖 cell 的 import 闭包
  能触及 forbidden 包；当前每个平台 cell 均直接 import kernel/persistence + kernel/outbox，由
  `loadmode_nodeps_invariants_test.go` 跨 `./cells/...` 机器断言（len == forbidden 集大小）。
- 全仓 **0 处** 访问依赖包的 `.Syntax` / `.TypesInfo`（NeedDeps 专属字段）。
- 守卫：`typeseval.TestLoadMode_NoNeedDeps`（Medium，type-aware 值断言；Hard 不可达——
  `packages.LoadMode` 是 public int-flag，「任何地方不得带 NeedDeps」类型系统不可表达，同
  #851/#893/#1282 永久天花板）+ `tools/archtest/loadmode_nodeps_invariants_test.go`
  （Medium，4 walk 非空性回归，governance walk 由既有 `TestFindTypesPackageByPath` 覆盖）。

### 开源对标

- go/packages `usesExportData(cfg)`：`NeedTypes != 0 && NeedDeps == 0` 时**显式**走 export
  data——本改动命中的设计分支，非 hack。`NeedImports` doc：无 NeedDeps 时 `Imports` map 仅含
  ID placeholder（`tools/depgraph` 只读 import-path key，安全）。
- go/analysis 官方 checker：根包-only 分析默认 `LoadSyntax`（= `LoadTypes|NeedSyntax|
  NeedTypesInfo`，**不含 NeedDeps**）；`LoadAllSyntax`（+NeedDeps）仅用于遍历所有依赖 AST。
  GoCell 新 mode ≈ `LoadSyntax + NeedName + NeedFiles`，正落根包-only 档。

### 解锁但本 amendment 不做（范围外）

两条路径**抢同一份 freed RSS 余量、需取舍**，合并跟踪于 gh issue **#1518**（决策点 = 拿到 GHA Linux
per-shard RSS 实测后再定，不凭 macOS 数字硬拍）：

- **warmup 多 cacheKey 扩展**：去 NeedDeps 把单 cacheKey 常驻从 ~954MB 降到 ~149MB（tests=F）
  后，多 key warmup（之前 N=2 即在 `packages.Load` 期撞 6-9GB OOM，closed PR #865）才真正可行。
  **顺序**：先去 NeedDeps 解锁，再独立评估扩 warmup——不在本 PR。
- **shard 数 K=24→? 下调评估**：新 baseline 下重测 per-shard RSS 后独立 amendment。

### 逐行重评（per `.claude/rules/gocell/ai-robust.md` §"ADR amendment 落地必查"）

| 载体 | 状态 | 处理 |
|---|---|---|
| §Amendment 2026-05-28 §D1 「baseline ~3-4GB / 全模块常驻 / RSS 主导项」 | 直接矛盾 | **已同 PR 内就地重写**（指向本 §Amendment）；不保留原文作历史脉络 |
| §Amendment 2026-05-28 §Phase 0 覆盖表（per-shard macOS RSS 行） | ⚠️ 下限降低 | per-shard RSS 的 baseline 子项现降低（lower floor），sharding "K 是 OOM knob" 论点不变；GHA Linux Phase A 实测时反映新 baseline，不在本 PR 回填 |
| §范围外「slow-test 设计本身无低成本优化」（line 358） | ✅ 不受影响 | 该条是 **wall-time**（slowgate）轴；NeedDeps 是**内存**轴，load wall-time 1.3→1.6s 未恶化，两轴正交 |

### 关联（不在本 amendment 范围）

本 amendment 落地前，`tools/archtest` 测试包一度因 PR #1477 遗留的 2 处 `RunTypedProduction`/
`RunTypedFixture`（PR #1470 collapse Run* 时已删该 API）而**非编译**；该 compile break 已由
**PR #1502（closes #1500）独立修复并合并入 develop**，本 PR 不再含 compile-fix。编译恢复后浮现的
**pre-existing** 规则违规（webhook DAG / contractspec 字面量 / scanner-framework / require.Eventually
/ managed-resource 等，与 load mode 正交——WithDeps 与 NoDeps 失败集 byte-identical）中 orderfulfillment
TEST-POLLING 一项亦由 #1502 修复，**剩余 6 项由 gh issue #1506 跟踪**，均为 nightly-only archtest、
不在本 amendment 范围。

## Amendment 2026-06-10: archtest leaf build-tag — VERIFY_SKIP 单 gate skip 的漏洞闭合（#1803 workspace-module 回归）

### 触发

`#1803 refactor(tools): split tools into workspace module` 合并（2026-06-09 22:10 UTC）后，
`Governance Strict`（`make verify`）lane 从 ~6-7min 跳到 ~10-14min。develop push 计时铁证：
`06-09T06:43` push=7m → `06-09T22:10`（即 #1803 合并）push=11m。`hack/verify-workspace-test.sh` 对每个
非 root workspace 成员跑 `GOWORK=off go -C <dir> test ./...`；`tools/` 一旦成为成员，`tools/archtest`
（1218 个 `Test*`，K=1 ~5min）被这条通用遍历拉进 PR 关键路径，本地 `make verify` 还跑两遍。

### 根因

`VERIFY_SKIP=archtest`（§D6）是**按 gate 的 skip**——它只跳过专用的 `hack/verify-archtest.sh`，
**不是 archtest 套件自身的属性**。任何对含 archtest 的 module 跑裸 `go test ./...` 的 gate 都会重新执行它。
更深一层：`integration` / `e2e` / `examples_smoke` / `mqtt_tls` 这些重型 opt-in 套件**全部已挂 build tag**，
天然不泄进 `./...`；**archtest 是唯一「重型但没挂 tag」的套件**——这才是它会从 `verify-workspace-test`
侧门泄漏、且 §D6 single-owner 原则被静默破坏的本源。

### 决策

- **D1（funnel 口，Hard）**：leaf `tools/archtest/*_test.go`（288 文件，不含 `internal/`）挂
  `//go:build archtest`。未带 `-tags=archtest` 的 `go test ./...` 编译期即把 leaf 当「no test files」，
  **不可表达**地无法执行 archtest。与既有重型套件同范式（非新机制）。
- **D2（owner opt-in）**：`hack/verify-archtest.sh`（含 DRY_RUN/LIST_SHARD discovery）、
  `hack/verify-archtest-invariants.sh`、`hack/verify-rules-governance.sh`、`archtest-nightly.yml`（经
  verify-archtest.sh 继承）显式加 `-tags=archtest` + 各自非真空断言（discovery/跑数为 0 即 fail，堵
  「漏加 -tags → 静默假绿」）。
- **D3（上游完整性，Medium）**：`ARCHTEST-LEAF-BUILD-TAG-01`（`tools/archtest/archtest_leaf_build_tag_test.go`）
  typed `//go:build` 扫描断言每个 leaf `*_test.go` 以 archtest 为**必要 tag**（GOOS-无关的布尔必要性检查，
  正确接受 `archtest && !windows`），配 synthetic red fixture + anti-vacuity；并入
  `verify-archtest-invariants.sh` PR-time 子集，使「新文件漏 tag → 静默泄漏」在 PR-merge 即红。
- **D4（build-test grep 角色收窄）**：`_build-lint.yml` build-test tools shard 的
  `go list ./tools/... | grep -v …/tools/archtest` **保留**，但职责从「执行排除（唯一机制）」收窄为
  「**coverage-scope 排除**」——leaf 库代码 ~6.7k 语句若进入本 shard profile 会以 0% 拖累覆盖率（实测）；
  执行排除已由 D1 的 tag 保证。lint parity：satellite lint loop 对 tools 加 `--build-tags=archtest`
  以保 288 文件 lint 覆盖不回退（`golangci-lint run` 是 package-based / build-tag 敏感；`fmt` 是
  file-based，无需改 `verify-gofumpt.sh`，已实测）。

### 同 PR 同步载体

| 载体 | 变更 |
|---|---|
| `tools/archtest/*_test.go`（288） | 加 `//go:build archtest`（2 个 `!windows` 文件合并为 `archtest && !windows` 并提到首行） |
| `hack/verify-archtest.sh` / `-invariants.sh` / `verify-rules-governance.sh` | owner opt-in `-tags=archtest` + 非真空断言 |
| `tools/archtest/archtest_leaf_build_tag_test.go` | 新增 ARCHTEST-LEAF-BUILD-TAG-01（D3） |
| `.github/workflows/_build-lint.yml` | satellite lint `--build-tags=archtest`（tools）；build-test tools-shard 注释职责收窄（D4） |
| `hack/verify-workspace-test.sh` / `hack/README.md` | 头注释 / gate 登记记录 build-tag 边界 |
| 本 ADR | 本 §Amendment 2026-06-10（本节）+ §D6 就地交叉引用 |

### 开源对标

Go 生态「重型套件排出默认 `go test`」标准只有 build tag（编译期排除、默认安全）与 `testing.Short()`
（运行期跳过、默认*运行* → 要默认排除得每 caller 传 `-short`，回到 callsite-locking，不符）。archtest 对标
`golang.org/x/tools/go/analysis` 原始范式跑在 `multichecker.Main`（`cmd`，天然在 `go test` 外）；GoCell 选
`analysistest.Run` 式测试驱动 runner 才把它塞进 `go test` 图——本 amendment 用 build tag 把它收回与 cmd
范式等效的「默认不在 `./...`」位置，不重构 runner。`ref: golang.org/x/tools/go/analysis multichecker`。

### 逐行重评（per `.claude/rules/gocell/ai-robust.md` §"ADR amendment 落地必查"）

| 载体 | 状态 | 处理 |
|---|---|---|
| §D6 / line 65「push / pull_request 不再跑 archtest …不再双跑」 | 直接矛盾（#1803 后 workspace-test 实跑 archtest） | **已同 PR 内就地补强**：line 65 加交叉引用，明确 VERIFY_SKIP 只 skip 专用 gate、build tag（D1）才是「off bare `go test`」的编译级保证 |
| §D6 single-owner 原则（nightly + verify-archtest.sh 为唯一 owner） | ⚠️ 补强 | 原则不变；本 amendment 把它从「靠 VERIFY_SKIP 约定」升级为「靠 build tag 编译级 + ARCHTEST-LEAF-BUILD-TAG-01 守卫」机器强制 |
| §D6 `VERIFY_SKIP=archtest` env | ✅ 保留 | 仍 skip 专用 `verify-archtest.sh` gate（避免 governance lane 跑 K=1 全量）；与 build tag 互补，非冗余 |
| build-test `_dynamic_archtest_excluded` grep | ⚠️ 职责收窄 | 见 D4：执行排除 → coverage-scope 排除 |

---

## §Amendment 2026-06-10 (PR-A10 #1171, KERNEL-RECONCILE-01 收口) — reconcile invariant 族纳入 archtest 池

epic #661（`kernel/reconcile` L4 收敛控制环）的 archtest 守卫 `tools/archtest/reconcile_*_test.go`
（4 文件、**27 个顶层 Test 函数**：interface/Request/Result/Trigger/LeaderElector frozen、
BUILDER-FUNNEL、FENCED-WRITE-FUNNEL、RESULT-LABEL-VALUES-FROZEN、REQUEUE-ENQUEUE-CALLER、
LEADER-IMPL-FUNNEL、goleak TestMain funnel）随 PR-A2…A7 已 merge，早已在 develop archtest 池内。
本收口 PR（A10）**纯文档、不新增任何 archtest 函数**；本 §Amendment 在 epic 关闭时回溯记录该 invariant
族的 CI 足迹并再评威胁矩阵，不改决策。

**无 matrix / 脚本编辑**：D1 的 discovery（`go test -list '^Test' ./tools/archtest` modulo `SHARD_COUNT`）
按设计自动吸收这 27 个函数（A2…A7 落地时即生效），无需任何 CI 配置改动——这正是 D1 动态分片相对旧
K=N 静态 enumerate 的目的。

### 威胁矩阵再评（per ai-robust.md §"ADR amendment 落地必查"）

| 维度 | 评估 |
|---|---|
| per-shard RSS / OOM 包络 | reconcile 族 27 个顶层 Test 函数（见上；`grep -hc '^func TestReconcile' tools/archtest/reconcile*_test.go`，随 epic 冻结、增删须过 review）在 develop archtest 全池中占低个位数百分比，且该占比随全仓 archtest 增长只稀释不放大——K=24 下每 shard ≈ +1 函数。**本 ADR 不固化任何全池绝对计数**：池大小以 D1 discovery（`go test -tags archtest -list '^Test' ./tools/archtest`）实时为真源，写死的总数会随无关 PR 漂移（line 535 同理拒绝 true-up 历史 296/32）。OOM 触发源是 `typeseval.SharedResolver` 的 type-graph 累加，该族不引入新对标语义类别，per-shard RSS 仍落在 §决策 中 K=24 < GHA 7GB 的方向性包络内。|
| §决策 行的绝对计数（"296 函数"/"per-shard 32"）| 为历史 macOS baseline，早被 line 29 声明为「方向性论点权威、不以绝对数为真值源」；本 PR **不** true-up 该历史数（其漂移源自全仓 archtest 4× 增长，非 reconcile，超出本 PR 范围）。|
| 一致性守卫 | `ARCHTEST-CI-EXPLICIT-SHARD-COUNT-01` / `ARCHTEST-LEAF-BUILD-TAG-01` 不受影响；reconcile 测试文件已带 `//go:build archtest` leaf tag（同 D3 范式），编译级排除 bare `go test` 自动覆盖。|

> 关联 ADR2 `202605170000-...`：reconcile.Loop 共用的 control-plane clock carve-out 已由其 §Amendment
> 2026-06-06（#1169）就地重写，本 PR 不重复 amend（避免双真值源）。
