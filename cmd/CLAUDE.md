# cmd/ 层规则

## cmd/gocell vs cmd/corebundle

| | `cmd/gocell` | `cmd/corebundle` |
|---|---|---|
| **角色** | 治理 / 元数据 / 代码生成器 CLI | 运行时组装产物（部署二进制） |
| **使用时机** | dev + CI（构建期、治理期） | 生产部署（运行时） |
| **子命令** | validate / scaffold / generate (含 required-deps / shared-schema 子命令) / check / verify / graph / export / derive-service-keys | —（单一入口，启动 bootstrap） |
| **进入运行时进程** | 否 | 是 |
| **对标定位** | 构建 / 治理工具（类似 `kubectl` / `go generate`） | Composition Root（类似 Uber fx 的 wire-up 入口） |

- **`cmd/gocell`**：治理与元数据 CLI，供 dev 与 CI 调用。执行 validate（contract / cell / slice 声明合规）、scaffold（脚手架）、generate（codegen 契约派生；含 `required-deps` 子命令 — 从 Service struct `gocell:"required"` tag 生成 `service_required_gen.go`，对应 `REQUIRED-DEP-NIL-GUARD-01` funnel；含 `shared-schema` 子命令 — 把 `contracts/shared/errors/error-response-v1.schema.json` 字节恒等派生到每个声明 mirror（目标集以 `tools/codegen/sharedschema.Mirrors` 为准），对应 `SHARED-SCHEMA-MIRROR-FUNNEL-01` funnel）、check（自定义规则检查）、verify（验收规格对齐；含 `codegen-shared-schema` 子命令 — in-process 字节 diff 守护各声明 mirror 与 canonical 同步）、graph（模块包依赖图输出）、export（项目元数据 / 目录导出为 JSON/YAML）、**`derive-service-keys`**（从 master secret 派生 per-cell 签名/验签子密钥，供 split 模式进程注入）。**不进入运行时进程**，不依赖生产外部资源。

- **`cmd/corebundle`**：`assemblies/corebundle/` 的运行时组装产物。负责 bootstrap wiring：加载 SharedDeps（PG / Redis / AMQP / JWT / HMAC 等）、调用 `cellmodules/<cell>.Module()` 构造各 CellModule（平台 cell 业务 wiring 已迁移至 `cellmodules/` 层）、配置三个 HTTP listener（Primary / Internal / Health）、启动 `bootstrap.Run`。**是实际部署运行的二进制**，也是项目唯一的生产 Composition Root。`corebundle-no-cells` depguard 强制 `cmd/corebundle` 不直接 import `cells/`（cellmodules 层负责绑定 cell 与 adapter）。

> 一句话区分：`cmd/gocell` 是构建 / 治理期工具；`cmd/corebundle` 是运行时 Composition Root。

---

cmd/corebundle/ 是 Composition Root，负责组装所有 Cell、配置三个 listener、启动 bootstrap。

## 三层组装模式

```go
// 第一层：环境变量注入 + 模块工厂
// LoadSharedDepsFromEnv 内部经 composition.NewSharedDeps(...) 构造并校验，返回
// 已盖 sealed-construction marker 的 *composition.SharedDeps（裸字面量 Build 会拒）。
shared, _ := LoadSharedDepsFromEnv(ctx)
modules, _ := corebundleModules(assemblyID, assemblyCellIDs)

// 第二层：公开 composition API 组装 cells + bootstrap.Option（失败按 LIFO 回滚资源）。
// RuntimeOptionsFunc 在 cmd 内构造 assembly + 三 listener auth（composition 层禁构造 AuthPlan）。
app, err := composition.New().
    With(modules...).
    Build(ctx, shared, func(cells []cell.Cell) ([]bootstrap.Option, error) {
        asm, err := buildAssembly(shared.PromStack, assemblyID, mode, cells...)
        if err != nil {
            return nil, err
        }
        return defaultRuntimeOptions(shared, asm, consumerBase, metricsHandler, adapterInfo)
    })
if err != nil {
    return fmt.Errorf("composition.Build: %w", err)
}

// 第三层：运行
app.Run(ctx)
```

## Listener 配置

> 自 PR #615（G-10）后，`auth.AuthPlan` / `auth.ListenerAuth` / `auth.NewAuth*` 等
> 符号来自 `github.com/ghbvf/gocell/framework/kernel/auth`（不是 `kernel/cell`）。当同一文件
> 还 import `runtime/auth` 时，把 kernel/auth 别名为 `kauth`：
> `kauth "github.com/ghbvf/gocell/framework/kernel/auth"`。`cell.PrimaryListener` 等 listener
> 引用保留在 `kernel/cell`。

```go
// B2-K-02: composition root 改 error-first；MustNew* 已删除
// Primary：公开 API + JWT（phase4 自动发现 verifier）
jwtAuth, err := auth.NewAuthJWTFromAssembly(asm)
if err != nil {
    return nil, fmt.Errorf("NewAuthJWTFromAssembly: %w", err)
}
bootstrap.WithListener(cell.PrimaryListener, shared.PrimaryHTTPAddr,
    []auth.ListenerAuth{jwtAuth})

// Internal：控制平面 + ServiceToken（HMAC-SHA256 per-cell 子密钥 + replay guard）
// nonce store + per-cell keyring 提升到 composition.SharedDeps（#1410/#2153），auth plan 用
// 同一份已校验的 SharedDeps 字段构造——不要另造 nonce store（否则绕过
// SharedDeps.NonceStore.Kind() 的 control-plane 校验）。
svcTokenAuth, err := auth.NewAuthServiceToken(shared.NonceStore, shared.InternalServiceKeyring)
if err != nil {
    return nil, fmt.Errorf("NewAuthServiceToken: %w", err)
}
bootstrap.WithListener(cell.InternalListener, shared.InternalHTTPAddr,
    []auth.ListenerAuth{svcTokenAuth})

// Health：/healthz /readyz /metrics，显式无认证
bootstrap.WithListener(cell.HealthListener, shared.HealthHTTPAddr,
    []auth.ListenerAuth{auth.AuthNone{}})
```

`authChain` 必须非 nil；显式无认证用 `auth.AuthNone{}`，传 nil 在 phase0 fail-fast。

## CellModule 接口

每个模块实现 `composition.CellModule`，通过单一 `ModuleResult` 返回 Cell +
非资源 bootstrap.Option + ManagedResource（#1420 单源化，取代旧 4 返回值）：

```go
type ModuleResult struct {
    Cell      cell.Cell                        // 构造的 Cell，成功时非 nil
    Opts      []bootstrap.Option               // 非资源 option（如 WithRelay）
    Resources []lifecycle.ManagedResource      // 本模块打开的资源（PG pool / vault…）
}

type CellModule interface {
    ID() string
    Provide(ctx context.Context, shared *SharedDeps) (ModuleResult, error)
}
```

资源**只**放进 `Resources`，模块自身**不**调 `bootstrap.WithManagedResource`
（由 `WITHMANAGEDRESOURCE-CELLMODULE-FUNNEL-01` type-aware 守卫）；`Builder.Build`
从 `Resources` 一处同时派生稳态注册（`WithManagedResource`）与 pre-Run rollback
栈，两条生命周期通道不可能漂移（#1420 收口前的双写 bug）。

Wave-1 #1423 删除了跨 module value handoff（`ModuleExports` + `in` 参数）；
跨 cell 通信改为 event（contract-based）。不再经可变 `*SharedDeps` 字段或
`ModuleExports` 做 mid-Build handoff。

参考实现：`cellmodules/{accesscore,auditcore,configcore}/module.go`。

## SharedDeps 关键字段

| 字段 | 说明 |
|------|------|
| `JWTDeps` | issuer + verifier（JWT 签发/验证） |
| `InternalServiceKeyring` | /internal/v1/* service-token per-cell keyring（接口，monolith=master 派生 / split=ProvisionedKeyring），#2153 |
| `NonceStore` | /internal/v1/* 服务令牌防重放 store；control-plane 校验经 `Kind()` 拒 noop / 多 pod in-memory（#1410） |
| `PG` | sealed `capability.PGSet`（per-cell 路由层，`ForCell(cellID)` 解析本 cell 的 provider，`Sole()` 仅 colocated 返回唯一 provider），由 `cellmodules/percellpg.Resolve` 在 postgres 拓扑下注入；memory 拓扑下为 nil，cell module 走 in-memory 路径 |
| `ConsumerClaimer` | outbox 消费幂等键声明者；`Kind()` 自报 in_memory/distributed（#1410，CP8 fail-closed） |
| `Publisher` / `Subscriber` | outbox 事件传输（relay 发布 sink + consumer 订阅源）；接口型，经 `cellmodules/eventtransport.Resolve` 按 Topology 选型——demo=in-memory，postgres=真实 broker（RabbitMQ），缺 broker fail-closed（#1940） |
| `PrimaryHTTPAddr` / `InternalHTTPAddr` / `HealthHTTPAddr` | 三 listener 绑定地址 |

## 环境变量（关键）

| 变量 | 说明 | 缺失行为 |
|------|------|---------|
| `GOCELL_JWT_ISSUER` | JWT iss claim | fail-fast |
| `GOCELL_SERVICE_SECRET` | /internal/v1/* HMAC master secret（≥32 字节）；**master 模式（monolith）**，与 split provisioned env 互斥（皆设或皆缺均 fail-fast）。split 模式 per-cell env（`GOCELL_SERVICE_CELL` / `GOCELL_SERVICE_SIGNING_KEY` / `GOCELL_SERVICE_VERIFY_KEYS`）见 `docs/ops/env-vars.md` §Service Token | fail-fast |
| `GOCELL_ACCESSCORE_IP_HASH_SALT` | bootstrap-failed 事件 client-IP keyed-hash salt（≥32 字节，#1488） | real 模式 fail-fast（缺失/demo key/<32B） |
| `GOCELL_ADAPTER_MODE` | 适配器模式：`""`（dev，默认）/ `real` | — |
| `GOCELL_CELL_ADAPTER_MODE` | 存储后端：`memory`（默认）/ `postgres`（`postgres` 经 Topology 耦合规则强制要求 `GOCELL_ADAPTER_MODE=real`） | — |
| `GOCELL_<CELLID>_DATABASE_URL` | 每个 postgres cell 的 DSN（#1964 per-cell PG seam）。postgres 拓扑下 `generatedPostgresCells()` 中的每个 cell（accesscore / auditcore / configcore）均须设置；缺失任一 → fail-fast 报告 cell 名 + 期望的 env var。colocated 部署时三个 cell 设同一 DSN，`percellpg.Resolve` dedup 后只开 1 个 pool + 1 个 relay，keyed by `DefaultInstanceKey()`；split 部署时 >1 个不同 DSN，每个 distinct DSN 开 1 个 pool + 1 个 relay，keyed by `NewInfraInstanceKey(rep)`（rep = 该 DSN 组内字母序首 cell ID）（#2341 落地） | postgres 拓扑 fail-fast |
| `GOCELL_<CELLID>_AMQP_URL` | 每个 broker cell 的 per-cell RabbitMQ URL（#2152 PR-2 per-cell broker seam）。postgres 拓扑下对 `generatedPostgresCells()` 每个 cell 读取，缺失回退 `GOCELL_AMQP_URL`；`eventtransport.dedupBrokerURL` 去重。共址部署留空、只设 `GOCELL_AMQP_URL`（dedup 后只开一个连接）；>1 个不同 URL → fail-closed（egress-only：单 subscriber 无法消费 N broker，真 N-broker fan-out 需 ingress N-router #2366；注：per-cell DB pool fan-out #2341 已落地，但不解除此 broker 闸） | 可选（回退 `GOCELL_AMQP_URL`） |
| `GOCELL_AMQP_URL` | postgres 拓扑的 RabbitMQ broker URL（事件传输 publisher/subscriber，#1940）；per-cell `GOCELL_<CELLID>_AMQP_URL` 的 assembly 级回退；demo 拓扑不读 | postgres 拓扑 fail-fast（broker cell 既无 per-cell URL 也无此回退即启动期报错，不静默降级回 in-memory） |
