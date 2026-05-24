// Package saga is the GoCell runtime engine for L3 WorkflowEventual sagas
// (capability cap-15). It drives saga instances forward by reading the
// kernel/saga/journal.Journal, executing user-supplied Step bodies, then
// writing the resulting journal event(s) and outbox commands inside one
// short database transaction.
//
// # Single-process unsafe mode
//
// PR-03 ships a single-process Coordinator with NO leader-elect; it is NOT
// suitable for production. PR-05 will add distlock-based leader-elect.
// The Coordinator emits slog.Warn(mode="unsafe_no_leader") at Start.
//
// # Layering
//
// The Definition / Step / Registry data primitives live in kernel/saga (a
// pure data package). The Coordinator + Dispatcher engine lives here. No
// cell wraps runtime/saga in PR-03 — the cell-side wiring (including
// RegisterRepoReady) lands in PR-09.
//
// # Carry-over from PR-02 (#952)
//
//   - RepoReady probe registration: Coordinator satisfies healthz.RepoProber;
//     cell-side registration deferred to PR-09 (tracked in follow-up issue).
//   - Enqueuer interface split: deferred until a real producer ships.
package saga
