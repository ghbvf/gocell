# R1D-3 RabbitMQ Adapter Code Style Review

| Field | Value |
|---|---|
| Reviewer Seat | S5 DX/Maintainability |
| Scope | `src/adapters/rabbitmq/*.go` (7 files: doc.go, connection.go, publisher.go, subscriber.go, consumer_base.go, rabbitmq_test.go, integration_test.go) |
| Review Basis Commit | `ce03ba1` (develop HEAD) |
| Date | 2026-04-06 |

---

## Summary

The rabbitmq adapter is a well-structured implementation of `outbox.Publisher` / `outbox.Subscriber` with auto-reconnect, channel pooling, publisher confirms, and a `ConsumerBase` providing idempotency + retry + DLQ. Overall code quality is high: errcode is used consistently for production code, slog is used with structured fields throughout, and the Watermill `ref:` tag is present in both doc.go and subscriber.go. Six findings identified below, with one P1.

---

## Findings

### F-01 | P1 | errcode | consumer_base.go:134 | PermanentError check uses direct type assertion instead of errors.As

**Evidence:**

```go
// consumer_base.go:134
if _, ok := lastErr.(*PermanentError); ok {
```

**Problem:** The check uses a direct type assertion `lastErr.(*PermanentError)`. If a caller wraps a `PermanentError` inside another error (e.g., `fmt.Errorf("handler: %w", NewPermanentError(err))`), the type assertion will fail and the permanent error will be retried instead of routed to DLQ. The Go idiomatic approach is `errors.As`.

**Fix:**

```go
var permErr *PermanentError
if errors.As(lastErr, &permErr) {
```

This requires adding `"errors"` to the import block. Since `ConsumerBase` is a framework-level building block used by all consumers, this incorrect unwrapping behavior will silently cause wrapped permanent errors to exhaust retries and reach DLQ via the retry-exhaustion path instead of the immediate-DLQ path, leading to unnecessary retry delays and misleading log messages.

**Status:** OPEN

---

### F-02 | P2 | slog-level | consumer_base.go:216 | DLQ success routing logged at Error level

**Evidence:**

```go
// consumer_base.go:216
slog.Error("rabbitmq: message routed to dead letter queue",
    slog.String("event_id", entry.ID),
    ...
```

**Problem:** After the DLQ publish succeeds (line 205 returned nil), the "message routed to dead letter queue" log at line 216 uses `slog.Error`. Per the observability spec, Error is for events "affecting correctness" (DB write failures, ACK failures). A successful DLQ routing is a degraded-but-expected code path -- it should be `slog.Warn`. The `slog.Error` at lines 198 and 206 (marshal failure / publish failure) are correct since those represent actual failures.

**Fix:** Change `slog.Error` to `slog.Warn` on line 216.

**Status:** OPEN

---

### F-03 | P2 | sanitize | connection.go:372-379 | sanitizeURL is incomplete -- may leak credentials

**Evidence:**

```go
// connection.go:372-379
func sanitizeURL(url string) string {
    // Simple approach: just indicate the host portion.
    // In production, parse the URL and redact credentials.
    if len(url) > 10 {
        return url[:10] + "***"
    }
    return "***"
}
```

**Problem:** The function truncates to 10 characters, but depending on the URL format, the first 10 characters may include parts of the username (e.g., `amqp://use***` for `amqp://user:pass@host`). The comment acknowledges this is incomplete ("In production, parse the URL and redact credentials") but the TODO was never resolved. For a connection module that logs the URL on every connection/reconnection event (lines 182-183, 256), this needs a proper implementation.

**Fix:** Use `net/url.Parse` to properly redact the userinfo component:

```go
func sanitizeURL(raw string) string {
    u, err := url.Parse(raw)
    if err != nil {
        return "***"
    }
    u.User = nil
    return u.String()
}
```

**Status:** OPEN

---

### F-04 | P2 | ref-tag | connection.go, publisher.go, consumer_base.go | Missing ref: tag on connection.go, publisher.go, consumer_base.go

**Evidence:**

The `ref:` Watermill tag appears in:
- `doc.go:7` -- `ref: Watermill watermill-amqp subscriber.go`
- `subscriber.go:46` -- `ref: Watermill watermill-amqp subscriber.go`

But is absent from:
- `connection.go` -- reconnect loop with exponential backoff is a core Watermill pattern (`watermill-amqp/connection.go`)
- `publisher.go` -- publisher confirm mode aligns with Watermill's publisher
- `consumer_base.go` -- idempotency + retry + DLQ pattern

Per CLAUDE.md: "编码时在 PR 描述或 commit message 中注明: `ref: {framework} {file}` + 采纳/偏离理由". The `connection.go` reconnect pattern and `consumer_base.go` DLQ routing are the most architecturally significant pieces and should carry `ref:` annotations documenting design decisions.

**Fix:** Add `ref:` comments to:
- `connection.go` Connection type godoc: `ref: Watermill watermill-amqp connection.go -- reconnect backoff pattern`
- `publisher.go` Publisher type godoc: `ref: Watermill watermill-amqp publisher.go -- confirm mode pattern`
- `consumer_base.go` ConsumerBase type godoc: `ref: Watermill middleware/ -- retry + poison queue pattern`

**Status:** OPEN

---

### F-05 | P2 | CC/complexity | consumer_base.go:102-175 | Wrap method cognitive complexity is borderline

**Evidence:**

Manual CC count for `Wrap` closure (consumer_base.go lines 102-175):

| Element | +CC |
|---|---|
| `if err != nil` (idempotency check) | +1 |
| nested `if` (shouldProcess = true) | +1 (nesting) |
| `if !shouldProcess` | +1 |
| `for attempt := range` | +1 |
| `if lastErr == nil` | +1 (nesting +1) |
| `if _, ok := ... PermanentError` | +1 (nesting +2) |
| `if attempt < RetryCount-1` | +1 (nesting +1) |
| `select` (time.After / ctx.Done) | +1 (nesting +2) |
| Total | ~12 |

**Problem:** CC is approximately 12, within the <= 15 limit but close. The method handles four distinct concerns in one closure: idempotency check, retry loop, permanent error detection, and DLQ routing. While technically compliant, extracting the retry loop into a private `executeWithRetry` method would improve readability and testability.

**Fix:** Optional refactor -- extract retry logic into:

```go
func (cb *ConsumerBase) executeWithRetry(ctx context.Context, entry outbox.Entry, topic string, handler func(context.Context, outbox.Entry) error) error
```

**Status:** OPEN (advisory)

---

### F-06 | P2 | naming | consumer_base.go:53 | PermanentError.Error() uses fmt.Sprintf unnecessarily

**Evidence:**

```go
// consumer_base.go:53
func (e *PermanentError) Error() string {
    return fmt.Sprintf("permanent: %s", e.Err.Error())
}
```

**Problem:** Minor -- `fmt.Sprintf` with a single `%s` and `.Error()` call can be simplified to string concatenation: `"permanent: " + e.Err.Error()`. More importantly, the conventional Go pattern is `fmt.Sprintf("permanent: %v", e.Err)` (using `%v` on the error itself rather than calling `.Error()` explicitly). This is a cosmetic nit.

**Status:** OPEN (advisory)

---

## Positive Observations

1. **errcode discipline:** All production `.go` files use `errcode.New` / `errcode.Wrap` exclusively. Zero `errors.New` in production code. The `errors.New` calls in `rabbitmq_test.go` are appropriate for test stubs.

2. **slog structure:** Every slog call includes structured key-value fields (`topic`, `event_id`, `consumer_group`, `error`, `attempt`, etc.). No bare `slog.Error("failed")` anywhere. Log levels are correct for connection lifecycle (Info), degraded states (Warn), and failures (Error) -- with the one exception noted in F-02.

3. **No fmt.Println / log.Printf:** Zero occurrences in the entire module.

4. **AMQP abbreviation:** Correctly uppercased in type names: `AMQPConnection`, `AMQPChannel`, `ErrAdapterAMQP*`. The `DLQ` abbreviation is correctly uppercased in `DLQTopic`, `DLQEntry` metadata keys.

5. **Consumer declaration comment:** `ConsumerBase` at line 68-72 and `Subscriber.Subscribe` at line 76-79 both carry the required EventBus consumer declaration format (Consumer tag, Idempotency key, ACK timing, Retry strategy).

6. **Compile-time interface checks:** Both `publisher.go:15` and `subscriber.go:19` have `var _ outbox.Publisher = (*Publisher)(nil)` / `var _ outbox.Subscriber = (*Subscriber)(nil)`.

7. **Layer compliance:** adapters/rabbitmq imports only `kernel/outbox`, `kernel/idempotency`, and `pkg/errcode`. No imports from `runtime/`, `cells/`, or other adapters. Fully compliant with the dependency rules.

8. **Test coverage breadth:** 30+ unit tests covering success paths, error paths, edge cases (closed subscriber, pool full, idempotent replay, context cancellation, permanent vs transient errors, DLQ routing). Integration tests use testcontainers for real RabbitMQ validation.

---

## Findings Ledger

| ID | Sev | File | Category | Status |
|---|---|---|---|---|
| F-01 | P1 | consumer_base.go:134 | errcode/errors.As | OPEN |
| F-02 | P2 | consumer_base.go:216 | slog-level | OPEN |
| F-03 | P2 | connection.go:372-379 | sanitize/security | OPEN |
| F-04 | P2 | connection.go, publisher.go, consumer_base.go | ref-tag | OPEN |
| F-05 | P2 | consumer_base.go:102-175 | CC/complexity | OPEN (advisory) |
| F-06 | P2 | consumer_base.go:53 | naming/style | OPEN (advisory) |

**Totals:** 0 P0 | 1 P1 | 5 P2

**Verdict:** No P0 blockers. One P1 (F-01 `errors.As`) should be fixed before the next release to prevent silent misbehavior with wrapped permanent errors. P2 items are recommended improvements.
