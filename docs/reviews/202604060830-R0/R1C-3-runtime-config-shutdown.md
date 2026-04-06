# R1C-3: runtime/config + runtime/shutdown Review

| Field | Value |
|-------|-------|
| Reviewer | R1C-3 (multi-seat: S1 Architecture, S3 Test, S4 Ops, S5 DX) |
| Scope | `src/runtime/config/` (~342 LOC), `src/runtime/shutdown/` (~99 LOC) |
| Baseline commit | ce03ba1 (develop HEAD) |
| Date | 2026-04-06 |

---

## Files Reviewed

| File | LOC (approx) | Purpose |
|------|-------------|---------|
| `src/runtime/config/config.go` | 216 | Config interface, YAML+env loading, Reload, flatten/setNested |
| `src/runtime/config/watcher.go` | 114 | fsnotify file watcher with callback dispatch |
| `src/runtime/config/doc.go` | 14 | Package godoc |
| `src/runtime/config/config_test.go` | 189 | Tests for Load/Scan/Keys/Reload |
| `src/runtime/config/watcher_test.go` | 90 | Tests for Watcher lifecycle |
| `src/runtime/shutdown/shutdown.go` | 97 | Graceful shutdown Manager with LIFO hooks |
| `src/runtime/shutdown/shutdown_test.go` | 137 | Tests for LIFO, error continuation, signal, timeout |
| `src/runtime/shutdown/doc.go` | 3 | Package godoc |

---

## Summary

Both modules are well-structured and compact. `runtime/config` provides a clean Config interface with YAML+env loading, hot-reload via fsnotify, and proper RWMutex protection for concurrent reads/writes. `runtime/shutdown` implements LIFO hook execution with signal handling and timeout. The integration with `runtime/bootstrap` is coherent.

Key strengths:
- Config uses `sync.RWMutex` correctly for concurrent access
- Reload is atomic: new data prepared in local vars, then swapped under write lock
- Watcher copies callback slice before invoking, avoiding lock-during-callback
- Watcher recovers panics in callbacks
- Shutdown LIFO order is correct and tested
- All hooks execute even if one fails
- Double-close safety on Watcher
- `ref:` tag present in config.go header (go-micro reference)

---

## Findings

### F01 -- [S1 Architecture] P2 -- Watcher.Close() calls fsnotify.Close() on every invocation

**File:** `src/runtime/config/watcher.go:105-113`

**Evidence:**
```go
func (w *Watcher) Close() error {
    select {
    case <-w.done:
        // Already closed.
    default:
        close(w.done)
    }
    return w.watcher.Close() // called EVERY time, even after done is already closed
}
```

**Description:** The `done` channel is properly guarded against double-close, but `w.watcher.Close()` is called unconditionally on every `Close()` invocation. While `fsnotify.Watcher.Close()` is documented as safe to call multiple times (it returns nil), this is relying on an implementation detail. Logically, the second branch should return early.

**Severity:** P2

**Suggestion:**
```go
func (w *Watcher) Close() error {
    select {
    case <-w.done:
        return nil // Already closed
    default:
        close(w.done)
    }
    return w.watcher.Close()
}
```

**Status:** OPEN

---

### F02 -- [S1 Architecture] P2 -- Close() has TOCTOU race on `done` channel

**File:** `src/runtime/config/watcher.go:105-113`

**Evidence:**
```go
func (w *Watcher) Close() error {
    select {
    case <-w.done:
        // Already closed.
    default:
        close(w.done)   // Two goroutines can both reach this line concurrently
    }
    ...
```

**Description:** If two goroutines call `Close()` concurrently, both could enter the `default` branch and both attempt `close(w.done)`, causing a panic on double-close of a channel. The `select` on a closed channel is not atomic with the `close()` call. This is plausible in practice: `StartWithContext` launches a goroutine that calls `Close()` on context cancellation, while the caller may also call `Close()` directly.

**Severity:** P1

**Suggestion:** Use `sync.Once`:
```go
type Watcher struct {
    // ...
    closeOnce sync.Once
    closeErr  error
}

func (w *Watcher) Close() error {
    w.closeOnce.Do(func() {
        close(w.done)
        w.closeErr = w.watcher.Close()
    })
    return w.closeErr
}
```

**Status:** OPEN

---

### F03 -- [S3 Test] P1 -- No concurrent read/write test for Config

**File:** `src/runtime/config/config_test.go`

**Evidence:** All test functions run sequentially. No test calls `Get()` from multiple goroutines while `Reload()` runs.

**Description:** The `config` struct uses `sync.RWMutex` to protect concurrent access, but there is no test exercising this under the race detector. A concurrent test with `-race` would validate the correctness of the locking. Given that `Reload` is designed to be called from watcher callbacks (different goroutine), this is a real usage scenario.

**Severity:** P1

**Suggestion:** Add a test like:
```go
func TestConfig_ConcurrentReadReload(t *testing.T) {
    // Create config, then spawn N goroutines doing Get()
    // while main goroutine calls Reload() in a loop
    // Run with -race flag
}
```

**Status:** OPEN

---

### F04 -- [S3 Test] P2 -- No test for Watcher callback panic recovery

**File:** `src/runtime/config/watcher_test.go`

**Evidence:** `watcher.go:83-87` has panic recovery, but no test triggers a panicking callback.

**Description:** The watcher has a `defer recover()` guard in the callback dispatch loop (line 83-87), which is good defensive programming. However, there is no test verifying that (a) a panicking callback does not crash the watcher loop, and (b) subsequent callbacks still fire.

**Severity:** P2

**Suggestion:** Add a test with a panicking first callback and a normal second callback, assert both are dispatched.

**Status:** OPEN

---

### F05 -- [S5 DX] P2 -- Scan() does not reflect env overrides in structured output

**File:** `src/runtime/config/config.go:80-93`

**Evidence:**
```go
func (c *config) Scan(dest interface{}) error {
    c.mu.RLock()
    defer c.mu.RUnlock()
    b, err := yaml.Marshal(c.raw)
    // ...
    if err := yaml.Unmarshal(b, dest); err != nil {
```

The `raw` map is updated by `applyEnv` via `setNested`, so env overrides ARE reflected in Scan. However, the type is always `string` from env vars (line 175: `data[key] = v` where `v` is a string), while YAML parsing may produce `int`, `bool`, etc.

**Description:** When an env var overrides a YAML key like `server.port: 8080`, the `raw` map will contain the string `"9090"` instead of the integer `9090`. `Scan` then marshals this to YAML and unmarshals it into a struct. Since YAML unmarshaling handles string-to-int conversion, this works in most cases. However, it can produce unexpected behavior for boolean fields: `"true"` (string) vs `true` (bool) may not always round-trip identically depending on the struct tag and YAML library version.

The `Get()` return type also changes from `int` to `string` after env override, which is a semantic surprise for callers (see `TestLoad_EnvOverridesYAML` where the assertion checks for string `"9090"` not int `9090`).

**Severity:** P2

**Suggestion:** Document this type coercion behavior clearly in the Config interface godoc, or consider type-aware env parsing (detect numeric/bool strings).

**Status:** OPEN

---

### F06 -- [S1 Architecture] P2 -- shutdown.Manager.Register is not goroutine-safe

**File:** `src/runtime/shutdown/shutdown.go:48-49`

**Evidence:**
```go
func (m *Manager) Register(h Hook) {
    m.hooks = append(m.hooks, h)
}
```

**Description:** `Register` directly appends to the `hooks` slice without any synchronization. If `Register` is called from multiple goroutines (e.g., during parallel cell initialization), this is a data race. In practice, the current bootstrap code registers hooks sequentially, so this is not an active bug, but it violates the principle of making concurrent-use types safe by default.

**Severity:** P2

**Suggestion:** Add a `sync.Mutex` to protect `Register` and `runHooks`, or document clearly that `Register` must not be called concurrently or after `Wait`/`Shutdown`.

**Status:** OPEN

---

### F07 -- [S3 Test] P2 -- shutdown.Manager timeout test does not assert DeadlineExceeded

**File:** `src/runtime/shutdown/shutdown_test.go:59-70`

**Evidence:**
```go
func TestManager_Shutdown_Timeout(t *testing.T) {
    m := New(WithTimeout(50 * time.Millisecond))
    m.Register(func(ctx context.Context) error {
        <-ctx.Done()
        return ctx.Err()
    })
    err := m.Shutdown()
    assert.Error(t, err)  // only checks non-nil, not the specific error
}
```

**Description:** The test only asserts `err != nil` but does not verify that the error is `context.DeadlineExceeded`. This weakens the test -- it would pass even if the error source changed to something unrelated.

**Severity:** P2

**Suggestion:** `assert.ErrorIs(t, err, context.DeadlineExceeded)`

**Status:** OPEN

---

### F08 -- [S5 DX] P2 -- Config.Get returns `any` with no type helper

**File:** `src/runtime/config/config.go:24-26`

**Evidence:**
```go
type Config interface {
    Get(key string) any
    Scan(dest interface{}) error
    Keys() []string
}
```

**Description:** `Get` returns `any`, requiring callers to type-assert every value. The go-micro reference (primary framework) provides typed helpers like `config.Get("key").String("")`, `config.Get("key").Int(0)`. For DX, a `GetString(key, default)` / `GetInt(key, default)` pattern would reduce boilerplate and prevent runtime panics from bad assertions.

**Severity:** P2

**Suggestion:** Add typed convenience methods (at least `GetString`, `GetInt`, `GetBool`, `GetDuration`) that return the value or a default, matching the go-micro pattern referenced in the file header.

**Status:** OPEN

---

### F09 -- [S1 Architecture] P2 -- No validation hook in Reload path

**File:** `src/runtime/config/config.go:109-129`

**Evidence:**
```go
func (c *config) Reload(yamlPath string, envPrefix string) error {
    newData := make(map[string]any)
    newRaw := make(map[string]any)
    if yamlPath != "" {
        raw, err := readYAML(yamlPath)
        // ...
        newRaw = raw
        flatten("", raw, newData)
    }
    applyEnv(envPrefix, newData, newRaw)
    c.mu.Lock()
    c.data = newData
    c.raw = newRaw
    c.mu.Unlock()
    return nil
}
```

**Description:** `Reload` applies the new configuration unconditionally as long as the YAML is syntactically valid. There is no validation callback to reject semantically invalid configurations (e.g., port out of range, missing required keys). The CLAUDE.md and framework comparison reference go-micro which supports validation on reload. An invalid config file change on disk would be silently applied.

**Severity:** P2

**Suggestion:** Add an optional `ValidateFunc(map[string]any) error` field to `config` (or a `WithValidator` option for `Load`). If validation fails on reload, log an error and keep the old config. This is especially important for hot-reload scenarios.

**Status:** OPEN

---

### F10 -- [S4 Ops] P2 -- No debounce on file watcher events

**File:** `src/runtime/config/watcher.go:68-101`

**Evidence:**
```go
func (w *Watcher) loop() {
    for {
        select {
        case event, ok := <-w.watcher.Events:
            if !ok { return }
            if event.Has(fsnotify.Write) || event.Has(fsnotify.Create) {
                // immediately invoke all callbacks
```

**Description:** File editors (vim, VS Code) often produce multiple rapid write events for a single save operation (write temp file, rename, etc.). The watcher fires callbacks on every `Write` or `Create` event without any debounce, which can trigger multiple rapid Reload calls. In the bootstrap integration, each event triggers a full YAML re-parse and map swap, which is wasteful and could cause log spam.

**Severity:** P2

**Suggestion:** Add a configurable debounce interval (e.g., 100ms default) using a `time.Timer` that resets on each event, only firing the callbacks when the timer expires.

**Status:** OPEN

---

### F11 -- [S3 Test] P1 -- Watcher test relies on timing with `time.Sleep`

**File:** `src/runtime/config/watcher_test.go:33-42`

**Evidence:**
```go
w.Start()

// Give the watcher time to start.
time.Sleep(50 * time.Millisecond)

// Modify the file.
require.NoError(t, os.WriteFile(file, []byte("key: val2"), 0o644))

// Wait for callback.
assert.Eventually(t, func() bool {
    return called.Load() >= 1
}, 2*time.Second, 50*time.Millisecond, ...)
```

**Description:** The 50ms sleep before file modification is a race-prone pattern. On slow CI machines, the watcher goroutine may not have started its `select` loop yet, causing the test to miss the write event. The `Eventually` timeout of 2 seconds provides a safety net, but the initial sleep is fragile. This has been a known flaky-test pattern with fsnotify.

**Severity:** P1

**Suggestion:** Use a "ready" signal from the watcher (e.g., a channel that closes once the loop starts its first `select`) instead of a fixed sleep. Alternatively, write the file in a retry loop until the callback fires.

**Status:** OPEN

---

### F12 -- [S1 Architecture] P2 -- shutdown.runHooks returns ctx.Err() even on success

**File:** `src/runtime/shutdown/shutdown.go:77-96`

**Evidence:**
```go
func (m *Manager) runHooks(ctx context.Context) error {
    var firstErr error
    for i := len(m.hooks) - 1; i >= 0; i-- {
        if err := m.hooks[i](ctx); err != nil {
            // ...
            if firstErr == nil { firstErr = err }
        }
    }
    if firstErr != nil {
        return firstErr
    }
    return ctx.Err()  // <--- returns context error even if all hooks succeeded
}
```

**Description:** When all hooks succeed, `runHooks` returns `ctx.Err()`. If hooks complete quickly but the context happens to expire between the last hook and this line (possible with very short timeouts), the method returns `context.DeadlineExceeded` even though all hooks completed successfully. The `TestManager_NoHooks` test passes only because the 100ms timeout has not expired yet.

More practically: looking at `Wait()` -- after running all hooks successfully, `ctx.Err()` will be nil because the context has not yet been cancelled (the timeout is 30s by default). So this is a theoretical concern but a correctness smell.

**Severity:** P2

**Suggestion:** Return `nil` when all hooks succeed:
```go
if firstErr != nil {
    return firstErr
}
return nil
```

**Status:** OPEN

---

### F13 -- [S5 DX] P2 -- doc.go example in config uses incorrect NewFromMap key format

**File:** `src/runtime/config/doc.go:12-13`

**Evidence:**
```go
// For testing, use NewFromMap to create a Config from an existing map:
//
//	cfg := config.NewFromMap(map[string]any{"server.port": 8080})
```

**Description:** `NewFromMap` passes the map through `flatten()`, which expects nested maps. A flat key like `"server.port"` is treated as a single key (not split on dot), so `cfg.Get("server.port")` would return `8080` but `cfg.Scan()` would produce `{"server.port": 8080}` as a top-level key, not a nested `server.port` structure. The example is misleading because it suggests dot-separated keys work as input, but they are only meaningful for the flattened `data` map, not the `raw` map used by `Scan`.

**Severity:** P2

**Suggestion:** Update the example to use nested maps:
```go
//	cfg := config.NewFromMap(map[string]any{
//	    "server": map[string]any{"port": 8080},
//	})
```

**Status:** OPEN

---

## Dependency Compliance Check (All Seats)

| Check | Result |
|-------|--------|
| `runtime/config` imports `cells/` | NO -- Clean |
| `runtime/config` imports `adapters/` | NO -- Clean |
| `runtime/shutdown` imports `cells/` | NO -- Clean |
| `runtime/shutdown` imports `adapters/` | NO -- Clean |
| `runtime/config` imports `kernel/` | NO -- Clean (only std lib + gopkg.in/yaml.v3 + fsnotify) |
| `runtime/shutdown` imports `kernel/` | NO -- Clean (only std lib) |
| Cross-Cell direct import | N/A (these are runtime packages) |
| `ref:` tag present | YES -- config.go header has `ref: go-micro` |

---

## Findings Ledger Summary

| ID | Severity | Seat | Category | File | Status |
|----|----------|------|----------|------|--------|
| F01 | P2 | S1 | Redundant fsnotify.Close call | watcher.go:105-113 | OPEN |
| F02 | P1 | S1 | TOCTOU race in Watcher.Close | watcher.go:105-113 | OPEN |
| F03 | P1 | S3 | Missing concurrent read/reload test | config_test.go | OPEN |
| F04 | P2 | S3 | Missing panic-recovery test for watcher | watcher_test.go | OPEN |
| F05 | P2 | S5 | Env override type coercion undocumented | config.go:175 | OPEN |
| F06 | P2 | S1 | shutdown.Register not goroutine-safe | shutdown.go:48-49 | OPEN |
| F07 | P2 | S3 | Timeout test does not assert DeadlineExceeded | shutdown_test.go:59-70 | OPEN |
| F08 | P2 | S5 | No typed Get helpers (DX gap vs go-micro ref) | config.go:24-26 | OPEN |
| F09 | P2 | S1 | No validation hook in Reload path | config.go:109-129 | OPEN |
| F10 | P2 | S4 | No debounce on watcher events | watcher.go:68-101 | OPEN |
| F11 | P1 | S3 | Watcher test timing-dependent (flaky risk) | watcher_test.go:33-42 | OPEN |
| F12 | P2 | S1 | runHooks returns ctx.Err() on full success | shutdown.go:94 | OPEN |
| F13 | P2 | S5 | doc.go example misleading for NewFromMap | doc.go:12-13 | OPEN |

**Totals:** 0 P0, 3 P1, 10 P2

---

## Verdict

**No P0 blockers.** Three P1 findings (F02, F03, F11) should be addressed before the next milestone:

- **F02** is a real race condition that can cause a panic in production when `StartWithContext` cancellation and explicit `Close()` overlap. Fix with `sync.Once`.
- **F03** is a test gap for a concurrency-critical code path. The mutex is there but unexercised under `-race`.
- **F11** is a CI flakiness risk that will cause spurious failures.

The P2 findings are quality improvements that should be tracked in the backlog.
