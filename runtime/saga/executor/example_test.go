package executor_test

import (
	"context"
	"fmt"
	"time"

	"github.com/ghbvf/gocell/kernel/clock"
	ksaga "github.com/ghbvf/gocell/kernel/saga"
	"github.com/ghbvf/gocell/pkg/idutil"
	"github.com/ghbvf/gocell/runtime/saga/executor"
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
		// Both durations equal the package defaults (DefaultHeartbeatInterval /
		// DefaultLeaseDuration); passed explicitly only to illustrate the options.
		executor.WithHeartbeatInterval(10*time.Second),
		executor.WithLeaseDuration(30*time.Second),
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
