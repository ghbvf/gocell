# F2 批次编目：HTTP / 可观测性 / Postgres+迁移 / 分布式锁 / Redis / Adapter 契约 / kernel 杂项

本批次覆盖运行时与适配器层约束，主题横跨 HTTP metrics cell 归因、span/health 脱敏漏斗、
Postgres 迁移安全、分布式锁正确性、Redis keyspace 治理、adapter 错误分类，以及
kernel 层杂项边界。

本文件 **47 条** | Hard 17 / Medium 23 / Soft 0 / 推断 7 | 通用 3 / 半通用 27 / 专属 17

---

### 批次 1：HTTP metrics 与 HTTP 契约可见性

| INVARIANT ID | 评级 | 机制类型 | 用途（≤30字中文） | 依赖 gocell 包 | 可移植性 |
|---|---|---|---|---|---|
| HTTP-METRICS-LABEL-CELLID-CTXSOURCE-01 | Medium（推断） | AST-pattern | 确保 HTTP metrics cell label 来自 ctxkeys 而非构造函数 | `runtime/http/middleware`, `kernel/ctxkeys` | 专属 |
| HTTP-METRICS-LABEL-NO-ASSEMBLY-DERIVE-01 | Medium（推断） | AST-pattern | 禁止从 assembly/config 中派生 cell label | `runtime/http/middleware` | 专属 |
| HTTP-METRICS-LABEL-NO-CONFIG-CELLID-01 | Medium（推断） | AST-pattern | 禁止在 ProviderCollectorConfig 中注入 CellID | `runtime/http/middleware` | 专属 |
| HTTP-METRICS-LABEL-RUNTIME-SENTINEL-01 | Medium（推断） | AST-pattern | 缺失 cell 上下文时必须使用 `_runtime` 哨兵 | `runtime/http/middleware` | 专属 |
| HTTP-METRICS-LABEL-ROUTER-ATTRIBUTION-01 | Medium（推断） | AST-pattern | cell label 归因必须在 router-root 请求上下文写入 | `runtime/http/router` | 专属 |
| HTTP-CONTRACT-VISIBILITY-TYPE-SEGREGATION-01 | Medium | conformance-test | 禁止单一 Go 类型同时实现公开和内部路径的 contract 接口 | `generated/contracts/http` | 专属 |

### 批次 2：HTTP 工具层与 5xx 单源

| INVARIANT ID | 评级 | 机制类型 | 用途（≤30字中文） | 依赖 gocell 包 | 可移植性 |
|---|---|---|---|---|---|
| HTTPUTIL-5XX-KIND-NORMALIZE-01 | Medium（推断） | AST-pattern | 5xx 路径必须使用 errcode.KindXxx 常量而非透传 ecErr.Kind | `pkg/httputil`, `pkg/errcode` | 半通用 |
| HTTPUTIL-5XX-LOG-REDACT-01 | Medium（推断） | AST-pattern | log5xx 必须经 redaction.RedactSlogAttr 处理 Details 后再写 slog | `pkg/httputil`, `pkg/redaction` | 半通用 |
| HTTPUTIL-SURFACE-REGISTERED-01 | Medium（推断） | callsite-allowlist | pkg/httputil 每个导出函数必须在 doc.go 或治理 map 中注册 | `pkg/httputil`, `kernel/governance` | 半通用 |
| WIRE-CODE-5XX-SINGLE-SOURCE-01 | Medium（推断） | AST-pattern | 5xx wire code 单源：PublicCode() 与 PublicCodeForStatus 必须同步扩展 | `pkg/errcode` | 半通用 |

### 批次 3：Metrics 漏斗与 Prometheus cell label

| INVARIANT ID | 评级 | 机制类型 | 用途（≤30字中文） | 依赖 gocell 包 | 可移植性 |
|---|---|---|---|---|---|
| OBS-01 | Medium（推断） | conformance-test | errcode classifier 函数需机器可读 ack 才能作 metric label | `tools/metricschema`, `pkg/errcode` | 专属 |
| METRICS-GAUGEVEC-FUNNEL-01 | Hard | typed-marker-funnel | 禁止在 promwrap/otelwrap 外直接调用 prom.New*/Meter 构造 | `adapters/prometheus/internal/promwrap`, `adapters/otel/internal/otelwrap` | 半通用 |
| METRICS-GAUGEVEC-UPSTREAM-HARD-01 | Hard | reflect-field-freeze | 冻结 promwrap/otelwrap 内 funnel 导出符号集 | `adapters/prometheus`, `adapters/otel` | 半通用 |
| METRICS-ADAPTERPROM-CALLER-ALLOWLIST-01 | Hard↓/Medium↑ | callsite-allowlist | 锁定 adapters/prometheus 公开 funnel 函数的调用方文件集 | `adapters/prometheus` | 半通用 |
| METRICS-CANCEL-CTX-CONFORMANCE-01 | Medium | conformance-test | adapters/ 下每个 metrics.Provider 实现必须入列取消上下文合规测试 | `adapters/otel`, `adapters/prometheus`, `kernel/observability/metrics` | 半通用 |
| PROM-CELL-LABEL-FUNNEL-01 | Hard | typed-marker-funnel | HookEvent.CellID 读取必须过 promCellLabel 漏斗避免高基数 label | `adapters/prometheus`, `kernel/cell` | 专属 |

### 批次 4：Span 脱敏漏斗

| INVARIANT ID | 评级 | 机制类型 | 用途（≤30字中文） | 依赖 gocell 包 | 可移植性 |
|---|---|---|---|---|---|
| SPAN-RECORD-ERROR-REDACT-01 | Hard | typed-marker-funnel | span.RecordError 参数必须包裹 redaction.RedactError | `kernel/wrapper`, `runtime/http/middleware`, `pkg/redaction` | 半通用 |
| SPAN-RECORD-ERROR-REDACT-ARCHTEST-01 | Medium（推断） | meta-archtest | 确保 span redact 扫描目录覆盖所有含 RecordError 调用的新目录 | `kernel/wrapper`, `runtime/http/middleware` | 半通用 |
| SPAN-SETATTR-REDACT-01 | Hard | typed-marker-funnel | adapters/otel 所有字符串 span attribute 必须过 safeStringAttr 两层脱敏 | `adapters/otel`, `pkg/redaction` | 半通用 |

### 批次 5：Health verbose 与 healthz 漏斗

| INVARIANT ID | 评级 | 机制类型 | 用途（≤30字中文） | 依赖 gocell 包 | 可移植性 |
|---|---|---|---|---|---|
| HEALTH-VERBOSE-WIRE-SHAPE-FROZEN-01 | Hard | reflect-field-freeze | 冻结 /readyz?verbose 响应体 dependency entry 字段集与 json tag | `runtime/http/health` | 专属 |
| HEALTH-REDACTED-ERROR-MSG-FUNNEL-01 | Hard | typed-marker-funnel | readyz slog error text 必须经 newRedactedErrorMsg→RedactString 漏斗 | `runtime/http/health`, `pkg/redaction` | 半通用 |
| HEALTHZ-WRITE-01 | Hard | callsite-allowlist | healthz/readyz HTTP handler 注册必须经 healthz.Aggregator 漏斗 | `runtime/http/health`, `runtime/observability/healthz`, `kernel/healthz` | 专属 |
| HEALTHZ-TYPED-REGISTER-01 | Hard | callsite-allowlist | cells/ 包内调用 reg.Healthz() 必须在 cellgen 生成的 healthz_gen.go 中 | `kernel/cell`, `cells/*` | 专属 |
| HEALTHCHECK-VERIFY-CLEANUP-AND-WAIT-TIMEOUT-01 | Medium | conformance-test | 以 bash 黑盒测试验证 docker cleanup 失败时 exit code 正确传播 | — | 半通用 |

### 批次 6：Readyz probe 命名与 ops-contract funnel

| INVARIANT ID | 评级 | 机制类型 | 用途（≤30字中文） | 依赖 gocell 包 | 可移植性 |
|---|---|---|---|---|---|
| READYZ-PROBE-NAMING-01 | Medium | string-typed-funnel | healthz.NewProbe 调用的 probe name 禁止含连字符 | `kernel/healthz` | 专属 |
| OPS-CONTRACT-STRING-FUNNEL-01 | Hard | string-typed-funnel | adapter 就绪探针名必须是 ReadyProbeName 类型常量且在许可包中声明 | `kernel/healthz`, `adapters/*` | 专属 |

### 批次 7：Postgres 迁移安全

| INVARIANT ID | 评级 | 机制类型 | 用途（≤30字中文） | 依赖 gocell 包 | 可移植性 |
|---|---|---|---|---|---|
| MIGRATION-NO-TRANSACTION-RERUN-SAFE-01 | Medium（推断） | metadata/yaml-derive | NO TRANSACTION 迁移的每条 DDL 必须幂等（IF NOT EXISTS 等） | `adapters/postgres/migrations` | 半通用 |
| MIGRATION-PAIR-DEPLOY-01 | Medium | metadata/yaml-derive | pair-deploy 指令引用的伴侣迁移文件必须存在 | `adapters/postgres/migrations` | 半通用 |
| MIGRATION-DESTRUCTIVE-DOWN-GUC-GUARD-01 | Medium | AST-pattern | 含破坏性 DDL 的 Down section 必须含 GUC fail-closed 保护 | `adapters/postgres/migrations` | 半通用 |
| SCHEMA-GUARD-COVERS-EVERY-OWNED-TABLE-01 | Medium | metadata/yaml-derive | migration 017+ 创建的每张表必须在 schema_guard expectedColumns 中注册 | `adapters/postgres` | 专属 |

### 批次 8：Postgres 运行时与 SQL builder

| INVARIANT ID | 评级 | 机制类型 | 用途（≤30字中文） | 依赖 gocell 包 | 可移植性 |
|---|---|---|---|---|---|
| PG-REPO-AMBIENT-TX-01 | Hard↓/Medium↑ | typed-marker-funnel | pgxpool.Pool 字段只能由 pgExecutor 持有；New* 构造必须过 newPGExecutor | `adapters/postgres` | 半通用 |
| PG-CONSTRUCTOR-MUST-FREE-01 | Medium（推断） | AST-pattern | adapters/postgres 中禁止声明 MustNew* 类名构造函数 | `adapters/postgres` | 半通用 |
| PG-TESTCONTAINER-FUNNEL-01 | Medium | callsite-allowlist | PG 集成测试必须通过 pgclone 而非自起 testcontainer 容器 | `tests/testutil/pgclone` | 半通用 |
| POSTGRES-NOTFOUND-TEST-OTHER-ERROR-MIXUP-ARCHTEST-01 | Hard | typed-marker-funnel | _NotFound 测试必须调 errcodetest.AssertCode 断言正确错误码 | `pkg/errcode/errcodetest` | 半通用 |
| GOOSE-SESSION-LOCKER-01 | Medium（推断） | callsite-allowlist | goose.NewProvider 必须配置 WithSessionLocker 防并发迁移竞争 | `adapters/postgres` | 半通用 |
| POSTGRES-MIGRATOR-LOCK-ORDER-REGRESSION-01 | Medium（推断） | callsite-allowlist | 同上，作为 GOOSE-SESSION-LOCKER-01 的回归标签 | `adapters/postgres` | 半通用 |
| SQLSTATE-SINGLE-SOURCE-01 | Medium | string-typed-funnel | pgconn.PgError.Code 对 pkg/pgquery 所拥有 SQLSTATE 的比较只能在 pkg/pgquery 中 | `pkg/pgquery` | 半通用 |

### 批次 9：CAS 协议、分布式锁与内存事务锁

| INVARIANT ID | 评级 | 机制类型 | 用途（≤30字中文） | 依赖 gocell 包 | 可移植性 |
|---|---|---|---|---|---|
| CAS-PROTOCOL-COMPOSITION-ROOT-01 | Medium | callsite-allowlist | cas.NewProtocol 只能在 cmd/* 或 runtime/state/cas/* 内构造 | `runtime/state/cas` | 专属 |
| DISTLOCK-LOCK-NOT-CONTEXT-01 | Hard↓/Medium↑ | type-system-seal | distlock.Lock 不能实现 context.Context 接口 | `runtime/distlock` | 通用 |
| MEM-TX-LOCK-OWNERSHIP-01 | Hard（核心）/Medium（层） | AST-pattern | txlock.Acquire 只能在 runLocked 函数体直接调用，inLiveTx 必须委托 Live | `cells/accesscore/internal/mem` | 专属 |

### 批次 10：Redis 不变式

| INVARIANT ID | 评级 | 机制类型 | 用途（≤30字中文） | 依赖 gocell 包 | 可移植性 |
|---|---|---|---|---|---|
| IDEMPOTENCY-LUA-HASHTAG-01 | Medium（推断） | AST-pattern | Redis 幂等 Lua EVAL 的 leaseKey/doneKey 必须经 applyHashtag 保证同 slot | `adapters/redis` | 半通用 |
| REDIS-KEY-NAMESPACE-01 | Medium（推断） | AST-pattern | adapters/redis 四个公开构造函数必须接受 KeyNamespace 并在顶部 Validate | `adapters/redis` | 半通用 |

### 批次 11：Adapter 错误契约

| INVARIANT ID | 评级 | 机制类型 | 用途（≤30字中文） | 依赖 gocell 包 | 可移植性 |
|---|---|---|---|---|---|
| ADAPTER-ERROR-CLASSIFICATION-TRANSIENT-01 | Hard | typed-marker-funnel | adapter transient 标记只能由 errcode.WrapInfra 写入；各 adapter 必须路由到该函数 | `pkg/errcode`, `adapters/postgres`, `adapters/redis`, `adapters/s3`, `adapters/rabbitmq` | 半通用 |
| ADAPTER-NET-TRANSIENT-FUNNEL-01 | Hard | typed-marker-funnel | net.Error 判定只能在许可的 (pkgPath, funcName) 中处理，其余必须委托 errcode.IsTransientNet | `pkg/errcode` | 半通用 |
| TRANSIENT-NET-HELPER-FORM-01 | Hard | AST-pattern | pkg/errcode.IsTransientNet 体必须含 errors.As(…, net.Error) 且不能 .Timeout() 收窄 | `pkg/errcode` | 半通用 |
| ADAPTER-RETURNS-DECLARED-TYPES-01 | Medium（推断） | metadata/yaml-derive | adapter HTTP 返回的 status code 必须在 contract.yaml 中声明 | `kernel/metadata`, `cells/*/slices/*/handler.go` | 专属 |

### 批次 12：kernel 杂项与 audit

| INVARIANT ID | 评级 | 机制类型 | 用途（≤30字中文） | 依赖 gocell 包 | 可移植性 |
|---|---|---|---|---|---|
| KERNEL-METADATA-NO-WIRE-01 | Medium（推断） | import-ban | kernel/metadata 禁止声明 wire 格式符号（Document/Entity 等） | `kernel/metadata` | 专属 |
| KERNEL-MUSTCTOR-PRODUCTION-DECL-01 | Medium | callsite-allowlist | 生产包禁止声明 Must* 名前缀函数（carve-out 列表除外） | `kernel/`, `runtime/`, `adapters/`, `cells/` | 通用 |
| KERNEL-POOLSTATS-LOCATION-01a | Medium（推断） | import-ban | 禁止导入已废弃的 runtime/observability/poolstats 路径 | `kernel/observability/poolstats` | 专属 |
| KERNEL-POOLSTATS-LOCATION-01b | Medium（推断） | import-ban | kernel/observability/poolstats 包必须保持 import-zero | `kernel/observability/poolstats` | 专属 |
| PKG-CTXKEYS-NO-CELL-MODEL-01 | Hard↓/Medium↑ | reflect-field-freeze | pkg/ctxkeys 禁止声明 cell/slice/journey/contract 相关标识符 | `pkg/ctxkeys`, `kernel/ctxkeys` | 专属 |
| CTXCANCEL-LOCAL-IMPL-BAN-01 | Medium（推断） | AST-pattern | cells/*/adapters 禁止本地重新实现 ctx-cancel 错误类型或预检逻辑 | `pkg/ctxcancel`, `cells/` | 半通用 |
| REPO-LOG-KEY-ID-REDACT-01 | Medium（推断） | AST-pattern | cells/ 禁止在 slog 属性 key 中出现密钥 ID 字面量 | `cells/` | 半通用 |
| AUDIT-LEDGER-PROTOCOL-COMPOSITION-ROOT-01 | Medium | callsite-allowlist | ledger.NewProtocol 只能在 cmd/* 或 runtime/audit/ledger 中构造 | `runtime/audit/ledger` | 专属 |
| AUDITCORE-APPENDER-SINGLE-SOURCE-01 | Medium | AST-pattern | auditappend* slice 包必须是 appender.Service 的薄 facade，不能重新定义类型 | `cells/auditcore/internal/appender` | 专属 |
| YAML-QUOTE-FUNNEL-01 | Hard↓/Medium↑ | string-typed-funnel | yamlsafe.Scalar 转换的参数必须是 yamlsafe.Quote 或已类型化的 Scalar | `pkg/yamlsafe` | 通用 |
