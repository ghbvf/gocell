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
	"github.com/ghbvf/gocell/pkg/query"
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
	pool  *adapterpg.Pool
	repo  *accesspgrepo.PGPolicyRepo
	txMgr *adapterpg.TxManager
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
		clock.Real(), bundle.repo, testCursorCodec, slog.Default(), query.RunModeProd,
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
		clock.Real(), bundle.repo, testCursorCodec, slog.Default(), query.RunModeProd,
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

// TestL2Atomicity_policymanage_UpdateDelete_RollsBack proves the Update and
// Delete write paths are L2-atomic the same way Create is: when the outbox
// writer fails, neither the row mutation (version bump / deletion) nor the
// outbox entry persists, and a pass-through writer co-commits exactly one outbox
// row per successful mutation. (Create is covered by the sibling test above; the
// review's F4 flagged that Update/Delete are independent write-then-emit paths
// with no PG rollback proof.)
func TestL2Atomicity_policymanage_UpdateDelete_RollsBack(t *testing.T) {
	bundle := setupPolicyIntegPG(t)
	ctx := integAdminCtx()
	sentinel := errors.New("outbox broker down")

	passSvc := func() *Service {
		svc, err := NewService(
			clock.Real(), bundle.repo, testCursorCodec, slog.Default(), query.RunModeProd,
			WithEmitter(outbox.WrapEmitterForCell(testoutbox.MustEmitter(t, adapterpg.NewOutboxWriter(clock.Real())))),
			WithTxManager(persistence.WrapForCell(bundle.txMgr)),
		)
		require.NoError(t, err)
		return svc
	}
	failSvc := func() *Service {
		svc, err := NewService(
			clock.Real(), bundle.repo, testCursorCodec, slog.Default(), query.RunModeProd,
			WithEmitter(outbox.WrapEmitterForCell(testoutbox.MustEmitter(t, &recordingWriter{Err: sentinel}))),
			WithTxManager(persistence.WrapForCell(bundle.txMgr)),
		)
		require.NoError(t, err)
		return svc
	}
	outboxCount := func() int {
		var n int
		row := bundle.pool.DB().QueryRow(context.Background(),
			"SELECT COUNT(*) FROM outbox_entries WHERE event_type = $1", TopicPolicyUpdated)
		require.NoError(t, row.Scan(&n))
		return n
	}

	// Seed a policy at version 1 (co-commits one outbox row).
	created, err := passSvc().Create(ctx, CreateInput{Name: "CAS", Rules: minimalRules()})
	require.NoError(t, err)
	require.Equal(t, 1, created.Version)
	require.Equal(t, 1, outboxCount())

	// --- Update rollback ---
	_, updErr := failSvc().Update(ctx, UpdateInput{
		ID: created.ID, Name: "CAS-v2", Rules: minimalRules(), ExpectedVersion: 1,
	})
	require.Error(t, updErr)
	assert.ErrorIs(t, updErr, sentinel, "Update error must wrap the injected outbox sentinel")

	stillV1, err := bundle.repo.GetByID(context.Background(), integTestTenant, created.ID)
	require.NoError(t, err)
	assert.Equal(t, 1, stillV1.Version, "failed Update must roll back the version bump")
	assert.Equal(t, "CAS", stillV1.Name, "failed Update must roll back the name change")
	assert.Equal(t, 1, outboxCount(), "failed Update must not co-commit an outbox row")

	// Negative control: pass-through Update → version 2 + one new outbox row.
	updated, err := passSvc().Update(ctx, UpdateInput{
		ID: created.ID, Name: "CAS-v2", Rules: minimalRules(), ExpectedVersion: 1,
	})
	require.NoError(t, err)
	assert.Equal(t, 2, updated.Version)
	assert.Equal(t, 2, outboxCount())

	// --- Delete rollback ---
	_, delErr := failSvc().Delete(ctx, created.ID, 2)
	require.Error(t, delErr)
	assert.ErrorIs(t, delErr, sentinel, "Delete error must wrap the injected outbox sentinel")

	stillThere, err := bundle.repo.GetByID(context.Background(), integTestTenant, created.ID)
	require.NoError(t, err)
	assert.Equal(t, 2, stillThere.Version, "failed Delete must roll back — row still present at version 2")
	assert.Equal(t, 2, outboxCount(), "failed Delete must not co-commit an outbox row")

	// Negative control: pass-through Delete removes the row + one new outbox row.
	_, err = passSvc().Delete(ctx, created.ID, 2)
	require.NoError(t, err)
	_, getErr := bundle.repo.GetByID(context.Background(), integTestTenant, created.ID)
	require.Error(t, getErr, "policy must be gone after successful Delete")
	assert.Equal(t, 3, outboxCount())
}
