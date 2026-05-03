# 可观测性规范

## slog 日志级别

| Level | 使用场景 | 要求 |
|-------|---------|------|
| Error | 影响正确性：DB 写入失败、ACK 失败、状态机违规、安全事件 | 必须含完整 error + 关联业务字段 |
| Warn | 降级运行：Redis 不可用、noop publisher、重试预算耗尽 | — |
| Info | 生命周期：服务启动、consumer group 加入、migration 完成 | — |
| Debug | 开发诊断：payload dump、逐条命令 trace | 生产环境关闭 |

## 安全约束

- 禁止 Debug 级别 dump 完整请求/响应 body（生产中泄漏敏感信息）
- 错误日志必须包含结构化关联字段（`execution_id`、`policy_id` 等），禁止裸 `slog.Error("failed")`

## Readyz Probe 命名

- Adapter readiness probe 使用 stable snake_case，并以后缀 `_ready` 表示依赖可用性，例如 `rabbitmq_ready`、`vault_transit_ready`。
- 一个 adapter 只有单一外部依赖时，禁止同时暴露多个同义 ready probe；多角色 worker 可用 `component-role` 拆分不同失败域。
- probe 名是运维契约；改名必须同步 dashboard / alert / 文档，并用 archtest 或单测锁定。

## HTTP Metrics `cell` Label

`http_requests_total` 与 `http_request_duration_seconds` 的 `cell` label 表示请求落入的 cell：

- **业务请求**：`cell=<cellID>` — 由 `bootstrap.mountOneRouteGroup` 在挂载 `RouteGroup` 时把 `WithCellIDContext(rg.CellID)` 注入到 chi sub-mux，使该 group 内的所有 handler 在请求 ctx 看到具体 cellID（来源 = K#02 `phase5CollectRouteGroups` 自动填充的 `RouteGroup.CellID`）。
- **框架/未匹配请求**：`cell="_runtime"` 哨兵 — 由 `runtime/http/router.go` 在 listener root mux 上安装 `WithCellIDContext("_runtime")`（在 `Metrics` 中间件之前），覆盖 `/healthz`、`/readyz`、`/metrics`、404 等所有未落入业务 RouteGroup 的请求。

`router` 是单一注入点，`bootstrap` 仅做 group 级覆盖；运行时顺序「root → group」配合 `context.WithValue` 后写优先，business cellID 自动覆盖 `_runtime`。

`metrics` 中间件用 `ctxkeys.MustCellIDFrom(r.Context())` 读取 cellID — 缺失即 panic（safeObserve 吞掉 panic 但不录入 metric），保证「无 fallback 静默打错 label」。回退由 `tools/archtest/http_metrics_label_test.go` 4 条 archtest 守护：CTXSOURCE / NO-ASSEMBLY-DERIVE / NO-CONFIG-CELLID / LISTENER-ROOT-RUNTIME。
