package postgres

import (
	"strings"

	"github.com/ghbvf/gocell/pkg/pgquery"
)

// lastAdminTriggerSentinel is the message prefix emitted by the
// effective_admin_invariant_fn trigger function (migration 024) when it
// blocks a mutation that would leave the system with no effective admin. The
// full trigger message is:
//
//	"effective_admin_invariant: would leave the system with no effective admin"
//
// The colon delimiter is load-bearing: isLastAdminProtected matches
// strings.HasPrefix(msg, sentinel+":") to avoid false-positives from any
// hypothetical sibling trigger whose name starts with the same prefix
// (e.g. "effective_admin_invariant_v2: ..."). Bare prefix would return true
// for such a sibling; colon-delimited match is exact.
//
// ref: adapters/postgres/migrations/024_effective_admin_invariant.sql
const lastAdminTriggerSentinel = "effective_admin_invariant"

// isLastAdminProtected reports whether err (or any error in its Unwrap chain)
// is the PL/pgSQL exception raised by the effective_admin_invariant_fn trigger
// function (migration 024). Distinct from a bare SQLSTATE P0001 check because
// P0001 is a generic class used by any RAISE EXCEPTION site.
//
// The match requires both:
//  1. SQLSTATE P0001 (PL/pgSQL RAISE EXCEPTION) — classified via the single
//     source pgquery.AsRaiseException so this file never duplicates the
//     SQLSTATE code knowledge (SQLSTATE-SINGLE-SOURCE-01). AsRaiseException
//     returns the unwrapped *pgconn.PgError in one Unwrap walk so we never
//     errors.As twice.
//  2. Message starts with lastAdminTriggerSentinel + ":" — the colon delimiter
//     ensures that a sibling trigger "effective_admin_invariant_v2: ..." does
//     NOT match (P2-3 precision requirement). The message is a trigger-specific
//     attribution signal, not a SQLSTATE code, so it stays local here.
//
// ref: adapters/postgres/migrations/024_effective_admin_invariant.sql
func isLastAdminProtected(err error) bool {
	pgErr, ok := pgquery.AsRaiseException(err)
	if !ok {
		return false
	}
	return strings.HasPrefix(pgErr.Message, lastAdminTriggerSentinel+":")
}
