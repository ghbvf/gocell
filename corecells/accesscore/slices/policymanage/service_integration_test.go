//go:build integration

// Package policymanage — L2 atomicity integration test.
//
// Build tag: integration. Run with: go test -tags=integration ./...
// Not included in the default go test ./... run.
//
// TestL2Atomicity_policymanage_RollsBack verifies that when the outbox writer
// returns an error during Create, the policy row is absent (transaction rolled
// back atomically — L2 canonical rollback proof for the policymanage slice).
//
// A negative control follows: the same Create with a pass-through writer must
// succeed, proving the rollback assertion is not vacuously trivial.
package policymanage

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	adapterpg "github.com/ghbvf/gocell/adapters/postgres"
	accesspgrepo "github.com/ghbvf/gocell/corecells/accesscore/internal/adapters/postgres"
	"github.com/ghbvf/gocell/corecells/internal/testoutbox"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/pkg/ctxkeys"
	"github.com/ghbvf/gocell/pkg/migration"
	"github.com/ghbvf/gocell/pkg/tenant"
	"github.com/ghbvf/gocell/runtime/auth"
	globaltestutil "github.com/ghbvf/gocell/tests/testutil"
)

const integTestTenantStr = "30000000-0000-0000-0000-000000000001"

var integTestTenant = tenant.TenantID(integTestTenantStr)

func integAdminCtx() context.Context {
	return ctxkeys.WithTenantID(auth.TestContext("integ-admin", []string{"admin"}), integTestTenantStr)
}

func pgIntegMigrationsFS(t testing.TB) fs.FS {
	t.Helper()
	fsys, err := adapterpg.MigrationsFS()
	require.NoError(t, err)
	return fsys
}

type policyIntegBundle struct {
	pool   *adapterpg.Pool
	repo   *accesspgrepo.PGPolicyRepo
	txMgr  *adapterpg.TxManager
}

// setupPolicyIntegPG starts a testcontainers PostgreSQL instance, applies all
// migrations, and returns a PGPolicyRepo + TxManager wired on the real pool.
func setupPolicyIntegPG(t *testing.T) policyIntegBundle {
	t.Helper()
	globaltestutil.RequireDocker(t)

	ctx := context.Background()
	container, err := tcpostgres.Run(
		ctx, globaltestutil.PostgresImage,
		tcpostgres.WithDatabase("test"),
		tcpostgres.WithUsername("test"),
		tcpostgres.WithPassword("test"),
		tcpostgres.BasicWaitStrategies(),
	)
	require.NoError(t, err, "start postgres container")

	connStr, err := container.ConnectionString(ctx, "sslmode=disable")
	require.NoError(t, err)

	pool, err := adapterpg.NewPool(ctx, adapterpg.Config{DSN: connStr})
	require.NoError(t, err)

	migrator, err := adapterpg.NewMigrator(pool, pgIntegMigrationsFS(t), migration.PlatformNamespace)
	require.NoError(t, err)
	require.NoError(t, migrator.Up(ctx), "migrations must apply cleanly")

	txMgr := adapterpg.NewTxManager(pool)
	repo, err := accesspgrepo.NewPGPolicyRepo(pool.DB(), txMgr, clock.Real())
	require.NoError(t, err)

	t.Cleanup(func() {
		if cerr := pool.Close(ctx); cerr != nil {
			t.Logf("WARN: pool close: %v", cerr)
		}
		if cerr := container.Terminate(ctx); cerr != nil {
			t.Logf("WARN: terminate container: %v", cerr)
		}
	})

	return policyIntegBundle{pool: pool, repo: repo, txMgr: txMgr}
}

// TestL2Atomicity_policymanage_RollsBack verifies that when the outbox write
// fails during Create, the policy row is NOT persisted (L2 rollback proof).
//
// Negative control: the same Create with a pass-through outbox writer must
// succeed — this guards against a vacuous pass where the row was never written
// to begin with.
func TestL2Atomicity_policymanage_RollsBack(t *testing.T) {
	bundle := setupPolicyIntegPG(t)

	// Failing outbox writer — simulates outbox broker unavailable.
	sentinel := errors.New("outbox broker down")
	failWriter := &recordingWriter{Err: sentinel}

	svc, err := NewService(
		clock.Real(), bundle.repo, slog.Default(),
		WithEmitter(outbox.WrapEmitterForCell(testoutbox.MustEmitter(t, failWriter))),
		WithTxManager(persistence.WrapForCell(bundle.txMgr)),
	)
	require.NoError(t, err)

	_, createErr := svc.Create(integAdminCtx(), CreateInput{
		Name:  "L2RollbackTest",
		Rules: minimalRules(),
	})
	require.Error(t, createErr)
	assert.ErrorIs(t, createErr, sentinel,
		"Create error must wrap the injected outbox sentinel (proves Write was invoked)")

	// Policy row must NOT exist (transaction rolled back atomically).
	_, getErr := bundle.repo.ListByTenant(context.Background(), integTestTenant)
	require.NoError(t, getErr)
	// ListByTenant returns empty slice when tenant has no policies; the rollback
	// proof is that we see zero rows, not a not-found error.
	policies, listErr := bundle.repo.ListByTenant(context.Background(), integTestTenant)
	require.NoError(t, listErr)
	assert.Empty(t, policies, "policy row must not persist after outbox-failure rollback")

	// Negative control: pass-through Service on the same pool must succeed.
	passSvc, err := NewService(
		clock.Real(), bundle.repo, slog.Default(),
		WithEmitter(outbox.WrapEmitterForCell(testoutbox.MustEmitter(t, adapterpg.NewOutboxWriter(clock.Real())))),
		WithTxManager(persistence.WrapForCell(bundle.txMgr)),
	)
	require.NoError(t, err)

	got, err := passSvc.Create(integAdminCtx(), CreateInput{
		Name:  "L2RollbackTest",
		Rules: minimalRules(),
	})
	require.NoError(t, err, "negative control: Create must succeed with pass-through writer")
	assert.Equal(t, "L2RollbackTest", got.Name)
	assert.Equal(t, 1, got.Version)

	// Confirm the policy row is persisted.
	policies, listErr = bundle.repo.ListByTenant(context.Background(), integTestTenant)
	require.NoError(t, listErr)
	assert.Len(t, policies, 1, "negative control: policy row must exist after successful Create")

	// Positive co-commit assertion: the outbox_entries row must also exist in the
	// same transaction (L2 atomicity proof — policy row and outbox entry committed together).
	var outboxCount int
	row := bundle.pool.DB().QueryRow(context.Background(),
		"SELECT COUNT(*) FROM outbox_entries WHERE event_type = $1", TopicPolicyUpdated)
	require.NoError(t, row.Scan(&outboxCount))
	assert.Equal(t, 1, outboxCount,
		"positive co-commit: one outbox_entries row must exist for the successful Create")
}
