# First-Admin Bootstrap 选型与客户端集成

> 本文档面向**应用层 / 客户端开发者**，回答：
>
> - 部署 GoCell 时该选 `bootstrap` 还是 `interactive` 模式？
> - 客户端收到 setup 端点的 `410 Gone` 时该如何处理？
> - 日常如何区分 setup 端点的 `400` / `401` / `409` / `410`？
>
> 运维侧的部署细节（env 变量、Docker / K8s 配置、密码重置流程）见 [`docs/operations/first-run-setup.md`](../operations/first-run-setup.md)。
> 安全边界 ADR 见 [`docs/architecture/202605061600-adr-bootstrap-admin-boundary.md`](../architecture/202605061600-adr-bootstrap-admin-boundary.md)。

---

## 1. 为什么有两条路径

`accesscore` Cell 把"创建第一个 admin"建模为一次性事实：admin role 在系统中唯一存在一份，先到者拥有它。围绕这次一次性事件，GoCell 提供两条彼此互斥的路径：

| 路径 | 触发者 | admin 身份来源 |
|---|---|---|
| **Bootstrap** | `accesscore` lifecycle 在启动时自动检测并创建 | env `GOCELL_BOOTSTRAP_ADMIN_USERNAME` / `GOCELL_BOOTSTRAP_ADMIN_PASSWORD`（运维注入） |
| **Interactive** | 运维通过 HTTP 端点 `POST /api/v1/access/setup/admin` 手工触发 | body 中的 `username` / `email` / `password`（运维自选业务身份） |

两种模式都通过 `GOCELL_SETUP_MODE` 选择（**必填，无默认值**；空值或未知值 fail-fast）。

`POST /api/v1/access/setup/admin` 的密码必须是 8-72 个可打印 ASCII 字节，与 bcrypt 72-byte 输入上限一致。

> 这是 **deployment-time** 决策，不是运行时开关。同一部署不能同时启用两条路径。
> bootstrap 凭据 env 是**持久 startup credential**——admin 创建后 env 不可删除，是 setup endpoint 的常驻 Basic Auth 保护。详见 ADR §D9 + `docs/operations/first-run-setup.md` §凭据轮换。

---

## 2. 选型矩阵

| 维度 | Bootstrap | Interactive |
|---|---|---|
| **典型部署形态** | 容器 / K8s / 无人值守自动化 | VM / 单机 / 半人工 stage |
| **首次启动是否需要人在场** | 否（启动即就绪） | 是（运维发送 HTTP 请求） |
| **admin 凭据所在介质** | env（K8s Secret / Vault / CI secret） | 运维自选（密钥管理 / 密码本） |
| **setup/admin endpoint 认证** | HTTP Basic Auth（env 凭据，持久）；admin 创建后 endpoint 返回 410，但 401 优先 | HTTP Basic Auth（env 操作员凭据，持久） |
| **env 凭据角色** | 同时是 admin 的 username/password + 持久 Basic Auth 保护 | 操作员身份（验证谁可以发起 setup）+ 持久 Basic Auth 保护 |
| **setup 后 endpoint 状态** | 410 Gone（Basic Auth 通过后；未通过 401） | 410 Gone（Basic Auth 通过后；未通过 401） |
| **multi-pod 兼容** | 是（lifecycle 幂等：admin 已存在则跳过创建，env 仍持续保护） | 否（`GOCELL_REPLICA_COUNT > 1` 时 fail-fast） |
| **推荐场景** | 生产 K8s / Docker、CI sandbox、希望部署即就绪 | 内部工具、需要显式署名首位 admin、开发环境 |

---

## 3. Bootstrap 路径完整流

```
[startup]
  GOCELL_SETUP_MODE=bootstrap        # 必填，无默认值
  GOCELL_BOOTSTRAP_ADMIN_USERNAME=ops
  GOCELL_BOOTSTRAP_ADMIN_PASSWORD=OpsPass123!
  start gocell

[accesscore lifecycle]
  → 校验 env 凭据（空则 fail-fast）
  → 检测 admin role 是否有 user
    → 无：以 env username/password 创建 admin（写入 accesscore DB）
    → 有：跳过创建（env 仍作为 setup endpoint 的 Basic Auth 常驻保护，禁止删除）
  → setup/admin endpoint：Basic Auth 通过后返回 410 Gone；未通过 401（401 优先于 410）

[client]
  POST /api/v1/access/sessions/login
  { "username": "ops", "password": "OpsPass123!" }
  → 201 Created + access/refresh tokens
```

---

## 4. Interactive 路径完整流

**凭据关系**：env `GOCELL_BOOTSTRAP_ADMIN_USERNAME` / `GOCELL_BOOTSTRAP_ADMIN_PASSWORD` 是 **操作员身份**（HTTP Basic Auth），body 是**业务 admin 身份**（要创建的用户）。两者解耦，对标 Keycloak temp-admin 退化版。

```
[startup]
  GOCELL_SETUP_MODE=interactive
  GOCELL_BOOTSTRAP_ADMIN_USERNAME=ops
  GOCELL_BOOTSTRAP_ADMIN_PASSWORD=OpsPass123!
  start gocell

[accesscore]
  → setup/admin endpoint 以 HTTP Basic Auth 保护（env 操作员凭据）

[client — 运维操作]
  POST /api/v1/access/setup/admin
  Authorization: Basic <base64(ops:OpsPass123!)>
  { "username":"admin","email":"admin@corp.example","password":"AdminPass456!" }
  → 201 Created

[client — 再次调用 setup/admin]
  → 410 Gone（永久）

[client — 用业务凭据登录]
  POST /api/v1/access/sessions/login
  { "username": "admin", "password": "AdminPass456!" }
  → 201 Created + access/refresh tokens
```

---

## 5. `410 Gone` 响应 body 示例

setup 端点退休后，所有 `POST /api/v1/access/setup/admin` 请求得到统一形态的错误信封：

```json
{
  "error": {
    "code": "ERR_SETUP_ALREADY_INITIALIZED",
    "message": "first-run admin already provisioned; this endpoint is retired",
    "details": {
      "nextAction": "login"
    }
  }
}
```

设计决定：

- `details` 上**只暴露语义动词** `nextAction`，不嵌入任何 HTTP path 字面量
- login 端点的实际路径由 contract 定义（`http.auth.login.v1`），客户端通过 OpenAPI / contract registry / 自身路由表解析
- 该响应跨部署稳定——即便 sessions/login 路径未来改版，410 body 字段不变

---

## 6. 客户端迁移示例

### 6.1 curl 示意

```bash
# Bootstrap 模式（admin 已创建）：必须传 Basic Auth 才能拿到 410；未通过 401
$ curl -sS -X POST https://gocell.example/api/v1/access/setup/admin \
    -u "ops:OpsPass123!" \
    -H 'content-type: application/json' \
    -d '{"username":"root","email":"root@local","password":"SecretPass!23"}'
{"error":{"code":"ERR_SETUP_ALREADY_INITIALIZED","message":"first-run admin already provisioned; this endpoint is retired","details":{"nextAction":"login"}}}

# Interactive 模式：需要 Basic Auth
$ curl -sS -X POST https://gocell.example/api/v1/access/setup/admin \
    -u "ops:OpsPass123!" \
    -H 'content-type: application/json' \
    -d '{"username":"admin","email":"admin@corp.example","password":"AdminPass456!"}'
```

### 6.2 Go 客户端伪代码

```go
type errEnvelope struct {
    Error struct {
        Code    string         `json:"code"`
        Message string         `json:"message"`
        Details map[string]any `json:"details"`
    } `json:"error"`
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
    if env.Error.Details["nextAction"] != "login" {
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
| **401** | `ERR_AUTH_BOOTSTRAP_FAILED` | Basic Auth 凭据错误（两种模式都先经 Basic Auth） | 核对 env 操作员凭据；不要自动重试（防枚举） |
| **409** | `ERR_AUTH_USER_DUPLICATE` | 请求 username 已被其他 user 占用，但还没成为 admin | 换 username → 重试；不要静默 retry 同名 |
| **410** | `ERR_SETUP_ALREADY_INITIALIZED` | Basic Auth 通过 + admin role 已有 user（来自 bootstrap 或 interactive） | 进入 login 流；**不要重试 setup**；视为终态 |
| **429** | `ERR_RATE_LIMITED` | per-IP rate limit 触发（默认 5 req/min, burst 10） | 等待 rate limit 窗口；检查请求来源 |

判定顺序（两种模式统一）：

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
- [`docs/architecture/202605061600-adr-bootstrap-admin-boundary.md`](../architecture/202605061600-adr-bootstrap-admin-boundary.md) — 安全边界 ADR（8 项决策）
- [`contracts/http/auth/setup/admin/v1/contract.yaml`](../../contracts/http/auth/setup/admin/v1/contract.yaml) — setup admin 端点契约（含 400 / 401 / 409 / 410 声明）
- [`contracts/http/auth/setup/status/v1/contract.yaml`](../../contracts/http/auth/setup/status/v1/contract.yaml) — setup status 端点契约
