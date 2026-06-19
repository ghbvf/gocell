package mem

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/corecells/registrycore/internal/ports"
	"github.com/ghbvf/gocell/framework/kernel/cell/celltest"
	"github.com/ghbvf/gocell/framework/kernel/clock/clockmock"
	"github.com/ghbvf/gocell/framework/kernel/registry"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/pkg/query"
	"github.com/ghbvf/gocell/framework/pkg/tenant"
)

// idASC is the canonical sort used by the registry list interface (keyset id ASC).
var idASC = []query.SortColumn{{Name: "id", Direction: query.SortASC}}

var (
	testEpoch   = time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)
	testTenant  = tenant.TenantID("00000000-0000-0000-0000-000000000001")
	testTenantB = tenant.TenantID("00000000-0000-0000-0000-000000000002")
)

func newRegistry(t *testing.T) (*Registry, *clockmock.FakeClock) {
	t.Helper()
	clk := clockmock.New(testEpoch)
	return NewRegistry(clk), clk
}

// TestRegistry_RepoReady verifies the in-memory Registry always reports ready
// (MemStore convention — no external dependency).
func TestRegistry_RepoReady(t *testing.T) {
	r, _ := newRegistry(t)
	assert.NoError(t, r.RepoReady(context.Background()), "in-memory RepoReady must always return nil")
}

// TestRegistry_RepoReady_Conformance enrolls the mem Registry in the single-source
// readiness-conformance harness (CELL-REPO-READYZ-PROBE-01). broken=nil signals
// "no differentiated failure domain" — the in-mem store has no unreachable state,
// so the harness skips the broken sub-test.
func TestRegistry_RepoReady_Conformance(t *testing.T) {
	r, _ := newRegistry(t)
	celltest.RunRepoReadinessConformance(t, "registrycore-mem", r, nil)
}

func mustCreate(t *testing.T, r *Registry, tn tenant.TenantID, id, kind, submitter string) registry.ContractRegistration {
	t.Helper()
	reg, err := r.Create(context.Background(), tn, registry.SubmitInput{ID: id, Kind: kind, Submitter: submitter})
	require.NoError(t, err)
	return reg
}

func assertCode(t *testing.T, err error, want errcode.Code) {
	t.Helper()
	var ce *errcode.Error
	require.True(t, errors.As(err, &ce), "want *errcode.Error, got %v", err)
	assert.Equal(t, want, ce.Code)
}

func TestRegistry_Create_RecordsSubmitted(t *testing.T) {
	r, _ := newRegistry(t)
	reg := mustCreate(t, r, testTenant, "http.foo.v1", "http", "alice")

	assert.Equal(t, "http.foo.v1", reg.ID)
	assert.Equal(t, "http", reg.Kind)
	assert.Equal(t, "alice", reg.Submitter)
	assert.Equal(t, registry.StateSubmitted(), reg.State)
	assert.Equal(t, testEpoch, reg.CreatedAt)
	assert.Equal(t, testEpoch, reg.UpdatedAt)

	// Read-back via Get.
	got, ok, err := r.Get(context.Background(), testTenant, "http.foo.v1")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, reg, got)

	// Initial migration event: From = zero sentinel, To = submitted, Seq = 1.
	evs, err := r.History(context.Background(), testTenant, "http.foo.v1")
	require.NoError(t, err)
	require.Len(t, evs, 1)
	assert.True(t, evs[0].From.IsZero())
	assert.Equal(t, registry.StateSubmitted(), evs[0].To)
	assert.Equal(t, 1, evs[0].Seq)
	assert.Equal(t, "alice", evs[0].Actor)
}

func TestRegistry_Create_DuplicateRejected(t *testing.T) {
	r, _ := newRegistry(t)
	mustCreate(t, r, testTenant, "http.foo.v1", "http", "alice")

	_, err := r.Create(context.Background(), testTenant, registry.SubmitInput{ID: "http.foo.v1", Kind: "http", Submitter: "bob"})
	assertCode(t, err, errcode.ErrRegistrationDuplicate)
}

func TestRegistry_Create_MissingFieldRejected(t *testing.T) {
	r, _ := newRegistry(t)
	_, err := r.Create(context.Background(), testTenant, registry.SubmitInput{ID: "", Kind: "http", Submitter: "alice"})
	assertCode(t, err, errcode.ErrValidationFailed)
}

func TestRegistry_Transition_LegalAdvancesAndAppends(t *testing.T) {
	r, clk := newRegistry(t)
	mustCreate(t, r, testTenant, "http.foo.v1", "http", "alice")
	clk.Advance(time.Minute)

	got, err := r.Transition(context.Background(), testTenant, registry.AdvanceInput{
		ID: "http.foo.v1", To: registry.StateProbing(), Actor: "system",
	})
	require.NoError(t, err)
	assert.Equal(t, registry.StateProbing(), got.State)
	assert.Equal(t, testEpoch.Add(time.Minute), got.UpdatedAt)

	evs, err := r.History(context.Background(), testTenant, "http.foo.v1")
	require.NoError(t, err)
	require.Len(t, evs, 2)
	assert.Equal(t, registry.StateSubmitted(), evs[1].From)
	assert.Equal(t, registry.StateProbing(), evs[1].To)
	assert.Equal(t, 2, evs[1].Seq)
}

func TestRegistry_Transition_IllegalRejectedNoMutation(t *testing.T) {
	r, _ := newRegistry(t)
	mustCreate(t, r, testTenant, "http.foo.v1", "http", "alice")

	_, err := r.Transition(context.Background(), testTenant, registry.AdvanceInput{
		ID: "http.foo.v1", To: registry.StateActive(), Actor: "system", // submitted→active is illegal
	})
	assertCode(t, err, errcode.ErrRegistrationInvalidTransition)

	// No half-write: state unchanged, history still length 1.
	got, _, _ := r.Get(context.Background(), testTenant, "http.foo.v1")
	assert.Equal(t, registry.StateSubmitted(), got.State)
	evs, _ := r.History(context.Background(), testTenant, "http.foo.v1")
	assert.Len(t, evs, 1)
}

func TestRegistry_Transition_UnknownIDNotFound(t *testing.T) {
	r, _ := newRegistry(t)
	_, err := r.Transition(context.Background(), testTenant, registry.AdvanceInput{
		ID: "missing", To: registry.StateProbing(), Actor: "system",
	})
	assertCode(t, err, errcode.ErrRegistrationNotFound)
}

func TestRegistry_Transition_ApprovedRecordsApprover(t *testing.T) {
	r, _ := newRegistry(t)
	mustCreate(t, r, testTenant, "http.foo.v1", "http", "alice")
	for _, to := range []registry.RegistrationState{
		registry.StateProbing(), registry.StateConformant(), registry.StatePendingApproval(), registry.StateApproved(),
	} {
		_, err := r.Transition(context.Background(), testTenant, registry.AdvanceInput{ID: "http.foo.v1", To: to, Actor: "admin"})
		require.NoError(t, err)
	}
	got, _, _ := r.Get(context.Background(), testTenant, "http.foo.v1")
	assert.Equal(t, registry.StateApproved(), got.State)
	assert.Equal(t, "admin", got.Approver)
}

func TestRegistry_List_OrderedCursorPaginated(t *testing.T) {
	r, _ := newRegistry(t)
	for _, id := range []string{"c", "a", "b", "d"} {
		mustCreate(t, r, testTenant, id, "http", "alice")
	}
	ctx := context.Background()

	// First page: limit=2, FetchLimit=3. 4 items exist → returns a, b, c (the
	// caller trims to Limit=2 and builds the cursor for page 2 from item b).
	page, err := r.List(ctx, testTenant, query.ListParams{Limit: 2, Sort: idASC}, ports.ListFilter{})
	require.NoError(t, err)
	require.Len(t, page, 3) // FetchLimit = Limit+1 for N+1 hasMore detection
	assert.Equal(t, "a", page[0].ID)
	assert.Equal(t, "b", page[1].ID)
	assert.Equal(t, "c", page[2].ID) // the +1 row that signals hasMore

	// Next page with cursor after "b" (last visible item from page 1).
	// limit=2, FetchLimit=3, 2 remaining items (c, d) → returns c, d (< FetchLimit → no more).
	page2, err := r.List(ctx, testTenant, query.ListParams{Limit: 2, Sort: idASC, CursorValues: []any{"b"}}, ports.ListFilter{})
	require.NoError(t, err)
	require.Len(t, page2, 2)
	assert.Equal(t, "c", page2[0].ID)
	assert.Equal(t, "d", page2[1].ID)
}

func TestRegistry_List_Empty(t *testing.T) {
	r, _ := newRegistry(t)
	page, err := r.List(context.Background(), testTenant, query.ListParams{Limit: 10, Sort: idASC}, ports.ListFilter{})
	require.NoError(t, err)
	assert.Empty(t, page)
}

func TestRegistry_List_HasMoreNPlusOne(t *testing.T) {
	r, _ := newRegistry(t)
	for _, id := range []string{"a", "b", "c"} {
		mustCreate(t, r, testTenant, id, "http", "alice")
	}
	// Request FetchLimit (limit+1 = 3) to detect hasMore: all 3 returned → hasMore=true.
	params := query.ListParams{Limit: 2, Sort: idASC}
	page, err := r.List(context.Background(), testTenant, params, ports.ListFilter{})
	require.NoError(t, err)
	// FetchLimit=3, have 3 rows → returns 3 items; caller detects len>limit → hasMore.
	assert.Len(t, page, params.FetchLimit())
}

func TestRegistry_List_LimitTruncation(t *testing.T) {
	r, _ := newRegistry(t)
	for _, id := range []string{"a", "b", "c", "d", "e"} {
		mustCreate(t, r, testTenant, id, "http", "alice")
	}
	page, err := r.List(context.Background(), testTenant, query.ListParams{Limit: 3, Sort: idASC}, ports.ListFilter{})
	require.NoError(t, err)
	// FetchLimit=4; 5 rows exist → returns first 4.
	assert.Len(t, page, 4)
	assert.Equal(t, "a", page[0].ID)
	assert.Equal(t, "d", page[3].ID)
}

func TestRegistry_List_CursorLastPageEmpty(t *testing.T) {
	r, _ := newRegistry(t)
	mustCreate(t, r, testTenant, "a", "http", "alice")

	// Cursor after the only item → empty next page.
	page, err := r.List(context.Background(), testTenant, query.ListParams{
		Limit: 10, Sort: idASC, CursorValues: []any{"a"},
	}, ports.ListFilter{})
	require.NoError(t, err)
	assert.Empty(t, page)
}

func TestRegistry_AllMethods_InvalidTenant(t *testing.T) {
	r, _ := newRegistry(t)
	zero := tenant.TenantID("")
	ctx := context.Background()

	_, cErr := r.Create(ctx, zero, registry.SubmitInput{ID: "x", Kind: "http", Submitter: "a"})
	assertCode(t, cErr, errcode.ErrValidationFailed)
	_, tErr := r.Transition(ctx, zero, registry.AdvanceInput{ID: "x", To: registry.StateProbing(), Actor: "s"})
	assertCode(t, tErr, errcode.ErrValidationFailed)
	_, _, gErr := r.Get(ctx, zero, "x")
	assertCode(t, gErr, errcode.ErrValidationFailed)
	_, lErr := r.List(ctx, zero, query.ListParams{Limit: 10, Sort: idASC}, ports.ListFilter{})
	assertCode(t, lErr, errcode.ErrValidationFailed)
	_, hErr := r.History(ctx, zero, "x")
	assertCode(t, hErr, errcode.ErrValidationFailed)
}

func TestRegistry_CrossTenantIsolation(t *testing.T) {
	r, _ := newRegistry(t)
	mustCreate(t, r, testTenant, "http.foo.v1", "event", "carol") // non-http kind + distinct submitter

	// Tenant B cannot see tenant A's row.
	_, ok, err := r.Get(context.Background(), testTenantB, "http.foo.v1")
	require.NoError(t, err)
	assert.False(t, ok)
	page, err := r.List(context.Background(), testTenantB, query.ListParams{Limit: 10, Sort: idASC}, ports.ListFilter{})
	require.NoError(t, err)
	assert.Empty(t, page)

	// The same id can be independently submitted in tenant B (per-tenant dedup).
	_, err = r.Create(context.Background(), testTenantB, registry.SubmitInput{ID: "http.foo.v1", Kind: "http", Submitter: "bob"})
	require.NoError(t, err)
}

// mustTransition advances a registration through one state transition in the
// test registry. Used to set up multi-state fixtures for filter tests.
func mustTransition(t *testing.T, r *Registry, tn tenant.TenantID, id string, to registry.RegistrationState) {
	t.Helper()
	_, err := r.Transition(context.Background(), tn, registry.AdvanceInput{
		ID: id, To: to, Actor: "system",
	})
	require.NoError(t, err)
}

// TestRegistry_List_StateFilter_SubmittedOnly: only submitted registrations are
// returned when filtering by submitted state.
func TestRegistry_List_StateFilter_SubmittedOnly(t *testing.T) {
	r, _ := newRegistry(t)
	ctx := context.Background()
	// Create two registrations: advance one to probing, leave the other submitted.
	mustCreate(t, r, testTenant, "a", "http", "alice")
	mustCreate(t, r, testTenant, "b", "http", "alice")
	mustTransition(t, r, testTenant, "b", registry.StateProbing())

	filter := ports.ListFilter{State: registry.StateSubmitted()}
	page, err := r.List(ctx, testTenant, query.ListParams{Limit: 10, Sort: idASC}, filter)
	require.NoError(t, err)
	require.Len(t, page, 1)
	assert.Equal(t, "a", page[0].ID)
	assert.Equal(t, registry.StateSubmitted(), page[0].State)
}

// TestRegistry_List_StateFilter_ProbingOnly: only probing registrations are
// returned when filtering by probing state.
func TestRegistry_List_StateFilter_ProbingOnly(t *testing.T) {
	r, _ := newRegistry(t)
	ctx := context.Background()
	mustCreate(t, r, testTenant, "a", "http", "alice")
	mustCreate(t, r, testTenant, "b", "http", "alice")
	mustTransition(t, r, testTenant, "b", registry.StateProbing())

	filter := ports.ListFilter{State: registry.StateProbing()}
	page, err := r.List(ctx, testTenant, query.ListParams{Limit: 10, Sort: idASC}, filter)
	require.NoError(t, err)
	require.Len(t, page, 1)
	assert.Equal(t, "b", page[0].ID)
	assert.Equal(t, registry.StateProbing(), page[0].State)
}

// TestRegistry_List_StateFilter_NoMatch: no rows for a state with no registrations.
func TestRegistry_List_StateFilter_NoMatch(t *testing.T) {
	r, _ := newRegistry(t)
	ctx := context.Background()
	mustCreate(t, r, testTenant, "a", "http", "alice") // only submitted

	filter := ports.ListFilter{State: registry.StateApproved()}
	page, err := r.List(ctx, testTenant, query.ListParams{Limit: 10, Sort: idASC}, filter)
	require.NoError(t, err)
	assert.Empty(t, page)
}

// TestRegistry_List_StateFilter_WithPagination: state filter + cursor pagination
// returns only the filtered state across pages correctly.
func TestRegistry_List_StateFilter_WithPagination(t *testing.T) {
	r, _ := newRegistry(t)
	ctx := context.Background()
	// Seed 4 registrations: a,b,c submitted; d advanced to probing.
	for _, id := range []string{"a", "b", "c", "d"} {
		mustCreate(t, r, testTenant, id, "http", "alice")
	}
	mustTransition(t, r, testTenant, "d", registry.StateProbing())

	filter := ports.ListFilter{State: registry.StateSubmitted()}
	// Page 1: limit=2, FetchLimit=3 submitted; have 3 (a,b,c) → returns a,b,c.
	page1, err := r.List(ctx, testTenant, query.ListParams{Limit: 2, Sort: idASC}, filter)
	require.NoError(t, err)
	// FetchLimit=3; 3 submitted rows → all 3 returned, caller detects hasMore.
	require.Len(t, page1, 3)
	assert.Equal(t, "a", page1[0].ID)
	assert.Equal(t, "b", page1[1].ID)
	assert.Equal(t, "c", page1[2].ID)

	// Page 2 with cursor after "b": should return c only (1 remaining submitted row).
	page2, err := r.List(ctx, testTenant, query.ListParams{
		Limit: 2, Sort: idASC, CursorValues: []any{"b"},
	}, filter)
	require.NoError(t, err)
	require.Len(t, page2, 1)
	assert.Equal(t, "c", page2[0].ID)
}
