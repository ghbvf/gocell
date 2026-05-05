# ADR: errcode message PII safety — const literal message, typed Details, Assertion ctor (K #08 W1-G)

> Status: Accepted
> Date: 2026-05-05
> ref: `docs/plans/202605011500-029-master-roadmap.md` Track K #08 W1-G
> ref: `docs/reviews/202604061630-dependency-replacement-plan.md`
> ref: PR #368（errcode residual 审计收口）

## Context

### 问题一：`WithDetails(map[string]any)` 类型不安全

旧版 `errcode.WithDetails` 接受 `map[string]any`，由内部 `attrsToMap` helper 在 marshal 时将
`[]slog.Attr` 转成 `map[string]any`。两套路径并存导致：

- 调用方可以绕过 `slog.Attr` 构造，直接传任意结构（嵌套 map、切片、指针），序列化结果不可预测；
- wire schema `error.details` 声明为 `object`（任意 key-value），和 `array<{key,value}>` 的实际设计意图矛盾；
- `attrsToMap` 是运行时转换，编译器对 key 类型无约束，拼写错误只在测试/运行时暴露。

### 问题二：message 参数允许 runtime 数据，导致 PII 泄漏到客户端

`errcode.New(code, "user %s not found", userID)` 这种写法将 runtime 数据（`userID`）直接拼入
message，message 在所有状态码（含 4xx）下不加过滤地下发给客户端。GoCell 无单独的 message
redaction 路径，PII 数据无法被 `pkg/redaction` 拦截（redaction 只处理 `span.RecordError` 文本
和 outbox `last_error` 列，不处理 `errcode.Error.message` 字段）。

### 问题三：生产 panic 兜底分散，缺少统一语义

审计发现 `cells/` 层存在直接 `panic(fmt.Sprintf(...))` 的不可达分支处理，没有走框架 recover
路径，导致：

- 不可达路径的错误无法被 kernel recover 捕获并转换为结构化 500；
- 日志缺少 CategoryInfra 标记，无法在 alert 中与框架级错误统一过滤；
- 代码审查时无法区分"合法的显式 re-throw"与"懒得处理的 panic"。

## Decision

### Decision 1 — 新增 `errcode.Assertion(format string, args ...any) *Error`

```go
func Assertion(format string, args ...any) *Error {
    return New(KindInternal, ErrInternal,
        fmt.Sprintf(format, args...),
        WithCategory(CategoryInfra),
    )
}
```

调用约定：不可达分支（状态机 impossible case、编程约定违反）统一使用 `errcode.Assertion`，
由 kernel `Recovery` middleware 捕获后转 500 并记 `slog.Error`。`format` 不受 const literal
约束（Assertion 本身即调试上下文，不下发客户端）。

C 类 re-throw（框架生命周期）保留 bare panic，需在注释中声明豁免理由，共 6 处：
`lifecycle.go` 启动超时、`circuit_breaker` recover re-throw、`tx_manager` 嵌套事务 re-throw、
`websocket handler` protocol error、`metrics` 注册冲突、`kernel/cell` bootstrap fatal。

archtest `PANIC-REGISTERED-01` 拦截非豁免文件中出现的裸 `panic`（recover 块内 re-throw 需在 `architecturalPanicWhitelist` 中显式注册）。

### Decision 2 — `Error.Details` 改 `[]slog.Attr`，`WithDetails(...slog.Attr)`

```go
// 新签名
func WithDetails(attrs ...slog.Attr) Option

// 调用示例
errcode.New(ErrNotFound, "device not found",
    errcode.WithDetails(
        slog.String("deviceId", id),
        slog.Int("retryCount", n),
    ))
```

删除 `attrsToMap` helper（单源治理）。`[]slog.Attr` 是 `log/slog` 标准类型，编译器保证
key 为 `string`，value 为 `slog.Value`（类型安全）。

archtest `DETAILS-SLOG-ATTR-01` 拦截以 `map[string]any` 形式调用 `WithDetails` 的旧式代码。

### Decision 3 — `Error.MarshalJSON()` 输出 wire `details: array<{key,value}>`

```json
{
  "error": {
    "code": "ERR_NOT_FOUND",
    "message": "device not found",
    "details": [
      {"key": "deviceId", "value": "abc-123"},
      {"key": "retryCount", "value": 3}
    ]
  }
}
```

`slog.Attr.Value` 经 `slog.Value.Any()` 取出后直接序列化，保留原始 Go 类型（string/int/bool
等）。嵌套 `slog.GroupValue` 序列化为嵌套 object（`{"key": "db", "value": {"host": "...", "port": 5432}}`）。

`contracts/shared/errors/error-response-v1.schema.json` 中 `details` 字段类型从 `object` 改为：

```json
{
  "type": "array",
  "items": {
    "type": "object",
    "properties": {
      "key":   {"type": "string"},
      "value": {}
    },
    "required": ["key", "value"],
    "additionalProperties": false
  }
}
```

wire schema 不向后兼容（`object` → `array`）。GoCell 宪法保证无外部 SDK 消费方，接受该破坏性变更。

### Decision 4 — `errcode.New/Wrap` message 必须 const literal

message 参数语义收窄为"固定的、程序员写死的描述性文本"。runtime 数据通道：

| 通道 | API | 4xx 客户端可见 | 5xx 客户端可见 | 服务端日志 |
|------|-----|--------------|--------------|-----------|
| message | const literal | 是 | 是 | 是 |
| details | `WithDetails(slog.Attr...)` | 是 | 否（框架 strip） | 是 |
| internal | `WithInternal(string)` | 否 | 否 | 是 |

archtest `MESSAGE-CONST-LITERAL-01` 静态检查：`errcode.New` / `errcode.Wrap` 第三参数位置
（message 位置）出现 `fmt.Sprintf` 调用或字符串 `+` 拼接时报错。`errcode.Assertion` 和
`WithInternal` 调用点不在检查范围内。

**Bridge / serialization boundary carve-out**：`ERRCODE-KIND-LITERAL-01` 对
`pkg/ctxcancel/ctxcancel.go` 和 `pkg/httputil/response.go` 做 file-level 豁免。
前者 `WrapOrInfra` 接受 caller-supplied `fallbackMsg string`；后者 `WritePublic`
接受 framework-selected `message string`。两个函数都是 IO/wire 边界 helper，
调用方实际全部传 const literal 字面量，但 Go 类型系统不区分 const-string 与
var-string，archtest 只能在 helper 自身处豁免。后续工作见 backlog `B2-K-08-CARVEOUT-NARROW`。

## Consequences

### Positive

- **类型安全**：`WithDetails` 编译期保证 key/value 类型，消除 `attrsToMap` 运行时转换层。
- **PII 默认隔离**：message 是 const literal，runtime 数据只能走 `WithDetails`（5xx 自动
  strip）或 `WithInternal`（永不下发），无需额外 redaction 规则。
- **Assertion 单源治理**：不可达分支统一走 `errcode.Assertion`，kernel recover 路径完整覆盖，
  CategoryInfra 标记使 alert 过滤一致。
- **wire schema 明确**：`details: array<{key,value}>` 比任意 object 更易验证和生成文档。
- **移除 attrsToMap**：减少约 30 行内部转换代码，消除一处隐式类型宽化点。

### Negative（已接受）

- **wire schema 不向后兼容**：`details` 从 `object` 改为 `array`，任何已部署客户端解析
  `details` 的代码需同步更新。GoCell 宪法明确"无外部调用方，不考虑向后兼容"，接受。
- **存量调用点批量改造**：全仓库 `WithDetails(map[string]any{...})` 调用需改为
  `WithDetails(slog.String(...), slog.Int(...), ...)`，工作量集中在一次 batch PR 内。
- **MESSAGE-CONST-LITERAL-01 误报风险**：极少数场景下 message 确实需要包含有限枚举值（如
  `"unsupported kind: %s"`）。archtest 豁免列表维护成本小，接受。

## Alternatives Considered

### A1：保留 `attrsToMap` bridge，`WithDetails` 同时接受 `map[string]any` 和 `[]slog.Attr`

被否。两套路径并存是当前混乱的根源。bridge 是"向后兼容"的软兼容结构，违反"激进三原则"（彻底/不向后兼容/优雅简洁）。GoCell 无外部消费方，不需要过渡期。

### A2：`WithUnsafeMessage(fmt.Sprintf(...))` 作为逃生阀，message 可选携带 runtime 数据

被否。archtest `MESSAGE-CONST-LITERAL-01` 的价值在于强制所有 runtime 数据走有 redaction
语义的通道；提供逃生阀等同于开后门，时间一长调用方会优先走最方便的路径（WithUnsafeMessage），
而不是语义正确的路径（WithDetails/WithInternal）。

### A3：生产 panic 不动，只加 `errcode.Assertion` 作为可选 alternative

被否。不动就无法消除"合法 re-throw"与"懒得处理的 panic"的区分问题。archtest 守卫必须
以"新默认"（Assertion）为基准，否则无法静态区分两类。C 类豁免列表明确列出 6 处，其余
强制迁移，代价可控。

## References

- `ref: cockroachdb/errors assert/assert.go` — `AssertionFailedf` 模式，panic 与结构化
  错误的分类治理
- `ref: golang/go log/slog` — `slog.Attr` / `slog.Value` 类型系统
- `ref: golang/go net/url` — `URL.Redacted()` 硬编替换模式（PII 分层隔离参考）
- PR #368 errcode 残留收口审计
- `docs/architecture/202604242030-adr-kernel-wrapper-contract-observability.md` §8（Span Error Redaction）
- `docs/architecture/202605031600-adr-v1-schema-evolution.md` §5（error envelope 保持 strict）
- `.claude/rules/gocell/error-handling.md` §4-6（MESSAGE-CONST-LITERAL-01 / PANIC-REGISTERED-01 / DETAILS-SLOG-ATTR-01）
