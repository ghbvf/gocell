# ADR 661 — kernel/reconcile L4 desired-state 收敛控制环设计

| 字段 | 值 |
|------|---|
| ADR ID | 661 |
| 状态 | **Accepted（设计冻结）；A1–A3 ahead-of-trigger 验证 stack 依次合入 develop，A4–A10 PARKED-ON-TRIGGER（详见 §6）** |
| 日期 | 2026-05-29 |
| Issue | [#661](https://github.com/ghbvf/gocell/issues/661)（父）/ [#1162](https://github.com/ghbvf/gocell/issues/1162)（PR-A1） |
| Spec | `docs/plans/specs/202605262359-661-kernel-reconcile-{spec,plan,tasks}.md` |
| 一致性级别 | **L4 DeviceLatent**（issue 第一性原理重评结论） |

> 本 ADR 是 `kernel/reconcile` 的设计权威源。其设计经本 stack 的 PR-A2（3 件套最小核）+
> PR-A3（Loop 调度骨架）可运行实现验证（同 stack 开发，`go test -race ./kernel/reconcile/`
> 通过、coverage 90.8%），故本文记录的是**经代码验证的设计**而非纯前瞻推演——`§2 ≥80%
> reuse`、`§3 接口形态`、`§7 panic 隔离`等论断均有该实现背书。
> A1–A3 是 **stacked PR，按 A1（base develop）← A2 ← A3 依次合入**（plan.md「每 PR merge
> 后 trunk 可发布」）；本文用「由 PR-Ax 交付」标注每条论断的承载 PR——读者在 develop 上看到
> 的实现取决于已合入到哪一 PR：单独合入 A1 时 develop 上尚无 A2/A3 代码，因此 A2/A3 交付的
> 形态/数值（接口、Loop、metrics、archtest 等）是「设计 + 该 PR 兑现」而非 A1 合入即存在。
> 设计与实现分歧时以本 ADR 为准，并在同 PR 内修正实现或修订本 ADR。

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

**交付划分**：本 stack 分三 PR——PR-A1（本 ADR）/ PR-A2（接口 + 3 frozen archtest）/
PR-A3（Loop 调度骨架 + 4 metrics + clock carve-out），各经 `go test -race` + 90.8% coverage
验证后依次合入 develop（A1←A2←A3）。下文「由 PR-Ax 交付」标注每条论断的承载 PR——某论断
背书的代码只有在其承载 PR 合入后才存在于 develop。PR-A4–A10（Trigger / Backoff /
LeaderElector / Builder / 迁移 / 文档）受 §6 trigger gate 封存。

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

## §2 对标 controller-runtime（快照 @ main，2026-05-29）

对标快照固定在以下 5 个上游源；后续 PR commit message 引用对应 `ref:` 行。

### 2.1 `pkg/reconcile/reconcile.go` — Reconciler / Request / Result

ref: `kubernetes-sigs/controller-runtime pkg/reconcile/reconcile.go`
（https://raw.githubusercontent.com/kubernetes-sigs/controller-runtime/main/pkg/reconcile/reconcile.go）

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

**GoCell 适配（由 PR-A3 交付）**：`Loop` 起 `MaxConcurrentReconciles` 个 worker（默认 1）从
内部 queue 取 `Request`；`process()` 做同 ID 串行（`sync.Map` inflight，level-triggered
下重复触发安全丢弃 = `resultSkipped`）+ panic recovery（`safeReconcile`，单实体 panic 不杀
worker，转 transient）+ 按 `Result`/error 重排。**未采纳 workqueue 的 dirty/processing 去重 +
rate-limited delaying queue**：A3 用「skip-if-busy（丢重复，level-triggered 安全）+ per-requeue
goroutine（受 runCtx 取消、WaitGroup 跟踪、无泄漏）」，比上游 workqueue 简（见 §7 威胁矩阵
T-LEAK）；指数退避 rate limiter 是 PR-A5 的事（上游 default `5ms..1000s`，见 §2.4）。

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

### 3.2 Trigger（PR-A4 设计）

```go
type Trigger interface { Start(ctx context.Context, queue chan<- Request) error }
func TickerTrigger(interval time.Duration) Trigger   // 周期全量重观察（替代 controller-runtime resync Source）
func ChannelTrigger(in <-chan Request) Trigger       // outbox 事件唤醒
```

替代 controller-runtime `Source`，最小 2 实现。PR-A3 的 `Loop.Source <-chan Request` 是 Trigger
产出的原始 channel 接缝（A3 测试直接注入 channel，A4 由 Trigger 产出）。Loop 控制面时钟走
`controlPlaneClock` carve-out（见 §7 T-CLOCK）。

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
	AcquiredAt   time.Time
	ExpiresAt    time.Time
}
```

接口在 kernel 声明、实现在 adapter（对齐 `outbox.Emitter` / `persistence.CellTxManager`
kernel-driven adapter-implements 模式，满足分层）。`LeaseToken` 字段对标 §2.5
`LeaderElectionRecord`，但用中立形态（不绑 K8s `coordination.k8s.io/Lease`），扩 etcd 等
adapter 不改 kernel。

### 3.5 Builder（PR-A7 设计）

```go
func New(reconciler Reconciler) *Builder
func (*Builder) WithTrigger(Trigger) *Builder
func (*Builder) WithLeader(LeaderElector) *Builder
func (*Builder) WithConcurrency(int) *Builder
func (*Builder) WithBackoff(...) *Builder
func (*Builder) WithMetrics(Metrics) *Builder
func (*Builder) Build() (*Loop, error)   // Loop 构造私有化：Builder 是唯一公开入口
```

funnel：`Loop` 公开构造私有化（PR-A7 把 A3 的 exported `Loop{}` 字面量构造收口到 Builder），
`RECONCILE-BUILDER-FUNNEL-01` 锁「消费方构造 Loop 必经 Builder」（funnel 上游 Hard = 构造函数
私有化；下游 Hard = callsite allowlist）。**注**：PR-A3 阶段 `Loop` 字段 exported（支持
struct 字面量构造，供测试 + kernel/command 迁移过渡）；A7 收口为私有 + Builder。

### 3.6 Metrics（由 PR-A3 交付）

`RegisterMetrics(p Provider) (Metrics, error)` 单源注册 4 件：`reconcile_total{reconciler,result}`
（result ∈ success/transient/permanent/skipped）/ `reconcile_duration_seconds{reconciler}` /
`reconcile_in_flight{reconciler}` / `reconcile_leader{reconciler}`。注入式 pre-bound vec +
`preflight`（recover 包裹 label 校验，misconfig → fail-fast Start，对齐
`runtime/command.preflightSweepErrorCounter`）。

> result 标签集冻结为 {success/transient/permanent/skipped}（FR-010）；recovered panic 归为
> transient（可重试）。**spec 内部矛盾备案**：tasks.md T18 提到 `result="panic"` 第 5 标签，
> 与 FR-010 四标签集冲突；本 ADR 采 FR-010 四标签，panic→transient；若 A5 需独立 panic
> disposition，由 A5 同 PR 修订 FR-010 + 本 §3.6。

---

## §4 leader-elect 设计

多副本部署时，`Loop` 必须保证**单实例扫描**（否则 mdmcell 会向设备重复发命令）。

### 4.1 两 adapter

| adapter | 机制 | 续约 | 失败语义 |
|---------|------|------|---------|
| `adapters/redis` | `SET key holderID NX PX leaseMs`（SETNX + TTL） | 周期 `PEXPIRE`（< LeaseDuration） | TTL 到期自动释放，follower SETNX 接管 |
| `adapters/postgres` | `pg_try_advisory_lock(hash(reconcilerID))` | session-scoped lock + heartbeat goroutine | session 断 → lock 自动释放 |

复用 `adapters/redis` Cache 的 cell-namespaced key 约定（lease key 带 namespace 前缀）。

### 4.2 lease/token 模型 + RTO

- `AcquireLease` 成功返回 `LeaseToken{ExpiresAt = now + LeaseDuration}`；`Loop` 仅在持 lease
  时调 `Reconcile`，follower 在 `awaitProbe` 等待。
- RTO（SC-004）：leader 崩溃 → follower 接管 P99 ≤ `LeaseDuration + 1s`（默认 LeaseDuration=15s，
  对标 §2.5）；graceful shutdown（`ReleaseLease`）→ follower 接管 P99 ≤ 1s。
- `reconcile_leader{reconciler}` gauge：持 lease=1，否则=0（A3 单进程恒置 1，A6 接真实选举）。

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

### 6.1 Trigger Gate（A4–A10 实施前必须满足）

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
4. A4–A10（leader adapter / Builder funnel / kernel/command 迁移 / examples 切换）**仍 parked**，
   `runtime/command.SweeperLifecycle` 旧路径不动（无双轨破裂）。

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
| **T-CLOCK** | 控制面 ticker/probe/duration 被注入非实时（fake）clock → Start 死锁 / 时间错乱 | `controlPlaneClock` 包私有 sealed type（包外不可构造/替换）+ `PROD-CLOCK-INJECTION-01` host-set 扩 `kernel/reconcile/`（gate(a) + (method,callee) form-uniqueness：`newProbeTimer/newRequeueTimer→NewTimer`、`now→Now`）+ GREEN/RED fixtures（由 PR-A3 交付） | **Medium**（永久天花板——stdlib `time.NewTimer`/`Now` free function 在 Go 不可 uncallable；receiver-type 限制 + form-uniqueness 是该形状可达上限，同 runtime/command controlPlaneClock 自评） |
| **T-LEAK** | Loop goroutine（worker / pump / requeue 定时）在 Stop/owner-cancel 后泄漏 | 全 goroutine 由 runCtx 派生 + `WaitGroup` 跟踪 + `done` channel；`Stop` cancel→等 done（StopTimeout budget）；per-requeue goroutine 双 select runCtx.Done。`goleak.VerifyNone` 守 6 个生命周期测试（由 PR-A3 交付，`-race` 通过） | **Medium**（runtime guard + goleak 测试；Go 无法在类型层表达「无 goroutine 泄漏」） |
| **T-PANIC** | 单实体 Reconcile panic 杀 worker goroutine → 整进程崩 / 其他实体停摆 | `safeReconcile` recover → 转 transient error → 记 metric → 不影响其他实体；`TestLoop_PanicRecoveredAndOtherEntitiesUnaffected` 守（由 PR-A3 交付）。A5 细化 panic 分类/taxonomy | **Medium**（runtime recover guard + 测试；对标 controller-runtime `RecoverPanic`） |
| **T-DUAL** | 多 cell / 多副本并发扫描 → 重复驱动（mdmcell 重发命令） | `LeaderElector` 单实例保证（§4，PR-A6）；同实例内同 EntityID 由 `inflight` sync.Map 串行（level-triggered 丢重复=skipped，由 PR-A3 交付，`TestLoop_SameEntityIDSerial` 守） | 同实例串行 **Medium**（runtime guard + 测试）；跨副本 leader **设计**（A6 落地后补 conformance） |
| **T-LEADER** | leader 流转失败（双 leader / 长期空窗） | lease/renew/token 模型（§4）；fail-closed（lease 故障 follower 不抢）；RTO ≤ LeaseDuration+1s；2 adapter conformance（PR-A6） | **设计**（A6 落地 + real-failure-injection conformance 后定级） |
| **T-BUILDER** | 消费方裸构造 Loop 绕过 metric/leader/backoff wiring | Builder funnel：`Loop` 构造私有化 + `RECONCILE-BUILDER-FUNNEL-01`（PR-A7） | **设计**（A7 落地后：上游 Hard 构造私有化 + 下游 Hard callsite） |

> A3 阶段 `Loop` 字段 exported（过渡，支持 struct 字面量 + kernel/command 迁移），故 T-BUILDER
> 的上游 Hard 在 A7 才闭环；A3–A6 期间「裸构造」由 code review 兜底，不是 silent gap（A7 是
> 已规划的 funnel 收口 PR）。

---

## §8 不向后兼容声明

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
ref: gocell runtime/command/lifecycle.go（SweeperLifecycle，调度骨架平移源）
ref: gocell kernel/command/sweeper.go（既有 L4 控制环）
