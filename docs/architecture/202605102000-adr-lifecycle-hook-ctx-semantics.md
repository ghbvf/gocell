# ADR: cell.LifecycleHook.OnStart ctx 语义 — owner ctx（controller-runtime 范式）

> Status: Partially Superseded
> Superseded-by: `202605170000-adr-control-plane-business-plane-decouple.md`（§D1 RETRACTED, §D3 RESOLVED, §Consequences 依赖前提段重写）
> Date: 2026-05-10
> Updated: 2026-05-17（§D1 RETRACTED / §D3 RESOLVED / §Consequences 重写，见 supersede ADR）
> Implementation: PR #441 第二轮 review F3-C（runtime/command/lifecycle_rollback_test.go 集成测试）

## Context

`cell.LifecycleHook.OnStart(ctx context.Context) error` 当前由 bootstrap phase6 调用并传入 startup-budget ctx（来自 `LifecycleHook.StartTimeout` 字段）。`runtime/command.SweeperLifecycle.Start` 实现（PR #441 之前）：

```go
func (l *SweeperLifecycle) Start(_ context.Context) error {
    runCtx, cancel := context.WithCancel(context.Background())
    // ...
    go func() {
        l.Sweeper.Start(runCtx)
        // ...
    }()
    return nil
}
```

worker goroutine 用 `context.Background()` 派生的 runCtx 跑，**与 OnStart 传入的 ctx 完全脱钩**。

PR 441 第二轮 review 的运维席位将此标记为 P1：**启动失败/回滚时 worker 生命周期失配**。论据：controller-runtime `manager.Start(ctx)` 把调用方 ctx 贯穿所有 internal worker；Uber Fx OnStart 与回滚 OnStop 共享启动预算 ctx；GoCell 这条独立 background 派生违背业界主流。

激进自审：`cell.LifecycleHook.StartTimeout` 字段的存在表明 OnStart ctx 设计语义就是 **startup deadline**（短生命周期，启动完成立刻 cancel）。如果 worker 从 OnStart ctx 派生，OnStart 一返回 worker 就被 cancel —— 行为退化为"启动一次就死"，与 uber-go/fx hook 语义一致。所以 background 派生是**正确的**，与 `cell.LifecycleHook` 协议匹配。

但运维席位的另一层关切真实存在：bootstrap LIFO rollback 路径下，worker goroutine 的取消通道必须可靠。当前实现里 worker 仅由 `Stop()` 内的 `cancel()` 触发退出 —— 如果 bootstrap 在 LIFO 反向遍历时漏调 OnStop（理论上不会，但 silent 假设），worker 永远活。

## Decision

### D1. ~~维持 `context.WithCancel(context.Background())` 作为 worker ctx 的派生源~~ **[RETRACTED]**

> **RETRACTED**（2026-05-17，superseded by `202605170000-adr-control-plane-business-plane-decouple.md` §D-B）
>
> 原 D1 决定"维持 Background() 派生"已被推翻。实际落地如下：
>
> `cell.LifecycleHook.OnStart(ctx)` 的 ctx 语义重定义为**长生命 owner ctx**，对齐
> controller-runtime `Runnable.Start(managerCtx)` 范式。bootstrap 在 `Run()` 内从
> `runCtx`（assembly 运行期 ctx）派生 `ownerCtx`，并传入 `lifecycle.Start(ownerCtx)`；
> `runHook(isStart=true)` 直接将 ownerCtx 透传给 `OnStart`，不再 `applyTimeout(StartTimeout)` 包裹。
>
> 原 D1 中保留 Background() 派生的三点理由（协议一致性、零实测泄漏、不引入预设需求）
> 均已因 backlog `LIFECYCLE-OWNER-CTX-PROPAGATION-01` 触发条件成立而失效：
> `LIFECYCLE-CLOCK-CONTROL-PLANE-DECOUPLE-01`（C.1 startup-probe deadlock）与
> `LIFECYCLE-OWNER-CTX-PROPAGATION-01`（C.2 worker ctx 脱钩）共享同一根因，
> 同束实施。
>
> **StartTimeout 语义降级**：字段保留，但仅作 hook 自身探针窗口预算（informational），
> runner 不再将其强制为 OnStart ctx deadline。

<details>
<summary>原 D1 文案（保留历史参考）</summary>

不改 `runtime/command/lifecycle.go::Start` 现状。理由：

1. **协议一致性**：`cell.LifecycleHook.StartTimeout` 字段 + `OnStart(ctx) error` 的语义就是 startup deadline，让 worker 跟着 cancel 是退化。对齐 uber-go/fx hook 范式（OnStart ctx 仅用于"启动期超时"）。
2. **零实测泄漏**：bootstrap LIFO rollback 路径下，已 Start 的 cell 的 OnStop 会被反向遍历调用 —— 这是 `runtime/bootstrap` 的强契约，与 lifecycle 协议无关。
3. **不引入预设需求**：现有 GoCell 没有"主 lifecycle ctx 贯穿"机制。引入会冲击 `cell.LifecycleHook` 协议（增加 OwnerCtx 字段或 SweeperLifecycle 构造期注入），影响所有 lifecycle hook 实现，scope 远超本 PR 的 review 修复范围。

</details>

### D2. 加 `TestSweeperLifecycle_StartupFailRollback` 集成测试钉死 rollback 契约

在 `runtime/command/lifecycle_rollback_test.go` 模拟 bootstrap LIFO rollback 路径：OnStart → OnStop（启动后立即触发回滚）→ goleak 验证 worker goroutine 干净退出。

**新语义下重验通过**（2026-05-17）：`TestSweeperLifecycle_StartupFailRollback` 在 owner ctx 语义下仍通过 —— rollback LIFO 正确执行，goleak 无 goroutine 泄漏。worker 现在额外响应 ownerCancel，使得 rollback 更可靠（两条取消路径：ownerCancel + OnStop cancel）。

如果未来 refactor 让 worker 的取消通道脱离 OnStop（或漏调 OnStop），该测试仍会 fail。这是 D2"零实测泄漏"论据的反向防护。

### D3. ~~长期改造留 backlog `LIFECYCLE-OWNER-CTX-PROPAGATION-01`~~ **[RESOLVED]**

> **RESOLVED**（2026-05-17，本 PR #212 落地）
>
> backlog `LIFECYCLE-OWNER-CTX-PROPAGATION-01` 已在本 PR 核销：
> - `cell.LifecycleHook.OnStart(ctx)` 语义已重定义为 owner ctx（见 D1 RETRACTED 重写）
> - `runtime/command.SweeperLifecycle.Start(ownerCtx)` 从 ownerCtx 派生 runCtx
> - `cells/accesscore/refresh_gc.go` OnStart 透传 ownerCtx 给 `gc_worker.Start(ctx)`
> - `runtime/auth/refresh/gc_worker.go` 删除 `context.WithoutCancel` 自 re-root
>
> 详见 `202605170000-adr-control-plane-business-plane-decouple.md` §D-B。

<details>
<summary>原 D3 文案（保留历史参考）</summary>

owner-ctx 贯穿（controller-runtime 范式）作为 backlog item 登记，**触发条件**：

- (a) 出现 worker 在 OnStop 之外需响应主 lifecycle ctx cancel 的场景（例如 cell 间相互依赖且需要全局停机协调）；或
- (b) 复杂多 cell rollback 实测出现生命周期不一致事故（D2 测试在事故发生前已可见）

触发时按以下方向改造（不在本 ADR 定夺）：

- `cell.LifecycleHook` 增 `OwnerCtx context.Context` 字段（or `SweeperLifecycle` 构造期注入），bootstrap 从主 lifecycle ctx 派生 hook OwnerCtx
- worker goroutine 用 `OwnerCtx` 派生，OnStop cancel 仍存在但作 fast-path 关停
- 主 lifecycle ctx cancel → 所有 worker 自动停（不依赖 OnStop 链路完整性）

参考：
- kubernetes-sigs/controller-runtime `manager.Start(ctx)` 主 ctx 贯穿所有 internal worker
- uber-go/fx `app.go` Lifecycle.Append OnStart/OnStop 共享启动预算 ctx，但 runner ctx 独立（mixed pattern）
- temporal/sdk-go `internal_worker.go` 启动期用 background（反例，对照说明 GoCell 现状不是孤例）

</details>

## Consequences

正面（更新后语义）：
- OnStart ctx = owner ctx（long-lived）：hook 可响应 assembly 关停 ∪ OnStop，两条取消路径都可用
- ownerCancel 先于 lifecycle.Stop 执行，worker goroutine 在 OnStop 调用前已退出，OnStop channel-wait 立即返回
- 集成测试 `TestSweeperLifecycle_StartupFailRollback` 钉死 rollback 契约，新语义下重验通过

负面/取舍（更新后语义）：
- OnStart 不再有 runner 强制的 StartTimeout deadline；hook 必须自带快速探针（如 SweeperLifecycle 的 50 ms `controlPlaneProbeTimer`）
- StartTimeout 降为 informational（slow-start warning threshold）；若 hook 自己不实现探针就直接阻塞，会无限期占用 lifecycle.Start
- `_ context.Context` 形式的 OnStart ctx 忽略已被消除 —— 但 hook 实现者必须理解 ctx 是 owner ctx（不是用于派生超时上下文）

~~bootstrap LIFO rollback 完整性是隐式契约（runtime/bootstrap 单独保证），lifecycle hook 协议层不可见~~（已更新：ownerCancel 提供独立的协议层可见取消通道）

~~`runtime/bootstrap` 的 LIFO 完整性由 `runtime/bootstrap` 包内 phase 编排逻辑保证；如未来改 Stop 调用策略（例如并行 Stop 跳过某些 cell），需同步审查本 ADR §D1 依赖前提是否仍成立。~~（已更新：本依赖前提已消除。OnStop 调用策略变更不影响 ownerCancel → worker 退出路径的正确性。OnStop 仅负责有序排空，不再是唯一取消通道。）

## ref

- `runtime/command/lifecycle.go::SweeperLifecycle.Start` 实现（新语义）
- `runtime/bootstrap/lifecycle.go::runHook` — OnStart 分支直接传 ownerCtx，无 applyTimeout 包裹
- `runtime/bootstrap/bootstrap.go` — `ownerCtx, ownerCancel = WithCancel(runCtx)`
- `runtime/command/lifecycle_rollback_test.go` 集成测试（新语义下重验通过）
- supersede ADR `202605170000-adr-control-plane-business-plane-decouple.md`
