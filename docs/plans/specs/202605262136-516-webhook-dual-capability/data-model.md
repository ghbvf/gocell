# Data Model: Webhook 双向能力

**Phase**: 1
**Date**: 2026-05-26

## 实体清单

### 1. Source（入站来源）

| 字段 | 类型 | 说明 |
|------|------|------|
| ID | SourceID（typed string） | 唯一标识，如 `"stripe"`、`"shopify"`，URL safe（`[a-z0-9-]`） |
| Secrets | `[][]byte` | 一把或多把活跃密钥（支持轮换） |
| Algorithm | Algorithm（typed const） | 固定 `AlgorithmHMACSHA256`（初版） |
| ToleranceSeconds | int | timestamp 容忍窗口（默认 300，双向） |

**生命周期**：注册时（bootstrap 或 hot reload）写入 SourceRegistry；运行时只读；密钥轮换由部署侧重启或 ConfigWatcher 触发热加载（初版仅 bootstrap）。

### 2. Endpoint（接收端点）

| 字段 | 类型 | 说明 |
|------|------|------|
| Path | string | 路由路径，如 `/api/webhooks/stripe/payment-events` |
| ContractID | string | 关联的 webhook contract ID（如 `webhook.stripe.payment-events.v1`） |
| SourceID | SourceID | 绑定的来源（决定签名密钥） |
| CellID | string | 归属 Cell（observability owner） |
| Handler | func(ctx, Delivery) error | 业务回调 |

**生命周期**：由 cellgen 从 slice.yaml 派生 `reg.RegisterWebhookReceiver(spec, handler)` 注册；进程启动时 bootstrap drain 到 router；不支持 runtime 动态增删。

### 3. Delivery（一次回调单元）

| 字段 | 类型 | 说明 |
|------|------|------|
| Direction | enum | `Inbound` / `Outbound` |
| ID | DeliveryID（typed string） | 入站取自请求 header；出站取自 outbox entry ID |
| Source / Target | string | 入站 = SourceID；出站 = DispatchTarget 名 |
| Timestamp | time.Time | 入站取自签名 header；出站取自 dispatcher 即时时钟 |
| Payload | []byte | 原始 body |
| Headers | Headers struct | DeliveryID / Timestamp / Signature 三元组 |

**幂等键**：`"webhook:{direction}:{source-or-target}:{id}"`，经 `kernel/idempotency.Claimer` 两阶段。

**Delivery 状态机（Inbound）**：

```
incoming
  │
  ├─ signature/timestamp invalid → reject (401, no claim)
  │
  └─ valid
     │
     ├─ Claimer.Claim
     │    ├─ ClaimAcquired → handler.Call
     │    │     ├─ ok → Claimer.Commit → 200
     │    │     └─ err → Claimer.Release → 5xx (retry by sender)
     │    │
     │    └─ ClaimDone（duplicate）→ 200 idempotent
     │
     └─ Claimer 故障 → fail-closed Requeue（502，让对端重发）
```

**Delivery 状态机（Outbound, 复用 outbox.Entry 状态）**：

```
outbox.Entry (status=pending)
  │
  └─ relay claim → dispatcher.Handle
       │
       ├─ SSRF check
       │    ├─ ip allowed → http POST
       │    │   ├─ 2xx → outbox.Ack → status=published
       │    │   ├─ 4xx → outbox.Reject(Permanent) → status=dead → DLX
       │    │   ├─ 5xx → outbox.Requeue → status=retry (按 Svix schedule)
       │    │   ├─ timeout → outbox.Requeue → 同 5xx
       │    │   └─ redirect → outbox.Reject(Permanent) → DLX (禁止跟随)
       │    │
       │    └─ ip blocked → outbox.Reject(Permanent) → DLX (SSRF deny)
       │
       └─ retry 预算耗尽 → status=dead → DLX
```

### 4. DispatchTarget（出站目标）

| 字段 | 类型 | 说明 |
|------|------|------|
| Name | string | slice.yaml 中声明的逻辑名，如 `"ShopifyOrderUpdated"` |
| URLSelector | func(ctx, event) string | 业务侧给出 URL（动态从 event/config 解析）|
| SourceID | SourceID | 用于签名的密钥来源 |
| ContractID | string | 关联的 webhook contract（dispatcher 角色） |
| CellID | string | 归属 Cell |

**生命周期**：由 cellgen 从 slice.yaml 派生 `reg.RegisterWebhookDispatch(spec, urlSelector)` 注册；运行时只读。

### 5. RetrySchedule

| 字段 | 类型 | 说明 |
|------|------|------|
| Steps | []time.Duration | 退避间隔序列；默认 `DefaultSvixSchedule = [5s, 5min, 30min, 2h, 5h, 10h, 10h, 10h]` |
| MaxAttempts | int | 派生自 `len(Steps)`，初版固定 8 |

**约束**：
- Step 单调递增（非严格——支持平台口（重复 10h）但禁止下降）
- 最大间隔 ≤ 10h（SC-006）
- 总覆盖时间 ≤ 48h（防夜间无人值守期间消耗预算后未察觉）

## 状态/事件流图

```
                    (US1 Receiver flow)

   external POST → CSRF/path.Clean → BodyLimit → WebhookVerify
                                                       │
                          fail (signature/timestamp)─→ 401
                                                       │
                                              Claimer.Claim
                                                  │
                                       ClaimDone ─→ 200 idempotent
                                                  │
                                          ClaimAcquired
                                                  │
                                          business handler
                                              │       │
                                            err     ok
                                              │       │
                                          Release  Commit
                                              │       │
                                            5xx    200



                    (US2 Dispatcher flow)

   business publish → outbox.Entry persisted → relay claim
                                                     │
                                          dispatcher.Handle
                                                     │
                                             ssrf.SafeDial
                                              │           │
                                       blocked         allowed
                                              │           │
                                        Reject(Perm)   http POST
                                              │     ┌──┴──┬──┬──┐
                                            DLX    2xx   4xx 5xx t/o
                                                    │     │  │   │
                                                   Ack  Rej Req Req
```

## 与现有 GoCell 实体的接缝

| 现有实体 | 接缝点 | 说明 |
|---------|--------|------|
| `kernel/idempotency.Claimer` | Receiver Delivery 经两阶段 | key 命名空间 `"webhook:..."` |
| `kernel/outbox.Entry` | Dispatcher 输入 | 复用现有 entry 字段；dispatch 元数据放 `EntryMeta` |
| `kernel/outbox.HandleResult` | Dispatcher 输出 | Ack / Requeue / Reject(Permanent) |
| `kernel/healthz.RepoProber` | Dispatcher target 可达性 probe | 不强制（仅 receiver/dispatcher 自身 `_ready`） |
| `cell.Registrar` | RegisterWebhookReceiver / RegisterWebhookDispatch | 与现有 reg.Subscribe 同范式（位置参数 cellID 必填）|
| `runtime/http/middleware.Recovery` | Receiver panic 兜底 | 复用 |
| `runtime/http/router.Router` | Receiver 端点挂载 | 通过 cell.RouteGroup 注册 |
| `pkg/redaction.IsSensitiveKey` | 密钥 / 签名 redact | 扩 6 key |

## 边界条件（与 spec.md Edge Cases 映射）

| Edge case | 实体/字段承载 |
|----------|--------------|
| 密钥轮换 | Source.Secrets 多把同时有效 |
| 时钟漂移 5min 边界 | Verifier 双向窗口（now-tol ≤ ts ≤ now+tol） |
| 未来 timestamp | 双向窗口右侧拒绝 |
| handler panic | Recovery middleware（已有） |
| Claim 未 commit 崩溃 | Claimer ReleaseAfterTTL（已有） |
| DNS rebinding | ssrf.SafeDialContext dial-time 二次校验 |
| Redirect 内网 | http.Client.CheckRedirect = ErrUseLastResponse |
| 巨大 response body | io.LimitReader(1MB) |
| 巨大 nested JSON | stdlib json 默认深度兜底（500） |
| Path traversal | CSRF middleware path.Clean（已有） |
| Secret 漏配 | NewSource() fail-fast（required field） |
