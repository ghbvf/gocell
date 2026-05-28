package projection

import (
	"context"

	"github.com/ghbvf/gocell/kernel/outbox"
)

// Apply is the business event→state projection hook: given a consumed event,
// mutate the read-model. The transaction is ambient — Apply obtains it via
// persistence.TxFromContext(ctx) exactly like outbox.Writer.Write, because the
// Coordinator (PR-01) invokes Apply inside persistence.TxRunner.RunInTx so the
// read-model mutation and the checkpoint advance commit atomically (exactly-once
// delivery; the harness never calls Apply twice for the same offset).
//
// Apply MUST NOT open its own transaction or connection. A transient failure
// returns a plain error (the Coordinator requeues); a permanent failure returns
// an errcode permanent error (the Coordinator rejects to the DLX). Decided in
// ADR §3 Q2 against the eventhorizon read-modify-write entity shape and the
// explicit tx-handle parameter.
//
// ref: JasperFx/marten async-daemon IDocumentOperations apply shape.
type Apply func(ctx context.Context, event outbox.Entry) error

// CheckpointStore persists a projection's consumed offset. It is the framework's
// own offset table — it does NOT touch any business read-model schema (the
// CellTx-offset design keeps the harness clear of the GAP-8 seal; ADR §4).
//
// Both methods are ambient-tx: SaveOffset participates in the caller's
// transaction via persistence.TxFromContext(ctx) (no raw db handle — enforced by
// PROJECTION-CHECKPOINT-TX-BOUND-01), so the offset advance commits together
// with the Apply mutation. LoadOffset returns 0 for an unknown
// (cellID, projectionID) pair (cold start = offset 0).
//
// Implementations: mem (PR-01) + postgres (PR-02); both verified by the shared
// projectiontest.RunCheckpointConformance template (PR-01). Decided in ADR §3
// Q1 (caller-provided tx + harness-internal SaveOffset, mirroring outbox.Writer
// and Axon's JdbcTokenStore same-tx commit).
type CheckpointStore interface {
	// LoadOffset returns the last committed offset for the projection, or 0 if
	// none has been recorded yet (cold start).
	LoadOffset(ctx context.Context, cellID, projectionID string) (int64, error)
	// SaveOffset advances the projection's committed offset within the ambient
	// transaction carried by ctx.
	SaveOffset(ctx context.Context, cellID, projectionID string, offset int64) error
}

// Option configures a projection subscription. It is the frozen functional-option
// seam consumed by Coordinator.Subscribe (PR-01); concrete option constructors
// (e.g. starting offset, fail-open policy) are added alongside the Coordinator.
// Declared here so the Subscribe API surface is fixed by the ADR rather than
// drifting when the implementation lands.
type Option func(*subscribeOptions)

// subscribeOptions holds the resolved Subscribe configuration. It is the target
// of Option closures; fields are introduced with the Coordinator in PR-01.
type subscribeOptions struct{}
