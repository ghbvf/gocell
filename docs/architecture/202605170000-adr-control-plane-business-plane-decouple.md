# ADR: 控制面/业务面解耦 — 时钟二分、Owner Ctx、Sweep 错误可观测

> Status: Accepted
> Date: 2026-05-17
> Implementation: PR #212 (worktree 212-control-plane-decouple)
>   Commits: 087ca782a / a44197f19 / 85165f557 / fc289fed2
> Backlog: `BOOTSTRAP-CONTROL-PLANE-DECOUPLE-BUNDLE` (cap-01)
> Source: PR #441 second/third-round review 聚合，3 子条 C.1/C.2/C.3 同根因
> Supersedes: `202605102000-adr-lifecycle-hook-ctx-semantics.md` §D1/§D3/§Consequences

---

## Context

PR #441 review 聚合识别出三个表面不同但同根因的问题，统一登记为
`BOOTSTRAP-CONTROL-PLANE-DECOUPLE-BUNDLE`（cap-01 backlog）：

**C.1 LIFECYCLE-CLOCK-CONTROL-PLANE-DECOUPLE-01**（原 P3/Cx3）

`runtime/command.SweeperLifecycle` 的启动探针（startup probe）原本调用注入的
`clock.Clock.NewTimerAt(...)` 创建 50 ms 窗口。当测试注入 frozen fake clock 且不
手动 `Advance` 时，探针永久阻塞 — 直接死锁 `Start()` 调用。根因：业务可注入时钟
与控制面 scheduling 职责混入同一 `clock.Clock` 字段。

同时，`kernel/command.Sweeper`（PR #441 前）持有 `clk clock.Clock` 字段，既驱动
ticker（控制面 scheduling），又将 `s.clk.Now()` 注入 `SweepOnce`（业务面 deterministic
time）。两面混用让"控制面注入 fake clock"在类型层是合法的，却在行为层造成
deadlock。

**C.2 LIFECYCLE-OWNER-CTX-PROPAGATION-01**（原 P2/Cx3）

`runtime/command.SweeperLifecycle.Start` 原实现：

```go
func (l *SweeperLifecycle) Start(_ context.Context) error {
    runCtx, cancel := context.WithCancel(context.Background())
    go l.Sweeper.Start(runCtx)
    return nil
}
```

worker goroutine 派生自 `context.Background()`，与 assembly 关停信号（ownerCancel）
完全脱钩，唯一取消通道是 `OnStop` 的 `cancel()` 调用。`cells/accesscore/refresh_gc.go`
的 `gc_worker.go:68` 原本同样采用 `context.WithCancel(context.WithoutCancel(ctx))`
自我重根（self-re-root），本质是同一 workaround 的翻版。

**C.3 SWEEPER-OBSERVABLE-01**（原 P1/Cx2）

`kernel/command.Sweeper` 原持有 `onError func(error)` 回调。当 `onError == nil`（默认值）
时，`runTick` 静默吞掉 scanner/Ack 错误，生产故障无可见性。

---

## Decision

### D-A 控制面/业务面时钟二分

**kernel 层（C.1 Hard 载体）**：`kernel/command.Sweeper` 删除全部时钟相关字段
（`clk clock.Clock`、`interval`、`onError`）与方法（`Start`/`Stop`/`WithSweeperInterval`/
`WithSweeperOnError`）。`runTick` 提升为导出方法 `SweepTick(ctx context.Context, now time.Time) error`，
`now` 由调用方（控制面）按 tick 传入。`SweepOnce(entries, now)` 保持纯函数不动。

效果：kernel `Sweeper` 无任何时钟字段。"向 Sweeper 注入 fake clock 控制控制面
scheduling"在 Go 类型系统层不可表达 — 字段根本不存在。

**runtime 层（C.1 Medium carve-out）**：`runtime/command.SweeperLifecycle` 也不再
持有 `clock.Clock` 字段。控制面时间源（ticker + 50 ms 启动探针）由两个私有函数
收敛：

```go
//archtest:allow:clock-injection:control-plane startup-deadlock-regression-C1
func controlPlaneTicker(interval time.Duration) *time.Ticker {
    return time.NewTicker(interval)
}

//archtest:allow:clock-injection:control-plane startup-deadlock-regression-C1
func controlPlaneProbeTimer(d time.Duration) *time.Timer {
    return time.NewTimer(d)
}
```

这两个函数携带函数级 comment-guard `//archtest:allow:clock-injection:control-plane`，
是 `PROD-CLOCK-INJECTION-01` archtest 在 `runtime/command` 包内认可的唯一真实时钟
使用点（允许 allowMarker）。

**AI-rebust 评级（双栏）**：

| 维度 | 评级 | 根据 |
|---|---|---|
| C.1 下游：kernel Sweeper 无时钟字段 | **Hard** | 类型不可表达 — 字段不存在，无论何种 AI 实现变体均无法合法引入 fake clock |
| C.1 runtime 侧：控制面真实时间 carve-out | **Medium** | 函数级 comment-guard；archtest `PROD-CLOCK-INJECTION-01` 的 `clockControlPlaneAllowMarker` 识别并允许这两个函数，carve-out 范围外的 `runtime/command` 调用 `clock.Real()` 仍会被 RED 反向 fixture 捕获 |

**carve-out 唯一真值**：carve-out 的权威登记处是函数本身的 `//archtest:allow:clock-injection:control-plane` marker（in-source）。

不同于 `ERRCODE-KIND-LITERAL-01` 的 carve-out 机制（其独立维护了 `errcodeKindLiteralCarveOuts` map，与 ADR registry 表存在两份真值源，因此需要 `ERRCODE-CARVEOUT-ADR-CONSISTENCY-01` 双向校验 archtest），本 clock carve-out **没有独立的代码侧 map**——marker 本身就是登记，无第二真值源，因此**本 ADR 是文档，不是 enforcement 来源，不新增 consistency archtest**。

**carve-out 盲区已关闭（L4 review 修复）**：`enclosingFuncDeclKey` 已通过
`EachInSubtree[ast.FuncLit]` 递归排除所有嵌套 FuncLit body，确保闭包内
`time.*` 调用不被豁免。反向自检：fixture
`control_plane_exempt_func_closure_violates` 断言 exempt 函数内闭包的
`time.NewTicker` 仍被 flag（RED）。

ref: `tools/archtest/clock_invariants_test.go::enclosingFuncDeclKey`（盲区修复实现）
ref: `tools/archtest/prod_clock_injection_fixtures_test.go`（反向自检 fixture 列表）

**本 clock carve-out 不登记于 `202605121800-adr-archtest-carveout-narrow.md`**：该 ADR 的 `CARVEOUT-REGISTRY` 仅定义 `ERRCODE-KIND-LITERAL-01` 规则的豁免，`ERRCODE-CARVEOUT-ADR-CONSISTENCY-01` archtest 的解析锚点限定在该规则范围内。将 clock carve-out 插入该 registry 会破坏 `ERRCODE-CARVEOUT-ADR-CONSISTENCY-01` 的严格等价断言，导致 CI 误报。

**Medium 同 PR Hard 升级 backlog 登记**（ai-collab.md 要求，不可 silent carryover）：

`CONTROL-PLANE-CLOCK-TYPED-FUNNEL-HARD-UPGRADE-01` — Hard 路径 = 引入 sealed typed
real-only 控制面时钟 funnel（让调用方连 `time.NewTicker`/`time.NewTimer` 都无法
绕过 funnel）。受豁免函数清单（截至本 PR）：

- `controlPlaneTicker` — `runtime/command/lifecycle.go`
- `controlPlaneProbeTimer` — `runtime/command/lifecycle.go`

该 backlog 条目同步维护在 `docs/backlog.md` cap-01 章节。

### D-B OnStart ctx = Owner ctx（controller-runtime 范式）

**单一 ctx 真值**：`cell.LifecycleHook.OnStart(ctx)` 的 ctx 语义从"startup deadline ctx"重定义为"长生命 owner ctx"，对齐 controller-runtime `Runnable.Start(managerCtx)` 范式。

bootstrap 在 `Run()` 内从 `runCtx`（`context.Background()` 派生的 assembly 运行期 ctx，
由 phase10/finalize 取消）派生 ownerCtx：

```go
b.ownerCtx, b.ownerCancel = context.WithCancel(runCtx)
if err := b.lifecycle.Start(b.ownerCtx); err != nil {
    b.ownerCancel()
    return err
}
```

teardown 注册顺序（LIFO 先进后出）：

1. `lifecycle.Stop`（先注册 → 后运行）
2. `ownerCancel`（后注册 → **先运行**）

net effect：`ownerCancel()` → 所有 worker goroutine 自动退出 → `lifecycle.Stop()` 有序排空。

`runtime/command.SweeperLifecycle.Start(ownerCtx)` 内部：

```go
runCtx, cancel := context.WithCancel(ownerCtx)
```

worker 现响应 `ownerCancel`（assembly 关停）与 `OnStop` 的 `cancel()`（OnStop 显式停）
两条路径，任一先到即退出。

`cells/accesscore/refresh_gc.go` 的 `OnStart` 现将收到的 `ownerCtx` 透传给
`worker.Start(ctx)`，`gc_worker.go` 直接用传入 ctx 派生 `runCtx`，删除
`context.WithoutCancel` 自 re-root。

**Design-time 精化偏离 backlog 文案**：backlog `LIFECYCLE-OWNER-CTX-PROPAGATION-01` 描述
"`cell.LifecycleHook` 增 `OwnerCtx context.Context` 字段"作为预设改造方向。本实现
选择**不新增字段**，而是直接重定义 `OnStart(ctx)` 的 ctx 语义为 owner ctx（单一
ctx 真值）。理由：增加 `OwnerCtx` 字段会产生两套 ctx 真值源（StartTimeout ctx
vs OwnerCtx），在 hook 实现者视角是 L3 反模式（两份上下文需分别处理）。`OnStart ctx
= owner ctx` 是更简洁的单一语义，且与 controller-runtime `Runnable.Start(managerCtx)`
一一对应。`runtime/bootstrap/lifecycle.go` 的 `runHook(isStart=true)` 分支直接
传入 ownerCtx，不再 `applyTimeout(StartTimeout)` 包裹。

**StartTimeout 语义降级**：`StartTimeout` 字段保留，但语义变为"hook 自身探针窗口
预算（informational）"——hook 内部用于快速探针（如 SweeperLifecycle 的 50 ms 启动
探针），runner 不再将其作为 OnStart ctx 的 deadline 强制。这是对 ADR
`202605102000` §D1 的精确撤销（见 Supersedes）。

**ADR 202605102000 §D2 重验**：`TestSweeperLifecycle_StartupFailRollback` 在新的
owner ctx 语义下仍通过——rollback 正确 LIFO 运行，goleak 无 goroutine 泄漏。

### D-C Sweep 错误可观测

删除 `onError func(error)` 回调（nil-tolerated 静默反模式整体删除）。

`SweepTick` 聚合所有错误（`errors.Join`）返回给调用方。`SweeperLifecycle.runLoop`
在每个 tick 错误时：

1. `slog.Error("runtime/command: SweepTick error", slog.String("hook", hookName), slog.Any("error", err))`
2. 若 `SweepErrorCounter != nil`，调用 `SweepErrorCounter.With(Labels{"cell": ""}).Inc()`

`SweepErrorCounter` 是可选注入的 `kernelmetrics.CounterVec`（composition root 注入，
nil 则跳过 counter），对齐 `observability.md` 的 `cell` label 约定。

---

## Consequences

### 行为变化（breaking）

- `kernel/command.NewSweeper` 签名变更：删除 `clk clock.Clock` 入参、`WithSweeperInterval`、
  `WithSweeperOnError`。所有调用方必须迁移到新签名（唯一消费方：`examples/iotdevice/cells/devicecell/cell.go`，已同 PR 更新）。
- `runtime/command.NewSweeperLifecycle` 删除 `clk clock.Clock` 入参。
- `SweepTicker` 接口替代旧 `SweeperRunner`（`Start/Stop` 风格）。
- `cell.LifecycleHook.OnStart(ctx)` 语义重定义：ctx 现为 long-lived owner ctx，
  **不**是 startup deadline ctx。

### owner-cancel 级联效果

ownerCancel 先于 lifecycle.Stop 运行后，所有通过 `context.WithCancel(ownerCtx)` 派生的
worker goroutine（sweeper loop、refresh GC loop）在 `lifecycle.Stop` 执行前已收到
取消信号并自行退出。`lifecycle.Stop` 的 `OnStop` 调用仍执行（确认退出、释放资源），
但 worker 通常已退出，OnStop 的 channel `<-done` 可立即返回。

### 威胁分析

| 威胁 | 状态 | 说明 |
|---|---|---|
| fake clock 注入控制面 scheduling | 消除 | kernel Sweeper 无时钟字段（Hard），runtime 层函数级 carve-out（Medium）|
| 启动探针 frozen-clock deadlock | 消除 | controlPlaneProbeTimer 使用真实时间 |
| worker goroutine 在 assembly 关停后仍存活 | 消除 | ownerCancel 先行，worker 响应 ownerCtx.Done() |
| sweep 错误静默 | 消除 | SweepTick 返回聚合错误；runLoop slog.Error + counter |
| OnStop 链路中断导致 worker 永活 | 缓解 | ownerCancel 提供独立取消通道；OnStop 仅为有序排空 |
| StartTimeout 强制 OnStart 超时 | 语义变更已知 | StartTimeout 仅 informational；hook 必须自带快速探针后返回 |

### ref

- kubernetes-sigs/controller-runtime `pkg/manager/internal.go`：`internalCtx=WithCancel(ctx)` → `Runnable.Start(internalCtx)`（owner ctx 贯穿所有 Runnable）
- uber-go/fx `app.go`：OnStart ctx 是 startup-timeout ctx，长期 worker 不可复用；GoCell 选择 controller-runtime 范式更彻底贯穿。
- kubernetes/utils `clock/clock.go`：`RealClock` 是控制面时钟，不可由业务注入替换。
- ADR `docs/architecture/202605021500-adr-kernel-clock-injection.md`：kernel 层时钟注入基线约定。
- ADR `docs/architecture/202605102000-adr-lifecycle-hook-ctx-semantics.md`：被本 ADR §D1/§D3 supersede（见 Supersedes 节）。

---

## Supersedes

本 ADR supersedes `202605102000-adr-lifecycle-hook-ctx-semantics.md` 的以下段落：

- **§D1**（"维持 context.WithCancel(context.Background()) 作为 worker ctx 派生源"）→ RETRACTED，见 §D1 重写
- **§D3**（"长期改造留 backlog LIFECYCLE-OWNER-CTX-PROPAGATION-01"）→ RESOLVED，本 PR 落地
- **§Consequences**（"如未来改 Stop 调用策略…需同步审查本 ADR §D1 依赖前提"）→ 依赖前提已变更，见 §D2 重写

见该 ADR 顶部的 superseded-by 指针。
