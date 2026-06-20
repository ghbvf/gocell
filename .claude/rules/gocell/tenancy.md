# 多租户 / ABAC / 行列级数据权限规则

本文件只保留当前行为约束。设计历史、分阶段计划和未来工作归 ADR、spec 或
GitHub Issues。

## TenantID

`tenant.TenantID` 是隔离域边界类型。空值和 nil UUID 非法；非空必须是 canonical
UUID。repo 和 service API 使用 typed tenant 参数，不传裸 string。

## RowScope / RowVisibility

`tenant.RowScope` 取值为 self、device、tenant、all，零值 invalid。
`tenant.RowVisibility` 是 sealed obligation，由 constructor 生成：

- self / device 必须带 subject。
- tenant / all 不带 subject。
- SQLPredicate / Allows 是纯翻译器，不决定 all 是否可用。

`tenant.NewRowVisibility` 拒绝 `RowScopeAll`。跨租户可见性只能由
`tenant.NewCrossTenantVisibility()` 生产；跨租户读取 API 必须接收 sealed
`tenant.CrossTenantVisibility` 位置参，不能接收普通 `RowVisibility` 或裸 scope。
`RowScopeAll` 只能从 `runtime/auth` 的 super-admin 派生路径进入业务；派生必须与
FR-007 强制审计同址。

audit read 的 serving 池对 `RowScopeAll` 始终 fail-closed，返回
`RowScopeAllUnsupportedError` / 501。super-admin 跨租户 audit read 只能走专用
`gocell_audit_admin` admin 读取池；该池必须使用角色限定 permissive RLS policy，
不得使用 BYPASSRLS。admin 池未 provision 时返回 501，不 fail-open。

## Principal claim source

JWT tenant claim 在 auth 边界解析并写入 context。service principal 无 tenant。
`Principal.RowVisibility(ctx)` 是身份到 row-scope 的框架级派生入口：

- normal user -> self
- device -> device
- admin -> tenant
- super-admin -> all
- service / anonymous / unknown -> fail-closed

## RLS 与 PG scope

PG tenant scope 使用 `SET LOCAL` 注入当前事务。scope 写入只允许通过受控 helper；
绕过 TxManager 直接借连接必须 fail-fast。

RLS policy shape 由 schema guard 检查。app-serving role 必须非 owner 且无 bypass RLS
权限。写端点依靠 typed tenant 参数、ctx tenant 与 PostgreSQL FORCE RLS 维护 tenant
边界。

## ABAC authz 接线（permission-based）

业务端点授权走 PDP 决策，不在 handler 硬编 role-name 字面量。

- 路由门禁用 `auth.RequirePermission(authz.Permission)`、
  `auth.RequirePermissionForResource(pathParam, perm)` 或
  `auth.RequirePermissionForContract(...)`，不用 `auth.AnyRole` / `auth.SelfOr` /
  `auth.RequireAnyRole` 做授权分支。
- `authz.Permission` 是 sealed 闭值集；业务代码经 accessor 函数
  （如 `authz.PermAuditRead()`）取得 permission，不传 role 字符串。
- handler 不手写 `(*auth.Principal).HasRole` 或遍历 `Principal.Roles` 做授权。
- `EffectAllow` 规则必须声明至少一个 action；空 action 只允许用于
  `EffectDeny` 的 deny-all。写侧和读侧都必须 fail-closed。
- 路由门禁只做 coarse allow/deny，不执行 RowScope / FieldMask obligation。Allow
  规则携带非零 obligation 时必须 fail-closed。

## Resource ownership

path-param 标识的 resource ownership 是 PDP ABAC 决策，不是 handler 短路。owner-scoped /
self-scoped gate **contract-derived**（#2355）：契约声明 `endpoints.http.resource:
<pathParam>`（owner-scoped）或 `endpoints.http.selfScoped: true`（self-scoped），生成
handler 经单一 `auth.RequirePermissionForContract(contractSpec, resolver)` funnel 派生
`RequirePermissionForResource(pathParam, perm)` / `RequirePermissionForSelf(perm)`——业务
slice 不手写 gate。`resource`/`selfScoped` 各 ⇒ permission、二者互斥（schema + FMT-42 +
ContractSpec.Validate 三重）。owner-scoped gate 把 canonical resource id（self-scoped 把
调用者自身 subject）转发给 PDP。

- baseline ownership 用 `subject.sub == resource.id` 判定。
- **owner vs admin 同 permission**：同一 owner-scoped action（如 `user:write`）既用于带
  resource 的 owner 路由（改自己），也用于不带 resource 的 admin 路由（coarse，改任意）。
  故 HTTP **不照搬** gRPC FMT-41 的「owner-scoped permission ⇒ resource 必填」（会误拒 admin
  路由）；`resource` 是 per-route 授权选择，不从 permission 派生。详见 ADR
  `202606201500-2355`。
- 空或非 canonical path-param 不等于 self；resource 不可解析时规则不命中并
  fail-closed。
- delegated ownership 用 `subject.sub == resource.owner`，owner 由 PIP lookup 供给。
- device 读自身状态必须 kind-gated：`subject.kind == device AND subject.sub == resource.id`。
- owner-scoped route gate 的 baseline allow surface 是 `{owner, admin}`；owner/self
  不扩大数据访问，数据可见性仍由 principal 派生的 `RowScope` 独立治理。

query-param self scoping 仍留在 handler / service：例如 audit 的空 `actorId` 对 admin
表示全 actor permissioned 读，不是隐式 self；只有显式 `param == subject` 才走 PDP
ownership 规则。

## Authorizer 与 PDP

Authorizer 经 composition root `bootstrap.WithPrimaryAuthorizer` 注入 primary listener
request context。Cell 不 import 兄弟 Cell 的 Authorizer。

强依赖缺失必须 fail-fast；可解析的 Authorizer 在 bootstrap router build（Init 后、
serve 前）预解析。nil provider 在启动期 fail-fast，而不是首请求暴露。

PDP 默认 fail-closed：缺 Authorizer、缺租户、store 不可用或无适用 permit 都 deny。
baseline 是 action-scoped + role-conditioned 的 allow 规则，不是 allow-all。租户 policy
可以叠加 allow / deny；deny 优先。

租户 allow 可以放宽路由门禁，但不能扩大数据访问。读端点的数据可见性由 principal
派生的 `RowScope` 决定；写端点没有 RowScope 维度，必须依赖 typed tenant 参数和 FORCE
RLS 维护 tenant 边界。

## gRPC 授权

非 public gRPC RPC 进入 runtime 后由 auth interceptor 调同一 `auth.Authorizer`
做 PDP gate，不在 handler 手写谓词。

- method -> permission 由契约 `endpoints.grpc.methods[].permission` overlay 派生。
- 非 public 方法缺 permission overlay 必须 fail-closed，并在生成或注册期拒绝上线。
- 未知 permission 必须启动 fail-fast。
- owner-scoped permission 必须声明 `endpoints.grpc.methods[].resource`，interceptor
  从请求消息字段提取 resource 并 canonicalize 后转发给 PDP。
- unary RPC 在入口取 resource；server-streaming owner-scoped RPC 将 permission gate
  延后到首个 `RecvMsg`。
- resource 提取失败（非 proto.Message、字段不存在、非 string）必须 deny，不回退到
  full method；空或非 UUID 值转发给 PDP。提取值不得写入 ErrorInfo metadata。
- `passwordResetExempt: true` 只允许用于非 public 且声明 permission 的方法。
- 每个 deny 必须携带 sealed `google.rpc.ErrorInfo`，metadata 不含 subject / token /
  resource value 等 PII。
- gRPC 与 HTTP 使用同一 PDP 决策指标 family，不新增 transport 分叉。

## HTTP 授权

HTTP route gate 与 gRPC 同源。HTTP route -> permission 由契约
`endpoints.http.permission` overlay 派生，生成 handler 通过
`auth.RequirePermissionForContract(contractSpec, resolver)` 解析并进入同一 PDP 路径。owner-scoped
（`endpoints.http.resource`）/ self-scoped（`endpoints.http.selfScoped`）由该同一 funnel 按
`contractSpec.{Resource,SelfScoped}` 三分支派生，见 §Resource ownership 与 ADR `202606201500-2355`。

每个 `lifecycle: active` 且 `codegen` 的 HTTP 契约必须声明恰好一个 AuthZ mode：

- ABAC 默认：`endpoints.http.permission`
- 显式 opt-out：`public` / `bootstrap` / `clientsOnly` / `serviceOwned`

opt-out 必须带非空 `endpoints.http.auth.reason`。ABAC mode 不带 reason；
reason-without-opt-out 必须拒绝。permission 与 opt-out mode 互斥。
HTTP `passwordResetExempt` 不是 AuthZ mode；单独声明仍是 modeless，必须拒绝。

`contractgen.buildHTTPSpec` 是 codegen 完整性门：modeless route 不得被渲染上线。
`gocell validate` / governance 规则只做纵深检查，不能替代 codegen 强制门。

## References

- 规则文件职责：`docs/guides/agent-instruction-surfaces.md`
- ABAC / permission 接线：`docs/architecture/202606121400-1348-adr-pr10a-authz-wiring.md`
- audit / RLS / 跨租户读取：`docs/architecture/202606131900-1810-adr-super-admin-cross-tenant-audit-read.md`
- HTTP AuthZ mode：`docs/architecture/202606190847-2020-adr-authz-default-abac.md`
- gRPC 授权：`docs/architecture/202605260000-adr-grpc-transport-adapter.md`
- HTTP owner/self-scoped contract-derived gate：`docs/architecture/202606201500-2355-adr-http-owner-scoped-contract-derived-authz.md`
