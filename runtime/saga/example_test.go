package saga_test

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/ghbvf/gocell/kernel/clock"
	kernelmetrics "github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/persistence"
	ksaga "github.com/ghbvf/gocell/kernel/saga"
	"github.com/ghbvf/gocell/kernel/saga/journal"
	"github.com/ghbvf/gocell/kernel/wrapper"
	obsmetrics "github.com/ghbvf/gocell/runtime/observability/metrics"
	"github.com/ghbvf/gocell/runtime/saga"
)

// Test-time Duration values live in a package-level const block
// (TEST-TIME-LITERAL-01), never inline at the call site.
const (
	// examplePollInterval shortens the Coordinator's claim cadence so the example
	// drives its single instance promptly (DefaultConfig uses 200ms).
	examplePollInterval = 5 * time.Millisecond
	// exampleSagaTimeout is the overall saga deadline (the Expired ceiling).
	exampleSagaTimeout = 30 * time.Second
)

// exampleTxRunner is a minimal in-memory persistence.TxRunner sufficient to run
// the example against journal.MemJournal. The Coordinator persists each step
// (Append + MarkTerminal) inside RunInTx and registers a post-commit dispatcher
// kick via persistence.RegisterAfterCommit, so a TxRunner MUST install the
// after-commit registry and drain it on success — a bare fn(ctx) is not enough.
//
// Production wiring passes a real, transactional runner (adapters/postgres.TxManager)
// that also confines all DB access to fn's ctx; this in-memory runner exists only to
// keep the example self-contained (it has no connection to misuse).
type exampleTxRunner struct{}

func (exampleTxRunner) RunInTx(ctx context.Context, fn func(context.Context) error) error {
	ctx, installed := persistence.WithAfterCommitRegistry(ctx)
	mark := persistence.AfterCommitMark(ctx)
	if err := fn(ctx); err != nil {
		persistence.TruncateAfterCommitTo(ctx, mark)
		return err
	}
	if installed {
		persistence.RunAfterCommitHooks(ctx)
	}
	return nil
}

// ExampleNewCoordinator shows the minimal single-package wiring for a saga
// Coordinator and drives one instance to a terminal state.
//
// Since #1181/#1210 the Coordinator builds its own *executor.Executor
// internally (using the supplied journal as the heartbeater so claim and
// heartbeat are same-source by construction). A consumer therefore learns only
// this one package: WithObserver / WithConfig / WithTracer configure both the
// coordinator and the step layer through a single call site. There is no
// saga.WithExecutor or executor.WithObserver at the call site.
func ExampleNewCoordinator() {
	clk := clock.Real()

	// 1. In-memory journal — the append-only event log the Coordinator drives.
	j, err := journal.NewMemJournal(clk)
	if err != nil {
		panic(err)
	}

	// 2. The saga definition: one forward step that "charges a card". It closes
	//    stepRan when it runs, letting the example observe forward progress
	//    without polling (Run executes once on the success path; sync.Once keeps
	//    the close safe if a copy-paste adaptation ever retries).
	stepRan := make(chan struct{})
	var once sync.Once
	def := &ksaga.Definition{
		ID: "payment_saga",
		Steps: []ksaga.Step{{
			Name: "charge_card",
			Run: func(_ context.Context, _ *ksaga.Instance, _ []byte) ([]byte, error) {
				once.Do(func() { close(stepRan) })
				return []byte(`{"charged":true}`), nil
			},
		}},
		Timeout: exampleSagaTimeout,
	}
	reg, err := ksaga.NewInMemoryRegistry(def)
	if err != nil {
		panic(err)
	}

	// 3. The observability sink: SagaCollector implements executor.Observer and
	//    receives both step-level and coordinator-level events. NopProvider
	//    records nothing but still validates the metric label sets.
	collector, err := obsmetrics.NewSagaCollector(kernelmetrics.NopProvider{}, "examplecell")
	if err != nil {
		panic(err)
	}

	// 4. Config: start from DefaultConfig (WithConfig rejects zero-value fields,
	//    so always derive from it) and shorten the poll interval so the tick
	//    loop claims the enqueued instance promptly.
	cfg := saga.DefaultConfig()
	cfg.PollInterval = examplePollInterval

	// 5. Build the Coordinator. One WithObserver / WithTracer reaches both the
	//    coordinator and the internally-constructed Executor.
	coord, err := saga.NewCoordinator(j, exampleTxRunner{}, outbox.NewNoopEmitter(), reg, clk,
		saga.WithObserver(collector),
		saga.WithConfig(cfg),
		saga.WithTracer(wrapper.NoopTracer{}),
	)
	if err != nil {
		panic(err)
	}

	// 6. Run the control loop and enqueue one instance.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = coord.Start(ctx) }()
	<-coord.Ready()

	inst := ksaga.NewInstance("payment_inst_1", "payment_saga", clk.Now())
	if err := j.Enqueue(ctx, inst); err != nil {
		panic(err)
	}

	// 7. Wait for the step to run, then Stop. Stop drains the in-flight drive —
	//    including the terminal MarkTerminal write — before it returns, so the
	//    journal deterministically holds the terminal event afterward, with no
	//    polling. context.Background() gives Stop an unbounded drain budget;
	//    production should pass a deadline-bounded ctx.
	<-stepRan
	if err := coord.Stop(context.Background()); err != nil {
		panic(err)
	}

	// 8. Read the terminal state back via the journal.Reader (Load) — the same
	//    read path a status-query slice would use.
	events, err := j.Load(context.Background(), inst.ID)
	if err != nil {
		panic(err)
	}
	fmt.Println(events[len(events)-1].Kind)
	// Output:
	// saga_succeeded
}
