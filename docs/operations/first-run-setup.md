# GoCell First-Run Admin Bootstrap

> 本文档专注**运维侧部署细节**：环境变量配置、Docker / Kubernetes / macOS 配置、两种模式启动流程。
> 选型决策与架构分析见 [`docs/guides/admin-bootstrap-paths.md`](../guides/admin-bootstrap-paths.md)。
> 安全边界决策见 [`docs/architecture/202605061600-adr-bootstrap-admin-boundary.md`](../architecture/202605061600-adr-bootstrap-admin-boundary.md)。

## 概览

GoCell 支持两种 first-run admin 模式，通过 `GOCELL_SETUP_MODE` 选择：

- **bootstrap 模式**（`GOCELL_SETUP_MODE=bootstrap`）：启动时由 accesscore lifecycle 自动检测 admin role 是否有 user。若无，使用 `GOCELL_BOOTSTRAP_ADMIN_USERNAME` / `GOCELL_BOOTSTRAP_ADMIN_PASSWORD` 创建初始 admin。lifecycle 完成后 setup/admin endpoint 返回 410 Gone（仍受 Basic Auth 保护，未通过 401 优先）。
- **interactive 模式**（`GOCELL_SETUP_MODE=interactive`）：启动时不自动创建 admin。运维通过 `POST /api/v1/access/setup/admin` 主动创建第一个 admin，endpoint 以 HTTP Basic Auth（env 凭据）保护，创建成功后永久返回 410。

`GOCELL_SETUP_MODE` **必填**，**无默认值**；空值在启动期 fail-fast。两种模式均**必须**设置凭据 env：

```
GOCELL_SETUP_MODE=bootstrap | interactive
GOCELL_BOOTSTRAP_ADMIN_USERNAME=<operator-username>
GOCELL_BOOTSTRAP_ADMIN_PASSWORD=<operator-password>
```

**持久 startup credential 模型**：上述两个 bootstrap 凭据 env 是 setup endpoint 的**常驻保护**，不是「一次性 seed」。admin 创建后 env **不可删除**；轮换走「滚动替换 K8s Secret + restart」（详见 §凭据轮换）。

**empty config fail-fast**：`GOCELL_SETUP_MODE` / `GOCELL_BOOTSTRAP_ADMIN_USERNAME` / `GOCELL_BOOTSTRAP_ADMIN_PASSWORD` 任一为空时，启动 fail-fast，防止无凭据的 endpoint 暴露 / 模式默认值歧义。

设计决策详见 `docs/architecture/202605061600-adr-bootstrap-admin-boundary.md` §D2 + §D9。

---

## 必要环境变量

| 变量 | 模式 | 说明 |
|------|------|------|
| `GOCELL_SETUP_MODE` | 两种 | **必填，无默认值**；`bootstrap` 或 `interactive`；空值或未知值 fail-fast |
| `GOCELL_BOOTSTRAP_ADMIN_USERNAME` | 两种 | **必填，持久**；bootstrap 模式下同时是 admin username |
| `GOCELL_BOOTSTRAP_ADMIN_PASSWORD` | 两种 | **必填，持久**；≥8 byte；TrimSpace 自动处理 K8s secret 末尾换行；含控制字符则 fail-fast |
| `GOCELL_REPLICA_COUNT` | interactive | 可选；`> 1` 时 interactive 模式 fail-fast，防止多 pod 竞态 |

---

## Bootstrap 模式

### 启动流程

```
[startup]
  GOCELL_SETUP_MODE=bootstrap        # 必填
  GOCELL_BOOTSTRAP_ADMIN_USERNAME=ops
  GOCELL_BOOTSTRAP_ADMIN_PASSWORD=MyStr0ngP@ss!

  accesscore lifecycle 启动：
    → 校验凭据 env（任一为空则 fail-fast）
    → 检测 admin role 是否有 user
    → 若无：以 env username/password 创建 admin
    → 若有：跳过创建，env 凭据继续作为 setup endpoint 的 Basic Auth 保护
    → setup/admin endpoint 返回 410 Gone（仍受 Basic Auth 保护：401 优先于 410）

[client]
  POST /api/v1/access/sessions/login
  { "username": "ops", "password": "MyStr0ngP@ss!" }
  → 201 Created + tokens
```

### Docker Compose 示例

```yaml
services:
  gocell:
    image: gocell:latest
    environment:
      - GOCELL_SETUP_MODE=bootstrap
      - GOCELL_BOOTSTRAP_ADMIN_USERNAME=ops
      - GOCELL_BOOTSTRAP_ADMIN_PASSWORD=MyStr0ngP@ss!
      - GOCELL_JWT_ISSUER=https://gocell.example
      - GOCELL_JWT_AUDIENCE=gocell
```

启动后直接登录：

```bash
docker compose up -d
curl -s -X POST http://localhost:8080/api/v1/access/sessions/login \
  -H 'Content-Type: application/json' \
  -d '{"username":"ops","password":"MyStr0ngP@ss!"}'
```

### Kubernetes 示例

凭据通过 K8s Secret 注入（不写入镜像）：

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: gocell-bootstrap
type: Opaque
stringData:
  username: ops
  password: MyStr0ngP@ss!
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: gocell
spec:
  replicas: 1
  template:
    spec:
      containers:
        - name: gocell
          image: gocell:latest
          env:
            - name: GOCELL_SETUP_MODE
              value: bootstrap
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

启动后查看日志确认 bootstrap 完成：

```bash
kubectl logs -l app=gocell | grep "initial_admin"
# level=INFO msg="initialadmin: initial admin created from env credentials" username=ops
# admin 已存在时：lifecycle 路径不打日志（持久 startup credential 模型，env 必须保留作为 setup endpoint 的 Basic Auth 常驻保护）
```

### macOS 开发

```bash
export GOCELL_SETUP_MODE=bootstrap
export GOCELL_BOOTSTRAP_ADMIN_USERNAME=dev
export GOCELL_BOOTSTRAP_ADMIN_PASSWORD=devpassword123
go run ./cmd/corebundle
```

> 注：`examples/ssobff` 不再启用 initialadmin lifecycle，运行时走 interactive 模式演示流（详见 `examples/ssobff/README.md`）。

---

## Interactive 模式

### 适用场景

- 运维需要自选 admin username / email，不用 env 中的 username 作为业务身份
- 内部工具、需要审计"谁创建了首位 admin"的场景

### 凭据关系

`GOCELL_BOOTSTRAP_ADMIN_USERNAME` / `GOCELL_BOOTSTRAP_ADMIN_PASSWORD` 是 **HTTP Basic Auth 操作员凭据**（验证谁有权限发起 setup 请求）。body 中的 `username` / `email` / `password` 是**要创建的 admin 业务凭据**，两者独立。

### 启动流程

```
[startup]
  GOCELL_SETUP_MODE=interactive
  GOCELL_BOOTSTRAP_ADMIN_USERNAME=ops
  GOCELL_BOOTSTRAP_ADMIN_PASSWORD=OpsPass123!

  accesscore 启动：setup/admin endpoint 以 HTTP Basic Auth 保护

[client — 运维操作]
  POST /api/v1/access/setup/admin
  Authorization: Basic <base64(ops:OpsPass123!)>
  Content-Type: application/json
  { "username": "admin", "email": "admin@corp.example", "password": "AdminPass456!" }
  → 201 Created

[再次调用 setup/admin]
  → 410 Gone（永久）

[client — 用业务凭据登录]
  POST /api/v1/access/sessions/login
  { "username": "admin", "password": "AdminPass456!" }
  → 201 Created + tokens
```

### curl 示例

```bash
# 1. 设置凭据变量
OPS_USER="ops"
OPS_PASS="OpsPass123!"
ADMIN_PASS="AdminPass456!"

# 2. 创建 admin（HTTP Basic Auth 验证操作员身份）
curl -s -X POST http://localhost:8080/api/v1/access/setup/admin \
  -u "${OPS_USER}:${OPS_PASS}" \
  -H 'Content-Type: application/json' \
  -d "{\"username\":\"admin\",\"email\":\"admin@corp.example\",\"password\":\"${ADMIN_PASS}\"}"
# 201 Created

# 3. 用业务凭据登录
curl -s -X POST http://localhost:8080/api/v1/access/sessions/login \
  -H 'Content-Type: application/json' \
  -d "{\"username\":\"admin\",\"password\":\"${ADMIN_PASS}\"}"
```

### multi-pod 限制

`GOCELL_REPLICA_COUNT > 1` 时，interactive 模式 fail-fast（启动即失败），防止多 pod 竞态注册。HA 部署请使用 bootstrap 模式。

### Docker Compose 示例

```yaml
services:
  gocell:
    image: gocell:latest
    environment:
      - GOCELL_SETUP_MODE=interactive
      - GOCELL_BOOTSTRAP_ADMIN_USERNAME=ops
      - GOCELL_BOOTSTRAP_ADMIN_PASSWORD=OpsPass123!
      - GOCELL_REPLICA_COUNT=1
```

---

## 故障排查

| 现象 | 原因 | 处理 |
|------|------|------|
| 启动失败：`ERR_AUTH_BOOTSTRAP_CREDENTIALS_MISSING` | `GOCELL_BOOTSTRAP_ADMIN_USERNAME` 或 `GOCELL_BOOTSTRAP_ADMIN_PASSWORD` 为空 | 检查两个 env 是否正确注入；K8s Secret 是否挂载 |
| 启动失败：`ERR_AUTH_BOOTSTRAP_PASSWORD_TOO_SHORT` | `GOCELL_BOOTSTRAP_ADMIN_PASSWORD` 少于 8 字节（TrimSpace 后） | 使用更长密码；K8s secret 末尾换行由 TrimSpace 自动处理 |
| 启动失败：`ERR_AUTH_BOOTSTRAP_PASSWORD_CONTROL_CHAR` | 密码含控制字符 | 检查 secret 编码；使用可打印 ASCII |
| 启动失败：interactive + `GOCELL_REPLICA_COUNT > 1` | multi-pod 场景不允许 interactive | 改用 bootstrap 模式；或降为单副本 |
| 启动失败：`GOCELL_SETUP_MODE is required` | `GOCELL_SETUP_MODE` 未设置或为空 | 显式设置为 `bootstrap` 或 `interactive`，无默认值 |
| 启动后无 `initial admin created` 日志 | bootstrap 模式，admin 已存在 | 正常现象；env 必须保留作为 setup endpoint 的 Basic Auth 常驻保护，**禁止**在 admin 创建后删除 |
| setup/admin 返回 401 | Basic Auth 凭据错误（两种模式都会先经 Basic Auth） | 核对 `GOCELL_BOOTSTRAP_ADMIN_USERNAME` / `GOCELL_BOOTSTRAP_ADMIN_PASSWORD` |
| setup/admin 返回 410 | Basic Auth 通过 + admin 已创建（bootstrap 或 interactive） | 进入 login 流；admin 已就绪 |
| setup/admin 返回 429 | per-IP rate limit 触发（默认 5 req/min, burst 10） | 等待 rate limit 窗口重置；检查是否有异常请求来源 |
| 未知 `GOCELL_SETUP_MODE` 值 | 拼写错误或空值 | 只接受 `bootstrap`、`interactive`，无默认值 |

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

- **env 凭据生命周期 — 持久 startup credential**：bootstrap 凭据是 setup endpoint 的常驻保护层，**不是一次性 seed**。admin 创建后 env 必须保留，不允许在运行期删除。轮换走「滚动替换 K8s Secret + restart」（详见 §凭据轮换）。设计原因：D1 闭合契约要求 setup endpoint 永久受 Basic Auth 保护，401 优先于 410，避免「endpoint 是否已退休」探测口。
- **`crypto/subtle.ConstantTimeCompare`**：HTTP Basic Auth 校验使用时间安全比较，防止时序侧信道泄漏操作员凭据。
- **per-IP token-bucket rate limit**：两种模式默认启用（5 req/min, burst 10），防止暴力枚举 Basic Auth 凭据；触发后返回 `429 ERR_RATE_LIMITED`。
- **oracle-safe 401 envelope**：认证失败统一返回 `ERR_AUTH_BOOTSTRAP_FAILED`，不区分"用户名错误"与"密码错误"，防止枚举攻击。
- **bcrypt cost = 12**（OWASP 2023 推荐），防止离线暴力破解业务 admin 密码。

---

## 凭据轮换

bootstrap 凭据轮换走「滚动替换 K8s Secret + restart」。无停机轮换需要分两步操作（先更新 Secret，再 rollout restart）：

```bash
# 1. 滚动替换 K8s Secret（kubectl apply 自动覆盖；client-side dry-run 避免本地保存敏感数据）
kubectl create secret generic gocell-bootstrap \
  --from-literal=username=ops --from-literal=password=NewOpsPass456! \
  --dry-run=client -o yaml | kubectl apply -f -

# 2. 滚动重启 deployment 使新 env 生效
kubectl rollout restart deployment/gocell

# 3. 验证 rollout 完成
kubectl rollout status deployment/gocell

# 4. 旧凭据立即失效；新凭据用于后续所有 setup/admin 请求（如 admin 已存在则 410 响应）
```

注意事项：

- 轮换前必须确认所有运维 runbook / CI 脚本已更新到新凭据；旧凭据在 rollout 完成后立即失效。
- multi-pod interactive 部署不支持轮换（启动期 fail-fast 阻止）；bootstrap 模式无此限制。
- env 改名（如 GOCELL_BOOTSTRAP_ADMIN_* → 其他名称）是不兼容变更，需联动 ADR 与 Helm chart 同步发布。
