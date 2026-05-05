# WebSocket 集成指南

> 适用版本：GoCell v1.0（PR-V1-SEC-WS-AUTH-ACL）

---

## 1. 架构概览

```
HTTP 请求
    │
    ▼
adapters/websocket.UpgradeHandler   — transport 层
    ├─ Authenticate (before Accept)
    ├─ websocket.Accept (coder/websocket)
    └─ hub.Register(conn)
            │
            ▼
    runtime/websocket.Hub            — 应用层
        ├─ connMu + conns map
        ├─ subjectIdx (O(1) subject → conns)
        ├─ pingLoop (goroutine)
        └─ per-conn readLoop + writeLoop (goroutines)
```

**职责边界**：

| 层 | 包 | 职责 |
|---|---|---|
| Transport | `adapters/websocket` | HTTP 升级、Origin 校验、认证、conn 封装 |
| 应用层 | `runtime/websocket` | 连接生命周期、心跳驱逐、广播路由 |

`adapters/websocket` 依赖 `coder/websocket` 处理帧协议；`runtime/websocket.Hub` 不了解 transport 细节，只与 `Conn` 接口交互。

---

## 2. Origin 配置

`UpgradeConfig.AllowedOrigins` 是安全关键字段：

```go
cfg := adapterws.UpgradeConfig{
    AllowedOrigins: []string{
        "https://app.example.com",
        "https://*.example.com",   // wildcard 仅限 host 一段
    },
    Authenticator: auth.NewContextAuthenticator(),
}
handler, err := adapterws.UpgradeHandler(hub, cfg)
```

规则：

- **必填非空**：空 slice → `errcode.ErrWebsocketOriginsMissing`，构造时失败。
- **scheme 必填**：`"example.com"` 无 scheme 被拒（`errcode.ErrWebsocketOriginsInvalid`）；浏览器 Origin header 始终含 scheme，裸 host 永远不会匹配。
- **禁止全通配符 `"*"`**：明确拒绝，拒绝路由到 `errcode.ErrWebsocketOriginsInvalid`。
- **Wildcard 只作 host 一段**：`"https://*.example.com"` 合法；`"https://**"` 语义不明，避免使用。

> **生产环境警告**：`"http://*"` 与 `"https://*"` 全 host 通配仅用于本地开发或内网调试；生产环境必须使用具体 host pattern，例如 `"https://app.example.com"` 或 `"https://*.app.example.com"`。全 host 通配会绕过 Origin 安全边界，导致任意跨域访问。

---

## 3. 认证集成

`UpgradeConfig.Authenticator` 必填（nil → `errcode.ErrWebsocketAuthenticatorMissing`）。认证在 `websocket.Accept` 之前执行；认证失败直接写 `401 Unauthorized` 明文响应（浏览器 WebSocket API 无法读响应 body，JSON envelope 无意义）。

### 3.1 三种内置方式

#### Bearer token via Authorization header

```go
// 适合：服务端直连（curl、native app、后端 worker）
// 限制：浏览器 JS WebSocket API 无法设置 Authorization header
// verifier 类型为 auth.IntentTokenVerifier，实现 VerifyIntent(ctx, token, expected TokenIntent) (Claims, error)
authenticator := auth.NewJWTAuthenticator(verifier)
```

#### listener middleware 已鉴权后透传（推荐 `/api/v1/*`）

```go
// 适合：WebSocket 路由挂载在已有 JWT listener 上
// Principal 由 listener JWT middleware 写入 ctx，Authenticator 读出
authenticator := auth.NewContextAuthenticator()
```

WebSocket handler 注册在 PrimaryListener 时优先选此方式：listener 已做 JWT 校验，避免重复验签。

#### 显式无认证（broadcast-only 频道）

```go
// 适合：只推送公开数据（公告频道、行情推送）的 hub
// 必须显式声明，不可用 nil 代替
authenticator := auth.NewAnonymousAuthenticator()
```

### 3.2 自定义 AuthenticatorFunc

浏览器 JS `WebSocket` API 不支持设置 `Authorization` header，常见替代方式：

#### a. Query-param token

```go
authenticator := auth.AuthenticatorFunc(func(r *http.Request) (*auth.Principal, bool, error) {
    token := r.URL.Query().Get("token")
    if token == "" {
        return nil, false, nil
    }
    claims, err := verifier.VerifyIntent(r.Context(), token, auth.TokenIntentAccess)
    if err != nil {
        return nil, false, err
    }
    return claims.Principal(), true, nil
})
```

**安全权衡**：token 出现在 URL，会落入服务器访问日志、浏览器历史、代理日志。仅在无法使用 Cookie 的场景使用，并设置极短 TTL（≤ 60s 一次性 token）。

#### b. Cookie（推荐浏览器场景）

```go
authenticator := auth.AuthenticatorFunc(func(r *http.Request) (*auth.Principal, bool, error) {
    cookie, err := r.Cookie("session_token")
    if err != nil {
        return nil, false, nil
    }
    claims, err := verifier.VerifyIntent(r.Context(), cookie.Value, auth.TokenIntentAccess)
    if err != nil {
        return nil, false, err
    }
    return claims.Principal(), true, nil
})
```

**安全权衡**：Cookie 不出现在 URL；需设置 `SameSite=Strict`（或 `Lax`）+ `HttpOnly` + `Secure` 防止 CSRF 和 XSS。

#### c. Sec-WebSocket-Protocol 子协议携带 token

```go
authenticator := auth.AuthenticatorFunc(func(r *http.Request) (*auth.Principal, bool, error) {
    // 浏览器可通过 new WebSocket(url, ["v1", "<token>"]) 传递子协议
    protos := r.Header.Get("Sec-WebSocket-Protocol")
    // 解析出 token 部分...
    ...
})
```

**安全权衡**：token 明文出现在握手 header，不在 URL 日志中，但需服务端在 Accept 时回传选中的子协议，实现略复杂。

### 3.3 MessageHandler 读取 Principal（P1-3）

Hub 在 Register 时把 Principal 注入 per-connection context（`auth.WithPrincipal(connCtx, p)`）。MessageHandler 收到的 `ctx` 可直接通过 `auth.FromContext(ctx)` 取 principal，无需经过 Conn 对象：

```go
hub := rtws.NewHub(cfg, func(ctx context.Context, connID string, data []byte) {
    p, ok := auth.FromContext(ctx)
    if !ok || p == nil {
        slog.Warn("ws: message from unauthenticated conn", slog.String("conn_id", connID))
        return
    }
    // p.Subject / p.Roles / p.ExpiresAt 是握手时快照的只读字段，禁止修改。
    _ = p.Subject
    _ = p.Roles
    // 业务处理...
})
```

### 3.4 Principal immutability 约定（P2-1）

Authenticator 返回 `*auth.Principal` 后，调用方**禁止修改**其任何字段（`Subject` / `Roles` / `ExpiresAt` / `Claims`）。Hub 在握手时从 `conn.Principal()` 快照 `subject` 和 `expiresAt` 到 `connEntry`，注册完成后 hub 内部不再回读 `conn.Principal()`。`Conn.Principal()` 字段在整个连接生命周期内必须保持不变；如需更新身份，客户端应重新握手。

### 3.5 composition root 示例

```go
// 选一：ContextAuthenticator（/api/v1/* JWT listener 上的推荐方式）
handler, err := adapterws.UpgradeHandler(hub, adapterws.UpgradeConfig{
    AllowedOrigins: []string{"https://app.example.com"},
    Authenticator:  auth.NewContextAuthenticator(),
})

// 选二：JWTAuthenticator（独立端口，需 Bearer token）
// verifier 实现 auth.IntentTokenVerifier 接口
handler, err := adapterws.UpgradeHandler(hub, adapterws.UpgradeConfig{
    AllowedOrigins: []string{"https://app.example.com"},
    Authenticator:  auth.NewJWTAuthenticator(verifier),
})

// 选三：AnonymousAuthenticator（广播频道，无认证）
handler, err := adapterws.UpgradeHandler(hub, adapterws.UpgradeConfig{
    AllowedOrigins: []string{"https://app.example.com"},
    Authenticator:  auth.NewAnonymousAuthenticator(),
})
```

### 3.6 service principal

service token 的身份通过 `CallerCellID` 表达，**不是** `Subject`（service principal 的 `Subject` 一律为空）。过滤 service 连接时应读 `p.CallerCellID`：

```go
// 按 CallerCellID 过滤特定 cell 的 service 连接
err := hub.BroadcastFilter(ctx, data, func(c rtws.Conn) bool {
    p := c.Principal()
    return p != nil && p.CallerCellID == "accesscore"
})
```

---

## 4. 心跳与 token 续期

Hub 内置 ping-pong 循环：

- **PingInterval**（默认 30s）：每轮向所有连接发 ping。
- **PingMissMax**（默认 2）：连续 miss 达到阈值则驱逐连接。
- **PingTimeout**（默认 5s）：单次 ping 的 deadline。

**Token 过期驱逐**：ping loop 每轮先于发 ping 检查 `Principal.ExpiresAt`。若当前时间已超过 `ExpiresAt`，连接被驱逐，无需等待下一次 miss。`ExpiresAt.IsZero()` 时不检查（Anonymous principal 不过期）。token 过期驱逐在 slog 中带 `reason="token_expired"` 结构化字段（P2-3）。

**v1.0 不支持服务端 push refresh**：token 续期必须由客户端主动操作：

1. 客户端检测到 token 临近过期（推荐提前 60s）。
2. 客户端通过原认证 API 获取新 token。
3. 客户端主动断开 WS 连接，用新 token 重新握手建立连接。

**最坏情况**下，token 过期后**最多 `PingInterval`（默认 30s）**才被驱逐；对敏感场景缩短 `PingInterval`。

---

## 5. 重连策略

客户端实现指数退避重连（推荐参数）：

```
初始延迟: 1s
倍增系数: 2
最大延迟: 30s
抖动:     ±500ms（防止 thundering herd）
```

示例序列：`1s → 2s → 4s → 8s → 16s → 30s → 30s → ...`

### HTTP 状态码对照表（P2-2）

| 状态码 | 触发条件 | 客户端动作 |
|---|---|---|
| 101 | 升级成功 | — |
| 400 | 客户端握手协议违规（无 `Sec-WebSocket-Key`、Origin 拒绝、非 GET 等） | 修正客户端实现，不重试 |
| 401 | 凭证缺失或无效 | 重新获取 token / 跳认证 |
| 500 | 服务端错误（hijack 不支持、Accept I/O 失败） | 指数退避重试 |
| 503 | hub 未启动或停机中 | 指数退避重试，等 readyz 通过 |

服务端关系：

- `hub.IsRunning() == false` 时，`UpgradeHandler` 返回 `503 Service Unavailable`。客户端收到 503 应继续退避重连。
- hub 停止（`Stop` 调用或 `Start` ctx cancel）时，所有连接被关闭。客户端重连请求在 hub 重新就绪前会持续得到 503。
- 正常停机顺序：先停止 hub（关闭连接），readyz 返回 503 防止 LB 继续路由新请求。

---

## 6. 广播：BroadcastFilter vs BroadcastToSubject

### BroadcastFilter — 通用过滤广播

```go
// filter 必填，nil 返回 errcode.ErrWebsocketBroadcastFilterMissing
err := hub.BroadcastFilter(ctx, data, func(c rtws.Conn) bool {
    p := c.Principal()
    return p != nil && p.CallerCellID == "accesscore"
})

// 全广播（显式）
err := hub.BroadcastFilter(ctx, data, func(rtws.Conn) bool { return true })
```

特性：

- O(N) 迭代所有连接。
- **filter 在锁外执行**（P1-2）：Hub 在 connMu 下 snapshot 连接列表后释放锁，再逐个调用 filter。因此 filter 慢不会阻塞 `Register` / `Stop`，filter 内可以**安全**调用 `hub.Send(...)` / `hub.BroadcastToSubject(...)` 而不会死锁。
- **仍建议 filter O(1) cheap**：filter 变慢只会拖慢该次广播延迟（snapshot 完成后才迭代），但不影响连接管理；禁止在 filter 内发起 DB 查询或远程 RPC（N 连接 × RPC 延迟 = 广播延迟放大反模式）。

### BroadcastToSubject — subject 索引广播

```go
// O(1) 索引 lookup，通过 subjectIdx 直接定位
// subject == "" 返回 errcode.ErrWebsocketBroadcastSubjectMissing
// subject 不存在（无连接）→ noop，返回 nil
err := hub.BroadcastToSubject(ctx, userID, data)
```

特性：

- `subjectIdx` 由 Hub 在 Register/Unregister 时维护，与 `conns` 严格同步。
- Subject 来自 `conn.Principal().Subject`（JWT sub claim）。service principal 的 Subject 为空，不进入 subjectIdx；service 连接应通过 `BroadcastFilter` + `CallerCellID` 路由。
- Anonymous principal（Subject == ""）不进入 subjectIdx。

### ctx 行为说明（P1-5）

`Send` / `BroadcastFilter` / `BroadcastToSubject` 在入队前检查调用方 ctx：

- 若调用方 ctx 已 canceled，立即返回 `ctx.Err()`，不向 send channel 投递任何消息（short-circuit）。
- **ctx 仅控制入队 timeout**；writeLoop 写 socket 使用内部 per-connection connCtx，调用方 ctx canceled 后已成功入队的消息**仍会**被 writeLoop 送达。
- 调用方 ctx 不会污染 send channel（canceled ctx 永远不会把消息写入 channel）。

### 多租户 ACL 示例

```go
// 按 subject 精准推送（用户数据变更通知）
func notifyUser(hub *rtws.Hub, userID string, event []byte) error {
    return hub.BroadcastToSubject(ctx, userID, event)
}

// 按 Cell 过滤广播（仅推送给特定 cell 的 service 连接）
func broadcastToCell(hub *rtws.Hub, cellID string, event []byte) error {
    return hub.BroadcastFilter(ctx, event, func(c rtws.Conn) bool {
        p := c.Principal()
        return p != nil && p.CallerCellID == cellID
    })
}
```

---

## 7. 慢客户端驱逐

每个连接有独立的 send channel，容量由 `HubConfig.SendBufferSize`（默认 32）控制。**零值自动 fallback 到默认值 32**（与 `PingInterval`、`PingMissMax` 等字段同模式），不存在"0 = unbuffered"语义。

**驱逐触发条件**：

- `BroadcastFilter` / `BroadcastToSubject` fanout 时，`send` channel 满 → 立即驱逐（select default-drop）。
- `Send(connID)` 时，channel 满 → 驱逐 + 返回 `errcode.ErrWebsocketSlowClient`。
- `writeLoop` 内 `conn.Write` 失败（网络错误） → 同路径驱逐。

客户端必须容忍服务端主动断开连接。收到 `1001 Going Away` 或 EOF 后执行重连退避逻辑。

**evict slog reason 字段**（P2-3）：所有驱逐路径在 slog 输出中均包含 `reason` 结构化字段，运维可按 label 区分驱逐原因：

| `reason` 值 | 触发路径 |
|---|---|
| `send_buffer_full` | 慢客户端：send channel 满，fanout 或 Send 触发 |
| `connection_write_failed` | writeLoop 写 socket 失败（网络中断） |
| `token_expired` | ping loop 检测到 Principal.ExpiresAt 超期 |
| `duplicate_conn_id` | Register 时发现同 ID 的旧连接，旧连接被驱逐 |

---

## 8. 故障注入与压测

### fakeConn 模式

`runtime/websocket/hub_test.go` 提供 `fakeConn` 参考实现：

```go
// 正常连接
conn := newFakeConn("conn-1", &auth.Principal{Subject: "user-1"})
require.NoError(t, hub.Register(ctx, conn))

// 阻塞连接（模拟慢客户端）
conn := newBlockingFakeConn("slow-1", &auth.Principal{Subject: "slow"})
require.NoError(t, hub.Register(ctx, conn))
// 触发 BroadcastFilter 后，conn 的 send buffer 满 → 驱逐
```

### clockmock 推进 token 过期

`clockmock.New(initial time.Time)` 返回 `*FakeClock`，参数为初始时刻（`time.Time`），**不是** `*testing.T`：

```go
clk := clockmock.New(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
hub := rtws.NewHub(rtws.DefaultHubConfig(clk), nil)

p := &auth.Principal{
    Subject:   "user-1",
    ExpiresAt: clk.Now().Add(5 * time.Minute),
}
conn := newFakeConn("conn-1", p)
require.NoError(t, hub.Register(ctx, conn))

// 推进时钟，超过 token 过期时间
clk.Advance(6 * time.Minute)

// 下一个 ping tick 触发过期驱逐
clk.Advance(hub.Config().PingInterval)

require.Eventually(t, func() bool {
    return hub.ConnCount() == 0
}, time.Second, 10*time.Millisecond)
```

### BroadcastToSubject 异步断言

`BroadcastToSubject` 将数据投入 writeLoop goroutine 的 channel，断言需要等待异步完成：

```go
err := hub.BroadcastToSubject(ctx, "user-1", []byte("hello"))
require.NoError(t, err)

// 等待 writeLoop 实际投递
require.Eventually(t, func() bool {
    return conn.ReceivedCount() == 1
}, time.Second, time.Millisecond)
```

---

## 9. 运维参数表

| 字段 | DefaultHubConfig 值 | 调优触发条件 |
|---|---|---|
| `PingInterval` | `30s` | 网络不稳定时减小（10s）；连接数大时增大（60s）减少 ping 开销 |
| `PingTimeout` | `5s` | 高延迟网络（如跨大洲）适当增大；需要快速检测死连接时减小 |
| `ReadLimit` | `64KB` | 消息 payload 超限时按业务需求增大；安全边界低于默认值可减小 |
| `PingMissMax` | `2` | 对抖动容忍高时增大（3-5）；严格活跃性检测时设为 1 |
| `MaxConnections` | `0`（无限制） | 防止 OOM 时设置上限（如 10000）；与 CPU/内存容量匹配 |
| `SendBufferSize` | `32`；**零值自动取默认值 32** | 高吞吐推送时增大（64-256）；严格 fail-closed 时减小 |
| `Clock` | 无默认，必须传入 | `clock.Real()` for production；`clockmock.New(time.Now())` for tests |

**evict slog `reason` 字段**：所有连接驱逐均在 slog 中附带 `reason` 字段，可用于告警分类：`send_buffer_full`（慢客户端）、`token_expired`（token 超期）、`connection_write_failed`（写失败/网络中断）、`duplicate_conn_id`（重复连接 ID）。

---

## 更多

- 架构决策：`docs/architecture/202605011500-adr-ws-auth-acl.md`（SEC-FAIL-CLOSED 设计）
- 错误码参考：`pkg/errcode/errcode.go`（`ErrWebsocket*` 系列）
- archtest 规则：`tools/archtest/security_defaults_test.go`（SEC-07/08/09）

ref: coder/websocket accept.go; centrifugal/centrifuge hub.go; olahol/melody hub.go
