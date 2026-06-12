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

RowScopeAll 的生产者必须经审计 funnel。audit read 在不支持跨租户时 fail-closed。

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
绕过 TxManager 直接借连接必须 fail-fast。RLS policy shape 由 schema guard 检查。
app-serving role 必须非 owner 且无 bypass RLS 权限。

相关 enforcement 的完整 ID、评级和盲区写在对应 archtest godoc。

## ABAC authz 接线（permission-based）

业务端点授权迁向 PDP 决策，不在 handler 硬编 role-name 字面量。

- 路由门禁用 `auth.RequirePermission(authz.Permission)`，不用 `auth.AnyRole`/`auth.SelfOr`
  做授权分支。`authz.Permission` 是 sealed 闭值集（唯一 minter = registry，包外不可伪造）；
  role 字符串不可传入 Permission 位（概念隔离）。
- self / ownership 检查（path/query 参数 == subject）是请求形状判定，留 handler 代码；
  行可见性由数据层 `RowScope` 治理，不进路由 PDP。
- Authorizer 经 composition root `bootstrap.WithPrimaryAuthorizer` 注入 primary listener
  request ctx（唯一 `auth.WithAuthorizer` 上游 + `AuthorizerFromContext`/`RequirePermission`
  下游）。Cell 不 import 兄弟 cell 的 Authorizer；强依赖缺失 fail-fast。
- PDP fail-closed：缺 Authorizer / 缺租户 / store 不可用 / 无适用 permit → deny。内置 baseline
  是 action-scoped + role-conditioned 的 allow 规则（复刻既有 role 门禁），租户 policy 经
  forbid-wins 只能收窄；baseline ≠ 降级 allow-all。
- 业务 handler 无 role-literal 授权分支由 `PERMISSION-BASED-AUTHZ-01`（Medium，带未迁移 cell
  allowlist）守卫；allowlist 是迁移进度账，逐 PR 删空。

相关 enforcement 的完整 ID、评级、Hard 化路径和盲区写在对应 archtest godoc 与 PR-10a ADR
（`docs/architecture/202606121400-1348-adr-pr10a-authz-wiring.md`）。
