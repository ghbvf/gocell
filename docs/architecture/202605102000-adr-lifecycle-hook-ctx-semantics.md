# ADR: cell.LifecycleHook.OnStart ctx 语义 — startup-deadline，worker 用 background 派生

> Status: Accepted
> Date: 2026-05-10
> Defers to: backlog `LIFECYCLE-OWNER-CTX-PROPAGATION-01`（owner-ctx 贯穿改造，按 controller-runtime / Uber Fx 范式）
> Implementation: PR #441 第二轮 review F3-C（runtime/command/lifecycle_rollback_test.go 集成测试）

## Context

`cell.LifecycleHook.OnStart(ctx context.Context) error` 当前由 bootstrap phase6 调用并传入 startup-budget ctx（来自 `LifecycleHook.StartTimeout` 字段）。`runtime/command.SweeperLifecycle.Start` 实现：

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

### D1. 维持 `context.WithCancel(context.Background())` 作为 worker ctx 的派生源

不改 `runtime/command/lifecycle.go::Start` 现状。理由：

1. **协议一致性**：`cell.LifecycleHook.StartTimeout` 字段 + `OnStart(ctx) error` 的语义就是 startup deadline，让 worker 跟着 cancel 是退化。对齐 uber-go/fx hook 范式（OnStart ctx 仅用于"启动期超时"）。
2. **零实测泄漏**：bootstrap LIFO rollback 路径下，已 Start 的 cell 的 OnStop 会被反向遍历调用 —— 这是 `runtime/bootstrap` 的强契约，与 lifecycle 协议无关。
3. **不引入预设需求**：现有 GoCell 没有"主 lifecycle ctx 贯穿"机制。引入会冲击 `cell.LifecycleHook` 协议（增加 OwnerCtx 字段或 SweeperLifecycle 构造期注入），影响所有 lifecycle hook 实现，scope 远超本 PR 的 review 修复范围。

### D2. 加 `TestSweeperLifecycle_StartupFailRollback` 集成测试钉死 rollback 契约

在 `runtime/command/lifecycle_rollback_test.go` 模拟 bootstrap LIFO rollback 路径：OnStart → OnStop（启动后立即触发回滚）→ goleak 验证 worker goroutine 干净退出。

如果未来 refactor 让 worker 的取消通道脱离 OnStop（或漏调 OnStop），该测试 fail。这是 D1"零实测泄漏"论据的反向防护 —— 不是"防御未来变更"的过度设计，而是把 D1 的依赖前提（OnStop 必被调用 + worker 必响应 OnStop cancel）显式锁住。

### D3. 长期改造留 backlog `LIFECYCLE-OWNER-CTX-PROPAGATION-01`

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

## Consequences

正面：
- 协议语义清晰：OnStart ctx = startup deadline；worker ctx = independent，OnStop 取消
- 零代码变更（仅加测试 + ADR）；本 PR scope 不扩张
- 集成测试钉死 rollback 契约，未来 refactor 任何改动 OnStop 取消路径必经此 gate

负面 / 取舍：
- 不与 controller-runtime 主流贯穿模式对齐（backlog 跟踪触发条件，不强行升级）
- bootstrap LIFO rollback 完整性是隐式契约（runtime/bootstrap 单独保证），lifecycle hook 协议层不可见
- `_ context.Context` 形式的 OnStart ctx 忽略让 reviewer 一眼看不到"为什么忽略"——已在 lifecycle.go godoc 引用本 ADR 解释决策

## ref

- `runtime/command/lifecycle.go::SweeperLifecycle.Start` 实现
- `runtime/command/lifecycle_rollback_test.go` 集成测试
- backlog `LIFECYCLE-OWNER-CTX-PROPAGATION-01`（待触发条件）
