# First-Admin Setup — Single Setup-Driven Path

> 本文档面向**应用层 / 客户端开发者**，回答：
>
> - GoCell first-run admin 如何注册？
> - 客户端收到 setup 端点的 `410 Gone` 时该如何处理？
> - 日常如何区分 setup 端点的 `400` / `401` / `409` / `410`？
>
> 运维侧的部署细节（env 变量、Docker / K8s 配置、密码重置流程）见 [`docs/operations/first-run-setup.md`](../operations/first-run-setup.md)。
> 安全边界 ADR 见 [`docs/architecture/202605061600-adr-bootstrap-admin-boundary.md`](../architecture/202605061600-adr-bootstrap-admin-boundary.md)。

---

## 1. Single setup-driven path

`accesscore` Cell 把"创建第一个 admin"建模为一次性事实：admin role 在系统中唯一存在一份，先到者拥有它。GoCell 提供单一路径：

运维启动服务后，通过 `POST /api/v1/access/setup/admin` 引导式创建首个 admin。endpoint 以 HTTP Basic Auth（env 操作员凭据）保护，body 中的 `username` / `email` / `password` 是业务 admin 身份，两者完全独立。

```
env  → GOCELL_BOOTSTRAP_ADMIN_USERNAME / GOCELL_BOOTSTRAP_ADMIN_PASSWORD
         = operator authenticator（谁有权限发起 setup 请求）

body → username / email / password
         = admin user identity（要创建的账号）
```

`POST /api/v1/access/setup/admin` 的密码必须是 8-72 个可打印 ASCII 字节，与 bcrypt 72-byte 输入上限一致。

admin 创建后 setup endpoint 永久返回 410 Gone（Basic Auth 通过后）。env 凭据是持久 operator authenticator，admin 创建后不可删除。

---

## 2. 完整流

```
[startup]
  GOCELL_BOOTSTRAP_ADMIN_USERNAME=ops
  GOCELL_BOOTSTRAP_ADMIN_PASSWORD=OpsPass123!
  start gocell

[accesscore]
  → 校验 env 凭据（空则 fail-fast）
  → setup/admin endpoint 以 HTTP Basic Auth 保护

[运维操作]
  POST /api/v1/access/setup/admin
  Authorization: Basic <base64(ops:OpsPass123!)>
  { "username":"admin","email":"admin@corp.example","password":"AdminPass456!" }
  → 201 Created

[再次调用 setup/admin]
  → 410 Gone（永久；Basic Auth 通过后；未通过仍 401）

[用业务凭据登录]
  POST /api/v1/access/sessions/login
  { "username": "admin", "password": "AdminPass456!" }
  → 201 Created + access/refresh tokens
```

---

## 3. curl 示例

```bash
OPS_USER="ops"
OPS_PASS="OpsPass123!"

# 设置 admin（HTTP Basic Auth 验证操作员身份）
curl -sS -X POST https://gocell.example/api/v1/access/setup/admin \
  -u "${OPS_USER}:${OPS_PASS}" \
  -H 'content-type: application/json' \
  -d '{"username":"admin","email":"admin@corp.example","password":"AdminPass456!"}'
# 201 Created

# admin 已存在时
curl -sS -X POST https://gocell.example/api/v1/access/setup/admin \
  -u "${OPS_USER}:${OPS_PASS}" \
  -H 'content-type: application/json' \
  -d '{"username":"root","email":"root@local","password":"SecretPass!23"}'
# {"error":{"code":"ERR_SETUP_ALREADY_INITIALIZED","message":"first-run admin already provisioned; this endpoint is retired","details":[{"key":"nextAction","value":"login"}]}}
```

## 4. K8s Secret 滚动轮换

operator 凭据轮换走滚动替换 K8s Secret + restart：

```bash
kubectl create secret generic gocell-bootstrap \
  --from-literal=username=ops --from-literal=password=NewOpsPass456! \
  --dry-run=client -o yaml | kubectl apply -f -
kubectl rollout restart deployment/gocell
kubectl rollout status deployment/gocell
```

---

## 5. `410 Gone` 响应 body 示例

setup 端点退休后，所有 `POST /api/v1/access/setup/admin` 请求得到统一形态的错误信封：

```json
{
  "error": {
    "code": "ERR_SETUP_ALREADY_INITIALIZED",
    "message": "first-run admin already provisioned; this endpoint is retired",
    "details": [
      {"key": "nextAction", "value": "login"}
    ]
  }
}
```

- `details` 是 `array<{key,value}>`（共享 envelope `contracts/shared/errors/error-response-v1.schema.json`），客户端按 key 匹配条目而非 map 索引
- `details` 上只暴露语义动词 `nextAction`，不嵌入任何 HTTP path 字面量
- login 端点的实际路径由 contract 定义（`http.auth.login.v1`），客户端通过 OpenAPI / contract registry / 自身路由表解析
- 该响应跨部署稳定——即便 sessions/login 路径未来改版，410 body 字段不变

---

## 6. Go 客户端伪代码

```go
type errDetail struct {
    Key   string `json:"key"`
    Value any    `json:"value"`
}

type errEnvelope struct {
    Error struct {
        Code    string      `json:"code"`
        Message string      `json:"message"`
        Details []errDetail `json:"details"`
    } `json:"error"`
}

func (e errEnvelope) detail(key string) (any, bool) {
    for _, d := range e.Error.Details {
        if d.Key == key {
            return d.Value, true
        }
    }
    return nil, false
}

func provisionOrLogin(ctx context.Context, c *Client, in AdminSeed) error {
    resp, err := c.Post(ctx, "http.auth.setup.admin.v1", in)
    if err != nil {
        return fmt.Errorf("setup: post admin: %w", err)
    }
    defer resp.Body.Close()

    if resp.StatusCode == http.StatusCreated {
        return nil // 我们就是首位 admin
    }
    if resp.StatusCode != http.StatusGone {
        return fmt.Errorf("setup: unexpected status %d", resp.StatusCode)
    }

    var env errEnvelope
    if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
        return fmt.Errorf("setup: decode 410 envelope: %w", err)
    }
    if v, ok := env.detail("nextAction"); !ok || v != "login" {
        return fmt.Errorf("setup: 410 with unexpected nextAction %v", env.Error.Details)
    }
    // login 路径由 contract registry 提供，不读 410 body 上的字面量
    return c.LoginByContractID(ctx, "http.auth.login.v1", in.LoginCreds())
}
```

---

## 7. `400` / `401` / `409` / `410` 区分

| 状态 | errcode | 触发条件 | 客户端建议处理 |
|---|---|---|---|
| **400** | `ERR_AUTH_IDENTITY_INVALID_INPUT` | 请求体字段缺失、超长、非可打印 ASCII 密码、控制字符 | 校验输入 → 提示用户 → 重发 |
| **400** | `ERR_VALIDATION_FAILED` | JSON malformed、未知字段、Content-Type 错误 | 修请求体格式 → 重发 |
| **401** | `ERR_AUTH_BOOTSTRAP_FAILED` | Basic Auth 凭据错误 | 核对 env 操作员凭据；不要自动重试（防枚举） |
| **409** | `ERR_AUTH_USER_DUPLICATE` | 请求 username 已被其他 user 占用，但还没成为 admin | 换 username → 重试；不要静默 retry 同名 |
| **410** | `ERR_SETUP_ALREADY_INITIALIZED` | Basic Auth 通过 + admin role 已有 user | 进入 login 流；**不要重试 setup**；视为终态 |
| **429** | `ERR_RATE_LIMITED` | per-IP rate limit 触发（默认 5 req/min, burst 10） | 等待 rate limit 窗口；检查请求来源 |

判定顺序：

```
        ┌────────────────────────────┐
        │ POST /access/setup/admin   │
        └──────────┬─────────────────┘
                   │
       ┌───────────┴────────────┐
       │ rate limit 通过?       │
       └────┬──────────────┬────┘
            │ 否            │ 是
            ▼              ▼
         429 Too Many     ┌──────────────────┐
         Requests         │ Basic Auth 通过? │
                          └────┬──────────┬──┘
                               │ 否        │ 是
                               ▼          ▼
                          401 Unauthorized ┌────────────────────┐
                                           │ admin 已存在?       │
                                           └────┬──────────┬────┘
                                                │ 否        │ 是
                                                ▼          ▼
                                       ┌────────────────┐  410 Gone（终态）
                                       │ 输入合法?      │
                                       └─┬──────────┬───┘
                                         │ 否        │ 是
                                         ▼          ▼
                                       400 Bad    ┌─────────────────┐
                                       Request    │ username 已占?   │
                                                  └─┬───────────┬───┘
                                                    │ 是         │ 否
                                                    ▼           ▼
                                                 409 Conflict  201 Created
```

---

## 8. 相关文档

- [`docs/operations/first-run-setup.md`](../operations/first-run-setup.md) — 环境变量配置、Docker / K8s 部署细节、密码重置流程
- [`docs/architecture/202605061600-adr-bootstrap-admin-boundary.md`](../architecture/202605061600-adr-bootstrap-admin-boundary.md) — 安全边界 ADR（D1–D5）
- [`contracts/http/auth/setup/admin/v1/contract.yaml`](../../contracts/http/auth/setup/admin/v1/contract.yaml) — setup admin 端点契约（含 400 / 401 / 409 / 410 声明）
- [`contracts/http/auth/setup/status/v1/contract.yaml`](../../contracts/http/auth/setup/status/v1/contract.yaml) — setup status 端点契约
