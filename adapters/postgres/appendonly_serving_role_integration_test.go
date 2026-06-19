//go:build integration

package postgres

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// servingRoleName / servingRolePassword identify the CLUSTER-GLOBAL gocell_app
// serving role shared by every *_appendonly test. The role is cluster-global, and
// CREATE ROLE ... IF NOT EXISTS does NOT update an existing role's password, so all
// these tests MUST agree on one password — provisionServingRole is the single
// source so they cannot drift (a mismatch fails SASL auth, SQLSTATE 28P01).
const (
	servingRoleName = "gocell_app"
	//nolint:gosec // G101 false positive: a test-only password for an ephemeral
	// test-scoped PG role in a throwaway testcontainer, not a real credential.
	servingRolePassword = "gocell_app_appendonly_testkey"
)

// provisionServingRole reproduces deploy/postgres/init/10-restricted-role.sh
// ordering: it idempotently creates the gocell_app serving role (NOSUPERUSER
// NOBYPASSRLS) and grants it the default DML on future tables in owner's database
// — BEFORE migrations run, so an append-only table is born with UPDATE/DELETE that
// the migration's REVOKE then removes. ALTER DEFAULT PRIVILEGES (no FOR ROLE)
// targets the current migrating role, matching deploy where the table owner grants
// to gocell_app. Shared by the projection_events and contract_registration_events
// append-only tests.
func provisionServingRole(t *testing.T, ctx context.Context, owner *Pool) {
	t.Helper()
	stmts := []string{
		`DO $$ BEGIN
		   IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = '` + servingRoleName + `') THEN
		     CREATE ROLE ` + servingRoleName + ` LOGIN PASSWORD '` + servingRolePassword + `' NOSUPERUSER NOBYPASSRLS;
		   END IF;
		 END $$;`,
		`GRANT USAGE ON SCHEMA public TO ` + servingRoleName,
		`ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO ` + servingRoleName,
	}
	for _, s := range stmts {
		_, err := owner.DB().Exec(ctx, s)
		require.NoErrorf(t, err, "provision serving role\nstmt: %s", s)
	}
}
