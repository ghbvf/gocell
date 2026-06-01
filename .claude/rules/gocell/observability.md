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
Every production assembly entry point seals the process-global slog default
**before any log emission**:

```go
slog.SetDefault(slog.New(logging.NewHandler(logging.Options{Format: logging.FormatJSON})))
```

The `contextHandler` provides three layers of protection on every `Handle` call:

1. **Message redaction**: `redaction.RedactString(r.Message)` — masks
   `key=value` / `Authorization: Bearer …` substrings in free-form messages.
2. **Attribute value redaction** (per attr in the `r.Attrs` callback):
   `redaction.RedactSlogAttr(a)` — two-layer scrubber:
   - **Key-aware**: if `IsSensitiveKey(a.Key)` → replace value with `<REDACTED>`.
   - **Free-form** (non-sensitive keys only): `RedactString` on string/any values.
3. **Bind-time redaction** in `WithAttrs`: each pre-bound attr is redacted at
   bind time to cover the `logger.With(...)` path before any `Handle` call.

Entry points (production assembly `run.go` / `main.go`):
- `cmd/corebundle/run.go::runCorebundle`
- `examples/iotdevice/run.go::runIotdevice`
- `examples/todoorder/run.go::runTodoorder`
- `examples/ssobff/main.go::main`

**Archtest**: `SLOG-HANDLER-SEALED-FUNNEL-01`
(`tools/archtest/slog_handler_sealed_funnel_test.go`):

| Assertion | Rating |
|-----------|--------|
| A1 — `slog.NewJSONHandler`/`NewTextHandler` banned outside logging pkg (go/types typed resolution; alias bypass ineffective) | **Hard downstream** |
| A2 — `contextHandler.Handle` and `WithAttrs` body form-lock (RedactSlogAttr + RedactString presence; dropping fails immediately) | **Hard downstream** |
| A3 — production entry points must each contain `slog.SetDefault(slog.New(logging.NewHandler(...)))` (caller-allowlist) | **Medium upstream** |

A3 is Medium (Go cannot enforce SetDefault timing relative to early init).
Hard-upgrade path: codegen injection of the seal as the first generated line in
each entry point. Tracked at **gh #1401**.

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

**为何不与 `postgres_ready` 合并**：pool 级 `postgres_ready`（`adapters/postgres.*Pool` 注册，bare `Ping`）只覆盖连接活性；cell-level repo probe 执行各 cell 自己关系表上的代表性查询，能捕获 schema/migration 漂移、表级权限丢失、缺失表等 pool Ping 检测不到的失败模式——失败域不同，非同义重复，不在"禁止暴露多个同义 ready probe"范围内。

**注册方式约束**：cell-level repo readiness probe **必须**通过 cellgen 生成的 `<cellpkg>.RegisterReadiness(reg, prober)` 有类型 funnel 注册（`healthz_gen.go` 生成产物）；`reg.RegisterReadiness` 是唯一写面（`Registrar.Healthz()` 已移除，调用是编译错误）；以匿名 duck-type 形式绕过同样不可编译（参数类型 `healthz.Probe` 必须经 `healthz.NewProbe(name ProbeName, ...)` 构造）。enforcement：archtest `PROBENAME-SEALED-FUNNEL-01`（A1/A2/A3 覆盖声明 + 构造 + write 入口）；`kernel/cell/celltest.RunRepoReadinessConformance` 提供 real-failure-injection 合规测试（healthy → nil；PG 表删除 → non-nil；mem → skip）。conformance 入列（每个 `healthz.RepoProber` 实现必须出现在 `RunRepoReadinessConformance` 调用点）由 archtest `CELL-REPO-READYZ-PROBE-01`（Medium，`tools/archtest/cell_repo_readyz_probe_test.go`）守卫——范围 cells/+adapters/+runtime/+examples/，kernel/ 因 CELLTEST-B（`CELLTEST-IMPORT-BOUNDARY-01`：kernel/ 禁 import `kernel/cell/celltest`）层级不变式排除。

emitter health probe（`outbox_failopen_rate_<cell>`）的注册同理收口：cell 统一调 kernel `cell.RegisterEmitterHealthProbes(reg, c.emitter)`（内含 `healthz.ProbeSet` 断言 + `validation.IsNilInterface` 守卫，内部调 `reg.RegisterReadiness(p.Name(), p)`，其中 `p.Name()` 返回 `healthz.EmitterFailOpenProbeName(cellID)` 派生的 `ProbeName`）。注：自 PR-A23 起 `c.emitter` 是 `outbox.CellEmitter`（embed `healthz.ProbeSet`），故 `ProbeSet` 断言对生产 cell 恒成立；非 DirectEmitter 内层时 `Probes()` 返回 nil，循环为空。该 funnel 是 `HEALTHZ-WRITE-01/A2` allowlist 的唯一 kernel/ caller（A2 scan scope 含 kernel/）。AI-robust 评级（两条正交轴，grading 以 `HEALTHZ-WRITE-01` godoc 为准，不在此复制）：**downstream** = `Registrar.Healthz()` 已删，type system Hard gate；`HEALTHZ-WRITE-01/A2` 为 Medium archtest caller-identity backstop，锁 `Aggregator.Register` 直调点；**upstream** 的唯一 Hard 形态是 type-system seal `Aggregator` interface，但不可行（holder 轴 Go 类型系统无法表达「谁能声明某类型的字段」，sealing 仅约束 implementer；且 4 处跨包实现 + `kernel/healthz↔kernel/outbox` import 环阻断单包内实现），故 `HEALTHZ-HOLDER-SEAL-01`（gh #893）won't-do——upstream 的 A3 holder allowlist 维持 Medium archtest 为 Go 下永久天花板。详见 `tools/archtest/healthz_invariants_test.go` 的 A3 godoc。

## HTTP Metrics `cell` Label

`http_requests_total` 与 `http_request_duration_seconds` 的 `cell` label 表示请求落入的 cell：

- **业务请求**：`cell=<cellID>` — 由 `runtime/http/router.Router.MountRouteGroup` 记录 `RouteGroup.CellID` 与 HTTP namespace 的 ownership，listener-root `CellAttribution` middleware 在 tracing/access-log/metrics/protection 之前写入 `kernel/ctxkeys.CellID`。成功 handler、auth/rate-limit/circuit-breaker/body-limit 前置拒绝、chi 405 都必须使用同一 cell。
- **框架/未匹配请求**：`cell="_runtime"` 哨兵 — `runtime/http/middleware.Metrics` 在 `ctxkeys.CellID` 缺失时使用 `RuntimeCellIDSentinel`，覆盖 `/healthz`、`/readyz`、`/metrics` 自身、listener 外 404 等所有未落入业务 RouteGroup 的请求。

`RouteGroup.Prefix` 非空时按 path segment 前缀归属；`Prefix == ""` 不表示拥有整个 listener，而是从该 RouteGroup 内实际注册的 `Route` / `Handle` / `Mount` / `auth.Mount` 合同路径派生归属。重叠 namespace 按最长前缀/最长模板胜出；同一 listener 内跨 cell 声明完全相同的 owner path/template 必须 fail-fast，不能靠注册顺序抢占。

`metrics` 中间件只读取 `ctxkeys.CellIDFrom`，缺失时使用 `_runtime`。route label 对前置拒绝使用 router 的 route-template fallback resolver，避免业务路径拒绝被记为 `route="unmatched"`。回退由 `tools/archtest/http_metrics_label_test.go` 守护：CTXSOURCE / ROUTER-ATTRIBUTION / NO-ASSEMBLY-DERIVE / NO-CONFIG-CELLID / RUNTIME-SENTINEL。

迁移 / 运维约束：

- 不做代码侧 backward-compat / double-write：禁止恢复 `ProviderCollectorConfig.CellID`、assembly/default 推导、旧 label 复制，或同时写新旧 HTTP 指标序列。
- `cell` 是粗粒度 owner 维度，不是 slice / contract / tenant / user 维度；业务 SLO / 告警默认过滤 `cell!="_runtime"`，运行时探针与未匹配流量排查使用 `cell="_runtime"`。
- 旧 dashboard / alert 需要过渡时，在 Prometheus 侧用 recording rule 聚合新序列；remote-write 或业务专用 Prometheus 需要降噪时，用 metric relabel drop `_runtime` 序列。示例见 `docs/ops/alerting-rules.md`。

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

## Audit Payload Redaction

`auditcore` 通过 `runtime/audit/ledger.Store.Append` 落 hash chain；payload 是订阅事件的原始 JSON。从 `auditquery` HTTP 出口下发时，`cells/auditcore/slices/auditquery/handler.go` 强制走 `pkg/redaction.RedactPayload(payload []byte) []byte`：

- payload JSON 解析后，递归剔除敏感 key：`password / passwd / pwd / secret / token / api_key / authorization / private_key / signing_key`（与 `pkg/redaction.RedactError` 同源 key 列表）
- 不可解析为 JSON object 的 payload（数组 / 标量 / 不合法 JSON）整段替换为 `<REDACTED>` 字符串（fail-closed）
- 内部 store 落盘 `audit_entries.payload`（JSONB）保留原始数据用于合规审计；redaction 仅在出站 HTTP 路径生效

ref: `cells/auditcore/slices/auditquery/handler.go` 出口；`pkg/redaction/redaction.go` 单源治理。
