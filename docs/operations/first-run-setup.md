# GoCell First-Run Admin Bootstrap

## 概览

首次启动 GoCell 时，access-core cell 自动检测 admin role 是否有 user。若无，生成随机密码
并写入凭据文件（0600 权限），同时启动 24h TTL worker 自动销毁该文件。运维通过读取凭据文件
获取首登密码，登录后被强制改密（middleware 拦截非改密/登出端点）。

对标：GitLab `/etc/gitlab/initial_root_password` + Keycloak v26 `kc.sh bootstrap-admin`。

设计决策详见 `docs/architecture/202604181900-adr-auth-setup-first-run.md`。

---

## 凭据文件位置

默认路径：`/run/gocell/initial_admin_password`（systemd RuntimeDirectory 惯例，tmpfs 不写磁盘）

通过环境变量覆盖：

```
GOCELL_STATE_DIR=/var/lib/gocell
```

覆盖后文件路径：`/var/lib/gocell/initial_admin_password`

目录权限：`0700`，文件权限：`0600`

文件格式（由 `cells/access-core/internal/initialadmin/credfile.go::WriteCredentialFile` 生成）：

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

`/run/` 在 macOS 上不存在。开发环境必须手动 export：

```bash
export GOCELL_STATE_DIR=$TMPDIR/gocell
go run ./examples/sso-bff
```

读取凭据：

```bash
cat $TMPDIR/gocell/initial_admin_password
```

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
# {"data":{"accessToken":"...","refreshToken":"...","passwordResetRequired":true,...}}

ACCESS_TOKEN=$(echo "$TOKEN_RESP" | jq -r '.data.accessToken')
USER_ID=$(echo "$TOKEN_RESP" | jq -r '.data.userId')

# 4. 试调业务接口 — 被 middleware 拦截
curl -i -X GET http://localhost:8080/api/v1/access/roles/admin \
  -H "Authorization: Bearer $ACCESS_TOKEN"
# HTTP/1.1 403 Forbidden
# {"error":{"code":"ERR_AUTH_PASSWORD_RESET_REQUIRED","message":"password reset required","details":{}}}

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

当前 in-memory repo（开发模式）下，单进程内多次 bootstrap 由 `Bootstrapper.Run` 内置幂等处理（`userRepo.Create` 返回 `ErrAuthUserDuplicate` → silent skip + recount confirm）。

PostgreSQL 模式（X1 PG-DOMAIN-REPO 上线后）：多 pod 同时启动会触发 bootstrap 竞态。当前通过 unique constraint + `ErrAuthUserDuplicate` silent skip 防止重复创建，但多 pod 均可触发密码生成与凭据文件写入（多文件路径或最后 rename 胜出）。PG advisory lock 加固已列入 backlog T-PG-ADVISORY-LOCK-01，X1 上线时一并实现。

---

## 故障排查

| 现象 | 原因 | 处理 |
|------|------|------|
| 凭据文件不存在 | 已超 24h TTL；或 admin 已存在（跳过 bootstrap） | 业务已过 bootstrap 阶段，用现有 admin login；如忘记密码，走 admin reset 流程 |
| 文件权限被改成非 0600 | 运维误 `chmod` | `RemoveCredentialFile` 检测到 mode 异常会返回 `ErrCredFileTampered` 并 slog.Error，拒绝删除；需人工核查并恢复权限或手动删除 |
| 启动失败 "WithBootstrapWorkerSink required" 或类似 panic | main.go 配置错误 | 必须同时调用 `WithInitialAdminBootstrap` + `WithBootstrapWorkerSink`，并将 sink 收到的 worker 传给 `bootstrap.WithWorkers` |
| 业务接口 403 + ERR_AUTH_PASSWORD_RESET_REQUIRED | 使用了 bootstrap 密码但未完成改密 | 按流程 Step 5 执行改密，使用响应中的新 accessToken 继续操作 |
| macOS 启动报错写文件失败 | `GOCELL_STATE_DIR` 未设置 | `export GOCELL_STATE_DIR=$TMPDIR/gocell` 后重启 |
| Kubernetes Pod 重启后凭据文件消失 | emptyDir 随 Pod 生命周期 | admin user 已存在，bootstrap 不再执行；如忘记初始密码，需人工 admin reset |

---

## 安全说明

- **凭据文件 0600 权限**：阻止同节点其他 user 读取，仅 gocell 进程 owner 可访问
- **`/run` tmpfs**：systemd RuntimeDirectory 默认挂载为 tmpfs，reboot 自动清空，不写入持久存储
- **24h TTL 兜底**：即使运维忘记手动删除，24h 后 cleaner worker 自动销毁
- **middleware 拦截**：攻击者即使获得泄漏的 bootstrap 密码，也只能执行改密操作（强制改密后旧密码失效），无法无限制访问系统
- **slog 不含明文密码**：bootstrap 路径只打印 `username` + `credential_file` 路径，密码仅写入 0600 文件，不出现在任何日志流
- **bcrypt cost = 12**（OWASP 2023 推荐），防止离线暴力破解
