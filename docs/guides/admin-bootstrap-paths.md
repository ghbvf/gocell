# First-Admin Bootstrap 选型与客户端集成

> 本文档面向**应用层 / 客户端开发者**，回答：
>
> - 部署 GoCell 时该选 `interactive` 还是 `bootstrap` 模式？
> - 客户端收到 setup 端点的 `410 Gone` 时该如何处理？
> - 日常如何区分 setup 端点的 `400` / `409` / `410`？
>
> 运维侧的部署细节（凭据文件路径、Docker / K8s 配置、密码重置流程）见 [`docs/operations/first-run-setup.md`](../operations/first-run-setup.md)。

---

## 1. 为什么有两条路径

`accesscore` Cell 把"创建第一个 admin"建模为一次性事实：admin role 在系统中唯一存在一份，先到者拥有它。围绕这次一次性事件，GoCell 提供两条彼此互斥的路径：

| 路径 | 触发者 | admin 身份来源 |
|---|---|---|
| **Interactive** | 运维通过 HTTP 端点 `POST /api/v1/access/setup/admin` 主动提供 | 运维自选用户名 / 邮箱 / 密码 |
| **Bootstrap** | `accesscore` lifecycle 在启动时自动检测并创建 | 框架生成随机密码（用户名默认 `admin`，可通过 `initialadmin.WithUsername` 在 lifecycle 装配时覆盖），写凭据文件，运维读文件登录后强制改密 |

`cmd/corebundle` 通过环境变量 `GOCELL_ACCESSCORE_ADMIN_PROVISION_MODE` 选择，仅接受空值 / `interactive` / `bootstrap`，其他值启动 fail-fast。

> 这是 **deployment-time** 决策，不是运行时开关。同一部署不能同时启用两条路径——`interactive` 模式下 lifecycle 不写凭据文件；`bootstrap` 模式下 setup 端点在启动后立即返回 410。

---

## 2. 选型矩阵

| 维度 | Interactive | Bootstrap |
|---|---|---|
| **典型部署形态** | VM / 单机 / 半人工 stage | 容器 / K8s / 无人值守自动化 |
| **首次启动是否需要人在场** | 是（运维 POST） | 否（启动即就绪） |
| **admin 凭据所在介质** | 运维自带（密钥管理 / 密码本） | 凭据文件（OS 默认路径，0600 权限） |
| **首登后是否强制改密** | 否（密码由运维自定） | 是（middleware 拦截直到改密完成） |
| **多副本兼容** | 一次性、需要外部锁（PG `adminprovision` 加 advisory lock） | 一次性、`adminprovision` 内置锁；副本自动收敛 |
| **公网暴露风险** | setup 端点必须放在 ingress 后或限速，否则有暴力枚举风险 | setup 端点开机即 410，公网零暴露 |
| **成功后 setup 端点状态** | 410 Gone（永久） | 410 Gone（永久；从启动那一刻开始就是 410） |
| **推荐场景** | 内部工具、开发环境、需要明确署名首位 admin 时 | 生产 K8s / Docker、CI sandbox、希望部署即就绪时 |

> ⚠️ **多副本部署的 interactive 模式当前不安全**：`adminprovision` 仅用 `sync.Mutex` 保护单进程内竞争，跨进程锁（backlog `ADMINPROVISION-DIST-LOCK-01` / 026 plan PR-A56）尚未落地。当 deployment replicas ≥ 2 时，并发的 `POST /setup/admin` 可能在两个 pod 各自 `Status()` 看到 `hasAdmin=false` 后双双进入 `Ensure()` 并各创建一份 admin。**replicas ≥ 2 部署请使用 bootstrap 模式**，待 PG advisory lock 落地后再启用 interactive。

> 已经决定哪条路径后，运维侧细节请跳转 [`docs/operations/first-run-setup.md`](../operations/first-run-setup.md)。

---

## 3. Interactive 路径完整流

```
[client]                              [accesscore]
  │  GET  /api/v1/access/setup/status
  ├────────────────────────────────────►   data.hasAdmin = false
  │
  │  POST /api/v1/access/setup/admin
  │  { "username":"root","email":"...","password":"..." }
  ├────────────────────────────────────►   201 Created
  │  (admin 唯一性 = adminprovision 锁；多副本同时 POST 仅一笔成功)
  │
  │  POST /api/v1/access/setup/admin (再次)
  ├────────────────────────────────────►   410 Gone
  │
  │  POST /api/v1/access/sessions/login
  ├────────────────────────────────────►   201 Created + access/refresh tokens
```

要点：
- `GET /status` 仅作 hint；并发 setup 时仍由 410 断言一次性
- 创建成功后 setup 端点对**该部署生命周期内的所有后续请求**都返回 410（不是临时冲突 409）
- 客户端不应把 410 当作 retry 信号；进入 login 流即可
- `GET /status` 在 provisioner 故障时返回 500：这是 infra 故障类，客户端按自身 HTTP retry middleware 处理（沿用应用全局退避策略；若响应携带 `Retry-After` header 则遵循其值），不需要为 setup 端点单独写一份 retry 表
- `/setup/status` 是业务端点，**不是** liveness / readiness probe — K8s probe 应指向 `/healthz` / `/readyz`，那两条路径独立于业务故障域

---

## 4. Bootstrap 路径完整流

```
[startup]                             [accesscore lifecycle]
  │  set GOCELL_ACCESSCORE_ADMIN_PROVISION_MODE=bootstrap
  │  start gocell
  ├────────────────────────────────────►   detects admin role empty
  │                                        generates random password (username
  │                                          defaults to "admin")
  │                                        writes credential file (0600)
  │                                        starts 24h TTL purge worker

[client]                              [accesscore]
  │  read credential file              (运维带外动作；文件含 username + password)
  │
  │  POST /api/v1/access/sessions/login
  │  { "username":"admin","password":"<from-cred-file>" }
  ├────────────────────────────────────►   201 Created + reset-required token
  │
  │  POST /api/v1/access/users/{userId}/password
  │  { "old":"...", "new":"..." }
  ├────────────────────────────────────►   200 OK
  │  (middleware 在改密完成前会拒绝任何业务端点)

[and any time]
  │  POST /api/v1/access/setup/admin
  ├────────────────────────────────────►   410 Gone
```

凭据文件路径与权限规范见 [`docs/operations/first-run-setup.md`](../operations/first-run-setup.md)。

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

> curl 块为人工调试用途；**生产客户端请参考 §6.2 Go 伪代码** — 通过 contract registry 解析 path，不要把 curl 字面量复制到代码中（与 §5 设计原则一致）。

```bash
# 1. 尝试 setup（部署后任何时刻都可能拿到 410）
$ curl -sS -X POST https://gocell.example/api/v1/access/setup/admin \
    -H 'content-type: application/json' \
    -d '{"username":"root","email":"root@local","password":"SecretPass!23"}'

{"error":{"code":"ERR_SETUP_ALREADY_INITIALIZED","message":"first-run admin already provisioned; this endpoint is retired","details":{"nextAction":"login"}}}

# 2. 客户端读 details.nextAction → 进入 login 流（路径来自客户端自己的 contract / OpenAPI）
$ curl -sS -X POST https://gocell.example/api/v1/access/sessions/login \
    -H 'content-type: application/json' \
    -d '{"username":"<known-admin>","password":"<...>"}'
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

要点：
- 客户端**不**在代码中硬编码 `/api/v1/access/sessions/login`；用 contract id 反查
- `nextAction` 用作语义路标——未来若框架增加新路径（例如 SSO redirect），再扩 `nextAction` 取值

---

## 7. `400` / `409` / `410` 区分

| 状态 | errcode | 触发条件 | 客户端建议处理 |
|---|---|---|---|
| **400** | `ERR_AUTH_IDENTITY_INVALID_INPUT` | 请求体字段缺失、超长、非可打印 ASCII 密码、控制字符 | 校验输入 → 提示用户 → 重发 |
| **400** | `ERR_VALIDATION_FAILED` | JSON malformed、未知字段、Content-Type 错误（含 `DecodeJSONStrict` 触发的所有校验失败） | 修请求体格式 → 重发 |
| **409** | `ERR_AUTH_USER_DUPLICATE` | 请求 username 已被其他 user（identity 路径或 bootstrap pending 行）占用，但**还没成为 admin** | 换 username → 重试；不要静默 retry 同名 |
| **410** | `ERR_SETUP_ALREADY_INITIALIZED` | admin role 已有 user（来自 interactive 或 bootstrap） | 进入 login 流；**不要重试 setup**；视为终态 |

判定顺序与语义：

```
        ┌────────────────────────┐
        │ POST /access/setup/admin│
        └──────────┬─────────────┘
                   │
       ┌───────────┴────────────┐
       │ admin 已存在?          │
       └────┬──────────────┬────┘
            │ 否            │ 是
            ▼              ▼
   ┌────────────────┐    410 Gone (终态)
   │ 输入合法?      │
   └─┬──────────┬───┘
     │ 否        │ 是
     ▼          ▼
   400 Bad    ┌─────────────────┐
   Request    │ username 已占? │
              └─┬───────────┬───┘
                │ 是         │ 否
                ▼           ▼
             409 Conflict  201 Created
```

## 8. 相关文档

- [`docs/operations/first-run-setup.md`](../operations/first-run-setup.md) — 凭据文件路径、Docker / K8s / macOS / Windows 部署细节、密码重置流程
- [`docs/architecture/202604181900-adr-auth-setup-first-run.md`](../architecture/202604181900-adr-auth-setup-first-run.md) — 双模式 ADR
- [`contracts/http/auth/setup/admin/v1/contract.yaml`](../../contracts/http/auth/setup/admin/v1/contract.yaml) — setup admin 端点契约（含 400 / 409 / 410 声明）
- [`contracts/http/auth/setup/status/v1/contract.yaml`](../../contracts/http/auth/setup/status/v1/contract.yaml) — setup status 端点契约
