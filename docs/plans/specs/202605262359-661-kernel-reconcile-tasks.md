# Tasks: kernel/reconcile L4 Desired-State 收敛控制环

**Feature ID**: `661-kernel-reconcile`
**Status**: **IMPLEMENTED-HISTORICAL**（已落地历史任务；A1-A10 已闭环）
**Input**: [spec.md](./202605262359-661-kernel-reconcile-spec.md) + [plan.md](./202605262359-661-kernel-reconcile-plan.md)
**Issue**: [#661](https://github.com/ghbvf/gocell/issues/661)
**Prerequisites**: 历史前置已被 ADR-661 amendments + ADR-1895 重定义；当前后续消费方引用 ADR-661 / ADR-1895 / `kernel/reconcile/doc.go`

---

## 历史任务清单使用说明

本 tasks 列表保留 A1-A10 的历史 PR 切分与 provenance。`kernel/reconcile` 基建、command 迁移、
examples/iotdevice 消费方与文档治理已闭环；后续新增业务 cell 消费方不再通过修改本文件启动，
而是引用当前 ADR / godoc invariants 独立立项。

---

## Format: `[T-IDx] [P?] [PR] Description`

- **[T-IDx]**: Task ID（T01-T70）
- **[P]**: 可与同批次其他 [P] 标记 task 并行
- **[PR]**: 归属 PR（A1-A10）
- 每条 task 含：改动文件 + 验收信号 + 估算 LoC

## Path Conventions

- kernel: `kernel/reconcile/`（新建）+ `kernel/command/`（改）
- runtime: `runtime/command/`（删 lifecycle.go）
- adapters: `adapters/{redis,postgres}/reconcile_leader.go`（新增）
- examples: `examples/iotdevice/cells/devicecell/`（改 wiring）
- archtest: `tools/archtest/reconcile_*_test.go`（新增）
- docs: `docs/architecture/` + `.claude/rules/gocell/`

---

## Phase 1: Setup（B1）

### PR-A1 — docs: ADR kernel/reconcile 设计

**Goal**: 落地架构决策文档，固定对标 controller-runtime 快照与 3 件套最小核决策；后续 PR 引用本 ADR。

**Independent Test**: ADR 在 review 后 merge，不阻塞 build/test；规则文件未引用 ADR 不算 ship。

#### Tasks

- [ ] **T01** [PR-A1] 创建 ADR `docs/architecture/<timestamp>-adr-kernel-reconcile-design.md`：
  - §0 决策摘要（3 件套最小核 + 删除 K8s 抽象清单）
  - §1 问题陈述（kernel/command Sweeper 域绑定 → 4 个真消费方需要泛化）
  - §2 对标 controller-runtime（含 5 个 raw.githubusercontent.com URL + 关键代码片段 + GoCell 适配建议）
  - §3 接口设计（Reconciler / Request / Result / Trigger / LeaderElector / Builder）
  - §4 leader-elect 设计（Redis SETNX + PG advisory lock 两种 adapter，lease/token 模型）
  - §5 与 saga (#969) / projection harness (#1079) 边界
  - §6 trigger 满足条件 + 激活流程
  - §7 威胁矩阵（接口被错误泛化 / leader 流转失败 / panic 隔离失败 / 多 cell 并发）
  - §8 不向后兼容声明（SweeperLifecycle / SweepTicker 完全删除）
  - 估算：600 LoC（markdown）

**Checkpoint A1**: ADR merge，#661 加 ADR 引用链接

---

## Phase 2: Foundational（B2 + B3）

### PR-A2 — feat: Reconciler interface + Request/Result + PermanentError + archtest frozen

**Goal**: 落地 3 件套最小核：Reconciler interface + Request/Result 字段集 + PermanentError marker；同 PR 含 2 条字段 frozen archtest。

**Independent Test**: `var _ kernel/reconcile.Reconciler = (*fakeReconciler)(nil)` 编译通过；`Result.RequeueAfter = 5s` 不报错；archtest `RECONCILE-INTERFACE-FROZEN-01` 与 `RECONCILE-RESULT-FIELDS-FROZEN-01` 在 nightly 跑过。

#### Tests for PR-A2 (TDD 先写)

- [ ] **T02** [P] [PR-A2] `kernel/reconcile/reconciler_test.go`：
  - `TestReconciler_RequestEntityIDOnly`：构造 `Request{EntityID: "x"}`，验证字段集只有 EntityID
  - `TestReconciler_ResultRequeueAfterZeroSemantic`：`Result{}` 零值等价 default tick
  - 期望先 fail：`undefined: reconcile.Reconciler` / `undefined: reconcile.Request`
  - 估算：150 LoC
- [ ] **T03** [P] [PR-A2] `kernel/reconcile/result_test.go`：
  - `TestPermanentError_IsClassifiedNonRetry`：`PermanentError(errors.New("x"))` 经 `IsPermanent` 返回 true
  - `TestResult_RequeueAfter_PositiveBound`：负值 RequeueAfter 视为 0（无 panic）
  - 估算：200 LoC

#### Implementation for PR-A2

- [ ] **T04** [PR-A2] `kernel/reconcile/reconciler.go`：
  - `type Reconciler interface { Reconcile(ctx context.Context, req Request) (Result, error) }`
  - `type Request struct { EntityID string }`（注释锁字段集 // INVARIANT: RECONCILE-REQUEST-FIELDS-FROZEN-01）
  - 估算：80 LoC（含包文档 doc.go 部分）
- [ ] **T05** [PR-A2] `kernel/reconcile/result.go`：
  - `type Result struct { RequeueAfter time.Duration }`（注释锁字段集 // INVARIANT: RECONCILE-RESULT-FIELDS-FROZEN-01）
  - `func PermanentError(err error) error`（marker）
  - `func IsPermanent(err error) bool`
  - 估算：120 LoC
- [ ] **T06** [PR-A2] `tools/archtest/reconcile_interface_frozen_test.go`：
  - `RECONCILE-INTERFACE-FROZEN-01`：reflect 锁 Reconciler 方法集（仅 Reconcile，签名固定）
  - `RECONCILE-REQUEST-FIELDS-FROZEN-01`：reflect 锁 Request 字段集为 {EntityID string}
  - 估算：100 LoC
- [ ] **T07** [PR-A2] `tools/archtest/reconcile_result_fields_frozen_test.go`：
  - `RECONCILE-RESULT-FIELDS-FROZEN-01`：reflect 锁 Result 字段集为 {RequeueAfter time.Duration}
  - 拒绝 Requeue bool / Priority int 等 K8s 残留字段
  - 估算：与 T06 合并到同文件（计入 T06 LoC）

**Checkpoint A2**: 接口与字段集冻结；后续 PR 不可改 Reconciler/Request/Result 字段

---

### PR-A3 — feat: Loop 调度骨架 + metrics 4 件

**Goal**: 把 `runtime/command.SweeperLifecycle` 调度骨架（451 LoC）平移到 `kernel/reconcile.Loop`，去掉命令实体耦合；wiring 4 个 metric。

**Independent Test**: `Loop` 在 fake Reconciler + fake Trigger 下 Start/Stop 不泄漏 goroutine（goroutine leak detector）；metric counter/histogram 在 Reconcile 调用后值增。

#### Tests for PR-A3 (TDD)

- [ ] **T08** [P] [PR-A3] `kernel/reconcile/loop_test.go`：
  - `TestLoop_StartTickerStopGraceful`：Start + ctx cancel + Stop 无 goroutine 泄漏
  - `TestLoop_AwaitProbeUnblocksOnStart`：probe channel 在 Start 后释放
  - `TestLoop_StopTimeoutKillsRunningReconcile`：长时 Reconcile 在 StopTimeout 内被切断
  - `TestLoop_MaxConcurrencyRespected`：MaxConcurrentReconciles=2 时只 2 个并行
  - `TestLoop_SameEntityIDSerial`：同 ID 串行（不重入）
  - 期望先 fail：`undefined: reconcile.Loop`
  - 估算：500 LoC（table-driven）
- [ ] **T09** [P] [PR-A3] `kernel/reconcile/metrics_test.go`：
  - 4 个 metric wiring 验证（counter/histogram/gauge with label = reconcilerID）
  - `TestMetrics_PreflightValidate`：参考 `runtime/command.preflightSweepErrorCounter`
  - 估算：200 LoC

#### Implementation for PR-A3

- [ ] **T10** [PR-A3] `kernel/reconcile/loop.go`：
  - 平移 `runtime/command.SweeperLifecycle` 的 Start/Stop/runLoop/awaitProbe（≈ 360 LoC 平移）
  - 改名 `SweepTick` → `Reconcile`、`SweepErrorCounter` → `reconcileErrorCounter`
  - 注入 Reconciler + Trigger + LeaderElector + Backoff 配置
  - 同 ID 串行用 sync.Map[entityID]struct{} 标记
  - MaxConcurrentReconciles semaphore
  - 估算：500 LoC
- [ ] **T11** [PR-A3] `kernel/reconcile/metrics.go`：
  - 4 个 metric：`reconcile_total{reconciler,result}` / `reconcile_duration_seconds{reconciler}` / `reconcile_in_flight{reconciler}` / `reconcile_leader{reconciler}`
  - PreflightValidate helper
  - 估算：100 LoC
- [ ] **T12** [PR-A3] `kernel/reconcile/doc.go`：
  - 包文档 INVARIANT 锚点（覆盖 5+ archtest）
  - Reconciler 实现模式示例
  - 估算：100 LoC

**Checkpoint A3**: Loop 可在 fake 注入下跑通 Start/Stop；metric 可观测

---

## Phase 3: Triggers + Backoff（B4 — 并行）

### PR-A4 — feat: Trigger interface + TickerTrigger + ChannelTrigger + interface-frozen archtest

**Goal**: 抽象触发源（最小 2 种实现）；TickerTrigger 节拍走注入 `clock.Clock`（无 carve-out，因 ticker 不调 stdlib `time.*`，`PROD-CLOCK-INJECTION-01` 保持 GREEN）。

**Independent Test**: `TickerTrigger(clk, 1*time.Second)` 在 1s 内触发 1 次（业务时钟可注入测试不依赖 wall-clock）；`ChannelTrigger(ch)` 在 ch 写入 Request 后立即调度。

#### Tests for PR-A4 (TDD)

- [ ] **T13** [P] [PR-A4] `kernel/reconcile/trigger_test.go`：
  - `TestTickerTrigger_EmitsAtInterval`：使用注入业务时钟
  - `TestTickerTrigger_RespectsCtxCancel`
  - `TestChannelTrigger_PassesRequest`：写 ch → reconciler 收 EntityID
  - `TestChannelTrigger_BlockedOnFull`：channel full 时 backpressure 不丢
  - 估算：500 LoC

#### Implementation for PR-A4

- [ ] **T14** [PR-A4] `kernel/reconcile/trigger.go`：
  - `type Trigger interface { Start(ctx context.Context, queue chan<- Request) error }`
  - `func TickerTrigger(clk clock.Clock, interval time.Duration) Trigger`（clock 强制位置参 per `CLOCK-POSITIONAL-INJECTION-01`）
  - `func ChannelTrigger(in <-chan Request) Trigger`
  - 估算：250 LoC
- [ ] **T15** [PR-A4] clock carve-out archtest（取消）：
  - TickerTrigger 节拍走注入 `clock.Clock`，不调 stdlib `time.*`，故无需 mirror `PROD-CLOCK-INJECTION-01` 的白名单；clock discipline 由既有 `CLOCK-POSITIONAL-INJECTION-01`（漏传 clk = 编译错误）守，`PROD-CLOCK-INJECTION-01` 保持 GREEN
  - 估算：0 LoC（删除原计划的 `reconcile_loop_clock_carveout_test.go`）
- [ ] **T16** [PR-A4] `tools/archtest/reconcile_invariants_test.go`：
  - `RECONCILE-TRIGGER-INTERFACE-FROZEN-01`：reflect 锁 Trigger 接口（含 send-only sink 方向）+ 反向盲区自检；并入既有 reconcile 主题文件
  - 估算：50 LoC

**Checkpoint A4**: Trigger 接口 frozen；TickerTrigger 业务时钟可注入

---

### PR-A5 — feat: Backoff + panic recovery + 错误分类

**Goal**: 指数退避（baseDelay=5ms, maxDelay=1000s）+ panic recovery + 错误分类（transient/permanent）。

**Independent Test**: 20 次连续 transient error 触发指数退避 → 第 20 次延迟达 maxDelay；panic 转 transient error 在 metric 可见，不杀进程。

#### Tests for PR-A5 (TDD)

- [x] **T17** [P] [PR-A5] `kernel/reconcile/backoff_test.go`：
  - `TestBackoff_ExponentialBounded`：第 N 次延迟 = min(baseDelay * 2^N, maxDelay)
  - `TestBackoff_ResetOnSuccess`：success 后下次退避归零
  - 估算：200 LoC
- [x] **T18** [P] [PR-A5] `kernel/reconcile/recovery_test.go`：
  - `TestRecovery_PanicConvertsToError`：reconciler panic 被 catch + 转 error
  - `TestRecovery_PanicMetricRecorded`：recovered panic 计入 reconcile_total{result="transient"}（对齐 FR-009「panic → transient error」+ FR-010 四标签集 success/transient/permanent/skipped；**不**新增 result="panic" 第 5 标签）
  - `TestRecovery_OtherEntityNotAffected`：单 entity panic 不影响其他 entity 串行处理
  - 估算：250 LoC

#### Implementation for PR-A5

- [x] **T19** [PR-A5] `kernel/reconcile/backoff.go`：
  - 指数退避实现（与 controller-runtime client-go workqueue 形态对齐）
  - 注入式 baseDelay/maxDelay（默认 5ms / 1000s）
  - 估算：150 LoC
- [x] **T20** [PR-A5] `kernel/reconcile/recovery.go`：
  - panic recover wrapper（与 `kernel/wrapper.WrapConsumer` 形态对齐）
  - 错误分类 dispatcher：err == nil → success / errors.Is(err, PermanentError) → permanent / else → transient
  - 估算：200 LoC

**Checkpoint A5**: ✅ 退避收敛 + panic 隔离验证通过（PR-A5 #1166，merged）。
PR-A5 issue body scope supplement: F5（dirty/processing dedup — in-flight entity marks dirty; on completion re-runs once, coalesced）+ F6（shared heap-based delaying queue — one waitingLoop goroutine + container/heap; transient errors requeue with per-entity exponential backoff 5ms..1000s no-jitter via entityBackoff; success forgets backoff; permanent dead-letters）均在本 PR 交付（不在 T17–T20 原列表，补充于此）。

---

## Phase 4: Leader Election（B5）

### PR-A6 — feat: LeaderElector + epoch fencing（FencedWriter）+ Redis/PG 实现 + archtest frozen

**Goal**: 在 kernel/ 声明 `LeaderElector` 接口（含单调 `Epoch` fencing token）+ `FencedRepository`/`FencedWriter` seam；adapters/{redis,postgres} 各提供一个 leader 实现；接口字段 frozen。**leader election 非 fencing**（client-go 明示），跨副本正确性靠 epoch 写路径 CAS + 消费方幂等（见 ADR §4.3/§4.4），不靠 lease 本身。

**Independent Test**: 在 fake adapter 实现 LeaderElector：两个 Loop 实例并起，**稳态下**只一个调 Reconcile；leader ctx cancel 后 follower 在 LeaseDuration + 1s 内接管。**Fencing real-failure-injection**：旧 leader 以 `Epoch=N` 的 in-flight 写在 follower 以 `Epoch=N+1` 接管后重放，断言写路径 CAS 拒绝 stale-epoch 写、设备无重复命令。

#### Tests for PR-A6 (TDD)

- [x] **T21** [P] [PR-A6] `kernel/reconcile/leader_test.go`：
  - `TestLeaderElector_AcquireExclusive`：两个 fake 实例只一个获得 lease
  - `TestLeaderElector_ReleaseUnblocksFollower`：leader release → follower acquire
  - `TestLeaderElector_RenewKeepsLease`：renew 周期 < LeaseDuration 不丢 lease
  - `TestLeaderElector_StaleFollowerAcquiresOnExpiry`：leader 不 renew → follower 超时接管
  - 估算：400 LoC
- [x] **T22** [P] [PR-A6] `adapters/redis/reconcile_leader_test.go`：
  - 用 miniredis 跑 SETNX 实现的 leader 接口契约（同 conformance test 集）
  - 估算：150 LoC
- [x] **T23** [P] [PR-A6] `adapters/postgres/reconcile_leader_test.go`：
  - 用真 PG（integration tag）跑 advisory lock 实现的 leader 接口契约
  - 估算：150 LoC

#### Implementation for PR-A6

- [x] **T24** [PR-A6] `kernel/reconcile/leader.go`：
  - `type LeaderElector interface { AcquireLease / ReleaseLease / RenewLease }`
  - `type LeaseToken struct { ReconcilerID string; HolderID string; Epoch uint64; AcquiredAt time.Time; ExpiresAt time.Time }`（`Epoch` = 单调 fencing token，每次换持有者 +1）
  - `Loop` 从 lease 派生 lease-scoped ctx + lease 丢失瞬间 cancel（中断 in-flight Reconcile，best-effort 收窄窗口）
  - 估算：180 LoC（接口 + 类型 + lost-lease ctx 接缝 + 文档）
- [x] **T24b** [PR-A6] `kernel/reconcile/fenced.go`：`FencedRepository` / `FencedWriter` seam
  - `Loop` 给每次 `Reconcile` 注入 epoch-bound `FencedWriter`（reconciler 唯一写面，sealed 构造）
  - 写路径 CAS：拒 `incoming_epoch < 资源已见最高 epoch`（Kleppmann monotonic fencing，**非** outbox UUID identity-fencing）
  - 估算：220 LoC
- [x] **T25** [PR-A6] `adapters/redis/reconcile_leader.go`：
  - SETNX + EXPIRE 实现 + 续约 goroutine
  - 复用 adapters/redis Cache 命名空间约定（cell-namespaced key）
  - 估算：250 LoC
- [x] **T26** [PR-A6] `adapters/postgres/reconcile_leader.go`：
  - pg_try_advisory_lock 实现
  - 续约通过 transaction-scoped lock + heartbeat goroutine
  - 估算：250 LoC
- [x] **T27** [PR-A6] `tools/archtest/reconcile_leader_interface_frozen_test.go`：
  - `RECONCILE-LEADER-INTERFACE-FROZEN-01`：reflect 锁 LeaderElector 三方法 + `LeaseToken.Epoch` 字段
  - `RECONCILE-LEADER-IMPL-FUNNEL-01`：消费方使用 LeaderElector 必经 Builder（不可裸构造 Loop）
  - `RECONCILE-FENCED-WRITE-FUNNEL-01`：L4 reconciler 写必经 epoch-bound `FencedWriter`（上游 Hard = 唯一写面 + sealed 构造；下游 Hard = callsite allowlist），绕过 fencing CAS 在 type system 不可表达
  - 估算：160 LoC
- [x] **T27b** [PR-A6] `kernel/reconcile/reconciletest/conformance.go` 扩 fencing：
  - `RunFencingConformance`：real-failure-injection——epoch=N 写在 epoch=N+1 接管后重放，断言 CAS 拒绝 + 无重复命令（对齐 `outboxtest` / `celltest.RunRepoReadinessConformance` 形态）
  - 估算：120 LoC

**Checkpoint A6**: leader 流转可测；2 个 adapter 通过 conformance；**fencing real-failure-injection 通过**（stale-epoch 写被拒、无重复命令）；明确 leader election ≠ fencing

---

## Phase 5: Builder + Conformance（B6）

### PR-A7 — feat: Builder DSL + reconciletest.ConformanceFactory + funnel archtest

**Goal**: Builder 是消费方构造 Loop 的唯一公开入口（Loop 构造函数私有化，Builder 是 funnel 上游 Hard）；conformance harness 让消费方一次跑过所有契约。

**Independent Test**: 消费方代码 `reconcile.New(myReconciler).WithTrigger(...).WithLeader(...).Build()` 编译通过；`var _ = reconcile.NewLoop(...)` 编译失败（私有化）。

#### Tests for PR-A7 (TDD)

- [x] **T28** [P] [PR-A7] `kernel/reconcile/builder_test.go`：
  - `TestBuilder_RequiresReconcilerAndTrigger`：缺 reconciler 或 trigger 返回 err
  - `TestBuilder_DefaultsLeaderAsNoop`：无 LeaderElector 时 single-process 模式（in-process mutex）
  - `TestBuilder_DefaultsConcurrencyTo1`：MaxConcurrentReconciles 默认 1
  - 估算：300 LoC
- [x] **T29** [P] [PR-A7] `kernel/reconcile/reconciletest/conformance_test.go`：
  - 自检：fakeReconciler 跑过所有 conformance test 集
  - 估算：200 LoC

#### Implementation for PR-A7

- [x] **T30** [PR-A7] `kernel/reconcile/builder.go`：
  - `func New(reconciler Reconciler) *Builder`
  - `WithTrigger / WithLeader / WithFencedRepo / WithConcurrency / WithBackoff / WithMetrics / WithInterval / WithName / WithReconcilerID / WithRenewInterval`
  - `Build() (*Loop, error)`（私有化 Loop 构造）
  - 估算：250 LoC
- [x] **T31** [PR-A7] `kernel/reconcile/reconciletest/conformance.go`：
  - `type HarnessFactory func(t *testing.T) Wiring`（`Wiring{NewTrigger, Leader, Fenced, Cleanup}`）+ `type Features struct { Leader, Fencing bool }`
  - `RunConformance(t, newHarness, features)` 跑遍：basic Reconcile / RequeueAfter / PermanentError / panic recovery / MaxConcurrentReconciles（cross-entity + same-entity）/ leader 流转 / fencing
  - 估算：400 LoC
- [x] **T32** [PR-A7] `kernel/reconcile/reconciletest/fake.go`：
  - `FakeLeaderElector` / `FakeTrigger` / `FakeReconciler`
  - 估算：200 LoC
- [x] **T33** [PR-A7] `tools/archtest/reconcile_builder_funnel_test.go`：
  - `RECONCILE-BUILDER-FUNNEL-01`：消费方构造 Loop callsite ⊆ Builder.Build()（funnel 上游 Hard：Loop 构造函数私有化）
  - 估算：100 LoC
  - 落地说明：archtest 合并进 `tools/archtest/reconcile_invariants_test.go`（同主题 invariants 文件），非独立 `reconcile_builder_funnel_test.go`。

**Checkpoint A7** ✅ 已交付（PR #1528，commit `7311ca058`）：Builder funnel 上游 Hard；消费方可跑 conformance

---

## Phase 6: kernel/command + examples 迁移（B7）

### PR-A8 — refactor: kernel/command.Sweeper 实现 reconcile.Reconciler + runtime/command/lifecycle.go 删除

**Goal**: 删除 SweeperLifecycle / SweepTicker 命名，kernel/command.Sweeper 改为实现 reconcile.Reconciler；archtest 守 frozen 命名。

**Independent Test**: `var _ reconcile.Reconciler = (*command.Sweeper)(nil)` 编译通过；`grep -r SweeperLifecycle` 在 production 0 命中。（`RECONCILE-NAMING-FROZEN-01` won't-do — see T38 annotation below）

#### Tests for PR-A8 (TDD)

- [ ] **T34** [P] [PR-A8] `kernel/command/sweeper_reconcile_test.go`：
  - `TestCommandSweeper_ImplementsReconciler`：type assertion 编译期通过
  - `TestCommandSweeper_ReconcileBehaviorPreserved`：现有 SweepOnce 行为不变（StatusExpired 转换）
  - 估算：250 LoC
- [ ] **T35** [P] [PR-A8] `runtime/command/lifecycle_removed_test.go`：
  - `TestSweeperLifecycle_Removed`：archtest 验证 type 不存在
  - 估算：50 LoC

#### Implementation for PR-A8

- [ ] **T36** [PR-A8] `kernel/command/sweeper.go`：
  - 改 `SweepOnce` 接口让其实现 `reconcile.Reconciler.Reconcile`
  - 或新增 `SweeperAsReconciler` adapter wrapper
  - 估算：150 LoC
- [ ] **T37** [PR-A8] **删除** `runtime/command/lifecycle.go`（451 LoC）+ 调用方迁移：
  - `runtime/command/bootstrap_phase.go` 改引用 `reconcile.Builder` 代替 `NewSweeperLifecycle`
  - 估算：300 LoC（删除 + 替换）
- [ ] **T38** [PR-A8] `tools/archtest/reconcile_naming_frozen_test.go`：
  - `RECONCILE-NAMING-FROZEN-01`：grep production 不得出现 SweeperLifecycle / SweepTicker
  - 估算：80 LoC
  - **WON'T-DO (PR-A8 decision)**: a grep-of-deleted-name archtest is Soft per ai-robust.md（"Soft 严禁立项"）. The frozen-naming guarantee is instead type-system Hard — the SweeperLifecycle/SweepTicker TYPES are deleted, so any production reference is a compile error — backed by the existing RECONCILE-BUILDER-FUNNEL-01 (Hard) + a compile-time `var _ reconcile.Reconciler = (*command.Sweeper)(nil)` assertion. A one-time merge-gate grep of production source is empty. No standing Soft archtest is added.
- [ ] **T39** [PR-A8] 更新 `tools/archtest/command_projection_explicit_test.go`：
  - 加 reconcile contract kind 枚举到 `COMMAND-PROJECTION-EXPLICIT-01`
  - 估算：50 LoC（diff）
- [ ] **T40** [PR-A8] 更新 `tools/archtest/clock_invariants_test.go`：
  - 删除 `controlPlaneTicker` / `controlPlaneProbeTimer` 在 runtime/command 的 carve-out（已迁出）
  - kernel/reconcile.Loop 的 carve-out 在 PR-A3 已加（Loop 平移自 SweeperLifecycle，control-plane clock 同步纳入 `PROD-CLOCK-INJECTION-01`；PR-A4 的 TickerTrigger 不用此 carve-out，走注入 clock）
  - 估算：30 LoC（diff）

**Checkpoint A8**: SweeperLifecycle 命名完全删除；kernel/command.Sweeper 成为 reconcile 首个示例消费方

---

### PR-A9 — refactor: examples/iotdevice 切换 + e2e 验证

**Goal**: examples/iotdevice/cells/devicecell 改为 wiring `kernel/reconcile.Loop`；e2e 跑通命令超时重发场景。

**Independent Test**: `go test ./examples/iotdevice/...` 全绿；`devicecell.commandSweeper` 字段类型为 `*reconcile.Loop` 而非 `*kcommand.SweeperLifecycle`。

> **STATUS（#1170 — closed as spec-reconcile, no new code）**：PR-A9 的全部实质已被 **PR-A8（#1169）吸收**。
> `devicecell` 是 `SweeperLifecycle` 唯一生产消费方，删 `runtime/command/lifecycle.go` 的强制编译前置即要求同 PR 切换
> wiring（OSS 标准形态：controller-runtime 把"接入 reconciler"当作 consumer 自身代码内联几行，不抽独立迁移 PR）。
> #1170 原列的 T41/T42 两个 net-new 测试在激进自审 + 开源对标下判定为 **Soft 冗余 / 跨层重复，按 AI-robust 否决**——
> 详见 #1170 的 pm:ship 评论（含 controller-runtime / fx primary-source 论据）。**Independent Test 判据在 develop 已满足**。

#### Tests for PR-A9 (TDD)

- [x] **T41** [PR-A9] ~~`examples/iotdevice/cells/devicecell/cell_reconcile_test.go`~~ — **NOT ADDED（superseded）**：
  - "commandSweeper 字段是 `*reconcile.Loop`" 是**编译期事实**（字段声明 `commandSweeper *reconcile.Loop` @ `cell.go`
    + `*kcommand.SweeperLifecycle` 类型已删 → 任何其它类型是编译错误）；非 nil 已由 `sweeper_lifecycle_test.go`
    （`Init` NoError + `OnStart` NoError）证明。运行期再断言一个编译期事实 = AI-robust Soft，「Soft 严禁立项」。
    OSS 对标：controller-runtime 对 reconciler 满足契约的唯一断言 = 编译期 `var _ Reconciler = Func(nil)`，无运行期字段类型扫描。
- [x] **T42** [PR-A9] ~~`examples/iotdevice/e2e_test.go`~~ — **NOT ADDED（已被覆盖，跨层 duplicate）**：
  - "Enqueue → 超时 → Loop Reconcile → Expired" 已由 **#1169 `command_sweep_behavior_test.go`** 覆盖（cell 级
    real Loop+TickerTrigger→`StatusExpired` + ticker 单源 + `reconcile_total{result=success}`）；"no-deadline 命令不被扫"
    与"未到期不动"已由 `kernel/command/sweeper_test.go::TestSweepOnce_NoTimeoutsConfigured_NoTransitions` /
    `TestSweepOnce_NotYetExpired` / `TestSweepOnce_OverallExpired` 覆盖。OSS 对标：循环机制由无 domain 逻辑的
    `fakeReconciler` 在框架层（`pkg/internal/controller/controller_test.go`）一次测，consumer 不重测"循环驱动我的 Reconcile"。
  - 注：公开 `devicecmd.Enqueue` 写 `command.Timeouts{}`（无 OverallDeadline → 永不到期），任何到期 e2e 须显式 seed deadline。

#### Implementation for PR-A9

- [x] **T43** [PR-A9] `examples/iotdevice/cells/devicecell/cell.go` — **delivered by #1169（PR-A8 吸收 + 强制编译前置）**：
  - `buildCommandSweeper` = `reconcile.New(sweeper).WithTrigger(reconcile.TickerTrigger(c.clk, commandSweepInterval)).WithoutDefaultRequeue().Build()`；
    字段 `commandSweeper *reconcile.Loop`，经 `reg.Lifecycle` 挂 `Loop.Start/Stop`。
- [x] **T44** [PR-A9] `examples/iotdevice/cells/devicecell/sweeper_lifecycle_test.go` — **delivered by #1169（重写，非删除）**：
  - #1169 未按原计划删除，而是重写为 `TestDeviceCell_CommandSweeperLoop_OnStartClean`（OnStart 不 panic + OnStop 幂等 +
    goleak），作为 reconcile 迁移的 cell 级回归守卫——比"删除"更优的结果。
- [x] **T45** [PR-A9] `examples/iotdevice/run.go` — **skipped（YAGNI）**：
  - devicecell 命令扫描器是单副本，无多副本需求；`reconcile.Loop` 默认无 leader 即正确形态，故不加 fake LeaderElector wiring。

**Checkpoint A9**: ✅ 达成——examples/iotdevice 全绿；reconcile 端到端验证由 #1169 cell 级 e2e（`command_sweep_behavior_test.go`）
+ kernel 级 sweeper 逻辑测试（`kernel/command/sweeper_test.go`）共同覆盖；#1170 关闭为 spec reconciliation（无 net-new 代码）。

---

## Phase 7: Polish & Docs（B8）

### PR-A10 — docs: `.claude/rules/gocell/reconcile.md` + ADR amendment + CLAUDE.md 引用

**Goal**: 规则文件落地单源治理文档；现有相关 ADR 加 amendment 反映 reconcile 落地。

**Independent Test**: `.claude/rules/gocell/reconcile.md` 在 CLAUDE.md 章程引用链中可达；现有 ADR amendment 写入威胁矩阵的"满足"标记。

#### Tasks

- [x] **T46** [PR-A10] 新建 `.claude/rules/gocell/reconcile.md` — **delivered**：
  - § 适用范围（cell 治理 / 设备收敛，不做业务编排）
  - § Reconciler 实现要点（PermanentError 何时用 / RequeueAfter 计算 / panic 处理）
  - § Builder 强制约束（消费方禁止裸构造 Loop）
  - § leader-elect adapter 选型（Redis vs PG）
  - § 与 saga / projection 边界
  - § 现有 archtest invariants 引用（5+ 条）
  - 估算：350 LoC（markdown）
- [x] **T47** [PR-A10] 现有 ADR amendment — **delivered across later ADR amendments**：
  - `docs/architecture/202605120000-adr-archtest-process-isolation.md` 加 §Amendment：reconcile 新 archtest 加入 shard matrix
  - `docs/architecture/202605170000-*` 加 §Amendment：reconcile.Loop 共用 control-plane ticker carve-out
  - 估算：100 LoC（diff）
- [x] **T48** [PR-A10] CLAUDE.md 章程引用更新 — **superseded by instruction-surface boundary**：
  - 第 "AI-robust 治理章程" 段补充 reconcile 规则链接
  - 估算：30 LoC（diff）
- [x] **T49** [PR-A10] `docs/references/framework-comparison.md` 加 reconcile 对标行 — **delivered**：
  - 表加一行：`L4 控制环 | controller-runtime Reconciler`
  - 估算：20 LoC（diff）

**Checkpoint A10**: ✅ 文档闭环；历史激活 checklist 已完成；后续状态漂移由 ADR amendment 维护

---

## Dependencies & Execution Order

### PR-level Dependencies

```text
A1 (ADR) ──→ A2 (interface) ──→ A3 (Loop) ──┬→ A4 (Trigger) ──┐
                                              │                  │
                                              ├→ A5 (Backoff) ──┘
                                              │                  │
                                              └──────────────────┴→ A6 (Leader)
                                                                       │
                                                                       └→ A7 (Builder/Conformance)
                                                                              │
                                                                              ├→ A8 (command 迁移)
                                                                              │       │
                                                                              │       └→ A9 (examples 迁移)
                                                                              │
                                                                              └────→ A10 (docs)
```

- **B1**: A1 单独（ADR 必须先 merge）
- **B2**: A2（接口 frozen）
- **B3**: A3（Loop 调度骨架）
- **B4**: A4 ∥ A5（Trigger 与 Backoff 不交叠）
- **B5**: A6（LeaderElector，依赖 A3 Loop）
- **B6**: A7（Builder 是 A4+A5+A6 的 funnel）
- **B7**: A8 → A9（A9 依赖 A8 已 merge 的 Reconciler 实现，串行）
- **B8**: A10（最后文档收尾）

### Task-level Parallel Opportunities

- **Within PR**: tests + impl 严格 TDD 顺序（先 [P] 测试 fail，再 impl 通过）
- **Across [P] tests in same PR**: 不同文件可并行写
- **Across PRs in same batch (B4)**: A4 / A5 各自 developer agent 并行实施（不改同一文件）

---

## Parallel Example: Batch B4 (PR-A4 + PR-A5)

```text
worktrees/661-04-trigger     ← developer A: PR-A4 任务集（T13-T16）
worktrees/661-05-backoff     ← developer B: PR-A5 任务集（T17-T20）
```

两个 worktree 基于 A3 merge 后的 develop；A4 与 A5 同 PR 各 ~1000 LoC，单 reviewer per PR。

---

## Implementation Strategy

### MVP First (PR-A1 → PR-A7)

1. 完成 Phase 1-5（B1-B6）= PR-A1 → PR-A7
2. **STOP and VALIDATE**: kernel/reconcile 公开 API 与 conformance harness 就绪；可被新消费方（runtime/certlifecycle / mdmcell.command 等）开始 wiring
3. 此时 kernel/command 仍走 SweeperLifecycle 路径（旧路径），不破坏现有

### Incremental Migration (PR-A8 + PR-A9)

4. PR-A8 (#1169): kernel/command.Sweeper 迁为 reconcile.Reconciler；删除 SweeperLifecycle；**并吸收 PR-A9 的 devicecell 切换**（删除唯一消费方的强制编译前置）
5. PR-A9 (#1170): examples/iotdevice 切换到 reconcile.Loop 端到端 —— **实质已随 #1169 落地；#1170 关闭为 spec reconciliation（见上 PR-A9 §STATUS）**
6. 此时只有 reconcile 一个 control loop 抽象，无双轨

### Documentation Closure (PR-A10)

7. PR-A10: 规则文件 + ADR amendment + CLAUDE.md 引用收口
8. **关闭 issue #661**

### Parallel Team Strategy

- **B1 / B2 / B3**: 单 developer 串行（ADR → 接口 → Loop）
- **B4**: 2 developer 并行（A4 Trigger / A5 Backoff）
- **B5 / B6**: 单 developer
- **B7**: A8 + A9 视复杂度可串行（推荐）或并行（A9 依赖 A8 接口稳定）
- **B8**: 单 developer

---

## Notes

- [P] = 同 PR 内不同文件可并行写测试
- 所有 TDD 测试必须先 fail 再 impl
- 每 PR commit message 必须含 `Refs: #661` + `ref: <framework> <file>`
- 每 PR review 走 ship skill 阶段 7 自动 reviewer 数（按 diff 行数 1/2/3/6）
- 每 PR merge 前跑 `make verify`（含 archtest 全集）
- 不留 follow-up：单 PR 内 Cx1/Cx2 finding 同 PR 修；Cx3+ 走人工确认（不延期到下 PR）
- 不向后兼容：SweeperLifecycle / SweepTicker 命名完全删除（archtest 守 frozen），不留 deprecation 别名

---

## Tracking Matrix（trigger 满足时填）

| PR | Branch | Worktree | Owner | Start | Merge | Closes |
|----|--------|----------|-------|-------|-------|--------|
| A1 | `661-adr-reconcile-design` | `worktrees/661-01-adr` | — | — | — | — |
| A2 | `661-reconciler-interface` | `worktrees/661-02-interface` | — | — | — | — |
| A3 | `661-loop-skeleton` | `worktrees/661-03-loop` | — | — | — | — |
| A4 | `661-trigger` | `worktrees/661-04-trigger` | — | — | — | — |
| A5 | `661-backoff` | `worktrees/661-05-backoff` | — | — | — | — |
| A6 | `661-leader-elector` | `worktrees/661-06-leader` | — | — | — | — |
| A7 | `661-builder-conformance` | `worktrees/661-07-builder` | — | — | — | — |
| A8 | `661-command-migration` | `worktrees/661-08-command` | — | — | — | — |
| A9 | `661-examples-migration` | `worktrees/661-09-examples` | — | — | — | — |
| A10 | `661-docs-closure` | `worktrees/661-10-docs` | — | — | — | #661 |
