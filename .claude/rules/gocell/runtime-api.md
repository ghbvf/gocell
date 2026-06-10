# Runtime API

## Auth package

Kernel auth plan 类型来自 `github.com/ghbvf/gocell/kernel/auth`。当同一文件也 import
`runtime/auth` 时，kernel auth 使用别名 `kauth`。

`cell.Registrar`、listener 常量和 route group 类型位于 `kernel/cell`。

## RouteGroup

Cell 在 `Init(ctx, reg)` 中通过 `reg.RouteGroup` 声明 listener、prefix、register
callback。callback 返回 error，错误必须冒泡到 bootstrap；禁止 `MustMount` 风格 panic。

业务路由使用 `runtime/auth.Mount(mux, auth.Route{...})`。`auth.Route.Contract`
承载 method、path、contract ID。Public 和 password-reset-exempt 只能通过 route 字段显式声明。

## Listener

标准 listener：

- `PrimaryListener`：业务 API。
- `InternalListener`：服务间控制面。
- `HealthListener`：health、ready、metrics。
- `AdminListener`：operator 或管理面。

listener auth chain 必须显式声明。无认证使用 `AuthNone{}`，nil 是配置错误。
单 listener 只能有一个 auth scheme；不同 scheme 通过不同 listener 表达。

## Internal endpoint

`/internal/v1/*` 必须满足：

- 挂在 internal listener。
- 使用 service token 或更强认证。
- 声明 caller-cell allowlist。
- nonce store 在多实例部署中必须 replay-safe。

FinalizeAuth 在所有 route 注册完成后运行；业务不得绕过最终 matcher。

## Auth plan 优先级

认证来源优先级：

1. route 显式 Public / PasswordResetExempt。
2. listener auth plan。
3. bootstrap fail-fast 默认拒绝。

Cell 禁止构造 AuthPlan；composition root 组装后通过 bootstrap option 注入。

## Option 范式

- 强依赖 option 必须 fail-fast，不静默 noop。
- 累加式 builder 可忽略空输入，但最终 build 必须 validate。
- 删除旧 shim，不保留兼容别名。
- 新 runtime option 必须有明确 owner、默认值、安全失败路径和测试。
