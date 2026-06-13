//go:build archtest_fixture

// Package projectionnodeletefixture is the RED/GREEN fixture for
// PROJECTION-EVENT-JOURNAL-NO-DELETE-01 (EPIC #1504 PR-05 / ADR 202606071600-1504
// §6 I4). It is the negative control proving the SQL-literal scanner actually
// fires on a DELETE/TRUNCATE statement targeting projection_events — without it,
// the "expect zero matches in production" rule could pass green forever on a
// broken regex.
//
// RED (must be flagged — exactly three):
//   - badDelete: DELETE FROM projection_events …
//   - badTruncate: TRUNCATE TABLE projection_events
//   - badTruncateSchemaQualified: TRUNCATE public.projection_events
//
// GREEN (must NOT be flagged):
//   - goodInsert / goodSelect: the sanctioned append + replay-read literals
//     (they reference the table but are not DELETE/TRUNCATE).
//   - goodSiblingTableDelete: DELETE FROM projection_events_archive — a different
//     table; proves the \bprojection_events\b word boundary does not over-match a
//     prefix collision.
package projectionnodeletefixture

import "context"

// fakeTx mimics the pgx.Tx.Exec shape (ctx, sql, args...) so the fixture reads like
// real adapter code, but the scanner is a pure string-literal walk and fires on the
// SQL literal regardless of the surrounding call — no real pgx dependency needed.
type fakeTx struct{}

func (fakeTx) Exec(ctx context.Context, sql string, args ...any) (any, error) { return nil, nil }

// badDelete is the RED case: a DELETE against the durable projection journal —
// exactly the append-only violation the invariant forbids.
func badDelete(ctx context.Context, tx fakeTx) {
	_, _ = tx.Exec(ctx, "DELETE FROM projection_events WHERE global_seq < $1", 0)
}

// badTruncate is the RED case: TRUNCATE wipes the journal wholesale.
func badTruncate(ctx context.Context, tx fakeTx) {
	_, _ = tx.Exec(ctx, "TRUNCATE TABLE projection_events")
}

// badTruncateSchemaQualified is the RED case for the schema-qualified form, which the
// (?:\w+\.)? branch of the pattern must also catch.
func badTruncateSchemaQualified(ctx context.Context, tx fakeTx) {
	_, _ = tx.Exec(ctx, "TRUNCATE public.projection_events")
}

// goodInsert is the GREEN control: the sanctioned append. It references the table but
// is not a DELETE/TRUNCATE, so it must NOT be flagged.
func goodInsert(ctx context.Context, tx fakeTx) {
	_, _ = tx.Exec(ctx, "INSERT INTO projection_events (id, global_seq, topic) VALUES ($1, $2, $3) ON CONFLICT (id) DO NOTHING")
}

// goodSelect is the GREEN control: a replay read. Not a DELETE/TRUNCATE.
func goodSelect(ctx context.Context, tx fakeTx) {
	_, _ = tx.Exec(ctx, "SELECT global_seq, id, topic FROM projection_events WHERE global_seq > $1 ORDER BY global_seq")
}

// goodSiblingTableDelete is the GREEN control for word-boundary precision: a DELETE on
// a DIFFERENT table whose name has projection_events as a prefix. \bprojection_events\b
// must NOT match it (the trailing \b sits between two word chars: …events_archive).
func goodSiblingTableDelete(ctx context.Context, tx fakeTx) {
	_, _ = tx.Exec(ctx, "DELETE FROM projection_events_archive WHERE id = $1", "x")
}
