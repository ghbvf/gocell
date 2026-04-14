# R0-A3: 分层依赖图与违规列表

> 生成日期: 2026-04-06  
> 分析范围: 项目根目录 下所有非测试 `.go` 文件的 `github.com/ghbvf/gocell/` 内部 import  
> 模块路径: `github.com/ghbvf/gocell`

---

## 依赖图（按层）

### Layer 0: pkg

| 包 | 内部依赖 |
|---|---------|
| `pkg/ctxkeys` | (无内部依赖) |
| `pkg/errcode` | (无内部依赖) |
| `pkg/httputil` | `pkg/errcode` |
| `pkg/id` | (无内部依赖, deprecated) |
| `pkg/uid` | (无内部依赖) |

### Layer 1: kernel

| 包 | 内部依赖 |
|---|---------|
| `kernel/outbox` | (无内部依赖 -- 仅标准库) |
| `kernel/idempotency` | (���内部依赖 -- 仅标准库) |
| `kernel/metadata` | `pkg/errcode` |
| `kernel/metadata/schemas` | (无内部依赖 -- embed only) |
| `kernel/cell` (types.go, base.go) | `pkg/errcode` |
| `kernel/cell` (interfaces.go) | (无内部依赖) |
| `kernel/cell` (registrar.go) | `kernel/outbox` |
| `kernel/cell` (consistency.go) | (无内部依赖) |
| `kernel/registry` | `kernel/metadata` |
| `kernel/assembly` (assembly.go) | `kernel/cell`, `pkg/errcode` |
| `kernel/assembly` (generator.go) | `kernel/assembly/gentpl`, `kernel/metadata`, `kernel/registry`, `pkg/errcode` |
| `kernel/assembly/gentpl` | (无内部依赖 -- embed only) |
| `kernel/governance` (validate.go) | `kernel/metadata` |
| `kernel/governance` (helpers.go) | `kernel/cell`, `kernel/metadata` |
| `kernel/governance` (rules_topo.go) | `kernel/cell` |
| `kernel/governance` (rules_verify.go) | `kernel/cell` |
| `kernel/governance` (rules_fmt.go) | `kernel/cell` |
| `kernel/governance` (rules_advisory.go) | (无内部依赖) |
| `kernel/governance` (rules_ref.go) | (无内部依赖 -- uses receiver types only) |
| `kernel/governance` (depcheck.go) | `kernel/metadata`, `kernel/registry` |
| `kernel/governance` (targets.go) | `kernel/metadata` |
| `kernel/journey` | `kernel/metadata` |
| `kernel/scaffold` | `pkg/errcode` |
| `kernel/slice` | `kernel/metadata`, `pkg/errcode` |

**kernel 汇总依赖**:
- kernel -> pkg/errcode (合规)
- kernel -> kernel 内部交叉 (合规: cell -> outbox, governance -> cell/metadata/registry, etc.)
- **无违规**: kernel 不依赖 runtime/, adapters/, cells/

### Layer 2: runtime

| 包 | 内部依赖 |
|---|---------|
| `runtime/auth` (auth.go) | (无内部依赖) |
| `runtime/auth` (jwt.go) | `pkg/errcode` |
| `runtime/auth` (keys.go) | `pkg/errcode` |
| `runtime/auth` (middleware.go) | `pkg/ctxkeys` |
| `runtime/auth` (servicetoken.go) | (无内部依赖) |
| `runtime/config` (config.go) | (无内部依赖) |
| `runtime/config` (watcher.go) | (无内部依赖 -- 仅 fsnotify) |
| `runtime/eventbus` | `kernel/outbox`, `pkg/errcode`, `pkg/uid` |
| `runtime/http/health` | `kernel/assembly` |
| `runtime/http/middleware` (access_log.go) | `pkg/ctxkeys` |
| `runtime/http/middleware` (body_limit.go) | (无内部依赖) |
| `runtime/http/middleware` (rate_limit.go) | `pkg/ctxkeys` |
| `runtime/http/middleware` (real_ip.go) | `pkg/ctxkeys` |
| `runtime/http/middleware` (recovery.go) | `pkg/ctxkeys` |
| `runtime/http/middleware` (request_id.go) | `pkg/ctxkeys` |
| `runtime/http/middleware` (security_headers.go) | (无内部依赖) |
| `runtime/http/router` | `kernel/cell`, `runtime/http/health`, `runtime/http/middleware`, `runtime/observability/metrics` |
| `runtime/observability/logging` | `pkg/ctxkeys` |
| `runtime/observability/metrics` | (无内部依赖) |
| `runtime/observability/tracing` | `pkg/ctxkeys`, `pkg/httputil` |
| `runtime/shutdown` | (无内部依赖) |
| `runtime/worker` (worker.go) | (无内部依赖) |
| `runtime/worker` (periodic.go) | (无内部依赖) |
| `runtime/bootstrap` | `kernel/assembly`, `kernel/cell`, `kernel/outbox`, `runtime/config`, `runtime/eventbus`, `runtime/http/health`, `runtime/http/router`, `runtime/shutdown`, `runtime/worker` |

**runtime 汇总依赖**:
- runtime -> kernel/assembly, kernel/cell, kernel/outbox (合规)
- runtime -> pkg/errcode, pkg/ctxkeys, pkg/httputil, pkg/uid (合规)
- runtime -> runtime 内部交叉 (合规: router -> health/middleware/metrics, bootstrap -> 多 runtime 子包)
- **无违规**: runtime 不依赖 cells/, adapters/

### Layer 3: adapters

| 包 | 内部依赖 |
|---|---------|
| `adapters/postgres` (errors.go) | `pkg/errcode` |
| `adapters/postgres` (pool.go) | `pkg/errcode` |
| `adapters/postgres` (migrator.go) | `pkg/errcode` |
| `adapters/postgres` (tx_manager.go) | `pkg/errcode` |
| `adapters/postgres` (helpers.go) | (无内部依赖) |
| `adapters/postgres` (outbox_writer.go) | `kernel/outbox`, `pkg/errcode` |
| `adapters/postgres` (outbox_relay.go) | `kernel/outbox`, `pkg/errcode`, **`runtime/worker`** |
| `adapters/oidc` (config.go) | `pkg/errcode` |
| `adapters/oidc` (errors.go) | `pkg/errcode` |
| `adapters/oidc` (provider.go) | `pkg/errcode` |
| `adapters/oidc` (token.go) | `pkg/errcode` |
| `adapters/oidc` (userinfo.go) | `pkg/errcode` |
| `adapters/oidc` (verifier.go) | `pkg/errcode` |
| `adapters/rabbitmq` (connection.go) | `pkg/errcode` |
| `adapters/rabbitmq` (publisher.go) | `kernel/outbox`, `pkg/errcode` |
| `adapters/rabbitmq` (subscriber.go) | `kernel/outbox`, `pkg/errcode` |
| `adapters/rabbitmq` (consumer_base.go) | `kernel/idempotency`, `kernel/outbox` |
| `adapters/redis` (cache.go) | `pkg/errcode` |
| `adapters/redis` (client.go) | `pkg/errcode` |
| `adapters/redis` (distlock.go) | `pkg/errcode` |
| `adapters/redis` (idempotency.go) | `kernel/idempotency`, `pkg/errcode` |
| `adapters/s3` (client.go) | `pkg/errcode` |
| `adapters/s3` (errors.go) | `pkg/errcode` |
| `adapters/s3` (objects.go) | `pkg/errcode` |
| `adapters/s3` (presigned.go) | `pkg/errcode` |
| `adapters/websocket` (errors.go) | `pkg/errcode` |
| `adapters/websocket` (handler.go) | `pkg/errcode` |
| `adapters/websocket` (hub.go) | `pkg/errcode`, `pkg/uid` |

**adapters 汇总依赖**:
- adapters -> kernel/outbox, kernel/idempotency (合规: 实现 kernel 定义的接口)
- adapters -> pkg/errcode, pkg/uid (合规)
- **违规**: `adapters/postgres/outbox_relay.go` -> `runtime/worker` (adapters 不应依赖 runtime)
- adapters 不依赖 cells/ (合规)

### Layer 4: cells

#### cells/access-core

| 包 | 内部依赖 |
|---|---------|
| `cells/access-core` (cell.go) | 自身 internal/mem, internal/ports, slices/*, `kernel/cell`, `kernel/outbox`, `pkg/errcode`, **`runtime/auth`** |
| `cells/access-core/internal/domain` (session.go) | `pkg/errcode` |
| `cells/access-core/internal/domain` (user.go) | `pkg/errcode` |
| `cells/access-core/internal/domain` (role.go) | (无内部依赖) |
| `cells/access-core/internal/ports` | 自身 internal/domain |
| `cells/access-core/internal/mem` | 自身 internal/domain, internal/ports, `pkg/errcode` |
| `cells/access-core/slices/authorizationdecide` | 自身 internal/ports, **`runtime/auth`** |
| `cells/access-core/slices/identitymanage` (service.go) | 自身 internal/domain, internal/ports, `kernel/outbox`, `pkg/errcode`, `pkg/uid` |
| `cells/access-core/slices/identitymanage` (handler.go) | 自身 internal/domain, `pkg/httputil` |
| `cells/access-core/slices/rbaccheck` (service.go) | 自身 internal/domain, internal/ports, `pkg/errcode` |
| `cells/access-core/slices/rbaccheck` (handler.go) | `pkg/httputil` |
| `cells/access-core/slices/sessionlogin` (service.go) | 自身 internal/domain, internal/ports, `kernel/outbox`, `pkg/errcode`, `pkg/uid`, **`runtime/auth`** |
| `cells/access-core/slices/sessionlogin` (handler.go) | `pkg/httputil` |
| `cells/access-core/slices/sessionlogout` (service.go) | 自身 internal/ports, `kernel/outbox`, `pkg/errcode`, `pkg/uid` |
| `cells/access-core/slices/sessionlogout` (handler.go) | `pkg/httputil` |
| `cells/access-core/slices/sessionrefresh` (service.go) | 自身 internal/ports, `pkg/errcode`, **`runtime/auth`** |
| `cells/access-core/slices/sessionrefresh` (handler.go) | `pkg/httputil` |
| `cells/access-core/slices/sessionvalidate` (service.go) | 自身 internal/ports, `pkg/errcode`, **`runtime/auth`** |

#### cells/audit-core

| 包 | 内部依赖 |
|---|---------|
| `cells/audit-core` (cell.go) | 自身 internal/mem, internal/ports, slices/*, `kernel/cell`, `kernel/outbox`, `pkg/errcode` |
| `cells/audit-core/internal/domain` | (无内部依赖) |
| `cells/audit-core/internal/ports` | 自身 internal/domain |
| `cells/audit-core/internal/mem` | 自身 internal/domain, internal/ports |
| `cells/audit-core/internal/adapters/postgres` | 自身 internal/domain, internal/ports, `pkg/errcode` |
| `cells/audit-core/internal/adapters/s3archive` | 自身 internal/domain, internal/ports, `pkg/errcode` |
| `cells/audit-core/slices/auditappend` | 自身 internal/domain, internal/ports, `kernel/outbox`, `pkg/uid` |
| `cells/audit-core/slices/auditarchive` | `pkg/errcode` |
| `cells/audit-core/slices/auditquery` (service.go) | 自身 internal/domain, internal/ports |
| `cells/audit-core/slices/auditquery` (handler.go) | 自身 internal/ports, `pkg/httputil` |
| `cells/audit-core/slices/auditverify` | 自身 internal/domain, internal/ports, `kernel/outbox`, `pkg/uid` |

#### cells/config-core

| 包 | 内部依赖 |
|---|---------|
| `cells/config-core` (cell.go) | 自身 internal/mem, internal/ports, slices/*, `kernel/cell`, `kernel/outbox`, `pkg/errcode` |
| `cells/config-core/internal/domain` | (无内部依赖) |
| `cells/config-core/internal/ports` | 自身 internal/domain |
| `cells/config-core/internal/mem` | 自身 internal/domain, internal/ports, `pkg/errcode` |
| `cells/config-core/internal/adapters/postgres` | 自身 internal/domain, internal/ports, `pkg/errcode` |
| `cells/config-core/slices/configpublish` (service.go) | 自身 internal/domain, internal/ports, `kernel/outbox`, `pkg/errcode`, `pkg/uid` |
| `cells/config-core/slices/configpublish` (handler.go) | `pkg/httputil` |
| `cells/config-core/slices/configread` (service.go) | 自身 internal/domain, internal/ports |
| `cells/config-core/slices/configread` (handler.go) | `pkg/httputil` |
| `cells/config-core/slices/configsubscribe` (service.go) | `kernel/outbox` |
| `cells/config-core/slices/configwrite` (service.go) | 自身 internal/domain, internal/ports, `kernel/outbox`, `pkg/errcode`, `pkg/uid` |
| `cells/config-core/slices/configwrite` (handler.go) | `pkg/httputil` |
| `cells/config-core/slices/featureflag` (service.go) | 自身 internal/domain, internal/ports, `pkg/errcode` |
| `cells/config-core/slices/featureflag` (handler.go) | `pkg/httputil` |

#### cells/order-cell

| 包 | 内部依赖 |
|---|---------|
| `cells/order-cell` (cell.go) | 自身 internal/domain, internal/mem, slices/*, `kernel/cell`, `kernel/outbox` |
| `cells/order-cell/internal/domain` | (无内部依赖) |
| `cells/order-cell/internal/mem` | 自身 internal/domain, `pkg/errcode` |
| `cells/order-cell/slices/order-create` (service.go) | 自身 internal/domain, `kernel/outbox`, `pkg/errcode`, `pkg/uid` |
| `cells/order-cell/slices/order-create` (handler.go) | `pkg/errcode`, `pkg/httputil` |
| `cells/order-cell/slices/order-query` (service.go) | 自身 internal/domain |
| `cells/order-cell/slices/order-query` (handler.go) | `pkg/httputil` |

#### cells/device-cell

| 包 | 内部依赖 |
|---|---------|
| `cells/device-cell` (cell.go) | 自身 internal/domain, internal/mem, slices/*, `kernel/cell`, `kernel/outbox` |
| `cells/device-cell/internal/domain` | (无内部依赖) |
| `cells/device-cell/internal/mem` | 自身 internal/domain, `pkg/errcode` |
| `cells/device-cell/slices/device-command` (service.go) | 自身 internal/domain, `pkg/errcode`, `pkg/uid` |
| `cells/device-cell/slices/device-command` (handler.go) | `pkg/errcode`, `pkg/httputil` |
| `cells/device-cell/slices/device-register` (service.go) | 自身 internal/domain, `kernel/outbox`, `pkg/errcode`, `pkg/uid` |
| `cells/device-cell/slices/device-register` (handler.go) | `pkg/errcode`, `pkg/httputil` |
| `cells/device-cell/slices/device-status` (service.go) | 自身 internal/domain |
| `cells/device-cell/slices/device-status` (handler.go) | `pkg/httputil` |

**cells 汇总依赖**:
- cells -> kernel/cell, kernel/outbox (合规)
- cells -> pkg/errcode, pkg/httputil, pkg/uid (合规)
- cells -> runtime/auth (合规: cells 允许依赖 runtime)
- cells 不依赖 adapters/ (合规 -- cells 内部的 internal/adapters/ 子包仅依赖自身 domain/ports + pkg)
- **无跨 Cell import** (合规 -- 无 Cell A import Cell B 的情况)

### Layer 5: cmd / examples / root

| 包 | 内部依赖 |
|---|---------|
| `gocell.go` (root) | `kernel/assembly` |
| `cmd/gocell` (main.go) | (无内部依赖 -- 仅标准库) |
| `cmd/gocell` (validate.go) | `kernel/governance`, `kernel/metadata` |
| `cmd/gocell` (check.go) | `kernel/metadata`, `kernel/registry` |
| `cmd/gocell` (generate.go) | `kernel/assembly`, `kernel/metadata` |
| `cmd/gocell` (scaffold.go) | `kernel/scaffold` |
| `cmd/gocell` (verify.go) | `kernel/governance`, `kernel/metadata`, `kernel/slice` |
| `cmd/gocell` (helpers.go) | `kernel/governance` |
| `cmd/core-bundle` (main.go) | `cells/access-core`, `cells/audit-core`, `cells/config-core`, `kernel/assembly`, `runtime/bootstrap`, `runtime/eventbus` |
| `examples/sso-bff` | `cells/access-core`, `cells/audit-core`, `cells/config-core`, `kernel/assembly`, `kernel/outbox`, `runtime/auth`, `runtime/bootstrap`, `runtime/eventbus` |
| `examples/todo-order` | `cells/order-cell`, `kernel/assembly`, `runtime/bootstrap`, `runtime/eventbus` |
| `examples/iot-device` | `cells/device-cell`, `kernel/assembly`, `runtime/bootstrap`, `runtime/eventbus` |

**cmd/examples 汇总**: 可以依赖所有层 (合规)

---

## 分层违规

| # | 源文件 | 违规 import | 规则 | 严重性 | 说明 |
|---|--------|------------|------|--------|------|
| 1 | `adapters/postgres/outbox_relay.go` | `runtime/worker` | adapters/ 不应依赖 runtime/ | P1 | OutboxRelay 实现了 `worker.Worker` 接口, 该接口定义在 runtime/ 层。adapters/ 应只实现 kernel/ 或 runtime/ 定义的接口, 但不应 import runtime/ 包。修复方案: 将 `Worker` 接口提升到 `kernel/` 层, 或在 `adapters/postgres` 内部定义等效接口。 |

**总计: 1 项违规**

### 已确认不是违规的边界情况

| 场景 | 分析 |
|------|------|
| `cells/access-core` -> `runtime/auth` | 合规: CLAUDE.md 明确允许 cells/ 依赖 runtime/ |
| `cells/*/internal/adapters/postgres/` | 合规: 这些是 Cell 内部的 adapter 实现, 位于 `cells/` 目录下, 且不 import 顶层 `adapters/` 包 |
| `runtime/bootstrap` -> `kernel/assembly` | 合规: runtime/ 允许依赖 kernel/ |
| `runtime/http/router` -> `kernel/cell` | 合规: runtime/ 允许依赖 kernel/ |
| `runtime/observability/tracing` -> `pkg/httputil` | 合规: runtime/ 允许依赖 pkg/ |
| `kernel/cell/registrar.go` -> `kernel/outbox` | 合规: kernel 内部交叉依赖 |
| `kernel/governance` -> `kernel/cell`, `kernel/metadata`, `kernel/registry` | 合规: kernel 内部交叉依赖 |

---

## 依赖统计

| 层 | Go 包数 | 非测试 .go 文件数 | 内部依赖边数 (去重) | 外部依赖数 (第三方) |
|----|---------|-------------------|---------------------|---------------------|
| pkg/ | 4 (ctxkeys, errcode, httputil, id, uid) | 9 | 1 (httputil->errcode) | 0 |
| kernel/ | 10 (assembly, assembly/gentpl, cell, governance, idempotency, journey, metadata, metadata/schemas, registry, scaffold, slice) | 25 | 14 | 1 (gopkg.in/yaml.v3) |
| runtime/ | 11 (auth, bootstrap, config, eventbus, http/health, http/middleware, http/router, observability/logging, observability/metrics, observability/tracing, shutdown, worker) | 25 | 18 | 3 (chi, jwt, fsnotify) |
| adapters/ | 6 (oidc, postgres, rabbitmq, redis, s3, websocket) | 27 | 14 (含 1 违规) | 5 (pgx, amqp091, go-redis, nhooyr/websocket, crypto/... for s3) |
| cells/ | 5 顶层 Cell + ~30 子包 | ~50 | ~45 (Cell 内部 + kernel/runtime/pkg) | 1 (golang.org/x/crypto for bcrypt) |
| cmd/ | 2 (gocell, core-bundle) | 9 | 10 | 0 |
| examples/ | 3 (sso-bff, todo-order, iot-device) | 3 | 12 | 0 |
| **总计** | **~70** | **~148** | **~115** | **~10** |

---

## 架构评估

### 1. [分层架构] adapters/postgres/outbox_relay.go 依赖 runtime/worker -- P1

**理由**: `OutboxRelay` 通过 `var _ worker.Worker = (*OutboxRelay)(nil)` 实现了 `runtime/worker.Worker` 接口, 导致 adapters/ 层对 runtime/ 层产生了编译期依赖。这违反了 "adapters/ 实现 kernel/ 或 runtime/ 定义的接口" 的规则中 "adapters 不依赖 runtime" 的隐含约束。

**修复建议**: 将 `Worker` 接口 (`Start(ctx) error` + `Stop(ctx) error`) 提升到 `kernel/worker/` 或作为 `kernel/cell` 的一部分, 然后 runtime/worker.WorkerGroup 和 adapters/postgres 都依赖 kernel 定义的接口。

**影响**: 中 -- 功能正常, 但违反分层原则, 可能导致后续 adapter 层与 runtime 层耦合扩散。

### 2. [接口稳定性] Worker 接口位置不够通用 -- P2 (建议)

**理由**: `worker.Worker` 是一个通用的后台任务抽象, 被 bootstrap、adapters 等多处使用。将其放在 runtime/ 层使得下层 (adapters) 无法实现它而不产生逆向依赖。

### 3. [Cell 聚合边界] 无跨 Cell 直接 import -- 合规确认

所有 5 个 Cell (access-core, audit-core, config-core, order-cell, device-cell) 之间无直接 import, 通信将通过 contract (eventbus) 进行。Cell 内部 adapter 包 (cells/*/internal/adapters/) 也未 import 顶层 adapters/ 包, 而是通过接口解耦。

### 4. [依赖方向] kernel/ 层零违规 -- 合规确认

kernel/ 的全部 10 个子包仅依赖标准库、pkg/, 以及 kernel 内部其他子包。无逆向依赖。

### 5. [依赖方向] runtime/ 层零违规 -- 合规确认

runtime/ 的全部 11 个子包仅依赖标准库、pkg/、kernel/, 以及 runtime 内部其他子包。无���向依赖 (不依赖 cells/ 或 adapters/)。

---

## 附: 完整依赖边列表

以下列出所有非测试 .go 文件中的跨包内部 import, 格式为 `源包 -> 目标包`。

### pkg 内部
```
pkg/httputil -> pkg/errcode
```

### kernel 内部
```
kernel/cell -> pkg/errcode
kernel/cell -> kernel/outbox
kernel/assembly -> kernel/cell, pkg/errcode
kernel/assembly -> kernel/assembly/gentpl, kernel/metadata, kernel/registry, pkg/errcode
kernel/metadata -> pkg/errcode
kernel/governance -> kernel/cell, kernel/metadata, kernel/registry
kernel/journey -> kernel/metadata
kernel/registry -> kernel/metadata
kernel/scaffold -> pkg/errcode
kernel/slice -> kernel/metadata, pkg/errcode
```

### runtime 内部
```
runtime/auth -> pkg/errcode, pkg/ctxkeys
runtime/eventbus -> kernel/outbox, pkg/errcode, pkg/uid
runtime/http/health -> kernel/assembly
runtime/http/middleware -> pkg/ctxkeys
runtime/http/router -> kernel/cell, runtime/http/health, runtime/http/middleware, runtime/observability/metrics
runtime/observability/logging -> pkg/ctxkeys
runtime/observability/tracing -> pkg/ctxkeys, pkg/httputil
runtime/bootstrap -> kernel/assembly, kernel/cell, kernel/outbox, runtime/config, runtime/eventbus, runtime/http/health, runtime/http/router, runtime/shutdown, runtime/worker
```

### adapters -> kernel/pkg
```
adapters/postgres -> kernel/outbox, pkg/errcode, runtime/worker [VIOLATION]
adapters/rabbitmq -> kernel/outbox, kernel/idempotency, pkg/errcode
adapters/redis -> kernel/idempotency, pkg/errcode
adapters/oidc -> pkg/errcode
adapters/s3 -> pkg/errcode
adapters/websocket -> pkg/errcode, pkg/uid
```

### cells -> kernel/runtime/pkg
```
cells/access-core -> kernel/cell, kernel/outbox, pkg/errcode, pkg/httputil, pkg/uid, runtime/auth
cells/audit-core -> kernel/cell, kernel/outbox, pkg/errcode, pkg/httputil, pkg/uid
cells/config-core -> kernel/cell, kernel/outbox, pkg/errcode, pkg/httputil, pkg/uid
cells/order-cell -> kernel/cell, kernel/outbox, pkg/errcode, pkg/httputil, pkg/uid
cells/device-cell -> kernel/cell, kernel/outbox, pkg/errcode, pkg/httputil, pkg/uid
```
