# 错误处理规范

## 错误响应格式

```json
{"error": {"code": "ERR_DEVICE_NOT_FOUND", "message": "device not found", "details": [{"key": "deviceId", "value": "abc-123"}]}}
```

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

- `WithDetails(slog.String("key", val))` — 公开字段，4xx 时随 `details` 数组下发给客户端，5xx 时框架 strip；
- `WithInternal(fmt.Sprintf(...))` — 仅服务端日志可见，任何状态码下均不下发客户端。

`WithInternal` 不受 const literal 约束。archtest `MESSAGE-CONST-LITERAL-01` 静态守卫，拦截任何在 `errcode.New/Wrap` 第三参数位置出现 `fmt.Sprintf` 或字符串拼接的调用点。

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

## Details 类型安全：slog.Attr

`WithDetails` 参数类型由 `map[string]any` 改为 `...slog.Attr`，调用方使用标准构造函数：

```go
errcode.New(ErrNotFound, "device not found",
    errcode.WithDetails(
        slog.String("deviceId", id),
        slog.Int("retryCount", n),
    ))
```

wire schema `error.details` 为 `array<{key: string, value: any}>`，由 `Error.MarshalJSON()` 从 `[]slog.Attr` 派生，不再是任意 JSON object。`error-response-v1.schema.json` 中 `details` 字段类型同步改为 `array`。archtest `DETAILS-SLOG-ATTR-01` 拦截直接传入 `map[string]any` 的旧式调用。移除旧版 `attrsToMap` helper（单源治理，不保留过渡桥接）。
