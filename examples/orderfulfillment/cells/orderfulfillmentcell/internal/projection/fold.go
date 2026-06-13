package projection

import (
	"fmt"

	orderstatusgen "github.com/ghbvf/gocell/generated/contracts/http/orderfulfillment/orderstatus/v1"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/saga/journal"
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
// Unknown/zero kind → fail-closed: returns ("", error). The Tailer treats a
// returned error from the Apply handler as permanent (DLX route).
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
		// Unknown / zero kind — fail-closed. The caller must wrap this as a
		// PermanentError (DLX) so the bad event is not silently retried.
		return "", outbox.NewPermanentError(
			fmt.Errorf("projection.FoldStatus: unknown journal.EventKind %q; reject as permanent", kind.String()),
		)
	}
}
