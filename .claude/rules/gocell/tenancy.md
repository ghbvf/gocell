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
| `TENANT-REPO-PARAM-FUNNEL-01` | accesscore repo 方法须收 `tenant.TenantID` 位置参（param[1]，紧跟 ctx），除 `UserRepository.GetByID` by-PK 派生豁免；漏传=编译器 Hard，string-slot 或位置偏移漂移=Medium typed scan + 反向 fixture（F12 fix：position assertion） | Medium（Hard 主门控=编译器 typed 位置参；sealed-handle Hard-upgrade 见 archtest godoc，gh #1478） | `tools/archtest/tenant_repo_param_funnel_test.go` |
| `TENANT-REPO-CALLSITE-FUNNEL-01` | 生产代码调用无 tenant 参的 `UserRepository.GetByID`（接口方法调用）必须在有界许可列表中（sessionrefresh / sessionvalidate / rbacassign + test-support）；go/types Selections 对象身份匹配，不受 import alias 影响；Hard-upgrade 路径 = sealed TenantScopedRepo handle（gh #1478） | Medium 下游（archtest caller-allowlist）/ Medium 上游（Go 可见性天花板，won't-do 同 #1282） | `tools/archtest/tenant_repo_param_funnel_test.go` |
| `tenant.FromContext` fail-closed | 后认证 service/consumer 经它取 ctx tenant 传 repo；缺失/非法 → error（不静默零值） | Medium（read-side 桥；ctx 写侧由 `CTXKEYS-PRINCIPAL-WRITE-CALLER-01` 锁） | `pkg/tenant/context.go` runtime guard |

## 后续 PR 增量（占位，落地时补）

- PR-2a（本 PR #1340，已落）：**accesscore** repo `tenant.TenantID` 强制 typed 位置参 + rebuild 列迁移（migration 046）+ `TENANT-REPO-PARAM-FUNNEL-01`。**Model A**（tenant-scoped username：复合 unique `(tenant_id, username)`/`(tenant_id, email)`；roles per-tenant，PK `(tenant_id, id)`；effective-admin 不变式 per-tenant advisory lock）。`UserRepository.GetByID` 为 by-全局-UUID-PK tenant-deriving 豁免（唯一无源 caller = sessionrefresh）。pre-auth tenant 来源：login/setup 经 `X-Tenant-ID` header → `LoginInput.TenantID`/`CreateAdminInput.TenantID`；post-auth 经 `tenant.FromContext`。sessions 表 + session.Store typed param + refresh tenant 载体 **推迟 PR-3**（refresh 路径在 PR-3 RLS SET LOCAL 下才有真隔离）。
- PR-2b（carryover #1340 split）：**configcore/auditcore** repo tenant typed param + 列迁移；auditcore `AuditFilters.TenantID` + auditquery tenant-scoped 读路径（删 PR-1 的 403 fail-closed 闸门 + appender `INV-SINGLE-TENANT-ONLY` tripwire）。**注**：PR-2a **未动** auditquery 403 闸门 / tripwire（仍是正确止血）。
- PR-3：PG `FORCE ROW LEVEL SECURITY` + `TxRunner` `SET LOCAL app.tenant_id`。
- PR-4：`RowScope` typed 位置参 + `ROWSCOPE-REPO-PARAM-FUNNEL-01`。
- PR-5：身份 → RowScope 收窄 + `RowScope=all` 强制审计。
- PR-6..10：ABAC policy 域模型 / 评估引擎 / `Authorizer` 重做 / policymanage / 接线（#914）。
- PR-11/12：`pkg/projection.ResourceProjection` sealed + `FieldMask` 列 masking。
- PR-13：跨层 ADR ×3 + 扇出收口。
