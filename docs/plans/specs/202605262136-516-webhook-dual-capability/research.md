# Research Notes: Webhook 双向能力

**Phase**: 0
**Source**: ship 技能阶段 1 三个并行 explorer agent 的浓缩结果
**Date**: 2026-05-26

## 对标项目研究

| 项目 | 用途 | 关键源码引用 |
|------|------|------------|
| Svix | 行业事实标准 SaaS（receiver + dispatcher 双向） | `svix/svix-webhooks` go/webhook.go @ main |
| Stripe webhooks | HMAC-SHA256 + 5min timestamp tolerance | `stripe/stripe-go` webhook/client.go @ master |
| Slack events API | signing secret v0 协议 | `slack-go/slack` security.go @ master |
| GitHub webhooks | X-Hub-Signature-256 + delivery_id 幂等 | `google/go-github` github/messages.go @ master |
| Watermill HTTP | HTTP receiver per-source path 注册 | `ThreeDotsLabs/watermill-http` pkg/http/subscriber.go @ master |
| Convoy | dispatcher 全状态机 + SSRF 防御 | https://www.getconvoy.io/blog/circuit-breaker-in-golang |
| safeurl | SSRF 库（Go） | https://blog.doyensec.com/2022/12/13/safeurl.html |

## 关键技术决策（与对标对齐）

### Receiver 签名

- **算法**：HMAC-SHA256 单算法（业界 80%+ 平台首选，Svix/Stripe/GitHub/Slack 一致）
- **格式**：`{webhook_id}.{unix_ts}.{body_bytes}`（Svix 范式，比 Stripe 的 `t=<ts>.{body}` 多一段 ID，更利于本地 round-trip 测试）
- **多签名头**：支持 `v1,sig1 v1,sig2` 空格分隔（key rotation 过渡期）
- **timestamp 容忍**：双向 5 分钟（Svix 拒 too old + too new，比 Stripe/Slack 单向 5min 更严防时钟攻击）
- **常时比较**：`hmac.Equal` stdlib，禁止 `==` / `bytes.Equal`
- **raw body 保留**：签名校验在 JSON 解析前

### Dispatcher 出站

- **retry schedule**：Svix 8 步固定（5s / 5min / 30min / 2h / 5h / 10h / 10h / 10h），共覆盖 ~38 小时；不用 Stripe 指数退避（无界，与 outbox MaxRetries 槽位不易对齐）
- **header schema**：`X-Webhook-Id` / `X-Webhook-Timestamp` / `X-Webhook-Signature: v1,<base64>`（Svix 范式）
- **超时**：每请求 30s（context + http.Client.Timeout）
- **response body limit**：默认 1MB（防 amplification）

### SSRF 防御（Convoy + safeurl 范式）

- **CIDR 黑名单（IPv4）**：`127.0.0.0/8` / `10.0.0.0/8` / `172.16.0.0/12` / `192.168.0.0/16` / `169.254.0.0/16` / `0.0.0.0/8` / `192.0.2.0/24` / `198.51.100.0/24` / `203.0.113.0/24`（文档段）
- **CIDR 黑名单（IPv6）**：`::1/128` / `fc00::/7`（ULA）/ `fe80::/10`（link-local）/ `::ffff:0:0/96`（IPv4-mapped）/ `2002::/16`（6to4）
- **DNS rebinding**：自定义 `net.Dialer.DialContext`，dial 前 host resolve 校验 + dial 后再校验解析 IP（防 TTL=0 轮换）
- **redirect**：禁止跟随（`http.Client.CheckRedirect = ErrUseLastResponse`）
- **scheme**：仅 `https`；`WithAllowLoopback()` opt-in 给 dev/test 用

### 与现有 GoCell 基建对齐

- **幂等**：`kernel/idempotency.Claimer` 两阶段 Claim/Commit/Release，key = `"webhook:{sourceID}:{delivery_id}"`，TTL 24h
- **outbox**：dispatcher 实现 `outbox.EntryHandler`，HandleResult.Requeue → retry，Reject → DLX
- **error 出口**：复用 `pkg/redaction.RedactError` + `safeStringAttr`（覆盖 span/error）；扩 `IsSensitiveKey` 6 个 key
- **errcode**：12 个新 sentinel 全部 const literal message，runtime 数据走 `WithDetails`/`WithInternal`
- **panic**：所有 panic 经 `panicregister.Approved("webhook-*", errcode.Assertion(...))` funnel
- **healthz**：typed const ProbeName（`webhook_receiver_ready` / `webhook_dispatcher_ready`），不与 outbox/PG probe 同义重复
- **metrics**：`webhook_deliveries_total{result, source}` / `webhook_signature_failures_total{source, reason}` / `webhook_delivery_duration_seconds` / `webhook_idempotency_hits_total`

## 决策记录与拒绝

| 决策 | 选项 A（采纳） | 选项 B（拒绝） | 拒绝理由 |
|------|--------------|--------------|---------|
| 签名算法初版 | HMAC-SHA256 单算法 | + Ed25519 / KMS | 用户在 ship AskUserQuestion 确认选 Recommended；行业 80%+ 用 HMAC；KMS 触发 adapter 依赖跨层 |
| Source 密钥后端 | in-memory + interface | configcore / vault | configcore 加密能力另行评估；vault 触发 adapter 依赖跨层 |
| cellgen 派生 | 进初版 | follow-up | 用户要求"做彻底，不行就加 PR"；与 reg.Subscribe 同范式，是 GoCell cell-native 范式的必需 |
| Circuit breaker | 简化版（outbox MaxRetries + endpoint 标记 disabled） | 完整状态机（Convoy 模式 + 分布式锁） | 简化版已满足 acceptance scenarios；完整状态机作为 V1.1+ follow-up |
| Egress HTTP proxy | 接缝（WithHTTPClient option，让部署侧自决） | 内置 | 框架不耦合具体 proxy 实现；CIDR 黑名单作为默认兜底 |
| Receiver 端点路径模式 | per-source 路径（与 Watermill 一致） | 单 dispatch handler | per-source 利于 secret 隔离 + path 隔离 + 路由清晰 |

## Open Items（保留作 follow-up issue 跟踪，**不进**本特性）

1. Ed25519 签名算法支持
2. Vault Transit / KMS 签名（密钥不出 KMS）
3. Source secret 持久化到 configcore（加密）/ vault（HSM）
4. Circuit breaker 全状态机（Convoy 范式）
5. webhook 投递端到端 trace propagation（W3C Trace Context 注入到 X-Webhook-Trace-* header）

每条 follow-up 在 PR-6 backlog closeout 时开出 gh issue 并贴 `backlog` + `pri-pX` label。
