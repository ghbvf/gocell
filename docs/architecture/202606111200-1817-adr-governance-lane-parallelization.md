# ADR: Governance Strict lane 并行化（bucket fan-out + 单 owner 合并）

- 状态：Accepted
- 日期：2026-06-11
- 关联：#1817（CI: make verify 串行 lane 无背压 → 基线 creep）；衍生自 /fix #1813 的 CI 时长根因分析
- 取代/修订：无（新增）；但**显式重评 #1817 验收标准 AC#3 的威胁矩阵**（见 §AC 重评）

## Context

`make verify`（Governance Strict, `.github/workflows/governance.yml`）是整条 PR CI 的 wall-clock
关键路径。`hack/make-rules/verify.sh` 用 K8s glob-discovery 模型**串行**发现并运行 ~29 个
`hack/verify-*.sh` gate，耗时 = 所有 gate 之和，无并行。6 天内（06-04→06-10）一波新 gate +
既有 gate 长胖把结构 floor 从 ~3m 推到 ~7m。

根因（L3）：**这条「go.work 成员 → 自发现 gate scope → make verify cost」耦合链没有成本闸门。**
单 job 串行 lane 对「加 gate」零背压——每条新 gate 的全部耗时线性叠加到每个 PR 的关键路径，
作者侧"免费"、全员侧线性变慢。`build-test` / `integration-test` lane 早已是并行矩阵，唯独
governance lane 还是串行巨石，成为所有新治理检查的垃圾桶。

此外发现一处 dual path：`_build-lint.yml` 有一个独立并行的 `verify-codegen` job，用**硬编码 step
清单**重跑 codegen/scaffold 系列 gate——这些 gate 同时被 `make verify` 的 glob 发现，于是每个 PR
**双跑**。该硬编码清单还违反 governance lane 珍视的 glob 单源。

## Decision

1. **Bucket fan-out（并行化症状修复）**：每个 `hack/verify-*.sh` 在文件头声明
   `# verify-bucket: <name>` 注解（lowercase kebab）。`governance.yml` 不再是单 job，而是：
   - `generate-buckets` job（纯 bash，~10s）：跑 `verify-bucket-coverage.sh`（漏注解 fail-fast），
     再由注解**派生** PR 矩阵（`gocell::buckets::list`，排除保留桶 `nightly`）。
   - `bucket` matrix job（`fail-fast: false`）：每桶一个 runner，跑 `VERIFY_BUCKET=<bucket> make verify`。
   - `verify` 聚合 job（`name: make verify`，`if: always()`）：保留历史 required-check 名，任一 leg
     非 success 即红——分支保护无需改动。
   - wall-clock = 最慢单 leg（~setup + 最重桶）≈ ≤3m，不再是所有 gate 之和。

2. **单 owner 合并（彻底 + 不向后兼容）**：删除 `_build-lint.yml` 的 `verify-codegen` job。
   codegen/scaffold gate 此后**唯一** owner = `make verify`（glob 单源 + bucket 并行）。
   触发对等：governance（push+PR 到 develop/main/release/**）⊇ verify-codegen（pr-check PR +
   ci push:develop），合并只**扩大**覆盖（push main/release/** 现在也覆盖 codegen），不收窄。

3. **删 magic skip（不留软回退）**：取消旧的 `VERIFY_SKIP=archtest`。archtest 改注解
   `# verify-bucket: nightly`，由 `generate-buckets` 排除出 PR 矩阵；全量 archtest 仍由
   `archtest-nightly.yml`（24-shard）唯一 own。本地 `make verify`（无 env）仍跑 archtest（K=1），不变。

4. **驱动单源**：`hack/lib/buckets.sh` 是读注解的唯一 helper（驱动路由 / 守卫 / 矩阵派生三方共用），
   消除「矩阵第二源 + SYNC 注释」的漂移面。

## 机制与 AI-robust 评级

| 机制 | 载体 | 评级 | 说明 |
|---|---|---|---|
| 加 gate 必须声明可路由 bucket | 注解 marker funnel：驱动 bucket 模式 fail-fast + `generate-buckets` fail-fast + `verify-bucket-coverage.sh` guard 三点机器拦截 | **Medium** | 漏/错注解在 CI 运行时多点硬红，不可静默漏跑。**非 Hard**：违反仍可表达（文件合法存在、只缺一行注释），无 compile/golden/field-freeze；Hard 化路径见下行 |
| 桶划分 anti-vacuity（漏跑 gate → 硬红，AC#4） | `verify-bucket-coverage.sh`（静态守卫）+ `bucket-coverage-selftest.sh`（合成红回归）+ 本 ADR/README（文档契约）= 三件套 | **Medium** | 空目录 / 漏注解 / 重复 / 非法值 / 未观察到自身 均硬红；selftest 含 anti-vacuity 计数锚（项数单源 = `bucket-coverage-selftest.sh` 的 `EXPECTED_CHECKS`，当前 13，勿在此另写数字以免漂移） |
| 矩阵从注解单源派生 | `gocell::buckets::list` + `fromJSON` | **Hard** | 矩阵是注解的派生物，无第二源可漂移（charter §载体#1） |

**未新增 cost 机制**：本 PR **不**引入 per-bucket 成本闸门。这不是「新增了一个 Soft 机制」
（charter「Soft 严禁立项」针对的是*新增* Soft enforcement），而是 owner 显式选择**不**对 cost 维度
施加 enforcement——见 §AC 重评。

## AC 重评（#1817 验收标准的威胁矩阵）

| AC | 状态 | 依据 / 残余盲区 |
|---|---|---|
| AC#1 governance wall-clock ≤ ~3m | **满足** | bucket fan-out，最慢 leg ≈ setup + 最重桶（archtest-invariants ~93s GHA）≈ ≤3m。**balance 由实测人工保证**，首个 CI run 回灌微调。 |
| AC#2 本地 glob 单源不变（加 gate 零驱动改动） | **满足** | `make verify` 无 env 仍全量串行；`VERIFY_BUCKET` 是 one-time 基建，加 gate 只需新文件 + 注解，不改驱动。 |
| AC#3 新增 gate 无法静默膨胀（机器守卫，非口头） | **部分满足 + cost-bound 显式 descope** | 「非静默」半**满足**：加 gate 必机器化声明 bucket（漏则多点硬红），是可 review 的 conscious 选择，不再是"丢进串行 lane 免费"。「cost-bound」半**主动放弃**：owner 决策（velocity > 严格 CI 闸门）不设成本上限。**残余盲区**：① 新 heavy gate 注入某桶 → 该桶 wall-clock 回归但 CI 仍绿；② 既有 gate 长胖（如 archtest-inv 51→93s）无机器定界。缓解=每 leg job-summary 回显 gate 状态，人工读时间回灌 rebalance。 |
| AC#4 桶划分 anti-vacuity（漏跑某 gate → 硬红） | **满足** | `verify-bucket-coverage.sh` + 驱动 bucket 模式 fail-fast + `generate-buckets` fail-fast；selftest 合成红覆盖漏/错/重/空。 |

威胁模型结论：本 PR 关闭「串行无并行」与「加 gate 静默漏跑」两类盲区；**有意保留**「cost 无机器
定界」一类（velocity 取舍），并以 job-summary 可视性 + 人工回灌作为该残余的缓解，而非 enforcement。

## Alternatives considered

- **实测 per-bucket wall-clock CAP（Hard 成本闸门）**：抓新增+长胖，不可低报；但共享 runner 上
  generous CAP 仍有罕见抖动误红（同 slowgate 风险）。owner 决策**不做闸门**以免 CI 卡开发进度。
- **声明 `# verify-budget` + 静态 Σ-cap（Medium 成本闸门）**：零 flaky，但漏「长胖」且多 29 个 budget
  待维护。同被 owner descope。
- **静态矩阵列表 in YAML + 守卫 grep 交叉校验**：省 `generate-buckets` job，但引入矩阵第二源 +
  SYNC 脆弱性。否决，选注解单源派生（更优雅、charter §载体#1）。
- **保留 codegen 双跑（Q1=A）**：blast radius 最小但留 dual path。否决，选单 owner 合并（彻底）。

## Consequences

- 正面：governance wall-clock 大幅下降；codegen 不再双跑（省 CI 分钟）+ glob 单源 owner 唯一；
  加 gate 必声明 bucket 让成本归属可见可 review；删 magic skip 与硬编码 step 清单，少两处隐式耦合。
- 代价/运维：分支保护 required check 名仍是「Governance Strict / make verify」（聚合 job 保名），
  matrix leg 另有「Governance Strict / inv」等独立 check（非 required，advisory 可见）。bucket balance
  非机器保证——新加重 gate 或既有 gate 长胖会让某 leg 变慢而 CI 仍绿，需人工读 job-summary 回灌。
- 后续（非本 PR）：CodeQL(4.28m) 在本 lever 后成新地板（issue 二阶项，单列）。
