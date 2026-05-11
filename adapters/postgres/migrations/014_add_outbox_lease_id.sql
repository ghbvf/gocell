-- +goose NO TRANSACTION
-- +goose Up

-- Outbox claim fencing token (B2-A-01).
--
-- Before this migration, the relay's CAS chain (markPublished / markRetry /
-- markDead) only checked status='claiming'. A worker whose Claim succeeded but
-- whose publish was still in flight could complete after a parallel
-- ReclaimStale + new Claim cycle, and its mark would still match the new
-- worker's claiming row — overwriting the new lease's outcome and breaking the
-- L2 OutboxFact "exactly once published" guarantee.
--
-- Each ClaimPending now generates a fresh lease_id (UUID); the four mark/
-- reclaim queries pin their CAS WHERE/SET clauses to lease_id so a stale
-- worker's mark must miss.
--
-- ref: graphile/worker sql/000001.sql get_job/complete_job — locked_by CAS
-- ref: jackc/pgxjob pgxjob.go — worker_id UUID claim CTE

-- Fail-closed pre-flight: any row still in 'claiming' here would either be an
-- in-flight publish that loses fencing across the schema cut, or worker-crash
-- residue. Both require explicit operator action (drain the relay, or reset
-- the residue). The migration aborts with a row count so ops can decide.
-- +goose StatementBegin
DO $migration_014$
DECLARE
    residue_count bigint;
BEGIN
    SELECT count(*) INTO residue_count FROM outbox_entries WHERE status = 'claiming';
    IF residue_count > 0 THEN
        RAISE EXCEPTION
            'outbox migration 014: % rows still in claiming state; drain the relay (or manually reset crash residue) before applying',
            residue_count;
    END IF;
END
$migration_014$;
-- +goose StatementEnd

ALTER TABLE outbox_entries ADD COLUMN IF NOT EXISTS lease_id UUID;

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_outbox_claiming_lease ON outbox_entries (lease_id) WHERE status = 'claiming';

-- +goose Down
-- Fail-closed: refuse destructive rollback unless gocell.allow_destructive_down is set.
-- This migration uses NO TRANSACTION; each statement runs in its own implicit
-- transaction. The GUC is session-scope so it persists across all statements.
-- +goose StatementBegin
DO $$
BEGIN
    IF current_setting('gocell.allow_destructive_down', true) IS DISTINCT FROM 'true' THEN
        RAISE EXCEPTION 'destructive down blocked: GUC gocell.allow_destructive_down not set';
    END IF;
END $$;
-- +goose StatementEnd

DROP INDEX IF EXISTS idx_outbox_claiming_lease;
ALTER TABLE outbox_entries DROP COLUMN IF EXISTS lease_id;
