package projection

import (
	"fmt"

	"github.com/ghbvf/gocell/framework/kernel/outbox"
	"github.com/ghbvf/gocell/framework/kernel/saga/journal"
	orderstatusgen "github.com/ghbvf/gocell/generated/contracts/http/orderfulfillment/orderstatus/v1"
)

// terminalStatuses is the closed set of terminal status values. Used for the
// terminal-absorbing (monotonic) check in FoldStatus.
var terminalStatuses = map[orderstatusgen.ResponseDataStatus]struct{}{
	orderstatusgen.ResponseDataStatusSucceeded:   {},
	orderstatusgen.ResponseDataStatusCompensated: {},
	orderstatusgen.ResponseDataStatusFailed:      {},
}

// FoldStatus computes the next order status from the previous one and a new
// saga journal event kind.
//
// Terminal-absorbing (monotonic): if prev is already a terminal status
// (succeeded / compensated / failed), FoldStatus returns prev unchanged for
// ANY subsequent kind. This makes Apply idempotent under re-delivery and
// replay.
//
// Unknown/zero kind → fail-closed: returns ("", error) wrapped permanent. A
// permanent apply error makes the saga-journal Tailer SKIP the poison event —
// record it to the dead-letter sink and advance the checkpoint past it in one
// transaction — so a single bad event does not freeze the projection (#2110).
//
// Note: "accepted" is NOT a valid stored status — it means the read model has
// NO row yet (handled by the query layer). FoldStatus only stores Running or a
// terminal status.
func FoldStatus(prev orderstatusgen.ResponseDataStatus, kind journal.EventKind) (orderstatusgen.ResponseDataStatus, error) {
	// Terminal-absorbing: once terminal, ignore all subsequent events.
	if _, isTerminal := terminalStatuses[prev]; isTerminal {
		return prev, nil
	}

	switch kind {
	case journal.KindSagaSucceeded:
		return orderstatusgen.ResponseDataStatusSucceeded, nil
	case journal.KindSagaCompensated:
		return orderstatusgen.ResponseDataStatusCompensated, nil
	case journal.KindSagaFailed, journal.KindSagaExpired, journal.KindSagaCompensationFailed:
		return orderstatusgen.ResponseDataStatusFailed, nil
	case journal.KindStepStarted, journal.KindStepCompleted, journal.KindStepFailed,
		journal.KindStepCompensated, journal.KindCompensationStarted, journal.KindStepCompensationFailed:
		return orderstatusgen.ResponseDataStatusRunning, nil
	default:
		// Unknown / zero kind — fail-closed as a PermanentError so the bad event
		// is not silently retried. The saga-journal Tailer dead-letters the poison
		// event and advances its checkpoint past it (#2110), so the projection
		// keeps progressing instead of freezing on the one bad event.
		return "", outbox.NewPermanentError(
			fmt.Errorf("projection.FoldStatus: unknown journal.EventKind %q; reject as permanent", kind.String()),
		)
	}
}
