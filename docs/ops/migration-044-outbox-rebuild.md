# Migration 044 Runbook — outbox_entries principal rebuild

`044_outbox_entries_principal.sql` is a **destructive forward** migration: it
`TRUNCATE`s `outbox_entries` and adds two `NOT NULL` columns (`principal jsonb`,
`occurred_at timestamptz`) for the sealed-construction principal-injection wire
envelope (#1229). PostgreSQL itself *can* add a nullable column, or one with a
default, and `SET NOT NULL` after a back-fill — the blocker is semantic, not
engine: a single-step `ADD COLUMN ... NOT NULL` (no default) cannot apply to
non-empty rows, and there is no trustworthy business source for `principal` /
`occurred_at` on rows written before the envelope carried them (a synthetic
default would be fabricated audit identity / event time). GoCell ships only
itself (no rolling-deploy compatibility — see CLAUDE.md "不考虑向后兼容"), so a
one-shot TRUNCATE + schema upgrade is the correct pattern (mirrors
`043_audit_entries_v2`).

The SQL header carries the canonical step list; this runbook is its discoverable
ops home and adds the caveats that the SQL comment does not spell out: the relay
drain has a **broker side** beyond the DB-row count, and the **data-aware gate**
behaves differently on a fresh database vs an existing one.

## Gate mechanism — `ForwardRebuildPermit` typed channel

Since issue #1248, the forward-rebuild gate is enforced in Go by
`Migrator.ForwardRebuild` + `ForwardRebuildPermit` (not by a SQL GUC). The
gate is **data-aware**:

- If `outbox_entries` does not exist (e.g. fresh provision — table not yet
  created by earlier migrations) → **allowed automatically** (`to_regclass`
  probe returns NULL → safe).
- If `outbox_entries` exists but is empty → **allowed automatically** (no data
  to lose).
- If `outbox_entries` exists and holds **any row** (including rows with
  `status='published'` awaiting retention cleanup) → **refused unless** an
  explicit `ForwardRebuildPermit` is supplied.

Note: the gate checks for **any row**, not just undelivered rows. This is
intentionally more conservative than the previous SQL guard (which only checked
`status <> 'published'`). In practice, published-but-not-yet-cleaned rows are
safe to discard; the coarser Go gate treats them uniformly.

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

   - The RabbitMQ subscriber's `StopIntake` phase
     (`adapters/rabbitmq/subscriber.go`) stops pulling new deliveries and waits
     up to `SubscriberConfig.StopIntakeDrainTimeout` (default 30s wall-clock)
     for in-flight ones to Ack/Requeue/Reject. Tune that field if your handlers
     need longer to settle.
   - On Kubernetes this is the normal graceful-shutdown path; size
     `terminationGracePeriodSeconds` per `docs/ops/graceful-shutdown-k8s.md` so
     SIGKILL does not truncate the drain.
   - Requeued / unacked deliveries return to the broker queue, **not** to
     `outbox_entries`, so they survive the TRUNCATE independently — which is
     exactly why the broker queues **MUST be fully drained before cutover, not
     after.** A message published before this migration carries the old wire
     envelope with no `occurred_at`; once the new code is live, redelivering it
     fails `UnmarshalEnvelope` → `Entry.Validate` (missing occurredAt) and the
     message is rejected to the DLX — lost, not silently accepted. Confirm the
     broker queues are empty before proceeding; **do not** rely on post-rebuild
     redelivery.

4. **Confirm all relay instances are stopped** so no row re-enters `claiming`
   between the check and the migration run.

5. **Run the migration with an explicit rebuild permit.** Use the
   `tools/pg-migrate` CLI with the `-rebuild` flag:

   ```bash
   pg-migrate -dsn "$GOCELL_PG_DSN" -rebuild "44:<reason>"
   ```

   Replace `<reason>` with a concise incident-grade description of why data loss
   is accepted (e.g. `"drain-verified-zero-undelivered-2026-05-31"`).

   The reason is logged and kept in the typed permit struct for audit
   traceability — treat supplying it as an incident-grade decision: record who
   ran it and why.

   Alternatively, call the Go API directly:

   ```go
   import adapterpg "github.com/ghbvf/gocell/adapters/postgres"

   permit, err := adapterpg.AllowForwardRebuild(44, reason)
   if err != nil { /* handle */ }
   if err := migrator.ForwardRebuild(ctx, permit); err != nil { /* handle */ }
   ```

   If `outbox_entries` has any rows and no permit is supplied (plain `Up()`),
   the migration fails closed with:

   ```
   postgres: forward-rebuild refused: target table is non-empty and no permit was supplied
   ```

   If the table is empty or does not yet exist, `Up()` proceeds without needing
   a permit (fresh provision path).

## Down (rollback)

The Down block drops both columns, permanently deleting all principal identity
and `occurred_at` data. Use `Migrator.Down` with an explicit `DestructiveDownPermit`:

```go
permit, err := postgres.AllowDestructiveDown("reason for rollback")
if err != nil { /* handle */ }
if err := migrator.Down(ctx, permit); err != nil { /* handle */ }
```

**Back up `outbox_entries` before rolling back in production.** Rolling back
drops the columns and all their data irreversibly.

## Cross-references

- SQL: `adapters/postgres/migrations/044_outbox_entries_principal.sql`
- Go gate implementation: `adapters/postgres/migrator.go` (`ForwardRebuildPermit`,
  `AllowForwardRebuild`, `Migrator.ForwardRebuild`, `gatePendingRebuilds`,
  `tableHasRows`)
- CLI tool: `tools/pg-migrate/main.go` (`-rebuild` flag)
- Schema guard inventory: `adapters/postgres/schema_guard.go` (`outbox_entries (001/044)`)
- Graceful shutdown / drain budget: `docs/ops/graceful-shutdown-k8s.md`
- ADR (principal envelope): `docs/architecture/202605281200-1042-outbox-wire-envelope-principal-occurred-at.md`
- ADR (permit typed channel): `docs/architecture/202605310200-1122-adr-migrator-permit-typed-channel.md`
- Sibling rebuild: `043_audit_entries_v2.sql`
