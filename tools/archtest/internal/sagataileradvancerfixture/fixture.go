//go:build archtest_fixture

// Package sagataileradvancerfixture is a synthetic violation fixture for
// SAGA-TAILER-CHECKPOINT-ADVANCER-CALLER-01. It contains a stub type that
// calls projection.OwnerCheckpointStore.AdvanceIfOwner from an UNSANCTIONED
// function (not (*Tailer).commitEvent), which the rule's detector must flag.
//
// The fixture is loaded only via Run(t, Fixture(...)) (archtest_fixture build
// tag), so it never enters a normal build or the production scan.
//
// DO NOT use this package in production code.
package sagataileradvancerfixture

import (
	"context"

	"github.com/ghbvf/gocell/kernel/projection"
)

// badCaller is a stub struct that illegally calls AdvanceIfOwner from a
// function that is NOT (*Tailer).commitEvent.
// SAGA-TAILER-CHECKPOINT-ADVANCER-CALLER-01 must fire on the call below.
type badCaller struct {
	store projection.OwnerCheckpointStore
}

// illegalAdvance is an unsanctioned function that calls AdvanceIfOwner.
// The rule must flag this because the enclosing function is not commitEvent
// on a *Tailer receiver.
func (b *badCaller) illegalAdvance(ctx context.Context) error {
	return b.store.AdvanceIfOwner(ctx, "cell", "proj", "token", 42)
}
