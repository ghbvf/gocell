# ADR: Cell-level Repo Readiness Probe — typed funnel + real-failure conformance

- Status: Accepted
- Date: 2026-05-16
- Tracks: REPO-HEALTHCHECKER-01 (backlog cap-13 §13.1) + B2-R-02 (backlog.md line 228)
- Builds on: PR #485 (PR-8, A-01) — `adapters/postgres.Pool` implements `lifecycle.ManagedResource` + `postgres_ready` pool probe; PR #450 (S7) — auditcore `ledger.Store` + `LedgerStore.Probes()` partial pre-coverage (F6)
- Implemented by: PR-REPO-READYZ (branch `fix/202-repo-readyz`)

## Context

### The gap before this ADR

After PR #485 the readyz surface had one pool-level probe registered by `adapters/postgres.*Pool`:

- `postgres_ready` — bare `Ping` to the PostgreSQL pool; proves the TCP connection is alive.

Three platform cells (configcore / accesscore / auditcore) expose business-critical relations
(`config_entries`, `feature_flags`, `sessions`, `audit_entries`).  A pool Ping cannot detect:

- A migration that was not applied (missing table or column).
- A table-level permission revocation (`REVOKE SELECT ON sessions FROM app_user`).
- A schema drift that breaks the representative query used by the cell's service layer.

These failure modes produce a green `postgres_ready` probe and a broken service — the exact
class of silent failure that readyz probes are meant to surface.

**configcore**: had no repo probe at all (backlog B2-R-02).

**accesscore** (`session_store_ready`): an anonymous duck-type `interface{ Health(context.Context) error }`
was wired into the cell's HealthCheckers, but the concrete `*SessionStore` type never
implemented that interface.  The duck-type assignment compiled (Go structural typing) but
matched nothing at runtime: the probe was registered as dead code and never fired (real bug,
introduced without test coverage, caught only during PR-REPO-READYZ code review).

**auditcore** (`audit_ledger_ready`): partially covered by PR #450 F6, which introduced
`LedgerStore.Probes()` returning a `HealthProber` via a special-cased code path in
`cells/auditcore/cell.go`.  The shape differed from the other cells and required
per-cell branching logic.  No conformance harness existed to verify the probe fired under
real failure conditions.

### Backlog items

- `REPO-HEALTHCHECKER-01` (cap-13 §13.1 line 22): configcore/auditcore repo接 HealthCheckers.
- `B2-R-02` (backlog.md line 228): Readyz 缺少 repo probe — configcore/auditcore HealthCheckers 仅接 outbox.

Both were tagged P1/Cx2 and explicitly cross-referenced ("同 PR").

## Decision

### D1 — Differentiated repo probe, not merged into `postgres_ready`

Pool-level `postgres_ready` (bare Ping) and cell-level repo probes have distinct failure
domains.  Merging them would produce a probe that fires on connection loss AND on schema
drift — two unrelated operational events with different remediation paths.  The
`observability.md` rule "禁止同时暴露多个同义 ready probe" applies to probes with the
**same** failure domain; differentiated probes serving different diagnostic purposes are
not synonymous duplicates.

### D2 — `kernel/cell.RepoHealthProber` interface + `cell.RegisterRepoReadiness` typed funnel

A new interface is introduced in `kernel/cell`:

```go
// RepoHealthProber is implemented by any store that can report its own
// schema/relation health via a representative query.
type RepoHealthProber interface {
    RepoReady(ctx context.Context) error
}
```

Registration goes through a single typed funnel:

```go
func RegisterRepoReadiness(reg Registry, name string, prober RepoHealthProber)
```

This is the **only approved form** for cell-level repo readiness registration.
The funnel provides Hard form-uniqueness (see §AI-rebust below): any call site that is not
`cell.RegisterRepoReadiness(reg, name, prober)` either fails to compile (wrong type) or
is caught by archtest `CELL-REPO-READYZ-PROBE-01`.

### D3 — Real-failure conformance harness as the load-bearing differentiated-property carrier

`kernel/cell/celltest.RunRepoReadinessConformance` is a test helper that exercises three
scenarios against any `RepoHealthProber` implementation:

1. **Healthy** — probe returns `nil`.
2. **Table dropped** (PG only) — `DROP TABLE` in a test transaction; probe returns non-nil.
3. **In-memory store** — skip (memory stores have no schema to drop).

This harness is the load-bearing carrier for the differentiated property: scenario 2 is
the exact failure mode that `postgres_ready` cannot detect.  Without this harness, the
probe could exist as dead code (the accesscore regression) or return `nil` unconditionally
(a trivial implementation passing archtest's form check without being correct).

### D4 — Unify all three cells onto the single funnel; delete `LedgerStore.Probes()` special path

All three cells now register their repo probe identically via `cell.RegisterRepoReadiness`:

| Cell | Probe name | RepoReady implementation |
|------|-----------|--------------------------|
| configcore | `config_repo_ready` | `ConfigRepository.RepoReady` — queries `config_entries` + `feature_flags` |
| accesscore | `session_store_ready` | `session.Store.RepoReady` — queries `sessions` |
| auditcore | `audit_ledger_ready` | `ledger.Store.RepoReady` — reuses `Tail` to query `audit_entries` |

The `LedgerStore.Probes()` special-case path introduced in PR #450 F6 is **deleted**.
No backward-compat shim: GoCell has no external consumers; Review and重构 do not consider
backward compatibility (CLAUDE.md).

### D5 — No anonymous duck-type wiring

`reg.Health(name, prober)` called directly with an anonymous
`interface{ Health(context.Context) error }` is forbidden for repo probes.  The accesscore
regression (dead probe via structural typing) demonstrates the failure mode: Go's structural
typing accepts the assignment at compile time, producing a probe that is registered but
never executes the intended query.  The typed funnel (`cell.RegisterRepoReadiness`) requires
the concrete type to explicitly implement `RepoHealthProber`, making the registration
verifiable by the compiler and by `RunRepoReadinessConformance`.

## AI-rebust Rating

### Funnel 双向锁评级 (per `ai-collab.md` §"Funnel 双向锁评级")

| Dimension | Grade | Evidence |
|-----------|-------|----------|
| **Downstream Hard** — only `cell.RegisterRepoReadiness` may register a repo probe for a cell | **Hard** (form-uniqueness) | Archtest `CELL-REPO-READYZ-PROBE-01` N1/N2 enforce that every `reg.Health` call with a const-string name in cells/ production files must be routed through `cell.RegisterRepoReadiness`.  Any bypass (bare `reg.Health`, anonymous duck-type, const-ident first arg) fails archtest.  Scope: cells/ only — registration is a cell-init responsibility. |
| **Upstream (conformance auto-join)** — every `RepoHealthProber` implementation must be exercised by `RunRepoReadinessConformance` | **Medium** (archtest wired-conformance backstop) | Archtest `CELL-REPO-READYZ-PROBE-01` P1 checks that each type implementing `RepoHealthProber` appears in a `RunRepoReadinessConformance` call in the test tree.  A new implementation that omits conformance will fail archtest at CI time — not at compile time (Go cannot require a test to exist).  Note: harness **behavioral correctness** is Hard (scenario 2, DROP TABLE → non-nil, cannot be satisfied by a no-op); it is harness **wiring presence** that is Medium upstream. |

**Combined posture**: Hard downstream + Medium upstream.  Per charter: "允许 Medium 上游 + Hard 下游的过渡形态，但必须同步登记 backlog 显式 Hard 化任务."  Backlog item `REPO-READYZ-UPSTREAM-FUNNEL-HARD-01` is registered to track the upgrade path (sealed interface or codegen marker forcing conformance wiring at compile time).

### Full AI-rebust table

| Layer | Carrier | Grade | Failure mode blocked |
|-------|---------|-------|----------------------|
| Typed funnel `cell.RegisterRepoReadiness` | Go type system — `RepoHealthProber` interface | **Hard** | Anonymous duck-type bypass (accesscore regression class) |
| `RunRepoReadinessConformance` real-failure harness | Integration test + real PG DROP TABLE | **Hard** (behavioral max) | Trivial `return nil` implementation passing form check but not detecting schema drift |
| Archtest `CELL-REPO-READYZ-PROBE-01` N1/N2 (cells/ scope) | AST + types.Info form lock | **Medium** (archtest backstop) | Bare `reg.Health` bypass in cells/ (BasicLit or const-ident first arg); adapters/runtime not scanned (not cell-init callers) |
| Archtest `CELL-REPO-READYZ-PROBE-01` P1 (cells/adapters/runtime scope) | types.Implements + test-corpus scan | **Medium** (archtest backstop) | New `RepoHealthProber` implementation missing conformance wiring |

The conformance harness is rated **Hard (behavioral max)** because scenario 2 (DROP TABLE → non-nil) cannot be satisfied by a no-op implementation; it requires the concrete store to execute a real query against the test database.  This is the highest behavioral grade reachable for correctness properties that cannot be expressed as types.

N1/N2 scope note: these rules scan `cells/` production files only.  Registration of repo probes is a cell-init responsibility; `adapters/` and `runtime/` packages do not call `cell.Registry.Health` for repo probes and are correctly excluded from N1/N2.  P1 scans the broader `cells/ + adapters/ + runtime/` corpus for `RepoHealthProber` implementations regardless.

### AI-rebust honest caveats

- Archtest `CELL-REPO-READYZ-PROBE-01` is archtest-bound, not compile-time.  An AI session editing the archtest allowlist or the probe-name pattern list in the same PR could bypass it; the reviewer must backstop the topmost meta layer (ai-collab.md §"meta-governance").
- The upstream Medium grade means a new `RepoHealthProber` implementation that adds itself to `cell.RegisterRepoReadiness` but omits `RunRepoReadinessConformance` will be caught by archtest at CI time, not at compile time.

## Consequences

**Positive**:
- Three platform cells all have differentiated repo probes that fire on schema drift and permission loss — failure modes invisible to `postgres_ready`.
- The accesscore dead-probe regression class is eliminated: `cell.RegisterRepoReadiness` requires the concrete type to satisfy `RepoHealthProber`, which is verified by `go build` + conformance test.
- A single reusable pattern (`RegisterRepoReadiness` + `RunRepoReadinessConformance`) is available for future cells and example cells.
- `LedgerStore.Probes()` special-case path deleted — one fewer divergent wiring shape.

**Negative / accepted costs**:
- `kernel/cell/celltest.RunRepoReadinessConformance` requires a live PostgreSQL test database for the DROP TABLE scenario; it is gated by the `integration` build tag and runs in the CI integration-test job only, not in the unit-test job.
- Each new cell store implementing `RepoHealthProber` must be explicitly added to `RunRepoReadinessConformance` coverage; archtest enforces this but does not make it free.

## Alternatives rejected

### Alt-A: Close as "covered by `postgres_ready`"

Rejected.  Pool Ping covers connection liveness only.  The explicit accesscore dead-probe
regression and the configcore probe gap demonstrate that "covered" was not factually true.
The differentiated failure domain (schema/migration/permission vs. TCP connection) is the
load-bearing argument; without it the probe would not exist.

### Alt-B: Keep anonymous duck-type wiring, add a test

Rejected.  The accesscore regression was an anonymous duck-type that compiled cleanly and
had no test.  Adding a test does not eliminate the structural-typing ambiguity: Go may
accept a future refactoring of the store type that drops the method without a compile
error at the wiring site.  The typed funnel (`RepoHealthProber` explicit interface) makes
the wiring verifiable at compile time.

### Alt-C: Keep `LedgerStore.Probes()` and add similar special paths for the other cells

Rejected.  Three divergent wiring shapes produce three archtest rules, three conformance
harness shapes, and three onboarding docs.  The unified funnel is strictly simpler and
reduces the probability of a future cell implementing a fourth divergent shape.

### Alt-D: Separate optional interface not embedded in Store (ISP-pure design)

Interface Segregation Principle (ISP) would suggest that `RepoReady(ctx) error` is a
readiness-plane concern separate from domain store methods (`Get`, `Create`, etc.) and
should live on a thin, optional adapter rather than being embedded in the broad `Store`
interface.

Rejected for two reasons:

1. **Unified conformance completeness**: embedding `RepoHealthProber` in the broad store
   interface means every concrete store (PG + mem) must implement the method at compile
   time.  With a separate optional interface, new stores can forget to implement it, and
   the omission is only caught when `cell.RegisterRepoReadiness` is called — or worse,
   when the probe is registered against a wrong type via structural typing (the exact
   regression class this ADR eliminates).  The accepted ISP cost (slightly wider
   interfaces) buys a compile-time completeness guarantee.

2. **Single conformance harness**: a separate optional interface would require a second
   type assertion or discovery mechanism in `RunRepoReadinessConformance`, increasing
   harness complexity.  The current uniform `RepoHealthProber` parameter keeps the
   harness simple and its behavioral correctness grade Hard.
