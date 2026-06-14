package saga

import (
	"errors"
	"log/slog"

	"github.com/ghbvf/gocell/framework/pkg/errcode"
)

// journalErrLevel classifies a Journal-returned error so the coordinator can
// log at the appropriate severity. The classification is sentinel-aware so
// expected races (leader handoff → ErrSagaStaleLease) do not pollute ERROR/WARN
// dashboards reserved for real infra faults.
//
// The kernel/saga/journal contract defines three caller-observable saga
// failure modes via dedicated errcode sentinels (PR-04). Operator routing
// expectations:
//
//   - ErrSagaStaleLease → Info: lease lost during normal leader handoff,
//     expected race in a multi-coordinator deployment.
//   - ErrSagaNotFound → Warn: instance disappeared mid-drive (external
//     delete / TTL purge); not a code fault but worth attention.
//   - Anything else (PG infra, JSON validation, panics) → Warn: real fault
//     surfaces at the existing default severity.
//
// Callers MUST wrap the message context themselves; this helper only picks
// the level so the WarnContext / InfoContext branch is locally obvious.
func journalErrLevel(err error) slog.Level {
	var ec *errcode.Error
	if !errors.As(err, &ec) {
		return slog.LevelWarn
	}
	switch ec.Code {
	case errcode.ErrSagaStaleLease:
		return slog.LevelInfo
	case errcode.ErrSagaNotFound:
		return slog.LevelWarn
	default:
		return slog.LevelWarn
	}
}

// foldErrLevel classifies a foldEvents error for the drive loop's "fold failed,
// marking terminal" log. ErrSagaFoldUnknownKind (a code↔journal-schema drift —
// a new EventKind reached replay without a foldEvents case) is a real
// correctness anomaly → Error so it surfaces on Error dashboards. The defensive
// errFoldEventMismatch (KindStepFailed mid-history, which the Journal should have
// MarkTerminal'd) is an expected guard → Warn. Both still MarkTerminal Failed;
// only the log severity differs.
func foldErrLevel(err error) slog.Level {
	var ec *errcode.Error
	if errors.As(err, &ec) && ec.Code == errcode.ErrSagaFoldUnknownKind {
		return slog.LevelError
	}
	return slog.LevelWarn
}
