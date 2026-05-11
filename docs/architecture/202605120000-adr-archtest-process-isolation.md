# ADR: Archtest CI 入口 process-isolated sharding（K=16）

> Status: Accepted
> Date: 2026-05-12
> Implementation: fix/307-archtest-verify-process-isolation
> Source plan: docs/plans/202605110830-305-archtest-verify-process-isolation.md

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

### D1. CI 入口改为 process-isolated 16-shard 矩阵

`hack/verify-archtest.sh` 整体重写：discovery via `go test -list '^Test' ./tools/archtest`，按字母序 modulo 16 分片，每 shard 独立 `go test -run '^(name1|name2|...)$'` 调用。SHARD_TARGET 单 shard 模式给 GHA matrix 用；无 SHARD_TARGET 时串行跑全部 16 shard（`make verify` 路径）。

`.github/workflows/_build-lint.yml` 新增 `verify-archtest` job：`matrix.shard: [0..15]`，每 shard 独立 ubuntu-latest runner。`fail-fast: false` 对齐 K8s `hack/make-rules/verify.sh` continue-on-failure 范式。

### D2. tools shard 不再 enumerate archtest，pkgs 运行时计算

`.github/workflows/_build-lint.yml` tools shard `pkgs` 改为 sentinel `_dynamic_archtest_excluded`；Test step 用 `go list ./tools/...` + grep 排除 archtest **顶层包**（`github.com/ghbvf/gocell/tools/archtest`）。`tools/archtest/internal/{scanner,typeseval,rawparamfixture,wrapfixture}` 子包保留在 tools shard 执行——它们的测试是轻量单元测试（11+3+0+0 \_test.go），不触发 `typeseval.SharedResolver` 的 type graph 累加（296 archtest 顶层函数才是 OOM 触发源）。

**AI-rebust 评级**：按 `.claude/rules/gocell/ai-collab.md` 载体定义严格分类为 **Medium**（shell runtime guard + go list subprocess + grep 过滤），不是 codegen funnel / type system / sealed interface 的 Hard。但从覆盖保证角度等效 Hard：「新 `tools/<sub>` 包被遗漏」**在 type system 不可表达**——`go list ./tools/...` 是 ground truth，新包自动入列，archtest 是唯一显式排除目标。AI 仍能通过手工改 yml 回硬编码列表绕过 runtime 计算，但这种回退要改 yml + 改注释 + 通过 diff review，可观测性足够。在本 PR 范围内接受为 **Hard-effective**，不立 archtest 元规则守卫（守卫本身也是 Soft 的字符串扫，反而开倒车）。

### D3. discovery vs AST 一致性元规则

`tools/archtest/archtest_verify_coverage_test.go::TestArchtestVerifyCoverage01`（INVARIANT `ARCHTEST-VERIFY-COVERAGE-01`）：shell-out `DRY_RUN=1 bash hack/verify-archtest.sh` → 与 `scanner.EachNode[ast.FuncDecl]` AST 扫到的 top-level Test* 函数集合做对称 diff，非空则 fail。守的风险：维护者改脚本加 `grep -v TestFoo` debug 过滤忘删 → CI silent unenforce（local `go test ./tools/archtest/...` 仍捕获，但 PR Check 漏过）。AI-rebust **Medium**（runtime cross-check 双重源）。

### D4. `make verify` 不再 skip archtest

`.github/workflows/governance.yml` 删 `VERIFY_SKIP: archtest` env、timeout-minutes 5 → 15。重写后 verify-archtest.sh serial 16-shard 各独立 process，governance.yml 跑完只多 5-8 min wall，无 OOM。统一 `make verify` 入口覆盖 develop push + PR；PR Check 的 matrix-parallel `verify-archtest` job 提供 fast-feedback 通道。

### D5. slowgate 重接

`hack/verify-archtest.sh` 内 shard 路径：若 `$SLOWGATE_BIN` executable，`go test ... -json -run '...'` 管道入 slowgate（与 `_build-lint.yml` 旧 tools shard 同范式）；否则 plain `go test`（local dev）。CI job 内 `go build -o "$RUNNER_TEMP/slowgate" ./tools/slowgate` 后注入 env。

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
