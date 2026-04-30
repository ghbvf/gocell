# GoCell 最终形态能力讲解（E1-E10 + E14 完成后）

> 日期：2026-04-30
> 状态：**目标形态描述**，等 E1-E10 + E14 落地后即为 v1.0 实际能力
> 受众：(a) 评估是否引入 GoCell 的工程团队；(b) 在 Go 社区内向「重框架嫌弃」群体讲清楚 GoCell 价值；(c) GoCell 自身贡献者理解最终承诺
> 关联：
> - `202604300600-radical-lightweight-revision.md`（核心 10 落点 E1-E10，已承诺）
> - `202604300700-extension-leverages.md`（E14 库化承诺路径）
> - `../ci-governance/`（lint/CI 治理已完成研究）

---

## §1 Elevator Pitch（30 秒版本）

GoCell 是 Go 的 Cell-native 工程底座，**双形态可用**：

- **重 framework 形态**：cells/ + slices/ + contracts/ + assemblies/ 强结构化组织代码，bootstrap 一行启动；codegen 消化所有样板（cell/slice yaml + DTO + service interface 全自动生成），开发者只写 service.go 业务逻辑
- **轻库形态**：25 个独立可用包（errcode / config / outbox / idempotency / distlock / observability / migrator 等），可被任何 Go 项目直接 `import`，零 GoCell framework 依赖

**核心承诺**：代码不变，部署形态由 assembly 决定（单进程 / 微服务 / 设备端）；不引入 IoC 反射或注解魔法；架构边界由 archtest LAYER + boundary.yaml fingerprint 静态守护。

---

## §2 双形态总览

```
┌─────────────────────────────────────────────────────────────────────┐
│                      GoCell 仓库（单 go.mod）                        │
│                                                                      │
│  ┌─────────────────────────────────┐  ┌──────────────────────────┐  │
│  │   重 framework 形态              │  │   轻库形态（25 个包）     │  │
│  │   ─────────────────              │  │   ─────────────          │  │
│  │   入口：bootstrap.Run(ctx)        │  │   入口：import path 直接  │  │
│  │   组织：cells/ + slices/          │  │                          │  │
│  │   契约：contracts/ + assembly     │  │   Tier A 通用工具:        │  │
│  │   生成：cell_gen.go +              │  │   - pkg/errcode          │  │
│  │         slice_gen.go +            │  │   - pkg/httputil         │  │
│  │         types.go (E10) +          │  │   - pkg/ctxkeys          │  │
│  │         iface.go (E10)            │  │   - pkg/query            │  │
│  │   验证：gocell validate            │  │   - pkg/secutil          │  │
│  │   可视化：gocell visualize         │  │                          │  │
│  │   样板：开发者只写 service.go      │  │   Tier B 独立中间件:       │  │
│  │                                   │  │   - kernel/idempotency   │  │
│  │   类比：go-zero / Kratos          │  │   - kernel/outbox        │  │
│  │                                   │  │   - runtime/config       │  │
│  │                                   │  │   - runtime/distlock     │  │
│  │                                   │  │   - runtime/eventbus     │  │
│  │                                   │  │   - runtime/outbox       │  │
│  │                                   │  │   - runtime/worker       │  │
│  │                                   │  │   - runtime/shutdown     │  │
│  │                                   │  │   - runtime/websocket    │  │
│  │                                   │  │   - runtime/observability│  │
│  │                                   │  │     /{logging,metrics,   │  │
│  │                                   │  │      tracing,poolstats}  │  │
│  │                                   │  │   - adapters/postgres/   │  │
│  │                                   │  │     {pool,migrator}      │  │
│  │                                   │  │                          │  │
│  │                                   │  │   类比：uber/multierr +   │  │
│  │                                   │  │   sourcegraph/conc +     │  │
│  │                                   │  │   chi/middleware 等独立 │  │
│  └─────────────────────────────────┘  └──────────────────────────┘  │
│                                                                      │
│  共享：单 go.mod / archtest LAYER 守卫 / SemVer 守 API（v1.0+）      │
└─────────────────────────────────────────────────────────────────────┘
```

**关键点**：两形态共用单 go.mod 和单 archtest LAYER 守卫体系——这避免了多 repo 拆分带来的复杂度（CloudWeGo 也是单 org 多模块但有协同 release 成本，GoCell 走更简的单 go.mod 路线）。

---

## §3 重 Framework 形态能力清单

### 3.1 开发者写 hello world 实际操作

```bash
# 一键 scaffold（E9）
$ gocell new-cell hello --type=core --level=L0 --with-http
✓ cells/hello/cell.go (with marker comments)
✓ cells/hello/slices/greet/service.go
✓ cells/hello/slices/greet/service_test.go
✓ contracts/http/hello/greet/v1/contract.yaml
✓ generated cell_gen.go + slice_gen.go (auto via E3)
✓ generated contracts/.../types.go + iface.go (auto via E10)
✓ go test ./cells/hello/... PASS

# 跑一下
$ go run cmd/corebundle/main.go
INFO bootstrap phase0 validate ...
INFO bootstrap phase5 mount route POST /api/v1/hello/greet
INFO bootstrap ready

$ curl -X POST localhost:8080/api/v1/hello/greet -d '{"name":"world"}'
{"greeting":"Hello, world"}
```

**手写文件清单**（E9 scaffold 后开发者只动这些）：
- `cells/hello/slices/greet/service.go` — 业务逻辑（~25 行）
- `contracts/http/hello/greet/v1/contract.yaml` — API 契约
- `assemblies/corebundle/assembly.yaml` — 加 `cells: [..., hello]` 一行

**Generated 文件清单**（机器产出，CI 守护一致性）：
- `cells/hello/cell_gen.go` — Cell skeleton + Init/Start/Stop + 注册调用
- `cells/hello/slices/greet/slice_gen.go` — handler 桩 + 路由声明
- `contracts/http/hello/greet/v1/types.go` — DTO struct
- `contracts/http/hello/greet/v1/iface.go` — service interface
- `contracts/http/hello/greet/v1/errors.go` — errcode 常量

### 3.2 启动模式

**单一入口（默认）**：
```go
// cmd/corebundle/main.go
func main() {
    ctx, cancel := shutdown.NotifyContext(...)
    defer cancel()
    must(bootstrap.Run(ctx, "config.yaml"))  // 一行
}
```

**显式 phase 模式（E8 后可选）**：
```go
func main() {
    ctx, cancel := shutdown.NotifyContext(...)
    defer cancel()

    cfg := must(bootstrap.LoadConfig(ctx, "config.yaml"))
    asm := buildAssembly(cfg)
    must(bootstrap.ValidateAssembly(ctx, asm))
    eb := bootstrap.NewEventBus(cfg.PubSub)
    must(bootstrap.StartCells(ctx, asm, bootstrap.WithEventBus(eb)))
    listeners := bootstrap.MountRoutes(asm, cfg.Listeners)
    workers := bootstrap.StartWorkers(ctx, asm)
    err := bootstrap.WaitShutdown(ctx, listeners, workers)
    bootstrap.TeardownLIFO(ctx, asm, listeners, workers)
    if err != nil { os.Exit(1) }
}
```

两种风格都被官方支持。前者最少代码，后者最大可读性 + 可裁剪。

### 3.3 Cell 注册（E2 后唯一显式 API）

```go
// cells/hello/cell.go（marker + struct embed 表达元数据）
//
// +gocell:cell:id=hello
// +gocell:cell:verify.smoke=cmd/gocell/check/hello_smoke.sh
package hello

type Hello struct {
    cell.SupportCell      // type=support（embed 表达）
    cell.LocalOnlyLevel   // consistencyLevel=L0（embed 表达）
    svc *greetSvc
}

func (c *Hello) Init(ctx context.Context, reg cell.Registry) error {
    // 一眼可见所有注册（E2 Registry API）：
    reg.Routes(greetRoutes(c.svc)...)
    reg.Health("hello_ready", c.checkReady)
    return nil
}

func (c *Hello) Start(ctx context.Context) error { return nil }
func (c *Hello) Stop(ctx context.Context) error  { return nil }
```

**对比**：原 5 个 framework-defined contributor interface（RouteGroupContributor / EventRegistrar / HealthContributor / LifecycleContributor / ConfigReloader）+ bootstrap type assertion 自动发现，全部消失。开发者只看 `Init(reg)` 一段就知道 cell 注册了什么。

### 3.4 Service 实现（E10 后只填业务逻辑）

```go
// cells/hello/slices/greet/service.go
type Service struct{}

// 编译期断言：实现 generated interface
var _ contracts.GreetService = (*Service)(nil)

func (s *Service) Greet(ctx context.Context, req *contracts.GreetRequest) (*contracts.GreetResponse, error) {
    if req.Name == "" {
        return nil, errcode.New(errcode.ErrValidationRequired, "name is required")
    }
    return &contracts.GreetResponse{
        Greeting: fmt.Sprintf("Hello, %s", req.Name),
    }, nil
}
```

DTO struct（GreetRequest / GreetResponse）+ service interface（GreetService）+ errcode 常量（ErrValidationRequired）全部由 contract.yaml codegen，不写。

### 3.5 多形态部署能力

同一份 cells/ 代码，不同 assembly 决定不同打包形态：

```yaml
# assemblies/monolith/assembly.yaml — 单进程 monolith
id: monolith
cells: [accesscore, auditcore, configcore, hello]
```

```yaml
# assemblies/access-microservice/assembly.yaml — 仅 accesscore 微服务
id: access-ms
cells: [accesscore]
```

```yaml
# assemblies/edge-device/assembly.yaml — 设备端
id: edge
cells: [hello]
deployTemplate:
  kind: device
```

`gocell generate` 各自产出 boundary.yaml + 部署模板。**这是吸收 Service Weaver 「以单体编码、以微服务部署」理念，但用显式 yaml 而非反射**。

### 3.6 治理工具链

| 工具 | 作用 | 何时跑 |
|---|---|---|
| `gocell validate` | 元数据 schema + ADV-06 双向校验 + FMT-* 命名规则 | 每次保存 + CI |
| `gocell generate` | cell_gen.go + slice_gen.go + types.go + iface.go + boundary.yaml + metrics-schema.yaml | scaffold 后 + 改 contract 后 |
| `gocell visualize` | DOT/SVG/mermaid 依赖图 | PR review 附图 + 文档 |
| `gocell verify` | 跑各 cell smoke + journey | CI |
| `gocell scaffold` / `new-cell` / `new-slice` | 一键创建骨架 | 新增功能 |
| `make verify-codegen` | git worktree 沙箱重跑 generate + git status 计数 | CI hard gate |

### 3.7 运行时能力清单（开箱即用）

- **多 listener 分流**：3 个 listener（public / internal / metrics）自动区分，路由按 contract 类型挂到对应 listener
- **路由 + 中间件**：chi.Mux + auth chain 编译，错误统一映射 HTTP 状态码（pkg/httputil）
- **事件总线**：In-memory（demo）/ RabbitMQ / Kafka 三种 driver，consumer 模式默认两阶段幂等
- **Outbox**：Transactional outbox 内置，emitter + relay + subscriber 全套
- **Lifecycle**：10-phase 启动 + LIFO teardown，OnStart 失败对称清理保证
- **Config 热更新**：fsnotify + ConfigMap symlink pivot 检测，无需重启
- **健康检查**：`/livez` + `/readyz`，cell 注册的所有 health checker 聚合，readyz 翻转 → drain
- **可观测性**：OTel trace + slog 结构化日志 + Prometheus 指标，metrics-schema.yaml 守护 label cardinality
- **认证**：JWT 签发 + 验证 + key rotation
- **持久化**：Postgres + advisory lock migration（goose），Redis cache，S3 blob
- **强结构化守护**：archtest LAYER（11 条规则）+ boundary.yaml fingerprint diff gate

---

## §4 轻库形态能力地图（25 个独立包）

外部 Go 项目可直接 `import` 任何包，**无需引入 GoCell framework 本身**。单 go.mod 设计意味着 import 不会拖入 cell/assembly/bootstrap 等 framework 类型（archtest 守护）。

### Tier A — 通用工具（pkg/，5 包）

| 包 | 能力 | 类比 |
|---|---|---|
| `pkg/errcode` | 结构化错误（80+ Code + Category + PII-safe + HTTP 映射） | cockroachdb/errors（轻量版） |
| `pkg/httputil` | JSON 编解码 + cursor pagination + 5xx mask | 自研，stdlib + 一些 helper |
| `pkg/ctxkeys` | request_id / trace_id / span_id / correlation_id 上下文 key | google/uuid + ctx propagation |
| `pkg/query` | DB query builder helpers | sqlc / squirrel 的轻量替代 |
| `pkg/secutil` | TLS endpoint validation + 密码学 helpers | 自研 |

### Tier B — runtime 完全独立子包（runtime/，13 包，零 framework 契约依赖）

| 包 | 能力 | 类比 |
|---|---|---|
| `runtime/config` | 配置加载 + fsnotify 热更新 + ConfigMap symlink pivot | spf13/viper（轻量替代） |
| `runtime/crypto` | 密码学辅助 | crypto/* stdlib + helper |
| `runtime/distlock` | 分布式锁原语（Redis/PG advisory） | redsync/redsync |
| `runtime/eventbus` | In-memory pub/sub | watermill 的极简版 |
| `runtime/outbox` | Transactional outbox 实现 | watermill outbox |
| `runtime/shutdown` | NotifyContext + signal handling | sourcegraph/conc 部分 |
| `runtime/websocket` | WebSocket helpers | nhooyr.io/websocket 包装 |
| `runtime/worker` | Worker pool | sourcegraph/conc / errgroup |
| `runtime/observability/logging` | slog 结构化日志包装 | log/slog stdlib + helper |
| `runtime/observability/metrics` | Provider 接口抽象 | prometheus client_golang 包装 |
| `runtime/observability/poolstats` | DB Pool 统计指标 | 自研 |
| `runtime/observability/tracing` | OTel adapter（B3 propagator） | go.opentelemetry.io/otel 包装 |
| `runtime/http/healthtest` | health 测试辅助 | 自研 |

### Tier B — kernel/ + adapters/ 中可独立的（7 包，E1+E2 完成后自然解耦）

| 包 | 能力 | 类比 |
|---|---|---|
| `kernel/idempotency` | Claimer/Receipt 两阶段幂等控制 | 无直接对标，业界论文模式 |
| `kernel/outbox` | Transactional outbox 接口定义 | watermill outbox interface |
| `kernel/persistence/tx` | TxRunner 接口 | database/sql.Tx 包装 |
| `kernel/observability/metrics` | Provider 抽象 | OpenMetrics SDK pattern |
| `kernel/metricschema` | OBS-01 typed gate（go/types + AST） | golang.org/x/tools/go/analysis 风格 |
| `adapters/postgres/migrator` | goose wrapper + invalid-index 前置检测 | pressly/goose 包装 |
| `adapters/postgres/pool` | Pool + PoolStats + 健康检查 | jackc/pgx 包装 |

### 4.1 import 示例

任何 Go 项目（不一定是 cell-native）：

```go
// 一个普通 Go HTTP server，只用 GoCell 的 errcode + httputil + idempotency
package main

import (
    "net/http"
    "github.com/ghbvf/gocell/pkg/errcode"
    "github.com/ghbvf/gocell/pkg/httputil"
    "github.com/ghbvf/gocell/kernel/idempotency"
)

func main() {
    claimer := idempotency.NewMemoryClaimer()
    http.HandleFunc("/order", func(w http.ResponseWriter, r *http.Request) {
        receipt, err := claimer.Claim(r.Context(), r.Header.Get("Idempotency-Key"))
        if err != nil {
            httputil.WriteError(w, errcode.Wrap(err, errcode.ErrInternal, "claim failed"))
            return
        }
        // ... 业务逻辑
        receipt.Commit(r.Context())
        httputil.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
    })
    http.ListenAndServe(":8080", nil)
}
```

零 GoCell framework 依赖，零 cell.yaml / contract.yaml / archtest LAYER 约束。**和用 chi + uber/multierr 做组合一样自然**。

---

## §5 选型决策流

```
                    ┌──────────────────────────────┐
                    │   你要建什么？                │
                    └───────────────┬──────────────┘
                                    │
            ┌───────────────────────┼───────────────────────┐
            ▼                       ▼                       ▼
┌─────────────────────┐  ┌─────────────────────┐  ┌─────────────────────┐
│ 多服务 cell-native   │  │ 已有 Go 项目，需      │  │ 单服务 + 想要         │
│ 后端平台（含治理）   │  │ 某个特定能力（幂等   │  │ 强结构化但不需要     │
│                     │  │ /配置热更/outbox/    │  │ 多形态部署           │
│                     │  │ migration/...）      │  │                      │
└──────────┬──────────┘  └──────────┬──────────┘  └──────────┬──────────┘
           ▼                        ▼                        ▼
    重 framework 形态         轻库形态                混合形态
    用 cells/ + bootstrap    单包 import             用 cells/ 但跳过
    + codegen 全套           零 framework 约束       一些 contracts/journeys
                                                     治理（按需取治理强度）
```

**典型场景**：

1. **大型多 cell 平台**（IAM + 审计 + 配置管理 + ...）→ 重 framework 形态
2. **单功能 SaaS**（如 webhook 服务）→ 轻库形态：用 errcode + httputil + idempotency + outbox 4 包，main.go 全显式
3. **设备端 / IoT**（资源受限）→ 重 framework 形态 + assembly type=device，部分模块裁剪
4. **遗留代码改造**（已有 chi/Gin 项目）→ 轻库形态渐进引入：先用 errcode 替换 errors.New，再用 idempotency 替换手写幂等，逐步评估是否引入 framework

---

## §6 谱系定位（CloudWeGo 模式的变体）

```
重 framework 体感（用全套 cells/ + bootstrap）：

  轻库 ── chi/Gin ── Spine ── Kratos ── GoCell ── go-zero ── K8s operator ── Rails
                                          ↑
                              位于 Kratos 与 go-zero 之间
                              （IoC 5-10% / 入场 5-10 分钟 / 比 net/http 重 ~5×）

轻库体感（只 import 25 个独立包之一或几个）：

  位置：与 chi/Gin、uber/multierr、sourcegraph/conc、jackc/pgx 等 Go 社区独立 library 同档
       完全没有「框架感」，与「import 一个 helper 包」无异
```

**最贴切的双形态类比**：

| 项目 | Framework 部分 | Library 矩阵 |
|---|---|---|
| **CloudWeGo** | Kitex（RPC 框架） | Netpoll / sonic / volo-rs / ... |
| **GoCell** | cells/ + bootstrap + assembly | pkg/* + runtime/* 25 个独立包 |
| **Watermill** | Router + Subscriber 编排 | message + middleware 单包可独立 |
| **uber-go monorepo** | — | fx / dig / multierr / atomic / zap 各自独立 library |

GoCell 的特殊点：**framework 形态承担「Cell 治理 + 多形态部署」承诺，library 形态共享同 go.mod 但通过 archtest LAYER 守护互不污染**。

---

## §7 与 Go 社区哲学的和解

文档 `docs/architecture/positioning.md`（E5）必须明确这点。三个常见挑战的回应：

### 挑战 1：「Go 偏爱库不要框架」
- **回应**：GoCell 双形态，重 framework 是为治理需求服务，轻库是为单功能服务；用户按需选
- **不假装**：承认 framework 形态比 chi/Gin 重 ~5×，但 codegen 消化样板后入场摩擦同 Kratos

### 挑战 2：「Go 推崇显式依赖，反 IoC」
- **回应**：GoCell 70-90% 显式装配（main.go 一眼追踪），E2 后剩 5-10% IoC（仅 phase 编排本身）；不用反射、不用 init() 注册、不用 dig
- **不假装**：承认 5-10% IoC 仍存在；但这是 type assertion 模式，可读 + 可编译期检查

### 挑战 3：「Go 反对抽象过载（Clean Arch 水土不服）」
- **回应**：GoCell 一级 6 词（Cell/Slice/Contract/Journey/Actor/L0-L4）有真实业务对应物（与 K8s Pod / DDD Bounded Context 同源），不是 Java 层名移植
- **不假装**：承认 hello world 11+ 概念多于 net/http 3，但 E10 后 codegen 消化大半，开发者实际接触 5-6

---

## §8 API 稳定性承诺（v1.0 后）

| 层级 | 稳定性承诺 | SemVer 边界 |
|---|---|---|
| `pkg/*` | **稳定**：v1.0 后按 SemVer，破坏改动需要 major bump | 强 |
| `runtime/*`（Tier B 13 个独立子包） | **稳定** | 强 |
| `kernel/idempotency / outbox / persistence / observability / metricschema` | **稳定** | 强 |
| `adapters/postgres/{pool,migrator}` | **稳定** | 强 |
| `kernel/cell / metadata / assembly`（framework 契约） | **稳定** | 强（破坏需要 v2） |
| `runtime/bootstrap / auth / command / eventrouter / http/health,middleware,router`（framework 强耦合） | **稳定** | 强 |
| `cmd/gocell` 工具 CLI flag | **半稳定**：参数可重命名（提供 deprecation alias），输出格式按 SemVer | 中 |
| `cells/* / contracts/* / assemblies/*` 示例 cells | **不承诺稳定**（示例是参考实现，可能整改） | 无 |

---

## §9 升级路径（轻库 → 重 framework）

GoCell 双形态自然支持渐进式采用：

```
Phase 0：仅引 pkg/errcode + pkg/httputil 改造已有 HTTP server
        （0 GoCell framework 痕迹）
            ↓
Phase 1：加 runtime/idempotency + runtime/outbox 处理消息幂等
        （仍 0 framework 痕迹）
            ↓
Phase 2：加 runtime/observability/* 集成 OTel + slog + metrics
        （仍 0 framework 痕迹）
            ↓
Phase 3：第一个新功能用 cell-native 写（gocell new-cell）
        （重 framework 形态登场，但与 Phase 0-2 共存）
            ↓
Phase 4：逐步把 Phase 0-2 的非 cell 代码迁移到 cells/
        （或保留为 cells/<x>/internal/，按 archtest LAYER 守卫）
            ↓
Phase 5：assembly.yaml 决定单体 / 微服务 / 设备端部署形态
        （Service Weaver 风味卖点开花）
```

每个 phase 都可以停下来稳定运行，不强制升级到下一阶段。

---

## §10 Hello World 完整代码（E1-E10 + E14 后）

供工程团队评估时直接对照：

```bash
# 1. 一键创建
$ gocell new-cell hello --type=support --level=L0 --with-http
```

`cells/hello/cell.go`（开发者修改 marker，剩下 cell_gen.go 自动）：
```go
// +gocell:cell:id=hello
// +gocell:cell:verify.smoke=cmd/gocell/check/hello_smoke.sh
package hello

type Hello struct {
    cell.SupportCell
    cell.LocalOnlyLevel
    svc *Service
}

func (c *Hello) Init(ctx context.Context, reg cell.Registry) error {
    reg.Routes(greetRoutes(c.svc)...)
    reg.Health("hello_ready", c.svc.Ready)
    return nil
}
func (c *Hello) Start(ctx context.Context) error { return nil }
func (c *Hello) Stop(ctx context.Context) error  { return nil }
```

`cells/hello/slices/greet/service.go`（开发者只填业务逻辑）：
```go
package greet

import (
    "context"
    "fmt"
    "github.com/ghbvf/gocell/contracts/http/hello/greet/v1"
    "github.com/ghbvf/gocell/pkg/errcode"
)

type Service struct{}
var _ contracts.GreetService = (*Service)(nil)

func (s *Service) Greet(ctx context.Context, req *contracts.GreetRequest) (*contracts.GreetResponse, error) {
    if req.Name == "" {
        return nil, errcode.New(errcode.ErrValidationRequired, "name is required")
    }
    return &contracts.GreetResponse{Greeting: fmt.Sprintf("Hello, %s", req.Name)}, nil
}

func (s *Service) Ready() bool { return true }
```

`contracts/http/hello/greet/v1/contract.yaml`（开发者写）：
```yaml
kind: http
endpoints:
  POST /api/v1/hello/greet:
    request:
      schema: { type: object, required: [name], properties: { name: { type: string } } }
    response:
      200:
        schema: { type: object, properties: { greeting: { type: string } } }
errors:
  - code: ERR_VALIDATION_REQUIRED
```

**总手写：1 个 Go 文件（service.go ~25 行）+ 1 个 cell.go（marker + Init 共 ~15 行）+ 1 个 contract.yaml（~15 行）= 3 文件 / ~55 行**

加 `assemblies/<x>/assembly.yaml` 加一行 `cells: [..., hello]` 就跑起来。

**对比当前 GoCell**：7 文件 / ~220 行 / 11+ 概念（见摩擦 1 grep 实证）。

**对比 net/http**：1 文件 / ~12 行 / 3 概念。

GoCell 介于两者之间但非常靠近 net/http 端（~5×）—— 这正是「重 framework + 轻库矩阵」组合的真实代价。

---

## §11 总结

E1-E10 + E14 完成后 GoCell 是：

1. **双形态产品**，不是单一 framework
2. 重 framework 形态等价于 **Kratos + go-zero 中间档**（位于 Kratos 偏左，因为入场摩擦更小）
3. 轻库形态等价于 **chi/uber/sourcegraph 等独立 library 同档**
4. 共享单 go.mod + archtest LAYER 守卫，避免多 repo 协同复杂度
5. **核心承诺**：代码不变，部署形态由 assembly 决定；强结构化由 archtest 守卫；不引入 IoC 反射或注解魔法
6. 类比：**CloudWeGo 模式**（重 framework + 大量独立 library 矩阵）

**对外讲解一句话**：GoCell 是 Cell-native Go 工程底座，提供重 framework 形态用于大型多 cell 平台 + 25 个独立可用包用于单功能 / 渐进式采用。
