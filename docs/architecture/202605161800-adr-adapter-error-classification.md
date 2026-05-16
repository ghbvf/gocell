# ADR: Adapter transient-error classification via the errcode.WrapInfra Hard funnel

- Date: 2026-05-16
- Status: Accepted
- Refs: backlog `ADAPTER-ERROR-CLASSIFICATION-TRANSIENT-01` (cap-x-cross); plan
  `docs/plans/202605121830-038-p0-p1-blocking-implementation-plan.md` Wave 4
- Enforcement: archtest `ADAPTER-ERROR-CLASSIFICATION-TRANSIENT-01`

## Context

`adapters/{postgres,redis,s3}` returned flat `KindInternal` errors with no
transient-vs-permanent distinction. Outbox consumers therefore could not make a
backoff/DLX decision: `cells/auditcore/internal/appender/service.go` blind-
`Requeue`d every store failure, so a permanent schema/marshal error burned the
whole retry budget before reaching the DLX. The backlog trigger is "第 1 个
handler disposition 收口".

`errcode.IsTransient` previously keyed on a single Vault-specific code string
(`ErrKeyProviderTransient`), unusable by other adapters.

## Decision

A single typed funnel **`errcode.WrapInfra(code, message, cause, opts…)`** is
the only constructor that produces a transient (retry-safe) error. It sets
`KindUnavailable` (HTTP 503) + `CategoryInfra` + a **private** `Error.transient`
marker. There is no parallel `ErrAdapter*Transient` code set — transient-vs-
permanent is the single Kind+marker axis, queryable in metrics by Kind.

`errcode.IsTransient` is generalized to a single predicate (no parallel
`IsAdapterTransient`):

- `*errcode.Error` positive branch keys **only** on the private marker;
- plus raw recognizers for un-wrapped low-level errors: `context.DeadlineExceeded`
  (not `context.Canceled` — the caller gave up), `net.Error.Timeout()` (the
  modern replacement for the deprecated `net.Error.Temporary()`, golang/go
  #45729), and `errors.As(interface{ RetryableError() bool })` (the
  pgconn.SafeToRetry / aws-sdk-go-v2 RetryableError idiom).

The old `ec.Code == ErrKeyProviderTransient` branch is removed and
`adapters/vault.classifyVaultError` migrated onto `WrapInfra` — **single truth
source**, no dual recognition mechanism, no transitional shim.

Per-adapter `classify{Postgres,Redis,S3}Error` stay in their adapter packages
(they need `pgconn` / go-redis / smithy SDK types; adapters cannot depend on
each other) and route their transient branch through `errcode.WrapInfra`,
modeled on the pre-existing `adapters/vault.classifyVaultError` precedent.

Adapter transient inventory:

| Adapter | Transient when |
|---------|----------------|
| postgres | `pgconn.SafeToRetry`; SQLSTATE `40001` / `40P01`; class `08*`; ctx deadline |
| redis | net timeout / ctx deadline / `i/o timeout`; server-recovering `CLUSTERDOWN` / `LOADING` / `TRYAGAIN` / `MASTERDOWN` |
| s3 | HTTP 429 / 408 / 5xx; net timeout / ctx deadline |

Consumer demonstrator: the auditcore appender now Requeues a positively-
transient error, Rejects a positively-permanent classified error
(domain/validation/auth — not infra) to DLX, and keeps unknown/ambiguous infra
errors on the Requeue (retry-then-budget-DLX) path — fail-closed toward not
losing an event on a transient blip. Mirrors the `configreceive` precedent.

## Funnel double-lock grading (ai-collab.md §"Funnel 双向锁")

| Side | Mechanism | Grade |
|------|-----------|-------|
| Upstream | transient marker producible only via `WrapInfra`; the field is unexported (Go type system forbids any package outside `pkg/errcode` from setting it) + archtest locks the in-package writer to func `WrapInfra` | **Hard** (type system + form-uniqueness archtest, the `panicregister.Approved` 范本) |
| Downstream | `IsTransient`'s `*Error` positive branch keys only on the private marker — a transient-looking code built via `New`/`Wrap` is type-inexpressibly not transient | **Hard** |
| Adapter routing presence | archtest `RunTypedProduction` resolves (via `*types.Info`) that each of postgres/redis/s3 calls `errcode.WrapInfra` | **Medium→Hard** (type-aware, fail-on-deviation) |

Declared blind spot (compensated, not silent): archtest does **not** enforce
that *every* adapter error site calls its `classify…`. An unclassified error
carries no marker → `IsTransient` false → consumer Requeues (retry-then-budget-
DLX) — the fail-closed safe degradation (never "wrongly retried-then-lost"). The
broad no-bare-error sweep (covering oidc/websocket) is a separate larger rule
outside this Cx3 PR. Reverse self-check:
`TestAdapterErrorClassificationTransient01_FixturePattern` loads a real
build-tag-gated package and asserts the two non-WrapInfra marker writes are
reported (exact count, both false-negative and false-positive drift caught).

## Coverage / threat re-eval

| Concern | Before | After |
|---------|--------|-------|
| transient adapter error → Requeue | ❌ blind Requeue all | ✅ marker-driven |
| permanent error burns retry budget | ❌ | ✅ positively-permanent → Reject (auditcore) |
| dual truth source (code string vs marker) | n/a | ✅ single marker; vault migrated; old branch deleted |
| transient-looking error forged outside funnel | ⚠️ any code | ✅ unexported field + archtest = Hard |
| oidc/websocket adapters classified | ❌ | ❌ (declared out of scope; fail-closed safe) |
| classifier unknown-error default symmetry | ⚠️ vault step-4 was fail-open (any unknown → transient) | ✅ all four classifiers fail-closed-on-unknown: vault step-4 now requires `*net.OpError`/`net.Error`, else permanent — symmetric with classifyPG/Redis/S3 |

## Consequences

- Adapters emit semantically richer errors; consumers branch on
  `errcode.IsTransient`.
- New adapter classifiers must route transient through `errcode.WrapInfra` or
  the archtest fails CI.
- `net.Error.Temporary()` is never used (deprecated); the codebase standardizes
  on `Timeout()` + `errors.As`.

ref: jackc/pgx `pgconn` SafeToRetry; aws/aws-sdk-go-v2 `aws/retry`
RetryableConnectionError/RetryableHTTPStatusCode; ThreeDotsLabs/watermill
retry middleware (caller-decides predicate); golang/go #45729.
