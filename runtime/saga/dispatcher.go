// Package saga (see doc.go for the package narrative) is the runtime engine that
// drives saga instances forward. This file declares the post-commit signal sink
// the Coordinator calls from an AfterCommit hook.
package saga

import "context"

// Dispatcher is the post-commit signal sink. After a successful saga step
// commit, the Coordinator calls Kick once from a kernel/persistence.RegisterAfterCommit
// hook so a downstream component (typically the outbox Relay) can immediately
// poll instead of waiting for its next periodic tick.
//
// PR-03 ships only NoopDispatcher. A real wiring (e.g., poke outbox.Relay) will
// land in a later PR when Relay grows a Notify API.
//
// Lifecycle: Kick is called from a hookCtx that has the database transaction
// stripped (see kernel/persistence.RunAfterCommitHooks). Implementations MUST
// NOT call back into the database — Kick is signal-only.
type Dispatcher interface {
	Kick(ctx context.Context)
}

// NoopDispatcher is a default Dispatcher that does nothing. It is safe to use
// in tests and as the default Coordinator dispatcher when no downstream is wired.
type NoopDispatcher struct{}

// Kick implements Dispatcher with intentional no-op behavior — NoopDispatcher
// is the default sink when no downstream component is wired (PR-03 ships this
// only; outbox Relay integration lands in a later PR). The Coordinator still
// calls Kick once per successful commit; dropping it costs nothing because the
// downstream poller will catch up via its own periodic tick.
func (NoopDispatcher) Kick(context.Context) {
	// Intentionally empty — see godoc above.
}
