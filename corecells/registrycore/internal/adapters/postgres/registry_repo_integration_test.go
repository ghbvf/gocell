//go:build integration

package postgres

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	adapterpg "github.com/ghbvf/gocell/adapters/postgres"
	"github.com/ghbvf/gocell/corecells/registrycore/internal/ports"
	"github.com/ghbvf/gocell/framework/kernel/clock"
	"github.com/ghbvf/gocell/framework/kernel/registry"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/pkg/panicregister"
	"github.com/ghbvf/gocell/framework/pkg/query"
	"github.com/ghbvf/gocell/framework/pkg/tenant"
)

var (
	itTenantA = mustTenant("00000000-0000-0000-0000-000000000001")
	itTenantB = mustTenant("00000000-0000-0000-0000-000000000002")
)

func mustTenant(s string) tenant.TenantID {
	t, err := tenant.ParseTenantID(s)
	if err != nil {
		panic(panicregister.Approved("registry-integration-test-tenant-id",
			errcode.Assertion("mustTenant: invalid tenant id %q: %v", s, err)))
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
	page, err := repo.List(ctx, itTenantB, query.ListParams{Limit: 50, Sort: []query.SortColumn{{Name: "id", Direction: query.SortASC}}}, ports.ListFilter{})
	require.NoError(t, err)
	assert.Empty(t, page)

	// Per-tenant dedup: the same id is independently creatable under tenant B.
	createInTx(t, repo, txMgr, itTenantB, registry.SubmitInput{ID: "shared.id", Kind: "http", Submitter: "bob"})
	gotB, ok, err := repo.Get(ctx, itTenantB, "shared.id")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "bob", gotB.Submitter)
}

// TestRegistryPG_Integration_List_StateFilter verifies the WHERE state=$N SQL path in
// List. The mem-layer has TestRegistry_List_StateFilter_* coverage; this test
// exercises the PG-specific pgquery.AppendKeyset + b.AppendIf(state) branch on a
// real Postgres instance.
func TestRegistryPG_Integration_List_StateFilter(t *testing.T) {
	repo, txMgr := setupRegistryPG(t)
	ctx := context.Background()

	// Seed: two registrations under the same tenant.
	createInTx(t, repo, txMgr, itTenantA, registry.SubmitInput{ID: "sf.one", Kind: "http", Submitter: "alice"})
	createInTx(t, repo, txMgr, itTenantA, registry.SubmitInput{ID: "sf.two", Kind: "http", Submitter: "bob"})

	// Advance sf.two to probing — sf.one stays submitted.
	_, err := transitionInTx(t, repo, txMgr, itTenantA, registry.AdvanceInput{ID: "sf.two", To: registry.StateProbing(), Actor: "system"})
	require.NoError(t, err)

	listParams := query.ListParams{Limit: 50, Sort: []query.SortColumn{{Name: "id", Direction: query.SortASC}}}

	t.Run("MatchSubmitted", func(t *testing.T) {
		got, err := repo.List(ctx, itTenantA, listParams, ports.ListFilter{State: registry.StateSubmitted()})
		require.NoError(t, err)
		require.Len(t, got, 1, "only sf.one should be submitted")
		assert.Equal(t, "sf.one", got[0].ID)
		assert.Equal(t, registry.StateSubmitted(), got[0].State)
	})

	t.Run("MatchProbing", func(t *testing.T) {
		got, err := repo.List(ctx, itTenantA, listParams, ports.ListFilter{State: registry.StateProbing()})
		require.NoError(t, err)
		require.Len(t, got, 1, "only sf.two should be probing")
		assert.Equal(t, "sf.two", got[0].ID)
		assert.Equal(t, registry.StateProbing(), got[0].State)
	})

	t.Run("NoMatch", func(t *testing.T) {
		// Filter for approved — no rows match, must return empty slice not error.
		got, err := repo.List(ctx, itTenantA, listParams, ports.ListFilter{State: registry.StateApproved()})
		require.NoError(t, err)
		assert.Empty(t, got, "filter approved must return empty when no approved rows exist")
	})

	t.Run("NoFilter", func(t *testing.T) {
		// Omitting state filter returns all rows.
		got, err := repo.List(ctx, itTenantA, listParams, ports.ListFilter{})
		require.NoError(t, err)
		assert.Len(t, got, 2, "no filter must return both rows")
	})
}

// TestRegistryPG_Integration_List_Pagination exercises the keyset WHERE clause
// in List via real PostgreSQL. The existing TestRegistryPG_Integration_List_StateFilter
// uses a single page (Limit=50, no CursorValues), so pgquery.AppendKeyset's
// cursor branch is never reached against real PG. This test seeds N≥5 rows and
// walks three pages (Limit=2) to cover the single-column id-ASC keyset path:
//
//	AND id > $N  ORDER BY id ASC  LIMIT 3  (FetchLimit = Limit+1)
//
// Assertions:
//   - no id overlap between pages
//   - ids are globally monotonically increasing across pages (id ASC order preserved)
//   - last page returns fewer than Limit+1 rows (hasMore=false sentinel)
//   - the full seed set is returned exactly once across all pages
func TestRegistryPG_Integration_List_Pagination(t *testing.T) {
	repo, txMgr := setupRegistryPG(t)
	ctx := context.Background()

	// Seed 5 registrations ordered by id ("p." prefix avoids collision with
	// rows seeded in other tests that run in the same per-test DB).
	// All share the same submitted_at (clock.Real() is not advanced between
	// calls, so ties on any time column are possible — the tie-breaker is id).
	seedIDs := []string{"pg.a", "pg.b", "pg.c", "pg.d", "pg.e"}
	for _, id := range seedIDs {
		createInTx(t, repo, txMgr, itTenantA, registry.SubmitInput{ID: id, Kind: "http", Submitter: "ci"})
	}

	idASC := []query.SortColumn{{Name: "id", Direction: query.SortASC}}
	const limit = 2

	var (
		allIDs  []string // collects ids in page-walk order
		cursorV []any    // nil → first page
		pageNum int
	)

	for {
		pageNum++
		params := query.ListParams{Limit: limit, Sort: idASC, CursorValues: cursorV}
		rows, err := repo.List(ctx, itTenantA, params, ports.ListFilter{})
		require.NoError(t, err, "page %d: List must not error", pageNum)

		// hasMore = N+1 detection: FetchLimit=limit+1, so len>limit means more rows exist.
		hasMore := len(rows) > limit
		if hasMore {
			rows = rows[:limit] // trim to requested page size
		}

		require.NotEmpty(t, rows, "page %d must not be empty (unexpected early termination)", pageNum)

		for _, r := range rows {
			allIDs = append(allIDs, r.ID)
		}

		if !hasMore {
			break // last page
		}

		// Advance cursor to last visible row's id (single-column keyset: id ASC).
		cursorV = []any{rows[len(rows)-1].ID}

		// Safety: stop after more pages than total seeds to prevent infinite loop.
		if pageNum > len(seedIDs) {
			t.Fatalf("pagination did not terminate after %d pages (seed count=%d)", pageNum, len(seedIDs))
		}
	}

	// Full set returned exactly once.
	assert.Equal(t, seedIDs, allIDs,
		"all seeded ids must appear exactly once across pages in id ASC order")

	// Global monotonic order: ids must be strictly increasing.
	for i := 1; i < len(allIDs); i++ {
		assert.Less(t, allIDs[i-1], allIDs[i],
			"id order must be strictly ascending across page boundary at position %d", i)
	}

	// No duplicates.
	seen := make(map[string]struct{}, len(allIDs))
	for _, id := range allIDs {
		assert.NotContains(t, seen, id, "duplicate id %q across pages", id)
		seen[id] = struct{}{}
	}
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
