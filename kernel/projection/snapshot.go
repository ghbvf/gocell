package projection

import "context"

// RebuildController is the narrow control-plane surface the rebuild HTTP
// endpoint consumes from a projection Coordinator: trigger a background rebuild
// and read a status snapshot. *Coordinator satisfies it.
//
// The endpoint (runtime/bootstrap) holds this interface rather than the concrete
// *Coordinator so the handler's dependency is honest (it only triggers + reads,
// never touches the raw checkpoint store / tx runner) and is unit-testable with
// a fake. This is the standard accept-interfaces-at-the-consumer idiom, not a
// speculative abstraction — there is exactly one production implementer.
type RebuildController interface {
	// Rebuild triggers a background full rebuild; nil = admitted (caller → 202),
	// ErrRebuildInProgress = already running (caller → 409).
	Rebuild(ctx context.Context) error
	// Snapshot returns the current phase plus best-effort pending/lag.
	Snapshot(ctx context.Context) (Snapshot, error)
}

// Compile-time assertion that *Coordinator satisfies RebuildController.
var _ RebuildController = (*Coordinator)(nil)

// Snapshot is a point-in-time view of a projection's lifecycle and replay lag,
// returned by the rebuild control-plane endpoint as the 202 response body
// ({phase, pendingEvents, replayLagSeconds}).
//
// Phase is always populated (read from the in-memory atomic — no I/O, never
// fails). PendingEvents and ReplayLagSeconds are best-effort: on a
// checkpoint-store / replay read error they are zero and Snapshot returns a
// non-nil error. Callers (the rebuild handler) log the error but MUST NOT fail
// an already-admitted rebuild over a degraded snapshot read — the rebuild has
// been accepted regardless of whether its current lag could be read.
type Snapshot struct {
	Phase            Phase
	PendingEvents    int64
	ReplayLagSeconds float64
}

// Snapshot returns the Coordinator's current lifecycle phase plus a best-effort
// pending-events / replay-lag reading. Phase is read from the live atomic
// (Coordinator.Phase, never errors). PendingEvents and ReplayLagSeconds are
// derived from the replay head, stored checkpoint, and last-applied domain time
// via computeLagPending — the same single-source computation the lag readyz
// probe uses (so the wire snapshot can never diverge from the probe). On a
// store/replay read error the Snapshot carries the valid Phase with zeroed
// pending/lag and the error is returned for the caller to log.
func (c *Coordinator) Snapshot(ctx context.Context) (Snapshot, error) {
	pending, lagSecs, _, err := c.computeLagPending(ctx)
	return Snapshot{
		Phase:            c.Phase(),
		PendingEvents:    pending,
		ReplayLagSeconds: lagSecs,
	}, err
}
