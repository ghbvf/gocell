# R1D-3: adapters/rabbitmq Testing Review

- **Reviewer seat**: S3 (Test/Regression)
- **Review baseline commit**: `ce03ba1` (develop HEAD)
- **Scope**: `src/adapters/rabbitmq/` -- all production files and test files
- **Date**: 2026-04-06

---

## 1. Inventory

### Production files (4 + doc.go)

| File | LOC (approx) | Description |
|------|-------------|-------------|
| `connection.go` | 380 | AMQP connection, reconnect loop, channel pool, health, config |
| `publisher.go` | 81 | Outbox.Publisher implementation with confirm mode |
| `subscriber.go` | 257 | Outbox.Subscriber with consume loop, ACK/NACK, graceful shutdown |
| `consumer_base.go` | 224 | Idempotency + retry + DLQ wrapper for handlers |
| `doc.go` | 8 | Package documentation |

### Test files (2)

| File | LOC (approx) | Build tag | Description |
|------|-------------|-----------|-------------|
| `rabbitmq_test.go` | ~1081 | (none) | Unit tests with mocks: 30 test functions |
| `integration_test.go` | ~267 | `//go:build integration` | testcontainers tests: 5 test functions |

---

## 2. Coverage Analysis (estimated)

Coverage cannot be run in this environment (no running Go toolchain), so the following analysis is based on manual path tracing through the code.

### 2.1 Functions with direct or indirect unit-test coverage

**connection.go**:
- `Config.setDefaults()` -- `TestConfig_Defaults`
- `NewConnection()` success/fail -- `TestNewConnection_Success`, `TestNewConnection_DialFails`
- `WithDialFunc()` -- used by `newTestConnection` helper
- `connect()` -- indirectly via `NewConnection`
- `backoffDelay()` -- `TestConnection_BackoffDelay` (table-driven, 4 cases)
- `AcquireChannel()` success/closed/from-pool -- 3 tests
- `ReleaseChannel()` pool-full -- `TestConnection_ReleaseChannel_PoolFull`
- `Health()` open/closed -- 2 tests
- `Close()` idempotent -- `TestConnection_Close_Idempotent`
- `WaitConnected()` success/timeout -- 2 tests
- `sanitizeURL()` -- `TestSanitizeURL` (table-driven, 2 cases)

**publisher.go**:
- `Publish()` success -- `TestPublisher_Publish_Success`
- `Publish()` broker-nack -- `TestPublisher_Publish_Nacked`
- `Publish()` confirm-timeout -- `TestPublisher_Publish_ConfirmTimeout`
- `Publish()` ctx-cancelled -- `TestPublisher_Publish_ContextCancelled`
- `Publish()` publish-error -- `TestPublisher_Publish_PublishError`
- `Publish()` confirm-mode-error -- `TestPublisher_Publish_ConfirmModeError`

**subscriber.go**:
- `SubscriberConfig.setDefaults()` -- `TestSubscriberConfig_Defaults`
- `Subscribe()` normal delivery + ACK -- `TestSubscriber_Subscribe_ProcessesDelivery`
- `Subscribe()` unmarshal fail + NACK(no-requeue) -- `TestSubscriber_Subscribe_UnmarshalFailure_Nack`
- `Subscribe()` handler error + NACK(requeue) -- `TestSubscriber_Subscribe_HandlerError_NackWithRequeue`
- `Subscribe()` default queue name -- `TestSubscriber_Subscribe_DefaultQueueName`
- `Subscribe()` after close -- `TestSubscriber_Subscribe_AfterClose`
- `Subscribe()` delivery channel closed -- `TestSubscriber_DeliveryChannelClosed`
- `Close()` idempotent -- `TestSubscriber_Close_Idempotent`

**consumer_base.go**:
- `ConsumerBaseConfig.setDefaults()` -- `TestConsumerBaseConfig_Defaults`
- `PermanentError` Error/Unwrap -- `TestPermanentError`
- `Wrap()` success -- `TestConsumerBase_Wrap_Success`
- `Wrap()` already-processed (idempotency skip) -- `TestConsumerBase_Wrap_AlreadyProcessed`
- `Wrap()` transient error with retry -- `TestConsumerBase_Wrap_TransientError_Retry`
- `Wrap()` retry exhausted -> DLQ -- `TestConsumerBase_Wrap_RetryExhausted_DLQ`
- `Wrap()` permanent error -> DLQ -- `TestConsumerBase_Wrap_PermanentError_DLQ`
- `Wrap()` custom DLQ topic -- `TestConsumerBase_Wrap_CustomDLQTopic`
- `Wrap()` idempotency check error (fail-open) -- `TestConsumerBase_Wrap_IdempotencyCheckError_StillProcesses`
- `Wrap()` context cancelled during retry -- `TestConsumerBase_Wrap_ContextCancelled_DuringRetry`

### 2.2 Estimated coverage by file

| File | Covered functions | Total functions | Path coverage estimate |
|------|-------------------|-----------------|----------------------|
| `connection.go` | 11/16 | ~70% of paths | ~65-70% |
| `publisher.go` | 2/2 | 6/8 error paths | ~80% |
| `subscriber.go` | 4/6 | 7/12 error paths | ~60-65% |
| `consumer_base.go` | 5/5 | 9/11 paths | ~85% |
| **Overall estimate** | | | **~70-75%** |

**Verdict**: Likely below the 80% target for new adapter code.

---

## 3. Findings

### F-R1D3-01 [S3 / P1 / Missing coverage] reconnectLoop and reconnectWithBackoff have zero unit-test coverage

**Files**: `src/adapters/rabbitmq/connection.go` lines 187-260

**Evidence**: The `reconnectLoop()` (line 187) and `reconnectWithBackoff()` (line 228) methods are never invoked by any unit test. `newTestConnection()` does create a connection that starts the reconnect goroutine, but no test ever triggers a disconnect (sending an error on `NotifyClose`) to exercise the reconnect path. The `drainChannelPool()` (line 270) is also only indirectly tested via `Close()`.

The integration test `TestIntegration_ConnectionRecovery` only checks Health + AcquireChannel, it does NOT actually kill the connection or test recovery.

**Impact**: The core reliability mechanism of the adapter (auto-reconnect with exponential backoff) has no test coverage at all. Reconnection regressions would be invisible.

**Recommendation**: Add a unit test that:
1. Creates a connection with a mock dial func
2. Sends an `*amqp.Error` on the `notifyCloseCh` to trigger reconnect
3. Makes the first reconnect attempt fail, second succeed
4. Asserts `Health()` recovers and `AcquireChannel()` works after reconnect
5. Asserts `drainChannelPool()` was called (pooled channels from before disconnect are no longer returned)

---

### F-R1D3-02 [S3 / P1 / Missing coverage] Publisher.Publish -- AcquireChannel failure and ExchangeDeclare failure paths untested

**Files**: `src/adapters/rabbitmq/publisher.go` lines 33-41

**Evidence**:
- Line 33: `ch, err := p.conn.AcquireChannel()` -- if this returns error, the early return at line 35 is never tested. Existing tests (`TestPublisher_Publish_*`) always succeed at channel acquisition.
- Line 39: `ch.ExchangeDeclare(...)` -- if this returns error, line 40 is never reached by any test.
- Line 63-65: `confirm, ok := <-confirmCh; if !ok` -- the confirm-channel-closed path (ok==false) is not tested. `TestPublisher_Publish_ConfirmTimeout` tests the timeout path, not the closed-channel path.

**Impact**: Three error branches in Publisher.Publish have zero coverage, creating potential for silent regressions in error wrapping or error code assignment.

**Recommendation**: Add tests where:
1. `mockConnection.chanErr` is set (AcquireChannel failure)
2. `mockChannel.ExchangeDeclare` returns error (override the mock to support this)
3. The `notifyPublishCh` is closed before a confirmation is sent (ok==false path)

Note: the mock's `ExchangeDeclare` always returns nil. The mock needs to support configurable error returns for this function.

---

### F-R1D3-03 [S3 / P1 / Missing coverage] Subscriber setup error paths (Qos/ExchangeDeclare/QueueDeclare/QueueBind/Consume) untested

**Files**: `src/adapters/rabbitmq/subscriber.go` lines 99-124

**Evidence**: The `Subscribe` method has 5 sequential setup calls that can each fail:
- Line 100: `ch.Qos(...)` -- mock always returns nil (line 82 of test file; note: no `qosErr` field)
- Line 105: `ch.ExchangeDeclare(...)` -- mock always returns nil (line 104 of test file)
- Line 110: `ch.QueueDeclare(...)` -- mock always returns nil (line 111 of test file)
- Line 115: `ch.QueueBind(...)` -- mock always returns nil (line 118 of test file)
- Line 121: `ch.Consume(...)` -- mock supports error via `consumeErr`, but no test sets it

None of these error branches are tested. Each produces a different `errcode.Wrap` with different messages, and their correctness is unverified.

**Impact**: If any setup call's error wrapping or error code is wrong, it will not be caught.

**Recommendation**: Add 5 tests (or a table-driven test) setting each mock error field individually and asserting the correct error code + message substring.

---

### F-R1D3-04 [S3 / P2 / Missing coverage] Subscriber ACK/NACK error paths only logged, never verified

**Files**: `src/adapters/rabbitmq/subscriber.go` lines 183-187, 199-205, 212-216

**Evidence**: In `processDelivery()`:
- Line 183: `ch.Nack(...)` error during unmarshal failure -- if `nackErr != nil`, only logged. No test sets `nackErr` on the mock channel.
- Line 200-205: `ch.Nack(...)` error during handler failure -- same, never tested.
- Line 212-216: `ch.Ack(...)` error after successful handler -- no test sets `ackErr` on the mock channel.

While these are log-only paths (no functional impact beyond logging), they are code branches that contribute to coverage metrics and could mask bugs if the error handling is changed in the future.

**Recommendation**: Add tests where `mockChannel.ackErr` and `mockChannel.nackErr` are set, verifying the function does not panic and still completes normally.

---

### F-R1D3-05 [S3 / P2 / Missing coverage] ConsumerBase.deadLetter -- json.Marshal failure and publisher.Publish failure untested

**Files**: `src/adapters/rabbitmq/consumer_base.go` lines 196-210

**Evidence**:
- Line 196-201: `json.Marshal(dlqEntry)` failure path -- returns early after logging. Not tested (json.Marshal of `outbox.Entry` is unlikely to fail in practice, but the branch exists).
- Line 205-210: `cb.publisher.Publish(...)` failure path -- returns early after logging. The mock publisher supports error injection (`mockPublisher.err`), but no test exercises this path.

**Impact**: If DLQ publish fails silently, messages are lost. While the current code only logs, there is no test verifying this logging happens or that the function returns gracefully.

**Recommendation**: Add a test where `mockPublisher.err` is set, trigger a DLQ path (e.g., permanent error), and verify the wrapped handler still returns nil (message ACK'd despite DLQ failure). Consider whether DLQ publish failure should surface an error rather than silently drop -- this is a design question, not just a coverage gap.

---

### F-R1D3-06 [S3 / P2 / Missing coverage] No concurrency tests for channel pool or multi-consumer scenarios

**Files**: `src/adapters/rabbitmq/connection.go` lines 285-319

**Evidence**: `AcquireChannel()` and `ReleaseChannel()` use a buffered channel as a pool, which is inherently concurrent-safe. However, there is no test that exercises concurrent access:
- No test calls `AcquireChannel()` from multiple goroutines simultaneously
- No test verifies the pool behaves correctly under contention
- No test verifies `drainChannelPool()` is safe while `AcquireChannel` / `ReleaseChannel` run concurrently

The `Subscriber.Subscribe` uses `sync.Mutex` for `s.channels` and `sync.WaitGroup` for in-flight tracking, but no multi-subscriber test exists.

**Recommendation**: Add `TestConnection_AcquireRelease_Concurrent` using multiple goroutines with `sync.WaitGroup` and `-race` flag verification. Add `TestSubscriber_MultipleDeliveries_Concurrent` exercising parallel delivery processing.

---

### F-R1D3-07 [S3 / P1 / Missing coverage] Subscriber.Close with in-flight messages and shutdown timeout path untested

**Files**: `src/adapters/rabbitmq/subscriber.go` lines 221-256

**Evidence**: `Close()` has two distinct paths after `close(s.closeCh)`:
1. **Graceful**: `s.wg.Wait()` completes before `s.config.ShutdownTimeout` (line 236)
2. **Timeout**: `s.wg.Wait()` does NOT complete, `time.After` fires (line 238)

Neither path is tested with in-flight messages. `TestSubscriber_Close_Idempotent` tests the double-close guard only, with no active subscriptions.

**Impact**: The graceful shutdown mechanism, which is critical for message processing reliability, is untested. A regression could cause message loss or goroutine leaks.

**Recommendation**: Add a test that:
1. Starts a subscriber with a slow handler (e.g., blocks for 500ms)
2. Sends a delivery to the mock channel
3. Calls `Close()` while the handler is still running
4. Asserts the close waits for the handler to complete (graceful path)

Add a second test with `ShutdownTimeout: 10ms` and a handler that blocks indefinitely, verifying Close returns within the timeout.

---

### F-R1D3-08 [S3 / P2 / Test quality] Mock channel does not support configurable errors for ExchangeDeclare, QueueDeclare, QueueBind, Qos

**Files**: `src/adapters/rabbitmq/rabbitmq_test.go` lines 104-123

**Evidence**: The `mockChannel` struct has error fields for `publishErr`, `confirmErr`, `consumeErr`, `ackErr`, `nackErr`, `closeErr` -- but NOT for:
- `ExchangeDeclare` (line 104: always returns nil)
- `QueueDeclare` (line 111: always returns `amqp.Queue{Name: name}, nil`)
- `QueueBind` (line 118: always returns nil)
- `Qos` (line 82: always returns nil)

This mock limitation is the root cause of F-R1D3-02 and F-R1D3-03.

**Recommendation**: Add `exchangeDeclareErr`, `queueDeclareErr`, `queueBindErr`, `qosErr` fields to `mockChannel` and use them in the respective methods.

---

### F-R1D3-09 [S3 / P2 / Integration test quality] Integration test skip guard uses build tag but no runtime skip

**Files**: `src/adapters/rabbitmq/integration_test.go` line 1

**Evidence**: The file uses `//go:build integration` (line 1) which requires `-tags integration` to compile. This is acceptable. However, the CLAUDE.md eventbus spec mentions "skip guard" and the standard Go testcontainers pattern includes `testing.Short()` check as a secondary guard:

```go
if testing.Short() {
    t.Skip("skipping integration test in short mode")
}
```

This is missing from all 5 integration tests.

**Recommendation**: Add `t.Skip` guard for `testing.Short()` as a defense-in-depth measure. Low priority since the build tag already gates compilation.

---

### F-R1D3-10 [S3 / P2 / Integration test quality] TestIntegration_ConnectionRecovery does not test actual recovery

**Files**: `src/adapters/rabbitmq/integration_test.go` lines 238-250

**Evidence**: The test name is `TestIntegration_ConnectionRecovery`, but it only:
1. Checks `Health()` (line 247)
2. Acquires and releases a channel (lines 249-250)

It does NOT:
- Kill the RabbitMQ connection or container
- Wait for reconnect
- Verify Health transitions from error to ok
- Verify AcquireChannel works after recovery

The test name is misleading; it is actually a "connection works" test, not a "recovery" test.

**Impact**: The integration test suite has no actual reconnection/recovery coverage.

**Recommendation**: Either rename to `TestIntegration_ConnectionBasic` or implement actual recovery testing by stopping/restarting the container.

---

### F-R1D3-11 [S3 / P2 / Missing coverage] Publisher.Publish -- AcquireChannel failure path (connection nil)

**Files**: `src/adapters/rabbitmq/publisher.go` line 33, `src/adapters/rabbitmq/connection.go` line 297

**Evidence**: When `conn` is nil (line 297) or `conn.IsClosed()` is true (line 297), `AcquireChannel` returns an error. This path is tested for `Connection` directly (`TestConnection_AcquireChannel_ConnectionClosed`) but NOT through `Publisher.Publish()`, meaning the errcode wrapping at `publisher.go:35` (`ErrAdapterAMQPPublish` wrapping `ErrAdapterAMQPConnect`) is unverified.

**Recommendation**: Add `TestPublisher_Publish_AcquireChannelFails` where mockConn.chanErr is set before calling Publish.

---

## 4. Summary

### Coverage assessment

| Area | Target | Estimated | Verdict |
|------|--------|-----------|---------|
| Overall unit coverage | >= 80% | ~70-75% | **BELOW TARGET** |
| ConsumerBase (TryProcess/DLQ/retry/permanent) | critical | ~85% | Adequate |
| Publisher (publish/confirm) | critical | ~80% | Borderline |
| Connection (reconnect) | critical | ~55% | **SIGNIFICANT GAP** |
| Subscriber (setup errors, shutdown) | critical | ~60% | **SIGNIFICANT GAP** |
| Concurrency | expected | 0% | **MISSING ENTIRELY** |
| Integration (testcontainers) | present | present | Present but recovery test is misleading |

### Findings summary

| ID | Severity | Category | Description |
|----|----------|----------|-------------|
| F-R1D3-01 | P1 | Missing coverage | reconnectLoop/reconnectWithBackoff zero coverage |
| F-R1D3-02 | P1 | Missing coverage | Publisher error paths (AcquireChannel, ExchangeDeclare, confirm-closed) |
| F-R1D3-03 | P1 | Missing coverage | Subscriber 5 setup error paths untested |
| F-R1D3-04 | P2 | Missing coverage | ACK/NACK error logging paths untested |
| F-R1D3-05 | P2 | Missing coverage | deadLetter marshal/publish failure paths |
| F-R1D3-06 | P2 | Missing coverage | No concurrency tests for channel pool |
| F-R1D3-07 | P1 | Missing coverage | Subscriber.Close in-flight + shutdown timeout untested |
| F-R1D3-08 | P2 | Test quality | Mock channel incomplete -- 4 methods lack error injection |
| F-R1D3-09 | P2 | Integration quality | Missing testing.Short() skip guard |
| F-R1D3-10 | P2 | Integration quality | ConnectionRecovery test does not test recovery |
| F-R1D3-11 | P2 | Missing coverage | Publisher AcquireChannel failure through Publish |

**P0**: 0 | **P1**: 4 | **P2**: 7

### Priority fix order

1. **F-R1D3-08 first** (mock enhancement) -- unblocks F-R1D3-02 and F-R1D3-03
2. **F-R1D3-01** (reconnect tests) -- highest-risk gap
3. **F-R1D3-07** (shutdown tests) -- second-highest-risk gap
4. **F-R1D3-02 + F-R1D3-03** (publisher + subscriber error paths) -- breadth coverage
5. **F-R1D3-06** (concurrency) -- race condition defense
6. Remaining P2s as time permits

### What is done well

- Mock infrastructure is well-structured with mutex protection and clean separation of concerns
- ConsumerBase tests are thorough: TryProcess, already-processed, transient retry, retry-exhausted-DLQ, permanent-error-DLQ, custom DLQ topic, idempotency-check-error fail-open, context cancellation during retry -- these cover the primary ConsumerBase specification requirements
- Integration tests use testcontainers with proper cleanup functions
- Test names follow Go conventions and are descriptive
- Table-driven tests used where appropriate (backoff, sanitizeURL, config defaults)
- Interface compliance checks via compile-time `var _ = (*Type)(nil)` pattern
- DLQ metadata assertions (x-death-reason, x-death-topic, x-death-consumer-group, x-death-retry-count) are thorough
