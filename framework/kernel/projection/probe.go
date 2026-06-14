package projection

import (
	"context"
	"fmt"
	"time"

	"github.com/ghbvf/gocell/framework/kernel/healthz"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
)

// projectionLagThresholdSeconds is the fixed v1 lag threshold in seconds.
// When the time since the last applied event's OccurredAt exceeds this value,
// the lag probe reports unhealthy. This is a fixed const for v1 (least
// moving parts). Per-projection tuning will be revisited only if a specific
// projection demonstrates a concrete need — no timeline is promised.
//
// Cross-reference: docs/ops/alerting-rules.md GoCellProjectionReplayLagHigh
// uses `> 300` to match this threshold; if this const changes that alert rule
// must be updated to match. The probe name is "<cell>_projection_<name>_lag"
// (no _ready suffix — operational health, not dependency availability).
const projectionLagThresholdSeconds = 300

// Probes returns the two healthz.Probe values for this projection, mirroring
// the ProbeSet pattern used by adapters/postgres.Pool.Probes():
//
//   - "<cell>_projection_<proj>_store_ready": dependency-availability probe.
//     Healthy when both replay.Head and store.LoadOffset succeed. Unhealthy
//     when either errors (storage unreachable).
//
//   - "<cell>_projection_<proj>_lag": operational-health probe (no _ready
//     suffix — same convention as outbox_relay_*). Healthy when pending events
//     = 0 (idle), nothing applied yet (startup grace), or lag ≤ threshold.
//     Unhealthy when pending > 0 AND lag > projectionLagThresholdSeconds.
//
// Both probes compute their state on demand (no background ticker), consistent
// with the plan's "metrics computed on probe/snapshot reads" decision.
//
// The split follows observability.md: storage reachability (dependency
// availability) and read-model staleness (operational health) are distinct
// failure domains with different on-call responses.
func (c *Coordinator) Probes() ([]healthz.Probe, error) {
	storeReadyName, err := healthz.ProjectionStoreReadyProbeName(c.cellID, c.projectionID)
	if err != nil {
		return nil, fmt.Errorf("projection.Probes: store-ready name: %w", err)
	}
	lagName, err := healthz.ProjectionLagProbeName(c.cellID, c.projectionID)
	if err != nil {
		return nil, fmt.Errorf("projection.Probes: lag name: %w", err)
	}
	return []healthz.Probe{
		healthz.NewProbe(storeReadyName, c.checkStoreReady),
		healthz.NewProbe(lagName, c.checkLag),
	}, nil
}

// checkStoreReady is the store-availability probe check. It verifies both
// replay.Head and store.LoadOffset succeed. If either errors, storage is
// considered unreachable and the probe returns unhealthy (KindUnavailable).
func (c *Coordinator) checkStoreReady(ctx context.Context) error {
	if _, err := c.replay.Head(ctx); err != nil {
		return errcode.New(errcode.KindUnavailable, errcode.ErrServiceUnavailable,
			"projection.probe: replay Head unreachable",
			errcode.WithInternal(errcode.InternalAttr("error", err.Error())))
	}
	if _, err := c.store.LoadOffset(ctx, c.cellID, c.projectionID); err != nil {
		return errcode.New(errcode.KindUnavailable, errcode.ErrServiceUnavailable,
			"projection.probe: checkpoint store unreachable",
			errcode.WithInternal(errcode.InternalAttr("error", err.Error())))
	}
	return nil
}

// computeLagPending is the single-source pending-events / replay-lag computation
// shared by the lag readyz probe (checkLag) and the rebuild control-plane
// snapshot (Coordinator.Snapshot). It is PURE — no metric side-effects — so the
// two consumers cannot diverge: checkLag adds the gauge writes + threshold
// verdict, Snapshot reads the values for the wire response.
//
// It reads the replay head and stored checkpoint and derives:
//   - pending: head − checkpoint, clamped to ≥ 0. A checkpoint > head anomaly
//     (negative raw difference, F13) yields pending=0 (treated as caught up),
//     mirroring the lag probe's "no negative gauge" rule.
//   - lagSecs: 0 when there are no pending events, or when nothing has been
//     applied yet (lastApplied == 0 — cold start / fresh rebuild). Otherwise it
//     is the wall-clock time since the last applied event's domain OccurredAt.
//   - lagApplicable: false when pending == 0 OR lastApplied == 0. checkLag uses
//     it to decide whether to WRITE the lag gauge — under startup grace
//     (pending > 0 but nothing applied) the gauge is deliberately left untouched
//     so an idle cold start does not report a false-healthy zero lag.
func (c *Coordinator) computeLagPending(ctx context.Context) (pending int64, lagSecs float64, lagApplicable bool, err error) {
	head, err := c.replay.Head(ctx)
	if err != nil {
		return 0, 0, false, fmt.Errorf("projection.probe: Head: %w", err)
	}
	cp, err := c.store.LoadOffset(ctx, c.cellID, c.projectionID)
	if err != nil {
		return 0, 0, false, fmt.Errorf("projection.probe: LoadOffset: %w", err)
	}
	pending = head - cp
	// F13: checkpoint > head anomaly (pending < 0) → clamp to 0 (caught up).
	// pending == 0 → caught up. Either way lag is not applicable.
	if pending <= 0 {
		return 0, 0, false, nil
	}
	// Startup grace: nothing has been applied yet (cold start or fresh rebuild) →
	// lag is unknown, not zero. lagApplicable=false so checkLag skips the gauge.
	last := c.lastAppliedUnixNano.Load()
	if last == 0 {
		return pending, 0, false, nil
	}
	// Lag = time since the last applied event's domain time (OccurredAt).
	return pending, c.clk.Since(time.Unix(0, last)).Seconds(), true, nil
}

// checkLag is the operational-health probe check. It computes pending events and
// lag on demand (no background ticker) via computeLagPending, writes the gauges,
// and returns unhealthy when the lag exceeds the threshold.
//
// F13: a checkpoint > head anomaly is collapsed into the pending==0 branch by
// computeLagPending (clamped to 0), so both write pending=0/lag=0 and report
// healthy. Startup grace (pending > 0, nothing applied) leaves the lag gauge
// untouched (lagApplicable == false) so a stuck cold start is not masked healthy.
func (c *Coordinator) checkLag(ctx context.Context) error {
	pending, lagSecs, lagApplicable, err := c.computeLagPending(ctx)
	if err != nil {
		return err
	}

	// Update pending_events gauge on demand.
	c.metrics.setPendingEvents(ctx, c.cellID, c.projectionID, float64(pending))

	if pending == 0 {
		// No pending events → always healthy; zero the lag gauge so it does not
		// produce false-positive GoCellProjectionReplayLagHigh alerts on idle streams.
		c.metrics.setReplayLag(ctx, c.cellID, c.projectionID, 0)
		return nil
	}

	if !lagApplicable {
		// Startup grace: nothing applied yet. Leave the lag gauge untouched.
		return nil
	}

	// Update replay lag gauge on demand.
	c.metrics.setReplayLag(ctx, c.cellID, c.projectionID, lagSecs)

	if lagSecs > projectionLagThresholdSeconds {
		return errcode.New(errcode.KindUnavailable, errcode.ErrServiceUnavailable,
			"projection.probe: replay lag exceeds threshold",
			errcode.WithDetails(
				errcode.PublicInt("thresholdSeconds", projectionLagThresholdSeconds),
				errcode.PublicInt("lagSeconds", int64(lagSecs)),
			),
			errcode.WithInternal(
				errcode.InternalAttr("cell", c.cellID),
				errcode.InternalAttr("projection", c.projectionID),
				errcode.InternalAttr("lagSeconds", lagSecs),
			),
		)
	}
	return nil
}
