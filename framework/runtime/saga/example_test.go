package saga_test

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/ghbvf/gocell/framework/kernel/clock"
	kernelmetrics "github.com/ghbvf/gocell/framework/kernel/observability/metrics"
	"github.com/ghbvf/gocell/framework/kernel/outbox"
	"github.com/ghbvf/gocell/framework/kernel/persistence"
	ksaga "github.com/ghbvf/gocell/framework/kernel/saga"
	"github.com/ghbvf/gocell/framework/kernel/saga/journal"
	"github.com/ghbvf/gocell/framework/kernel/wrapper"
	obsmetrics "github.com/ghbvf/gocell/framework/runtime/observability/metrics"
	"github.com/ghbvf/gocell/framework/runtime/saga"
)

// Test-time Duration values live in a package-level const block
// (TEST-TIME-LITERAL-01), never inline at the call site.
const (
	// examplePollInterval shortens the Coordinator's claim cadence so the example
	// drives its single instance promptly (DefaultConfig uses 200ms).
	examplePollInterval = 5 * time.Millisecond
	// exampleSagaTimeout is the overall saga deadline (the Expired ceiling).
	exampleSagaTimeout = 30 * time.Second
	// exampleWaitBudget bounds every asynchronous lifecycle wait (Ready, the
	// step running, Stop's drain). A regression — Start exiting early, the tick
	// loop never claiming, or the step never running — then fails this Example
	// locally within the budget instead of hanging until the whole package's
	// `go test` deadline. It runs on the wall clock deliberately: the Coordinator
	// below is wired to clock.Real(), so a fake clock would never advance these
	// real-time waits.
	exampleWaitBudget = 10 * time.Second
)

// awaitExample blocks until ch is signaled, but panics if the coordinator's
// Start goroutine exits first (surfacing its error) or the wait budget elapses.
// An Example has no *testing.T, so a labeled panic is the only way to turn a
// regression into a local failure instead of a low-signal whole-package
// `go test` timeout. It lives outside ExampleNewCoordinator so each wait reads
// as a single self-documenting line in the godoc-rendered body.
func awaitExample(what string, ch <-chan struct{}, startErr <-chan error) {
	select {
	case <-ch:
	case err := <-startErr:
		panic(fmt.Errorf("saga example: coordinator Start exited before %s: %w", what, err))
	case <-time.After(exampleWaitBudget):
		panic("saga example: timed out waiting for " + what)
	}
}

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

	// 6. Run the control loop and enqueue one instance. Start's return is
	//    captured in a buffered channel (not discarded with `_ =`) so a Start
	//    failure surfaces and the goroutine's exit is observable.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	startErr := make(chan error, 1)
	go func() { startErr <- coord.Start(ctx) }()
	awaitExample("coordinator ready", coord.Ready(), startErr)

	inst := ksaga.NewInstance("payment_inst_1", "payment_saga", clk.Now())
	if err := j.Enqueue(ctx, inst); err != nil {
		panic(err)
	}

	// 7. Wait for the step to run, then Stop. Stop drains the in-flight drive —
	//    including the terminal MarkTerminal write — before it returns, so the
	//    journal deterministically holds the terminal event afterward, with no
	//    polling. Stop is given a deadline-bounded context (the drain budget):
	//    production should do the same so a wedged step cannot make shutdown hang.
	awaitExample("charge_card step to run", stepRan, startErr)
	stopCtx, stopCancel := context.WithTimeout(context.Background(), exampleWaitBudget)
	defer stopCancel()
	if err := coord.Stop(stopCtx); err != nil {
		panic(err)
	}

	// Stop cancels the Coordinator's internal context, so Start has unwound by
	// now; confirm its goroutine exited without error.
	if err := <-startErr; err != nil {
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
