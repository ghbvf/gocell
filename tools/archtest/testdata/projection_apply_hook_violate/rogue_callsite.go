// Package projection_apply_hook_violate is a synthetic violation fixture for the
// PROJECTION-APPLY-HOOK-FUNNEL-01 archtest rule. It contains a rogue
// Coordinator.Subscribe callsite in a non-allowlisted, non-generated file.
//
// DO NOT use this package in production code.
package projection_apply_hook_violate

import (
	"context"

	"github.com/ghbvf/gocell/framework/kernel/contractspec"
	"github.com/ghbvf/gocell/framework/kernel/projection"
)

// rogueSubscribe calls Coordinator.Subscribe from a file that is not in the
// kernel/projection/ package, not a _test.go, and not a cellgen DO-NOT-EDIT
// file. This must trigger PROJECTION-APPLY-HOOK-FUNNEL-01.
func rogueSubscribe(coord *projection.Coordinator) {
	spec := contractspec.ContractSpec{
		ID:        "event.test.v1",
		Kind:      "event",
		Transport: "amqp",
		Topic:     "test.v1",
	}
	var apply projection.Apply = func(_ context.Context, _ projection.ProjectionEvent) error {
		return nil
	}
	// VIOLATION: Coordinator.Subscribe called from a non-allowlisted file.
	// PROJECTION-APPLY-HOOK-FUNNEL-01 must fire here.
	_ = coord.Subscribe(context.Background(), spec, apply)
}
