//go:build integration

package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	adapterpg "github.com/ghbvf/gocell/adapters/postgres"
	"github.com/ghbvf/gocell/corecells/accesscore/internal/abac"
	"github.com/ghbvf/gocell/kernel/cell/celltest"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/pkg/authz"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/tenant"
)

// policyCreatedAt reads the policies.created_at column directly (bypassing the
// repo) so tests can assert the upsert preserves it.
func policyCreatedAt(t *testing.T, pool *adapterpg.Pool, tid tenant.TenantID, id string) time.Time {
	t.Helper()
	var createdAt time.Time
	require.NoError(t, pool.DB().QueryRow(context.Background(),
		`SELECT created_at FROM policies WHERE tenant_id = $1 AND id = $2`, string(tid), id).Scan(&createdAt))
	return createdAt
}

// setupPolicyRepoPG clones the package-shared pre-migrated template database into
// a fresh per-test DB and returns a PGPolicyRepo + Pool (for tests that need
// direct SQL access). Pool + per-test DB lifecycle is owned by t.Cleanup inside
// sharedPG.NewPerTestPool (see testmain_integration_test.go).
func setupPolicyRepoPG(t *testing.T) (*PGPolicyRepo, *adapterpg.Pool) {
	t.Helper()
	pool := sharedPG.NewPerTestPool(t)
	txMgr := adapterpg.NewTxManager(pool)
	repo, err := NewPGPolicyRepo(pool.DB(), txMgr, clock.Real())
	require.NoError(t, err)
	return repo, pool
}

// setupBrokenPolicyRepoPG clones a fresh per-test DB, DROPs the policies table to
// simulate schema drift / missing migration, and returns a PGPolicyRepo backed by
// that broken database — the "broken" prober for RunRepoReadinessConformance.
// Per-test DB isolation (TEMPLATE clone) keeps the DROP from leaking across tests.
func setupBrokenPolicyRepoPG(t *testing.T) *PGPolicyRepo {
	t.Helper()
	pool := sharedPG.NewPerTestPool(t)
	_, dropErr := pool.DB().Exec(context.Background(), "DROP TABLE policies CASCADE")
	require.NoError(t, dropErr, "dropping policies to create broken repo")
	txMgr := adapterpg.NewTxManager(pool)
	repo, err := NewPGPolicyRepo(pool.DB(), txMgr, clock.Real())
	require.NoError(t, err)
	return repo
}

// TestPGPolicyRepo_Integration_RepoReadiness enrolls PGPolicyRepo in the
// healthz.RepoProber readiness conformance (CELL-REPO-READYZ-PROBE-01). healthy:
// full migrations applied → probe returns nil. broken: policies dropped → the
// probe must return the differentiated error a pool-level ping cannot detect.
func TestPGPolicyRepo_Integration_RepoReadiness(t *testing.T) {
	healthy, _ := setupPolicyRepoPG(t)
	broken := setupBrokenPolicyRepoPG(t)
	celltest.RunRepoReadinessConformance(t, "accesscore-policy-pg", healthy, broken)
}

// newIntegrationTenant returns a fresh canonical-UUID tenant.
func newIntegrationTenant(t *testing.T) tenant.TenantID {
	t.Helper()
	tid := tenant.TenantID(uuid.NewString())
	require.NoError(t, tid.Validate())
	return tid
}

// richPolicy exercises every nested branch of the codec: multiple rules,
// multi-condition AND with set operators, and obligations carrying both a
// RowScope and a non-empty FieldMask.
func richPolicy(id string, tid tenant.TenantID) *abac.Policy {
	return &abac.Policy{
		ID:          id,
		TenantID:    tid,
		Name:        "Rich Policy",
		Description: "exercises nested conditions and obligations",
		Rules: []abac.Rule{
			{
				ID:     "r-allow",
				Name:   "Allow eng non-secret",
				Effect: authz.EffectAllow,
				Conditions: []abac.Condition{
					{Source: abac.SourceSubject, Key: "department", Operator: abac.OpEquals, Values: []string{"eng"}},
					{Source: abac.SourceResource, Key: "classification", Operator: abac.OpNotIn, Values: []string{"secret", "top_secret"}},
				},
				Obligations: authz.Obligations{
					RowScope:  tenant.RowScopeSelf,
					FieldMask: authz.FieldMask{Fields: []string{"ssn", "email"}},
				},
			},
			{
				// No conditions, no obligations — exercises the nil/zero branches.
				ID:     "r-deny",
				Name:   "Deny everything else",
				Effect: authz.EffectDeny,
			},
		},
	}
}

// TestPGPolicyRepo_NestedPolicyRoundTrip proves a richly-nested policy survives a
// real PG JSONB encode→store→fetch→decode cycle unchanged — the fidelity the mem
// store cannot exercise (no serialization boundary).
func TestPGPolicyRepo_NestedPolicyRoundTrip(t *testing.T) {
	repo, _ := setupPolicyRepoPG(t)
	ctx := context.Background()
	tid := newIntegrationTenant(t)
	p := richPolicy("pol-rich", tid)

	require.NoError(t, repo.Save(ctx, tid, p))
	got, err := repo.GetByID(ctx, tid, "pol-rich")
	require.NoError(t, err)
	assert.Equal(t, p, got, "a richly-nested policy must survive a real PG JSONB round-trip unchanged")
}

// TestPGPolicyRepo_UpsertReplace proves Save over an existing (tenant_id, id)
// replaces the row atomically (ON CONFLICT DO UPDATE) rather than failing on a
// duplicate key or creating a second row.
func TestPGPolicyRepo_UpsertReplace(t *testing.T) {
	repo, pool := setupPolicyRepoPG(t)
	ctx := context.Background()
	tid := newIntegrationTenant(t)

	require.NoError(t, repo.Save(ctx, tid, richPolicy("pol-x", tid)))
	createdAt1 := policyCreatedAt(t, pool, tid, "pol-x")

	v2 := &abac.Policy{
		ID:       "pol-x",
		TenantID: tid,
		Name:     "Replaced",
		Rules:    []abac.Rule{{ID: "only", Name: "Allow all", Effect: authz.EffectAllow}},
	}
	require.NoError(t, repo.Save(ctx, tid, v2), "Save over an existing id must replace, not conflict")

	got, err := repo.GetByID(ctx, tid, "pol-x")
	require.NoError(t, err)
	assert.Equal(t, "Replaced", got.Name)
	assert.Len(t, got.Rules, 1, "replaced policy must reflect the new rule set")

	// The ON CONFLICT clause advances updated_at but must NOT touch created_at —
	// guards against a future SQL edit that adds created_at = EXCLUDED.created_at.
	assert.Equal(t, createdAt1, policyCreatedAt(t, pool, tid, "pol-x"),
		"upsert must preserve the original created_at")

	list, err := repo.ListByTenant(ctx, tid)
	require.NoError(t, err)
	assert.Len(t, list, 1, "upsert must not create a duplicate row")
}

// TestPGPolicyRepo_CorruptRulesRow_FailsClosed inserts a row carrying an unknown
// enum code directly (bypassing the codec) and asserts BOTH read paths fail closed
// with ErrPGSchemaShape — GetByID and ListByTenant (the evaluator's hot path) must
// classify a corrupt/forward-incompatible row identically, never silently
// mis-decoding a stored policy.
func TestPGPolicyRepo_CorruptRulesRow_FailsClosed(t *testing.T) {
	repo, pool := setupPolicyRepoPG(t)
	ctx := context.Background()
	tid := newIntegrationTenant(t)

	_, err := pool.DB().Exec(ctx,
		`INSERT INTO policies (tenant_id, id, name, description, rules, created_at, updated_at)
		 VALUES ($1, $2, $3, '', $4, now(), now())`,
		string(tid), "pol-corrupt", "Corrupt",
		[]byte(`[{"id":"r1","name":"x","effect":"permit","obligations":{}}]`))
	require.NoError(t, err)

	assertSchemaShape := func(t *testing.T, err error) {
		t.Helper()
		require.Error(t, err, "an unknown enum code must fail closed, not silently mis-decode")
		var ec *errcode.Error
		require.ErrorAs(t, err, &ec)
		assert.Equal(t, errcode.ErrPGSchemaShape, ec.Code, "corrupt rules JSON → ErrPGSchemaShape")
	}

	t.Run("GetByID", func(t *testing.T) {
		_, err := repo.GetByID(ctx, tid, "pol-corrupt")
		assertSchemaShape(t, err)
	})
	t.Run("ListByTenant", func(t *testing.T) {
		_, err := repo.ListByTenant(ctx, tid)
		assertSchemaShape(t, err)
	})
}
