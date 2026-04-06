# R1D-3: adapters/rabbitmq -- Product Review

| Field | Value |
|-------|-------|
| Module | `adapters/rabbitmq` |
| Reviewer Role | Product Manager |
| Date | 2026-04-06 |
| Source PR | #12 (introduced), #14 (idempotency evolution), #31 (integration), #35 (TryProcess) |
| Persona | Go developer integrating GoCell event-driven messaging via `go get` |

---

## 1. Executive Summary

The `adapters/rabbitmq` module delivers a functionally complete RabbitMQ adapter covering the five core components (Connection, Publisher, Subscriber, ConsumerBase, DLQ). The API surface is clean and idiomatic Go, with proper `outbox.Publisher` and `outbox.Subscriber` interface compliance verified at compile time. However, several product-level gaps prevent a "green" verdict: (a) documentation drift between `adapter-config-reference.md` and actual code, (b) zero example code showing RabbitMQ usage, (c) missing observability metrics, and (d) two known correctness findings that degrade at-least-once delivery guarantees.

**Overall Verdict: CONDITIONAL PASS** -- P1 core features implemented, but 2 red and 2 yellow dimensions require follow-up.

---

## 2. Verification Scope -- Files Reviewed

| File | Lines | Role |
|------|-------|------|
| `doc.go` | 8 | Package documentation |
| `connection.go` | 380 | Connection lifecycle, auto-reconnect, channel pool |
| `publisher.go` | 81 | Publish with confirm mode |
| `subscriber.go` | 257 | Subscribe with ACK/NACK, graceful shutdown |
| `consumer_base.go` | 224 | Idempotency + retry + DLQ wrapper |
| `rabbitmq_test.go` | 1081 | Unit tests (mock-based) |
| `integration_test.go` | 267 | Integration tests (testcontainers) |
| `kernel/outbox/outbox.go` | 75 | Publisher/Subscriber interfaces |
| `kernel/idempotency/idempotency.go` | 28 | Checker interface |
| `docs/guides/adapter-config-reference.md` | 126 | Configuration reference |

---

## 3. Persona Assessment: "Go Developer Integrating RabbitMQ"

### 3.1 Can they discover the API via godoc?

`doc.go` is minimal (8 lines) but accurately describes scope: outbox.Publisher, outbox.Subscriber, auto-reconnect, channel pooling, publisher confirms, ConsumerBase. The Watermill reference is appropriate.

**Verdict**: Adequate for discovery. Missing: usage example in doc.go package comment.

### 3.2 Can they understand configuration?

`Config`, `SubscriberConfig`, and `ConsumerBaseConfig` structs have field-level godoc with defaults documented inline. `setDefaults()` methods ensure zero-value configs produce sensible behavior.

**Verdict**: Good. Developer can use zero-value structs and override only what matters.

### 3.3 Can they write a consumer in under 30 minutes?

The happy path requires:
1. `NewConnection(Config{URL: "amqp://..."})` -- straightforward
2. `NewPublisher(conn)` -- one-liner
3. `NewSubscriber(conn, SubscriberConfig{QueueName: "..."})` -- clear
4. `NewConsumerBase(checker, pub, ConsumerBaseConfig{...})` -- requires idempotency.Checker and outbox.Publisher

The ConsumerBase.Wrap() pattern is well-documented with inline comments explaining return semantics.

**Verdict**: Achievable for an experienced Go developer. However, no runnable example exists in `examples/` -- all three examples (sso-bff, todo-order, iot-device) use `eventbus.New()` (in-memory) instead.

### 3.4 Can they diagnose failures?

Error messages follow `"rabbitmq: {action}"` pattern with structured slog fields. Error codes (`ERR_ADAPTER_AMQP_*`) are module-specific and searchable. The `PermanentError` type provides a clear signal for DLQ routing.

**Verdict**: Good error diagnostics. One gap: `sanitizeURL` uses naive fixed-length truncation (first 10 chars), which can leak credential fragments for short URLs (known finding P0-F12S01).

---

## 4. Acceptance Criteria Assessment

### P1 -- Core Functionality

| ID | Criterion | Status | Evidence |
|----|-----------|--------|----------|
| AC-1 | Publisher implements `outbox.Publisher` | PASS | Compile-time check `var _ outbox.Publisher = (*Publisher)(nil)` in publisher.go:15 |
| AC-2 | Subscriber implements `outbox.Subscriber` | PASS | Compile-time check `var _ outbox.Subscriber = (*Subscriber)(nil)` in subscriber.go:19 |
| AC-3 | Publisher uses confirm mode for delivery guarantee | PASS | `ch.Confirm(false)` + `NotifyPublish` + timeout/nack handling in publisher.go:44-79 |
| AC-4 | Subscriber ACKs on handler success, NACKs on error | PASS | processDelivery: handler nil -> ACK (line 212), handler error -> NACK+requeue (line 203), unmarshal fail -> NACK no requeue (line 183) |
| AC-5 | Connection auto-reconnects on broker failure | PASS | `reconnectLoop()` with exponential backoff, tested in rabbitmq_test.go (backoff delay tests) |
| AC-6 | ConsumerBase provides idempotency check | PASS | `TryProcess` call in consumer_base.go:107. Uses atomic check-and-mark. |
| AC-7 | ConsumerBase routes exhausted retries to DLQ | PASS | Retry loop + deadLetter() call at line 170. Tested in TestConsumerBase_Wrap_RetryExhausted_DLQ |
| AC-8 | ConsumerBase routes PermanentError to DLQ immediately | PASS | Type assertion at line 134, tested in TestConsumerBase_Wrap_PermanentError_DLQ |
| AC-9 | Graceful shutdown waits for in-flight messages | PASS | Subscriber.Close() uses WaitGroup + ShutdownTimeout (subscriber.go:221-256) |
| AC-10 | Channel pool recycles connections | PASS | AcquireChannel/ReleaseChannel with buffered channel, tested in TestConnection_AcquireFromPool and TestConnection_ReleaseChannel_PoolFull |

**P1 Result: 10/10 PASS**

### P2 -- Enhanced Functionality

| ID | Criterion | Status | Evidence / Reason |
|----|-----------|--------|-------------------|
| AC-11 | Idempotency key follows `{group}:{event-id}` format per EventBus spec | PASS | consumer_base.go:104: `fmt.Sprintf("%s:%s", cb.config.ConsumerGroup, entry.ID)` |
| AC-12 | DLQ messages carry x-death metadata for debugging | PASS | deadLetter() enriches: x-death-reason, x-death-topic, x-death-consumer-group, x-death-retry-count, x-death-time |
| AC-13 | DLQ topic is configurable (default: `{topic}.dlq`) | PASS | ConsumerBaseConfig.DLQTopic with fallback, tested in TestConsumerBase_Wrap_CustomDLQTopic |
| AC-14 | RetryCount and RetryBaseDelay are configurable | PASS | ConsumerBaseConfig fields with defaults (3, 1s) |
| AC-15 | PrefetchCount is configurable per subscriber | PASS | SubscriberConfig.PrefetchCount, set via ch.Qos() |
| AC-16 | Idempotency fail-open preserves at-least-once semantics | FAIL | [P0-F12D01] TryProcess marks key before handler runs. If handler fails after mark, redelivered message is silently skipped. See PR35-noncompat-findings.md Finding #1. |
| AC-17 | Examples demonstrate RabbitMQ adapter usage | SKIP | All 3 examples use in-memory eventbus. Docker-compose files provision RabbitMQ but no Go code uses it. Reason: project stage (examples show dev-mode flow). |
| AC-18 | Metrics/counters for publish, consume, retry, DLQ events | SKIP | No metrics integration exists. DLQ events are logged only (slog.Error). Known finding P1-M4. |

**P2 Result: 6 PASS, 1 FAIL, 2 SKIP (with reasons)**

### P3 -- Infrastructure

| ID | Criterion | Status | Evidence / Reason |
|----|-----------|--------|-------------------|
| AC-19 | Unit tests cover happy path + error paths | PASS | 30+ test cases: connection, publisher (success/nack/timeout/cancel/error), subscriber (process/unmarshal/handler-error/close/default-queue), consumer base (success/already-processed/retry/dlq/permanent/custom-dlq/idempotency-error/context-cancel) |
| AC-20 | Integration tests with real RabbitMQ | PASS | 5 tests using testcontainers: health, publish-consume, publish-only, consumer-base-retry, connection-recovery |
| AC-21 | Consumer declaration comment per EventBus spec | PASS | Subscriber.Subscribe godoc (subscriber.go:79) and ConsumerBase type comment (consumer_base.go:69-72) include Consumer/Idempotency/ACK/Retry declarations |
| AC-22 | TLS configuration support | SKIP | No TLS Config extension point. Known finding P1-M5. Acceptable at current project stage. |

**P3 Result: 3 PASS, 1 SKIP**

---

## 5. Product Review -- 7 Dimensions

### A. Acceptance Criteria Coverage -- YELLOW

- P1: 10/10 = 100% PASS
- P2: 1 FAIL (AC-16 idempotency correctness), 2 SKIP with reasons
- P3: 1 SKIP with reason

The P2 FAIL on AC-16 is the primary concern. It represents a semantic correctness issue documented in PR35-noncompat-findings.md (Finding #1: "TryProcess can silently lose requeued messages"). While the code functions correctly in the happy path, the edge case degrades at-least-once to at-most-once.

### B. UI Compliance Check (API Surface) -- GREEN

| Check | Status |
|-------|--------|
| Empty/nil state handling | Config.setDefaults() handles all zero values; nil metadata initialized in processDelivery and deadLetter |
| Error responses | All errors use errcode package with structured codes (ERR_ADAPTER_AMQP_*) |
| Loading/waiting state | WaitConnected(ctx) provides blocking wait with context cancellation |
| Navigation (API discoverability) | Types are logically grouped: Connection -> Publisher/Subscriber -> ConsumerBase |

### C. Error Path Coverage -- YELLOW

| Error Scenario | Designed | Tested |
|----------------|----------|--------|
| Dial failure | Yes | Yes (TestNewConnection_DialFails) |
| Connection closed mid-operation | Yes | Yes (TestConnection_Health_Closed, AcquireChannel_ConnectionClosed) |
| Publish NACK from broker | Yes | Yes (TestPublisher_Publish_Nacked) |
| Publish confirm timeout | Yes | Yes (TestPublisher_Publish_ConfirmTimeout) |
| Context cancellation during publish | Yes | Yes (TestPublisher_Publish_ContextCancelled) |
| Channel error during publish | Yes | Yes (TestPublisher_Publish_PublishError) |
| Confirm mode enable failure | Yes | Yes (TestPublisher_Publish_ConfirmModeError) |
| Unmarshal failure in subscriber | Yes | Yes (TestSubscriber_Subscribe_UnmarshalFailure_Nack) |
| Handler transient error | Yes | Yes (TestSubscriber_Subscribe_HandlerError_NackWithRequeue) |
| Delivery channel closed | Yes | Yes (TestSubscriber_DeliveryChannelClosed) |
| Subscribe after close | Yes | Yes (TestSubscriber_Subscribe_AfterClose) |
| Idempotency check failure (fail-open) | Yes | Yes (TestConsumerBase_Wrap_IdempotencyCheckError_StillProcesses) |
| Context cancel during retry backoff | Yes | Yes (TestConsumerBase_Wrap_ContextCancelled_DuringRetry) |
| Reconnect after broker restart | Designed | Partial (connection recovery test only checks Health, not re-subscribe) |
| Concurrent consumer processing | Designed (PrefetchCount) | NOT TESTED (known P1-L9) |
| DLQ publish failure | Designed (logged) | NOT TESTED |

Coverage: 13/16 scenarios tested = ~81%.

Gap: reconnect + re-subscribe end-to-end flow, concurrent consumption under PrefetchCount, and DLQ publish failure path are untested.

### D. Documentation Link Completeness -- RED

| Document | Status | Issue |
|----------|--------|-------|
| doc.go | Exists | Accurate but minimal. No usage example. |
| adapter-config-reference.md | Drift | **Significant mismatch** -- see Section 6 below |
| examples/ Go code | Missing | No example uses `adapters/rabbitmq` |
| examples/ docker-compose | Exists | sso-bff and todo-order provision RabbitMQ containers |
| Consumer declaration comments | Present | Matches EventBus spec format |

### E. Functionality Completeness -- GREEN

All five core components are implemented:
1. **Connection** -- lifecycle, auto-reconnect, channel pool, health check, WaitConnected
2. **Publisher** -- confirm mode, exchange declaration, context-aware
3. **Subscriber** -- QoS, exchange/queue/binding declaration, graceful shutdown
4. **ConsumerBase** -- idempotency, retry with backoff, PermanentError, DLQ routing
5. **DLQ** -- metadata enrichment (x-death-*), configurable topic

### F. Success Criteria Achievement -- YELLOW

| Criterion | Status |
|-----------|--------|
| RabbitMQ adapter implements outbox.Publisher + outbox.Subscriber | Met |
| ConsumerBase provides built-in retry + DLQ + idempotency | Met (with correctness caveat on TryProcess) |
| Adapter follows dependency rules (no cells/ or runtime/ imports) | Met -- depends only on kernel/outbox, kernel/idempotency, pkg/errcode |
| Developer can configure all knobs (retry, backoff, DLQ, prefetch) | Met |
| Production-ready observability (metrics) | NOT Met -- log-only |

### G. Product Tech Debt -- 5 items

| ID | Category | Description | Impact |
|----|----------|-------------|--------|
| [PRODUCT] PTD-1 | Correctness | TryProcess marks idempotency key before handler succeeds; redelivered messages silently lost | At-least-once guarantee broken in edge cases |
| [PRODUCT] PTD-2 | Documentation | adapter-config-reference.md describes fields that do not exist in code (see Section 6) | Developer confusion on first integration |
| [PRODUCT] PTD-3 | DX | No runnable example using RabbitMQ adapter in examples/ | Adoption friction |
| [PRODUCT] PTD-4 | Observability | No metrics (publish count, consume count, retry count, DLQ count) | Ops blind spot in production |
| [PRODUCT] PTD-5 | Security | sanitizeURL uses naive 10-char truncation, can leak credential fragments | Security hygiene |

---

## 6. Specific Findings

### 6.1 [验收标准缺失] AC-16: TryProcess Idempotency Correctness

**Severity**: P1 (impacts at-least-once guarantee -- core delivery semantics)

The `ConsumerBase.Wrap()` method calls `TryProcess()` (consumer_base.go:107) which atomically marks the idempotency key before the business handler executes. If the handler subsequently fails:
- ConsumerBase retries within its own retry budget -- this works correctly.
- But if the entire `Wrap()` function returns an error (e.g., context cancellation at line 158), the Subscriber NACKs and requeues.
- On redelivery, `TryProcess` returns `shouldProcess=false` and the message is silently ACKed without processing.

**Given** a message is delivered and TryProcess succeeds
**When** the handler returns a context.Cancelled error during retry backoff
**Then** the message is NACKed, requeued, redelivered, and silently dropped

This is documented in PR35-noncompat-findings.md (Finding #1) but remains unresolved.

**Recommendation**: Either (a) defer TryProcess to after handler success, reverting to IsProcessed pre-check + MarkProcessed post-success with a note about the TOCTOU trade-off, or (b) add a `ReleaseProcess(key)` method to idempotency.Checker for cleanup on non-permanent failures.

### 6.2 [开发者体验] Documentation Drift in adapter-config-reference.md

**Severity**: P2

The config reference document at `/Users/shengming/Documents/code/gocell/docs/guides/adapter-config-reference.md` (lines 52-78) describes a RabbitMQ configuration surface that does not match the actual code:

| Reference Doc Field | Actual Code Field | Mismatch |
|---------------------|-------------------|----------|
| `reconnectDelay` (duration) | `ReconnectBaseDelay` + `ReconnectMaxBackoff` (two fields) | Name and cardinality differ |
| `maxReconnect` (int, 0=unlimited) | Does not exist | Code always retries unlimited |
| `exchange` (required on Publisher) | Not a Publisher config field; topic passed per-call to Publish() | Architectural mismatch |
| `exchangeType` (default "topic") | Hardcoded "fanout" in publisher.go:39 | Type mismatch |
| `confirmMode` (bool, default true) | Always enabled, not configurable | Missing toggle |
| `queue` (required on ConsumerBase) | QueueName on SubscriberConfig, not ConsumerBaseConfig | Wrong struct |
| `consumerTag` (auto) | Generated as `cg-{queue}-{topic}`, not configurable | Correct default but doc implies configurability |
| `dlqExchange` (string) | `DLQTopic` (string on ConsumerBaseConfig) | Name mismatch |
| `idempotencyStore` (idempotency.Store) | `idempotency.Checker` interface, passed to NewConsumerBase() | Type name mismatch |

A developer reading this guide before looking at code will build incorrect mental models.

### 6.3 [开发者体验] No RabbitMQ Usage in Examples

**Severity**: P2

All three example applications (sso-bff, todo-order, iot-device) use `eventbus.New()` for in-memory pub/sub despite having `docker-compose.yml` files that provision RabbitMQ containers. A Go developer looking for "how do I actually wire RabbitMQ into my GoCell assembly" has no reference code.

**Recommendation**: Add a `// +build rabbitmq` variant in at least one example, or add a standalone `examples/rabbitmq-demo/main.go`.

### 6.4 [验收标准缺失] Missing Observability Metrics

**Severity**: P2

The EventBus specification (`.claude/rules/gocell/eventbus.md`) requires: "dead-letter messages must be observable (counting metrics or logs)." Currently DLQ events are logged via `slog.Error` (consumer_base.go:216-222), satisfying the "or logs" portion. However, there are no metrics for:
- Messages published (count, latency)
- Messages consumed (count, latency)
- Retry attempts (count by topic)
- DLQ routings (count by topic and consumer group)

The `runtime/observability/metrics` package exists in the codebase but is not integrated with the RabbitMQ adapter.

### 6.5 [范围偏移] processDelivery is Serial Despite PrefetchCount

**Severity**: P2 (known finding P1-M7)

`subscriber.go:162` calls `s.processDelivery()` synchronously within the select loop. Even with `PrefetchCount=10`, only one message is processed at a time. The WaitGroup (`s.wg`) exists but is never leveraged for concurrent processing.

A developer setting `PrefetchCount: 50` would expect concurrent consumption but get serial behavior, which is misleading.

### 6.6 [开发者体验] sanitizeURL Leaks Credential Fragments

**Severity**: P1 (security)

`connection.go:372-379`: `sanitizeURL` takes the first 10 characters of the URL. For `amqp://guest:guest@localhost:5672/`, the output is `amqp://gue***` -- this leaks the beginning of the username. For shorter URLs with passwords, more sensitive information could be exposed.

**Recommendation**: Use `net/url.Parse()` to properly redact the userinfo component, or replace with a fixed string like `amqp://***@{host}`.

---

## 7. Dimension Summary

| Dimension | Rating | Key Rationale |
|-----------|--------|---------------|
| A. Acceptance Criteria Coverage | YELLOW | P1 100%, but P2 has 1 FAIL (AC-16 idempotency) |
| B. API Surface Compliance | GREEN | Clean types, proper defaults, structured errors |
| C. Error Path Coverage | YELLOW | ~81% (13/16). Reconnect flow, concurrent consume, DLQ-publish-fail untested |
| D. Documentation Completeness | RED | adapter-config-reference.md severely drifted; no example code |
| E. Functionality Completeness | GREEN | All 5 components delivered |
| F. Success Criteria Achievement | YELLOW | 4/5 met; metrics gap |
| G. Product Tech Debt | RED | 5 items including P1 correctness issue |

---

## 8. Confirmation Checklist

| Check | Status |
|-------|--------|
| Product context defined (persona + success criteria) | DONE (Section 3 + F) |
| Acceptance criteria graded (P1/P2/P3) | DONE (Section 4) |
| P1 acceptance criteria = 100% PASS | PASS (10/10) |
| P2 no FAIL (SKIP must have reason) | FAIL -- AC-16 is FAIL |
| Product review report has no RED dimensions | FAIL -- D and G are RED |
| Consumer sign-off | CONDITIONAL |

**Final Verdict: CONDITIONAL PASS**

The module is functionally complete for all P1 criteria. The two blocking items for full PASS are:

1. **Must-fix (blocks production use)**: Resolve TryProcess idempotency race (PTD-1 / AC-16). Either revert to IsProcessed + MarkProcessed or add ReleaseProcess cleanup path.
2. **Must-fix (blocks developer adoption)**: Update adapter-config-reference.md to match actual Config/SubscriberConfig/ConsumerBaseConfig fields (PTD-2).

Three additional items recommended for next phase:
3. Add RabbitMQ example in examples/ (PTD-3)
4. Integrate runtime/observability/metrics for publish/consume/retry/DLQ counters (PTD-4)
5. Fix sanitizeURL to use net/url.Parse (PTD-5)
