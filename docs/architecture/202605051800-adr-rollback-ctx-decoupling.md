# ADR — kernel/assembly rollback ctx decoupling (PR-V1-030-G02)

- **Date**: 2026-05-05
- **Status**: Accepted (grace formula portion superseded by [202605101730-adr-shutdown-budget-decouple.md](202605101730-adr-shutdown-budget-decouple.md))
- **Refs**: `kernel/assembly/assembly.go`, `runtime/bootstrap/phases_assembly.go`, `docs/ops/graceful-shutdown-k8s.md`

> **Note**: 本 ADR 的"部署配套"章节中给出的 grace 公式 `>= shutdownTimeout + 10s`（行 52）已被 [`202605101730-adr-shutdown-budget-decouple.md`](202605101730-adr-shutdown-budget-decouple.md) supersede——phase10 拆双 budget 后正确公式为 `>= 2 × shutdownTimeout + 10s`。本 ADR 的 rollback ctx 解耦决策本身仍生效。

## 问题

`CoreAssembly.Start(ctx)` 失败时回滚已启动 cell。原实现把同一个 `ctx` 透传给 `rollbackCells(ctx, upTo) → stopCellWithHooks(ctx, c)`：

- `c.Stop(ctx)` 直接使用 ctx（不经 `invokeHook` 包装）
- `BeforeStop` / `AfterStop` 经 `invokeHook` 包装，但 `WithTimeout(ctx, HookTimeout)` 在已 done 父 ctx 上派生的子 ctx 也立即 done

后果：当 SIGTERM 在 Start 期间触发并 cancel 调用方 ctx，rollback 看到的是已 cancel ctx，所有 Stop hook 立即返回。已启动 cell 未被释放，资源/连接泄漏。

## 决议

`rollbackCells` 不再接受 ctx 参数；rollback 根 ctx 由内部 `newRollbackCtx()` 派生：

```
HookTimeout > 0    → context.WithTimeout(context.Background(), HookTimeout)
HookTimeout == 0   → context.WithTimeout(context.Background(), DefaultHookTimeout)
HookTimeout  < 0   → context.WithCancel(context.Background())   // no deadline
```

负值语义与 `invokeHook` 现有行为一致——HookTimeout < 0 表示"禁用 deadline 包装"。

`startCellWithHooks` 中三处 caller（BeforeStart 失败 / Start 失败 / AfterStart 失败）改为调用零参 `rollbackCells(i-1)`。AfterStart 失败路径还需先 stop 当前 cell（其 Start 已成功），同样使用 fresh rollback ctx：`stopCellWithHooks(rollbackCtx, c)`。

## 与 uber-go/fx 的差异

`fx.App.withRollback`（`app.go:483-499`）把 startCtx 复用给 `lifecycle.Stop`。fx issue #751 已记录这是 known limitation——startCtx 已超时或被 cancel 时 Stop hook 全跳过。fx 正常 shutdown 路径（`run()` 内部）从 `context.Background()` 派生 stopCtx，但 rollback 路径未对齐这个模式。

GoCell 选择"正常 shutdown 派生模式"作为 rollback 的统一行为，与 fx 的 rollback 缺陷分歧。

ref: uber-go/fx app.go withRollback / run

## 共享预算 vs per-cell 预算

`HookTimeout > 0` 时 rollback 的所有 cell 共享一个 `WithTimeout(Background, HookTimeout)` ctx。`invokeHook` 内部仍按 per-hook `HookTimeout` 包装，与共享预算取较小者。

替代方案"per-cell 全额 HookTimeout"被驳回：总 wallclock 上界 = `HookTimeout × N`，与 K8s `terminationGracePeriodSeconds` 边界耦合度差，运维直觉受损。共享预算让单次 rollback 的最大耗时与 `HookTimeout` 直接对应。

## API 影响

- `func (a *CoreAssembly) rollbackCells(ctx context.Context, upTo int)` → `func (a *CoreAssembly) rollbackCells(upTo int)`（unexported，包内 3 处 caller 同步改）。AfterStart-fail 分支调用 `rollbackCells(i)`（注意是 `i` 而非 `i-1`），把刚 AfterStart 失败的 cell 自身一并塞进 LIFO 序列，所有 cell 共用一个 rollback ctx——这是"单一 HookTimeout 预算"语义的关键，避免失败 cell 与历史已启动 cell 各自获得独立预算导致总 wallclock 翻倍。
- 新增 unexported `func (a *CoreAssembly) newRollbackCtx() (context.Context, context.CancelFunc)`
- 无导出 API 变更，无 deprecation 别名

## 部署配套

phase0 增 `WithTerminationGracePeriod(d time.Duration)` option（advisory only，不改运行时行为）；当声明值小于 `shutdownTimeout + 10s` 时 phase0 emit `slog.Warn`。**注意**：roadmap N3 原文给的是 `>= shutdownTimeout + preShutdownDelay + 10s`，但 `phase10OrchestrateShutdown` 把 `preShutdownDelay` 嵌在同一个 `shutCtx`（受 `shutdownTimeout` 总预算约束）内消耗，preShutdownDelay 不应再叠加 — 实施时按数学正确公式落地，详见 `docs/ops/graceful-shutdown-k8s.md`。
