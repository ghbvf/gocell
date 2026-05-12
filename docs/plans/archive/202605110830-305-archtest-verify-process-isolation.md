# PR305-FU-ARCHTEST-VERIFY-PROCESS-ISOLATION-01：D 路径完整说明

> **状态**：✅ shipped via fix/307-archtest-verify-process-isolation（2026-05-12）
> **实施 ADR**：`docs/architecture/202605120000-adr-archtest-process-isolation.md`
> **关键偏差**：
>   - test count 296 实测（源 plan 估 70 偏低 4.2×）
>   - shard K=16（源 plan 默认 6；Phase 0 实测 K=6 max peak 11 GB 不达标，K=16 max 4.22 GB 合格）
>   - Makefile：未加 `verify-archtest` target（直接靠 `hack/make-rules/verify.sh` 自动发现）
>   - 元规则：1 条（ARCHTEST-VERIFY-COVERAGE-01）；TOOLS-SHARD-PKG-COVERAGE-01 通过 `go list ./tools/...` 运行时计算升 Hard 消除
>   - slowgate：本 PR 同步重接
> **作者**：Claude Opus（PR #305 hotfix follow-up，2026-05-11）
> **背景**：本文是源计划，归档供历史追溯。

---

## 0. TL;DR

把 `tools/archtest/*_test.go` 里 70+ archtest 函数从「`go test ./tools/archtest/...` 单进程运行」改成「`make verify-archtest` 多进程分片运行」，**每个分片是独立 Go process**，跑完释放内存。对齐 Kubernetes `make verify-*` OSS 范式。彻底解决 PR #445 后 GHA 2-core 7GB runner 上的 OOM SIGTERM 问题，无需付费、无需 self-hosted runner。

**核心机制**：单 `go test` binary 持有 N 个 `*types.Info` 缓存累加超 7GB → OOM。改成 K shard ×（每 shard 独立 process × 独立 packages.Load × 跑完 exit 释放）后，内存峰值 = 单 shard 内的 `*types.Info` 占用（≪ 7GB）。

---

## 1. 问题陈述

### 1.1 已观测现象

2026-05-10 19:47 UTC PR #445 (PR-Φ EachNode funnel + SCANNER-FRAMEWORK-USAGE-01 type-aware) merge 进 develop 后：

- **15+ 跨 PR 的 PR Check `build-test (tools, ./tools/...)` job** 出现 SIGTERM exit 143（runner 端杀进程）
- **临界点完全对齐 PR #445 merge**：merge 前 100% pass，merge 后 100% fail
- **失败模式**：Test step 启动 2-3 分钟无任何输出后 SIGTERM；`go test -timeout 10m/20m` 内置 timer 也不触发
- **影响范围**：develop + 所有 PR；本 PR #305 多次 hotfix 尝试（5 次）均未解决

### 1.2 根因分析

层层根因（已在 PR #305 commit message 留档）：

| 层 | 描述 |
|---|---|
| **L1 直接** | PR #445 SCANNER-FRAMEWORK-USAGE-01 升 type-aware，`forbiddenWalkRefs` 用 `*types.Info` 做 receiver method 识别 |
| **L2 机制** | `typeseval.SharedResolver` 按 cacheKey 缓存。PR #445 引入新 cacheKey 维度（receiver type + interface impl 解析），N 个 cacheKey 各自 hold 一份完整 module type graph (`packages.Load(...NeedTypesInfo)` 单次结果 200-500 MB) |
| **L3 架构** | `go test ./tools/archtest/...` 是**单进程**跑 70+ archtest 函数，所有 `*types.Info` 缓存累加在同一 process heap。Go GC 无法跨 test function 释放包级 var (`SharedResolver`) 持有的 type graph 引用 |
| **L4 设计** | 把 70+ governance 静态检查塞进 `go test` unit-test pipeline，本质上不可扩展。GHA 2-core 7GB runner 无法承载 |

### 1.3 已 evaluate 但放弃的备选方案

| 方案 | 否决理由 |
|---|---|
| GHA larger runners (`ubuntu-latest-8-cores`) | personal account / Org Free plan 不支持，需 Team plan ($4/user/月) |
| Self-hosted runner | public repo + fork PR 安全风险高；Mac 本机 always-on 维护负担 |
| `act` 本地跑 | 诊断工具，不能替代 PR Check 真实 CI |
| `runtime/debug.SetMemoryLimit` 内 process 内限制 | 治标，仍可能在峰值时被 Go runtime panic kill |
| amortize TestMain SharedResolver（C 路径）| 仍单进程，GC 不释放包级缓存 → 治标不治本 |
| revert PR #445 | 失去 PR-Φ 收益；即使重交也面临同一 OOM 困境 |

D 路径（process isolation）是**结构性治本**且**对齐 OSS 主流**的唯一可行方案。

---

## 2. 目标与成功标准

### 2.1 主要目标

1. **PR Check `tools` shard wall time < 1 min**（archtest 移走后 tools shard 仅剩 codegen / depgraph / generatedverify / metricschema / nogo / slowgate 等子包，本地实测 < 50s）
2. **PR Check `verify-archtest` job wall time < 5 min**（K shard 并行，每 shard 独立 process，单 shard 内存峰值 < 2GB）
3. **archtest 全套 70+ 规则继续 blocking**（不允许 archtest 失败 PR 仍能 merge）
4. **本地 `go test ./tools/archtest/` 仍可工作**（开发者无感切换）
5. **K8s `make verify-*` 范式对齐**：每个 verify 子命令独立 process，可独立调试、独立 timeout、独立资源占用

### 2.2 非目标

- ❌ 不重写 archtest 规则本身（70+ 函数代码不动）
- ❌ 不优化 archtest 性能（amortization 是另一条 backlog 条目，与本项独立）
- ❌ 不引入新依赖（不上 bazel、不上 ARC k8s 等重型方案）
- ❌ 不改 CLAUDE.md ai-collab.md 中的 archtest 治理章程（载体决策不变）

### 2.3 成功验收

- [ ] PR Check `tools` shard pass，wall < 1 min
- [ ] PR Check 新增 `verify-archtest` job pass，wall < 5 min（K shard 并行）
- [ ] develop 上 `make verify-archtest` 本地跑 0 violation
- [ ] 引入一处真违反（手动 commit 一个绕过 archtest 的 case）→ `make verify-archtest` 报错 exit 1
- [ ] PR Check 中 `verify-archtest` 失败时阻塞 PR merge（required check）
- [ ] 旧 `tools/archtest-inventory.txt` (如有) 与新 sharding 列表一致；无 test 函数被遗漏

---

## 3. 设计

### 3.1 架构总览

```
                                  PR Check (pr-check.yml)
                                         │
        ┌────────────┬────────────┬──────┴──────┬────────────┬──────────────┐
        │            │            │             │            │              │
   build-test   build-test    build-test   build-test   build-test    verify-archtest
   (kernel)     (tools)       (runtime)    (cells)      (others)      (matrix shard 0..5)
   ubuntu-l     ubuntu-l      ubuntu-l     ubuntu-l     ubuntu-l      ubuntu-l × 6
                ↑                                                     ↑
                │                                                     │
        ./tools/codegen/...                                  独立 Go process per shard
        ./tools/depgraph/...                                 跑 ./tools/archtest/ 的子集
        ./tools/generatedverify/...                          shard partition 由 hack/
        ./tools/metricschema/...                             archtest-shards.sh 决定
        ./tools/nogo/...                                     每 shard wall < 1 min
        ./tools/slowgate
        (NOT ./tools/archtest/...)
```

### 3.2 组件分解

#### 3.2.1 `hack/verify-archtest.sh`（新建）

Top-level orchestrator，等价于 K8s `hack/verify-*.sh`。

```bash
#!/usr/bin/env bash
# hack/verify-archtest.sh
# Runs archtest in process-isolated shards. Each shard is an independent
# `go test` invocation against a deterministic subset of test functions
# in ./tools/archtest/, ensuring memory accumulated by typeseval.
# SharedResolver is released between shards.
#
# Usage:
#   hack/verify-archtest.sh                # run all shards sequentially
#   hack/verify-archtest.sh --shard N      # run only shard N (for CI matrix)
#   hack/verify-archtest.sh --shards M     # override shard count (default 6)
#
# Exit code: 0 = all pass, 1 = any shard failed.

set -euo pipefail

SHARD_COUNT="${SHARD_COUNT:-6}"
SHARD_TARGET="${SHARD_TARGET:-}"
TIMEOUT="${TIMEOUT:-5m}"
ARCHTEST_PKG="./tools/archtest"

# Discover all top-level Test* functions (deterministic order).
TESTS=$(go test -list '^Test' "$ARCHTEST_PKG" \
  | grep -E '^Test' | sort)

if [ -z "$TESTS" ]; then
  echo "ERROR: no archtest functions discovered" >&2
  exit 1
fi

TOTAL=$(echo "$TESTS" | wc -l | tr -d ' ')

run_shard() {
  local shard=$1
  local pattern
  pattern=$(echo "$TESTS" \
    | awk -v s="$shard" -v n="$SHARD_COUNT" 'NR % n == s' \
    | tr '\n' '|' \
    | sed 's/|$//')
  if [ -z "$pattern" ]; then
    echo "[shard $shard] no tests assigned (TOTAL=$TOTAL, SHARD_COUNT=$SHARD_COUNT)"
    return 0
  fi
  local count
  count=$(echo "$pattern" | tr '|' '\n' | wc -l | tr -d ' ')
  echo "=== shard $shard/$SHARD_COUNT ($count tests) ==="
  go test -count=1 -timeout "$TIMEOUT" \
    -run "^($pattern)$" "$ARCHTEST_PKG"
}

if [ -n "$SHARD_TARGET" ]; then
  run_shard "$SHARD_TARGET"
else
  for s in $(seq 0 $((SHARD_COUNT - 1))); do
    run_shard "$s"
  done
fi

echo "PASS: archtest verified (sharded process-isolation)"
```

**关键设计点**：

1. **Discovery 用 `go test -list`**：动态发现新增 archtest 函数，无 manual registry 漂移
2. **Modulo 分片**：deterministic stable assignment（test function 名按字母序 modulo SHARD_COUNT）
3. **每 shard 一次 `go test` 调用**：独立 OS process，跑完 exit 释放整个 heap
4. **SHARD_COUNT 默认 6**：经验估算 70 functions / 6 ≈ 12 functions/shard，单 process 内 cacheKey 数量受控
5. **`--shard N` 参数支持 CI matrix 并行**

#### 3.2.2 `Makefile` target（修改）

在现有 `Makefile` 中添加：

```makefile
# archtest as process-isolated verify pass (K8s pattern). Replaces
# direct `go test ./tools/archtest/...` in PR Check tools shard, which
# OOMs on the GHA 2-core 7GB runner due to typeseval.SharedResolver
# accumulation across 70+ test functions in a single process.
# See docs/plans/202605110830-305-archtest-verify-process-isolation.md
verify-archtest:
	@bash hack/verify-archtest.sh

verify-archtest-shard-%:
	@SHARD_TARGET=$* bash hack/verify-archtest.sh
```

并把 `verify-archtest` 加入既有 `verify` aggregate target（与现有 `verify-codegen` / `verify-journey` / etc. 同级）：

```makefile
verify: verify-codegen verify-journey verify-contract-health verify-archtest
	@echo "ALL VERIFY PASSED"
```

#### 3.2.3 `tools/archtest/` 测试代码（**不动**）

70+ archtest 函数代码全部保持原样：
- `tools/archtest/*_test.go` 不改动
- `tools/archtest/internal/scanner/` 不改动
- `tools/archtest/internal/typeseval/` 不改动
- 本地 `go test ./tools/archtest/...` 仍 100% 工作（与改造前等价）

**改造仅在 CI 编排层**——把 archtest 的 CI 入口从 `go test` 切换到 `make verify-archtest`。

#### 3.2.4 `.github/workflows/_build-lint.yml`（修改）

**变更 1**：tools shard `pkgs` 字段移除 archtest

```yaml
- shard: tools
  # archtest moved out to verify-archtest job (process isolation, see
  # docs/plans/202605110830-305-archtest-verify-process-isolation.md).
  # tools shard now covers tools/ subpackages with bounded memory cost.
  pkgs: ./tools/codegen/... ./tools/depgraph/... ./tools/e2egate/...
        ./tools/generatedcatalog/... ./tools/generatedverify/...
        ./tools/internal/... ./tools/metricschema/... ./tools/nogo/...
        ./tools/slowgate
  static_checks: false
  timeout: 5m
  jobTimeoutMin: 13
```

> **注意**：`pkgs` 必须显式枚举（不能用 `./tools/...` exclusion，go test 不支持）。新增 tools 子包时需同步加入此列表，由新 archtest `TOOLS-SHARD-PKG-COVERAGE-01` 守护（见 §6 Risks）。

**变更 2**：新加 `verify-archtest` job

```yaml
verify-archtest:
  # Process-isolated archtest verification. Replaces the previous
  # ./tools/archtest/... inclusion in build-test (tools) shard which
  # OOMed on 2-core 7GB GHA runners after PR #445. Each matrix shard
  # is an independent Go process; typeseval.SharedResolver memory is
  # released at process exit. See docs/plans/202605110830-305-...md.
  runs-on: ubuntu-latest
  timeout-minutes: 10
  strategy:
    fail-fast: false
    matrix:
      shard: [0, 1, 2, 3, 4, 5]
  steps:
    - uses: actions/checkout@de0fac2e4500dabe0009e67214ff5f5447ce83dd
      with:
        fetch-depth: 0
    - uses: actions/setup-go@4a3601121dd01d1626a1e23e37211e3254c1c06c
      with:
        go-version-file: go.mod
        cache-dependency-path: go.sum
    - name: Pre-download modules
      timeout-minutes: 3
      run: go mod download -x
    - name: Verify archtest shard ${{ matrix.shard }}
      env:
        SHARD_COUNT: 6
        SHARD_TARGET: ${{ matrix.shard }}
        TIMEOUT: 5m
      run: bash hack/verify-archtest.sh
```

**变更 3**：pr-check.yml / ci.yml `required` checks 加入新 job

如果有 branch protection 配置，更新 `Required status checks` 列表，纳入 6 个 `verify-archtest (shard X)` job。

#### 3.2.5 `governance.yml`（可选修改）

`make verify` 已经在 governance workflow 跑。改造后 `make verify` 自动包含 `verify-archtest`——会让 governance.yml job 也跑 archtest，与新 PR Check `verify-archtest` job 重复。

**两个选项**：
- (a) 保持重复（governance 是 push-on-develop trigger，PR Check 是 PR trigger，覆盖场景不同）
- (b) governance.yml 用 `make verify-no-archtest` 等过滤目标避免重复

推荐 (a)：重复成本可接受，覆盖更严密。

---

## 4. 实施分阶段

### Phase 0：本地验证 prerequisites（0.5h）

- [ ] 本地跑 `go test ./tools/archtest/` 当前 baseline 拿到 wall time + memory peak
- [ ] 本地跑 `go test -list '^Test' ./tools/archtest/ | wc -l` 确认 test 数量（应该 ~70）
- [ ] 检查 `tools/archtest/` 是否有 TestMain / init() 干扰 sharding（按预期 SharedResolver 是 sync.Once 包级 var，不会跨 process 共享）

### Phase 1：`hack/verify-archtest.sh` skeleton（2h）

- [ ] 新建 `hack/verify-archtest.sh`（按 §3.2.1 实现）
- [ ] 加 `set -euo pipefail` 严格模式
- [ ] **本地验证**：
  - `bash hack/verify-archtest.sh` 全部 6 shard 跑通，结果与 `go test ./tools/archtest/` 等价（test 总数一致、违反一致）
  - `bash hack/verify-archtest.sh --shard 0` 单 shard 跑通
- [ ] 测各 shard 内存峰值（用 `/usr/bin/time -v` Linux 或 `gtime -v` Mac）：每 shard ≤ 2GB；与单 process 全跑对比，验证内存确实分摊

### Phase 2：Makefile target（0.5h）

- [ ] 新增 `verify-archtest` / `verify-archtest-shard-%` target
- [ ] 加入 aggregate `verify` target
- [ ] **本地验证**：
  - `make verify-archtest` 跑通
  - `make verify-archtest-shard-3` 跑通
  - `make verify` 全套（含 verify-archtest）跑通

### Phase 3：CI workflow integration（2h）

- [ ] 修改 `_build-lint.yml`：
  - tools shard `pkgs` 移除 `./tools/archtest/...`，改为显式枚举其余 tools 子包
  - 新增 `verify-archtest` job（matrix shard 0..5）
- [ ] **本地用 `act` 验证**（如果可用）：
  - `act -j verify-archtest --matrix shard:0`
  - `act -j build-test --matrix shard:tools`（确认 tools shard 不再含 archtest）
- [ ] PR 提交，观察 PR Check：
  - tools shard 应该 < 1 min
  - 6 个 verify-archtest shard 各 < 1.5 min（并行总 wall < 1.5 min）

### Phase 4：required check 配置（0.5h）

- [ ] repo Settings → Branches → Branch protection rules → develop
- [ ] 在 `Require status checks to pass before merging` 列表中：
  - 新增 6 个 `verify-archtest (X)` checks
  - 移除旧的 `build-test (tools, ...)` 中 archtest 相关引用（如果有）
- [ ] 验证：刻意提一个 archtest 违反 PR，PR Check `verify-archtest (X)` red，PR merge 按钮变 disabled

### Phase 5：archtest 治理元规则（2-4h）

新增 archtest 守护 sharding 不漂移的元规则：

#### 5.1 `TOOLS-SHARD-PKG-COVERAGE-01`

守护 `_build-lint.yml` 中 tools shard `pkgs` 列表覆盖 `./tools/` 下除 archtest 外**所有**子包。新加子包时如果忘记加入 tools shard `pkgs`，archtest 报错。

实现：scan `./tools/*/` 目录，对照 yml 字符串提取的 pkgs 列表（已知 yaml 难解析，可用 `kernel/depgraph` 范式或 `go list ./tools/...` + 排除 archtest 后比对）。

#### 5.2 `ARCHTEST-VERIFY-COVERAGE-01`

守护 `hack/verify-archtest.sh` 通过 discovery 确实拿到所有 archtest 函数，无遗漏。

实现：`bash hack/verify-archtest.sh --dry-run` 输出 discovered tests 总数 + 每 shard 分配；与 `go test -list '^Test' ./tools/archtest/` 总数对比。

### Phase 6：文档与清理（1h）

- [ ] 更新 `CLAUDE.md` archtest 章节：补充 `make verify-archtest` 入口
- [ ] 更新 `.claude/rules/gocell/ai-collab.md`：archtest 文件命名规则后加一段「CI 通过 `make verify-archtest` 跑」
- [ ] 写一条 ADR：`docs/architecture/202605120000-adr-archtest-process-isolation.md`，记录架构决策与 K8s 范式对照
- [ ] backlog：
  - close `PR305-FU-ARCHTEST-VERIFY-PROCESS-ISOLATION-01`
  - close `PR305-FU-SLOWGATE-PIPE-RESTORATION-01`（恢复 slowgate 为 verify-archtest 后置 post-test 文件分析，单 shard 一份 -json file）
  - 转移 `PR305-FU-ARCHTEST-SHAREDRESOLVER-AMORTIZATION-01` 状态：从 P1 必做降为 P3 触发型（process isolation 已治本，amortization 是性能优化，触发条件 = 单 shard 突破 2 min wall）

---

## 5. 文件级改动清单

| 文件 | 操作 | 行数估计 |
|---|---|---|
| `hack/verify-archtest.sh` | **新建** | ~50 行 bash |
| `Makefile` | **追加 target** | ~10 行 |
| `.github/workflows/_build-lint.yml` | tools shard pkgs 改 + 新 job | ~40 行（净增） |
| `tools/archtest/tools_shard_pkg_coverage_test.go` | **新建** archtest 元规则 | ~80 行 |
| `tools/archtest/archtest_verify_coverage_test.go` | **新建** archtest 元规则 | ~60 行 |
| `CLAUDE.md` | 加 archtest 入口段 | ~10 行 |
| `.claude/rules/gocell/ai-collab.md` | 加 verify-archtest 段 | ~5 行 |
| `docs/architecture/202605120000-adr-archtest-process-isolation.md` | **新建** ADR | ~80 行 |
| `docs/backlog.md` | close 2 条 + downgrade 1 条 | ~10 行 |

**总计**：约 300-400 行净变更。**0 行 archtest 规则代码改动**——只是 CI 编排和元规则。

---

## 6. 风险与缓解

| 风险 | 等级 | 缓解 |
|---|---|---|
| 新加 tools 子包时忘记加入 tools shard `pkgs` 列表 → 该子包测试不跑 | M | `TOOLS-SHARD-PKG-COVERAGE-01` archtest 元规则（Phase 5） |
| `hack/verify-archtest.sh` discovery 漏 test 函数 → silent unenforce | H | `ARCHTEST-VERIFY-COVERAGE-01` archtest 元规则（Phase 5） |
| Sharding modulo 在新增 test 时改变某 test 的 shard 分配 → CI 失败定位变化 | L | 接受；shard 边界不影响正确性，只影响日志位置 |
| Shard 分配不均（某 shard 12 tests / 某 shard 8 tests）→ 某 shard wall time 偏长 | L | 6 shard 对 70 tests 是合理粒度；如不均可改 SHARD_COUNT 或换 weighted partition |
| `make verify-archtest` 本地与 CI 行为不一致 | L | bash script + 显式 env 变量参数化；`act` 本地可重现 |
| `_build-lint.yml` 改 yml 时引入 yaml 语法错 | L | yaml 解析校验 + actionlint（已有）|
| `verify-archtest` job 数量增加（6 个） → 占用 GHA 并发 quota | L | 公开仓 GHA 无限免费；2-core × 6 并发 < ubuntu-latest 默认 quota（20 并发） |
| Phase 5 archtest 元规则本身有 bug → 误报 / 漏报 | M | TDD：元规则 fixture 覆盖正反例；review 第二人确认 |
| K8s 范式描述与实际 K8s 实现细节不一致 | L | doc 引用 K8s `hack/verify-*.sh` 源码 commit hash 锁版本 |

---

## 7. Rollback 计划

若 Phase 3 上线后发现严重问题（archtest false negative / CI flakiness）：

1. **快速 rollback**（10 min）：
   - `_build-lint.yml` tools shard `pkgs` 加回 `./tools/archtest/...`
   - 删 `verify-archtest` job
   - 删 required check 中 `verify-archtest (X)` 6 个条目
   - tools shard 立即回到 hotfix 前状态（OOM SIGTERM，但能 push）
2. **完整 rollback**（30 min）：
   - revert Phase 3 commit
   - `hack/verify-archtest.sh` + Makefile target 保留（无害）
   - archtest 元规则 disable（commented out）
3. **重启 D 路径**：基于 rollback 时收集的具体失败 case 修正 `hack/verify-archtest.sh`，重新进入 Phase 3

---

## 8. 验收 Test Plan

### 8.1 功能等价性

- [ ] `bash hack/verify-archtest.sh` 与 `go test ./tools/archtest/` 在 develop tip 上结果一致（都 PASS / 都 FAIL 同样 cases）
- [ ] 故意引入一处 archtest 违反 → `bash hack/verify-archtest.sh` exit 1 + 报告同 `go test`
- [ ] `bash hack/verify-archtest.sh --shard 0` × 6 次（s=0..5）全跑后 = `bash hack/verify-archtest.sh` 全跑

### 8.2 性能

- [ ] tools shard PR Check wall < 1 min（移走 archtest 后）
- [ ] 单 verify-archtest shard wall < 1.5 min（CI ubuntu-latest 2-core）
- [ ] 单 verify-archtest shard 内存峰值 < 2 GB（`gtime -v` 测量）
- [ ] 6 shard parallel 在 GHA 总 wall < 2 min（独立 job 同时调度）

### 8.3 治理保护

- [ ] `TOOLS-SHARD-PKG-COVERAGE-01` 故意加新 tools 子包不入 yml → 报错
- [ ] `ARCHTEST-VERIFY-COVERAGE-01` 故意改 `hack/verify-archtest.sh` 让 discovery 跳一个 test → 报错
- [ ] `verify-archtest (X)` 是 required check，绿才能 merge

### 8.4 兼容性

- [ ] 开发者本地 `go test ./tools/archtest/...` 仍 100% 工作
- [ ] 开发者本地 `make verify` 含 verify-archtest 跑通
- [ ] 现有 archtest debug 工作流不受影响（开发者直接 `go test -run TestX -v ./tools/archtest/` 仍可单跑）

---

## 9. K8s 范式参照

Kubernetes 项目 verify pattern 参考：

| K8s 路径 | 作用 | GoCell 对应 |
|---|---|---|
| `hack/make-rules/verify.sh` | top-level verify aggregate | `make verify` target |
| `hack/verify-codegen.sh` | 验证 codegen output 与源同步 | 现有 `make verify-codegen`（保留）|
| `hack/verify-openapi-spec.sh` | 验证 OpenAPI schema 一致 | 现有 `make verify-journey`（类似）|
| `hack/verify-typecheck.sh` | go vet 跨包 | 现有 `go vet ./...` |
| **`hack/verify-staticcheck.sh`** | **跑 staticcheck** | **新 `hack/verify-archtest.sh`** ← 本文 |
| `hack/verify-test-files.sh` | test 文件命名约定 | 现有 archtest `*_invariants_test.go` 命名规则 |

K8s 设计要点：
- **独立 process**：每 verify-X.sh 是单独 Go binary 或 shell + go run，跑完释放内存
- **`make verify` aggregate**：CI 入口跑 `make verify`，遍历所有 `verify-*.sh`
- **失败独立**：一个 verify-X 失败不阻塞其他 verify-Y 跑（K8s `--continue-on-failure` flag；本设计选 fail-fast=false 同效）
- **本地可用**：开发者本地 `make verify-codegen` 等同于 CI 跑

ref:
- [Kubernetes hack/make-rules/verify.sh](https://github.com/kubernetes/kubernetes/blob/master/hack/make-rules/verify.sh)
- [Kubernetes Makefile verify target](https://github.com/kubernetes/kubernetes/blob/master/Makefile#L189-L196)
- [Kubernetes hack/verify-staticcheck.sh](https://github.com/kubernetes/kubernetes/blob/master/hack/verify-staticcheck.sh)（与本设计 `hack/verify-archtest.sh` 最接近的范本）

---

## 10. 与其他 follow-up 的关系

| Follow-up | 关系 |
|---|---|
| `PR305-FU-ARCHTEST-SHAREDRESOLVER-AMORTIZATION-01`（C 路径，amortize TestMain） | **本项落地后降级**：amortize 仍能进一步加速（200s → 50-80s 单 process），但不再是 hotfix。降为 P3 触发型（单 shard 突破 2 min wall 时启动） |
| `PR305-FU-SLOWGATE-PIPE-RESTORATION-01`（恢复 slowgate budget gate） | **本项落地后同 PR 一起落地**：slowgate 改成 post-test 文件分析（每 verify-archtest shard 输出 `.json` artifact，verify-archtest job 末尾跑 `slowgate < .json`） |
| PR Check `tools` shard 现状（移走 archtest 后） | **本项落地后**：tools shard wall < 1 min；架构稳定，无需进一步优化 |
| 架构 long-term（E：go/analysis Pass DAG 重写） | **本项不阻挡 E**：D 是进程级隔离（粗粒度），E 是 analyzer DAG（细粒度共享）。E 仍可后续启动，但 ROI 大幅降低（D 已治本） |

---

## 11. 时间表

| Phase | 工时 | 累计 | 关键产出 |
|---|---|---|---|
| Phase 0 | 0.5h | 0.5h | baseline 数据 + 障碍排查 |
| Phase 1 | 2h | 2.5h | `hack/verify-archtest.sh` v1 |
| Phase 2 | 0.5h | 3h | Makefile target |
| Phase 3 | 2h | 5h | CI workflow 改 + 提 PR |
| Phase 4 | 0.5h | 5.5h | required check 配置 |
| Phase 5 | 2-4h | 7.5-9.5h | 2 个 archtest 元规则 |
| Phase 6 | 1h | 8.5-10.5h | 文档 + ADR + backlog 收尾 |
| **Buffer** | 3-5h | 11.5-15.5h | review iterations / unexpected |
| **总计** | **8-16h dev** | **+ 3-4h review** | |

---

## 12. 启动 checklist

启动 D 路径前确认：

- [ ] develop 上 PR #445 尚未 revert（如已 revert，本路径优先级降低）
- [ ] 本 PR #305 hotfix 已合并或已 abort（避免冲突）
- [ ] 当前没有进行中的 PR-Φ 后续 commit（避免 archtest 文件并发改动）
- [ ] 主仓有可用 worktree slot（`worktrees/<NNN>` 路径）
- [ ] `make verify` 在 develop tip 是绿（baseline 干净）

满足以上 → 创建 worktree `worktrees/<NNN>-archtest-verify-process-isolation`，按 §4 phases 顺序执行。
