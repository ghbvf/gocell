# Migration 050 Runbook — accesscore tenant rebuild (users / roles / role_assignments)

`050_accesscore_tenant_id.sql` is a **destructive forward** migration: it `DROP TABLE … CASCADE`s
and recreates `users`, `roles`, and `role_assignments` with `tenant_id` for Model-A
multi-tenancy isolation (EPIC #1337 PR-2a). This is the accesscore equivalent of
`043_audit_entries_v2` — a one-shot canonical rebuild that trades backward
compatibility for schema correctness (see CLAUDE.md "不考虑向后兼容").

The SQL header carries the canonical step list; this runbook is its discoverable
ops home and adds the caveats the SQL comment does not spell out.

## What migration 050 does

1. **Drops** (in reverse dependency order):
   - The `effective_admin_invariant_fn` trigger function and both triggers.
   - `role_assignments` (FK to both users and roles).
   - `roles`.
   - `users CASCADE` — the CASCADE also drops `sessions_subject_id_fkey` from
     the `sessions` table, leaving `sessions` itself intact.

2. **Recreates** the three tables with `tenant_id TEXT NOT NULL`:
   - `users`: PK stays `(id UUID)`; composite `UNIQUE(tenant_id, username)` and
     `UNIQUE(tenant_id, email)` replace the old global unique indexes; a new
     `UNIQUE(tenant_id, id)` support index enables the cross-tenant FK on
     `role_assignments`.
   - `roles`: PK becomes composite `(tenant_id, id)` — roles are per-tenant.
   - `role_assignments`: PK `(tenant_id, user_id, role_id)`; two composite FKs
     enforce same-tenant user membership and role existence at the DB layer.

3. **Recreates** the `effective_admin_invariant_fn` trigger function and both
   triggers, updated so the advisory lock key and `COUNT` query are scoped per
   tenant.

4. **Restores** `sessions_subject_id_fkey` via
   `ALTER TABLE sessions ADD CONSTRAINT … FOREIGN KEY (subject_id) REFERENCES users(id) ON DELETE CASCADE`.
   This FK was dropped by the `DROP TABLE users CASCADE` in step 1; it must be
   re-added after `users` is recreated. The sessions table itself is **not**
   modified (deferred to PR-3).

## Data consequences

- **All `users`, `roles`, and `role_assignments` data is permanently destroyed.**
  The DROP TABLE removes every row before the new schema is created. There is no
  in-place migration.
- **Sessions must be drained before applying migration 050.** The final step of
  the Up block re-adds `sessions_subject_id_fkey` via `ALTER TABLE sessions ADD
  CONSTRAINT`. PostgreSQL validates all existing rows when adding a FK — if any
  session row's `subject_id` references a user UUID that was just deleted by the
  DROP, the constraint addition fails with `23503 foreign_key_violation` and the
  migration aborts. Ensure all sessions have expired or been explicitly deleted
  before running this migration.
- GoCell is pre-v1.0 with no external consumers. In development: drop the DB and
  re-run from migration 001. In CI: containers start fresh per test run.

## Gate mechanism — `ForwardRebuildPermit` typed channel

Since issue #1248, the forward-rebuild gate is enforced in Go by
`Migrator.ForwardRebuild` + `ForwardRebuildPermit` (not by a SQL GUC). The
gate is **data-aware** per target table. Migration 050 registers three targets:
`users`, `roles`, `role_assignments`.

| State | Behavior |
|---|---|
| Target table does not exist | Allowed automatically (`to_regclass` probe → NULL → safe) |
| Target table exists and is empty | Allowed automatically (no data to lose) |
| Target table exists and holds **any row** | **Refused** unless an explicit `ForwardRebuildPermit` is supplied |

One permit covers all three targets: `AllowForwardRebuild(50, reason)` authorizes
the rebuild of whichever of the three tables is non-empty. If only `users` has
rows (the common case for early dev environments), a single permit for migration 50
is sufficient.

## Operator apply steps

1. **Stop all producer traffic** that writes to `users`, `roles`, or `role_assignments`.
   The new schema is incompatible with the old binary (composite keys, new columns);
   a mixed-version window is not safe.

2. **Drain any in-flight requests** that read `users` / `roles` / `role_assignments`
   (e.g., active sessions validating against the users table). A graceful shutdown
   of the old binary is sufficient; see `docs/ops/graceful-shutdown-k8s.md`.

3. **Drain all sessions.** Migration 050's Up block ends with an `ALTER TABLE sessions
   ADD CONSTRAINT sessions_subject_id_fkey` that validates all existing sessions rows.
   Any session row whose `subject_id` references a user UUID that was just deleted by
   the DROP (whether or not the session is logically expired) will cause the FK
   addition to fail (`23503 foreign_key_violation`), aborting the migration.
   **Sessions must be physically deleted** before running this migration — logical
   expiry (`expires_at < now()`) does not remove the row from the table and does not
   satisfy the FK check. Delete all sessions and confirm the table is empty:

   ```sql
   -- Delete expired sessions first, then verify the table is empty.
   DELETE FROM sessions WHERE expires_at < now();
   -- If any active (non-expired) sessions remain, drain them or forcibly delete:
   DELETE FROM sessions;
   -- Confirm the table is empty before proceeding:
   SELECT count(*) FROM sessions;  -- must be 0
   ```

4. **Run the migration with an explicit rebuild permit.** Use the
   `tools/pg-migrate` CLI with the `-rebuild` flag:

   ```bash
   pg-migrate -dsn "$GOCELL_PG_DSN" -rebuild "50:<reason>"
   ```

   Replace `<reason>` with a concise incident-grade description of why data loss
   is accepted (e.g. `"tenant-rebuild-2026-06-04-dev-reset"`).

   Alternatively, call the Go API directly:

   ```go
   import adapterpg "github.com/ghbvf/gocell/adapters/postgres"

   permit, err := adapterpg.AllowForwardRebuild(50, reason)
   if err != nil { /* handle */ }
   if err := migrator.ForwardRebuild(ctx, permit); err != nil { /* handle */ }
   ```

   If any of the three target tables has rows and no permit is supplied (plain `Up()`),
   the migration fails closed with:

   ```
   postgres: forward-rebuild refused: target table is non-empty and no permit was supplied
   ```

   If all three tables are empty or do not yet exist, `Up()` proceeds without a
   permit (fresh provision path).

5. **Deploy the new tenant-aware binary** once the migration completes. The old
   binary cannot write the new composite-keyed schema; step 1 ensures it is
   already stopped.

6. **Restore traffic.**

## Down (rollback)

The Down block drops `users CASCADE` (removing `sessions_subject_id_fkey` again),
`roles`, and `role_assignments` — permanently deleting all post-050 data.

Use `Migrator.Down` with an explicit `DestructiveDownPermit`:

```go
permit, err := postgres.AllowDestructiveDown("reason for rollback")
if err != nil { /* handle */ }
if err := migrator.Down(ctx, permit); err != nil { /* handle */ }
```

Rolling back from 050 to 049 is a dev-only operation (no production data expected
pre-v1.0). In production a rollback requires a full backup restore; do **not** rely
on `goose down` in a live production environment.

## Cross-references

- SQL: `adapters/postgres/migrations/050_accesscore_tenant_id.sql`
- Go gate implementation: `adapters/postgres/migrator.go` (`ForwardRebuildPermit`,
  `AllowForwardRebuild`, `Migrator.ForwardRebuild`, `gatePendingRebuilds`,
  `tableHasRows`)
- CLI tool: `tools/pg-migrate/main.go` (`-rebuild` flag)
- Schema guard inventory: `adapters/postgres/schema_guard.go` (users/roles/role_assignments sections)
- Integration tests: `adapters/postgres/integration_test.go`
  (`TestMigrator_ForwardRebuild_Migration050_*`)
- Schema guard cycle test: `adapters/postgres/schema_guard_integration_test.go`
  (`TestMigration050_UpDownUpIdempotency`, `TestMigration050_DestructiveDownPermitRejection`)
- ADR (permit typed channel): `docs/architecture/202605310200-1122-adr-migrator-permit-typed-channel.md`
- Sibling rebuilds: `043_audit_entries_v2.sql`, `044_outbox_entries_principal.sql`
- Runbook (sibling): `docs/ops/migration-044-outbox-rebuild.md`
- Graceful shutdown / drain budget: `docs/ops/graceful-shutdown-k8s.md`
