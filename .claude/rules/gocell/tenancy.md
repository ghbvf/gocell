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
