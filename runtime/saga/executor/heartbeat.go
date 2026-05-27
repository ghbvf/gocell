package executor

import (
	"context"
	"log/slog"
	"time"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/pkg/idutil"
)

// Heartbeater extends a saga instance's lease by calling Heartbeat periodically.
// The interface matches the journal.Journal.Heartbeat signature exactly so that
// journal implementations satisfy it without importing this package.
//
// INVARIANT: executor never imports kernel/saga/journal — it uses this narrow
// interface instead (SAGA-JOURNAL-HOLDER-SEAL-01).
//
// Caller contract: the Executor stops the heartbeat loop when ok=false is
// returned; implementations should guarantee that ok=false is idempotent
// (i.e., repeated observation after the lease is lost is safe).
type Heartbeater interface {
	// Heartbeat attempts to renew the lease for the given instance.
	// Returns ok=true when the lease was successfully renewed; ok=false when
	// the lease is stale (another coordinator took over). err indicates an
	// infrastructure failure (retry is appropriate).
	Heartbeat(ctx context.Context, instanceID, leaseID idutil.SafeID, leaseDuration time.Duration) (ok bool, err error)
}

// runHeartbeat runs in its own goroutine and beats the lease at each ticker
// interval until:
//   - ctx is canceled (clean shutdown — Execute called stopHB), or
//   - Heartbeat returns ok=false (stale lease — another coordinator took over).
//
// On a stale lease it invokes onStale (which the executor wires to cancel the
// running step's context with errLeaseLost) before returning, so the orphaned
// step stops executing rather than racing to commit under a lost lease. The
// lease-lost log is Warn (degraded operation per observability.md), not Info.
//
// Infrastructure errors from Heartbeat are logged as Warn and the goroutine
// continues to the next tick (fail-open for transient infra issues).
//
// The ticker is stopped before the function returns; callers using
// clockmock.FakeClock can assert PendingTickers()==0 after joining this goroutine.
func runHeartbeat(
	ctx context.Context,
	clk clock.Clock,
	hb Heartbeater,
	instanceID, leaseID idutil.SafeID,
	interval, leaseDuration time.Duration,
	logger *slog.Logger,
	onStale func(),
) {
	ticker := clk.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C():
			ok, err := hb.Heartbeat(ctx, instanceID, leaseID, leaseDuration)
			if err != nil {
				logger.WarnContext(ctx, "saga executor: heartbeat failed",
					slog.String("instance_id", string(instanceID)),
					slog.String("lease_id", string(leaseID)),
					slog.Any("error", err),
				)
				continue
			}
			if !ok {
				logger.WarnContext(ctx, "saga executor: lease lost (stale); canceling step",
					slog.String("instance_id", string(instanceID)),
					slog.String("lease_id", string(leaseID)),
				)
				onStale()
				return
			}
		}
	}
}
