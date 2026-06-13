//go:build archtest_fixture

// Package projectionnodeletefixture is the RED/GREEN fixture for
// PROJECTION-EVENT-JOURNAL-NO-DELETE-01 (EPIC #1504 PR-05 / ADR 202606071600-1504
// §6 I4). It is the negative control proving the const-string scanner actually fires
// on a DELETE/TRUNCATE of projection_events — without it the "expect zero matches in
// production" rule could pass green forever on a broken regex or scan.
//
// RED (must be flagged — exactly eight):
//   - badDelete:                 DELETE FROM projection_events …
//   - badTruncate:               TRUNCATE TABLE projection_events
//   - badTruncateSchemaQualified: TRUNCATE public.projection_events
//   - badQuotedDelete:           DELETE FROM "projection_events"           (delimited identifier)
//   - badQuotedTruncate:         TRUNCATE TABLE "projection_events"        (delimited identifier)
//   - badQuotedSchemaQualified:  TRUNCATE "public"."projection_events"     (delimited schema+table)
//   - badConstConcatLiteral:     "DELETE FROM " + "projection_events"      (compile-time fold)
//   - badConstConcatName:        "TRUNCATE TABLE " + fixtureTable          (compile-time fold of a const ref)
//
// GREEN (must NOT be flagged):
//   - goodInsert / goodSelect:   the sanctioned append + replay-read (reference the table,
//     not a DELETE/TRUNCATE).
//   - goodSiblingTableDelete:    DELETE FROM projection_events_archive — a different table;
//     proves the \bprojection_events\b word boundary does not over-match a prefix collision.
//   - goodRuntimeConcat:         "DELETE FROM " + table where table is a RUNTIME (non-const)
//     value — the documented literal/const-scan blind spot: no constant projection_events
//     string to fold, so it is (intentionally) not flagged.
package projectionnodeletefixture

import "context"

// fixtureTable is a compile-time string constant used by badConstConcatName to prove the
// scanner folds "verb" + constTableName, not just whole-literal SQL.
const fixtureTable = "projection_events"

// fakeTx mimics the pgx.Tx.Exec shape (ctx, sql, args...) so the fixture reads like real
// adapter code, but the scanner folds the SQL string via go/types and fires on the value
// regardless of the surrounding call — no real pgx dependency needed.
type fakeTx struct{}

func (fakeTx) Exec(ctx context.Context, sql string, args ...any) (any, error) { return nil, nil }

// --- RED: every form below MUST be flagged ---

// badDelete — a DELETE against the durable journal (the append-only violation forbidden).
func badDelete(ctx context.Context, tx fakeTx) {
	_, _ = tx.Exec(ctx, "DELETE FROM projection_events WHERE global_seq < $1", 0)
}

// badTruncate — TRUNCATE wipes the journal wholesale.
func badTruncate(ctx context.Context, tx fakeTx) {
	_, _ = tx.Exec(ctx, "TRUNCATE TABLE projection_events")
}

// badTruncateSchemaQualified — the schema-qualified bare form.
func badTruncateSchemaQualified(ctx context.Context, tx fakeTx) {
	_, _ = tx.Exec(ctx, "TRUNCATE public.projection_events")
}

// badQuotedDelete — a delimited (double-quoted) identifier; a legal PostgreSQL reference
// to the same table that the regex must also catch.
func badQuotedDelete(ctx context.Context, tx fakeTx) {
	_, _ = tx.Exec(ctx, `DELETE FROM "projection_events" WHERE id = $1`, "x")
}

// badQuotedTruncate — TRUNCATE with a delimited identifier.
func badQuotedTruncate(ctx context.Context, tx fakeTx) {
	_, _ = tx.Exec(ctx, `TRUNCATE TABLE "projection_events"`)
}

// badQuotedSchemaQualified — delimited schema AND table.
func badQuotedSchemaQualified(ctx context.Context, tx fakeTx) {
	_, _ = tx.Exec(ctx, `TRUNCATE "public"."projection_events"`)
}

// badConstConcatLiteral — the verb and table are split across a compile-time concatenation
// of two string literals; go/types folds it to "DELETE FROM projection_events".
func badConstConcatLiteral(ctx context.Context, tx fakeTx) {
	_, _ = tx.Exec(ctx, "DELETE FROM "+"projection_events")
}

// badConstConcatName — the table name is a string constant referenced by name; the fold
// yields "TRUNCATE TABLE projection_events".
func badConstConcatName(ctx context.Context, tx fakeTx) {
	_, _ = tx.Exec(ctx, "TRUNCATE TABLE "+fixtureTable)
}

// --- GREEN: none of the forms below may be flagged ---

// goodInsert — the sanctioned append. References the table but is not a DELETE/TRUNCATE.
func goodInsert(ctx context.Context, tx fakeTx) {
	_, _ = tx.Exec(ctx, "INSERT INTO projection_events (id, global_seq, topic) VALUES ($1, $2, $3) ON CONFLICT (id) DO NOTHING")
}

// goodSelect — a replay read. Not a DELETE/TRUNCATE.
func goodSelect(ctx context.Context, tx fakeTx) {
	_, _ = tx.Exec(ctx, "SELECT global_seq, id, topic FROM projection_events WHERE global_seq > $1 ORDER BY global_seq")
}

// goodSiblingTableDelete — a DELETE on a DIFFERENT table whose name has projection_events
// as a prefix. \bprojection_events\b must NOT match it (the trailing \b sits between two
// word chars: …events_archive).
func goodSiblingTableDelete(ctx context.Context, tx fakeTx) {
	_, _ = tx.Exec(ctx, "DELETE FROM projection_events_archive WHERE id = $1", "x")
}

// goodRuntimeConcat — the documented blind spot: the table name arrives as a runtime
// (non-const) value, so there is no constant projection_events string to fold. The scan
// (intentionally) cannot see it; the DB-engine REVOKE (migration 058) is the Hard guard
// that still covers this at runtime for the serving role.
func goodRuntimeConcat(ctx context.Context, tx fakeTx, table string) {
	_, _ = tx.Exec(ctx, "DELETE FROM "+table)
}
