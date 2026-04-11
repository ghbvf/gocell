# Wave C Architecture Design: CS-AR-2 + CS-AR-3 + F-OB-01

> Date: 2026-04-11
> Status: Draft
> Prerequisite: PR#68 (D1 kernel interface migration) merged
> Scope: kernel/cell interfaces, kernel/outbox Writer

---

## 1. CS-AR-2: Dependencies Interface Minimization

### 1.1 Current State

`Dependencies` is defined at `kernel/cell/interfaces.go:6-10`:

```go
type Dependencies struct {
    Cells     map[string]Cell
    Contracts map[string]Contract
    Config    map[string]any
}
```

The `Cell.Init` method receives it at `interfaces.go:68`:

```go
Init(ctx context.Context, deps Dependencies) error
```

The `CoreAssembly` builds Dependencies at `kernel/assembly/assembly.go:159-163`, passing the full `cellMap` (every registered Cell) plus all contracts and config:

```go
deps := cell.Dependencies{
    Cells:     a.cellMap,
    Contracts: make(map[string]cell.Contract),
    Config:    cfgMap,
}
```

### 1.2 Caller Analysis

**Who actually uses which fields?**

| Cell | `deps.Cells` | `deps.Contracts` | `deps.Config` |
|------|-------------|-----------------|---------------|
| BaseCell (`base.go:91`) | No | No | No |
| AccessCore (`access-core/cell.go:161`) | No | No | Yes -- `deps.Config["access.signing_key"]` |
| AuditCore (`audit-core/cell.go:112`) | No | No | Yes -- `deps.Config["audit.hmac_key"]` |
| ConfigCore (`config-core/cell.go:103`) | No | No | No |
| DeviceCell (`device-cell/cell.go:84`) | No | No | No |
| OrderCell (`order-cell/cell.go:75`) | No | No | No |
| failInitCell (`assembly_test.go:49`) | No | No | No |

**Result: Zero callers use `deps.Cells` or `deps.Contracts`.** Only two cells use `deps.Config` for fallback key resolution. All dependencies (outboxWriter, publisher, repos) are injected via functional options at construction time, not via `Dependencies`.

The `Cells map[string]Cell` field is the most problematic: it exposes the **entire cell graph** to every cell, violating least-privilege. A cell could call `deps.Cells["other-cell"].Stop(ctx)` and crash the system.

### 1.3 Design Options

#### Option A: Narrow Dependencies to Config-only (Recommended)

Replace the current struct with a config-only container:

```go
// Dependencies is the set of collaborators injected into a Cell during Init.
type Dependencies struct {
    Config map[string]any
}
```

Remove `Cells` and `Contracts` fields. Cross-cell communication must go through contracts (already enforced by CLAUDE.md rules). The assembly already resolves all real dependencies via functional options.

**Migration**:
- `assembly.go:159-163`: Remove `Cells` and `Contracts` from construction.
- All cell `Init` methods: No code change needed -- none use `deps.Cells` or `deps.Contracts`.
- All test `Dependencies{}` literals: No change needed if they only use `Config`.

**Impact**: 0 production code changes in cells; ~15 test file updates (remove unused `Cells`/`Contracts` from `Dependencies{}` literals in tests).

#### Option B: Replace Dependencies with a ConfigProvider interface

```go
type ConfigProvider interface {
    ConfigValue(key string) (any, bool)
}
```

Change `Init` signature to `Init(ctx context.Context, cfg ConfigProvider) error`.

**Tradeoff**: More type-safe, but breaks the `Init` signature (breaking change to the `Cell` interface). Every cell and every test must update.

#### Option C: Keep struct, deprecate unused fields

```go
type Dependencies struct {
    // Deprecated: Do not use. Cross-cell access must go through contracts.
    Cells     map[string]Cell
    // Deprecated: Do not use.
    Contracts map[string]Contract
    Config    map[string]any
}
```

**Tradeoff**: Least disruptive but doesn't actually fix the coupling.

### 1.4 Recommendation: Option A, phased

**Phase 1 (this PR)**: Remove `Cells` and `Contracts` from `Dependencies` struct. This is a **breaking change** to the exported type, but since zero callers use these fields, the actual breakage is limited to struct literal compilation in tests.

**Phase 2 (future, if needed)**: If `Config` proves insufficient, evolve to `ConfigProvider` interface.

### 1.5 Backward Compatibility Assessment

| Dimension | Impact |
|-----------|--------|
| `Dependencies` struct literal with field names | Compile error if `Cells:` or `Contracts:` used. Grep confirms: 0 production callers, ~15 test files use `Dependencies{}` but none set `Cells` or `Contracts` to non-empty values that are read. |
| `Cell.Init` signature | Unchanged -- still `Init(ctx, Dependencies)`. |
| `CoreAssembly.startInternal` | Simplifies -- remove 2 lines. |
| `runtime/bootstrap` | No change -- uses `asm.StartWithConfig` which delegates. |

**Risk: Low.** No caller reads `deps.Cells` or `deps.Contracts`.

### 1.6 Files to Change

| File | Change |
|------|--------|
| `kernel/cell/interfaces.go` | Remove `Cells` and `Contracts` from `Dependencies` |
| `kernel/assembly/assembly.go` | Remove `Cells` and `Contracts` from deps construction (lines 160-161) |
| `kernel/cell/base_test.go` | Update `Dependencies{}` literals (no functional change) |
| `kernel/assembly/assembly_test.go` | Update `Dependencies{}` literals |
| `cells/*/cell_test.go` (5 files) | Update `Dependencies{}` literals |

---

## 2. CS-AR-3: Remove net/http from kernel/cell

### 2.1 Current State

`kernel/cell/registrar.go` imports two non-`pkg/` packages:

```go
import (
    "net/http"                                    // stdlib
    "github.com/ghbvf/gocell/kernel/outbox"       // kernel-internal
)
```

**Where `net/http` is used (registrar.go)**:

| Line | Usage | Context |
|------|-------|---------|
| 37 | `handler http.Handler` | `RouteMux.Handle` parameter |
| 48 | `handler http.Handler` | `RouteMux.Mount` parameter |
| 60 | `func(http.Handler) http.Handler` | `RouteMux.With` middleware parameter |

**Where `outbox` is used (registrar.go)**:

| Line | Usage | Context |
|------|-------|---------|
| 70 | `outbox.Subscriber` | `EventRegistrar.RegisterSubscriptions` parameter |

Additionally, `kernel/cell/celltest/mux.go` imports `net/http` for `TestMux`.

### 2.2 Architectural Analysis

The CLAUDE.md constraint states: _"kernel/ 不依赖 runtime/、adapters/、cells/（只依赖标准库 + pkg/）"_

`net/http` is a **standard library** package. Strictly speaking, importing `net/http` does not violate the literal dependency rule. However, there is a stronger architectural argument:

1. **Conceptual purity**: `kernel/cell` defines the Cell abstraction. HTTP is a transport concern that belongs in `runtime/`. Not all Cells serve HTTP (e.g., worker-only cells, L0 computation cells).

2. **Import weight**: `net/http` pulls in `crypto/tls`, `net`, `mime`, etc. While these are stdlib, they increase the kernel's conceptual surface.

3. **Testability burden**: Every test that implements `RouteMux` must import `net/http` for the `http.Handler` type, even if the test has nothing to do with HTTP. This is visible in the 5 `stubMux` implementations scattered across cell tests.

The `kernel/outbox` import is **not** a problem -- it is kernel-internal (`kernel/` depending on `kernel/`).

### 2.3 Design: Replace http.Handler with a kernel-defined Handler type

Introduce a transport-agnostic handler type in `kernel/cell`:

```go
// Handler is a transport-agnostic request handler.
// For HTTP transport, runtime/http/router bridges this to net/http.Handler.
// This keeps kernel/cell free of transport-specific dependencies.
type Handler interface {
    ServeHTTP(ResponseWriter, *Request)
}

// ResponseWriter is a minimal response interface.
type ResponseWriter interface {
    Header() Header
    Write([]byte) (int, error)
    WriteHeader(statusCode int)
}

// Request represents an incoming request.
type Request struct { ... }
```

**Problem**: This is essentially re-inventing `net/http` types. The abstraction is leaky -- every Cell already thinks in HTTP terms (`GET /users/{id}`), and `http.Handler` is the Go ecosystem standard. Creating a parallel type system would:
- Force every cell to convert between `cell.Handler` and `http.Handler`
- Break compatibility with all existing HTTP middleware (`func(http.Handler) http.Handler`)
- Add no real value since GoCell cells communicate via HTTP contracts

### 2.4 Design (Recommended): Move RouteMux to runtime/http, keep kernel/cell with a generic registrar

**The key insight**: `RouteMux` is an HTTP-specific concern. `HTTPRegistrar` is an HTTP-specific interface. They belong in `runtime/http`, not `kernel/cell`.

However, the bootstrap discovery pattern (`cell.(HTTPRegistrar)`) requires the interface to be in a package that both `runtime/bootstrap` and `cells/` can import. Currently `kernel/cell` serves this role.

**Solution: Introduce `kernel/cell/transport` sub-package with zero-dependency types.**

Actually, a simpler approach:

#### Option A: Accept net/http in kernel (Recommended)

`net/http` is a Go standard library package. The CLAUDE.md constraint says _"只依赖标准库 + pkg/"_ -- and `net/http` IS standard library. The `RouteMux` interface is intentionally stdlib-only (no chi, gorilla imports). The pattern `http.Handler` is Go's universal handler interface, equivalent to `io.Reader` for I/O.

**Rationale**:
- `net/http.Handler` is the Go idiom. Every framework (chi, echo, gin) ultimately bridges to it.
- Creating a parallel type system adds friction without value.
- The current design already achieves decoupling from specific routers (chi etc.) -- the only remaining dependency is the stdlib interface.
- `celltest.TestMux` proves the interface is implementable with pure stdlib.

**Action**: Document this as an explicit architectural decision. Add a comment to `registrar.go`:

```go
// Design decision (CS-AR-3): kernel/cell imports net/http for http.Handler,
// the standard Go handler interface. This is permitted because net/http is
// standard library (per CLAUDE.md: "只依赖标准库 + pkg/"). No third-party
// router libraries are imported. Concrete router implementations live in
// runtime/http/router.
```

#### Option B: Move RouteMux + HTTPRegistrar to runtime/http/routedef

Create `runtime/http/routedef/routedef.go`:

```go
package routedef

import "net/http"

type RouteMux interface {
    Handle(pattern string, handler http.Handler)
    Route(pattern string, fn func(sub RouteMux))
    Mount(pattern string, handler http.Handler)
    Group(fn func(RouteMux))
    With(mw ...func(http.Handler) http.Handler) RouteMux
}

type HTTPRegistrar interface {
    RegisterRoutes(mux RouteMux)
}
```

**Problem**: `cells/` currently depends on `kernel/cell` for both `Cell` and `HTTPRegistrar`. Moving `HTTPRegistrar` to `runtime/` means cells depend on runtime, which is already permitted by CLAUDE.md (`"cells/ 依赖 kernel/ 和 runtime/"`). However, this splits the cell-related interfaces across two packages, hurting discoverability.

**Problem 2**: `bootstrap.go` does `cell.(cell.HTTPRegistrar)` -- type assertion requires the interface to be in the same package as where it is checked. If `HTTPRegistrar` moves to `runtime/`, bootstrap still works (it already imports runtime), but cell implementations would need to import `runtime/http/routedef` instead of `kernel/cell`.

#### Option C: Define RouteMux with stdlib-compatible but kernel-defined types

```go
// In kernel/cell/registrar.go -- no import of net/http

// ServeFunc is a handler function compatible with http.HandlerFunc.
// The runtime/http bridge converts it to net/http.Handler.
type ServeFunc func(w any, r any)

// RouteMux is a transport-agnostic route registration interface.
type RouteMux interface {
    HandleFunc(pattern string, handler ServeFunc)
    Route(pattern string, fn func(sub RouteMux))
    ...
}
```

**Problem**: Loses type safety entirely. `any` parameters are worse than importing `net/http`.

### 2.5 Recommendation: Option A

Accept `net/http` as a permitted stdlib dependency in kernel/cell. Document the decision explicitly. The architectural constraint "kernel/ 只依赖标准库 + pkg/" already permits this -- `net/http` IS stdlib.

If a future need arises for non-HTTP transport (gRPC, WebSocket-native routing), a `TransportRegistrar` generic interface can be added alongside `HTTPRegistrar` without breaking changes.

### 2.6 Files to Change

| File | Change |
|------|--------|
| `kernel/cell/registrar.go` | Add architectural decision comment (lines 1-10) |

No code changes needed. This is a documentation-only clarification.

---

## 3. F-OB-01: Writer Batch Write

### 3.1 Current State

`kernel/outbox/outbox.go:61-67` (PR#68 version):

```go
type Writer interface {
    Write(ctx context.Context, entry Entry) error
}
```

**All 7 call sites** write exactly one entry per operation:

| Call Site | File | Pattern |
|-----------|------|---------|
| sessionlogin | `access-core/slices/sessionlogin/service.go:159` | Single entry in tx |
| sessionlogout | `access-core/slices/sessionlogout/service.go:95` | Single entry in tx |
| identitymanage | `access-core/slices/identitymanage/service.go:224` | Single entry, no tx |
| auditappend | `audit-core/slices/auditappend/service.go:118` | Single entry in tx |
| auditverify | `audit-core/slices/auditverify/service.go:99` | Single entry, no tx |
| configwrite | `config-core/slices/configwrite/service.go:178` | Single entry, no tx |
| configpublish | `config-core/slices/configpublish/service.go:162` | Single entry, no tx |

PR#68 added `Entry.Validate()` (`outbox.go:44-52`) which provides input validation. The postgres implementation (`adapters/postgres/outbox_writer.go:37-84`) performs one INSERT per call within a context-embedded transaction.

### 3.2 Design Options

#### Option A: Add WriteBatch to the Writer interface (Breaking)

```go
type Writer interface {
    Write(ctx context.Context, entry Entry) error
    WriteBatch(ctx context.Context, entries []Entry) error
}
```

**Problem**: Breaking change. Every `Writer` implementation must now implement `WriteBatch`. This includes the postgres `OutboxWriter`, any mock writers in tests, and future implementations.

#### Option B: Separate BatchWriter interface (Recommended)

```go
// BatchWriter extends Writer with batch write support.
// Implementations that support batch operations SHOULD implement this
// interface for atomic multi-entry writes within a single transaction.
//
// If an implementation does not support BatchWriter, callers can fall
// back to sequential Write calls (see WriteBatchFallback).
type BatchWriter interface {
    Writer
    // WriteBatch persists multiple outbox entries atomically within the
    // caller's transaction scope. All entries are validated before any
    // write occurs. If validation fails for any entry, no entries are
    // written and the first validation error is returned.
    //
    // Implementations SHOULD use a single batch INSERT for efficiency.
    // The context MUST carry the caller's transaction (same requirement
    // as Writer.Write).
    WriteBatch(ctx context.Context, entries []Entry) error
}

// WriteBatchFallback writes entries using the Writer interface, falling
// back to sequential Write calls if the writer does not implement
// BatchWriter. All entries are validated upfront before any write occurs.
func WriteBatchFallback(ctx context.Context, w Writer, entries []Entry) error {
    // Phase 1: Validate all entries.
    for i, e := range entries {
        if err := e.Validate(); err != nil {
            return fmt.Errorf("outbox: entry[%d]: %w", i, err)
        }
    }

    // Phase 2: Use batch if available, otherwise sequential.
    if bw, ok := w.(BatchWriter); ok {
        return bw.WriteBatch(ctx, entries)
    }
    for _, e := range entries {
        if err := w.Write(ctx, e); err != nil {
            return err
        }
    }
    return nil
}
```

**Advantages**:
- Backward compatible: existing `Writer` implementations continue to work.
- Callers that need batch can use `WriteBatchFallback` which auto-detects.
- Postgres implementation can optimize with a single multi-row INSERT.
- Validation-first semantics: no partial writes on validation failure.

#### Option C: Buffered Writer decorator

```go
type BufferedWriter struct {
    inner Writer
    buf   []Entry
}

func (bw *BufferedWriter) Write(ctx context.Context, entry Entry) error {
    bw.buf = append(bw.buf, entry)
    return nil
}

func (bw *BufferedWriter) Flush(ctx context.Context) error {
    for _, e := range bw.buf {
        if err := bw.inner.Write(ctx, e); err != nil {
            return err
        }
    }
    bw.buf = bw.buf[:0]
    return nil
}
```

**Problem**: Deferred writes break the atomicity guarantee. The caller expects `Write` to persist within the current transaction. A buffered writer that delays writes could miss the transaction commit window. This conflicts with the Outbox pattern's core guarantee.

### 3.3 Recommendation: Option B

The separate `BatchWriter` interface plus `WriteBatchFallback` helper function provides:

1. **Zero breaking changes**: Existing `Writer` interface untouched.
2. **Gradual adoption**: Callers opt-in when they need batch.
3. **Optimized path**: Postgres can use `INSERT ... VALUES ($1), ($2), ...` in `WriteBatch`.
4. **All-or-nothing semantics**: Validate all entries before any write. Since the entire batch is within one transaction, either all succeed or none do (the tx rolls back).

### 3.4 Error Semantics

**Partial success is not possible** because:
- All writes happen within a single database transaction (context-embedded tx pattern).
- If any individual INSERT fails, the transaction rolls back all writes.
- Validation happens before any INSERT, so validation errors prevent all writes.

The error contract is: `WriteBatch` either writes all entries or writes none.

### 3.5 Integration with Outbox Relay Three-Phase

The Relay (`0-B2`) polls `outbox_entries WHERE status = 'pending'`. Each entry is independently polled and published. `WriteBatch` does not affect Relay behavior -- it just means multiple rows appear atomically in the pending state. The Relay's `pollOnce` already processes entries individually, so batch-written entries are naturally handled.

### 3.6 Postgres Implementation Sketch

```go
func (w *OutboxWriter) WriteBatch(ctx context.Context, entries []outbox.Entry) error {
    tx, ok := TxFromContext(ctx)
    if !ok {
        return errcode.New(ErrAdapterPGNoTx, "outbox batch write requires a transaction")
    }

    if len(entries) == 0 {
        return nil
    }

    // Validate all entries upfront.
    for i, e := range entries {
        if e.ID == "" || !uuidPattern.MatchString(e.ID) {
            return errcode.New(errcode.ErrValidationFailed,
                fmt.Sprintf("outbox entry[%d] ID invalid: %s", i, e.ID))
        }
        if err := e.Validate(); err != nil {
            return fmt.Errorf("outbox entry[%d]: %w", i, err)
        }
    }

    // Build batch INSERT with pgx CopyFrom or multi-row VALUES.
    // CopyFrom is more efficient for large batches but VALUES is simpler.
    // For typical batch sizes (2-10 entries), multi-row VALUES is adequate.
    // ... (implementation detail)
}
```

### 3.7 Files to Change

| File | Change |
|------|--------|
| `kernel/outbox/outbox.go` | Add `BatchWriter` interface + `WriteBatchFallback` function |
| `kernel/outbox/outbox_test.go` | Tests for `WriteBatchFallback` (validation-first, fallback path, batch path) |
| `adapters/postgres/outbox_writer.go` | Implement `BatchWriter` on `OutboxWriter` |
| `adapters/postgres/outbox_writer_test.go` | Tests for batch INSERT |

---

## 4. Risk Summary

| ID | Risk | Likelihood | Impact | Mitigation |
|----|------|-----------|--------|-----------|
| R1 | CS-AR-2 breaks third-party code using `deps.Cells` | Very Low | Medium | Grep confirms zero usage. Announce in changelog. |
| R2 | CS-AR-3 "accept stdlib" decision challenged later | Low | Low | Document rationale. RouteMux can be moved later without breaking Cell interface. |
| R3 | F-OB-01 `BatchWriter` interface bloat | Low | Low | Separate interface (not on Writer); callers use `WriteBatchFallback` helper. |
| R4 | F-OB-01 partial-success confusion | Low | Medium | Document all-or-nothing semantics. Transaction guarantees prevent partial writes. |

---

## 5. Implementation Order

```
Step 1: CS-AR-2 (Dependencies minimization)
         Reason: Simplest change, zero functional impact.
         Effort: ~1h (struct change + test literal updates)
             |
Step 2: CS-AR-3 (Document net/http decision)
         Reason: Documentation only, no code risk.
         Effort: ~15min
             |
Step 3: F-OB-01 (BatchWriter interface + fallback)
         Reason: Kernel interface first, then adapter.
         Effort: ~2h (interface + tests)
             |
Step 4: F-OB-01 postgres implementation
         Reason: Requires Step 3 interface to be stable.
         Effort: ~1h (multi-row INSERT + tests)
```

All four steps can be in a single PR since they are small and independent. Total estimated effort: ~4h.

---

## 6. Relationship to SOL-B-01 and SOL-B-02

The backlog lists two related tech debts:

- **SOL-B-01** (Claimer lease renewal): Independent of this design. Affects `idempotency.Claimer`, not `outbox.Writer`.
- **SOL-B-02** (`idempotency -> outbox` dependency direction): The `Claimer` interface at `kernel/idempotency/idempotency.go:64` returns `outbox.Receipt`, creating a dependency from `kernel/idempotency` to `kernel/outbox`. This is kernel-internal and not addressed in this design, but should be considered before adding more cross-kernel-package types.

---

## Appendix: Cell Interface Size

The backlog notes _"Cell 接口 11 个方法，考虑拆分为 Cell + CellLifecycle + CellMetadata"_ (backlog.md line 209). This is related to CS-AR-2 but is a separate concern. Analysis:

Current `Cell` interface methods:
1. `ID()` -- identity
2. `Type()` -- metadata
3. `ConsistencyLevel()` -- metadata
4. `Init(ctx, deps)` -- lifecycle
5. `Start(ctx)` -- lifecycle
6. `Stop(ctx)` -- lifecycle
7. `Health()` -- runtime
8. `Ready()` -- runtime
9. `Metadata()` -- metadata
10. `OwnedSlices()` -- structure
11. `ProducedContracts()` -- structure
12. `ConsumedContracts()` -- structure

A split would look like:

```go
type Cell interface {
    ID() string
    Metadata() CellMetadata
}

type CellLifecycle interface {
    Init(ctx context.Context, deps Dependencies) error
    Start(ctx context.Context) error
    Stop(ctx context.Context) error
    Health() HealthStatus
    Ready() bool
}

type CellStructure interface {
    OwnedSlices() []Slice
    ProducedContracts() []Contract
    ConsumedContracts() []Contract
}
```

**Assessment**: Premature. The assembly needs all three facets during startup. Splitting forces `interface { Cell; CellLifecycle; CellStructure }` composite types everywhere. Recommend deferring until a concrete caller needs only a subset. The `Dependencies` minimization (CS-AR-2) is the higher-value change.
