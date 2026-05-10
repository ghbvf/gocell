# ADR — phase10 shutdown budget decouple (PR405-BOOTSTRAP-SHUTDOWN-BUDGET-DECOUPLE)

- **Date**: 2026-05-10
- **Status**: Accepted
- **Refs**: `runtime/bootstrap/phases_shutdown.go`, `runtime/bootstrap/phases_assembly.go`, `runtime/bootstrap/options_lifecycle.go`, `docs/ops/graceful-shutdown-k8s.md`, `docs/architecture/202605051800-adr-rollback-ctx-decoupling.md`

## 问题

`phase10OrchestrateShutdown`（`runtime/bootstrap/phases_shutdown.go`）原实现用单一 `shutCtx = context.WithTimeout(Background, b.shutdownTimeout)` 覆盖所有四个 stage：

```
stage1 readiness flip → stage2 HTTP drain → stage3 LIFO teardown → stage4 finalize
```

stage 之间共享同一个 deadline，HTTP drain 慢就会吃掉 LIFO teardown 的预算 —— workers / event router / assembly / kernel lifecycle / closers / managed resources 拿到的全是已 cancel 的 ctx，全部立即返回 `context.DeadlineExceeded`，资源/连接泄漏。

具体可观测症状（`-race` CI 偶发）：

```
TestPhase7ServeAll_DualListener_NoCloseRace
  teardown[teardown_http_drain]: listener "primary" shutdown: context deadline exceeded
```

PR405 reviewer 在评审 `4ae9e21e fix(test): bootstrap dual_listener no-close-race shutdown budget D2s→D5s` 时识别此架构问题，登记 backlog `PR405-BOOTSTRAP-SHUTDOWN-BUDGET-DECOUPLE`。后续 D5s→D10s 涨预算（`dual_listener_test.go:39-45` 注释）只是治标。

## 决议

phase10 把单一 shutCtx 拆成两个独立的 budget bucket：

| Bucket | 覆盖阶段 | 预算 |
|---|---|---|
| **drainCtx** | stage 1 readiness flip + `preShutdownDelay` + stage 2 HTTP drain | `b.shutdownTimeout` |
| **tearCtx** | stage 3 LIFO teardown | `b.shutdownTimeout` |

```go
drainCtx, drainCancel := context.WithTimeout(context.Background(), b.shutdownTimeout)
defer drainCancel()
b.phase10ReadinessFlip(drainCtx, s)
httpDrainErr := s.httpDrain(drainCtx)

tearCtx, tearCancel := context.WithTimeout(context.Background(), b.shutdownTimeout)
defer tearCancel()
teardownErrs := b.phase10LIFOTeardown(tearCtx, s)

switch {
case drainCtx.Err() != nil || tearCtx.Err() != nil:
    outcome = "timeout"
// ...
}
```

两个 ctx 互不吞噬：HTTP drain 即使吃满 drainCtx，LIFO teardown 仍拿到完整 fresh tearCtx。stage 顺序不变（drain 仍在 LIFO 前显式跑）。

## 与对标框架对照

- **kubernetes/kubernetes apiserver/pkg/server/genericapiserver.go `RunWithContext`** — 用多个独立 channel signal（`NotAcceptingNewRequest` / `InFlightRequestsDrained` / `stopHttpServerCtx` / `listenerStoppedCh`）显式分离 drain 与 listener stop，每个 signal 自带独立 deadline。GoCell drainCtx/tearCtx 是这一思想的简化形式。
- **sigs.k8s.io/controller-runtime pkg/manager/internal.go `engageStopProcedure`** — 使用一个 `gracefulShutdownTimeout` 覆盖整个 LIFO，但前置的 webhook/HTTP server stop 在外层 `Start()` errgroup 内并发完成（与 LIFO 解耦）。
- **uber-go/fx app.go `Stop`** — 单一 `StopTimeout`，hook 顺序消耗预算。fx issue #751 已知 limitation 与本 ADR 修的是同源问题（前段拖累后段）；GoCell 不沿用 fx 的单一预算模式。

## API 影响

- `phase10OrchestrateShutdown` 内部改造，**无导出 API 变更**
- `WithShutdownTimeout` godoc 重写：语义从「整体预算」→「每 stage 独立预算」
- `WithPreShutdownDelay` godoc 重写：明确「消耗于 drainCtx 内部，不影响 tearCtx」
- `WithTerminationGracePeriod` godoc + `warnTerminationGracePeriodInsufficient` 公式更新：
  ```
  minRequired = 2 × shutdownTimeout + terminationGraceSafetyMargin (10s)
  ```
  这是本 ADR 的运维侧后果——pod spec `terminationGracePeriodSeconds` 必须按此公式配置。

## Supersedes / 兼容性

- 本 ADR **supersede** `docs/architecture/202605051800-adr-rollback-ctx-decoupling.md` 行 52 中的 grace 公式 `>= shutdownTimeout + 10s`（仅公式部分；rollback ctx 解耦决策本身仍生效）
- `docs/ops/graceful-shutdown-k8s.md` 公式同步更新为 `>= 2 × shutdownTimeout + 10s`
- 不留 deprecation 别名 / 旧路径 / 双 ctx fallback；旧公式已在文档中删除，新公式为唯一表达
- `cmd/*` / pod manifest 默认值需要按新公式 review：默认 `shutdownTimeout = 30s`（`runtime/shutdown.DefaultTimeout`），新最低门槛 `2*30 + 10 = 70s`。`bootstrap.WithTerminationGracePeriod` 在不达标时 emit `slog.Warn`（advisory only，不阻断启动）— 现有 < 70s 的 manifest 启动时会触发 warn 但仍能跑，需要在下次部署窗口对齐

## 测试与验证

- 新增 `TestPhase10_BudgetIsolation_LIFOTeardownGetsFreshCtx`（`runtime/bootstrap/shutdown_ordering_test.go`）— runtime invariant guard，AI-rebust **Medium**。注入 httpDrain 阻塞到 ctx.Done，断言 LIFO teardown ctx 在入口仍未 done。
- 既有 `TestPhase10_HTTPDrainsBeforeLIFO_*` / `TestPhase10ShutdownStageOrder` / `TestPhase10_HTTPDrainError_AggregatedAndWrapped` 全部继续 GREEN（顺序契约不变）。
- `TestPhase0_TerminationGracePeriodWarn` 阈值同步更新（`graceMinThreshold = 2*graceShutdownTimeout + terminationGraceSafetyMargin`，单源派生）。

## 与 NoCloseRace flake 的关系

测试 flake `TestPhase7ServeAll_DualListener_NoCloseRace` 的根因是 **stdlib `http.Server.Shutdown` 内部 polling close idle conns 在 -race + GH runner contention 下偶发被 keep-alive 的 idle socket 拖累**——HTTP drain 自身 ≥ shutdownTimeout 才超时，与 LIFO teardown 无直接关系。

本 ADR 的 budget 拆分**不直接修复**该测试 flake；测试 flake 由配套改动 `noCloseRaceHTTPClient`（`Transport.DisableKeepAlives=true`）从根因上消除（停止 polling 慢路径）。

但 budget 拆分本身是独立 backlog 项（`PR405-BOOTSTRAP-SHUTDOWN-BUDGET-DECOUPLE`），同 PR 一起做避免重复访问 phase10 代码路径，且产品代码改动与测试改动各自独立 commit、容易回顾。
