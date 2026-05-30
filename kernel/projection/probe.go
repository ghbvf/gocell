package projection

import (
	"context"
	"fmt"
	"time"

	"github.com/ghbvf/gocell/kernel/healthz"
	"github.com/ghbvf/gocell/pkg/errcode"
)

// projectionLagThresholdSeconds is the v1 constant lag threshold in seconds.
// When the time since the last applied event's OccurredAt exceeds this value,
// the readiness probe reports unhealthy. Configurable per projection in a
// future release; const for v1 (least moving parts).
const projectionLagThresholdSeconds = 300

// ReadinessProbe returns a [healthz.Probe] for this projection. The probe name
// follows the format "<cellID>_projection_<projectionID>_ready" via the sole
// sanctioned constructor [healthz.ProjectionReadyProbeName].
//
// Check returns nil (healthy) when:
//   - pending events = 0 (head == checkpoint), OR
//   - nothing has been applied yet (last == 0, startup grace period)
//
// Check returns an error (unhealthy) when lag > projectionLagThresholdSeconds.
func (c *Coordinator) ReadinessProbe() (healthz.Probe, error) {
	name, err := healthz.ProjectionReadyProbeName(c.cellID, c.projectionID)
	if err != nil {
		return nil, fmt.Errorf("projection.ReadinessProbe: %w", err)
	}
	return healthz.NewProbe(name, c.checkReady), nil
}

// checkReady implements the probe check function. It computes pending events
// and lag on demand (no background ticker), consistent with the plan's
// "metrics computed on probe/snapshot reads" decision.
func (c *Coordinator) checkReady(ctx context.Context) error {
	head, err := c.replay.Head(ctx)
	if err != nil {
		return fmt.Errorf("projection.probe: Head: %w", err)
	}
	cp, err := c.store.LoadOffset(ctx, c.cellID, c.projectionID)
	if err != nil {
		return fmt.Errorf("projection.probe: LoadOffset: %w", err)
	}
	pending := head - cp

	// Update pending_events gauge on demand.
	c.metrics.setPendingEvents(ctx, c.cellID, c.projectionID, float64(pending))

	// No pending events → always healthy.
	if pending <= 0 {
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
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"projection.probe: replay lag exceeds threshold",
			errcode.WithDetails(
				errcode.PublicString("cell", c.cellID),
				errcode.PublicString("projection", c.projectionID),
				errcode.PublicInt("thresholdSeconds", projectionLagThresholdSeconds),
			),
		)
	}
	return nil
}
