package mem

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/framework/kernel/clock/clockmock"
	"github.com/ghbvf/gocell/framework/kernel/registry"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/pkg/tenant"
)

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
	// First page, limit 2 → a, b (id ascending).
	page, err := r.List(context.Background(), testTenant, "", 2)
	require.NoError(t, err)
	require.Len(t, page, 2)
	assert.Equal(t, "a", page[0].ID)
	assert.Equal(t, "b", page[1].ID)

	// Next page after "b" → c, d.
	page2, err := r.List(context.Background(), testTenant, "b", 2)
	require.NoError(t, err)
	require.Len(t, page2, 2)
	assert.Equal(t, "c", page2[0].ID)
	assert.Equal(t, "d", page2[1].ID)
}

func TestRegistry_CrossTenantIsolation(t *testing.T) {
	r, _ := newRegistry(t)
	mustCreate(t, r, testTenant, "http.foo.v1", "event", "carol") // non-http kind + distinct submitter

	// Tenant B cannot see tenant A's row.
	_, ok, err := r.Get(context.Background(), testTenantB, "http.foo.v1")
	require.NoError(t, err)
	assert.False(t, ok)
	page, err := r.List(context.Background(), testTenantB, "", 10)
	require.NoError(t, err)
	assert.Empty(t, page)

	// The same id can be independently submitted in tenant B (per-tenant dedup).
	_, err = r.Create(context.Background(), testTenantB, registry.SubmitInput{ID: "http.foo.v1", Kind: "http", Submitter: "bob"})
	require.NoError(t, err)
}
