# PR #376 Review — 安全/权限维度

PR: feat(codegen): PR-V1-CODEGEN-FULL-MIGRATION (方案 D PR-4 末段)
HEAD: 446930ad / Base: origin/develop

## Summary

- 总体结论: 需修复（P1 × 2，P2 × 2）
- Findings: P0=0 P1=2 P2=2
- Cx: Cx1=2 Cx2=1 Cx3=1 Cx4=0

---

## Findings

### F-SEC-001 [P1] [Cx1] generated/contracts/http/auth/login/v1/handler_gen.go:68-74
**问题**: 生成的 login handler 在 public（JWT-exempt）端点上对 `password` 字段暴露 `"password: value too short"` / `"password: value too long"` 的 400 错误消息。这构成**密码长度 oracle 攻击**：匿名攻击者可通过测试不同长度确认目标账户的密码是否符合长度约束。

**证据**:
```go
if len(req.Password) < 8 {
    httputil.WriteError(r.Context(), w, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed, "password: value too short"))
    return
}
if len(req.Password) > 72 {
    httputil.WriteError(r.Context(), w, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed, "password: value too long"))
    return
}
```

根源在 `handler.tmpl` 对所有带 minLength/maxLength 字段统一生成校验，无 password 特例。

**建议**: 在 `handler.tmpl` 中识别 `password|passwd|pwd`（不区分大小写）字段，跳过 minLength/maxLength 暴露，对长度违规统一降级为 `"invalid credentials"` 或让 service 层处理。

### F-SEC-002 [P1] [Cx3] kernel/governance + tools/codegen/contractgen/builder.go
**问题**: 没有静态守卫检查 `auth.public: true` 不能出现在 `/internal/v1/` 路径的 contract.yaml 上。现有防线（`runtime/auth.validateBypassCompatibility()` 仅查 Public+PasswordResetExempt 互斥；`runtime/http/router.FinalizeAuth()` 拒绝 `/internal/v1/*` 挂在 PrimaryListener）都是**运行时**拒绝而非**契约层**静态拒绝。

当前三个 internal endpoint（`http.auth.role.assign.v1`、`http.auth.role.revoke.v1`、`http.config.internal.get.v1`）均未设置 `public: true`，**不存在已有违规**。风险是未来 codegen 全覆盖后 `auth` 字段误写概率增加。

**建议**: (1) `kernel/governance/rules_fmt.go` 新增规则：`kind=http && path starts with /internal/v1/ && auth.public == true → SeverityError`；(2) `tools/codegen/contractgen/builder.go` 的 `buildHTTPEndpointSpec` 在该组合下 fail-fast；(3) 补 table-driven test。

### F-SEC-003 [P2] [Cx1] contracts/http/auth/refresh/v1/contract.yaml:16
**问题**: `auth.refresh.v1` 标记 `auth.public: true`（refresh token 自身就是凭据，不依赖 JWT，public 可理解）。但意味着保护完全依赖 refresh token 的不可猜测性 + service 层验证。应显式注释说明意图。

**建议**: 在 `auth` 字段加 `# public: refresh token is itself the credential; JWT verification is inapplicable` 注释。

### F-SEC-004 [P2] [Cx1] cells/accesscore/slices/sessionlogout/handler.go:57
**问题**: `deletegen.NewHandler(DeleteAdapter{svc}, nil)` 传入 nil policy。设计合理（ownership 在 service 层 `callerUserID` 校验），但 `NewHandler` 注释未明确表达"所有权校验在 service 层"。

**建议**: 注释更新为 `// nil policy: ownership enforced in service layer (callerUserID vs session.user_id); no route-level policy needed.`。

---

## public: true 完整清单（已穷举）

| Contract | Path | 是否合理 |
|---|---|---|
| `http.auth.login.v1` | `POST /api/v1/access/sessions/login` | ✓ 合理 |
| `http.auth.refresh.v1` | `POST /api/v1/access/sessions/refresh` | ✓ 见 F-SEC-003 |
| `http.auth.setup.status.v1` | `GET /api/v1/access/setup/status` | ✓ 合理 |
| `http.auth.setup.admin.v1` | `POST /api/v1/access/setup/admin` | ✓ 合理（自带 410 Gone） |

**确认无 internal path + public: true 组合**。

## passwordResetExempt: true 完整清单

| Contract | Path | 是否合理 |
|---|---|---|
| `http.auth.session.delete.v1` | `DELETE /api/v1/access/sessions/{id}` | ✓ 合理 |
| `http.auth.user.change-password.v1` | `POST /api/v1/access/users/{id}/password` | ✓ 合理 |

---

## 复杂度汇总
Cx1: 2 / Cx2: 0 / Cx3: 1 / Cx4: 0

## 修复分流建议
- F-SEC-001/003/004（Cx1）→ 派发 developer agent
- F-SEC-002（Cx3）→ 派发 developer agent，但需 architect 确认 governance 规则编号

## 总体结论
**需修复**（P1 × 2）。主要风险：(1) login handler password 字段长度 oracle；(2) `auth.public: true` + internal path 互斥缺静态规则。

---

## Out of scope（其他维度疑似问题）
1. **[架构]** `contractIDToPackagePath` 在 contractgen 与 archtest 各有一份独立实现，存在漂移风险
2. **[测试]** `auth.refresh` 缺少 refresh token 重放保护的 contract test
3. **[可维护性]** generated 校验代码（如 `if len(req.Password) < 8`）不在 `contract.yaml` 中声明，gocell check contract-health 的 CH-04 不扫生成代码
4. **[运维]** `http.config.internal.get.v1` 内部 API 响应包含 `sensitive: bool` 和 `value: string`，需确认 accesscore 确实需要原始 sensitive value
5. **[DX]** `policy: nil` 模式没有 archtest 守护，依赖人工审查
