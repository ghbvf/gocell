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

`RowScopeAll`（跨租户）不经 `NewRowVisibility`——它**拒绝** all（#1760）；唯一生产者是
sealed `tenant.NewCrossTenantVisibility()`，sole production caller = `runtime/auth` 的
super-admin 派生（与强制 FR-007 审计同址，`ROWSCOPEALL-AUDIT-FUNNEL-01`）。跨租户读取
API 取 sealed `tenant.CrossTenantVisibility` 位置参（Hard typed funnel，漏传/伪造皆编译错）；
minter 单调用方限制是文档化 Medium Go 天花板（#1282/#851/#893 同族）。

audit read 的 **serving 池**（NOBYPASSRLS）对 `RowScopeAll` 始终 fail-closed
（`RowScopeAllUnsupportedError` → 501，纵深防御）。super-admin 跨租户读取由**专用
`gocell_audit_admin` admin 读取池**（角色限定 permissive RLS policy，非 BYPASSRLS）服务，
未 provision 时优雅 fail-closed（501，不 fail-open）。机制/威胁矩阵见 ADR
`202606131900-1810` + `202606071300-1618`/`202606071200-1676` 的 2026-06-13 amendment。

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
- 业务 handler 无 role-literal 授权分支由 `PERMISSION-BASED-AUTHZ-01`（Medium）守卫，扫描范围
  `corecells/` + `examples/`，**两条 arm**：① helper 形态 ban（`AnyRole`/`SelfOr`/`RequireAnyRole`），
  allowlist 冻结为空（zero-exception）；② 手写 `(*auth.Principal).HasRole` 授权分支 ban（type-aware
  receiver 解析），sanctioned 用法（两个 example PDP baseline + auditcore RowScope/FR-007 派生）收录独立
  allowlist `permissionBasedAuthzHasRoleAllowlist`，每条带理由 + no-stale 反查。残留盲区：手写
  `range p.Roles` 成员判定（identitymanage 字段级 admin guard）不在 `HasRole` 方法范围内、不被捕获。
  Hard 化路径与完整盲区见对应 archtest godoc 与 PR-10a ADR。
- **gRPC 方法授权与 HTTP 同构**（#2008）：非 public gRPC RPC 进 runtime 后由 auth interceptor 的
  PDP gate 调同一 `auth.Authorizer`（composition root 经 `interceptor.Deps.Authorizer` 注入，
  与 HTTP `WithPrimaryAuthorizer` 同源），按方法所需 permission 决策——不在 handler 手写谓词。
  method→permission 由契约 `endpoints.grpc.methods[].permission` overlay 经 cellgen 派生入
  `GRPCServiceSpec.MethodPermissions`，registrar 解析成 sealed `authz.Permission`（未知即启动 fail-fast）。
  严格 fail-closed：非 public 方法缺 permission overlay = gate deny，且 codegen completeness 预检在构建期
  拒绝（dead 403 不可静默上线）。resource = full method name（coarse，owner-scoped 取 message 字段延后）。
  **启动期 fail-fast 与 HTTP 同构**（#2204）：spec 含 permission-gated 方法但未 wire Authorizer，注册期
  （phase7b drain，Init 后 / serve 前）fail-fast，不再 boot+请求期才 403——对齐 HTTP `ResolveAuthorizer`；
  overlay method-key 在注册期对本 spec 已注册方法集做闭集校验（stale/typo key fail-fast，非请求期 dead 403）。
  **错误模型机器可读**：每个 deny 携 sealed `google.rpc.ErrorInfo`（`Reason` 闭值集 + `Domain=gocell.authz.grpc`
  + 非 PII metadata：method/permission，无 subject/token），客户端无需解析英文文本区分 no-mapping/not-wired/
  denied/obligation/unavailable。**PDP 决策指标同构**：gRPC PDP 决策经 `NewObservableAuthorizer` 包装（真实
  provider 时，`kernelmetrics.IsReal` 单源与 HTTP `hasRealMetricsProvider` 共用），落同一 `auth_pdp_decision_*`
  series（无 transport 标签，registerOrReuse 共享 family）。机制/评级/威胁矩阵见 grpc-transport-adapter ADR
  §"Amendment 2026-06-15 — #2008" + §"Amendment 2026-06-16 — #2204" + archtest
  `GRPC-PERMISSION-GATE-WIRING-FUNNEL-01` + 治理 `FMT-41`。

相关 enforcement 的完整 ID、评级、Hard 化路径和盲区写在对应 archtest godoc 与 PR-10a ADR
（`docs/architecture/202606121400-1348-adr-pr10a-authz-wiring.md`）。
