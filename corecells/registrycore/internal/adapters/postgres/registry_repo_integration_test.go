//go:build integration

package postgres

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	adapterpg "github.com/ghbvf/gocell/adapters/postgres"
	"github.com/ghbvf/gocell/framework/kernel/clock"
	"github.com/ghbvf/gocell/framework/kernel/registry"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/pkg/tenant"
)

var (
	itTenantA = mustTenant("00000000-0000-0000-0000-000000000001")
	itTenantB = mustTenant("00000000-0000-0000-0000-000000000002")
)

func mustTenant(s string) tenant.TenantID {
	t, err := tenant.ParseTenantID(s)
	if err != nil {
		panic("mustTenant: " + err.Error())
	}
	return t
}

// setupRegistryPG clones the package-shared pre-migrated template into a fresh
// per-test database and returns a PG Registry + TxManager over it.
func setupRegistryPG(t *testing.T) (*Registry, *adapterpg.TxManager) {
	t.Helper()
	pool := sharedPG.NewPerTestPool(t)
	repo := NewRegistry(pool.DB(), clock.Real())
	txMgr := adapterpg.NewTxManager(pool)
	return repo, txMgr
}

func createInTx(t *testing.T, repo *Registry, txMgr *adapterpg.TxManager, tn tenant.TenantID, in registry.SubmitInput) registry.ContractRegistration {
	t.Helper()
	var out registry.ContractRegistration
	require.NoError(t, txMgr.RunInTx(context.Background(), func(ctx context.Context) error {
		var err error
		out, err = repo.Create(ctx, tn, in)
		return err
	}))
	return out
}

func transitionInTx(t *testing.T, repo *Registry, txMgr *adapterpg.TxManager, tn tenant.TenantID, in registry.AdvanceInput) (registry.ContractRegistration, error) {
	t.Helper()
	var out registry.ContractRegistration
	err := txMgr.RunInTx(context.Background(), func(ctx context.Context) error {
		var e error
		out, e = repo.Transition(ctx, tn, in)
		return e
	})
	return out, err
}

func TestRegistryPG_Integration_CreateGetHistory(t *testing.T) {
	repo, txMgr := setupRegistryPG(t)
	ctx := context.Background()

	reg := createInTx(t, repo, txMgr, itTenantA, registry.SubmitInput{ID: "http.foo.v1", Kind: "http", Submitter: "alice", PayloadSchema: "sha256:abc"})
	assert.Equal(t, registry.StateSubmitted(), reg.State)
	assert.Equal(t, "sha256:abc", reg.PayloadSchema)

	got, ok, err := repo.Get(ctx, itTenantA, "http.foo.v1")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "alice", got.Submitter)
	assert.Equal(t, registry.StateSubmitted(), got.State)

	evs, err := repo.History(ctx, itTenantA, "http.foo.v1")
	require.NoError(t, err)
	require.Len(t, evs, 1)
	assert.True(t, evs[0].From.IsZero())
	assert.Equal(t, registry.StateSubmitted(), evs[0].To)
	assert.Equal(t, 1, evs[0].Seq)
}

func TestRegistryPG_Integration_DuplicateRejected(t *testing.T) {
	repo, txMgr := setupRegistryPG(t)
	createInTx(t, repo, txMgr, itTenantA, registry.SubmitInput{ID: "dup", Kind: "http", Submitter: "alice"})

	err := txMgr.RunInTx(context.Background(), func(ctx context.Context) error {
		_, e := repo.Create(ctx, itTenantA, registry.SubmitInput{ID: "dup", Kind: "http", Submitter: "bob"})
		return e
	})
	var ce *errcode.Error
	require.ErrorAs(t, err, &ce)
	assert.Equal(t, errcode.ErrRegistrationDuplicate, ce.Code)
}

func TestRegistryPG_Integration_TransitionLegalAndApprover(t *testing.T) {
	repo, txMgr := setupRegistryPG(t)
	ctx := context.Background()
	createInTx(t, repo, txMgr, itTenantA, registry.SubmitInput{ID: "r1", Kind: "http", Submitter: "alice"})

	for _, to := range []registry.RegistrationState{
		registry.StateProbing(), registry.StateConformant(), registry.StatePendingApproval(), registry.StateApproved(),
	} {
		_, err := transitionInTx(t, repo, txMgr, itTenantA, registry.AdvanceInput{ID: "r1", To: to, Actor: "admin"})
		require.NoError(t, err)
	}
	got, _, err := repo.Get(ctx, itTenantA, "r1")
	require.NoError(t, err)
	assert.Equal(t, registry.StateApproved(), got.State)
	assert.Equal(t, "admin", got.Approver)

	evs, err := repo.History(ctx, itTenantA, "r1")
	require.NoError(t, err)
	require.Len(t, evs, 5) // submit + 4 transitions
	for i, ev := range evs {
		assert.Equal(t, i+1, ev.Seq, "seq must be 1-based contiguous")
	}
}

func TestRegistryPG_Integration_TransitionIllegalRejected(t *testing.T) {
	repo, txMgr := setupRegistryPG(t)
	ctx := context.Background()
	createInTx(t, repo, txMgr, itTenantA, registry.SubmitInput{ID: "r1", Kind: "http", Submitter: "alice"})

	_, err := transitionInTx(t, repo, txMgr, itTenantA, registry.AdvanceInput{ID: "r1", To: registry.StateActive(), Actor: "admin"})
	var ce *errcode.Error
	require.ErrorAs(t, err, &ce)
	assert.Equal(t, errcode.ErrRegistrationInvalidTransition, ce.Code)

	// No half-write: still submitted, history length 1.
	got, _, _ := repo.Get(ctx, itTenantA, "r1")
	assert.Equal(t, registry.StateSubmitted(), got.State)
	evs, _ := repo.History(ctx, itTenantA, "r1")
	assert.Len(t, evs, 1)
}

// TestRegistryPG_Integration_L1Atomicity is T052: a transition that writes the
// projection + history row but whose enclosing tx then fails must roll BOTH back
// — no half state.
func TestRegistryPG_Integration_L1Atomicity(t *testing.T) {
	repo, txMgr := setupRegistryPG(t)
	ctx := context.Background()
	createInTx(t, repo, txMgr, itTenantA, registry.SubmitInput{ID: "r1", Kind: "http", Submitter: "alice"})

	err := txMgr.RunInTx(ctx, func(txCtx context.Context) error {
		if _, e := repo.Transition(txCtx, itTenantA, registry.AdvanceInput{ID: "r1", To: registry.StateProbing(), Actor: "system"}); e != nil {
			return e
		}
		return errors.New("simulated failure after the transition write — roll back both")
	})
	require.Error(t, err)

	// Projection unchanged AND no new history row (the append must have rolled back too).
	got, _, err := repo.Get(ctx, itTenantA, "r1")
	require.NoError(t, err)
	assert.Equal(t, registry.StateSubmitted(), got.State, "rolled-back transition must not persist the projection update")
	evs, err := repo.History(ctx, itTenantA, "r1")
	require.NoError(t, err)
	assert.Len(t, evs, 1, "rolled-back transition must not persist the appended event")
}

func TestRegistryPG_Integration_CrossTenantIsolation(t *testing.T) {
	repo, txMgr := setupRegistryPG(t)
	ctx := context.Background()
	createInTx(t, repo, txMgr, itTenantA, registry.SubmitInput{ID: "shared.id", Kind: "http", Submitter: "alice"})

	// Tenant B cannot see tenant A's row.
	_, ok, err := repo.Get(ctx, itTenantB, "shared.id")
	require.NoError(t, err)
	assert.False(t, ok)
	page, err := repo.List(ctx, itTenantB, "", 50)
	require.NoError(t, err)
	assert.Empty(t, page)

	// Per-tenant dedup: the same id is independently creatable under tenant B.
	createInTx(t, repo, txMgr, itTenantB, registry.SubmitInput{ID: "shared.id", Kind: "http", Submitter: "bob"})
	gotB, ok, err := repo.Get(ctx, itTenantB, "shared.id")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "bob", gotB.Submitter)
}

func TestRegistryPG_Integration_EmptyTenantGuard(t *testing.T) {
	repo, txMgr := setupRegistryPG(t)
	zero := tenant.TenantID("")

	err := txMgr.RunInTx(context.Background(), func(ctx context.Context) error {
		_, e := repo.Create(ctx, zero, registry.SubmitInput{ID: "x", Kind: "http", Submitter: "alice"})
		return e
	})
	var ce *errcode.Error
	require.ErrorAs(t, err, &ce)
	assert.Equal(t, errcode.ErrValidationFailed, ce.Code, "empty tenant must fail validation, not issue a tenant-less query")
}
