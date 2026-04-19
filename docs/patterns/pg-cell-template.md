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

---

## Chapter 5 — Repository-boundary Encryption

Sensitive config values are encrypted before they reach the database and
decrypted on the way out, entirely within the repository layer. The handler
and service layers are unaware of encryption details.

### Core abstractions

```go
// runtime/crypto — three interfaces, one selection concern.

type KeyHandle interface {
    ID() string
    Encrypt(ctx context.Context, plaintext, aad []byte) (ciphertext, nonce, edk []byte, err error)
    Decrypt(ctx context.Context, ciphertext, nonce, edk, aad []byte) (plaintext []byte, err error)
}

type KeyProvider interface {
    Current(ctx context.Context) (KeyHandle, error)      // current key for encryption
    ByID(ctx context.Context, keyID string) (KeyHandle, error) // historical key for decryption
    Rotate(ctx context.Context) (newKeyID string, err error)
}

type ValueTransformer interface {
    Encrypt(ctx context.Context, plaintext, aad []byte) (ciphertext []byte, keyID string, nonce, edk []byte, err error)
    Decrypt(ctx context.Context, ciphertext []byte, keyID string, nonce, edk, aad []byte) (plaintext []byte, err error)
}
```

`ValueTransformer` is the boundary interface: it wraps `KeyProvider` to handle
key resolution and forwards AAD to `KeyHandle`. The repository only calls
`Encrypt` and `Decrypt` — it does not manage keys.

`NoopTransformer` is the production-safe fallback for `sensitive=false` entries
and for development without a key provider. It is an identity transformer, not
a zero value — explicit, auditable, and searchable in code.

### Envelope encryption design

Each sensitive row uses a per-row Data Encryption Key (DEK):

```
plaintext ──→ AES-GCM-256 (DEK, nonce) ──→ value_cipher  + value_nonce
DEK       ──→ AES-GCM-256 (KEK, edk_nonce) ──→ value_edk
KeyHandle.ID() ──→ value_key_id
```

- `value_cipher`: raw ciphertext (no nonce prefix — nonce stored separately)
- `value_nonce`: 12-byte random nonce for AES-GCM  
- `value_edk`: encrypted DEK (self-contained blob with its own nonce prefix)
- `value_key_id`: key version identifier used to resolve the correct KEK on
  read (enables key rotation without re-encrypting all rows)

VaultTransit uses a different scheme: Vault manages DEK + KEK internally.
`nonce` and `edk` are nil; `value_cipher` contains the opaque Vault ciphertext
(`vault:vN:base64...`); `value_key_id` stores the Vault key version extracted
from the ciphertext prefix.

### Additional Authenticated Data (AAD)

```go
func AADForConfig(cellID, configKey string) []byte {
    return []byte(fmt.Sprintf("cell:%s/key:%s", cellID, configKey))
}
```

AAD is computed from the row's identity and bound into the ciphertext. It is
**not** stored in the database — it is recomputed on decrypt from the row's key.
This prevents a ciphertext for `cell:A/key:x` from being transplanted into
`cell:B/key:y` (cross-row replay attack).

### Repository boundary — write path

```go
// config_repo.go — Create (sensitive branch)
ct, keyID, nonce, edk, err := r.encryptValue(ctx, entry.Key, entry.Value)
db.Exec(ctx, `INSERT INTO config_entries
    (id, key, value, sensitive, version, created_at, updated_at,
     value_cipher, value_key_id, value_edk, value_nonce)
    VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
    entry.ID, entry.Key, "",  // value stored as "" when encrypted
    entry.Sensitive, entry.Version, entry.CreatedAt, entry.UpdatedAt,
    ct, keyID, edk, nonce,
)
```

Plaintext value is set to `""` (empty string) for encrypted rows. On decrypt
failure the entry is **not** returned with an empty value — the error propagates
as `ErrConfigDecryptFailed` (fail-closed).

### Repository boundary — read path

```go
// Transparent decryption (config_repo.go — GetByKey)
if e.Sensitive && len(valueCipher) > 0 && valueKeyID != nil && *valueKeyID != "" {
    plain, err := r.decryptValue(ctx, key, valueCipher, *valueKeyID, valueNonce, valueEDK)
    if err != nil {
        return nil, err // ErrConfigDecryptFailed — never return empty plaintext
    }
    e.Value = plain
}
```

### Provider selection at startup

```
GOCELL_KEY_PROVIDER=local-aes      → LocalAESKeyProvider (dev/CI)
GOCELL_KEY_PROVIDER=vault-transit  → VaultTransitKeyProvider (production)
(unset)                            → NoopTransformer (dev mode, plaintext)
```

When `GOCELL_KEY_PROVIDER` is unset and storage backend is `postgres`,
`buildKeyProvider` logs a structured Warn (not an error) so that operators
who haven't yet configured encryption are notified without breaking deployments.

Fail-fast applies for invalid values:
```
GOCELL_KEY_PROVIDER=unknown → ErrValidationFailed at startup
GOCELL_MASTER_KEY not set with local-aes → ErrValidationFailed at startup
```

### Wiring in cell.go (deferred construction)

`WithKeyProvider` and `WithPostgresDefaults` may be applied in any order.
Repo construction is deferred to `Init()` so both options are visible:

```go
// cell.go — Init()
if c.pgPool != nil && c.configRepo == nil {
    session := cellpg.NewSession(c.pgPool)
    c.configRepo = cellpg.NewConfigRepository(session, c.valueTransformer)
    c.flagRepo = cellpg.NewFlagRepository(session)
}
```

`c.valueTransformer` is set by `WithKeyProvider` or `WithValueTransformer`.
If neither is called, the repo receives `nil` — the repo treats nil as
"no encryption for non-sensitive entries" but fails-fast on sensitive writes
with `ErrConfigKeyMissing`.

---

## Chapter 6 — Key Rotation and Staleness-Driven Lazy Re-encrypt

### Rotation model

Keys are versioned. `KeyProvider.Rotate(ctx)` advances the "current" key ID.
Existing ciphertext rows are **not** re-encrypted eagerly — they remain readable
via `ByID(ctx, storedKeyID)` until explicitly migrated.

```
Before rotation:   current = "local-aes-v1"
After rotation:    current = "local-aes-v2", "local-aes-v1" still accessible
```

This decouples rotation from downtime: you rotate, deploy, then run the
migration tool at a convenient time.

### Staleness signal

On each read, the repository compares the stored `value_key_id` against the
current key ID:

```go
// hasCurrent is the optional interface for staleness detection.
type hasCurrent interface {
    CurrentKeyID(ctx context.Context) (string, error)
}

func (r *ConfigRepository) currentKeyID(ctx context.Context) (string, bool) {
    if hc, ok := r.transformer.(hasCurrent); ok {
        id, err := hc.CurrentKeyID(ctx)
        if err != nil || id == "" {
            return "", false
        }
        return id, true
    }
    return "", false
}
```

`ConfigEntry.Stale = true` when `storedKeyID != currentKeyID`. The service
and handler layers may expose `stale` in responses so operators know which
entries need re-encryption.

The `hasCurrent` interface is optional — `ValueTransformer` does not require
it. Only `keyProviderTransformer` implements it. `NoopTransformer` and custom
test transformers that do not implement it will simply never mark entries stale.

### Re-encryption patterns

**Lazy (per-request)**: On a write (`Update`, `PublishVersion`) for a stale
entry, the repository naturally re-encrypts under the new key because it
always calls `transformer.Encrypt(ctx, ...)` with the current key. No explicit
lazy path is needed — re-encryption happens on the next mutation.

**Eager (bulk migration)**: For long-lived read-only entries that are never
updated, use `plaintextMigrator.MigrateConfigEntries(ctx)`:

```go
migrator, err := newPlaintextMigrator(db, transformer, PlaintextMigrationConfig{
    BatchSize:      100,            // rows per DB round-trip
    RateLimitDelay: 50 * time.Millisecond, // pause between batches
})
result, err := migrator.MigrateConfigEntries(ctx)
// result.Processed = rows encrypted this run
// result.Skipped   = rows already encrypted (idempotent)
```

The migrator scans `WHERE sensitive = true AND value_cipher IS NULL`, so it is
safe to run multiple times (idempotent by SQL predicate). It does not update
already-encrypted rows regardless of their key ID. Key-rotation migration
(re-encrypting under a new KEK) is a separate future concern (backlog S14b).

### LocalAES key loading

```
GOCELL_MASTER_KEY          64-char hex or base64 (required)
GOCELL_MASTER_KEY_PREVIOUS 64-char hex or base64 (optional; enables reading
                            pre-rotation ciphertext after KEK rotation)
```

LocalAES does not call an external KMS — keys are loaded from environment
variables at startup. This is intentional for dev/CI: no network dependency,
deterministic, fast. Production should use VaultTransit.

### VaultTransit key management

VaultTransit delegates all key material management to Vault:

```
GOCELL_VAULT_ADDR    e.g. https://vault.example.com
GOCELL_VAULT_TOKEN   service token with transit/{name}/encrypt+decrypt+rotate
GOCELL_VAULT_KEY     Vault key name (default: "gocell-config")
```

Vault manages DEK + KEK rotation internally. `ByID` validates the
`vault-transit:{version}` prefix and reconstructs the Vault key name.
`nonce` and `edk` columns are NULL for VaultTransit rows.

Integration with the production Vault SDK is tracked in backlog S14a.

---

## Chapter 7 — Three-Layer Testing Strategy

Testing encrypted config values requires three layers: unit tests that
avoid DB and KMS calls, testcontainers integration tests with a real PG schema,
and e2e tests that validate the full assembly.

### Layer 1 — Unit tests (no DB, no KMS)

Test the transformer independently using table-driven cases:

```go
// runtime/crypto/local_aes_provider_test.go — pattern
func TestLocalAESHandle_Encrypt_Decrypt_RoundTrip(t *testing.T) {
    cases := []struct {
        name      string
        plaintext string
    }{
        {"empty", ""},
        {"short", "v"},
        {"unicode", "日本語テスト"},
        {"binary", string([]byte{0x00, 0xFF, 0xFE})},
    }
    for _, tc := range cases {
        t.Run(tc.name, func(t *testing.T) {
            kp := mustBuildLocalAESProvider(t)
            h, _ := kp.Current(ctx)
            ct, nonce, edk, _ := h.Encrypt(ctx, []byte(tc.plaintext), nil)
            pt, _ := h.Decrypt(ctx, ct, nonce, edk, nil)
            assert.Equal(t, normBytes(tc.plaintext), normBytes(string(pt)))
        })
    }
}
```

Test the repository boundary using the `fakeDB` pattern (implemented in
`config_repo_test.go`):

```go
// Inject a pre-built transformer alongside a fakeDB — no pgxpool.Pool needed.
m := newEncryptedRepoFromDBTX(fakeDB, transformer)
err := m.Create(ctx, &domain.ConfigEntry{Key: "k", Value: "v", Sensitive: true})
```

AAD mismatch and wrong-key tests must be present and marked fail-closed:
```go
_, err = h.Decrypt(ctx, ct, nonce, edk, []byte("wrong-aad"))
require.Error(t, err, "AAD mismatch must fail-closed")
```

### Layer 2 — testcontainers (real PG schema)

Integration tests in `cells/config-core/internal/adapters/postgres/*_test.go`
use `testcontainers-go` to spin up a real PostgreSQL instance and run
migrations 001–008 before the test suite. These tests exercise the full
repository path including cipher columns.

```go
//go:build integration

func TestConfigRepository_Encrypt_Decrypt_IntegrationRoundTrip(t *testing.T) {
    pool := startTestPG(t) // testcontainers helper
    kp, _ := crypto.NewLocalAESKeyProviderFromKeys(validKey, "")
    tr := crypto.NewValueTransformer(kp)
    session := cellpg.NewSession(pool)
    repo := cellpg.NewConfigRepository(session, tr)

    entry := &domain.ConfigEntry{Key: "sec.key", Value: "secret", Sensitive: true}
    // ... Create, GetByKey, assert decrypted value matches
}
```

Run with: `go test -tags=integration -timeout=120s ./cells/config-core/...`

### Layer 3 — e2e compose (full assembly)

E2E tests in `tests/e2e/config_pilot_e2e_test.go` validate the full HTTP API
with a running core-bundle, PostgreSQL, and optionally Vault:

```go
//go:build e2e

func TestE2E_ConfigEncryption_SensitiveValueNotExposedInResponse(t *testing.T) {
    waitForReady(t, 30*time.Second)
    token := e2eAdminToken()
    // POST sensitive entry, GET back — assert value is redacted.
}
```

Required environment:
```
GOCELL_CELL_ADAPTER_MODE=postgres
GOCELL_KEY_PROVIDER=local-aes
GOCELL_MASTER_KEY=<64-char hex>
GOCELL_DATABASE_URL=postgres://...
E2E_ADMIN_TOKEN=<jwt>
```

Run with: `go test -tags=e2e -timeout=120s ./tests/e2e/...`

### Coverage thresholds

| Layer | Package | Minimum |
|-------|---------|---------|
| Unit | `runtime/crypto/` | ≥ 90% |
| Unit | `cells/config-core/internal/adapters/postgres/` | ≥ 80% |
| Integration | cipher columns round-trip | required (no skip) |
| E2E | all `t.Skip` stubs | acceptable — activated by compose environment |

### Anti-patterns to avoid

- Using `NoopTransformer` in integration tests that claim to test encryption.
- Asserting that `entry.Value == ""` as proof of encryption — the plaintext
  field is cleared on write but the integration test must verify the value can
  be decrypted back, not just that it was zeroed.
- Testing key rotation without asserting that the old ciphertext is still
  decryptable after rotation (historical key access is a correctness invariant).
- Skipping AAD mismatch tests — they must fail-closed; a passing test here
  is a security regression.
