# Implementation Plan: kernel/reconcile L4 Desired-State 收敛控制环

**Feature ID**: `661-kernel-reconcile`
**Date**: 2026-05-26
**Status**: **PARKED-ON-TRIGGER**（trigger 未满足，本计划冻结，等触发后激活）
**Spec**: [202605262359-661-kernel-reconcile-spec.md](./202605262359-661-kernel-reconcile-spec.md)
**Issue**: [#661](https://github.com/ghbvf/gocell/issues/661)

---

## Summary

把 `kernel/command.Sweeper` + `runtime/command.SweeperLifecycle`（共 ~1300 LoC + tests）泛化为通用 `kernel/reconcile` 控制环（controller-runtime `Reconciler` 形态：level-triggered + RequeueAfter + framework-managed backoff + leader-elect），保留 3 件套最小核（Reconciler interface / Result.RequeueAfter / PermanentError），删除 K8s 特有抽象（Informer / Predicate / Manager / Builder.For）。`kernel/command.Sweeper` 同步迁为首个示例实现，`examples/iotdevice` 同步迁移端到端验证。

## Technical Context

**Language/Version**: Go 1.22+（与 GoCell 一致）
**Primary Dependencies**: 标准库 + `pkg/errcode` + `pkg/validation` + `kernel/metautil`（kernel 层依赖收口）
**Storage**:
- Reconciler 状态：消费方自管（pkicell/mdmcell DB schema）
- LeaderElector lease：adapters/redis（SETNX + EXPIRE）/ adapters/postgres（pg_try_advisory_lock）；lease store 维护单调 `Epoch`（fencing token）
- Fencing：消费方写经 epoch-bound `FencedWriter`，资源行记「已见最高 epoch」+ CAS 拒 stale（leader election 非 fencing，见 ADR §4.3）
**Testing**:
- table-driven test 覆盖率 kernel/ ≥ 90% / 其余 ≥ 80%（CLAUDE.md）
- `reconciletest.ConformanceFactory` 收口（kernel/command/commandtest 同形态）
- archtest ≥ 5 条 invariants（接口字段 frozen + carve-out + funnel）
**Target Platform**: Linux server（与 GoCell 一致）
**Project Type**: kernel library + adapter（无 HTTP / 无 CLI）
**Performance Goals**:
- 单 Loop tick scan 1k 非终态 entity ≤ 100ms（与 `kernel/command.Sweeper` 现状对齐）
- leader 流转 RTO ≤ LeaseDuration + 1s（默认 LeaseDuration=15s）
- Reconcile panic 不影响其他 entity（隔离 ≥ 99.99%）
**Constraints**:
- kernel/reconcile 不依赖 runtime/ / adapters/ / cells/（CLAUDE.md 分层）
- LeaderElector 接口在 kernel 声明 / 实现在 adapter
- 不向后兼容（`SweeperLifecycle` 命名整体删除，由 archtest 守名 frozen）
**Scale/Scope**:
- 短期消费方 ≤ 5（pkicell.rotation / mdmcell.command / devicelifecycle.cronsweep / zerotrust.trustscore + example）
- 单 reconciler 处理 entity 上限：1M 行（与 PG/Redis 上限对齐，不引入额外限制）

## Constitution Check（GATE）

参考 `.specify/memory/constitution.md` + `.claude/rules/gocell/ai-robust.md`：

| Gate | 通过判定 | 本计划 |
|------|---------|--------|
| 分层约束 | kernel/ 不依赖 runtime/adapters/cells/ | ✅ kernel/reconcile 仅依赖 pkg/errcode + pkg/validation + kernel/metautil |
| 一致性级别 | 明确 L0-L4 归属 | ✅ L4 DeviceLatent（issue 第一性原理重评结论） |
| AI-robust 评级 | 新增 enforcement 机制 ≥ Medium | ✅ 接口字段 frozen + carve-out 走 Hard archtest；funnel 评级见 PR-A5/A6 |
| 不向后兼容 | 删字段/改签名直接做 | ✅ `SweeperLifecycle` / `SweepTicker` 命名完全删除，由 archtest 锁 frozen |
| 优雅简洁 | 不引入新抽象层 | ✅ 删 6 个 K8s 特有抽象（Source/Predicate/Manager/Leader/Builder.For/Priority） |
| 测试覆盖 | kernel/ ≥ 90% | ✅ Conformance harness + table-driven，每 PR TDD |
| 文档命名 | yyyyMMddHHmm-编号-描述.md | ✅ 本目录全部满足 |

**违反**：无；本计划无 Constitution 例外。

## Project Structure

### Documentation (this feature)

```text
docs/plans/later/
├── 202605262359-661-kernel-reconcile-spec.md   # WHAT/WHY
├── 202605262359-661-kernel-reconcile-plan.md   # 本文档（HOW + PR 切分）
└── 202605262359-661-kernel-reconcile-tasks.md  # PR 级 task list
```

### Source Code Layout（trigger 满足后实施）

```text
kernel/
├── reconcile/                           # 新增（v1 主体）
│   ├── doc.go                           # 包文档 + INVARIANT 锚点
│   ├── reconciler.go                    # Reconciler interface + Request/Result struct
│   ├── result.go                        # Result + PermanentError + Ack/Requeue/Reject 工厂
│   ├── loop.go                          # Loop（调度环；从 SweeperLifecycle 平移）
│   ├── trigger.go                       # Trigger interface + TickerTrigger + ChannelTrigger
│   ├── backoff.go                       # 指数退避（baseDelay=5ms / maxDelay=1000s）
│   ├── builder.go                       # New(reconciler).WithTrigger(...).Build() 最小 DSL
│   ├── leader.go                        # LeaderElector interface + LeaseToken（含单调 Epoch fencing token）
│   ├── fenced.go                        # FencedRepository / FencedWriter（epoch-bound 写面 + CAS，PR-A6）
│   ├── metrics.go                       # 4 个 metric wiring（counter/histogram/gauge）
│   └── reconciletest/                   # conformance harness
│       ├── conformance.go               # HarnessFactory + Wiring + Features → RunConformance 跑遍 basic/RequeueAfter/Permanent/panic/MaxConcurrent/leader/fencing
│       └── fake.go                      # 测试 fake（fake LeaderElector + fake Trigger）
│
├── command/                             # 现有；改 Sweeper 实现 Reconciler 接口
│   ├── sweeper.go                       # 改：实现 reconcile.Reconciler
│   └── ...                              # 其他文件不动
│
adapters/
├── redis/
│   └── reconcile_leader.go              # 新增：SETNX-based LeaderElector
└── postgres/
    └── reconcile_leader.go              # 新增：pg_try_advisory_lock-based LeaderElector

runtime/
└── command/
    └── lifecycle.go                     # 改：变薄 adapter 调 kernel/reconcile.Loop（或整体删，由 PR-A8 定）

examples/
└── iotdevice/
    └── cells/devicecell/
        └── cell.go                      # 改：从 SweeperLifecycle 切换到 kernel/reconcile.Loop

tools/archtest/
├── reconcile_interface_frozen_test.go         # 新增（PR-A2）
├── reconcile_result_fields_frozen_test.go     # 新增（PR-A2）
├── reconcile_loop_clock_carveout_test.go      # 新增（PR-A4）
├── reconcile_leader_interface_frozen_test.go  # 新增（PR-A6）
├── reconcile_fenced_write_funnel_test.go       # 新增（PR-A6：RECONCILE-FENCED-WRITE-FUNNEL-01）
└── reconcile_builder_funnel_test.go           # 新增（PR-A7）

.claude/rules/gocell/
└── reconcile.md                         # 新增（PR-A10）

docs/architecture/
└── 202707xxxxxx-adr-kernel-reconcile-design.md  # 新增（PR-A1）
```

**Structure Decision**: 在 `kernel/` 下新建 `reconcile/` 包 + `reconciletest/` 子包，遵循现有 `kernel/command + commandtest` 形态；adapter 实现在 `adapters/{redis,postgres}/reconcile_leader.go`，与现有 `adapters/postgres/command_queue.go` 平行。

---

## PR 切分（10 个 PR，每个 ≤ 2000 LoC）

切分原则：
- **依赖最小化**：PR-A1 写 ADR 与骨架占位 → PR-A2 接口 frozen → PR-A3+ 主体逐块加
- **每 PR 可独立 review**：单 PR 不超 1500 LoC（留 25% 余量到 2000 上限）
- **TDD 双轨**：每 PR 同 commit 内含 production 代码 + 单测；archtest invariants 单独 PR（PR-A2/A4/A6/A7）
- **可中途暂停**：每 PR merge 后 trunk 仍可发布，无中间态破裂

### PR 总览表

| PR | 标题 | 估算 LoC | 依赖 | 主要文件 | 并行批次 |
|----|------|---------|------|---------|---------|
| A1 | docs: ADR kernel/reconcile 设计 | 600 | — | `docs/architecture/*-adr-kernel-reconcile-design.md` + spec 链接 | **B1** |
| A2 | feat: Reconciler interface + Request/Result + PermanentError + archtest frozen | 700 | A1 | `kernel/reconcile/{reconciler,result}.go` + tests + 2 archtest | **B2** |
| A3 | feat: Loop 调度骨架（从 SweeperLifecycle 平移）+ metrics 4 件 | 1400 | A2 | `kernel/reconcile/{loop,metrics}.go` + tests | **B3** |
| A4 | feat: Trigger interface + TickerTrigger + ChannelTrigger + Loop clock carve-out archtest | 1000 | A3 | `kernel/reconcile/{trigger}.go` + tests + 1 archtest | **B4**（与 A5 并行）|
| A5 | feat: Backoff（指数退避）+ panic recovery + 错误分类 transient/permanent | 900 | A2 | `kernel/reconcile/{backoff,recovery}.go` + tests | **B4**（与 A4 并行）|
| A6 | feat: LeaderElector（含单调 Epoch fencing token）+ FencedWriter 写路径 CAS + Redis/PG 实现 + archtest frozen + fencing conformance | 1900 | A3 | `kernel/reconcile/{leader,fenced}.go` + `adapters/{redis,postgres}/reconcile_leader.go` + tests + 2 archtest（leader frozen + fenced-write funnel）| **B5** |
| A7 | feat: Builder DSL + reconciletest.ConformanceFactory + funnel archtest | 1100 | A4/A5/A6 | `kernel/reconcile/{builder}.go` + `kernel/reconcile/reconciletest/{conformance,fake}.go` + 1 archtest | **B6** |
| A8 | refactor: kernel/command.Sweeper 实现 reconcile.Reconciler + runtime/command/lifecycle.go 删除 | 1200 | A7 | `kernel/command/sweeper.go` + `runtime/command/lifecycle.go`（删）+ tests + archtest 名字 frozen 改写 | **B7** |
| A9 | refactor: examples/iotdevice 切换到 kernel/reconcile.Loop 端到端验证 | 800 | A8 | `examples/iotdevice/cells/devicecell/cell.go` + e2e tests | **B7**（与 A8 同批次，并行可能） |
| A10 | docs: `.claude/rules/gocell/reconcile.md` + 现有 ADR amendment + CLAUDE.md 引用 | 500 | A9 | `.claude/rules/gocell/reconcile.md` + `docs/architecture/202605170000-*.md` amendment | **B8** |

**LoC 总计估算**：≈ 9700 LoC（含 production + tests + archtest + docs）

### 批次并行度图

```text
B1 (PR-A1) ────────────────────┐
                               ↓
                          B2 (PR-A2)
                               ↓
                          B3 (PR-A3)
                               ↓
              ┌─────────── B4 ──────────┐
              ↓                          ↓
        PR-A4 (Trigger)          PR-A5 (Backoff)
              └───────────┬─────────────┘
                          ↓
                     B5 (PR-A6 LeaderElector)
                          ↓
                     B6 (PR-A7 Builder + Conformance)
                          ↓
              ┌─────────── B7 ──────────┐
              ↓                          ↓
        PR-A8 (command 迁移)      PR-A9 (examples 迁移)
              └───────────┬─────────────┘
                          ↓
                     B8 (PR-A10 Docs)
```

**并行机会**：
- B4 内：PR-A4 / PR-A5 不改同一文件，可并行（两个 developer agent）
- B7 内：PR-A8 / PR-A9 改不同包（kernel/command vs examples/），可并行；但 A9 依赖 A8 已 merge 的 reconcile.Reconciler，建议先 A8 后 A9 安全起见

### 每 PR LoC 详细估算（含 tests + archtest + docs）

| PR | Production | Tests | archtest | Docs | 合计 | 距 2000 余量 |
|----|-----------|-------|---------|------|-----|-------------|
| A1 | 0 | 0 | 0 | 600 | **600** | 1400 |
| A2 | 200 | 350 | 100 | 50 | **700** | 1300 |
| A3 | 600 | 700 | 0 | 100 | **1400** | 600 |
| A4 | 350 | 500 | 100 | 50 | **1000** | 1000 |
| A5 | 350 | 450 | 0 | 100 | **900** | 1100 |
| A6 | 600 | 700 | 100 | 100 | **1500** | 500 |
| A7 | 400 | 500 | 100 | 100 | **1100** | 900 |
| A8 | 400 | 500 | 200 | 100 | **1200** | 800 |
| A9 | 300 | 400 | 0 | 100 | **800** | 1200 |
| A10 | 0 | 0 | 0 | 500 | **500** | 1500 |

**最高 PR**：A6（LeaderElector + 2 adapter 实现） @ 1500，余量 500 LoC，安全可控

### PR 切分判定（每条满足才能切分）

- ✅ 单 PR 净增删 ≤ 2000 行（最高 A6 @ 1500，留 25% 余量到上限）
- ✅ 单 PR 可独立 review（不存在「半实现」状态）
- ✅ 单 PR merge 后 trunk 可发布（不破坏现有 build / test）
- ✅ 每 PR 含 TDD 测试（覆盖率 kernel/ ≥ 90%）
- ✅ archtest invariants 在引入实体的同一 PR 内落（不留 follow-up）
- ✅ archtest carve-out 同 PR 内落（不留 follow-up）
- ✅ 删除性变更（PR-A8 删 runtime/command/lifecycle.go）同 PR 内含调用方迁移

### 接口冻结时机

| 接口 | 冻结 PR | archtest invariant ID（候选） |
|------|--------|----------------------------|
| `Reconciler.Reconcile(ctx, Request) (Result, error)` | A2 | RECONCILE-INTERFACE-FROZEN-01 |
| `Request` 字段集 `{EntityID string}` | A2 | RECONCILE-REQUEST-FIELDS-FROZEN-01 |
| `Result` 字段集 `{RequeueAfter time.Duration}` | A2 | RECONCILE-RESULT-FIELDS-FROZEN-01 |
| `Trigger.Start(ctx, queue) error` | A4 | RECONCILE-TRIGGER-INTERFACE-FROZEN-01 |
| `Loop` clock 字段集（无 wall-clock 字段） | A4 | RECONCILE-LOOP-CLOCK-CARVEOUT-01（mirror PROD-CLOCK-INJECTION-01） |
| `LeaderElector` 三方法签名 | A6 | RECONCILE-LEADER-INTERFACE-FROZEN-01 |
| `Builder` funnel（消费方构造 Loop 必经 Builder） | A7 | RECONCILE-BUILDER-FUNNEL-01 |
| 命名 frozen（删除 SweeperLifecycle / SweepTicker） | A8 | ~~RECONCILE-NAMING-FROZEN-01~~ **WON'T-DO**（grep-of-deleted-name = Soft，ai-robust「Soft 严禁立项」）。改由类型删除（Hard，编译错误）+ 既有 RECONCILE-BUILDER-FUNNEL-01 + 编译期 `var _ reconcile.Reconciler = (*command.Sweeper)(nil)` 断言 + 一次性 merge-gate grep 守。详见 tasks T38 + 设计 ADR §8。 |

---

## TDD 测试先写清单（每 PR 阶段 4 必须先 fail）

| PR | 第一条 TDD test | 期望 fail 信号 |
|----|---------------|---------------|
| A2 | `TestReconciler_RequestEntityIDOnly` | undefined: reconcile.Request |
| A3 | `TestLoop_StartTickerStopGraceful` | undefined: reconcile.Loop |
| A4 | `TestTickerTrigger_EmitsAtInterval` | undefined: reconcile.TickerTrigger |
| A5 | `TestBackoff_ExponentialBounded` | undefined: reconcile.expBackoff |
| A6 | `TestLeaderElector_AcquireExclusive` | undefined: reconcile.LeaderElector |
| A7 | `TestBuilder_RequiresReconcilerAndTrigger` | undefined: reconcile.New |
| A8 | `TestCommandSweeper_ImplementsReconciler` | type assertion fails: `var _ reconcile.Reconciler = (*command.Sweeper)(nil)` |
| A9 | `TestIotDevice_E2EReconcileCommand` | wiring 旧 SweeperLifecycle 已删，新 Loop 未挂 |

---

## 参考框架（每 PR commit message 必须含）

- ref: kubernetes-sigs/controller-runtime pkg/reconcile/reconcile.go@main
- ref: kubernetes-sigs/controller-runtime pkg/internal/controller/controller.go@main
- ref: kubernetes-sigs/controller-runtime pkg/builder/controller.go@main
- ref: gocell kernel/command/sweeper.go（既有 L4 控制环实现，平移基础）
- ref: gocell runtime/command/lifecycle.go（SweeperLifecycle，调度骨架基础）

详见 `docs/references/framework-comparison.md`。

---

## Complexity Tracking

| Violation | Why Needed | Simpler Alternative Rejected Because |
|-----------|------------|-------------------------------------|
| 新建 kernel/ 子包（kernel/reconcile） | 真消费方 ≥ 4（pkicell/mdmcell/devicelifecycle/zerotrust），现有 kernel/command 强耦合命令实体无法复用 | 在 kernel/command 内泛化：会让 command 包暴露与命令无关的接口，破坏单一职责；且 kernel/command 已 PR-A8 迁出 |
| Builder DSL（PR-A7） | funnel 上游 Hard：消费方构造 Loop 必经 Builder，使「裸 NewLoop 直构」编译期不可表达（Loop 构造函数私有化 + Builder 是唯一公开入口） | 直接 NewLoop(opts) public 构造：funnel 上游 Soft，无法防止消费方绕过 metric/leader/backoff wiring |
| 新建 archtest invariants × 5+ | 接口字段 frozen + carve-out + funnel 三类 enforcement 均需 archtest 兜底；ai-robust 评级 Medium-Hard | 不写 archtest：消费方可随意改 Request/Result 字段集，本 spec 的接口最小性 SC-001 立即失守 |
| LeaderElector 接口在 kernel 层声明 / 实现在 adapter | 接口归属遵循 kernel-driven adapter-implements 模式（对齐 outbox.Emitter / persistence.CellTxManager） | 接口下沉到 adapter：kernel/reconcile 无法在 Loop 内调用 lease 抽象，需要消费方在每个 cell 重复 wiring |
| `runtime/command/lifecycle.go` 整体删除（PR-A8） | 不向后兼容是 GoCell 治理原则（CLAUDE.md），且本 spec 明确不留 SweeperLifecycle 旧名 | 保留 alias：会创建两个 control loop 抽象，违反 ai-robust §"消除字面约定" |

---

## Risk & Mitigation

| 风险 | 影响 | 缓解 |
|------|------|------|
| trigger 满足前 `kernel/command` 不兼容重构 | 本计划 baseline 偏移，PR-A8 迁移成本高 | spec.md A2 假设已声明；trigger 启动时先 amend spec/plan，再执行 |
| controller-runtime 在 trigger 前接口大改 | 对标失效，PR-A2/A3 设计要调 | 本计划锁定 controller-runtime 当前形态作为对标快照（ref hash 在 PR-A1 ADR 中固定）；调整成本接受 |
| LeaderElector 在 Redis/PG 之外有新需求（如 etcd） | adapter 层需扩展 | 接口设计抽象到 LeaseToken 中立形态，扩 adapter 不改 kernel；扩 adapter 时再开新 issue |
| examples/iotdevice 迁移破坏端到端 | PR-A9 阻塞 | 在 PR-A9 前先在 fake adapter 里跑过 conformance；PR-A9 单独 e2e test 守 |
| 单 PR 超 2000 行（A6：leader + epoch fencing + 2 adapter + conformance） | review 压力大 | A6 内分三段 sub-commit（同 PR）：①LeaderElector + Epoch ②FencedWriter/CAS + funnel archtest ③redis/PG adapter；让 reviewer 按段 review；fencing 必须与 leader 同 PR（否则留「lease 即 fencing」错觉窗口）|

---

## 激活前 Checklist（trigger 满足时执行）

- [ ] 核验 trigger T1/T2/T3/T4 满足条件（≥ 2 个生产 cell 落地或 T4 + 任一）
- [ ] 核验 `kernel/command` baseline 未发生不兼容重构（无则 amend spec）
- [ ] 核验 controller-runtime 对标快照仍有效（无大变则 OK，否则 PR-A1 ADR 修订）
- [ ] 开发者拉本目录文档 + 创建新 implementation plan（引用本计划，加 active 时间戳）
- [ ] 按 PR-A1 到 PR-A10 顺序执行（B4/B7 内可并行）
- [ ] 每 PR 满足"PR 切分判定"6 条
- [ ] PR-A10 merge 后关闭 issue #661
