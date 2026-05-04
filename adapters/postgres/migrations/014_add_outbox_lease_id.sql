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

-- Reset any in-flight claims. Deploys are expected to drain the relay before
-- migration; this UPDATE only catches the worker-crash residue case.
UPDATE outbox_entries SET status = 'pending', claimed_at = NULL WHERE status = 'claiming';

-- Nullable column: NULL for unclaimed (pending) rows; set on Claim; cleared on
-- Reclaim back-to-pending. Matches graphile worker locked_by NULL semantics.
ALTER TABLE outbox_entries ADD COLUMN lease_id UUID;

-- Partial index: only claiming rows participate in CAS lookups.
CREATE INDEX idx_outbox_claiming_lease ON outbox_entries (lease_id) WHERE status = 'claiming';

-- +goose Down

DROP INDEX IF EXISTS idx_outbox_claiming_lease;
ALTER TABLE outbox_entries DROP COLUMN IF EXISTS lease_id;
