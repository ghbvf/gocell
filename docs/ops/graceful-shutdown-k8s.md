# Graceful Shutdown on Kubernetes

GoCell 的 graceful shutdown 由 phase10 的两个独立 budget bucket 承载（drainCtx + tearCtx），运维侧需要在 Pod spec `terminationGracePeriodSeconds` 中预留**至少** `2 × shutdownTimeout + 10s` 安全余量，否则 K8s SIGKILL 会截断进程内 shutdown。

## 预算公式

```
terminationGracePeriodSeconds  >=  2 × shutdownTimeout + 10s
```

- `shutdownTimeout` — `bootstrap.WithShutdownTimeout(d)`，默认 30s（`runtime/shutdown.DefaultTimeout`）。**每个 stage bucket 独立分配** 这个时长：
  - **drainCtx**（stage 1+2）— readiness flip + `preShutdownDelay` + HTTP drain
  - **tearCtx**（stage 3）— LIFO teardown（workers / event router / assembly / kernel lifecycle / closers / managed resources）

  两个 bucket 互不吞噬：HTTP drain 即使吃满 drainCtx，LIFO teardown 仍拿到完整的 tearCtx。最坏总耗时 ≈ `2 × shutdownTimeout + finalize 微小开销`。
- `10s` 安全余量 — 覆盖 SIGTERM → kubelet 上报 → 进程主循环响应、以及操作系统 / runtime 调度抖动

> `preShutdownDelay` 为何不在公式里：它消耗的是 drainCtx 内部预算（与 readiness flip / HTTP drain 共享 `shutdownTimeout`），不在 tearCtx 上额外叠加。一个长 `preShutdownDelay` 只会让 HTTP drain 的剩余预算变小，不会让总 shutdown 时长超过 `2 × shutdownTimeout`。

> 历史背景：早期版本所有 stage 共享单一 `shutCtx`（公式 `>= shutdownTimeout + 10s`），HTTP drain 慢就直接吃掉 LIFO teardown 的预算。ADR `docs/architecture/202605101730-adr-shutdown-budget-decouple.md` 解耦双 budget 后，公式跟随升级到 2×。

## 进程侧声明（advisory）

`bootstrap.WithTerminationGracePeriod(d)` 让 composition root 把 K8s manifest 的预期值告诉框架；phase0 用此值做一致性校验。下例使用 `shutdownTimeout = DefaultTimeout = 30s`：

```go
bootstrap.New(
    // bootstrap.WithShutdownTimeout(...) omitted — DefaultTimeout (30s) applies.
    bootstrap.WithPreShutdownDelay(5*time.Second),         // 嵌在 drainCtx (30s) 内
    bootstrap.WithTerminationGracePeriod(70*time.Second),  // >= 2*30 + 10 = 70
    // ...
)
```

不一致时 phase0 emit `slog.Warn`（**advisory only — 不阻断启动**）：

```
WARN bootstrap: terminationGracePeriodSeconds insufficient for graceful shutdown
  termination_grace_period=60s shutdown_timeout=30s pre_shutdown_delay=5s minimum_required=70s
  hint=increase Kubernetes pod terminationGracePeriodSeconds to >= 2*shutdownTimeout + 10s, ...
```

`pre_shutdown_delay` 字段仅作信息记录（运维排查用）；不参与 `minimum_required` 计算。

**`WithTerminationGracePeriod` 不改变运行时行为**——真实的 K8s grace window 仍然在 pod spec 里。这个 option 只是把"应当配多少"声明给 phase0：misalignment 在启动期作为 `slog.Warn` 大声告知（warn-loud, non-blocking），而不是等到某次 SIGTERM 被 SIGKILL 截断才暴露。需要"启动期硬阻断"语义请追踪 backlog 升级条目（暂未提供 strict 开关）。

## Pod spec 示例

`shutdownTimeout` 取默认 30s 时，K8s grace 最低 70s：

```yaml
apiVersion: apps/v1
kind: Deployment
spec:
  template:
    spec:
      terminationGracePeriodSeconds: 70   # >= 2*30 + 10  (DefaultTimeout)
      containers:
        - name: gocell
          # …
```

## Stage 时序与监控

phase10 emit 的 `bootstrap_shutdown_phase_duration_seconds`（histogram）按 stage label 分桶——`readiness_flip` / `http_drain` / `lifo_teardown` / `total` —— 直接观察 drainCtx vs tearCtx 各自吃了多少：

- `readiness_flip` + `http_drain` ≤ `shutdownTimeout`（drainCtx 一个 bucket）
- `lifo_teardown` ≤ `shutdownTimeout`（tearCtx 另一个 bucket）
- `total` ≈ 两段总和

`bootstrap_shutdown_outcome_total{outcome="timeout"}` 在任一 ctx 超时时增加；不区分哪个 bucket 超时（dashboards 简化）。具体哪段超时通过 `phaseError.Phase` 字段诊断（`teardown_http_drain` / `teardown_<component>`）。

## 相关文档

- `docs/ops/listener-topology.md` — HTTP listener / drain 顺序
- `docs/ops/startup-timeout.md` — 启动阶段超时排查（`bootstrap.ErrBootstrapStartupTimeout`，与 shutdown 预算正交）
- `docs/architecture/202605101730-adr-shutdown-budget-decouple.md` — phase10 双 budget 解耦决策
- `docs/architecture/202605051800-adr-rollback-ctx-decoupling.md` — 启动失败路径的 rollback ctx 派生（与 SIGTERM-during-Start 场景相关）
