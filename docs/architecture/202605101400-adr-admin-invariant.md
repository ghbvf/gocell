# ADR: Admin 不变量（"至少一个 admin"）

**Date**: 2026-05-10
**Status**: Proposed
**Related plan**: `docs/plans/202605082145-034-pg-corecell-b-route-plan.md` S1
**Related ADRs**:

- `docs/architecture/202605101200-adr-typed-go-heavy-protocol-primitives.md`（typed-Go-heavy 范式）
- `docs/architecture/202605101400-adr-credential-session-protocol.md`（credential/session 协议；本 ADR 与之同 PR 落地）

---

## 1. Context

`accesscore` cell 当前对 "admin" 角色的不变量是隐式约定：`internal/adminprovision/provisioner.go` 的 `Ensure()` 用 `CountByRole("admin")` 快速路径 + 进程内 mutex 保证"首次创建幂等"。但 PR#417 §12 指出：**这是产品/架构决策，不应由 PG unique index 隐式决定**。

mem 模式下 admin 的语义保持隐式；PG schema 落地时必须显式选择以下两条之一：

- **至少一个 admin**：允许多个 admin 并存；禁止删除最后一个 admin；可有 0 admin（系统未初始化态）→ 1 admin（首次 setup）→ N admin（运维实践）
- **只能一个 admin**：系统永远只有 0 或 1 个 admin；新增第二个 admin 必须先经过显式交接流程（同 tx demote 旧 + promote 新，禁止双 admin 共存）

PR#417 §12 倾向"至少一个"，但未拍板。本 ADR 锁定决议。

### 1.1 当前实现摘要（mem 模式）

| 文件 | 行为 |
|---|---|
| `cells/accesscore/internal/adminprovision/provisioner.go:76-174` | `Ensure()`：fast-path `CountByRole("admin") == 0` → 创建 admin user + role assignment；进程内 mutex 序列化；返回 `OutcomeCreated` / `OutcomeAlreadyExists` / `OutcomeRaceSkipped` |
| `cells/accesscore/slices/setup/handler.go` | `POST /api/v1/access/setup/admin` 调 `Provisioner.Ensure()`；handler 用 `auth.Route{Bootstrap: true}` 保护（HTTP Basic Auth + per-IP 限流） |
| `cells/accesscore/slices/identitymanage/` | 现有 ChangePassword / Lock / Delete 路径无 "如果是最后一个 admin 拒绝" 校验；mem 模式下"删最后一个 admin 后系统失能"作为运维风险隐式存在 |

### 1.2 关联 backlog

- **B2-C-02 SETUP-ADMIN-PUBLIC-ROUTE-PERMANENT** 🔴 P0：setup endpoint 当前常驻 Public Route，应在 admin 存在后转 409；本 ADR 决议直接为 B2-C-02 提供 setup lifecycle 形态依据
- **B2-PROVISIONER-MUTEX-REVIEW** 🟠 P2：PG 落地后审视 mutex 是否仍需；本 ADR 决议 + S3+S5 PG schema 落地后自然消化

---

## 2. Decision

**选 "至少一个 admin"**：

1. **可有任意数量的 admin**（≥ 0）；上限不强制
2. **删除最后一个 admin 必须被拒**（业务校验）
3. **handoff 流程**：先 grant 新 admin → 再 revoke 旧 admin。**禁**同 tx 完整 swap；强制存在"两个 admin 并存的中间状态"
4. **setup endpoint lifecycle**：仅在 `count(admin) == 0` 时可用；`count(admin) >= 1` 时 endpoint 返回 `409 ERR_AUTH_ADMIN_ALREADY_EXISTS`；endpoint 始终挂 PrimaryListener（`auth.Route{Bootstrap: true}` HTTP Basic Auth 保护，见 §3.3）
5. **0 admin → 1 admin 的过渡**：仅由 setup endpoint（受 Bootstrap HTTP Basic Auth 保护）触发；后续 admin 由现有 admin 通过 RBACAssign 创建

### 2.1 Alternatives Considered

| 方案 | 评价 | 否决理由 |
|---|---|---|
| 至少一个（本 ADR）✅ | 符合交接 / 应急管理员 / 当前 mem 行为 / 常见运维需求 | — |
| 只能一个 | 简单（PG partial unique index `WHERE role='admin'` 一行 SQL）；admin 语义"系统主控"清晰 | 无法应急（admin 失能 → 系统失能）；交接必须同 tx swap，无错误恢复窗口；与"团队多管理员"运维模式冲突 |

---

## 3. Consequences

### 3.1 cell 内 domain rule

```go
// cells/accesscore/internal/domain/admin.go (S3+S5 PR 落地，本 ADR 锁定语义)

// LastAdminGuard rejects operations that would remove the only remaining admin.
type LastAdminGuard struct{ count func(ctx) (int, error) }

// CheckRemove 返回 ErrLastAdminCannotBeRemoved（已声明 errcode）当 user 是 admin
// 且 admin 总数 == 1。
func (g *LastAdminGuard) CheckRemove(ctx context.Context, userID string, hasAdminRole bool) error {
    if !hasAdminRole {
        return nil
    }
    n, err := g.count(ctx)
    if err != nil {
        return err
    }
    if n <= 1 {
        return errcode.New(errcode.ErrAuthLastAdminProtected,
            "cannot remove the last admin")
    }
    return nil
}
```

调用点（S3+S5 PR）：

- `identitymanage.DeleteUser` 入口
- `identitymanage.ChangeUserStatus(Locked)` 入口（lock 视同"暂时不可用"，对 last admin 也拒）
- `rbacassign.RevokeRole("admin")` 入口

### 3.2 PG schema 约束（S3+S5 PR）

**不**使用 partial unique index `WHERE role='admin'`（那是"只能一个" 的工具）。

改用：

- 应用层 `LastAdminGuard.CheckRemove` 在 tx 内执行
- DB 兜底：`role_assignments` 表上加 `BEFORE DELETE` trigger（行级），当被删行 `role='admin'` 且 `(SELECT COUNT(*) FROM role_assignments WHERE role='admin') = 1` 时 `RAISE EXCEPTION 'last_admin_protected'`
- trigger 不替代应用层校验（应用层错误码更精准），是 DB 兜底防直连 SQL 误删

### 3.3 setup endpoint lifecycle

setup endpoint 形态在 S3+S5 PR 落地，但语义本 ADR 锁定：

```
GET  /api/v1/access/setup/admin  → 200 if count(admin)==0 else 409 (with retry-hint)
POST /api/v1/access/setup/admin  → 201 if count(admin)==0
                                  → 409 ERR_AUTH_ADMIN_ALREADY_EXISTS otherwise
```

本 ADR 决议 setup endpoint 始终挂 PrimaryListener，由 `auth.Route{Bootstrap: true}` 提供 HTTP Basic Auth (env credentials) 保护；count(admin) >= 1 时返回 409 ERR_AUTH_ADMIN_ALREADY_EXISTS。**本决议替代 backlog B2-C-02 提议的 'setup endpoint 移到 /internal/v1/setup/'**——理由：(1) InternalListener 用 service token 鉴权（cell-to-cell RPC 体系），不适合运维首次入口；(2) PrimaryListener Bootstrap auth 已为此场景设计；(3) Vault / K8s / Keycloak 均采用 '默认收敛暴露面 + 明确生命周期' 范式（参见 reviewer 主题 C 对照），409 + bootstrap-only lifecycle 满足该范式，无需 listener 切换。

实现位置（S3+S5 PR）：
- `cells/accesscore/slices/setup/handler.go` — GET/POST handler，count(admin) 逻辑
- `auth.Route{Bootstrap: true}` 保护（HTTP Basic Auth + per-IP token-bucket + `subtle.ConstantTimeCompare`）
- endpoint 路径保持 `/api/v1/access/setup/admin`，始终挂 PrimaryListener

### 3.4 handoff 流程

旧 admin Alice 把权限交给新 admin Bob：

1. Alice 调 `POST /api/v1/access/users/<bob-id>/roles { "role": "admin" }`（grant）
2. 此时 admin count = 2（Alice + Bob 并存）
3. Alice 或 Bob 调 `DELETE /api/v1/access/users/<alice-id>/roles/admin`（revoke Alice）
4. admin count = 1（Bob）

**禁**：把上述 1+3 合并为单 tx swap。理由：失败可恢复（步骤 2 失败 Alice 仍有权；步骤 3 失败 Alice 仍有权可重试）。

### 3.5 与 PR262 / typed AuthPlan 的关系

本 ADR 是**纯产品语义决议**，不引入 typed Go primitive（admin 不变量是 cell 业务规则，不是跨 cell 协议）。runtime/auth 不出现 `AdminProtocol` 之类的 sealed type。

---

## 4. Migration

mem 模式当前已是"至少一个 admin"语义（`Provisioner.Ensure` 仅在 count=0 时创建；删 admin 路径无校验，但 demo 流量下不触发）。本 ADR 锁定后：

| 阶段 | 工作 | PR |
|---|---|---|
| S1（本 PR） | ADR 落地；无代码改动 | refactor/547 |
| S3+S5 | PG schema 落地：`role_assignments` 表 + last-admin trigger；`LastAdminGuard` domain 实现；setup endpoint lifecycle 切换 | TBD |
| S4 | accesscore composition root 注入 `LastAdminGuard` 给 identitymanage / rbacassign service | TBD |

**不向后兼容**：S3+S5 落地后，旧 mem 路径下"无校验删 admin" 行为消失；任何依赖该旧行为的测试必须更新（GoCell 项目无外部消费方，按 CLAUDE.md "Review 和重构时不考虑向后兼容" 原则）。

---

## 5. References

- `docs/reviews/202605082044-pr417-pg-corecell-framework-analysis.md` §12 — admin 不变量 决策点（PR#417 review 倾向"至少一个"）
- `docs/plans/202605082145-034-pg-corecell-b-route-plan.md` §4 S1 — admin 不变量 ADR 出处
- `docs/plans/202605082130-pg-corecell-open-issues.md` — B2-C-02 SETUP-ADMIN-PUBLIC-ROUTE-PERMANENT / B2-PROVISIONER-MUTEX-REVIEW
- `cells/accesscore/internal/adminprovision/provisioner.go` — 当前 mem 模式 Ensure 实现
- `cells/accesscore/slices/setup/handler.go` — 当前 setup endpoint
- CLAUDE.md `## 工作方式` — "Review 和重构时不考虑向后兼容" 原则
