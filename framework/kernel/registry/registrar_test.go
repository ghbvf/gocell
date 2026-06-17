package registry_test

import (
	"fmt"
	"sync"
	"sync/atomic"
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

// advanceClockStep is the clock advance between transitions in timestamp tests
// (TEST-TIME-LITERAL-01: site-specific test durations are package-level consts).
const advanceClockStep = 5 * time.Minute

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

// advance is a test convenience over the named-field AdvanceInput API.
func advance(r *registry.ContractRegistrar, id string, to registry.RegistrationState, actor, reason string) (
	registry.ContractRegistration, error,
) {
	return r.Advance(registry.AdvanceInput{ID: id, To: to, Actor: actor, Reason: reason})
}

// happyPath is the full main-path lifecycle (excluding the initial submitted state).
var happyPath = []registry.RegistrationState{
	registry.StateProbing(), registry.StateConformant(),
	registry.StatePendingApproval(), registry.StateApproved(),
	registry.StateActive(), registry.StateRetired(),
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
	assert.Equal(t, "reg-1", events[0].RegistrationID)
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
	for _, to := range happyPath {
		reg, err := advance(r, "reg-1", to, "admin", "ok")
		require.NoError(t, err, "advance to %q", to)
		assert.Equal(t, to, reg.State)
	}
	events, ok := r.Events("reg-1")
	require.True(t, ok)
	assert.Len(t, events, len(happyPath)+1) // +1 for the initial submit event
}

// TestAdvance_SubmittedToActivate_Rejected is acceptance scenario 1: a submitted
// contract cannot be activated without approval; the call is rejected, state is
// unchanged, and NO event is appended (fail-closed, no half-write).
func TestAdvance_SubmittedToActivate_Rejected(t *testing.T) {
	t.Parallel()
	r, _ := newRegistrar(t)
	mustSubmit(t, r, "reg-1")

	_, err := advance(r, "reg-1", registry.StateActive(), "attacker", "bypass")
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
	_, err := advance(r, "nope", registry.StateProbing(), "admin", "")
	errcodetest.AssertCode(t, err, errcode.ErrRegistrationNotFound)
}

// TestAdvance_EmptyFields verifies the attribution guard: an empty id or actor is
// rejected with ErrValidationFailed (symmetric with SubmitInput requiring a
// Submitter — an approve/retire with no actor is an audit-attribution hole).
func TestAdvance_EmptyFields(t *testing.T) {
	t.Parallel()
	r, _ := newRegistrar(t)
	mustSubmit(t, r, "reg-1")
	_, err := advance(r, "", registry.StateProbing(), "admin", "")
	errcodetest.AssertCode(t, err, errcode.ErrValidationFailed)
	_, err = advance(r, "reg-1", registry.StateProbing(), "", "no actor")
	errcodetest.AssertCode(t, err, errcode.ErrValidationFailed)

	// The rejected empty-actor advance must not have mutated state or appended.
	reg, ok := r.Get("reg-1")
	require.True(t, ok)
	assert.Equal(t, registry.StateSubmitted(), reg.State)
	events, _ := r.Events("reg-1")
	assert.Len(t, events, 1)
}

// TestAdvance_SetsApproverOnApprove verifies Approver is set ONLY on the approve
// transition, stays empty before it, and is NOT changed by later transitions
// (active/retired) — symmetric with TestTransition_ActiveOnlyFromApproved.
func TestAdvance_SetsApproverOnApprove(t *testing.T) {
	t.Parallel()
	r, _ := newRegistrar(t)
	mustSubmit(t, r, "reg-1")
	for _, to := range []registry.RegistrationState{registry.StateProbing(), registry.StateConformant(), registry.StatePendingApproval()} {
		reg, err := advance(r, "reg-1", to, "system", "")
		require.NoError(t, err)
		assert.Empty(t, reg.Approver, "Approver must not be set before approval (state %q)", to)
	}
	reg, err := advance(r, "reg-1", registry.StateApproved(), "admin-42", "looks good")
	require.NoError(t, err)
	assert.Equal(t, "admin-42", reg.Approver)

	// Subsequent transitions must NOT change the recorded approver.
	for _, to := range []registry.RegistrationState{registry.StateActive(), registry.StateRetired()} {
		reg, err := advance(r, "reg-1", to, "ops-1", "")
		require.NoError(t, err)
		assert.Equal(t, "admin-42", reg.Approver, "Approver must be unchanged after approval (state %q)", to)
	}
}

func TestAdvance_StampsUpdatedAt(t *testing.T) {
	t.Parallel()
	r, clk := newRegistrar(t)
	reg := mustSubmit(t, r, "reg-1")
	created := reg.CreatedAt

	clk.Advance(advanceClockStep)
	advanced, err := advance(r, "reg-1", registry.StateProbing(), "system", "")
	require.NoError(t, err)
	assert.Equal(t, created, advanced.CreatedAt, "CreatedAt must not change on advance")
	assert.Equal(t, clk.Now(), advanced.UpdatedAt)
	assert.True(t, advanced.UpdatedAt.After(created), "UpdatedAt must advance with the clock")
}

// TestReplayEvents_ProjectionConsistent is acceptance scenario 3: folding the
// append-only event stream (from the zero sentinel) reproduces the live
// projection — an honest cross-check that the independently-maintained
// projection equals the fold of the log. It drives the FULL happy path so the
// Approver field (set on the approve event) is also reconstructed from the log.
func TestReplayEvents_ProjectionConsistent(t *testing.T) {
	t.Parallel()
	r, _ := newRegistrar(t)
	mustSubmit(t, r, "reg-1")
	for _, to := range happyPath {
		actor := "system"
		if to == registry.StateApproved() {
			actor = "admin-7"
		}
		_, err := advance(r, "reg-1", to, actor, "")
		require.NoError(t, err)
	}

	events, ok := r.Events("reg-1")
	require.True(t, ok)
	require.NotEmpty(t, events)

	// Fold the event stream from the zero sentinel; each event's From must chain
	// to the prior event's To. State and Approver are reconstructed independently
	// of the live projection.
	var derivedState registry.RegistrationState
	var derivedApprover string
	for i, e := range events {
		assert.Equal(t, i+1, e.Seq, "events must be 1-based and contiguous")
		assert.Equal(t, "reg-1", e.RegistrationID)
		assert.Equal(t, derivedState, e.From, "event %d From must chain from prior To", i)
		derivedState = e.To
		if e.To == registry.StateApproved() {
			derivedApprover = e.Actor
		}
	}

	live, ok := r.Get("reg-1")
	require.True(t, ok)
	assert.Equal(t, live.State, derivedState, "replayed state must equal the live projection")
	assert.Equal(t, live.Approver, derivedApprover, "replayed approver must equal the live projection")
	assert.Equal(t, "admin-7", live.Approver)
}

func TestByState_Filters(t *testing.T) {
	t.Parallel()
	r, _ := newRegistrar(t)
	mustSubmit(t, r, "a")
	mustSubmit(t, r, "b")
	mustSubmit(t, r, "c")
	_, err := advance(r, "b", registry.StateProbing(), "system", "")
	require.NoError(t, err)

	submitted := r.ByState(registry.StateSubmitted())
	probing := r.ByState(registry.StateProbing())
	assert.Len(t, submitted, 2)
	assert.Len(t, probing, 1)
	assert.Equal(t, "b", probing[0].ID)
	assert.Empty(t, r.ByState(registry.StateApproved()))
}

// TestByState_ZeroStateEmpty pins the fail-closed contract: querying a
// forged/zero RegistrationState returns empty (never panics, never matches).
func TestByState_ZeroStateEmpty(t *testing.T) {
	t.Parallel()
	r, _ := newRegistrar(t)
	mustSubmit(t, r, "a")
	assert.Empty(t, r.ByState(registry.RegistrationState{}))
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

func TestGet_Missing(t *testing.T) {
	t.Parallel()
	r, _ := newRegistrar(t)
	_, ok := r.Get("missing")
	assert.False(t, ok, "Get returns (zero, false) for an unknown id — no error, by design")
}

func TestAllIDs_Sorted(t *testing.T) {
	t.Parallel()
	r, _ := newRegistrar(t)
	for _, id := range []string{"c", "a", "b"} {
		mustSubmit(t, r, id)
	}
	assert.Equal(t, []string{"a", "b", "c"}, r.AllIDs())
}

// TestConcurrent_SubmitAdvance_Race fires concurrent Submit+Advance on DISTINCT
// ids and asserts no data race (run with -race) and that each registration's
// event count matches the transitions applied (projection consistent).
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
			_, err = advance(r, id, registry.StateProbing(), "system", "")
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

// TestConcurrent_SameID_Advance fires N concurrent Advance calls against the SAME
// id (submitted→probing). The lock must serialize them: exactly one wins, the
// rest see the already-advanced state and fail with ErrRegistrationInvalidTransition
// (probing→probing is a self-loop). Run with -race to prove single-key locking.
func TestConcurrent_SameID_Advance(t *testing.T) {
	t.Parallel()
	r, _ := newRegistrar(t)
	mustSubmit(t, r, "shared")
	const n = 16
	var wg sync.WaitGroup
	var success, rejected int64
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			_, err := advance(r, "shared", registry.StateProbing(), "system", "")
			if err == nil {
				atomic.AddInt64(&success, 1)
			} else {
				errcodetest.AssertCode(t, err, errcode.ErrRegistrationInvalidTransition)
				atomic.AddInt64(&rejected, 1)
			}
		}()
	}
	wg.Wait()

	assert.Equal(t, int64(1), success, "exactly one concurrent advance must win")
	assert.Equal(t, int64(n-1), rejected, "the rest must be rejected as invalid transitions")
	events, ok := r.Events("shared")
	require.True(t, ok)
	assert.Len(t, events, 2, "submit + exactly one successful advance")
}
