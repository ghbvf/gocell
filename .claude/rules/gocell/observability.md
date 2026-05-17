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

## Readyz Probe 命名

- Adapter readiness probe 使用 stable snake_case，并以后缀 `_ready` 表示依赖可用性，例如 `rabbitmq_ready`、`vault_transit_ready`。
- 一个 adapter 只有单一外部依赖时，禁止同时暴露多个同义 ready probe；多角色 worker 可用 `component-role` 拆分不同失败域。
- probe 名是运维契约；改名必须同步 dashboard / alert / 文档，并用 archtest 或单测锁定。

### Cell 级别 Repo Readiness Probe

三个平台 Cell 各自注册一个 **cell-level repo probe**，失败域与 pool 级 `postgres_ready` 不同：

| Cell | Probe 名 | 实现来源 |
|------|---------|---------|
| configcore | `config_repo_ready` | `ConfigRepository.RepoReady` — 探测 `config_entries` + `feature_flags` 表 |
| accesscore | `session_store_ready` | `session.Store.RepoReady` — 探测 `sessions` 表 |
| auditcore | `audit_ledger_ready` | `ledger.Store.RepoReady` — 复用 `Tail` 探测 `audit_entries` 表 |

**为何不与 `postgres_ready` 合并**：pool 级 `postgres_ready`（`adapters/postgres.*Pool` 注册，bare `Ping`）只覆盖连接活性；cell-level repo probe 执行各 cell 自己关系表上的代表性查询，能捕获 schema/migration 漂移、表级权限丢失、缺失表等 pool Ping 检测不到的失败模式——失败域不同，非同义重复，不在"禁止暴露多个同义 ready probe"范围内。

**注册方式约束**：cell-level repo readiness probe **必须**通过 `cell.RegisterRepoReadiness(reg, name, prober)` 有类型 funnel 注册；禁止使用 `reg.Health(...)` 直接注册 repo probe，也禁止以匿名 `interface{ Health(context.Context) error }` duck-type 形式绕过（accesscore 曾因此产生一个永远不触发的死代码 probe，已在 PR-REPO-READYZ 修复）。enforcement：archtest `CELL-REPO-READYZ-PROBE-01` 锁 funnel 形态；`kernel/cell/celltest.RunRepoReadinessConformance` 提供 real-failure-injection 合规测试（healthy → nil；PG 表删除 → non-nil；mem → skip）。

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
- slog `dependencies` — 类型 `map[string]SlogDependencyEntry`（已导出，供 `_test.go` 断言用），其中 `ErrorMsg` 字段类型 = `redactedErrorMsg`（包私有 newtype）。唯一构造路径是 `newRedactedErrorMsg(err) → pkg/redaction.RedactString(err.Error())`，由 `HEALTH-REDACTED-ERROR-MSG-FUNNEL-01` archtest 锁定（包内 conversion CallExpr 必在 funnel 函数体内 + 反向 fixture 锁字面量字段赋值；类型 unexported 关闭上游包外构造可能）。

Handler 直接 `slog.Warn` 的 ops-diagnostics 通道（d）见下方 §"Readyz Verbose 四通道"。

详见 ADR `docs/architecture/202605171200-adr-readyz-verbose-four-channel-redaction.md`。

## Audit Payload Redaction（auditcore S7）

`auditcore` 通过 `runtime/audit/ledger.Store.Append` 落 hash chain；payload 是订阅事件的原始 JSON。从 `auditquery` HTTP 出口下发时，`cells/auditcore/slices/auditquery/handler.go` 强制走 `pkg/redaction.RedactPayload(payload []byte) []byte`：

- payload JSON 解析后，递归剔除敏感 key：`password / passwd / pwd / secret / token / api_key / authorization / private_key / signing_key`（与 `pkg/redaction.RedactError` 同源 key 列表）
- 不可解析为 JSON object 的 payload（数组 / 标量 / 不合法 JSON）整段替换为 `<REDACTED>` 字符串（fail-closed）
- 内部 store 落盘 `audit_entries.payload`（JSONB）保留原始数据用于合规审计；redaction 仅在出站 HTTP 路径生效

ref: `cells/auditcore/slices/auditquery/handler.go` 出口；`pkg/redaction/redaction.go` 单源治理。
