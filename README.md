# GoCell

Cell-native Go engineering foundation.

GoCell provides Cell/Slice runtime primitives, a governance toolchain, and built-in cells for building reliable Go services with slice-cell architecture.

## Built-in Cells

- **access-core** — SSO/OIDC authentication, JWT, session management, RBAC
- **audit-core** — Tamper-evident audit trail with HMAC-SHA256 hash chain
- **config-core** — Configuration hot-reload, feature flags, version rollback

## Kernel

- Cell/Slice/Assembly runtime with lifecycle management
- Metadata governance (cell.yaml / slice.yaml / contract.yaml)
- Assembly code generation
- Journey Catalog and Status Board
- Transactional outbox, idempotency, replay
- Contract registry, dependency checker, impact analysis
- Caller trace, verified wrappers
- Webhook receiver and dispatcher

## Adapters

| Tier | Adapters |
|------|----------|
| First-class | PostgreSQL, Redis, OIDC/SSO, S3/MinIO, VictoriaMetrics |
| Formal family | RabbitMQ, WebSocket |
| Optional | MySQL/MariaDB, Kafka, SQLite, SSE, gRPC, search, notification |

## Quick Start

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

## License

[MIT](LICENSE)
