# Graceful Shutdown on Kubernetes

GoCell 的 graceful shutdown 总耗时由 `shutdownTimeout` 上界，运维侧需要在 Pod spec `terminationGracePeriodSeconds` 中预留**至少** `shutdownTimeout` 加 10 秒安全余量，否则 K8s SIGKILL 会截断进程内 shutdown。

## 预算公式

```
terminationGracePeriodSeconds  >=  shutdownTimeout + 10s
```

- `shutdownTimeout` — `bootstrap.WithShutdownTimeout(d)`，默认 30s。覆盖整个四阶段 shutdown：readiness flip → preShutdownDelay → HTTP drain → LIFO teardown。`preShutdownDelay` 嵌在这个总预算里串行消耗，**不**额外叠加
- `10s` 安全余量 — 覆盖 SIGTERM → kubelet 上报 → 进程主循环响应、以及操作系统 / runtime 调度抖动

> `preShutdownDelay` 为何不在公式里：`runtime/bootstrap/phases_shutdown.go:108` 用 `shutdownTimeout` 一次性派生 `shutCtx`，readiness flip 阶段的 `preShutdownDelay` 等待与 HTTP drain / LIFO teardown 共享这同一个 deadline。`WithPreShutdownDelay` godoc 里同步声明：「The delay counts toward the total shutdownTimeout budget (not additive).」因此 K8s grace 只需覆盖 `shutdownTimeout`，再加 OS 级安全余量即可。

## 进程侧声明（advisory）

`bootstrap.WithTerminationGracePeriod(d)` 让 composition root 把 K8s manifest 的预期值告诉框架；phase0 用此值做一致性校验：

```go
bootstrap.New(
    bootstrap.WithShutdownTimeout(20*time.Second),
    bootstrap.WithPreShutdownDelay(5*time.Second),  // 嵌在 20s 内
    bootstrap.WithTerminationGracePeriod(35*time.Second), // >= 20 + 10
    // ...
)
```

不一致时 phase0 emit `slog.Warn`：

```
WARN bootstrap: terminationGracePeriodSeconds insufficient for graceful shutdown
  termination_grace_period=25s shutdown_timeout=20s pre_shutdown_delay=5s minimum_required=30s
  hint=increase Kubernetes pod terminationGracePeriodSeconds to >= shutdownTimeout + 10s, ...
```

`pre_shutdown_delay` 字段仅作信息记录，方便运维排查；不参与 `minimum_required` 计算。

**`WithTerminationGracePeriod` 不改变运行时行为**——真实的 K8s grace window 仍然在 pod spec 里。这个 option 只是把"应当配多少"声明给 phase0，让 misalignment 在启动期 fail-loud 而不是在某次 SIGTERM 才暴露。

## Pod spec 示例

```yaml
apiVersion: apps/v1
kind: Deployment
spec:
  template:
    spec:
      terminationGracePeriodSeconds: 35   # >= 20 + 10
      containers:
        - name: gocell
          # …
```

## Roadmap 偏离备忘

`docs/plans/202605011500-029-master-roadmap.md` N3 原文给的公式是 `>= shutdownTimeout + preShutdownDelay + 10s`，与 `WithPreShutdownDelay` 的 godoc 语义冲突（preShutdownDelay 已嵌在 shutdownTimeout 里）。本 PR 按代码实际行为落地正确公式 `>= shutdownTimeout + 10s`，并在 ADR `docs/architecture/202605051800-adr-rollback-ctx-decoupling.md` 注明偏离原因。

## 相关文档

- `docs/ops/listener-topology.md` — HTTP listener / drain 顺序
- `docs/architecture/202605051800-adr-rollback-ctx-decoupling.md` — 启动失败路径的 rollback ctx 派生（与 SIGTERM-during-Start 场景相关）
