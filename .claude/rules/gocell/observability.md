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

- **业务请求**：`cell=<cellID>` — 由 `runtime/http/router.Router.MountRouteGroup` 记录 `RouteGroup.CellID` 与 HTTP namespace 的 ownership，listener-root `CellAttribution` middleware 在 tracing/access-log/metrics/protection 之前写入 `kernel/ctxkeys.CellID`。成功 handler、auth/rate-limit/circuit-breaker/body-limit 前置拒绝、chi 405 都必须使用同一 cell。
- **框架/未匹配请求**：`cell="_runtime"` 哨兵 — `runtime/http/middleware.Metrics` 在 `ctxkeys.CellID` 缺失时使用 `RuntimeCellIDSentinel`，覆盖 `/healthz`、`/readyz`、`/metrics` 自身、listener 外 404 等所有未落入业务 RouteGroup 的请求。

`RouteGroup.Prefix` 非空时按 path segment 前缀归属；`Prefix == ""` 不表示拥有整个 listener，而是从该 RouteGroup 内实际注册的 `Route` / `Handle` / `Mount` / `auth.Mount` 合同路径派生归属。重叠 namespace 按最长前缀/最长模板胜出。

`metrics` 中间件只读取 `ctxkeys.CellIDFrom`，缺失时使用 `_runtime`。route label 对前置拒绝使用 router 的 route-template fallback resolver，避免业务路径拒绝被记为 `route="unmatched"`。回退由 `tools/archtest/http_metrics_label_test.go` 守护：CTXSOURCE / ROUTER-ATTRIBUTION / NO-ASSEMBLY-DERIVE / NO-CONFIG-CELLID / RUNTIME-SENTINEL。
