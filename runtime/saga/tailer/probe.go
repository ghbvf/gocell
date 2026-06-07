package tailer

import (
	"context"
	"fmt"

	"github.com/ghbvf/gocell/kernel/healthz"
	"github.com/ghbvf/gocell/pkg/errcode"
)

// Probes implements lifecycle.ManagedResource. There is a SINGLE readiness probe
// "<cell>_saga_tailer_<proj>_ready" (dependency availability: the Tailer is
// running AND journal/checkpoint storage is reachable). Replay lag is exposed as
// a metric gauge (Observer.ObserveLag), not a second probe — mirroring the
// metric/probe split in ADR 202606051200-1609 §4.3. The probe name is validated
// and cached at construction (NewTailer), so this method cannot fail.
func (t *Tailer) Probes() []healthz.Probe {
	return []healthz.Probe{healthz.NewProbe(t.readyProbeName, t.checkReady)}
}

// checkReady is the readiness probe check: not-ready while stopped/starting/
// stopping; when running, unhealthy if journal/checkpoint storage is unreachable
// (computePending exercises both replay.Head and store.LoadOffset).
func (t *Tailer) checkReady(ctx context.Context) error {
	switch tailerState(t.state.Load()) {
	case tailerStopped:
		return notReady("not running")
	case tailerStarting:
		return notReady("starting")
	case tailerStopping:
		return notReady("stopping")
	}
	if _, err := t.computePending(ctx); err != nil {
		return errcode.New(errcode.KindUnavailable, errcode.ErrServiceUnavailable,
			"tailer.probe: journal/checkpoint storage unreachable",
			errcode.WithInternal(errcode.InternalAttr("error", err.Error())))
	}
	return nil
}

func notReady(reason string) error {
	return errcode.New(errcode.KindUnavailable, errcode.ErrServiceUnavailable,
		"tailer.probe: not ready",
		errcode.WithInternal(errcode.InternalAttr("reason", reason)))
}

// computePending returns the pending backlog (Head − checkpoint), clamped to
// ≥ 0 (a checkpoint > head anomaly yields 0, treated as caught up — mirrors
// kernel/projection computeLagPending). It is the shared read used by both the
// readiness probe (storage reachability) and tick-success lag reporting.
func (t *Tailer) computePending(ctx context.Context) (int64, error) {
	head, err := t.replay.Head(ctx)
	if err != nil {
		return 0, fmt.Errorf("tailer: Head: %w", err)
	}
	cp, err := t.store.LoadOffset(ctx, t.cellID, t.projectionID)
	if err != nil {
		return 0, fmt.Errorf("tailer: LoadOffset: %w", err)
	}
	if pending := head - cp; pending > 0 {
		return pending, nil
	}
	return 0, nil
}
