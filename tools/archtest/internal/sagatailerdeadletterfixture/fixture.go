//go:build archtest_fixture

// Package sagatailerdeadletterfixture is a synthetic violation fixture for
// SAGA-TAILER-DEAD-LETTER-WRITER-01. It contains a stub type that calls
// projection.DeadLetterStore.Record from an UNSANCTIONED function (not
// (*Tailer).skipPoisonEvent), which the rule's detector must flag.
//
// The fixture is loaded only via Run(t, Fixture(...)) (archtest_fixture build
// tag), so it never enters a normal build or the production scan.
//
// DO NOT use this package in production code.
package sagatailerdeadletterfixture

import (
	"context"

	"github.com/ghbvf/gocell/framework/kernel/projection"
)

// badWriter is a stub struct that illegally records a dead-letter from a function
// that is NOT (*Tailer).skipPoisonEvent.
type badWriter struct {
	store projection.DeadLetterStore
}

// illegalRecord is an unsanctioned function that calls DeadLetterStore.Record.
// SAGA-TAILER-DEAD-LETTER-WRITER-01 must fire on the call below because the
// enclosing function is not skipPoisonEvent on a *Tailer receiver.
func (b *badWriter) illegalRecord(ctx context.Context) error {
	return b.store.Record(ctx, projection.DeadLetter{CellID: "cell", ProjectionID: "proj", GlobalSeq: 1})
}
