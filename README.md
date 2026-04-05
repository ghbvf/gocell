# GoCell

> **Phase 3 完成** — 6 个外部系统适配器（postgres / redis / oidc / s3 / rabbitmq / websocket）已实现，安全加固完成，Phase 2 技术债务系统性偿还（约 65/74 条 RESOLVED）。examples/ 示例项目在 Phase 4 实现中。

Cell-native Go 工程底座。

GoCell 提供 Cell/Slice 运行时原语、治理工具链和内置 Cell，用于构建基于 Slice-Cell 架构的可靠 Go 服务。

## 内置 Cell

- **config-core** — 配置热更新、功能开关、版本发布与回滚（5 Slices: config-write / config-read / config-publish / config-subscribe / feature-flag）
- **access-core** — 身份管理、JWT Session 生命周期、RBAC 授权（7 Slices: identity-manage / session-login / session-refresh / session-logout / session-validate / authorization-decide / rbac-check）
- **audit-core** — 基于 HMAC-SHA256 哈希链的防篡改审计追踪（4 Slices: audit-append / audit-verify / audit-archive / audit-query）

## Adapter 层

| Adapter | 核心能力 | 实现的 kernel 接口 |
|---------|---------|-----------------|
| `adapters/postgres` | Pool (pgx/v5)、TxManager、Migrator、OutboxWriter、OutboxRelay | `outbox.Writer`、`outbox.Relay`、`worker.Worker` |
| `adapters/redis` | Client (go-redis/v9)、DistLock、IdempotencyChecker、Cache | `idempotency.Checker` |
| `adapters/oidc` | OIDC Provider Client、Token Exchange、JWKS 验证 | — |
| `adapters/s3` | S3/MinIO Client、PresignedURL、ConfigFromEnv | — |
| `adapters/rabbitmq` | Publisher、Subscriber、ConsumerBase (DLQ + retry) | `outbox.Publisher`、`outbox.Subscriber` |
| `adapters/websocket` | WebSocket Hub、signal-first 推送、Origin 白名单 | — |

每个 adapter 均提供：`Health(ctx) error`、编译时接口断言、`doc.go`、统一 `GOCELL_*` 环境变量前缀。

## Runtime 层

- **bootstrap** — 统一应用生命周期管理（配置加载 → Assembly 启动 → HTTP 服务 → 事件订阅 → 优雅关闭）；支持 `WithPublisher` / `WithSubscriber` 接口注入
- **config** — YAML + 环境变量配置加载，支持热更新，已集成 bootstrap 生命周期
- **eventbus** — 内存事件总线（开发/测试用，支持重试 + 死信队列）
- **http/middleware** — RequestID / RealIP (trustedProxies) / Recovery / AccessLog / SecurityHeaders / BodyLimit / RateLimit
- **http/router** — 基于 chi 的路由构建器
- **http/health** — 健康检查端点
- **shutdown** — 信号捕获 + 优雅关闭管理
- **worker** — 后台任务编排
- **auth** — RS256 JWTIssuer / JWTVerifier / RBAC / ServiceToken（timestamp 防重放）

## Kernel

- Cell/Slice/Assembly 运行时 + 生命周期管理（LIFO 关闭顺序、BaseCell 互斥锁）
- 元数据治理（cell.yaml / slice.yaml / contract.yaml）
- Assembly 代码生成
- Journey Catalog 和 Status Board
- 契约注册、依赖检查、影响面分析
- 事务性 Outbox（outbox.Writer / outbox.Relay / outbox.Publisher / outbox.Subscriber）
- 幂等检查（idempotency.Checker）
- 统一错误码（pkg/errcode）

## 快速开始

### 一键启动完整基础设施（PostgreSQL + Redis + RabbitMQ + MinIO）

```bash
cp .env.example .env
docker compose up -d
# 等待所有服务 healthy（约 30 秒）
```

### 启动 core-bundle（3 个内建 Cell）

```bash
cd src && go run ./cmd/core-bundle
# HTTP server listening on :8080
# 健康检查: GET /healthz
# 配置 API: /api/v1/config/*
# 认证 API: /api/v1/access/*
# 审计 API: /api/v1/audit/*
```

### 自定义 Assembly（with Adapter 注入）

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
    "github.com/ghbvf/gocell/adapters/postgres"
    "github.com/ghbvf/gocell/adapters/rabbitmq"
    "github.com/ghbvf/gocell/runtime/bootstrap"
)

func main() {
    pg, _ := postgres.NewPool(ctx, postgres.ConfigFromEnv())
    txMgr := postgres.NewTxManager(pg)
    outboxWriter := postgres.NewOutboxWriter(txMgr)

    rmqConn, _ := rabbitmq.NewConnection(rabbitmq.ConfigFromEnv())
    publisher, _ := rabbitmq.NewPublisher(rmqConn, rabbitmq.PublisherConfig{})

    configCell := configcore.NewConfigCore(
        configcore.WithPublisher(publisher),
        configcore.WithOutboxWriter(outboxWriter),
    )

    asm := assembly.New(assembly.Config{ID: "my-app"})
    asm.Register(configCell)

    ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
    defer cancel()

    app := bootstrap.New(
        bootstrap.WithAssembly(asm),
        bootstrap.WithHTTPAddr(":8080"),
        bootstrap.WithPublisher(publisher),
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
├── pkg/          — 共享工具包（errcode / ctxkeys / uid）
├── cmd/          — CLI 入口（gocell validate / scaffold / generate / check / verify）
├── examples/     — 示例项目（sso-bff / todo-order / iot-device，Phase 4）
├── generated/    — 工具生成产物（禁止手工编辑）
└── actors.yaml   — 外部 Actor 注册
```

## 许可证

[MIT](LICENSE)
