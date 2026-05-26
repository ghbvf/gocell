# ADR-1121: Audit Chain Bootstrap Namespace Isolation

- **Date**: 2026-05-27
- **Status**: Accepted
- **Issue**: [#1121 — 审计哈希链双写入方分叉](https://github.com/ghbvf/gocell/issues/1121)
- **Supersedes / amends**: `202605101800-adr-audit-ledger-protocol.md` §D6 (NamespaceID partitioning) — addendum noting bootstrap chain as the second canonical namespace.

## Context

PR #1005 (ssobff bootstrap-audit-observer wiring) added a tamper-evident
compliance channel to the bootstrap auth-fail path: 401/429 events are
appended to the audit hash chain via `runtime/audit.AppendBootstrapAuthFail`.
Until issue #1121 the bootstrap observer wrote to **the same** `ledger.Store`
instance as the auditcore relay-driven appender:

- `cells/auditcore/internal/appender/service.go:HandleEvent` consumes
  outbox-delivered events (`event.user.created.v1`,
  `event.session.created.v1`, …) inside `s.txRunner.RunInTx` and calls
  `store.Append`.
- `runtime/audit/bootstrap_observer.go:NewBootstrapAuthFailObserver` returns a
  closure that calls `AppendBootstrapAuthFail` → `store.Append` directly in a
  detached-timeout context, with no outer business transaction.

Both pointed at the single `shared.BootstrapLedgerStore = ledgerStore`
assignment in `cmd/corebundle/audit_module.go:137`. Both used the same
`NamespaceID="auditcore"` and the same HMAC key, so both wrote into one HMAC
chain.

The ssobff walkthrough subtest `TestWalkthrough/bootstrap_auth-fail_writes_audit_chain_entry`
was `t.Skip`'d (`examples/ssobff/walkthrough_test.go:570`) pointing at this
issue. Observed symptom under contention: the DB held all three audit rows
(`bootstrap.auth.fail` + `event.session.created.v1` + `event.user.created.v1`)
but `GET /api/v1/audit/entries?eventType=bootstrap.auth.fail` returned 0.

PG `LedgerStore.Append` already engages
`pg_advisory_xact_lock(hashtextextended(namespace, 0))` plus
`SELECT … FOR UPDATE` on the tail row, so the failure mode is not a weak
single-namespace CAS. The root cause is that two writers with different
transaction shapes (outer `RunInTx` vs. detached-no-tx) operate on the same
chain, exposing the entire fork class to any future bug in the lock
implementation. The right architectural answer is to remove the shared chain.

## Decision

D1. **Bootstrap chain physical namespace isolation.** A new typed constructor
`runtime/audit.BootstrapNamespace()` returns the single-source
`ledger.NamespaceID="bootstrap"`. Composition roots construct **two**
`(Protocol, Store)` pairs — one on `NamespaceID="auditcore"`, one on
`BootstrapNamespace()`.

D2. **Sealed typed handle for the bootstrap chain.** A new struct
`*audit.BootstrapLedgerStore` wraps the bootstrap chain's `ledger.Store` with
an unexported `inner` field. `NewBootstrapLedgerStore(inner) (*BootstrapLedgerStore, error)`
rejects nil. `audit.AppendBootstrapAuthFail` and
`audit.NewBootstrapAuthFailObserver` accept this typed pointer instead of the
raw `ledger.Store` interface. Misconfiguration (passing the auditcore-namespace
store) becomes a **compile error**.

D3. **Independent HMAC keys per chain.** Composition roots load two
independent HMAC keys (`GOCELL_AUDITCORE_HMAC_KEY` and
`GOCELL_AUDIT_BOOTSTRAP_HMAC_KEY`). In real adapter mode the bootstrap key is
required (no fallback); in dev mode separate demo defaults are registered in
`wellKnownDemoKeys` so both keys are rejected if they appear in production.

D4. **Read-side aggregator with narrow interface.** `runtime/audit/ledger`
adds a `QueryStore` interface containing only `Query(...)`. A new
`ledger.MultiStore` implements **only** `QueryStore` (not the full `Store`
surface). `cells/auditcore.WithQueryStore(s ledger.QueryStore)` injects the
aggregator into the auditquery slice; `cells/auditcore/slices/auditquery`'s
`Service.store` field is typed `ledger.QueryStore`. The appender slices keep
`WithLedgerStore(s ledger.Store)` for writes — injecting MultiStore there is a
**compile error** because MultiStore does not satisfy `Store`.

D5. **Bootstrap chain is L1 LocalTx.** No outbox emit follows
`AppendBootstrapAuthFail`; the chain is a single-transaction PG write fenced
by the existing per-namespace advisory lock. The `L2-OUTBOX-ATOMICITY-COVERAGE-01`
archtest does not apply (and the bootstrap path is intentionally excluded).

D6. **No schema migration.** The existing `audit_entries.namespace TEXT NOT NULL`
column plus `UNIQUE (namespace, seq_no)` and `UNIQUE (namespace, event_id)`
indexes (migrations 020/021) already partition the chain. PG advisory locks
key on `hashtextextended(namespace, 0)`, so distinct namespaces never contend
on the same lock slot.

## Open-source benchmark

Each decision matches an established pattern in the field:

| Source | Pattern | Decision it informs |
|---|---|---|
| Google Trillian `storage/log_storage.go` / `log/sequencer.go` | Per-`tree_id` physical column partitioning + `ReadWriteTransaction(ctx, *trillian.Tree, ...)` typed-pointer that makes cross-tree writes structurally unrepresentable | D1, D2 |
| Kubernetes `k8s.io/apiserver/pkg/audit/union.go` `Union(backends ...Backend) Backend` | Pure read-side fan-out aggregator; each Backend stays independent at write time | D4 |
| HashiCorp Vault `vault/audit/broker.go` + `audit/backend.go` per-device `Salt` | Independent HMAC state per audit device; broker dispatches to every device; no shared chain | D3 |
| Watermill `message/pubsub.go` | No cross-producer write-side ordering primitive; serialization is the store layer's job | confirms D1 (don't try to merge writes at the bus layer) |
| PostgreSQL `pg_advisory_xact_lock` canonical pattern | Transaction-scoped, auto-released, cluster-wide unique slot per int64 key | confirms D6 (existing PG store is correct under one namespace; the fix is two namespaces) |

## Consequences

### Positive

- The dual-writer fork bug is removed by construction — two writers with
  different transaction shapes can no longer collide on shared chain state
  because they no longer share chain state.
- The Hard upstream guarantees (D2 + D4) catch misconfiguration at compile
  time instead of at the first 401/429 or the first concurrent burst.
- Vault-style per-device HMAC key isolation (D3) means compromise of either
  chain's signing key cannot forge entries in the other chain.
- Operators continue to query bootstrap auth failures with the existing
  `GET /api/v1/audit/entries?eventType=bootstrap.auth.fail` filter; no new
  HTTP wire field is introduced.

### Negative / accepted costs

- Composition root now constructs **two** `ledger.Store` instances and a
  MultiStore aggregator instead of one store. The audit module is ~50 lines
  longer; the helper extraction (`buildAuditProtocol`, `buildAuditStores`)
  keeps cognitive complexity ≤ 15.
- auditquery's read path executes one Query per backing chain (worst case
  O(2 · FetchLimit) rows merged per page); the aggregator returns the
  Limit+1 sentinel for N+1 hasMore detection as before. The merge is
  in-memory by `(timestamp DESC, id ASC)`; cursor pagination remains exact.
- Production deployments must set `GOCELL_AUDIT_BOOTSTRAP_HMAC_KEY` in real
  adapter mode. Operators upgrading from a prior version must mint and inject
  this secret before rolling out — no fallback to the auditcore key (per
  CLAUDE.md / `feedback_no_soft_fallback`).
- Existing `audit_entries` rows written before the rollout remain on
  `namespace="auditcore"`. New bootstrap events land on `namespace="bootstrap"`.
  Pre-rollout `bootstrap.auth.fail` entries (if any exist in deployed audit
  tables) stay queryable via `/api/v1/audit/entries?eventType=bootstrap.auth.fail`
  because MultiStore aggregates both namespaces. There is no backfill or
  migration of historical rows; pre-v1.0 evolution rules permit this
  one-shot transition.

## Threat model

| Threat | Pre-fix | Post-fix |
|---|---|---|
| Concurrent appender + observer write produces forked chain | ⚠ Real (issue #1121 reproducer) | ✅ Impossible — separate chains, no shared tail |
| Single HMAC key compromise forges entries in both event classes | ⚠ Yes | ✅ Independent keys; compromise of either does not extend to the other (Vault per-device salt) |
| MultiStore accidentally injected as a write store | n/a (MultiStore did not exist) | ✅ Compile error — MultiStore does not satisfy `ledger.Store` |
| Auditcore-namespace store accidentally injected as bootstrap observer dep | ⚠ Same store every time (no signal) | ✅ Compile error — `*BootstrapLedgerStore` is the only accepted handle |
| Composition root forgets to construct the bootstrap chain entirely | n/a (only one chain) | ✅ archtest `AUDIT-NS-DISJOINT-01` fails the build; the typed `*BootstrapLedgerStore` field on `SharedDeps` is never populated; the access module's observer construction fails fast at startup |
| Bootstrap observer detached timeout exceeds budget | Same as pre-fix (2s cap) | Same (unchanged) |

## AI-robust enforcement

| Mechanism | Carrier | Grade |
|---|---|---|
| `*audit.BootstrapLedgerStore` typed parameter in `AppendBootstrapAuthFail` + `NewBootstrapAuthFailObserver` | Go type system (single sanctioned holder pattern, ai-robust §Hard 范本目录) | **Hard upstream** |
| `ledger.MultiStore` implements only `ledger.QueryStore`, never `ledger.Store` | Go type system (narrow interface — input-struct field exclusion analog) | **Hard downstream** |
| `cmd/corebundle/**` production must call both `audit.BootstrapNamespace()` and `ledger.ParseNamespaceID(<non-"bootstrap">)` | archtest `AUDIT-NS-DISJOINT-01` (`tools/archtest/bootstrap_audit_namespace_disjoint_test.go`) | **Medium** (caller-identity scan; the type-system Hard layers already preclude single-chain wiring through wrong types, this backstops "single namespace, two protocols" composition mistakes) |
| HMAC demo keys rejected in real mode | `wellKnownDemoKeys` registry + `rejectDemoKey` (existing pattern) | Pre-existing Medium |

## Alternatives considered

(a) **auditcore cell holds two stores and fans out in the slice layer.**
Rejected: leaks cross-chain wiring knowledge into the cell, breaking the
established "cell is single-namespace owner" boundary.

(b) **Extend `Store.Query` to accept a list of namespaces.** Rejected:
triggers the full 5-carrier contract fanout (interface + PG impl + mem impl
+ conformance suite + auditquery contract + every existing test). High
blast radius for a minor read-side reshape.

(c) **Single namespace plus tightened chain-tail CAS.** Rejected: PG
`pg_advisory_xact_lock + SELECT FOR UPDATE` is already correct under one
namespace; tightening is a no-op against the actual failure shape (two
writers with different transaction shapes), and does not address the
defense-in-depth value of physical chain separation.

(d) **Loosen verify-read semantics so forked-chain rows are still returned.**
Rejected: weakens the fail-closed compliance guarantee — a tamper-detected
chain would silently surface in operator queries.

## Cross-refs

- ADR `202605101800-adr-audit-ledger-protocol.md` §D6 — NamespaceID is the
  canonical chain partition key; this ADR introduces the second namespace.
- archtest `BOOTSTRAP-AUDIT-OBSERVER-FUNNEL-DOWNSTREAM-HARD-01` /
  `BOOTSTRAP-AUDIT-OBSERVER-FUNNEL-UPSTREAM-MEDIUM-01` — observer-construction
  funnel; signature update lands in this PR.
- archtest `AUDIT-NS-DISJOINT-01` — new Medium archtest backstop.
- `k8s.io/apiserver/pkg/audit/union.go`, `google/trillian/storage/log_storage.go`,
  `hashicorp/vault/audit/broker.go` — external benchmark references.
