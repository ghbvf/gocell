#!/usr/bin/env sh
# deploy/postgres/init/10-restricted-role.sh
#
# Runs ONCE on empty data dir, BEFORE migrations.
# Migrations run as ${POSTGRES_USER}=gocell (table owner).
# Every table created by migrations is auto-granted to gocell_app via
# ALTER DEFAULT PRIVILEGES — so gocell_app gets DML on all future tables
# without per-table grants.
#
# gocell_app owns nothing → non-owner → FORCE ROW LEVEL SECURITY applies.
# NOSUPERUSER + NOBYPASSRLS → tenant_isolation policies are enforced at runtime.
#
# gocell_audit_admin is the dedicated cross-tenant audit read pool role
# (#1810). It is NOSUPERUSER + NOBYPASSRLS — it never bypasses RLS; its
# cross-tenant visibility comes solely from the role-scoped PERMISSIVE policy
# audit_admin_read_all added by migration 064. SELECT on audit_entries is
# granted by that migration (inert where the role is absent).
#
# Interpolated passwords are ephemeral fixture values; avoid special chars.
set -e

: "${GOCELL_APP_PASSWORD:?GOCELL_APP_PASSWORD required for restricted serving role}"

# Defense-in-depth: passwords are interpolated into CREATE ROLE SQL literals
# AND into DSN userinfo. Restrict to the RFC 3986 "unreserved" set
# (A-Z a-z 0-9 - . _ ~) so a single quote / shell metachar / URL-breaking byte
# cannot break the SQL string or the DSN — fail fast at init rather than
# mid-statement. This accepts both gen-deploy-secrets.sh output
# (openssl rand -hex 16) and the hyphenated CI/e2e fixture values.
case "${GOCELL_APP_PASSWORD}" in
  *[!A-Za-z0-9._~-]*|"")
    echo "10-restricted-role: GOCELL_APP_PASSWORD must be non-empty URL-safe (RFC 3986 unreserved: A-Za-z0-9._~-)" >&2
    exit 1
    ;;
esac

psql \
  --username "$POSTGRES_USER" \
  --dbname   "$POSTGRES_DB"   \
  --set ON_ERROR_STOP=1       \
  <<SQL
CREATE ROLE gocell_app
  LOGIN
  PASSWORD '${GOCELL_APP_PASSWORD}'
  NOSUPERUSER
  NOBYPASSRLS;

GRANT USAGE ON SCHEMA public TO gocell_app;

ALTER DEFAULT PRIVILEGES FOR ROLE ${POSTGRES_USER}
  IN SCHEMA public
  GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO gocell_app;

ALTER DEFAULT PRIVILEGES FOR ROLE ${POSTGRES_USER}
  IN SCHEMA public
  GRANT USAGE, SELECT ON SEQUENCES TO gocell_app;
SQL

# gocell_audit_admin: optional dedicated read-only pool for cross-tenant audit
# access (#1810). NOSUPERUSER + NOBYPASSRLS — cross-tenant visibility is granted
# exclusively via the role-scoped permissive policy audit_admin_read_all
# (migration 064), NOT by bypassing RLS. The serving role gocell_app is
# unaffected: PERMISSIVE policies are OR-ed per role, and the policy names
# gocell_audit_admin explicitly.
#
# When GOCELL_AUDIT_ADMIN_PASSWORD is unset/empty this block is skipped
# entirely; migration 064 is a no-op where the role is absent. Absent role →
# GOCELL_AUDIT_ADMIN_DSN is not configured → super-admin cross-tenant audit
# read returns HTTP 501 (fail-closed).
if [ -z "${GOCELL_AUDIT_ADMIN_PASSWORD:-}" ]; then
  echo "10-restricted-role: GOCELL_AUDIT_ADMIN_PASSWORD unset — audit admin role not provisioned; super-admin cross-tenant audit read disabled"
else
  case "${GOCELL_AUDIT_ADMIN_PASSWORD}" in
    *[!A-Za-z0-9._~-]*|"")
      echo "10-restricted-role: GOCELL_AUDIT_ADMIN_PASSWORD must be URL-safe (RFC 3986 unreserved: A-Za-z0-9._~-)" >&2
      exit 1
      ;;
  esac

  psql \
    --username "$POSTGRES_USER" \
    --dbname   "$POSTGRES_DB"   \
    --set ON_ERROR_STOP=1       \
    <<SQL
CREATE ROLE gocell_audit_admin
  LOGIN
  PASSWORD '${GOCELL_AUDIT_ADMIN_PASSWORD}'
  NOSUPERUSER
  NOBYPASSRLS;

GRANT USAGE ON SCHEMA public TO gocell_audit_admin;
SQL
fi
