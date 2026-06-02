# 错误处理规范

## 错误响应格式

```json
{"error": {"code": "ERR_DEVICE_NOT_FOUND", "message": "device not found", "details": [{"key": "deviceId", "value": "abc-123"}], "requestId": "..."}}
```

`requestId` 由框架从 ctx 自动注入（5xx 与 4xx 均下发；schema 定义为 optional），用于运维日志关联；wire camelCase 字段对应 slog 日志键 `request_id` (snake_case)。

## 错误码分组

| 前缀 | 模块 | 示例 |
|------|------|------|
| `ERR_AUTH_*` | 认证 | `ERR_AUTH_INVALID_TOKEN` |
| `ERR_VALIDATION_*` | 通用校验 | `ERR_VALIDATION_REQUIRED_FIELD` |

> 按项目实际模块扩展错误码前缀。

## HTTP 状态码映射

200 GET/PUT/PATCH | 201 POST | 202 异步 | 204 DELETE | 400 参数错误 | 401 未认证 | 403 无权限 | 404 不存在 | 409 冲突 | 413 过大 | 429 限流 | 500 内部错误

## 编码规则

1. 禁止 `errors.New` 对外暴露（exported package-scope `var Err* = errors.New(...)` 必须走 `errcode.New(code, message)`），由 archtest `EXPORTED-ERROR-NEW-01` 静态拦截。函数体内 `errors.New` 局部错误允许。
2. 错误必须包装上下文：`fmt.Errorf("enrollment: %w", err)`
3. 禁止 `_ = someFunc()` 忽略错误，必须显式处理或记录
4. handler 层统一转换领域错误为 HTTP 状态码，domain 层禁止返回 HTTP 状态码。对于 codegen 合同（contract.yaml `codegen: true`），cell adapter 通过返回生成的 `Xxx{Status}ErrorResponse{Body: errcode.Error{...}}` typed struct 表达业务 4xx/5xx；`return nil, err` 仅保留给未声明的 framework 5xx（panic recover、infrastructure faults），由 generated handler 走 `httputil.WriteError` 兜底。详见 ADR `docs/architecture/202605061500-adr-typed-response-envelope.md`。
5. 500 不暴露内部细节，写 `slog`；客户端看到的错误信息必须对用户有意义

## Message PII 静态字面量约束

`errcode.New` / `errcode.Wrap` 的 message 参数必须是 const literal（程序员写死的描述性文本）；runtime 产生的数据（用户输入、ID、计数等）禁止拼进 message。runtime 数据走两条通道：

- `WithDetails(errcode.PublicString("key", val))`（及 `PublicInt` / `PublicBool` / `PublicDuration` / `PublicTime`）— 公开字段，4xx 时随 `details` 数组下发给客户端，5xx 时框架 strip；
- `WithInternal(errcode.InternalAttr("key", val))` — 仅服务端日志可见，任何状态码下均不下发客户端。

`WithInternal` 不受 const literal 约束（整条通道仅服务端可见）。archtest `MESSAGE-CONST-LITERAL-01` 静态守卫 `errcode.New/Wrap` message 参数位置；`PublicString` 系列 / `InternalAttr` 的 value 参数无 const 约束（runtime 数据在 details/internal 通道是预期用法）。

### archtest carve-out 约束

archtest 对上述规则的豁免（carve-out）仅允许 **function-level** 粒度，禁止 file-level 或 package-level 豁免。任何 carve-out 必须登记于 ADR `docs/architecture/202605121800-adr-archtest-carveout-narrow.md` 的 registry 表中，并说明理由。

新增或删除 carve-out 必须在**同一 PR 内**同时完成：

1. 修改 ADR registry 表（增删对应行）
2. 修改 `tools/archtest/errcode_invariants_test.go` 中的 `errcodeKindLiteralCarveOuts` 映射

archtest `ERRCODE-CARVEOUT-ADR-CONSISTENCY-01` Hard 守卫：任一侧单独漂移即导致 CI 红，阻止合并。

## Panic taxonomy and Approved funnel

All production panic calls must wrap with the typed marker:

```
panic(panicregister.Approved("<reason>", <value>))
```

`<reason>` is a kebab-case string literal identifying the site (e.g.
`registry-health-name`, `pg-tx-savepoint-rollback-rethrow`). It is not
cross-checked against any catalog at build time; it serves as source-level
documentation.

`<value>` is the panic payload:

- **A class** (state-machine unreachable branch): wrap `errcode.Assertion("...")` —
  the constructor returns `*errcode.Error` with KindInternal/ErrInternal/CategoryInfra
  for kernel Recovery middleware to convert to 500 and log.
- **B class** (programmer-error parameter): same as A — `errcode.Assertion("...")`.
- **C class** (framework re-throw): wrap the original recovered value unchanged.
  These are exactly four sites — `kernel/wrapper/lifecycle.go::recoverAndFinish`,
  `runtime/http/middleware/circuit_breaker.go::repanicAfterBreakerFailure`,
  `adapters/postgres/tx_manager.go::repanicAfterTopLevelTxRollback`,
  `adapters/postgres/tx_manager.go::repanicAfterSavepointRollback`. See
  `docs/architecture/202604270030-architectural-panic-whitelist.md` §4.1.

Archtest `PANIC-REGISTERED-01` enforces the wrap shape: every production
`panic(arg)` must have `arg = panicregister.Approved(literal, _)` CallExpr.
There is no `Must*`-prefix exemption, no comment-anchor escape, no whitelist
map. AI co-authors writing a new panic call site can either:

1. Wrap with Approved + Assertion (A/B class) and the archtest passes.
2. Wrap with Approved + recovered value (C class) — used in exactly the
   four ADR-listed re-throw functions.

Any other shape — bare panic, missing wrap, non-literal reason, different
callee — fails archtest immediately.

## Details 类型安全：PublicDetail / InternalDetail sealed newtype + sealed value marker

`WithDetails` / `WithInternal` 参数是 sealed newtype（`pkg/errcode/details.go`）：

- **Public 通道**走 typed scalar 构造器 — `PublicString` / `PublicInt[T 整数]` / `PublicBool` / `PublicDuration` / `PublicTime`。`PublicDetail.value` 字段是 sealed `publicValue` marker interface（unexported `publicValue()` method），包外类型无法实现，所以非 wire-safe 值（chan / func / NaN/Inf float64 / map / struct / pointer）编译期不可表达。
- **Internal 通道**走 `InternalAttr(key, value any)` — value 仍是 `any`，因为整条通道仅服务端可见，wire-safety 不约束。

```go
errcode.New(ErrNotFound, "device not found",
    errcode.WithDetails(
        errcode.PublicString("deviceId", id),
        errcode.PublicInt("retryCount", n),
    ),
    errcode.WithInternal(errcode.InternalAttr("query", q)))
```

`PublicDetail` / `InternalDetail` 字段全部 unexported，包外不可结构字面量构造、不可类型 alias 重新可构造（Go nominal typing 同字段集 re-shape 也不接受）。Wire schema `error.details` 是 `array<{key: string, value: <scalar>}>`（由 `PublicDetail.MarshalJSON` 派生，wire schema 约束 value ∈ {string, number, boolean}）；5xx wire 强制 strip 为 `[]`（由 `Error.MarshalJSON` 经 `PublicProjection` / `project()` 实现）。`error-response-v1.schema.json` 的 `details.value` schema 与 typed 构造器集对齐（不含 float64）。

`InternalDetail` 永不进 wire，仅服务端 slog 与 `Error.Error()` 字符串可见；handler 想在 slog 输出每条 InternalDetail 用 `d.AsSlogAttr()` 转 `slog.Attr`。free-form 单字符串场景按约定走 `InternalAttr("_", "...")` —— `Error.Error()` 将单条 `_` 键渲染为 bare value（保留原 `[CODE] msg` 字符串格式）：

```go
// free-form 单字符串场景（mechanical migration of pre-#1035 WithInternal(string)）
errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf("query=%s page=%d", q, p)))

// 结构化场景（推荐用于新写代码：单独的 key 让 slog 字段可被查询）
errcode.WithInternal(
    errcode.InternalAttr("op", "scan"),
    errcode.InternalAttr("query", q),
    errcode.InternalAttr("retries", n),
)
```

archtest `DETAILS-SLOG-ATTR-01` 已退役（type system 已表达 invariant）；新加 `DETAILS-SEALED-FIELD-FROZEN-01`（reflect + AST lock）反向守卫 PublicDetail/InternalDetail 字段集 + 可见性 + publicValue 类型身份 + publicValue 实现集合 + PublicDetail constructor 集合，防止包内漂移（如 value 字段被改回 `any`、字段大写化、重命名、悄悄新增 `PublicFloat`）。`MustValidateDetailsKinds` 同样退役（typed value 已使非 scalar 在 type system 不可表达）。`PublicAttr(any)` 删除：调用方按 value 类型选 typed scalar 构造器（不再有 `any` 入口）；为强制 caller 自行 format 浮点数（NaN/Inf 风险），**故意不提供** `PublicFloat`。

详见 ADR `docs/architecture/202605051730-adr-errcode-message-pii-safety.md` §Amendment 2026-05-27。

## 错误码前缀所有权 (#1091)

`pkg/errcode` 维护一个 **closed-set 前缀所有权注册表**，防止跨 module 前缀碰撞。

- **平台自注册**：`pkg/errcode/prefix_registry.go` 的 `init()` 将全部 65 个平台前缀（52 个 namespace entry `ERR_<SEG>_` + 13 个 whole-code entry）注册为 owner `github.com/ghbvf/gocell`。
- **外部 cell 注册**：外部 cell module 在自己的 `init()` 中调用 `errcode.RegisterPrefix(prefix, owner)` 声明自己的命名空间；同 prefix 不同 owner 触发 panic fail-fast（启动期暴露碰撞，不静默继续）。
- **新增 namespace prefix**：新增 `ERR_<SEG>_` 前缀需在同一 PR 内：(1) 追加到 `gocellPlatformPrefixes`（或外部 module `init()`）；(2) 以 `ERRCODE_PREFIX_GOLDEN_UPDATE=1` 重新生成 `pkg/errcode/testdata/prefix_set.golden`；(3) 通过 `ERRCODE-PREFIX-OWNERSHIP-01` archtest——三步缺一 CI 红。
- **新增 whole-code entry**：适用于单概念泛型 code（`ERR_NOT_FOUND` / `ERR_INTERNAL` 等）——这类 code 不构成 namespace，用 whole-code entry 避免 `ERR_NOT_` 等前缀误覆盖无关外部命名空间。步骤同上。
- **ERRCODE-PREFIX-OWNERSHIP-01**：archtest 扫描所有生产 `errcode.New`/`Wrap` callsite（字面量 + const selector）及导出 Code sentinel，断言每个 code 的前缀 ∈ 注册集合；直接 runtime 组装（`errcode.Code(non-const)` / `"ERR_"+x` at mint site）hard-fail。评级见 ADR `docs/architecture/202606031200-1091-adr-errcode-prefix-ownership-registry.md` §AI-robust 评级。
