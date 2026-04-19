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

---

## Chapter 4 — Durable Write Slice + RunInTx + Outbox Event Pattern

This chapter documents the **flag-write slice** as the canonical template for
adding a durable CUD write slice to any GoCell cell. The pattern ensures L2
OutboxFact consistency: the domain write and the outbox event are always
committed or rolled back together.

### Why RunInTx + Outbox must be atomic

Unleash's original design (pre-2020) wrote the flag record and then published
the change event as two separate database calls. When the event write failed,
the flag was already mutated with no event — consumers diverged silently.

The GoCell pattern wraps **both** operations inside a single `RunInTx` call.
If the outbox write fails the entire transaction rolls back, including the
domain write. The outbox relay then delivers the event at-least-once after
commit, driven by the `outbox_entries` table.

ref: Unleash/unleash src/lib/db/feature-environment-store.ts  
ref: Watermill router pattern — event publishing decoupled from HTTP handler  

### Service implementation (from `cells/config-core/slices/flagwrite/service.go`)

```go
// L2 OutboxFact: repo writes + outbox writes are wrapped in a single RunInTx
// per operation. Failure in either rolls back both.
type Service struct {
    repo         ports.FlagRepository
    outboxWriter outbox.Writer
    txRunner     persistence.TxRunner
    logger       *slog.Logger
}

func (s *Service) Toggle(ctx context.Context, key string, enabled bool) (*domain.FeatureFlag, error) {
    if key == "" {
        return nil, errcode.New(errcode.ErrFlagInvalidInput, "key is required")
    }

    var updated *domain.FeatureFlag

    if err := s.runInTx(ctx, func(txCtx context.Context) error {
        var err error
        updated, err = s.repo.Toggle(txCtx, key, enabled)  // atomic UPDATE ... RETURNING
        if err != nil {
            return fmt.Errorf("flag-write: toggle: %w", err)
        }
        return s.emitFlagChanged(txCtx, "toggled", updated) // outbox write inside same tx
    }); err != nil {
        return nil, err
    }

    s.logger.Info("feature flag toggled",
        slog.String("key", key),
        slog.Bool("enabled", enabled))
    return updated, nil
}

// FlagChangedPayload is the typed event struct (camelCase JSON per convention).
type FlagChangedPayload struct {
    EventID    string    `json:"eventId"`
    Action     string    `json:"action"`
    Key        string    `json:"key"`
    Enabled    bool      `json:"enabled"`
    Version    int       `json:"version"`
    OccurredAt time.Time `json:"occurredAt"`
}
```

### Key design decisions

| Decision | Choice | Reason |
|----------|--------|--------|
| Toggle SQL | `UPDATE ... SET enabled=$1, version=version+1 ... RETURNING *` | Prevents concurrent partial-write data loss (Unleash lesson) |
| Payload struct | Typed `FlagChangedPayload` | Schema enforced at compile time; JSON Schema contract validated in tests |
| runInTx nil-check | `if s.txRunner != nil { ... } else fn(ctx)` | Demo mode (no tx) stays functional; Init() validates presence for L2 slices |
| Event action field | `"created" \| "updated" \| "toggled" \| "deleted"` | Distinct `toggled` action separates bulk-update from enable/disable flows |
| outboxWriter nil-check | No event emitted if nil | Integration test wiring; Init() must fail-fast before serving in durable mode |

### Checklist for adding a new durable write slice

1. Create `cells/<cell>/slices/<slice-name>/service.go` — implement
   `Create/Update/Delete` each calling `s.runInTx(ctx, func(txCtx) { repo.X + outboxWriter.Write })`.
2. Create typed `*ChangedPayload` struct with `eventId`, `action`, `occurredAt`.
3. Create `handler.go` — decode request, call service, respond with DTO.
4. Create `slice.yaml` in `cells/<cell>/slices/<slice-name>/` — declare `contractUsages`
   for each HTTP contract (`role: serve`) and the event contract (`role: publish`).
5. Create contracts under `contracts/http/...` and `contracts/event/...` with JSON Schema.
6. Add `initXxxWriteSlice()` in `cell.go` — mirror `initFlagWriteSlice()`.
7. Register HTTP routes in `RegisterRoutes` under the resource prefix.
8. Write `service_test.go` (atomicity), `contract_test.go` (schema), `ctx_cancel_test.go` (rollback).
9. Run `go run ./cmd/gocell validate` — must be 0 errors.

### Disposition flow

```
Service.Create(ctx)
  └─ runInTx(ctx, fn)
       ├─ repo.Create(txCtx, flag)     ──→ INSERT INTO feature_flags
       └─ outboxWriter.Write(txCtx, e) ──→ INSERT INTO outbox_entries
             ↓ tx commits
       relay polls outbox_entries
             ↓ publishes to broker
       consumer receives flag.changed.v1
             ↓ DispositionAck
       Receipt.Commit (idempotency key marked done)
```

### Chapters 5–7 (placeholder — PR3)

- **Chapter 5**: Repository-boundary encryption + `ValueTransformer` + `KeyProvider` selection
- **Chapter 6**: Key rotation + staleness-driven lazy re-encrypt
- **Chapter 7**: Three-layer testing (unit / testcontainers / e2e compose)
