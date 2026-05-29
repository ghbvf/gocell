# Migration 044 Runbook — outbox_entries principal rebuild

`044_outbox_entries_principal.sql` is a **destructive forward** migration: it
`TRUNCATE`s `outbox_entries` and adds two `NOT NULL` columns (`principal jsonb`,
`occurred_at timestamptz`) for the sealed-construction principal-injection wire
envelope (#1229). `NOT NULL` without a default cannot be back-filled on existing
rows in PostgreSQL, and GoCell ships only itself (no rolling-deploy
compatibility — see CLAUDE.md "不考虑向后兼容"), so a one-shot
TRUNCATE + schema upgrade is the correct pattern (mirrors `043_audit_entries_v2`).

The SQL header carries the canonical step list; this runbook is its discoverable
ops home and adds the two caveats that the SQL comment does not spell out: the
GUC guard is **not** a privilege boundary, and the relay drain has a **broker
side** beyond the DB-row count.

## Operator steps

1. **Stop all producer traffic.** No new rows may enter `outbox_entries` from
   here until the migration completes.

2. **Drain the relay (DB side).** The drain is complete only when there are
   **zero** undelivered rows:

   ```sql
   SELECT count(*) FROM outbox_entries WHERE status <> 'published';  -- must be 0
   ```

   Do **not** use `CountPending` / `outbox_pending_depth` as the drain signal:
   that query counts only `status='pending'` rows whose backoff has elapsed
   (`next_retry_at <= now()`), so it excludes rows still in retry backoff and
   rows in `claiming`. A green `CountPending == 0` can hide undelivered rows that
   the `TRUNCATE` would destroy.

3. **Drain in-flight messages (broker side).** A row flips to `published` the
   moment the relay hands it to the AMQP broker — but the consumer may still be
   processing that delivery, and unacked/in-flight deliveries are not visible in
   the step-2 count. Stop the relay and consumers gracefully so their in-flight
   deliveries settle before the rebuild:

   - The RabbitMQ subscriber drains via `StopIntake` → `drainRemaining`
     (`adapters/rabbitmq/subscriber.go`): it stops pulling new deliveries and
     waits up to `defaultRMQDrainDeadline` (30s wall-clock) for in-flight ones to
     Ack/Requeue/Reject.
   - On Kubernetes this is the normal graceful-shutdown path; size
     `terminationGracePeriodSeconds` per `docs/ops/graceful-shutdown-k8s.md` so
     SIGKILL does not truncate the drain.
   - Requeued / unacked deliveries return to the broker queue, **not** to
     `outbox_entries`, so they survive the TRUNCATE independently. Confirm the
     broker queues are empty (or accept redelivery after the rebuild) before
     proceeding.

4. **Confirm all relay instances are stopped** so no row re-enters `claiming`
   between the check and `goose up`.

5. **Run `goose up`.** The Up block fails closed if any non-`published` row
   remains (see GUC note below).

## GUC guard — fail-closed, **not** a privilege boundary

The Up block raises an exception if undelivered rows exist, **unless** the
operator sets a dedicated forward-rebuild GUC:

```sql
SET gocell.allow_outbox_rebuild = 'true';  -- accept loss of undelivered rows
```

This GUC is decoupled from `gocell.allow_destructive_down` (which gates the Down
section) so that "I am rolling back" and "I am rebuilding forward" never share
one switch — same decoupling `043` established for `audit_entries`.

> **Caveat (review F11): the GUC is an honor-system guard, not an authorization
> control.** `current_setting('gocell.allow_outbox_rebuild', true)` reads a
> custom GUC that **any** role running the migration can set in its own session
> (and a superuser trivially so). It exists to force a deliberate, auditable
> "yes, I accept data loss" action — it does **not** stop a privileged operator
> from destroying undelivered rows. The real safety boundary is the drain
> (steps 2–3) plus who is permitted to run migrations at all. Treat setting this
> GUC as an incident-grade decision: record who set it and why, because the
> fail-closed exception is the only thing standing between a non-drained outbox
> and silent event loss.

## Down (rollback)

The Down block drops both columns, permanently deleting all principal identity
and `occurred_at` data. It is gated by `gocell.allow_destructive_down` (shared
with `001`/`003`/etc.). **Back up `outbox_entries` before rolling back in
production** — the same honor-system caveat applies to this GUC.

## Cross-references

- SQL: `adapters/postgres/migrations/044_outbox_entries_principal.sql`
- Schema guard inventory: `adapters/postgres/schema_guard.go` (`outbox_entries (001/044)`)
- Graceful shutdown / drain budget: `docs/ops/graceful-shutdown-k8s.md`
- ADR: `docs/architecture/202605281200-1042-outbox-wire-envelope-principal-occurred-at.md`
- Sibling rebuild: `043_audit_entries_v2.sql`
