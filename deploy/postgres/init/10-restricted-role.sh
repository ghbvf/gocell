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
# Interpolated password is an ephemeral fixture value; avoid special chars.
set -e

: "${GOCELL_APP_PASSWORD:?GOCELL_APP_PASSWORD required for restricted serving role}"

# Defense-in-depth: the password is interpolated into the CREATE ROLE SQL literal
# below AND into the serving DSN userinfo. Restrict it to the RFC 3986 "unreserved"
# set (A-Z a-z 0-9 - . _ ~) so a single quote / shell metachar / URL-breaking byte
# (' / + = space etc.) cannot break the SQL string or the DSN — fail fast at init
# rather than mid-statement. This accepts both gen-deploy-secrets.sh output
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
