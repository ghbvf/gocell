# ADR 661 — kernel/reconcile L4 desired-state 收敛控制环设计

| 字段 | 值 |
|------|---|
| ADR ID | 661 |
| 状态 | **Accepted（设计冻结）。PR-A1–A7 已合入 develop；A8–A10 在 §6.1 trigger gate 内（kernel 基建 A1–A8 不门 trigger，详见 §6.2 Amendment 2026-06-02）** |
| 日期 | 2026-05-29 |
| Issue | [#661](https://github.com/ghbvf/gocell/issues/661)（父）/ [#1162](https://github.com/ghbvf/gocell/issues/1162)（PR-A1） |
| Spec | `docs/plans/specs/202605262359-661-kernel-reconcile-{spec,plan,tasks}.md` |
| 一致性级别 | **L4 DeviceLatent**（issue 第一性原理重评结论） |

> 本 ADR 是 `kernel/reconcile` 的设计权威源。本 PR（PR-A1）是 **docs-only**——`kernel/reconcile`
> 的实现代码**不在本 PR、也尚未合入 develop**，而活在同 stack 的原型分支 `661-loop-skeleton`
> （PR-A2 3 件套最小核 + PR-A3 Loop 调度骨架）。该原型经 `go test -race ./kernel/reconcile/`
> 通过、coverage 90.8%，为 `§2 ≥80% reuse`、`§3 接口形态`、`§7 panic 隔离` 等论断提供**原型实现
> 背书**——但这是「**分支原型验证**」而非「trunk 已验证」：A2/A3 作为各自独立 PR（经各自 review）
> 实际合入 develop 前，develop 上没有任何 `kernel/reconcile` 代码，本文论断在 trunk 维度仍是
> 「设计 + 待兑现」。引用具体数值（coverage / LoC）时须带此 provenance，勿表述为 trunk 现状。
> A1–A3 是 **stacked PR，按 A1（base develop）← A2 ← A3 依次合入**（plan.md「每 PR merge
> 后 trunk 可发布」）；本文用「由 PR-Ax 交付」标注每条论断的承载 PR——读者在 develop 上看到的
> 实现取决于已合入到哪一 PR，A2/A3 交付的形态/数值（接口、Loop、metrics、archtest 等）只在其
> 承载 PR 实际合入 develop 后才成为 trunk 事实。设计与实现分歧时以本 ADR 为准，并在同 PR 内
> 修正实现或修订本 ADR。

---

## §0 决策摘要

**3 件套最小核**（由 PR-A2 交付，archtest frozen）：

| 件 | 形态 | 对标 controller-runtime |
|----|------|------------------------|
| `Reconciler` | `interface { Reconcile(ctx, Request) (Result, error) }` | `reconcile.Reconciler`（采纳） |
| `Result.RequeueAfter` | `struct { RequeueAfter time.Duration }` | `reconcile.Result`（**削到单字段**） |
| `PermanentError` / `IsPermanent` | sealed marker（不重试，进死信 metric） | `reconcile.TerminalError`（采纳语义，独立实现） |

**删除的 K8s 控制平面专属抽象**（GoCell 无对应需求，详见 §2）：

- `Informer` / CRD watch / `Source.Informer`（GoCell 不依赖 K8s 控制平面，无 watch 源）
- `Predicate`（事件过滤；GoCell level-triggered 全量扫描）
- `Manager`（K8s 多控制器编排；GoCell 由 cell registrar lifecycle 托管）
- `Builder.For` / `Builder.Owns` / `Builder.Watches`（绑定 K8s informer 源）
- `Result.Requeue bool`（用 `RequeueAfter==0` 表达，冗余字段删除）
- `Result.Priority *int`（K8s 调度器优先队列特性；GoCell 无需求）

简化倍率（SC-001）：`kernel/reconcile` 公开类型表面 `Reconciler`+`Request`+`Result`+
`PermanentError`/`IsPermanent` ≈ 30 LoC（`reconciler.go` 45 + `result.go` 类型部分）；
controller-runtime `pkg/reconcile`+`pkg/builder` 公开面 ≥800 LoC，简化 ≥4x。

**交付划分**：本 stack 分三 PR——PR-A1（本 ADR，docs-only，= 当前 PR）/ PR-A2（接口 + 3 frozen
archtest）/ PR-A3（Loop 调度骨架 + 4 metrics + clock carve-out）。A2/A3 已在原型分支
`661-loop-skeleton` 经 `go test -race` + 90.8% coverage 验证，但**尚未作为 PR 合入 develop**；
合入顺序 A1←A2←A3。下文「由 PR-Ax 交付」标注每条论断的承载 PR——某论断背书的代码只有在其
承载 PR 实际合入后才存在于 develop。PR-A4–A10（Trigger / Backoff / LeaderElector / Builder /
迁移 / 文档）受 §6 trigger gate 封存。

---

## §1 问题陈述

### 1.1 kernel/command Sweeper 域绑定

现状 L4 控制环唯一实例是 `kernel/command.Sweeper` + `runtime/command.SweeperLifecycle`：

- `kernel/command.Sweeper.SweepTick(ctx, now)` 是**命令实体专属**的批量扫描器——内部
  `scanner.ScanActive` → `SweepOnce`（计算过期）→ `queue.Ack(AckTimeout)`，签名与命令
  domain 强耦合（`ActiveScanner` / `Queue` / `ExpiryTransition`）。
- `runtime/command.SweeperLifecycle`（446 LoC）是通用的**调度骨架**：control-plane ticker /
  startup probe / owner-ctx 派生 / graceful stop / 错误计数——这部分与命令域无关。

骨架可复用，但被命令域签名（`SweepTick`/`SweepTicker`/`SweepErrorCounter`）绑死，无法被
其他 L4 消费方直接复用。

### 1.2 四个真消费方需要泛化

issue #661 第一性原理重评列出 ≥4 个 roadmap-committed 的 L4 desired-state 消费方
（详见 §6 trigger 表）：`pkicell.rotation`（证书续期）、`mdmcell.command`（命令超时重发）、
`devicelifecycle.cronsweep`（设备墓碑状态机）、`zerotrust.trustscore`（信任分周期重评）。
四者都需要「周期观察非终态实体 → 逐个驱动至期望态 → 按结果重排」的相同控制环，仅
Reconcile 逻辑不同。把 Sweeper 骨架泛化为 `kernel/reconcile.Loop` + `Reconciler` 接口，
四者只写 Reconcile 本体。

### 1.3 权限模型澄清（防与 ADR-041 §3.3 矛盾）

ADR `202605041430-adr-architecture-optimization-via-engineering-thinking.md` §3.3 论证：
**GoCell 框架自身的自收敛必须落在编译期/CI 期**，因为「运行时框架是被嵌入宿主应用的库，
对宿主应用无修改权限」。本 ADR 不与该结论矛盾——它管辖的是**另一个收敛域**：

| 收敛域 | 主体 | 时间维度 | 权限来源 | 归属 |
|--------|------|---------|---------|------|
| **框架自收敛** | GoCell 对**自己的仓库元数据** | 编译期 / CI 期 | 对自己仓库有 PR/commit 权限 | ADR-041 §3.3（**不在本 ADR**） |
| **宿主 cell 业务 L4 收敛** | 宿主 cell 对**自己拥有的持久实体**（device commands / cert rows / trust scores） | 运行时 | cell 对自己的 PG/Redis 表有读写权限 | **本 ADR** |

关键区分：`kernel/reconcile` 不让框架去改宿主应用——它只提供 `Loop` harness；**收敛权限
属于消费 cell**（它对自己的实体表行使写权限，在 `Reconcile` 内）。框架不代行宿主权限，
故 ADR-041 §3.3「框架无运行时权限」约束不适用于「cell 用框架提供的环来收敛自己的实体」。

---

## §2 对标 controller-runtime（快照 @ pinned commit，2026-05-29）

对标快照 **pin 到具体 commit SHA**（不用移动的 `main`/`master` ref，保证可复现）；后续 PR
commit message 引用对应 `ref:` 行。下方所有 `ref:` 行均锚定于这两个 commit：

| 上游 repo | 默认分支 | pinned commit（2026-05-29 抓取，HEAD-of-default-branch） |
|-----------|---------|------------------------------------------------------|
| `kubernetes-sigs/controller-runtime` | `main` | `346f1930fde577d7d5e49c88e5ee625e4ebb7daa`（短 `346f1930fde5`，committed 2026-05-26） |
| `kubernetes/client-go` | **`master`**（非 `main`） | `5d252d37f7301d279fc55030c9f8e0e1688a6985`（短 `5d252d37f730`，committed 2026-05-28） |

> 上述 5 个引用路径已在对应 pinned SHA 处核验存在。§6.3「激活前核验对标快照仍有效」=
> 比对 pinned SHA 处源码与本 §2 摘录；上游演进时先 re-pin（更新本表 SHA + 下方 raw URL）
> 再修订摘录。注意 client-go 默认分支是 `master` 不是 `main`——§2.4 / §2.5 的 raw URL 须用
> 上表 SHA（commit-pinned，分支无关），勿写 `/main/`。

### 2.1 `pkg/reconcile/reconcile.go` — Reconciler / Request / Result

ref: `kubernetes-sigs/controller-runtime pkg/reconcile/reconcile.go`
（https://raw.githubusercontent.com/kubernetes-sigs/controller-runtime/346f1930fde577d7d5e49c88e5ee625e4ebb7daa/pkg/reconcile/reconcile.go）

```go
type TypedReconciler[request comparable] interface {
	Reconcile(context.Context, request) (Result, error)
}
type Request struct { types.NamespacedName }
type Result struct {
	Requeue      bool          // deprecated
	RequeueAfter time.Duration // if >0, requeue the key after Duration
	Priority     *int          // priority on re-enqueue
}
func TerminalError(wrapped error) error // prevents retry, still logs + metrics
```

**GoCell 适配**：

| 上游 | GoCell | 决策 |
|------|--------|------|
| `Reconcile(ctx, request) (Result, error)` | 同形态，`request` 固定为 `Request` | **采纳** |
| `Request{types.NamespacedName}` | `Request{EntityID string}` | **削**——cell-local 实体表无 namespace 维度，单 opaque ID 足够 |
| `Result.RequeueAfter` | `Result.RequeueAfter` | **采纳**（核心调度提示） |
| `Result.Requeue bool` | 删 | **删**——`RequeueAfter==0` 即「按 default tick 重入」，bool 冗余 |
| `Result.Priority *int` | 删 | **删**——优先队列是 K8s 调度器特性，无需求 |
| `TerminalError` | `PermanentError`/`IsPermanent` | **采纳语义、独立实现**——见 §3.3 |

> GoCell 语义偏离一处：上游 `Result{}`（无 requeue）= 「done，等 watch/resync 再触发」；
> GoCell `Result{}`（`RequeueAfter==0`）= 「按 default tick interval 重入」。因为 GoCell 是
> level-triggered 周期扫描模型（承自 Sweeper），无 watch 源，需周期性重观察。

### 2.2 `pkg/internal/controller/controller.go` — worker loop

ref: `kubernetes-sigs/controller-runtime pkg/internal/controller/controller.go`

```go
wg.Add(c.MaxConcurrentReconciles)
for i := 0; i < c.MaxConcurrentReconciles; i++ {
	go func() { defer wg.Done(); for c.processNextWorkItem(ctx) {} }()
}
```

- `MaxConcurrentReconciles` worker goroutines 从 workqueue 取 item；
- 结果处理：`RequeueAfter`→`Queue.AddWithOpts{After}`；非 terminal error→`AddWithOpts{RateLimited}`；
  success→`Queue.Forget`；terminal error→记 metric 不重排；
- `RecoverPanic`（默认开）：`Reconcile` 内 panic 被 recover → 转 `fmt.Errorf("panic: %v")`。

**GoCell 适配（由 PR-A3 交付，PR-A5 #1166 完善）**：`Loop` 起 `MaxConcurrentReconciles`
个 worker（默认 1）从内部 queue 取 `Request`；`process()` 做同 ID 串行 + panic recovery
（`recoverReconcile`，单实体 panic 不杀 worker，转 transient）+ 按 `Result`/error 重排。

**§Amendment 2026-06-01 (PR-A5 #1166)**：原 §2.2 描述"未采纳 dirty/processing 去重 + rate-limited
delaying queue"已被 PR-A5 落地实现取代，以下为当前真值（旧描述不保留，避免两套真理源）：

- **F5 dirty/processing dedup（PR-A5 已落地）**：替换 A3 的「skip-if-busy（丢重复）+ sync.Map
  inflight」方案。`Loop` 在 `entityMu` 下维护 `processing` + `dirty` 两张 map：一个 trigger
  在 in-flight 期间到达时，写入 `dirty[entityID] = req`（latest wins，coalesced）而非丢弃；
  in-flight 完成后检查 dirty——若有则以 delay=0 立即 re-enqueue（一次 re-run，不论中间积压多少
  duplicate）。集合内的 set/clear 全在 entityMu 下原子完成，消除 lost-wakeup 窗口。
  skipped metric 仍记录（对进行中实体的 coalesced trigger）；re-run 是新的 full reconcile，
  不带 backoff（dirty re-run 是收敛，非失败重试）。

- **F6 共享 rate-limited delaying queue（PR-A5 已落地）**：替换 A3 的「per-requeue goroutine」
  方案。ONE `waitingLoop` goroutine 使用 `container/heap`（`waitingHeap`，readyAt min-heap，
  对标 client-go `delaying_queue.go`）+ ONE reusable timer（`controlPlaneClock{}.newRequeueTimer`，
  sealed real-clock，对齐 T-CLOCK carve-out）。所有重排路径（success/transient/dirty-re-run）
  统一经 `enqueueDelayed` → `addCh` channel → waitingLoop heap，不再创建 per-entity goroutine。
  goroutine 由 runCtx 取消退出（WaitGroup 跟踪），goleak-clean（`TestLoop_SharedWaitingLoopNoLeak`
  守）。

- **per-entity 指数退避（PR-A5 已落地）**：`entityBackoff`（`backoff.go`）对标 client-go
  `ItemExponentialFailureRateLimiter`：`base·2^n`（n = 连续失败次数），base=5ms，max=1000s，
  无 jitter。transient error 调 `backoff.When(entityID)` 递增 n；success 调 `backoff.Forget`
  归零；dirty re-run 不经 backoff（delay=0）。

### 2.3 `pkg/builder/controller.go` — Builder DSL

ref: `kubernetes-sigs/controller-runtime pkg/builder/controller.go`

```go
func (b *TypedBuilder[request]) For(object client.Object, opts ...ForOption) *TypedBuilder[request]
func (b *TypedBuilder[request]) Owns(object client.Object, opts ...OwnsOption) *TypedBuilder[request]
func (b *TypedBuilder[request]) Watches(object client.Object, h ..., opts ...) *TypedBuilder[request]
func (b *TypedBuilder[request]) Complete(r reconcile.TypedReconciler[request]) error
```

`For`/`Owns`/`Watches` 绑定 K8s informer 源——**全删**（GoCell 无 informer）。GoCell Builder
（PR-A7）= `New(reconciler).WithTrigger(...).WithLeader(...).WithConcurrency(...).Build()`，
唯一公开构造入口（`Loop` 构造私有化，funnel 上游 Hard），不绑定任何 K8s 资源。

### 2.4 `client-go util/workqueue/default_rate_limiters.go` — 退避默认值

ref: `kubernetes/client-go util/workqueue/default_rate_limiters.go`

```go
NewTypedItemExponentialFailureRateLimiter[T](5*time.Millisecond, 1000*time.Second)
```

GoCell `Backoff`（PR-A5）采纳 `baseDelay=5ms / maxDelay=1000s`（FR-008）。

### 2.5 `client-go tools/leaderelection/leaderelection.go` — lease 模型

ref: `kubernetes/client-go tools/leaderelection/leaderelection.go`

`LeaseDuration`（默认 15s，非 leader 强制接管前的观察窗）/ `RenewDeadline`（10s，leader 放弃前
续约重试窗）/ `RetryPeriod`（2s，轮询间隔）；`LeaderElectionRecord{HolderIdentity,
LeaseDurationSeconds, AcquireTime, RenewTime}`。GoCell `LeaderElector`（PR-A6）采纳 lease/renew
模型，见 §4。

### 2.6 ≥80% reuse 论证（SC-002，经实现验证）

`runtime/command/lifecycle.go` 的 import 全部是 stdlib + `kernel/{cell,clock,observability/metrics}`
+ `pkg/{redaction,validation}`，**零 runtime-only 依赖**（已核验），可平移进 `kernel/reconcile`
不违反分层（kernel/ 只依赖 stdlib+pkg+kernel）。

实测（PR-A3，`loop.go` 392 LoC vs `lifecycle.go` 446 LoC）：

| 部分 | 来源 | 形态 |
|------|------|------|
| **调度骨架**（`controlPlaneClock` seal / `Start` fast-return / `awaitProbe` / `Stop` graceful / owner-ctx 派生 / metrics preflight / helpers） | `SweeperLifecycle` 1:1 平移（仅改名 `Sweep*`→`Reconcile*`、删 `BusinessClock`） | ~270 LoC |
| **per-entity 派发**（`pump` / `runWorker` / `process` / `safeReconcile` / `scheduleRequeue` + inflight 串行 + concurrency 信号量） | 新写（Sweeper 是单次批量 `SweepTick`，无 per-entity 派发层） | ~120 LoC |

**口径**：SC-002「≥80%」指**调度骨架层**——它高保真平移（仅改名 + 删 `BusinessClock`，因
`Reconcile(ctx, Request)` 不收 `now`，reconciler 自带时钟）。整包 `loop.go` reuse ≈ 270/392 ≈
**69%**，低于 80% 的差额来自 Sweeper 没有的「per-entity worker 派发层」——这是把「单次批量
扫描」泛化为「per-entity reconcile」的本质新增，不是平移损耗。**这是实现对 spec 假设的修正**：
spec SC-002 原文「451 中迁移 ≥360」按「整体平移」估算，实测揭示需新增 per-entity 层，故口径
收敛到「调度骨架层 ≥80% 平移 + per-entity 层新写」。

---

## §3 接口设计

### 3.1 Reconciler / Request / Result（由 PR-A2 交付，frozen）

```go
// INVARIANT: RECONCILE-INTERFACE-FROZEN-01
type Reconciler interface { Reconcile(ctx context.Context, req Request) (Result, error) }
// INVARIANT: RECONCILE-REQUEST-FIELDS-FROZEN-01
type Request struct { EntityID string }
// INVARIANT: RECONCILE-RESULT-FIELDS-FROZEN-01
type Result struct { RequeueAfter time.Duration }
```

字段集由 reflect golden archtest 冻结（`tools/archtest/reconcile_invariants_test.go`），
`RECONCILE-RESULT-FIELDS-FROZEN-01` 显式拒绝 `Requeue bool` / `Priority int` 残留 + 含反向
盲区自检。`RequeueAfter` 语义：`>0` N 后重入；`==0` 按 default tick 重入；error≠nil 时忽略
（走退避）；负值 clamp 到 0（`normalizedRequeueAfter`，PR-A2 测试）。

### 3.2 Trigger（PR-A4 落地）

```go
type Trigger interface { Start(ctx context.Context, queue chan<- Request) error }
func TickerTrigger(clk clock.Clock, interval time.Duration) Trigger // 周期全量重观察脉冲（替代 controller-runtime resync Source）
func ChannelTrigger(in <-chan Request) Trigger                      // outbox 事件唤醒
```

替代 controller-runtime `Source`，最小 2 实现。`Trigger.Start` 非阻塞（spawn producer 即返回）+
block-don't-drop（queue 满阻塞不丢，level-triggered 安全）——对标 `source.Source.Start` 形态，但
sink 削为裸 `chan<- Request`（去 client-go workqueue：去重/退避归 Loop，不归 Trigger）。
PR-A3 的 `Loop.Source <-chan Request` 是 Trigger 产出的原始 channel 接缝（A3 测试直接注入 channel，
A7 Builder 把 Trigger 输出 channel 接进 Loop.Source）。

> **Trigger 受 leader gate（PR-A7 review C1/F2 决议）**：leader-elect 模式下 `Trigger.Start`
> 在 **per-lease-term**（`runLeaseTerm` 的 leaseCtx）启动，**不**在 `Loop.Start` 无条件启动——
> 否则 follower 会在赢得 lease 前就消费外部源（ChannelTrigger 抢/缓他人事件）。single-process
> 模式（always-leader）仍在 `Loop.Start` 启动一次。对标 controller-runtime：source 默认不在赢得
> leader election 前启动（无 warmup；GoCell 当前不引入 `EnableWarmup` 类 opt-in，YAGNI）。跨 term
> 复用同一 `triggerCh`（同时仅一个 term 活跃，垂死 term 的 producer goroutine 与下一 term 的短暂
> 重叠是良性——channel send 并发安全，残留 buffered Request 是幂等 re-reconcile）。

`TickerTrigger` 发**零值 `Request{}` resync 脉冲**（无 entity 上下文的间隔 ticker 只能发空 ID
脉冲，消费方 `Reconcile` 从中扇出）；其节拍走**注入的 `clock.Clock`**（`clk.NewTicker(interval)`，
fake clock `Advance` 可确定性测试，不依赖 wall-clock），构造期 `clock.MustHaveClock` +
`clock.MustHavePositiveInterval` 守——clock 是强制位置参（`CLOCK-POSITIONAL-INJECTION-01`，禁
`WithClock` option / 禁 Config.Clock 字段），不是 control-plane sealed clock。

> **时钟归属澄清（F4 amendment 决议，AI-robust §ADR amendment 落地必查 → 见 §7 T-CLOCK 重评）**：
> `controlPlaneClock` carve-out（§7 T-CLOCK）只覆盖 **Loop 自身**的 probe / requeue 定时器与 duration
> 测量（real wall-clock 必需，否则 Start 死锁）；as-built 的 Loop **没有 ticker**（它是 Source +
> requeue 驱动）。周期节拍由 `TickerTrigger` 持有，走注入 clock，**不**进 carve-out。原 spec 草图
> `TickerTrigger(interval)`（无 clock）与 TDD「注入业务时钟、不依赖 wall-clock」+
> `CLOCK-POSITIONAL-INJECTION-01` 冲突，A4 落地裁决为注入 clock 位置参——本节即重写后真值源，不留
> 旧签名作历史。`RECONCILE-TRIGGER-INTERFACE-FROZEN-01`（reflect 锁 `Start(ctx, chan<- Request)
> error`，含 send-only chan 方向）冻结接口形态。

### 3.3 PermanentError 来源裁决（由 PR-A2 交付）

```go
type permanentError struct{ err error }       // unexported sealed marker
func PermanentError(err error) error          // 唯一构造（nil→nil）
func IsPermanent(err error) bool              // 唯一分类（errors.As，穿透 %w）
```

**裁决：新建 reconcile-local sealed marker，不复用 `kernel/outbox.PermanentError`。**

- 现状：无 `errcode.IsPermanent`；唯一既有 marker 是 `kernel/outbox.PermanentError`（exported
  struct，broker disposition 语义）。
- Rejected alternative：import `kernel/outbox.PermanentError`——会让 `kernel/reconcile` 跨域
  耦合 outbox 的 broker 语义（reconcile 与 outbox 是正交关注点），违反「优雅简洁/不引入
  无关依赖」。
- 采用：unexported `permanentError`（sealed construction 范本）——包外只能经 `PermanentError()`
  构造、`IsPermanent()` 分类，「未经 `PermanentError` 的 permanent error」在包外不可表达。
  语义等价上游 `reconcile.TerminalError`。

### 3.4 LeaderElector（PR-A6 设计）

```go
type LeaderElector interface {
	AcquireLease(ctx context.Context, reconcilerID string) (LeaseToken, error)
	ReleaseLease(ctx context.Context, token LeaseToken) error
	RenewLease(ctx context.Context, token LeaseToken) error
}
type LeaseToken struct {
	ReconcilerID string
	HolderID     string
	Epoch        uint64    // 单调 fencing token（每次换持有者 +1）——见 §4.3
	AcquiredAt   time.Time
	ExpiresAt    time.Time
}
```

接口在 kernel 声明、实现在 adapter（对齐 `outbox.Emitter` / `persistence.CellTxManager`
kernel-driven adapter-implements 模式，满足分层）。`LeaseToken` 字段对标 §2.5
`LeaderElectionRecord`，但用中立形态（不绑 K8s `coordination.k8s.io/Lease`），扩 etcd 等
adapter 不改 kernel。

> `LeaseToken` 是 **PR-A6 设计接口**（未 frozen，与 §3.1 三件套 frozen core 不同），A6 落地时
> 随 `LeaderElector` 一并 frozen。`Epoch` 是 F5 review 引入的**单调 fencing token**——leader
> election 本身**不是 fencing 保证**（见 §4），正确性闭环靠 `Epoch` + §4.3 `FencedRepository`
> 写路径 CAS。注意它与 `kernel/outbox` 的 UUID `lease_id`（identity-fencing）语义不同（§4.3）。

### 3.5 Builder（PR-A7 **已交付**）

```go
func New(reconciler Reconciler) *Builder
func (*Builder) WithTrigger(Trigger) *Builder
func (*Builder) WithLeader(LeaderElector) *Builder
func (*Builder) WithFencedRepo(FencedRepository) *Builder
func (*Builder) WithConcurrency(int) *Builder
func (*Builder) WithBackoff(base, max time.Duration) *Builder
func (*Builder) WithMetrics(Metrics) *Builder
func (*Builder) WithInterval(time.Duration) *Builder
func (*Builder) WithName(string) *Builder
func (*Builder) WithReconcilerID(string) *Builder
func (*Builder) WithRenewInterval(time.Duration) *Builder
func (*Builder) Build() (*Loop, error)   // Loop 构造私有化：Builder 是唯一公开入口
```

上述 With* 集合覆盖每个**消费方可配置**字段。两个字段刻意 framework-owned、**无** With*
入口：(1) `logger` 固定走 `slog.Default()`（进程级 sealed 脱敏 sink，per-loop raw logger 会
绕过 fail-closed redaction）；(2) 控制面 clock 是 sealed real-only（`controlPlaneClock`）。
注：早期草图的 `WithStartTimeout`/`WithStopTimeout` 是 **no-op**（Loop 启动探针走常量
`startProbeTimeout`、Stop 走调用方 ctx，二字段从不被读），PR-A7 review 已**删除**——不暴露
不生效的配置。`Build()` 缺 reconciler/trigger → err；`WithFencedRepo` 不带 `WithLeader` → err
（fencing 需 leadership epoch 源，否则静默 Epoch 0 = 无 fencing）；其余运行时 fail-fast 保留在
`Loop.Start.preStartValidate`。

funnel（**已闭环**）：`Loop` 所有配置字段私有化（PR-A7），包外 `reconcile.Loop{field: v}`
是编译错误。`RECONCILE-BUILDER-FUNNEL-01` 锁「消费方构造 Loop 必经 Builder」（funnel
上游 Hard = 字段私有化，type system 封闭；下游 Hard = AST+Unalias 禁零值字面量
`reconcile.Loop{}` 出现在 `kernel/reconcile` 包外）。临时过渡 archtest
`RECONCILE-LOOP-CONSTRUCTION-ALLOWLIST-01` 已退役。

**默认值策略**：lazy 默认 getter（`interval()`/`name()`/`reconcilerID()`/`logger()`/
`maxConcurrent()`）已删除，默认值收口到 `preStartValidate()` 末尾（metrics preflight 之前）
的单一 `applyDefaults()`（eager defaulting）——`start()` 看到所有字段均为最终值；对标
controller-runtime builder `doController()` eager 默认，消除双路径。

### 3.6 Metrics（由 PR-A3 交付）

`RegisterMetrics(p Provider) (Metrics, error)` 单源注册 4 件：`reconcile_total{reconciler,result}`
（result ∈ success/transient/permanent/skipped）/ `reconcile_duration_seconds{reconciler}` /
`reconcile_in_flight{reconciler}` / `reconcile_leader{reconciler}`。注入式 pre-bound vec +
`preflight`（recover 包裹 label 校验，misconfig → fail-fast Start，对齐
`runtime/command.preflightSweepErrorCounter`）。

> result 标签集冻结为 {success/transient/permanent/skipped}（FR-010）；recovered panic 归为
> transient（可重试），与 FR-009「panic → transient error」一致。单一真值源：spec FR-009/
> FR-010、tasks.md T18（`TestRecovery_PanicMetricRecorded` 断言 `result="transient"`）、
> 本 §3.6 三处一致——**不存在** `result="panic"` 第 5 标签。若未来 A5 确需独立 panic
> disposition，必须由 A5 同 PR 同步修订 FR-010 + tasks.md T18 + 本 §3.6 三处，不得单点漂移。
>
> **`skipped` 语义（PR-A5 修订）**：`skipped` 不是 trigger 丢弃，而是 trigger 到达时实体
> 正在处理中（F5 dirty/processing dedup）——trigger 被 coalesced 进 dirty map，当前飞行 reconcile
> 完成后 **立即（delay=0）触发一次 re-run**（dirty re-run）。A3 阶段 skipped = 丢弃（skip-if-busy）；
> A5 阶段 skipped = coalesced 待 re-run；收敛性更强，没有 trigger 被静默丢弃。

---

## §4 leader-elect 设计

多副本部署时，`Loop` 用 leader election **降低但不消除**跨副本并发 Reconcile——leader election
**不是 fencing 机制**。client-go `tools/leaderelection` 自身文档明示：*"This implementation does
not guarantee that only one client is acting as a leader (a.k.a. fencing)."* STW GC 暂停、时钟
偏移、renew/acquire 竞争都会留下**残余双执行窗口**：旧 leader L1 在 `Reconcile(X)` 中途被暂停
→ lease 过期 → L2 接管并 `Reconcile(X)` → L1 苏醒后写完 → mdmcell 对 X 发两次命令。

因此单实例正确性**不能**靠 lease 本身，必须靠 **monotonic fencing token + 写路径 CAS**（§4.3）
+ **消费方幂等**（§4.4）兜底。本节 §4.1–§4.2 是 lease 机制（best-effort 收窄窗口），§4.3–§4.4
是正确性闭环（结构性兜底）。**以下 §4 全节是 PR-A6 设计**（未落地，受 §6 trigger gate 封存）。

### 4.1 两 adapter

| adapter | 机制 | 续约 | 失败语义 |
|---------|------|------|---------|
| `adapters/redis` | `SET key holderID NX PX leaseMs`（SETNX + TTL）+ INCR epoch key（{rid} hashtag colocate） | 周期 `PEXPIRE` holder + `EXPIRE` epoch（< LeaseDuration） | TTL 到期自动释放，follower SETNX 接管 |
| `adapters/postgres` | **row-TTL CAS**：`INSERT … ON CONFLICT … WHERE expires_at<now() OR holder=self`（**无 advisory lock**） | `UPDATE … WHERE holder=self AND expires_at>now()` | `expires_at` 到期 follower 接管（与 redis/fake 一致） |

复用 `adapters/redis` Cache 的 cell-namespaced key 约定（lease key 带 namespace 前缀）。

> **§4.1 Amendment 2026-06-02 round-2（PR-A6 深审 C2）**：原 PG 行用 **session-scoped
> `pg_try_advisory_lock`** 作 lease 权威——**这是错的**：session advisory lock 持有到 session 显式
> 结束，**不随 `expires_at` 过期**，故 leader 进程**挂起但 TCP 不断**（长 GC / 网络分区连接未掉）时
> follower **无法**在 LeaseDuration+1s 内接管（只有 crash/session-death 触发 failover），违反 SC-004。
> as-built 改为 **row（`expires_at` TTL）作权威**：单条 `ON CONFLICT … WHERE 过期或自己` UPSERT
> 即原子 CAS（Postgres 行锁串行化并发 acquirer，败者重评 WHERE 得 0 行 → 竞争），**不需要 advisory
> lock**，failover 由 TTL 驱动，与 redis/fake 同语义。elector 因此**无状态**（每次调用一条 pool query，
> 无 held-conn map / 无 Hijack）。原「session 断即时 failover」的优点换成「TTL 接管」——但 TTL 接管
> 对挂起场景正确，advisory-lock 对挂起场景**错误**，故净收益为正。redis 两 key 用 `{reconcilerID}`
> hashtag colocate（否则 Redis Cluster 多 key Lua = CROSSSLOT，C1/F2）；epoch key 在 acquire **与
> renew** 均刷新 TTL（否则长持有 leader 的 epoch key 到期 → 单调计数器归零 → fencing 失效，C1/F1）。

### 4.2 lease/token 模型 + RTO + lost-lease 中断

- `AcquireLease` 成功返回 `LeaseToken{Epoch, ExpiresAt = now + LeaseDuration}`；`Loop` 仅在持
  lease 时 dispatch `Reconcile`，follower 在 `awaitProbe` 等待。
- **lost-lease 中断（A6 必做，best-effort 收窄窗口）**：`Loop` 必须从 lease 派生 lease-scoped
  ctx，并在 `RenewLease` 失败 / lease 被观察到丢失的**瞬间** cancel 该 ctx、中断 in-flight
  `Reconcile`——镜像 client-go `release()` 文档警告「cancel ctx 前须确保 lease 守护的代码已完成，
  否则两进程会同时在 critical path」。这只**收窄**不**消除**窗口（暂停/分区下 L1 可能根本来不及
  观察到丢失），故必须叠加 §4.3 fencing。
- RTO（SC-004）：leader 崩溃 → follower 接管 P99 ≤ `LeaseDuration + 1s`（默认 LeaseDuration=15s，
  对标 §2.5）；graceful shutdown（`ReleaseLease`）→ follower 接管 P99 ≤ 1s。**RTO 是接管延迟
  指标，不是「双执行不发生」的保证。**
- `reconcile_leader{reconciler}` gauge：持 lease=1，否则=0（A3 单进程恒置 1，A6 接真实选举）。

### 4.3 fencing：FencedRepository 写路径 CAS（结构性正确性闭环）

leader election 留下的残余窗口由 **monotonic fencing token（Kleppmann DDIA §8.4）+ 写路径
CAS** 兜底，并以**结构性收口**（而非每消费方自觉）落地：

- **monotonic epoch**：lease store 每次成功 `AcquireLease`（**换持有者**）单调递增 `Epoch`；
  `RenewLease` 保持 epoch 不变。PG 用 SEQUENCE / 行版本号在 acquire UPDATE 内 bump；Redis 在
  SETNX-acquire Lua 内 INCR per-reconciler epoch key。
- **为何不照搬 outbox 的 UUID `lease_id`**：outbox fencing（ADR `202605051600`，archtest
  `OUTBOX-LEASE-ID-CAS-01`）用 UUID 做 **identity fencing**——CAS 按「等于当前 lease_id」放行，
  是单次换手语义（任何 stale token != current 即失败）。reconcile 的设备写可能在 **L1→L2→L3
  多次换手后**才迟到落地；UUID 只能判「不等」不能判「更旧」，无法拒绝乱序迟到写。故 reconcile
  **必须用单调 epoch**：资源行记「已见最高 epoch」，CAS 拒绝**所有** `incoming_epoch < 已见最高`
  的写（`UPDATE ... WHERE incoming_epoch >= row.last_epoch`），而不仅是非等值。
- **FencedRepository seam（结构性收口）**：`Loop` 持 `LeaseToken{Epoch}`，给每次 `Reconcile`
  注入一个 epoch-bound 的 `FencedWriter`（而非让 `Reconcile` 直接写裸 cell 表）。L4 reconciler
  **唯一拿到的写面**是这个 epoch-bound handle，CAS 在 handle 内统一注入 epoch 并拒 stale——
  消费方**结构上无法**发出未 fenced 的设备命令（不是「记得 fence」而是「想绕过都没有 API」），
  把 fencing 从「每消费方义务」升级为「结构不变式」，对齐 AI-robust「违反不可表达」。enforcement
  目标（A6 落地）：上游 Hard = `FencedWriter` 是 Reconciler 唯一写面 + sealed 构造；下游 Hard =
  `RECONCILE-FENCED-WRITE-FUNNEL-01` callsite + `reconciletest.RunFencingConformance`
  real-failure-injection（epoch-N 写在 epoch-N+1 接管后重放，断言 CAS 拒绝、无重复命令）入列。

### 4.4 消费方幂等契约（残余窗口的最终兜底）

即便有 §4.2 中断 + §4.3 fencing，**残余双执行/迟到窗口仍被 ACCEPTED**（分布式 lease 的本质，
无法 100% 消除）。故 **L4 reconcile 消费方契约**：所有 `Reconcile` / 设备写 / 命令发射路径
**必须幂等**（per `EntityID + intent` 的 dedup key）。这与 §5「L4 跨不可靠设备边界」已隐含的
at-least-once 投递语义一致——幂等是 L4 的入场券，不是可选项。

---

## §5 与 saga (#969) / projection harness (#1079) 边界

三者正交，互不重叠：

| 机制 | 触发 | 形态 | 一致性 | 对标 |
|------|------|------|--------|------|
| **reconcile**（本 ADR） | level-triggered（周期扫描非终态） | desired↔actual 无限收敛环 | **L4** DeviceLatent | controller-runtime |
| **saga**（#969） | edge-triggered（事件） | 有限步骤前向编排 + 补偿（跑完即终态） | **L3** WorkflowEventual | Temporal |
| **projection**（#1079） | edge-triggered（outbox 事件） | event → 读模型投影 | **L3**（CQRS 读侧） | Watermill |

判别器：**能否信任事件驱动收敛？** L3 在系统内部走可靠 outbox（L2 保证投递），边沿触发吃满
→ saga/projection；L4 跨不可靠设备边界，不能等事件，必须周期扫描主动驱动 → reconcile。
reconcile **仅用于 cell 治理 / 设备收敛，不做业务编排**（业务编排是 saga 的活，
controller-runtime 也明确二者正交）。

---

## §6 trigger 满足条件 + 激活流程

### 6.1 Trigger Gate（真实业务消费方 cell + examples 端到端切换实施前必须满足）

> **gate 范围说明（§6.2 Amendment 2026-06-02 收窄）**：本 gate 仅针对**真实业务消费方 cell**
> （T1–T4：pkicell.rotation / mdmcell.command / devicelifecycle.cronsweep /
> zerotrust.trustscore）的建设 + `examples` 端到端切换，**不门 kernel 基建 A1–A8**。
> kernel 基建（接口 / Trigger / backoff / LeaderElector + adapter / Builder /
> kernel/command 迁移）已经 maintainer 逐 PR 显式 un-park，不再受本 gate 约束——详见 §6.2。

| # | 触发条件 | 预计 |
|---|---------|------|
| T1 | `pkicell.rotation`（证书续期 L4 环） | winmdm Stage 1, 2027 Q1 |
| T2 | `mdmcell.command`（命令超时重发） | winmdm Stage 2, 2027 Q2-Q3 |
| T3 | `devicelifecycle.cronsweep`（设备墓碑状态机） | winmdm Stage 4, 2027 Q4 |
| T4 | `zerotrust.trustscore`（信任分周期重评） | zt Phase 5, 2029 Q1 |

**满足判定**：T1/T2/T3 至少 **2 个生产 cell** 落地（不含 `examples/iotdevice`），或 T1/T2/T3
任一 + T4。

### 6.2 A1–A3 ahead-of-trigger 例外（本 PR）

trigger gate 的原始约束（spec.md §Trigger Gate）是「trigger 满足前合并任何
`kernel/reconcile/*.go` **代码**均视为违章」，目的是禁止为 2027+ 消费方**预建**未验证的设计。
本 PR 系列（A1–A3）经**显式决策 un-park**，理由：

1. 一个无法被实现验证的设计 ADR 是「不可证伪的推演」——A2/A3 的可运行实现是 ADR 论断
   （≥80% reuse / 接口最小性 / panic 隔离 / leak-free）的**验证手段**，不是预建产能。
2. 当前 repo 只有 gocell 自身、无外部调用方（CLAUDE.md），un-park 决策由 maintainer 行使。
3. A1–A3 不引入任何业务 cell / `mdm/` 目录（plan-D §10 禁止预建的是业务产能，非 kernel 基建）。
4. ~~A4–A10 仍 parked~~ **（已 superseded，见 §6.2 Amendment 2026-06-02）**。

> **§6.2 Amendment 2026-06-02（PR-A6 落地，AI-robust §ADR amendment 落地必查）**：
> §6.2 原 item 4 称「A4–A10 仍 parked」已与现实矛盾——A4（Trigger，#1373）、A5（backoff +
> panic recovery，#1419）、A6（LeaderElector + epoch fencing，#1167，本 PR）均经 maintainer
> **逐 PR 显式 un-park** 后合入 develop。统一决议：**kernel 基建 A1–A8（接口 / Trigger /
> backoff / LeaderElector + adapter / Builder / kernel/command 迁移）不在 trigger gate 内**——
> §6.2 item 1–3 的 un-park 理由（无外部调用方 + 可运行实现是 ADR 论断的验证手段 + 不引入业务
> cell / `mdm/` 目录）对 A4–A8 同等成立。`runtime/command.SweeperLifecycle` 旧路径在 A8 才删，
> A6 不动它（无双轨破裂）。**§6.1 trigger gate 语义同步收窄**：T1–T4 现仅门**真实业务消费方 cell**
> （pkicell.rotation / mdmcell.command / devicelifecycle.cronsweep / zerotrust.trustscore）的建设
> + `examples` 端到端切换，**不门 kernel 基建**。这与 §6.2 item 3「不引入业务 cell」是同一条线的
> 延伸，非新政策。§6.1 表头已同步更新为与本 amendment 一致，冲突已在源头解决。

### 6.3 激活流程（A4–A10）

trigger 满足时：开新 implementation plan（`docs/plans/<ts>-661-kernel-reconcile-active.md`）引用
本 ADR + spec，按 A4→A10 顺序执行（B4 内 A4∥A5 可并行）；A10 merge 后关闭 #661。激活前核验
controller-runtime 对标快照（§2 的 5 个 ref）仍有效，否则先修订本 §2。

---

## §7 威胁矩阵

每条含缓解 + AI-robust 评级（评级载体活在对应 archtest 的 godoc，此处给结论）。

| ID | 威胁 | 缓解 | 评级 |
|----|------|------|------|
| **T-IFACE** | 接口被错误泛化（加 namespace / Priority / Requeue bool，重新引入 K8s 残留） | `RECONCILE-{INTERFACE,REQUEST-FIELDS,RESULT-FIELDS}-FROZEN-01` reflect golden 锁字段/方法集 + 显式拒残留 + 反向盲区自检（由 PR-A2 交付） | **Hard**（违反不可表达——加字段即 CI 红，无 string-anchor 逃逸） |
| **T-CLOCK** | 控制面 probe/requeue/duration 被注入非实时（fake）clock → Start 死锁 / 时间错乱 | `controlPlaneClock` 包私有 sealed type（包外不可构造/替换）+ `PROD-CLOCK-INJECTION-01` host-set 扩 `kernel/reconcile/`（gate(a) + (method,callee) form-uniqueness：`newProbeTimer/newRequeueTimer→NewTimer`、`now→Now`）+ GREEN/RED fixtures（由 PR-A3 交付）。**F4 amendment 重评（A4）**：原描述含「ticker」，但 as-built Loop **无 ticker**（Source + requeue 驱动）；周期节拍由 `TickerTrigger`（§3.2）持有并走**注入 clock**（fake-able 是刻意可测性，非威胁），故 carve-out **不加** `newTicker`——格子不退化，威胁面反而收窄（少一个 real-clock 调用点）。 | **Medium**（永久天花板——stdlib `time.NewTimer`/`Now` free function 在 Go 不可 uncallable；receiver-type 限制 + form-uniqueness 是该形状可达上限，同 runtime/command controlPlaneClock 自评） |
| **T-LEAK** | Loop goroutine（worker / pump / waitingLoop）在 Stop/owner-cancel 后泄漏 | 全 goroutine 由 runCtx 派生 + `WaitGroup` 跟踪 + `done` channel；`Stop` cancel→等 done（StopTimeout budget）。**PR-A5 amendment**：per-requeue goroutine 已被 ONE shared `waitingLoop` goroutine 取代（F6），消除「n 次 transient 泄漏 n 个 goroutine」的放大路径；`waitingLoop` 在 `runCtx.Done()` 退出（clean exit，pending heap items 被 abandoned 而非 channel-blocked）。`goleak.VerifyNone` 守 6 个生命周期测试 + `TestLoop_SharedWaitingLoopNoLeak`（PR-A5 新增，专项验证单-waitingLoop 形态）。**T-LEAK 评级维持 ✅ 不退化**：旧 per-requeue goroutine 设计同样 leak-free（双 select runCtx.Done），A5 是结构简化，非威胁修复；新设计泄漏面更小（固定 goroutine 数 vs 动态）。 | **Medium**（runtime guard + goleak 测试；Go 无法在类型层表达「无 goroutine 泄漏」） |
| **T-PANIC** | 单实体 Reconcile panic 杀 worker goroutine → 整进程崩 / 其他实体停摆 | `recoverReconcile`（PR-A5，替换 A3 的 `safeReconcile`）recover → 转 transient error → 记 metric（resultTransient，无独立 panic label）→ 不影响其他实体；`TestLoop_PanicRecoveredAndOtherEntitiesUnaffected` + `TestRecovery_PanicConvertsToError` + `TestRecovery_PanicMetricRecorded` 守（PR-A5 #1166 落地，panic → transient 语义已兑现） | **Medium**（runtime recover guard + 测试；对标 controller-runtime `RecoverPanic`） |
| **T-DUAL** | 多 cell / 多副本并发扫描 → 重复驱动（mdmcell 重发命令） | **leader election 非 fencing**（§4，client-go 明示不保证单 leader）——只 best-effort 收窄窗口。跨副本正确性靠 §4.3 `FencedRepository` + monotonic-epoch 写路径 CAS（结构拒 stale-epoch 写）+ §4.4 消费方幂等；同实例内同 EntityID 由 dirty/processing dedup（F5，PR-A5 已落地）串行：processing 标记下互斥（同时到达的 trigger 被 coalesced 到 dirty 等待一次 re-run，非多路并行），`TestLoop_SameEntityIDSerial` + `TestLoop_DirtyDedup_CoalescesDuplicates` 守。**T-DUAL 评级维持 ✅ 不退化**：F5 是对 A3 sync.Map skip-if-busy 的强化——旧方案丢弃 duplicate（level-triggered 安全，但错过了一次 re-run）；新方案将 duplicate coalesced 为一次 re-run，收敛更快，不引入新的 dual-execution 路径。 | 同实例串行 **Medium**（runtime guard + 测试，已兑现）；跨副本正确性 **设计**（A6：`FencedWriter` 上游 Hard + `RECONCILE-FENCED-WRITE-FUNNEL-01` 下游 Hard + `RunFencingConformance` real-failure-injection 后定级；leader election 永远只是 best-effort 收窄，不计入正确性保证） |
| **T-LEADER** | leader 流转失败（双 leader / 长期空窗） | lease/renew 模型（§4.1–4.2）+ lost-lease ctx-cancel 中断（§4.2，收窄）；fail-closed（lease 故障 follower 不抢）；RTO ≤ LeaseDuration+1s（接管延迟，**非**双执行保证）。**双 leader 不靠 lease 排除**——靠 §4.3 epoch fencing CAS 让旧 leader 迟到写被结构拒绝 | **设计**（A6 落地 lease 模型 + epoch fencing + real-failure-injection conformance 后定级；明确 leader election ≠ fencing） |
| **T-FENCE** | 旧 leader 迟到设备写绕过 fencing → 落地为重复命令（leader election 残余窗口的兜底失效） | §4.3 `FencedRepository`：`Loop` 只给 Reconciler epoch-bound `FencedWriter`，写路径 CAS 拒 `incoming_epoch < 已见最高`（**单调 epoch**，非 outbox 的 UUID identity-fencing）；绕过在 type system 不可表达（消费方无裸写面）。**⚠️ Redis-eviction residual（C6）**：Redis adapter 的 epoch **值** provenance 依赖 epoch key 持久性——live-holder 路径（acquire same-holder + renew）缺失即 fail-closed，但 free-holder 分支 eviction 后从 1 重建无法 fail-closed（first-acquire 与 post-eviction 不可区分）；缓解 = 30d TTL 刷新 + 非 `allkeys-*` eviction policy + 监控；**严格跨副本 fencing 选 PG adapter（持久 epoch SoR）**。写面 Hard 不退化（与 epoch 值 provenance 正交）。 | **设计**（A6：上游 Hard = `FencedWriter` 唯一写面 + sealed 构造；下游 Hard = `RECONCILE-FENCED-WRITE-FUNNEL-01` callsite + conformance 入列；leader election ≠ fencing 由本行结构兜底）；Redis epoch provenance **⚠️ residual（accepted，见 round-3 C6）** |
| **T-BUILDER** | 消费方裸构造 Loop 绕过 metric/leader/backoff wiring | **已交付**（PR-A7）：上游 Hard 含两个子声明——(i) 带字段赋值的复合字面量（`reconcile.Loop{field: v}`）在包外是编译错误（type system 封闭）；(ii) 零值字面量 `reconcile.Loop{}` 仍可编译但被 `RECONCILE-BUILDER-FUNNEL-01` archtest（AST+Unalias ban）在下游 Hard 拦截，禁止其出现在 `kernel/reconcile` 包外——两者共同封闭全部裸构造路径。临时 `RECONCILE-LOOP-CONSTRUCTION-ALLOWLIST-01` 退役 | **已闭环 Hard**（上游 Hard 字段私有化封闭含字段赋值的字面量 + 下游 Hard callsite ban 封闭零值字面量；双侧均 Hard，无过渡 Medium） |

> PR-A7 已交付：`Loop` 所有配置字段私有化，Builder 是唯一公开构造入口（`reconcile.New(r).With*().Build()`）。
> 上游 Hard 封闭含字段赋值的复合字面量（`reconcile.Loop{field: v}` 包外编译错误）；零值字面量
> `reconcile.Loop{}` 仍可编译但由下游 Hard `RECONCILE-BUILDER-FUNNEL-01` AST+Unalias ban 拦截——
> 两者合力封闭全部裸构造路径。A3–A6 窗口期使用的临时 Medium archtest
> `RECONCILE-LOOP-CONSTRUCTION-ALLOWLIST-01` 已退役，funnel 双侧均 Hard 闭环。
> kernel/command 不再在允许列表中——它无任何 Loop 字面量（grep 确认），A8 迁移将使用 Builder。

> **F5 amendment 重评（AI-robust §ADR amendment 落地必查）**：本次 review 把 leader election 从
> 「单实例 fencing 保证」更正为「best-effort 收窄，非 fencing」。受影响格子逐行重评：
> - **T-DUAL 缓解列**：原文「`LeaderElector` 单实例保证」是 overclaim（client-go 自身文档否认
>   fencing），**已同 PR 重写**为「leader election best-effort + §4.3 epoch fencing CAS + §4.4
>   幂等」——非保留原文加注，避免两套真理源。
> - **T-LEADER 缓解列**：原文「lease/renew/token 模型」被误当作双 leader 的排除手段，**已重写**
>   为「lease 收窄 + epoch fencing 结构兜底」。
> - **新增 T-FENCE 行**：覆盖「旧 leader 迟到写」这一原矩阵漏掉的威胁；评级 **设计**（A6 落地
>   FencedWriter funnel 后定级）。
> - 没有格子从 ✅ 退化为 ❌ 而无补偿：跨副本正确性原本就标「设计」（A6 未落地），本次只是把
>   *保证来源* 从 lease（错）改为 epoch fencing + 幂等（对），并把 A6 验收门槛写死，使 A6 实现者
>   无法回退到「信 lease」的旧错。fencing 设计是 docs（不建代码），不违反 §6 trigger gate。

> **§Amendment 2026-06-01 (PR-A5 #1166) — F5/F6 落地威胁矩阵逐行重评**：
> PR-A5 落地 F5（dirty/processing dedup）+ F6（shared waitingLoop delaying queue）+
> per-entity 指数退避（5ms..1000s no-jitter）。依 AI-robust §"ADR amendment 落地必查"规则，
> 逐行重评：
> - **T-LEAK**：旧描述「per-requeue goroutine 双 select runCtx.Done」已被 ONE shared waitingLoop
>   goroutine 取代（F6），goroutine 在 runCtx.Done() 退出，pending heap items abandoned（clean
>   exit）。旧设计 leak-free；新设计泄漏面更小（固定 goroutine 数）。
>   **结论：✅ 不退化**（仍 leak-free）。goleak regression test `TestLoop_SharedWaitingLoopNoLeak`
>   (PR-A5) 专项验证单-waitingLoop 形态。缓解列已同 PR 重写为当前真值，不保留旧描述。
> - **T-DUAL**：旧描述「sync.Map inflight 串行，level-triggered 丢重复=skipped」已被 F5
>   dirty/processing dedup 取代（processing 标记 + dirty map，coalesced re-run）。F5 是强化，
>   不引入新的 dual-execution 路径——同 EntityID 仍严格串行（`entityMu` 下互斥）。
>   **结论：✅ 不退化**（更强：duplicate 不再丢弃，而是 coalesced 触发一次 re-run）。
>   缓解列已同 PR 重写为当前真值。
> - **T-PANIC**：旧描述「A5 细化 panic 分类/taxonomy（待落地）」已落地（`recoverReconcile`
>   wrapper，panic → transient，无独立 panic label）。
>   **结论：✅ 不退化**。缓解列已更新为当前真值（`recoverReconcile` 取代 `safeReconcile`）。
> - **其他格子**（T-IFACE / T-CLOCK / T-LEADER / T-FENCE / T-BUILDER）：PR-A5 不涉及这些域，
>   评级不变，无需重评。
> - **没有格子从 ✅ 退化为 ⚠️/❌**：F5/F6 是调度内核的结构简化 + 强化，非行为退步。
> - **残余风险（已知）**：无界 distinct EntityID 来源（如受攻击的 Source）可使 heap/backoff map 无
>   上界增长；缓解：§3.1 S1 bounded-set 契约（EntityID 必须来自 cell-local 表主键集）+
>   `MaxConcurrentReconciles` 限制并发。硬 cap（上限整数）作为 defense-in-depth 已评估并
>   延后（A5 scope 外，deferred）。

> **§Amendment 2026-06-02 (PR-A6 #1167) — leader-elect + epoch fencing 落地威胁矩阵逐行重评**：
> PR-A6 落地 `LeaderElector`（lease/renew + monotonic Epoch）+ 2 adapter（redis SETNX+INCR /
> postgres session advisory-lock + 行级 epoch CAS）+ `FencedRepository`/`FencedWriter` sealed
> seam + `RunLeaderConformance`/`RunFencingConformance`。依 AI-robust §"ADR amendment 落地必查"
> 逐行重评受影响格子（无格子 ✅→⚠️/❌）：
> - **T-LEADER**：缓解列从「（A6 落地…后定级）设计」更新为 **已兑现**：lease/renew 模型 +
>   lost-lease ctx-cancel 中断（`Loop.renewLoop` 失败瞬间 `leaseCancel()`，`TestLoop_LeaderElect
>   LostLeaseCancelsInflight` 守）+ fail-closed（`AcquireLease` 错误→不 dispatch）已落地；接口由
>   `RECONCILE-LEADER-INTERFACE-FROZEN-01`（**Hard** reflect golden）冻结。**评级：Hard**（接口冻结）
>   + 行为 Medium（runtime guard + 测试）。leader election ≠ fencing 维持不变（best-effort 收窄）。
> - **T-DUAL / T-FENCE**：跨副本正确性从「设计」更新为 **已兑现**（fencing 评级口径见下方
>   round-2 amendment C3——「上下游均 Hard」是 overclaim，已更正为三向量评级：伪造 epoch =
>   type-system Hard / mint = Hard / 消费方直调 ApplyFenced = archtest 下游）。`FencedWriter` 字段
>   + 构造器 unexported（reflect 锁 seal）；`RunFencingConformance` real-failure-injection（epoch-N
>   写在 epoch-N+1 接管后重放 → 单调 CAS 拒绝 + 无重复 effect）对 fake/redis/postgres 三实现入列。
>   monotonic-epoch（非 outbox UUID identity-fencing）已落地。
> - **新增 enforcement T-IMPL**（分层卫生）：`RECONCILE-LEADER-IMPL-FUNNEL-01` 限定 `LeaderElector`
>   实现 ⊆ {adapters/redis, adapters/postgres, reconciletest fake}。**评级：Medium，永久 Go 天花板**
>   （Go 无法 seal interface 实现，同 #851/#893/#1282；won't-do）。**非正确性闭环**——跨副本正确性
>   由 T-DUAL/T-FENCE 的 Hard 兜底，本规则只防「业务包手搓 elector 绕过 adapter 边界」的分层 smell。
> - **T-BUILDER**：A6 新增 `Loop.Leader` / `Loop.FencedRepo` exported 字段（过渡），当时由
>   临时 `RECONCILE-LOOP-CONSTRUCTION-ALLOWLIST-01`（Medium 上游 + Hard 下游）守。PR-A7 已交付：
>   全部配置字段私有化，funnel 上升为双侧 Hard（`RECONCILE-BUILDER-FUNNEL-01`），ALLOWLIST-01 退役。
> - **as-built 偏离 controller-runtime（已核实并记录）**：client-go/controller-runtime 丢 lease 时
>   `log.Fatal()` 退进程；GoCell `Loop` 是 cell lifecycle hook 而非独立进程，故丢 lease 后
>   `leaseCancel()` 中断 in-flight + 转 follower 重新竞争（不退进程）——cancel-and-recontend，理由
>   见 `loop.go::leaderManage` godoc。
> - **PG 时间源 + 机制（已被 round-2 修订取代）**：as-built PG lease 时间戳由 DB `now()` 计算
>   （单一时间权威）。**注意**：本条原文称 PG 用 session-scoped advisory lock 且「与 §4.1 一致」——
>   该机制在 round-2 深审（C2）被判定为**错误**并整体替换为 row-TTL CAS（见 §4.1 Amendment
>   2026-06-02 round-2 + 下方 round-2 amendment）；本行仅保留「时间源 = DB now()」结论，机制描述
>   以 §4.1 Amendment 为准。

> **§Amendment 2026-06-02 round-2 (PR-A6 #1167 深审 C1–C5) — 生产语义修正**：
> 第二轮深审（带 Redis Cluster / PG advisory-lock / fencing 边界的生产/开源对标）暴露了
> 首版的核心正确性缺陷，逐项修正：
> - **C2（PG，架构）**：session advisory-lock 非 TTL 权威 → 改 **row-TTL CAS**（见 §4.1 Amendment）。
>   挂起-不崩溃的 leader 现在也会在 `expires_at` 后被接管。
> - **C1（Redis，正确性）**：① epoch key 仅 acquire 设 TTL、renew 不刷新 → 长持有 leader 的 epoch
>   到期归零破坏 fencing → renew 同步刷新 epoch key TTL（专用 `reconcileRenewScript`）；② holder/epoch
>   两 key 无共享 hashtag → Redis Cluster CROSSSLOT → 改 `{reconcilerID}` hashtag colocate。
> - **C3（fencing Hard 过度声明，诚实重评）**：T-DUAL/T-FENCE 行原称「`FencedWriter` 上游
>   type-system Hard」覆盖整个「消费方无法发未 fenced 写」——**overclaim**。诚实三向量评级：
>   (1) 伪造 writer 的 epoch = type-system Hard；(2) 越过 mint/inject = Hard（unexported）；
>   (3) 消费方**直调自己的 `ApplyFenced(ctx,id,epoch,mut)`** 绕过 writer = **archtest 下游
>   caller-allowlist**（非 type-system；epoch 是消费方存储 CAS 的必需入参，本质无法在类型层封死）。
>   故整体不是「结构上不可能」，而是「Loop 必供正确 epoch（Hard）+ 消费方不能 out-of-band 触达
>   ApplyFenced（archtest）」。enforcement = `RECONCILE-FENCED-WRITE-FUNNEL-01` 新增 ApplyFenced
>   caller-allowlist（⊆ fenced.go + conformance.go）。
> - **C4（identity）**：holderID 由 adapter 构造期 `idutil.NewUUID()` **内部铸造**（不再取参），
>   跨进程 holderID 复用（被当同一 holder 重入）在构造上不可能。
> - **C5（运维/DX）**：`leaderRetryPeriod` 2s→1s（兑现 graceful P99≤1s）；告警 PromQL 改
>   `sum by (reconciler)` + `absent()` 兜底全 series 消失；`reconcile_leases` 纳入 schema_guard；
>   Loop.Leader/FencedRepo 加 Start 期 typed-nil fail-fast。
> 受影响威胁格子均不退化：T-LEADER/T-DUAL/T-FENCE 的「已兑现」结论仍成立，只是**实现机制**
> （PG row-TTL、redis hashtag、fencing 三向量评级）被更正为生产可用 + 诚实形态。

> **PR-A6 round-3 review（C6 — Redis epoch-key fail-closed + eviction residual 诚实重评）**：
> C1/F1 修了「renew 不刷 epoch-key TTL → 自然过期归零」，但**未**覆盖 epoch key 被
> eviction / 运维误删 后的 live-holder 路径。两处 fail-OPEN 缺口在 round-3 闭合：
> - **same-holder acquire 分支**（`reconcileAcquireScript`）：原 `if e == false then e = 0`
>   把缺失 epoch 静默当 0 → 持有 epoch N 的 leader 重入后 token epoch 倒退至 0（zombie
>   write 被错误 fence）。改为 `redis.error_reply` → `AcquireLease` 返回非 `ErrLeaseHeld`
>   错误 → `leaderManage` fail-closed（不 dispatch）+ Warn 日志；stale holder key 不刷新，
>   自然 lapse 后下次走 free-holder 路径干净重启。
> - **renew 分支**（`reconcileRenewScript`，**长持有 leader 的主路径**）：原脚本对缺失 epoch
>   key 只 `EXPIRE`（Redis no-op 返回 0）却仍 `return 1` 成功 → leader 带着「Redis 已无
>   epoch key、本地 token 仍 epoch N」继续持有，换手时 free-branch 从 1 重建 → 新 leader
>   epoch 1 < 旧僵尸 leader epoch N 的 **fencing 反转**静默落地。改为先 `GET KEYS[2]`，缺失
>   即 `redis.error_reply` → `RenewLease` 返回非 `ErrReconcileLeaseLost` 错误 → `renewLoop`
>   lease-ctx cancel 结束 term → 重新 acquire（同样 fail-closed）。
>   守卫：单测 `TestReconcileElector_EpochKeyMissing_FailClosed`（mock，两路径）+ 真 Redis
>   `TestIntegration_ReconcileElector_EpochKeyMissing_FailClosed`（Lua error_reply 实跑）+
>   两处脚本 golden（`Test*ScriptContent`）。
>
> **威胁矩阵逐行重评（C6）——T-DUAL / T-FENCE / T-LEADER 的 Redis-eviction residual（accepted risk）**：
> Redis 单调 epoch 的正确性依赖 **epoch key 在 Redis 的持久性**。round-3 把 live-holder
> 路径（acquire same-holder + renew）的缺失检测做成 fail-closed，但 **free-holder 分支**
> （`cur == false` → `INCR` 一个被 evict 成 nil 的 key → 从 1 重建）**无法**做成 fail-closed：
> 「史上第一次 acquire」与「eviction 后接管」在 Redis 上不可区分（皆无 epoch key）。因此：
> - **Redis adapter 的单调 fencing 是 eviction-best-effort**——全 key 丢失后只能从 1 重建，
>   无法提供跨 eviction 的永久单调性。这是 **accepted residual risk**，不是 round-3 能修的 bug。
> - **缓解**：① epoch key 在 acquire+renew 均刷 30d TTL（远超任何 lease）；② 部署 Redis 配
>   `volatile-*` eviction policy 或排除 reconcile keyspace，避免 `allkeys-*` 误删；③ 监控
>   epoch key 存在性 + `reconcile_leader` gauge 抖动告警。
> - **强持久单调保证选 Postgres adapter**：`reconcile_leases` 行是持久 epoch SoR
>   （`ON CONFLICT epoch+1`，release 只置 `expires_at` 不删行），无 eviction 面，是需要严格
>   跨副本 fencing 时的权威实现。
> - 评级影响：T-FENCE / T-LEADER 的 `FencedWriter` 上游/下游 Hard（写面封闭）**不退化**——
>   该 Hard 约束的是「消费方无裸写面」，与 epoch **值的 provenance** 正交；Redis-eviction
>   residual 只削弱 **Redis 来源** epoch 的单调 provenance（标 ⚠️ residual，缓解如上 + 迁
>   PG），不影响 PG 来源 + 写面封闭结论。

A8 删除 `runtime/command.SweeperLifecycle` + `SweepTicker` 命名，`kernel/command.Sweeper` 改为
实现 `reconcile.Reconciler`：

- **不留 alias / 不留 deprecation**——`SweeperLifecycle` / `SweepTicker` 名字完全删除，由
  `RECONCILE-NAMING-FROZEN-01`（A8）grep production 0 命中守 frozen。
- **不留双轨**——A8 后只有 `reconcile.Loop` 一个 control-loop 抽象（A9 把 `examples/iotdevice`
  端到端切换验证）。
- `runtime/command/lifecycle.go`（446 LoC）整包删除，调用方（`runtime/command/bootstrap_phase.go`
  等）同 PR 迁移到 `reconcile.Builder`。
- A8 同步迁移 `clock_invariants_test.go`：runtime/command 的 controlPlaneClock carve-out 随
  `lifecycle.go` 删除而退场（reconcile 的 carve-out 已在本 PR/A3 加入 `controlPlaneClockHosts`）。

> 当前 PR（A1–A3）**不**删除 SweeperLifecycle——它仍是 runtime/command 的活跃路径；§8 是 A8
> 的设计声明，A8 受 §6 trigger gate 封存。

ref: kubernetes-sigs/controller-runtime pkg/reconcile/reconcile.go
ref: kubernetes-sigs/controller-runtime pkg/internal/controller/controller.go
ref: kubernetes-sigs/controller-runtime pkg/builder/controller.go
ref: kubernetes/client-go util/workqueue/default_rate_limiters.go
ref: kubernetes/client-go tools/leaderelection/leaderelection.go
ref: gocell runtime/command/lifecycle.go（SweeperLifecycle，调度骨架平移源）（deleted in PR-A8 #1169; scheduling now lives in kernel/reconcile.Loop）
ref: gocell kernel/command/sweeper.go（既有 L4 控制环）
