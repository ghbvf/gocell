# cmd/ 层规则

## cmd/gocell vs cmd/corebundle

| | `cmd/gocell` | `cmd/corebundle` |
|---|---|---|
| **角色** | 治理 / 元数据 / 代码生成器 CLI | 运行时组装产物（部署二进制） |
| **使用时机** | dev + CI（构建期、治理期） | 生产部署（运行时） |
| **子命令** | validate / scaffold / generate / check / verify / graph / export | —（单一入口，启动 bootstrap） |
| **进入运行时进程** | 否 | 是 |
| **对标定位** | 构建 / 治理工具（类似 `kubectl` / `go generate`） | Composition Root（类似 Uber fx 的 wire-up 入口） |

- **`cmd/gocell`**：治理与元数据 CLI，供 dev 与 CI 调用。执行 validate（contract / cell / slice 声明合规）、scaffold（脚手架）、generate（codegen 契约派生）、check（自定义规则检查）、verify（验收规格对齐）、graph（模块包依赖图输出）、export（项目元数据 / 目录导出为 JSON/YAML）。**不进入运行时进程**，不依赖生产外部资源。

- **`cmd/corebundle`**：`assemblies/corebundle/` 的运行时组装产物。负责 bootstrap wiring：加载 SharedDeps（PG / Redis / AMQP / JWT / HMAC 等）、构造各 CellModule、配置三个 HTTP listener（Primary / Internal / Health）、启动 `bootstrap.Run`。**是实际部署运行的二进制**，也是项目唯一的 Composition Root。

> 一句话区分：`cmd/gocell` 是构建 / 治理期工具；`cmd/corebundle` 是运行时 Composition Root。

---

cmd/corebundle/ 是 Composition Root，负责组装所有 Cell、配置三个 listener、启动 bootstrap。

## 三层组装模式

```go
// 第一层：环境变量注入 + 模块工厂
shared, _ := LoadSharedDepsFromEnv(ctx)
modules, _ := corebundleModules(assemblyCellIDs)

// 第二层：BuildApp 组装 cells + bootstrap.Option（失败按 LIFO 回滚资源）
cells, cellOpts, _ := BuildApp(ctx, shared, modules...)
asm, _ := buildAssembly(shared.PromStack, assemblyID, mode, cells...)

// 第三层：三 listener + bootstrap
opts, err := defaultRuntimeOptions(shared, asm, consumerBase, metricsHandler, adapterInfo)
if err != nil {
    return fmt.Errorf("defaultRuntimeOptions: %w", err)
}
opts = append(opts, cellOpts...)
bootstrap.New(opts...).Run(ctx)
```

## Listener 配置

```go
// B2-K-02: composition root 改 error-first；MustNew* 已删除
// Primary：公开 API + JWT（phase4 自动发现 verifier）
jwtAuth, err := cell.NewAuthJWTFromAssembly(asm)
if err != nil {
    return nil, fmt.Errorf("NewAuthJWTFromAssembly: %w", err)
}
bootstrap.WithListener(cell.PrimaryListener, shared.PrimaryHTTPAddr,
    []cell.ListenerAuth{jwtAuth})

// Internal：控制平面 + ServiceToken（HMAC-SHA256 + replay guard）
svcTokenAuth, err := cell.NewAuthServiceToken(guard.NonceStore(), guard.Ring())
if err != nil {
    return nil, fmt.Errorf("NewAuthServiceToken: %w", err)
}
bootstrap.WithListener(cell.InternalListener, shared.InternalHTTPAddr,
    []cell.ListenerAuth{svcTokenAuth})

// Health：/healthz /readyz /metrics，显式无认证
bootstrap.WithListener(cell.HealthListener, shared.HealthHTTPAddr,
    []cell.ListenerAuth{cell.AuthNone{}})
```

`authChain` 必须非 nil；显式无认证用 `cell.AuthNone{}`，传 nil 在 phase0 fail-fast。

## CellModule 接口

每个模块实现 `CellModule`，提供 Cell + bootstrap.Option + ManagedResource：

```go
type CellModule interface {
    ID() string
    Provide(ctx context.Context, shared *SharedDeps) (cell.Cell, []bootstrap.Option, []lifecycle.ManagedResource, error)
}
```

参考实现：`access_module.go`、`config_module.go`、`audit_module.go`。

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
| `GOCELL_CELL_ADAPTER_MODE` | `dev`（默认）/ `real` | — |
| `GOCELL_STORAGE_BACKEND` | `memory`（默认）/ `postgres` | — |
