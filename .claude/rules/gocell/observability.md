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
| **Details**（`[]slog.Attr`） | runtime 业务字段（ID、计数、枚举值等） | 下发 | strip | 记录 |
| **Internal**（`WithInternal`） | runtime 调试上下文（堆栈摘要、SQL 片段等） | 不下发 | 不下发 | 记录 |

框架 HTTP middleware 在序列化响应前检查状态码：5xx 时将 `details` 置空（不下发），`internal` 字段永不出现在 wire 层。开发者通过查 `slog` 结构化日志获取 Internal 内容，不走 trace span（防止 PII 泄漏到 trace backend）。详见 ADR `docs/architecture/202605051730-adr-errcode-message-pii-safety.md`。

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

ref: `pkg/redaction/redaction.go`；archtest `SPAN-RECORD-ERROR-REDACT-01`（sibling pattern）；ADR `docs/architecture/202604242030-adr-kernel-wrapper-contract-observability.md` §8.

## Readyz Probe 命名

- Adapter readiness probe 使用 stable snake_case，并以后缀 `_ready` 表示依赖可用性，例如 `rabbitmq_ready`、`vault_transit_ready`。
- 一个 adapter 只有单一外部依赖时，禁止同时暴露多个同义 ready probe；多角色 worker 可用 `component-role` 拆分不同失败域。
- probe 名是运维契约；改名必须同步 dashboard / alert / 文档。
- Adapter probe 名是 `kernel/healthz.ReadyProbeName` typed-string funnel：各 adapter 声明 typed const（如 `postgres.ProbeReady`），构造点（Checkers map key 走 `string(<const>)`、`adapterutil.HealthToCheckers` 首参收 typed）只能引用声明的 const，裸字面量在静态分析层不可表达。archtest `OPS-CONTRACT-STRING-FUNNEL-01` 双向锁（下游构造解析 + 上游声明站点锁）+ golden inventory；符号清单活在该 archtest 的 package godoc。框架 probe（`config_watcher` / `outbox_failopen_rate_<cell>`）走 `NewProbe` / `WithHealthChecker` 的裸 `string`，由 `READYZ-PROBE-NAMING-01` 守 hyphen。注册路径分两个 typed funnel：emitter probe（`outbox_failopen_rate_<cell>`）经 kernel `cell.RegisterEmitterHealthProbes(reg, emitter)` 共享 funnel（内部做 `healthz.ProbeSet` 断言 + typed-nil 守卫，删 per-cell 重复，不改 probe 名分类）；cell repo probe 由 cellgen `RegisterRepoReady` funnel。

### Cell 级别 Repo Readiness Probe

三个平台 Cell 各自注册一个 **cell-level repo probe**，失败域与 pool 级 `postgres_ready` 不同：

| Cell | Probe 名 | 实现来源 |
|------|---------|---------|
| configcore | `configcore_repo_ready` | `ConfigRepository.RepoReady` — 探测 `config_entries` + `feature_flags` 表 |
| accesscore | `accesscore_repo_ready` | `session.Store.RepoReady` — 探测 `sessions` 表 |
| auditcore | `auditcore_repo_ready` | `ledger.Store.RepoReady` — 复用 `Tail` 探测 `audit_entries` 表 |

**为何不与 `postgres_ready` 合并**：pool 级 `postgres_ready`（`adapters/postgres.*Pool` 注册，bare `Ping`）只覆盖连接活性；cell-level repo probe 执行各 cell 自己关系表上的代表性查询，能捕获 schema/migration 漂移、表级权限丢失、缺失表等 pool Ping 检测不到的失败模式——失败域不同，非同义重复，不在"禁止暴露多个同义 ready probe"范围内。

**注册方式约束**：cell-level repo readiness probe **必须**通过 cellgen 生成的 `<cellpkg>.RegisterRepoReady(reg, prober)` 有类型 funnel 注册（`healthz_gen.go` 生成产物）；禁止直接调用 `reg.Healthz()` 注册 repo probe，也禁止以匿名 duck-type 形式绕过（accesscore 曾因此产生一个永远不触发的死代码 probe，已在 PR-REPO-READYZ 修复）。enforcement：archtest `HEALTHZ-WRITE-01`（A2 caller allowlist 锁 Register callsites）+ `HEALTHZ-TYPED-REGISTER-01`（锁 cells/ 包内 `reg.Healthz()` 调用必须在 `healthz_gen.go` 内）；`kernel/cell/celltest.RunRepoReadinessConformance` 提供 real-failure-injection 合规测试（healthy → nil；PG 表删除 → non-nil；mem → skip）。

emitter health probe（`outbox_failopen_rate_<cell>`）的注册同理收口：cell 不再各自生成 `RegisterEmitterProbes`，而是统一调 kernel `cell.RegisterEmitterHealthProbes(reg, c.emitter)`（内含 `healthz.ProbeSet` 断言 + `validation.IsNilInterface` 守卫）。该 funnel 是 `HEALTHZ-WRITE-01/A2` allowlist 的唯一 kernel/ caller（A2 scan scope 含 kernel/）。AI-robust 评级：下游 Medium（`HEALTHZ-TYPED-REGISTER-01` 锁 cells/ 不直调 `reg.Healthz()`）+ 上游 Medium（A2 caller allowlist），共享既有 healthz funnel 的 Hard-upgrade 路径 `HEALTHZ-HOLDER-SEAL-01`（seal `Aggregator` interface）。

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
| b. Details | `errcode.Details` `[]slog.Attr` | 4xx ✓ / 5xx strip | ✓ | runtime 字段为低敏感 |
| c. Internal | `errcode.WithInternal` | ✗ | ✓ | server-only |
| **d. Ops-Diagnostics** | handler-side `slog.Warn` typed payload | ✗ | ✓ | **typed funnel + archtest** |

readyz 各字段归属：
- wire body `dependencies[*]` (200 verbose) — 类型 `verboseDependencyEntry{Status, DurationMs}`，字段集冻结（`HEALTH-VERBOSE-WIRE-SHAPE-FROZEN-01`）。**wire 上不携带 error 文本**——对齐 Kubernetes apiserver healthz.go:274-275 wire/klog 双 buffer 分离。
- slog `dependencies` — 用 `slog.Group("dependencies", slog.Any(name, entry)...)`，**不要**用 `slog.Any("dependencies", map)`（后者在 unexported 字段下被 JSON handler 输出成 `{}`，丢失诊断，PR #552 实测 bug）。`SlogDependencyEntry` 三字段全 unexported，唯一构造路径 `newRedactedErrorMsg → RedactString`、无 testing backdoor（上游 Hard），下游 `HEALTH-REDACTED-ERROR-MSG-FUNNEL-01` 锁 conversion callsite——funnel 细节见 archtest godoc + ADR。

详见 ADR `docs/architecture/202605171200-adr-readyz-verbose-four-channel-redaction.md`。

## Audit Payload Redaction

`auditcore` 通过 `runtime/audit/ledger.Store.Append` 落 hash chain；payload 是订阅事件的原始 JSON。从 `auditquery` HTTP 出口下发时，`cells/auditcore/slices/auditquery/handler.go` 强制走 `pkg/redaction.RedactPayload(payload []byte) []byte`：

- payload JSON 解析后，递归剔除敏感 key：`password / passwd / pwd / secret / token / api_key / authorization / private_key / signing_key`（与 `pkg/redaction.RedactError` 同源 key 列表）
- 不可解析为 JSON object 的 payload（数组 / 标量 / 不合法 JSON）整段替换为 `<REDACTED>` 字符串（fail-closed）
- 内部 store 落盘 `audit_entries.payload`（JSONB）保留原始数据用于合规审计；redaction 仅在出站 HTTP 路径生效

ref: `cells/auditcore/slices/auditquery/handler.go` 出口；`pkg/redaction/redaction.go` 单源治理。
