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

### Decision 2 — sealed PublicDetail / InternalDetail newtypes (revised 2026-05-27, see §Amendment)

```go
// pkg/errcode/details.go — sealed (unexported fields)
type PublicDetail   struct { key string; value any }
type InternalDetail struct { key string; value any }

func PublicAttr(key string, value any) PublicDetail
func InternalAttr(key string, value any) InternalDetail

// pkg/errcode/errcode.go
func WithDetails(details ...PublicDetail) Option
func WithInternal(details ...InternalDetail) Option

// Call sites
errcode.New(ErrNotFound, "device not found",
    errcode.WithDetails(
        errcode.PublicAttr("deviceId", id),
        errcode.PublicAttr("retryCount", n),
    ))
```

Outside-package construction of `PublicDetail` / `InternalDetail` is a Go
compile error (unexported fields). Same-field-set re-shape and type-alias
re-export are also rejected by Go's nominal type system. `attrsToMap` was
removed in this ADR's original landing; `MustValidateDetailsKinds` was removed
in the 2026-05-27 amendment after sealing made the bypass paths inexpressible.

Original Decision 2 used `WithDetails(...slog.Attr)` guarded by archtest
DETAILS-SLOG-ATTR-01 (Medium); see §Amendment 2026-05-27 for the upgrade
rationale and details.

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

`slog.Attr.Value` 经 `slog.Value.Any()` 取出后直接序列化，但只允许 JSON scalar：
string / number / boolean。`slog.Group`、`slog.Any`、`LogValuer`、以及 NaN/Inf float64
均在构造期 fail-fast；绕过构造器的防御层会替换为固定 sentinel，避免 JSON marshal 失败或
把对象结构暴露到 wire。

`contracts/shared/errors/error-response-v1.schema.json` 中 `details` 字段类型从 `object` 改为：

```json
{
  "type": "array",
  "items": {
    "type": "object",
    "properties": {
      "key":   {"type": "string"},
              "value": {"type": ["string", "number", "boolean"]}
    },
    "required": ["key", "value"],
    "additionalProperties": false
  }
}
```

wire schema 不向后兼容（`object` → `array`）。GoCell 宪法保证无外部 SDK 消费方，接受该破坏性变更。

### Decision 4 — `errcode.New/Wrap` message 必须 const literal

message 参数语义收窄为"固定的、程序员写死的描述性文本"。runtime 数据通道（revised 2026-05-27 to match the sealed types from §Amendment）：

| 通道 | API | 4xx 客户端可见 | 5xx 客户端可见 | 服务端日志 |
|------|-----|--------------|--------------|-----------|
| message | const literal | 是 | 是 | 是 |
| details | `WithDetails(errcode.PublicAttr(k, v))` | 是 | 否（框架 strip） | 是 |
| internal | `WithInternal(errcode.InternalAttr(k, v))` | 否 | 否 | 是 |

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
- **存量调用点批量改造**：全仓库改为 `WithDetails(errcode.PublicAttr(k, v))` /
  `WithInternal(errcode.InternalAttr(k, v))`（自 Amendment 2026-05-27 起），
  680+ callsites 在 PR #1035 单次 batch 内机械迁移完成。
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
- `.claude/rules/gocell/error-handling.md` §"Details 类型安全：PublicDetail" / §"Message PII 静态字面量约束"

## Amendment 2026-05-27 — sealed PublicDetail / InternalDetail newtype (#1035)

> Status: Accepted
> Date: 2026-05-27
> PR: #1035

### Trigger

DETAILS-SLOG-ATTR-01 archtest was AI-robust **Medium** — AST scanner rejecting
`WithDetails(map[string]any{...})` / `WithDetails(slog.Any(...))` / `WithDetails(slog.Group(...))`
after the fact. A misconfigured archtest entry, a hand-built `*errcode.Error`
literal bypassing `WithDetails`, or a future AI co-author silently broadening the
input type would all reintroduce the wire-shape risk this ADR closed. The
matching rule MESSAGE-CONST-LITERAL-01 covers the `message` parameter and stays
Medium because Go cannot express "const literal only" at the type level; the
Details / Internal channels, however, have an expressible upgrade.

### Amendment

Replace Decision 2's signature with the sealed-construction Hard pattern from
`.claude/rules/gocell/ai-robust.md` §Hard 范本目录 "sealed construction":

```go
// pkg/errcode/details.go — unexported fields prevent outside-package construction
type PublicDetail   struct { key string; value any }
type InternalDetail struct { key string; value any }

func PublicAttr(key string, value any) PublicDetail   { return PublicDetail{key, value} }
func InternalAttr(key string, value any) InternalDetail { return InternalDetail{key, value} }

func (d PublicDetail)  MarshalJSON() ([]byte, error) // preserves wire schema {"key":...,"value":...}
func (d PublicDetail)  Key() string                  // accessor
func (d PublicDetail)  Value() any                   // accessor
func (d PublicDetail)  AsSlogAttr() slog.Attr        // for slog forwarding

// errcode.go
func WithDetails(details ...PublicDetail) Option
func WithInternal(details ...InternalDetail) Option

type Error struct {
    ...
    Details         []PublicDetail
    InternalDetails []InternalDetail
    ...
}
```

`MustValidateDetailsKinds` / `isWireSafeAttrKind` / wire-unsafe sentinels (`<UNSUPPORTED_KIND>`
/ `<UNSUPPORTED_VALUE>`) are deleted: outside-package construction of `PublicDetail`
is impossible in Go's type system, so the bypass paths the validators guarded no
longer exist. JSON-marshalability remains enforced by `json.Marshal` at the wire
boundary (errors surface as `Error.MarshalJSON` failures, caught by
`pkg/httputil.writeErrorBody`'s sentinel fallback).

### Three-layer table revision

The Decision 4 channel table is restated for the new types:

| 通道 | API | 4xx wire | 5xx wire | server-side slog |
|------|-----|---------|---------|------------------|
| Message | `errcode.New(kind, code, "literal", ...)` — const literal | ✓ | ✓ | ✓ |
| Details | `WithDetails(errcode.PublicAttr("k", v))` — sealed `[]PublicDetail` | ✓ | strip → `[]` | ✓ |
| Internal | `WithInternal(errcode.InternalAttr("k", v))` — sealed `[]InternalDetail` | ✗ | ✗ | ✓ |

`InternalAttr` accepts arbitrary key/value pairs (the channel is server-only;
runtime data including `fmt.Sprintf` output flows here). For free-form
diagnostic strings, callers may use the conventional `_` sentinel key
(`InternalAttr("_", fmt.Sprintf(...))`) — `Error.Error()` renders a single
`_`-keyed entry as the bare value, preserving the pre-amendment Error() output
format.

### Archtest fate

| Archtest | Before | After | Rationale |
|----------|--------|-------|-----------|
| `DETAILS-SLOG-ATTR-01` | Medium AST scanner | **retired** | Compile-time check via sealed `WithDetails(...PublicDetail)`; wire-unsafe inputs are not expressible |
| `MESSAGE-CONST-LITERAL-01` | Medium (typed) | Medium (unchanged) | Guards `errcode.New/Wrap` `message string` parameter — Go cannot express "const literal only" at type level; archtest remains the only enforcement |
| `errcodeKindLiteralCarveOuts` registry + `ERRCODE-CARVEOUT-ADR-CONSISTENCY-01` | Hard | Hard (unchanged) | Carve-outs target `Message` channel, not Details / Internal |

The Decision-2 sentence "archtest `DETAILS-SLOG-ATTR-01` 拦截以 `map[string]any`
形式调用 `WithDetails` 的旧式代码" is **superseded by this amendment**;
references to that archtest in `.claude/rules/gocell/error-handling.md` and
`.claude/rules/gocell/observability.md` are updated in the same PR.

### Known bypass: `Error.Details` is an exported field

`errcode.Error.Details` ([]PublicDetail) remains an exported field, so
external code can write `e.Details = append(e.Details, errcode.PublicAttr("k", v))`
or replace the slice entirely with PublicDetail values constructed via the
public PublicAttr constructor. The sealed-construction Hard claim is "no
outside-package code can construct a non-zero PublicDetail without going
through PublicAttr", **not** "no outside code can mutate Error.Details".

This bypass surface is **acceptable** because the wire-side 5xx Details-strip
invariant (Error.MarshalJSON → project() → `Details = []PublicDetail{}`) is
the canonical defense against runtime data leaking onto the wire — the
defense is independent of append source. Tests that synthesize stress
conditions (e.g. `pkg/httputil/response_test.go::TestEncodeErrorEnvelopeTo_*`)
intentionally exercise this path to verify wire fail-closed.

Future hardening would seal `Error.Details` via an unexported `details
[]PublicDetail` field + read-only accessor + `addDetails(...)` mutator — but
that requires reworking the existing field reads (test files, the 2 direct
`Error{}` literal carve-outs in `pkg/httputil.WritePublic` /
`pkg/ctxcancel.WrapOrInfra`) and is out of scope for #1035.

### Why MESSAGE-CONST-LITERAL-01 does not retire

Sealing the `message` parameter (e.g. `type MessageLiteral string` newtype)
would not buy additional safety: any `string` can wrap into the newtype at
runtime (`MessageLiteral("dynamic")`), so the const-literal restriction remains
expressible only at the AST level. The archtest stays Medium with the existing
carveout registry; no scope change in this amendment.

### Consequences delta

Positive (new):
- **Hard sealing**: outside-package construction of `PublicDetail` /
  `InternalDetail` is a Go compile error; one Hard type-system invariant
  replaces one Medium AST archtest.
- **`MustValidateDetailsKinds` deleted**: ~30 LOC of runtime validation gone,
  no behavior change at the wire boundary (json.Marshal errors continue to
  trigger `sentinelInternalErrorBody`).

Negative (new, accepted):
- **`Error.InternalMessage` field renamed to `Error.InternalDetails`** with
  type change `string` → `[]InternalDetail`. Server-side slog log shape
  changes from `"internal_message": "..."` to per-entry attrs (or a
  formatted multi-key string via `Error.Error()`). No external consumers
  (GoCell is single-consumer per CLAUDE.md); 680+ callsites migrated mechanically.
- **`InternalAttr(key, value)` ergonomic cost**: free-form diagnostic
  strings now require a sentinel key. Convention is `_`; future PRs may
  introduce semantic keys (`op`, `query`, `reason`) where structure aids
  triage.
