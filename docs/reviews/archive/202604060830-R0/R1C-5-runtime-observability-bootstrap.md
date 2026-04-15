# R1C-5: runtime/observability + runtime/bootstrap Review

| Field | Value |
|---|---|
| Reviewer | R1C-5 Agent (Seats 1/3/4/5 composite) |
| Scope | `runtime/observability/{logging,metrics,tracing}/`, `runtime/bootstrap/` |
| Baseline commit | ce03ba1 (develop HEAD) |
| Date | 2026-04-06 |
| LOC reviewed | ~780 (bootstrap 357, logging 105, metrics 177, tracing 116, tests ~370) |

---

## Summary

The observability and bootstrap modules form the lifecycle backbone of GoCell. The code is generally well-structured with clean interfaces, proper slog usage, and good test coverage for the observability sub-packages. However, the bootstrap `Run()` function has high cognitive complexity and several integration gaps. The metrics package contains a duplicated ResponseWriter wrapper and dead code. Tracing middleware exists but is never wired into the default middleware chain.

---

## Findings

### F-01 [Seat 5: DX/Maintainability] P1 -- bootstrap.Run() cognitive complexity exceeds limit

**File:** `/Users/shengming/Documents/code/gocell/runtime/bootstrap/bootstrap.go` lines 158-356

**Evidence:** The `Run()` function is 198 lines long with 10 sequential lifecycle steps, nested closures (rollback, watcher callback), multiple select branches, and inline teardown registration. SonarCloud flags CC=57 against a limit of 15.

**Description:** The function handles config loading, config watcher setup, publisher/subscriber init, assembly start, router build, HTTP route registration, event subscription registration, HTTP server start, worker start, signal wait, and orderly shutdown -- all in a single function body. This makes it difficult to test individual phases and hard to reason about.

**Recommendation:** Extract sub-functions for each lifecycle phase:
- `loadConfig() (config.Config, []teardownFn, error)`
- `initEventBus() (outbox.Publisher, outbox.Subscriber, []teardownFn)`
- `startAssembly(ctx, cfg) (*assembly.CoreAssembly, []teardownFn, error)`
- `buildAndServeHTTP(asm) (*http.Server, []teardownFn, error)`
- `startWorkers(ctx) ([]teardownFn, error)`
- `awaitShutdown(ctx, teardowns) error`

Each function returns its own teardown closures. This would reduce Run() to ~30 lines of sequential calls and bring CC well below 15.

---

### F-02 [Seat 3: Test/Regression] P1 -- No integration test for bootstrap.Run() happy path

**File:** `/Users/shengming/Documents/code/gocell/runtime/bootstrap/bootstrap_test.go`

**Evidence:** The test file has 11 test functions. `TestBootstrap_RunContextCancel` (line 137) immediately cancels the context and does not assert on the result (`_ = err`). The remaining tests only exercise option setters and assembly internals -- they never test a complete Run() that successfully starts and shuts down.

**Description:** `WithListener` exists precisely for test-friendly port allocation, but no test uses it. A minimal integration test should: (1) create a net.Listener on `:0`, (2) register a test cell, (3) Run() with a context that cancels after a short delay, (4) assert no error. Without this, regressions in the startup/shutdown sequence are invisible.

**Recommendation:** Add a `TestBootstrap_RunHappyPath` test:
```go
func TestBootstrap_RunHappyPath(t *testing.T) {
    ln, err := net.Listen("tcp", "127.0.0.1:0")
    require.NoError(t, err)
    
    asm := assembly.New(assembly.Config{ID: "test", DurabilityMode: cell.DurabilityDemo})
    require.NoError(t, asm.Register(newTestCell("cell-1")))
    
    ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
    defer cancel()
    
    b := New(
        WithAssembly(asm),
        WithListener(ln),
        WithShutdownTimeout(time.Second),
    )
    err = b.Run(ctx)
    require.NoError(t, err)
}
```

---

### F-03 [Seat 5: DX/Maintainability] P1 -- metrics.metricsRecorder duplicates pkg/httputil.StatusRecorder

**File:** `/Users/shengming/Documents/code/gocell/runtime/observability/metrics/metrics.go` lines 148-169

**Evidence:** `metricsRecorder` (lines 148-169) implements `WriteHeader` and `Write` to capture status codes -- the exact same responsibility as `pkg/httputil.StatusRecorder` (lines 66-83 in `response.go`). The tracing middleware already uses `httputil.NewStatusRecorder`.

**Description:** Two independent ResponseWriter wrappers that do the same thing create a maintenance burden: a bug fix in one will not propagate to the other. The metrics `Write` method also sets `wroteHeader = true` but `StatusRecorder` does not, which means their behaviors subtly diverge.

**Recommendation:** Replace `metricsRecorder` with `httputil.StatusRecorder` in the metrics `Middleware` function:
```go
rec := httputil.NewStatusRecorder(w)
next.ServeHTTP(rec, r)
collector.RecordRequest(r.Method, r.URL.Path, rec.Status, duration)
```

---

### F-04 [Seat 1: Architecture] P1 -- Tracing middleware exists but is never wired into router or bootstrap

**File:** `/Users/shengming/Documents/code/gocell/runtime/observability/tracing/tracing.go` lines 92-108
**File:** `/Users/shengming/Documents/code/gocell/runtime/http/router/router.go` lines 71-79

**Evidence:** `tracing.Middleware(tracer)` is a fully implemented HTTP middleware (line 95). However, `router.New()` (line 62) only wires `RequestID, RealIP, Recovery, AccessLog, SecurityHeaders, BodyLimit` and optionally metrics middleware. There is no `WithTracer` option on the router. A grep for `tracing.Middleware` or `tracing.NewTracer` across all of `runtime/` returns zero results.

**Description:** Without tracing middleware in the default chain, trace_id and span_id are never injected into the request context for production traffic. The logging handler (`contextHandler`) correctly extracts these from context, but they will always be empty. This renders the entire tracing package effectively dead in production.

**Recommendation:** Add a `WithTracer(t tracing.Tracer)` option to `router.Router` and wire `tracing.Middleware(tracer)` early in the default middleware chain (before AccessLog, so logs include trace_id).

---

### F-05 [Seat 5: DX/Maintainability] P2 -- Dead code: `metricsText` function never called

**File:** `/Users/shengming/Documents/code/gocell/runtime/observability/metrics/metrics.go` lines 173-176

**Evidence:** `metricsText` is an unexported function. A project-wide grep for `metricsText` returns only its definition (line 173) and its doc comment (line 171). It is never called from any code or test.

**Description:** Dead code increases cognitive load and suggests incomplete implementation. The comment says "debugging/testing" but no test uses it.

**Recommendation:** Remove the function, or if the intent is to provide a debug helper, export it as `MetricsText` and add a test.

---

### F-06 [Seat 5: DX/Maintainability] P2 -- metrics.Handler() uses fragile Sscanf to reverse-parse composite key

**File:** `/Users/shengming/Documents/code/gocell/runtime/observability/metrics/metrics.go` line 114

**Evidence:**
```go
_, _ = fmt.Sscanf(k, "%s %s %d", &method, &path, &status)
```
The key format is `"METHOD /path STATUS"` (built by `metricKey` on line 47). The Sscanf return values (count and error) are both discarded. If a path contained a space (e.g., encoded incorrectly), parsing would silently produce wrong values.

**Description:** The code constructs a composite string key and then parses it back, which is fragile. The error from Sscanf is silenced, violating the project rule "must explicitly handle or record errors."

**Recommendation:** Use a struct key (`metricEntry{Method, Path, Status}`) as the map key (requires a string representation for map key, e.g., via a helper). Alternatively, store entries as a slice of structs and remove the need for parsing entirely. At minimum, check the Sscanf return value and log on failure.

---

### F-07 [Seat 3: Test/Regression] P2 -- metrics.Handler() JSON encoding error silenced

**File:** `/Users/shengming/Documents/code/gocell/runtime/observability/metrics/metrics.go` line 125

**Evidence:**
```go
_ = json.NewEncoder(w).Encode(map[string]any{
```

**Description:** The JSON encode error is discarded. While unlikely to fail for simple map structures, silencing the error prevents detection of writer failures (e.g., client disconnect). The project rule says "must explicitly handle or record errors."

**Recommendation:** Log the error at Debug level if non-nil:
```go
if err := json.NewEncoder(w).Encode(...); err != nil {
    slog.Debug("metrics: failed to encode response", slog.Any("error", err))
}
```

---

### F-08 [Seat 3: Test/Regression] P2 -- tracing.generateID silences rand.Read error

**File:** `/Users/shengming/Documents/code/gocell/runtime/observability/tracing/tracing.go` line 113

**Evidence:**
```go
_, _ = rand.Read(buf)
```

**Description:** `crypto/rand.Read` returns an error when the OS entropy source fails. On Go 1.24+ `rand.Read` never returns an error (the function panics instead), so this is technically safe for the current Go version (1.25 per go.mod). However, the double underscore suppression pattern is a code smell and could mislead future readers.

**Recommendation:** Since Go 1.25 guarantees no error from `crypto/rand.Read`, simplify to:
```go
rand.Read(buf) // Go 1.25: guaranteed no error (panics on failure)
```
Or equivalently use `crypto/rand.Read` with a comment explaining the guarantee.

---

### F-09 [Seat 1: Architecture] P2 -- bootstrap imports runtime/eventbus concrete type in WithEventBus

**File:** `/Users/shengming/Documents/code/gocell/runtime/bootstrap/bootstrap.go` lines 87-92

**Evidence:**
```go
func WithEventBus(eb *eventbus.InMemoryEventBus) Option {
```
This option accepts a concrete `*eventbus.InMemoryEventBus`, creating a hard coupling to the in-memory implementation. The newer `WithPublisher` and `WithSubscriber` correctly accept interfaces (`outbox.Publisher` and `outbox.Subscriber`).

**Description:** The function is marked `Deprecated` (line 83), which is correct. However, it is still used in `bootstrap_test.go` line 39. Since the Deprecated annotation exists, the priority is low, but the test should migrate to the interface-based options to validate the recommended path.

**Recommendation:** Update `TestNew_WithOptions` to use `WithPublisher(eb)` and `WithSubscriber(eb)` instead of `WithEventBus(eb)`. Consider removing `WithEventBus` in the next breaking change window.

---

### F-10 [Seat 5: DX/Maintainability] P2 -- Snapshot field name DurationSumsMs stores microseconds, displays milliseconds

**File:** `/Users/shengming/Documents/code/gocell/runtime/observability/metrics/metrics.go` lines 28, 89

**Evidence:**
```go
// line 28
DurationSumsMs   map[string]int64
// line 89
snap.DurationSumsMs[k] = v.Load() / 1000 // microseconds -> milliseconds
```
Internally, `durations` stores microseconds (line 73: `int64(durationSeconds * 1e6)`). The `Snapshot()` method converts to milliseconds on read. The field name `DurationSumsMs` correctly indicates milliseconds, but the internal storage in microseconds and the integer division truncation means values under 1ms are silently rounded to 0.

**Description:** For sub-millisecond request durations (common in health checks), the reported duration will be 0. This could mislead users into thinking a request took no time.

**Recommendation:** Either store and report microseconds (`DurationSumsUs`) for precision, or use `float64` milliseconds to preserve sub-ms detail. Document the unit explicitly.

---

### F-11 [Seat 1: Architecture] P2 -- router.WithMetricsCollector accepts concrete InMemoryCollector, not Collector interface

**File:** `/Users/shengming/Documents/code/gocell/runtime/http/router/router.go` line 36

**Evidence:**
```go
func WithMetricsCollector(c *metrics.InMemoryCollector) Option {
```
The `Collector` interface exists (metrics.go line 18) but `WithMetricsCollector` requires the concrete `*InMemoryCollector`. This prevents passing a production Prometheus adapter without also wrapping it.

**Description:** The pattern violates the GoCell principle of interface-based decoupling. The middleware `metrics.Middleware(collector Collector)` (line 133) correctly takes the interface, but the router's option does not.

**Recommendation:** Split into two concerns: (1) `WithMetricsMiddleware(c metrics.Collector)` for the middleware, (2) `WithMetricsHandler(h http.Handler)` for the `/metrics` endpoint. This allows production adapters (e.g., promhttp.Handler) to be passed without depending on `InMemoryCollector`.

---

### F-12 [Seat 3: Test/Regression] P2 -- logging tests do not cover WithGroup

**File:** `/Users/shengming/Documents/code/gocell/runtime/observability/logging/logging_test.go`

**Evidence:** The `contextHandler.WithGroup` method is implemented (logging.go line 84) but no test covers it. The existing tests cover `WithAttrs` (line 100) but not `WithGroup`.

**Description:** `WithGroup` is part of the `slog.Handler` interface contract. Without a test, a regression in group namespace propagation would go undetected.

**Recommendation:** Add a `TestContextHandler_WithGroup` test that verifies context attributes appear correctly under a named group.

---

### F-13 [Seat 3: Test/Regression] P2 -- No concurrency test for InMemoryCollector

**File:** `/Users/shengming/Documents/code/gocell/runtime/observability/metrics/metrics_test.go`

**Evidence:** The `InMemoryCollector` uses `sync.RWMutex` with a double-check locking pattern and `atomic.Int64` (metrics.go lines 51-74), indicating it is designed for concurrent use. However, no test exercises concurrent access (e.g., `t.Parallel()` with multiple goroutines calling `RecordRequest`).

**Description:** The double-check locking pattern (RLock, check, RUnlock, Lock, re-check) is a common source of subtle bugs. Without a race-detected concurrent test, data races may exist undetected.

**Recommendation:** Add a `TestInMemoryCollector_Concurrent` test using `sync.WaitGroup` with multiple goroutines and run with `-race`.

---

## Findings Summary

| ID | Seat | Severity | Category | File(s) |
|---|---|---|---|---|
| F-01 | S5 DX | P1 | Cognitive complexity | bootstrap.go |
| F-02 | S3 Test | P1 | Missing integration test | bootstrap_test.go |
| F-03 | S5 DX | P1 | Code duplication | metrics.go, httputil/response.go |
| F-04 | S1 Arch | P1 | Dead integration | tracing.go, router.go |
| F-05 | S5 DX | P2 | Dead code | metrics.go |
| F-06 | S5 DX | P2 | Fragile parsing | metrics.go |
| F-07 | S3 Test | P2 | Error silencing | metrics.go |
| F-08 | S3 Test | P2 | Error silencing | tracing.go |
| F-09 | S1 Arch | P2 | Concrete coupling | bootstrap.go |
| F-10 | S5 DX | P2 | Misleading precision | metrics.go |
| F-11 | S1 Arch | P2 | Concrete coupling | router.go |
| F-12 | S3 Test | P2 | Missing test | logging_test.go |
| F-13 | S3 Test | P2 | Missing test | metrics_test.go |

**P0: 0 | P1: 4 | P2: 9 | Total: 13**

---

## Compliance Checklist

| Check | Status | Notes |
|---|---|---|
| kernel/ imports from runtime/adapters/cells | PASS | No violations |
| cells/ imports adapters/ | N/A | Not in scope |
| Cross-cell direct import | N/A | Not in scope |
| CUD consistency level annotations | N/A | No CUD operations in these modules |
| runtime/ imports cells/ or adapters/ | PASS | Only imports kernel/ and pkg/ |
| bare errors.New | PASS | None found |
| fmt.Println / log.Printf | PASS | None found |
| slog structured fields | PASS | All slog calls include structured fields |
| Bare slog.Error("failed") | PASS | All Error calls include slog.Any("error", ...) |
| Debug dump of request body | PASS | No Debug-level logging in these modules |
| ref: tag in commit messages | INFO | bootstrap.go has ref: comments inline (lines 6-8, 66-67, 144); no commit grep performed |

---

## Positive Observations

1. **Clean interface design**: `Tracer`, `Span`, and `Collector` interfaces are well-defined and follow Go idioms (small, single-purpose).
2. **Context propagation**: The `contextHandler` correctly enriches every log record with trace/span/request/cell IDs from context -- excellent correlation design.
3. **LIFO teardown**: Bootstrap's rollback and shutdown both use reverse-order teardown, matching the uber-go/fx reference pattern.
4. **Option pattern consistency**: All constructors use functional options, consistent across bootstrap, router, and shutdown.
5. **Double-close prevention**: Bootstrap correctly checks `any(pub) != any(sub)` before registering a separate publisher teardown (line 227).
6. **ReadHeaderTimeout**: HTTP server sets `ReadHeaderTimeout: 10 * time.Second` (line 279), preventing Slowloris attacks.
