//go:build archtest_fixture

package redoutboxcall

import (
	"context"

	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/saga"
)

// makeViolatingStep constructs a saga.Step whose Compensate calls
// outbox.Writer.Write — a forbidden persistent side effect.
// SAGA-STEP-COMPENSATE-PURE-01 A1 must flag this.
func makeViolatingStep(w outbox.Writer) saga.Step {
	return saga.Step{
		Name: "violating-step",
		Run: func(_ context.Context, _ *saga.Instance, _ []byte) ([]byte, error) {
			return nil, nil
		},
		Compensate: func(ctx context.Context, _ *saga.Instance, _ []byte) error {
			return w.Write(ctx, outbox.Entry{})
		},
	}
}
