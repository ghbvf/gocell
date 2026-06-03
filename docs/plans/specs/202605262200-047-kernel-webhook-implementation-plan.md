# 047 — kernel/webhook 双向能力实施计划

> 实施 backlog 条目 `KERNEL-WEBHOOK-01`（P3/Cx3 → 转 P2 in-flight）：webhook receiver (K39) + dispatcher (K40)，含 HMAC 签名、SSRF 防御、cellgen 派生注册。依赖 Outbox Relay (AL-01 ✅) + DistLock (AL-02 ✅) 均已稳定。
>
> **每个 PR diff ≤ 2000 行（含 test / fixture / ADR），6 PR 线性依赖。**

## 1. 目标与范围

### 1.1 In-scope（必须做彻底）

- `kernel/webhook/` 纯计算内核：Signer / Verifier / Dispatcher / SSRF / Retry（只依赖 stdlib + `pkg/errcode` + `pkg/redaction`）
- `runtime/webhook/` 集成层：HTTP middleware (receiver) + outbox consumer (dispatcher)
- `contracts/webhook/` schema：新 contract kind `webhook` + 双角色 `webhook-receive` / `webhook-dispatch`
- `tools/contractgen/` + `tools/cellgen/` 解析与代码生成：从 `slice.yaml contractUsages[role=webhook-*]` 单源派生 `reg.RegisterWebhookReceiver` / `reg.RegisterWebhookDispatch` 调用（与现有 `reg.Subscribe` 同范式）
- `pkg/errcode/` 12 新 sentinel（ERR_WEBHOOK_*）
- `pkg/redaction/` sensitive key 扩列（`webhook_secret` / `x_signature` 等 6 条）
- 3 个新 ADR：webhook-signing-algorithm / webhook-ssrf-policy / webhook-retry-default
- 5 条 archtest invariants（≥ Medium 评级，含 Hard 上游 / 下游双向锁）
- healthz probes + OTel metrics

### 1.2 Out-of-scope（显式 follow-up issue）

| Follow-up | 理由 | 跟踪 |
|----------|------|------|
| Ed25519 / Vault Transit KMS 签名 | HMAC-SHA256 是行业事实标准；KMS 涉及 adapters/vault 跨层依赖 | gh issue（待开） |
| Source secret 持久化到 configcore / vault | 初版 in-memory `SourceRegistry` + `SourceStore` interface 已可替换；configcore 加密能力另行评估 | gh issue（待开） |
| Circuit breaker 全状态机 | 初版用 outbox `MaxRetries` 触底 → endpoint 标记 `disabled` 简化版即可 | gh issue（待开） |
| Dispatcher egress HTTP proxy | 通过 `WithHTTPClient` option 让部署侧自决；CIDR 黑名单为默认兜底 | 无需 issue（架构已留接缝） |

### 1.3 Acceptance（验收准则）

- `kernel/webhook/` 测试覆盖率 ≥ 90%（kernel/ 层基线）
- `runtime/webhook/` ≥ 80%
- 5 条 archtest invariants 全部 CI 通过（含双向锁）
- 3 个 ADR 落地并被代码引用
- Backlog `KERNEL-WEBHOOK-01` 关闭
- `make verify` 全绿，CI shard matrix 全绿
- examples 中至少一个 cell（建议 `ssobff` 或新增 demo）通过 `slice.yaml` 声明 webhook 接收，cellgen 生成的注册代码可运行

## 2. 设计要点

### 2.1 分层约束

```
contracts/webhook/         — kind: webhook 契约 schema（与 event/sync/command 同级）
kernel/webhook/            — 纯计算：Signer/Verifier/Dispatcher/SSRF/Retry（stdlib + pkg/）
runtime/webhook/           — HTTP middleware + outbox consumer 集成
runtime/webhook/dispatch/  — dispatcher outbox.EntryHandler 适配
tools/contractgen/         — 解析 webhook contract
tools/cellgen/             — 从 slice.yaml 派生 reg.RegisterWebhookReceiver/Dispatch
```

**依赖规则验证**：
- `kernel/webhook/` 只 import stdlib + `pkg/errcode` + `pkg/redaction` + `kernel/idempotency` + `kernel/outbox` + `kernel/healthz`（符合 kernel/ 规则）
- `runtime/webhook/` import `kernel/webhook` + `runtime/http` + `runtime/outbox`（符合 runtime/ 规则）

### 2.2 核心 API surface（初稿，PR-1/PR-5 落定）

```go
// kernel/webhook/webhook.go
type Algorithm string
const AlgorithmHMACSHA256 Algorithm = "hmac-sha256"

type DeliveryID string  // typed marker for idempotency key
type SourceID string

type Source struct {
    ID     SourceID
    Secret []byte  // never logged; redacted in slog/span via sensitive key
}

// kernel/webhook/signer.go (sealed Signer interface for funnel)
type Signer interface {
    Sign(payload []byte, ts time.Time, deliveryID DeliveryID) (Headers, error)
    sealed()  // unexported marker — package-external implementations impossible
}

type Headers struct {
    DeliveryID DeliveryID
    Timestamp  string  // unix seconds
    Signature  string  // v1,<base64>
}

// kernel/webhook/verifier.go
type Verifier interface {
    Verify(rawBody []byte, headers Headers, source Source) error
    sealed()
}

// kernel/webhook/dispatcher.go (implements outbox.EntryHandler)
type Dispatcher struct {
    signer   Signer       `gocell:"required"`
    client   *http.Client `gocell:"required"`  // SSRF-wrapped Transport
    schedule RetrySchedule
}
func (d *Dispatcher) Handle(ctx context.Context, entry outbox.Entry) outbox.HandleResult

// kernel/webhook/ssrf.go
type SafeDialContext func(ctx context.Context, network, address string) (net.Conn, error)
type SafePolicy struct{ /* unexported config */ }         // single-source SSRF policy
func NewSafePolicy(opts ...SafeOption) *SafePolicy
func (p *SafePolicy) DialContext(ctx context.Context, network, address string) (net.Conn, error)
func (p *SafePolicy) ValidateTargetURL(rawURL string) error
func (p *SafePolicy) DenyRedirect(_ *http.Request, via []*http.Request) error
type SafeOption func(*SafePolicy)
func WithAllowLoopback() SafeOption  // dev-only opt-in (applies to dial AND pre-flight)
```

### 2.3 cellgen 派生模式（K42 接缝）

参考 `.claude/rules/gocell/eventbus.md` Cell 订阅注册（Registry builder 模式）的成熟范式：

```yaml
# slice.yaml — 单源真值
contractUsages:
  - contract: webhook.stripe.payment-events.v1
    role: webhook-receive           # 新 role
    handler: HandleStripeEvent      # 必填
    sourceID: stripe                # 必填，secret 隔离键

  - contract: webhook.shopify.orders.v1
    role: webhook-dispatch          # 新 role
    targetSelector: ShopifyTarget   # 必填，dispatcher 取 URL 的方法名
```

cellgen 派生进 `cell_gen.go`（不手写）：

```go
// Auto-generated by cellgen
func (c *MyCell) Init(ctx context.Context, reg cell.Registrar) error {
    if err := c.BaseCell.Init(ctx, reg); err != nil {
        return err
    }
    if err := reg.RegisterWebhookReceiver(webhook.ReceiverSpec{
        ContractID: "webhook.stripe.payment-events.v1",
        SourceID:   "stripe",
        CellID:     c.ID(),
    }, c.svc.HandleStripeEvent); err != nil {
        return err
    }
    return reg.RegisterWebhookDispatch(webhook.DispatchSpec{
        ContractID: "webhook.shopify.orders.v1",
        CellID:     c.ID(),
    }, c.svc.ShopifyTarget)
}
```

`CellID` 位置参数为 HARD 必填（对齐 `REGISTRY-SUBSCRIBE-CELLID-POSITIONAL-01` ADR 经验）。

### 2.4 archtest 评级与双向锁

| ID | 守卫目标 | 评级 | 上游 / 下游形态 |
|----|---------|------|----------------|
| WEBHOOK-HMAC-FUNNEL-01 | `crypto/hmac.New` callsite 限定 `kernel/webhook/signer.go` | Hard 下游 + Medium 上游 | 下游 callsite allowlist；上游 `Signer` sealed interface（`sealed()` unexported marker → 包外不可实现） |
| WEBHOOK-SIGNER-FUNNEL-01 | `Webhook-Signature` header 写入只经 Signer | Medium | 下游 AST ban literal `Header.Set("X-Webhook-Signature", ...)`；上游 Dispatcher struct field typed |
| WEBHOOK-SSRF-GUARD-01 | dispatcher 所有 HTTP 出站经 SafePolicy | Hard 下游 + Medium 上游 | 下游 `Dispatcher.client` 持有 `*webhook.SafePolicy`（唯一构造器 `NewSafePolicy`），`net.Dialer.DialContext` 仅 `(*SafePolicy).DialContext` 可调；上游 archtest ban `http.DefaultClient` + `net.Dial` / `net.DialTCP` 直调 |
| WEBHOOK-IDEMPOTENCY-CLAIMER-01 | Receiver 处理前必经 Claimer | Medium | 复用 `REQUIRED-DEP-NIL-GUARD-01` 框架，receiver struct claimer 字段 `gocell:"required"` |
| WEBHOOK-SIGNED-STRING-FORM-01 | 签名串格式 `{webhookID}.{timestamp}.{body}` 与 Svix 对齐 | Medium | golden fixture（Svix 官方 test vector）+ AST 锁 `fmt.Sprintf` 模板字面量 |

**已知盲区**（每条都附反向自检测试）：
- B1: `Transport.RoundTrip` 直调绕 SSRF → `TestSSRFGuard_NoDirectRoundTrip`
- B2: `type MySigner = webhook.Signer` 别名绕 funnel → `TestHMACFunnel_NoSignerAlias`
- B3: `fmt.Fprintf(w, "X-Webhook-Signature: ...")` 形态 → `TestSignerFunnel_NoFmtFprintfHeader`
- B4: reflect 绕 Claimer nil guard → `TestIdempotencyClaimer_NoReflectClaim`
- B5: `net.DialTCP` 直调 → `TestSSRFGuard_NoDirectDial`

## 3. PR 拆分（6 PR，全部 ≤ 2000 行 diff）

### PR-1: Foundation — kernel/webhook 核心 + errcode + redaction（估 1700-1900 行）

**分支名**：`feat/047-1-webhook-kernel-foundation`

**改动文件**：

| 文件 | 估行 | 说明 |
|------|------|------|
| `pkg/errcode/errcode.go` | +60 | 12 个 sentinel：ERR_WEBHOOK_INVALID_SIGNATURE / TIMESTAMP_EXPIRED / DUPLICATE_DELIVERY / INVALID_HEADER / ALGORITHM_UNSUPPORTED / SOURCE_NOT_FOUND / SSRF_BLOCKED / DELIVERY_FAILED / DELIVERY_TIMEOUT / PERMANENT_FAILURE / BODY_TOO_LARGE / CONFIG_INVALID |
| `pkg/errcode/errcode_test.go` | +80 | 12 sentinel coverage + Kind/HTTPStatus 映射 |
| `pkg/redaction/redaction.go` | +10 | sensitiveKeyPattern 追加 `webhook[_-]?secret`, `x[_-]?signature`, `x[_-]?hub[_-]?signature`, `x[_-]?webhook[_-]?signature`, `svix[_-]?signature`, `hmac[_-]?key` |
| `pkg/redaction/redaction_test.go` | +80 | 6 新 key 命中 / mask 验证 |
| `kernel/webhook/doc.go` | +50 | 包文档：API surface、AI-robust 评级、引用 ADR |
| `kernel/webhook/webhook.go` | +120 | typed: Algorithm / DeliveryID / SourceID / Source / Headers / Algorithm const |
| `kernel/webhook/signer.go` | +180 | hmacSigner 实现 + sealed Signer interface + algorithm typed funnel |
| `kernel/webhook/verifier.go` | +250 | hmacVerifier：HMAC 校验 + timestamp 双向窗口 + 常时比较 + header parser + 多签名头支持（key rotation） |
| `kernel/webhook/source.go` | +120 | in-memory SourceRegistry + SourceStore interface（外部可替换） |
| `kernel/webhook/webhook_test.go` | +250 | typed marker + Source 校验 + Algorithm typed const |
| `kernel/webhook/signer_test.go` | +200 | hmacSigner sign 输出 / sealed 不可包外实现验证 |
| `kernel/webhook/verifier_test.go` | +350 | table-driven：valid / 边界 / 错误（12 sentinel 全覆盖）/ timing-attack property |
| `fixtures/webhook-hmac-vectors.yaml` | +100 | Svix 官方 5 个 test vector（[Svix Manual Verification](https://docs.svix.com/receiving/verifying-payloads/how-manual)） + 自构 edge case 3 个 |
| `tools/archtest/webhook_hmac_funnel_test.go` | +280 | WEBHOOK-HMAC-FUNNEL-01：下游 callsite allowlist（`hmac.New` 调用点 ⊆ `kernel/webhook/signer.go`）+ 上游 sealed marker 检查 + 5 条反向盲区自检（B1/B2/B3/B5 + alias） |
| **合计** | **~2130** | 含 godoc/包文档预留 |

> 行数若超 2000，优先把 `verifier_test.go` 部分 case 抽到 conformance 包 `kernel/webhook/webhooktest/`，把 conformance 落 PR-3 一起。

**TDD 测试先写顺序**：errcode_test → redaction_test → webhook_test → signer_test → verifier_test → archtest

**对标参考（commit message refs）**：
- `ref: stripe/stripe-go webhook/client.go@master`
- `ref: svix/svix-webhooks go/webhook.go@main`
- `ref: slack-go/slack security.go@master`

**依赖**：无（首发）

**完成判定**：
- `go -C worktrees/047-1 test ./kernel/webhook/... ./pkg/errcode/... ./pkg/redaction/... ./tools/archtest/... -run Webhook` 全绿
- `go -C worktrees/047-1 test ./kernel/webhook/... -cover` ≥ 90%
- `golangci-lint run ./...` 0 issues
- `bash hack/verify-archtest-invariants.sh` 全绿

### PR-2: Contract schema + contractgen + cellgen 解析（估 1500-1800 行）

**分支名**：`feat/047-2-webhook-contract-codegen`

**改动文件**：

| 文件 | 估行 | 说明 |
|------|------|------|
| `contracts/webhook/README.md` | +80 | 包文档：kind / 双角色 / 字段集 / cellgen 派生模式 |
| `contracts/_schemas/webhook-contract.schema.json` | +120 | JSON Schema：webhook contract 字段集（id / version / endpoints / signature.algorithm 等） |
| `contracts/_schemas/slice.schema.json` | +30 | 扩 contractUsages[].role enum：追加 `webhook-receive` / `webhook-dispatch`，每个角色的必填字段（handler / sourceID / targetSelector） |
| `tools/contractgen/parser/webhook.go` | +250 | 解析 webhook kind contract 与 contractUsages role |
| `tools/contractgen/parser/webhook_test.go` | +200 | 解析正常 / 缺字段 / 跨 role 校验 |
| `tools/cellgen/template/webhook_register.tmpl` | +60 | 生成 `reg.RegisterWebhookReceiver` / `reg.RegisterWebhookDispatch` 调用骨架 |
| `tools/cellgen/cellgen.go` | +200 | 集成 webhook template 进 cell_gen.go 生成流程 |
| `tools/cellgen/cellgen_test.go` | +180 | 端到端：slice.yaml → cell_gen.go fixture 比对 |
| `tools/cellgen/testdata/webhook-receive/` | +120 | fixture：slice.yaml + 期望生成的 cell_gen.go |
| `tools/cellgen/testdata/webhook-dispatch/` | +120 | fixture：dispatch 角色 |
| `tools/archtest/webhook_yaml_invariants_test.go` | +200 | CONTRACT-YAML-WEBHOOK-FIELDS-FROZEN / WEBHOOK-MARKER-RETIRED |
| **合计** | **~1560** | |

**TDD**：JSON Schema 单元 → parser test → cellgen template fixture → archtest

**依赖**：无（与 PR-1 平行可启动，但建议 PR-1 先合以稳定 errcode/sentinel 供 generated code 引用）

**对标参考**：
- `ref: kubernetes/kubernetes scheme/cellgen 模式`（K8s codegen typed builder）
- `ref: go-zero codegen tools/goctl`

### PR-3: Receiver runtime + 集成 + ADR signing-algorithm（估 1700-1900 行）

**分支名**：`feat/047-3-webhook-receiver-runtime`

**改动文件**：

| 文件 | 估行 | 说明 |
|------|------|------|
| `runtime/webhook/doc.go` | +40 | 包文档 |
| `runtime/webhook/receiver.go` | +280 | HTTP handler：body 限流 → header 提取 → verifier → claimer 两阶段 → business handler 调用 |
| `runtime/webhook/middleware.go` | +180 | WebhookVerify middleware（与 Recovery / BodyLimit / Metrics chain）|
| `runtime/webhook/registry.go` | +160 | `RegisterWebhookReceiver(spec, handler)` 与 `cell.Registrar` 接缝 |
| `runtime/webhook/receiver_test.go` | +350 | unit：handler 路径分支 |
| `runtime/webhook/receiver_integration_test.go` | +500 | `httptest` integration：有效 HMAC → 200 / 无效 → 401 / 重复 delivery_id → 200 idempotent / body 过大 → 413 / 缺 header → 401 / replay > 5min → 401（build tag `integration`） |
| `kernel/webhook/webhooktest/conformance.go` | +180 | Verifier/Signer 跨实现一致性（参考 `outboxtest.Features`）|
| `docs/architecture/202605262300-adr-webhook-signing-algorithm.md` | +250 | ADR：HMAC-SHA256 唯一合法算法 / 拒 SHA-1 降级 / secret rotation 双 secret 过渡 |
| **合计** | **~1940** | |

**TDD**：conformance 测试 → unit test → integration test → ADR

**依赖**：PR-1（kernel/webhook 核心）+ PR-2（cell.Registrar 接缝点 spec 结构）

**对标参考**：
- `ref: ThreeDotsLabs/watermill-http pkg/http/subscriber.go@master`
- `ref: stripe/stripe-go webhook/client.go@master ConstructEvent`

### PR-4: SSRF guard + ADR ssrf-policy（估 1500-1700 行）

**分支名**：`feat/047-4-webhook-ssrf-guard`

**改动文件**：

| 文件 | 估行 | 说明 |
|------|------|------|
| `kernel/webhook/ssrf.go` | +350 | SafeDialContext：自定义 `net.Dialer.DialContext`，dial 前 + dial 后 IP CIDR 检查；redirect deny；scheme allowlist；WithAllowLoopback opt-in |
| `kernel/webhook/ssrf_test.go` | +500 | table-driven：所有黑名单 CIDR（IPv4 + IPv6 完整列表）+ DNS rebinding（fakeDNSResolver）+ redirect 拒绝 + scheme 校验 + opt-in loopback |
| `fixtures/webhook-ssrf-deny.yaml` | +80 | CIDR 黑名单 source of truth（IPv4 + IPv6 + APIPA + 文档段）|
| `kernel/webhook/ssrf_fixtures_test.go` | +100 | 读取 fixture 验证 ssrf.go 内部 CIDR 列表与 fixture 一致（防漂移）|
| `tools/archtest/webhook_ssrf_guard_test.go` | +320 | WEBHOOK-SSRF-GUARD-01：Hard 下游（`Dispatcher.client` 字段 type 检查）+ Medium 上游（ban `http.DefaultClient` / `net.Dial` / `net.DialTCP` 在 webhook 包 + 反向盲区 B1/B5）|
| `docs/architecture/202605262330-adr-webhook-ssrf-policy.md` | +300 | ADR：CIDR 黑名单 source of truth / DNS rebinding 防御 / redirect 策略 / WithAllowLoopback 仅 dev / 与业界（Convoy / safeurl / Smokescreen）对照 |
| **合计** | **~1650** | |

**TDD**：ssrf_test → fixture cross-check → archtest → ADR

**依赖**：PR-1（webhook 包结构）

**对标参考**：
- `ref: doyensec/safeurl ssrf-protection@main`
- `ref: getconvoy/convoy docs/webhook-guides/tackling-ssrf@main`

### PR-5: Dispatcher + cellgen 派生 + ADR retry-default（估 1800-2000 行）

**分支名**：`feat/047-5-webhook-dispatcher`

**改动文件**：

| 文件 | 估行 | 说明 |
|------|------|------|
| `kernel/webhook/dispatcher.go` | +280 | Dispatcher struct + outbox.EntryHandler impl + signing 注入 + SSRF client 必填 |
| `kernel/webhook/retry.go` | +120 | RetrySchedule type + DefaultSvixSchedule（5s / 5min / 30min / 2h / 5h / 10h / 10h / 10h）+ HandleResult 映射（2xx Ack / 4xx Reject / 5xx Requeue / SSRF Reject） |
| `kernel/webhook/dispatcher_test.go` | +400 | table-driven：retry schedule / 2xx / 4xx / 5xx / SSRF Reject / signing header inject / Body 反向 mask |
| `kernel/webhook/retry_test.go` | +120 | 8 步 schedule 验证 / clockmock |
| `runtime/webhook/dispatch/consumer.go` | +180 | dispatcher outbox consumer 包装（接入 ConsumerBase / Settlement）|
| `runtime/webhook/dispatch/registry.go` | +120 | `RegisterWebhookDispatch(spec, targetSelector)` 与 `cell.Registrar` 接缝 |
| `runtime/webhook/dispatch/consumer_integration_test.go` | +350 | `httptest` fake target server：outbox entry → POST 投递 / 2xx Ack / 5xx requeue / SSRF deny / signature verify by fake server（互验签名/验签 round-trip）|
| `tools/archtest/webhook_signer_funnel_test.go` | +250 | WEBHOOK-SIGNER-FUNNEL-01 + WEBHOOK-SIGNED-STRING-FORM-01：Medium 双向锁 + golden Svix vector + 反向盲区 B3 |
| `docs/architecture/202605270000-adr-webhook-retry-default.md` | +180 | ADR：Svix 8 步固定 schedule / retry budget / DLX 路由 / delivery timeout 30s |
| **合计** | **~2000** | 边界值；如超直接把 `dispatcher_test.go` 表 case 抽出 `webhooktest` 包 |

**TDD**：retry_test → dispatcher_test → consumer integration → archtest → ADR

**依赖**：PR-1（核心）+ PR-2（cellgen 派生 RegisterWebhookDispatch）+ PR-4（SSRF guard，dispatcher 必经）

**对标参考**：
- `ref: svix/svix-webhooks server/retry@main`
- `ref: getconvoy/convoy worker/retry`

### PR-6: Observability + healthz + backlog closeout（估 1000-1300 行）

**分支名**：`feat/047-6-webhook-observability-closeout`

**改动文件**：

| 文件 | 估行 | 说明 |
|------|------|------|
| `kernel/healthz/webhook_probes.go` | +120 | typed const ProbeName：`webhook_receiver_ready`、`webhook_dispatcher_ready`（注：`webhook_outbox_relay_ready` 复用现有 outbox probe，不重复）|
| `runtime/webhook/probes.go` | +80 | `cell.RegisterWebhookHealthProbes(reg, receiver/dispatcher)` 共享 funnel |
| `runtime/webhook/metrics.go` | +180 | OTel metrics：`webhook_deliveries_total{result,source}`、`webhook_signature_failures_total{source,reason}`、`webhook_delivery_duration_seconds`、`webhook_idempotency_hits_total` |
| `runtime/webhook/metrics_test.go` | +200 | metric 注入验证、label cardinality 上限 |
| `tools/archtest/webhook_idempotency_claimer_test.go` | +180 | WEBHOOK-IDEMPOTENCY-CLAIMER-01：Medium，receiver struct 必有 `claimer` 字段且打 `gocell:"required"` tag + B4 反向盲区（无 reflect Claim 调用） |
| `cells/auditcore/.../audit_payload.go` | +30 | （若适用）扩 audit 出口对 webhook event 二次 redaction（复用 `pkg/redaction.RedactPayload`） |
| `docs/backlog/20260520/cap-x-cross.md` | -1 / +1 | `KERNEL-WEBHOOK-01` 标记 ✅ |
| `docs/backlog.md` | +20 | follow-up issue 登记入口（Ed25519 / Vault Transit / Source persistence / Circuit breaker 全状态机）|
| `docs/plans/202605262200-047-kernel-webhook-implementation-plan.md` | +30 | 本文件改名追加 closeout 后缀（如 `-closeout.md`）记录完成 |
| examples 接入 demo（optional，建议 `examples/ssobff` 加一个 webhook receiver slice 演示 cellgen 派生）| +200 | slice.yaml + handler + integration 验证 |
| **合计** | **~1240** | |

**TDD**：archtest → metrics_test → cell 接入 demo

**依赖**：PR-3 + PR-5 全部合入

**关闭判定**：
- backlog `KERNEL-WEBHOOK-01` 移至 ✅
- 本计划文档移至 `docs/plans/archive/` 加 `-closeout` 后缀
- 4 个 follow-up issue 全部开出并贴 `backlog` + `pri-pX` label

## 4. 并行批次与冲突分析

```
       ┌───────┐
       │ PR-1  │ (foundation; signer/verifier/errcode/redaction)
       └───┬───┘
           │
     ┌─────┴─────┐
     │           │
   ┌─▼──┐      ┌─▼──┐
   │PR-2│      │PR-4│ (PR-2 cellgen / PR-4 SSRF 互不依赖)
   └─┬──┘      └─┬──┘
     │           │
     └────┬──────┘
          │
       ┌──▼──┐
       │PR-3 │ (Receiver runtime;依赖 PR-1 + PR-2 spec 结构)
       └──┬──┘
          │
       ┌──▼──┐
       │PR-5 │ (Dispatcher;依赖 PR-1 + PR-2 + PR-4)
       └──┬──┘
          │
       ┌──▼──┐
       │PR-6 │ (Observability + closeout)
       └─────┘
```

**并行机会**：
- PR-2 与 PR-4 可并行启动（无文件交叉）
- PR-3 可与 PR-4 部分并行（Receiver 不依赖 SSRF，SSRF 不依赖 Receiver；但完整集成测试需 PR-4 已合）

**同文件冲突点**：
- 无（每个 PR 文件清单不交叉）

**Skill 命中**：所有 PR 都不触 `.claude/skills/**` 或 `.claude/agents/**`，无 `skill-test-discipline.md` 触发。

## 5. 风险与缓解

| 风险 | 影响 | 缓解 |
|------|------|------|
| PR-1 行数边界（~2100）超 2000 | CI / review 负担 | 把部分 verifier table case 抽到 PR-3 的 `webhooktest` 包 |
| PR-5 行数边界（~2000）正好 | 同上 | 把 dispatcher 表 case 抽到 `webhooktest` 包，留缓冲 |
| cellgen template 改动影响所有现有 cell | regression 风险 | `tools/cellgen/testdata/` 现有 fixture 必须先全绿；新增 webhook template 走独立 codepath，旧 cell 无 webhook role 不受影响 |
| SSRF CIDR 列表漂移（fixtures vs code） | 防御失效 | PR-4 内 `ssrf_fixtures_test.go` 双向比对，archtest 锁 CIDR 列表声明 |
| HMAC sealed interface 阻塞测试 fake | 测试 ergonomics | `webhooktest/conformance.go` 用 sanctioned fake；外部测试不需要 fake，conformance suite 是唯一入口 |
| Receiver 单 pod claimer 与多 pod claimer 不一致 | 幂等漏洞 | 完全沿用现有 `kernel/idempotency.Claimer`（InMem + Redis）已通过 conformance；webhook 仅做调用方 |

## 6. 文档同步清单（每 PR 必查）

- [ ] godoc 包文档（kernel/webhook/doc.go、runtime/webhook/doc.go）
- [ ] CLAUDE.md 不需要新增章节（webhook 是 kernel/ 内常规 capability）
- [ ] `.claude/rules/gocell/` 不需新增规则文件（observability / error-handling / eventbus / api-versioning / contract-fanout 均已覆盖 webhook 类似 invariant）；如 PR-2/PR-3 引入 cellgen 派生新 yaml 字段，扩 `.claude/rules/gocell/eventbus.md` 的 cellgen 派生段落即可（不写新文件）
- [ ] backlog `KERNEL-WEBHOOK-01` 状态更新（PR-1 in-flight / PR-6 ✅）
- [ ] 3 个新 ADR 在对应 PR（PR-3 / PR-4 / PR-5）落地
- [ ] follow-up issue 在 PR-6 开出并贴 label

## 7. Implementation Matrix（每 PR body 必填）

按 `.claude/rules/gocell/contract-fanout.md` 模板：

```
Contract: webhook contract kind + Signer / Verifier / Dispatcher interface
Change: <one-line per PR>
Implementations: [ ] hmacSigner [ ] hmacVerifier [ ] hmacDispatcher
Conformance test: kernel/webhook/webhooktest.RunSignerVerifierConformance
Repro: go test -tags=integration ./runtime/webhook/... -run TestWebhookIntegration
Dependent contracts (governance scan): <list>
Invariant inventory: N/A (greenfield, no DROP COLUMN)
```

## 8. CI / 本地验证命令清单

每个 PR 完工前必须本地跑通：

```bash
# Build
go -C worktrees/047-<N> build ./...
go -C worktrees/047-<N> build -tags=integration ./...

# Tests
go -C worktrees/047-<N> test ./... -count=1
go -C worktrees/047-<N> test -tags=integration ./runtime/webhook/... -count=1

# Coverage
go -C worktrees/047-<N> test ./kernel/webhook/... -cover -coverprofile=/tmp/cov.out
go tool cover -func=/tmp/cov.out | tail -1  # 需 ≥ 90%

# archtest (4 core invariants PR-time gate)
bash hack/verify-archtest-invariants.sh

# Full archtest (PR-1/PR-4/PR-5 推荐)
bash hack/verify-archtest.sh

# Lint
golangci-lint run ./...   # 必须 0 issues

# 全验证
make verify
```

## 9. References

### 行业对标
- Svix: https://docs.svix.com/receiving/verifying-payloads/how-manual
- Stripe webhook: https://github.com/stripe/stripe-go/blob/master/webhook/client.go
- Slack security: https://github.com/slack-go/slack/blob/master/security.go
- GitHub webhook: https://github.com/google/go-github/blob/master/github/messages.go
- Watermill HTTP: https://watermill.io/pubsubs/http/
- Convoy SSRF: https://www.getconvoy.io/docs/webhook-guides/tackling-ssrf
- safeurl: https://blog.doyensec.com/2022/12/13/safeurl.html

### GoCell 现有约束（必读）
- `.claude/rules/gocell/observability.md`（errcode 三层 / redaction / span funnel / healthz probe naming）
- `.claude/rules/gocell/error-handling.md`（errcode New + Panic Approved + Details slog.Attr）
- `.claude/rules/gocell/eventbus.md`（ConsumerBase / Settlement / Cell 订阅注册范式 — webhook 派生与此对齐）
- `.claude/rules/gocell/ai-robust.md`（Hard/Medium/Soft 评级 + 范本目录）
- `.claude/rules/gocell/contract-fanout.md`（implementation matrix 强制）
- `.claude/rules/gocell/api-versioning.md`（webhook contract v1 演化策略）

### 相关 ADR
- `docs/architecture/202604242030-adr-kernel-wrapper-contract-observability.md` §8（redaction）
- `docs/architecture/202605051730-adr-errcode-message-pii-safety.md`（message const literal）
- `docs/architecture/202605111000-adr-subscription-cellid-mandatory.md`（cellID HARD 位置必填，webhook 派生对齐）
- `docs/architecture/202604270030-architectural-panic-whitelist.md` §4.1（panic Approved）
- `docs/architecture/202605171200-adr-readyz-verbose-four-channel-redaction.md`（probe verbose 通道）

### Backlog 来源
- `docs/backlog/20260520/cap-x-cross.md` — `KERNEL-WEBHOOK-01`
- `docs/design/capability-map.md` — K39 / K40
- `docs/design/master-plan.md` §webhook

### 姊妹制品（spec-kit 视角）
- `specs/516-webhook-dual-capability/spec.md` — user-story 视角的 feature spec（US1 receiver MVP / US2 dispatcher / US3 observability）
- `specs/516-webhook-dual-capability/plan.md` — Constitution Check + user-story → PR 映射
- `specs/516-webhook-dual-capability/tasks.md` — 62 task 按 user story / PR 分组
- `specs/516-webhook-dual-capability/data-model.md` — 5 实体 + 2 状态机
- `specs/516-webhook-dual-capability/contracts/webhook-contract.md` — webhook contract kind schema 草图
- `specs/516-webhook-dual-capability/analyze-report.md` — 跨制品一致性分析（含 Medium/Low findings）

## Closeout（PR-6 落地，#1161）

6 个 PR 全部合入。PR-6（observability + healthz + closeout）相对原 §3 PR-6 计划有经两轮激进自审 + 开源对标的实质偏离，逐条记录如下。

### 交付内容

| 项 | 状态 | 载体 |
|----|------|------|
| 4 个 OTel metrics | ✅ | `kernel/webhook/metrics.go`：`webhook_deliveries_total{result,source}` / `webhook_delivery_duration_seconds{source}` / `webhook_signature_failures_total{source,reason}` / `webhook_idempotency_hits_total{source}`。收集器在 kernel（镜像 `kernel/reconcile`），`Dispatcher.Handle`（发端，status-aware）+ runtime `Receiver.ServeHTTP`（收端）记录；bootstrap `autoWireWebhookMetricsCollector` 经 `autoWireCachedCollector` funnel 单源接线 phase5+phase6 |
| `WEBHOOK-METRIC-LABEL-VALUES-FROZEN-01` archtest | ✅ Medium | 双 sealed 枚举（`webhookDeliveryResult` 5 值 + `SignatureFailureReason` 5 值）按 TYPE 冻结 + `recordDelivery` callsite guard；Hard 路径并入 metricschema golden #1416 |
| `WEBHOOK-IDEMPOTENCY-CLAIMER-01` archtest | ✅ Medium | type-keyed data-flow：`claimed.rcpt` 必须溯源自 `idempotency.Claimer.Claim`（object identity + method resolution，rename-proof）。**与计划假设的 `gocell:"required"` tag 形态不同**——receiver 用构造器 nil-guard；本规则锁 substance（claim 做真实幂等工作），与 PIPELINE-01 锁 ordering 正交 |
| backlog `KERNEL-WEBHOOK-01` | PR-6 交付完成；EPIC #831 关闭**待人工裁定** | 6 个 PR 全合入 + follow-up issues #1539–#1544 全开；但 follow-up #1544（webhook receiver 在 JWT-gated PrimaryListener 上功能阻断）是 capability 级未竟项，EPIC 是否就此关闭由维护者决定。`docs/backlog/20260520/cap-x-cross.md` 是冻结快照，不编辑 |

### 相对计划的偏离（激进自审 + 开源对标）

- **healthz probes 全删**（计划列为 in-scope）：`webhook_receiver_ready`/`webhook_dispatcher_ready` 在当前架构 vacuous/synonymous（依赖已被 `redis_ready`/`postgres_ready`/`rabbitmq_ready` 覆盖，source store 启动期 eager 校验且运行时不可变），违反 observability.md「禁止同义/vacuous probe」。carve 到 follow-up（store 变运行时可变时加 `webhook_source_store_ready`）。
- **result label status-aware 5 值**（非计划隐含的 disposition 折叠）：开源对标 Alertmanager（clientError/serverError 分）/ K8s admission（error_type）/ Convoy（Discarded）均保留 status 维度，折叠 4xx/5xx 丢 SLO 信号。值集 `{success, client_error, server_error, transport_error, blocked}`，时延 buckets 到 30s（standard-webhooks 超时量级）。
- **不做 auditcore webhook 二次 redaction**（计划「若适用」行）：auditcore 已对所有订阅事件 payload 在 auditquery 出口统一 `RedactPayload`，webhook 敏感 key 已在 PR-1 入 pattern——无需额外工作。
- **examples 真实 demo carve 到 follow-up**：webhook receiver `buildRouteGroup` 硬编 `cell.PrimaryListener`（JWT-gated）且未标 `Public`，故真实 assembly 中 webhook 路径被 JWT 拦截——可运行的真实 demo 需 webhook 专属 listener 或 Public 标记，属 PR-3 运行时范围而非 PR-6。cellgen→drain→RouteGroup→Router→signed-200 全链路已由 `TestPhase5DrainWebhookReceivers_EndToEnd`（真实链 + 合成 cell）+ cellgen golden fixtures 覆盖。

### Follow-up issues（已开）

1. #1539 Ed25519 / Vault Transit KMS 签名（pri-p3）
2. #1540 Source secret 持久化 configcore/vault（pri-p2）
3. #1541 Circuit-breaker 全状态机（pri-p3）
4. #1542 Per-contract Claim TTL 配置（pri-p2，`receiver.go` 固定 24h doneTTL < provider 重试窗口）
5. #1543 `webhook_source_store_ready` probe（pri-p3，store 变运行时可变时开）
6. **#1544 webhook receiver 在 JWT-gated PrimaryListener 上不可运行（功能阻断，非仅 demo）+ examples demo**（pri-p2）

> #1544 是 review（产品维度）揭示的真实功能缺陷：`buildRouteGroup` 硬编 `cell.PrimaryListener` 且不标 `Public`，真实 assembly 中 webhook POST（携 HMAC 而非 JWT）被 JWT middleware 401 拦截。属 PR-3 运行时范围（webhook listener / Public 标记），非 PR-6 observability。详见 `runtime/webhook/doc.go` §"Known gap"。
