# Graceful Shutdown on Kubernetes

GoCell 的 graceful shutdown 由两块预算组成，运维侧需要在 Pod spec `terminationGracePeriodSeconds` 中预留**至少**两块预算之和加 10 秒安全余量，否则 K8s SIGKILL 会截断进程内 shutdown。

## 预算公式

```
terminationGracePeriodSeconds  >=  shutdownTimeout + preShutdownDelay + 10s
```

- `shutdownTimeout` — `bootstrap.WithShutdownTimeout(d)`，默认 30s。覆盖 HTTP drain + lifecycle teardown + ManagedResource Close 的总时长
- `preShutdownDelay` — `bootstrap.WithPreShutdownDelay(d)`，默认 0s。`/readyz` 翻 503 之后等待 LB 拉黑流量再开始 HTTP shutdown 的延迟。**计入** `shutdownTimeout` 总预算（不是叠加）
- `10s` 安全余量 — 覆盖 SIGTERM → preStop hook 进入 → 进程主循环响应的 OS / kubelet 传播开销

> 注意：`preShutdownDelay` 在公式中**单独累加**用作下界估算，因为它会延后 `shutdownTimeout` 的实际开始时间，操作员需要 grace 总长能覆盖这两段串行。

## 进程侧声明（advisory）

`bootstrap.WithTerminationGracePeriod(d)` 让 composition root 把 K8s manifest 的预期值告诉框架；phase0 用此值做一致性校验：

```go
bootstrap.New(
    bootstrap.WithShutdownTimeout(20*time.Second),
    bootstrap.WithPreShutdownDelay(5*time.Second),
    bootstrap.WithTerminationGracePeriod(45*time.Second),
    // ...
)
```

不一致时 phase0 emit `slog.Warn`：

```
WARN bootstrap: terminationGracePeriodSeconds insufficient for graceful shutdown
  termination_grace_period=30s shutdown_timeout=20s pre_shutdown_delay=5s minimum_required=35s
  hint=increase Kubernetes pod terminationGracePeriodSeconds to >= shutdownTimeout + preShutdownDelay + 10s, ...
```

**`WithTerminationGracePeriod` 不改变运行时行为**——真实的 K8s grace window 仍然在 pod spec 里。这个 option 只是把"应当配多少"声明给 phase0，让 misalignment 在启动期 fail-loud 而不是在某次 SIGTERM 才暴露。

## Pod spec 示例

```yaml
apiVersion: apps/v1
kind: Deployment
spec:
  template:
    spec:
      terminationGracePeriodSeconds: 45   # >= 20 + 5 + 10
      containers:
        - name: gocell
          # …
```

## 相关文档

- `docs/ops/listener-topology.md` — HTTP listener / drain 顺序
- `docs/architecture/202605051800-adr-rollback-ctx-decoupling.md` — 启动失败路径的 rollback ctx 派生（与 SIGTERM-during-Start 场景相关）
