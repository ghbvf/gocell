# GoCell

> **早期开发阶段** — kernel（运行时原语 + 治理工具链）已实现，runtime / adapters / cells 业务逻辑在 Phase 2-4 实现中。

Cell-native Go 工程底座。

GoCell 提供 Cell/Slice 运行时原语、治理工具链和内置 Cell，用于构建基于 Slice-Cell 架构的可靠 Go 服务。

## 内置 Cell

- **access-core** — SSO/OIDC 认证、JWT、Session 管理、RBAC
- **audit-core** — 基于 HMAC-SHA256 哈希链的防篡改审计追踪
- **config-core** — 配置热更新、功能开关、版本回滚

## Kernel

- Cell/Slice/Assembly 运行时 + 生命周期管理
- 元数据治理（cell.yaml / slice.yaml / contract.yaml）
- Assembly 代码生成
- Journey Catalog 和 Status Board
- 事务性 Outbox、幂等、Replay
- 契约注册、依赖检查、影响面分析
- Caller Trace、Verified Wrapper
- Webhook 接收与分发

## 适配器

| 层级 | 适配器 |
|------|--------|
| 一等适配器 | PostgreSQL、Redis、OIDC/SSO、S3/MinIO、VictoriaMetrics |
| 正式 Adapter Family | RabbitMQ、WebSocket |
| 可选 | MySQL/MariaDB、Kafka、SQLite、SSE、gRPC、搜索、通知 |

## 快速开始

```go
package main

import (
    "context"
    "github.com/ghbvf/gocell"
    "github.com/ghbvf/gocell/cells/access"
    "github.com/ghbvf/gocell/cells/audit"
    "github.com/ghbvf/gocell/cells/config"
)

func main() {
    app := gocell.NewAssembly("my-app")
    app.Register(access.NewCore(accessCfg))
    app.Register(audit.NewCore(auditCfg))
    app.Register(config.NewCore(configCfg))
    app.Start(context.Background())
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
