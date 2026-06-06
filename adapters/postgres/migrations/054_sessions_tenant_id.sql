-- Migration 054: add sessions.tenant_id carrier column + composite same-tenant FK
-- (EPIC #1337 PR-3b, issue #1617).
--
-- WHY a carrier column (and why sessions is NOT under RLS):
--   Migration 053 placed users/roles/role_assignments under FORCE RLS. That
--   creates a bootstrap problem on the pre-auth refresh/validate paths: they
--   hold only an opaque session id (refresh token / JWT sid) and must LEARN the
--   tenant before they can set the RLS scope to read the user row. They cannot
--   read users tenant-less (RLS fail-closes to 0 rows), and they have no JWT
--   tenant claim yet (refresh) / are mid-establishing it (validate). So sessions
--   carries tenant_id and is read by its unguessable PK with NO RLS — it is the
--   trusted pre-auth carrier. sessionlogin stamps tenant_id at Create; refresh
--   and validate derive their scope from the session row (sessions.tenant_id),
--   not the user row, breaking the GetByID-on-users circular dependency.
--
-- WHY the carrier is trustworthy (AI-HARD, not cosmetic):
--   sessions is not under RLS, so it has no WITH CHECK write-side guard. The
--   composite FK below is the DB-level Hard guarantee instead: it forces
--   (tenant_id, subject_id) to reference a REAL user in that SAME tenant. If
--   sessionlogin ever wrote a session row whose tenant_id disagrees with the
--   authenticated user's tenant, this FK rejects the INSERT. refresh/validate can
--   therefore trust session.TenantID as the scope source.
--
-- WHY ALTER (not a drop+recreate rebuild):
--   sessions is provably empty at deploy time (project invariant — no production
--   instances exist outside CI; same invariant migration 026 relied on). Adding a
--   column + swapping the FK is non-destructive: it preserves the table, every
--   index (idx_sessions_jti / _subject_active / _expires), and the
--   sessions_authz_epoch_at_issue_positive CHECK from migrations 018/028. A
--   drop+recreate would have to faithfully re-derive all of that, so ALTER is the
--   simpler, lower-risk realization of the same goal. No `+gocell forward-rebuild`
--   annotation and no Go destructive permit are needed (no populated-table DROP).
--   lock_timeout is auto-injected by the migrator session locker (README rule 4).
--
-- DDL form: ADD COLUMN ... NOT NULL DEFAULT '' then DROP DEFAULT — the empty-string
-- default is a DDL-compatibility device only (rules/go-standards.md "新字段必须有
-- 默认值或允许 NULL"; same idiom as migration 026 ADD COLUMN ... DEFAULT 0). The
-- default is dropped immediately so every session row must carry an explicit,
-- real tenant_id; the composite FK rejects the placeholder '' (no user has
-- tenant_id = '').
--
-- ref: adapters/postgres/migrations/050_accesscore_tenant_id.sql (users(tenant_id,id)
--      UNIQUE support index idx_users_tenant_id_id referenced by the composite FK,
--      and the role_assignments composite-FK same-tenant pattern this mirrors)
-- ref: adapters/postgres/migrations/053_accesscore_tenant_rls_force.sql (RLS on users)

-- +goose Up

-- 1. Add the carrier column. DEFAULT '' is a DDL-compat device for ADD COLUMN
--    NOT NULL on a (provably empty) table; dropped immediately below.
ALTER TABLE sessions ADD COLUMN tenant_id TEXT NOT NULL DEFAULT '';
ALTER TABLE sessions ALTER COLUMN tenant_id DROP DEFAULT;

-- 2. Swap the single-column FK for a composite same-tenant FK. The old
--    sessions_subject_id_fkey (subject_id) -> users(id) cannot express the
--    tenant invariant; the composite (tenant_id, subject_id) -> users(tenant_id, id)
--    enforces that a session always references a user in its OWN tenant.
--    References the UNIQUE(tenant_id, id) support index (idx_users_tenant_id_id,
--    migration 050). ON DELETE CASCADE preserved: deleting a user removes their
--    sessions (DB-level safety net; the runtime Store also RevokeForSubject).
ALTER TABLE sessions DROP CONSTRAINT sessions_subject_id_fkey;
ALTER TABLE sessions
    ADD CONSTRAINT sessions_subject_id_fkey
        FOREIGN KEY (tenant_id, subject_id) REFERENCES users(tenant_id, id) ON DELETE CASCADE;

-- +goose Down
-- Reversible: restore the single-column FK and drop the carrier column. Destructive
-- only in that it drops a column (no row rewrite beyond that). Destructive-down gate
-- is enforced in Go (Migrator.Down + DestructiveDownPermit); see issue #1248.

ALTER TABLE sessions DROP CONSTRAINT sessions_subject_id_fkey;
ALTER TABLE sessions
    ADD CONSTRAINT sessions_subject_id_fkey
        FOREIGN KEY (subject_id) REFERENCES users(id) ON DELETE CASCADE;

ALTER TABLE sessions DROP COLUMN tenant_id;
