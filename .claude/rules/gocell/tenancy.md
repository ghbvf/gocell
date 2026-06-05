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
| `PG-SETLOCAL-FUNNEL-01` | adapters/postgres：Prong1 `.Exec` string-literal 以 `SET ` 开头但非 `SET LOCAL ` → 拒（防 session 级裸 SET 归还连接泄漏租户，反向 fixture 守）；Prong2 `app.tenant_id` GUC 写（`set_config('app.tenant_id'…`/`SET [LOCAL] app.tenant_id`）callsite 只能在 `tx_manager.go::setLocalTenant`（+ anti-vacuity） | Medium（Prong2 caller-allowlist 真 funnel；上游 Go 可见性天花板，Hard-upgrade=sealed GUC-write handle，gh #1619） | `tools/archtest/pg_setlocal_funnel_test.go` |
| `TENANT-TXSCOPE-WRITE-CALLER-01` | `tenant.WithScope`（RLS GUC 写入边界）生产引用方锁定（PR-3a 唯一=`configcore/internal/scopedread`；anti-vacuity 反向自检） | Hard 下游（`scopeKey` 未导出 sealed + go/types caller-allowlist）/ Medium 上游（Go 可见性天花板，#1282 同族，Hard-upgrade=sealed scoped-tx handle，gh #1619） | `tools/archtest/tenant_txscope_write_caller_test.go` |
| schema_guard `verifyRLS` | config 三表（config_entries/config_versions/feature_flags）`relrowsecurity && relforcerowsecurity` + `tenant_isolation` policy 存在性，随 /readyz `VerifyExpectedShape` 起验（防 RLS 被静默关/drop） | Medium（DB shape 探针；启动期 fail-fast） | `adapters/postgres/schema_guard.go::verifyRLS` |

## 后续 PR 增量（占位，落地时补）

- PR-2a（本 PR #1340，已落）：**accesscore** repo `tenant.TenantID` 强制 typed 位置参 + rebuild 列迁移（migration 050）+ `TENANT-REPO-PARAM-FUNNEL-01`。**Model A**（tenant-scoped username：复合 unique `(tenant_id, username)`/`(tenant_id, email)`；roles per-tenant，PK `(tenant_id, id)`；effective-admin 不变式 per-tenant advisory lock）。`UserRepository.GetByID` 为 by-全局-UUID-PK tenant-deriving 豁免（唯一无源 caller = sessionrefresh）。pre-auth tenant 来源：login/setup 经 `X-Tenant-ID` header → `LoginInput.TenantID`/`CreateAdminInput.TenantID`；post-auth 经 `tenant.FromContext`。access token 携带 `tenant_id` claim（登录后 accesscore tenant-scoped repo 经 `tenant.FromContext` 取 tenant）。sessions 表 + session.Store typed param + refresh tenant 载体 **推迟 PR-3**（refresh 路径在 PR-3 RLS SET LOCAL 下才有真隔离）。**auditquery tenant-scoped 读路径已随本 PR 提前落地**（token 带 tenant 后 PR-1 的 403 闸门会拒所有认证用户 audit 查询，二者硬耦合必须同时落地）：`ledger.AuditFilters.TenantID`（store 层 AppendIf optional predicate）+ auditquery handler 始终从 authenticated principal 设 `filters.TenantID`（隔离边界在 handler，非 generic store）+ 删 PR-1 的 403 fail-closed 闸门 + 删 appender `INV-SINGLE-TENANT-ONLY` tripwire。tenant-less 残留由 PR-3 RLS 兜底。
- PR-2b（carryover #1340 split，#1479）：**configcore/auditcore** repo tenant typed param + 列迁移 + auditcore 写侧 tenant 收口。**注**：auditquery tenant-scoped **读**路径（`AuditFilters.TenantID` + 删 403 闸门 + 删 tripwire）已在 PR-2a 提前落地，不在 #1479 范围。
- PR-3a（本 PR #1341，已落）：PG `ENABLE`+`FORCE ROW LEVEL SECURITY` + `tenant_isolation` policy（migration 052，**仅 config 三表** config_entries/config_versions/feature_flags；谓词 `tenant_id (TEXT) = NULLIF(current_setting('app.tenant_id', true), '')`，空/未设→0 行 fail-closed；USING+WITH CHECK；TEXT=TEXT 不 cast 列，issue 字面 `::uuid` 假设 uuid 列、实际 TEXT 故偏离）。`TxManager.RunInTx` 顶层经 `setLocalTenant` 注入 `set_config('app.tenant_id', $1, true)`（bind 参零插值；nested savepoint 不重发；空 tenant fail-closed；优先 `tenant.ScopeFromContext` fallback `ctxkeys.TenantIDFrom`）。新 `tenant.WithScope`/`ScopeFromContext` 专用 sealed ctx key。pgxpool `PrepareConn` 深度防御（scope-bearing ctx 绕过 RunInTx 直借连接→`(true,err)` clean fail-fast；非 security primitive）。config 读路径收口经 `cells/configcore/internal/scopedread.Do`（configreader + featureflag 全部读过 tenant-scoped RunInTx；唯一 WithScope 调用点）。schema_guard `verifyRLS` 随 /readyz 起验。守卫：`PG-SETLOCAL-FUNNEL-01`、`TENANT-TXSCOPE-WRITE-CALLER-01`、`verifyRLS`。`[F-B11]`：应用 PG role 非 owner + `NOBYPASSRLS`（部署配置，非 migration；integration 断言 `rolbypassrls=false`）。
- **PR-3b（#1617，defer）**：accesscore 三表（users/roles/role_assignments）RLS + `sessions.tenant_id` 载体 + session.Store typed param + login 2-tx 拆分（重验计时-oracle）+ refresh 重构（载体=session 行）+ sessionvalidate txRunner + rbacassign 契约加 tenantId。风险隔离的敏感 auth 手术。
- **audit_entries RLS（#1618，defer）**：hash chain 是 namespace-global（`UNIQUE(namespace,seq_no)` + tail/prev-hash/Verify 跨租户读），需先重构为 per-`(namespace,tenant)` 才能上 RLS；当前 audit 读隔离由 app 层 `AuditFilters.TenantID`（PR-2a）保障。
- **AI-robust Hard-upgrade（#1619，won't-do-now）**：`PG-SETLOCAL-FUNNEL-01` Prong2 / `TENANT-TXSCOPE-WRITE-CALLER-01` 的 sealed GUC-write / scoped-tx handle 升级（#851/#893/#1282 同族 Go 可见性天花板）。
- PR-4：`RowScope` typed 位置参 + `ROWSCOPE-REPO-PARAM-FUNNEL-01`。
- PR-5：身份 → RowScope 收窄 + `RowScope=all` 强制审计。
- PR-6..10：ABAC policy 域模型 / 评估引擎 / `Authorizer` 重做 / policymanage / 接线（#914）。
- PR-11/12：`pkg/projection.ResourceProjection` sealed + `FieldMask` 列 masking。
- PR-13：跨层 ADR ×3 + 扇出收口。
