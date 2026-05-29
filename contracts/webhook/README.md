# `webhook` contract kind

`webhook` is GoCell's fifth contract kind, alongside `http` / `event` / `command` /
`projection`. It declares the immutable contract for an HTTP webhook endpoint that
crosses the trust boundary to an **external** system (Stripe, Shopify, GitHub …),
in one of two directions:

| direction | 我方角色 | slice role | 对端 |
|-----------|---------|-----------|------|
| `inbound` | receiver（接收外部回调，HMAC + timestamp + Claimer 三重保护） | `webhook-receive` | 外部 source 推送给我们 |
| `outbound` | dispatcher（签名后推送，SSRF 防御 + 退避重试） | `webhook-dispatch` | 我们推送给外部 target |

> **Scope note (PR-2)**: 本目录与 cellgen 派生**只覆盖契约识别 + 代码生成层**。运行时
> （`reg.RegisterWebhookReceiver/Dispatch` 方法本体、HTTP 接收 middleware、dispatcher
> outbox consumer、SSRF guard、healthz/metrics）在后续 PR 落地（receiver = PR-3，
> SSRF = PR-4，dispatcher = PR-5）。kernel 纯计算内核（Signer/Verifier/Source）已在
> PR-1（#1251）落地于 `kernel/webhook/`。
>
> **PR-2 → PR-3/5/6 排序约束（forward reference）**：cellgen 的 `cell.tmpl` 已无条件
> emit `reg.RegisterWebhookReceiver/Dispatch` 调用，但这两个 `cell.Registrar` 方法本体
> 在 PR-3/PR-5 才落地。PR-2 之所以 `go build ./...` 绿，是因为**当前没有任何真实 webhook
> cell**——这些调用只存在于 cellgen 的 golden 文本夹具（`*.go.golden`，不参与编译）。
> 因此 **PR-3（receiver）/ PR-5（dispatcher）必须先把对应 Registrar 方法 + bootstrap drain
> 落地，才能生成第一个真实 webhook cell（PR-6 demo）**；否则 `gocell generate cell` 会产出
> 引用不存在方法的 `cell_gen.go`（编译失败）。这是有意的 seam 排序，不是缺陷。
>
> **codegen 零产物语义**：webhook 契约即便 `codegen: true`，contractgen 也**有意产出零
> per-contract 产物**（注册经 cellgen 字面量，不经 contractgen；见 `generator.go` webhook 分支
> + 下方差异表）。`codegen: true` 仅用于让 `Generate(ScopeAll)` 选中该契约。`CODEGEN-CONTRACT-GEN-01`
> archtest（opted-in 契约须有 `types_gen.go`/`iface_gen.go`）**已对 webhook kind 显式豁免**
> （本 PR 落地：`TestCodegenContractGen01_OptedInHasGen` 对 `Kind == "webhook"` 提前 `continue`，
> 与 `generator.go` 零产物分支对齐）。因此 PR-6 落地首个真实 webhook 契约（即使 `codegen: true`）
> 时不会 false-fail。

## 文件布局

```
contracts/webhook/{domain-path}/{version}/contract.yaml
contracts/webhook/{domain-path}/{version}/payload.schema.json
```

例：`contracts/webhook/stripe/payment-events/v1/contract.yaml`

> PR-2 不落地真实 webhook 契约——首个真实契约 + cell demo 在 PR-6。本文档的 contract.yaml
> 示例与 codegen 的合成夹具（`tools/codegen/{contractgen,cellgen}/testdata/`）一致。

## contract.yaml 字段集

```yaml
id: webhook.stripe.payment-events.v1
kind: webhook
direction: inbound                 # inbound | outbound
ownerCell: paymentcore
lifecycle: active
signature:
  algorithm: hmac-sha256           # 初版唯一合法值（PR-1 kernel 已实现）
  toleranceSeconds: 300            # 双向时间窗（拒 too old + too new）
  deliveryIDHeader: X-Stripe-Delivery-Id
  timestampHeader: X-Stripe-Timestamp
  signatureHeader: X-Stripe-Signature
  signedStringForm: "{deliveryID}.{timestamp}.{body}"
endpoints:
  inbound:                         # 仅 inbound；outbound 的 URL 由业务侧 selector 给出
    pathPattern: /api/webhooks/stripe/payment-events
    sourceID: stripe
payload:
  schemaRef: ./payload.schema.json
  contentType: application/json
  maxBodyBytes: 1048576            # 1 MB
```

`outbound` 方向字段集更精简——dispatcher 用业务侧 selector 给出 target URL，无 `endpoints.inbound`；
`signature` / `payload` 对 outbound **非必填**（dispatcher 用自己配置的密钥签名）：

```yaml
id: webhook.shopify.orders.v1
kind: webhook
direction: outbound                # inbound | outbound
ownerCell: ordercore
lifecycle: active
# outbound 无 endpoints.inbound；签名密钥来源由 slice.yaml 的 sourceID 指定。
# 若声明 signature 块，algorithm 仍只能是 hmac-sha256（FMT-37 校验）。
```

> **inbound 必填字段（FMT-37 live 校验）**：`direction: inbound` 的契约**必须**声明 `signature`
> 块（`algorithm: hmac-sha256` 为唯一合法值）+ `payload` 块——否则 `gocell validate` 报 error
> （与 event 的 FMT-04 必填对等，闭合 fail-open）。`algorithm` 的值集封闭由 contract.schema.json
> 的 `enum: [hmac-sha256]` 与 FMT-37 双重守卫。

`endpoints.receivers` / `endpoints.dispatchers` 是**派生字段**（`yaml:"-"`，由 parser 从
slice.yaml 的 `contractUsages[role=webhook-receive|webhook-dispatch]` 的 `belongsToCell`
计算），**禁止手写**——KnownFields 严格解码会拒绝。archtest
`CONTRACT-YAML-WEBHOOK-FIELDS-FROZEN-01` reflect 锁这两个字段的 `yaml:"-"` 标签。

## slice.yaml 声明（单源真值）

业务 cell 在 slice.yaml 通过 contractUsages 引用契约，cellgen 据此派生注册代码：

```yaml
# inbound
contractUsages:
  - contract: webhook.stripe.payment-events.v1
    role: webhook-receive          # 新 role
    handler: HandleStripeEvent     # 必填，业务消费 handler 方法名
    sourceID: stripe               # 必填，密钥隔离键（须 == contract.endpoints.inbound.sourceID）
verify:
  contract:
    # 必填——VERIFY-01 强制每个 contractUsage 有对应 verify.contract 条目（或 waiver）。
    # key 格式 contract.<contractID>.<role>，role 用完整角色名 webhook-receive（非 receive）。
    - contract.webhook.stripe.payment-events.v1.webhook-receive

# outbound
contractUsages:
  - contract: webhook.shopify.orders.v1
    role: webhook-dispatch         # 新 role
    targetSelector: ShopifyTarget  # 必填，target URL 选择器方法名
    sourceID: shopify              # 必填，签名密钥来源
verify:
  contract:
    - contract.webhook.shopify.orders.v1.webhook-dispatch
```

> **verify.contract 必填**：与 subscribe 同范式，VERIFY-01 对每个 webhook contractUsage 强制
> 一条 `contract.<contractID>.<role>` 条目（断言该角色有可执行的 receive/dispatch contract
> 测试），无测试时用 waiver 记录缺口。漏写 = `gocell validate` 报 error。

新建订阅 slice 时，cell.go 结构体须声明对应 `*<sliceID>.Service` 字段（与 subscribe 同范式，
cellgen 按「字段指针类型包名 == sliceID」解析 handler 表达式）。

## cellgen 派生形态（生成进 cell_gen.go，不手写）

```go
// Code generated by gocell generate cell. DO NOT EDIT.
func (c *PaymentCoreCell) Init(ctx context.Context, reg cell.Registrar) error {
    if err := c.BaseCell.Init(ctx, reg); err != nil {
        return err
    }
    // initInternal: hand-written init hook (permanent K#04 convention; implement in cell.go)
    if err := c.initInternal(ctx, reg); err != nil {
        return err
    }

    if err := reg.RegisterWebhookReceiver(webhook.ReceiverSpec{
        ContractID: "webhook.stripe.payment-events.v1",
        SourceID:   "stripe",
        CellID:     "paymentcore",
    }, c.stripeSvc.HandleStripeEvent); err != nil {
        return fmt.Errorf("paymentcore: webhook-receive webhook.stripe.payment-events.v1: %w", err)
    }
    return nil
}
```

`webhook.ReceiverSpec` / `DispatchSpec` 是 `kernel/webhook` 的纯数据结构；`CellID` 作为字面量
由 cellgen 从 cell.yaml 注入（observability owner 溯源 cell 元数据，与 `reg.Subscribe` 的位置
cellID 同源约束）。`reg.RegisterWebhookReceiver/Dispatch` 这两个 `cell.Registrar` 方法本体在
PR-3/PR-5 落地。

## 静态守卫（archtest）

| invariant | 守卫 | 评级 |
|-----------|------|------|
| `CONTRACT-YAML-WEBHOOK-FIELDS-FROZEN-01` | `EndpointsMeta.Receivers` / `Dispatchers` 必须 `yaml:"-"`（reflect 锁，派生不可手写） | Hard |
| `WEBHOOK-MARKER-RETIRED-01` | markergen 只识别 `cell:`/`slice:` 前缀，`webhook:` marker 被**静默忽略**（不产生 wiring，也**不报错**）；AST backstop 扫 production cell.go 拦截残留 `// +webhook:*` marker，强制 slice.yaml 单源 | Medium（AST backstop scan；上游 markergen 结构上无法用 marker 驱动 wiring，但非编译期错误，无开发者反馈） |

### 故障排查（与 subscribe 同范式，见 `.claude/rules/gocell/eventbus.md`）

- **webhook 注册代码未生成**：检查 slice.yaml contractUsages 是否含 `role: webhook-receive` 或 `role: webhook-dispatch`；`// +webhook:*` 注释不产生任何效果也不报错（marker 已退役，单源是 slice.yaml）。
- **`gocell generate cell` 报 "no cell.go struct field for slice"**：cell.go 结构体缺少对应 webhook slice 的字段。在 cell struct 添加 `*<sliceID>.Service`（或 `*<sliceID>.Consumer`）字段后重新运行——该字段必须在 `generate` 执行前已存在于 cell.go（cellgen 按「字段指针类型包名 == sliceID」解析 handler/selector 表达式）。
- **`gocell generate cell` 报 "ambiguous field for slice <sliceID>"**：cell struct 中有多个字段的指针类型包名与 sliceID 相同。在 slice.yaml 的 webhook-receive / webhook-dispatch CU 中添加 `field: <fieldName>` 消歧。

## 与现有 contract kind 的差异

| 维度 | event | webhook |
|------|-------|---------|
| 传输 | AMQP | HTTP（外部对接） |
| 方向 | pub/sub | inbound / outbound 双向 |
| 签名 | 否 | HMAC-SHA256 强制 |
| 幂等 | consumer Claimer | Claimer（inbound）+ outbox delivery_id（outbound） |
| codegen 包 | per-contract `subscription_gen.go`（携带 Topic） | 无（注册用 `webhook.ReceiverSpec` 字面量，零 per-contract 生成包） |

## 演化策略

对齐 `.claude/rules/gocell/api-versioning.md`：v1.0 GA 前 wire 契约直接演化（无 v2、无
deprecation 期）；业务 payload schema 演化按 v1 schema evolution ADR（response/event payload
禁 `additionalProperties: false`）。
