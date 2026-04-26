# GoCell First-Run Admin Bootstrap

> 本文档专注**运维侧部署细节**：凭据文件路径、Docker / Kubernetes / macOS / Windows 配置、密码重置流程。
> 选型决策（何时选 interactive vs bootstrap）、`410 Gone` 客户端处理、`400` / `409` / `410` 区分见 [`docs/guides/admin-bootstrap-paths.md`](../guides/admin-bootstrap-paths.md)。

## 概览

GoCell 支持两种 first-run admin 模式：

- `interactive`（`cmd/corebundle` 默认）：启动时不创建 admin。运维通过
  `POST /api/v1/access/setup/admin` 创建第一个 admin，成功后该 endpoint 永久返回 410。
- `bootstrap`：启动时由 accesscore lifecycle 自动检测 admin role 是否有 user。若无，
  生成随机密码并写入凭据文件（0600 权限），同时启动 24h TTL worker 自动销毁该文件。
  运维通过读取凭据文件获取首登密码，登录后被强制改密（middleware 拦截非改密/登出端点）。

`cmd/corebundle` 通过 `GOCELL_ACCESSCORE_ADMIN_PROVISION_MODE` 选择模式。只接受空值、
`interactive`、`bootstrap`；其他值会启动失败，避免拼写错误把 headless 部署意外暴露给
public interactive setup endpoint。

`POST /api/v1/access/setup/admin` 的交互式密码必须是 8-72 个可打印 ASCII 字节。
这个限制与 bcrypt 的 72-byte 输入上限一致，并且让公开 contract schema 与服务端校验使用同一语义。

对标：GitLab `/etc/gitlab/initial_root_password` + Keycloak v26 `kc.sh bootstrap-admin`。

设计决策详见 `docs/architecture/202604181900-adr-auth-setup-first-run.md`。

---

## 凭据文件位置

GoCell 按操作系统选择不同的默认路径：

| 操作系统 | 默认凭据文件路径 |
|----------|-----------------|
| Linux | `/run/gocell/initial_admin_password`（systemd RuntimeDirectory 惯例，tmpfs 不写磁盘） |
| macOS | `~/Library/Application Support/gocell/run/initial_admin_password` |
| Windows | `%LOCALAPPDATA%\gocell\run\initial_admin_password` |

通过环境变量覆盖（所有操作系统均适用）：

```
GOCELL_STATE_DIR=/var/lib/gocell
```

覆盖后文件路径：`/var/lib/gocell/initial_admin_password`（拼接 `/initial_admin_password`）

目录权限：`0700`，文件权限：`0600`（Windows 使用 DACL 限制为进程所有者独占访问）

文件格式（由 `cells/accesscore/initialadmin/credfile_io.go::writeCredentialFile` 生成）：

```
# GoCell initial admin credential
# Generated at: 2026-04-18T19:00:00Z
# Expires at:   2026-04-19T19:00:00Z
# This file is auto-deleted by the cleanup worker.
username=admin
password=<base64url-no-pad-random-32bytes>
expires_at=<unix timestamp>
```

---

## Docker / Docker Compose

凭据文件在容器内写入，需要使宿主或部署工具可读。有两种方式：

### 方式 A — bind mount 到宿主目录

```yaml
services:
  gocell:
    image: gocell:latest
    volumes:
      - ./gocell-state:/run/gocell
    environment:
      - GOCELL_STATE_DIR=/run/gocell
```

启动后在宿主机读取：

```bash
docker compose up -d
cat ./gocell-state/initial_admin_password
```

### 方式 B — docker exec 读取（无 bind mount）

```bash
docker compose up -d
docker compose exec gocell cat /run/gocell/initial_admin_password
```

---

## Kubernetes

凭据文件需挂 emptyDir Volume（建议 `medium: Memory` 使用 tmpfs，不写磁盘），运维通过 `kubectl exec` 读取：

```yaml
apiVersion: v1
kind: Pod
metadata:
  name: gocell
spec:
  containers:
    - name: gocell
      image: gocell:latest
      env:
        - name: GOCELL_STATE_DIR
          value: /run/gocell
      volumeMounts:
        - name: state
          mountPath: /run/gocell
  volumes:
    - name: state
      emptyDir:
        medium: Memory
```

读取凭据：

```bash
kubectl exec -it gocell -- cat /run/gocell/initial_admin_password
```

注意：Pod 重启后 emptyDir 内容消失。若 24h TTL 内 Pod 重启，凭据文件丢失，但 admin user 已存在，bootstrap 不会重复执行。此时需走管理员密码重置流程。

---

## macOS 开发

`/run/` 在 macOS 上不存在。GoCell 自动使用 `~/Library/Application Support/gocell/run/` 作为
默认状态目录，无需额外配置即可运行：

```bash
go run ./examples/ssobff
```

读取凭据：

```bash
cat ~/Library/Application\ Support/gocell/run/initial_admin_password
```

如需自定义路径，仍可通过环境变量覆盖：

```bash
export GOCELL_STATE_DIR=$TMPDIR/gocell
go run ./examples/ssobff
cat $TMPDIR/gocell/initial_admin_password
```

## Windows 开发

GoCell 自动使用 `%LOCALAPPDATA%\gocell\run\` 作为默认状态目录，无需额外配置：

```powershell
go run ./examples/ssobff
```

读取凭据：

```powershell
Get-Content "$env:LOCALAPPDATA\gocell\run\initial_admin_password"
```

如需自定义路径，通过环境变量覆盖：

```powershell
$env:GOCELL_STATE_DIR = "C:\gocell-state"
go run ./examples/ssobff
```

注意：凭据文件使用 Windows DACL 设置为进程所有者独占访问，等效于 Unix 的 `0600` 权限。

---

## 完整 Bootstrap 到改密流程演示

```bash
# 1. 启动服务（首次）
docker compose up -d

# 2. 读取凭据文件
docker compose exec gocell cat /run/gocell/initial_admin_password
# 输出示例：
# # GoCell initial admin credential
# # Generated at: 2026-04-18T19:00:00Z
# # Expires at:   2026-04-19T19:00:00Z
# # This file is auto-deleted by the cleanup worker.
# username=admin
# password=dGhpcyBpcyBhIHRlc3QgcGFzc3dvcmQ
# expires_at=1745107200

INIT_PASS="dGhpcyBpcyBhIHRlc3QgcGFzc3dvcmQ"

# 3. 用初始密码登录
TOKEN_RESP=$(curl -s -X POST http://localhost:8080/api/v1/access/sessions/login \
  -H 'Content-Type: application/json' \
  -d "{\"username\":\"admin\",\"password\":\"${INIT_PASS}\"}")
echo "$TOKEN_RESP"
# {"data":{"accessToken":"...","refreshToken":"...","sessionId":"sess-...","userId":"usr-...","expiresAt":"...","passwordResetRequired":true}}

ACCESS_TOKEN=$(echo "$TOKEN_RESP" | jq -r '.data.accessToken')
USER_ID=$(echo "$TOKEN_RESP"     | jq -r '.data.userId')

# 4. 试调业务接口 — 被 middleware 拦截
curl -i -X GET http://localhost:8080/api/v1/access/roles/admin \
  -H "Authorization: Bearer $ACCESS_TOKEN"
# HTTP/1.1 403 Forbidden
# {"error":{"code":"ERR_AUTH_PASSWORD_RESET_REQUIRED","message":"password reset required before accessing this endpoint","details":{"changePasswordEndpoint":"POST /api/v1/access/users/{id}/password"}}}

# 5. 改密（同步拿到新 TokenPair，自动脱困）
NEW_TOKEN_RESP=$(curl -s -X POST "http://localhost:8080/api/v1/access/users/${USER_ID}/password" \
  -H "Authorization: Bearer $ACCESS_TOKEN" \
  -H 'Content-Type: application/json' \
  -d "{\"oldPassword\":\"${INIT_PASS}\",\"newPassword\":\"MyStr0ngP@ssword!\"}")

NEW_ACCESS_TOKEN=$(echo "$NEW_TOKEN_RESP" | jq -r '.data.accessToken')
echo "passwordResetRequired=$(echo "$NEW_TOKEN_RESP" | jq -r '.data.passwordResetRequired')"
# passwordResetRequired=false

# 6. 用新 token 访问业务接口 — 放行
curl -i -X GET http://localhost:8080/api/v1/access/roles/admin \
  -H "Authorization: Bearer $NEW_ACCESS_TOKEN"
# HTTP/1.1 200 OK

# 7. 24h 后凭据文件由 TTL worker 自动销毁
#    届时文件不存在，ls 返回非零
docker compose exec gocell ls /run/gocell/initial_admin_password 2>&1 || echo "file removed by cleaner"
```

---

## 多副本 / HA 部署

当前 in-memory repo（开发模式）下，单进程内多次 bootstrap 由 `Bootstrapper.Run` 内置幂等处理（admin 已存在则跳过；并发 duplicate 会 recount 确认是否已有 admin）。

duplicate username 只在同源、同 ID 前缀、`ProvisionState=pending` 的 setup/bootstrap row 上恢复。普通 identity user、不同来源的 pending row、已完成 provisioning row 都不会被覆盖密码或提升为 admin；bootstrap/setup 会返回 duplicate conflict 并由运维处理用户名冲突。

PostgreSQL 模式（X1 PG-DOMAIN-REPO 上线后）：多 pod 同时启动会触发 bootstrap 竞态。当前通过 unique constraint + `ErrAuthUserDuplicate` + same-source pending provenance 防止重复创建和用户接管，但多 pod 均可触发密码生成与凭据文件写入（多文件路径或最后 rename 胜出）。PG advisory lock 加固已列入 backlog T-PG-ADVISORY-LOCK-01，X1 上线时一并实现。

---

## 故障排查

| 现象 | 原因 | 处理 |
|------|------|------|
| 凭据文件不存在 | 已超 24h TTL；或 admin 已存在（跳过 bootstrap） | 业务已过 bootstrap 阶段，用现有 admin login；如忘记密码，走下方"管理员密码重置流程" |
| 文件权限被改成非 0600 | 运维误 `chmod` | `RemoveCredentialFile` 检测到 mode 异常仍会删除文件，并记录 slog.Warn；文件已销毁，如忘记密码走下方重置流程 |
| 启动 hook 超时 "hook.timeout name=accesscore.initial-admin-bootstrap" | `bootstrap.Hook.StartTimeout` 默认 30s 不足以完成 Sweep + EnsureAdmin，或 DB 连接慢 | 通过 `initialadmin.WithScheduler`/`WithClock` 注入确定性依赖降低启动成本；DB 问题单独排查 |
| 启动日志 `hook.start_err name=accesscore.initial-admin-bootstrap` | credential 目录不可写、DB 连接失败等 | 看 `error=` 字段定位根因；auto-discovery 已替代旧 `WithBootstrapWorkerSink`，无需手工接线 |
| 启动失败且日志提到 `GOCELL_ACCESSCORE_ADMIN_PROVISION_MODE` | provisioning mode 拼写错误或不支持 | 设置为空/`interactive`/`bootstrap` 之一；headless 部署通常设为 `bootstrap` |
| 启动失败 "credential dir not writable" | 凭据目录无写权限或路径非法 | 检查 `GOCELL_STATE_DIR` 是否为绝对路径且进程 user 有写权限；macOS 请 `export GOCELL_STATE_DIR=$TMPDIR/gocell` |
| 业务接口 403 + ERR_AUTH_PASSWORD_RESET_REQUIRED | 使用了 bootstrap 密码但未完成改密 | 按流程 Step 5 执行改密，使用响应中的新 accessToken 继续操作 |
| macOS 启动报错写文件失败 | 用户主目录路径解析失败（罕见） | 手动 `export GOCELL_STATE_DIR=$TMPDIR/gocell` 后重启；或检查 `$HOME` 环境变量是否正常 |
| Kubernetes Pod 重启后凭据文件消失 | emptyDir 随 Pod 生命周期 | admin user 已存在，bootstrap 不再执行；如忘记初始密码，需人工 admin reset |

---

## 管理员密码重置流程

### 场景：已知当前密码（正常改密）

已登录的 admin 用户通过 `POST /api/v1/access/users/{id}/password` 改密：

```bash
ACCESS_TOKEN="<your-current-token>"
USER_ID="<your-user-id>"
NEW_PASS="NewStr0ng@Pass!"

curl -s -X POST "http://localhost:8080/api/v1/access/users/${USER_ID}/password" \
  -H "Authorization: Bearer ${ACCESS_TOKEN}" \
  -H "Content-Type: application/json" \
  -d "{\"oldPassword\":\"<current>\",\"newPassword\":\"${NEW_PASS}\"}"
# 返回新 TokenPair，PasswordResetRequired=false
```

改密成功后，响应中携带新 `accessToken`，立即使用新 token 继续操作。

### 场景：忘记 admin 密码（需 DB 直接操作）

> 安全提示：以下操作需要直接访问数据库，必须由有权限的运维人员执行。

当 admin 忘记密码且凭据文件已过期或不存在时，可通过以下 SQL 脚本重置：

```sql
-- 1. 生成新的 bcrypt hash（cost=12，OWASP 2023 推荐）
--    在 Go 中生成：bcrypt.GenerateFromPassword([]byte("NewPass!"), 12)
--    或使用 htpasswd 工具：htpasswd -bnBC 12 "" "NewPass!" | tr -d ':\n'
--    假设生成的 hash 为 $2a$12$...

-- 2. 更新 admin 用户密码 + 强制下次改密（可选）
UPDATE users
SET password_hash = '$2a$12$<your-bcrypt-hash-here>',
    password_reset_required = true,
    updated_at = NOW()
WHERE username = 'admin';

-- 3. 验证更新成功
SELECT id, username, password_reset_required, updated_at
FROM users
WHERE username = 'admin';
```

重置后：
1. 用新密码重新 `POST /api/v1/access/sessions/login`
2. 若 `password_reset_required=true`，按上方"已知当前密码"流程改密

### 场景：bootstrap 过程中磁盘写满（admin 已创建但无凭据文件）

bootstrap 预检（`probeWriteable`）在创建 user 之前验证目录可写，可减少此类情况。
若在创建 user 之后、写凭据文件之前磁盘写满（极罕见），admin user 已存在但密码未知。
此时需人工 SQL 清理后重启：

```sql
-- 1. 找到 bootstrap 创建的 admin user
SELECT id, username, created_at FROM users WHERE username = 'admin' ORDER BY created_at DESC LIMIT 1;

-- 2. 删除 role assignment
DELETE FROM user_roles WHERE user_id = '<bootstrap-user-id>';

-- 3. 删除 user
DELETE FROM users WHERE id = '<bootstrap-user-id>';

-- 4. 重启服务 — bootstrap 将重新执行
```

> 注：真正的事务性解决方案（user 创建与凭据写入原子化）已列入 backlog（Cx3 scope），
> 待 PG adapter 完善后实现。

---

## 安全说明

- **凭据文件 0600 权限**：阻止同节点其他 user 读取，仅 gocell 进程 owner 可访问
- **`/run` tmpfs**：systemd RuntimeDirectory 默认挂载为 tmpfs，reboot 自动清空，不写入持久存储
- **24h TTL 兜底**：即使运维忘记手动删除，24h 后 cleaner worker 自动销毁
- **middleware 拦截**：攻击者即使获得泄漏的 bootstrap 密码，也只能执行改密操作（强制改密后旧密码失效），无法无限制访问系统
- **slog 不含明文密码**：bootstrap 路径只打印 `username` + `credential_file` 路径，密码仅写入 0600 文件，不出现在任何日志流
- **bcrypt cost = 12**（OWASP 2023 推荐），防止离线暴力破解
