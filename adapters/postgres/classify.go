package postgres

// classify.go — SQLSTATE transient-vs-permanent classifier for the PostgreSQL
// adapter. All transient errors are routed through errcode.WrapInfra (the single
// funnel that sets KindUnavailable + CategoryInfra + private transient marker).
//
// SQLSTATE classes recognized as transient:
//
//	40001  serialization_failure  — safe to retry inside a new transaction
//	40P01  deadlock_detected      — safe to retry inside a new transaction
//	08xxx  connection_exception   — network/socket-level failures (08000, 08003,
//	                               08006, 08P01, etc.)
//
// context.DeadlineExceeded is also transient; context.Canceled is NOT (the
// caller gave up — retrying is pointless).
//
// pgconn.SafeToRetry(err) covers driver-level SafeToRetry() implementations
// (e.g. *pgconn.connectError) that are safe to retry from the pgconn layer.
//
// ref: jackc/pgx pgconn SafeToRetry (github.com/jackc/pgx/v5/pgconn)
// ref: https://www.postgresql.org/docs/current/errcodes-appendix.html
// ref: archtest ADAPTER-ERROR-CLASSIFICATION-TRANSIENT-01 (WrapInfra funnel)

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/ghbvf/gocell/pkg/errcode"
)

// sqlStateClassPrefix returns the 2-character SQLSTATE class prefix (the first
// two characters of the 5-character code, with any trailing alphanumeric for
// class codes that use a letter suffix like "08P01").
//
// Per the PostgreSQL spec the class is always the first two characters.
func sqlStateClassPrefix(code string) string {
	if len(code) < 2 {
		return code
	}
	return code[:2]
}

// isRetryablePGError reports whether err represents a condition that is safe to
// retry (with a new transaction or reconnect attempt).
//
// Returns true when:
//   - pgconn.SafeToRetry(err) — driver-level safe-to-retry signal
//   - errors.As → *pgconn.PgError with SQLSTATE 40001 (serialization_failure)
//   - errors.As → *pgconn.PgError with SQLSTATE 40P01 (deadlock_detected)
//   - errors.As → *pgconn.PgError whose class prefix == "08" (connection_exception)
//   - errors.Is  → context.DeadlineExceeded
//
// Returns false for:
//   - context.Canceled  — the caller abandoned the work; retry is pointless
//   - all other SQLSTATE codes (fail-closed: unclassified = permanent)
//   - nil
//
// ref: jackc/pgx pgconn SafeToRetry
// ref: https://www.postgresql.org/docs/current/errcodes-appendix.html
func isRetryablePGError(err error) bool {
	if err == nil {
		return false
	}

	// context.Canceled FIRST — the caller abandoned the work; retry is
	// pointless. This MUST precede pgconn.SafeToRetry: pgx sets safeToRetry
	// on its connect/exec wrapper when the failure occurred before any bytes
	// were sent, which is exactly the shape of a context-canceled acquire —
	// so SafeToRetry(canceledErr) can be true. Checking Canceled first
	// prevents a caller-canceled operation from being mislabeled transient.
	if errors.Is(err, context.Canceled) {
		return false
	}

	// Driver-level safe-to-retry signal (e.g. *pgconn.connectError).
	if pgconn.SafeToRetry(err) {
		return true
	}

	// context.DeadlineExceeded — the deadline may not recur on retry.
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}

	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return false
	}

	switch pgErr.Code {
	case "40001", // serialization_failure
		"40P01": // deadlock_detected
		return true
	}

	// SQLSTATE class 08 — connection_exception (08000, 08003, 08006, 08P01, …)
	if sqlStateClassPrefix(pgErr.Code) == "08" {
		return true
	}

	return false
}

// classifyPGError routes a raw PostgreSQL error to a transient or permanent
// errcode. The caller supplies the errcode.Code and a string context that
// describes the operation (used as errcode.WithInternal text for server-side
// logs — NOT placed in message, which must be a const literal per
// MESSAGE-CONST-LITERAL-01).
//
// Transient branch (isRetryablePGError == true):
//
//	errcode.WrapInfra(permanentCode, "postgres: transient error", err,
//	    errcode.WithInternal(opContext))
//
// WrapInfra sets KindUnavailable + CategoryInfra + the private transient
// marker, enabling errcode.IsTransient to recognize the result. The caller's
// operation code is reused (no separate ErrAdapterPGTransient constant).
//
// Permanent branch (default, fail-closed):
//
//	errcode.Wrap(errcode.KindInternal, permanentCode, "postgres: operation failed", err,
//	    errcode.WithInternal(opContext))
//
// Constraint-violation helpers (IsUniqueViolation / IsForeignKeyViolation /
// IsLastAdminProtected) remain separate and are NOT routed through this
// function — they produce domain signals, not infra signals.
//
// ref: jackc/pgx pgconn SafeToRetry
// ref: archtest ADAPTER-ERROR-CLASSIFICATION-TRANSIENT-01
func classifyPGError(err error, permanentCode errcode.Code, opContext string) error {
	if isRetryablePGError(err) {
		return errcode.WrapInfra(permanentCode,
			"postgres: transient error", err,
			errcode.WithInternal(opContext))
	}
	return errcode.Wrap(errcode.KindInternal, permanentCode,
		"postgres: operation failed", err,
		errcode.WithInternal(opContext))
}
