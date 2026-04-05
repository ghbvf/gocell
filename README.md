# GoCell

> **Phase 2 开发中** — kernel 运行时原语 + 治理工具链已稳定。runtime 层（bootstrap / config / eventbus / HTTP middleware）和 3 个内建 Cell（config-core / access-core / audit-core）已实现。adapters 在 Phase 3 实现中。

Cell-native Go 工程底座。

GoCell 提供 Cell/Slice 运行时原语、治理工具链和内置 Cell，用于构建基于 Slice-Cell 架构的可靠 Go 服务。

## 内置 Cell

- **config-core** — 配置热更新、功能开关、版本发布与回滚（5 Slices: config-write / config-read / config-publish / config-subscribe / feature-flag）
- **access-core** — 身份管理、JWT Session 生命周期、RBAC 授权（7 Slices: identity-manage / session-login / session-refresh / session-logout / session-validate / authorization-decide / rbac-check）
- **audit-core** — 基于 HMAC-SHA256 哈希链的防篡改审计追踪（4 Slices: audit-append / audit-verify / audit-archive / audit-query）

## Runtime 层

- **bootstrap** — 统一应用生命周期管理（配置加载 → Assembly 启动 → HTTP 服务 → 事件订阅 → 优雅关闭）
- **config** — YAML + 环境变量配置加载，支持热更新
- **eventbus** — 内存事件总线（开发/测试用，支持重试 + 死信队列）
- **http/middleware** — RequestID / RealIP / Recovery / AccessLog / SecurityHeaders / BodyLimit / RateLimit
- **http/router** — 基于 chi 的路由构建器
- **http/health** — 健康检查端点
- **shutdown** — 信号捕获 + 优雅关闭管理
- **worker** — 后台任务编排
- **auth** — TokenVerifier / Authorizer 接口

## Kernel

- Cell/Slice/Assembly 运行时 + 生命周期管理
- 元数据治理（cell.yaml / slice.yaml / contract.yaml）
- Assembly 代码生成
- Journey Catalog 和 Status Board
- 契约注册、依赖检查、影响面分析
- 事务性 Outbox、幂等、Replay（Phase 2 实现中）
- Caller Trace、Verified Wrapper（Phase 2 实现中）
- Webhook 接收与分发（Phase 2 实现中）

## 适配器（Phase 3）

| 层级 | 适配器 |
|------|--------|
| 一等适配器 | PostgreSQL、Redis、OIDC/SSO、S3/MinIO、VictoriaMetrics |
| 正式 Adapter Family | RabbitMQ、WebSocket |
| 可选 | MySQL/MariaDB、Kafka、SQLite、SSE、gRPC、搜索、通知 |

## 快速开始

### 启动 core-bundle（3 个内建 Cell）

```bash
cd src && go run ./cmd/core-bundle
# HTTP server listening on :8080
# 健康检查: GET /healthz
# 配置 API: /api/v1/config/*
# 认证 API: /api/v1/access/*
# 审计 API: /api/v1/audit/*
```

### 自定义 Assembly

```go
package main

import (
    "context"
    "log/slog"
    "os"
    "os/signal"
    "syscall"

    configcore "github.com/ghbvf/gocell/cells/config-core"
    "github.com/ghbvf/gocell/kernel/assembly"
    "github.com/ghbvf/gocell/runtime/bootstrap"
    "github.com/ghbvf/gocell/runtime/eventbus"
)

func main() {
    eb := eventbus.New()

    configCell := configcore.NewConfigCore(
        configcore.WithInMemoryDefaults(),
        configcore.WithPublisher(eb),
    )

    asm := assembly.New(assembly.Config{ID: "my-app"})
    asm.Register(configCell)

    ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
    defer cancel()

    app := bootstrap.New(
        bootstrap.WithAssembly(asm),
        bootstrap.WithHTTPAddr(":8080"),
        bootstrap.WithEventBus(eb),
    )
    if err := app.Run(ctx); err != nil {
        slog.Error("application failed", "error", err)
        os.Exit(1)
    }
}
```

## 目录结构

```
src/
├── kernel/       — Cell/Slice 运行时 + 治理工具（底座灵魂）
├── cells/        — Cell 实现（access-core / audit-core / config-core）
├── contracts/    — 跨 Cell 边界契约（按 {kind}/{domain}/{version}/ 组织）
├── journeys/     — Journey 验收规格 + status-board.yaml
├── assemblies/   — 物理打包配置
├── fixtures/     — 测试夹具
├── runtime/      — 通用运行时（http / auth / worker / observability）
├── adapters/     — 外部系统适配（postgres / redis / oidc / s3 / rabbitmq / websocket）
├── pkg/          — 共享工具包（errcode / ctxkeys）
├── cmd/          — CLI 入口（gocell validate / scaffold / generate / check / verify）
├── examples/     — 示例项目（sso-bff / todo-order / iot-device）
├── generated/    — 工具生成产物（禁止手工编辑）
└── actors.yaml   — 外部 Actor 注册
```

## 许可证

[MIT](LICENSE)
