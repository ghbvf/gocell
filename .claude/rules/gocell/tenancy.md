# 多租户 / ABAC / 行列级数据权限规则导航

> 本文件是 EPIC #1337（多租户隔离 + ABAC policy-as-code + 行列级数据权限）的规则导航索引，随各 PR 增量补充。设计真值源：`docs/plans/specs/1220-tenancy-abac-dataperm/`（spec.md / plan.md / tasks.md / research.md）。每条 enforcement 的完整盲区清单 + AI-robust 评级举证活在对应 archtest 的 package godoc（单源，本文件只汇总 ID / 一行摘要 / 评级供导航）。

## 类型地基（`pkg/tenant/`）

- **`tenant.TenantID`**（`pkg/tenant/tenant_id.go`）：隔离域边界 sealed newtype，对齐 `idutil.SafeID` 范式。与 SafeID 不同——**空值无效**（tenant 查询不可缺 tenant），非空须为 canonical UUID。`Validate` / `ParseTenantID` / `UnmarshalJSON` runtime fail-fast（Medium；type-system Hard 来源 = PR-2 repo 方法收 `TenantID` typed 位置参，漏传=编译错误）。
- **`tenant.RowScope`**（`pkg/tenant/rowscope.go`）：行可见范围 typed obligation enum `{self, device, tenant, all}`，零值 invalid。XACML PDP→PEP obligation 模型的有界 Go 投影。**仅类型，消费在 PR-4**（repo list/get typed 位置参）。取值扩张超 4 值则迁 `pkg/authz/`。

## Principal / claim source（`runtime/auth/`）

- **`PrincipalKind` 增 `PrincipalDevice`**（`runtime/auth/principal.go`）：device 一等主体类型地基（喂下游 MDM #1051 / 零信任 #1052）。develop 无 device-token 签发方，`jwtClaimsToPrincipal` 仍产 `PrincipalUser`；device 分类随真实签发方落地。
- **JWT `tenant_id` claim → `ctxkeys.TenantID`**（`runtime/auth/jwt.go` + `authenticator.go` + `middleware.go`）：消除「no source on develop」。claim 在 JWT authenticator 边界经 `tenant.ParseTenantID` fail-closed 校验 + canonical 归一（malformed UUID → 401）。service 主体无 tenant（callerCellID ≠ tenant）。

## Enforcement 索引

| ID | 摘要 | 评级 | 载体 |
|----|------|------|------|
| `CTXKEYS-PRINCIPAL-WRITE-CALLER-01` | principal ctxkey setter（含 `WithTenantID`）写入方锁定在 auth 边界桥 + consumer restore | Hard 下游 / Medium 上游（Go 可见性天花板，#1282） | `tools/archtest/ctxkeys_principal_write_caller_test.go` |
| `PRINCIPAL-KIND-EXHAUSTIVE-SWITCH-01` | 生产 `switch` on `PrincipalKind` 必须显式覆盖全部常量（default 不豁免） | Medium | `tools/archtest/principal_kind_exhaustive_switch_test.go` |
| `tenant.TenantID.Validate()` 空值 + UUID | repo 拿到空 tenant 是 bug → 拒；非空须 UUID | Medium（Hard 来源 = PR-2 typed 位置参） | `pkg/tenant/tenant_id.go` runtime guard |

## 后续 PR 增量（占位，落地时补）

- PR-2：平台 repo `tenant.TenantID` 强制 typed 位置参 + 列迁移（`TENANT-REPO-PARAM-FUNNEL-01`）；auditcore `INV-SINGLE-TENANT-ONLY` 哨兵移除 + tenant-scoped audit 过滤。
- PR-3：PG `FORCE ROW LEVEL SECURITY` + `TxRunner` `SET LOCAL app.tenant_id`。
- PR-4：`RowScope` typed 位置参 + `ROWSCOPE-REPO-PARAM-FUNNEL-01`。
- PR-5：身份 → RowScope 收窄 + `RowScope=all` 强制审计。
- PR-6..10：ABAC policy 域模型 / 评估引擎 / `Authorizer` 重做 / policymanage / 接线（#914）。
- PR-11/12：`pkg/projection.ResourceProjection` sealed + `FieldMask` 列 masking。
- PR-13：跨层 ADR ×3 + 扇出收口。
