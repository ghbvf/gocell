// Package conformance defines a ports.Registry contract acceptance suite shared
// by every ports.Registry implementation (mem, PG, future). Each implementation
// MUST call RunRegistryConformance from a _test.go in its own package; the
// archtest REGISTRY-CONFORMANCE-ENROLLMENT-01 enforces enrollment so the two
// stores (in-memory + PostgreSQL) can never silently diverge on the interface
// contract (#2388 — finding F16 from PR #2383: independent per-impl tests do not
// catch a boundary divergence such as History returning nil vs an empty slice).
//
// The suite asserts the *documented* port contract, not byte-level impl detail:
// History's "no events" result is checked with wantEmpty (the port godoc states
// callers MUST NOT distinguish a nil from an empty slice), so the suite is purely
// additive — it forces semantic equivalence without mandating a behavior change
// in either store.
//
// Writes (Create/Transition) require an ambient tx (the port contract; the PG
// store asserts it), so the factory hands back a persistence.TxRunner the suite
// wraps every write in. Reads (Get/List/History) are issued directly: both stores
// isolate by the explicit typed tenant parameter (PG additionally via a
// WHERE tenant_id predicate), so no read needs an ambient tx under the test role.
// There is no Features struct — unlike UserRepository there is no mem/PG behavior
// fork to gate; every sub-test exercises one concrete path on both stores.
//
// Assertions use the stdlib testing.T directly (no testify): conformance.go is a
// non-_test.go file in the corecells module, where the cells-isolation depguard
// bans third-party test libraries — same constraint and pattern as the accesscore
// ports/conformance suite.
//
// ref: corecells/accesscore/internal/ports/conformance/conformance.go (in-repo template)
// ref: ThreeDotsLabs/watermill pubsub/tests/test_pubsub.go (factory + shared suite)
package conformance

import (
	"context"
	"errors"
	"testing"

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

// ─── assertion + fixture helpers (stdlib testing, no testify) ─────────────────

// fatalIfErr fails the sub-test immediately on a non-nil error.
func fatalIfErr(t *testing.T, err error, what string) {
	t.Helper()
	if err != nil {
		t.Fatalf("%s: unexpected error: %v", what, err)
	}
}

// fatalUnless fails the sub-test immediately when cond is false (preconditions).
func fatalUnless(t *testing.T, cond bool, format string, args ...any) {
	t.Helper()
	if !cond {
		t.Fatalf(format, args...)
	}
}

// errUnless records a non-fatal failure when cond is false (assertions).
func errUnless(t *testing.T, cond bool, format string, args ...any) {
	t.Helper()
	if !cond {
		t.Errorf(format, args...)
	}
}

// assertCode asserts err carries a *errcode.Error with the wanted Code.
func assertCode(t *testing.T, err error, want errcode.Code, what string) {
	t.Helper()
	var ce *errcode.Error
	if !errors.As(err, &ce) {
		t.Fatalf("%s: want *errcode.Error %s, got %v", what, want, err)
	}
	errUnless(t, ce.Code == want, "%s: code = %s, want %s", what, ce.Code, want)
}

// create submits one registration inside an ambient tx (the write contract) and
// returns the projected ContractRegistration.
func create(
	t *testing.T, txRunner persistence.TxRunner, repo ports.Registry, tn tenant.TenantID, in registry.SubmitInput,
) registry.ContractRegistration {
	t.Helper()
	var out registry.ContractRegistration
	err := txRunner.RunInTx(context.Background(), func(ctx context.Context) error {
		var e error
		out, e = repo.Create(ctx, tn, in)
		return e
	})
	fatalIfErr(t, err, "create "+in.ID)
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
	errUnless(t, reg.ID == "http.foo.v1", "Create: id = %q, want http.foo.v1", reg.ID)
	errUnless(t, reg.Kind == "http", "Create: kind = %q, want http", reg.Kind)
	errUnless(t, reg.Submitter == "alice", "Create: submitter = %q, want alice", reg.Submitter)
	errUnless(t, reg.State == registry.StateSubmitted(), "Create: state = %s, want submitted", reg.State)

	got, ok, err := repo.Get(context.Background(), testTenantID, "http.foo.v1")
	fatalIfErr(t, err, "Get")
	fatalUnless(t, ok, "Get: want ok=true for created registration")
	errUnless(t, got.State == registry.StateSubmitted(), "Get: state = %s, want submitted", got.State)
	errUnless(t, got.Submitter == "alice", "Get: submitter = %q, want alice", got.Submitter)

	// Initial migration event: From = zero sentinel, To = submitted, Seq = 1.
	evs, err := repo.History(context.Background(), testTenantID, "http.foo.v1")
	fatalIfErr(t, err, "History")
	fatalUnless(t, len(evs) == 1, "History: len = %d, want 1", len(evs))
	errUnless(t, evs[0].From.IsZero(), "History: initial event From must be the zero sentinel")
	errUnless(t, evs[0].To == registry.StateSubmitted(), "History: initial event To = %s, want submitted", evs[0].To)
	errUnless(t, evs[0].Seq == 1, "History: initial event Seq = %d, want 1", evs[0].Seq)
	errUnless(t, evs[0].Actor == "alice", "History: initial event Actor = %q, want alice", evs[0].Actor)
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
	assertCode(t, dupErr, errcode.ErrRegistrationDuplicate, "Create duplicate")
}

func conformCreateMissingFieldRejected(t *testing.T, factory RegistryFactory) {
	t.Helper()
	repo, txRunner, cleanup := factory(t)
	t.Cleanup(cleanup)

	err := txRunner.RunInTx(context.Background(), func(ctx context.Context) error {
		_, e := repo.Create(ctx, testTenantID, registry.SubmitInput{ID: "", Kind: "http", Submitter: "alice"})
		return e
	})
	assertCode(t, err, errcode.ErrValidationFailed, "Create missing field")
}

func conformTransitionLegal(t *testing.T, factory RegistryFactory) {
	t.Helper()
	repo, txRunner, cleanup := factory(t)
	t.Cleanup(cleanup)

	create(t, txRunner, repo, testTenantID, submitInput("http.foo.v1"))
	got, err := transition(t, txRunner, repo, testTenantID, registry.AdvanceInput{
		ID: "http.foo.v1", To: registry.StateProbing(), Actor: "system",
	})
	fatalIfErr(t, err, "Transition")
	errUnless(t, got.State == registry.StateProbing(), "Transition: state = %s, want probing", got.State)

	evs, err := repo.History(context.Background(), testTenantID, "http.foo.v1")
	fatalIfErr(t, err, "History")
	fatalUnless(t, len(evs) == 2, "History: len = %d, want 2", len(evs))
	errUnless(t, evs[1].From == registry.StateSubmitted(), "History: event[1] From = %s, want submitted", evs[1].From)
	errUnless(t, evs[1].To == registry.StateProbing(), "History: event[1] To = %s, want probing", evs[1].To)
	errUnless(t, evs[1].Seq == 2, "History: event[1] Seq = %d, want 2", evs[1].Seq)
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
	assertCode(t, err, errcode.ErrRegistrationInvalidTransition, "Transition illegal")

	// No half-write: still submitted, history unchanged (length 1).
	got, _, _ := repo.Get(context.Background(), testTenantID, "http.foo.v1")
	errUnless(t, got.State == registry.StateSubmitted(), "Transition illegal: state must stay submitted, got %s", got.State)
	evs, _ := repo.History(context.Background(), testTenantID, "http.foo.v1")
	errUnless(t, len(evs) == 1, "Transition illegal: history must stay length 1, got %d", len(evs))
}

func conformTransitionUnknownID(t *testing.T, factory RegistryFactory) {
	t.Helper()
	repo, txRunner, cleanup := factory(t)
	t.Cleanup(cleanup)

	_, err := transition(t, txRunner, repo, testTenantID, registry.AdvanceInput{
		ID: "missing", To: registry.StateProbing(), Actor: "system",
	})
	assertCode(t, err, errcode.ErrRegistrationNotFound, "Transition unknown id")
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
		fatalIfErr(t, err, "Transition to "+to.String())
	}
	got, ok, err := repo.Get(context.Background(), testTenantID, "http.foo.v1")
	fatalIfErr(t, err, "Get")
	fatalUnless(t, ok, "Get: want ok=true")
	errUnless(t, got.State == registry.StateApproved(), "Approver: state = %s, want approved", got.State)
	errUnless(t, got.Approver == "admin", "Approver: approver = %q, want admin", got.Approver)
}

func conformGetUnknown(t *testing.T, factory RegistryFactory) {
	t.Helper()
	repo, _, cleanup := factory(t)
	t.Cleanup(cleanup)

	got, ok, err := repo.Get(context.Background(), testTenantID, "nobody")
	fatalIfErr(t, err, "Get unknown")
	errUnless(t, !ok, "Get unknown: want ok=false")
	errUnless(t, got.ID == "" && got.State.IsZero(), "Get unknown: want the zero registration, got %+v", got)
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
	fatalIfErr(t, err, "List page1")
	fatalUnless(t, len(page) == 3, "List page1: len = %d, want 3 (N+1)", len(page))
	errUnless(t, page[0].ID == "a" && page[1].ID == "b" && page[2].ID == "c",
		"List page1: ids = [%s %s %s], want [a b c]", page[0].ID, page[1].ID, page[2].ID)

	// Page 2: cursor after the last visible id "b" → returns c,d (< FetchLimit → no more).
	page2, err := repo.List(ctx, testTenantID, query.ListParams{Limit: 2, Sort: idASC, CursorValues: []any{"b"}}, ports.ListFilter{})
	fatalIfErr(t, err, "List page2")
	fatalUnless(t, len(page2) == 2, "List page2: len = %d, want 2", len(page2))
	errUnless(t, page2[0].ID == "c" && page2[1].ID == "d",
		"List page2: ids = [%s %s], want [c d]", page2[0].ID, page2[1].ID)
}

func conformListEmpty(t *testing.T, factory RegistryFactory) {
	t.Helper()
	repo, _, cleanup := factory(t)
	t.Cleanup(cleanup)

	page, err := repo.List(context.Background(), testTenantID, query.ListParams{Limit: 10, Sort: idASC}, ports.ListFilter{})
	fatalIfErr(t, err, "List empty")
	errUnless(t, len(page) == 0, "List empty: want 0 rows, got %d", len(page))
}

func conformListStateFilter(t *testing.T, factory RegistryFactory) {
	t.Helper()
	repo, txRunner, cleanup := factory(t)
	t.Cleanup(cleanup)

	create(t, txRunner, repo, testTenantID, submitInput("a"))
	create(t, txRunner, repo, testTenantID, submitInput("b"))
	_, err := transition(t, txRunner, repo, testTenantID, registry.AdvanceInput{ID: "b", To: registry.StateProbing(), Actor: "system"})
	fatalIfErr(t, err, "Transition b→probing")
	ctx := context.Background()

	// submitted filter → only "a".
	subPage, err := repo.List(ctx, testTenantID, query.ListParams{Limit: 10, Sort: idASC}, ports.ListFilter{State: registry.StateSubmitted()})
	fatalIfErr(t, err, "List submitted")
	fatalUnless(t, len(subPage) == 1, "List submitted: len = %d, want 1", len(subPage))
	errUnless(t, subPage[0].ID == "a", "List submitted: id = %q, want a", subPage[0].ID)

	// probing filter → only "b".
	probePage, err := repo.List(ctx, testTenantID, query.ListParams{Limit: 10, Sort: idASC}, ports.ListFilter{State: registry.StateProbing()})
	fatalIfErr(t, err, "List probing")
	fatalUnless(t, len(probePage) == 1, "List probing: len = %d, want 1", len(probePage))
	errUnless(t, probePage[0].ID == "b", "List probing: id = %q, want b", probePage[0].ID)

	// a state with no rows → empty.
	nonePage, err := repo.List(ctx, testTenantID, query.ListParams{Limit: 10, Sort: idASC}, ports.ListFilter{State: registry.StateApproved()})
	fatalIfErr(t, err, "List approved")
	errUnless(t, len(nonePage) == 0, "List approved: want 0 rows, got %d", len(nonePage))
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
		fatalIfErr(t, err, "Transition to "+to.String())
	}

	evs, err := repo.History(context.Background(), testTenantID, "r1")
	fatalIfErr(t, err, "History")
	fatalUnless(t, len(evs) == 5, "History: len = %d, want 5 (submit + 4 transitions)", len(evs))
	for i, ev := range evs {
		errUnless(t, ev.Seq == i+1, "History: event[%d] Seq = %d, want %d (1-based contiguous)", i, ev.Seq, i+1)
	}
}

// conformHistoryUnknownEmpty pins the #2388 / F16 divergence: History on an
// unknown id returns a no-events result on both stores. Asserted with wantEmpty
// (NOT a nil-specific check): the port godoc states callers MUST NOT distinguish
// a nil from an empty slice, so mem (nil) and PG (empty slice) are both
// contract-conformant and the suite must treat them as equivalent.
func conformHistoryUnknownEmpty(t *testing.T, factory RegistryFactory) {
	t.Helper()
	repo, _, cleanup := factory(t)
	t.Cleanup(cleanup)

	evs, err := repo.History(context.Background(), testTenantID, "nobody")
	fatalIfErr(t, err, "History unknown")
	errUnless(t, len(evs) == 0, "History unknown: want a no-events result (nil or empty), got len %d", len(evs))
}

func conformCrossTenantIsolation(t *testing.T, factory RegistryFactory) {
	t.Helper()
	repo, txRunner, cleanup := factory(t)
	t.Cleanup(cleanup)

	create(t, txRunner, repo, testTenantID, registry.SubmitInput{ID: "shared.id", Kind: "event", Submitter: "carol"})
	ctx := context.Background()

	// Tenant B cannot see tenant A's row by Get / List / History.
	_, ok, err := repo.Get(ctx, testTenantIDOther, "shared.id")
	fatalIfErr(t, err, "Get cross-tenant")
	errUnless(t, !ok, "Get cross-tenant: tenant B must not see tenant A's row")
	page, err := repo.List(ctx, testTenantIDOther, query.ListParams{Limit: 10, Sort: idASC}, ports.ListFilter{})
	fatalIfErr(t, err, "List cross-tenant")
	errUnless(t, len(page) == 0, "List cross-tenant: tenant B must see 0 rows, got %d", len(page))
	evs, err := repo.History(ctx, testTenantIDOther, "shared.id")
	fatalIfErr(t, err, "History cross-tenant")
	errUnless(t, len(evs) == 0, "History cross-tenant: tenant B must see 0 events, got %d", len(evs))

	// Per-tenant dedup: the same id is independently creatable under tenant B.
	create(t, txRunner, repo, testTenantIDOther, registry.SubmitInput{ID: "shared.id", Kind: "http", Submitter: "bob"})
	gotB, ok, err := repo.Get(ctx, testTenantIDOther, "shared.id")
	fatalIfErr(t, err, "Get tenant B")
	fatalUnless(t, ok, "Get tenant B: want ok=true")
	errUnless(t, gotB.Submitter == "bob", "Get tenant B: submitter = %q, want bob", gotB.Submitter)

	// Sanity: tenant A's row is still visible under its own tenant.
	gotA, ok, err := repo.Get(ctx, testTenantID, "shared.id")
	fatalIfErr(t, err, "Get tenant A")
	fatalUnless(t, ok, "Get tenant A: want ok=true")
	errUnless(t, gotA.Submitter == "carol", "Get tenant A: submitter = %q, want carol", gotA.Submitter)
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
	assertCode(t, cErr, errcode.ErrValidationFailed, "Create invalid tenant")

	tErr := txRunner.RunInTx(ctx, func(ctx context.Context) error {
		_, e := repo.Transition(ctx, zero, registry.AdvanceInput{ID: "x", To: registry.StateProbing(), Actor: "s"})
		return e
	})
	assertCode(t, tErr, errcode.ErrValidationFailed, "Transition invalid tenant")

	_, _, gErr := repo.Get(ctx, zero, "x")
	assertCode(t, gErr, errcode.ErrValidationFailed, "Get invalid tenant")
	_, lErr := repo.List(ctx, zero, query.ListParams{Limit: 10, Sort: idASC}, ports.ListFilter{})
	assertCode(t, lErr, errcode.ErrValidationFailed, "List invalid tenant")
	_, hErr := repo.History(ctx, zero, "x")
	assertCode(t, hErr, errcode.ErrValidationFailed, "History invalid tenant")
}
