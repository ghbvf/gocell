// Package conformance defines a ports.Registry contract acceptance suite shared
// by every ports.Registry implementation (mem, PG, future). Each implementation
// MUST call RunRegistryConformance from a _test.go in its own package; the
// archtest REGISTRY-CONFORMANCE-ENROLLMENT-01 enforces enrollment so the two
// stores (in-memory + PostgreSQL) can never silently diverge on the interface
// contract (#2388 — finding F16 from PR #2383: independent per-impl tests do not
// catch a boundary divergence such as History returning nil vs an empty slice).
//
// The suite asserts the *documented* port contract, not byte-level impl detail:
// History's "no events" result is checked with require.Empty (the port godoc
// states callers MUST NOT distinguish a nil from an empty slice), so the suite is
// purely additive — it forces semantic equivalence without mandating a behavior
// change in either store.
//
// Writes (Create/Transition) require an ambient tx (the port contract; the PG
// store asserts it), so the factory hands back a persistence.TxRunner the suite
// wraps every write in. Reads (Get/List/History) are issued directly: both stores
// isolate by the explicit typed tenant parameter (PG additionally via a
// WHERE tenant_id predicate), so no read needs an ambient tx under the test role.
// There is no Features struct — unlike UserRepository there is no mem/PG behavior
// fork to gate; every sub-test exercises one concrete path on both stores.
//
// ref: corecells/accesscore/internal/ports/conformance/conformance.go (in-repo template)
// ref: ThreeDotsLabs/watermill pubsub/tests/test_pubsub.go (factory + shared suite)
package conformance

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/corecells/registrycore/internal/ports"
	"github.com/ghbvf/gocell/framework/kernel/persistence"
	"github.com/ghbvf/gocell/framework/kernel/registry"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/pkg/panicregister"
	"github.com/ghbvf/gocell/framework/pkg/query"
	"github.com/ghbvf/gocell/framework/pkg/tenant"
)

// idASC is the canonical keyset sort (id ASC) — the only sort column the Registry
// list interface supports.
var idASC = []query.SortColumn{{Name: "id", Direction: query.SortASC}}

// testTenantID / testTenantIDOther are the canonical tenant UUIDs used across the
// suite: every read/write is scoped to testTenantID; the cross-tenant isolation
// sub-test probes testTenantIDOther to prove rows are partitioned by tenant.
var (
	testTenantID      = mustParseTenant("00000000-0000-0000-0000-000000000001")
	testTenantIDOther = mustParseTenant("00000000-0000-0000-0000-000000000002")
)

// mustParseTenant parses a canonical tenant UUID for the package-level fixtures.
// A parse failure is a programmer error in a constant literal; it surfaces through
// the registered-panic funnel (conformance.go is a non-_test.go file scanned by
// PANIC-REGISTERED-01).
func mustParseTenant(s string) tenant.TenantID {
	id, err := tenant.ParseTenantID(s)
	if err != nil {
		panic(panicregister.Approved("registry-conformance-test-tenant-id",
			errcode.Assertion("conformance: invalid test tenant id %q: %v", s, err)))
	}
	return id
}

// RegistryFactory constructs a fresh ports.Registry, its paired
// persistence.TxRunner (used to provide the ambient tx writes require — a
// pass-through for stores that need none), and a cleanup func, for one sub-case.
// The factory is called once per sub-test; cleanup is registered via t.Cleanup.
type RegistryFactory func(t *testing.T) (repo ports.Registry, txRunner persistence.TxRunner, cleanup func())

// RunRegistryConformance executes the full ports.Registry conformance suite.
func RunRegistryConformance(t *testing.T, factory RegistryFactory) {
	t.Helper()
	t.Run("Create_RecordsSubmitted", func(t *testing.T) { conformCreateRecordsSubmitted(t, factory) })
	t.Run("Create_DuplicateRejected", func(t *testing.T) { conformCreateDuplicateRejected(t, factory) })
	t.Run("Create_MissingFieldRejected", func(t *testing.T) { conformCreateMissingFieldRejected(t, factory) })
	t.Run("Transition_LegalAdvancesAndAppends", func(t *testing.T) { conformTransitionLegal(t, factory) })
	t.Run("Transition_IllegalRejectedNoMutation", func(t *testing.T) { conformTransitionIllegal(t, factory) })
	t.Run("Transition_UnknownIDNotFound", func(t *testing.T) { conformTransitionUnknownID(t, factory) })
	t.Run("Transition_ApprovedRecordsApprover", func(t *testing.T) { conformTransitionApprover(t, factory) })
	t.Run("Get_UnknownReturnsNotFound", func(t *testing.T) { conformGetUnknown(t, factory) })
	t.Run("List_OrderedCursorPaginated", func(t *testing.T) { conformListPaginated(t, factory) })
	t.Run("List_Empty", func(t *testing.T) { conformListEmpty(t, factory) })
	t.Run("List_StateFilter", func(t *testing.T) { conformListStateFilter(t, factory) })
	t.Run("History_OrderedSeq", func(t *testing.T) { conformHistoryOrdered(t, factory) })
	t.Run("History_UnknownIDEmpty", func(t *testing.T) { conformHistoryUnknownEmpty(t, factory) })
	t.Run("CrossTenant_Isolation", func(t *testing.T) { conformCrossTenantIsolation(t, factory) })
	t.Run("InvalidTenant_Rejected", func(t *testing.T) { conformInvalidTenantRejected(t, factory) })
}

// ─── helpers ────────────────────────────────────────────────────────────────

// create submits one registration inside an ambient tx (the write contract) and
// returns the projected ContractRegistration.
func create(
	t *testing.T, txRunner persistence.TxRunner, repo ports.Registry, tn tenant.TenantID, in registry.SubmitInput,
) registry.ContractRegistration {
	t.Helper()
	var out registry.ContractRegistration
	require.NoError(t, txRunner.RunInTx(context.Background(), func(ctx context.Context) error {
		var err error
		out, err = repo.Create(ctx, tn, in)
		return err
	}), "create: %s", in.ID)
	return out
}

// transition advances one registration inside an ambient tx and returns the repo
// result (projection, error) for the caller to assert.
func transition(
	t *testing.T, txRunner persistence.TxRunner, repo ports.Registry, tn tenant.TenantID, in registry.AdvanceInput,
) (registry.ContractRegistration, error) {
	t.Helper()
	var out registry.ContractRegistration
	err := txRunner.RunInTx(context.Background(), func(ctx context.Context) error {
		var e error
		out, e = repo.Transition(ctx, tn, in)
		return e
	})
	return out, err
}

// assertCode asserts err carries a *errcode.Error with the wanted Code.
func assertCode(t *testing.T, err error, want errcode.Code) {
	t.Helper()
	var ce *errcode.Error
	require.True(t, errors.As(err, &ce), "want *errcode.Error %s, got %v", want, err)
	assert.Equal(t, want, ce.Code)
}

// submitInput is a small fixture builder for the common http submission shape.
func submitInput(id string) registry.SubmitInput {
	return registry.SubmitInput{ID: id, Kind: "http", Submitter: "alice"}
}

// ─── sub-tests ──────────────────────────────────────────────────────────────

func conformCreateRecordsSubmitted(t *testing.T, factory RegistryFactory) {
	t.Helper()
	repo, txRunner, cleanup := factory(t)
	t.Cleanup(cleanup)

	reg := create(t, txRunner, repo, testTenantID, registry.SubmitInput{
		ID: "http.foo.v1", Kind: "http", Submitter: "alice", PayloadSchema: "sha256:abc",
	})
	assert.Equal(t, "http.foo.v1", reg.ID)
	assert.Equal(t, "http", reg.Kind)
	assert.Equal(t, "alice", reg.Submitter)
	assert.Equal(t, registry.StateSubmitted(), reg.State)

	got, ok, err := repo.Get(context.Background(), testTenantID, "http.foo.v1")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, registry.StateSubmitted(), got.State)
	assert.Equal(t, "alice", got.Submitter)

	// Initial migration event: From = zero sentinel, To = submitted, Seq = 1.
	evs, err := repo.History(context.Background(), testTenantID, "http.foo.v1")
	require.NoError(t, err)
	require.Len(t, evs, 1)
	assert.True(t, evs[0].From.IsZero())
	assert.Equal(t, registry.StateSubmitted(), evs[0].To)
	assert.Equal(t, 1, evs[0].Seq)
	assert.Equal(t, "alice", evs[0].Actor)
}

func conformCreateDuplicateRejected(t *testing.T, factory RegistryFactory) {
	t.Helper()
	repo, txRunner, cleanup := factory(t)
	t.Cleanup(cleanup)

	create(t, txRunner, repo, testTenantID, submitInput("dup"))
	dupErr := txRunner.RunInTx(context.Background(), func(ctx context.Context) error {
		_, e := repo.Create(ctx, testTenantID, registry.SubmitInput{ID: "dup", Kind: "http", Submitter: "bob"})
		return e
	})
	assertCode(t, dupErr, errcode.ErrRegistrationDuplicate)
}

func conformCreateMissingFieldRejected(t *testing.T, factory RegistryFactory) {
	t.Helper()
	repo, txRunner, cleanup := factory(t)
	t.Cleanup(cleanup)

	err := txRunner.RunInTx(context.Background(), func(ctx context.Context) error {
		_, e := repo.Create(ctx, testTenantID, registry.SubmitInput{ID: "", Kind: "http", Submitter: "alice"})
		return e
	})
	assertCode(t, err, errcode.ErrValidationFailed)
}

func conformTransitionLegal(t *testing.T, factory RegistryFactory) {
	t.Helper()
	repo, txRunner, cleanup := factory(t)
	t.Cleanup(cleanup)

	create(t, txRunner, repo, testTenantID, submitInput("http.foo.v1"))
	got, err := transition(t, txRunner, repo, testTenantID, registry.AdvanceInput{
		ID: "http.foo.v1", To: registry.StateProbing(), Actor: "system",
	})
	require.NoError(t, err)
	assert.Equal(t, registry.StateProbing(), got.State)

	evs, err := repo.History(context.Background(), testTenantID, "http.foo.v1")
	require.NoError(t, err)
	require.Len(t, evs, 2)
	assert.Equal(t, registry.StateSubmitted(), evs[1].From)
	assert.Equal(t, registry.StateProbing(), evs[1].To)
	assert.Equal(t, 2, evs[1].Seq)
}

func conformTransitionIllegal(t *testing.T, factory RegistryFactory) {
	t.Helper()
	repo, txRunner, cleanup := factory(t)
	t.Cleanup(cleanup)

	create(t, txRunner, repo, testTenantID, submitInput("http.foo.v1"))
	// submitted → active is illegal (must go through the lifecycle).
	_, err := transition(t, txRunner, repo, testTenantID, registry.AdvanceInput{
		ID: "http.foo.v1", To: registry.StateActive(), Actor: "system",
	})
	assertCode(t, err, errcode.ErrRegistrationInvalidTransition)

	// No half-write: still submitted, history unchanged (length 1).
	got, _, _ := repo.Get(context.Background(), testTenantID, "http.foo.v1")
	assert.Equal(t, registry.StateSubmitted(), got.State)
	evs, _ := repo.History(context.Background(), testTenantID, "http.foo.v1")
	assert.Len(t, evs, 1)
}

func conformTransitionUnknownID(t *testing.T, factory RegistryFactory) {
	t.Helper()
	repo, txRunner, cleanup := factory(t)
	t.Cleanup(cleanup)

	_, err := transition(t, txRunner, repo, testTenantID, registry.AdvanceInput{
		ID: "missing", To: registry.StateProbing(), Actor: "system",
	})
	assertCode(t, err, errcode.ErrRegistrationNotFound)
}

func conformTransitionApprover(t *testing.T, factory RegistryFactory) {
	t.Helper()
	repo, txRunner, cleanup := factory(t)
	t.Cleanup(cleanup)

	create(t, txRunner, repo, testTenantID, submitInput("http.foo.v1"))
	for _, to := range []registry.RegistrationState{
		registry.StateProbing(), registry.StateConformant(), registry.StatePendingApproval(), registry.StateApproved(),
	} {
		_, err := transition(t, txRunner, repo, testTenantID, registry.AdvanceInput{ID: "http.foo.v1", To: to, Actor: "admin"})
		require.NoError(t, err)
	}
	got, ok, err := repo.Get(context.Background(), testTenantID, "http.foo.v1")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, registry.StateApproved(), got.State)
	assert.Equal(t, "admin", got.Approver)
}

func conformGetUnknown(t *testing.T, factory RegistryFactory) {
	t.Helper()
	repo, _, cleanup := factory(t)
	t.Cleanup(cleanup)

	got, ok, err := repo.Get(context.Background(), testTenantID, "nobody")
	require.NoError(t, err)
	assert.False(t, ok, "unknown id must report ok=false")
	assert.Equal(t, registry.ContractRegistration{}, got, "unknown id must return the zero registration")
}

func conformListPaginated(t *testing.T, factory RegistryFactory) {
	t.Helper()
	repo, txRunner, cleanup := factory(t)
	t.Cleanup(cleanup)

	for _, id := range []string{"c", "a", "b", "d"} {
		create(t, txRunner, repo, testTenantID, submitInput(id))
	}
	ctx := context.Background()

	// Page 1: Limit=2 → FetchLimit=3; 4 rows → returns a,b,c (the +1 row signals hasMore).
	page, err := repo.List(ctx, testTenantID, query.ListParams{Limit: 2, Sort: idASC}, ports.ListFilter{})
	require.NoError(t, err)
	require.Len(t, page, 3)
	assert.Equal(t, "a", page[0].ID)
	assert.Equal(t, "b", page[1].ID)
	assert.Equal(t, "c", page[2].ID)

	// Page 2: cursor after the last visible id "b" → returns c,d (< FetchLimit → no more).
	page2, err := repo.List(ctx, testTenantID, query.ListParams{Limit: 2, Sort: idASC, CursorValues: []any{"b"}}, ports.ListFilter{})
	require.NoError(t, err)
	require.Len(t, page2, 2)
	assert.Equal(t, "c", page2[0].ID)
	assert.Equal(t, "d", page2[1].ID)
}

func conformListEmpty(t *testing.T, factory RegistryFactory) {
	t.Helper()
	repo, _, cleanup := factory(t)
	t.Cleanup(cleanup)

	page, err := repo.List(context.Background(), testTenantID, query.ListParams{Limit: 10, Sort: idASC}, ports.ListFilter{})
	require.NoError(t, err)
	assert.Empty(t, page)
}

func conformListStateFilter(t *testing.T, factory RegistryFactory) {
	t.Helper()
	repo, txRunner, cleanup := factory(t)
	t.Cleanup(cleanup)

	create(t, txRunner, repo, testTenantID, submitInput("a"))
	create(t, txRunner, repo, testTenantID, submitInput("b"))
	_, err := transition(t, txRunner, repo, testTenantID, registry.AdvanceInput{ID: "b", To: registry.StateProbing(), Actor: "system"})
	require.NoError(t, err)
	ctx := context.Background()

	// submitted filter → only "a".
	subPage, err := repo.List(ctx, testTenantID, query.ListParams{Limit: 10, Sort: idASC}, ports.ListFilter{State: registry.StateSubmitted()})
	require.NoError(t, err)
	require.Len(t, subPage, 1)
	assert.Equal(t, "a", subPage[0].ID)

	// probing filter → only "b".
	probePage, err := repo.List(ctx, testTenantID, query.ListParams{Limit: 10, Sort: idASC}, ports.ListFilter{State: registry.StateProbing()})
	require.NoError(t, err)
	require.Len(t, probePage, 1)
	assert.Equal(t, "b", probePage[0].ID)

	// a state with no rows → empty.
	nonePage, err := repo.List(ctx, testTenantID, query.ListParams{Limit: 10, Sort: idASC}, ports.ListFilter{State: registry.StateApproved()})
	require.NoError(t, err)
	assert.Empty(t, nonePage)
}

func conformHistoryOrdered(t *testing.T, factory RegistryFactory) {
	t.Helper()
	repo, txRunner, cleanup := factory(t)
	t.Cleanup(cleanup)

	create(t, txRunner, repo, testTenantID, submitInput("r1"))
	for _, to := range []registry.RegistrationState{
		registry.StateProbing(), registry.StateConformant(), registry.StatePendingApproval(), registry.StateApproved(),
	} {
		_, err := transition(t, txRunner, repo, testTenantID, registry.AdvanceInput{ID: "r1", To: to, Actor: "admin"})
		require.NoError(t, err)
	}

	evs, err := repo.History(context.Background(), testTenantID, "r1")
	require.NoError(t, err)
	require.Len(t, evs, 5) // submit + 4 transitions
	for i, ev := range evs {
		assert.Equal(t, i+1, ev.Seq, "Seq must be 1-based contiguous ascending")
	}
}

// conformHistoryUnknownEmpty pins the #2388 / F16 divergence: History on an
// unknown id returns a no-events result on both stores. Asserted with
// require.Empty (NOT a nil-specific check): the port godoc states callers MUST
// NOT distinguish a nil from an empty slice, so mem (nil) and PG (empty slice)
// are both contract-conformant and the suite must treat them as equivalent.
func conformHistoryUnknownEmpty(t *testing.T, factory RegistryFactory) {
	t.Helper()
	repo, _, cleanup := factory(t)
	t.Cleanup(cleanup)

	evs, err := repo.History(context.Background(), testTenantID, "nobody")
	require.NoError(t, err)
	assert.Empty(t, evs, "History on an unknown id must be a no-events result (nil or empty both conform)")
}

func conformCrossTenantIsolation(t *testing.T, factory RegistryFactory) {
	t.Helper()
	repo, txRunner, cleanup := factory(t)
	t.Cleanup(cleanup)

	create(t, txRunner, repo, testTenantID, registry.SubmitInput{ID: "shared.id", Kind: "event", Submitter: "carol"})
	ctx := context.Background()

	// Tenant B cannot see tenant A's row by Get / List / History.
	_, ok, err := repo.Get(ctx, testTenantIDOther, "shared.id")
	require.NoError(t, err)
	assert.False(t, ok)
	page, err := repo.List(ctx, testTenantIDOther, query.ListParams{Limit: 10, Sort: idASC}, ports.ListFilter{})
	require.NoError(t, err)
	assert.Empty(t, page)
	evs, err := repo.History(ctx, testTenantIDOther, "shared.id")
	require.NoError(t, err)
	assert.Empty(t, evs)

	// Per-tenant dedup: the same id is independently creatable under tenant B.
	create(t, txRunner, repo, testTenantIDOther, registry.SubmitInput{ID: "shared.id", Kind: "http", Submitter: "bob"})
	gotB, ok, err := repo.Get(ctx, testTenantIDOther, "shared.id")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "bob", gotB.Submitter)

	// Sanity: tenant A's row is still visible under its own tenant.
	gotA, ok, err := repo.Get(ctx, testTenantID, "shared.id")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "carol", gotA.Submitter)
}

// conformInvalidTenantRejected asserts every method rejects an empty (invalid)
// tenant with ErrValidationFailed — the typed tenant boundary fail-closes before
// any store access.
func conformInvalidTenantRejected(t *testing.T, factory RegistryFactory) {
	t.Helper()
	repo, txRunner, cleanup := factory(t)
	t.Cleanup(cleanup)

	zero := tenant.TenantID("")
	ctx := context.Background()

	cErr := txRunner.RunInTx(ctx, func(ctx context.Context) error {
		_, e := repo.Create(ctx, zero, submitInput("x"))
		return e
	})
	assertCode(t, cErr, errcode.ErrValidationFailed)

	tErr := txRunner.RunInTx(ctx, func(ctx context.Context) error {
		_, e := repo.Transition(ctx, zero, registry.AdvanceInput{ID: "x", To: registry.StateProbing(), Actor: "s"})
		return e
	})
	assertCode(t, tErr, errcode.ErrValidationFailed)

	_, _, gErr := repo.Get(ctx, zero, "x")
	assertCode(t, gErr, errcode.ErrValidationFailed)
	_, lErr := repo.List(ctx, zero, query.ListParams{Limit: 10, Sort: idASC}, ports.ListFilter{})
	assertCode(t, lErr, errcode.ErrValidationFailed)
	_, hErr := repo.History(ctx, zero, "x")
	assertCode(t, hErr, errcode.ErrValidationFailed)
}
