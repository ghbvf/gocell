// Package auditcoretest provides testutil helpers for the auditcore Cell.
//
// # Purpose
//
// Journey and integration tests need to drive the auditcore subscription seam
// (the auditappendsession EntryHandler registered during cell.Init) without
// going through broker delivery and without importing internal packages from
// cells/auditcore/internal/. This package exports the wiring that the
// integration tests previously duplicated inline, making it reusable across
// criterion test files.
//
// # Import scope
//
// auditcoretest is a sibling package inside the cells/auditcore subtree.
// It may import cells/auditcore/* (including internal sub-packages) because
// Go's internal-package rule only restricts packages *outside* the parent
// tree. Production code must never import auditcoretest; the
// CELLTEST-IMPORT-SCOPE-01 archtest auto-discovers this package form and
// enforces that constraint.
//
// # Relationship to cells/auditcore/cell.go
//
// BuildAuditcoreChain wires the same options as the production composition
// root (WithLedgerProtocol, WithLedgerStore, WithEmitter, WithTxManager,
// WithMetricsProvider, WithLogger, WithClock) and then calls c.Init to
// trigger subscription registration — mirroring what bootstrap phase3
// does in production. The resulting handler is the exact same
// outbox.EntryHandler that a broker delivery loop would invoke.
//
// # Usage
//
//	handler, store, ctx := auditcoretest.BuildAuditcoreChain(t)
//	entry := auditcoretest.CanonicalSessionCreatedEntry("sess-1", "usr-1")
//	result := handler(ctx, entry)
//	// result.Disposition == outbox.DispositionAck
//	// store.Tail(ctx) has one entry
package auditcoretest
