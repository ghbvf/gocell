# ADR: 控制面/业务面解耦 — 时钟二分、Owner Ctx、Sweep 错误可观测

> Status: Accepted (Amended)
> Date: 2026-05-17
> Implementation: PR #212 (worktree 212-control-plane-decouple)
>   Commits: 087ca782a / a44197f19 / 85165f557 / fc289fed2
> Amended: 2026-05-17 — PR #531 second-round review remediation
>   (P1-1/P1-2/P1-3/P1-4/P2-1/P2-2/P2-3). See §Amendment A1; §D-A/§D-B and
>   the 威胁分析 matrix are rewritten in place (no dual truth source).
> Backlog: `BOOTSTRAP-CONTROL-PLANE-DECOUPLE-BUNDLE` (cap-01)
> Source: PR #441 second/third-round review 聚合，3 子条 C.1/C.2/C.3 同根因
> Supersedes: `202605102000-adr-lifecycle-hook-ctx-semantics.md` §D1/§D3/§Consequences

---

## Amendment A1 (PR #531 second-round review)

PR #212 shipped the C.1/C.2/C.3 decouple; a second-round review found that the
§D-B "OnStart = owner ctx, no startup deadline" contract, while correct as a
single-truth model, had no deadlock backstop and several adjacent gaps. This
amendment closes them. **The original §D-A/§D-B prose and the 威胁分析 table
below are rewritten in place** — per ai-collab.md「ADR amendment 落地必查」, no
"historical context" dual truth source is retained.

Deltas (each pinned by a regression test):

- **A1-1 (review P1-1) — startup deadlock backstop**: `Bootstrap.Run` now
  supervises `lifecycle.Start(ownerCtx)` in a goroutine, aborting on caller-ctx
  cancel OR a `WithStartupTimeout` budget (default 30s). On abort it
  `ownerCancel()`s then rolls back. The owner-ctx single-truth (§D-B) is
  unchanged — the bound lives at the orchestration layer (mirrors
  controller-runtime `mgr.Start`, unblocked by its caller ctx), NOT as a
  per-hook deadline. `StartTimeout` remains informational (slow-start warning
  only). New sentinel `bootstrap.ErrBootstrapStartupTimeout` (reuses
  `ErrBootstrapLifecycle` — no new errcode).
- **A1-2 (review P1-2) — failed-hook teardown ordering**: `lifecycle.Start`
  hands hooks a `workCtx` (child of ownerCtx) and cancels it BEFORE the
  internal LIFO rollback, so a failed hook's own already-spawned goroutine is
  torn down before any sibling's OnStop. OnStop still uses the original ctx
  (live during drain). Still one ctx per hook (§D-B preserved).
- **A1-3 (review P2-2) — business-plane now**: `SweeperLifecycle` holds a
  `BusinessClock` used ONLY for the `SweepTick(now)` argument; `now` no longer
  comes from the real-time ticker's tick value (which mismatched fake-clock
  assemblies). Control-plane ticker/probe stay real-time; `kernel/command.Sweeper`
  still holds NO clock field (the §D-A Hard invariant is unchanged).
  `NewSweeperLifecycle` re-gains a `businessClock clock.Clock` parameter (NOT a
  control-plane scheduling clock — see rewritten §D-A).
- **A1-4 (review P2-1) — readiness gate**: `kernel/command.Sweeper.Validate()`
  (no side effects) is invoked at `SweeperLifecycle` OnStart; a zero-value
  `&command.Sweeper{}` now fails startup (bootstrap rolls back) instead of
  starting and erroring on every swallowed tick.
- **A1-5 (review P1-4) — counter crash-safety**: `SweeperLifecycle` OnStart
  preflights `SweepErrorCounter.With({"cell":…})` under recover, converting a
  label-set mismatch (which would panic+crashloop the loop) into a fail-fast
  wiring error.
- **A1-6 (review P1-3) — carve-out anchoring**: the `PROD-CLOCK-INJECTION-01`
  control-plane carve-out is now gated by an explicit
  `controlPlaneClockCarveOut` `{rel → func names}` allowlist in addition to the
  marker. The marker alone no longer self-exempts any function in any file
  (was effectively Soft → now Medium, properly anchored). RED self-checks:
  `control_plane_marker_wrong_path_violates`,
  `control_plane_marker_wrong_func_violates`. Hard upgrade path unchanged
  (backlog `CONTROL-PLANE-CLOCK-TYPED-FUNNEL-HARD-UPGRADE-01`).
- **A1-7 (review P2-3) — godoc sync**: `WithLifecycleDefaultStartTimeout` /
  `DefaultStartTimeout` / `Hook.OnStart` godoc rewritten to state StartTimeout
  is informational; the backstop is `WithStartupTimeout` + caller ctx.

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

**runtime 层（C.1 Medium carve-out）**：`runtime/command.SweeperLifecycle`
持有 `BusinessClock clock.Clock` 字段，**仅供 `SweepTick(ctx, now)` 的 `now`
实参**（业务面过期判定时间）使用，构造期 `clock.MustHaveClock` 校验非 nil。它
**不是控制面 scheduling 时钟**：ticker 与 50 ms 启动探针的时间源仍由下方两个
私有函数（真实 wall-clock）收敛，与 `BusinessClock` 完全正交（A1-3 / review
P2-2）。修复前 `now` 取自实时 ticker.C 的 tick 值，与 cell 注入时钟（可能是
fake）产生的命令创建时间错配，污染 fake-clock assembly 的过期决策。
`kernel/command.Sweeper` 仍无任何时钟字段（本节 Hard 不变量不受影响）。

控制面时间源（ticker + 50 ms 启动探针）由两个私有函数收敛：

```go
//archtest:allow:clock-injection:control-plane startup-deadlock-regression-C1 (CONTROL-PLANE-CLOCK-TYPED-FUNNEL-HARD-UPGRADE-01)
func controlPlaneTicker(interval time.Duration) *time.Ticker {
    return time.NewTicker(interval)
}

//archtest:allow:clock-injection:control-plane startup-deadlock-regression-C1 (CONTROL-PLANE-CLOCK-TYPED-FUNNEL-HARD-UPGRADE-01)
func controlPlaneProbeTimer(d time.Duration) *time.Timer {
    return time.NewTimer(d)
}
```

这两个函数携带函数级 comment-guard `//archtest:allow:clock-injection:control-plane`，
是 `PROD-CLOCK-INJECTION-01` archtest 在 `runtime/command` 包内认可的唯一真实时钟
使用点（允许 allowMarker）。trailing `(CONTROL-PLANE-CLOCK-TYPED-FUNNEL-HARD-UPGRADE-01)`
是内联 backlog 引用，属于 marker 语法的一部分，非可选注释。

**AI-rebust 评级（双栏）**：

| 维度 | 评级 | 根据 |
|---|---|---|
| C.1 下游：kernel Sweeper 无时钟字段 | **Hard** | 类型不可表达 — 字段不存在，无论何种 AI 实现变体均无法合法引入 fake clock |
| C.1 runtime 侧：控制面真实时间 carve-out | **Medium**（A1-6 锚定后） | 函数级 comment-guard **+** archtest 内 `controlPlaneClockCarveOut` `{rel → 函数名}` allowlist 双重门：marker 单独不再豁免任意函数（修复前等效 Soft，review P1-3）。allowlist 外的 `runtime/command` 真实时钟调用、第三个加 marker 的函数、其他文件/包加 marker，均被 RED 反向 fixture 捕获 |

**carve-out 唯一真值（A1-6 修订）**：carve-out 的权威登记处是 archtest
`tools/archtest/clock_invariants_test.go` 内的 `controlPlaneClockCarveOut`
allowlist（`{"runtime/command/lifecycle.go": {controlPlaneTicker,
controlPlaneProbeTimer}}`）**配合**函数自身的
`//archtest:allow:clock-injection:control-plane` marker——两者皆满足方豁免。
allowlist 与 marker 同属 enforcement 侧（archtest），不构成"代码 vs ADR"两份
真值源；**本 ADR 仍是文档，不是 enforcement 来源**。allowlist 是 archtest
内部数据（非业务 PR 一行注释可塞入），新增条目是显式可审查的 archtest 变更，
因此**不需要 `ERRCODE-CARVEOUT-ADR-CONSISTENCY-01` 式的双向 consistency
archtest**（不同于 `errcodeKindLiteralCarveOuts` 与 ADR registry 表的双真值源
场景）。

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
// A1-1: supervised — Start runs in a goroutine; the caller ctx + a
// WithStartupTimeout budget bound it so a hook whose OnStart never
// returns cannot wedge Run(). On abort: ownerCancel() then rollback.
if err := b.superviseLifecycleStart(ctx); err != nil {
    b.ownerCancel()
    return rollback(err)
}
```

**A1-1 启动死锁 backstop（review P1-1）**：ownerCtx 派生自
`context.Background()` 的 runCtx，且 §D-B 取消了 OnStart 的 per-hook deadline；
若某 hook 的 OnStart 永不返回，`lifecycle.Start` 会永久阻塞，调用方取消也进
不了 phase9/phase10——Run() 死锁。修复：`superviseLifecycleStart` 把
`lifecycle.Start(ownerCtx)` 放 goroutine，`select` 于 {start 完成, 调用方
ctx.Done(), `WithStartupTimeout` 预算 timer}；后两者触发即 `ownerCancel()`
（解除 wedged OnStart）→ 等待 Start unwind → 返回（预算路径返回
`ErrBootstrapStartupTimeout`，复用 `ErrBootstrapLifecycle`，不新增 errcode）。
**owner-ctx 单一真值不变**：hook 仍只见一个 ctx；bound 在编排层，对齐
controller-runtime `mgr.Start`（由其 caller ctx 解阻）。`StartTimeout` 仍仅
informational（slow-start 警告阈值），**不是** deadlock 防线。

**A1-2 失败 hook 拆卸顺序（review P1-2）**：`lifecycle.Start` 给每个 hook 传
`workCtx`（ownerCtx 的子 ctx），在内部 LIFO rollback **之前**先
`workCancel()`，使失败 hook 自己已 spawn 的 goroutine 在任何 sibling 的
OnStop 运行前被取消。OnStop 仍用原始 ctx（drain 期间 live）。hook 仍只见一个
ctx，§D-B 单一真值不变。

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
一一对应。`runtime/bootstrap/lifecycle.go` 的 `runHook(isStart=true)` 分支传入
`workCtx`（ownerCtx 的子 ctx，A1-2；行为等同 owner ctx，额外仅在 start 失败时
随 `workCancel` 取消），不再 `applyTimeout(StartTimeout)` 包裹。

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
nil 则跳过 counter）。`SweeperLifecycle.CellID` 字段由 composition root 注入（如
`lc.CellID = c.ID()`），默认值为 `"_runtime"` sentinel（空时回退）。counter label
`{"cell": CellID}` 与 slog.Error `slog.String("cell", CellID)` 同源，对齐
`observability.md` 的 `cell` label 约定。`WithSweepErrorCounter(cv)` Option 暴露给
DeviceCell composition root 用于生产注入。

---

## Consequences

### 行为变化（breaking）

- `kernel/command.NewSweeper` 签名变更：删除 `clk clock.Clock` 入参、`WithSweeperInterval`、
  `WithSweeperOnError`。所有调用方必须迁移到新签名（唯一消费方：`examples/iotdevice/cells/devicecell/cell.go`，已同 PR 更新）。
- `runtime/command.NewSweeperLifecycle` 删除控制面 `clk` 入参，但（A1-3 / review
  P2-2）**新增 `businessClock clock.Clock` 末位入参**——仅作 `SweepTick` 的业务
  `now` 源，**非控制面 scheduling 时钟**。最终签名
  `NewSweeperLifecycle(name string, sweeper SweepTicker, interval time.Duration, businessClock clock.Clock)`。
  唯一消费方 `examples/iotdevice/cells/devicecell/cell.go` 传 `c.clk`，已同 PR 更新。
- `SweepTicker` 接口替代旧 `SweeperRunner`（`Start/Stop` 风格）。`*kcommand.Sweeper`
  另实现 `Validate() error`（A1-4 readiness gate，`SweeperLifecycle` OnStart 调用）。
- `cell.LifecycleHook.OnStart(ctx)` 语义重定义：ctx 现为 long-lived owner ctx，
  **不**是 startup deadline ctx。`lifecycle.Start` 的 deadlock backstop 由
  `bootstrap` 编排层 `superviseLifecycleStart`（caller ctx + `WithStartupTimeout`，
  A1-1）提供，非 per-hook deadline。
- 新增 `bootstrap.WithStartupTimeout(d)` Option + `bootstrap.ErrBootstrapStartupTimeout`
  sentinel（A1-1；sentinel 复用 `ErrBootstrapLifecycle` errcode，未新增 Kind/Sentinel）。

### owner-cancel 级联效果

ownerCancel 先于 lifecycle.Stop 运行后，所有通过 `context.WithCancel(ownerCtx)` 派生的
worker goroutine（sweeper loop、refresh GC loop）在 `lifecycle.Stop` 执行前已收到
取消信号并自行退出。`lifecycle.Stop` 的 `OnStop` 调用仍执行（确认退出、释放资源），
但 worker 通常已退出，OnStop 的 channel `<-done` 可立即返回。

### 威胁分析

> 逐行重评（ai-collab.md「ADR amendment 落地必查」）：原表无格子从 ✅ 退化为
> ⚠️/❌；原"StartTimeout 强制 OnStart 超时 = 语义变更已知（⚠️ 残留）"一行的
> 残留风险由 A1-1 补偿后转为「消除」。下方新增 A1 引入/暴露的威胁行。

| 威胁 | 状态 | 说明 |
|---|---|---|
| fake clock 注入控制面 scheduling | 消除 | kernel Sweeper 无时钟字段（Hard，A1-3 后仍无）；runtime 层控制面 ticker/probe carve-out 现由 marker + `controlPlaneClockCarveOut` allowlist 双重锚定（Medium，A1-6）。`BusinessClock` 仅供业务 `now`，不驱动 scheduling |
| 启动探针 frozen-clock deadlock | 消除 | controlPlaneProbeTimer 使用真实时间（A1-3 未改控制面时间源）|
| worker goroutine 在 assembly 关停后仍存活 | 消除 | ownerCancel 先行，worker 响应 ownerCtx.Done() |
| sweep 错误静默 | 消除 | SweepTick 返回聚合错误；runLoop slog.Error + counter |
| OnStop 链路中断导致 worker 永活 | 缓解 | ownerCancel 提供独立取消通道；OnStop 仅为有序排空 |
| OnStart 永不返回 wedge `Run()` | 缓解（A1-1） | `superviseLifecycleStart`：caller ctx cancel 或 `WithStartupTimeout` 预算耗尽触发 `ownerCancel()`，解除 wedged OnStart **的前提是该 hook 的 OnStart 观察到 workCtx/ownerCtx 取消**；`superviseLifecycleStart` 随后阻塞于 `<-startErr`，等待 `lifecycle.Start` unwind 完成后返回，再走 rollback 路径，Run() 得以继续。若某 hook 完全忽略 ctx、无条件永久阻塞，则 `lifecycle.Start` 和 `superviseLifecycleStart` 的 unwind 等待均不返回，Run() 仍然 wedge——该残留归属于违规 hook 自身 bug（hook 实现必须响应 ctx 取消）。goroutine 泄漏同归违规 hook 负责。原"Run 仍能返回"表述仅在 hook 响应 ctx 时成立，非无条件成立。|
| 失败 hook 的 goroutine 在 sibling rollback 期存活 | 消除（A1-2） | `lifecycle.Start` 在内部 LIFO rollback 前先 `workCancel()`；OnStop 用原始 ctx 仍 live |
| 控制面 real-now 当业务过期时间，fake-clock assembly 时间域错配 | 消除（A1-3） | `SweepTick` 的 `now` = `BusinessClock.Now()`，不再取自实时 ticker.C |
| 零值 `&command.Sweeper{}` 启动成功、首 tick 才静默错 | 消除（A1-4） | OnStart 调 `Sweeper.Validate()`（无副作用），失败即 fail-fast，bootstrap rollback |
| `SweepErrorCounter` label 不匹配 → `With` panic crashloop | 消除（A1-5） | OnStart 在 recover 下 preflight `.With({"cell":…})`，label 错配转 fail-fast wiring error |
| carve-out marker 被任意函数添加绕过 PROD-CLOCK-INJECTION-01 | 缓解（A1-6，Medium） | marker + `{rel,name}` allowlist 双门；marker 单独不豁免（修复前等效 Soft）。Hard 升级 backlog `CONTROL-PLANE-CLOCK-TYPED-FUNNEL-HARD-UPGRADE-01` 已登记 |
| StartTimeout 强制 OnStart 超时 | 消除（语义变更 + A1-1 backstop） | StartTimeout 仅 informational（slow-start 警告）；deadlock 防线移至编排层 supervise，非 per-hook deadline |

### ref

- kubernetes-sigs/controller-runtime@v0.18.4 `pkg/manager/internal.go`：`internalCtx=WithCancel(ctx)` → `Runnable.Start(internalCtx)`（owner ctx 贯穿所有 Runnable）
- uber-go/fx@v1.21.0 `app.go`：OnStart ctx 是 startup-timeout ctx，长期 worker 不可复用；GoCell 选择 controller-runtime 范式更彻底贯穿。
- kubernetes/utils@master `clock/clock.go`：`RealClock` 是控制面时钟，不可由业务注入替换。
- ADR `docs/architecture/202605021500-adr-kernel-clock-injection.md`：kernel 层时钟注入基线约定。
- ADR `docs/architecture/202605102000-adr-lifecycle-hook-ctx-semantics.md`：被本 ADR §D1/§D3 supersede（见 Supersedes 节）。

---

## Supersedes

本 ADR supersedes `202605102000-adr-lifecycle-hook-ctx-semantics.md` 的以下段落：

- **§D1**（"维持 context.WithCancel(context.Background()) 作为 worker ctx 派生源"）→ RETRACTED，见 §D1 重写
- **§D3**（"长期改造留 backlog LIFECYCLE-OWNER-CTX-PROPAGATION-01"）→ RESOLVED，本 PR 落地
- **§Consequences**（"如未来改 Stop 调用策略…需同步审查本 ADR §D1 依赖前提"）→ 依赖前提已变更，见 §D2 重写

见该 ADR 顶部的 superseded-by 指针。
