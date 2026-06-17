package registry_test

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/framework/kernel/clock/clockmock"
	"github.com/ghbvf/gocell/framework/kernel/registry"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/pkg/errcode/errcodetest"
)

var testEpoch = time.Date(2026, 6, 17, 0, 0, 0, 0, time.UTC)

func newRegistrar(t *testing.T) (*registry.ContractRegistrar, *clockmock.FakeClock) {
	t.Helper()
	clk := clockmock.New(testEpoch)
	return registry.NewContractRegistrar(clk), clk
}

func mustSubmit(t *testing.T, r *registry.ContractRegistrar, id string) registry.ContractRegistration {
	t.Helper()
	reg, err := r.Submit(registry.SubmitInput{ID: id, Kind: "http", PayloadSchema: "schema-ref", Submitter: "cell-a"})
	require.NoError(t, err)
	return reg
}

func TestSubmit_CreatesAtSubmitted(t *testing.T) {
	t.Parallel()
	r, clk := newRegistrar(t)
	reg := mustSubmit(t, r, "reg-1")

	assert.Equal(t, "reg-1", reg.ID)
	assert.Equal(t, "http", reg.Kind)
	assert.Equal(t, "cell-a", reg.Submitter)
	assert.Equal(t, registry.StateSubmitted(), reg.State)
	assert.Empty(t, reg.Approver)
	assert.Equal(t, clk.Now(), reg.CreatedAt)
	assert.Equal(t, clk.Now(), reg.UpdatedAt)

	events, ok := r.Events("reg-1")
	require.True(t, ok)
	require.Len(t, events, 1)
	assert.Equal(t, 1, events[0].Seq)
	assert.True(t, events[0].From.IsZero(), "initial event From must be the zero sentinel")
	assert.Equal(t, registry.StateSubmitted(), events[0].To)
	assert.Equal(t, "cell-a", events[0].Actor)
	assert.Equal(t, 1, r.Count())
}

func TestSubmit_DuplicateID(t *testing.T) {
	t.Parallel()
	r, _ := newRegistrar(t)
	mustSubmit(t, r, "dup")
	_, err := r.Submit(registry.SubmitInput{ID: "dup", Kind: "event", Submitter: "cell-b"})
	errcodetest.AssertCode(t, err, errcode.ErrRegistrationDuplicate)
	assert.Equal(t, 1, r.Count(), "duplicate submit must not create a second registration")
}

func TestSubmit_EmptyFields(t *testing.T) {
	t.Parallel()
	r, _ := newRegistrar(t)
	cases := []registry.SubmitInput{
		{ID: "", Kind: "http", Submitter: "cell-a"},
		{ID: "x", Kind: "", Submitter: "cell-a"},
		{ID: "x", Kind: "http", Submitter: ""},
	}
	for _, in := range cases {
		_, err := r.Submit(in)
		errcodetest.AssertCode(t, err, errcode.ErrValidationFailed)
	}
	assert.Equal(t, 0, r.Count())
}

func TestAdvance_FullLifecycle(t *testing.T) {
	t.Parallel()
	r, _ := newRegistrar(t)
	mustSubmit(t, r, "reg-1")
	path := []registry.RegistrationState{
		registry.StateProbing(), registry.StateConformant(),
		registry.StatePendingApproval(), registry.StateApproved(),
		registry.StateActive(), registry.StateRetired(),
	}
	for _, to := range path {
		reg, err := r.Advance("reg-1", to, "admin", "ok")
		require.NoError(t, err, "advance to %q", to)
		assert.Equal(t, to, reg.State)
	}
	events, ok := r.Events("reg-1")
	require.True(t, ok)
	assert.Len(t, events, len(path)+1) // +1 for the initial submit event
}

// TestAdvance_SubmittedToActivate_Rejected is acceptance scenario 1: a submitted
// contract cannot be activated without approval; the call is rejected, state is
// unchanged, and NO event is appended (fail-closed, no half-write).
func TestAdvance_SubmittedToActivate_Rejected(t *testing.T) {
	t.Parallel()
	r, _ := newRegistrar(t)
	mustSubmit(t, r, "reg-1")

	_, err := r.Advance("reg-1", registry.StateActive(), "attacker", "bypass")
	errcodetest.AssertCode(t, err, errcode.ErrRegistrationInvalidTransition)

	reg, ok := r.Get("reg-1")
	require.True(t, ok)
	assert.Equal(t, registry.StateSubmitted(), reg.State, "state must be unchanged after rejected transition")

	events, ok := r.Events("reg-1")
	require.True(t, ok)
	assert.Len(t, events, 1, "no event must be appended on a rejected transition (no half-write)")
}

func TestAdvance_NotFound(t *testing.T) {
	t.Parallel()
	r, _ := newRegistrar(t)
	_, err := r.Advance("nope", registry.StateProbing(), "admin", "")
	errcodetest.AssertCode(t, err, errcode.ErrRegistrationNotFound)
}

func TestAdvance_SetsApproverOnApprove(t *testing.T) {
	t.Parallel()
	r, _ := newRegistrar(t)
	mustSubmit(t, r, "reg-1")
	for _, to := range []registry.RegistrationState{registry.StateProbing(), registry.StateConformant(), registry.StatePendingApproval()} {
		reg, err := r.Advance("reg-1", to, "system", "")
		require.NoError(t, err)
		assert.Empty(t, reg.Approver, "Approver must not be set before approval (state %q)", to)
	}
	reg, err := r.Advance("reg-1", registry.StateApproved(), "admin-42", "looks good")
	require.NoError(t, err)
	assert.Equal(t, "admin-42", reg.Approver)
}

func TestAdvance_StampsUpdatedAt(t *testing.T) {
	t.Parallel()
	r, clk := newRegistrar(t)
	reg := mustSubmit(t, r, "reg-1")
	created := reg.CreatedAt

	clk.Advance(5 * time.Minute)
	advanced, err := r.Advance("reg-1", registry.StateProbing(), "system", "")
	require.NoError(t, err)
	assert.Equal(t, created, advanced.CreatedAt, "CreatedAt must not change on advance")
	assert.Equal(t, clk.Now(), advanced.UpdatedAt)
	assert.True(t, advanced.UpdatedAt.After(created), "UpdatedAt must advance with the clock")
}

// TestReplayEvents_ProjectionConsistent is acceptance scenario 3: folding the
// append-only event stream (from the zero sentinel) reproduces the live
// projection state — an honest cross-check that the independently-maintained
// projection equals the fold of the log.
func TestReplayEvents_ProjectionConsistent(t *testing.T) {
	t.Parallel()
	r, _ := newRegistrar(t)
	mustSubmit(t, r, "reg-1")
	lifecycle := []registry.RegistrationState{
		registry.StateProbing(), registry.StateConformant(),
		registry.StatePendingApproval(), registry.StateRejected(),
	}
	for _, to := range lifecycle {
		_, err := r.Advance("reg-1", to, "system", "")
		require.NoError(t, err)
	}

	events, ok := r.Events("reg-1")
	require.True(t, ok)
	require.NotEmpty(t, events)

	// Fold the event stream from the zero sentinel; each event's From must chain
	// to the prior event's To, and the final To is the derived current state.
	var derived registry.RegistrationState
	for i, e := range events {
		assert.Equal(t, i+1, e.Seq, "events must be 1-based and contiguous")
		assert.Equal(t, derived, e.From, "event %d From must chain from prior To", i)
		derived = e.To
	}

	live, ok := r.Get("reg-1")
	require.True(t, ok)
	assert.Equal(t, live.State, derived, "replayed state must equal the live projection")
}

func TestByState_Filters(t *testing.T) {
	t.Parallel()
	r, _ := newRegistrar(t)
	mustSubmit(t, r, "a")
	mustSubmit(t, r, "b")
	mustSubmit(t, r, "c")
	_, err := r.Advance("b", registry.StateProbing(), "system", "")
	require.NoError(t, err)

	submitted := r.ByState(registry.StateSubmitted())
	probing := r.ByState(registry.StateProbing())
	assert.Len(t, submitted, 2)
	assert.Len(t, probing, 1)
	assert.Equal(t, "b", probing[0].ID)
	assert.Empty(t, r.ByState(registry.StateApproved()))
}

func TestGet_NoAliasMutation(t *testing.T) {
	t.Parallel()
	r, _ := newRegistrar(t)
	mustSubmit(t, r, "reg-1")
	got, ok := r.Get("reg-1")
	require.True(t, ok)
	got.Kind = "tampered"
	got.Submitter = "tampered"

	again, ok := r.Get("reg-1")
	require.True(t, ok)
	assert.Equal(t, "http", again.Kind, "mutating a returned copy must not affect the registrar")
	assert.Equal(t, "cell-a", again.Submitter)
}

func TestEvents_DeepCopy(t *testing.T) {
	t.Parallel()
	r, _ := newRegistrar(t)
	mustSubmit(t, r, "reg-1")
	events, ok := r.Events("reg-1")
	require.True(t, ok)
	events[0].Actor = "tampered"

	again, ok := r.Events("reg-1")
	require.True(t, ok)
	assert.Equal(t, "cell-a", again[0].Actor, "mutating returned events must not affect the registrar")

	_, ok = r.Events("missing")
	assert.False(t, ok)
}

func TestGet_NotFound(t *testing.T) {
	t.Parallel()
	r, _ := newRegistrar(t)
	_, ok := r.Get("missing")
	assert.False(t, ok)
}

func TestAllIDs_Sorted(t *testing.T) {
	t.Parallel()
	r, _ := newRegistrar(t)
	for _, id := range []string{"c", "a", "b"} {
		mustSubmit(t, r, id)
	}
	assert.Equal(t, []string{"a", "b", "c"}, r.AllIDs())
}

// TestConcurrent_SubmitAdvance_Race fires concurrent Submit+Advance and asserts
// no data race (run with -race) and that each registration's event count matches
// the transitions applied (projection consistent under the lock).
func TestConcurrent_SubmitAdvance_Race(t *testing.T) {
	t.Parallel()
	r, _ := newRegistrar(t)
	const n = 32
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			id := fmt.Sprintf("reg-%d", i)
			_, err := r.Submit(registry.SubmitInput{ID: id, Kind: "http", Submitter: "cell"})
			require.NoError(t, err)
			_, err = r.Advance(id, registry.StateProbing(), "system", "")
			require.NoError(t, err)
			_ = r.ByState(registry.StateProbing())
		}(i)
	}
	wg.Wait()

	assert.Equal(t, n, r.Count())
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("reg-%d", i)
		events, ok := r.Events(id)
		require.True(t, ok)
		assert.Len(t, events, 2, "submit + one advance")
		reg, ok := r.Get(id)
		require.True(t, ok)
		assert.Equal(t, registry.StateProbing(), reg.State)
	}
}
