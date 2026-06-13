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
  permission 单例经 accessor 函数（`authz.PermAuditRead()`）暴露而非可重赋值的导出 var——
  函数不可重赋值 = 注册值类型级不可变（重赋值即编译错）。role 字符串不可传入 Permission 位
  （概念隔离）。
- **resource ownership 进 PDP**（#1977）：path-param 标识的 resource ownership 是 PDP ABAC 决策，
  不是 handler 短路。owner-scoped 端点用 `auth.RequirePermissionForResource(pathParam, perm)`——它把
  canonical 化的 path-param 转发给 PDP 作 `resource`，由 baseline ownership 规则
  `subject.sub == resource.id`（`abac.OpEqualsAttr` 跨属性算子）判定，引擎决策、无 Go `isSelfAccess`
  短路。「空参数 ≠ self」保留：空/非 canonical param → `resource.id` not-found → 规则不命中（fail-closed）。
  delegated ownership（owner ≠ id，如设备）用 `subject.sub == resource.owner`（owner 由 PIP lookup 供）。
  owner-scoped 端点的 gate 形状由 `OWNER-SCOPED-GATE-EXACT-SET-01`（Medium）冻结守卫——把
  owner gate 回退成裸 `auth.RequirePermission`（转发 `r.URL.Path` 而非 canonical resource id）即 CI 红；
  精确集（identitymanage / rbaccheck）与盲区见该 archtest godoc。baseline owner-scoped action
  （user:read/write、role:read）的授予面由 `BASELINE-OWNER-RULE-TENANT-FREEZE-01`（Medium，value-golden）
  冻结：① 每条 owner self 规则须 = EffectAllow + 精确单 action + frozen owner condition
  （`subject.sub == resource.id`）；② 每个 owner action 的 allow 规则闭集恰为 `{1 owner, 1 admin}`。真正的
  owner→tenant widen 向量（PDP 跨规则 OR）——**新增一条 tenant 匹配 allow 规则**、替换 owner 条件、或扩
  action——即 build-test lane 红（给现有规则加 AND 条件是收紧非 widen，仍按 forbidden drift 拒）。跨租户拒绝
  是 tenant-agnostic ownership 规则（`subject.sub != resource.id`）的天然结果，由 e2e `cross_tenant_*` 用例
  覆盖（#2026）。评级/盲区见对应测试 godoc。
- self ownership **不扩大数据访问**：路由门禁放行只让 owner 过 coarse gate；行可见性仍由 principal 派生的
  `RowScope`（身份决定，policy 改不动）独立治理（D3）。query-param self scoping 仍留 handler/service：如
  audit 的空 `actorId` 对 admin 是全 actor permissioned 读，非隐式 self（只有显式 `param == subject` 经
  PDP ownership 规则豁免门禁）。
- Authorizer 经 composition root `bootstrap.WithPrimaryAuthorizer` 注入 primary listener
  request ctx（唯一 `auth.WithAuthorizer` 上游 + `AuthorizerFromContext`/`RequirePermission`
  下游）。Cell 不 import 兄弟 cell 的 Authorizer；强依赖缺失 fail-fast；可解析的 Authorizer
  在 bootstrap router build（Init 后、serve 前）经 `ResolveAuthorizer` 预解析，nil provider
  在启动期 fail-fast 而非首请求才暴露。
- PDP fail-closed：缺 Authorizer / 缺租户 / store 不可用 / 无适用 permit → deny。内置 baseline
  是 action-scoped + role-conditioned 的 allow 规则（复刻既有 role 门禁）；baseline ≠ 降级
  allow-all。租户 policy 叠加在 baseline 上，可加 allow 也可加 deny（forbid-wins 保证 deny
  优先）——故租户 allow 可放宽**路由门禁**，但**不能扩大数据访问**：数据可见性由 principal
  派生的 `RowScope`（身份决定，policy 改不动）独立治理，租户给非 admin 授 `audit:read` 只让其
  过门禁，数据层仍按 RowScope=self 只返回本人行。路由门禁是 RowScope 之上的纵深防御，不是唯一控制点。
- 路由门禁是 coarse allow/deny，不**执行** obligation（RowScope/FieldMask 由数据层 PEP 执行），
  但对 Allow 携带的非零 obligation **fail-closed**（拒绝而非静默丢弃）——baseline obligation
  为零，正常路径不受影响。
- 业务 handler 无 role-literal 授权分支由 `PERMISSION-BASED-AUTHZ-01`（Medium，带未迁移 cell
  allowlist）守卫；allowlist 是迁移进度账，逐 PR 删空。

相关 enforcement 的完整 ID、评级、Hard 化路径和盲区写在对应 archtest godoc 与 PR-10a ADR
（`docs/architecture/202606121400-1348-adr-pr10a-authz-wiring.md`）。
