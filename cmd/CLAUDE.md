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

- **`cmd/corebundle`**：`assemblies/corebundle/` 的运行时组装产物。负责 bootstrap wiring：加载 SharedDeps（PG / Redis / AMQP / JWT / HMAC 等）、调用 `platform/<cell>.Module()` 构造各 CellModule（平台 cell 业务 wiring 已迁移至 `platform/` 层）、配置三个 HTTP listener（Primary / Internal / Health）、启动 `bootstrap.Run`。**是实际部署运行的二进制**，也是项目唯一的生产 Composition Root。`corebundle-no-cells` depguard 强制 `cmd/corebundle` 不直接 import `cells/`（platform 层负责绑定 cell 与 adapter）。

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
svcTokenAuth, err := auth.NewAuthServiceToken(guard.NonceStore(), guard.Ring())
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

每个模块实现 `composition.CellModule`，提供 Cell + 向下游模块传递的 typed
`ModuleExports` + bootstrap.Option + ManagedResource：

```go
type CellModule interface {
    ID() string
    Provide(ctx context.Context, shared *SharedDeps, in ModuleExports) (
        cell.Cell, ModuleExports, []bootstrap.Option, []lifecycle.ManagedResource, error)
}
```

`in` 是先注册模块的累积导出；返回值 `ModuleExports` 是本模块向下游交付的 typed
值（如 auditcore 产出 `BootstrapLedgerStore`，accesscore 经 `in` 消费并在缺失时
fail-fast）。不再经可变 `*SharedDeps` 字段做 mid-Build handoff。

参考实现：`cellmodules/{accesscore,auditcore,configcore}/module.go`。

## SharedDeps 关键字段

| 字段 | 说明 |
|------|------|
| `JWTDeps` | issuer + verifier（JWT 签发/验证） |
| `InternalGuard` | HMAC ring + NonceStore（/internal/v1/* 防护） |
| `SharedPGPool` | postgres 连接池（跨 Cell 共享） |
| `ConsumerClaimer` | outbox 消费幂等键声明者 |
| `PrimaryHTTPAddr` / `InternalHTTPAddr` / `HealthHTTPAddr` | 三 listener 绑定地址 |

## 环境变量（关键）

| 变量 | 说明 | 缺失行为 |
|------|------|---------|
| `GOCELL_JWT_ISSUER` | JWT iss claim | fail-fast |
| `GOCELL_SERVICE_SECRET` | /internal/v1/* HMAC 密钥（≥32 字节） | fail-fast |
| `GOCELL_ADAPTER_MODE` | 适配器模式：`""`（dev，默认）/ `real` | — |
| `GOCELL_CELL_ADAPTER_MODE` | 存储后端：`memory`（默认）/ `postgres`（`postgres` 经 Topology 耦合规则强制要求 `GOCELL_ADAPTER_MODE=real`） | — |
