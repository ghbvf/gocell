# ADR-AUTH-SETUP-01: First-Run Admin Bootstrap

**Status**: Accepted
**Date**: 2026-04-18
**Author**: AUTH-SETUP-01 工作流
**Deciders**: GoCell 工程团队

---

## Context

首次部署 GoCell 时，access-core cell 中没有任何 user，因此没有 admin 角色持有者。
历史实现在 `examples/sso-bff/main.go` 用 `slog.Info("seed admin ready", slog.String("password", seedAdminPass))` 把随机生成的 admin 明文密码写入 stdout，容器日志会被 Loki/CloudWatch/Datadog 等系统长期采集，属于 PR#172 review F1 标记的安全反模式。

同时该路径没有 production-grade 出口——dev 模式 seed 一旦关闭，生产部署根本无法创建第一个 admin。

### 调研背景

Agent C 调查了 7 个主流产品（PostgreSQL/Vault/Keycloak/GitLab/etcd/Grafana/MinIO），无一例外通过 stdout/文件/env var/stdin 等带外通道完成首次 admin bootstrap。没有任何产品走 HTTP 端点完成首次 admin 创建：HTTP bootstrap 端点是鸡蛋悖论——认证根尚未建立时，端点无法可靠认证调用者。

Agent A 调查了 Kubernetes，发现 K8s v1.20 移除 `--insecure-bind-address=127.0.0.1` 是因为 CVE-2020-8558 证明容器环境下 `127.0.0.1` 边界不可信，本项目原方案 D（独立内网 listener）的前提被证伪。

---

## Decision

采用**方案 H**：对标 GitLab `/etc/gitlab/initial_root_password` + Keycloak v26 `kc.sh bootstrap-admin` 的带外凭据通道模式。

### 核心机制

1. **启动期检测**：access-core 在 `Init()` 阶段调用 `Bootstrapper.Run()`，通过 `roleRepo.CountByRole("admin")` 检测是否存在 admin 用户。
2. **随机密码生成**：若无 admin，调用 `initialadmin.GeneratePassword()` 用 `crypto/rand` 生成 32 字节、base64url no-pad 编码的随机密码。
3. **双通道输出**：
   - `slog.Warn("initial admin created", slog.String("username", "admin"), slog.String("credential_file", path))` — **绝不**把 password 作为 slog attr 写入
   - 原子写文件（`O_EXCL + rename`，权限 `0600`，目录 `0700`），默认路径 `/run/gocell/initial_admin_password`，可通过 `GOCELL_STATE_DIR` env var 覆盖
4. **凭据文件格式**（见 `initialadmin.WriteCredentialFile`）：

   ```
   # GoCell initial admin credential
   # Generated at: <ISO8601>
   # Expires at:   <ISO8601>
   # This file is auto-deleted by the cleanup worker.
   username=<username>
   password=<password>
   expires_at=<unix timestamp>
   ```

5. **24h TTL worker**：通过 `runtime/worker.Worker` 接口实现的 cleaner（`time.AfterFunc`），由 `WithBootstrapWorkerSink` 桥接到 `bootstrap.WithWorkers`，超时后调用 `RemoveCredentialFile`（先校验 mode 防篡改再删除）。
6. **PasswordResetRequired 强制**：
   - 创建 admin user 时标记 `domain.User.PasswordResetRequired = true`
   - `sessionlogin.Service.Login` 从 user 读 flag，写入 `IssueOptions.PasswordResetRequired`
   - JWT claim `password_reset_required: true`（false 时省略以压缩 token 体积）
   - `AuthMiddleware.ServeHTTP` 在 `VerifyIntent` 成功后追加检测：若 `claims.PasswordResetRequired && !isPasswordResetExempt(method, path)` → 403 `ERR_AUTH_PASSWORD_RESET_REQUIRED`
   - Exempt 端点（硬编码）：`POST /api/v1/access/users/{id}/password`（改密）、`DELETE /api/v1/access/sessions/{id}`（登出）
7. **ChangePassword 自动脱困**：`identitymanage.Service.ChangePassword` 在 bcrypt 校验旧密码 → 更新密码 → `ClearPasswordResetRequired` 后，**同步签发新 TokenPair**（新 token 不含 reset claim），客户端拿到新 token 即可访问业务接口，避免"改密成功但所有请求仍 403"的 UX 陷阱。

### IssueOptions 重构（T2）

`JWTIssuer.Issue` 签名重构为：

```go
func (j *JWTIssuer) Issue(intent TokenIntent, subject string, opts IssueOptions) (string, error)
```

其中 `IssueOptions` 聚合了原本的散参数：

```go
type IssueOptions struct {
    Roles                 []string
    Audience              []string
    SessionID             string
    PasswordResetRequired bool
}
```

此为 backlog T2 的触发条件（Issue() 第 6 参），本 PR 顺带完成，消除参数列表持续膨胀的技术债。

### 关键约束

- `cmd/core-bundle` 删除 `buildAdminOpts` 整段，不再读取 `GOCELL_ADMIN_USER` / `GOCELL_ADMIN_PASS` env var（breaking change，接受）
- `examples/sso-bff` 删除 `generateDevPassword` 及明文 slog.Info 路径
- 竞态处理：多进程同时 bootstrap 时，`userRepo.Create` 返回 `ErrAuthUserDuplicate` → silent skip + recount confirm（见 `Bootstrapper.Run` 实现）

---

## Consequences

### 正面影响

- **PR#172 review F1 彻底解决**：明文密码不再出现在任何 slog 输出，消除容器日志安全风险
- **与对标产品行为一致**：GitLab/Keycloak/Vault 均采用相同模式，运维团队有成熟经验
- **server-side enforcement 闭环**：攻击者即使获得泄漏的 bootstrap 密码，也只能执行改密操作（middleware 拦截其他端点），无法无限制访问系统
- **T2 技术债还清**：IssueOptions 重构落地，后续扩展新 claim 不再需要修改函数签名

### 负面影响 / 已知限制

- **运维 bootstrap 流程新增一步**：需要读取凭据文件（`cat /run/gocell/initial_admin_password` 或 `kubectl exec`），对比历史"直接看日志"稍复杂，但与对标产品一致
- **`GOCELL_ADMIN_USER` / `GOCELL_ADMIN_PASS` 不再生效**：现有使用这两个 env var 的部署脚本需更新
- **macOS 开发需要手动 export `GOCELL_STATE_DIR`**（`/run/` 在 macOS 不存在）
- **多副本 PG 竞态防护当前靠 unique constraint + ErrAuthUserDuplicate silent skip**，PG advisory lock 加固已列入 backlog T-PG-ADVISORY-LOCK-01，X1 PG-DOMAIN-REPO 上线时一并实现

---

## Rejected Alternatives

### 方案 D — 独立内网 Listener（originally proposed）

**原始方案**：在启动期绑定一个仅监听 `127.0.0.1` 的独立 HTTP 端口，暴露 `POST /bootstrap/admin` 端点，认为 localhost boundary 可以隔离外部访问。

**否决理由**：K8s CVE-2020-8558 和 CVE-2020-8559 证明，在容器化/Pod 共享网络命名空间环境下，`127.0.0.1` 边界**不可信**。同一 Pod 内的任意容器（包括 sidecar、init container）均可访问 loopback 地址，恶意 sidecar 可直接调用 bootstrap 端点。K8s 因此在 v1.20 移除了 `--insecure-bind-address` 功能，官方安全公告明确指出"localhost is not a security boundary in containers"。

**参考**：
- https://github.com/kubernetes/kubernetes/issues/91506
- https://github.com/kubernetes/kubernetes/issues/92315

**历史价值保留**：方案 D 促使了对"绑定地址是否构成安全边界"的深入调查，调查结论最终指向了方案 H。

### 方案 F — API 级 IP 过滤中间件

**描述**：在公共 HTTP server 上对 `/bootstrap/admin` 端点增加 IP 过滤中间件，只允许来自 `127.0.0.1` 或私有 IP 段的请求。

**否决理由**：IP 过滤依赖 `X-Forwarded-For` / `X-Real-IP` header 的信任链配置正确性——一次代理跳数（`--proxy-count`）配置错误，或 ingress 不剥离 XFF header，即可导致攻击者伪造 IP 绕过过滤、公网暴露 bootstrap 端点。GitLab/Rails 历史上出现过多次与 XFF 信任链相关的 CVE，证明该模式在生产环境中脆弱。

### HTTP 公网 Setup Endpoint（任何形式）

**描述**：任何形式的"首次设置"HTTP 端点，无论是公网暴露还是依赖 IP 过滤/请求体内密钥校验。

**否决理由**：鸡蛋悖论——认证根（第一个 admin）尚未建立时，端点无法可靠认证调用者。即使加入"初始化令牌"机制，该令牌本身的分发又是一个同样的问题。Agent C 对 7 个主流产品的调研结论：PostgreSQL 用 `peer` 认证本地 socket、Vault 用 `operator init` 标准输出、Keycloak v26 用 `kc.sh bootstrap-admin`、GitLab 用 `/etc/gitlab/initial_root_password` 文件、etcd 用 auth API 本地 curl、Grafana 用 `GF_SECURITY_ADMIN_PASSWORD` env var、MinIO 用 `MINIO_ROOT_PASSWORD` env var。**无一例外使用带外通道，没有任何产品走 HTTP 端点完成首次 admin 创建**。

---

## References / 对标

| 产品 | 机制 | 参考链接 |
|------|------|---------|
| GitLab | `/etc/gitlab/initial_root_password`，首次 reconfigure 生成，24h TTL 后自动删除 | https://docs.gitlab.com/security/reset_user_password/ |
| Keycloak v26 | `kc.sh bootstrap-admin`，带外 CLI 命令写入 realm admin | https://www.keycloak.org/server/bootstrap-admin-recovery |
| HashiCorp Vault | `vault operator init`，标准输出 unseal keys + root token，单次输出不存储 | https://github.com/hashicorp/vault/blob/main/command/operator_init.go |
| K8s CVE-2020-8558 教训 | localhost 不是容器安全边界，直接否决了方案 D | https://github.com/kubernetes/kubernetes/issues/92315 |

---

## Implementation Notes

实现分 5 个 Phase 落地：

| Phase | 内容 | 状态 |
|-------|------|------|
| 1 | `initialadmin` 工具包（generator + credfile + cleaner） | 完成 (commit 6b10bfb) |
| 2 | TTL Cleaner worker | 完成 (commit 1a60d7e) |
| 3 | Bootstrapper + domain.PasswordResetRequired + 删除 WithSeedAdmin | 完成 (commit ac9d9f6) |
| 3.5 | JWT IssueOptions 重构 (T2) + AuthMiddleware password-reset enforcement | 完成 (commit 2fed6ae) |
| 3.6 | ChangePassword + RequirePasswordReset full closure | 完成 (commit 6698b31) |
| 4 | cmd/core-bundle + sso-bff + e2e walkthrough cutover | 完成 (commit e60293b) |
| 5 | ADR Accepted + 运维手册 + backlog 更新 | 完成（本 commit） |
