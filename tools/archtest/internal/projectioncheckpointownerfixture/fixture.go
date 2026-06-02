//go:build archtest_fixture

// Package projectioncheckpointownerfixture is a synthetic violation fixture for
// PROJECTION-CHECKPOINT-OWNER-COLUMN-V1-RESERVED-01. It contains an
// INSERT-into-projection_checkpoints SQL string literal that ILLEGALLY writes
// the reserved owner column, which the rule's detector must flag. The fixture
// is loaded only via Run(t, Fixture(...)) (archtest_fixture build tag), so
// it never enters a normal build or the production scan scoped to adapters/postgres.
//
// DO NOT use this package in production code.
package projectioncheckpointownerfixture

// badUpsertSQL ILLEGALLY references the reserved owner column in a
// projection_checkpoints INSERT write path. v1 must never WRITE owner (ADR §Q5;
// reads are harmless). PROJECTION-CHECKPOINT-OWNER-COLUMN-V1-RESERVED-01 must fire here.
const badUpsertSQL = `INSERT INTO projection_checkpoints (cell_id, projection_id, offset_seq, owner)
VALUES ($1, $2, $3, $4)
ON CONFLICT (cell_id, projection_id)
DO UPDATE SET offset_seq = EXCLUDED.offset_seq, owner = EXCLUDED.owner`

// readSQL is a benign SELECT against the same table. It is NOT a write path, so
// the rule must NOT flag it even though it mentions the table — it anchors the
// detector's INSERT/UPDATE-only scope (read of owner is the documented B2 blind
// spot, intentionally out of scope).
const readSQL = `SELECT offset_seq, owner FROM projection_checkpoints WHERE cell_id = $1`
