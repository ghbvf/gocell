//go:build archtest_fixture

// Package registrationnodeletefixture is the RED/GREEN fixture for
// REGISTRATION-EVENT-NO-DELETE-01 (#2386 / ADR 202606162119-303 §"Amendment
// 2026-06-19 — #2392/#2386"). It is the negative control proving the const-string
// scanner actually fires on a DELETE/TRUNCATE of contract_registration_events —
// without it the "expect zero matches in production" rule could pass green forever
// on a broken regex or scan. Mirrors tools/archtest/internal/projectionnodeletefixture.
//
// RED (must be flagged — exactly eight):
//   - badDelete:                 DELETE FROM contract_registration_events …
//   - badTruncate:               TRUNCATE TABLE contract_registration_events
//   - badTruncateSchemaQualified: TRUNCATE public.contract_registration_events
//   - badQuotedDelete:           DELETE FROM "contract_registration_events"        (delimited identifier)
//   - badQuotedTruncate:         TRUNCATE TABLE "contract_registration_events"     (delimited identifier)
//   - badQuotedSchemaQualified:  TRUNCATE "public"."contract_registration_events"  (delimited schema+table)
//   - badConstConcatLiteral:     "DELETE FROM " + "contract_registration_events"   (compile-time fold)
//   - badConstConcatName:        "TRUNCATE TABLE " + fixtureTable                   (compile-time fold of a const ref)
//
// GREEN (must NOT be flagged):
//   - goodInsert / goodSelect:   the sanctioned append + replay-read (reference the table,
//     not a DELETE/TRUNCATE).
//   - goodSiblingTableDelete:    DELETE FROM contract_registration_events_archive — a different
//     table; proves the \bcontract_registration_events\b word boundary does not over-match a
//     prefix collision.
//   - goodRuntimeConcat:         "DELETE FROM " + table where table is a RUNTIME (non-const)
//     value — the documented literal/const-scan blind spot: no constant
//     contract_registration_events string to fold, so it is (intentionally) not flagged.
package registrationnodeletefixture

import "context"

// fixtureTable is a compile-time string constant used by badConstConcatName to prove the
// scanner folds "verb" + constTableName, not just whole-literal SQL.
const fixtureTable = "contract_registration_events"

// fakeTx mimics the pgx.Tx.Exec shape (ctx, sql, args...) so the fixture reads like real
// adapter code, but the scanner folds the SQL string via go/types and fires on the value
// regardless of the surrounding call — no real pgx dependency needed.
type fakeTx struct{}

func (fakeTx) Exec(ctx context.Context, sql string, args ...any) (any, error) { return nil, nil }

// --- RED: every form below MUST be flagged ---

// badDelete — a DELETE against the append-only migration history (the forbidden violation).
func badDelete(ctx context.Context, tx fakeTx) {
	_, _ = tx.Exec(ctx, "DELETE FROM contract_registration_events WHERE seq < $1", 0)
}

// badTruncate — TRUNCATE wipes the history wholesale.
func badTruncate(ctx context.Context, tx fakeTx) {
	_, _ = tx.Exec(ctx, "TRUNCATE TABLE contract_registration_events")
}

// badTruncateSchemaQualified — the schema-qualified bare form.
func badTruncateSchemaQualified(ctx context.Context, tx fakeTx) {
	_, _ = tx.Exec(ctx, "TRUNCATE public.contract_registration_events")
}

// badQuotedDelete — a delimited (double-quoted) identifier; a legal PostgreSQL reference
// to the same table that the regex must also catch.
func badQuotedDelete(ctx context.Context, tx fakeTx) {
	_, _ = tx.Exec(ctx, `DELETE FROM "contract_registration_events" WHERE registration_id = $1`, "x")
}

// badQuotedTruncate — TRUNCATE with a delimited identifier.
func badQuotedTruncate(ctx context.Context, tx fakeTx) {
	_, _ = tx.Exec(ctx, `TRUNCATE TABLE "contract_registration_events"`)
}

// badQuotedSchemaQualified — delimited schema AND table.
func badQuotedSchemaQualified(ctx context.Context, tx fakeTx) {
	_, _ = tx.Exec(ctx, `TRUNCATE "public"."contract_registration_events"`)
}

// badConstConcatLiteral — the verb and table are split across a compile-time concatenation
// of two string literals; go/types folds it to "DELETE FROM contract_registration_events".
func badConstConcatLiteral(ctx context.Context, tx fakeTx) {
	_, _ = tx.Exec(ctx, "DELETE FROM "+"contract_registration_events")
}

// badConstConcatName — the table name is a string constant referenced by name; the fold
// yields "TRUNCATE TABLE contract_registration_events".
func badConstConcatName(ctx context.Context, tx fakeTx) {
	_, _ = tx.Exec(ctx, "TRUNCATE TABLE "+fixtureTable)
}

// --- GREEN: none of the forms below may be flagged ---

// goodInsert — the sanctioned append. References the table but is not a DELETE/TRUNCATE.
func goodInsert(ctx context.Context, tx fakeTx) {
	_, _ = tx.Exec(ctx, "INSERT INTO contract_registration_events (tenant_id, registration_id, seq, to_state) VALUES ($1, $2, $3, $4)")
}

// goodSelect — a History replay read. Not a DELETE/TRUNCATE.
func goodSelect(ctx context.Context, tx fakeTx) {
	_, _ = tx.Exec(ctx, "SELECT seq, from_state, to_state FROM contract_registration_events WHERE registration_id = $1 ORDER BY seq")
}

// goodSiblingTableDelete — a DELETE on a DIFFERENT table whose name has
// contract_registration_events as a prefix. \bcontract_registration_events\b must NOT match
// it (the trailing \b sits between two word chars: …events_archive).
func goodSiblingTableDelete(ctx context.Context, tx fakeTx) {
	_, _ = tx.Exec(ctx, "DELETE FROM contract_registration_events_archive WHERE registration_id = $1", "x")
}

// goodRuntimeConcat — the documented blind spot: the table name arrives as a runtime
// (non-const) value, so there is no constant contract_registration_events string to fold.
// The scan (intentionally) cannot see it; the DB-engine REVOKE (migration 066) is the Hard
// guard that still covers this at runtime for the serving role.
func goodRuntimeConcat(ctx context.Context, tx fakeTx, table string) {
	_, _ = tx.Exec(ctx, "DELETE FROM "+table)
}
