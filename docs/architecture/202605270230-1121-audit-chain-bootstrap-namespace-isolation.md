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
- `runtime/audit/bootstrap_observer.go:NewBootstrapAuthFailObserver` returned a
  closure that called `AppendBootstrapAuthFail` → `store.Append` directly in a
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
rejects nil. `audit.AppendBootstrapAuthFail` accepts this typed pointer instead of the
raw `ledger.Store` interface. Misconfiguration (passing the auditcore-namespace
store) becomes a **compile error**.

Note: `audit.NewBootstrapAuthFailObserver` has been deleted in Wave-1 #1423.
The bootstrap auth-fail flow is now event-driven (see §Amendment 2026-06-02).
`AppendBootstrapAuthFail` remains and is called by `auditappendbootstrap` subscriber.

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

D5. **Bootstrap chain write path is L2 OutboxFact (Wave-1 #1423 amendment).** The
bootstrap auth-fail path is now event-driven: accesscore emits
`event.auth.bootstrap-failed.v1` inside a transaction via the L2 outbox; the
auditcore `auditappendbootstrap` subscriber consumes the event and calls
`AppendBootstrapAuthFail`. See §Amendment 2026-06-02 for full details.

The original D5 ("Bootstrap chain is L1 LocalTx") described the pre-Wave-1
behaviour where `AppendBootstrapAuthFail` was called directly in a detached
context; that characterisation no longer applies. The new path is L2
(durable outbox tx → relay → subscriber Append), which is strictly more
durable than the previous best-effort direct write.

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
- (Wave-1 #1423) The bootstrap write path is now L2 outbox-durable: the
  outbox row persists across process restarts, and relay delivers it to
  auditcore even if the subscriber is briefly unavailable. This is strictly
  more durable than the prior detached-timeout best-effort direct write.

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
- (Wave-1 #1423) Bootstrap audit entries are now written asynchronously
  (relay-delivered). The compliance channel is more durable but no longer
  synchronous with the 401/429 response. Accepted trade-off: compliance
  channels prioritise durability over immediacy; the outbox row is persisted
  in the same transaction that returns 401/429.

## Threat model

The table below reflects the **post-Wave-1 #1423 state** — the bootstrap
write path is now event-driven. Each row has been re-evaluated against the
amendment; rows where the handoff path change is relevant are noted.

| Threat | Pre-fix (original #1121) | Post-fix (original) | Post-Wave-1 #1423 |
|---|---|---|---|
| Concurrent appender + observer write produces forked chain | ⚠ Real (issue #1121 reproducer) | ✅ Impossible — separate chains, no shared tail | ✅ Unchanged — bootstrap writes go through a separate subscriber, not a concurrent in-process appender; fork class is still impossible |
| Single HMAC key compromise forges entries in both event classes | ⚠ Yes | ✅ Independent keys; compromise of either does not extend to the other (Vault per-device salt) | ✅ Unchanged — two independent HMAC keys, both held inside `cellmodules/auditcore`, never passed across the `CellModule.Provide` boundary |
| MultiStore accidentally injected as a write store | n/a (MultiStore did not exist) | ✅ Compile error — MultiStore does not satisfy `ledger.Store` | ✅ Unchanged |
| Auditcore-namespace store accidentally injected as bootstrap dep | ⚠ Same store every time (no signal) | ✅ Compile error — `*BootstrapLedgerStore` is the only accepted handle | ✅ Unchanged — `AppendBootstrapAuthFail` still requires `*BootstrapLedgerStore`; now called only from `auditappendbootstrap` slice (auditcore-internal) |
| Composition root forgets to construct the bootstrap chain entirely | n/a (only one chain) | ✅ archtest `AUDIT-NS-DISJOINT-01` fails; typed `*BootstrapLedgerStore` nil → observer construction fails fast | ✅ `cellmodules/auditcore.Provide` constructs `*BootstrapLedgerStore` and passes it to `auditcell.WithBootstrapStore` — failure is build-time not startup-time; the store is auditcore-internal, no cross-module nil is possible |
| accesscore directly writes auditcore ledger (cross-cell coupling) | ⚠ Yes — via in-process handle passed through ModuleExports | ✅ Mitigated by typed handle + module order archtest | ✅ **Eliminated** — `ModuleExports` type deleted; `IMPL-DECL-COVER-01` archtest forbids `cells/accesscore` importing `cells/auditcore`; `MODULE-PROVIDE-NO-VALUE-HANDOFF-01` archtest freezes the Provide signature to have no cross-module value handoff; direct write is structurally unrepresentable |
| Bootstrap observer detached timeout exceeds budget | Same as pre-fix (2s cap) | ✅ Same (unchanged) | ✅ Timeout budget is still 2s, but now caps the outbox emit write (not a ledger append); the ledger append happens in the subscriber and is not subject to the accesscore 2s window |
| Bootstrap audit event lost (relay or subscriber outage) | n/a (synchronous write) | n/a | ✅ L2 outbox durability: the outbox row persists in the same tx as the 401/429 response; relay retries until the subscriber acks; `auditappendbootstrap` handler Requeues on transient errors and Rejects (DLX) only on permanent unmarshal failures |
| Bootstrap auth-fail event emitted without a tx (ErrAdapterPGNoTx) | n/a | n/a | ✅ `setup.Service.RecordBootstrapAuthFail` wraps the emit in `txRunner.RunInTx`; `adapters/postgres.OutboxWriter.Write` rejects writes outside a tx with `ErrAdapterPGNoTx` — fail-fast at runtime |

## AI-robust enforcement

| Mechanism | Carrier | Grade |
|---|---|---|
| `*audit.BootstrapLedgerStore` typed parameter in `AppendBootstrapAuthFail` | Go type system (single sanctioned holder pattern, ai-robust §Hard 范本目录) | **Hard upstream** |
| `ledger.MultiStore` implements only `ledger.QueryStore`, never `ledger.Store` | Go type system (narrow interface — input-struct field exclusion analog) | **Hard downstream** |
| `CellModule.Provide` signature frozen: no cross-module value handoff channel | archtest `MODULE-PROVIDE-NO-VALUE-HANDOFF-01` (reflect interface method signature freeze; re-introducing a handoff requires changing the signature → archtest breaks) | **Hard** (see ADR-1085 §Amendment 2026-06-02) |
| `cells/accesscore` cannot import `cells/auditcore` | archtest `IMPL-DECL-COVER-01` (cross-cell import ban) | **Hard downstream** / **Medium upstream** (Go package visibility ceiling, gh #1282) |
| `cmd/corebundle/**` production must call both `audit.BootstrapNamespace()` and `ledger.ParseNamespaceID(<non-"bootstrap">)` | archtest `AUDIT-NS-DISJOINT-01` (`tools/archtest/bootstrap_audit_namespace_disjoint_test.go`) | **Medium** (caller-identity scan; the type-system Hard layers already preclude single-chain wiring through wrong types, this backstops "single namespace, two protocols" composition mistakes) |
| HMAC demo keys rejected in real mode | `wellKnownDemoKeys` registry + `rejectDemoKey` (existing pattern) | Pre-existing Medium |

**Retired archtests (Wave-1 #1423)**:

- `BOOTSTRAP-AUDIT-OBSERVER-FUNNEL-DOWNSTREAM-HARD-01` — retired. The observer no longer writes the ledger; the emit path (accesscore → outbox) is covered by `EMIT-DECL-COVER-01` (existing). The subscriber write path is covered by the event contract + `auditappendbootstrap` conformance test.
- `BOOTSTRAP-AUDIT-OBSERVER-FUNNEL-UPSTREAM-MEDIUM-01` — retired for the same reason. The funnel concept (observer → AppendBootstrapAuthFail) no longer exists; `AppendBootstrapAuthFail` is now called exclusively from the auditcore-internal subscriber slice.

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

(e) **HTTP contract between accesscore and auditcore (rejected for Wave-1).**
Rejected: introduces a circular dependency `accesscore → auditcore → accesscore`
(auditcore already subscribes to `event.session.created.v1` from accesscore),
triggering the DEP-02 cycle detected by `go run ./cmd/gocell validate`. The
event-based approach (accesscore publishes, auditcore subscribes) keeps the
dependency edge `auditcore → accesscore` consistent with the existing session
event direction and avoids the cycle. See plan `.claude/plans/1423-issues-foamy-orbit.md`
§"设计裁决历程" for full rationale.

## Cross-refs

- ADR `202605101800-adr-audit-ledger-protocol.md` §D6 — NamespaceID is the
  canonical chain partition key; this ADR introduces the second namespace.
- ADR `202605311000-1085-adr-cellmodule-composition-public-api.md` §Amendment 2026-06-02
  — `ModuleExports` deletion; `CellModule.Provide` signature freeze;
  archtest `MODULE-PROVIDE-NO-VALUE-HANDOFF-01`.
- archtest `AUDIT-NS-DISJOINT-01` — Medium archtest backstop for namespace disjointness.
- Contract `contracts/event/auth/bootstrap-failed/v1/contract.yaml` — the new
  event contract that carries bootstrap auth-fail facts from accesscore to auditcore.
- `k8s.io/apiserver/pkg/audit/union.go`, `google/trillian/storage/log_storage.go`,
  `hashicorp/vault/audit/broker.go` — external benchmark references.

---

## Amendment 2026-06-02 #1423 — Bootstrap write path: in-process handle → event

**Summary**: The in-process `BootstrapLedgerStore` handoff from auditcore to
accesscore has been removed. The bootstrap auth-fail write path is now fully
event-driven and auditcore-internal.

**Before (original #1085 / #1121 design)**:

```
/setup/admin 401/429
  → runtime/auth bootstrap middleware
  → audit.NewBootstrapAuthFailObserver closure (in accesscore composition)
       → detached 2s ctx
       → audit.AppendBootstrapAuthFail(ctx, bootstrapStore, clk, reason, ip)
            → bootstrap chain ledger.Store.Append (direct write, no outbox)
```

`bootstrapStore` was the `*audit.BootstrapLedgerStore` produced by
`cellmodules/auditcore.Provide` and passed to `cellmodules/accesscore.Provide`
via `composition.ModuleExports`. This constituted a cross-cell in-process
Go handle, violating the GoCell rule that L1+ cross-cell interactions must
go through a contract.

**After (Wave-1 #1423 event design)**:

```
/setup/admin 401/429
  → runtime/auth bootstrap middleware
  → bootstrapAuthObserver closure (constructed in cellmodules/accesscore)
       → slog.Error "bootstrap_auth_failed" (SRE channel, always emitted)
       → detached 2s ctx
       → cells/accesscore/slices/setup.Service.RecordBootstrapAuthFail(ctx, reason, ip)
            → txRunner.RunInTx {
                outbox.Emit(event.auth.bootstrap-failed.v1, {reason, clientIp, eventId, occurredAt})
              }  [L2 outbox, durable]
  → relay (async, retries until subscriber acks)
  → cells/auditcore/slices/auditappendbootstrap.Service.HandleEvent
       → audit.AppendBootstrapAuthFail(ctx, bootstrapStore, clk, reason, clientIp)
            → bootstrap chain ledger.Store.Append  [bootstrap-namespace, independent HMAC]
```

**Preserved isolation guarantees**:

- The bootstrap chain namespace (`"bootstrap"`) remains physically separated from
  the relay chain namespace (`"auditcore"`). D1 is preserved.
- `*audit.BootstrapLedgerStore` sealed handle is still the only type accepted by
  `AppendBootstrapAuthFail`. D2 is preserved and narrowed: the handle is now
  exclusively held inside `cellmodules/auditcore` (via `auditcell.WithBootstrapStore`)
  and is not passed across any module or cell boundary.
- Independent HMAC keys (`GOCELL_AUDITCORE_HMAC_KEY` / `GOCELL_AUDIT_BOOTSTRAP_HMAC_KEY`)
  remain. D3 is preserved.
- `ledger.MultiStore` still implements only `QueryStore`. D4 is preserved.
- `audit_entries.namespace` column partitioning and PG advisory locks. D6 is preserved.

**Archtest changes**:

- `BOOTSTRAP-AUDIT-OBSERVER-FUNNEL-DOWNSTREAM-HARD-01`: **retired**. The observer
  no longer calls `AppendBootstrapAuthFail`. The emit path is covered by
  `EMIT-DECL-COVER-01` (existing archtest) + the `event.auth.bootstrap-failed.v1`
  contract declaration.
- `BOOTSTRAP-AUDIT-OBSERVER-FUNNEL-UPSTREAM-MEDIUM-01`: **retired**. Same reason.
- `MODULE-PROVIDE-NO-VALUE-HANDOFF-01`: **new** (Hard). Reflect-freezes `CellModule.Provide`
  to have exactly 2 inputs and 4 outputs with no cross-module value handoff channel.
  This prevents any future re-introduction of a `BootstrapLedgerStore`-style handle
  passing via the composition API.
- `MODULE-ORDER-AUDITCORE-BEFORE-ACCESSCORE-01`: **retired**. The module ordering
  constraint existed solely to guarantee `BootstrapLedgerStore` was populated before
  accesscore's `Provide` ran. With the handoff deleted, there is no Provide-time
  ordering dependency between auditcore and accesscore.
- `AUDIT-NS-DISJOINT-01`: **preserved** (unchanged). The namespace disjointness scan
  still runs on `cmd/corebundle/**` to backstop any future composition mistakes.
