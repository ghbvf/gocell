# ADR: Role 命名约定（业务 role: 前缀 / 平台裸名）

**Date**: 2026-06-15
**Status**: Accepted
**Related ADRs**:

- `docs/architecture/202605101400-adr-admin-invariant.md`（admin 不变量；平台保留 admin 角色的语义来源）
- `docs/architecture/202606121400-1348-adr-pr10a-authz-wiring.md`（ABAC 授权接线；role 与 permission 的概念隔离）

---

## 1. Context

GoCell 在 corecells 与 examples 中存在两类 role 字符串常量：

1. **平台保留 role**：`"admin"` / `"superadmin"`，定义于 `framework/runtime/auth/roles.go`（`RoleAdmin` / `RoleSuperAdmin`），被 principal → RowScope 派生（admin → tenant、superadmin → all）和 PDP baseline 规则直接消费。
2. **业务 / 应用 role**：由各 cell 或 example 自定义，例如 `"role:operator"`、`"role:device"`、`"role:customer"`。

在 PR#267 落地时，这条分身约定仅靠 `examples/iotdevice/cells/devicecell/internal/dto/authz.go` 中一条注释（Soft 约定）守卫。没有 ADR 记录决策，没有机器可执行的 enforcement。

此外，`framework/pkg/authz/permission.go` 中存在形如 `"role:read"` 的 permission action 字符串——其前缀形式与业务 role 相同，但语义完全独立（permission action，不是 role）。需要在 ADR 层面明确两者正交，防止歧义。

本 ADR 补录该命名约定并升级 enforcement 为 Medium（archtest `ROLE-PREFIX-NAMESPACED-01`）。

---

## 2. Decision

### 2.1 命名规则

1. **业务 / 应用 role 常量必须带 `"role:"` 前缀**，例如：
   - `"role:operator"`、`"role:device"`（`examples/iotdevice`）
   - `"role:customer"`（`examples/todoorder`）

2. **平台保留 role 使用裸名，单源 `framework/runtime/auth/roles.go`**：
   - `RoleAdmin = "admin"`
   - `RoleSuperAdmin = "superadmin"`

   这两个值不加 `"role:"` 前缀，以保持与 PDP baseline 规则、JWT claim、RowScope 派生逻辑的一致性。

3. **业务 cell 若需等同于平台保留 role，使用裸值 alias**，例如：

   ```go
   // examples/iotdevice/cells/devicecell/internal/dto/authz.go
   RoleAdmin = "admin"  // 对齐 framework/runtime/auth.RoleAdmin
   ```

   alias 不引入新含义，只是在本地给平台保留值一个有意义的常量名。

4. **`"role:"` 前缀命名空间与 permission action 命名空间正交**：`framework/pkg/authz/permission.go` 中的 `"role:read"` 是 permission action（动词：读取 role 资源），不是 role 字符串，不受本约定约束。

### 2.2 Alternatives Considered

| 方案 | 评价 | 否决理由 |
|---|---|---|
| 全裸名（业务 role 不加前缀）| 简洁 | 业务 role 可与 `"admin"` / `"superadmin"` 平台保留名静默碰撞；无命名空间隔离，语义不自文档化 |
| 全 `"role:"` 含平台（`RoleAdmin = "role:admin"`）| 统一前缀 | 破坏 JWT claim 格式、RowScope 派生判断、PDP baseline 规则中对 `"admin"` 的硬编码匹配；与 `ROLE-ADMIN-LITERAL-01` 的 `roleAdminAllowRels` 冲突，影响 auth.RoleAdmin 单源约束 |

---

## 3. Consequences

### 3.1 Enforcement

本 ADR 对应两条 archtest 规则共同执行：

- **`ROLE-ADMIN-LITERAL-01`**（既有，Medium）：禁止在 `runtime/auth/roles.go` 之外的文件复制定义 `const *Admin* = "admin"` 形式的常量，强制消费方使用 `auth.RoleAdmin` 常量。
- **`ROLE-PREFIX-NAMESPACED-01`**（新增，Medium）：扫描 `corecells/` + `examples/` 中所有以 `Role` 开头的导出字符串常量，断言其值要么以 `"role:"` 开头，要么属于 sanctioned 平台裸值集合 `{"admin", "superadmin"}`。含 anti-vacuity 与 RED/GREEN synthetic fixture。

评级均为 **Medium**：AST 字面量扫描 + anti-vacuity，符合 AI-robust 治理章程最低门槛。符号、盲区与评级证明见 `tools/archtest/role_prefix_namespaced_test.go` godoc，不在本 ADR 复述。

### 3.2 对 principal → RowScope 派生的影响

principal → RowScope 派生逻辑（admin → tenant、superadmin → all）依赖的是 JWT claim 中 role 字段的**字符串值**，与本约定正交——平台 role 值保持裸名 `"admin"` / `"superadmin"`，派生行为不受影响。

---

## References

- `framework/runtime/auth/roles.go` — 平台保留 role 的权威定义（`RoleAdmin`、`RoleSuperAdmin`）
- `tools/archtest/role_prefix_namespaced_test.go` — `ROLE-PREFIX-NAMESPACED-01` archtest（符号、盲区、anti-vacuity）
- `tools/archtest/role_admin_literal_test.go` — `ROLE-ADMIN-LITERAL-01` archtest（admin 裸名复制 ban）
- `.claude/rules/gocell/tenancy.md` — principal → RowScope 派生规则（admin → tenant、superadmin → all）
- PR#267 — role 命名约定首次出现（iotdevice authz.go 注释）
