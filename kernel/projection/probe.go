package projection

import (
	"context"
	"fmt"
	"time"

	"github.com/ghbvf/gocell/kernel/healthz"
	"github.com/ghbvf/gocell/pkg/errcode"
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

// checkLag is the operational-health probe check. It computes pending events
// and lag on demand (no background ticker).
//
// F13: if pending < 0 (checkpoint > head anomaly), log a warning and treat as
// healthy (set lag gauge to 0 — do NOT write a negative gauge).
func (c *Coordinator) checkLag(ctx context.Context) error {
	head, err := c.replay.Head(ctx)
	if err != nil {
		return fmt.Errorf("projection.probe: Head: %w", err)
	}
	cp, err := c.store.LoadOffset(ctx, c.cellID, c.projectionID)
	if err != nil {
		return fmt.Errorf("projection.probe: LoadOffset: %w", err)
	}
	pending := head - cp

	// F13: checkpoint > head anomaly — log warn and treat as healthy (0 gauge).
	if pending < 0 {
		c.metrics.setPendingEvents(ctx, c.cellID, c.projectionID, 0)
		c.metrics.setReplayLag(ctx, c.cellID, c.projectionID, 0)
		return nil
	}

	// Update pending_events gauge on demand.
	c.metrics.setPendingEvents(ctx, c.cellID, c.projectionID, float64(pending))

	// No pending events → always healthy; zero the lag gauge so it does not
	// produce false-positive GoCellProjectionReplayLagHigh alerts on idle streams.
	if pending == 0 {
		c.metrics.setReplayLag(ctx, c.cellID, c.projectionID, 0)
		return nil
	}

	// Startup grace: nothing has been applied yet (cold start or fresh rebuild).
	last := c.lastAppliedUnixNano.Load()
	if last == 0 {
		return nil
	}

	// Lag = time since the last applied event's domain time (OccurredAt).
	lagSecs := c.clk.Since(time.Unix(0, last)).Seconds()
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
