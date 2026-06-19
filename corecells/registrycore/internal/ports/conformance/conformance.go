// Package conformance defines a ports.Registry contract acceptance suite shared
// by every ports.Registry implementation (mem, PG, future). Each implementation
// MUST call RunRegistryConformance from a _test.go in its own package; the
// archtest REGISTRY-CONFORMANCE-ENROLLMENT-01 enforces enrollment so the two
// stores (in-memory + PostgreSQL) can never silently diverge on the interface
// contract (#2388 — finding F16 from PR #2383: independent per-impl tests do not
// catch a boundary divergence such as History returning nil vs an empty slice).
//
// The suite asserts the *documented* port contract, not byte-level impl detail:
// History's "no events" result is checked with a len()==0 assertion (the port
// godoc states callers MUST NOT distinguish a nil from an empty slice), so the
// suite is purely additive — it forces semantic equivalence without mandating a
// behavior change in either store.
//
// Writes (Create/Transition) require an ambient tx (the port contract; the PG
// store asserts it), so the factory hands back a persistence.TxRunner the suite
// wraps every write in. Reads (Get/List/History) of a valid tenant are issued
// through getScoped/listScoped/historyScoped, which wrap each read in
// tenant.WithScope(ctx, t) + TxRunner.RunInTx — modeling the production PG read
// caller obligation (port godoc §"Read scoping under RLS": the RLS GUC is injected
// (SET LOCAL) only inside TxManager.RunInTx). The mem store's pass-through runner
// makes this a no-op wrapper, so the call shape is identical across stores. The
// suite runs under the test pool's owner role, so it asserts the tenant-parameter
// isolation common to both stores, not RLS fail-closed enforcement — the latter
// (which needs the restricted serving role) is covered by the PG RLS integration
// tests. The InvalidTenant sub-test calls reads directly (an invalid tenant cannot
// be scoped): the repo's tenant.Validate must fail-close before any store access.
//
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
// ref: corecells/configcore/internal/scopedread/scopedread.go (WithScope + RunInTx read funnel)
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
// persistence.TxRunner (used to provide the ambient tx writes require AND the
// tenant-scoped read tx — a pass-through for stores that need neither), and a
// cleanup func, for one sub-case. The factory is called once per sub-test; cleanup
// is registered via t.Cleanup. A store that needs no real ambient tx (e.g. the
// in-memory Registry) can pass outbox.DemoTxRunner{} as the TxRunner.
type RegistryFactory func(t *testing.T) (repo ports.Registry, txRunner persistence.TxRunner, cleanup func())

// RunRegistryConformance executes the full ports.Registry conformance suite.
func RunRegistryConformance(t *testing.T, factory RegistryFactory) {
	t.Helper()
	t.Run("Create_RecordsSubmitted", func(t *testing.T) { conformCreateRecordsSubmitted(t, factory) })
	t.Run("Create_DuplicateRejected", func(t *testing.T) { conformCreateDuplicateRejected(t, factory) })
	t.Run("Create_MissingFieldRejected", func(t *testing.T) { conformCreateMissingFieldRejected(t, factory) })
	t.Run("Transition_LegalAdvancesAndAppends", func(t *testing.T) { conformTransitionLegal(t, factory) })
	t.Run("Transition_IllegalRejectedNoMutation", func(t *testing.T) { conformTransitionIllegal(t, factory) })
	t.Run("Transition_SelfTransitionRejected", func(t *testing.T) { conformTransitionSelf(t, factory) })
	t.Run("Transition_TerminalRejected", func(t *testing.T) { conformTransitionTerminal(t, factory) })
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

// getScoped reads (t, id) inside a tenant-scoped tx (tenant.WithScope + RunInTx),
// modeling the production PG read caller obligation; mem's pass-through runner makes
// it a no-op wrapper. A read error fails the sub-test (no valid-tenant read should
// error); callers assert on the (registration, ok) result.
func getScoped(
	t *testing.T, txRunner persistence.TxRunner, repo ports.Registry, tn tenant.TenantID, id string,
) (registry.ContractRegistration, bool) {
	t.Helper()
	var (
		out registry.ContractRegistration
		ok  bool
	)
	err := txRunner.RunInTx(tenant.WithScope(context.Background(), tn), func(ctx context.Context) error {
		var e error
		out, ok, e = repo.Get(ctx, tn, id)
		return e
	})
	fatalIfErr(t, err, "Get "+id)
	return out, ok
}

// listScoped is getScoped's List counterpart (tenant-scoped read tx).
func listScoped(
	t *testing.T, txRunner persistence.TxRunner, repo ports.Registry,
	tn tenant.TenantID, params query.ListParams, filter ports.ListFilter,
) []registry.ContractRegistration {
	t.Helper()
	var out []registry.ContractRegistration
	err := txRunner.RunInTx(tenant.WithScope(context.Background(), tn), func(ctx context.Context) error {
		var e error
		out, e = repo.List(ctx, tn, params, filter)
		return e
	})
	fatalIfErr(t, err, "List")
	return out
}

// historyScoped is getScoped's History counterpart (tenant-scoped read tx).
func historyScoped(
	t *testing.T, txRunner persistence.TxRunner, repo ports.Registry, tn tenant.TenantID, id string,
) []registry.RegistrationEvent {
	t.Helper()
	var out []registry.RegistrationEvent
	err := txRunner.RunInTx(tenant.WithScope(context.Background(), tn), func(ctx context.Context) error {
		var e error
		out, e = repo.History(ctx, tn, id)
		return e
	})
	fatalIfErr(t, err, "History "+id)
	return out
}

// submitInput is a small fixture builder for the common http submission shape.
func submitInput(id string) registry.SubmitInput {
	return registry.SubmitInput{ID: id, Kind: "http", Submitter: "alice"}
}

// pageParams builds a first-page ListParams with the canonical id-ASC sort.
func pageParams(limit int) query.ListParams {
	return query.ListParams{Limit: limit, Sort: idASC}
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

	got, ok := getScoped(t, txRunner, repo, testTenantID, "http.foo.v1")
	fatalUnless(t, ok, "Get: want ok=true for created registration")
	errUnless(t, got.State == registry.StateSubmitted(), "Get: state = %s, want submitted", got.State)
	errUnless(t, got.Submitter == "alice", "Get: submitter = %q, want alice", got.Submitter)

	// Initial migration event: From = zero sentinel, To = submitted, Seq = 1.
	evs := historyScoped(t, txRunner, repo, testTenantID, "http.foo.v1")
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

	// Every required field (id / kind / submitter) blank must be rejected with
	// ErrValidationFailed — the contract is per-field, not just the id. Validation
	// fires before any store write, so the shared "x" id never collides.
	cases := []struct {
		name string
		in   registry.SubmitInput
	}{
		{"missing-id", registry.SubmitInput{ID: "", Kind: "http", Submitter: "alice"}},
		{"missing-kind", registry.SubmitInput{ID: "x", Kind: "", Submitter: "alice"}},
		{"missing-submitter", registry.SubmitInput{ID: "x", Kind: "http", Submitter: ""}},
	}
	for _, tc := range cases {
		err := txRunner.RunInTx(context.Background(), func(ctx context.Context) error {
			_, e := repo.Create(ctx, testTenantID, tc.in)
			return e
		})
		assertCode(t, err, errcode.ErrValidationFailed, "Create "+tc.name)
	}
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

	evs := historyScoped(t, txRunner, repo, testTenantID, "http.foo.v1")
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
	got, ok := getScoped(t, txRunner, repo, testTenantID, "http.foo.v1")
	fatalUnless(t, ok, "Get after illegal transition: want ok=true")
	errUnless(t, got.State == registry.StateSubmitted(), "Transition illegal: state must stay submitted, got %s", got.State)
	evs := historyScoped(t, txRunner, repo, testTenantID, "http.foo.v1")
	errUnless(t, len(evs) == 1, "Transition illegal: history must stay length 1, got %d", len(evs))
}

// conformTransitionSelf asserts a self-transition (submitted → submitted) is
// rejected — the frozen legalTransitions table has no self-edges.
func conformTransitionSelf(t *testing.T, factory RegistryFactory) {
	t.Helper()
	repo, txRunner, cleanup := factory(t)
	t.Cleanup(cleanup)

	create(t, txRunner, repo, testTenantID, submitInput("self"))
	_, err := transition(t, txRunner, repo, testTenantID, registry.AdvanceInput{
		ID: "self", To: registry.StateSubmitted(), Actor: "system",
	})
	assertCode(t, err, errcode.ErrRegistrationInvalidTransition, "Transition self")
}

// conformTransitionTerminal drives a registration to the terminal rejected state
// (submitted → probing → rejected) and asserts any further transition out of it
// is rejected — terminal states have no outgoing edges.
func conformTransitionTerminal(t *testing.T, factory RegistryFactory) {
	t.Helper()
	repo, txRunner, cleanup := factory(t)
	t.Cleanup(cleanup)

	create(t, txRunner, repo, testTenantID, submitInput("term"))
	for _, to := range []registry.RegistrationState{registry.StateProbing(), registry.StateRejected()} {
		_, err := transition(t, txRunner, repo, testTenantID, registry.AdvanceInput{ID: "term", To: to, Actor: "system"})
		fatalIfErr(t, err, "Transition to "+to.String())
	}
	_, err := transition(t, txRunner, repo, testTenantID, registry.AdvanceInput{
		ID: "term", To: registry.StateProbing(), Actor: "system",
	})
	assertCode(t, err, errcode.ErrRegistrationInvalidTransition, "Transition out of terminal")
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
	got, ok := getScoped(t, txRunner, repo, testTenantID, "http.foo.v1")
	fatalUnless(t, ok, "Get: want ok=true")
	errUnless(t, got.State == registry.StateApproved(), "Approver: state = %s, want approved", got.State)
	errUnless(t, got.Approver == "admin", "Approver: approver = %q, want admin", got.Approver)
}

func conformGetUnknown(t *testing.T, factory RegistryFactory) {
	t.Helper()
	repo, txRunner, cleanup := factory(t)
	t.Cleanup(cleanup)

	got, ok := getScoped(t, txRunner, repo, testTenantID, "nobody")
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

	// Page 1: Limit=2 → FetchLimit=3; 4 rows → returns a,b,c (the +1 row signals hasMore).
	page := listScoped(t, txRunner, repo, testTenantID, pageParams(2), ports.ListFilter{})
	fatalUnless(t, len(page) == 3, "List page1: len = %d, want 3 (N+1)", len(page))
	errUnless(t, page[0].ID == "a" && page[1].ID == "b" && page[2].ID == "c",
		"List page1: ids = [%s %s %s], want [a b c]", page[0].ID, page[1].ID, page[2].ID)

	// Page 2: cursor after the last visible id "b" → returns c,d (< FetchLimit → no more).
	page2 := listScoped(t, txRunner, repo, testTenantID,
		query.ListParams{Limit: 2, Sort: idASC, CursorValues: []any{"b"}}, ports.ListFilter{})
	fatalUnless(t, len(page2) == 2, "List page2: len = %d, want 2", len(page2))
	errUnless(t, page2[0].ID == "c" && page2[1].ID == "d",
		"List page2: ids = [%s %s], want [c d]", page2[0].ID, page2[1].ID)
}

func conformListEmpty(t *testing.T, factory RegistryFactory) {
	t.Helper()
	repo, txRunner, cleanup := factory(t)
	t.Cleanup(cleanup)

	page := listScoped(t, txRunner, repo, testTenantID, pageParams(10), ports.ListFilter{})
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

	// submitted filter → only "a".
	subPage := listScoped(t, txRunner, repo, testTenantID, pageParams(10), ports.ListFilter{State: registry.StateSubmitted()})
	fatalUnless(t, len(subPage) == 1, "List submitted: len = %d, want 1", len(subPage))
	errUnless(t, subPage[0].ID == "a", "List submitted: id = %q, want a", subPage[0].ID)

	// probing filter → only "b".
	probePage := listScoped(t, txRunner, repo, testTenantID, pageParams(10), ports.ListFilter{State: registry.StateProbing()})
	fatalUnless(t, len(probePage) == 1, "List probing: len = %d, want 1", len(probePage))
	errUnless(t, probePage[0].ID == "b", "List probing: id = %q, want b", probePage[0].ID)

	// a state with no rows → empty.
	nonePage := listScoped(t, txRunner, repo, testTenantID, pageParams(10), ports.ListFilter{State: registry.StateApproved()})
	errUnless(t, len(nonePage) == 0, "List approved: want 0 rows, got %d", len(nonePage))

	// Zero-value ListFilter{} means "all states" (the port contract): a mixed-state
	// store must return every row, not silently treat the zero State as a predicate.
	allPage := listScoped(t, txRunner, repo, testTenantID, pageParams(10), ports.ListFilter{})
	fatalUnless(t, len(allPage) == 2, "List all-states: len = %d, want 2 (submitted a + probing b)", len(allPage))
	errUnless(t, allPage[0].ID == "a" && allPage[1].ID == "b",
		"List all-states: ids = [%s %s], want [a b]", allPage[0].ID, allPage[1].ID)
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

	evs := historyScoped(t, txRunner, repo, testTenantID, "r1")
	fatalUnless(t, len(evs) == 5, "History: len = %d, want 5 (submit + 4 transitions)", len(evs))
	for i, ev := range evs {
		errUnless(t, ev.Seq == i+1, "History: event[%d] Seq = %d, want %d (1-based contiguous)", i, ev.Seq, i+1)
	}
}

// conformHistoryUnknownEmpty pins the #2388 / F16 divergence: History on an
// unknown id returns a no-events result on both stores. Asserted with len()==0
// (NOT a nil-specific check): the port godoc states callers MUST NOT distinguish
// a nil from an empty slice, so mem (nil) and PG (empty slice) are both
// contract-conformant and the suite must treat them as equivalent.
func conformHistoryUnknownEmpty(t *testing.T, factory RegistryFactory) {
	t.Helper()
	repo, txRunner, cleanup := factory(t)
	t.Cleanup(cleanup)

	evs := historyScoped(t, txRunner, repo, testTenantID, "nobody")
	errUnless(t, len(evs) == 0, "History unknown: want a no-events result (nil or empty), got len %d", len(evs))
}

func conformCrossTenantIsolation(t *testing.T, factory RegistryFactory) {
	t.Helper()
	repo, txRunner, cleanup := factory(t)
	t.Cleanup(cleanup)

	create(t, txRunner, repo, testTenantID, registry.SubmitInput{ID: "shared.id", Kind: "event", Submitter: "carol"})

	// Tenant B cannot see tenant A's row by Get / List / History (each scoped to B).
	_, ok := getScoped(t, txRunner, repo, testTenantIDOther, "shared.id")
	errUnless(t, !ok, "Get cross-tenant: tenant B must not see tenant A's row")
	page := listScoped(t, txRunner, repo, testTenantIDOther, pageParams(10), ports.ListFilter{})
	errUnless(t, len(page) == 0, "List cross-tenant: tenant B must see 0 rows, got %d", len(page))
	evs := historyScoped(t, txRunner, repo, testTenantIDOther, "shared.id")
	errUnless(t, len(evs) == 0, "History cross-tenant: tenant B must see 0 events, got %d", len(evs))

	// Write-path isolation: tenant B cannot advance tenant A's registration. Probed
	// before tenant B creates its own shared.id below, so not-found is genuinely the
	// cross-tenant predicate, not a missing row.
	_, tErr := transition(t, txRunner, repo, testTenantIDOther, registry.AdvanceInput{
		ID: "shared.id", To: registry.StateProbing(), Actor: "attacker",
	})
	assertCode(t, tErr, errcode.ErrRegistrationNotFound, "Transition cross-tenant")

	// Per-tenant dedup: the same id is independently creatable under tenant B.
	create(t, txRunner, repo, testTenantIDOther, registry.SubmitInput{ID: "shared.id", Kind: "http", Submitter: "bob"})
	gotB, ok := getScoped(t, txRunner, repo, testTenantIDOther, "shared.id")
	fatalUnless(t, ok, "Get tenant B: want ok=true")
	errUnless(t, gotB.Submitter == "bob", "Get tenant B: submitter = %q, want bob", gotB.Submitter)

	// Sanity: tenant A's row is still visible under its own tenant.
	gotA, ok := getScoped(t, txRunner, repo, testTenantID, "shared.id")
	fatalUnless(t, ok, "Get tenant A: want ok=true")
	errUnless(t, gotA.Submitter == "carol", "Get tenant A: submitter = %q, want carol", gotA.Submitter)
}

// conformInvalidTenantRejected asserts every method rejects an invalid tenant with
// ErrValidationFailed — the typed tenant boundary fail-closes before any store
// access. The invalid set covers the full TenantID.Validate rejection contract
// (empty / reserved nil UUID / non-canonical), not just the empty symptom. Reads
// are issued directly (NOT through the scoped helpers): an invalid tenant cannot be
// scoped (tenant.WithScope of an invalid id is meaningless), and the repo's
// tenant.Validate must reject before any tx/store access regardless of scoping.
func conformInvalidTenantRejected(t *testing.T, factory RegistryFactory) {
	t.Helper()
	repo, txRunner, cleanup := factory(t)
	t.Cleanup(cleanup)

	invalids := []struct {
		name string
		tn   tenant.TenantID
	}{
		{"empty", tenant.TenantID("")},
		{"reserved-nil-uuid", tenant.TenantID("00000000-0000-0000-0000-000000000000")},
		{"uppercase-non-canonical", tenant.TenantID("AAAAAAAA-AAAA-AAAA-AAAA-AAAAAAAAAAAA")},
		{"malformed", tenant.TenantID("not-a-uuid")},
	}
	ctx := context.Background()
	for _, inv := range invalids {
		cErr := txRunner.RunInTx(ctx, func(ctx context.Context) error {
			_, e := repo.Create(ctx, inv.tn, submitInput("x"))
			return e
		})
		assertCode(t, cErr, errcode.ErrValidationFailed, "Create invalid tenant ("+inv.name+")")

		tErr := txRunner.RunInTx(ctx, func(ctx context.Context) error {
			_, e := repo.Transition(ctx, inv.tn, registry.AdvanceInput{ID: "x", To: registry.StateProbing(), Actor: "s"})
			return e
		})
		assertCode(t, tErr, errcode.ErrValidationFailed, "Transition invalid tenant ("+inv.name+")")

		_, _, gErr := repo.Get(ctx, inv.tn, "x")
		assertCode(t, gErr, errcode.ErrValidationFailed, "Get invalid tenant ("+inv.name+")")
		_, lErr := repo.List(ctx, inv.tn, pageParams(10), ports.ListFilter{})
		assertCode(t, lErr, errcode.ErrValidationFailed, "List invalid tenant ("+inv.name+")")
		_, hErr := repo.History(ctx, inv.tn, "x")
		assertCode(t, hErr, errcode.ErrValidationFailed, "History invalid tenant ("+inv.name+")")
	}
}
