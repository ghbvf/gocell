package projection

import "context"

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
//
// ReplayLagSeconds == 0 is ambiguous by design and must be read together with
// PendingEvents: it means either "caught up" (PendingEvents == 0) OR "lag
// unknown" (PendingEvents > 0 but nothing applied yet — cold start / fresh
// rebuild, so there is no last-applied time to measure from). The lag readyz
// probe distinguishes these (it leaves the lag gauge unwritten during the
// startup-grace case rather than reporting a false-healthy zero).
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
