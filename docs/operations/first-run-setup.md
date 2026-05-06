# GoCell First-Run Admin Setup

> 运维通过 POST /api/v1/access/setup/admin 引导式注册首个 admin。无 mode 选择，单一路径。

本文档专注**运维侧部署细节**：环境变量配置、Docker / Kubernetes 配置、启动流程与故障排查。
安全边界决策见 [`docs/architecture/202605061600-adr-bootstrap-admin-boundary.md`](../architecture/202605061600-adr-bootstrap-admin-boundary.md)。

## 概览

GoCell first-run admin 注册走单一路径：运维启动服务后，通过 `POST /api/v1/access/setup/admin` 发送请求创建首个 admin。endpoint 以 HTTP Basic Auth（env 凭据）保护，admin 创建成功后永久返回 410 Gone。

必填凭据 env：

```
GOCELL_BOOTSTRAP_ADMIN_USERNAME=<operator-username>
GOCELL_BOOTSTRAP_ADMIN_PASSWORD=<operator-password>
```

**持久 operator authenticator**：上述 env 是 setup endpoint 的常驻保护，不是一次性 seed。admin 创建后 env **不可删除**；轮换走「滚动替换 K8s Secret + restart」（详见 §凭据轮换）。

**empty config fail-fast**：`GOCELL_BOOTSTRAP_ADMIN_USERNAME` / `GOCELL_BOOTSTRAP_ADMIN_PASSWORD` 任一为空时，启动 fail-fast。

---

## 必要环境变量

| 变量 | 说明 |
|------|------|
| `GOCELL_BOOTSTRAP_ADMIN_USERNAME` | **必填，持久**；HTTP Basic Auth 操作员身份，保护 setup/admin endpoint |
| `GOCELL_BOOTSTRAP_ADMIN_PASSWORD` | **必填，持久**；≥8 byte；TrimSpace 自动处理 K8s secret 末尾换行；含控制字符则 fail-fast |

---

## 启动流程

```
[startup]
  GOCELL_BOOTSTRAP_ADMIN_USERNAME=ops
  GOCELL_BOOTSTRAP_ADMIN_PASSWORD=OpsPass123!

  accesscore 启动：校验 env 凭据（空则 fail-fast）
  → setup/admin endpoint 以 HTTP Basic Auth 保护

[运维操作]
  POST /api/v1/access/setup/admin
  Authorization: Basic <base64(ops:OpsPass123!)>
  Content-Type: application/json
  { "username": "admin", "email": "admin@corp.example", "password": "AdminPass456!" }
  → 201 Created

[再次调用 setup/admin]
  → 410 Gone（永久）

[用业务凭据登录]
  POST /api/v1/access/sessions/login
  { "username": "admin", "password": "AdminPass456!" }
  → 201 Created + tokens
```

**凭据关系**：`GOCELL_BOOTSTRAP_ADMIN_USERNAME` / `GOCELL_BOOTSTRAP_ADMIN_PASSWORD` 是 **HTTP Basic Auth 操作员凭据**（验证谁有权限发起 setup 请求）。body 中的 `username` / `email` / `password` 是**要创建的 admin 业务凭据**，两者完全独立。

---

## curl 示例

```bash
OPS_USER="ops"
OPS_PASS="OpsPass123!"
ADMIN_PASS="AdminPass456!"

# 1. 创建 admin（HTTP Basic Auth 验证操作员身份）
curl -s -X POST http://localhost:8080/api/v1/access/setup/admin \
  -u "${OPS_USER}:${OPS_PASS}" \
  -H 'Content-Type: application/json' \
  -d "{\"username\":\"admin\",\"email\":\"admin@corp.example\",\"password\":\"${ADMIN_PASS}\"}"
# 201 Created

# 2. 用业务凭据登录
curl -s -X POST http://localhost:8080/api/v1/access/sessions/login \
  -H 'Content-Type: application/json' \
  -d "{\"username\":\"admin\",\"password\":\"${ADMIN_PASS}\"}"
```

---

## Docker Compose 示例

```yaml
services:
  gocell:
    image: gocell:latest
    environment:
      - GOCELL_BOOTSTRAP_ADMIN_USERNAME=ops
      - GOCELL_BOOTSTRAP_ADMIN_PASSWORD=OpsPass123!
      - GOCELL_JWT_ISSUER=https://gocell.example
      - GOCELL_JWT_AUDIENCE=gocell
```

启动后发起 setup：

```bash
docker compose up -d
curl -s -X POST http://localhost:8080/api/v1/access/setup/admin \
  -u "ops:OpsPass123!" \
  -H 'Content-Type: application/json' \
  -d '{"username":"admin","email":"admin@corp.example","password":"AdminPass456!"}'
```

---

## Kubernetes 示例

凭据通过 K8s Secret 注入（不写入镜像）：

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: gocell-bootstrap
type: Opaque
stringData:
  username: ops
  password: OpsPass123!
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: gocell
spec:
  template:
    spec:
      containers:
        - name: gocell
          image: gocell:latest
          env:
            - name: GOCELL_BOOTSTRAP_ADMIN_USERNAME
              valueFrom:
                secretKeyRef:
                  name: gocell-bootstrap
                  key: username
            - name: GOCELL_BOOTSTRAP_ADMIN_PASSWORD
              valueFrom:
                secretKeyRef:
                  name: gocell-bootstrap
                  key: password
```

部署语义：当前 `UserRepository` 仅有 in-memory 实现，进程内 `sync.Mutex` 保证 admin 唯一；first-run admin 必须落到单 pod replica。多 pod 幂等承诺要等 `ACCESSCORE-PG-USERS-MIGRATION-01`（PG `users` 表 + `UNIQUE(role='admin')` 部分索引）落地后才成立——届时第一个 INSERT 胜出，后续 POST 返回 409 或 410。

---

## 故障排查

启动失败统一返回 `ERR_CELL_INVALID_CONFIG`（K#08 后 bootstrap 校验归类为 cell config 失败），按 `message` 区分根因：

| 现象 | message | 原因 | 处理 |
|------|---------|------|------|
| 启动失败 | `... are required to protect setup/admin endpoint` | 两个 env 同时为空 | 注入 `GOCELL_BOOTSTRAP_ADMIN_USERNAME` 与 `GOCELL_BOOTSTRAP_ADMIN_PASSWORD`；检查 K8s Secret 是否挂载 |
| 启动失败 | `... must both be set or both be empty` | 两个 env 中只设置了一个 | 同时设置或同时清空两个 env |
| 启动失败 | `... USERNAME must not contain control characters` | username 含控制字符 | 检查 secret 编码；使用可打印 ASCII |
| 启动失败 | `... PASSWORD must be at least 8 bytes` | password TrimSpace 后少于 8 字节 | 使用更长密码；K8s secret 末尾换行由 TrimSpace 自动处理 |
| setup/admin 返回 401 | Basic Auth 凭据错误 | 核对 `GOCELL_BOOTSTRAP_ADMIN_USERNAME` / `GOCELL_BOOTSTRAP_ADMIN_PASSWORD` |
| setup/admin 返回 409 | 请求 username 已被其他 user 占用 | 换 username → 重试 |
| setup/admin 返回 410 | Basic Auth 通过 + admin 已创建 | 进入 login 流；admin 已就绪 |
| setup/admin 返回 429 | per-IP rate limit 触发（默认 5 req/min, burst 10） | 等待 rate limit 窗口重置；检查是否有异常请求来源 |

---

## 管理员密码重置流程

### 场景：已知当前密码（正常改密）

已登录的 admin 用户通过 `POST /api/v1/access/users/{id}/password` 改密：

```bash
ACCESS_TOKEN="<your-current-token>"
USER_ID="<your-user-id>"

curl -s -X POST "http://localhost:8080/api/v1/access/users/${USER_ID}/password" \
  -H "Authorization: Bearer ${ACCESS_TOKEN}" \
  -H "Content-Type: application/json" \
  -d '{"oldPassword":"<current>","newPassword":"NewStr0ng@Pass!"}'
# 返回新 TokenPair
```

### 场景：忘记 admin 密码（需 DB 直接操作）

> 安全提示：以下操作需要直接访问数据库，必须由有权限的运维人员执行。

```sql
-- 1. 生成新的 bcrypt hash（cost=12，OWASP 2023 推荐）
--    Go: bcrypt.GenerateFromPassword([]byte("NewPass!"), 12)
--    htpasswd: htpasswd -bnBC 12 "" "NewPass!" | tr -d ':\n'

-- 2. 更新 admin 用户密码
UPDATE users
SET password_hash = '$2a$12$<your-bcrypt-hash-here>',
    password_reset_required = true,
    updated_at = NOW()
WHERE username = 'admin';

-- 3. 验证更新成功
SELECT id, username, password_reset_required, updated_at FROM users WHERE username = 'admin';
```

重置后：
1. 用新密码 `POST /api/v1/access/sessions/login`
2. 若 `password_reset_required=true`，按"已知当前密码"流程改密

---

## 安全说明

- **env 凭据生命周期 — 持久 operator authenticator**：bootstrap 凭据是 setup endpoint 的常驻保护层，admin 创建后 env 必须保留，不允许在运行期删除。轮换走「滚动替换 K8s Secret + restart」（详见 §凭据轮换）。
- **`crypto/subtle.ConstantTimeCompare`**：HTTP Basic Auth 校验使用时间安全比较，防止时序侧信道泄漏操作员凭据。
- **per-IP token-bucket rate limit**：默认启用（5 req/min, burst 10），防止暴力枚举 Basic Auth 凭据；触发后返回 `429 ERR_RATE_LIMITED`。
- **oracle-safe 401 envelope**：认证失败统一返回 `ERR_AUTH_BOOTSTRAP_FAILED`，不区分"用户名错误"与"密码错误"，防止枚举攻击。
- **bcrypt cost = 12**（OWASP 2023 推荐），防止离线暴力破解业务 admin 密码。

---

## 凭据轮换

bootstrap 凭据轮换走「滚动替换 K8s Secret + restart」：

```bash
# 1. 滚动替换 K8s Secret
kubectl create secret generic gocell-bootstrap \
  --from-literal=username=ops --from-literal=password=NewOpsPass456! \
  --dry-run=client -o yaml | kubectl apply -f -

# 2. 滚动重启 deployment 使新 env 生效
kubectl rollout restart deployment/gocell

# 3. 验证 rollout 完成
kubectl rollout status deployment/gocell

# 4. 旧凭据立即失效；新凭据用于后续所有 setup/admin 请求（admin 已存在则 410 响应）
```

注意事项：

- 轮换前必须确认所有运维 runbook / CI 脚本已更新到新凭据；旧凭据在 rollout 完成后立即失效。
- env 改名（如 GOCELL_BOOTSTRAP_ADMIN_* → 其他名称）是不兼容变更，需联动 ADR 与 Helm chart 同步发布。
