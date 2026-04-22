# Phase Charter — Phase 2: Runtime + Built-in Cells

## Phase 目标

在已完成的 kernel 底座（Phase 0+1: 59 Go 文件, 全部编译通过 & 测试绿色）上构建 runtime 运行时层和 3 个内建 Cell，使 GoCell 从"可编译的元数据治理框架"进化为"可运行的 Cell-native Go 框架"。

交付物包括：HTTP 中间件链 / chi 路由 / 健康检查、YAML+env 配置加载 / 文件 watcher 热更新、统一启动器 / graceful shutdown、Prometheus 指标 / OpenTelemetry tracing / slog 结构化日志、后台 worker 生命周期、JWT RS256 验证 / RBAC / 服务间认证，以及 access-core (5 slices)、audit-core (3 slices)、config-core (4 slices) 三个 Cell 的完整业务实现。

## 范围

### 目标（In Scope）

- **runtime/http**: 7 个 chi 中间件（request_id, real_ip, recovery, access_log, security_headers, body_limit, rate_limit）、`/healthz` + `/readyz` 健康端点、chi-based 路由构建器
- **runtime/config**: YAML/env 配置加载 + 文件变更 watcher
- **runtime/bootstrap**: 统一启动器（parse config -> init assembly -> start HTTP -> start workers）
- **runtime/shutdown**: graceful shutdown（signal -> timeout -> 有序 teardown）
- **runtime/observability**: Prometheus 指标注册 + HTTP 指标中间件、OpenTelemetry tracer、slog handler + trace_id/span_id 关联
- **runtime/worker**: 后台 worker 生命周期 + 异步 job 框架
- **runtime/auth**: JWT RS256 验证 + Claims + kid rotation、RBAC 中间件、服务间认证
- **cells/access-core**: identity-manage / session-login / session-refresh / session-logout / authorization-decide（5 slices）
- **cells/audit-core**: audit-write / audit-verify / audit-archive（3 slices, HMAC-SHA256 hash chain）
- **cells/config-core**: config-manage / config-publish / config-subscribe / feature-flag（4 slices）
- 补齐对应的 contract YAML、Journey YAML、status-board 条目
- 新增外部依赖: `go-chi/chi/v5`, `golang.org/x/crypto`

### 非目标（Out of Scope）

- adapters/ 层实现（postgres, redis, rabbitmq 等） — Phase 3
- examples/ 示例项目 — Phase 4
- Docker / CI/CD 部署配置 — Phase 3+
- 前端代码 — 项目无前端

### N/A 声明

| 标准文件 | 理由 |
|---------|------|
| 上一 Phase kernel-review-report.md | Phase 0+1 在工作流体系前完成，无此文件 |
| 上一 Phase product-review-report.md | 同上 |
| 上一 Phase tech-debt.md | 同上 |

## 连续性处理

### 从上一 Phase 继承的必须修复项

Phase 0+1 在工作流体系建立前完成，无遗留 review/tech-debt 文件。首次启用工作流，连续性检查 N/A。

### 延迟处理

无。

## 对标参考要求

根据 CLAUDE.md「对标对比规则」，Phase 2 每个模块实施前必须：

1. 查 `docs/references/framework-comparison.md` 找到对标文件路径
2. 用 WebFetch 拉取对标源码
3. 提取关键设计决策
4. 在 commit message 中注明 `ref: {framework} {file}` + 采纳/偏离理由

主要对标映射：

| 模块 | Primary 对标 | Secondary 对标 |
|------|-------------|---------------|
| runtime/bootstrap | Uber fx (app.go) | Kratos (app.go) |
| runtime/http/middleware | Kratos (middleware/) | go-zero (rest/handler/) |
| runtime/config | go-micro (config/) | Kratos (config/) |
| runtime/worker | Watermill (message/) | — |
| runtime/auth | Kratos (middleware/auth/) | — |
| runtime/observability | Kratos (middleware/tracing/, metrics/) | — |
