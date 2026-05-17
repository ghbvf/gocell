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
	"net"

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
//   - errors.As → net.Error with Timeout() == true (network-level timeout)
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

	// net.Error.Timeout() — transport-level timeout (e.g. *net.OpError from
	// pgx dial when a network-level deadline fires). Retry is safe; the
	// dedicated ConnectTimeout code substitution lives in
	// classifyPGConnectError. For non-connect callers this stays transient
	// under the caller-supplied code (savepoint / query semantics preserved).
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
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
// errcode for **non-connect** call paths (query / commit / savepoint). For
// connect-class call paths (pool init ping / pool health / TxManager Begin)
// use [classifyPGConnectError] instead — it adds the dedicated
// ErrAdapterPGConnectTimeout substitution that this funnel intentionally does
// not perform.
//
// The split is the "typed function choice" Hard pattern (.claude/rules/gocell/
// ai-collab.md §3): picking the wrong funnel name yields the wrong error code
// at the call site, with no runtime fallback. archtest
// ADAPTER-ERROR-CLASSIFICATION-TRANSIENT-01 verifies both function bodies
// route their transient branch through errcode.WrapInfra.
//
// The caller supplies the errcode.Code and a string context that describes
// the operation (used as errcode.WithInternal text for server-side logs —
// NOT placed in message, which must be a const literal per
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
// Timeout-class causes (context.DeadlineExceeded, net.Error.Timeout() == true)
// stay transient under the caller's code — they do not get coerced to
// ErrAdapterPGConnectTimeout because non-connect callers must keep their own
// semantic code (ErrAdapterPGQuery for savepoint, etc.).
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

// classifyPGConnectError is the dedicated funnel for **connect-class** call
// paths (pool init ping / pool health / TxManager Begin). Network-level
// timeouts get the distinct ErrAdapterPGConnectTimeout code so operators can
// route ConnectTimeout alerts independently from generic connect/query
// failures. Non-timeout errors delegate to [classifyPGError] with
// ErrAdapterPGConnect so the transient/permanent disposition stays single-
// sourced.
//
// Timeout sub-branch (Wave-4-B ADAPTER-CONNECT-BUDGET-01):
//
//	errcode.WrapInfra(ErrAdapterPGConnectTimeout, "postgres: connect timeout", err,
//	    errcode.WithInternal(opContext))
//
// Triggered by context.DeadlineExceeded or net.Error.Timeout() == true —
// typically *net.OpError surfaced from pgxpool's dial path when
// Config.ConnectTimeout fires.
//
// Non-connect callers (commit / savepoint / query) MUST use [classifyPGError]
// directly; picking this funnel would relabel their own timeout-class errors
// with the wrong ConnectTimeout code.
//
// ref: jackc/pgx pgconn SafeToRetry
// ref: archtest ADAPTER-ERROR-CLASSIFICATION-TRANSIENT-01
func classifyPGConnectError(err error, opContext string) error {
	if isConnectTimeout(err) {
		return errcode.WrapInfra(ErrAdapterPGConnectTimeout,
			"postgres: connect timeout", err,
			errcode.WithInternal(opContext))
	}
	return classifyPGError(err, ErrAdapterPGConnect, opContext)
}

// isConnectTimeout reports whether err carries a network-level timeout signal:
//   - errors.Is(err, context.DeadlineExceeded)
//   - any error in the chain implementing net.Error with Timeout() == true
//
// Used ONLY by classifyPGConnectError to substitute ErrAdapterPGConnectTimeout
// for the generic transient code; non-connect callers must not invoke this
// helper directly (the typed-funnel split is the upstream guardrail). The
// retry-vs-permanent disposition for non-connect callers already handles
// timeout-class errors via isRetryablePGError (DeadlineExceeded + net.Error
// timeout, both return true → caller's permanentCode under WrapInfra).
// context.Canceled is NOT a timeout — caller-abandoned work is filtered out
// by isRetryablePGError before this helper is consulted.
func isConnectTimeout(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}
	return false
}
