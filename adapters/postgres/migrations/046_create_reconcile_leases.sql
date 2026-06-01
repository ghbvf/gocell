-- Migration 046: create reconcile_leases for the kernel/reconcile LeaderElector
-- PG backend (epic #661, PR-A6). One row per reconciler identity records the
-- current leader holder, the monotonic fencing epoch, and the lease window.
--
-- Design decisions:
--   - reconciler_id is the PRIMARY KEY: leader election is per reconciler identity
--     (one Loop term holds one lease). It mirrors the redis holder-key namespace.
--   - holder_id TEXT NOT NULL identifies the replica that holds the lease. The
--     PGReconcileElector writes its per-instance holderID; renew/release are
--     guarded by `holder_id = <self>` so a stale holder cannot extend or clear a
--     lease another replica has taken over (the row-level correctness CAS).
--   - epoch BIGINT NOT NULL DEFAULT 0 is the monotonic fencing token. The acquire
--     UPSERT increments it on every holder CHANGE (takeover) and keeps it on an
--     idempotent same-holder, still-live re-acquire — the CASE in the elector's
--     ON CONFLICT clause. It feeds reconcile.LeaseToken.Epoch → the FencedWriter's
--     write-path CAS, so a zombie leader's lower-epoch late write is rejected.
--     DEFAULT 0 makes the INSERT well-defined; the first acquire writes 1.
--   - acquired_at / expires_at TIMESTAMPTZ are the lease window. The elector also
--     holds a SESSION-scoped pg_try_advisory_lock(hashtextextended(reconciler_id))
--     for the lease lifetime, so a crashed leader's lock auto-releases when its
--     session drops (instant failover); expires_at is the secondary TTL signal.
--
-- No CONCURRENTLY: new table, no concurrent reads at migration time.
--
-- ref: kubernetes/client-go tools/leaderelection (lease/renew model)
-- ref: docs/architecture/202605291600-661-adr-kernel-reconcile-design.md §4 (leader-elect + epoch fencing)

-- +goose Up
CREATE TABLE IF NOT EXISTS reconcile_leases (
    reconciler_id TEXT        NOT NULL,
    holder_id     TEXT        NOT NULL,
    epoch         BIGINT      NOT NULL DEFAULT 0,
    acquired_at   TIMESTAMPTZ NOT NULL,
    expires_at    TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (reconciler_id)
);

-- +goose Down
-- WARNING: dropping reconcile_leases PERMANENTLY DELETES all leader-election state
-- (holder + monotonic epoch). After a rollback every reconciler restarts epoch
-- numbering from 1 — acceptable only because no leader is running during a
-- rollback. Destructive-down gate is enforced in Go (Migrator.Down +
-- DestructiveDownPermit); see issue #1248.
DROP TABLE IF EXISTS reconcile_leases;
