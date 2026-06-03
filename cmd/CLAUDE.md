# cmd/ 层规则

## cmd/gocell vs cmd/corebundle

| | `cmd/gocell` | `cmd/corebundle` |
|---|---|---|
| **角色** | 治理 / 元数据 / 代码生成器 CLI | 运行时组装产物（部署二进制） |
| **使用时机** | dev + CI（构建期、治理期） | 生产部署（运行时） |
| **子命令** | validate / scaffold / generate (含 required-deps / shared-schema 子命令) / check / verify / graph / export | —（单一入口，启动 bootstrap） |
| **进入运行时进程** | 否 | 是 |
| **对标定位** | 构建 / 治理工具（类似 `kubectl` / `go generate`） | Composition Root（类似 Uber fx 的 wire-up 入口） |

- **`cmd/gocell`**：治理与元数据 CLI，供 dev 与 CI 调用。执行 validate（contract / cell / slice 声明合规）、scaffold（脚手架）、generate（codegen 契约派生；含 `required-deps` 子命令 — 从 Service struct `gocell:"required"` tag 生成 `service_required_gen.go`，对应 `REQUIRED-DEP-NIL-GUARD-01` funnel；含 `shared-schema` 子命令 — 把 `contracts/shared/errors/error-response-v1.schema.json` 字节恒等派生到 3 个 mirror，对应 `SHARED-SCHEMA-MIRROR-FUNNEL-01` funnel）、check（自定义规则检查）、verify（验收规格对齐；含 `codegen-shared-schema` 子命令 — in-process 字节 diff 守护 3 个 mirror 与 canonical 同步）、graph（模块包依赖图输出）、export（项目元数据 / 目录导出为 JSON/YAML）。**不进入运行时进程**，不依赖生产外部资源。

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
> 符号来自 `github.com/ghbvf/gocell/kernel/auth`（不是 `kernel/cell`）。当同一文件
> 还 import `runtime/auth` 时，把 kernel/auth 别名为 `kauth`：
> `kauth "github.com/ghbvf/gocell/kernel/auth"`。`cell.PrimaryListener` 等 listener
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

// Internal：控制平面 + ServiceToken（HMAC-SHA256 + replay guard）
// nonce store + HMAC ring 提升到 composition.SharedDeps（#1410），auth plan 用
// 同一份已校验的 SharedDeps 字段构造——不要另造 nonce store（否则绕过
// SharedDeps.NonceStore.Kind() 的 control-plane 校验）。
svcTokenAuth, err := auth.NewAuthServiceToken(shared.NonceStore, shared.InternalHMACRing)
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
| `InternalHMACRing` | /internal/v1/* service-token HMAC ring（#1410 起独立字段，原 `internalGuard` 已 dissolve） |
| `NonceStore` | /internal/v1/* 服务令牌防重放 store；control-plane 校验经 `Kind()` 拒 noop / 多 pod in-memory（#1410） |
| `SharedPGPool` | postgres 连接池（跨 Cell 共享） |
| `ConsumerClaimer` | outbox 消费幂等键声明者；`Kind()` 自报 in_memory/distributed（#1410，CP8 fail-closed） |
| `PrimaryHTTPAddr` / `InternalHTTPAddr` / `HealthHTTPAddr` | 三 listener 绑定地址 |

## 环境变量（关键）

| 变量 | 说明 | 缺失行为 |
|------|------|---------|
| `GOCELL_JWT_ISSUER` | JWT iss claim | fail-fast |
| `GOCELL_SERVICE_SECRET` | /internal/v1/* HMAC 密钥（≥32 字节） | fail-fast |
| `GOCELL_ADAPTER_MODE` | 适配器模式：`""`（dev，默认）/ `real` | — |
| `GOCELL_CELL_ADAPTER_MODE` | 存储后端：`memory`（默认）/ `postgres`（`postgres` 经 Topology 耦合规则强制要求 `GOCELL_ADAPTER_MODE=real`） | — |
