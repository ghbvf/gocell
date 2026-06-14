package sagaprojection_test

import (
	"context"
	"sync"
	"testing"

	"github.com/ghbvf/gocell/framework/kernel/cell"
	"github.com/ghbvf/gocell/framework/kernel/cellvocab"
	"github.com/ghbvf/gocell/framework/kernel/contractspec"
	"github.com/ghbvf/gocell/framework/kernel/outbox"
	"github.com/ghbvf/gocell/framework/kernel/persistence"
	"github.com/ghbvf/gocell/framework/kernel/projection"
	"github.com/ghbvf/gocell/framework/kernel/saga/journal"
	"github.com/ghbvf/gocell/framework/kernel/saga/sagaprojection"
	"github.com/ghbvf/gocell/framework/kernel/wrapper"
	"github.com/ghbvf/gocell/framework/pkg/ctxkeys"
	"github.com/ghbvf/gocell/framework/pkg/testutil/testtime"
	"github.com/ghbvf/gocell/framework/pkg/testutil/testwait"
)

// wiringTxRunner is a synchronous no-tx TxRunner: it runs fn under the same ctx.
// Sufficient for the mem-backed rebuild path (the real atomic apply+checkpoint
// commit is a PG concern, exercised in the adapter integration suite).
type wiringTxRunner struct{}

func (wiringTxRunner) RunInTx(ctx context.Context, fn func(context.Context) error) error {
	return fn(ctx)
}

var _ persistence.TxRunner = wiringTxRunner{}

// wiringRegistrar is a minimal SubscribeRegistrar: the live-subscription wiring is
// not under test here (the rebuild drain is the SOLE applier while the gate is
// shut), so Subscribe just succeeds.
type wiringRegistrar struct{}

func (wiringRegistrar) Subscribe(
	_ contractspec.ContractSpec,
	_ outbox.EntryHandler,
	_ string,
	_ string,
	_ ...cell.SubscriptionOption,
) error {
	return nil
}

var _ projection.SubscribeRegistrar = wiringRegistrar{}

// TestSagaJournalSource_RebuildWiring_FoldsEventsUnderSystemPrincipal is the
// end-to-end proof that SagaJournalSource plugs into the existing projection
// rebuild harness (Coordinator + MemCheckpointStore + serial-delivery gate) — the
// "接入 harness rebuild 路径" deliverable of #1627 — AND that the saga carrier's
// SystemPrincipal identity reaches the business Apply through a real Rebuild drain
// (ADR #1609 §5: the trigger's admin principal must NOT leak into Apply).
//
// It composes the real Coordinator (not a stub) so a regression in either the
// carrier identity install or the Rebuild-boundary clearAmbientPrincipal would
// fail here.
func TestSagaJournalSource_RebuildWiring_FoldsEventsUnderSystemPrincipal(t *testing.T) {
	t.Parallel()

	const nEvents = 5

	factory := newMemJournalFactory()
	j, clk, cleanup := factory(t)
	defer cleanup()

	seeded := seedGlobalEvents(t, j, clk, nEvents)
	if len(seeded) != nEvents {
		t.Fatalf("seedGlobalEvents returned %d carriers, want %d", len(seeded), nEvents)
	}

	gr, ok := j.(journal.GlobalReader)
	if !ok {
		t.Fatalf("journal %T does not implement journal.GlobalReader", j)
	}
	src, err := sagaprojection.NewSagaJournalSource(gr)
	if err != nil {
		t.Fatalf("NewSagaJournalSource: %v", err)
	}

	var (
		mu      sync.Mutex
		applied []string // EventID of every folded event
		actors  []string // ActorIDFrom(ctx) observed inside Apply
	)
	apply := func(ctx context.Context, ev projection.ProjectionEvent) error {
		mu.Lock()
		defer mu.Unlock()
		applied = append(applied, ev.EventID())
		actor, _ := ctxkeys.ActorIDFrom(ctx)
		actors = append(actors, actor)
		return nil
	}

	coord, err := projection.NewCoordinator(clk, projection.CoordinatorConfig{
		Registrar:    wiringRegistrar{},
		CellID:       "test_cell",
		ProjectionID: "saga_terminal_proj",
		TxRunner:     wiringTxRunner{},
		Store:        projection.NewMemCheckpointStore(),
		Cursor:       src,
		Replay:       src,
		Tracer:       wrapper.NoopTracer{},
	})
	if err != nil {
		t.Fatalf("NewCoordinator: %v", err)
	}

	// The projection subscribes to the saga journal's single logical stream; the
	// per-spec drainGap filter (entry.Stream() == spec.Topic) applies every saga
	// event because SagaJournalSource reports SagaJournalStream for all of them.
	spec := contractspec.ContractSpec{
		ID:        "event.saga.journal.v1",
		Kind:      cellvocab.ContractEvent,
		Transport: "internal",
		Topic:     sagaprojection.SagaJournalStream,
	}
	if err := coord.Subscribe(context.Background(), spec, apply); err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	// Trigger the rebuild under an ambient ADMIN principal — exactly the
	// AdminListener case the ADR §5 impersonation fix targets. The admin identity
	// is forwarded through context.WithoutCancel at the Rebuild detach boundary,
	// where clearAmbientPrincipal strips it; the saga carrier then installs the
	// system identity. Neither must reach Apply as "admin-operator".
	triggerCtx := ctxkeys.WithActorID(context.Background(), "admin-operator")
	if err := coord.Rebuild(triggerCtx); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	waitForLive(t, coord)

	mu.Lock()
	defer mu.Unlock()

	if len(applied) != nEvents {
		t.Fatalf("rebuild folded %d events, want %d (SagaJournalSource not fully drained through the harness)", len(applied), nEvents)
	}
	// Every Apply ctx must run under the system identity, never the admin trigger.
	for i, actor := range actors {
		if actor != projection.SystemPrincipalActor {
			t.Errorf("Apply[%d] ran under actor %q, want %q "+
				"(saga rebuild Apply must use the system identity, not the trigger's admin principal — ADR #1609 §5)",
				i, actor, projection.SystemPrincipalActor)
		}
	}
}

// waitForLive polls the coordinator phase until it returns to PhaseLive (rebuild
// complete) or a generous real-time deadline elapses. The mem-backed rebuild is
// synchronous and fast; the deadline only guards against a hang.
//
// Uses testwait.External (TEST-SLEEP-DISCIPLINE-01) because the rebuild state
// machine runs on a background goroutine with no exported channel signal — polling
// is the only viable synchronization mechanism here.
func waitForLive(t *testing.T, c *projection.Coordinator) {
	t.Helper()
	testwait.External(t, "sagaprojection-rebuild-wait-live",
		func() bool { return c.Phase() == projection.PhaseLive },
		testtime.EventuallyLong, testtime.FastPoll,
		"phase != PhaseLive (current: %v)", c.Phase())
}
