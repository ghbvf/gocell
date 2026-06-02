package executor

import (
	"context"
	"log/slog"
	"time"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/pkg/idutil"
	"github.com/ghbvf/gocell/runtime/saga/internal/sagalog"
)

// Heartbeater extends a saga instance's lease by calling Heartbeat periodically.
// The interface matches kernel/saga/journal.Heartbeater (and the Heartbeat
// method on the full journal.Journal) exactly, so any journal implementation
// satisfies it structurally without this package importing journal.
//
// This is deliberately a SEPARATE declaration from journal.Heartbeater, not an
// alias or re-use: kernel/ cannot depend on runtime/, and this package must
// never import kernel/saga/journal (below), so the two structurally-identical
// interfaces are an intentional layering artifact. journal.Heartbeater exists
// only to compose the full journal.Journal; this one is the executor's own
// dependency surface.
//
// INVARIANT: executor never imports kernel/saga/journal — it uses this narrow
// interface instead. Enforced by the saga-executor-no-journal-import depguard
// rule in .golangci.yml (path-level import ban) and complemented by
// SAGA-JOURNAL-HOLDER-SEAL-01 (no journal.* field may be persisted here either).
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

// heartbeatConfig holds the identity and timing parameters for runHeartbeat.
// Grouping them into a struct keeps runHeartbeat's parameter count within the
// go:S107 limit (≤7 parameters). All fields are required; Interval and
// LeaseDuration must be > 0.
type heartbeatConfig struct {
	InstanceID    idutil.SafeID
	LeaseID       idutil.SafeID
	DefinitionID  idutil.SafeID
	Interval      time.Duration
	LeaseDuration time.Duration
}

// runHeartbeat runs in its own goroutine and beats the lease at each ticker
// interval until:
//   - ctx is canceled (clean shutdown — caller called stopHB), or
//   - Heartbeat returns ok=false (stale lease — another coordinator took over).
//
// On a stale lease it invokes onStale (which the caller wires to cancel the
// running step's / fn's context with errLeaseLost) before returning, so the
// orphaned work stops executing rather than racing to commit under a lost
// lease. The lease-lost log is Info (expected handoff in multi-coordinator
// deployments, per issue #1181 §6), not Warn. Infrastructure errors remain
// Warn (degraded operation per observability.md).
//
// Infrastructure errors from Heartbeat are logged as Warn and the goroutine
// continues to the next tick (fail-open for transient infra issues, matching
// Temporal's heartbeat semantics — see ref note).
//
// onHBFailure is invoked synchronously on every tick that fails or observes a
// stale lease, with the reason classified. It is best-effort: it MUST NOT
// block (observers must be non-blocking — see Observer godoc); a slow observer
// would delay the next heartbeat tick and risk dropping the lease.
//
// The ticker is stopped before the function returns; callers using
// clockmock.FakeClock can assert PendingTickers()==0 after joining this
// goroutine.
//
// ref: temporalio/sdk-go internal_task_handlers.go — transient heartbeat
// failures are logged and retried on the next interval; only server-side
// CancelRequested (≈ ok=false) cancels the activity ctx.
func runHeartbeat(
	ctx context.Context,
	clk clock.Clock,
	hb Heartbeater,
	cfg heartbeatConfig,
	logger *slog.Logger,
	onStale func(),
	onHBFailure func(reason HeartbeatFailureReason),
) {
	instanceID := cfg.InstanceID
	leaseID := cfg.LeaseID
	definitionID := cfg.DefinitionID
	interval := cfg.Interval
	leaseDuration := cfg.LeaseDuration
	ticker := clk.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C():
			ok, err := hb.Heartbeat(ctx, instanceID, leaseID, leaseDuration)
			if err != nil {
				// #1181 F12: include reason + definition_id so per-definition
				// dashboards can group heartbeat failures without parsing the
				// observer counter labels separately.
				logger.LogAttrs(ctx, slog.LevelWarn, "saga executor: heartbeat failed",
					sagalog.InstanceFields(instanceID, leaseID,
						slog.String("definition_id", string(definitionID)),
						slog.String("reason", string(HeartbeatFailureInfraError)),
						slog.Any("error", err))...)
				onHBFailure(HeartbeatFailureInfraError)
				continue
			}
			if !ok {
				logger.LogAttrs(ctx, slog.LevelInfo, "saga executor: lease lost (stale); canceling step",
					sagalog.InstanceFields(instanceID, leaseID,
						slog.String("definition_id", string(definitionID)),
						slog.String("reason", string(HeartbeatFailureStaleLease)))...)
				// #1210 round-N F2: onStale FIRST — cancel the worker ctx so
				// the in-flight step can bail immediately. onHBFailure is
				// synchronous and potentially slow (observer implementations
				// must be non-blocking per Observer godoc, but recover() guards
				// the Executor goroutine only, not runHeartbeat's caller stack).
				// Canceling the worker first ensures step authors selecting on
				// ctx.Done() observe the cancellation without waiting for the
				// observer call to complete.
				onStale()
				onHBFailure(HeartbeatFailureStaleLease)
				return
			}
		}
	}
}
