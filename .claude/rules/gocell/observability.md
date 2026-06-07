# 可观测性规范

## slog 日志级别

| Level | 使用场景 | 要求 |
|-------|---------|------|
| Error | 影响正确性：DB 写入失败、ACK 失败、状态机违规、安全事件 | 必须含完整 error + 关联业务字段 |
| Warn | 降级运行：Redis 不可用、noop publisher、重试预算耗尽 | — |
| Info | 生命周期：服务启动、consumer group 加入、migration 完成 | — |
| Debug | 开发诊断：payload dump、逐条命令 trace | 生产环境关闭 |

## 安全约束

- 禁止 Debug 级别 dump 完整请求/响应 body（生产中泄漏敏感信息）
- 错误日志必须包含结构化关联字段（`execution_id`、`policy_id` 等），禁止裸 `slog.Error("failed")`

## errcode 三层 redaction 分工

`errcode.Error` 将运行时信息分三层隔离，各层在 HTTP 响应与服务端日志中的可见性不同：

| 层 | 存放内容 | 4xx 响应 | 5xx 响应 | 服务端日志 |
|----|---------|---------|---------|-----------|
| **Message**（const literal） | 程序员写死的描述性文本，无 runtime 数据 | 下发 | 下发 | 记录 |
| **Details**（`[]errcode.PublicDetail`，sealed） | runtime 业务字段（ID、计数、枚举值等） | 下发 | strip | 记录 |
| **Internal**（`[]errcode.InternalDetail`，sealed） | runtime 调试上下文（堆栈摘要、SQL 片段等） | 不下发 | 不下发 | 记录 |

构造路径唯一：Public 通道走 typed scalar 构造器 `errcode.PublicString` / `PublicInt[T]` / `PublicBool` / `PublicDuration` / `PublicTime`（`pkg/errcode/details.go`，`PublicDetail.value` 字段是 sealed `publicValue` marker interface — 包外类型既不能结构字面量构造也无法实现该 marker，wire-unsafe 类型 chan/func/NaN/Inf/map/struct/pointer 编译期不可表达）；Internal 通道走 `errcode.InternalAttr(k, v any)` —— value 仍是 `any`，因为整条通道仅服务端可见，wire-safety 不约束。框架 HTTP middleware 在序列化响应前检查状态码：5xx 时将 `details` 置空（不下发），`internal` 字段永不出现在 wire 层。开发者通过查 `slog` 结构化日志获取 Internal 内容（handler 用 `InternalDetail.AsSlogAttr()` 转 `slog.Attr` 输出），不走 trace span（防止 PII 泄漏到 trace backend）。详见 ADR `docs/architecture/202605051730-adr-errcode-message-pii-safety.md` + §Amendment 2026-05-27。

## Span Error Redaction（fail-closed by default）

`kernel/wrapper.WrapConsumer` 与 `runtime/http/middleware.Recovery` 把所有写入 `span.RecordError` 的 error 文本无条件经过 `pkg/redaction.RedactError`。**没有调用方 opt-out**（无 `WithConsumerErrorRedactor` / `WithErrorRedactor` / `bootstrap.WithErrorRedactor` 等 wiring）。dev/debug 需要原始 error 文本走 `slog` 结构化字段，trace span 仅用于运维关联。

默认 mask `key=value` / `key: value` 形式中的下列敏感 key（value 段替换为 `<REDACTED>`，保留原 key 与大小写）：

```
password | passwd | pwd | secret | token | api[_-]?key | authorization | bearer
private[_-]?key | signing[_-]?key | dsn | connection[_ ]?string
```

Value boundary fail-closed：每个 pattern 一直消耗到下一个空白（authorization 到换行）。`,` 与 `;` **不**作 value 边界——secret 可能含这些字符（ODBC `Pwd=a;b;c`、base64url JWT），停在 `,`/`;` 会泄漏后续字节。代价：同一行 `password="abc",user="alice"` 中 `user="alice"` 一并被 mask；co-located 字段通常本身是 PII 或可从 `slog` 结构化字段副本恢复，over-mask 是 fail-closed 的接受代价。

`runtime/outbox.SanitizeError`（last_error 列存储）也走同一份 regex（`pkg/redaction`），确保单源治理。

ref: hashicorp/vault `audit log_raw=false` 默认；golang/go `net/url.URL.Redacted()` 硬编替换。ADR `docs/architecture/202604242030-adr-kernel-wrapper-contract-observability.md` §8。

## Span Attribute Redaction（fail-closed by default）

`adapters/otel/span.go` 的 `attrToKeyValue` 把 `wrapper.Attr.Value` 转成 OTel `attribute.KeyValue` 之前，所有 string-valued 分支无条件经过 `safeStringAttr` 两层 scrubber。**没有调用方 opt-out**——与 [Span Error Redaction](#span-error-redaction-fail-closed-by-default) 共享 `pkg/redaction` 「默认硬编 fail-closed，无调用方 opt-out wiring」哲学。

`safeStringAttr(key, raw)` 两层 fail-closed：

1. **Key-aware**（结构化）：若 `pkg/redaction.IsSensitiveKey(key)` 命中（与 `pkg/redaction.RedactPayload` 同源 sensitive key set），value 无条件替换为 `redaction.Mask`，不再走自由文本扫描。覆盖结构化泄漏 `wrapper.Attr{Key: "password", Value: "hunter2"}`——value 是裸字符串，无 `password=` 锚点，仅靠 `RedactString` 永远不会匹配。
2. **Free-form**（自由文本，仅非敏感 key 走）：`RedactString` mask `key=value` / `Authorization: Bearer …` 形态敏感子串 → `TruncateString` 截到 2048 runes。

| 分支 | 出口 helper | 处理 |
|------|------------|------|
| `case string` | `safeStringAttr(key, raw)` | 两层 scrubber（见上） |
| `case []byte` | `safeBytesAttr(key, b)` | SHA256 + length 元信息（保留 ops 调试可观察性，binary 上 `RedactString` 无意义）|
| `default`（任意类型 fmt.Sprint）| `safeStringAttr(key, fmt.Sprint(v))` | 同 `case string` |
| `case int / int64 / float64 / bool` | OTel SDK 原生 typed constructor | 非字符串标量，结构上无法承载 `key=value` 敏感串，原样直通 |

**Order 在 free-form 层是 correctness invariant**：`RedactString` MUST 先于 `TruncateString`。如果反过来，sensitive 值落在 cap 之外时 mask anchor 被切掉，tail 会未 mask 出站。key-aware 层无 ordering 顾虑（Mask 是 10 字符常量，远低于 cap）。

**双向闭环 funnel**（archtest `SPAN-SETATTR-REDACT-01`）：

| 方向 | 形态 | 评级 |
|------|------|------|
| 上游 (package-external) | `oteltrace.Span` 字段 package-private (`otelSpan.inner` lowercase)；包外无法构造持有者，Go 编译器即 gate | **Hard** |
| 上游 (package-internal) | archtest A1 锁包内任何 struct 持有 `oteltrace.Span` 字段必须是 `otelSpan`；包内新增 struct 由 archtest 在 CI 捕获，但 Go 类型系统无法编译期拒绝 | **Medium** |
| 下游 (callsite + form uniqueness) | archtest A2 锁 `attribute.String` 与 `attribute.Key(_).String(_)` chain 形态在 span.go 内 callsite ⊆ `{safeStringAttr.Body, safeBytesAttr.Body}`；A3a 锁 `safeStringAttr` 的 key-branch IfStmt 形态（必须先 `IsSensitiveKey(key)` 判定再 `return Mask`）；A3b 锁 free-form return 表达式 AST；A4 锁 `safeBytesAttr` return 表达式 AST；参数身份绑定到 FuncDecl 形参列表（不只 `*ast.Ident` 任意名） | **Hard** |

上游 package-internal 升级路径：通过引入 unexported interface 封装 `oteltrace.Span` 所用方法 + 私有构造函数，使包内新 struct 在 type system 上无法绕过 funnel。升级追踪：gh issue #851 SPAN-SETATTR-HOLDER-SEAL-01（见 `tools/archtest/span_setattr_redact_test.go` 包文档）。

形态参照 `.claude/rules/gocell/ai-robust.md` 「Hard 范本目录」: single sanctioned holder + typed marker funnel；Funnel 双向锁评级：Medium 上游（package-internal）+ Hard 下游 → 已开 gh issue 跟踪（见上 #851）。

**Metric label 不在 redact 范围**：`adapters/otel/metric_provider.go` / `messaging_channel_collector.go` / `pool_resource.go` 的 `attribute.String` callsite 架构上有界：

1. label value 在 GoCell 是 **registration-time enumerated set**（cell / route / status_code 等枚举），而非用户输入；`kernel/observability/metrics.MustValidateLabels` 再拒分隔符 `=` / `|` 作为额外防御。即便 fail-open，正确性来自 metric label 的约定 vs span attribute 用户来源的语义差异。
2. cardinality 上限 2000（`defaultAttrCacheMaxSize`），超出走 `otel.metric.overflow=true` overflow bucket。
3. metric label 是聚合键；统一 mask 为 `<REDACTED>` 会塌缩整个 series，破坏可观测性。
4. 由约定承载枚举值（cell / route / status_code），非用户输入。

Note: `wrapper.Span` does not currently expose `AddEvent` / `Link`; if added in the future they must route string attributes through `safeStringAttr` and extend `SPAN-SETATTR-REDACT-01` A2 callsite-coverage accordingly.

ref: `pkg/redaction/redaction.go`；archtest `SPAN-SETATTR-REDACT-01` / `SPAN-RECORD-ERROR-SEAL-01`；ADR `docs/architecture/202604242030-adr-kernel-wrapper-contract-observability.md` §8 + Amendment 2026-05-31.

## slog Sink Redaction（fail-closed, sink-side）

All slog output is redacted fail-closed at the **sink level** by
`runtime/observability/logging.contextHandler` (a `slog.Handler` implementation).
Every production entry point seals the process-global slog default
**before any log emission**:

```go
slog.SetDefault(slog.New(logging.NewHandler(logging.Options{Format: logging.FormatJSON})))
```

For **generated assemblies** the seal is injected by the `gocell generate
assembly` template (`kernel/assembly/gentpl/main.go.tpl`) as the first statement
of the generated `run()` — no hand-maintained list, covers every current and
future assembly automatically. For the **hand-written** entry points
(`examples/ssobff`, `examples/corebundlestarter`, and `cmd/gocell`) the seal is
hand-written. Long-running services seal with `FormatJSON` (above); the
`cmd/gocell` governance/codegen CLI seals with `FormatText` (human-readable
output) — both route through the same redacting `contextHandler`.

The `contextHandler` provides three layers of protection on every `Handle` call:

1. **Message redaction**: `redaction.RedactString(r.Message)` — masks
   `key=value` / `Authorization: Bearer …` substrings in free-form messages.
2. **Attribute value redaction** (per attr in the `r.Attrs` callback):
   `redaction.RedactSlogAttr(a)` — two-layer scrubber:
   - **Key-aware**: if `IsSensitiveKey(a.Key)` → replace value with `<REDACTED>`.
   - **Free-form** (non-sensitive keys only): `RedactString` on string/any values.
3. **Bind-time redaction** in `WithAttrs`: each pre-bound attr is redacted at
   bind time to cover the `logger.With(...)` path before any `Handle` call.

Entry points:
- **Generated** (seal in generated `run()`, via assembly template): `cmd/corebundle`,
  `examples/iotdevice`, `examples/todoorder`, `examples/orderfulfillment`, + any
  future `gocell generate assembly` output.
- **Hand-written** (seal in `main()`): `examples/ssobff`, `examples/corebundlestarter`,
  `cmd/gocell` (CLI — `FormatText`).

**Archtest**: `SLOG-HANDLER-SEALED-FUNNEL-01`
(`tools/archtest/slog_handler_sealed_funnel_test.go`):

| Assertion | Rating |
|-----------|--------|
| A1 — `slog.NewJSONHandler`/`NewTextHandler` banned outside logging pkg (go/types typed resolution; alias bypass ineffective) | **Hard downstream** |
| A2 — `contextHandler.Handle` and `WithAttrs` body form-lock (RedactSlogAttr + RedactString presence; dropping fails immediately) | **Hard downstream** |
| A3 generated segment — seal injected by assembly template into generated `run()`; marker-derived self-covering scan + regenerate byte-diff lock | **Hard** (#1401 delivered) |
| A3 handwritten segment — bounded `slogHandwrittenEntryPoints` allowlist (ssobff, corebundlestarter) must seal in `main()` | **Medium** (won't-do gh #1424) |

A3 generated segment is Hard: the seal lives in the codegen template, so adding a
new assembly never requires touching the archtest — the `Code generated by gocell
generate assembly` marker drives coverage automatically. The handwritten segment
stays Medium (Go cannot force a hand-written `main()` to `SetDefault` first); A1's
Hard bare-handler ban is the backstop. Tracked as a deliberate won't-do at
**gh #1424** (analogous to #851 / #893 holder-seal ceilings).

**Retired Soft archtests** (superseded by sink-side redaction):
- `PANIC-REDACT-01` — `slog.Any("panic", X)` must wrap X with `redaction.RedactAny`.
  Sink's `RedactSlogAttr` KindAny branch redacts error / `fmt.Stringer` values
  (GoCell panics are `panicregister.Approved(errcode.Assertion)`, i.e. errors);
  genuinely structured values pass through to preserve structured logs (Option A,
  #1036 review F2).
- `HTTPUTIL-5XX-LOG-REDACT-01` — `log5xx` must call `RedactSlogAttr` on attrs.
  Sink now redacts all attrs unconditionally.

Call-site redaction calls (`redaction.RedactAny`, `redaction.RedactSlogAttr`) are
**preserved** as defense-in-depth for contexts where the process-global seal may
not be active (unit-test / library code).

**Orthogonal rule not retired**: `REPO-LOG-KEY-ID-REDACT-01`
(`tools/archtest/repoerr_test.go`) — this is a **key-name prohibition** (forbids
`key_id`/`keyID` in attr key slots in `cells/`), not a value-redaction rule.
`IsSensitiveKey` does not contain `key_id`, so the sink does not mask `key_id`
values. The two rules are orthogonal.

ref: `runtime/observability/logging/logging.go`；archtest `SLOG-HANDLER-SEALED-FUNNEL-01`；ADR `docs/architecture/202604242030-adr-kernel-wrapper-contract-observability.md` §8 Amendment 2026-05-31.

## Readyz Probe 命名

- Adapter readiness probe 使用 stable snake_case，并以后缀 `_ready` 表示依赖可用性，例如 `rabbitmq_ready`、`vault_transit_ready`。
- 一个 adapter 只有单一外部依赖时，禁止同时暴露多个同义 ready probe；多角色 worker 可用 `component-role` 拆分不同失败域。
- 运行时操作 probe（outbox relay 等非依赖可用性、而是操作健康度）不带 `_ready` 后缀；当前 inventory：`outbox_relay_poll` / `outbox_relay_reclaim` / `outbox_relay_cleanup`（PR #1187 round-3 由旧 wire key `outbox-relay-*` rename 为 underscore 形态，因 `ProbeName` regex 禁连字符；外部 dashboard / alert / startup-validation script 若硬编码旧 hyphen key 需同步更新）。
- probe 名是运维契约；改名必须同步 dashboard / alert / 文档；ADR `docs/architecture/202605271100-adr-probename-sealed-funnel.md` F1 amendment §3 记录 outbox relay wire rename 明细。
- Adapter probe 名是 `kernel/healthz.ProbeName` typed-string funnel：各 adapter 声明 typed const（如 `postgres.ProbeReady`），构造点（`adapterutil.HealthToProbe` 首参收 typed、`reg.RegisterReadiness` 首参收 typed）只能引用声明的 const，裸字面量在 type system 层不可表达。archtest `PROBENAME-SEALED-FUNNEL-01` 双向锁（下游构造解析 + 上游声明站点锁）+ golden inventory；符号清单活在该 archtest 的 package godoc。框架 probe（`config_watcher` / `config_drift`）使用 `healthz.ConfigWatcherProbeName` / `healthz.ConfigDriftProbeName` typed const；emitter probe（`outbox_failopen_rate_<cell>`）使用 `healthz.EmitterFailOpenProbeName(cellID)` typed 构造器，全部接入同一 funnel，不存在 bare-string 逃生口。注册路径统一收口：emitter probe 经 kernel `cell.RegisterEmitterHealthProbes(reg, emitter)` 共享 funnel（内部做 `healthz.ProbeSet` 断言 + typed-nil 守卫，调 `reg.RegisterReadiness(p.Name(), p)`）；cell repo probe 由 cellgen `RegisterReadiness` funnel（`healthz_gen.go` 生成产物）。

### Cell 级别 Repo Readiness Probe

三个平台 Cell 各自注册一个 **cell-level repo probe**，失败域与 pool 级 `postgres_ready` 不同：

| Cell | Probe 名 | 实现来源 |
|------|---------|---------|
| configcore | `configcore_repo_ready` | `ConfigRepository.RepoReady` — 探测 `config_entries` + `feature_flags` 表 |
| accesscore | `accesscore_repo_ready` | `session.Store.RepoReady` — 探测 `sessions` 表 |
| auditcore | `auditcore_repo_ready` | `ledger.Store.RepoReady` — 复用 `Tail` 探测 `audit_entries` 表 |

**adapter-level：serving-role capability probe（#1676）**：`postgres_app_role_restricted_ready`
是 adapter 级别 probe（由 `adapters/postgres` 注册，非 cell-level），失败域与上述所有 probe
均不同：它查询 `SELECT rolsuper, rolbypassrls FROM pg_roles WHERE rolname = current_user`，
当 serving role 是 superuser 或带 BYPASSRLS 时 fail（→ /readyz 503）。这是 `FORCE ROW LEVEL SECURITY`
运行时实际生效的前提——`schema_guard.verifyRLS` 只验 policy shape（在 schema 上定义正确），
该 probe 验 serving-role capability（在运行时 bypass 与否）。两者正交，合并会混淆两类不同失败域。
ProbeName typed const = `postgres.ProbeAppRoleRestrictedReady`（`adapters/postgres/pool.go`），
纳入 `PROBENAME-SEALED-FUNNEL-01` golden inventory。仅当 `Config.RequireRestrictedRole`
为 true 时注册（serving pool；corebundle 置 true，admin/migration pool 如 `tools/pg-migrate` 不置）。
ref: ADR `docs/architecture/202606071200-1676-adr-restricted-app-serving-pool.md`。

**为何不与 `postgres_ready` 合并**：pool 级 `postgres_ready`（`adapters/postgres.*Pool` 注册，bare `Ping`）只覆盖连接活性；cell-level repo probe 执行各 cell 自己关系表上的代表性查询，能捕获 schema/migration 漂移、表级权限丢失、缺失表等 pool Ping 检测不到的失败模式——失败域不同，非同义重复，不在"禁止暴露多个同义 ready probe"范围内。

**注册方式约束**：cell-level repo readiness probe **必须**通过 cellgen 生成的 `<cellpkg>.RegisterReadiness(reg, prober)` 有类型 funnel 注册（`healthz_gen.go` 生成产物）；`reg.RegisterReadiness` 是唯一写面（`Registrar.Healthz()` 已移除，调用是编译错误）；以匿名 duck-type 形式绕过同样不可编译（参数类型 `healthz.Probe` 必须经 `healthz.NewProbe(name ProbeName, ...)` 构造）。enforcement：archtest `PROBENAME-SEALED-FUNNEL-01`（A1/A2/A3 覆盖声明 + 构造 + write 入口）；`kernel/cell/celltest.RunRepoReadinessConformance` 提供 real-failure-injection 合规测试（healthy → nil；PG 表删除 → non-nil；mem → skip）。conformance 入列（每个 `healthz.RepoProber` 实现必须出现在 `RunRepoReadinessConformance` 调用点）由 archtest `CELL-REPO-READYZ-PROBE-01`（Medium，`tools/archtest/cell_repo_readyz_probe_test.go`）守卫——范围 cells/+adapters/+runtime/+examples/，kernel/ 因 CELLTEST-B（`CELLTEST-IMPORT-BOUNDARY-01`：kernel/ 禁 import `kernel/cell/celltest`）层级不变式排除。

emitter health probe（`outbox_failopen_rate_<cell>`）的注册同理收口：cell 统一调 kernel `cell.RegisterEmitterHealthProbes(reg, c.emitter)`（内含 `healthz.ProbeSet` 断言 + `validation.IsNilInterface` 守卫，内部调 `reg.RegisterReadiness(p.Name(), p)`，其中 `p.Name()` 返回 `healthz.EmitterFailOpenProbeName(cellID)` 派生的 `ProbeName`）。注：自 PR-A23 起 `c.emitter` 是 `outbox.CellEmitter`（embed `healthz.ProbeSet`），故 `ProbeSet` 断言对生产 cell 恒成立；非 DirectEmitter 内层时 `Probes()` 返回 nil，循环为空。该 funnel 是 `HEALTHZ-WRITE-01/A2` allowlist 的唯一 kernel/ caller（A2 scan scope 含 kernel/）。AI-robust 评级（两条正交轴，grading 以 `HEALTHZ-WRITE-01` godoc 为准，不在此复制）：**downstream** = `Registrar.Healthz()` 已删，type system Hard gate；`HEALTHZ-WRITE-01/A2` 为 Medium archtest caller-identity backstop，锁 `Aggregator.Register` 直调点；**upstream** 的唯一 Hard 形态是 type-system seal `Aggregator` interface，但不可行（holder 轴 Go 类型系统无法表达「谁能声明某类型的字段」，sealing 仅约束 implementer；且 4 处跨包实现 + `kernel/healthz↔kernel/outbox` import 环阻断单包内实现），故 `HEALTHZ-HOLDER-SEAL-01`（gh #893）won't-do——upstream 的 A3 holder allowlist 维持 Medium archtest 为 Go 下永久天花板。详见 `tools/archtest/healthz_invariants_test.go` 的 A3 godoc。

## HTTP Metrics `cell` Label

`http_requests_total` 与 `http_request_duration_seconds` 的 `cell` label 表示请求落入的 cell：

- **业务请求**：`cell=<cellID>` — 由 `runtime/http/router.Router.MountRouteGroup` 记录 `RouteGroup.CellID` 与 HTTP namespace 的 ownership，listener-root `CellAttribution` middleware 在 tracing/access-log/metrics/protection 之前写入 `kernel/ctxkeys.CellID`。成功 handler、auth/rate-limit/circuit-breaker/body-limit 前置拒绝、chi 405 都必须使用同一 cell。
- **框架/未匹配请求**：`cell="_runtime"` 哨兵 — cell label 经 sealed `metrics.ResolveCellLabel(ctx, validCellIDs)` 漏斗解析，`ctxkeys.CellID` **缺失或不在 assembly closed set** 时降级为 `metrics.RuntimeCellSentinel`，覆盖 `/healthz`、`/readyz`、`/metrics` 自身、listener 外 404 等所有未落入业务 RouteGroup 的请求。

HTTP/gRPC **request-metrics** `cell` label 的 `_runtime` 哨兵单源 = `runtime/observability/metrics.RuntimeCellSentinel`（HTTP middleware 与 gRPC metrics interceptor 共读同一常量，metrics 维度仅此一处声明）。注意这不是「全仓唯一 `"_runtime"` 字面量」：`_runtime` 作为「框架 / 无 owner」字符串约定在仓内更广泛复用——`kernel/reconcile.reconcilerIDSentinel`（reconcile owner label）、Redis `KeyNamespace` 的 idempotency-claimer 哨兵（`cmd/corebundle`）等都各自声明同值字面量。它们**不能**共享 `RuntimeCellSentinel`：`kernel/` 不可 import `runtime/observability/metrics`（分层依赖规则），故按层独立声明是刻意为之，`RuntimeCellSentinel` 只能是 metrics 维度的单源。

`RouteGroup.Prefix` 非空时按 path segment 前缀归属；`Prefix == ""` 不表示拥有整个 listener，而是从该 RouteGroup 内实际注册的 `Route` / `Handle` / `Mount` / `auth.Mount` 合同路径派生归属。重叠 namespace 按最长前缀/最长模板胜出；同一 listener 内跨 cell 声明完全相同的 owner path/template 必须 fail-fast，不能靠注册顺序抢占。

`metrics` 中间件经 sealed `metrics.ResolveCellLabel(ctx, validCellIDs)` 漏斗解析 cell label（内部读 `ctxkeys.CellIDFrom` + 校验 closed set，详见下方 M12b），缺失/越界时返回零值 `CellLabel{}`（`String()` 渲染为 `_runtime`）。route label 对前置拒绝使用 router 的 route-template fallback resolver，避免业务路径拒绝被记为 `route="unmatched"`。漏斗路由由 `tools/archtest/http_metrics_label_test.go` 守护：CTXSOURCE / BODYLIMIT-CTXSOURCE（断言两写入点过 `ResolveCellLabel`、不再内联 `ctxkeys.CellIDFrom`）/ ROUTER-ATTRIBUTION / NO-ASSEMBLY-DERIVE / NO-CONFIG-CELLID；漏斗体 + sentinel + 闭集成员校验由 `tools/archtest/cell_id_closed_set_test.go`（`CELL-ID-CLOSED-SET-01`，亦 subsume 旧 RUNTIME-SENTINEL-01）守护。

迁移 / 运维约束：

- 不做代码侧 backward-compat / double-write：禁止恢复 `ProviderCollectorConfig.CellID`、assembly/default 推导、旧 label 复制，或同时写新旧 HTTP 指标序列。
- `cell` 是粗粒度 owner 维度，不是 slice / contract / tenant / user 维度；业务 SLO / 告警默认过滤 `cell!="_runtime"`，运行时探针与未匹配流量排查使用 `cell="_runtime"`。
- 旧 dashboard / alert 需要过渡时，在 Prometheus 侧用 recording rule 聚合新序列；remote-write 或业务专用 Prometheus 需要降噪时，用 metric relabel drop `_runtime` 序列。示例见 `docs/ops/alerting-rules.md`。

### cellID closed set（M12a #1093）

业务 `cell` label 的合法取值集 = assembly.yaml 列举的 cell 集合。该 closed set 的 build 期真值源是 `composition.Builder.Build`（`runtime/composition/builder.go`），**两阶段**：(1) pre-Provide 对每个被装配 module 的 `ID()` 做双射校验（越界 / 缺失 / 重复 fail-fast）；(2) post-Provide 对每个 module 构造出的 cell 校验 `c.ID() == m.ID()`。`composition.New(assemblyCellIDs...)` 注入 assembly 声明的 cell-id 集（内部 `slices.Clone` 防 caller 篡改）。**越界 cellID 硬拒、不降级 `_runtime`**（外部 cell 注册不在 assembly 的 cellID 是配置 bug，混入 `_runtime` 会污染哨兵语义）。对标 K8s `runtime.Scheme` 注册期枚举 → 越界拒绝。第二阶段是必要的：runtime `cell` label 取自 `c.ID()`（cell metadata 派生），与 module 自报的 `m.ID()` 独立来源；只校验 `m.ID()` 会让 module 构造出越界身份的 cell 蒙混过双射（PR #1514 review F1）。

**覆盖范围**：该 funnel 覆盖**走 `composition.Builder` 的 compositionAPI assembly**——当前 `cmd/corebundle` + `examples/corebundlestarter`，`cmd/corebundle` 原手写 `assertModuleIDsMatch` 已删。**legacy-form 示例 assembly**（`examples/todoorder` / `iotdevice` / `orderfulfillment`，用 `generatedCellModules() []CellModule` 本地类型形态，不经 `composition.Builder`）仍保留各自的手写 `assertModuleIDsMatch`；它们迁移到 `composition.New` closed set 是后续工作（待这些示例采用 compositionAPI/cellmodules 形态时）。详见 ADR `docs/architecture/202606030230-adr-assembly-cross-module-composition.md` §D4。

**M12b（label 写入点 defense-in-depth，#1093 已交付）**：`http_requests_total{cell}` / `http_request_duration_seconds{cell}` / body-limit 拒绝计数器的 cell label 在**写入点**经 sealed `metrics.CellLabel` 类型 + 唯一构造器 `metrics.ResolveCellLabel(ctx, valid)` 校验 closed set——越界或缺失 cellID 降级 `RuntimeCellSentinel`，绝不污染平台 SLO 序列。closed set 由 bootstrap `buildListenerRouterOpts` 经 `router.WithCellIDClosedSet(s.asm.CellIDs())` 注入每个 listener router，再由 `buildMux` 传给 Metrics / BodyLimit 中间件（与 M12a 同源 `s.asm.CellIDs()`）。

**M12a build-reject vs M12b runtime-degrade（互补分层，非矛盾）**：M12a 在 **build 期**对越界 cellID **硬拒、不降级**（配置 bug 必须 serving 前 fail-fast）；M12b 在 **runtime metric 写入点**对漏过的越界 cellID **降级 `_runtime`**（活请求不该为一个 metric label panic / 污染序列）。「不降级」治 build 路径，「→ sentinel」治 runtime label 路径——两条规则正交。

**下游 type-system Hard**：`Collector.RecordRequest` / `RecordBodyLimitRejection` / `GRPCCollector.RecordRPC` 的 cell 参为 sealed `CellLabel`（unexported 字段，唯一导出构造器 = `ResolveCellLabel`），裸 string 当 label 编译不可表达（同 `outbox.Entry` sealed-construction 范式）。上游 Medium：`CELL-ID-CLOSED-SET-01` 锁漏斗体（必读 `ctxkeys.CellIDFrom` + `valid[v]` 成员校验 + miss 返回 `CellLabel{}`）+ 包内 `CellLabel{}` 字面量仅限 `ResolveCellLabel`（防包内后门）+ bootstrap 恒传 `s.asm.CellIDs()`。注意与 `HTTP-METRICS-LABEL-NO-ASSEMBLY-DERIVE-01` 正交：后者禁 assembly **名**（单数）当 label **值**；M12b 用 assembly **cell-id 集**（复数）当成员**过滤器**——value-source vs membership-filter，互不冲突。

## gRPC Metrics `cell` Label

`grpc_server_requests_total` / `grpc_server_request_duration_seconds` 复用与 HTTP 同一 `cell` label 语义与同一 `_runtime` 哨兵单源（`runtime/observability/metrics.RuntimeCellSentinel`）。gRPC cell attribution 已落地（#1383 / #1152）：拦截器链含 `interceptor.UnaryCellAttribution`，它按 `FullMethod → cellID`（生成式 registrar `runtimegrpc.ServiceRegistrar.CellIDForMethod` 派生）写入 `kernel/ctxkeys.CellID`，置于 `UnaryMetrics`/`UnaryAccessLog` 之前（链序 `RequestID→CellAttribution→Tracing→AccessLog→Metrics→Auth→Recovery`，由 `GRPC-INTERCEPTOR-CHAIN-ORDER-01` 守）。`UnaryMetrics` 经 sealed `metrics.ResolveCellLabel(ctx, validCellIDs)` 读取，`validCellIDs` = assembly closed set（composition root 传 `asm.CellIDs()`，对称于 HTTP `router.WithCellIDClosedSet`）；attributed cell ∈ closed set 时反映归属 cell，否则（未注册方法 / 越界 / 缺失）降级 `_runtime`。运维侧**可以**对 gRPC 指标用 `cell!="_runtime"` 过滤（详见 `docs/ops/alerting-rules.md`）。reader 侧契约由 archtest `GRPC-METRICS-LABEL-CELLID-CTXSOURCE-01` 守卫。`RecordRPC` 的 cell 参与 HTTP 同走 sealed `CellLabel`，裸 string label 编译不可表达。

**Registrar 共享（Option 3 #1152）**：cell attribution 的 resolver 与 adapter 的 grpc.Server delegation 是同一个 `ServiceRegistrar`——composition root 先 `runtimegrpc.NewServiceRegistrar()`，把 `reg` 同时传 `interceptor.Deps.Registrar`（chain 内派生 `CellIDForMethod`）与 `adaptersgrpc.Config.Registrar`（必填，`New` 内 `BindServer` 绑定 grpc.Server）；两侧对称 `Registrar: reg` 使误传可见。`Deps.Registrar` + `Deps.CellIDClosedSet` 均必填，`NewUnaryChain` 缺任一即 panic（fail-closed，不静默 `_runtime`）。compile-proof 同实例（单 builder 同出 chain+config）为 Hard 升级，backlog `#1752`。

**`grpc_ready` readyz probe（#1152）**：`adapters/grpc.Server.Probes()` 暴露 `ProbeReady`（typed const `grpc_ready`，在 `PROBENAME-SEALED-FUNNEL-01` golden inventory），bootstrap `expandGRPCServerProbes`（Run 起始，邻 `expandManagedResources`）收集进 `healthCheckers`，由 phase5 `drainProbes` 注册到 aggregator。多 gRPC listener 同名 probe 按 name 分组合并为单一 process-level checker（AND：任一 server not-serving 即 `grpc_ready` 不健康——无 dup-name 冲突、无 silent drop）。per-listener 粒度命名（区分哪个 listener 故障）需 typed ProbeName 构造器，backlog `#1748` 跟踪。

### gRPC streaming（PR-10 #1153）

streaming（server-stream + client-stream + bidi）由 `interceptor.NewStreamChain` 组装的**流拦截器链**承载，与 unary 链**全等价**（含 auth——否则 streaming RPC 无认证）：`RequestID→CellAttribution→Tracing→AccessLog→Metrics→Auth→Drain→Recovery`（多一个 stream-only `StreamDrain`，紧贴 Auth 内侧）。各 concern 的核心（request-id 派生 / cell 归属 / span 开闭 / access log / bearer-auth 决策 / panic 收敛）与 unary 共享单源；**唯一例外** = metrics cell-label funnel 按 `GRPC-METRICS-LABEL-CELLID-CTXSOURCE-01` 的逐函数 provenance binding 在 `StreamMetrics` 内联（不共享）。composition root 把 `NewUnaryChain` + `NewStreamChain` 同 `Deps` 一并装进 server options（`examples/iotdevice/run.go` 首个消费者，server-stream `WatchCommands`）。

**框架侧 drain（#1153）**：`runtime/grpc.DrainSignal`（composition root 构造一个，对称注入 `interceptor.Deps.Drain` + `adaptersgrpc.Config.Drain`，同 Registrar 的 Option-3 实例共享）让 GracefulStop **主动 cancel** 在途 stream 的 ctx（grpc-go 的 GracefulStop 只 wait 不 cancel handler ctx）：adapter `gracefulStop` 起始 `Trigger()` → `StreamDrain` 把每条 stream ctx 绑到信号 → handler `select` 到 `ctx.Done()` 即返回 → GracefulStop 在预算内完成。`Config.Drain` 与 `Config.Registrar` 同为 required（无 `if drain != nil` 双路径）。

**运维（drain 可观测）**：`gracefulStop` 在 Trigger 前打一条 `slog.Info("grpc: draining …", shutdown_timeout)` 作为关闭序列锚点；被 drain 取消的在途 stream 以 `code=Canceled` 收尾，故 `grpc_server_requests_total{code="Canceled"}` 在 drain/rolling-deploy 窗口内的脉冲是**预期现象**（非故障），告警应排除关闭窗口。

守卫（导航；完整盲区清单活在各 archtest godoc 单源）：

| Archtest ID | 摘要 | 评级 |
|---|---|---|
| `GRPC-STREAM-CHAIN-ORDER-01` | `NewStreamChain` 的 `grpc.ChainStreamInterceptor` 8-arg 顺序冻结（含 `StreamDrain`），go/types 解析每参到 interceptor 构造器 + 单站点/参数解析两 blind-spot | Medium（变参顺序 Go 不可编译期表达，同 unary `…-ORDER-01` 天花板） |
| `GRPC-CHAIN-STREAM-INTERCEPTOR-CALLER-01` | `grpc.ChainStreamInterceptor` 生产引用点收口 `runtime/grpc/interceptor/stream.go::NewStreamChain` + anti-vacuity | 下游 Medium（caller-allowlist）/ 上游 Go 天花板（第三方导出 func，won't-do #1394；单-builder Hard 升级 #1752） |
| `GRPC-STREAM-DRAIN-01` | 两侧框架-drain 守：(A) `StreamDrain` 入 `NewStreamChain` 参（消费侧）+ (B) `runtimegrpc.DrainSignal.Trigger` caller-allowlist 收口 `adapters/grpc/server.go`（生产侧）+ 双 anti-vacuity | 下游 Medium / 上游 Go 天花板（Trigger 是导出方法，#1394/#851/#1282 族；单-builder Hard 升级 #1752） |

`StreamMetrics` 已纳入 `GRPC-METRICS-LABEL-CELLID-CTXSOURCE-01`（扫 `UnaryMetrics` + `StreamMetrics` 两函数）；`StreamRecovery` 经共享 `recoverGRPCPanic` 自动受 `PANIC-LOG-REDACT-01` 覆盖。**codegen**：`contractgen.ReadProtoServiceInfo` 已接受 streaming RPC（#1153 退役 unary-only 拒绝，service-level 注册整个 proto service，stream 形态由运行时链处理）。

## Redis Key Namespace（owner 维度的 keyspace 等价物）

`adapters/redis` 四个 primitive（IdempotencyClaimer / Cache / NonceStore / RedisDriver）构造期注入 `KeyNamespace` 给 Redis key 加 owner 前缀。命名约定与 HTTP metrics `cell` label 同源：

- **per-cell 资源**：cell 直接构造（如 `NewCache(client, "accesscore")`）→ namespace 用 cell ID，Redis key 形如 `accesscore:<userKey>` 或 `accesscore:{<bizKey>}:lease`（hashtag 内只含业务 key，slot colocate 不受 prefix 影响）。
- **shared 框架基建**：`cmd/corebundle` 单实例 + 跨 cell 共享 → 两种命名约定：
  - IdempotencyClaimer 用 `_runtime` sentinel：事件 UUID 全局唯一已规避 cell 间碰撞，namespace 仅作类型合约 sentinel；与 HTTP metrics `_runtime` 标签语义一致。
  - NonceStore 用角色名 `servicetoken-nonce`：替换 PR-V1-REDIS-KEYNS 之前硬编码的 `servicetoken:nonce:` 内置前缀，namespace 直接表达 role，避免 `_runtime:servicetoken:nonce:<n>` 三层冗余前缀。

约束：
- `KeyNamespace.Validate()` 拒绝空、`:`、`{`、`}`、大写、长度 >48；首字符限定 `[a-z_]`。
- 4 个公开构造函数返回 `(*T, error)`，body 顶部强制 `if err := ns.Validate(); err != nil { return nil, err }`，由 archtest `REDIS-KEY-NAMESPACE-01` 静态守卫。
- 扩 Redis primitive 时同步进 `tools/archtest/redis_key_namespace_test.go` 的 `redisConstructors` 列表。

## Readyz Verbose 四通道（HEALTH-VERBOSE-WIRE-SHAPE-FROZEN-01 + HEALTH-REDACTED-ERROR-MSG-FUNNEL-01）

`/readyz?verbose` 路径的 dependency 信息分四通道，与上文 errcode 三层（Message / Details / Internal）并列：

| 通道 | 载体 | wire 体 | server-side slog | 脱敏机制 |
|------|------|--------|-----------------|---------|
| a. Message | `errcode.Message` const literal | ✓ | ✓ | 不需要 |
| b. Details | `errcode.Details` `[]PublicDetail` (sealed) | 4xx ✓ / 5xx strip | ✓ | runtime 字段为低敏感 |
| c. Internal | `errcode.InternalDetails` `[]InternalDetail` (sealed) | ✗ | ✓ | server-only |
| **d. Ops-Diagnostics** | handler-side `slog.Log(ctx, level, ...)` typed payload（透传 request ctx 关联字段） | ✗ | ✓ | **typed funnel + archtest** |

readyz 各字段归属：
- wire body `dependencies[*]` (200 verbose) — 类型 `verboseDependencyEntry{Status, DurationMs}`，字段集冻结（`HEALTH-VERBOSE-WIRE-SHAPE-FROZEN-01`）。**wire 上不携带 error 文本**——对齐 Kubernetes apiserver healthz.go:274-275 wire/klog 双 buffer 分离。
- slog `dependencies` — 用 `slog.Group("dependencies", slog.Any(name, entry)...)`，**不要**用 `slog.Any("dependencies", map)`（后者在 unexported 字段下被 JSON handler 输出成 `{}`，丢失诊断，PR #552 实测 bug）。`SlogDependencyEntry` 三字段全 unexported，唯一构造路径 `newRedactedErrorMsg → RedactString`、无 testing backdoor（上游 Hard），下游 `HEALTH-REDACTED-ERROR-MSG-FUNNEL-01` 锁 conversion callsite——funnel 细节见 archtest godoc + ADR。
- `logDiagnostics` 经 `slog.Log(ctx, level, ...)` 透传 request ctx，readyz record 带 `request_id` + `correlation_id`（RequestID middleware 注入，与 errcode `WithInternal` 同源；#942 R2）；`trace_id` 在 probe 端点**默认不出现**——`DefaultProbeFilter` 跳过 `/healthz /readyz /livez /metrics` 的 tracing span 创建。slog 是 wire 删 error 文本后的主诊断通道，secret 泄漏面真值以 ADR §3 威胁矩阵为准（裸 JWT/PEM/UUID 仍会进 slog——已知盲区，wire 兜底）。

详见 ADR `docs/architecture/202605171200-adr-readyz-verbose-four-channel-redaction.md`。

## Outbox Wire Envelope 三族字段（sealed construction）

`kernel/outbox.Entry` 跨 async 边界携带**三族正交身份**，命名空间物理隔离，禁止互串：

| 族 | 载体字段 | 来源 | 消费侧 | 出站脱敏 |
|----|---------|------|--------|---------|
| **Observability**（trace/request/correlation） | `Entry.observability ObservabilityMetadata` | `NewEntry` 从 ctx 注入 | `SubscriberWithMiddleware` 自动 `RestoreToContext` | span attr 走 `safeStringAttr` |
| **Principal**（actor/subject/tenant/session） | `Entry.principal PrincipalMetadata` | `NewEntry` 从 ctx 注入（生产桥 `runtime/auth` 认证后写 `ctxkeys`） | 同上自动还原 | `gocell.principal.session_id` 命中 `IsSensitiveKey` mask；actor/subject/tenant opaque 明文 |
| **业务 Metadata** | `Entry.metadata map[string]string` | producer 经 `WithMetadata` | handler 读 `entry.Metadata()`（clone） | `RedactPayload` 不覆盖（结构化 KV，非 payload） |

约束（**sealed construction**，issue #1229，权威记录见 ADR `202605281200-1042` §Amendment 2026-05-29 round-2）：

- `Entry` 全字段 unexported；获得 `Entry` 的三条路径都在 `kernel/outbox` 包内：`NewEntry(clk, ctx, …)`（producer 构造，ctx 注入身份）/ `UnmarshalEnvelope`（wire 解码）/ `EntryScan.ToEntry`（storage 重建）。包外 populated `outbox.Entry{…}` 字面量编译不可表达（**type-system Hard 上游**，`OUTBOX-ENTRY-SEALED-CONSTRUCTION-01`；其 SoleReconstructionSurface 子检查另锁「唯一产出 Entry 的导出 func = `NewEntry`+`UnmarshalEnvelope`，唯一重建 mirror = `EntryScan`」）。该 type-system Hard **只封闭「字面量伪造」一条向量**；reconstruction 与 ctx 注入两条 provenance 通道不在其内，由下面的 caller-allowlist funnel 封闭——不要把整体 sealing 笼统称为 type-system Hard。
- **provenance funnel 双向锁**（Principal/OccurredAt 可信补齐的实际 enforcement）：
  - **reconstruction**：`UnmarshalEnvelope` / `EntryScan.ToEntry` 的生产引用点（call 或 function-value）锁定在 storage adapter（`adapters/postgres/outbox_store.go`）+ wire/consumer 解码（`runtime/eventbus` / `adapters/rabbitmq`）+ store conformance helper——`OUTBOX-RECONSTRUCTION-CALLER-01`。
  - **ctx 注入**：principal ctxkeys setter（`WithActorID/SubjectID/TenantID/SessionID`）的生产引用点锁定在 auth 请求边界桥（`runtime/auth/middleware.go`，JWT+service-token 共用 `injectPrincipalCtxKeys`）+ consumer 还原（`kernel/outbox/principal.go::RestoreToContext`）——`CTXKEYS-PRINCIPAL-WRITE-CALLER-01`。business producer（cells/examples）无法触达任一通道。
  - **评级**：两条 funnel 均 **Hard 下游（archtest caller-allowlist，go/types 解析 alias / value-ref，形态唯一）+ Medium 上游（Go-language ceiling）**。Hard 上游不可达——reconstruction 与 ctx-write 本质跨包（kernel ↔ runtime ↔ adapters 互为不同包，且 kernel 不可 import runtime/auth），Go 包可见性无法表达「仅某几个包可调某导出符号」。同 `SPAN-SETATTR-HOLDER-SEAL`(#851) / `HEALTHZ-HOLDER-SEAL`(#893) 的永久天花板形态，gh issue **#1282** 跟踪（won't-do）。
  - observability ctxkeys setter（`WithTraceID` 等，~11 生产调用点）**不**纳入同等锁——伪造 trace id 无害，伪造 principal 是审计身份越权（P1）；安全语义差异下的非对称是刻意为之。
- Observability + Principal **由 `NewEntry` 从 ctx 注入**（无 producer-facing inject API，无 `WithPrincipal`/`WithObservability` option）；producer 亦不可经 `Metadata` 伪造身份键（`ReservedMetadataKeys` 11 key = 7 observability + 4 principal + `Entry.Validate` fail-fast；membership 由 `OUTBOX-RESERVED-METADATA-KEYS-FROZEN-01` 冻结。occurred_at 刻意不在 reserved——它不属于 observability/principal 任一桥族，是有专属 sealed 字段的 domain 时间标量，见 ADR 202605281200-1042 §5）。三条伪造面——字面量 / `Metadata` / (ctx-write + reconstruction)——由上述机制合并封闭。
- 消费侧还原由 `SubscriberWithMiddleware.SubscribeEntry`（`kernel/outbox/outbox.go`）的 outermost built-in step 完成（`entry.observability` + `entry.principal` → ctxkeys），先于业务 middleware；`kernel/wrapper.WrapSubscriber` 只写 delivery span attrs，不负责 ctx 还原。
- `OccurredAt`（producer 域事件时间）mandatory（`Validate` 拒零值），`NewEntry` stamp，`WithOccurredAt` 注入 domain 时间；与 `CreatedAt`（store/seal 时间）语义分层，两者作为独立 wire 字段端到端携带（`outbox_fullchain_test.go` 锁 round-trip 独立性）。auditquery 出口对二者用 `time.RFC3339Nano`（亚秒精度是 HMAC chain `*UnixNano` 的一部分，不可截断）。
- wire schema：Principal `omitempty`（additive），OccurredAt required；`PrincipalMetadata` 4 字段 `idutil.SafeID`，由 `SAFEID-WIREMESSAGE-USAGE-01` + `PRINCIPAL-SEALED-FIELD-FROZEN-01` 冻结。
- 读侧 size cap：PG scan 对 observability / principal / metadata 三个 JSONB 列对称施加 `maxObservabilityJSONBytes` / `maxPrincipalJSONBytes` / `maxMetadataJSONBytes` 上限（drop+warn），防 corrupted 或 maliciously-crafted row 无界分配——三列面对同一 DoS 向量。identity 两列经 `decodeOversizeGuardedJSONB[T]` 泛型 funnel（带 `Validate()`）；metadata 是无 `Validate()` 的 business KV map，cap 内联但语义对齐。各 cap = `4 ×` 对应 producer-side total（`kout.Max{Observability,Principal}TotalSize` / `metautil.MaxMetadataTotalSize`），4× 为 JSON 编码 overhead headroom。

audit `actor_id` 例外：源自事件 payload 的 domain actor（`appender.extractActor`），非 `entry.Principal().ActorID`——actor 是被审计动作的执行者（login 期 `session.created` 无 auth principal 时仍可用），Principal 族是正交的 request-context。详见 ADR §Amendment 2026-05-29 "actor 来源决议"。

## Reconcile Metrics `result` Label

`reconcile_total{result=...}` 的 `result` label 值集冻结为 `{success, transient, permanent, skipped}`（FR-010）。关键约束：

- **recovered panic → `transient`**（不引入第 5 个 `panic` label；panic 是可重试的瞬态失败）
- **`skipped` = trigger 合并到脏重跑，非丢弃**：`skipped` 表示 trigger 到达时实体正在处理中（processing=true），trigger 被 coalesced 进 dirty map；当前飞行 reconcile 完成后 **保证立即（delay=0）触发一次 re-run**（F5 dirty re-run）。收敛性不受影响——没有 trigger 被静默丢弃。（此语义由 PR-A5 取代 A3 的 skip-if-drop 实现；A3 阶段 skipped = 丢弃，A5 阶段 skipped = coalesced 待 re-run。）
- 值集唯一来源：`kernel/reconcile/metrics.go` 的 `result*` 常量，类型为 sealed `resultLabel`；`recovery.go::classify()` 是唯一分类函数。`recordResult` 形参类型为 `resultLabel`，且 archtest callsite guard 禁止任何内联常量实参（字符串字面量 / `resultLabel(...)` 转换），故能到达指标的只有声明的 4 个 const 或 `classify()` 产出的 typed 值。
- **单 requeue 路径**：所有向工作队列或延迟队列的 channel send 必须经由受认可函数之一（`drainReadyItems` / `(*Loop).feedFromSource` / `(*Loop).enqueueDelayed` / `tickerTrigger.Start` / `channelTrigger.Start`）；archtest **下降进 FuncLit** 并把 send 归属最近 enclosing FuncDecl，故 `go func(){ queue <- req }()` / `go func(){ addCh <- item }()` per-entity goroutine 在未授权函数内会被拦截（Trigger 的闭包 send 是合法外部 producer，已显式列入 allowlist）。
- **延迟队列按实体合并**：`waitingLoop` 持 `pending map[EntityID]*waitingItem`，同实体重复入队只保留更早 readyAt（`heap.Fix`），堆中每实体至多一项；dirty re-run（delay=0）因此 supersede 更晚的 interval/backoff 项而非留双项（对齐 client-go delaying_queue `waitingForMap`）。
- **permanent 取消旧 pending**：`resultPermanent` 分支经 `enqueueCancel` → `cancelCh` → `cancelPending` 从堆中删除该实体任何 pending 项（防止此前 success/transient 排下的长 requeue 到点又 reconcile 一个已 dead-letter 的实体）；同时 `process()` 对 permanent 抑制 dirty re-run（`wasDirty && label != resultPermanent`），避免 cancel 与 re-run 跨 `cancelCh`/`addCh` 双通道竞态。permanent 实体仅靠 fresh Source trigger 重新观察。`enqueueCancel` 是 `cancelCh` 的唯一受认可 send funnel（纳入 `RECONCILE-REQUEUE-ENQUEUE-CALLER-01` sanctioned set）。
- **backoff 失败计数有界**：`entityBackoff` 以 LRU（cap `maxBackoffEntries`）持有 per-entity 失败计数，满则淘汰最久未用项，封住高基数/不可信 `EntityID` 的内存放大面（`Request.EntityID` 有界集契约的 enforcement 补强）。

| Archtest ID | 摘要 | 评级 |
|---|---|---|
| `RECONCILE-RESULT-LABEL-VALUES-FROZEN-01` | `result` label 值集冻结（按 sealed `resultLabel` 类型枚举 vs. hardcoded golden）+ 下游 `recordResult` callsite guard（禁内联常量实参） | Medium（archtest；Hard 升级路径 = metricschema golden 字节锁，追踪 gh #1416） |
| `RECONCILE-REQUEUE-ENQUEUE-CALLER-01` | kernel/reconcile 内所有 `SendStmt`（含 FuncLit 闭包内）必须在受认可函数范围内（enclosing FuncDecl allowlist） | Medium（archtest；Hard 升级路径 = channel send-end 接口封装 + 构造 seal，追踪 gh #1418） |

完整盲区清单 + 反向自检活在各 archtest 的 package godoc；本节只做导航。

## HTTP Idempotency `state` Label

`idempotency_requests_total{cell,state}`（#1460）记录每次 HTTP 幂等决策的终态，使 replay 命中 / busy 409 / store error / oversize / key-reused 在 dashboard 可分、可告警（此前只在 slog 可见，重放被计入 `http_requests_total` 时与正常请求不可区分）。

- **`state` 值集冻结**为 `{acquired, replayed, busy, store_error, oversize, key_reused}`（单源 = `runtime/http/idempotency/metrics.go` 的 `RequestState` typed const）：
  - `acquired` = 新 claim 成功（ClaimAcquired，handler 执行），是已处理请求的分母。
  - `replayed` = 缓存响应回放（ClaimDone）。
  - `busy` = 在途 lease 冲突（ClaimBusy → 409 Retry-After）。
  - `store_error` = `Store.Claim` 非指纹错误（→ 500）。**仅指 Claim 路径失败**；Record/Release 失败（响应已下发、仅缓存写失败的罕见 store 故障）刻意留 slog.Error，不计 state。
  - `oversize` = 响应体超限不录（响应仍正常下发）。
  - `key_reused` = 同 key 不同 body 指纹不匹配（→ 422 ErrIdempotencyKeyReused，附 per-field diff 字段名），安全相关（客户端 bug / 重放）。
- **`acquired` ⊇ `oversize`**：同一 oversize 请求先计 `acquired`（claim 时）再计 `oversize`（录入时），刻意如此——`oversize/acquired` = 不可缓存率。其余 5 个 state 互斥、各计一次。
- **`cell` label** 复用 `http_requests_total{cell}` 同款语义：collector 从 `kernel/ctxkeys.CellID`（router root `CellAttribution` 注入）读取，缺失回退 `RuntimeCellSentinel`（`_runtime`）；业务取值天然 ∈ assembly closed-set（#1093 在 `composition.Builder` 已守），不需新 enforcement。注册经 bootstrap `autoWireIdempotencyMetricsCollector` → 共享 `autoWireCachedCollector` funnel（`BOOTSTRAP-AUTOWIRE-COLLECTOR-FUNNEL-01` 守），与 HTTP collector 同纪律（construct-once / cache / 冲突 startup-fatal）。

| Archtest ID | 摘要 | 评级（双向锁分轴） |
|---|---|---|
| `IDEMPOTENCY-REQUESTS-STATE-LABEL-VALUES-FROZEN-01` | A1 按 sealed `RequestState` 类型枚举 const 值集 vs golden（6 值）；A2 type-aware callsite + assignment guard 禁内联字面量 / `RequestState("x")` 转换 / 跨包 laundered const 达到 metric label，扫 producer + collector 两包 | **下游 Hard**（A2 type-aware，alias/转换/foreign-const 不可绕过、form 唯一）+ **上游 Medium**（A1 archtest 见证；Go 无法表达「只有这 6 个 RequestState 值」。Hard 路径 = enroll metricschema golden 字节锁，**共享 gh #1416**，届时 A1 退役） |

完整盲区清单（含 raw-map `Labels{"state":"x"}` bypass，与 saga `string(reason)` 同源已知盲区）+ 反向自检（RED/GREEN fixtures + negative control）活在 `tools/archtest/idempotency_metric_label_frozen_test.go` 的 package godoc；本节只做导航。

## MQTT Adapter Metric 面冻结（#1429）

`adapters/mqtt` 注册 11 个 `mqtt_*` metric（publish/consume/dlx/reconnect/ack 延迟/in-flight），全 `cell` label 构造期绑定。EPIC #1138 spec AC-8 字面列 6 名、实现细化为 11 名（含 `subscribe_inflight`→`mqtt_consume_inflight` gauge、`packet_too_large_total`→`mqtt_publish_failed_total{reason=payload_too_large}` reason-label）；完整对账 + 决策见 ADR-048 §Amendment 2026-06-07。三个 reason 闭集：`PublishFailureReason`(10) / `ConsumeFailureReason`(6) / `SubscribeFailureReason`(2)。

| Archtest ID | 摘要 | 评级 |
|---|---|---|
| `MQTT-METRIC-LABEL-VALUES-FROZEN-01` | (a) metric 名集 11 enumeration freeze（枚举 `metrics.{Counter,Histogram,Gauge}Opts` 的 `Name:` 字面量，覆盖 inline + 包级 var）；(b) reason 闭集 full funnel = 值集 freeze + Record* callsite guard + `XReason(...)` conversion ban + **reason-constant provenance backstop**（return/var/assign/arg 隐式转换）+ **receiver-identity sole-writer lock** + go/types-派生 instrument 字段 inventory 交叉校验 | 名集/值集 Medium（Hard-upgrade = metricschema golden，**共享 gh #1416**）；sole-writer outside-pkg **Hard**（unexported instrument 字段）+ within-pkg Medium（#851/#893 family）；callsite + provenance 下游 Medium（残留 = 非常量数据流，#1282 族天花板） |

完整盲区清单 + 反向自检（negative control + RED/GREEN fixtures）活在 `tools/archtest/mqtt_metric_label_values_frozen_test.go` 的 package godoc；本节只做导航。

## Audit Payload Redaction

`auditcore` 通过 `runtime/audit/ledger.Store.Append` 落 hash chain；payload 是订阅事件的原始 JSON。从 `auditquery` HTTP 出口下发时，`cells/auditcore/slices/auditquery/handler.go` 强制走 `pkg/redaction.RedactPayload(payload []byte) []byte`：

- payload JSON 解析后，递归剔除敏感 key：`password / passwd / pwd / secret / token / api_key / authorization / private_key / signing_key`（与 `pkg/redaction.RedactError` 同源 key 列表）
- 不可解析为 JSON object 的 payload（数组 / 标量 / 不合法 JSON）整段替换为 `<REDACTED>` 字符串（fail-closed）
- 内部 store 落盘 `audit_entries.payload`（JSONB）保留原始数据用于合规审计；redaction 仅在出站 HTTP 路径生效

ref: `cells/auditcore/slices/auditquery/handler.go` 出口；`pkg/redaction/redaction.go` 单源治理。

## Client-IP PII Hash Funnel（replayable payload，#1488）

replayable event payload 里的客户端 IP 一律以 keyed、非可逆 HMAC 哈希承载，明文永不进 outbox/broker/DLX 或 audit ledger。单源 primitive = `pkg/redaction.IPHash`（sealed struct，唯一构造器 `HashIP(salt, ip)` = HMAC-SHA256；`HashIPForLog` 已删）。composition root（`cellmodules/accesscore` / `examples/ssobff`）从 `cellsecrets` 载 salt（`GOCELL_<APP>_IP_HASH_SALT`，real 模式 demo-key fail-fast），在 bootstrap 观察者闭包内哈希——cell 收 sealed `IPHash`，永不见明文；slog 与 wire payload 共用同一哈希。

双侧 funnel（完整盲区清单 + 评级举证活在各 archtest package godoc，本节只导航）：

| ID | 侧 | 摘要 | 评级 |
|----|----|------|------|
| `CLIENT-IP-HASH-FUNNEL-01` | sink | go/types 冻结 `redaction.IPHash` 字段集 + 唯一构造器集 + `dto.BootstrapAuthFailedEvent.ClientIPHash` 字段类型 = `redaction.IPHash` | 上游 Hard（sealed unexported 字段）/ 下游 Hard（reflect/types 冻结） |
| `CTXKEYS-REALIP-READ-CALLER-01` | source | `ctxkeys.RealIPFrom` 生产引用点收口为 5-成员 allowlist（rate-limit ×2 / access-log / 2 observer） | 下游 Hard（go/types caller-allowlist）/ 上游 Medium（Go 可见性天花板，won't-do 同 #1282 族） |

范围 = IP 字段（今日唯一 replayable-payload PII）；generic「按字段名扫所有 payload」= Soft，宪章拒绝立项；generic Hard 机制（codegen 派生 typed PII 字段）追踪 gh #1605。设计真值源：ADR `docs/architecture/202606050558-1488-adr-replayable-payload-pii-hash-funnel.md`。

## Audit trace_id 反查（trace → audit）

`audit_entries` 带 `trace_id` 列（observability，**非** HMAC 链字段——`Protocol.ComputeHash` 12-field 输入冻结，`audit_hash_input_frozen_test.go` 守）。注入唯一路径 = `cells/auditcore/internal/appender`，经 `correlation.New(string(obs.TraceID), string(obs.RequestID), string(obs.CorrelationID))`（`obs = entry.Observability()`）从 W0 outbox observability envelope 同时派生 `trace_id` + `correlation_id`（`correlation.Correlation` 全字段 unexported 只防包外 struct literal 构造，但 `New` 是公开通用构造器，故 Correlation seal 不 gate provenance；值的可信 provenance 仅来自上游 sealed `outbox.Entry` 的 Hard 继承——下游 `AUDIT-TRACE-ID-WRITE-CALLER-01` 只锁**写入位置**（appender 为唯一写点，Medium caller-allowlist），**不校验 appender 内部值来源**，该最后一跳由单一审查注入点 + anti-vacuity 兜底）。

反查入口复用 auditquery：`GET /api/v1/audit/entries?traceId=<tid>`（admin 全局；非 admin 经既有 `auditQueryPolicy` AND `actor_id=self`，无后门），复用标准 `nextCursor`/`hasMore` 游标分页。**不新增端点**（端点收敛决策见 ADR）。

`AUDIT-TRACE-ID-WRITE-CALLER-01`（`tools/archtest/audit_trace_id_write_caller_test.go`）锁 `ledger.Entry.{TraceID, CorrelationID}` **写入位置** = appender(injection) + storetest(conformance)，即「谁可写该字段」，**不校验 appender 内部把哪个值写进字段**（值来源由上游 sealed envelope + 单一审查注入点保证）；PG `rows.Scan(&e.Field)` 重建为 scan-address 自然逃逸。评级 **Medium**（下游 archtest caller-allowlist，锁写入位置非值来源）+ 上游 Hard（仅继承 sealed `outbox.Entry`；`correlation.Correlation` 字段 seal 只防字面量构造、不 gate provenance，不计入上游 Hard）；`ledger.Entry` 导出字段（PG reflect/scan 必需）使下游 type-system Hard 不可达，同 #851/#893 永久天花板；全 seal Hard 化路径 won't-do-now 跟踪 #1501（godoc 点名）。设计真值源：ADR `docs/architecture/202606021400-1048-adr-observability-correlate-reverse-lookup.md`。
