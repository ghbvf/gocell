# PG Cell Template

Patterns for adding PostgreSQL storage to a GoCell cell. Covers the three
primitives introduced in PR-CC-TOPOLOGY-BUILDER and the rules for combining
them correctly.

---

## Chapter 1 — Topology: Single Source of Truth

`bootstrap.Topology` is the authority for the adapter-mode / storage-backend
pairing in a GoCell assembly. It must be the **only** place where that pairing
is resolved; all other code reads from `Topology`.

### What it is

```go
type Topology struct {
    AdapterMode    string // "" (dev) | "real" (production)
    StorageBackend string // "memory" | "postgres"
}
```

`TopologyFromEnv()` reads `GOCELL_ADAPTER_MODE` and `GOCELL_CELL_ADAPTER_MODE`,
validates the combination, and returns the resolved `Topology`. The only valid
combinations are:

| GOCELL_CELL_ADAPTER_MODE | GOCELL_ADAPTER_MODE | Valid? |
|--------------------------|---------------------|--------|
| (unset) / memory         | (any)               | yes    |
| postgres                 | real                | yes    |
| postgres                 | (unset)             | **no** — fail-fast |

The coupling rule prevents "real persistence + dev-grade keys": if the data
plane uses PostgreSQL, the control plane must also use production-grade key
loading, token-guarded `/metrics`, and token-guarded `/readyz?verbose`.

### Where to use it

- Read `Topology` exactly once at composition root (`AppDepsFromEnv`).
- Pass the resolved `Topology` struct through `AppDeps` to all consumers.
- Never re-read `GOCELL_ADAPTER_MODE` or `GOCELL_CELL_ADAPTER_MODE` in
  lower-level code — read from the `Topology` you received.

### AdapterInfo for observability

```go
info := topo.AdapterInfo()
// {"mode": "real-keys-postgres-storage", "storage": "postgres",
//  "event_bus": "in-memory", "outbox_storage": "postgres"}
```

Pass `info` to `bootstrap.WithAdapterInfo(info)` so operators can confirm
active backends via `/readyz?verbose` without reading logs.

### Anti-patterns to avoid

- Checking `os.Getenv("GOCELL_CELL_ADAPTER_MODE")` outside `TopologyFromEnv`.
- Coupling mode checks inside cell constructors or adapter packages.
- Deriving mode coupling in multiple places — leads to the assembly drift
  finding this pattern was created to fix.

---

## Chapter 2 — ManagedResource: Three-Piece Lifecycle

`bootstrap.ManagedResource` is the single interface through which an external
resource (PG pool + relay, Redis, etc.) participates in the bootstrap lifecycle.

### The interface

```go
type ManagedResource interface {
    // Checkers returns named health probe functions registered under /readyz.
    Checkers() map[string]func() error

    // Worker returns the background worker (relay, cache warmer, etc.).
    // May be nil when no background work is needed.
    Worker() worker.Worker

    // Close shuts down the resource. Called during LIFO teardown after the
    // assembly, HTTP server, and workers have stopped.
    Close() error
}
```

### PGResource: the concrete implementation

```go
pgRes := adapterpg.NewPGResource(pool, relayWorker)
// pgRes.Checkers()["postgres"] → pings the pool with a 5-second standalone ctx
// pgRes.Worker()               → the outbox relay worker (may be nil)
// pgRes.Close()                → pool.Close()
```

Key design decisions:

1. **5-second standalone context in Checkers()** — Each probe call creates
   `context.WithTimeout(context.Background(), 5s)`, never the caller's context.
   A SIGTERM cancelling the outer context must not cause the probe to report
   PG as down; Kubernetes cannot distinguish "PG unreachable" from "process
   shutting down" if the outer context is passed directly.

2. **nil relay is valid** — `NewPGResource(pool, nil)` is correct for modes
   where no outbox relay is needed. `Worker()` returns nil; bootstrap ignores
   nil workers.

3. **LIFO teardown order** — ManagedResource teardowns are appended to the
   `teardowns` slice before assembly/HTTP teardowns. Because teardowns are
   executed in LIFO (reverse) order, the managed resource is closed **last**:
   assembly stops → HTTP stops → workers stop → PG pool closes.
   This matches the uber-go/fx lifecycle order and prevents the pool from
   being closed while cells are still processing requests.

### Registering with bootstrap

```go
// Pass via AppDeps.PGResource:
if d.deps.PGResource != nil {
    opts = append(opts, bootstrap.WithManagedResource(d.deps.PGResource))
}

// bootstrap.expandManagedResources() handles the rest:
// → adds Checkers() entries to health probes
// → adds Worker() to the worker group (if non-nil)
// → registers Close() in the LIFO teardown slice
```

### Anti-patterns to avoid

- Passing the pool context to the health checker (SIGTERM sensitivity).
- Registering pool health checkers manually alongside `WithManagedResource`
  (double registration, duplicate checker names).
- Calling `pool.Close()` directly in tests — use `res.Close()` so the
  abstraction holds even when the pool is swapped for a fake.

---

## Chapter 3 — AppDeps + BuildBootstrap: Test/Production Sharing

Assembly drift occurs when tests assemble cells differently from production.
The `AppDeps` + `BuildBootstrap` pattern eliminates this by sharing a single
assembly entry point.

### The pattern

```go
// Production path:
deps, err := AppDepsFromEnv(ctx)   // reads env, builds all deps
app, err := BuildBootstrap(deps)   // assembles cells + bootstrap
app.Run(ctx)

// Test path:
deps := &AppDeps{
    Topology:   bootstrap.Topology{StorageBackend: "memory"},
    JWTDeps:    jwtDeps{issuer: testIssuer, verifier: testVerifier},
    PromStack:  ps,
    CursorCodecs: codecs,
    HMACKey:    []byte("test-hmac-key-32-bytes-long!!!!!"),
    EventBus:   eventbus.New(),
}
app, err := BuildBootstrap(deps, bootstrap.WithListener(ln))
```

Both paths call the same `BuildBootstrap`, so any assembly change in
production automatically appears in tests.

### AppDeps design decisions

- **`PGResource bootstrap.ManagedResource`** (interface, not `*adapterpg.PGResource`)
  — allows tests to inject a `fakeManagedResource` without a real PG pool.
  Tests set `PGResource = &fakeManagedResource{name: "postgres"}`.

- **`configCellOpts []configcore.Option` (private)** — populated by
  `AppDepsFromEnv` with production adapter options. Test struct literals
  leave it nil, so `buildConfigCell` falls back to `WithInMemoryDefaults()`.
  This lets the struct literal syntax remain clean without exposing internals.

- **`MetricsToken` and `VerboseToken` are required in real adapter mode** —
  tests using `AdapterMode: "real"` (e.g. postgres wiring tests) must supply
  both tokens; omitting them triggers a fail-fast in `BuildBootstrap`.

### Test topology helpers

```go
// buildTestDeps returns AppDeps for memory topology without a real PG pool.
func buildTestDeps(t *testing.T) *AppDeps {
    t.Setenv("GOCELL_STATE_DIR", t.TempDir())
    t.Setenv("GOCELL_JWT_ISSUER", "test-issuer")
    t.Setenv("GOCELL_JWT_AUDIENCE", "test-audience")
    eb := eventbus.New()
    privKey, pubKey := auth.MustGenerateTestKeyPair()
    keySet, _ := auth.NewKeySet(privKey, pubKey)
    issuer, _ := auth.NewJWTIssuer(keySet, "test-issuer", 15*time.Minute,
        auth.WithDefaultAudience("test-audience"))
    verifier, _ := auth.NewJWTVerifier(keySet,
        auth.WithExpectedAudiences("test-audience"))
    ps, _ := buildPromStack()
    codecs, _ := loadAllCursorCodecs("")
    return &AppDeps{
        Topology:     bootstrap.Topology{StorageBackend: "memory"},
        JWTDeps:      jwtDeps{issuer: issuer, verifier: verifier},
        PromStack:    ps,
        CursorCodecs: codecs,
        HMACKey:      []byte("test-hmac-key-32-bytes-long!!!!!"),
        EventBus:     eb,
    }
}
```

For postgres topology wiring tests, add `MetricsToken` and `VerboseToken`:

```go
deps := &AppDeps{
    Topology:     bootstrap.Topology{StorageBackend: "postgres", AdapterMode: "real"},
    PGResource:   fakePG, // inject fake; no real PG needed for wiring test
    MetricsToken: "test-metrics-token",
    VerboseToken: "test-verbose-token",
    // ... other fields
}
```

### What BuildBootstrap does

1. Builds config-core cell (in-memory defaults when `configCellOpts` is nil,
   otherwise uses the production adapter options).
2. Builds access-core with `adminBootstrapWorkerOpts` (lazy admin cleaner).
3. Builds audit-core.
4. Registers all three cells in `CoreAssembly`.
5. Assembles all `bootstrap.Option` slices including `WithManagedResource`
   (if `PGResource` is non-nil), internal guard (if `InternalGuard` is
   non-nil), and verbose/metrics token guards.
6. Returns `bootstrap.New(opts...)`.

Tests and production get identical wiring. No separate "test assembly path"
exists anywhere in the codebase.

### Anti-patterns to avoid

- Manually calling `adapterpg.NewPool` in integration tests to build cells —
  use `AppDeps.PGResource` injection instead.
- Duplicating cell option construction across `main_test.go`, e2e tests, and
  production `run()` — a single `BuildBootstrap` call serves all.
- Forgetting `MetricsToken`/`VerboseToken` when testing with `AdapterMode:
  "real"` — `BuildBootstrap` will fail-fast with a clear error message.
