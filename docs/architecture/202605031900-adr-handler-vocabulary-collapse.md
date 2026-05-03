# ADR: handler vocabulary collapse — explicit Disposition, no legacy fallback (K #03)

> Status: Accepted
> Date: 2026-05-03
> ref: `docs/plans/202605011500-029-master-roadmap.md` Track K #03 PR-V1-API-VOCABULARY-COLLAPSE
> ref: `docs/plans/archive/202604301129-027-release-v1.0-readiness.md` §32 (E1 leverage 13pt)

## Context

After K #02 PR-V1-API-CONTRIB-COLLAPSE (PR #356) collapsed five Cell
contributor interfaces into a single `cell.Registry`, the `kernel/outbox`
public API still carried a parallel set of legacy concepts: `LegacyHandler` /
`WrapLegacyHandler` (a `func(ctx, entry) error` adapter), `Receipt` (a type
alias re-exporting `idempotency.Receipt` to handler authors), and a hidden
behavioural rule in `ConsumerBase.isPermanentRejection` that **implicitly
upgraded** `DispositionRequeue + PermanentError-wrapped Err` to
`DispositionReject` so legacy callers could route to DLX without explicit
opt-in.

Side by side, `kernel/persistence` exported `NoopTxRunner` + `RunnerOrNoop`
which let cells silently degrade to a no-transaction fallback when the
caller forgot to inject a real `TxRunner`. Eleven service constructors
across `cells/{accesscore,auditcore,configcore}` and `examples/todoorder`
relied on this fallback.

The combined effect: handler authors had five overlapping ways to express
the same idempotency intent (`Claimer` / `ClaimState` / `Receipt` /
`Stopper` / `WrapLegacyHandler`), inconsistent handler signatures across
six L2 consumers (some directly returned `HandleResult`, others returned
`error` and went through `WrapLegacyHandler`), and an implicit "fail-safe"
upgrade path that masked authoring mistakes — any handler that accidentally
returned `DispositionRequeue` with a wrapped `PermanentError` would silently
route to DLX, bypassing retry budget. Public API surface area was 30 %
larger than the underlying domain warranted, and the implicit upgrade
contradicted the stated goal of an "explicit three-state vocabulary".

## Decision

Collapse the handler vocabulary to a single explicit shape:

### Decision 1 — Delete `WrapLegacyHandler` and `LegacyHandler`

Remove both the legacy `func(ctx, Entry) error` type alias and its adapter.
All handlers must implement `EntryHandler = func(context.Context, Entry)
HandleResult` directly. There is no compatibility shim or deprecation
period — at the time of this ADR there are no external GoCell consumers, so
the project standard is to break and rewrite, not deprecate.

### Decision 2 — Delete `persistence.NoopTxRunner` / `RunnerOrNoop`

Service constructors that accept a `TxRunner` MUST fail-fast with
`errcode.ErrValidationFailed` when the runner is nil. Tests inject either a
real `TxRunner` (via testcontainers) or a locally-defined no-op stub
implementing `kernel/cell.Nooper`. Production callers can no longer silently
run "as if there were no transaction".

### Decision 3 — Delete `outbox.Receipt` type alias

`HandleResult.Receipt` keeps its position as the Subscriber-implementer
delivery-loop hand-off, but its type is now `idempotency.Receipt` directly.
The alias was the last `kernel/idempotency` symbol leaking into the handler
vocabulary; deleting it makes the layering explicit and steers handler
authors away from touching the field at all (see Trade-off Q1 below).

### Decision 4 — Delete the implicit `PermanentError → Reject` upgrade

`isPermanentRejection` simplifies to a single condition:

```go
func isPermanentRejection(result HandleResult) bool {
    return result.Disposition == DispositionReject
}
```

`PermanentError` itself is preserved as a classification tag for
logging/metrics, but it has **no** behavioural effect on `Disposition`. A
handler that wraps a permanent error in `Requeue` will exhaust its retry
budget and only then route to DLX (the budget-exhaust path), exactly as it
would for any other transient failure. Handlers that need fast DLX routing
must return `DispositionReject` explicitly. This restores the "explicit
three-state vocabulary" the public API was supposed to provide and is
locked by `TestConsumerBase_Wrap_WrappedPermanentErrorInRequeue_NotEscalated`.

## Trade-off Q1 — `HandleResult.Receipt` field retained (not deleted)

The `Receipt` field is *not* part of the handler-author vocabulary — it is
a `Subscriber → ConsumerBase → Subscriber-delivery-loop` internal hand-off
threaded through the same struct. Eliminating it requires a structural
redesign of the Subscriber-Handler boundary (e.g., a side-channel
`ReleaseAfterSettlement(entryID, disposition)` API on `ConsumerBase`, or a
`sync.Map` keyed by entry ID maintained inside ConsumerBase). That work is
~12h dev + 6h review, which exceeds this PR's scope.

For now the field stays, with godoc explicitly marking it as a
Subscriber-implementer field that business handlers MUST NOT write. The
follow-up is tracked as **029 #12 PR-V1-OUTBOX-RECEIPT-EXTRACT** in the
master roadmap (single source of truth — `docs/backlog.md` does not get a
duplicate entry).

## Consequences

- Public API surface area in `kernel/outbox` + `kernel/persistence`
  shrinks by ~100 lines of exported declarations.
- Six L2 consumer handlers (`cells/accesscore/{configreceive,
  sessionlogout}`, `cells/configcore/configsubscribe`,
  `cells/auditcore/auditappend`) plus their cell_init wiring now use the
  single `EntryHandler` signature directly; tests assert
  `result.Disposition` rather than wrapping behaviour.
- Eleven service constructors gain a nil-check on `TxRunner` and return
  `(*Service, error)` (some signatures change as a result; all callers in
  `cells/`, `examples/` updated in this PR).
- A handler that wraps a `PermanentError` in `Requeue` no longer bypasses
  retry budget — surfaced in observability (retry-exhausted log) and in
  the lock test. This is a deliberate trade of "implicit safety" for
  "explicit vocabulary"; the prior fallback masked authoring mistakes.
- `SubscriberIntakeStopper` is retained on the public boundary because
  `runtime/eventbus`, `runtime/eventrouter`, and `adapters/rabbitmq` all
  depend on it as a Subscriber-implementer extension contract. Godoc now
  marks it as off-limits to handler authors.
- `kernel/cell.Nooper` marker interface is preserved; its noop tx
  implementation is now per-cell test-local rather than a shared kernel
  export.

## Future Work

- **029 #12 PR-V1-OUTBOX-RECEIPT-EXTRACT** — restructure
  Subscriber-Handler boundary so `HandleResult.Receipt` can be deleted
  outright, completing the "handler-author cannot touch idempotency types"
  goal. Dependency: this ADR (#03). Estimate: 12h dev + 6h review.

## References

- `ref: ThreeDotsLabs/watermill message/router.go` — handler returns
  `([]*Message, error)`; ack/nack are framework-internal, no compatibility
  wrapper exposed
- `ref: kubernetes-sigs/controller-runtime pkg/reconcile/reconcile.go` —
  three-state semantics expressed via `(Result, error)`; `TerminalError`
  is an explicit wrap, never an implicit upgrade
- `ref: nats-io/nats.go jetstream/msg.go` — `Term`/`Nak`/`InProgress` are
  explicit msg-method calls, not return-value implicit upgrades
- `ref: go-kratos/kratos middleware/middleware.go` — handler signature
  `(ctx, req) → (resp, error)` is the Go-idiomatic minimum surface area
