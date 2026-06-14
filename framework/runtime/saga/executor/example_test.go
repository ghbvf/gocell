package executor_test

import (
	"context"
	"fmt"
	"time"

	"github.com/ghbvf/gocell/framework/kernel/clock"
	ksaga "github.com/ghbvf/gocell/framework/kernel/saga"
	"github.com/ghbvf/gocell/framework/pkg/idutil"
	"github.com/ghbvf/gocell/framework/runtime/saga/executor"
)

// Test-time Duration values live in a package-level const block
// (TEST-TIME-LITERAL-01), never inline at the call site. Both equal the
// executor package defaults (DefaultHeartbeatInterval / DefaultLeaseDuration);
// passed explicitly only to illustrate the options.
const (
	exampleHeartbeatInterval = 10 * time.Second
	exampleLeaseDuration     = 30 * time.Second
)

// exampleHeartbeater is a minimal executor.Heartbeater for the example: it
// always reports the lease as still held. A production Heartbeater renews the
// lease against the saga journal — any journal.Journal satisfies the interface
// structurally, which is why the Coordinator can pass its own journal in.
type exampleHeartbeater struct{}

func (exampleHeartbeater) Heartbeat(_ context.Context, _, _ idutil.SafeID, _ time.Duration) (bool, error) {
	return true, nil
}

// ExampleNewExecutor shows the lower-level Executor used standalone. Most
// consumers do NOT build an Executor directly — saga.NewCoordinator constructs
// one internally (see ExampleNewCoordinator). This example documents the
// advanced path for code that drives a single step itself: NewExecutor takes a
// Heartbeater plus a clock, and WithObserver / WithHeartbeatInterval /
// WithLeaseDuration tune it. Execute runs one step with retry/heartbeat and
// returns a Result classifying the outcome.
func ExampleNewExecutor() {
	clk := clock.Real()

	exec, err := executor.NewExecutor(exampleHeartbeater{}, clk,
		executor.WithObserver(executor.NopObserver{}),
		executor.WithHeartbeatInterval(exampleHeartbeatInterval),
		executor.WithLeaseDuration(exampleLeaseDuration),
	)
	if err != nil {
		panic(err)
	}

	step := ksaga.Step{
		Name: "charge_card",
		Run: func(_ context.Context, _ *ksaga.Instance, _ []byte) ([]byte, error) {
			return []byte(`{"charged":true}`), nil
		},
	}
	inst := ksaga.NewInstance("payment_inst_1", "payment_saga", clk.Now())

	// In production leaseID is the CAS fencing token from journal.ClaimPending's
	// ci.LeaseID, not a hand-picked string; "lease_1" works here only because
	// exampleHeartbeater always reports the lease as still held.
	res := exec.Execute(context.Background(), &inst, "lease_1", step, ksaga.RetryPolicy{}, nil)
	fmt.Println(res.Outcome)
	// Output:
	// Succeeded
}
